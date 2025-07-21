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

	"github.com/ianlancetaylor/demangle"
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

func extractImageAndFunction(m []string) (string, string) {
	image, function := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	function = demangle.Filter(function) // Apply demangling for c++
	return image, function
}

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
				image, function := extractImageAndFunction(m)
				if image == "" || function == "" {
					continue
				}
				if _, ok := coverage[image]; !ok {
					coverage[image] = &CoverageData{make(map[string]struct{}), make(map[string]struct{})}
				}
				coverage[image].TotalFunctions[function] = struct{}{}
			} else if m := functionCallRe.FindStringSubmatch(line); m != nil {
				image, function := extractImageAndFunction(m)
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
	summary := summarizeCoverage(coverage)
	for _, row := range summary.Rows {
		uncalled := row.TotalCount - row.CalledCount
		fmt.Printf("\n==================================================\n")
		fmt.Printf("Image: %s\n", row.ImageName)
		fmt.Printf("==================================================\n")
		fmt.Printf("  Functions Found:   %d\n", row.TotalCount)
		fmt.Printf("  Functions Called:  %d\n", row.CalledCount)
		fmt.Printf("  Coverage:          %.2f%%\n", row.CoveragePct)
		fmt.Printf("--------------------------------------------------\n")
		if row.CalledCount > 0 {
			fmt.Println("  Called Functions:")
			// Print called functions (need to look up in coverage map)
			for fn := range coverage[row.ImageName].CalledFunctions {
				fmt.Printf("    - %s\n", fn)
			}
		} else {
			fmt.Println("  No functions were called for this image.")
		}
		if uncalled > 0 {
			fmt.Println("\n  Uncalled Functions:")
			for fn := range coverage[row.ImageName].TotalFunctions {
				if _, ok := coverage[row.ImageName].CalledFunctions[fn]; !ok {
					fmt.Printf("    - %s\n", fn)
				}
			}
		}
	}
	// Print totals
	fmt.Println("\n==================== Totals ======================")
	fmt.Printf("  Total Functions:   %d\n", summary.TotalFunctions)
	fmt.Printf("  Total Called:      %d\n", summary.TotalCalled)
	fmt.Printf("  Average Coverage:  %.2f%%\n", summary.AverageCoverage)
	fmt.Println("==================================================")
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

	// Use summarizeCoverage for totals
	coverage := map[string]*CoverageData{image: data}
	summary := summarizeCoverage(coverage)

	summaryText := fmt.Sprintf(
		"Coverage Summary for %s | Total Functions: %d | Called Functions: %d | Uncalled Functions: %d | Coverage: %.2f%%\n"+
			"Totals: Total Functions: %d | Total Called: %d | Average Coverage: %.2f%%",
		safeName, totalCount, len(calledFns), skippedCount, float64(len(calledFns))/float64(totalCount)*100,
		summary.TotalFunctions, summary.TotalCalled, summary.AverageCoverage,
	)

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

	// Add totals section to details
	details.WriteString(fmt.Sprintf(
		"\nTOTALS:\n  Total Functions: %d\n  Total Called: %d\n  Average Coverage: %.2f%%\n",
		summary.TotalFunctions, summary.TotalCalled, summary.AverageCoverage,
	))

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
							Message: summaryText,
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
	Rows            []Row
	GeneratedAt     string
	TotalFunctions  int
	TotalCalled     int
	AverageCoverage float64
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
	summary := summarizeCoverage(coverage)

	// Convert CoverageSummary to Row for template compatibility
	rows := make([]Row, len(summary.Rows))
	for i, r := range summary.Rows {
		rows[i] = Row{
			ImageName:   filepath.Base(r.ImageName),
			TotalCount:  r.TotalCount,
			CalledCount: r.CalledCount,
			CoveragePct: r.CoveragePct,
		}
	}

	aggData := AggregateData{
		Rows:            rows,
		GeneratedAt:     time.Now().Format("2006-01-02 15:04:05 MST"),
		TotalFunctions:  summary.TotalFunctions,
		TotalCalled:     summary.TotalCalled,
		AverageCoverage: summary.AverageCoverage,
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

type CoverageSummary struct {
	ImageName   string
	TotalCount  int
	CalledCount int
	CoveragePct float64
}

type CoverageTotals struct {
	Rows            []CoverageSummary
	TotalFunctions  int
	TotalCalled     int
	AverageCoverage float64
}

// summarizeCoverage aggregates coverage data across all images and calculates totals.
// It returns a CoverageTotals struct containing the summary.
// Each row contains the image name, total functions, called functions, and coverage percentage.
// The coverage percentage is calculated as (called functions / total functions) * 100.
// The average coverage is calculated as (total called functions / total functions across all images) * 100.
// The function sorts the images alphabetically by name before summarizing.
func summarizeCoverage(coverage map[string]*CoverageData) CoverageTotals {
	imageNames := make([]string, 0, len(coverage))
	for image := range coverage {
		imageNames = append(imageNames, image)
	}
	sort.Strings(imageNames)

	rows := []CoverageSummary{}
	var totalFunctions, totalCalled int
	for _, image := range imageNames {
		data := coverage[image]
		total := len(data.TotalFunctions)
		called := len(data.CalledFunctions)
		coveragePct := 0.0
		if total > 0 {
			coveragePct = float64(called) / float64(total) * 100
		}
		rows = append(rows, CoverageSummary{
			ImageName:   image,
			TotalCount:  total,
			CalledCount: called,
			CoveragePct: coveragePct,
		})
		totalFunctions += total
		totalCalled += called
	}
	averageCoverage := 0.0
	if totalFunctions > 0 {
		averageCoverage = float64(totalCalled) / float64(totalFunctions) * 100
	}
	return CoverageTotals{
		Rows:            rows,
		TotalFunctions:  totalFunctions,
		TotalCalled:     totalCalled,
		AverageCoverage: averageCoverage,
	}
}
