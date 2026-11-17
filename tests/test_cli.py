"""Exercise the documented CLI as a process against disposable databases."""

import json
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

    def run_cli(self, *arguments, database=None):
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments, "--db", str(database or self.database)],
            cwd=ROOT,
            text=True,
            capture_output=True,
            timeout=20,
            check=False,
        )

    def json_result(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stderr, "")
        return json.loads(result.stdout)

    def test_demo_runs_twice_with_same_results(self):
        first = self.json_result(self.run_cli("demo"))
        second = self.json_result(self.run_cli("demo"))
        self.assertEqual(first, second)
        self.assertEqual(first["status"], "foundation")
        self.assertEqual(first["counts"]["accounts"], 30)
        self.assertEqual(first["counts"]["activities"], 0)
        self.assertEqual(first["account"]["id"], "acc-001")
        self.assertTrue(first["account"]["contacts"])
        self.assertTrue(first["account"]["opportunities"])
        self.assertTrue(first["account"]["call_notes"])

    def test_seed_then_list_search_and_inspect_records(self):
        self.json_result(self.run_cli("seed"))
        accounts = self.json_result(self.run_cli("accounts"))
        self.assertEqual(len(accounts), 30)
        self.assertEqual([row["id"] for row in accounts], sorted(row["id"] for row in accounts))
        found = self.json_result(self.run_cli("accounts", "--query", accounts[0]["name"].upper()))
        self.assertIn(accounts[0]["id"], [row["id"] for row in found])
        self.assertEqual(self.json_result(self.run_cli("accounts", "--query", "not-an-account-xyz")), [])
        account = self.json_result(self.run_cli("account", "acc-007"))
        note = next(row for row in account["call_notes"] if row["id"] == "note-007")
        self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", note["body"])

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


if __name__ == "__main__":
    unittest.main()
