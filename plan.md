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

## 3. Fix `enumerateOne` Short-Circuit on Sparse Own-Symtab

* **Found**: investigating openQA job 6163815 (`binary_coverage_ultimate`) reports of "1 function" for postfix/rpm/sestatus. Those three are correct (real thin CLI wrappers — confirmed independently via `gdb -batch -ex 'info functions'`; real logic lives in their shared libs, reported separately as `binary_coverage_lib*`).
* **But**: the same "1 function" pattern for CPython extension modules (`_bz2.cpython-313...so`, `_asyncio...so`, etc. — pulled in because `gdb` is a coverage target and links `libpython3.13`) is a genuine bug, not the same thing. `_bz2.so`'s own `.dynsym` exports exactly one symbol (`PyInit__bz2` — CPython convention, real implementation is `static`), but its external debug file's `.symtab` (once `python313-base-debuginfo` is installed) has 18 real functions (`_bz2_BZ2Compressor_compress`, `compress`, ...) that are never consulted.
* **Root cause**: `enumerateOne` (`cmd/enumerate.go:101`) returns on the first non-empty result — `symtabFunctions(path)` finds 1 and returns immediately, never reaching the external-debug-file check.
* **Fix**: when an external debug file exists, check/prefer its `.symtab` over the main file's own `.symtab` (a debug file's symtab is always ≥ the stripped file's, since it's the unstripped counterpart) — only fall back to the main file's own symtab when no debug file is found at all.
* **Test to add**: a fixture pairing a stripped `.so` (sparse dynsym, single exported symbol) with its external debug file (rich symtab), asserting `enumerateOne` returns the rich list, not just the sparse one.
* **openQA-side note (`tests/coverage/coverage_setup.pm` in os-autoinst-distri-opensuse)**: reproducing the `_bz2.so` gap required *two* debuginfo packages, not one — `python313-debuginfo` alone doesn't cover it. `gdb` (the coverage target) links `libpython3.13`, which dlopens `python313-base`'s `lib-dynload/*.so` — a *different*, split-off subpackage (plus its CPU-variant `python313-base-x86-64-v3`), each needing its own `-debuginfo`. `coverage_setup.pm`'s current `push @packages, $pkg, $pkg . '-debuginfo'` only ever requests debuginfo for the target package itself, not for packages pulled in transitively by ldd/dlopen. Left a TODO at that line; worth keeping in mind if/when Python-loaded native extensions become an intentional coverage target rather than a side effect of instrumenting `gdb`.

---
