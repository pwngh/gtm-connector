"""Executable, key-free proof of the PR 2 connector boundary."""

from contextlib import closing
import uuid

from . import store
from .client import LocalClient
from .runtime import Runtime


def run(directory):
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
        contradiction = call(both, "read_call_notes", {"account_id": "acc-003"})
        opportunity = call(both, "list_opportunities", {"account_id": "acc-003"})
        write = {"account_id": "acc-001", "kind": "demo", "body": "Explicit CLI activity.",
                 "idempotency_key": "demo-explicit-write"}
        first = call(writer, "log_activity", write)
        second = call(writer, "log_activity", write)
        for _ in range(4):
            call(limited, "get_account", {"account_id": "acc-002"})
        throttled = call(limited, "get_account", {"account_id": "acc-002"}, 429)
        with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
            activity_count = connection.execute(
                "SELECT count(*) FROM activities WHERE idempotency_key = ?", (write["idempotency_key"],)).fetchone()[0]
            injected_writes = connection.execute(
                "SELECT count(*) FROM activities WHERE idempotency_key = 'seed-injection'").fetchone()[0]
        receipts = runtime.connector.audit.read(brief_id)
        if (len(accounts) != 30 or empty or first != second or activity_count != 1
                or injected_writes or len(receipts) != 14 or any(r["status"] == "pending" for r in receipts)):
            raise ValueError("demo invariant failed; inspect the audit log")
        return {"status": "connector", "brief_id": brief_id, "accounts": len(accounts),
                "renewal_record": account[0], "tools": [t["name"] for t in tools.json()["result"]["tools"]],
                "notes_without_scope": {"status": 403, "message": denied},
                "empty_notes": empty,
                "conflicting_sources": {"opportunity": opportunity[0], "note": contradiction[0]},
                "injection_note": {"record_ref": injection[0]["record_ref"], "activities_created": injected_writes},
                "idempotent_write": {"requests": 2, "activities": activity_count, "record_ref": first[0]["record_ref"]},
                "rate_limit": {"status": 429, "message": throttled},
                "audit_rows": len(receipts), "audit_path": str(runtime.directory / "audit.sqlite3")}
