#!/usr/bin/env python3
import os
import shutil
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

# --- Configuration ---
SCRIPT_DIR = Path(__file__).resolve().parent
PROJECT_ROOT = SCRIPT_DIR.parent.parent
FUNKOVERAGE = PROJECT_ROOT / "funkoverage"
FUNC_TRACER = PROJECT_ROOT / "obj-intel64" / "FuncTracer.so"
ENV_FILE = PROJECT_ROOT / "env"

SOURCE_CODE = r"""
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int sum(int a, int b) { return a + b; }
int sub(int a, int b) { return a - b; }
int mult(int a, int b) { return a * b; }
int div_op(int a, int b) { return a / b; }

int main(int argc, char *argv[]) {
    if (argc < 4) {
        printf("Usage: %s <num1> <num2> <op>\n", argv[0]);
        return 1;
    }
    int a = atoi(argv[1]);
    int b = atoi(argv[2]);
    char *op = argv[3];

    if (strcmp(op, "+") == 0) printf("%d\n", sum(a, b));
    else if (strcmp(op, "-") == 0) printf("%d\n", sub(a, b));
    else if (strcmp(op, "*") == 0) printf("%d\n", mult(a, b));
    else if (strcmp(op, "/") == 0) printf("%d\n", div_op(a, b));
    else printf("Unknown op\n");
    return 0;
}
"""

class TestCalcE2E(unittest.TestCase):
    def setUp(self):
        print(f"\n[SETUP] Starting E2E test in temporary env...")
        self._check_prerequisites()

        self.test_dir = Path(tempfile.mkdtemp(prefix="binarycoverage_e2e_"))
        self.bin_name = "calc"
        self.src_file = self.test_dir / f"{self.bin_name}.c"
        self.bin_path = self.test_dir / self.bin_name
        self.log_dir = self.test_dir / "logs"
        self.report_dir = self.test_dir / "report"
        self.xml_report = self.report_dir / f"coverage_{self.bin_name}.xml"

        self.env = self._prepare_env()
        self.src_file.write_text(SOURCE_CODE)
        print(f"[SETUP] Created test directory: {self.test_dir}")

    def tearDown(self):
        if self.test_dir.exists():
            shutil.rmtree(self.test_dir)
        print(f"[TEARDOWN] Cleaned up {self.test_dir}")

    def _check_prerequisites(self):
        if not FUNKOVERAGE.exists():
            self.fail(f"[FAIL] Tool not found: {FUNKOVERAGE}. Run build.sh first.")
        if not FUNC_TRACER.exists():
            self.fail(f"[FAIL] Tracer not found: {FUNC_TRACER}. Run build.sh first.")

    def _prepare_env(self):
        env = os.environ.copy()
        if ENV_FILE.exists():
            print(f"[ENV] Loading configuration from {ENV_FILE}")
            with open(ENV_FILE, "r") as f:
                for line in f:
                    if line.strip().startswith("export PIN_ROOT="):
                        val = line.split("=", 1)[1].strip().strip('"\'')
                        env["PIN_ROOT"] = val

        env.update({
            "PIN_TOOL_SEARCH_DIR": str(FUNC_TRACER.parent),
            "LOG_DIR": str(self.log_dir),
            "SAFE_BIN_DIR": str(self.test_dir / "safe_bin"),
        })
        return env

    def run_cmd(self, cmd, msg=None):
        if msg:
            print(f"[INFO] {msg}")

        # Convert all args to string to allow Path objects
        cmd_str = [str(c) for c in cmd]
        print(f"   Running: {' '.join(cmd_str)}")

        result = subprocess.run(
            cmd_str,
            cwd=self.test_dir,
            env=self.env,
            capture_output=True,
            text=True
        )

        if result.returncode != 0:
            print(f"\n[FAIL] COMMAND FAILED: {' '.join(cmd_str)}")
            print(f"   STDOUT:\n{result.stdout}")
            print(f"   STDERR:\n{result.stderr}")
            self.fail(f"Command failed with return code {result.returncode}")

        return result

    def get_coverage_status(self):
        """Parses the XML report and returns a dictionary of function statuses."""
        if not self.xml_report.exists():
            self.fail(f"[FAIL] Report missing: {self.xml_report}")

        try:
            tree = ET.parse(self.xml_report)
            # Find the text content inside <passed> tag which contains the ✓/✗ list
            passed_node = tree.find(".//testcase/passed")
            if passed_node is None or not passed_node.text:
                self.fail("[FAIL] Invalid XML report format: <passed> node missing or empty")

            status_map = {}
            for line in passed_node.text.splitlines():
                line = line.strip()
                if line.startswith("✓"):
                    status_map[line[1:].strip()] = "called"
                elif line.startswith("✗"):
                    status_map[line[1:].strip()] = "uncalled"
            return status_map
        except ET.ParseError as e:
            self.fail(f"[FAIL] Failed to parse XML report: {e}")

    def test_workflow(self):
        """
        Executes the full E2E workflow:
        1. Compile target
        2. Wrap binary
        3. Run 'sum' -> check coverage (sum called, sub uncalled)
        4. Run 'sub' -> check coverage (sub called)
        5. Unwrap binary
        """

        # --- Step 1: Compile ---
        self.run_cmd(
            ["gcc", "-g", "-gdwarf-4", self.src_file, "-o", self.bin_path],
            msg="Compiling target binary..."
        )

        # --- Step 2: Wrap ---
        self.run_cmd([FUNKOVERAGE, "wrap", self.bin_path], msg="Wrapping binary...")

        # Verify wrapper content
        content = self.bin_path.read_text(errors="ignore")
        self.assertIn("Pin Wrapper generated by Go tool funkoverage", content,
                      "[FAIL] Binary was not correctly wrapped (wrapper signature missing)")
        print("   [OK] Wrapper signature verified.")

        # --- Step 3: Run Case 1 (Sum) ---
        self.run_cmd([self.bin_path, "10", "20", "+"], msg="Executing: 10 + 20")

        # --- Step 4: Generate & Verify Report ---
        self.report_dir.mkdir(exist_ok=True)
        self.run_cmd(
            [FUNKOVERAGE, "report", self.log_dir, self.report_dir, "--formats", "xml"],
            msg="Generating coverage report..."
        )

        status = self.get_coverage_status()
        self.assertEqual(status.get("sum"), "called", "[FAIL] 'sum' should be marked as CALLED")
        self.assertEqual(status.get("sub"), "uncalled", "[FAIL] 'sub' should be marked as UNCALLED")
        print("   [OK] Invariant Check Passed: sum=called, sub=uncalled")

        # --- Step 5: Run Case 2 (Sub) ---
        self.run_cmd([self.bin_path, "10", "20", "-"], msg="Executing: 10 - 20")

        # --- Step 6: Regenerate & Verify ---
        self.run_cmd(
            [FUNKOVERAGE, "report", self.log_dir, self.report_dir, "--formats", "xml"],
            msg="Regenerating report..."
        )

        status = self.get_coverage_status()
        self.assertEqual(status.get("sub"), "called", "[FAIL] 'sub' should now be marked as CALLED")
        print("   [OK] Invariant Check Passed: sub=called")

        # --- Step 7: Unwrap ---
        self.run_cmd([FUNKOVERAGE, "unwrap", self.bin_path], msg="Unwrapping binary...")

        # Verify ELF header
        with open(self.bin_path, "rb") as f:
            header = f.read(4)
        self.assertEqual(header, b"\x7fELF", "[FAIL] Binary was not restored to ELF format")
        print("   [OK] Unwrap verified: Binary is ELF.")

if __name__ == "__main__":
    unittest.main(verbosity=2)
