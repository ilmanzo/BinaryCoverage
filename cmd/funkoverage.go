package main

import (
	"cmp"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"funkoverage/internal/funkutil"

	"golang.org/x/sync/errgroup"
)

var versionString = "dev"

// command is one funkoverage subcommand. run receives the args after the
// subcommand name (so for "funkoverage install -no-libs foo", run sees
// ["-no-libs", "foo"]). It returns an error to signal a non-zero exit.
type command struct {
	name string
	help string
	run  func(args []string) error
}

func commands() map[string]command {
	cmds := []command{
		{"setup", "validate eBPF environment", cmdSetup},
		{"install", "install shim for binary", cmdInstall},
		{"uninstall", "restore original binary", cmdUninstall},
		{"trace", "run binary under tracing without permanent install", cmdTrace},
		{"enumerate", "list discoverable functions", cmdEnumerate},
		{"report", "generate coverage reports", cmdReport},
		{"version", "print version", cmdVersion},
		{"help", "print help", cmdHelp},
	}
	m := make(map[string]command, len(cmds)+14)
	for _, c := range cmds {
		m[c.name] = c
	}
	// Aliases, all documented in helpText. -u for uninstall is deliberately
	// absent: it's unreachable, shadowed by the "unwrap" deprecation guard
	// below, which intercepts the literal string "-u" before this map is
	// ever consulted.
	m["--help"] = m["help"]
	m["-h"] = m["help"]
	m["--version"] = m["version"]
	m["-v"] = m["version"]
	m["-r"] = m["report"]
	m["--report"] = m["report"]
	m["-i"] = m["install"]
	m["--install"] = m["install"]
	m["--uninstall"] = m["uninstall"]
	m["-t"] = m["trace"]
	m["--trace"] = m["trace"]
	m["-e"] = m["enumerate"]
	m["--enumerate"] = m["enumerate"]
	m["--setup"] = m["setup"]
	return m
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(helpText)
		os.Exit(1)
	}
	name := os.Args[1]
	cmds := commands()

	if name == "wrap" || name == "-w" {
		exitf("wrap is renamed to 'install'. Use: funkoverage install <binary>")
	}
	if name == "unwrap" || name == "-u" {
		exitf("unwrap is renamed to 'uninstall'. Use: funkoverage uninstall <binary>")
	}

	c, ok := cmds[name]
	if !ok {
		fmt.Fprintln(os.Stderr, "Unknown command:", name)
		fmt.Print(helpText)
		os.Exit(1)
	}
	if err := c.run(os.Args[2:]); err != nil {
		// -h on a subcommand: helpText already documents every subcommand's
		// flags, so show that rather than one FlagSet's bare defaults.
		if errors.Is(err, flag.ErrHelp) {
			fmt.Print(helpText)
			return
		}
		fmt.Fprintf(os.Stderr, "%s error: %v\n", c.name, err)
		os.Exit(1)
	}
}

// newFlagSet returns a FlagSet that hands parse errors back to its caller
// instead of writing its own message and calling os.Exit(2) behind main's
// back — which made every `if err := fs.Parse(...)` in this file unreachable.
// Output is discarded so the error surfaces exactly once, through main.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// addFilterFlags binds --include and --exclude on fs and returns a closure
// that builds the FuncFilter after fs.Parse has run.
func addFilterFlags(fs *flag.FlagSet) func() (*funkutil.FuncFilter, error) {
	include := fs.String("include", "", "Regex: only trace functions matching pattern")
	exclude := fs.String("exclude", "", "Regex: skip functions matching pattern")
	return func() (*funkutil.FuncFilter, error) {
		return funkutil.NewFuncFilter(*include, *exclude)
	}
}

// parseInterspersed parses fs, allowing flags to appear after positional
// arguments. Go's flag package stops at the first non-flag argument, so
// documented forms like `report <in> <out> --formats xml` would otherwise
// silently drop the flag. Returns the positional arguments in order. A "--"
// terminator still ends flag parsing as usual.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
	return positional, nil
}

// --- subcommand implementations ---

func cmdHelp(args []string) error    { fmt.Print(helpText); return nil }
func cmdVersion(args []string) error { fmt.Println("funkoverage version", versionString); return nil }
func cmdSetup(args []string) error   { return setupEnv() }

func cmdInstall(args []string) error {
	fs := newFlagSet("install")
	noLibs := fs.Bool("no-libs", false, "Skip library tracing")
	buildFilter := addFilterFlags(fs)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("missing binary path(s)")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	return installMany(positional, LibScope(*noLibs), filter)
}

func cmdUninstall(args []string) error {
	fs := newFlagSet("uninstall")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path(s)")
	}
	return uninstallMany(fs.Args())
}

func cmdTrace(args []string) error {
	fs := newFlagSet("trace")
	noLibs := fs.Bool("no-libs", false, "Skip library tracing")
	buildFilter := addFilterFlags(fs)
	// Deliberately NOT parseInterspersed: everything after the binary path
	// belongs to the traced program, not to funkoverage. `trace foo --help`
	// must pass --help to foo, so parsing must stop at the first positional.
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	code, err := traceInline(fs.Arg(0), fs.Args()[1:], LibScope(*noLibs), filter)
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

func cmdEnumerate(args []string) error {
	fs := newFlagSet("enumerate")
	noLibs := fs.Bool("no-libs", false, "Skip library enumeration")
	buildFilter := addFilterFlags(fs)
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 1 {
		return fmt.Errorf("missing binary path")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	_, display, err := EnumerateFunctions(positional[0], LibScope(*noLibs), filter)
	if err != nil {
		return err
	}
	type entry struct{ image, name string }
	var entries []entry
	for image, names := range display {
		for _, name := range names {
			entries = append(entries, entry{image, name})
		}
	}
	slices.SortFunc(entries, func(a, b entry) int {
		if c := cmp.Compare(a.image, b.image); c != 0 {
			return c
		}
		return cmp.Compare(a.name, b.name)
	})
	for _, e := range entries {
		fmt.Printf("%s %s\n", e.image, e.name)
	}
	fmt.Fprintf(os.Stderr, "Total: %d functions across %d image(s)\n", len(entries), len(display))
	return nil
}

func cmdReport(args []string) error {
	fs := newFlagSet("report")
	formats := fs.String("formats", "html,txt,xml", "Comma-separated list: html,xml,txt")
	positional, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(positional) < 2 {
		return fmt.Errorf("usage: report <inputdir|log1,log2> <outputdir> [--formats html,xml,txt]")
	}
	inputArg, outputDir := positional[0], positional[1]

	// A traced short-lived binary may have just exited: its shim's
	// background tracer helper flushes and closes the log asynchronously,
	// so a report run immediately afterward (as e2e/CI scripts commonly
	// do) can otherwise race an in-progress flush and undercount coverage.
	funkutil.WaitForDrain(5 * time.Second)

	logFiles := collectLogFiles(inputArg)
	if len(logFiles) == 0 {
		return fmt.Errorf("no .log files found")
	}
	coverage, err := analyzeLogs(logFiles)
	if err != nil {
		return err
	}
	// Built once and shared by every requested format.
	set := buildReportSet(coverage)
	var errs []error
	for format := range strings.SplitSeq(*formats, ",") {
		if err := emitReport(strings.TrimSpace(format), set, outputDir); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func emitReport(format string, set reportSet, outputDir string) error {
	switch format {
	case "txt":
		printTxtReport(os.Stdout, set)
	case "html":
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", outputDir, err)
		}
		perImage(set.Images, outputDir, "HTML report error:", generateHTMLReport)
		if err := generateAggregateHTMLReport(set.Totals, outputDir); err != nil {
			return fmt.Errorf("aggregate html report: %w", err)
		}
	case "xml":
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("create %s: %w", outputDir, err)
		}
		perImage(set.Images, outputDir, "XUnit report error:", generateXUnitReport)
	default:
		return fmt.Errorf("unknown format %q (want html, xml or txt)", format)
	}
	return nil
}

// perImage runs fn concurrently for every image. A per-image error is logged
// under errLabel, not returned — one bad image shouldn't stop the rest of the
// report.
func perImage(images []imageReport, outputDir, errLabel string, fn func(rep imageReport, outputDir string) error) {
	g := new(errgroup.Group)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for _, rep := range images {
		g.Go(func() error {
			if err := fn(rep, outputDir); err != nil {
				fmt.Fprintln(os.Stderr, errLabel, err)
			}
			return nil
		})
	}
	_ = g.Wait()
}

func collectLogFiles(inputArg string) []string {
	info, err := os.Stat(inputArg)
	if err == nil && info.IsDir() {
		entries, err := os.ReadDir(inputArg)
		if err != nil {
			return nil
		}
		var files []string
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
				files = append(files, filepath.Join(inputArg, entry.Name()))
			}
		}
		return files
	}
	return strings.Split(inputArg, ",")
}
