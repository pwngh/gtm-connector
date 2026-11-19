"""A saved card is written whole or not at all, and only a valid one is traced."""

import json
from pathlib import Path
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
        self.assertEqual((brief["schema_version"], brief["status"]), (2, "accepted"))
        self.assertIn('role Missing', brief["text"])
        before = path.read_bytes()
        for unit in brief["units"]:
            traced = trace_saved(self.state, brief["brief_id"], unit["n"])
            self.assertEqual((traced["n"], traced["record_ref"]), (unit["n"], unit["record_ref"]))
            self.assertIn(traced["receipt"]["audit_id"], {row["audit_id"] for row in receipts})
        # A changed card text cannot change what a number resolves to.
        edited = {**brief, "text": brief["text"].replace("Missing", '"Chief buyer"')}
        path.write_text(json.dumps(edited))
        self.assertEqual(trace_saved(self.state, brief["brief_id"], 2)["fields"]["title"], "")
        # A card saved under other sentence rules, or with a unit changed, is refused: its numbers may differ.
        for changed in ({"text": "other"}, {"sanitizer_version": 1}):
            units = [dict(unit, **changed) if unit["n"] == len(brief["units"]) else unit for unit in brief["units"]]
            path.write_text(json.dumps({**brief, "units": units}))
            with self.subTest(changed=changed), self.assertRaisesRegex(ValueError, "Make it again"):
                trace_saved(self.state, brief["brief_id"], 1)
        path.write_bytes(before)
        for number in (0, len(brief["units"]) + 1, "1", 1.0, True):
            with self.subTest(number=number), self.assertRaisesRegex(ValueError, "This card has numbers 1 to"):
                trace_saved(self.state, brief["brief_id"], number)
        self.assertEqual(path.read_bytes(), before)

    def test_a_failed_save_leaves_no_card_and_keeps_the_receipts(self):
        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.os.replace", side_effect=OSError("fixture rename failure")):
                with self.assertRaisesRegex(OSError, "rename failure"):
                    create_brief(runtime, "acc-001")
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            self.assertEqual(len(runtime.connector.audit.read()), 3)

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
            self.assertEqual(len(runtime.connector.audit.read()), 4)

    def test_failed_replace_preserves_old_bytes_and_cleans_temporary_file(self):
        self.state.mkdir()
        path = self.state / "snapshot.json"
        _save_artifact(path, {"status": "collected"})
        before = path.read_bytes()
        with patch("gtm_connector.briefing.os.replace", side_effect=OSError("fixture rename failure")):
            with self.assertRaisesRegex(OSError, "rename failure"):
                _save_artifact(path, {"status": "accepted", "text": "candidate"})
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual(list(self.state.iterdir()), [path])

    def test_reader_open_before_replacement_keeps_the_complete_old_snapshot(self):
        self.state.mkdir()
        path = self.state / "snapshot.json"
        old = {"status": "collected", "evidence": {"fixture": "original"}}
        new = {**old, "status": "accepted", "text": "checked fixture text"}
        _save_artifact(path, old)
        with path.open("rb") as reader:
            _save_artifact(path, new)
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
            _save_artifact(link, {"status": "collected"})
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
                      {"schema_version": 2, "brief_id": identifier, "evidence": []},
                      {"schema_version": 2, "brief_id": identifier, "evidence": None},
                      {"schema_version": 1, "brief_id": identifier, "evidence": {"brief_id": identifier}},
                      {"schema_version": 2, "brief_id": identifier, "evidence": {"brief_id": "brief-" + "b" * 32}}):
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
