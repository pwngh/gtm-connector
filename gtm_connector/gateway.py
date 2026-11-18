"""One scoped MCP gateway over independent CRM, calls, and Slack fixtures."""

from contextlib import closing
from contextvars import ContextVar
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import sqlite3
import threading
import time
from urllib.parse import urlsplit
import uuid
from typing import Any, Callable

from mcp.server import MCPServer
from mcp.server.auth.provider import AccessToken
from mcp.server.mcpserver.exceptions import ToolError
from mcp.server.auth.middleware.auth_context import get_access_token
from mcp.server.auth.settings import AuthSettings
from mcp.server.transport_security import TransportSecuritySettings
from mcp.types import ToolAnnotations
from pydantic import AnyHttpUrl
from starlette.responses import JSONResponse

from . import calls, slack, store
from .audit import AuditLog
from .request_checks import MISSING, Missing, RequestFacts, RequestPolicy, charge_rate, check_request
from .salesforce import SalesforceReadError
from .selected_briefs import BRIEF_SECONDS, ChildRead, Invocation
from .tool_definitions import DEFAULT_TOOLS, SELECTED_TOOLS, TOOL_DEFINITIONS, valid_account_search


# Set only by Connector.__call__ after admission and a committed receipt. Tool
# arguments and request metadata can never supply or change it.
_INVOCATION: ContextVar[Invocation | None] = ContextVar("gtm_connector_invocation", default=None)
_COMPOSITE_TOOLS = ("prepare_renewal_brief", "read_renewal_brief")
_DEFAULT_DEFINITIONS = {name: definition for name, definition in TOOL_DEFINITIONS.items()
                        if name not in _COMPOSITE_TOOLS}


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
                 crm_reader=None, on_call=None, demo_binding=None, calls_reader=None,
                 slack_reader=None, _slack_only=False, selected=None):
        if rate_limit < 1 or window_seconds <= 0:
            raise ValueError("rate limits must be positive")
        if _slack_only and (slack_reader is None or crm_reader is not None or calls_reader is not None
                            or demo_binding is not None):
            raise ValueError("A Slack-only check requires only its selected Slack reader.")
        if selected is not None and (_slack_only or demo_binding is not None):
            raise ValueError("A selection cannot be combined with the Slack check or a demo binding.")
        if _slack_only:
            # Disabled source paths are never resolved, statted, initialized, or opened.
            paths = [Path(p).absolute() for p in (crm_path, calls_path, audit_path)]
            paths.append(paths[0].with_name("slack.sqlite3"))
        else:
            paths = [Path(p).resolve() for p in (crm_path, calls_path, audit_path)]
            paths.append(Path(slack_path).resolve() if slack_path is not None
                         else paths[0].with_name("slack.sqlite3").resolve())
            paths = require_distinct_databases(paths)
        self.crm_path, self.calls_path = paths[:2]
        self.slack_path = paths[3]
        self.crm_reader = crm_reader
        self.calls_reader = calls_reader
        self.slack_reader = slack_reader
        self._slack_only = _slack_only
        self.selected = selected
        self._definitions = TOOL_DEFINITIONS if selected is not None else _DEFAULT_DEFINITIONS
        self.enabled_tools = ({"read_slack_messages"} if _slack_only else
                              set(SELECTED_TOOLS) if selected is not None else set(DEFAULT_TOOLS))
        self.demo_binding = demo_binding
        self.on_call = on_call
        self.crm_backend = "sqlite" if crm_reader is None else crm_reader.name
        if demo_binding is not None and self.crm_backend != "salesforce":
            raise ValueError("Salesforce demo bindings require the Salesforce CRM backend.")
        if selected is not None and selected.selection.crm_backend != self.crm_backend:
            raise ValueError("The selection was written for another CRM backend.")
        self.audit = AuditLog(paths[2])
        self.authority = authority
        self.rate_limit, self.window_seconds, self.clock = rate_limit, window_seconds, clock
        self.windows = {}
        # Direct calls are admitted on the event loop; composite reads count on worker threads.
        self._windows_lock = threading.Lock()
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

    def _emit_call(self, *, started, token, tool, decision, receipt_id, reason):
        """Observe a completed attempt without changing its response or audit receipt."""
        if self.on_call is None:
            return
        event = {
            "type": "tool_call",
            "time": datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
            "tool": tool if tool in self._definitions or tool in ("<unparsed>", "<invalid>") else "<unknown>",
            "caller": "anonymous" if token is None else hashlib.sha256(token.encode()).hexdigest(),
            "decision": decision,
            "duration_ms": (time.perf_counter() - started) * 1000,
            "receipt_id": receipt_id,
            "reason": reason,
        }
        try:
            self.on_call(event)
        except Exception:
            pass  # The observer is best effort; the durable audit remains authoritative.

    def _require_tool(self, name):
        if name not in self.enabled_tools:
            raise PermissionError("This tool is disabled in the Slack-only check.")
        scope = TOOL_DEFINITIONS[name].scope
        token = get_access_token()
        if token is None or scope not in token.scopes:
            raise PermissionError(f"Required scope: {scope}")

    def _require_selected(self, account_id, *, slack=False):
        """In selected mode, refuse before any source access; an unselected ID is never looked up."""
        if self.selected is None:
            return
        if not self.selected.selection.includes(account_id):
            raise ToolError("account_not_selected")
        if slack and not self.selected.selection.includes_slack(account_id):
            raise ToolError("slack_not_selected")

    def child_read(self, invocation: Invocation, tool: str, account_id: str, *, brief_id: str,
                   ordinal: int) -> ChildRead:
        """One audited source read inside a composite call, under the caller's own bearer.

        The receipt is committed before any check or source access and finished
        before data returns. The token, rate window, scope, and selection are
        checked again here, as for a direct call.
        """
        if self.selected is None or tool not in self._source_reads:
            raise ToolError("brief_plan_invalid")
        arguments = {"account_id": account_id}
        started = self.clock()
        audit_id = self.audit.start(
            token=invocation.raw_token, tool=tool, arguments=arguments, brief_id=brief_id,
            request_id={"kind": "brief_child", "parent_audit_id": invocation.audit_id, "ordinal": ordinal})
        status, http_status, rows = "error", 503, []
        error: str | None = "execution_or_audit_failure"
        try:
            token = self.authority.verify_token_now(invocation.raw_token)
            if token is None:
                status, http_status, error = "error", 401, "request_failed"
                raise ToolError("invalid_token")
            policy = RequestPolicy(resource=self.authority.resource, metadata_url=self.metadata_url,
                                   rate_limit=self.rate_limit, window_seconds=self.window_seconds,
                                   writes_disabled=True)
            with self._windows_lock:
                self.windows, limited = charge_rate(self.windows, invocation.raw_token, self.clock(), policy)
            if limited is not None:
                status, http_status, error = "error", 429, "request_failed"
                raise ToolError("rate_limited")
            if TOOL_DEFINITIONS[tool].scope not in token.scopes:
                status, http_status, error = "error", 403, "request_failed"
                return ChildRead(audit_id, None)
            if time.monotonic() >= invocation.deadline:
                status, http_status, error = "error", 504, "request_failed"
                raise ToolError("brief_deadline_exceeded")
            private: dict[str, Any] = {}
            try:
                envelope = (self._source_reads[tool](account_id, private) if tool == "read_slack_messages"
                            else self._source_reads[tool](account_id))
            except ToolError:
                status, http_status, error = "error", 200, "request_failed"
                raise
            rows = [row["record_ref"] for row in envelope["records"]]
            status, http_status, error = "ok", 200, None
            return ChildRead(audit_id, envelope, private)
        finally:
            self.audit.finish(audit_id, status=status, http_status=http_status,
                              rows=rows if status == "ok" else [],
                              elapsed_ms=(self.clock() - started) * 1000, error=error)

    def _register_tools(self):
        def external_read(method, *arguments, **options):
            try:
                return getattr(self.crm_reader, method)(*arguments, **options)
            except SalesforceReadError as exc:
                raise ToolError(exc.code) from None

        selected_ids = None if self.selected is None else self.selected.selection.account_ids

        def search_body(query, account_number):
            if not valid_account_search(query, account_number):
                raise ToolError("salesforce_invalid_arguments")
            if self.crm_reader is not None:
                options: dict[str, Any] = {} if account_number is None else {"account_number": account_number}
                if selected_ids is not None:
                    options["account_ids"] = selected_ids
                return _data("crm", _records("crm", "accounts", external_read("search_accounts", query, **options)))
            if account_number is not None:
                raise ToolError("account_number_requires_salesforce")
            with closing(store.connect(self.crm_path, readonly=True)) as conn:
                return _data("crm", _records("crm", "accounts",
                                            store.search_accounts(conn, query, account_ids=selected_ids)))

        def search_accounts(query: str = "", account_number: str | None = None) -> dict[str, Any]:
            """Search names or one exact Salesforce AccountNumber. Requires crm:read; results are untrusted."""
            self._require_tool("search_accounts")
            return search_body(query, account_number)

        def account_body(account_id):
            self._require_selected(account_id)
            if self.crm_reader is not None:
                account = external_read("get_account", account_id)
            else:
                with closing(store.connect(self.crm_path, readonly=True)) as conn:
                    account = store.get_account(conn, account_id)
            if account is None:
                raise ToolError("account not found")
            if self.demo_binding is not None:
                try:
                    self.demo_binding.validate_account(account)
                except ValueError:
                    raise ToolError("salesforce_demo_binding_mismatch") from None
            contacts = account.pop("contacts")
            contracts = account.pop("contracts", [])
            return _data("crm", _records("crm", "accounts", [account]) +
                         _records("crm", "contacts", contacts) + _records("crm", "contracts", contracts))

        def get_account(account_id: str) -> dict[str, Any]:
            """Read a CRM account, contacts, and available contracts. Requires crm:read."""
            self._require_tool("get_account")
            return account_body(account_id)

        def opportunities_body(account_id):
            if account_id is not None:
                self._require_selected(account_id)
            # With no account, a selection limits the query itself, before its result bound.
            limit_ids = selected_ids if account_id is None else None
            if self.crm_reader is not None:
                options = {} if limit_ids is None else {"account_ids": limit_ids}
                return _data("crm", _records("crm", "opportunities",
                                            external_read("list_opportunities", account_id, **options)))
            with closing(store.connect(self.crm_path, readonly=True)) as conn:
                return _data("crm", _records("crm", "opportunities",
                                            store.list_opportunities(conn, account_id, account_ids=limit_ids)))

        def list_opportunities(account_id: str | None = None) -> dict[str, Any]:
            """Read opportunity fields from CRM. Requires crm:read; call notes are a separate source."""
            self._require_tool("list_opportunities")
            return opportunities_body(account_id)

        def notes_body(account_id):
            self._require_selected(account_id)
            if self.calls_reader is not None:
                from .calls_import import CallsImportError
                try:
                    rows = self.calls_reader.read_call_notes(account_id)
                except CallsImportError as exc:
                    raise ToolError(exc.code) from None
                return _data("calls", _records("calls", "call_notes", rows))
            with closing(calls.connect(self.calls_path, readonly=True)) as conn:
                return _data("calls", _records("calls", "call_notes",
                                              calls.read_call_notes(conn, account_id)))

        def read_call_notes(account_id: str) -> dict[str, Any]:
            """Read untrusted note text from the calls system. Requires calls:read, independently of CRM."""
            self._require_tool("read_call_notes")
            return notes_body(account_id)

        def slack_body(account_id, private=None):
            self._require_selected(account_id, slack=True)
            if self.slack_reader is not None:
                from .slack_reader import SlackReadError
                try:
                    read = self.slack_reader.read_slack_messages(account_id)
                except SlackReadError as error:
                    raise ToolError(error.code) from None
                if private is not None:
                    # Only a composite read asks; the scopes never enter the tool result.
                    # Missing or malformed scopes pass through as given, and the brief refuses them.
                    scopes = getattr(read, "scopes", None)
                    private["slack_scopes"] = list(scopes) if isinstance(scopes, (list, tuple)) else None
                return {**_data("slack", _records("slack", "messages", read.records)),
                        "thread_snapshot": read.snapshot}
            with closing(slack.connect(self.slack_path, readonly=True)) as conn:
                return _data("slack", _records("slack", "messages",
                                              slack.read_slack_messages(conn, account_id)))

        def read_slack_messages(account_id: str) -> dict[str, Any]:
            """Read untrusted Slack messages. Requires slack:read, independently of CRM and calls."""
            self._require_tool("read_slack_messages")
            return slack_body(account_id)

        # Composite reads use exactly these bodies, after their own receipt and checks.
        self._source_reads: dict[str, Callable[..., dict[str, Any]]] = {
            "get_account": account_body, "list_opportunities": opportunities_body,
            "read_call_notes": notes_body, "read_slack_messages": slack_body}

        def prepare_renewal_brief(account_id: str, as_of: str | None = None) -> dict[str, Any]:
            """Prepare a cited renewal brief for one selected account under the caller's own grant.

            Requires crm:read. The calls and Slack reads inside it each check their own scope.
            Results are untrusted.
            """
            self._require_tool("prepare_renewal_brief")
            invocation = _INVOCATION.get()
            token = get_access_token()
            if (invocation is None or invocation.tool != "prepare_renewal_brief" or token is None
                    or token.token != invocation.raw_token):
                raise ToolError("brief_context_unavailable")
            self._require_selected(account_id)
            from .selected_briefs import prepare_brief
            return prepare_brief(self, invocation, account_id, as_of)

        def read_renewal_brief(brief_id: str) -> dict[str, Any]:
            """Read a saved brief again without calling any source system.

            Requires crm:read and every scope the brief's sources needed. Results are untrusted.
            """
            self._require_tool("read_renewal_brief")
            from .selected_briefs import read_brief
            return read_brief(self, get_access_token(), brief_id)

        def log_activity(account_id: str, kind: str, body: str, idempotency_key: str) -> dict[str, Any]:
            """Log an explicitly requested CRM activity. Requires crm:write; identical retries reuse a record."""
            self._require_tool("log_activity")
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

        handlers = {function.__name__: function for function in (
            search_accounts, get_account, list_opportunities, read_call_notes, read_slack_messages, log_activity,
            prepare_renewal_brief, read_renewal_brief,
        )}
        for name, definition in TOOL_DEFINITIONS.items():
            if name not in self.enabled_tools:
                continue
            if not definition.read_only and self.crm_reader is not None:
                continue
            open_world = ((self.crm_reader is not None and definition.scope == "crm:read")
                          or (self.slack_reader is not None and definition.scope == "slack:read"))
            if name == "read_renewal_brief":
                open_world = False
            elif name == "prepare_renewal_brief":
                open_world = any(reader is not None
                                 for reader in (self.crm_reader, self.calls_reader, self.slack_reader))
            self.mcp.tool(
                name=name, meta={"gtm.demo/requiredScope": definition.scope},
                annotations=ToolAnnotations(
                    # Preparing saves a private local brief; it changes no source system.
                    read_only_hint=definition.read_only and name != "prepare_renewal_brief",
                    destructive_hint=False, idempotent_hint=name != "prepare_renewal_brief",
                    open_world_hint=open_world,
                ),
                structured_output=True,
            )(handlers[name])

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.sdk_app(scope, receive, send)
        if scope["path"] in ("/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"):
            return await JSONResponse({"resource": self.authority.resource,
                "authorization_servers": [self.authority.issuer],
                "scopes_supported": ["slack:read"] if self._slack_only else ["crm:read"],
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
        event_started = time.perf_counter() if is_call and self.on_call is not None else None
        caller_token = raw_token or raw_auth or None
        if is_call:
            try:
                # JSON-RPC ids are strings or integers. Any other value is recorded as a fixed
                # marker, so a caller cannot imitate the server's own correlation for brief reads.
                receipt_id = request_id if request_id is None or type(request_id) in (int, str) else "<invalid>"
                audit_id = self.audit.start(token=caller_token, tool=tool,
                    arguments=(arguments if request is not None
                               else {"unparsed_sha256": hashlib.sha256(body).hexdigest()}),
                    request_id=receipt_id, brief_id=brief_id)
            except (OSError, sqlite3.Error, ValueError):
                self._emit_call(started=event_started, token=caller_token, tool=tool,
                                decision="failed", receipt_id=None, reason="audit_unavailable")
                return await JSONResponse(
                    {"error": "audit_unavailable; call was not executed"}, 503,
                )(scope, receive, send)

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
            facts = RequestFacts(
                body_size=len(body), parsed=request, scheme=scope.get("scheme"), headers=headers,
                header_names=tuple(key for key, _ in scope["headers"]), raw_token=raw_token,
                is_call=is_call, tool=tool, arguments=arguments, meta=meta,
            )
            policy = RequestPolicy(
                resource=self.authority.resource, metadata_url=self.metadata_url,
                rate_limit=self.rate_limit, window_seconds=self.window_seconds,
                writes_disabled=self.crm_reader is not None or self._slack_only or self.selected is not None,
            )
            token: AccessToken | None | Missing = MISSING
            wall_time: float | Missing = MISSING
            decision = check_request(facts, policy, self.windows, self._definitions)
            if decision.needs == "token":
                token = await self.authority.verify_token(raw_token)
                decision = check_request(facts, policy, self.windows, self._definitions, token=token)
            if decision.needs == "wall_time":
                wall_time = time.time()
                decision = check_request(facts, policy, self.windows, self._definitions,
                                         token=token, wall_time=wall_time)
            if decision.needs == "rate_time":
                # Composite reads update the same windows from worker threads.
                with self._windows_lock:
                    rate_time = self.clock()
                    decision = check_request(facts, policy, self.windows, self._definitions,
                                             token=token, wall_time=wall_time, rate_time=rate_time)
                    if decision.windows is not None:
                        self.windows = decision.windows
            if decision.error is not None:
                error = decision.error
                rejection = JSONResponse({"jsonrpc": "2.0", "id": request_id,
                    "error": {"code": error.code, "message": error.message, "data": {"reason": error.reason}}},
                    error.status, headers=error.headers)
                await rejection(scope, replay, capture)
            else:
                frame = None
                if is_call and audit_id and raw_token is not None and self.selected is not None:
                    frame = _INVOCATION.set(Invocation(raw_token, audit_id, tool,
                                                       time.monotonic() + BRIEF_SECONDS))
                try:
                    await self.sdk_app(scope, replay, capture)
                finally:
                    if frame is not None:
                        _INVOCATION.reset(frame)
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
                    response_start["headers"] = [
                        (k, v) for k, v in response_start["headers"] if k.lower() != b"content-length"
                    ]
                    response_start["headers"].append((b"content-length", str(len(payload)).encode()))
                    messages = [response_start, {"type": "http.response.body", "body": payload}]
        except BaseException as exc:
            if audit_id:
                try:
                    self.audit.finish(audit_id, status="error", http_status=503, rows=[],
                                      elapsed_ms=(self.clock() - started) * 1000, error="execution_or_audit_failure")
                except (OSError, sqlite3.Error, ValueError):
                    pass  # The already-committed pending receipt remains visible.
            if is_call:
                self._emit_call(started=event_started, token=caller_token, tool=tool,
                                decision="failed", receipt_id=audit_id, reason="execution_or_audit_failure")
            if not isinstance(exc, Exception):
                raise
            return await JSONResponse(
                {"error": "execution_or_audit_unavailable; no result released"}, 503,
            )(scope, receive, send)
        if is_call:
            call_decision = "refused" if decision.error is not None else "failed" if failed else "allowed"
            reason = decision.error.reason if decision.error is not None else "tool_error" if failed else None
            self._emit_call(started=event_started, token=caller_token, tool=tool,
                            decision=call_decision, receipt_id=audit_id, reason=reason)
        for message in messages:
            await send(message)
