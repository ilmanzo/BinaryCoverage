#!/bin/bash
set -euo pipefail

PIN_ARCHIVE="pin-external-4.0-99633-g5ca9893f2-gcc-linux"
PIN_URL="https://software.intel.com/sites/landingpage/pintool/downloads/${PIN_ARCHIVE}.tar.gz"

pushd .. > /dev/null

if [[ ! -d "$PIN_ARCHIVE" ]]; then
    echo "Downloading Intel Pin..."
    curl -fsSL "$PIN_URL" | tar -xzf -
fi

if [[ ! -d "$PIN_ARCHIVE" ]]; then
    echo "Error: Failed to download or extract Intel Pin archive." >&2
    exit 1
fi

PIN_ROOT=$(realpath `ls -d $PIN_ARCHIVE`)
export PIN_ROOT
popd > /dev/null
make
echo "export PIN_ROOT=\"$PIN_ROOT\"" > env
strip obj-intel64/FuncTracer.so

# Build Go program in cmd folder
if command -v go &>/dev/null; then
    echo "Building Go CLI in ./cmd..."
    pushd cmd > /dev/null
    go build -ldflags="-s -w" -o ../funkoverage .
    popd > /dev/null
    echo "Go CLI built as ./funkoverage"
else
    echo "Warning: Go not found, skipping Go CLI build."
fi


