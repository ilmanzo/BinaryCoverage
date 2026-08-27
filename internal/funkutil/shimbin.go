package funkutil

// ShimBinaryPath returns the per-binary sidecar path (<safePath>.shimbin.json)
// recording the stable funkoverage-shim binary path resolved at install
// time. The shim needs this to spawn its background tracer helper: once
// running as the target's own installed path (e.g. /usr/bin/bzip2),
// os.Executable() resolves to that per-target path rather than the stable
// shim location, and re-execing it for the helper would leave that
// per-target path mapped as the helper's running text — causing
// `funkoverage uninstall` to fail with ETXTBSY for as long as the helper
// stays attached.
func ShimBinaryPath(safePath string) string { return safePath + ".shimbin.json" }

// WriteShimBinary writes shimPath as JSON to ShimBinaryPath(safePath).
func WriteShimBinary(safePath, shimPath string) error {
	return writeJSON(ShimBinaryPath(safePath), shimPath, func(v string) bool { return v == "" })
}

// ReadShimBinary reads ShimBinaryPath(safePath). Missing or malformed
// sidecars yield "" (no error).
func ReadShimBinary(safePath string) string {
	return readJSON[string](ShimBinaryPath(safePath))
}
