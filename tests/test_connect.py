"""connect saves a folder's sources once. Every other command reads them and takes no source flag."""

from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector import profile
from gtm_connector.cli import main


ROOT = Path(__file__).resolve().parents[1]
NOT_CONNECTED = ("No CRM is connected. Run gtm-connector connect salesforce --org ALIAS, "
                 "or gtm-connector connect demo for invented data.")
DEMO = {"crm": {"provider": "demo"}, "calls": {"provider": "demo"}, "slack": {"provider": "demo"}}
PRIVATE = "invented-private-slack-token"


class ConnectTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = self.root / "state"
        self.export = self.root / "call.txt"
        self.export.write_text("Jordan Lee: We will sign on Friday.\nMarian Holt: Send the rider first.\n")

    def run_cli(self, *arguments, as_json=True):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main([*arguments, "--state-dir", str(self.state), *(["--json"] if as_json else [])])
        return code, out.getvalue(), err.getvalue()

    def ok(self, *arguments):
        code, out, err = self.run_cli(*arguments)
        self.assertEqual(code, 0, err)
        return json.loads(out)

    def refused(self, *arguments, code=2):
        actual, out, err = self.run_cli(*arguments)
        self.assertEqual((actual, out), (code, ""), err)
        return json.loads(err.splitlines()[-1])["error"]

    def files(self):
        return {path.name for path in self.state.iterdir()}

    def sign_in_to_slack(self):
        from gtm_connector import slack_auth as auth
        configuration = auth.validate_configuration(client_id="123456789.987654321",
                                                    redirect_uri="http://localhost:8765/slack/callback")
        now = datetime.now(timezone.utc)
        document = auth._parse_initial(
            {"ok": True, "app_id": "A12345678", "team": {"id": "T12345678"},
             "authed_user": {"id": "U12345678", "token_type": "user", "scope": "channels:history",
                             "access_token": PRIVATE, "refresh_token": "invented-refresh", "expires_in": 3600}},
            configuration=configuration, obtained_at=now)
        document["credential_state"] = "ready"
        document["last_verified_at"] = auth._stamp(now)
        with auth._connection_lock(self.state / profile.SLACK_CONNECTION, create=True) as descriptor:
            auth._write_credentials(descriptor, document)

    def test_a_folder_with_no_profile_refuses_each_daily_command_and_creates_nothing(self):
        commands = (["brief", "acc-003"], ["accounts"], ["account", "acc-003"], ["serve"],
                    ["mcp", "stdio", "--scope", "crm:read"],
                    ["calls", "import", str(self.export), "--account", "acc-003"],
                    ["slack", "map", "--account", "acc-003", "--channel", "C12345678",
                     "--thread-ts", "1790000000.000001"])
        for arguments in commands:
            with self.subTest(command=arguments[0]), \
                    patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No state")):
                error = self.refused(*arguments)
                self.assertEqual((error["code"], error["message"]), ("usage_error", NOT_CONNECTED))
                self.assertEqual(error["message"], str(profile.NotConnected()))
                self.assertEqual(error["next"], "gtm-connector connect --help")
                self.assertFalse(self.state.exists())
        code, out, err = self.run_cli("brief", "acc-003", as_json=False)
        self.assertEqual((code, out), (2, ""))
        self.assertEqual(err, f"Error: {NOT_CONNECTED}\nNext: gtm-connector connect --help\n")

    def test_connect_demo_then_brief_accounts_and_account_read_demo_data_with_no_flags(self):
        connected = self.ok("connect", "demo")
        self.assertEqual(connected, {"schema_version": 1, "provider": "demo", "status": "connected", "sources": DEMO})
        self.assertEqual(self.files(), {"profile.json", "server.lock"})
        self.assertEqual((self.state / "profile.json").stat().st_mode & 0o777, 0o600)
        self.assertEqual(json.loads((self.state / "profile.json").read_text()), {"schema_version": 1, "sources": DEMO})
        brief = self.ok("brief", "acc-003")
        shown = (ROOT / "README.md").read_text().split("```text\nDEAL CARD", 1)[1].split("```", 1)[0]
        # The card says when its sources were read. Every other character equals the README card.
        self.assertEqual(re.sub(r"\nSources read [^\n]+\n", "\nSources read 2027-01-15T14:00:02Z.\n",
                                brief["text"], count=1), "DEAL CARD" + shown)
        self.assertEqual(brief["sources"], {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "demo_fixture"})
        self.assertEqual(len(self.ok("accounts")), 30)
        self.assertEqual(self.ok("account", "acc-003")["name"], "Thornfield Wren LLP")
        self.assertEqual(self.ok("trace", brief["brief_id"], "5")["speaker"], "Marian Holt")

    def test_accounts_read_the_demo_book_before_any_card_and_leave_the_folder_as_it_was(self):
        self.ok("connect", "demo")
        self.assertEqual([row["id"] for row in self.ok("accounts", "--query", "Thornfield")], ["acc-003"])
        self.assertEqual(self.ok("account", "acc-003")["name"], "Thornfield Wren LLP")
        self.assertEqual(self.files(), {"profile.json", "server.lock"})
        # Reading accounts rules nothing out: transcripts can still be connected and read beside the demo CRM.
        self.ok("connect", "calls")
        code, out, err = self.run_cli("calls", "import", str(self.export), "--account", "acc-003")
        self.assertEqual(code, 0, err)
        self.assertEqual(self.ok("brief", "acc-003")["sources"]["calls"], "imported_transcript")
        # Once the folder holds its own book, that book is the one read, as the card reads it.
        import sqlite3
        with sqlite3.connect(self.state / "crm.sqlite3") as connection:
            connection.execute("UPDATE accounts SET name = 'Thornfield Wren Partners' WHERE id = 'acc-003'")
        self.assertEqual([row["name"] for row in self.ok("accounts", "--query", "Thornfield")],
                         ["Thornfield Wren Partners"])

    def test_status_says_what_is_connected_before_and_after(self):
        for arguments in (["connect"], ["connect", "status"]):
            self.assertEqual(self.ok(*arguments), {"schema_version": 1, "sources": {}})
            code, out, err = self.run_cli(*arguments, as_json=False)
            self.assertEqual((code, err), (0, ""))
            self.assertEqual(out, "crm\tnot connected\t\ncalls\tnot connected\t\nslack\tnot connected\t\n")
        self.assertFalse(self.state.exists())
        self.ok("connect", "demo")
        self.assertEqual(self.ok("connect", "status"), {"schema_version": 1, "sources": DEMO})
        code, out, err = self.run_cli("connect", as_json=False)
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(out, "crm\tconnected\tdemo data\ncalls\tconnected\tdemo data\nslack\tconnected\tdemo data\n")
        from gtm_connector.cli import human_result
        with patch("sys.stdout.isatty", return_value=True), patch.dict("os.environ", {"NO_COLOR": "1"}):
            shown = human_result("connect", self.ok("connect", "status"))
        self.assertEqual([line.split() for line in shown.splitlines()],
                         [["SOURCE", "STATUS", "DETAIL"], *[[kind, "connected", "demo", "data"] for kind in DEMO]])

    def test_remove_detaches_the_named_source_and_only_when_it_is_the_one_connected(self):
        self.assertEqual(self.ok("connect", "demo", "--remove")["status"], "not_connected")
        self.assertFalse(self.state.exists())
        self.ok("connect", "demo")
        # Salesforce is not what this folder is connected to, so nothing changes.
        unchanged = self.ok("connect", "salesforce", "--remove")
        self.assertEqual((unchanged["status"], unchanged["sources"]), ("not_connected", DEMO))
        self.assertEqual(self.ok("connect", "calls", "--remove")["sources"], DEMO)
        removed = self.ok("connect", "demo", "--remove")
        self.assertEqual(removed, {"schema_version": 1, "provider": "demo", "status": "removed", "sources": {}})
        self.assertEqual(self.refused("brief", "acc-003")["message"], NOT_CONNECTED)
        code, out, err = self.run_cli("connect", "demo", "--remove", as_json=False)
        self.assertEqual((code, err), (0, ""))
        self.assertTrue(out.startswith("Demo data was not connected. Nothing changed.\n"), out)

    def test_connect_calls_then_import_then_brief_shows_the_transcript(self):
        self.assertEqual(self.refused("connect", "calls")["message"], NOT_CONNECTED)
        self.assertFalse(self.state.exists())
        self.ok("connect", "demo")
        connected = self.ok("connect", "calls")
        self.assertEqual(connected["sources"], {**DEMO, "calls": {"provider": "imported"}})
        self.assertEqual(self.files(), {"profile.json", "server.lock"})
        code, out, err = self.run_cli("calls", "import", str(self.export), "--account", "acc-003", "--speaker-names")
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out)["source"], "imported_transcript")
        brief = self.ok("brief", "acc-003")
        self.assertEqual(brief["sources"], {"crm": "demo_fixture", "calls": "imported_transcript",
                                            "slack": "demo_fixture"})
        said = [(unit["speaker"], unit["role"], unit["text"]) for unit in brief["units"]
                if unit["kind"] == "sentence" and unit["source"] == "calls"]
        self.assertEqual(said, [("Jordan Lee", "unknown", "We will sign on Friday."),
                                ("Marian Holt", "customer", "Send the rider first.")])
        self.assertIn('        "We will sign on Friday."', brief["text"].splitlines())
        self.assertFalse((self.state / "calls.sqlite3").exists())
        code, out, err = self.run_cli("brief", "acc-003", as_json=False)
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "Sources: CRM is demo data; calls are imported transcripts, whose provider and "
                              "statements are unverified; Slack is demo data.\n")
        # Beside the demo CRM a removed source is demo data again. Demo call notes never share a folder with
        # transcripts, so removing them is refused, the profile stays, and the folder still reads them.
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        error = self.refused("connect", "calls", "--remove", code=3)
        self.assertIn("source_identity_mismatch: This state folder uses imported transcripts.", error["message"])
        self.assertNotIn("--calls-backend", error["message"])
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}, before)
        self.assertEqual(self.ok("brief", "acc-003")["sources"]["calls"], "imported_transcript")

    def test_a_folder_that_read_demo_data_refuses_transcripts_and_live_slack_at_connect(self):
        self.ok("connect", "demo")
        self.ok("brief", "acc-003")
        self.sign_in_to_slack()
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        fresh = "Connect the sources again with --state-dir " + str(self.root / "state-fresh")
        error = self.refused("connect", "calls", "--seller", "Jordan Lee", code=3)
        self.assertIn("source_identity_mismatch: Imported transcripts cannot reuse fixture calls.", error["message"])
        self.assertIn(fresh, error["message"])
        error = self.refused("connect", "slack", code=3)
        self.assertIn("slack_source_mismatch", error["message"])
        self.assertEqual({path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}, before)
        self.assertEqual(profile.load(self.state), DEMO)
        self.assertEqual(self.refused("calls", "import", str(self.export), "--account", "acc-003")["message"],
                         "Transcripts are not connected. Run gtm-connector connect calls first.")

    def test_the_demo_crm_connected_again_keeps_the_transcripts_the_folder_holds(self):
        self.ok("connect", "demo")
        self.ok("connect", "calls")
        code, out, err = self.run_cli("calls", "import", str(self.export), "--account", "acc-003")
        self.assertEqual(code, 0, err)
        held = {**DEMO, "calls": {"provider": "imported"}}
        # Over the same profile, and after the CRM was removed, demo data never stands in for the transcripts.
        self.assertEqual(self.ok("connect", "demo")["sources"], held)
        self.assertEqual(self.ok("connect", "demo", "--remove")["sources"], {})
        self.assertEqual(self.refused("connect", "calls")["message"], NOT_CONNECTED)
        self.assertEqual(self.ok("connect", "demo")["sources"], held)
        self.assertEqual(self.ok("brief", "acc-003")["sources"]["calls"], "imported_transcript")
        # A folder made before profiles, which holds the same state with no profile.json, connects the same way.
        (self.state / "profile.json").unlink()
        self.assertEqual(self.ok("connect", "demo")["sources"], held)

    def test_a_folder_being_served_or_changed_refuses_connect_and_keeps_its_profile(self):
        import fcntl
        self.ok("connect", "demo")
        with open(self.state / "server.lock", "wb") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            for arguments in (["connect", "calls"], ["connect", "demo", "--remove"], ["connect", "demo"]):
                with self.subTest(arguments=arguments):
                    error = self.refused(*arguments, code=1)
                    self.assertIn("running connector or another command", error["message"])
            # Reading what is connected needs no lock.
            self.assertEqual(self.ok("connect", "status")["sources"], DEMO)
        self.assertEqual(profile.load(self.state), DEMO)
        self.assertEqual(self.ok("connect", "calls")["sources"]["calls"], {"provider": "imported"})

    def test_a_connect_that_fails_leaves_no_folder_and_a_failed_seller_list_changes_nothing(self):
        self.assertEqual(self.refused("connect", "calls")["message"], NOT_CONNECTED)
        self.assertFalse(self.state.exists())
        self.ok("connect", "demo")
        (self.state / "sellers.json").mkdir()
        code, out, err = self.run_cli("connect", "calls", "--seller", "Jordan Lee")
        self.assertNotEqual(code, 0)
        self.assertEqual(profile.load(self.state), DEMO)

    def test_accounts_read_only_the_folders_own_private_book(self):
        import sqlite3
        other = self.root / "other"
        self.ok("connect", "demo")
        self.ok("brief", "acc-003")
        (self.state / "crm.sqlite3").rename(other)
        with sqlite3.connect(other) as connection:
            connection.execute("UPDATE accounts SET name = 'Another folder' WHERE id = 'acc-003'")
        for link in ("symlink_to", "hardlink_to"):
            with self.subTest(link=link):
                getattr(self.state / "crm.sqlite3", link)(other)
                self.assertEqual(self.refused("account", "acc-003", code=3)["code"], "refused")
                (self.state / "crm.sqlite3").unlink()
        # A folder pinned to Salesforce has no demo book, whatever its profile says.
        (self.state / "crm-connection.json").write_text("{}")
        self.assertIn("Salesforce connection", self.refused("accounts", code=3)["message"])

    def test_an_org_username_or_an_address_given_as_the_environment_is_refused_and_never_repeated(self):
        for arguments, private in ((["--org", "dana@invented.example"], "dana@invented.example"),
                                   (["--org", "invented-alias", "--environment", "https://invented.example"],
                                    "invented.example")):
            with self.subTest(private=private):
                code, out, err = self.run_cli("connect", "salesforce", *arguments)
                self.assertEqual((code, out), (2, ""))
                self.assertNotIn(private, err)
        self.assertFalse(self.state.exists())

    def test_verify_runs_only_in_a_folder_of_its_own(self):
        self.ok("connect", "demo")
        self.ok("connect", "calls")
        self.assertIn("Give verify its own --state-dir", self.refused("verify")["message"])
        self.assertFalse((self.state / "calls.sqlite3").exists())

    def test_audit_before_any_read_names_a_command_the_folder_can_run(self):
        self.ok("connect", "demo")
        self.assertEqual(self.refused("audit")["next"], f"gtm-connector demo --state-dir {self.state}")
        self.ok("connect", "calls")
        self.assertEqual(self.refused("audit")["next"], "gtm-connector brief --help")

    def test_connect_slack_is_refused_until_a_sign_in_is_saved_in_the_folder(self):
        self.ok("connect", "demo")
        error = self.refused("connect", "slack")
        self.assertEqual(error["code"], "usage_error")
        self.assertIn("gtm-connector slack connect", error["message"])
        self.assertEqual(profile.load(self.state), DEMO)
        self.sign_in_to_slack()
        connected = self.ok("connect", "slack")
        self.assertEqual(connected["sources"], {**DEMO, "slack": {"provider": "slack"}})
        self.assertNotIn(PRIVATE, json.dumps(connected) + (self.state / "profile.json").read_text())
        self.assertEqual(self.ok("connect", "slack", "--remove")["sources"], DEMO)

    def test_live_slack_and_transcripts_beside_the_demo_crm_open_together_from_the_profile(self):
        from queue import SimpleQueue
        self.ok("connect", "demo")
        self.sign_in_to_slack()
        self.ok("connect", "slack")
        self.ok("connect", "calls")

        class InterruptQueue(SimpleQueue):
            def get(self, *args, **kwargs):
                raise KeyboardInterrupt

        queues = [InterruptQueue()]
        with patch("queue.SimpleQueue", side_effect=lambda: queues.pop() if queues else SimpleQueue()), \
                patch("gtm_connector.slack_auth.get_slack_access", side_effect=AssertionError("No token read")):
            code, out, err = self.run_cli("serve")
        self.assertEqual(code, 0, err)
        ready = json.loads(out)
        self.assertEqual(ready["sources"], {"crm": "demo_fixture", "calls": "imported_transcript", "slack": "slack"})
        owner = json.loads((self.state / "owner.json").read_text())
        self.assertEqual(owner["slack_connection_dir"], str(self.state / profile.SLACK_CONNECTION))
        for fixture in ("calls.sqlite3", "slack.sqlite3"):
            self.assertFalse((self.state / fixture).exists())
        self.assertNotIn(PRIVATE, out + err)
        # Once the folder has opened live Slack and transcripts, removing either, which would make it demo data
        # again, is refused and changes nothing.
        for kind, mismatch in (("slack", "slack_source_mismatch"), ("calls", "source_identity_mismatch")):
            with self.subTest(kind=kind):
                self.assertIn(mismatch, self.refused("connect", kind, "--remove", code=3)["message"])
        self.assertEqual(profile.load(self.state),
                         {**DEMO, "calls": {"provider": "imported"}, "slack": {"provider": "slack"}})

    def test_the_seller_list_is_taken_at_connect_and_kept_until_given_again(self):
        self.ok("connect", "demo")
        sellers = self.state / "sellers.json"
        self.ok("connect", "calls", "--seller", "Jordan Lee", "--seller", "U12345678")
        self.assertEqual(json.loads(sellers.read_text()),
                         {"schema_version": 1, "sellers": ["Jordan Lee", "U12345678"]})
        self.assertEqual(sellers.stat().st_mode & 0o777, 0o600)
        self.ok("connect", "calls")
        self.assertEqual(json.loads(sellers.read_text())["sellers"], ["Jordan Lee", "U12345678"])
        for arguments in (["connect", "calls", "--seller", ""], ["connect", "calls", "--seller", "x" * 201],
                          ["connect", "calls", *["--seller", "Avery"] * 201],
                          ["connect", "calls", "--remove", "--seller", "Avery"],
                          ["connect", "demo", "--seller", "Avery"], ["connect", "status", "--seller", "Avery"]):
            with self.subTest(arguments=arguments[:5]):
                self.assertEqual(self.refused(*arguments)["code"], "usage_error")
                self.assertEqual(json.loads(sellers.read_text())["sellers"], ["Jordan Lee", "U12345678"])
        self.assertEqual(profile.load(self.state), {**DEMO, "calls": {"provider": "imported"}})
        code, out, err = self.run_cli("calls", "import", str(self.export), "--account", "acc-003", "--speaker-names")
        self.assertEqual(code, 0, err)
        roles = {unit["speaker"]: (unit["role"], unit["basis"]) for unit in self.ok("brief", "acc-003")["units"]
                 if unit["kind"] == "sentence" and unit["source"] == "calls"}
        self.assertEqual(roles["Jordan Lee"], ("seller", "listed seller identity"))
        self.ok("connect", "calls", "--seller", "Someone Else")
        self.assertEqual(json.loads(sellers.read_text())["sellers"], ["Someone Else"])

    def test_an_option_of_another_provider_is_a_usage_error_that_changes_nothing(self):
        for arguments in (["connect", "demo", "--org", "invented-alias"], ["connect", "status", "--remove"],
                          ["connect", "calls", "--scope", "crm:read"], ["connect", "calls", "--environment", "sandbox"],
                          ["connect", "inspector", "--org", "invented-alias"], ["connect", "cursor", "--remove"],
                          ["connect", "slack", "--config-file", str(self.root / "config.json")],
                          ["connect", "salesforce"], ["connect", "salesforce", "--org", "bad alias"],
                          ["connect", "elsewhere"]):
            with self.subTest(arguments=arguments):
                self.assertEqual(self.refused(*arguments)["code"], "usage_error")
                self.assertFalse(self.state.exists())

    def test_the_demo_leaves_a_demo_profile_so_its_folder_answers_the_daily_commands(self):
        demo = self.ok("demo")
        self.assertEqual(profile.load(self.state), DEMO)
        self.assertEqual(self.ok("connect", "status")["sources"], DEMO)
        brief = self.ok("brief", "acc-003")
        # The two cards differ only in the line that says when their sources were read.
        lines, shown = brief["text"].splitlines(), demo["brief"]["text"].splitlines()
        self.assertEqual(lines[:1] + lines[2:], shown[:1] + shown[2:])
        self.assertEqual(self.ok("trace", demo["brief"]["brief_id"], "5")["kind"], "sentence")
        self.assertEqual(len(self.ok("audit", "--brief", brief["brief_id"])), 4)

    def test_the_source_flags_and_the_seed_command_are_gone(self):
        self.ok("connect", "demo")
        for arguments in (["brief", "acc-003", "--crm-backend", "sqlite"], ["serve", "--calls-backend", "imported"],
                          ["brief", "acc-003", "--slack-backend", "fixture"], ["brief", "acc-003", "--with-slack"],
                          ["serve", "--slack-connection-dir", str(self.root)], ["accounts", "--sf-org", "invented"],
                          ["account", "acc-003", "--sf-environment", "sandbox"],
                          ["accounts", "--db", str(self.root / "crm.sqlite3")],
                          ["calls", "import", str(self.export), "--account", "acc-003", "--crm-backend", "sqlite"],
                          ["slack", "status", "--connection-dir", str(self.root)], ["seed"]):
            with self.subTest(arguments=arguments):
                self.assertEqual(self.refused(*arguments)["code"], "usage_error")
        self.assertEqual(self.files(), {"profile.json", "server.lock"})


if __name__ == "__main__":
    unittest.main()
