// Package funkutil holds helpers shared between the funkoverage CLI and
// the funkoverage-shim binary. Both are package main, so anything they
// need to share has to live here.
package funkutil

import (
	"encoding/json"
	"os"
	"strings"
)

// Filesystem defaults. Override with the LOG_DIR / SAFE_BIN_DIR env vars.
const (
	DefaultLogDir     = "/var/coverage/data"
	DefaultSafeBinDir = "/var/coverage/bin"
)

// EnvOr returns the value of the named env var, or fallback if unset/empty.
func EnvOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// LogDir returns LOG_DIR or DefaultLogDir.
func LogDir() string { return EnvOr("LOG_DIR", DefaultLogDir) }

// SafeBinDir returns SAFE_BIN_DIR or DefaultSafeBinDir.
func SafeBinDir() string { return EnvOr("SAFE_BIN_DIR", DefaultSafeBinDir) }

// StripVersion removes a trailing "@VERSION" suffix from a symbol name.
// e.g. "memcpy@GLIBC_2.14" → "memcpy".
func StripVersion(name string) string {
	if before, _, ok := strings.Cut(name, "@"); ok {
		return before
	}
	return name
}

// writeJSON marshals v to path. An empty value (per isEmpty) deletes the file.
func writeJSON[T any](path string, v T, isEmpty func(T) bool) error {
	if isEmpty(v) {
		_ = os.Remove(path)
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// readJSON unmarshals path into a fresh T. Missing or malformed files yield
// the zero value (no error) so callers can detect "no sidecar" via emptiness.
func readJSON[T any](path string) T {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		return zero
	}
	if err := json.Unmarshal(data, &zero); err != nil {
		var empty T
		return empty
	}
	return zero
}

// LibsSidecarPath returns the per-binary library list path
// (<safePath>.libs.json) used by the shim's eBPF tracer.
func LibsSidecarPath(safePath string) string { return safePath + ".libs.json" }

// WriteLibsSidecar writes the library list as JSON to LibsSidecarPath(safePath).
// A nil/empty libs list deletes any existing sidecar.
func WriteLibsSidecar(safePath string, libs []string) error {
	return writeJSON(LibsSidecarPath(safePath), libs, func(v []string) bool { return len(v) == 0 })
}

// ReadLibsSidecar reads LibsSidecarPath(safePath) and returns the list.
// Missing or malformed sidecar files yield nil (no error).
func ReadLibsSidecar(safePath string) []string {
	return readJSON[[]string](LibsSidecarPath(safePath))
}

// FilterSidecar carries the --include/--exclude regex source patterns so the
// shim can re-apply them to functions discovered at runtime (dlopen JIT
// instrumentation), matching the filtering already applied at enumeration
// time to statically discovered functions.
type FilterSidecar struct {
	Include string
	Exclude string
}

// FilterSidecarPath returns the per-binary filter sidecar path
// (<safePath>.filter.json) used by the shim's eBPF tracer.
func FilterSidecarPath(safePath string) string { return safePath + ".filter.json" }

// WriteFilterSidecar writes the filter patterns as JSON to
// FilterSidecarPath(safePath). Empty patterns delete any existing sidecar.
func WriteFilterSidecar(safePath string, filter FilterSidecar) error {
	return writeJSON(FilterSidecarPath(safePath), filter, func(v FilterSidecar) bool {
		return v.Include == "" && v.Exclude == ""
	})
}

// ReadFilterSidecar reads FilterSidecarPath(safePath) and returns the
// patterns. Missing or malformed sidecar files yield the zero value (no
// filtering, no error).
func ReadFilterSidecar(safePath string) FilterSidecar {
	return readJSON[FilterSidecar](FilterSidecarPath(safePath))
}
