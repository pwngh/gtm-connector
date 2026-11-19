"""A saved card is written whole or not at all, and only a valid one is traced."""

from contextlib import closing
import hashlib
import json
from pathlib import Path
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.briefing import _save_artifact, create_brief, trace_saved
from gtm_connector.runtime import Runtime


class BriefPersistenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name) / "state"

    def test_a_saved_card_is_private_and_traces_from_its_stored_evidence(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-007", include_slack=True)
            receipts = runtime.connector.audit.read()
        path = self.state / "briefs" / (brief["brief_id"] + ".json")
        self.assertEqual(json.loads(path.read_text()), brief)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
        self.assertEqual((brief["schema_version"], brief["status"], brief["rules_version"]), (3, "accepted", 2))
        # The seal is one finished receipt that belongs to no brief and names the digest of the saved bytes.
        [seal] = [row for row in receipts if row["audit_id"] == brief["sealed_by"]]
        self.assertEqual((seal["tool"], seal["status"], seal["brief_id"], seal["arguments"]),
                         ("save_brief", "ok", None, {"account_id": "acc-007"}))
        self.assertEqual(seal["rows"], [f"briefs/{brief['brief_id']}/{hashlib.sha256(path.read_bytes()).hexdigest()}"])
        self.assertIn('role Missing', brief["text"])
        before = path.read_bytes()
        for unit in brief["units"]:
            traced = trace_saved(self.state, brief["brief_id"], unit["n"])
            self.assertEqual((traced["n"], traced["record_ref"]), (unit["n"], unit["record_ref"]))
            self.assertIn(traced["receipt"]["audit_id"], {row["audit_id"] for row in receipts})
        # A changed file is not the card that was sealed, whatever was changed and however it is spelled.
        for changed in ({**brief, "text": brief["text"].replace("Missing", '"Chief buyer"')},
                        {**brief, "units": brief["units"][:-1]}, brief):
            path.write_text(json.dumps(changed))
            with self.subTest(changed=sorted(changed)), self.assertRaisesRegex(ValueError, "not the card that was sealed"):
                trace_saved(self.state, brief["brief_id"], 1)
        # A card from before sealing says so.
        path.write_text(json.dumps({**brief, "schema_version": 2}))
        with self.assertRaisesRegex(ValueError, "saved before cards were sealed"):
            trace_saved(self.state, brief["brief_id"], 1)
        path.write_bytes(before)
        # The log is checked too: without the seal receipt, or one read receipt, the card does not trace.
        log = self.state / "audit.sqlite3"
        kept = log.read_bytes()
        for tool, message in (("save_brief", "not the card that was sealed"), ("get_account", "read receipts")):
            with closing(sqlite3.connect(log)) as connection, connection:
                connection.execute("DELETE FROM audit_log WHERE tool = ?", (tool,))
            with self.subTest(deleted=tool), self.assertRaisesRegex(ValueError, message):
                trace_saved(self.state, brief["brief_id"], 1)
            log.write_bytes(kept)
        for number in (0, len(brief["units"]) + 1, "1", 1.0, True):
            with self.subTest(number=number), self.assertRaisesRegex(ValueError, "This card has numbers 1 to"):
                trace_saved(self.state, brief["brief_id"], number)
        self.assertEqual(path.read_bytes(), before)

    def test_a_card_sealed_under_older_sentence_rules_keeps_its_own_numbers(self):
        with Runtime(self.state) as runtime, patch("gtm_connector.briefing.SANITIZER_VERSION", 1):
            brief = create_brief(runtime, "acc-003", include_slack=True)
        self.assertEqual(brief["rules_version"], 1)
        # Today's rules would number this card afresh. The sealed units answer, and nothing is rebuilt.
        with patch("gtm_connector.briefing.build", side_effect=AssertionError("rebuilt")):
            for unit in brief["units"]:
                traced = trace_saved(self.state, brief["brief_id"], unit["n"])
                self.assertEqual((traced["n"], traced["record_ref"], traced.get("text")),
                                 (unit["n"], unit["record_ref"], unit.get("text")))
        # Under the same rules a sealed card must also equal a rebuild from its own evidence.
        with Runtime(self.state) as runtime:
            current = create_brief(runtime, "acc-003", include_slack=True)
        with patch("gtm_connector.briefing.build", return_value=([], "")):
            with self.assertRaisesRegex(ValueError, "does not match a rebuild"):
                trace_saved(self.state, current["brief_id"], 1)

    def test_a_failed_save_leaves_no_card_and_keeps_the_receipts(self):
        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.os.replace", side_effect=OSError("fixture rename failure")):
                with self.assertRaisesRegex(OSError, "rename failure"):
                    create_brief(runtime, "acc-001")
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            # The three reads keep their receipts, and the seal is finished as failed, never left open.
            receipts = runtime.connector.audit.read()
            self.assertEqual([(row["tool"], row["status"], row["error"]) for row in receipts[3:]],
                             [("save_brief", "error", "brief_save_failed")])
            self.assertEqual(len(receipts), 4)

    def test_an_interrupted_write_leaves_no_partial_card(self):
        original = tempfile.NamedTemporaryFile

        class InterruptedWrite:
            def __init__(self, output):
                self.output = output
                self.name = output.name

            def __enter__(self):
                return self

            def __exit__(self, *args):
                return self.output.__exit__(*args)

            def fileno(self):
                return self.output.fileno()

            def write(self, content):
                self.output.write(content[:32])
                self.output.flush()
                raise OSError("fixture disk-full failure")

        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.NamedTemporaryFile",
                       side_effect=lambda *args, **kwargs: InterruptedWrite(original(*args, **kwargs))):
                with self.assertRaisesRegex(OSError, "disk-full"):
                    create_brief(runtime, "acc-003", include_slack=True)
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            self.assertEqual([row["status"] for row in runtime.connector.audit.read()], ["ok"] * 4 + ["error"])

    def test_failed_replace_preserves_old_bytes_and_cleans_temporary_file(self):
        self.state.mkdir()
        path = self.state / "snapshot.json"
        _save_artifact(path, b'{"status": "collected"}')
        before = path.read_bytes()
        with patch("gtm_connector.briefing.os.replace", side_effect=OSError("fixture rename failure")):
            with self.assertRaisesRegex(OSError, "rename failure"):
                _save_artifact(path, b'{"status": "accepted", "text": "candidate"}')
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(list(self.state.iterdir()), [path])

    def test_reader_open_before_replacement_keeps_the_complete_old_snapshot(self):
        self.state.mkdir()
        path = self.state / "snapshot.json"
        old = {"status": "collected", "evidence": {"fixture": "original"}}
        new = {**old, "status": "accepted", "text": "checked fixture text"}
        _save_artifact(path, json.dumps(old).encode())
        with path.open("rb") as reader:
            _save_artifact(path, json.dumps(new).encode())
            self.assertEqual(json.loads(reader.read()), old)
        self.assertEqual(json.loads(path.read_text()), new)
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(list(self.state.iterdir()), [path])

    def test_destination_symlink_is_not_replaced_or_followed(self):
        self.state.mkdir()
        target = self.state / "original.json"
        target.write_text("original bytes")
        link = self.state / "snapshot.json"
        link.symlink_to(target)
        with self.assertRaisesRegex(ValueError, "symbolic link"):
            _save_artifact(link, b'{"status": "collected"}')
        self.assertTrue(link.is_symlink())
        self.assertEqual(target.read_text(), "original bytes")
        self.assertEqual(set(self.state.iterdir()), {target, link})

    def test_briefs_directory_symlink_is_rejected(self):
        outside = Path(self.directory.name) / "outside"
        outside.mkdir()
        with Runtime(self.state) as runtime:
            (self.state / "briefs").symlink_to(outside, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "symbolic link"):
                create_brief(runtime, "acc-001")
        self.assertEqual(list(outside.iterdir()), [])

    def test_trace_rejects_non_object_artifacts_and_evidence(self):
        identifier = "brief-" + "a" * 32
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(parents=True)
        for value in ([], None, "not an artifact", 1,
                      {"schema_version": 3, "brief_id": identifier, "evidence": []},
                      {"schema_version": 3, "brief_id": identifier, "evidence": None},
                      {"schema_version": 1, "brief_id": identifier, "evidence": {"brief_id": identifier}},
                      {"schema_version": 3, "brief_id": identifier, "evidence": {"brief_id": "brief-" + "b" * 32}},
                      {"schema_version": 3, "brief_id": identifier, "evidence": {"brief_id": identifier}}):
            with self.subTest(value=value):
                path.write_text(json.dumps(value))
                with self.assertRaisesRegex(ValueError, "invalid saved brief evidence"):
                    trace_saved(self.state, identifier, 1)
        for unsafe in ("../audit", "brief-" + "A" * 32, "brief-short", None):
            with self.subTest(unsafe=unsafe), self.assertRaisesRegex(ValueError, "invalid brief ID"):
                trace_saved(self.state, unsafe, 1)
        with self.assertRaisesRegex(ValueError, "No saved card has this brief ID"):
            trace_saved(self.state, "brief-" + "c" * 32, 1)

    def test_cli_reports_invalid_saved_shape_without_a_traceback(self):
        identifier = "brief-" + "a" * 32
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(parents=True)
        path.write_text("[]")
        result = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "trace", identifier, "1", "--state-dir", str(self.state)],
            cwd=Path(__file__).resolve().parents[1], capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("invalid saved brief evidence", result.stderr)
        self.assertIn("gtm-connector", result.stderr)
        self.assertEqual(path.read_text(), "[]")


if __name__ == "__main__":
    unittest.main()
