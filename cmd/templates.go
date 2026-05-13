package main

import (
	_ "embed"
)

//go:embed templates/detailed.html
var detailedHTMLTemplateStr string

//go:embed templates/aggregate.html
var aggregateHTMLTemplate string

const helpText = `funkoverage - binary function coverage via eBPF

Usage:
  funkoverage setup
      Validate eBPF environment (kernel ≥6.6, BTF). Run once as root.

  funkoverage install [--no-libs] <binary...>
      Install shim for ELF binary (requires debug symbols).

  funkoverage uninstall <binary...>
      Restore original binary.

  funkoverage trace [--no-libs] <binary> [args...]
      Run binary under tracing without permanent installation.

  funkoverage enumerate [--no-libs] <binary>
      List all discoverable functions (debug utility).

  funkoverage report <inputdir|log1,log2> <outputdir> [--formats html,xml,txt]
      Generate coverage reports from log files.

  funkoverage version
  funkoverage help

Environment variables:
  FUNKOVERAGE_SHIM   Path to funkoverage-shim binary (default: same dir as funkoverage)
  LOG_DIR            Coverage log directory (default: /var/coverage/data)
  SAFE_BIN_DIR       Original binary store (default: /var/coverage/bin)

Quick start:
  sudo funkoverage setup          # validate eBPF environment (once)
  sudo funkoverage install /usr/bin/myapp
  myapp --run-tests               # traced automatically by the shim
  funkoverage report /var/coverage/data /tmp/report
  sudo funkoverage uninstall /usr/bin/myapp
`
