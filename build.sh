#!/bin/bash
set -euo pipefail

for tool in bpftrace go; do
    if ! command -v "$tool" &>/dev/null; then
        echo "Error: '$tool' not found. Please install it first." >&2
        exit 1
    fi
done

echo "Building funkoverage CLI..."
go build -ldflags="-s -w" -o funkoverage ./cmd/

echo "Building funkoverage-shim..."
go build -ldflags="-s -w" -o funkoverage-shim ./cmd/shim_binary/

echo ""
echo "Build complete:"
echo "  funkoverage        - main CLI"
echo "  funkoverage-shim   - shim binary (place alongside funkoverage or in /usr/lib64/coverage-tools)"
echo ""
echo "Note: bpftrace requires root or CAP_DAC_READ_SEARCH."
echo "  Configure passwordless sudo for bpftrace, or run with sudo."
