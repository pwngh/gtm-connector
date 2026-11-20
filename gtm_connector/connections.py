"""Print reviewed client setup without running an assistant or exposing its grant."""

import json
import os
from pathlib import Path
import shlex
import stat
import sys
import tempfile

from . import strict_json
from .auth import SCOPES
from .doctor import _directory, load_local_owner


class InspectorConnectionError(RuntimeError):
    """The local authorization service could not be reached."""


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


_STDIO_DOCUMENTATION = {
    "claude-code": ["https://code.claude.com/docs/en/mcp"],
    "claude-desktop": ["https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers"],
    "cursor": ["https://cursor.com/docs/mcp"],
}
_STDIO_LIMITS = [
    "Untested in the app itself. The bridge command is tested with the MCP SDK's own stdio client.",
    "The app starts the bridge, which attaches to the serve process already running in this state folder. "
    "Start serve first; the bridge exits with a message on stderr if it cannot reach it.",
    "At launch the bridge obtains a grant for exactly the listed scopes and keeps it in memory. "
    "It renews only those scopes.",
    "Restarting serve replaces its endpoints. Restart the app afterward.",
    "The configuration holds the Python path, the state folder, and scopes. It holds no token or owner credential.",
]
_MAX_CONFIG_BYTES = 4 * 1024 * 1024
SERVER_NAME = "gtm-connector"


def stdio_entry(directory, app, scopes):
    """The client entry: an absolute interpreter, fixed arguments, and paths. Never a credential."""
    import gtm_connector
    root = Path(gtm_connector.__file__).resolve().parent.parent
    # -I keeps the app's working folder, PYTHONPATH, and the user site off the import path, so nothing
    # there can stand in for this package while it holds the owner's grant.
    if {"site-packages", "dist-packages"} & set(root.parts):
        launch = ["-I", "-m", "gtm_connector"]
    else:
        # A source checkout is named outright, since an isolated interpreter does not search for it.
        launch = ["-I", "-c", f"import sys; sys.path.insert(0, {str(root)!r}); "
                              "from gtm_connector.cli import main; sys.exit(main(sys.argv[1:]))"]
    arguments = [*launch, "mcp", "stdio", "--state-dir", str(_directory(directory))]
    for scope in sorted(set(scopes)):
        arguments += ["--scope", scope]
    # Keep the venv interpreter path as invoked; resolving its symlink would leave the venv.
    entry: dict = {"command": str(Path(sys.executable).absolute()), "args": arguments}
    return {"type": "stdio", **entry} if app == "claude-code" else entry


def _read_regular(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_size > _MAX_CONFIG_BYTES:
            raise ValueError(f"{path.name} must be a regular file with one name and at most 4 MiB; "
                             "nothing was written.")
        with os.fdopen(descriptor, "rb", closefd=False) as source:
            data = source.read(_MAX_CONFIG_BYTES + 1)
    finally:
        os.close(descriptor)
    if len(data) > _MAX_CONFIG_BYTES:
        raise ValueError(f"{path.name} is larger than 4 MiB; nothing was written.")
    return data, stat.S_IMODE(info.st_mode)


def _staged(path, data, mode):
    """Write data to a synced temporary file beside path and return its name. Nothing is replaced yet."""
    descriptor, temporary = tempfile.mkstemp(dir=path.parent, prefix=f".{path.name}.", suffix=".tmp")
    try:
        with os.fdopen(descriptor, "wb") as output:
            os.fchmod(output.fileno(), mode)
            output.write(data)
            output.flush()
            os.fsync(output.fileno())
    except BaseException:
        Path(temporary).unlink(missing_ok=True)
        raise
    return temporary


def merge_client_config(path, entry, *, _after_read=None):
    """Merge only the connector's entry; back up first with the same mode; refuse links and concurrent edits."""
    path = Path(path).expanduser().absolute()
    backup = path.with_name(path.name + ".gtm-connector-backup")
    for candidate in (path, backup):
        if candidate.is_symlink():
            raise ValueError(f"{candidate.name} is a symbolic link; nothing was written.")
    if os.path.lexists(path):
        original, mode = _read_regular(path)
        try:
            document = strict_json.loads(original)
        except (ValueError, RecursionError):
            raise ValueError(f"{path.name} is not valid JSON; nothing was written.") from None
        servers = document.get("mcpServers", {}) if isinstance(document, dict) else None
        if not isinstance(servers, dict):
            raise ValueError(f"{path.name} must be a JSON object whose mcpServers is an object; nothing was written.")
    else:
        if not path.parent.is_dir():
            raise ValueError(f"The folder for {path.name} does not exist; nothing was written.")
        original, mode, document, servers = None, 0o600, {}, {}
    updated = dict(document)
    updated["mcpServers"] = {**servers, SERVER_NAME: entry}
    payload = (json.dumps(updated, indent=2, ensure_ascii=False, allow_nan=False) + "\n").encode()
    if len(payload) > _MAX_CONFIG_BYTES:
        raise ValueError(f"{path.name} would grow past 4 MiB; nothing was written.")
    if _after_read is not None:
        _after_read()
    # Both files are written and synced under temporary names first. The app's file is then read
    # again, and only if it is still what was merged do the two renames happen, backup first. A rename
    # replaces a name and never writes through a link. An app that saves in the instant between the
    # last read and the rename can still lose that save, so quit the app before running connect.
    staged = [_staged(path, payload, mode)]
    try:
        if original is not None:
            staged.append(_staged(backup, original, mode))
        current = _read_regular(path)[0] if os.path.lexists(path) else None
        if current != original:
            raise ValueError(f"{path.name} changed after it was read; nothing was written. Run connect again.")
        if original is not None:
            os.replace(staged.pop(), backup)
        os.replace(staged.pop(0), path)
    finally:
        for temporary in staged:
            Path(temporary).unlink(missing_ok=True)
    return {"config": str(path), "backup": str(backup) if original is not None else None}


def _connect_stdio(directory, app, scopes, config_file):
    if app not in _STDIO_DOCUMENTATION:
        raise ValueError("Choose claude-code, claude-desktop, or cursor with --transport stdio.")
    if not scopes or any(scope not in SCOPES for scope in scopes):
        raise ValueError("Select each supported scope explicitly with --scope.")
    if config_file is None:
        raise ValueError("--transport stdio needs --config-file naming the app's configuration file.")
    directory = _directory(directory)
    written = merge_client_config(config_file, stdio_entry(directory, app, scopes))
    setup = [*_serve_steps(directory), f"Restart {app} so it reads {shlex.quote(written['config'])}."]
    if written["backup"] is not None:
        setup.append(f"The previous file is saved as {shlex.quote(written['backup'])}.")
    return {"app": app, "transport": "stdio", "status": "untested", "config": written["config"],
            "backup": written["backup"], "setup": setup, "limits": list(_STDIO_LIMITS),
            "documentation": _STDIO_DOCUMENTATION[app]}



def _serve_steps(directory):
    """How to start the connector in a folder. serve reads the folder's profile, so a folder with none is
    connected first."""
    serve = shlex.join(["gtm-connector", "serve", "--state-dir", str(directory)])
    if os.path.lexists(directory / "profile.json"):
        return [serve]
    return [shlex.join(["gtm-connector", "connect", "demo", "--state-dir", str(directory)])
            + " (or connect salesforce --org ALIAS)", serve]

def connect(directory, app, scopes=None, *, transport="http", config_file=None):
    """Return setup text; only explicitly requested scopes create a local grant."""
    if transport == "stdio":
        return _connect_stdio(directory, app, scopes, config_file)
    if config_file is not None:
        raise ValueError("--config-file applies only to --transport stdio.")
    if app not in _DOCUMENTATION:
        raise ValueError("Choose inspector, claude-code, claude-desktop, or cursor.")
    directory = _directory(directory)
    result = {"app": app, "status": "tested" if app == "inspector" else "untested",
              "setup": [], "limits": list(_AUTH_LIMITS), "documentation": _DOCUMENTATION[app]}
    setup = result["setup"]
    if app == "claude-desktop":
        setup.append(shlex.join(["gtm-connector", "connect", "claude-desktop", "--transport", "stdio",
                                 "--state-dir", str(directory), "--scope", "crm:read", "--config-file",
                                 "CLAUDE_DESKTOP_CONFIG_FILE"]))
        result["limits"] = [
            "Desktop remote connectors originate in Anthropic's cloud and require public HTTPS on port 443; "
            "they cannot reach this server on 127.0.0.1.",
            "Desktop starts local servers as processes. Use --transport stdio as above; it is untested in the app.",
            "No tunnel or deployment is created.",
        ] + result["limits"]
        return result
    if not scopes:
        setup.extend(_serve_steps(directory))
        setup.append(shlex.join(["gtm-connector", "connect", app, "--state-dir", str(directory),
                                 "--scope", "crm:read"]))
        result["limits"].insert(0, "No grant or config was created. Select each scope explicitly.")
    else:
        import httpx2
        from .client import LocalClient
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
