package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSandboxProblems(t *testing.T) {
	const logDir = "/var/coverage/data"
	tests := []struct {
		name  string
		props map[string]string
		want  []string // substrings, one per expected problem
	}{
		{"no sandbox", map[string]string{"SystemCallFilter": "~", "PrivateUsers": "no", "ProtectSystem": "no"}, nil},
		{"allow-list without bpf", map[string]string{"SystemCallFilter": "accept brk read"}, []string{"does not allow bpf"}},
		{"allow-list with bpf", map[string]string{"SystemCallFilter": "accept bpf brk"}, nil},
		{"deny-list with bpf", map[string]string{"SystemCallFilter": "~bpf reboot"}, []string{"denies bpf"}},
		{"deny-list without bpf", map[string]string{"SystemCallFilter": "~reboot"}, nil},
		{"private users", map[string]string{"PrivateUsers": "yes"}, []string{"PrivateUsers=yes"}},
		{"strict, log dir not writable", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/proc/irq"}, []string{"ProtectSystem=strict"}},
		{"strict, parent writable", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "-/var/coverage/"}, nil},
		{"strict, sibling prefix", map[string]string{"ProtectSystem": "strict", "ReadWritePaths": "/var/cov"}, []string{"ProtectSystem=strict"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sandboxProblems(tt.props, logDir)
			if len(got) != len(tt.want) {
				t.Fatalf("got %q, want %d problems matching %q", got, len(tt.want), tt.want)
			}
			for i, w := range tt.want {
				if !strings.Contains(got[i], w) {
					t.Errorf("problem %d = %q, want it to contain %q", i, got[i], w)
				}
			}
		})
	}
}

func TestUnitsExecuting(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "daemon")
	if err := os.WriteFile(target, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "daemon-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	units := map[string]string{
		"plain.service":    "[Service]\nExecStart=" + target + " --foreground\n",
		"prefixed.service": "[Service]\nExecStartPre=-" + target + " --check\n",
		"symlink.service":  "[Service]\nExecStart=" + link + "\n",
		"tmpl@.service":    "[Service]\nExecStart=" + target + " %i\n",
		"other.service":    "[Service]\nExecStart=" + target + "x\n",
		"plain.timer":      "[Timer]\nExecStart=" + target + "\n",
	}
	for name, body := range units {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(dir, "plain.service"), filepath.Join(dir, "alias.service")); err != nil {
		t.Fatal(err)
	}

	got := unitsExecuting([]string{dir, dir}, target)
	slices.Sort(got)
	want := []string{"plain.service", "prefixed.service", "symlink.service", "tmpl@funkoverage.service"}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
