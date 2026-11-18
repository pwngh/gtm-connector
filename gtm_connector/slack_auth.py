"""One private Slack user connection, with fixed requests and bounded credentials."""

import asyncio
import base64
from contextlib import contextmanager
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import time
from urllib.parse import urlencode, urlsplit


ALLOWED_USER_SCOPES = ("channels:history", "groups:history")
DEFAULT_USER_SCOPES = ("channels:history",)
LOGIN_SECONDS = 600
OPERATION_SECONDS = 20
MAX_RESPONSE_BYTES = 65536
MAX_CREDENTIAL_BYTES = 65536
EXPIRY_MARGIN_SECONDS = 30
REFRESH_LIFETIME_SECONDS = 30 * 24 * 60 * 60
_TOKEN_URL = "https://slack.com/api/oauth.v2.access"
_AUTHORIZE_URL = "https://slack.com/oauth/v2/authorize"
_IDENTITY_URL = "https://slack.com/api/auth.test"
_REVOKE_URL = "https://slack.com/api/auth.revoke"
_ERROR_EXITS = {
    "slack_invalid_configuration": 2, "slack_connection_busy": 1,
    "slack_private_state_invalid": 3, "slack_credentials_invalid": 4,
    "slack_not_connected": 3, "slack_browser_unavailable": 1,
    "slack_callback_unavailable": 1, "slack_login_timeout": 1,
    "slack_login_denied": 3, "slack_oauth_rejected": 3,
    "slack_scope_mismatch": 3, "slack_identity_mismatch": 3,
    "slack_token_type_unsupported": 3, "slack_reconnect_required": 3,
    "slack_response_invalid": 5, "slack_rate_limited": 5,
    "slack_timeout": 5, "slack_unavailable": 5, "slack_storage_failed": 1,
}
SLACK_AUTH_ERROR_CODES = frozenset(_ERROR_EXITS)
_CATEGORIES = {1: "local_failure", 2: "usage_error", 3: "refused", 4: "check_failed", 5: "upstream_failure"}
_KEYS = {"schema_version", "provider", "client_id", "redirect_uri", "identity", "workspace_origin",
         "requested_scopes", "granted_scopes", "credential_state", "access_token", "refresh_token",
         "access_obtained_at", "access_expires_at", "refresh_obtained_at", "last_verified_at"}


class SlackAuthError(RuntimeError):
    """Expose only a fixed code, never a provider response or private value."""

    def __init__(self, code):
        if code not in SLACK_AUTH_ERROR_CODES:
            raise ValueError("Unknown Slack authentication error code.")
        self.code = code
        self.exit_code = _ERROR_EXITS[code]
        self.category = _CATEGORIES[self.exit_code]
        super().__init__(code)


@dataclass(frozen=True)
class SlackAccess:
    access_token: str = field(repr=False)
    identity: dict = field(repr=False)
    scopes: tuple[str, ...]
    expires_at: str | None
    workspace_origin: str | None = field(default=None, repr=False)


class _Clock:
    monotonic = staticmethod(time.monotonic)

    @staticmethod
    def now():
        return datetime.now(timezone.utc)


def _unique(pairs):
    value = {}
    for key, item in pairs:
        if key in value:
            raise ValueError
        value[key] = item
    return value


def _reject_constant(_):
    raise ValueError


def _decode(raw):
    value = json.loads(raw.decode("utf-8"), object_pairs_hook=_unique, parse_constant=_reject_constant)
    if not isinstance(value, dict):
        raise ValueError
    return value


def _stamp(value):
    return value.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")


def _date(value):
    if not isinstance(value, str) or not re.fullmatch(r"[0-9]{4}-[0-9T:.-]{14,25}Z", value):
        raise ValueError
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    if _stamp(parsed) != value:
        raise ValueError
    return parsed


def _identifier(value, prefix):
    if not isinstance(value, str) or not re.fullmatch(f"[{prefix}][A-Z0-9]{{8,63}}", value):
        raise ValueError
    return value


def _token(value):
    if not isinstance(value, str) or not 1 <= len(value) <= 8192 or any(not 33 <= ord(c) <= 126 for c in value):
        raise ValueError
    return value


def _scopes(value):
    if (not isinstance(value, (tuple, list)) or not value or any(not isinstance(v, str) for v in value)
            or len(value) != len(set(value)) or not set(value) <= set(ALLOWED_USER_SCOPES)):
        raise ValueError
    return tuple(scope for scope in ALLOWED_USER_SCOPES if scope in value)


def _scope_text(value):
    if not isinstance(value, str):
        raise SlackAuthError("slack_scope_mismatch")
    try:
        return _scopes(value.split(","))
    except ValueError:
        raise SlackAuthError("slack_scope_mismatch") from None


def _redirect(value):
    from .slack_callback import CallbackError, validate_redirect
    try:
        return validate_redirect(value)
    except CallbackError:
        raise ValueError from None


def validate_configuration(*, client_id, redirect_uri, user_scopes=DEFAULT_USER_SCOPES, expected_team_id=None):
    """Validate all public configuration before private files or browser access."""
    try:
        if not isinstance(client_id, str) or not re.fullmatch(r"[0-9]{1,32}\.[0-9]{1,32}", client_id):
            raise ValueError
        scopes = _scopes(user_scopes)
        redirect = _redirect(redirect_uri)
        if expected_team_id is not None:
            _identifier(expected_team_id, "T")
    except (ValueError, TypeError):
        raise SlackAuthError("slack_invalid_configuration") from None
    return {"client_id": client_id, "redirect_uri": redirect, "user_scopes": scopes,
            "expected_team_id": expected_team_id}


def _origin(value):
    try:
        if (not isinstance(value, str) or not value.isascii() or len(value) > 256
                or any(c.isspace() or not c.isprintable() for c in value)):
            return None
        parsed = urlsplit(value)
        if (parsed.scheme != "https" or parsed.username is not None or parsed.password is not None
                or parsed.port not in (None, 443) or parsed.query or parsed.fragment or parsed.path not in ("", "/")
                or not re.fullmatch(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.slack\.com", parsed.hostname or "")):
            return None
        return "https://" + str(parsed.hostname)
    except (ValueError, UnicodeError):
        return None


def _ok(document, operation):
    if not isinstance(document, dict) or type(document.get("ok")) is not bool:
        raise SlackAuthError("slack_response_invalid")
    if document["ok"]:
        return
    error = document.get("error")
    if error in ("ratelimited", "rate_limited"):
        code = "slack_rate_limited"
    elif error in ("invalid_auth", "token_revoked", "token_expired", "account_inactive", "not_authed",
                   "invalid_refresh_token", "refresh_token_expired", "invalid_grant") and operation != "exchange":
        code = "slack_reconnect_required"
    elif error in ("not_allowed_token_type", "not_allowed_token"):
        code = "slack_token_type_unsupported"
    else:
        code = "slack_oauth_rejected"
    raise SlackAuthError(code)


class SlackProvider:
    """Four fixed provider operations; each consumes the original deadline."""

    def __init__(self, *, _transport=None, _clock=None):
        self._transport = _transport
        self._clock = _clock or _Clock()

    def exchange(self, code, verifier, client_id, redirect_uri, deadline):
        return self._request("POST", _TOKEN_URL, deadline, "exchange", data={
            "grant_type": "authorization_code", "client_id": client_id, "code": code,
            "redirect_uri": redirect_uri, "code_verifier": verifier})

    def refresh(self, refresh_token, client_id, deadline):
        return self._request("POST", _TOKEN_URL, deadline, "refresh", data={
            "grant_type": "refresh_token", "client_id": client_id, "refresh_token": refresh_token})

    def identity(self, access_token, deadline):
        return self._request("POST", _IDENTITY_URL, deadline, "identity", token=access_token)

    def revoke(self, access_token, deadline):
        return self._request("GET", _REVOKE_URL, deadline, "revoke", token=access_token)

    def _request(self, method, url, deadline, operation, *, data=None, token=None):
        import httpx2

        async def bounded():
            remaining = deadline - self._clock.monotonic()
            if remaining <= 0:
                raise SlackAuthError("slack_timeout")
            async with asyncio.timeout(remaining):
                timeout = httpx2.Timeout(min(5, remaining), connect=min(3, remaining),
                                         write=min(5, remaining), pool=min(3, remaining))
                headers = {"Accept": "application/json", "Accept-Encoding": "identity"}
                if token is not None:
                    headers["Authorization"] = "Bearer " + token
                async with httpx2.AsyncClient(transport=self._transport, trust_env=False,
                                             follow_redirects=False, timeout=timeout) as client:
                    async with client.stream(method, url, data=data, headers=headers) as response:
                        if response.status_code == 429:
                            raise SlackAuthError("slack_rate_limited")
                        if response.status_code >= 500:
                            raise SlackAuthError("slack_unavailable")
                        if response.status_code in (401, 403):
                            raise SlackAuthError("slack_oauth_rejected" if operation == "exchange"
                                                 else "slack_reconnect_required")
                        if response.status_code != 200:
                            raise SlackAuthError("slack_response_invalid")
                        encoding = response.headers.get("content-encoding", "identity").lower()
                        length = response.headers.get("content-length")
                        if encoding != "identity" or (length is not None and (
                                not re.fullmatch(r"[0-9]{1,10}", length) or int(length) > MAX_RESPONSE_BYTES)):
                            raise SlackAuthError("slack_response_invalid")
                        body = bytearray()
                        # Accepted responses use identity encoding, so decoded and raw bytes agree.
                        async for part in response.aiter_bytes():
                            body.extend(part)
                            if len(body) > MAX_RESPONSE_BYTES:
                                raise SlackAuthError("slack_response_invalid")
                        document = _decode(bytes(body))
                        _ok(document, operation)
                if self._clock.monotonic() >= deadline:
                    raise SlackAuthError("slack_timeout")
                return document

        try:
            return asyncio.run(bounded())
        except (TimeoutError, httpx2.TimeoutException):
            raise SlackAuthError("slack_timeout") from None
        except httpx2.HTTPError:
            raise SlackAuthError("slack_unavailable") from None
        except (ValueError, UnicodeError, RecursionError):
            raise SlackAuthError("slack_response_invalid") from None


def _identity(value):
    if not isinstance(value, dict) or set(value) != {"app_id", "team_id", "user_id", "enterprise_id"}:
        raise ValueError
    _identifier(value["app_id"], "A")
    _identifier(value["team_id"], "T")
    _identifier(value["user_id"], "UW")
    if value["enterprise_id"] is not None:
        _identifier(value["enterprise_id"], "E")


def _enterprise(document):
    value = document.get("enterprise")
    if value is None:
        return None
    if not isinstance(value, dict):
        raise ValueError
    return _identifier(value.get("id"), "E")


def _token_fields(user, obtained_at, *, rotating_required=False):
    if not isinstance(user, dict):
        raise ValueError
    _no_bot(user)
    if user.get("token_type") != "user":
        raise SlackAuthError("slack_token_type_unsupported")
    result = {"access_token": _token(user.get("access_token")), "access_obtained_at": _stamp(obtained_at),
              "refresh_token": None, "refresh_obtained_at": None, "access_expires_at": None}
    rotating = "refresh_token" in user or "expires_in" in user
    if rotating_required and not rotating:
        raise ValueError
    if rotating:
        lifetime = user.get("expires_in")
        if type(lifetime) is not int or not 1 <= lifetime <= 86400:
            raise ValueError
        result.update(refresh_token=_token(user.get("refresh_token")), refresh_obtained_at=_stamp(obtained_at),
                      access_expires_at=_stamp(obtained_at + timedelta(seconds=lifetime)))
    return result


def _no_bot(document):
    if ("bot_user_id" in document or "bot_id" in document or document.get("is_enterprise_install") is True
            or ("is_enterprise_install" in document and type(document["is_enterprise_install"]) is not bool)):
        raise SlackAuthError("slack_token_type_unsupported")


def _parse_initial(document, *, configuration, obtained_at):
    _ok(document, "exchange")
    _no_bot(document)
    if any(key in document for key in ("access_token", "refresh_token", "token_type")):
        raise SlackAuthError("slack_token_type_unsupported")
    try:
        user = document.get("authed_user")
        token = _token_fields(user, obtained_at)
        scopes = _scope_text(user.get("scope"))
        if scopes != configuration["user_scopes"]:
            raise SlackAuthError("slack_scope_mismatch")
        identity = {"app_id": _identifier(document.get("app_id"), "A"),
                    "team_id": _identifier(document["team"]["id"], "T"),
                    "user_id": _identifier(user.get("id"), "UW"), "enterprise_id": _enterprise(document)}
        if configuration["expected_team_id"] not in (None, identity["team_id"]):
            raise SlackAuthError("slack_identity_mismatch")
        return {"schema_version": 1, "provider": "slack", "client_id": configuration["client_id"],
                "redirect_uri": configuration["redirect_uri"], "identity": identity, "workspace_origin": None,
                "requested_scopes": list(scopes), "granted_scopes": list(scopes),
                "credential_state": "ready", "last_verified_at": None, **token}
    except (ValueError, TypeError, KeyError, OverflowError):
        raise SlackAuthError("slack_response_invalid") from None


def _parse_refresh(document, *, previous, obtained_at):
    _ok(document, "refresh")
    _no_bot(document)
    nested = "authed_user" in document
    if nested and any(key in document for key in ("access_token", "refresh_token", "token_type")):
        raise SlackAuthError("slack_token_type_unsupported")
    try:
        user = document["authed_user"] if nested else document
        token = _token_fields(user, obtained_at, rotating_required=True)
        scopes = _scope_text(user.get("scope"))
        if scopes != tuple(previous["requested_scopes"]):
            raise SlackAuthError("slack_scope_mismatch")
        pin = previous["identity"]
        supplied = []
        if "id" in user:
            supplied.append(("user_id", _identifier(user["id"], "UW")))
        if "user_id" in document:
            supplied.append(("user_id", _identifier(document["user_id"], "UW")))
        if "app_id" in document:
            supplied.append(("app_id", _identifier(document["app_id"], "A")))
        if "team" in document:
            supplied.append(("team_id", _identifier(document["team"]["id"], "T")))
        if "team_id" in document:
            supplied.append(("team_id", _identifier(document["team_id"], "T")))
        if "enterprise" in document:
            supplied.append(("enterprise_id", _enterprise(document)))
        if "enterprise_id" in document:
            enterprise = document["enterprise_id"]
            supplied.append(("enterprise_id", None if enterprise is None else _identifier(enterprise, "E")))
        if any(pin[key] != value for key, value in supplied):
            raise SlackAuthError("slack_identity_mismatch")
        return {**previous, **token, "credential_state": "identity_unchecked", "last_verified_at": None}
    except (ValueError, TypeError, KeyError, OverflowError):
        raise SlackAuthError("slack_response_invalid") from None


def _verify_identity(document, *, candidate):
    _ok(document, "identity")
    _no_bot(document)
    try:
        pin = candidate["identity"]
        team = _identifier(document.get("team_id"), "T")
        user = _identifier(document.get("user_id"), "UW")
        if team != pin["team_id"] or user != pin["user_id"]:
            raise SlackAuthError("slack_identity_mismatch")
        if "enterprise_id" in document and document["enterprise_id"] != pin["enterprise_id"]:
            raise SlackAuthError("slack_identity_mismatch")
        return _origin(document.get("url"))
    except (ValueError, TypeError):
        raise SlackAuthError("slack_response_invalid") from None


def _validate_document(document):
    try:
        if (not isinstance(document, dict) or set(document) != _KEYS or type(document["schema_version"]) is not int
                or document["schema_version"] != 1 or document["provider"] != "slack"):
            raise ValueError
        config = validate_configuration(client_id=document["client_id"], redirect_uri=document["redirect_uri"],
                                        user_scopes=document["requested_scopes"])
        if (list(config["user_scopes"]) != document["requested_scopes"]
                or document["granted_scopes"] != document["requested_scopes"]):
            raise ValueError
        _identity(document["identity"])
        origin = document["workspace_origin"]
        if origin is not None and (not isinstance(origin, str) or _origin(origin) != origin):
            raise ValueError
        _token(document["access_token"])
        obtained = _date(document["access_obtained_at"])
        if document["refresh_token"] is None:
            if document["access_expires_at"] is not None or document["refresh_obtained_at"] is not None:
                raise ValueError
        else:
            _token(document["refresh_token"])
            expiry = _date(document["access_expires_at"])
            if not 1 <= (expiry - obtained).total_seconds() <= 86400:
                raise ValueError
            if _date(document["refresh_obtained_at"]) != obtained:
                raise ValueError
        state = document["credential_state"]
        if state not in ("ready", "refresh_pending", "identity_unchecked"):
            raise ValueError
        if document["last_verified_at"] is not None:
            if _date(document["last_verified_at"]) < obtained:
                raise ValueError
        elif state == "ready":
            raise ValueError
    except (ValueError, TypeError, KeyError, SlackAuthError):
        raise SlackAuthError("slack_credentials_invalid") from None
    return document


def _private(info, *, directory=False):
    return (info.st_uid == os.getuid() and stat.S_IMODE(info.st_mode) == (0o700 if directory else 0o600)
            and (stat.S_ISDIR(info.st_mode) if directory else stat.S_ISREG(info.st_mode) and info.st_nlink == 1))


def _entry(directory_fd, name):
    try:
        info = os.stat(name, dir_fd=directory_fd, follow_symlinks=False)
    except FileNotFoundError:
        return None
    if not _private(info):
        raise SlackAuthError("slack_private_state_invalid")
    return info


@contextmanager
def _connection_lock(directory, *, create):
    import fcntl

    descriptor = lock = None
    try:
        path = Path(directory).absolute()
        if any(not c.isprintable() for c in str(path)):
            raise SlackAuthError("slack_invalid_configuration")
        path = path.parent.resolve() / path.name
        if not os.path.lexists(path):
            if not create:
                yield None
                return
            missing = []
            ancestor = path
            while not ancestor.exists():
                missing.append(ancestor)
                ancestor = ancestor.parent
            for target in reversed(missing):
                target.mkdir(mode=0o700)
        if not _private(path.lstat(), directory=True):
            raise SlackAuthError("slack_private_state_invalid")
        descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        if not _private(os.fstat(descriptor), directory=True):
            raise SlackAuthError("slack_private_state_invalid")
        info = _entry(descriptor, "connection.lock")
        if info is None:
            if os.listdir(descriptor):
                raise SlackAuthError("slack_private_state_invalid")
            if not create:
                yield None
                return
        flags = os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK
        if create and info is None:
            flags |= os.O_CREAT | os.O_EXCL
        lock = os.open("connection.lock", flags, 0o600, dir_fd=descriptor)
        opened = os.fstat(lock)
        current = _entry(descriptor, "connection.lock")
        if (not _private(opened) or current is None
                or (opened.st_dev, opened.st_ino) != (current.st_dev, current.st_ino)):
            raise SlackAuthError("slack_private_state_invalid")
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise SlackAuthError("slack_connection_busy") from None
        if not set(os.listdir(descriptor)) <= {"connection.lock", "credentials.json"}:
            raise SlackAuthError("slack_private_state_invalid")
        _entry(descriptor, "credentials.json")
        yield descriptor
    except OSError:
        raise SlackAuthError("slack_storage_failed") from None
    finally:
        if lock is not None:
            os.close(lock)
        if descriptor is not None:
            os.close(descriptor)


def _read_credentials(directory_fd):
    info = _entry(directory_fd, "credentials.json")
    if info is None:
        return None
    descriptor = os.open("credentials.json", os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory_fd)
    with os.fdopen(descriptor, "rb") as source:
        opened = os.fstat(source.fileno())
        if not _private(opened) or (opened.st_dev, opened.st_ino) != (info.st_dev, info.st_ino):
            raise SlackAuthError("slack_private_state_invalid")
        raw = source.read(MAX_CREDENTIAL_BYTES + 1)
    try:
        if len(raw) > MAX_CREDENTIAL_BYTES:
            raise ValueError
        document = _decode(raw)
    except (ValueError, UnicodeError, RecursionError):
        raise SlackAuthError("slack_credentials_invalid") from None
    return _validate_document(document)


def _write_credentials(directory_fd, document):
    _validate_document(document)
    raw = json.dumps(document, ensure_ascii=True, allow_nan=False, sort_keys=True).encode("utf-8")
    if len(raw) > MAX_CREDENTIAL_BYTES:
        raise SlackAuthError("slack_storage_failed")
    _entry(directory_fd, "credentials.json")
    name = ".credentials-" + secrets.token_hex(16) + ".tmp"
    descriptor = None
    try:
        descriptor = os.open(name, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory_fd)
        with os.fdopen(descriptor, "wb") as output:
            descriptor = None
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        _entry(directory_fd, "credentials.json")
        os.replace(name, "credentials.json", src_dir_fd=directory_fd, dst_dir_fd=directory_fd)
        os.fsync(directory_fd)
    except OSError:
        raise SlackAuthError("slack_storage_failed") from None
    finally:
        if descriptor is not None:
            os.close(descriptor)
        try:
            os.unlink(name, dir_fd=directory_fd)
        except FileNotFoundError:
            pass


def _remove_credentials(directory_fd):
    if _entry(directory_fd, "credentials.json") is None:
        return False
    os.unlink("credentials.json", dir_fd=directory_fd)
    os.fsync(directory_fd)
    return True


def _needs_login(document, now):
    if document["credential_state"] == "refresh_pending" or now < _date(document["access_obtained_at"]):
        return True
    if document["last_verified_at"] is not None and now < _date(document["last_verified_at"]):
        return True
    refresh_at = document["refresh_obtained_at"]
    return refresh_at is not None and (now < _date(refresh_at)
        or now >= _date(refresh_at) + timedelta(seconds=REFRESH_LIFETIME_SECONDS))


def _public_status(document, *, verification, now):
    return {"schema_version": 1, "configured": document is not None,
            "credential_state": "missing" if document is None else document["credential_state"],
            "verification": verification, "verified_component": "authentication",
            "requested_scopes": [] if document is None else list(document["requested_scopes"]),
            "granted_scopes": [] if document is None else list(document["granted_scopes"]),
            "rotation": None if document is None else document["refresh_token"] is not None,
            "access_expires_at": None if document is None else document["access_expires_at"],
            "last_verified_at": None if document is None else document["last_verified_at"],
            "needs_login": document is None or _needs_login(document, now)}


def _progress(callback, step, status):
    if callback is not None:
        callback(step, status)


def _deadline(clock, deadline):
    if clock.monotonic() >= deadline:
        raise SlackAuthError("slack_timeout")


def _verify_and_save(directory_fd, candidate, *, provider, clock, deadline, progress, persisted=False):
    _deadline(clock, deadline)
    _progress(progress, "identity", "running")
    try:
        origin = _verify_identity(provider.identity(candidate["access_token"], deadline), candidate=candidate)
    except SlackAuthError as exc:
        if persisted and exc.code in ("slack_identity_mismatch", "slack_reconnect_required",
                                       "slack_token_type_unsupported"):
            # This state means unusable credentials, not proof of an in-flight refresh.
            _write_credentials(directory_fd, {**candidate, "credential_state": "refresh_pending"})
        raise
    _deadline(clock, deadline)
    now = clock.now()
    if now < _date(candidate["access_obtained_at"]) or (candidate["access_expires_at"] is not None
            and now >= _date(candidate["access_expires_at"])):
        raise SlackAuthError("slack_reconnect_required")
    _progress(progress, "identity", "passed")
    ready = {**candidate, "credential_state": "ready", "last_verified_at": _stamp(now), "workspace_origin": origin}
    _progress(progress, "save", "running")
    _deadline(clock, deadline)
    _write_credentials(directory_fd, ready)
    _deadline(clock, deadline)
    _progress(progress, "save", "passed")
    return ready


def connect_slack(connection_dir, *, client_id, redirect_uri, user_scopes=DEFAULT_USER_SCOPES,
                  expected_team_id=None, progress=None, _provider=None, _open_browser=None, _clock=None):
    """Authorize one existing public app and publish only a verified user grant."""
    configuration = validate_configuration(client_id=client_id, redirect_uri=redirect_uri,
                                           user_scopes=user_scopes, expected_team_id=expected_team_id)
    from .slack_callback import CallbackError, receive_consent
    clock = _clock or _Clock()
    provider = _provider or SlackProvider(_clock=clock)
    with _connection_lock(connection_dir, create=True) as directory_fd:
        previous = _read_credentials(directory_fd)
        verifier = secrets.token_urlsafe(32)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode("ascii")).digest()).rstrip(b"=").decode()

        def authorization_url_for_state(state):
            fields = {"client_id": client_id, "scope": "", "user_scope": ",".join(configuration["user_scopes"]),
                      "redirect_uri": redirect_uri, "state": state, "code_challenge": challenge,
                      "code_challenge_method": "S256"}
            if expected_team_id is not None:
                fields["team"] = expected_team_id
            return _AUTHORIZE_URL + "?" + urlencode(fields)

        def finish(code, *, deadline):
            deadline = min(deadline, clock.monotonic() + OPERATION_SECONDS)
            _deadline(clock, deadline)
            _progress(progress, "exchange", "running")
            obtained = clock.now()
            candidate = _parse_initial(provider.exchange(code, verifier, client_id, redirect_uri, deadline),
                                       configuration=configuration, obtained_at=obtained)
            if previous is not None and candidate["identity"] != previous["identity"]:
                raise SlackAuthError("slack_identity_mismatch")
            ready = _verify_and_save(directory_fd, candidate, provider=provider, clock=clock, deadline=deadline,
                                     progress=progress)
            return _public_status(ready, verification="live", now=clock.now())

        _progress(progress, "browser", "waiting")
        try:
            return receive_consent(redirect_uri, authorization_url_for_state=authorization_url_for_state,
                                   finish=finish, open_browser=_open_browser, timeout_seconds=LOGIN_SECONDS)
        except CallbackError as exc:
            codes = {"invalid_redirect": "slack_invalid_configuration",
                     "callback_unavailable": "slack_callback_unavailable",
                     "browser_unavailable": "slack_browser_unavailable", "login_timeout": "slack_login_timeout",
                     "login_denied": "slack_login_denied", "oauth_rejected": "slack_oauth_rejected"}
            raise SlackAuthError(codes[exc.code]) from None


def _usable(directory_fd, document, *, provider, clock, deadline, progress, check):
    if document is None:
        raise SlackAuthError("slack_not_connected")
    if _needs_login(document, clock.now()):
        raise SlackAuthError("slack_reconnect_required")
    if document["credential_state"] == "identity_unchecked":
        return _verify_and_save(directory_fd, document, provider=provider, clock=clock, deadline=deadline,
                                progress=progress, persisted=True)
    expiry = document["access_expires_at"]
    if expiry is not None and clock.now() + timedelta(seconds=EXPIRY_MARGIN_SECONDS) >= _date(expiry):
        pending = {**document, "credential_state": "refresh_pending"}
        _deadline(clock, deadline)
        _write_credentials(directory_fd, pending)
        _deadline(clock, deadline)
        _progress(progress, "refresh", "running")
        obtained = clock.now()
        replacement = _parse_refresh(provider.refresh(document["refresh_token"], document["client_id"], deadline),
                                     previous=document, obtained_at=obtained)
        _write_credentials(directory_fd, replacement)
        return _verify_and_save(directory_fd, replacement, provider=provider, clock=clock, deadline=deadline,
                                progress=progress, persisted=True)
    if check:
        # A failed quarantine write must not leave a known-invalid token reusable as ready.
        unchecked = {**document, "credential_state": "identity_unchecked"}
        _deadline(clock, deadline)
        _write_credentials(directory_fd, unchecked)
        return _verify_and_save(directory_fd, unchecked, provider=provider, clock=clock, deadline=deadline,
                                progress=progress, persisted=True)
    return document


def slack_status(connection_dir, *, check=False, progress=None, _provider=None, _clock=None):
    """Inspect saved metadata, or explicitly verify authentication without reading messages."""
    clock = _clock or _Clock()
    deadline = clock.monotonic() + OPERATION_SECONDS
    with _connection_lock(connection_dir, create=False) as directory_fd:
        document = None if directory_fd is None else _read_credentials(directory_fd)
        if check:
            document = _usable(directory_fd, document, provider=_provider or SlackProvider(_clock=clock),
                               clock=clock, deadline=deadline, progress=progress, check=True)
        return _public_status(document, verification="live" if check else "saved", now=clock.now())


def get_slack_access(connection_dir, *, required_scopes, _provider=None, _clock=None):
    """Return private usable access; no browser flow or source read occurs here."""
    try:
        required = _scopes(required_scopes)
    except ValueError:
        raise SlackAuthError("slack_invalid_configuration") from None
    clock = _clock or _Clock()
    deadline = clock.monotonic() + OPERATION_SECONDS
    with _connection_lock(connection_dir, create=False) as directory_fd:
        document = None if directory_fd is None else _read_credentials(directory_fd)
        if document is not None and not set(required) <= set(document["granted_scopes"]):
            raise SlackAuthError("slack_scope_mismatch")
        document = _usable(directory_fd, document, provider=_provider or SlackProvider(_clock=clock),
                           clock=clock, deadline=deadline, progress=None, check=False)
        return SlackAccess(document["access_token"], dict(document["identity"]), tuple(document["granted_scopes"]),
                           document["access_expires_at"], document["workspace_origin"])


def disconnect_slack(connection_dir, *, revoke=False, progress=None, _provider=None, _clock=None):
    """Remove local credentials, optionally revoking only the stored access token."""
    clock = _clock or _Clock()
    deadline = clock.monotonic() + OPERATION_SECONDS
    result = {"schema_version": 1, "status": "disconnected", "local_credentials_removed": False,
              "remote_revoke_requested": revoke, "remote_access_token_revoked": False, "revocation_error": None}
    with _connection_lock(connection_dir, create=False) as directory_fd:
        document = None if directory_fd is None else _read_credentials(directory_fd)
        if revoke and document is not None:
            _progress(progress, "revoke", "running")
            try:
                provider = _provider or SlackProvider(_clock=clock)
                response = provider.revoke(document["access_token"], deadline)
                _ok(response, "revoke")
                if response.get("revoked") is not True:
                    raise SlackAuthError("slack_response_invalid")
                _deadline(clock, deadline)
                result["remote_access_token_revoked"] = True
                _progress(progress, "revoke", "passed")
            except SlackAuthError as exc:
                result["revocation_error"] = exc.code
        elif revoke:
            result["revocation_error"] = "slack_not_connected"
        if document is not None:
            result["local_credentials_removed"] = _remove_credentials(directory_fd)
    return result
