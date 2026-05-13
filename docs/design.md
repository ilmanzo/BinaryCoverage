# funkoverage — Design

Function-level binary code coverage via native eBPF (`uprobe_multi`). This document explains the architecture, data flow, and key invariants.

## What problem it solves

Given an ELF binary, **which of its functions actually got called** during a test run? Useful for:

- Measuring coverage of functional/integration tests against pre-built binaries
- Identifying dead code in shipped releases
- Understanding which library code paths a black-box program exercises

Constraints we accept:
- No source code required
- No recompilation, no debugger, no `LD_PRELOAD`
- Works on stripped binaries (with separate debug info installed)
- Acceptable overhead for first-call-only coverage (atomic CAS dedup in kernel)

## Two binaries

```
┌─────────────────┐         ┌──────────────────┐
│   funkoverage   │  CLI    │ funkoverage-shim │  installed in place
│   (cmd/)        │         │ (cmd/shim_binary)│  of the target binary
└────────┬────────┘         └────────┬─────────┘
         │ install/uninstall          │ runs when target is invoked
         │ enumerate / report         │ attaches uprobes,
         │ trace                      │ writes _called.log
         │                            │
         ▼                            ▼
   /var/coverage/bin/<name>     /var/coverage/data/
   <name>.funcs.json            *_called.log
   <name>.libs.json             *_functions.log
```

Both are `package main`; shared helpers live in `internal/funkutil/`.

## Lifecycle

### Install (`funkoverage install /usr/bin/foo`)

```
                         ┌─────────────────┐
   /usr/bin/foo  ────────┤ funkoverage CLI │
                         └────────┬────────┘
                                  │
        ┌─────────────────────────┼─────────────────────────────┐
        ▼                         ▼                             ▼
   1. Move foo to          2. Enumerate functions        3. Copy shim binary
      /var/coverage/bin/      via DWARF/symtab              to /usr/bin/foo
      foo                     write sidecars                setcap CAP_BPF...
                              foo.funcs.json
                              foo.libs.json
```

Steps:

1. **Stat & validate** — must be ELF, must have debug info (embedded or external `.build-id`/`.dwz`)
2. **Move** real binary to `$SAFE_BIN_DIR/foo` (default `/var/coverage/bin/`)
3. **Merge debug** — if external `.debug` file exists, run `eu-unstrip` to merge into the safe copy
4. **Enumerate functions**:
   - Try `.symtab` first (uprobe attach uses symtab; also avoids dwz issues)
   - Fall back to external `.debug` symtab
   - Last resort: DWARF traversal
5. **Write sidecars** beside the safe binary:
   - `foo.funcs.json` — `{image_path: [mangled_name, ...]}` for the shim to attach
   - `foo.libs.json` — list of library paths for multi-image tracing
6. **Write functions log** — `$LOG_DIR/foo_<ts>_functions.log` with demangled names (for the report)
7. **Copy shim** to `/usr/bin/foo` with original permissions
8. **`setcap cap_bpf,cap_perfmon,cap_dac_read_search+ep`** on the new shim copy

### Run (transparent — user invokes `foo`)

```
  user runs `foo arg1`
         │
         ▼
  /usr/bin/foo  ◄── this is now the shim
         │
         ├── locate real binary at /var/coverage/bin/foo
         ├── read foo.funcs.json (mangled names per image)
         │
         ├── fork a child shim process (paused on a pipe)
         │   ├── set FUNKOVERAGE_CHILD=1, FUNKOVERAGE_WAIT_FD=3
         │   └── child blocks reading the pipe
         │
         ├── load embedded BPF program (arch-specific, build-tag selected)
         ├── for each image: link.UprobeMulti(symbols, cookies)
         │   (one syscall per image, all symbols at once)
         ├── attach sched_process_fork tracepoint
         ├── seed `watched` map with child's TGID
         ├── start ringbuf reader goroutine
         │
         ├── unblock child via pipe (1 byte)
         │   └── child execs the real binary
         │
         ├── ringbuf events → demangle → write to $LOG_DIR/foo_<ts>_called.log
         │
         └── child exits → tracer.Stop()
                            ├── detach all links (stops new events)
                            ├── sleep 100ms (drain in-flight events)
                            ├── close ringbuf reader
                            └── close log + release BPF objects
```

### Report (`funkoverage report /var/coverage/data /tmp/report --formats html,xml,txt`)

```
  $LOG_DIR/*_functions.log  ┐
                            ├──► analyzeLogs ──► CoverageData per image
  $LOG_DIR/*_called.log     ┘                    {Total, Called}
                                                       │
                                            ┌──────────┼──────────┐
                                            ▼          ▼          ▼
                                          HTML        XML        TXT
                                       (one file    (xunit)   (stdout)
                                        per image
                                        + aggregate)
```

### Uninstall (`funkoverage uninstall /usr/bin/foo`)

Reverses install: move `/var/coverage/bin/foo` back to `/usr/bin/foo`, delete sidecars.

## eBPF program

`cmd/shim_binary/bpf/tracer.bpf.c` (GPL-2.0-only) — three pieces:

```
┌────────────────────────────────────────────────────────────────┐
│  uprobe.multi/probe                                            │
│  ─────────────────                                             │
│  on every traced function entry:                               │
│    tgid = pid_tgid >> 32                                       │
│    if !watched[tgid]: return  ← scope to our process tree      │
│    idx = (u32) bpf_get_attach_cookie(ctx)  ← global func index │
│    if CAS(seen[idx], 0, 1) != 0: return  ← already reported    │
│    ringbuf_submit({idx})                                       │
└────────────────────────────────────────────────────────────────┘

┌────────────────────────────────────────────────────────────────┐
│  tp/sched/sched_process_fork                                   │
│  ───────────────────────────                                   │
│  if watched[parent]: watched[child] = 1                        │
│  → tracing follows fork() across process tree                  │
└────────────────────────────────────────────────────────────────┘

Maps:
  watched     HASH (4096 entries)        u32 tgid → u8
  seen        ARRAY (resized to N funcs) u32 idx  → u64 atomic flag
  events      RINGBUF (256 KB)           {u32 func_idx}
```

### Key design decisions

| Decision | Why |
|---|---|
| Cookie = sequential global index | O(1) array dedup vs O(log N) hash; reproducible across runs |
| u64 for `seen` (not bitmap) | BPF default `-mcpu=v1` only has 64-bit atomic CAS; 8x cost is trivial |
| First-call-only events | Bandwidth bounded by total func count, not call frequency |
| CO-RE (`vmlinux.h` + BTF reloc) | BPF `.o` portable across kernels ≥6.6 — no rebuild on kernel update |
| No `sched_process_exit` cleanup | execve's `de_thread()` fires exit with TGID, would prematurely evict |
| Drain delay on Stop | Ringbuf reader.Read() blocks; close before drain loses events |

## Data formats

### Sidecar files (next to the real binary in `$SAFE_BIN_DIR/`)

- `<name>.funcs.json` — `{"image_path": ["mangled_name", ...], ...}`
- `<name>.libs.json` — `["lib_path", ...]`

Mangled names are mandatory: BPF uprobe attach resolves names against the ELF symbol table directly. Demangling happens **only at output time** (writes to `_called.log`, `_functions.log`, and stdout).

### Log files (in `$LOG_DIR/`)

Format: space-separated, one record per line. Filenames carry a timestamp + nanosecond suffix to avoid collisions across runs.

```
foo_20260513-143022_1715610622123456789_functions.log
  FUNC /usr/bin/foo str_length(std::string const&)
  FUNC /usr/bin/foo math_add(int, int)
  ...

foo_20260513-143022_1715610622987654321_called.log
  CALLED /usr/bin/foo str_length(std::string const&)
  CALLED /usr/lib64/libssl.so.3 SSL_connect
  ...
```

The functions log is written at **install time** (one record per discovered function). The called log is written at **runtime** by the shim's ringbuf reader (one record per first-observed call).

## Function enumeration

`cmd/enumerate.go` discovers traceable functions per image:

```
┌────────────────┐
│ enumerateOne() │
└───────┬────────┘
        │
        ▼
   1. .symtab? ──── yes ──► return functions
        │
        no (or empty)
        │
        ▼
   2. external .debug found?
       │       │
       │       ▼
       │   .symtab in .debug? ──── yes ──► return functions
       │       │
       no      no
       │       │
       └───────┴──► 3. DWARF (last resort)
```

Filters applied to every candidate:
1. **Blacklist**: `main`, `_init`, `_start`, `.plt*`
2. **Suffix**: `@plt`, `@plt.got`
3. **Prefix**: `__` (compiler/runtime internals)
4. **`--include`/`--exclude`** regex (against demangled name)

Library discovery: `ldd` output, minus glibc/runtime libs (libc, libm, libpthread, ...) and vDSO. The `--no-libs` flag skips library tracing entirely.

## Concurrency model in the shim

```
┌─────────────────────────────────────────────────────┐
│  parent shim process                                │
│                                                     │
│  main goroutine:                                    │
│    fork child, attach probes, start reader, wait    │
│                                                     │
│  reader goroutine (started by Tracer.Start):        │
│    loop:                                            │
│      record = reader.Read()  ← blocks on ringbuf    │
│      lookup func by index, demangle, write to log   │
│    exit on reader.Close() → ErrClosed               │
└─────────────────────────────────────────────────────┘
        │
        ├──► fork ──► child shim (paused on pipe)
        │              ├── exec real binary
        │              └── kernel fires uprobes →
        │                  → ringbuf → parent's reader
        │
        └──► tracer.Stop():
             1. detach links (no new events)
             2. sleep 100ms (drain in-flight)
             3. close reader (reader goroutine exits)
             4. wg.Wait, close log, release objs
             (idempotent via sync.Once)
```

## Permissions model

- **Install**: must run as root (moves files in `/usr/bin`, runs `setcap`)
- **Runtime**: shim must have `cap_bpf`, `cap_perfmon`, `cap_dac_read_search` — granted via file capabilities at install time
- **Recursion guard**: `FUNKOVERAGE_ACTIVE=1` in env tells nested shim invocations to `exec` the real binary directly (no second tracer)

## Repo layout

```
.
├── cmd/
│   ├── funkoverage.go        # CLI dispatch (setup, install, ..., report)
│   ├── enumerate.go          # symtab + DWARF + filters + ldd
│   ├── shim.go               # install/uninstall logic
│   ├── trace.go              # one-shot trace without permanent install
│   ├── report.go             # HTML/XML/text generation, log analysis
│   ├── elfutil.go            # ELF helpers, debug merging via eu-unstrip
│   ├── templates.go          # embedded help text + HTML templates
│   ├── templates/            # *.html for report rendering
│   └── shim_binary/
│       ├── main.go           # shim main + child fork dance
│       ├── tracer.go         # cilium/ebpf wiring, ringbuf reader
│       ├── tracer_{x86,arm64}_bpfel.{go,o}  # bpf2go-generated bindings
│       └── bpf/
│           └── tracer.bpf.c  # GPL-2.0 eBPF program
├── internal/funkutil/        # shared helpers (env, sidecar I/O, version strip)
├── tests/
│   ├── e2e/                  # standalone bash scripts (bzip2, squid, openssl)
│   └── sample/               # 100-function C++ test binary
├── rpm/coverage-tools.spec   # openSUSE/Fedora packaging
└── docs/design.md            # this file
```

## Licensing

- Go userspace code: **MIT**
- eBPF kernel code (`cmd/shim_binary/bpf/`): **GPL-2.0-only**

The eBPF programs must be GPL to use GPL-only kernel helpers like `bpf_get_attach_cookie` and `bpf_ringbuf_reserve`. The Go runtime userspace stays MIT.
