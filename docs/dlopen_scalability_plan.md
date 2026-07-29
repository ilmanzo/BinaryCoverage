# Scalable Dynamic Library (`dlopen`) Coverage Plan

This document outlines a highly scalable, event-driven design to address the `dlopen()` TODO in the `README.md`. 
The solution is designed to scale efficiently up to **5000+ instrumented binaries** without causing CPU/memory exhaustion or kernel uprobe resource bottlenecks.

---

## 1. The Challenge

Currently, `funkoverage` resolves shared libraries at **install time** or **trace start time** using `ldd` (via `DT_NEEDED` entries).
Any library loaded dynamically at runtime via `dlopen()` or `dlmopen()` is invisible to `ldd` and is currently not traced.

To solve this, we must:
1. **Detect** when a new `.so` library is loaded at runtime.
2. **Enumerate** and filter functions in the newly loaded library on-the-fly.
3. **Attach** eBPF uprobes to the new library's functions at runtime.
4. Do so with **minimal resource usage** suitable for large-scale enterprise environments (up to 5000 instrumented binaries running concurrently or sequentially).

---

## 2. Architectural Design: Event-Driven JIT Instrumentation

Rather than actively polling `/proc/<pid>/maps` (which introduces heavy CPU/IO overhead and does not scale), we propose an **Event-Driven JIT (Just-In-Time) Instrumentation** strategy.

### Visual Architecture

```
┌─────────────────────────────────┐
│     Target Application (Child)  │
│  ┌───────────────────────────┐  │
│  │ Calls dlopen("plugin.so") │  │
│  └─────────────┬─────────────┘  │
└────────────────┼────────────────┘
                 │ (1) Trapped by uretprobe
                 ▼
┌─────────────────────────────────┐
│           eBPF Kernel           │
│  ┌───────────────────────────┐  │
│  │ trace_dlopen_return()     │  │
│  │ Check non-NULL handle     │  │
│  │ ringbuf_submit(0xFFFFFFFF)│  │
│  └─────────────┬─────────────┘  │
└────────────────┼────────────────┘
                 │ (2) Ringbuffer event
                 ▼
┌─────────────────────────────────┐
│      funkoverage-shim (Go)      │
│  ┌───────────────────────────┐  │
│  │ readLoop: got 0xFFFFFFFF  │  │
│  │ Read /proc/<pid>/maps     │  │
│  │ Enumerate plugin.so       │  │
│  │ attach.UprobeMulti(...)   │  │
│  └───────────────────────────┘  │
└─────────────────────────────────┘
```

---

## 3. High-Performance / Scalability Rationale

When scaling to **5000+ instrumented binaries**, performance and resource limitations are the primary constraints. Here is how our design scales:

### A. Active Polling vs. Event-Driven Maps Parsing
* **Active Polling (Do Not Use)**: Polling `/proc/<pid>/maps` every 10ms in 5000 shims requires reading up to **500,000 files/sec**. This degrades file system caches, consumes substantial CPU, and triggers process throttling.
* **Event-Driven (Our Plan)**: We only read `/proc/<pid>/maps` when a `dlopen` successfully completes. Since dynamic library loads are rare in steady-state operation (typically happening only at startup or on explicit plugin loads), the steady-state overhead is **0% CPU** and **0% IO**.

### B. eBPF `seen` Dedup Map Scaling
* The BPF `seen` array map is used for atomic in-kernel CAS first-call filtering.
* **Array Map with Headroom**: In `NewTracer`, instead of sizing the `seen` map precisely to `len(refs)`, we size it to `len(refs) + MaxDynamicFuncs` (e.g., +100,000 headroom). 
  * At 8 bytes per entry, 100,000 headroom entries consume **only 800 KB of memory** per running tracer.
  * Array lookup remains a super-fast, O(1) direct register offset operation in the kernel.
  * Out-of-bounds safety is guaranteed: any dynamic function index greater than the map size returns `NULL` from `bpf_map_lookup_elem`, preventing panics.

### C. Fast, Kernel-Level Hooking of `dlopen`
* We locate the target's mapped `libc.so.6` (or `libdl.so.2`) by inspecting `/proc/<child_pid>/maps` once at startup.
* We attach a **`Uretprobe`** to the `dlopen` symbol.
* The uretprobe BPF program:
  1. Checks if the return value (registers `rax` on x86_64, `x0` on arm64) is non-NULL.
  2. If non-NULL, writes a reserved function index `0xFFFFFFFF` to the `events` ring buffer.

---

## 4. Implementation Specification

### 1. BPF Changes (`cmd/shim_binary/bpf/tracer.bpf.c`)
Add a return uprobe hook specifically for `dlopen`:

```c
SEC("uretprobe/dlopen")
int trace_dlopen_return(struct pt_regs *ctx)
{
    // Check if parent TGID is watched
    __u32 tgid = bpf_get_current_pid_tgid() >> 32;
    if (!bpf_map_lookup_elem(&watched, &tgid))
        return 0;

    // Check if return register is non-NULL (dlopen succeeded)
#if defined(__TARGET_ARCH_x86)
    void *handle = (void *)PT_REGS_RC(ctx);
#elif defined(__TARGET_ARCH_arm64)
    void *handle = (void *)ctx->regs[0];
#else
    void *handle = (void *)1; // Fallback
#endif

    if (!handle)
        return 0;

    // Send the special 0xFFFFFFFF event
    struct event *e = bpf_ringbuf_reserve(&events, sizeof(struct event), 0);
    if (!e)
        return 0;
    e->func_idx = 0xFFFFFFFF; // Reserved token for dynamic load
    bpf_ringbuf_submit(e, 0);
    return 0;
}
```

### 2. Userspace Go Changes (`cmd/shim_binary/tracer.go`)

#### Hooking `dlopen` at Startup
In `Tracer.Start()`:
1. Parse `/proc/<child_pid>/maps` to find the loaded `libc.so.6` or `libdl.so.2` path.
2. Open the dynamic linker / libc using `link.OpenExecutable()`.
3. Call `ex.Uretprobe("dlopen", t.objs.TraceDlopenReturn, nil)` and save the link to `t.links`.

#### Handling `0xFFFFFFFF` in `readLoop()`
Modify the event loop to intercept the special index:

```go
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
		
		if idx == 0xFFFFFFFF {
			// Trigger Dynamic JIT Hooking
			t.handleDynamicLoad()
			continue
		}
		
		if idx >= uint32(len(t.funcs)) {
			continue
		}
		// ... demangle and write CALLED log as usual ...
	}
}
```

#### Performing the JIT Attach
In `handleDynamicLoad()`:
1. Re-read `/proc/<child_pid>/maps`.
2. Find any `.so` file path that is mapped with `PROT_EXEC` and is NOT in `t.imgCookies`.
3. For each new library:
   - Run `enumerateOne(libPath, t.filter)` to discover its functions.
   - Assign cookies sequentially starting from the current `len(t.funcs)`.
   - Call `ex.UprobeMulti(...)` on the new library.
   - Append the new symbols and cookies to `t.imgSymbols`, `t.imgCookies`, and `t.funcs`.
   - Log the newly discovered functions to the functions log (`_functions.log`) so they are registered in the report.

### 3. Preserving Filter Rules
To ensure `--include` / `--exclude` filtering continues to work perfectly on dynamically loaded libraries, we will serialize the `FuncFilter` regexes inside the `<name>.funcs.json` sidecar at install/trace start time, or pass them in `t.filter` to the tracer.

---

## 5. Verification Plan

We will implement automated E2E and Unit tests to verify correct operation and scale:

1. **Unit Test**: 
   * Construct a test case with a dummy executable that calls `dlopen()` on a dynamic shared library on-the-fly.
   * Verify that functions from the dynamic library are correctly captured, logged to `_called.log`, and successfully demangled.
2. **Scalability and Stress Test**:
   * Generate 5000 dummy binaries each tracing distinct or overlapping sets of dynamic libraries.
   * Verify that resource limits (FDs, VMA/Uprobe kernel memory limits, `/proc` read rates) stay well within system safety parameters.
