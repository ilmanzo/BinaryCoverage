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
