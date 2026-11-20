"""inspect reads what a folder holds: an account, a saved card, numbers on it, or one receipt. It writes nothing."""

from contextlib import closing, redirect_stderr, redirect_stdout
import hashlib
import io
import json
from pathlib import Path
import shutil
import sqlite3
import tempfile
import unittest

from gtm_connector import profile
from gtm_connector.cli import main


CUSTOMER = ('5  calls  2027-01-12T15:00:00Z  customer  "Marian Holt"  shown  "Our general counsel will not sign until '
            'the zero-retention and client-consent rider for AI processing is approved."')
MISSING_RECEIPT = "00000000-0000-4000-8000-000000000000"


class InspectTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = self.root / "state"
        profile.save(self.state, profile.DEMO)

    def run_cli(self, *arguments, as_json=True, state=None):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main([*arguments, "--state-dir", str(state or self.state), *(["--json"] if as_json else [])])
        return code, out.getvalue(), err.getvalue()

    def ok(self, *arguments, **options):
        code, out, err = self.run_cli(*arguments, **options)
        self.assertEqual(code, 0, err)
        return json.loads(out)

    def text(self, *arguments):
        code, out, err = self.run_cli(*arguments, as_json=False)
        self.assertEqual(code, 0, err)
        return out

    def refused(self, *arguments, code=2, **options):
        actual, out, err = self.run_cli(*arguments, **options)
        self.assertEqual((actual, out), (code, ""), err)
        return json.loads(err.splitlines()[-1])["error"]

    def snapshot(self):
        return {path.relative_to(self.state): path.read_bytes() for path in self.state.rglob("*") if path.is_file()}

    def reseal(self, brief_id, change):
        """Rewrite a saved card and its seal together, as only the owner of the state folder can."""
        path = self.state / "briefs" / (brief_id + ".json")
        artifact = json.loads(path.read_text())
        change(artifact)
        payload = json.dumps(artifact).encode()
        path.write_bytes(payload)
        with closing(sqlite3.connect(self.state / "audit.sqlite3")) as connection, connection:
            connection.execute("UPDATE audit_log SET rows = ? WHERE audit_id = ?", (json.dumps(
                [f"briefs/{brief_id}/{hashlib.sha256(payload).hexdigest()}"]), artifact["sealed_by"]))

    def test_a_card_lists_every_unit_on_one_line_each_in_number_order(self):
        brief = self.ok("brief", "acc-003")
        lines = self.text("inspect", brief["brief_id"]).splitlines()
        self.assertEqual(lines[:3], ["1  record  accounts  acc-003", "2  record  contacts  con-003",
                                     "3  record  opportunities  opp-003"])
        self.assertEqual(lines[4], CUSTOMER)
        self.assertEqual(lines[6], '7  slack  2027-01-12T16:00:00Z  seller  "Avery Linden"  shown  "Heads up '
                                   '[mention channel]: their general counsel will not sign until the zero-retention '
                                   'and client-consent rider is approved."')
        self.assertEqual(len(lines), len(brief["units"]))
        # The JSON form is the units as sealed.
        self.assertEqual(self.ok("inspect", brief["brief_id"]),
                         {"brief_id": brief["brief_id"], "account_id": "acc-003", "units": brief["units"]})
        # One role keeps only that role's sentences.
        self.assertEqual([unit["n"] for unit in self.ok("inspect", brief["brief_id"], "--role", "customer")["units"]],
                         [5, 6])
        self.assertEqual(self.text("inspect", brief["brief_id"], "--role", "customer").splitlines()[0], CUSTOMER)
        self.assertEqual(self.ok("inspect", brief["brief_id"], "--role", "unknown")["units"], [])

    def test_numbers_resolve_in_the_order_given_with_the_next_sentence_by_the_same_speaker(self):
        brief = self.ok("brief", "acc-003")
        traced = self.ok("inspect", brief["brief_id"], "5", "1", "7", "6")
        self.assertEqual([(row["n"], row["kind"], row["next_by_speaker"]) for row in traced],
                         [(5, "sentence", 6), (1, "record", None), (7, "sentence", 8), (6, "sentence", None)])
        self.assertEqual(traced[1]["record_ref"], "crm/accounts/acc-003")
        five, six = self.text("inspect", brief["brief_id"], "5", "6").split("\n\n")
        self.assertIn('Speaker\t"Marian Holt"\nNext by this speaker\t6\n', five)
        self.assertIn("Next by this speaker\tnone\n", six + "\n")
        # Every number must be on the card, or nothing is printed.
        error = self.refused("inspect", brief["brief_id"], "5", "10")
        self.assertIn("This card has numbers 1 to 9.", error["message"])

    def test_a_card_sealed_before_shown_span_and_contact_existed_says_not_recorded(self):
        brief = self.ok("brief", "acc-003")

        def older(artifact):
            for unit in artifact["units"]:
                for key in ("shown", "span", "contact"):
                    unit.pop(key, None)

        self.reseal(brief["brief_id"], older)
        listed = self.text("inspect", brief["brief_id"]).splitlines()
        self.assertEqual(listed[4], CUSTOMER.replace("  shown  ", "  not recorded  "))
        traced = self.text("inspect", brief["brief_id"], "5")
        self.assertIn("On the card\tnot recorded\n", traced)
        self.assertNotIn("Cues or lines", traced)
        self.assertNotIn("shown", self.ok("inspect", brief["brief_id"])["units"][4])

    def test_a_transcript_sentence_names_the_cues_or_lines_it_lies_in(self):
        self.ok("connect", "calls")
        export = self.root / "call.vtt"
        export.write_text("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Marian Holt>We will sign\n\n"
                          "00:00:02.500 --> 00:00:04.000\n<v Marian Holt>on Friday.\n")
        self.ok("debug", "calls", "import", str(export), "--account", "acc-003",
                "--called-at", "2027-01-14T15:00:00Z")
        lines = self.text("inspect", self.ok("brief", "acc-003")["brief_id"]).splitlines()
        self.assertIn('4  calls  2027-01-14T15:00:00Z cues 1-2  customer  "Marian Holt"  shown  '
                      '"We will sign on Friday."', lines)
        export = self.root / "call.txt"
        export.write_text("Dana Crest: We will sign.\n")
        self.ok("debug", "calls", "import", str(export), "--account", "acc-007", "--speaker-names")
        lines = self.text("inspect", self.ok("brief", "acc-007")["brief_id"]).splitlines()
        self.assertIn('4  calls  time Missing lines 1-1  customer  "Dana Crest"  shown  "We will sign."', lines)

    def test_a_changed_or_copied_card_is_refused_the_way_trace_refuses_it(self):
        brief = self.ok("brief", "acc-003")
        path = self.state / "briefs" / (brief["brief_id"] + ".json")
        sealed = path.read_bytes()
        path.write_bytes(sealed.replace(b"450,000", b"1"))
        for arguments in ((brief["brief_id"],), (brief["brief_id"], "1"), (brief["brief_id"], "--role", "seller")):
            with self.subTest(arguments=arguments):
                self.assertIn("not the card that was sealed", self.refused("inspect", *arguments)["message"])
        path.write_bytes(sealed)
        other = self.root / "other"
        profile.save(other, profile.DEMO)
        (other / "briefs").mkdir()
        shutil.copy(path, other / "briefs")
        self.assertIn("seal receipt cannot be read",
                      self.refused("inspect", brief["brief_id"], state=other)["message"])
        # A unit that does not resolve to its record and receipt is refused even when the seal matches.
        self.reseal(brief["brief_id"], lambda artifact: artifact["units"][6].update(raw_sha256="0" * 64))
        self.assertIn("Number 7 does not resolve", self.refused("inspect", brief["brief_id"])["message"])

    def test_a_receipt_shows_as_audit_shows_it_with_its_arguments_and_returned_rows(self):
        brief = self.ok("brief", "acc-003")
        receipt = self.ok("audit", "--brief", brief["brief_id"])[0]
        self.assertEqual(self.ok("inspect", receipt["audit_id"]), receipt)
        row, arguments, rows = self.text("inspect", receipt["audit_id"]).split("\n\n")
        self.assertEqual(row, self.text("audit", "--brief", brief["brief_id"]).splitlines()[0])
        self.assertEqual(arguments, 'Arguments\n{"account_id": "acc-003"}')
        self.assertEqual(rows, "Returned rows\ncrm/accounts/acc-003\ncrm/contacts/con-003\n")
        # The demo's refused read returned nothing.
        demo = self.root / "demo"
        self.ok("demo", state=demo)
        [refused] = self.ok("audit", "--status", "error", state=demo)
        code, out, err = self.run_cli("inspect", refused["audit_id"], as_json=False, state=demo)
        self.assertEqual(code, 0, err)
        self.assertTrue(out.endswith("\n\nReturned rows\nnone\n"), out)
        self.assertEqual(self.refused("inspect", MISSING_RECEIPT)["message"], "Audit receipt not found.")
        self.assertIn("audit database not found", self.refused("inspect", MISSING_RECEIPT, state=self.root)["message"])

    def test_an_account_names_its_newest_card_by_when_it_was_sealed(self):
        account = self.ok("inspect", "acc-003")
        self.assertEqual((account["id"], account["name"], account["newest_brief_id"]),
                         ("acc-003", "Thornfield Wren LLP", None))
        self.assertNotIn("Newest card", self.text("inspect", "acc-003"))
        first = self.ok("brief", "acc-003")["brief_id"]
        other = self.ok("brief", "acc-001")["brief_id"]
        newest = self.ok("brief", "acc-003")["brief_id"]
        self.assertEqual(self.ok("inspect", "acc-003")["newest_brief_id"], newest)
        self.assertEqual(self.ok("inspect", "acc-001")["newest_brief_id"], other)
        self.assertEqual(self.text("inspect", "acc-003").splitlines()[-1],
                         f"Newest card: {newest}  (gtm-connector inspect {newest} --state-dir {self.state})")
        # A card that fails the checks inspect makes is not named, and the newest one that passes is.
        path = self.state / "briefs" / (newest + ".json")
        path.write_bytes(path.read_bytes()[:100])
        self.assertEqual(self.ok("inspect", "acc-003")["newest_brief_id"], first)
        self.assertEqual(self.ok("inspect", first)["brief_id"], first)
        path.unlink()
        self.assertEqual(self.ok("inspect", "acc-003")["newest_brief_id"], first)
        # The account itself is what the folder's CRM returns.
        self.assertEqual(self.refused("inspect", "acc-999")["message"], "account not found: acc-999")

    def test_a_receipts_arguments_are_shown_inert_as_source_text_on_a_card_is(self):
        from gtm_connector.client import LocalClient
        from gtm_connector.runtime import Runtime
        with Runtime(self.state) as runtime, LocalClient(runtime.config) as client:
            token = client.authorize(["crm:read"])
            client.call(token, "search_accounts", {"query": "<!here> [5] https://invented.example/x"})
        [receipt] = [row for row in self.ok("audit") if row["tool"] == "search_accounts"]
        shown = self.ok("inspect", receipt["audit_id"])
        self.assertEqual(shown["arguments"], {"query": "[mention here] (5) [link invented.example]"})
        self.assertIn('Arguments\n{"query": "[mention here] (5) [link invented.example]"}',
                      self.text("inspect", receipt["audit_id"]))

    def test_the_next_sentence_by_a_speaker_is_a_whole_one_and_never_by_a_label_that_prints_alike(self):
        self.ok("connect", "calls")
        export = self.root / "call.vtt"
        export.write_text("WEBVTT\n\n00:00:01.000 --> 00:00:02.000\n<v Dana Crest>We agreed.\n\n"
                          "00:00:30.000 --> 00:00:31.000\n<v Dana Crest>and then\n\n"
                          "00:00:40.000 --> 00:00:41.000\n<v Dana Crest>We objected.\n\n"
                          "00:00:50.000 --> 00:00:51.000\n<v Sam [5]>We signed.\n\n"
                          "00:00:52.000 --> 00:00:53.000\n<v Sam (5)>We refused.\n\n"
                          "00:01:10.000 --> 00:01:11.000\n<v Dana Crest>We left.\n")
        self.ok("debug", "calls", "import", str(export), "--account", "acc-007", "--cues", "1:6")
        brief = self.ok("brief", "acc-007")
        said = {unit["text"]: unit for unit in brief["units"] if unit["kind"] == "sentence"}
        # The words after a cut sentence may be its other half, so they are cut too.
        self.assertEqual((said["and then"]["partial"], said["We objected."]["partial"]), (True, True))
        listing = self.text("inspect", brief["brief_id"]).splitlines()
        self.assertIn("cut off", listing[said["and then"]["n"] - 1])
        traced = self.ok("inspect", brief["brief_id"], str(said["We agreed."]["n"]), str(said["We signed."]["n"]))
        # The cut words between are skipped. Two labels that print alike are never called one speaker.
        self.assertEqual([row["next_by_speaker"] for row in traced], [said["We left."]["n"], None])

    def test_many_numbers_check_the_card_once(self):
        from unittest.mock import patch
        from gtm_connector import briefing
        brief = self.ok("brief", "acc-003")
        with patch.object(briefing, "_sealed", side_effect=briefing._sealed) as sealed:
            traced = self.ok("inspect", brief["brief_id"], *map(str, range(1, len(brief["units"]) + 1)))
        self.assertEqual(sealed.call_count, 1)
        self.assertEqual([row["n"] for row in traced], list(range(1, len(brief["units"]) + 1)))

    def test_json_and_a_bad_number_anywhere_give_the_same_parse_and_name_inspect_help(self):
        brief = self.ok("brief", "acc-003")
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(["inspect", brief["brief_id"], "5", "--json", "6", "--state-dir", str(self.state)])
        self.assertEqual((code, [row["n"] for row in json.loads(out.getvalue())]), (0, [5, 6]), err.getvalue())
        error = self.refused("inspect", brief["brief_id"], "five")
        self.assertEqual(error["next"], "gtm-connector inspect --help")

    def test_a_folder_name_with_a_control_character_never_reaches_the_terminal(self):
        state = self.root / "state\x1bname"
        profile.save(state, profile.DEMO)
        code, out, err = self.run_cli("brief", "acc-003", as_json=False, state=state)
        self.assertEqual(code, 0, err)
        self.assertNotIn("\x1b", out + err)
        self.assertNotIn("\x1b", self.text_in(state, "inspect", "acc-003"))

    def text_in(self, state, *arguments):
        code, out, err = self.run_cli(*arguments, as_json=False, state=state)
        self.assertEqual(code, 0, err)
        return out + err

    def test_inspect_writes_nothing_to_the_folder(self):
        self.ok("inspect", "acc-003")
        self.assertEqual(self.snapshot(), {Path("profile.json"): (self.state / "profile.json").read_bytes()})
        brief = self.ok("brief", "acc-003")["brief_id"]
        receipt = self.ok("audit")[0]["audit_id"]
        before = self.snapshot()
        for arguments in (("acc-003",), (brief,), (brief, "--role", "seller"), (brief, "5", "1"), (receipt,)):
            with self.subTest(arguments=arguments):
                self.ok("inspect", *arguments)
                self.text("inspect", *arguments)
                self.assertEqual(self.snapshot(), before)

    def test_anything_else_is_a_usage_error_that_names_the_four_forms(self):
        brief = self.ok("brief", "acc-003")["brief_id"]
        receipt = self.ok("audit")[0]["audit_id"]
        for arguments in (("acc-missing",), ("ACC-003",), ("brief-123",), ("brief-" + "A" * 32,), ("5",),
                          ("note-" + "0" * 64,), ("acc-003", "5"), (receipt, "5")):
            with self.subTest(arguments=arguments):
                error = self.refused("inspect", *arguments)
                self.assertEqual((error["code"], error["next"]), ("usage_error", "gtm-connector inspect --help"))
                for form in ("acc-003", "brief-", "numbers", "receipt"):
                    self.assertIn(form, error["message"])
        for arguments in (("acc-003",), (brief, "5"), (receipt,), (brief, "--role", "buyer")):
            with self.subTest(arguments=arguments):
                self.assertEqual(self.refused("inspect", *arguments, *(() if "--role" in arguments else
                                                                       ("--role", "customer")))["code"], "usage_error")

    def test_trace_and_account_are_gone_and_point_at_inspect(self):
        brief = self.ok("brief", "acc-003")["brief_id"]
        for arguments in (("trace", brief, "1"), ("account", "acc-003")):
            with self.subTest(command=arguments[0]):
                error = self.refused(*arguments)
                self.assertEqual((error["code"], error["next"]), ("usage_error", "gtm-connector inspect --help"))

    def test_numbers_are_the_digits_printed_on_the_card_and_every_number_is_checked(self):
        brief = self.ok("brief", "acc-003")["brief_id"]
        for spelled in ("\uff13", "+5", "05", "1_0", "5 ", "0", "-1"):
            with self.subTest(spelled=spelled):
                self.assertEqual(self.refused("inspect", brief, spelled)["next"], "gtm-connector inspect --help")

        def break_unit_seven(artifact):
            artifact["units"][6]["raw_sha256"] = "0" * 64

        self.reseal(brief, break_unit_seven)
        for numbers in (("1",), ("6",), ()):
            with self.subTest(numbers=numbers):
                self.assertIn("Number 7", self.refused("inspect", brief, *numbers)["message"])

    def test_the_newest_card_never_fails_the_account_read(self):
        self.ok("brief", "acc-003")
        with closing(sqlite3.connect(self.state / "audit.sqlite3")) as connection, connection:
            connection.execute("UPDATE audit_log SET arguments = '{bad' WHERE tool = 'save_brief'")
        account = self.ok("inspect", "acc-001")
        self.assertEqual((account["id"], account["newest_brief_id"]), ("acc-001", None))

    def test_a_brief_an_assistant_prepared_is_named_as_such(self):
        brief_id = "brief-" + "a" * 32
        (self.state / "selected-briefs").mkdir()
        (self.state / "selected-briefs" / (brief_id + ".json")).write_text("{}")
        for arguments in ((brief_id,), (brief_id, "1")):
            with self.subTest(arguments=arguments):
                self.assertIn("prepare_renewal_brief", self.refused("inspect", *arguments)["message"])

    def test_a_listing_shows_a_card_sealed_under_older_rules_by_todays_rules_and_says_why_a_sentence_is_off(self):
        brief = self.ok("brief", "acc-003")["brief_id"]

        def older_rules(artifact):
            artifact["units"][4]["text"] = "<!here> will not sign."

        self.reseal(brief, older_rules)
        self.assertIn('"[mention here] will not sign."', self.text("inspect", brief).splitlines()[4])
        self.assertEqual(self.ok("inspect", brief)["units"][4]["text"], "[mention here] will not sign.")

    def test_a_receipt_with_a_tool_the_connector_does_not_have_shows_that_name_as_a_literal(self):
        from gtm_connector.client import LocalClient
        from gtm_connector.runtime import Runtime
        with Runtime(self.state) as runtime, LocalClient(runtime.config) as client:
            client.call(client.authorize(["crm:read"]), "get_account [1] <!here>", {})
        [receipt] = [row for row in self.ok("audit") if row["tool"] not in ("get_account",)]
        self.assertIn('"get_account [1] <!here>"', self.text("inspect", receipt["audit_id"]))

    def test_mcp_help_says_what_it_needs_running(self):
        with redirect_stdout(io.StringIO()) as out:
            self.assertEqual(main(["mcp", "--help"]), 0)
        self.assertIn("debug serve", out.getvalue())


if __name__ == "__main__":
    unittest.main()
