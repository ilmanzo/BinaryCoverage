package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"funkoverage/internal/funkutil"
	"golang.org/x/sys/unix"
)

func TestEnvSafeName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"my-binary", "MY_BINARY"},
		{"libcrypto.so.3", "LIBCRYPTO_SO_3"},
		{"UPPER_123", "UPPER_123"},
		{"a.b.c-d", "A_B_C_D"},
	}
	for _, tc := range cases {
		if got := envSafeName(tc.in); got != tc.want {
			t.Errorf("envSafeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewLogPaths(t *testing.T) {
	logs := newLogPaths("/tmp/logs", "/usr/bin/mybin")

	for _, tc := range []struct{ got, suffix string }{
		{logs.Called, "_called.log"},
		{logs.Functions, "_functions.log"},
	} {
		if !strings.HasPrefix(tc.got, "/tmp/logs/mybin_") {
			t.Errorf("%q should start with expected prefix", tc.got)
		}
		if !strings.HasSuffix(tc.got, tc.suffix) {
			t.Errorf("%q should end with %s", tc.got, tc.suffix)
		}
	}
	// The report side pairs a run's two logs by their shared stem, so they
	// must not be built from two separate timestamps.
	if strings.TrimSuffix(logs.Called, "_called.log") != strings.TrimSuffix(logs.Functions, "_functions.log") {
		t.Errorf("logs of one run must share a stem: %q vs %q", logs.Called, logs.Functions)
	}
}

func TestSetEnv(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux"}

	replaced := setEnv(env, "FOO", "new")
	if len(replaced) != 2 {
		t.Fatalf("setEnv replace: got %d entries, want 2: %v", len(replaced), replaced)
	}
	if replaced[0] != "FOO=new" {
		t.Errorf("setEnv replace: got %q, want FOO=new", replaced[0])
	}

	appended := setEnv(env, "NEW", "1")
	if len(appended) != 3 || appended[2] != "NEW=1" {
		t.Errorf("setEnv append: got %v, want [FOO=bar BAZ=qux NEW=1]", appended)
	}
}

func TestDropEnv(t *testing.T) {
	env := []string{"FOO=bar", "BAZ=qux", "FOO=stale"}

	got := dropEnv(env, "FOO")
	if len(got) != 1 || got[0] != "BAZ=qux" {
		t.Errorf("dropEnv: got %v, want [BAZ=qux]", got)
	}

	unchanged := dropEnv([]string{"BAZ=qux"}, "FOO")
	if len(unchanged) != 1 || unchanged[0] != "BAZ=qux" {
		t.Errorf("dropEnv with no match should be a no-op, got %v", unchanged)
	}
}

// TestWaitForTargetExit_Pidfd guards issue #167: the helper learns about the
// target's death through a pidfd, which survives the target's exec() and
// cannot fire for a live process. The call must block while the target runs
// and return once it exits.
func TestWaitForTargetExit_Pidfd(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stand-in target: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	pidfd, err := unix.PidfdOpen(cmd.Process.Pid, 0)
	if err != nil {
		t.Fatalf("pidfd_open: %v", err)
	}
	defer unix.Close(pidfd)

	done := make(chan struct{})
	go func() {
		waitForTargetExit(pidfd)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("waitForTargetExit returned while the target was still running")
	case <-time.After(200 * time.Millisecond):
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill target: %v", err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForTargetExit did not return after the target exited")
	}
}

// TestRunHelper_BadTargetPid covers runHelper's PID validation without
// touching eBPF: invalid values must fail before any tracer setup is attempted.
func TestRunHelper_BadTargetPid(t *testing.T) {
	for _, pid := range []string{"not-a-pid", "-1", "0", "2147483648", "4294967296"} {
		t.Run(pid, func(t *testing.T) {
			t.Setenv(targetPidEnvVar, pid)
			if err := runHelper(nil, nil); err == nil {
				t.Errorf("runHelper with invalid target pid %q should return an error", pid)
			}
		})
	}
}

// pidfdOnSelf returns an *os.File for a pidfd referring to the test process,
// standing in for the one the shim opens and passes down.
func pidfdOnSelf(t *testing.T) *os.File {
	t.Helper()
	fd, err := unix.PidfdOpen(os.Getpid(), 0)
	if err != nil {
		t.Fatalf("pidfd_open(self): %v", err)
	}
	f := os.NewFile(uintptr(fd), "test-pidfd")
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// TestRunHelper_MissingPidfd covers the required pidfd argument: without it
// there is no way to learn when the target exits.
func TestRunHelper_MissingPidfd(t *testing.T) {
	t.Setenv(targetPidEnvVar, strconv.Itoa(os.Getpid()))
	if err := runHelper(nil, nil); err == nil || !strings.Contains(err.Error(), "pidfd") {
		t.Errorf("runHelper without a pidfd should report it missing, got %v", err)
	}
}

// TestRunHelper_MissingRealBin covers the FUNKOVERAGE_REAL_BIN validation,
// which runs before any sidecar/tracer setup — reachable without eBPF.
func TestRunHelper_MissingRealBin(t *testing.T) {
	t.Setenv(targetPidEnvVar, strconv.Itoa(os.Getpid()))
	os.Unsetenv(realBinEnvVar)

	err := runHelper(nil, pidfdOnSelf(t))
	if err == nil || !strings.Contains(err.Error(), realBinEnvVar) {
		t.Errorf("runHelper with no %s should report it missing, got %v", realBinEnvVar, err)
	}
}

// TestFailedHelperDoesNotLeakToTracedProgram covers the fail-open path of
// issue #167 without eBPF: the helper fails before it reports ready (a
// LOG_DIR under a regular file makes EnsureLogDir fail), and the shim must
// not leave it as a child when it execs the real binary. The traced program
// is a fake daemon that reaps any child with waitpid(-1); it must not collect
// the helper. This does not cover the normal reparenting (a helper that is
// still running when the shim execs); the e2e PPid check does that.
func TestFailedHelperDoesNotLeakToTracedProgram(t *testing.T) {
	shim, safeDir, tmp := buildFakeDaemon(t)

	// A regular file where EnsureLogDir needs a directory: the helper reports
	// the failure and exits at once, reproducing an early helper exit.
	blocker := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write blocker: %v", err)
	}

	run := exec.Command(shim, "-reaper")
	run.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"SAFE_BIN_DIR=" + safeDir,
		"LOG_DIR=" + filepath.Join(blocker, "logs"),
	}
	out, err := run.CombinedOutput()
	if strings.Contains(string(out), "COLLECTED_CHILD") {
		t.Fatalf("fake daemon collected a child: the helper was in the traced program's process tree (issue #167)\n%s", out)
	}
	if err != nil {
		t.Fatalf("run shim: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NO_CHILD") {
		t.Fatalf("fake daemon did not report its result:\n%s", out)
	}
}

// buildFakeDaemon builds the shim and a fake "real binary" (the notifydaemon
// fixture, which reaps children with waitpid(-1) like vsftpd) side by side.
// The shim resolves the real binary to SAFE_BIN_DIR/basename(itself), so both
// are named fakedaemon.
func buildFakeDaemon(t *testing.T) (shim, safeDir, tmp string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH, cannot build the shim")
	}
	tmp = t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	safeDir = filepath.Join(tmp, "safe")
	for _, d := range []string{binDir, safeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	shim = filepath.Join(binDir, "fakedaemon")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	build = exec.Command("go", "build", "-o", filepath.Join(safeDir, "fakedaemon"), "funkoverage/tests/sample/notifydaemon")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reaper: %v\n%s", err, out)
	}
	return shim, safeDir, tmp
}

// denyBPFEnv makes this test binary act as a launcher (see TestMain) instead
// of running tests.
const denyBPFEnv = "FUNKOVERAGE_TEST_DENY_BPF"

func TestMain(m *testing.M) {
	if os.Getenv(denyBPFEnv) != "" {
		denyBPFAndExec(os.Args[1:])
	}
	os.Exit(m.Run())
}

// denyBPFAndExec does what a systemd unit's SystemCallFilter= without bpf
// does before it execs the service: installs a seccomp filter that kills the
// whole process on bpf(2) (SCMP_ACT_KILL_PROCESS), then execs argv in place.
// It only returns by exiting.
func denyBPFAndExec(argv []string) {
	die := func(err error) {
		fmt.Fprintf(os.Stderr, "deny-bpf launcher: %v\n", err)
		os.Exit(127)
	}
	// Filters are per-thread and survive only on the thread that execs.
	runtime.LockOSThread()
	filter := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS},                          // A = seccomp_data.nr
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jf: 1, K: unix.SYS_BPF}, // bpf: fall into the kill
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW},
	}
	prog := unix.SockFprog{Len: uint16(len(filter)), Filter: &filter[0]}
	// No core file per killed process (systemd-coredump still logs it).
	if err := unix.Setrlimit(unix.RLIMIT_CORE, &unix.Rlimit{}); err != nil {
		die(fmt.Errorf("rlimit core: %w", err))
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		die(fmt.Errorf("no_new_privs: %w", err))
	}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&prog)), 0, 0); err != nil {
		die(fmt.Errorf("seccomp: %w", err))
	}
	runtime.KeepAlive(&prog)
	die(syscall.Exec(argv[0], argv, os.Environ()))
}

// #173: under a seccomp kill on bpf(2) only the helper may die; the shim must
// still exec the real binary. Catches any bpf(2) issued before main().
func TestShimFailsOpenWhenBPFIsDenied(t *testing.T) {
	shim, safeDir, tmp := buildFakeDaemon(t)

	run := exec.Command(os.Args[0], shim, "-reaper")
	run.Env = []string{
		denyBPFEnv + "=1",
		"PATH=" + os.Getenv("PATH"),
		"SAFE_BIN_DIR=" + safeDir,
		"LOG_DIR=" + filepath.Join(tmp, "logs"),
	}
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("shim did not exec the real binary under a bpf(2) seccomp kill: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "NO_CHILD") {
		t.Fatalf("real binary did not run:\n%s", out)
	}
	if !strings.Contains(string(out), "helper died before attaching") {
		t.Errorf("killed helper not reported:\n%s", out)
	}
}

// TestStartHelper_StartFailure covers the intermediate's failure path: if
// the shim binary cannot be executed, it must report the error on the ready
// pipe and return it (the shim then runs untraced).
func TestStartHelper_StartFailure(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer r.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()

	if err := startHelper(filepath.Join(t.TempDir(), "no-such-shim"), w, devnull); err == nil {
		t.Fatal("startHelper should fail for a non-existent shim binary")
	}

	buf := make([]byte, 256)
	n, _ := r.Read(buf)
	if !strings.Contains(string(buf[:n]), "ERR:") {
		t.Errorf("failure was not reported on the ready pipe, got %q", buf[:n])
	}
}

func TestRealBinaryPath(t *testing.T) {
	t.Setenv("FUNKOVERAGE_BINARY_NAME", "my_custom_name")
	expected := filepath.Join(funkutil.SafeBinDir(), "my_custom_name")
	if got := realBinaryPath(); got != expected {
		t.Errorf("realBinaryPath() with env: got %q, want %q", got, expected)
	}

	os.Unsetenv("FUNKOVERAGE_BINARY_NAME")
	got := realBinaryPath()
	if !strings.HasSuffix(got, filepath.Base(got)) {
		t.Errorf("realBinaryPath() defaulted oddly: %q", got)
	}
}
