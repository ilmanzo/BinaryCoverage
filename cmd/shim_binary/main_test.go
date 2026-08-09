package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	env := buildChildEnv("/usr/bin/mybin")

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
