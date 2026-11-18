"""Load invented demo assets and bind their local sources to an owner's import."""

from copy import deepcopy
from datetime import date, datetime
from functools import lru_cache
import hashlib
from importlib.resources import files
import json
import os
from pathlib import Path
import re
import stat
from tempfile import NamedTemporaryFile
from typing import Any


_FILES = ("plan.json", "accounts.json", "manifest.json")
_PREFIXES = {"Account": "001", "Contact": "003", "Opportunity": "006", "Contract": "800"}
_MAX_BYTES = 128 * 1024
_NATIVE_ID = re.compile(r"[A-Za-z0-9]{18}\Z")
_KIT_ID = "gtm-salesforce-demo-v1"


class DemoBindingError(ValueError):
    """A fixed diagnostic that never includes imported IDs or file content."""

    code = "salesforce_demo_binding_invalid"


def _fail():
    raise DemoBindingError("The demo binding or import receipt is invalid. Export the kit and bind a complete import.")


def _unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            _fail()
        result[key] = value
    return result


def _parse(raw):
    if not isinstance(raw, bytes) or len(raw) > _MAX_BYTES:
        _fail()
    try:
        return json.loads(raw, object_pairs_hook=_unique, parse_constant=lambda _: _fail())
    except (ValueError, UnicodeError, RecursionError):
        _fail()


def _canonical(document):
    return (json.dumps(document, sort_keys=True, ensure_ascii=True, allow_nan=False,
                       separators=(",", ":")) + "\n").encode()


def _hash(raw):
    return hashlib.sha256(raw).hexdigest()


def _check_manifest(manifest, tree, plan):
    if (not isinstance(manifest, dict) or type(manifest.get("schema_version")) is not int
            or manifest["schema_version"] != 1 or manifest.get("kit_id") != _KIT_ID
            or manifest.get("as_of") != "2027-01-15"
            or plan != [{"sobject": "Account", "files": ["accounts.json"]}]
            or not isinstance(tree, dict) or set(tree) != {"records"}
            or not isinstance(tree["records"], list) or len(tree["records"]) != 3):
        _fail()
    declared = manifest.get("records")
    if not isinstance(declared, list) or len(declared) != 12:
        _fail()
    observed = []
    numbers = set()
    for number, root in enumerate(tree["records"], 1):
        account_number = f"GTM-DEMO-{number:03}"
        if not isinstance(root, dict) or root.get("AccountNumber") != account_number:
            _fail()
        numbers.add(account_number)
        rows = [("Account", root)]
        for object_name, relationship in (("Contact", "Contacts"), ("Opportunity", "Opportunities"),
                                          ("Contract", "Contracts")):
            children = root.get(relationship)
            if (not isinstance(children, dict) or set(children) != {"records"}
                    or not isinstance(children["records"], list) or len(children["records"]) != 1):
                _fail()
            rows.append((object_name, children["records"][0]))
        for object_name, row in rows:
            ref = f"{object_name}Ref{number}"
            if (not isinstance(row, dict) or row.get("attributes") != {"type": object_name, "referenceId": ref}
                    or "Id" in row or "AccountId" in row):
                _fail()
            if object_name == "Contract" and (row.get("Status") != "Draft" or "EndDate" in row):
                _fail()
            observed.append({"ref_id": ref, "object": object_name,
                             "id_prefix": _PREFIXES[object_name], "account_number": account_number})
    if declared != observed:
        _fail()
    for name, prefix, fields, timestamp in (
        ("notes", "note", {"id", "account_number", "called_at", "body"}, "called_at"),
        ("messages", "msg", {"id", "account_number", "sent_at", "body", "channel", "author"}, "sent_at"),
    ):
        rows = manifest.get(name)
        if not isinstance(rows, list) or len(rows) != 3:
            _fail()
        if {row.get("account_number") for row in rows if isinstance(row, dict)} != numbers:
            _fail()
        for number, row in enumerate(rows, 1):
            if (not isinstance(row, dict) or set(row) != fields or row["id"] != f"{prefix}-demo-{number:03}"
                    or row["account_number"] != f"GTM-DEMO-{number:03}"
                    or any(not isinstance(value, str) or len(value) > 4096 for value in row.values())):
                _fail()
            try:
                if datetime.fromisoformat(row[timestamp]).tzinfo is None:
                    _fail()
            except ValueError:
                _fail()
    if date.fromisoformat(manifest["as_of"]).isoformat() != manifest["as_of"]:
        _fail()


@lru_cache(maxsize=1)
def _assets():
    try:
        root = files("gtm_connector").joinpath("demo_kit")
        content = {name: root.joinpath(name).read_bytes() for name in _FILES}
        manifest, tree, plan = (_parse(content[name]) for name in ("manifest.json", "accounts.json", "plan.json"))
        if manifest.get("files_sha256") != {name: _hash(content[name]) for name in ("plan.json", "accounts.json")}:
            _fail()
        _check_manifest(manifest, tree, plan)
        return content
    except (OSError, AttributeError, TypeError, ValueError, KeyError):
        _fail()


def kit_files():
    """Return copies of the three validated, canonical packaged asset bytes."""
    return dict(_assets())


def kit_document(name):
    """Read a fresh document from the fixed plan, accounts, or manifest asset."""
    if not isinstance(name, str) or name not in ("plan", "accounts", "manifest"):
        _fail()
    return _parse(_assets()[name + ".json"])


def load_manifest():
    return kit_document("manifest")


def load_tree():
    return kit_document("accounts")


def _valid_id(identifier, prefix):
    return isinstance(identifier, str) and _NATIVE_ID.fullmatch(identifier) and identifier.startswith(prefix)


def bind_import(receipt):
    """Validate a complete sf --json import receipt without trusting its type column."""
    if (not isinstance(receipt, dict) or type(receipt.get("status")) is not int or receipt["status"] != 0
            or not isinstance(receipt.get("result"), list) or len(receipt["result"]) != 12):
        _fail()
    manifest = load_manifest()
    expected = {row["ref_id"]: row for row in manifest["records"]}
    actual = {}
    identifiers = set()
    for row in receipt["result"]:
        if (not isinstance(row, dict) or set(row) != {"refId", "id", "type"}
                or not isinstance(row["refId"], str) or row["refId"] not in expected
                or row["refId"] in actual or not isinstance(row["type"], str)):
            _fail()
        declaration = expected[row["refId"]]
        if (not _valid_id(row["id"], declaration["id_prefix"]) or row["id"] in identifiers
                or row["type"] not in ("Account", declaration["object"])):
            _fail()
        actual[row["refId"]] = row["id"]
        identifiers.add(row["id"])
    records = [{"ref_id": row["ref_id"], "object": row["object"], "id": actual[row["ref_id"]],
                "account_number": row["account_number"]} for row in manifest["records"]]
    return {"schema_version": 1, "kit_id": manifest["kit_id"],
            "manifest_sha256": _hash(_assets()["manifest.json"]),
            "accounts": {row["account_number"]: "acc-sf-" + row["id"] for row in records
                         if row["object"] == "Account"}, "records": records}


class BoundDemo:
    """An explicit owner assertion relating invented fixture keys to imported IDs."""

    def __init__(self, document):
        manifest = load_manifest()
        if (not isinstance(document, dict)
                or set(document) != {"schema_version", "kit_id", "manifest_sha256", "accounts", "records"}
                or type(document["schema_version"]) is not int or document["schema_version"] != 1
                or document["kit_id"] != manifest["kit_id"]
                or document["manifest_sha256"] != _hash(_assets()["manifest.json"])
                or not isinstance(document["accounts"], dict) or not isinstance(document["records"], list)
                or len(document["records"]) != 12):
            _fail()
        receipt: dict[str, Any] = {"status": 0, "result": []}
        declarations = {row["ref_id"]: row for row in manifest["records"]}
        for row in document["records"]:
            if (not isinstance(row, dict) or set(row) != {"ref_id", "object", "id", "account_number"}
                    or not isinstance(row["ref_id"], str) or row["ref_id"] not in declarations):
                _fail()
            expected = declarations[row["ref_id"]]
            if row["object"] != expected["object"] or row["account_number"] != expected["account_number"]:
                _fail()
            receipt["result"].append({"refId": row["ref_id"], "type": row["object"], "id": row["id"]})
        normalized = bind_import(receipt)
        if document["accounts"] != normalized["accounts"]:
            _fail()
        self._document = normalized
        self._manifest = manifest
        self.kit_id = manifest["kit_id"]
        self.as_of = manifest["as_of"]
        self.manifest_sha256 = normalized["manifest_sha256"]
        self.binding_sha256 = _hash(_canonical(normalized))

    @property
    def accounts(self):
        return dict(self._document["accounts"])

    @property
    def records(self):
        return deepcopy(self._document["records"])

    def validate_account(self, account):
        """Check the explicit binding against an already authorized CRM result."""
        if not isinstance(account, dict):
            _fail()
        identifier, number = account.get("id"), account.get("account_number")
        if not isinstance(identifier, str) or (number is not None and not isinstance(number, str)):
            _fail()
        accounts = self._document["accounts"]
        expected = {value: key for key, value in accounts.items()}
        if ((identifier in expected and number != expected[identifier])
                or (isinstance(number, str) and number in accounts and identifier != accounts[number])):
            raise DemoBindingError("The demo AccountNumber does not match its import binding. "
                                   "Rebind the correct import.")

    def _seed(self, connection, table, fields, source):
        expected = [(row["id"], self._document["accounts"][row["account_number"]])
                    for row in self._manifest[source]]
        connection.execute("SAVEPOINT seed_salesforce_demo")
        try:
            existing = list(connection.execute(f"SELECT id, account_id FROM {table} ORDER BY id"))
            if existing:
                if [tuple(row) for row in existing] != sorted(expected):
                    raise DemoBindingError("The source contains another or incomplete demo. Use a fresh state folder.")
            else:
                for row in self._manifest[source]:
                    values = {**row, "account_id": self._document["accounts"][row["account_number"]]}
                    connection.execute(f"INSERT INTO {table} ({', '.join(fields)}) "
                                       f"VALUES ({', '.join('?' for _ in fields)})",
                                       [values[field] for field in fields])
            connection.execute("RELEASE SAVEPOINT seed_salesforce_demo")
        except Exception:
            connection.execute("ROLLBACK TO SAVEPOINT seed_salesforce_demo")
            connection.execute("RELEASE SAVEPOINT seed_salesforce_demo")
            raise

    def seed_calls(self, connection):
        self._seed(connection, "call_notes", ("id", "account_id", "called_at", "body"), "notes")

    def seed_slack(self, connection):
        self._seed(connection, "messages", ("id", "account_id", "channel", "author", "sent_at", "body"), "messages")


def _private_file_info(info):
    return (stat.S_ISREG(info.st_mode) and stat.S_IMODE(info.st_mode) == 0o600
            and info.st_uid == os.getuid() and info.st_nlink == 1 and info.st_size <= _MAX_BYTES)


def load_binding(path):
    """Read only a bounded regular mode-0600 binding owned by this user."""
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as source:
            if not _private_file_info(os.fstat(source.fileno())):
                _fail()
            raw = source.read(_MAX_BYTES + 1)
        return BoundDemo(_parse(raw))
    except (OSError, TypeError, ValueError):
        raise DemoBindingError("The demo binding must be a valid private regular file. "
                               "Bind the import again.") from None


def write_binding(path, document):
    """Atomically replace a private binding without following a destination symlink."""
    bound = BoundDemo(document)
    raw = _canonical(bound._document)
    destination = Path(path)
    temporary = None
    try:
        destination.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        parent = destination.parent.stat()
        if not stat.S_ISDIR(parent.st_mode) or parent.st_uid != os.getuid() or stat.S_IMODE(parent.st_mode) & 0o077:
            _fail()
        try:
            info = destination.lstat()
        except FileNotFoundError:
            info = None
        if info is not None and not _private_file_info(info):
            _fail()
        with NamedTemporaryFile(mode="wb", dir=destination.parent, prefix=".demo-binding-", delete=False) as output:
            temporary = Path(output.name)
            os.fchmod(output.fileno(), 0o600)
            output.write(raw)
        os.replace(temporary, destination)
    except (OSError, TypeError, ValueError):
        raise DemoBindingError("The demo binding needs a private folder and regular file. "
                               "Choose a new binding path.") from None
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)
