# AGENTS.md

This file provides guidance to coding agents when working with code in this repository.

## What This Is

**funkoverage** — function-level code coverage via native eBPF (uprobe_multi). No source code needed, no recompilation. Installs a self-contained Go ELF shim in place of the original binary; at runtime, the shim attaches uprobes via `cilium/ebpf` to every enumerated function and emits a CALLED log used to build HTML/XML/text coverage reports.

For full architecture details, see [docs/design.md](docs/design.md).

## Build

```bash
./build.sh          # produces: funkoverage, funkoverage-shim
```

Incremental:
```bash
go build -o funkoverage ./cmd/
go build -o funkoverage-shim ./cmd/shim_binary/
cd tests/sample && make    # compile 100-function C++ test binary
```

The repo ships pre-generated BPF bindings for x86_64 and ARM64 (`cmd/shim_binary/tracer_{x86,arm64}_bpfel.{go,o}`); a normal build needs only Go ≥1.27. Build tags ensure the correct variant is compiled for each architecture. To regenerate after editing `cmd/shim_binary/bpf/tracer.bpf.c`:

```bash
REGEN_BPF=1 ./build.sh    # needs clang, llvm-strip, bpftool, libbpf-devel
```

## Tests

```bash
./run_unit_tests.sh                            # Go unit tests

# Single Go test
go test -v -run TestIsELF ./cmd/

# E2E (requires root or a shim binary with CAP_BPF)
sudo python3 tests/e2e/test_coverage.py

# E2E with system binary tests
sudo FUNKOVERAGE_SYSTEM_TEST=1 python3 tests/e2e/test_coverage.py

# E2E system binary tests (run as root on openSUSE, standalone)
sudo bash tests/e2e/test_bzip2.sh
sudo bash tests/e2e/test_squid.sh
sudo bash tests/e2e/test_openssl.sh
```

## Architecture

Two binaries work together:

**1. `funkoverage` CLI (`./cmd/`)**
Commands: `setup`, `install`, `uninstall`, `trace`, `enumerate`, `report`, `version`.
All commands that enumerate functions (`install`, `trace`, `enumerate`) accept `--include RE` and `--exclude RE` to filter functions by demangled name regex.
- `setup`: validates the eBPF environment (kernel ≥6.6, BTF available, log/bin dirs writable). Capabilities are applied per-shim at install time, NOT here.
- `install <binary>`: moves real binary to `$SAFE_BIN_DIR/<basename>`, enumerates functions → `_functions.log` and `<basename>.funcs.json` sidecar, copies shim to original path, runs `setcap cap_bpf,cap_perfmon,cap_dac_read_search+ep` on the copy.
- `uninstall <binary>`: reverses install (removes sidecars, restores original).
- `enumerate <binary>`: lists discovered functions to stdout (debug aid).
- `report <logdir> <outdir>`: reads `_functions.log` + `_called.log` files, writes coverage reports.

**2. `funkoverage-shim` (`./cmd/shim_binary/`)**
Installed transparently in place of the real binary. When invoked:
1. Detects recursion via `FUNKOVERAGE_ACTIVE` env var (if set, exec real binary directly).
2. Re-invokes itself as a "child" process with a pipe fd for coordination.
3. Loads embedded BPF program (`tracer_x86_bpfel.go`), reads `<safePath>.funcs.json`, attaches all uprobes against the main image + libraries via `link.UprobeMulti` (single syscall per image).
4. Seeds the watched-pid set with the child's TGID; fork tracepoint propagates to children.
5. Unblocks child via pipe; child `exec()`s real binary.
6. Ringbuf reader goroutine drains kernel events → demangle → `_called.log`.
7. On child exit, detaches links and closes log.

**Test binary**: `tests/sample/` — 100 C++ functions in 4 groups (`str_*`, `math_*`, `arr_*`, `util_*`). CLI: `--strings`, `--math`, `--arrays`, `--utils`, `--all`.

### Data Flow

```
install:  ELF binary → SAFE_BIN_DIR/<name>  +  shim copied to original path
                     → SAFE_BIN_DIR/<name>.funcs.json  (per-image symbol list, runtime input)
                     → SAFE_BIN_DIR/<name>.libs.json   (library paths, runtime input)
                     → LOG_DIR/<name>_*_functions.log  (textual enumeration, report input)
run:      shim → uprobe_multi (kernel) → ringbuf → LOG_DIR/<name>_*_called.log
report:   _functions.log + _called.log → coverage → HTML/XML/text
```

### Log Formats

`_functions.log` (written at install time, used by report):
```
FUNC /path/to/image funcname
```

`_called.log` (written by shim at runtime, demangled, used by report):
```
CALLED /path/to/image funcname
```

### Key Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `LOG_DIR` | `/var/coverage/data` | Where runtime logs are written |
| `SAFE_BIN_DIR` | `/var/coverage/bin` | Where original binaries + sidecars are stored |
| `FUNKOVERAGE_SHIM` | (searched) | Path to `funkoverage-shim` binary |
| `FUNKOVERAGE_ACTIVE` | (runtime) | Recursion guard in shim |

### Sidecar files (no JSON config in env)

The shim locates the real binary via `filepath.Join(SAFE_BIN_DIR, filepath.Base(os.Executable()))`. Beside it:
- `<basename>.libs.json` — library paths to attach uprobes against (from `ldd`).
- `<basename>.funcs.json` — `{image: [func, ...]}` map seeding `link.UprobeMulti`.

### Shared package (`internal/funkutil`)

Both `cmd/` binaries are separate `package main` and both import `internal/funkutil`. Key exports:

- `LogDir() / SafeBinDir()` — env-backed defaults for `LOG_DIR` / `SAFE_BIN_DIR`
- `WriteFuncList / ReadFuncList(safePath)` → `<safePath>.funcs.json` (image→[]func map)
- `WriteLibsSidecar / ReadLibsSidecar(safePath)` → `<safePath>.libs.json`
- `StripVersion(name)` — strips `@VERSION` suffix from symbol names

### DWARF enumeration (`cmd/enumerate.go`)

Uses Go stdlib `debug/dwarf`. Visits `DW_TAG_subprogram` entries with `DW_AT_LowPc` (skip abstract origins). Prefers `DW_AT_linkage_name` (mangled), falls back to `DW_AT_name`. Applies `demangle.Filter()` + version suffix stripping. Falls back to `.symtab` when no `.debug_info`. Handles external `.build-id` debug files via `eu-unstrip`.

### eBPF program (`cmd/shim_binary/bpf/tracer.bpf.c`)

- `uprobe.multi/probe`: per-call entry. Filters on watched TGID, dedupes via atomic CAS on a u64 array sized to total funcs, emits `{u32 func_idx}` to ringbuf on first hit.
- `tp/sched/sched_process_fork`: copies parent's watched bit to child so tracing follows fork().
- Cookies: at attach, user space passes `[]uint64` = global func indices; BPF reads via `bpf_get_attach_cookie(ctx)`. User space holds the parallel `[]FuncRef{Image, Name}` slice for cookie → name resolution.
- Maps die with the shim invocation; no `sched_process_exit` cleanup (would race with execve's de_thread).

## Constraints

- **x86_64 or ARM64 Linux only**
- **Kernel ≥6.6** (uprobe_multi)
- **CONFIG_DEBUG_INFO_BTF=y** (BTF for CO-RE relocations)
- `eu-unstrip` (elfutils) needed only for binaries with separate `.debug` files
- `setcap` is invoked at install time on each shim copy; install must run as root
- Go module root is repo root (`go.mod` at `/`); requires Go ≥1.27
