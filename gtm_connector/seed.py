"""Deterministic CRM fixtures with synthetic labels, never real entity names."""

from datetime import date, timedelta
import sqlite3


DEMO_AS_OF = "2027-01-15"
ACCOUNT_COUNT = 30


def _existing_fixture(connection: sqlite3.Connection) -> bool:
    """Recognize complete CRM rows by identity, preserving subsequent edits."""
    prefixes = {"accounts": "acc", "contacts": "con", "opportunities": "opp"}
    actual = {
        table: {row[0] for row in connection.execute(f"SELECT id FROM {table}")}
        for table in prefixes
    }
    if not any(actual.values()):
        if connection.execute("SELECT COUNT(*) FROM activities").fetchone()[0]:
            raise ValueError("Refusing to seed a database with orphan activities.")
        return False
    for table, prefix in prefixes.items():
        expected = {f"{prefix}-{number:03}" for number in range(1, ACCOUNT_COUNT + 1)}
        if actual[table] != expected:
            raise ValueError(
                "Database contains a partial or unknown fixture; use a fresh database path."
            )
        if table != "accounts":
            for row in connection.execute(f"SELECT id, account_id FROM {table}"):
                if row[1] != "acc-" + row[0].rsplit("-", 1)[1]:
                    raise ValueError("Fixture record has an unexpected account association.")
    if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
        raise ValueError("Database has invalid foreign-key references.")
    return True


def seed(connection: sqlite3.Connection) -> None:
    """Insert CRM rows atomically or preserve an existing complete fixture.

    Call store.initialize() first. IDs remain stable across fresh databases and
    reseeds. Display labels are synthetic identifiers, not person/company names.
    """
    connection.execute("SAVEPOINT seed_fixture")
    try:
        if not _existing_fixture(connection):
            stages = ("discovery", "evaluation", "proposal", "negotiation")
            industries = ("Manufacturing", "Distribution", "Software", "Education")
            for number in range(1, ACCOUNT_COUNT + 1):
                suffix = f"{number:03}"
                account_id = f"acc-{suffix}"
                renewal = date(2027, 2, 1) + timedelta(days=(number - 1) * 7)
                arr = (18_000 + number * 2_500) * 100
                risk_status = "at_risk" if number == 2 else "healthy"
                risk_reason = "Renewal budget is not approved." if number == 2 else ""
                connection.execute(
                    "INSERT INTO accounts "
                    "(id, name, industry, renewal_date, arr_cents, risk_status, risk_reason) "
                    "VALUES (?, ?, ?, ?, ?, ?, ?)",
                    (account_id, f"Fixture account {account_id}",
                     industries[(number - 1) % len(industries)], renewal.isoformat(),
                     arr, risk_status, risk_reason),
                )
                connection.execute(
                    "INSERT INTO contacts VALUES (?, ?, ?, ?, ?)",
                    (f"con-{suffix}", account_id, f"Fixture contact con-{suffix}",
                     "Operations contact", f"contact{suffix}@account{suffix}.example"),
                )
                stage = "closed_won" if number == 3 else stages[(number - 1) % len(stages)]
                close_date = date(2027, 1, 10) if number == 3 else renewal - timedelta(days=7)
                connection.execute(
                    "INSERT INTO opportunities VALUES (?, ?, ?, ?, ?, ?)",
                    (f"opp-{suffix}", account_id, f"Fixture renewal opp-{suffix}",
                     stage, arr, close_date.isoformat()),
                )
        connection.execute("RELEASE SAVEPOINT seed_fixture")
    except Exception:
        connection.execute("ROLLBACK TO SAVEPOINT seed_fixture")
        connection.execute("RELEASE SAVEPOINT seed_fixture")
        raise
