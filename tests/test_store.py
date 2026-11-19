"""Behavior checks for invented CRM records and the SQLite boundary."""

import sqlite3
import tempfile
import unittest
from datetime import date
from pathlib import Path

from gtm_connector.seed import DEMO_AS_OF, seed
from gtm_connector.store import (
    connect, counts, get_account, initialize, list_opportunities, search_accounts,
)


TABLES = ("accounts", "contacts", "opportunities", "activities")


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
            {"accounts": 30, "contacts": 30, "opportunities": 30, "activities": 0},
        )
        accounts, more = search_accounts(self.connection)
        self.assertIs(more, False)
        self.assertEqual(sorted(row["id"] for row in accounts), [f"acc-{i:03}" for i in range(1, 31)])
        self.assertEqual(len({row["name"] for row in accounts}), 30)
        self.assertEqual([row["name"] for row in accounts], sorted(row["name"] for row in accounts))
        self.assertEqual(accounts[0]["name"], "Alder Vale Packaging")
        self.assertEqual(accounts[-1]["name"], "Windward Finch Travel")
        industries = set()
        for row in accounts:
            account = get_account(self.connection, row["id"])
            self.assertNotIn(account["id"], account["name"])
            industries.add(account["industry"])
            self.assertEqual(date.fromisoformat(account["renewal_date"]).year, 2027)
            self.assertIsInstance(account["arr_cents"], int)
            self.assertGreater(account["arr_cents"], 0)
            self.assertTrue(account["contacts"])
            self.assertNotIn("opportunities", account)
            self.assertNotIn("call_notes", account)
            for contact in account["contacts"]:
                self.assertEqual(contact["account_id"], account["id"])
                self.assertNotIn(contact["id"], contact["name"])
                self.assertTrue(contact["email"].split("@", 1)[1].endswith(".example"))
            opportunities = list_opportunities(self.connection, account["id"])
            self.assertEqual(len(opportunities), 1)
            for opportunity in opportunities:
                self.assertEqual(opportunity["name"], account["name"] + " 2027 renewal")
                self.assertEqual(opportunity["account_id"], account["id"])
                self.assertIsInstance(opportunity["amount_cents"], int)
        self.assertEqual(self.connection.execute("PRAGMA foreign_key_check").fetchall(), [])
        self.assertGreaterEqual(len(industries), 10)

    def test_briefing_edge_cases_exist_in_crm_source(self):
        seed(self.connection)
        renewal = date.fromisoformat(get_account(self.connection, "acc-001")["renewal_date"])
        self.assertEqual((renewal - date.fromisoformat(DEMO_AS_OF)).days, 17)
        risk = get_account(self.connection, "acc-002")
        self.assertEqual(risk["risk_status"], "at_risk")
        self.assertTrue(risk["risk_reason"])
        self.assertEqual(list_opportunities(self.connection, "acc-003")[0]["stage"], "closed_won")
        story = get_account(self.connection, "acc-007")
        self.assertEqual(story["name"], "Juniper Crest Learning")
        self.assertEqual((date.fromisoformat(story["renewal_date"]) - date.fromisoformat(DEMO_AS_OF)).days, 14)
        self.assertEqual(story["contacts"][0]["name"], "Dana Crest")
        self.assertEqual(story["contacts"][0]["title"], "")
        self.assertEqual(story["contacts"][0]["email"], "dana@juniper-crest.example")
        self.assertEqual(list_opportunities(self.connection, "acc-007")[0]["close_date"], "2027-01-22")

    def test_crm_has_no_call_notes_table_or_enrichment(self):
        seed(self.connection)
        tables = {row[0] for row in self.connection.execute("SELECT name FROM sqlite_master WHERE type = 'table'")}
        self.assertNotIn("call_notes", tables)
        self.assertEqual(tables, set(TABLES))
        for row in search_accounts(self.connection)[0]:
            self.assertNotIn("call_notes", row)
        self.assertNotIn("call_notes", get_account(self.connection, "acc-007"))
        self.assertNotIn("body", list_opportunities(self.connection, "acc-007")[0])

    def test_fresh_databases_have_identical_records_and_readable_ids(self):
        seed(self.connection)
        other = connect(Path(self.directory.name) / "other.sqlite3")
        self.addCleanup(other.close)
        initialize(other)
        seed(other)
        self.assertEqual(snapshot(self.connection), snapshot(other))
        for table, prefix in (("accounts", "acc"), ("contacts", "con"), ("opportunities", "opp")):
            ids = [row["id"] for row in snapshot(self.connection)[table]]
            self.assertEqual(ids, [f"{prefix}-{i:03}" for i in range(1, 31)])

    def test_reseeding_preserves_ids_edits_and_activities(self):
        seed(self.connection)
        with self.connection:
            self.connection.execute("UPDATE accounts SET name = ? WHERE id = ?", ("Edited fixture label", "acc-001"))
            self.connection.execute("UPDATE opportunities SET stage = ? WHERE id = ?", ("negotiation", "opp-003"))
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
            self.connection.execute("UPDATE accounts SET name = ? WHERE id = ?", ("Odd %_ quote' fixture", "acc-001"))
        for query in ("oDd", "%", "_", "QUOTE'"):
            self.assertEqual([row["id"] for row in search_accounts(self.connection, query)[0]], ["acc-001"])
        self.assertEqual(search_accounts(self.connection, "' OR 1=1 --"), ([], False))
        self.assertEqual(counts(self.connection)["accounts"], 30)

    def test_search_returns_the_first_hundred_by_name_then_id_and_says_when_more_match(self):
        for matching in (100, 101, 250):
            # Names run against the IDs and pairs of rows share a name, so the order shows both keys.
            book = [(f"acc-{number:03}", f"Invented account {(matching - number) // 2:03}")
                    for number in range(1, matching + 1)]
            with self.connection:
                self.connection.execute("DELETE FROM accounts")
                self.connection.executemany(
                    "INSERT INTO accounts VALUES (?, ?, 'Software', '2027-02-01', 0, 'healthy', '')", book)
            ordered = sorted(book, key=lambda row: (row[1], row[0]))
            with self.subTest(matching=matching):
                rows, more = search_accounts(self.connection, "invented")
                self.assertEqual([(row["id"], row["name"]) for row in rows], ordered[:100])
                self.assertIs(more, matching > 100)
        # A selection is part of the query, so accounts outside it never count as more.
        selected = [account_id for account_id, _ in book[:100]]
        rows, more = search_accounts(self.connection, "invented", account_ids=selected)
        self.assertEqual(sorted(row["id"] for row in rows), selected)
        self.assertIs(more, False)

    def test_search_order_ignores_ascii_case_as_the_match_does(self):
        with self.connection:
            self.connection.executemany(
                "INSERT INTO accounts VALUES (?, ?, 'Software', '2027-02-01', 0, 'healthy', '')",
                [("acc-001", "invented beta"), ("acc-002", "Invented Alpha"), ("acc-003", "invented alpha"),
                 ("acc-004", "INVENTED ALPHA")])
        self.assertEqual([row["id"] for row in search_accounts(self.connection, "invented")[0]],
                         ["acc-002", "acc-003", "acc-004", "acc-001"])

    def test_account_and_opportunity_lookups_are_parameterized(self):
        seed(self.connection)
        self.assertIsNone(get_account(self.connection, "acc-missing"))
        self.assertIsNone(get_account(self.connection, "acc-001' OR 1=1 --"))
        self.assertEqual(list_opportunities(self.connection, "acc-missing"), [])
        self.assertEqual(list_opportunities(self.connection, "acc-001' OR 1=1 --"), [])
        self.assertEqual(len(list_opportunities(self.connection)), 30)

    def test_read_only_connection_can_read_but_cannot_write(self):
        seed(self.connection)
        reader = connect(self.path, readonly=True)
        self.addCleanup(reader.close)
        self.assertEqual(get_account(reader, "acc-001")["id"], "acc-001")
        with self.assertRaises(sqlite3.OperationalError):
            reader.execute("DELETE FROM contacts")
        self.assertEqual(counts(self.connection)["contacts"], 30)

    def test_foreign_keys_reject_orphan_records(self):
        seed(self.connection)
        with self.assertRaises(sqlite3.IntegrityError):
            with self.connection:
                self.connection.execute(
                    "INSERT INTO contacts VALUES (?, ?, ?, ?, ?)",
                    ("con-orphan", "acc-missing", "Fixture contact con-orphan", "Fixture", "orphan@fixture.example"),
                )
        self.assertEqual(counts(self.connection)["contacts"], 30)

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
                "INSERT INTO accounts VALUES (?, ?, ?, ?, ?, ?, ?)",
                ("acc-001", "Partial fixture label", "Testing", "2027-01-01", 100, "healthy", ""),
            )
        before = snapshot(self.connection)
        with self.assertRaises(ValueError):
            seed(self.connection)
        self.assertEqual(snapshot(self.connection), before)

    def test_legacy_combined_database_is_rejected_before_schema_writes(self):
        legacy = connect(Path(self.directory.name) / "legacy.sqlite3")
        self.addCleanup(legacy.close)
        legacy.execute("CREATE TABLE call_notes (id TEXT PRIMARY KEY, body TEXT)")
        legacy.execute("INSERT INTO call_notes VALUES ('note-001', 'Legacy fixture text')")
        legacy.commit()
        before = legacy.execute("SELECT name, sql FROM sqlite_master ORDER BY name").fetchall()
        with self.assertRaisesRegex(ValueError, "fresh database path"):
            initialize(legacy)
        self.assertEqual(legacy.execute("SELECT name, sql FROM sqlite_master ORDER BY name").fetchall(), before)
        self.assertEqual(legacy.execute("SELECT body FROM call_notes").fetchone()[0], "Legacy fixture text")


if __name__ == "__main__":
    unittest.main()
