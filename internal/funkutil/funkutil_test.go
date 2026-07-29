package funkutil

import "testing"

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
