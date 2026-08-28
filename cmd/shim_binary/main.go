package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"funkoverage/internal/funkutil"
	"golang.org/x/sys/unix"
)

const (
	activeEnvPrefix = "FUNKOVERAGE_ACTIVE_"
	helperEnvVar    = "FUNKOVERAGE_HELPER"
	targetPidEnvVar = "FUNKOVERAGE_TARGET_PID"
	realBinEnvVar   = "FUNKOVERAGE_REAL_BIN"
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
	if os.Getenv(helperEnvVar) == "1" {
		helperMain()
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
	helperCmd := exec.Command(shimExe)
	helperCmd.Stdout, helperCmd.Stderr = os.Stderr, os.Stderr
	helperCmd.ExtraFiles = []*os.File{readyW}
	helperCmd.Env = append(os.Environ(),
		helperEnvVar+"=1",
		targetPidEnvVar+"="+strconv.Itoa(os.Getpid()),
		// The helper can't derive realBin from realBinaryPath() itself:
		// that relies on os.Executable(), which for the helper resolves to
		// shimExe (the stable path above), not the target's installed
		// path. Pass the already-resolved value through explicitly.
		realBinEnvVar+"="+realBin,
	)

	if err := helperCmd.Start(); err != nil {
		readyW.Close()
		return fmt.Errorf("start helper: %w", err)
	}
	readyW.Close()
	// We can never Wait() on the helper — we're about to exec away for
	// good — so don't have Go track it for reaping. Once we exec, the
	// helper is reparented to the nearest subreaper (init/systemd) on our
	// eventual exit, which reaps it.
	helperCmd.Process.Release()

	reply, err := io.ReadAll(readyR)
	if err != nil {
		return fmt.Errorf("read helper status: %w", err)
	}
	if msg := string(reply); msg != "OK" {
		return fmt.Errorf("helper: %s", strings.TrimPrefix(msg, "ERR:"))
	}

	activeEnvVar := activeEnvPrefix + envSafeName(filepath.Base(realBin))
	env := dropEnv(setEnv(os.Environ(), activeEnvVar, "1"), arg0EnvVar)

	// `funkoverage trace` invokes the shim via exec.Command(shimBinary,
	// args...), which sets argv[0] to the shim binary's own path — pass the
	// user's original invocation path through FUNKOVERAGE_ARG0 so the real
	// binary sees the argv[0] it would have gotten unshimmed (some programs
	// use it for usage messages or multicall dispatch). Regular installed
	// invocations don't set this: os.Args[0] here already IS what the
	// caller originally used, since this process itself was exec'd directly.
	argv := os.Args
	if arg0 := os.Getenv(arg0EnvVar); arg0 != "" {
		argv = append([]string{arg0}, os.Args[1:]...)
	}

	if err := syscall.Exec(realBin, argv, env); err != nil {
		return fmt.Errorf("exec %s: %w", realBin, err)
	}
	return nil
}

// helperMain runs as a detached background process: it attaches the eBPF
// tracer to the target pid (the original shim invocation, about to exec
// into the real binary in place), reports back over the ready pipe (fd 3),
// then keeps running — draining tracer events — until the target process
// exits.
func helperMain() {
	readyW := os.NewFile(3, "ready-pipe")
	if err := runHelper(readyW); err != nil {
		fmt.Fprintf(readyW, "ERR:%v", err)
		readyW.Close()
		os.Exit(1)
	}
}

// runHelper does the actual attach/wait work. Structured to return errors
// (rather than exiting directly) so deferred cleanup — stopping the
// tracer — always runs, including on error paths partway through setup.
func runHelper(readyW *os.File) error {
	targetPid, err := strconv.Atoi(os.Getenv(targetPidEnvVar))
	if err != nil {
		return fmt.Errorf("bad %s: %w", targetPidEnvVar, err)
	}

	// We can't waitpid() our own parent (only a parent can wait on a
	// child), and polling by pid risks a pid-reuse race on a busy system,
	// so ask the kernel to signal us when it dies — whether that's the
	// original shim process exiting/crashing before it execs, or (far more
	// commonly) the real daemon it became eventually shutting down.
	//
	// PR_SET_PDEATHSIG is scoped to the calling OS thread, not the process:
	// if the goroutine that registers it later migrates to a different
	// thread (routine under Go's scheduler — e.g. after any blocking call),
	// the registration silently stops applying and the signal never
	// arrives, leaking this process forever (observed as systemd "left-over
	// process ... in control group" warnings across service restarts).
	// LockOSThread pins us to this thread for the rest of the process's
	// life so that can't happen; never unlocked, matching the standard
	// idiom for a goroutine that owns a thread-scoped kernel registration
	// for its whole lifetime.
	runtime.LockOSThread()

	// Install the catching handler before anything else so there's no
	// window where an early parent death would hit SIGUSR1's default
	// (terminate) disposition and skip our cleanup below.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGUSR1)
	_ = unix.Prctl(unix.PR_SET_PDEATHSIG, uintptr(unix.SIGUSR1), 0, 0, 0)
	if os.Getppid() != targetPid {
		// Parent already gone in the window between fork and prctl above;
		// no signal will ever arrive for it. Nothing to trace.
		return nil
	}

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

	tracer, err := NewTracer(funcs, newLogPaths(dir, realBin), filter.Include, filter.Exclude)
	if err != nil {
		return err
	}

	if err := tracer.Start(uint32(targetPid)); err != nil {
		tracer.Stop()
		return fmt.Errorf("tracer start: %w", err)
	}

	fmt.Fprint(readyW, "OK")
	readyW.Close()

	waitForTargetExit(sigCh, targetPid, 5*time.Second)

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

// waitForTargetExit blocks until the target process exits. PDEATHSIG
// (delivered via sigCh) is the fast path, but per PR_SET_PDEATHSIG(2const)'s
// CAVEATS it's scoped to the specific OS thread that created this process,
// not "the parent process" in general — observed in practice as systemd
// logging "left-over process ... in control group" for Type=notify units
// using KillMode=process (e.g. sshd.service). Units using the default
// KillMode=control-group mask a missed signal by killing the whole cgroup
// (including us) on stop regardless; KillMode=process units have no such
// backup, so we poll the target's liveness as a fallback. A worst-case
// false negative here just stops tracing a few seconds later than ideal,
// versus leaking the helper (and its attached tracer) forever.
func waitForTargetExit(sigCh <-chan os.Signal, targetPid int, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			return
		case <-ticker.C:
			if unix.Kill(targetPid, 0) != nil {
				return
			}
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
