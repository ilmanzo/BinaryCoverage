package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const versionString = "0.7.0"

func main() {
	if len(os.Args) < 2 {
		fmt.Print(helpText)
		os.Exit(1)
	}

	installCmd := flag.NewFlagSet("install", flag.ExitOnError)
	installNoLibs := installCmd.Bool("no-libs", false, "Skip library tracing")

	uninstallCmd := flag.NewFlagSet("uninstall", flag.ExitOnError)

	traceCmd := flag.NewFlagSet("trace", flag.ExitOnError)
	traceNoLibs := traceCmd.Bool("no-libs", false, "Skip library tracing")

	enumerateCmd := flag.NewFlagSet("enumerate", flag.ExitOnError)
	enumerateNoLibs := enumerateCmd.Bool("no-libs", false, "Skip library enumeration")

	reportCmd := flag.NewFlagSet("report", flag.ExitOnError)
	reportFormats := reportCmd.String("formats", "html,txt,xml", "Comma-separated list: html,xml,txt")

	switch os.Args[1] {
	case "help", "--help", "-h":
		fmt.Print(helpText)

	case "version", "--version", "-v":
		fmt.Println("funkoverage version", versionString)

	case "setup":
		if err := setupBpftrace(); err != nil {
			fmt.Fprintln(os.Stderr, "setup error:", err)
			os.Exit(1)
		}

	case "install":
		installCmd.Parse(os.Args[2:])
		if installCmd.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "install: missing binary path(s)")
			os.Exit(1)
		}
		if err := installMany(installCmd.Args(), *installNoLibs); err != nil {
			fmt.Fprintln(os.Stderr, "install error:", err)
			os.Exit(1)
		}

	case "uninstall":
		uninstallCmd.Parse(os.Args[2:])
		if uninstallCmd.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "uninstall: missing binary path(s)")
			os.Exit(1)
		}
		if err := uninstallMany(uninstallCmd.Args()); err != nil {
			fmt.Fprintln(os.Stderr, "uninstall error:", err)
			os.Exit(1)
		}

	case "trace":
		traceCmd.Parse(os.Args[2:])
		if traceCmd.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "trace: missing binary path")
			os.Exit(1)
		}
		binaryPath := traceCmd.Arg(0)
		args := traceCmd.Args()[1:]
		if err := traceInline(binaryPath, args, *traceNoLibs); err != nil {
			fmt.Fprintln(os.Stderr, "trace error:", err)
			os.Exit(1)
		}

	case "enumerate":
		enumerateCmd.Parse(os.Args[2:])
		if enumerateCmd.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "enumerate: missing binary path")
			os.Exit(1)
		}
		funcs, err := EnumerateFunctions(enumerateCmd.Arg(0), *enumerateNoLibs)
		if err != nil {
			fmt.Fprintln(os.Stderr, "enumerate error:", err)
			os.Exit(1)
		}
		// Sort and print
		type entry struct{ image, name string }
		var entries []entry
		for image, names := range funcs {
			for _, name := range names {
				entries = append(entries, entry{image, name})
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].image != entries[j].image {
				return entries[i].image < entries[j].image
			}
			return entries[i].name < entries[j].name
		})
		for _, e := range entries {
			fmt.Printf("%s %s\n", e.image, e.name)
		}
		fmt.Fprintf(os.Stderr, "Total: %d functions across %d image(s)\n", len(entries), len(funcs))

	case "report", "-r":
		reportCmd.Parse(os.Args[2:])
		if reportCmd.NArg() < 2 {
			fmt.Fprintln(os.Stderr, "report: usage: report <inputdir|log1,log2> <outputdir> [--formats html,xml,txt]")
			os.Exit(1)
		}
		inputArg := reportCmd.Arg(0)
		outputDir := reportCmd.Arg(1)
		formats := strings.Split(*reportFormats, ",")

		logFiles := collectLogFiles(inputArg)
		if len(logFiles) == 0 {
			fmt.Fprintln(os.Stderr, "report: no .log files found")
			os.Exit(1)
		}
		coverage, err := analyzeLogs(logFiles)
		if err != nil {
			fmt.Fprintln(os.Stderr, "report error:", err)
			os.Exit(1)
		}
		for _, format := range formats {
			switch strings.TrimSpace(format) {
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

	// Legacy aliases
	case "wrap", "-w":
		fmt.Fprintln(os.Stderr, "wrap is renamed to 'install'. Use: funkoverage install <binary>")
		os.Exit(1)
	case "unwrap", "-u":
		fmt.Fprintln(os.Stderr, "unwrap is renamed to 'uninstall'. Use: funkoverage uninstall <binary>")
		os.Exit(1)

	default:
		fmt.Fprintln(os.Stderr, "Unknown command:", os.Args[1])
		fmt.Print(helpText)
		os.Exit(1)
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
