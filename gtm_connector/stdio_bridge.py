"""A local stdio MCP bridge to a running connector.

An assistant app on this computer starts this process. Every request it forwards
still goes through the connector's HTTPS gateway, with its receipts and rate
limit. The bridge holds one grant for the scopes named at launch, in memory
only, and renews it for exactly those scopes. A request can name only a tool and
its arguments; it cannot name another address, a credential, or a scope.
"""

import threading
import time
from typing import Any

import anyio
import httpx2
from mcp import types
from mcp.server.lowlevel import Server
from mcp.server.stdio import stdio_server
from mcp.shared.exceptions import MCPError

from .auth import SCOPES
from .client import AuthorizationError, LocalClient


RENEW_BEFORE_SECONDS = 60
# A prepared brief has 45 seconds plus its receipt writes; wait longer than that.
BRIEF_READ_SECONDS = 75
_BRIEF_TOOLS = ("prepare_renewal_brief",)
_INTERNAL_ERROR = -32603
_UNREACHABLE = "The connector is unreachable or restarted. Start debug serve again, then restart this client."


class BridgeError(RuntimeError):
    """The bridge cannot reach or obtain its grant from the running connector."""


class Grant:
    """One launch grant held in memory. Renewal requests exactly the launch scopes, never more."""

    def __init__(self, client, scopes, *, clock=time.monotonic):
        requested = sorted(set(scopes or ()))
        if not requested or any(scope not in SCOPES for scope in requested):
            raise ValueError("Name one or more supported scopes with --scope.")
        self.scopes = tuple(requested)
        self._client, self._clock = client, clock
        self._lock = threading.Lock()
        self._token: str | None = None
        self._renew_at = 0.0
        self.issued = 0

    def _issue(self) -> str:
        try:
            token, lifetime = self._client.authorize_with_lifetime(list(self.scopes))
        except (AuthorizationError, httpx2.HTTPError, OSError, ValueError):
            raise BridgeError(_UNREACHABLE) from None
        self._token = token
        self._renew_at = self._clock() + lifetime - min(RENEW_BEFORE_SECONDS, lifetime / 4)
        self.issued += 1
        return token

    def renew(self, refused: str | None = None) -> str:
        """Replace the grant. A call whose refused token another call has already replaced reuses that one."""
        with self._lock:
            if refused is not None and self._token is not None and self._token != refused:
                return self._token
            return self._issue()

    def current(self) -> str:
        with self._lock:
            if self._token is not None and self._clock() < self._renew_at:
                return self._token
            return self._issue()


def _decode(response) -> dict[str, Any]:
    """Return a result or raise the gateway's own JSON-RPC error. Nothing is retried here."""
    try:
        document = response.json()
    except ValueError:
        raise MCPError(_INTERNAL_ERROR, f"The connector returned HTTP {response.status_code} without JSON.") from None
    if not isinstance(document, dict):
        raise MCPError(_INTERNAL_ERROR, "The connector returned an invalid response.")
    result, error = document.get("result"), document.get("error")
    if response.status_code == 200 and isinstance(result, dict):
        return result
    if isinstance(error, dict) and type(error.get("code")) is int and isinstance(error.get("message"), str):
        data = error.get("data")
        raise MCPError(error["code"], error["message"], data if isinstance(data, dict) else None)
    if isinstance(error, str):
        raise MCPError(_INTERNAL_ERROR, error)
    raise MCPError(_INTERNAL_ERROR, f"The connector returned HTTP {response.status_code}.")


class Bridge:
    """Forward MCP requests to the gateway with the launch grant."""

    def __init__(self, owner_config: dict, scopes, *, transport=None, clock=time.monotonic):
        self.client = LocalClient(owner_config, transport=transport)
        self.grant = Grant(self.client, scopes, clock=clock)

    def start(self) -> "Bridge":
        # The launch grant comes from the existing local owner flow, for exactly the named scopes.
        self.grant.renew()
        return self

    def forward(self, method: str, params: dict | None, *, read_timeout: float | None = None) -> dict[str, Any]:
        for attempt in (1, 2):
            try:
                token = self.grant.current()
                response = self.client.rpc(token, method, params, read_timeout=read_timeout)
            except BridgeError as exc:
                raise MCPError(_INTERNAL_ERROR, str(exc)) from None
            except httpx2.HTTPError:
                raise MCPError(_INTERNAL_ERROR, _UNREACHABLE) from None
            if response.status_code == 401 and attempt == 1:
                # The gateway refused the grant before running anything. Renew the same
                # scopes once; a request is never retried with more authority.
                try:
                    self.grant.renew(refused=token)
                except BridgeError as exc:
                    raise MCPError(_INTERNAL_ERROR, str(exc)) from None
                continue
            return _decode(response)
        raise MCPError(_INTERNAL_ERROR, _UNREACHABLE)

    def close(self) -> None:
        self.client.close()


def build_server(bridge: Bridge) -> Server:
    async def list_tools(ctx, params) -> types.ListToolsResult:
        result = await anyio.to_thread.run_sync(lambda: bridge.forward("tools/list", None))
        return types.ListToolsResult.model_validate(result)

    async def call_tool(ctx, params: types.CallToolRequestParams) -> types.CallToolResult:
        # Only the tool name and its arguments cross; caller metadata, headers, and scopes do not.
        payload = {"name": params.name, "arguments": dict(params.arguments or {})}
        timeout = BRIEF_READ_SECONDS if params.name in _BRIEF_TOOLS else None
        result = await anyio.to_thread.run_sync(
            lambda: bridge.forward("tools/call", payload, read_timeout=timeout))
        return types.CallToolResult.model_validate(result)

    return Server("gtm-connector", version="0.2.0", on_list_tools=list_tools, on_call_tool=call_tool)


async def _record_malformed_calls(bridge: Bridge, source, target) -> None:
    """Pass every message on. A tool call the SDK will refuse for its shape is first sent to the gateway,
    which records it and refuses it, so no attempted call is missing from the audit log."""
    async with source, target:
        async for item in source:
            request = getattr(item, "message", None)
            params = getattr(request, "params", None)
            if getattr(request, "method", None) == "tools/call":
                name = params.get("name") if isinstance(params, dict) else None
                arguments = params.get("arguments") if isinstance(params, dict) else None
                if not isinstance(name, str) or not isinstance(arguments, (dict, type(None))):
                    payload = {"name": name if isinstance(name, str) else "<invalid>", "arguments": arguments}
                    try:
                        await anyio.to_thread.run_sync(lambda: bridge.forward("tools/call", payload))
                    except MCPError:
                        pass  # The gateway refused it, as intended. The SDK answers the client next.
            await target.send(item)


def run_bridge(directory, scopes, sources) -> None:
    """Serve MCP on stdin and stdout until the client closes stdin. Diagnostics go to stderr.

    The connector it attaches to must serve the sources the folder's profile names.
    """
    from .doctor import load_local_owner
    try:
        owner = load_local_owner(directory)
    except (OSError, ValueError):
        raise BridgeError("No running connector state was found in this folder. "
                          "Start gtm-connector debug serve with the same --state-dir first.") from None
    if any(owner.get(name) != value for name, value in sources.items()):
        raise BridgeError("The running connector was started before this folder's sources changed. "
                          "Start gtm-connector debug serve again, then restart this client.")
    bridge = Bridge(owner, scopes).start()
    server = build_server(bridge)

    async def serve():
        async with stdio_server() as (read_stream, write_stream), anyio.create_task_group() as tasks:
            incoming, checked = anyio.create_memory_object_stream[Any](0)
            tasks.start_soon(_record_malformed_calls, bridge, read_stream, incoming)
            await server.run(checked, write_stream, server.create_initialization_options())
            tasks.cancel_scope.cancel()

    try:
        anyio.run(serve)
    finally:
        bridge.close()
