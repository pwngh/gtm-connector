"""A failed snapshot replacement must not destroy previously collected evidence."""

import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

from gtm_connector.briefing import _save_artifact, check_saved_brief, create_brief
from gtm_connector.citations import BriefRejected, build_facts
from gtm_connector.drafting import DraftResult
from gtm_connector.runtime import Runtime
from gtm_connector.usage import build_usage_report


class BriefPersistenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name) / "state"

    def test_saved_empty_role_keeps_legacy_spelling_without_loosening_new_drafts(self):
        for include_slack in (False, True):
            state = self.state / ("slack" if include_slack else "calls")
            with self.subTest(include_slack=include_slack), Runtime(state) as runtime:
                current = create_brief(runtime, "acc-007", include_slack=include_slack)
                current_line = ('- Contact "Dana Crest": role not provided; '
                                'email "dana@juniper-crest.example". [crm/contacts/con-007]')
                legacy_line = current_line.replace("role not provided;", 'role "";')
                self.assertIn(current_line, current["text"].splitlines())
                self.assertNotIn(legacy_line, current["text"].splitlines())
                legacy_text = current["text"].replace(current_line, legacy_line)
                path = state / "briefs" / (current["brief_id"] + ".json")
                _save_artifact(path, {**current, "text": legacy_text})
                before = path.read_bytes()
                receipts = runtime.connector.audit.read()
                self.assertEqual(check_saved_brief(state, current["brief_id"], legacy_text), legacy_text)
                self.assertEqual(check_saved_brief(state, current["brief_id"], current["text"]), current["text"])
                bad_candidates = (
                    legacy_text.replace('role "";', 'role "Decision maker";'),
                    legacy_text.replace(legacy_line,
                                        legacy_line.replace("crm/contacts/con-007", "crm/accounts/acc-007")),
                    legacy_text + legacy_line + "\n",
                    legacy_text + current_line + "\n",
                )
                for candidate in bad_candidates:
                    with self.subTest(candidate=candidate), self.assertRaises(BriefRejected):
                        check_saved_brief(state, current["brief_id"], candidate)
                self.assertEqual(path.read_bytes(), before)
                self.assertEqual(runtime.connector.audit.read(), receipts)
                regenerated = create_brief(runtime, "acc-007", include_slack=include_slack)
                self.assertIn(current_line, regenerated["text"].splitlines())
                self.assertNotIn(legacy_line, regenerated["text"].splitlines())

    def test_current_saved_brief_keeps_missing_follow_up_and_duplicate_failure_reasons(self):
        with Runtime(self.state) as runtime:
            current = create_brief(runtime, "acc-007", include_slack=True)
            contact = next(line for line in current["text"].splitlines() if line.startswith("- Contact "))
            self.assertIn("role not provided;", contact)
            path = self.state / "briefs" / (current["brief_id"] + ".json")
            before = path.read_bytes()
            receipts = runtime.connector.audit.read()
            candidates = (
                (current["text"].replace(current["proposed_follow_up"] + "\n", ""),
                 "Brief must disclose required source outcomes, source conflicts, and proposed follow-up."),
                (current["text"] + contact + "\n", "Brief repeats a statement."),
                (None, "A brief must contain supported, cited statements."),
            )
            for candidate, message in candidates:
                with self.subTest(message=message):
                    with self.assertRaises(BriefRejected) as failure:
                        check_saved_brief(self.state, current["brief_id"], candidate)
                    self.assertEqual(str(failure.exception), message)
            self.assertEqual(path.read_bytes(), before)
            self.assertEqual(runtime.connector.audit.read(), receipts)

    def test_failed_initial_save_never_invokes_the_drafter(self):
        adapter = Mock()
        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.os.replace", side_effect=OSError("fixture rename failure")):
                with self.assertRaisesRegex(OSError, "rename failure"):
                    create_brief(runtime, "acc-001", adapter=adapter)
            adapter.draft.assert_not_called()
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            self.assertEqual(len(runtime.connector.audit.read()), 3)

    def test_failed_final_write_preserves_collected_evidence(self):
        attempts = 0
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

        def temporary_file(*args, **kwargs):
            nonlocal attempts
            attempts += 1
            output = original(*args, **kwargs)
            return InterruptedWrite(output) if attempts == 2 else output

        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.NamedTemporaryFile", side_effect=temporary_file):
                with self.assertRaisesRegex(OSError, "disk-full"):
                    create_brief(runtime, "acc-001")
            saved = list((self.state / "briefs").iterdir())
            self.assertEqual(len(saved), 1)
            artifact = json.loads(saved[0].read_text())
            self.assertEqual(artifact["status"], "collected")
            self.assertNotIn("text", artifact)
            self.assertEqual(len(runtime.connector.audit.read(artifact["brief_id"])), 3)
            self.assertEqual(stat.S_IMODE(saved[0].stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(saved[0].parent.stat().st_mode), 0o700)
            candidate = "\n".join(build_facts(artifact["evidence"]))
            self.assertEqual(check_saved_brief(self.state, artifact["brief_id"], candidate),
                             candidate + "\n")

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

    def test_failed_model_save_keeps_slack_evidence_and_usage_unknown(self):
        class FakeModel:
            name = "ollama"

            def draft(self, evidence):
                return DraftResult("\n".join(build_facts(evidence)), model="fixture-model",
                                   input_tokens=13, output_tokens=7)

        from gtm_connector.briefing import os as save_os
        replace = save_os.replace
        attempts = 0

        def fail_final_replace(source, destination):
            nonlocal attempts
            attempts += 1
            if attempts == 2:
                raise OSError("fixture final-save failure")
            return replace(source, destination)

        with Runtime(self.state) as runtime:
            with patch("gtm_connector.briefing.os.replace", side_effect=fail_final_replace):
                with self.assertRaisesRegex(OSError, "final-save failure"):
                    create_brief(runtime, "acc-003", include_slack=True, adapter=FakeModel())
            saved = list((self.state / "briefs").iterdir())
            self.assertEqual(len(saved), 1)
            artifact = json.loads(saved[0].read_text())
            self.assertEqual(artifact["status"], "collected")
            self.assertEqual(artifact["evidence"]["workflow_version"], 2)
            self.assertNotIn("text", artifact)
            self.assertNotIn("proposed_follow_up", artifact)
            self.assertEqual(len(runtime.connector.audit.read(artifact["brief_id"])), 4)
            candidate = "\n".join(build_facts(artifact["evidence"]))
            self.assertEqual(check_saved_brief(self.state, artifact["brief_id"], candidate),
                             candidate + "\n")
            usage = build_usage_report(self.state)
            self.assertEqual(usage["model_tokens"], {"saved_briefs": 1, "unknown_briefs": 1,
                                                    "input_tokens": None, "output_tokens": None})

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

    def test_saved_checker_rejects_non_object_artifacts_and_evidence(self):
        identifier = "brief-" + "a" * 32
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(parents=True)
        for value in ([], None, "not an artifact", 1,
                      {"schema_version": 1, "brief_id": identifier, "evidence": []},
                      {"schema_version": 1, "brief_id": identifier, "evidence": None}):
            with self.subTest(value=value):
                path.write_text(json.dumps(value))
                with self.assertRaisesRegex(ValueError, "invalid saved brief evidence"):
                    check_saved_brief(self.state, identifier, "candidate")

    def test_cli_reports_invalid_saved_shape_without_a_traceback(self):
        identifier = "brief-" + "a" * 32
        path = self.state / "briefs" / (identifier + ".json")
        path.parent.mkdir(parents=True)
        path.write_text("[]")
        candidate = Path(self.directory.name) / "candidate.txt"
        candidate.write_text("Uncited candidate.\n")
        result = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "check-brief", identifier, str(candidate),
             "--state-dir", str(self.state)], cwd=Path(__file__).resolve().parents[1],
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("invalid saved brief evidence", result.stderr)
        self.assertIn("gtm-connector", result.stderr)
        self.assertEqual(path.read_text(), "[]")


if __name__ == "__main__":
    unittest.main()
