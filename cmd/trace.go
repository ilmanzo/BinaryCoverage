package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"funkoverage/internal/funkutil"
)

// traceInline runs a binary under tracing without permanent installation.
// Writes _functions.log, creates a temporary real-binary symlink in
// SAFE_BIN_DIR so the shim's path-convention lookup works, invokes the
// shim, then cleans up.
func traceInline(binaryPath string, args []string, libScope LibScope, filter *funkutil.FuncFilter) (int, error) {
	logDir := funkutil.LogDir()
	safeBinDir := funkutil.SafeBinDir()

	realBin, err := filepath.EvalSymlinks(binaryPath)
	if err != nil {
		return 1, fmt.Errorf("resolve %s: %w", binaryPath, err)
	}
	if !isELF(realBin) {
		return 1, fmt.Errorf("'%s' is not an ELF executable", binaryPath)
	}

	shimBinary, err := findShimBinary()
	if err != nil {
		return 1, err
	}

	funcs, err := enumerateFuncs(realBin, filepath.Base(realBin), logDir, libScope, filter)
	if err != nil {
		return 1, err
	}

	if err := os.MkdirAll(safeBinDir, 0755); err != nil {
		return 1, err
	}
	safePath := filepath.Join(safeBinDir, filepath.Base(realBin))

	tempSafe := false
	if _, err := os.Stat(safePath); err != nil {
		if err := os.Symlink(realBin, safePath); err != nil {
			return 1, fmt.Errorf("create temp safe entry: %w", err)
		}
		tempSafe = true
	}

	tempFuncs := false
	if len(funcs) > 0 {
		if err := funkutil.WriteFuncList(safePath, funcs); err == nil {
			tempFuncs = true
		}
	}

	tempFilter := false
	if err := funkutil.WriteFilterSidecar(safePath, filter.Sidecar()); err == nil {
		tempFilter = true
	}

	defer func() {
		if tempSafe {
			os.Remove(safePath)
		}
		if tempFuncs {
			_ = funkutil.WriteFuncList(safePath, nil)
		}
		if tempFilter {
			_ = funkutil.WriteFilterSidecar(safePath, funkutil.FilterSidecar{})
		}
	}()

	cmd := exec.Command(shimBinary, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		"SAFE_BIN_DIR="+safeBinDir,
		"LOG_DIR="+logDir,
		"FUNKOVERAGE_ARG0="+binaryPath,
		"FUNKOVERAGE_BINARY_NAME="+filepath.Base(realBin),
	)

	if err := cmd.Run(); err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return exitErr.ExitCode(), nil
		}
		return 1, err
	}
	return 0, nil
}
