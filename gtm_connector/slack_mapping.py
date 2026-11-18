"""Bind one selected Slack thread to an audited account without reading either source."""

from contextlib import contextmanager
from copy import deepcopy
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import stat
from urllib.parse import parse_qsl, urlsplit
import uuid


MAX_MAPPING_BYTES = 131072
MAX_MAPPINGS = 100
MAPPING_ERROR_CODES = {
    "slack_selection_invalid": ("usage", 2), "slack_mapping_missing": ("refusal", 3),
    "slack_mapping_conflict": ("refusal", 3), "slack_source_mismatch": ("refusal", 3),
    "slack_mapping_invalid": ("check", 4), "slack_storage_failed": ("io", 1),
    "slack_account_not_found": ("usage", 2), "slack_account_ambiguous": ("refusal", 3),
    "slack_association_failed": ("check", 4), "slack_association_invalid": ("check", 4),
    "slack_crm_unavailable": ("upstream", 5), "slack_crm_configuration_invalid": ("usage", 2),
    "slack_identity_unchecked": ("refusal", 3),
}
_HEX = re.compile(r"[0-9a-f]{64}")
_ENTRY_KEYS = {"schema_version", "mapping_id", "account_id", "crm_binding", "slack_binding", "channel_id",
               "thread_ts", "selected_at", "resolution_receipt_id"}


class SlackMappingError(RuntimeError):
    def __init__(self, code):
        self.code = code if code in MAPPING_ERROR_CODES else "slack_mapping_invalid"
        self.category, self.exit_code = MAPPING_ERROR_CODES[self.code]
        super().__init__(self.code)


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False).encode("utf-8")


def valid_crm_binding(value):
    return (isinstance(value, dict) and set(value) == {"schema_version", "crm_backend", "identity_sha256"}
            and type(value["schema_version"]) is int and value["schema_version"] == 1
            and value["crm_backend"] in ("sqlite", "salesforce") and isinstance(value["identity_sha256"], str)
            and _HEX.fullmatch(value["identity_sha256"]) is not None)


def valid_slack_binding(value):
    return (isinstance(value, dict) and set(value) == {"schema_version", "provider", "identity_sha256"}
            and type(value["schema_version"]) is int and value["schema_version"] == 1
            and value["provider"] == "slack" and isinstance(value["identity_sha256"], str)
            and _HEX.fullmatch(value["identity_sha256"]) is not None)


def slack_identity_binding(identity):
    if not isinstance(identity, dict) or set(identity) != {"app_id", "team_id", "user_id", "enterprise_id"}:
        raise SlackMappingError("slack_source_mismatch")
    for key, prefix in (("app_id", "A"), ("team_id", "T"), ("user_id", "UW"), ("enterprise_id", "E")):
        value = identity[key]
        if key == "enterprise_id" and value is None:
            continue
        if not isinstance(value, str) or re.fullmatch("[" + prefix + "][A-Z0-9]{1,63}", value) is None:
            raise SlackMappingError("slack_source_mismatch")
    document = {"schema_version": 1, "provider": "slack", **identity}
    return {"schema_version": 1, "provider": "slack",
            "identity_sha256": hashlib.sha256(_canonical(document)).hexdigest()}


def validate_thread_timestamp(value):
    if (not isinstance(value, str) or re.fullmatch(r"(?:0|[1-9][0-9]{0,11})\.[0-9]{6}", value) is None
            or int(value.split(".")[0]) > 253402300799):
        raise SlackMappingError("slack_selection_invalid")
    return value


def timestamp_microseconds(value):
    seconds, fraction = validate_thread_timestamp(value).split(".")
    return int(seconds) * 1000000 + int(fraction)


def _channel(value):
    if not isinstance(value, str) or re.fullmatch(r"[CG][A-Z0-9]{1,31}", value) is None:
        raise SlackMappingError("slack_selection_invalid")
    return value


def parse_thread_selection(*, thread_url=None, channel_id=None, thread_ts=None, workspace_origin=None):
    try:
        if thread_url is not None:
            if channel_id is not None or thread_ts is not None or not isinstance(thread_url, str):
                raise ValueError
            if len(thread_url) > 4096 or any(ord(char) < 33 or ord(char) > 126 for char in thread_url):
                raise ValueError
            origin = urlsplit(workspace_origin) if isinstance(workspace_origin, str) else None
            url = urlsplit(thread_url)
            if (origin is None or origin.scheme != "https" or origin.path not in ("", "/")
                    or origin.query or origin.fragment or origin.username or origin.password
                    or origin.port not in (None, 443) or origin.hostname is None
                    or re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.slack\.com", origin.hostname) is None
                    or url.scheme != "https" or url.hostname != origin.hostname or url.username or url.password
                    or url.port not in (None, 443) or url.fragment):
                raise ValueError
            match = re.fullmatch(r"/archives/([CG][A-Z0-9]{1,31})/p([0-9]{7,18})", url.path)
            if match is None or re.search(r"%(?![0-9a-fA-F]{2})", url.query):
                raise ValueError
            pairs = parse_qsl(url.query, keep_blank_values=True, strict_parsing=True, errors="strict", max_num_fields=2)
            fields = dict(pairs)
            if len(fields) != len(pairs) or set(fields) - {"thread_ts", "cid"}:
                raise ValueError
            channel_id = match[1]
            if "cid" in fields and fields["cid"] != channel_id:
                raise ValueError
            path_ts = match[2][:-6] + "." + match[2][-6:]
            validate_thread_timestamp(path_ts)
            thread_ts = fields.get("thread_ts", path_ts)
            if timestamp_microseconds(thread_ts) > timestamp_microseconds(path_ts):
                raise ValueError
        elif channel_id is None or thread_ts is None:
            raise ValueError
        return {"channel_id": _channel(channel_id), "thread_ts": validate_thread_timestamp(thread_ts)}
    except (ValueError, TypeError, UnicodeError):
        raise SlackMappingError("slack_selection_invalid") from None


def _utc(value):
    if (not isinstance(value, str) or re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}"
                                                  r"(?:\.[0-9]{1,6})?Z", value) is None):
        raise ValueError
    if datetime.fromisoformat(value).utcoffset() != timezone.utc.utcoffset(None):
        raise ValueError


def validate_slack_mapping(mapping, *, account_id, crm_binding, slack_binding):
    try:
        if (not valid_crm_binding(crm_binding) or not valid_slack_binding(slack_binding)
                or not isinstance(mapping, dict) or set(mapping) != _ENTRY_KEYS
                or type(mapping["schema_version"]) is not int or mapping["schema_version"] != 1):
            raise ValueError
        if mapping["crm_binding"] != crm_binding or mapping["slack_binding"] != slack_binding:
            raise SlackMappingError("slack_source_mismatch")
        pattern = r"acc-sf-[A-Za-z0-9]{18}" if crm_binding["crm_backend"] == "salesforce" else r"acc-[0-9]{3}"
        if (not isinstance(account_id, str) or re.fullmatch(pattern, account_id) is None
                or mapping["account_id"] != account_id):
            raise ValueError
        _channel(mapping["channel_id"])
        validate_thread_timestamp(mapping["thread_ts"])
        _utc(mapping["selected_at"])
        receipt = mapping["resolution_receipt_id"]
        if not isinstance(receipt, str) or str(uuid.UUID(receipt)) != receipt:
            raise ValueError
        manifest = {key: value for key, value in mapping.items() if key != "mapping_id"}
        if mapping["mapping_id"] != "map-" + hashlib.sha256(_canonical(manifest)).hexdigest():
            raise ValueError
        return deepcopy(mapping)
    except SlackMappingError as error:
        if error.code == "slack_source_mismatch":
            raise
        raise SlackMappingError("slack_mapping_invalid") from None
    except (KeyError, TypeError, ValueError, UnicodeError, RecursionError, OverflowError):
        raise SlackMappingError("slack_mapping_invalid") from None


def _unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError
        result[key] = value
    return result


@contextmanager
def _directory(path):
    path = Path(path)
    descriptor = None
    try:
        info = path.parent.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise SlackMappingError("slack_mapping_invalid")
        descriptor = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        opened = os.fstat(descriptor)
        if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
            raise SlackMappingError("slack_mapping_invalid")
        yield descriptor, path.name
    except OSError:
        raise SlackMappingError("slack_storage_failed") from None
    finally:
        if descriptor is not None:
            os.close(descriptor)


def _validate_document(document, *, crm_binding, slack_binding):
    if (not isinstance(document, dict)
            or set(document) != {"schema_version", "crm_binding", "slack_binding", "mappings"}
            or type(document["schema_version"]) is not int or document["schema_version"] != 1
            or not valid_crm_binding(document["crm_binding"]) or not valid_slack_binding(document["slack_binding"])):
        raise SlackMappingError("slack_mapping_invalid")
    if document["crm_binding"] != crm_binding or document["slack_binding"] != slack_binding:
        raise SlackMappingError("slack_source_mismatch")
    entries = document["mappings"]
    if not isinstance(entries, list) or len(entries) > MAX_MAPPINGS:
        raise SlackMappingError("slack_mapping_invalid")
    accounts, threads = [], set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise SlackMappingError("slack_mapping_invalid")
        validated = validate_slack_mapping(entry, account_id=entry.get("account_id"),
                                           crm_binding=crm_binding, slack_binding=slack_binding)
        account, thread = validated["account_id"], (validated["channel_id"], validated["thread_ts"])
        if account in accounts or thread in threads:
            raise SlackMappingError("slack_mapping_invalid")
        accounts.append(account)
        threads.add(thread)
    if accounts != sorted(accounts):
        raise SlackMappingError("slack_mapping_invalid")
    return document


def _read(descriptor, name, *, crm_binding, slack_binding):
    try:
        info = os.stat(name, dir_fd=descriptor, follow_symlinks=False)
    except FileNotFoundError:
        raise SlackMappingError("slack_mapping_missing") from None
    if (not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077
            or info.st_nlink != 1 or info.st_size > MAX_MAPPING_BYTES):
        raise SlackMappingError("slack_mapping_invalid")
    handle = os.open(name, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=descriptor)
    with os.fdopen(handle, "rb") as source:
        opened = os.fstat(source.fileno())
        if (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
            raise SlackMappingError("slack_mapping_invalid")
        raw = source.read(MAX_MAPPING_BYTES + 1)
    try:
        if len(raw) > MAX_MAPPING_BYTES:
            raise ValueError
        document = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique,
                              parse_constant=lambda _: (_ for _ in ()).throw(ValueError()))
    except (ValueError, UnicodeError, RecursionError):
        raise SlackMappingError("slack_mapping_invalid") from None
    return _validate_document(document, crm_binding=crm_binding, slack_binding=slack_binding)


def _write(descriptor, name, document):
    raw = _canonical(document)
    if len(raw) > MAX_MAPPING_BYTES:
        raise SlackMappingError("slack_mapping_conflict")
    temporary = ".slack-map-" + uuid.uuid4().hex
    try:
        handle = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=descriptor)
        with os.fdopen(handle, "wb") as target:
            target.write(raw)
            target.flush()
            os.fsync(target.fileno())
        os.replace(temporary, name, src_dir_fd=descriptor, dst_dir_fd=descriptor)
        os.fsync(descriptor)
    finally:
        try:
            os.unlink(temporary, dir_fd=descriptor)
        except FileNotFoundError:
            pass


def initialize_slack_mapping_store(path, *, crm_binding, slack_binding):
    if not valid_crm_binding(crm_binding) or not valid_slack_binding(slack_binding):
        raise SlackMappingError("slack_source_mismatch")
    with _directory(path) as (descriptor, name):
        try:
            _read(descriptor, name, crm_binding=crm_binding, slack_binding=slack_binding)
        except SlackMappingError as error:
            if error.code != "slack_mapping_missing":
                raise
            _write(descriptor, name, {"schema_version": 1, "crm_binding": crm_binding,
                                      "slack_binding": slack_binding, "mappings": []})


def validate_slack_mapping_store(path, *, crm_binding, slack_binding):
    with _directory(path) as (descriptor, name):
        return len(_read(descriptor, name, crm_binding=crm_binding, slack_binding=slack_binding)["mappings"])


def load_slack_mapping(path, *, account_id, crm_binding, slack_binding):
    with _directory(path) as (descriptor, name):
        document = _read(descriptor, name, crm_binding=crm_binding, slack_binding=slack_binding)
    for entry in document["mappings"]:
        if entry["account_id"] == account_id:
            return deepcopy(entry)
    raise SlackMappingError("slack_mapping_missing")


def save_slack_mapping(path, *, association, slack_binding, selection, selected_at, replace=False, reassign=False):
    try:
        if (not isinstance(association, dict) or not isinstance(selection, dict)
                or set(selection) != {"channel_id", "thread_ts"}
                or type(replace) is not bool or type(reassign) is not bool):
            raise ValueError
        manifest = {"schema_version": 1, "account_id": association["account_id"],
                    "crm_binding": deepcopy(association["crm_binding"]), "slack_binding": deepcopy(slack_binding),
                    "channel_id": selection["channel_id"], "thread_ts": selection["thread_ts"],
                    "selected_at": selected_at, "resolution_receipt_id": association["resolution_receipt_id"]}
        candidate = {**manifest, "mapping_id": "map-" + hashlib.sha256(_canonical(manifest)).hexdigest()}
        candidate = validate_slack_mapping(candidate, account_id=manifest["account_id"],
                                            crm_binding=manifest["crm_binding"], slack_binding=slack_binding)
    except (KeyError, TypeError, ValueError, UnicodeError, RecursionError):
        raise SlackMappingError("slack_association_invalid") from None
    with _directory(path) as (descriptor, name):
        document = _read(descriptor, name, crm_binding=manifest["crm_binding"], slack_binding=slack_binding)
        account = next((row for row in document["mappings"] if row["account_id"] == candidate["account_id"]), None)
        selected_thread = candidate["channel_id"], candidate["thread_ts"]
        thread = next((row for row in document["mappings"]
                       if (row["channel_id"], row["thread_ts"]) == selected_thread), None)
        if account is not None and account == thread:
            return {"mapping": deepcopy(account), "changed": False}
        if (account is not None and not replace) or (thread is not None and not reassign):
            raise SlackMappingError("slack_mapping_conflict")
        entries = [row for row in document["mappings"] if row != account and row != thread]
        entries.append(candidate)
        if len(entries) > MAX_MAPPINGS:
            raise SlackMappingError("slack_mapping_conflict")
        entries.sort(key=lambda row: row["account_id"])
        updated = {**document, "mappings": entries}
        _validate_document(updated, crm_binding=manifest["crm_binding"], slack_binding=slack_binding)
        _write(descriptor, name, updated)
    return {"mapping": deepcopy(candidate), "changed": True}
