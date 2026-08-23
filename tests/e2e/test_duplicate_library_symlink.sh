#!/bin/bash
# E2E reproducer: same physical shared library double-counted in the
# coverage report when it's reachable under two different names.
#
# EnumerateFunctions (cmd/enumerate.go) keys its result map by whatever path
# string discovered it, with no symlink/realpath canonicalization. In real
# coverage runs (see GitHub issue #141) this shows up as ~40 system
# libraries reported twice -- once under a SONAME symlink (e.g. libz.so.1)
# and once under the fully-versioned real file it points to (libz.so.1.3.1)
# -- each copy carrying identical function/coverage data, inflating both
# the "unique images" count and the aggregate totals.
#
# This reproduces the same defect with two purpose-built, non-system
# libraries so it never touches anything the host actually depends on:
#   libdir_a/libfoo.so.1.0.0 (real file, SONAME "libfoo.so.1")
#     + libdir_a/libfoo.so.1 (symlink -> libfoo.so.1.0.0)
#   libdir_b/libfoo.so.1.0.0 (identical source, built with NO soname)
# prog_a links against libdir_a (DT_NEEDED "libfoo.so.1", resolved via its
# symlink); prog_b links against libdir_b (DT_NEEDED "libfoo.so.1.0.0"
# directly, since a library with no soname makes the linker fall back to
# the literal filename). Same functions (foo_add, foo_mul), same content,
# two different image names once both are installed and reported on.
#
# Run as root on openSUSE with funkoverage in PATH.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/lib_test_helpers.sh"

WORKDIR=""
PROG_A=""
PROG_B=""

cleanup() {
    [[ -n "$PROG_A" ]] && funkoverage uninstall "$PROG_A" 2>/dev/null || true
    [[ -n "$PROG_B" ]] && funkoverage uninstall "$PROG_B" 2>/dev/null || true
    [[ -n "$WORKDIR" ]] && rm -rf "$WORKDIR"
}
trap cleanup EXIT

# --- Prerequisites ---
header "Prerequisites"
require_root
require_funkoverage
command -v gcc >/dev/null || fail "gcc not installed"

WORKDIR=$(mktemp -d /tmp/funkoverage_dupelib_XXXX)
mkdir -p "$WORKDIR/libdir_a" "$WORKDIR/libdir_b"

cat > "$WORKDIR/libfoo.c" << 'EOF'
int foo_add(int a, int b) { return a + b; }
int foo_mul(int a, int b) { return a * b; }
EOF

gcc -shared -fPIC -g -Wl,-soname,libfoo.so.1 \
    -o "$WORKDIR/libdir_a/libfoo.so.1.0.0" "$WORKDIR/libfoo.c"
ln -sf libfoo.so.1.0.0 "$WORKDIR/libdir_a/libfoo.so.1"

gcc -shared -fPIC -g \
    -o "$WORKDIR/libdir_b/libfoo.so.1.0.0" "$WORKDIR/libfoo.c"

cat > "$WORKDIR/prog.c" << 'EOF'
extern int foo_add(int, int);
extern int foo_mul(int, int);
int main(void) { return foo_add(1, 2) + foo_mul(2, 3); }
EOF

gcc -O0 -g -o "$WORKDIR/prog_a" "$WORKDIR/prog.c" \
    -L"$WORKDIR/libdir_a" -l:libfoo.so.1 -Wl,-rpath,"$WORKDIR/libdir_a"
gcc -O0 -g -o "$WORKDIR/prog_b" "$WORKDIR/prog.c" \
    -L"$WORKDIR/libdir_b" -l:libfoo.so.1.0.0 -Wl,-rpath,"$WORKDIR/libdir_b"
PROG_A="$WORKDIR/prog_a"
PROG_B="$WORKDIR/prog_b"
pass "prog_a (needs libfoo.so.1) and prog_b (needs libfoo.so.1.0.0) built"

ldd "$PROG_A" | grep -q "libfoo.so.1 => $WORKDIR/libdir_a/libfoo.so.1 " \
    || fail "prog_a should resolve libfoo via its SONAME symlink"
ldd "$PROG_B" | grep -q "libfoo.so.1.0.0 => $WORKDIR/libdir_b/libfoo.so.1.0.0 " \
    || fail "prog_b should resolve libfoo via its real filename directly"
pass "confirmed: same library content, two different resolved names"

# --- Setup ---
header "Setup"
clean_coverage_data
install_shim "$PROG_A"
install_shim "$PROG_B"
pass "both programs installed (library tracing enabled)"

# --- Exercise ---
header "Exercising both programs"
"$PROG_A" || true
"$PROG_B" || true
pass "foo_add and foo_mul called through both programs"

# --- Uninstall ---
header "Uninstall"
funkoverage uninstall "$PROG_A"
funkoverage uninstall "$PROG_B"
PROG_A=""; PROG_B="" # prevent double-uninstall in trap

# --- Report: this is the actual bug check ---
header "Coverage report"
images=$(grep -h '^FUNC ' /var/coverage/data/*_functions.log \
            | awk '{print $2}' | grep libfoo | xargs -n1 basename | sort -u)
count=$(echo "$images" | wc -l)
info "distinct libfoo image names discovered: $count"
echo "$images" | sed 's/^/    /'

total_foo_functions=$(grep -h '^FUNC ' /var/coverage/data/*_functions.log \
                        | awk '{print $2, $3}' | grep -E 'foo_(add|mul)$' | sort -u | wc -l)
info "total distinct (image, function) pairs for foo_add/foo_mul: $total_foo_functions"

if [[ "$count" -ne 1 ]]; then
    fail "BUG REPRODUCED: libfoo.so.1 and libfoo.so.1.0.0 are the exact same file, reported as $count separate images ($total_foo_functions function entries instead of 2). Root cause: EnumerateFunctions (cmd/enumerate.go) never canonicalizes a library path via filepath.EvalSymlinks before using it as a report key. See plan.md section 4."
fi
pass "libfoo reported as a single image (bug fixed)"

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (duplicate library symlink/realpath aliasing)"
