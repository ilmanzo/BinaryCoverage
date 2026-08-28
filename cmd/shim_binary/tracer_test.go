package main

import (
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"funkoverage/internal/funkutil"
	"funkoverage/internal/testutil"
)

func TestFlattenFuncs_StableOrderAndCookies(t *testing.T) {
	funcs := map[string]funkutil.ImageFuncs{
		"/lib/b.so": {Names: []string{"z"}},
		"/lib/a.so": {
			BuildID: "beef",
			Names:   []string{"x", "y"},
			Offsets: []funkutil.FuncAddr{{Name: "hidden", Offset: 0x40}},
		},
	}
	refs, plans := flattenFuncs(funcs)

	// Address-attached functions share the one cookie space with the
	// name-attached ones, and follow them within their image.
	wantRefs := []FuncRef{
		{Image: "/lib/a.so", Name: "x"},
		{Image: "/lib/a.so", Name: "y"},
		{Image: "/lib/a.so", Name: "hidden"},
		{Image: "/lib/b.so", Name: "z"},
	}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("refs: got %v, want %v", refs, wantRefs)
	}

	a := plans["/lib/a.so"]
	if a.buildID != "beef" {
		t.Errorf("plans[/lib/a.so].buildID = %q, want beef", a.buildID)
	}
	if !reflect.DeepEqual(a.names, []string{"x", "y"}) || !reflect.DeepEqual(a.nameCookies, []uint64{0, 1}) {
		t.Errorf("plans[/lib/a.so] names = %v/%v, want [x y]/[0 1]", a.names, a.nameCookies)
	}
	if !reflect.DeepEqual(a.addrs, []uint64{0x40}) || !reflect.DeepEqual(a.addrCookies, []uint64{2}) {
		t.Errorf("plans[/lib/a.so] addrs = %v/%v, want [64]/[2]", a.addrs, a.addrCookies)
	}

	b := plans["/lib/b.so"]
	if !reflect.DeepEqual(b.names, []string{"z"}) || !reflect.DeepEqual(b.nameCookies, []uint64{3}) {
		t.Errorf("plans[/lib/b.so] = %v/%v, want [z]/[3]", b.names, b.nameCookies)
	}
	if len(b.addrs) != 0 {
		t.Errorf("plans[/lib/b.so].addrs = %v, want none", b.addrs)
	}
}

func TestFlattenFuncs_DeterministicAcrossRuns(t *testing.T) {
	funcs := map[string]funkutil.ImageFuncs{
		"/lib/c.so": {Names: []string{"one"}},
		"/lib/a.so": {Names: []string{"two", "three"}},
		"/lib/b.so": {Names: []string{"four"}},
	}
	refs1, _ := flattenFuncs(funcs)
	refs2, _ := flattenFuncs(funcs)
	if !reflect.DeepEqual(refs1, refs2) {
		t.Errorf("flattenFuncs is not deterministic: %v vs %v", refs1, refs2)
	}
}

// A nil/empty funcs map supports pure runtime dynamic-library tracing (a
// binary with no static functions of its own, relying entirely on dlopen'd
// plugins) — flattenFuncs must handle it directly (ranging a nil map is
// legal Go) without any normalization step.
func TestFlattenFuncs_HandlesNilAndEmpty(t *testing.T) {
	for name, in := range map[string]map[string]funkutil.ImageFuncs{
		"nil":   nil,
		"empty": {},
	} {
		refs, plans := flattenFuncs(in)
		if len(refs) != 0 || len(plans) != 0 {
			t.Errorf("flattenFuncs(%s): got refs=%v plans=%v, want both empty", name, refs, plans)
		}
	}
}

// TestAttachBisect covers the recovery that keeps a couple of unprobeable
// functions from costing an entire library: UprobeMulti rejects a whole batch
// over one bad entry, so the batch is halved until the failures are isolated.
func TestAttachBisect(t *testing.T) {
	for _, tc := range []struct {
		name        string
		n           int
		bad         []int
		wantDropped []int
	}{
		{"all good", 8, nil, nil},
		{"one bad", 8, []int{3}, []int{3}},
		{"first and last", 8, []int{0, 7}, []int{0, 7}},
		{"odd length", 7, []int{5}, []int{5}},
		{"single bad probe", 1, []int{0}, []int{0}},
		{"all bad", 4, []int{0, 1, 2, 3}, []int{0, 1, 2, 3}},
		{"empty", 0, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := make(map[int]bool, len(tc.bad))
			for _, i := range tc.bad {
				bad[i] = true
			}
			attached := make([]bool, tc.n)
			var dropped []int
			wantAttached := tc.n - len(tc.bad)

			got := attachBisect(tc.n, func(lo, hi int) error {
				for i := lo; i < hi; i++ {
					if bad[i] {
						return errors.New("operation not supported")
					}
				}
				for i := lo; i < hi; i++ {
					if attached[i] {
						t.Errorf("probe %d attached twice", i)
					}
					attached[i] = true
				}
				return nil
			}, func(i int, err error) {
				dropped = append(dropped, i)
			})

			if got != wantAttached {
				t.Errorf("attached %d, want %d", got, wantAttached)
			}
			if !reflect.DeepEqual(dropped, tc.wantDropped) {
				t.Errorf("dropped %v, want %v", dropped, tc.wantDropped)
			}
			for i, ok := range attached {
				if ok == bad[i] {
					t.Errorf("probe %d: attached=%v, bad=%v", i, ok, bad[i])
				}
			}
		})
	}
}

// A batch that never fails must attach in exactly one call — bisection is a
// recovery path, not the normal one, and 7000 individual attach syscalls per
// library would be a real cost.
func TestAttachBisect_NoSplitWhenHealthy(t *testing.T) {
	calls := 0
	if got := attachBisect(1000, func(lo, hi int) error { calls++; return nil }, func(int, error) {
		t.Error("drop called for a healthy batch")
	}); got != 1000 {
		t.Errorf("attached %d, want 1000", got)
	}
	if calls != 1 {
		t.Errorf("made %d attach calls, want 1", calls)
	}
}

func TestSubslice_PreservesNil(t *testing.T) {
	// attachProbes picks name mode vs address mode by which slice is nil, so a
	// nil that comes back as an empty slice silently attaches the wrong thing.
	if got := subslice[string](nil, 0, 0); got != nil {
		t.Errorf("subslice(nil) = %v, want nil", got)
	}
	if got := subslice([]string{"a", "b", "c"}, 1, 3); !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("subslice = %v, want [b c]", got)
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
	if out := testutil.CaptureOutput(t, &os.Stderr, func() { debugLog("silent %d", 1) }); out != "" {
		t.Errorf("debugLog without FUNKOVERAGE_DEBUG wrote %q, want nothing", out)
	}

	t.Setenv("FUNKOVERAGE_DEBUG", "1")
	out := testutil.CaptureOutput(t, &os.Stderr, func() { debugLog("loud %d", 42) })
	if !strings.Contains(out, "loud 42") {
		t.Errorf("debugLog with FUNKOVERAGE_DEBUG = %q, want it to contain %q", out, "loud 42")
	}
}

func TestWarnCapacityExhausted_Once(t *testing.T) {
	tr := &Tracer{seenCapacity: 128}
	out := testutil.CaptureOutput(t, &os.Stderr, func() {
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

// TestAttachPlanVerify_KeepsCookiesPaired checks the name filter against a
// stripped library: an unresolvable name is dropped and every survivor keeps
// the cookie flattenFuncs handed it. Getting that pairing wrong misattributes
// every subsequent function in the image.
func TestAttachPlanVerify_KeepsCookiesPaired(t *testing.T) {
	lib := buildStrippedLib(t)

	// local_func survives stripping only in the debug file, which is exactly
	// the name enumeration hands the tracer and the mapped file cannot
	// resolve.
	plan := &attachPlan{
		names:       []string{"public_func", "local_func"},
		nameCookies: []uint64{10, 11},
	}
	got := plan.verify(lib)
	if !reflect.DeepEqual(got.names, []string{"public_func"}) {
		t.Errorf("names: got %v, want [public_func]", got.names)
	}
	if !reflect.DeepEqual(got.nameCookies, []uint64{10}) {
		t.Errorf("cookies: got %v, want [10]", got.nameCookies)
	}
}

// The address batch is only valid for the exact build it was computed from:
// a stale offset puts a uprobe mid-instruction and kills the target with
// SIGILL, so a build-id mismatch must drop it while leaving names alone.
func TestAttachPlanVerify_BuildIDGuardsAddresses(t *testing.T) {
	lib := buildStrippedLib(t)
	f, err := elf.Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	buildID, err := funkutil.BuildID(f)
	f.Close()
	if err != nil {
		t.Skipf("no build-id in the test library: %v", err)
	}

	for _, tc := range []struct {
		name      string
		buildID   string
		wantAddrs []uint64
	}{
		{"match", buildID, []uint64{0x1000}},
		{"mismatch", "0000000000000000000000000000000000000000", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := &attachPlan{
				buildID:     tc.buildID,
				names:       []string{"public_func"},
				nameCookies: []uint64{0},
				addrs:       []uint64{0x1000},
				addrCookies: []uint64{1},
			}
			got := plan.verify(lib)
			if !reflect.DeepEqual(got.addrs, tc.wantAddrs) {
				t.Errorf("addrs = %v, want %v", got.addrs, tc.wantAddrs)
			}
			if !reflect.DeepEqual(got.names, []string{"public_func"}) {
				t.Errorf("names = %v, want [public_func] regardless of the address verdict", got.names)
			}
		})
	}
}

// An image whose symbol tables are gone entirely tells us nothing about which
// names are attachable, so verify must not read that as "none are" and drop
// the whole batch — leave it to UprobeMulti, which reports the real reason.
func TestAttachPlanVerify_NoSymbolTables(t *testing.T) {
	if _, err := exec.LookPath("objcopy"); err != nil {
		t.Skip("objcopy not found")
	}
	lib := buildStrippedLib(t)
	if out, err := exec.Command("objcopy", "--remove-section=.dynsym", "--remove-section=.symtab", lib).CombinedOutput(); err != nil {
		t.Fatalf("objcopy: %v\n%s", err, out)
	}

	plan := &attachPlan{names: []string{"public_func"}, nameCookies: []uint64{3}}
	got := plan.verify(lib)
	if !reflect.DeepEqual(got.names, plan.names) || !reflect.DeepEqual(got.nameCookies, plan.nameCookies) {
		t.Errorf("got %v/%v, want the plan passed through unchanged", got.names, got.nameCookies)
	}
}

// buildStrippedLib compiles a shared library with one exported and one static
// function and strips it, leaving .dynsym only — the shape of a packaged
// system library whose .symtab lives in a separate debuginfo file.
func buildStrippedLib(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"gcc", "strip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "lib.c")
	code := "static int local_func(void) { return 1; }\nint public_func(void) { return local_func(); }\n"
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(tmp, "libtest.so")
	if out, err := exec.Command("gcc", "-shared", "-fPIC", "-Wl,--build-id", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command("strip", lib).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}
	return lib
}

// A file the ELF reader cannot make sense of is passed through untouched, so
// UprobeMulti reports the real reason instead of "no attachable symbols".
func TestAttachPlanVerify_PassesThroughUnreadable(t *testing.T) {
	plan := &attachPlan{names: []string{"a"}, nameCookies: []uint64{7}}
	if got := plan.verify(t.TempDir() + "/absent"); got != plan {
		t.Errorf("verify on an unreadable image = %+v, want the plan unchanged", got)
	}
}
