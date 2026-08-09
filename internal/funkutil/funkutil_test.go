package funkutil

import (
	"os"
	"path/filepath"
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

func TestFuncIsRelevant(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"foo", true},
		{"bar", true},
		{"myFunc", true},
		{"str_length", true},
		{"math_add", true},
		{"plugin_func", true},
		{"printf@GLIBC_2.2.5", true},
		{"main", false},
		{"_init", false},
		{"_start", false},
		{".plt", false},
		{".plt.got", false},
		{"foo@plt", false},
		{"bar@plt.got", false},
		{"__cxa_atexit", false},
		{"__libc_start_main", false},
		{"_dl_relocate_static_pie", false},
	}
	for _, c := range cases {
		if got := FuncIsRelevant(c.name); got != c.want {
			t.Errorf("FuncIsRelevant(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestIsSystemLib(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/lib64/libc.so.6", true},
		{"/lib64/libm.so.6", true},
		{"/usr/lib/x86_64-linux-gnu/libpthread.so.0", true},
		{"/lib64/ld-linux-x86-64.so.2", true},
		{"/lib64/libstdc++.so.6", true},
		{"/usr/lib/x86_64-linux-gnu/libstdc++.so.6", true},
		{"/lib64/libgcc_s.so.1", true},
		{"/lib64/libdl.so.2", true},
		{"/lib64/librt.so.1", true},
		{"/usr/lib64/libssl.so.3", false},
		{"/usr/lib64/libcurl.so.4", false},
		{"/opt/foo/libmycrypto.so.1", false},
		{"/lib64/libz.so.1", false},
		{"/opt/myapp/libplugin.so", false},
		{"./libplugin.so", false},
		{"/usr/lib64/libcustomthing.so.1", false},
		// Direct ldd dependencies of these are ordinary, meaningful trace
		// targets at install time — only IsNoisyDlopenLib skips them.
		{"/usr/lib64/libselinux.so.1", false},
		{"/usr/lib64/libsystemd.so.0", false},
		{"/usr/lib64/libpam.so.0", false},
	}
	for _, c := range cases {
		if got := IsSystemLib(c.path); got != c.want {
			t.Errorf("IsSystemLib(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestIsNoisyDlopenLib(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		// Everything IsSystemLib already covers must still be skipped.
		{"/lib64/libc.so.6", true},
		{"/lib64/libstdc++.so.6", true},
		// Plus the bulky/common dlopen targets install-time no longer skips.
		{"/usr/lib64/libselinux.so.1", true},
		{"/usr/lib64/libsystemd.so.0", true},
		{"/usr/lib64/libpam.so.0", true},
		{"/usr/lib64/libaudit.so.1", true},
		{"/usr/lib64/libdbus-1.so.3", true},
		{"/usr/lib64/libudev.so.1", true},
		{"/usr/lib64/libmount.so.1", true},
		{"/usr/lib64/libblkid.so.1", true},
		{"/usr/lib64/libuuid.so.1", true},
		{"/usr/lib64/libglib-2.0.so.0", true},
		// Ordinary application libraries are still traced.
		{"/usr/lib64/libssl.so.3", false},
		{"/usr/lib64/libcurl.so.4", false},
		{"/opt/myapp/libplugin.so", false},
	}
	for _, c := range cases {
		if got := IsNoisyDlopenLib(c.path); got != c.want {
			t.Errorf("IsNoisyDlopenLib(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	origLogDir := os.Getenv("LOG_DIR")
	origSafeBinDir := os.Getenv("SAFE_BIN_DIR")
	defer func() {
		t.Setenv("LOG_DIR", origLogDir)
		t.Setenv("SAFE_BIN_DIR", origSafeBinDir)
	}()

	t.Setenv("LOG_DIR", "/custom/log/dir")
	if got := LogDir(); got != "/custom/log/dir" {
		t.Errorf("LogDir() with env: got %q, want %q", got, "/custom/log/dir")
	}

	t.Setenv("SAFE_BIN_DIR", "/custom/safe/bin")
	if got := SafeBinDir(); got != "/custom/safe/bin" {
		t.Errorf("SafeBinDir() with env: got %q, want %q", got, "/custom/safe/bin")
	}

	os.Unsetenv("LOG_DIR")
	if got := LogDir(); got != DefaultLogDir {
		t.Errorf("LogDir() unset: got %q, want %q", got, DefaultLogDir)
	}

	os.Unsetenv("SAFE_BIN_DIR")
	if got := SafeBinDir(); got != DefaultSafeBinDir {
		t.Errorf("SafeBinDir() unset: got %q, want %q", got, DefaultSafeBinDir)
	}
}

func TestFilterSidecarRoundtrip(t *testing.T) {
	tmpDir := t.TempDir()
	safePath := filepath.Join(tmpDir, "testbin")

	// Missing file should return zero-value filter sidecar without error
	got := ReadFilterSidecar(safePath)
	if got.Include != "" || got.Exclude != "" {
		t.Errorf("ReadFilterSidecar non-existent: got %+v, want empty", got)
	}

	// Write and read roundtrip
	filter := FilterSidecar{
		Include: "^plugin_",
		Exclude: "^internal_",
	}
	if err := WriteFilterSidecar(safePath, filter); err != nil {
		t.Fatalf("WriteFilterSidecar: %v", err)
	}

	got = ReadFilterSidecar(safePath)
	if got.Include != "^plugin_" || got.Exclude != "^internal_" {
		t.Errorf("ReadFilterSidecar roundtrip: got %+v, want %+v", got, filter)
	}

	// Empty patterns should delete the sidecar file
	emptyFilter := FilterSidecar{Include: "", Exclude: ""}
	if err := WriteFilterSidecar(safePath, emptyFilter); err != nil {
		t.Fatalf("WriteFilterSidecar empty: %v", err)
	}

	if _, err := os.Stat(FilterSidecarPath(safePath)); !os.IsNotExist(err) {
		t.Errorf("expected filter sidecar file to be deleted")
	}
}
