"""Any upstream answer gives a documented result or a fixed refusal, for Salesforce and Slack reads.

Each example starts from valid invented responses and changes some of them: statuses, headers, body
bytes, field types, identifiers, pagination, and timing. Only mock transports are used.

A deep run: GTM_FUZZ_EXAMPLES=10000 python -m unittest discover -s tests_stateful -p "test_fuzz_providers.py"
"""

import asyncio
from contextlib import contextmanager, suppress
from copy import deepcopy
from datetime import date
from decimal import Decimal
import gzip
import json
import os
from pathlib import Path
import re
import tempfile
import traceback
import unittest
from unittest.mock import patch
from urllib.parse import parse_qsl

from hypothesis import assume, given, HealthCheck, settings, strategies as st
import httpx2

from gtm_connector import salesforce, slack_auth, slack_mapping, slack_reader
from gtm_connector.salesforce_check import run_check
from gtm_connector.sf_credentials import SFCredentialsError
from tests.test_slack_auth import (ACCESS, APP, CLIENT, Clock, grant, identity, IDENTITY, NEW_ACCESS, NEW_REFRESH,
                                   REDIRECT, REFRESH, replacement, saved_document, seed, TEAM, USER)


ROOT = Path(__file__).resolve().parent.parent
EXAMPLES = int(os.environ.get("GTM_FUZZ_EXAMPLES", "0"))
MIB = 1024 * 1024
MARK = "upstream-private-zq7"  # Planted in upstream text and headers. A refusal must never carry it.
OMITTED = "omitted-rich-content-zq7"  # Planted in Slack files, attachments, and blocks. No result may carry it.


def fuzz(quick, share=1.0):
    """Repeatable and quick as checked in. GTM_FUZZ_EXAMPLES raises the count for a deep run."""
    return settings(max_examples=max(quick, int(EXAMPLES * share)), derandomize=True, database=None, deadline=None,
                    suppress_health_check=[HealthCheck.too_slow, HealthCheck.data_too_large])


def documented(page, prefix):
    """The reason codes listed under Errors in one docs page."""
    errors = (ROOT / "docs" / "python" / page).read_text().split("\n## Errors\n", 1)[1].split("\n## ", 1)[0]
    return frozenset(re.findall(rf"`({prefix}_[a-z_]+)`", errors))


SALESFORCE_CODES = documented("salesforce.md", "salesforce")
SLACK_CODES = documented("slack.md", "slack")

# Salesforce fixtures.
ORIGIN, OTHER_ORIGIN = "https://invented-fuzz.my.salesforce.com", "https://invented-other.my.salesforce.com"
TOKEN, FRESH = "invented-first-token-zq7", "invented-fresh-token-zq7"
QUERY = "/services/data/v68.0/query"
LOCATOR = re.compile(re.escape(QUERY) + r"/[A-Za-z0-9_-]{1,128}")
FIELDS = salesforce.REQUIRED_FIELDS
PREFIX = {"Account": "001", "Contact": "003", "Opportunity": "006", "Contract": "800"}
ACCOUNTS = tuple(f"001{number:015d}" for number in range(3))
OWNERS = (ACCOUNTS[0], None, ACCOUNTS[1])
ORGANIZATIONS = {
    "developer": {"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition", "IsSandbox": False},
    "sandbox": {"Id": "00D000000000001AAA", "OrganizationType": "Enterprise Edition", "IsSandbox": True},
    "production": {"Id": "00D000000000001AAA", "OrganizationType": "Enterprise Edition", "IsSandbox": False},
}
HOSTILE_LOCATORS = (
    "https://untrusted.example/query/page", ORIGIN + QUERY + "/page", "//untrusted.example/query/page",
    "/services/data/v67.0/query/page", QUERY + "/../sobjects", QUERY + "/%2e%2e", QUERY + "/page?token=" + TOKEN,
    QUERY + "/page#fragment", QUERY + "\\page", QUERY + "/", QUERY + "/" + "p" * 129, QUERY + "/pagé", QUERY, "",
    None, 7, True, [QUERY + "/page"], {"url": QUERY + "/page"},
)

# Slack fixtures.
PARENT, CHANNEL, ACCOUNT = "1700000000.000001", "C12345678", "acc-001"
CUTOFF = f"{int(Clock().wall.timestamp())}.000000"
CRM = {"schema_version": 1, "crm_backend": "sqlite", "identity_sha256": "a" * 64}
SLACK = slack_mapping.slack_identity_binding(IDENTITY)
REPLIES, TOKENS, IDENTIFY = "/api/conversations.replies", "/api/oauth.v2.access", "/api/auth.test"
SUBTYPES = (None, "bot_message", "me_message", "file_share", "thread_broadcast")
SLACK_SECRETS = (ACCESS, NEW_ACCESS, REFRESH, NEW_REFRESH)
SLACK_ERRORS = (
    "token_expired", "channel_not_found", "thread_not_found", "not_in_channel", "missing_scope", "access_denied",
    "no_permission", "invalid_auth", "token_revoked", "account_inactive", "not_authed", "ratelimited", "rate_limited",
    "invalid_refresh_token", "refresh_token_expired", "invalid_grant", "not_allowed_token_type", "not_allowed_token",
    "internal_error", "fatal_error", "service_unavailable", "request_timeout", "invalid_arguments", "invalid_cursor",
    "invalid_ts_latest", "org_login_required", "ekm_access_denied", "team_access_not_granted", "is_archived",
    "TOKEN_EXPIRED", "token_expired ", "", MARK, None, 7, True, ["token_expired"],
)
# Codes about this computer's own files and flags. No upstream answer may produce one.
LOCAL_CODES = frozenset({
    "slack_selection_invalid", "slack_mapping_missing", "slack_mapping_conflict", "slack_mapping_invalid",
    "slack_snapshot_invalid", "slack_clock_invalid", "slack_storage_failed", "slack_invalid_configuration",
    "slack_connection_busy", "slack_private_state_invalid", "slack_credentials_invalid", "slack_not_connected",
})

# Changes shared by both providers.
RAW = "\x01raw:"
_RAW = re.compile(r'"\\u0001raw:([^"\\]*)"')
STATUSES = (401, 401, 403, 403, 429, 429, 100, 101, 201, 202, 204, 206, 300, 301, 302, 303, 304, 307, 308, 400, 404,
            405, 408, 409, 410, 418, 500, 501, 502, 503, 504, 599)
FAILURES = (httpx2.ConnectError, httpx2.ReadError, httpx2.WriteError, httpx2.CloseError, httpx2.ConnectTimeout,
            httpx2.ReadTimeout, httpx2.WriteTimeout, httpx2.PoolTimeout, httpx2.RemoteProtocolError,
            httpx2.LocalProtocolError, httpx2.ProxyError, httpx2.UnsupportedProtocol, httpx2.DecodingError,
            httpx2.TooManyRedirects)
CODINGS = ("gzip", "x-gzip", "deflate", "br", "zstd", "compress")
NOT_JSON = (b"text/html", b"text/html; charset=utf-8", b"text/plain", b"application/xml", b"application/octet-stream",
            b"application/x-www-form-urlencoded", b"image/png")
HEADERS = tuple((name, value) for name, choices in {
    b"content-encoding": (b"gzip", b"deflate", b"br", b"zstd", b"identity", b"IDENTITY", b"gzip, identity"),
    b"content-type": (None, b"application/json", b"application/json; charset=latin-1", MARK.encode(), *NOT_JSON),
    b"location": (b"https://untrusted.example/" + MARK.encode(), QUERY.encode() + b"/01gMOVED-1"),
    b"retry-after": (b"0", b"1", b"5", b"0005", b"6", b"3600", b"3601", b"99999", b"-1", b"1.5", b" 2", b"+2",
                     "\u0663".encode(), b"0x1", b"1e0", b"", b"Wed, 21 Oct 2026 07:28:00 GMT", MARK.encode()),
    b"content-length": (b"0", b"7", b"1048576", b"1048577", b"99999999999", b"-1", b"1e3", b"", MARK.encode()),
    b"set-cookie": (b"sid=" + MARK.encode() + b"; Path=/",),
    b"www-authenticate": (b"Bearer realm=" + MARK.encode(),),
}.items() for value in choices)
# Values with no regard for where they land: other types, and near misses of identifiers, dates, and timestamps.
ODD = (
    [], {}, [None], [{}], ["bad"], [[[]]], [{"text": OMITTED}] * 3, {"ts": PARENT}, {"id": "T87654321"},
    "001000000000001AAA", "001000000000001AA\n", "00D000000000001AAA", ACCOUNTS[1], "U12345678", "U" + "1" * 64,
    "COTHER", "2027-06-30", "2027-W01-1", "\uff12\uff10\uff12\uff17-01-01", "12345.67", "$1,000.00", PARENT,
    "1700000000.000002", "1700000000.000002\n", "1.7e9", "-1.000000", CUTOFF, "253402300800.000000",
    "\u0661\u0667\u0660\u0660\u0660\u0660\u0660\u0660\u0660\u0660.\u0660\u0660\u0660\u0660\u0660\u0662",
    "bot_message", "message_changed", "Developer Edition", "Draft", "channels:history", "token_expired",
)
TEXTS = (MARK, "", " ", MARK + " \x00\x1b[31m\r\n\x7f", "\u202e\ufeff" + MARK + "\U0001f600", "\u00e9" * 40 + MARK)
# Number spellings are written to the body unquoted, so any token can reach the parser.
NUMBERS = tuple(RAW + token for token in (
    "1e5", "1E-30", "0.10", "-12.50", "1e999999999", "1e-999999999", "1e99999999999999999999999999",
    "0.1000000000000000000000000000001", "123456789012345678901234567890", "9" * 5000, "-0", "-0.0", "1.0", "01",
    "+1", ".5", "1.", "0x10", "1_000", "1,5", "NaN", "Infinity", "-Infinity", "1e", "true", "2147483648"))
WRONG_TEXTS = (None, "\ud800" + MARK, MARK * 1000, 7, [MARK], {"text": MARK})
DAYS = (None, "2027-06-30", "1999-12-31", "0001-01-01", "9999-12-31", "2027-02-30", "20270101", "2027-1-5",
        " 2027-01-01", "2027-01-01T00:00:00Z", "01/02/2027", 20270101)
FLAGS = (True, False, None, 0, 1, "true")
plain = st.sampled_from(TEXTS) | st.text(max_size=24)
dates = st.dates().map(date.isoformat)
amounts = st.sampled_from(("12345.67", "0.10", "1e3", "-5", "1E-28", "0", "9" * 28, "1.5E+10")).map(RAW.__add__)
values = st.one_of(
    st.none(), st.booleans(), st.integers(-2, 200), st.integers(-2 ** 70, 2 ** 70),
    st.floats(allow_nan=False, allow_infinity=False), plain, st.sampled_from(NUMBERS), st.sampled_from(ODD),
    st.sampled_from(("\ud800" + MARK, MARK * 1000, "x" * 16384, "x" * 16385, "\u00e9" * 8193, "\x00" * 70000,
                     "x" * 300000)),
)
bodies = st.one_of(
    st.sampled_from(("utf-8", "utf-8-sig", "utf-16", "utf-16-le", "utf-32", "latin-1")).map(lambda v: ("encoding", v)),
    st.integers(0, 99).map(lambda v: ("cut", v)), st.binary(min_size=1, max_size=12).map(lambda v: ("append", v)),
    st.sampled_from((300_000, 600_000, MIB)).map(lambda v: ("pad", v)),
    st.sampled_from((64, 5_000, 200_000)).map(lambda v: ("nest", v)), st.just(("duplicate", None)),
    st.booleans().map(lambda v: ("gzip", v)), st.binary(max_size=40).map(lambda v: ("bytes", v)),
    st.integers(1, 64).map(lambda v: ("chunks", v)), st.integers(0, 99).map(lambda v: ("break", v)),
)


def changes(top, fields, *special):
    """Edits to one JSON answer: a key or row field gets a value that suits it, or any value at all."""
    row, pick = st.integers(0, 5), st.integers(0, 60)
    fit = st.tuples(st.just("fit"), st.none() | row | row, pick, pick, st.booleans())
    return fit | st.one_of(
        st.tuples(st.just("top"), st.sampled_from(sorted(top)), values),
        st.tuples(st.just("drop"), st.sampled_from(sorted(top))),
        st.tuples(st.just("row"), row, pick | st.sampled_from(sorted(fields)), values),
        st.tuples(st.just("unrow"), row, pick | st.sampled_from(sorted(fields))),
        st.tuples(st.just("swap"), row, values), st.tuples(st.sampled_from(("copy", "clash")), row, row),
        st.tuples(st.just("cut"), row), st.tuples(st.just("more"), st.sampled_from((1, 9, 99, 101, 1001))),
        st.just(("reverse",)), st.tuples(st.just("whole"), values), *special,
    )


def one_at_a_time(document, top, fields, absent):
    """Each listed value at each key of an answer and of its rows, and each key or row left out or repeated."""
    rows = next((document[key] for key in ("records", "fields", "messages") if key in document), [])
    yield from (("top", name, value) for name, choices in top.items() for value in choices)
    yield from (("drop", name) for name in document)
    for index, row in enumerate(rows):
        yield from (("row", index, name, value) for name, choices in fields.items() if absent or name in row
                    for value in choices)
        yield from (("unrow", index, name) for name in row)
        yield from (("cut", index), ("copy", index, 0), ("clash", index, 0), ("swap", index, None))


def mutations(edits, *targeted):
    """One request's change: edits to the document, or a status, header, body form, failure, or delay."""
    one = st.one_of(
        st.lists(edits, min_size=1, max_size=2).map(lambda v: {"edits": v}),
        st.lists(edits, min_size=1, max_size=2).map(lambda v: {"edits": v}),
        st.lists(edits, min_size=1, max_size=2).map(lambda v: {"edits": v}),
        st.sampled_from(STATUSES).map(lambda v: {"status": v}), bodies.map(lambda v: {"body": v}),
        st.lists(st.sampled_from(HEADERS), min_size=1, max_size=2).map(lambda v: {"headers": v}),
        st.sampled_from(FAILURES).map(lambda v: {"fail": v}),
        st.sampled_from((1, 4, 19, 21)).map(lambda v: {"seconds": v}),
    )
    several = st.lists(one, min_size=2, max_size=3).map(lambda parts: {k: v for part in parts for k, v in part.items()})
    return st.one_of(one, one, one, several, *targeted)


def specs(draw, mutation, mode):
    """Per-request changes for a plan: none, one at an early request, or any mix.

    Most requests of a mix get a valid answer, so that a read reaches its later requests.
    """
    if mode == "valid":
        return []
    if mode == "one":
        return [{}] * draw(st.integers(0, 4)) + [draw(mutation)]
    return draw(st.lists(st.one_of(st.just({}), st.just({}), mutation), max_size=12))


def edited(document, edits, top, fields):
    """Apply drawn edits to a valid answer. An edit with nothing to act on does nothing."""
    document = deepcopy(document)
    for kind, *rest in edits:
        if kind == "whole":
            document = rest[0]
        if kind == "whole" or not isinstance(document, dict):
            continue
        rows = next((document[key] for key in ("records", "fields", "messages")
                     if isinstance(document.get(key), list)), [])
        row = rows[rest[0] % len(rows)] if rows and kind in ("row", "unrow", "fit") and rest[0] is not None else None
        name = rest[1] if kind in ("row", "unrow") else None
        if isinstance(name, int) and isinstance(row, dict) and row:
            name = sorted(row)[name % len(row)]
        if kind == "fit":
            # A value that suits the key it lands on: a present key, or any key the answer could carry.
            target, table = (document, top) if rest[0] is None else (row, fields)
            names = [key for key in sorted(table) if rest[3] or key in target] if isinstance(target, dict) else []
            if names:
                choices = table[names[rest[1] % len(names)]]
                target[names[rest[1] % len(names)]] = choices[rest[2] % len(choices)]
        elif kind == "top":
            document[rest[0]] = rest[1]
        elif kind == "drop":
            document.pop(rest[0], None)
        elif kind == "row" and isinstance(row, dict) and isinstance(name, str):
            row[name] = rest[2]
        elif kind == "unrow" and isinstance(row, dict):
            row.pop(name, None)
        elif kind == "swap" and rows:
            rows[rest[0] % len(rows)] = rest[1]
        elif kind in ("copy", "clash") and rows:
            # A second row with the same identifier: identical, or with other text.
            twin = deepcopy(rows[rest[0] % len(rows)])
            if kind == "clash" and isinstance(twin, dict):
                twin.update(text=MARK + " changed", Name=MARK + " changed")
            rows.insert(rest[1] % (len(rows) + 1), twin)
        elif kind == "cut" and rows:
            del rows[rest[0] % len(rows)]
        elif kind == "more" and rows and isinstance(rows[-1], dict):
            # New rows with their own identifiers, and an honest total, so only the count is out of bounds.
            for number in range(rest[0]):
                rows.append({**deepcopy(rows[-1]), **({"Id": f"00Q{number:015d}"} if "Id" in rows[-1] else {}),
                             **({"ts": f"1700000001.{number:06d}", "thread_ts": PARENT} if "ts" in rows[-1] else {})})
            if type(document.get("totalSize")) is int:
                document["totalSize"] += rest[0]
        elif kind == "reverse":
            rows.reverse()
        elif kind == "total" and type(document.get("totalSize")) is int:
            document["totalSize"] += rest[0]
        elif kind == "locator":
            document.update(done=False, nextRecordsUrl=rest[0])
        elif kind == "cursor":
            document["response_metadata"] = {"next_cursor": rest[0]}
    return document


def encoded(document, mode):
    """The body for one document in the drawn form, and the header that form implies."""
    kind, value = mode
    spelled = _RAW.sub(lambda match: match[1], json.dumps(document, ensure_ascii=kind != "encoding"))
    body = spelled.encode("utf-8", "surrogatepass")
    if kind == "encoding":
        body = spelled.encode(value, "surrogatepass" if value.startswith("utf") else "replace")
    elif kind == "cut":
        body = body[:len(body) * value // 100]
    elif kind == "append":
        body += value
    elif kind == "pad":
        body = b" " * value + body
    elif kind == "nest":
        body = b"[" * value + body + b"]" * value
    elif kind == "duplicate":
        body = b'{"ok":true,"done":true,"name":"Account",' + body[1:]
    elif kind == "gzip":
        return gzip.compress(body, mtime=0), ({b"content-encoding": b"gzip"} if value else {})
    elif kind == "bytes":
        body = value
    return body, {}


def parsed(body, *, text):
    """What a tolerant reader finds in the bytes, and whether they are one unambiguous JSON document."""
    doubts = []

    def pairs(items):
        if len({key for key, _ in items}) != len(items):
            doubts.append("duplicate key")
        return dict(items)

    try:
        return json.loads(body.decode("utf-8") if text else body, parse_float=Decimal, object_pairs_hook=pairs,
                          parse_constant=doubts.append), not doubts
    except (ValueError, RecursionError, ArithmeticError):
        return None, False


class Parts(httpx2.SyncByteStream, httpx2.AsyncByteStream):
    """A body sent in pieces, optionally failing after the last one."""

    def __init__(self, parts, error=None):
        self.parts, self.error = parts, error

    def __iter__(self):
        yield from self.parts
        if self.error is not None:
            raise self.error

    async def __aiter__(self):
        for part in self:
            yield part


def answer(spec, kind, document, content_type, tables, *, text=False, **noted):
    """Record one exchange: a valid document changed as its spec says and encoded for the wire."""
    mode = spec.get("body", ("plain", None))
    body, implied = encoded(edited(document, spec.get("edits", ()), *tables), mode)
    headers = {b"content-type": content_type, b"content-length": str(len(body)).encode(), **implied,
               **dict(spec.get("headers", ()))}
    found, sound = parsed(body, text=text)
    error = spec.get("fail", httpx2.ReadError if mode[0] == "break" else None)
    return {"kind": kind, "spec": spec, "status": spec.get("status", 200), "error": error, "body": body,
            "headers": {name: value for name, value in headers.items() if value is not None},
            "document": found, "sound": sound and error is None, **noted}


def send(exchange):
    """Return the response of a recorded exchange, or raise its transport failure."""
    spec, body = exchange["spec"], exchange["body"]
    if "fail" in spec:
        raise spec["fail"](MARK)
    kind, value = spec.get("body", ("plain", None))
    parts, error = [body], None
    if kind == "chunks":
        parts = [body[at:at + value] for at in range(0, len(body), value)]
    elif kind == "break":
        parts, error = [body[:len(body) * value // 100]], httpx2.ReadError(MARK)
    # A stream keeps httpx from decoding the body before the reader has seen the headers.
    return httpx2.Response(exchange["status"], headers=list(exchange["headers"].items()), stream=Parts(parts, error))


def header(exchange, name):
    return exchange["headers"].get(name, b"").decode("utf-8", "replace").lower()


def compressed(exchange):
    return any(coding in re.split(r"[ ,]+", header(exchange, b"content-encoding")) for coding in CODINGS)


def canonical(value):
    """JSON text that tells True from 1 and 1 from 1.0, which == does not."""
    return json.dumps(value, sort_keys=True)


def fixed(case, error, kind, codes, secrets):
    """A refusal is one documented code and nothing else: no upstream text, credential, or address."""
    case.assertIs(type(error), kind)
    case.assertIn(error.code, codes)
    case.assertEqual((str(error), error.args), (error.code, (error.code,)))
    shown = "".join(traceback.format_exception(error)) + repr(error) + repr(vars(error))
    for secret in (MARK, OMITTED, *secrets):
        case.assertNotIn(secret, shown)
    return error.code


@contextmanager
def documented_shape():
    """Reading an accepted answer the way the docs describe it must not fail."""
    try:
        yield
    except (KeyError, TypeError, IndexError, AttributeError, ValueError) as error:
        raise AssertionError(f"An answer outside the documented shape was accepted: {error!r}") from error


# Salesforce.

ROWS = {
    "Account": st.fixed_dictionaries({"Name": plain, "Industry": st.none() | plain,
                                      "AccountNumber": st.none() | plain}),
    "Contact": st.fixed_dictionaries({"Name": plain, "Title": st.none() | plain, "Email": st.none() | plain}),
    "Opportunity": st.fixed_dictionaries({"Name": plain, "StageName": plain, "Amount": st.none() | amounts,
                                          "CloseDate": dates, "IsClosed": st.booleans(), "IsWon": st.booleans()}),
    "Contract": st.fixed_dictionaries({
        "Status": plain, "StatusCode": st.sampled_from(("Draft", "InApproval", "Activated")),
        "StartDate": st.none() | dates, "EndDate": st.none() | dates,
        "ContractTerm": st.none() | st.integers(-2 ** 31, 2 ** 31 - 1)}),
}
one_account = st.fixed_dictionaries({"account_id": st.sampled_from(ACCOUNTS).map("acc-sf-".__add__)})
selection = st.lists(st.sampled_from(ACCOUNTS), min_size=1, max_size=3, unique=True).map(
    lambda found: {"account_ids": ["acc-sf-" + value for value in found]})
literal = st.text(st.characters(min_codepoint=32, max_codepoint=0x24FF, exclude_characters="\x7f"), max_size=12)
row_reads = st.one_of(
    st.tuples(st.just("search_accounts"), st.builds(lambda query, ids: {"query": query, **ids}, literal,
                                                    st.just({}) | selection)),
    st.tuples(st.just("get_account"), one_account),
    st.tuples(st.just("list_opportunities"), st.one_of(st.just({}), one_account, selection)),
)
operations = st.one_of(
    row_reads, row_reads,
    st.tuples(st.just("search_accounts"), st.builds(
        lambda number, ids: {"account_number": number, **ids},
        st.sampled_from(("FIXTURE-001", "It's \\ 7", " É-7 ")), st.just({}) | selection)),
    st.tuples(st.sampled_from(("probe_accounts", "check_edition", "prepare_connection")), st.just({})),
    st.tuples(st.just("describe_fields"), st.fixed_dictionaries({"object_name": st.sampled_from(sorted(FIELDS))})),
    st.tuples(st.just("candidate_account"), st.fixed_dictionaries({"as_of": dates})),
)
OWNER_IDS = ACCOUNTS + ("001000000000001AAA", "00D000000000001AAA", "001000000000000", "001000000000000AAAA",
                        "001-00000000000AAA", "\uff10\uff10\uff11000000000000000", None, 7)
SALESFORCE_TOP = {
    "totalSize": (0, 1, 2, 99, 100, 101, -1, True, None, "1", 1.0), "done": FLAGS, "queryable": FLAGS,
    "records": ([], None, {}), "fields": ([], None, [{"name": "Id"}] * 2), "name": (*sorted(FIELDS), "User", None),
    "nextRecordsUrl": (QUERY + "/01gUNISSUED-9", *HOSTILE_LOCATORS),
}
SALESFORCE_ROW = {
    "Id": OWNER_IDS, "AccountId": OWNER_IDS, "attributes": (None, [], MARK),
    **dict.fromkeys(("Name", "Industry", "Title", "Email", "StageName", "Status"), TEXTS + WRONG_TEXTS),
    "AccountNumber": TEXTS + WRONG_TEXTS + ("FIXTURE-001", "fixture-001", "FIXTURE-001 "),
    "Amount": (None, "100", "1.234,56", "$1,000.00", True, *NUMBERS), **dict.fromkeys(("IsClosed", "IsWon"), FLAGS),
    **dict.fromkeys(("CloseDate", "StartDate", "EndDate"), DAYS),
    "StatusCode": ("Draft", "InApproval", "Activated", "Terminated", "Expired", "draft", None, []),
    "ContractTerm": (None, 0, 12, -1, 2 ** 31 - 1, 2 ** 31, -2 ** 31 - 1, True, 1.5, "12"),
    "OrganizationType": ("Developer Edition", "Enterprise Edition", "Trial", " Developer Edition", "", None, MARK),
    "IsSandbox": FLAGS, "name": (*sorted(set().union(*FIELDS.values())), "../Id", "Extra__c", None, 7),
}
SALESFORCE_TABLES = (SALESFORCE_TOP, SALESFORCE_ROW)
salesforce_edits = changes(
    (*SALESFORCE_TOP, "extra"), (*SALESFORCE_ROW, "Extra__c"),
    st.tuples(st.just("total"), st.sampled_from((-1, 1, 100, 2 ** 40))),
    st.tuples(st.just("locator"), st.sampled_from(HOSTILE_LOCATORS + (QUERY + "/01gUNISSUED-9",)) | values),
    st.sampled_from(("REQUEST_LIMIT_EXCEEDED", "API_DISABLED_FOR_ORG", "INSUFFICIENT_ACCESS", 7, None)).map(
        lambda code: ("whole", [{"errorCode": code, "message": MARK}])),
)
denied = st.sampled_from(("REQUEST_LIMIT_EXCEEDED", "API_DISABLED_FOR_ORG", "INSUFFICIENT_ACCESS")).map(
    lambda code: {"status": 403, "edits": [("whole", [{"errorCode": code, "message": MARK}])]})


salesforce_mutations = mutations(salesforce_edits, denied, st.just({"status": 401}), st.just({"status": 401}))


@st.composite
def salesforce_plans(draw, mode="any"):
    """One org, one adapter, and the reads to make. A valid plan stays inside every documented limit."""
    small = mode != "any"
    return {
        "environment": draw(st.sampled_from(("developer", "developer", "sandbox", "production"))),
        # None is a pasted token. Otherwise an sf login, and what a second read of it gives.
        "sf": None if mode == "valid" else draw(st.sampled_from(
            (None, None, "fresh", "fresh", "other_org", "salesforce_auth_required", "salesforce_cli_timeout"))),
        "templates": {name: draw(st.lists(rows, min_size=1, max_size=2)) for name, rows in ROWS.items()},
        "count": draw(st.sampled_from((0, 1, 2, 3, 5) if small else
                                      (0, 1, 1, 2, 3, 5, 10, 11, 40, 99, 100, 101, 130, 1000, 1001))),
        "page": draw(st.sampled_from((None, None, 2, 3) if small else (None, None, None, 1, 2, 3, 10, 50, 200))),
        "operations": draw(st.lists(row_reads if mode == "valid" else operations, min_size=1,
                                    max_size=1 if small else 3)),
        "specs": specs(draw, salesforce_mutations, mode),
    }


class Org:
    """An invented org: valid answers to the adapter's fixed GETs, each changed as its spec says.

    It also stands in for the adapter's clock and for the sf CLI, so a plan decides both.
    """

    def __init__(self, plan):
        self.plan, self.exchanges, self.violations = plan, [], []
        self.now, self.invocations, self.bearers = 0.0, 0, {"Bearer " + TOKEN}
        self.chain = self.arguments = None
        self.identities, self.offered, self.proven = set(), set(), set()

    def monotonic(self):
        return self.now

    def read(self):
        self.invocations += 2
        mode = self.plan["sf"]
        if self.invocations == 2:
            return ORIGIN, TOKEN
        if mode in SALESFORCE_CODES:
            raise SFCredentialsError(mode)
        if mode == "fresh":
            self.bearers.add("Bearer " + FRESH)
        return (ORIGIN if mode == "fresh" else OTHER_ORIGIN), FRESH

    def adapter(self):
        options = {"environment": self.plan["environment"], "transport": httpx2.MockTransport(self)}
        if self.plan["sf"] is None:
            return salesforce.SalesforceReadOnlyAdapter(ORIGIN, TOKEN, **options)
        with patch.object(salesforce, "SFCredentials", lambda alias: self):
            return salesforce.SalesforceReadOnlyAdapter.from_sf("invented-alias", **options)

    def __call__(self, request):
        spec = self.plan["specs"][len(self.exchanges)] if len(self.exchanges) < len(self.plan["specs"]) else {}
        kind, name, document = self.valid(request)
        self.now += spec.get("seconds", 0)
        paged = kind in ("query", "page")
        exchange = answer(spec, kind, document, b"application/json;charset=UTF-8", SALESFORCE_TABLES, object=name,
                          over=paged and self.chain["over"])
        self.exchanges.append(exchange)
        following = exchange["document"].get("nextRecordsUrl") if isinstance(exchange["document"], dict) else None
        if paged and exchange["status"] == 200 and isinstance(following, str):
            self.chain["issued"].add(following)
        with suppress(KeyError, TypeError, AttributeError):
            if kind == "org":
                # Every organization ID an answer carried. A saved identity must be one of them.
                self.offered.update(row["Id"] for row in exchange["document"]["records"] if isinstance(row["Id"], str))
            # Sandbox and production reads need all four objects queryable with the mapped fields.
            proof = exchange["document"] if kind == "describe" and exchange["status"] == 200 else {}
            if (proof["name"], proof["queryable"]) == (name, True) and FIELDS[name] <= {
                    field["name"] for field in proof["fields"]}:
                self.proven.add(name)
        return send(exchange)

    def valid(self, request):
        """The kind of request and a valid answer to it. A request outside the fixed shape is recorded."""
        url, raw = request.url, request.url.raw_path.decode("ascii")
        if ((request.method, url.scheme, url.host, url.port) != ("GET", "https", ORIGIN[8:], None) or request.content
                or request.headers.get("accept-encoding") != "identity"
                or request.headers.get("authorization") not in self.bearers):
            self.violations.append("A request left the fixed origin, method, or credential.")
        described = re.fullmatch(r"/services/data/v68\.0/sobjects/(\w+)/describe", raw)
        if described and described[1] in FIELDS:
            return "describe", described[1], {"name": described[1], "queryable": True, "label": MARK, "fields": [
                {"name": name, "label": MARK} for name in sorted(FIELDS[described[1]])]}
        if url.path == QUERY and set(url.params) == {"q"}:
            return self.query(url.params["q"])
        if self.chain is not None and raw in self.chain["issued"] and LOCATOR.fullmatch(raw):
            return "page", self.chain["object"], self.page(self.chain["offsets"].get(raw, self.chain["sent"]))
        self.violations.append("A request used a path or locator outside the supported shape.")
        return "unknown", None, {}

    def query(self, text):
        plan, name = self.plan, re.search(r" FROM (\w+)", text)[1]
        if name == "Organization":
            row = {"attributes": {"type": name, "url": MARK}, **ORGANIZATIONS[plan["environment"]]}
            return "org", name, {"totalSize": 1, "done": True, "records": [row]}
        selected = re.findall(r"'([A-Za-z0-9]{18})'", text)
        lookup, dated = " WHERE AccountNumber = '" in text, re.search(r"EndDate >= (\S+)", text)
        by_id = name == "Account" and " WHERE Id = '" in text
        owned = name != "Account" and " WHERE AccountId = '" in text
        limit = 2 if lookup else 1 if by_id or text.endswith(" LIMIT 1") else 1000 if owned else 100
        search = name == "Account" and limit == 100
        if search and not text.endswith(" ORDER BY Name, Id LIMIT 101"):
            self.violations.append("A name search did not ask for name order and one row past its bound.")
        # A name search answers with at most the 101 rows it asks for. A lookup may answer with one row too many.
        # Queries for one row never do.
        count = min(plan["count"], {1: 1, 2: 3, 100: 101 if search else 999, 1000: 9999}[limit])
        if name == "Account" and selected:
            count = min(count, len(selected))
        rows, templates = [], plan["templates"][name]
        for number in range(count):
            row = {"attributes": {"type": name, "url": MARK}, "Id": f"{PREFIX[name]}{number:015d}",
                   **templates[number % len(templates)]}
            if name == "Account" and selected:
                row["Id"] = selected[number]
            if name != "Account":
                row["AccountId"] = (selected[number % len(selected)] if selected
                                    else OWNERS[number % 3 if dated is None else 0])
            if lookup:
                row["AccountNumber"] = self.arguments["account_number"]
            if dated:
                row["EndDate"] = dated[1]
            rows.append(row)
        size = plan["page"] or count or 1
        # The documented bounds: 1,000 rows of one account, 100 of an opportunity list across accounts, 2 for a
        # number lookup, and 10 pages. A name search past 100 rows is cut, not refused.
        self.chain = {"object": name, "rows": rows, "offsets": {}, "issued": set(), "sent": 0,
                      "over": (count > limit > 1 and not search) or -(-count // size) > 10}
        return "query", name, self.page(0)

    def page(self, start):
        chain = self.chain
        part = chain["rows"][start:start + (self.plan["page"] or len(chain["rows"]) or 1)]
        chain["sent"] = end = start + len(part)
        document = {"totalSize": len(chain["rows"]), "done": end >= len(chain["rows"]), "records": deepcopy(part)}
        if not document["done"]:
            document["nextRecordsUrl"] = f"{QUERY}/01g{len(self.exchanges):09d}-{end}"
            chain["offsets"][document["nextRecordsUrl"]] = end
        return document


def native(value):
    if not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9]{18}", value):
        raise TypeError("not an 18-character Salesforce ID")
    return value


def text_of(row, field, nullable=False):
    value = row[field]
    if not (isinstance(value, str) or (nullable and value is None)):
        raise TypeError(field)
    if value is not None:
        value.encode("utf-8")
    return value


def day_of(row, field, nullable=False):
    value = text_of(row, field, nullable)
    if value is not None and date.fromisoformat(value).isoformat() != value:
        raise ValueError(field)
    return value


def flag_of(row, field):
    if type(row[field]) is not bool:
        raise TypeError(field)
    return row[field]


def account(row):
    """The record docs/python/salesforce.md maps one Account row to. The other builders follow the same table."""
    return {"id": "acc-sf-" + native(row["Id"]), "source_id": row["Id"], "backend": "salesforce",
            "name": text_of(row, "Name"), "industry": text_of(row, "Industry", True),
            "account_number": text_of(row, "AccountNumber", True)}


def owned(row, asked):
    if native(row["AccountId"]) != asked:
        raise ValueError("a record of another account")
    return "acc-sf-" + asked


def contact(row, asked):
    return {"id": "con-sf-" + native(row["Id"]), "source_id": row["Id"], "backend": "salesforce",
            "account_id": owned(row, asked), "name": text_of(row, "Name"), "title": text_of(row, "Title", True),
            "email": text_of(row, "Email", True)}


def contract(row, asked):
    term = row["ContractTerm"]
    if term is not None and (type(term) is not int or not -2 ** 31 <= term < 2 ** 31):
        raise TypeError("ContractTerm")
    if text_of(row, "StatusCode") not in ("Draft", "InApproval", "Activated"):
        raise ValueError("StatusCode")
    return {"id": "ctr-sf-" + native(row["Id"]), "source_id": row["Id"], "backend": "salesforce",
            "account_id": owned(row, asked), "status": text_of(row, "Status"), "status_code": row["StatusCode"],
            "start_date": day_of(row, "StartDate", True), "end_date": day_of(row, "EndDate", True),
            "contract_term": term}


def opportunity(row, returned):
    """The returned amount must be a plain decimal string equal to the upstream number, never a float."""
    amount, related = row["Amount"], row["AccountId"]
    if amount is not None and (type(amount) not in (int, Decimal) or not isinstance(returned, str)
                               or not re.fullmatch(r"-?[0-9]+(\.[0-9]+)?", returned) or Decimal(returned) != amount):
        raise ValueError("Amount")
    return {"id": "opp-sf-" + native(row["Id"]), "source_id": row["Id"], "backend": "salesforce",
            "account_id": None if related is None else "acc-sf-" + native(related), "name": text_of(row, "Name"),
            "stage": text_of(row, "StageName"), "close_date": day_of(row, "CloseDate"),
            "is_closed": flag_of(row, "IsClosed"), "is_won": flag_of(row, "IsWon"),
            "amount_decimal": None if amount is None else returned, "currency": None}


def salesforce_cause(code, org, arguments, trace, elapsed):
    """Whether an upstream answer in this read can explain the refusal, by the causes the docs give."""
    statuses = {exchange["status"] for exchange in trace if "fail" not in exchange["spec"]}
    timed = [issubclass(exchange["error"], httpx2.TimeoutException) for exchange in trace if exchange["error"]]
    kinds, developer = {exchange["kind"] for exchange in trace}, org.plan["environment"] == "developer"
    return {
        "salesforce_auth_required": 401 in statuses,
        "salesforce_cli_timeout": 401 in statuses and org.plan["sf"] == code,
        "salesforce_rate_limited": bool(statuses & {403, 429}), "salesforce_api_disabled": 403 in statuses,
        "salesforce_forbidden": 403 in statuses or "describe" in kinds,
        "salesforce_http_error": bool(statuses - {200, 401, 403, 429}),
        "salesforce_timeout": elapsed >= 20 or any(timed), "salesforce_unavailable": not all(timed),
        "salesforce_edition_required": "org" in kinds and developer, "salesforce_environment_mismatch": "org" in kinds,
        "salesforce_schema_unavailable": "describe" in kinds and not developer,
        "salesforce_account_number_ambiguous": "account_number" in arguments,
        "salesforce_pagination_invalid": bool(kinds & {"org", "query", "page"}),
        "salesforce_invalid_configuration": False, "salesforce_invalid_arguments": False,
    }.get(code, True)


class SalesforceFuzz(unittest.TestCase):
    def read(self, org, adapter, name, arguments):
        """Run one read and hold its outcome to the properties. Returns the refusal code, or None."""
        start, started, logins = len(org.exchanges), org.now, org.invocations
        org.arguments = arguments
        try:
            result = getattr(adapter, name)(**arguments)
        except salesforce.SalesforceReadError as error:
            trace = org.exchanges[start:]
            code = fixed(self, error, salesforce.SalesforceReadError, SALESFORCE_CODES,
                         (TOKEN, FRESH, ORIGIN[8:], OTHER_ORIGIN[8:]))
            self.assertTrue(salesforce_cause(code, org, arguments, trace, org.now - started),
                            f"{code} has no documented cause in this read")
            if not any(exchange["spec"] for exchange in trace):
                # Every answer was valid, so only a documented bound can refuse the read.
                self.assertEqual(code, "salesforce_result_limit")
                self.assertTrue(any(exchange["over"] for exchange in trace) or len(trace) == 12
                                or sum(len(exchange["body"]) for exchange in trace) > MIB)
        else:
            trace, code = org.exchanges[start:], None
            self.accepted(org, name, arguments, result, trace, org.now - started)
        self.assertEqual(org.violations, [])
        self.assertLessEqual(len(trace), 12)
        self.assertLessEqual(org.invocations - logins, 2, "A read asked sf for credentials more than once.")
        return code

    def rows(self, pages, limit=100):
        """The upstream rows of one accepted query, once its pages are seen to be complete and in bounds."""
        rows = [row for page in pages for row in page["records"]]
        self.assertLessEqual(len(pages), 10)
        self.assertLessEqual(len(rows), limit)
        for page in pages:
            self.assertIs(page["done"], page is pages[-1])
            self.assertEqual(canonical(page["totalSize"]), canonical(len(rows)), "Partial or miscounted rows.")
            self.assertTrue(page is pages[-1] or LOCATOR.fullmatch(page["nextRecordsUrl"]))
        self.assertEqual(len({native(row["Id"]) for row in rows}), len(rows))
        return rows

    def accepted(self, org, name, arguments, result, trace, elapsed):
        plan = org.plan
        used = [exchange for exchange in trace if exchange["status"] == 200]
        # Only one expired token is retried, and only when sf gave a fresh one for the same org.
        self.assertLessEqual(len(trace) - len(used), plan["sf"] == "fresh")
        self.assertTrue(all(exchange["status"] in (200, 401) for exchange in trace))
        self.assertLessEqual(sum(len(exchange["body"]) for exchange in trace), MIB)
        self.assertLess(elapsed, 20)
        for exchange in used:
            # Content types are held to their property in one test of their own, so that it hides nothing here.
            self.assertTrue(exchange["sound"], "A body that is not one unambiguous JSON document was accepted.")
            self.assertFalse(compressed(exchange), "A compressed response was accepted.")
        with documented_shape():
            queries, described = [], {}
            for exchange in used:
                document = exchange["document"]
                if exchange["kind"] == "org":
                    (row,) = self.rows([document], 1)
                    edition, sandbox = text_of(row, "OrganizationType"), flag_of(row, "IsSandbox")
                    self.assertTrue({"developer": edition == "Developer Edition" and not sandbox, "sandbox": sandbox,
                                     "production": not sandbox and edition != "Developer Edition"}[plan["environment"]])
                    org.identities.add(row["Id"])
                elif exchange["kind"] == "describe":
                    described[exchange["object"]] = document
                elif exchange["kind"] == "query":
                    queries.append((exchange["object"], [document]))
                elif exchange["kind"] == "page":
                    queries[-1][1].append(document)
            self.assertLessEqual(len(org.identities), 1, "The org behind one adapter changed.")
            asked, ids = arguments.get("account_id", "")[7:], arguments.get("account_ids")
            if name in ("search_accounts", "probe_accounts"):
                ((_, pages),) = queries
                found = self.rows(pages, 2 if "account_number" in arguments else 1 if name == "probe_accounts" else 101)
                expected = [account(row) for row in found]
                self.assertTrue(all(ids is None or row["id"] in ids for row in expected))
                self.assertTrue(all(row["AccountNumber"] == arguments.get("account_number", row["AccountNumber"])
                                    for row in found))
                if name == "search_accounts":
                    # The first 100 rows, and whether a 101st matched. A selection of at most 100 never has one.
                    expected = (expected[:100], len(expected) > 100)
                    self.assertFalse(ids is not None and expected[1])
            elif name == "get_account":
                found = self.rows(queries[0][1], 1)
                self.assertEqual([kind for kind, _ in queries],
                                 ["Account", "Contact", "Contract"] if found else ["Account"])
                expected = None
                if found:
                    self.assertEqual(found[0]["Id"], asked)
                    expected = {**account(found[0]),
                                "contacts": [contact(row, asked) for row in self.rows(queries[1][1], 1000)],
                                "contracts": [contract(row, asked) for row in self.rows(queries[2][1], 1000)]}
            elif name == "list_opportunities":
                ((_, pages),) = queries
                found = self.rows(pages, 1000 if asked else 100)
                self.assertEqual(len(result), len(found))
                expected = [opportunity(row, got["amount_decimal"]) for row, got in zip(found, result)]
                self.assertTrue(all(not asked or row["AccountId"] == asked for row in found))
                self.assertTrue(all(ids is None or row["account_id"] in ids for row in expected))
            elif name == "candidate_account":
                ((_, pages),) = queries
                expected = None
                for row in self.rows(pages, 1):
                    expected = contract(row, row["AccountId"])["account_id"]
                    self.assertGreaterEqual(day_of(row, "EndDate"), arguments["as_of"])
            elif name == "describe_fields":
                document = described[arguments["object_name"]]
                expected = sorted(text_of(field, "name") for field in document["fields"])
                self.assertEqual(len(set(expected)), len(expected))
                self.assertTrue(plan["environment"] == "developer" or (document["name"], document["queryable"])
                                == (arguments["object_name"], True))
                result = sorted(result)
            elif name == "prepare_connection":
                self.assertIn(result["organization_id"], org.identities or org.offered)
                expected = {"schema_version": 1, "backend": "salesforce", "instance_url": ORIGIN,
                            "organization_id": result["organization_id"], "environment": plan["environment"]}
            else:
                expected = None
            self.assertEqual(canonical(result), canonical(expected))
            if plan["environment"] != "developer" and name not in ("check_edition", "describe_fields"):
                self.assertEqual(org.proven, set(FIELDS), "A read went ahead without the four objects' fields.")

    @fuzz(2000)
    @given(salesforce_plans() | salesforce_plans("one"))
    def test_any_answer_gives_documented_records_or_a_fixed_refusal(self, plan):
        self.run_plan(plan)

    def test_every_status_and_every_listed_field_value_alone(self):
        steady = {"environment": "developer", "sf": None, "count": 2, "page": None, "specs": [], "templates": {
            "Account": [{"Name": MARK, "Industry": None, "AccountNumber": "FIXTURE-001"}],
            "Contact": [{"Name": MARK, "Title": None, "Email": MARK}],
            "Opportunity": [{"Name": MARK, "StageName": MARK, "Amount": RAW + "12345.67", "CloseDate": "2027-02-01",
                             "IsClosed": False, "IsWon": False}],
            "Contract": [{"Status": MARK, "StatusCode": "Draft", "StartDate": "2026-02-01", "EndDate": "2027-01-31",
                          "ContractTerm": 12}]}}
        one, two = "acc-sf-" + ACCOUNTS[0], ["acc-sf-" + ACCOUNTS[0], "acc-sf-" + ACCOUNTS[1]]
        plans = [{**steady, "operations": [read]} for read in (
            ("search_accounts", {"query": ""}), ("search_accounts", {"account_number": "FIXTURE-001"}),
            ("search_accounts", {"query": "", "account_ids": two}), ("get_account", {"account_id": one}),
            ("list_opportunities", {}), ("list_opportunities", {"account_id": one}),
            ("list_opportunities", {"account_ids": two}), ("candidate_account", {"as_of": "2027-01-15"}),
            ("describe_fields", {"object_name": "Contract"}))]
        plans += [{**plans[0], "count": 3, "page": 2}, {**plans[3], "environment": "production", "sf": "fresh"}]
        for plan in plans:
            valid = self.run_plan(plan)
            self.assertEqual(valid.codes, [None])
            for place, exchange in enumerate(valid.exchanges):
                alone = [{"edits": [edit]} for edit in one_at_a_time(exchange["document"], *SALESFORCE_TABLES, False)]
                # Every status is tried once at the organization check and once at a record query.
                alone += [{"status": status} for status in range(100, 600) if plan is plans[0]]
                for spec in alone:
                    with self.subTest(read=plan["operations"][0], place=place, spec=spec):
                        self.run_plan({**plan, "specs": [{}] * place + [spec]})

    @fuzz(100, 0.05)
    @given(salesforce_plans("valid"), st.sampled_from(((101, None), (130, 50), (300, 100), (1001, None), (1300, 500),
                                                       (11, 1), (22, 2), (33, 3))))
    def test_too_many_rows_or_pages_refuse_the_whole_read(self, plan, size):
        org = self.run_plan({**plan, "count": size[0], "page": size[1]})
        if any(exchange["over"] for exchange in org.exchanges):
            self.assertEqual(org.codes, ["salesforce_result_limit"])
        elif plan["operations"][0][0] == "search_accounts":
            # Within the page limit, a name search past 100 rows is answered with its first 100.
            self.assertEqual(org.codes, [None])

    def run_plan(self, plan):
        org = Org(plan)
        with patch.object(salesforce, "time", org):
            adapter = org.adapter()
            org.codes = [self.read(org, adapter, name, arguments) for name, arguments in plan["operations"]]
        return org

    def changed_at(self, plan, change, suits=lambda exchange: True):
        """Run a valid plan with its last suitable answer changed. Returns the refusal code, or None for a result."""
        places = [place for place, exchange in enumerate(self.run_plan(plan).exchanges) if suits(exchange)]
        assume(places)
        return self.run_plan({**plan, "specs": [{}] * places[-1] + [change]}).codes[0]

    @fuzz(60, 0.05)
    @given(salesforce_plans("valid"), st.sampled_from((
        ({"body": ("pad", MIB)}, "salesforce_result_limit"),
        ({"headers": [(b"content-encoding", b"gzip")]}, "salesforce_invalid_response"),
        ({"headers": [(b"content-encoding", b"br")]}, "salesforce_invalid_response"),
        ({"body": ("gzip", True)}, "salesforce_invalid_response"),
        ({"body": ("gzip", False)}, "salesforce_invalid_response"),
        ({"status": 301, "headers": [(b"location", QUERY.encode() + b"/01gMOVED-1")]}, "salesforce_http_error"),
        ({"status": 302, "headers": [(b"location", b"https://untrusted.example/")]}, "salesforce_http_error"),
        ({"status": 307, "headers": [(b"location", ORIGIN.encode() + QUERY.encode())]}, "salesforce_http_error"),
    )))
    def test_one_out_of_bounds_answer_refuses_with_its_code(self, plan, case):
        self.assertEqual(self.changed_at(plan, case[0]), case[1])

    @fuzz(60, 0.05)
    @given(salesforce_plans("valid"), st.sampled_from(HOSTILE_LOCATORS))
    def test_an_unsupported_locator_is_refused_and_never_requested(self, plan, locator):
        # The change lands on a page that has more to come, where a reader would follow the locator.
        code = self.changed_at({**plan, "count": 5, "page": 2}, {"edits": [("locator", locator)]},
                               lambda exchange: exchange["document"].get("done") is False)
        self.assertEqual(code, "salesforce_pagination_invalid")

    @fuzz(20, 0.01)
    @given(salesforce_plans("valid"), st.sampled_from(NOT_JSON))
    def test_a_non_json_content_type_is_refused(self, plan, content_type):
        self.assertEqual(self.changed_at(plan, {"headers": [(b"content-type", content_type)]}),
                         "salesforce_invalid_response")

    @fuzz(12, 0.01)
    @given(salesforce_plans("valid"), st.integers(0, 24), st.lists(salesforce_mutations, min_size=1, max_size=3))
    def test_salesforce_check_reports_fixed_codes_and_nothing_upstream(self, plan, place, drawn):
        plan["specs"] = [{}] * place + drawn
        org = Org(plan)
        with patch.object(salesforce, "time", org):
            report = run_check(as_of="2027-01-15", environment=plan["environment"], adapter_factory=org.adapter)
        self.assertEqual(org.violations, [])
        for secret in (MARK, TOKEN, ORIGIN[8:], ACCOUNTS[0], ORGANIZATIONS["developer"]["Id"]):
            self.assertNotIn(secret, json.dumps(report))
        for step in report["steps"]:
            # check_failed means an exception other than the adapter's fixed refusal reached the report.
            self.assertIn(step["code"], SALESFORCE_CODES | {"ok", "prerequisite_failed", "missing_fields",
                                                            "renewal_fields_unmapped"}, step["name"])


# Slack.

rich = st.lists(st.just({"text": OMITTED, "url_private": "https://invalid.example/" + OMITTED}), max_size=3)
messages = st.builds(
    lambda text, author, more: {"type": "message", "text": text, **author, **more},
    plain | st.sampled_from(("x" * 16384, "é" * 8192)),
    st.sampled_from((
        {"user": "U12345678"}, {"user": "W2"}, {"bot_id": "B12345678"}, {}, {"user": "U12345678", "bot_id": "B7"},
        {"subtype": "bot_message", "bot_id": "B1", "username": MARK}, {"subtype": "bot_message"},
        {"subtype": "me_message", "user": "U1"}, {"subtype": "file_share", "user": "U1"},
        {"subtype": "thread_broadcast", "user": "U1", "root": {"ts": PARENT, "text": OMITTED}})),
    st.fixed_dictionaries({}, optional={
        "files": rich, "attachments": rich, "blocks": rich, "channel": st.just(CHANNEL), "hidden": st.just(False),
        "edited": st.just({"ts": "1790000000.000000", "user": "U12345678"}), "team": st.just("T0OTHER"),
        "reactions": st.just([{"name": MARK, "users": ["U1"]}])}))
STAMPS = (PARENT, "1700000000.000000", "1700000000.000002", "1700000000.000003", "1700000000.000009", CUTOFF,
          "1795003200.000001", "1700000000.1", "1700000000", " 1700000000.000002", None, 1700000000.000002)
PEOPLE = (None, "U12345678", "U87654321", "W1", "B12345678", "u12345678", "U", 7)
RICH = ([], [{"text": OMITTED}], [{"text": OMITTED}] * 3, None, {}, ["bad"], [None], "text")
OTHER_USER = {"id": USER, "token_type": "user", "scope": "channels:history",
              "access_token": "invented-other-access-token"}
SLACK_TOP = {
    "ok": FLAGS, "error": SLACK_ERRORS, "has_more": FLAGS, "messages": ([], None, {}),
    "response_metadata": (None, {}, [], "page-0", *({"next_cursor": cursor} for cursor in (
        "page-0", "never-" + MARK, "x" * 2048, "x" * 2049, "\u00e9" * 1025, "bad\n", 7, None))),
    "team_id": (TEAM, "T87654321", None), "user_id": (USER, "U87654321", None),
    "enterprise_id": (None, "E12345678"), "enterprise": (None, {"id": "E12345678"}),
    "url": ("https://other.slack.com/", "http://invented-workspace.slack.com/", None, MARK),
    "access_token": ("invented-other-access-token", "", "has space", None),
    "refresh_token": ("invented-other-refresh-token", "", None), "token_type": ("user", "bot", None),
    "expires_in": (0, 1, 3600, 86400, 86401, -1, True, "3600", 1.5),
    "scope": ("channels:history", "groups:history", "channels:history,groups:history", "admin", "", None),
    "bot_id": PEOPLE, "bot_user_id": PEOPLE, "is_enterprise_install": FLAGS, "app_id": (APP, "A87654321", None),
    "team": ({"id": TEAM}, {"id": "T87654321"}, {}, None),
    "authed_user": ({}, None, OTHER_USER, {**OTHER_USER, "id": "U87654321"}, {**OTHER_USER, "token_type": "bot"},
                    {**OTHER_USER, "scope": "admin"}, {**OTHER_USER, "bot_id": "B12345678"}),
}
SLACK_ROW = {
    "type": ("message", "event_callback", None), "ts": STAMPS, "thread_ts": STAMPS, "user": PEOPLE, "bot_id": PEOPLE,
    "text": TEXTS + WRONG_TEXTS + ("x" * 16384, "x" * 16385, "\u00e9" * 8193, "\x00" * 16384),
    "subtype": SUBTYPES + ("message_changed", "message_deleted", "channel_join", "tombstone", "", 7),
    "hidden": FLAGS, "files": RICH, "attachments": RICH, "blocks": RICH,
    "channel": (CHANNEL, "COTHER", "G12345678", None),
    "edited": (None, {}, {"ts": PARENT}, {"ts": "1790000000.000000", "user": "U12345678"},
               {"ts": "0.000000", "user": "U1"}, {"ts": "1790000000.000000", "user": "B1"}),
    "root": ({"ts": PARENT}, {"ts": "1700000000.000002"}, {}, None),
    "message": ({"text": OMITTED},), "previous_message": ({"text": OMITTED},), "deleted_ts": (PARENT,),
}
SLACK_TABLES = (SLACK_TOP, SLACK_ROW)
slack_edits = changes(
    (*SLACK_TOP, "extra"), (*SLACK_ROW, "extra"),
    st.sampled_from(SLACK_ERRORS).map(lambda error: ("whole", {"ok": False, "error": error, "detail": MARK})),
    st.tuples(st.just("cursor"), values),
)
expired = st.sampled_from((200, 401)).map(
    lambda status: {"status": status, "edits": [("whole", {"ok": False, "error": "token_expired"})]})
limited = st.sampled_from([b"0", b"1", b"5"] + [value for name, value in HEADERS if name == b"retry-after"]).map(
    lambda delay: {"status": 429, "headers": [(b"retry-after", delay)]})


slack_mutations = mutations(slack_edits, expired, expired, limited, limited)


@st.composite
def slack_plans(draw, mode="any"):
    """One thread and how it is paged. A valid plan is a small thread that ends."""
    small = mode != "any"
    return {
        "rotating": draw(st.booleans()), "parent": draw(messages),
        "templates": draw(st.lists(messages, min_size=1, max_size=3)),
        "count": draw(st.sampled_from((0, 1, 2, 5) if small else (0, 1, 1, 2, 2, 5, 9, 30, 98, 99, 100, 120))),
        "page": draw(st.sampled_from((None, None, 1, 2, 3, 40))),
        # Slack repeats the parent on later pages, which the reader counts as duplicates.
        "echo": draw(st.booleans()),
        "cursors": "end" if small else draw(st.sampled_from(("end", "end", "end", "end", "endless", "loop"))),
        "specs": specs(draw, slack_mutations, mode),
    }


class Sleepless:
    """The reader's asyncio, with a sleep that only moves the invented clock so a retry costs no real time."""

    def __init__(self, workspace):
        self.workspace = workspace

    def __getattr__(self, name):
        return getattr(asyncio, name)

    async def sleep(self, seconds):
        self.workspace.naps.append(seconds)
        self.workspace.clock.advance(seconds)


class Workspace:
    """An invented workspace: valid answers to the fixed Slack requests, each changed as its spec says."""

    def __init__(self, plan):
        self.plan, self.exchanges, self.violations, self.naps = plan, [], [], []
        self.clock, self.tokens, self.offsets, self.sent = Clock(), {ACCESS}, {}, 0
        self.rows = []
        if "parent" in plan:
            self.rows = [{**plan["parent"], "ts": PARENT, "reply_count": plan["count"]}] + [
                {**plan["templates"][number % len(plan["templates"])], "ts": f"1700000000.{number + 2:06d}",
                 "thread_ts": PARENT} for number in range(plan["count"])]

    def __call__(self, request):
        spec = self.plan["specs"][len(self.exchanges)] if len(self.exchanges) < len(self.plan["specs"]) else {}
        kind, cursor, document = self.valid(request)
        self.clock.advance(spec.get("seconds", 0))
        exchange = answer(spec, kind, document, b"application/json; charset=utf-8", SLACK_TABLES, text=True,
                          cursor=cursor)
        self.exchanges.append(exchange)
        found = exchange["document"]
        if kind in ("refresh", "grant") and exchange["status"] == 200 and isinstance(found, dict):
            user = found["authed_user"] if isinstance(found.get("authed_user"), dict) else found
            self.tokens.add(user.get("access_token") if isinstance(user.get("access_token"), str) else ACCESS)
        return send(exchange)

    def valid(self, request):
        """The kind of request and a valid answer to it. A request outside the fixed shape is recorded."""
        url, headers = request.url, request.headers
        form = dict(parse_qsl(request.content.decode("ascii")))
        fixed_shape = ((url.scheme, url.host, url.port) == ("https", "slack.com", None) and "cookie" not in headers
                       and headers.get("accept-encoding") == "identity")
        bearer = headers.get("authorization", "")[len("Bearer "):] in self.tokens
        if fixed_shape and (request.method, url.path) == ("GET", REPLIES) and bearer and not request.content:
            fields = dict(url.params)
            cursor, limit = fields.pop("cursor", None), fields.pop("limit", "")
            if (fields != {"channel": CHANNEL, "ts": PARENT, "latest": CUTOFF, "inclusive": "true"}
                    or not re.fullmatch(r"[1-9][0-9]?|100", limit)):
                self.violations.append("A thread request changed its fixed fields.")
            return "page", cursor, self.page(cursor)
        if fixed_shape and (request.method, url.path) == ("POST", TOKENS) and not url.query and form.get(
                "client_id") == CLIENT and "authorization" not in headers:
            if form == {"grant_type": "refresh_token", "client_id": CLIENT, "refresh_token": REFRESH}:
                return "refresh", None, replacement()
            if set(form) == {"grant_type", "client_id", "code", "redirect_uri", "code_verifier"} and (
                    form["grant_type"], form["redirect_uri"]) == ("authorization_code", REDIRECT):
                return "grant", None, grant()
        if (fixed_shape and (request.method, url.path) == ("POST", IDENTIFY) and bearer and not url.query
                and not request.content):
            return "identity", None, identity()
        self.violations.append("A request left the fixed Slack methods, or used a token no answer supplied.")
        return "unknown", None, {}

    def page(self, cursor):
        rows, mode = self.rows, self.plan["cursors"]
        start = self.offsets.get(cursor, self.sent) if cursor else 0
        part = rows[start:start + (self.plan["page"] or len(rows))]
        self.sent = end = start + len(part)
        following = f"page-{end}" if end < len(rows) else ""
        if not following and mode == "loop":
            following, end = "page-0", 0
        elif not following and mode == "endless":
            following = f"more-{len(self.exchanges)}"
        self.offsets[following] = end
        echo = [rows[0]] if self.plan["echo"] and start else []
        return {"ok": True, "messages": deepcopy(echo + part), "has_more": bool(following),
                "response_metadata": {"next_cursor": following}}


def micros(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{1,12}\.[0-9]{6}", value):
        raise ValueError("not a Slack timestamp")
    return int(value.replace(".", ""))


def observed(row):
    """What docs/python/slack.md says one accepted row becomes. Anything it says to refuse raises."""
    stamp, subtype, body = row["ts"], row.get("subtype"), row["text"]
    # The placeholder Slack leaves for a deleted parent is kept, hidden or not. Nothing else may be hidden.
    placeholder = (subtype == "tombstone" and stamp == PARENT and row.get("user") == "USLACKBOT"
                   and row.get("bot_id") is None)
    if (row["type"] != "message" or {"message", "previous_message", "deleted_ts"} & set(row)
            or (row.get("hidden", False) is not False and not (placeholder and row["hidden"] is True))
            or (subtype not in SUBTYPES and not placeholder) or not isinstance(body, str)
            or len(body.encode("utf-8")) > 16384 or not micros(PARENT) <= micros(stamp) <= micros(CUTOFF)
            or row.get("thread_ts", PARENT if stamp == PARENT else None) != PARENT
            or row.get("channel", CHANNEL) != CHANNEL):
        raise ValueError("a row the docs refuse")
    omissions = {}
    for key in ("files", "attachments", "blocks"):
        if not isinstance(row.get(key, []), list) or not all(isinstance(item, dict) for item in row.get(key, [])):
            raise TypeError(key)
        omissions[key] = len(row.get(key, []))
    app = subtype == "bot_message" or row.get("bot_id") is not None
    return {"ts": stamp, "body": body, "subtype": subtype, "user_id": row.get("user"), "bot_id": row.get("bot_id"),
            "author": "Deleted Slack message" if placeholder else "Slack app" if app
            else "Slack participant" if row.get("user") is not None else "Unknown Slack author",
            "edited": {"ts": row["edited"]["ts"], "user_id": row["edited"]["user"]} if "edited" in row else None,
            "omissions": omissions}


def positions(value, path=()):
    """Every dict key and list index in saved evidence, at any depth."""
    for key, item in value.items() if isinstance(value, dict) else enumerate(value):
        yield path + (key,)
        if isinstance(item, (dict, list)):
            yield from positions(item, path + (key,))


def replaced(value, choice):
    """A different value of the same kind, a near miss of another kind, or None."""
    if isinstance(value, bool):
        options = (not value, int(value), None)
    elif isinstance(value, int):
        options = (value + 1, value - 1, float(value), str(value), value == 1, None)
    elif isinstance(value, str):
        options = (value + "x", value[:-1], value.swapcase(), value + "\n", " " + value, MARK, None, 0, "0" + value)
    elif isinstance(value, list):
        options = (value[::-1], value + value[:1], value[:-1], None, {})
    elif isinstance(value, dict):
        options = ({**value, "extra": 1}, {}, None, [])
    else:
        options = ("", 0, False, "U12345678", "B12345678", {}, [], {"ts": PARENT, "user_id": "U12345678"})
    return options[choice % len(options)]


class SlackFuzz(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = Path(cls.enterClassContext(tempfile.TemporaryDirectory()))
        cls.connection, cls.path = cls.directory / "connection", cls.directory / "slack-mappings.json"
        slack_mapping.initialize_slack_mapping_store(cls.path, crm_binding=CRM, slack_binding=SLACK)
        cls.mapping = slack_mapping.save_slack_mapping(
            cls.path, slack_binding=SLACK, selection={"channel_id": CHANNEL, "thread_ts": PARENT},
            association={"crm_binding": CRM, "account_id": ACCOUNT,
                         "resolution_receipt_id": "12345678-1234-1234-1234-123456789abc"},
            selected_at="2026-11-18T12:00:00Z")["mapping"]

    def provider(self, workspace):
        return slack_auth.SlackProvider(_transport=httpx2.MockTransport(workspace), _clock=workspace.clock)

    def read(self, plan):
        """Read the thread once and hold the outcome to the properties. Returns the workspace with its result."""
        workspace, events = Workspace(plan), []
        seed(self.connection, saved_document(workspace.clock.now(), rotating=plan["rotating"]))
        source = slack_reader.SlackThreadReader(
            self.path, connection_dir=self.connection, crm_binding=CRM, slack_binding=SLACK, _clock=workspace.clock,
            _provider=slack_reader.SlackRepliesProvider(_transport=httpx2.MockTransport(workspace)),
            _auth_provider=self.provider(workspace), on_read=events.append)
        result = None
        with patch.object(slack_reader, "asyncio", Sleepless(workspace)):
            try:
                result = source.read_slack_messages(ACCOUNT)
            except slack_reader.SlackReadError as error:
                self.refused(workspace, error)
            else:
                self.accepted(workspace, result)
        trace = workspace.exchanges
        self.assertEqual(workspace.violations, [])
        self.assertLessEqual(len(trace), 10)
        self.assertLessEqual(len(workspace.naps), 1)
        (event,) = events
        self.assertEqual((event["status"], event["counts"]["returned_messages"]),
                         ("failed", 0) if result is None else ("passed", len(result.records)))
        for secret in (MARK, OMITTED, *SLACK_SECRETS):
            self.assertNotIn(secret, json.dumps(event))
        workspace.result = result
        return workspace

    def refused(self, workspace, error):
        plan, trace = workspace.plan, workspace.exchanges
        code = fixed(self, error, slack_reader.SlackReadError, SLACK_CODES, SLACK_SECRETS)
        self.assertNotIn(code, LOCAL_CODES)
        delays = [header(exchange, b"retry-after") for exchange in trace if exchange["status"] == 429]
        # The delay is the one value a refusal takes from Slack: a whole number of seconds, at most an hour.
        self.assertIn(error.retry_after_seconds, (None, *(int(delay) for delay in delays if re.fullmatch(
            r"[0-9]{1,4}", delay) and int(delay) <= 3600)))
        if not any(exchange["spec"] for exchange in trace) and plan["cursors"] == "end":
            # Every answer was valid, so only a documented bound can refuse the read.
            texts = [row["text"] for row in workspace.rows]
            pages = -(-len(texts) // (plan["page"] or len(texts)))
            self.assertTrue(len(texts) + plan["echo"] * (pages - 1) > 100 or pages > 10
                            or sum(len(text.encode()) for text in texts) > 65536
                            or sum(len(json.dumps(text)) for text in texts) > 190_000)
            self.assertIn(code, ("slack_resource_limit", "slack_output_too_large"))

    def accepted(self, workspace, result):
        plan, trace = workspace.plan, workspace.exchanges
        records, snapshot = result.records, result.snapshot
        self.assertLessEqual(sum(len(exchange["body"]) for exchange in trace), MIB)
        self.assertLess(workspace.clock.tick - Clock().tick, 20)
        pages, retried, credential = [], {"rate": [], "token": []}, []
        for exchange in trace:
            document, good = exchange["document"], exchange["sound"] and not compressed(exchange)
            if exchange["kind"] != "page":
                # A refresh and its identity check: both answered in full, for the pinned workspace and user.
                self.assertTrue(good and exchange["status"] == 200 and document.get("ok") is True)
                self.assertLessEqual(len(exchange["body"]), 65536)
                credential.append(exchange["kind"])
                if exchange["kind"] == "identity":
                    self.assertEqual((document.get("team_id"), document.get("user_id")), (TEAM, USER))
            elif good and exchange["status"] == 200 and isinstance(document, dict) and document.get("ok") is True:
                pages.append(exchange)
            elif exchange["status"] == 429:
                retried["rate"].append(header(exchange, b"retry-after"))
            else:
                self.assertTrue(good and exchange["status"] in (200, 401) and plan["rotating"]
                                and (document.get("ok"), document.get("error")) == (False, "token_expired"),
                                "A read went on after an answer that the docs do not retry.")
                retried["token"].append(exchange)
        # One retry after a 429 that asks for at most five seconds, and one after token_expired.
        self.assertTrue(all(re.fullmatch(r"[0-9]{1,4}", delay) and int(delay) <= 5 for delay in retried["rate"]))
        self.assertEqual(workspace.naps, [int(delay) for delay in retried["rate"]])
        self.assertLessEqual(len(retried["token"]), 1)
        self.assertEqual(credential, ["refresh", "identity"] * len(retried["token"]))
        with documented_shape():
            rows = [row for page in pages for row in page["document"]["messages"]]
            self.assertLessEqual(len(rows), 100)
            self.assertEqual(sum(row["ts"] == PARENT for row in pages[0]["document"]["messages"]), 1)
            unique = {}
            for row in rows:
                # Rows with one timestamp must agree, or the thread changed while it was read.
                self.assertEqual(canonical(unique.setdefault(row["ts"], observed(row))), canonical(observed(row)))
            expected = sorted(unique.values(), key=lambda row: micros(row["ts"]))
            self.assertLessEqual(sum(len(row["body"].encode()) for row in expected), 65536)
            returned = [{**record["slack_message"], "body": record["body"], "author": record["author"]}
                        for record in records]
            for row in returned:
                self.assertEqual((row.pop("schema_version"), row.pop("snapshot_sha256")),
                                 (1, snapshot["snapshot_sha256"]))
                self.assertEqual(row.pop("thread_ts"), PARENT)
            self.assertEqual(canonical(returned), canonical(expected))
            self.assertTrue(all((record["account_id"], record["channel"]) == (ACCOUNT, CHANNEL) for record in records))
            # Each page is asked for with the cursor the page before gave, and the last gives none.
            cursors = [(page["document"].get("response_metadata") or {}).get("next_cursor") or None for page in pages]
            self.assertEqual([page["cursor"] for page in pages], [None] + cursors[:-1])
            self.assertIsNone(cursors[-1])
            self.assertEqual(len(set(cursors)), len(cursors))
            self.assertTrue(all(len(cursor.encode()) <= 2048 for cursor in cursors[:-1]))
        counts = {"http_attempts": len(trace), "auth_attempts": len(credential),
                  "message_attempts": len(trace) - len(credential), "pages": len(pages), "raw_rows": len(rows),
                  "response_bytes": sum(len(exchange["body"]) for exchange in trace), "messages": len(expected),
                  "duplicates": len(rows) - len(expected), "refresh_attempts": len(retried["token"]),
                  "token_retries": len(retried["token"]), "rate_retries": len(retried["rate"]),
                  **{"omitted_" + key: sum(row["omissions"][key] for row in expected)
                     for key in ("files", "attachments", "blocks")}}
        self.assertEqual(snapshot["counts"], counts)
        self.assertEqual((snapshot["mapping"], snapshot["cutoff_ts"], snapshot["pagination_complete"]),
                         (self.mapping, CUTOFF, True))
        saved = json.dumps({"records": records, "thread_snapshot": snapshot}, separators=(",", ":"))
        self.assertLessEqual(len(saved), 262_144)
        for secret in (OMITTED, *SLACK_SECRETS):
            self.assertNotIn(secret, saved + repr(result))
        self.assertEqual(self.validate(records, snapshot), snapshot)

    def validate(self, records, snapshot, account_id=ACCOUNT, crm_binding=CRM, slack_binding=SLACK):
        return slack_reader.validate_slack_snapshot(records, snapshot, account_id=account_id,
                                                    crm_binding=crm_binding, slack_binding=slack_binding)

    @fuzz(1500)
    @given(slack_plans() | slack_plans("one"))
    def test_any_answer_gives_literal_messages_or_a_fixed_refusal(self, plan):
        self.read(plan)

    def test_every_status_error_delay_and_listed_field_value_alone(self):
        thread = {"rotating": True, "count": 2, "page": 2, "echo": True, "cursors": "end", "specs": [],
                  "parent": {"type": "message", "text": MARK, "user": "U12345678"},
                  "templates": [{"type": "message", "text": MARK + " reply", "bot_id": "B12345678",
                                 "files": [{"text": OMITTED}]}]}
        renewed = {**thread, "specs": [{"edits": [("whole", {"ok": False, "error": "token_expired"})]}]}
        # The two pages of the thread, then the refresh and the identity check that follow token_expired.
        for plan, places in ((thread, (0, 1)), (renewed, (1, 2))):
            valid = self.read(plan)
            self.assertEqual(len(valid.result.records), 3)
            self.refuses_changed_evidence(valid.result, range(10))
            for place in places:
                alone = [{"edits": [edit]}
                         for edit in one_at_a_time(valid.exchanges[place]["document"], *SLACK_TABLES, True)]
                alone += [{"status": status, "edits": [("whole", {"ok": False, "error": error, "detail": MARK})]}
                          for status in (200, 401, 403) for error in SLACK_ERRORS]
                alone += [{"status": 429, "headers": [header]} for header in HEADERS if header[0] == b"retry-after"]
                alone += [{"status": status} for status in (range(100, 600) if place == 0 else sorted(set(STATUSES)))]
                for spec in alone:
                    with self.subTest(place=place, spec=spec):
                        self.read({**plan, "specs": plan["specs"] + [{}] * (place - len(plan["specs"])) + [spec]})

    def refuses_changed_evidence(self, result, choices):
        """Change each saved field alone, and each binding the evidence is checked against. None may pass."""
        evidence = {"records": result.records, "snapshot": result.snapshot, "account_id": ACCOUNT,
                    "crm_binding": CRM, "slack_binding": SLACK}
        before = canonical(evidence)
        for (*parents, last), choice in ((position, choice) for position in positions(evidence) for choice in choices):
            changed = deepcopy(evidence)
            holder = changed
            for key in parents:
                holder = holder[key]
            if choice == 9 and parents:
                del holder[last]
            else:
                holder[last] = replaced(holder[last], choice)
            if canonical(changed) != before:
                with self.assertRaises(slack_reader.SlackReadError, msg=(parents, last, choice)) as refused:
                    self.validate(**changed)
                self.assertEqual(fixed(self, refused.exception, slack_reader.SlackReadError, SLACK_CODES,
                                       SLACK_SECRETS), "slack_snapshot_invalid")

    @fuzz(100, 0.1)
    @given(slack_plans("valid"), st.integers(0, 9))
    def test_snapshot_validation_refuses_every_changed_field(self, plan, choice):
        result = self.read(plan).result
        assume(result is not None)
        self.refuses_changed_evidence(result, [choice])

    @fuzz(150, 0.2)
    @given(st.sampled_from(("check", "check", "connect", "reconnect")), st.booleans(), st.booleans(),
           st.lists(st.just({}) | slack_mutations, max_size=2))
    def test_identity_and_exchange_checks_save_only_what_slack_confirmed(self, mode, rotating, expiring, drawn):
        workspace = Workspace({"specs": drawn})
        clock, provider = workspace.clock, self.provider(workspace)
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary) / "connection"
            saved = directory / "credentials.json"
            if mode != "connect":
                seed(directory, saved_document(clock.now(), rotating=rotating, expired=expiring))
            before = saved.read_bytes() if saved.exists() else None

            def consent(redirect_uri, *, authorization_url_for_state, finish, open_browser, timeout_seconds):
                authorization_url_for_state("invented-state")
                return finish("invented-code", deadline=clock.monotonic() + timeout_seconds)

            try:
                if mode == "check":
                    status = slack_auth.slack_status(directory, check=True, _provider=provider, _clock=clock)
                else:
                    with patch("gtm_connector.slack_callback.receive_consent", side_effect=consent):
                        status = slack_auth.connect_slack(directory, client_id=CLIENT, redirect_uri=REDIRECT,
                                                          _provider=provider, _clock=clock)
            except slack_auth.SlackAuthError as error:
                fixed(self, error, slack_auth.SlackAuthError, SLACK_CODES, SLACK_SECRETS)
                if mode != "check":
                    # A failed sign-in leaves the folder as it was.
                    self.assertEqual(saved.read_bytes() if saved.exists() else None, before)
            else:
                self.assertEqual(set(status), {
                    "schema_version", "configured", "credential_state", "verification", "verified_component",
                    "requested_scopes", "granted_scopes", "rotation", "access_expires_at", "last_verified_at",
                    "needs_login"})
                self.assertEqual((status["credential_state"], status["verification"], status["needs_login"]),
                                 ("ready", "live", False))
                for secret in (MARK, TEAM, USER, APP, CLIENT, *workspace.tokens, *SLACK_SECRETS):
                    self.assertNotIn(secret, json.dumps(status))
                for exchange in workspace.exchanges:
                    self.assertTrue(exchange["sound"] and exchange["status"] == 200 and not compressed(exchange)
                                    and exchange["document"].get("ok") is True and len(exchange["body"]) <= 65536)
                confirmed = workspace.exchanges[-1]["document"]
                self.assertEqual((workspace.exchanges[-1]["kind"], confirmed.get("team_id"), confirmed.get("user_id")),
                                 ("identity", TEAM, USER))
                if mode != "check":
                    # The saved grant is the one Slack answered with: a user token with exactly the asked scope.
                    granted, stored = workspace.exchanges[0]["document"], json.loads(saved.read_bytes())
                    user = granted["authed_user"]
                    self.assertEqual((user["token_type"], user["scope"], stored["access_token"]),
                                     ("user", "channels:history", user["access_token"]))
                    self.assertEqual(stored["identity"], {
                        "app_id": granted["app_id"], "team_id": granted["team"]["id"], "user_id": user["id"],
                        "enterprise_id": (granted.get("enterprise") or {}).get("id")})
            self.assertEqual(workspace.violations, [])
            self.assertLessEqual(len(workspace.exchanges), 2)
            if saved.exists():
                # Whatever Slack answered, the saved file stays readable and holds a token Slack supplied.
                selected = slack_auth.inspect_slack_connection(directory, _clock=clock)
                self.assertIn(json.loads(saved.read_bytes())["access_token"], workspace.tokens)
                if mode != "connect":
                    self.assertEqual(selected.identity, IDENTITY)


if __name__ == "__main__":
    unittest.main()
