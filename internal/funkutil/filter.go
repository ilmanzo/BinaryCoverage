package funkutil

import (
	"fmt"
	"regexp"
)

// FuncFilter gates which functions pass enumeration based on regex patterns
// applied to the demangled name. Shared between install-time enumeration
// (cmd) and the shim's runtime dlopen JIT filtering (cmd/shim_binary),
// which must agree on what a function name matches.
type FuncFilter struct {
	Include *regexp.Regexp
	Exclude *regexp.Regexp
}

func NewFuncFilter(include, exclude string) (*FuncFilter, error) {
	f := &FuncFilter{}
	if include != "" {
		re, err := regexp.Compile(include)
		if err != nil {
			return nil, fmt.Errorf("bad --include regex: %w", err)
		}
		f.Include = re
	}
	if exclude != "" {
		re, err := regexp.Compile(exclude)
		if err != nil {
			return nil, fmt.Errorf("bad --exclude regex: %w", err)
		}
		f.Exclude = re
	}
	return f, nil
}

func (f *FuncFilter) Match(demangled string) bool {
	if f == nil {
		return true
	}
	if f.Include != nil && !f.Include.MatchString(demangled) {
		return false
	}
	if f.Exclude != nil && f.Exclude.MatchString(demangled) {
		return false
	}
	return true
}

// Sidecar converts f to its serializable form (regex source patterns) so the
// shim can re-apply the same filter to functions discovered via dlopen at
// runtime. A nil filter yields the zero value (no filtering).
func (f *FuncFilter) Sidecar() FilterSidecar {
	var s FilterSidecar
	if f == nil {
		return s
	}
	if f.Include != nil {
		s.Include = f.Include.String()
	}
	if f.Exclude != nil {
		s.Exclude = f.Exclude.String()
	}
	return s
}

// FilterFromSidecar rebuilds a *FuncFilter from its serialized form, so the
// shim can re-apply the same filtering to functions discovered later via
// dlopen JIT instrumentation that enumeration already applied to statically
// discovered functions. A bad pattern silently degrades to "no filter on
// that side" rather than erroring — the shim is tracing a live process and
// must not abort a run over a malformed sidecar.
func FilterFromSidecar(s FilterSidecar) *FuncFilter {
	f := &FuncFilter{}
	if s.Include != "" {
		if re, err := regexp.Compile(s.Include); err == nil {
			f.Include = re
		}
	}
	if s.Exclude != "" {
		if re, err := regexp.Compile(s.Exclude); err == nil {
			f.Exclude = re
		}
	}
	return f
}
