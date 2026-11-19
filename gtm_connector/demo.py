"""One invented law-firm renewal, read through scoped and audited tools, as a Deal card."""

from contextlib import closing
from pathlib import Path
import sqlite3
import uuid

from . import store
from .briefing import create_brief
from .client import LocalClient
from .doctor import _private
from .runtime import Runtime
from .seed import SHOWCASE


ACCOUNT_ID = f"acc-{SHOWCASE:03}"
ACCOUNT_NAME = "Thornfield Wren LLP"


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
    """Look up the account, observe a refused read, and print its card. Nothing is written to a source."""
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
                         f"The current story requires {ACCOUNT_NAME} ({ACCOUNT_ID}).")
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
                         f"The lookup must return {ACCOUNT_NAME} ({ACCOUNT_ID}) once.")
        account = records[0]
        denied = client.call(crm, "read_call_notes", {"account_id": ACCOUNT_ID}, brief_id=run_id)
        challenge = denied.headers.get("www-authenticate", "")
        _require(denied.status_code == 403 and 'error="insufficient_scope"' in challenge
                 and 'scope="calls:read"' in challenge, "the call-note scope refusal was not enforced")
        brief = create_brief(runtime, ACCOUNT_ID, include_slack=True)
        receipts = runtime.connector.audit.read(run_id)
        _require([(row["tool"], row["status"], row["http_status"]) for row in receipts] == [
            ("search_accounts", "ok", 200), ("read_call_notes", "error", 403)],
            "the lookup or denied read has no matching final receipt")
        _require([(row["tool"], row["status"], row["http_status"]) for row in brief["evidence"]["receipts"]] == [
            ("get_account", "ok", 200), ("list_opportunities", "ok", 200),
            ("read_call_notes", "ok", 200), ("read_slack_messages", "ok", 200)],
            "the card did not finish its four planned reads")
        created = _activities(runtime.directory) - before
        _require(created == 0, "the read-only workflow created an activity")
        return {
            "schema_version": 2, "status": "accepted", "workflow": "renewal-demo", "data_mode": "demo",
            "sources": brief["sources"],
            "account": {key: account[key] for key in ("id", "name", "record_ref", "renewal_date")},
            "lookup": {"query": ACCOUNT_NAME, "matches": len(records), "receipt_id": receipts[0]["audit_id"]},
            "denied_read": {"tool": "read_call_notes", "status": denied.status_code,
                            "required_scope": "calls:read", "receipt_id": receipts[1]["audit_id"]},
            "brief": {key: brief[key] for key in ("brief_id", "account_id", "as_of", "text", "units")},
            # The two calls of this run, the card's reads, and the receipt that seals the saved card.
            "audit": {"run_id": run_id, "rows": len(receipts) + len(brief["evidence"]["receipts"]) + 1},
            "activities_created": created,
        }
