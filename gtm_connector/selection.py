"""An owner-written selection of the accounts and Slack threads a remote caller may read."""

from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import stat


MAX_SELECTION_BYTES = 65536
MAX_ACCOUNTS = 100
_ACCOUNT_PATTERNS = {
    "sqlite": re.compile(r"acc-[0-9]{3}\Z"),
    "salesforce": re.compile(r"acc-sf-[A-Za-z0-9]{18}\Z"),
}
_SHAPE = ('Use {"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": true}]} '
          "with 1 to 100 distinct accounts.")


class SelectionError(ValueError):
    """The selection file is missing, unsafe, or not in the documented shape."""


@dataclass(frozen=True)
class Selection:
    """An immutable, validated selection. A running server never reloads it."""

    crm_backend: str
    accounts: frozenset
    slack_accounts: frozenset

    def includes(self, account_id) -> bool:
        return isinstance(account_id, str) and account_id in self.accounts

    def includes_slack(self, account_id) -> bool:
        return isinstance(account_id, str) and account_id in self.slack_accounts

    @property
    def account_ids(self) -> tuple:
        return tuple(sorted(self.accounts))


def _unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise SelectionError("The selection file repeats a JSON key. " + _SHAPE)
        result[key] = value
    return result


def _reject_constant(_):
    raise SelectionError("The selection file contains a non-finite number. " + _SHAPE)


def parse_selection(document: bytes, *, crm_backend: str) -> Selection:
    """Validate the exact documented shape. Nothing here opens a source or credential."""
    if crm_backend not in _ACCOUNT_PATTERNS:
        raise SelectionError("Selections support the sqlite and salesforce CRM backends.")
    if len(document) > MAX_SELECTION_BYTES:
        raise SelectionError(f"The selection file is larger than {MAX_SELECTION_BYTES} bytes.")
    try:
        value = json.loads(document.decode("utf-8"), object_pairs_hook=_unique, parse_constant=_reject_constant)
    except SelectionError:
        raise
    except (UnicodeError, ValueError, RecursionError):
        raise SelectionError("The selection file is not valid UTF-8 JSON. " + _SHAPE) from None
    if (not isinstance(value, dict) or set(value) != {"schema_version", "accounts"}
            or type(value["schema_version"]) is not int or value["schema_version"] != 1
            or not isinstance(value["accounts"], list)
            or not 1 <= len(value["accounts"]) <= MAX_ACCOUNTS):
        raise SelectionError("The selection file has an unsupported shape. " + _SHAPE)
    pattern = _ACCOUNT_PATTERNS[crm_backend]
    accounts, slack = set(), set()
    for entry in value["accounts"]:
        if (not isinstance(entry, dict) or set(entry) != {"account_id", "slack"}
                or not isinstance(entry["account_id"], str) or type(entry["slack"]) is not bool):
            raise SelectionError("Each selected account needs exactly account_id and slack. " + _SHAPE)
        account_id = entry["account_id"]
        if not pattern.fullmatch(account_id):
            example = "acc-sf- followed by its 18-character ID" if crm_backend == "salesforce" else "acc-007"
            raise SelectionError(f"A selected account ID does not match the {crm_backend} CRM. Use {example}.")
        if account_id in accounts:
            raise SelectionError("The selection file lists an account twice. " + _SHAPE)
        accounts.add(account_id)
        if entry["slack"]:
            slack.add(account_id)
    return Selection(crm_backend, frozenset(accounts), frozenset(slack))


def load_selection(path, *, crm_backend: str) -> Selection:
    """Read one owner-written file that only its owner can change."""
    path = Path(path).expanduser()
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    except FileNotFoundError:
        raise SelectionError("The selection file was not found.") from None
    except OSError:
        raise SelectionError("The selection file cannot be opened. It must be a regular file, "
                             "not a symbolic link.") from None
    try:
        info = os.fstat(descriptor)
        if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid()
                or info.st_mode & 0o022):
            raise SelectionError("The selection file must be a regular file you own that others cannot write.")
        chunks, size = [], 0
        while True:
            chunk = os.read(descriptor, 16384)
            if not chunk:
                break
            size += len(chunk)
            if size > MAX_SELECTION_BYTES:
                raise SelectionError(f"The selection file is larger than {MAX_SELECTION_BYTES} bytes.")
            chunks.append(chunk)
    finally:
        os.close(descriptor)
    return parse_selection(b"".join(chunks), crm_backend=crm_backend)
