"""Actual scoped HTTPS briefs with invented Slack responses and immutable evidence."""

from contextlib import closing
from copy import deepcopy
import json
import sqlite3
import unittest
from unittest.mock import patch

from gtm_connector.briefing import check_saved_brief, create_brief
from gtm_connector.citations import BriefRejected, check_brief
from gtm_connector.slack_reader import SlackReadError
from slack_live_fixtures import ACCOUNT, INJECTED, OMITTED, ROWS, LiveSlackFixture
import test_salesforce_environments as sf_fixtures


class LiveSlackBriefingTests(LiveSlackFixture, unittest.TestCase):
    def test_exact_quotes_unknowns_and_injection_cross_the_actual_gateway(self):
        with self.runtime(calls_backend="imported") as runtime:
            self.map_account(runtime)
            self.import_call(runtime)
            artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
            self.assertEqual(artifact["status"], "accepted")
            self.assertEqual(artifact["adapter"], "extractive")
            self.assertEqual(artifact["sources"], {"crm": "demo_fixture", "calls": "imported_transcript", "slack": "slack"})
            self.assertEqual(artifact["model_usage"], {"model": None, "input_tokens": 0, "output_tokens": 0})
            evidence = artifact["evidence"]
            self.assertEqual(evidence["workflow_version"], 5)
            self.assertEqual(evidence["crm_binding"], evidence["calls_binding"])
            self.assertEqual(evidence["thread_snapshot"]["counts"]["messages"], 3)
            for row in ROWS:
                self.assertIn("says " + json.dumps(row["text"], ensure_ascii=True) + ".", artifact["text"])
            self.assertIn("3 messages returned across 1 page", artifact["text"])
            self.assertIn("previous edits and deletions are unknown", artifact["text"])
            self.assertIn("content may have changed during paging", artifact["text"])
            self.assertIn("Omitted entry counts are files 1, attachments 1, and blocks 1", artifact["text"])
            self.assertIn("Slack non-text content was omitted from evidence", artifact["text"])
            self.assertIn("No separate requests fetched non-text resources", artifact["text"])
            self.assertNotIn("Slack non-text content was not fetched", artifact["text"])
            self.assertIn('from "Slack app"', artifact["text"])
            self.assertIn('from "Unknown Slack author"', artifact["text"])
            self.assertIn("Edit metadata was not reported", artifact["text"])
            self.assertIn('Slack reports an edit at "1700000001.000003"', artifact["text"])
            self.assertIn("call time not provided", artifact["text"])
            self.assertNotIn("\x1b", artifact["text"])
            self.assertNotIn(OMITTED, json.dumps(artifact))
            self.assertEqual(len(self.requests), 1)
            self.assertEqual([row["tool"] for row in evidence["receipts"]],
                             ["get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
            self.assertTrue(all(row["status"] == "ok" for row in evidence["receipts"]))
            self.assertEqual(len(runtime.connector.audit.read()), 6)
            with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection:
                self.assertEqual(connection.execute("SELECT count(*) FROM activities").fetchone()[0], 0)
        with patch("sqlite3.connect", side_effect=AssertionError("Saved checks must remain offline")), \
                patch("gtm_connector.slack_reader.get_slack_access", side_effect=AssertionError("No credentials")):
            self.assertEqual(check_saved_brief(self.state, artifact["brief_id"], artifact["text"]), artifact["text"])

    def test_quotes_and_limit_disclosures_are_required(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
            artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
        for prefix in ("- Slack observation:", "- Slack non-text content", "- Untrusted Slack message"):
            candidate = "\n".join(line for line in artifact["text"].splitlines() if not line.startswith(prefix))
            with self.subTest(prefix=prefix), self.assertRaises(BriefRejected):
                check_brief(candidate, artifact["evidence"])
        candidate = artifact["text"].replace("earlier edits are unknown", "the message was never edited")
        with self.assertRaises(BriefRejected):
            check_brief(candidate, artifact["evidence"])
        candidate = artifact["text"].replace("was omitted from evidence", "was not fetched")
        with self.assertRaises(BriefRejected):
            check_brief(candidate, artifact["evidence"])
        candidate = artifact["text"] + "- The app approved the renewal.\n"
        with self.assertRaises(BriefRejected):
            check_brief(candidate, artifact["evidence"])

    def test_scope_denial_has_no_snapshot_credentials_or_provider_work(self):
        with self.runtime() as runtime:
            # No mapping exists. The scope guard must still decide before opening it.
            with patch("gtm_connector.slack_reader.get_slack_access", side_effect=AssertionError("No credentials")), \
                    patch("gtm_connector.slack_mapping.load_slack_mapping", side_effect=AssertionError("No mapping")):
                artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True, grant_slack=False)
            self.assertEqual(artifact["evidence"]["slack_status"], "denied")
            self.assertIsNone(artifact["evidence"]["thread_snapshot"])
            self.assertEqual(self.requests, [])
            self.assertEqual(self.source_events, [])
            self.assertEqual(artifact["evidence"]["receipts"][-1]["http_status"], 403)
            self.assertIn("token lacks slack:read", artifact["text"])
            self.assertFalse(any(row["record_ref"].startswith("slack/") for row in artifact["evidence"]["records"]))

    def test_provider_failure_aborts_instead_of_publishing_optional_empty_evidence(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
            self.reply = {"ok": False, "error": "channel_not_found", "private_detail": OMITTED}
            with self.assertRaises(SlackReadError) as failure:
                create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
            self.assertEqual(failure.exception.code, "slack_thread_unavailable")
            self.assertFalse((self.state / "briefs").exists())
            receipt = runtime.connector.audit.read()[-1]
            self.assertEqual((receipt["status"], receipt["rows"]), ("error", []))
            self.assertNotIn(OMITTED, str(failure.exception))

    def test_isolated_snapshot_binding_receipt_and_version_mutations_are_rejected(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
            artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
        mutations = (
            lambda value: value["thread_snapshot"].update(pagination_complete=False),
            lambda value: value["thread_snapshot"]["mapping"].update(channel_id="C99999999"),
            lambda value: value["thread_snapshot"]["counts"].update(omitted_files=0),
            lambda value: value["slack_binding"].update(identity_sha256="0" * 64),
            lambda value: value["crm_binding"].update(identity_sha256="0" * 64),
            lambda value: value["receipts"][-1].update(rows=[], row_count=0),
            lambda value: value.update(workflow_version=2),
            lambda value: value.update(slack_status="denied"),
            lambda value: value.update(unrequested_original_text=OMITTED),
        )
        for mutation in mutations:
            evidence = deepcopy(artifact["evidence"])
            mutation(evidence)
            with self.subTest(mutation=mutations.index(mutation)), self.assertRaises(BriefRejected):
                check_brief(artifact["text"], evidence)
        for field, value in (("body", "Invented altered statement."), ("author", "Verified account owner")):
            evidence = deepcopy(artifact["evidence"])
            message = next(row for row in evidence["records"] if row["record_ref"].startswith("slack/"))
            message[field] = value
            with self.subTest(field=field), self.assertRaises(BriefRejected):
                check_brief(artifact["text"], evidence)
        evidence = deepcopy(artifact["evidence"])
        evidence.update(workflow_version=2)
        for field in ("crm_binding", "slack_binding", "slack_backend", "thread_snapshot"):
            evidence.pop(field)
        with self.assertRaises(BriefRejected):
            check_brief(artifact["text"], evidence)

    def test_historical_brief_keeps_its_association_after_explicit_reassignment(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
            artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
            self.map_account(runtime, "acc-008", reassign=True)
            with self.assertRaises(SlackReadError) as failure:
                create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
            self.assertEqual(failure.exception.code, "slack_mapping_missing")
        with patch("gtm_connector.slack_mapping.load_slack_mapping", side_effect=AssertionError("No current mapping")):
            self.assertEqual(check_saved_brief(self.state, artifact["brief_id"], artifact["text"]), artifact["text"])

    def test_salesforce_fixture_imported_call_and_selected_slack_share_one_brief(self):
        fixture = sf_fixtures.SalesforceEnvironmentTests()
        fixture.setUp()
        with self.runtime(crm_reader=fixture.adapter("production"), calls_backend="imported") as runtime:
            self.map_account(runtime, sf_fixtures.ACCOUNT_ID)
            self.import_call(runtime, sf_fixtures.ACCOUNT_ID)
            artifact = create_brief(runtime, sf_fixtures.ACCOUNT_ID, as_of="2027-01-15", include_slack=True)
            self.assertEqual(artifact["status"], "accepted")
            self.assertEqual(artifact["sources"], {"crm": "salesforce", "calls": "imported_transcript", "slack": "slack"})
            self.assertIn("Contract end date: 2027-01-31.", artifact["text"])
            self.assertIn(json.dumps(INJECTED, ensure_ascii=True), artifact["text"])
            self.assertEqual(len(artifact["evidence"]["receipts"]), 4)
            self.assertTrue(all(request.method == "GET" for request in fixture.requests))
            self.assertEqual(len(self.requests), 1)
            self.assertTrue(all(row["tool"] != "log_activity" for row in runtime.connector.audit.read()))


if __name__ == "__main__":
    unittest.main()
