// Package funkutil holds helpers shared between the funkoverage CLI and
// the funkoverage-shim binary. Both are package main, so anything they
// need to share has to live here.
package funkutil

import (
	"cmp"
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

// LogDir returns LOG_DIR or DefaultLogDir.
func LogDir() string { return cmp.Or(os.Getenv("LOG_DIR"), DefaultLogDir) }

// SafeBinDir returns SAFE_BIN_DIR or DefaultSafeBinDir.
func SafeBinDir() string { return cmp.Or(os.Getenv("SAFE_BIN_DIR"), DefaultSafeBinDir) }

// EnsureLogDir creates dir as 1777 (world-writable + sticky, same contract
// as /tmp). The shim is installed with file capabilities specifically so it
// can be invoked by users other than whoever ran `setup`/`install`; they
// must be able to create their own _called.log, and the sticky bit stops
// one user from deleting or renaming another's. MkdirAll applies the
// umask, so the mode is re-applied with Chmod — best-effort: once the
// directory exists with the right mode (typically created by root during
// `setup`), a later non-root caller can't Chmod a directory it doesn't
// own, and that failure must not be treated as fatal.
func EnsureLogDir(dir string) error {
	if err := os.MkdirAll(dir, 0o1777); err != nil {
		return err
	}
	_ = os.Chmod(dir, os.ModeSticky|0o777)
	return nil
}

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

// coreSystemLibRe matches glibc/ld.so-family runtime libraries: never a
// useful trace target anywhere, at install time or runtime, since they
// carry no application code. Shared between install-time ldd-based
// enumeration (cmd) and runtime dlopen JIT discovery (cmd/shim_binary).
//
// libstdc\+\+ is matched as its own alternative, outside the shared trailing
// \b: "+" is not a word character, so a \b immediately after it can never
// match a following "." (both sides non-word) — under the combined pattern
// this alternative could never actually match a real "libstdc++.so*" path.
var coreSystemLibRe = regexp.MustCompile(`(?i)(?:libc|libm|libpthread|librt|libdl|libthread_db|ld-linux|libgcc_s|libresolv|libnsl|libutil|libanl)\b|libstdc\+\+`)

// noisyDlopenLibRe matches additional libraries that carry thousands of
// symbols and are common dlopen() targets (NSS/PAM/D-Bus modules, systemd,
// SELinux, the glib/gobject stack, ...). These are only skipped for
// *runtime* dlopen rediscovery, which just wants to avoid the attach-time
// cost of wildcard-tracing something bulky and dynamically loaded — they
// are NOT skipped at install time: a binary that directly links libselinux
// or libsystemd (an ordinary ldd dependency) should still get those
// functions statically enumerated and traced like any other dependency.
var noisyDlopenLibRe = regexp.MustCompile(`(?i)(?:libglib|libgobject|libgthread|libgio|libcap|libattr|libpcre|libselinux|libmount|libblkid|libuuid|libpam|libaudit|libdbus|libsystemd|libudev)\b`)

// IsSystemLib reports whether path names a glibc/ld.so-family library that
// is never a useful trace target — used by install-time ldd-based
// enumeration to skip pure runtime libraries while still tracing ordinary
// application dependencies.
func IsSystemLib(path string) bool {
	return coreSystemLibRe.MatchString(filepath.Base(path))
}

// IsNoisyDlopenLib reports whether path names a library that runtime dlopen
// rediscovery should skip in addition to IsSystemLib: bulky, commonly
// dlopen'd libraries that are worth tracing when directly linked (see
// IsSystemLib) but not worth re-instrumenting every time they're dlopen'd.
func IsNoisyDlopenLib(path string) bool {
	base := filepath.Base(path)
	return coreSystemLibRe.MatchString(base) || noisyDlopenLibRe.MatchString(base)
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
