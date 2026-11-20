"""The short demo and its saved evidence describe one read-only renewal workflow."""

from contextlib import closing, redirect_stderr, redirect_stdout
import io
import json
import os
from pathlib import Path
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.audit import read_audit
from gtm_connector.briefing import trace_saved
from gtm_connector.cli import main
from gtm_connector.client import LocalClient
from gtm_connector.demo import run
from gtm_connector.runtime import Runtime


ROOT = Path(__file__).resolve().parents[1]


class NarrativeDemoTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.state = self.directory / "state"

    def command(self, *arguments):
        return subprocess.run([sys.executable, "-m", "gtm_connector", *arguments,
                               "--state-dir", str(self.state)], cwd=ROOT, capture_output=True,
                              text=True, timeout=30)

    def saved(self, brief_id):
        return json.loads((self.state / "briefs" / (brief_id + ".json")).read_text())

    def activities(self):
        with closing(sqlite3.connect((self.state / "crm.sqlite3").as_uri() + "?mode=ro", uri=True)) as connection:
            return connection.execute("SELECT count(*) FROM activities").fetchone()[0]

    def assert_story(self, result):
        self.assertEqual(set(result), {"schema_version", "status", "workflow", "data_mode", "sources", "account",
                                       "lookup", "denied_read", "brief", "audit", "activities_created"})
        self.assertEqual(result["schema_version"], 2)
        self.assertEqual(result["status"], "accepted")
        self.assertEqual(result["workflow"], "renewal-demo")
        self.assertEqual(result["data_mode"], "demo")
        self.assertEqual(result["sources"], {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "demo_fixture"})
        self.assertEqual(result["account"], {
            "id": "acc-003", "name": "Thornfield Wren LLP", "record_ref": "crm/accounts/acc-003",
            "renewal_date": "2027-02-08"})
        self.assertEqual(result["lookup"]["query"], result["account"]["name"])
        self.assertEqual(result["lookup"]["matches"], 1)
        self.assertEqual(result["denied_read"]["tool"], "read_call_notes")
        self.assertEqual(result["denied_read"]["status"], 403)
        self.assertEqual(result["denied_read"]["required_scope"], "calls:read")
        self.assertEqual(result["activities_created"], 0)
        brief = result["brief"]
        self.assertEqual(set(brief), {"brief_id", "account_id", "as_of", "text", "units", "gaps"})
        self.assertEqual(brief["account_id"], "acc-003")
        self.assertEqual(brief["as_of"], "2027-01-15")
        artifact = self.saved(brief["brief_id"])
        self.assertEqual(brief, {key: artifact[key] for key in brief})
        self.assertEqual((artifact["schema_version"], artifact["status"]), (3, "accepted"))
        self.assertTrue(brief["text"].startswith('DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15\n'))
        self.assertIn("  Renewal date: 2027-02-08, in 24 days [1]", brief["text"].splitlines())
        records = {row["record_ref"]: row for row in artifact["evidence"]["records"]}
        self.assertEqual({row.get("account_id", row["id"]) for row in records.values()}, {"acc-003"})
        # Every number on the card traces to a record of this account and a finished receipt.
        for unit in brief["units"]:
            traced = trace_saved(self.state, brief["brief_id"], unit["n"])
            self.assertIn(traced["record_ref"], records)
            self.assertEqual(traced["receipt"]["status"], "ok")
        receipts = read_audit(self.state / "audit.sqlite3", result["audit"]["run_id"])
        brief_receipts = read_audit(self.state / "audit.sqlite3", brief["brief_id"])
        self.assertEqual([(row["tool"], row["http_status"]) for row in receipts],
                         [("search_accounts", 200), ("read_call_notes", 403)])
        self.assertEqual(result["lookup"]["receipt_id"], receipts[0]["audit_id"])
        self.assertEqual(result["denied_read"]["receipt_id"], receipts[1]["audit_id"])
        self.assertEqual(receipts[1]["rows"], [])
        self.assertEqual(receipts[1]["error"], "request_failed")
        self.assertEqual([row["tool"] for row in brief_receipts],
                         ["get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
        self.assertEqual([row["status"] for row in receipts + brief_receipts],
                         ["ok", "error", "ok", "ok", "ok", "ok"])
        self.assertEqual(result["audit"]["rows"], len(receipts + brief_receipts) + 1)
        self.assertEqual(len(receipts + brief_receipts), 6)
        sealed = read_audit(self.state / "audit.sqlite3", tool="save_brief")
        self.assertIn(("ok", artifact["sealed_by"]), [(row["status"], row["audit_id"]) for row in sealed])
        self.assertFalse(any(row["tool"] == "log_activity" for row in receipts + brief_receipts))
        self.assertEqual(self.activities(), 0)

    def test_two_runs_keep_one_account_and_only_read_grants_with_independent_saved_briefs(self):
        authorize = LocalClient.authorize
        grants, tokens = [], []

        def observe(client, scopes):
            grants.append(list(scopes))
            token = authorize(client, scopes)
            tokens.append(token)
            return token

        with patch.object(LocalClient, "authorize", observe):
            first = run(self.state)
            second = run(self.state)
        for result in (first, second):
            self.assert_story(result)
        self.assertNotEqual(first["audit"]["run_id"], second["audit"]["run_id"])
        self.assertNotEqual(first["brief"]["brief_id"], second["brief"]["brief_id"])
        self.assertEqual(first["account"], second["account"])
        self.assertEqual(grants, [["crm:read"], ["crm:read", "calls:read", "slack:read"]] * 2)
        self.assertEqual([row["status"] for row in read_audit(self.state / "audit.sqlite3")].count("ok"), 12)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 14)
        owner = json.loads((self.state / "owner.json").read_text())
        rendered = json.dumps([first, second])
        for secret in [owner["owner_secret"], *tokens]:
            self.assertNotIn(secret, rendered)

    def test_json_command_keeps_the_narrative_contract(self):
        completed = self.command("demo", "--json")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stderr, "")
        self.assert_story(json.loads(completed.stdout))

    def test_human_command_prints_the_saved_checked_text_with_one_source_disclosure(self):
        completed = self.command("demo")
        self.assertEqual(completed.returncode, 0, completed.stderr)
        self.assertEqual(completed.stderr, "")
        artifacts = list((self.state / "briefs").glob("*.json"))
        self.assertEqual(len(artifacts), 1)
        artifact = json.loads(artifacts[0].read_text())
        self.assertTrue(completed.stdout.startswith(
            "Call-note access was refused (HTTP 403) for a grant without calls:read.\n"
            "Every source below is an invented demo fixture.\n\n" + artifact["text"]))
        self.assertTrue(completed.stdout.endswith(
            f"Brief: {artifact['brief_id']}\n"
            f"List every unit: gtm-connector inspect {artifact['brief_id']} --state-dir {shlex.quote(str(self.state))}\n"
            f"Resolve a number: gtm-connector inspect {artifact['brief_id']} N "
            f"--state-dir {shlex.quote(str(self.state))}\n"))
        self.assertNotIn("Idempotent write", completed.stdout)
        self.assertNotIn("Rate limit", completed.stdout)
        self.assertNotIn("\x1b", completed.stdout)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 7)
        self.assertEqual(self.activities(), 0)

    def test_changed_account_does_not_fall_back_to_another_story_or_publish_a_brief(self):
        with Runtime(self.state):
            pass
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection, connection:
            connection.execute("UPDATE accounts SET name = ? WHERE id = 'acc-003'", ("Changed invented account",))
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        occupied = self.directory / "renewal-demo-fresh"
        occupied.mkdir()
        (occupied / "keep.txt").write_text("Preserve this existing folder.")
        completed = self.command("demo", "--json")
        self.assertEqual(completed.returncode, 4, completed.stderr)
        self.assertEqual(completed.stdout, "")
        error = json.loads(completed.stderr)["error"]
        self.assertEqual(error["code"], "check_failed")
        self.assertIn("older or edited book", error["message"])
        self.assertIn("Thornfield Wren LLP (acc-003)", error["message"])
        self.assertEqual(shlex.split(error["next"]),
                         ["gtm-connector", "demo", "--state-dir", str(self.directory / "renewal-demo-fresh-2")])
        self.assertFalse((self.directory / "renewal-demo-fresh-2").exists())
        self.assertEqual((occupied / "keep.txt").read_text(), "Preserve this existing folder.")
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}, before)
        self.assertFalse((self.state / "briefs").exists())
        self.assertEqual(self.activities(), 0)

    def test_default_demo_and_verification_leave_older_v2_state_untouched(self):
        legacy = self.directory / "var" / "v2"
        with Runtime(legacy):
            pass
        with closing(sqlite3.connect(legacy / "crm.sqlite3")) as connection, connection:
            connection.execute("UPDATE accounts SET name = 'Fixture account acc-003' WHERE id = 'acc-003'")
        before = {path.name: path.read_bytes() for path in legacy.iterdir() if path.is_file()}
        for command, folder in ((["demo"], "renewal-demo"), (["debug", "verify"], "verification")):
            completed = subprocess.run([sys.executable, "-m", "gtm_connector", *command, "--json"],
                                       cwd=self.directory, env={**os.environ, "PYTHONPATH": str(ROOT)},
                                       capture_output=True, text=True, timeout=30)
            self.assertEqual(completed.returncode, 0, completed.stderr)
            self.assertEqual({path.name: path.read_bytes() for path in legacy.iterdir() if path.is_file()}, before)
            self.assertTrue((self.directory / "var" / folder / "audit.sqlite3").is_file())
        self.state = self.directory / "var" / "renewal-demo"
        self.assertEqual(self.activities(), 0)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 7)
        self.state = self.directory / "var" / "verification"
        self.assertEqual(self.activities(), 1)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 24)

    def test_existing_demo_preflight_refuses_a_symlink_before_opening_the_database(self):
        with Runtime(self.state):
            pass
        source = self.state / "crm.sqlite3"
        outside = self.directory / "outside.sqlite3"
        source.rename(outside)
        source.symlink_to(outside)
        before = outside.read_bytes()
        owner_before = (self.state / "owner.json").read_bytes()
        with patch("gtm_connector.demo.store.connect") as connect, \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["demo", "--json", "--state-dir", str(self.state)])
        self.assertEqual(code, 4)
        connect.assert_not_called()
        self.assertEqual(stdout.getvalue(), "")
        self.assertIn("private regular local fixtures", json.loads(stderr.getvalue())["error"]["message"])
        self.assertEqual(outside.read_bytes(), before)
        self.assertEqual((self.state / "owner.json").read_bytes(), owner_before)

    def test_verification_invariant_failure_uses_the_failed_check_exit(self):
        with patch("gtm_connector.verification.run", side_effect=ValueError("demo invariant failed; inspect audit")), \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["debug", "verify", "--json", "--state-dir", str(self.state)])
        self.assertEqual(code, 4)
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], "check_failed")
        self.assertEqual(json.loads(stderr.getvalue())["error"]["next"], "gtm-connector debug verify --help")


if __name__ == "__main__":
    unittest.main()
