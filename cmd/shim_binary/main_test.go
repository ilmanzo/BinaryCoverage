package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH, cannot build the shim")
	}
	tmp := t.TempDir()
	binDir := filepath.Join(tmp, "bin")
	safeDir := filepath.Join(tmp, "safe")
	for _, d := range []string{binDir, safeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// The shim resolves the real binary to SAFE_BIN_DIR/basename(itself), so
	// both are named fakedaemon.
	shim := filepath.Join(binDir, "fakedaemon")
	build := exec.Command("go", "build", "-o", shim, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build shim: %v\n%s", err, out)
	}
	// The fake real binary reaps any child with waitpid(-1), like vsftpd.
	reaper := filepath.Join(safeDir, "fakedaemon")
	build = exec.Command("go", "build", "-o", reaper, "funkoverage/tests/sample/notifydaemon")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build reaper: %v\n%s", err, out)
	}

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
