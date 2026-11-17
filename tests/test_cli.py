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
        location = ([] if arguments[0] == "evaluate" else
                    ["--db", str(database or self.database)]
                    if arguments[0] in ("seed", "accounts", "account")
                    else ["--state-dir", str(state or self.state)])
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments, *location],
            cwd=ROOT,
            text=True,
            capture_output=True,
            timeout=60,
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
        generated_briefs = []
        for summary in (first, second):
            with self.subTest(brief=summary["brief_id"]):
                self.assertEqual(summary["status"], "connector")
                self.assertTrue(summary["brief_id"].startswith("demo-"))
                self.assertEqual(summary["accounts"], 30)
                self.assertEqual(set(summary["tools"]), {
                    "search_accounts", "get_account", "list_opportunities", "read_call_notes",
                    "read_slack_messages", "log_activity"
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
                self.assertEqual(summary["brief_audit_rows"], 9)
                self.assertEqual(len(summary["contradiction_flags"]), 1)
                self.assertIn("[crm/opportunities/opp-003] [calls/call_notes/note-003]",
                              summary["contradiction_flags"][0])
                full, restricted = summary["briefs"]
                self.assertEqual(full["account_id"], "acc-003")
                self.assertIn("closed_won", full["text"])
                self.assertIn("Procurement has not approved this renewal.", full["text"])
                self.assertIn("[calls/call_notes/note-003]", full["text"])
                self.assertEqual(restricted["account_id"], "acc-001")
                self.assertIn("Call notes were unavailable because the token lacks calls:read.",
                              restricted["text"])
                self.assertEqual(summary["injection_brief"]["record_ref"], "calls/call_notes/note-007")
                self.assertEqual(summary["injection_brief"]["activities_created"], 0)
                self.assertTrue(summary["uncited_candidate_rejected"])
                generated_briefs.extend([full["brief_id"], restricted["brief_id"],
                                         summary["injection_brief"]["brief_id"]])
        all_rows = self.json_result(self.run_cli("audit"))
        self.assertEqual(len(all_rows), 46)
        self.assertEqual(len(set(generated_briefs)), 6)
        for identifier in generated_briefs:
            rows = [row for row in all_rows if row["brief_id"] == identifier]
            self.assertEqual([row["tool"] for row in rows],
                             ["get_account", "list_opportunities", "read_call_notes"])
            self.assertFalse(any(row["status"] == "pending" for row in rows))
        self.assertEqual(self.json_result(self.run_cli("audit", "--brief", "unknown-brief")), [])
        owner = json.loads((self.state / "owner.json").read_text())
        self.assertNotIn(owner["owner_secret"], json.dumps([first, second, all_rows]))
        usage = self.json_result(self.run_cli("usage"))
        self.assertEqual(usage["calls"], {"total": 46, "ok": 40, "error": 6, "pending": 0})
        self.assertEqual({row["tool"]: row["total"] for row in usage["by_tool"]},
                         {"search_accounts": 2, "get_account": 18, "read_call_notes": 14,
                          "list_opportunities": 8, "log_activity": 4})
        self.assertEqual(usage["model_tokens"], {"saved_briefs": 6, "unknown_briefs": 0,
                                                "input_tokens": 0, "output_tokens": 0})
        scoped = self.json_result(self.run_cli("usage", "--brief", generated_briefs[0]))
        self.assertEqual(scoped["calls"]["total"], 3)
        self.assertEqual(len(scoped["briefs"]), 1)

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

    def test_optional_slack_demo_and_evaluation_keep_follow_up_grounded(self):
        summary = self.json_result(self.run_cli("demo", "--with-slack"))
        self.assertEqual(summary["workflow"], "crm-calls-slack")
        self.assertEqual(summary["audit_rows"], 18)
        self.assertEqual(summary["brief_audit_rows"], 12)
        self.assertEqual(summary["slack"]["record"]["record_ref"], "slack/messages/msg-003")
        for direction in ("crm_without_slack", "slack_without_crm", "slack_without_calls"):
            self.assertEqual(summary["slack"][direction]["status"], 403)
        self.assertEqual(len(summary["contradiction_flags"]), 2)
        self.assertEqual(summary["injection_brief"]["activities_created"], 0)
        for brief in summary["briefs"]:
            self.assertIn(brief["proposed_follow_up"], brief["text"].splitlines())
            receipts = self.json_result(self.run_cli("audit", "--brief", brief["brief_id"]))
            self.assertEqual([row["tool"] for row in receipts], [
                "get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
        restricted = summary["briefs"][1]
        self.assertIn("lacks slack:read", restricted["text"])
        self.assertNotIn("slack/messages/", restricted["text"])
        usage = self.json_result(self.run_cli("usage"))
        self.assertEqual(usage["calls"], {"total": 30, "ok": 23, "error": 7, "pending": 0})
        self.assertEqual(usage["model_tokens"]["input_tokens"], 0)
        report = self.json_result(self.run_cli("evaluate", "--with-slack"))
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["workflow"], "crm-calls-slack")
        self.assertGreater(report["expected_facts"], 182)
        self.assertEqual(report["facts_cited_correctly"], report["expected_facts"])
        self.assertEqual(report["audit_rows"], 80)
        self.assertEqual(report["activities_created"], 0)

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

    def test_usage_does_not_create_missing_state(self):
        missing = Path(self.directory.name) / "missing-state"
        result = self.run_cli("usage", state=missing)
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("audit database not found", result.stderr)
        self.assertFalse(missing.exists())

    def test_evaluate_reports_twenty_account_fixture_score(self):
        result = self.json_result(self.run_cli("evaluate"))
        self.assertEqual(result["status"], "passed")
        self.assertEqual(result["accounts"], 20)
        self.assertEqual(result["expected_facts"], 182)
        self.assertEqual(result["facts_found"], 182)
        self.assertEqual(result["facts_cited_correctly"], 182)
        self.assertEqual(result["facts_invented"], 0)
        self.assertEqual(result["audit_rows"], 60)
        self.assertEqual(result["activities_created"], 0)
        self.assertFalse(self.state.exists())

    def test_brief_text_displays_the_checked_draft_and_requested_date(self):
        result = self.run_cli("brief", "acc-001", "--as-of", "2027-02-01")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertRegex(result.stdout, r"\ABrief: brief-[0-9a-f]{32}\nAs of: 2027-02-01\n\n")
        identifier = result.stdout.splitlines()[0].removeprefix("Brief: ")
        artifact = json.loads((self.state / "briefs" / (identifier + ".json")).read_text())
        self.assertEqual(result.stdout.split("\n\n", 1)[1].strip(), artifact["text"].strip())
        self.assertIn("Renewal is due on 2027-02-01.", result.stdout)
        rows = self.json_result(self.run_cli("audit", "--brief", identifier))
        self.assertEqual(len(rows), 3)

    def test_json_brief_and_offline_checker_reject_uncited_and_misattributed_edits(self):
        brief = self.json_result(self.run_cli("brief", "acc-001", "--without-calls", "--json"))
        self.assertEqual(brief["status"], "accepted")
        self.assertEqual(brief["evidence"]["notes_status"], "denied")
        self.assertEqual(brief["model_usage"], {"model": None, "input_tokens": 0, "output_tokens": 0})
        self.assertIn("Call notes were unavailable", brief["text"])
        saved = self.state / "briefs" / (brief["brief_id"] + ".json")
        original_artifact = saved.read_bytes()
        candidate = Path(self.directory.name) / "candidate.txt"
        candidate.write_text(brief["text"])
        valid = self.json_result(self.run_cli("check-brief", brief["brief_id"], str(candidate)))
        self.assertEqual(valid, {"status": "valid", "brief_id": brief["brief_id"]})
        for changed in (brief["text"] + "The renewal was approved.\n",
                        brief["text"].replace("[crm/accounts/acc-001]", "[crm/accounts/acc-030]")):
            with self.subTest(candidate=changed):
                candidate.write_text(changed)
                rejected = self.run_cli("check-brief", brief["brief_id"], str(candidate))
                self.assertEqual(rejected.returncode, 2)
                self.assertEqual(rejected.stdout, "")
                self.assertIn("unsupported", rejected.stderr)
                self.assertNotIn("Traceback", rejected.stderr)
                self.assertEqual(saved.read_bytes(), original_artifact)

    def test_invalid_brief_inputs_and_missing_saved_evidence_exit_two(self):
        for arguments in (("brief", "acc-missing"),
                          ("brief", "acc-999"),
                          ("brief", "acc-001", "--as-of", "2027-02-30")):
            with self.subTest(arguments=arguments):
                rejected = self.run_cli(*arguments)
                self.assertEqual(rejected.returncode, 2)
                self.assertEqual(rejected.stdout, "")
                self.assertTrue(rejected.stderr.strip())
                self.assertNotIn("Traceback", rejected.stderr)
        candidate = Path(self.directory.name) / "candidate.txt"
        candidate.write_text("An unsupported sentence.\n")
        missing = self.run_cli("check-brief", "brief-" + "0" * 32, str(candidate))
        self.assertEqual(missing.returncode, 2)
        self.assertEqual(missing.stdout, "")
        self.assertNotIn("Traceback", missing.stderr)
        self.assertFalse((self.state / "briefs").exists())

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
