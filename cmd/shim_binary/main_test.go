package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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

func TestCleanEnv(t *testing.T) {
	t.Setenv(childEnvVar, "1")
	t.Setenv(waitFdEnvVar, "4")
	t.Setenv(arg0EnvVar, "/bin/true")
	t.Setenv("SOME_CUSTOM_VAR", "keep")

	cleaned := cleanEnv("SOME_CUSTOM_VAR")

	for _, kv := range cleaned {
		k, _, _ := strings.Cut(kv, "=")
		if k == childEnvVar || k == waitFdEnvVar || k == arg0EnvVar || k == "SOME_CUSTOM_VAR" {
			t.Errorf("cleanEnv failed to drop key: %s", k)
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

func TestBuildChildEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("SAFE_BIN_DIR", tmpDir)
	t.Setenv("LOG_DIR", tmpDir)
	t.Setenv(arg0EnvVar, "custom-arg0")

	env := buildChildEnv("/usr/bin/mybin", "")

	var foundChild, foundWait, foundArg0, foundActive, foundSafe, foundLog bool
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case childEnvVar:
			foundChild = true
			if v != "1" {
				t.Errorf("unexpected value for %s: %s", childEnvVar, v)
			}
		case waitFdEnvVar:
			foundWait = true
			if v != "3" {
				t.Errorf("unexpected value for %s: %s", waitFdEnvVar, v)
			}
		case arg0EnvVar:
			foundArg0 = true
			if v != "custom-arg0" {
				t.Errorf("unexpected value for %s: %s", arg0EnvVar, v)
			}
		case activeEnvPrefix + "MYBIN":
			foundActive = true
			if v != "1" {
				t.Errorf("unexpected value for active prefix: %s", v)
			}
		case "SAFE_BIN_DIR":
			foundSafe = true
		case "LOG_DIR":
			foundLog = true
		}
	}

	if !foundChild || !foundWait || !foundArg0 || !foundActive || !foundSafe || !foundLog {
		t.Errorf("buildChildEnv missing expected environment definitions")
	}
}

func TestBuildChildEnv_NotifyProxyOverride(t *testing.T) {
	t.Setenv("SAFE_BIN_DIR", t.TempDir())
	t.Setenv(notifySocketEnvVar, "/run/systemd/notify")

	env := buildChildEnv("/usr/bin/mybin", "/tmp/funkoverage-notify-123.sock")

	var got string
	var count int
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == notifySocketEnvVar {
			got = v
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one %s entry, got %d", notifySocketEnvVar, count)
	}
	if got != "/tmp/funkoverage-notify-123.sock" {
		t.Errorf("%s = %q, want proxy path", notifySocketEnvVar, got)
	}
}

// signalHelperTable maps a name usable on the command line/env to the
// syscall.Signal it identifies, for TestHelperSignalChild and
// TestWaitForwardingSignals_ForwardsSignalToChild.
var signalHelperTable = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"USR1": syscall.SIGUSR1,
}

// TestHelperSignalChild is not a real test: it's spawned as a subprocess by
// TestWaitForwardingSignals_ForwardsSignalToChild to act as a child that
// traps and reports whichever signal FUNKOVERAGE_TEST_SIGNAL names. A shell
// "trap ...; sleep N" fixture doesn't work here — bash defers running the
// trap's `exit` until its blocking `sleep` child returns, which defeats the
// point of testing prompt delivery.
func TestHelperSignalChild(t *testing.T) {
	name := os.Getenv("FUNKOVERAGE_TEST_SIGNAL")
	sig, ok := signalHelperTable[name]
	if !ok {
		t.Skip("helper process, not a real test")
	}
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, sig)
	<-sigCh
	fmt.Println("GOT_" + name)
	os.Exit(0)
}

// TestWaitForwardingSignals_ForwardsSignalToChild verifies the shim relays
// real, catchable signals to the child instead of the child either being
// left unsignalled (issue #143's socket-not-released bug) or SIGKILLed
// (losing any graceful-shutdown output, as reported for tcpdump). Covers
// both a signal that was already on the old hand-picked allowlist (TERM)
// and one that wasn't (USR1) — catch-all forwarding means both work the
// same way, with no per-signal list to fall out of date.
func TestWaitForwardingSignals_ForwardsSignalToChild(t *testing.T) {
	for name, sig := range signalHelperTable {
		t.Run(name, func(t *testing.T) {
			child := exec.Command(os.Args[0], "-test.run=^TestHelperSignalChild$")
			child.Env = append(os.Environ(), "FUNKOVERAGE_TEST_SIGNAL="+name)
			var out bytes.Buffer
			child.Stdout = &out
			if err := child.Start(); err != nil {
				t.Fatalf("start child: %v", err)
			}

			done := make(chan *os.ProcessState, 1)
			go func() { done <- waitForwardingSignals(child.Process) }()

			// Retry self-signalling rather than sleeping a fixed guess:
			// closes the (unavoidable) race between this goroutine starting
			// and waitForwardingSignals installing its signal.Notify.
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			deadline := time.After(5 * time.Second)
			var state *os.ProcessState
		loop:
			for {
				select {
				case state = <-done:
					break loop
				case <-ticker.C:
					_ = syscall.Kill(os.Getpid(), sig)
				case <-deadline:
					t.Fatal("waitForwardingSignals did not return after child exit")
				}
			}

			if state == nil || !state.Success() {
				t.Errorf("unexpected child exit state: %v", state)
			}
			if !strings.Contains(out.String(), "GOT_"+name) {
				t.Errorf("child never ran its own %s trap (would mean SIGKILL, not forwarding); output: %q", name, out.String())
			}
		})
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
