#!/bin/bash
set -euo pipefail

echo "=== Running Leap 16.0 Container E2E Test ==="

# Define the container runtime
RUNTIME="podman"
if ! command -v podman >/dev/null 2>&1; then
    RUNTIME="docker"
fi

# We use a temporary directory inside the container to avoid polluting the host workspace
# but we map the current directory to copy from it.
sudo $RUNTIME run --rm --privileged --pid=host \
    -v "$PWD:/host_workspace:ro" \
    -v /sys/kernel/btf:/sys/kernel/btf:ro \
    -v /sys/kernel/tracing:/sys/kernel/tracing \
    -v /sys/kernel/debug:/sys/kernel/debug \
    registry.opensuse.org/opensuse/leap:16.0 bash -c '
set -euo pipefail

echo "--- Setting up repos and installing dependencies ---"
zypper modifyrepo -e openSUSE:repo-oss-debug
zypper ref
zypper -n install which file go1.26 elfutils make gcc-c++ libcap-progs \
    bzip2 bzip2-debuginfo \
    gzip gzip-debuginfo \
    gmp-devel libgmp10-debuginfo \
    cpupower cpupower-debuginfo \
    squid squid-debuginfo \
    curl

echo "--- Copying workspace ---"
cp -r /host_workspace /workspace
cd /workspace

echo "--- Building funkoverage ---"
./build.sh

echo "--- Adding funkoverage to PATH ---"
export PATH="/workspace:$PATH"

echo "--- Running test_bzip2.sh ---"
bash tests/e2e/test_bzip2.sh

echo "--- Running test_gzip.sh ---"
bash tests/e2e/test_gzip.sh

echo "--- Running test_gmp.sh ---"
bash tests/e2e/test_gmp.sh

echo "--- Running test_cpupower.sh ---"
bash tests/e2e/test_cpupower.sh

echo "--- Running test_openssl.sh ---"
bash tests/e2e/test_openssl.sh

echo "--- Running test_squid.sh ---"
bash tests/e2e/test_squid.sh

echo "--- Success ---"
'
