"""SQLite storage for the invented CRM. No network services are involved."""

from pathlib import Path
import sqlite3

from .seed import seed as seed


TABLES = ("accounts", "contacts", "opportunities", "activities")


def connect(path: str | Path, *, readonly: bool = False) -> sqlite3.Connection:
    """Open a database; inspection never creates a missing file or directory."""
    if str(path) == ":memory:":
        if readonly:
            raise ValueError("An in-memory database cannot be opened read-only.")
        connection = sqlite3.connect(":memory:")
    else:
        database = Path(path).expanduser().resolve()
        if readonly:
            connection = sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)
        else:
            database.parent.mkdir(parents=True, exist_ok=True)
            connection = sqlite3.connect(database)
    connection.row_factory = sqlite3.Row
    connection.execute("PRAGMA foreign_keys = ON")
    return connection


def initialize(connection: sqlite3.Connection) -> None:
    """Create the fixture schema without replacing existing tables or rows."""
    if connection.execute(
        "SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'call_notes'"
    ).fetchone():
        raise ValueError(
            "This is a legacy combined CRM/calls database; use a fresh database path."
        )
    statements = (
        """CREATE TABLE IF NOT EXISTS accounts (
            id TEXT PRIMARY KEY NOT NULL,
            name TEXT NOT NULL,
            industry TEXT NOT NULL,
            renewal_date TEXT NOT NULL,
            arr_cents INTEGER NOT NULL CHECK (arr_cents >= 0),
            risk_status TEXT NOT NULL CHECK (risk_status IN ('healthy', 'at_risk')),
            risk_reason TEXT NOT NULL
        )""",
        """CREATE TABLE IF NOT EXISTS contacts (
            id TEXT PRIMARY KEY NOT NULL,
            account_id TEXT NOT NULL REFERENCES accounts(id),
            name TEXT NOT NULL,
            title TEXT NOT NULL,
            email TEXT NOT NULL
        )""",
        """CREATE TABLE IF NOT EXISTS opportunities (
            id TEXT PRIMARY KEY NOT NULL,
            account_id TEXT NOT NULL REFERENCES accounts(id),
            name TEXT NOT NULL,
            stage TEXT NOT NULL CHECK (stage IN (
                'discovery', 'evaluation', 'proposal', 'negotiation',
                'closed_won', 'closed_lost'
            )),
            amount_cents INTEGER NOT NULL CHECK (amount_cents >= 0),
            close_date TEXT NOT NULL
        )""",
        """CREATE TABLE IF NOT EXISTS activities (
            id TEXT PRIMARY KEY NOT NULL,
            account_id TEXT NOT NULL REFERENCES accounts(id),
            kind TEXT NOT NULL,
            body TEXT NOT NULL,
            created_at TEXT NOT NULL,
            idempotency_key TEXT NOT NULL UNIQUE
        )""",
    )
    for statement in statements:
        connection.execute(statement)


def counts(connection: sqlite3.Connection) -> dict[str, int]:
    """Return row counts for the fixed set of CRM tables."""
    return {
        table: connection.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]
        for table in TABLES
    }


def search_accounts(
    connection: sqlite3.Connection, query: str = ""
) -> list[dict]:
    """Find account names containing the literal query, ignoring ASCII case."""
    rows = connection.execute(
        "SELECT * FROM accounts WHERE instr(lower(name), lower(?)) > 0 ORDER BY id",
        (query,),
    )
    return [dict(row) for row in rows]


def get_account(connection: sqlite3.Connection, account_id: str) -> dict | None:
    """Return a CRM account and contacts only; calls are a separate source."""
    row = connection.execute(
        "SELECT * FROM accounts WHERE id = ?", (account_id,)
    ).fetchone()
    if row is None:
        return None
    account = dict(row)
    account["contacts"] = [
        dict(record)
        for record in connection.execute(
            "SELECT * FROM contacts WHERE account_id = ? ORDER BY id", (account_id,)
        )
    ]
    return account


def list_opportunities(
    connection: sqlite3.Connection, account_id: str | None = None
) -> list[dict]:
    """Return opportunity records without enriching them from the calls source."""
    if account_id is None:
        rows = connection.execute("SELECT * FROM opportunities ORDER BY id")
    else:
        rows = connection.execute(
            "SELECT * FROM opportunities WHERE account_id = ? ORDER BY id", (account_id,)
        )
    return [dict(row) for row in rows]
