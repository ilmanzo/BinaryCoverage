#!/bin/bash
# E2E test: funkoverage dlopen JIT tracing of a real nginx dynamic module.
# nginx dlopens modules/*.so at config-parse time via `load_module`, once,
# well before workers start serving traffic — invisible to ldd, and a good
# complementary case to PAM's tighter dlopen->call window. Run as root on
# openSUSE with funkoverage in PATH.
#
# IMPORTANT: nginx daemonizes by default (forks, parent process exits) —
# funkoverage tracks the exec'd process by PID, so if nginx backgrounds
# itself the "real" long-running master+workers continue completely
# untraced. Must run with `-g "daemon off;"` and background via the shell
# instead, exactly like squid's test does with `"$BINARY" &`.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
WORKDIR=""
MODULE_SO="/usr/lib64/nginx/modules/ngx_http_echo_module.so"
PORT=18080

cleanup() {
    pkill -f "nginx: (master|worker) process" 2>/dev/null || true
    sleep 1
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
ensure_packages nginx nginx-debuginfo nginx-module-echo nginx-module-echo-debuginfo

BINARY=$(which nginx)
require_debug_symbols "$BINARY"
pass "nginx with debug symbols"

[[ -f "$MODULE_SO" ]] || fail "$MODULE_SO not found"
ldd "$BINARY" | grep -q echo_module && fail "echo module unexpectedly a direct dependency of nginx"
pass "echo module present and NOT a static dependency of nginx (ldd doesn't see it)"

# --- Setup ---
header "Setup"
pkill -f "nginx: (master|worker) process" 2>/dev/null || true
sleep 1

WORKDIR=$(mktemp -d /tmp/funkoverage_nginx_XXXX)
cat > "$WORKDIR/nginx.conf" << EOF
load_module $MODULE_SO;
worker_processes 1;
error_log $WORKDIR/error.log;
pid $WORKDIR/nginx.pid;
events { worker_connections 16; }
http {
    access_log off;
    server {
        listen $PORT;
        location /echo {
            echo "funkoverage test";
        }
    }
}
EOF
"$BINARY" -t -c "$WORKDIR/nginx.conf" || fail "nginx config test failed"

clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Starting nginx (daemon off, backgrounded by shell)"
"$BINARY" -c "$WORKDIR/nginx.conf" -g "daemon off;" &

info "Waiting for nginx to accept connections..."
for i in $(seq 1 15); do
    if curl -s -o /dev/null "http://127.0.0.1:$PORT/echo" 2>/dev/null; then
        pass "nginx ready after ${i}s"
        break
    fi
    [[ $i -eq 15 ]] && fail "nginx did not start in 15s"
    sleep 1
done

RESULT=$(curl -s "http://127.0.0.1:$PORT/echo")
[[ "$RESULT" == "funkoverage test" ]] || fail "unexpected response: $RESULT"
pass "GET /echo returned expected body"

# --- Stop + report ---
header "Stop nginx + report"
"$BINARY" -c "$WORKDIR/nginx.conf" -s stop
sleep 1

if grep -qh "ngx_http_echo_handler" /var/coverage/data/*_called.log 2>/dev/null; then
    pass "ngx_http_echo_handler (the actual per-request handler) was traced"
else
    fail "ngx_http_echo_handler not captured — dynamic module request path missed"
fi

info "echo module functions called:"
grep -h "ngx_http_echo" /var/coverage/data/*_called.log | awk '{print "  - " $3}' | sort -u

# Verify it's genuinely JIT-discovered, not just present because ldd/static
# enumeration somehow picked it up.
IMAGE_HIT=$(grep -lh "$MODULE_SO" /var/coverage/data/*_functions.log 2>/dev/null | wc -l)
[[ "$IMAGE_HIT" -gt 0 ]] || fail "echo module functions never appeared in a functions.log"
pass "echo module functions registered via the runtime (dlopen JIT) functions log"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (nginx dlopen)"
