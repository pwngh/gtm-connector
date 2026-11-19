"""Fuzzed /mcp requests are held to the gateway's response, receipt, scope, account, and retry rules.

Requests are raw ASGI scopes run on the TestClient's event loop, shaped as an HTTP server delivers them
(lowercase header names, stripped values, a decoded path), so duplicate or missing headers and body
chunks reach the gateway exactly as generated. GTM_FUZZ_EXAMPLES sets the single-request example count,
scales the sequence count with it, and drops the fixed seed.
"""

from collections import Counter
from contextlib import closing, ExitStack
import json
import logging
import os
from pathlib import Path
import sqlite3
import string
from tempfile import TemporaryDirectory
from typing import NamedTuple
import unittest
from unittest.mock import patch

import anyio
from hypothesis import given, HealthCheck, settings, strategies as st
from mcp.server.auth.provider import AccessToken
from starlette.testclient import TestClient

from gtm_connector import calls, slack, store, strict_json
from gtm_connector.gateway import Connector


RESOURCE = "https://127.0.0.1:8765/mcp"
HOST = b"127.0.0.1:8765"
VERSION = "2026-07-28"
META = "io.modelcontextprotocol/"
LIMIT = 4
EXAMPLES = int(os.environ.get("GTM_FUZZ_EXAMPLES", "0"))
READS = {"crm": "crm:read", "calls": "calls:read", "slack": "slack:read"}
TOOL_SCOPES = {"search_accounts": "crm:read", "get_account": "crm:read", "list_opportunities": "crm:read",
               "read_call_notes": "calls:read", "read_slack_messages": "slack:read", "log_activity": "crm:write"}
TOOL_ERRORS = ("account not found", "idempotency_conflict: key was used with different arguments",
               "account_number_requires_salesforce")
EVERY = tuple(sorted(set(TOOL_SCOPES.values())))
FAR = 2147483647
# Token text -> scopes, expiry, resource. Only the first six are live for this resource.
TOKENS = {
    "fuzz-full": (EVERY, FAR, RESOURCE), "fuzz-crm": (("crm:read",), FAR, RESOURCE),
    "fuzz-calls": (("calls:read",), FAR, RESOURCE), "fuzz-slack": (("slack:read",), FAR, RESOURCE),
    "fuzz-write": (("crm:write",), FAR, RESOURCE), "fuzz-none": ((), FAR, RESOURCE),
    "fuzz-expired": (EVERY, 1, RESOURCE), "fuzz-unbounded": (EVERY, None, RESOURCE),
    "fuzz-elsewhere": (EVERY, FAR, "https://127.0.0.1:8765/other"),
}
LIVE = {token: scopes for token, (scopes, expiry, resource) in TOKENS.items() if (expiry, resource) == (FAR, RESOURCE)}
ASCII_LOWER = str.maketrans(string.ascii_uppercase, string.ascii_lowercase)


class Authority:
    issuer = "https://127.0.0.1:8766"
    resource = RESOURCE

    async def verify_token(self, token):
        if token not in TOKENS:
            return None
        scopes, expiry, resource = TOKENS[token]
        return AccessToken(token=token, client_id="fuzz", scopes=list(scopes), expires_at=expiry, resource=resource)


class Sent(NamedTuple):
    method: str
    path: str
    scheme: str
    headers: tuple
    chunks: tuple


class Asked:
    """What a request asks for and may see, worked out apart from the gateway."""

    def __init__(self, sent):
        self.headers = [(name, value.decode("latin-1")) for name, value in sent.headers]
        read = b""
        for chunk in sent.chunks:  # The gateway stops reading once the body passes its size cap.
            read += chunk
            if len(read) > 65536:
                break
        try:
            document = json.loads(read)
        except (ValueError, RecursionError):
            document = None
        self.document = document if isinstance(document, dict) else {}
        # Text the request itself carries may come back; it is the caller's own. The credential is not.
        self.echo = json.dumps(self.document, ensure_ascii=False) + "\n".join(
            value for name, value in self.headers if name != b"authorization")
        params = self.document.get("params")
        self.params = params if isinstance(params, dict) else {}
        arguments = self.params.get("arguments")
        self.arguments = arguments if isinstance(arguments, dict) else {}
        self.tool = self.params.get("name")
        self.at_gateway = (sent.method, sent.path) == ("POST", "/mcp")
        # The gateway also takes the Mcp-Method header as naming a call, whatever the body says.
        marks = (self.document.get("method"), (self.header(b"mcp-method") or [None])[-1])
        self.call = self.at_gateway and "tools/call" in marks
        authorization = self.header(b"authorization")
        scheme, _, token = authorization[0].partition(" ") if len(authorization) == 1 else ("", "", "")
        self.token = token if scheme.lower() == "bearer" and token else None
        self.scopes = LIVE.get(self.token, ())

    def header(self, name):
        return [value for key, value in self.headers if key == name]

    def may_see(self, account, names):
        """Whether the arguments name this account: by ID, by search text, or by listing every opportunity."""
        named = self.arguments.get("account_id")
        if isinstance(named, str):
            return account == named
        if self.tool == "search_accounts":
            query = self.arguments.get("query", "")
            return isinstance(query, str) and query.translate(ASCII_LOWER) in names[account].translate(ASCII_LOWER)
        return self.tool == "list_opportunities"


async def _deliver(app, sent):
    """Run one request to its end. The client hangs up only after the response is complete."""
    pending, messages, done = list(sent.chunks), [], anyio.Event()

    async def receive():
        if pending:
            return {"type": "http.request", "body": pending.pop(0), "more_body": bool(pending)}
        await done.wait()
        return {"type": "http.disconnect"}

    async def send(message):
        messages.append(message)
        if message["type"] == "http.response.body" and not message.get("more_body"):
            done.set()

    scope = {"type": "http", "http_version": "1.1", "method": sent.method, "path": sent.path,
             "raw_path": sent.path.encode(), "root_path": "", "scheme": sent.scheme, "query_string": b"",
             "headers": list(sent.headers), "client": ("127.0.0.1", 50000), "server": ("127.0.0.1", 8765), "state": {}}
    with anyio.fail_after(30):  # A request that never answers fails here instead of hanging the run.
        await app(scope, receive, send)
    return messages


def _fixture_markers(root):
    """Text found in only one account's rows of one source, read from the seeded files."""
    lines = {}
    for source, tables in (("crm", ("accounts", "contacts", "opportunities")), ("calls", ("call_notes",)),
                           ("slack", ("messages",))):
        with closing(sqlite3.connect(root / f"{source}.sqlite3")) as connection:
            connection.row_factory = sqlite3.Row
            for table in tables:
                for row in map(dict, connection.execute(f"SELECT * FROM {table}")):
                    owner = row.pop("id" if table == "accounts" else "account_id")
                    # Row IDs count at any length; other text must be long enough not to occur by chance.
                    lines.setdefault((source, owner), []).extend(
                        line for column, value in row.items() if isinstance(value, str)
                        for line in value.splitlines() if column == "id" or len(line) >= 12)
    markers = {}
    for key, own in lines.items():
        other = "\n".join(line for owner, group in lines.items() if owner != key for line in group)
        markers[key] = sorted({line for line in own if line not in other})
    return markers


class Gateway:
    """One seeded connector with read access to its audit log and activities."""

    def __init__(self, rate_limit):
        self.resources = ExitStack()
        root = Path(self.resources.enter_context(TemporaryDirectory()))
        for name, module in (("crm", store), ("calls", calls), ("slack", slack)):
            with closing(module.connect(root / f"{name}.sqlite3")) as connection, connection:
                module.initialize(connection)
                module.seed(connection)
        self.markers = _fixture_markers(root)
        self.now = 0.0
        self.connector = Connector(root / "crm.sqlite3", root / "calls.sqlite3", root / "audit.sqlite3", Authority(),
                                   rate_limit=rate_limit, clock=lambda: self.now, slack_path=root / "slack.sqlite3")
        self.client = self.resources.enter_context(TestClient(
            self.connector, base_url="https://127.0.0.1:8765", raise_server_exceptions=False))
        # Separate connections see only what the gateway committed.
        self.crm = self.resources.enter_context(closing(sqlite3.connect(root / "crm.sqlite3", isolation_level=None)))
        self.audit = self.resources.enter_context(closing(sqlite3.connect(root / "audit.sqlite3")))
        self.audit.row_factory = sqlite3.Row
        self.names = dict(self.crm.execute("SELECT id, name FROM accounts"))
        # The SDK logs a traceback for each request it answers with an internal error.
        logging.disable(logging.CRITICAL)
        self.resources.callback(logging.disable, logging.NOTSET)

    def reset(self):
        """Start an example from the seeded state, so its outcome does not depend on earlier examples."""
        self.crm.execute("DELETE FROM activities")
        self.connector.windows = {}
        self.now = 0.0

    def activities(self):
        rows = self.crm.execute("SELECT idempotency_key, id, account_id, kind, body FROM activities").fetchall()
        stored = {row[0]: dict(zip(("id", "account_id", "kind", "body"), row[1:])) for row in rows}
        assert len(stored) == len(rows), "Two activities share an idempotency key"
        return stored

    def exchange(self, sent):
        """Send one request and hold its response, receipts, and activity writes to every rule."""
        asked = Asked(sent)
        mark = self.audit.execute("SELECT coalesce(max(rowid), 0) FROM audit_log").fetchone()[0]
        before = self.activities()
        messages = self.client.portal.call(_deliver, self.connector, sent)
        receipts = [dict(row) for row in self.audit.execute("SELECT * FROM audit_log WHERE rowid > ?", (mark,))]
        after = self.activities()

        # 1. One complete response with a status the gateway intends. An exception has already failed the call above.
        kinds = [message["type"] for message in messages]
        assert kinds[:1] == ["http.response.start"] and set(kinds[1:]) == {"http.response.body"}, kinds
        status, headers = messages[0]["status"], messages[0]["headers"]
        payload = b"".join(message.get("body", b"") for message in messages[1:])
        lengths = [value for name, value in headers if name.lower() == b"content-length"]
        assert lengths in ([], [str(len(payload)).encode()]), (lengths, len(payload))
        # The SDK's router sends a trailing slash back to /mcp. No fault is injected here, so a 503 reports a
        # failure that did not happen.
        redirect = status == 307 and sent.path == "/mcp/"
        assert status in (200, 202) or 400 <= status < 500 or redirect, (status, payload[:200])

        # 4. A body from the gateway is one JSON object that a strict parser accepts.
        text, body = payload.decode("utf-8", "replace"), {}
        is_json = (b"content-type", b"application/json") in headers
        if payload and (asked.at_gateway or is_json):
            assert is_json, headers
            body = strict_json.loads(payload.decode("utf-8"))
            assert isinstance(body, dict), body
            json.dumps(body, ensure_ascii=False).encode()  # A lone surrogate is not text; other parsers refuse it.
        result, error = body.get("result"), body.get("error")
        assert result is None or error is None, body
        # The SDK turns an unhandled exception into this code under HTTP 200, or into a tool error.
        internal = isinstance(error, dict) and error.get("code") == -32603
        assert not internal, body
        released = asked.call and status == 200 and isinstance(result, dict) and not result.get("isError")
        if isinstance(result, dict) and result.get("isError"):
            known = {f"Unknown tool: {asked.tool}"} | {f"Error executing tool {asked.tool}: {reason}"
                                                       for reason in TOOL_ERRORS}
            assert result["content"][0]["text"] in known, result
        records = result.get("structuredContent", {}).get("records", []) if released else []

        # 2. A tool call leaves exactly one finished receipt that agrees with the response. Nothing else leaves one.
        assert len(receipts) == asked.call, (asked.call, receipts)
        for receipt in receipts:
            assert receipt["status"] in ("ok", "denied", "error"), receipt
            assert receipt["http_status"] == status, receipt
            assert (receipt["status"] == "ok") == released, receipt
            assert json.loads(receipt["rows"]) == [record["record_ref"] for record in records], receipt
            assert result is None or result["_meta"]["gtm.demo/auditId"] == receipt["audit_id"], body
            # The receipt names what ran.
            assert not released or (receipt["tool"], json.loads(receipt["arguments"])) == (asked.tool, asked.arguments)
        # A credential reaches neither the response nor the receipt.
        written = [text, *(value.decode("latin-1") for _, value in headers), *map(str, receipts)]
        assert not any(token in place for token in TOKENS for place in written if token not in asked.echo), written

        # 3. A result is released only with a receipt, under a live token that carries the tool's scope, and record
        # text only under its own source's scope. 4. It holds nothing from an account the arguments did not name.
        if released:
            assert receipts and TOOL_SCOPES[asked.tool] in asked.scopes, (asked.tool, asked.scopes)
        for (source, account), lines in self.markers.items():
            if any(line in text for line in lines if line not in asked.echo):
                assert released, (source, account, status)
                assert READS[source] in asked.scopes, (source, account, asked.scopes)
                assert asked.may_see(account, self.names), (account, asked.arguments)
        for record in records:
            owner = record["id"] if record["record_ref"].startswith("crm/accounts/") else record["account_id"]
            assert asked.may_see(owner, self.names), (record, asked.arguments)

        # 5. Activities are never changed or removed. One appears only for the released log_activity that named it,
        # and a released log_activity returns the one activity stored under its key, with its own arguments.
        assert all(after.get(key) == row for key, row in before.items()), (before, after)
        created = after.keys() - before.keys()
        if created or (released and asked.tool == "log_activity"):
            key = asked.arguments.get("idempotency_key")
            assert released and asked.tool == "log_activity" and created <= {key}, (created, status)
            stored = after.get(key)
            assert [{name: record[name] for name in stored} for record in records] == [stored], (records, stored)
            assert all(stored[name] == asked.arguments[name] for name in ("account_id", "kind", "body")), stored
        return asked, status, released

    def close(self):
        self.resources.close()


# One step from a real account ID, or shaped like SQL or a path.
NEAR = st.sampled_from((
    "acc-001 ", " acc-001", "acc-001\n", "\tacc-001", "ACC-001", "acc-001\x00", "acc-001\x00acc-002", "acc-00_",
    "acc-%", "%", "_______", "*", "acc-0010", "acc-1", "\ufeffacc-001", "acc-001\u200b", "\uff41cc-001",
    "acc-001,acc-002", "acc-001' OR '1'='1", "acc-001'; DROP TABLE accounts;--", 'acc-001" OR 1=1 --',
    "1 UNION SELECT * FROM contacts", "acc-001/../acc-002", "../crm.sqlite3", "../../../../etc/passwd", "/etc/passwd",
    "file:calls.sqlite3?mode=rw",
))
TEXT = NEAR | st.text(max_size=30) | st.sampled_from((
    "", " ", "\x00", "\x1f\x7f\x85", "\u202eacc-001", "a\u0301cc-001", "\U0001f600", "\ud800", "x" * 65, "x" * 5000))
JSON = st.recursive(
    st.none() | st.booleans() | st.integers(-2**70, 2**70) | st.floats() | TEXT,
    lambda inner: st.lists(inner, max_size=3) | st.dictionaries(st.text(max_size=6), inner, max_size=3), max_leaves=6)
# Header bytes a server passes on: visible ASCII and obs-text, with outer whitespace already stripped.
HEADER_BYTES = st.text(st.characters(min_codepoint=0x20, max_codepoint=0xff, exclude_characters="\x7f"),
                       max_size=40).map(lambda value: value.encode("latin-1").strip())
ARGUMENTS = {
    "search_accounts": {"query": ("", "a", "Alder", "ALDER", "vale", "zzz")},
    "log_activity": {"account_id": ("acc-001", "acc-002", "acc-999"), "kind": ("call", "note"),
                     "body": ("first", "second"), "idempotency_key": ("key-1", "key-2", "Key-1", "key-1 ", "k\x00")},
}
ACCOUNT = {"account_id": ("acc-001", "acc-002", "acc-007", "acc-030", "acc-999")}
TOOLS = tuple(TOOL_SCOPES)
OTHER_TOOLS = ("prepare_renewal_brief", "read_renewal_brief", "nope", "", "Get_Account", "get_account ", "n\u00e9")
METHODS = ("tools/list", "server/discover", "resources/list", "prompts/list", "resources/read", "prompts/get",
           "completion/complete", "subscriptions/listen", "ping", "initialize", "notifications/initialized", "nope",
           "tools/call", "TOOLS/CALL", "tools/call ", "")
PATHS = tuple((*path, key) for path, keys in (
    ((), ("jsonrpc", "id", "method", "params", "extra")),
    (("params",), ("name", "arguments", "_meta", "extra", "task", "cursor", "uri")),
    (("params", "arguments"), ("account_id", "query", "account_number", "kind", "body", "idempotency_key", "extra")),
    (("params", "_meta"), (META + "protocolVersion", META + "clientCapabilities", META + "clientInfo",
                           "gtm.demo/briefId", "progressToken")),
) for key in keys)
# Spliced in unquoted where a value stood: what a JSON encoder cannot or will not write.
RAW = (
    b"NaN", b"Infinity", b"-Infinity", b"1e999", b"-1e999", b"1" + b"0" * 400, b"9" * 5000, b"-0", b"1.0", b"01",
    b"+1", b"0x10", b"1e", b"true", b"", b"{", b"]", b"'single'", b'"unterminated', b'"\\ud800"', b'"\\udc00\\ud800"',
    b'"\xff"', b'"\xed\xa0\x80"', b'"\xc0\xaf"', b'"\\u0000"', b'"\\x41"', b'"' + b"x" * 70000 + b'"',
    b'{"account_id":"acc-001","account_id":"acc-002"}', b'{"account_id":"acc-002","account_id":"acc-001"}',
    b'{"a":' * 3000 + b"1" + b"}" * 3000,
    *(b"[" * depth + b"]" * depth for depth in (50, 990, 5000, 32000)),
)
MEMBERS = (b'"method": "tools/list"', b'"method": "tools/call"', b'"id": 2', b'"name": "search_accounts"',
           b'"name": "log_activity"', b'"account_id": "acc-002"', b'"arguments": {}', b'"params": {}')
RECODE = {
    "bom": lambda body: b"\xef\xbb\xbf" + body, "utf-16": lambda body: body.decode("utf-8", "replace").encode("utf-16"),
    "utf-16-le": lambda body: body.decode("utf-8", "replace").encode("utf-16-le"),
    "utf-32": lambda body: body.decode("utf-8", "replace").encode("utf-32"), "nul": lambda body: body + b"\x00",
    "second": lambda body: body + b"\n" + body, "lead": lambda body: b" " * 60000 + body,
    "over": lambda body: b" " * 65536 + body, "tail": lambda body: body + b" " * 65536,
}
CREDENTIALS = ("Bearer {0}", "bearer {0}", "BEARER {0}", "Bearer  {0}", "Bearer\t{0}", "{0}", "Basic {0}", "Token {0}",
               "Bearer {0} {0}", "Bearer {0},Bearer fuzz-full", "Bearer x{0}", "Bearer {0}x", "Bearer {0}\xa0",
               "Bearer {0}\xff", "Bearer {0:.8}", "Bearer {0}/", "Bearer", "Bearer null", "Bearer " + "x" * 5000, "")
HEADERS = {
    b"authorization": (b"Negotiate fuzz-full", b"Bearer \xff\xfe"),
    b"host": (HOST, b"127.0.0.1", b"localhost:8765", b"127.0.0.1:8766", b"evil.example", HOST + b".evil.example",
              b"[::1]:8765", b"", b"127.0.0.1:08765", b"user@" + HOST, HOST + b", evil.example"),
    b"origin": (b"https://" + HOST, b"null", b"https://evil.example", b""),
    b"content-type": (b"application/json", b"application/json; charset=utf-8", b"Application/JSON", b"text/plain", b"",
                      b"application/jsonx", b"application/x-www-form-urlencoded", b"multipart/form-data; boundary=x"),
    b"accept": (b"application/json, text/event-stream", b"application/json", b"text/event-stream", b"*/*",
                b"application/*", b"text/*", b"", b"text/html", b"application/json;q=0"),
    b"mcp-protocol-version": (VERSION.encode(), b"2025-06-18", b"2025-03-26", b"2024-11-05", b"2026-07-29", b"",
                              b"latest", VERSION.encode() + b"," + VERSION.encode()),
    b"mcp-method": tuple(method.encode() for method in METHODS),
    b"mcp-name": tuple(tool.encode() for tool in TOOLS + OTHER_TOOLS) + (b"=?base64?Z2V0X2FjY291bnQ=?=",),
    b"mcp-session-id": (b"1",), b"last-event-id": (b"1",), b"mcp-param-account_id": (b"acc-001",),
    b"x-forwarded-host": (HOST, b"evil.example"), b"x-forwarded-proto": (b"https", b"http"),
    b"x-http-method-override": (b"GET",), b"content-length": (b"0", b"1"), b"cookie": (b"token=fuzz-full",),
}
VERBS = ("GET", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE", "CONNECT", "QUERY", "post", "Post")
ROUTES = ("/mcp/", "/MCP", "//mcp", "/mcp/x", "/", "*", "/mcp;x", "/mcp?x", "/mcp#x", "/mcp\x00", "/mcp ", "/./mcp",
          "/x/../mcp", "/mcp/..", "mcp", "https://127.0.0.1:8765/mcp", "/.well-known/oauth-protected-resource",
          "/.well-known/oauth-protected-resource/mcp", "/.well-known/oauth-protected-resource/",
          "/.well-known/oauth-authorization-server", "/authorize", "/token", "/register", "/sse", "/messages")


class Draft:
    """A valid request under change: a JSON message until it is frozen into body bytes."""

    def __init__(self, method, params, token="fuzz-full", name=None):
        self.verb, self.path, self.scheme = "POST", "/mcp", "https"
        self.raw = self.body = None
        self.ascii = True
        self.message = {"jsonrpc": "2.0", "id": 1, "method": method, "params": {**params, "_meta": {
            META + "protocolVersion": VERSION, META + "clientCapabilities": {},
            META + "clientInfo": {"name": "fuzz", "version": "1"}}}}
        self.headers = [(b"host", HOST), (b"authorization", b"Bearer " + token.encode()),
                        (b"content-type", b"application/json"), (b"accept", b"application/json, text/event-stream"),
                        (b"mcp-protocol-version", VERSION.encode()), (b"mcp-method", method.encode())]
        if name is not None:
            self.headers.append((b"mcp-name", name.encode()))

    def holder(self, path):
        """The object that holds the last key of a path, or None once another change has removed it."""
        node = self.message
        for key in path[:-1]:
            node = node.get(key) if isinstance(node, dict) else None
        return node if isinstance(node, dict) else None

    def freeze(self):
        if self.body is None:
            encoded = json.dumps(self.message, ensure_ascii=self.ascii).encode("utf-8", "surrogatepass")
            self.body = encoded if self.raw is None else encoded.replace(b'"@raw@"', self.raw)
        return self.body

    def sent(self, cuts=()):
        body = self.freeze()
        chunks = tuple(body[start:end] for start, end in zip((0, *cuts), (*cuts, len(body))))
        return Sent(self.verb, self.path, self.scheme, tuple(self.headers), chunks)


def call(tool, arguments, token="fuzz-full"):
    return Draft("tools/call", {"name": tool, "arguments": arguments}, token, tool)


def _set_value(draw, draft):
    path = draw(st.sampled_from(PATHS))
    holder = draft.holder(path)
    if holder is not None:
        holder[path[-1]] = draw(JSON)


def _set_argument(draw, draft):
    arguments = draft.holder(("params", "arguments", ""))
    if arguments:
        arguments[draw(st.sampled_from(sorted(arguments)))] = draw(TEXT)


def _drop_value(draw, draft):
    path = draw(st.sampled_from(PATHS))
    (draft.holder(path) or {}).pop(path[-1], None)


def _raw_value(draw, draft):
    path = draw(st.sampled_from(PATHS))
    holder = draft.holder(path)
    if holder is not None:
        holder[path[-1]], draft.raw = "@raw@", draw(st.sampled_from(RAW))


def _rename(draw, draft):
    """Change the tool or the method in the body, in its header, or in both."""
    key, header, names = draw(st.sampled_from(((("params", "name"), b"mcp-name", TOOLS + OTHER_TOOLS),
                                               (("method",), b"mcp-method", METHODS))))
    name, where = draw(st.sampled_from(names)), draw(st.sampled_from(("body", "header", "both")))
    holder = draft.holder(key)
    if where != "header" and holder is not None:
        holder[key[-1]] = name
    if where != "body":
        draft.headers = [pair for pair in draft.headers if pair[0] != header] + [(header, name.encode())]


def _batch(draw, draft):
    draft.message = draw(st.sampled_from(([draft.message], [draft.message] * 2, [], {"batch": draft.message})))


def _plain_text(draw, draft):
    draft.ascii = False


def _truncate(draw, draft):
    body = draft.freeze()
    draft.body = body[:draw(st.integers(0, max(0, len(body) - 1)))]


def _splice(draw, draft):
    body = draft.freeze()
    start = draw(st.integers(0, len(body)))
    draft.body = body[:start] + draw(st.binary(max_size=4)) + body[start + draw(st.integers(0, 4)):]


def _duplicate(draw, draft):
    """Write a member twice, or once more in another object, before or after the one already there."""
    body, member = draft.freeze(), draw(st.sampled_from(MEMBERS))
    opening = draw(st.sampled_from((b"{", b'"params": {', b'"arguments": {')))
    if draw(st.booleans()):
        draft.body = body.replace(opening, opening + member + b", ", 1)
    elif body.endswith(b"}"):
        draft.body = body[:-1] + b", " + member + b"}"


def _recode(draw, draft):
    draft.body = RECODE[draw(st.sampled_from(sorted(RECODE)))](draft.freeze())


def _header(draw, draft):
    """Replace a header, or add a second one before or after it."""
    name = draw(st.sampled_from(sorted(HEADERS)))
    value = draw(st.sampled_from(HEADERS[name]) | HEADER_BYTES)
    if draw(st.booleans()):
        draft.headers = [pair for pair in draft.headers if pair[0] != name]
    draft.headers.insert(draw(st.sampled_from((0, len(draft.headers)))), (name, value))


def _authorization(draw, draft):
    """Break the credential in one way, or send a second one beside it."""
    token = draw(st.sampled_from(("fuzz-full",) * 4 + tuple(TOKENS)))
    value = draw(st.sampled_from(CREDENTIALS)).format(token).encode("latin-1")
    if draw(st.sampled_from((True, True, False))):
        draft.headers = [pair for pair in draft.headers if pair[0] != b"authorization"]
    draft.headers.insert(draw(st.sampled_from((0, len(draft.headers)))), (b"authorization", value))


def _drop_header(draw, draft):
    name = draw(st.sampled_from(sorted({pair[0] for pair in draft.headers})))
    draft.headers = [pair for pair in draft.headers if pair[0] != name]


def _route(draw, draft):
    part = draw(st.sampled_from(("verb", "path", "scheme")))
    if part == "verb":
        draft.verb = draw(st.sampled_from(VERBS))
    elif part == "path":
        draft.path = draw(st.sampled_from(ROUTES) | st.text(max_size=12).map("/".__add__))
    else:
        draft.scheme = "http"


# In order of application: the message, then its bytes, then headers and route. Repeats weigh the choice.
CHANGES = (*(_set_value,) * 3, *(_set_argument,) * 4, _drop_value, _raw_value, _rename, _batch, _plain_text,
           _truncate, _splice, _duplicate, _recode, *(_header,) * 3, *(_authorization,) * 2, _drop_header, _route)


@st.composite
def requests(draw):
    """Mostly a valid request with one thing changed; random bytes alone stop at the first check."""
    token = draw(st.sampled_from(("fuzz-full",) * 20 + tuple(TOKENS)))
    kind = draw(st.sampled_from(("call",) * 6 + ("arguments",) * 3 + ("other", "other", "bytes")))
    if kind == "arguments":  # The argument values are the one change.
        tool = draw(st.sampled_from(TOOLS))
        draft = call(tool, {name: draw(st.sampled_from(values) | NEAR | TEXT)
                            for name, values in ARGUMENTS.get(tool, ACCOUNT).items()}, token)
    elif kind == "call":
        tool = draw(st.sampled_from(TOOLS * 3 + OTHER_TOOLS))
        draft = call(tool, {name: draw(st.sampled_from(values))
                            for name, values in ARGUMENTS.get(tool, ACCOUNT).items()}, token)
    else:
        method = draw(st.sampled_from(METHODS))
        named = method in ("prompts/get", "resources/read")
        draft = Draft(method, {"name": "x", "uri": "x"}, token, "x" if named else None)
    if kind == "bytes":
        draft.body = draw(st.binary(max_size=64) | JSON.map(lambda value: json.dumps(value).encode()))
    count = 0 if kind == "arguments" else draw(st.sampled_from((0, 1, 1, 1, 1, 2, 3)))
    for index in sorted(draw(st.lists(st.integers(0, len(CHANGES) - 1), min_size=count, max_size=count))):
        CHANGES[index](draw, draft)
    cuts = draw(st.lists(st.integers(0, len(draft.freeze())), max_size=draw(st.sampled_from((0, 0, 0, 0, 1, 3)))))
    return draft.sent(sorted(cuts))


@st.composite
def retries(draw):
    """An unchanged log_activity call drawn from few enough values that keys and arguments collide."""
    token = draw(st.sampled_from(("fuzz-full",) * 6 + ("fuzz-write", "fuzz-crm", "fuzz-expired")))
    return call("log_activity", {name: draw(st.sampled_from(values))
                                 for name, values in ARGUMENTS["log_activity"].items()}, token).sent()


def _settings(examples):
    # Bounded and repeatable as checked in. No deadline or health check, so a slow machine cannot fail the run.
    return settings(max_examples=EXAMPLES * examples // 5000 or examples, derandomize=not EXAMPLES, database=None,
                    deadline=None, suppress_health_check=list(HealthCheck), print_blob=True)


class GatewayCase(unittest.TestCase):
    rate_limit = 10**9

    def setUp(self):
        self.gateway = Gateway(self.rate_limit)
        self.addCleanup(self.gateway.close)


class TestFuzzGateway(GatewayCase):
    @_settings(5000)
    @given(sent=requests())
    def test_any_request_keeps_every_rule(self, sent):
        self.gateway.reset()
        self.gateway.exchange(sent)


class TestFuzzRetries(GatewayCase):
    rate_limit = LIMIT

    @_settings(300)
    @given(steps=st.lists(st.one_of(*(st.tuples(st.just("retry"), retries()),) * 2,
                                    st.tuples(st.just("any"), requests()), st.just(("wait", None))),
                          min_size=2, max_size=12))
    def test_retries_never_duplicate_an_activity(self, steps):
        gateway = self.gateway
        gateway.reset()
        charged = Counter()
        for kind, sent in steps:
            if kind == "wait":  # Past the window, every credential starts again.
                gateway.now += 61
                charged.clear()
                continue
            before = gateway.activities()
            asked, status, released = gateway.exchange(sent)
            # Only a credential's own earlier requests can fill its window.
            assert status != 429 or (asked.token in LIVE and charged[asked.token] >= LIMIT), (asked.token, charged)
            charged[asked.token] += 1
            if kind == "retry" and status != 429:
                arguments = dict(asked.arguments)
                stored = before.get(arguments.pop("idempotency_key"))
                # A first use or an identical retry succeeds; a key reused with other arguments is refused.
                fresh = stored is None or {**stored, **arguments} == stored
                expected = "crm:write" in asked.scopes and arguments["account_id"] in gateway.names and fresh
                assert released == expected, (asked.arguments, stored, status)


class TestFoundByFuzzing(GatewayCase):
    """The smallest request for each defect the fuzz found, kept so it stays fixed."""

    def test_call_with_lone_surrogate_keeps_its_receipt(self):
        body = b'{"method":"tools/call","id":"\\ud800"}'
        self.gateway.exchange(Sent("POST", "/mcp", "https", (), (body,)))

    def test_lone_surrogate_id_is_refused_as_a_client_error(self):
        self.gateway.exchange(Sent("POST", "/mcp", "https", (), (b'{"id":"\\ud800"}',)))

    def test_missing_content_type_is_a_client_error(self):
        draft = call("get_account", {"account_id": "acc-001"})
        draft.headers.remove((b"content-type", b"application/json"))
        self.gateway.exchange(draft.sent())

    def test_call_without_an_id_is_not_recorded_as_ok(self):
        draft = call("log_activity", {"account_id": "acc-001", "kind": "call", "body": "first", "idempotency_key": "k"})
        del draft.message["id"]
        self.gateway.exchange(draft.sent())

    def test_unknown_prompt_is_not_an_internal_error(self):
        self.gateway.exchange(Draft("prompts/get", {"name": "x"}, name="x").sent())

    def test_a_get_is_refused_instead_of_opening_a_stream_that_never_ends(self):
        headers = ((b"host", b"127.0.0.1:8765"), (b"authorization", b"Bearer fuzz-full"),
                   (b"accept", b"application/json, text/event-stream"))
        status, body, receipt = self.answer(Sent("GET", "/mcp", "https", headers, (b"",)))
        self.assertEqual((status, body["error"]["code"], receipt), (405, -32600, None))

    def answer(self, sent):
        """Return (status, JSON body or None, the last receipt or None) for one request."""
        mark = self.gateway.audit.execute("SELECT coalesce(max(rowid), 0) FROM audit_log").fetchone()[0]
        messages = self.gateway.client.portal.call(_deliver, self.gateway.connector, sent)
        payload = b"".join(message.get("body", b"") for message in messages[1:])
        try:
            body = json.loads(payload)
        except ValueError:
            body = None
        receipts = [dict(row) for row in self.gateway.audit.execute("SELECT * FROM audit_log WHERE rowid > ?", (mark,))]
        self.assertLessEqual(len(receipts), 1)
        return messages[0]["status"], body, receipts[0] if receipts else None

    def test_each_new_refusal_has_its_status_code_and_reason_and_one_finished_receipt(self):
        no_type = call("get_account", {"account_id": "acc-001"})
        no_type.headers.remove((b"content-type", b"application/json"))
        no_id = call("get_account", {"account_id": "acc-001"})
        del no_id.message["id"]
        odd_id = call("get_account", {"account_id": "acc-001"})
        odd_id.message["id"] = {"private": "echo"}
        for draft, refusal in ((no_type, (415, -32600, "unsupported_media_type")),
                               (no_id, (400, -32600, "call_id_required")),
                               (odd_id, (400, -32600, "call_id_required"))):
            status, body, receipt = self.answer(draft.sent())
            self.assertEqual((status, body["error"]["code"], body["error"]["data"]["reason"]), refusal)
            # An id that is not a string or integer is never echoed back.
            self.assertEqual(body["id"], 1 if draft is no_type else None)
            self.assertEqual((receipt["status"], receipt["http_status"]), ("error", status))
        status, body, receipt = self.answer(Draft("prompts/get", {"name": "x"}, name="x").sent())
        self.assertEqual((status, body["error"]["code"], body["error"]["data"]["reason"], receipt),
                         (404, -32601, "method_not_found", None))

    def test_only_a_tool_result_is_recorded_as_ok(self):
        def sdk(body):
            async def app(scope, receive, send):
                await send({"type": "http.response.start", "status": 200, "headers": [
                    (b"content-type", b"application/json"), (b"content-length", str(len(body)).encode())]})
                await send({"type": "http.response.body", "body": body})
            return patch.object(self.gateway.connector, "sdk_app", app)

        sent = call("get_account", {"account_id": "acc-001"}).sent()
        for body in (b"", b"[]", b"5", b"not json", b'{"result": 5}', b'{"result": null}', b'{"result": []}',
                     b'{"result": {"isError": true}}', b'{"result": {}, "error": {}}'):
            with self.subTest(body=body), sdk(body):
                status, _, receipt = self.answer(sent)
                self.assertEqual((status, receipt["status"], json.loads(receipt["rows"])), (200, "error", []))
        # A result whose own metadata is malformed is still answered, with the receipt it names.
        records = b'{"records": [{"record_ref": "crm/accounts/acc-001"}]}'
        with sdk(b'{"result": {"_meta": 5, "structuredContent": ' + records + b"}}"):
            status, body, receipt = self.answer(sent)
        self.assertEqual((status, receipt["status"], json.loads(receipt["rows"])),
                         (200, "ok", ["crm/accounts/acc-001"]))
        self.assertEqual(body["result"]["_meta"], {"gtm.demo/auditId": receipt["audit_id"]})


if __name__ == "__main__":
    unittest.main()
