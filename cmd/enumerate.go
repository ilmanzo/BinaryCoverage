package main

import (
	"bufio"
	"debug/dwarf"
	"debug/elf"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ianlancetaylor/demangle"
)

var funcBlacklist = map[string]struct{}{
	"main": {}, "_init": {}, "_start": {}, ".plt.got": {}, ".plt": {},
	"_dl_relocate_static_pie": {},
}

func funcIsRelevant(name string) bool {
	if _, bad := funcBlacklist[name]; bad {
		return false
	}
	if strings.HasSuffix(name, "@plt") || strings.HasSuffix(name, "@plt.got") {
		return false
	}
	if strings.HasPrefix(name, "__") {
		return false
	}
	return true
}

func imageIsRelevant(name string) bool {
	return name != "[vdso]" && name != "" && name != "linux-vdso.so.1"
}

// stripVersionSuffix removes @GLIBC_x.y style version annotations.
func stripVersionSuffix(name string) string {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		return name[:i]
	}
	return name
}

// demangleName applies C++/Rust demangling and strips version suffixes.
func demangleName(raw string) string {
	stripped := stripVersionSuffix(raw)
	return demangle.Filter(stripped)
}

// EnumerateFunctions returns map[imagePath][]functionName for the binary and
// all its shared libraries that have debug info.
func EnumerateFunctions(binPath string, noLibs bool) (map[string][]string, error) {
	result := make(map[string][]string)

	funcs, err := enumerateOne(binPath)
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
		ok, _ := hasDebugInfo(lib)
		if !ok {
			continue
		}
		libFuncs, err := enumerateOne(lib)
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
// Tries DWARF first, falls back to symbol table.
func enumerateOne(path string) ([]string, error) {
	// Check for external debug file and open the richer one.
	debugPath := externalDebugPath(path)
	elfPath := path
	if debugPath != "" {
		elfPath = debugPath
	}

	f, err := elf.Open(elfPath)
	if err != nil {
		return nil, fmt.Errorf("open elf: %w", err)
	}
	defer f.Close()

	// Try DWARF
	dwarfData, err := f.DWARF()
	if err == nil {
		funcs, err := enumerateDWARF(dwarfData)
		if err == nil && len(funcs) > 0 {
			return funcs, nil
		}
	}

	// Fall back to symbol table from original file (debug file may not have .symtab)
	orig, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open original elf: %w", err)
	}
	defer orig.Close()
	return enumerateSymtab(orig)
}

// externalDebugPath returns the path to an external .debug file, or "".
func externalDebugPath(binPath string) string {
	f, err := elf.Open(binPath)
	if err != nil {
		return ""
	}
	buildID, err := getBuildID(f)
	f.Close()
	if err != nil || len(buildID) <= 2 {
		return ""
	}
	p := buildIDDebugPath(buildID)
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func enumerateDWARF(dwarfData *dwarf.Data) ([]string, error) {
	seen := make(map[string]struct{})
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
		if entry.Val(dwarf.AttrLowpc) == nil {
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

		name := demangleName(raw)
		if !funcIsRelevant(name) {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		funcs = append(funcs, name)
	}
	return funcs, nil
}

func enumerateSymtab(f *elf.File) ([]string, error) {
	symbols, err := f.Symbols()
	if err != nil {
		// Try dynamic symbols as last resort
		symbols, err = f.DynamicSymbols()
		if err != nil {
			return nil, fmt.Errorf("no symbol table: %w", err)
		}
	}
	seen := make(map[string]struct{})
	var funcs []string
	for _, sym := range symbols {
		if elf.ST_TYPE(sym.Info) != elf.STT_FUNC {
			continue
		}
		if sym.Value == 0 {
			continue
		}
		name := demangleName(sym.Name)
		if !funcIsRelevant(name) {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		funcs = append(funcs, name)
	}
	return funcs, nil
}

// ParseLddLibraries runs ldd on binPath and returns absolute paths of
// shared libraries (skips vdso, ld-linux, and "not found" entries).
func ParseLddLibraries(binPath string) ([]string, error) {
	out, err := exec.Command("ldd", binPath).Output()
	if err != nil {
		return nil, err
	}
	var libs []string
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// Typical ldd output:
		//   libfoo.so.1 => /lib64/libfoo.so.1 (0x...)
		//   /lib64/ld-linux-x86-64.so.2 (0x...)
		//   linux-vdso.so.1 (0x...)
		if strings.Contains(line, "linux-vdso") || strings.Contains(line, "not found") {
			continue
		}
		var libPath string
		if strings.Contains(line, "=>") {
			parts := strings.SplitN(line, "=>", 2)
			rhs := strings.TrimSpace(parts[1])
			// strip the (0x...) suffix
			if i := strings.Index(rhs, " ("); i >= 0 {
				rhs = strings.TrimSpace(rhs[:i])
			}
			if rhs == "" || rhs == "not found" {
				continue
			}
			libPath = rhs
		} else {
			// direct path line like /lib64/ld-linux...
			if i := strings.Index(line, " ("); i >= 0 {
				libPath = strings.TrimSpace(line[:i])
			}
		}
		if libPath == "" || strings.Contains(libPath, "ld-linux") {
			continue
		}
		libs = append(libs, libPath)
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
			fmt.Fprintf(w, "FUNC %s %s\n", image, n)
		}
	}
	return path, w.Flush()
}
