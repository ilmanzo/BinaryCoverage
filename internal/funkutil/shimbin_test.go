package funkutil

import (
	"path/filepath"
	"testing"
)

func TestShimBinaryRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")

	if err := WriteShimBinary(safe, "/usr/bin/funkoverage-shim"); err != nil {
		t.Fatalf("write: %v", err)
	}

	if got := ReadShimBinary(safe); got != "/usr/bin/funkoverage-shim" {
		t.Errorf("roundtrip: got %q, want /usr/bin/funkoverage-shim", got)
	}
}

func TestReadShimBinary_Missing(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")

	if got := ReadShimBinary(safe); got != "" {
		t.Errorf("missing sidecar: got %q, want empty", got)
	}
}
