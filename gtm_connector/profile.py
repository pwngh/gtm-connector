"""The sources one state folder is connected to, saved as profile.json.

`connect` is the only command that changes it. Every other command reads it, so none takes a
source option. It holds no credential: Salesforce stays in the sf CLI, Slack in its private
connection folder.
"""

import json
import os
from pathlib import Path
import re
import stat
import tempfile

from . import strict_json


NAME = "profile.json"
KINDS = ("crm", "calls", "slack")
# The Slack sign-in of a state folder is saved in this folder inside it.
SLACK_CONNECTION = "slack-connection"
DEMO = {kind: {"provider": "demo"} for kind in KINDS}
_ALIAS = re.compile(r"[A-Za-z0-9][A-Za-z0-9._-]{0,79}\Z")
_PROVIDERS = {"crm": ("demo",), "calls": ("demo", "imported"), "slack": ("demo", "slack")}


class ProfileError(ValueError):
    """The profile is unreadable, unsafe, or names a source combination that is not allowed."""


class NotConnected(ProfileError):
    """No CRM is connected, so there is nothing to read an account from."""

    def __init__(self, here=""):
        # `here` repeats the folder's --state-dir in the advice when it is not the default.
        super().__init__(f"No CRM is connected. Run gtm-connector connect salesforce --org ALIAS{here}, "
                         f"or gtm-connector connect demo{here} for invented data.")


def _valid_source(kind, source):
    if not isinstance(source, dict):
        return False
    if kind == "crm" and source.get("provider") == "salesforce":
        return (set(source) == {"provider", "org", "environment"}
                and isinstance(source["org"], str) and _ALIAS.fullmatch(source["org"]) is not None
                and source["environment"] in ("developer", "sandbox", "production"))
    return set(source) == {"provider"} and source["provider"] in _PROVIDERS[kind]


def _valid(sources):
    if (not isinstance(sources, dict) or not set(sources) <= set(KINDS)
            or not all(_valid_source(kind, source) for kind, source in sources.items())):
        return False
    if "crm" not in sources:
        # Calls and Slack belong to a CRM.
        return not sources
    if sources["crm"]["provider"] == "demo":
        # Beside the demo CRM, demo data stands in for a source that is not connected, so both are named.
        return set(sources) == set(KINDS)
    # Beside a real CRM a source is real or not connected. It is never demo data.
    return all(source["provider"] != "demo" for source in sources.values())


def load(directory) -> dict:
    """Return the connected sources by kind. A folder with no profile has none."""
    # os.path.islink, not Path.is_symlink: on Python 3.11 the latter goes through Path.lstat.
    if os.path.islink(directory):
        raise ProfileError("The state folder must not be a symbolic link. Use a private --state-dir.")
    path = Path(directory) / NAME
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return {}
    except OSError:
        raise ProfileError("profile.json cannot be opened. It must be a private regular file that you own, "
                           "not a link.") from None
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077
                or info.st_nlink != 1):
            raise ProfileError("profile.json must be a private regular file that you own, with no other link.")
        document = os.read(descriptor, 4097)
    finally:
        os.close(descriptor)
    try:
        profile = strict_json.loads(document)
    except (ValueError, RecursionError):
        profile = None
    if (len(document) > 4096 or not isinstance(profile, dict) or set(profile) != {"schema_version", "sources"}
            or type(profile["schema_version"]) is not int or profile["schema_version"] != 1
            or not _valid(profile["sources"])):
        raise ProfileError("profile.json is not a profile this version wrote. Reconnect in a fresh --state-dir.")
    return profile["sources"]


def check(sources) -> None:
    if not _valid(sources):
        raise ProfileError("These sources cannot be connected together. Beside Salesforce a source is real or "
                           "not connected, and an org alias holds only letters, digits, and . _ -")


def save(directory, sources) -> None:
    """Replace the whole profile atomically. A failed write leaves the previous one."""
    check(sources)
    directory = Path(directory)
    if directory.is_symlink():
        raise ProfileError("The state folder must not be a symbolic link.")
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    content = json.dumps({"schema_version": 1, "sources": sources}, sort_keys=True, indent=2).encode()
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(dir=directory, prefix=".profile-", delete=False) as output:
            temporary = Path(output.name)
            os.fchmod(output.fileno(), 0o600)
            output.write(content)
        os.replace(temporary, directory / NAME)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def merged(sources, kind, source) -> dict:
    """The profile after one source is connected. Another CRM starts over, because calls and Slack belong to a
    CRM: beside the demo CRM they start as demo data, beside Salesforce as not connected. The same CRM again
    keeps them."""
    if kind != "crm":
        if "crm" not in sources:
            raise NotConnected()
        return {**sources, kind: source}
    if sources.get("crm", {}).get("provider") != source["provider"]:
        return dict(DEMO) if source["provider"] == "demo" else {"crm": source}
    return {**sources, "crm": source}


def connect(directory, kind, source) -> dict:
    """Connect one source and return the new profile."""
    sources = merged(load(directory), kind, source)
    save(directory, sources)
    return sources


def removed(sources, kind) -> dict:
    """The profile after one source is removed. Removing the CRM removes the sources that belong to it, and
    beside the demo CRM a removed source is demo data again."""
    if kind == "crm":
        return {}
    if sources.get("crm", {}).get("provider") == "demo":
        return {**sources, kind: {"provider": "demo"}}
    return {name: source for name, source in sources.items() if name != kind}


def disconnect(directory, kind) -> dict:
    """Remove one source and return the new profile."""
    sources = removed(load(directory), kind)
    save(directory, sources)
    return sources


def backends(sources) -> dict:
    """How the runtime reads each source. A source beside a real CRM is real or not connected."""
    if "crm" not in sources:
        raise NotConnected()

    def backend(kind, real):
        provider = sources.get(kind, {}).get("provider")
        return "none" if provider is None else "fixture" if provider == "demo" else real

    return {"crm": sources["crm"]["provider"], "calls_backend": backend("calls", "imported"),
            "slack_backend": backend("slack", "slack")}
