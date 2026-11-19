"""Numbered evidence units and the Deal card rendered from them.

The connector writes every character of a card. A figure comes from a typed record field or from
connector arithmetic. A quotation is one whole sentence of neutralized source text, shown with the
role of its speaker and never promoted to a record value. Source text is data: it is neutralized
once, here, before anything displays it.
"""

from datetime import date, datetime, timezone
import hashlib
import json
import re
from typing import Any
import unicodedata
from urllib.parse import urlsplit


class BriefRejected(ValueError):
    """The evidence cannot support a card."""


SANITIZER_VERSION = 1
SHOWN_SENTENCES = 12
SHOWN_RECORDS = 5
MAX_SENTENCE = 300
_SOON_DAYS = 30
_STALE_DAYS = 60
_ID = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}\Z")
_CURRENCY = re.compile(r"[A-Z]{3}\Z")
_KEYS = {"brief_id", "account_id", "as_of", "records", "receipts", "notes_status", "slack_status",
         "crm_backend", "crm_binding", "calls_backend", "calls_binding", "slack_backend", "slack_binding",
         "thread_snapshot", "sellers"}
_SOURCES = {
    "accounts": ("crm", "acc", "get_account"),
    "contacts": ("crm", "con", "get_account"),
    "contracts": ("crm", "ctr", "get_account"),
    "opportunities": ("crm", "opp", "list_opportunities"),
    "call_notes": ("calls", "note", "read_call_notes"),
    "messages": ("slack", "msg", "read_slack_messages"),
}

# Slack wraps mentions, channels, and links in angle brackets. Each becomes a bracketed word that no
# client renders as a mention or a link. The destination of a link is reduced to its host.
_SLACK_TOKEN = re.compile(r"<([!@#]?)([^<>|\n]*)(?:\|([^<>\n]*))?>")
# A mention that was never closed is still neutralized, so no output holds the start of one.
_OPEN_MENTION = re.compile(r"<([!@])([A-Za-z0-9^_-]*)()")
_MARKDOWN_LINK = re.compile(r"\[([^\[\]\n]*)\]\(\s*(?:https?://|mailto:)[^\s)]*\s*\)", re.IGNORECASE)
_RAW_LINK = re.compile(r"(?:https?://|mailto:)[^\s<>\"']*", re.IGNORECASE)
# The longest speaker label a card shows. A longer one is no label.
MAX_LABEL = 200
# A time in this form is shown as given. Any other time is source text and is shown as a neutralized literal.
_PLAIN_INSTANT = re.compile(
    r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?(?:Z|[+-][0-9]{2}:[0-9]{2})")
# A sentence ends at . ! or ? and any closing quote or bracket, before a word that does not start in lower case.
_SENTENCE_END = re.compile(r"""[.!?]["')\]]*(\s+)(?=["'(\[]?[^\sa-z])""")
_ABBREVIATION = re.compile(
    r"(?:^|[\s(\[\"'])(?:Mr|Mrs|Ms|Dr|Prof|Sr|Jr|St|vs|etc|Inc|Ltd|Co|No|e\.g|i\.e|[A-Za-z])\.\Z")
_VOICE = re.compile(r"<v(?:\.[^\s>]*)*[ \t]+([^<>\n]+)>")
_LINE_BREAKS = {0x0A, 0x0B, 0x0C, 0x85, 0x2028, 0x2029}


def _host(target):
    if target.lower().startswith("mailto:"):
        return "mail"
    try:
        host = (urlsplit(target).hostname or "").lower()
    except ValueError:
        host = ""
    return host if re.fullmatch(r"[a-z0-9.-]{1,80}", host) else "unknown host"


def safe_text(raw: object) -> tuple[str, dict[str, int]]:
    """Neutralize one source text. Returns the text every output uses and what was replaced."""
    if not isinstance(raw, str):
        raise BriefRejected("Evidence text fields must be strings.")
    counts = {"mentions": 0, "links": 0, "controls": 0}

    def token(match):
        kind, target, label = match.group(1), match.group(2).strip(), (match.group(3) or "").strip()
        if kind == "!":
            counts["mentions"] += 1
            word = target.split("^")[0]
            return f"[mention {word}]" if word in ("channel", "here", "everyone") else "[mention group]"
        if kind == "@":
            counts["mentions"] += 1
            return f"[user {target}]" if _ID.fullmatch(target) else "[user]"
        if kind == "#":
            counts["mentions"] += 1
            return f"[channel {target}]" if _ID.fullmatch(target) else "[channel]"
        if target.lower().startswith(("http://", "https://", "mailto:")):
            counts["links"] += 1
            return f"{label} [link {_host(target)}]" if label else f"[link {_host(target)}]"
        return match.group(0)

    def markdown(match):
        counts["links"] += 1
        target = match.group(0)[match.group(0).index("](") + 2:-1].strip()
        return f"{match.group(1)} [link {_host(target)}]"

    def raw_link(match):
        counts["links"] += 1
        return f"[link {_host(match.group(0))}]"

    text = _OPEN_MENTION.sub(token, _SLACK_TOKEN.sub(token, unicodedata.normalize("NFC", raw)))
    text = _RAW_LINK.sub(raw_link, _MARKDOWN_LINK.sub(markdown, text))
    visible = []
    for character in text:
        code = ord(character)
        if code in _LINE_BREAKS:
            visible.append("\n")
        elif character == "\t":
            visible.append(" ")
        elif code < 32 or 127 <= code <= 159 or (code > 159 and unicodedata.category(character) in ("Cf", "Cs")):
            counts["controls"] += 1
            visible.append(f"[U+{code:04X}]")
        else:
            visible.append(character)
    return "".join(visible), counts


def sentences(safe: str) -> list[str]:
    """Split neutralized text into whole sentences. A quotation is always one of these, never a part."""
    found = []
    for line in safe.split("\n"):
        line, start = " ".join(line.split()), 0
        for match in _SENTENCE_END.finditer(line):
            if not _ABBREVIATION.search(line[start:match.start(1)]):
                found.append(line[start:match.start(1)])
                start = match.end(1)
        if line[start:]:
            found.append(line[start:])
    return found


def _label(value):
    return " ".join(value.split()) if isinstance(value, str) else ""


def speaker_role(label, sellers, contacts, *, slack=False) -> tuple[str, str]:
    """Derive a role from identity only, never from what the text says about itself."""
    name = _label(label)
    if not name:
        return "unknown", "no speaker label"
    matches = [] if slack else [contact for contact in contacts if _label(contact["name"]) == name]
    listed = name in {_label(seller) for seller in sellers}
    if listed and matches:
        return "unknown", "label matches a listed seller and a contact"
    if listed:
        return "seller", "listed seller identity"
    if len(matches) == 1:
        return "customer", f"label matches contact [{matches[0]['n']}]"
    if matches:
        return "unknown", f"label matches {len(matches)} contacts"
    if slack:
        return "unknown", "author is not a listed seller; Slack identities cannot show a customer"
    return "unknown", "label matches no listed seller and no contact"


def _day(value):
    if not isinstance(value, str) or date.fromisoformat(value).isoformat() != value:
        raise BriefRejected("Evidence dates must use YYYY-MM-DD.")
    return date.fromisoformat(value)


def _instant(value):
    """A source instant in UTC, or None when it is absent or has no offset."""
    try:
        moment = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return moment.astimezone(timezone.utc) if moment.tzinfo is not None else None
    except (AttributeError, TypeError, ValueError, OverflowError):
        return None


def _days(count):
    return f"{count} {'day' if count == 1 else 'days'}"


def _valid_binding(value, *, key, backend):
    return (isinstance(value, dict) and set(value) == {"schema_version", key, "identity_sha256"}
            and type(value["schema_version"]) is int and value["schema_version"] == 1
            and value[key] == backend and isinstance(value["identity_sha256"], str)
            and re.fullmatch(r"[0-9a-f]{64}", value["identity_sha256"]) is not None)


def _validated(evidence):
    """Check that every record belongs to this account and that one finished receipt covers each read."""
    if not isinstance(evidence, dict) or set(evidence) != _KEYS:
        raise BriefRejected("Evidence has an unsupported shape.")
    crm, calls, slack = evidence["crm_backend"], evidence["calls_backend"], evidence["slack_backend"]
    if (crm not in ("sqlite", "salesforce") or calls not in ("fixture", "imported")
            or slack not in ("fixture", "slack")
            or evidence["notes_status"] not in ("available", "denied")
            or evidence["slack_status"] not in ("available", "denied", "not_requested")):
        raise BriefRejected("Evidence names an unknown source or outcome.")
    salesforce, imported = crm == "salesforce", calls == "imported"
    with_slack = evidence["slack_status"] != "not_requested"
    live_slack = with_slack and slack == "slack"
    if imported and not _valid_binding(evidence["calls_binding"], key="crm_backend", backend=crm):
        raise BriefRejected("Imported evidence requires a supported source binding.")
    if live_slack and (not _valid_binding(evidence["crm_binding"], key="crm_backend", backend=crm)
                       or not _valid_binding(evidence["slack_binding"], key="provider", backend="slack")
                       or (imported and evidence["calls_binding"] != evidence["crm_binding"])):
        raise BriefRejected("Live Slack evidence requires its exact source selections and bindings.")
    if not live_slack and evidence["thread_snapshot"] is not None:
        raise BriefRejected("Only a live Slack read carries a thread snapshot.")
    sellers = evidence["sellers"]
    if (not isinstance(sellers, list) or len(sellers) > 200
            or any(not isinstance(seller, str) or not 1 <= len(seller) <= 200 for seller in sellers)):
        raise BriefRejected("Seller identities must be a short list of names or IDs.")
    account_id, brief_id = evidence["account_id"], evidence["brief_id"]
    if not all(isinstance(value, str) and _ID.fullmatch(value) for value in (account_id, brief_id)):
        raise BriefRejected("Invalid account or brief identifier.")
    if not re.fullmatch(r"acc-sf-[A-Za-z0-9]{18}" if salesforce else r"acc-[0-9]{3}", account_id):
        raise BriefRejected("The account identifier does not match its CRM.")
    if not isinstance(evidence["records"], list) or not isinstance(evidence["receipts"], list):
        raise BriefRejected("Evidence records and receipts must be lists.")
    tables = set(_SOURCES) - (set() if salesforce else {"contracts"}) - (set() if with_slack else {"messages"})
    groups: dict[str, list[dict[str, Any]]] = {name: [] for name in tables}
    expected_rows: dict[str, list[str]] = {_SOURCES[name][2]: [] for name in tables}
    seen = set()
    for record in evidence["records"]:
        reference, identifier = record["record_ref"], record["id"]
        if not isinstance(reference, str) or not isinstance(identifier, str):
            raise BriefRejected("Record identifiers must be strings.")
        parts = reference.split("/")
        if len(parts) != 3 or parts[1] not in tables or reference in seen:
            raise BriefRejected("Unknown or duplicate source reference.")
        table = parts[1]
        system, prefix, tool = _SOURCES[table]
        if (not _ID.fullmatch(identifier) or not identifier.startswith(prefix + "-")
                or reference != f"{system}/{table}/{identifier}"):
            raise BriefRejected("Record reference does not identify its source.")
        if ("slack_message" in record) != (live_slack and table == "messages"):
            raise BriefRejected("A live Slack message requires the live Slack source and its snapshot.")
        if ("manifest" in record) != (imported and table == "call_notes"):
            raise BriefRejected("An imported transcript requires its declared source binding.")
        if salesforce and system == "crm":
            native_id = record.get("source_id")
            if (record.get("backend") != "salesforce" or not isinstance(native_id, str)
                    or not re.fullmatch(r"[A-Za-z0-9]{18}", native_id) or identifier != f"{prefix}-sf-{native_id}"):
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
    if imported and evidence["notes_status"] == "available" and len(groups["call_notes"]) != 1:
        raise BriefRejected("Imported calls require exactly one selected transcript excerpt.")
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
    receipts: dict[str, dict] = {}
    receipt_ids = set()
    for receipt in evidence["receipts"]:
        tool, audit_id = receipt["tool"], receipt["audit_id"]
        if tool not in expected_rows or tool in receipts:
            raise BriefRejected("Evidence requires one receipt per briefing tool.")
        if (not isinstance(audit_id, str) or not _ID.fullmatch(audit_id) or audit_id in receipt_ids
                or receipt["brief_id"] != brief_id or receipt["arguments"] != {"account_id": account_id}):
            raise BriefRejected("Audit receipt does not match this brief and account.")
        refs = receipt["rows"]
        if (not isinstance(refs, list) or any(not isinstance(ref, str) for ref in refs)
                or type(receipt["row_count"]) is not int or receipt["row_count"] != len(refs)
                or sorted(refs) != sorted(expected_rows[tool])):
            raise BriefRejected("Audit receipt does not match returned records.")
        denied = ((tool == "read_call_notes" and evidence["notes_status"] == "denied")
                  or (tool == "read_slack_messages" and evidence["slack_status"] == "denied"))
        if (receipt["status"], receipt["http_status"]) != (("error", 403) if denied else ("ok", 200)):
            raise BriefRejected("Audit receipt has an unsupported outcome.")
        if denied and refs:
            raise BriefRejected("A denied read must not contain returned records.")
        receipt_ids.add(audit_id)
        receipts[tool] = receipt
    if set(receipts) != set(expected_rows):
        raise BriefRejected("Evidence is missing a briefing tool receipt.")
    return groups, receipts


class _Card:
    """Collects units in a fixed order and the card lines that cite them."""

    def __init__(self, evidence, groups, receipts):
        self.evidence, self.groups, self.receipts = evidence, groups, receipts
        self.units: list[dict[str, Any]] = []
        self.neutralized = {"mentions": 0, "links": 0, "controls": 0}
        self.missing = 0
        self.values: list[str] = []
        self.signals: list[str] = []
        self.not_read: list[str] = []

    def line(self, value):
        """Source text neutralized onto one line."""
        safe, counts = safe_text(value)
        for key, count in counts.items():
            self.neutralized[key] += count
        return " ".join(safe.split())

    def text(self, value):
        """One field as a quoted literal on one line, or Missing when the record has none."""
        if value is None or value == "":
            self.missing += 1
            return "Missing"
        return json.dumps(self.line(value), ensure_ascii=False)

    def record(self, row, table):
        self.units.append({"n": len(self.units) + 1, "kind": "record", "source": _SOURCES[table][0],
                           "record_ref": row["record_ref"], "tool": _SOURCES[table][2]})
        return len(self.units)

    def spoken(self, row, table, raw, *, at, speaker, role, basis):
        # A speaker label and a time are source text too. A role was derived from the label as given.
        speaker = None if speaker is None else self.line(speaker) or None
        if speaker is not None and len(speaker) > MAX_LABEL:
            speaker, role, basis = None, "unknown", f"speaker label over {MAX_LABEL} characters"
        if at is not None and not (isinstance(at, str) and _PLAIN_INSTANT.fullmatch(at)):
            at = self.line(at) or None
        safe, counts = safe_text(raw)
        for key, count in counts.items():
            self.neutralized[key] += count
        digest = hashlib.sha256(raw.encode("utf-8", "surrogatepass")).hexdigest()
        for sentence in sentences(safe):
            self.units.append({"n": len(self.units) + 1, "kind": "sentence", "source": _SOURCES[table][0],
                               "record_ref": row["record_ref"], "tool": _SOURCES[table][2], "at": at,
                               "speaker": speaker, "role": role, "basis": basis, "text": sentence,
                               "raw_sha256": digest, "sanitizer_version": SANITIZER_VERSION})

    def value(self, label, value, *numbers):
        self.values.append(f"  {label}: {value} " + "".join(f"[{number}]" for number in numbers))

    def signal(self, sentence, *numbers):
        self.signals.append(f"  {sentence} " + "".join(f"[{number}]" for number in numbers))


def _money(amount, currency):
    return f"{currency} {amount}" if currency is not None else f"{amount} (currency Missing)"


def _currency(card, row):
    currency = row["currency"]
    if currency is None:
        card.missing += 1
    elif not isinstance(currency, str) or not _CURRENCY.fullmatch(currency):
        raise BriefRejected("A currency must be a three-letter code or absent.")
    return currency


def _cents(card, row, field):
    cents = row[field]
    if type(cents) is not int or cents < 0:
        raise BriefRejected("Evidence amounts must be nonnegative integer cents.")
    return _money(f"{cents // 100:,}.{cents % 100:02}", _currency(card, row))


def _decimal(card, row):
    value = row["amount_decimal"]
    if value is None:
        card.missing += 1
        return "Missing"
    if not isinstance(value, str) or len(value) > 128 or not re.fullmatch(r"-?[0-9]+(?:\.[0-9]+)?", value):
        raise BriefRejected("Salesforce amounts must be finite decimal strings or null.")
    return _money(value, _currency(card, row))


def _shown(card, rows, what):
    """The first few records in their fixed order. The rest are counted, never dropped silently."""
    if len(rows) > SHOWN_RECORDS:
        card.not_read.append(f"CRM: {len(rows) - SHOWN_RECORDS} more {what} were read and are not shown "
                             f"({len(rows)} in all).")
    return rows[:SHOWN_RECORDS]


def _crm(card, as_of):
    groups = card.groups
    salesforce = card.evidence["crm_backend"] == "salesforce"
    account = groups["accounts"][0]
    first = card.record(account, "accounts")
    contacts = sorted(groups["contacts"], key=lambda row: row["id"])
    contact_numbers = {row["id"]: card.record(row, "contacts") for row in contacts}
    named = [{"name": row["name"], "n": contact_numbers[row["id"]]} for row in contacts]
    contracts = sorted(groups.get("contracts", []), key=lambda row: row["id"])
    contract_numbers = {row["id"]: card.record(row, "contracts") for row in contracts}
    opportunities = sorted(groups["opportunities"], key=lambda row: row["id"])
    opportunity_numbers = {row["id"]: card.record(row, "opportunities") for row in opportunities}
    days, renewal_numbers = None, (first,)
    if salesforce:
        ahead: list[tuple] = []
        ended: list[tuple] = []
        for contract in contracts:
            if contract["status_code"] not in ("Draft", "InApproval", "Activated"):
                raise BriefRejected("Unknown Salesforce Contract status category.")
            if contract["end_date"] is not None:
                end = _day(contract["end_date"])
                (ahead if end >= as_of else ended).append((end, contract["id"], contract))
        if ahead:
            end, _, contract = min(ahead, key=lambda candidate: candidate[:2])
            days, renewal_numbers = (end - as_of).days, (contract_numbers[contract["id"]],)
            activated = ("" if contract["status_code"] == "Activated"
                         else "; a planning candidate, not activated")
            card.value("Contract end", f"{end}, {'in ' + _days(days) if days else 'today'}; "
                       f"status {card.text(contract['status'])}{activated}", *renewal_numbers)
        else:
            card.missing += 1
            card.value("Contract end", f"Missing: no contract ends on or after {as_of}", first)
            if ended:
                end, _, last = max(ended, key=lambda candidate: candidate[:2])
                card.signal(f"The latest contract end date, {end}, was {_days((as_of - end).days)} ago.",
                            contract_numbers[last["id"]])
        card.not_read.append("CRM: annual recurring revenue and renewal risk are not mapped from "
                             "standard Salesforce fields.")
    else:
        renewal = _day(account["renewal_date"])
        days = (renewal - as_of).days
        timing = f"in {_days(days)}" if days > 0 else f"{_days(-days)} ago" if days < 0 else "today"
        card.value("Renewal date", f"{renewal}, {timing}", first)
        card.value("Annual recurring revenue", _cents(card, account, "arr_cents"), first)
        if account["risk_status"] not in ("healthy", "at_risk"):
            raise BriefRejected("Unknown CRM risk status.")
        reason = f"; reason {card.text(account['risk_reason'])}" if account["risk_reason"] else ""
        card.value("CRM risk", account["risk_status"].replace("_", " ") + reason, first)
        if account["risk_status"] == "at_risk":
            card.signal("CRM marks this account at risk.", first)
    card.value("Account", f"{card.text(account['name'])}; industry {card.text(account['industry'])}", first)
    still_open, open_ids = [], set()
    for row in opportunities:
        if salesforce:
            if type(row["is_closed"]) is not bool or type(row["is_won"]) is not bool:
                raise BriefRejected("Salesforce opportunity outcomes must be booleans.")
            closed = row["is_closed"]
        else:
            closed = row["stage"] in ("closed_won", "closed_lost")
        _day(row["close_date"])
        if not closed:
            open_ids.add(row["id"])
            if days is not None and days <= _SOON_DAYS:
                still_open.append(row)
    # Open opportunities first, soonest close first. Then closed ones, latest close first.
    ordered = (sorted((row for row in opportunities if row["id"] in open_ids),
                      key=lambda row: (row["close_date"], row["id"]))
               + sorted((row for row in opportunities if row["id"] not in open_ids),
                        key=lambda row: (row["close_date"], row["id"]), reverse=True))
    for row in _shown(card, ordered, "opportunities"):
        amount = _decimal(card, row) if salesforce else _cents(card, row, "amount_cents")
        outcome = (f"; closed {str(row['is_closed']).lower()}; won {str(row['is_won']).lower()}"
                   if salesforce else "")
        card.value("Opportunity", f"{card.text(row['name'])}; stage {card.text(row['stage'])}; amount {amount}; "
                   f"close date {row['close_date']}{outcome}", opportunity_numbers[row["id"]])
    if not opportunities:
        card.not_read.append("CRM: no opportunity was returned for this account.")
    for row in _shown(card, contracts, "contracts"):
        if contract_numbers[row["id"]] not in renewal_numbers:
            ends = "no end date" if row["end_date"] is None else f"ends {_day(row['end_date'])}"
            card.value("Contract", f"status {card.text(row['status'])}; {ends}", contract_numbers[row["id"]])
    for row in _shown(card, contacts, "contacts"):
        card.value("Contact", f"{card.text(row['name'])}; role {card.text(row['title'])}; "
                   f"email {card.text(row['email'])}", contact_numbers[row["id"]])
    when = "today" if days == 0 else f"in {_days(days)}" if days and days > 0 else None
    if days is not None and days < 0:
        card.signal(f"The renewal date passed {_days(-days)} ago.", *renewal_numbers)
    elif still_open and len(still_open) > SHOWN_RECORDS:
        card.signal(f"The {'contract end' if salesforce else 'renewal'} is {when} and {len(still_open)} "
                    "opportunities are still open.", *renewal_numbers)
    else:
        for row in still_open:
            card.signal(f"The {'contract end' if salesforce else 'renewal'} is {when} and this opportunity is "
                        f"still open at stage {card.text(row['stage'])}.",
                        *renewal_numbers, opportunity_numbers[row["id"]])
    no_role = [row for row in contacts if row["title"] in (None, "")]
    if len(no_role) > SHOWN_RECORDS:
        card.signal(f"{len(no_role)} contacts have no role recorded.", first)
    else:
        for row in no_role:
            card.signal("This contact has no role recorded.", contact_numbers[row["id"]])
    return named


def _calls(card, as_of, contacts):
    evidence, sellers = card.evidence, card.evidence["sellers"]
    notes = card.groups["call_notes"]
    if evidence["notes_status"] == "denied":
        card.not_read.append("Calls: not read. The grant lacks calls:read.")
        return
    if not notes:
        card.not_read.append("Calls: no call record was returned for this account.")
        return
    imported = evidence["calls_backend"] == "imported"
    before = len(card.units)
    for note in sorted(notes, key=lambda row: (str(row["called_at"]), row["id"])):
        if imported:
            manifest = note["manifest"]
            for cue in manifest["units"]:
                # A WebVTT voice tag names the speaker. A cue identifier does only when it is not a number.
                voice = _VOICE.match(cue["text"]) if manifest["format"] == "vtt" else None
                speaker = voice.group(1) if voice else cue["cue_id"]
                speaker = None if speaker is None or speaker.isdigit() else _label(speaker)
                role, basis = speaker_role(speaker, sellers, contacts)
                card.spoken(note, "call_notes", cue["text"], at=note["called_at"], speaker=speaker or None,
                            role=role, basis=basis)
            cited = manifest["cited_range"]
            card.not_read.append(
                f"Calls: one transcript excerpt was read, {manifest['unit_kind']} {cited['first']} to {cited['last']} "
                f"of {manifest['total_units']}. The rest of that call and every other call were not read.")
        else:
            role, basis = speaker_role(note.get("speaker"), sellers, contacts)
            card.spoken(note, "call_notes", note["body"], at=note["called_at"],
                        speaker=_label(note.get("speaker")) or None, role=role, basis=basis)
    if not imported:
        card.not_read.append(f"Calls: {len(notes)} call {'record was' if len(notes) == 1 else 'records were'} read. "
                             "Recordings and anything outside those records were not read.")
    timed = [(moment, note["record_ref"]) for note in notes if (moment := _instant(note["called_at"])) is not None]
    if timed:
        moment, reference = max(timed)
        age = (as_of - moment.date()).days
        cited = [unit["n"] for unit in card.units[before:] if unit["record_ref"] == reference][:1]
        if age > _STALE_DAYS and cited:
            card.signal(f"The latest dated call that was read is {_days(age)} old.", *cited)


def _slack(card):
    evidence, sellers = card.evidence, card.evidence["sellers"]
    status = evidence["slack_status"]
    if status == "not_requested":
        card.not_read.append("Slack: not requested for this card.")
        return
    if status == "denied":
        card.not_read.append("Slack: not read. The grant lacks slack:read.")
        return
    messages = card.groups["messages"]
    live = evidence["slack_backend"] == "slack"
    if not live:
        messages = sorted(messages, key=lambda row: (row["sent_at"], row["id"]))
    files, attached_to = 0, []
    for message in messages:
        if live:
            metadata = message["slack_message"]
            speaker = metadata["user_id"] if isinstance(metadata["user_id"], str) else None
        else:
            speaker = message["author"]
            if type(message["files"]) is not int or message["files"] < 0:
                raise BriefRejected("A Slack file count must be a nonnegative integer.")
            files += message["files"]
        role, basis = speaker_role(speaker, sellers, (), slack=True)
        first = len(card.units) + 1
        card.spoken(message, "messages", message["body"], at=message["sent_at"], speaker=_label(speaker) or None,
                    role=role, basis=basis)
        if not live and message["files"] and len(card.units) >= first:
            attached_to.append(f"[{first}]")
    count = f"{len(messages)} {'message was' if len(messages) == 1 else 'messages were'} read"
    if live:
        counts = evidence["thread_snapshot"]["counts"]
        card.not_read.append(
            f"Slack: {count} from one selected thread, and paging completed. Not read: "
            f"files ({counts['omitted_files']}), attachments ({counts['omitted_attachments']}), "
            f"blocks ({counts['omitted_blocks']}), earlier versions of edited messages, and every other thread.")
    elif not messages:
        card.not_read.append("Slack: no message was returned for this account.")
    else:
        where = f" (messages starting at {''.join(attached_to[:SHOWN_RECORDS])})" if attached_to else ""
        attached = (f" {files} attached {'file was' if files == 1 else 'files were'} not read{where}."
                    if files else "")
        card.not_read.append(f"Slack: {count}.{attached} Other channels were not read.")


def _said(card):
    """Whole sentences, newest record first. Sentences keep their order inside one moment."""
    spoken = [unit for unit in card.units if unit["kind"] == "sentence"]
    fits = [unit for unit in spoken if len(unit["text"]) <= MAX_SENTENCE]
    def recency(unit):
        moment = _instant(unit["at"])
        return (moment is not None, moment.timestamp() if moment else 0.0)

    ordered = sorted(fits, key=recency, reverse=True)
    lines = []
    for unit in ordered[:SHOWN_SENTENCES]:
        where = {"calls": "call", "slack": "Slack"}[unit["source"]]
        who = "no speaker label" if unit["speaker"] is None else "speaker " + json.dumps(unit["speaker"],
                                                                                    ensure_ascii=False)
        when = ("time Missing" if unit["at"] is None else unit["at"] if _PLAIN_INSTANT.fullmatch(unit["at"])
                else "time " + json.dumps(unit["at"], ensure_ascii=False))
        lines.append(f"  [{unit['n']}] {unit['role']} ({unit['basis']}); {where} {when}; {who}")
        lines.append("      " + json.dumps(unit["text"], ensure_ascii=False))
    hidden, long = len(ordered) - min(len(ordered), SHOWN_SENTENCES), len(spoken) - len(fits)
    if hidden:
        card.not_read.append(f"Sentences: {hidden} older "
                             f"{'sentence was read and is' if hidden == 1 else 'sentences were read and are'} "
                             "not shown.")
    if long:
        card.not_read.append(f"Sentences: {long} over {MAX_SENTENCE} characters "
                             f"{'was read and is' if long == 1 else 'were read and are'} not shown, because a "
                             "quotation is never cut.")
    unknown = sum(unit["role"] == "unknown" for unit in spoken)
    if unknown:
        card.not_read.append(f"Speakers: {unknown} of {len(spoken)} sentences have no established speaker role.")
    return lines


def _build(evidence):
    groups, receipts = _validated(evidence)
    as_of = _day(evidence["as_of"])
    card = _Card(evidence, groups, receipts)
    contacts = _crm(card, as_of)
    _calls(card, as_of, contacts)
    _slack(card)
    said = _said(card)
    if card.missing:
        card.not_read.insert(0, "CRM: 1 field is empty on the record and prints as Missing." if card.missing == 1
                             else f"CRM: {card.missing} fields are empty on the record and print as Missing.")
    words = {"mentions": "mention", "links": "link", "controls": "control character"}
    replaced = [f"{count} {words[name]}{'' if count == 1 else 's'}"
                for name, count in card.neutralized.items() if count]
    if replaced:
        card.not_read.append("Neutralized in source text: " + ", ".join(replaced) + ".")
    account = groups["accounts"][0]
    name = " ".join(safe_text(account["name"])[0].split())
    lines = [f"DEAL CARD: {name} ({evidence['account_id']}) as of {evidence['as_of']}", ""]
    if card.signals:
        lines += ["ATTENTION, from record fields and dates", *card.signals, ""]
    lines += ["RECORD VALUES, from CRM fields and connector arithmetic", *card.values, "",
              "SAID, whole sentences as recorded; not record values",
              *(said or ["  No sentence was read."]), "",
              "NOT READ", *("  " + line for line in card.not_read), ""]
    return card.units, "\n".join(lines)


def build(evidence: dict) -> tuple[list[dict], str]:
    """Check the evidence and return its numbered units and the card. Bad evidence gives no card."""
    try:
        return _build(evidence)
    except BriefRejected:
        raise
    except (KeyError, TypeError, ValueError, AttributeError):
        raise BriefRejected("Evidence contains missing or invalid fields.") from None


def trace(evidence: dict, number: int) -> dict:
    """Resolve one number on a card to its source record, its neutralized text, and its audit receipt."""
    units, _ = build(evidence)
    if type(number) is not int or not 1 <= number <= len(units):
        raise ValueError(f"This card has numbers 1 to {len(units)}.")
    unit = dict(units[number - 1])
    tool = unit.pop("tool")
    receipt = next(row for row in evidence["receipts"] if row["tool"] == tool)
    unit["receipt"] = {key: receipt[key] for key in ("audit_id", "tool", "status", "http_status")}
    if unit["kind"] == "record":
        record = next(row for row in evidence["records"] if row["record_ref"] == unit["record_ref"])
        unit["fields"] = {key: (" ".join(safe_text(value)[0].split()) if isinstance(value, str) else value)
                          for key, value in sorted(record.items())
                          if key != "record_ref" and (value is None or isinstance(value, (str, int, bool)))}
    return unit
