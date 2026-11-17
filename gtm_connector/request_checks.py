"""Pure request decisions. Verification, clocks, and state writes stay in the gateway."""

from dataclasses import dataclass
from enum import Enum
import hashlib
import math
from typing import Literal, Mapping
from urllib.parse import urlsplit

from mcp.server.auth.provider import AccessToken

from .tool_definitions import ToolDefinition, valid_arguments


class Missing(Enum):
    VALUE = "not supplied"


MISSING = Missing.VALUE
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
    status: int
    code: int
    message: str
    headers: dict[str, str] | None = None


@dataclass(frozen=True)
class RequestDecision:
    needs: Literal["token", "wall_time", "rate_time"] | None = None
    error: RequestError | None = None
    windows: dict[str, tuple[float, int]] | None = None


def check_request(request: RequestFacts, policy: RequestPolicy, windows: Windows,
                  definitions: Mapping[str, ToolDefinition], *,
                  token: AccessToken | None | Missing = MISSING,
                  wall_time: float | Missing = MISSING,
                  rate_time: float | Missing = MISSING) -> RequestDecision:
    """Request missing inputs in order, then return a decision without changing inputs."""
    if request.body_size > 65536:
        return RequestDecision(error=RequestError(413, -32600, "request body exceeds 65536 bytes"))
    if not isinstance(request.parsed, dict):
        return RequestDecision(error=RequestError(400, -32700, "expected one valid JSON-RPC object"))
    if (request.scheme != "https" or "origin" in request.headers
            or sum(name.lower() == b"host" for name in request.header_names) != 1
            or request.headers.get("host") != urlsplit(policy.resource).netloc):
        return RequestDecision(error=RequestError(403, 1001, "untrusted origin or host"))
    raw = request.raw_token if sum(name.lower() == b"authorization" for name in request.header_names) == 1 else None
    if not raw:
        token = None
    elif token is MISSING:
        return RequestDecision(needs="token")
    if token is not None and token.expires_at is not None and wall_time is MISSING:
        return RequestDecision(needs="wall_time")
    if (token is None or token.expires_at is None or token.expires_at <= wall_time
            or token.resource != policy.resource):
        return RequestDecision(error=RequestError(401, 1001, "invalid or expired access token", {
            "WWW-Authenticate": f'Bearer error="invalid_token", resource_metadata="{policy.metadata_url}"',
        }))
    updated = None
    if request.is_call:
        if rate_time is MISSING:
            return RequestDecision(needs="rate_time")
        key = hashlib.sha256(raw.encode()).hexdigest()
        updated = {key: value for key, value in windows.items() if rate_time - value[0] < policy.window_seconds}
        begin, used = updated.get(key, (rate_time, 0))
        if used >= policy.rate_limit:
            retry = max(1, math.ceil(policy.window_seconds - (rate_time - begin)))
            return RequestDecision(windows=updated, error=RequestError(
                429, 1002, f"rate_limited: retry after {retry} seconds", {"Retry-After": str(retry)},
            ))
        updated[key] = (begin, used + 1)
        definition = definitions.get(request.tool)
        required = definition.scope if definition is not None else None
        if required and required not in token.scopes:
            challenge = (f'Bearer error="insufficient_scope", scope="{required}", '
                         f'resource_metadata="{policy.metadata_url}"')
            return RequestDecision(windows=updated, error=RequestError(
                403, 1001, f"insufficient_scope: {required} required", {"WWW-Authenticate": challenge},
            ))
        if definition is not None and not definition.read_only and policy.writes_disabled:
            return RequestDecision(windows=updated, error=RequestError(403, 1001, "crm_write_disabled"))
        if definition is not None and not valid_arguments(definition, request.arguments):
            return RequestDecision(windows=updated, error=RequestError(400, -32602, "invalid tool arguments"))
    version = request.meta.get("io.modelcontextprotocol/protocolVersion") if isinstance(request.meta, dict) else None
    if version is None:
        return RequestDecision(windows=updated, error=RequestError(
            400, -32602, "request metadata requires protocolVersion",
        ))
    if request.headers.get("mcp-protocol-version") != version:
        return RequestDecision(windows=updated, error=RequestError(
            400, -32020, "protocol version header must match request metadata",
        ))
    if version != "2026-07-28":
        return RequestDecision(windows=updated, error=RequestError(
            400, -32022, "supported protocol version: 2026-07-28",
        ))
    return RequestDecision(windows=updated)
