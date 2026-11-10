package main

import (
	"errors"
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
	"golang.org/x/sys/unix"
)

const (
	activeEnvPrefix    = "FUNKOVERAGE_ACTIVE_"
	helperEnvVar       = "FUNKOVERAGE_HELPER"
	intermediateEnvVar = "FUNKOVERAGE_INTERMEDIATE"
	targetPidEnvVar    = "FUNKOVERAGE_TARGET_PID"
	realBinEnvVar      = "FUNKOVERAGE_REAL_BIN"
	arg0EnvVar         = "FUNKOVERAGE_ARG0"
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
	if os.Getenv(helperEnvVar) == "1" {
		helperMain()
		return
	}
	if os.Getenv(intermediateEnvVar) == "1" {
		if err := intermediateMain(); err != nil {
			fmt.Fprintf(os.Stderr, "funkoverage-shim: start coverage helper: %v\n", err)
			os.Exit(1)
		}
		return
	}

	realBin := realBinaryPath()

	// Recursion guard: already tracing this specific binary (e.g. sshd
	// re-execing itself per connection), exec real binary directly rather
	// than spawning a second tracer for a binary that's already covered.
	activeEnvVar := activeEnvPrefix + envSafeName(filepath.Base(realBin))
	if os.Getenv(activeEnvVar) != "" {
		if err := syscall.Exec(realBin, os.Args, os.Environ()); err != nil {
			fmt.Fprintf(os.Stderr, "funkoverage-shim: exec %s: %v\n", realBin, err)
			os.Exit(1)
		}
		return
	}

	if err := runWithTracing(realBin); err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: %v\n", err)
		os.Exit(1)
	}
	// runWithTracing only returns on error: on success it execs the real
	// binary in this same process and never comes back.
}

// runWithTracing spawns a background helper that attaches the eBPF tracer
// to THIS process's pid, waits for the helper to confirm it's attached,
// then execs into the real binary in place.
//
// This process's pid is left unchanged by the exec, so any supervisor that
// checks process identity — systemd's LISTEN_PID for socket activation and
// its default NotifyAccess=main for sd_notify, pg_ctl's postmaster.pid pid
// field, anything using getppid()/pidfd — finds the real daemon running
// exactly where it expects (issue #152). NOTIFY_SOCKET and LISTEN_FDS/
// LISTEN_PID all flow through untouched: the real daemon talks to systemd
// directly, with its own (correct, matching) pid attached to every
// datagram/syscall by the kernel. Signals the supervisor sends land on the
// real daemon directly too; no forwarding relay is needed (the earlier
// fork-a-child-then-forward design from issue #143 no longer applies once
// there's nothing left to forward *to*).
func runWithTracing(realBin string) error {
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("ready pipe: %w", err)
	}
	defer readyR.Close()

	// The helper must run from the *stable* funkoverage-shim binary, not
	// os.Executable() — by the time we're running, that resolves to
	// realBin's own installed path (e.g. /usr/bin/bzip2), and the helper
	// would keep that exact file mapped as its running text for as long as
	// it stays attached, making `funkoverage uninstall` fail with ETXTBSY
	// (issue #152 regression) even after the traced invocation has long
	// since exited. Fall back to os.Executable() for the rare case a
	// binary is being traced without having gone through `install` (e.g.
	// `funkoverage trace`), where no sidecar exists.
	shimExe := funkutil.ReadShimBinary(realBin)
	if shimExe == "" {
		shimExe, _ = os.Executable()
	}
	// Open a pidfd on ourselves before forking. The helper learns about the
	// traced program's death by polling it. Opening it here, while we are
	// still this PID, means it can never refer to a reused PID, and the fd
	// stays tied to the process across our exec() below.
	pidfd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: pidfd_open: %v; running untraced\n", err)
		return execReal(realBin)
	}
	pidfdFile := os.NewFile(uintptr(pidfd), "target-pidfd")

	// Start an intermediate rather than the helper directly, so the helper
	// is a grandchild: once the intermediate exits, the helper is
	// reparented to init/systemd and, unless the traced program is itself a
	// subreaper (e.g. PID 1 in a container), stops being its child. A direct
	// child would be reaped by any daemon that waitpid()s unknown children
	// (e.g. vsftpd), which then tries to account for a non-client PID
	// (issue #167).
	interCmd := exec.Command(shimExe)
	interCmd.Stdout, interCmd.Stderr = os.Stderr, os.Stderr
	interCmd.ExtraFiles = []*os.File{readyW, pidfdFile}
	interCmd.Env = append(os.Environ(),
		intermediateEnvVar+"=1",
		targetPidEnvVar+"="+strconv.Itoa(os.Getpid()),
		// Neither the intermediate nor the helper can derive realBin from
		// realBinaryPath(): that relies on os.Executable(), which resolves
		// to shimExe, not the target's installed path. Pass it through.
		realBinEnvVar+"="+realBin,
	)

	startErr := interCmd.Start()
	readyW.Close()
	pidfdFile.Close() // the intermediate/helper hold their own copies
	if startErr != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: start coverage helper: %v; running untraced\n", startErr)
		return execReal(realBin)
	}
	// Reap the intermediate before exec: it exits as soon as it has started
	// the helper, so it is never left as a zombie child of the traced
	// program.
	_ = interCmd.Wait()

	// Fail open, never closed. Both of these used to return an error, which
	// main() turns into os.Exit(1) — and that happens *before* the exec
	// below, so any environment that blocks eBPF setup replaced the user's
	// program with a stub that just fails. Losing a run's coverage is an
	// acceptable cost of instrumenting a system; breaking the instrumented
	// program is not (issue #158).
	//
	// Both failure shapes have been seen in the wild and neither is
	// distinguishable here, so both merely warn: a seccomp *allow*-list
	// (udev) lets the helper run and report its own error, while a
	// *deny*-list kills it with SIGSYS and leaves an empty reply.
	reply, err := io.ReadAll(readyR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: read helper status: %v; running untraced\n", err)
	} else if msg := string(reply); msg != "OK" {
		fmt.Fprintf(os.Stderr, "funkoverage-shim: tracing unavailable (%s); running untraced\n",
			strings.TrimPrefix(msg, "ERR:"))
	}

	return execReal(realBin)
}

// execReal replaces this process with the real binary, keeping the recursion
// guard and the caller's argv[0]. `funkoverage trace` invokes the shim via
// exec.Command(shimBinary, args...), which sets argv[0] to the shim's own
// path — FUNKOVERAGE_ARG0 carries the user's original path so the real
// binary sees the argv[0] it would have gotten unshimmed (some programs use
// it for usage or multicall dispatch). Regular installed invocations don't
// set it: os.Args[0] already is what the caller used.
func execReal(realBin string) error {
	activeEnvVar := activeEnvPrefix + envSafeName(filepath.Base(realBin))
	env := dropEnv(setEnv(os.Environ(), activeEnvVar, "1"), arg0EnvVar)
	argv := os.Args
	if arg0 := os.Getenv(arg0EnvVar); arg0 != "" {
		argv = append([]string{arg0}, os.Args[1:]...)
	}
	if err := syscall.Exec(realBin, argv, env); err != nil {
		return fmt.Errorf("exec %s: %w", realBin, err)
	}
	return nil
}

// intermediateMain starts the coverage helper from a short-lived
// intermediate and returns, so main exits at once. Its exit reparents the
// helper to init/systemd, outside the traced program's process tree
// (issue #167).
func intermediateMain() error {
	readyW := os.NewFile(3, "ready-pipe")
	pidfdFile := os.NewFile(4, "target-pidfd")
	shimExe, err := os.Executable()
	if err != nil {
		return reportHelperStartFailure(readyW, err)
	}
	return startHelper(shimExe, readyW, pidfdFile)
}

// startHelper forks the coverage helper and returns without waiting for it,
// so the caller's exit reparents the helper to init/systemd. The helper runs
// in its own session so a terminal signal to the traced program (Ctrl-C)
// does not kill it before it flushes its coverage log.
func startHelper(shimExe string, readyW, pidfdFile *os.File) error {
	cmd := exec.Command(shimExe)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	cmd.ExtraFiles = []*os.File{readyW, pidfdFile}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(dropEnv(os.Environ(), intermediateEnvVar), helperEnvVar+"=1")
	if err := cmd.Start(); err != nil {
		return reportHelperStartFailure(readyW, err)
	}
	// Do not Wait: returning (and exiting) is what reparents the helper away
	// from the traced program.
	return nil
}

func reportHelperStartFailure(readyW *os.File, err error) error {
	fmt.Fprintf(readyW, "ERR:%v", err)
	readyW.Close()
	return err
}

// helperMain runs as a detached background process: it attaches the eBPF
// tracer to the target pid (the original shim invocation, about to exec
// into the real binary in place), reports back over the ready pipe (fd 3),
// then keeps running — draining tracer events — until the target process
// exits.
func helperMain() {
	readyW := os.NewFile(3, "ready-pipe")
	pidfdFile := os.NewFile(4, "target-pidfd")
	if err := runHelper(readyW, pidfdFile); err != nil {
		fmt.Fprintf(readyW, "ERR:%v", err)
		readyW.Close()
		os.Exit(1)
	}
}

// runHelper does the actual attach/wait work. Structured to return errors
// (rather than exiting directly) so deferred cleanup — stopping the
// tracer — always runs, including on error paths partway through setup.
//
// pidfdFile is the target's pidfd, opened by the shim on itself before this
// process tree was forked and passed down. It refers to the process itself,
// survives the target's exec(), cannot be confused by PID reuse, and
// becomes readable as soon as the process dies.
func runHelper(readyW *os.File, pidfdFile *os.File) error {
	targetPIDValue, err := strconv.ParseUint(os.Getenv(targetPidEnvVar), 10, 32)
	if err != nil {
		return fmt.Errorf("bad %s: %w", targetPidEnvVar, err)
	}
	if targetPIDValue == 0 || targetPIDValue > uint64(maxLinuxPID) {
		return fmt.Errorf("bad %s: pid is outside the supported range", targetPidEnvVar)
	}
	if pidfdFile == nil {
		return errors.New("missing target pidfd")
	}
	// Keep pidfdFile reachable for the whole wait: os.File installs a
	// finalizer that would close the fd once only the raw int remains
	// reachable, letting Poll watch a descriptor the tracer could reuse.
	defer pidfdFile.Close()
	pidfd := int(pidfdFile.Fd())

	realBin := os.Getenv(realBinEnvVar)
	if realBin == "" {
		return fmt.Errorf("missing %s", realBinEnvVar)
	}
	funcs := funkutil.ReadFuncList(realBin)
	filter := funkutil.ReadFilterSidecar(realBin)

	dir := funkutil.LogDir()
	if err := funkutil.EnsureLogDir(dir); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	tracer, err := NewTracer(funcs, newLogPaths(dir, realBin), filter)
	if err != nil {
		return err
	}

	if err := tracer.Start(uint32(targetPIDValue), realBin); err != nil {
		tracer.Stop()
		return fmt.Errorf("tracer start: %w", err)
	}

	fmt.Fprint(readyW, "OK")
	readyW.Close()

	waitForTargetExit(pidfd)

	// Hold the drain lock across Stop()'s flush/close so a `funkoverage
	// report`/`uninstall` run immediately after the (now exited) traced
	// process — as e2e/CI scripts commonly do for short-lived CLI tools —
	// waits for the log to actually be complete instead of racing this
	// now-detached, otherwise invisible cleanup. Best-effort: proceed
	// regardless of whether the lock was acquired.
	release, lockErr := funkutil.HoldDrainLock()
	tracer.Stop()
	if lockErr == nil {
		release()
	}
	return nil
}

// waitForTargetExit blocks until the process behind pidfd exits. A pidfd
// becomes readable on process death and refers to the process itself, so
// this is race-free and immediate, with no polling interval and no PID-reuse
// hazard.
func waitForTargetExit(pidfd int) {
	pfd := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
	for {
		_, err := unix.Poll(pfd, -1)
		if err != nil && !errors.Is(err, unix.EINTR) {
			fmt.Fprintf(os.Stderr, "funkoverage-shim: poll target pidfd: %v; giving up\n", err)
			return
		}
		if rev := pfd[0].Revents; rev != 0 {
			// POLLIN means the target died. Anything else (POLLERR,
			// POLLNVAL, ...) is unexpected; log it rather than silently
			// stopping coverage, since a closed or reused fd shows up here.
			if rev&unix.POLLIN == 0 {
				fmt.Fprintf(os.Stderr, "funkoverage-shim: unexpected pidfd poll event %#x\n", rev)
			}
			return
		}
	}
}

// logPaths is one run's pair of log files: CALLED events go to Called, and
// functions discovered later via dlopen go to Functions.
type logPaths struct{ Called, Functions string }

// newLogPaths builds both names from a single timestamped stem, so the two
// logs of a run sort together and pair up by prefix.
func newLogPaths(dir, realBin string) logPaths {
	ts := time.Now()
	stem := filepath.Join(dir, fmt.Sprintf("%s_%s_%d",
		filepath.Base(realBin), ts.Format("20060102-150405"), ts.UnixNano()))
	return logPaths{Called: stem + "_called.log", Functions: stem + "_functions.log"}
}

// setEnv replaces key's value in env if present, or appends it otherwise.
func setEnv(env []string, key, val string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + val
			return env
		}
	}
	return append(env, prefix+val)
}

// dropEnv removes key from env, if present.
func dropEnv(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
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
