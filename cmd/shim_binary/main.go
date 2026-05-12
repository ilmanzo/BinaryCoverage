package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"funkoverage/internal/funkutil"

	"github.com/ianlancetaylor/demangle"
)

const (
	activeEnvVar = "FUNKOVERAGE_ACTIVE"
	childEnvVar  = "FUNKOVERAGE_CHILD"
	waitFdEnvVar = "FUNKOVERAGE_WAIT_FD"
	arg0EnvVar   = "FUNKOVERAGE_ARG0"
)

func realBinaryPath() string {
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

	// Recursion guard: already tracing, exec real binary directly.
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
		os.Exit(1)
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

func runWithTracing(realBin string) (exitCode int, err error) {
	dir := funkutil.LogDir()
	if err := os.MkdirAll(dir, 0777); err != nil {
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
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
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

	scriptPath, err := writeBpftraceScript(realBin, childCmd.Process.Pid)
	if err != nil {
		return 1, err
	}
	cleanups = append(cleanups, func() { os.Remove(scriptPath) })

	calledLog, err := openCalledLog(dir, realBin)
	if err != nil {
		return 1, err
	}
	// calledLog is closed by the stdout-capture goroutine after bpftrace exits.

	bpfCmd := exec.Command("bpftrace", scriptPath)
	bpfPipe, _ := bpfCmd.StdoutPipe()
	bpfStderr, _ := bpfCmd.StderrPipe()
	if err := bpfCmd.Start(); err != nil {
		calledLog.Close()
		return 1, fmt.Errorf("start bpftrace: %w", err)
	}
	cleanups = append(cleanups, func() {
		if bpfCmd.Process != nil && bpfCmd.ProcessState == nil {
			_ = bpfCmd.Process.Signal(syscall.SIGINT)
			_, _ = bpfCmd.Process.Wait()
		}
	})

	go captureCalledLog(bpfPipe, calledLog)
	waitForAttach(bpfStderr, attachTimeoutDuration())

	if _, err := pipeW.Write([]byte{1}); err != nil {
		return 1, fmt.Errorf("signal child: %w", err)
	}
	pipeW.Close()

	state, _ := childCmd.Process.Wait()
	if state != nil {
		exitCode = state.ExitCode()
	}

	if bpfCmd.Process != nil {
		_ = bpfCmd.Process.Signal(syscall.SIGINT)
		_ = bpfCmd.Wait()
	}
	return exitCode, nil
}

// writeBpftraceScript renders the bpftrace script for realBin/childPID and
// writes it to a temp file, returning the path.
func writeBpftraceScript(realBin string, childPID int) (string, error) {
	f, err := os.CreateTemp("", "funkoverage-*.bt")
	if err != nil {
		return "", fmt.Errorf("create script temp: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(generateBpftraceScript(realBin, childPID)); err != nil {
		os.Remove(f.Name())
		return "", fmt.Errorf("write script: %w", err)
	}
	return f.Name(), nil
}

// openCalledLog opens a freshly timestamped <basename>_*_called.log in dir.
func openCalledLog(dir, realBin string) (*os.File, error) {
	ts := time.Now()
	name := fmt.Sprintf("%s_%s_%d_called.log",
		filepath.Base(realBin), ts.Format("20060102-150405"), ts.UnixNano())
	return os.Create(filepath.Join(dir, name))
}

// captureCalledLog reads bpftrace stdout, demangles each "CALLED" symbol, and
// writes it to calledLog. Closes calledLog on EOF.
func captureCalledLog(r io.Reader, calledLog *os.File) {
	defer calledLog.Close()
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "CALLED ") {
			continue
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) != 3 {
			continue
		}
		demangled := demangle.Filter(funkutil.StripVersion(parts[2]))
		fmt.Fprintf(calledLog, "CALLED %s %s\n", parts[1], demangled)
	}
}

// waitForAttach blocks until bpftrace prints "Attaching N probes..." on stderr
// or the timeout fires. Either way, returns: the child must be unblocked even
// if attachment is incomplete.
func waitForAttach(r io.Reader, timeout time.Duration) {
	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "Attaching") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}
		close(ready)
	}()
	select {
	case <-ready:
	case <-time.After(timeout):
		fmt.Fprintln(os.Stderr, "funkoverage-shim: warning: bpftrace attach timeout, proceeding untraced")
	}
}

func buildChildEnv(realBin string) []string {
	env := cleanEnv()
	env = append(env,
		childEnvVar+"=1",
		waitFdEnvVar+"=3",
		arg0EnvVar+"="+os.Args[0],
		activeEnvVar+"=1",
		"SAFE_BIN_DIR="+funkutil.SafeBinDir(),
	)
	if v := os.Getenv("LOG_DIR"); v != "" {
		env = append(env, "LOG_DIR="+v)
	}
	return env
}

func cleanEnv() []string {
	skip := map[string]bool{childEnvVar: true, waitFdEnvVar: true, arg0EnvVar: true}
	var env []string
	for _, e := range os.Environ() {
		k := strings.SplitN(e, "=", 2)[0]
		if !skip[k] {
			env = append(env, e)
		}
	}
	return env
}

func generateBpftraceScript(realBin string, childPID int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "BEGIN { @watched[%d] = 1; }\n\n", childPID)

	writeUprobeBlock(&sb, realBin, "called")
	for i, lib := range funkutil.ReadLibsSidecar(realBin) {
		writeUprobeBlock(&sb, lib, fmt.Sprintf("lcalled%d", i))
	}

	sb.WriteString("tracepoint:sched:sched_process_fork {\n")
	sb.WriteString("    if (@watched[args->parent_pid]) { @watched[args->child_pid] = 1; }\n")
	sb.WriteString("}\n\n")
	sb.WriteString("tracepoint:sched:sched_process_exit { delete(@watched[pid]); }\n")

	return sb.String()
}

// writeUprobeBlock emits one bpftrace uprobe block. The image path is passed
// as a bpftrace string argument to printf so paths containing '%' do not act
// as format directives.
func writeUprobeBlock(sb *strings.Builder, imagePath, mapName string) {
	fmt.Fprintf(sb, "uprobe:%s:* {\n", imagePath)
	sb.WriteString("    if (!@watched[pid]) { return; }\n")
	fmt.Fprintf(sb, "    if (@%s[func]) { return; }\n", mapName)
	fmt.Fprintf(sb, "    @%s[func] = 1;\n", mapName)
	fmt.Fprintf(sb, "    printf(\"CALLED %%s %%s\\n\", %s, func);\n", bpfQuote(imagePath))
	sb.WriteString("}\n\n")
}

// bpfQuote returns a bpftrace string literal: surrounding quotes, with
// backslashes and double-quotes escaped.
func bpfQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

func attachTimeoutDuration() time.Duration {
	if v := os.Getenv("FUNKOVERAGE_ATTACH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 60 * time.Second
}

