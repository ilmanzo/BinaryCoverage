package main

import (
	"debug/elf"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"funkoverage/internal/funkutil"
)

// scanLimit bounds how many host binaries TestResolveLibraries_MatchesLdd
// compares. Every one costs a fork+exec of ldd; a few dozen is already a wide
// enough sample to cover RUNPATH, $ORIGIN and glibc-hwcaps layouts.
const scanLimit = 60

// dynamicHostBinaries returns up to scanLimit dynamically linked executables
// from /usr/bin, so the test exercises whatever shapes the host happens to
// have rather than a hardcoded list that may not exist everywhere.
func dynamicHostBinaries(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("/usr/bin")
	if err != nil {
		t.Skipf("cannot read /usr/bin: %v", err)
	}
	var bins []string
	for _, e := range entries {
		path := filepath.Join("/usr/bin", e.Name())
		f, err := elf.Open(path)
		if err != nil {
			continue
		}
		dynamic := f.SectionByType(elf.SHT_DYNAMIC) != nil
		f.Close()
		if dynamic {
			bins = append(bins, path)
		}
		if len(bins) == scanLimit {
			break
		}
	}
	return bins
}

// TestResolveLibraries_MatchesLdd pins the DT_NEEDED walk to ldd's answer on
// whatever binaries the host provides. Both sides are canonicalised first:
// ldd echoes the spelling it happened to find (/lib64 against /usr/lib64,
// $ORIGIN-relative RUNPATH entries left uncleaned) and only the physical file
// matters, since EnumerateFunctions keys its result map by canonicalPath too.
func TestResolveLibraries_MatchesLdd(t *testing.T) {
	if _, err := exec.LookPath("ldd"); err != nil {
		t.Skip("ldd not available")
	}
	bins := dynamicHostBinaries(t)
	if len(bins) == 0 {
		t.Skip("no dynamically linked binaries in /usr/bin")
	}

	var compared, walked int
	for _, bin := range bins {
		resolvedBin := canonicalPath(bin)
		want, err := ParseLddLibraries(resolvedBin)
		if err != nil {
			continue // statically linked, or ldd refused it
		}
		got, err := dtNeededClosure(resolvedBin)
		if err != nil {
			continue // resolveLibraries would fall back to ldd, trivially equal
		}
		walked++
		var filtered []string
		for _, lib := range got {
			if !funkutil.IsSystemLib(lib) {
				filtered = append(filtered, lib)
			}
		}
		compared++
		if w, g := canonicalSet(want), canonicalSet(filtered); !slices.Equal(w, g) {
			t.Errorf("%s: dependency sets differ\n  ldd only: %v\n  walk only: %v",
				bin, missing(w, g), missing(g, w))
		}
	}
	if compared == 0 {
		t.Skip("no binary could be compared")
	}
	// A resolver that errored on everything would fall back to ldd and pass
	// the loop above without ever being exercised.
	if walked*2 < compared {
		t.Errorf("DT_NEEDED walk only handled %d of %d binaries", walked, compared)
	}
	t.Logf("compared %d binaries", compared)
}

func canonicalSet(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = canonicalPath(p)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func missing(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

// TestReadLdCache checks the hand-rolled binary parser against the host's own
// cache: every glibc system has libc.so.6 in it, mapped to a readable ELF.
func TestReadLdCache(t *testing.T) {
	cache, err := readLdCache("/etc/ld.so.cache")
	if err != nil {
		t.Skipf("no parsable ld.so.cache on this host: %v", err)
	}
	paths := cache["libc.so.6"]
	if len(paths) == 0 {
		t.Fatalf("libc.so.6 absent from a cache of %d sonames", len(cache))
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			t.Errorf("libc.so.6 -> %q is not absolute", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Errorf("libc.so.6 -> %q: %v", p, err)
		}
	}
}

func TestHwcapsUsable(t *testing.T) {
	active := hwcapsSubdirs()
	tests := []struct {
		path string
		want bool
	}{
		{"/lib64/libz.so.1", true},
		{"/usr/lib64/zlib-ng-compat/libz.so.1", true},
		{"/lib64/glibc-hwcaps/x86-64-v3/libz.so.1", slices.Contains(active, "x86-64-v3")},
		{"/lib64/glibc-hwcaps/made-up-level/libz.so.1", false},
	}
	for _, tc := range tests {
		if got := hwcapsUsable(tc.path); got != tc.want {
			t.Errorf("hwcapsUsable(%q) = %v, want %v (active: %v)", tc.path, got, tc.want, active)
		}
	}
}
