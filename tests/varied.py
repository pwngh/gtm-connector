"""Data books: invented accounts, some written by other models, shaped for each source.

A book is one JSON file in tests/books. `demo_rows` turns one of its accounts into rows for the three
demo stores. `expected` restates, from those rows alone, what a card must say about the CRM record.
`hold` checks what every card promises about its units and quotations, whatever the data looks like.
"""

from contextlib import closing
from datetime import date, datetime, timezone
import json
import os
from pathlib import Path
import re
import sqlite3
import unicodedata

from gtm_connector import strict_json
from gtm_connector.units import GAP_CODES, safe_text, sentences, trace


AS_OF = date(2027, 1, 15)
# GTM_BOOKS=name,name runs only those books, for a quick pass over new data.
_ONLY = set(filter(None, os.environ.get("GTM_BOOKS", "").split(",")))
BOOKS = {path.stem: path for path in sorted((Path(__file__).parent / "books").glob("*.json"))
         if not _ONLY or path.stem in _ONLY}
STAGES = ("discovery", "evaluation", "proposal", "negotiation", "closed_won", "closed_lost")
HEADINGS = ("ATTENTION, from record fields and dates", "RECORD VALUES, from CRM fields and connector arithmetic",
            "SAID, whole sentences as recorded; not record values", "NOT READ")
SHOWN = 5  # docs/briefs.md: at most five opportunities, contracts, and contacts are shown.
_HEAD = re.compile(r"  \[(\d+)\] (seller|customer|unknown) \((.+?)\); (call|Slack) (.+); "
                   r'(no speaker label|speaker (".*"))\Z')
_CITED = re.compile(r" ((?:\[\d+\])+)(?: and \d+ more)?\Z")
# A WebVTT voice tag is a speaker label, and its closing tag is markup. Neither is part of what was said.
_VOICE_TAG = re.compile(r"<v(?:\.[^\s>]*)*[ \t]+[^<>\n]+>|</v>")
# Every line NOT READ can print: how it opens, its code, and what kind of line it is. Where the pattern has a
# group, the group is the number the line is about ("no" is zero).
_GAP_LINES = (
    (r"CRM: (\d+) fields? (?:is|are) empty", "crm_fields_empty", "note"),
    (r"CRM: annual recurring revenue and renewal risk are not mapped", "crm_unmapped_fields", "not_read"),
    (r"CRM: (\d+) more opportunit", "crm_more_opportunities", "read_not_shown"),
    (r"CRM: (\d+) more contracts?", "crm_more_contracts", "read_not_shown"),
    (r"CRM: (\d+) more contacts?", "crm_more_contacts", "read_not_shown"),
    (r"CRM: (no) opportunity was returned", "crm_no_opportunity", "note"),
    (r"Calls: not read\. The grant lacks calls:read", "calls_denied", "not_read"),
    (r"Calls: (no) call record was returned", "calls_no_record", "note"),
    (r"Calls: one transcript excerpt was read", "calls_excerpt", "not_read"),
    (r"Calls: \d+ call records? (?:was|were) read\.", "calls_outside_records", "not_read"),
    (r"Slack: not requested", "slack_not_requested", "not_read"),
    (r"Slack: not read\. The grant lacks slack:read", "slack_denied", "not_read"),
    (r"Slack: .* from one selected thread", "slack_thread", "not_read"),
    (r"Slack: (no) message was returned", "slack_no_message", "note"),
    (r"Slack: [^.]*\.(?: (\d+) attached files? (?:was|were) not read)?", "slack_channel", "not_read"),
    (r"Sentences: (\d+) cut off", "sentences_cut", "read_not_shown"),
    (r"Sentences: (\d+) more", "sentences_more", "read_not_shown"),
    (r"Sentences: (\d+) over", "sentences_long", "read_not_shown"),
    (r"Speakers: (\d+) of", "speakers_unknown", "note"),
    (r"Neutralized in source text: ", "neutralized", "note"),
)


def named(text):
    """One line or cue of a transcript that declares speaker names as (name, words), read here apart from the connector.

    It is one line. What stands before its first colon is the name: a letter first and not a lower-case one, at
    most 100 characters, no question or exclamation mark, angle bracket, or full stop of another script, and one
    sentence. A space and then words follow the colon. None otherwise.
    """
    lines = [line for line in text.split("\n") if line.strip()]
    name, colon, words = lines[0].partition(":") if len(lines) == 1 else ("", "", "")
    name = name.strip(" \t")
    if (not colon or not name or not name[0].isalpha() or name[0].islower() or len(name) > 100
            or any(mark in name for mark in "!?<>\u3002\uff01\uff1f\u0964\u061f\u06d4") or len(sentences(name)) != 1
            or words[:1] not in (" ", "\t") or not words.strip()):
        return None
    return name, words.lstrip(" \t")


# Styling spans, a ruby reading, and cue timestamps of a WebVTT cue: markup, like a voice tag, and not speech.
CUE_MARKUP = re.compile(r"<rt(?:\.[^\s<>.]*)*>[^<>]*</rt>|</?(?:[biuc]|ruby|rt)(?:\.[^\s<>.]*)*>"
                        r"|</?lang(?:\.[^\s<>.]*)*(?:[ \t][^<>\n]*)?>|<(?:\d{2,}:)?[0-5]\d:[0-5]\d\.\d{3}>")


def read(name):
    return strict_json.loads(BOOKS[name].read_bytes())["accounts"]


def demo_rows(number, account):
    """One account as rows for the crm, calls, and slack stores, under the id acc-<number>."""
    identifier = f"acc-{number}"
    return {
        "crm": [("accounts", (identifier, account["name"], account["industry"], account["renewal_date"],
                              account["arr_cents"], "USD", account["risk_status"], account["risk_reason"]))]
               + [("contacts", (f"con-{number}-{index:03d}", identifier, row["name"], row["title"], row["email"]))
                  for index, row in enumerate(account["contacts"])]
               + [("opportunities", (f"opp-{number}-{index:03d}", identifier, row["name"], row["stage"],
                                     row["amount_cents"], "USD", row["close_date"]))
                  for index, row in enumerate(account["opportunities"])],
        "calls": [("call_notes", (f"note-{number}-{index:03d}", identifier, row["called_at"], None, row["body"]))
                  for index, row in enumerate(account["call_notes"])],
        "slack": [("messages", (f"msg-{number}-{index:03d}", identifier, row["channel"], row["author"],
                                row["sent_at"], row["body"], 0))
                  for index, row in enumerate(account["slack_messages"])],
    }


def load(state, *accounts):
    for database in ("crm", "calls", "slack"):
        with closing(sqlite3.connect(Path(state) / f"{database}.sqlite3")) as connection, connection:
            for rows in accounts:
                for table, row in rows[database]:
                    connection.execute(f"INSERT INTO {table} VALUES ({', '.join('?' * len(row))})", row)


def quoted(value):
    """One source text field as a card prints it: neutralized, on one line, as a literal. Empty is Missing."""
    return "Missing" if value in (None, "") else json.dumps(" ".join(safe_text(value)[0].split()), ensure_ascii=False)


def days(count):
    return f"{count} day" if count == 1 else f"{count} days"


def instant(text):
    try:
        moment = datetime.fromisoformat(text.replace("Z", "+00:00"))
        return moment.astimezone(timezone.utc) if moment.tzinfo else None
    except (AttributeError, ValueError, OverflowError):
        return None


def sections(case, text):
    """The lines under each heading of a card. Source text never starts a line, so only the connector's do."""
    case.assertTrue(text.endswith("\n"))
    lines = text[:-1].split("\n")
    flush = [line for line in lines if line and not line.startswith("  ")]
    case.assertRegex(flush[0], r'\ADEAL CARD: ".*" \(acc-[A-Za-z0-9-]+\) as of \d{4}-\d\d-\d\d\Z')
    json.loads(flush[0][len("DEAL CARD: "):flush[0].rindex(" (acc-")])
    case.assertIn(flush[1:], (list(HEADINGS), list(HEADINGS[1:])))
    found = {heading.split(",")[0]: [] for heading in HEADINGS}
    current = None
    for line in lines[1:]:
        if line in HEADINGS:
            current = found[line.split(",")[0]]
        elif line:
            current.append(line)
    return found


def cited(line):
    """The unit numbers a record-value or signal line ends in."""
    return [int(number) for number in re.findall(r"\d+", _CITED.search(line).group(1))]


def uncited(line):
    """A card line without the citations the connector put at its end."""
    return _CITED.sub("", line)


def said(card):
    """Each shown sentence as (number, role, basis, source, time, speaker literal or None, text)."""
    lines = card["SAID"]
    if len(lines) == 1:
        # One line says that nothing was read, or that what was read is not shown.
        assert re.fullmatch(r"  (No sentence was read\.|1 sentence was read and is not shown\. NOT READ says why\."
                            r"|\d+ sentences were read and none is shown\. NOT READ says why\.)", lines[0]), lines
        return []
    shown = []
    for head, quote in zip(lines[::2], lines[1::2], strict=True):
        number, role, basis, source, when, _, speaker = _HEAD.fullmatch(head).groups()
        shown.append((int(number), role, basis, source, when, speaker, json.loads(quote[6:])))
    return shown


def stale_call(calls, brief):
    """The signal for the newest dated call when it is more than 60 days old. `calls` is (record_ref, time)."""
    timed = [(moment, reference) for reference, called_at in calls if (moment := instant(called_at)) is not None]
    if not timed:
        return []
    moment, reference = max(timed)
    # Every call resolves: its first sentence, or the call itself when it holds none.
    first = next(unit["n"] for unit in brief["units"] if unit["record_ref"] == reference)
    age = (AS_OF - moment.date()).days
    return [f"  The latest dated call that was read is {days(age)} old. [{first}]"] if age > 60 else []


def expected(rows, brief):
    """The demo CRM part of a card, restated from the rows: fixed value lines, one line per opportunity and per
    contact, and the signals in order. Numbers follow docs/briefs.md: the account, then contacts and
    opportunities by ID."""
    (_, (_, name, industry, renewal, arr, currency, risk, reason)), *others = rows["crm"]
    contacts = sorted(row for table, row in others if table == "contacts")
    deals = sorted(row for table, row in others if table == "opportunities")
    left = (date.fromisoformat(renewal) - AS_OF).days
    timing = f"in {days(left)}" if left > 0 else f"{days(-left)} ago" if left < 0 else "today"
    when = f"The renewal date passed {days(-left)} ago" if left < 0 else f"The renewal is {timing}"
    fixed = [f"  Renewal date: {renewal}, {timing} [1]",
             f"  Annual recurring revenue: {currency} {arr // 100:,}.{arr % 100:02} [1]",
             f"  CRM risk: {risk.replace('_', ' ')}{f'; reason {quoted(reason)}' if reason else ''} [1]",
             f"  Account: {quoted(name)}; industry {quoted(industry)} [1]"]
    signals = ["  CRM marks this account at risk. [1]"] if risk == "at_risk" else []
    people, roles = [], []
    for number, (_, _, person, title, email) in enumerate(contacts, 2):
        people.append(f"  Contact: {quoted(person)}; role {quoted(title)}; email {quoted(email)} [{number}]")
        if not title:
            roles.append(number)
    offers, still_open = [], []
    for number, (_, _, deal, stage, cents, code, close) in enumerate(deals, 2 + len(contacts)):
        offers.append(f"  Opportunity: {quoted(deal)}; stage {quoted(stage)}; amount {code} {cents // 100:,}."
                      f"{cents % 100:02}; close date {close} [{number}]")
        if left <= 30 and not stage.startswith("closed_"):
            still_open.append((number, stage))
    # docs/briefs.md, Signals: more than five records raising one signal are counted in one line, which
    # cites the first five of them.
    if left < 0 and not still_open:
        signals.append(f"  {when}. [1]")
    elif len(still_open) > SHOWN:
        signals.append(f"  {when} and {len(still_open)} opportunities are still open. [1]"
                       + counted(number for number, _ in still_open))
    else:
        signals += [f"  {when} and this opportunity is still open at stage {quoted(stage)}. [1][{number}]"
                    for number, stage in still_open]
    signals += ([f"  {len(roles)} contacts have no role recorded. " + counted(roles)] if len(roles) > SHOWN
                else [f"  This contact has no role recorded. [{number}]" for number in roles])
    signals += stale_call([(f"calls/call_notes/{row[0]}", row[2]) for _, row in rows["calls"]], brief)
    return {"fixed": fixed, "opportunities": offers, "contacts": people, "signals": signals}


def counted(numbers):
    """The citations of a counted signal: its first five units, then how many more raised it."""
    numbers = list(numbers)
    return "".join(f"[{number}]" for number in numbers[:SHOWN]) + f" and {len(numbers) - SHOWN} more"


def more(count, plural):
    """The NOT READ line for records past the five shown, `count` in all."""
    extra = count - SHOWN
    one = plural[:-3] + "y" if plural.endswith("ies") else plural[:-1]
    return (f"  CRM: {extra} more {plural if extra > 1 else one} "
            f"{'were read and are' if extra > 1 else 'was read and is'} not shown ({count} in all).")


def hold_values(case, card, fixed, groups):
    """The fixed lines open RECORD VALUES. Each group of (label, plural, lines) shows every line when there are
    five or fewer. Past five it shows five of them and counts the rest under NOT READ."""
    lines = card["RECORD VALUES"]
    case.assertEqual(lines[:len(fixed)], fixed)
    rest = lines[len(fixed):]
    for label, plural, wanted in groups:
        shown = [line for line in rest if line.startswith(f"  {label}: ")]
        rest = [line for line in rest if line not in shown]
        one = plural[:-3] + "y" if plural.endswith("ies") else plural[:-1]
        unshown = [line for line in card["NOT READ"] if re.match(rf"  CRM: \d+ more ({plural}|{one}) ", line)]
        if len(wanted) <= SHOWN:
            case.assertEqual(sorted(shown), sorted(wanted))
            case.assertEqual(unshown, [])
        else:
            case.assertEqual(len(set(shown)), SHOWN)
            case.assertLessEqual(set(shown), set(wanted))
            case.assertEqual(unshown, [more(len(wanted), plural)])
    case.assertEqual(rest, [])


def hold(case, brief):
    """What every card promises: connector-written lines only, every record a unit, every sentence whole,
    and every number resolving to its record and the receipt of the read that returned it."""
    case.assertEqual(brief["status"], "accepted")
    units, evidence, text = brief["units"], brief["evidence"], brief["text"]
    case.assertEqual([unit["n"] for unit in units], list(range(1, len(units) + 1)))
    card = sections(case, text)
    # Nothing invisible or line-breaking from a source reaches a line, in the card or in a unit.
    shown = text + "".join(str(unit.get(key)) for unit in units for key in ("text", "speaker", "at"))
    case.assertEqual({character for character in set(shown) - {"\n"}
                      if unicodedata.category(character) in ("Cc", "Cf", "Cs", "Zl", "Zp")}, set())
    for live in ("<!", "<@", "http://", "https://"):
        case.assertNotIn(live, shown)
    # Every record that was read is one unit, or one unit for each whole sentence of its text and nothing more.
    by_record: dict[str, list] = {}
    for unit in units:
        by_record.setdefault(unit["record_ref"], []).append(unit)
    receipts = {receipt["tool"]: receipt for receipt in evidence["receipts"]}
    for record in evidence["records"]:
        own = by_record.pop(record["record_ref"], [])
        for unit in own:
            case.assertIn(record["record_ref"], receipts[unit["tool"]]["rows"])
        if record["record_ref"].startswith("crm/"):
            case.assertEqual([unit["kind"] for unit in own], ["record"])
            continue
        # What was said, in order, with nothing lost and nothing added. A text record with no sentence is
        # still one unit, so it resolves.
        if "manifest" in record:
            vtt = record["manifest"]["format"] == "vtt"
            # In an import that declares speaker names, the name that opens a line is not part of what was said.
            texts = [cue["text"] for cue in record["manifest"]["units"]]
            if "speaker_names" in record["manifest"]:
                texts = [named(text)[1] if text.strip() else text for text in texts]
            pieces = [CUE_MARKUP.sub("", _VOICE_TAG.sub("\0", text)).replace("\0", "") if vtt else text
                      for text in texts]
        else:
            pieces = [record["body"]]
        whole = " ".join(filter(None, (" ".join(safe_text(piece)[0].split()) for piece in pieces)))
        if not whole:
            case.assertEqual([unit["kind"] for unit in own], ["record"])
            continue
        case.assertEqual({unit["kind"] for unit in own}, {"sentence"})
        case.assertEqual(" ".join(unit["text"] for unit in own), whole)
        for unit in own:
            # A bracketed integer is the connector's citation form. No sentence holds one.
            case.assertNotRegex(unit["text"], r"\[\d+\]")
    case.assertEqual(by_record, {})
    # A value or signal ends in the numbers of records. A quotation is exactly one sentence unit, with its
    # role, basis, time, and speaker, newest first and never more than twelve.
    for line in card["ATTENTION"] + card["RECORD VALUES"]:
        for number in cited(line):
            case.assertIn(units[number - 1]["kind"], ("record", "sentence"))
    quotes = said(card)
    case.assertLessEqual(len(quotes), 12)
    for number, role, basis, source, when, speaker, sentence in quotes:
        unit = units[number - 1]
        case.assertEqual((unit["kind"], unit["text"], unit["role"], unit["basis"]), ("sentence", sentence, role, basis))
        case.assertEqual({"calls": "call", "slack": "Slack"}[unit["source"]], source)
        case.assertEqual(None if speaker is None else json.loads(speaker), unit["speaker"])
        case.assertLessEqual(len(sentence), 300)
        case.assertFalse(unit["partial"])
    # The customer's sentences come first, then the seller's, then the rest. Inside a role the newest record
    # is first and a sentence with no time is last. No role is left out while another has two places.
    roles = [role for _, role, *_ in quotes]
    case.assertEqual(roles, sorted(roles, key=("customer", "seller", "unknown").index))
    for role in set(roles):
        moments = [instant(units[number - 1]["at"]) for number, said_by, *_ in quotes if said_by == role]
        dated = [moment for moment in moments if moment is not None]
        case.assertEqual(dated, sorted(dated, reverse=True))
        case.assertEqual(moments[:len(dated)], dated)
    fit = {role: sum(unit["kind"] == "sentence" and unit["role"] == role and not unit["partial"]
                     and len(unit["text"]) <= 300 for unit in units) for role in ("customer", "seller", "unknown")}
    for role, count in fit.items():
        case.assertGreaterEqual(roles.count(role), min(count, 12 // sum(map(bool, fit.values())) if count else 0))
    # What a tool reads says what the card prints: the sentences marked shown are the ones under SAID, each of
    # the others says why it is not, and NOT READ is the text of each gap, in order, under a code of the list.
    spoken = [unit for unit in units if unit["kind"] == "sentence"]
    case.assertEqual(sorted(unit["n"] for unit in spoken if unit["shown"] == "yes"), sorted(q[0] for q in quotes))
    for unit in spoken:
        case.assertEqual(unit["shown"] in ("yes", "more"), not unit["partial"] and len(unit["text"]) <= 300)
        case.assertEqual(unit["shown"] == "cut", unit["partial"])
        # A customer's sentence names its contact as data: the number of a contact record, the one in its basis.
        case.assertEqual(unit["contact"] is not None, unit["role"] == "customer")
        if unit["contact"] is not None:
            case.assertEqual((units[unit["contact"] - 1]["record_ref"].split("/")[:2], unit["basis"]),
                             (["crm", "contacts"], f"label matches contact [{unit['contact']}]"))
    case.assertEqual(card["NOT READ"], ["  " + gap["text"] for gap in brief["gaps"]])
    case.assertEqual(len({gap["code"] for gap in brief["gaps"]}), len(brief["gaps"]))
    for gap in brief["gaps"]:
        case.assertEqual(set(gap), {"code", "disposition", "count", "by_role", "text"})
        # Each line has the code, kind and number that this table, written apart from the connector's, gives it.
        pattern, code, disposition = next(row for row in _GAP_LINES if re.match(row[0], gap["text"]))
        found = re.match(pattern, gap["text"])
        numbers = [int(number) for number in re.findall(r"\d+", gap["text"])]
        count = (sum(numbers) if code == "neutralized" else sum(numbers[-3:]) if code == "slack_thread"
                 else None if not found.groups() or found.group(1) is None
                 else 0 if found.group(1) == "no" else int(found.group(1)))
        case.assertEqual((gap["code"], gap["disposition"], gap["count"], GAP_CODES[code]),
                         (code, disposition, count, disposition), gap["text"])
        # A line that counts sentences says whose they are.
        which = {"sentences_cut": "cut", "sentences_more": "more", "sentences_long": "long"}.get(code)
        counted = [unit for unit in spoken if unit["shown"] == which or code == "speakers_unknown"]
        case.assertEqual(gap["by_role"], None if which is None and code != "speakers_unknown" else {
            role: sum(unit["role"] == role for unit in counted if code != "speakers_unknown" or role == "unknown")
            for role in ("customer", "seller", "unknown")})
    # A sentence of an imported transcript says which cues or lines it lies in, inside the cited range, with
    # times that cover them. No other sentence has a place.
    manifests = {record["record_ref"]: record["manifest"] for record in evidence["records"] if "manifest" in record}
    for unit in spoken:
        manifest, span = manifests.get(unit["record_ref"]), unit["span"]
        case.assertEqual(span is None, manifest is None)
        if manifest is not None:
            cues = {cue["index"]: cue for cue in manifest["units"]}
            case.assertLessEqual(manifest["cited_range"]["first"], span["first"])
            case.assertLessEqual(span["first"], span["last"])
            case.assertLessEqual(span["last"], manifest["cited_range"]["last"])
            lies = [cues[index] for index in range(span["first"], span["last"] + 1)]
            if manifest["format"] == "txt":
                case.assertEqual((span["start_ms"], span["end_ms"]), (None, None))
            else:
                case.assertLessEqual(min(cue["start_ms"] for cue in lies), span["start_ms"])
                case.assertLessEqual(span["start_ms"], span["end_ms"])
                case.assertLessEqual(span["end_ms"], max(cue["end_ms"] for cue in lies))
    for why in ("cut", "more", "long"):
        counted = [gap["count"] for gap in brief["gaps"] if gap["code"] == "sentences_" + why]
        case.assertEqual(counted, [count] if (count := sum(unit["shown"] == why for unit in spoken)) else [])
    # A few numbers, traced from the saved evidence alone, name the same record and a finished receipt.
    for number in sorted({1, len(units), *(quote[0] for quote in quotes[:2])}):
        traced = trace(evidence, number)
        case.assertEqual((traced["n"], traced["record_ref"]), (number, units[number - 1]["record_ref"]))
        case.assertEqual(traced["receipt"]["audit_id"], receipts[units[number - 1]["tool"]]["audit_id"])
    return card
