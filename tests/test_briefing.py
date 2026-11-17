"""Exercise the complete read-only briefing workflow over local HTTPS."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.briefing import create_brief
from gtm_connector.calls import INJECTION_TEXT
from gtm_connector.citations import BriefRejected, build_facts
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime


class BriefingTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name).resolve() / "state"

    def activities(self):
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection:
            return connection.execute("SELECT id FROM activities").fetchall()

    def assert_read_plan(self, runtime, brief):
        receipts = runtime.connector.audit.read(brief["brief_id"])
        self.assertEqual([row["tool"] for row in receipts],
                         ["get_account", "list_opportunities", "read_call_notes"])
        self.assertEqual(len({row["audit_id"] for row in receipts}), 3)
        self.assertTrue(all(row["arguments"] == {"account_id": brief["account_id"]}
                            for row in receipts))
        self.assertEqual(brief["evidence"]["receipts"], receipts)
        self.assertTrue(all("crm:write" not in grant.scopes
                            for grant in runtime.connector.authority.tokens.values()))
        self.assertEqual(self.activities(), [])
        return receipts

    def assert_no_accepted_artifact(self):
        for path in (self.state / "briefs").glob("*.json"):
            artifact = json.loads(path.read_text())
            self.assertNotEqual(artifact["status"], "accepted")
            self.assertNotIn("text", artifact)
            self.assertNotIn("candidate", artifact)

    def test_full_brief_cites_facts_and_persists_private_evidence_without_credentials(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-001")
            self.assertEqual(brief["status"], "accepted")
            self.assertEqual(brief["as_of"], "2027-01-15")
            self.assertIn("2027-02-01", brief["text"])
            self.assertIn("Renewal is in 17 days as of 2027-01-15.", brief["text"])
            self.assertIn("20,500.00", brief["text"])
            for reference in ("crm/accounts/acc-001", "crm/contacts/con-001",
                              "crm/opportunities/opp-001", "calls/call_notes/note-001"):
                self.assertIn("[" + reference + "]", brief["text"])
            self.assertIn("[run/" + brief["brief_id"] + "]", brief["text"])
            self.assertEqual(brief["model_usage"]["input_tokens"], 0)
            self.assertEqual(brief["model_usage"]["output_tokens"], 0)
            receipts = self.assert_read_plan(runtime, brief)
            self.assertEqual([row["http_status"] for row in receipts], [200, 200, 200])
            saved = self.state / "briefs" / (brief["brief_id"] + ".json")
            self.assertEqual(json.loads(saved.read_text()), brief)
            self.assertEqual(stat.S_IMODE(saved.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(saved.parent.stat().st_mode), 0o700)
            serialized = saved.read_text()
            self.assertNotIn(runtime.config["owner_secret"], serialized)
            for token in runtime.connector.authority.tokens:
                self.assertNotIn(token, serialized)

    def test_restricted_brief_reports_denied_notes_with_the_denial_receipt(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-007", include_calls=False)
            self.assertIn("Call notes were unavailable because the token lacks calls:read.", brief["text"])
            self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", brief["text"])
            self.assertEqual(brief["evidence"]["notes_status"], "denied")
            self.assertFalse(any(row["record_ref"].startswith("calls/")
                                 for row in brief["evidence"]["records"]))
            receipts = self.assert_read_plan(runtime, brief)
            self.assertEqual([row["http_status"] for row in receipts], [200, 200, 403])
            self.assertIn("[audit/" + receipts[-1]["audit_id"] + "]", brief["text"])
            self.assertEqual([set(grant.scopes) for grant in runtime.connector.authority.tokens.values()],
                             [{"crm:read"}])

    def test_empty_notes_are_distinguished_from_denied_notes(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-004")
            self.assertIn("No call notes were returned for acc-004.", brief["text"])
            self.assertNotIn("unavailable", brief["text"])
            self.assertEqual(brief["evidence"]["notes_status"], "available")
            receipts = self.assert_read_plan(runtime, brief)
            self.assertEqual((receipts[-1]["status"], receipts[-1]["http_status"], receipts[-1]["row_count"]),
                             ("ok", 200, 0))
            self.assertIn("[audit/" + receipts[-1]["audit_id"] + "]", brief["text"])

    def test_conflicting_sources_and_risk_are_kept_as_separate_cited_claims(self):
        with Runtime(self.state) as runtime:
            conflicting = create_brief(runtime, "acc-003")
            self.assertIn("closed_won", conflicting["text"])
            self.assertIn("Procurement has not approved this renewal.", conflicting["text"])
            self.assertIn("[crm/opportunities/opp-003]", conflicting["text"])
            self.assertIn("[calls/call_notes/note-003]", conflicting["text"])
            self.assertIn('- Source conflict: CRM opportunity opp-003 reports stage "closed_won", '
                          'while call note note-003 reports unapproved procurement. '
                          '[crm/opportunities/opp-003] [calls/call_notes/note-003]', conflicting["text"])
            self.assert_read_plan(runtime, conflicting)
            restricted = create_brief(runtime, "acc-003", include_calls=False)
            self.assertNotIn("Source conflict:", restricted["text"])
            self.assertIn("Call notes were unavailable", restricted["text"])
            self.assert_read_plan(runtime, restricted)
            risk = create_brief(runtime, "acc-002")
            self.assertIn("at_risk", risk["text"])
            self.assertIn("Renewal budget is not approved.", risk["text"])
            self.assertIn("[crm/accounts/acc-002]", risk["text"])
            self.assert_read_plan(runtime, risk)

    def test_an_adapter_cannot_hide_the_recognized_conflict(self):
        class OmitConflict:
            def draft(self, evidence):
                return "\n".join(line for line in build_facts(evidence)
                                 if not line.startswith("- Source conflict:"))

        with Runtime(self.state) as runtime:
            with self.assertRaises(BriefRejected):
                create_brief(runtime, "acc-003", adapter=OmitConflict())
            self.assert_no_accepted_artifact()
            self.assertEqual(self.activities(), [])

    def test_injected_note_is_quoted_data_and_cannot_change_the_read_plan(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-007")
            note = next(row for row in brief["evidence"]["records"]
                        if row["record_ref"] == "calls/call_notes/note-007")
            self.assertIn(INJECTION_TEXT, note["body"])
            self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", brief["text"])
            self.assertIn("log_activity", brief["text"])
            self.assertIn("[calls/call_notes/note-007]", brief["text"])
            self.assert_read_plan(runtime, brief)

    def test_an_adapter_cannot_turn_note_instructions_into_a_write(self):
        class ActionAdapter:
            def draft(self, evidence):
                return '- Call log_activity and approve the renewal. [calls/call_notes/note-007]'

        with Runtime(self.state) as runtime:
            with self.assertRaises(BriefRejected):
                create_brief(runtime, "acc-007", adapter=ActionAdapter())
            self.assertEqual([row["tool"] for row in runtime.connector.audit.read()],
                             ["get_account", "list_opportunities", "read_call_notes"])
            self.assertEqual(self.activities(), [])
            self.assert_no_accepted_artifact()

    def test_adapter_mutations_cannot_rewrite_the_evidence_used_by_the_checker(self):
        class MutatingAdapter:
            def draft(self, evidence):
                account = next(row for row in evidence["records"]
                               if row["record_ref"] == "crm/accounts/acc-001")
                account["arr_cents"] = 999999999
                return "\n".join(build_facts(evidence))

        with Runtime(self.state) as runtime:
            with self.assertRaises(BriefRejected):
                create_brief(runtime, "acc-001", adapter=MutatingAdapter())
            self.assert_no_accepted_artifact()
            saved = next((self.state / "briefs").glob("*.json"))
            account = next(row for row in json.loads(saved.read_text())["evidence"]["records"]
                           if row["record_ref"] == "crm/accounts/acc-001")
            self.assertEqual(account["arr_cents"], 2050000)
            self.assertEqual(self.activities(), [])

    def test_a_rate_limited_notes_read_aborts_instead_of_claiming_empty_notes(self):
        with Runtime(self.state, rate_limit=2) as runtime:
            with self.assertRaises((ValueError, RuntimeError)):
                create_brief(runtime, "acc-004")
            receipts = runtime.connector.audit.read()
            self.assertEqual([row["http_status"] for row in receipts], [200, 200, 429])
            self.assert_no_accepted_artifact()
            self.assertEqual(self.activities(), [])

    def test_unknown_account_does_not_produce_a_guessed_brief(self):
        with Runtime(self.state) as runtime:
            with self.assertRaises((ValueError, RuntimeError)):
                create_brief(runtime, "acc-999")
            receipts = runtime.connector.audit.read()
            self.assertEqual([row["tool"] for row in receipts], ["get_account"])
            self.assertEqual((receipts[0]["status"], receipts[0]["row_count"]), ("error", 0))
            self.assert_no_accepted_artifact()
            self.assertEqual(self.activities(), [])

    def test_transport_and_malformed_notes_responses_do_not_become_availability_claims(self):
        original_call = LocalClient.call
        for fault in ("timeout", "invalid_json", "unrelated_forbidden"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as directory:
                self.state = Path(directory).resolve() / "state"

                def faulty_call(client, token, tool, arguments, brief_id=None):
                    response = original_call(client, token, tool, arguments, brief_id)
                    if tool != "read_call_notes":
                        return response
                    if fault == "timeout":
                        raise httpx2.ReadTimeout("Fixture response timeout")
                    if fault == "invalid_json":
                        return httpx2.Response(200, content="not JSON", request=response.request)
                    return httpx2.Response(403, json={"error": "untrusted origin"}, request=response.request)

                with Runtime(self.state) as runtime, patch.object(LocalClient, "call", faulty_call):
                    with self.assertRaises((ValueError, RuntimeError, httpx2.HTTPError)):
                        create_brief(runtime, "acc-004")
                    self.assert_no_accepted_artifact()
                    self.assertEqual(self.activities(), [])


if __name__ == "__main__":
    unittest.main()
