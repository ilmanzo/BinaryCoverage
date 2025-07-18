package main

import _ "embed"

//go:embed templates/detailed.html
var detailedHTMLTemplateStr string

//go:embed templates/aggregate.html
var aggregateHTMLTemplate string

const helpText = `Usage:
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
  SAFE_BIN_DIR        Directory to store original binaries (default: /var/coverage/bin)`
