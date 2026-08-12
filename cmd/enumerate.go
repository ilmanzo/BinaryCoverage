package main

import (
	"bufio"
	"cmp"
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"funkoverage/internal/funkutil"

	"github.com/ianlancetaylor/demangle"
)

// FuncFilter gates which functions pass enumeration based on regex patterns
// applied to the demangled name.
type FuncFilter struct {
	Include *regexp.Regexp
	Exclude *regexp.Regexp
}

func NewFuncFilter(include, exclude string) (*FuncFilter, error) {
	f := &FuncFilter{}
	if include != "" {
		re, err := regexp.Compile(include)
		if err != nil {
			return nil, fmt.Errorf("bad --include regex: %w", err)
		}
		f.Include = re
	}
	if exclude != "" {
		re, err := regexp.Compile(exclude)
		if err != nil {
			return nil, fmt.Errorf("bad --exclude regex: %w", err)
		}
		f.Exclude = re
	}
	return f, nil
}

func (f *FuncFilter) Match(demangled string) bool {
	if f == nil {
		return true
	}
	if f.Include != nil && !f.Include.MatchString(demangled) {
		return false
	}
	if f.Exclude != nil && f.Exclude.MatchString(demangled) {
		return false
	}
	return true
}

// Sidecar converts f to its serializable form (regex source patterns) so the
// shim can re-apply the same filter to functions discovered via dlopen at
// runtime. A nil filter yields the zero value (no filtering).
func (f *FuncFilter) Sidecar() funkutil.FilterSidecar {
	var s funkutil.FilterSidecar
	if f == nil {
		return s
	}
	if f.Include != nil {
		s.Include = f.Include.String()
	}
	if f.Exclude != nil {
		s.Exclude = f.Exclude.String()
	}
	return s
}

// acceptFunc reports whether to keep `raw` (mangled name) given a filter and
// dedup set. On accept it marks `raw` as seen.
func acceptFunc(seen map[string]struct{}, raw string, filter *FuncFilter) bool {
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

// EnumerateFunctions returns map[imagePath][]functionName for the binary and
// all its shared libraries that have debug info.
func EnumerateFunctions(binPath string, noLibs bool, filter *FuncFilter) (map[string][]string, error) {
	result := make(map[string][]string)

	funcs, err := enumerateOne(binPath, filter)
	if err != nil {
		return nil, fmt.Errorf("enumerate %s: %w", binPath, err)
	}
	if len(funcs) > 0 {
		result[binPath] = funcs
	}

	if noLibs {
		return result, nil
	}

	libs, err := ParseLddLibraries(binPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "enumerate: ldd failed for %s: %v\n", binPath, err)
	}
	for _, lib := range libs {
		if !imageIsRelevant(filepath.Base(lib)) {
			continue
		}
		libFuncs, err := enumerateOne(lib, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "enumerate: skipping %s: %v\n", lib, err)
			continue
		}
		if len(libFuncs) > 0 {
			result[lib] = libFuncs
		}
	}
	return result, nil
}

// enumerateOne enumerates functions from a single ELF file (binary or library).
//
// Symtab is the source of truth: uprobe attach resolves names against the ELF
// symbol table directly. We also dodge dwz-compressed DWARF (openSUSE/Fedora),
// where most DW_TAG_subprogram entries reference abstract DIEs in a separate
// .gnu_debugaltlink file that Go's debug/dwarf package cannot follow.
//
// Order: binary's .symtab → external .debug file's .symtab → DWARF.
func enumerateOne(path string, filter *FuncFilter) ([]string, error) {
	if funcs := symtabFunctions(path, filter); len(funcs) > 0 {
		return funcs, nil
	}
	debugPath := externalDebugPath(path)
	if debugPath != "" {
		if funcs := symtabFunctions(debugPath, filter); len(funcs) > 0 {
			return funcs, nil
		}
	}
	return dwarfFunctions(cmp.Or(debugPath, path), filter)
}

func symtabFunctions(path string, filter *FuncFilter) []string {
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	funcs, err := enumerateSymtab(f, filter)
	if err != nil {
		return nil
	}
	return funcs
}

func dwarfFunctions(path string, filter *FuncFilter) ([]string, error) {
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
// Tries: .build-id layout, then .gnu_debuglink (standard GNU separate-debug
// convention), then .gnu_debugaltlink (dwz-compressed).
func externalDebugPath(binPath string) string {
	f, err := elf.Open(binPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	// Try .build-id path
	buildID, err := getBuildID(f)
	if err == nil && len(buildID) > 2 {
		p := buildIDDebugPath(buildID)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Try .gnu_debuglink: <debugRoot>/<canonical-dir-of-binary>/<name>
	if linkPath := debugLinkPath(binPath, readGnuDebugLink(f)); linkPath != "" {
		if _, err := os.Stat(linkPath); err == nil {
			return linkPath
		}
	}

	// Try .gnu_debugaltlink (dwz-compressed debug)
	if altPath := readGnuDebugAltLink(f); altPath != "" {
		// Try direct path from section
		if _, err := os.Stat(altPath); err == nil {
			return altPath
		}
		// Try relative to /usr/lib/debug
		relPath := filepath.Join("/usr/lib/debug", altPath)
		if _, err := os.Stat(relPath); err == nil {
			return relPath
		}
		// Try /usr/lib/debug/.dwz/<basename>
		if base := filepath.Base(altPath); base != altPath {
			dwzPath := filepath.Join("/usr/lib/debug/.dwz", base)
			if _, err := os.Stat(dwzPath); err == nil {
				return dwzPath
			}
		}
	}

	return ""
}

func enumerateDWARF(dwarfData *dwarf.Data, filter *FuncFilter) ([]string, error) {
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

func enumerateSymtab(f *elf.File, filter *FuncFilter) ([]string, error) {
	symbols, err := f.Symbols()
	if err != nil {
		// Try dynamic symbols as last resort
		symbols, err = f.DynamicSymbols()
		if err != nil {
			return nil, fmt.Errorf("no symbol table: %w", err)
		}
	}
	seen := make(map[string]struct{})
	seenAddr := make(map[uint64]struct{})
	var funcs []string
	for _, sym := range symbols {
		if elf.ST_TYPE(sym.Info) != elf.STT_FUNC {
			continue
		}
		if sym.Value == 0 {
			continue
		}
		// uprobe attach requires offset < symbol size; skip the CRT helpers
		// (deregister_tm_clones, frame_dummy, etc.) that the linker emits
		// with size 0.
		if sym.Size == 0 {
			continue
		}
		// Itanium ABI complete-object/base-object ctor-dtor pairs (C1/C2,
		// D1/D2) are distinct symbols that alias the same address whenever
		// the class has no virtual bases. Dedup by address, not name, so
		// they count once instead of double-attaching and double-counting —
		// this is safe even for virtual-base classes, where C1/C2 genuinely
		// compile to different code at different addresses.
		if _, dup := seenAddr[sym.Value]; dup {
			continue
		}
		if acceptFunc(seen, sym.Name, filter) {
			seenAddr[sym.Value] = struct{}{}
			funcs = append(funcs, sym.Name)
		}
	}
	return funcs, nil
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
func ParseLddLibraries(binPath string) ([]string, error) {
	out, err := exec.Command("ldd", binPath).Output()
	if err != nil {
		return nil, err
	}
	var libs []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
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

// writeFunctionsLog writes a _functions.log file to logDir and returns its path.
func writeFunctionsLog(logDir, binaryBasename string, funcs map[string][]string) (string, error) {
	if err := os.MkdirAll(logDir, 0777); err != nil {
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
	for image, names := range funcs {
		for _, n := range names {
			// Demangle for the report side; .funcs.json keeps the mangled
			// form for uprobe attach.
			fmt.Fprintf(w, "FUNC %s %s\n", image, demangleName(n))
		}
	}
	return path, w.Flush()
}
