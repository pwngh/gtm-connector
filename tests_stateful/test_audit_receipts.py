"""Generated gateway sequences check committed receipts, including write retries."""

from contextlib import closing, ExitStack
import json
from pathlib import Path
import sqlite3
from tempfile import TemporaryDirectory
from unittest.mock import patch

from hypothesis import settings, strategies as st
from hypothesis.stateful import initialize, invariant, rule, RuleBasedStateMachine
from mcp.server.auth.provider import AccessToken
from starlette.testclient import TestClient

from gtm_connector import calls, slack, store
from gtm_connector.gateway import Connector


RESOURCE = "https://127.0.0.1:8765/mcp"
VERSION = "2026-07-28"
TOOLS = (
    "search_accounts", "get_account", "list_opportunities", "read_call_notes",
    "read_slack_messages", "log_activity",
)


class FixtureAuthority:
    issuer = "https://127.0.0.1:8766"
    resource = RESOURCE

    async def verify_token(self, token):
        scopes = {
            "fixture-full": ["crm:read", "crm:write", "calls:read", "slack:read"],
            "fixture-crm": ["crm:read"],
        }.get(token)
        if scopes is None:
            return None
        return AccessToken(token=token, client_id="fixture-cli", scopes=scopes,
                           expires_at=2147483647, resource=RESOURCE)


class AuditReceiptMachine(RuleBasedStateMachine):
    def __init__(self):
        super().__init__()
        self.resources = ExitStack()
        root = Path(self.resources.enter_context(TemporaryDirectory()))
        for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
            with closing(module.connect(root / f"{name}.sqlite3")) as connection, connection:
                module.initialize(connection)
                module.seed(connection)
        self.now = 100.0
        self.connector = Connector(root / "crm.sqlite3", root / "calls.sqlite3", root / "audit.sqlite3",
                                   FixtureAuthority(), rate_limit=4, clock=lambda: self.now,
                                   slack_path=root / "slack.sqlite3")
        self.audit_path = root / "audit.sqlite3"
        with closing(sqlite3.connect(self.audit_path)) as connection, connection:
            connection.execute("CREATE TABLE completion_events (audit_id TEXT NOT NULL)")
            connection.execute("""CREATE TRIGGER observe_finish AFTER UPDATE ON audit_log
                WHEN NEW.status != 'pending'
                BEGIN INSERT INTO completion_events VALUES (NEW.audit_id); END""")
        self.client = self.resources.enter_context(TestClient(
            self.connector, base_url="https://127.0.0.1:8765", raise_server_exceptions=False,
        ))
        self.request_number = 0
        self.current = None
        self.executions = []
        self.violations = []
        self.receipt_ids = []
        self.finalized = {}
        for module in (store, calls, slack):
            real_connect = module.connect

            def observed_connect(path, *, readonly=False, connect=real_connect):
                self.observe_execution()
                return connect(path, readonly=readonly)

            self.resources.enter_context(patch.object(module, "connect", observed_connect))

    def rows(self):
        # A separate connection sees only committed receipts, not the writer's transaction.
        with closing(sqlite3.connect(self.audit_path.as_uri() + "?mode=ro", uri=True)) as connection:
            connection.row_factory = sqlite3.Row
            return {row["audit_id"]: dict(row) for row in connection.execute("SELECT * FROM audit_log")}

    def observe_execution(self):
        # The SDK can catch handler exceptions. Keep oracle failures for an assertion
        # outside dispatch so a failed check cannot become an acceptable HTTP 503.
        self.executions.append(self.current["id"])
        try:
            matching = [row for row in self.rows().values()
                        if row["request_id"] == json.dumps(self.current["id"])]
            expected = ("pending", self.current["tool"], self.current["arguments"])
            actual = [(row["status"], row["tool"], json.loads(row["arguments"])) for row in matching]
            if actual != [expected]:
                self.violations.append((self.current["id"], actual))
        except Exception as error:
            self.violations.append((self.current["id"], type(error).__name__))

    @staticmethod
    def arguments(tool, account, key):
        if tool == "search_accounts":
            return {"query": account}
        if tool == "log_activity":
            return {"account_id": account, "kind": "fixture", "body": f"Fixture activity {key}.",
                    "idempotency_key": f"sequence-key-{key}"}
        return {"account_id": account}

    def call(self, tool, credential="full", account="acc-001", key=0, fault="none"):
        self.request_number += 1
        arguments = self.arguments(tool, account, key)
        self.current = {"id": self.request_number, "tool": tool, "arguments": arguments}
        before = self.rows()
        executions_before = len(self.executions)
        with ExitStack() as faults:
            if fault in ("insert", "update"):
                operation = {"insert": "INSERT", "update": "UPDATE"}[fault]
                with closing(sqlite3.connect(self.audit_path)) as connection, connection:
                    connection.execute(f"""CREATE TRIGGER fail_audit BEFORE {operation} ON audit_log
                        BEGIN SELECT RAISE(ABORT, 'fixture audit failure'); END""")
                faults.callback(self.clear_fault)
            elif fault == "after_commit":
                real_finish = self.connector.audit.finish

                def finish_then_raise(*args, **kwargs):
                    real_finish(*args, **kwargs)
                    raise sqlite3.OperationalError("fixture failure after committed finish")

                faults.enter_context(patch.object(self.connector.audit, "finish", finish_then_raise))
            response = self.client.post("/mcp", headers={
                "Authorization": "Bearer fixture-" + credential,
                "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": VERSION,
                "Mcp-Method": "tools/call", "Mcp-Name": tool,
            }, json={
                "jsonrpc": "2.0", "id": self.request_number, "method": "tools/call",
                "params": {"name": tool, "arguments": arguments, "_meta": {
                    "io.modelcontextprotocol/protocolVersion": VERSION,
                    "io.modelcontextprotocol/clientCapabilities": {},
                    "io.modelcontextprotocol/clientInfo": {"name": "receipt-sequences", "version": "1"},
                }},
            })
        assert not self.violations, f"Execution without its committed pending receipt: {self.violations}"
        added = self.rows().keys() - before.keys()
        if fault == "insert":
            assert response.status_code == 503
            assert not added
            assert len(self.executions) == executions_before
        else:
            assert len(added) == 1
            self.receipt_ids.extend(added)
            if fault != "none":
                assert response.status_code == 503
            elif response.status_code == 200:
                assert len(self.executions) == executions_before + 1
            else:
                assert response.status_code in (401, 403, 429)
                assert len(self.executions) == executions_before

    def clear_fault(self):
        with closing(sqlite3.connect(self.audit_path)) as connection, connection:
            connection.execute("DROP TRIGGER fail_audit")

    @initialize()
    def successful_read(self):
        # Every example executes a real handler; an all-denied sequence cannot pass vacuously.
        self.call("get_account")
        assert self.executions == [1]

    @rule(tool=st.sampled_from(TOOLS), credential=st.sampled_from(("full", "crm", "invalid")),
          account=st.sampled_from(("acc-001", "acc-missing")), key=st.integers(min_value=0, max_value=2),
          fault=st.sampled_from(("none", "insert", "update", "after_commit")))
    def tool_call(self, tool, credential, account, key, fault):
        self.call(tool, credential, account, key, fault)

    @rule(data=st.data(), success=st.booleans())
    def finish_previous_receipt(self, data, success):
        audit_id = data.draw(st.sampled_from(self.receipt_ids), label="receipt")
        before = self.rows()[audit_id]
        fields = {"status": "ok" if success else "error", "http_status": 200 if success else 503,
                  "rows": [], "elapsed_ms": 1.0, "error": None if success else "fixture_failure"}
        if before["status"] == "pending":
            self.connector.audit.finish(audit_id, **fields)
        else:
            try:
                self.connector.audit.finish(audit_id, **fields)
            except ValueError:
                pass
            else:
                raise AssertionError("A finalized receipt accepted another finish")
            assert self.rows()[audit_id] == before

    @rule(seconds=st.sampled_from((1, 61)))
    def advance_quota_window(self, seconds):
        self.now += seconds

    @invariant()
    def receipts_finalize_at_most_once(self):
        rows = self.rows()
        with closing(sqlite3.connect(self.audit_path.as_uri() + "?mode=ro", uri=True)) as connection:
            completions = dict(connection.execute(
                "SELECT audit_id, count(*) FROM completion_events GROUP BY audit_id"))
        assert all(count == 1 for count in completions.values()), "A receipt finalized twice"
        assert completions.keys() <= rows.keys()
        for audit_id, row in rows.items():
            assert completions.get(audit_id, 0) == (0 if row["status"] == "pending" else 1)
            if audit_id in self.finalized:
                assert row == self.finalized[audit_id], "A finalized receipt changed"
            elif row["status"] != "pending":
                self.finalized[audit_id] = row
        assert not self.violations

    def teardown(self):
        self.resources.close()


# Bounded, repeatable sequences; shrinking still finds a small failing sequence.
TestAuditReceiptSequences = AuditReceiptMachine.TestCase
TestAuditReceiptSequences.settings = settings(
    max_examples=50, stateful_step_count=20, derandomize=True, database=None, deadline=None,
)
