#!/bin/bash
set -euo pipefail
set -x 
PIN_ARCHIVE="pin-external-4.1-99687-gd9b8f822c-gcc-linux.tar.gz"
PIN_URL="https://software.intel.com/sites/landingpage/pintool/downloads/$PIN_ARCHIVE"

pushd .. > /dev/null

if [[ ! -f "$PIN_ARCHIVE" ]]; then
    echo "Downloading Intel Pin..."
    wget $PIN_URL
    tar xzf $PIN_ARCHIVE
fi

PIN_DIR=$(basename $PIN_ARCHIVE .tar.gz)

if [[ ! -d "$PIN_DIR" ]]; then
    echo "Error: Failed to download or extract Intel Pin archive." >&2
    exit 1
fi

export PIN_ROOT=$(realpath `ls -d $PIN_DIR`)
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


