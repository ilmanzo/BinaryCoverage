package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// --- isELF tests ---

func TestIsELF(t *testing.T) {
	tmp := t.TempDir()

	// Create a fake ELF file
	elfFile := filepath.Join(tmp, "elf")
	if err := os.WriteFile(elfFile, []byte("\x7fELFfoobar"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isELF(elfFile) {
		t.Errorf("isELF should return true for ELF magic")
	}

	// Create a shell script
	shFile := filepath.Join(tmp, "script.sh")
	if err := os.WriteFile(shFile, []byte("#!/bin/bash\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if isELF(shFile) {
		t.Errorf("isELF should return false for shell script")
	}

	// Create an empty file
	emptyFile := filepath.Join(tmp, "empty")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if isELF(emptyFile) {
		t.Errorf("isELF should return false for empty file")
	}
}

// --- hasDebugInfo tests ---

func TestHasDebugInfo(t *testing.T) {
	// We need gcc to compile binaries with/without debug info
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found, skipping debug info test")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1. Compile with debug info (-g)
	binDebug := filepath.Join(tmp, "bin_debug")
	if out, err := exec.Command("gcc", "-g", "-o", binDebug, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile with debug info: %v\n%s", err, out)
	}

	if has, err := hasDebugInfo(binDebug); err != nil || !has {
		t.Errorf("expected binary with -g to have debug info (err: %v, has: %v)", err, has)
	}

	// 2. Compile without debug info (-s strips symbols)
	binStrip := filepath.Join(tmp, "bin_strip")
	if out, err := exec.Command("gcc", "-s", "-o", binStrip, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile stripped binary: %v\n%s", err, out)
	}

	// Stripped binary should not have embedded debug info, and we don't expect external debug info in /usr/lib/debug for this temp file
	if has, err := hasDebugInfo(binStrip); err != nil || has {
		t.Errorf("expected stripped binary to NOT have debug info (err: %v, has: %v)", err, has)
	}
}

func TestHasDebugInfo_Linked(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}

	tmp := t.TempDir()

	// Override globalDebugRoot to point to our temp dir
	orig := globalDebugRoot
	globalDebugRoot = tmp
	defer func() { globalDebugRoot = orig }()

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(tmp, "bin_linked")
	// Compile with build-id and debug info
	if out, err := exec.Command("gcc", "-g", "-Wl,--build-id", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile: %v\n%s", err, out)
	}

	// Extract Build ID
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := getBuildID(f)
	f.Close()
	if err != nil {
		t.Fatalf("failed to get build ID: %v", err)
	}

	// Strip debug info
	if out, err := exec.Command("strip", "--strip-debug", bin).CombinedOutput(); err != nil {
		t.Fatalf("failed to strip: %v\n%s", err, out)
	}

	// Should NOT have debug info now (no embedded, no external yet)
	if has, err := hasDebugInfo(bin); err != nil || has {
		t.Fatalf("expected no debug info after strip")
	}

	// Create external debug file
	// Structure: <root>/.build-id/xx/xxxx.debug
	dir := filepath.Join(tmp, ".build-id", buildID[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(dir, buildID[2:]+".debug")
	if err := os.WriteFile(debugFile, []byte("dummy debug info"), 0644); err != nil {
		t.Fatal(err)
	}

	// Should have debug info now (linked)
	if has, err := hasDebugInfo(bin); err != nil || !has {
		t.Errorf("expected linked debug info found")
	}
}

// --- findPinTool tests ---

func TestFindPinTool(t *testing.T) {
	tmp := t.TempDir()
	// Should not find anything
	_, err := findPinTool(tmp)
	if err == nil {
		t.Error("findPinTool should fail if FuncTracer.so is not present")
	}
	// Create a dummy FuncTracer.so
	subdir := filepath.Join(tmp, "sub")
	os.Mkdir(subdir, 0755)
	target := filepath.Join(subdir, "FuncTracer.so")
	if err := os.WriteFile(target, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}
	found, err := findPinTool(tmp)
	if err != nil {
		t.Fatalf("findPinTool failed: %v", err)
	}
	if found != target {
		t.Errorf("findPinTool returned wrong path: got %s, want %s", found, target)
	}
}

// --- analyzeLogs tests ---

func TestAnalyzeLogs(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "log.txt")
	content := `[Image:prog] [Function:foo]
[Image:prog] [Function:bar]
[Image:prog] [Called:foo]
[Image:prog] [Function:baz]
`
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	coverage, err := analyzeLogs([]string{logFile})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := coverage["prog"]
	if !ok {
		t.Fatal("prog not found in coverage")
	}
	if len(data.TotalFunctions) != 3 {
		t.Errorf("expected 3 total functions, got %d", len(data.TotalFunctions))
	}
	if len(data.CalledFunctions) != 1 {
		t.Errorf("expected 1 called function, got %d", len(data.CalledFunctions))
	}
	if _, ok := data.CalledFunctions["foo"]; !ok {
		t.Error("foo should be in called functions")
	}
	if _, ok := data.TotalFunctions["baz"]; !ok {
		t.Error("baz should be in total functions")
	}
}

func TestAnalyzeLogsEmpty(t *testing.T) {
	coverage, err := analyzeLogs([]string{})
	if err != nil {
		t.Fatalf("analyzeLogs should not error on empty input: %v", err)
	}
	if len(coverage) != 0 {
		t.Errorf("expected empty coverage map, got %v", coverage)
	}
}

func TestAnalyzeLogsMalformed(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "bad.log")
	// Write a malformed log
	content := `not a real log line`
	if err := os.WriteFile(logFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	coverage, err := analyzeLogs([]string{logFile})
	if err != nil {
		t.Fatalf("analyzeLogs should not error on malformed log: %v", err)
	}
	// Should be empty, as no valid lines
	if len(coverage) != 0 {
		t.Errorf("expected empty coverage map for malformed log, got %v", coverage)
	}
}

// --- wrap/unwrap logic (integration) ---

func TestWrapUnwrapLogic(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}

	tmp := t.TempDir()
	orig := filepath.Join(tmp, "origbin")
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	// Compile with debug info
	if out, err := exec.Command("gcc", "-g", "-o", orig, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile: %v\n%s", err, out)
	}

	// Set up dummy environment
	os.Setenv("PIN_ROOT", "/tmp/pin")
	os.Setenv("PIN_TOOL_SEARCH_DIR", tmp)
	os.Setenv("SAFE_BIN_DIR", tmp)
	os.Setenv("LOG_DIR", tmp)
	// Create dummy FuncTracer.so
	funcTracer := filepath.Join(tmp, "FuncTracer.so")
	if err := os.WriteFile(funcTracer, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}
	// Wrap
	if err := wrap(orig); err != nil {
		t.Fatalf("wrap failed: %v", err)
	}
	// The wrapper should now exist and be a shell script
	content, err := os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), wrapperIDComment) {
		t.Error("wrapper script missing ID comment")
	}
	// Unwrap
	if err := unwrap(orig); err != nil {
		t.Fatalf("unwrap failed: %v", err)
	}
	// The original ELF should be restored
	_, err = os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !isELF(orig) {
		t.Error("unwrap did not restore ELF binary")
	}
}

func TestWrapManyAndUnwrapMany(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}

	tmp := t.TempDir()
	os.Setenv("PIN_ROOT", "/tmp/pin")
	os.Setenv("PIN_TOOL_SEARCH_DIR", tmp)
	os.Setenv("SAFE_BIN_DIR", tmp)
	os.Setenv("LOG_DIR", tmp)
	// Create dummy FuncTracer.so
	funcTracer := filepath.Join(tmp, "FuncTracer.so")
	if err := os.WriteFile(funcTracer, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create multiple fake ELF binaries
	bin1 := filepath.Join(tmp, "bin1")
	bin2 := filepath.Join(tmp, "bin2")
	bin3 := filepath.Join(tmp, "bin3")
	for _, bin := range []string{bin1, bin2, bin3} {
		if out, err := exec.Command("gcc", "-g", "-o", bin, src).CombinedOutput(); err != nil {
			t.Fatalf("failed to compile %s: %v\n%s", bin, err, out)
		}
	}

	// Wrap all binaries
	if err := wrapMany([]string{bin1, bin2, bin3}); err != nil {
		t.Fatalf("wrapMany failed: %v", err)
	}
	for _, bin := range []string{bin1, bin2, bin3} {
		content, err := os.ReadFile(bin)
		if err != nil {
			t.Fatalf("failed to read wrapped binary %s: %v", bin, err)
		}
		if !strings.Contains(string(content), wrapperIDComment) {
			t.Errorf("binary %s was not wrapped", bin)
		}
	}

	// Unwrap all binaries
	if err := unwrapMany([]string{bin1, bin2, bin3}); err != nil {
		t.Fatalf("unwrapMany failed: %v", err)
	}
	for _, bin := range []string{bin1, bin2, bin3} {
		_, err := os.ReadFile(bin)
		if err != nil {
			t.Fatalf("failed to read unwrapped binary %s: %v", bin, err)
		}
		if !isELF(bin) {
			t.Errorf("binary %s was not restored to ELF", bin)
		}
	}
}

func TestWrapUnwrapMulticall(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}

	tmp := t.TempDir()
	os.Setenv("PIN_ROOT", "/tmp/pin")
	os.Setenv("PIN_TOOL_SEARCH_DIR", tmp)
	os.Setenv("SAFE_BIN_DIR", tmp)
	os.Setenv("LOG_DIR", tmp)
	// Create dummy FuncTracer.so
	funcTracer := filepath.Join(tmp, "FuncTracer.so")
	if err := os.WriteFile(funcTracer, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create real binary
	realBin := filepath.Join(tmp, "real_bin")
	if out, err := exec.Command("gcc", "-g", "-o", realBin, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile: %v\n%s", err, out)
	}

	// Create symlink: run0 -> real_bin
	symlinkBin := filepath.Join(tmp, "run0")
	if err := os.Symlink("real_bin", symlinkBin); err != nil {
		t.Fatal(err)
	}

	// Wrap the symlink
	if err := wrap(symlinkBin); err != nil {
		t.Fatalf("wrap failed: %v", err)
	}

	// 1. Check that real_bin is now a wrapper
	// Note: wrap resolves symlink, so it wraps the target.
	content, err := os.ReadFile(realBin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), wrapperIDComment) {
		t.Error("real binary was not wrapped")
	}

	// 2. Check that the wrapper points to the symlink in backup
	// We expect ORIGINAL_BINARY=".../run0"
	if !strings.Contains(string(content), "/run0\"") {
		t.Errorf("wrapper does not point to multicall symlink name. Content:\n%s", content)
	}

	// 3. Unwrap (using the real binary path, as wrap resolves it)
	if err := unwrap(realBin); err != nil {
		t.Fatalf("unwrap failed: %v", err)
	}

	// 4. Verify restoration
	if !isELF(realBin) {
		t.Error("unwrap did not restore ELF binary")
	}
}

func TestUnwrapViaSymlink(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}

	tmp := t.TempDir()
	os.Setenv("PIN_ROOT", "/tmp/pin")
	os.Setenv("PIN_TOOL_SEARCH_DIR", tmp)
	os.Setenv("SAFE_BIN_DIR", tmp)
	os.Setenv("LOG_DIR", tmp)
	// Create dummy FuncTracer.so
	funcTracer := filepath.Join(tmp, "FuncTracer.so")
	if err := os.WriteFile(funcTracer, []byte("dummy"), 0644); err != nil {
		t.Fatal(err)
	}

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create real binary
	realBin := filepath.Join(tmp, "real_bin")
	if out, err := exec.Command("gcc", "-g", "-o", realBin, src).CombinedOutput(); err != nil {
		t.Fatalf("failed to compile: %v\n%s", err, out)
	}

	// Create symlink: link_to_bin -> real_bin
	symlinkBin := filepath.Join(tmp, "link_to_bin")
	if err := os.Symlink("real_bin", symlinkBin); err != nil {
		t.Fatal(err)
	}

	// Wrap the real binary directly
	if err := wrap(realBin); err != nil {
		t.Fatalf("wrap failed: %v", err)
	}

	// Verify it is wrapped
	content, err := os.ReadFile(realBin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), wrapperIDComment) {
		t.Error("real binary was not wrapped")
	}

	// Unwrap via the symlink
	if err := unwrap(symlinkBin); err != nil {
		t.Fatalf("unwrap via symlink failed: %v", err)
	}

	// Verify restoration
	if !isELF(realBin) {
		t.Error("unwrap did not restore ELF binary")
	}
}

func TestGenerateHTMLReportBaseName(t *testing.T) {
	tmp := t.TempDir()
	data := &CoverageData{
		TotalFunctions:  map[string]struct{}{"foo": {}, "bar": {}},
		CalledFunctions: map[string]struct{}{"foo": {}},
	}
	imagePath := "/some/long/path/mybinary"
	err := generateHTMLReport(imagePath, data, tmp)
	if err != nil {
		t.Fatalf("generateHTMLReport failed: %v", err)
	}
	// Check that the HTML file exists and contains only the base name
	htmlFile := filepath.Join(tmp, "mybinary.html")
	content, err := os.ReadFile(htmlFile)
	if err != nil {
		t.Fatalf("failed to read generated HTML: %v", err)
	}
	if !strings.Contains(string(content), "mybinary") {
		t.Errorf("expected HTML report to contain base name 'mybinary'")
	}
	if strings.Contains(string(content), "/some/long/path/mybinary") {
		t.Errorf("HTML report should not contain full path")
	}
}

func TestSummarizeCoverage_Empty(t *testing.T) {
	coverage := map[string]*CoverageData{}
	summary := summarizeCoverage(coverage)
	if len(summary.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(summary.Rows))
	}
	if summary.TotalFunctions != 0 {
		t.Errorf("expected 0 total functions, got %d", summary.TotalFunctions)
	}
	if summary.TotalCalled != 0 {
		t.Errorf("expected 0 total called, got %d", summary.TotalCalled)
	}
	if summary.AverageCoverage != 0.0 {
		t.Errorf("expected 0.0 average coverage, got %f", summary.AverageCoverage)
	}
}

func TestSummarizeCoverage_SingleImage(t *testing.T) {
	coverage := map[string]*CoverageData{
		"foo": {
			TotalFunctions:  map[string]struct{}{"a": {}, "b": {}, "c": {}},
			CalledFunctions: map[string]struct{}{"a": {}, "b": {}},
		},
	}
	summary := summarizeCoverage(coverage)
	if len(summary.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(summary.Rows))
	}
	row := summary.Rows[0]
	if row.ImageName != "foo" {
		t.Errorf("expected image name 'foo', got %s", row.ImageName)
	}
	if row.TotalCount != 3 {
		t.Errorf("expected 3 total, got %d", row.TotalCount)
	}
	if row.CalledCount != 2 {
		t.Errorf("expected 2 called, got %d", row.CalledCount)
	}
	if row.CoveragePct != 66.66666666666666 && row.CoveragePct != 66.67 {
		t.Errorf("expected coverage ~66.67, got %f", row.CoveragePct)
	}
	if summary.TotalFunctions != 3 {
		t.Errorf("expected 3 total functions, got %d", summary.TotalFunctions)
	}
	if summary.TotalCalled != 2 {
		t.Errorf("expected 2 total called, got %d", summary.TotalCalled)
	}
	if summary.AverageCoverage < 66.6 || summary.AverageCoverage > 66.7 {
		t.Errorf("expected average coverage ~66.67, got %f", summary.AverageCoverage)
	}
}

func TestSummarizeCoverage_MultipleImages(t *testing.T) {
	coverage := map[string]*CoverageData{
		"foo": {
			TotalFunctions:  map[string]struct{}{"a": {}, "b": {}},
			CalledFunctions: map[string]struct{}{"a": {}},
		},
		"bar": {
			TotalFunctions:  map[string]struct{}{"x": {}, "y": {}, "z": {}},
			CalledFunctions: map[string]struct{}{"x": {}, "y": {}, "z": {}},
		},
	}
	summary := summarizeCoverage(coverage)
	if len(summary.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(summary.Rows))
	}
	// Check totals
	if summary.TotalFunctions != 5 {
		t.Errorf("expected 5 total functions, got %d", summary.TotalFunctions)
	}
	if summary.TotalCalled != 4 {
		t.Errorf("expected 4 total called, got %d", summary.TotalCalled)
	}
	if summary.AverageCoverage < 79.9 || summary.AverageCoverage > 80.1 {
		t.Errorf("expected average coverage ~80.0, got %f", summary.AverageCoverage)
	}
	// Check sorting
	if !(summary.Rows[0].ImageName < summary.Rows[1].ImageName) {
		t.Errorf("expected rows sorted by image name, got: %v", []string{summary.Rows[0].ImageName, summary.Rows[1].ImageName})
	}
}
