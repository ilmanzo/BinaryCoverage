#!/bin/bash
# E2E test: funkoverage must resolve a CPU-optimized (glibc-hwcaps) library
# variant to the exact file ld.so actually maps, not the plain fallback
# living in the same directory.
#
# ld.so tries <dir>/glibc-hwcaps/<level>/<soname> before <dir>/<soname>
# (cmd/libdeps.go: findInDir/hwcapsUsable). Distros ship real, differently
# -built copies of common libraries under both paths -- on this host
# bzip2's only library dependency, libbz2, is one of them:
#   /usr/lib64/libbz2.so.1.0.6                          (plain build)
#   /usr/lib64/glibc-hwcaps/x86-64-v3/libbz2.so.1.0.6   (v3-optimized build)
# Different content (confirmed via sha256 below), same soname. The running
# bzip2 process maps the v3 file. If the dependency resolver ever regresses
# to picking the plain one -- wrong search order, a hwcaps level check that's
# too permissive -- it attaches uprobes to an inode the process never
# touches: no error anywhere, just silent 0% coverage for every libbz2
# function. That failure mode is exactly what this test pins down.
#
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

BINARY=""

cleanup() {
    [[ -n "$BINARY" ]] && funkoverage uninstall "$BINARY" 2>/dev/null || true
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
BINARY=$(which bzip2)
require_debug_symbols "$BINARY"

REAL_LIB=$(ldd "$BINARY" | awk '/libbz2\.so/ {print $3}')
[[ -n "$REAL_LIB" ]] || fail "bzip2 does not depend on libbz2 on this host"
case "$REAL_LIB" in
    */glibc-hwcaps/*) ;;
    *)
        info "this host/distro does not ship a glibc-hwcaps build of libbz2"
        info "(package build policy, not a CPU or funkoverage limitation) -- nothing to check here"
        echo ""
        echo -e "${GREEN}SKIPPED${NC} (glibc-hwcaps library resolution)"
        exit 0
        ;;
esac
REAL_LIB=$(readlink -f "$REAL_LIB")
PLAIN_LIB=$(readlink -f "$(echo "$REAL_LIB" | sed -E 's#/glibc-hwcaps/[^/]+/#/#')")
[[ -f "$PLAIN_LIB" && "$PLAIN_LIB" != "$REAL_LIB" ]] \
    || fail "expected a distinct plain-path libbz2 build to exist alongside the hwcaps one"
[[ "$(sha256sum "$REAL_LIB" | cut -d' ' -f1)" != "$(sha256sum "$PLAIN_LIB" | cut -d' ' -f1)" ]] \
    || fail "hwcaps and plain libbz2 builds are byte-identical -- test can't distinguish them"
pass "confirmed: ld.so maps $REAL_LIB, distinct plain build at $PLAIN_LIB"

# --- Enumerate: catches the bug before any install/attach machinery runs ---
header "Enumerate"
ENUM_OUT=$(funkoverage enumerate "$BINARY")
echo "$ENUM_OUT" | grep -qF "$REAL_LIB " \
    || fail "enumerate did not list $REAL_LIB -- resolver picked something else"
echo "$ENUM_OUT" | grep -qF "$PLAIN_LIB " \
    && fail "enumerate also listed the plain-path build $PLAIN_LIB -- two images for one library"
pass "enumerate resolved libbz2 to the exact hwcaps file ld.so maps"

# --- Setup + exercise + report: confirms the uprobes actually land there ---
header "Setup"
clean_coverage_data
install_shim "$BINARY"
pass "Shim installed (library tracing enabled)"

header "Exercising bzip2"
echo "hello hwcaps" | bzip2 -c | bzip2 -d -c >/dev/null
pass "compress + decompress"

header "Coverage report"
REPORT_DIR=$(generate_report)
LIBBZ2_CALLED=$(grep -h '^CALLED ' /var/coverage/data/*_called.log 2>/dev/null \
    | awk -v f="$REAL_LIB" '$2==f' | wc -l)
info "libbz2 functions called via $REAL_LIB: $LIBBZ2_CALLED"
[[ "$LIBBZ2_CALLED" -gt 0 ]] \
    || fail "no libbz2 functions traced through the hwcaps-resolved image -- uprobes attached to the wrong file"
pass "libbz2 coverage recorded against the correct (hwcaps) image"
remove_report_dir "$REPORT_DIR"

header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (glibc-hwcaps library resolution)"
