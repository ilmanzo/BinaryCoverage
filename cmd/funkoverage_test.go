package main

import (
	"debug/elf"
	"encoding/xml"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"funkoverage/internal/funkutil"
)

// --- checkKernelVersion tests ---

func TestCheckKernelVersion(t *testing.T) {
	if err := checkKernelVersion(0, 0); err != nil {
		t.Errorf("checkKernelVersion(0, 0) should always pass on a real kernel: %v", err)
	}
	if err := checkKernelVersion(999, 0); err == nil {
		t.Error("checkKernelVersion(999, 0) should fail on any real kernel")
	}
}

// --- setupEnv test ---

func TestSetupEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("LOG_DIR", filepath.Join(tmp, "logs"))
	t.Setenv("SAFE_BIN_DIR", filepath.Join(tmp, "safe"))

	err := setupEnv()
	if err != nil {
		// Expected on machines without BTF (most CI runners) or an old kernel.
		return
	}
	// If it succeeded, both directories must actually exist.
	if _, statErr := os.Stat(funkutil.LogDir()); statErr != nil {
		t.Errorf("setupEnv succeeded but LOG_DIR missing: %v", statErr)
	}
	if _, statErr := os.Stat(funkutil.SafeBinDir()); statErr != nil {
		t.Errorf("setupEnv succeeded but SAFE_BIN_DIR missing: %v", statErr)
	}
}

// --- FuncFilter.Sidecar tests ---

func TestFuncFilterSidecar(t *testing.T) {
	var nilFilter *FuncFilter
	if s := nilFilter.Sidecar(); s.Include != "" || s.Exclude != "" {
		t.Errorf("nil filter should produce empty sidecar, got %+v", s)
	}

	empty, _ := NewFuncFilter("", "")
	if s := empty.Sidecar(); s.Include != "" || s.Exclude != "" {
		t.Errorf("empty filter should produce empty sidecar, got %+v", s)
	}

	includeOnly, _ := NewFuncFilter("^str_", "")
	if s := includeOnly.Sidecar(); s.Include != "^str_" || s.Exclude != "" {
		t.Errorf("unexpected sidecar for include-only: %+v", s)
	}

	both, _ := NewFuncFilter("^math_", "is_")
	if s := both.Sidecar(); s.Include != "^math_" || s.Exclude != "is_" {
		t.Errorf("unexpected sidecar for both: %+v", s)
	}
}

// --- findDebugFile tests ---

func TestFindDebugFile(t *testing.T) {
	tmp := t.TempDir()
	direct := filepath.Join(tmp, "foo.debug")
	if err := os.WriteFile(direct, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findDebugFile(direct); got != direct {
		t.Errorf("findDebugFile(direct path) = %q, want %q", got, direct)
	}
	if got := findDebugFile(filepath.Join(tmp, "nonexistent.debug")); got != "" {
		t.Errorf("findDebugFile(missing) = %q, want empty", got)
	}
}

// --- locateExternalDebugForMerge tests ---

func TestLocateExternalDebugForMerge(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}
	tmp := t.TempDir()
	orig := globalDebugRoot
	globalDebugRoot = tmp
	defer func() { globalDebugRoot = orig }()

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin_merge")
	if out, err := exec.Command("gcc", "-g", "-Wl,--build-id", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := getBuildID(f)
	f.Close()
	if err != nil {
		t.Fatalf("get build id: %v", err)
	}
	if out, err := exec.Command("strip", "--strip-debug", bin).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}

	// No external debug file placed yet: nothing to find.
	if got, err := locateExternalDebugForMerge(bin); err != nil || got != "" {
		t.Errorf("locateExternalDebugForMerge before placing debug file = (%q, %v), want (\"\", nil)", got, err)
	}

	dir := filepath.Join(tmp, ".build-id", buildID[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(dir, buildID[2:]+".debug")
	if err := os.WriteFile(debugFile, []byte("dummy debug info"), 0644); err != nil {
		t.Fatal(err)
	}
	got, err := locateExternalDebugForMerge(bin)
	if err != nil {
		t.Fatalf("locateExternalDebugForMerge: %v", err)
	}
	if got != debugFile {
		t.Errorf("locateExternalDebugForMerge = %q, want %q", got, debugFile)
	}
}

// --- ParseLddLibraries tests ---

func TestParseLddLibraries(t *testing.T) {
	if _, err := exec.LookPath("ldd"); err != nil {
		t.Skip("ldd not found")
	}
	shBin, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not found")
	}

	libs, err := ParseLddLibraries(shBin)
	if err != nil {
		t.Fatalf("ParseLddLibraries: %v", err)
	}
	for _, l := range libs {
		if funkutil.IsSystemLib(l) {
			t.Errorf("system library leaked into result: %s", l)
		}
		if strings.Contains(l, "vdso") {
			t.Errorf("vdso leaked into result: %s", l)
		}
	}

	if _, err := ParseLddLibraries("/nonexistent/binary_xyz123"); err == nil {
		t.Error("expected error for nonexistent binary")
	}
}

// --- dwarfFunctions / enumerateDWARF tests ---

func TestDwarfFunctions(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	code := `
int dwarf_add(int a, int b) { return a + b; }
int dwarf_sub(int a, int b) { return a - b; }
int main() { return dwarf_add(1, 2) + dwarf_sub(3, 1); }
`
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "dwarf_test")
	if out, err := exec.Command("gcc", "-g", "-O0", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}

	funcs, err := dwarfFunctions(bin, nil)
	if err != nil {
		t.Fatalf("dwarfFunctions: %v", err)
	}
	hasAdd, hasSub := false, false
	for _, name := range funcs {
		if name == "dwarf_add" {
			hasAdd = true
		}
		if name == "dwarf_sub" {
			hasSub = true
		}
	}
	if !hasAdd || !hasSub {
		t.Errorf("expected dwarf_add and dwarf_sub in DWARF-enumerated functions, got %v", funcs)
	}

	if _, err := dwarfFunctions("/nonexistent/binary_xyz123", nil); err == nil {
		t.Error("expected error for nonexistent binary")
	}
}

// --- isELF tests ---

func TestIsELF(t *testing.T) {
	tmp := t.TempDir()

	elfFile := filepath.Join(tmp, "elf")
	if err := os.WriteFile(elfFile, []byte("\x7fELFfoobar"), 0644); err != nil {
		t.Fatal(err)
	}
	if !isELF(elfFile) {
		t.Error("isELF should return true for ELF magic")
	}

	shFile := filepath.Join(tmp, "script.sh")
	if err := os.WriteFile(shFile, []byte("#!/bin/bash\necho hi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if isELF(shFile) {
		t.Error("isELF should return false for shell script")
	}

	emptyFile := filepath.Join(tmp, "empty")
	if err := os.WriteFile(emptyFile, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}
	if isELF(emptyFile) {
		t.Error("isELF should return false for empty file")
	}
}

// --- hasDebugInfo tests ---

func TestHasDebugInfo(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	binDebug := filepath.Join(tmp, "bin_debug")
	if out, err := exec.Command("gcc", "-g", "-o", binDebug, src).CombinedOutput(); err != nil {
		t.Fatalf("compile with debug: %v\n%s", err, out)
	}
	if has, err := hasDebugInfo(binDebug); err != nil || !has {
		t.Errorf("expected debug info present (err: %v, has: %v)", err, has)
	}

	binStrip := filepath.Join(tmp, "bin_strip")
	if out, err := exec.Command("gcc", "-s", "-o", binStrip, src).CombinedOutput(); err != nil {
		t.Fatalf("compile stripped: %v\n%s", err, out)
	}
	if has, err := hasDebugInfo(binStrip); err != nil || has {
		t.Errorf("expected no debug info for stripped binary (err: %v, has: %v)", err, has)
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

	orig := globalDebugRoot
	globalDebugRoot = tmp
	defer func() { globalDebugRoot = orig }()

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin_linked")
	if out, err := exec.Command("gcc", "-g", "-Wl,--build-id", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := getBuildID(f)
	f.Close()
	if err != nil {
		t.Fatalf("get build id: %v", err)
	}
	if out, err := exec.Command("strip", "--strip-debug", bin).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}
	if has, err := hasDebugInfo(bin); err != nil || has {
		t.Fatal("expected no debug info after strip")
	}
	dir := filepath.Join(tmp, ".build-id", buildID[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(dir, buildID[2:]+".debug")
	if err := os.WriteFile(debugFile, []byte("dummy debug info"), 0644); err != nil {
		t.Fatal(err)
	}
	if has, err := hasDebugInfo(bin); err != nil || !has {
		t.Error("expected linked debug info found")
	}
}

// --- analyzeLogs tests ---

func TestAnalyzeLogs(t *testing.T) {
	tmp := t.TempDir()
	funcLog := filepath.Join(tmp, "run_functions.log")
	calledLog := filepath.Join(tmp, "run_called.log")

	if err := os.WriteFile(funcLog, []byte("FUNC /bin/prog foo\nFUNC /bin/prog bar\nFUNC /bin/prog baz\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(calledLog, []byte("CALLED /bin/prog foo\n"), 0644); err != nil {
		t.Fatal(err)
	}

	coverage, err := analyzeLogs([]string{funcLog, calledLog})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := coverage["/bin/prog"]
	if !ok {
		t.Fatal("/bin/prog not found in coverage")
	}
	if len(data.TotalFunctions) != 3 {
		t.Errorf("expected 3 total, got %d", len(data.TotalFunctions))
	}
	if len(data.CalledFunctions) != 1 {
		t.Errorf("expected 1 called, got %d", len(data.CalledFunctions))
	}
	if _, ok := data.CalledFunctions["foo"]; !ok {
		t.Error("foo should be called")
	}
}

func TestAnalyzeLogsNewFormat(t *testing.T) {
	tmp := t.TempDir()

	funcLog := filepath.Join(tmp, "sample_20260101-120000_1234_functions.log")
	calledLog := filepath.Join(tmp, "sample_20260101-120001_5678_called.log")

	if err := os.WriteFile(funcLog, []byte("FUNC /bin/sample foo\nFUNC /bin/sample bar\nFUNC /bin/sample baz\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(calledLog, []byte("CALLED /bin/sample foo\nCALLED /bin/sample bar\n"), 0644); err != nil {
		t.Fatal(err)
	}

	coverage, err := analyzeLogs([]string{funcLog, calledLog})
	if err != nil {
		t.Fatal(err)
	}
	data, ok := coverage["/bin/sample"]
	if !ok {
		t.Fatal("/bin/sample not in coverage")
	}
	if len(data.TotalFunctions) != 3 {
		t.Errorf("expected 3 total, got %d", len(data.TotalFunctions))
	}
	if len(data.CalledFunctions) != 2 {
		t.Errorf("expected 2 called, got %d", len(data.CalledFunctions))
	}
	if _, ok := data.CalledFunctions["foo"]; !ok {
		t.Error("foo should be called")
	}
	if _, ok := data.TotalFunctions["baz"]; !ok {
		t.Error("baz should be in total")
	}
}

func TestDetectLogType(t *testing.T) {
	cases := []struct{ path, want string }{
		{"sample_20260101_functions.log", "functions"},
		{"sample_20260101_called.log", "called"},
		{"old_pin.log", ""},   // unrecognized → skipped
		{"functions.log", ""}, // suffix only, no prefix → skipped
	}
	for _, c := range cases {
		if got := detectLogType(c.path); got != c.want {
			t.Errorf("detectLogType(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestAnalyzeLogsEmpty(t *testing.T) {
	coverage, err := analyzeLogs([]string{})
	if err != nil {
		t.Fatalf("should not error on empty input: %v", err)
	}
	if len(coverage) != 0 {
		t.Errorf("expected empty map, got %v", coverage)
	}
}

func TestAnalyzeLogsMalformed(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "bad.log")
	if err := os.WriteFile(logFile, []byte("not a real log line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	coverage, err := analyzeLogs([]string{logFile})
	if err != nil {
		t.Fatalf("should not error on malformed log: %v", err)
	}
	if len(coverage) != 0 {
		t.Errorf("expected empty map for malformed log, got %v", coverage)
	}
}

// --- EnumerateFunctions test ---

func TestEnumerateFunctions(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	code := `
int add(int a, int b) { return a + b; }
int sub(int a, int b) { return a - b; }
int main() { return add(1,2) + sub(3,1); }
`
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "test_bin")
	if out, err := exec.Command("gcc", "-g", "-O0", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}

	funcs, err := EnumerateFunctions(bin, true, nil)
	if err != nil {
		t.Fatalf("EnumerateFunctions: %v", err)
	}
	if len(funcs) == 0 {
		t.Fatal("expected at least one image in result")
	}
	var found []string
	for _, names := range funcs {
		found = append(found, names...)
	}
	hasAdd := false
	hasSub := false
	for _, name := range found {
		if name == "add" {
			hasAdd = true
		}
		if name == "sub" {
			hasSub = true
		}
	}
	if !hasAdd {
		t.Error("expected 'add' in enumerated functions")
	}
	if !hasSub {
		t.Error("expected 'sub' in enumerated functions")
	}
}

// --- install/uninstall tests ---

// compileDebugBinary compiles a small C program with -g and returns the binary path.
func compileDebugBinary(t *testing.T, dir, name string) string {
	t.Helper()
	src := filepath.Join(dir, name+".c")
	if err := os.WriteFile(src, []byte("int helper() { return 42; }\nint main() { return helper(); }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, name)
	if out, err := exec.Command("gcc", "-g", "-O0", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile %s: %v\n%s", name, err, out)
	}
	return bin
}

// makeDummyShim creates a copy of /bin/true as a stand-in for funkoverage-shim.
func makeDummyShim(t *testing.T, dir string) string {
	t.Helper()
	shimPath := filepath.Join(dir, "funkoverage-shim")
	data, err := os.ReadFile("/bin/true")
	if err != nil {
		// fallback
		data, err = os.ReadFile("/usr/bin/true")
		if err != nil {
			t.Skip("cannot find /bin/true for dummy shim")
		}
	}
	if err := os.WriteFile(shimPath, data, 0755); err != nil {
		t.Fatal(err)
	}
	return shimPath
}

func TestInstallUninstall(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	safeBinDir := filepath.Join(tmp, "safe")
	logDir := filepath.Join(tmp, "logs")
	shimDir := filepath.Join(tmp, "shim")
	if err := os.MkdirAll(shimDir, 0755); err != nil {
		t.Fatal(err)
	}

	shimPath := makeDummyShim(t, shimDir)
	t.Setenv("FUNKOVERAGE_SHIM", shimPath)
	t.Setenv("SAFE_BIN_DIR", safeBinDir)
	t.Setenv("LOG_DIR", logDir)

	bin := compileDebugBinary(t, tmp, "testbin")

	if err := install(bin, true, nil); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Real binary should be at SAFE_BIN_DIR/<basename>
	safePath := filepath.Join(safeBinDir, "testbin")
	if _, err := os.Stat(safePath); err != nil {
		t.Errorf("real binary missing from safe dir: %v", err)
	}
	// The shim (copy of /bin/true) should be at the binary path
	if !isELF(bin) {
		t.Error("expected shim ELF at original binary path")
	}
	// Functions log should exist
	entries, _ := os.ReadDir(logDir)
	hasFunc := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "_functions.log") {
			hasFunc = true
		}
	}
	if !hasFunc {
		t.Error("expected _functions.log in log dir")
	}

	// Funcs sidecar should exist next to the safe binary and round-trip.
	funcsSidecar := funkutil.FuncListPath(safePath)
	if _, err := os.Stat(funcsSidecar); err != nil {
		t.Errorf("expected funcs sidecar at %s: %v", funcsSidecar, err)
	}
	if got := funkutil.ReadFuncList(safePath); len(got) == 0 {
		t.Error("ReadFuncList: expected non-empty map after install")
	}

	if err := uninstall(bin); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	// Safe path should be gone after uninstall
	if _, err := os.Stat(safePath); err == nil {
		t.Error("safe binary should be removed after uninstall")
	}
	// Funcs sidecar should be gone after uninstall
	if _, err := os.Stat(funcsSidecar); err == nil {
		t.Error("funcs sidecar should be removed after uninstall")
	}
	// Original ELF should be restored
	if !isELF(bin) {
		t.Error("expected original ELF restored after uninstall")
	}
}

func TestInstallMany(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	safeBinDir := filepath.Join(tmp, "safe")
	logDir := filepath.Join(tmp, "logs")
	shimDir := filepath.Join(tmp, "shim")
	if err := os.MkdirAll(shimDir, 0755); err != nil {
		t.Fatal(err)
	}

	shimPath := makeDummyShim(t, shimDir)
	t.Setenv("FUNKOVERAGE_SHIM", shimPath)
	t.Setenv("SAFE_BIN_DIR", safeBinDir)
	t.Setenv("LOG_DIR", logDir)

	bin1 := compileDebugBinary(t, tmp, "bin1")
	bin2 := compileDebugBinary(t, tmp, "bin2")

	if err := installMany([]string{bin1, bin2}, true, nil); err != nil {
		t.Fatalf("installMany: %v", err)
	}
	if err := uninstallMany([]string{bin1, bin2}); err != nil {
		t.Fatalf("uninstallMany: %v", err)
	}
	if !isELF(bin1) || !isELF(bin2) {
		t.Error("expected original ELFs restored")
	}
}

// --- HTML report test ---

func TestGenerateHTMLReportBaseName(t *testing.T) {
	tmp := t.TempDir()
	data := &CoverageData{
		TotalFunctions:  map[string]struct{}{"foo": {}, "bar": {}},
		CalledFunctions: map[string]struct{}{"foo": {}},
	}
	if err := generateHTMLReport("/some/long/path/mybinary", data, tmp); err != nil {
		t.Fatalf("generateHTMLReport: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tmp, "mybinary.html"))
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	if !strings.Contains(string(content), "mybinary") {
		t.Error("HTML should contain base name 'mybinary'")
	}
	if strings.Contains(string(content), "/some/long/path/mybinary") {
		t.Error("HTML should not contain full path")
	}
}

// --- summarizeCoverage tests ---

func TestSummarizeCoverage_Empty(t *testing.T) {
	summary := summarizeCoverage(map[string]*CoverageData{})
	if len(summary.Rows) != 0 || summary.TotalFunctions != 0 || summary.TotalCalled != 0 {
		t.Errorf("expected empty summary, got %+v", summary)
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
	if row.TotalCount != 3 || row.CalledCount != 2 {
		t.Errorf("unexpected counts: %+v", row)
	}
	if summary.AverageCoverage < 66.6 || summary.AverageCoverage > 66.7 {
		t.Errorf("expected ~66.67%% coverage, got %f", summary.AverageCoverage)
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
	if summary.TotalFunctions != 5 || summary.TotalCalled != 4 {
		t.Errorf("unexpected totals: %+v", summary)
	}
	if summary.AverageCoverage < 79.9 || summary.AverageCoverage > 80.1 {
		t.Errorf("expected ~80%% coverage, got %f", summary.AverageCoverage)
	}
	if len(summary.Rows) != 2 || !(summary.Rows[0].ImageName < summary.Rows[1].ImageName) {
		t.Error("rows should be sorted alphabetically")
	}
}

// --- FuncFilter tests ---

func TestNewFuncFilter(t *testing.T) {
	f, err := NewFuncFilter("", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Include != nil || f.Exclude != nil {
		t.Error("empty strings should produce nil regexps")
	}

	f, err = NewFuncFilter("^str_", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Include == nil {
		t.Error("expected non-nil Include")
	}

	f, err = NewFuncFilter("", "^util_")
	if err != nil {
		t.Fatal(err)
	}
	if f.Exclude == nil {
		t.Error("expected non-nil Exclude")
	}

	_, err = NewFuncFilter("[invalid", "")
	if err == nil {
		t.Error("expected error for invalid include regex")
	}
	_, err = NewFuncFilter("", "[invalid")
	if err == nil {
		t.Error("expected error for invalid exclude regex")
	}
}

func TestFuncFilterMatch(t *testing.T) {
	var nilFilter *FuncFilter
	if !nilFilter.Match("anything") {
		t.Error("nil filter should match everything")
	}

	include, _ := NewFuncFilter("^str_", "")
	if !include.Match("str_length") {
		t.Error("should match str_length")
	}
	if include.Match("math_add") {
		t.Error("should not match math_add")
	}

	exclude, _ := NewFuncFilter("", "^util_")
	if !exclude.Match("str_length") {
		t.Error("should match str_length")
	}
	if exclude.Match("util_clamp") {
		t.Error("should not match util_clamp")
	}

	both, _ := NewFuncFilter("^math_", "is_")
	if !both.Match("math_add") {
		t.Error("should match math_add")
	}
	if both.Match("math_is_prime") {
		t.Error("should not match math_is_prime (excluded)")
	}
	if both.Match("str_length") {
		t.Error("should not match str_length (not included)")
	}

	empty, _ := NewFuncFilter("", "")
	if !empty.Match("anything") {
		t.Error("empty filter should match everything")
	}
}

// --- imageIsRelevant tests ---

func TestImageIsRelevant(t *testing.T) {
	if imageIsRelevant("[vdso]") {
		t.Error("[vdso] should not be relevant")
	}
	if imageIsRelevant("linux-vdso.so.1") {
		t.Error("linux-vdso.so.1 should not be relevant")
	}
	if imageIsRelevant("") {
		t.Error("empty should not be relevant")
	}
	if !imageIsRelevant("libssl.so.3") {
		t.Error("libssl.so.3 should be relevant")
	}
	if !imageIsRelevant("libcrypto.so.3") {
		t.Error("libcrypto.so.3 should be relevant")
	}
}

// --- utsString tests ---

func TestUtsString(t *testing.T) {
	input := []int8{'h', 'e', 'l', 'l', 'o', 0, 'x', 'y'}
	if got := utsString(input); got != "hello" {
		t.Errorf("utsString = %q, want %q", got, "hello")
	}
	if got := utsString([]int8{0}); got != "" {
		t.Errorf("utsString([0]) = %q, want empty", got)
	}
	if got := utsString([]int8{'a', 'b'}); got != "ab" {
		t.Errorf("utsString without null = %q, want %q", got, "ab")
	}
}

// --- collectLogFiles tests ---

func TestCollectLogFiles_Dir(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a_functions.log"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmp, "b_called.log"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmp, "c.txt"), []byte("x"), 0644)

	files := collectLogFiles(tmp)
	if len(files) != 2 {
		t.Fatalf("expected 2 log files, got %d: %v", len(files), files)
	}
	for _, f := range files {
		if !strings.HasSuffix(f, ".log") {
			t.Errorf("non-log file included: %s", f)
		}
	}
}

func TestCollectLogFiles_CommaSep(t *testing.T) {
	files := collectLogFiles("a.log,b.log,c.log")
	if len(files) != 3 {
		t.Fatalf("expected 3, got %d", len(files))
	}
	if files[0] != "a.log" || files[2] != "c.log" {
		t.Errorf("unexpected files: %v", files)
	}
}

// --- generateXUnitReport tests ---

func TestGenerateXUnitReport(t *testing.T) {
	tmp := t.TempDir()
	data := &CoverageData{
		TotalFunctions:  map[string]struct{}{"foo": {}, "bar": {}, "baz": {}},
		CalledFunctions: map[string]struct{}{"foo": {}},
	}
	if err := generateXUnitReport("/bin/test", data, tmp); err != nil {
		t.Fatalf("generateXUnitReport: %v", err)
	}
	xmlFile := filepath.Join(tmp, "coverage_test.xml")
	content, err := os.ReadFile(xmlFile)
	if err != nil {
		t.Fatalf("read xml: %v", err)
	}

	var ts TestSuites
	if err := xml.Unmarshal(content, &ts); err != nil {
		t.Fatalf("unmarshal xml: %v", err)
	}
	if len(ts.TestSuite) != 1 {
		t.Fatalf("expected 1 suite, got %d", len(ts.TestSuite))
	}
	suite := ts.TestSuite[0]
	if suite.Tests != 3 {
		t.Errorf("expected 3 tests, got %d", suite.Tests)
	}
	if suite.Skipped != 2 {
		t.Errorf("expected 2 skipped (uncalled), got %d", suite.Skipped)
	}
}

// --- generateAggregateHTMLReport tests ---

func TestGenerateAggregateHTMLReport(t *testing.T) {
	tmp := t.TempDir()
	coverage := map[string]*CoverageData{
		"/bin/foo": {
			TotalFunctions:  map[string]struct{}{"a": {}, "b": {}},
			CalledFunctions: map[string]struct{}{"a": {}},
		},
		"/bin/bar": {
			TotalFunctions:  map[string]struct{}{"x": {}},
			CalledFunctions: map[string]struct{}{"x": {}},
		},
	}
	if err := generateAggregateHTMLReport(coverage, tmp); err != nil {
		t.Fatalf("generateAggregateHTMLReport: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(tmp, "aggregate.html"))
	if err != nil {
		t.Fatalf("read aggregate html: %v", err)
	}
	html := string(content)
	if !strings.Contains(html, "foo") || !strings.Contains(html, "bar") {
		t.Error("aggregate HTML should contain both image names")
	}
}

// --- printTxtReport test ---

func TestPrintTxtReport(t *testing.T) {
	coverage := map[string]*CoverageData{
		"/bin/test": {
			TotalFunctions:  map[string]struct{}{"a": {}, "b": {}},
			CalledFunctions: map[string]struct{}{"a": {}},
		},
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	printTxtReport(coverage)
	w.Close()
	os.Stdout = old

	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	output := string(buf[:n])
	if !strings.Contains(output, "Coverage:") {
		t.Error("txt report should contain 'Coverage:'")
	}
	if !strings.Contains(output, "50.00%") {
		t.Errorf("expected 50%% coverage in output: %s", output)
	}
}

// --- moveCrossDevice tests ---

func TestMoveCrossDevice(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src.txt")
	dst := filepath.Join(tmp, "dst.txt")
	content := []byte("hello world")
	if err := os.WriteFile(src, content, 0750); err != nil {
		t.Fatal(err)
	}
	if err := moveCrossDevice(src, dst); err != nil {
		t.Fatalf("moveCrossDevice: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content mismatch: %q vs %q", got, content)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("source should be removed after move")
	}
	fi, _ := os.Stat(dst)
	if fi.Mode().Perm() != 0750 {
		t.Errorf("expected perm 0750, got %o", fi.Mode().Perm())
	}
}

// --- EnumerateFunctions with filter tests ---

func TestEnumerateFunctionsWithFilter(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	code := `
int str_length() { return 42; }
int str_upper() { return 1; }
int math_add() { return 2; }
int main() { return str_length() + str_upper() + math_add(); }
`
	os.WriteFile(src, []byte(code), 0644)
	bin := filepath.Join(tmp, "test_filter")
	if out, err := exec.Command("gcc", "-g", "-O0", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}

	filter, _ := NewFuncFilter("^str_", "")
	funcs, err := EnumerateFunctions(bin, true, filter)
	if err != nil {
		t.Fatalf("EnumerateFunctions: %v", err)
	}
	var names []string
	for _, fns := range funcs {
		names = append(names, fns...)
	}
	for _, n := range names {
		if !strings.HasPrefix(n, "str_") {
			t.Errorf("filter leaked non-str_ function: %s", n)
		}
	}
	if len(names) != 2 {
		t.Errorf("expected 2 str_ functions, got %d: %v", len(names), names)
	}
}

// --- emitReport dispatch tests ---

func TestEmitReport(t *testing.T) {
	tmp := t.TempDir()
	coverage := map[string]*CoverageData{
		"/bin/test": {
			TotalFunctions:  map[string]struct{}{"a": {}},
			CalledFunctions: map[string]struct{}{"a": {}},
		},
	}

	emitReport("html", coverage, tmp)
	if _, err := os.Stat(filepath.Join(tmp, "test.html")); err != nil {
		t.Error("emitReport('html') should create test.html")
	}

	emitReport("xml", coverage, tmp)
	if _, err := os.Stat(filepath.Join(tmp, "coverage_test.xml")); err != nil {
		t.Error("emitReport('xml') should create XML file")
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	emitReport("txt", coverage, tmp)
	w.Close()
	os.Stdout = old
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if !strings.Contains(string(buf[:n]), "Coverage:") {
		t.Error("emitReport('txt') should print coverage")
	}
}
