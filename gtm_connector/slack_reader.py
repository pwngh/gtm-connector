"""Read one explicitly mapped Slack thread within one shared operation budget."""

import asyncio
from copy import deepcopy
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import re
import time
from typing import Any

from .slack_auth import SlackAuthError, get_slack_access, _decode


OPERATION_SECONDS = 20
MAX_REQUESTS = 10
MAX_RESPONSE_BYTES = 1_048_576
MAX_ROWS = 100
MAX_TEXT_BYTES = 16_384
MAX_TOTAL_TEXT_BYTES = 65_536
MAX_OUTPUT_BYTES = 262_144
_REPLIES_URL = "https://slack.com/api/conversations.replies"
SUPPORTED_SUBTYPES = (None, "bot_message", "me_message", "file_share", "thread_broadcast")
_ERRORS = {
    "slack_selection_invalid": 2, "slack_mapping_missing": 3, "slack_mapping_conflict": 3,
    "slack_source_mismatch": 3, "slack_mapping_invalid": 4, "slack_snapshot_invalid": 4,
    "slack_thread_unavailable": 3, "slack_forbidden": 3, "slack_thread_changed": 3,
    "slack_thread_incomplete": 3, "slack_resource_limit": 3, "slack_output_too_large": 3,
    "slack_clock_invalid": 3, "slack_response_invalid": 5, "slack_rate_limited": 5,
    "slack_timeout": 5, "slack_unavailable": 5, "slack_reconnect_required": 3,
    "slack_storage_failed": 1, "slack_invalid_configuration": 2, "slack_connection_busy": 1,
    "slack_private_state_invalid": 3, "slack_credentials_invalid": 4, "slack_not_connected": 3,
    "slack_token_type_unsupported": 3, "slack_oauth_rejected": 3,
}
SLACK_READ_ERROR_CODES = frozenset(_ERRORS)
_CATEGORIES = {1: "local_failure", 2: "usage_error", 3: "refused", 4: "check_failed", 5: "upstream_failure"}


class SlackReadError(RuntimeError):
    """A fixed error code with no provider text or private identifiers."""

    def __init__(self, code, *, retry_after_seconds=None):
        if code not in SLACK_READ_ERROR_CODES:
            raise ValueError("Unknown Slack read error code.")
        if retry_after_seconds is not None and (type(retry_after_seconds) is not int
                                               or not 0 <= retry_after_seconds <= 3600):
            raise ValueError("Invalid Slack retry delay.")
        self.code = code
        self.exit_code = _ERRORS[code]
        self.category = _CATEGORIES[self.exit_code]
        self.retry_after_seconds = retry_after_seconds
        super().__init__(code)


class _TokenExpired(RuntimeError):
    pass


class _Clock:
    monotonic = staticmethod(time.monotonic)

    @staticmethod
    def now():
        return datetime.now(timezone.utc)


def _stamp(value):
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def _date(value):
    if not isinstance(value, str) or len(value) > 32 or not value.endswith("Z"):
        raise ValueError
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if _stamp(parsed) != value:
        raise ValueError
    return parsed


def _canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True,
                      allow_nan=False).encode("ascii")


def _digest(value):
    return hashlib.sha256(_canonical(value)).hexdigest()


def _timestamp(value):
    from .slack_mapping import SlackMappingError, timestamp_microseconds
    try:
        return timestamp_microseconds(value)
    except (SlackMappingError, TypeError, ValueError):
        raise SlackReadError("slack_response_invalid") from None


def _wall_timestamp(value):
    delta = value - datetime(1970, 1, 1, tzinfo=timezone.utc)
    micros = (delta.days * 86400 + delta.seconds) * 1_000_000 + delta.microseconds
    seconds, fraction = divmod(micros, 1_000_000)
    result = f"{seconds}.{fraction:06d}"
    _timestamp(result)
    return result


def _sent_at(value):
    return _stamp(datetime(1970, 1, 1, tzinfo=timezone.utc) + timedelta(microseconds=_timestamp(value)))


@dataclass
class SlackOperationBudget:
    """One deadline and cumulative counters, shared with credential requests."""

    _clock: Any = field(default_factory=_Clock, repr=False)
    started_monotonic: float = field(init=False)
    deadline: float = field(init=False)
    started_at: str = field(init=False)
    http_attempts: int = 0
    auth_attempts: int = 0
    message_attempts: int = 0
    response_bytes: int = 0
    auth_response_bytes: int = 0
    message_response_bytes: int = 0
    refresh_attempts: int = 0
    token_retried: bool = False
    rate_retried: bool = False

    def __post_init__(self):
        self.started_monotonic = self._clock.monotonic()
        self.deadline = self.started_monotonic + OPERATION_SECONDS
        self.started_at = _stamp(self._clock.now())

    def remaining(self):
        remaining = self.deadline - self._clock.monotonic()
        if remaining <= 0:
            raise SlackReadError("slack_timeout")
        return remaining

    def begin_request(self, *, kind):
        self.remaining()
        if kind not in ("auth", "messages"):
            raise ValueError("Invalid Slack request kind.")
        if self.http_attempts >= MAX_REQUESTS:
            raise SlackReadError("slack_resource_limit")
        self.http_attempts += 1
        if kind == "auth":
            self.auth_attempts += 1
        else:
            self.message_attempts += 1

    def add_bytes(self, amount, *, kind):
        if type(amount) is not int or amount < 0 or kind not in ("auth", "messages"):
            raise ValueError("Invalid Slack byte count.")
        self.response_bytes += amount
        if kind == "auth":
            self.auth_response_bytes += amount
        else:
            self.message_response_bytes += amount
        if self.response_bytes > MAX_RESPONSE_BYTES:
            raise SlackReadError("slack_resource_limit")
        self.remaining()

    def begin_refresh(self):
        self.remaining()
        if self.refresh_attempts >= 1:
            raise SlackReadError("slack_reconnect_required")
        self.refresh_attempts += 1


def _provider_result(document):
    if not isinstance(document, dict) or type(document.get("ok")) is not bool:
        raise SlackReadError("slack_response_invalid")
    if document["ok"]:
        return document
    error = document.get("error")
    if error == "token_expired":
        raise _TokenExpired
    codes = {
        "channel_not_found": "slack_thread_unavailable", "thread_not_found": "slack_thread_unavailable",
        "not_in_channel": "slack_forbidden", "missing_scope": "slack_forbidden",
        "access_denied": "slack_forbidden", "no_permission": "slack_forbidden",
        "invalid_auth": "slack_reconnect_required", "token_revoked": "slack_reconnect_required",
        "account_inactive": "slack_reconnect_required", "not_authed": "slack_reconnect_required",
        "ratelimited": "slack_rate_limited", "rate_limited": "slack_rate_limited",
    }
    raise SlackReadError(codes.get(error, "slack_response_invalid") if isinstance(error, str)
                         else "slack_response_invalid")


class SlackRepliesProvider:
    """GET the fixed thread endpoint; never follow a source-provided URL."""

    def __init__(self, *, _transport=None):
        self._transport = _transport

    def replies(self, access_token, *, channel_id, parent_ts, latest, cursor, limit, budget):
        import httpx2

        async def bounded():
            budget.begin_request(kind="messages")
            remaining = budget.remaining()
            async with asyncio.timeout(remaining):
                timeout = httpx2.Timeout(min(5, remaining), connect=min(3, remaining),
                                         write=min(5, remaining), pool=min(3, remaining))
                params = {"channel": channel_id, "ts": parent_ts, "latest": latest,
                          "inclusive": "true", "limit": str(limit)}
                if cursor is not None:
                    params["cursor"] = cursor
                headers = {"Authorization": "Bearer " + access_token,
                           "Accept": "application/json", "Accept-Encoding": "identity"}
                async with httpx2.AsyncClient(transport=self._transport, trust_env=False,
                                             follow_redirects=False, timeout=timeout) as client:
                    async with client.stream("GET", _REPLIES_URL, params=params, headers=headers) as response:
                        length = response.headers.get("content-length")
                        if response.headers.get("content-encoding", "identity").lower() != "identity":
                            raise SlackReadError("slack_response_invalid")
                        if length is not None:
                            if not re.fullmatch(r"[0-9]{1,10}", length):
                                raise SlackReadError("slack_response_invalid")
                            if int(length) > MAX_RESPONSE_BYTES - budget.response_bytes:
                                raise SlackReadError("slack_resource_limit")
                        body = bytearray()
                        async for part in response.aiter_bytes():
                            budget.add_bytes(len(part), kind="messages")
                            body.extend(part)
                        budget.remaining()
                        if response.status_code == 429:
                            header = response.headers.get("retry-after", "")
                            delay = int(header) if re.fullmatch(r"[0-9]{1,4}", header) else None
                            if delay is not None and delay > 3600:
                                delay = None
                            raise SlackReadError("slack_rate_limited", retry_after_seconds=delay)
                        if response.status_code >= 500:
                            raise SlackReadError("slack_unavailable")
                        if response.status_code in (401, 403):
                            # Only the exact token_expired response authorizes a rotation retry.
                            if response.status_code == 401:
                                _provider_result(_decode(bytes(body)))
                                raise SlackReadError("slack_response_invalid")
                            raise SlackReadError("slack_forbidden")
                        if response.status_code != 200:
                            raise SlackReadError("slack_response_invalid")
                        document = _provider_result(_decode(bytes(body)))
                budget.remaining()
                return document

        try:
            return asyncio.run(bounded())
        except (TimeoutError, httpx2.TimeoutException):
            raise SlackReadError("slack_timeout") from None
        except httpx2.HTTPError:
            raise SlackReadError("slack_unavailable") from None
        except (ValueError, UnicodeError, RecursionError):
            raise SlackReadError("slack_response_invalid") from None


def _identifier(value, prefix):
    if not isinstance(value, str) or not re.fullmatch(f"[{prefix}][A-Z0-9]{{1,63}}", value):
        raise SlackReadError("slack_response_invalid")
    return value


def _manifest_rules(value, *, parent, cutoff):
    if not isinstance(value, dict) or set(value) != {
            "ts", "thread_ts", "subtype", "text", "user_id", "bot_id", "edited", "omissions"}:
        raise SlackReadError("slack_response_invalid")
    stamp = _timestamp(value["ts"])
    if value["thread_ts"] != parent or not _timestamp(parent) <= stamp <= _timestamp(cutoff):
        raise SlackReadError("slack_thread_incomplete")
    if value["subtype"] not in SUPPORTED_SUBTYPES:
        raise SlackReadError("slack_response_invalid")
    if not isinstance(value["text"], str):
        raise SlackReadError("slack_response_invalid")
    try:
        size = len(value["text"].encode("utf-8"))
    except UnicodeError:
        raise SlackReadError("slack_response_invalid") from None
    if size > MAX_TEXT_BYTES:
        raise SlackReadError("slack_output_too_large")
    for key, prefix in (("user_id", "UW"), ("bot_id", "B")):
        if value[key] is not None:
            _identifier(value[key], prefix)
    edited = value["edited"]
    if edited is not None:
        if not isinstance(edited, dict) or set(edited) != {"ts", "user_id"}:
            raise SlackReadError("slack_response_invalid")
        _identifier(edited["user_id"], "UW")
        if _timestamp(edited["ts"]) < stamp:
            raise SlackReadError("slack_response_invalid")
    omissions = value["omissions"]
    if (not isinstance(omissions, dict) or set(omissions) != {"files", "attachments", "blocks"}
            or any(type(n) is not int or not 0 <= n <= MAX_RESPONSE_BYTES for n in omissions.values())):
        raise SlackReadError("slack_response_invalid")
    return value


def _normalize(row, *, mapping, cutoff):
    if (not isinstance(row, dict) or row.get("type") != "message" or "text" not in row
            or any(key in row for key in ("message", "previous_message", "deleted_ts"))
            or ("hidden" in row and row["hidden"] is not False)
            or ("subtype" in row and row["subtype"] not in SUPPORTED_SUBTYPES[1:])):
        raise SlackReadError("slack_response_invalid")
    parent = mapping["thread_ts"]
    stamp = row.get("ts")
    if ("channel" in row and row["channel"] != mapping["channel_id"]
            or row.get("thread_ts", parent if stamp == parent else None) != parent):
        raise SlackReadError("slack_thread_incomplete")
    omissions = {}
    for key in ("files", "attachments", "blocks"):
        items = row.get(key, [])
        if not isinstance(items, list) or any(not isinstance(item, dict) for item in items):
            raise SlackReadError("slack_response_invalid")
        omissions[key] = len(items)
    edited = None
    if "edited" in row:
        item = row["edited"]
        if not isinstance(item, dict) or "ts" not in item or "user" not in item:
            raise SlackReadError("slack_response_invalid")
        edited = {"ts": item["ts"], "user_id": item["user"]}
    if "root" in row:
        root = row["root"]
        if row.get("subtype") != "thread_broadcast" or not isinstance(root, dict):
            raise SlackReadError("slack_response_invalid")
        if "ts" in root and root["ts"] != parent:
            raise SlackReadError("slack_thread_incomplete")
    value = {"ts": stamp, "thread_ts": parent, "subtype": row.get("subtype"), "text": row["text"],
             "user_id": row.get("user"), "bot_id": row.get("bot_id"), "edited": edited, "omissions": omissions}
    if ("user" in row and row["user"] is None) or ("bot_id" in row and row["bot_id"] is None):
        raise SlackReadError("slack_response_invalid")
    return _manifest_rules(value, parent=parent, cutoff=cutoff)


def _cursor(document, requested):
    metadata = document.get("response_metadata", {})
    if not isinstance(metadata, dict):
        raise SlackReadError("slack_thread_incomplete")
    cursor = metadata.get("next_cursor")
    if cursor in (None, ""):
        cursor = None
    else:
        try:
            if (not isinstance(cursor, str) or len(cursor.encode("utf-8")) > 2048
                    or any(ord(c) < 32 or 127 <= ord(c) <= 159 for c in cursor) or cursor == requested):
                raise ValueError
        except (ValueError, UnicodeError):
            raise SlackReadError("slack_thread_incomplete") from None
    if "has_more" in document and (type(document["has_more"]) is not bool
                                    or document["has_more"] != (cursor is not None)):
        raise SlackReadError("slack_thread_incomplete")
    return cursor


def _author(manifest):
    if manifest["subtype"] == "bot_message" or manifest["bot_id"] is not None:
        return "Slack app"
    return "Slack participant" if manifest["user_id"] is not None else "Unknown Slack author"


def _record(manifest, snapshot):
    content = {key: value for key, value in manifest.items() if key != "text"}
    mapping = snapshot["mapping"]
    record_id = "msg-" + _digest({"snapshot_sha256": snapshot["snapshot_sha256"],
                                 "manifest_sha256": _digest(manifest)})
    return {"id": record_id, "account_id": mapping["account_id"], "channel": mapping["channel_id"],
            "author": _author(manifest), "sent_at": _sent_at(manifest["ts"]), "body": manifest["text"],
            "slack_message": {"schema_version": 1, "snapshot_sha256": snapshot["snapshot_sha256"], **content}}


def _output_size(records, snapshot):
    with_refs = [{**row, "record_ref": "slack/messages/" + row["id"]} for row in records]
    return len(_canonical({"source": "slack", "records": with_refs, "untrusted_data": True,
                           "thread_snapshot": snapshot}))


@dataclass(frozen=True)
class ThreadRead:
    records: list[dict]
    snapshot: dict


def _auth_error(error):
    code = {"slack_scope_mismatch": "slack_forbidden", "slack_identity_mismatch": "slack_source_mismatch"}.get(
        error.code, error.code)
    return SlackReadError(code if code in SLACK_READ_ERROR_CODES else "slack_reconnect_required")


class SlackThreadReader:
    """One pinned account mapping; construction performs no I/O."""

    name = "slack"

    def __init__(self, mapping_path, *, connection_dir, crm_binding, slack_binding,
                 _provider=None, _auth_provider=None, _clock=None, on_read=None):
        self.mapping_path = Path(mapping_path)
        self.connection_dir = Path(connection_dir)
        self.crm_binding = deepcopy(crm_binding)
        self.slack_binding = deepcopy(slack_binding)
        self._provider = _provider or SlackRepliesProvider()
        self._auth_provider = _auth_provider
        self._clock = _clock or _Clock()
        self.on_read = on_read

    def read_slack_messages(self, account_id):
        from .slack_mapping import SlackMappingError, load_slack_mapping, slack_identity_binding

        budget = SlackOperationBudget(_clock=self._clock)
        phase = "mapping"
        phase_started = self._clock.monotonic()
        durations = {"mapping": 0.0, "credential_binding": 0.0, "thread_read": 0.0}
        mapping_ok = credential_ok = complete = False
        pages = raw_rows = duplicates = 0
        manifests: dict[str, dict] = {}
        result = None
        code = "slack_unavailable"

        def set_phase(next_phase):
            nonlocal phase, phase_started
            now = self._clock.monotonic()
            durations[phase] += max(0.0, now - phase_started)
            phase, phase_started = next_phase, now

        def acquire(expired=None):
            nonlocal credential_ok
            credential_ok = False
            set_phase("credential_binding")
            access = get_slack_access(self.connection_dir, required_scopes=(), _provider=self._auth_provider,
                                      _clock=self._clock, _budget=budget, _expired_access=expired)
            if slack_identity_binding(access.identity) != self.slack_binding:
                raise SlackReadError("slack_source_mismatch")
            budget.remaining()
            credential_ok = True
            set_phase("thread_read")
            return access

        try:
            mapping = load_slack_mapping(self.mapping_path, account_id=account_id,
                                         crm_binding=self.crm_binding, slack_binding=self.slack_binding)
            budget.remaining()
            cutoff = _wall_timestamp(_date(budget.started_at))
            if _timestamp(mapping["thread_ts"]) > _timestamp(cutoff):
                raise SlackReadError("slack_thread_unavailable")
            mapping_ok = True
            access = acquire()
            cursor = None
            seen_cursors: set[str] = set()
            while True:
                if raw_rows >= MAX_ROWS:
                    raise SlackReadError("slack_resource_limit")
                limit = MAX_ROWS - raw_rows
                try:
                    document = self._provider.replies(access.access_token, channel_id=mapping["channel_id"],
                                                      parent_ts=mapping["thread_ts"], latest=cutoff, cursor=cursor,
                                                      limit=limit, budget=budget)
                except _TokenExpired:
                    if budget.token_retried:
                        raise SlackReadError("slack_reconnect_required") from None
                    budget.token_retried = True
                    access = acquire(access)
                    continue
                except SlackReadError as exc:
                    delay = exc.retry_after_seconds
                    if (exc.code != "slack_rate_limited" or delay is None or delay > 5 or budget.rate_retried
                            or budget.http_attempts >= MAX_REQUESTS or budget.remaining() <= delay):
                        raise
                    budget.rate_retried = True

                    async def wait():
                        async with asyncio.timeout(budget.remaining()):
                            await asyncio.sleep(delay)
                    try:
                        asyncio.run(wait())
                    except TimeoutError:
                        raise SlackReadError("slack_timeout") from None
                    budget.remaining()
                    continue
                budget.remaining()
                if not isinstance(document, dict) or document.get("ok") is not True:
                    raise SlackReadError("slack_response_invalid")
                rows = document.get("messages")
                if not isinstance(rows, list):
                    raise SlackReadError("slack_response_invalid")
                raw_rows += len(rows)
                if len(rows) > limit:
                    raise SlackReadError("slack_resource_limit")
                pages += 1
                if pages == 1 and sum(isinstance(row, dict) and row.get("ts") == mapping["thread_ts"]
                                      for row in rows) != 1:
                    raise SlackReadError("slack_thread_incomplete")
                for row in rows:
                    manifest = _normalize(row, mapping=mapping, cutoff=cutoff)
                    stamp = manifest["ts"]
                    if stamp in manifests:
                        if manifests[stamp] != manifest:
                            raise SlackReadError("slack_thread_changed")
                        duplicates += 1
                    else:
                        manifests[stamp] = manifest
                        if sum(len(item["text"].encode("utf-8")) for item in manifests.values()) > MAX_TOTAL_TEXT_BYTES:
                            raise SlackReadError("slack_output_too_large")
                following = _cursor(document, cursor)
                if following is None:
                    complete = True
                    break
                if following in seen_cursors:
                    raise SlackReadError("slack_thread_incomplete")
                seen_cursors.add(following)
                cursor = following
            fetched_at = _stamp(self._clock.now())
            if _date(fetched_at) < _date(budget.started_at):
                raise SlackReadError("slack_clock_invalid")
            ordered = sorted(manifests.values(), key=lambda value: _timestamp(value["ts"]))
            counts = {"http_attempts": budget.http_attempts, "auth_attempts": budget.auth_attempts,
                      "message_attempts": budget.message_attempts, "response_bytes": budget.response_bytes,
                      "pages": pages, "raw_rows": raw_rows, "messages": len(ordered), "duplicates": duplicates,
                      "refresh_attempts": budget.refresh_attempts, "token_retries": int(budget.token_retried),
                      "rate_retries": int(budget.rate_retried)}
            counts.update({"omitted_" + key: sum(item["omissions"][key] for item in ordered)
                           for key in ("files", "attachments", "blocks")})
            snapshot = {"schema_version": 1, "source": "slack", "mapping": mapping, "cutoff_ts": cutoff,
                        "started_at": budget.started_at, "fetched_at": fetched_at, "pagination_complete": True,
                        "counts": counts, "manifest_sha256": [_digest(item) for item in ordered]}
            snapshot["snapshot_sha256"] = _digest(snapshot)
            records = [_record(item, snapshot) for item in ordered]
            if _output_size(records, snapshot) > MAX_OUTPUT_BYTES:
                raise SlackReadError("slack_output_too_large")
            budget.remaining()
            result = ThreadRead(records, snapshot)
            code = "ok"
            return result
        except SlackAuthError as exc:
            error = _auth_error(exc)
            code = error.code
            raise error from None
        except SlackMappingError as exc:
            code = exc.code if exc.code in SLACK_READ_ERROR_CODES else "slack_mapping_invalid"
            raise SlackReadError(code) from None
        except SlackReadError as exc:
            code = exc.code
            raise
        finally:
            now = self._clock.monotonic()
            durations[phase] += max(0.0, now - phase_started)
            event = {"schema_version": 1, "status": "passed" if result is not None else "failed", "code": code,
                     "failure_phase": None if result is not None else phase,
                     "mapping_validated": mapping_ok, "credential_binding_validated": credential_ok,
                     "pagination_complete": complete,
                     "duration_ms": round(max(0, now - budget.started_monotonic)*1000, 3),
                     "durations_ms": {key: round(value*1000, 3) for key, value in durations.items()},
                     "counts": {"api_calls": budget.http_attempts, "auth_calls": budget.auth_attempts,
                                "message_calls": budget.message_attempts, "response_bytes": budget.response_bytes,
                                "auth_response_bytes": budget.auth_response_bytes,
                                "message_response_bytes": budget.message_response_bytes, "pages": pages,
                                "raw_rows": raw_rows, "messages_seen": len(manifests),
                                "returned_messages": 0 if result is None else len(result.records),
                                "duplicates": duplicates, "refresh_attempts": budget.refresh_attempts,
                                "token_retries": int(budget.token_retried), "rate_retries": int(budget.rate_retried)}}
            if self.on_read is not None:
                try:
                    self.on_read(event)
                except Exception:
                    pass


def validate_slack_snapshot(records, snapshot, *, account_id, crm_binding, slack_binding):
    """Validate saved source evidence without files, credentials, HTTP, or a model."""
    from .slack_mapping import SlackMappingError, validate_slack_mapping

    try:
        if not isinstance(snapshot, dict) or set(snapshot) != {
                "schema_version", "source", "mapping", "cutoff_ts", "started_at", "fetched_at",
                "pagination_complete", "counts", "manifest_sha256", "snapshot_sha256"}:
            raise ValueError
        if (type(snapshot["schema_version"]) is not int or snapshot["schema_version"] != 1
                or snapshot["source"] != "slack" or snapshot["pagination_complete"] is not True):
            raise ValueError
        mapping = validate_slack_mapping(snapshot["mapping"], account_id=account_id,
                                          crm_binding=crm_binding, slack_binding=slack_binding)
        if (_date(snapshot["fetched_at"]) < _date(snapshot["started_at"])
                or snapshot["cutoff_ts"] != _wall_timestamp(_date(snapshot["started_at"]))):
            raise ValueError
        if snapshot["snapshot_sha256"] != _digest({k: v for k, v in snapshot.items() if k != "snapshot_sha256"}):
            raise ValueError
        if not isinstance(records, list) or not 1 <= len(records) <= MAX_ROWS:
            raise ValueError
        manifests = []
        record_keys = {"id", "account_id", "channel", "author", "sent_at", "body", "slack_message"}
        previous = -1
        for row in records:
            if not isinstance(row, dict) or set(row) not in (record_keys, record_keys | {"record_ref"}):
                raise ValueError
            metadata = row["slack_message"]
            if not isinstance(metadata, dict) or set(metadata) != {
                    "schema_version", "snapshot_sha256", "ts", "thread_ts", "subtype", "user_id",
                    "bot_id", "edited", "omissions"}:
                raise ValueError
            if type(metadata["schema_version"]) is not int or metadata["schema_version"] != 1:
                raise ValueError
            manifest = {key: value for key, value in metadata.items()
                        if key not in ("schema_version", "snapshot_sha256")}
            manifest["text"] = row["body"]
            _manifest_rules(manifest, parent=mapping["thread_ts"], cutoff=snapshot["cutoff_ts"])
            timestamp = _timestamp(manifest["ts"])
            if timestamp <= previous:
                raise ValueError
            previous = timestamp
            expected = _record(manifest, snapshot)
            if row != expected and row != {**expected, "record_ref": "slack/messages/" + expected["id"]}:
                raise ValueError
            manifests.append(manifest)
        if manifests[0]["ts"] != mapping["thread_ts"]:
            raise ValueError
        if sum(len(item["text"].encode("utf-8")) for item in manifests) > MAX_TOTAL_TEXT_BYTES:
            raise ValueError
        if snapshot["manifest_sha256"] != [_digest(item) for item in manifests]:
            raise ValueError
        counts = snapshot["counts"]
        keys = {"http_attempts", "auth_attempts", "message_attempts", "response_bytes", "pages", "raw_rows",
                "messages", "duplicates", "refresh_attempts", "token_retries", "rate_retries",
                "omitted_files", "omitted_attachments", "omitted_blocks"}
        if (not isinstance(counts, dict) or set(counts) != keys
                or any(type(value) is not int or value < 0 for value in counts.values())):
            raise ValueError
        if (not 1 <= counts["http_attempts"] <= MAX_REQUESTS
                or counts["http_attempts"] != counts["auth_attempts"] + counts["message_attempts"]
                or not 1 <= counts["pages"] <= counts["message_attempts"]
                or counts["message_attempts"] != counts["pages"] + counts["token_retries"] + counts["rate_retries"]
                or counts["response_bytes"] > MAX_RESPONSE_BYTES or counts["raw_rows"] > MAX_ROWS
                or counts["messages"] != len(records) or counts["raw_rows"] != len(records) + counts["duplicates"]
                or any(counts[key] > 1 for key in ("refresh_attempts", "token_retries", "rate_retries"))
                or counts["auth_attempts"] < 2*counts["refresh_attempts"]):
            raise ValueError
        if any(counts["omitted_" + key] != sum(item["omissions"][key] for item in manifests)
               for key in ("files", "attachments", "blocks")):
            raise ValueError
        if _output_size(records, snapshot) > MAX_OUTPUT_BYTES:
            raise ValueError
        return deepcopy(snapshot)
    except (KeyError, TypeError, ValueError, UnicodeError, RecursionError, OverflowError,
            SlackMappingError, SlackReadError):
        raise SlackReadError("slack_snapshot_invalid") from None
