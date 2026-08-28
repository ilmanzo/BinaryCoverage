package funkutil

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func TestFuncListRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	funcs := map[string]ImageFuncs{
		"/var/coverage/bin/ssh": {Names: []string{"main", "do_authentication"}},
		"/usr/lib64/libcrypto.so.3": {
			BuildID: "cbd15edf3ba464",
			Names:   []string{"EVP_DigestInit", "EVP_DigestUpdate"},
			Offsets: []FuncAddr{{Name: "ossl_rand_pool_new", Offset: 0x34160}},
		},
	}

	if err := WriteFuncList(safe, funcs); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(FuncListPath(safe)); err != nil {
		t.Fatalf("sidecar not created: %v", err)
	}

	got := ReadFuncList(safe)
	if !reflect.DeepEqual(got, funcs) {
		t.Errorf("roundtrip: got %v, want %v", got, funcs)
	}
}

func TestFuncListEmptyDeletes(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	_ = WriteFuncList(safe, map[string]ImageFuncs{"/x": {Names: []string{"f"}}})

	if err := WriteFuncList(safe, nil); err != nil {
		t.Fatalf("delete with nil: %v", err)
	}
	if _, err := os.Stat(FuncListPath(safe)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after nil write, stat err: %v", err)
	}

	_ = WriteFuncList(safe, map[string]ImageFuncs{"/x": {Names: []string{"f"}}})
	if err := WriteFuncList(safe, map[string]ImageFuncs{}); err != nil {
		t.Fatalf("delete with empty: %v", err)
	}
	if _, err := os.Stat(FuncListPath(safe)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after empty-map write, stat err: %v", err)
	}

	if got := ReadFuncList(safe); got != nil {
		t.Errorf("missing sidecar should yield nil, got %v", got)
	}
}

func TestReadFuncListMalformed(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	if err := os.WriteFile(FuncListPath(safe), []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := ReadFuncList(safe); got != nil {
		t.Errorf("malformed sidecar should yield nil, got %v", got)
	}
}

func TestFuncListPath(t *testing.T) {
	got := FuncListPath("/var/coverage/bin/ssh")
	want := "/var/coverage/bin/ssh.funcs.json"
	if got != want {
		t.Errorf("FuncListPath: got %q, want %q", got, want)
	}
}

// TestReadFuncListLegacyFormat pins the upgrade path: a sidecar written by a
// pre-0.8.4 install is a bare array of names per image, and must still load as
// a name-only attach plan rather than silently tracing nothing.
func TestReadFuncListLegacyFormat(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	legacy := `{"/usr/bin/ssh":["main","do_authentication"]}`
	if err := os.WriteFile(FuncListPath(safe), []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	got := ReadFuncList(safe)
	want := map[string]ImageFuncs{
		"/usr/bin/ssh": {Names: []string{"main", "do_authentication"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("legacy sidecar: got %v, want %v", got, want)
	}
}

func TestImageFuncsAll(t *testing.T) {
	i := ImageFuncs{
		Names:   []string{"a", "b"},
		Offsets: []FuncAddr{{Name: "c", Offset: 1}},
	}
	if got, want := slices.Collect(i.All()), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Errorf("All: got %v, want %v", got, want)
	}
	if i.Len() != 3 {
		t.Errorf("Len: got %d, want 3", i.Len())
	}

	// Breaking out mid-iteration must stop both loops, not just the one it
	// happened to be in.
	for _, stopAfter := range []int{1, 3} {
		var got []string
		for name := range i.All() {
			got = append(got, name)
			if len(got) == stopAfter {
				break
			}
		}
		if len(got) != stopAfter {
			t.Errorf("All kept yielding after break at %d: got %v", stopAfter, got)
		}
	}

	var empty ImageFuncs
	if got := slices.Collect(empty.All()); got != nil {
		t.Errorf("All on a zero ImageFuncs: got %v, want nothing", got)
	}
	if empty.Len() != 0 {
		t.Errorf("Len on a zero ImageFuncs: got %d, want 0", empty.Len())
	}
}
