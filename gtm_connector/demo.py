"""One invented rep prepares one renewal through scoped, checked source reads."""

from contextlib import closing
import json
import uuid

from . import calls, slack, store
from .briefing import create_brief
from .client import LocalClient
from .runtime import Runtime


ACCOUNT_ID = "acc-007"
ACCOUNT_NAME = "Juniper Crest Learning"
REP = {"name": "Morgan Reed", "role": "Account executive"}


def _require(condition, message):
    if not condition:
        raise ValueError("demo invariant failed: " + message)


def _activities(directory):
    with closing(store.connect(directory / "crm.sqlite3", readonly=True)) as connection:
        return connection.execute("SELECT count(*) FROM activities").fetchone()[0]


def run(directory):
    """Look up an account, observe a refusal, and prepare one read-only brief."""
    run_id = "demo-" + uuid.uuid4().hex
    with Runtime(directory) as runtime, LocalClient(runtime.config) as client:
        before = _activities(runtime.directory)
        crm = client.authorize(["crm:read"])
        found = client.call(crm, "search_accounts", {"query": ACCOUNT_NAME}, brief_id=run_id)
        _require(found.status_code == 200, "the account lookup did not succeed")
        result = found.json().get("result", {})
        _require(not result.get("isError") and result.get("resultType") == "complete",
                 "the account lookup returned a tool error")
        records = result.get("structuredContent", {}).get("records", [])
        _require(len(records) == 1 and records[0].get("id") == ACCOUNT_ID,
                 "the invented account was changed; choose a fresh --state-dir")
        account = records[0]
        denied = client.call(crm, "read_call_notes", {"account_id": ACCOUNT_ID}, brief_id=run_id)
        challenge = denied.headers.get("www-authenticate", "")
        _require(denied.status_code == 403 and 'error="insufficient_scope"' in challenge
                 and 'scope="calls:read"' in challenge, "the call-note scope refusal was not enforced")
        brief = create_brief(runtime, ACCOUNT_ID, include_slack=True)
        after = _activities(runtime.directory)
        receipts = runtime.connector.audit.read(run_id)
        brief_receipts = runtime.connector.audit.read(brief["brief_id"])
        _require([(row["tool"], row["status"], row["http_status"]) for row in receipts] == [
            ("search_accounts", "ok", 200), ("read_call_notes", "error", 403)],
            "the lookup or denied read has no matching final receipt")
        _require([(row["tool"], row["status"], row["http_status"]) for row in brief_receipts] == [
            ("get_account", "ok", 200), ("list_opportunities", "ok", 200),
            ("read_call_notes", "ok", 200), ("read_slack_messages", "ok", 200)],
            "the brief did not finish its four planned reads")
        _require(before == after, "the read-only workflow created an activity")
        by_ref = {row["record_ref"]: row for row in brief["evidence"]["records"]}
        _require(by_ref.get(account["record_ref"]) == account,
                 "the account changed between lookup and the brief")
        for reference, instruction in (("calls/call_notes/note-007", calls.INJECTION_TEXT),
                                       ("slack/messages/msg-007", slack.INJECTION_TEXT)):
            row = by_ref.get(reference, {})
            body = row.get("body", "")
            _require(instruction in body and json.dumps(body, ensure_ascii=True) in brief["text"]
                     and "[" + reference + "]" in brief["text"],
                     "an injected source instruction was not preserved as quoted data")
        _require("[" + account["record_ref"] + "]" in brief["text"]
                 and brief["proposed_follow_up"] in brief["text"].splitlines(),
                 "the checked brief lost its CRM citation or follow-up proposal")
        return {
            "schema_version": 1, "status": "accepted", "workflow": "renewal-demo", "data_mode": "demo",
            "sources": {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "demo_fixture"},
            "rep": dict(REP),
            "account": {key: account[key] for key in ("id", "name", "record_ref", "renewal_date")},
            "lookup": {"query": ACCOUNT_NAME, "matches": len(records), "receipt_id": receipts[0]["audit_id"]},
            "denied_read": {"tool": "read_call_notes", "status": denied.status_code,
                            "required_scope": "calls:read", "receipt_id": receipts[1]["audit_id"]},
            "brief": {key: brief[key] for key in ("brief_id", "account_id", "as_of", "text", "proposed_follow_up")},
            "injection": {"record_ref": "calls/call_notes/note-007", "slack_record_ref": "slack/messages/msg-007",
                          "quoted": True, "activities_created": after - before},
            "audit": {"run_id": run_id, "rows": len(receipts) + len(brief_receipts)},
            "activities_created": after - before,
        }
