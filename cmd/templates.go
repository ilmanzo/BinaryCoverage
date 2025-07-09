package main

const detailedHTMLTemplateStr = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Coverage Report for {{.ImageName}}</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 2em; background: #f9f9f9; color: #333; }
        .container { max-width: 900px; margin: auto; background: #fff; padding: 2em; border-radius: 8px; box-shadow: 0 4px 8px rgba(0,0,0,0.1);}
        .summary { background: #f4f4f4; padding: 1.5em; border-radius: 8px; margin-bottom: 2em; border: 1px solid #ddd;}
        .summary .percentage { font-size: 1.8em; font-weight: bold; color: #0c322c;}
        .progress-bar { background: #e9ecef; border-radius: 50px; overflow: hidden; height: 30px; margin-top: 1em;}
        .progress-bar-inner { background: #30ba78; height: 100%; color: white, text-align: center; line-height: 30px; font-weight: bold; transition: width 0.5s;}
        .function-list { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 1em; list-style-type: none; padding: 0;}
        .function-list li { padding: 0.6em; border-radius: 5px; font-family: monospace; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; transition: transform 0.2s;}
        .function-list li:hover { transform: translateY(-2px); box-shadow: 0 2px 4px rgba(0,0,0,0.08);}
        .called { background: #d4edda; color: #155724; border-left: 5px solid #28a745;}
        .uncalled { background: #f8d7da; color: #721c24; border-left: 5px solid #dc3545;}
    </style>
</head>
<body>
<div class="container">
    <h1>Coverage Report</h1>
    <h2>Image: {{.ImageName}}</h2>
    <div class="summary">
        <p><strong>Total Functions:</strong> {{.TotalCount}}</p>
        <p><strong>Called Functions:</strong> {{.CalledCount}}</p>
        <p><strong>Uncalled Functions:</strong> {{.UncalledCount}}</p>
        <p class="percentage">Coverage: {{printf "%.1f" .CoveragePercentage}}%</p>
        <div class="progress-bar">
            <div class="progress-bar-inner" style="width: {{.CoveragePercentage}}%">{{printf "%.2f" .CoveragePercentage}}%</div>
        </div>
    </div>
    <details>
    <summary><h2>Function Details</h2></summary>
    <p><strong>Legend: </strong><span class="called"> Called Function </span><span class="uncalled"> Uncalled Function </span></p>
    <ul class="function-list">
        {{range .Functions}}
        <li class="{{.Status}}" title="{{.Name}}">{{.Name}}</li>
        {{end}}
    </ul>
    </details>
</div>
</body>
</html>`

// aggregateHTMLTemplate_bars is a variant of the aggregate report with bar charts instead of pie charts.
// It uses a simple horizontal bar to represent coverage percentage.
const aggregateHTMLTemplate = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>Aggregate Coverage Report</title>
    <style>
        body { font-family: Arial, sans-serif; margin: 2em; background: #f9f9f9; color: #333; }
        .container { max-width: 900px; margin: auto; background: #fff; padding: 2em; border-radius: 8px; box-shadow: 0 4px 8px rgba(0,0,0,0.1);}
        table { width: 100%; border-collapse: collapse; margin-top: 2em;}
        th, td { padding: 0.7em 1em; border-bottom: 1px solid #ddd; text-align: left;}
        th { background: #f4f4f4; cursor: pointer; user-select: none; }
        tr:hover { background: #f1f7ff; }
        .bar { height: 18px; background: #efefef; border-radius: 9px; overflow: hidden; }
        .bar-inner {
            background: #30ba78;
            height: 100%;
            color: #0c322c; 
            text-align: center;
            font-size: 0.9em;
            font-weight: bold;
            line-height: 18px;
        }
        tr:nth-child(even) { background-color: #f9f9f9; }
        th.sort-asc::after { content: " ▲"; }
        th.sort-desc::after { content: " ▼"; }
    </style>
</head>
<body>
<div class="container">
    <h1>Aggregate Coverage Report</h1>
    <p><em>Generated at: {{.GeneratedAt}}</em></p>
    <div class="summary">
        <h2>Total Coverage</h2>
        <ul>
            <li><strong>Total Functions:</strong> {{.TotalFunctions}}</li>
            <li><strong>Total Executed:</strong> {{.TotalCalled}}</li>
            <li><strong>Average Coverage:</strong> {{printf "%.2f" .AverageCoverage}}%</li>
        </ul>
    </div>    
    <table>
        <thead>
            <tr>
                <th>Image</th>
                <th>Total Functions</th>
                <th>Called Functions</th>
                <th>Coverage</th>
            </tr>
        </thead>
        <tbody>
            {{range .Rows}}
            <tr>
                <td>{{.ImageName}}</td>
                <td>{{.TotalCount}}</td>
                <td>{{.CalledCount}}</td>
                <td>
                    <div class="bar">
                        <div class="bar-inner" style="width: {{printf "%.1f" .CoveragePct}}%">
                            {{printf "%.1f" .CoveragePct}}%
                        </div>
                    </div>
                </td>
            </tr>
            {{end}}
        </tbody>
    </table>
</div>
<script>
document.addEventListener('DOMContentLoaded', () => {
    const getCellValue = (row, idx) => {
        const cell = row.children[idx];
        if (!cell) return '';
        const val = cell.innerText.trim();
        if (val.includes('%')) return parseFloat(val.replace('%', ''));
        return isNaN(val) ? val : parseFloat(val);
    };

    const comparer = (idx, asc) => (a, b) => {
        const v1 = getCellValue(a, idx);
        const v2 = getCellValue(b, idx);
        if (typeof v1 === 'number' && typeof v2 === 'number') {
            return (v1 - v2) * (asc ? 1 : -1);
        }
        return String(v1).localeCompare(String(v2)) * (asc ? 1 : -1);
    };

    const table = document.querySelector("table");
    const tbody = table.querySelector("tbody");
    const headers = table.querySelectorAll("th");

    headers.forEach((th, idx) => {
        th.addEventListener("click", () => {
            const isAsc = !th.classList.contains("sort-asc");

            headers.forEach(header => header.classList.remove("sort-asc", "sort-desc"));
            th.classList.add(isAsc ? "sort-asc" : "sort-desc");

            const rows = Array.from(tbody.querySelectorAll("tr"));
            rows.sort(comparer(idx, isAsc)).forEach(row => tbody.appendChild(row));
        });
    });

    // Default sort: Image Name ascending
    const defaultSortIdx = 0; // 1st column: Name
    const defaultHeader = headers[defaultSortIdx];
    defaultHeader.classList.add("sort-asc");
    const rows = Array.from(tbody.querySelectorAll("tr"));
    rows.sort(comparer(defaultSortIdx, true)).forEach(row => tbody.appendChild(row));
});
</script>
</body>
</html>`

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
