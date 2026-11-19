"""Book transcripts take the real import path: parsed, selected, resolved, stored, then briefed."""

from datetime import datetime, timezone
import json
from pathlib import Path
import re
import tempfile
import unicodedata
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.call_workflow import resolve_call_account
from gtm_connector.calls_import import (CallsImportError, inspect_import, list_imports, parse_transcript,
                                        select_excerpt, store_import)
from gtm_connector.citations import BriefRejected, check_brief
from gtm_connector.runtime import Runtime
from tests import varied
from tests.test_results import citations, corruptions


AS_OF = varied.AS_OF.isoformat()
IMPORTED_AT = "2027-01-15T12:00:00Z"
# The limits table of docs/calls.md, restated so that the importer is held to the document.
MAX_FILE, MAX_UNITS, MAX_QUOTE, MAX_RETURNED, MAX_RECORD = 2 * 1024 * 1024, 10_000, 16 * 1024, 256, 128 * 1024
QUOTED_LIMIT = 120 * 1024
# One per transcript of a book: no call time, 60 days before the planning date, 61 days, and 593 days.
CALLED = (None, "2026-11-16T00:00:00Z", "2026-11-15T23:59:59Z", "2025-06-01T08:00:00Z")
STAMP = r"(?:(\d{2,}):)?([0-5]\d):([0-5]\d)\.(\d{3})"
TIMING = re.compile(rf"{STAMP}[ \t]+-->[ \t]+{STAMP}(?:[ \t].*)?")
SRT_STAMP = r"([0-9]{2,9}):([0-5][0-9]):([0-5][0-9])[,.]([0-9]{3})"
SRT_TIMING = re.compile(rf"{SRT_STAMP}[ \t]+-->[ \t]+{SRT_STAMP}(?:[ \t].*)?")
HEADER = re.compile(r"Kind: (?:captions|subtitles|descriptions|chapters|metadata)|Language: [A-Za-z]{2,3}(?:-[A-Za-z0-9]{1,8})*"
                    r"|X-TIMESTAMP-MAP=(?:MPEGTS:[0-9]+,LOCAL:[0-9:.]+|LOCAL:[0-9:.]+,MPEGTS:[0-9]+)")
# How exports label a speaker: a WebVTT voice tag, "Name:" with a timestamp on either side, a time and then
# "Name" before a long dash or between bars, and a name line that ends in a time.
SPEAKER = (re.compile(r"<v(?:\.[^ >]+)? ([^>]+)>"),
           re.compile(r"^(?:\[[\d:]+\] )?([^\d\[:][^\[:]*?)(?: \[[\d:]+\])?: ", re.M),
           re.compile(r"^[\d:]+ (?:\| )?(.+?) [\u2014|] ", re.M),
           re.compile(r"^(.+?) {2,}\d+:\d\d$", re.M),
           re.compile(r"^\[([^\]\d][^\]]*)\]", re.M))
# A plain account for the cases built in code.
PLAIN = {"name": "Size Case Co", "industry": "Software", "renewal_date": "2027-06-30", "arr_cents": 100,
         "risk_status": "healthy", "risk_reason": "", "contacts": [], "opportunities": [], "call_notes": [],
         "slack_messages": []}


def blank_lines(lines, timing):
    """Which lines end a block: empty ones, and lines of spaces that a cue, a comment, or nothing follows."""
    def starts(at):
        return (at >= len(lines) or not lines[at].strip(" \t") or timing.fullmatch(lines[at]) is not None
                or lines[at].startswith(("NOTE", "STYLE", "REGION"))
                or (at + 1 < len(lines) and timing.fullmatch(lines[at + 1]) is not None))
    return [line == "" or (not line.strip(" \t") and starts(at + 1)) for at, line in enumerate(lines)]


def milliseconds(hours, minutes, seconds, millis):
    return ((int(hours or 0) * 60 + int(minutes)) * 60 + int(seconds)) * 1000 + int(millis)


def units(text, kind):
    """The lines or timed cues of an export, read here without the importer. ValueError when it is not valid."""
    lines = text.removeprefix("\ufeff").replace("\r\n", "\n").replace("\r", "\n").split("\n")
    if lines[-1] == "":
        lines.pop()
    if kind == "txt":
        return [{"index": index, "text": line, "cue_id": None, "start_ms": None, "end_ms": None}
                for index, line in enumerate(lines, 1)]
    if kind == "vtt":
        if not lines or not re.fullmatch(r"WEBVTT(?:[ \t].*)?", lines[0]) or "-->" in lines[0]:
            raise ValueError("no WebVTT header")
        body = 1
        while body < len(lines) and HEADER.fullmatch(lines[body]):
            body += 1
        if lines[body:body + 1] != [""]:
            raise ValueError("text under the WebVTT header")
        lines = lines[body + 1:]
    blocks, cues = [[]], []
    for line, blank in zip(lines, blank_lines(lines, TIMING if kind == "vtt" else SRT_TIMING)):
        if not blank:
            blocks[-1].append(line)
        elif blocks[-1]:
            blocks.append([])
    for block in filter(None, blocks):
        if re.match(r"(?:NOTE|STYLE|REGION)(?:[ \t]|$)", block[0]):
            continue
        at = 0 if "-->" in block[0] else 1
        timing = (TIMING if kind == "vtt" else SRT_TIMING).fullmatch(block[at]) if len(block) > at else None
        if timing is None or any("-->" in line for line in block[at + 1:]):
            raise ValueError("a block is not a cue")
        start, end = milliseconds(*timing.groups()[:4]), milliseconds(*timing.groups()[4:])
        cue_id = block[0] if at else None
        # Cues may repeat or reorder start times and may end where they start. None may end before it starts.
        if end < start:
            raise ValueError("a cue ends before it starts")
        cues.append({"index": len(cues) + 1, "text": "\n".join(block[at + 1:]), "cue_id": cue_id,
                     "start_ms": start, "end_ms": end})
    return cues


def default_last(found):
    """The end of the default excerpt: the first 20 units, or fewer when they and one more pass 16 KiB."""
    return next((last for last in range(min(20, len(found)), 0, -1)
                 if len(said(found[:last + 1]).encode()) <= MAX_QUOTE), 0)


def deserved(raw, kind):
    """The refusal docs/calls.md gives this export on a default import, or None when it must be accepted."""
    if len(raw) > MAX_FILE:
        return "calls_import_too_large"
    try:
        text = raw.decode("utf-8")
        found = units(text, kind)
    except ValueError:
        return "calls_import_invalid"
    if (any(unicodedata.category(char) == "Cc" and char not in "\t\r\n" for char in text)
            or not any(unit["text"].strip() for unit in found)):
        return "calls_import_invalid"
    if len(found) > MAX_UNITS:
        return "calls_import_too_large"
    return None if default_last(found) else "calls_excerpt_too_large"


def said(units_):
    """What a brief quotes: each unit's text, under its identifier when that has a space and so names the speaker."""
    return "\n".join(unit["text"] if not unit["cue_id"] or " " not in unit["cue_id"]
                     else unit["cue_id"] + "\n" + unit["text"] for unit in units_)


def speakers(text):
    return {name.strip() for pattern in SPEAKER for name in pattern.findall(text)}


def strings(value):
    """Every string inside a JSON value, keys included."""
    if isinstance(value, str):
        yield value
    elif isinstance(value, dict):
        for key, item in value.items():
            yield key
            yield from strings(item)
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)


def escaped(text):
    return json.dumps(text, ensure_ascii=True)[1:-1]


def load_crm(state, *accounts):
    """Load the CRM rows only. This folder has no fixture calls store, and varied.load leaves an empty one."""
    varied.load(state, *({**rows, "calls": [], "slack": []} for rows in accounts))
    (Path(state) / "calls.sqlite3").unlink()


def serialized(record):
    """The selected record as the limits table measures it: ASCII-escaped JSON."""
    return len(json.dumps(record, sort_keys=True, separators=(",", ":"), ensure_ascii=True))


def vtt(count):
    """A WebVTT file of this many one-millisecond cues."""
    stamp = lambda ms: f"{ms // 60000:02d}:{ms // 1000 % 60:02d}.{ms % 1000:03d}"  # noqa: E731
    return ("WEBVTT\n\n" + "".join(f"{stamp(index)} --> {stamp(index + 1)}\nDana: cue {index}\n\n"
                                   for index in range(count))).encode()


class BookCallsTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)

    def runtime(self, name):
        return Runtime(self.directory / name, calls_backend="imported")

    def import_call(self, runtime, account_id, raw, kind, *, called_at=None, selected=None, reassign=False):
        """The steps of `calls import`, in its order: parse, select, resolve the account, store."""
        parsed = parse_transcript(raw, format=kind)
        excerpt = select_excerpt(parsed, requested_range=selected)
        association = resolve_call_account(runtime, account_id=account_id)
        return store_import(runtime.imports_path, parsed, excerpt, association=association, called_at=called_at,
                            imported_at=IMPORTED_AT, reassign=reassign)

    def imports(self, runtime):
        return list_imports(runtime.imports_path, crm_binding=runtime.calls_binding)["imports"]

    def refused(self, code, runtime, account_id, raw, kind, **options):
        """The import fails with this code, as a usage error or a refusal, and stores nothing."""
        before = self.imports(runtime)
        with self.assertRaises(CallsImportError) as failure:
            self.import_call(runtime, account_id, raw, kind, **options)
        self.assertEqual((failure.exception.code, failure.exception.exit_code),
                         (code, 3 if code == "calls_reassign_required" else 2))
        self.assertEqual(self.imports(runtime), before)

    def brief(self, runtime, rows, imported, raw, kind, *, called_at=None, selected=None):
        """Create the brief and hold it to the transcript as read here and to the rules for every brief."""
        account_id, import_id = rows["crm"][0][1][0], imported["import_id"]
        found = units(raw.decode(), kind)
        first, last = selected or (1, default_last(found))
        cited = max(1, first - 1), min(len(found), last + 1)
        quoted = found[cited[0] - 1:cited[1]]
        body = said(quoted)
        self.assertEqual((imported["total_units"], imported["requested_range"], imported["cited_range"]),
                         (len(found), {"first": first, "last": last}, {"first": cited[0], "last": cited[1]}))
        brief = create_brief(runtime, account_id, as_of=AS_OF)
        self.assertEqual(brief["status"], "accepted")
        self.assertEqual(json.loads((runtime.directory / "briefs" / f"{brief['brief_id']}.json").read_text()), brief)
        lines, evidence = brief["text"].splitlines(), brief["evidence"]
        note, run = f"[calls/call_notes/{import_id}]", f"[run/{brief['brief_id']}]"
        records = {f"[{record['record_ref']}]" for record in evidence["records"]}
        known = records | {f"[audit/{row['audit_id']}]" for row in evidence["receipts"]} | {run}
        for line in lines:
            # Source text never breaks out of its line: one printable bullet that ends in known citations.
            self.assertTrue(line.startswith("- ") and line.isascii() and line.isprintable())
            self.assertTrue(citations(line))
            self.assertLessEqual(set(citations(line)), known)
        self.assertEqual(records, {f"[crm/{table}/{row[0]}]" for table, row in rows["crm"]} | {note})
        # The saved units are the transcript's own, whole and in place: nothing invented, altered, or merged.
        record = next(row for row in evidence["records"] if row["record_ref"].startswith("calls/"))
        self.assertEqual((record["manifest"]["units"], record["body"], record["manifest"]["total_units"]),
                         (quoted, body, len(found)))
        self.assertLessEqual(len(quoted), MAX_RETURNED)
        self.assertLessEqual(len(body.encode()), MAX_QUOTE)
        self.assertLessEqual(serialized(record), MAX_RECORD)
        # The one excerpt line states the ranges, the counts, that it is no summary, and the literal text.
        word = "lines" if kind == "txt" else "cues"
        when = "not provided" if called_at is None else json.dumps(called_at)
        excerpt = (f"- User-supplied transcript excerpt {import_id}: call time {when}; imported at "
                   f'"{IMPORTED_AT}"; requested {word} {first} through {last}; quoted {word} {cited[0]} through '
                   f"{cited[1]} of {len(found)} including available adjacent context. This is an excerpt, not a "
                   f"complete call summary. It says {json.dumps(body)}. {note}")
        self.assertEqual([line for line in lines if note in line and not line.startswith("- Signal: ")], [excerpt])
        # The CRM lines and signals are those of the rows. An old call adds the last signal, a missing time none.
        stated, signals = varied.expected({**rows, "calls": [], "slack": []}, brief["brief_id"])
        if called_at is not None:
            age = (varied.AS_OF - datetime.fromisoformat(called_at.replace("Z", "+00:00"))
                   .astimezone(timezone.utc).date()).days
            if age > 60:
                signals.append(f"- Signal: the selected call excerpt {import_id} is {age} days old as of {AS_OF}. "
                               f"{note} {run}")
        self.assertEqual(lines[:len(signals)], signals)
        self.assertEqual(sum(line.startswith("- Signal: ") for line in lines), len(signals))
        rest = lines[len(signals):]
        self.assertEqual(sorted(line for line in rest if all(ref.startswith("[crm/") for ref in citations(line))),
                         sorted(stated))
        for line in set(rest) - set(stated) - {excerpt}:
            self.assertTrue(line.startswith(varied.DERIVED), line[:80])
        # What was not selected is nowhere in the saved result, as written or as an escaped literal.
        kept = "\0".join([body, *strings([list(row) for _, row in rows["crm"]])])
        saved = "\0".join(strings(brief))
        # A unit of a few characters can be part of an unrelated word, so only longer ones are searched for.
        hidden = [unit["text"] for unit in found[:cited[0] - 1] + found[cited[1]:]
                  if len(unit["text"].strip()) >= 12 and unit["text"] not in kept]
        self.assertEqual([text for text in hidden if text in saved or escaped(text) in saved], [])
        self.assertNotIn("original_text", saved)
        # The checker returns the brief unchanged and refuses each way of spoiling it.
        self.assertEqual(check_brief(brief["text"], evidence), brief["text"])
        for spoiled, candidate in corruptions(brief["text"]):
            with self.subTest(account=account_id, corruption=spoiled), self.assertRaises(BriefRejected):
                check_brief(candidate, evidence)
        return brief, excerpt, hidden

    def test_every_book_transcript_is_briefed_or_refused_for_a_documented_reason(self):
        self.assertGreaterEqual(len(varied.BOOKS), 1)
        searched = 0
        for name in varied.BOOKS:
            shapes = dict(enumerate(varied.read(name), 300))
            carriers = [number for number, shape in shapes.items() if shape.get("transcript")]
            self.assertTrue(carriers, name)
            with self.runtime(name) as runtime:
                for number, shape in shapes.items():
                    rows = varied.demo_rows(number, shape)
                    load_crm(runtime.directory, rows)
                    with self.subTest(book=name, account=shape["name"][:40]):
                        if number not in carriers:
                            # No transcript: the brief stops. The account's fixture-style notes are not used instead.
                            with self.assertRaises(CallsImportError) as missing:
                                create_brief(runtime, f"acc-{number}", as_of=AS_OF)
                            self.assertEqual((missing.exception.code, missing.exception.exit_code),
                                             ("calls_not_imported", 3))
                            continue
                        kind, raw = shape["transcript"]["format"], shape["transcript"]["text"].encode()
                        called_at = CALLED[carriers.index(number) % len(CALLED)]
                        reason = deserved(raw, kind)
                        if reason is not None:
                            # A documented refusal passes, and shows as a skip that names the export.
                            self.refused(reason, runtime, f"acc-{number}", raw, kind, called_at=called_at)
                            self.skipTest(f"{reason} for a {kind} export that starts "
                                          f"{shape['transcript']['text'][:120]!r}")
                        # Anything raised here is a readable export turned away: the message names how it looks.
                        try:
                            imported = self.import_call(runtime, f"acc-{number}", raw, kind, called_at=called_at)
                        except CallsImportError as error:
                            self.fail(f"{error.code} for a valid {kind} export that starts "
                                      f"{shape['transcript']['text'][:120]!r}")
                        self.assertEqual((imported["status"], imported["created"], imported["reassigned"]),
                                         ("imported", True, False))
                        _, excerpt, hidden = self.brief(runtime, rows, imported, raw, kind, called_at=called_at)
                        # Speaker names survive: each label of the quoted part is in the brief's literal.
                        found = units(raw.decode(), kind)[:imported["cited_range"]["last"]]
                        quoted = "\n".join(unit["text"] for unit in found)
                        # An identifier with a space in it is a speaker's name, as Webex writes it.
                        named = [unit["cue_id"] for unit in found if unit["cue_id"] and " " in unit["cue_id"]]
                        for speaker in speakers(quoted) | set(named):
                            self.assertIn(escaped(speaker), excerpt)
                        searched += len(hidden)
        # The search for unselected text had something to look for.
        self.assertGreater(searched, 0)

    def test_repeats_replacements_and_reassignment_follow_the_documented_rules(self):
        for name in varied.BOOKS:
            (first, one), (second, two) = [
                (shape["transcript"]["text"].encode(), shape["transcript"]["format"])
                for shape in varied.read(name) if shape.get("transcript")][:2]
            with self.subTest(book=name), self.runtime(name) as runtime:
                here, there = varied.demo_rows(300, PLAIN), varied.demo_rows(301, PLAIN)
                load_crm(runtime.directory, here, there)
                # The same bytes, account, call time, and range reuse the revision.
                original = self.import_call(runtime, "acc-300", first, one, called_at=CALLED[1])
                again = self.import_call(runtime, "acc-300", first, one, called_at=CALLED[1])
                self.assertEqual((again["status"], again["created"], again["import_id"]),
                                 ("unchanged", False, original["import_id"]))
                self.assertEqual([(row["import_id"], row["active"]) for row in self.imports(runtime)],
                                 [(original["import_id"], True)])
                # Any change creates a new active revision, and the older one stays inspectable.
                later = self.import_call(runtime, "acc-300", first, one, called_at=CALLED[2])
                self.assertEqual((later["status"], later["created"]), ("imported", True))
                self.assertEqual({row["import_id"]: row["active"] for row in self.imports(runtime)},
                                 {original["import_id"]: False, later["import_id"]: True})
                kept = inspect_import(runtime.imports_path, original["import_id"], crm_binding=runtime.calls_binding)
                self.assertEqual((kept["import"]["called_at"], kept["import"]["active"]), (CALLED[1], False))
                # A second transcript becomes the account's one active excerpt. The brief quotes it alone.
                other = self.import_call(runtime, "acc-300", second, two)
                self.assertEqual({row["import_id"] for row in self.imports(runtime) if row["active"]},
                                 {other["import_id"]})
                brief, _, _ = self.brief(runtime, here, other, second, two)
                saved = "\0".join(strings(brief))
                texts = {unit["text"] for unit in units(second.decode(), two)}
                self.assertEqual([unit["text"] for unit in units(first.decode(), one) if len(unit["text"]) >= 12
                                  and unit["text"] not in texts
                                  and (unit["text"] in saved or escaped(unit["text"]) in saved)], [])
                # Known content moves to another account only with --reassign.
                self.refused("calls_reassign_required", runtime, "acc-301", second, two)
                moved = self.import_call(runtime, "acc-301", second, two, reassign=True)
                self.assertEqual((moved["status"], moved["reassigned"]), ("imported", True))
                self.brief(runtime, there, moved, second, two)
                # The account it left has no selected transcript.
                with self.assertRaises(CallsImportError) as missing:
                    create_brief(runtime, "acc-300", as_of=AS_OF)
                self.assertEqual((missing.exception.code, missing.exception.exit_code), ("calls_not_imported", 3))

    def test_size_cases_have_exact_outcomes(self):
        line = lambda size: b"x" * size  # noqa: E731
        numbered = lambda count: b"".join(b"Dana: line %d\n" % index for index in range(count))  # noqa: E731
        megabytes = b"".join(b"Dana %04d " % index + line(245) + b"\n" for index in range(8192))
        thousand = b"\n".join(line(1000) for _ in range(30))
        self.assertEqual(len(megabytes), MAX_FILE)
        accepted = {
            # label: (kind, bytes, explicit range, total units, requested range, quoted range)
            "one line": ("txt", b"Dana: Seats stay at forty.", None, 1, (1, 1), (1, 1)),
            "one cue": ("vtt", vtt(1), None, 1, (1, 1), (1, 1)),
            "2 MiB file": ("txt", megabytes, None, 8192, (1, 20), (1, 21)),
            "10,000 lines": ("txt", numbered(MAX_UNITS), None, MAX_UNITS, (1, 20), (1, 21)),
            "10,000 cues": ("vtt", vtt(MAX_UNITS), None, MAX_UNITS, (1, 20), (1, 21)),
            "16 KiB line": ("txt", line(MAX_QUOTE), None, 1, (1, 1), (1, 1)),
            "16 KiB line of three-byte characters": ("txt", ("\u3042" * 5461 + "a").encode(), None, 1, (1, 1), (1, 1)),
            "16 KiB across context": ("txt", b"\n".join(map(line, (5460, 5460, 5462))), (2, 2), 3, (2, 2), (1, 3)),
            "default shortened to 16 KiB": ("txt", thousand, None, 30, (1, 15), (1, 16)),
            "256 units": ("txt", numbered(300), (1, 255), 300, (1, 255), (1, 256)),
            "last line": ("txt", numbered(40), (40, 40), 40, (40, 40), (39, 40)),
        }
        refused = {
            "empty txt": ("txt", b"", None, "calls_import_invalid"),
            "empty vtt": ("vtt", b"", None, "calls_import_invalid"),
            "header and no cue": ("vtt", b"WEBVTT\n\n", None, "calls_import_invalid"),
            "2 MiB and a byte": ("txt", megabytes + b"x", None, "calls_import_too_large"),
            "10,001 lines": ("txt", numbered(MAX_UNITS + 1), None, "calls_import_too_large"),
            "10,001 cues": ("vtt", vtt(MAX_UNITS + 1), None, "calls_import_too_large"),
            "16 KiB line and a byte": ("txt", line(MAX_QUOTE + 1), None, "calls_excerpt_too_large"),
            "16 KiB across context and a byte": ("txt", b"\n".join(map(line, (5460, 5460, 5463))), (2, 2),
                                                 "calls_excerpt_too_large"),
            "explicit range past 16 KiB": ("txt", thousand, (1, 20), "calls_excerpt_too_large"),
            "257 units": ("txt", numbered(300), (1, 256), "calls_excerpt_too_large"),
            "10 MB txt": ("txt", b"Dana: line\n" * 1_000_000, None, "calls_import_too_large"),
            "10 MB vtt": ("vtt", b"WEBVTT\n\n" + b"00:00.000 --> 00:01.000\nx\n\n" * 400_000, None,
                          "calls_import_too_large"),
        }
        with self.runtime("sizes") as runtime:
            for number, (label, (kind, raw, selected, total, requested, cited)) in enumerate(accepted.items(), 400):
                rows = varied.demo_rows(number, PLAIN)
                load_crm(runtime.directory, rows)
                with self.subTest(case=label):
                    self.assertIsNone(deserved(raw, kind))
                    imported = self.import_call(runtime, f"acc-{number}", raw, kind, selected=selected)
                    self.assertEqual((imported["status"], imported["total_units"], imported["requested_range"],
                                      imported["cited_range"], imported["selection_mode"]),
                                     ("imported", total, dict(zip(("first", "last"), requested)),
                                      dict(zip(("first", "last"), cited)), "explicit" if selected else "default"))
                    self.brief(runtime, rows, imported, raw, kind, selected=selected)
            rows = varied.demo_rows(500, PLAIN)
            load_crm(runtime.directory, rows)
            for label, (kind, raw, selected, code) in refused.items():
                with self.subTest(case=label):
                    if selected is None:
                        self.assertEqual(deserved(raw, kind), code)
                    self.refused(code, runtime, "acc-500", raw, kind, selected=selected)
            # Nothing that was refused left the account with a transcript.
            with self.assertRaises(CallsImportError) as missing:
                create_brief(runtime, "acc-500", as_of=AS_OF)
            self.assertEqual(missing.exception.code, "calls_not_imported")

    def test_a_byte_order_mark_and_crlf_endings_leave_the_units_clean(self):
        text = "\ufeffDana: Caf\u00e9 at nine.\r\n\r\nLee: Agreed.\r\n".encode()
        cues = ("\ufeffWEBVTT\r\n\r\nintro\r\n00:01.000 --> 00:02.000\r\nDana: hello\r\nworld\r\n\r\n"
                "00:02.000 --> 00:03.500\r\nLee: yes\r\n").encode()
        with self.runtime("endings") as runtime:
            for number, kind, raw, expected, literal in (
                    (400, "txt", text, [(1, "Dana: Caf\u00e9 at nine.", None, None, None), (2, "", None, None, None),
                                        (3, "Lee: Agreed.", None, None, None)],
                     r'It says "Dana: Caf\u00e9 at nine.\n\nLee: Agreed.".'),
                    (401, "vtt", cues, [(1, "Dana: hello\nworld", "intro", 1000, 2000),
                                        (2, "Lee: yes", None, 2000, 3500)],
                     r'It says "Dana: hello\nworld\nLee: yes".')):
                rows = varied.demo_rows(number, PLAIN)
                load_crm(runtime.directory, rows)
                with self.subTest(kind=kind):
                    imported = self.import_call(runtime, f"acc-{number}", raw, kind)
                    brief, excerpt, _ = self.brief(runtime, rows, imported, raw, kind)
                    record = next(row for row in brief["evidence"]["records"] if "manifest" in row)
                    self.assertEqual([tuple(unit[key] for key in ("index", "text", "cue_id", "start_ms", "end_ms"))
                                      for unit in record["manifest"]["units"]], expected)
                    self.assertIn(literal, excerpt)
                    # The stored original is the file as given, mark and endings included.
                    full = inspect_import(runtime.imports_path, imported["import_id"],
                                          crm_binding=runtime.calls_binding, full=True)
                    self.assertEqual(full["original_text"].encode(), raw)

    def test_quoted_text_with_its_units_fits_120_kib_and_the_saved_record_stays_under_128(self):
        # One cue whose identifier fills the record. The identifier is saved with the cue and is no quoted text.
        export = lambda size: b"WEBVTT\n\n" + b"a" * size + b"\n00:00.000 --> 00:01.000\nDana: brief\n"  # noqa: E731

        def sizes(runtime, number, size):
            """Return (the quoted text with its units, the whole saved record), each as ASCII-escaped JSON bytes."""
            rows = varied.demo_rows(number, PLAIN)
            load_crm(runtime.directory, rows)
            imported = self.import_call(runtime, f"acc-{number}", export(size), "vtt")
            brief, _, _ = self.brief(runtime, rows, imported, export(size), "vtt")
            record = next(row for row in brief["evidence"]["records"] if "manifest" in row)
            return serialized({"body": record["body"], "units": record["manifest"]["units"]}), serialized(record)

        with self.runtime("record") as runtime:
            # Each identifier byte is one byte of both sizes, so one measurement gives the largest that fits.
            quoted, _ = sizes(runtime, 400, 100_000)
            largest = 100_000 + QUOTED_LIMIT - quoted
            quoted, record = sizes(runtime, 401, largest)
            self.assertEqual(quoted, QUOTED_LIMIT)
            self.assertLess(record, MAX_RECORD)
            rows = varied.demo_rows(402, PLAIN)
            load_crm(runtime.directory, rows)
            self.refused("calls_excerpt_too_large", runtime, "acc-402", export(largest + 1), "vtt")

if __name__ == "__main__":
    unittest.main()
