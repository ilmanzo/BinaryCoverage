//go:build ignore

// gen_report_bench_corpus generates a synthetic _functions.log/_called.log
// corpus for benchmarking `funkoverage report`, simulating issue #123's
// scenario (report generation slows as the number of traced binaries and
// libraries grows) without needing to actually install/trace hundreds of
// real binaries. Deterministic: same args always produce byte-identical
// output, so before/after runs are directly comparable.
//
// Usage: go run tests/gen_report_bench_corpus.go <outdir> <numImages> <funcsPerImage> <calledFilesPerImage>
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: gen_report_bench_corpus <outdir> <numImages> <funcsPerImage> <calledFilesPerImage>")
		os.Exit(1)
	}
	outdir := os.Args[1]
	numImages := atoi(os.Args[2])
	funcsPerImage := atoi(os.Args[3])
	calledFiles := atoi(os.Args[4])

	if err := os.MkdirAll(outdir, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	calledPerFile := (funcsPerImage * 6 / 10) / calledFiles
	if calledPerFile == 0 {
		calledPerFile = 1
	}

	for i := 0; i < numImages; i++ {
		base := fmt.Sprintf("image%05d", i)
		image := fmt.Sprintf("/opt/bench/%s/lib%s.so", base, base)

		writeLines(filepath.Join(outdir, base+"_functions.log"), funcsPerImage, func(w *bufio.Writer, j int) {
			fmt.Fprintf(w, "FUNC %s func_%s_%d(int, char const*)\n", image, base, j)
		})

		for c := 0; c < calledFiles; c++ {
			start := c * calledPerFile
			path := filepath.Join(outdir, fmt.Sprintf("%s_run%d_called.log", base, c))
			writeRange(path, start, start+calledPerFile, func(w *bufio.Writer, j int) {
				fmt.Fprintf(w, "CALLED %s func_%s_%d(int, char const*)\n", image, base, j)
			})
		}
	}

	totalFunc := numImages * funcsPerImage
	totalCalled := numImages * calledPerFile * calledFiles
	fmt.Printf("generated %d images, %d functions.log lines, %d called.log lines across %d files\n",
		numImages, totalFunc, totalCalled, numImages*(1+calledFiles))
}

func writeLines(path string, n int, line func(w *bufio.Writer, i int)) {
	writeRange(path, 0, n, line)
}

func writeRange(path string, start, end int, line func(w *bufio.Writer, i int)) {
	f, err := os.Create(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "create:", err)
		os.Exit(1)
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 64*1024)
	for i := start; i < end; i++ {
		line(w, i)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "flush:", err)
		os.Exit(1)
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad number:", s)
		os.Exit(1)
	}
	return n
}
