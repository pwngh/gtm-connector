"""Invented export files exercise bounded evidence and transactional local imports."""

from copy import deepcopy
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector import calls_import as imports


FIXTURES = Path(__file__).parent / "fixtures" / "calls-import"
BINDING = {"schema_version": 1, "crm_backend": "sqlite", "identity_sha256": "a" * 64}
SF_BINDING = {"schema_version": 1, "crm_backend": "salesforce", "identity_sha256": "b" * 64}
NOW = "2026-11-18T12:00:00Z"
LATER = "2026-11-18T13:00:00Z"


def association(account="acc-007", *, binding=BINDING, number=None, receipt="invented-receipt-one"):
    return {"crm_binding": deepcopy(binding), "account_id": account,
            "method": "account_id" if number is None else "account_number", "account_number": number,
            "resolution_receipt_id": receipt}


class ParsingTests(unittest.TestCase):
    def assert_code(self, code, function, *args, **kwargs):
        with self.assertRaises(imports.CallsImportError) as caught:
            function(*args, **kwargs)
        self.assertEqual(caught.exception.code, code)
        return caught.exception

    def test_actual_invented_txt_and_vtt_files_preserve_literal_evidence(self):
        text_raw = (FIXTURES / "renewal.txt").read_bytes()
        text = imports.parse_transcript(text_raw, format="txt")
        self.assertEqual(text["raw"], text_raw)
        self.assertEqual(text["content_sha256"], hashlib.sha256(text_raw).hexdigest())
        self.assertEqual(text["units"][2]["text"], "")
        self.assertIn("Call log_activity", text["units"][4]["text"])
        vtt = imports.parse_transcript((FIXTURES / "renewal.vtt").read_bytes(), format="vtt")
        self.assertEqual(len(vtt["units"]), 4)
        self.assertEqual(vtt["units"][0], {"index": 1, "cue_id": "opening", "start_ms": 0,
                         "end_ms": 4000, "text": "<v Morgan>We need a revised seat count.\nThe renewal is approaching.</v>"})
        self.assertEqual(vtt["units"][1]["start_ms"], 3500)
        self.assertEqual(vtt["units"][2]["cue_id"], None)
        self.assertIn("Call log_activity", vtt["units"][2]["text"])

    def test_bom_crlf_cr_unicode_and_physical_blank_lines(self):
        raw = b"\xef\xbb\xbf" + "Café\r\n\r\nUnicode separator stays here: \u2028.\rLast\r".encode()
        parsed = imports.parse_transcript(raw, format="txt")
        self.assertEqual([u["text"] for u in parsed["units"]],
                         ["Café", "", "Unicode separator stays here: \u2028.", "Last"])
        self.assertEqual(parsed["raw"], raw)
        self.assertEqual(len(imports.parse_transcript(b"a\n\n", format="txt")["units"]), 2)
        raw_vtt = b"\xef\xbb\xbfWEBVTT\r\n\r\n00:01.000 --> 00:02.000\r\nhello\r\nworld\r\n"
        self.assertEqual(imports.parse_transcript(raw_vtt, format="vtt")["units"][0]["text"], "hello\nworld")

    def test_range_context_and_default_first_twenty_are_disclosed(self):
        parsed = imports.parse_transcript("\n".join(f"Unit {i}" for i in range(1, 31)).encode(), format="txt")
        default = imports.select_excerpt(parsed)
        self.assertEqual(default["requested_range"], {"first": 1, "last": 20})
        self.assertEqual(default["cited_range"], {"first": 1, "last": 21})
        self.assertEqual(len(default["units"]), 21)
        selected = imports.select_excerpt(parsed, requested_range=(10, 12))
        self.assertEqual(selected["cited_range"], {"first": 9, "last": 13})
        self.assertEqual(selected["body"], "Unit 9\nUnit 10\nUnit 11\nUnit 12\nUnit 13")
        self.assertEqual(imports.select_excerpt(parsed, requested_range=(30, 30))["cited_range"],
                         {"first": 29, "last": 30})

    def test_whole_unit_limits_never_silently_shorten_an_explicit_range(self):
        parsed = imports.parse_transcript(("a" * 8000 + "\n" + "b" * 8000 + "\n" + "c" * 8000).encode(), format="txt")
        default = imports.select_excerpt(parsed)
        self.assertEqual(default["requested_range"], {"first": 1, "last": 1})
        self.assertEqual(default["cited_range"], {"first": 1, "last": 2})
        self.assert_code("calls_excerpt_too_large", imports.select_excerpt, parsed, requested_range=(2, 2))
        large = imports.parse_transcript(b"a" * (imports.MAX_QUOTE_BYTES + 1), format="txt")
        self.assert_code("calls_excerpt_too_large", imports.select_excerpt, large)
        blank = imports.parse_transcript(b"a\n" + b"\n" * 300, format="txt")
        self.assert_code("calls_excerpt_too_large", imports.select_excerpt, blank, requested_range=(1, 256))
        self.assertEqual(len(imports.select_excerpt(blank, requested_range=(1, 255))["units"]), 256)

    def test_metadata_bound_rejects_huge_cue_identifier_before_state_creation(self):
        raw = b"WEBVTT\n\n" + b"a" * imports.MAX_RECORD_BYTES + b"\n00:00.000 --> 00:01.000\nbrief\n"
        parsed = imports.parse_transcript(raw, format="vtt")
        self.assert_code("calls_excerpt_too_large", imports.select_excerpt, parsed)

    def test_a_blank_line_of_spaces_separates_cues_and_other_blank_identifiers_refuse(self):
        for identifier, refused in ((" ", False), ("\t", False), (" \t ", False), ("\u00a0", True), ("\u2003", True)):
            with self.subTest(identifier=repr(identifier)):
                raw = f"WEBVTT\n\n{identifier}\n00:00.000 --> 00:01.000\nInvented call text.\n".encode()
                if refused:
                    self.assert_code("calls_import_invalid", imports.parse_transcript, raw, format="vtt")
                else:
                    self.assertIsNone(imports.parse_transcript(raw, format="vtt")["units"][0]["cue_id"])

    def test_invalid_inputs_and_units_are_fixed_private_errors(self):
        for raw in (b"", b" \n\t", b"\xef\xbb\xbf", b"\xff", b"a\x00", b"a\x1b", "a\u0085b".encode()):
            with self.subTest(raw=raw):
                error = self.assert_code("calls_import_invalid", imports.parse_transcript, raw, format="txt")
                self.assertEqual(str(error), "calls_import_invalid")
                self.assertEqual(error.exit_code, 2)
        self.assert_code("calls_import_too_large", imports.parse_transcript,
                         b"x" * (imports.MAX_RAW_BYTES + 1), format="txt")
        self.assert_code("calls_import_too_large", imports.parse_transcript,
                         b"x\n" * (imports.MAX_UNITS + 1), format="txt")
        parsed = imports.parse_transcript(b"one\ntwo", format="txt")
        for bounds in ((0, 1), (2, 1), (1, 3), (True, 1), (1.0, 2), [1, 2]):
            self.assert_code("calls_import_invalid", imports.select_excerpt, parsed, requested_range=bounds)

    def test_vtt_header_metadata_is_skipped_and_other_header_text_refuses(self):
        cue = "\n\n00:00.000 --> 00:01.000\nDana: hello\n"
        for header in ("WEBVTT\nKind: captions\nLanguage: en", "WEBVTT - meeting\nLanguage: pt-BR",
                       "WEBVTT\nX-TIMESTAMP-MAP=MPEGTS:900000,LOCAL:00:00:00.000"):
            with self.subTest(header=header):
                parsed = imports.parse_transcript((header + cue).encode(), format="vtt")
                self.assertEqual([unit["text"] for unit in parsed["units"]], ["Dana: hello"])
        # A line that could be speech is never dropped as a header.
        for header in ("WEBVTT\nDana: this was said", "WEBVTT\nKind: captions\nspoken text",
                       "WEBVTT\nKind: We must wait for procurement.", "WEBVTT\nLanguage: we never agreed",
                       "WEBVTT\nX-TIMESTAMP-MAP=the budget is frozen"):
            with self.subTest(header=header):
                self.assert_code("calls_import_invalid", imports.parse_transcript, (header + cue).encode(),
                                 format="vtt")

    def test_srt_files_and_the_cue_shapes_real_exports_use_are_read(self):
        srt = ("1\n00:00:01,000 --> 00:00:04,000\nDana: hello\n\n"
               "2\n00:00:04,500 --> 00:00:06,000\nline two\nsecond line\n \n3\n01:00:00,000 --> 01:00:00,000\nlast\n")
        units = imports.parse_transcript(srt.encode(), format="srt")["units"]
        self.assertEqual([(unit["cue_id"], unit["text"], unit["start_ms"], unit["end_ms"]) for unit in units],
                         [("1", "Dana: hello", 1000, 4000), ("2", "line two\nsecond line", 4500, 6000),
                          ("3", "last", 3_600_000, 3_600_000)])
        excerpt = imports.select_excerpt(imports.parse_transcript(srt.encode(), format="srt"))
        self.assertEqual((excerpt["unit_kind"], excerpt["total_units"]), ("cues", 3))
        # Auto-captions repeat a start time, end a cue where it starts, and are not always in order.
        vtt = ("WEBVTT\n\n00:00:02.000 --> 00:00:02.000\nzero length\n\t\n"
               "00:00:01.000 --> 00:00:03.000\nearlier start\n \n00:00:01.000 --> 00:00:02.500\nsame start\n")
        units = imports.parse_transcript(vtt.encode(), format="vtt")["units"]
        self.assertEqual([unit["text"] for unit in units], ["zero length", "earlier start", "same start"])
        # Webex writes the speaker as the cue identifier, so identifiers repeat.
        webex = "WEBVTT\n\nDana Crest\n00:01.000 --> 00:02.000\none\n\nDana Crest\n00:02.000 --> 00:03.000\ntwo\n"
        units = imports.parse_transcript(webex.encode(), format="vtt")["units"]
        self.assertEqual([(unit["cue_id"], unit["text"]) for unit in units], [("Dana Crest", "one"), ("Dana Crest", "two")])
        # The speaker stays in the quote. A number or a single token is only an identifier and does not.
        parsed = imports.parse_transcript(webex.encode(), format="vtt")
        self.assertEqual(imports.select_excerpt(parsed)["body"], "Dana Crest\none\nDana Crest\ntwo")
        numbered = imports.parse_transcript(b"WEBVTT\n\n1\n00:01.000 --> 00:02.000\none\n\nabc-def\n00:02.000 --> 00:03.000\ntwo\n",
                                            format="vtt")
        self.assertEqual(imports.select_excerpt(numbered)["body"], "one\ntwo")
        # A line of spaces inside a cue's text stays text, because no cue starts after it.
        spaced = "WEBVTT\n\n00:01.000 --> 00:02.000\none\n \nstill one\n \n00:02.000 --> 00:03.000\ntwo\n"
        units = imports.parse_transcript(spaced.encode(), format="vtt")["units"]
        self.assertEqual([unit["text"] for unit in units], ["one\n \nstill one", "two"])
        # An SRT file still needs its timing lines, and no cue may end before it starts. Only a number may sit
        # above a timing line, nothing may follow one, and a comment block is not skipped as WebVTT's is.
        for bad in ("1\nDana: hello\n", "1\n00:00:04,000 --> 00:00:01,000\ntext\n",
                    "WEBVTT\n\n1\n00:00:01,000 --> 00:00:02,000\nx\n", "Dana\n00:00:01,000 --> 00:00:02,000\nx\n",
                    "1\n00:00:01,000 --> 00:00:02,000 we agreed on 40 seats\nx\n",
                    "NOTE this matters\nand so does this\n\n1\n00:00:01,000 --> 00:00:02,000\nx\n",
                    "1\n00:00:01,000 --> 00:00:02,000\nx\n2\n00:00:02,000 --> 00:00:03,000\nmerged\n"):
            with self.subTest(bad=bad):
                self.assert_code("calls_import_invalid", imports.parse_transcript, bad.encode(), format="srt")
        arrow = imports.parse_transcript(b"1\n00:00:01,000 --> 00:00:02,000\nplan A --> plan B\n", format="srt")
        self.assertEqual(arrow["units"][0]["text"], "plan A --> plan B")

    def test_lines_of_spaces_never_cost_a_word(self):
        cue = "00:01.000 --> 00:02.000\n"
        # Text after a run of spaces stays in its cue, whatever it starts with, and a comment is only a block of its own.
        for text, units in (
                ("one\n \nNOTE this matters\nmore", ["one\n \nNOTE this matters\nmore"]),
                ("one\n \n\t\nstill one", ["one\n \n\t\nstill one"]),
                ("one\n \nNOTEWORTHY: forty seats", ["one\n \nNOTEWORTHY: forty seats"]),
                ("one\n \n \n00:02.000 --> 00:03.000\ntwo", ["one", "two"]),
                ("one\n \nid\n00:02.000 --> 00:03.000\ntwo", ["one", "two"]),
                ("one\n\nNOTE a comment\n\n00:02.000 --> 00:03.000\ntwo", ["one", "two"])):
            with self.subTest(text=text):
                parsed = imports.parse_transcript(("WEBVTT\n\n" + cue + text + "\n").encode(), format="vtt")
                self.assertEqual([unit["text"] for unit in parsed["units"]], units)
        # What follows an end time is cue settings. Anything else there could be speech and refuses.
        settings = "WEBVTT\n\n00:01.000 --> 00:02.000 align:start position:10%\none\n"
        self.assertEqual(len(imports.parse_transcript(settings.encode(), format="vtt")["units"]), 1)
        self.assert_code("calls_import_invalid", imports.parse_transcript,
                         b"WEBVTT\n\n00:01.000 --> 00:02.000 we agreed on 40 seats\none\n", format="vtt")
        # A long identifier is not a speaker's name and stays out of the quote.
        long_id = "WEBVTT\n\n" + "word " * 60 + "\n" + cue + "one\n"
        self.assertEqual(imports.select_excerpt(imports.parse_transcript(long_id.encode(), format="vtt"))["body"], "one")

    def test_malformed_vtt_cannot_silently_drop_spoken_content(self):
        self.assert_code("calls_import_invalid", imports.parse_transcript,
                         (FIXTURES / "invalid-timing.vtt").read_bytes(), format="vtt")
        bad = (
            "BAD\n\n00:00.000 --> 00:01.000\ntext",
            "WEBVTT\n00:00.000 --> 00:01.000\ntext",
            "WEBVTT\n\n00:02.000 --> 00:01.000\ntext",
            "WEBVTT\n\n00:60.000 --> 01:01.000\ntext",
            "WEBVTT\n\n00:00.00 --> 00:01.000\ntext",
            "WEBVTT\n\n00:00.000 --> 00:01.000\none\n00:01.000 --> 00:02.000\ntwo",
            "WEBVTT\n\nNOTE hidden\n00:00.000 --> 00:01.000\ntext",
            "WEBVTT\n\nunknown block\nspoken text",
            "WEBVTT\n\n00:00.000 --> 00:01.000\ntext\n\nSTYLE\nbody{}",
        )
        for value in bad:
            with self.subTest(value=value):
                self.assert_code("calls_import_invalid", imports.parse_transcript, value.encode(), format="vtt")


class StoreTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.path = self.directory / "imports.sqlite3"
        imports.initialize_import_store(self.path, crm_binding=BINDING)

    def publish(self, raw=b"Invented renewal discussion.\nNext steps remain proposed.", *, account="acc-007",
                span=None, called_at=None, now=NOW, receipt="invented-receipt-one", reassign=False,
                binding=BINDING, number=None, format="txt"):
        parsed = imports.parse_transcript(raw, format=format)
        excerpt = imports.select_excerpt(parsed, requested_range=span)
        return imports.store_import(self.path, parsed, excerpt, association=association(
            account, binding=binding, number=number, receipt=receipt),
            called_at=called_at, imported_at=now, reassign=reassign)

    def read(self, account="acc-007", binding=BINDING):
        return imports.ImportedCallsReader(self.path, crm_binding=binding).read_call_notes(account)[0]

    def assert_code(self, code, function, *args, **kwargs):
        with self.assertRaises(imports.CallsImportError) as caught:
            function(*args, **kwargs)
        self.assertEqual(caught.exception.code, code)

    def dump(self):
        with closing(sqlite3.connect(self.path)) as connection, connection:
            return list(connection.iterdump())

    def test_empty_reader_has_no_fallback_and_constructor_does_no_io(self):
        missing = self.directory / "absent" / "imports.sqlite3"
        imports.ImportedCallsReader(missing, crm_binding=BINDING)
        self.assertFalse(missing.parent.exists())
        self.assertEqual(imports.ImportedCallsReader(self.path, crm_binding=BINDING).read_call_notes("acc-007"), [])
        self.assertFalse((self.directory / "calls.sqlite3").exists())
        self.assertEqual(stat.S_IMODE(self.path.stat().st_mode), 0o600)
        imports.validate_import_store(self.path, crm_binding=BINDING)

    def test_actual_file_idempotency_restart_and_immutable_selection_revisions(self):
        raw = (FIXTURES / "renewal.vtt").read_bytes()
        first = self.publish(raw, format="vtt", span=(1, 1))
        saved = self.read()
        imports.initialize_import_store(self.path, crm_binding=BINDING)
        retry = self.publish(raw, format="vtt", span=(1, 1), now=LATER, receipt="invented-receipt-two")
        self.assertEqual(retry["import_id"], first["import_id"])
        self.assertFalse(retry["created"])
        self.assertEqual(retry["imported_at"], NOW)
        self.assertEqual(retry["resolution_receipt_id"], "invented-receipt-two")
        self.assertEqual(self.read(), saved)
        changed = self.publish(raw, format="vtt", span=(3, 3), now=LATER)
        self.assertNotEqual(changed["import_id"], first["import_id"])
        self.assertEqual(imports.inspect_import(self.path, first["import_id"], crm_binding=BINDING)["body"], saved["body"])
        self.assertNotEqual(self.publish(raw, format="vtt", span=(3, 3), called_at=NOW)["import_id"], changed["import_id"])
        with closing(sqlite3.connect(self.path)) as connection, connection:
            self.assertEqual(connection.execute("SELECT count(*) FROM contents").fetchone()[0], 1)
            self.assertEqual(connection.execute("SELECT count(*) FROM revisions").fetchone()[0], 3)
        imports.validate_imported_record(saved, account_id="acc-007", crm_binding=BINDING)

    def test_reassign_a_to_b_to_a_preserves_old_evidence_and_needs_explicit_flag(self):
        first = self.publish()
        original = self.read()
        before = self.dump()
        self.assert_code("calls_reassign_required", self.publish, account="acc-008")
        self.assertEqual(before, self.dump())
        second = self.publish(account="acc-008", reassign=True, now=LATER)
        self.assertTrue(second["reassigned"])
        self.assertEqual(imports.ImportedCallsReader(self.path, crm_binding=BINDING).read_call_notes("acc-007"), [])
        self.assertEqual(self.read("acc-008")["manifest"]["association_generation"], 2)
        self.assert_code("calls_reassign_required", self.publish)
        third = self.publish(reassign=True, now=LATER)
        self.assertNotEqual(first["import_id"], third["import_id"])
        self.assertEqual(self.read()["manifest"]["association_generation"], 3)
        self.assertEqual(imports.ImportedCallsReader(self.path, crm_binding=BINDING).read_call_notes("acc-008"), [])
        self.assertEqual(imports.validate_imported_record(original, account_id="acc-007", crm_binding=BINDING), original)

    def test_reassignment_does_not_clear_a_newer_other_content_selection(self):
        self.publish(b"First call.")
        newer = self.publish(b"Different later call.")
        self.publish(b"First call.", account="acc-008", reassign=True)
        self.assertEqual(self.read()["id"], newer["import_id"])
        self.assertEqual(self.read("acc-008")["body"], "First call.")

    def test_same_number_repointing_cannot_hide_behind_changed_content(self):
        self.path.unlink()
        imports.initialize_import_store(self.path, crm_binding=SF_BINDING)
        first, second = "acc-sf-001000000000001AAA", "acc-sf-001000000000002AAA"
        self.publish(b"First.", account=first, binding=SF_BINDING, number="INVENTED-1")
        before = self.dump()
        self.assert_code("calls_association_changed", self.publish, b"Changed bytes.", account=second,
                         binding=SF_BINDING, number="INVENTED-1")
        self.assertEqual(before, self.dump())
        reassigned = self.publish(b"Changed bytes.", account=second, binding=SF_BINDING,
                                  number="INVENTED-1", reassign=True)
        self.assertTrue(reassigned["reassigned"])
        self.assertEqual(self.read(second, SF_BINDING)["account_id"], second)

    def test_private_selector_changes_do_not_create_a_public_manifest_or_retry_key(self):
        self.path.unlink()
        imports.initialize_import_store(self.path, crm_binding=SF_BINDING)
        account = "acc-sf-001000000000001AAA"
        first_number, next_number = "PRIVATE-INVENTED-1", "PRIVATE-INVENTED-2"
        first = self.publish(account=account, binding=SF_BINDING, number=first_number)
        initial_record = self.read(account, SF_BINDING)
        initial_record["record_ref"] = "calls/call_notes/" + initial_record["id"]
        retry = self.publish(account=account, binding=SF_BINDING, number=next_number,
                             now=LATER, receipt="invented-receipt-two")
        self.assertEqual(retry["import_id"], first["import_id"])
        self.assertFalse(retry["created"])
        self.assertEqual(retry["imported_at"], NOW)
        self.assertEqual(retry["resolution_receipt_id"], "invented-receipt-two")
        manifest = self.read(account, SF_BINDING)["manifest"]
        self.assertEqual(set(manifest["association"]),
                         {"crm_binding", "account_id", "method", "resolution_receipt_id"})
        for number in (first_number, next_number):
            self.assertNotIn(number, json.dumps(manifest))
            self.assertNotIn(hashlib.sha256(number.encode()).hexdigest(), json.dumps(manifest))
        with closing(sqlite3.connect(self.path)) as connection:
            self.assertEqual(connection.execute("SELECT * FROM number_associations ORDER BY account_number").fetchall(),
                             [(first_number, account), (next_number, account)])
            self.assertEqual(connection.execute("SELECT count(*) FROM revisions").fetchone()[0], 1)
        # Public snapshots must reject even a self-consistent attempt to add the private selector.
        changed = deepcopy(initial_record)
        changed["manifest"]["association"]["account_number"] = first_number
        changed["id"] = imports._manifest_id(changed["manifest"])
        changed["record_ref"] = "calls/call_notes/" + changed["id"]
        self.assert_code("calls_import_corrupt", imports.validate_imported_record, changed,
                         account_id=account, crm_binding=SF_BINDING)

    def test_changed_stored_raw_fails_reads_but_saved_snapshot_check_is_independent(self):
        item = self.publish()
        saved = self.read()
        with closing(sqlite3.connect(self.path)) as connection, connection:
            connection.execute("UPDATE contents SET raw = ?", (b"Changed private source.",))
        self.assert_code("calls_import_corrupt", self.read)
        self.assert_code("calls_import_corrupt", imports.inspect_import, self.path, item["import_id"],
                         crm_binding=BINDING, full=True)
        self.path.unlink()
        self.assertEqual(imports.validate_imported_record(saved, account_id="acc-007", crm_binding=BINDING), saved)

    def test_snapshot_shape_and_manifest_tampering_fail_without_original_bytes(self):
        self.publish((FIXTURES / "renewal.vtt").read_bytes(), format="vtt")
        original = self.read()
        original["record_ref"] = "calls/call_notes/" + original["id"]
        self.assertEqual(imports.validate_imported_record(original, account_id="acc-007", crm_binding=BINDING), original)
        mutations = (
            lambda r: r.update(body="Forged text"),
            lambda r: r["manifest"]["units"][0].update(text="Forged selected unit"),
            lambda r: r["manifest"]["units"][0].update(start_ms=1),
            lambda r: r["manifest"]["cited_range"].update(last=3),
            lambda r: r["manifest"].update(content_sha256="f" * 64),
            lambda r: r["manifest"]["association"].update(resolution_receipt_id="forged-receipt"),
            lambda r: r["manifest"]["association"].update(account_id="acc-008"),
            lambda r: r.update(record_ref="calls/call_notes/note-wrong"),
            lambda r: r.update(original_text="forbidden original"),
            lambda r: r.update(source_bundle={"raw": "Zm9yYmlkZGVu"}),
            lambda r: r["manifest"].update(parser_version=True),
            lambda r: r["manifest"]["units"][0].update(index=True),
        )
        for mutate in mutations:
            changed = deepcopy(original)
            mutate(changed)
            self.assert_code("calls_import_corrupt", imports.validate_imported_record, changed,
                             account_id="acc-007", crm_binding=BINDING)
        other_binding = {**BINDING, "identity_sha256": "c" * 64}
        self.assert_code("calls_import_corrupt", imports.validate_imported_record, original,
                         account_id="acc-007", crm_binding=other_binding)
        # A record that says its import declared speaker names, remade whole so its ID is its own: its cues
        # do not open with a name, so it is refused. The statement is true or absent, never false.
        for stated in (True, False):
            manifest = {**deepcopy(original["manifest"]), "speaker_names": stated}
            remade = imports._make_record(manifest)
            self.assert_code("calls_import_corrupt", imports.validate_imported_record,
                             {**remade, "record_ref": "calls/call_notes/" + remade["id"]},
                             account_id="acc-007", crm_binding=BINDING)

    def test_isolated_retry_key_or_active_head_corruption_cannot_select_other_evidence(self):
        first = self.publish(b"First.")
        second = self.publish(b"Second.")
        with closing(sqlite3.connect(self.path)) as connection, connection:
            first_key = connection.execute("SELECT request_key FROM revisions WHERE import_id = ?",
                                           (first["import_id"],)).fetchone()[0]
            connection.execute("UPDATE revisions SET request_key = ? WHERE import_id = ?",
                               ("0" * 64, first["import_id"]))
            connection.execute("UPDATE revisions SET request_key = ? WHERE import_id = ?",
                               (first_key, second["import_id"]))
        self.assert_code("calls_import_corrupt", self.publish, b"First.")
        self.assert_code("calls_import_corrupt", self.read)

    def test_manifest_cannot_smuggle_unselected_or_unknown_fields(self):
        self.publish()
        saved = self.read()
        changed = deepcopy(saved)
        changed["manifest"]["original_text"] = "Unselected private text."
        self.assert_code("calls_import_corrupt", imports.validate_imported_record, changed,
                         account_id="acc-007", crm_binding=BINDING)
        changed = deepcopy(saved)
        changed["manifest"]["units"][0]["speaker"] = "Guessed speaker"
        self.assert_code("calls_import_corrupt", imports.validate_imported_record, changed,
                         account_id="acc-007", crm_binding=BINDING)

    def test_schema_validation_does_not_query_raw_source_blobs(self):
        self.publish()
        original_connect = sqlite3.connect
        queries = []

        def connect(*args, **kwargs):
            connection = original_connect(*args, **kwargs)
            connection.set_trace_callback(queries.append)
            return connection

        with patch.object(imports.sqlite3, "connect", side_effect=connect):
            imports.validate_import_store(self.path, crm_binding=BINDING)
        self.assertFalse(any("SELECT raw" in query or "length(raw)" in query for query in queries))

    def test_unselected_sentinel_appears_only_in_explicit_full_inspection(self):
        sentinel = "UNSELECTED_INVENTED_SENTINEL"
        raw = "\n".join(["Selected."] * 30 + [sentinel] + ["Unselected."] * 30).encode()
        item = self.publish(raw)
        record = self.read()
        normal = imports.inspect_import(self.path, item["import_id"], crm_binding=BINDING)
        listing = imports.list_imports(self.path, crm_binding=BINDING)
        for result in (item, record, normal, listing):
            self.assertNotIn(sentinel, json.dumps(result))
            self.assertNotIn("original_text", result)
        full = imports.inspect_import(self.path, item["import_id"], crm_binding=BINDING, full=True)
        self.assertEqual(full["original_text"].encode(), raw)
        self.assertIn(sentinel, full["original_text"])
        self.assertEqual(len(full["units"]), 61)

    def test_maximum_original_does_not_inflate_returned_record(self):
        prefix = b"Selected first line.\n" * 22
        raw = prefix + b"x" * (imports.MAX_RAW_BYTES - len(prefix))
        self.publish(raw)
        record = self.read()
        self.assertEqual(record["manifest"]["raw_byte_count"], imports.MAX_RAW_BYTES)
        self.assertLess(len(json.dumps(record, ensure_ascii=True).encode()), 10_000)
        self.assertEqual(len(record["manifest"]["units"]), 21)

    def test_bound_listing_filter_and_historical_active_metadata(self):
        a = self.publish(b"First.")
        b = self.publish(b"Second.")
        self.publish(b"Other account.", account="acc-008")
        first = imports.list_imports(self.path, crm_binding=BINDING, account_id="acc-007", limit=1)
        self.assertEqual(len(first["imports"]), 1)
        self.assertIsNotNone(first["next_after"])
        second = imports.list_imports(self.path, crm_binding=BINDING, account_id="acc-007", limit=1,
                                     after=first["next_after"])
        self.assertIsNone(second["next_after"])
        rows = {row["import_id"]: row for row in first["imports"] + second["imports"]}
        self.assertFalse(rows[a["import_id"]]["active"])
        self.assertTrue(rows[b["import_id"]]["active"])
        self.assert_code("calls_import_invalid", imports.list_imports, self.path, crm_binding=BINDING, limit=101)

    def test_transaction_failure_rolls_back_content_revision_mapping_and_pointer(self):
        self.publish()
        before = self.dump()
        original_connect = sqlite3.connect
        directory = self.directory

        class FailingConnection(sqlite3.Connection):
            def execute(self, statement, parameters=()):
                if statement.startswith("INSERT INTO active_imports"):
                    journal = directory / "imports.sqlite3-journal"
                    if journal.exists():
                        assert stat.S_IMODE(journal.stat().st_mode) == 0o600
                    raise sqlite3.OperationalError("PRIVATE CONTENT MUST NEVER BE PRINTED")
                return super().execute(statement, parameters)

        def connect(*args, **kwargs):
            return original_connect(*args, **kwargs, factory=FailingConnection)

        with patch.object(imports.sqlite3, "connect", side_effect=connect):
            self.assert_code("calls_import_corrupt", self.publish, account="acc-008", reassign=True)
        self.assertEqual(before, self.dump())
        self.assertEqual(self.read()["account_id"], "acc-007")

    def test_private_store_binding_schema_and_aliases_fail_without_repair(self):
        before = self.path.read_bytes()
        self.assert_code("calls_source_mismatch", imports.initialize_import_store, self.path,
                         crm_binding={**BINDING, "identity_sha256": "f" * 64})
        self.assertEqual(before, self.path.read_bytes())
        self.path.chmod(0o644)
        self.assert_code("calls_source_mismatch", imports.validate_import_store, self.path, crm_binding=BINDING)
        self.path.chmod(0o600)
        link = self.directory / "link.sqlite3"
        link.symlink_to(self.path)
        self.assert_code("calls_source_mismatch", imports.initialize_import_store, link, crm_binding=BINDING)
        link.unlink()
        os.link(self.path, link)
        self.assert_code("calls_source_mismatch", imports.validate_import_store, self.path, crm_binding=BINDING)
        link.unlink()
        with closing(sqlite3.connect(self.path)) as connection, connection:
            connection.execute("CREATE TABLE unknown (value TEXT)")
        self.assert_code("calls_import_corrupt", imports.validate_import_store, self.path, crm_binding=BINDING)

    def test_missing_readonly_store_creates_nothing_and_fixed_error_catalog_is_public(self):
        missing = self.directory / "does-not-exist.sqlite3"
        self.assert_code("calls_import_io", imports.list_imports, missing, crm_binding=BINDING)
        self.assertFalse(missing.exists())
        for code in imports.ERROR_CODES:
            error = imports.CallsImportError(code)
            self.assertIsInstance(error, RuntimeError)
            self.assertNotIsInstance(error, ValueError)
            self.assertIn(error.category, {"usage", "refusal", "check", "upstream", "io"})
            self.assertEqual(str(error), code)

    def test_forged_parser_result_and_invalid_metadata_cannot_publish(self):
        parsed = imports.parse_transcript(b"Original.", format="txt")
        excerpt = imports.select_excerpt(parsed)
        before = self.dump()
        changed = deepcopy(parsed)
        changed["units"][0]["text"] = "Forged."
        self.assert_code("calls_import_invalid", imports.store_import, self.path, changed, excerpt,
                         association=association(), called_at=None, imported_at=NOW)
        for timestamp in ("2026-11-18", "2026-11-18T12:00:00", "2026-11-18T12:00:00+01:00", True):
            self.assert_code("calls_import_invalid", imports.store_import, self.path, parsed, excerpt,
                             association=association(), called_at=timestamp, imported_at=NOW)
        self.assertEqual(before, self.dump())


if __name__ == "__main__":
    unittest.main()
