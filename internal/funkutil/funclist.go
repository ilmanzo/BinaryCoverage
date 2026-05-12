package funkutil

import (
	"encoding/json"
	"os"
)

// FuncListPath returns the per-binary function-list sidecar path
// (<safePath>.funcs.json) used by the shim to seed eBPF uprobe attachments.
func FuncListPath(safePath string) string {
	return safePath + ".funcs.json"
}

// WriteFuncList writes the per-image function map as JSON to FuncListPath(safePath).
// A nil/empty map deletes any existing sidecar.
func WriteFuncList(safePath string, funcs map[string][]string) error {
	path := FuncListPath(safePath)
	if len(funcs) == 0 {
		_ = os.Remove(path)
		return nil
	}
	data, err := json.Marshal(funcs)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// ReadFuncList reads FuncListPath(safePath) and returns the per-image function map.
// Missing or malformed sidecars yield nil (no error) so callers can detect
// "no sidecar" with len(result) == 0.
func ReadFuncList(safePath string) map[string][]string {
	data, err := os.ReadFile(FuncListPath(safePath))
	if err != nil {
		return nil
	}
	var funcs map[string][]string
	if err := json.Unmarshal(data, &funcs); err != nil {
		return nil
	}
	return funcs
}
