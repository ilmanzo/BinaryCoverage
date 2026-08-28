package funkutil

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
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
	var namesz, descsz, noteType uint32
	reader := bytes.NewReader(data)
	for _, p := range []*uint32{&namesz, &descsz, &noteType} {
		if err := binary.Read(reader, f.ByteOrder, p); err != nil {
			return "", fmt.Errorf("read note header: %w", err)
		}
	}
	if namesz != 4 || noteType != 3 {
		return "", fmt.Errorf("not a gnu build id note")
	}
	if int(16+descsz) > len(data) {
		return "", fmt.Errorf("note descsz overflows section")
	}
	return hex.EncodeToString(data[16 : 16+descsz]), nil
}
