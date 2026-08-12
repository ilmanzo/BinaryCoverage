package funkutil

import (
	"debug/elf"

	"github.com/ianlancetaylor/demangle"
)

// SymtabFunctions returns raw (mangled) names of STT_FUNC symbols in f,
// unioning .symtab and .dynsym rather than falling back to .dynsym only on
// a hard error: a present-but-stripped-down .symtab (common for shared
// libraries — glibc's libc.so.6 ships one that omits exported functions
// like dlopen, which live only in .dynsym) would otherwise silently hide
// real, callable functions instead of being found via the union.
//
// keep receives each symbol's demangled, version-stripped name and decides
// whether to include it — callers combine relevance filtering
// (FuncIsRelevant) and any --include/--exclude pattern into one predicate.
//
// Symbols are deduped by both name and address: the same symbol can appear
// in both tables, and Itanium ABI complete-object/base-object ctor-dtor
// pairs (C1/C2, D1/D2) alias one address for classes without virtual
// bases — attaching a uprobe at the same address under two cookies would
// double-count a single call (this is safe even for virtual-base classes,
// where C1/C2 genuinely compile to different code at different addresses).
func SymtabFunctions(f *elf.File, keep func(demangled string) bool) []string {
	// .dynsym first: address-based dedup below keeps whichever name is
	// encountered first at a given address, and a library can have a
	// .symtab-only local alias sharing an exported function's address
	// (observed for real: glibc's libc.so.6 has a "dlopen" entry only in
	// .dynsym, at an address .symtab also references under a different
	// name). Checking .dynsym first ensures the canonical exported name
	// wins that race instead of being silently shadowed.
	var syms []elf.Symbol
	if dynSyms, err := f.DynamicSymbols(); err == nil {
		syms = append(syms, dynSyms...)
	}
	if statSyms, err := f.Symbols(); err == nil {
		syms = append(syms, statSyms...)
	}

	seen := make(map[string]struct{})
	seenAddr := make(map[uint64]struct{})
	var funcs []string
	for _, sym := range syms {
		if elf.ST_TYPE(sym.Info) != elf.STT_FUNC {
			continue
		}
		if sym.Value == 0 || sym.Size == 0 || sym.Name == "" {
			continue
		}
		if _, dup := seenAddr[sym.Value]; dup {
			continue
		}
		if _, dup := seen[sym.Name]; dup {
			continue
		}
		if !keep(demangle.Filter(StripVersion(sym.Name))) {
			continue
		}
		seen[sym.Name] = struct{}{}
		seenAddr[sym.Value] = struct{}{}
		funcs = append(funcs, sym.Name)
	}
	return funcs
}
