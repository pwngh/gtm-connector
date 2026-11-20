"""Every card the demo book, tests/books, the varied accounts, the Slack thread files, and the ten cases build,
made through create_brief and the real gateway with fixed clocks and IDs."""

from contextlib import ExitStack, closing, contextmanager
from datetime import timedelta
from decimal import Decimal
import hashlib
import importlib
import json
import math
from pathlib import Path
import random
import sqlite3
import tempfile
from typing import Any, Iterator
import unittest
from unittest import mock

import httpx2

from gtm_connector import briefing, slack_auth, slack_reader, strict_json
from gtm_connector.audit import read_ids
from gtm_connector.call_workflow import resolve_call_account
from gtm_connector.calls_import import CallsImportError, parse_transcript, select_excerpt, store_import
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadOnlyAdapter
from gtm_connector.seed import ACCOUNT_COUNT, SHOWCASE
from gtm_connector.slack_mapping import save_slack_mapping
from gtm_connector.slack_workflow import resolve_slack_account
from oracle import freeze
from oracle.output import Writer


CREATE_BRIEF = briefing.create_brief
BOOKS = Path(__file__).resolve().parents[2] / "tests" / "books"


def all_books() -> dict[str, list[dict]]:
    """Every book in tests/books, by name, read here and not through tests/varied.py, whose BOOKS a test run
    may narrow with GTM_BOOKS. The oracle always reads them all."""
    found = {path.stem: strict_json.loads(path.read_bytes())["accounts"] for path in sorted(BOOKS.glob("*.json"))}
    if not found:
        raise AssertionError(f"no books in {BOOKS}")
    return found


def _test_module(name: str) -> Any:
    """A test module whose data and helpers the cards are built from. They are read as the tests read them."""
    return importlib.import_module(name)


def plain(value: Any) -> Any:
    """Source rows as JSON values: tuples become lists and a Decimal amount its exact text, marked."""
    if isinstance(value, Decimal):
        return {"decimal": str(value)}
    if isinstance(value, dict):
        return {key: plain(item) for key, item in value.items()}
    if isinstance(value, (list, tuple)):
        return [plain(item) for item in value]
    return value


class Cards:
    """Collects the cards of each family. A card is the sealed file's text, its digest, and its seal receipt."""

    def __init__(self) -> None:
        self.families: dict[str, list[dict]] = {}

    def brief(self, family: str, key: str, runtime: Any, account_id: str, source: Any, **options: Any) -> dict:
        """Make one card in the current scope and keep it, or keep the refusal and the receipts it left."""
        before = len(runtime.connector.audit.read())
        row: dict[str, Any] = {"key": key, "account_id": account_id, "options": options, "input": plain(source)}
        try:
            artifact = CREATE_BRIEF(runtime, account_id, **options)
        except Exception as error:  # A documented refusal is a fixture too.
            row["refused"] = {"error": type(error).__name__, "code": getattr(error, "code", None),
                              "message": str(error)}
            row["receipts"] = runtime.connector.audit.read()[before:]
        else:
            payload = (runtime.directory / "briefs" / f"{artifact['brief_id']}.json").read_bytes()
            [seal] = read_ids(runtime.directory / "audit.sqlite3", [artifact["sealed_by"]])
            digest = hashlib.sha256(payload).hexdigest()
            if seal["rows"] != [f"briefs/{artifact['brief_id']}/{digest}"]:
                raise AssertionError(f"{family}/{key}: the seal does not name the saved bytes")
            row.update(sha256=digest, card=payload.decode(), seal=seal)
        self.families.setdefault(family, []).append(row)
        return row

    def write(self, writer: Writer) -> None:
        for family, rows in sorted(self.families.items()):
            writer.lines(f"cards/{family}.jsonl.gz", rows)


@contextmanager
def _folder() -> Iterator[Path]:
    with tempfile.TemporaryDirectory(prefix="oracle-") as folder:
        yield Path(folder)


def _dump_stores(directory: Path) -> dict:
    """Every row of the demo stores, by database and table, with the column names."""
    stores: dict = {}
    for database in ("crm", "calls", "slack"):
        with closing(sqlite3.connect((directory / f"{database}.sqlite3").as_uri() + "?mode=ro", uri=True)) as db:
            tables = [row[0] for row in db.execute("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")]
            stores[database] = {}
            for table in tables:
                cursor = db.execute(f"SELECT * FROM {table} ORDER BY rowid")
                stores[database][table] = {"columns": [column[0] for column in cursor.description],
                                           "rows": [list(row) for row in cursor]}
    return stores


def demo(cards: Cards, writer: Writer) -> None:
    """The demo book: every account, then the showcase account with each source left out once."""
    with _folder() as folder, Runtime(folder / "state") as runtime:
        for number in range(1, ACCOUNT_COUNT + 1):
            account_id = f"acc-{number:03d}"
            with freeze.scope(f"demo/{account_id}"):
                cards.brief("demo", account_id, runtime, account_id, None, include_slack=True)
        showcase = f"acc-{SHOWCASE:03d}"
        for name, options in (("without calls", {"include_calls": False, "include_slack": True}),
                              ("slack not requested", {"include_slack": False}),
                              ("slack not granted", {"include_slack": True, "grant_slack": False})):
            with freeze.scope(f"demo/{showcase}/{name}"):
                cards.brief("demo", f"{showcase} {name}", runtime, showcase, None, **options)
        writer.json("cards/demo_book.json", {
            "note": "the demo stores as the demo seeds them, every table in rowid order",
            **_dump_stores(runtime.directory)}, 3)


def books_demo(cards: Cards) -> None:
    """tests/books, the six unusual accounts, and the forty generated ones, in the demo stores, as
    tests/test_varied_data.py loads them."""
    varied = _test_module("tests.varied")
    shapes = _test_module("tests.test_varied_data")
    with _folder() as folder, Runtime(folder / "state") as runtime:
        accounts: list[tuple[str, int, dict, dict]] = []
        number = 300
        for name, book in all_books().items():
            for index, shape in enumerate(book):
                accounts.append(("books", number, {"book": name, "index": index, "account": shape}, shape))
                number += 1
        for number, shape in shapes.SHAPES.items():
            accounts.append(("varied", number, {"shape": number, "account": shape}, shape))
        generator = random.Random(2027)
        for number in range(200, 240):
            shape = shapes.generated(generator)
            accounts.append(("varied", number, {"generated": number, "seed": 2027, "account": shape}, shape))
        for family, number, source, shape in accounts:
            rows = varied.demo_rows(number, shape)
            varied.load(runtime.directory, rows)
            with freeze.scope(f"{family}/demo/acc-{number}"):
                cards.brief(f"{family}_demo", f"acc-{number}", runtime, f"acc-{number}", {**source, "rows": rows},
                            include_slack=True)


def books_salesforce(cards: Cards) -> None:
    """tests/books served by the invented Salesforce org of tests/test_books_salesforce.py."""
    org_module = _test_module("tests.test_books_salesforce")
    varied = _test_module("tests.varied")
    org = org_module.Org()
    reader = SalesforceReadOnlyAdapter(org_module.INSTANCE, org_module.TOKEN, environment="production",
                                       transport=httpx2.MockTransport(org.respond))
    with _folder() as folder, Runtime(folder / "state", crm_reader=reader) as runtime:
        number = 0
        for name, book in all_books().items():
            for index, account in enumerate(book):
                rows = org_module.org_rows((name, index), account)
                account_id = org.add(rows)
                number += 1
                notes = [(f"note-{number}-{position:03d}", account_id, row["called_at"], None, row["body"])
                         for position, row in enumerate(account["call_notes"])]
                messages = [(f"msg-{number}-{position:03d}", account_id, row["channel"], row["author"],
                             row["sent_at"], row["body"], 0) for position, row in enumerate(account["slack_messages"])]
                for database, table, loaded in (("calls", "call_notes", notes), ("slack", "messages", messages)):
                    with closing(sqlite3.connect(runtime.directory / f"{database}.sqlite3")) as connection, connection:
                        for row in loaded:
                            connection.execute(f"INSERT INTO {table} VALUES ({', '.join('?' * len(row))})", row)
                with freeze.scope(f"books/salesforce/{name}/{index}"):
                    cards.brief("books_salesforce", f"{name}/{index}", runtime, account_id,
                                {"book": name, "index": index, "org_rows": rows, "call_notes": notes,
                                 "messages": messages}, as_of=varied.AS_OF.isoformat(), include_slack=True)
        if org.unexpected:
            raise AssertionError(f"the invented org was asked what it does not serve: {org.unexpected[:3]}")


def books_calls(cards: Cards) -> None:
    """tests/books transcripts through the import path of tests/test_books_calls.py: parse, select, resolve,
    store, then the card. An export that names every speaker is imported again with that stated."""
    calls = _test_module("tests.test_books_calls")
    varied = _test_module("tests.varied")
    with _folder() as folder:
        for name, book in all_books().items():
            shapes = dict(enumerate(book, 300))
            carriers = [number for number, shape in shapes.items() if shape.get("transcript")]
            with Runtime(folder / name, calls_backend="imported") as runtime:
                for number, shape in shapes.items():
                    account_id = f"acc-{number}"
                    rows = varied.demo_rows(number, shape)
                    calls.load_crm(runtime.directory, rows)
                    source: dict[str, Any] = {"book": name, "index": number - 300, "rows": {"crm": rows["crm"]}}
                    if number not in carriers:
                        with freeze.scope(f"books/calls/{name}/{number}"):
                            cards.brief("books_calls", f"{name}/{number - 300}", runtime, account_id, source,
                                        as_of=calls.AS_OF)
                        continue
                    kind, raw = shape["transcript"]["format"], shape["transcript"]["text"].encode()
                    called_at = calls.CALLED[carriers.index(number) % len(calls.CALLED)]
                    declared = [False]
                    if calls.deserved(raw, kind) is None and all(
                            varied.named(unit["text"]) for unit in calls.units(raw.decode(), kind)
                            if unit["text"].strip()):
                        declared.append(True)
                    for speaker_names in declared:
                        key = f"{name}/{number - 300}" + (" speaker names" if speaker_names else "")
                        with freeze.scope(f"books/calls/{key}"):
                            imported = _import(runtime, account_id, raw, kind, calls.IMPORTED_AT, called_at,
                                               speaker_names)
                            transcript = {"format": kind, "text": shape["transcript"]["text"],
                                          "called_at": called_at, "imported_at": calls.IMPORTED_AT,
                                          "speaker_names": speaker_names, "import": imported}
                            if "refused" in imported:
                                cards.families.setdefault("books_calls", []).append(
                                    {"key": key, "account_id": account_id, "input": plain({**source,
                                                                                          "transcript": transcript}),
                                     "import_refused": imported["refused"]})
                                continue
                            cards.brief("books_calls", key, runtime, account_id, {**source, "transcript": transcript},
                                        as_of=calls.AS_OF)


def _import(runtime: Any, account_id: str, raw: bytes, kind: str, imported_at: str, called_at: str | None,
            speaker_names: bool) -> dict:
    try:
        parsed = parse_transcript(raw, format=kind, speaker_names=speaker_names)
        excerpt = select_excerpt(parsed)
        association = resolve_call_account(runtime, account_id=account_id)
        return store_import(runtime.imports_path, parsed, excerpt, association=association, called_at=called_at,
                            imported_at=imported_at)
    except CallsImportError as error:
        return {"refused": {"code": error.code, "exit_code": error.exit_code}}


class _SlackFolder:
    """A live-Slack state folder, as tests/test_books_slack.py makes one, with the reader's clock fixed."""

    def __init__(self, stack: ExitStack, folder: Path, name: str, upstream: Any) -> None:
        books = _test_module("tests.test_books_slack")
        auth_tests = _test_module("test_slack_auth")
        self.books = books
        connection = folder / f"{name}-connection"
        auth_tests.seed(connection, auth_tests.saved_document(freeze.SLACK_EPOCH, rotating=False))
        self.clock = auth_tests.Clock()
        self.clock.wall = freeze.SLACK_EPOCH
        self.cutoff = books.stamp((self.clock.wall - books.EPOCH) // timedelta(microseconds=1))
        transport = httpx2.MockTransport(upstream)

        def reader(mapping_path: Any, **options: Any) -> Any:
            return books.READER(mapping_path, **options, _clock=self.clock,
                                _provider=slack_reader.SlackRepliesProvider(_transport=transport),
                                _auth_provider=slack_auth.SlackProvider(_transport=transport, _clock=self.clock))

        stack.enter_context(mock.patch("gtm_connector.slack_reader.SlackThreadReader", reader))
        self.runtime = stack.enter_context(Runtime(folder / name, slack_backend="slack",
                                                   slack_connection_dir=connection))
        self.upstream = upstream

    def serve(self, account_id: str, rows: list, **options: Any) -> str:
        """Put the thread upstream and map it to the account, as `slack map` does."""
        channel = "C0000000" + account_id[-3:]
        self.upstream.add(channel, rows, **options)
        save_slack_mapping(
            self.runtime.slack_mapping_path,
            association=resolve_slack_account(self.runtime, account_id=account_id),
            slack_binding=self.runtime.slack_binding, selection={"channel_id": channel, "thread_ts": rows[0]["ts"]},
            selected_at=self.clock.now().isoformat().replace("+00:00", "Z"))
        return channel


def books_slack(cards: Cards) -> None:
    """tests/books threads served by the invented Slack of tests/test_books_slack.py and read live."""
    books = _test_module("tests.test_books_slack")
    varied = _test_module("tests.varied")
    with _folder() as folder:
        for name, book in all_books().items():
            shapes = dict(enumerate(book, 300))
            with ExitStack() as stack:
                place = _SlackFolder(stack, folder, name, books.Upstream())
                rows = {number: varied.demo_rows(number, shape) for number, shape in shapes.items()}
                books.load(place.runtime.directory, *rows.values())
                threads = {number: books.thread(shape["slack_messages"])
                           for number, shape in shapes.items() if shape["slack_messages"]}
                sizes = {number: math.ceil(len(served) / 3) for number, served in threads.items()}
                with freeze.scope(f"books/slack/{name}/mapping"):
                    for number, served in threads.items():
                        place.serve(f"acc-{number}", served, page=sizes[number])
                for number in shapes:
                    source = {"book": name, "index": number - 300,
                              "rows": {"crm": rows[number]["crm"], "calls": rows[number]["calls"]},
                              "thread": threads.get(number), "page": sizes.get(number), "cutoff": place.cutoff}
                    with freeze.scope(f"books/slack/{name}/{number}"):
                        cards.brief("books_slack", f"{name}/{number - 300}", place.runtime, f"acc-{number}", source,
                                    as_of=books.AS_OF, include_slack=True)


def slack_threads(cards: Cards) -> None:
    """tests/slack_threads: conversations.replies pages as the Slack API returns them, each read whole."""
    books = _test_module("tests.test_books_slack")
    varied = _test_module("tests.varied")
    folder_path = Path(books.__file__).parent / "slack_threads"
    with _folder() as folder:
        for path in sorted(folder_path.glob("*.json")):
            for index, thread in enumerate(json.loads(path.read_text())["threads"]):
                rows, seen = [], set()
                for page in thread["pages"]:
                    for message in page["messages"]:
                        if message["ts"] not in seen:
                            seen.add(message["ts"])
                            rows.append(json.loads(json.dumps(message).replace("T0AAAAAAA1", "T12345678")))
                size = len(thread["pages"][0]["messages"])
                key = f"{path.stem}/{index}"
                with ExitStack() as stack:
                    place = _SlackFolder(stack, folder, f"{path.stem}-{index}", books.Upstream())
                    account = varied.demo_rows(400, books.PLAIN)
                    books.load(place.runtime.directory, account)
                    with freeze.scope(f"slack_threads/{key}"):
                        place.serve("acc-400", rows, page=size)
                        cards.brief("slack_threads", key, place.runtime, "acc-400",
                                    {"file": path.name, "thread": index, "kind": thread["kind"], "rows": rows,
                                     "page": size, "cutoff": place.cutoff, "crm": account["crm"]},
                                    as_of=books.AS_OF, include_slack=True)


def cases(cards: Cards) -> None:
    """The ten deals of tests/test_cases.py, run as the tests run them, through the CLI. Each card is kept with
    the name of the test that made it. A test's card() seeds the demo stores with a first card of acc-001, inserts
    its deal's rows, writes sellers.json, and makes the target card; each card's input says which it was."""
    module = _test_module("tests.test_cases")
    current: dict[str, Any] = {"test": "", "count": 0, "deal": None}
    load = module.card

    def card(test: unittest.TestCase, rows: tuple, *, sellers: tuple = ("Morgan Reed",)) -> dict:
        account_id, tables = rows
        current["deal"] = {"account_id": account_id, "rows": plain(tables),
                           "sellers": {"schema_version": 1, "sellers": list(sellers)}}
        try:
            return load(test, rows, sellers=sellers)
        finally:
            current["deal"] = None

    def capture(runtime: Any, account_id: str, **options: Any) -> dict:
        current["count"] += 1
        key = f"{current['test']}/{current['count']}"
        deal = current["deal"]
        if deal is not None and account_id == deal["account_id"]:
            source = {"target": True, "stores": "cards/demo_book.json, seeded by the card before this one",
                      "inserted": deal["rows"], "sellers.json": deal["sellers"]}
        elif deal is not None:
            source = {"target": False, "stores": "cards/demo_book.json"}
        else:
            # A test that makes its card without card(): the demo command's own card.
            source = {"target": True, "stores": "cards/demo_book.json"}
        with freeze.scope(f"cases/{key}"):
            row = cards.brief("cases", key, runtime, account_id, source, **options)
        if "refused" in row:
            raise AssertionError(f"case {key} was refused: {row['refused']}")
        return json.loads(row["card"])

    class Result(unittest.TestResult):
        def startTest(self, test: unittest.TestCase) -> None:
            current.update(test=test.id().rsplit(".", 1)[-1], count=0)
            super().startTest(test)

    suite = unittest.defaultTestLoader.loadTestsFromModule(module)
    result = Result()
    with mock.patch.object(briefing, "create_brief", capture), mock.patch.object(module, "card", card):
        for test in _tests(suite):
            with freeze.scope(f"cases/{test.id()}"):
                test.run(result)
    if not result.wasSuccessful() or result.testsRun != 10:
        raise AssertionError(f"tests/test_cases.py did not pass under the oracle: {result.errors + result.failures}")
    targets = [row for row in cards.families["cases"] if row["input"]["target"]]
    if len(targets) != 10 or len({row["key"].split("/")[0] for row in targets}) != 10:
        raise AssertionError(f"each case should make one target card: {[row['key'] for row in targets]}")


def _tests(suite: unittest.TestSuite) -> Iterator[unittest.TestCase]:
    for test in suite:
        if isinstance(test, unittest.TestSuite):
            yield from _tests(test)
        else:
            yield test


def generate(writer: Writer) -> None:
    cards = Cards()
    demo(cards, writer)
    books_demo(cards)
    books_salesforce(cards)
    books_calls(cards)
    books_slack(cards)
    slack_threads(cards)
    cases(cards)
    cards.write(writer)
    writer.json("cards/index.json", {
        "note": ("each line of a family file is one card: key, account, options, the source rows it read, and the "
                 "sealed file's exact text with its SHA-256 and seal receipt; or a refusal with the receipts it left. "
                 "In cases, input.target marks the card a test checks; a target made by card() lists the rows "
                 "inserted into the seeded demo stores, table by table in order, and the sellers.json it wrote"),
        "families": {family: len(rows) for family, rows in sorted(cards.families.items())}},
        len(cards.families))
