package main

import (
	"io"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"funkoverage/internal/funkutil"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// everything it wrote.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	out, _ := io.ReadAll(r)
	return string(out)
}

func TestFlattenFuncs_StableOrderAndCookies(t *testing.T) {
	funcs := map[string][]string{
		"/lib/b.so": {"z"},
		"/lib/a.so": {"x", "y"},
	}
	refs, syms, cookies := flattenFuncs(funcs)

	wantRefs := []FuncRef{
		{Image: "/lib/a.so", Name: "x"},
		{Image: "/lib/a.so", Name: "y"},
		{Image: "/lib/b.so", Name: "z"},
	}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("refs: got %v, want %v", refs, wantRefs)
	}

	if !reflect.DeepEqual(syms["/lib/a.so"], []string{"x", "y"}) {
		t.Errorf("syms[/lib/a.so] = %v, want [x y]", syms["/lib/a.so"])
	}
	if !reflect.DeepEqual(syms["/lib/b.so"], []string{"z"}) {
		t.Errorf("syms[/lib/b.so] = %v, want [z]", syms["/lib/b.so"])
	}

	if !reflect.DeepEqual(cookies["/lib/a.so"], []uint64{0, 1}) {
		t.Errorf("cookies[/lib/a.so] = %v, want [0 1]", cookies["/lib/a.so"])
	}
	if !reflect.DeepEqual(cookies["/lib/b.so"], []uint64{2}) {
		t.Errorf("cookies[/lib/b.so] = %v, want [2]", cookies["/lib/b.so"])
	}
}

func TestFlattenFuncs_DeterministicAcrossRuns(t *testing.T) {
	funcs := map[string][]string{
		"/lib/c.so": {"one"},
		"/lib/a.so": {"two", "three"},
		"/lib/b.so": {"four"},
	}
	refs1, _, _ := flattenFuncs(funcs)
	refs2, _, _ := flattenFuncs(funcs)
	if !reflect.DeepEqual(refs1, refs2) {
		t.Errorf("flattenFuncs is not deterministic: %v vs %v", refs1, refs2)
	}
}

// A nil/empty funcs map supports pure runtime dynamic-library tracing (a
// binary with no static functions of its own, relying entirely on dlopen'd
// plugins) — flattenFuncs must handle it directly (ranging a nil map is
// legal Go) without any normalization step.
func TestFlattenFuncs_HandlesNilAndEmpty(t *testing.T) {
	for name, in := range map[string]map[string][]string{
		"nil":   nil,
		"empty": {},
	} {
		refs, syms, cookies := flattenFuncs(in)
		if len(refs) != 0 || len(syms) != 0 || len(cookies) != 0 {
			t.Errorf("flattenFuncs(%s): got refs=%v syms=%v cookies=%v, want all empty", name, refs, syms, cookies)
		}
	}
}

func TestClipToCapacity(t *testing.T) {
	cases := []struct {
		name        string
		syms        []string
		alreadyUsed int
		capacity    uint32
		wantLen     int
		wantWarn    bool
	}{
		{"fits entirely", []string{"a", "b"}, 0, 10, 2, false},
		{"exact fit", []string{"a", "b"}, 8, 10, 2, false},
		{"needs clipping", []string{"a", "b", "c"}, 8, 10, 2, true},
		{"already exhausted", []string{"a", "b"}, 10, 10, 0, true},
		{"over capacity", []string{"a", "b"}, 11, 10, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, warn := clipToCapacity(c.syms, c.alreadyUsed, c.capacity)
			if len(got) != c.wantLen {
				t.Errorf("clipToCapacity: got %d syms, want %d", len(got), c.wantLen)
			}
			if warn != c.wantWarn {
				t.Errorf("clipToCapacity: warn = %v, want %v", warn, c.wantWarn)
			}
		})
	}
}

func TestTracer_MatchesFilter(t *testing.T) {
	tr := &Tracer{
		filter: &funkutil.FuncFilter{
			Include: regexp.MustCompile(`^plugin_`),
			Exclude: regexp.MustCompile(`_internal$`),
		},
	}
	cases := []struct {
		name string
		want bool
	}{
		{"plugin_func", true},
		{"plugin_func_internal", false},
		{"other_func", false},
	}
	for _, c := range cases {
		if got := tr.filter.Match(c.name); got != c.want {
			t.Errorf("filter.Match(%q) = %v, want %v", c.name, got, c.want)
		}
	}

	// No filter configured (nil *FuncFilter): everything passes.
	tr2 := &Tracer{}
	if !tr2.filter.Match("anything") {
		t.Error("filter.Match with no filter configured should pass everything")
	}
}

// findLibcPath/hasSymbol against the real system libc — skipped rather
// than failed on environments without one (e.g. musl-based).
func testLibcPath(t *testing.T) string {
	t.Helper()
	path, err := findLibcPath(uint32(os.Getpid()))
	if err != nil {
		t.Skipf("no libc/libdl with dlopen found in this environment: %v", err)
	}
	return path
}

func TestFindLibcPath_HasDlopen(t *testing.T) {
	path := testLibcPath(t)
	if !hasSymbol(path, "dlopen") {
		t.Errorf("findLibcPath returned %s, which lacks a dlopen symbol", path)
	}
}

func TestHasSymbol(t *testing.T) {
	path := testLibcPath(t)
	if hasSymbol(path, "this_symbol_does_not_exist_xyz123") {
		t.Errorf("hasSymbol(%s, bogus) = true, want false", path)
	}
	if hasSymbol("/nonexistent/path/to/nothing.so", "dlopen") {
		t.Error("hasSymbol on a nonexistent path = true, want false")
	}
}

func TestGetSharedLibrarySymbols(t *testing.T) {
	path := testLibcPath(t)
	syms, err := getSharedLibrarySymbols(path)
	if err != nil {
		t.Fatalf("getSharedLibrarySymbols(%s): %v", path, err)
	}
	if len(syms) == 0 {
		t.Fatalf("getSharedLibrarySymbols(%s) returned no symbols", path)
	}
	found := slices.Contains(syms, "dlopen")
	if !found {
		t.Errorf("expected dlopen among discovered symbols in %s", path)
	}
}

func TestGetMappedSharedLibraries_NoDuplicates(t *testing.T) {
	libs, err := getMappedSharedLibraries(uint32(os.Getpid()))
	if err != nil {
		t.Fatalf("getMappedSharedLibraries: %v", err)
	}
	seen := make(map[string]bool)
	for _, l := range libs {
		if seen[l] {
			t.Errorf("duplicate library path returned: %s", l)
		}
		seen[l] = true
	}
}

func TestGetMappedSharedLibraries_BadPID(t *testing.T) {
	// A PID that cannot exist: /proc/<pid>/maps is absent → ReadFile error.
	if _, err := getMappedSharedLibraries(^uint32(0)); err == nil {
		t.Error("getMappedSharedLibraries on a nonexistent pid should error")
	}
}

func TestDebugLog(t *testing.T) {
	t.Setenv("FUNKOVERAGE_DEBUG", "")
	if out := captureStderr(t, func() { debugLog("silent %d", 1) }); out != "" {
		t.Errorf("debugLog without FUNKOVERAGE_DEBUG wrote %q, want nothing", out)
	}

	t.Setenv("FUNKOVERAGE_DEBUG", "1")
	out := captureStderr(t, func() { debugLog("loud %d", 42) })
	if !strings.Contains(out, "loud 42") {
		t.Errorf("debugLog with FUNKOVERAGE_DEBUG = %q, want it to contain %q", out, "loud 42")
	}
}

func TestWarnCapacityExhausted_Once(t *testing.T) {
	tr := &Tracer{seenCapacity: 128}
	out := captureStderr(t, func() {
		tr.warnCapacityExhausted()
		tr.warnCapacityExhausted()
	})
	if n := strings.Count(out, "capacity"); n != 1 {
		t.Errorf("warnCapacityExhausted warned %d times, want exactly 1: %q", n, out)
	}
	if !tr.capacityWarned {
		t.Error("capacityWarned should be set after the first warning")
	}
}
