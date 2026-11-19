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

from gtm_connector.audit import read_audit
from gtm_connector.briefing import _save_artifact, create_brief, trace_saved
from gtm_connector.client import LocalClient
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
        self.assertEqual((brief["schema_version"], brief["status"], brief["rules_version"]), (3, "accepted", 3))
        # The seal is one finished receipt that belongs to no brief and names the digest of the saved bytes.
        [seal] = [row for row in receipts if row["audit_id"] == brief["sealed_by"]]
        self.assertEqual((seal["tool"], seal["status"], seal["brief_id"], seal["arguments"]),
                         ("save_brief", "ok", None, {"account_id": "acc-007", "brief_id": brief["brief_id"]}))
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
        # The log is checked too: the seal must be a finished save_brief receipt, and every read receipt the
        # card names must be there unchanged.
        log = self.state / "audit.sqlite3"
        kept = log.read_bytes()
        for change, message in (("DELETE FROM audit_log WHERE tool = 'save_brief'", "seal receipt cannot be read"),
                                ("UPDATE audit_log SET tool = 'log_activity' WHERE tool = 'save_brief'",
                                 "not the card that was sealed"),
                                ("UPDATE audit_log SET status = 'error' WHERE tool = 'save_brief'",
                                 "not the card that was sealed"),
                                ("DELETE FROM audit_log WHERE tool = 'get_account'", "read receipts"),
                                ("UPDATE audit_log SET rows = '[]' WHERE tool = 'get_account'", "read receipts")):
            with closing(sqlite3.connect(log)) as connection, connection:
                connection.execute(change)
            with self.subTest(change=change), self.assertRaisesRegex(ValueError, message):
                trace_saved(self.state, brief["brief_id"], 1)
            log.write_bytes(kept)
        for number in (0, len(brief["units"]) + 1, "1", 1.0, True):
            with self.subTest(number=number), self.assertRaisesRegex(ValueError, "This card has numbers 1 to"):
                trace_saved(self.state, brief["brief_id"], number)
        self.assertEqual(path.read_bytes(), before)

    def reseal(self, brief, **changes):
        """Rewrite a saved card and its seal together, which only the owner of the state folder can do."""
        payload = json.dumps({**brief, **changes}).encode()
        (self.state / "briefs" / (brief["brief_id"] + ".json")).write_bytes(payload)
        with closing(sqlite3.connect(self.state / "audit.sqlite3")) as connection, connection:
            connection.execute("UPDATE audit_log SET rows = ? WHERE audit_id = ?", (json.dumps(
                [f"briefs/{brief['brief_id']}/{hashlib.sha256(payload).hexdigest()}"]), brief["sealed_by"]))

    def test_a_sealed_card_answers_from_its_own_units_whatever_rules_are_installed(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-003", include_slack=True)
        # A rule fix, another Python, or a changed seller list would build other units from the same evidence.
        # The sealed ones are the card, so nothing is rebuilt and every number still answers.
        with patch("gtm_connector.briefing.build", side_effect=AssertionError("rebuilt")), \
                patch("gtm_connector.units.build", side_effect=AssertionError("rebuilt")):
            for unit in brief["units"]:
                traced = trace_saved(self.state, brief["brief_id"], unit["n"])
                self.assertEqual((traced["n"], traced["record_ref"], traced.get("text")),
                                 (unit["n"], unit["record_ref"], unit.get("text")))
        # Older rules let a mention through. The sealed unit answers, and today's rules neutralize what it shows.
        last = brief["units"][-1]
        self.assertEqual(last["kind"], "sentence")
        self.reseal(brief, rules_version=1, units=brief["units"][:-1] + [{**last, "text": "<!channel> " + last["text"]}])
        traced = trace_saved(self.state, brief["brief_id"], last["n"])
        self.assertEqual((traced["n"], traced["record_ref"], traced["text"]),
                         (last["n"], last["record_ref"], "[mention channel] " + last["text"]))
        # Nothing is rebuilt, and a unit still answers only as itself: under its own number, for a record of
        # this card that the receipt of its read returned. Only the folder's owner can write a file like these.
        for wrong in ({**last, "n": 1}, {**last, "record_ref": "calls/call_notes/note-999"},
                      {**last, "record_ref": brief["units"][0]["record_ref"]}, {**last, "tool": "log_activity"}):
            self.reseal(brief, units=brief["units"][:-1] + [wrong])
            with self.subTest(wrong=wrong), self.assertRaisesRegex(ValueError, "does not resolve to a record"):
                trace_saved(self.state, brief["brief_id"], last["n"])

    def test_a_later_call_that_names_a_saved_card_does_not_stop_it_tracing(self):
        with Runtime(self.state) as runtime:
            brief = create_brief(runtime, "acc-001")
            with LocalClient(runtime.config) as client:
                token = client.authorize(["crm:read"])
                self.assertEqual(client.call(token, "get_account", {"account_id": "acc-002"},
                                             brief_id=brief["brief_id"]).status_code, 200)
            self.assertEqual(len(runtime.connector.audit.read(brief["brief_id"])),
                             len(brief["evidence"]["receipts"]) + 1)
        self.assertEqual(trace_saved(self.state, brief["brief_id"], 1)["n"], 1)

    def test_a_save_that_cannot_finish_leaves_no_card_and_no_open_receipt(self):
        with Runtime(self.state) as runtime:
            # The card cannot be put together: the seal is finished as failed, and no file is left.
            with patch("gtm_connector.briefing.source_names", side_effect=ValueError("fixture assembly failure")):
                with self.assertRaisesRegex(ValueError, "assembly failure"):
                    create_brief(runtime, "acc-001")
            self.assertEqual([(row["tool"], row["status"]) for row in runtime.connector.audit.read()][-1],
                             ("save_brief", "error"))
            # The file is whole and the seal cannot be finished: a file with no finished seal is not kept.
            finish = runtime.connector.audit.finish

            def refuse_the_seal(audit_id, **fields):
                if fields["status"] == "ok" and fields["rows"][0].startswith("briefs/"):
                    raise ValueError("fixture audit failure")
                return finish(audit_id, **fields)

            with patch.object(runtime.connector.audit, "finish", side_effect=refuse_the_seal):
                with self.assertRaisesRegex(ValueError, "audit failure"):
                    create_brief(runtime, "acc-001")
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            # Ctrl-C after the file is whole: the file goes, and the seal is finished as failed.
            def stopped(path, payload):
                path.write_bytes(payload)
                raise KeyboardInterrupt
            with patch("gtm_connector.briefing._save_artifact", side_effect=stopped):
                with self.assertRaises(KeyboardInterrupt):
                    create_brief(runtime, "acc-001")
            self.assertEqual(list((self.state / "briefs").iterdir()), [])
            self.assertEqual([(row["tool"], row["status"]) for row in runtime.connector.audit.read()][-1],
                             ("save_brief", "error"))
            # A hard stop between the file and the finished seal is the one way to a pending seal. Trace says
            # so, and the pending receipt names the card.
            brief = create_brief(runtime, "acc-001")
        with closing(sqlite3.connect(self.state / "audit.sqlite3")) as connection, connection:
            connection.execute("UPDATE audit_log SET status = 'pending' WHERE audit_id = ?", (brief["sealed_by"],))
        with self.assertRaisesRegex(ValueError, "seal was never finished"):
            trace_saved(self.state, brief["brief_id"], 1)
        [pending] = read_audit(self.state / "audit.sqlite3", status="pending")
        self.assertEqual(pending["arguments"]["brief_id"], brief["brief_id"])

    def test_one_broken_receipt_elsewhere_does_not_stop_a_healthy_card(self):
        with Runtime(self.state) as runtime:
            healthy = create_brief(runtime, "acc-001")
            other = create_brief(runtime, "acc-003")
        with closing(sqlite3.connect(self.state / "audit.sqlite3")) as connection, connection:
            connection.execute("UPDATE audit_log SET rows = 'not json' WHERE audit_id = ?", (other["sealed_by"],))
        self.assertEqual(trace_saved(self.state, healthy["brief_id"], 1)["n"], 1)
        with self.assertRaisesRegex(ValueError, "seal receipt cannot be read"):
            trace_saved(self.state, other["brief_id"], 1)
        # One card's seal does not seal another card's file.
        with Runtime(self.state) as runtime:
            third = create_brief(runtime, "acc-002")
        path = self.state / "briefs" / (healthy["brief_id"] + ".json")
        sealed = path.read_bytes()
        path.write_text(path.read_text().replace(healthy["sealed_by"], third["sealed_by"]))
        with self.assertRaisesRegex(ValueError, "not the card that was sealed"):
            trace_saved(self.state, healthy["brief_id"], 1)
        # An untouched card copied to another state folder was not changed. Its seal is simply not there.
        elsewhere = self.state.parent / "elsewhere"
        with Runtime(elsewhere) as runtime:
            create_brief(runtime, "acc-001")
        (elsewhere / "briefs" / path.name).write_bytes(sealed)
        with self.assertRaisesRegex(ValueError, "seal receipt cannot be read from the audit log of this state folder"):
            trace_saved(elsewhere, healthy["brief_id"], 1)
        # The same in a folder that has no audit log at all.
        (self.state.parent / "empty" / "briefs").mkdir(parents=True)
        (self.state.parent / "empty" / "briefs" / path.name).write_bytes(sealed)
        with self.assertRaisesRegex(ValueError, "seal receipt cannot be read"):
            trace_saved(self.state.parent / "empty", healthy["brief_id"], 1)

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
