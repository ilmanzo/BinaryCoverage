#!/bin/bash
# E2E test: funkoverage coverage of cpupower (regression guard for issue #128).
#
# cpupower's debuginfo package resolves via .gnu_debuglink, not always via
# .build-id (a stale/missing .build-id symlink is a realistic real-world
# packaging skew). funkoverage used to have no .gnu_debuglink support at
# all, so whenever .build-id resolution failed it fell through to parsing
# the stripped binary's own (absent) DWARF, crashing with:
#   dwarf: decoding dwarf section info at offset 0x0: too short
#
# This test deliberately breaks the .build-id symlink before installing, so
# a regression in .gnu_debuglink resolution is actually exercised rather
# than silently masked by the (already-working) .build-id path.
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""
BID_FILE=""
BID_BACKUP=""

cleanup() {
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
    if [[ -n "$BID_BACKUP" && -f "$BID_BACKUP" ]]; then
        mv "$BID_BACKUP" "$BID_FILE"
    fi
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
ensure_packages cpupower cpupower-debuginfo

BINARY=$(which cpupower)
require_debug_symbols "$BINARY"
pass "cpupower with debug symbols"

# --- Break .build-id resolution to force the .gnu_debuglink fallback ---
header "Simulating stale/missing .build-id symlink"
BUILD_ID=$(readelf -n "$BINARY" 2>/dev/null | grep -A1 "Build ID" | grep "Build ID" | awk '{print $3}')
[[ -n "$BUILD_ID" ]] || fail "could not read cpupower's build-id"
BID_FILE="/usr/lib/debug/.build-id/${BUILD_ID:0:2}/${BUILD_ID:2}.debug"
if [[ -e "$BID_FILE" ]]; then
    BID_BACKUP="${BID_FILE}.funkoverage-bak"
    mv "$BID_FILE" "$BID_BACKUP"
    pass "build-id symlink moved aside: $BID_FILE"
else
    info "build-id symlink already absent — condition already present"
fi

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim "$BINARY"
pass "Shim installed via .gnu_debuglink resolution (not .build-id)"

FLOG=$(ls -t /var/coverage/data/*"$(basename "$BINARY")"*_functions.log | head -1)
FUNC_COUNT=$(grep -c "^FUNC " "$FLOG")
[[ "$FUNC_COUNT" -gt 100 ]] || fail "expected >100 functions enumerated, got $FUNC_COUNT"
pass "$FUNC_COUNT functions enumerated with .build-id broken"

# --- Exercise ---
header "Exercising cpupower"
"$BINARY" frequency-info || true
"$BINARY" -h || true
pass "frequency-info + help"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*cpupower*" 5
remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

# Restore build-id symlink
if [[ -n "$BID_BACKUP" && -f "$BID_BACKUP" ]]; then
    mv "$BID_BACKUP" "$BID_FILE"
    BID_BACKUP="" # prevent double-restore in trap
    pass "build-id symlink restored"
fi

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (cpupower, .gnu_debuglink resolution)"
