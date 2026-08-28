package main

import (
	"debug/elf"
	"encoding/binary"
	"fmt"
	"maps"
	"os"
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
//
// It is deliberately flat and deliberately large. Scaling it off the static
// count (tried: max(len(refs)/2, 4096)) looks tidier and costs ~800 KB of
// kernel memory less per shim invocation, but the dlopen path instruments
// *every* newly mapped non-noisy library, not just the plugin — nginx's 3940
// static functions bought 1970 dynamic slots, which its own mapped libraries
// exhausted before the echo module was reached, and
// test_nginx_dlopen.sh failed on a missing ngx_http_echo_handler. The reserve
// has to be sized for the libraries the target might map, which is unrelated
// to how many functions the target itself has.
//
// ponytail: 800 KB per invocation, wasted on any target that never dlopens.
// The real fix is a BPF_MAP_TYPE_HASH "seen" map, which allocates on demand —
// it costs a hash lookup in the uprobe hot path and a tracer.bpf.c change, so
// it waits for evidence that the memory actually matters.
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
	funcs   []FuncRef              // global cookie/index → ref (parallel to attach order)
	plans   map[string]*attachPlan // image path → what to attach and under which cookies
	scanned map[string]struct{}    // image paths already considered, so dlopen rescans skip them

	objs           tracerObjects
	linksMu        sync.Mutex // guards links: Stop (caller goroutine) vs handleDynamicLoad (readLoop goroutine)
	links          []link.Link
	reader         *ringbuf.Reader
	logFile        *os.File
	funcsLogPath   string
	funcsLogFile   *os.File // lazily opened by ensureFuncsLog on first dlopen event
	seenCapacity   uint32   // "seen" map MaxEntries; dynamic cookies beyond this are dropped by the kernel
	capacityWarned bool
	filter         *funkutil.FuncFilter // dlopen JIT filter, mirrors the install-time --include/--exclude filter

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewTracer loads BPF objects sized for the given function set and opens the
// _called.log output file. It does NOT attach probes — call Start for that.
//
// `funcs` maps each ELF image path (main binary or shared library) to the
// list of symbol names to trace. The flattened order — images sorted, symbols
// in input order — defines the global cookie space used to identify events.
// A nil/empty funcs map is valid: it supports pure runtime dynamic-library
// tracing, a binary with no static functions of its own that relies
// entirely on dlopen'd plugins for coverage — flattenFuncs below ranges it
// (a no-op on nil) and simply produces no initial cookies.
//
// `includePattern`/`excludePattern` are the source regex patterns from the
// install-time --include/--exclude filter (empty string = no filter). They
// are re-applied to functions discovered later via dlopen JIT
// instrumentation, matching the filtering already applied at enumeration
// time to statically discovered functions.
func NewTracer(funcs map[string]funkutil.ImageFuncs, logPath, includePattern, excludePattern string) (*Tracer, error) {
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("tracer: remove memlock: %w", err)
	}

	filter := funkutil.FilterFromSidecar(funkutil.FilterSidecar{Include: includePattern, Exclude: excludePattern})
	if includePattern != "" && filter.Include == nil {
		debugLog("funkoverage-shim: bad --include pattern %q", includePattern)
	}
	if excludePattern != "" && filter.Exclude == nil {
		debugLog("funkoverage-shim: bad --exclude pattern %q", excludePattern)
	}

	refs, plans := flattenFuncs(funcs)

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
		plans:        plans,
		scanned:      make(map[string]struct{}, len(plans)),
		objs:         objs,
		logFile:      logFile,
		funcsLogPath: funcsLogPath,
		seenCapacity: seenCapacity,
		filter:       filter,
	}, nil
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

// attachPlan is one image's uprobe batches. UprobeMulti takes either names or
// addresses, never both, so an image whose external debug file contributed
// functions needs two links; nameCookies and addrCookies keep each batch
// aligned with the global cookie space.
type attachPlan struct {
	buildID     string
	names       []string
	nameCookies []uint64
	addrs       []uint64
	addrCookies []uint64
}

// flattenFuncs builds the global cookie space. Images are sorted so that
// repeated invocations against the same input produce identical cookies —
// useful for debugging and for cross-run analysis.
func flattenFuncs(funcs map[string]funkutil.ImageFuncs) ([]FuncRef, map[string]*attachPlan) {
	images := slices.Sorted(maps.Keys(funcs))

	var refs []FuncRef
	plans := make(map[string]*attachPlan, len(images))

	for _, img := range images {
		imgFuncs := funcs[img]
		plan := &attachPlan{
			buildID:     imgFuncs.BuildID,
			names:       make([]string, 0, len(imgFuncs.Names)),
			nameCookies: make([]uint64, 0, len(imgFuncs.Names)),
		}
		for _, name := range imgFuncs.Names {
			plan.names = append(plan.names, name)
			plan.nameCookies = append(plan.nameCookies, uint64(len(refs)))
			refs = append(refs, FuncRef{Image: img, Name: name})
		}
		for _, fa := range imgFuncs.Offsets {
			plan.addrs = append(plan.addrs, fa.Offset)
			plan.addrCookies = append(plan.addrCookies, uint64(len(refs)))
			refs = append(refs, FuncRef{Image: img, Name: fa.Name})
		}
		plans[img] = plan
	}
	return refs, plans
}

// verify adapts the plan to the file actually on disk at attach time.
//
// It drops names the mapped file cannot resolve. UprobeMulti resolves the
// whole batch up front and fails on the first miss, so one stale name would
// otherwise cost the entire image's coverage — and names do go stale: they are
// chosen at install time against a file that can be replaced by a package
// upgrade before the binary is ever run.
//
// It drops the address batch outright unless the build-id still matches.
// Offsets are only meaningful for the exact build they were computed from, and
// a wrong one puts a uprobe mid-instruction, killing the target with SIGILL —
// strictly worse than missing coverage, so the check is mandatory, not
// best-effort. A file that cannot be read leaves the plan untouched, so
// UprobeMulti reports the real reason.
func (p *attachPlan) verify(img string) *attachPlan {
	f, err := elf.Open(img)
	if err != nil {
		return p
	}
	defer f.Close()

	out := &attachPlan{buildID: p.buildID}

	if len(p.addrs) > 0 {
		switch id, err := funkutil.BuildID(f); {
		case err != nil || id != p.buildID:
			debugLog("funkoverage-shim: %s: build-id changed since install (%s -> %s); dropping %d address probes",
				img, p.buildID, id, len(p.addrs))
		default:
			out.addrs, out.addrCookies = p.addrs, p.addrCookies
		}
	}

	resolvable := funkutil.ResolvableFuncNames(f)
	if len(resolvable) == 0 {
		out.names, out.nameCookies = p.names, p.nameCookies
		return out
	}
	out.names = make([]string, 0, len(p.names))
	out.nameCookies = make([]uint64, 0, len(p.nameCookies))
	for i, name := range p.names {
		if _, ok := resolvable[name]; ok {
			out.names = append(out.names, name)
			out.nameCookies = append(out.nameCookies, p.nameCookies[i])
		}
	}
	if dropped := len(p.names) - len(out.names); dropped > 0 {
		debugLog("funkoverage-shim: %s: %d of %d symbols not in the mapped file, attaching the rest",
			img, dropped, len(p.names))
	}
	return out
}

// attachProbes attaches one uprobe batch — by name if names is set, by file
// offset if addrs is — and returns how many probes went live.
//
// UprobeMulti is all-or-nothing: the kernel rejects the whole request over a
// single bad entry, so a batch that fails is halved and retried until the
// failures are isolated to individual probes, which are logged and dropped.
// The alternative is losing an entire library over a couple of functions, and
// those functions exist: libcrypto's hand-written AES-NI assembly
// (_aesni_ctr32_ghash_6x, _aesni_ctr32_6x) starts with an instruction the x86
// uprobe decoder refuses (EOPNOTSUPP), and those two alone cost the other 7165
// offsets in that image — measured, not hypothetical.
//
// ponytail: bisection is ~2k·log2(n) attach syscalls for k bad probes, so an
// image that is mostly unprobeable degrades to ~2n. Pre-filtering would need an
// x86 instruction decoder in the shim; revisit only if such an image shows up.
func (t *Tracer) attachProbes(ex *link.Executable, img string, names []string, addrs, cookies []uint64) int {
	n := len(cookies)
	if n == 0 {
		return 0
	}

	return attachBisect(n, func(lo, hi int) error {
		opts := &link.UprobeMultiOptions{Cookies: cookies[lo:hi]}
		if names == nil {
			opts.Addresses = addrs[lo:hi]
		}
		l, err := ex.UprobeMulti(subslice(names, lo, hi), t.objs.TraceUprobe, opts)
		if err != nil {
			return err
		}
		t.addLink(l)
		return nil
	}, func(i int, err error) {
		debugLog("funkoverage-shim: %s: dropping probe %s: %v", img, probeLabel(names, addrs, i), err)
	})
}

// attachBisect attaches [0,n) via attach, halving and retrying any range that
// fails until every failure is pinned to a single index, which is reported to
// drop. Returns how many probes attached.
func attachBisect(n int, attach func(lo, hi int) error, drop func(i int, err error)) int {
	var walk func(lo, hi int) int
	walk = func(lo, hi int) int {
		err := attach(lo, hi)
		if err == nil {
			return hi - lo
		}
		if hi-lo == 1 {
			drop(lo, err)
			return 0
		}
		mid := lo + (hi-lo)/2
		return walk(lo, mid) + walk(mid, hi)
	}
	return walk(0, n)
}

// subslice slices s, preserving nil — attachProbes distinguishes name mode from
// address mode by which of its two slices is nil, and an empty non-nil slice
// would send it down the wrong path.
func subslice[T any](s []T, lo, hi int) []T {
	if s == nil {
		return nil
	}
	return s[lo:hi]
}

func probeLabel(names []string, addrs []uint64, i int) string {
	if names != nil {
		return names[i]
	}
	return fmt.Sprintf("offset %#x", addrs[i])
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

	for img, plan := range t.plans {
		// Marked scanned regardless of what follows: a dlopen rescan
		// re-enumerating an image install already accounted for would allocate
		// a second set of cookies for the same functions and double-count them.
		t.scanned[img] = struct{}{}

		ex, err := link.OpenExecutable(img)
		if err != nil {
			// Some libraries lack the execute bit (packaging bug); skip rather than abort.
			debugLog("funkoverage-shim: skipping %s: %v", img, err)
			continue
		}
		plan = plan.verify(img)
		t.attachProbes(ex, img, plan.names, nil, plan.nameCookies)
		t.attachProbes(ex, img, nil, plan.addrs, plan.addrCookies)
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
			t.handleDynamicLoad()
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

func (t *Tracer) Stop() {
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

	hasName := func(s elf.Symbol) bool { return s.Name == name }
	if syms, err := f.DynamicSymbols(); err == nil && slices.ContainsFunc(syms, hasName) {
		return true
	}
	if syms, err := f.Symbols(); err == nil && slices.ContainsFunc(syms, hasName) {
		return true
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

// watchedPIDs returns the live contents of the "watched" BPF map: rootPID
// plus every descendant seen by the sched_process_fork tracepoint so far.
func (t *Tracer) watchedPIDs() ([]uint32, error) {
	var pids []uint32
	var key uint32
	var val uint8
	it := t.objs.Watched.Iterate()
	for it.Next(&key, &val) {
		pids = append(pids, key)
	}
	return pids, it.Err()
}

func getMappedSharedLibraries(pid uint32) ([]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/maps", pid))
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var libs []string
	for line := range strings.Lines(string(data)) {
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

// getSharedLibrarySymbols delegates the actual symbol walk (union of
// .symtab and .dynsym, STT_FUNC + size/address filtering, address+name
// dedup — shared with install-time enumeration) to
// funkutil.SymtabFunctions, keeping only relevance filtering here; the
// --include/--exclude filter is applied separately by the caller
// (handleDynamicLoad), after capacity clipping.
func getSharedLibrarySymbols(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	funcs := funkutil.SymtabFunctions(f, funkutil.FuncIsRelevant)
	if len(funcs) == 0 {
		return nil, fmt.Errorf("no symbol table (dynamic or static) in %s", path)
	}
	return funcs, nil
}

// handleDynamicLoad reacts to a dlopen() event by rescanning every currently
// watched process's memory map, not just the root process. The ringbuf event
// that triggers this carries no pid — dlopen() may have been called from a
// forked child (watched via sched_process_fork), whose newly-mapped library
// would be invisible if we only inspected the root pid's /proc/maps.
//
// ponytail: this runs on the readLoop goroutine, so nothing drains the ringbuf
// while it works. The t.scanned memo keeps the per-event cost at one
// /proc/<pid>/maps read per watched pid once the libraries settle, and the
// kernel dedups events (one per unique function) into a 256 KB ringbuf, so
// overflow needs a pathological plugin load. If one ever shows up, move this
// to its own goroutine behind a coalescing channel.
func (t *Tracer) handleDynamicLoad() {
	pids, err := t.watchedPIDs()
	if err != nil {
		debugLog("funkoverage-shim: error listing watched pids: %v", err)
		return
	}

	seen := make(map[string]struct{})
	var libs []string
	for _, pid := range pids {
		mapped, err := getMappedSharedLibraries(pid)
		if err != nil {
			debugLog("funkoverage-shim: error getting mapped libraries for pid %d: %v", pid, err)
			continue
		}
		for _, lib := range mapped {
			if _, dup := seen[lib]; dup {
				continue
			}
			seen[lib] = struct{}{}
			libs = append(libs, lib)
		}
	}

	for _, lib := range libs {
		if _, seen := t.scanned[lib]; seen {
			continue
		}
		// Marked before any of the checks below, not after a successful
		// attach: a library rejected as noisy, filtered out, or lacking a
		// symbol table stays rejected, and re-deciding that on every
		// subsequent dlopen means re-parsing its symbol tables every time.
		t.scanned[lib] = struct{}{}

		// Skip system/noisy dlopen libraries to keep trace size reasonable
		if funkutil.IsNoisyDlopenLib(lib) {
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

		if t.filter.Include != nil || t.filter.Exclude != nil {
			filtered := syms[:0]
			for _, name := range syms {
				if t.filter.Match(demangle.Filter(funkutil.StripVersion(name))) {
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

		attached := t.attachProbes(ex, lib, names, nil, cookies)
		debugLog("funkoverage-shim: successfully instrumented %d of %d functions in %s", attached, len(syms), lib)
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
