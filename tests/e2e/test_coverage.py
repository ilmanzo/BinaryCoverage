"""
End-to-end tests for funkoverage bpftrace-based coverage tool.

Run as root (or with bpftrace having CAP_BPF via 'funkoverage setup'):
    sudo python3 tests/e2e/test_coverage.py

Set FUNKOVERAGE_SYSTEM_TEST=1 to enable tests on system binaries (wget, unzip).
"""

import os
import shutil
import subprocess
import tempfile
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path

PROJECT_ROOT = Path(__file__).resolve().parent.parent.parent
FUNKOVERAGE   = PROJECT_ROOT / "funkoverage"
SHIM_BINARY   = PROJECT_ROOT / "funkoverage-shim"
SAMPLE_DIR    = PROJECT_ROOT / "tests" / "sample"
SAMPLE_SRC    = SAMPLE_DIR / "sample"


def run(args, *, env=None, check=True, capture=False, **kw):
    merged = {**os.environ, **(env or {})}
    return subprocess.run(
        [str(a) for a in args],
        env=merged,
        check=check,
        capture_output=capture,
        text=True,
        **kw,
    )


class TestPrerequisites(unittest.TestCase):
    def test_funkoverage_exists(self):
        self.assertTrue(FUNKOVERAGE.exists(), f"Build funkoverage first: {FUNKOVERAGE}")

    def test_shim_exists(self):
        self.assertTrue(SHIM_BINARY.exists(), f"Build funkoverage-shim first: {SHIM_BINARY}")

    def test_sample_compiled(self):
        if not SAMPLE_SRC.exists():
            run(["make", "-C", str(SAMPLE_DIR)])
        self.assertTrue(SAMPLE_SRC.exists(), "Compile tests/sample/sample first")

    def test_bpftrace_available(self):
        self.assertTrue(shutil.which("bpftrace"), "bpftrace not found in PATH")


class BaseCoverageTest(unittest.TestCase):
    """Base class that sets up a temp env for each test."""

    def setUp(self):
        if not FUNKOVERAGE.exists() or not SHIM_BINARY.exists():
            self.skipTest("funkoverage or funkoverage-shim not built")
        if not SAMPLE_SRC.exists():
            try:
                run(["make", "-C", str(SAMPLE_DIR)])
            except subprocess.CalledProcessError:
                self.skipTest("could not compile sample binary")

        self.tmp      = Path(tempfile.mkdtemp(prefix="funkoverage_e2e_"))
        self.safe_bin = self.tmp / "safe"
        self.log_dir  = self.tmp / "logs"
        self.report   = self.tmp / "report"
        self.bin      = self.tmp / "sample"

        for d in (self.safe_bin, self.log_dir, self.report):
            d.mkdir(parents=True)

        shutil.copy2(str(SAMPLE_SRC), str(self.bin))
        self.bin.chmod(0o755)

        self.env = {
            "FUNKOVERAGE_SHIM": str(SHIM_BINARY),
            "SAFE_BIN_DIR":     str(self.safe_bin),
            "LOG_DIR":          str(self.log_dir),
        }

    def tearDown(self):
        # Best-effort cleanup (uninstall if still installed)
        try:
            run([FUNKOVERAGE, "uninstall", str(self.bin)], env=self.env, check=False)
        except Exception:
            pass
        shutil.rmtree(str(self.tmp), ignore_errors=True)

    def install(self, extra_args=None):
        cmd = [FUNKOVERAGE, "install"] + (extra_args or []) + [str(self.bin)]
        run(cmd, env=self.env)

    def uninstall(self):
        run([FUNKOVERAGE, "uninstall", str(self.bin)], env=self.env)

    def run_sample(self, *flags):
        run([str(self.bin)] + list(flags), env=self.env)

    def generate_report(self, formats="xml"):
        run([FUNKOVERAGE, "report", str(self.log_dir), str(self.report),
             "--formats", formats], env=self.env)

    def called_functions(self):
        """Return set of function names found in any _called.log."""
        called = set()
        for f in self.log_dir.glob("*_called.log"):
            for line in f.read_text().splitlines():
                if line.startswith("CALLED "):
                    parts = line.split(" ", 2)
                    if len(parts) == 3:
                        called.add(parts[2])
        return called

    def known_functions(self):
        """Return set of function names found in any _functions.log."""
        known = set()
        for f in self.log_dir.glob("*_functions.log"):
            for line in f.read_text().splitlines():
                if line.startswith("FUNC "):
                    parts = line.split(" ", 2)
                    if len(parts) == 3:
                        known.add(parts[2])
        return known

    def parse_xml_coverage(self, image_base):
        """Return (called_set, uncalled_set) from XML report for given image."""
        xml_files = list(self.report.glob(f"coverage_*{image_base}*.xml"))
        if not xml_files:
            return set(), set()
        tree = ET.parse(str(xml_files[0]))
        called, uncalled = set(), set()
        for passed in tree.iter("passed"):
            text = passed.text or ""
            for line in text.splitlines():
                line = line.strip()
                if line.startswith("✓ "):
                    called.add(line[2:])
                elif line.startswith("✗ "):
                    uncalled.add(line[2:])
        return called, uncalled


class TestEnumerate(BaseCoverageTest):
    def test_enumerate_finds_all_groups(self):
        result = run(
            [FUNKOVERAGE, "enumerate", "--no-libs", str(self.bin)],
            env=self.env, capture=True,
        )
        funcs = set()
        for line in result.stdout.splitlines():
            parts = line.split(" ", 1)
            if len(parts) == 2:
                funcs.add(parts[1])

        groups = {
            "str_": [n for n in funcs if n.startswith("str_")],
            "math_": [n for n in funcs if n.startswith("math_")],
            "arr_": [n for n in funcs if n.startswith("arr_")],
            "util_": [n for n in funcs if n.startswith("util_")],
        }
        for prefix, found in groups.items():
            self.assertGreaterEqual(len(found), 20,
                f"Expected >=20 {prefix} functions, got {len(found)}: {found}")


class TestInstallUninstall(BaseCoverageTest):
    def test_install_places_shim(self):
        self.install(["--no-libs"])
        # The shim should be an ELF at the original path
        magic = self.bin.read_bytes()[:4]
        self.assertEqual(magic, b"\x7fELF", "Expected ELF shim at binary path")
        # Real binary should be in safe dir
        safe = self.safe_bin / "sample"
        self.assertTrue(safe.exists(), f"Real binary not found at {safe}")

    def test_uninstall_restores_original(self):
        self.install(["--no-libs"])
        self.uninstall()
        magic = self.bin.read_bytes()[:4]
        self.assertEqual(magic, b"\x7fELF", "Expected original ELF after uninstall")
        self.assertFalse((self.safe_bin / "sample").exists(),
                         "Safe binary should be removed after uninstall")

    def test_functions_log_written_on_install(self):
        self.install(["--no-libs"])
        func_logs = list(self.log_dir.glob("*_functions.log"))
        self.assertGreater(len(func_logs), 0, "Expected _functions.log after install")
        funcs = self.known_functions()
        self.assertGreater(len(funcs), 50,
            f"Expected >50 known functions, got {len(funcs)}")

    def test_double_install_fails(self):
        self.install(["--no-libs"])
        result = run(
            [FUNKOVERAGE, "install", "--no-libs", str(self.bin)],
            env=self.env, check=False, capture=True,
        )
        self.assertNotEqual(result.returncode, 0,
            "Second install should fail")


@unittest.skipUnless(shutil.which("bpftrace"), "bpftrace not available")
class TestCoverageTracing(BaseCoverageTest):
    """Tests that require bpftrace to actually run (needs CAP_BPF or root)."""

    def _skip_if_no_bpf(self):
        if os.getuid() != 0:
            result = subprocess.run(
                ["getcap", shutil.which("bpftrace")],
                capture_output=True, text=True,
            )
            if "cap_bpf" not in result.stdout:
                self.skipTest(
                    "bpftrace needs CAP_BPF. Run: sudo funkoverage setup"
                )

    def test_strings_group_traced(self):
        self._skip_if_no_bpf()
        self.install(["--no-libs"])
        self.run_sample("--strings")
        self.uninstall()

        called = self.called_functions()
        str_called = {f for f in called if f.startswith("str_")}
        self.assertGreater(len(str_called), 15,
            f"Expected >15 str_ functions traced, got {len(str_called)}: {str_called}")

        # Math functions should NOT be called
        math_called = {f for f in called if f.startswith("math_")}
        self.assertEqual(len(math_called), 0,
            f"math_ functions should not be called, but got: {math_called}")

    def test_math_group_traced(self):
        self._skip_if_no_bpf()
        self.install(["--no-libs"])
        self.run_sample("--math")
        self.uninstall()

        called = self.called_functions()
        math_called = {f for f in called if f.startswith("math_")}
        self.assertGreater(len(math_called), 15,
            f"Expected >15 math_ functions traced, got {len(math_called)}")

    def test_cumulative_coverage(self):
        self._skip_if_no_bpf()
        self.install(["--no-libs"])
        self.run_sample("--strings")
        self.run_sample("--math")
        self.uninstall()

        called = self.called_functions()
        str_called  = {f for f in called if f.startswith("str_")}
        math_called = {f for f in called if f.startswith("math_")}
        self.assertGreater(len(str_called), 15)
        self.assertGreater(len(math_called), 15)

    def test_all_coverage_report(self):
        self._skip_if_no_bpf()
        self.install(["--no-libs"])
        self.run_sample("--all")
        self.uninstall()

        self.generate_report(formats="xml,txt")
        xml_files = list(self.report.glob("coverage_*.xml"))
        self.assertGreater(len(xml_files), 0, "Expected XML report")

    def test_trace_inline(self):
        """funkoverage trace does not require permanent install."""
        self._skip_if_no_bpf()
        run([FUNKOVERAGE, "trace", "--no-libs", str(self.bin), "--strings"],
            env=self.env)
        called_logs = list(self.log_dir.glob("*_called.log"))
        self.assertGreater(len(called_logs), 0, "Expected _called.log after trace")


@unittest.skipUnless(os.getenv("FUNKOVERAGE_SYSTEM_TEST"), "set FUNKOVERAGE_SYSTEM_TEST=1 to enable")
class TestSystemBinaries(unittest.TestCase):
    """Tests on real system binaries requiring debug symbol packages."""

    def _check_debug_symbols(self, binary):
        result = subprocess.run(
            [str(FUNKOVERAGE), "enumerate", "--no-libs", binary],
            capture_output=True, text=True,
        )
        if result.returncode != 0 or not result.stdout.strip():
            self.skipTest(
                f"No debug symbols for {binary}. "
                f"Install: sudo zypper install $(rpm -qf {binary} --qf '%{{NAME}}-debuginfo')"
            )

    def test_wget(self):
        wget = shutil.which("wget")
        if not wget:
            self.skipTest("wget not installed")
        self._check_debug_symbols(wget)
        result = subprocess.run(
            [str(FUNKOVERAGE), "enumerate", "--no-libs", wget],
            capture_output=True, text=True,
        )
        count = len(result.stdout.strip().splitlines())
        self.assertGreater(count, 50, f"Expected >50 functions in wget, got {count}")

    def test_unzip(self):
        unzip = shutil.which("unzip")
        if not unzip:
            self.skipTest("unzip not installed")
        self._check_debug_symbols(unzip)


if __name__ == "__main__":
    unittest.main(verbosity=2)
