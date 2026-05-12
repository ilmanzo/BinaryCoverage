# BinaryCoverage (funkoverage)

**BinaryCoverage** (renamed to **funkoverage**) is a native code coverage tool for GNU/Linux. Originally built on Intel Pin, it now uses **eBPF (uprobes)** for high-performance, system-wide function-level coverage without the overhead of dynamic binary instrumentation.

[![Build check](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/build.yml/badge.svg)](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/build.yml) [![Run unit tests](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/test.yml/badge.svg)](https://github.com/ilmanzo/BinaryCoverage/actions/workflows/test.yml)

## ✅ Supported Platforms

- **GNU/Linux** (x86_64)
- Requires **eBPF** support (Linux kernel 4.4+, 5.10+ recommended for ringbuffer)

## 📦 Prerequisites

- **Go 1.22+**
- **Clang/LLVM** (for BPF CO-RE compilation)
- **elfutils** (provides `eu-unstrip` for debug info merging)
- **bpftrace** (optional, used by the shim for tracing)

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

#### Tracing a command

```bash
./funkoverage trace /usr/bin/curl -- --version
```

#### Installing the shim

```bash
sudo ./funkoverage install /usr/bin/grep
grep "pattern" file.txt  # Automatically captures coverage to /tmp/
./funkoverage report /tmp/ ./coverage_report/
```

## 🧪 Running Unit Tests

```bash
./run_unit_tests.sh
```

## 📎 Technical Details

- **eBPF Uprobes**: Uses kernel uprobes to trigger events on function entry.
- **DWARF & Symbol Tables**: Automatically discovers functions via ELF symbols and DWARF debug info.
- **DWZ Support**: Handles compressed debug information (common in openSUSE/Fedora).
- **Multi-Library Tracing**: Can simultaneously trace the main binary and all linked shared libraries.
