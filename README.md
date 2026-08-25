# BinaryCoverage (funkoverage)

**BinaryCoverage** (binary: `funkoverage`) is a native function-level code coverage tool for GNU/Linux. It uses **eBPF uprobes** (`uprobe_multi`) to capture function entry events from any ELF binary — no source code, no recompilation, no debugger.

**Documentation:**
- [docs/install.md](docs/install.md) — Fresh-system installation guide
- [docs/design.md](docs/design.md) — Architecture diagrams and internals
- [docs/dlopen_realworld_testing.md](docs/dlopen_realworld_testing.md) — Real-world VM validation runbook for dlopen() JIT tracing

[![Build check](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/build.yml/badge.svg)](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/build.yml) [![Run unit tests](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/test.yml/badge.svg)](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/test.yml) [![codecov](https://codecov.io/gh/ilmanzo/BinaryCoverage/graph/badge.svg)](https://codecov.io/gh/ilmanzo/BinaryCoverage)

## ✅ Supported Platforms

- **GNU/Linux** (x86_64, ARM64)
- Requires **eBPF** support (Linux kernel 6.6+ with `CONFIG_DEBUG_INFO_BTF=y`)

## 📦 Prerequisites

- **Go 1.27+**
- **Clang/LLVM** (only for BPF regeneration with `REGEN_BPF=1`)
- **bpftool** (only for BPF regeneration with `REGEN_BPF=1`)
- **libbpf-devel** (only for BPF regeneration with `REGEN_BPF=1`)
- **elfutils** (provides `eu-unstrip` for debug info merging)

## 🛠️ Build & Run

### 🔧 Build

```bash
./build.sh
```

This compiles the BPF tracer and the `funkoverage` CLI tool.

### ▶️ Usage

`funkoverage` supports several subcommands:

- **`enumerate`**: List all discoverable functions in a binary and its shared libraries.
- **`trace`**: Run a binary and capture coverage on-the-fly.
- **`install`**: Replace a binary with a transparent shim that captures coverage every time it runs.
- **`uninstall`**: Restore the original binary.
- **`report`**: Generate HTML, Text, or XML reports from captured logs.

All enumeration commands (`enumerate`, `install`, `trace`) support function filtering:
- `--include RE` — only trace functions whose demangled name matches the regex
- `--exclude RE` — skip functions whose demangled name matches the regex

#### Tracing a command

```bash
./funkoverage trace /usr/bin/curl -- --version

# Trace only SSL-related functions
./funkoverage trace --include "^SSL_" /usr/bin/curl -- https://example.com
```

#### Installing the shim

```bash
sudo ./funkoverage install /usr/bin/grep
grep "pattern" file.txt  # Automatically captures coverage
./funkoverage report /var/coverage/data ./coverage_report/
```

#### Enumerating functions

```bash
# List all functions
./funkoverage enumerate /usr/bin/openssl

# Only math-related, excluding internal helpers
./funkoverage enumerate --include "^math_" --exclude "is_" tests/sample/sample
```

## 🧪 Running Unit Tests

```bash
./run_unit_tests.sh
```

Pre-captured coverage logs are available in `tests/sample_data/` to develop or test report generation without needing a live eBPF environment:

```bash
./funkoverage report tests/sample_data/ /tmp/report/
```

## 📎 Technical Details

- **eBPF Uprobes**: Uses kernel uprobes to trigger events on function entry.
- **DWARF & Symbol Tables**: Automatically discovers functions via ELF symbols and DWARF debug info.
- **DWZ Support**: Handles compressed debug information (common in openSUSE/Fedora).
- **Multi-Library Tracing**: Can simultaneously trace the main binary and all linked shared libraries.
- **Function Filtering**: `--include`/`--exclude` regex filters to focus on specific namespaces.
- **`dlopen()` Support**: Dynamically loaded libraries are traced on-the-fly via JIT instrumentation of `dlopen` calls using eBPF uretprobes and automatic maps monitoring. **Known limitation**: this only hooks the *public* `dlopen()` ELF symbol. glibc's own NSS module loading (`getpwnam`, `gethostbyname`, etc. pulling in `libnss_*.so`) goes through an internal, non-exported `__libc_dlopen_mode` instead, so NSS modules are never JIT-instrumented — verified on real hardware, see [docs/dlopen_realworld_testing.md](docs/dlopen_realworld_testing.md#7-nss-known-limitation). Everything else that calls the public `dlopen()` (PAM modules, nginx dynamic modules, application plugins, etc.) is captured correctly.

## 🤝 Contributing

Want to help? Start by getting confidence with the project: build it (`./build.sh`), run the tests (`./run_unit_tests.sh`), try it on a sample binary (`./funkoverage enumerate tests/sample/sample`), see what works and what doesn't, and suggest improvements. Check the [good first issue](https://github.com/ilmanzo/BinaryCoverage/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22) label if you want a place to start.

Since this traces live binaries via eBPF, install a spare, clean Tumbleweed VM as your test bed rather than running it on your main machine.

Remember: fun is the first thing (fun - coverage).

## License

Dual licensed: **MIT** (Go userspace code) and **GPL-2.0-only** (eBPF kernel code in `cmd/shim_binary/bpf/`). The eBPF programs must be GPL to use GPL-only BPF kernel helpers.
