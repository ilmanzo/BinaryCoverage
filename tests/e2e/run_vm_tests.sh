#!/bin/bash
# Run funkoverage E2E tests in a dedicated virtual machine via SSH.
#
# Supports running all tests or a specific test against an openSUSE Tumbleweed/Leap VM.
# Auto-detects local libvirt domains (e.g. tw_bcov) or connects to any remote host.
#
# Usage:
#   ./tests/e2e/run_vm_tests.sh [OPTIONS] [TEST_NAME ...]
#
# Examples:
#   ./tests/e2e/run_vm_tests.sh                           # Run all E2E tests
#   ./tests/e2e/run_vm_tests.sh test_sigchld_reaper.sh   # Run single test
#   ./tests/e2e/run_vm_tests.sh sigchld_reaper           # Short name also works
#   ./tests/e2e/run_vm_tests.sh --start                  # Boot tw_bcov if shut off
#   ./tests/e2e/run_vm_tests.sh --host 192.168.122.238   # Custom VM IP
#   ./tests/e2e/run_vm_tests.sh --list                   # List available tests

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKSPACE_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Styling
RED=$'\033[0;31m'
GREEN=$'\033[0;32m'
YELLOW=$'\033[1;33m'
BLUE=$'\033[0;34m'
CYAN=$'\033[0;36m'
BOLD=$'\033[1m'
NC=$'\033[0m'

pass() { echo -e "  ${GREEN}PASS${NC}: $1"; }
fail() { echo -e "  ${RED}FAIL${NC}: $1"; }
skip() { echo -e "  ${YELLOW}SKIP${NC}: $1"; }
info() { echo -e "${CYAN}==>${NC} $1"; }
warn() { echo -e "${YELLOW}WARNING:${NC} $1"; }
header() { echo -e "\n${BOLD}=== $1 ===${NC}"; }

# Complete list of available E2E tests in execution order
ALL_TESTS=(
    test_bzip2.sh
    test_gzip.sh
    test_gmp.sh
    test_cpupower.sh
    test_glibc_hwcaps_resolution.sh
    test_openssl.sh
    test_squid.sh
    test_nginx_dlopen.sh
    test_nss_dlopen.sh
    test_pam_dlopen.sh
    test_rust_ripgrep.sh
    test_signal_and_notify_relay.sh
    test_duplicate_library_symlink.sh
    test_shim_fail_open.sh
    test_bystander_traps.sh
    test_sigchld_reaper.sh
)

# Configuration defaults
VM_DOMAIN="${VM_DOMAIN:-tw_bcov}"
VM_HOST="${VM_HOST:-}"
VM_USER="${VM_USER:-}"
VM_PORT="${VM_PORT:-22}"
VM_IDENTITY="${VM_IDENTITY:-}"
REMOTE_DIR="${VM_REMOTE_DIR:-}"
AUTO_START=0
DO_SYNC=1
DO_BUILD=1
CLEAN_DATA=0
DRY_RUN=0
SELECTED_TESTS=()

usage() {
    cat <<EOF
${BOLD}Usage:${NC} $(basename "$0") [OPTIONS] [TEST_NAME ...]

Run all E2E tests or a specific test in a separate virtual machine via SSH.

${BOLD}Options:${NC}
  -H, --host <host>          Target VM hostname or IP address
  -t, --target <[user@]host> Target user and host
  -u, --user <user>          SSH username (default: root or current user)
  -p, --port <port>          SSH port (default: 22)
  -i, --identity <path>      SSH private key identity file
  -d, --domain <domain>      Libvirt domain name for auto-discovery (default: tw_bcov)
  -s, --start                Auto-start libvirt domain if currently shut off
  -r, --remote-dir <path>    Remote directory for repo sync (default: ~/binarycoverage)
      --no-sync              Skip syncing workspace with rsync
      --no-build             Skip rebuilding funkoverage on the VM
  -c, --clean                Clean /var/coverage/data on VM before testing
  -n, --dry-run              Print target and test plan without executing
  -l, --list                 List all available E2E tests and exit
  -h, --help                 Show this help message and exit

${BOLD}Environment Variables:${NC}
  VM_HOST, VM_USER, VM_PORT, VM_IDENTITY, VM_DOMAIN, VM_REMOTE_DIR

${BOLD}Examples:${NC}
  $(basename "$0")                                     # Run all tests on auto-detected VM
  $(basename "$0") test_sigchld_reaper.sh             # Run only the sigchld reaper test
  $(basename "$0") reaper                             # Fuzzy name matching supported
  $(basename "$0") --start test_sigchld_reaper.sh     # Boot tw_bcov and run test
  $(basename "$0") --host 192.168.122.238 --user root # Run on explicit target
EOF
    exit 0
}

list_tests() {
    echo -e "${BOLD}Available E2E Tests (${#ALL_TESTS[@]}):${NC}"
    for t in "${ALL_TESTS[@]}"; do
        echo "  - $t"
    done
    exit 0
}

resolve_test_name() {
    local input="$1"
    local base
    base="$(basename "$input")"
    [[ "$base" != *.sh ]] && base="${base}.sh"
    [[ "$base" != test_* ]] && base="test_${base}"

    for t in "${ALL_TESTS[@]}"; do
        if [[ "$t" == "$base" ]]; then
            echo "$t"
            return 0
        fi
    done

    # Fallback to substring match
    local clean_input
    clean_input="$(echo "$input" | sed -E 's/(^test_|\.sh$)//g')"
    for t in "${ALL_TESTS[@]}"; do
        if [[ "$t" == *"$clean_input"* ]]; then
            echo "$t"
            return 0
        fi
    done

    echo ""
    return 1
}

# Parse CLI arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        -h|--help)
            usage
            ;;
        -l|--list)
            list_tests
            ;;
        -H|--host)
            VM_HOST="$2"
            shift 2
            ;;
        -t|--target)
            if [[ "$2" == *"@"* ]]; then
                VM_USER="${2%%@*}"
                VM_HOST="${2#*@}"
            else
                VM_HOST="$2"
            fi
            shift 2
            ;;
        -u|--user)
            VM_USER="$2"
            shift 2
            ;;
        -p|--port)
            VM_PORT="$2"
            shift 2
            ;;
        -i|--identity)
            VM_IDENTITY="$2"
            shift 2
            ;;
        -d|--domain)
            VM_DOMAIN="$2"
            shift 2
            ;;
        -s|--start)
            AUTO_START=1
            shift
            ;;
        -r|--remote-dir)
            REMOTE_DIR="$2"
            shift 2
            ;;
        --no-sync)
            DO_SYNC=0
            shift
            ;;
        --no-build)
            DO_BUILD=0
            shift
            ;;
        -c|--clean)
            CLEAN_DATA=1
            shift
            ;;
        -n|--dry-run)
            DRY_RUN=1
            shift
            ;;
        -*)
            echo -e "${RED}Unknown option:${NC} $1" >&2
            usage
            ;;
        *)
            resolved="$(resolve_test_name "$1")"
            if [[ -z "$resolved" ]]; then
                echo -e "${RED}Error:${NC} Test '$1' not found." >&2
                echo "Use --list to see available tests." >&2
                exit 1
            fi
            SELECTED_TESTS+=("$resolved")
            shift
            ;;
    esac
done

# If no specific tests were requested, run all of them
if [[ ${#SELECTED_TESTS[@]} -eq 0 ]]; then
    SELECTED_TESTS=("${ALL_TESTS[@]}")
fi

# Auto-detect VM host via libvirt if not explicitly set
if [[ -z "$VM_HOST" ]]; then
    info "No --host provided; attempting auto-discovery for libvirt domain '$VM_DOMAIN'..."
    if command -v virsh >/dev/null 2>&1; then
        domain_state="$(virsh -c qemu:///system domstate "$VM_DOMAIN" 2>/dev/null || true)"
        if [[ -n "$domain_state" ]]; then
            info "Found libvirt domain '$VM_DOMAIN' (state: $domain_state)"
            if [[ "$domain_state" == *"shut off"* ]]; then
                if [[ $AUTO_START -eq 1 ]]; then
                    info "Starting libvirt domain '$VM_DOMAIN'..."
                    virsh -c qemu:///system start "$VM_DOMAIN" >/dev/null
                    info "Waiting for domain to boot and acquire an IP address..."
                    for _ in $(seq 30); do
                        sleep 2
                        ip="$(virsh -c qemu:///system domifaddr "$VM_DOMAIN" 2>/dev/null | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
                        [[ -n "$ip" ]] && { VM_HOST="$ip"; break; }
                    done
                else
                    # Check if there is a known DHCP lease or warn
                    lease_ip="$(virsh -c qemu:///system net-dhcp-leases default 2>/dev/null | awk -v dom="$VM_DOMAIN" '$0 ~ dom || /twbincov/ {split($5, a, "/"); print a[1]; exit}')"
                    if [[ $DRY_RUN -eq 1 ]]; then
                        VM_HOST="${lease_ip:-192.168.122.238}"
                        info "Dry run: using domain '$VM_DOMAIN' lease IP $VM_HOST"
                    else
                        warn "Domain '$VM_DOMAIN' is shut off."
                        if [[ -n "$lease_ip" ]]; then
                            echo "  Last known DHCP lease IP: $lease_ip"
                        fi
                        echo "  To start it automatically, pass --start: $(basename "$0") --start"
                        echo "  Or specify an active IP: $(basename "$0") --host <IP>"
                        exit 1
                    fi
                fi
            else
                # Domain is already running; query IP
                ip="$(virsh -c qemu:///system domifaddr "$VM_DOMAIN" 2>/dev/null | awk '/ipv4/ {split($4, a, "/"); print a[1]; exit}')"
                if [[ -z "$ip" ]]; then
                    # Try matching MAC address from XML against DHCP leases
                    mac="$(virsh -c qemu:///system dumpxml "$VM_DOMAIN" 2>/dev/null | grep -oE "mac address='[^']+'" | head -n1 | cut -d"'" -f2 || true)"
                    if [[ -n "$mac" ]]; then
                        ip="$(virsh -c qemu:///system net-dhcp-leases default 2>/dev/null | awk -v mac="$mac" '$3 == mac {split($5, a, "/"); print a[1]; exit}')"
                    fi
                fi
                if [[ -n "$ip" ]]; then
                    VM_HOST="$ip"
                fi
            fi
        fi
    fi

    # Fallback default if virsh discovery didn't find an IP
    if [[ -z "$VM_HOST" ]]; then
        # Check if default 192.168.122.238 lease responds
        if ping -c 1 -W 1 192.168.122.238 >/dev/null 2>&1; then
            VM_HOST="192.168.122.238"
            info "Discovered reachable VM at default IP $VM_HOST"
        else
            echo -e "${RED}Error:${NC} Could not auto-discover VM host." >&2
            echo "Please supply --host <ip/hostname> or ensure domain '$VM_DOMAIN' is running." >&2
            exit 1
        fi
    fi
fi

info "Target VM host: $VM_HOST"

# Build SSH options
SSH_OPTS=(
    -p "$VM_PORT"
    -o BatchMode=yes
    -o ConnectTimeout=8
    -o StrictHostKeyChecking=accept-new
)
[[ -n "$VM_IDENTITY" ]] && SSH_OPTS+=(-i "$VM_IDENTITY")

# Auto-detect SSH user if not provided
if [[ -z "$VM_USER" ]]; then
    if [[ $DRY_RUN -eq 1 ]]; then
        VM_USER="root"
    elif ssh "${SSH_OPTS[@]}" "root@$VM_HOST" "true" >/dev/null 2>&1; then
        VM_USER="root"
    else
        VM_USER="${USER:-andrea}"
    fi
fi

SSH_TARGET="$VM_USER@$VM_HOST"
info "SSH Target: $SSH_TARGET"

# Set remote directory default
if [[ -z "$REMOTE_DIR" ]]; then
    if [[ "$VM_USER" == "root" ]]; then
        REMOTE_DIR="/root/binarycoverage"
    else
        REMOTE_DIR="/home/$VM_USER/binarycoverage"
    fi
fi

# In dry-run mode, display plan and exit
if [[ $DRY_RUN -eq 1 ]]; then
    header "Dry Run - Execution Plan"
    echo "  VM Host:       $VM_HOST"
    echo "  SSH Target:    $SSH_TARGET (port $VM_PORT)"
    echo "  Remote Dir:    $REMOTE_DIR"
    echo "  Sync Repo:     $([[ $DO_SYNC -eq 1 ]] && echo 'Yes' || echo 'No')"
    echo "  Build Target:  $([[ $DO_BUILD -eq 1 ]] && echo 'Yes' || echo 'No')"
    echo "  Clean Data:    $([[ $CLEAN_DATA -eq 1 ]] && echo 'Yes' || echo 'No')"
    echo "  Selected Tests (${#SELECTED_TESTS[@]}):"
    for t in "${SELECTED_TESTS[@]}"; do
        echo "    - $t"
    done
    exit 0
fi

# SSH execution helper
vm_ssh() {
    ssh "${SSH_OPTS[@]}" "$SSH_TARGET" "$@"
}

# Check connectivity
header "Verifying SSH Connection"
if ! vm_ssh 'echo "SSH connection verified on $(hostname)"'; then
    echo -e "${RED}Error:${NC} Failed to connect to $SSH_TARGET via SSH." >&2
    exit 1
fi

# Synchronize workspace
if [[ $DO_SYNC -eq 1 ]]; then
    header "Syncing Workspace to VM"
    info "Local:  $WORKSPACE_DIR"
    info "Remote: $SSH_TARGET:$REMOTE_DIR"

    vm_ssh "mkdir -p '$REMOTE_DIR'"

    rsync -avz --delete \
        --exclude='.git/' \
        --exclude='.tokensave/' \
        --exclude='funkoverage' \
        --exclude='funkoverage-shim' \
        --exclude='coverage.out' \
        --exclude='*.o' \
        --exclude='tests/bench/' \
        -e "ssh ${SSH_OPTS[*]}" \
        "$WORKSPACE_DIR/" "$SSH_TARGET:$REMOTE_DIR/"
    pass "Workspace synchronized"
else
    info "Skipping workspace sync (--no-sync specified)"
fi

# Build project on VM
if [[ $DO_BUILD -eq 1 ]]; then
    header "Building funkoverage on VM"
    vm_ssh "cd '$REMOTE_DIR' && ./build.sh"
    pass "Build completed successfully"
else
    info "Skipping build (--no-build specified)"
fi

# Optional clean of coverage data
if [[ $CLEAN_DATA -eq 1 ]]; then
    header "Cleaning Coverage Data on VM"
    vm_ssh "sudo rm -rf /var/coverage/data/* /var/coverage/bin/* 2>/dev/null || true"
    pass "Cleaned /var/coverage"
fi

# Run test(s)
header "Executing E2E Tests on VM (${#SELECTED_TESTS[@]} selected)"

RESULTS=()
PASSED_COUNT=0
FAILED_COUNT=0
SKIPPED_COUNT=0
T_TOTAL_START=$SECONDS

for test_script in "${SELECTED_TESTS[@]}"; do
    echo -e "\n${BOLD}>>> Running $test_script [VM: $VM_HOST] <<<${NC}"
    t_start=$SECONDS

    set +e
    vm_ssh "cd '$REMOTE_DIR' && sudo -E env PATH=\"$REMOTE_DIR:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin:\$PATH\" bash 'tests/e2e/$test_script'"
    rc=$?
    set -e

    duration=$((SECONDS - t_start))

    if [[ $rc -eq 0 ]]; then
        pass "$test_script (${duration}s)"
        RESULTS+=("PASS|$test_script|${duration}s")
        PASSED_COUNT=$((PASSED_COUNT + 1))
    else
        fail "$test_script (exit code $rc, ${duration}s)"
        RESULTS+=("FAIL|$test_script|${duration}s|exit $rc")
        FAILED_COUNT=$((FAILED_COUNT + 1))
    fi
done

total_duration=$((SECONDS - T_TOTAL_START))

# Summary
header "VM E2E Test Execution Summary"
echo "Target VM: $SSH_TARGET ($REMOTE_DIR)"
echo "----------------------------------------------------"
for res in "${RESULTS[@]}"; do
    IFS="|" read -r status name dur extra <<< "$res"
    if [[ "$status" == "PASS" ]]; then
        printf "  ${GREEN}%-4s${NC}  %-36s %s\n" "$status" "$name" "$dur"
    else
        printf "  ${RED}%-4s${NC}  %-36s %s (%s)\n" "$status" "$name" "$dur" "$extra"
    fi
done
echo "----------------------------------------------------"
printf "${BOLD}Total: %d | Passed: ${GREEN}%d${NC}${BOLD} | Failed: ${RED}%d${NC}${BOLD} | Total Time: %ds${NC}\n" \
    "${#SELECTED_TESTS[@]}" "$PASSED_COUNT" "$FAILED_COUNT" "$total_duration"

if [[ $FAILED_COUNT -gt 0 ]]; then
    exit 1
fi
exit 0
