"""Exercise the documented CLI as a process against disposable databases."""

import json
import select
import signal
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


class CLITests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.database = Path(self.directory.name) / "crm.sqlite3"
        self.state = Path(self.directory.name).resolve() / "state"

    def run_cli(self, *arguments, database=None, state=None):
        location = (["--db", str(database or self.database)]
                    if arguments[0] in ("seed", "accounts", "account")
                    else ["--state-dir", str(state or self.state)])
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments, *location],
            cwd=ROOT,
            text=True,
            capture_output=True,
            timeout=30,
            check=False,
        )

    def json_result(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        return json.loads(result.stdout)

    def test_demo_runs_twice_with_distinct_audit_groups_and_one_activity(self):
        first = self.json_result(self.run_cli("demo"))
        second = self.json_result(self.run_cli("demo"))
        self.assertNotEqual(first["brief_id"], second["brief_id"])
        self.assertEqual(first["renewal_record"], second["renewal_record"])
        self.assertEqual(first["idempotent_write"], second["idempotent_write"])
        for summary in (first, second):
            with self.subTest(brief=summary["brief_id"]):
                self.assertEqual(summary["status"], "connector")
                self.assertTrue(summary["brief_id"].startswith("demo-"))
                self.assertEqual(summary["accounts"], 30)
                self.assertEqual(set(summary["tools"]), {
                    "search_accounts", "get_account", "list_opportunities", "read_call_notes", "log_activity"
                })
                self.assertEqual(summary["renewal_record"]["record_ref"], "crm/accounts/acc-001")
                self.assertEqual(summary["notes_without_scope"]["status"], 403)
                self.assertIn("calls:read", summary["notes_without_scope"]["message"])
                self.assertEqual(summary["empty_notes"], [])
                conflict = summary["conflicting_sources"]
                self.assertEqual(conflict["opportunity"]["stage"], "closed_won")
                self.assertIn("has not approved", conflict["note"]["body"])
                self.assertEqual(summary["injection_note"], {
                    "record_ref": "calls/call_notes/note-007", "activities_created": 0
                })
                self.assertEqual(summary["idempotent_write"]["requests"], 2)
                self.assertEqual(summary["idempotent_write"]["activities"], 1)
                self.assertEqual(summary["rate_limit"]["status"], 429)
                self.assertEqual(summary["audit_rows"], 14)
                self.assertEqual(Path(summary["audit_path"]), self.state / "audit.sqlite3")
                rows = self.json_result(self.run_cli("audit", "--brief", summary["brief_id"]))
                self.assertEqual(len(rows), 14)
                self.assertEqual({row["brief_id"] for row in rows}, {summary["brief_id"]})
                self.assertEqual(sum(row["http_status"] == 403 for row in rows), 1)
                self.assertEqual(sum(row["http_status"] == 429 for row in rows), 1)
                self.assertFalse(any(row["status"] == "pending" for row in rows))
        all_rows = self.json_result(self.run_cli("audit"))
        self.assertEqual(len(all_rows), 28)
        self.assertEqual(self.json_result(self.run_cli("audit", "--brief", "unknown-brief")), [])
        owner = json.loads((self.state / "owner.json").read_text())
        self.assertNotIn(owner["owner_secret"], json.dumps([first, second, all_rows]))

    def test_seed_then_list_search_and_inspect_records(self):
        self.json_result(self.run_cli("seed"))
        accounts = self.json_result(self.run_cli("accounts"))
        self.assertEqual(len(accounts), 30)
        self.assertEqual([row["id"] for row in accounts], sorted(row["id"] for row in accounts))
        found = self.json_result(self.run_cli("accounts", "--query", accounts[0]["name"].upper()))
        self.assertIn(accounts[0]["id"], [row["id"] for row in found])
        self.assertEqual(self.json_result(self.run_cli("accounts", "--query", "not-an-account-xyz")), [])
        account = self.json_result(self.run_cli("account", "acc-007"))
        self.assertEqual(account["id"], "acc-007")
        self.assertEqual(account["contacts"][0]["id"], "con-007")
        self.assertNotIn("call_notes", account)
        self.assertNotIn("opportunities", account)
        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", json.dumps(account))

    def test_missing_account_exits_two_without_json_on_stdout(self):
        self.json_result(self.run_cli("seed"))
        result = self.run_cli("account", "acc-missing")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("acc-missing", result.stderr)

    def test_read_commands_do_not_create_missing_database_or_parent(self):
        missing = Path(self.directory.name) / "missing" / "crm.sqlite3"
        for arguments in (("accounts",), ("account", "acc-001")):
            with self.subTest(arguments=arguments):
                result = self.run_cli(*arguments, database=missing)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, "")
                self.assertTrue(result.stderr.strip())
                self.assertNotIn("Traceback", result.stderr)
                self.assertFalse(missing.parent.exists())

    def test_audit_does_not_create_missing_state(self):
        missing = Path(self.directory.name) / "missing-state"
        result = self.run_cli("audit", "--brief", "unknown", state=missing)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("audit database not found", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertFalse(missing.exists())

    def test_serve_prints_endpoints_and_shuts_down_on_interrupt(self):
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--state-dir", str(self.state)],
            cwd=ROOT, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True,
        )
        try:
            ready, _, _ = select.select([process.stdout], [], [], 15)
            self.assertTrue(ready, "serve did not publish its endpoints within 15 seconds")
            endpoints = json.loads(process.stdout.readline())
            self.assertTrue(endpoints["resource"].startswith("https://127.0.0.1:"))
            self.assertTrue(endpoints["resource"].endswith("/mcp"))
            self.assertTrue(endpoints["issuer"].startswith("https://127.0.0.1:"))
            self.assertEqual(Path(endpoints["owner_config"]), self.state / "owner.json")
            self.assertNotIn("owner_secret", endpoints)
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


if __name__ == "__main__":
    unittest.main()
