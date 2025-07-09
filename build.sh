#!/bin/bash
set -euo pipefail

PIN_ARCHIVE="pin-external-3.31-98869-gfa6f126a8-gcc-linux"
PIN_URL="https://software.intel.com/sites/landingpage/pintool/downloads/${PIN_ARCHIVE}.tar.gz"
REQUIRED_GCC=14

CXX=""
GPP_VERSION=""

if command -v g++-14 &>/dev/null; then
    CXX=$(command -v g++-14)
else
    if command -v g++ &>/dev/null; then
        CXX=$(command -v g++)
        GPP_VERSION=$($CXX -dumpversion)
        GPP_MAJOR_VERSION=${GPP_VERSION%%.*}
        if [[ "$GPP_MAJOR_VERSION" -ne "$REQUIRED_GCC" ]]; then
            echo "Error: Found $CXX version $GPP_VERSION; required version is $REQUIRED_GCC.*"
            exit 1
        fi
    else
        echo "Error: g++ 14.* not found in PATH."
        exit 1
    fi
fi
echo "Using $CXX $GPP_VERSION"

pushd .. > /dev/null

if [[ ! -d "$PIN_ARCHIVE" ]]; then
    echo "Downloading Intel Pin..."
    curl -fsSL "$PIN_URL" | tar -xzf -
fi

if [[ ! -d "$PIN_ARCHIVE" ]]; then
    echo "Error: Failed to download or extract Intel Pin archive." >&2
    exit 1
fi

PIN_ROOT=$(realpath `ls -d pin*`)
export PIN_ROOT
popd > /dev/null
export CXX="$CXX -std=c++20"
make
echo "export PIN_ROOT=\"$PIN_ROOT\"" > env

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



# Optional: Build and run the example
# First you have to set PIN_ENV generated in `env`
#
# pushd example && make && popd
# "$PIN_ROOT/pin" -t ./obj-intel64/FuncTracer.so -- example/cov_sample 7 3
