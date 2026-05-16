package main

import (
	"cmp"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	m := make(map[string]command, len(cmds)+4)
	for _, c := range cmds {
		m[c.name] = c
	}
	// Aliases
	m["--help"] = m["help"]
	m["-h"] = m["help"]
	m["--version"] = m["version"]
	m["-v"] = m["version"]
	m["-r"] = m["report"]
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
		fmt.Fprintf(os.Stderr, "%s error: %v\n", c.name, err)
		os.Exit(1)
	}
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// addFilterFlags binds --include and --exclude on fs and returns a closure
// that builds the FuncFilter after fs.Parse has run.
func addFilterFlags(fs *flag.FlagSet) func() (*FuncFilter, error) {
	include := fs.String("include", "", "Regex: only trace functions matching pattern")
	exclude := fs.String("exclude", "", "Regex: skip functions matching pattern")
	return func() (*FuncFilter, error) {
		return NewFuncFilter(*include, *exclude)
	}
}

// --- subcommand implementations ---

func cmdHelp(args []string) error    { fmt.Print(helpText); return nil }
func cmdVersion(args []string) error { fmt.Println("funkoverage version", versionString); return nil }
func cmdSetup(args []string) error   { return setupEnv() }

func cmdInstall(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	noLibs := fs.Bool("no-libs", false, "Skip library tracing")
	buildFilter := addFilterFlags(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path(s)")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	return installMany(fs.Args(), *noLibs, filter)
}

func cmdUninstall(args []string) error {
	fs := flag.NewFlagSet("uninstall", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path(s)")
	}
	return uninstallMany(fs.Args())
}

func cmdTrace(args []string) error {
	fs := flag.NewFlagSet("trace", flag.ExitOnError)
	noLibs := fs.Bool("no-libs", false, "Skip library tracing")
	buildFilter := addFilterFlags(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	code, err := traceInline(fs.Arg(0), fs.Args()[1:], *noLibs, filter)
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

func cmdEnumerate(args []string) error {
	fs := flag.NewFlagSet("enumerate", flag.ExitOnError)
	noLibs := fs.Bool("no-libs", false, "Skip library enumeration")
	buildFilter := addFilterFlags(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("missing binary path")
	}
	filter, err := buildFilter()
	if err != nil {
		return err
	}
	funcs, err := EnumerateFunctions(fs.Arg(0), *noLibs, filter)
	if err != nil {
		return err
	}
	type entry struct{ image, name string }
	var entries []entry
	for image, names := range funcs {
		for _, name := range names {
			entries = append(entries, entry{image, demangleName(name)})
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
	fmt.Fprintf(os.Stderr, "Total: %d functions across %d image(s)\n", len(entries), len(funcs))
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	formats := fs.String("formats", "html,txt,xml", "Comma-separated list: html,xml,txt")
	fs.Parse(args)
	if fs.NArg() < 2 {
		return fmt.Errorf("usage: report <inputdir|log1,log2> <outputdir> [--formats html,xml,txt]")
	}
	inputArg, outputDir := fs.Arg(0), fs.Arg(1)

	logFiles := collectLogFiles(inputArg)
	if len(logFiles) == 0 {
		return fmt.Errorf("no .log files found")
	}
	coverage, err := analyzeLogs(logFiles)
	if err != nil {
		return err
	}
	for format := range strings.SplitSeq(*formats, ",") {
		emitReport(strings.TrimSpace(format), coverage, outputDir)
	}
	return nil
}

func emitReport(format string, coverage map[string]*CoverageData, outputDir string) {
	switch format {
	case "txt":
		printTxtReport(coverage)
	case "html":
		_ = os.MkdirAll(outputDir, 0755)
		for image, data := range coverage {
			if err := generateHTMLReport(image, data, outputDir); err != nil {
				fmt.Fprintln(os.Stderr, "HTML report error:", err)
			}
		}
		_ = generateAggregateHTMLReport(coverage, outputDir)
	case "xml":
		_ = os.MkdirAll(outputDir, 0755)
		for image, data := range coverage {
			if err := generateXUnitReport(image, data, outputDir); err != nil {
				fmt.Fprintln(os.Stderr, "XUnit report error:", err)
			}
		}
	}
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
