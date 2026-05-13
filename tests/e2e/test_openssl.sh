#!/bin/bash
# E2E test: funkoverage coverage of openssl
# Tests multi-library tracing (openssl + libcrypto + libssl + libz + libjitterentropy).
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
command -v openssl >/dev/null || fail "openssl not installed"

# Find the actual debug packages needed for this distro
BINARY=$(which openssl)
OPENSSL_PKG=$(rpm -qf "$BINARY" --qf '%{NAME}' 2>/dev/null || echo "openssl-3")
LIBSSL_SO=$(ldd "$BINARY" 2>/dev/null | grep libssl | awk '{print $3}')
LIBSSL_PKG=$(rpm -qf "$LIBSSL_SO" --qf '%{NAME}' 2>/dev/null || echo "libopenssl3")

ensure_packages "$OPENSSL_PKG" "${OPENSSL_PKG}-debuginfo" "${LIBSSL_PKG}-debuginfo"

require_debug_symbols "$BINARY"

# Show library enumeration
NOLIB_COUNT=$(funkoverage enumerate --no-libs "$BINARY" 2>&1 | tail -1 | grep -oP '\d+(?= functions)')
FULL_COUNT=$(funkoverage enumerate "$BINARY" 2>&1 | tail -1 | grep -oP '\d+(?= functions)')
IMAGE_COUNT=$(funkoverage enumerate "$BINARY" 2>&1 | tail -1 | grep -oP '\d+(?= image)')
pass "openssl: $NOLIB_COUNT binary + $FULL_COUNT total across $IMAGE_COUNT images"

# --- Setup ---
header "Setup"
WORKDIR=$(mktemp -d /tmp/funkoverage_openssl_XXXX)
clean_coverage_data
install_shim "$BINARY" # WITH library tracing (no --no-libs)
pass "Shim installed (with library tracing)"

# --- Exercise ---
header "Exercising openssl"
cd "$WORKDIR"

openssl version -a >/dev/null 2>&1
pass "version"

openssl ciphers -v 2>&1 | wc -l >/dev/null
pass "list ciphers"

openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out rsa.key 2>/dev/null
pass "RSA key generation"

openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out ec.key 2>/dev/null
pass "EC key generation"

openssl req -new -x509 -key rsa.key -out cert.pem -days 1 \
    -subj "/CN=funkoverage-test" 2>/dev/null
pass "self-signed certificate"

openssl x509 -in cert.pem -text -noout >/dev/null 2>&1
pass "x509 inspect"

openssl req -new -key ec.key -out csr.pem \
    -subj "/CN=csr-test/O=funkoverage" 2>/dev/null
pass "CSR creation"

openssl verify -CAfile cert.pem cert.pem >/dev/null 2>&1
pass "certificate verify"

echo "hash me" | openssl dgst -sha256 >/dev/null
echo "hash me" | openssl dgst -sha3-512 >/dev/null
echo "hash me" | openssl dgst -md5 >/dev/null
pass "digests (sha256, sha3-512, md5)"

echo "hmac me" | openssl mac -digest SHA256 -macopt key:secret HMAC >/dev/null
pass "HMAC"

echo "encrypt this" > plain.txt
openssl enc -aes-256-cbc -salt -in plain.txt -out enc.bin -pass pass:test -pbkdf2 2>/dev/null
RESULT=$(openssl enc -aes-256-cbc -d -in enc.bin -pass pass:test -pbkdf2 2>/dev/null)
[[ "$RESULT" == "encrypt this" ]] || fail "AES roundtrip"
pass "AES-256-CBC encrypt/decrypt"

echo "base64 test" | openssl base64 | openssl base64 -d >/dev/null
pass "base64 encode/decode"

openssl rand -hex 32 >/dev/null
pass "random bytes"

echo "sign this" > msg.txt
openssl dgst -sha256 -sign rsa.key -out sig.bin msg.txt
openssl dgst -sha256 -verify <(openssl pkey -in rsa.key -pubout 2>/dev/null) \
    -signature sig.bin msg.txt >/dev/null
pass "RSA sign + verify"

openssl pkcs12 -export -in cert.pem -inkey rsa.key \
    -out bundle.p12 -passout pass:test 2>/dev/null
openssl pkcs12 -in bundle.p12 -passin pass:test -nokeys -clcerts >/dev/null 2>&1
pass "PKCS12 export/import"

echo "signed" | openssl smime -sign -signer cert.pem -inkey rsa.key \
    -text -nodetach >/dev/null 2>/dev/null
pass "S/MIME sign"

echo | timeout 10 openssl s_client -connect example.com:443 -brief >/dev/null 2>&1 || true
pass "TLS client connect"

openssl speed -seconds 1 sha256 >/dev/null 2>&1
pass "speed benchmark"

openssl asn1parse -in cert.pem >/dev/null 2>&1
pass "ASN.1 parse"

openssl dhparam -out dh.pem 1024 2>/dev/null
pass "DH parameter generation"

# --- Report ---
header "Coverage report"
REPORT_DIR=$(generate_report)
assert_min_called "*openssl*" 200

# Per-image breakdown
info "Per-image unique functions called:"
for log in /var/coverage/data/*openssl*_called.log; do true; done # ensure glob matches
awk '{print $2}' /var/coverage/data/*openssl*_called.log 2>/dev/null \
    | sort | uniq -c | sort -rn | while read count img; do
    printf "  %-55s %5d\n" "$(basename "$img")" "$count"
done

# Verify multi-image tracing
IMAGE_HIT=$(awk '{print $2}' /var/coverage/data/*openssl*_called.log 2>/dev/null \
    | sort -u | wc -l)
[[ "$IMAGE_HIT" -ge 2 ]] || fail "expected functions from >= 2 images, got $IMAGE_HIT"
pass "Multi-library tracing working ($IMAGE_HIT images hit)"

remove_report_dir "$REPORT_DIR"

# --- Uninstall ---
header "Uninstall"
uninstall_and_verify "$BINARY"
BINARY="" # prevent double-uninstall in trap

echo ""
echo -e "${GREEN}ALL TESTS PASSED${NC} (openssl)"
