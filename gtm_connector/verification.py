"""Separate guarantee checks, including explicit local fixture writes."""

from contextlib import closing
import uuid

from . import store
from .briefing import SEAL_TOOL, create_brief
from .client import LocalClient
from .runtime import Runtime


def run(directory, *, include_slack=False):
    brief_id = "demo-" + uuid.uuid4().hex
    with Runtime(directory, rate_limit=4) as runtime, LocalClient(runtime.config) as client:
        crm = client.authorize(["crm:read"])
        both = client.authorize(["crm:read", "calls:read"])
        writer = client.authorize(["crm:write"])
        limited = client.authorize(["crm:read"])
        discovery = client.rpc(crm, "server/discover")
        discovery.raise_for_status()
        tools = client.rpc(crm, "tools/list")
        tools.raise_for_status()

        def call(token, tool, args, status=200):
            response = client.call(token, tool, args, brief_id=brief_id)
            if response.status_code != status:
                raise ValueError(f"demo failed: {tool} returned {response.status_code}, expected {status}")
            body = response.json()
            if status == 200:
                if body.get("error") or body.get("result", {}).get("isError"):
                    raise ValueError(f"demo failed: {tool} returned a tool error")
                return body["result"]["structuredContent"]["records"]
            return body["error"]["message"]

        accounts = call(crm, "search_accounts", {})
        account = call(crm, "get_account", {"account_id": "acc-001"})
        denied = call(crm, "read_call_notes", {"account_id": "acc-001"}, 403)
        injection = call(both, "read_call_notes", {"account_id": "acc-007"})
        empty = call(both, "read_call_notes", {"account_id": "acc-004"})
        write = {"account_id": "acc-001", "kind": "demo", "body": "Explicit CLI activity.",
                 "idempotency_key": "demo-explicit-write"}
        first = call(writer, "log_activity", write)
        second = call(writer, "log_activity", write)
        for _ in range(4):
            call(limited, "get_account", {"account_id": "acc-002"})
        throttled = call(limited, "get_account", {"account_id": "acc-002"}, 429)
        slack_proof = None
        if include_slack:
            slack_token = client.authorize(["slack:read"])
            denied_slack = call(crm, "read_slack_messages", {"account_id": "acc-003"}, 403)
            messages = call(slack_token, "read_slack_messages", {"account_id": "acc-003"})
            denied_crm = call(slack_token, "get_account", {"account_id": "acc-003"}, 403)
            denied_calls = call(slack_token, "read_call_notes", {"account_id": "acc-003"}, 403)
            slack_proof = {"record": messages[0], "crm_without_slack": {"status": 403, "message": denied_slack},
                           "slack_without_crm": {"status": 403, "message": denied_crm},
                           "slack_without_calls": {"status": 403, "message": denied_calls}}
        with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
            before_briefs = connection.execute("SELECT count(*) FROM activities").fetchone()[0]
        full = create_brief(runtime, "acc-003", include_slack=include_slack)
        restricted = create_brief(runtime, "acc-001", include_calls=False,
                                  include_slack=include_slack, grant_slack=False)
        injected = create_brief(runtime, "acc-007", include_slack=include_slack)
        brief_receipts = [row for brief in (full, restricted, injected)
                          for row in runtime.connector.audit.read(brief["brief_id"])]
        with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
            activity_count = connection.execute(
                "SELECT count(*) FROM activities WHERE idempotency_key = ?", (write["idempotency_key"],)).fetchone()[0]
            injected_writes = connection.execute(
                "SELECT count(*) FROM activities WHERE idempotency_key = 'seed-injection'").fetchone()[0]
            after_briefs = connection.execute("SELECT count(*) FROM activities").fetchone()[0]
        receipts = runtime.connector.audit.read(brief_id)
        # Each saved card has one finished receipt that seals it.
        seals = runtime.connector.audit.read_ids([brief["sealed_by"] for brief in (full, restricted, injected)])
        if (len(accounts) != 30 or empty or first != second or activity_count != 1
                or injected_writes or len(receipts) != (16 if include_slack else 12)
                or any(r["status"] == "pending" for r in receipts)
                or before_briefs != after_briefs or len(brief_receipts) != (12 if include_slack else 9)
                or any(row["tool"] == "log_activity" for row in brief_receipts)
                or any((row["tool"], row["status"]) != (SEAL_TOOL, "ok") for row in seals)):
            raise ValueError("demo invariant failed; inspect the audit log")
        result = {"status": "connector", "brief_id": brief_id, "accounts": len(accounts),
                "renewal_record": account[0], "tools": [t["name"] for t in tools.json()["result"]["tools"]],
                "notes_without_scope": {"status": 403, "message": denied},
                "empty_notes": empty,
                "injection_note": {"record_ref": injection[0]["record_ref"], "activities_created": injected_writes},
                "idempotent_write": {"requests": 2, "activities": activity_count, "record_ref": first[0]["record_ref"]},
                "rate_limit": {"status": 429, "message": throttled},
                "audit_rows": len(receipts), "audit_path": str(runtime.directory / "audit.sqlite3"),
                "briefs": [{key: brief[key] for key in ("brief_id", "account_id", "text")}
                           for brief in (full, restricted)],
                "injection_brief": {"brief_id": injected["brief_id"],
                                    "record_ref": "calls/call_notes/note-007",
                                    "activities_created": after_briefs - before_briefs},
                "brief_audit_rows": len(brief_receipts), "seal_audit_rows": len(seals)}
        if include_slack:
            result.update(slack=slack_proof, workflow="crm-calls-slack")
            result["injection_brief"]["slack_record_ref"] = "slack/messages/msg-007"
        return result
