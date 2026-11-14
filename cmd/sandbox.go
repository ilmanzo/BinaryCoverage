package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"funkoverage/internal/funkutil"
)

var unitDirs = []string{"/etc/systemd/system", "/run/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system"}

// warnSandboxedUnits warns about systemd services that run realTarget under
// a sandbox the tracer cannot work in: the service still starts (the shim
// fails open), but its coverage stays at 0%.
func warnSandboxedUnits(realTarget string) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	for _, unit := range unitsExecuting(unitDirs, realTarget) {
		out, err := exec.Command("systemctl", "show", "-p", "SystemCallFilter", "-p", "PrivateUsers",
			"-p", "ProtectSystem", "-p", "ReadWritePaths", unit).Output()
		if err != nil {
			continue
		}
		for _, p := range sandboxProblems(parseUnitProps(string(out)), funkutil.LogDir()) {
			fmt.Fprintf(os.Stderr, "warning: %s: %s; it will run untraced (see docs/install.md)\n", unit, p)
		}
	}
}

// unitsExecuting returns the services whose Exec*= lines run realTarget.
// Template units are returned as an instance, which systemctl show accepts.
// shortcut: reads unit files only, not drop-ins or wrapper scripts; upgrade
// if a target is started that way.
func unitsExecuting(dirs []string, realTarget string) []string {
	var units []string
	for _, dir := range dirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			name := e.Name()
			// Symlinks are aliases or enablement links of a unit found elsewhere.
			if e.Type()&fs.ModeSymlink != 0 || !strings.HasSuffix(name, ".service") {
				continue
			}
			if strings.HasSuffix(name, "@.service") {
				name = strings.TrimSuffix(name, ".service") + "funkoverage.service"
			}
			if !slices.Contains(units, name) && execsTarget(filepath.Join(dir, e.Name()), realTarget) {
				units = append(units, name)
			}
		}
	}
	return units
}

func execsTarget(unitFile, realTarget string) bool {
	f, err := os.Open(unitFile)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := strings.Cut(strings.TrimSpace(sc.Text()), "=")
		if !ok || !strings.HasPrefix(key, "Exec") {
			continue
		}
		fields := strings.Fields(strings.TrimLeft(val, "-@:+!"))
		if len(fields) == 0 {
			continue
		}
		path := fields[0]
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			path = resolved
		}
		if path == realTarget {
			return true
		}
	}
	return false
}

func parseUnitProps(out string) map[string]string {
	props := map[string]string{}
	for line := range strings.Lines(out) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props
}

// sandboxProblems lists the unit settings that keep the tracer from working.
func sandboxProblems(props map[string]string, logDir string) []string {
	var problems []string
	// systemctl shows an allow-list expanded, a deny-list as "~names", no filter as "~".
	filter := props["SystemCallFilter"]
	if deny, ok := strings.CutPrefix(filter, "~"); ok {
		if slices.Contains(strings.Fields(deny), "bpf") {
			problems = append(problems, "SystemCallFilter denies bpf")
		}
	} else if filter != "" && !slices.Contains(strings.Fields(filter), "bpf") {
		problems = append(problems, "SystemCallFilter does not allow bpf")
	}
	if pu := props["PrivateUsers"]; pu != "" && pu != "no" {
		problems = append(problems, "PrivateUsers="+pu+" (capabilities do not apply outside the unit's user namespace)")
	}
	if props["ProtectSystem"] == "strict" && !writable(logDir, props["ReadWritePaths"]) {
		problems = append(problems, "ProtectSystem=strict without ReadWritePaths="+logDir)
	}
	return problems
}

func writable(dir, readWritePaths string) bool {
	for _, p := range strings.Fields(readWritePaths) {
		p = strings.TrimLeft(p, "-+")
		if dir == p || strings.HasPrefix(dir, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}
