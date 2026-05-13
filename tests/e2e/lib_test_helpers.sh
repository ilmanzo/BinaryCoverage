#!/bin/bash
# Shared helpers for funkoverage e2e system binary tests.
# Source this file from each test script.

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BOLD='\033[1m'; NC='\033[0m'

pass() { echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { echo -e "  ${RED}FAIL${NC}: $1"; exit 1; }
info() { echo -e "${YELLOW}==>${NC} $1"; }
header() { echo -e "\n${BOLD}=== $1 ===${NC}"; }

# Ensure we're root
require_root() {
    [[ $(id -u) -eq 0 ]] || fail "must run as root"
}

# Check basic funkoverage prerequisites
require_funkoverage() {
    command -v funkoverage >/dev/null 2>&1 || fail "funkoverage not in PATH"
    [[ -f /sys/kernel/btf/vmlinux ]] || fail "kernel BTF not available (CONFIG_DEBUG_INFO_BTF=y required)"
}

# Install packages via zypper if not already present.
# Usage: ensure_packages pkg1 pkg2 ...
ensure_packages() {
    local to_install=()
    for pkg in "$@"; do
        if ! rpm -q "$pkg" >/dev/null 2>&1; then
            to_install+=("$pkg")
        fi
    done
    if [[ ${#to_install[@]} -gt 0 ]]; then
        info "Installing: ${to_install[*]}"
        zypper -n install -y "${to_install[@]}" 2>&1 | tail -5
    fi
}

# Verify a binary has debug symbols (funkoverage can enumerate it).
# Usage: require_debug_symbols /path/to/binary
require_debug_symbols() {
    local binary="$1"
    funkoverage enumerate --no-libs "$binary" >/dev/null 2>&1 \
        || fail "no debug symbols for $binary"
}

# Clean all coverage data.
clean_coverage_data() {
    rm -rf /var/coverage/data/*
}

# Install shim for a binary. Accepts optional extra args before the binary path.
# Usage: install_shim [--no-libs] /path/to/binary
install_shim() {
    funkoverage setup
    funkoverage install "$@"
}

# Uninstall shim and verify the original is restored.
# Usage: uninstall_and_verify /path/to/binary
uninstall_and_verify() {
    local binary="$1"
    local basename
    basename=$(basename "$binary")

    funkoverage uninstall "$binary"
    file "$binary" | grep -q "dynamically linked\|pie executable" \
        || fail "binary not restored to original"
    [[ ! -f "/var/coverage/bin/$basename" ]] \
        || fail "safe binary not cleaned up"
    pass "Shim uninstalled, original restored"
}

# Generate a text report and print it. Returns the report dir path via stdout.
# Usage: REPORT_DIR=$(generate_report)
generate_report() {
    local dir
    dir=$(mktemp -d /tmp/funkoverage_report_XXXX)
    funkoverage report /var/coverage/data "$dir" --formats txt
    echo "$dir"
}

# Safe removal of report directory (handles large dirs).
remove_report_dir() {
    [[ -n "${1:-}" && -d "$1" ]] && find "$1" -delete 2>/dev/null || true
}

# Count unique called functions matching a pattern in the called logs.
# Usage: count_called "*bzip2*"
count_called() {
    local pattern="$1"
    cat /var/coverage/data/${pattern}_called.log 2>/dev/null | sort -u | wc -l
}

# Count total known functions from the functions log.
# Usage: count_total "*bzip2*"
count_total() {
    local pattern="$1"
    grep -c "^FUNC " /var/coverage/data/${pattern}_functions.log 2>/dev/null || echo 0
}

# Assert a minimum number of unique functions were called.
# Usage: assert_min_called "*bzip2*" 10
assert_min_called() {
    local pattern="$1"
    local min="$2"
    local called
    called=$(count_called "$pattern")
    local total
    total=$(count_total "$pattern")
    info "Coverage: $called / $total functions called"
    [[ "$called" -ge "$min" ]] || fail "expected >= $min unique functions called, got $called"
    pass "Coverage threshold met ($called functions)"
}
