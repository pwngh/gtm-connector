"""Read-only usage summaries over presented credentials, including invalid ones."""

from collections import Counter
from contextlib import closing
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import sqlite3

from .inspection import ReceiptFilters


def _counts(rows):
    outcomes = Counter("pending" if row["status"] == "pending" else
                       "ok" if row["status"] == "ok" else "error" for row in rows)
    return {"total": len(rows), **{name: outcomes[name] for name in ("ok", "error", "pending")}}


def _groups(rows, fields):
    groups: dict[tuple[str, ...], list[dict[str, str | None]]] = {}
    for row in rows:
        key = tuple(row[field] for field in fields)
        groups.setdefault(key, []).append(row)
    return [{**dict(zip(fields, key)), **_counts(groups[key])} for key in sorted(groups)]


def _saved_briefs(directory, brief_id, call_counts, matching_briefs=None):
    path = directory / "briefs"
    if path.is_symlink():
        raise ValueError("briefs directory must not be a symbolic link")
    if not path.exists():
        return []
    if not path.is_dir():
        raise ValueError("briefs path must be a directory")
    briefs = []
    for artifact_path in sorted(path.glob("*.json")):
        if brief_id is not None and artifact_path.stem != brief_id:
            continue
        if matching_briefs is not None and artifact_path.stem not in matching_briefs:
            continue
        try:
            if artifact_path.is_symlink() or not artifact_path.is_file():
                raise ValueError("not a regular file")
            artifact = json.loads(artifact_path.read_text())
            identifier = artifact["brief_id"]
            if (not isinstance(identifier, str) or not re.fullmatch(r"brief-[0-9a-f]{32}", identifier)
                    or identifier != artifact_path.stem or artifact["schema_version"] != 1
                    or artifact["status"] not in ("collected", "accepted", "rejected")
                    or artifact["adapter"] not in ("fixture", "custom", "ollama")
                    or not isinstance(artifact["account_id"], str)):
                raise ValueError("unsupported artifact metadata")
            usage = artifact["model_usage"]
            model = usage["model"]
            if model is not None and not isinstance(model, str):
                raise ValueError("invalid model identifier")
            tokens = {field: usage[field] for field in ("input_tokens", "output_tokens")}
            if any(value is not None and (type(value) is not int or value < 0)
                   for value in tokens.values()):
                raise ValueError("invalid token counts")
            if artifact["adapter"] == "fixture" and (model is not None or
                                                    any(value != 0 for value in tokens.values())):
                raise ValueError("fixture usage must record no model and zero tokens")
            # Null is unknown, including an interrupted or rejected custom draft.
            briefs.append({"brief_id": identifier, "account_id": artifact["account_id"],
                           "status": artifact["status"], "adapter": artifact["adapter"],
                           "audit_calls": call_counts[identifier],
                           "model_usage": {"model": model, **tokens}})
        except (OSError, ValueError, TypeError, KeyError) as exc:
            raise ValueError(f"invalid brief usage artifact: {artifact_path.name}") from exc
    return briefs


def build_usage_report(directory, brief_id=None, *, tool=None, caller=None, since=None, until=None, status=None):
    """Count all audit outcomes and report recorded model usage, without mutation.

    Totals cover saved artifacts only. A missing artifact is not evidence of
    zero model usage. Malformed artifacts stop the report instead of silently
    disappearing from its totals. Receipt filters select whole saved briefs
    having at least one matching call; their model tokens are not prorated.
    Without receipt filters, saved artifacts with no receipts remain included.
    Local files remain inside the owner boundary.
    """
    filters = ReceiptFilters(tool=tool, caller=caller, since=since, until=until, status=status)
    directory = Path(directory).expanduser().absolute()
    path = directory / "audit.sqlite3"
    if path.is_symlink():
        raise ValueError("audit database must not be a symbolic link")
    if not path.is_file():
        raise ValueError("audit database not found; run the demo first")
    query = "SELECT token_id, tool, started_at, status, brief_id FROM audit_log"
    condition, parameters = filters.sql(brief_id)
    query += condition
    with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as connection:
        connection.row_factory = sqlite3.Row
        rows = [dict(row) for row in connection.execute(query, parameters)
                if filters.includes_time(row["started_at"])]
    for row in rows:
        timestamp = datetime.fromisoformat(row.pop("started_at"))
        if timestamp.tzinfo is None:
            raise ValueError("audit timestamp must include its UTC offset")
        row["day"] = timestamp.astimezone(timezone.utc).date().isoformat()
    call_counts = Counter(row["brief_id"] for row in rows if row["brief_id"] is not None)
    briefs = _saved_briefs(directory, brief_id, call_counts,
                           set(call_counts) if filters.description else None)
    totals = {}
    for field in ("input_tokens", "output_tokens"):
        counts = [brief["model_usage"][field] for brief in briefs]
        totals[field] = None if any(value is None for value in counts) else sum(counts)
    report = {"brief_id": brief_id, "day_basis": "UTC", "calls": _counts(rows),
            "by_token": _groups(rows, ("token_id",)), "by_tool": _groups(rows, ("tool",)),
            "by_day": _groups(rows, ("day",)),
            "by_token_tool_day": _groups(rows, ("token_id", "tool", "day")),
            "briefs": briefs,
            "model_tokens": {"saved_briefs": len(briefs),
                             "unknown_briefs": sum(any(brief["model_usage"][field] is None
                                                       for field in ("input_tokens", "output_tokens"))
                                                   for brief in briefs), **totals}}
    if filters.description:
        report["filters"] = filters.description
    return report
