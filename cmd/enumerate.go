package main

import (
	"bufio"
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

// demangleName applies C++/Rust demangling and strips version suffixes.
func demangleName(raw string) string {
	return demangle.Filter(funkutil.StripVersion(raw))
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
//
// bpftrace's `uprobe:<path>:*` wildcard expands using the ELF symbol table,
// not DWARF, so symtab is the source of truth for what we can actually trace.
// We also dodge dwz-compressed DWARF (openSUSE/Fedora), where most
// DW_TAG_subprogram entries reference abstract DIEs in a separate
// .gnu_debugaltlink file that Go's debug/dwarf package cannot follow.
//
// Order: binary's .symtab → external .debug file's .symtab → DWARF.
func enumerateOne(path string) ([]string, error) {
	if funcs := symtabFunctions(path); len(funcs) > 0 {
		return funcs, nil
	}
	debugPath := externalDebugPath(path)
	if debugPath != "" {
		if funcs := symtabFunctions(debugPath); len(funcs) > 0 {
			return funcs, nil
		}
	}
	target := path
	if debugPath != "" {
		target = debugPath
	}
	return dwarfFunctions(target)
}

func symtabFunctions(path string) []string {
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	funcs, err := enumerateSymtab(f)
	if err != nil {
		return nil
	}
	return funcs
}

func dwarfFunctions(path string) ([]string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open elf: %w", err)
	}
	defer f.Close()
	dw, err := f.DWARF()
	if err != nil {
		return nil, fmt.Errorf("dwarf: %w", err)
	}
	return enumerateDWARF(dw)
}

// externalDebugPath returns the path to an external .debug file, or "".
// Tries: .build-id layout, then .gnu_debugaltlink (dwz-compressed), then
// brute-force search in /usr/lib/debug/.dwz/ by package name.
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

	// Brute-force: scan .dwz dir for package matching binary name
	binName := filepath.Base(binPath)
	dwzDir := "/usr/lib/debug/.dwz"
	entries, err := os.ReadDir(dwzDir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && (strings.HasPrefix(e.Name(), binName) || strings.Contains(e.Name(), binName)) {
				return filepath.Join(dwzDir, e.Name())
			}
		}
	}

	return ""
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

// lddLineRe matches both forms of ldd output:
//
//	libfoo.so.1 => /lib64/libfoo.so.1 (0x...)
//	/lib64/ld-linux-x86-64.so.2 (0x...)
//
// Capture group 1 is the absolute library path.
var lddLineRe = regexp.MustCompile(`(?:=>\s*)?(/\S+)\s+\(0x[0-9a-fA-F]+\)`)

// systemLibRe matches glibc/runtime libraries we never want to wildcard-trace:
// each carries thousands of symbols, so attaching uprobes to all of them
// blows past FUNKOVERAGE_ATTACH_TIMEOUT and rarely yields useful coverage
// for the target program.
var systemLibRe = regexp.MustCompile(`^(ld-linux[^/]*|libc|libm|libdl|librt|libpthread|libresolv|libnsl|libutil|libcrypt|libanl|libgcc_s|libstdc\+\+)\.so(\.|$)`)

func isSystemLib(libPath string) bool {
	return systemLibRe.MatchString(filepath.Base(libPath))
}

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
		if isSystemLib(path) {
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
			fmt.Fprintf(w, "FUNC %s %s\n", image, n)
		}
	}
	return path, w.Flush()
}
