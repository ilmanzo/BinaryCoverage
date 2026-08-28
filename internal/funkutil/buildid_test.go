package funkutil

import (
	"debug/elf"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBuildID(t *testing.T) {
	if _, err := exec.LookPath("gcc"); err != nil {
		t.Skip("gcc not found")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name    string
		ldflag  string
		wantErr bool
	}{
		{"present", "-Wl,--build-id=sha1", false},
		{"absent", "-Wl,--build-id=none", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := filepath.Join(tmp, tc.name)
			if out, err := exec.Command("gcc", tc.ldflag, "-o", bin, src).CombinedOutput(); err != nil {
				t.Fatalf("compile: %v\n%s", err, out)
			}
			f, err := elf.Open(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			id, err := BuildID(f)
			if tc.wantErr {
				if err == nil {
					t.Errorf("BuildID = %q, want an error", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildID: %v", err)
			}
			if len(id) != 40 {
				t.Errorf("BuildID = %q, want 40 hex chars (sha1)", id)
			}
		})
	}
}

// TestBuildID_MalformedNote covers the rejections that keep a garbled note from
// being read as a valid build-id. That matters more than it looks: the shim
// compares this string before attaching uprobes at pre-computed addresses, so a
// note it misparses into a stable-looking value would defeat the staleness
// guard and leave a probe landing mid-instruction.
func TestBuildID_MalformedNote(t *testing.T) {
	for _, tool := range []string{"gcc", "objcopy"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not found", tool)
		}
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "main.c")
	if err := os.WriteFile(src, []byte("int main() { return 0; }"), 0644); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(tmp, "base")
	if out, err := exec.Command("gcc", "-Wl,--build-id=none", "-o", base, src).CombinedOutput(); err != nil {
		t.Fatalf("compile: %v\n%s", err, out)
	}

	// note assembles an ELF note: namesz, descsz, type, then the name padded
	// to 4 bytes, then the descriptor.
	note := func(namesz, descsz, noteType uint32, rest ...byte) []byte {
		b := make([]byte, 0, 12+len(rest))
		for _, v := range []uint32{namesz, descsz, noteType} {
			b = binary.NativeEndian.AppendUint32(b, v)
		}
		return append(b, rest...)
	}
	gnu := []byte{'G', 'N', 'U', 0}

	for _, tc := range []struct {
		name string
		data []byte
		want string // empty means "expect an error"
	}{
		{"truncated", []byte{1, 2, 3, 4, 5, 6, 7, 8}, ""},
		{"wrong name size", note(8, 4, 3, 'G', 'N', 'U', 0, 0, 0, 0, 0, 0xde, 0xad, 0xbe, 0xef), ""},
		{"wrong note type", note(4, 4, 1, append(gnu, 0xde, 0xad, 0xbe, 0xef)...), ""},
		{"descsz overflows", note(4, 0xffff, 3, append(gnu, 0xde, 0xad, 0xbe, 0xef)...), ""},
		{"well formed", note(4, 4, 3, append(gnu, 0xde, 0xad, 0xbe, 0xef)...), "deadbeef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blob := filepath.Join(tmp, "note.bin")
			if err := os.WriteFile(blob, tc.data, 0644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(tmp, "with-note")
			cmd := exec.Command("objcopy", "--add-section", ".note.gnu.build-id="+blob, base, bin)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("objcopy: %v\n%s", err, out)
			}
			f, err := elf.Open(bin)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()

			id, err := BuildID(f)
			if tc.want == "" {
				if err == nil {
					t.Errorf("BuildID = %q, want an error", id)
				}
				return
			}
			if err != nil {
				t.Fatalf("BuildID: %v", err)
			}
			if id != tc.want {
				t.Errorf("BuildID = %q, want %q", id, tc.want)
			}
		})
	}
}
