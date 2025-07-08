package main

import (
	"bufio"
	"encoding/xml"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CoverageData struct {
	TotalFunctions  map[string]struct{}
	CalledFunctions map[string]struct{}
}

type FunctionEntry struct {
	Name   string
	Status string // "called" or "uncalled"
}

type HTMLReportData struct {
	ImageName          string
	TotalCount         int
	CalledCount        int
	UncalledCount      int
	CoveragePercentage float64
	Functions          []FunctionEntry
	GeneratedAt        string // Add this field
}

// --- Coverage Analysis ---

var (
	functionDefRe  = regexp.MustCompile(`\[Image:(.*?)\] \[Function:(.*?)\]`)
	functionCallRe = regexp.MustCompile(`\[Image:(.*?)\] \[Called:(.*?)\]`)
)

// analyzeLogs processes the log files and extracts coverage data for each image.
func analyzeLogs(logFiles []string) (map[string]*CoverageData, error) {
	coverage := make(map[string]*CoverageData)
	for _, logFile := range logFiles {
		f, err := os.Open(logFile)
		if err != nil {
			return nil, fmt.Errorf("could not open log file %s: %w", logFile, err)
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if m := functionDefRe.FindStringSubmatch(line); m != nil {
				image, function := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
				if image == "" || function == "" {
					continue
				}
				if _, ok := coverage[image]; !ok {
					coverage[image] = &CoverageData{make(map[string]struct{}), make(map[string]struct{})}
				}
				coverage[image].TotalFunctions[function] = struct{}{}
			} else if m := functionCallRe.FindStringSubmatch(line); m != nil {
				image, function := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
				if image == "" || function == "" {
					continue
				}
				if _, ok := coverage[image]; !ok {
					coverage[image] = &CoverageData{make(map[string]struct{}), make(map[string]struct{})}
				}
				coverage[image].CalledFunctions[function] = struct{}{}
			}
		}
		f.Close()
	}
	return coverage, nil
}

// --- Console Report ---
// printTxtReport prints a text-based report to the console summarizing coverage for each image.
func printTxtReport(coverage map[string]*CoverageData) {
	for image, data := range coverage {
		total := len(data.TotalFunctions)
		called := len(data.CalledFunctions)
		uncalled := total - called
		coveragePct := 0.0
		if total > 0 {
			coveragePct = float64(called) / float64(total) * 100
		}
		fmt.Printf("\n==================================================\n")
		fmt.Printf("Image: %s\n", image)
		fmt.Printf("==================================================\n")
		fmt.Printf("  Functions Found:   %d\n", total)
		fmt.Printf("  Functions Called:  %d\n", called)
		fmt.Printf("  Coverage:          %.2f%%\n", coveragePct)
		fmt.Printf("--------------------------------------------------\n")
		if called > 0 {
			fmt.Println("  Called Functions:")
			for fn := range data.CalledFunctions {
				fmt.Printf("    - %s\n", fn)
			}
		} else {
			fmt.Println("  No functions were called for this image.")
		}
		if uncalled > 0 {
			fmt.Println("\n  Uncalled Functions:")
			for fn := range data.TotalFunctions {
				if _, ok := data.CalledFunctions[fn]; !ok {
					fmt.Printf("    - %s\n", fn)
				}
			}
		}
	}
	fmt.Println("\n--- End of Console Report ---")
}

// --- XUnit XML Report ---

type TestSuites struct {
	XMLName   xml.Name    `xml:"testsuites"`
	Generated string      `xml:"generated,attr"`
	TestSuite []TestSuite `xml:"testsuite"`
}
type TestSuite struct {
	Errors   int        `xml:"errors,attr"`
	Failures int        `xml:"failures,attr"`
	Name     string     `xml:"name,attr"`
	Skipped  int        `xml:"skipped,attr"`
	Tests    int        `xml:"tests,attr"`
	TestCase []TestCase `xml:"testcase"`
}
type TestCase struct {
	ClassName string  `xml:"classname,attr"`
	Name      string  `xml:"name,attr"`
	Passed    *Passed `xml:"passed"`
}
type Passed struct {
	Message string `xml:"message,attr"`
	Text    string `xml:",chardata"`
}

// generateXUnitReport generates an XUnit XML report for a single image's coverage data.
func generateXUnitReport(image string, data *CoverageData, outputDir string) error {
	totalFns := make([]string, 0, len(data.TotalFunctions))
	for fn := range data.TotalFunctions {
		totalFns = append(totalFns, fn)
	}
	calledFns := data.CalledFunctions
	totalCount := len(totalFns)
	skippedCount := totalCount - len(calledFns)
	calledList := make([]string, 0, len(calledFns))
	uncalledList := make([]string, 0, skippedCount)
	for fn := range data.TotalFunctions {
		if _, ok := calledFns[fn]; ok {
			calledList = append(calledList, fn)
		} else {
			uncalledList = append(uncalledList, fn)
		}
	}
	safeName := regexp.MustCompile(`[^a-zA-Z0-9._-]`).ReplaceAllString(filepath.Base(image), "_")
	outfile := filepath.Join(outputDir, fmt.Sprintf("coverage_%s.xml", safeName))

	summary := fmt.Sprintf("Coverage Summary for %s | Total Functions: %d | Called Functions: %d | Uncalled Functions: %d | Coverage: %.2f%%",
		safeName, totalCount, len(calledFns), skippedCount, float64(len(calledFns))/float64(totalCount)*100)
	var details strings.Builder
	if len(calledList) > 0 {
		details.WriteString("CALLED FUNCTIONS:\n")
		for _, fn := range calledList {
			details.WriteString(fmt.Sprintf("  ✓ %s\n", fn))
		}
		details.WriteString("\n")
	}
	if len(uncalledList) > 0 {
		details.WriteString("UNCALLED FUNCTIONS:\n")
		for _, fn := range uncalledList {
			details.WriteString(fmt.Sprintf("  ✗ %s\n", fn))
		}
	}
	ts := TestSuites{
		Generated: time.Now().Format("2006-01-02 15:04:05 MST"),
		TestSuite: []TestSuite{
			{
				Errors:   0,
				Failures: 0,
				Name:     "binary_coverage_" + safeName,
				Skipped:  skippedCount,
				Tests:    totalCount,
				TestCase: []TestCase{
					{
						ClassName: "binary_coverage_" + safeName,
						Name:      "Result",
						Passed: &Passed{
							Message: summary,
							Text:    details.String(),
						},
					},
				},
			},
		},
	}
	f, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := xml.NewEncoder(f)
	enc.Indent("", "  ")
	return enc.Encode(ts)
}

type Row struct {
	ImageName   string
	TotalCount  int
	CalledCount int
	CoveragePct float64
}
type AggregateData struct {
	Rows        []Row
	GeneratedAt string
}

// generateHTMLReport generates an HTML report for a single image's coverage data.
// It creates a detailed report with the image name, total functions, called functions,
func generateHTMLReport(image string, data *CoverageData, outputDir string) error {
	totalFns := make([]string, 0, len(data.TotalFunctions))
	for fn := range data.TotalFunctions {
		totalFns = append(totalFns, fn)
	}
	calledFns := data.CalledFunctions
	totalCount := len(totalFns)
	calledCount := len(calledFns)
	uncalledCount := totalCount - calledCount
	coveragePct := 0.0
	if totalCount > 0 {
		coveragePct = float64(calledCount) / float64(totalCount) * 100
	}
	functions := make([]FunctionEntry, 0, totalCount)
	for _, fn := range totalFns {
		status := "uncalled"
		if _, ok := calledFns[fn]; ok {
			status = "called"
		}
		functions = append(functions, FunctionEntry{Name: fn, Status: status})
	}
	reportData := HTMLReportData{
		ImageName:          filepath.Base(image),
		TotalCount:         totalCount,
		CalledCount:        calledCount,
		UncalledCount:      uncalledCount,
		CoveragePercentage: coveragePct,
		Functions:          functions,
		GeneratedAt:        time.Now().Format("2006-01-02 15:04:05 MST"),
	}
	tmpl, err := template.New("report").Parse(detailedHTMLTemplateStr)
	if err != nil {
		return err
	}
	safeName := regexp.MustCompile(`[^a-zA-Z0-9._-]`).ReplaceAllString(filepath.Base(image), "_")
	outfile := filepath.Join(outputDir, fmt.Sprintf("%s.html", safeName))
	f, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, reportData)
}

// generateAggregateHTMLReport generates an HTML report summarizing coverage across all images.
// It creates a table with the image name, total functions, called functions, and coverage percentage.
func generateAggregateHTMLReport(coverage map[string]*CoverageData, outputDir string) error {
	// Collect and sort image names
	imageNames := make([]string, 0, len(coverage))
	for image := range coverage {
		imageNames = append(imageNames, image)
	}
	sort.Strings(imageNames)

	rows := []Row{}
	for _, image := range imageNames {
		data := coverage[image]
		total := len(data.TotalFunctions)
		called := len(data.CalledFunctions)
		coveragePct := 0.0
		if total > 0 {
			coveragePct = float64(called) / float64(total) * 100
		}
		rows = append(rows, Row{
			ImageName:   filepath.Base(image),
			TotalCount:  total,
			CalledCount: called,
			CoveragePct: coveragePct,
		})
	}

	aggData := AggregateData{
		Rows:        rows,
		GeneratedAt: time.Now().Format("2006-01-02 15:04:05 MST"),
	}

	tmpl, err := template.New("aggregate").Parse(aggregateHTMLTemplate)
	if err != nil {
		return err
	}
	outfile := filepath.Join(outputDir, "aggregate.html")
	f, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	return tmpl.Execute(f, aggData)
}
