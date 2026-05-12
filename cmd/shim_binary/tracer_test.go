package main

import (
	"reflect"
	"testing"
)

func TestFlattenFuncs_StableOrderAndCookies(t *testing.T) {
	funcs := map[string][]string{
		"/lib/b.so": {"z"},
		"/lib/a.so": {"x", "y"},
	}
	refs, syms, cookies := flattenFuncs(funcs)

	wantRefs := []FuncRef{
		{Image: "/lib/a.so", Name: "x"},
		{Image: "/lib/a.so", Name: "y"},
		{Image: "/lib/b.so", Name: "z"},
	}
	if !reflect.DeepEqual(refs, wantRefs) {
		t.Errorf("refs: got %v, want %v", refs, wantRefs)
	}

	if !reflect.DeepEqual(syms["/lib/a.so"], []string{"x", "y"}) {
		t.Errorf("syms[/lib/a.so] = %v, want [x y]", syms["/lib/a.so"])
	}
	if !reflect.DeepEqual(syms["/lib/b.so"], []string{"z"}) {
		t.Errorf("syms[/lib/b.so] = %v, want [z]", syms["/lib/b.so"])
	}

	if !reflect.DeepEqual(cookies["/lib/a.so"], []uint64{0, 1}) {
		t.Errorf("cookies[/lib/a.so] = %v, want [0 1]", cookies["/lib/a.so"])
	}
	if !reflect.DeepEqual(cookies["/lib/b.so"], []uint64{2}) {
		t.Errorf("cookies[/lib/b.so] = %v, want [2]", cookies["/lib/b.so"])
	}
}

func TestFlattenFuncs_DeterministicAcrossRuns(t *testing.T) {
	funcs := map[string][]string{
		"/lib/c.so": {"one"},
		"/lib/a.so": {"two", "three"},
		"/lib/b.so": {"four"},
	}
	refs1, _, _ := flattenFuncs(funcs)
	refs2, _, _ := flattenFuncs(funcs)
	if !reflect.DeepEqual(refs1, refs2) {
		t.Errorf("flattenFuncs is not deterministic: %v vs %v", refs1, refs2)
	}
}

func TestNewTracer_RejectsEmptyFuncs(t *testing.T) {
	_, err := NewTracer(map[string][]string{}, "/tmp/should-not-be-created")
	if err == nil {
		t.Fatal("NewTracer with empty funcs: want error, got nil")
	}
}

func TestNewTracer_RejectsNilFuncs(t *testing.T) {
	_, err := NewTracer(nil, "/tmp/should-not-be-created")
	if err == nil {
		t.Fatal("NewTracer with nil funcs: want error, got nil")
	}
}
