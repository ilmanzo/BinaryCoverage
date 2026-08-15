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

echo "--- Running test_openssl.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_openssl.sh

echo "--- Running test_squid.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_squid.sh

echo "--- Running test_nginx_dlopen.sh [INSIDE CONTAINER] ---"
bash tests/e2e/test_nginx_dlopen.sh

echo "===================================================="
echo "=== ALL E2E CONTAINER TESTS SUCCEEDED ==="
echo "===================================================="
