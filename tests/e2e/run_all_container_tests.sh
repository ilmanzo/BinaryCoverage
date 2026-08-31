#!/bin/bash
set -euo pipefail

echo "===================================================="
echo "=== NOW RUNNING ENVIRONMENT INSIDE CONTAINER ==="
echo "===================================================="

echo "--- Copying workspace to avoid host file ownership pollution ---"
rm -rf /workspace
cp -r /host_workspace /workspace
cd /workspace

echo "--- Building funkoverage ---"
./build.sh

echo "--- Adding funkoverage to PATH ---"
export PATH="/workspace:$PATH"

echo "--- Running test_bzip2.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_bzip2.sh

echo "--- Running test_gzip.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_gzip.sh

echo "--- Running test_gmp.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_gmp.sh

echo "--- Running test_cpupower.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_cpupower.sh
echo "--- Running test_glibc_hwcaps_resolution.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_glibc_hwcaps_resolution.sh

echo "--- Running test_openssl.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_openssl.sh

echo "--- Running test_squid.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_squid.sh

echo "--- Running test_nginx_dlopen.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_nginx_dlopen.sh

echo "--- Running test_nss_dlopen.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_nss_dlopen.sh

echo "--- Running test_pam_dlopen.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_pam_dlopen.sh

echo "--- Running test_rust_ripgrep.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_rust_ripgrep.sh

echo "--- Running test_signal_and_notify_relay.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_signal_and_notify_relay.sh

echo "--- Running test_duplicate_library_symlink.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_duplicate_library_symlink.sh

# Self-skips in here: building its seccomp sandbox needs a running systemd.
# Listed anyway so the container runner stays a complete inventory of the
# suite, and so it starts covering this once a runner does have systemd.
echo "--- Running test_shim_fail_open.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_shim_fail_open.sh

echo "===================================================="
echo "=== ALL E2E CONTAINER TESTS SUCCEEDED ==="
echo "===================================================="
