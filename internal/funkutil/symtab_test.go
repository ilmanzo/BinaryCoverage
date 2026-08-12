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
	if !slices.Contains(funcs, "public_func") {
		t.Errorf("expected public_func among %v", funcs)
	}
	if !slices.Contains(funcs, "local_func") {
		t.Errorf("expected local_func (from .symtab) among %v", funcs)
	}

	// No duplicates: local_func and public_func each appear once even
	// though both .symtab and .dynsym were unioned.
	seen := make(map[string]int)
	for _, name := range funcs {
		seen[name]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Errorf("%s appeared %d times, want 1", name, count)
		}
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
	if len(funcs) != 1 || funcs[0] != "public_func" {
		t.Errorf("keep predicate not applied: got %v, want [public_func]", funcs)
	}
}
