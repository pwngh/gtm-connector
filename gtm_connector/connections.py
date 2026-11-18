"""Print reviewed client setup without running an assistant or exposing its grant."""

import json
import shlex

from .doctor import _directory, load_local_owner


_DOCUMENTATION = {
    "inspector": ["https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector"],
    "claude-code": ["https://code.claude.com/docs/en/mcp", "https://code.claude.com/docs/en/network-config"],
    "cursor": ["https://cursor.com/docs/mcp"],
    "claude-desktop": [
        "https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp"],
}
_AUTH_LIMITS = [
    "Interactive app login is unavailable. The authority registers only gtm-demo-cli with "
    "http://127.0.0.1:8767/callback and requires the local owner header.",
    "There is no dynamic client registration, browser consent page, or token refresh. Browser Origin is rejected.",
    "Supplied grants expire after five minutes. Rerun connect with explicit scopes to replace the private config.",
    "Restarting serve replaces its endpoints and certificate and invalidates old grants.",
]


def _environment(certificate):
    return ("NODE_EXTRA_CA_CERTS=" + shlex.quote(str(certificate)) + " "
            'NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" '
            'no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" '
            "NODE_TLS_REJECT_UNAUTHORIZED=1 ")


def connect(directory, app, scopes=None):
    """Return setup text; only explicitly requested scopes create a local grant."""
    if app not in _DOCUMENTATION:
        raise ValueError("Choose inspector, claude-code, claude-desktop, or cursor.")
    directory = _directory(directory)
    result = {"app": app, "status": "tested" if app == "inspector" else "untested",
              "setup": [], "limits": list(_AUTH_LIMITS), "documentation": _DOCUMENTATION[app]}
    setup = result["setup"]
    if app == "claude-desktop":
        setup.append(shlex.join(["gtm-connector", "connect", "inspector", "--state-dir", str(directory),
                                 "--scope", "crm:read"]))
        result["limits"] = [
            "No working Claude Desktop connection is available with this connector.",
            "Desktop remote connectors originate in Anthropic's cloud and require public HTTPS on port 443; "
            "they cannot reach this server on 127.0.0.1.",
            "Desktop's separate local configuration uses stdio. This connector supplies no stdio transport.",
            "Use the tested Inspector CLI path above. No tunnel or deployment is created.",
        ] + result["limits"]
        return result
    if not scopes:
        setup.append(shlex.join(["gtm-connector", "serve", "--state-dir", str(directory)]))
        setup.append(shlex.join(["gtm-connector", "connect", app, "--state-dir", str(directory),
                                 "--scope", "crm:read"]))
        result["limits"].insert(0, "No grant or config was created. Select each scope explicitly.")
    else:
        import httpx2
        from .client import LocalClient
        from .inspector import InspectorConnectionError
        from .runtime import private_file
        owner = load_local_owner(directory)
        try:
            with LocalClient(owner) as client:
                token = client.authorize(scopes)
        except httpx2.HTTPError:
            raise InspectorConnectionError(
                "Cannot reach the local authority; start serve in this state directory.") from None
        entry = {"url": owner["resource"], "headers": {"Authorization": "Bearer " + token}}
        if app == "inspector":
            entry.update({"type": "streamable-http", "protocolEra": "modern"})
        elif app == "claude-code":
            entry["type"] = "http"
        path = directory / (app + ".json")
        private_file(path, json.dumps({"mcpServers": {"gtm-connector": entry}}, indent=2).encode())
        result["config"] = str(path)
    certificate = directory / "tls.pem"
    if app == "inspector":
        path = directory / "inspector.json"
        setup.append("npx --yes @modelcontextprotocol/inspector@2.15.0 --version")
        setup.append(_environment(certificate) + shlex.join([
            "npx", "--yes", "@modelcontextprotocol/inspector@2.15.0", "--cli", "--config", str(path),
            "--server", "gtm-connector", "--stored-auth-only", "--quiet", "--format", "json",
            "--method", "tools/list"]))
        result["limits"].insert(0, "Tested: Inspector 2.15.0 CLI with a supplied grant. The UI and login are untested.")
    elif app == "claude-code":
        path = directory / "claude-code.json"
        setup.append(_environment(certificate) + "MCP_SDK_GENERATION=v2 MCP_PROTOCOL_NEGOTIATION=auto "
                     + shlex.join(["claude", "--mcp-config", str(path)]))
        result["limits"].insert(0, "Untested in Claude Code. This uses its documented HTTP config and modern "
                                "protocol options; no hosted assistant was connected.")
    else:
        path = directory / "cursor.json"
        setup.append("Merge the gtm-connector entry from " + shlex.quote(str(path))
                     + " into .cursor/mcp.json. Keep other entries and keep that file private and out of Git.")
        setup.append("Quit Cursor, then try this local launch: " + _environment(certificate) + "cursor .")
        result["limits"].insert(0, "Untested in Cursor. Its docs support URL and header config; compatibility "
                                "with MCP 2026-07-28 and this self-signed certificate is unverified.")
    return result
