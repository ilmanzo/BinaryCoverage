#!/bin/bash
# E2E test to verify that systemd Type=notify relay proxy works correctly.
# If notification relay fails (e.g. child notification is not forwarded), this test exits with 1.
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
WORKDIR=$(mktemp -d /tmp/funkoverage_notify_proxy_XXXX)
cd "$WORKDIR"

# 1. Create and compile mock daemon
info "Compiling mock daemon..."
cat > mock_daemon.c << 'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <sys/socket.h>
#include <sys/un.h>
#include <stddef.h>

void dummy_function() {
    printf("Dummy function called\n");
}

int main() {
    dummy_function();
    const char *e = getenv("NOTIFY_SOCKET");
    if (!e) {
        fprintf(stderr, "NOTIFY_SOCKET not set\n");
        return 1;
    }
    printf("Mock daemon started, NOTIFY_SOCKET is %s\n", e);
    fflush(stdout);

    int fd = socket(AF_UNIX, SOCK_DGRAM, 0);
    if (fd < 0) {
        perror("socket");
        return 1;
    }

    struct sockaddr_un addr;
    memset(&addr, 0, sizeof(addr));
    addr.sun_family = AF_UNIX;
    strncpy(addr.sun_path, e, sizeof(addr.sun_path) - 1);

    int len = sizeof(struct sockaddr_un);
    const char *msg = "READY=1\nSTATUS=Mock daemon is ready";
    if (sendto(fd, msg, strlen(msg), 0, (struct sockaddr *)&addr, len) < 0) {
        perror("sendto");
        close(fd);
        return 1;
    }

    printf("Notification sent successfully\n");
    fflush(stdout);
    close(fd);
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

# 3. Set up mock notification socket listener in Python
header "Setting up Mock Notification Listener"
MOCK_SOCKET="$WORKDIR/systemd_notify_socket"

cat > listener.py << 'EOF'
import socket
import sys
import os

socket_path = sys.argv[1]
if os.path.exists(socket_path):
    os.remove(socket_path)

s = socket.socket(socket.AF_UNIX, socket.SOCK_DGRAM)
s.bind(socket_path)
s.settimeout(5) # 5 seconds timeout

try:
    data, addr = s.recvfrom(4096)
    print(data.decode('utf-8'))
except socket.timeout:
    print("TIMEOUT")
    sys.exit(1)
finally:
    s.close()
    if os.path.exists(socket_path):
        os.remove(socket_path)
EOF

python3 listener.py "$MOCK_SOCKET" > received.log &
LISTENER_PID=$!
info "Mock notification listener started in background with PID $LISTENER_PID"

# Wait a brief moment to ensure the listener is bound and ready to receive
sleep 1

# 4. Run the daemon through the shim with NOTIFY_SOCKET set
header "Executing Daemon and Sending Notification"
export NOTIFY_SOCKET="$MOCK_SOCKET"
./mock_daemon > daemon.log 2>&1
pass "Mock daemon finished execution"

# Wait for the python listener to exit and capture its status
wait "$LISTENER_PID" || fail "Notification listener timed out or exited with an error"

# 5. Verification
header "Verification"
if [[ ! -f received.log ]]; then
    fail "No notification data was received (received.log missing)"
fi

echo "Received Notification Data:"
cat received.log
echo ""

if grep -q "READY=1" received.log && grep -q "STATUS=Mock daemon is ready" received.log; then
    pass "Notification correctly received and relayed by the shim parent!"
    echo -e "${GREEN}SUCCESS${NC}: systemd Type=notify relay proxy works!"
else
    fail "Notification was not received or contains incorrect data!"
fi
