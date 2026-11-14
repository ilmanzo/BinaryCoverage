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

These tests instrument real system utilities on the host to verify deep features like dynamic loading (`dlopen`), C++ demangling, `.gnu_debuglink` fallback, and address-based external debug info attachment.

- **Location**: `tests/e2e/`
- **Privileges**: Must run as `root` (with `/sys/kernel/btf/vmlinux` available).
- **Required Host OS**: openSUSE / SUSE (scripts dynamically invoke `zypper` to fetch debug symbols).

### Distro Test Directory:

| Script | Binary | Key Feature Verified | Required Packages |
|---|---|---|---|
| `test_bystander_traps.sh` | Custom C app (`bystander.c`, `libhot.so`) | PID-scoped uprobes on shared libraries (issue #163): untraced bystander processes calling shared library run at native speed; forked child processes remain traced | `gcc` |
| `test_bzip2.sh` | `/usr/bin/bzip2` | Basic lifecycle: install, trace, report, uninstall | `bzip2`, `bzip2-debuginfo` |
| `test_cpupower.sh` | `/usr/bin/cpupower` | `.gnu_debuglink` resolution when `.build-id` symlinks are broken/missing | `cpupower`, `cpupower-debuginfo` |
| `test_duplicate_library_symlink.sh` | Custom C app | Handling duplicate library paths and symlink resolution without redundant uprobe attachment (issue #150) | `gcc` |
| `test_glibc_hwcaps_resolution.sh` | `/usr/bin/bzip2` | Resolves and attaches uprobes to architecture-optimized glibc-hwcaps libraries | `bzip2`, `bzip2-debuginfo` |
| `test_gmp.sh` | Custom C app | Address-based external debug info attachment to trace library-local static symbols (e.g., `mpn_*` internal helpers) | `gmp-devel`, `libgmp10-debuginfo` |
| `test_gzip.sh` | `/usr/bin/gzip` | Script wrappers (`gunzip`, `zcat`), multi-file and recursive runs | `gzip`, `gzip-debuginfo` |
| `test_nginx_dlopen.sh` | `/usr/sbin/nginx` | `dlopen()` JIT uprobe attachment on dynamically loaded web modules | `nginx`, debug packages |
| `test_nss_dlopen.sh` | Custom C app | Verifies/documents glibc internal `__libc_dlopen_mode` NSS module behavior | `gcc` |
| `test_openssl.sh` | `/usr/bin/openssl` | Multi-library tracing (`libssl.so`, `libcrypto.so`) | `openssl`, debug packages |
| `test_pam_dlopen.sh` | `/usr/sbin/pam` | PAM modules loaded via `dlopen()` tracing | `pam`, `pam-debuginfo` |
| `test_rust_ripgrep.sh` | `/usr/bin/rg` | Rust symbol demangling (v0 and legacy schemes) on stripped system binaries | `ripgrep`, `ripgrep-debuginfo` |
| `test_shim_fail_open.sh` | `/usr/bin/bzip2` | Shim still execs the real binary when the tracer can't attach (seccomp blocks `perf_event_open`, or kills on any `bpf(2)` call: issue #173); needs a running systemd, self-skips otherwise | `bzip2`, `bzip2-debuginfo` |
| `test_sigchld_reaper.sh` | Custom C daemon (`reaper.c`) | Coverage helper isolation from traced process tree (issue #167): helper is never a child, never reaped by SIGCHLD handler (vsftpd crash), traced until exit, and no zombie on fail-open | `gcc`, running `systemd` (optional, for seccomp subtest) |
| `test_signal_and_notify_relay.sh` | Custom C daemon | Signal forwarding (SIGHUP, SIGTERM, etc.) from shim to child and systemd `sd_notify` socket relay (issue #143) | `gcc` |
| `test_squid.sh` | `/usr/sbin/squid` | C++ demangling, long-running daemon attachment, fork-tracking | `squid`, `squid-debuginfo`, `curl` |

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
  2. The image contains Go, GCC, make, gawk, and all debuginfo packages for tested binaries.
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

---

## 5. VM-based E2E Testing

Because `funkoverage` instruments live binaries via eBPF and interacts with low-level process lifecycle mechanisms (e.g. systemd seccomp filters, PID namespaces, signals, and process tree reaping), running tests inside a dedicated virtual machine provides the highest fidelity test environment without polluting your host system.

- **Location**: `tests/e2e/run_vm_tests.sh`

### Prerequisites & VM Setup

1. **Operating System**: A clean openSUSE Tumbleweed or Leap 16.0 VM (e.g. managed via KVM/libvirt as domain `tw_bcov`).
2. **Kernel BTF**: Ensure kernel BTF support is present:
   ```bash
   ls -lh /sys/kernel/btf/vmlinux
   ```
3. **Debug Repositories**: Enable the openSUSE debug repository so tests can install `-debuginfo` packages on demand:
   ```bash
   sudo zypper mr -e repo-debug   # or add explicitly:
   sudo zypper ar -f http://download.opensuse.org/debug/tumbleweed/repo/oss/ 'Main Repository (DEBUG)'
   ```
4. **Toolchain & Dependencies**: Install the required compiler, build tools, and BPF headers:
   ```bash
   sudo zypper -n install go gcc binutils make rsync gawk clang llvm bpftool libbpf-devel
   ```
5. **SSH Access**: Copy your SSH public key from the host to the VM:
   ```bash
   ssh-copy-id user@<vm_ip>   # e.g., andrea@192.168.122.238 or root
   ```
6. **Sudo Privileges**: If connecting as a non-root user, ensure passwordless sudo is configured (the test runner invokes tests via `sudo -E`):
   ```bash
   echo "$USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/$USER
   ```

### What the Runner Does

The runner script (`tests/e2e/run_vm_tests.sh`) automates the full test cycle:
1. **Host Discovery**: Detects the VM host via libvirt domain inspection (`tw_bcov` by default) or command-line flags/environment variables.
2. **Auto-boot (optional)**: Starts the domain automatically if currently shut off when `--start` is passed.
3. **Sync**: Incrementally synchronizes the workspace to the VM using `rsync` (excluding `.git/`, `.tokensave/`, and local binaries).
4. **Native Build**: Compiles `funkoverage` and `funkoverage-shim` natively inside the VM environment.
5. **Execution**: Runs the entire test suite or only selected tests with real-time log streaming.
6. **Reporting**: Displays elapsed times per test and an overall pass/fail summary report.

### Usage Examples

- **Run all E2E tests** (auto-discovers running VM):
  ```bash
  ./tests/e2e/run_vm_tests.sh
  ```

- **Run a single test** (exact script name or fuzzy shorthand):
  ```bash
  ./tests/e2e/run_vm_tests.sh test_sigchld_reaper.sh
  ./tests/e2e/run_vm_tests.sh reaper
  ./tests/e2e/run_vm_tests.sh bystander
  ./tests/e2e/run_vm_tests.sh bzip2
  ```

- **Run multiple specific tests**:
  ```bash
  ./tests/e2e/run_vm_tests.sh reaper bystander openssl
  ```

- **Auto-start libvirt VM if shut off**:
  ```bash
  ./tests/e2e/run_vm_tests.sh --start reaper
  ```

- **Target a specific VM or user**:
  ```bash
  ./tests/e2e/run_vm_tests.sh --host 192.168.122.238 --user andrea reaper
  ./tests/e2e/run_vm_tests.sh -t andrea@192.168.122.238 reaper
  ```

- **Using environment variables**:
  ```bash
  export VM_HOST=192.168.122.238
  export VM_USER=andrea
  ./tests/e2e/run_vm_tests.sh reaper
  ```

- **Fast iteration (skip sync and build)**:
  ```bash
  # Useful when iterating rapidly on a test script already synced:
  ./tests/e2e/run_vm_tests.sh --no-sync --no-build reaper
  ```

- **Clean coverage data before testing**:
  ```bash
  ./tests/e2e/run_vm_tests.sh --clean reaper
  ```

- **Preview target and plan without executing**:
  ```bash
  ./tests/e2e/run_vm_tests.sh --dry-run reaper
  ```

- **List all available tests**:
  ```bash
  ./tests/e2e/run_vm_tests.sh --list
  ```

### Troubleshooting & Gotchas

- **VM is shut off**: Pass `--start` to instruct `virsh` to boot the domain and wait for network initialization, or start it manually (`virsh -c qemu:///system start tw_bcov`).
- **Permission denied (publickey)**: Ensure your host's SSH key is added to `~/.ssh/authorized_keys` for the target VM user.
- **Sudo password prompt**: Tests require root privileges to attach eBPF uprobes. Configure passwordless sudo in `/etc/sudoers.d/` for non-root users.
- **Rolling-release package skew**: On Tumbleweed, upstream repo rebuilds can temporarily cause the main OSS repo to lag behind the DEBUG repo. If `funkoverage` or `zypper` reports build-id or package mismatch, run `sudo zypper refresh && sudo zypper -n dup` inside the VM.
