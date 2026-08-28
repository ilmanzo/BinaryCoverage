package main

import (
	"bytes"
	"debug/elf"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"funkoverage/internal/funkutil"
	"funkoverage/internal/testutil"
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

// --- parseInterspersed tests ---

func TestParseInterspersed(t *testing.T) {
	newFS := func() (*flag.FlagSet, *string) {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		formats := fs.String("formats", "default", "")
		return fs, formats
	}

	cases := []struct {
		name        string
		args        []string
		wantPos     []string
		wantFormats string
	}{
		{"no flags", []string{"a", "b"}, []string{"a", "b"}, "default"},
		{"flag before positionals", []string{"--formats", "xml", "a", "b"}, []string{"a", "b"}, "xml"},
		{"flag after positionals", []string{"a", "b", "--formats", "xml"}, []string{"a", "b"}, "xml"},
		{"flag interleaved", []string{"a", "--formats", "xml", "b"}, []string{"a", "b"}, "xml"},
		{"flag with = syntax", []string{"a", "--formats=xml", "b"}, []string{"a", "b"}, "xml"},
		{"-- terminates flag parsing", []string{"a", "--", "--formats", "xml"}, []string{"a", "--formats", "xml"}, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs, formats := newFS()
			pos, err := parseInterspersed(fs, tc.args)
			if err != nil {
				t.Fatalf("parseInterspersed: %v", err)
			}
			if !slices.Equal(pos, tc.wantPos) {
				t.Errorf("positional = %v, want %v", pos, tc.wantPos)
			}
			if *formats != tc.wantFormats {
				t.Errorf("formats = %q, want %q", *formats, tc.wantFormats)
			}
		})
	}

	t.Run("unknown flag errors", func(t *testing.T) {
		fs, _ := newFS()
		if _, err := parseInterspersed(fs, []string{"a", "--bogus"}); err == nil {
			t.Error("expected error for unknown flag, got nil")
		}
	})
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

	// Both the globalDebugRoot-relative and globalDebugRoot/.dwz/<basename>
	// branches must honor globalDebugRoot, not a hardcoded /usr/lib/debug —
	// otherwise they're untestable and inconsistent with every other
	// resolver in this file, which all respect the override.
	orig := globalDebugRoot
	globalDebugRoot = filepath.Join(tmp, "debugroot")
	defer func() { globalDebugRoot = orig }()

	altPath := "/some/alt/path.debug"
	relPath := filepath.Join(globalDebugRoot, altPath)
	if err := os.MkdirAll(filepath.Dir(relPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findDebugFile(altPath); got != relPath {
		t.Errorf("findDebugFile(globalDebugRoot-relative) = %q, want %q", got, relPath)
	}

	dwzAltPath := "/another/alt/pkg-1.2.3.x86_64"
	dwzPath := filepath.Join(globalDebugRoot, ".dwz", filepath.Base(dwzAltPath))
	if err := os.MkdirAll(filepath.Dir(dwzPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dwzPath, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := findDebugFile(dwzAltPath); got != dwzPath {
		t.Errorf("findDebugFile(globalDebugRoot/.dwz) = %q, want %q", got, dwzPath)
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
	if got, err := locateExternalDebugForMerge(bin, bin); err != nil || got != "" {
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
	got, err := locateExternalDebugForMerge(bin, bin)
	if err != nil {
		t.Fatalf("locateExternalDebugForMerge: %v", err)
	}
	if got != debugFile {
		t.Errorf("locateExternalDebugForMerge = %q, want %q", got, debugFile)
	}
}

// TestLocateExternalDebugForMerge_DebugLink verifies the .gnu_debuglink
// fallback: when build-id resolution fails (e.g. a missing/stale
// .build-id symlink), the debug file is still found via the standard
// <debugRoot>/<canonical-dir-of-binary>/<debuglink-basename> convention
// used by rpm/dpkg debuginfo packages.
func TestLocateExternalDebugForMerge_DebugLink(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("objcopy"); err != nil {
		t.Skip("objcopy not found")
	}
	tmp := t.TempDir()
	orig := globalDebugRoot
	globalDebugRoot = filepath.Join(tmp, "debugroot")
	defer func() { globalDebugRoot = orig }()

	binDir := filepath.Join(tmp, "usr", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(binDir, "prog")
	if out, err := exec.Command("gcc", "-g", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	realBinDir, err := filepath.EvalSymlinks(binDir)
	if err != nil {
		t.Fatal(err)
	}

	debugDir := filepath.Join(globalDebugRoot, realBinDir)
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(debugDir, "prog.debug")
	if out, err := exec.Command("objcopy", "--only-keep-debug", bin, debugFile).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --only-keep-debug: %v\n%s", err, out)
	}
	if out, err := exec.Command("objcopy", "--strip-debug", "--add-gnu-debuglink="+debugFile, bin).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --add-gnu-debuglink: %v\n%s", err, out)
	}

	got, err := locateExternalDebugForMerge(bin, bin)
	if err != nil {
		t.Fatalf("locateExternalDebugForMerge: %v", err)
	}
	if got != debugFile {
		t.Errorf("locateExternalDebugForMerge via .gnu_debuglink = %q, want %q", got, debugFile)
	}
}

// TestExternalDebugPath_IgnoresDwzFile verifies that a same-named file
// sitting in a .dwz/-style directory is never picked up as a substitute
// debug source: it can never provide a symtab or a standalone-parseable
// .debug_info (see issue #128), so treating it as found is worse than
// reporting no debug info at all.
func TestExternalDebugPath_IgnoresDwzFile(t *testing.T) {
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
	bin := filepath.Join(tmp, "cpupower")
	if out, err := exec.Command("gcc", "-g", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	if out, err := exec.Command("strip", "--strip-all", bin).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}

	dwzDir := filepath.Join(tmp, ".dwz")
	if err := os.MkdirAll(dwzDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dwzDir, "cpupower-1.0-1.x86_64"), []byte("not a real debug file"), 0644); err != nil {
		t.Fatal(err)
	}

	if got := externalDebugPath(bin); got != "" {
		t.Errorf("externalDebugPath picked up a .dwz/ file as a debug source: %q, want \"\"", got)
	}
	if got, err := locateExternalDebugForMerge(bin, bin); err != nil || got != "" {
		t.Errorf("locateExternalDebugForMerge picked up a .dwz/ file as a debug source: (%q, %v), want (\"\", nil)", got, err)
	}
}

// TestResolveDebugFile_SkipIfEmbedded verifies the one behavioral
// difference between the two resolveDebugFile callers: locateExternalDebugForMerge
// (skipIfEmbedded=true) has nothing useful to merge into a binary that
// already carries embedded DWARF, so it returns "". externalDebugPath
// (skipIfEmbedded=false) still returns a matching external candidate
// regardless — enumeration wants it as a fallback candidate even when
// static enumeration might succeed via the binary's own DWARF instead.
func TestResolveDebugFile_SkipIfEmbedded(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	orig := globalDebugRoot
	globalDebugRoot = tmp
	defer func() { globalDebugRoot = orig }()

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	// -g and no strip: bin keeps its own embedded DWARF (.debug_info etc.).
	bin := filepath.Join(tmp, "embedded")
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

	dir := filepath.Join(tmp, ".build-id", buildID[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(dir, buildID[2:]+".debug")
	if err := os.WriteFile(debugFile, []byte("dummy debug info"), 0644); err != nil {
		t.Fatal(err)
	}

	if got, err := locateExternalDebugForMerge(bin, bin); err != nil || got != "" {
		t.Errorf("locateExternalDebugForMerge(embedded DWARF) = (%q, %v), want (\"\", nil)", got, err)
	}
	if got := externalDebugPath(bin); got != debugFile {
		t.Errorf("externalDebugPath(embedded DWARF) = %q, want %q", got, debugFile)
	}
}

// --- mergeLibraryDebugInfo / restoreLibraryBackups tests ---

// TestMergeLibraryDebugInfo verifies that a library gets its external debug
// info merged in place (so uprobe attach can resolve names that only exist
// in the debug file's symtab), that a backup of the pre-merge original is
// recorded, and that restoreLibraryBackups puts the original back exactly.
// buildStrippedLibFixture compiles a small shared library with a LOCAL
// (static) function and a PUBLIC function, strips it the way real distro
// debuginfo packaging does (--strip-all, keeping only .dynsym), and points
// it at an external debug file via .gnu_debuglink under globalDebugRoot.
// lib_local_func is deliberately `static`: LOCAL binding, present in
// .symtab but never in .dynsym, so it vanishes from a fully-stripped
// runtime .so exactly like GMP's internal mpn_* helpers do (the real bug
// this mirrors). Returns the library path and its stripped (pre-merge)
// bytes. Caller must set globalDebugRoot and check for gcc/strip/objcopy
// first.
func buildStrippedLibFixture(t *testing.T, libDir, name string) (lib string, strippedBytes []byte) {
	t.Helper()
	if err := os.MkdirAll(libDir, 0755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(libDir, name+".c")
	code := "static int lib_local_func(void) { return 42; }\nint lib_public_func(void) { return lib_local_func(); }\n"
	if err := os.WriteFile(src, []byte(code), 0644); err != nil {
		t.Fatal(err)
	}
	lib = filepath.Join(libDir, name)
	if out, err := exec.Command("gcc", "-shared", "-fPIC", "-g", "-o", lib, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	realLibDir, err := filepath.EvalSymlinks(libDir)
	if err != nil {
		t.Fatal(err)
	}
	debugDir := filepath.Join(globalDebugRoot, realLibDir)
	if err := os.MkdirAll(debugDir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(debugDir, name+".debug")
	if out, err := exec.Command("objcopy", "--only-keep-debug", lib, debugFile).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --only-keep-debug: %v\n%s", err, out)
	}
	// strip --strip-all (not --strip-debug) matches real distro debuginfo
	// packaging: the shipped runtime .so keeps only .dynsym, dropping the
	// full .symtab (and with it every LOCAL/static symbol) entirely.
	if out, err := exec.Command("strip", "--strip-all", lib).CombinedOutput(); err != nil {
		t.Fatalf("strip --strip-all: %v\n%s", err, out)
	}
	if out, err := exec.Command("objcopy", "--add-gnu-debuglink="+debugFile, lib).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --add-gnu-debuglink: %v\n%s", err, out)
	}
	strippedBytes, err = os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if funcs := symtabFunctions(lib, nil); slices.Contains(funcs, "lib_local_func") {
		t.Fatalf("test setup: lib_local_func should NOT be resolvable pre-merge, got %v", funcs)
	}
	return lib, strippedBytes
}

func TestMergeLibraryDebugInfo(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}
	if _, err := exec.LookPath("eu-unstrip"); err != nil {
		t.Skip("eu-unstrip not found")
	}
	tmp := t.TempDir()
	safeBinDir := filepath.Join(tmp, "safebin")
	t.Setenv("SAFE_BIN_DIR", safeBinDir)
	orig := globalDebugRoot
	globalDebugRoot = filepath.Join(tmp, "debugroot")
	defer func() { globalDebugRoot = orig }()

	libDir := filepath.Join(tmp, "usr", "lib64")
	lib, strippedBytes := buildStrippedLibFixture(t, libDir, "libdemo.so")

	funcs := map[string][]string{
		lib:          {"lib_local_func"},
		"/some/main": {"main_func"},
	}
	backups := mergeLibraryDebugInfo(funcs, "/some/main")

	backupPath, ok := backups[lib]
	if !ok {
		t.Fatalf("mergeLibraryDebugInfo did not back up %s; backups = %v", lib, backups)
	}
	backedUp, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backedUp) != string(strippedBytes) {
		t.Error("backup does not match the pre-merge (stripped) library bytes")
	}

	merged, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if len(merged) <= len(strippedBytes) {
		t.Errorf("merged library (%d bytes) should be larger than stripped (%d bytes)", len(merged), len(strippedBytes))
	}
	if funcs := symtabFunctions(lib, nil); !slices.Contains(funcs, "lib_local_func") {
		t.Errorf("merged library should now expose lib_local_func directly, got %v", funcs)
	}

	// Restore, via the same sidecar install() would write.
	safePath := filepath.Join(safeBinDir, "some-target")
	if err := os.MkdirAll(safeBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := funkutil.WriteLibBackups(safePath, backups); err != nil {
		t.Fatal(err)
	}
	restoreLibraryBackups(safePath)

	restored, err := os.ReadFile(lib)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(strippedBytes) {
		t.Error("restoreLibraryBackups did not restore the pre-merge (stripped) library bytes")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("backup file %s should be gone after restore, stat err = %v", backupPath, err)
	}
}

// TestMergeLibraryDebugInfo_SkipsMainImage verifies the main target binary
// (already merged separately by install()) is never re-processed here.
func TestMergeLibraryDebugInfo_SkipsMainImage(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("SAFE_BIN_DIR", filepath.Join(tmp, "safebin"))

	main := filepath.Join(tmp, "main")
	if err := os.WriteFile(main, []byte("not a real elf, must not be touched"), 0755); err != nil {
		t.Fatal(err)
	}
	funcs := map[string][]string{main: {"main_func"}}
	backups := mergeLibraryDebugInfo(funcs, main)
	if len(backups) != 0 {
		t.Errorf("mergeLibraryDebugInfo should skip mainImage, got backups = %v", backups)
	}
}

// TestMergeLibraryDebugInfo_PreservesSymlink guards against the bug found
// live during the audit-fixes.md Phase 0 baseline sweep: ldd reports a
// library's SONAME path as-is (e.g. /lib64/libgmp.so.10), which is normally
// a symlink to the real versioned file (libgmp.so.10.5.0). Before this fix,
// mergeLibraryDebugInfo's move() onto that path replaced the symlink itself
// with a regular-file copy — confirmed for real via `zypper install --force
// libgmp10` on the VM, which restored the pristine symlink.
func TestMergeLibraryDebugInfo_PreservesSymlink(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}
	if _, err := exec.LookPath("eu-unstrip"); err != nil {
		t.Skip("eu-unstrip not found")
	}
	tmp := t.TempDir()
	safeBinDir := filepath.Join(tmp, "safebin")
	t.Setenv("SAFE_BIN_DIR", safeBinDir)
	orig := globalDebugRoot
	globalDebugRoot = filepath.Join(tmp, "debugroot")
	defer func() { globalDebugRoot = orig }()

	libDir := filepath.Join(tmp, "usr", "lib64")
	realLib, strippedBytes := buildStrippedLibFixture(t, libDir, "libdemo.so.1.0.0")

	// Mirror the real SONAME convention (libgmp.so.10 -> libgmp.so.10.5.0):
	// the symlink is what ldd reports and what mergeLibraryDebugInfo
	// receives as the map key.
	soname := filepath.Join(libDir, "libdemo.so.1")
	if err := os.Symlink(filepath.Base(realLib), soname); err != nil {
		t.Fatal(err)
	}

	backups := mergeLibraryDebugInfo(map[string][]string{soname: {"lib_local_func"}}, "/some/main")

	if _, ok := backups[soname]; ok {
		t.Errorf("backups should be keyed by the resolved real path, not the symlink %s", soname)
	}
	backupPath, ok := backups[realLib]
	if !ok {
		t.Fatalf("mergeLibraryDebugInfo did not back up the resolved path %s; backups = %v", realLib, backups)
	}

	assertSymlinkIntact := func(when string) {
		t.Helper()
		fi, err := os.Lstat(soname)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("SONAME symlink was replaced by a regular file %s merge", when)
		}
		if target, err := os.Readlink(soname); err != nil || target != filepath.Base(realLib) {
			t.Errorf("symlink target %s merge = %q, %v; want %q", when, target, err, filepath.Base(realLib))
		}
	}
	assertSymlinkIntact("after")
	if funcs := symtabFunctions(soname, nil); !slices.Contains(funcs, "lib_local_func") {
		t.Errorf("merged library (opened via the symlink) should expose lib_local_func, got %v", funcs)
	}

	safePath := filepath.Join(safeBinDir, "some-target")
	if err := os.MkdirAll(safeBinDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := funkutil.WriteLibBackups(safePath, backups); err != nil {
		t.Fatal(err)
	}
	restoreLibraryBackups(safePath)

	assertSymlinkIntact("after restore, following")
	restored, err := os.ReadFile(realLib)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(strippedBytes) {
		t.Error("restoreLibraryBackups did not restore the pre-merge (stripped) library bytes")
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Errorf("backup file %s should be gone after restore, stat err = %v", backupPath, err)
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

	// A short file containing a genuine prefix of the ELF magic (fewer than
	// 4 bytes) must not be misread via a padded/zero-filled buffer — it
	// must be treated as "not ELF", not silently compared against garbage.
	shortFile := filepath.Join(tmp, "short")
	if err := os.WriteFile(shortFile, []byte("\x7fE"), 0644); err != nil {
		t.Fatal(err)
	}
	if isELF(shortFile) {
		t.Error("isELF should return false for a file shorter than the magic")
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
	cases := []struct {
		path string
		want logType
	}{
		{"sample_20260101_functions.log", logTypeFunctions},
		{"sample_20260101_called.log", logTypeCalled},
		{"old_pin.log", logTypeUnknown},   // unrecognized → skipped
		{"functions.log", logTypeUnknown}, // suffix only, no prefix → skipped
	}
	for _, c := range cases {
		if got := detectLogType(c.path); got != c.want {
			t.Errorf("detectLogType(%q) = %v, want %v", c.path, got, c.want)
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

	funcs, err := EnumerateFunctions(bin, MainBinaryOnly, nil)
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

// TestEnumerateFunctions_SymlinkAliasing_DuplicateKeys reproduces a
// duplicated-library-report bug seen in real coverage runs: the same
// physical shared library shows up twice in a merged report — once under
// its SONAME symlink name and once under its fully-versioned real filename
// (e.g. "libz.so.1" and "libz.so.1.3.1" side by side, identical function
// counts and coverage data). That happens because an explicit install
// target is keyed by whatever path string was given verbatim, while a
// transitive ldd dependency is keyed by ldd's own arrow-resolved path —
// and EnumerateFunctions never canonicalizes either one before using it as
// a map key, so two spellings of the same file never collapse into one
// image. Currently FAILS; should pass once the fix (canonicalize via
// filepath.EvalSymlinks) lands. See plan.md.
func TestEnumerateFunctions_SymlinkAliasing_DuplicateKeys(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "lib.c")
	if err := os.WriteFile(src, []byte("int lib_func(void) { return 42; }"), 0644); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(tmp, "libfoo.so.1.2.3")
	if out, err := exec.Command("gcc", "-shared", "-fPIC", "-g", "-o", real, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	link := filepath.Join(tmp, "libfoo.so.1") // mimics a SONAME symlink, e.g. libz.so.1 -> libz.so.1.3.1
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	byReal, err := EnumerateFunctions(real, MainBinaryOnly, nil)
	if err != nil {
		t.Fatalf("EnumerateFunctions(real): %v", err)
	}
	byLink, err := EnumerateFunctions(link, MainBinaryOnly, nil)
	if err != nil {
		t.Fatalf("EnumerateFunctions(link): %v", err)
	}
	if len(byReal) != 1 || len(byLink) != 1 {
		t.Fatalf("expected exactly one image per call, got real=%v link=%v", byReal, byLink)
	}
	var realKey, linkKey string
	for k := range byReal {
		realKey = k
	}
	for k := range byLink {
		linkKey = k
	}
	if realKey != linkKey {
		t.Errorf("same physical file keyed differently depending on path spelling: %q vs %q — this is what duplicates a library across images in a merged coverage report", realKey, linkKey)
	}
}

// TestEnumerateFunctions_DifferentVersions_DistinctKeys guards the flip side
// of the symlink-aliasing fix: two distinct real versions of a library
// (different content, different directories) must never collapse into one
// image just because they're both reachable through a same-named SONAME
// symlink (e.g. two library search paths that each have their own
// "libfoo.so.1" pointing at a different real file). Canonicalizing via
// filepath.EvalSymlinks must key by the resolved real path, not the
// symlink's basename.
func TestEnumerateFunctions_DifferentVersions_DistinctKeys(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()

	build := func(dir, body string) string {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(dir, "lib.c")
		if err := os.WriteFile(src, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
		real := filepath.Join(dir, "libfoo.so.real")
		if out, err := exec.Command("gcc", "-shared", "-fPIC", "-g", "-o", real, src).CombinedOutput(); err != nil {
			t.Fatalf("compile: %v\n%s", err, out)
		}
		link := filepath.Join(dir, "libfoo.so.1") // same basename in both dirs, different targets
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		return link
	}

	v1 := build(filepath.Join(tmp, "v1"), "int lib_func(void) { return 1; }")
	v2 := build(filepath.Join(tmp, "v2"), "int lib_func(void) { return 2; }")

	byV1, err := EnumerateFunctions(v1, MainBinaryOnly, nil)
	if err != nil {
		t.Fatalf("EnumerateFunctions(v1): %v", err)
	}
	byV2, err := EnumerateFunctions(v2, MainBinaryOnly, nil)
	if err != nil {
		t.Fatalf("EnumerateFunctions(v2): %v", err)
	}
	if len(byV1) != 1 || len(byV2) != 1 {
		t.Fatalf("expected exactly one image per call, got v1=%v v2=%v", byV1, byV2)
	}
	var v1Key, v2Key string
	for k := range byV1 {
		v1Key = k
	}
	for k := range byV2 {
		v2Key = k
	}
	if v1Key == v2Key {
		t.Errorf("two distinct library versions collapsed to the same key %q — same-named symlinks must not merge different real files", v1Key)
	}
}

// --- copyFile / unstrip mode+ownership preservation tests ---

func TestCopyFilePreservesModeAndOwner(t *testing.T) {
	tmp := t.TempDir()
	metaSrc := filepath.Join(tmp, "meta-src")
	if err := os.WriteFile(metaSrc, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(metaSrc, 0640); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(metaSrc)
	if err != nil {
		t.Fatal(err)
	}

	bytesSrc := filepath.Join(tmp, "bytes-src")
	if err := os.WriteFile(bytesSrc, []byte("distinct content"), 0644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(tmp, "dst")
	if err := copyFile(bytesSrc, dst, info); err != nil {
		t.Fatalf("copyFile: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "distinct content" {
		t.Error("copyFile should copy bytes from src, mode/owner from info")
	}

	dstInfo, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if dstInfo.Mode().Perm() != 0640 {
		t.Errorf("dst mode = %v, want 0640", dstInfo.Mode().Perm())
	}
	srcStat, _ := info.Sys().(*syscall.Stat_t)
	dstStat, _ := dstInfo.Sys().(*syscall.Stat_t)
	if srcStat != nil && dstStat != nil && (srcStat.Uid != dstStat.Uid || srcStat.Gid != dstStat.Gid) {
		t.Errorf("ownership not preserved: src=%d:%d dst=%d:%d", srcStat.Uid, srcStat.Gid, dstStat.Uid, dstStat.Gid)
	}
}

func TestUnstripPreservesMode(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	if _, err := exec.LookPath("strip"); err != nil {
		t.Skip("strip not found")
	}
	if _, err := exec.LookPath("eu-unstrip"); err != nil {
		t.Skip("eu-unstrip not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		mode os.FileMode
	}{
		{"regular", 0755},
		{"setuid", os.ModeSetuid | 0555},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(tmp, "bin_"+tc.name)
			if out, err := exec.Command("gcc", "-g", "-o", bin, src).CombinedOutput(); err != nil {
				t.Fatalf("compile: %v\n%s", err, out)
			}
			debugFile := bin + ".debug"
			if out, err := exec.Command("objcopy", "--only-keep-debug", bin, debugFile).CombinedOutput(); err != nil {
				t.Fatalf("objcopy --only-keep-debug: %v\n%s", err, out)
			}
			if out, err := exec.Command("strip", "--strip-all", bin).CombinedOutput(); err != nil {
				t.Fatalf("strip --strip-all: %v\n%s", err, out)
			}
			if err := os.Chmod(bin, tc.mode); err != nil {
				t.Fatal(err)
			}
			if err := unstrip(bin, debugFile); err != nil {
				t.Fatalf("unstrip: %v", err)
			}
			fi, err := os.Stat(bin)
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode()&preservedModeBits != tc.mode&preservedModeBits {
				t.Errorf("mode after unstrip = %v, want %v", fi.Mode()&preservedModeBits, tc.mode&preservedModeBits)
			}
		})
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

	if err := install(bin, MainBinaryOnly, nil); err != nil {
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

// assertSafeBinDirEmpty fails if anything is left in SAFE_BIN_DIR. Sidecars
// live there too (<safePath>.funcs.json and friends), so this covers both the
// relocated binary and every sidecar keyed on it.
func assertSafeBinDirEmpty(t *testing.T, safeBinDir string) {
	t.Helper()
	entries, err := os.ReadDir(safeBinDir)
	if err != nil {
		return // never created, which is just as empty
	}
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if len(left) > 0 {
		t.Errorf("SAFE_BIN_DIR not clean, leftovers: %v", left)
	}
}

// TestInstall_RollsBackWhenShimCopyFails covers the window opened by
// relocateOriginal: the original binary has been moved out of its own path
// and the shim is not there yet. A failure in that window used to leave the
// path empty — the binary gone from the system — because installMany only
// logs the error and moves on.
//
// Pointing FUNKOVERAGE_SHIM at a directory reaches exactly that window:
// findShimBinary only stats its candidates, so the directory is accepted,
// and the failure surfaces inside copyFile's io.Copy as EISDIR. That works
// the same whether or not the test runs as root, unlike an unreadable file.
func TestInstall_RollsBackWhenShimCopyFails(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	safeBinDir := filepath.Join(tmp, "safe")
	logDir := filepath.Join(tmp, "logs")

	notAShim := filepath.Join(tmp, "not-a-shim")
	if err := os.MkdirAll(notAShim, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FUNKOVERAGE_SHIM", notAShim)
	t.Setenv("SAFE_BIN_DIR", safeBinDir)
	t.Setenv("LOG_DIR", logDir)

	bin := compileDebugBinary(t, tmp, "rollbackbin")
	wantMode := os.ModeSetuid | 0555
	if err := os.Chmod(bin, wantMode); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}

	if err := install(bin, MainBinaryOnly, nil); err == nil {
		t.Fatal("install: expected an error when the shim cannot be copied")
	}

	after, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("original binary not restored to %s: %v", bin, err)
	}
	if !bytes.Equal(before, after) {
		t.Error("restored binary differs from the original")
	}
	if fi, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	} else if fi.Mode()&preservedModeBits != wantMode&preservedModeBits {
		t.Errorf("restored mode = %v, want %v", fi.Mode()&preservedModeBits, wantMode&preservedModeBits)
	}
	assertSafeBinDirEmpty(t, safeBinDir)
}

// TestInstallUninstall_PreservesSetuidMode verifies "nothing changes on the
// original binary except the wrapping": a setuid/setgid binary must keep
// those bits, on the shim copy that replaces it during install AND on the
// restored original after uninstall.
func TestInstallUninstall_PreservesSetuidMode(t *testing.T) {
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

	bin := compileDebugBinary(t, tmp, "setuidbin")
	wantMode := os.ModeSetuid | 0555
	if err := os.Chmod(bin, wantMode); err != nil {
		t.Fatal(err)
	}

	if err := install(bin, MainBinaryOnly, nil); err != nil {
		t.Fatalf("install: %v", err)
	}
	if fi, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	} else if fi.Mode()&preservedModeBits != wantMode&preservedModeBits {
		t.Errorf("shim copy mode = %v, want %v", fi.Mode()&preservedModeBits, wantMode&preservedModeBits)
	}

	if err := uninstall(bin); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	if fi, err := os.Stat(bin); err != nil {
		t.Fatal(err)
	} else if fi.Mode()&preservedModeBits != wantMode&preservedModeBits {
		t.Errorf("restored original mode = %v, want %v", fi.Mode()&preservedModeBits, wantMode&preservedModeBits)
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

	if err := installMany([]string{bin1, bin2}, MainBinaryOnly, nil); err != nil {
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

// --- parseKernelVersion tests ---

func TestParseKernelVersion(t *testing.T) {
	cases := []struct {
		release   string
		wantMajor int
		wantMinor int
		wantErr   bool
	}{
		{"6.6.0-1-default", 6, 6, false},
		{"5.15.0-generic", 5, 15, false},
		{"6.6", 6, 6, false},
		{"6.6.0", 6, 6, false},
		{"6", 0, 0, true},
		{"", 0, 0, true},
		{"a.b.c", 0, 0, true},
	}
	for _, c := range cases {
		maj, min, err := parseKernelVersion(c.release)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseKernelVersion(%q) expected error, got nil", c.release)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseKernelVersion(%q): %v", c.release, err)
			continue
		}
		if maj != c.wantMajor || min != c.wantMinor {
			t.Errorf("parseKernelVersion(%q) = (%d, %d), want (%d, %d)", c.release, maj, min, c.wantMajor, c.wantMinor)
		}
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

	if len(suite.TestCase) != 1 || suite.TestCase[0].Passed == nil {
		t.Fatalf("expected 1 testcase with a Passed result, got %+v", suite.TestCase)
	}
	details := suite.TestCase[0].Passed.Text
	wantTotals := "TOTALS:\n  Total Functions: 3\n  Total Called: 1\n  Average Coverage: 33.33%"
	if !strings.Contains(details, wantTotals) {
		t.Errorf("expected details to contain %q, got:\n%s", wantTotals, details)
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

// TestMoveCrossDevice_PreservesSetuid guards the fidelity moveCrossDevice
// gained by delegating to copyFile (A5): it used to chmod with the plain
// os.Rename-equivalent mode only, never touching ownership, and B2's mode
// fix landed on unstrip/copyFile without this cross-device path — verify
// it inherited the fix rather than needing its own.
func TestMoveCrossDevice_PreservesSetuid(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	if err := os.WriteFile(src, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	wantMode := os.ModeSetuid | 0555
	if err := os.Chmod(src, wantMode); err != nil {
		t.Fatal(err)
	}
	if err := moveCrossDevice(src, dst); err != nil {
		t.Fatalf("moveCrossDevice: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&preservedModeBits != wantMode&preservedModeBits {
		t.Errorf("mode after moveCrossDevice = %v, want %v", fi.Mode()&preservedModeBits, wantMode&preservedModeBits)
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

	filter, _ := funkutil.NewFuncFilter("^str_", "")
	funcs, err := EnumerateFunctions(bin, MainBinaryOnly, filter)
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

	if err := emitReport("html", coverage, tmp); err != nil {
		t.Errorf("emitReport('html'): %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "test.html")); err != nil {
		t.Error("emitReport('html') should create test.html")
	}

	if err := emitReport("xml", coverage, tmp); err != nil {
		t.Errorf("emitReport('xml'): %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "coverage_test.xml")); err != nil {
		t.Error("emitReport('xml') should create XML file")
	}

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := emitReport("txt", coverage, tmp)
	w.Close()
	os.Stdout = old
	if err != nil {
		t.Errorf("emitReport('txt'): %v", err)
	}
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	if !strings.Contains(string(buf[:n]), "Coverage:") {
		t.Error("emitReport('txt') should print coverage")
	}

	if err := emitReport("bogus", coverage, tmp); err == nil {
		t.Error("emitReport('bogus') should return an error")
	} else if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("emitReport('bogus') error should name the format, got: %v", err)
	}
}

func TestCmdReport_UnknownFormat(t *testing.T) {
	tmp := t.TempDir()
	logDir := filepath.Join(tmp, "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "x_1_1_functions.log"), []byte("FUNC /bin/x foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(tmp, "out")

	err := cmdReport([]string{logDir, outDir, "--formats", "bogus"})
	if err == nil {
		t.Fatal("cmdReport with an unknown format should return an error")
	}
	if !strings.Contains(err.Error(), "bogus") {
		t.Errorf("cmdReport error should name the format, got: %v", err)
	}
}

// TestCommands asserts the exact key set: the 8 subcommand names plus every
// documented alias. "-u" for uninstall is deliberately absent — it's
// unreachable, shadowed by the "unwrap" deprecation guard in main, which
// intercepts the literal string "-u" before commands() is ever consulted;
// "--uninstall" (not shadowed) is the reachable alias instead.
func TestCommands(t *testing.T) {
	cmds := commands()
	want := map[string]string{
		"setup": "setup", "--setup": "setup",
		"install": "install", "-i": "install", "--install": "install",
		"uninstall": "uninstall", "--uninstall": "uninstall",
		"trace": "trace", "-t": "trace", "--trace": "trace",
		"enumerate": "enumerate", "-e": "enumerate", "--enumerate": "enumerate",
		"report": "report", "-r": "report", "--report": "report",
		"version": "version", "-v": "version", "--version": "version",
		"help": "help", "-h": "help", "--help": "help",
	}
	if len(cmds) != len(want) {
		t.Errorf("commands() has %d keys, want %d: got %v", len(cmds), len(want), slices.Sorted(maps.Keys(cmds)))
	}
	for k, wantName := range want {
		cmd, ok := cmds[k]
		if !ok {
			t.Errorf("expected key %q missing from commands()", k)
			continue
		}
		if cmd.name != wantName {
			t.Errorf("commands()[%q].name = %q, want %q", k, cmd.name, wantName)
		}
	}
	if _, ok := cmds["-u"]; ok {
		t.Error(`"-u" must not be registered: it's unreachable, shadowed by the "unwrap" guard in main`)
	}
}

func TestWriteFunctionsLog(t *testing.T) {
	tmp := t.TempDir()
	funcs := map[string][]string{
		"/bin/test": {"main", "foo_bar"},
	}
	path, err := writeFunctionsLog(tmp, "testbin", funcs)
	if err != nil {
		t.Fatalf("writeFunctionsLog failed: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("functions log file should exist: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile functions log: %v", err)
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "FUNC /bin/test main") || !strings.Contains(contentStr, "FUNC /bin/test foo_bar") {
		t.Errorf("functions log has unexpected content: %s", contentStr)
	}
}

func TestTraceInline(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found, skipping traceInline test")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int my_test_function() { return 42; }\nint main() { return my_test_function(); }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "my_custom_test_binary")
	if out, err := exec.Command("gcc", "-g", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile temporary binary: %v\n%s", err, out)
	}

	tmpLog := t.TempDir()
	tmpSafe := t.TempDir()

	t.Setenv("LOG_DIR", tmpLog)
	t.Setenv("SAFE_BIN_DIR", tmpSafe)

	trueBin, err := exec.LookPath("true")
	if err != nil {
		t.Skip("true binary not found, skipping traceInline test")
	}
	t.Setenv("FUNKOVERAGE_SHIM", trueBin)

	filter, _ := funkutil.NewFuncFilter("", "")
	code, err := traceInline(bin, []string{}, MainBinaryOnly, filter)
	if err != nil {
		t.Fatalf("traceInline error: %v", err)
	}
	if code != 0 {
		t.Errorf("traceInline code = %d, want 0", code)
	}

	files, _ := filepath.Glob(filepath.Join(tmpLog, "*_functions.log"))
	if len(files) == 0 {
		t.Error("expected functions log to be written in LOG_DIR")
	}
}

// --- addFilterFlags tests ---

func TestAddFilterFlags(t *testing.T) {
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	build := addFilterFlags(fs)
	if err := fs.Parse([]string{"--include", "^str_", "--exclude", "_internal$"}); err != nil {
		t.Fatal(err)
	}
	filter, err := build()
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	if !filter.Match("str_len") {
		t.Error("str_len should match")
	}
	if filter.Match("math_add") {
		t.Error("math_add should not match (not included)")
	}
	if filter.Match("str_internal") {
		t.Error("str_internal should not match (excluded)")
	}

	// A bad regex is surfaced by the returned closure, not at flag-parse time.
	fsBad := flag.NewFlagSet("y", flag.ContinueOnError)
	buildBad := addFilterFlags(fsBad)
	if err := fsBad.Parse([]string{"--include", "[unterminated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := buildBad(); err == nil {
		t.Error("expected error for invalid include regex")
	}
}

// --- cmdVersion / cmdHelp tests ---

func TestCmdVersion(t *testing.T) {
	out := testutil.CaptureOutput(t, &os.Stdout, func() {
		if err := cmdVersion(nil); err != nil {
			t.Errorf("cmdVersion: %v", err)
		}
	})
	if !strings.Contains(out, "funkoverage version") {
		t.Errorf("cmdVersion output = %q, want it to contain %q", out, "funkoverage version")
	}
}

func TestCmdHelp(t *testing.T) {
	out := testutil.CaptureOutput(t, &os.Stdout, func() {
		if err := cmdHelp(nil); err != nil {
			t.Errorf("cmdHelp: %v", err)
		}
	})
	if !strings.Contains(out, "Usage:") {
		t.Errorf("cmdHelp output missing usage text: %q", out)
	}
}

// --- readGnuDebugAltLink tests ---

func TestReadGnuDebugAltLink(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin")
	if out, err := exec.Command("gcc", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}

	// No .gnu_debugaltlink section present → empty string.
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	got := readGnuDebugAltLink(f)
	f.Close()
	if got != "" {
		t.Errorf("readGnuDebugAltLink (no section) = %q, want empty", got)
	}

	if _, err := exec.LookPath("objcopy"); err != nil {
		t.Skip("objcopy not found")
	}
	// Section layout: null-terminated filename followed by build-id bytes.
	secFile := filepath.Join(tmp, "altlink")
	content := append([]byte("alt.debug\x00"), make([]byte, 20)...)
	if err := os.WriteFile(secFile, content, 0644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("objcopy", "--add-section", ".gnu_debugaltlink="+secFile, bin).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --add-section: %v\n%s", err, out)
	}
	f2, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if got := readGnuDebugAltLink(f2); got != "alt.debug" {
		t.Errorf("readGnuDebugAltLink = %q, want %q", got, "alt.debug")
	}
}

// --- getBuildID error-path test ---

func TestGetBuildID_NoSection(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "nobuildid")
	if out, err := exec.Command("gcc", "-Wl,--build-id=none", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	f, err := elf.Open(bin)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := getBuildID(f); err == nil {
		t.Error("getBuildID on a binary built with --build-id=none should error")
	}
}

// --- writeBuffered tests ---

func TestWriteBuffered(t *testing.T) {
	tmp := t.TempDir()

	okPath := filepath.Join(tmp, "ok")
	if err := writeBuffered(okPath, func(w io.Writer) error {
		_, err := io.WriteString(w, "hello")
		return err
	}); err != nil {
		t.Fatalf("writeBuffered (success): %v", err)
	}
	if b, _ := os.ReadFile(okPath); string(b) != "hello" {
		t.Errorf("writeBuffered wrote %q, want %q", b, "hello")
	}

	// A write callback error propagates.
	wantErr := errors.New("boom")
	if err := writeBuffered(filepath.Join(tmp, "e"), func(io.Writer) error { return wantErr }); !errors.Is(err, wantErr) {
		t.Errorf("writeBuffered error = %v, want %v", err, wantErr)
	}

	// os.Create failure (path under a nonexistent directory) is returned.
	if err := writeBuffered(filepath.Join(tmp, "no", "such", "dir", "f"), func(io.Writer) error { return nil }); err == nil {
		t.Error("writeBuffered should error when os.Create fails")
	}
}

// --- enumerateOne external-debug fallback test ---

// TestEnumerateOne_ExternalDebugFallback exercises the fallback chain past
// the binary's own (stripped-away) symbol table: enumerateOne must find
// functions via an external .build-id debug file's .symtab. -no-pie keeps a
// PIE binary's leftover .dynsym from masking the "no symtab" state.
func TestEnumerateOne_ExternalDebugFallback(t *testing.T) {
	for _, tool := range []string{"gcc", "objcopy", "strip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	tmp := t.TempDir()
	orig := globalDebugRoot
	globalDebugRoot = tmp
	defer func() { globalDebugRoot = orig }()

	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int enum_helper() { return 7; }\nint main() { return enum_helper(); }"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "prog")
	if out, err := exec.Command("gcc", "-g", "-no-pie", "-Wl,--build-id", "-o", bin, src).CombinedOutput(); err != nil {
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
	dir := filepath.Join(tmp, ".build-id", buildID[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	debugFile := filepath.Join(dir, buildID[2:]+".debug")
	if out, err := exec.Command("objcopy", "--only-keep-debug", bin, debugFile).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --only-keep-debug: %v\n%s", err, out)
	}
	if out, err := exec.Command("strip", "--strip-all", bin).CombinedOutput(); err != nil {
		t.Fatalf("strip --strip-all: %v\n%s", err, out)
	}
	// Binary's own tables are gone now: enumeration must resolve via the
	// external debug file.
	if got := symtabFunctions(bin, nil); slices.Contains(got, "enum_helper") {
		t.Fatalf("test setup: enum_helper should be gone from the stripped binary, got %v", got)
	}

	funcs, err := enumerateOne(bin, nil)
	if err != nil {
		t.Fatalf("enumerateOne: %v", err)
	}
	if !slices.Contains(funcs, "enum_helper") {
		t.Errorf("enumerateOne via external debug = %v, want it to contain enum_helper", funcs)
	}
}
