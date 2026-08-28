package funkutil

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildSplitDebugLib compiles a shared library, splits its debug info out with
// objcopy --only-keep-debug, then strips it — the exact shape SymbolFileOffsets
// exists for: a runtime file that has lost .symtab, and a debug companion that
// has the symbols but whose own p_offset values are meaningless.
func buildSplitDebugLib(t *testing.T) (runtime, debug string) {
	t.Helper()
	for _, tool := range []string{"gcc", "objcopy", "strip"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "lib.c")
	var code strings.Builder
	// The exported bulk is not decoration: it pushes .dynsym/.dynstr/.rela
	// past one page so the executable segment starts at a file offset above
	// 0x1000. Below that threshold the runtime and debug files agree on
	// p_offset by accident and the cross-check below becomes vacuous —
	// assertDiscriminating enforces that it did not.
	for i := range 300 {
		fmt.Fprintf(&code, "int funkoverage_probe_padding_function_%03d(void) { return %d; }\n", i, i)
	}
	code.WriteString(`static int local_a(void) { return 1; }
static int local_b(void) { return local_a() + 1; }
int public_func(void) { return local_b(); }
`)
	if err := os.WriteFile(src, []byte(code.String()), 0644); err != nil {
		t.Fatal(err)
	}
	runtime = filepath.Join(tmp, "libtest.so")
	if out, err := exec.Command("gcc", "-shared", "-fPIC", "-Wl,--build-id", "-o", runtime, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}
	debug = runtime + ".debug"
	if out, err := exec.Command("objcopy", "--only-keep-debug", runtime, debug).CombinedOutput(); err != nil {
		t.Fatalf("objcopy --only-keep-debug: %v\n%s", err, out)
	}
	if out, err := exec.Command("strip", runtime).CombinedOutput(); err != nil {
		t.Fatalf("strip: %v\n%s", err, out)
	}
	return runtime, debug
}

// sectionFileOffset converts a virtual address using section headers instead of
// program headers. It is an independent derivation of the same number, so it
// catches a conversion that reads the wrong file's headers — which is the one
// mistake here that produces a running uprobe at a wrong address rather than an
// error.
func sectionFileOffset(t *testing.T, f *elf.File, vaddr uint64) uint64 {
	t.Helper()
	for _, sec := range f.Sections {
		if sec.Flags&elf.SHF_ALLOC == 0 || sec.Type == elf.SHT_NOBITS {
			continue
		}
		if sec.Addr <= vaddr && vaddr < sec.Addr+sec.Size {
			return sec.Offset + (vaddr - sec.Addr)
		}
	}
	t.Fatalf("no allocated section contains vaddr %#x", vaddr)
	return 0
}

// assertDiscriminating fails unless the two files disagree about where the
// executable segment sits in the file while agreeing on its virtual address.
// That disagreement is the trap SymbolFileOffsets' two-file signature exists to
// prevent, and a fixture that does not exhibit it proves nothing.
func assertDiscriminating(t *testing.T, runtime, debug *elf.File) {
	t.Helper()
	execSeg := func(f *elf.File) *elf.Prog {
		for _, p := range f.Progs {
			if p.Type == elf.PT_LOAD && p.Flags&elf.PF_X != 0 {
				return p
			}
		}
		t.Fatal("no executable PT_LOAD segment")
		return nil
	}
	r, d := execSeg(runtime), execSeg(debug)
	if r.Vaddr != d.Vaddr {
		t.Fatalf("fixture: exec segment vaddr differs (%#x vs %#x); the conversion assumes it does not", r.Vaddr, d.Vaddr)
	}
	if r.Off == d.Off {
		t.Fatalf("fixture: both files place the exec segment at %#x, so the cross-check cannot detect using the wrong one", r.Off)
	}
}

func TestSymbolFileOffsets(t *testing.T) {
	runtimePath, debugPath := buildSplitDebugLib(t)

	rf, err := elf.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	df, err := elf.Open(debugPath)
	if err != nil {
		t.Fatal(err)
	}
	defer df.Close()
	assertDiscriminating(t, rf, df)

	offsets := SymbolFileOffsets(rf, df)

	// The whole point: functions that stripping removed from the runtime file
	// are still reachable, by address.
	resolvable := ResolvableFuncNames(rf)
	for _, name := range []string{"local_a", "local_b"} {
		if _, ok := resolvable[name]; ok {
			t.Fatalf("test setup: %s should be unresolvable in the stripped library", name)
		}
		if _, ok := offsets[name]; !ok {
			t.Errorf("%s missing from SymbolFileOffsets", name)
		}
	}

	// Cross-check every offset against the section-header derivation. Both
	// walk the runtime file; feeding the debug file's program headers to the
	// conversion under test shows up here as a constant skew.
	syms, err := df.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, sym := range syms {
		off, ok := offsets[sym.Name]
		if !ok {
			continue
		}
		if want := sectionFileOffset(t, rf, sym.Value); off != want {
			t.Errorf("%s: offset %#x, section headers say %#x", sym.Name, off, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("cross-checked no symbols at all")
	}
}

// A debug file whose build-id disagrees with the library beside it is not a
// corrupted install — openSUSE Leap 16 ships libopenssl3-debuginfo built from a
// different build of libcrypto than the one on disk (780e425a… vs b844407f…,
// with a 0x310-byte difference in exec segment size). Its symbol values describe
// other code, so the offsets derived from them land mid-instruction: `openssl
// version` died with SIGSEGV until this pairing check went in. Losing the
// debug-only functions is the correct trade.
func TestSymbolFileOffsets_RequiresMatchingBuildIDs(t *testing.T) {
	if _, err := exec.LookPath("objcopy"); err != nil {
		t.Skip("objcopy not found")
	}
	runtimePath, debugPath := buildSplitDebugLib(t)
	rf, err := elf.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()

	// Vacuity check: the untouched pair must yield offsets, or a guard that
	// rejects everything would pass the cases below.
	df, err := elf.Open(debugPath)
	if err != nil {
		t.Fatal(err)
	}
	defer df.Close()
	if len(SymbolFileOffsets(rf, df)) == 0 {
		t.Fatal("matched pair produced no offsets; the rejections below prove nothing")
	}

	note := make([]byte, 0, 36)
	for _, v := range []uint32{4, 20, 3} {
		note = binary.NativeEndian.AppendUint32(note, v)
	}
	note = append(note, 'G', 'N', 'U', 0)
	note = append(note, bytes.Repeat([]byte{0xaa}, 20)...)
	blob := filepath.Join(t.TempDir(), "note.bin")
	if err := os.WriteFile(blob, note, 0644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"mismatched", []string{"--update-section", ".note.gnu.build-id=" + blob}},
		{"debug file has no note", []string{"--remove-section", ".note.gnu.build-id"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			altered := filepath.Join(t.TempDir(), "altered.debug")
			args := append(append([]string{}, tc.args...), debugPath, altered)
			if out, err := exec.Command("objcopy", args...).CombinedOutput(); err != nil {
				t.Fatalf("objcopy: %v\n%s", err, out)
			}
			af, err := elf.Open(altered)
			if err != nil {
				t.Fatal(err)
			}
			defer af.Close()

			if got := SymbolFileOffsets(rf, af); len(got) != 0 {
				t.Errorf("SymbolFileOffsets returned %d offsets from an unpaired debug file; want none", len(got))
			}
		})
	}
}

// The conversion must reject a virtual address that lands in no executable
// segment rather than passing it through: a vaddr handed to the kernel as a
// file offset is a uprobe at an arbitrary address.
func TestFileOffset_RejectsUnmappedVaddr(t *testing.T) {
	runtimePath, _ := buildSplitDebugLib(t)
	f, err := elf.Open(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if off, ok := fileOffset(f, 1<<40); ok {
		t.Errorf("fileOffset on an unmapped vaddr = %#x, true; want false", off)
	}
}
