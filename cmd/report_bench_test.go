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
	for i := 0; i < numImages; i++ {
		image := fmt.Sprintf("/opt/bench/image%04d/bin", i)
		base := fmt.Sprintf("image%04d", i)

		funcPath := filepath.Join(dir, base+"_functions.log")
		f, err := os.Create(funcPath)
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < funcsPerImage; j++ {
			fmt.Fprintf(f, "FUNC %s func_%s_%d(int, char const*)\n", image, base, j)
		}
		f.Close()
		files = append(files, funcPath)

		called := (funcsPerImage * 6) / 10
		perFile := called / callFilesPerImage
		if perFile == 0 {
			perFile = 1
		}
		for c := 0; c < callFilesPerImage; c++ {
			calledPath := filepath.Join(dir, base+"_run"+strconv.Itoa(c)+"_called.log")
			cf, err := os.Create(calledPath)
			if err != nil {
				b.Fatal(err)
			}
			start := c * perFile
			end := start + perFile
			if end > funcsPerImage {
				end = funcsPerImage
			}
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
	for i := 0; i < b.N; i++ {
		coverage := make(map[string]*CoverageData)
		if err := scanLog(files[0], "functions", coverage); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyzeLogs(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 50, 500, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzeLogs(files); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGenerateHTMLReport(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 1, 5000, 4)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	outDir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for image, data := range coverage {
			if err := generateHTMLReport(image, data, outDir); err != nil {
				b.Fatal(err)
			}
		}
	}
}

func BenchmarkGenerateXUnitReport(b *testing.B) {
	dir := b.TempDir()
	files := genBenchLogs(b, dir, 1, 5000, 4)
	coverage, err := analyzeLogs(files)
	if err != nil {
		b.Fatal(err)
	}
	outDir := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for image, data := range coverage {
			if err := generateXUnitReport(image, data, outDir); err != nil {
				b.Fatal(err)
			}
		}
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
	for i := 0; i < b.N; i++ {
		if _, err := EnumerateFunctions(sample, false, nil); err != nil {
			b.Fatal(err)
		}
	}
}
