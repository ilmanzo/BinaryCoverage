package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// genBenchLogs writes a synthetic log corpus: numImages images, funcsPerImage
// functions each, ~60% called, split across callFilesPerImage separate
// _called.log files per image (simulating repeated shim invocations).
// Returns the list of generated log file paths.
func genBenchLogs(b *testing.B, dir string, numImages, funcsPerImage, callFilesPerImage int) []string {
	b.Helper()
	var files []string
	for i := range numImages {
		image := fmt.Sprintf("/opt/bench/image%04d/bin", i)
		base := fmt.Sprintf("image%04d", i)

		funcPath := filepath.Join(dir, base+"_functions.log")
		f, err := os.Create(funcPath)
		if err != nil {
			b.Fatal(err)
		}
		for j := range funcsPerImage {
			fmt.Fprintf(f, "FUNC %s func_%s_%d(int, char const*)\n", image, base, j)
		}
		f.Close()
		files = append(files, funcPath)

		called := (funcsPerImage * 6) / 10
		perFile := called / callFilesPerImage
		if perFile == 0 {
			perFile = 1
		}
		for c := range callFilesPerImage {
			calledPath := filepath.Join(dir, base+"_run"+strconv.Itoa(c)+"_called.log")
			cf, err := os.Create(calledPath)
			if err != nil {
				b.Fatal(err)
			}
			start := c * perFile
			end := min(start+perFile, funcsPerImage)
			for j := start; j < end; j++ {
				fmt.Fprintf(cf, "CALLED %s func_%s_%d(int, char const*)\n", image, base, j)
			}
			cf.Close()
			files = append(files, calledPath)
		}
	}
	return files
}

func BenchmarkScanLog(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 1, 20000, 1)
	b.ResetTimer()
	for b.Loop() {
		coverage := make(map[string]*CoverageData)
		if err := scanLog(files[0], logTypeFunctions, coverage); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeLogs(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 50, 500, 4)
	b.ResetTimer()
	for b.Loop() {
		if _, err := analyzeLogs(files); err != nil {
			b.Fatal(err)
		}
	}
}

// generateHTMLReport/generateXUnitReport are called once per image by
// emitReport, so the benchmark needs many images (the axis bottleneck B's
// per-call template re-parse actually costs on) rather than few images
// with huge function counts.
func BenchmarkGenerateHTMLReport(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 200, 50, 2)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	outDir := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		// buildReportSet is inside the loop on purpose: it does the
		// called/uncalled split that generateHTMLReport used to do itself,
		// so the benchmark keeps measuring the same total work.
		for _, rep := range buildReportSet(coverage).Images {
			if err := generateHTMLReport(rep, outDir); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkGenerateXUnitReport(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 200, 50, 2)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	outDir := b.TempDir()
	b.ResetTimer()
	for b.Loop() {
		for _, rep := range buildReportSet(coverage).Images {
			if err := generateXUnitReport(rep, outDir); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkEmitReportHTML/XML go through emitReport itself (not the
// generate*Report functions directly), since that's where bottleneck C's
// per-image concurrency actually lives.
func BenchmarkEmitReportHTML(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 200, 50, 2)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		outDir := b.TempDir()
		emitReport("html", buildReportSet(coverage), outDir)
	}
}

func BenchmarkEmitReportXML(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 200, 50, 2)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		outDir := b.TempDir()
		emitReport("xml", buildReportSet(coverage), outDir)
	}
}

// BenchmarkEmitReportMultiFormat is the default `report` invocation: more than
// one format off a single coverage map. That is the case the shared reportSet
// exists for — the single-format benchmarks above cannot show it, because with
// one format there is nothing to share.
func BenchmarkEmitReportMultiFormat(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 200, 50, 2)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		outDir := b.TempDir()
		set := buildReportSet(coverage)
		emitReport("html", set, outDir)
		emitReport("xml", set, outDir)
	}
}

func BenchmarkEnumerateFunctions(b *testing.B) {
	// Enumerating real libraries requires a real ELF + ldd; skip if the
	// bench sample binary isn't available in this environment.
	sample := filepath.Join("..", "tests", "sample", "sample")
	if _, err := os.Stat(sample); err != nil {
		b.Skip("tests/sample/sample not built")
	}
	b.ResetTimer()
	for b.Loop() {
		if _, _, err := EnumerateFunctions(sample, WithLibraries, nil); err != nil {
			b.Fatal(err)
		}
	}
}
