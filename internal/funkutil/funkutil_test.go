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
