package funkutil

// FuncListPath returns the per-binary function-list sidecar path
// (<safePath>.funcs.json) used by the shim to seed eBPF uprobe attachments.
func FuncListPath(safePath string) string { return safePath + ".funcs.json" }

// WriteFuncList writes the per-image function map as JSON to FuncListPath(safePath).
// A nil/empty map deletes any existing sidecar.
func WriteFuncList(safePath string, funcs map[string][]string) error {
	return writeJSON(FuncListPath(safePath), funcs, func(v map[string][]string) bool { return len(v) == 0 })
}

// ReadFuncList reads FuncListPath(safePath) and returns the per-image function map.
// Missing or malformed sidecars yield nil (no error) so callers can detect
// "no sidecar" with len(result) == 0.
func ReadFuncList(safePath string) map[string][]string {
	return readJSON[map[string][]string](FuncListPath(safePath))
}
