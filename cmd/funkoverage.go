package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const versionString = "0.3.2"

// --- CLI ---

func printHelp() {
	fmt.Println(`Usage:
  wrap /path/to/binary
      Wrap the given ELF binary with the Pin coverage wrapper.

  unwrap /path/to/binary
      Restore the original binary previously wrapped.

  report <inputdir|log1.txt,log2.txt> <outputdir> [--formats <formats>]
      Generate coverage reports from log files.
      <inputdir>         Directory containing .log files (all will be used)
      log1.txt,log2.txt  Comma-separated list of log files
      <outputdir>        Output directory for reports (mandatory)
      --formats          Comma-separated list: html,xml,txt (default: html,txt,xml)

  help
      Show this help message.

  version
      Show program version.

Environment variables:
  PIN_ROOT            Path to Intel Pin root directory (default: autodetect or required)
  PIN_TOOL_SEARCH_DIR Directory to search for FuncTracer.so (default: /usr/lib64/coverage-tools)
  LOG_DIR             Directory for coverage logs (default: /var/coverage/data)
  SAFE_BIN_DIR        Directory to store original binaries (default: /var/coverage/bin)`)
}

func printVersion() {
	fmt.Println("funkoverage version", versionString)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(1)
	}

	// Define subcommands
	wrapCmd := flag.NewFlagSet("wrap", flag.ExitOnError)
	unwrapCmd := flag.NewFlagSet("unwrap", flag.ExitOnError)
	reportCmd := flag.NewFlagSet("report", flag.ExitOnError)
	reportFormats := reportCmd.String("formats", "html,txt,xml", "Comma-separated list: html,xml,txt (default: html,txt,xml)")

	switch os.Args[1] {
	case "help", "--help", "-h":
		printHelp()
		return
	case "version", "--version", "-v":
		printVersion()
		return
	case "wrap", "-w":
		wrapCmd.Parse(os.Args[2:])
		if wrapCmd.NArg() < 1 {
			fmt.Println("wrap: missing binary path")
			os.Exit(1)
		}
		if err := wrap(wrapCmd.Arg(0)); err != nil {
			fmt.Println("wrap error:", err)
			os.Exit(1)
		}
	case "unwrap", "-u":
		unwrapCmd.Parse(os.Args[2:])
		if unwrapCmd.NArg() < 1 {
			fmt.Println("unwrap: missing binary path")
			os.Exit(1)
		}
		if err := unwrap(unwrapCmd.Arg(0)); err != nil {
			fmt.Println("unwrap error:", err)
			os.Exit(1)
		}
	case "report", "-r":
		reportCmd.Parse(os.Args[2:])
		if reportCmd.NArg() < 2 {
			fmt.Println("report: missing arguments. Usage: report <inputdir|log1.txt,log2.txt> <outputdir> [--formats <formats>]")
			os.Exit(1)
		}
		inputArg := reportCmd.Arg(0)
		outputDir := reportCmd.Arg(1)
		formats := strings.Split(*reportFormats, ",")

		if len(formats) == 0 {
			fmt.Println("report: must specify at least one of html, xml, txt")
			os.Exit(1)
		}

		logFiles := []string{}
		info, err := os.Stat(inputArg)
		if err == nil && info.IsDir() {
			entries, err := os.ReadDir(inputArg)
			if err != nil {
				fmt.Printf("report: failed to read directory %s: %v\n", inputArg, err)
				os.Exit(1)
			}
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
					logFiles = append(logFiles, filepath.Join(inputArg, entry.Name()))
				}
			}
			if len(logFiles) == 0 {
				fmt.Printf("report: no .log files found in directory %s\n", inputArg)
				os.Exit(1)
			}
		} else {
			logFiles = strings.Split(inputArg, ",")
		}
		coverage, err := analyzeLogs(logFiles)
		if err != nil {
			fmt.Println("report error:", err)
			os.Exit(1)
		}
		for _, format := range formats {
			switch format {
			case "txt":
				printTxtReport(coverage)
			case "html":
				_ = os.MkdirAll(outputDir, 0755)
				for image, data := range coverage {
					if err := generateHTMLReport(image, data, outputDir); err != nil {
						fmt.Println("HTML report error:", err)
					}
				}
				_ = generateAggregateHTMLReport(coverage, outputDir)
			case "xml":
				_ = os.MkdirAll(outputDir, 0755)
				for image, data := range coverage {
					if err := generateXUnitReport(image, data, outputDir); err != nil {
						fmt.Println("XUnit report error:", err)
					}
				}
			}
		}
	default:
		fmt.Println("Unknown command:", os.Args[1])
		printHelp()
		os.Exit(1)
	}
}
