#!/bin/bash
# E2E test: library-local function tracing via in-place debug merge.
#
# uprobes are attached by resolving symbol names against the exact file the
# kernel maps at runtime. A shared library's local/static functions (e.g.
# GMP's internal mpn_* helpers) live only in an external debuginfo file's
# .symtab, not in the stripped runtime .so's .dynsym — so without merging
# that debug info into the real library file in place, uprobe_multi fails
# to resolve even one requested symbol and the WHOLE library's uprobes get
# silently dropped:
#   funkoverage-shim: skipping uprobes on /lib64/libgmp.so.10: symbol
#   mpn_fft_initl: not found
#
# This uses a tiny self-built program (own DWARF, no distro debuginfo
# dependency) that calls into GMP's FFT/Toom-Cook/sqrt/Miller-Rabin
# internals with large operands, so real internal libgmp functions
# (not just the __gmpz_* public API, which a separate "__"-prefix filter
# excludes from tracing) actually get exercised and traced.
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
ensure_packages gmp-devel libgmp10-debuginfo

WORKDIR=$(mktemp -d /tmp/funkoverage_gmp_XXXX)
cat > "$WORKDIR/gmp_demo.c" << 'EOF'
#include <stdio.h>
#include <stdlib.h>
#include <gmp.h>

int main(void) {
    gmp_randstate_t rng;
    gmp_randinit_mt(rng);
    gmp_randseed_ui(rng, 42);

    mpz_t big_a, big_b, prod, quo, rem, root, gcdv;
    mpz_inits(big_a, big_b, prod, quo, rem, root, gcdv, NULL);

    /* Large random operands: pushes multiplication/division past the
     * schoolbook algorithm into GMP's FFT/Toom-Cook/divide-and-conquer
     * internals. */
    mpz_urandomb(big_a, rng, 4096);
    mpz_urandomb(big_b, rng, 4096);

    mpz_mul(prod, big_a, big_b);
    mpz_tdiv_qr(quo, rem, prod, big_b);
    mpz_sqrt(root, prod);
    mpz_gcd(gcdv, big_a, big_b);

    gmp_printf("prod has %zu bits\n", mpz_sizeinbase(prod, 2));
    gmp_printf("sqrt(prod) has %zu digits\n", mpz_sizeinbase(root, 10));

    mpz_t candidate;
    mpz_init_set_ui(candidate, 1000000007ULL);
    printf("1000000007 probably prime: %s\n", mpz_probab_prime_p(candidate, 25) ? "yes" : "no");

    mpz_t binom;
    mpz_init(binom);
    mpz_bin_uiui(binom, 500, 250);
    printf("C(500,250) has %zu digits\n", mpz_sizeinbase(binom, 10));

    mpq_t q1, q2;
    mpq_inits(q1, q2, NULL);
    mpq_set_ui(q1, 355, 113);
    mpq_set_ui(q2, 22, 7);
    printf("355/113 < 22/7: %s\n", mpq_cmp(q1, q2) < 0 ? "yes" : "no");

    mpz_clears(big_a, big_b, prod, quo, rem, root, gcdv, candidate, binom, NULL);
    mpq_clears(q1, q2, NULL);
    gmp_randclear(rng);
    return 0;
}
EOF
gcc -O2 -o "$WORKDIR/gmp_demo" "$WORKDIR/gmp_demo.c" -lgmp
BINARY="$WORKDIR/gmp_demo"
pass "gmp_demo built"

LIBGMP=$(ldd "$BINARY" | grep libgmp | awk '{print $3}')
[[ -n "$LIBGMP" ]] || fail "could not resolve libgmp.so path via ldd"
require_debug_symbols "$LIBGMP"
pass "libgmp debug symbols available ($LIBGMP)"

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim "$BINARY"
pass "Shim installed (library tracing enabled)"

# --- Verify libgmp was merged in place, with a backup recorded ---
header "Verify library debug merge"
readelf -S "$LIBGMP" 2>&1 | grep -q "\.symtab" \
    || fail "libgmp should now have an embedded .symtab after merge"
BACKUP_SIDECAR="/var/coverage/bin/$(basename "$BINARY").libbackup.json"
[[ -f "$BACKUP_SIDECAR" ]] || fail "expected library backup sidecar at $BACKUP_SIDECAR"
grep -q "$LIBGMP" "$BACKUP_SIDECAR" || fail "backup sidecar does not reference $LIBGMP"
pass "libgmp merged in place; backup recorded in $BACKUP_SIDECAR"

# --- Exercise ---
header "Exercising gmp_demo"
"$BINARY"
pass "FFT multiply, sqrt, Miller-Rabin, binomial, rational compare"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*gmp_demo*" 10
LIB_CALLS=$(grep -ch "libgmp" /var/coverage/data/*gmp_demo*_called.log 2>/dev/null || echo 0)
[[ "$LIB_CALLS" -gt 0 ]] || fail "expected at least one traced libgmp function call, got 0"
pass "$LIB_CALLS libgmp internal functions traced"
remove_report_dir "$REPORT_DIR"

# --- Uninstall + verify library restore ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

readelf -S "$LIBGMP" 2>&1 | grep -q "\.symtab" \
    && fail "libgmp should be back to its stripped state after uninstall"
[[ -f "$BACKUP_SIDECAR" ]] && fail "backup sidecar should be removed after uninstall"
pass "libgmp restored to its pre-merge stripped state"

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (gmp, library debug merge)"
