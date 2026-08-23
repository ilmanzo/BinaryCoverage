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

## 3. Fix `enumerateOne` Short-Circuit on Sparse Own-Symtab

* **Found**: investigating openQA job 6163815 (`binary_coverage_ultimate`) reports of "1 function" for postfix/rpm/sestatus. Those three are correct (real thin CLI wrappers — confirmed independently via `gdb -batch -ex 'info functions'`; real logic lives in their shared libs, reported separately as `binary_coverage_lib*`).
* **But**: the same "1 function" pattern for CPython extension modules (`_bz2.cpython-313...so`, `_asyncio...so`, etc. — pulled in because `gdb` is a coverage target and links `libpython3.13`) is a genuine bug, not the same thing. `_bz2.so`'s own `.dynsym` exports exactly one symbol (`PyInit__bz2` — CPython convention, real implementation is `static`), but its external debug file's `.symtab` (once `python313-base-debuginfo` is installed) has 18 real functions (`_bz2_BZ2Compressor_compress`, `compress`, ...) that are never consulted.
* **Root cause**: `enumerateOne` (`cmd/enumerate.go:101`) returns on the first non-empty result — `symtabFunctions(path)` finds 1 and returns immediately, never reaching the external-debug-file check.
* **Fix**: when an external debug file exists, check/prefer its `.symtab` over the main file's own `.symtab` (a debug file's symtab is always ≥ the stripped file's, since it's the unstripped counterpart) — only fall back to the main file's own symtab when no debug file is found at all.
* **Test to add**: a fixture pairing a stripped `.so` (sparse dynsym, single exported symbol) with its external debug file (rich symtab), asserting `enumerateOne` returns the rich list, not just the sparse one.
* **openQA-side note (`tests/coverage/coverage_setup.pm` in os-autoinst-distri-opensuse)**: reproducing the `_bz2.so` gap required *two* debuginfo packages, not one — `python313-debuginfo` alone doesn't cover it. `gdb` (the coverage target) links `libpython3.13`, which dlopens `python313-base`'s `lib-dynload/*.so` — a *different*, split-off subpackage (plus its CPU-variant `python313-base-x86-64-v3`), each needing its own `-debuginfo`. `coverage_setup.pm`'s current `push @packages, $pkg, $pkg . '-debuginfo'` only ever requests debuginfo for the target package itself, not for packages pulled in transitively by ldd/dlopen. Left a TODO at that line; worth keeping in mind if/when Python-loaded native extensions become an intentional coverage target rather than a side effect of instrumenting `gdb`.

---

## 4. Fix Duplicate Library Reports (Symlink vs Real-Path Key Mismatch)

* **Found**: analyzing the full coverage ZIPs attached to GitHub issue #141 (3 real `binary_coverage_ultimate` runs — job_185, job_6163815, job_6167511). ~37-41 shared libraries out of ~540 reported images are duplicated: identical function count, identical called/uncalled function names, reported under two different basenames — always the SONAME-style short name plus the fully-versioned real filename (e.g. `libz.so.1` / `libz.so.1.3.1`, `libssl.so.3` / `libssl.so.3.5.3`, `libkrb5.so.3` / `libkrb5.so.3.3`, `libeconf.so.0` / `libeconf.so.0.8.4`, and ~33 more — full list in the issue #141 analysis). This inflates the reported "unique images traced" count and double-counts shared function/coverage totals. Ruled out as coincidence: a handful of *other* same-count pairs (`snmpget`/`snmpset`, `pam_warn.so`/`pam_deny.so`, `pam_umask.so`/`pam_limits.so`) are NOT this bug — those are genuinely different binaries that legitimately share identical function names (net-snmp CLI boilerplate, PAM's required `pam_sm_*` entry points) — confirmed by diffing full called+uncalled function name lists, not just counts.
* **Root cause**: `EnumerateFunctions` (`cmd/enumerate.go`) keys its result map by whatever path string it was handed, with no symlink canonicalization — confirmed by a passing code comment already in `mergeLibraryDebugInfo` (`cmd/elfutil.go:122`): "ldd reports a library's SONAME path as-is ... which is commonly a symlink". An explicit `coverage_targets` entry pointing straight at a library (e.g. `/usr/lib64/libz.so.1`) is keyed by that literal symlink path; the same physical library discovered transitively as another target's ldd dependency is keyed by ldd's own arrow-resolved path, which can be a *different* basename entirely (glibc-hwcaps picks `/lib64/glibc-hwcaps/x86-64-v3/libz.so.1.3.1` over the plain `/lib64/libz.so.1.3.1`). Neither path is canonicalized before being used as a map key, so the same file never collapses into one report image.
* **Reproduced**: `cmd/funkoverage_test.go::TestEnumerateFunctions_SymlinkAliasing_DuplicateKeys` — builds one real `.so`, symlinks it under a second name, calls `EnumerateFunctions` once per path spelling, asserts both should key identically. Currently **fails** (proves the bug); passes once fixed.
* **Fix direction**: canonicalize every library path (`filepath.EvalSymlinks`) before it's used as an `EnumerateFunctions` result-map key — both for the main target's own path and for each `ParseLddLibraries`-discovered dependency — so an explicit target and a transitive dependency of the same physical file always collapse to one key. `mergeLibraryDebugInfo` already does this locally (line 128) for its own bookkeeping; the canonicalization needs to happen earlier, at the point the key is minted, so it's visible to `_functions.log`/the report too.
* **Caution before touching this**: do NOT `funkoverage install` directly on a live, widely-linked system library (e.g. `/usr/lib64/libz.so.1`) on a shared VM to test this — nearly every process on the system links it; if the library-install path has an edge case that corrupts the file in place instead of merging safely, it can break the whole VM. Test via the unit test above or in an isolated/disposable environment only.
