package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ianlancetaylor/demangle"
)

const (
	activeEnvVar   = "FUNKOVERAGE_ACTIVE"
	childEnvVar    = "FUNKOVERAGE_CHILD"
	waitFdEnvVar   = "FUNKOVERAGE_WAIT_FD"
	arg0EnvVar     = "FUNKOVERAGE_ARG0"
	defaultLogDir  = "/var/coverage/data"
	defaultSafeBin = "/var/coverage/bin"
)

func realBinaryPath() string {
	exe, _ := os.Executable()
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	basename := filepath.Base(exe)
	safeBinDir := os.Getenv("SAFE_BIN_DIR")
	if safeBinDir == "" {
		safeBinDir = defaultSafeBin
	}
	return filepath.Join(safeBinDir, basename)
}

func logDir() string {
	if v := os.Getenv("LOG_DIR"); v != "" {
		return v
	}
	return defaultLogDir
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
	waitFile.Read(buf)
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

func runWithTracing(realBin string) (int, error) {
	dir := logDir()
	if err := os.MkdirAll(dir, 0777); err != nil {
		return 1, fmt.Errorf("create log dir: %w", err)
	}

	pipeR, pipeW, err := os.Pipe()
	if err != nil {
		return 1, fmt.Errorf("pipe: %w", err)
	}

	shimExe, _ := os.Executable()
	childCmd := exec.Command(shimExe, os.Args[1:]...)
	childCmd.Stdin = os.Stdin
	childCmd.Stdout = os.Stdout
	childCmd.Stderr = os.Stderr
	childCmd.ExtraFiles = []*os.File{pipeR}
	childCmd.Env = buildChildEnv(realBin)

	if err := childCmd.Start(); err != nil {
		pipeR.Close()
		pipeW.Close()
		return 1, fmt.Errorf("start child: %w", err)
	}
	pipeR.Close()

	childPID := childCmd.Process.Pid
	script := generateBpftraceScript(realBin, childPID)

	scriptFile, err := os.CreateTemp("", "funkoverage-*.bt")
	if err != nil {
		pipeW.Close()
		childCmd.Process.Kill()
		childCmd.Wait()
		return 1, err
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	scriptFile.WriteString(script)
	scriptFile.Close()

	ts := time.Now()
	calledLogPath := filepath.Join(dir, fmt.Sprintf("%s_%s_%d_called.log",
		filepath.Base(realBin), ts.Format("20060102-150405"), ts.UnixNano()))
	calledLog, err := os.Create(calledLogPath)
	if err != nil {
		pipeW.Close()
		childCmd.Process.Kill()
		childCmd.Wait()
		return 1, err
	}

	bpfArgs := buildBpftraceArgs(scriptPath)
	bpfCmd := exec.Command(bpfArgs[0], bpfArgs[1:]...)
	bpfPipe, _ := bpfCmd.StdoutPipe()
	bpfStderr, _ := bpfCmd.StderrPipe()

	if err := bpfCmd.Start(); err != nil {
		calledLog.Close()
		pipeW.Close()
		childCmd.Process.Kill()
		childCmd.Wait()
		return 1, fmt.Errorf("start bpftrace: %w", err)
	}

	// Capture bpftrace stdout → demangle → called log
	go func() {
		scanner := bufio.NewScanner(bpfPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "CALLED ") {
				parts := strings.SplitN(line, " ", 3)
				if len(parts) == 3 {
					demangled := demangle.Filter(stripVersionSuffix(parts[2]))
					fmt.Fprintf(calledLog, "CALLED %s %s\n", parts[1], demangled)
					continue
				}
			}
		}
		calledLog.Close()
	}()

	// Wait for "Attaching N probes..." then unblock child
	attachTimeout := attachTimeoutDuration()
	ready := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(bpfStderr)
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
	case <-time.After(attachTimeout):
		fmt.Fprintln(os.Stderr, "funkoverage-shim: warning: bpftrace attach timeout, proceeding untraced")
	}

	pipeW.Write([]byte{1})
	pipeW.Close()

	childState, _ := childCmd.Process.Wait()
	exitCode := 0
	if childState != nil {
		exitCode = childState.ExitCode()
	}

	if bpfCmd.Process != nil {
		bpfCmd.Process.Signal(syscall.SIGINT)
		bpfCmd.Wait()
	}

	return exitCode, nil
}

func buildChildEnv(realBin string) []string {
	env := cleanEnv()
	safeBinDir := os.Getenv("SAFE_BIN_DIR")
	if safeBinDir == "" {
		safeBinDir = defaultSafeBin
	}
	env = append(env,
		childEnvVar+"=1",
		waitFdEnvVar+"=3",
		arg0EnvVar+"="+os.Args[0],
		activeEnvVar+"=1",
		"SAFE_BIN_DIR="+safeBinDir,
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

func buildBpftraceArgs(scriptPath string) []string {
	// bpftrace is expected to have CAP_BPF set via 'funkoverage setup'.
	return []string{"bpftrace", scriptPath}
}

func generateBpftraceScript(realBin string, childPID int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "BEGIN { @watched[%d] = 1; }\n\n", childPID)

	fmt.Fprintf(&sb, "uprobe:%s:* {\n", realBin)
	sb.WriteString("    if (!@watched[pid]) { return; }\n")
	sb.WriteString("    if (@called[func]) { return; }\n")
	sb.WriteString("    @called[func] = 1;\n")
	fmt.Fprintf(&sb, "    printf(\"CALLED %s %%s\\n\", func);\n", realBin)
	sb.WriteString("}\n\n")

	// Per-library blocks (from sibling .libs.json)
	for i, lib := range readLibsSidecar(realBin + ".libs.json") {
		mapName := fmt.Sprintf("lcalled%d", i)
		fmt.Fprintf(&sb, "uprobe:%s:* {\n", lib)
		sb.WriteString("    if (!@watched[pid]) { return; }\n")
		fmt.Fprintf(&sb, "    if (@%s[func]) { return; }\n", mapName)
		fmt.Fprintf(&sb, "    @%s[func] = 1;\n", mapName)
		fmt.Fprintf(&sb, "    printf(\"CALLED %s %%s\\n\", func);\n", lib)
		sb.WriteString("}\n\n")
	}

	sb.WriteString("tracepoint:sched:sched_process_fork {\n")
	sb.WriteString("    if (@watched[args->parent_pid]) { @watched[args->child_pid] = 1; }\n")
	sb.WriteString("}\n\n")
	sb.WriteString("tracepoint:sched:sched_process_exit { delete(@watched[pid]); }\n")

	return sb.String()
}

func attachTimeoutDuration() time.Duration {
	if v := os.Getenv("FUNKOVERAGE_ATTACH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 60 * time.Second
}

func stripVersionSuffix(name string) string {
	if i := strings.IndexByte(name, '@'); i >= 0 {
		return name[:i]
	}
	return name
}

func readLibsSidecar(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var libs []string
	if err := json.Unmarshal(data, &libs); err != nil {
		return nil
	}
	return libs
}
