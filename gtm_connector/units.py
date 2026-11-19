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


from .seed import SELLERS


class BriefRejected(ValueError):
    """The evidence cannot support a card."""


SANITIZER_VERSION = 2
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
# A link is any scheme, a scheme-relative //host, mailto:, or a bare www. address.
_TARGET = r"(?:[a-z][a-z0-9+.-]*://|mailto:|www\.|//(?=[a-z0-9]))"
_MARKDOWN_LINK = re.compile(r"\[([^\[\]\n]*)\]\(\s*([^\s()]+)\s*\)")
# In running text a scheme or mailto: is a link wherever it starts. A bare www. or // is one only at the
# start of a word, so a path such as a//b is left alone.
_RAW_LINK = re.compile(r"(?:[a-z][a-z0-9+.-]{0,31}://|mailto:|(?<![a-z0-9])www\.|(?<![a-z0-9:/])//(?=[a-z0-9]))"
                       r"[^\s<>\"']*", re.IGNORECASE)
_IS_TARGET = re.compile(_TARGET, re.IGNORECASE)
# The longest speaker label a card shows. A longer one is no label.
MAX_LABEL = 200
# A time in this form is shown as given. Any other time is source text and is shown as a neutralized literal.
_PLAIN_INSTANT = re.compile(
    r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?(?:Z|[+-][0-9]{2}:[0-9]{2})")
# A WebVTT voice tag, or the tag that closes one. A class holds no dot, so the pattern cannot backtrack.
_VOICE = re.compile(r"<v(?:\.[^\s>.]*)*[ \t]+([^<>\n]+)>|</v>")
# The order of SAID, and of every count by role.
_ROLES = ("customer", "seller", "unknown")
# Cues by one speaker join into one run only when the next starts within this long of the last.
_RUN_GAP_MS = 2000
# One speaker's cues up to this far apart still join when the earlier one stops mid-sentence and the later one
# goes on in lower case.
_BRIDGE_GAP_MS = 10_000
# WebVTT markup that is not speech: bold, italic, underline, class, language and ruby spans, the reading a ruby
# span carries, and cue timestamps. Only a language span takes an annotation, so "<b not>" stays as text.
_CUE_MARKUP = re.compile(r"<rt(?:\.[^\s<>.]*)*>[^<>]*</rt>|</?(?:[biuc]|ruby|rt)(?:\.[^\s<>.]*)*>"
                         r"|</?lang(?:\.[^\s<>.]*)*(?:[ \t][^<>\n]*)?>|<(?:\d{2,}:)?[0-5]\d:[0-5]\d\.\d{3}>")
_LINE_BREAKS = {0x0A, 0x0B, 0x0C, 0x85, 0x2028, 0x2029}
# A bracketed integer is how a card cites a unit, so source text never keeps one.
_CITATION = re.compile(r"\[(\d+)\]")
_ENDED = re.compile(r"""[.!?\u3002\uff01\uff1f\u0964\u061f\u06d4]["')\]\u201d\u2019\u00bb\u300d\uff09]*\Z""")


def _host(target):
    if target.lower().startswith("mailto:"):
        return "mail"
    try:
        host = (urlsplit(target if "//" in target[:40] else "//" + target).hostname or "").lower()
    except ValueError:
        host = ""
    # A host is shown without a leading www., which a client would turn back into a link.
    while host.startswith("www."):
        host = host[4:]
    return host if re.fullmatch(r"[a-z0-9.-]{1,80}", host) and "www." not in host else "unknown host"


def safe_text(raw: object) -> tuple[str, dict[str, int]]:
    """Neutralize one source text. Returns the text every output uses and what was replaced."""
    if not isinstance(raw, str):
        raise BriefRejected("Evidence text fields must be strings.")
    counts = {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}

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
        if _IS_TARGET.match(target) or match.group(3) is not None:
            # Slack renders any target|label pair as a link, whatever the target is.
            counts["links"] += 1
            return f"{label} [link {_host(target)}]" if label else f"[link {_host(target)}]"
        return match.group(0)

    def markdown(match):
        if not _IS_TARGET.match(match.group(2)):
            return match.group(0)
        counts["links"] += 1
        return f"{match.group(1)} [link {_host(match.group(2))}]"

    def raw_link(match):
        # Punctuation that closes the sentence around a link is not part of the link.
        link = match.group(0).rstrip(".,;:!?)]}'\"")
        counts["links"] += 1
        return f"[link {_host(link)}]" + match.group(0)[len(link):]

    def citation(match):
        counts["brackets"] += 1
        return f"({match.group(1)})"

    # A Windows line ending is a line break, not a control character to show.
    plain = unicodedata.normalize("NFC", raw).replace("\r\n", "\n")
    text = _OPEN_MENTION.sub(token, _SLACK_TOKEN.sub(token, plain))
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
    return _CITATION.sub(citation, "".join(visible)), counts


# A sentence may end at closing punctuation followed by a token that does not start in lower case.
_SENTENCE_END = re.compile(r"""([.!?]["')\]\u201d\u2019\u00bb]*)(\s+)(?=["'(\[\u201c\u2018\u00ab]?[^\sa-z])""")
_BEFORE = r"(?:^|[\s(\[\"'\u201c\u2018\u00ab])"
_AFTER = r"\.[\"')\]\u201d\u2019\u00bb]*\Z"
_CLOSERS = "\"')]\u201d\u2019\u00bb"
# A period after one of these does not end a sentence: "28 U.S.C. 1332", "F.Supp.2d", "e.g."; titles, legal and
# state short forms, days, months, and single letters; a list marker that opens a line.
_DOTTED = re.compile(_BEFORE + r"(?:[A-Za-z]{1,6}\.){2,}[\"')\]]*\Z")
_FORMS = (
    r"(?:Mr|Mrs|Ms|Mx|Mme|Mlle|Messrs|Sra|Dr|Prof|Esq|Sr|Jr|Hon|Rev|Sen|Rep|Gov|Pres|Gen|Col|Capt|Lt|Sgt"
    r"|Assn|Bros|Dept|Div|Est|Mfg|Natl|Intl|Univ|St|Mt|Ft"
    r"|v|vs|al|cf|etc|ibid|id|supra|infra|approx|Approx|misc|ca|pp|vol|fig|nr|Nr|est|ext"
    r"|Fed|Civ|Crim|Evid|App|Supp|Dist|Cir|Bankr|Admin|Stat|Reg|Code|Comp|Ann|Cons"
    r"|Tit|Art|Sec|Ch|Subd|Subsec|Cl|Para|Pt|Ex|Sched|Amend|Op|Proc|Ord|Const"
    r"|tit|art|sec|ch|subd|subsec|cl|para|pt|ex|sched|amend|op|proc"
    r"|Fam|Bus|Com|Fin|Ins|Lab|Pen|Prob|Prop|Tax|Veh|Welf"
    r"|Ala|Ariz|Ark|Cal|Colo|Conn|Del|Fla|Ga|Ill|Ind|Kan|Ky|La|Md|Mass|Mich|Minn|Miss|Mo|Mont|Neb|Nev|Okla|Or|Pa"
    r"|Tenn|Tex|Va|Vt|Wash|Wis|Wyo"
    r"|Mon|Tue|Tues|Wed|Thu|Thur|Thurs|Fri|Sat|Sun"
    r"|Jan|Feb|Mar|Apr|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec|[A-Za-z])")
_ABBREVIATION = re.compile(_BEFORE + _FORMS + _AFTER)
# A short capitalized word before a number or before another short form is a short form too: "Fig. 2",
# "127 S. Ct. 1955", "Pub. L. No. 117-58", "Haw. Rev. Stat.", "Treas. Reg.".
_SHORT = re.compile(_BEFORE + r"([A-Z][A-Za-z]{1,4})" + _AFTER)
_SHORT_NEXT = re.compile(_FORMS + r"\.")
# What cannot be the last word of a sentence, so a transcript run that ends on it stopped mid-sentence.
_NEVER_LAST = re.compile(_BEFORE + r"(?:Mr|Mrs|Ms|Mx|Mme|Mlle|Messrs|Dr|Prof|Hon|v|vs|cf|Fed|No|Nos|U\.S\.C|C\.F\.R"
                         r"|e\.g|i\.e)" + _AFTER)
_LIST_MARKER = re.compile(r"\s*(?:\d+|[A-Za-z])\.[\"')\]]*")
# A firm suffix, the word "I", or a capitalized initialism ends a sentence only before a word that opens one.
_MAY_END = re.compile(_BEFORE + r"(?:Inc|Ltd|Co|Corp|LLP|LLC|LP|PLC|PC|I)" + _AFTER)
_OPENER = re.compile(r"(?:We|Our|They|Their|He|She|It|Its|I|You|My|Your|This|That|These|Those|The|A|An|In|On|At|For|If"
                     r"|When|As|There|But|And|So|Then|However|Therefore)(?![\w.-])")
# "No. 14" is a number. "No." before anything else is an answer.
_NUMBER_SIGN = re.compile(_BEFORE + r"[Nn]os?\.\Z")
_SECTION_SIGN = re.compile(r"""["'(\[]*[§¶]""")


def _continues(line: str, start: int, end: int, after: int) -> bool:
    """Whether the closing punctuation that ends at `end` leaves its sentence, begun at `start`, unfinished.

    `after` is where the next token starts.
    """
    if line[after:after + 1].islower():
        return True
    # A bracket opened in this sentence and closed soon after holds its own punctuation: "(Approved? Ask Legal)".
    near = max(start, end - 200)
    for opened, closed in ("()", "[]"):
        if line.count(opened, near, end) > line.count(closed, near, end) and line.find(closed, end, end + 200) > 0:
            return True
    if not line[max(start, end - 8):end].rstrip(_CLOSERS).endswith("."):
        return False
    if _LIST_MARKER.fullmatch(line, start, end):
        return True
    window = max(start - 1, end - 24, 0)
    dotted = _DOTTED.search(line, window, end)
    if _MAY_END.search(line, window, end) or dotted and dotted.group().isupper():
        return _OPENER.match(line, after) is None
    if dotted or _ABBREVIATION.search(line, window, end) or _SECTION_SIGN.match(line, after):
        return True
    short = _SHORT.search(line, window, end)
    if short and (_SHORT_NEXT.match(line, after) or len(short.group(1)) < 5 and line[after:after + 1].isdigit()):
        return True
    # "Docket No. A-17" and "No. 14" go on. "No." as the whole sentence is an answer, unless a number follows.
    number = _NUMBER_SIGN.search(line, window, end)
    return number is not None and (number.end() - start > 5 or line[after:after + 1].isdigit())


def _unfinished(text: str) -> bool:
    """Whether transcript text stops mid-sentence: no closing punctuation, a trailing "...", a bare list
    marker, or a last word that cannot end a sentence ("Mr.", "v.", "28 U.S.C."). "No." alone is an answer."""
    text = text.strip()
    if not _ENDED.search(text) or text.rstrip(_CLOSERS).endswith("..") or _LIST_MARKER.fullmatch(text):
        return True
    last = _NEVER_LAST.search(text, max(len(text) - 24, 0))
    return last is not None and not _NUMBER_SIGN.fullmatch(text)


def _goes_on(text: str) -> bool:
    """Whether text opens as the rest of a sentence: with a letter in lower case."""
    return text.lstrip()[:1].islower()


def sentences(safe: str, *, lines: bool = False) -> list[str]:
    """Split neutralized text into whole sentences. A quotation is always one of these, never a part.

    A line break inside a paragraph is a wrap, not an end. Only a blank line or closing punctuation ends
    a sentence. With `lines`, every line break is an end: in a chat message each line is the author's own.
    """
    found: list[str] = []
    for paragraph in re.split(r"\n" if lines else r"\n[ \t]*\n", safe):
        line = " ".join(paragraph.split())
        if not line:
            continue
        start = 0
        for match in _SENTENCE_END.finditer(line):
            if not _continues(line, start, match.end(1), match.end(2)):
                found.append(line[start:match.end(1)])
                start = match.end(2)
        if line[start:]:
            found.append(line[start:])
    return found


def voices(text: str, label=None) -> list[tuple]:
    """Split one WebVTT cue at its voice tags into (speaker label, text). A tag is a label, not speech.

    Text outside a voice span belongs to the cue's own label, not to the voice before it.
    """
    spans, position, speaker = [], 0, label
    for match in _VOICE.finditer(text):
        spans.append((speaker, text[position:match.start()]))
        speaker, position = (label if match.group(1) is None else match.group(1)), match.end()
    spans.append((speaker, text[position:]))
    # Styling is removed after the voices are found, so removing a tag can never make a voice tag.
    return [(name, part) for name, raw in spans if (part := _CUE_MARKUP.sub("", raw)).strip()]


# "Name: " at the start of a line, in a transcript whose import declared that layout: a letter, then at most
# 100 characters with no colon, question or exclamation mark, angle bracket, or full stop of a script that
# writes it as one character.
_NAMED = re.compile(r"[ \t]*([^\W\d_][^:!?<>\n\u3002\uff01\uff1f\u0964\u061f\u06d4]{0,99}?)[ \t]*:[ \t]+(?=\S)")


def _opens(line):
    """The match for a line that opens with a name. A name holds no sentence end: "We signed. Dana: ..." has none."""
    opened = _NAMED.match(line)
    return opened if opened and len(sentences(opened.group(1))) == 1 else None


def named(text: str) -> tuple[str, str] | None:
    """One line or cue of a transcript that declares speaker names, as (name, words).

    The first line opens "Name: " and no later line does, because a cue is one speaker's turn. Anything else
    is not that layout, and the answer is None.
    """
    lines = text.split("\n")
    while lines and not lines[0].strip():
        lines.pop(0)
    opened = _opens(lines[0]) if lines else None
    if opened is None or any(_opens(line) for line in lines[1:]):
        return None
    return opened.group(1), "\n".join([lines[0][opened.end():], *lines[1:]])


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
    if imported and (not _valid_binding(evidence["calls_binding"], key="crm_backend", backend=crm)
                     or evidence["crm_binding"] != evidence["calls_binding"]):
        raise BriefRejected("Imported evidence requires one supported CRM binding for both sources.")
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

    def __init__(self, evidence, groups):
        self.evidence, self.groups = evidence, groups
        self.units: list[dict[str, Any]] = []
        self.neutralized = {"mentions": 0, "links": 0, "controls": 0, "brackets": 0}
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

    def quoted(self, value):
        """One field as a quoted literal on one line, or Missing when the record has none."""
        return "Missing" if value in (None, "") else json.dumps(self.line(value), ensure_ascii=False)

    def text(self, value):
        """A displayed field. An empty one is counted once, where it is shown."""
        self.missing += value in (None, "")
        return self.quoted(value)

    def record(self, row, table):
        self.units.append({"n": len(self.units) + 1, "kind": "record", "source": _SOURCES[table][0],
                           "record_ref": row["record_ref"], "tool": _SOURCES[table][2]})
        return len(self.units)

    def spoken(self, row, table, parts, *, at, role_of, transcript=False, open_start=False):
        """Units for one record's speech. `parts` is (speaker label, text, start ms, end ms) in order.

        Neighbouring parts under one speaker label are one run, so a sentence wrapped across cues stays whole:
        when they are at most two seconds apart, or at most ten when the earlier part stops mid-sentence and
        the later one goes on in lower case. Parts with no label or no times never join: no label is not one
        speaker, and lines with no times may be minutes apart.
        In a transcript, a run that stops mid-sentence is not a quotation, and neither is the opening of that
        speaker's next run. A record with no sentence still gets one unit, so every record resolves.
        """
        if at is not None and not (isinstance(at, str) and _PLAIN_INSTANT.fullmatch(at)):
            at = self.line(at) or None
        runs: list[list] = []
        for label, raw, start, end in parts:
            label = _label(label) or None
            last = runs[-1] if runs else None
            gap = None if last is None or start is None or last[2] is None else start - last[2]
            if last is not None and label is not None and last[0] == label and gap is not None and (
                    abs(gap) <= _RUN_GAP_MS
                    or 0 < gap <= _BRIDGE_GAP_MS and _unfinished(last[1]) and _goes_on(raw)):
                last[1:] = [last[1] + "\n" + raw, end]
            else:
                runs.append([label, raw, end])
        before = len(self.units)
        # The digest is of the record's text as stored, so it can be checked against the source.
        digest = hashlib.sha256(row["body"].encode("utf-8", "surrogatepass")).hexdigest()
        waiting: set = set()  # speakers whose last run stopped mid-sentence
        heard: set = set()  # speakers who have spoken in this excerpt
        cut = False  # whether the sentence before this one, by anyone, stopped mid-sentence
        for index, (label, raw, _) in enumerate(runs):
            role, basis = role_of(label)
            # A speaker label is source text too. The role was derived from the label as given.
            speaker = None if label is None else self.line(label) or None
            if speaker is not None and len(speaker) > MAX_LABEL:
                speaker, role, basis = None, "unknown", f"speaker label over {MAX_LABEL} characters"
            safe, counts = safe_text(raw)
            for key, count in counts.items():
                self.neutralized[key] += count
            found = sentences(safe, lines=table == "messages")
            # What follows a sentence that stopped may be its other half: the same speaker's next words, or
            # anyone's when they go on in lower case. In an excerpt that starts inside a call, each speaker's
            # first words may finish a sentence begun before it.
            follows = (open_start and label not in heard) or label in waiting or cut and _goes_on(safe)
            if found:
                waiting.discard(label)
                heard.add(label)
            for position, sentence in enumerate(found):
                # Inside a run only a paragraph break can leave a sentence without its end. At the end of a
                # run, a short form that expects more does too.
                cut = transcript and (_unfinished(sentence) if position == len(found) - 1
                                      else not _ENDED.search(sentence))
                partial, follows = follows or cut, cut
                self.units.append({"n": len(self.units) + 1, "kind": "sentence", "source": _SOURCES[table][0],
                                   "record_ref": row["record_ref"], "tool": _SOURCES[table][2], "at": at,
                                   "speaker": speaker, "role": role, "basis": basis, "text": sentence,
                                   "partial": bool(partial), "raw_sha256": digest,
                                   "sanitizer_version": SANITIZER_VERSION})
            if found and cut:
                waiting.add(label)
        if len(self.units) == before:
            self.record(row, table)
        return before + 1

    def value(self, label, value, *numbers):
        self.values.append(f"  {label}: {value} " + "".join(f"[{number}]" for number in numbers))

    def signal(self, sentence, *numbers, more=0):
        """One derived line. When many records raise it, it cites the first few and counts the rest."""
        self.signals.append(f"  {sentence} " + "".join(f"[{number}]" for number in numbers)
                            + (f" and {more} more" if more else ""))


def _money(amount, currency):
    return f"{currency} {amount}" if currency is not None else f"{amount} (currency Missing)"


def _amount(row, salesforce, field="amount_cents"):
    """An amount as printed and how many of its fields are empty. Every record is checked, shown or not."""
    currency = row["currency"]
    if currency is not None and (not isinstance(currency, str) or not _CURRENCY.fullmatch(currency)):
        raise BriefRejected("A currency must be a three-letter code or absent.")
    if salesforce:
        value = row["amount_decimal"]
        if value is None:
            return "Missing", 1
        if not isinstance(value, str) or len(value) > 128 or not re.fullmatch(r"-?[0-9]+(?:\.[0-9]+)?", value):
            raise BriefRejected("Salesforce amounts must be finite decimal strings or null.")
    else:
        cents = row[field]
        if type(cents) is not int or cents < 0:
            raise BriefRejected("Evidence amounts must be nonnegative integer cents.")
        value = f"{cents // 100:,}.{cents % 100:02}"
    return _money(value, currency), int(currency is None)


def _shown(card, rows, one, many):
    """The first few records in their fixed order. The rest are counted, never dropped silently."""
    more = len(rows) - SHOWN_RECORDS
    if more > 0:
        card.not_read.append(f"CRM: {more} more {many if more > 1 else one} "
                             f"{'were read and are' if more > 1 else 'was read and is'} not shown "
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
        recurring, empty = _amount(account, False, "arr_cents")
        card.missing += empty
        card.value("Annual recurring revenue", recurring, first)
        if account["risk_status"] not in ("healthy", "at_risk"):
            raise BriefRejected("Unknown CRM risk status.")
        reason = f"; reason {card.text(account['risk_reason'])}" if account["risk_reason"] else ""
        card.value("CRM risk", account["risk_status"].replace("_", " ") + reason, first)
        if account["risk_status"] == "at_risk":
            card.signal("CRM marks this account at risk.", first)
    card.value("Account", f"{card.text(account['name'])}; industry {card.text(account['industry'])}", first)
    still_open, open_ids, amounts = [], set(), {}
    for row in opportunities:
        amounts[row["id"]] = _amount(row, salesforce)
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
    for row in _shown(card, ordered, "opportunity", "opportunities"):
        amount, empty = amounts[row["id"]]
        card.missing += empty
        outcome = (f"; closed {str(row['is_closed']).lower()}; won {str(row['is_won']).lower()}"
                   if salesforce else "")
        card.value("Opportunity", f"{card.text(row['name'])}; stage {card.text(row['stage'])}; amount {amount}; "
                   f"close date {row['close_date']}{outcome}", opportunity_numbers[row["id"]])
    if not opportunities:
        card.not_read.append("CRM: no opportunity was returned for this account.")
    for row in contracts:
        if row["end_date"] is not None:
            _day(row["end_date"])
    for row in _shown(card, [row for row in contracts if contract_numbers[row["id"]] not in renewal_numbers],
                      "contract", "contracts"):
        ends = "no end date" if row["end_date"] is None else f"ends {row['end_date']}"
        card.value("Contract", f"status {card.text(row['status'])}; {ends}", contract_numbers[row["id"]])
    for row in _shown(card, contacts, "contact", "contacts"):
        card.value("Contact", f"{card.text(row['name'])}; role {card.text(row['title'])}; "
                   f"email {card.text(row['email'])}", contact_numbers[row["id"]])
    what = "contract end" if salesforce else "renewal"
    when = ("" if days is None else f"The {what} is today" if days == 0 else
            f"The {what} is in {_days(days)}" if days > 0 else f"The {what} date passed {_days(-days)} ago")
    if days is not None and days < 0 and not still_open:
        card.signal(when + ".", *renewal_numbers)
    elif still_open and len(still_open) > SHOWN_RECORDS:
        card.signal(f"{when} and {len(still_open)} opportunities are still open.", *renewal_numbers,
                    *(opportunity_numbers[row["id"]] for row in still_open[:SHOWN_RECORDS]),
                    more=len(still_open) - SHOWN_RECORDS)
    else:
        for row in still_open:
            card.signal(f"{when} and this opportunity is still open at stage {card.quoted(row['stage'])}.",
                        *renewal_numbers, opportunity_numbers[row["id"]])
    no_role = [row for row in contacts if row["title"] in (None, "")]
    if len(no_role) > SHOWN_RECORDS:
        card.signal(f"{len(no_role)} contacts have no role recorded.",
                    *(contact_numbers[row["id"]] for row in no_role[:SHOWN_RECORDS]),
                    more=len(no_role) - SHOWN_RECORDS)
    else:
        for row in no_role:
            card.signal("This contact has no role recorded.", contact_numbers[row["id"]])
    return named


def _sellers(evidence, backend):
    """The operator's list. Demo data adds its own invented sellers, for demo text only."""
    return evidence["sellers"] + (list(SELLERS) if evidence[backend] == "fixture" else [])


def _calls(card, as_of, contacts):
    evidence, sellers = card.evidence, _sellers(card.evidence, "calls_backend")
    notes = card.groups["call_notes"]
    if evidence["notes_status"] == "denied":
        card.not_read.append("Calls: not read. The grant lacks calls:read.")
        return
    if not notes:
        card.not_read.append("Calls: no call record was returned for this account.")
        return
    imported = evidence["calls_backend"] == "imported"

    def role_of(label):
        return speaker_role(label, sellers, contacts)

    first_unit = {}
    for note in sorted(notes, key=lambda row: (str(row["called_at"]), row["id"])):
        if imported:
            manifest = note["manifest"]
            cited = manifest["cited_range"]
            parts: list[tuple] = []
            for cue in manifest["units"]:
                # A WebVTT voice tag names the speaker. A cue identifier does only when it is not a number. In
                # an import that declares it, the name that opens a line does, and is not part of what was said.
                cue_name = None if cue["cue_id"] is None or cue["cue_id"].isdigit() else cue["cue_id"]
                label, text = (named(cue["text"]) or (None, cue["text"]) if "speaker_names" in manifest
                               else (cue_name, cue["text"]))
                spans = voices(text, label) if manifest["format"] == "vtt" else [(label, text)]
                parts += [(label, text, cue["start_ms"], cue["end_ms"]) for label, text in spans]
            first_unit[note["record_ref"]] = card.spoken(
                note, "call_notes", parts, at=note["called_at"], role_of=role_of, transcript=True,
                open_start=cited["first"] > 1)
            whole = cited["first"] == 1 and cited["last"] == manifest["total_units"]
            card.not_read.append(
                f"Calls: one transcript excerpt was read, {manifest['unit_kind']} {cited['first']} to {cited['last']} "
                f"of {manifest['total_units']}. "
                + ("Every other call was not read." if whole else
                   "The rest of that call and every other call were not read."))
        else:
            first_unit[note["record_ref"]] = card.spoken(
                note, "call_notes", [(note.get("speaker"), note["body"], None, None)], at=note["called_at"],
                role_of=role_of)
    if not imported:
        card.not_read.append(f"Calls: {len(notes)} call {'record was' if len(notes) == 1 else 'records were'} read. "
                             "Recordings and anything outside those records were not read.")
    timed = [(moment, note["record_ref"]) for note in notes if (moment := _instant(note["called_at"])) is not None]
    if timed:
        moment, reference = max(timed)
        age = (as_of - moment.date()).days
        if age > _STALE_DAYS:
            card.signal(f"The latest dated call that was read is {_days(age)} old.", first_unit[reference])


def _slack(card):
    evidence, sellers = card.evidence, _sellers(card.evidence, "slack_backend")
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
        first = card.spoken(message, "messages", [(speaker, message["body"], None, None)], at=message["sent_at"],
                            role_of=lambda label: speaker_role(label, sellers, (), slack=True))
        if not live and message["files"]:
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
    """Whole sentences by role: the customer's, then the seller's, then those of no established role.

    The places are dealt one at a time to each role that still has a sentence, newest record first, so one
    side of a conversation never crowds out the other. Sentences keep their order inside one moment.
    """
    spoken = [unit for unit in card.units if unit["kind"] == "sentence"]
    fits = [unit for unit in spoken if len(unit["text"]) <= MAX_SENTENCE and not unit["partial"]]
    def recency(unit):
        moment = _instant(unit["at"])
        return (moment is not None, moment.timestamp() if moment else 0.0)

    queues = {role: sorted((unit for unit in fits if unit["role"] == role), key=recency, reverse=True)
              for role in _ROLES}
    shown = dict.fromkeys(_ROLES, 0)
    for _ in range(SHOWN_SENTENCES):
        for role in _ROLES:
            if shown[role] < len(queues[role]) and sum(shown.values()) < SHOWN_SENTENCES:
                shown[role] += 1
    lines = []
    for unit in (unit for role in _ROLES for unit in queues[role][:shown[role]]):
        where = {"calls": "call", "slack": "Slack"}[unit["source"]]
        who = "no speaker label" if unit["speaker"] is None else "speaker " + json.dumps(unit["speaker"],
                                                                                    ensure_ascii=False)
        when = ("time Missing" if unit["at"] is None else unit["at"] if _PLAIN_INSTANT.fullmatch(unit["at"])
                else "time " + json.dumps(unit["at"], ensure_ascii=False))
        lines.append(f"  [{unit['n']}] {unit['role']} ({unit['basis']}); {where} {when}; {who}")
        lines.append("      " + json.dumps(unit["text"], ensure_ascii=False))
    def by_role(units):
        counts = {role: sum(unit["role"] == role for unit in units) for role in _ROLES}
        return "(" + ", ".join(f"{count} {role}" for role, count in counts.items() if count) + ")"

    cut = [unit for unit in spoken if unit["partial"]]
    more = [unit for role in _ROLES for unit in queues[role][shown[role]:]]
    long = [unit for unit in spoken if len(unit["text"]) > MAX_SENTENCE and not unit["partial"]]
    if cut:
        card.not_read.append(f"Sentences: {len(cut)} cut off at a pause, a change of speaker, or the edge of "
                             f"the excerpt {'was read and is' if len(cut) == 1 else 'were read and are'} "
                             f"not shown {by_role(cut)}.")
    if more:
        card.not_read.append(f"Sentences: {len(more)} more "
                             f"{'sentence was read and is' if len(more) == 1 else 'sentences were read and are'} "
                             f"not shown {by_role(more)}; the card shows {SHOWN_SENTENCES}.")
    if long:
        card.not_read.append(f"Sentences: {len(long)} over {MAX_SENTENCE} characters "
                             f"{'was read and is' if len(long) == 1 else 'were read and are'} not shown "
                             f"{by_role(long)}, because a quotation is never cut.")
    unknown = sum(unit["role"] == "unknown" for unit in spoken)
    if unknown:
        card.not_read.append(f"Speakers: {unknown} of {len(spoken)} "
                             f"{'sentence has' if len(spoken) == 1 else 'sentences have'} no established speaker "
                             "role.")
    return lines


def _build(evidence):
    groups, _ = _validated(evidence)
    as_of = _day(evidence["as_of"])
    card = _Card(evidence, groups)
    contacts = _crm(card, as_of)
    _calls(card, as_of, contacts)
    _slack(card)
    said = _said(card)
    if not said:
        read = sum(unit["kind"] == "sentence" for unit in card.units)
        said = ["  No sentence was read." if not read else
                "  1 sentence was read and is not shown. NOT READ says why." if read == 1 else
                f"  {read} sentences were read and none is shown. NOT READ says why."]
    if card.missing:
        card.not_read.insert(0, "CRM: 1 field is empty on the record and prints as Missing." if card.missing == 1
                             else f"CRM: {card.missing} fields are empty on the record and print as Missing.")
    words = {"mentions": "mention", "links": "link", "controls": "control character",
             "brackets": "bracketed number"}
    replaced = [f"{count} {words[name]}{'' if count == 1 else 's'}"
                for name, count in card.neutralized.items() if count]
    if replaced:
        card.not_read.append("Neutralized in source text: " + ", ".join(replaced) + ".")
    account = groups["accounts"][0]
    name = json.dumps(" ".join(safe_text(account["name"])[0].split()), ensure_ascii=False)
    lines = [f"DEAL CARD: {name} ({evidence['account_id']}) as of {evidence['as_of']}", ""]
    if card.signals:
        lines += ["ATTENTION, from record fields and dates", *card.signals, ""]
    lines += ["RECORD VALUES, from CRM fields and connector arithmetic", *card.values, "",
              "SAID, whole sentences as recorded; not record values",
              *said, "",
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


def trace(evidence: dict, number: int, units: list[dict] | None = None) -> dict:
    """Resolve one number on a card to its source record, its neutralized text, and its audit receipt.

    `units` are a sealed card's own; without them the card is built from the evidence.
    """
    if units is None:
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
    else:  # Neutralized again, so a card sealed under older rules shows nothing today's would have replaced.
        unit.update({key: safe_text(unit[key])[0] for key in ("text", "speaker") if isinstance(unit[key], str)})
    return unit
