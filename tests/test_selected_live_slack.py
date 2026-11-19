"""Selected mode with the real Slack reader, invented Slack HTTP, and real local HTTPS."""

import json
import os
import unittest
from unittest.mock import patch

from gtm_connector import slack_mapping
from gtm_connector.client import LocalClient
from gtm_connector.runtime import SourceIdentityError
from gtm_connector.selection import load_selection
from gtm_connector.slack_auth import SlackAuthError, disconnect_slack, held_slack_connection
from gtm_connector.slack_mapping import save_slack_mapping
from gtm_connector.slack_reader import SlackThreadReader, ThreadRead
from gtm_connector.slack_workflow import resolve_slack_account
from slack_live_fixtures import ACCOUNT, CHANNEL, LiveSlackFixture
from test_slack_auth import saved_document, seed


class SelectedLiveSlackTests(LiveSlackFixture, unittest.TestCase):
    def selection(self, accounts, name="selection.json"):
        path = self.directory / name
        path.write_text(json.dumps({"schema_version": 1, "accounts": accounts}))
        os.chmod(path, 0o600)
        return load_selection(path, crm_backend="sqlite")

    def test_a_live_slack_brief_needs_its_current_mapping(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            response = owner.call(token, "prepare_renewal_brief", {"account_id": ACCOUNT})
            result = response.json()["result"]
            self.assertFalse(result.get("isError"), result)
            brief = result["structuredContent"]["brief"]
            self.assertEqual(brief["sources"]["slack"], "slack")
            self.assertEqual(brief["slack_status"], "available")
            self.assertEqual(len(self.requests), 1)
            saved = json.loads((self.state / "selected-briefs" / (brief["brief_id"] + ".json")).read_text())
            self.assertEqual((saved["schema_version"], saved["evidence"]["slack_backend"]), (2, "slack"))
            self.assertIn("  Slack: 3 messages were read from one selected thread, and paging completed.",
                          brief["text"])
            self.assertEqual(saved["units"], brief["units"])
            self.assertEqual({unit["speaker"] for unit in brief["units"] if unit["source"] == "slack"
                              and unit["kind"] == "sentence"}, {"U12345678", None})

            reread = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertFalse(reread.get("isError"), reread)
            self.assertEqual(len(self.requests), 1)

            # Remapping the same thread keeps its mapping ID, so the brief stays readable.
            self.clock.advance(60)
            self.map_account(runtime, replace=True)
            same = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertFalse(same.get("isError"), same)
            # A different thread replaces the mapping, so the old brief no longer matches.
            association = resolve_slack_account(runtime, account_id=ACCOUNT)
            save_slack_mapping(runtime.slack_mapping_path, association=association,
                               slack_binding=runtime.slack_binding, replace=True,
                               selection={"channel_id": CHANNEL, "thread_ts": "1700000000.000002"},
                               selected_at=self.clock.now().isoformat().replace("+00:00", "Z"))
            refused = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertTrue(refused["isError"])
            self.assertTrue(refused["content"][0]["text"].endswith("brief_source_changed"))
            self.assertNotIn(brief["text"][:60], json.dumps(refused))
            self.assertEqual(len(self.requests), 1)

    def test_disconnecting_slack_withholds_saved_slack_text(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            brief = owner.call(token, "prepare_renewal_brief",
                               {"account_id": ACCOUNT}).json()["result"]["structuredContent"]["brief"]
            self.assertFalse(owner.call(token, "read_renewal_brief",
                                        {"brief_id": brief["brief_id"]}).json()["result"].get("isError"))
            # A replacement credential whose identity is still unchecked is not enough.
            seed(self.connection, {**saved_document(self.clock.now(), rotating=False),
                                   "credential_state": "identity_unchecked"})
            unchecked = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertTrue(unchecked["isError"])
            self.assertTrue(unchecked["content"][0]["text"].endswith("brief_source_changed"))
            seed(self.connection, saved_document(self.clock.now(), rotating=False))
            self.assertFalse(owner.call(token, "read_renewal_brief",
                                        {"brief_id": brief["brief_id"]}).json()["result"].get("isError"))
            # Removing local credentials makes no provider request.
            disconnect_slack(self.connection)
            refused = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertTrue(refused["isError"])
            self.assertTrue(refused["content"][0]["text"].endswith("brief_source_changed"))
            self.assertEqual(len(self.requests), 1)

    def test_release_decisions_hold_the_connection_lock_and_respect_its_scopes(self):
        broad = {**saved_document(self.clock.now(), rotating=False),
                 "requested_scopes": ["channels:history", "groups:history"],
                 "granted_scopes": ["channels:history", "groups:history"]}
        with self.runtime() as runtime:
            self.map_account(runtime)
        seed(self.connection, broad)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            brief = owner.call(token, "prepare_renewal_brief",
                               {"account_id": ACCOUNT}).json()["result"]["structuredContent"]["brief"]
            saved = json.loads((self.state / "selected-briefs" / (brief["brief_id"] + ".json")).read_text())
            self.assertEqual(saved["slack_scopes"], ["channels:history", "groups:history"])

            # A disconnect attempted while the decision runs cannot land between its checks.
            real_load, attempts = slack_mapping.load_slack_mapping, []

            def disconnect_mid_decision(*args, **kwargs):
                try:
                    disconnect_slack(self.connection)
                except SlackAuthError as exc:
                    attempts.append(exc.code)
                return real_load(*args, **kwargs)

            with patch("gtm_connector.slack_mapping.load_slack_mapping", side_effect=disconnect_mid_decision):
                released = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertFalse(released.get("isError"), released)
            self.assertEqual(attempts, ["slack_connection_busy"])

            with held_slack_connection(self.connection):
                busy = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertTrue(busy["content"][0]["text"].endswith("brief_source_busy"))

            # A narrower reconnect for the same identity no longer covers the saved Slack text.
            seed(self.connection, saved_document(self.clock.now(), rotating=False))
            narrowed = owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}).json()["result"]
            self.assertTrue(narrowed["content"][0]["text"].endswith("brief_source_changed"))
            self.assertEqual(len(self.requests), 1)

    def test_a_caller_without_slack_read_gets_the_denial_whatever_the_connection_state(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            briefs = []
            # Neither a busy nor a disconnected Slack connection is touched for this caller.
            with held_slack_connection(self.connection):
                briefs.append(owner.call(token, "prepare_renewal_brief", {"account_id": ACCOUNT}).json()["result"])
            disconnect_slack(self.connection)
            briefs.append(owner.call(token, "prepare_renewal_brief", {"account_id": ACCOUNT}).json()["result"])
            receipts = runtime.connector.audit.read()
            for result in briefs:
                self.assertFalse(result.get("isError"), result)
                brief = result["structuredContent"]["brief"]
                self.assertEqual(brief["slack_status"], "denied")
                self.assertIn("  Slack: not read. The grant lacks slack:read.", brief["text"])
                slack_reads = [row for row in receipts
                               if row["brief_id"] == brief["brief_id"] and row["tool"] == "read_slack_messages"]
                self.assertEqual([(row["status"], row["http_status"]) for row in slack_reads], [("error", 403)])
            self.assertEqual(self.requests, [])

    def test_a_reader_that_reports_no_scopes_saves_nothing(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        real_read = SlackThreadReader.read_slack_messages

        def without_scopes(reader, account_id):
            read = real_read(reader, account_id)
            return ThreadRead(read.records, read.snapshot)

        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner, \
                patch.object(SlackThreadReader, "read_slack_messages", without_scopes):
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            result = owner.call(token, "prepare_renewal_brief", {"account_id": ACCOUNT}).json()["result"]
        self.assertTrue(result["isError"])
        self.assertTrue(result["content"][0]["text"].endswith("brief_evidence_invalid"))
        self.assertEqual(list((self.state / "selected-briefs").glob("*.json")), [])

    def test_a_prepare_with_slack_read_reports_a_busy_connection(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True}])
        with self.runtime(selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            with held_slack_connection(self.connection):
                result = owner.call(token, "prepare_renewal_brief", {"account_id": ACCOUNT}).json()["result"]
        self.assertTrue(result["isError"])
        self.assertTrue(result["content"][0]["text"].endswith("slack_connection_busy"))
        self.assertEqual(self.requests, [])

    def test_an_unmapped_selected_thread_refuses_startup(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
        selection = self.selection([{"account_id": ACCOUNT, "slack": True},
                                    {"account_id": "acc-012", "slack": True}])
        with self.assertRaises(SourceIdentityError) as caught:
            with self.runtime(selection=selection):
                pass
        self.assertIn("slack map", str(caught.exception))
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()
