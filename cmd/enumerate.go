package main

import (
	"bufio"
	"cmp"
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"funkoverage/internal/funkutil"

	"github.com/ianlancetaylor/demangle"
)

// acceptFunc reports whether to keep `raw` (mangled name) given a filter and
// dedup set. On accept it marks `raw` as seen.
func acceptFunc(seen map[string]struct{}, raw string, filter *funkutil.FuncFilter) bool {
	demangled := demangleName(raw)
	if !funkutil.FuncIsRelevant(demangled) || !filter.Match(demangled) {
		return false
	}
	if _, dup := seen[raw]; dup {
		return false
	}
	seen[raw] = struct{}{}
	return true
}

func imageIsRelevant(name string) bool {
	return name != "[vdso]" && name != "" && name != "linux-vdso.so.1"
}

// demangleName applies C++/Rust demangling and strips version suffixes.
func demangleName(raw string) string {
	return demangle.Filter(funkutil.StripVersion(raw))
}

// LibScope controls whether install/trace/enumerate also cover a binary's
// shared library dependencies, or only the binary itself (the --no-libs
// flag) — a named type so call sites read EnumerateFunctions(path,
// MainBinaryOnly, filter) instead of an opaque EnumerateFunctions(path,
// true, filter).
type LibScope bool

const (
	WithLibraries  LibScope = false // default: also enumerate/trace shared library dependencies
	MainBinaryOnly LibScope = true  // --no-libs: skip library dependencies entirely
)

// EnumerateFunctions returns the traceable functions of the binary and of all
// its shared libraries that have debug info, keyed by canonical image path.
func EnumerateFunctions(binPath string, libScope LibScope, filter *funkutil.FuncFilter) (map[string]funkutil.ImageFuncs, error) {
	result := make(map[string]funkutil.ImageFuncs)

	funcs, err := enumerateImage(binPath, filter)
	if err != nil {
		return nil, fmt.Errorf("enumerate %s: %w", binPath, err)
	}
	if funcs.Len() > 0 {
		result[canonicalPath(binPath)] = funcs
	}

	if libScope == MainBinaryOnly {
		return result, nil
	}

	libs, err := resolveLibraries(binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enumerate: dependency resolution failed for %s: %v\n", binPath, err)
	}
	for _, lib := range libs {
		if !imageIsRelevant(filepath.Base(lib)) {
			continue
		}
		libFuncs, err := enumerateImage(lib, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "enumerate: skipping %s: %v\n", lib, err)
			continue
		}
		if libFuncs.Len() > 0 {
			result[canonicalPath(lib)] = libFuncs
		}
	}
	return result, nil
}

// enumerateImage enumerates path's functions and splits them by how the shim
// will have to reach them: names the mapped file itself can resolve, and names
// that live only in its external debug file and therefore need a pre-computed
// file offset.
//
// The split exists so funkoverage never has to modify a system library. The
// previous design merged the debug symbols into the library in place with
// eu-unstrip, which made `rpm -V` dirty, raced between concurrent installs,
// and left the host permanently altered if the process died before uninstall.
//
// Falling back to a name-only result is always safe: it is exactly the
// coverage a plain, unmerged library gives, so a failure here costs the
// internal functions, never correctness.
func enumerateImage(path string, filter *funkutil.FuncFilter) (funkutil.ImageFuncs, error) {
	names, debugPath, err := enumerateOne(path, filter)
	if err != nil || len(names) == 0 {
		return funkutil.ImageFuncs{}, err
	}

	f, err := elf.Open(path)
	if err != nil {
		return funkutil.ImageFuncs{Names: names}, nil
	}
	defer f.Close()

	resolvable := funkutil.ResolvableFuncNames(f)
	byName := make([]string, 0, len(names))
	var debugOnly []string
	for _, name := range names {
		if _, ok := resolvable[name]; ok {
			byName = append(byName, name)
		} else {
			debugOnly = append(debugOnly, name)
		}
	}
	if len(debugOnly) == 0 || debugPath == "" {
		return funkutil.ImageFuncs{Names: names}, nil
	}

	// No build-id means no way to detect that the library was upgraded between
	// install and run, and a stale offset lands mid-instruction. Not worth the
	// internal functions.
	buildID, err := funkutil.BuildID(f)
	if err != nil {
		debugLog("no build-id for %s (%v); tracing its %d debug-only functions is unsafe", path, err, len(debugOnly))
		return funkutil.ImageFuncs{Names: byName}, nil
	}

	offsets := debugFuncAddrs(f, debugPath, debugOnly)
	if len(offsets) == 0 {
		return funkutil.ImageFuncs{Names: byName}, nil
	}
	return funkutil.ImageFuncs{BuildID: buildID, Names: byName, Offsets: offsets}, nil
}

// debugFuncAddrs resolves each of want to its offset within the runtime file,
// reading symbol values from debugPath. Names the debug file does not place
// inside an executable segment are dropped.
func debugFuncAddrs(runtime *elf.File, debugPath string, want []string) []funkutil.FuncAddr {
	df, err := elf.Open(debugPath)
	if err != nil {
		debugLog("open debug file %s: %v", debugPath, err)
		return nil
	}
	defer df.Close()

	all := funkutil.SymbolFileOffsets(runtime, df)
	if len(all) == 0 {
		debugLog("%s: no usable symbol offsets (build-id mismatch, or no symbols); tracing by name only", debugPath)
		return nil
	}
	addrs := make([]funkutil.FuncAddr, 0, len(want))
	for _, name := range want {
		if off, ok := all[name]; ok {
			addrs = append(addrs, funkutil.FuncAddr{Name: name, Offset: off})
		}
	}
	return addrs
}

// canonicalPath resolves symlinks so the same physical file always maps to
// the same report key, regardless of which symlink/SONAME name discovered
// it (e.g. libz.so.1 vs. libz.so.1.3.1). Falls back to path if resolution
// fails, so a permission error can't drop an otherwise-enumerable image.
func canonicalPath(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return path
}

// enumerateOne enumerates functions from a single ELF file (binary or library).
//
// Symtab is the source of truth: uprobe attach resolves names against the ELF
// symbol table directly. We also dodge dwz-compressed DWARF (openSUSE/Fedora),
// where most DW_TAG_subprogram entries reference abstract DIEs in a separate
// .gnu_debugaltlink file that Go's debug/dwarf package cannot follow.
//
// Order: external .debug file's .symtab → binary's own .symtab → DWARF.
//
// The debug file comes first because for a stripped/debuglinked pair it is a
// superset: distro packaging moves the full .symtab there and leaves the
// runtime file with only .dynsym, so every LOCAL/static function exists in
// exactly one of the two. Checking the runtime file first and taking the
// first non-empty answer used to lose all of them whenever .dynsym exported
// anything at all — a CPython extension module resolved to just its PyInit_*
// entry point and stopped, hiding the other 18 (measured on Tumbleweed:
// _bz2.cpython-313 has 1 defined function in the runtime file and 19 in its
// debug file; libssl.so.3 has 603 against 2512).
// It also returns the external debug file it consulted (or ""), so callers
// don't have to resolve it a second time.
func enumerateOne(path string, filter *funkutil.FuncFilter) ([]string, string, error) {
	debugPath := externalDebugPath(path)
	if debugPath != "" {
		if funcs := symtabFunctions(debugPath, filter); len(funcs) > 0 {
			return funcs, debugPath, nil
		}
	}
	if funcs := symtabFunctions(path, filter); len(funcs) > 0 {
		return funcs, debugPath, nil
	}
	funcs, err := dwarfFunctions(cmp.Or(debugPath, path), filter)
	return funcs, debugPath, err
}

func symtabFunctions(path string, filter *funkutil.FuncFilter) []string {
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return enumerateSymtab(f, filter)
}

func dwarfFunctions(path string, filter *funkutil.FuncFilter) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open elf: %w", err)
	}
	defer f.Close()
	if !hasEmbeddedDebugInfo(f) {
		// No .debug_info anywhere in this file: there's nothing to parse.
		// This is the common, expected case once every external-debug
		// lookup has failed — not a real error, so let the caller treat it
		// as "no functions found" instead of surfacing a raw stdlib parser
		// error (Go's debug/dwarf errors on a missing/empty .debug_info
		// with a cryptic "too short" rather than reporting its absence).
		return nil, nil
	}
	dw, err := f.DWARF()
	if err != nil {
		return nil, fmt.Errorf("dwarf: %w", err)
	}
	return enumerateDWARF(dw, filter)
}

func hasEmbeddedDebugInfo(f *elf.File) bool {
	for _, s := range f.Sections {
		if s.Name == ".debug_info" && s.Size > 0 {
			return true
		}
	}
	return false
}

// externalDebugPath returns the path to an external .debug file, or "".
// Thin wrapper over resolveDebugFile (cmd/elfutil.go), which also backs the
// install-time merge path — enumeration wants a candidate even when the
// binary already carries embedded debug sections, so skipIfEmbedded is
// false, and any elf.Open error degrades to "" (no candidate) rather than
// propagating, matching this function's pre-existing (string, no error)
// signature.
func externalDebugPath(binPath string) string {
	path, _ := resolveDebugFile(binPath, binPath, AllowEmbedded)
	return path
}

func enumerateDWARF(dwarfData *dwarf.Data, filter *funkutil.FuncFilter) ([]string, error) {
	seen := make(map[string]struct{})
	seenAddr := make(map[uint64]struct{})
	var funcs []string

	reader := dwarfData.Reader()
	for {
		entry, err := reader.Next()
		if err != nil || entry == nil {
			break
		}
		if entry.Tag != dwarf.TagSubprogram {
			continue
		}
		// Skip declarations (no code)
		if decl, ok := entry.Val(dwarf.AttrDeclaration).(bool); ok && decl {
			continue
		}
		// Skip abstract origins without an address (inlined-only entries)
		lowpc, ok := entry.Val(dwarf.AttrLowpc).(uint64)
		if !ok {
			continue
		}

		// Prefer linkage name (mangled) for demangling, fall back to source name
		var raw string
		if ln, ok := entry.Val(dwarf.AttrLinkageName).(string); ok && ln != "" {
			raw = ln
		} else if n, ok := entry.Val(dwarf.AttrName).(string); ok && n != "" {
			raw = n
		} else {
			continue
		}

		// Itanium ABI complete-object/base-object ctor-dtor pairs (C1/C2,
		// D1/D2) are distinct DIEs that alias the same low_pc whenever the
		// class has no virtual bases. Dedup by address, not name (see the
		// matching comment in enumerateSymtab).
		if _, dup := seenAddr[lowpc]; dup {
			continue
		}

		// Keep the raw (mangled) name. uprobe attach resolves it against
		// the ELF symbol table directly; demangling happens at output time.
		if acceptFunc(seen, raw, filter) {
			seenAddr[lowpc] = struct{}{}
			funcs = append(funcs, raw)
		}
	}
	return funcs, nil
}

// enumerateSymtab delegates the actual symbol walk (union of .symtab and
// .dynsym, STT_FUNC + size/address filtering, address+name dedup — shared
// with the shim's runtime dlopen JIT discovery) to funkutil.SymtabFunctions,
// supplying the relevance + --include/--exclude predicate.
func enumerateSymtab(f *elf.File, filter *funkutil.FuncFilter) []string {
	return funkutil.SymtabFunctions(f, func(demangled string) bool {
		return funkutil.FuncIsRelevant(demangled) && filter.Match(demangled)
	})
}

// writeFunctionsLog writes a _functions.log file to logDir and returns its path.
func writeFunctionsLog(logDir, binaryBasename string, funcs map[string]funkutil.ImageFuncs) (string, error) {
	if err := funkutil.EnsureLogDir(logDir); err != nil {
		return "", err
	}
	ts := time.Now()
	name := fmt.Sprintf("%s_%s_%d_functions.log",
		binaryBasename,
		ts.Format("20060102-150405"),
		ts.UnixNano(),
	)
	path := filepath.Join(logDir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for image, imgFuncs := range funcs {
		for n := range imgFuncs.All() {
			// Demangle for the report side; .funcs.json keeps the mangled
			// form for uprobe attach.
			fmt.Fprintf(w, "FUNC %s %s\n", image, demangleName(n))
		}
	}
	return path, w.Flush()
}

// enumerateFuncs runs EnumerateFunctions against path and writes the
// functions log to logDir (best-effort — a log-write failure only warns).
// A zero-function result is a hard error when libScope is MainBinaryOnly;
// otherwise it's returned as an empty/partial map with no error, leaving it
// up to the caller whether that's worth a warning — install() warns about
// it, traceInline() doesn't, matching each one's pre-existing behavior.
//
// Shared between install (cmd/shim.go, permanent) and traceInline
// (cmd/trace.go, temporary) — both enumerate, then log, the same way; they
// differ in what happens to the result afterward (install additionally
// rolls back its move on error).
func enumerateFuncs(path, binaryName, logDir string, libScope LibScope, filter *funkutil.FuncFilter) (map[string]funkutil.ImageFuncs, error) {
	funcs, err := EnumerateFunctions(path, libScope, filter)
	if err != nil {
		return nil, fmt.Errorf("function enumeration: %w", err)
	}
	if len(funcs) == 0 && libScope == MainBinaryOnly {
		return nil, fmt.Errorf("no functions found in %s (debug symbols missing?)", path)
	}
	if _, err := writeFunctionsLog(logDir, binaryName, funcs); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write functions log: %v\n", err)
	}
	return funcs, nil
}
