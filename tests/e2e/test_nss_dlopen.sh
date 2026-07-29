#!/bin/bash
# E2E test: does dlopen JIT tracing catch glibc's own NSS module loading?
#
# This is a DIAGNOSTIC test, not a must-pass gate. glibc's NSS dispatcher
# (__nss_lookup_function) is known to load modules (libnss_dns.so.2,
# libnss_files.so.2, ...) via an internal __libc_dlopen_mode rather than
# the public dlopen() ELF symbol our uretprobe hooks. Either outcome here
# is informative — we print a clear result either way instead of failing.
#
# Uses a tiny self-built C program (own DWARF, no distro debuginfo
# dependency) that calls getpwnam()/gethostbyname() to trigger NSS lookups.
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
WORKDIR=""

cleanup() {
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
command -v gcc >/dev/null || fail "gcc not installed"

WORKDIR=$(mktemp -d /tmp/funkoverage_nss_XXXX)
cat > "$WORKDIR/nss_test.c" << 'EOF'
#include <stdio.h>
#include <pwd.h>
#include <netdb.h>

void do_nss_lookups(void) {
    struct passwd *pw = getpwnam("root");
    if (pw) printf("getpwnam(root) -> uid %d\n", pw->pw_uid);

    struct hostent *he = gethostbyname("localhost");
    if (he) printf("gethostbyname(localhost) -> resolved\n");
}

int main(void) {
    do_nss_lookups();
    return 0;
}
EOF
gcc -g -gdwarf-4 -o "$WORKDIR/nss_test" "$WORKDIR/nss_test.c"
BINARY="$WORKDIR/nss_test"
require_debug_symbols "$BINARY"
pass "nss_test built with own debug info"

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Exercising NSS lookups"
"$BINARY"
pass "getpwnam + gethostbyname"

# --- Report (diagnostic — never fail()) ---
header "Diagnostic result"
if grep -qh "libnss_" /var/coverage/data/*_called.log 2>/dev/null; then
    pass "NSS module functions WERE captured via dlopen JIT:"
    grep -h "libnss_" /var/coverage/data/*_called.log | awk '{print "  - " $2 " " $3}' | sort -u
else
    info "NSS module functions NOT captured — glibc's internal NSS loader"
    info "does not call the public dlopen() symbol this feature hooks"
    info "(known limitation, not a bug — see docs/dlopen_scalability_plan.md)"
fi

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}DIAGNOSTIC COMPLETE${NC} (nss dlopen)"
