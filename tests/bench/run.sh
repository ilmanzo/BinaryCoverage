#!/bin/bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)

if [ "$(id -u)" -ne 0 ]; then
    echo "Error: tests/bench/run.sh must be run as root" >&2
    exit 1
fi

if [ ! -f "$ROOT/funkoverage" ] || [ ! -f "$ROOT/funkoverage-shim" ]; then
    echo "run ./build.sh first" >&2
    exit 1
fi

for cmd in gcc objdump awk; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "Error: required tool '$cmd' not found" >&2
        exit 1
    fi
done

DO_PERF=0
for arg in "$@"; do
    case "$arg" in
        --perf)
            DO_PERF=1
            ;;
        *)
            echo "Error: unknown argument '$arg'" >&2
            exit 1
            ;;
    esac
done

if [ "$DO_PERF" -eq 1 ]; then
    if ! command -v perf >/dev/null 2>&1; then
        echo "Error: --perf specified but perf tool not found" >&2
        exit 1
    fi
fi

FUNCS=${FUNCS:-"100 1000 10000"}
CALLS=${CALLS:-2000000}
RUNS=${RUNS:-5}
WORK=${WORK:-"$ROOT/tests/bench/work"}

mkdir -p "$WORK"

VARIANTS=(
    "o0-nofcp:-O0 -fcf-protection=none"
    "o0-cet:-O0 -fcf-protection=full"
    "o1-nofcp:-O1 -fcf-protection=none"
    "o1-cet:-O1 -fcf-protection=full"
    "o0-call:-O0 -fcf-protection=none -pg -mfentry"
    "o1-call:-O1 -fcf-protection=none -pg -mfentry"
    "o1-cet-call:-O1 -fcf-protection=full -pg -mfentry"
)

now_ns() {
    date +%s%N
}

median() {
    # Sort numbers numerically and pick the middle element (lower middle if even count)
    local sorted=()
    while IFS= read -r line; do
        sorted+=("$line")
    done < <(printf '%s\n' "$@" | sort -n)
    local count=${#sorted[@]}
    if [ "$count" -eq 0 ]; then
        echo 0
        return
    fi
    local mid=$(( (count - 1) / 2 ))
    echo "${sorted[$mid]}"
}

wait_log_closed() {
    local logdir="$1"
    local timeout=30
    local start
    start=$(date +%s)
    local logfile=""

    while [ -z "$logfile" ]; do
        logfile=$(find "$logdir" -maxdepth 1 -name "*_called.log" -print -quit 2>/dev/null || true)
        if [ -n "$logfile" ]; then
            break
        fi
        if [ $(( $(date +%s) - start )) -ge "$timeout" ]; then
            echo "Timeout (30s) waiting for _called.log to appear in $logdir" >&2
            exit 1
        fi
        sleep 0.02
    done

    while true; do
        local busy=0
        if command -v fuser >/dev/null 2>&1; then
            if fuser "$logfile" >/dev/null 2>&1; then
                busy=1
            fi
        else
            local real
            real=$(readlink -f "$logfile")
            if find /proc/[0-9]*/fd -lname "$real" 2>/dev/null | grep -q .; then
                busy=1
            fi
        fi

        if [ "$busy" -eq 0 ]; then
            break
        fi

        if [ $(( $(date +%s) - start )) -ge "$timeout" ]; then
            echo "Timeout (30s) waiting for $logfile to be closed" >&2
            exit 1
        fi
        sleep 0.02
    done
}

verify_called_log() {
    local logdir="$1"
    local expected="$2"
    local latest_log
    latest_log=$(find "$logdir" -maxdepth 1 -name "*_called.log" -printf '%T@ %p\n' 2>/dev/null | sort -n | tail -1 | cut -d' ' -f2-)
    if [ -z "$latest_log" ] || [ ! -f "$latest_log" ]; then
        echo "Error: no _called.log found in $logdir" >&2
        exit 1
    fi
    local count
    count=$(awk '$1 == "CALLED" && $NF ~ /^fn_/ { funcs[$NF] = 1 } END { c = 0; for (k in funcs) c++; print c }' "$latest_log")
    if [ "$count" -ne "$expected" ]; then
        echo "Error: coverage verification failed in $latest_log: expected $expected distinct fn_ calls, got $count" >&2
        exit 1
    fi
}

CURRENT_INSTALLED_BENCH=""
CURRENT_D=""

cleanup() {
    if [ -n "$CURRENT_INSTALLED_BENCH" ] && [ -n "$CURRENT_D" ]; then
        echo "Cleaning up installed shim: $CURRENT_INSTALLED_BENCH" >&2
        env LOG_DIR="$CURRENT_D/log" SAFE_BIN_DIR="$CURRENT_D/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$ROOT/funkoverage" uninstall "$CURRENT_INSTALLED_BENCH" 2>/dev/null || true
    fi
}
trap cleanup EXIT

RESULTS=()

print_header() {
    printf "%-12s %6s %9s %10s %10s %12s %11s\n" "variant" "funcs" "calls" "native_ms" "shim_ms" "ns_per_call" "startup_ms"
}

format_row() {
    printf "%-12s %6d %9d %10.1f %10.1f %12.1f %11.1f\n" "$1" "$2" "$3" "$4" "$5" "$6" "$7"
}

print_header

for v_spec in "${VARIANTS[@]}"; do
    v="${v_spec%%:*}"
    v_flags="${v_spec#*:}"

    for N in $FUNCS; do
        d="$WORK/$v-$N"
        mkdir -p "$d/log" "$d/safe"
        export GMON_OUT_PREFIX="$d/gmon"

        # Generate funcs.h with a single awk call
        awk -v n="$N" 'BEGIN {
            print "#define NFUNCS " n
            for (i = 0; i < n; i++) {
                print "__attribute__((noinline)) int fn_" i "(int x) { return x * 31 + " i "; }"
            }
            printf "static int (*const fns[NFUNCS])(int) = {"
            for (i = 0; i < n; i++) {
                printf "%sfn_%d", (i > 0 ? ", " : " "), i
            }
            print " };"
        }' > "$d/funcs.h"

        # Compile native binary and print the first 2 instructions of fn_0
        # shellcheck disable=SC2086
        gcc -g -std=c11 $v_flags -I"$d" "$ROOT/tests/bench/bench.c" -o "$d/native"
        echo "Prologue for $v (NFUNCS=$N, fn_0):"
        objdump -d --disassemble=fn_0 "$d/native" | awk '/<fn_0>:/{print; n=2; next} n>0 && NF{print; n--}'

        # Copy to bench and install shim
        cp "$d/native" "$d/bench"
        CURRENT_INSTALLED_BENCH="$d/bench"
        CURRENT_D="$d"
        env LOG_DIR="$d/log" SAFE_BIN_DIR="$d/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$ROOT/funkoverage" install "$d/bench"

        loops=$(( CALLS / N ))
        if [ "$loops" -lt 1 ]; then
            loops=1
        fi
        real_calls=$(( loops * N ))

        # Timed runs for native binary
        native_times=()
        for ((r = 0; r < RUNS; r++)); do
            t0=$(now_ns)
            "$d/native" "$loops" >/dev/null
            t1=$(now_ns)
            native_times+=($(( t1 - t0 )))
        done

        # Timed runs for shim binary
        shim_times=()
        for ((r = 0; r < RUNS; r++)); do
            find "$d/log" -mindepth 1 -delete
            t0=$(now_ns)
            env LOG_DIR="$d/log" SAFE_BIN_DIR="$d/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$d/bench" "$loops" >/dev/null
            t1=$(now_ns)
            shim_times+=($(( t1 - t0 )))
            wait_log_closed "$d/log"
            verify_called_log "$d/log" "$N"
        done

        # Startup measurement (loops = 0)
        native_0_times=()
        for ((r = 0; r < RUNS; r++)); do
            t0=$(now_ns)
            "$d/native" 0 >/dev/null
            t1=$(now_ns)
            native_0_times+=($(( t1 - t0 )))
        done

        shim_0_times=()
        for ((r = 0; r < RUNS; r++)); do
            find "$d/log" -mindepth 1 -delete
            t0=$(now_ns)
            env LOG_DIR="$d/log" SAFE_BIN_DIR="$d/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$d/bench" 0 >/dev/null
            t1=$(now_ns)
            shim_0_times+=($(( t1 - t0 )))
            wait_log_closed "$d/log"
        done

        median_native=$(median "${native_times[@]}")
        median_shim=$(median "${shim_times[@]}")
        median_native_0=$(median "${native_0_times[@]}")
        median_shim_0=$(median "${shim_0_times[@]}")

        native_ms=$(awk -v n="$median_native" 'BEGIN { printf "%.1f", n / 1000000.0 }')
        shim_ms=$(awk -v s="$median_shim" 'BEGIN { printf "%.1f", s / 1000000.0 }')
        ns_per_call=$(awk -v s="$median_shim" -v n="$median_native" -v c="$real_calls" 'BEGIN { printf "%.1f", (s - n) / c }')
        startup_ms=$(awk -v s="$median_shim_0" -v n="$median_native_0" 'BEGIN { printf "%.1f", (s - n) / 1000000.0 }')

        row=$(format_row "$v" "$N" "$real_calls" "$native_ms" "$shim_ms" "$ns_per_call" "$startup_ms")
        RESULTS+=("$row")
        printf "%s\n" "$row"

        if [ "$DO_PERF" -eq 1 ]; then
            echo "Running perf on $v (NFUNCS=$N)..."
            find "$d/log" -mindepth 1 -delete
            perf record -a -g -o "$d/perf.data" -- env LOG_DIR="$d/log" SAFE_BIN_DIR="$d/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$d/bench" "$loops" >/dev/null
            wait_log_closed "$d/log"
            perf report --stdio --sort sym -i "$d/perf.data" | head -40
        fi

        # Uninstall shim
        env LOG_DIR="$d/log" SAFE_BIN_DIR="$d/safe" FUNKOVERAGE_SHIM="$ROOT/funkoverage-shim" "$ROOT/funkoverage" uninstall "$d/bench"
        CURRENT_INSTALLED_BENCH=""
        CURRENT_D=""
    done
done

echo
echo "=== Summary Results ==="
print_header
for row in "${RESULTS[@]}"; do
    printf "%s\n" "$row"
done
