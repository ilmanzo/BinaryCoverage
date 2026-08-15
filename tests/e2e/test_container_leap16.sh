#!/bin/bash
set -euo pipefail

echo "=== Running Leap 16.0 Container E2E Test ==="

# Define the container runtime
RUNTIME="podman"
if ! command -v podman >/dev/null 2>&1; then
    RUNTIME="docker"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
WORKSPACE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

echo "--- Building Container Image (caching packages) ---"
sudo $RUNTIME build -t leap16-e2e -f "$SCRIPT_DIR/Containerfile" "$WORKSPACE_DIR"

echo "--- Running Tests inside Container ---"
sudo $RUNTIME run --rm --privileged --pid=host \
    -v "$WORKSPACE_DIR:/host_workspace:ro" \
    -v /sys/kernel/btf:/sys/kernel/btf:ro \
    -v /sys/kernel/tracing:/sys/kernel/tracing \
    -v /sys/kernel/debug:/sys/kernel/debug \
    leap16-e2e bash -c '
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

echo "--- All Leap 16.0 Container E2E Tests Succeeded ---"
'
