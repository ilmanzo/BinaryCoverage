#!/bin/bash
# E2E test: funkoverage's demangler (github.com/ianlancetaylor/demangle) also
# handles Rust's mangling scheme, not just Itanium C++ -- but until this test,
# nothing exercised that: every other demangling assertion in this suite
# (test_squid.sh) is against a C++ binary. Rust's scheme produces genuinely
# different output shapes (trait-impl syntax "<T as Trait>::method",
# monomorphized generics with "::<...>", closures) that a demangler tuned
# only against real-world C++ output could still get wrong.
#
# ripgrep is a real, mostly-statically-linked Rust binary shipped by the
# distro with a matching -debuginfo package -- --no-libs keeps the assertion
# on its own (Rust) symbols, not its small C dependency (libpcre2).
#
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
ensure_packages ripgrep ripgrep-debuginfo
BINARY=$(which rg)
require_debug_symbols "$BINARY"

FUNC_COUNT=$(funkoverage enumerate --no-libs "$BINARY" 2>&1 | tail -1 | grep -oP '\d+(?= functions)')
pass "ripgrep (rg) with debug symbols ($FUNC_COUNT functions)"

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim --no-libs "$BINARY"
pass "Shim installed"

# --- Exercise ---
header "Exercising rg"
WORKDIR=$(mktemp -d /tmp/funkoverage_rg_XXXX)
printf 'hello world\nfoo bar\nHELLO AGAIN\n' > "$WORKDIR/a.txt"
printf 'line one\nline two\nfoo baz\n' > "$WORKDIR/b.txt"

rg "hello" "$WORKDIR" >/dev/null
pass "plain search"
rg -i "hello" "$WORKDIR" >/dev/null
pass "case-insensitive search (-i)"
rg -v "foo" "$WORKDIR" >/dev/null
pass "inverted match (-v)"
rg -l "foo" "$WORKDIR" >/dev/null
pass "list files (-l)"
rg -c "line" "$WORKDIR" >/dev/null
pass "count matches (-c)"
rg --json "foo" "$WORKDIR" >/dev/null
pass "JSON output (--json)"
rg -e "l[io]ne" -e "bar" "$WORKDIR" >/dev/null
pass "multiple patterns (-e)"
rg --version >/dev/null
pass "version/help"

# --- Coverage report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*rg*" 100

DEMANGLED=$(grep -h '^CALLED ' /var/coverage/data/*rg*_called.log 2>/dev/null | grep '::' | head -1 || true)
[[ -n "$DEMANGLED" ]] || fail "no demangled Rust names found"
pass "Rust demangling working: $DEMANGLED"

# Trait-impl syntax ("<Type as Trait>::method") only comes out of Rust's
# mangling scheme -- Itanium C++ demangling never produces it. Finding one
# proves the demangler is actually parsing Rust's own scheme, not just
# splitting on "::" the way a namespace-only check (as used for C++) would.
TRAIT_IMPL=$(grep -h '^CALLED ' /var/coverage/data/*rg*_called.log 2>/dev/null | grep -F ' as ' || true)
[[ -n "$TRAIT_IMPL" ]] || fail "no Rust trait-impl symbol (<T as Trait>::method) found among called functions"
pass "Rust trait-impl demangling confirmed: $(echo "$TRAIT_IMPL" | head -1 | cut -d' ' -f3-)"

remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (rust demangling, ripgrep)"
