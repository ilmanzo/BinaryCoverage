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

echo "--- Building Container Image (Leap 16.0) ---"
sudo $RUNTIME build \
    -t leap16-e2e \
    --build-arg BASE_IMAGE=registry.opensuse.org/opensuse/leap:16.0 \
    --build-arg DISTRO_CODENAME=leap16 \
    -f "$SCRIPT_DIR/Containerfile" "$WORKSPACE_DIR"

echo "--- Running Tests inside Container ---"
sudo $RUNTIME run --rm --privileged --pid=host \
    -v "$WORKSPACE_DIR:/host_workspace:ro" \
    -v /sys/kernel/btf:/sys/kernel/btf:ro \
    -v /sys/kernel/tracing:/sys/kernel/tracing \
    -v /sys/kernel/debug:/sys/kernel/debug \
    leap16-e2e /host_workspace/tests/e2e/run_all_container_tests.sh
