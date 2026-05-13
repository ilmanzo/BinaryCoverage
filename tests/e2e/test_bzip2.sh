#!/bin/bash
# E2E test: funkoverage coverage of bzip2
# Tests basic shim lifecycle: install, trace, report, uninstall.
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
ensure_packages bzip2 bzip2-debuginfo

BINARY=$(which bzip2)
require_debug_symbols "$BINARY"
pass "bzip2 with debug symbols"

# --- Setup ---
header "Setup"
WORKDIR=$(mktemp -d /tmp/funkoverage_bzip2_XXXX)
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Exercising bzip2"
cd "$WORKDIR"

echo "Hello funkoverage" > small.txt
bzip2 small.txt
bzip2 -d small.txt.bz2
pass "compress + decompress"

dd if=/dev/urandom bs=1K count=64 of=random.bin 2>/dev/null
bzip2 -9 random.bin
pass "max compression (-9)"

echo "keep me" > keep.txt
bzip2 -k keep.txt
[[ -f keep.txt && -f keep.txt.bz2 ]] || fail "keep original failed"
pass "keep original (-k)"

bzip2 -t keep.txt.bz2
pass "integrity test (-t)"

echo "verbose" > verb.txt
bzip2 verb.txt
bzip2 -dv verb.txt.bz2 2>&1
pass "verbose decompress (-v)"

echo "piped" | bzip2 -c > piped.bz2
RESULT=$(bzip2 -dc piped.bz2)
[[ "$RESULT" == "piped" ]] || fail "stdin/stdout pipe roundtrip"
pass "stdin/stdout pipe (-c)"

for i in 1 2 3; do echo "file $i" > multi_$i.txt; done
bzip2 multi_1.txt multi_2.txt multi_3.txt
[[ -f multi_1.txt.bz2 && -f multi_2.txt.bz2 && -f multi_3.txt.bz2 ]] || fail "multi-file"
pass "multi-file compress"

echo "overwrite" > force.txt
bzip2 -k force.txt
echo "new" > force.txt
bzip2 -f force.txt
pass "force overwrite (-f)"

bzip2 --help 2>&1 | grep -q "bzip2" || fail "help text missing"
pass "help/usage"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*bzip2*" 10
remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (bzip2)"
