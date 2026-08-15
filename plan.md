# funkoverage Feature & Enhancement Plan

This document outlines upcoming features, architectural improvements, and TODOs for the `funkoverage` project.

---

## 1. Avoidance of Double Shimming (Detecting Already Instrumented Binaries)

* **Goal**: Prevent `funkoverage install` from instrumenting a binary that is already a `funkoverage-shim` copy. This protection is critical to avoid recursive execution hangs, corrupt backup storage, or loss of the original native binary.
* **Approach**:
  1. During the installation phase inside `cmd/shim.go`, inspect the target binary before proceeding.
  2. Parse the target's ELF symbol table (using the standard `debug/elf` package).
  3. Search for known, shim-specific runtime symbols, such as:
     - `main.childMain`
     - `main.runWithTracing`
     - `main.calledLogPath`
  4. If any of these symbols are detected in the symbol table, immediately abort the installation with a user-friendly error (e.g., `"Error: binary is already a funkoverage-shim. Aborting to avoid double shimming."`).
  5. Add dedicated test cases in `cmd/funkoverage_test.go` to verify that attempting to install on a shim binary is correctly blocked.

---

## 2. Direct ELF Dependency Resolution (Eliminating `ldd` Fork-Exec)

* **Goal**: Completely eliminate running `/usr/bin/ldd` as an external subprocess.
* **Approach**: Parse the target ELF binary headers directly in Go (using the standard `debug/elf` package) to locate dynamic table tags (`DT_NEEDED` entries) and manually resolve dynamic dependency paths.
* **Benefit**: Removes fork-exec overhead, cuts down external system tool dependencies, and improves security compatibility on containerized/hardened environments.

---

## 3. Parent-Child Signal Forwarding (Completed)

* **Goal**: Ensure the shim parent process forwards terminal signals (like `SIGTERM`, `SIGINT`, `SIGHUP`, `SIGQUIT`) to the child process, so daemons or processes run under the shim can exit gracefully instead of being left orphaned.
* **Approach**:
  - Implemented signal capture using `os/signal` and `syscall` in `cmd/shim_binary/main.go`.
  - Added a goroutine to continuously relay any incoming signal to `childCmd.Process`.
  - Added deferred cleanup to safely close and unregister signal notification handlers when the parent exits.
* **Verification**:
  - Created a custom E2E test `tests/e2e/test_signal_forwarding.sh` to explicitly assert child exit gracefulness and exit code 0.
  - Integrated into container test suite runner `tests/e2e/run_all_container_tests.sh`.

---

## 4. systemd Type=notify Relay Proxy (Completed)

* **Goal**: Allow daemons configured as systemd `Type=notify` to successfully signal readiness and status when instrumented under the shim.
* **Approach**:
  - Intercepted the original `NOTIFY_SOCKET` environment variable in the parent shim.
  - Created a local UNIX domain datagram socket (`unixgram`) in a temporary directory and set it as the child's `NOTIFY_SOCKET`.
  - Spawned a background forwarding loop that reads notifications sent from the child and proxies/relays them directly to the original systemd `NOTIFY_SOCKET`.
  - This ensures systemd receives notifications coming from the main PID (the shim parent) and prevents service startup hangs.
* **Verification**:
  - Created a custom E2E test `tests/e2e/test_notify_proxy.sh` using a python-based unixgram receiver mock.

