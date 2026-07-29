package main

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"funkoverage/internal/funkutil"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/link"
	"github.com/cilium/ebpf/ringbuf"
	"github.com/cilium/ebpf/rlimit"

	"github.com/ianlancetaylor/demangle"
)

//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -type event -cc clang -cflags "-I/usr/include" -target amd64 tracer ./bpf/tracer.bpf.c
//go:generate go run github.com/cilium/ebpf/cmd/bpf2go -type event -cc clang -cflags "-I/usr/include" -target arm64 tracer ./bpf/tracer.bpf.c

// dynFuncHeadroom is extra capacity reserved in the "seen" dedup map at load
// time for functions discovered later via dlopen JIT instrumentation, on top
// of the statically enumerated function count.
const dynFuncHeadroom = 100000

// FuncRef identifies a single traced function by image path and (mangled) name.
type FuncRef struct {
	Image string
	Name  string
}

// Tracer owns one shim invocation's eBPF state: loaded program/maps, attached
// uprobe+tracepoint links, ringbuf reader goroutine, and the _called.log file.
// All resources are released by Stop, which is safe to call once.
type Tracer struct {
	funcs      []FuncRef           // global cookie/index → ref (parallel to attach order)
	imgSymbols map[string][]string // image path → symbols (for UprobeMulti)
	imgCookies map[string][]uint64 // image path → per-symbol cookies (global indices)

	objs           tracerObjects
	linksMu        sync.Mutex // guards links: Stop (caller goroutine) vs handleDynamicLoad (readLoop goroutine)
	links          []link.Link
	reader         *ringbuf.Reader
	logFile        *os.File
	funcsLogPath   string
	funcsLogFile   *os.File // lazily opened by ensureFuncsLog on first dlopen event
	rootPID        uint32
	seenCapacity   uint32 // "seen" map MaxEntries; dynamic cookies beyond this are dropped by the kernel
	capacityWarned bool
	includeRe      *regexp.Regexp // dlopen JIT filter, mirrors install-time --include
	excludeRe      *regexp.Regexp // dlopen JIT filter, mirrors install-time --exclude

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewTracer loads BPF objects sized for the given function set and opens the
// _called.log output file. It does NOT attach probes — call Start for that.
//
// `funcs` maps each ELF image path (main binary or shared library) to the
// list of symbol names to trace. The flattened order — images sorted, symbols
// in input order — defines the global cookie space used to identify events.
//
// `includePattern`/`excludePattern` are the source regex patterns from the
// install-time --include/--exclude filter (empty string = no filter). They
// are re-applied to functions discovered later via dlopen JIT
// instrumentation, matching the filtering already applied at enumeration
// time to statically discovered functions.
func NewTracer(funcs map[string][]string, logPath, includePattern, excludePattern string) (*Tracer, error) {
	funcs = normalizeFuncs(funcs)
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("tracer: remove memlock: %w", err)
	}

	var includeRe, excludeRe *regexp.Regexp
	if includePattern != "" {
		if re, err := regexp.Compile(includePattern); err == nil {
			includeRe = re
		} else {
			debugLog("funkoverage-shim: bad --include pattern %q: %v", includePattern, err)
		}
	}
	if excludePattern != "" {
		if re, err := regexp.Compile(excludePattern); err == nil {
			excludeRe = re
		} else {
			debugLog("funkoverage-shim: bad --exclude pattern %q: %v", excludePattern, err)
		}
	}

	refs, imgSyms, imgCookies := flattenFuncs(funcs)

	spec, err := loadTracer()
	if err != nil {
		return nil, fmt.Errorf("tracer: load BPF spec: %w", err)
	}
	// Resize the dedup map to one entry per global func index plus headroom
	// for dynamically loaded library functions.
	seenCapacity := uint32(len(refs) + dynFuncHeadroom)
	spec.Maps["seen"].MaxEntries = seenCapacity

	var objs tracerObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return nil, fmt.Errorf("tracer: load BPF objects: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("tracer: create log %s: %w", logPath, err)
	}

	// funcsLogFile is opened lazily on first dlopen-discovered function
	// (see ensureFuncsLog) — dlopen firing is rare, and creating this file
	// eagerly on every run leaves a near-always-empty file behind forever.
	funcsLogPath := strings.Replace(logPath, "_called.log", "_functions.log", 1)

	return &Tracer{
		funcs:        refs,
		imgSymbols:   imgSyms,
		imgCookies:   imgCookies,
		objs:         objs,
		logFile:      logFile,
		funcsLogPath: funcsLogPath,
		seenCapacity: seenCapacity,
		includeRe:    includeRe,
		excludeRe:    excludeRe,
	}, nil
}

// matchesFilter reports whether demangled passes the install-time
// --include/--exclude filter: include must match if set, exclude must not
// match. Mirrors FuncFilter.Match in cmd/enumerate.go (unavailable here —
// separate package main).
func (t *Tracer) matchesFilter(demangled string) bool {
	if t.includeRe != nil && !t.includeRe.MatchString(demangled) {
		return false
	}
	if t.excludeRe != nil && t.excludeRe.MatchString(demangled) {
		return false
	}
	return true
}

// ensureFuncsLog opens the per-run dynamic functions log on first use.
// Called only from handleDynamicLoad (readLoop goroutine), so no locking
// is needed — Stop only touches funcsLogFile after readLoop has exited.
func (t *Tracer) ensureFuncsLog() (*os.File, error) {
	if t.funcsLogFile != nil {
		return t.funcsLogFile, nil
	}
	f, err := os.Create(t.funcsLogPath)
	if err != nil {
		return nil, err
	}
	t.funcsLogFile = f
	return f, nil
}

// normalizeFuncs defaults a nil/empty funcs map to an empty (non-nil) map,
// supporting pure runtime dynamic-library tracing: a binary with no static
// functions of its own, relying entirely on dlopen'd plugins for coverage.
func normalizeFuncs(funcs map[string][]string) map[string][]string {
	if len(funcs) == 0 {
		return make(map[string][]string)
	}
	return funcs
}

// flattenFuncs builds the global cookie space. Images are sorted so that
// repeated invocations against the same input produce identical cookies —
// useful for debugging and for cross-run analysis.
func flattenFuncs(funcs map[string][]string) ([]FuncRef, map[string][]string, map[string][]uint64) {
	images := slices.Sorted(maps.Keys(funcs))

	var refs []FuncRef
	imgSyms := make(map[string][]string, len(images))
	imgCookies := make(map[string][]uint64, len(images))

	for _, img := range images {
		names := funcs[img]
		cookies := make([]uint64, 0, len(names))
		syms := make([]string, 0, len(names))
		for _, name := range names {
			cookies = append(cookies, uint64(len(refs)))
			syms = append(syms, name)
			refs = append(refs, FuncRef{Image: img, Name: name})
		}
		imgSyms[img] = syms
		imgCookies[img] = cookies
	}
	return refs, imgSyms, imgCookies
}

// Start seeds the watched-pid set with rootPID, attaches all uprobes plus
// the fork tracepoint, and spins up the ringbuf reader goroutine. It MUST
// be called before unblocking the traced child — otherwise early function
// calls are lost.
//
// Attach is synchronous: when Start returns nil, all probes are live in the
// kernel.
func (t *Tracer) Start(rootPID uint32) error {
	one := uint8(1)
	if err := t.objs.Watched.Update(&rootPID, &one, ebpf.UpdateAny); err != nil {
		return fmt.Errorf("tracer: seed watched pid %d: %w", rootPID, err)
	}
	t.rootPID = rootPID

	for img, cookies := range t.imgCookies {
		ex, err := link.OpenExecutable(img)
		if err != nil {
			// Some libraries lack the execute bit (packaging bug); skip rather than abort.
			debugLog("funkoverage-shim: skipping %s: %v", img, err)
			continue
		}
		l, err := ex.UprobeMulti(t.imgSymbols[img], t.objs.TraceUprobe, &link.UprobeMultiOptions{
			Cookies: cookies,
		})
		if err != nil {
			debugLog("funkoverage-shim: skipping uprobes on %s: %v", img, err)
			continue
		}
		t.addLink(l)
	}

	fl, err := link.Tracepoint("sched", "sched_process_fork", t.objs.TraceFork, nil)
	if err != nil {
		return fmt.Errorf("tracer: attach fork tracepoint: %w", err)
	}
	t.addLink(fl)

	// Dynamically locate and attach uretprobe to dlopen
	libcPath, err := findLibcPath(rootPID)
	if err == nil {
		ex, err := link.OpenExecutable(libcPath)
		if err == nil {
			l, err := ex.Uretprobe("dlopen", t.objs.TraceDlopenReturn, nil)
			if err == nil {
				t.addLink(l)
			} else {
				debugLog("funkoverage-shim: error attaching uretprobe to dlopen: %v", err)
			}
		} else {
			debugLog("funkoverage-shim: error opening %s: %v", libcPath, err)
		}
	} else {
		debugLog("funkoverage-shim: error finding libc path: %v", err)
	}

	rd, err := ringbuf.NewReader(t.objs.Events)
	if err != nil {
		return fmt.Errorf("tracer: open ringbuf: %w", err)
	}
	t.reader = rd

	t.wg.Add(1)
	go t.readLoop()

	return nil
}

// readLoop drains the ringbuf and writes one CALLED line per unique event.
// Exits when Read returns an error — either ringbuf.ErrClosed (Stop closed
// the reader) or os.ErrDeadlineExceeded (Stop set a drain deadline and the
// queue emptied).
func (t *Tracer) readLoop() {
	defer t.wg.Done()
	for {
		record, err := t.reader.Read()
		if err != nil {
			debugLog("Go readLoop: reader error: %v", err)
			return
		}
		if len(record.RawSample) < 4 {
			debugLog("Go readLoop: raw sample too short: %d", len(record.RawSample))
			continue
		}
		idx := binary.LittleEndian.Uint32(record.RawSample[:4])
		debugLog("Go readLoop: read event idx: %x", idx)
		if idx == 0xFFFFFFFF {
			t.handleDynamicLoad(t.rootPID)
			continue
		}
		if idx >= uint32(len(t.funcs)) {
			debugLog("Go readLoop: idx %x out of bounds (len: %d)", idx, len(t.funcs))
			continue
		}
		ref := t.funcs[idx]
		name := demangle.Filter(funkutil.StripVersion(ref.Name))
		fmt.Fprintf(t.logFile, "CALLED %s %s\n", ref.Image, name)
	}
}

// Stop tears down the tracer in this order:
//
//  1. Detach all links — stops new events from queuing.
//  2. Close reader after a delay — allows in-flight ringbuf events to drain
//     into the reader goroutine while probes are still attached (brief window).
//  3. Wait for the reader goroutine to exit.
//  4. Close log + release BPF objects.
const drainDelay = 100 * time.Millisecond

// addLink appends l to t.links under linksMu — safe to call from the
// readLoop goroutine (handleDynamicLoad) concurrently with Stop.
func (t *Tracer) addLink(l link.Link) {
	t.linksMu.Lock()
	t.links = append(t.links, l)
	t.linksMu.Unlock()
}

func (t *Tracer) Stop() error {
	t.stopOnce.Do(func() {
		t.linksMu.Lock()
		links := t.links
		t.links = nil
		t.linksMu.Unlock()
		for _, l := range links {
			_ = l.Close()
		}

		if t.reader != nil {
			go func() {
				time.Sleep(drainDelay)
				_ = t.reader.Close()
			}()
		}
		t.wg.Wait()

		if t.logFile != nil {
			_ = t.logFile.Close()
		}
		if t.funcsLogFile != nil {
			_ = t.funcsLogFile.Close()
		}
		t.objs.Close()
	})
	return nil
}

// hasSymbol reports whether the ELF file at path exports the given symbol
// name, checking dynamic symbols first (the common case for shared libs)
// then the full symbol table.
func hasSymbol(path, name string) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	if syms, err := f.DynamicSymbols(); err == nil {
		for _, s := range syms {
			if s.Name == name {
				return true
			}
		}
	}
	if syms, err := f.Symbols(); err == nil {
		for _, s := range syms {
			if s.Name == name {
				return true
			}
		}
	}
	return false
}

// findLibcPath locates the shared library exporting dlopen for the given
// pid. On glibc >= 2.34 that's libc.so.6; on older glibc it's libdl.so.2 —
// so we verify the symbol is actually present rather than trusting that a
// path merely exists.
func findLibcPath(pid uint32) (string, error) {
	standardPaths := []string{
		"/usr/lib/x86_64-linux-gnu/libc.so.6",
		"/lib/x86_64-linux-gnu/libc.so.6",
		"/lib64/libc.so.6",
		"/usr/lib/aarch64-linux-gnu/libc.so.6",
		"/lib/aarch64-linux-gnu/libc.so.6",
		"/usr/lib/x86_64-linux-gnu/libdl.so.2",
		"/lib/x86_64-linux-gnu/libdl.so.2",
		"/lib64/libdl.so.2",
		"/usr/lib/aarch64-linux-gnu/libdl.so.2",
		"/lib/aarch64-linux-gnu/libdl.so.2",
	}
	for _, p := range standardPaths {
		if hasSymbol(p, "dlopen") {
			return p, nil
		}
	}

	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return "", err
	}
	lines := strings.SplitSeq(string(data), "\n")
	for line := range lines {
		if (strings.Contains(line, "libc.so.6") || strings.Contains(line, "libdl.so.2")) && strings.Contains(line, "r-xp") {
			parts := strings.Fields(line)
			if len(parts) >= 6 && hasSymbol(parts[5], "dlopen") {
				return parts[5], nil
			}
		}
	}
	return "", fmt.Errorf("dlopen symbol not found in standard paths or /proc/%d/maps", pid)
}

func getMappedSharedLibraries(pid uint32) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")
	seen := make(map[string]struct{})
	var libs []string
	for _, line := range lines {
		if strings.Contains(line, ".so") && strings.Contains(line, "r-xp") {
			parts := strings.Fields(line)
			if len(parts) >= 6 {
				path := parts[5]
				if _, dup := seen[path]; !dup {
					seen[path] = struct{}{}
					libs = append(libs, path)
				}
			}
		}
	}
	return libs, nil
}

func getSharedLibrarySymbols(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Union .dynsym and .symtab rather than falling back only on a hard
	// error: a present-but-stripped-down .symtab (common for shared libs —
	// glibc's libc.so.6 ships one that omits exported functions like
	// dlopen, which live only in .dynsym) would otherwise silently hide
	// real, callable functions instead of triggering the fallback.
	var syms []elf.Symbol
	if dynSyms, err := f.DynamicSymbols(); err == nil {
		syms = append(syms, dynSyms...)
	}
	if statSyms, err := f.Symbols(); err == nil {
		syms = append(syms, statSyms...)
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("no symbol table (dynamic or static) in %s", path)
	}

	var funcs []string
	seen := make(map[string]struct{})
	for _, sym := range syms {
		if elf.ST_TYPE(sym.Info) != elf.STT_FUNC {
			continue
		}
		if sym.Value == 0 || sym.Size == 0 {
			continue
		}
		if sym.Name == "" || !funcIsRelevant(sym.Name) {
			continue
		}
		if _, dup := seen[sym.Name]; dup {
			continue
		}
		seen[sym.Name] = struct{}{}
		funcs = append(funcs, sym.Name)
	}
	return funcs, nil
}

// libstdc\+\+ is matched as its own alternative, outside the shared trailing
// \b: "+" is not a word character, so a \b immediately after it can never
// match a following "." (both sides non-word) — under the combined pattern
// this alternative could never actually match a real "libstdc++.so*" path.
var systemLibRe = regexp.MustCompile(`(?i)(?:libc|libm|libpthread|librt|libdl|libthread_db|ld-linux|libgcc_s|libglib|libgobject|libgthread|libgio|libcap|libattr|libpcre|libselinux|libmount|libblkid|libuuid|libpam|libaudit|libdbus|libsystemd|libudev)\b|libstdc\+\+`)

func isSystemLib(path string) bool {
	return systemLibRe.MatchString(filepath.Base(path))
}

func funcIsRelevant(name string) bool {
	if name == "main" || name == "_init" || name == "_start" || name == ".plt" || name == ".plt.got" {
		return false
	}
	if strings.HasSuffix(name, "@plt") || strings.HasSuffix(name, "@plt.got") {
		return false
	}
	if strings.HasPrefix(name, "__") {
		return false
	}
	return true
}

func (t *Tracer) handleDynamicLoad(pid uint32) {
	libs, err := getMappedSharedLibraries(pid)
	if err != nil {
		debugLog("funkoverage-shim: error getting mapped libraries: %v", err)
		return
	}

	for _, lib := range libs {
		// Skip already instrumented images
		if _, exists := t.imgCookies[lib]; exists {
			continue
		}

		// Also skip system libraries to keep trace size reasonable
		if isSystemLib(lib) {
			continue
		}

		debugLog("funkoverage-shim: dynamic dlopen detected for %s. Instrumenting...", lib)

		syms, err := getSharedLibrarySymbols(lib)
		if err != nil {
			debugLog("funkoverage-shim: error reading symbols from %s: %v", lib, err)
			continue
		}

		if len(syms) == 0 {
			continue
		}

		if t.includeRe != nil || t.excludeRe != nil {
			filtered := syms[:0]
			for _, name := range syms {
				if t.matchesFilter(demangle.Filter(funkutil.StripVersion(name))) {
					filtered = append(filtered, name)
				}
			}
			syms = filtered
			if len(syms) == 0 {
				continue
			}
		}

		// The "seen" dedup map was sized once at BPF load time; cookies
		// beyond its capacity silently fail lookup in the kernel and the
		// call is dropped rather than recorded. Clip rather than overrun.
		clipped, warn := clipToCapacity(syms, len(t.funcs), t.seenCapacity)
		if warn {
			t.warnCapacityExhausted()
		}
		syms = clipped
		if len(syms) == 0 {
			continue
		}

		ex, err := link.OpenExecutable(lib)
		if err != nil {
			debugLog("funkoverage-shim: error opening %s: %v", lib, err)
			continue
		}

		funcsLog, err := t.ensureFuncsLog()
		if err != nil {
			debugLog("funkoverage-shim: error opening funcs log: %v", err)
			continue
		}

		var cookies []uint64
		var names []string
		for _, name := range syms {
			cookie := uint64(len(t.funcs))
			cookies = append(cookies, cookie)
			names = append(names, name)

			t.funcs = append(t.funcs, FuncRef{Image: lib, Name: name})

			// Write to functions log so report captures it as a total function!
			demangledName := demangle.Filter(funkutil.StripVersion(name))
			fmt.Fprintf(funcsLog, "FUNC %s %s\n", lib, demangledName)
		}

		l, err := ex.UprobeMulti(names, t.objs.TraceUprobe, &link.UprobeMultiOptions{
			Cookies: cookies,
		})
		if err != nil {
			debugLog("funkoverage-shim: skipping uprobes on %s: %v", lib, err)
			continue
		}

		t.addLink(l)
		t.imgSymbols[lib] = names
		t.imgCookies[lib] = cookies

		debugLog("funkoverage-shim: successfully instrumented %d functions in %s", len(syms), lib)
	}
}

// clipToCapacity trims syms to fit the remaining "seen" map capacity
// (seenCapacity - alreadyUsed), reporting whether any symbols were dropped
// as a result (either all of them, if capacity is already exhausted, or
// the tail beyond what fits).
func clipToCapacity(syms []string, alreadyUsed int, seenCapacity uint32) (clipped []string, warn bool) {
	remaining := int(seenCapacity) - alreadyUsed
	if remaining <= 0 {
		return nil, true
	}
	if len(syms) > remaining {
		return syms[:remaining], true
	}
	return syms, false
}

// warnCapacityExhausted reports (once, unconditionally — this is a real
// coverage-correctness gap, not a debug detail) that dynamic function
// capacity ran out and further dlopen'd functions will not be traced.
func (t *Tracer) warnCapacityExhausted() {
	if t.capacityWarned {
		return
	}
	t.capacityWarned = true
	fmt.Fprintf(os.Stderr, "funkoverage-shim: dynamic function capacity (%d) exhausted; some dlopen'd functions will not be traced\n", t.seenCapacity)
}

func debugLog(format string, args ...any) {
	if os.Getenv("FUNKOVERAGE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}
