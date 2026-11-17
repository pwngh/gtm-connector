"""Durable receipts for connector calls; bearer credentials are fingerprinted."""

from contextlib import closing
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import uuid


def read_audit(path: str | Path, brief_id: str | None = None) -> list[dict]:
    """Read existing receipts without initializing or changing the database."""
    database = Path(path).expanduser().absolute()
    if database.is_symlink():
        raise ValueError("Audit database must not be a symbolic link.")
    if not database.is_file():
        raise ValueError("audit database not found; run the demo first")
    query = """SELECT audit_id, started_at, token_id, tool, arguments, request_id,
                      brief_id, status, http_status, row_count, rows, elapsed_ms, error
               FROM audit_log"""
    parameters = ()
    if brief_id is not None:
        query += " WHERE brief_id = ?"
        parameters = (brief_id,)
    with closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True, timeout=10)) as connection:
        connection.row_factory = sqlite3.Row
        rows = connection.execute(query + " ORDER BY rowid", parameters).fetchall()
    records = []
    for row in rows:
        record = dict(row)
        try:
            for field in ("arguments", "request_id", "rows"):
                record[field] = json.loads(record[field])
        except (ValueError, TypeError, RecursionError):
            raise ValueError("Audit receipt contains invalid JSON.") from None
        records.append(record)
    return records


class AuditLog:
    def __init__(self, path: str | Path):
        self.path = Path(path).expanduser().absolute()
        self.path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
        if self.path.is_symlink():
            raise ValueError("Audit database must not be a symbolic link.")
        try:
            descriptor = os.open(self.path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)
        except FileExistsError:
            if not self.path.is_file():
                raise ValueError("Audit database path must be a regular file.")
            self.path.chmod(0o600)
        else:
            os.close(descriptor)
        with closing(self._connect()) as connection, connection:
            connection.execute("""CREATE TABLE IF NOT EXISTS audit_log (
                audit_id TEXT PRIMARY KEY NOT NULL,
                started_at TEXT NOT NULL,
                token_id TEXT NOT NULL,
                tool TEXT NOT NULL,
                arguments TEXT NOT NULL,
                request_id TEXT NOT NULL,
                brief_id TEXT,
                status TEXT NOT NULL DEFAULT 'pending',
                http_status INTEGER,
                row_count INTEGER NOT NULL DEFAULT 0 CHECK (row_count >= 0),
                rows TEXT NOT NULL DEFAULT '[]',
                elapsed_ms REAL,
                error TEXT
            )""")

    def _connect(self) -> sqlite3.Connection:
        connection = sqlite3.connect(self.path.as_uri() + "?mode=rw", uri=True, timeout=10)
        connection.row_factory = sqlite3.Row
        return connection

    def start(
        self, *, token: str | None, tool: str, arguments,
        request_id=None, brief_id: str | None = None,
    ) -> str:
        """Commit a pending receipt before the caller may execute the tool."""
        audit_id = str(uuid.uuid4())
        started_at = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
        token_id = "anonymous" if token is None else hashlib.sha256(token.encode()).hexdigest()
        arguments_json = json.dumps(arguments, ensure_ascii=False, allow_nan=False)
        request_id_json = json.dumps(request_id, ensure_ascii=False, allow_nan=False)
        with closing(self._connect()) as connection, connection:
            connection.execute(
                "INSERT INTO audit_log "
                "(audit_id, started_at, token_id, tool, arguments, request_id, brief_id) "
                "VALUES (?, ?, ?, ?, ?, ?, ?)",
                (audit_id, started_at, token_id, tool, arguments_json, request_id_json, brief_id),
            )
        return audit_id

    def finish(
        self, audit_id: str, *, status: str, http_status: int,
        rows: list[str], elapsed_ms: float, error: str | None = None,
    ) -> None:
        """Finalize the existing receipt once; failure leaves it pending."""
        if not status or status == "pending":
            raise ValueError("A final audit status must not be empty or pending.")
        rows_json = json.dumps(rows, ensure_ascii=False, allow_nan=False)
        with closing(self._connect()) as connection, connection:
            cursor = connection.execute(
                "UPDATE audit_log SET status = ?, http_status = ?, row_count = ?, "
                "rows = ?, elapsed_ms = ?, error = ? "
                "WHERE audit_id = ? AND status = 'pending'",
                (status, http_status, len(rows), rows_json, elapsed_ms, error, audit_id),
            )
            if cursor.rowcount != 1:
                raise ValueError("Audit receipt not found or already finalized.")

    def read(self, brief_id: str | None = None) -> list[dict]:
        """Return receipts in insertion order, optionally for one brief."""
        return read_audit(self.path, brief_id)
