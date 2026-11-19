"""Book Slack threads are served by an invented Slack, read by the live reader, and quoted in briefs."""

from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import asyncio
import hashlib
import json
import math
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector import slack_auth, slack_reader
from gtm_connector.briefing import create_brief
from gtm_connector.runtime import Runtime
from gtm_connector.slack_mapping import save_slack_mapping
from gtm_connector.slack_reader import SlackReadError
from gtm_connector.slack_workflow import resolve_slack_account
from test_slack_auth import Clock, saved_document, seed
from tests import varied


AS_OF = varied.AS_OF.isoformat()
EPOCH = datetime(1970, 1, 1, tzinfo=timezone.utc)
# The read happens the day after the books' planning date, so every book message is already sent.
READ_AT = datetime(2027, 1, 16, tzinfo=timezone.utc)
READER = slack_reader.SlackThreadReader
REPLIES = "https://slack.com/api/conversations.replies"
# The limits table of docs/slack.md, restated so that the reader is held to the document.
MAX_ATTEMPTS, MAX_RESPONSE, MAX_ROWS, MAX_TEXT, MAX_ALL_TEXT, MAX_RESULT, MAX_CURSOR = (
    10, 1024 * 1024, 100, 16 * 1024, 64 * 1024, 256 * 1024, 2048)
# The exit code docs/slack.md gives each reason a read can stop for here.
EXIT = {"slack_mapping_missing": 3, "slack_resource_limit": 3, "slack_output_too_large": 3,
        "slack_thread_incomplete": 3, "slack_thread_unavailable": 3, "slack_response_invalid": 5,
        "slack_rate_limited": 5}
PLAIN = {"name": "Size Case Co", "industry": "Software", "renewal_date": "2027-06-30", "arr_cents": 100,
         "risk_status": "healthy", "risk_reason": "", "contacts": [], "opportunities": [], "call_notes": [],
         "slack_messages": []}
HIDDEN = "INVENTED_NON_TEXT_CONTENT"
SLOW = (429, {"Retry-After": "0"})


def micros(ts):
    seconds, fraction = ts.split(".")
    return int(seconds) * 1_000_000 + int(fraction)


def stamp(count):
    return f"{count // 1_000_000}.{count % 1_000_000:06d}"


def thread(messages):
    """Book messages as one Slack thread, oldest first: (channel, rows). ts is the second of sent_at."""
    rows, latest = [], -1
    ordered = sorted((datetime.fromisoformat(message["sent_at"].replace("Z", "+00:00")), index)
                     for index, message in enumerate(messages))
    for moment, index in ordered:
        # Slack keeps ts unique in a channel, so a second message in the same instant moves on by a microsecond.
        latest = max((moment - EPOCH) // timedelta(microseconds=1), latest + 1)
        author = hashlib.sha256(messages[index]["author"].encode()).hexdigest()[:10].upper()
        rows.append({"type": "message", "ts": stamp(latest), "user": "U" + author, "team": "T12345678",
                     "text": messages[index]["body"]})
    for row in rows:
        row["thread_ts"] = rows[0]["ts"]
    rows[0]["reply_count"] = len(rows) - 1
    return rows


def replies(texts, start=1_800_000_000):
    """A thread built in code: one plain message per text, a second apart, on 2027-01-15."""
    return [{"type": "message", "ts": f"{start + index}.000000", "thread_ts": f"{start}.000000",
             "user": "U0000000001", "text": text} for index, text in enumerate(texts)]


def refusal(rows, cutoff):
    """The reason docs/slack.md gives for refusing this whole thread, or None when it must be read."""
    sizes = [len(row["text"].encode()) for row in rows]
    if micros(rows[0]["ts"]) > micros(cutoff):
        return "slack_thread_unavailable"
    if len(rows) > MAX_ROWS:
        return "slack_resource_limit"
    if max(sizes) > MAX_TEXT or sum(sizes) > MAX_ALL_TEXT:
        return "slack_output_too_large"
    # A message newer than the read's cutoff means the thread was still changing.
    return "slack_thread_incomplete" if micros(rows[-1]["ts"]) > micros(cutoff) else None


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


def load(state, *accounts):
    """Load the CRM and call rows. This folder has no fixture Slack store, and varied.load leaves an empty one."""
    varied.load(state, *({**rows, "slack": []} for rows in accounts))
    (Path(state) / "slack.sqlite3").unlink()


class Upstream:
    """An invented Slack: threads served as conversations.replies pages, with scripted answers in between."""

    def __init__(self):
        self.threads, self.requests = {}, []

    def add(self, channel, rows, *, page=MAX_ROWS, first=(), cursor_size=0, repeat_parent=False):
        """Serve rows as one thread. `first` maps a page's first row to answers that are sent before the page."""
        self.threads[channel, rows[0]["ts"]] = {
            "rows": rows, "page": page, "first": {start: list(answers) for start, answers in dict(first).items()},
            "cursor_size": cursor_size, "repeat_parent": repeat_parent}

    def page(self, channel, parent, *, latest, start=0, limit=MAX_ROWS):
        """The bytes of one page. Later pages may open with the parent again."""
        found = self.threads[channel, parent]
        rows = [row for row in found["rows"] if micros(row["ts"]) <= micros(latest)]
        again = [rows[0]] if start and found["repeat_parent"] else []
        end = start + min(found["page"], limit) - len(again)
        more = end < len(rows)
        cursor = f"page-{end}".ljust(found["cursor_size"], "=") if more else ""
        document = {"ok": True, "messages": again + rows[start:end], "has_more": more,
                    "response_metadata": {"next_cursor": cursor}}
        return json.dumps(document, ensure_ascii=False, separators=(",", ":")).encode()

    def __call__(self, request):
        self.requests.append(request)
        params = request.url.params
        found = self.threads.get((params.get("channel"), params.get("ts")))
        if (request.method, request.url.copy_with(query=None)) != ("GET", REPLIES) or found is None:
            return httpx2.Response(200, json={"ok": False, "error": "thread_not_found"})
        start = int(params.get("cursor", "page-0").rstrip("=").removeprefix("page-"))
        if found["first"].get(start):
            status, headers = found["first"][start].pop(0)
            return httpx2.Response(status, headers=headers, json={"ok": False, "error": "ratelimited"})
        return httpx2.Response(200, content=self.page(params["channel"], params["ts"], latest=params["latest"],
                                                      start=start, limit=int(params["limit"])))


class BookSlackTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.directory = Path(directory.name)
        self.connection = self.directory / "connection"
        # The saved connection is checked against the real clock when a runtime starts. The reader's clock is
        # never earlier than it.
        now = datetime.now(timezone.utc).replace(microsecond=0)
        seed(self.connection, saved_document(now, rotating=False))
        self.clock = Clock()
        self.clock.wall = max(now, READ_AT)
        self.cutoff = stamp((self.clock.wall - EPOCH) // timedelta(microseconds=1))

    @contextmanager
    def runtime(self, name, upstream):
        transport = httpx2.MockTransport(upstream)

        def reader(mapping_path, **options):
            return READER(mapping_path, **options, _clock=self.clock,
                          _provider=slack_reader.SlackRepliesProvider(_transport=transport),
                          _auth_provider=slack_auth.SlackProvider(_transport=transport, _clock=self.clock))

        with patch("gtm_connector.slack_reader.SlackThreadReader", reader), \
                Runtime(self.directory / name, slack_backend="slack", slack_connection_dir=self.connection) as runtime:
            yield runtime

    def serve(self, runtime, upstream, account_id, rows, **options):
        """Put the thread upstream and map it to the account, as `slack map` does. Returns the channel."""
        channel = "C0000000" + account_id[-3:]
        upstream.add(channel, rows, **options)
        saved = save_slack_mapping(
            runtime.slack_mapping_path, association=resolve_slack_account(runtime, account_id=account_id),
            slack_binding=runtime.slack_binding, selection={"channel_id": channel, "thread_ts": rows[0]["ts"]},
            selected_at=self.clock.now().isoformat().replace("+00:00", "Z"))
        self.assertTrue(saved["changed"])
        return channel

    def stopped(self, code, runtime, upstream, account_id, *, requests=None):
        """The read fails with this reason and the brief stops: nothing is saved and the receipt names no row."""
        saved = set((runtime.directory / "briefs").glob("*"))
        before = len(upstream.requests)
        with self.assertRaises(SlackReadError) as failure:
            create_brief(runtime, account_id, as_of=AS_OF, include_slack=True)
        self.assertEqual((failure.exception.code, failure.exception.exit_code), (code, EXIT[code]))
        self.assertEqual(set((runtime.directory / "briefs").glob("*")), saved)
        receipt = runtime.connector.audit.read()[-1]
        self.assertEqual((receipt["tool"], receipt["arguments"], receipt["status"], receipt["rows"]),
                         ("read_slack_messages", {"account_id": account_id}, "error", []))
        if requests is not None:
            self.assertEqual(len(upstream.requests) - before, requests)

    def brief(self, runtime, upstream, rows, channel, served, *, pages, others=()):
        """Create the card and hold it to the thread as it was served and to what every card promises."""
        account_id = rows["crm"][0][1][0]
        asked = len(upstream.requests)
        brief = create_brief(runtime, account_id, as_of=AS_OF, include_slack=True)
        self.assertEqual(json.loads((runtime.directory / "briefs" / f"{brief['brief_id']}.json").read_text()), brief)
        card, evidence, units = varied.hold(self, brief), brief["evidence"], brief["units"]
        # Only this thread was asked for, with the saved credential, one request per page or retry.
        for request in upstream.requests[asked:]:
            self.assertEqual((request.method, request.url.path, request.url.params["channel"], request.url.params["ts"],
                              request.url.params["latest"], request.headers["authorization"][:7]),
                             ("GET", "/api/conversations.replies", channel, served[0]["ts"], self.cutoff, "Bearer "))
        counts = evidence["thread_snapshot"]["counts"]
        self.assertEqual((counts["pages"], counts["messages"], counts["http_attempts"]),
                         (pages, len(served), len(upstream.requests) - asked))
        # Every message is one record: literal text, thread order, and the time and author label the reader gives.
        records = {record["record_ref"] for record in evidence["records"]}
        messages = [record for record in evidence["records"] if record["record_ref"].startswith("slack/")]
        label = lambda row: ("Deleted Slack message" if row.get("subtype") == "tombstone"  # noqa: E731
                             else "Slack app" if "bot_id" in row or row.get("subtype") == "bot_message"
                             else "Slack participant" if "user" in row else "Unknown Slack author")
        sent = lambda row: (EPOCH + timedelta(microseconds=micros(row["ts"]))).isoformat().replace(  # noqa: E731
            "+00:00", "Z")
        self.assertEqual([(row["body"], row["sent_at"], row["author"], row["channel"]) for row in messages],
                         [(row["text"], sent(row), label(row), channel) for row in served])
        self.assertEqual(records - {row["record_ref"] for row in messages},
                         {f"{store}/{table}/{row[0]}" for store in ("crm", "calls") for table, row in rows[store]})
        # The sentences of the thread close the units in thread order. Each carries the message's time and the raw
        # user ID as its speaker, and no Slack author is a customer.
        spoken = [unit for unit in units if unit["source"] == "slack"]
        self.assertEqual(units[len(units) - len(spoken):], spoken)
        order = [record["record_ref"] for record in messages]
        self.assertEqual([order.index(unit["record_ref"]) for unit in spoken],
                         sorted(order.index(unit["record_ref"]) for unit in spoken))
        by_ref = {record["record_ref"]: row for row, record in zip(served, messages)}
        for unit in spoken:
            row = by_ref[unit["record_ref"]]
            self.assertEqual((unit["at"], unit["speaker"], unit["role"]), (sent(row), row.get("user"), "unknown"))
        # The card says what the read covered and what it left out: the omission counts and other threads.
        omitted = [sum(len(row.get(key, [])) for row in served) for key in ("files", "attachments", "blocks")]
        self.assertIn(
            f"  Slack: {len(served)} {'message was' if len(served) == 1 else 'messages were'} read from one selected "
            f"thread, and paging completed. Not read: files ({omitted[0]}), attachments ({omitted[1]}), "
            f"blocks ({omitted[2]}), earlier versions of edited messages, and every other thread.", card["NOT READ"])
        times = {sent(row) for row in served}
        self.assertLessEqual({when for _, _, _, source, when, _, _ in varied.said(card) if source == "Slack"}, times)
        # The CRM values and the signals are those of the rows.
        wanted = varied.expected({**rows, "slack": []}, brief)
        self.assertEqual(card["ATTENTION"], wanted["signals"])
        varied.hold_values(self, card, wanted["fixed"], (("Opportunity", "opportunities", wanted["opportunities"]),
                                                         ("Contact", "contacts", wanted["contacts"])))
        # Nothing from another thread, and nothing that is not message text, is in the saved result.
        saved = "\0".join(strings(brief))
        self.assertEqual([text for text in others if text in saved or escaped(text) in saved], [])
        self.assertNotIn(HIDDEN, saved)
        return brief

    def test_every_book_thread_is_quoted_whole_and_in_order(self):
        self.assertGreaterEqual(len(varied.BOOKS), 1)
        for name in varied.BOOKS:
            shapes = dict(enumerate(varied.read(name), 300))
            upstream = Upstream()
            with self.runtime(name, upstream) as runtime:
                rows = {number: varied.demo_rows(number, shape) for number, shape in shapes.items()}
                load(runtime.directory, *rows.values())
                # Every thread of the book is upstream and mapped before the first brief is made.
                threads = {number: thread(shape["slack_messages"])
                           for number, shape in shapes.items() if shape["slack_messages"]}
                sizes = {number: math.ceil(len(served) / 3) for number, served in threads.items()}
                channels = {number: self.serve(runtime, upstream, f"acc-{number}", served, page=sizes[number])
                            for number, served in threads.items()}
                for number, shape in shapes.items():
                    account_id = f"acc-{number}"
                    with self.subTest(book=name, account=shape["name"][:40]):
                        if number not in threads:
                            # No thread, no mapping: the read is refused before Slack is asked, and the brief stops.
                            self.stopped("slack_mapping_missing", runtime, upstream, account_id, requests=0)
                            continue
                        served = threads[number]
                        reason = refusal(served, self.cutoff)
                        if reason is not None:
                            self.stopped(reason, runtime, upstream, account_id)
                            continue
                        kept = "\0".join(strings([list(row) for store in rows[number].values() for _, row in store]))
                        # A short message such as "ack" is part of longer words, so only longer ones are searched for.
                        others = {row["text"] for other, thread_rows in threads.items() if other != number
                                  for row in thread_rows if len(row["text"]) >= 12 and row["text"] not in kept}
                        self.brief(runtime, upstream, rows[number], channels[number], served,
                                   pages=math.ceil(len(served) / sizes[number]), others=others)

    def test_threads_as_the_slack_api_returns_them_are_read_whole(self):
        # tests/slack_threads holds conversations.replies pages written by three models: full message objects
        # with blocks, reactions, edits, files, attachments, apps, broadcasts, and a deleted parent.
        folder = Path(__file__).parent / "slack_threads"
        read = 0
        for path in sorted(folder.glob("*.json")):
            for index, thread_ in enumerate(json.loads(path.read_text())["threads"]):
                rows, seen = [], set()
                for page in thread_["pages"]:
                    for message in page["messages"]:
                        if message["ts"] not in seen:
                            seen.add(message["ts"])
                            # The saved connection belongs to this team.
                            rows.append(json.loads(json.dumps(message).replace("T0AAAAAAA1", "T12345678")))
                upstream = Upstream()
                with self.subTest(file=path.stem, thread=thread_["kind"]), \
                        self.runtime(f"{path.stem}-{index}", upstream) as runtime:
                    account = varied.demo_rows(400, PLAIN)
                    load(runtime.directory, account)
                    size = len(thread_["pages"][0]["messages"])
                    channel = self.serve(runtime, upstream, "acc-400", rows, page=size)
                    self.brief(runtime, upstream, account, channel, rows, pages=-(-len(rows) // size))
                    read += 1
        self.assertEqual(read, 36)

    def test_an_account_without_a_thread_can_still_be_briefed_without_the_slack_grant(self):
        upstream = Upstream()
        with self.runtime("ungranted", upstream) as runtime:
            rows = varied.demo_rows(400, PLAIN)
            load(runtime.directory, rows)
            brief = create_brief(runtime, "acc-400", as_of=AS_OF, include_slack=True, grant_slack=False)
        receipt = brief["evidence"]["receipts"][-1]
        self.assertEqual((brief["status"], receipt["tool"], receipt["http_status"], upstream.requests),
                         ("accepted", "read_slack_messages", 403, []))
        self.assertIn("  Slack: not read. The grant lacks slack:read.", varied.hold(self, brief)["NOT READ"])
        self.assertFalse(any(record["record_ref"].startswith("slack/") for record in brief["evidence"]["records"]))

    def test_size_cases_have_exact_outcomes(self):
        text = lambda letter, size=MAX_TEXT: letter * size  # noqa: E731
        numbered = lambda count: replies([f"Reply {index}." for index in range(count)])  # noqa: E731
        mixed = replies(["Contract attached.", "", "", ""])
        mixed[1].update(subtype="file_share",
                        files=[{"name": HIDDEN}, {"url_private": "https://files.example/" + HIDDEN}])
        mixed[2].update(subtype="bot_message", bot_id="B0000000001", attachments=[{"fallback": HIDDEN}],
                        blocks=[{"type": "section", "text": {"type": "mrkdwn", "text": HIDDEN}}])
        del mixed[2]["user"], mixed[3]["user"]
        mixed[3].update(blocks=[{"type": "rich_text", "elements": [{"text": HIDDEN}]}])
        bare = replies(["Parent.", ""])
        del bare[1]["text"]
        bare[1]["blocks"] = [{"type": "section", "text": {"type": "mrkdwn", "text": HIDDEN}}]
        accepted = {
            # label: (thread, how it is served, pages, upstream requests)
            "one message": (replies(["Only the parent."]), {}, 1, 1),
            "0 and 1 characters": (replies(["Parent.", "", "x"]), {}, 1, 1),
            "16 KiB message": (replies([text("a")]), {}, 1, 1),
            "64 KiB of text": (replies([text(letter) for letter in "abcd"]), {}, 1, 1),
            "100 messages": (numbered(MAX_ROWS), {}, 1, 1),
            "100 rows, one a repeated parent": (numbered(MAX_ROWS - 1), {"page": 50, "repeat_parent": True}, 2, 2),
            "10 pages": (numbered(MAX_ATTEMPTS), {"page": 1}, 10, 10),
            "2,048-byte cursor": (numbered(2), {"page": 1, "cursor_size": MAX_CURSOR}, 2, 2),
            "rate limit between pages": (numbered(4), {"page": 2, "first": {2: [SLOW]}}, 2, 3),
            "9 pages and a rate limit": (numbered(MAX_ATTEMPTS - 1), {"page": 1, "first": {4: [SLOW]}}, 9, 10),
            "files, attachments, and blocks only": (mixed, {}, 1, 1),
            "one second shared and one message listed late": (thread([
                {"author": "Ana", "sent_at": "2027-01-10T10:00:00Z", "body": "Second."},
                {"author": "Bo", "sent_at": "2027-01-10T10:00:00Z", "body": "Third, in the same second."},
                {"author": "Ana", "sent_at": "2027-01-10T11:00:00+02:00", "body": "First, an hour earlier."}]),
                {}, 1, 1),
        }
        refused = {
            # label: (thread, how it is served, reason, upstream requests)
            "40,000 characters": (replies(["Parent.", text("x", 40_000)]), {}, "slack_output_too_large", 1),
            "16 KiB message and a byte": (replies([text("a", MAX_TEXT + 1)]), {}, "slack_output_too_large", 1),
            "64 KiB of text and a byte": (replies([*(text(letter) for letter in "abcd"), "e"]), {},
                                          "slack_output_too_large", 1),
            "101 messages": (numbered(MAX_ROWS + 1), {}, "slack_resource_limit", 1),
            "101 rows, one a repeated parent": (numbered(MAX_ROWS), {"page": 50, "repeat_parent": True},
                                                "slack_resource_limit", 2),
            "11 pages": (numbered(MAX_ATTEMPTS + 1), {"page": 1}, "slack_resource_limit", 10),
            "2,049-byte cursor": (numbered(2), {"page": 1, "cursor_size": MAX_CURSOR + 1},
                                  "slack_thread_incomplete", 1),
            "rate limit with no delay given": (numbered(4), {"page": 2, "first": {2: [(429, {})]}},
                                               "slack_rate_limited", 2),
            "rate limit twice": (numbered(4), {"page": 2, "first": {2: [SLOW, SLOW]}}, "slack_rate_limited", 3),
            "10 pages and a rate limit": (numbered(MAX_ATTEMPTS), {"page": 1, "first": {4: [SLOW]}},
                                          "slack_resource_limit", 10),
            "blocks and no text": (bare, {}, "slack_response_invalid", 1),
        }
        for label, (served, options, pages, requests) in accepted.items():
            upstream = Upstream()
            with self.subTest(case=label), self.runtime("accepted " + label, upstream) as runtime:
                rows = varied.demo_rows(400, PLAIN)
                load(runtime.directory, rows)
                self.assertIsNone(refusal(served, self.cutoff))
                channel = self.serve(runtime, upstream, "acc-400", served, **options)
                brief = self.brief(runtime, upstream, rows, channel, served, pages=pages)
                counts = brief["evidence"]["thread_snapshot"]["counts"]
                retries = sum(map(len, dict(options.get("first", ())).values()))
                repeats = (pages - 1) * bool(options.get("repeat_parent"))
                self.assertEqual((len(upstream.requests), counts["rate_retries"], counts["raw_rows"],
                                  counts["duplicates"]), (requests, retries, len(served) + repeats, repeats))
        for label, (served, options, code, requests) in refused.items():
            upstream = Upstream()
            with self.subTest(case=label), self.runtime("refused " + label, upstream) as runtime:
                load(runtime.directory, varied.demo_rows(400, PLAIN))
                self.serve(runtime, upstream, "acc-400", served, **options)
                self.stopped(code, runtime, upstream, "acc-400", requests=requests)

    def test_a_rate_limit_of_five_seconds_is_waited_out_and_six_is_not(self):
        waits, sleep = [], asyncio.sleep

        async def wait(delay, *args):
            # asyncio.sleep is shared with the server's own loop, whose ticks stay real. Only the reader waits seconds.
            if delay < 1:
                return await sleep(delay, *args)
            waits.append(delay)

        for delay, pages in ((5, 2), (6, None)):
            upstream = Upstream()
            served = replies([f"Reply {index}." for index in range(4)])
            # The wait is recorded, not slept through.
            with self.subTest(delay=delay), self.runtime(f"delay {delay}", upstream) as runtime, \
                    patch("gtm_connector.slack_reader.asyncio.sleep", wait):
                rows = varied.demo_rows(400, PLAIN)
                load(runtime.directory, rows)
                channel = self.serve(runtime, upstream, "acc-400", served, page=2,
                                     first={2: [(429, {"Retry-After": str(delay)})]})
                if pages is None:
                    self.stopped("slack_rate_limited", runtime, upstream, "acc-400", requests=2)
                else:
                    self.brief(runtime, upstream, rows, channel, served, pages=pages)
        self.assertEqual(waits, [5])

    def test_responses_of_exactly_1_mib_are_accepted_and_one_byte_more_is_refused(self):
        def served(padding):
            rows = replies(["Parent.", ""])
            rows[1]["blocks"] = [{"type": "rich_text", "text": "x" * padding}]
            return rows

        # Two pages share the budget. The second carries a block that is counted, left out, and sized to fit.
        probe = Upstream()
        probe.add("C0", served(0), page=1)
        empty = sum(len(probe.page("C0", served(0)[0]["ts"], latest=self.cutoff, start=start)) for start in (0, 1))
        for label, padding in (("exact", MAX_RESPONSE - empty), ("over", MAX_RESPONSE - empty + 1)):
            upstream = Upstream()
            with self.subTest(case=label), self.runtime(label, upstream) as runtime:
                rows = varied.demo_rows(400, PLAIN)
                load(runtime.directory, rows)
                channel = self.serve(runtime, upstream, "acc-400", served(padding), page=1)
                if label == "over":
                    self.stopped("slack_resource_limit", runtime, upstream, "acc-400", requests=2)
                    continue
                brief = self.brief(runtime, upstream, rows, channel, served(padding), pages=2)
                self.assertEqual(brief["evidence"]["thread_snapshot"]["counts"]["response_bytes"], MAX_RESPONSE)

    def test_a_normalized_result_of_exactly_256_kib_is_accepted_and_one_byte_more_is_refused(self):
        def served(padding):
            # A hundred edited messages from long user ids. A four-byte character is twelve bytes once escaped.
            rows = replies([chr(0x1F4CE) * 149 for _ in range(MAX_ROWS)])
            for row in rows:
                row.update(user="U" + "A" * 63, edited={"user": "U" + "B" * 63, "ts": stamp(micros(row["ts"]) + 1)})
            rows[-1]["text"] += "x" * padding
            return rows

        def result(padding):
            upstream = Upstream()
            with self.runtime(f"padding {padding}", upstream) as runtime:
                rows = varied.demo_rows(400, PLAIN)
                load(runtime.directory, rows)
                channel = self.serve(runtime, upstream, "acc-400", served(padding))
                evidence = self.brief(runtime, upstream, rows, channel, served(padding), pages=1)["evidence"]
            # The result of the read: its records and its snapshot, as ASCII JSON.
            records = [row for row in evidence["records"] if row["record_ref"].startswith("slack/")]
            return len(json.dumps({"source": "slack", "untrusted_data": True, "records": records,
                                   "thread_snapshot": evidence["thread_snapshot"]},
                                  sort_keys=True, separators=(",", ":"), ensure_ascii=True))

        # Each padding byte is one byte of the result, so a measured result gives the padding for the bound.
        padding = MAX_RESULT - result(0)
        self.assertGreater(padding, 0)
        self.assertLessEqual(sum(len(row["text"].encode()) for row in served(padding + 1)), MAX_ALL_TEXT)
        self.assertEqual(result(padding), MAX_RESULT)
        upstream = Upstream()
        with self.runtime("over", upstream) as runtime:
            load(runtime.directory, varied.demo_rows(400, PLAIN))
            self.serve(runtime, upstream, "acc-400", served(padding + 1))
            self.stopped("slack_output_too_large", runtime, upstream, "acc-400", requests=1)


if __name__ == "__main__":
    unittest.main()
