package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"funkoverage/internal/funkutil"
)

const defaultShimSearchDir = "/usr/lib64/coverage-tools"

// install moves the real binary to SAFE_BIN_DIR/<basename>, writes a
// _functions.log, and puts the shim binary at the original path.
// No JSON config — the shim finds the real binary by path convention.
func install(targetBinary string, libScope LibScope, filter *funkutil.FuncFilter) error {
	logDir := funkutil.LogDir()
	safeBinDir := funkutil.SafeBinDir()

	shimBinary, err := findShimBinary()
	if err != nil {
		return err
	}

	target, err := validateInstallTarget(targetBinary, safeBinDir, libScope)
	if err != nil {
		return err
	}

	origInfo, err := os.Stat(target.realTarget)
	if err != nil {
		return fmt.Errorf("stat original binary: %w", err)
	}

	safePath, err := relocateOriginal(target.realTarget, safeBinDir, target.binaryName)
	if err != nil {
		return err
	}

	installMulticallSymlink(safeBinDir, target.isSymlink, filepath.Base(targetBinary), target.binaryName)

	// Enumerate functions after merging debug info so all symbols are available
	funcs, err := enumerateAndPersist(safePath, target.realTarget, target.binaryName, logDir, libScope, filter)
	if err != nil {
		return err
	}

	if err := funkutil.WriteFuncList(safePath, funcs); err != nil {
		return fmt.Errorf("write func list sidecar: %w", err)
	}

	if err := finishInstall(shimBinary, target.realTarget, origInfo); err != nil {
		return err
	}

	if err := funkutil.WriteShimBinary(safePath, shimBinary); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write shim binary sidecar: %v\n", err)
	}

	fmt.Printf("Installed shim for %s (original at %s)\n", targetBinary, safePath)
	return nil
}

// installTarget bundles what validateInstallTarget discovers about the
// binary being installed.
type installTarget struct {
	realTarget string // symlink-resolved absolute path to the real binary
	binaryName string // filepath.Base(realTarget)
	isSymlink  bool   // true if targetBinary itself was a symlink
}

// validateInstallTarget resolves targetBinary to its real path and checks
// it's an installable ELF binary that isn't already shimmed. libScope
// controls whether missing debug info is a hard failure (MainBinaryOnly)
// or just informational (library functions can still come from ldd deps).
func validateInstallTarget(targetBinary, safeBinDir string, libScope LibScope) (installTarget, error) {
	fileInfo, err := os.Lstat(targetBinary)
	if err != nil {
		return installTarget{}, fmt.Errorf("lstat %s: %w", targetBinary, err)
	}
	isSymlink := (fileInfo.Mode() & os.ModeSymlink) != 0

	realTarget, err := filepath.EvalSymlinks(targetBinary)
	if err != nil {
		return installTarget{}, fmt.Errorf("resolve symlink: %w", err)
	}

	binaryName := filepath.Base(realTarget)
	safePath := filepath.Join(safeBinDir, binaryName)

	if _, err := os.Stat(safePath); err == nil {
		return installTarget{}, fmt.Errorf("'%s' already has a shim installed (found %s). Use uninstall first", targetBinary, safePath)
	}

	if !isELF(realTarget) {
		return installTarget{}, fmt.Errorf("'%s' is not an ELF executable", targetBinary)
	}
	found, err := hasDebugInfo(realTarget)
	if err != nil {
		return installTarget{}, fmt.Errorf("debug info check: %w", err)
	}
	if !found && libScope == MainBinaryOnly {
		return installTarget{}, fmt.Errorf("'%s' has no debug information. Install the debug symbols package first", targetBinary)
	}

	return installTarget{realTarget: realTarget, binaryName: binaryName, isSymlink: isSymlink}, nil
}

// relocateOriginal moves realTarget into safeBinDir (as binaryName) and
// merges any external debug info into the relocated copy. Returns the new
// safePath.
func relocateOriginal(realTarget, safeBinDir, binaryName string) (string, error) {
	safePath := filepath.Join(safeBinDir, binaryName)
	if err := os.MkdirAll(safeBinDir, 0755); err != nil {
		return "", err
	}
	if err := move(realTarget, safePath); err != nil {
		return "", err
	}
	if err := mergeDebugIfExternal(safePath, realTarget); err != nil {
		_ = move(safePath, realTarget)
		return "", fmt.Errorf("merge debug symbols: %w", err)
	}
	return safePath, nil
}

// installMulticallSymlink creates safeBinDir/originalName -> binaryName
// when targetBinary was invoked through a symlink with a different name
// (multicall binaries, e.g. busybox-style tools invoked under several
// names) so the alternate name keeps working after install. Soft-fails
// with a warning — a missing multicall alias breaks the alternate name,
// not the whole install.
func installMulticallSymlink(safeBinDir string, isSymlink bool, originalName, binaryName string) {
	if !isSymlink || originalName == binaryName {
		return
	}
	symlinkPath := filepath.Join(safeBinDir, originalName)
	if err := os.Symlink(binaryName, symlinkPath); err != nil {
		fmt.Fprintf(os.Stderr, "warning: multicall symlink %s -> %s: %v\n", symlinkPath, binaryName, err)
	}
}

// enumerateAndPersist enumerates+logs safePath's functions (via the shared
// enumerateFuncs, cmd/enumerate.go), then writes merged library debug info
// (WithLibraries only) and the filter sidecar. Rolls back the move to
// realTarget (undoing relocateOriginal) on enumeration failure — the
// failure mode serious enough that leaving the binary relocated would be
// worse than restoring it. (A later WriteFuncList failure back in install
// does NOT roll back this move — a pre-existing asymmetry, preserved here
// rather than fixed.)
func enumerateAndPersist(safePath, realTarget, binaryName, logDir string, libScope LibScope, filter *funkutil.FuncFilter) (map[string][]string, error) {
	funcs, err := enumerateFuncs(safePath, binaryName, logDir, libScope, filter)
	if err != nil {
		_ = move(safePath, realTarget)
		return nil, err
	}
	if len(funcs) == 0 {
		fmt.Fprintf(os.Stderr, "warning: no functions found in %s statically; coverage will rely entirely on runtime dlopen() discovery\n", safePath)
	}

	if libScope == WithLibraries {
		backups := mergeLibraryDebugInfo(funcs, safePath)
		if err := funkutil.WriteLibBackups(safePath, backups); err != nil {
			fmt.Fprintf(os.Stderr, "warning: write library backup sidecar: %v\n", err)
		}
	}

	if err := funkutil.WriteFilterSidecar(safePath, filter.Sidecar()); err != nil {
		fmt.Fprintf(os.Stderr, "warning: write filter sidecar: %v\n", err)
	}

	return funcs, nil
}

// finishInstall copies the shim binary over the original target's path and
// applies its runtime capabilities.
func finishInstall(shimBinary, realTarget string, origInfo os.FileInfo) error {
	if err := copyFile(shimBinary, realTarget, origInfo); err != nil {
		return fmt.Errorf("install shim binary: %w", err)
	}
	if err := setShimCaps(realTarget); err != nil {
		// Soft-fail: caps only matter for non-root invocation of the shim.
		// Root install + root invocation works without them.
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	// The background tracer helper re-execs shimBinary directly (see
	// docs/design.md's process-identity section) rather than realTarget, so
	// it needs the SAME capabilities on that file too. Otherwise a target
	// whose systemd unit sets User= at the unit level (e.g. postgresql.service)
	// forks its helper as that unprivileged user from the start, and the
	// helper's tracer setup fails outright (e.g. RemoveMemlock: operation
	// not permitted) even though the per-target copy is correctly capable.
	if err := setShimCaps(shimBinary); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}
	return nil
}

// setShimCaps grants the eBPF tracing capabilities the shim needs at runtime.
// File capabilities are an xattr and are NOT preserved by io.Copy, so they
// must be (re)applied to each installed shim copy after the copy completes.
//
// cap_sys_resource covers cilium/ebpf's rlimit.RemoveMemlock(): normally a
// no-op on kernels with memcg-based BPF memory accounting (all supported
// kernels here), but its own accounting-support probe can misfire under BPF
// memory pressure and fall back to raising RLIMIT_MEMLOCK, which requires
// this capability. Without it that fallback hard-fails the whole tracer.
func setShimCaps(shimPath string) error {
	caps := "cap_bpf,cap_perfmon,cap_dac_read_search,cap_sys_resource+ep"
	out, err := exec.Command("setcap", caps, shimPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("setcap %s %s: %w\n%s\nPATH=%s", caps, shimPath, err, out, os.Getenv("PATH"))
	}
	return nil
}

func uninstall(targetBinary string) error {
	safeBinDir := funkutil.SafeBinDir()

	realTarget, err := filepath.EvalSymlinks(targetBinary)
	if err != nil {
		return fmt.Errorf("resolve symlink: %w", err)
	}

	binaryName := filepath.Base(realTarget)
	safePath := filepath.Join(safeBinDir, binaryName)

	fi, err := os.Lstat(safePath)
	if err != nil {
		return fmt.Errorf("original binary not found at %s: %w", safePath, err)
	}

	sourcePath := safePath
	if (fi.Mode() & os.ModeSymlink) != 0 {
		resolved, err := filepath.EvalSymlinks(safePath)
		if err != nil {
			return fmt.Errorf("resolve backup symlink: %w", err)
		}
		if err := os.Remove(safePath); err != nil {
			fmt.Fprintf(os.Stderr, "warning: remove backup symlink: %v\n", err)
		}
		sourcePath = resolved
	}

	if err := move(sourcePath, realTarget); err != nil {
		return fmt.Errorf("restore binary: %w", err)
	}
	_ = funkutil.WriteFuncList(safePath, nil)
	restoreLibraryBackups(safePath)

	originalName := filepath.Base(targetBinary)
	if originalName != binaryName {
		mcLink := filepath.Join(safeBinDir, originalName)
		if _, err := os.Lstat(mcLink); err == nil {
			_ = os.Remove(mcLink)
		}
	}

	fmt.Printf("Uninstalled shim for %s (restored original)\n", targetBinary)
	return nil
}

// forEachBinary runs fn for each binary, logging and collecting failures
// rather than stopping at the first one, and reports the full failed set as
// a single error.
func forEachBinary(binaries []string, verb string, fn func(string) error) error {
	var failed []string
	for _, bin := range binaries {
		if err := fn(bin); err != nil {
			fmt.Fprintf(os.Stderr, "%s error for %s: %v\n", verb, bin, err)
			failed = append(failed, bin)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to %s: %v", verb, failed)
	}
	return nil
}

func installMany(binaries []string, libScope LibScope, filter *funkutil.FuncFilter) error {
	return forEachBinary(binaries, "install", func(bin string) error {
		return install(bin, libScope, filter)
	})
}

func uninstallMany(binaries []string) error {
	return forEachBinary(binaries, "uninstall", uninstall)
}

// setupEnv validates the host environment for the eBPF tracer:
//   - kernel ≥6.6 (uprobe_multi support);
//   - kernel BTF available at /sys/kernel/btf/vmlinux;
//   - LOG_DIR and SAFE_BIN_DIR exist and are writable.
//
// It does not perform any installation — capabilities are applied per-shim
// at install time (see setShimCaps).
func setupEnv() error {
	if err := checkKernelVersion(6, 6); err != nil {
		return err
	}
	if _, err := os.Stat("/sys/kernel/btf/vmlinux"); err != nil {
		return fmt.Errorf("BTF unavailable at /sys/kernel/btf/vmlinux: %w "+
			"(kernel must be built with CONFIG_DEBUG_INFO_BTF=y)", err)
	}
	// LOG_DIR is 1777 (like /tmp), not 0755: the shim is installed with file
	// capabilities so non-root users can invoke a shimmed binary and still
	// write their own _called.log. SAFE_BIN_DIR is root-only.
	if err := funkutil.EnsureLogDir(funkutil.LogDir()); err != nil {
		return fmt.Errorf("create %s: %w", funkutil.LogDir(), err)
	}
	if err := os.MkdirAll(funkutil.SafeBinDir(), 0755); err != nil {
		return fmt.Errorf("create %s: %w", funkutil.SafeBinDir(), err)
	}
	fmt.Println("Environment OK: kernel + BTF + log/bin directories ready.")
	fmt.Println("Run 'funkoverage install <binary>' as root to install a shim.")
	return nil
}

// checkKernelVersion parses `uname -r` and ensures the running kernel is
// at least major.minor. Patch and suffix are ignored.
func checkKernelVersion(wantMajor, wantMinor int) error {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return fmt.Errorf("read kernel release: %w", err)
	}
	maj, min, err := parseKernelVersion(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	if maj < wantMajor || (maj == wantMajor && min < wantMinor) {
		return fmt.Errorf("kernel %d.%d+ required (have %d.%d) for uprobe_multi support",
			wantMajor, wantMinor, maj, min)
	}
	return nil
}

// parseKernelVersion extracts the major.minor version from a kernel release
// string (e.g. "6.6.0-1-default" -> 6, 6), as reported by
// /proc/sys/kernel/osrelease.
func parseKernelVersion(release string) (major, minor int, err error) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("cannot parse kernel release %q", release)
	}
	maj, err1 := strconv.Atoi(parts[0])
	min, err2 := strconv.Atoi(strings.SplitN(parts[1], "-", 2)[0])
	if err1 != nil || err2 != nil {
		return 0, 0, fmt.Errorf("cannot parse kernel release %q", release)
	}
	return maj, min, nil
}

func findShimBinary() (string, error) {
	for _, p := range shimSearchPaths() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", errors.New("funkoverage-shim not found. Set FUNKOVERAGE_SHIM env var or place it alongside funkoverage")
}

func shimSearchPaths() []string {
	paths := []string{os.Getenv("FUNKOVERAGE_SHIM")}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		paths = append(paths, filepath.Join(filepath.Dir(exe), "funkoverage-shim"))
	}
	paths = append(paths, filepath.Join(defaultShimSearchDir, "funkoverage-shim"))
	return paths
}

// copyFile copies src's bytes to dst, applying info's mode (including any
// setuid/setgid/sticky bits) and, best-effort, its owner/group — so a
// shimmed binary keeps the exact privilege semantics of the file it
// replaces. info describes the metadata to apply to dst, independent of
// src's own metadata: the shim-install call site copies bytes from the
// generic shim binary but wears the original target's mode/owner.
func copyFile(src, dst string, info os.FileInfo) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	mode := info.Mode() & preservedModeBits
	// Create with the plain permission bits only — setuid/setgid go on in
	// the final Chmod below. chown (like write) clears any setuid/setgid
	// bit already present, so applying ownership before the create mode
	// could stick would be immediately undone.
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	out.Close()
	if err != nil {
		os.Remove(dst)
		return err
	}
	chownLike(dst, info)
	return os.Chmod(dst, mode)
}
