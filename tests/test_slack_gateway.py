"""Slack uses the same audited gateway with an independent read grant."""

from contextlib import closing
from itertools import combinations
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

from starlette.testclient import TestClient

from gtm_connector import calls, slack, store
from gtm_connector.gateway import Connector
from tests.test_gateway import FixtureAuthority, TOKEN_VALUES, TOOL_SCOPES, VERSION


class SlackGatewayTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()
        self.paths = {name: self.root / f"{name}.sqlite3"
                      for name in ("crm", "calls", "slack", "audit")}
        for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
            with closing(module.connect(self.paths[name])) as conn:
                module.initialize(conn)
                module.seed(conn)
        self.authority = FixtureAuthority()
        self.request_number = 0

    def open_connector(self, **options):
        self.connector = Connector(self.paths["crm"], self.paths["calls"], self.paths["audit"],
                                   self.authority, slack_path=self.paths["slack"], **options)
        self.client = self.enterContext(TestClient(
            self.connector, base_url="https://127.0.0.1:8765", raise_server_exceptions=False))

    def tool(self, name, arguments=None, *, token="full", header_name=None):
        self.request_number += 1
        return self.client.post("/mcp", json={
            "jsonrpc": "2.0", "id": self.request_number, "method": "tools/call",
            "params": {"name": name, "arguments": arguments or {"account_id": "acc-007"},
                       "_meta": {"io.modelcontextprotocol/protocolVersion": VERSION,
                                 "io.modelcontextprotocol/clientCapabilities": {},
                                 "io.modelcontextprotocol/clientInfo": {"name": "slack-tests", "version": "1"},
                                 "gtm.demo/briefId": "brief-slack-source"}},
        }, headers={"Accept": "application/json, text/event-stream",
                    "MCP-Protocol-Version": VERSION, "Mcp-Method": "tools/call",
                    "Mcp-Name": header_name or name,
                    "Authorization": "Bearer " + TOKEN_VALUES.get(token, token)})

    def records(self, response, source="slack"):
        self.assertEqual(response.status_code, 200, response.text)
        result = response.json()["result"]
        self.assertFalse(result.get("isError"), response.text)
        data = result["structuredContent"]
        self.assertEqual(data["source"], source)
        self.assertIs(data["untrusted_data"], True)
        return data["records"]

    def activities(self):
        with closing(store.connect(self.paths["crm"], readonly=True)) as conn:
            return store.counts(conn)["activities"]

    def test_each_read_scope_is_separate_from_other_sources_and_writes(self):
        self.open_connector()
        reads = ("search_accounts", "get_account", "list_opportunities",
                 "read_call_notes", "read_slack_messages")
        for token, scope in (("crm", "crm:read"), ("calls", "calls:read"),
                             ("slack", "slack:read"), ("write", "crm:write")):
            for tool in reads:
                arguments = {"query": "Juniper Crest Learning"} if tool == "search_accounts" else {"account_id": "acc-007"}
                with self.subTest(token=token, tool=tool):
                    response = self.tool(tool, arguments, token=token)
                    if TOOL_SCOPES[tool] == scope:
                        self.assertTrue(self.records(response, scope.split(":")[0]))
                    else:
                        self.assertEqual(response.status_code, 403, response.text)
                        self.assertIn(f'scope="{TOOL_SCOPES[tool]}"', response.headers["WWW-Authenticate"])
                        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", response.text)
                        self.assertNotIn("Juniper Crest Learning", response.text)
            if token != "write":
                response = self.tool("log_activity", {"account_id": "acc-007", "kind": "renewal",
                    "body": "Must not be written", "idempotency_key": "denied-slack-write"}, token=token)
                self.assertEqual(response.status_code, 403)
        self.assertEqual(self.activities(), 0)
        receipts = self.connector.audit.read("brief-slack-source")
        self.assertEqual(len(receipts), 23)
        self.assertEqual(sum(row["status"] == "ok" for row in receipts), 5)
        self.assertTrue(all(row["status"] == "ok" or row["rows"] == [] for row in receipts))

    def test_success_empty_and_denial_each_finalize_one_receipt(self):
        self.open_connector()
        self.assertEqual(self.records(self.tool("read_slack_messages", token="slack"))[0]["record_ref"],
                         "slack/messages/msg-007")
        self.assertEqual(self.records(self.tool("read_slack_messages", {"account_id": "acc-005"}, token="slack")), [])
        self.assertEqual(self.tool("read_slack_messages", token="crm").status_code, 403)
        receipts = self.connector.audit.read("brief-slack-source")
        self.assertEqual([row["tool"] for row in receipts], ["read_slack_messages"] * 3)
        self.assertEqual([row["http_status"] for row in receipts], [200, 200, 403])
        self.assertEqual([row["row_count"] for row in receipts], [1, 0, 0])
        self.assertEqual([row["rows"] for row in receipts], [["slack/messages/msg-007"], [], []])
        self.assertEqual([row["status"] for row in receipts], ["ok", "ok", "error"])
        self.assertEqual(len({row["audit_id"] for row in receipts}), 3)
        for token in TOKEN_VALUES.values():
            self.assertNotIn(token, json.dumps(receipts))

    def test_injected_message_is_raw_data_even_with_an_available_write_grant(self):
        self.open_connector()
        original = self.paths["slack"].read_bytes()
        with patch("gtm_connector.gateway.slack.connect", wraps=slack.connect) as connect:
            messages = self.records(self.tool("read_slack_messages"))
        connect.assert_called_once_with(self.paths["slack"], readonly=True)
        self.assertIn(slack.INJECTION_TEXT, messages[0]["body"])
        self.assertEqual(messages[0]["channel"], "acct-juniper-crest")
        self.assertEqual(self.paths["slack"].read_bytes(), original)
        self.assertEqual(self.activities(), 0)
        self.assertEqual([row["tool"] for row in self.connector.audit.read()], ["read_slack_messages"])

    def test_handler_checks_scope_before_opening_the_slack_database(self):
        self.open_connector()
        handler = self.connector.mcp._tool_manager.get_tool("read_slack_messages").fn
        for token in (None, "crm", "calls", "write"):
            granted = self.authority.tokens[TOKEN_VALUES[token]] if token else None
            with self.subTest(token=token), patch("gtm_connector.gateway.get_access_token", return_value=granted), \
                    patch("gtm_connector.gateway.slack.connect") as connect:
                with self.assertRaisesRegex(PermissionError, "slack:read"):
                    handler("acc-007")
                connect.assert_not_called()

    def test_denied_and_rate_limited_calls_never_open_slack(self):
        self.open_connector(rate_limit=1)
        with patch("gtm_connector.gateway.slack.connect") as connect:
            denied = self.tool("read_slack_messages", token="crm", header_name="get_account")
            limited = self.tool("read_slack_messages", token="crm")
            connect.assert_not_called()
        self.assertEqual(denied.status_code, 403)
        self.assertEqual(limited.status_code, 429)
        self.assertIn("rate_limited", limited.text)
        self.assertGreater(int(limited.headers["Retry-After"]), 0)
        self.assertEqual([row["http_status"] for row in self.connector.audit.read()], [403, 429])
        self.assertEqual(self.activities(), 0)

    def test_slack_read_obeys_audit_start_and_finish_failures(self):
        self.open_connector()
        with patch.object(self.connector.audit, "start", side_effect=sqlite3.OperationalError("unavailable")), \
                patch("gtm_connector.gateway.slack.connect") as connect:
            response = self.tool("read_slack_messages", token="slack")
            connect.assert_not_called()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(self.connector.audit.read(), [])
        with patch.object(self.connector.audit, "finish", side_effect=sqlite3.OperationalError("unavailable")):
            response = self.tool("read_slack_messages", token="slack")
        self.assertEqual(response.status_code, 503)
        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", response.text)
        receipts = self.connector.audit.read()
        self.assertEqual(len(receipts), 1)
        self.assertEqual(receipts[0]["status"], "pending")
        self.assertEqual(self.activities(), 0)

    def test_all_four_resolved_database_paths_must_be_distinct(self):
        default = Connector(self.paths["crm"], self.paths["calls"], self.paths["audit"], self.authority)
        self.assertEqual(default.slack_path, self.paths["slack"])
        for first, second in combinations(self.paths, 2):
            paths = {**self.paths, second: self.paths[first]}
            with self.subTest(first=first, second=second), self.assertRaisesRegex(ValueError, "different database"):
                Connector(paths["crm"], paths["calls"], paths["audit"], self.authority, slack_path=paths["slack"])
        alias = self.root / "alias.sqlite3"
        alias.symlink_to(self.paths["crm"])
        with self.assertRaisesRegex(ValueError, "different database"):
            Connector(self.paths["crm"], self.paths["calls"], self.paths["audit"], self.authority, slack_path=alias)
        self.paths["slack"].unlink()
        self.paths["slack"].symlink_to(self.paths["crm"])
        with self.assertRaisesRegex(ValueError, "different database"):
            Connector(self.paths["crm"], self.paths["calls"], self.paths["audit"], self.authority)


if __name__ == "__main__":
    unittest.main()
