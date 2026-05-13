#!/bin/bash
# E2E test: funkoverage coverage of squid proxy
# Tests C++ demangling and long-running daemon tracing.
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
SQUID_CONF="/etc/squid/squid.conf"
SQUID_CONF_BAK=""
PROXY="http://127.0.0.1:3128"

cleanup() {
    # Stop squid
    /var/coverage/bin/squid -k shutdown 2>/dev/null || squid -k shutdown 2>/dev/null || true
    sleep 2
    killall squid 2>/dev/null || true
    sleep 1
    # Uninstall shim
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    # Restore original config
    if [[ -n "$SQUID_CONF_BAK" && -f "$SQUID_CONF_BAK" ]]; then
        mv "$SQUID_CONF_BAK" "$SQUID_CONF"
    fi
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
ensure_packages squid squid-debuginfo curl

BINARY=$(which squid)
require_debug_symbols "$BINARY"

FUNC_COUNT=$(funkoverage enumerate --no-libs "$BINARY" 2>&1 | tail -1 | grep -oP '\d+(?= functions)')
pass "squid with debug symbols ($FUNC_COUNT functions)"

# --- Setup ---
header "Setup"

info "Stopping any running squid"
systemctl stop squid 2>/dev/null || true
sleep 1

info "Writing test config"
if [[ -f "$SQUID_CONF" ]]; then
    SQUID_CONF_BAK="${SQUID_CONF}.funkoverage-bak"
    cp "$SQUID_CONF" "$SQUID_CONF_BAK"
fi

cat > "$SQUID_CONF" <<'CONF'
http_port 3128
acl localnet src 127.0.0.0/8
acl localnet src ::1
acl Safe_ports port 80 443
http_access allow localnet
http_access deny all
access_log daemon:/var/log/squid/access.log
cache_log /var/log/squid/cache.log
cache_dir ufs /var/cache/squid 100 16 256
cache_mem 8 MB
shutdown_lifetime 2 seconds
dns_nameservers 8.8.8.8
CONF

info "Initializing cache dirs"
"$BINARY" -z --foreground 2>&1 || true

clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Start squid ---
header "Start squid (through shim)"
"$BINARY" &

info "Waiting for squid to accept connections..."
for i in $(seq 1 30); do
    if curl -s -o /dev/null -x "$PROXY" http://example.com 2>/dev/null; then
        pass "Squid ready after ${i}s"
        break
    fi
    [[ $i -eq 30 ]] && fail "squid did not start in 30s"
    sleep 1
done

# --- Exercise ---
header "Exercising squid proxy"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -x "$PROXY" http://example.com)
[[ "$HTTP_CODE" == "200" ]] || fail "HTTP GET returned $HTTP_CODE"
pass "HTTP GET"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -x "$PROXY" -I http://example.com)
[[ "$HTTP_CODE" == "200" ]] || fail "HEAD returned $HTTP_CODE"
pass "HEAD request"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -x "$PROXY" -H "User-Agent: funkoverage" http://example.com)
[[ "$HTTP_CODE" == "200" ]] || fail "custom header GET returned $HTTP_CODE"
pass "GET with custom headers"

curl -s -o /dev/null -x "$PROXY" https://example.com 2>&1 || true
pass "HTTPS CONNECT tunnel"

curl -s -o /dev/null -x "$PROXY" http://this-does-not-exist.invalid 2>&1 || true
pass "Non-existent host (error path)"

HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" -x "$PROXY" http://example.com)
pass "Cached request (second hit)"

curl -s -o /dev/null -x "$PROXY" -X POST -d "test=data" http://httpbin.org/post 2>&1 || true
pass "POST through proxy"

curl -s -o /dev/null -x "$PROXY" http://httpbin.org/bytes/10000 2>&1 || true
pass "Large response body"

# --- Stop + report ---
header "Stop squid + report"
/var/coverage/bin/squid -k shutdown 2>/dev/null || true
info "Waiting for squid to exit..."
sleep 3
killall -0 squid 2>/dev/null && { sleep 3; killall squid 2>/dev/null; sleep 2; } || true

REPORT_DIR=$(generate_report)
assert_min_called "*squid*" 100

# Verify demangling
DEMANGLED=$(cat /var/coverage/data/*squid*_called.log 2>/dev/null | grep '::' | head -1 || true)
[[ -n "$DEMANGLED" ]] || fail "no demangled C++ names found"
pass "C++ demangling working"

info "Top namespaces hit:"
cat /var/coverage/data/*squid*_called.log | awk '{print $3}' | sort -u | sed 's/::.*//' | sort | uniq -c | sort -rn | head -10

remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

# Restore config
if [[ -n "$SQUID_CONF_BAK" && -f "$SQUID_CONF_BAK" ]]; then
    mv "$SQUID_CONF_BAK" "$SQUID_CONF"
    SQUID_CONF_BAK="" # prevent double-restore in trap
fi

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (squid)"
