"""The short demo and its saved evidence describe one read-only renewal workflow."""

from contextlib import closing, redirect_stderr, redirect_stdout
import io
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector import calls, slack
from gtm_connector.audit import read_audit
from gtm_connector.briefing import check_saved_brief
from gtm_connector.cli import main
from gtm_connector.client import LocalClient
from gtm_connector.demo import run
from gtm_connector.runtime import Runtime
from gtm_connector.usage import build_usage_report


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
        self.assertEqual(result["schema_version"], 1)
        self.assertEqual(result["status"], "accepted")
        self.assertEqual(result["workflow"], "renewal-demo")
        self.assertEqual(result["data_mode"], "demo")
        self.assertEqual(result["sources"], {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "demo_fixture"})
        self.assertEqual(result["rep"], {"name": "Morgan Reed", "role": "Account executive"})
        self.assertEqual(result["account"], {
            "id": "acc-007", "name": "Juniper Crest Learning", "record_ref": "crm/accounts/acc-007",
            "renewal_date": "2027-01-29"})
        self.assertEqual(result["lookup"]["query"], result["account"]["name"])
        self.assertEqual(result["lookup"]["matches"], 1)
        self.assertEqual(result["denied_read"]["tool"], "read_call_notes")
        self.assertEqual(result["denied_read"]["status"], 403)
        self.assertEqual(result["denied_read"]["required_scope"], "calls:read")
        self.assertEqual(result["activities_created"], 0)
        self.assertEqual(result["injection"], {
            "record_ref": "calls/call_notes/note-007", "slack_record_ref": "slack/messages/msg-007",
            "quoted": True, "activities_created": 0})
        brief = result["brief"]
        self.assertEqual(brief["account_id"], "acc-007")
        self.assertEqual(brief["as_of"], "2027-01-15")
        artifact = self.saved(brief["brief_id"])
        self.assertEqual(brief, {key: artifact[key] for key in brief})
        self.assertEqual(artifact["status"], "accepted")
        self.assertEqual(artifact["adapter"], "fixture")
        self.assertEqual(artifact["model_usage"], {"model": None, "input_tokens": 0, "output_tokens": 0})
        self.assertIn("Renewal is in 14 days", brief["text"])
        self.assertIn(brief["proposed_follow_up"], brief["text"].splitlines())
        self.assertEqual(brief["text"].count("Proposed follow-up:"), 1)
        records = {row["record_ref"]: row for row in artifact["evidence"]["records"]}
        for reference, instruction in (("calls/call_notes/note-007", calls.INJECTION_TEXT),
                                       ("slack/messages/msg-007", slack.INJECTION_TEXT)):
            self.assertIn(instruction, records[reference]["body"])
            self.assertIn(json.dumps(records[reference]["body"], ensure_ascii=True), brief["text"])
            self.assertIn("[" + reference + "]", brief["text"])
        self.assertIn("[crm/accounts/acc-007]", brief["text"])
        self.assertEqual({row.get("account_id", row["id"]) for row in records.values()}, {"acc-007"})
        check_saved_brief(self.state, brief["brief_id"], brief["text"])
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
        self.assertEqual(result["audit"]["rows"], len(receipts + brief_receipts))
        self.assertEqual(len(receipts + brief_receipts), 6)
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
        usage = build_usage_report(self.state)
        self.assertEqual(usage["calls"], {"total": 12, "ok": 10, "error": 2, "pending": 0})
        self.assertEqual(usage["model_tokens"], {"saved_briefs": 2, "unknown_briefs": 0,
                                               "input_tokens": 0, "output_tokens": 0})
        owner = json.loads((self.state / "owner.json").read_text())
        rendered = json.dumps([first, second, usage])
        for secret in [owner["owner_secret"], *tokens]:
            self.assertNotIn(secret, rendered)

    def test_json_command_and_legacy_slack_spelling_keep_the_same_narrative_contract(self):
        for flags in ((), ("--with-slack",)):
            with self.subTest(flags=flags):
                completed = self.command("demo", *flags, "--json")
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
        self.assertTrue(completed.stdout.startswith("Morgan Reed prepares Juniper Crest Learning's renewal.\n"))
        self.assertIn("Sources: CRM, call notes, and Slack use invented demo fixtures.", completed.stdout)
        self.assertEqual(completed.stdout.count("Sources:"), 1)
        self.assertIn("Call-note access was refused", completed.stdout)
        self.assertIn(f"Brief: {artifact['brief_id']}\nAs of: {artifact['as_of']}\n\n" + artifact["text"],
                      completed.stdout)
        self.assertEqual(completed.stdout.count("Proposed follow-up:"), 1)
        self.assertTrue(completed.stdout.endswith("No activities were created. The follow-up remains a proposal.\n"))
        self.assertNotIn("Idempotent write", completed.stdout)
        self.assertNotIn("Rate limit", completed.stdout)
        self.assertNotIn("\x1b", completed.stdout)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 6)
        self.assertEqual(self.activities(), 0)

    def test_changed_account_does_not_fall_back_to_another_story_or_publish_a_brief(self):
        with Runtime(self.state):
            pass
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection, connection:
            connection.execute("UPDATE accounts SET name = ? WHERE id = 'acc-007'", ("Changed invented account",))
        completed = self.command("demo", "--json")
        self.assertEqual(completed.returncode, 4, completed.stderr)
        self.assertEqual(completed.stdout, "")
        error = json.loads(completed.stderr)["error"]
        self.assertEqual(error["code"], "check_failed")
        self.assertIn("fresh --state-dir", error["message"])
        self.assertEqual(error["next"], "gtm-connector demo --help")
        self.assertFalse((self.state / "briefs").exists())
        self.assertEqual(self.activities(), 0)

    def test_changed_injection_does_not_claim_a_proof_that_did_not_run(self):
        with Runtime(self.state):
            pass
        with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection, connection:
            connection.execute("UPDATE call_notes SET body = ? WHERE id = 'note-007'", ("Invented ordinary note",))
        completed = self.command("demo", "--json")
        self.assertEqual(completed.returncode, 4, completed.stderr)
        self.assertEqual(completed.stdout, "")
        error = json.loads(completed.stderr)["error"]
        self.assertEqual(error["code"], "check_failed")
        self.assertIn("quoted data", error["message"])
        self.assertEqual(self.activities(), 0)

    def test_verification_invariant_failure_uses_the_failed_check_exit(self):
        with patch("gtm_connector.verification.run", side_effect=ValueError("demo invariant failed; inspect audit")), \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["verify", "--json", "--state-dir", str(self.state)])
        self.assertEqual(code, 4)
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], "check_failed")
        self.assertEqual(json.loads(stderr.getvalue())["error"]["next"], "gtm-connector verify --help")


if __name__ == "__main__":
    unittest.main()
