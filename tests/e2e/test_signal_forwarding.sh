#!/bin/bash
# E2E test to verify that signal forwarding works correctly.
# If signal forwarding fails (e.g. child daemon left orphaned), this test exits with 1.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

WORKDIR=""
BINARY=""

cleanup() {
    info "Cleaning up..."
    if [[ -n "$BINARY" ]]; then
        funkoverage uninstall "$BINARY" 2>/dev/null || true
    fi
    if [[ -n "$WORKDIR" ]]; then
        rm -rf "$WORKDIR"
    fi
}
trap cleanup EXIT

header "Prerequisites"
require_root
require_funkoverage

# Create workspace
WORKDIR=$(mktemp -d /tmp/funkoverage_signal_forwarding_XXXX)
cd "$WORKDIR"

# 1. Create and compile mock daemon
info "Compiling mock daemon..."
cat > mock_daemon.c << 'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
#include <signal.h>

volatile sig_atomic_t g_running = 1;

void handle_sigterm(int sig) {
    g_running = 0;
}

int main(int argc, char **argv) {
    signal(SIGTERM, handle_sigterm);
    printf("Mock daemon started, PID %d\n", getpid());
    fflush(stdout);

    while (g_running) {
        sleep(1);
    }

    printf("Mock daemon received SIGTERM, exiting gracefully\n");
    fflush(stdout);
    return 0;
}
EOF

gcc -g -O0 mock_daemon.c -o mock_daemon
BINARY="$WORKDIR/mock_daemon"
require_debug_symbols "$BINARY"
pass "Mock daemon compiled with debug symbols"

# 2. Install shim
header "Setup"
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed for mock_daemon"

# 3. Start mock daemon in background
header "Executing Daemon through Shim"
./mock_daemon > stdout.log 2> stderr.log &
SHIM_PID=$!
info "Shim parent started in background with PID $SHIM_PID"

# Wait for daemon to print its PID
sleep 2

# Read child daemon PID from stdout.log or pgrep
CHILD_PID=$(pgrep -P "$SHIM_PID" || true)
if [[ -z "$CHILD_PID" ]]; then
    CHILD_PID=$(grep -oP 'Mock daemon started, PID \K\d+' stdout.log || true)
fi

if [[ -z "$CHILD_PID" ]]; then
    fail "Could not find child daemon PID"
fi
info "Found child daemon PID $CHILD_PID"

# Verify both are running
kill -0 "$SHIM_PID" 2>/dev/null || fail "Shim parent PID $SHIM_PID is not running"
kill -0 "$CHILD_PID" 2>/dev/null || fail "Child daemon PID $CHILD_PID is not running"
pass "Both shim parent and child daemon are running"

# 4. Send SIGTERM to the shim parent
header "Sending SIGTERM to Shim Parent"
info "Sending SIGTERM (kill -15) to shim parent PID $SHIM_PID"
kill -15 "$SHIM_PID"

# Wait for parent shim to exit
info "Waiting for shim parent to exit..."
wait "$SHIM_PID" || true
pass "Shim parent has exited"

# 5. Verify if the child daemon is still running
header "Verification"
if kill -0 "$CHILD_PID" 2>/dev/null; then
    echo -e "  ${RED}FAILURE${NC}: Child daemon PID $CHILD_PID is STILL RUNNING! Signal forwarding failed."
    info "Killing orphaned child daemon..."
    kill -9 "$CHILD_PID" 2>/dev/null || true
    fail "Signal forwarding did not work: child daemon remained running after parent exited."
else
    pass "Child daemon PID $CHILD_PID has exited"
    if grep -q "Mock daemon received SIGTERM, exiting gracefully" stdout.log; then
        pass "Child daemon exited gracefully after receiving forwarded SIGTERM"
    else
        fail "Child daemon exited, but not gracefully (no SIGTERM received, or killed)"
    fi
    echo ""
    echo -e "${GREEN}SUCCESS${NC}: Signal forwarding works!"
fi
