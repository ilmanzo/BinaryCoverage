#!/bin/bash
set -euo pipefail

if ! command -v go &>/dev/null; then
    echo "Error: 'go' not found. Install Go ≥1.26 first." >&2
    exit 1
fi

# `go generate` (BPF compilation) is only needed when cmd/shim_binary/bpf/
# changes. The repo ships pre-generated bindings (tracer_x86_bpfel.{go,o}),
# so a normal build does NOT require clang/llvm/libbpf-devel/bpftool.
if [[ "${REGEN_BPF:-0}" == "1" ]]; then
    for tool in clang llvm-strip bpftool; do
        if ! command -v "$tool" &>/dev/null; then
            echo "Error: REGEN_BPF=1 needs '$tool' (install clang, llvm21, bpftool)." >&2
            exit 1
        fi
    done
    echo "Regenerating BPF bindings..."
    bpftool btf dump file /sys/kernel/btf/vmlinux format c 2>/dev/null \
        > cmd/shim_binary/bpf/vmlinux.h
    go generate ./cmd/shim_binary/
fi

echo "Building funkoverage CLI..."
go build -buildvcs=false -ldflags="-s -w" -o funkoverage ./cmd/

echo "Building funkoverage-shim..."
go build -buildvcs=false -ldflags="-s -w" -o funkoverage-shim ./cmd/shim_binary/

echo ""
echo "Build complete:"
echo "  funkoverage        - main CLI"
echo "  funkoverage-shim   - eBPF shim (place alongside funkoverage or in /usr/lib64/coverage-tools)"
echo ""
echo "Install requires root (writes to /usr/bin and runs setcap)."
echo "Runtime requires kernel ≥6.6 with CONFIG_DEBUG_INFO_BTF=y."
