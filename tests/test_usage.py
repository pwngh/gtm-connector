"""Independent usage arithmetic over completed, rejected, and pending calls."""

from contextlib import closing
import hashlib
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from gtm_connector.audit import AuditLog
from gtm_connector.usage import build_usage_report


BRIEF_A = "brief-" + "a" * 32
BRIEF_B = "brief-" + "b" * 32
BRIEF_C = "brief-" + "c" * 32


class UsageTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name) / "state"
        self.audit = AuditLog(self.state / "audit.sqlite3")

    def add_call(self, token, tool, timestamp, status, brief_id=None):
        identifier = self.audit.start(token=token, tool=tool,
                                      arguments={"body": "private activity fixture text"},
                                      brief_id=brief_id)
        with closing(sqlite3.connect(self.audit.path)) as connection, connection:
            connection.execute("UPDATE audit_log SET started_at = ? WHERE audit_id = ?",
                               (timestamp, identifier))
        if status != "pending":
            self.audit.finish(identifier, status=status, http_status=200 if status == "ok" else 403,
                              rows=[], elapsed_ms=1)

    def save_brief(self, identifier, *, status="accepted", adapter="fixture",
                   model=None, input_tokens=0, output_tokens=0):
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(exist_ok=True)
        artifact = {"schema_version": 1, "brief_id": identifier, "account_id": "acc-001",
                    "status": status, "adapter": adapter,
                    "model_usage": {"model": model, "input_tokens": input_tokens,
                                    "output_tokens": output_tokens},
                    "text": "private draft fixture text", "evidence": {"records": []}}
        path.write_text(json.dumps(artifact))
        return path

    def test_groups_count_every_attempt_and_keep_unknown_usage(self):
        day_one = "2026-11-17T23:00:00Z"
        day_two = "2026-11-18T01:00:00Z"
        self.add_call("token-a", "get_account", day_one, "ok", BRIEF_A)
        self.add_call("token-a", "read_call_notes", day_one, "error", BRIEF_A)
        self.add_call("token-a", "list_opportunities", day_two, "pending", BRIEF_A)
        self.add_call("token-b", "get_account", day_two, "ok", BRIEF_B)
        self.add_call("token-b", "get_account", day_one, "ok", BRIEF_B)
        self.add_call("token-b", "read_call_notes", day_two, "error", BRIEF_B)
        self.add_call(None, "unknown_tool", day_two, "error")
        self.save_brief(BRIEF_A)
        self.save_brief(BRIEF_B, status="rejected", adapter="custom",
                        input_tokens=None, output_tokens=None)

        report = build_usage_report(self.state)
        self.assertEqual(report["calls"], {"total": 7, "ok": 3, "error": 3, "pending": 1})
        token_a = hashlib.sha256(b"token-a").hexdigest()
        token_b = hashlib.sha256(b"token-b").hexdigest()
        by_token = {row["token_id"]: row for row in report["by_token"]}
        self.assertEqual(by_token[token_a], {"token_id": token_a, "total": 3,
                                             "ok": 1, "error": 1, "pending": 1})
        self.assertEqual(by_token[token_b]["ok"], 2)
        self.assertEqual(by_token["anonymous"]["error"], 1)
        self.assertEqual(report["by_day"], [
            {"day": "2026-11-17", "total": 3, "ok": 2, "error": 1, "pending": 0},
            {"day": "2026-11-18", "total": 4, "ok": 1, "error": 2, "pending": 1}])
        self.assertEqual(report["by_tool"], [
            {"tool": "get_account", "total": 3, "ok": 3, "error": 0, "pending": 0},
            {"tool": "list_opportunities", "total": 1, "ok": 0, "error": 0, "pending": 1},
            {"tool": "read_call_notes", "total": 2, "ok": 0, "error": 2, "pending": 0},
            {"tool": "unknown_tool", "total": 1, "ok": 0, "error": 1, "pending": 0}])
        self.assertEqual(sum(row["total"] for row in report["by_token_tool_day"]), 7)
        self.assertIn({"token_id": token_a, "tool": "list_opportunities", "day": "2026-11-18",
                       "total": 1, "ok": 0, "error": 0, "pending": 1}, report["by_token_tool_day"])
        self.assertEqual([row["audit_calls"] for row in report["briefs"]], [3, 3])
        self.assertEqual(report["model_tokens"], {"saved_briefs": 2, "unknown_briefs": 1,
                                                  "input_tokens": None, "output_tokens": None})
        for private in ("token-a", "token-b", "private activity fixture text", "private draft fixture text"):
            self.assertNotIn(private, json.dumps(report))

    def test_brief_filter_applies_to_calls_and_artifacts(self):
        for identifier in (BRIEF_A, BRIEF_A, BRIEF_B):
            self.add_call("token-a", "get_account", "2026-11-17T12:00:00Z", "ok", identifier)
        self.save_brief(BRIEF_A)
        self.save_brief(BRIEF_B, adapter="custom", input_tokens=None, output_tokens=None)
        report = build_usage_report(self.state, BRIEF_A)
        self.assertEqual(report["brief_id"], BRIEF_A)
        self.assertEqual(report["calls"]["total"], 2)
        self.assertEqual([row["brief_id"] for row in report["briefs"]], [BRIEF_A])
        self.assertEqual(report["model_tokens"], {"saved_briefs": 1, "unknown_briefs": 0,
                                                  "input_tokens": 0, "output_tokens": 0})
        empty = build_usage_report(self.state, "' OR 1=1 --")
        self.assertEqual(empty["calls"]["total"], 0)
        self.assertEqual(empty["briefs"], [])

    def test_utc_day_is_independent_of_timestamp_offset(self):
        self.add_call(None, "get_account", "2026-11-18T00:30:00+02:00", "ok")
        report = build_usage_report(self.state)
        self.assertEqual(report["day_basis"], "UTC")
        self.assertEqual(report["by_day"][0]["day"], "2026-11-17")

    def test_usage_totals_distinguish_known_and_unknown_dimensions(self):
        self.save_brief(BRIEF_A, status="collected")
        self.save_brief(BRIEF_B, adapter="custom", model="fixture-test-model",
                        input_tokens=13, output_tokens=5)
        known = build_usage_report(self.state)
        self.assertEqual(known["model_tokens"], {"saved_briefs": 2, "unknown_briefs": 0,
                                                 "input_tokens": 13, "output_tokens": 5})
        self.save_brief(BRIEF_C, status="collected", adapter="custom",
                        input_tokens=None, output_tokens=0)
        unknown = build_usage_report(self.state)
        self.assertIsNone(unknown["model_tokens"]["input_tokens"])
        self.assertEqual(unknown["model_tokens"]["output_tokens"], 5)
        self.assertEqual(unknown["model_tokens"]["unknown_briefs"], 1)
        self.assertEqual([row["status"] for row in unknown["briefs"]],
                         ["collected", "accepted", "collected"])

    def test_report_does_not_mutate_files_or_permissions(self):
        self.add_call("token-a", "get_account", "2026-11-17T12:00:00Z", "ok", BRIEF_A)
        artifact = self.save_brief(BRIEF_A)
        files = (self.audit.path, artifact)
        self.audit.path.chmod(0o400)
        before = {path: (path.read_bytes(), path.stat().st_mode, path.stat().st_mtime_ns) for path in files}
        self.assertEqual(build_usage_report(self.state)["calls"]["total"], 1)
        after = {path: (path.read_bytes(), path.stat().st_mode, path.stat().st_mtime_ns) for path in files}
        self.assertEqual(before, after)

    def test_missing_audit_does_not_create_state(self):
        missing = Path(self.directory.name) / "not-created" / "nested"
        with self.assertRaisesRegex(ValueError, "audit database not found"):
            build_usage_report(missing)
        self.assertFalse(missing.parent.exists())

    def test_partial_or_invalid_usage_is_an_error_not_silently_skipped(self):
        path = self.save_brief(BRIEF_A)
        original = json.loads(path.read_text())
        path.write_text('{"brief_id":')
        with self.assertRaisesRegex(ValueError, "invalid brief usage artifact"):
            build_usage_report(self.state)
        for invalid in (None, -1, True, "0"):
            with self.subTest(input_tokens=invalid):
                original["model_usage"]["input_tokens"] = invalid
                path.write_text(json.dumps(original))
                with self.assertRaisesRegex(ValueError, "invalid brief usage artifact"):
                    build_usage_report(self.state)

    def test_symlink_artifact_is_rejected(self):
        path = self.save_brief(BRIEF_A)
        outside = Path(self.directory.name) / "outside.json"
        path.rename(outside)
        path.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "invalid brief usage artifact"):
            build_usage_report(self.state)


if __name__ == "__main__":
    unittest.main()
