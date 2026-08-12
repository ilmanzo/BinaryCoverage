package funkutil

import "testing"

func TestNewFuncFilter(t *testing.T) {
	f, err := NewFuncFilter("", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Include != nil || f.Exclude != nil {
		t.Error("empty strings should produce nil regexps")
	}

	f, err = NewFuncFilter("^str_", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.Include == nil {
		t.Error("expected non-nil Include")
	}

	f, err = NewFuncFilter("", "^util_")
	if err != nil {
		t.Fatal(err)
	}
	if f.Exclude == nil {
		t.Error("expected non-nil Exclude")
	}

	_, err = NewFuncFilter("[invalid", "")
	if err == nil {
		t.Error("expected error for invalid include regex")
	}
	_, err = NewFuncFilter("", "[invalid")
	if err == nil {
		t.Error("expected error for invalid exclude regex")
	}
}

func TestFuncFilterMatch(t *testing.T) {
	var nilFilter *FuncFilter
	if !nilFilter.Match("anything") {
		t.Error("nil filter should match everything")
	}

	include, _ := NewFuncFilter("^str_", "")
	if !include.Match("str_length") {
		t.Error("should match str_length")
	}
	if include.Match("math_add") {
		t.Error("should not match math_add")
	}

	exclude, _ := NewFuncFilter("", "^util_")
	if !exclude.Match("str_length") {
		t.Error("should match str_length")
	}
	if exclude.Match("util_clamp") {
		t.Error("should not match util_clamp")
	}

	both, _ := NewFuncFilter("^math_", "is_")
	if !both.Match("math_add") {
		t.Error("should match math_add")
	}
	if both.Match("math_is_prime") {
		t.Error("should not match math_is_prime (excluded)")
	}
	if both.Match("str_length") {
		t.Error("should not match str_length (not included)")
	}

	empty, _ := NewFuncFilter("", "")
	if !empty.Match("anything") {
		t.Error("empty filter should match everything")
	}
}

func TestFuncFilterSidecar(t *testing.T) {
	var nilFilter *FuncFilter
	if s := nilFilter.Sidecar(); s.Include != "" || s.Exclude != "" {
		t.Errorf("nil filter should produce empty sidecar, got %+v", s)
	}

	empty, _ := NewFuncFilter("", "")
	if s := empty.Sidecar(); s.Include != "" || s.Exclude != "" {
		t.Errorf("empty filter should produce empty sidecar, got %+v", s)
	}

	includeOnly, _ := NewFuncFilter("^str_", "")
	if s := includeOnly.Sidecar(); s.Include != "^str_" || s.Exclude != "" {
		t.Errorf("unexpected sidecar for include-only: %+v", s)
	}

	both, _ := NewFuncFilter("^math_", "is_")
	if s := both.Sidecar(); s.Include != "^math_" || s.Exclude != "is_" {
		t.Errorf("unexpected sidecar for both: %+v", s)
	}
}

func TestFilterFromSidecar(t *testing.T) {
	f := FilterFromSidecar(FilterSidecar{})
	if f.Include != nil || f.Exclude != nil {
		t.Error("empty sidecar should produce nil regexps")
	}

	f = FilterFromSidecar(FilterSidecar{Include: "^str_", Exclude: "^util_"})
	if f.Include == nil || f.Exclude == nil {
		t.Fatal("expected non-nil Include and Exclude")
	}
	if !f.Match("str_length") || f.Match("util_clamp") || f.Match("math_add") {
		t.Error("FilterFromSidecar did not reproduce the original filter's matching")
	}

	// A malformed pattern must degrade to "no filter on that side", not
	// error out — the shim traces a live process and must not abort on a
	// corrupt or hand-edited sidecar file.
	f = FilterFromSidecar(FilterSidecar{Include: "[invalid"})
	if f.Include != nil {
		t.Error("invalid include pattern should degrade to nil, not panic or propagate")
	}
	if !f.Match("anything") {
		t.Error("filter with a degraded (nil) Include should match everything")
	}
}
