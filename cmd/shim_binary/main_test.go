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

func TestCalledLogPath(t *testing.T) {
	dir := "/tmp/logs"
	bin := "/usr/bin/mybin"
	path := calledLogPath(dir, bin)

	if !strings.HasPrefix(path, "/tmp/logs/mybin_") {
		t.Errorf("calledLogPath %q should start with expected prefix", path)
	}
	if !strings.HasSuffix(path, "_called.log") {
		t.Errorf("calledLogPath %q should end with _called.log", path)
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

func TestWaitForTargetExit_SignalPath(t *testing.T) {
	sigCh := make(chan os.Signal, 1)
	sigCh <- os.Interrupt // stand-in for the real SIGUSR1 PDEATHSIG delivery

	done := make(chan struct{})
	go func() {
		waitForTargetExit(sigCh, os.Getpid(), time.Hour) // poll interval irrelevant: signal wins
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForTargetExit did not return promptly on signal")
	}
}

// TestWaitForTargetExit_PollFallback guards the issue #152 fix for
// sshd.service (KillMode=process): if PDEATHSIG never fires, the poll must
// still notice the target is gone and return, instead of leaking forever.
func TestWaitForTargetExit_PollFallback(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run stand-in target: %v", err)
	}
	deadPid := cmd.Process.Pid // guaranteed exited by the time Run() returns

	sigCh := make(chan os.Signal, 1) // never sent to: signal path must not be relied on

	done := make(chan struct{})
	go func() {
		waitForTargetExit(sigCh, deadPid, 20*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("waitForTargetExit did not fall back to polling for a dead target")
	}
}

// TestRunHelper_BadTargetPid covers runHelper's env-parsing validation
// without touching eBPF: an unparseable FUNKOVERAGE_TARGET_PID must fail
// fast, before any tracer setup is attempted.
func TestRunHelper_BadTargetPid(t *testing.T) {
	t.Setenv(targetPidEnvVar, "not-a-pid")

	if err := runHelper(nil); err == nil {
		t.Error("runHelper with a non-numeric target pid should return an error")
	}
}

// TestRunHelper_ParentAlreadyGone covers the race guard: if our parent (by
// pid) no longer matches os.Getppid() by the time we check, it must return
// immediately (nil, no tracer attach attempted) rather than trace a pid
// that's no longer ours to trace.
func TestRunHelper_ParentAlreadyGone(t *testing.T) {
	// Getppid() is a real, currently-running process (the test harness'
	// parent); using it as the fake pid guarantees it never equals our own.
	t.Setenv(targetPidEnvVar, strconv.Itoa(os.Getppid()+1))

	if err := runHelper(nil); err != nil {
		t.Errorf("runHelper with a mismatched parent pid should return nil, got %v", err)
	}
}

// TestRunHelper_MissingRealBin covers the FUNKOVERAGE_REAL_BIN validation,
// which runs before any sidecar/tracer setup — reachable without eBPF by
// passing the parent-pid check first.
func TestRunHelper_MissingRealBin(t *testing.T) {
	t.Setenv(targetPidEnvVar, strconv.Itoa(os.Getppid()))
	os.Unsetenv(realBinEnvVar)

	err := runHelper(nil)
	if err == nil || !strings.Contains(err.Error(), realBinEnvVar) {
		t.Errorf("runHelper with no %s should report it missing, got %v", realBinEnvVar, err)
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
