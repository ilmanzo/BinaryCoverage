# Audit fix plan (2026-08-12)

Execution plan for the repo-wide audit. Branch: `fix/audit-2026-08` (from `main` @ `e68b35c`).

Work items are **independently committable** and ordered so later items only ever
*delete* code earlier items touched — never conflict with them. Address one at a
time; each has its own verification step. Do not batch.

**Scope note:** `plan.md` (root) already tracks two unrelated future features
(double-shim detection, `ldd` elimination). Nothing here duplicates them.

---

## Phase 0 — Baseline

Do this once before starting. Establishes the "known good" reference so any e2e
failure later is attributable to a specific item.

```bash
./build.sh
./run_unit_tests.sh              # expect: all pass
go vet ./...                     # expect: clean
gofmt -l .                       # expect: empty
```

### VM e2e procedure (reused by every item that says "run e2e")

Remote VM has **no git**. Sync with rsync, build there, run as root.
Host `andrea@192.168.122.238`, sudo password `nots3cr3t`.

```bash
# from repo root
rsync -az --delete --exclude '.git' --exclude 'funkoverage*' \
      ./ andrea@192.168.122.238:~/binarycoverage/
ssh andrea@192.168.122.238 'cd ~/binarycoverage && ./build.sh'
ssh andrea@192.168.122.238 'sudo install -m755 ~/binarycoverage/funkoverage      /usr/local/bin/'
ssh andrea@192.168.122.238 'sudo install -m755 ~/binarycoverage/funkoverage-shim /usr/local/bin/'
```

Full e2e sweep:

```bash
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo FUNKOVERAGE_SYSTEM_TEST=1 python3 tests/e2e/test_coverage.py'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_bzip2.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_gzip.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_squid.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_openssl.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_cpupower.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_gmp.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_nss_dlopen.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_pam_dlopen.sh'
ssh andrea@192.168.122.238 'cd ~/binarycoverage && sudo bash tests/e2e/test_nginx_dlopen.sh'
```

**Record the Phase 0 baseline output.** Some of these may already be failing for
environmental reasons; only *new* failures count as regressions.

Cheap items (B3, B4, B6, B7, A-series, M-series) need unit tests + build only.
Items marked **[e2e]** must run the full sweep.

---

## Phase 1 — Correctness bugs

### B1 — `report --formats` is silently ignored in the documented form  **[e2e]**

**Severity: high (user-visible, documented behaviour is broken).**

Go's `flag` package stops parsing at the first non-flag argument. Every documented
invocation puts flags *after* positionals, so the flag is dropped and the default
`html,txt,xml` is used instead.

Verified:

```
$ funkoverage report fklogs fkrep --formats xml     # documented form
  → aggregate.html, coverage_x.xml, x.html          # WRONG: all three
$ funkoverage report --formats xml fklogs fkrep
  → coverage_x.xml                                  # correct
```

Same bug class affects `install`/`enumerate`: `funkoverage install /usr/bin/foo
--exclude 'std::.*'` silently traces everything.

**Files:** `cmd/funkoverage.go`

**Change:** add one helper and use it in `cmdInstall`, `cmdEnumerate`, `cmdReport`.

```go
// parseInterspersed parses fs, allowing flags to appear after positional
// arguments. Go's flag package stops at the first non-flag argument, so
// documented forms like `report <in> <out> --formats xml` would otherwise
// silently drop the flag. Returns the positional arguments in order.
// A "--" terminator still ends flag parsing as usual.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}
```

Call sites replace `fs.Parse(args)` + `fs.Args()` / `fs.NArg()` with the returned
slice.

> **DO NOT apply to `cmdTrace`.** `trace <binary> [args...]` deliberately relies on
> stop-at-first-positional: everything after the binary path belongs to the *traced
> program*, not to funkoverage. `funkoverage trace /usr/bin/foo --help` must pass
> `--help` to `foo`. Leave `cmdTrace` exactly as it is, and add a comment saying why.

**Tests:** in `cmd/funkoverage_test.go`, table-test `parseInterspersed` directly:
flags-before, flags-after, interleaved, `--` terminator, no flags, flag with value
split across two args, unknown flag → error (use `flag.ContinueOnError` in the test
FlagSet so it does not `os.Exit`).

**Verify:**
```bash
go test ./cmd/ -run Interspersed -v
/tmp/fk report fklogs out --formats xml   # → only coverage_*.xml
```

**e2e impact — check, do not assume:**
- `tests/e2e/lib_test_helpers.sh:80` uses the broken form (`... "$dir" --formats txt`).
  After the fix it produces **txt only** — no `.html`/`.xml` in `$dir`. Shell tests
  assert on `/var/coverage/data/*.log` via `assert_min_called`, not on report files,
  so they should be unaffected. Confirm `remove_report_dir` still works on an
  empty dir.
- `tests/e2e/test_coverage.py:105` defaults to `formats="xml"` and
  `test_all_coverage_report` (line 269) requests `"xml,txt"`, then globs
  `coverage_*.xml` (lines 133, 270). Both still request xml, so they should pass.
  Confirm no test parses txt stdout from a call that only requests xml.

**Risk:** medium — changes CLI parsing for three subcommands. Mitigated by leaving
`trace` untouched and by the e2e sweep.

---

### B2 — install/uninstall must fully preserve the original binary's mode AND ownership  **[e2e]**

**Severity: high. Scope expanded from the initial audit — see "Design decision" below.
Maintainer's requirement: "after the installation and after uninstall nothing must
change on the original binary except for instrumentation wrapping." That means mode
*and* owner:group must be exactly restored, not just approximately close.**

Two independent gaps, both in the same area:

**Gap 1 — mode.** `unstrip` unconditionally `os.Chmod(out, 0755)` on the merged
output. Verified locally:

```
-r-sr-xr-x  a          # setuid 4555 original
-rwxr-xr-x  merged     # eu-unstrip output, then forced to 0755
```

Consequences:
1. The backed-up original at `SAFE_BIN_DIR/<name>` becomes 0755. `uninstall` moves
   *that* back → **the setuid/setgid bit and any restrictive mode are lost
   permanently**.
2. `mergeLibraryDebugInfo` runs `unstrip` on system `.so` files (normally 0644),
   flipping them to 0755 for the lifetime of the install. `rpm -V` flags them.

**Gap 2 — mode AND ownership on the shim copy itself.** The file placed at the
*original* binary's path (the one users actually invoke) is `copyFile(shimBinary,
realTarget, origMode)`, where `origMode = origInfo.Mode().Perm()` — permission bits
only, setuid/setgid/sticky stripped, and ownership never touched (so it ends up
owned by whichever uid ran `install`, i.e. root). A shimmed setuid or setgid binary
(a deployment tool granted elevated rights, a service binary relying on a
group-writable setgid mode, ...) silently loses that behavior the instant it's
shimmed — the shim runs as the *invoking* user with root's ownership, not the file's
original owner, so whatever the setuid/setgid bit existed to do stops working. This
is a functional break in the wrapped program, not just a coverage or `rpm -V`
cosmetic issue.

**Design decision, reversing this doc's original reasoning for Gap 2:** the first
draft of this item argued dropping setuid on the shim was *safe* ("not a
privilege-escalation path... the shim never inherits it"). That was evaluated purely
from a security angle and missed the functional-fidelity requirement above:
transparently wrapping a binary must not change its privilege model or ownership, or
the wrapped program breaks. The fix is to give the shim copy the exact same mode
*and* owner:group as the original — the shim becomes the new privileged entry point,
exactly as the original binary was, and hands off (via `syscall.Exec`) to the real
binary at `SAFE_BIN_DIR`, which needs that same fidelity for its own execve-time
privilege grant to work.

**Security note to carry forward, not a blocker for this fix:** this means
`funkoverage-shim` itself must be trusted to run with the original binary's
privileges when invoked by an unprivileged user, for the (brief) window before it
execs the real binary. That is inherent to any transparent wrapper of a
setuid/setgid binary — it is not a new risk introduced by this fix, since silently
dropping the bit instead breaks the tool's basic transparency guarantee.

**Gap 3 — merging library debug info destroys SONAME symlinks.** Found live while
running the Phase 0 baseline sweep on the VM: `test_gmp.sh` left
`/usr/lib64/libgmp.so.10` as a **plain regular file** instead of the symlink it's
packaged as (confirmed via `zypper install --force libgmp10`, which restored
`libgmp.so.10 -> libgmp.so.10.5.0`). Root cause: `ldd` reports a library's SONAME
path as-is (e.g. `/lib64/libgmp.so.10`), not further-resolved past that symlink, and
`ParseLddLibraries` passes that path straight through as the map key
`mergeLibraryDebugInfo` operates on. `unstrip`'s final `move(out, binPath)` and
`restoreLibraryBackups`'s `move(backupPath, lib)` are both `os.Rename` onto that
literal path — `rename(2)` targeting a symlink replaces *the symlink itself*, not
its target, so the SONAME symlink is silently replaced by a regular-file copy on
first merge and never comes back on uninstall. Content is byte-identical (so the
library still works and no existing assertion caught it), but this is exactly the
"nothing must change except the wrapping" defect, for merged libraries instead of
the main binary.

**Change:** in `mergeLibraryDebugInfo` (`cmd/elfutil.go`), resolve `lib` via
`filepath.EvalSymlinks` to its real underlying file **before** doing anything else,
and do the backup/unstrip against that resolved path — key the returned `backups`
map by the resolved path too, so `restoreLibraryBackups`'s `move` also targets the
real file and never touches the symlink. A no-op for libraries that are already
plain files (the common case), so this changes nothing for anything but SONAME
symlinks:

```go
for lib := range funcs {
	if lib == mainImage {
		continue
	}
	realLib, err := filepath.EvalSymlinks(lib)
	if err != nil {
		continue
	}
	debugPath, err := locateExternalDebugForMerge(realLib, realLib)
	...
	// backup/unstrip/backups[...] all keyed on realLib, not lib
}
```

**Test:** fixture where `lib` is a symlink to a separate real `.so` file with
external debug info; assert after merge+restore the symlink is still a symlink
(`os.Lstat` + `os.Readlink`, not `os.Stat`) pointing at the same target.

**Files:** `cmd/elfutil.go`, `cmd/shim.go`

**Change:**

Add a shared mask and a best-effort chown helper in `cmd/elfutil.go` (needs a new
`"syscall"` import):

```go
// preservedModeBits are the mode bits that must survive a merge/copy/move so a
// shimmed binary's original privilege semantics (setuid tools, a group-writable
// setgid service binary, ...) keep working exactly as before instrumentation —
// losing them silently breaks the wrapped program's functionality, not just its
// coverage numbers.
const preservedModeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky

// chownLike applies path's owner/group to match info, best-effort: requires
// root/CAP_CHOWN, and a failure (e.g. a non-root test run) must not be fatal —
// mode is what governs exec/setuid semantics, ownership is secondary fidelity
// (ls -l, rpm -V, and the maintainer's "nothing changes but the wrapping"
// requirement).
func chownLike(path string, info os.FileInfo) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(stat.Uid), int(stat.Gid))
	}
}
```

In `unstrip`: stat `binPath` first (before `eu-unstrip` runs), then apply
`preservedModeBits` + `chownLike` to the merged output before `move`ing it into
place.

```go
info, err := os.Stat(binPath)
if err != nil {
	return fmt.Errorf("stat %s: %w", binPath, err)
}
...
if err := os.Chmod(out, info.Mode()&preservedModeBits); err != nil {
	os.Remove(out)
	return fmt.Errorf("chmod merged binary: %w", err)
}
chownLike(out, info)
return move(out, binPath)
```

In `mergeLibraryDebugInfo` (`cmd/elfutil.go:116`): pass the already-captured
`os.FileInfo` straight through to `copyFile` instead of narrowing it to
`info.Mode().Perm()`.

`copyFile` (`cmd/shim.go:306`) changes signature from `(src, dst string, perm
os.FileMode)` to `(src, dst string, info os.FileInfo)`. It applies
`info.Mode()&preservedModeBits` for both `OpenFile`'s create-mode and the post-copy
`Chmod`, then calls `chownLike(dst, info)`. `info` describes the *destination's*
target metadata, not `src`'s own — the existing call site (shim copy) already
separates "bytes from `shimBinary`" from "metadata from the original binary" via a
separate parameter; this just widens that parameter from a stripped `os.FileMode` to
the full `os.FileInfo`.

Call site in `install()` (`cmd/shim.go`): delete `origMode := origInfo.Mode().Perm()`
— `origInfo` is already captured before the move, at the binary's *original*
location, which is exactly the metadata needed. Change the call to
`copyFile(shimBinary, realTarget, origInfo)`.

**Out of scope, deliberately:** `moveCrossDevice` does not gain ownership
preservation here — it doesn't call `copyFile` today, and Phase 2's **A5** already
rewrites it to call `copyFile` for the mode fix. Once `copyFile`'s signature changes
here, A5's rewrite gets full mode+ownership fidelity for free; don't fix it twice.

**Tests:**
- `unstrip`: fixture `chmod 4555`, merge, assert output mode is still `4555`.
  Second case: `0644` input → `0644` output.
- `copyFile`: fixture with a distinct mode (e.g. `0640`); assert it round-trips.
  Ownership: assert chown-to-self succeeds (a process can always chown a file to its
  own current uid/gid without extra privilege) rather than asserting a chown to a
  *different* uid, which requires root and would make the test require root to pass.
- `install()`/`uninstall()` round trip on a `4555` fixture, extending
  `TestInstallUninstall` — this needs no root since it's a plain file the test itself
  created and owns.

**Verify (VM, needs root — the setuid/chown-to-other-owner case can't be tested
without it):**
```bash
sudo install -m 4755 /bin/true /tmp/setuid-probe
sudo chown daemon:daemon /tmp/setuid-probe        # simulate a non-root-owned setuid tool
stat -c '%a %U:%G' /tmp/setuid-probe              # 4755 daemon:daemon
sudo funkoverage install /tmp/setuid-probe
stat -c '%a %U:%G' /tmp/setuid-probe              # shim copy: still 4755 daemon:daemon
sudo funkoverage uninstall /tmp/setuid-probe
stat -c '%a %U:%G' /tmp/setuid-probe              # restored original: still 4755 daemon:daemon
```
Also, mid-install, confirm a merged library kept its original mode+owner:
`stat -c '%a %U:%G' /usr/lib64/libgmp.so.10.5.0` before vs during `test_gmp.sh`.

**Risk:** medium (was low — scope grew). Widens `copyFile`'s contract, which every
e2e test's `install_shim`/`install()` call goes through — the full sweep, not just
the setuid probe above, must pass.

**Implementation pitfall, found by the setuid unit test failing on the first
attempt:** `chown` clears any setuid/setgid bit already present on a file — this is
deliberate kernel behavior (POSIX; prevents ownership-change privilege tricks), not
a bug, and it fires even for a same-value chown-to-self. `chownLike` must therefore
run *before* the final `os.Chmod` in both `unstrip` and `copyFile`, never after —
chmod doesn't clear bits it just set, so it must be the last step. `copyFile` in
particular must create the destination file with the plain permission bits only
(`mode.Perm()`, not `mode`) and defer setting setuid/setgid to the final `Chmod`
call, since the file's *initial* owner (whoever's running `install`) isn't
necessarily its *final* owner (`info`'s), and the intervening chown would strip
anything set at creation time.

---

### B3 — LOG_DIR permissions are inconsistent, and `setup` makes non-root tracing impossible  **[e2e]**

**Severity: high. NOTE: this supersedes the audit's original "just use 0755"
recommendation, which was wrong — see below.**

Three sites create `LOG_DIR` with two different modes:

| site | mode |
|---|---|
| `cmd/shim.go:238` (`setupEnv`) | `0755` |
| `cmd/enumerate.go:382` (`writeFunctionsLog`) | `0777` |
| `cmd/shim_binary/main.go:106` (`runWithTracing`) | `0777` |

`MkdirAll` is a no-op when the directory already exists, so **whichever runs first
wins**. The documented workflow runs `sudo funkoverage setup` first → `/var/coverage/data`
ends up `0755 root:root`. The shim is installed with file capabilities precisely so
it can be invoked by a **non-root** user — and such an invocation then cannot create
its `_called.log`. The `0777` sites exist to work around exactly this.

Blanket-0755 would cement the bug. Blanket-0777 leaves a world-writable directory
with no sticky bit, which a capability-bearing binary writes into.

**Correct fix:** one shared helper, `1777` (world-writable + sticky, same as `/tmp`).
Sticky stops users deleting or renaming each other's logs; `fs.protected_symlinks`
(kernel default) blocks the pre-planted-symlink attack on `O_CREAT`.

`os.MkdirAll` applies the umask, so the sticky/world bits must be re-applied
explicitly:

```go
// internal/funkutil
// EnsureLogDir creates dir as 1777 (world-writable + sticky, like /tmp).
// The shim is installed with file capabilities so it can be invoked by
// non-root users; they must be able to create their own log files, and the
// sticky bit stops them from touching anyone else's. MkdirAll applies the
// umask, so the mode is re-applied with Chmod.
func EnsureLogDir(dir string) error {
	if err := os.MkdirAll(dir, 0o1777); err != nil {
		return err
	}
	return os.Chmod(dir, os.ModeSticky|0o777)
}
```

Call it from all three sites. `SAFE_BIN_DIR` stays `0755` — only root writes there.

**Verify (VM):**
```bash
sudo rm -rf /var/coverage && sudo funkoverage setup
stat -c %A /var/coverage/data          # expect drwxrwxrwt
sudo funkoverage install /usr/bin/bzip2
su - andrea -c 'bzip2 --help'          # non-root run
ls -l /var/coverage/data/*_called.log  # must exist, owned by andrea
```

**Risk:** low-medium. Touches the runtime write path — the full e2e sweep must pass.
If `Chmod` fails on an existing root-owned dir when running non-root, ignore the
error (best-effort) rather than aborting; the shim must not hard-fail here.

---

### B4 — dead `-u` alias, and 11 undocumented aliases

**Severity: low (UX/dead code).**

`m["-u"] = m["uninstall"]` (`cmd/funkoverage.go:52`) is unreachable — the `unwrap`
deprecation guard at line 72 intercepts `-u` first. Verified:

```
$ funkoverage -u /bin/true
unwrap is renamed to 'uninstall'. Use: funkoverage uninstall <binary>
```

Grepped `tests/` and `docs/`: **no alias is used anywhere**, and `helpText` documents
none of them.

**Change:** delete `-i -u -t -e -r --install --uninstall --trace --enumerate --report
--setup` from the alias table. Keep `-h --help -v --version`. Leave the
`wrap`/`unwrap`/`-w`/`-u` deprecation guards untouched (with the alias gone, the `-u`
guard is no longer shadowing anything). Drop the `+16` fudge in
`make(map[string]command, len(cmds)+16)`.

**Tests:** assert `commands()` has exactly the expected key set.

**Risk:** none.

---

### B5 — `emitReport` swallows unknown formats

**Severity: low.**

`funkoverage report in out --formats bogus` exits 0 and writes nothing.

**Change:** give `emitReport` an `error` return with a `default:` case
(`fmt.Errorf("unknown format %q (want html, xml or txt)", format)`); accumulate in
`cmdReport` with `errors.Join`. While there, propagate the currently-discarded
`os.MkdirAll` and `generateAggregateHTMLReport` errors.

**Tests:** `cmdReport` with a bogus format returns a non-nil error naming the format.

**Depends on:** do this **after** B1 — B1 is what makes `--formats` reachable at all.

**Risk:** none.

---

### B6 — `isELF` ignores short reads

**Severity: low.**

`f.Read(magic)` may return fewer than 4 bytes with a `nil` error; the subsequent
`string(magic) == "\x7fELF"` then compares against zero-padded garbage. A 2-byte
file could in principle be misclassified.

**Change:** `io.ReadFull(f, magic)`; treat any error as not-ELF. `cmd/elfutil.go:20`.

**Tests:** 0-byte, 2-byte, and valid-ELF fixtures.

**Risk:** none.

---

### B7 — `findDebugFile` hardcodes `/usr/lib/debug`, ignoring `globalDebugRoot`

**Severity: low (testability + inconsistency).**

`cmd/elfutil.go:238-244` uses three string literals while the package-level
`globalDebugRoot` var — which tests override — exists two functions away. That
branch is currently untestable.

**Change:** replace the literals with `globalDebugRoot`.

> **Subsumed by A1.** If A1 lands first, `findDebugFile` disappears entirely. Doing
> B7 first is still correct and takes two minutes; just delete it again in A1.

**Tests:** point `globalDebugRoot` at a `t.TempDir()`, plant a `.dwz` alt file,
assert resolution finds it.

**Risk:** none.

---

## Phase 2 — Structural dedup

Bigger diffs. Land Phase 1 first and re-run e2e before starting.

### A1 — Three near-identical external-debug resolvers → one  **[e2e]**

**Cut: ~55 lines.**

| function | file | lines |
|---|---|---|
| `externalDebugPath` | `cmd/enumerate.go` | 205-249 |
| `locateExternalDebugForMerge` | `cmd/elfutil.go` | 145-178 |
| `findDebugFile` | `cmd/elfutil.go` | 232-250 |

All three walk the same ladder: `.build-id` → `.gnu_debuglink` → `.gnu_debugaltlink`
(+ `/usr/lib/debug` prefix + `.dwz/<base>`). The only real differences:

- `locateExternalDebugForMerge` returns `("", nil)` early when the binary already
  has embedded `.debug_*` (merging would be pointless), and returns an `error`.
- `externalDebugPath` takes one path; `locateExternalDebugForMerge` takes
  `(binPath, origPath)` because `.gnu_debuglink` resolves relative to the binary's
  *original* directory (by merge time it has already moved to `SAFE_BIN_DIR`).

**Change:** single function in `cmd/elfutil.go`:

```go
// resolveDebugFile returns the external debug file for binPath, or "".
// origPath is binPath's original absolute location (equal to binPath unless
// the binary has already been moved to SAFE_BIN_DIR) — .gnu_debuglink
// resolves relative to it. When skipIfEmbedded is set, a binary that already
// carries .debug_* sections resolves to "" (nothing to merge).
func resolveDebugFile(binPath, origPath string, skipIfEmbedded bool) (string, error)
```

`externalDebugPath(p)` becomes a one-line wrapper that discards the error
(preserving today's behaviour at that call site) — or, better, propagate it; decide
when you get there and say which in the commit message.

**Tests:** the existing `TestLocateExternalDebugForMerge`,
`TestLocateExternalDebugForMerge_DebugLink`, `TestExternalDebugPath_IgnoresDwzFile`
must all keep passing against the merged function. Add a case proving
`skipIfEmbedded=false` still returns a path for a binary with embedded DWARF.

**Risk: medium-high.** This is the exact code path that issue #128 and PR #135 fixed.
`test_cpupower.sh` (`.gnu_debuglink` with `.build-id` deliberately broken) and
`test_gmp.sh` (library merge + restore) are the regression guards — both **must**
pass on the VM before this is committed.

---

### A2 — `getSharedLibrarySymbols` duplicates `enumerateSymtab`

**Cut: ~40 lines.**

`cmd/shim_binary/tracer.go:449` and `cmd/enumerate.go:303` implement the same thing:
iterate symbols, keep `STT_FUNC` with `Value != 0 && Size != 0`, dedup by address
(Itanium C1/C2 ctor aliasing), dedup by name, apply `FuncIsRelevant`. They are
duplicated only because both binaries are `package main`.

Differences to preserve:
- the shim **unions** `.dynsym` + `.symtab` (glibc ships a `.symtab` that omits
  exported functions); `enumerateSymtab` uses `.symtab` and falls back to `.dynsym`
  only on hard error. The union is the more correct behaviour — adopt it for both,
  and say so in the commit message, since it may increase enumerated counts.
- `enumerateSymtab` applies a `*FuncFilter`; the shim filters separately afterwards.

**Change:** move to `internal/funkutil` as
`SymtabFunctions(f *elf.File, keep func(demangled string) bool) []string`.
Both call sites pass their own predicate.

**Depends on:** A3 (shared filter type) makes the predicate natural. Consider doing
A3 first.

**Risk: medium.** Adopting the union changes enumerated function counts. Diff
`_functions.log` line counts before/after on the VM for bzip2 and openssl and record
the delta in the commit message.

---

### A3 — `Tracer.matchesFilter` duplicates `FuncFilter.Match`

**Cut: ~20 lines.**

`cmd/shim_binary/tracer.go:142` re-implements `cmd/enumerate.go:47`, and the shim
re-compiles the regexes from the sidecar by hand
(`tracer.go:82-96`). Same cross-package-`main` cause.

**Change:** move `FuncFilter`, `NewFuncFilter`, `Match`, `Sidecar` into
`internal/funkutil` (it already owns `FilterSidecar`). Add
`funkutil.FilterFromSidecar(FilterSidecar) *FuncFilter` for the shim. Keep the
shim's lenient behaviour — a bad pattern currently logs and degrades to
"no filter" rather than aborting a traced process; preserve that.

**Tests:** existing filter tests move with the type; add one for
`FilterFromSidecar` with an invalid pattern.

**Risk:** low.

---

### A4 — `findLibcPath`'s hardcoded path list — REJECTED, doc's premise was wrong

**Do not do this.** The original reasoning below is incorrect. Left here so it
isn't proposed again.

**Original (wrong) reasoning:** `cmd/shim_binary/tracer.go:379` probes ten
hardcoded libc/libdl paths **before** falling back to `/proc/<pid>/maps`. The
hardcoded probes test the *host* filesystem, not the traced pid's mount
namespace, so in a container or chroot they can select a libc the target never
maps — and the authoritative source is already implemented right below. Proposed
change: delete `standardPaths`; go straight to `/proc/<pid>/maps`.

**Why it's wrong:** `findLibcPath(rootPID)` is called from `Tracer.Start`, which
runs while `rootPID` is still the shim's own re-exec'd child, **blocked on the
wait-pipe read, before it calls `syscall.Exec` into the real target**
(`cmd/shim_binary/main.go`'s `runWithTracing`/`childMain`). At that exact moment,
`/proc/rootPID/maps` reflects the shim binary's own memory layout, not the
eventual target's. Confirmed directly:

```
$ file funkoverage-shim
funkoverage-shim: ELF 64-bit LSB executable, x86-64, ..., statically linked, ...
```

`funkoverage-shim` is a **statically linked** Go binary — it maps no libc at all.
Removing `standardPaths` makes `/proc/<pid>/maps` search fail every time it's
actually invoked in production, on every host, container or not — not just in
the container/chroot edge case the doc worried about. This would silently
disable the dlopen uretprobe attachment entirely, breaking all dlopen JIT
tracing (`test_nss_dlopen.sh`, `test_pam_dlopen.sh`, `test_nginx_dlopen.sh`).

Caught before landing: attempting this change made
`TestFindLibcPath_HasDlopen`/`TestHasSymbol`/`TestGetSharedLibrarySymbols` all
start skipping locally. Root-caused via `os.Getpid()` in the test also being a
static Go binary with no libc mapped — same shape as the real bug, just
surfaced through the test's own process instead of the shim's. Reverted; no
code change landed for this item.

**If this needs revisiting:** the real fix would have to attach the dlopen
uretprobe *after* the child execs into the target (so `/proc/pid/maps` reflects
the target, not the shim), which reintroduces a race against any `dlopen()` call
from the target's own `.init_array`/constructors before `main()` — a materially
bigger change than a 15-line deletion, out of scope for this audit pass.

---

### A5 — `moveCrossDevice` hand-rolls `copyFile`

**Cut: ~20 lines.**

`cmd/elfutil.go:309` duplicates `cmd/shim.go:306` plus a stat/chmod/remove.

**Change:**
```go
func moveCrossDevice(source, destination string) error {
	fi, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if err := copyFile(source, destination, fi.Mode()); err != nil {
		return err
	}
	return os.Remove(source)
}
```
Note `copyFile` already `Chmod`s to the requested mode, so pass the full
`fi.Mode()` (consistent with B2).

**Risk:** low. Only exercised on cross-device moves — construct a tmpfs mount in the
unit test, or accept unit coverage of `copyFile` alone and note the gap.

---

### A6 — `checkKernelVersion` / `utsString` via `syscall.Uname`

**Cut: ~18 lines.**

`cmd/shim.go:249,271` hand-rolls `int8`-slice-to-string conversion around
`syscall.Uname`. `/proc/sys/kernel/osrelease` gives the same string with one
`os.ReadFile` — confirmed present (`7.1.7-1-default`) — and sidesteps the
`[65]int8`-vs-`[65]uint8` `Utsname` portability question entirely. (ARM64
cross-compile currently succeeds, so this is cleanup, not a bug fix.)

**Change:** read the file, `strings.TrimSpace`, keep the existing major/minor parse
and error messages verbatim. Delete `utsString` and the `syscall` import if it
becomes unused.

**Tests:** the existing `checkKernelVersion` tests should still pass; factor the
parse into `parseKernelVersion(release string) (int, int, error)` so it can be
table-tested without touching `/proc`.

**Risk:** low.

---

### A7 — `hasDebugInfo`'s build-id block is dead duplication

**Cut: ~8 lines.**

`cmd/elfutil.go:50-56` checks the `.build-id` path, then line 58 calls
`externalDebugPath`, whose *first* step is the same check.

**Change:** delete lines 50-56.

**Subsumed by A1** if A1 lands first.

**Risk:** none.

---

### A8 — `normalizeFuncs` and its duplicate

**Cut: ~10 lines.**

`cmd/shim_binary/tracer.go:170` converts a nil map to an empty one; `main.go:99-102`
does the same thing again before calling `NewTracer`. The only consumer is
`flattenFuncs`, which does `slices.Sorted(maps.Keys(funcs))` — ranging a nil map is
legal Go.

**Change:** delete both. Keep a one-line comment on `NewTracer` noting that a nil
`funcs` map is valid (pure-dlopen tracing).

**Tests:** `NewTracer(nil, ...)` must still succeed — there is likely an existing
test for this; keep it.

**Risk:** low.

---

## Phase 3 — Small cuts and Go 1.26 modernization

Low risk, batchable into one or two commits. Unit tests + build; no e2e needed
unless noted.

| id | change | file | cut |
|---|---|---|---|
| A9  | `emitReport`'s `html` and `xml` cases are the same errgroup block twice — extract `perImage(coverage, outputDir, fn)` | `cmd/funkoverage.go:214` | -14 |
| A10 | **REJECTED** — `generateXUnitReport`'s `TOTALS` block repeats the three numbers already in `summaryText`, but `Message` (summaryText) and `Text` (details, holding TOTALS) are two separate XML fields consumed independently by CI tooling; maintainer wants both kept. `TestGenerateXUnitReport` now asserts on the TOTALS text explicitly. | `cmd/report.go:314` | 0 |
| A11 | `detectLogType` returns `"functions"`/`"called"`, then string-compares **per line** in `scanLog`'s hot loop → return a bool, select the target map before the loop | `cmd/report.go:81,201` | -8 |
| A12 | `hasSymbol`'s two nested loops → `slices.ContainsFunc` | `tracer.go:350` | -10 |
| A13 | `EnvOr` → `cmp.Or(os.Getenv(name), fallback)`; delete the helper and its test | `internal/funkutil/funkutil.go:22` | -6 |
| A14 | delete the leftover `// Add this field` comment | `cmd/report.go:39` | -1 |
| M1  | 4 `gopls modernize` hits (3× range-over-int, 1× `min`) | `cmd/report_bench_test.go:18,27,38,46` | -6 |
| M2  | `strings.Split(string(data), "\n")` → `strings.Lines`; `findLibcPath` 30 lines up already uses `SplitSeq` | `tracer.go:431` | -2 |
| M3  | `bufio.NewScanner(strings.NewReader(string(out)))` → `for line := range strings.Lines(string(out))` | `cmd/enumerate.go:361` | -3 |
| M4  | `cleanEnv`'s `map[string]bool` → `slices.Contains` on a 3-element slice | `shim_binary/main.go:207` | -4 |

Run `go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./...`
afterwards; it should report nothing.

### A15 — optional: collapse the three sidecar triplets

**Cut: ~25 lines. Judgement call — decide, do not default to yes.**

`funclist.go`, `libbackup.go`, and the `FilterSidecar` block in `funkutil.go` are
three copies of the same `XPath`/`WriteX`/`ReadX` triplet over `writeJSON`/`readJSON`.
A generic `sidecar[T]{suffix string}` value would collapse them.

Three is right at the threshold where dedup starts paying. Against: the current form
is dead obvious, and a generic wrapper over an already-generic pair of helpers is
the kind of abstraction that reads worse at 3am. **Recommendation: skip.** Revisit
at a fourth sidecar.

### Not doing: dropping `golang.org/x/sync`

`errgroup` is used three times; only `analyzeLogs` needs error propagation, and
`sync.WaitGroup.Go` (Go 1.25) plus a semaphore channel could replace all three. But
it is an already-installed dependency doing exactly its job, and the replacement is
*more* code. Skip unless a stdlib-only tree becomes a goal.

---

## Phase 4 — Final validation

```bash
./build.sh && ./run_unit_tests.sh && go vet ./... && gofmt -l .
GOARCH=arm64 GOOS=linux go build ./...          # ARM64 must still compile
go run golang.org/x/tools/gopls/internal/analysis/modernize/cmd/modernize@latest ./...
```

Then the **full VM e2e sweep** from Phase 0, compared against the recorded baseline.

Documentation to update before the PR:
- `helpText` (`cmd/templates.go`) if B4 changed the accepted invocations.
- `docs/install.md:111` and `docs/design.md:106` — both show
  `report ... --formats ...` after positionals. That form *works* after B1; no edit
  strictly needed, but re-read them once B1 lands.
- `CLAUDE.md` if the `LOG_DIR` mode contract (B3) is worth stating explicitly.
- Add a `plan.md` note if any item is deferred.

---

## Summary

| phase | items | est. cut | e2e required |
|---|---|---|---|
| 1 — correctness | B1-B7 | ~-20 | B1, B2, B3 |
| 2 — dedup | A1-A8 | ~-186 | A1, A4 |
| 3 — small + modernize | A9-A14, M1-M4 | ~-62 | no |

**Total: ~-270 lines, 0 dependencies removed.**

Highest value: **B1** (documented behaviour is broken) and **B2** (silent permanent
modification of system file modes). Highest risk: **A1** (touches the issue-#128
code path) and **A4** (touches dlopen JIT discovery).
