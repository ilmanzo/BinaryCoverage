package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitSymbol(t *testing.T) {
	tests := []struct{ name, group, short string }{
		// Go
		{"github.com/grafana/grafana/apps/v0alpha1.(*Receiver).Copy", "github.com/grafana/grafana/apps/v0alpha1", "(*Receiver).Copy"},
		{"github.com/x/y.Func", "github.com/x/y", "Func"},
		{"github.com/x/y.init.func1", "github.com/x/y", "init.func1"},
		{"github.com/x/y.Map[go.shape.*github.com/a/b.T]", "github.com/x/y", "Map[go.shape.*github.com/a/b.T]"},
		{"gopkg.in/yaml%2ev3.Marshal", "gopkg.in/yaml%2ev3", "Marshal"},
		{"runtime.main", "runtime", "main"},
		{"type..eq.[2]string", globalGroup, "type..eq.[2]string"},
		// C++
		{"ns::Class::method(int)", "ns::Class", "method(int)"},
		{"std::vector<int, std::allocator<int> >::push_back(int const&)", "std::vector<int, std::allocator<int> >", "push_back(int const&)"},
		{"Foo::operator()(int)", "Foo", "operator()(int)"},
		{"Foo::operator<<(Foo const&)", "Foo", "operator<<(Foo const&)"},
		{"Foo::operator<(Foo const&)", "Foo", "operator<(Foo const&)"},
		{"(anonymous namespace)::helper(int)", "(anonymous namespace)", "helper(int)"},
		{"ns::(anonymous namespace)::helper(int)", "ns::(anonymous namespace)", "helper(int)"},
		{"ns::free_fn(std::map<int, ns::X>)", "ns", "free_fn(std::map<int, ns::X>)"},
		// C and the rest
		{"main", globalGroup, "main"},
		{"foo.cold", globalGroup, "foo.cold"},
		{"foo.part.0", globalGroup, "foo.part.0"},
		{"", globalGroup, ""},
	}
	for _, tc := range tests {
		group, short := splitSymbol(tc.name)
		if group != tc.group || short != tc.short {
			t.Errorf("splitSymbol(%q) = (%q, %q), want (%q, %q)", tc.name, group, short, tc.group, tc.short)
		}
	}
}

func TestBuildGroups(t *testing.T) {
	groups := buildGroups(
		[]string{"b/pkg.A", "main"},
		[]string{"b/pkg.B", "a::C::f()"},
	)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3: %+v", len(groups), groups)
	}
	if groups[0].Name != globalGroup || groups[1].Name != "a::C" || groups[2].Name != "b/pkg" {
		t.Errorf("groups not sorted by name: %+v", groups)
	}
	pkg := groups[2]
	if pkg.Called != 1 || pkg.Total != 2 {
		t.Errorf("b/pkg counts = %d/%d, want 1/2", pkg.Called, pkg.Total)
	}
	if pkg.Functions[0].Status != "called" || pkg.Functions[0].Short != "A" || pkg.Functions[1].Name != "b/pkg.B" {
		t.Errorf("b/pkg functions = %+v", pkg.Functions)
	}
}

func TestGenerateHTMLReportGroups(t *testing.T) {
	tmp := t.TempDir()
	rep := imageReport{
		Image:    "/usr/bin/app",
		Called:   []string{"github.com/x/y.(*T).Run"},
		Uncalled: []string{"github.com/x/y.Stop"},
	}
	if err := generateHTMLReport(rep, tmp); err != nil {
		t.Fatalf("generateHTMLReport: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(tmp, "app.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	for _, want := range []string{
		"github.com/x/y",
		`data-name="github.com/x/y.(*T).Run"`,
		">(*T).Run</li>",
		">Stop</li>",
		"1/2",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
}
