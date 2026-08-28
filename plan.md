# funkoverage Feature & Enhancement Plan

This document outlines upcoming features, architectural improvements, and TODOs for the `funkoverage` project.

---

## 1. Avoidance of Double Shimming (Detecting Already Instrumented Binaries)

* **Goal**: Prevent `funkoverage install` from instrumenting a binary that is already a `funkoverage-shim` copy. This protection is critical to avoid recursive execution hangs, corrupt backup storage, or loss of the original native binary.
* **Approach**:
  1. During the installation phase inside `cmd/shim.go`, inspect the target binary before proceeding.
  2. Parse the target's ELF symbol table (using the standard `debug/elf` package).
  3. Search for known, shim-specific runtime symbols, such as:
     - `main.runWithTracing`
     - `main.runHelper`
     - `main.calledLogPath`
  4. If any of these symbols are detected in the symbol table, immediately abort the installation with a user-friendly error (e.g., `"Error: binary is already a funkoverage-shim. Aborting to avoid double shimming."`).
  5. Add dedicated test cases in `cmd/funkoverage_test.go` to verify that attempting to install on a shim binary is correctly blocked.

---

## 2. Direct ELF Dependency Resolution (Eliminating `ldd` Fork-Exec)

* **Goal**: Completely eliminate running `/usr/bin/ldd` as an external subprocess.
* **Approach**: Parse the target ELF binary headers directly in Go (using the standard `debug/elf` package) to locate dynamic table tags (`DT_NEEDED` entries) and manually resolve dynamic dependency paths.
* **Benefit**: Removes fork-exec overhead, cuts down external system tool dependencies, and improves security compatibility on containerized/hardened environments.

---

## 3. openQA Debuginfo Coverage for Transitively Loaded Extensions

The `enumerateOne` short-circuit this section originally tracked is fixed —
enumeration now prefers an external debug file's `.symtab` over the runtime
file's own. What remains is the openQA-side packaging gap that investigating it
turned up, which lives in a different repo.

* **`tests/coverage/coverage_setup.pm` in os-autoinst-distri-opensuse**: reproducing the `_bz2.so` gap required *two* debuginfo packages, not one — `python313-debuginfo` alone doesn't cover it. `gdb` (the coverage target) links `libpython3.13`, which dlopens `python313-base`'s `lib-dynload/*.so` — a *different*, split-off subpackage (plus its CPU-variant `python313-base-x86-64-v3`), each needing its own `-debuginfo`. `coverage_setup.pm`'s current `push @packages, $pkg, $pkg . '-debuginfo'` only ever requests debuginfo for the target package itself, not for packages pulled in transitively by ldd/dlopen. A TODO is left at that line; worth acting on if/when Python-loaded native extensions become an intentional coverage target rather than a side effect of instrumenting `gdb`.
* **Unrelated, and not a bug**: the "1 function" reports for postfix/rpm/sestatus are correct. Those are real thin CLI wrappers (confirmed via `gdb -batch -ex 'info functions'`); their logic lives in shared libs, reported separately as `binary_coverage_lib*`.

---
