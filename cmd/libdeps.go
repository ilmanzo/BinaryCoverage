package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"funkoverage/internal/funkutil"

	"golang.org/x/sys/cpu"
)

// resolveLibraries returns the absolute paths of binPath's transitive shared
// library dependencies, minus pure runtime libraries (funkutil.IsSystemLib).
//
// The preferred path is a DT_NEEDED walk applying ld.so's own resolution
// rules. It replaces ldd, which invokes the *target's* PT_INTERP with
// LD_TRACE_LOADED_OBJECTS=1 — i.e. it runs code out of the very binary being
// instrumented, and install runs as root. It also costs a fork+exec per
// target.
//
// If any SONAME cannot be resolved the whole walk is discarded and ldd runs
// after all. A partial dependency list would silently under-report coverage,
// whereas ldd is exact by construction: every host that this resolver does
// not fully understand keeps today's behaviour instead of quietly losing
// libraries.
func resolveLibraries(binPath string) ([]string, error) {
	libs, err := dtNeededClosure(binPath)
	if err != nil {
		debugLog("resolve deps for %s: %v; falling back to ldd", binPath, err)
		return ParseLddLibraries(binPath)
	}
	// Filter after the walk, not during it: a traceable library reachable
	// only *through* a runtime library still has to be found, because ldd
	// lists the whole closure and this has to match it.
	var out []string
	for _, lib := range libs {
		if !funkutil.IsSystemLib(lib) {
			out = append(out, lib)
		}
	}
	return out, nil
}

// lddLineRe matches both forms of ldd output:
//
//	libfoo.so.1 => /lib64/libfoo.so.1 (0x...)
//	/lib64/ld-linux-x86-64.so.2 (0x...)
//
// Capture group 1 is the absolute library path.
var lddLineRe = regexp.MustCompile(`(?:=>\s*)?(/\S+)\s+\(0x[0-9a-fA-F]+\)`)

// ParseLddLibraries runs ldd on binPath and returns absolute paths of
// shared libraries (skips vdso, "not found", and glibc/runtime system libs).
// It is the fallback for resolveLibraries, kept because it is exact by
// construction on any host whose layout the DT_NEEDED walk cannot follow.
func ParseLddLibraries(binPath string) ([]string, error) {
	out, err := exec.Command("ldd", binPath).Output()
	if err != nil {
		return nil, err
	}
	var libs []string
	for line := range strings.Lines(string(out)) {
		if strings.Contains(line, "linux-vdso") || strings.Contains(line, "not found") {
			continue
		}
		m := lddLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		path := m[1]
		if funkutil.IsSystemLib(path) {
			continue
		}
		libs = append(libs, path)
	}
	return libs, nil
}

func debugLog(format string, args ...any) {
	if os.Getenv("FUNKOVERAGE_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "[funkoverage] "+format+"\n", args...)
	}
}

// dynamicInfo is the part of an ELF file's dynamic section that dependency
// resolution needs, plus the identity its dependencies must match.
type dynamicInfo struct {
	needed  []string
	rpath   []string // DT_RPATH, split and expanded
	runpath []string // DT_RUNPATH, split and expanded
	class   elf.Class
	machine elf.Machine
}

func readDynamicInfo(path string) (dynamicInfo, error) {
	f, err := elf.Open(path)
	if err != nil {
		return dynamicInfo{}, err
	}
	defer f.Close()
	// DynString returns (nil, nil) when there is no SHT_DYNAMIC at all, so a
	// static binary reads back as "no dependencies" rather than an error.
	needed, err := f.DynString(elf.DT_NEEDED)
	if err != nil {
		return dynamicInfo{}, err
	}
	rpath, _ := f.DynString(elf.DT_RPATH)
	runpath, _ := f.DynString(elf.DT_RUNPATH)
	// $ORIGIN is the directory of the file ld.so actually opened, i.e. after
	// symlink resolution. /usr/bin/androiddeployqt6 is a symlink into
	// /usr/lib64/qt6/bin, so its RUNPATH of $ORIGIN/../../ means the qt6
	// tree, not /usr.
	origin := filepath.Dir(canonicalPath(path))
	return dynamicInfo{
		needed:  needed,
		rpath:   expandSearchPath(rpath, origin, f.Class),
		runpath: expandSearchPath(runpath, origin, f.Class),
		class:   f.Class,
		machine: f.Machine,
	}, nil
}

// searchDirs is this object's own part of the ld.so lookup order: DT_RPATH
// only when DT_RUNPATH is absent (DT_RUNPATH supersedes it), then
// LD_LIBRARY_PATH, then DT_RUNPATH. inheritedRPath is the executable's
// DT_RPATH, which — unlike DT_RUNPATH — glibc applies down the whole
// dependency chain, not just to the executable's direct needs.
//
// The cache and the default directories come after these, in resolveSoname.
func (d dynamicInfo) searchDirs(inheritedRPath []string) []string {
	var dirs []string
	if len(d.runpath) == 0 {
		dirs = append(dirs, d.rpath...)
		dirs = append(dirs, inheritedRPath...)
	}
	if v := os.Getenv("LD_LIBRARY_PATH"); v != "" {
		for dir := range strings.SplitSeq(v, ":") {
			if dir != "" {
				dirs = append(dirs, dir)
			}
		}
	}
	return append(dirs, d.runpath...)
}

// expandSearchPath splits colon-separated DT_RPATH/DT_RUNPATH values and
// expands the dynamic string tokens ld.so understands.
//
// $PLATFORM is deliberately left alone: it expands to a CPU-model string
// (x86_64, haswell, ...) that we would have to guess, and a wrong guess names
// a directory that does not exist — which simply sends the lookup on to the
// cache and, failing that, to the ldd fallback. Inventing a plausible-looking
// wrong directory would be worse than not expanding it.
func expandSearchPath(raw []string, origin string, class elf.Class) []string {
	libDir := "lib"
	if class == elf.ELFCLASS64 {
		libDir = "lib64"
	}
	expand := strings.NewReplacer(
		"$ORIGIN", origin, "${ORIGIN}", origin,
		"$LIB", libDir, "${LIB}", libDir,
	)
	var out []string
	for _, entry := range raw {
		for dir := range strings.SplitSeq(entry, ":") {
			if dir != "" {
				out = append(out, expand.Replace(dir))
			}
		}
	}
	return out
}

// dtNeededClosure walks DT_NEEDED transitively from binPath, resolving each
// SONAME the way ld.so would. It fails as soon as one SONAME cannot be
// resolved so the caller can fall back to ldd, rather than returning a subset
// that would look like a complete answer.
func dtNeededClosure(binPath string) ([]string, error) {
	root, err := readDynamicInfo(binPath)
	if err != nil {
		return nil, err
	}
	inherited := root.rpath

	seen := map[string]bool{}
	queue := []dynamicInfo{root}
	var out []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		dirs := cur.searchDirs(inherited)
		for _, soname := range cur.needed {
			path, ok := resolveSoname(soname, dirs, cur)
			if !ok {
				return nil, fmt.Errorf("cannot resolve %q", soname)
			}
			// Dedup on the resolved file, not the spelling: libc.so.6 needs
			// the interpreter, which different search paths reach as both
			// /lib64/ld-linux-*.so.2 and /usr/lib64/ld-linux-*.so.2.
			key := canonicalPath(path)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, path)
			next, err := readDynamicInfo(path)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", path, err)
			}
			queue = append(queue, next)
		}
	}
	return out, nil
}

// resolveSoname finds the file ld.so would load for soname, searching dirs
// first, then the cache, then the default directories. Candidates whose ELF
// class or machine differs from want's are rejected, which is how the 32-bit
// and 64-bit entries that share an SONAME in the cache are told apart without
// hardcoding glibc's per-architecture flag constants.
func resolveSoname(soname string, dirs []string, want dynamicInfo) (string, bool) {
	// An SONAME containing a slash is used as a path directly, as ld.so does.
	if strings.Contains(soname, "/") {
		return soname, elfMatches(soname, want)
	}
	for _, dir := range dirs {
		if p, ok := findInDir(dir, soname, want); ok {
			return p, true
		}
	}
	// ponytail: among cache entries that survive the hwcaps filter, the first
	// of the right class/machine wins. glibc scores the remainder by legacy
	// hwcap bits, which only separates alternative implementations of one
	// SONAME (zlib-ng-compat's libz.so.1 against the stock one). Both export
	// the same functions, so name-based attach is unaffected; address-based
	// attach is guarded by the build-id check.
	for _, p := range ldSoCache()[soname] {
		if hwcapsUsable(p) && elfMatches(p, want) {
			return p, true
		}
	}
	for _, dir := range defaultLibDirs(want.class) {
		if p, ok := findInDir(dir, soname, want); ok {
			return p, true
		}
	}
	return "", false
}

// findInDir applies ld.so's per-directory rule: the glibc-hwcaps
// subdirectories this CPU supports are searched before the directory itself.
// Skipping that would resolve, say, libjpeg.so.8 to /usr/lib64/libjpeg.so.8
// while the process actually maps
// /usr/lib64/glibc-hwcaps/x86-64-v3/libjpeg.so.8 — and a uprobe attached to
// the wrong inode simply never fires, silently reporting 0% for that library.
func findInDir(dir, soname string, want dynamicInfo) (string, bool) {
	for _, sub := range hwcapsSubdirs() {
		if p := filepath.Join(dir, "glibc-hwcaps", sub, soname); elfMatches(p, want) {
			return p, true
		}
	}
	if p := filepath.Join(dir, soname); elfMatches(p, want) {
		return p, true
	}
	return "", false
}

// hwcapsUsable rejects a cache entry living under a glibc-hwcaps subdirectory
// this CPU cannot run. ldconfig records every installed variant and leaves
// the choice to ld.so, so a v4 build can precede the baseline in the cache on
// a machine that only supports v3.
func hwcapsUsable(path string) bool {
	parent, sub := filepath.Split(filepath.Dir(path))
	if filepath.Base(filepath.Clean(parent)) != "glibc-hwcaps" {
		return true
	}
	return slices.Contains(hwcapsSubdirs(), sub)
}

// hwcapsSubdirs is the glibc-hwcaps subdirectory list this CPU can use,
// highest priority first — the same order glibc's dl_hwcaps_subdirs_active()
// produces.
//
// The feature sets are the x86-64 psABI microarchitecture levels.
// CMPXCHG16B, LAHF-SAHF, MOVBE, LZCNT and F16C are not checked: x/sys/cpu
// does not expose all of them, and no shipping CPU has the features that are
// checked without them.
var hwcapsSubdirs = sync.OnceValue(func() []string {
	if runtime.GOARCH != "amd64" {
		return nil
	}
	x := cpu.X86
	v2 := x.HasSSE3 && x.HasSSSE3 && x.HasSSE41 && x.HasSSE42 && x.HasPOPCNT
	v3 := v2 && x.HasAVX && x.HasAVX2 && x.HasBMI1 && x.HasBMI2 && x.HasFMA && x.HasOSXSAVE
	v4 := v3 && x.HasAVX512F && x.HasAVX512BW && x.HasAVX512CD && x.HasAVX512DQ && x.HasAVX512VL

	var out []string
	for _, level := range []struct {
		name      string
		supported bool
	}{{"x86-64-v4", v4}, {"x86-64-v3", v3}, {"x86-64-v2", v2}} {
		if level.supported {
			out = append(out, level.name)
		}
	}
	return out
})

func defaultLibDirs(class elf.Class) []string {
	if class == elf.ELFCLASS64 {
		return []string{"/lib64", "/usr/lib64", "/lib", "/usr/lib"}
	}
	return []string{"/lib", "/usr/lib"}
}

func elfMatches(path string, want dynamicInfo) bool {
	f, err := elf.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return f.Class == want.class && f.Machine == want.machine
}

// glibc's "new format" /etc/ld.so.cache (cache_file_new in elf/cache.c): a
// fixed header, then nlibs fixed-size entries, each holding two absolute file
// offsets into a trailing string table. Written in host byte order.
const (
	ldCacheMagic      = "glibc-ld.so.cache"
	ldCacheVersion    = "1.1"
	ldCacheHeaderSize = 48
	ldCacheEntrySize  = 24
)

// ldSoCache is the parsed /etc/ld.so.cache, read once per process. A missing
// or unparsable cache is not an error: resolution falls through to the
// default directories, and if that is not enough, resolveLibraries falls back
// to ldd.
var ldSoCache = sync.OnceValue(func() map[string][]string {
	c, err := readLdCache("/etc/ld.so.cache")
	if err != nil {
		debugLog("ld.so.cache unavailable: %v", err)
		return nil
	}
	return c
})

// readLdCache maps SONAME to every path listed for it, in cache order. One
// SONAME legitimately has several entries — a 32-bit and a 64-bit build of
// the same library, or glibc-hwcaps variants — so callers pick among them.
func readLdCache(path string) (map[string][]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	header := ldCacheMagic + ldCacheVersion
	if len(data) < ldCacheHeaderSize || string(data[:len(header)]) != header {
		return nil, fmt.Errorf("%s: not a new-format ld.so.cache", path)
	}
	nlibs := binary.NativeEndian.Uint32(data[20:24])
	end := ldCacheHeaderSize + int64(nlibs)*ldCacheEntrySize
	if end > int64(len(data)) {
		return nil, fmt.Errorf("%s: %d entries overrun a %d-byte file", path, nlibs, len(data))
	}
	out := make(map[string][]string, nlibs)
	for i := range int(nlibs) {
		e := data[ldCacheHeaderSize+i*ldCacheEntrySize:]
		soname, ok1 := ldCacheString(data, binary.NativeEndian.Uint32(e[4:8]))
		libPath, ok2 := ldCacheString(data, binary.NativeEndian.Uint32(e[8:12]))
		if ok1 && ok2 {
			out[soname] = append(out[soname], libPath)
		}
	}
	return out, nil
}

func ldCacheString(data []byte, off uint32) (string, bool) {
	if int64(off) >= int64(len(data)) {
		return "", false
	}
	rest := data[off:]
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return "", false
	}
	return string(rest[:end]), true
}
