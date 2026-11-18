"""Selected mode with the real Slack reader, invented Slack HTTP, and real local HTTPS."""

import json
import os
import unittest

from gtm_connector.client import LocalClient
from gtm_connector.runtime import SourceIdentityError
from gtm_connector.selection import load_selection
from gtm_connector.slack_mapping import save_slack_mapping
from gtm_connector.slack_workflow import resolve_slack_account
from slack_live_fixtures import ACCOUNT, CHANNEL, LiveSlackFixture


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
            self.assertEqual(saved["evidence"]["workflow_version"], 5)

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
