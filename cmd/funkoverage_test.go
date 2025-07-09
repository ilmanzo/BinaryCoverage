package main

import (
	"os"
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
	tmp := t.TempDir()
	orig := filepath.Join(tmp, "origbin")
	// Write a fake ELF binary
	if err := os.WriteFile(orig, []byte("\x7fELFfoobar"), 0755); err != nil {
		t.Fatal(err)
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
	content, err = os.ReadFile(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !isELF(orig) {
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
