#!/bin/bash
# E2E test: a shimmed binary must still run when the tracer cannot attach
# (regression guard for issue #158).
#
# The tracer's only perf_event_open(2) call is the sched_process_fork
# tracepoint; every uprobe goes through bpf(2). A seccomp-sandboxed caller
# routinely allows bpf and withholds perf_event_open -- systemd-udevd's
# SystemCallFilter is exactly this -- so that one attach fails while the rest
# would have worked.
#
# funkoverage used to treat it as fatal: the helper errored, runWithTracing
# propagated, and main() exited *before* exec'ing the real binary. The shim
# became a stub that always failed. Real-world impact: udev's nfsrahead
# callout stopped running at all, silently disabling NFS readahead tuning,
# and reported 0% coverage for a binary it never let execute.
#
# systemd-run with a SystemCallFilter deny-list reproduces the same attach
# failure as udev without needing an NFS mount or a udev event. Note the two
# differ in shape -- a deny-list kills the helper with SIGSYS (empty reply),
# udev's allow-list returns EPERM (helper reports its own error) -- and the
# shim must survive both.
#
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""

cleanup() {
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
# systemd-run is how this test denies the syscall; without a running systemd
# (containers) there is no way to set up the sandbox, so skip rather than fail.
if ! command -v systemd-run >/dev/null 2>&1 || [[ ! -d /run/systemd/system ]]; then
    info "SKIP: no running systemd — cannot build the seccomp sandbox this test needs"
    exit 0
fi
ensure_packages bzip2 bzip2-debuginfo

BINARY=$(which bzip2)
require_debug_symbols "$BINARY"
pass "bzip2 with debug symbols"

# Establish the expected output *before* shimming, so the assertions below
# compare against what this host's bzip2 actually prints.
EXPECTED=$(printf 'funkoverage 158' | "$BINARY" -c | "$BINARY" -dc)
[[ "$EXPECTED" == "funkoverage 158" ]] || fail "unshimmed bzip2 round-trip already broken"
pass "Baseline round-trip works unshimmed"

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Sanity: tracing works when nothing is blocked ---
header "Unrestricted run (tracing available)"
GOT=$(printf 'funkoverage 158' | "$BINARY" -c | "$BINARY" -dc)
[[ "$GOT" == "$EXPECTED" ]] || fail "shimmed bzip2 round-trip failed: got '$GOT'"
pass "Round-trip correct with tracing active"

[[ -n "$(ls /var/coverage/data/*bzip2*_called.log 2>/dev/null)" ]] \
    || fail "no called log produced -- tracing did not run at all"
pass "Coverage recorded"

# --- The regression: tracing blocked, program must still run ---
header "perf_event_open denied (tracer cannot attach)"
# --pipe/--wait so we get the payload back; the deny-list makes any
# perf_event_open in the tracee fail exactly as it does under udev.
# Deliberately outside `set -e`/`pipefail`: when the shim regresses, bzip2
# never runs, the decompress leg chokes on an empty stream, and the script
# would die there with a raw bzip2 error instead of the assertion below.
set +e +o pipefail
GOT=$(printf 'funkoverage 158' \
    | systemd-run --quiet --pipe --wait -p SystemCallFilter=~perf_event_open "$BINARY" -c 2>/dev/null \
    | "$BINARY" -dc 2>/dev/null)
set -e -o pipefail

# Before the fix this was empty: the shim exited 1 without exec'ing bzip2,
# so nothing was ever compressed.
[[ -n "$GOT" ]] || fail "shim produced no output -- it exited instead of exec'ing the real binary"
[[ "$GOT" == "$EXPECTED" ]] || fail "output corrupted under seccomp: got '$GOT'"
pass "Binary still ran and produced correct output with tracing blocked"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (shim fails open when the tracer cannot attach)"
