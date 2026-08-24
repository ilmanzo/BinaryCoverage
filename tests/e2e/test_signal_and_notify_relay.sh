#!/bin/bash
# E2E test: funkoverage shim signal forwarding + sd_notify relay (issue #143)
#
# Reproduces the systemd restart bug (parent shim dies on SIGTERM without
# telling the child, so the child keeps holding its socket after systemd
# already considers the unit stopped) using a tiny fixture daemon instead of
# sshd — avoids host keys / systemd-unit setup while isolating exactly the
# shim's process-supervision bug. Also verifies sd_notify(READY=1) reaches a
# stand-in "systemd" socket, since systemd's default NotifyAccess=main only
# trusts datagrams from the MainPID it tracks (the shim parent), not the
# child that actually runs the real daemon.
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

# --- Fix A: signal forwarding + socket release timing ---
header "Signal forwarding (issue #143 restart race)"

"$BINARY" "$PORT" > "$WORKDIR/daemon_output.txt" 2>&1 &
SHIM_PID=$!

info "Waiting for fixture to bind port $PORT..."
for i in $(seq 1 15); do
    ss -ltn 2>/dev/null | grep -q ":$PORT " && break
    [[ $i -eq 15 ]] && fail "notifydaemon did not bind port $PORT in 15s"
    sleep 1
done
pass "Port $PORT bound"

# Signal only the shim's own PID, mirroring systemd signalling just the
# tracked MainPID (not the whole cgroup) on `systemctl restart`.
kill -TERM "$SHIM_PID"
wait "$SHIM_PID" 2>/dev/null || true
pass "Shim (parent, tracked MainPID) exited"

if ss -ltn 2>/dev/null | grep -q ":$PORT "; then
    fail "port $PORT still bound after shim exited — child was not signalled/waited on"
fi
pass "Port released by the time the shim exited"

grep -q "EXITING" "$WORKDIR/daemon_output.txt" \
    || fail "child never ran its own SIGTERM handling — means it wasn't sent a real, catchable signal"
pass "Child received a real signal and shut down on its own (not SIGKILLed)"

SHIM_PID=""

# --- Fix B: sd_notify relay ---
header "sd_notify relay (Type=notify support)"

NOTIFY_SOCK="$WORKDIR/fake-systemd-notify.sock"
"$BINARY" -notify-listener "$NOTIFY_SOCK" > "$WORKDIR/notify_output.txt" 2>&1 &
LISTENER_PID=$!
sleep 0.5

NOTIFY_SOCKET="$NOTIFY_SOCK" "$BINARY" "$((PORT + 1))" > "$WORKDIR/daemon2_output.txt" 2>&1 &
SHIM_PID=$!

info "Waiting for sd_notify relay..."
for i in $(seq 1 10); do
    kill -0 "$LISTENER_PID" 2>/dev/null || break
    [[ $i -eq 10 ]] && fail "sd_notify READY=1 never reached the fake systemd socket"
    sleep 1
done
wait "$LISTENER_PID" 2>/dev/null || true

grep -q "READY=1" "$WORKDIR/notify_output.txt" \
    || fail "fake systemd socket did not receive READY=1"
pass "sd_notify READY=1 relayed to the real NOTIFY_SOCKET"

kill -TERM "$SHIM_PID" 2>/dev/null || true
wait "$SHIM_PID" 2>/dev/null || true
SHIM_PID=""

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (shim signal forwarding + notify relay)"
