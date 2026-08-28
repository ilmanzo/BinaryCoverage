package funkutil

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// buildTestLib compiles a small shared library with one exported and one
// static (local) function, unstripped so both .symtab and .dynsym are
// present — enough to exercise SymtabFunctions' union and dedup without
// needing to fabricate the harder-to-reproduce address-collision case
// (covered live, against the real system libc, by
// cmd/shim_binary's TestGetSharedLibrarySymbols).
func buildTestLib(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "lib.c")
	code := "static int local_func(void) { return 1; }\nint public_func(void) { return local_func(); }\n"
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(tmp, "libtest.so")
	if out, err := exec.Command("gcc", "-shared", "-fPIC", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	return lib
}

func TestSymtabFunctions(t *testing.T) {
	lib := buildTestLib(t)
	f, err := elf.Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	funcs := SymtabFunctions(f, FuncIsRelevant)
	raw := rawOf(funcs)
	if !slices.Contains(raw, "public_func") {
		t.Errorf("expected public_func among %v", raw)
	}
	if !slices.Contains(raw, "local_func") {
		t.Errorf("expected local_func (from .symtab) among %v", raw)
	}

	// No duplicates: local_func and public_func each appear once even
	// though both .symtab and .dynsym were unioned.
	seen := make(map[string]int)
	for _, name := range raw {
		seen[name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("%s appeared %d times, want 1", name, count)
		}
	}
}

func rawOf(funcs []Func) []string {
	out := make([]string, len(funcs))
	for i, fn := range funcs {
		out[i] = fn.Raw
	}
	return out
}

// Demangled must be the exact string the keep predicate judged: callers now
// use it instead of demangling again, so a mismatch would make the functions
// log disagree with the filter that admitted the function.
func TestSymtabFunctions_DemangledMatchesPredicateInput(t *testing.T) {
	if _, err := exec.LookPath("g++"); err != nil {
		t.Skip("g++ not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "lib.cpp")
	if err := os.WriteFile(src, []byte("int cpp_func(int a) { return a; }\n"), 0644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(tmp, "libcpp.so")
	if out, err := exec.Command("g++", "-shared", "-fPIC", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	f, err := elf.Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	saw := make(map[string]struct{})
	funcs := SymtabFunctions(f, func(demangled string) bool {
		saw[demangled] = struct{}{}
		return true
	})
	var found bool
	for _, fn := range funcs {
		if _, ok := saw[fn.Demangled]; !ok {
			t.Errorf("%s: returned Demangled %q was never offered to keep", fn.Raw, fn.Demangled)
		}
		if fn.Raw == "_Z8cpp_funci" {
			found = true
			if fn.Demangled != "cpp_func(int)" {
				t.Errorf("Demangled = %q, want cpp_func(int)", fn.Demangled)
			}
		}
	}
	if !found {
		t.Errorf("mangled _Z8cpp_funci not returned, got %v", rawOf(funcs))
	}
}

func TestSymtabFunctions_KeepPredicate(t *testing.T) {
	lib := buildTestLib(t)
	f, err := elf.Open(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	funcs := SymtabFunctions(f, func(demangled string) bool { return demangled == "public_func" })
	if len(funcs) != 1 || funcs[0].Raw != "public_func" {
		t.Errorf("keep predicate not applied: got %v, want [public_func]", funcs)
	}
}

// TestResolvableFuncNames mirrors the stripped-library case the shim's
// symbol filter exists for: a static function lives only in .symtab, so
// stripping the library makes the name unresolvable even though enumeration
// (which reads the debug file) still knows about it.
func TestResolvableFuncNames(t *testing.T) {
	lib := buildTestLib(t)

	names := resolvableNames(t, lib)
	for _, want := range []string{"public_func", "local_func"} {
		if _, ok := names[want]; !ok {
			t.Errorf("unstripped: %s not resolvable, got %v", want, names)
		}
	}

	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}
	if out, err := exec.Command("strip", lib).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}

	names = resolvableNames(t, lib)
	if _, ok := names["public_func"]; !ok {
		t.Errorf("stripped: public_func (.dynsym) not resolvable, got %v", names)
	}
	if _, ok := names["local_func"]; ok {
		t.Error("stripped: local_func resolvable, but .symtab is gone")
	}
}

func resolvableNames(t *testing.T, path string) map[string]struct{} {
	t.Helper()
	f, err := elf.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return ResolvableFuncNames(f)
}
