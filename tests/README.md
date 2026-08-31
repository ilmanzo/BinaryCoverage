# BinaryCoverage (funkoverage) Test Suite

This directory contains the automated and manual testing tools for the `funkoverage` code coverage framework.

The test suite is structured into two main tiers:
1. **Unit Tests**: Safe, non-privileged tests covering compiler and parser utilities.
2. **End-to-End (E2E) Tests**: Privileged tests running on real system binaries to verify the eBPF uprobe engine, daemon tracking, dynamic library attachment, and report generation.

---

## 1. Unit Tests

- **Location**: Executed via the `./run_unit_tests.sh` script at the root directory.
- **Privileges**: No root or BPF privileges required.
- **What they cover**:
  - `internal/funkutil/`: Log parsing, regex-free scanners, sidecar JSON reading/writing, and `.symtab` / `.dynsym` symbol parsing.
  - `cmd/`: Configuration parsing, templates, and basic UI generation.
- **How to run**:
  ```bash
  ./run_unit_tests.sh
  ```

---

## 2. End-to-End (E2E) Sample Testing

Verify basic binary tracing quickly using a custom target application:
- **Location**: `tests/sample/`
- **Privileges**: Requires `sudo` (for BPF uprobes loading).
- **How to run**:
  ```bash
  # Build the sample binary
  cd tests/sample && make && cd ../..

  # Trace execution
  sudo ./funkoverage trace tests/sample/sample -- --strings

  # Generate text/HTML reports
  sudo ./funkoverage report /var/coverage/data /tmp/report_sample --formats txt,html
  ```

---

## 3. Distro System Binary E2E Tests

These tests instrument real system utilities on the host to verify deep features like dynamic loading (`dlopen`), C++ demangling, `.gnu_debuglink` fallback, and in-place debug info merges.

- **Location**: `tests/e2e/`
- **Privileges**: Must run as `root` (with `/sys/kernel/btf/vmlinux` available).
- **Required Host OS**: openSUSE / SUSE (scripts dynamically invoke `zypper` to fetch debug symbols).

### Distro Test Directory:

| Script | Binary | Key Feature Verified | Required Packages |
|---|---|---|---|
| `test_bzip2.sh` | `/usr/bin/bzip2` | Basic lifecycle: install, trace, report, uninstall | `bzip2`, `bzip2-debuginfo` |
| `test_gzip.sh` | `/usr/bin/gzip` | Script wrappers (`gunzip`, `zcat`), multi-file and recursive runs | `gzip`, `gzip-debuginfo` |
| `test_gmp.sh` | Custom C app | In-place debugmerge to trace library-local static symbols (e.g., `mpn_*` internal helpers) | `gmp-devel`, `libgmp10-debuginfo` |
| `test_cpupower.sh` | `/usr/bin/cpupower` | `.gnu_debuglink` resolution when `.build-id` symlinks are broken/missing | `cpupower`, `cpupower-debuginfo` |
| `test_openssl.sh` | `/usr/bin/openssl` | Multi-library tracing (`libssl.so`, `libcrypto.so`) | `openssl`, debug packages |
| `test_squid.sh` | `/usr/sbin/squid` | C++ demangling, long-running daemon attachment, fork-tracking | `squid`, `squid-debuginfo`, `curl` |
| `test_nginx_dlopen.sh` | `/usr/sbin/nginx` | `dlopen()` JIT uprobe attachment on dynamically loaded web modules | `nginx`, debug packages |
| `test_pam_dlopen.sh` | `/usr/sbin/pam` | PAM modules loaded via `dlopen()` tracing | `pam`, `pam-debuginfo` |
| `test_shim_fail_open.sh` | `/usr/bin/bzip2` | Shim still execs the real binary when the tracer can't attach (seccomp blocks `perf_event_open`); needs a running systemd, self-skips otherwise | `bzip2`, `bzip2-debuginfo` |

- **How to run a specific test**:
  ```bash
  sudo -E bash tests/e2e/test_bzip2.sh
  ```

---

## 4. Containerized E2E Testing (Leap 16.0 & Tumbleweed)

To run all major system binary E2E tests in a clean, reproducible, and isolated environment, use the openSUSE Leap 16.0 or Tumbleweed containerized test runners.

- **Location**: `tests/e2e/test_container_leap16.sh`, `tests/e2e/test_container_tumbleweed.sh`, `tests/e2e/run_all_container_tests.sh`, and `tests/e2e/Containerfile`.
- **Privileges**: Requires `sudo` (needs `--privileged` container runtime capabilities and `/sys/` resource sharing).
- **Prerequisites**: `podman` (preferred) or `docker` installed.
- **What they do**:
  1. Build a custom container image (`leap16-e2e` or `tumbleweed-e2e`) from the shared `Containerfile` via build-args.
  2. The image contains Go, GCC, make, elfutils, gawk, and all debuginfo packages for tested binaries.
  3. Image builds are cached, making repeat local execution extremely fast.
  4. Launches the container in the host's PID namespace and mounts `/sys/kernel/btf`, `/sys/kernel/tracing`, and `/sys/kernel/debug`.
  5. Automatically executes a shared runner (`run_all_container_tests.sh`) to build `funkoverage` and run tests for: `bzip2`, `gzip`, `gmp`, `cpupower`, `openssl`, `squid`, `nginx_dlopen`, and `duplicate_library_symlink`.
- **How to run openSUSE Leap 16.0 container tests**:
  ```bash
  ./tests/e2e/test_container_leap16.sh
  ```
- **How to run openSUSE Tumbleweed container tests**:
  ```bash
  ./tests/e2e/test_container_tumbleweed.sh
  ```
