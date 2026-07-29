// Package funkutil holds helpers shared between the funkoverage CLI and
// the funkoverage-shim binary. Both are package main, so anything they
// need to share has to live here.
package funkutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

// funcBlacklist holds mangled names that are never real coverage targets:
// entry points and PLT stub markers rather than user code.
var funcBlacklist = []string{"main", "_init", "_start", ".plt.got", ".plt", "_dl_relocate_static_pie"}

// FuncIsRelevant reports whether a mangled function name is worth tracing —
// excludes entry points, PLT stubs/thunks, and compiler-internal symbols
// (leading "__"). Shared between install-time enumeration (cmd) and runtime
// dlopen JIT discovery (cmd/shim_binary), which must agree on what counts as
// a real function.
func FuncIsRelevant(name string) bool {
	if slices.Contains(funcBlacklist, name) {
		return false
	}
	if strings.HasSuffix(name, "@plt") || strings.HasSuffix(name, "@plt.got") {
		return false
	}
	if strings.HasPrefix(name, "__") {
		return false
	}
	return true
}

// systemLibRe matches glibc/runtime/system libraries that are never useful
// wildcard-trace targets: each carries thousands of symbols, so attaching
// uprobes to all of them blows past attach timeouts and rarely yields
// meaningful coverage for the target program. Shared between install-time
// ldd-based enumeration (cmd) and runtime dlopen JIT discovery
// (cmd/shim_binary), which both want the same "not worth it" list.
//
// libstdc\+\+ is matched as its own alternative, outside the shared trailing
// \b: "+" is not a word character, so a \b immediately after it can never
// match a following "." (both sides non-word) — under the combined pattern
// this alternative could never actually match a real "libstdc++.so*" path.
var systemLibRe = regexp.MustCompile(`(?i)(?:libc|libm|libpthread|librt|libdl|libthread_db|ld-linux|libgcc_s|libglib|libgobject|libgthread|libgio|libcap|libattr|libpcre|libselinux|libmount|libblkid|libuuid|libpam|libaudit|libdbus|libsystemd|libudev|libresolv|libnsl|libutil|libcrypt|libanl)\b|libstdc\+\+`)

// IsSystemLib reports whether path names a system/runtime library that
// install-time enumeration and runtime dlopen discovery should both skip.
func IsSystemLib(path string) bool {
	return systemLibRe.MatchString(filepath.Base(path))
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
