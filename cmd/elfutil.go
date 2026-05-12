package main

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var globalDebugRoot = "/usr/lib/debug"

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := f.Read(magic); err != nil {
		return false
	}
	return string(magic) == "\x7fELF"
}

// hasDebugInfo checks for embedded .debug_* sections or an external
// .build-id debug file.
func hasDebugInfo(path string) (bool, error) {
	f, err := elf.Open(path)
	if err != nil {
		return false, fmt.Errorf("failed to open elf: %w", err)
	}
	defer f.Close()

	for _, section := range f.Sections {
		if strings.HasPrefix(section.Name, ".debug_") || strings.HasPrefix(section.Name, ".zdebug_") {
			if section.Size > 0 {
				return true, nil
			}
		}
	}

	buildID, err := getBuildID(f)
	if err != nil {
		return false, nil
	}
	if len(buildID) > 2 {
		debugPath := buildIDDebugPath(buildID)
		if _, err := os.Stat(debugPath); err == nil {
			return true, nil
		}
	}
	return false, nil
}

func buildIDDebugPath(buildID string) string {
	return fmt.Sprintf("%s/.build-id/%s/%s.debug", globalDebugRoot, buildID[:2], buildID[2:])
}

// mergeDebugIfExternal merges external debug symbols into the binary using
// eu-unstrip so DWARF traversal works even on stripped binaries.
func mergeDebugIfExternal(binPath string) error {
	f, err := elf.Open(binPath)
	if err != nil {
		return fmt.Errorf("open elf: %w", err)
	}
	for _, s := range f.Sections {
		if (strings.HasPrefix(s.Name, ".debug_") || strings.HasPrefix(s.Name, ".zdebug_")) && s.Size > 0 {
			f.Close()
			return nil
		}
	}
	buildID, err := getBuildID(f)
	f.Close()
	if err != nil || len(buildID) <= 2 {
		return nil
	}
	debugPath := buildIDDebugPath(buildID)
	if _, err := os.Stat(debugPath); err != nil {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(binPath), ".unstrip-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	out := tmp.Name()
	tmp.Close()
	os.Remove(out)
	cmd := exec.Command("eu-unstrip", binPath, debugPath, "-o", out)
	if combined, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("eu-unstrip failed: %w: %s", err, combined)
	}
	if err := os.Chmod(out, 0755); err != nil {
		os.Remove(out)
		return fmt.Errorf("chmod merged binary: %w", err)
	}
	return move(out, binPath)
}

func getBuildID(f *elf.File) (string, error) {
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

func move(source, destination string) error {
	err := os.Rename(source, destination)
	if err != nil && strings.Contains(err.Error(), "invalid cross-device link") {
		return moveCrossDevice(source, destination)
	}
	return err
}

func moveCrossDevice(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open(source): %w", err)
	}
	dst, err := os.Create(destination)
	if err != nil {
		src.Close()
		return fmt.Errorf("create(destination): %w", err)
	}
	_, err = io.Copy(dst, src)
	src.Close()
	dst.Close()
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	fi, err := os.Stat(source)
	if err != nil {
		os.Remove(destination)
		return fmt.Errorf("stat: %w", err)
	}
	err = os.Chmod(destination, fi.Mode())
	if err != nil {
		os.Remove(destination)
		return fmt.Errorf("chmod: %w", err)
	}
	os.Remove(source)
	return nil
}
