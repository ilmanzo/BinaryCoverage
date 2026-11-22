package main

import (
	"regexp"
	"slices"
	"strings"
)

// globalGroup collects symbols with no recognizable package or namespace
// (plain C functions, unparsable names).
const globalGroup = "(global)"

// FunctionGroup is one package/namespace worth of functions in the detailed
// HTML report. Functions are listed called first, then uncalled.
type FunctionGroup struct {
	Name      string
	Called    int
	Total     int
	Functions []FunctionEntry
}

// buildGroups partitions called and uncalled names into groups sorted by name.
func buildGroups(called, uncalled []string) []FunctionGroup {
	byName := map[string]*FunctionGroup{}
	add := func(fn, status string) {
		group, short := splitSymbol(fn)
		g := byName[group]
		if g == nil {
			g = &FunctionGroup{Name: group}
			byName[group] = g
		}
		g.Total++
		if status == "called" {
			g.Called++
		}
		g.Functions = append(g.Functions, FunctionEntry{Name: fn, Short: short, Status: status})
	}
	for _, fn := range called {
		add(fn, "called")
	}
	for _, fn := range uncalled {
		add(fn, "uncalled")
	}
	groups := make([]FunctionGroup, 0, len(byName))
	for _, g := range byName {
		groups = append(groups, *g)
	}
	slices.SortFunc(groups, func(a, b FunctionGroup) int { return strings.Compare(a.Name, b.Name) })
	return groups
}

// splitSymbol splits a demangled symbol into its scope (Go package path or
// C++ namespace/class) and the remaining short name. Symbols with no
// recognizable scope land in globalGroup with the full name kept as is.
func splitSymbol(name string) (group, short string) {
	if start, end := cppScope(name); end > start {
		// A leading return type ("void std::vector<int>::f<T>()") stays with the short name.
		return name[start:end], name[:start] + name[end+2:]
	}
	if i := goPackageEnd(name); i > 0 {
		return name[:i], name[i+1:]
	}
	return globalGroup, name
}

const anonNamespace = "(anonymous namespace)"

// operatorTokens lists C++ operator names, longest first so "<=" wins over "<".
var operatorTokens = []string{
	"<=>", "<<=", ">>=", "->*", "()", "[]", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
	"++", "--", "+=", "-=", "*=", "/=", "%=", "^=", "&=", "|=", "->",
	"<", ">", "=", "!", "+", "-", "*", "/", "%", "^", "&", "|", "~", ",",
}

// cppScope locates the scope of a demangled C++ name: name[start:end] is the
// qualifier before the last top-level "::" of the function name, and end is
// -1 when there is none. start skips a leading return type, which template
// instantiations carry. Template arguments and operator tokens are skipped so
// their "<", ">" and "()" do not end the scope early.
//
// shortcut: conversion operators ("operator std::string()") put a scope
// separator after "operator" and get split there; revisit if they matter.
func cppScope(name string) (start, end int) {
	end, depth := -1, 0
	for i := 0; i < len(name); i++ {
		switch {
		case strings.HasPrefix(name[i:], anonNamespace):
			i += len(anonNamespace) - 1
		case strings.HasPrefix(name[i:], "operator") && (i == 0 || name[i-1] == ':' || name[i-1] == ' '):
			i += len("operator")
			if strings.HasPrefix(name[i:], " ") { // "operator new", "operator< <T>"
				i++
			}
			for _, tok := range operatorTokens {
				if strings.HasPrefix(name[i:], tok) {
					i += len(tok)
					if strings.HasPrefix(name[i:], " <") { // "operator<< <char>"
						i++
					}
					break
				}
			}
			i--
		case name[i] == '<':
			depth++
		case name[i] == '>' && depth > 0:
			depth--
		case name[i] == '(' && depth == 0:
			return start, end
		case depth == 0 && name[i] == ' ':
			start, end = i+1, -1
		case depth == 0 && strings.HasPrefix(name[i:], "::"):
			end = i
			i++
		}
	}
	return start, end
}

// compilerClone matches the suffixes GCC appends to C function clones
// ("foo.cold", "foo.part.0"), which must not be read as Go "pkg.Func".
var compilerClone = regexp.MustCompile(`^(cold|part|constprop|isra|lto_priv|localalias|clone)(\.\d+)*$`)

// goPackageEnd returns the index of the dot that ends the package path in a
// Go symbol ("github.com/x/y.(*T).M" -> index before "(*T)"), or -1. The linker
// escapes dots in the last path element as %2e, so the first dot after the
// last slash is the package boundary.
func goPackageEnd(name string) int {
	// Slashes inside generic arguments or receivers belong to the name, not the path.
	limit := strings.IndexAny(name, "([")
	if limit < 0 {
		limit = len(name)
	}
	slash := strings.LastIndexByte(name[:limit], '/')
	dot := strings.IndexByte(name[slash+1:], '.')
	if dot < 0 {
		return -1
	}
	dot += slash + 1
	if dot == len(name)-1 || name[dot+1] == '.' {
		return -1
	}
	if slash < 0 && compilerClone.MatchString(name[dot+1:]) {
		return -1
	}
	return dot
}
