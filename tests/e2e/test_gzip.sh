#!/bin/bash
# E2E test: funkoverage coverage of gzip
# Tests basic shim lifecycle: install, trace, report, uninstall.
# gunzip/zcat are shell wrappers around the gzip binary (not separate ELF
# binaries), so instrumenting gzip alone covers all three entry points.
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
ensure_packages gzip gzip-debuginfo

BINARY=$(which gzip)
require_debug_symbols "$BINARY"
pass "gzip with debug symbols"

# --- Setup ---
header "Setup"
WORKDIR=$(mktemp -d /tmp/funkoverage_gzip_XXXX)
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Exercising gzip"
cd "$WORKDIR"
mkdir -p subdir

seq 1 50000 > numbers.txt
{ yes "the quick brown fox jumps over the lazy dog" || true; } | head -20000 > repetitive.txt
head -c 200000 /dev/urandom | base64 > random.txt
echo "hello world" > subdir/small.txt

gzip -k numbers.txt
[[ -f numbers.txt && -f numbers.txt.gz ]] || fail "keep original failed"
pass "compress + keep original (-k)"

cp repetitive.txt level1.txt && gzip -1 level1.txt
cp repetitive.txt level9.txt && gzip -9 level9.txt
[[ -f level1.txt.gz && -f level9.txt.gz ]] || fail "compression levels failed"
pass "compression level extremes (-1, -9)"

gzip -t numbers.txt.gz
pass "integrity test (-t)"

gzip -l numbers.txt.gz level1.txt.gz >/dev/null
gzip -lv numbers.txt.gz level1.txt.gz >/dev/null
pass "list info (-l, -lv)"

cp numbers.txt dkeep.txt && gzip -k dkeep.txt && rm dkeep.txt
gzip -dk dkeep.txt.gz
[[ -f dkeep.txt && -f dkeep.txt.gz ]] || fail "decompress keep failed"
pass "decompress keep (-dk)"

cp numbers.txt plain.txt && gzip plain.txt && gzip -d plain.txt.gz
[[ -f plain.txt && ! -f plain.txt.gz ]] || fail "plain decompress failed"
pass "plain decompress (-d)"

gzip -c random.txt > random_stdout.gz
gzip -dc random_stdout.gz > /dev/null
pass "stdout mode (-c compress + decompress)"

gunzip -k level9.txt.gz
[[ -f level9.txt ]] || fail "gunzip wrapper failed"
pass "gunzip wrapper script"

zcat random_stdout.gz > /dev/null
pass "zcat wrapper script"

RESULT=$(cat repetitive.txt | gzip -c | gunzip -c)
[[ "$RESULT" == "$(cat repetitive.txt)" ]] || fail "stdin/stdout pipe roundtrip"
pass "stdin/stdout pipe roundtrip"

gzip -r subdir
[[ -f subdir/small.txt.gz ]] || fail "recursive compression failed"
pass "recursive directory compression (-r)"

for i in 1 2 3; do echo "multi $i" > multi_$i.txt; done
gzip multi_1.txt multi_2.txt multi_3.txt
[[ -f multi_1.txt.gz && -f multi_2.txt.gz && -f multi_3.txt.gz ]] || fail "multi-file"
pass "multi-file compress"

echo "overwrite" > force.txt
gzip -k force.txt
echo "new content" > force.txt
gzip -f force.txt
pass "force overwrite (-f)"

echo "verbose" > verbose.txt
gzip -v verbose.txt
gzip -dv verbose.txt.gz
pass "verbose compress/decompress (-v)"

echo "best" > best.txt
gzip --best best.txt
gunzip best.txt.gz
echo "noname" > noname.txt
gzip --no-name noname.txt
gunzip noname.txt.gz
pass "--best / --no-name flags"

gzip --help 2>&1 | grep -q "gzip" || fail "help text missing"
gzip --version >/dev/null
pass "help/version"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*gzip*" 20
remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (gzip)"
