package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
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

	objs    tracerObjects
	links   []link.Link
	reader  *ringbuf.Reader
	logFile *os.File

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
	if len(funcs) == 0 {
		return nil, errors.New("tracer: no functions to trace")
	}
	if err := rlimit.RemoveMemlock(); err != nil {
		return nil, fmt.Errorf("tracer: remove memlock: %w", err)
	}

	refs, imgSyms, imgCookies := flattenFuncs(funcs)

	spec, err := loadTracer()
	if err != nil {
		return nil, fmt.Errorf("tracer: load BPF spec: %w", err)
	}
	// Resize the dedup map to one entry per global func index; each entry stores
	// the 64-bit seen flag for that function.
	spec.Maps["seen"].MaxEntries = uint32(len(refs))

	var objs tracerObjects
	if err := spec.LoadAndAssign(&objs, nil); err != nil {
		return nil, fmt.Errorf("tracer: load BPF objects: %w", err)
	}

	logFile, err := os.Create(logPath)
	if err != nil {
		objs.Close()
		return nil, fmt.Errorf("tracer: create log %s: %w", logPath, err)
	}

	return &Tracer{
		funcs:      refs,
		imgSymbols: imgSyms,
		imgCookies: imgCookies,
		objs:       objs,
		logFile:    logFile,
	}, nil
}

// flattenFuncs builds the global cookie space. Images are sorted so that
// repeated invocations against the same input produce identical cookies —
// useful for debugging and for cross-run analysis.
func flattenFuncs(funcs map[string][]string) ([]FuncRef, map[string][]string, map[string][]uint64) {
	images := make([]string, 0, len(funcs))
	for img := range funcs {
		images = append(images, img)
	}
	slices.Sort(images)

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

	for img, cookies := range t.imgCookies {
		ex, err := link.OpenExecutable(img)
		if err != nil {
			return fmt.Errorf("tracer: open executable %s: %w", img, err)
		}
		l, err := ex.UprobeMulti(t.imgSymbols[img], t.objs.TraceUprobe, &link.UprobeMultiOptions{
			Cookies: cookies,
		})
		if err != nil {
			return fmt.Errorf("tracer: attach uprobes on %s (%d symbols): %w", img, len(cookies), err)
		}
		t.links = append(t.links, l)
	}

	fl, err := link.Tracepoint("sched", "sched_process_fork", t.objs.TraceFork, nil)
	if err != nil {
		return fmt.Errorf("tracer: attach fork tracepoint: %w", err)
	}
	t.links = append(t.links, fl)

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
			return
		}
		if len(record.RawSample) < 4 {
			continue
		}
		idx := binary.LittleEndian.Uint32(record.RawSample[:4])
		if idx >= uint32(len(t.funcs)) {
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
		t.objs.Close()
	})
	return nil
}
