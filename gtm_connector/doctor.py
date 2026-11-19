"""Check local setup without changing state or disclosing configuration values."""

from contextlib import closing
from datetime import datetime, timezone
import importlib.util
import os
from pathlib import Path
import shlex
import stat
import sys
from urllib.parse import urlsplit

from . import strict_json


_REQUIRED_FILES = ("owner.json", "tls.pem", "tls.key", "crm.sqlite3", "audit.sqlite3")
_DEPENDENCIES = ("mcp", "httpx2", "cryptography", "uvicorn")


def _directory(directory):
    # Normalize ancestor aliases such as macOS /var without accepting a state
    # folder that is itself a symlink. The final component is checked by lstat.
    path = Path(directory).absolute()
    if path.name == "..":
        return path.resolve()
    return path.parent.resolve() / path.name


def _private(path, *, directory=False):
    """Reject symlinks and files readable by another local user."""
    try:
        info = path.lstat()
    except OSError:
        return False
    correct_type = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    return correct_type and info.st_uid == os.getuid() and info.st_mode & 0o077 == 0


def _source_files(owner):
    calls_files = (("calls.sqlite3",) if owner["calls_backend"] == "fixture"
                   else ("imports.sqlite3", "calls-source.json"))
    slack_files = (("slack.sqlite3",) if owner["slack_backend"] == "fixture"
                   else ("slack-source.json", "slack-mappings.json"))
    return calls_files + slack_files + (
        ("crm-connection.json",) if owner["crm_backend"] == "salesforce" else ())


def _read_owner(directory):
    """Return validated local settings; error text never contains their values."""
    directory = _directory(directory)
    if not _private(directory, directory=True) or not _private(directory / "owner.json"):
        raise ValueError("State must contain regular, owner-only files. Run gtm-connector doctor.")
    try:
        descriptor = os.open(directory / "owner.json", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor) as source:
            opened = os.fstat(source.fileno())
            if not stat.S_ISREG(opened.st_mode) or opened.st_uid != os.getuid() or opened.st_mode & 0o077:
                raise ValueError
            content = source.read(65537)
        if len(content) > 65536:
            raise ValueError
        owner = strict_json.loads(content)
        required = {"issuer", "resource", "redirect_uri", "client_id", "owner_secret", "cert_path"}
        if not isinstance(owner, dict) or not required <= owner.keys():
            raise ValueError
        if any(not isinstance(owner[key], str) or not owner[key] for key in required):
            raise ValueError
        for key, path in (("issuer", ""), ("resource", "/mcp")):
            parts = urlsplit(owner[key])
            if (parts.scheme != "https" or parts.hostname != "127.0.0.1"
                    or parts.port is None or not 1 <= parts.port <= 65535
                    or parts.netloc != f"127.0.0.1:{parts.port}" or parts.path != path
                    or parts.query or parts.fragment):
                raise ValueError
        if (owner["redirect_uri"] != "http://127.0.0.1:8767/callback"
                or owner["client_id"] != "gtm-demo-cli"
                or owner["cert_path"] != str(directory / "tls.pem")
                or owner.get("crm_backend") not in ("sqlite", "salesforce")
                or owner.get("calls_backend") not in ("fixture", "imported")
                or owner.get("slack_backend") not in ("fixture", "slack")):
            raise ValueError
        if owner.get("slack_backend") == "slack" and (
                not isinstance(owner.get("slack_connection_dir"), str) or not owner["slack_connection_dir"]
                or not Path(owner["slack_connection_dir"]).is_absolute()):
            raise ValueError
    except (OSError, ValueError, TypeError, KeyError, UnicodeError, RecursionError):
        raise ValueError("Owner configuration is invalid. Run gtm-connector doctor.") from None
    return owner


def _validate_sources(directory, owner):
    if owner["slack_backend"] == "slack":
        from .runtime import inspect_slack_state
        try:
            state = inspect_slack_state(directory)
            if state.crm_backend != owner["crm_backend"]:
                raise ValueError
        except Exception:
            raise ValueError("Slack source state is invalid. Run gtm-connector doctor.") from None
    elif any(os.path.lexists(directory / name) for name in ("slack-source.json", "slack-mappings.json")):
        raise ValueError("Slack source selection is invalid. Run gtm-connector doctor.")
    if owner["calls_backend"] == "imported":
        from .runtime import _inspect_import_state
        try:
            state = _inspect_import_state(directory)
            if state.crm_backend != owner["crm_backend"]:
                raise ValueError
        except Exception:
            raise ValueError("Imported calls state is invalid. Run gtm-connector doctor.") from None
    elif any(os.path.lexists(directory / name) for name in ("calls-source.json", "imports.sqlite3")):
        raise ValueError("Calls source selection is invalid. Run gtm-connector doctor.")


def load_local_owner(directory):
    """Load fixed source-aware files only after validating the private owner document."""
    directory = _directory(directory)
    owner = _read_owner(directory)
    if any(not _private(directory / name) for name in _REQUIRED_FILES + _source_files(owner)):
        raise ValueError("State must contain regular, owner-only files. Run gtm-connector doctor.")
    _validate_sources(directory, owner)
    return owner


def doctor(directory):
    """Inspect private state and GET local metadata; never grant scopes or read sources."""
    directory = _directory(directory)
    serve = shlex.join(["gtm-connector", "serve", "--state-dir", str(directory)])
    fresh = "gtm-connector serve --state-dir var/doctor-new"
    checks = []

    def check(name, passed, message, fix):
        checks.append({"name": name, "status": "pass" if passed else "fail",
                       "message": message, "fix": "" if passed else fix})

    def report():
        return {"status": "passed" if all(row["status"] == "pass" for row in checks) else "failed",
                "checks": checks}

    python_ok = sys.version_info >= (3, 11)
    check("python", python_ok, "Python 3.11 or newer is required.",
          "Install Python 3.11 or newer, then reinstall gtm-connector.")
    dependencies_ok = all(importlib.util.find_spec(name) is not None for name in _DEPENDENCIES)
    check("dependencies", dependencies_ok, "The connector's Python dependencies must be installed.",
          "Reinstall with pipx install --force 'git+https://github.com/pwngh/gtm-connector.git@main'.")
    try:
        import sqlite3
        with closing(sqlite3.connect(":memory:")) as connection:
            sqlite_ok = connection.execute("SELECT 1").fetchone() == (1,)
    except Exception:
        sqlite_ok = False
    check("sqlite", sqlite_ok, "Python must provide a working SQLite connection.",
          "Install Python with SQLite support, then reinstall gtm-connector.")
    state_ok = _private(directory, directory=True)
    check("state_folder", state_ok, "The state folder must exist and belong only to this user.",
          "Start the server with " + serve + ". If the folder is shared or a symlink, use " + fresh + ".")
    if not state_ok:
        return report()
    files_ok = all(_private(directory / name) for name in _REQUIRED_FILES)
    check("state_files", files_ok, "Owner settings, TLS files, and databases must be regular owner-only files.",
          "Stop the server and use a fresh private folder: " + fresh + ".")
    if not files_ok:
        return report()
    try:
        owner = _read_owner(directory)
        if any(not _private(directory / name) for name in _source_files(owner)):
            checks[-1].update(status="fail", fix="Stop the server and use a fresh private folder: " + fresh + ".")
            return report()
        _validate_sources(directory, owner)
    except ValueError:
        check("configuration", False, "Owner settings must use the supported HTTPS loopback endpoints.",
              "Stop the server and keep this folder. Rerun your original serve command with a fresh --state-dir. "
              "Run gtm-connector serve --help to check the flags.")
        return report()
    check("configuration", True, "Owner settings use the supported HTTPS loopback endpoints.", "")
    restart_fix = "Stop and restart the server to generate a new certificate: " + serve + "."
    start_fix = "Start the server in another terminal with " + serve + "."
    if owner.get("crm_backend") == "salesforce":
        start_fix = ("Rerun the original Salesforce serve command for this state folder with the same connection, "
                     "environment, calls backend, and demo binding, if used. "
                     "Run gtm-connector serve --help to check the flags.")
        restart_fix = "Stop the server to renew its certificate. " + start_fix
    elif owner.get("calls_backend") == "imported":
        start_fix = ("Rerun the original serve command for this folder with --calls-backend imported. "
                     "Run gtm-connector serve --help to check the flags.")
        restart_fix = "Stop the server to renew its certificate. " + start_fix
    if owner.get("slack_backend") == "slack":
        start_fix = ("Rerun the original serve command with the same CRM connection, source modes, "
                     "private Slack connection folder, and existing state folder. "
                     "Run gtm-connector serve --help to check the flags.")
        restart_fix = "Stop the server to renew its certificate. " + start_fix
    if not dependencies_ok:
        return report()
    try:
        from cryptography import x509
        cert = x509.load_pem_x509_certificate((directory / "tls.pem").read_bytes())
        now = datetime.now(timezone.utc)
        certificate_ok = cert.not_valid_before_utc <= now < cert.not_valid_after_utc
    except (OSError, ValueError):
        certificate_ok = False
    check("certificate", certificate_ok, "The local TLS certificate must be valid at the current time.",
          restart_fix)
    if not certificate_ok:
        return report()
    try:
        from .client import LocalClient
        with LocalClient(owner) as client:
            # Metadata GETs do not create tokens, audit receipts, or source reads.
            authority = client._object(client._http.get(
                client.issuer + "/.well-known/oauth-authorization-server", timeout=2), "Authorization metadata")
            resource = client._object(client._http.get(
                client._resource_metadata_url, timeout=2), "Resource metadata")
            services_ok = (authority.get("issuer") == owner["issuer"]
                           and resource.get("resource") == owner["resource"]
                           and resource.get("authorization_servers") == [owner["issuer"]])
    except Exception:
        # Report no HTTP exception text or configuration values.
        services_ok = False
    check("services", services_ok, "Both local HTTPS services must return matching metadata.",
          start_fix)
    return report()
