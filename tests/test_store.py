"""Behavior checks for the invented CRM fixture and SQLite boundary."""

import sqlite3
import tempfile
import unittest
from datetime import date
from pathlib import Path

from gtm_connector.seed import seed
from gtm_connector.store import connect, counts, get_account, initialize, search_accounts


TABLES = ("accounts", "contacts", "opportunities", "call_notes", "activities")


def snapshot(connection):
    return {
        table: [dict(row) for row in connection.execute(f"SELECT * FROM {table} ORDER BY id")]
        for table in TABLES
    }


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "crm.sqlite3"
        self.connection = connect(self.path)
        self.addCleanup(self.connection.close)
        initialize(self.connection)

    def test_seed_has_thirty_complete_invented_accounts(self):
        seed(self.connection)
        self.assertEqual(
            counts(self.connection),
            {"accounts": 30, "contacts": 30, "opportunities": 30, "call_notes": 30, "activities": 0},
        )
        accounts = search_accounts(self.connection)
        self.assertEqual([row["id"] for row in accounts], [f"acc-{i:03}" for i in range(1, 31)])
        for row in accounts:
            account = get_account(self.connection, row["id"])
            self.assertEqual(date.fromisoformat(account["renewal_date"]).year, 2027)
            self.assertIsInstance(account["arr_cents"], int)
            self.assertGreater(account["arr_cents"], 0)
            for collection in ("contacts", "opportunities", "call_notes"):
                self.assertTrue(account[collection])
                self.assertTrue(all(child["account_id"] == account["id"] for child in account[collection]))
            for contact in account["contacts"]:
                self.assertTrue(contact["email"].split("@", 1)[1].endswith(".example"))
            for opportunity in account["opportunities"]:
                self.assertEqual(date.fromisoformat(opportunity["close_date"]).year, 2027)
                self.assertIsInstance(opportunity["amount_cents"], int)
        self.assertEqual(self.connection.execute("PRAGMA foreign_key_check").fetchall(), [])

    def test_fresh_databases_have_identical_records(self):
        seed(self.connection)
        other = connect(Path(self.directory.name) / "other.sqlite3")
        self.addCleanup(other.close)
        initialize(other)
        seed(other)
        self.assertEqual(snapshot(self.connection), snapshot(other))

    def test_reseeding_preserves_edits_and_activities(self):
        seed(self.connection)
        with self.connection:
            self.connection.execute("UPDATE accounts SET name = ? WHERE id = ?", ("Edited fixture", "acc-001"))
            self.connection.execute("UPDATE call_notes SET body = ? WHERE id = ?", ("Edited note", "note-001"))
            self.connection.execute(
                "INSERT INTO activities (id, account_id, kind, body, created_at, idempotency_key) VALUES (?, ?, ?, ?, ?, ?)",
                ("act-test", "acc-001", "call", "Local test", "2027-01-01T12:00:00Z", "test-reseed"),
            )
        before = snapshot(self.connection)
        seed(self.connection)
        self.assertEqual(snapshot(self.connection), before)

    def test_search_is_case_insensitive_and_treats_sql_characters_literally(self):
        seed(self.connection)
        with self.connection:
            self.connection.execute("UPDATE accounts SET name = ? WHERE id = ?", ("Odd %_ O'Brien fixture", "acc-001"))
        for query in ("oDd", "%", "_", "O'BRIEN"):
            self.assertEqual([row["id"] for row in search_accounts(self.connection, query)], ["acc-001"])
        self.assertEqual(search_accounts(self.connection, "' OR 1=1 --"), [])
        self.assertEqual(counts(self.connection)["accounts"], 30)

    def test_unknown_account_has_no_result(self):
        seed(self.connection)
        self.assertIsNone(get_account(self.connection, "acc-missing"))
        self.assertIsNone(get_account(self.connection, "acc-001' OR 1=1 --"))

    def test_read_only_connection_can_read_but_cannot_write(self):
        seed(self.connection)
        reader = connect(self.path, readonly=True)
        self.addCleanup(reader.close)
        self.assertEqual(get_account(reader, "acc-001")["id"], "acc-001")
        with self.assertRaises(sqlite3.OperationalError):
            reader.execute("DELETE FROM call_notes")
        self.assertEqual(counts(self.connection)["call_notes"], 30)

    def test_foreign_keys_reject_orphan_records(self):
        seed(self.connection)
        with self.assertRaises(sqlite3.IntegrityError):
            with self.connection:
                self.connection.execute(
                    "INSERT INTO call_notes (id, account_id, called_at, body) VALUES (?, ?, ?, ?)",
                    ("note-orphan", "acc-missing", "2027-01-01T12:00:00Z", "Orphan fixture"),
                )
        self.assertEqual(counts(self.connection)["call_notes"], 30)

    def test_injection_note_is_stored_as_text_without_creating_activity(self):
        seed(self.connection)
        account = get_account(self.connection, "acc-007")
        note = next(row for row in account["call_notes"] if row["id"] == "note-007")
        self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", note["body"])
        self.assertIn("log_activity", note["body"])
        stored = self.connection.execute("SELECT body FROM call_notes WHERE id = ?", ("note-007",)).fetchone()["body"]
        self.assertEqual(note["body"], stored)
        self.assertEqual(counts(self.connection)["activities"], 0)

    def test_seed_failure_rolls_back_all_rows(self):
        self.connection.execute(
            "CREATE TRIGGER interrupt_seed BEFORE INSERT ON accounts "
            "WHEN NEW.id = 'acc-015' BEGIN SELECT RAISE(ABORT, 'test interruption'); END"
        )
        self.connection.commit()
        with self.assertRaises(sqlite3.IntegrityError):
            seed(self.connection)
        self.assertEqual(counts(self.connection), {table: 0 for table in TABLES})

    def test_seed_refuses_partial_fixture_without_mutating_it(self):
        with self.connection:
            self.connection.execute(
                "INSERT INTO accounts (id, name, industry, renewal_date, arr_cents) VALUES (?, ?, ?, ?, ?)",
                ("acc-001", "Partial fixture", "Testing", "2027-01-01", 100),
            )
        before = snapshot(self.connection)
        with self.assertRaises(ValueError):
            seed(self.connection)
        self.assertEqual(snapshot(self.connection), before)


if __name__ == "__main__":
    unittest.main()
