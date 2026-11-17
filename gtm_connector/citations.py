"""Check a small, controlled brief vocabulary against collected source records.

This checks exact supported statements, not natural-language entailment. Source
text is quoted as data; the checker never treats it as a command or as truth.
"""

from datetime import date
import json
import re


class BriefRejected(ValueError):
    """A candidate or its evidence cannot support a checked brief."""


_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}\Z")
_SOURCES = {
    "accounts": ("crm", "acc", "get_account"),
    "contacts": ("crm", "con", "get_account"),
    "opportunities": ("crm", "opp", "list_opportunities"),
    "call_notes": ("calls", "note", "read_call_notes"),
}


def _text(value):
    if not isinstance(value, str):
        raise BriefRejected("Evidence text fields must be strings.")
    # ASCII JSON quoting keeps newlines, terminal controls, and directional
    # formatting characters inside a visible source literal on one line.
    return json.dumps(value, ensure_ascii=True)


def _day(value):
    if not isinstance(value, str) or date.fromisoformat(value).isoformat() != value:
        raise BriefRejected("Evidence dates must use YYYY-MM-DD.")
    return date.fromisoformat(value)


def _money(cents):
    if type(cents) is not int or cents < 0:
        raise BriefRejected("Evidence amounts must be nonnegative integer cents.")
    return f"USD {cents // 100:,}.{cents % 100:02}"


def _catalog(evidence):
    account_id, brief_id = evidence["account_id"], evidence["brief_id"]
    if not all(isinstance(value, str) and _ID.fullmatch(value)
               for value in (account_id, brief_id)):
        raise BriefRejected("Invalid account or brief identifier.")
    as_of = _day(evidence["as_of"])
    groups = {name: [] for name in _SOURCES}
    expected_rows = {tool: [] for _, _, tool in _SOURCES.values()}
    seen = set()
    if not isinstance(evidence["records"], list) or not isinstance(evidence["receipts"], list):
        raise BriefRejected("Evidence records and receipts must be lists.")
    for record in evidence["records"]:
        reference, identifier = record["record_ref"], record["id"]
        if not isinstance(reference, str) or not isinstance(identifier, str):
            raise BriefRejected("Record identifiers must be strings.")
        parts = reference.split("/")
        if len(parts) != 3 or parts[1] not in _SOURCES or reference in seen:
            raise BriefRejected("Unknown or duplicate source reference.")
        table = parts[1]
        system, prefix, tool = _SOURCES[table]
        if (not _ID.fullmatch(identifier) or not identifier.startswith(prefix + "-")
                or reference != f"{system}/{table}/{identifier}"):
            raise BriefRejected("Record reference does not identify its source.")
        if (identifier if table == "accounts" else record["account_id"]) != account_id:
            raise BriefRejected("Evidence contains a record for a different account.")
        seen.add(reference)
        groups[table].append(record)
        expected_rows[tool].append(reference)
    if len(groups["accounts"]) != 1:
        raise BriefRejected("Evidence must contain exactly one account.")

    if evidence["notes_status"] not in ("available", "denied"):
        raise BriefRejected("Unknown call-notes outcome.")
    receipts, receipt_ids = {}, set()
    for receipt in evidence["receipts"]:
        tool, audit_id = receipt["tool"], receipt["audit_id"]
        if tool not in expected_rows or tool in receipts:
            raise BriefRejected("Evidence requires one receipt per briefing tool.")
        if (not isinstance(audit_id, str) or not _ID.fullmatch(audit_id)
                or audit_id in receipt_ids or receipt["brief_id"] != brief_id
                or receipt["arguments"] != {"account_id": account_id}):
            raise BriefRejected("Audit receipt does not match this brief and account.")
        refs = receipt["rows"]
        if (not isinstance(refs, list) or any(not isinstance(ref, str) for ref in refs)
                or type(receipt["row_count"]) is not int
                or receipt["row_count"] != len(refs)
                or sorted(refs) != sorted(expected_rows[tool])):
            raise BriefRejected("Audit receipt does not match returned records.")
        denied = tool == "read_call_notes" and evidence["notes_status"] == "denied"
        expected_outcome = ("error", 403) if denied else ("ok", 200)
        if (receipt["status"], receipt["http_status"]) != expected_outcome:
            raise BriefRejected("Audit receipt has an unsupported outcome.")
        if denied and refs:
            raise BriefRejected("Denied notes must not contain returned records.")
        receipt_ids.add(audit_id)
        receipts[tool] = receipt
    if set(receipts) != set(expected_rows):
        raise BriefRejected("Evidence is missing a briefing tool receipt.")

    lines = []

    def add(sentence, *refs):
        line = "- " + sentence + " " + " ".join(f"[{ref}]" for ref in refs)
        lines.append(line)
        return line

    account = groups["accounts"][0]
    ref = account["record_ref"]
    add(f"Account: {_text(account['name'])}.", ref)
    add(f"Industry: {_text(account['industry'])}.", ref)
    add(f"Annual recurring revenue: {_money(account['arr_cents'])}.", ref)
    renewal = _day(account["renewal_date"])
    add(f"Renewal date: {renewal.isoformat()}.", ref)
    days = (renewal - as_of).days
    count = f"{abs(days)} {'day' if abs(days) == 1 else 'days'}"
    timing = (f"Renewal is in {count} as of {as_of}." if days > 0 else
              f"Renewal was {count} ago as of {as_of}." if days < 0 else
              f"Renewal is due on {as_of}.")
    add(timing, ref, f"run/{brief_id}")
    add(f"CRM risk status: {_text(account['risk_status'])}.", ref)
    reason = _text(account["risk_reason"])
    if account["risk_reason"]:
        add(f"CRM risk reason: {reason}.", ref)
    for contact in sorted(groups["contacts"], key=lambda row: row["id"]):
        add(f"Contact {_text(contact['name'])}: role {_text(contact['title'])}; "
            f"email {_text(contact['email'])}.", contact["record_ref"])
    for opportunity in sorted(groups["opportunities"], key=lambda row: row["id"]):
        add(f"CRM opportunity {opportunity['id']}: stage {_text(opportunity['stage'])}; "
            f"amount {_money(opportunity['amount_cents'])}; "
            f"close date {_day(opportunity['close_date'])}.", opportunity["record_ref"])
    if not groups["opportunities"]:
        add(f"No opportunities were returned for {account_id}.",
            f"audit/{receipts['list_opportunities']['audit_id']}")
    required = set()
    notes_receipt = f"audit/{receipts['read_call_notes']['audit_id']}"
    if evidence["notes_status"] == "denied":
        required.add(add("Call notes were unavailable because the token lacks calls:read.", notes_receipt))
    elif not groups["call_notes"]:
        required.add(add(f"No call notes were returned for {account_id}.", notes_receipt))
    else:
        for note in sorted(groups["call_notes"], key=lambda row: row["id"]):
            add(f"Untrusted call note {note['id']} at {_text(note['called_at'])} "
                f"says {_text(note['body'])}.", note["record_ref"])
            # One explicit fixture rule, not inference over arbitrary note prose.
            if note["body"] != ("Procurement has not approved this renewal. "
                                "The budget remains unsigned."):
                continue
            for opportunity in sorted(groups["opportunities"], key=lambda row: row["id"]):
                if opportunity["stage"] == "closed_won":
                    required.add(add(
                        f'Source conflict: CRM opportunity {opportunity["id"]} reports stage '
                        f'"closed_won", while call note {note["id"]} reports unapproved procurement.',
                        opportunity["record_ref"], note["record_ref"]))
    return lines, required


def _validated_catalog(evidence):
    try:
        return _catalog(evidence)
    except BriefRejected:
        raise
    except (KeyError, TypeError, ValueError, AttributeError):
        raise BriefRejected("Evidence contains missing or invalid fields.") from None


def build_facts(evidence: dict) -> list[str]:
    """Return the supported statements, in presentation order, for this snapshot."""
    return _validated_catalog(evidence)[0]


def check_brief(candidate: str, evidence: dict) -> str:
    """Reject uncited, altered, misattributed, or extra prose before publication.

    A valid subset is allowed: grounding and coverage are different checks.
    Unavailable/empty notes outcomes and recognized source conflicts must remain visible.
    """
    if not isinstance(candidate, str) or not candidate.strip():
        raise BriefRejected("A brief must contain supported, cited statements.")
    supported, required = _validated_catalog(evidence)
    lines = [line.strip() for line in candidate.splitlines() if line.strip()]
    if any(line not in supported for line in lines):
        raise BriefRejected("Brief contains an uncited, unsupported, or misattributed statement.")
    if len(lines) != len(set(lines)):
        raise BriefRejected("Brief repeats a statement.")
    if not required <= set(lines):
        raise BriefRejected("Brief must disclose required call-notes outcomes and source conflicts.")
    return "\n".join(lines) + "\n"
