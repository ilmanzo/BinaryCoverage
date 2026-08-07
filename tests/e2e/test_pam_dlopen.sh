#!/bin/bash
# E2E test: funkoverage dlopen JIT tracing of a real PAM module.
# libpam dlopens auth modules (e.g. pam_unix.so) from /usr/lib64/security/ at
# runtime based on /etc/pam.d/<service> — invisible to ldd, only discoverable
# via the dlopen uretprobe JIT path. Run as root on openSUSE with funkoverage
# in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
TEST_USER="funkoverage_pamtest"

cleanup() {
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    userdel -r "$TEST_USER" 2>/dev/null || true
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
ensure_packages pam pam-debuginfo util-linux-debuginfo

BINARY=$(which su)
require_debug_symbols "$BINARY"
pass "su with debug symbols"

[[ -f /usr/lib64/security/pam_unix.so ]] || fail "pam_unix.so not found"
ldd "$BINARY" | grep -q pam_unix && fail "pam_unix.so unexpectedly a direct dependency of su (test assumption broken)"
pass "pam_unix.so present and NOT a static dependency of su (ldd doesn't see it)"

# --- Setup ---
header "Setup"
userdel -r "$TEST_USER" 2>/dev/null || true
useradd -m "$TEST_USER"
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Exercising su (through PAM)"
# Root running su to another user needs no password, but still goes through
# the full PAM stack (pam_unix, pam_env, etc. per /etc/pam.d/su).
su - "$TEST_USER" -c true
pass "su - $TEST_USER -c true"

# --- Report ---
header "Coverage report"
CALLED_PAM=$(grep -h "pam_unix.so" /var/coverage/data/*_called.log 2>/dev/null | wc -l)
[[ "$CALLED_PAM" -gt 0 ]] || fail "no pam_unix.so functions captured"
pass "pam_unix.so functions captured via dlopen JIT ($CALLED_PAM calls)"

info "pam_unix.so functions called:"
grep "pam_unix.so" /var/coverage/data/*_called.log | awk '{print "  - " $3}' | sort -u

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

userdel -r "$TEST_USER" 2>/dev/null || true

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (pam dlopen)"
