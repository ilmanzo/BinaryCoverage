package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// traceInline runs a binary under tracing without permanent installation.
// Writes _functions.log, creates a temporary real-binary entry in SAFE_BIN_DIR
// pointing at the original path, invokes the shim, then cleans up.
func traceInline(binaryPath string, args []string, noLibs bool) error {
	LOG_DIR := os.Getenv("LOG_DIR")
	if LOG_DIR == "" {
		LOG_DIR = defaultLogDir
	}
	SAFE_BIN_DIR := os.Getenv("SAFE_BIN_DIR")
	if SAFE_BIN_DIR == "" {
		SAFE_BIN_DIR = defaultSafeBinDir
	}

	realBin, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", binaryPath, err)
	}
	if !isELF(realBin) {
		return fmt.Errorf("'%s' is not an ELF executable", binaryPath)
	}

	shimBinary, err := findShimBinary()
	if err != nil {
		return err
	}

	// Enumerate functions and write _functions.log
	funcs, err := EnumerateFunctions(realBin, noLibs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "trace: function enumeration warning: %v\n", err)
	} else {
		if _, err := writeFunctionsLog(LOG_DIR, filepath.Base(realBin), funcs); err != nil {
			fmt.Fprintf(os.Stderr, "trace: write functions log warning: %v\n", err)
		}
	}

	if err := os.MkdirAll(SAFE_BIN_DIR, 0755); err != nil {
		return err
	}

	basename := filepath.Base(realBin)
	safePath := filepath.Join(SAFE_BIN_DIR, basename)

	// For inline trace, the "real binary" stays in place — we create a symlink
	// in SAFE_BIN_DIR so the shim can find it by convention.
	tempSafe := false
	if _, err := os.Stat(safePath); err != nil {
		if err := os.Symlink(realBin, safePath); err != nil {
			return fmt.Errorf("create temp safe entry: %w", err)
		}
		tempSafe = true
	}

	// Write library list for bpftrace script
	libsPath := safePath + ".libs.json"
	tempLibs := false
	if !noLibs {
		if libs, err := ParseLddLibraries(realBin); err == nil && len(libs) > 0 {
			if data, err := json.Marshal(libs); err == nil {
				if writeErr := os.WriteFile(libsPath, data, 0644); writeErr == nil {
					tempLibs = true
				}
			}
		}
	}

	defer func() {
		if tempSafe {
			os.Remove(safePath)
		}
		if tempLibs {
			os.Remove(libsPath)
		}
	}()

	// Invoke shim: it reads real binary path from SAFE_BIN_DIR/<basename>
	cmd := exec.Command(shimBinary, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		"SAFE_BIN_DIR="+SAFE_BIN_DIR,
		"LOG_DIR="+LOG_DIR,
		"FUNKOVERAGE_ARG0="+binaryPath,
	)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
	}
	return nil
}
