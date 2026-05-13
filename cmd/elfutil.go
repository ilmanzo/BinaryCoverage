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

// hasDebugInfo checks for embedded .debug_* sections or an external debug file
// (.build-id or .dwz-compressed layout).
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
	if err == nil && len(buildID) > 2 {
		debugPath := buildIDDebugPath(buildID)
		if _, err := os.Stat(debugPath); err == nil {
			return true, nil
		}
	}

	if debugPath := externalDebugPath(path); debugPath != "" {
		return true, nil
	}

	// Last resort: scan all .dwz files (handles fully-stripped packages like openssh)
	binName := filepath.Base(path)
	dwzDir := "/usr/lib/debug/.dwz"
	if entries, err := os.ReadDir(dwzDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				// Match by package name prefix, or by partial name matching for common patterns
				name := e.Name()
				if strings.HasPrefix(name, binName) || strings.Contains(name, binName) {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

func buildIDDebugPath(buildID string) string {
	return fmt.Sprintf("%s/.build-id/%s/%s.debug", globalDebugRoot, buildID[:2], buildID[2:])
}

// mergeDebugIfExternal merges external debug symbols into the binary using
// eu-unstrip so DWARF traversal works even on stripped binaries.
// Searches: .build-id paths, .gnu_debugaltlink (dwz), and .dwz/ directory.
func mergeDebugIfExternal(binPath string) error {
	debugPath, err := locateExternalDebugForMerge(binPath)
	if err != nil || debugPath == "" {
		return err
	}
	return unstrip(binPath, debugPath)
}

// locateExternalDebugForMerge returns the external debug file path to merge
// into binPath, or "" if the binary already has embedded debug info or no
// external file is found.
func locateExternalDebugForMerge(binPath string) (string, error) {
	f, err := elf.Open(binPath)
	if err != nil {
		return "", fmt.Errorf("open elf: %w", err)
	}
	defer f.Close()

	for _, s := range f.Sections {
		if (strings.HasPrefix(s.Name, ".debug_") || strings.HasPrefix(s.Name, ".zdebug_")) && s.Size > 0 {
			return "", nil
		}
	}

	if buildID, err := getBuildID(f); err == nil && len(buildID) > 2 {
		debugPath := buildIDDebugPath(buildID)
		if _, err := os.Stat(debugPath); err == nil {
			return debugPath, nil
		}
	}

	if altPath := readGnuDebugAltLink(f); altPath != "" {
		if debugPath := findDebugFile(altPath); debugPath != "" {
			return debugPath, nil
		}
	}

	binName := filepath.Base(binPath)
	dwzDir := "/usr/lib/debug/.dwz"
	if entries, err := os.ReadDir(dwzDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasPrefix(e.Name(), binName) {
				return filepath.Join(dwzDir, e.Name()), nil
			}
		}
	}
	return "", nil
}

// readGnuDebugAltLink extracts the alt file path from .gnu_debugaltlink section.
func readGnuDebugAltLink(f *elf.File) string {
	sec := f.Section(".gnu_debugaltlink")
	if sec == nil {
		return ""
	}
	data, err := sec.Data()
	if err != nil || len(data) == 0 {
		return ""
	}
	// .gnu_debugaltlink contains: null-terminated filename + build-id bytes
	if i := bytes.IndexByte(data, 0); i > 0 {
		return string(data[:i])
	}
	return ""
}

// findDebugFile locates a debug file given an alt-link path.
func findDebugFile(altPath string) string {
	// Try direct path
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
	return ""
}

// unstrip merges debugPath into binPath using eu-unstrip.
// Uses --force to handle ELF type mismatches (PIE binary + relocatable debug).
func unstrip(binPath, debugPath string) error {
	tmp, err := os.CreateTemp(filepath.Dir(binPath), ".unstrip-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	out := tmp.Name()
	tmp.Close()
	os.Remove(out)
	cmd := exec.Command("eu-unstrip", "--force", binPath, debugPath, "-o", out)
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
