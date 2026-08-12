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

	return false, nil
}

func buildIDDebugPath(buildID string) string {
	return fmt.Sprintf("%s/.build-id/%s/%s.debug", globalDebugRoot, buildID[:2], buildID[2:])
}

// mergeDebugIfExternal merges external debug symbols into the binary using
// eu-unstrip so DWARF traversal works even on stripped binaries.
// origPath is the binary's original absolute location (before any move to
// SAFE_BIN_DIR), needed to resolve .gnu_debuglink's directory-relative
// convention. Searches: .build-id paths, .gnu_debuglink, and .gnu_debugaltlink.
func mergeDebugIfExternal(binPath, origPath string) error {
	debugPath, err := locateExternalDebugForMerge(binPath, origPath)
	if err != nil || debugPath == "" {
		return err
	}
	return unstrip(binPath, debugPath)
}

// mergeLibraryDebugInfo merges external debug info into each library in
// funcs (in place, at its real system path — every other library-linking
// funkoverage target on the system will pick up the merged copy too) so
// that uprobe attach, which resolves symbol names against the exact file
// the kernel maps at runtime, can find local functions that enumeration
// only discovered via an external debug file. mainImage (the already-moved
// and already-merged target binary) is skipped.
//
// ponytail: no cross-target reference counting — if two installed targets
// share a library, uninstalling one restores it for both. Add a refcount
// sidecar under SAFE_BIN_DIR/libs/ if that becomes a real problem.
//
// Returns the set of libraries actually modified (original path -> backup
// path under SAFE_BIN_DIR/libs/), so uninstall can restore exactly what
// this install call changed.
func mergeLibraryDebugInfo(funcs map[string][]string, mainImage string) map[string]string {
	backups := make(map[string]string)
	for lib := range funcs {
		if lib == mainImage {
			continue
		}
		// ldd reports a library's SONAME path as-is (e.g. /lib64/libgmp.so.10),
		// which is commonly a symlink to the real versioned file
		// (libgmp.so.10.5.0). Operate on the resolved real path so unstrip's
		// final move() never overwrites the symlink itself — os.Rename onto a
		// symlink path replaces the link, not its target, silently turning
		// the SONAME symlink into a plain-file copy forever.
		realLib, err := filepath.EvalSymlinks(lib)
		if err != nil {
			continue
		}
		debugPath, err := locateExternalDebugForMerge(realLib, realLib)
		if err != nil || debugPath == "" {
			continue // no external debug found, or lib is already self-sufficient
		}
		info, err := os.Stat(realLib)
		if err != nil {
			continue
		}
		backupPath := filepath.Join(funkutil.SafeBinDir(), "libs", realLib)
		if err := os.MkdirAll(filepath.Dir(backupPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: backup dir for %s: %v\n", realLib, err)
			continue
		}
		if err := copyFile(realLib, backupPath, info); err != nil {
			fmt.Fprintf(os.Stderr, "warning: back up %s before merge: %v\n", realLib, err)
			continue
		}
		if err := unstrip(realLib, debugPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: merge debug symbols into %s: %v\n", realLib, err)
			os.Remove(backupPath)
			continue
		}
		backups[realLib] = backupPath
	}
	return backups
}

// restoreLibraryBackups reverses mergeLibraryDebugInfo for the libraries
// this specific install call modified, per safePath's backup sidecar.
func restoreLibraryBackups(safePath string) {
	backups := funkutil.ReadLibBackups(safePath)
	for lib, backupPath := range backups {
		if err := move(backupPath, lib); err != nil {
			fmt.Fprintf(os.Stderr, "warning: restore %s: %v\n", lib, err)
		}
	}
	_ = funkutil.WriteLibBackups(safePath, nil)
}

// locateExternalDebugForMerge returns the external debug file path to merge
// into binPath, or "" if the binary already has embedded debug info or no
// external file is found.
func locateExternalDebugForMerge(binPath, origPath string) (string, error) {
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
	info, err := os.Stat(binPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", binPath, err)
	}
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
	// chown, THEN chmod: chown (like write) clears any setuid/setgid bit
	// already present — a kernel security measure against ownership-change
	// privilege tricks — so setting the final mode must come last.
	chownLike(out, info)
	if err := os.Chmod(out, info.Mode()&preservedModeBits); err != nil {
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
