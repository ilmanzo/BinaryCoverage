#!/bin/bash
# E2E test: funkoverage shim signal forwarding + sd_notify passthrough
# (issues #143, #152)
#
# The shim execs the real daemon in place of the original invocation,
# preserving its pid rather than running it as a separately-forked child.
# That's what makes both of the following work, using a tiny fixture daemon
# instead of sshd/rpcbind/postgres — avoids host keys / systemd-unit setup
# while isolating exactly the shim's process-identity behavior:
#
#   - Signals: systemd (or, here, bash) signals the pid it started; that pid
#     IS the real daemon, so no forwarding relay is needed. Verifies the
#     issue #143 bug (parent dies on SIGTERM without telling a separately
#     forked child, which keeps holding its socket) can't recur structurally.
#   - sd_notify/NOTIFY_SOCKET: the real daemon calls sd_notify() from the
#     exact pid systemd is tracking, so the datagram's kernel-attached
#     sender credentials already match what systemd's default
#     NotifyAccess=main expects — no relay needed either. Verifies the
#     issue #152 bug class (a supervisor checking process identity — systemd's
#     LISTEN_PID, pg_ctl's postmaster.pid pid field — sees a mismatch and
#     silently ignores/times out the real daemon).
#
# Run as root on openSUSE with funkoverage in PATH. Needs `go` in PATH to
# build the fixture.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
WORKDIR=""
PORT=18199
SHIM_PID=""

cleanup() {
    [[ -n "$SHIM_PID" ]] && kill "$SHIM_PID" 2>/dev/null || true
    pkill -f notifydaemon 2>/dev/null || true
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
command -v go >/dev/null 2>&1 || fail "go not in PATH (needed to build the fixture)"

WORKDIR=$(mktemp -d /tmp/funkoverage_notifydaemon_XXXX)
BINARY="$WORKDIR/notifydaemon"
info "Building fixture daemon"
(cd "$SCRIPT_DIR/../.." && go build -o "$BINARY" ./tests/sample/notifydaemon)
require_debug_symbols "$BINARY"
pass "notifydaemon fixture built with debug symbols"

clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Process identity + signal forwarding ---
header "Process identity (issue #152) + signal forwarding (issue #143)"

"$BINARY" "$PORT" > "$WORKDIR/daemon_output.txt" 2>&1 &
SHIM_PID=$!

info "Waiting for fixture to bind port $PORT..."
for i in $(seq 1 15); do
    ss -ltn 2>/dev/null | grep -q ":$PORT " && break
    [[ $i -eq 15 ]] && fail "notifydaemon did not bind port $PORT in 15s"
    sleep 1
done
pass "Port $PORT bound"

# The pid bash captured at fork/exec time must be the SAME pid the daemon
# reports for itself — the property every issue #152 fix (systemd's
# LISTEN_PID, pg_ctl's postmaster.pid) ultimately depends on. A regression
# here (e.g. reintroducing a fork-then-exec-in-child model) would silently
# break all of them at once.
DAEMON_PID=$(grep -o 'PID=[0-9]*' "$WORKDIR/daemon_output.txt" | head -1 | cut -d= -f2)
[[ "$DAEMON_PID" == "$SHIM_PID" ]] \
    || fail "shim pid ($SHIM_PID) != daemon's self-reported pid ($DAEMON_PID) — the shim is running the real binary in a different process than the one the caller started"
pass "Daemon runs under the exact pid the caller started (no separate forked child)"

# Signal only the pid the caller started, mirroring systemd signalling just
# the tracked MainPID (not the whole cgroup) on `systemctl restart`.
kill -TERM "$SHIM_PID"
wait "$SHIM_PID" 2>/dev/null || true
pass "Daemon exited"

if ss -ltn 2>/dev/null | grep -q ":$PORT "; then
    fail "port $PORT still bound after the daemon exited"
fi
pass "Port released"

grep -q "EXITING" "$WORKDIR/daemon_output.txt" \
    || fail "daemon never ran its own SIGTERM handling — means it wasn't sent a real, catchable signal"
pass "Daemon received a real signal and shut down on its own (not SIGKILLed)"

SHIM_PID=""

# --- sd_notify / NOTIFY_SOCKET passthrough ---
header "sd_notify passthrough (Type=notify support)"

NOTIFY_SOCK="$WORKDIR/fake-systemd-notify.sock"
"$BINARY" -notify-listener "$NOTIFY_SOCK" > "$WORKDIR/notify_output.txt" 2>&1 &
LISTENER_PID=$!
sleep 0.5

NOTIFY_SOCKET="$NOTIFY_SOCK" "$BINARY" "$((PORT + 1))" > "$WORKDIR/daemon2_output.txt" 2>&1 &
SHIM_PID=$!

info "Waiting for sd_notify to reach the fake systemd socket..."
for i in $(seq 1 10); do
    kill -0 "$LISTENER_PID" 2>/dev/null || break
    [[ $i -eq 10 ]] && fail "sd_notify READY=1 never reached the fake systemd socket"
    sleep 1
done
wait "$LISTENER_PID" 2>/dev/null || true

grep -q "READY=1" "$WORKDIR/notify_output.txt" \
    || fail "fake systemd socket did not receive READY=1"
pass "sd_notify READY=1 reached the real NOTIFY_SOCKET"

kill -TERM "$SHIM_PID" 2>/dev/null || true
wait "$SHIM_PID" 2>/dev/null || true
SHIM_PID=""

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (shim process identity + signal handling + notify passthrough)"
