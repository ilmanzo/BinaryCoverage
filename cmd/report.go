package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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

var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

var (
	funcPrefix   = []byte("FUNC ")
	calledPrefix = []byte("CALLED ")
)

// Parsed once at package init instead of on every report/image — the
// template text is a compile-time constant, so re-parsing per call was
// pure repeated work.
var (
	parsedDetailedTemplate  = template.Must(template.New("report").Parse(detailedHTMLTemplateStr))
	parsedAggregateTemplate = template.Must(template.New("aggregate").Parse(aggregateHTMLTemplate))
)

// safeImageName returns a filesystem-safe slug from an image path.
func safeImageName(image string) string {
	return safeNameRe.ReplaceAllString(filepath.Base(image), "_")
}

// detectLogType returns "functions" or "called" based on filename suffix.
func detectLogType(path string) string {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_functions.log"):
		return "functions"
	case strings.HasSuffix(base, "_called.log"):
		return "called"
	default:
		return ""
	}
}

func ensureCoverage(coverage map[string]*CoverageData, image string) {
	if _, ok := coverage[image]; !ok {
		coverage[image] = &CoverageData{
			TotalFunctions:  make(map[string]struct{}),
			CalledFunctions: make(map[string]struct{}),
		}
	}
}

// splitCalledUncalled returns sorted slices of called and uncalled function
// names. A function is "called" if it appears in CalledFunctions; everything
// else in TotalFunctions is "uncalled".
func splitCalledUncalled(data *CoverageData) (called, uncalled []string) {
	called = make([]string, 0, len(data.CalledFunctions))
	uncalled = make([]string, 0, len(data.TotalFunctions))
	for fn := range data.TotalFunctions {
		if _, ok := data.CalledFunctions[fn]; ok {
			called = append(called, fn)
		} else {
			uncalled = append(uncalled, fn)
		}
	}
	slices.Sort(called)
	slices.Sort(uncalled)
	return called, uncalled
}

// analyzeLogs processes _functions.log and _called.log files.
// Files with unrecognized suffixes are skipped with a warning.
func analyzeLogs(logFiles []string) (map[string]*CoverageData, error) {
	coverage := make(map[string]*CoverageData)
	for _, logFile := range logFiles {
		logType := detectLogType(logFile)
		if logType == "" {
			fmt.Fprintf(os.Stderr, "report: skipping unrecognized log file: %s\n", logFile)
			continue
		}
		if err := scanLog(logFile, logType, coverage); err != nil {
			return nil, err
		}
	}
	return coverage, nil
}

// scanLog reads a single log file and updates coverage in place.
// Defer-cleanup fires per call, not per analyzeLogs invocation.
func scanLog(logFile, logType string, coverage map[string]*CoverageData) error {
	f, err := os.Open(logFile)
	if err != nil {
		return fmt.Errorf("could not open log file %s: %w", logFile, err)
	}
	defer f.Close()

	prefix := funcPrefix
	if logType == "called" {
		prefix = calledPrefix
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		if !bytes.HasPrefix(line, prefix) {
			continue
		}
		rest := line[len(prefix):]
		sep := bytes.IndexAny(rest, " \t")
		if sep == -1 {
			continue
		}
		imageBytes := rest[:sep]
		funcBytes := bytes.TrimSpace(rest[sep:])
		if len(imageBytes) == 0 || len(funcBytes) == 0 {
			continue
		}
		image, function := string(imageBytes), string(funcBytes)
		ensureCoverage(coverage, image)
		if logType == "functions" {
			coverage[image].TotalFunctions[function] = struct{}{}
		} else {
			coverage[image].CalledFunctions[function] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading log file %s: %w", logFile, err)
	}
	return nil
}

// --- Console Report ---
// printTxtReport prints a text-based report to the console summarizing coverage for each image.
func printTxtReport(coverage map[string]*CoverageData) {
	summary := summarizeCoverage(coverage)
	for _, row := range summary.Rows {
		data := coverage[row.ImageName]
		called, uncalled := splitCalledUncalled(data)
		fmt.Printf("\n==================================================\n")
		fmt.Printf("Image: %s\n", row.ImageName)
		fmt.Printf("==================================================\n")
		fmt.Printf("  Functions Found:   %d\n", row.TotalCount)
		fmt.Printf("  Functions Called:  %d\n", row.CalledCount)
		fmt.Printf("  Coverage:          %.2f%%\n", row.CoveragePct)
		fmt.Printf("--------------------------------------------------\n")
		if len(called) > 0 {
			fmt.Println("  Called Functions:")
			for _, fn := range called {
				fmt.Printf("    - %s\n", fn)
			}
		} else {
			fmt.Println("  No functions were called for this image.")
		}
		if len(uncalled) > 0 {
			fmt.Println("\n  Uncalled Functions:")
			for _, fn := range uncalled {
				fmt.Printf("    - %s\n", fn)
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
	calledList, uncalledList := splitCalledUncalled(data)
	totalCount := len(data.TotalFunctions)
	skippedCount := len(uncalledList)
	safeName := safeImageName(image)
	outfile := filepath.Join(outputDir, fmt.Sprintf("coverage_%s.xml", safeName))

	// Use summarizeCoverage for totals
	coverage := map[string]*CoverageData{image: data}
	summary := summarizeCoverage(coverage)

	calledCount := len(calledList)
	pct := 0.0
	if totalCount > 0 {
		pct = float64(calledCount) / float64(totalCount) * 100
	}
	summaryText := fmt.Sprintf(
		"Coverage Summary for %s | Total Functions: %d | Called Functions: %d | Uncalled Functions: %d | Coverage: %.2f%%\n"+
			"Totals: Total Functions: %d | Total Called: %d | Average Coverage: %.2f%%",
		safeName, totalCount, calledCount, skippedCount, pct,
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

// AggregateData carries CoverageTotals plus the timestamp for HTML rendering.
type AggregateData struct {
	CoverageTotals
	GeneratedAt string
}

// generateHTMLReport generates an HTML report for a single image's coverage data.
func generateHTMLReport(image string, data *CoverageData, outputDir string) error {
	called, uncalled := splitCalledUncalled(data)
	totalCount := len(called) + len(uncalled)
	coveragePct := 0.0
	if totalCount > 0 {
		coveragePct = float64(len(called)) / float64(totalCount) * 100
	}
	functions := make([]FunctionEntry, 0, totalCount)
	for _, fn := range called {
		functions = append(functions, FunctionEntry{Name: fn, Status: "called"})
	}
	for _, fn := range uncalled {
		functions = append(functions, FunctionEntry{Name: fn, Status: "uncalled"})
	}
	reportData := HTMLReportData{
		ImageName:          filepath.Base(image),
		TotalCount:         totalCount,
		CalledCount:        len(called),
		UncalledCount:      len(uncalled),
		CoveragePercentage: coveragePct,
		Functions:          functions,
		GeneratedAt:        time.Now().Format("2006-01-02 15:04:05 MST"),
	}
	outfile := filepath.Join(outputDir, fmt.Sprintf("%s.html", safeImageName(image)))
	f, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	return parsedDetailedTemplate.Execute(f, reportData)
}

// generateAggregateHTMLReport generates an HTML report summarizing coverage across all images.
// It creates a table with the image name, total functions, called functions, and coverage percentage.
func generateAggregateHTMLReport(coverage map[string]*CoverageData, outputDir string) error {
	summary := summarizeCoverage(coverage)
	for i := range summary.Rows {
		summary.Rows[i].ImageName = filepath.Base(summary.Rows[i].ImageName)
	}
	aggData := AggregateData{
		CoverageTotals: summary,
		GeneratedAt:    time.Now().Format("2006-01-02 15:04:05 MST"),
	}

	outfile := filepath.Join(outputDir, "aggregate.html")
	f, err := os.Create(outfile)
	if err != nil {
		return err
	}
	defer f.Close()
	return parsedAggregateTemplate.Execute(f, aggData)
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
	imageNames := slices.Sorted(maps.Keys(coverage))

	rows := make([]CoverageSummary, 0, len(imageNames))
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
