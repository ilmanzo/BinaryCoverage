# Incremental Tracing: Scale to 100K+ Functions

## Concept

Once a uprobe fires and records a function call, **remove the probe**. This reduces active probe count over time, allowing multiple iterations to accumulate coverage of 10000+ functions.

```
Execution trace of large binary:

Iteration 1:
  Attach all 10000 uprobes
  Execute workload
  Record 400 function calls
  Remove those 400 probes
  Remaining: 9600 probes

Iteration 2:
  Attach 9600 remaining uprobes
  Execute same/different workload
  Record 150 new function calls (some repeat, skipped)
  Remove those 150 probes
  Remaining: 9450 probes

Iteration 3:
  Attach 9450 remaining uprobes
  Execute workload
  Record 40 new function calls
  Remove those 40 probes
  Remaining: 9410 probes

Iteration N:
  Negligible new calls detected
  Stop (coverage plateaued)

FINAL RESULT:
  Total unique functions traced: 400 + 150 + 40 + ... = ~590 across all runs
  Total functions enumerated: 10000
  Coverage: 5.9%
  Kernel burden: Never exceeded 10000 active probes
```

## Advantages Over Batch Approach

| Aspect | Batch (parallel) | Incremental | Winner |
|--------|------------------|-------------|--------|
| Scale limit | 8000 per script × N | Unbounded | Incremental |
| Memory usage | 3-4× (parallel bpftrace) | 1× (sequential) | Incremental |
| Kernel perf_events | 3× loaded | 1× loaded | Incremental |
| Implementation | ~200 LOC | ~100 LOC | Incremental |
| Workload diversity | Single run | Multiple runs | Incremental |
| Convergence | All funcs/run | Cumulative | Incremental |

**Key insight**: Incremental approach ALSO handles workload diversity naturally. Multiple runs with different workloads discover different code paths.

## Implementation Strategy

### Phase 1: Minimal changes

1. Add `--incremental` flag to `funkoverage trace/install`
2. After each workload execution:
   - Parse _called.log
   - Extract called functions
   - Rewrite _functions.log to exclude called functions
3. Next invocation traces only remaining functions

### Phase 2: Smarter iteration

1. Track "calls per iteration" metric
2. Stop when new calls < 5% of previous iteration
3. Generate coverage report with iteration metadata

### Phase 3: Workload sequencing

1. Allow specifying multiple test scenarios
2. Run each scenario once, accumulate coverage
3. Generate report showing:
   - Functions called per scenario
   - Overlaps
   - Total coverage

## Code Changes Required

### 1. Track "already seen" functions

**File: `cmd/shim.go`**

Add to `install()`:

```go
// Load previously-called functions
seenBefore := make(map[string]struct{})
if funcsLog, err := findLatestFunctionsLog(logDir, binaryName); err == nil {
    if seen, err := loadCalledFunctions(funcsLog); err == nil {
        seenBefore = seen
    }
}

// Enumerate, but exclude already-called
funcs, err := EnumerateFunctions(safePath, noLibs)
for image := range funcs {
    filtered := []string{}
    for _, fn := range funcs[image] {
        if _, alreadySeen := seenBefore[fn]; !alreadySeen {
            filtered = append(filtered, fn)
        }
    }
    if len(filtered) > 0 {
        funcs[image] = filtered
    } else {
        delete(funcs, image)  // Skip if all already called
    }
}

writeFunctionsLog(logDir, binaryName, funcs)
```

**New helper functions:**

```go
func findLatestFunctionsLog(logDir, binaryName string) (string, error) {
    // Return most recent ssh_*_functions.log
    entries, _ := os.ReadDir(logDir)
    var latest string
    var latestTime time.Time
    for _, e := range entries {
        if strings.HasPrefix(e.Name(), binaryName) && 
           strings.HasSuffix(e.Name(), "_functions.log") {
            fi, _ := e.Info()
            if fi.ModTime().After(latestTime) {
                latestTime = fi.ModTime()
                latest = filepath.Join(logDir, e.Name())
            }
        }
    }
    return latest, nil
}

func loadCalledFunctions(logPath string) (map[string]struct{}, error) {
    called := make(map[string]struct{})
    f, err := os.Open(logPath)
    if err != nil {
        return nil, err
    }
    defer f.Close()
    
    scanner := bufio.NewScanner(f)
    for scanner.Scan() {
        line := scanner.Text()
        parts := strings.Fields(line)  // "FUNC /path/to/image funcname"
        if len(parts) >= 3 {
            fn := parts[2]
            called[fn] = struct{}{}
        }
    }
    return called, scanner.Err()
}
```

### 2. Rename old logs, preserve history

**File: `cmd/enumerate.go`**

Modify `writeFunctionsLog()` to archive old logs:

```go
func writeFunctionsLog(logDir, binaryBasename string, funcs map[string][]string) (string, error) {
    if err := os.MkdirAll(logDir, 0777); err != nil {
        return "", err
    }
    
    ts := time.Now()
    name := fmt.Sprintf("%s_%s_%d_functions.log",
        binaryBasename,
        ts.Format("20060102-150405"),
        ts.UnixNano(),
    )
    path := filepath.Join(logDir, name)
    
    // Archive previous log (if any)
    prevLog := findLatestFunctionsLog(logDir, binaryBasename)
    if prevLog != "" && prevLog != path {
        archivePath := strings.Replace(prevLog, "_functions.log", "_prev.log", 1)
        os.Rename(prevLog, archivePath)  // Keep history
    }
    
    // Write new functions log
    f, err := os.Create(path)
    if err != nil {
        return "", err
    }
    defer f.Close()
    // ... existing write logic
}
```

### 3. Report generation: show iteration history

**File: `cmd/report.go`** (new logic)

```go
type IterationData struct {
    Timestamp    time.Time
    FunctionsEnumerated int
    FunctionsCalled     int
    CoveragePercent     float64
    NewFunctionsCalled  int  // Net new vs previous iteration
}

func analyzeIterations(logDir, binaryName string) []IterationData {
    // Scan for all _functions.log and _called.log pairs
    // Match by timestamp
    // Compute coverage per iteration
    // Return sorted by time
}
```

Then in HTML report:

```html
<table>
  <tr><th>Iteration</th><th>Time</th><th>Total Enum</th><th>Called</th><th>Coverage</th><th>New</th></tr>
  <tr><td>1</td><td>2026-05-12 15:40</td><td>8038</td><td>400</td><td>4.97%</td><td>400</td></tr>
  <tr><td>2</td><td>2026-05-12 15:45</td><td>7638</td><td>150</td><td>1.96%</td><td>150</td></tr>
  <tr><td>3</td><td>2026-05-12 15:50</td><td>7488</td><td>40</td><td>0.53%</td><td>40</td></tr>
</table>
```

## Example Workflow

```bash
# Installation (discovers all 10000 functions)
sudo funkoverage install /path/to/binary

# Run 1: Quick sanity check
/path/to/binary --version
# → 400 functions called, added to _called.log
# → Next invocation will exclude these 400

# Run 2: More complex scenario
/path/to/binary --test-mode
# → 150 NEW functions called (550 cumulative)
# → Next invocation will exclude these 550

# Run 3: Full integration test
/path/to/binary < large_input.txt
# → 40 NEW functions called (590 cumulative)

# Generate report with iteration history
funkoverage report /var/coverage/data /tmp/report
# → aggregate.html shows: 590 / 10000 = 5.9% coverage
# → Detailed report shows per-iteration breakdown
```

## Automatic Convergence Detection

Stop when new discoveries < threshold:

```go
func shouldContinueIterating(iterations []IterationData) bool {
    if len(iterations) < 2 {
        return true  // Always do at least 2 runs
    }
    
    recent := iterations[len(iterations)-1].NewFunctionsCalled
    previous := iterations[len(iterations)-2].NewFunctionsCalled
    
    // Stop if new calls are < 10% of previous iteration
    if recent < (previous / 10) {
        return false
    }
    
    // Or after 10 iterations regardless
    return len(iterations) < 10
}
```

## Trade-offs

| Pro | Con |
|-----|-----|
| ✅ Scales to 100K+ functions | ❌ Requires multiple runs |
| ✅ Single bpftrace process | ❌ Coverage accumulates slowly |
| ✅ Minimal code changes (~100 LOC) | ❌ Requires discipline (preserve logs) |
| ✅ Works with current arch | ❌ Iteration history tracking overhead |
| ✅ Natural diversity via workloads | ❌ Users must orchestrate scenarios |

## Recommended Approach

**Hybrid: Batch + Incremental**

1. Use BATCH for single large run (split 10K into 3 × 3.3K)
2. Use INCREMENTAL across multiple test scenarios
3. Best of both: No convergence waits + unbounded scalability

```bash
# First run: Batch approach (trace all 10000 at once)
funkoverage install --batch /path/to/binary
./binary --quick-test

# Second run: Same binary, different workload (incremental)
./binary --full-test
# Traces only functions not yet called

# Third run: Different scenario
./binary < large_file
# Traces only remaining functions

# Report shows cumulative coverage + per-iteration breakdown
funkoverage report /var/coverage/data /tmp/report
```

This achieves:
- ✅ Traces 10K functions across runs
- ✅ No kernel bottleneck (max 3.3K probes active)
- ✅ Workload diversity
- ✅ Graceful convergence detection

## Implementation Priority

1. **Phase 1 (Easy)**: Track seen functions, filter from next run (~100 LOC)
2. **Phase 2 (Medium)**: Iteration history in reports (~150 LOC)
3. **Phase 3 (Hard)**: Batch + Incremental combo (~200 LOC)

---

**Status**: Design phase  
**Complexity**: Low-Medium (100-200 LOC per phase)  
**Risk**: Very Low (purely additive, doesn't break existing single-run workflow)
