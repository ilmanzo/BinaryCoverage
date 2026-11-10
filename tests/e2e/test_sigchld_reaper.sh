#!/bin/bash
# Issue #167: the shim's helper must never be reaped by the traced daemon.
#
# vsftpd's listener reaps every child from its SIGCHLD handler and looks the
# pid up in its own table; a pid it never forked is a NULL deref. The helper
# is forked by the shim and so becomes the daemon's child once the shim execs
# in place, which breaks such daemons three ways:
#   1. the helper is still a child of the daemon at all (any wait(-1) loop
#      sees it; a daemon that waits for all children at shutdown hangs);
#   2. the helper exits while the daemon runs -- PR_SET_PDEATHSIG tracks the
#      *thread* that forked it, and exec kills every other thread -- so the
#      daemon reaps it, and the run's coverage stops at exec;
#   3. with tracing blocked (fail-open, issue #158) the dead helper's zombie
#      is inherited by the daemon.
#
# Run as root: sudo bash tests/e2e/test_sigchld_reaper.sh
source "$(dirname "$0")/lib_test_helpers.sh"

require_root
require_funkoverage

RUNS=${RUNS:-30}
SRC=$(dirname "$0")/reaper
WORK=$(mktemp -d)
BIN=$WORK/reaper

cleanup() {
    funkoverage uninstall "$BIN" >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

gcc -g -O0 -o "$BIN" "$SRC/reaper.c" || fail "build reaper"
clean_coverage_data
install_shim --no-libs "$BIN" >/dev/null

header "Helper is not the daemon's child (issue #167)"
inherited=0 reaped=0
for i in $(seq "$RUNS"); do
    rc=0
    "$BIN" 1 >"$WORK/out.$i" 2>&1 || rc=$?
    grep -q INHERITED_CHILD "$WORK/out.$i" && inherited=$((inherited + 1))
    [[ $rc == 4 ]] && reaped=$((reaped + 1))
done
# uninstall waits for every helper to drain its log.
funkoverage uninstall "$BIN" >/dev/null
late=$(grep -l '^CALLED .* late_marker$' /var/coverage/data/reaper_*_called.log 2>/dev/null | wc -l)
info "$RUNS runs: $inherited started with a child, $reaped reaped it, $late traced to the end"

(( inherited == 0 )) || fail "$inherited/$RUNS runs started with the helper as their child"
pass "helper never a child of the traced process"
(( reaped == 0 )) || fail "$reaped/$RUNS runs reaped the helper while running (vsftpd segfaults here)"
pass "helper never reaped by the traced process"
(( late == RUNS )) || fail "only $late/$RUNS runs traced until exit: helper stopped early"
pass "every run traced until exit"

header "No zombie inherited when tracing is blocked (fail-open)"
if ! command -v systemd-run >/dev/null 2>&1 || [[ ! -d /run/systemd/system ]]; then
    info "SKIP: no running systemd for the seccomp sandbox"
    exit 0
fi
install_shim --no-libs "$BIN" >/dev/null
# Deny-list kills the helper with SIGSYS before it reports, as in
# test_shim_fail_open.sh.
out=$(systemd-run --quiet --pipe --wait -p SystemCallFilter=~perf_event_open "$BIN" 0 2>/dev/null) || true
[[ $out == *LATE* ]] || fail "fixture did not run under the sandbox: '$out'"
[[ $out != *INHERITED_CHILD* ]] || fail "daemon inherited the failed helper: '$out'"
pass "failed helper reaped before exec"

echo -e "\n${GREEN}ALL TESTS PASSED${NC} (helper lifecycle invisible to the traced daemon)"
