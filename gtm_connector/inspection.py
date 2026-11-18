"""Read filters and change streams for private audit and usage inspection."""

from datetime import datetime, timezone
import json
import math
import re
import time


def _timestamp(value, label):
    try:
        timestamp = datetime.fromisoformat(value)
    except (TypeError, ValueError):
        raise ValueError(f"{label} must be an ISO 8601 timestamp with a UTC offset.") from None
    if timestamp.tzinfo is None or timestamp.utcoffset() is None:
        raise ValueError(f"{label} must include its UTC offset.")
    return timestamp.astimezone(timezone.utc)


class ReceiptFilters:
    """Validate filters before reading files and keep SQL values parameterized."""

    def __init__(self, *, tool=None, caller=None, since=None, until=None, status=None):
        self.description = {name: value for name, value in (
            ("tool", tool), ("caller", caller), ("since", since), ("until", until), ("status", status)
        ) if value is not None}
        if any(not isinstance(value, str) or not value.strip() for value in self.description.values()):
            raise ValueError("Receipt filters must be nonempty text.")
        if caller is not None and not re.fullmatch(r"(?:[0-9a-f]{64}|anonymous)", caller):
            raise ValueError("--caller must be a complete SHA-256 fingerprint or anonymous, not a bearer token.")
        self.since = _timestamp(since, "--since") if since is not None else None
        self.until = _timestamp(until, "--until") if until is not None else None
        if self.since is not None and self.until is not None and self.since >= self.until:
            raise ValueError("--since must be earlier than --until.")
        for name, timestamp in (("since", self.since), ("until", self.until)):
            if timestamp is not None:
                self.description[name] = timestamp.isoformat().replace("+00:00", "Z")

    def sql(self, brief_id=None):
        columns = (("tool", "tool"), ("caller", "token_id"), ("status", "status"))
        clauses, parameters = [], []
        if brief_id is not None:
            clauses.append("brief_id = ?")
            parameters.append(brief_id)
        for name, column in columns:
            if name in self.description:
                clauses.append(column + " = ?")
                parameters.append(self.description[name])
        return (" WHERE " + " AND ".join(clauses) if clauses else ""), tuple(parameters)

    def includes_time(self, started_at):
        if self.since is None and self.until is None:
            return True
        timestamp = _timestamp(started_at, "Audit timestamp")
        return ((self.since is None or timestamp >= self.since)
                and (self.until is None or timestamp < self.until))


def _check_interval(interval):
    if (isinstance(interval, bool) or not isinstance(interval, (float, int))
            or not math.isfinite(interval) or interval <= 0):
        raise ValueError("The follow interval must be a positive number of seconds.")


def follow_audit(path, brief_id=None, *, interval=0.5, tool=None, caller=None, since=None, until=None, status=None):
    """Yield existing matches, then changed matching versions of each receipt.

    Poll complete rows so an earlier pending receipt's finalization is visible.
    Each read opens and closes its own read-only connection. No lock is held
    while yielding or waiting. Deleted rows produce no event.
    """
    from .audit import read_audit

    _check_interval(interval)
    seen: dict[str, str] = {}
    while True:
        for row in read_audit(path, brief_id, tool=tool, caller=caller, since=since, until=until, status=status):
            signature = json.dumps(row, sort_keys=True, separators=(",", ":"))
            if seen.get(row["audit_id"]) != signature:
                seen[row["audit_id"]] = signature
                yield row
        time.sleep(interval)


def follow_usage(directory, brief_id=None, *, interval=0.5,
                 tool=None, caller=None, since=None, until=None, status=None):
    """Yield the initial report and changed summaries, including artifact updates."""
    from .usage import build_usage_report

    _check_interval(interval)
    previous = None
    while True:
        report = build_usage_report(directory, brief_id, tool=tool, caller=caller,
                                    since=since, until=until, status=status)
        signature = json.dumps(report, sort_keys=True, separators=(",", ":"))
        if signature != previous:
            previous = signature
            yield report
        time.sleep(interval)
