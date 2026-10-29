#!/bin/bash
# Issue #163: probes must only trap in traced processes.
#
# A traced instance keeps its probes on libhot.so attached while an unrelated,
# untraced executable calls into the same library in a hot loop — the shape
# of systemctl and PID 1 sharing libsystemd-shared. System-wide uprobes trap
# the bystander on every call (0.1-1 us each), which is how PID 1 slowed
# `systemctl daemon-reload` from 1 s to 45 s.
#
# Also checks that a forked child is still traced, including a call it makes
# right after fork(), as a daemonizing service's init code does.
#
# Run as root: sudo bash tests/e2e/test_bystander_traps.sh
source "$(dirname "$0")/lib_test_helpers.sh"

require_root
require_funkoverage

SPIN=20000000
SRC=$(dirname "$0")/bystander
WORK=$(mktemp -d)
BIN=$WORK/bystander
REAL=/var/coverage/bin/bystander
HOLD_PID=

cleanup() {
    [[ -n $HOLD_PID ]] && kill "$HOLD_PID" 2>/dev/null && { wait "$HOLD_PID" 2>/dev/null || true; }
    funkoverage uninstall "$BIN" >/dev/null 2>&1 || true
    rm -rf "$WORK"
}
trap cleanup EXIT

# Wall time of "$@" in milliseconds.
time_ms() {
    local t0=$EPOCHREALTIME
    "$@" >/dev/null
    echo $(( (${EPOCHREALTIME/./} - ${t0/./}) / 1000 ))
}

header "Bystander traps (issue #163)"
gcc -g -O0 -shared -fPIC -o "$WORK/libhot.so" "$SRC/hot.c" || fail "build libhot.so"
for exe in bystander spinner; do
    gcc -g -O0 -o "$WORK/$exe" "$SRC/bystander.c" -L"$WORK" -lhot -Wl,-rpath,"$WORK" \
        || fail "build $exe"
done
clean_coverage_data

base=$(time_ms "$WORK/spinner" spin $SPIN)
info "untraced spin, no probes: ${base} ms"

install_shim "$BIN" >/dev/null
grep -q libhot.so /var/coverage/bin/bystander.funcs.json || fail "libhot.so not instrumented"

"$BIN" hold 30 &
HOLD_PID=$!
# The shim execs the real binary only after the helper attached.
for _ in $(seq 50); do
    [[ $(readlink /proc/$HOLD_PID/exe) == "$REAL" ]] && break
    sleep 0.1
done
[[ $(readlink /proc/$HOLD_PID/exe) == "$REAL" ]] || fail "traced instance did not start"

bystander=$(time_ms "$WORK/spinner" spin $SPIN)
info "untraced spin, probes attached for pid $HOLD_PID: ${bystander} ms"

# A trap costs >=0.1 us (measured 0.14 us on x86_64, kernel 7.3), so
# trapping all $SPIN calls adds >=2 s.
limit=$(( base * 5 + 300 ))
(( bystander <= limit )) \
    || fail "untraced process slowed ${base} -> ${bystander} ms (limit ${limit}): probes trap outside the traced process"
pass "untraced process runs at native speed"

kill "$HOLD_PID"; wait "$HOLD_PID" 2>/dev/null || true; HOLD_PID=

header "Forked child still traced"
"$BIN" fork 200
funkoverage uninstall "$BIN" >/dev/null
sleep 1
for fn in child_late child_early; do
    grep -qh "^CALLED .* $fn\$" /var/coverage/data/bystander_*_called.log \
        || fail "$fn not recorded in forked child"
    pass "$fn recorded in forked child"
done
