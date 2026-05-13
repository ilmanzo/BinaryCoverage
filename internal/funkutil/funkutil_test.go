package funkutil

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestEnvOr(t *testing.T) {
	t.Setenv("FUNKUTIL_TEST_X", "value")
	if got := EnvOr("FUNKUTIL_TEST_X", "fallback"); got != "value" {
		t.Errorf("EnvOr set: got %q, want %q", got, "value")
	}
	if got := EnvOr("FUNKUTIL_TEST_UNSET", "fallback"); got != "fallback" {
		t.Errorf("EnvOr unset: got %q, want %q", got, "fallback")
	}
	t.Setenv("FUNKUTIL_TEST_EMPTY", "")
	if got := EnvOr("FUNKUTIL_TEST_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("EnvOr empty: got %q, want %q", got, "fallback")
	}
}

func TestStripVersion(t *testing.T) {
	cases := map[string]string{
		"memcpy":            "memcpy",
		"memcpy@GLIBC_2.14": "memcpy",
		"foo@@bar":          "foo",
		"":                  "",
		"@only_version":     "",
	}
	for in, want := range cases {
		if got := StripVersion(in); got != want {
			t.Errorf("StripVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLibsSidecarRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	libs := []string{"/lib64/libc.so.6", "/lib64/libm.so.6"}

	if err := WriteLibsSidecar(safe, libs); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(LibsSidecarPath(safe)); err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}

	got := ReadLibsSidecar(safe)
	if !reflect.DeepEqual(got, libs) {
		t.Errorf("roundtrip: got %v, want %v", got, libs)
	}
}

func TestLibsSidecarEmptyDeletes(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	_ = WriteLibsSidecar(safe, []string{"/lib/x.so"})
	if err := WriteLibsSidecar(safe, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(LibsSidecarPath(safe)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed, stat err: %v", err)
	}
	if got := ReadLibsSidecar(safe); got != nil {
		t.Errorf("missing sidecar should yield nil, got %v", got)
	}
}

func TestReadLibsSidecarMalformed(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	if err := os.WriteFile(LibsSidecarPath(safe), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadLibsSidecar(safe); got != nil {
		t.Errorf("malformed sidecar should yield nil, got %v", got)
	}
}
