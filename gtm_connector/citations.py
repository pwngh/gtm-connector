"""Check a small, controlled brief vocabulary against collected source records.

This checks exact supported statements, not natural-language entailment. Source
text is quoted as data; the checker never treats it as a command or as truth.
"""

from datetime import date
import json
import re
from typing import Any


class BriefRejected(ValueError):
    """A candidate or its evidence cannot support a checked brief."""


class NoRenewalCandidate(BriefRejected):
    """Validated Salesforce evidence has no eligible future Contract end date."""


_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}\Z")
_SOURCES = {
    "accounts": ("crm", "acc", "get_account"),
    "contacts": ("crm", "con", "get_account"),
    "opportunities": ("crm", "opp", "list_opportunities"),
    "call_notes": ("calls", "note", "read_call_notes"),
}
_SLACK_SOURCE = {"messages": ("slack", "msg", "read_slack_messages")}
_CONTRACT_SOURCE = {"contracts": ("crm", "ctr", "get_account")}
_PROCUREMENT_NOTE = "Procurement has not approved this renewal. The budget remains unsigned."
_SECURITY_MESSAGE = "Security review is delayed. Procurement approval is still pending."


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


def _valid_binding(value, *, key, backend):
    return (isinstance(value, dict) and set(value) == {"schema_version", key, "identity_sha256"}
            and type(value["schema_version"]) is int and value["schema_version"] == 1
            and value[key] == backend and isinstance(value["identity_sha256"], str)
            and re.fullmatch(r"[0-9a-f]{64}", value["identity_sha256"]) is not None)


def _live_slack_sources(evidence):
    expected = {"brief_id", "account_id", "as_of", "records", "receipts", "notes_status", "slack_status",
                "workflow_version", "crm_backend", "crm_binding", "calls_backend", "slack_backend",
                "slack_binding", "thread_snapshot"}
    if evidence.get("calls_backend") == "imported":
        expected.add("calls_binding")
    if (set(evidence) != expected or evidence.get("crm_backend") not in ("sqlite", "salesforce")
            or evidence.get("calls_backend") not in ("fixture", "imported")
            or evidence.get("slack_backend") != "slack"
            or not _valid_binding(evidence.get("crm_binding"), key="crm_backend", backend=evidence["crm_backend"])
            or not _valid_binding(evidence.get("slack_binding"), key="provider", backend="slack")
            or (evidence["calls_backend"] == "imported" and evidence["calls_binding"] != evidence["crm_binding"])):
        raise BriefRejected("Live Slack evidence requires its exact source selections and bindings.")


def _live_slack_facts(evidence, messages, receipt_ref, add):
    """Quote observed text and disclose what a bounded API observation cannot prove."""
    snapshot = evidence["thread_snapshot"]
    counts = snapshot["counts"]
    message_word = "message" if counts["messages"] == 1 else "messages"
    page_word = "page" if counts["pages"] == 1 else "pages"
    required = {add(
        f"Slack observation: {counts['messages']} {message_word} returned across {counts['pages']} {page_word}; "
        f"creation cutoff {_text(snapshot['cutoff_ts'])}; fetched at {_text(snapshot['fetched_at'])}. "
        "Paging completed for the selected API-visible thread text. This is not a complete archive; "
        "previous edits and deletions are unknown, and content may have changed during paging.", receipt_ref)}
    required.add(add(
        f"Slack non-text content was omitted from evidence. Omitted entry counts are files {counts['omitted_files']}, "
        f"attachments {counts['omitted_attachments']}, and blocks {counts['omitted_blocks']}. "
        "No separate requests fetched non-text resources. "
        "Links and mentions remain literal source text.", receipt_ref))
    # The validated reader order is chronological; immutable hash IDs do not encode time.
    for message in messages:
        metadata = message["slack_message"]
        edited = metadata["edited"]
        edit = ("Edit metadata was not reported; earlier edits are unknown." if edited is None else
                f"Slack reports an edit at {_text(edited['ts'])}; earlier text is unknown.")
        subtype = metadata["subtype"]
        kind = "" if subtype is None else f" Message kind: {_text(subtype)}."
        required.add(add(
            f"Untrusted Slack message at {_text(message['sent_at'])} from {_text(message['author'])} "
            f"says {_text(message['body'])}. {edit}{kind}", message["record_ref"]))
    return required


def _catalog(evidence):
    version = evidence.get("workflow_version", 1)
    if type(version) is not int or version not in (1, 2, 3, 4, 5):
        raise BriefRejected("Unsupported evidence workflow version.")
    if version == 1 and "slack_status" in evidence:
        raise BriefRejected("Legacy evidence cannot contain a Slack outcome.")
    live_slack = version == 5
    if live_slack:
        _live_slack_sources(evidence)
    elif {"slack_backend", "slack_binding", "thread_snapshot", "crm_binding"} & evidence.keys():
        raise BriefRejected("Legacy evidence cannot contain a live Slack source selection.")
    imported = version == 4 or (live_slack and evidence["calls_backend"] == "imported")
    if imported:
        allowed = {"brief_id", "account_id", "as_of", "records", "receipts", "notes_status",
                   "workflow_version", "crm_backend", "calls_backend", "calls_binding", "slack_status"}
        if live_slack:
            allowed.update({"crm_binding", "slack_backend", "slack_binding", "thread_snapshot"})
        binding = evidence.get("calls_binding")
        if (not evidence.keys() <= allowed or evidence.get("calls_backend") != "imported"
                or evidence.get("crm_backend") not in ("sqlite", "salesforce")
                or not isinstance(binding, dict)
                or set(binding) != {"schema_version", "crm_backend", "identity_sha256"}
                or type(binding["schema_version"]) is not int or binding["schema_version"] != 1
                or binding["crm_backend"] != evidence["crm_backend"]
                or not isinstance(binding["identity_sha256"], str)
                or re.fullmatch(r"[0-9a-f]{64}", binding["identity_sha256"]) is None):
            raise BriefRejected("Imported evidence requires a supported source binding and bounded snapshot.")
    salesforce = version == 3 or ((imported or live_slack) and evidence["crm_backend"] == "salesforce")
    include_slack = version == 2 or (version in (3, 4, 5) and "slack_status" in evidence)
    if salesforce and evidence.get("crm_backend") != "salesforce":
        raise BriefRejected("Salesforce evidence must identify its CRM backend.")
    sources = {**_SOURCES, **(_SLACK_SOURCE if include_slack else {}),
               **(_CONTRACT_SOURCE if salesforce else {})}
    account_id, brief_id = evidence["account_id"], evidence["brief_id"]
    if not all(isinstance(value, str) and _ID.fullmatch(value)
               for value in (account_id, brief_id)):
        raise BriefRejected("Invalid account or brief identifier.")
    if salesforce and not re.fullmatch(r"acc-sf-[A-Za-z0-9]{18}", account_id):
        raise BriefRejected("Salesforce evidence requires a namespaced account identifier.")
    if live_slack and not salesforce and not re.fullmatch(r"acc-[0-9]{3}", account_id):
        raise BriefRejected("Fixture CRM evidence requires its canonical account identifier.")
    as_of = _day(evidence["as_of"])
    groups: dict[str, list[dict[str, Any]]] = {name: [] for name in sources}
    expected_rows: dict[str, list[str]] = {tool: [] for _, _, tool in sources.values()}
    seen = set()
    if not isinstance(evidence["records"], list) or not isinstance(evidence["receipts"], list):
        raise BriefRejected("Evidence records and receipts must be lists.")
    for record in evidence["records"]:
        reference, identifier = record["record_ref"], record["id"]
        if not isinstance(reference, str) or not isinstance(identifier, str):
            raise BriefRejected("Record identifiers must be strings.")
        parts = reference.split("/")
        if len(parts) != 3 or parts[1] not in sources or reference in seen:
            raise BriefRejected("Unknown or duplicate source reference.")
        table = parts[1]
        if not live_slack and "slack_message" in record:
            raise BriefRejected("A live Slack message requires workflow 5 and its complete source snapshot.")
        if not imported and table == "call_notes" and "manifest" in record:
            raise BriefRejected("An imported transcript requires its declared source binding.")
        system, prefix, tool = sources[table]
        if (not _ID.fullmatch(identifier) or not identifier.startswith(prefix + "-")
                or reference != f"{system}/{table}/{identifier}"):
            raise BriefRejected("Record reference does not identify its source.")
        if salesforce and system == "crm":
            native_id = record.get("source_id")
            if (record.get("backend") != "salesforce" or not isinstance(native_id, str)
                    or not re.fullmatch(r"[A-Za-z0-9]{18}", native_id)
                    or identifier != f"{prefix}-sf-{native_id}"):
                raise BriefRejected("Salesforce record identity does not match its source.")
        if (identifier if table == "accounts" else record["account_id"]) != account_id:
            raise BriefRejected("Evidence contains a record for a different account.")
        if imported and table == "call_notes":
            from .calls_import import CallsImportError, validate_imported_record
            try:
                record = validate_imported_record(record, account_id=account_id, crm_binding=evidence["calls_binding"])
            except CallsImportError:
                raise BriefRejected(
                    "Imported transcript evidence does not match its immutable selected revision.") from None
        seen.add(reference)
        groups[table].append(record)
        expected_rows[tool].append(reference)
    if len(groups["accounts"]) != 1:
        raise BriefRejected("Evidence must contain exactly one account.")

    if evidence["notes_status"] not in ("available", "denied"):
        raise BriefRejected("Unknown call-notes outcome.")
    if imported and evidence["notes_status"] == "available" and len(groups["call_notes"]) != 1:
        raise BriefRejected("Imported calls require exactly one selected transcript excerpt.")
    if include_slack and evidence["slack_status"] not in ("available", "denied"):
        raise BriefRejected("Unknown Slack outcome.")
    if live_slack:
        if evidence["slack_status"] == "denied":
            if groups["messages"] or evidence["thread_snapshot"] is not None:
                raise BriefRejected("A denied Slack read cannot contain a source snapshot or messages.")
        else:
            from .slack_reader import SlackReadError, validate_slack_snapshot
            try:
                validate_slack_snapshot(groups["messages"], evidence["thread_snapshot"], account_id=account_id,
                                        crm_binding=evidence["crm_binding"], slack_binding=evidence["slack_binding"])
            except SlackReadError:
                raise BriefRejected(
                    "Live Slack evidence does not match its complete bounded source snapshot.") from None
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
        denied = ((tool == "read_call_notes" and evidence["notes_status"] == "denied")
                  or (tool == "read_slack_messages" and evidence["slack_status"] == "denied"))
        expected_outcome = ("error", 403) if denied else ("ok", 200)
        if (receipt["status"], receipt["http_status"]) != expected_outcome:
            raise BriefRejected("Audit receipt has an unsupported outcome.")
        if denied and refs:
            raise BriefRejected("Denied notes must not contain returned records." if tool == "read_call_notes"
                                else "Denied Slack reads must not contain returned records.")
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
    required = set()
    if salesforce:
        from .salesforce_facts import add_salesforce_facts
        required, renewal_ref = add_salesforce_facts(groups, as_of, brief_id, add)
    else:
        renewal_ref = ref
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
            role = "not provided" if contact["title"] == "" else _text(contact["title"])
            add(f"Contact {_text(contact['name'])}: role {role}; "
                f"email {_text(contact['email'])}.", contact["record_ref"])
        for opportunity in sorted(groups["opportunities"], key=lambda row: row["id"]):
            add(f"CRM opportunity {opportunity['id']}: stage {_text(opportunity['stage'])}; "
                f"amount {_money(opportunity['amount_cents'])}; "
                f"close date {_day(opportunity['close_date'])}.", opportunity["record_ref"])
    if not groups["opportunities"]:
        add(f"No opportunities were returned for {account_id}.",
            f"audit/{receipts['list_opportunities']['audit_id']}")
    procurement_conflicts = []
    notes_receipt = f"audit/{receipts['read_call_notes']['audit_id']}"
    if evidence["notes_status"] == "denied":
        required.add(add("Call notes were unavailable because the token lacks calls:read.", notes_receipt))
    elif not groups["call_notes"]:
        required.add(add(f"No call notes were returned for {account_id}.", notes_receipt))
    else:
        for note in sorted(groups["call_notes"], key=lambda row: row["id"]):
            if imported:
                manifest = note["manifest"]
                selected, cited = manifest["requested_range"], manifest["cited_range"]
                when = "not provided" if note["called_at"] is None else _text(note["called_at"])
                required.add(add(
                    f"User-supplied transcript excerpt {note['id']}: call time {when}; "
                    f"imported at {_text(manifest['imported_at'])}; "
                    f"requested {manifest['unit_kind']} {selected['first']} through {selected['last']}; "
                    f"quoted {manifest['unit_kind']} {cited['first']} through {cited['last']} "
                    f"of {manifest['total_units']} including available adjacent context. "
                    f"This is an excerpt, not a complete call summary. It says {_text(note['body'])}.",
                    note["record_ref"]))
                continue
            add(f"Untrusted call note {note['id']} at {_text(note['called_at'])} "
                f"says {_text(note['body'])}.", note["record_ref"])
            # One explicit fixture rule, not inference over arbitrary note prose.
            if salesforce or note["body"] != _PROCUREMENT_NOTE:
                continue
            for opportunity in sorted(groups["opportunities"], key=lambda row: row["id"]):
                if opportunity["stage"] == "closed_won":
                    procurement_conflicts.append((opportunity, note))
                    required.add(add(
                        f'Source conflict: CRM opportunity {opportunity["id"]} reports stage '
                        f'"closed_won", while call note {note["id"]} reports unapproved procurement.',
                        opportunity["record_ref"], note["record_ref"]))
    if include_slack:
        slack_receipt = f"audit/{receipts['read_slack_messages']['audit_id']}"
        conflict_refs: set[str] = set()
        if evidence["slack_status"] == "denied":
            required.add(add("Slack messages were unavailable because the token lacks slack:read.",
                             slack_receipt))
        elif live_slack:
            required.update(_live_slack_facts(evidence, groups["messages"], slack_receipt, add))
        elif not groups["messages"]:
            required.add(add(f"No Slack messages were returned for {account_id}.", slack_receipt))
        else:
            for message in sorted(groups["messages"], key=lambda row: row["id"]):
                add(f"Untrusted Slack message {message['id']} at {_text(message['sent_at'])} "
                    f"in {_text(message['channel'])} from {_text(message['author'])} "
                    f"says {_text(message['body'])}.", message["record_ref"])
                if message["body"] != _SECURITY_MESSAGE:
                    continue
                for opportunity, note in procurement_conflicts:
                    refs = (opportunity["record_ref"], note["record_ref"], message["record_ref"])
                    conflict_refs.update(refs)
                    required.add(add(
                        f'Three-source conflict: CRM opportunity {opportunity["id"]} reports stage '
                        f'"closed_won", call note {note["id"]} reports unapproved procurement, '
                        f'and Slack message {message["id"]} reports a delayed security review.', *refs))
        source_order = {"crm": 0, "calls": 1, "slack": 2}
        if conflict_refs:
            # One proposed action, even if several records match the bounded rule.
            refs = sorted(conflict_refs, key=lambda ref: (source_order[ref.split("/")[0]], ref))
            required.add(add("Proposed follow-up: Ask the account owner to confirm procurement "
                             "and security review status.", *refs))
        elif procurement_conflicts:
            refs = {row["record_ref"] for pair in procurement_conflicts for row in pair}
            refs = sorted(refs, key=lambda ref: (source_order[ref.split("/")[0]], ref))
            required.add(add("Proposed follow-up: Ask the account owner to confirm procurement status.",
                             *refs))
        else:
            required.add(add("Proposed follow-up: Ask the account owner to review the cited renewal context.",
                             renewal_ref))
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


def diagnose_brief(candidate: str, evidence: dict) -> dict:
    """Count failed line positions without retaining their untrusted text.

    Unsupported wording includes paraphrases. These counts do not establish
    whether a statement is factually false. Missing disclosures have no line
    position and are counted separately. Publication still uses check_brief.
    """
    supported, required = _validated_catalog(evidence)
    lines = [line.strip() for line in candidate.splitlines() if line.strip()]
    reasons = {key: 0 for key in ("uncited", "unsupported_or_miscited", "duplicate")}
    seen = set()
    for line in lines:
        if line not in supported:
            reason = ("unsupported_or_miscited" if re.search(r" \[[^\[\]\r\n]+\]$", line)
                      else "uncited")
            reasons[reason] += 1
        elif line in seen:
            reasons["duplicate"] += 1
        seen.add(line)
    missing = len(required - set(lines))
    return {"candidate_lines": len(lines), "rejected_lines": sum(reasons.values()),
            "missing_required_claims": missing,
            "reason_counts": {**reasons, "missing_required": missing, "empty": int(not lines)}}


def check_brief(candidate: object, evidence: dict) -> str:
    """Reject uncited, altered, misattributed, or extra prose before publication.

    A valid subset is allowed: grounding and coverage are different checks.
    Required source outcomes, recognized conflicts, and v2 follow-up must remain visible.
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
        if evidence.get("workflow_version", 1) == 1:
            raise BriefRejected("Brief must disclose required call-notes outcomes and source conflicts.")
        raise BriefRejected("Brief must disclose required source outcomes, source conflicts, and proposed follow-up.")
    return "\n".join(lines) + "\n"
