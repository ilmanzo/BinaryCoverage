package main

import (
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"funkoverage/internal/funkutil"
)

// preservedModeBits are the mode bits that must survive a merge/copy/move so
// a shimmed binary's original privilege semantics (setuid tools, a
// group-writable setgid service binary, ...) keep working exactly as before
// instrumentation — losing them silently breaks the wrapped program's
// functionality, not just its coverage numbers.
const preservedModeBits = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky

// chownLike applies path's owner/group to match info, best-effort: requires
// root/CAP_CHOWN, and a failure (e.g. a non-root test run) must not be
// fatal — mode is what governs exec/setuid semantics, ownership is
// secondary fidelity (ls -l, rpm -V, and "nothing changes but the
// wrapping"). Callers MUST chown before the final chmod: a successful
// chown clears any setuid/setgid bit already on the file (the kernel does
// this to prevent ownership-change privilege tricks), so setting those
// bits must be the last step, not this one.
func chownLike(path string, info os.FileInfo) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		_ = os.Chown(path, int(stat.Uid), int(stat.Gid))
	}
}

var globalDebugRoot = "/usr/lib/debug"

func isELF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	magic := make([]byte, 4)
	// io.ReadFull, not f.Read: a short file can return fewer than 4 bytes
	// with a nil error, which would otherwise compare the ELF magic against
	// a zero-padded buffer instead of correctly reporting "not ELF".
	if _, err := io.ReadFull(f, magic); err != nil {
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

	// externalDebugPath's first step is the same .build-id lookup, so a
	// separate check here would only ever duplicate it.
	return externalDebugPath(path) != "", nil
}

func buildIDDebugPath(buildID string) string {
	return fmt.Sprintf("%s/.build-id/%s/%s.debug", globalDebugRoot, buildID[:2], buildID[2:])
}

// restoreLibraryBackups puts back the system libraries that a pre-0.8.4
// install unstripped in place, per safePath's backup sidecar.
//
// Nothing writes that sidecar any more — libraries are attached by address
// now (see funkutil.SymbolFileOffsets) and are never modified. This is purely
// so that uninstalling a shim installed by an older funkoverage still leaves
// the host as it found it. Drop it, along with internal/funkutil/libbackup.go,
// one release after 0.8.4.
func restoreLibraryBackups(safePath string) {
	backups := funkutil.ReadLibBackups(safePath)
	for lib, backupPath := range backups {
		if err := move(backupPath, lib); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restore %s: %v\n", lib, err)
		}
	}
	_ = funkutil.WriteLibBackups(safePath, nil)
}

// resolveDebugFile returns the external debug file for binPath, or "".
// origPath is binPath's original absolute location (equal to binPath unless
// the binary has already been moved to SAFE_BIN_DIR) — .gnu_debuglink
// resolves relative to it. Tries, in order: .build-id, .gnu_debuglink (the
// standard GNU separate-debug convention), then .gnu_debugaltlink
// (dwz-compressed).
func resolveDebugFile(binPath, origPath string) (string, error) {
	f, err := elf.Open(binPath)
	if err != nil {
		return "", fmt.Errorf("open elf: %w", err)
	}
	defer f.Close()

	if buildID, err := funkutil.BuildID(f); err == nil && len(buildID) > 2 {
		debugPath := buildIDDebugPath(buildID)
		if _, err := os.Stat(debugPath); err == nil {
			return debugPath, nil
		}
	}

	if linkPath := debugLinkPath(origPath, readGnuDebugLink(f)); linkPath != "" {
		if _, err := os.Stat(linkPath); err == nil {
			return linkPath, nil
		}
	}

	if altPath := readGnuDebugAltLink(f); altPath != "" {
		if debugPath := findDebugFile(altPath); debugPath != "" {
			return debugPath, nil
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

// readGnuDebugLink extracts the debug file basename from the standard
// .gnu_debuglink section (null-terminated name, then a padded CRC32 we
// don't need here).
func readGnuDebugLink(f *elf.File) string {
	sec := f.Section(".gnu_debuglink")
	if sec == nil {
		return ""
	}
	data, err := sec.Data()
	if err != nil || len(data) == 0 {
		return ""
	}
	if i := bytes.IndexByte(data, 0); i > 0 {
		return string(data[:i])
	}
	return ""
}

// debugLinkPath resolves the standard GNU debug-link convention used by
// rpm/dpkg debuginfo packages: <debugRoot>/<canonical-dir-of-binary>/<name>.
// The directory must be canonicalized (e.g. /lib64 -> /usr/lib64 on
// merged-/usr systems) since that's where debuginfo packages actually
// install their files.
func debugLinkPath(origPath, linkName string) string {
	if linkName == "" {
		return ""
	}
	dir := filepath.Dir(origPath)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return filepath.Join(globalDebugRoot, dir, linkName)
}

// findDebugFile locates a debug file given an alt-link path.
func findDebugFile(altPath string) string {
	// Try direct path
	if _, err := os.Stat(altPath); err == nil {
		return altPath
	}
	// Try relative to globalDebugRoot
	relPath := filepath.Join(globalDebugRoot, altPath)
	if _, err := os.Stat(relPath); err == nil {
		return relPath
	}
	// Try globalDebugRoot/.dwz/<basename>
	if base := filepath.Base(altPath); base != altPath {
		dwzPath := filepath.Join(globalDebugRoot, ".dwz", base)
		if _, err := os.Stat(dwzPath); err == nil {
			return dwzPath
		}
	}
	return ""
}

func move(source, destination string) error {
	err := os.Rename(source, destination)
	if errors.Is(err, syscall.EXDEV) {
		return moveCrossDevice(source, destination)
	}
	return err
}

// moveCrossDevice copies source to destination (mode, mtime, and, best-effort,
// ownership included — see copyFile) and removes source, for the rare case
// where SAFE_BIN_DIR and the original binary's directory are on different
// filesystems and os.Rename can't do it atomically.
func moveCrossDevice(source, destination string) error {
	fi, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("stat: %w", err)
	}
	if err := copyFile(source, destination, fi); err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	_ = os.Chtimes(destination, fi.ModTime(), fi.ModTime())
	return os.Remove(source)
}
