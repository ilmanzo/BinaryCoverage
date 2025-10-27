package main

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed templates/detailed.html
var detailedHTMLTemplateStr string

//go:embed templates/aggregate.html
var aggregateHTMLTemplate string

const wrapHelpText = `Usage: funkoverage wrap /path/to/binary
Wrap the given ELF binary with the Pin coverage wrapper.`

const unwrapHelpText = `Usage: funkoverage unwrap /path/to/binary
Restore the original binary previously wrapped.`

const reportHelpText = `Usage: funkoverage report <inputdir|log1.txt,log2.txt> <outputdir> [--formats <formats>]

Generate coverage reports from log files.
  <inputdir>         Directory containing .log files (all will be used)
  log1.txt,log2.txt  Comma-separated list of log files
  <outputdir>        Output directory for reports (mandatory)
  --formats          Comma-separated list: html,xml,txt (default: html,txt,xml)
`

var helpText string

func init() {
	// We use fmt.Sprintf to build the main help text from the subcommand help texts
	// to avoid duplication. The subcommand help texts are modified slightly for
	// proper formatting in the main help view.
	helpText = fmt.Sprintf(`Usage:
  %s
  %s
  %s
  help
      Show this help message.
  version
      Show program version.

Environment variables:
  PIN_ROOT            Path to Intel Pin root directory (default: autodetect or required)
  PIN_TOOL_SEARCH_DIR Directory to search for FuncTracer.so (default: /usr/lib64/coverage-tools)
  LOG_DIR             Directory for coverage logs (default: /var/coverage/data)
  SAFE_BIN_DIR        Directory to store original binaries (default: /var/coverage/bin)
`,
		indent(strings.TrimPrefix(wrapHelpText, "Usage: funkoverage "), "  "),
		indent(strings.TrimPrefix(unwrapHelpText, "Usage: funkoverage "), "  "),
		indent(strings.TrimPrefix(reportHelpText, "Usage: funkoverage "), "  "))
}

// indent adds indentation to each line of a string.
func indent(text, indentation string) string {
	return indentation + strings.ReplaceAll(strings.TrimSpace(text), "\n", "\n"+indentation)
}
