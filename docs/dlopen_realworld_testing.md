# Real-World Testing: dlopen() JIT Tracing

Runbook for validating the dlopen JIT instrumentation feature (`docs/dlopen_scalability_plan.md`)
against real production binaries and shared libraries, not just the synthetic
`example/main_dlopen.c` demo. Verified end-to-end on **openSUSE Tumbleweed 20260724**
with kernel 7.1.4-1-default, glibc 2.43, on a fresh install.

Three real-world scenarios plus the existing static/multi-library E2E suite:

| Scenario | Target | Library JIT-discovered | Result |
|---|---|---|---|
| PAM | `su` | `pam_unix.so` (+ full auth module cascade) | ✅ works |
| nginx dynamic module | `nginx` | `ngx_http_echo_module.so` | ✅ works |
| NSS | self-built (`getpwnam`/`gethostbyname`) | `libnss_dns.so.2` / `libnss_files.so.2` | ❌ known limitation, see below |

## 1. Provision a fresh VM

Any KVM/libvirt Tumbleweed install works. Enable debug repositories during or
after install (needed for `-debuginfo` packages):

```bash
sudo zypper mr -e repo-debug   # or add the debug repo if not present:
sudo zypper ar -f http://download.opensuse.org/debug/tumbleweed/repo/oss/ 'Main Repository (DEBUG)'
```

Verify BTF is available before doing anything else:
```bash
ls -lh /sys/kernel/btf/vmlinux
```

## 2. Install the toolchain

A fresh minimal Tumbleweed install has none of this — install it all up front:

```bash
sudo zypper -n install go clang llvm bpftool libbpf-devel elfutils gcc binutils make
```

`bpftool` lands at `/usr/sbin/bpftool`, not on a normal user's `$PATH` — only
matters if you're regenerating BPF bytecode (`REGEN_BPF=1 ./build.sh`), not for
a plain build.

## 3. Transfer the code

Minimal installs don't have `git`. Use `rsync` from your dev machine instead of
`git clone`:

```bash
rsync -az --exclude='.git' --exclude='funkoverage' --exclude='funkoverage-shim' \
  -e ssh /path/to/binarycoverage/ user@vm:~/binarycoverage/
```

## 4. Build and baseline-verify

```bash
cd ~/binarycoverage
./build.sh
./run_unit_tests.sh
go test -race ./...
```

All of this runs without root — no BPF is actually loaded by the unit test suite.

## 5. Run the existing E2E suite

This needs root (BPF load) and matching binary+debuginfo package versions.

```bash
sudo python3 tests/e2e/test_coverage.py -v
```

Then the standalone shell scripts, each self-installs whatever packages it needs:

```bash
export PATH=/usr/sbin:$PATH   # setcap, nginx, etc. live there
sudo -E bash tests/e2e/test_bzip2.sh
sudo -E bash tests/e2e/test_openssl.sh
sudo -E bash tests/e2e/test_squid.sh
```

**Gotcha — rolling-release repo skew:** occasionally the main OSS repo lags
one release behind the DEBUG repo (or vice versa) for a given package, right
after an upstream rebuild wave. `funkoverage install`/`enumerate` will fail
with `dwarf: decoding dwarf section info at offset 0x0: too short` in this
case — the debug file's build-id genuinely doesn't match the installed
binary's, and `eu-unstrip` (even with `--force`) will correctly refuse to
merge them. Check before assuming a code bug:

```bash
rpm -q bzip2 bzip2-debuginfo   # release numbers must match exactly
```

If they don't match, it's an upstream mirror-sync timing issue, not fixable
client-side (switching mirrors doesn't help — DEBUG packages aren't
distributed to third-party mirrors at all, only served from the origin).
`sudo zypper refresh && sudo zypper -n dup` and retry later.

## 6. PAM real-world test

`su` links against `libpam.so.0` (visible to `ldd`), but the actual auth
modules (`pam_unix.so`, `pam_env.so`, ...) live under
`/usr/lib64/security/` and are loaded purely via `dlopen()` at runtime based
on `/etc/pam.d/su` — invisible to `ldd`, the exact case this feature targets.

```bash
sudo bash tests/e2e/test_pam_dlopen.sh
```

Expect `pam_unix.so`'s `pam_sm_open_session`/`pam_sm_close_session` in the
output, confirmed absent from `ldd $(which su)`. In practice a whole cascade
of modules gets JIT-instrumented (pam_rootok, pam_env, pam_limits,
pam_systemd, pam_selinux, pam_keyinit, ...) plus their own transitively
dlopen'd support libraries — no configuration needed beyond a throwaway test
user, which the script creates and removes itself.

## 7. NSS known limitation

glibc's own NSS module loader (`__nss_lookup_function`, used internally by
`getpwnam`, `gethostbyname`, and friends) does **not** call the public
`dlopen()` ELF symbol this feature hooks — it calls a private, non-exported
`__libc_dlopen_mode` instead. Confirmed two ways:

```bash
# separate symbol, different address, LOCAL (not exported)
readelf -Ws /lib64/libc.so.6 | grep -w dlopen
readelf -Ws /lib64/libc.so.6 | grep libc_dlopen
```

```bash
sudo bash tests/e2e/test_nss_dlopen.sh
```

The script builds a tiny self-contained program calling `getpwnam()`/
`gethostbyname()` (no distro debuginfo dependency), traces it, and reports
whether any `libnss_*.so` function got captured. It never does — the lookups
succeed, but zero dlopen events fire for them. This is a **diagnostic**, not
a pass/fail gate: the script always exits 0 and just states the (expected)
result plainly.

This is a real, permanent scope boundary of hooking only the public `dlopen`
symbol — not something a code fix on the userspace/BPF side can close without
also hooking `__libc_dlopen_mode` (a private glibc symbol, not guaranteed
stable across glibc versions).

## 8. nginx dynamic module real-world test

nginx dlopens `modules/*.so` once, at config-parse time (`load_module`
directive), well before workers start serving traffic — a good complementary
case to PAM's much tighter dlopen→call window.

```bash
sudo bash tests/e2e/test_nginx_dlopen.sh
```

Uses `nginx-module-echo` (minimal dependencies — no GeoIP database or Lua
runtime needed). Confirms the actual per-request handler
(`ngx_http_echo_handler`, `ngx_http_echo_run_cmds`, `ngx_http_echo_exec_echo`)
gets traced, not just the module's one-time init callbacks
(`ngx_http_echo_filter_init`, `ngx_http_echo_add_variables`).

**Gotcha — self-daemonizing targets:** nginx (like many classic Unix
daemons) forks and exits its own parent process by default. funkoverage
tracks the binary by the PID it originally `exec()`'d into — if that process
exits (as part of normal daemonization) while the real, re-forked
master+workers keep running detached, tracing tears down via `Stop()`
immediately, and the "real" daemon runs completely untraced from that point
on. Always run daemonizing targets in foreground mode and background them via
the shell instead of trusting their own daemonization:

```bash
# wrong — nginx forks, funkoverage sees its exec'd process exit, stops tracing
nginx -c nginx.conf &

# right — nginx stays in the foreground, the shell backgrounds it instead
nginx -c nginx.conf -g "daemon off;" &
```

`tests/e2e/test_squid.sh` uses the same pattern for squid.

## Full regression pass

Once packages are aligned, all six scripts pass in one run:

```bash
for t in test_bzip2 test_openssl test_squid test_pam_dlopen test_nss_dlopen test_nginx_dlopen; do
    sudo bash "tests/e2e/$t.sh" || echo "FAILED: $t"
done
```

## Cleanup

None of the scripts leave state behind (each uninstalls its shim and cleans
up in a `trap ... EXIT`), but to be sure:

```bash
sudo rm -rf /var/coverage/data/*
ls /var/coverage/bin/   # should be empty
```
