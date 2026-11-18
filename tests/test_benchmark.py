"""The benchmark must measure working fixture calls without leaking local state."""

import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class BenchmarkTests(unittest.TestCase):
    def test_small_run_has_complete_metrics_and_committed_receipts(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary) / "report.json"
            result = subprocess.run(
                [sys.executable, str(ROOT / "scripts/benchmark.py"), "--source", str(ROOT),
                 "--samples", "1", "--calls", "1", "--output", str(output)],
                cwd=temporary, capture_output=True, text=True, timeout=60)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(output.read_text(), result.stdout)
            report = json.loads(result.stdout)
            self.assertEqual(set(report["metrics"]), {
                "version", "help", "brief", "server_ready", "certificate_generation", "audited_call",
                "direct_read", "connector_overhead"})
            for metric in report["metrics"].values():
                self.assertEqual(metric["count"], 1)
                self.assertEqual(len(metric["samples_ms"]), 1)
                self.assertEqual(metric["p50_ms"], metric["samples_ms"][0])
                self.assertEqual(metric["p95_ms"], metric["samples_ms"][0])
            self.assertEqual(report["metrics"]["connector_overhead"]["samples_ms"][0],
                             report["metrics"]["audited_call"]["samples_ms"][0]
                             - report["metrics"]["direct_read"]["samples_ms"][0])
            self.assertIn("cryptography", report["libraries"])
            self.assertIn("cryptography_openssl", report["libraries"])
            self.assertIn("python_openssl", report["libraries"])
            self.assertEqual(report["audit"], {
                "measured_calls": 1, "warmup_calls": 5, "committed_receipts": 6, "pending_receipts": 0})
            self.assertRegex(report["source_commit"], r"^[0-9a-f]{40}$")
            self.assertIsInstance(report["source_dirty"], bool)
            self.assertRegex(report["script_sha256"], r"^[0-9a-f]{64}$")
            self.assertIn("python", report["machine"])
            self.assertIn("cpu", report["machine"])
            self.assertIn("memory_bytes", report["machine"])
            for private in (str(ROOT), temporary, "https://", "owner_secret", "Bearer ", "audit_id", "token_id"):
                self.assertNotIn(private, result.stdout)
            self.assertIn("Measuring", result.stderr)

    def test_percentiles_use_median_and_nearest_rank(self):
        spec = importlib.util.spec_from_file_location("fixture_benchmark", ROOT / "scripts/benchmark.py")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        summary = module.summarize(list(range(1, 21)))
        self.assertEqual(summary["count"], 20)
        self.assertEqual(summary["p50_ms"], 10.5)
        self.assertEqual(summary["p95_ms"], 19)


if __name__ == "__main__":
    unittest.main()
