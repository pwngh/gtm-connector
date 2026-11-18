"""Receive one Slack authorization callback on exclusively owned loopback sockets."""

from collections.abc import Callable
import errno
import math
import re
import secrets
import select
import socket
import subprocess
import sys
import time
from typing import Any, TypeVar
from urllib.parse import parse_qsl


T = TypeVar("T")
CALLBACK_ERROR_CODES = frozenset({"invalid_redirect", "callback_unavailable", "browser_unavailable",
                                  "login_timeout", "login_denied", "oauth_rejected"})
MAX_REQUESTS = 128
MAX_REQUEST_LINE = 8192
MAX_HEADERS = 16384
MAX_QUERY = 8192
REQUEST_SECONDS = 2
BROWSER_SECONDS = 10
_UNAVAILABLE_FAMILY = {errno.EAFNOSUPPORT, errno.EPROTONOSUPPORT, errno.EADDRNOTAVAIL}
_SUCCESS = (b"<!doctype html><title>Slack authentication</title>"
            b"<p>Slack authentication is saved. Return to the terminal.</p>")
_FAILURE = (b"<!doctype html><title>Slack authentication</title>"
            b"<p>Slack authentication did not finish. Return to the terminal.</p>")
_INVALID = b"<!doctype html><title>Local callback</title><p>This request was not accepted.</p>"
_BROWSER_PROGRAM = """import sys, webbrowser
try:
    raw = sys.stdin.buffer.read(16385)
    if not raw or len(raw) > 16384:
        raise ValueError()
    opened = webbrowser.open(raw.decode('utf-8'), new=1, autoraise=True)
except Exception:
    sys.exit(1)
sys.exit(0 if opened else 1)
"""


class CallbackError(RuntimeError):
    """Expose only a fixed local failure code."""

    def __init__(self, code: str):
        self.code = code if code in CALLBACK_ERROR_CODES else "callback_unavailable"
        super().__init__(self.code)


class _InvalidRequest(Exception):
    def __init__(self, status: int = 400):
        self.status = status


def validate_redirect(redirect_uri: str) -> str:
    """Return an exact supported redirect without performing any I/O."""
    match = re.fullmatch(r"http://localhost:([1-9][0-9]{3,4})/slack/callback", redirect_uri) \
        if isinstance(redirect_uri, str) else None
    if match is None or not 1024 <= int(match[1]) <= 65535:
        raise CallbackError("invalid_redirect")
    return redirect_uri


def _listen(port: int) -> list[socket.socket]:
    listeners: list[socket.socket] = []
    try:
        for family, address in ((socket.AF_INET, ("127.0.0.1", port)), (socket.AF_INET6, ("::1", port))):
            listener = None
            try:
                listener = socket.socket(family, socket.SOCK_STREAM)
                if family == socket.AF_INET6:
                    listener.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                if hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
                    listener.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
                else:
                    # Reuse a closed connection's TIME_WAIT port, never an active listener.
                    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                listener.bind(address)
                listener.listen(8)
                listener.setblocking(False)
                listeners.append(listener)
            except OSError as error:
                if listener is not None:
                    listener.close()
                if error.errno not in _UNAVAILABLE_FAMILY:
                    raise CallbackError("callback_unavailable") from None
            except BaseException:
                if listener is not None:
                    listener.close()
                raise
        if not listeners:
            raise CallbackError("callback_unavailable")
        return listeners
    except BaseException:
        _close_listeners(listeners)
        raise


def _close_listeners(listeners: list[socket.socket]) -> None:
    for listener in listeners:
        listener.close()
    listeners.clear()


def _read_headers(connection: socket.socket, deadline: float) -> bytes:
    data = bytearray()
    request_deadline = min(deadline, time.monotonic() + REQUEST_SECONDS)
    while b"\r\n\r\n" not in data:
        remaining = request_deadline - time.monotonic()
        if remaining <= 0:
            raise _InvalidRequest()
        connection.settimeout(remaining)
        try:
            chunk = connection.recv(1024)
        except OSError:
            raise _InvalidRequest() from None
        if not chunk:
            raise _InvalidRequest()
        data.extend(chunk)
        line_end = data.find(b"\r\n")
        if (line_end < 0 and len(data) > MAX_REQUEST_LINE) or line_end + 2 > MAX_REQUEST_LINE:
            raise _InvalidRequest()
        if line_end >= 0 and len(data) - line_end - 2 > MAX_HEADERS:
            raise _InvalidRequest()
    head, trailing = bytes(data).split(b"\r\n\r\n", 1)
    if trailing:
        raise _InvalidRequest()
    return head


def _callback_fields(head: bytes, *, port: int, state: str) -> dict[str, str]:
    lines = head.split(b"\r\n")
    try:
        method, target, version = lines[0].decode("ascii").split(" ")
    except (UnicodeError, ValueError):
        raise _InvalidRequest() from None
    if method != "GET":
        raise _InvalidRequest(405)
    if version not in {"HTTP/1.0", "HTTP/1.1"} or not target.startswith("/") or "#" in target:
        raise _InvalidRequest()
    path, separator, query = target.partition("?")
    if path != "/slack/callback":
        raise _InvalidRequest(404)
    if not separator or len(query) > MAX_QUERY or len(lines) - 1 > 32:
        raise _InvalidRequest()
    headers: dict[str, str] = {}
    for line in lines[1:]:
        name, colon, header_value = line.partition(b":")
        if not colon or re.fullmatch(rb"[!#$%&'*+.^_`|~0-9A-Za-z-]+", name) is None:
            raise _InvalidRequest()
        if any(byte < 32 and byte != 9 or byte == 127 for byte in header_value):
            raise _InvalidRequest()
        key = name.decode("ascii").lower()
        if key in headers:
            raise _InvalidRequest()
        headers[key] = header_value.decode("latin-1").strip(" \t")
    if headers.get("host", "").lower() != f"localhost:{port}":
        raise _InvalidRequest()
    if "transfer-encoding" in headers or headers.get("content-length", "0") != "0":
        raise _InvalidRequest()
    if re.search(r"%(?![0-9A-Fa-f]{2})", query):
        raise _InvalidRequest()
    try:
        pairs = parse_qsl(query, keep_blank_values=True, strict_parsing=True,
                          encoding="utf-8", errors="strict", max_num_fields=5)
    except (UnicodeError, ValueError):
        raise _InvalidRequest() from None
    fields: dict[str, str] = {}
    for key, value in pairs:
        if key in fields or key not in {"state", "code", "error", "error_description", "error_uri"}:
            raise _InvalidRequest()
        if not value or any(ord(char) < 32 or ord(char) == 127 for char in value):
            raise _InvalidRequest()
        fields[key] = value
    supplied_state = fields.get("state", "")
    if not supplied_state.isascii() or len(supplied_state) > 128 or not secrets.compare_digest(supplied_state, state):
        raise _InvalidRequest()
    if ("code" in fields) == ("error" in fields):
        raise _InvalidRequest()
    if "code" in fields:
        code = fields["code"]
        if set(fields) != {"code", "state"} or len(code) > 4096 or not all(33 <= ord(c) <= 126 for c in code):
            raise _InvalidRequest()
    else:
        error = fields["error"]
        if len(error) > 128 or not all(33 <= ord(c) <= 126 for c in error):
            raise _InvalidRequest()
        if any(len(fields.get(key, "").encode("utf-8")) > 1024 for key in ("error_description", "error_uri")):
            raise _InvalidRequest()
    return fields


def _respond(connection: socket.socket, status: int, body: bytes, deadline: float) -> None:
    remaining = min(1.0, deadline - time.monotonic())
    if remaining <= 0:
        return
    reasons = {200: "OK", 400: "Bad Request", 404: "Not Found", 405: "Method Not Allowed", 500: "Internal Server Error"}
    response = (f"HTTP/1.1 {status} {reasons[status]}\r\nContent-Type: text/html; charset=utf-8\r\n"
                f"Content-Length: {len(body)}\r\nCache-Control: no-store\r\nReferrer-Policy: no-referrer\r\n"
                "X-Content-Type-Options: nosniff\r\nContent-Security-Policy: default-src 'none'; "
                "frame-ancestors 'none'; base-uri 'none'\r\nConnection: close\r\n\r\n").encode("ascii") + body
    try:
        connection.settimeout(remaining)
        connection.sendall(response)
    except OSError:
        pass  # A disconnected browser does not undo a completed private commit.


def _stop_helper(process: subprocess.Popen) -> None:
    if process.poll() is not None:
        return
    try:
        process.terminate()
        try:
            process.wait(timeout=1)
        except subprocess.TimeoutExpired:
            process.kill()
            process.wait(timeout=1)
    except OSError:
        pass


def _open_system_browser(url: str, *, deadline: float) -> bool:
    """Bound browser opening and keep its URL out of the helper's arguments."""
    remaining = min(float(BROWSER_SECONDS), deadline - time.monotonic())
    if remaining <= 0:
        raise CallbackError("login_timeout")
    process = None
    try:
        process = subprocess.Popen([sys.executable, "-I", "-c", _BROWSER_PROGRAM], stdin=subprocess.PIPE,
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, close_fds=True)
        process.communicate(input=url.encode("utf-8"), timeout=remaining)
        return process.returncode == 0
    except subprocess.TimeoutExpired:
        code = "login_timeout" if time.monotonic() >= deadline else "browser_unavailable"
        raise CallbackError(code) from None
    except OSError:
        raise CallbackError("browser_unavailable") from None
    finally:
        if process is not None:
            _stop_helper(process)
            if process.stdin is not None:
                process.stdin.close()


def receive_consent(redirect_uri: str, *, authorization_url_for_state: Callable[[str], str],
                    finish: Callable[..., T], open_browser: Callable[[str], bool] | None = None,
                    timeout_seconds: float = 600) -> T:
    """Finish one validated consent synchronously before showing browser success."""
    validate_redirect(redirect_uri)
    if (isinstance(timeout_seconds, bool) or not isinstance(timeout_seconds, (int, float))
            or not math.isfinite(timeout_seconds) or not 0 < timeout_seconds <= 600):
        raise CallbackError("invalid_redirect")
    port = int(redirect_uri.split(":")[2].split("/")[0])
    deadline = time.monotonic() + timeout_seconds
    state = secrets.token_urlsafe(32)
    listeners = _listen(port)
    try:
        try:
            url = authorization_url_for_state(state)
            if (not isinstance(url, str) or not url or len(url.encode("utf-8")) > 16384
                    or any(ord(char) < 32 or ord(char) == 127 for char in url)):
                raise CallbackError("browser_unavailable")
            opened = _open_system_browser(url, deadline=deadline) if open_browser is None else open_browser(url)
        except CallbackError:
            raise
        except Exception:
            raise CallbackError("browser_unavailable") from None
        if time.monotonic() >= deadline:
            raise CallbackError("login_timeout")
        if opened is not True:
            raise CallbackError("browser_unavailable")
        for _ in range(MAX_REQUESTS):
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise CallbackError("login_timeout")
            try:
                readable, _, _ = select.select(listeners, [], [], remaining)
                if not readable:
                    raise CallbackError("login_timeout")
                connection, _ = readable[0].accept()
            except OSError:
                raise CallbackError("callback_unavailable") from None
            with connection:
                try:
                    fields = _callback_fields(_read_headers(connection, deadline), port=port, state=state)
                except _InvalidRequest as error:
                    _respond(connection, error.status, _INVALID, deadline)
                    continue
                if time.monotonic() >= deadline:
                    raise CallbackError("login_timeout")
                _close_listeners(listeners)
                if "error" in fields:
                    _respond(connection, 400, _FAILURE, deadline)
                    code = "login_denied" if fields["error"] == "access_denied" else "oauth_rejected"
                    raise CallbackError(code)
                try:
                    result: Any = finish(fields["code"], deadline=deadline)
                    if time.monotonic() >= deadline:
                        raise CallbackError("login_timeout")
                except BaseException:
                    _respond(connection, 500, _FAILURE, deadline)
                    raise
                _respond(connection, 200, _SUCCESS, deadline)
                return result
        raise CallbackError("callback_unavailable")
    finally:
        _close_listeners(listeners)
