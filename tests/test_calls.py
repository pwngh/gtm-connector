"""Checks for the separate, untrusted call-note source."""

import sqlite3
import tempfile
import unittest
from pathlib import Path

from gtm_connector import calls, slack, store


def notes_snapshot(connection):
    return [dict(row) for row in connection.execute("SELECT * FROM call_notes ORDER BY id")]


class CallsTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "calls.sqlite3"
        self.connection = calls.connect(self.path)
        self.addCleanup(self.connection.close)
        calls.initialize(self.connection)

    def test_source_has_only_notes_with_stable_readable_ids(self):
        calls.seed(self.connection)
        self.assertEqual(calls.counts(self.connection), 29)
        tables = {row[0] for row in self.connection.execute("SELECT name FROM sqlite_master WHERE type = 'table'")}
        self.assertEqual(tables, {"call_notes"})
        expected = [f"note-{i:03}" for i in range(1, 31) if i != 4]
        self.assertEqual([row["id"] for row in notes_snapshot(self.connection)], expected)
        self.assertEqual(calls.read_call_notes(self.connection, "acc-004"), [])
        self.assertEqual(calls.read_call_notes(self.connection, "acc-missing"), [])
        self.assertEqual(calls.read_call_notes(self.connection, "acc-001' OR 1=1 --"), [])

    def test_contradiction_is_retained_in_both_sources(self):
        calls.seed(self.connection)
        crm = store.connect(Path(self.directory.name) / "crm.sqlite3")
        self.addCleanup(crm.close)
        store.initialize(crm)
        store.seed(crm)
        opportunity = store.list_opportunities(crm, "acc-003")[0]
        note = calls.read_call_notes(self.connection, "acc-003")[0]
        self.assertEqual(opportunity["stage"], "closed_won")
        self.assertIn("has not approved", note["body"])
        self.assertIn("budget remains unsigned", note["body"])
        self.assertEqual(store.counts(crm)["activities"], 0)

    def test_injection_is_raw_data_and_does_not_modify_crm(self):
        calls.seed(self.connection)
        crm = store.connect(Path(self.directory.name) / "crm.sqlite3")
        self.addCleanup(crm.close)
        store.initialize(crm)
        store.seed(crm)
        before = store.get_account(crm, "acc-007")
        note = calls.read_call_notes(self.connection, "acc-007")[0]
        self.assertEqual(note["id"], "note-007")
        self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", note["body"])
        self.assertIn("log_activity", note["body"])
        self.assertEqual(note["body"], self.connection.execute("SELECT body FROM call_notes WHERE id = 'note-007'").fetchone()[0])
        self.assertEqual(store.get_account(crm, "acc-007"), before)
        self.assertEqual(store.counts(crm)["activities"], 0)

    def test_old_note_and_current_message_retain_their_different_seat_reports(self):
        calls.seed(self.connection)
        source = slack.connect(Path(self.directory.name) / "slack.sqlite3")
        self.addCleanup(source.close)
        slack.initialize(source)
        slack.seed(source)
        note = calls.read_call_notes(self.connection, "acc-006")[0]
        message = slack.read_slack_messages(source, "acc-006")[0]
        self.assertEqual(note["called_at"], "2026-10-03T15:00:00Z")
        self.assertEqual(note["body"], "The field rollout is complete. The current seat count should remain unchanged.")
        self.assertEqual(message["sent_at"], "2027-01-14T16:00:00Z")
        self.assertEqual(message["body"], "The January staffing review added two field teams. The seat count needs review.")

    def test_fresh_seeds_match_and_reseeds_preserve_edits(self):
        calls.seed(self.connection)
        other = calls.connect(Path(self.directory.name) / "other-calls.sqlite3")
        self.addCleanup(other.close)
        calls.initialize(other)
        calls.seed(other)
        self.assertEqual(notes_snapshot(other), notes_snapshot(self.connection))
        with self.connection:
            self.connection.execute("UPDATE call_notes SET body = ? WHERE id = ?", ("Edited fixture text", "note-001"))
        before = notes_snapshot(self.connection)
        calls.seed(self.connection)
        self.assertEqual(notes_snapshot(self.connection), before)

    def test_read_only_source_rejects_writes(self):
        calls.seed(self.connection)
        reader = calls.connect(self.path, readonly=True)
        self.addCleanup(reader.close)
        self.assertEqual(calls.read_call_notes(reader, "acc-001")[0]["id"], "note-001")
        with self.assertRaises(sqlite3.OperationalError):
            reader.execute("DELETE FROM call_notes")
        self.assertEqual(calls.counts(self.connection), 29)

    def test_seed_failure_is_atomic(self):
        self.connection.execute(
            "CREATE TRIGGER interrupt_seed BEFORE INSERT ON call_notes "
            "WHEN NEW.id = 'note-015' BEGIN SELECT RAISE(ABORT, 'test interruption'); END"
        )
        self.connection.commit()
        with self.assertRaises(sqlite3.IntegrityError):
            calls.seed(self.connection)
        self.assertEqual(calls.counts(self.connection), 0)

    def test_partial_or_reassociated_notes_are_rejected_without_overwrite(self):
        calls.seed(self.connection)
        with self.connection:
            self.connection.execute("UPDATE call_notes SET account_id = ? WHERE id = ?", ("acc-002", "note-001"))
        before = notes_snapshot(self.connection)
        with self.assertRaisesRegex(ValueError, "fresh database path"):
            calls.seed(self.connection)
        self.assertEqual(notes_snapshot(self.connection), before)
        with self.connection:
            self.connection.execute("DELETE FROM call_notes WHERE id = 'note-001'")
        before = notes_snapshot(self.connection)
        with self.assertRaises(ValueError):
            calls.seed(self.connection)
        self.assertEqual(notes_snapshot(self.connection), before)

    def test_crm_database_cannot_be_used_for_calls(self):
        crm = store.connect(Path(self.directory.name) / "crm.sqlite3")
        self.addCleanup(crm.close)
        store.initialize(crm)
        store.seed(crm)
        with self.assertRaisesRegex(ValueError, "separate source"):
            calls.initialize(crm)
        tables = {row[0] for row in crm.execute("SELECT name FROM sqlite_master WHERE type = 'table'")}
        self.assertNotIn("call_notes", tables)


if __name__ == "__main__":
    unittest.main()
