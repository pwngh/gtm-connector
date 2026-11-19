"""Exercise the documented CLI as a process against disposable databases."""

from contextlib import closing
import json
import select
import signal
import sqlite3
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

    def run_cli(self, *arguments, database=None, state=None, json_output=True):
        location = (["--db", str(database or self.database)]
                    if arguments[0] in ("seed", "accounts", "account")
                    else ["--state-dir", str(state or self.state)])
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments, *location,
             *(["--json"] if json_output else [])],
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

    def test_verification_runs_twice_with_distinct_audit_groups_and_one_activity(self):
        first = self.json_result(self.run_cli("verify"))
        second = self.json_result(self.run_cli("verify"))
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
                self.assertFalse({"conflicting_sources", "contradiction_flags", "uncited_candidate_rejected"}
                                 & set(summary))
                self.assertEqual(summary["injection_note"], {
                    "record_ref": "calls/call_notes/note-007", "activities_created": 0
                })
                self.assertEqual(summary["idempotent_write"]["requests"], 2)
                self.assertEqual(summary["idempotent_write"]["activities"], 1)
                self.assertEqual(summary["rate_limit"]["status"], 429)
                self.assertEqual(summary["audit_rows"], 12)
                self.assertEqual(Path(summary["audit_path"]), self.state / "audit.sqlite3")
                rows = self.json_result(self.run_cli("audit", "--brief", summary["brief_id"]))
                self.assertEqual(len(rows), 12)
                self.assertEqual({row["brief_id"] for row in rows}, {summary["brief_id"]})
                self.assertEqual(sum(row["http_status"] == 403 for row in rows), 1)
                self.assertEqual(sum(row["http_status"] == 429 for row in rows), 1)
                self.assertFalse(any(row["status"] == "pending" for row in rows))
                self.assertEqual(summary["brief_audit_rows"], 9)
                full, restricted = summary["briefs"]
                self.assertEqual(full["account_id"], "acc-003")
                self.assertTrue(full["text"].startswith(
                    'DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15\n'))
                self.assertIn('      "We will have the order form countersigned by January 30."',
                              full["text"].splitlines())
                self.assertIn("  Slack: not requested for this card.", full["text"].splitlines())
                self.assertEqual(restricted["account_id"], "acc-001")
                self.assertIn("  Calls: not read. The grant lacks calls:read.", restricted["text"].splitlines())
                self.assertEqual(summary["injection_brief"]["record_ref"], "calls/call_notes/note-007")
                self.assertEqual(summary["injection_brief"]["activities_created"], 0)
                generated_briefs.extend([full["brief_id"], restricted["brief_id"],
                                         summary["injection_brief"]["brief_id"]])
        all_rows = self.json_result(self.run_cli("audit"))
        self.assertEqual(len(all_rows), 42)
        self.assertEqual(len(set(generated_briefs)), 6)
        for identifier in generated_briefs:
            rows = [row for row in all_rows if row["brief_id"] == identifier]
            self.assertEqual([row["tool"] for row in rows],
                             ["get_account", "list_opportunities", "read_call_notes"])
            self.assertFalse(any(row["status"] == "pending" for row in rows))
        self.assertEqual(self.json_result(self.run_cli("audit", "--brief", "unknown-brief")), [])
        owner = json.loads((self.state / "owner.json").read_text())
        self.assertNotIn(owner["owner_secret"], json.dumps([first, second, all_rows]))
        by_tool = {}
        for row in all_rows:
            by_tool[row["tool"]] = by_tool.get(row["tool"], 0) + 1
        self.assertEqual(by_tool, {"search_accounts": 2, "get_account": 18, "read_call_notes": 12,
                                   "list_opportunities": 6, "log_activity": 4})
        self.assertEqual(sum(row["status"] == "error" for row in all_rows), 6)

    def test_accounts_past_one_hundred_matches_show_the_first_hundred_and_say_so(self):
        self.json_result(self.run_cli("seed"))

        def add(rows):
            with closing(sqlite3.connect(self.database)) as connection, connection:
                connection.executemany(
                    "INSERT INTO accounts VALUES (?, ?, 'Software', '2027-02-01', 0, 'USD', 'healthy', '')", rows)

        add([(f"acc-{number}", f"Invented account {300 - number}") for number in range(101, 171)])
        complete = self.run_cli("accounts")
        hundred = self.json_result(complete)
        self.assertEqual(len(hundred), 100)
        self.assertEqual([(row["name"], row["id"]) for row in hundred],
                         sorted((row["name"], row["id"]) for row in hundred))
        self.assertEqual(complete.stderr, "")
        plain = self.run_cli("accounts", json_output=False)
        self.assertEqual((len(plain.stdout.splitlines()), plain.stderr), (100, ""))

        # One more match, sorted last, leaves the rows as they were and adds only the statement.
        add([("acc-171", "Zeta invented account")])
        more = self.run_cli("accounts")
        self.assertEqual(self.json_result(more), {"accounts": hundred, "has_more": True})
        self.assertEqual(more.stderr, "")
        told = self.run_cli("accounts", json_output=False)
        self.assertEqual(told.returncode, 0, told.stderr)
        self.assertEqual(told.stdout, plain.stdout)
        self.assertEqual(told.stderr, "Showing the first 100 accounts. More match; narrow the search with --query.\n")
        narrowed = self.run_cli("accounts", "--query", "zeta")
        self.assertEqual([row["id"] for row in self.json_result(narrowed)], ["acc-171"])

    def test_seed_then_list_search_and_inspect_records(self):
        self.json_result(self.run_cli("seed"))
        accounts = self.json_result(self.run_cli("accounts"))
        self.assertEqual(len(accounts), 30)
        self.assertEqual([row["name"] for row in accounts], sorted(row["name"] for row in accounts))
        found = self.json_result(self.run_cli("accounts", "--query", accounts[0]["name"].upper()))
        self.assertIn(accounts[0]["id"], [row["id"] for row in found])
        self.assertEqual(self.json_result(self.run_cli("accounts", "--query", "not-an-account-xyz")), [])
        account = self.json_result(self.run_cli("account", "acc-007"))
        self.assertEqual(account["id"], "acc-007")
        self.assertEqual(account["contacts"][0]["id"], "con-007")
        self.assertNotIn("call_notes", account)
        self.assertNotIn("opportunities", account)
        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", json.dumps(account))

    def test_optional_slack_verification_reads_slack_under_its_own_scope(self):
        summary = self.json_result(self.run_cli("verify", "--with-slack"))
        self.assertEqual(summary["workflow"], "crm-calls-slack")
        self.assertEqual(summary["audit_rows"], 16)
        self.assertEqual(summary["brief_audit_rows"], 12)
        self.assertEqual(summary["slack"]["record"]["record_ref"], "slack/messages/msg-003")
        for direction in ("crm_without_slack", "slack_without_crm", "slack_without_calls"):
            self.assertEqual(summary["slack"][direction]["status"], 403)
        self.assertEqual(summary["injection_brief"]["activities_created"], 0)
        for brief in summary["briefs"]:
            self.assertNotIn("proposed_follow_up", brief)
            receipts = self.json_result(self.run_cli("audit", "--brief", brief["brief_id"]))
            self.assertEqual([row["tool"] for row in receipts], [
                "get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
        full, restricted = summary["briefs"]
        # The message's mention, link, and figure reach the card as inert quoted text.
        self.assertIn('      "Heads up [mention channel]: their general counsel will not sign until the zero-retention '
                      'and client-consent rider is approved."', full["text"].splitlines())
        self.assertNotIn("<!channel>", full["text"])
        self.assertNotIn("300,000", full["text"].split("SAID", 1)[0])
        self.assertIn("  Slack: not read. The grant lacks slack:read.", restricted["text"].splitlines())
        rows = self.json_result(self.run_cli("audit"))
        self.assertEqual((len(rows), sum(row["status"] == "error" for row in rows)), (28, 7))

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

    def test_brief_text_displays_the_saved_card_and_requested_date(self):
        result = self.run_cli("brief", "acc-001", "--as-of", "2027-02-01", json_output=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertRegex(result.stdout, r"\nBrief: brief-[0-9a-f]{32}\nResolve any number: gtm-connector trace "
                                        r"brief-[0-9a-f]{32} N --state-dir \S+\n\Z")
        identifier = result.stdout.splitlines()[-2].removeprefix("Brief: ")
        artifact = json.loads((self.state / "briefs" / (identifier + ".json")).read_text())
        self.assertEqual(result.stdout.split("\nBrief: ", 1)[0], artifact["text"])
        self.assertIn("  Renewal date: 2027-02-01, today [1]", result.stdout.splitlines())
        rows = self.json_result(self.run_cli("audit", "--brief", identifier))
        self.assertEqual(len(rows), 3)

    def test_json_brief_and_trace_resolve_each_number_from_saved_evidence(self):
        brief = self.json_result(self.run_cli("brief", "acc-001", "--without-calls"))
        self.assertEqual(set(brief), {"schema_version", "brief_id", "account_id", "as_of", "status", "sources",
                                      "text", "units"})
        self.assertEqual((brief["schema_version"], brief["status"]), (2, "accepted"))
        self.assertIn("  Calls: not read. The grant lacks calls:read.", brief["text"].splitlines())
        saved = self.state / "briefs" / (brief["brief_id"] + ".json")
        original_artifact = saved.read_bytes()
        artifact = json.loads(original_artifact)
        # The private file holds what was printed, and the evidence that was not.
        self.assertEqual({key: artifact[key] for key in brief}, brief)
        self.assertEqual(set(artifact) - set(brief), {"evidence"})
        self.assertEqual(artifact["evidence"]["notes_status"], "denied")
        traced = self.json_result(self.run_cli("trace", brief["brief_id"], "3"))
        self.assertEqual(set(traced), {"brief_id", "account_id", "n", "kind", "source", "record_ref", "receipt",
                                       "fields"})
        self.assertEqual((traced["brief_id"], traced["account_id"], traced["n"], traced["kind"], traced["record_ref"]),
                         (brief["brief_id"], "acc-001", 3, "record", "crm/opportunities/opp-001"))
        self.assertEqual(traced["fields"]["amount_cents"], 2_050_000)
        receipt = next(row for row in artifact["evidence"]["receipts"] if row["tool"] == "list_opportunities")
        self.assertEqual(traced["receipt"], {"audit_id": receipt["audit_id"], "tool": "list_opportunities",
                                             "status": "ok", "http_status": 200})
        human = self.run_cli("trace", brief["brief_id"], "1", json_output=False)
        self.assertEqual(human.returncode, 0, human.stderr)
        self.assertIn("Record\tcrm/accounts/acc-001\n", human.stdout)
        self.assertIn("(get_account, ok)", human.stdout)
        for number in ("0", "4", "-1"):
            with self.subTest(number=number):
                rejected = self.run_cli("trace", brief["brief_id"], number)
                self.assertEqual(rejected.returncode, 2)
                self.assertEqual(rejected.stdout, "")
                self.assertIn("This card has numbers 1 to 3.", rejected.stderr)
                self.assertNotIn("Traceback", rejected.stderr)
        self.assertEqual(saved.read_bytes(), original_artifact)

    def test_printed_json_holds_neutralized_text_and_the_raw_records_stay_in_the_private_file(self):
        brief = self.run_cli("brief", "acc-003", "--with-slack")
        printed = self.json_result(brief)
        saved = (self.state / "briefs" / (printed["brief_id"] + ".json")).read_text()
        for raw in ("<!channel>", "https://files.thornfield-wren.example", "rider-redline-v4.pdf"):
            with self.subTest(raw=raw):
                self.assertIn(raw, saved)
                self.assertNotIn(raw, brief.stdout)
        self.assertNotRegex(brief.stdout, r"<[!@]|https?://")
        self.assertIn("[mention channel]", printed["text"])
        self.assertIn("[link files.thornfield-wren.example]", printed["text"])
        for command in (("demo",), ("trace", printed["brief_id"], "7")):
            with self.subTest(command=command[0]):
                state = self.state if command[0] == "trace" else Path(self.directory.name) / "demo"
                output = self.run_cli(*command, state=state)
                self.assertEqual(output.returncode, 0, output.stderr)
                self.assertNotRegex(output.stdout, r"<[!@]|https?://")

    def test_trace_refuses_a_saved_card_that_no_longer_matches_the_audit_log(self):
        brief = self.json_result(self.run_cli("brief", "acc-003"))
        saved = self.state / "briefs" / (brief["brief_id"] + ".json")
        original = json.loads(saved.read_text())

        def edited(change):
            artifact = json.loads(json.dumps(original))
            change(artifact)
            saved.write_text(json.dumps(artifact))
            return self.run_cli("trace", brief["brief_id"], "1")

        cases = {
            "receipt outcome": lambda artifact: artifact["evidence"]["receipts"][0].update(row_count=9),
            "receipt identity": lambda artifact: artifact["evidence"]["receipts"][1].update(audit_id="forged"),
            "receipt dropped": lambda artifact: artifact["evidence"]["receipts"].pop(),
        }
        for name, change in cases.items():
            with self.subTest(name=name):
                refused = edited(change)
                self.assertEqual((refused.returncode, refused.stdout), (2, ""))
                self.assertIn("The saved card does not match the audit log of this state folder.", refused.stderr)
        refused = edited(lambda artifact: artifact.update(account_id="acc-004"))
        self.assertEqual((refused.returncode, refused.stdout), (2, ""))
        self.assertIn("invalid saved brief evidence", refused.stderr)
        saved.write_text(json.dumps(original))
        self.assertEqual(self.json_result(self.run_cli("trace", brief["brief_id"], "1"))["n"], 1)

    def test_a_spoken_number_traces_to_its_sentence_speaker_and_source_digest(self):
        brief = self.json_result(self.run_cli("brief", "acc-003", "--with-slack"))
        traced = self.json_result(self.run_cli("trace", brief["brief_id"], "5"))
        self.assertEqual({key: traced[key] for key in ("kind", "source", "record_ref", "speaker", "role", "basis",
                                                       "at", "sanitizer_version")},
                         {"kind": "sentence", "source": "calls", "record_ref": "calls/call_notes/note-003b",
                          "speaker": "Marian Holt", "role": "customer", "basis": "label matches contact [2]",
                          "at": "2027-01-12T15:00:00Z", "sanitizer_version": 1})
        self.assertRegex(traced["raw_sha256"], r"\A[0-9a-f]{64}\Z")
        self.assertIn(json.dumps(traced["text"]), brief["text"])
        human = self.run_cli("trace", brief["brief_id"], "7", json_output=False)
        self.assertIn("Role\tseller (listed seller identity)\n", human.stdout)
        self.assertIn("[mention channel]", human.stdout)
        self.assertNotIn("<!channel>", human.stdout)

    def test_invalid_brief_inputs_and_missing_saved_evidence_use_distinct_exits(self):
        for arguments, expected_exit in ((("brief", "acc-missing"), 2),
                                         (("brief", "acc-999"), 5),
                                         (("brief", "acc-001", "--as-of", "2027-02-30"), 2),
                                         (("trace", "brief-" + "0" * 32, "1"), 2),
                                         (("trace", "not-a-brief", "1"), 2),
                                         (("trace", "brief-" + "0" * 32, "one"), 2)):
            with self.subTest(arguments=arguments):
                rejected = self.run_cli(*arguments)
                self.assertEqual(rejected.returncode, expected_exit)
                self.assertEqual(rejected.stdout, "")
                self.assertTrue(rejected.stderr.strip())
                self.assertNotIn("Traceback", rejected.stderr)
        self.assertFalse((self.state / "briefs").exists())

    def test_serve_prints_endpoints_and_shuts_down_on_interrupt(self):
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--state-dir", str(self.state)],
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
