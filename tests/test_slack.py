"""The Slack fixture is separate, stable, and read-only during inspection."""

from contextlib import closing
import sqlite3
import tempfile
import unittest
from pathlib import Path

from gtm_connector import calls, slack, store
from gtm_connector.audit import AuditLog


def snapshot(connection):
    return [dict(row) for row in connection.execute("SELECT * FROM messages ORDER BY id")]


class SlackTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.path = self.root / "slack.sqlite3"
        self.connection = slack.connect(self.path)
        self.addCleanup(self.connection.close)
        slack.initialize(self.connection)

    def test_stable_messages_have_only_synthetic_account_channel_context(self):
        slack.seed(self.connection)
        self.assertEqual(slack.counts(self.connection), 29)
        tables = {row[0] for row in self.connection.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table'")}
        self.assertEqual(tables, {"messages"})
        rows = snapshot(self.connection)
        self.assertEqual([row["id"] for row in rows],
                         [f"msg-{number:03}" for number in range(1, 31) if number != 5])
        for row in rows:
            number = row["id"].removeprefix("msg-")
            self.assertEqual(row["account_id"], "acc-" + number)
            self.assertEqual(row["channel"], "acct-acc-" + number)
            self.assertEqual(row["author"], "Fixture Slack author msg-" + number)
        self.assertEqual(rows[0]["sent_at"], "2027-01-14T16:00:00Z")
        for account in ("acc-005", "acc-missing", "acc-001' OR 1=1 --"):
            self.assertEqual(slack.read_slack_messages(self.connection, account), [])

    def test_champion_departure_and_security_delay_remain_source_statements(self):
        slack.seed(self.connection)
        self.assertEqual(slack.read_slack_messages(self.connection, "acc-002")[0]["body"],
                         "The renewal champion has left the team. A replacement owner has not been assigned.")
        self.assertEqual(slack.read_slack_messages(self.connection, "acc-003")[0]["body"],
                         "Security review is delayed. Procurement approval is still pending.")
        with closing(store.connect(self.root / "crm.sqlite3")) as crm:
            store.initialize(crm)
            store.seed(crm)
            self.assertEqual(store.list_opportunities(crm, "acc-003")[0]["stage"], "closed_won")
            self.assertEqual(store.counts(crm)["activities"], 0)

    def test_fresh_seeds_match_and_reseeding_preserves_message_edits(self):
        slack.seed(self.connection)
        with closing(slack.connect(self.root / "other.sqlite3")) as other:
            slack.initialize(other)
            slack.seed(other)
            self.assertEqual(snapshot(other), snapshot(self.connection))
        with self.connection:
            self.connection.execute("UPDATE messages SET body = ?, channel = ? WHERE id = ?",
                                    ("Edited fixture message", "edited-fixture-channel", "msg-001"))
        before = snapshot(self.connection)
        slack.seed(self.connection)
        self.assertEqual(snapshot(self.connection), before)

    def test_partial_unknown_and_reassociated_fixtures_fail_without_overwrite(self):
        slack.seed(self.connection)
        original = snapshot(self.connection)
        mutations = (
            "DELETE FROM messages WHERE id = 'msg-001'",
            "UPDATE messages SET id = 'msg-unknown' WHERE id = 'msg-001'",
            "UPDATE messages SET account_id = 'acc-002' WHERE id = 'msg-001'",
        )
        for statement in mutations:
            with self.subTest(statement=statement):
                self.connection.execute("SAVEPOINT test_mutation")
                self.connection.execute(statement)
                before = snapshot(self.connection)
                with self.assertRaisesRegex(ValueError, "fresh database path"):
                    slack.seed(self.connection)
                self.assertEqual(snapshot(self.connection), before)
                self.connection.execute("ROLLBACK TO SAVEPOINT test_mutation")
                self.connection.execute("RELEASE SAVEPOINT test_mutation")
        self.assertEqual(snapshot(self.connection), original)

    def test_seed_failure_is_atomic(self):
        self.connection.execute(
            "CREATE TRIGGER interrupt_seed BEFORE INSERT ON messages "
            "WHEN NEW.id = 'msg-015' BEGIN SELECT RAISE(ABORT, 'test interruption'); END"
        )
        self.connection.commit()
        with self.assertRaises(sqlite3.IntegrityError):
            slack.seed(self.connection)
        self.assertEqual(slack.counts(self.connection), 0)

    def test_read_only_connection_preserves_injection_as_data_and_rejects_writes(self):
        slack.seed(self.connection)
        with closing(slack.connect(self.path, readonly=True)) as reader:
            message = slack.read_slack_messages(reader, "acc-007")[0]
            self.assertIn(slack.INJECTION_TEXT, message["body"])
            self.assertNotEqual(slack.INJECTION_TEXT, calls.INJECTION_TEXT)
            self.assertIn("slack-seed-injection", message["body"])
            with self.assertRaises(sqlite3.OperationalError):
                reader.execute("DELETE FROM messages")
        self.assertEqual(slack.counts(self.connection), 29)

    def test_other_source_and_audit_files_cannot_be_initialized_as_slack(self):
        for name, module in (("crm", store), ("calls", calls)):
            with self.subTest(source=name), closing(module.connect(self.root / f"{name}.sqlite3")) as conn:
                module.initialize(conn)
                module.seed(conn)
                before = conn.execute("SELECT name, sql FROM sqlite_master ORDER BY name").fetchall()
                with self.assertRaisesRegex(ValueError, "separate source"):
                    slack.initialize(conn)
                self.assertEqual(conn.execute("SELECT name, sql FROM sqlite_master ORDER BY name").fetchall(), before)
        audit = AuditLog(self.root / "audit.sqlite3")
        with closing(slack.connect(audit.path)) as conn:
            with self.assertRaisesRegex(ValueError, "separate source"):
                slack.initialize(conn)

    def test_missing_read_only_source_is_not_created(self):
        missing = self.root / "missing" / "slack.sqlite3"
        with self.assertRaises(sqlite3.OperationalError):
            slack.connect(missing, readonly=True)
        self.assertFalse(missing.parent.exists())


if __name__ == "__main__":
    unittest.main()
