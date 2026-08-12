package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"funkoverage/internal/funkutil"
)

const (
	activeEnvPrefix = "FUNKOVERAGE_ACTIVE_"
	childEnvVar     = "FUNKOVERAGE_CHILD"
	waitFdEnvVar    = "FUNKOVERAGE_WAIT_FD"
	arg0EnvVar      = "FUNKOVERAGE_ARG0"
)

func realBinaryPath() string {
	// If FUNKOVERAGE_BINARY_NAME is set (from trace command), use it directly.
	if name := os.Getenv("FUNKOVERAGE_BINARY_NAME"); name != "" {
		return filepath.Join(funkutil.SafeBinDir(), name)
	}
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Join(funkutil.SafeBinDir(), filepath.Base(exe))
}

func main() {
	if os.Getenv(childEnvVar) == "1" {
		childMain()
		return
	}

	realBin := realBinaryPath()

	// Recursion guard: already tracing this specific binary, exec real binary directly.
	activeEnvVar := activeEnvPrefix + envSafeName(filepath.Base(realBin))
	if os.Getenv(activeEnvVar) != "" {
		if err := syscall.Exec(realBin, os.Args, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "funkoverage-shim: exec %s: %v\n", realBin, err)
			os.Exit(1)
		}
		return
	}

	exitCode, err := runWithTracing(realBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: %v\n", err)
		os.Exit(exitCode)
	}
	os.Exit(exitCode)
}

func childMain() {
	waitFdStr := os.Getenv(waitFdEnvVar)
	if waitFdStr == "" {
		fmt.Fprintln(os.Stderr, "funkoverage-shim child: missing FUNKOVERAGE_WAIT_FD")
		os.Exit(1)
	}
	fd, err := strconv.Atoi(waitFdStr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim child: bad fd: %v\n", err)
		os.Exit(1)
	}
	waitFile := os.NewFile(uintptr(fd), "wait-pipe")
	buf := make([]byte, 1)
	if _, err := waitFile.Read(buf); err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim child: wait-pipe read: %v\n", err)
		os.Exit(1)
	}
	waitFile.Close()

	realBin := realBinaryPath()
	argv0 := os.Getenv(arg0EnvVar)
	if argv0 == "" {
		argv0 = realBin
	}
	args := append([]string{argv0}, os.Args[1:]...)
	env := cleanEnv()
	if err := syscall.Exec(realBin, args, env); err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim child: exec %s: %v\n", realBin, err)
		os.Exit(1)
	}
}

// runWithTracing forks the real binary as a paused child, attaches the eBPF
// tracer scoped to the child's TGID, then unblocks the child. The tracer
// runs concurrently and writes CALLED entries to LOG_DIR until the child
// exits. Returns the child's exit code (or 1 with err on tracer failure).
func runWithTracing(realBin string) (exitCode int, err error) {
	funcs := funkutil.ReadFuncList(realBin)
	if funcs == nil {
		funcs = make(map[string][]string)
	}
	filter := funkutil.ReadFilterSidecar(realBin)

	dir := funkutil.LogDir()
	if err := funkutil.EnsureLogDir(dir); err != nil {
		return 1, fmt.Errorf("create log dir: %w", err)
	}

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return 1, fmt.Errorf("pipe: %w", err)
	}

	// Cleanup is centralised: each successful step appends to `cleanups`,
	// which run in LIFO order on any error return.
	var cleanups []func()
	defer func() {
		for _, cleanup := range slices.Backward(cleanups) {
			cleanup()
		}
	}()
	cleanups = append(cleanups, func() { pipeW.Close() })

	shimExe, _ := os.Executable()
	childCmd := exec.Command(shimExe, os.Args[1:]...)
	childCmd.Stdin, childCmd.Stdout, childCmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	childCmd.ExtraFiles = []*os.File{pipeR}
	childCmd.Env = buildChildEnv(realBin)

	if err := childCmd.Start(); err != nil {
		pipeR.Close()
		return 1, fmt.Errorf("start child: %w", err)
	}
	pipeR.Close()
	cleanups = append(cleanups, func() {
		if childCmd.ProcessState == nil {
			_ = childCmd.Process.Kill()
			_, _ = childCmd.Process.Wait()
		}
	})

	logPath := calledLogPath(dir, realBin)
	tracer, err := NewTracer(funcs, logPath, filter.Include, filter.Exclude)
	if err != nil {
		return 1, err
	}
	cleanups = append(cleanups, func() { _ = tracer.Stop() })

	if err := tracer.Start(uint32(childCmd.Process.Pid)); err != nil {
		return 1, fmt.Errorf("tracer start: %w", err)
	}

	if _, err := pipeW.Write([]byte{1}); err != nil {
		return 1, fmt.Errorf("signal child: %w", err)
	}
	pipeW.Close()

	state, _ := childCmd.Process.Wait()
	if state != nil {
		exitCode = state.ExitCode()
	}
	_ = tracer.Stop()
	return exitCode, nil
}

func calledLogPath(dir, realBin string) string {
	ts := time.Now()
	name := fmt.Sprintf("%s_%s_%d_called.log",
		filepath.Base(realBin), ts.Format("20060102-150405"), ts.UnixNano())
	return filepath.Join(dir, name)
}

func buildChildEnv(realBin string) []string {
	arg0 := os.Getenv(arg0EnvVar)
	if arg0 == "" {
		arg0 = os.Args[0]
	}
	activeEnvVar := activeEnvPrefix + envSafeName(filepath.Base(realBin))
	env := cleanEnv(activeEnvVar)
	env = append(env,
		childEnvVar+"=1",
		waitFdEnvVar+"=3",
		arg0EnvVar+"="+arg0,
		activeEnvVar+"=1",
		"SAFE_BIN_DIR="+funkutil.SafeBinDir(),
	)
	if v := os.Getenv("LOG_DIR"); v != "" {
		env = append(env, "LOG_DIR="+v)
	}
	return env
}

func envSafeName(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		if r >= 'a' && r <= 'z' {
			return r - 32
		}
		return '_'
	}, s)
}

func cleanEnv(extra ...string) []string {
	skip := map[string]bool{childEnvVar: true, waitFdEnvVar: true, arg0EnvVar: true}
	for _, k := range extra {
		skip[k] = true
	}
	src := os.Environ()
	env := make([]string, 0, len(src))
	for _, e := range src {
		k, _, _ := strings.Cut(e, "=")
		if !skip[k] {
			env = append(env, e)
		}
	}
	return env
}
