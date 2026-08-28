#!/bin/bash
# E2E test: library-local function tracing without touching the library.
#
# uprobes are normally attached by resolving symbol names against the exact
# file the kernel maps at runtime. A shared library's local/static functions
# (e.g. GMP's internal mpn_* helpers) live only in an external debuginfo
# file's .symtab, not in the stripped runtime .so's .dynsym, so name-based
# attach cannot see them at all.
#
# funkoverage used to fix that by merging the debug info into the real system
# library in place with eu-unstrip. It no longer does: it computes each
# debug-only function's file offset at install time and attaches by address,
# guarded by the library's build-id. The library on disk is never written to.
# That is what this test exists to prove — the sha256 assertions below are the
# point of it, not a detail.
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

# Internal libgmp functions reachable only by address-based attach. Measured at
# 15 on Tumbleweed 20260825 (libgmp 10.5.0); the floor sits below that so a
# distro with a differently-built GMP does not fail, while a silent regression
# to name-only attach — which yields 0, since none of these are in .dynsym —
# still does.
MIN_LIB_CALLS=5

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

LIBGMP_REAL=$(readlink -f "$LIBGMP")
SHA_BEFORE=$(sha256sum "$LIBGMP_REAL" | awk '{print $1}')
GMP_PKG=$(rpm -qf --qf '%{NAME}' "$LIBGMP_REAL" 2>/dev/null || true)
# The baseline is what rpm already says, not a clean bill of health: an earlier
# funkoverage that merged debug info in place and restored it leaves an
# mtime-only difference behind forever. What must not change is this state.
rpm_state() {
    [[ -n "$GMP_PKG" ]] || return 0
    rpm -V "$GMP_PKG" 2>/dev/null | grep -F "$LIBGMP_REAL" || true
}
RPM_BEFORE=$(rpm_state)

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim "$BINARY"
pass "Shim installed (library tracing enabled)"

# --- Verify the library was left alone ---
header "Verify library integrity"
SHA_AFTER_INSTALL=$(sha256sum "$LIBGMP_REAL" | awk '{print $1}')
[[ "$SHA_AFTER_INSTALL" == "$SHA_BEFORE" ]] \
    || fail "install modified $LIBGMP_REAL ($SHA_BEFORE -> $SHA_AFTER_INSTALL)"
BACKUP_SIDECAR="/var/coverage/bin/$(basename "$BINARY").libbackup.json"
[[ -f "$BACKUP_SIDECAR" ]] && fail "install wrote a library backup sidecar; nothing should merge libraries any more"
RPM_AFTER=$(rpm_state)
[[ "$RPM_AFTER" == "$RPM_BEFORE" ]] \
    || fail "install changed what rpm -V $GMP_PKG reports for $LIBGMP_REAL: '$RPM_BEFORE' -> '$RPM_AFTER'"
[[ -n "$GMP_PKG" ]] && pass "rpm -V $GMP_PKG unchanged for $LIBGMP_REAL"
pass "libgmp untouched by install (sha256 $SHA_BEFORE)"

# --- Exercise ---
header "Exercising gmp_demo"
"$BINARY"
pass "FFT multiply, sqrt, Miller-Rabin, binomial, rational compare"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*gmp_demo*" 10
# These are reachable only through the address-based attach path: they are
# absent from the runtime .so's .dynsym, so a regression to name-only attach
# shows up here as zero.
LIB_CALLS=$(grep -ch "libgmp" /var/coverage/data/*gmp_demo*_called.log 2>/dev/null || echo 0)
[[ "$LIB_CALLS" -ge "$MIN_LIB_CALLS" ]] \
    || fail "expected at least $MIN_LIB_CALLS traced libgmp function calls, got $LIB_CALLS"
pass "$LIB_CALLS libgmp internal functions traced"
remove_report_dir "$REPORT_DIR"

# --- Uninstall + verify library restore ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

SHA_AFTER_UNINSTALL=$(sha256sum "$LIBGMP_REAL" | awk '{print $1}')
[[ "$SHA_AFTER_UNINSTALL" == "$SHA_BEFORE" ]] \
    || fail "uninstall left $LIBGMP_REAL modified ($SHA_BEFORE -> $SHA_AFTER_UNINSTALL)"
pass "libgmp byte-identical across the whole install/uninstall cycle"

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (gmp, address-based library tracing)"
