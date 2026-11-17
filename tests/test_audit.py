"""Audit persistence and concurrency, without model or network calls."""

from contextlib import closing

from concurrent.futures import ThreadPoolExecutor
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest

from gtm_connector.audit import AuditLog


class AuditTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.path = Path(self.directory.name) / "gateway" / "audit.sqlite3"
        self.audit = AuditLog(self.path)

    def test_start_then_finish_updates_exactly_one_durable_row(self):
        before = datetime.now(timezone.utc)
        audit_id = self.audit.start(
            token="fixture-bearer-value", tool="get_account",
            arguments={"account_id": "acc-001"}, request_id=7, brief_id="brief-001",
        )
        pending = AuditLog(self.path).read()[0]
        self.assertEqual(pending["audit_id"], audit_id)
        self.assertEqual(pending["status"], "pending")
        self.assertIsNone(pending["http_status"])
        self.assertIsNone(pending["elapsed_ms"])
        self.assertIsNone(pending["error"])
        self.assertEqual(pending["rows"], [])
        self.assertGreaterEqual(datetime.fromisoformat(pending["started_at"]), before)
        self.assertLessEqual(datetime.fromisoformat(pending["started_at"]), datetime.now(timezone.utc))
        self.audit.finish(audit_id, status="ok", http_status=200,
                          rows=["crm:accounts:acc-001", "crm:contacts:con-001"], elapsed_ms=4.2)
        records = AuditLog(self.path).read()
        self.assertEqual(len(records), 1)
        result = records[0]
        self.assertEqual(result["audit_id"], audit_id)
        self.assertEqual(result["started_at"], pending["started_at"])
        self.assertEqual(result["arguments"], {"account_id": "acc-001"})
        self.assertEqual(result["request_id"], 7)
        self.assertEqual(result["status"], "ok")
        self.assertEqual(result["row_count"], 2)
        self.assertEqual(result["rows"], ["crm:accounts:acc-001", "crm:contacts:con-001"])
        self.assertEqual(result["elapsed_ms"], 4.2)

    def test_bearer_value_never_persists_or_renders(self):
        token = "fixture-secret-bearer-681f900cb"
        self.audit.start(token=token, tool="search_accounts", arguments={"query": "acc"})
        record = self.audit.read()[0]
        self.assertEqual(record["token_id"], hashlib.sha256(token.encode()).hexdigest())
        self.assertNotIn(token, json.dumps(self.audit.read()))
        self.assertNotIn(token.encode(), self.path.read_bytes())
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)

    def test_directory_path_is_rejected_without_changing_its_permissions(self):
        directory = Path(self.directory.name) / "not-a-database"
        directory.mkdir(mode=0o700)
        permissions = stat.S_IMODE(directory.stat().st_mode)
        with self.assertRaisesRegex(ValueError, "regular file"):
            AuditLog(directory)
        self.assertEqual(stat.S_IMODE(directory.stat().st_mode), permissions)

    def test_brief_filter_and_insertion_order_preserve_attempts(self):
        ids = []
        for brief_id in ("brief-001", "brief-002", "brief-001", None):
            ids.append(self.audit.start(token=None, tool="unknown' tool", arguments=["raw", 1],
                                        request_id="rpc' id", brief_id=brief_id))
        self.assertEqual([row["audit_id"] for row in self.audit.read()], ids)
        filtered = self.audit.read("brief-001")
        self.assertEqual([row["audit_id"] for row in filtered], [ids[0], ids[2]])
        self.assertEqual(filtered[0]["token_id"], "anonymous")
        self.assertEqual(filtered[0]["arguments"], ["raw", 1])
        self.assertEqual(filtered[0]["request_id"], "rpc' id")
        self.assertEqual(self.audit.read("' OR 1=1 --"), [])

    def test_unknown_and_already_finalized_receipts_are_rejected(self):
        with self.assertRaises(ValueError):
            self.audit.finish("missing", status="ok", http_status=200, rows=[], elapsed_ms=1)
        audit_id = self.audit.start(token=None, tool="read_call_notes", arguments={})
        self.audit.finish(audit_id, status="forbidden", http_status=403, rows=[], elapsed_ms=2,
                          error="calls scope required")
        before = self.audit.read()
        with self.assertRaises(ValueError):
            self.audit.finish(audit_id, status="ok", http_status=200, rows=[], elapsed_ms=3)
        self.assertEqual(self.audit.read(), before)

    def test_failed_finish_preserves_pending_receipt(self):
        audit_id = self.audit.start(token=None, tool="log_activity", arguments={})
        with closing(sqlite3.connect(self.path)) as connection, connection:
            connection.execute("CREATE TRIGGER deny_update BEFORE UPDATE ON audit_log "
                               "BEGIN SELECT RAISE(ABORT, 'fixture failure'); END")
        with self.assertRaises(sqlite3.IntegrityError):
            self.audit.finish(audit_id, status="ok", http_status=200, rows=[], elapsed_ms=2)
        records = AuditLog(self.path).read()
        self.assertEqual(len(records), 1)
        self.assertEqual(records[0]["status"], "pending")

    def test_failed_start_is_not_suppressed(self):
        with closing(sqlite3.connect(self.path)) as connection, connection:
            connection.execute("CREATE TRIGGER deny_insert BEFORE INSERT ON audit_log "
                               "BEGIN SELECT RAISE(ABORT, 'fixture failure'); END")
        with self.assertRaises(sqlite3.IntegrityError):
            self.audit.start(token=None, tool="get_account", arguments={})
        self.assertEqual(self.audit.read(), [])

    def test_ten_parallel_calls_each_keep_one_receipt(self):
        def call(number):
            audit_id = self.audit.start(token=f"fixture-token-{number}", tool="get_account",
                                        arguments={"account_id": f"acc-{number + 1:03}"},
                                        request_id=number, brief_id="parallel")
            self.audit.finish(audit_id, status="ok", http_status=200,
                              rows=[f"crm:accounts:acc-{number + 1:03}"], elapsed_ms=1.0)
            return audit_id

        with ThreadPoolExecutor(max_workers=10) as pool:
            ids = list(pool.map(call, range(10)))
        records = AuditLog(self.path).read("parallel")
        self.assertEqual(len(set(ids)), 10)
        self.assertEqual({row["audit_id"] for row in records}, set(ids))
        self.assertEqual(len(records), 10)
        self.assertTrue(all(row["status"] == "ok" and row["row_count"] == 1 for row in records))


if __name__ == "__main__":
    unittest.main()
