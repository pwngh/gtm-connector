"""Observe real gateway attempts without exposing sources or changing durable receipts."""

from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

from starlette.testclient import TestClient

from gtm_connector import calls, store
from gtm_connector.client import LocalClient
from gtm_connector.gateway import Connector
from gtm_connector.runtime import Runtime
from tests.test_gateway import FixtureAuthority, TOKEN_VALUES, VERSION


class CallEventTests(unittest.TestCase):
    def setUp(self):
        directory = self.enterContext(tempfile.TemporaryDirectory())
        self.root = Path(directory)
        for module, name in ((store, "crm"), (calls, "calls")):
            with closing(module.connect(self.root / f"{name}.sqlite3")) as connection:
                module.initialize(connection)
                module.seed(connection)
                connection.commit()
        self.events = []
        self.observed_receipts = []
        self.connector = Connector(self.root / "crm.sqlite3", self.root / "calls.sqlite3",
                                   self.root / "audit.sqlite3", FixtureAuthority(), on_call=self.observe)
        self.client = self.enterContext(TestClient(self.connector, base_url="https://127.0.0.1:8765",
                                                   raise_server_exceptions=False))

    def observe(self, event):
        self.events.append(event)
        # This reader uses its own SQLite connection, so an uncommitted finish is invisible.
        self.observed_receipts.append(self.connector.audit.read())

    def call(self, tool="get_account", arguments=None, *, token="crm", headers=None):
        supplied_headers = {
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": VERSION,
            "Mcp-Method": "tools/call", "Mcp-Name": tool,
        }
        if token is not None:
            supplied_headers["Authorization"] = "Bearer " + TOKEN_VALUES.get(token, token)
        supplied_headers.update(headers or {})
        return self.client.post("/mcp", json={
            "jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": {
                "name": tool, "arguments": {"account_id": "acc-001"} if arguments is None else arguments,
                "_meta": {"io.modelcontextprotocol/protocolVersion": VERSION,
                          "io.modelcontextprotocol/clientCapabilities": {}},
            },
        }, headers=supplied_headers)

    def assert_event(self, decision, reason, *, receipt_status="error"):
        self.assertEqual(len(self.events), 1)
        event = self.events[0]
        self.assertEqual(set(event), {"type", "time", "tool", "caller", "decision",
                                     "duration_ms", "receipt_id", "reason"})
        self.assertEqual(event["type"], "tool_call")
        self.assertEqual((event["decision"], event["reason"]), (decision, reason))
        self.assertTrue(event["time"].endswith("Z"))
        self.assertEqual(datetime.fromisoformat(event["time"]).tzinfo, timezone.utc)
        self.assertGreaterEqual(event["duration_ms"], 0)
        if receipt_status is None:
            self.assertIsNone(event["receipt_id"])
            self.assertEqual(self.observed_receipts, [[]])
        else:
            self.assertEqual(len(self.observed_receipts[0]), 1)
            receipt = self.observed_receipts[0][0]
            self.assertEqual(receipt["status"], receipt_status)
            self.assertEqual(event["receipt_id"], receipt["audit_id"])
            self.assertEqual(event["caller"], receipt["token_id"])
        return event

    def test_allowed_call_is_observed_after_receipt_commit_without_source_data(self):
        response = self.call()
        self.assertEqual(response.status_code, 200, response.text)
        event = self.assert_event("allowed", None, receipt_status="ok")
        result = response.json()["result"]
        self.assertEqual(event["receipt_id"], result["_meta"]["gtm.demo/auditId"])
        self.assertEqual(event["tool"], "get_account")
        self.assertEqual(event["caller"], hashlib.sha256(TOKEN_VALUES["crm"].encode()).hexdigest())
        serialized = json.dumps(event)
        for secret in (TOKEN_VALUES["crm"], "acc-001", "Bearer", "Synthetic"):
            self.assertNotIn(secret, serialized)

    def test_refusals_include_fixed_reason_and_same_caller_as_audit(self):
        cases = [
            ({"tool": "read_call_notes"}, "insufficient_scope", 403),
            ({"token": "invented-invalid-secret"}, "invalid_token", 401),
            ({"token": None}, "invalid_token", 401),
            ({"token": "expired"}, "invalid_token", 401),
            ({"token": "wrong_audience"}, "invalid_token", 401),
            ({"headers": {"Authorization": "Bearer invented-malformed secret"}}, "invalid_token", 401),
            ({"arguments": {"account_id": "private argument that must not enter the event", "extra": True}},
             "invalid_arguments", 400),
        ]
        for options, reason, status in cases:
            with self.subTest(options=options):
                before = len(self.events)
                response = self.call(**options)
                self.assertEqual(response.status_code, status, response.text)
                self.assertEqual(len(self.events), before + 1)
                event = self.events[-1]
                self.assertEqual((event["decision"], event["reason"]), ("refused", reason))
                receipt = self.observed_receipts[-1][-1]
                self.assertEqual((event["receipt_id"], event["caller"]),
                                 (receipt["audit_id"], receipt["token_id"]))
                self.assertEqual(receipt["status"], "error")
                if options.get("token", "crm") is None:
                    self.assertEqual(event["caller"], "anonymous")
        serialized = json.dumps(self.events)
        for secret in (*TOKEN_VALUES.values(), "invented-invalid-secret", "invented-malformed",
                       "private argument", "Bearer"):
            self.assertNotIn(secret, serialized)

    def test_tool_error_does_not_copy_error_text_or_arguments(self):
        response = self.call("log_activity", {
            "account_id": "acc-missing", "kind": "renewal_call",
            "body": "invented-private-note", "idempotency_key": "invented-private-key",
        }, token="write")
        self.assertEqual(response.status_code, 200, response.text)
        self.assertTrue(response.json()["result"]["isError"])
        event = self.assert_event("failed", "tool_error")
        for value in ("invented-private-note", "invented-private-key", "account not found"):
            self.assertNotIn(value, json.dumps(event))

    def test_marked_unparseable_attempt_emits_one_refusal(self):
        response = self.client.post("/mcp", content=b"{invented-private-invalid-json", headers={
            "Mcp-Method": "tools/call", "Mcp-Name": "get_account",
            "Authorization": "Bearer " + TOKEN_VALUES["crm"],
        })
        self.assertEqual(response.status_code, 400)
        event = self.assert_event("refused", "invalid_json")
        self.assertEqual(event["tool"], "get_account")
        self.assertNotIn("invented-private-invalid-json", json.dumps(event))

    def test_unknown_tool_emits_one_failure(self):
        response = self.call("invented_unknown_tool", {})
        self.assertEqual(response.status_code, 200, response.text)
        self.assertTrue(response.json()["result"]["isError"])
        event = self.assert_event("failed", "tool_error")
        self.assertEqual(event["tool"], "<unknown>")
        self.assertNotIn("invented_unknown_tool", json.dumps(event))

    def test_unknown_header_tool_does_not_leak_into_stream(self):
        response = self.client.post("/mcp", content=b"{", headers={
            "Mcp-Method": "tools/call", "Mcp-Name": "invented-private-secret",
            "Authorization": "Bearer " + TOKEN_VALUES["crm"],
        })
        self.assertEqual(response.status_code, 400)
        event = self.assert_event("refused", "invalid_json")
        self.assertEqual(event["tool"], "<unknown>")
        self.assertNotIn("invented-private-secret", json.dumps(event))

    def test_audit_start_failure_emits_once_without_receipt_or_tool_execution(self):
        with patch.object(self.connector.audit, "start", side_effect=sqlite3.OperationalError("private failure")), \
                patch("gtm_connector.gateway.store.get_account") as handler:
            response = self.call()
        handler.assert_not_called()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json(), {"error": "audit_unavailable; call was not executed"})
        event = self.assert_event("failed", "audit_unavailable", receipt_status=None)
        self.assertNotIn("private failure", json.dumps(event))

    def test_execution_failure_emits_once_after_error_receipt_commit(self):
        async def broken_app(*_):
            raise RuntimeError("private execution failure")

        with patch.object(self.connector, "sdk_app", broken_app):
            response = self.call()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json(), {"error": "execution_or_audit_unavailable; no result released"})
        event = self.assert_event("failed", "execution_or_audit_failure")
        self.assertNotIn("private execution failure", json.dumps(event))

    def test_audit_finish_failure_emits_once_and_preserves_pending_receipt(self):
        with patch.object(self.connector.audit, "finish", side_effect=sqlite3.OperationalError("private failure")):
            response = self.call()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json(), {"error": "execution_or_audit_unavailable; no result released"})
        self.assert_event("failed", "execution_or_audit_failure", receipt_status="pending")

    def test_recovered_error_receipt_is_committed_before_single_failure_event(self):
        finish = self.connector.audit.finish
        attempts = []

        def fail_once(*args, **kwargs):
            attempts.append(kwargs["status"])
            if len(attempts) == 1:
                raise sqlite3.OperationalError("invented first finish failure")
            return finish(*args, **kwargs)

        with patch.object(self.connector.audit, "finish", side_effect=fail_once):
            response = self.call()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(attempts, ["ok", "error"])
        self.assert_event("failed", "execution_or_audit_failure")

    def test_duration_includes_both_audit_writes_without_stopwatch_threshold(self):
        ticks = [100.0]
        start, finish = self.connector.audit.start, self.connector.audit.finish

        def audit_start(*args, **kwargs):
            result = start(*args, **kwargs)
            ticks[0] += 2.0
            return result

        def audit_finish(*args, **kwargs):
            result = finish(*args, **kwargs)
            ticks[0] += 3.0
            return result

        with patch("gtm_connector.gateway.time.perf_counter", side_effect=lambda: ticks[0]), \
                patch.object(self.connector.audit, "start", side_effect=audit_start), \
                patch.object(self.connector.audit, "finish", side_effect=audit_finish):
            response = self.call()
        self.assertEqual(response.status_code, 200)
        event = self.assert_event("allowed", None, receipt_status="ok")
        self.assertEqual(event["duration_ms"], 5000.0)

    def test_observer_exception_does_not_change_results_or_receipts(self):
        observed = []

        def broken_observer(event):
            observed.append(event)
            raise RuntimeError("invented observer failure")

        self.connector.on_call = broken_observer
        response = self.call()
        self.assertEqual(response.status_code, 200, response.text)
        self.assertFalse(response.json()["result"].get("isError", False))
        response = self.call("read_call_notes")
        self.assertEqual(response.status_code, 403)
        self.assertEqual([event["decision"] for event in observed], ["allowed", "refused"])
        self.assertEqual([row["status"] for row in self.connector.audit.read()], ["ok", "error"])
        with patch.object(self.connector.audit, "start", side_effect=sqlite3.OperationalError("fixture")):
            response = self.call()
        self.assertEqual(response.status_code, 503)
        self.assertEqual(len(observed), 3)
        self.assertEqual(len(self.connector.audit.read()), 2)

    def test_metadata_and_non_call_requests_do_not_emit_tool_events(self):
        response = self.client.get("/.well-known/oauth-protected-resource/mcp")
        self.assertEqual(response.status_code, 200)
        self.client.post("/mcp", json={"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
        self.assertEqual(self.events, [])
        self.assertEqual(self.connector.audit.read(), [])


class RuntimeEventTests(unittest.TestCase):
    def test_runtime_passes_observer_to_actual_https_gateway(self):
        events = []
        with tempfile.TemporaryDirectory() as directory, Runtime(directory, on_call=events.append) as runtime:
            with LocalClient(runtime.config) as client:
                token = client.authorize(["crm:read"])
                response = client.call(token, "get_account", {"account_id": "acc-001"})
                self.assertEqual(response.status_code, 200, response.text)
            self.assertEqual(len(events), 1)
            self.assertEqual(events[0]["decision"], "allowed")
            self.assertEqual(events[0]["caller"], hashlib.sha256(token.encode()).hexdigest())
            self.assertEqual(events[0]["receipt_id"], runtime.connector.audit.read()[0]["audit_id"])


if __name__ == "__main__":
    unittest.main()
