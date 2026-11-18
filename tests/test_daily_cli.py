"""Exercise the server stream and daily commands through real CLI processes."""

import json
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import unittest

from gtm_connector.audit import read_audit
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime


ROOT = Path(__file__).resolve().parents[1]


class DailyCLITests(unittest.TestCase):
    def run_cli(self, *arguments):
        return subprocess.run([sys.executable, "-m", "gtm_connector", *arguments], cwd=ROOT,
                              capture_output=True, text=True, timeout=60)

    def test_event_json_escapes_unpaired_and_control_characters(self):
        from contextlib import redirect_stdout
        import io
        from gtm_connector.cli import _print_event
        buffer = io.BytesIO()
        output = io.TextIOWrapper(buffer, encoding="utf-8", write_through=True)
        event = {"type": "tool_call", "time": "2026-11-17T00:00:00Z", "tool": "bad\ud800\x1bname",
                 "caller": "anonymous", "decision": "failed", "duration_ms": 1.0,
                 "receipt_id": None, "reason": "tool_error"}
        with redirect_stdout(output):
            _print_event(event, True)
        self.assertEqual(json.loads(buffer.getvalue()), event)
        self.assertNotIn(b"\x1b", buffer.getvalue())
        output.detach()

    def test_server_json_lines_include_allowed_and_refused_calls(self):
        with tempfile.TemporaryDirectory() as directory:
            process = subprocess.Popen([sys.executable, "-m", "gtm_connector", "serve", "--json",
                                        "--state-dir", directory], cwd=ROOT, stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                readable, _, _ = select.select([process.stdout], [], [], 15)
                self.assertTrue(readable, "server did not announce readiness")
                ready = json.loads(process.stdout.readline())
                self.assertEqual(ready["type"], "ready")
                self.assertEqual(ready["granted_scopes"], [])
                config = json.loads((Path(directory) / "owner.json").read_text())
                with LocalClient(config) as client:
                    token = client.authorize(["crm:read"])
                    allowed = client.call(token, "get_account", {"account_id": "acc-001"})
                    self.assertEqual(allowed.status_code, 200)
                    first = json.loads(process.stdout.readline())
                    denied = client.call(token, "read_call_notes", {"account_id": "acc-001"})
                    self.assertEqual(denied.status_code, 403)
                    second = json.loads(process.stdout.readline())
                self.assertEqual([first["decision"], second["decision"]], ["allowed", "refused"])
                self.assertEqual(second["reason"], "insufficient_scope")
                receipts = read_audit(Path(directory) / "audit.sqlite3")
                self.assertEqual({first["receipt_id"], second["receipt_id"]}, {r["audit_id"] for r in receipts})
                for secret in (token, config["owner_secret"]):
                    self.assertNotIn(secret, json.dumps([ready, first, second]))
                self.assertEqual(first["caller"], receipts[0]["token_id"])
                self.assertGreaterEqual(first["duration_ms"], 0)
                process.send_signal(signal.SIGINT)
                stdout, stderr = process.communicate(timeout=15)
                self.assertEqual(process.returncode, 0, stderr)
                self.assertEqual(stdout, "")
                self.assertNotIn("Traceback", stderr)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.communicate(timeout=5)
                process.stdout.close()
                process.stderr.close()

    def test_closed_event_pipe_keeps_server_and_audit_running(self):
        with tempfile.TemporaryDirectory() as directory:
            process = subprocess.Popen([sys.executable, "-m", "gtm_connector", "serve", "--json",
                                        "--state-dir", directory], cwd=ROOT, stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                readable, _, _ = select.select([process.stdout], [], [], 15)
                self.assertTrue(readable)
                self.assertEqual(json.loads(process.stdout.readline())["type"], "ready")
                process.stdout.close()
                config = json.loads((Path(directory) / "owner.json").read_text())
                with LocalClient(config) as client:
                    token = client.authorize(["crm:read"])
                    self.assertEqual(client.call(token, "get_account", {"account_id": "acc-001"}).status_code, 200)
                    readable, _, _ = select.select([process.stderr], [], [], 10)
                    self.assertTrue(readable, "server did not report the closed output")
                    self.assertIn("server continues", json.loads(process.stderr.readline())["message"])
                    self.assertIsNone(process.poll())
                    self.assertEqual(client.call(token, "get_account", {"account_id": "acc-002"}).status_code, 200)
                receipts = read_audit(Path(directory) / "audit.sqlite3")
                self.assertEqual(len(receipts), 2)
                self.assertTrue(all(row["status"] == "ok" for row in receipts))
                process.send_signal(signal.SIGINT)
                process.wait(timeout=15)
                stderr = process.stderr.read()
                self.assertEqual(process.returncode, 0, stderr)
                self.assertNotIn("Traceback", stderr)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                process.stdout.close()
                process.stderr.close()

    def test_closed_combined_output_keeps_server_running(self):
        with tempfile.TemporaryDirectory() as directory:
            process = subprocess.Popen([sys.executable, "-m", "gtm_connector", "serve", "--json",
                                        "--state-dir", directory], cwd=ROOT, stdin=subprocess.DEVNULL,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
            try:
                readable, _, _ = select.select([process.stdout], [], [], 15)
                self.assertTrue(readable)
                self.assertEqual(json.loads(process.stdout.readline())["type"], "ready")
                process.stdout.close()
                config = json.loads((Path(directory) / "owner.json").read_text())
                with LocalClient(config) as client:
                    token = client.authorize(["crm:read"])
                    self.assertEqual(client.call(token, "get_account", {"account_id": "acc-001"}).status_code, 200)
                    # Give the consumer time to observe its failed event and notice writes.
                    with self.assertRaises(subprocess.TimeoutExpired):
                        process.wait(timeout=2)
                    self.assertEqual(client.call(token, "get_account", {"account_id": "acc-002"}).status_code, 200)
                process.send_signal(signal.SIGINT)
                process.wait(timeout=15)
                self.assertEqual(process.returncode, 0)
                self.assertEqual(len(read_audit(Path(directory) / "audit.sqlite3")), 2)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                process.stdout.close()

    def test_doctor_reports_missing_setup_without_creating_it(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "missing"
            result = self.run_cli("doctor", "--state-dir", str(state), "--json")
            self.assertEqual(result.returncode, 4, result.stderr)
            report = json.loads(result.stdout)
            self.assertEqual(report["status"], "failed")
            self.assertTrue(all(row["fix"] for row in report["checks"] if row["status"] == "fail"))
            self.assertFalse(state.exists())

    def test_json_evaluation_has_separate_progress(self):
        result = self.run_cli("evaluate", "--json")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["status"], "passed")
        messages = [json.loads(line)["progress"] for line in result.stderr.splitlines()]
        self.assertEqual([message["completed"] for message in messages], list(range(21)))
        self.assertTrue(all(message["total"] == 20 for message in messages))

    def test_follow_receipts_prints_json_lines_and_interrupts_cleanly(self):
        with tempfile.TemporaryDirectory() as directory, Runtime(directory) as runtime:
            with LocalClient(runtime.config) as client:
                token = client.authorize(["crm:read"])
                client.call(token, "get_account", {"account_id": "acc-001"})
                process = subprocess.Popen([sys.executable, "-m", "gtm_connector", "audit", "--follow", "--json",
                                            "--state-dir", directory], cwd=ROOT, stdin=subprocess.DEVNULL,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    ready, _, _ = select.select([process.stdout], [], [], 10)
                    self.assertTrue(ready)
                    first = json.loads(process.stdout.readline())
                    client.call(token, "list_opportunities", {"account_id": "acc-001"})
                    ready, _, _ = select.select([process.stdout], [], [], 10)
                    self.assertTrue(ready)
                    second = json.loads(process.stdout.readline())
                    self.assertNotEqual(first["audit_id"], second["audit_id"])
                    process.send_signal(signal.SIGINT)
                    stdout, stderr = process.communicate(timeout=15)
                    self.assertEqual(process.returncode, 130)
                    self.assertEqual(json.loads(stderr)["error"]["code"], "interrupted")
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.communicate(timeout=5)
                    process.stdout.close()
                    process.stderr.close()


if __name__ == "__main__":
    unittest.main()
