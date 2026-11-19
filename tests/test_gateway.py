"""Exercise the connector through HTTP, including failures before tool dispatch."""

import base64
from contextlib import closing
import json
import sqlite3
import tempfile
import time
import unittest
from datetime import datetime
from pathlib import Path
from unittest.mock import patch

from mcp.server.auth.provider import AccessToken
from starlette.testclient import TestClient

from gtm_connector import calls, store
from gtm_connector.gateway import Connector


RESOURCE = "https://127.0.0.1:8765/mcp"
ISSUER = "https://127.0.0.1:8766"
VERSION = "2026-07-28"
TOKEN_VALUES = {
    name: f"fixture-only-{name}-bearer-value"
    for name in ("crm", "calls", "slack", "write", "full", "expired", "wrong_audience")
}
TOOL_SCOPES = {
    "search_accounts": "crm:read",
    "get_account": "crm:read",
    "list_opportunities": "crm:read",
    "read_call_notes": "calls:read",
    "read_slack_messages": "slack:read",
    "log_activity": "crm:write",
}


class FixtureAuthority:
    """An independent verifier: malformed tokens fail; bad claims reach the gateway."""

    issuer = ISSUER
    resource = RESOURCE

    def __init__(self):
        scopes = {
            "crm": ["crm:read"],
            "calls": ["calls:read"],
            "slack": ["slack:read"],
            "write": ["crm:write"],
            "full": ["crm:read", "calls:read", "slack:read", "crm:write"],
            "expired": ["crm:read", "calls:read", "crm:write"],
            "wrong_audience": ["crm:read", "calls:read", "crm:write"],
        }
        self.tokens = {
            TOKEN_VALUES[name]: AccessToken(
                token=TOKEN_VALUES[name],
                client_id="fixture-cli",
                scopes=granted,
                expires_at=int(time.time()) + (-60 if name == "expired" else 3600),
                resource=("https://127.0.0.1:9999/other" if name == "wrong_audience" else RESOURCE),
                subject="local-fixture-owner",
                claims={"iss": ISSUER},
            )
            for name, granted in scopes.items()
        }

    async def verify_token(self, token):
        return self.tokens.get(token)


class GatewayTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.crm_path = self.root / "crm.sqlite3"
        self.calls_path = self.root / "calls.sqlite3"
        self.audit_path = self.root / "audit.sqlite3"
        for module, path in ((store, self.crm_path), (calls, self.calls_path)):
            connection = module.connect(path)
            try:
                module.initialize(connection)
                module.seed(connection)
                connection.commit()
            finally:
                connection.close()
        self.authority = FixtureAuthority()
        self.request_number = 0

    def open_connector(self, **options):
        self.connector = Connector(
            self.crm_path, self.calls_path, self.audit_path, self.authority, **options
        )
        self.client = self.enterContext(TestClient(
            self.connector.app,
            base_url="https://127.0.0.1:8765",
            raise_server_exceptions=False,
        ))

    def rpc(self, method, params=None, *, token="full", headers=None):
        self.request_number += 1
        supplied = {
            "_meta": {
                "io.modelcontextprotocol/protocolVersion": VERSION,
                "io.modelcontextprotocol/clientCapabilities": {},
                "io.modelcontextprotocol/clientInfo": {
                    "name": "gateway-tests", "version": "0.2.0"
                },
                "gtm.demo/briefId": "brief-fixture-001",
            },
            **(params or {}),
        }
        request_headers = {
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": VERSION,
            "Mcp-Method": method,
        }
        if method == "tools/call":
            request_headers["Mcp-Name"] = supplied["name"]
        if token is not None:
            request_headers["Authorization"] = "Bearer " + TOKEN_VALUES.get(token, token)
        for name, value in (headers or {}).items():
            if value is None:
                request_headers.pop(name, None)
            else:
                request_headers[name] = value
        return self.client.post(
            "/mcp",
            json={"jsonrpc": "2.0", "id": self.request_number, "method": method, "params": supplied},
            headers=request_headers,
        )

    def tool(self, name, arguments=None, **options):
        return self.rpc("tools/call", {"name": name, "arguments": arguments or {}}, **options)

    def records(self, response, source):
        self.assertEqual(response.status_code, 200, response.text)
        result = response.json()["result"]
        self.assertEqual(result["resultType"], "complete")
        self.assertFalse(result.get("isError", False), response.text)
        structured = result["structuredContent"]
        self.assertEqual(structured["source"], source)
        self.assertIs(structured["untrusted_data"], True)
        for record in structured["records"]:
            self.assertTrue(record["record_ref"].startswith(source + "/"))
        return structured["records"]

    def activities(self):
        with closing(sqlite3.connect(self.crm_path)) as connection:
            connection.row_factory = sqlite3.Row
            return [dict(row) for row in connection.execute("SELECT * FROM activities ORDER BY id")]

    def activity_arguments(self, **changes):
        return {
            "account_id": "acc-001",
            "kind": "renewal_call",
            "body": "Fixture owner requested a renewal call.",
            "idempotency_key": "fixture-write-001",
            **changes,
        }

    def test_current_discovery_and_tool_scope_declarations(self):
        self.open_connector()
        discovery = self.rpc("server/discover")
        self.assertEqual(discovery.status_code, 200, discovery.text)
        result = discovery.json()["result"]
        self.assertEqual(result["resultType"], "complete")
        self.assertIn(VERSION, result["supportedVersions"])
        self.assertIn("tools", result["capabilities"])
        self.assertIn(result["cacheScope"], ("public", "private"))
        self.assertGreaterEqual(result["ttlMs"], 0)
        listing = self.rpc("tools/list")
        self.assertEqual(listing.status_code, 200, listing.text)
        result = listing.json()["result"]
        self.assertEqual(result["resultType"], "complete")
        self.assertIn(result["cacheScope"], ("public", "private"))
        self.assertGreaterEqual(result["ttlMs"], 0)
        self.assertEqual(
            {tool["name"]: tool["_meta"]["gtm.demo/requiredScope"] for tool in result["tools"]},
            TOOL_SCOPES,
        )
        self.assertEqual(self.connector.audit.read(), [])

    def test_scopes_separate_both_sources_and_writes(self):
        self.open_connector()
        account = self.records(self.tool("get_account", {"account_id": "acc-007"}, token="crm"), "crm")
        self.assertEqual({row["record_ref"] for row in account}, {"crm/accounts/acc-007", "crm/contacts/con-007"})
        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", json.dumps(account))
        for record in account:
            self.assertNotIn("call_notes", record)
            self.assertNotIn("opportunities", record)
        denied = (
            ("read_call_notes", {"account_id": "acc-007"}, "crm", "calls:read"),
            ("get_account", {"account_id": "acc-001"}, "calls", "crm:read"),
            ("search_accounts", {}, "calls", "crm:read"),
            ("list_opportunities", {}, "calls", "crm:read"),
            ("log_activity", self.activity_arguments(), "crm", "crm:write"),
            ("log_activity", self.activity_arguments(), "calls", "crm:write"),
        )
        for name, arguments, token, scope in denied:
            with self.subTest(tool=name, token=token):
                response = self.tool(name, arguments, token=token)
                self.assertEqual(response.status_code, 403, response.text)
                self.assertEqual(response.json()["error"]["data"]["reason"], "insufficient_scope")
                self.assertEqual(response.json()["error"]["code"], 1004)
                challenge = response.headers["WWW-Authenticate"]
                self.assertIn('error="insufficient_scope"', challenge)
                self.assertIn(f'scope="{scope}"', challenge)
                self.assertIn("resource_metadata=", challenge)
                self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", response.text)
        self.assertEqual(self.activities(), [])
        notes = self.records(self.tool("read_call_notes", {"account_id": "acc-001"}, token="calls"), "calls")
        self.assertEqual([row["record_ref"] for row in notes], ["calls/call_notes/note-001"])
        empty = self.records(self.tool("read_call_notes", {"account_id": "acc-004"}, token="calls"), "calls")
        self.assertEqual(empty, [])

    def test_routing_header_cannot_disguise_a_call_note_request(self):
        self.open_connector()
        response = self.tool("read_call_notes", {"account_id": "acc-007"}, token="crm", headers={"Mcp-Name": "get_account"})
        self.assertIn(response.status_code, (400, 403), response.text)
        self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", response.text)
        row = self.connector.audit.read()[0]
        self.assertEqual(row["row_count"], 0)
        self.assertEqual(row["rows"], [])
        self.assertEqual(self.activities(), [])

    def test_expired_wrong_audience_missing_and_malformed_tokens_are_unauthorized(self):
        self.open_connector()
        tokens = ("expired", "wrong_audience", None, "unknown-fixture-token", "not.a.valid.token")
        for token in tokens:
            with self.subTest(token=token):
                response = self.tool("get_account", {"account_id": "acc-001"}, token=token)
                self.assertEqual(response.status_code, 401, response.text)
                self.assertEqual(response.json()["error"]["data"]["reason"], "invalid_token")
                self.assertEqual(response.json()["error"]["code"], 1001)
                self.assertIn('error="invalid_token"', response.headers["WWW-Authenticate"])
                self.assertIn("resource_metadata=", response.headers["WWW-Authenticate"])
                self.assertNotIn("Alder Vale Packaging", response.text)
                if token is not None:
                    self.assertNotIn(TOKEN_VALUES.get(token, token), response.text)
        self.assertEqual(len(self.connector.audit.read()), len(tokens))
        self.assertTrue(all(row["row_count"] == 0 for row in self.connector.audit.read()))

    def test_duplicate_authorization_headers_have_the_same_invalid_token_reason(self):
        self.open_connector()
        response = self.client.post("/mcp", json={
            "jsonrpc": "2.0", "id": 1, "method": "tools/call",
            "params": {"name": "get_account", "arguments": {"account_id": "acc-001"}},
        }, headers=[
            ("Mcp-Method", "tools/call"),
            ("Authorization", "Bearer " + TOKEN_VALUES["crm"]),
            ("Authorization", "Bearer " + TOKEN_VALUES["full"]),
        ])
        self.assertEqual(response.status_code, 401)
        self.assertEqual(response.json()["error"]["data"]["reason"], "invalid_token")
        self.assertEqual(response.json()["error"]["code"], 1001)
        self.assertIn('error="invalid_token"', response.headers["WWW-Authenticate"])
        self.assertNotIn("Alder Vale Packaging", response.text)
        for token in TOKEN_VALUES.values():
            self.assertNotIn(token, response.text)
        receipts = self.connector.audit.read()
        self.assertEqual(len(receipts), 1)
        self.assertEqual((receipts[0]["http_status"], receipts[0]["rows"]), (401, []))
        self.assertEqual(self.activities(), [])

    def test_every_tool_attempt_has_one_complete_audit_row(self):
        self.open_connector()
        attempts = [
            ("search_accounts", {"query": "Alder Vale Packaging"}, "crm", None),
            ("get_account", {"account_id": "acc-001"}, "crm", None),
            ("list_opportunities", {"account_id": "acc-003"}, "crm", None),
            ("read_call_notes", {"account_id": "acc-004"}, "calls", None),
            ("log_activity", self.activity_arguments(), "write", None),
            ("read_call_notes", {"account_id": "acc-007"}, "crm", None),
            ("get_account", {"account_id": "acc-001"}, "expired", None),
            ("unknown_fixture_tool", {}, "full", None),
            ("get_account", {}, "crm", None),
            ("get_account", {"account_id": "acc-001"}, "full", {"Mcp-Name": "search_accounts"}),
        ]
        for number, (name, arguments, token, headers) in enumerate(attempts, 1):
            response = self.tool(name, arguments, token=token, headers=headers)
            with self.subTest(attempt=number, tool=name):
                if number <= 5:
                    records = self.records(response, "calls" if name == "read_call_notes" else "crm")
                    if number <= 3:
                        expected = (
                            ["crm/accounts/acc-001"],
                            ["crm/accounts/acc-001", "crm/contacts/con-001"],
                            ["crm/opportunities/opp-003"],
                        )[number - 1]
                        self.assertEqual([record["record_ref"] for record in records], expected)
                elif number in (6, 7, 9, 10):
                    self.assertEqual(response.status_code, {6: 403, 7: 401, 9: 400, 10: 400}[number])
                else:
                    self.assertTrue("error" in response.json() or response.json().get("result", {}).get("isError"))
                rows = self.connector.audit.read("brief-fixture-001")
                self.assertEqual(len(rows), number)
                row = rows[-1]
                self.assertEqual(row["tool"], name)
                self.assertEqual(row["arguments"], arguments)
                self.assertEqual(row["request_id"], number)
                self.assertEqual(row["brief_id"], "brief-fixture-001")
                self.assertEqual(row["http_status"], response.status_code)
                self.assertNotEqual(row["status"], "pending")
                self.assertGreaterEqual(row["elapsed_ms"], 0)
                datetime.fromisoformat(row["started_at"].replace("Z", "+00:00"))
                payload = response.json()
                result = payload.get("result", {})
                expected_rows = [record["record_ref"] for record in result.get("structuredContent", {}).get("records", [])]
                self.assertEqual(row["row_count"], len(expected_rows))
                self.assertEqual(row["rows"], expected_rows)
                if "result" in payload:
                    self.assertEqual(result["_meta"]["gtm.demo/auditId"], row["audit_id"])
        rows = self.connector.audit.read()
        self.assertEqual(len({row["audit_id"] for row in rows}), len(attempts))
        self.assertEqual(rows[0]["token_id"], rows[1]["token_id"])
        self.assertNotEqual(rows[0]["token_id"], rows[3]["token_id"])
        self.assertEqual(self.connector.audit.read("brief-unrelated"), [])
        serialized = json.dumps(rows)
        for token in TOKEN_VALUES.values():
            self.assertNotIn(token, serialized)
            self.assertNotIn(token.encode(), self.audit_path.read_bytes())

    def test_idempotent_write_returns_one_activity_and_rejects_changed_payload(self):
        self.open_connector()
        arguments = self.activity_arguments()
        first = self.records(self.tool("log_activity", arguments, token="write"), "crm")
        second = self.records(self.tool("log_activity", arguments, token="write"), "crm")
        self.assertEqual(first, second)
        self.assertEqual(len(first), 1)
        self.assertEqual(len(self.activities()), 1)
        conflict = self.tool("log_activity", {**arguments, "body": "Changed fixture payload."}, token="write")
        self.assertEqual(conflict.status_code, 200, conflict.text)
        self.assertIs(conflict.json()["result"]["isError"], True)
        self.assertEqual(self.activities()[0]["body"], arguments["body"])
        self.assertEqual(len(self.activities()), 1)
        self.assertEqual(len(self.connector.audit.read()), 3)

    def test_rate_limit_counts_scope_denials_is_per_token_and_resets(self):
        clock = [100.0]
        self.open_connector(rate_limit=2, window_seconds=60, clock=lambda: clock[0])
        denied = self.tool("read_call_notes", {"account_id": "acc-001"}, token="crm")
        self.assertEqual(denied.status_code, 403)
        self.records(self.tool("search_accounts", token="crm"), "crm")
        limited = self.tool("get_account", {"account_id": "acc-001"}, token="crm")
        self.assertEqual(limited.status_code, 429, limited.text)
        self.assertEqual(limited.json()["error"]["data"]["reason"], "rate_limited")
        self.assertEqual(limited.json()["error"]["code"], 1002)
        self.assertGreater(int(limited.headers["Retry-After"]), 0)
        self.assertRegex(limited.text.lower(), "rate.?limit")
        self.records(self.tool("read_call_notes", {"account_id": "acc-001"}, token="calls"), "calls")
        clock[0] += 61
        self.records(self.tool("get_account", {"account_id": "acc-001"}, token="crm"), "crm")
        rows = self.connector.audit.read()
        self.assertEqual([row["http_status"] for row in rows], [403, 200, 429, 200, 200])
        self.assertEqual(rows[2]["rows"], [])

    def test_duplicate_write_attempts_also_consume_rate_limit(self):
        self.open_connector(rate_limit=1)
        arguments = self.activity_arguments()
        self.records(self.tool("log_activity", arguments, token="write"), "crm")
        retry = self.tool("log_activity", arguments, token="write")
        self.assertEqual(retry.status_code, 429, retry.text)
        self.assertEqual(len(self.activities()), 1)
        self.assertEqual(len(self.connector.audit.read()), 2)

    def test_injected_note_remains_data_until_an_explicit_write(self):
        self.open_connector()
        notes = self.records(self.tool("read_call_notes", {"account_id": "acc-007"}), "calls")
        self.assertIn(calls.INJECTION_TEXT, notes[0]["body"])
        self.assertEqual(self.activities(), [])
        self.assertEqual([row["tool"] for row in self.connector.audit.read()], ["read_call_notes"])
        self.records(self.tool("log_activity", self.activity_arguments(account_id="acc-007")), "crm")
        activities = self.activities()
        self.assertEqual(len(activities), 1)
        self.assertEqual(activities[0]["body"], "Fixture owner requested a renewal call.")
        self.assertNotEqual(activities[0]["idempotency_key"], "seed-injection")

    def test_header_mismatches_are_rejected_and_audited(self):
        self.open_connector()
        cases = (
            {"Mcp-Method": "tools/list"},
            {"Mcp-Name": "search_accounts"},
            {"Mcp-Name": None},
            {"MCP-Protocol-Version": None},
            {"MCP-Protocol-Version": "2025-11-25"},
        )
        for headers in cases:
            with self.subTest(headers=headers):
                response = self.tool("get_account", {"account_id": "acc-001"}, headers=headers)
                self.assertEqual(response.status_code, 400, response.text)
                self.assertEqual(response.json()["error"]["code"], -32020)
                if "MCP-Protocol-Version" in headers:
                    self.assertEqual(response.json()["error"]["data"]["reason"], "protocol_version_mismatch")
                self.assertNotIn("Alder Vale Packaging", response.text)
        self.assertEqual(len(self.connector.audit.read()), len(cases))
        encoded = "=?base64?" + base64.b64encode(b"get_account").decode() + "?="
        self.records(self.tool("get_account", {"account_id": "acc-001"}, headers={"Mcp-Name": encoded}), "crm")

    def test_hostile_browser_origin_is_rejected_without_data(self):
        self.open_connector()
        response = self.tool("get_account", {"account_id": "acc-001"}, headers={"Origin": "https://untrusted.example"})
        self.assertEqual(response.status_code, 403, response.text)
        self.assertEqual(response.json()["error"]["data"]["reason"], "untrusted_origin_or_host")
        self.assertEqual(response.json()["error"]["code"], 1003)
        self.assertNotIn("Alder Vale Packaging", response.text)
        self.assertEqual(len(self.connector.audit.read()), 1)
        self.assertEqual(self.connector.audit.read()[0]["rows"], [])

    def test_audit_start_failure_prevents_writes(self):
        self.open_connector()
        with patch.object(self.connector.audit, "start", side_effect=sqlite3.OperationalError("fixture audit unavailable")):
            response = self.tool("log_activity", self.activity_arguments(), token="write")
        self.assertEqual(response.status_code, 503, response.text)
        self.assertEqual(response.json(), {"error": "audit_unavailable; call was not executed"})
        self.assertEqual(self.activities(), [])

    def test_deeply_nested_marked_request_is_rejected_and_audited(self):
        self.open_connector()
        # Older supported Python parsers raise RecursionError here; 3.14 can
        # parse it. Both paths must reject this non-object and keep one receipt.
        response = self.client.post("/mcp", content="[" * 10000 + "0" + "]" * 10000,
                                    headers={"Mcp-Method": "tools/call", "Mcp-Name": "get_account"})
        self.assertEqual(response.status_code, 400)
        rows = self.connector.audit.read()
        self.assertEqual(len(rows), 1)
        self.assertEqual((rows[0]["tool"], rows[0]["status"], rows[0]["http_status"], rows[0]["row_count"]),
                         ("get_account", "error", 400, 0))
        self.assertEqual(self.activities(), [])

    def test_a_tool_call_the_strict_parser_refuses_keeps_one_receipt(self):
        self.open_connector()
        for body in ('{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account",'
                     '"arguments":{"account_id":"acc-001","weight":1e999}}}',
                     '{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/call","params":{}}'):
            with self.subTest(body=body[:40]):
                before = len(self.connector.audit.read())
                response = self.client.post("/mcp", content=body, headers={"Authorization": "Bearer unused"})
                self.assertEqual(response.status_code, 400)
                self.assertEqual(response.json()["error"]["data"]["reason"], "invalid_json")
                rows = self.connector.audit.read()
                self.assertEqual(len(rows), before + 1)
                self.assertEqual((rows[-1]["status"], rows[-1]["http_status"], rows[-1]["row_count"]), ("error", 400, 0))
                self.assertIn("unparsed_sha256", rows[-1]["arguments"])
        self.assertEqual(self.activities(), [])

    def test_unknown_account_write_returns_an_explicit_audited_tool_error(self):
        self.open_connector()
        response = self.tool("log_activity", self.activity_arguments(account_id="acc-missing"), token="write")
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.json()["result"]["isError"])
        self.assertIn("account not found", response.json()["result"]["content"][0]["text"])
        self.assertEqual(self.activities(), [])
        rows = self.connector.audit.read()
        self.assertEqual(len(rows), 1)
        self.assertEqual((rows[0]["status"], rows[0]["row_count"]), ("error", 0))

    def test_audit_finish_failure_withholds_data(self):
        self.open_connector()
        with patch.object(self.connector.audit, "finish", side_effect=sqlite3.OperationalError("fixture audit unavailable")):
            response = self.tool("get_account", {"account_id": "acc-001"}, token="crm")
        self.assertEqual(response.status_code, 503, response.text)
        self.assertEqual(response.json(), {"error": "execution_or_audit_unavailable; no result released"})
        self.assertNotIn("Alder Vale Packaging", response.text)
        self.assertEqual(self.activities(), [])

    def test_write_committed_before_audit_failure_is_reused_on_retry(self):
        self.open_connector()
        arguments = self.activity_arguments()
        with patch.object(self.connector.audit, "finish", side_effect=sqlite3.OperationalError("fixture audit unavailable")):
            response = self.tool("log_activity", arguments, token="write")
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json(), {"error": "execution_or_audit_unavailable; no result released"})
        self.assertEqual(len(self.activities()), 1)
        original_id = self.activities()[0]["id"]
        pending = self.connector.audit.read()
        self.assertEqual(len(pending), 1)
        self.assertEqual(pending[0]["status"], "pending")
        retried = self.records(self.tool("log_activity", arguments, token="write"), "crm")
        self.assertEqual(retried[0]["id"], original_id)
        self.assertEqual(len(self.activities()), 1)
        receipts = self.connector.audit.read()
        self.assertEqual([row["status"] for row in receipts], ["pending", "ok"])
        self.assertEqual(receipts[1]["rows"], ["crm/activities/" + original_id])


if __name__ == "__main__":
    unittest.main()
