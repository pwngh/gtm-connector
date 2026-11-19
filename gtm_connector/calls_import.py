"""Bounded local transcript imports and immutable selected call evidence.

The runtime owns the state lock and source selection. This module contacts no
provider. Source reads reparse stored bytes; saved-record validation checks only
the selected snapshot and does not authenticate unseen original bytes.
"""

from contextlib import contextmanager
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import stat
from typing import Any

from . import strict_json


MAX_RAW_BYTES = 2 * 1024 * 1024
MAX_UNITS = 10_000
MAX_QUOTE_BYTES = 16 * 1024
MAX_RETURNED_UNITS = 256
MAX_RECORD_BYTES = 128 * 1024
DEFAULT_UNITS = 20
CONTEXT_UNITS = 1
PARSER_VERSION = 1
RECORD_VERSION = 1
_PROVENANCE_RESERVE = 8192
_HEX = re.compile(r"[0-9a-f]{64}\Z")
_NOTE_ID = re.compile(r"note-[0-9a-f]{64}\Z")
_CONTROLS = re.compile(r"[\x00-\x08\x0b\x0c\x0e-\x1f\x7f-\x9f]")
_STAMP = r"(?:(?:[0-9]{2,9}):)?[0-5][0-9]:[0-5][0-9]\.[0-9]{3}"
_TIMING = re.compile(rf"^({_STAMP})[ \t]+-->[ \t]+({_STAMP})(?:[ \t]+([^\r\n]*))?$")
_UNITS_KEYS = {"index", "text", "cue_id", "start_ms", "end_ms"}
_MANIFEST_KEYS = {"schema_version", "source", "format", "parser_version", "content_sha256",
                  "raw_byte_count", "association", "association_generation", "called_at", "imported_at",
                  "selection_mode", "unit_kind", "requested_range", "cited_range", "total_units", "units"}
_RECORD_KEYS = {"id", "account_id", "record_kind", "record_version", "called_at", "body", "manifest"}
_PUBLIC_ASSOCIATION_KEYS = {"crm_binding", "account_id", "method", "resolution_receipt_id"}
_CATEGORIES = {
    "calls_import_invalid": "usage", "calls_import_too_large": "usage", "calls_excerpt_too_large": "usage",
    "calls_import_not_found": "usage", "calls_account_not_found": "usage",
    "calls_not_imported": "refusal", "calls_reassign_required": "refusal",
    "calls_association_changed": "refusal", "calls_source_mismatch": "refusal",
    "calls_account_ambiguous": "refusal", "calls_import_corrupt": "check",
    "calls_association_invalid": "check", "calls_association_failed": "check",
    "calls_source_unavailable": "upstream", "calls_import_io": "io",
}
ERROR_CODES = frozenset(_CATEGORIES)


class CallsImportError(RuntimeError):
    """A fixed diagnostic that never includes private source or provider details."""

    def __init__(self, code: str):
        if code not in _CATEGORIES:
            code = "calls_import_corrupt"
        self.code = code
        self.category = _CATEGORIES[code]
        self.exit_code = {"usage": 2, "refusal": 3, "check": 4, "upstream": 5, "io": 1}[self.category]
        super().__init__(code)


def _canonical(value, *, ascii_only=False):
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=ascii_only, allow_nan=False).encode("utf-8")


def _digest(value):
    return hashlib.sha256(_canonical(value)).hexdigest()


def _json(value):
    try:
        return strict_json.loads(value)
    except (TypeError, ValueError, UnicodeError, RecursionError):
        raise CallsImportError("calls_import_corrupt") from None


def _int(value, minimum, maximum):
    return type(value) is int and minimum <= value <= maximum


def _text(value, *, maximum=MAX_RAW_BYTES, nonempty=False):
    if not isinstance(value, str) or (nonempty and not value.strip()) or _CONTROLS.search(value):
        return False
    try:
        return len(value.encode("utf-8")) <= maximum
    except UnicodeError:
        return False


def _time(value, *, nullable=False):
    if value is None:
        return nullable
    if not isinstance(value, str) or len(value) > 32 or not value.endswith("Z"):
        return False
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return (parsed.tzinfo is not None and parsed.utcoffset() == timezone.utc.utcoffset(parsed)
                and parsed.isoformat().replace("+00:00", "Z") == value)
    except (ValueError, OverflowError):
        return False


def _valid_binding(binding):
    return (isinstance(binding, dict)
            and set(binding) == {"schema_version", "crm_backend", "identity_sha256"}
            and type(binding["schema_version"]) is int and binding["schema_version"] == 1
            and binding["crm_backend"] in ("sqlite", "salesforce")
            and isinstance(binding["identity_sha256"], str) and _HEX.fullmatch(binding["identity_sha256"]))


def _valid_account(account_id, binding):
    pattern = r"acc-[0-9]{3}" if binding["crm_backend"] == "sqlite" else r"acc-sf-[A-Za-z0-9]{18}"
    return isinstance(account_id, str) and re.fullmatch(pattern, account_id) is not None


def _validate_public_association(association):
    """Validate call provenance without accepting a private CRM selector."""
    if not isinstance(association, dict) or set(association) != _PUBLIC_ASSOCIATION_KEYS:
        raise CallsImportError("calls_association_invalid")
    binding = association["crm_binding"]
    if not _valid_binding(binding) or not _valid_account(association["account_id"], binding):
        raise CallsImportError("calls_association_invalid")
    if not _text(association["resolution_receipt_id"], maximum=128, nonempty=True):
        raise CallsImportError("calls_association_invalid")
    if (association["method"] not in ("account_id", "account_number")
            or (association["method"] == "account_number" and binding["crm_backend"] != "salesforce")):
        raise CallsImportError("calls_association_invalid")


def _validate_association(association):
    """Validate the owner-only resolution used for private number associations."""
    if not isinstance(association, dict) or set(association) != _PUBLIC_ASSOCIATION_KEYS | {"account_number"}:
        raise CallsImportError("calls_association_invalid")
    _validate_public_association({key: association[key] for key in _PUBLIC_ASSOCIATION_KEYS})
    number = association["account_number"]
    if association["method"] == "account_id":
        valid = number is None
    elif association["method"] == "account_number":
        valid = (_text(number, maximum=160, nonempty=True)
                 and len(number) <= 40 and not any(char in number for char in "\r\n\t"))
    else:
        valid = False
    if not valid:
        raise CallsImportError("calls_association_invalid")


def _lines(text):
    lines = re.split(r"\r\n|\r|\n", text)
    if text.endswith(("\r", "\n")):
        lines.pop()
    return lines


def _milliseconds(value):
    fields = value.split(":")
    seconds, millis = fields[-1].split(".")
    hours = int(fields[0]) if len(fields) == 3 else 0
    return ((hours * 60 + int(fields[-2])) * 60 + int(seconds)) * 1000 + int(millis)


def parse_transcript(raw: bytes, *, format: str) -> dict[str, Any]:
    """Parse a strict transcript subset, preserving the exact original bytes."""
    if not isinstance(raw, bytes) or format not in ("txt", "vtt"):
        raise CallsImportError("calls_import_invalid")
    if len(raw) > MAX_RAW_BYTES:
        raise CallsImportError("calls_import_too_large")
    try:
        text = raw.decode("utf-8")
    except UnicodeError:
        raise CallsImportError("calls_import_invalid") from None
    if not _text(text, nonempty=True):
        raise CallsImportError("calls_import_invalid")
    text = text.removeprefix("\ufeff")
    lines = _lines(text)
    units = []
    if format == "txt":
        if len(lines) > MAX_UNITS:
            raise CallsImportError("calls_import_too_large")
        units = [{"index": index, "text": line, "cue_id": None, "start_ms": None, "end_ms": None}
                 for index, line in enumerate(lines, 1)]
    else:
        if (len(lines) < 2 or re.fullmatch(r"WEBVTT(?:[ \t][^\r\n]*)?", lines[0]) is None
                or "-->" in lines[0] or lines[1] != ""):
            raise CallsImportError("calls_import_invalid")
        blocks: list[list[str]] = []
        for line in lines[2:]:
            if line == "":
                if blocks and blocks[-1]:
                    blocks.append([])
            else:
                if not blocks:
                    blocks.append([])
                blocks[-1].append(line)
        identifiers = set()
        previous_start = -1
        for block in blocks:
            if not block:
                continue
            first = block[0]
            comment = first == "NOTE" or first.startswith(("NOTE ", "NOTE\t"))
            display = first.rstrip(" \t") in ("STYLE", "REGION")
            if comment or display:
                if any("-->" in line for line in block) or (display and units):
                    raise CallsImportError("calls_import_invalid")
                continue
            cue_id = None
            timing = _TIMING.fullmatch(first)
            payload = block[1:]
            if timing is None:
                if "-->" in first or len(block) < 2:
                    raise CallsImportError("calls_import_invalid")
                cue_id, timing, payload = first, _TIMING.fullmatch(block[1]), block[2:]
                if not _text(cue_id, nonempty=True) or cue_id in identifiers:
                    raise CallsImportError("calls_import_invalid")
                identifiers.add(cue_id)
            if timing is None or any("-->" in line for line in payload):
                raise CallsImportError("calls_import_invalid")
            start, end = _milliseconds(timing[1]), _milliseconds(timing[2])
            if start < previous_start or end <= start or (timing[3] is not None and "-->" in timing[3]):
                raise CallsImportError("calls_import_invalid")
            previous_start = start
            units.append({"index": len(units) + 1, "text": "\n".join(payload), "cue_id": cue_id,
                          "start_ms": start, "end_ms": end})
            if len(units) > MAX_UNITS:
                raise CallsImportError("calls_import_too_large")
    if not units or not any(unit["text"].strip() for unit in units):
        raise CallsImportError("calls_import_invalid")
    return {"raw": raw, "format": format, "parser_version": PARSER_VERSION,
            "content_sha256": hashlib.sha256(raw).hexdigest(), "units": tuple(units)}


def _range(first, last):
    return {"first": first, "last": last}


def _excerpt(parsed, first, last, mode):
    total = len(parsed["units"])
    cited_first, cited_last = max(1, first - CONTEXT_UNITS), min(total, last + CONTEXT_UNITS)
    selected = deepcopy(list(parsed["units"][cited_first - 1:cited_last]))
    return {"unit_kind": "lines" if parsed["format"] == "txt" else "cues", "selection_mode": mode,
            "requested_range": _range(first, last), "cited_range": _range(cited_first, cited_last),
            "total_units": total, "units": selected, "body": "\n".join(unit["text"] for unit in selected)}


def _excerpt_fits(excerpt):
    return (len(excerpt["units"]) <= MAX_RETURNED_UNITS
            and len(excerpt["body"].encode("utf-8")) <= MAX_QUOTE_BYTES
            and len(_canonical({"body": excerpt["body"], "units": excerpt["units"]}, ascii_only=True))
            <= MAX_RECORD_BYTES - _PROVENANCE_RESERVE)


def select_excerpt(parsed: dict, *, requested_range: tuple[int, int] | None = None) -> dict[str, Any]:
    """Select complete units with one adjacent context unit on each side."""
    try:
        total = len(parsed["units"])
        if parsed["format"] not in ("txt", "vtt") or not _int(total, 1, MAX_UNITS):
            raise CallsImportError("calls_import_invalid")
        if requested_range is not None:
            if (not isinstance(requested_range, tuple) or len(requested_range) != 2
                    or not all(_int(value, 1, total) for value in requested_range)
                    or requested_range[0] > requested_range[1]):
                raise CallsImportError("calls_import_invalid")
            excerpt = _excerpt(parsed, *requested_range, "explicit")
            if not _excerpt_fits(excerpt):
                raise CallsImportError("calls_excerpt_too_large")
            return excerpt
        for last in range(min(DEFAULT_UNITS, total), 0, -1):
            excerpt = _excerpt(parsed, 1, last, "default")
            if _excerpt_fits(excerpt):
                return excerpt
        raise CallsImportError("calls_excerpt_too_large")
    except (KeyError, TypeError, UnicodeError):
        raise CallsImportError("calls_import_invalid") from None


def _manifest_id(manifest):
    return "note-" + _digest(manifest)


def _make_record(manifest):
    return {"id": _manifest_id(manifest), "account_id": manifest["association"]["account_id"],
            "record_kind": "imported_transcript", "record_version": RECORD_VERSION,
            "called_at": manifest["called_at"], "body": "\n".join(unit["text"] for unit in manifest["units"]),
            "manifest": deepcopy(manifest)}


def validate_imported_record(record: dict, *, account_id: str, crm_binding: dict) -> dict[str, Any]:
    """Validate selected snapshot consistency, without reading unseen original bytes."""
    try:
        if not isinstance(record, dict) or set(record) not in (_RECORD_KEYS, _RECORD_KEYS | {"record_ref"}):
            raise ValueError
        if not _valid_binding(crm_binding) or not _valid_account(account_id, crm_binding):
            raise ValueError
        manifest = record["manifest"]
        if not isinstance(manifest, dict) or set(manifest) != _MANIFEST_KEYS:
            raise ValueError
        if (type(manifest["schema_version"]) is not int or manifest["schema_version"] != 1
                or type(manifest["parser_version"]) is not int or manifest["parser_version"] != PARSER_VERSION
                or manifest["source"] != "user_supplied_export" or manifest["format"] not in ("txt", "vtt")
                or not isinstance(manifest["content_sha256"], str)
                or not _HEX.fullmatch(manifest["content_sha256"])
                or not _int(manifest["raw_byte_count"], 1, MAX_RAW_BYTES)
                or not _int(manifest["association_generation"], 1, 2**63 - 1)
                or not _time(manifest["called_at"], nullable=True) or not _time(manifest["imported_at"])):
            raise ValueError
        _validate_public_association(manifest["association"])
        if manifest["association"]["crm_binding"] != crm_binding or manifest["association"]["account_id"] != account_id:
            raise ValueError
        total = manifest["total_units"]
        if not _int(total, 1, MAX_UNITS) or manifest["selection_mode"] not in ("default", "explicit"):
            raise ValueError
        for name in ("requested_range", "cited_range"):
            span = manifest[name]
            if (not isinstance(span, dict) or set(span) != {"first", "last"}
                    or not all(_int(value, 1, total) for value in span.values()) or span["first"] > span["last"]):
                raise ValueError
        requested, cited = manifest["requested_range"], manifest["cited_range"]
        if cited != _range(max(1, requested["first"] - 1), min(total, requested["last"] + 1)):
            raise ValueError
        if manifest["selection_mode"] == "default" and (requested["first"] != 1 or requested["last"] > DEFAULT_UNITS):
            raise ValueError
        if manifest["unit_kind"] != ("lines" if manifest["format"] == "txt" else "cues"):
            raise ValueError
        units = manifest["units"]
        if (not isinstance(units, list) or not 1 <= len(units) <= MAX_RETURNED_UNITS
                or len(units) != cited["last"] - cited["first"] + 1):
            raise ValueError
        previous_start, identifiers = -1, set()
        for index, unit in enumerate(units, cited["first"]):
            if (not isinstance(unit, dict) or set(unit) != _UNITS_KEYS or type(unit["index"]) is not int
                    or unit["index"] != index or not _text(unit["text"])):
                raise ValueError
            cue_id = unit["cue_id"]
            if manifest["format"] == "txt":
                if cue_id is not None or unit["start_ms"] is not None or unit["end_ms"] is not None:
                    raise ValueError
                if any(char in unit["text"] for char in "\r\n"):
                    raise ValueError
            else:
                if (not _int(unit["start_ms"], 0, 3_600_000_000_000_000)
                        or not _int(unit["end_ms"], 1, 3_600_000_000_000_000)
                        or unit["end_ms"] <= unit["start_ms"] or unit["start_ms"] < previous_start
                        or "-->" in unit["text"] or "\r" in unit["text"]):
                    raise ValueError
                previous_start = unit["start_ms"]
                if cue_id is not None:
                    if (not _text(cue_id, nonempty=True) or any(part in cue_id for part in ("\r", "\n", "-->"))
                            or cue_id in identifiers):
                        raise ValueError
                    identifiers.add(cue_id)
        expected = _make_record(manifest)
        if any(record[key] != value or type(record[key]) is not type(value) for key, value in expected.items()):
            raise ValueError
        reference = "calls/call_notes/" + expected["id"]
        if "record_ref" in record and record["record_ref"] != reference:
            raise ValueError
        if (len(expected["body"].encode("utf-8")) > MAX_QUOTE_BYTES
                or len(_canonical({**expected, "record_ref": reference}, ascii_only=True)) > MAX_RECORD_BYTES):
            raise ValueError
        return deepcopy(record)
    except (CallsImportError, KeyError, TypeError, ValueError, UnicodeError, OverflowError, RecursionError):
        raise CallsImportError("calls_import_corrupt") from None


_SCHEMA = {
    "import_meta": ("binding",),
    "contents": ("content_sha256", "raw"),
    "revisions": ("import_id", "request_key", "content_sha256", "account_id", "manifest"),
    "content_associations": ("content_sha256", "account_id", "generation"),
    "number_associations": ("account_number", "account_id"),
    "active_imports": ("account_id", "import_id"),
}
_CREATE = (
    "CREATE TABLE import_meta (binding TEXT NOT NULL)",
    "CREATE TABLE contents (content_sha256 TEXT PRIMARY KEY NOT NULL, raw BLOB NOT NULL)",
    "CREATE TABLE revisions (import_id TEXT PRIMARY KEY NOT NULL, request_key TEXT UNIQUE NOT NULL, "
    "content_sha256 TEXT NOT NULL REFERENCES contents(content_sha256), "
    "account_id TEXT NOT NULL, manifest TEXT NOT NULL)",
    "CREATE TABLE content_associations (content_sha256 TEXT PRIMARY KEY NOT NULL REFERENCES contents(content_sha256), "
    "account_id TEXT NOT NULL, generation INTEGER NOT NULL CHECK(generation > 0))",
    "CREATE TABLE number_associations (account_number TEXT PRIMARY KEY NOT NULL, account_id TEXT NOT NULL)",
    "CREATE TABLE active_imports (account_id TEXT PRIMARY KEY NOT NULL, "
    "import_id TEXT UNIQUE NOT NULL REFERENCES revisions(import_id))",
)


def _private(path, *, directory=False):
    info = path.lstat()
    expected = stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode)
    if (not expected or info.st_uid != os.getuid() or info.st_mode & 0o077
            or (not directory and info.st_nlink != 1)):
        raise CallsImportError("calls_source_mismatch")
    return info


def _paths(path):
    _private(path.parent, directory=True)
    original = _private(path)
    for suffix in ("-journal", "-wal", "-shm"):
        sidecar = path.with_name(path.name + suffix)
        if os.path.lexists(sidecar):
            _private(sidecar)
            if suffix != "-journal":
                raise CallsImportError("calls_source_mismatch")
    return original


def _validate_schema(connection, binding):
    if not _valid_binding(binding):
        raise CallsImportError("calls_source_mismatch")
    objects = connection.execute("SELECT name, type FROM sqlite_master WHERE name NOT LIKE 'sqlite_%'").fetchall()
    if {row[0]: row[1] for row in objects} != {name: "table" for name in _SCHEMA}:
        raise CallsImportError("calls_import_corrupt")
    for name, columns in _SCHEMA.items():
        if tuple(row[1] for row in connection.execute(f"PRAGMA table_info({name})")) != columns:
            raise CallsImportError("calls_import_corrupt")
    if connection.execute("PRAGMA user_version").fetchone()[0] != 1:
        raise CallsImportError("calls_import_corrupt")
    rows = connection.execute("SELECT binding FROM import_meta LIMIT 2").fetchall()
    if len(rows) != 1 or len(rows[0][0]) > 4096 or _json(rows[0][0]) != binding:
        raise CallsImportError("calls_source_mismatch")
    if connection.execute("PRAGMA journal_mode").fetchone()[0] != "delete":
        raise CallsImportError("calls_source_mismatch")


@contextmanager
def _database(path, *, binding=None, writable=False, check_schema=True):
    connection = None
    try:
        path = Path(path)
        original = _paths(path)
        uri = path.absolute().as_uri() + ("?mode=rw" if writable else "?mode=ro")
        connection = sqlite3.connect(uri, uri=True, timeout=0)
        connection.row_factory = sqlite3.Row
        current = _paths(path)
        if (original.st_dev, original.st_ino) != (current.st_dev, current.st_ino):
            raise CallsImportError("calls_source_mismatch")
        connection.execute("PRAGMA foreign_keys = ON")
        connection.execute("PRAGMA trusted_schema = OFF")
        if check_schema:
            _validate_schema(connection, binding)
        yield connection
    except CallsImportError:
        raise
    except OSError:
        raise CallsImportError("calls_import_io") from None
    except (sqlite3.DatabaseError, TypeError, ValueError, UnicodeError, RecursionError):
        raise CallsImportError("calls_import_corrupt") from None
    finally:
        if connection is not None:
            connection.close()


def initialize_import_store(path: Path, *, crm_binding: dict) -> None:
    """Initialize only after the caller validates the source mode and holds its lock."""
    if not _valid_binding(crm_binding):
        raise CallsImportError("calls_source_mismatch")
    path = Path(path)
    try:
        _private(path.parent, directory=True)
        if not os.path.lexists(path):
            descriptor = os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY | os.O_NOFOLLOW, 0o600)
            os.close(descriptor)
    except OSError:
        raise CallsImportError("calls_import_io") from None
    with _database(path, writable=True, check_schema=False) as connection:
        if connection.execute("SELECT 1 FROM sqlite_master LIMIT 1").fetchone():
            _validate_schema(connection, crm_binding)
            return
        with connection:
            connection.execute("BEGIN IMMEDIATE")
            for statement in _CREATE:
                connection.execute(statement)
            connection.execute("INSERT INTO import_meta VALUES (?)", (_canonical(crm_binding).decode(),))
            connection.execute("PRAGMA user_version = 1")


def validate_import_store(path: Path, *, crm_binding: dict) -> None:
    """Validate existing private schema and binding without reading source BLOBs."""
    with _database(path, binding=crm_binding):
        pass


def _load_record(connection, import_id, binding):
    row = connection.execute("SELECT content_sha256, manifest, account_id, request_key "
                             "FROM revisions WHERE import_id = ?",
                             (import_id,)).fetchone()
    if row is None:
        raise CallsImportError("calls_import_not_found")
    if not isinstance(row["manifest"], str) or len(row["manifest"].encode()) > MAX_RECORD_BYTES:
        raise CallsImportError("calls_import_corrupt")
    manifest = _json(row["manifest"])
    try:
        record = _make_record(manifest)
        validate_imported_record(record, account_id=manifest["association"]["account_id"], crm_binding=binding)
        if (record["id"] != import_id or manifest["content_sha256"] != row["content_sha256"]
                or record["account_id"] != row["account_id"]
                or _request_key(manifest, manifest, manifest["association"], manifest["called_at"],
                                manifest["association_generation"]) != row["request_key"]):
            raise CallsImportError("calls_import_corrupt")
        content = connection.execute("SELECT length(raw) FROM contents WHERE content_sha256 = ?", (row[0],)).fetchone()
        if content is None or not _int(content[0], 1, MAX_RAW_BYTES):
            raise CallsImportError("calls_import_corrupt")
        raw = connection.execute("SELECT raw FROM contents WHERE content_sha256 = ?", (row[0],)).fetchone()[0]
        parsed = parse_transcript(raw, format=manifest["format"])
        selection = (None if manifest["selection_mode"] == "default" else
                     (manifest["requested_range"]["first"], manifest["requested_range"]["last"]))
        excerpt = select_excerpt(parsed, requested_range=selection)
        if (len(raw) != manifest["raw_byte_count"] or parsed["content_sha256"] != manifest["content_sha256"]
                or any(excerpt[key] != manifest[key] for key in
                       ("units", "total_units", "requested_range", "cited_range", "unit_kind", "selection_mode"))):
            raise CallsImportError("calls_import_corrupt")
        return record, parsed
    except (CallsImportError, KeyError, TypeError, ValueError, UnicodeError):
        raise CallsImportError("calls_import_corrupt") from None


def _metadata(manifest, import_id):
    fields = ("format", "called_at", "imported_at", "content_sha256", "requested_range", "cited_range",
              "total_units", "unit_kind", "selection_mode")
    return {"import_id": import_id, "account_id": manifest["association"]["account_id"],
            **{key: deepcopy(manifest[key]) for key in fields}}


def _request_key(parsed, excerpt, association, called_at, generation):
    stable_association = {key: value for key, value in association.items()
                          if key not in ("resolution_receipt_id", "account_number")}
    return _digest({"content_sha256": parsed["content_sha256"], "format": parsed["format"],
                    "parser_version": PARSER_VERSION, "association": stable_association,
                    "called_at": called_at, "association_generation": generation,
                    **{key: excerpt[key] for key in ("selection_mode", "requested_range", "cited_range")}})


def store_import(path: Path, parsed: dict, excerpt: dict, *, association: dict,
                 called_at: str | None, imported_at: str, reassign: bool = False) -> dict[str, Any]:
    """Publish content, immutable revision and current pointers in one transaction."""
    _validate_association(association)
    if not _time(called_at, nullable=True) or not _time(imported_at) or type(reassign) is not bool:
        raise CallsImportError("calls_import_invalid")
    try:
        reparsed = parse_transcript(parsed["raw"], format=parsed["format"])
        if reparsed != parsed:
            raise CallsImportError("calls_import_invalid")
        span = excerpt["requested_range"]
        selection = None if excerpt["selection_mode"] == "default" else (span["first"], span["last"])
        if select_excerpt(reparsed, requested_range=selection) != excerpt:
            raise CallsImportError("calls_import_invalid")
    except (KeyError, TypeError):
        raise CallsImportError("calls_import_invalid") from None
    binding, account_id = association["crm_binding"], association["account_id"]
    digest = parsed["content_sha256"]
    with _database(path, binding=binding, writable=True) as connection:
        with connection:
            connection.execute("BEGIN IMMEDIATE")
            number = association["account_number"]
            number_row = (connection.execute("SELECT account_id FROM number_associations WHERE account_number = ?",
                                             (number,)).fetchone() if number is not None else None)
            if number_row is not None and not _valid_account(number_row[0], binding):
                raise CallsImportError("calls_import_corrupt")
            number_reassigned = number_row is not None and number_row[0] != account_id
            if number_reassigned and not reassign:
                raise CallsImportError("calls_association_changed")
            head = connection.execute("SELECT account_id, generation FROM content_associations "
                                      "WHERE content_sha256 = ?", (digest,)).fetchone()
            content = connection.execute("SELECT length(raw) FROM contents WHERE content_sha256 = ?",
                                         (digest,)).fetchone()
            if (head is None) != (content is None):
                raise CallsImportError("calls_import_corrupt")
            if head is not None and (not _valid_account(head[0], binding) or not _int(head[1], 1, 2**63 - 2)):
                raise CallsImportError("calls_import_corrupt")
            content_reassigned = head is not None and head[0] != account_id
            reassigned = content_reassigned or number_reassigned
            if content_reassigned and not reassign:
                raise CallsImportError("calls_reassign_required")
            generation = 1 if head is None else head[1] + int(content_reassigned)
            if content is not None:
                if content[0] != len(parsed["raw"]):
                    raise CallsImportError("calls_import_corrupt")
                stored_raw = connection.execute("SELECT raw FROM contents WHERE content_sha256 = ?",
                                                (digest,)).fetchone()[0]
                if stored_raw != parsed["raw"]:
                    raise CallsImportError("calls_import_corrupt")
            else:
                connection.execute("INSERT INTO contents VALUES (?, ?)", (digest, parsed["raw"]))
            key = _request_key(parsed, excerpt, association, called_at, generation)
            previous = connection.execute("SELECT import_id FROM revisions WHERE request_key = ?", (key,)).fetchone()
            created = previous is None
            if created:
                manifest = {"schema_version": 1, "source": "user_supplied_export", "format": parsed["format"],
                            "parser_version": PARSER_VERSION, "content_sha256": digest,
                            "raw_byte_count": len(parsed["raw"]),
                            "association": {key: deepcopy(association[key]) for key in _PUBLIC_ASSOCIATION_KEYS},
                            "association_generation": generation,
                            "called_at": called_at, "imported_at": imported_at,
                            **{name: deepcopy(excerpt[name]) for name in
                               ("selection_mode", "unit_kind", "requested_range", "cited_range",
                                "total_units", "units")}}
                record = validate_imported_record(_make_record(manifest), account_id=account_id, crm_binding=binding)
                connection.execute("INSERT INTO revisions VALUES (?, ?, ?, ?, ?)",
                                   (record["id"], key, digest, account_id, _canonical(manifest).decode()))
            else:
                record, _ = _load_record(connection, previous[0], binding)
                manifest = record["manifest"]
                if _request_key(manifest, manifest, manifest["association"], manifest["called_at"],
                                manifest["association_generation"]) != key:
                    raise CallsImportError("calls_import_corrupt")
            if content_reassigned:
                connection.execute("DELETE FROM active_imports WHERE account_id = ? AND import_id IN "
                                   "(SELECT import_id FROM revisions WHERE content_sha256 = ?)", (head[0], digest))
            connection.execute("INSERT INTO content_associations VALUES (?, ?, ?) ON CONFLICT(content_sha256) "
                               "DO UPDATE SET account_id=excluded.account_id, generation=excluded.generation",
                               (digest, account_id, generation))
            if number is not None:
                connection.execute("INSERT INTO number_associations VALUES (?, ?) ON CONFLICT(account_number) "
                                   "DO UPDATE SET account_id=excluded.account_id", (number, account_id))
            connection.execute("INSERT INTO active_imports VALUES (?, ?) ON CONFLICT(account_id) "
                               "DO UPDATE SET import_id=excluded.import_id", (account_id, record["id"]))
    return {"schema_version": 1, "source": "imported_transcript", "status": "imported" if created else "unchanged",
            "created": created, "selected": True, "reassigned": reassigned,
            "resolution_receipt_id": association["resolution_receipt_id"], **_metadata(manifest, record["id"])}


def list_imports(path: Path, *, crm_binding: dict, account_id: str | None = None,
                 limit: int = 100, after: str | None = None) -> dict[str, Any]:
    """List import metadata without opening transcript content."""
    if (not _valid_binding(crm_binding) or not _int(limit, 1, 100)
            or (account_id is not None and not _valid_account(account_id, crm_binding))
            or (after is not None and (not isinstance(after, str) or not _NOTE_ID.fullmatch(after)))):
        raise CallsImportError("calls_import_invalid")
    with _database(path, binding=crm_binding) as connection:
        rows = connection.execute("SELECT r.import_id, r.manifest, r.account_id, a.account_id AS active_account "
                                  "FROM revisions r LEFT JOIN active_imports a ON a.import_id=r.import_id "
                                  "WHERE r.import_id > ? AND (? IS NULL OR r.account_id = ?) "
                                  "ORDER BY r.import_id LIMIT ?", (after or "", account_id, account_id, limit + 1))
        result = []
        for row in rows:
            if not isinstance(row["manifest"], str) or len(row["manifest"].encode()) > MAX_RECORD_BYTES:
                raise CallsImportError("calls_import_corrupt")
            manifest = _json(row["manifest"])
            try:
                record = _make_record(manifest)
                validate_imported_record(record, account_id=manifest["association"]["account_id"],
                                         crm_binding=crm_binding)
                if record["id"] != row["import_id"] or record["account_id"] != row["account_id"]:
                    raise CallsImportError("calls_import_corrupt")
                if row["active_account"] is not None and row["active_account"] != record["account_id"]:
                    raise CallsImportError("calls_import_corrupt")
            except (CallsImportError, KeyError, TypeError, ValueError):
                raise CallsImportError("calls_import_corrupt") from None
            result.append({**_metadata(manifest, record["id"]), "active": row["active_account"] is not None})
            if len(result) > limit:
                break
    more = len(result) > limit
    return {"schema_version": 1, "source": "imported_transcript", "imports": result[:limit],
            "next_after": result[limit - 1]["import_id"] if more else None}


def inspect_import(path: Path, import_id: str, *, crm_binding: dict, full: bool = False) -> dict[str, Any]:
    """Inspect exact saved evidence; only explicit full inspection exposes raw text."""
    if not isinstance(import_id, str) or not _NOTE_ID.fullmatch(import_id) or type(full) is not bool:
        raise CallsImportError("calls_import_invalid")
    with _database(path, binding=crm_binding) as connection:
        record, parsed = _load_record(connection, import_id, crm_binding)
        active = connection.execute("SELECT account_id FROM active_imports WHERE import_id = ?",
                                    (import_id,)).fetchone()
        if active is not None and active[0] != record["account_id"]:
            raise CallsImportError("calls_import_corrupt")
    result = {"schema_version": 1, "source": "imported_transcript",
              "import": {**_metadata(record["manifest"], import_id), "active": active is not None},
              "body": record["body"], "units": deepcopy(record["manifest"]["units"])}
    if full:
        result.update(units=list(parsed["units"]), original_text=parsed["raw"].decode("utf-8"))
    return result


class ImportedCallsReader:
    """Read one selected revision after the gateway has checked calls:read."""

    name = "imported"

    def __init__(self, path: Path, *, crm_binding: dict):
        self.path = Path(path)
        self.crm_binding = deepcopy(crm_binding)

    def active_import_id(self, account_id: str) -> str | None:
        """Return the current revision ID from metadata only, without reading transcript content."""
        if not _valid_binding(self.crm_binding) or not _valid_account(account_id, self.crm_binding):
            raise CallsImportError("calls_import_invalid")
        with _database(self.path, binding=self.crm_binding) as connection:
            row = connection.execute("SELECT import_id FROM active_imports WHERE account_id = ?",
                                     (account_id,)).fetchone()
        return None if row is None else row[0]

    def read_call_notes(self, account_id: str) -> list[dict]:
        if not _valid_binding(self.crm_binding) or not _valid_account(account_id, self.crm_binding):
            raise CallsImportError("calls_import_invalid")
        with _database(self.path, binding=self.crm_binding) as connection:
            row = connection.execute("SELECT import_id FROM active_imports WHERE account_id = ?",
                                     (account_id,)).fetchone()
            if row is None:
                raise CallsImportError("calls_not_imported")
            record, _ = _load_record(connection, row[0], self.crm_binding)
            if record["account_id"] != account_id:
                raise CallsImportError("calls_import_corrupt")
            head = connection.execute("SELECT account_id, generation FROM content_associations "
                                      "WHERE content_sha256 = ?", (record["manifest"]["content_sha256"],)).fetchone()
            if (head is None or head[0] != account_id
                    or head[1] != record["manifest"]["association_generation"]):
                raise CallsImportError("calls_import_corrupt")
            return [record]
