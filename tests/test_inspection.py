"""Receipt filters and follow streams over committed, invented local state."""

from contextlib import closing
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.audit import AuditLog, read_audit
from gtm_connector.inspection import follow_audit, follow_usage
from gtm_connector.usage import build_usage_report


BRIEF_A = "brief-" + "a" * 32
BRIEF_B = "brief-" + "b" * 32
BRIEF_C = "brief-" + "c" * 32
CALLER_A = hashlib.sha256(b"invented-token-a").hexdigest()


class StopFollowing(Exception):
    pass


class InspectionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name)
        self.path = self.state / "audit.sqlite3"
        self.log = AuditLog(self.path)

    def call(self, *, timestamp="2026-11-18T00:00:00Z", token="invented-token-a",
             tool="get_account", status="ok", brief_id=BRIEF_A):
        identifier = self.log.start(token=token, tool=tool, brief_id=brief_id,
                                    arguments={"body": "invented private source text"})
        with closing(sqlite3.connect(self.path)) as connection, connection:
            connection.execute("UPDATE audit_log SET started_at = ? WHERE audit_id = ?", (timestamp, identifier))
        if status != "pending":
            self.log.finish(identifier, status=status, http_status=200 if status == "ok" else 403,
                            rows=[], elapsed_ms=1, error=None if status == "ok" else "insufficient_scope")
        return identifier

    def save(self, identifier, *, status="accepted", input_tokens=10, output_tokens=5):
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(exist_ok=True)
        path.write_text(json.dumps({
            "schema_version": 1, "brief_id": identifier, "account_id": "acc-001",
            "status": status, "adapter": "custom",
            "model_usage": {"model": "invented-local-model", "input_tokens": input_tokens,
                            "output_tokens": output_tokens},
            "text": "invented private draft text", "evidence": {"records": []},
        }))
        return path

    def test_time_bounds_compare_instants_with_microseconds_and_offsets(self):
        before = self.call(timestamp="2026-11-17T23:59:59.999999Z")
        start = self.call(timestamp="2026-11-18T00:00:00Z")
        same_start = self.call(timestamp="2026-11-18T01:00:00+01:00", tool="read_call_notes", status="error")
        near_end = self.call(timestamp="2026-11-18T01:59:59.999999+01:00", token=None, brief_id=BRIEF_B)
        end = self.call(timestamp="2026-11-18T01:00:00Z", status="pending")
        filters = {"since": "2026-11-17T19:00:00-05:00", "until": "2026-11-18T01:00:00Z"}
        rows = read_audit(self.path, **filters)
        self.assertEqual([row["audit_id"] for row in rows], [start, same_start, near_end])
        self.assertNotIn(before, [row["audit_id"] for row in rows])
        self.assertNotIn(end, [row["audit_id"] for row in rows])
        report = build_usage_report(self.state, **filters)
        self.assertEqual(report["calls"], {"total": 3, "ok": 2, "error": 1, "pending": 0})
        self.assertEqual(report["filters"], {"since": "2026-11-18T00:00:00Z", "until": "2026-11-18T01:00:00Z"})
        self.assertEqual(report["by_day"], [{"day": "2026-11-18", "total": 3, "ok": 2, "error": 1, "pending": 0}])

    def test_filters_combine_and_match_literal_values_without_writing(self):
        expected = self.call()
        self.call(tool="read_call_notes")
        self.call(token=None)
        self.call(status="error")
        self.call(brief_id=BRIEF_B)
        before = self.path.read_bytes(), self.path.stat().st_mode, self.path.stat().st_mtime_ns
        filters = {"tool": "get_account", "caller": CALLER_A, "status": "ok"}
        rows = read_audit(self.path, BRIEF_A, **filters)
        self.assertEqual([row["audit_id"] for row in rows], [expected])
        self.assertEqual(build_usage_report(self.state, BRIEF_A, **filters)["calls"]["total"], 1)
        self.assertEqual(read_audit(self.path, tool="' OR 1=1 --"), [])
        self.assertEqual(read_audit(self.path, "' OR 1=1 --"), [])
        self.assertEqual(len(read_audit(self.path, caller="anonymous")), 1)
        self.assertEqual((self.path.read_bytes(), self.path.stat().st_mode, self.path.stat().st_mtime_ns), before)

    def test_usage_filters_include_whole_matching_briefs_once(self):
        self.call()
        self.call()
        self.call(tool="read_call_notes", token=None, brief_id=BRIEF_B)
        self.save(BRIEF_A, input_tokens=11, output_tokens=7)
        self.save(BRIEF_B, input_tokens=13, output_tokens=3)
        self.save(BRIEF_C, input_tokens=17, output_tokens=2)
        report = build_usage_report(self.state, tool="get_account")
        self.assertEqual([brief["brief_id"] for brief in report["briefs"]], [BRIEF_A])
        self.assertEqual(report["briefs"][0]["audit_calls"], 2)
        self.assertEqual(report["model_tokens"], {"saved_briefs": 1, "unknown_briefs": 0,
                                                  "input_tokens": 11, "output_tokens": 7})
        self.assertNotIn("private", json.dumps(report))
        self.assertNotIn("invented-token", json.dumps(report))
        original = build_usage_report(self.state)
        self.assertNotIn("filters", original)
        self.assertEqual(original["model_tokens"]["saved_briefs"], 3)
        self.assertEqual(original["model_tokens"]["input_tokens"], 41)
        empty = build_usage_report(self.state, tool="unknown")
        self.assertEqual(empty["briefs"], [])
        self.assertEqual(empty["model_tokens"]["saved_briefs"], 0)

    def test_invalid_filter_rejects_before_accessing_missing_state_without_echoing_credentials(self):
        missing = self.state / "missing"
        invalid = ({"since": "2026-11-18"}, {"until": "2026-11-18T00:00:00"},
                   {"since": "invalid"}, {"since": "2026-11-18T01:00:00Z", "until": "2026-11-18T00:00:00Z"},
                   {"since": "2026-11-18T01:00:00Z", "until": "2026-11-18T01:00:00Z"},
                   {"caller": "invented-reusable-credential"}, {"tool": ""})
        for filters in invalid:
            for reader, path in ((read_audit, missing / "audit.sqlite3"), (build_usage_report, missing)):
                with self.subTest(filters=filters, reader=reader.__name__):
                    with self.assertRaises(ValueError) as raised:
                        reader(path, **filters)
                    self.assertNotIn("not found", str(raised.exception))
                    self.assertNotIn("invented-reusable-credential", str(raised.exception))
                    self.assertFalse(missing.exists())

    def test_audit_follow_catches_earlier_receipt_finalization_and_same_id_updates_once(self):
        first = self.call(status="pending")
        second = self.call()
        follower = follow_audit(self.path)
        self.addCleanup(follower.close)
        self.assertEqual(next(follower)["audit_id"], first)
        self.assertEqual(next(follower)["audit_id"], second)
        with patch("gtm_connector.inspection.time.sleep") as sleep:
            sleep.side_effect = lambda _: self.log.finish(first, status="ok", http_status=200,
                                                           rows=[], elapsed_ms=2)
            updated = next(follower)
            self.assertEqual((updated["audit_id"], updated["status"]), (first, "ok"))
            self.assertEqual(sleep.call_count, 1)

            def update_same_receipt(_):
                with closing(sqlite3.connect(self.path)) as connection, connection:
                    connection.execute("UPDATE audit_log SET elapsed_ms = 3 WHERE audit_id = ?", (first,))

            sleep.side_effect = update_same_receipt
            self.assertEqual(next(follower)["elapsed_ms"], 3)
            self.assertEqual(sleep.call_count, 2)
            sleep.side_effect = StopFollowing
            with self.assertRaises(StopFollowing):
                next(follower)
            self.assertEqual(sleep.call_count, 3)

    def test_audit_follow_reads_new_commits_after_an_initially_empty_scan(self):
        follower = follow_audit(self.path)
        self.addCleanup(follower.close)
        created = []
        with patch("gtm_connector.inspection.time.sleep", side_effect=lambda _: created.append(self.call())) as sleep:
            self.assertEqual(next(follower)["audit_id"], created[0])
            self.assertEqual(sleep.call_count, 1)

    def test_audit_follow_emits_a_receipt_when_it_enters_the_status_filter(self):
        identifier = self.call(status="pending")
        follower = follow_audit(self.path, status="ok")
        self.addCleanup(follower.close)
        with patch("gtm_connector.inspection.time.sleep", side_effect=lambda _: self.log.finish(
                identifier, status="ok", http_status=200, rows=[], elapsed_ms=1)) as sleep:
            self.assertEqual(next(follower)["audit_id"], identifier)
            self.assertEqual(sleep.call_count, 1)

    def test_usage_follow_observes_receipt_and_artifact_updates_without_repeating_unchanged_reports(self):
        identifier = self.call(status="pending")
        self.save(BRIEF_A, status="collected", input_tokens=None, output_tokens=None)
        follower = follow_usage(self.state)
        self.addCleanup(follower.close)
        initial = next(follower)
        self.assertEqual(initial["calls"]["pending"], 1)
        self.assertIsNone(initial["model_tokens"]["input_tokens"])
        with patch("gtm_connector.inspection.time.sleep") as sleep:
            def change_after_unchanged_scan(_):
                if sleep.call_count == 2:
                    self.log.finish(identifier, status="ok", http_status=200, rows=[], elapsed_ms=1)

            sleep.side_effect = change_after_unchanged_scan
            report = next(follower)
            self.assertEqual(report["calls"]["ok"], 1)
            self.assertEqual(sleep.call_count, 2)
            sleep.side_effect = lambda _: self.save(BRIEF_A, input_tokens=5, output_tokens=3)
            report = next(follower)
            self.assertEqual(report["briefs"][0]["status"], "accepted")
            self.assertEqual(report["model_tokens"]["input_tokens"], 5)
            self.assertEqual(sleep.call_count, 3)
            sleep.side_effect = StopFollowing
            with self.assertRaises(StopFollowing):
                next(follower)

    def test_follow_rejects_invalid_intervals_without_creating_state(self):
        for interval in (0, -1, float("nan"), float("inf"), True):
            for follow in (follow_audit, follow_usage):
                with self.subTest(interval=interval, follow=follow.__name__):
                    with self.assertRaisesRegex(ValueError, "positive number"):
                        next(follow(self.state / "missing", interval=interval))
        self.assertFalse((self.state / "missing").exists())


if __name__ == "__main__":
    unittest.main()
