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
	if i := strings.IndexByte(name, '@'); i >= 0 {
		return name[:i]
	}
	return name
}

// LibsSidecarPath returns the per-binary library list path
// (<safePath>.libs.json) used by the shim's eBPF tracer.
func LibsSidecarPath(safePath string) string {
	return safePath + ".libs.json"
}

// WriteLibsSidecar writes the library list as JSON to LibsSidecarPath(safePath).
// A nil/empty libs list deletes any existing sidecar.
func WriteLibsSidecar(safePath string, libs []string) error {
	path := LibsSidecarPath(safePath)
	if len(libs) == 0 {
		_ = os.Remove(path)
		return nil
	}
	data, err := json.Marshal(libs)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ReadLibsSidecar reads LibsSidecarPath(safePath) and returns the list.
// Missing or malformed sidecar files yield nil (no error).
func ReadLibsSidecar(safePath string) []string {
	data, err := os.ReadFile(LibsSidecarPath(safePath))
	if err != nil {
		return nil
	}
	var libs []string
	if err := json.Unmarshal(data, &libs); err != nil {
		return nil
	}
	return libs
}
