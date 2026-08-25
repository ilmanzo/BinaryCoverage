# Installation Guide

Fresh-system installation verified on **openSUSE Tumbleweed 20260511** with kernel 7.0.5.

## System Requirements

| Requirement | Minimum | Verified |
|---|---|---|
| **OS** | GNU/Linux x86_64 or ARM64 | openSUSE Tumbleweed |
| **Kernel** | 6.6+ | 7.0.5 |
| **BTF** | `CONFIG_DEBUG_INFO_BTF=y` | `/sys/kernel/btf/vmlinux` (5.7MB) |
| **Go** | 1.27+ | 1.27.0 |

Check BTF availability:
```bash
ls -lh /sys/kernel/btf/vmlinux
# Should show a file, not "No such file"
```

## Dependencies

### Build-time (required)

```bash
# openSUSE/SUSE
sudo zypper install go1.27 elfutils make gcc-c++

# Fedora/RHEL
sudo dnf install golang elfutils make gcc-c++

# Debian/Ubuntu
sudo apt install golang elfutils make g++
```

### BPF regeneration (optional)

Only needed if editing `cmd/shim_binary/bpf/tracer.bpf.c`. The repo ships pre-generated `tracer_x86_bpfel.{go,o}`.

```bash
# openSUSE/SUSE
sudo zypper install clang llvm bpftool libbpf-devel

# Fedora/RHEL
sudo dnf install clang llvm bpftool libbpf-devel

# Debian/Ubuntu
sudo apt install clang llvm bpftool libbpf-dev
```

### Runtime (install/trace)

```bash
# openSUSE/SUSE
sudo zypper install libcap-progs elfutils

# Fedora/RHEL
sudo dnf install libcap elfutils

# Debian/Ubuntu
sudo apt install libcap2-bin elfutils
```

**libcap-progs** provides `setcap` (file capabilities). **elfutils** provides `eu-unstrip` (merges external debug info).

### Testing (optional)

```bash
# openSUSE/SUSE
sudo zypper install python3

# For E2E system tests, install binaries + debug symbols:
sudo zypper install bzip2 bzip2-debuginfo
sudo zypper install squid squid-debuginfo
sudo zypper install openssl openssl-3-debuginfo libopenssl3-x86-64-v3-debuginfo
```

## Build

```bash
git clone https://github.com/ilmanzo/BinaryCoverage.git
cd BinaryCoverage
./build.sh
```

Output:
- `funkoverage` — CLI (enumerate, install, trace, report)
- `funkoverage-shim` — runtime shim (auto-located by CLI)

To regenerate BPF bindings after editing C code:
```bash
REGEN_BPF=1 ./build.sh
```

## Verification

### Unit tests
```bash
./run_unit_tests.sh
```

Expected: ~43 tests pass.

### E2E with sample binary
```bash
cd tests/sample && make && cd ../..

# Trace the sample binary (no install needed)
sudo ./funkoverage trace tests/sample/sample -- --strings

# Check coverage
sudo ./funkoverage report /var/coverage/data /tmp/report --formats txt
```

Expected: `strings: ok`, coverage report shows functions from `tests/sample/sample`.

### E2E with system binaries

Requires root + debug symbols installed.

```bash
# bzip2
sudo bash tests/e2e/test_bzip2.sh

# squid (installs squid if missing, starts/stops daemon)
sudo bash tests/e2e/test_squid.sh

# openssl (requires openssl + debug packages)
sudo bash tests/e2e/test_openssl.sh
```

Expected: `ALL TESTS PASSED` for each.

### Python E2E suite
```bash
sudo python3 tests/e2e/test_coverage.py
```

Expected: 16 tests, 14 pass, 2 skipped (system tests need `FUNKOVERAGE_SYSTEM_TEST=1`).

## Installation (optional)

To make `funkoverage` available system-wide:

```bash
sudo install -m 755 funkoverage /usr/bin/funkoverage
sudo install -m 755 funkoverage-shim /usr/lib64/coverage-tools/funkoverage-shim
```

Or add to `PATH`:
```bash
export PATH="$PWD:$PATH"
```

The CLI locates the shim via:
1. `FUNKOVERAGE_SHIM` env var
2. `./funkoverage-shim` (same directory)
3. `/usr/lib64/coverage-tools/funkoverage-shim`

## Common Issues

### `setcap: command not found`

Install `libcap-progs` (openSUSE) or `libcap2-bin` (Debian/Ubuntu).

### `no matching files found: tracer_x86_bpfel.o`

The pre-generated `.o` file is checked into git. If missing:
```bash
REGEN_BPF=1 ./build.sh
```

Requires clang, llvm, bpftool, libbpf-devel.

### `CONFIG_DEBUG_INFO_BTF not enabled`

Your kernel lacks BTF. Rebuild kernel with `CONFIG_DEBUG_INFO_BTF=y` or use a distro kernel that ships BTF (most recent openSUSE/Fedora/Ubuntu kernels do).

### Debug info package names

| Distro | Pattern |
|---|---|
| openSUSE/SUSE | `<package>-debuginfo`, `<package>-debugsource` |
| Fedora/RHEL | `<package>-debuginfo` |
| Debian/Ubuntu | `<package>-dbgsym` (needs `ddebs` repo) |

Example for openssl on openSUSE:
```bash
zypper install openssl-3-debuginfo libopenssl3-x86-64-v3-debuginfo
```

### Permission denied during trace

`funkoverage trace` needs root or `CAP_BPF`/`CAP_PERFMON` to load BPF programs:
```bash
sudo ./funkoverage trace /usr/bin/curl -- --version
```

`funkoverage install` always requires root (modifies `/usr/bin`, runs `setcap`).

## Next Steps

See [docs/design.md](design.md) for architecture details and [README.md](../README.md) for usage examples.
