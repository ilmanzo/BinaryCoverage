# BinaryCoverage

**BinaryCoverage** is a code coverage tool built as a plugin for
[Intel Pin][pin], a dynamic binary instrumentation (DBI) framework.
It analyzes binary executables to measure and report code coverage.

[pin]: https://www.intel.com/content/www/us/en/developer/articles/tool/pin-a-dynamic-binary-instrumentation-tool.html

## ✅ Supported Platforms

- **GNU/Linux** (x86_64 only)


## 📦 Prerequisites

To build and run this tool, you'll need:

- **x86_64 CPU** (other architectures are not supported)
- `make`
- **Go language compiler**
- `g++` version **15** (or any c++ 2017 )
- **Catch2 v2** library (optional, only for running the C++ test suite)
  - A copy is provided in `tests/catch2/catch.hpp`


## 🛠️ Build & Run

### 🔧 Build

Clone the repository:

```bash
git clone git@github.com:ilmanzo/BinaryCoverage.git
cd BinaryCoverage/
```

Download and build the project:

```bash
./build.sh
```

`build.sh` will:
- Download and extract Intel Pin locally
- Build the coverage tool
- Compile and instrument the example C program in the `example/` directory

### ▶️ Run

Before building, export the PIN_ROOT environment variable:

```bash
export PIN_ROOT=../pin-external-4.0-99633-g5ca9893f2-gcc-linux
```

`PIN_ROOT` should point to the root directory where Intel Pin was extracted.
This is a common convention when building Pin tools.

To run the tool:

```bash
$PIN_ROOT/pin -t ./obj-intel64/FuncTracer.so -- <target_binary_path> <args...>
```

Replace ``<target_binary_path>`` and ``<args...>`` with your target program and
its arguments.

### 📎 Note on Debug Info

This tool relies on DWARF debugging information to determine line-level
coverage. Please compile your target programs with:

```bash
gcc -g -gdwarf-4 main.c
```

Pin 3+ supports DWARF4. Debug info is essential for accurate line mapping.

## 🧪 Running Unit Tests

To run the unit tests:

```bash
./run_unit_tests.sh
```

Uses Catch2 v2 (already included in the repo in `tests/catch2/catch.hpp`).

## 🖼️ Modifying the HTML Output

If you just want to modify the HTML report templates, you don't need to rebuild
everything.

HTML templates are located in `cmd/templates/`. You can modify them and
preview the results by displaying the HTML in a browser.

### 🔄 Rebuilding Just the Report Generator

If you change the analyzer logic or Go code:

```bash
cd cmd
go build
./cmd report ../example/sample_data /tmp
```

This generates example HTML reports (with dummy data) under `/tmp`.
