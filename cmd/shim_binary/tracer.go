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

	objs         tracerObjects
	links        []link.Link
	reader       *ringbuf.Reader
	logFile      *os.File
	funcsLogFile *os.File
	rootPID      uint32

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewTracer loads BPF objects sized for the given function set and opens the
// _called.log output file. It does NOT attach probes — call Start for that.
//
// `funcs` maps each ELF image path (main binary or shared library) to the
// list of symbol names to trace. The flattened order — images sorted, symbols
// in input order — defines the global cookie space used to identify events.
func NewTracer(funcs map[string][]string, logPath string) (*Tracer, error) {
	// If funcs is empty, allow it for pure runtime dynamic library tracing
	if len(funcs) == 0 {
		funcs = make(map[string][]string)
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("tracer: remove memlock: %w", err)
	}

	refs, imgSyms, imgCookies := flattenFuncs(funcs)

	spec, err := loadTracer()
	if err != nil {
		return nil, fmt.Errorf("tracer: load BPF spec: %w", err)
	}
	// Resize the dedup map to one entry per global func index plus headroom
	// for dynamically loaded library functions.
	spec.Maps["seen"].MaxEntries = uint32(len(refs) + 100000)

	var objs tracerObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return nil, fmt.Errorf("tracer: load BPF objects: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("tracer: create log %s: %w", logPath, err)
	}

	funcsLogPath := strings.Replace(logPath, "_called.log", "_functions.log", 1)
	funcsLogFile, err := os.Create(funcsLogPath)
	if err != nil {
		logFile.Close()
		objs.Close()
		return nil, fmt.Errorf("tracer: create funcs log %s: %w", funcsLogPath, err)
	}

	return &Tracer{
		funcs:        refs,
		imgSymbols:   imgSyms,
		imgCookies:   imgCookies,
		objs:         objs,
		logFile:      logFile,
		funcsLogFile: funcsLogFile,
	}, nil
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
		t.links = append(t.links, l)
	}

	fl, err := link.Tracepoint("sched", "sched_process_fork", t.objs.TraceFork, nil)
	if err != nil {
		return fmt.Errorf("tracer: attach fork tracepoint: %w", err)
	}
	t.links = append(t.links, fl)

	// Dynamically locate and attach uretprobe to dlopen
	libcPath, err := findLibcPath(rootPID)
	if err == nil {
		ex, err := link.OpenExecutable(libcPath)
		if err == nil {
			l, err := ex.Uretprobe("dlopen", t.objs.TraceDlopenReturn, nil)
			if err == nil {
				t.links = append(t.links, l)
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

func (t *Tracer) Stop() error {
	t.stopOnce.Do(func() {
		for _, l := range t.links {
			_ = l.Close()
		}
		t.links = nil

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
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
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

	syms, err := f.Symbols()
	if err != nil {
		// Fall back to dynamic symbols if .symtab is stripped (common for shared libraries!)
		syms, err = f.DynamicSymbols()
		if err != nil {
			return nil, err
		}
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

var systemLibRe = regexp.MustCompile(`(?i)(?:libc|libm|libpthread|librt|libdl|libthread_db|ld-linux|libstdc\+\+|libgcc_s|libglib|libgobject|libgthread|libgio|libcap|libattr|libpcre|libselinux|libmount|libblkid|libuuid|libpam|libaudit|libdbus|libsystemd|libudev)\b`)

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

		ex, err := link.OpenExecutable(lib)
		if err != nil {
			debugLog("funkoverage-shim: error opening %s: %v", lib, err)
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
			fmt.Fprintf(t.funcsLogFile, "FUNC %s %s\n", lib, demangledName)
		}

		l, err := ex.UprobeMulti(names, t.objs.TraceUprobe, &link.UprobeMultiOptions{
			Cookies: cookies,
		})
		if err != nil {
			debugLog("funkoverage-shim: skipping uprobes on %s: %v", lib, err)
			continue
		}

		t.links = append(t.links, l)
		t.imgSymbols[lib] = names
		t.imgCookies[lib] = cookies

		debugLog("funkoverage-shim: successfully instrumented %d functions in %s", len(syms), lib)
	}
}

func debugLog(format string, args ...any) {
	if os.Getenv("FUNKOVERAGE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}
