"""Pure request decisions. Verification, clocks, and state writes stay in the gateway."""

from dataclasses import dataclass
import hashlib
import math
from typing import Mapping
from urllib.parse import urlsplit

from mcp.server.auth.provider import AccessToken

from .tool_definitions import ToolDefinition, valid_arguments


@dataclass(frozen=True)
class Refusal:
    status: int
    code: int


REFUSALS = {
    "request_too_large": Refusal(413, -32600),
    "invalid_json": Refusal(400, -32700),
    "unsupported_media_type": Refusal(415, -32600),
    "call_id_required": Refusal(400, -32600),
    "method_not_found": Refusal(404, -32601),
    "untrusted_origin_or_host": Refusal(403, 1003),
    "invalid_token": Refusal(401, 1001),
    "rate_limited": Refusal(429, 1002),
    "insufficient_scope": Refusal(403, 1004),
    "crm_write_disabled": Refusal(403, 1005),
    "invalid_arguments": Refusal(400, -32602),
    "missing_protocol_version": Refusal(400, -32602),
    "protocol_version_mismatch": Refusal(400, -32020),
    "unsupported_protocol_version": Refusal(400, -32022),
}


Windows = Mapping[str, tuple[float, int]]


@dataclass(frozen=True)
class RequestFacts:
    body_size: int
    parsed: object
    scheme: str | None
    headers: Mapping[str, str]
    header_names: tuple[bytes, ...]
    raw_token: str | None
    is_call: bool
    tool: str
    arguments: object
    meta: object


@dataclass(frozen=True)
class RequestPolicy:
    resource: str
    metadata_url: str
    rate_limit: int
    window_seconds: float
    writes_disabled: bool


@dataclass(frozen=True)
class RequestError:
    reason: str
    message: str
    headers: dict[str, str] | None = None

    @property
    def status(self) -> int:
        return REFUSALS[self.reason].status

    @property
    def code(self) -> int:
        return REFUSALS[self.reason].code


@dataclass(frozen=True)
class RequestDecision:
    error: RequestError | None = None
    windows: dict[str, tuple[float, int]] | None = None


def charge_rate(windows: Windows, raw_token: str, rate_time: float,
                policy: RequestPolicy) -> tuple[dict[str, tuple[float, int]], RequestError | None]:
    """Count one attempt against this credential's window, or refuse it when the window is full."""
    key = hashlib.sha256(raw_token.encode()).hexdigest()
    updated = {name: value for name, value in windows.items() if rate_time - value[0] < policy.window_seconds}
    begin, used = updated.get(key, (rate_time, 0))
    if used >= policy.rate_limit:
        retry = max(1, math.ceil(policy.window_seconds - (rate_time - begin)))
        return updated, RequestError("rate_limited", f"rate_limited: retry after {retry} seconds",
                                     {"Retry-After": str(retry)})
    updated[key] = (begin, used + 1)
    return updated, None


def check_request(request: RequestFacts, policy: RequestPolicy, windows: Windows,
                  definitions: Mapping[str, ToolDefinition], *,
                  token: AccessToken | None, wall_time: float, rate_time: float) -> RequestDecision:
    """Return the first refusal in a fixed order, or none. Inputs are never changed."""
    if request.body_size > 65536:
        return RequestDecision(error=RequestError("request_too_large", "request body exceeds 65536 bytes"))
    if not isinstance(request.parsed, dict):
        return RequestDecision(error=RequestError("invalid_json", "expected one valid JSON-RPC object"))
    if (request.scheme != "https" or "origin" in request.headers
            or sum(name.lower() == b"host" for name in request.header_names) != 1
            or request.headers.get("host") != urlsplit(policy.resource).netloc):
        return RequestDecision(error=RequestError("untrusted_origin_or_host", "untrusted origin or host"))
    raw = request.raw_token if sum(name.lower() == b"authorization" for name in request.header_names) == 1 else None
    if (not raw or token is None or token.expires_at is None or token.expires_at <= wall_time
            or token.resource != policy.resource):
        return RequestDecision(error=RequestError("invalid_token", "invalid or expired access token", {
            "WWW-Authenticate": f'Bearer error="invalid_token", resource_metadata="{policy.metadata_url}"',
        }))
    updated = None
    if request.is_call:
        # Every call a credential makes counts against its window, refused or not.
        updated, limited = charge_rate(windows, raw, rate_time, policy)
        if limited is not None:
            return RequestDecision(windows=updated, error=limited)
    if (sum(name.lower() == b"content-type" for name in request.header_names) != 1
            or request.headers.get("content-type", "").split(";")[0].strip().lower() != "application/json"):
        return RequestDecision(windows=updated, error=RequestError(
            "unsupported_media_type", "Content-Type must be application/json"))
    if request.is_call:
        if type(request.parsed.get("id")) not in (int, str):
            # Without an id the call is a notification: nothing would run and nothing would answer.
            return RequestDecision(windows=updated, error=RequestError(
                "call_id_required", "a tool call requires a string or integer id"))
        definition = definitions.get(request.tool)
        required = definition.scope if definition is not None else None
        if required and required not in token.scopes:
            challenge = (f'Bearer error="insufficient_scope", scope="{required}", '
                         f'resource_metadata="{policy.metadata_url}"')
            return RequestDecision(windows=updated, error=RequestError(
                "insufficient_scope", f"insufficient_scope: {required} required", {"WWW-Authenticate": challenge},
            ))
        if definition is not None and not definition.read_only and policy.writes_disabled:
            return RequestDecision(windows=updated, error=RequestError("crm_write_disabled", "crm_write_disabled"))
        if definition is not None and not valid_arguments(definition, request.arguments):
            return RequestDecision(windows=updated, error=RequestError("invalid_arguments", "invalid tool arguments"))
    version = request.meta.get("io.modelcontextprotocol/protocolVersion") if isinstance(request.meta, dict) else None
    if version is None:
        return RequestDecision(windows=updated, error=RequestError(
            "missing_protocol_version", "request metadata requires protocolVersion",
        ))
    if request.headers.get("mcp-protocol-version") != version:
        return RequestDecision(windows=updated, error=RequestError(
            "protocol_version_mismatch", "protocol version header must match request metadata",
        ))
    if version != "2026-07-28":
        return RequestDecision(windows=updated, error=RequestError(
            "unsupported_protocol_version", "supported protocol version: 2026-07-28",
        ))
    if request.parsed.get("method") in ("prompts/get", "resources/read"):
        # The connector serves tools only, so no prompt or resource exists to return.
        return RequestDecision(windows=updated, error=RequestError("method_not_found", "Method not found"))
    return RequestDecision(windows=updated)
