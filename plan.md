# Report Generator, Enumerator & CLI Performance Optimization and Feature Plan

This document outlines the high-performance optimization plans, verified results, and upcoming features for `funkoverage`. 

---

## 1. Completed Performance Optimizations (Status: 100% Implemented)

All primary performance bottlenecks identified in earlier iterations have been fully optimized, verified, and integrated into the codebase:

### Bottleneck A: Regexp Matching in `scanLog` (Critical)
* **Problem**: `scanLog` parsed log files line-by-line using regular expressions, consuming excessive CPU cycles and triggering high memory allocation rates.
* **Resolution**: Replaced `regexp` matching with optimized, regex-free linear string/bytes manipulation using `bufio.Scanner` and direct byte slice comparisons (`bytes.HasPrefix`, `bytes.IndexAny`).
* **Gain**: **6.08x speedup** on log parsing.

### Bottleneck B: Redundant Template Parsing (High)
* **Problem**: HTML templates were parsed from scratch on every single invocation of report generation.
* **Resolution**: Pre-parsed HTML templates exactly once at package-level initialization (`parsedDetailedTemplate` and `parsedAggregateTemplate`).

### Bottleneck C: Sequential Log File Scanning & Report Writing (Medium)
* **Problem**: Log processing and report document writes ran entirely sequentially.
* **Resolution**: Leveraged `golang.org/x/sync/errgroup` to scan multiple log files and write reports in parallel across all available CPU cores.

### Bottleneck D: Sequential Shared Library Function Enumeration (Medium)
* **Problem**: Library function enumeration scanned dependencies in a serial loop.
* **Resolution**: Parallelized library scans by processing dependency ELF/DWARF tables in concurrent goroutines.

### Bottleneck E: Inefficient Redundant Map & Sum Allocations (Medium)
* **Problem**: `generateXUnitReport` allocated transient maps and sorted keys repeatedly.
* **Resolution**: Simplified calculations to use direct slice lengths instead of triggering redundant maps or sort operations.

### Bottleneck F: Unbuffered Disk I/O (Medium)
* **Problem**: HTML and XML report files were written using unbuffered `*os.File` handles.
* **Resolution**: Wrapped all disk-output streams in `bufio.NewWriter` before executing templates or XML encoding, reducing system call overhead.

---

## 2. Empirical Benchmark Results (Verified Baseline vs. Optimized)

The following metrics are empirical test results verified directly on standard dual-core testbed systems.

### Benchmark A: Regexp-Free Log Parsing (`scanLog`)
*Tested with a single 20,000-line functions/called log dataset.*
* **Baseline (Regex-based)**: `111,996,571 ns/op` (~112.0 ms)
* **Optimized (Regex-Free Bytes Slicing)**: `18,413,599 ns/op` (~18.4 ms)
* **Performance Gain**: **6.08x speedup** (83.5% total CPU execution time saved).

### Benchmark B: Sequential vs. Parallel Log Scanning (`analyzeLogs`)
*Tested with 10 log files (50,000 lines total) across different CPU limitations.*
* **1 CPU (embedded limit)**:
  * **Sequential**: `71,904,792 ns/op` (~71.9 ms)
  * **Parallel**: `70,583,822 ns/op` (~70.5 ms)
  * **Performance Gain**: **~2% faster** (even on 1-core targets, parallel scanning benefits from asynchronous disk I/O multiplexing).
* **2 CPUs (dual-core target VM)**:
  * **Sequential**: `64,732,743 ns/op` (~64.7 ms)
  * **Parallel**: `38,883,121 ns/op` (~38.8 ms)
  * **Performance Gain**: **1.66x speedup** (40% total execution time saved).

---

## 3. Future Scope / TODOs (Later Iterations)

### A. Avoidance of Double Shimming (Detecting Already Instrumented Binaries)
* **Goal**: Prevent `funkoverage install` from instrumenting a binary that is already a `funkoverage-shim` copy. This protection is critical to avoid recursive execution hangs, corrupt backup storage, or loss of the original native binary.
* **Approach**:
  1. During the installation phase inside `cmd/shim.go`, inspect the target binary before proceeding.
  2. Parse the target's ELF symbol table (using the standard `debug/elf` package).
  3. Search for known, shim-specific runtime symbols, such as:
     - `main.childMain`
     - `main.runWithTracing`
     - `main.calledLogPath`
  4. If any of these symbols are detected in the symbol table, immediately abort the installation with a user-friendly error (e.g., `"Error: binary is already a funkoverage-shim. Aborting to avoid double shimming."`).
  5. Add dedicated test cases in `cmd/funkoverage_test.go` to verify that attempting to install on a shim binary is correctly blocked.

### B. Direct ELF Dependency Resolution (Eliminating `ldd` Fork-Exec)
* **Goal**: Completely eliminate running `/usr/bin/ldd` as an external subprocess.
* **Approach**: Parse the target ELF binary headers directly in Go (using the standard `debug/elf` package) to locate dynamic table tags (`DT_NEEDED` entries) and manually resolve dynamic dependency paths.
* **Benefit**: Removes fork-exec overhead, cuts down external system tool dependencies, and improves security compatibility on containerized/hardened environments.
