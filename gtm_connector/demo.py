"""One invented rep prepares one renewal through scoped, checked source reads."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
from typing import Any
import uuid

from . import calls, slack, store
from .briefing import create_brief
from .citations import BriefRejected
from .client import LocalClient
from .doctor import _private
from .runtime import Runtime


ACCOUNT_ID = "acc-007"
ACCOUNT_NAME = "Juniper Crest Learning"
REP = {"name": "Morgan Reed", "role": "Account executive"}


class DemoStateError(ValueError):
    """The selected state cannot demonstrate the current invented story."""

    code = "demo_state_mismatch"


def _require_fixture(condition, message):
    if not condition:
        raise DemoStateError("This demo state contains an older or edited book. " + message)


def _require(condition, message):
    if not condition:
        raise ValueError("demo invariant failed: " + message)


def _activities(directory):
    with closing(store.connect(directory / "crm.sqlite3", readonly=True)) as connection:
        return connection.execute("SELECT count(*) FROM activities").fetchone()[0]


def run(directory):
    """Look up an account, observe a refusal, and prepare one read-only brief."""
    # Refuse a CRM file that is not the demo book before Runtime replaces owner and TLS files.
    crm_path = Path(directory) / "crm.sqlite3"
    if crm_path.exists() or crm_path.is_symlink():
        _require_fixture(_private(Path(directory).resolve(), directory=True) and _private(crm_path),
                         "The CRM file and state folder must be private regular local fixtures.")
        try:
            with closing(store.connect(crm_path, readonly=True)) as connection:
                selected = connection.execute("SELECT name FROM accounts WHERE id = ?", (ACCOUNT_ID,)).fetchone()
        except sqlite3.Error:
            raise DemoStateError("This demo state contains an older or edited book. "
                                 "The CRM fixture could not be read.") from None
        _require_fixture(selected is not None and selected[0] == ACCOUNT_NAME,
                         "The current story requires Juniper Crest Learning (acc-007).")
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
        _require_fixture(len(records) == 1 and records[0].get("id") == ACCOUNT_ID,
                         "The lookup must return Juniper Crest Learning (acc-007) once.")
        account = records[0]
        denied = client.call(crm, "read_call_notes", {"account_id": ACCOUNT_ID}, brief_id=run_id)
        challenge = denied.headers.get("www-authenticate", "")
        _require(denied.status_code == 403 and 'error="insufficient_scope"' in challenge
                 and 'scope="calls:read"' in challenge, "the call-note scope refusal was not enforced")
        observed: dict[str, Any] = {}
        invariant_failure = None

        def validate_draft(draft, evidence):
            nonlocal invariant_failure
            try:
                receipts = runtime.connector.audit.read(run_id)
                brief_receipts = evidence["receipts"]
                _require([(row["tool"], row["status"], row["http_status"]) for row in receipts] == [
                    ("search_accounts", "ok", 200), ("read_call_notes", "error", 403)],
                    "the lookup or denied read has no matching final receipt")
                _require([(row["tool"], row["status"], row["http_status"]) for row in brief_receipts] == [
                    ("get_account", "ok", 200), ("list_opportunities", "ok", 200),
                    ("read_call_notes", "ok", 200), ("read_slack_messages", "ok", 200)],
                    "the brief did not finish its four planned reads")
                after = _activities(runtime.directory)
                _require(before == after, "the read-only workflow created an activity")
                by_ref = {row["record_ref"]: row for row in evidence["records"]}
                _require_fixture(by_ref.get(account["record_ref"]) == account,
                                 "The account changed between lookup and the brief.")
                _require(isinstance(draft.text, str), "the draft is not text")
                for reference, instruction in (("calls/call_notes/note-007", calls.INJECTION_TEXT),
                                               ("slack/messages/msg-007", slack.INJECTION_TEXT)):
                    body = by_ref.get(reference, {}).get("body", "")
                    _require_fixture(instruction in body and json.dumps(body, ensure_ascii=True) in draft.text
                                     and "[" + reference + "]" in draft.text,
                                     f"The instruction in {reference} must remain quoted data.")
                _require("[" + account["record_ref"] + "]" in draft.text
                         and len([line for line in draft.text.splitlines()
                                  if line.startswith("- Proposed follow-up:")]) == 1,
                         "the draft lost its CRM citation or follow-up proposal")
                observed.update(receipts=receipts, brief_receipts=brief_receipts, activities_created=after - before)
            except ValueError as exc:
                invariant_failure = exc
                raise

        try:
            brief = create_brief(runtime, ACCOUNT_ID, include_slack=True, observe_draft=validate_draft)
        except BriefRejected:
            if invariant_failure is not None:
                raise invariant_failure from None
            raise
        receipts, brief_receipts = observed["receipts"], observed["brief_receipts"]
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
                          "quoted": True, "activities_created": observed["activities_created"]},
            "audit": {"run_id": run_id, "rows": len(receipts) + len(brief_receipts)},
            "activities_created": observed["activities_created"],
        }
