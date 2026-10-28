package funkutil

import (
	"debug/elf"
	"encoding/hex"
	"fmt"
)

// BuildID returns the hex-encoded GNU build-id from f's .note.gnu.build-id
// section. It identifies the exact build of an image, which is what makes it
// usable both to find the matching separate debug file and to detect that a
// library was upgraded out from under a set of pre-computed uprobe offsets.
func BuildID(f *elf.File) (string, error) {
	sec := f.Section(".note.gnu.build-id")
	if sec == nil {
		return "", fmt.Errorf("no build-id section")
	}
	data, err := sec.Data()
	if err != nil {
		return "", err
	}
	if len(data) < 16 {
		return "", fmt.Errorf("malformed note")
	}
	namesz, descsz, noteType := f.ByteOrder.Uint32(data), f.ByteOrder.Uint32(data[4:]), f.ByteOrder.Uint32(data[8:])
	if namesz != 4 || noteType != 3 {
		return "", fmt.Errorf("not a gnu build id note")
	}
	// descsz is attacker-controlled (any ELF install or a library can carry
	// one); compare in uint64 so 16+descsz cannot wrap around uint32.
	if uint64(descsz) > uint64(len(data)-16) {
		return "", fmt.Errorf("note descsz overflows section")
	}
	return hex.EncodeToString(data[16 : 16+descsz]), nil
}
