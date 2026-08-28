package funkutil

import (
	"encoding/json"
	"iter"
)

// FuncAddr pairs a function name with its offset from the start of the image
// *file* — not its virtual address, which is what a symbol table records and
// what the uprobe API does not want.
type FuncAddr struct {
	Name   string `json:"n"`
	Offset uint64 `json:"o"`
}

// ImageFuncs is one ELF image's traceable functions as decided at install
// time, split by how the shim has to attach to them.
//
// Names are present in the image's own symbol tables, so the kernel can look
// them up and they are attached by name. Offsets hold functions that exist
// only in the image's external debug file: nothing in the mapped file names
// them, so their file offsets are pre-computed here.
//
// BuildID pins Offsets to the exact file they were computed against. An
// offset is meaningless — worse, actively dangerous, since a uprobe landing
// mid-instruction kills the target with SIGILL — if the library was upgraded
// between install and run, so the shim MUST re-read the build-id at attach
// time and discard Offsets on any mismatch. It is empty when Offsets is
// empty, and when the image carries no build-id note at all (in which case no
// address-based attach is possible for it).
type ImageFuncs struct {
	BuildID string     `json:"build_id,omitempty"`
	Names   []string   `json:"names,omitempty"`
	Offsets []FuncAddr `json:"offsets,omitempty"`
}

// All iterates every function name in the image, by-name and by-address
// alike — what the coverage denominator and the functions log care about.
func (i ImageFuncs) All() iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, name := range i.Names {
			if !yield(name) {
				return
			}
		}
		for _, fa := range i.Offsets {
			if !yield(fa.Name) {
				return
			}
		}
	}
}

// Len is the total number of functions in the image.
func (i ImageFuncs) Len() int { return len(i.Names) + len(i.Offsets) }

// UnmarshalJSON also accepts the pre-0.8.4 encoding, a bare array of names.
// funkoverage can be upgraded while shims are installed, and the sidecar is
// only rewritten by a fresh install — without this, every already-installed
// binary would silently trace nothing until reinstalled.
func (i *ImageFuncs) UnmarshalJSON(data []byte) error {
	var names []string
	if err := json.Unmarshal(data, &names); err == nil {
		i.Names = names
		return nil
	}
	type plain ImageFuncs // shed the method set, or this recurses
	return json.Unmarshal(data, (*plain)(i))
}

// FuncListPath returns the per-binary function-list sidecar path
// (<safePath>.funcs.json) used by the shim to seed eBPF uprobe attachments.
func FuncListPath(safePath string) string { return safePath + ".funcs.json" }

// WriteFuncList writes the per-image function map as JSON to FuncListPath(safePath).
// A nil/empty map deletes any existing sidecar.
func WriteFuncList(safePath string, funcs map[string]ImageFuncs) error {
	return writeJSON(FuncListPath(safePath), funcs, func(v map[string]ImageFuncs) bool { return len(v) == 0 })
}

// ReadFuncList reads FuncListPath(safePath) and returns the per-image function map.
// Missing or malformed sidecars yield nil (no error) so callers can detect
// "no sidecar" with len(result) == 0.
func ReadFuncList(safePath string) map[string]ImageFuncs {
	return readJSON[map[string]ImageFuncs](FuncListPath(safePath))
}
