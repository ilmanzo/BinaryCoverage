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
  user/systemd runs `foo arg1`
         │
         ▼
  /usr/bin/foo  ◄── this is now the shim, pid P
         │
         ├── locate real binary at /var/coverage/bin/foo
         │
         ├── fork the *stable* funkoverage-shim binary (per foo.shimbin.json,
         │   NOT os.Executable() — that would resolve to /usr/bin/foo itself,
         │   and the helper holding it open as its own running text would
         │   make a later `funkoverage uninstall foo` fail with ETXTBSY)
         │   as a background helper (paused on a pipe, fd 3)
         │   ├── set FUNKOVERAGE_HELPER=1, FUNKOVERAGE_TARGET_PID=P,
         │   │   FUNKOVERAGE_REAL_BIN=/var/coverage/bin/foo
         │   └── release it immediately (Process.Release) — we can never
         │       Wait() on it, we're about to exec away for good
         │
         │   [in the helper, a separate process:]
         │   ├── PR_SET_PDEATHSIG so it's notified when P eventually dies
         │   │   (exec doesn't trigger this — only P's actual termination,
         │   │   which by then means the real daemon has exited)
         │   ├── read foo.funcs.json (mangled names per image) — via
         │   │   FUNKOVERAGE_REAL_BIN, since the helper isn't running from
         │   │   /usr/bin/foo and can't derive this from os.Executable()
         │   ├── load embedded BPF program (arch-specific, build-tag
         │   │   selected), for each image: link.UprobeMulti(symbols,
         │   │   cookies) (one syscall per image, all symbols at once)
         │   ├── attach sched_process_fork tracepoint
         │   ├── seed `watched` map with P's TGID
         │   ├── start ringbuf reader goroutine
         │   └── write "OK" to the pipe
         │
         ├── read "OK" from the pipe (blocks until the helper is attached)
         │
         └── syscall.Exec(real binary) — IN THIS PROCESS, replacing it;
             pid stays P throughout. Supervisors that check process
             identity (systemd's LISTEN_PID/NotifyAccess=main, pg_ctl's
             postmaster.pid pid field, ...) see the real daemon running
             exactly where they expect (issue #152) — no signal-forwarding
             or sd_notify-relay layer is needed, because there's no longer
             a separate tracked-vs-actual pid to bridge.
                            │
                            ▼
             kernel fires uprobes → ringbuf → helper's reader →
             demangle → write to $LOG_DIR/foo_<ts>_called.log
                            │
                            ▼
             P (the real daemon) exits → helper's PDEATHSIG fires →
             tracer.Stop() (detach links, drain, close log)
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

┌────────────────────────────────────────────────────────────────┐
│  uretprobe/dlopen                                              │
│  ─────────────────                                             │
│  on dlopen return:                                             │
│    tgid = pid_tgid >> 32                                       │
│    if !watched[tgid]: return                                   │
│    if return_register == NULL: return  ← skip failed loads     │
│    ringbuf_submit({0xFFFFFFFF})  ← reserved JIT token          │
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
- `<name>.shimbin.json` — the stable `funkoverage-shim` binary path resolved at install time. The background helper (which no longer runs from `<name>`'s own path — see the process-identity section above) reads this instead of `os.Executable()` to know which binary to re-exec itself from.

Mangled names are mandatory: BPF uprobe attach resolves names against the ELF symbol table directly. Demangling happens **only at output time** (writes to `_called.log`, `_functions.log`, and stdout).

### Drain lock (`$LOG_DIR/.drain.lock`)

An advisory flock, not a sidecar of any one binary. The background helper holds a shared lock on it only while flushing its log after the traced process exits (not for the tracer's whole lifetime — a still-running daemon must never block this). `funkoverage report` waits (bounded, best-effort) to acquire it exclusively before reading any logs, so a `report` run immediately after a short-lived traced tool exits doesn't race the helper's now-asynchronous shutdown and undercount coverage.

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
│  background helper process (fork of the shim)       │
│                                                     │
│  main goroutine:                                    │
│    attach probes to target pid, start reader,       │
│    signal ready, waitForTargetExit (PDEATHSIG,      │
│    with a 5s liveness-poll backup)                  │
│                                                     │
│  reader goroutine (started by Tracer.Start):        │
│    loop:                                            │
│      record = reader.Read()  ← blocks on ringbuf    │
│      lookup func by index, demangle, write to log   │
│    exit on reader.Close() → ErrClosed               │
└─────────────────────────────────────────────────────┘
        │
        ├──► original shim process execs the real binary
        │    (same pid throughout) →
        │    kernel fires uprobes → ringbuf → helper's reader
        │
        └──► real daemon exits → PR_SET_PDEATHSIG fires (or, on a
             miss, the 5s liveness poll notices) →
             tracer.Stop():
             1. detach links (no new events)
             2. sleep 100ms (drain in-flight)
             3. close reader (reader goroutine exits)
             4. wg.Wait, close log, release objs
             (idempotent via sync.Once)
```

### Process identity, signal handling, and sd_notify (issues #143, #152)

Earlier versions forked a child to run the real daemon and kept the
original (supervisor-tracked) process alive as a relay: forwarding signals
to the child, and proxying `sd_notify` datagrams since systemd's default
`NotifyAccess=main` only trusts datagrams from the MainPID it's tracking —
which was the parent, not the child actually running the daemon. That fixed
sshd/tcpdump/apache/snapper (issue #143) but still broke rpcbind (systemd
socket activation checks `LISTEN_PID`, and on systemd ≥257 a pidfd-based
`LISTEN_PIDFDID` that can't be satisfied by a different process at all) and
postgres (`pg_ctl start -w` checks that the pid it forked matches the pid
postgres wrote to its own `postmaster.pid`) — both are supervisors checking
process identity in ways a forked child can never satisfy (issue #152).

The current design sidesteps the whole class of bug: the *original* shim
process — the one the supervisor exec'd and whose pid it's tracking —
`syscall.Exec`s into the real binary itself, in place. A background helper
(forked *before* that exec) owns the tracer and eBPF attachment instead, so
attaching to a pid still doesn't require running that pid's actual target
code inside the same process that hosts the Go runtime's ring-buffer
reader — exec would destroy that runtime, which is why a second process is
still unavoidable. What changed is *which* of the two processes keeps its
identity: now it's the one the supervisor already knows about.

Consequences:
- **No signal forwarding.** The supervisor's signals land on the real
  daemon directly (same pid) — there's nothing left to forward *to*.
- **No sd_notify relay.** The real daemon's `sd_notify()` calls go straight
  to the real `NOTIFY_SOCKET`, from the exact pid `NotifyAccess=main`
  expects.
- **No LISTEN_FDS/LISTEN_PID handling.** `syscall.Exec` in the same process
  preserves the fd table and pid exactly as systemd set them up — nothing
  to preserve or rewrite.
- **The helper can't `waitpid()` its own parent** (only a parent can wait on
  a child), so it uses `PR_SET_PDEATHSIG` to learn when the original process
  (now the real daemon) exits, whether normally or via crash.
- **PDEATHSIG alone isn't fully reliable, so it needs a backup.** Per
  `PR_SET_PDEATHSIG(2const)`'s CAVEATS, "the parent" is scoped to the
  specific *thread* that created the child, not the process in general —
  `runtime.LockOSThread()` (pinning the helper's goroutine to one OS thread
  for its whole life) closes the most common way Go code gets this wrong,
  but a real, direct leak was still observed on real hardware (a helper
  blocked on the signal channel for 50+ minutes after its target had
  genuinely exited). Units with the default `KillMode=control-group` mask a
  missed signal — systemd kills the whole cgroup, including the helper, on
  stop regardless — but `KillMode=process` units (sshd.service is the one
  exception among this project's tested targets) have no such backup.
  `waitForTargetExit` polls `kill(targetPid, 0)` every 5s alongside the
  signal channel, so a missed PDEATHSIG costs a few seconds of delayed
  cleanup instead of leaking the helper (and its attached tracer) forever.

### JIT Dynamic Library Tracing (dlopen)

To support coverage for dynamic libraries loaded on-the-fly at runtime via `dlopen()` or `dlmopen()`, the shim implements an event-driven Just-In-Time (JIT) instrumentation mechanism:

1. **eBPF Hooking:** During tracer startup, the shim locates the target's mapped `libc.so.6` or `libdl.so.2` by parsing `/proc/<child_pid>/maps`. It then attaches a `uretprobe` to the `dlopen` symbol, mapping it to the `trace_dlopen_return` eBPF handler.
2. **Detection:** When `dlopen` returns successfully (non-NULL handle), the eBPF program writes a special reserved token (`0xFFFFFFFF`) to the `events` ring buffer.
3. **Rescan & Enumerate:** The shim's userspace ringbuffer reader intercepts the `0xFFFFFFFF` event, triggers `handleDynamicLoad()`, and rescans `/proc/<child_pid>/maps` to identify newly mapped executable files (`PROT_EXEC` segments) that are not yet instrumented.
4. **JIT Attach:** For each new library, the shim:
   - Discovers functions via `enumerateOne()` using the saved sidecar filters (`--include` / `--exclude`).
   - Assigns global function indices/cookies sequentially, dynamically expanding its internal function list.
   - Attaches BPF multi-uprobes to the new symbols via a single `link.UprobeMulti()` call for the entire library.
   - Appends the newly discovered functions to the functions log (`_functions.log`) so they appear in reports.

This event-driven JIT approach scales efficiently to thousands of concurrent tracing processes without active polling, keeping idle CPU and I/O overhead at **0%**.

**Scope Boundary:** Because it relies on the public `dlopen` ELF symbol, it does not trace internal glibc library loads like NSS modules (e.g. `libnss_*.so` invoked by `getpwnam()`), which use glibc's private, non-exported `__libc_dlopen_mode` helper instead.

## Permissions model

- **Install**: must run as root (moves files in `/usr/bin`, runs `setcap`)
- **Runtime**: shim must have `cap_bpf`, `cap_perfmon`, `cap_dac_read_search` — granted via file capabilities at install time
- **`LOG_DIR` mode is `1777`** (world-writable + sticky, like `/tmp`): a shim installed with file capabilities can be run by any non-owning user, who must still be able to create their own `_called.log`; the sticky bit stops one user deleting another's. Not `0755` — that would let only the dir owner write.
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
│       ├── main.go           # shim main, background helper, exec-in-place
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
