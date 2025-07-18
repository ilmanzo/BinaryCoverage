package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const versionString = "0.4.3"

// --- CLI ---

func main() {
	if len(os.Args) < 2 {
		fmt.Println(helpText)
		os.Exit(1)
	}

	// Define subcommands
	wrapCmd := flag.NewFlagSet("wrap", flag.ExitOnError)
	unwrapCmd := flag.NewFlagSet("unwrap", flag.ExitOnError)
	reportCmd := flag.NewFlagSet("report", flag.ExitOnError)
	reportFormats := reportCmd.String("formats", "html,txt,xml", "Comma-separated list: html,xml,txt (default: html,txt,xml)")

	switch os.Args[1] {
	case "help", "--help", "-h":
		fmt.Println(helpText)
		return
	case "version", "--version", "-v":
		fmt.Println("funkoverage version", versionString)
		return
	case "wrap", "-w":
		wrapCmd.Parse(os.Args[2:])
		if wrapCmd.NArg() < 1 {
			fmt.Println("wrap: missing binary path(s)")
			os.Exit(1)
		}
		if err := wrapMany(wrapCmd.Args()); err != nil {
			fmt.Println("wrap error:", err)
			os.Exit(1)
		}
	case "unwrap", "-u":
		unwrapCmd.Parse(os.Args[2:])
		if unwrapCmd.NArg() < 1 {
			fmt.Println("unwrap: missing binary path(s)")
			os.Exit(1)
		}
		if err := unwrapMany(unwrapCmd.Args()); err != nil {
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
		fmt.Println(helpText)
		os.Exit(1)
	}
}
