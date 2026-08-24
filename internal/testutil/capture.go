// Package testutil holds small test-only helpers shared between the
// funkoverage CLI and funkoverage-shim test suites (both package main, so
// they can't import each other's test helpers directly).
package testutil

import (
	"io"
	"os"
	"testing"
)

// CaptureOutput redirects *stream to a pipe for the duration of fn and
// returns everything written to it. Pass &os.Stdout or &os.Stderr.
func CaptureOutput(t *testing.T, stream **os.File, fn func()) string {
	t.Helper()
	old := *stream
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	*stream = w
	fn()
	w.Close()
	*stream = old
	out, _ := io.ReadAll(r)
	return string(out)
}
