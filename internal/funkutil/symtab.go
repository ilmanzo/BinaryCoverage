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

// ResolvableFuncNames returns every symbol name in f that a uprobe can be
// attached to by name.
//
// It mirrors cilium/ebpf's Executable exactly, down to the details that look
// like they should not matter: the same .symtab-then-.dynsym order (its cache
// is a map, so a later entry shadows an earlier one under the same name), the
// same STT_FUNC test, and the same rejection of a zero value (an undefined
// import) or a zero size (its offset-within-symbol bounds check rejects even
// offset 0 against a sizeless symbol). Anything cilium would refuse must be
// absent here, because UprobeMulti resolves the whole batch up front and fails
// on the first miss — one stale name costs an entire image's coverage.
func ResolvableFuncNames(f *elf.File) map[string]struct{} {
	cached := make(map[string]elf.Symbol)
	for _, table := range []func() ([]elf.Symbol, error){f.Symbols, f.DynamicSymbols} {
		syms, err := table()
		if err != nil {
			continue
		}
		for _, sym := range syms {
			if elf.ST_TYPE(sym.Info) == elf.STT_FUNC && sym.Name != "" {
				cached[sym.Name] = sym
			}
		}
	}

	names := make(map[string]struct{}, len(cached))
	for name, sym := range cached {
		if sym.Value != 0 && sym.Size != 0 {
			names[name] = struct{}{}
		}
	}
	return names
}

// SymbolFileOffsets maps each traceable function symbol of debug to its offset
// from the start of runtime — the form link.UprobeMultiOptions.Addresses
// wants, and the same conversion cilium performs internally when it resolves a
// name.
//
// Symbol *values* come from debug, program *headers* from runtime, and the two
// are emphatically not interchangeable. An --only-keep-debug companion file
// preserves p_vaddr but not p_offset: its allocated sections are NOBITS, so
// they have no contents to place. For libgmp the executable segment sits at
// p_offset 0x12000 in the library and 0x1000 in its debug file, so converting
// with the debug file's own headers yields an offset 0x11000 too low — a
// uprobe landing mid-instruction, which kills the target with SIGILL rather
// than merely missing coverage. Taking both files is what makes that mistake
// impossible to express.
//
// Symbols outside every executable PT_LOAD segment are omitted rather than
// passed through unconverted: a virtual address handed to the kernel as a file
// offset is exactly the failure above.
//
// The two files must carry the same GNU build-id or nothing is returned. That
// is not belt-and-braces: openSUSE Leap 16 ships libopenssl3-debuginfo built
// from a different build of libcrypto than the library it sits next to
// (780e425a… on disk, b844407f… in the debug file, and different segment
// sizes). Trusting its symbol values put uprobes mid-instruction and killed
// `openssl version` with SIGSEGV. A mismatch here costs the debug-only
// functions; ignoring it costs the traced process.
func SymbolFileOffsets(runtime, debug *elf.File) map[string]uint64 {
	runtimeID, err := BuildID(runtime)
	if err != nil {
		return nil
	}
	if debugID, err := BuildID(debug); err != nil || debugID != runtimeID {
		return nil
	}

	offsets := make(map[string]uint64)
	for _, table := range []func() ([]elf.Symbol, error){debug.DynamicSymbols, debug.Symbols} {
		syms, err := table()
		if err != nil {
			continue
		}
		for _, sym := range syms {
			if elf.ST_TYPE(sym.Info) != elf.STT_FUNC || sym.Name == "" {
				continue
			}
			if sym.Value == 0 || sym.Size == 0 {
				continue
			}
			if _, dup := offsets[sym.Name]; dup {
				continue
			}
			if off, ok := fileOffset(runtime, sym.Value); ok {
				offsets[sym.Name] = off
			}
		}
	}
	return offsets
}

// fileOffset converts a virtual address to an offset within f, using the
// executable PT_LOAD segment that contains it.
func fileOffset(f *elf.File, vaddr uint64) (uint64, bool) {
	for _, prog := range f.Progs {
		if prog.Type != elf.PT_LOAD || prog.Flags&elf.PF_X == 0 {
			continue
		}
		if prog.Vaddr <= vaddr && vaddr < prog.Vaddr+prog.Memsz {
			return vaddr - prog.Vaddr + prog.Off, true
		}
	}
	return 0, false
}
