"""Fixed clocks and IDs for the package's own call sites.

Only calls made from gtm_connector modules get fixed values, with one exception: the MCP SDK's bearer and token
checks decide the same expiry as the package's authority, so they read the same clock. Other libraries keep the
real clock and real random IDs, so the HTTPS services and TLS still work. Each card runs in its own scope, named
by a key, so its IDs and times depend on that key alone and not on what ran before it.
"""

from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import hashlib
import importlib
import secrets
import sys
import types
from typing import Any, Iterator
import uuid


# The day the demo book is planned from, at 15:00 UTC. Every receipt time on a card starts here.
EPOCH = datetime(2027, 1, 15, 15, 0, 0, tzinfo=timezone.utc)
# The Slack reads happen the day after the books' planning date, as tests/test_books_slack.py has them.
SLACK_EPOCH = datetime(2027, 1, 16, 0, 0, 0, tzinfo=timezone.utc)
# The monotonic reading every timed package call sees, so every elapsed time is 0.
MONOTONIC = 1000.0


class Clock:
    """The wall and monotonic readings at every decision point: token and code expiry, the gateway's wall time
    and rate window, the brief deadlines, and Salesforce's operation budget. They stand still unless a fixture
    advances them, and each scope starts them at EPOCH and MONOTONIC."""

    def __init__(self) -> None:
        self.wall = EPOCH.timestamp()
        self.monotonic = MONOTONIC

    def advance(self, seconds: float) -> None:
        self.wall += seconds
        self.monotonic += seconds

    def now(self) -> datetime:
        return datetime.fromtimestamp(self.wall, timezone.utc)


CLOCK = Clock()

_REAL_UUID4 = uuid.uuid4
_REAL_TOKEN_URLSAFE = secrets.token_urlsafe


def _from_package(depth: int = 2) -> bool:
    return sys._getframe(depth).f_globals.get("__name__", "").startswith("gtm_connector")


class Scope:
    """The counters of one named scope. IDs are unique across scopes because the name is in each one."""

    def __init__(self, key: str, receipts: int = 0) -> None:
        self.key = key
        self.space = int.from_bytes(hashlib.sha256(key.encode()).digest()[:4], "big")
        self.ids = 0
        self.tokens = 0
        self.receipts = receipts
        self.issued: list[tuple[str, str]] = []

    def uuid(self) -> uuid.UUID:
        self.ids += 1
        value = uuid.UUID(f"{self.space:08x}-0000-4000-8000-{self.ids:012x}")
        self.issued.append(("id", str(value)))
        return value

    def token(self, size: int) -> str:
        self.tokens += 1
        length = -(-size * 4 // 3)
        base = f"t{self.space:08x}{self.tokens:08x}"
        value = (base * (length // len(base) + 1))[:length]
        self.issued.append(("token", value))
        return value

    def receipt_time(self) -> datetime:
        """Each receipt starts one second after the one before it in this scope."""
        moment = EPOCH + timedelta(seconds=self.receipts)
        self.receipts += 1
        self.issued.append(("time", moment.isoformat().replace("+00:00", "Z")))
        return moment


_current = Scope("setup")
_keys: set[str] = set()


@contextmanager
def scope(key: str, *, receipts: int = 0) -> Iterator[Scope]:
    """Run with counters that start fresh for this key. A key is used once per run. The receipt clock starts
    `receipts` seconds after EPOCH, so a sequence of scopes can keep one clock."""
    global _current
    if key in _keys:
        raise ValueError(f"oracle scope used twice: {key}")
    _keys.add(key)
    previous, _current = _current, Scope(key, receipts)
    readings = CLOCK.wall, CLOCK.monotonic
    CLOCK.wall, CLOCK.monotonic = EPOCH.timestamp(), MONOTONIC
    try:
        yield _current
    finally:
        _current = previous
        CLOCK.wall, CLOCK.monotonic = readings


def _uuid4() -> uuid.UUID:
    return _current.uuid() if _from_package() else _REAL_UUID4()


def _token_urlsafe(nbytes: int | None = None) -> str:
    if not _from_package():
        return _REAL_TOKEN_URLSAFE(nbytes)
    return _current.token(32 if nbytes is None else nbytes)


class _ReceiptClock(datetime):
    """audit.py's clock: one second per receipt in the current scope."""

    @classmethod
    def now(cls, tz: Any = None) -> datetime:  # type: ignore[override]
        moment = _current.receipt_time()
        return moment if tz is not None else moment.replace(tzinfo=None)


class _FixedClock(datetime):
    """The gateway's and selected briefs' wall clock: CLOCK's reading, EPOCH unless a fixture advances it."""

    @classmethod
    def now(cls, tz: Any = None) -> datetime:  # type: ignore[override]
        moment = CLOCK.now()
        return moment if tz is not None else moment.replace(tzinfo=None)


class _SlackClock(datetime):
    """The Slack reader's and sign-in's wall clock: it stands still at SLACK_EPOCH."""

    @classmethod
    def now(cls, tz: Any = None) -> datetime:  # type: ignore[override]
        return SLACK_EPOCH if tz is not None else SLACK_EPOCH.replace(tzinfo=None)


def _still_time() -> types.SimpleNamespace:
    """The time module, with its wall, monotonic and performance clocks read from CLOCK. sleep stays real."""
    import time
    fields = {name: getattr(time, name) for name in dir(time) if not name.startswith("__")}
    fields.update(time=lambda: CLOCK.wall, time_ns=lambda: int(CLOCK.wall * 1e9),
                  monotonic=lambda: CLOCK.monotonic, monotonic_ns=lambda: int(CLOCK.monotonic * 1e9),
                  perf_counter=lambda: CLOCK.monotonic, perf_counter_ns=lambda: int(CLOCK.monotonic * 1e9))
    return types.SimpleNamespace(**fields)


def install() -> None:
    """Fix the clocks and IDs at the call sites the package uses. Nothing under gtm_connector/ changes."""
    uuid.uuid4 = _uuid4
    secrets.token_urlsafe = _token_urlsafe
    modules = {name: importlib.import_module(f"gtm_connector.{name}")
               for name in ("audit", "auth", "gateway", "briefing", "selected_briefs", "salesforce", "slack_reader",
                            "slack_auth")}
    modules["audit"].datetime = _ReceiptClock  # type: ignore[attr-defined]
    for name in ("gateway", "selected_briefs"):
        modules[name].datetime = _FixedClock  # type: ignore[attr-defined]
    for name in ("slack_reader", "slack_auth"):
        modules[name].datetime = _SlackClock  # type: ignore[attr-defined]
        clock = getattr(modules[name], "_Clock")
        clock.monotonic = staticmethod(lambda: CLOCK.monotonic)
    # Authorization codes and tokens expire by auth.py's wall clock, the gateway checks a token against its own,
    # and a Salesforce read runs on an operation budget.
    for name in ("auth", "gateway", "briefing", "selected_briefs", "salesforce"):
        modules[name].time = _still_time()  # type: ignore[attr-defined]
    # The SDK checks the same expiry again on the request path, so it reads the same clock.
    for name in ("mcp.server.auth.middleware.bearer_auth", "mcp.server.auth.handlers.token"):
        importlib.import_module(name).time = _still_time()  # type: ignore[attr-defined]
    # The connector's rate and elapsed clock is a default argument, bound when the class was defined.
    connector = getattr(modules["gateway"], "Connector")
    import time
    connector.__init__.__defaults__ = tuple(
        (lambda: CLOCK.monotonic) if value is time.monotonic else value
        for value in connector.__init__.__defaults__)
