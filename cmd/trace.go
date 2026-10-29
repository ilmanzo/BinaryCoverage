package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"funkoverage/internal/funkutil"
)

// refuseInstalled errors unless safePath is free or holds only the scratch
// symlink an earlier, interrupted trace left behind. Anything else is an
// installed shim's original (or an unrelated backup of the same name), and
// traceInline's cleanup would delete the sidecars that shim traces from.
func refuseInstalled(safePath, realBin string) error {
	fi, err := os.Lstat(safePath)
	if err != nil {
		return nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		if target, err := filepath.EvalSymlinks(safePath); err == nil && target == realBin {
			return nil
		}
	}
	return fmt.Errorf("%s is already taken (installed shim?); run the binary directly, or uninstall it first", safePath)
}

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

	safePath := filepath.Join(safeBinDir, filepath.Base(realBin))
	if err := refuseInstalled(safePath, realBin); err != nil {
		return 1, fmt.Errorf("'%s': %w", binaryPath, err)
	}

	shimBinary, err := findShimBinary()
	if err != nil {
		return 1, err
	}

	funcs, err := enumerateFuncs(realBin, realBin, filepath.Base(realBin), logDir, libScope, filter)
	if err != nil {
		return 1, err
	}

	if err := os.MkdirAll(safeBinDir, 0755); err != nil {
		return 1, err
	}

	tempSafe := false
	if _, err := os.Lstat(safePath); err != nil {
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
			return shellExitCode(exitErr), nil
		}
		return 1, err
	}
	return 0, nil
}

// shellExitCode is e's exit status, or 128+signo when a signal killed the
// process — ExitError.ExitCode reports -1 for that, which os.Exit turns into 255.
func shellExitCode(e *exec.ExitError) int {
	if ws, ok := e.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return e.ExitCode()
}
