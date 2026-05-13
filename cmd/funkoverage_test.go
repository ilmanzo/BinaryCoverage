package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"funkoverage/internal/funkutil"
)

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

// --- analyzeLogs tests (legacy Pin format + new format) ---

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

// --- funcIsRelevant tests ---

func TestFuncIsRelevant(t *testing.T) {
	relevant := []string{"foo", "bar", "myFunc", "str_length", "math_add"}
	for _, name := range relevant {
		if !funcIsRelevant(name) {
			t.Errorf("funcIsRelevant(%q) should be true", name)
		}
	}
	irrelevant := []string{"main", "_init", "_start", "__cxa_atexit", "foo@plt", "bar@plt.got", "__libc_start_main"}
	for _, name := range irrelevant {
		if funcIsRelevant(name) {
			t.Errorf("funcIsRelevant(%q) should be false", name)
		}
	}
}

// --- isSystemLib tests ---

func TestIsSystemLib(t *testing.T) {
	syslibs := []string{
		"/lib64/libc.so.6",
		"/lib64/libm.so.6",
		"/usr/lib/x86_64-linux-gnu/libpthread.so.0",
		"/lib64/ld-linux-x86-64.so.2",
		"/lib64/libstdc++.so.6",
		"/lib64/libgcc_s.so.1",
		"/lib64/libdl.so.2",
		"/lib64/librt.so.1",
	}
	for _, p := range syslibs {
		if !isSystemLib(p) {
			t.Errorf("isSystemLib(%q) should be true", p)
		}
	}
	userlibs := []string{
		"/usr/lib64/libssl.so.3",
		"/usr/lib64/libcurl.so.4",
		"/opt/foo/libmycrypto.so.1",
		"/lib64/libz.so.1",
	}
	for _, p := range userlibs {
		if isSystemLib(p) {
			t.Errorf("isSystemLib(%q) should be false", p)
		}
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
