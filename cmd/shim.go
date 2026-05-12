package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	defaultShimSearchDir = "/usr/lib64/coverage-tools"
)

// install moves the real binary to SAFE_BIN_DIR/<basename>, writes a
// _functions.log, and puts the shim binary at the original path.
// No JSON config — the shim finds the real binary by path convention.
func install(targetBinary string, noLibs bool) error {
	LOG_DIR := os.Getenv("LOG_DIR")
	if LOG_DIR == "" {
		LOG_DIR = defaultLogDir
	}
	SAFE_BIN_DIR := os.Getenv("SAFE_BIN_DIR")
	if SAFE_BIN_DIR == "" {
		SAFE_BIN_DIR = defaultSafeBinDir
	}

	shimBinary, err := findShimBinary()
	if err != nil {
		return err
	}

	fileInfo, err := os.Lstat(targetBinary)
	if err != nil {
		return fmt.Errorf("lstat %s: %w", targetBinary, err)
	}
	isSymlink := (fileInfo.Mode() & os.ModeSymlink) != 0
	originalName := filepath.Base(targetBinary)

	realTarget, err := filepath.EvalSymlinks(targetBinary)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}

	binaryName := filepath.Base(realTarget)
	safePath := filepath.Join(SAFE_BIN_DIR, binaryName)

	// Check not already installed
	if _, err := os.Stat(safePath); err == nil {
		return fmt.Errorf("'%s' already has a shim installed (found %s). Use uninstall first", targetBinary, safePath)
	}

	if !isELF(realTarget) {
		return fmt.Errorf("'%s' is not an ELF executable", targetBinary)
	}
	found, err := hasDebugInfo(realTarget)
	if err != nil {
		return fmt.Errorf("debug info check: %w", err)
	}
	if !found {
		return fmt.Errorf("'%s' has no debug information. Install the debug symbols package first", targetBinary)
	}

	if err := os.MkdirAll(SAFE_BIN_DIR, 0755); err != nil {
		return err
	}

	if err := move(realTarget, safePath); err != nil {
		return err
	}
	if err := mergeDebugIfExternal(safePath); err != nil {
		return fmt.Errorf("merge debug symbols: %w", err)
	}

	// Handle multicall symlinks
	if isSymlink && originalName != binaryName {
		symlinkPath := filepath.Join(SAFE_BIN_DIR, originalName)
		if err := os.Symlink(binaryName, symlinkPath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: multicall symlink %s -> %s: %v\n", symlinkPath, binaryName, err)
		}
	}

	// Enumerate functions and write _functions.log
	funcs, err := EnumerateFunctions(safePath, noLibs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: function enumeration: %v\n", err)
	} else {
		if _, err := writeFunctionsLog(LOG_DIR, binaryName, funcs); err != nil {
			fmt.Fprintf(os.Stderr, "warning: write functions log: %v\n", err)
		}
	}

	// Write library list for the shim's bpftrace script
	if !noLibs {
		if libs, err := ParseLddLibraries(safePath); err == nil && len(libs) > 0 {
			if data, err := json.Marshal(libs); err == nil {
				libsPath := safePath + ".libs.json"
				os.WriteFile(libsPath, data, 0644)
			}
		}
	}

	// Copy shim binary to original location
	if err := copyFile(shimBinary, realTarget, 0755); err != nil {
		return fmt.Errorf("install shim binary: %w", err)
	}

	fmt.Printf("Installed shim for %s (original at %s)\n", targetBinary, safePath)
	return nil
}

func uninstall(targetBinary string) error {
	SAFE_BIN_DIR := os.Getenv("SAFE_BIN_DIR")
	if SAFE_BIN_DIR == "" {
		SAFE_BIN_DIR = defaultSafeBinDir
	}

	realTarget, err := filepath.EvalSymlinks(targetBinary)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}

	binaryName := filepath.Base(realTarget)
	safePath := filepath.Join(SAFE_BIN_DIR, binaryName)

	fi, err := os.Lstat(safePath)
	if err != nil {
		return fmt.Errorf("original binary not found at %s: %w", safePath, err)
	}

	sourcePath := safePath
	if (fi.Mode() & os.ModeSymlink) != 0 {
		resolved, err := filepath.EvalSymlinks(safePath)
		if err != nil {
			return fmt.Errorf("resolve backup symlink: %w", err)
		}
		if err := os.Remove(safePath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove backup symlink: %v\n", err)
		}
		sourcePath = resolved
	}

	if err := move(sourcePath, realTarget); err != nil {
		return fmt.Errorf("restore binary: %w", err)
	}
	os.Remove(safePath + ".libs.json")

	fmt.Printf("Uninstalled shim for %s (restored original)\n", targetBinary)
	return nil
}

func installMany(binaries []string, noLibs bool) error {
	var failed []string
	for _, bin := range binaries {
		if err := install(bin, noLibs); err != nil {
			fmt.Fprintf(os.Stderr, "install error for %s: %v\n", bin, err)
			failed = append(failed, bin)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to install: %v", failed)
	}
	return nil
}

func uninstallMany(binaries []string) error {
	var failed []string
	for _, bin := range binaries {
		if err := uninstall(bin); err != nil {
			fmt.Fprintf(os.Stderr, "uninstall error for %s: %v\n", bin, err)
			failed = append(failed, bin)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to uninstall: %v", failed)
	}
	return nil
}

// setupBpftrace grants CAP_BPF, CAP_DAC_READ_SEARCH, and CAP_PERFMON to the
// bpftrace binary so non-root users can trace programs without sudo.
// Must be run as root (once, after bpftrace is installed or upgraded).
func setupBpftrace() error {
	bpftracePath, err := exec.LookPath("bpftrace")
	if err != nil {
		return fmt.Errorf("bpftrace not found in PATH: %w", err)
	}
	caps := "cap_bpf,cap_dac_read_search,cap_perfmon+ep"
	out, err := exec.Command("setcap", caps, bpftracePath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("setcap failed: %w\n%s\nTip: run 'funkoverage setup' as root", err, out)
	}
	fmt.Printf("Set capabilities on %s: %s\n", bpftracePath, caps)
	fmt.Println("Non-root users can now run the shim without sudo.")
	return nil
}

func findShimBinary() (string, error) {
	if v := os.Getenv("FUNKOVERAGE_SHIM"); v != "" {
		if _, err := os.Stat(v); err == nil {
			return v, nil
		}
	}
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		candidate := filepath.Join(filepath.Dir(exe), "funkoverage-shim")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	candidate := filepath.Join(defaultShimSearchDir, "funkoverage-shim")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", errors.New("funkoverage-shim not found. Set FUNKOVERAGE_SHIM env var or place it alongside funkoverage")
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	out.Close()
	if err != nil {
		os.Remove(dst)
		return err
	}
	return os.Chmod(dst, perm)
}
