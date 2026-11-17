"""Audit inspection reads receipts without repairing or changing local files."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unittest

from gtm_connector.audit import AuditLog, read_audit


class AuditReaderTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.path = self.root / "audit.sqlite3"

    def snapshot(self, path):
        info = path.stat()
        return path.read_bytes(), info.st_mode, info.st_mtime_ns

    def schema(self, path):
        with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as connection:
            return connection.execute("SELECT type, name, sql FROM sqlite_schema ORDER BY name").fetchall()

    def run_cli(self, state=None, brief=None):
        command = [sys.executable, "-m", "gtm_connector", "audit", "--state-dir",
                   str(self.root if state is None else state)]
        if brief is not None:
            command += ["--brief", brief]
        return subprocess.run(command, cwd=Path(__file__).resolve().parents[1],
                              capture_output=True, text=True, timeout=10)

    def assert_cli_error(self, result, message):
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn(message, result.stderr)
        self.assertNotIn("Traceback", result.stderr)

    def receipt(self, path=None):
        log = AuditLog(self.path if path is None else path)
        identifier = log.start(token=None, tool="get_account", arguments={"account_id": "acc-001"},
                               request_id=12, brief_id="brief-a")
        log.finish(identifier, status="ok", http_status=200,
                   rows=["crm/accounts/acc-001"], elapsed_ms=1)
        return log, identifier

    def test_reader_preserves_read_only_file_contents_schema_mode_and_mtime(self):
        # URI metacharacters must remain part of the filename, not connection options.
        path = self.root / "audit #?.sqlite3"
        log, identifier = self.receipt(path)
        path.chmod(0o400)
        before, schema = self.snapshot(path), self.schema(path)
        rows = read_audit(path)
        self.assertEqual(rows[0]["audit_id"], identifier)
        self.assertEqual(rows[0]["rows"], ["crm/accounts/acc-001"])
        self.assertEqual(log.read(), rows)
        self.assertEqual(self.snapshot(path), before)
        self.assertEqual(self.schema(path), schema)
        self.assertEqual(list(self.root.iterdir()), [path])

    def test_filter_and_insertion_order_preserve_receipt_values(self):
        log = AuditLog(self.path)
        first = log.start(token=None, tool="get_account", arguments={"account_id": "acc-001"},
                          request_id=12, brief_id="brief-a")
        middle = log.start(token=None, tool="search_accounts", arguments={"query": "fixture"},
                           request_id="request-b", brief_id="brief-b")
        last = log.start(token=None, tool="read_call_notes", arguments={"account_id": "acc-001"},
                         brief_id="brief-a")
        log.finish(last, status="error", http_status=403, rows=[], elapsed_ms=2.5,
                   error="insufficient_scope")
        rows = read_audit(self.path)
        self.assertEqual([row["audit_id"] for row in rows], [first, middle, last])
        self.assertEqual(rows[0]["arguments"], {"account_id": "acc-001"})
        self.assertEqual(rows[0]["request_id"], 12)
        self.assertEqual(rows[0]["status"], "pending")
        self.assertEqual(rows[1]["request_id"], "request-b")
        self.assertIsNone(rows[2]["request_id"])
        self.assertEqual(rows[2]["http_status"], 403)
        self.assertEqual(rows[2]["elapsed_ms"], 2.5)
        self.assertEqual(rows[2]["error"], "insufficient_scope")
        self.assertEqual(rows[2]["rows"], [])
        self.assertEqual(read_audit(self.path, "brief-a"), [rows[0], rows[2]])
        self.assertEqual(log.read("brief-a"), [rows[0], rows[2]])
        self.assertEqual(read_audit(self.path, "' OR 1=1 --"), [])

    def test_cli_prints_existing_receipts_without_changing_the_database(self):
        _, identifier = self.receipt()
        expected = read_audit(self.path)
        self.path.chmod(0o400)
        before, schema = self.snapshot(self.path), self.schema(self.path)
        for brief, rows in ((None, expected), ("brief-a", expected), ("missing", [])):
            with self.subTest(brief=brief):
                result = self.run_cli(brief=brief)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, "")
                self.assertEqual(json.loads(result.stdout), rows)
                self.assertEqual(self.snapshot(self.path), before)
                self.assertEqual(self.schema(self.path), schema)
                self.assertEqual(list(self.root.iterdir()), [self.path])
        self.assertEqual(expected[0]["audit_id"], identifier)
        self.assertEqual(expected[0]["token_id"], "anonymous")

    def test_missing_file_or_directory_path_does_not_create_state(self):
        state = self.root / "missing" / "nested"
        path = state / "audit.sqlite3"
        with self.assertRaisesRegex(ValueError, "audit database not found"):
            read_audit(path)
        self.assert_cli_error(self.run_cli(state), "audit database not found")
        self.assertFalse(state.parent.exists())
        self.path.mkdir()
        before = self.path.stat().st_mtime_ns
        self.assert_cli_error(self.run_cli(), "audit database not found")
        self.assertEqual(self.path.stat().st_mtime_ns, before)
        self.assertEqual(list(self.path.iterdir()), [])

    def test_empty_wrong_schema_and_invalid_database_fail_without_mutation(self):
        for kind in ("empty", "wrong_schema", "invalid_database"):
            with self.subTest(kind=kind):
                state = self.root / kind
                state.mkdir()
                path = state / "audit.sqlite3"
                if kind == "wrong_schema":
                    with closing(sqlite3.connect(path)) as connection, connection:
                        connection.execute("CREATE TABLE audit_log (unrelated TEXT)")
                else:
                    path.write_bytes(b"" if kind == "empty" else b"not a SQLite database")
                path.chmod(0o400)
                before = self.snapshot(path)
                schema = None if kind == "invalid_database" else self.schema(path)
                with self.assertRaises(sqlite3.DatabaseError):
                    read_audit(path)
                result = self.run_cli(state)
                self.assert_cli_error(result, "error:")
                self.assertEqual(self.snapshot(path), before)
                if schema is not None:
                    self.assertEqual(self.schema(path), schema)
                self.assertEqual(list(state.iterdir()), [path])

    def test_symlink_rejected_without_changing_target_or_creating_missing_target(self):
        target = self.root / "target.sqlite3"
        self.receipt(target)
        target.chmod(0o400)
        before, schema = self.snapshot(target), self.schema(target)
        self.path.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "symbolic link"):
            read_audit(self.path)
        self.assert_cli_error(self.run_cli(), "symbolic link")
        self.assertEqual(self.snapshot(target), before)
        self.assertEqual(self.schema(target), schema)
        self.assertTrue(self.path.is_symlink())
        self.path.unlink()
        missing = self.root / "never-created.sqlite3"
        self.path.symlink_to(missing)
        self.assert_cli_error(self.run_cli(), "symbolic link")
        self.assertFalse(missing.exists())

    def test_invalid_receipt_json_fails_cleanly_without_partial_output_or_mutation(self):
        self.receipt()
        # These fields are fixed schema constants, never caller-controlled SQL.
        for field in ("arguments", "request_id", "rows"):
            for malformed in ('{"incomplete":', b"\xff"):
                with self.subTest(field=field, malformed=repr(malformed)):
                    with closing(sqlite3.connect(self.path)) as connection, connection:
                        connection.execute(f"UPDATE audit_log SET {field} = ?", (malformed,))
                    before, schema = self.snapshot(self.path), self.schema(self.path)
                    with self.assertRaisesRegex(ValueError, "invalid JSON"):
                        read_audit(self.path)
                    self.assert_cli_error(self.run_cli(), "invalid JSON")
                    self.assertEqual(self.snapshot(self.path), before)
                    self.assertEqual(self.schema(self.path), schema)
                    with closing(sqlite3.connect(self.path)) as connection, connection:
                        connection.execute(f"UPDATE audit_log SET {field} = ?", ("null",))


if __name__ == "__main__":
    unittest.main()
