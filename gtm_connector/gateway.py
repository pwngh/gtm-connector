"""One scoped MCP gateway over independent CRM, calls, and Slack fixtures."""

from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import math
from pathlib import Path
import sqlite3
import time
from urllib.parse import urlsplit
import uuid
from typing import Any

from mcp.server import MCPServer
from mcp.server.mcpserver.exceptions import ToolError
from mcp.server.auth.middleware.auth_context import get_access_token
from mcp.server.auth.settings import AuthSettings
from mcp.server.transport_security import TransportSecuritySettings
from mcp.types import ToolAnnotations
from pydantic import AnyHttpUrl
from starlette.responses import JSONResponse

from . import calls, slack, store
from .audit import AuditLog
from .salesforce import SalesforceReadError

SCOPES = {
    "search_accounts": "crm:read", "get_account": "crm:read",
    "list_opportunities": "crm:read", "read_call_notes": "calls:read",
    "read_slack_messages": "slack:read",
    "log_activity": "crm:write",
}
ARGUMENTS = {
    "search_accounts": ({}, {"query": (str, 200)}),
    "get_account": ({"account_id": (str, 64)}, {}),
    "list_opportunities": ({}, {"account_id": (str, 64)}),
    "read_call_notes": ({"account_id": (str, 64)}, {}),
    "read_slack_messages": ({"account_id": (str, 64)}, {}),
    "log_activity": ({"account_id": (str, 64), "kind": (str, 40),
                      "body": (str, 2000), "idempotency_key": (str, 128)}, {}),
}


def _json_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _records(source, table, rows):
    return [{**row, "record_ref": f"{source}/{table}/{row['id']}"} for row in rows]


def _data(source, rows):
    return {"source": source, "records": rows, "untrusted_data": True}


def require_distinct_databases(paths):
    """Reject path aliases and existing hardlinks before initializing a store."""
    resolved = [Path(path).resolve() for path in paths]
    identities = []
    for path in resolved:
        try:
            info = path.stat()
        except FileNotFoundError:
            continue
        identities.append((info.st_dev, info.st_ino))
    if len(set(resolved)) != len(resolved) or len(set(identities)) != len(identities):
        raise ValueError("CRM, calls, Slack, and audit require different database files")
    return resolved


class Connector:
    def __init__(self, crm_path, calls_path, audit_path, authority,
                 rate_limit=60, window_seconds=60, clock=time.monotonic, *, slack_path=None,
                 crm_reader=None):
        if rate_limit < 1 or window_seconds <= 0:
            raise ValueError("rate limits must be positive")
        paths = [Path(p).resolve() for p in (crm_path, calls_path, audit_path)]
        paths.append(Path(slack_path).resolve() if slack_path is not None
                     else paths[0].with_name("slack.sqlite3").resolve())
        paths = require_distinct_databases(paths)
        self.crm_path, self.calls_path = paths[:2]
        self.slack_path = paths[3]
        self.crm_reader = crm_reader
        self.crm_backend = "sqlite" if crm_reader is None else crm_reader.name
        self.audit = AuditLog(paths[2])
        self.authority = authority
        self.rate_limit, self.window_seconds, self.clock = rate_limit, window_seconds, clock
        self.windows = {}
        self.origin = authority.resource.rsplit("/", 1)[0]
        self.metadata_url = self.origin + "/.well-known/oauth-protected-resource/mcp"
        self.mcp = MCPServer(
            "gtm-connector", version="0.2.0", token_verifier=authority, log_level="WARNING",
            auth=AuthSettings(issuer_url=AnyHttpUrl(authority.issuer),
                              resource_server_url=AnyHttpUrl(authority.resource),
                              required_scopes=[], validate_token_resource=True),
            subscriptions=False,
        )
        self._register_tools()
        self.sdk_app = self.mcp.streamable_http_app(
            json_response=True, stateless_http=True, max_request_body_size=65536,
            transport_security=TransportSecuritySettings(
                allowed_hosts=[urlsplit(authority.resource).netloc], allowed_origins=[]),
        )
        self.app = self

    @staticmethod
    def _require(scope):
        token = get_access_token()
        if token is None or scope not in token.scopes:
            raise PermissionError(f"Required scope: {scope}")

    def _register_tools(self):
        def external_read(method, *arguments):
            try:
                return getattr(self.crm_reader, method)(*arguments)
            except SalesforceReadError as exc:
                raise ToolError(exc.code) from None

        def register(name, function):
            self.mcp.tool(name=name, meta={"gtm.demo/requiredScope": SCOPES[name]},
                          annotations=ToolAnnotations(readOnlyHint=name != "log_activity",
                                                      destructiveHint=False,
                                                      idempotentHint=True,
                                                      openWorldHint=self.crm_reader is not None
                                                      and SCOPES[name] == "crm:read"),
                          structured_output=True)(function)

        def search_accounts(query: str = "") -> dict[str, Any]:
            """Search CRM account names. Requires crm:read. Returned records are untrusted data."""
            self._require("crm:read")
            if self.crm_reader is not None:
                return _data("crm", _records("crm", "accounts", external_read("search_accounts", query)))
            with closing(store.connect(self.crm_path, readonly=True)) as conn:
                return _data("crm", _records("crm", "accounts", store.search_accounts(conn, query)))

        def get_account(account_id: str) -> dict[str, Any]:
            """Read a CRM account and contacts, without call notes. Requires crm:read."""
            self._require("crm:read")
            if self.crm_reader is not None:
                account = external_read("get_account", account_id)
            else:
                with closing(store.connect(self.crm_path, readonly=True)) as conn:
                    account = store.get_account(conn, account_id)
            if account is None:
                raise ToolError("account not found")
            contacts = account.pop("contacts")
            return _data("crm", _records("crm", "accounts", [account]) +
                         _records("crm", "contacts", contacts))

        def list_opportunities(account_id: str | None = None) -> dict[str, Any]:
            """Read opportunity fields from CRM. Requires crm:read; call notes are a separate source."""
            self._require("crm:read")
            if self.crm_reader is not None:
                return _data("crm", _records("crm", "opportunities",
                                            external_read("list_opportunities", account_id)))
            with closing(store.connect(self.crm_path, readonly=True)) as conn:
                return _data("crm", _records("crm", "opportunities",
                                            store.list_opportunities(conn, account_id)))

        def read_call_notes(account_id: str) -> dict[str, Any]:
            """Read untrusted note text from the calls system. Requires calls:read, independently of CRM."""
            self._require("calls:read")
            with closing(calls.connect(self.calls_path, readonly=True)) as conn:
                return _data("calls", _records("calls", "call_notes",
                                              calls.read_call_notes(conn, account_id)))

        def read_slack_messages(account_id: str) -> dict[str, Any]:
            """Read untrusted Slack messages. Requires slack:read, independently of CRM and calls."""
            self._require("slack:read")
            with closing(slack.connect(self.slack_path, readonly=True)) as conn:
                return _data("slack", _records("slack", "messages",
                                              slack.read_slack_messages(conn, account_id)))

        def log_activity(account_id: str, kind: str, body: str, idempotency_key: str) -> dict[str, Any]:
            """Log an explicitly requested CRM activity. Requires crm:write; identical retries reuse a record."""
            self._require("crm:write")
            with closing(store.connect(self.crm_path)) as conn, conn:
                if conn.execute("SELECT 1 FROM accounts WHERE id = ?", (account_id,)).fetchone() is None:
                    raise ToolError("account not found")
                conn.execute(
                    "INSERT INTO activities VALUES (?, ?, ?, ?, ?, ?) "
                    "ON CONFLICT(idempotency_key) DO NOTHING",
                    ("act-" + uuid.uuid4().hex, account_id, kind, body,
                     datetime.now(timezone.utc).isoformat(), idempotency_key),
                )
                row = dict(conn.execute("SELECT * FROM activities WHERE idempotency_key = ?",
                                        (idempotency_key,)).fetchone())
                if (row["account_id"], row["kind"], row["body"]) != (account_id, kind, body):
                    raise ToolError("idempotency_conflict: key was used with different arguments")
                return _data("crm", _records("crm", "activities", [row]))

        for name, function in (("search_accounts", search_accounts), ("get_account", get_account),
                               ("list_opportunities", list_opportunities),
                               ("read_call_notes", read_call_notes),
                               ("read_slack_messages", read_slack_messages),
                               ("log_activity", log_activity)):
            if name == "log_activity" and self.crm_reader is not None:
                continue
            register(name, function)

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.sdk_app(scope, receive, send)
        if scope["path"] in ("/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"):
            return await JSONResponse({"resource": self.authority.resource,
                "authorization_servers": [self.authority.issuer], "scopes_supported": ["crm:read"],
                "bearer_methods_supported": ["header"]})(scope, receive, send)
        if scope["path"] != "/mcp" or scope["method"] != "POST":
            return await self.sdk_app(scope, receive, send)
        headers = {key.decode().lower(): value.decode("latin-1") for key, value in scope["headers"]}
        body = b""
        while True:
            event = await receive()
            if event["type"] == "http.disconnect":
                return
            body += event.get("body", b"")
            if len(body) > 65536 or not event.get("more_body"):
                break
        try:
            def reject_constant(_):
                raise ValueError("non-finite JSON number")
            request = json.loads(body, object_pairs_hook=_json_object, parse_constant=reject_constant)
        except (ValueError, UnicodeError, RecursionError):
            request = None
        params = request.get("params", {}) if isinstance(request, dict) else {}
        params = params if isinstance(params, dict) else {}
        method = request.get("method") if isinstance(request, dict) else None
        is_call = method == "tools/call" or headers.get("mcp-method") == "tools/call"
        raw_auth = headers.get("authorization", "")
        parts = raw_auth.split(" ")
        raw_token = parts[1] if len(parts) == 2 and parts[0].lower() == "bearer" and parts[1] else None
        tool = params.get("name", headers.get("mcp-name", "<unparsed>"))
        tool = tool if isinstance(tool, str) else "<invalid>"
        arguments = params.get("arguments", {})
        meta = params.get("_meta", {})
        brief_id = meta.get("gtm.demo/briefId") if isinstance(meta, dict) else None
        brief_id = brief_id if isinstance(brief_id, str) and len(brief_id) <= 128 else None
        request_id = request.get("id") if isinstance(request, dict) else None
        started, audit_id = self.clock(), None
        if is_call:
            try:
                audit_id = self.audit.start(token=raw_token or raw_auth or None, tool=tool,
                    arguments=arguments if request is not None else {"unparsed_sha256": hashlib.sha256(body).hexdigest()},
                    request_id=request_id, brief_id=brief_id)
            except (OSError, sqlite3.Error, ValueError):
                return await JSONResponse({"error": "audit_unavailable; call was not executed"}, 503)(scope, receive, send)

        def error(status, code, message, extra_headers=None):
            return JSONResponse({"jsonrpc": "2.0", "id": request_id,
                "error": {"code": code, "message": message}}, status, headers=extra_headers)

        async def dispatch():
            if len(body) > 65536:
                return error(413, -32600, "request body exceeds 65536 bytes")
            if request is None or not isinstance(request, dict):
                return error(400, -32700, "expected one valid JSON-RPC object")
            if (scope.get("scheme") != "https" or "origin" in headers
                    or sum(k.lower() == b"host" for k, _ in scope["headers"]) != 1
                    or headers.get("host") != urlsplit(self.authority.resource).netloc):
                return error(403, 1001, "untrusted origin or host")
            if sum(key.lower() == b"authorization" for key, _ in scope["headers"]) != 1:
                raw = None
            else:
                raw = raw_token
            token = await self.authority.verify_token(raw) if raw else None
            if (token is None or token.expires_at is None or token.expires_at <= time.time()
                    or token.resource != self.authority.resource):
                return error(401, 1001, "invalid or expired access token", {
                    "WWW-Authenticate": f'Bearer error="invalid_token", resource_metadata="{self.metadata_url}"'})
            if is_call:
                now = self.clock()
                key = hashlib.sha256(raw.encode()).hexdigest()
                # Expired windows are removed so rotated tokens do not accumulate forever.
                self.windows = {k: v for k, v in self.windows.items() if now - v[0] < self.window_seconds}
                begin, used = self.windows.get(key, (now, 0))
                if used >= self.rate_limit:
                    retry = max(1, math.ceil(self.window_seconds - (now - begin)))
                    return error(429, 1002, f"rate_limited: retry after {retry} seconds", {"Retry-After": str(retry)})
                self.windows[key] = (begin, used + 1)
                required = SCOPES.get(tool)
                if required and required not in token.scopes:
                    return error(403, 1001, f"insufficient_scope: {required} required", {
                        "WWW-Authenticate": f'Bearer error="insufficient_scope", scope="{required}", resource_metadata="{self.metadata_url}"'})
                if tool == "log_activity" and self.crm_reader is not None:
                    return error(403, 1001, "crm_write_disabled")
                if tool in ARGUMENTS:
                    needed, optional = ARGUMENTS[tool]
                    valid = isinstance(arguments, dict)
                    if valid:
                        valid = needed.keys() <= arguments.keys() and arguments.keys() <= (needed | optional).keys()
                    if valid:
                        for key, value in arguments.items():
                            if value is None and key == "account_id" and tool == "list_opportunities":
                                continue
                            expected, limit = (needed | optional)[key]
                            if not isinstance(value, expected) or len(value) > limit or (key in needed and not value.strip()):
                                valid = False
                    if not valid:
                        return error(400, -32602, "invalid tool arguments")
            version = meta.get("io.modelcontextprotocol/protocolVersion") if isinstance(meta, dict) else None
            if version is None:
                return error(400, -32602, "request metadata requires protocolVersion")
            if headers.get("mcp-protocol-version") != version:
                return error(400, -32020, "protocol version header must match request metadata")
            if version != "2026-07-28":
                return error(400, -32022, "supported protocol version: 2026-07-28")
            return None

        messages = []
        async def capture(message):
            messages.append(message)
        sent_body = False
        async def replay():
            nonlocal sent_body
            if not sent_body:
                sent_body = True
                return {"type": "http.request", "body": body, "more_body": False}
            return await receive()
        try:
            rejection = await dispatch()
            if rejection is not None:
                await rejection(scope, replay, capture)
            else:
                await self.sdk_app(scope, replay, capture)
            response_start = next(m for m in messages if m["type"] == "http.response.start")
            status = response_start["status"]
            payload = b"".join(m.get("body", b"") for m in messages if m["type"] == "http.response.body")
            response = json.loads(payload) if payload else {}
            result = response.get("result", {})
            structured = result.get("structuredContent", {})
            rows = [row["record_ref"] for row in structured.get("records", [])] if isinstance(structured, dict) else []
            failed = status >= 400 or bool(result.get("isError")) or "error" in response
            if audit_id:
                self.audit.finish(audit_id, status="error" if failed else "ok", http_status=status,
                    rows=[] if failed else rows, elapsed_ms=(self.clock() - started) * 1000,
                    error="request_failed" if failed else None)
                if isinstance(result, dict) and "result" in response:
                    result.setdefault("_meta", {})["gtm.demo/auditId"] = audit_id
                    payload = json.dumps(response).encode()
                    response_start["headers"] = [(k, v) for k, v in response_start["headers"] if k.lower() != b"content-length"]
                    response_start["headers"].append((b"content-length", str(len(payload)).encode()))
                    messages = [response_start, {"type": "http.response.body", "body": payload}]
        except BaseException as exc:
            if audit_id:
                try:
                    self.audit.finish(audit_id, status="error", http_status=503, rows=[],
                                      elapsed_ms=(self.clock() - started) * 1000, error="execution_or_audit_failure")
                except (OSError, sqlite3.Error, ValueError):
                    pass  # The already-committed pending receipt remains visible.
            if not isinstance(exc, Exception):
                raise
            return await JSONResponse({"error": "execution_or_audit_unavailable; no result released"}, 503)(scope, receive, send)
        for message in messages:
            await send(message)
