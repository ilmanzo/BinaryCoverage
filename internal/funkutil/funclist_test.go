package funkutil

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestFuncListRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	safe := filepath.Join(tmp, "mybin")
	funcs := map[string][]string{
		"/var/coverage/bin/ssh":     {"main", "do_authentication"},
		"/usr/lib64/libcrypto.so.3": {"EVP_DigestInit", "EVP_DigestUpdate"},
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
	_ = WriteFuncList(safe, map[string][]string{"/x": {"f"}})

	if err := WriteFuncList(safe, nil); err != nil {
		t.Fatalf("delete with nil: %v", err)
	}
	if _, err := os.Stat(FuncListPath(safe)); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed after nil write, stat err: %v", err)
	}

	_ = WriteFuncList(safe, map[string][]string{"/x": {"f"}})
	if err := WriteFuncList(safe, map[string][]string{}); err != nil {
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
