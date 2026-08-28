package main

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"html/template"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
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
	GeneratedAt        string
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

// writeBuffered creates path and passes a buffered writer to write, flushing
// before close. Avoids the small-chunk syscalls that html/template.Execute
// and xml.Encoder otherwise issue directly against the raw *os.File.
func writeBuffered(path string, write func(w io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := bufio.NewWriter(f)
	if err := write(bw); err != nil {
		return err
	}
	return bw.Flush()
}

// safeImageName returns a filesystem-safe slug from an image path.
func safeImageName(image string) string {
	return safeNameRe.ReplaceAllString(filepath.Base(image), "_")
}

// pctOf returns num/den as a percentage, or 0 if den is 0.
func pctOf(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den) * 100
}

// logType identifies which log file a line came from — used everywhere
// coverage/report code needs to distinguish _functions.log from
// _called.log, instead of ad-hoc bools or magic strings.
type logType int

const (
	logTypeUnknown logType = iota
	logTypeFunctions
	logTypeCalled
)

// detectLogType classifies path by filename suffix; logTypeUnknown for
// anything else.
func detectLogType(path string) logType {
	base := filepath.Base(path)
	switch {
	case strings.HasSuffix(base, "_functions.log"):
		return logTypeFunctions
	case strings.HasSuffix(base, "_called.log"):
		return logTypeCalled
	default:
		return logTypeUnknown
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

// imageReport is one image's sorted called/uncalled split. len(Called)+
// len(Uncalled) == len(data.TotalFunctions), since the split partitions
// exactly that set.
type imageReport struct {
	Image            string
	Called, Uncalled []string
}

// reportSet is everything the output formats need, derived from the coverage
// map once per run. Images[i] describes Totals.Rows[i] — both are ordered by
// image name.
type reportSet struct {
	Images []imageReport
	Totals CoverageTotals
}

// buildReportSet computes the split and the totals once. Each format used to
// redo them: splitCalledUncalled once per image per format (two allocations
// and two sorts each), summarizeCoverage once for txt and again for the
// aggregate HTML page.
func buildReportSet(coverage map[string]*CoverageData) reportSet {
	totals := summarizeCoverage(coverage)
	images := make([]imageReport, len(totals.Rows))
	g := new(errgroup.Group)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, row := range totals.Rows {
		g.Go(func() error {
			called, uncalled := splitCalledUncalled(coverage[row.ImageName])
			images[i] = imageReport{Image: row.ImageName, Called: called, Uncalled: uncalled}
			return nil
		})
	}
	_ = g.Wait()
	return reportSet{Images: images, Totals: totals}
}

// analyzeLogs processes _functions.log and _called.log files concurrently.
// Files with unrecognized suffixes are skipped with a warning. Each file is
// scanned into its own local map (scanLog mutates a map in place, and Go
// maps aren't safe for concurrent writes from multiple files touching the
// same image); results are merged sequentially once every scan completes.
func analyzeLogs(logFiles []string) (map[string]*CoverageData, error) {
	locals := make([]map[string]*CoverageData, len(logFiles))

	g := new(errgroup.Group)
	g.SetLimit(runtime.GOMAXPROCS(0))
	for i, logFile := range logFiles {
		g.Go(func() error {
			lt := detectLogType(logFile)
			if lt == logTypeUnknown {
				fmt.Fprintf(os.Stderr, "report: skipping unrecognized log file: %s\n", logFile)
				return nil
			}
			local := make(map[string]*CoverageData)
			if err := scanLog(logFile, lt, local); err != nil {
				return err
			}
			locals[i] = local
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}

	coverage := make(map[string]*CoverageData)
	for _, local := range locals {
		mergeCoverage(coverage, local)
	}
	return coverage, nil
}

// mergeCoverage unions src's per-image function sets into dst.
func mergeCoverage(dst, src map[string]*CoverageData) {
	for image, data := range src {
		ensureCoverage(dst, image)
		maps.Copy(dst[image].TotalFunctions, data.TotalFunctions)
		maps.Copy(dst[image].CalledFunctions, data.CalledFunctions)
	}
}

// scanLog reads a single log file and updates coverage in place.
// Defer-cleanup fires per call, not per analyzeLogs invocation.
func scanLog(logFile string, lt logType, coverage map[string]*CoverageData) error {
	f, err := os.Open(logFile)
	if err != nil {
		return fmt.Errorf("could not open log file %s: %w", logFile, err)
	}
	defer f.Close()

	prefix := funcPrefix
	if lt == logTypeCalled {
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
		if lt == logTypeCalled {
			coverage[image].CalledFunctions[function] = struct{}{}
		} else {
			coverage[image].TotalFunctions[function] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading log file %s: %w", logFile, err)
	}
	return nil
}

// --- Console Report ---
// printTxtReport prints a text-based report to the console summarizing coverage for each image.
func printTxtReport(set reportSet) {
	summary := set.Totals
	for i, row := range summary.Rows {
		called, uncalled := set.Images[i].Called, set.Images[i].Uncalled
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
func generateXUnitReport(rep imageReport, outputDir string) error {
	calledList, uncalledList := rep.Called, rep.Uncalled
	totalCount := len(calledList) + len(uncalledList)
	skippedCount := len(uncalledList)
	safeName := safeImageName(rep.Image)
	outfile := filepath.Join(outputDir, fmt.Sprintf("coverage_%s.xml", safeName))

	calledCount := len(calledList)
	pct := pctOf(calledCount, totalCount)
	// Totals here are a single-image report, so they're identical to the
	// per-image numbers above (was previously recomputed via a throwaway
	// single-entry map + summarizeCoverage call).
	summaryText := fmt.Sprintf(
		"Coverage Summary for %s | Total Functions: %d | Called Functions: %d | Uncalled Functions: %d | Coverage: %.2f%%\n"+
			"Totals: Total Functions: %d | Total Called: %d | Average Coverage: %.2f%%",
		safeName, totalCount, calledCount, skippedCount, pct,
		totalCount, calledCount, pct,
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
		totalCount, calledCount, pct,
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
	return writeBuffered(outfile, func(w io.Writer) error {
		enc := xml.NewEncoder(w)
		enc.Indent("", "  ")
		return enc.Encode(ts)
	})
}

// AggregateData carries CoverageTotals plus the timestamp for HTML rendering.
type AggregateData struct {
	CoverageTotals
	GeneratedAt string
}

// generateHTMLReport generates an HTML report for a single image's coverage data.
func generateHTMLReport(rep imageReport, outputDir string) error {
	called, uncalled := rep.Called, rep.Uncalled
	totalCount := len(called) + len(uncalled)
	coveragePct := pctOf(len(called), totalCount)
	functions := make([]FunctionEntry, 0, totalCount)
	for _, fn := range called {
		functions = append(functions, FunctionEntry{Name: fn, Status: "called"})
	}
	for _, fn := range uncalled {
		functions = append(functions, FunctionEntry{Name: fn, Status: "uncalled"})
	}
	reportData := HTMLReportData{
		ImageName:          filepath.Base(rep.Image),
		TotalCount:         totalCount,
		CalledCount:        len(called),
		UncalledCount:      len(uncalled),
		CoveragePercentage: coveragePct,
		Functions:          functions,
		GeneratedAt:        time.Now().Format("2006-01-02 15:04:05 MST"),
	}
	outfile := filepath.Join(outputDir, fmt.Sprintf("%s.html", safeImageName(rep.Image)))
	return writeBuffered(outfile, func(w io.Writer) error {
		return parsedDetailedTemplate.Execute(w, reportData)
	})
}

// generateAggregateHTMLReport generates an HTML report summarizing coverage across all images.
// It creates a table with the image name, total functions, called functions, and coverage percentage.
func generateAggregateHTMLReport(summary CoverageTotals, outputDir string) error {
	// Rows are cloned: summary is now shared with the txt and xml formats,
	// which want the full paths this loop shortens.
	summary.Rows = slices.Clone(summary.Rows)
	for i := range summary.Rows {
		summary.Rows[i].ImageName = filepath.Base(summary.Rows[i].ImageName)
	}
	aggData := AggregateData{
		CoverageTotals: summary,
		GeneratedAt:    time.Now().Format("2006-01-02 15:04:05 MST"),
	}

	outfile := filepath.Join(outputDir, "aggregate.html")
	return writeBuffered(outfile, func(w io.Writer) error {
		return parsedAggregateTemplate.Execute(w, aggData)
	})
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
		coveragePct := pctOf(called, total)
		rows = append(rows, CoverageSummary{
			ImageName:   image,
			TotalCount:  total,
			CalledCount: called,
			CoveragePct: coveragePct,
		})
		totalFunctions += total
		totalCalled += called
	}
	averageCoverage := pctOf(totalCalled, totalFunctions)
	return CoverageTotals{
		Rows:            rows,
		TotalFunctions:  totalFunctions,
		TotalCalled:     totalCalled,
		AverageCoverage: averageCoverage,
	}
}
