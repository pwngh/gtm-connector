"""Owner selection and source commands use invented identities and local HTTPS only."""

from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector import profile
from gtm_connector.cli import main


PRIVATE = "invented-private-selector-token"
CHANNEL = "C12345678"
STAMP = "1790000000.000001"
ORIGIN = "https://invented-workspace.slack.com"
IDENTITY = {"app_id": "A12345678", "team_id": "T12345678", "user_id": "U12345678", "enterprise_id": None}


class SlackSourceCLITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.use(self.root / "source state")

    def use(self, state):
        """Select a state folder. Its Slack sign-in is saved inside it."""
        self.state = state
        self.connection = state / profile.SLACK_CONNECTION

    def connected(self, crm=None, *, calls=False):
        """Save the profile connect saves: live Slack beside the demo CRM, or beside an invented org."""
        imported = {"calls": {"provider": "imported"}} if calls else {}
        profile.save(self.state, {**profile.DEMO, **imported, "slack": {"provider": "slack"}} if crm is None else
                     {"crm": {"provider": "salesforce", "org": crm, "environment": "developer"}, **imported,
                      "slack": {"provider": "slack"}})

    def files(self):
        return {path.name for path in self.state.iterdir()}

    def invoke(self, *arguments, as_json=True):
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main([*arguments, *(["--json"] if as_json else [])])
        return code, out.getvalue(), err.getvalue()

    def map(self, *arguments, as_json=True):
        return self.invoke("slack", "map", "--state-dir", str(self.state),
                           "--account", "acc-003", "--channel", CHANNEL, "--thread-ts", STAMP,
                           *arguments, as_json=as_json)

    def failure(self, result, expected):
        code, out, err = result
        self.assertEqual(code, expected, err)
        self.assertEqual(out, "")
        detail = json.loads(err.splitlines()[-1])["error"]
        self.assertNotIn(PRIVATE, err)
        self.assertNotIn(CHANNEL, err)
        self.assertNotIn(STAMP, err)
        self.assertNotIn(ORIGIN, err)
        return detail

    def seed_connection(self, *, state="ready", expired=False):
        from datetime import timedelta
        from gtm_connector import slack_auth as auth
        now = datetime.now(timezone.utc)
        obtained = now - timedelta(hours=2) if expired else now
        configuration = auth.validate_configuration(client_id="123456789.987654321",
                                                    redirect_uri="http://localhost:8765/slack/callback")
        document = auth._parse_initial({"ok": True, "app_id": IDENTITY["app_id"],
                                        "team": {"id": IDENTITY["team_id"]},
                                        "authed_user": {"id": IDENTITY["user_id"], "token_type": "user",
                                                         "scope": "channels:history", "access_token": PRIVATE,
                                                         "refresh_token": "invented-refresh", "expires_in": 3600}},
                                       configuration=configuration, obtained_at=obtained)
        document["credential_state"] = state
        document["last_verified_at"] = auth._stamp(obtained)
        document["workspace_origin"] = ORIGIN
        with auth._connection_lock(self.connection, create=True) as descriptor:
            auth._write_credentials(descriptor, document)

    def test_source_action_help_and_auth_options_remain_separate(self):
        for action in ("map", "check"):
            code, out, err = self.invoke("slack", action, "--help")
            self.assertEqual(code, 0, err)
            for flag in ("--state-dir", "--account", "--json"):
                self.assertIn(flag, out)
            for flag in ("--client-id", "--user-scope", "--redirect-uri", "--connection-dir"):
                self.assertNotIn(flag, out)
            if action == "check":
                for flag in ("--account-number", "--thread-url", "--replace"):
                    self.assertNotIn(flag, out)
        for args in (("slack", "map", "--state-dir", str(self.state), "--account", "acc-003", "--channel", CHANNEL,
                      "--thread-ts", STAMP, "--connection-dir", PRIVATE),
                     ("slack", "check", "--state-dir", str(self.state), "--account", "acc-003",
                      "--account-number", PRIVATE)):
            self.failure(self.invoke(*args), 2)
        self.assertFalse(self.state.exists())

    def test_invalid_map_selectors_do_not_open_credentials_or_runtime(self):
        cases = [
            ["--account", PRIVATE], ["--account", "acc-sf-001000000000001AAA"],
            ["--channel", PRIVATE], ["--thread-ts", "1e6"],
        ]
        self.connected()
        with patch("gtm_connector.slack_auth.inspect_slack_connection", side_effect=AssertionError("No credentials")) as saved, \
                patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No state")) as runtime, \
                patch("gtm_connector.salesforce.SalesforceReadOnlyAdapter.from_sf",
                      side_effect=AssertionError("No CRM")) as crm:
            for options in cases:
                with self.subTest(options=options):
                    self.failure(self.map(*options), 2)
            for url in ("https://example.invalid/" + PRIVATE, ORIGIN + "/archives/" + CHANNEL + "/pbad",
                        ORIGIN + "/archives/" + CHANNEL + "/p1790000000000001?secret=" + PRIVATE):
                self.failure(self.invoke("slack", "map", "--state-dir", str(self.state), "--account", "acc-003",
                                         "--thread-url", url), 2)
            self.failure(self.invoke("slack", "map", "--state-dir", str(self.state),
                                     "--account-number", "invented-number", "--channel", CHANNEL,
                                     "--thread-ts", STAMP), 2)
            self.connected("invented-alias")
            self.failure(self.invoke("slack", "map", "--state-dir", str(self.state),
                                     "--account-number", "invented\u0085number", "--channel", CHANNEL,
                                     "--thread-ts", STAMP), 2)
        saved.assert_not_called()
        runtime.assert_not_called()
        crm.assert_not_called()
        self.assertEqual(self.files(), {"profile.json"})

    def test_a_map_needs_slack_connected_in_the_profile_before_credentials_or_state(self):
        with patch("gtm_connector.slack_auth.inspect_slack_connection", side_effect=AssertionError("No credentials")), \
                patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No state")):
            detail = self.failure(self.map(), 2)
            self.assertEqual(detail["message"], str(profile.NotConnected()))
            self.assertFalse(self.state.exists())
            profile.save(self.state, profile.DEMO)
            detail = self.failure(self.map(), 2)
        self.assertIn("Slack is not connected.", detail["message"])
        self.assertIn("gtm-connector connect slack", detail["message"])
        self.assertEqual(detail["next"], "gtm-connector connect --help")
        self.assertEqual(self.files(), {"profile.json"})

    def test_connected_slack_whose_sign_in_is_gone_refuses_and_never_reads_demo_messages(self):
        export = self.root / "invented.txt"
        export.write_text("Invented renewal discussion.\n")
        self.connected(calls=True)
        for arguments in (["serve"], ["brief", "acc-003"],
                          ["calls", "import", str(export), "--account", "acc-003"]):
            with self.subTest(command=arguments[0]):
                detail = self.failure(self.invoke(*arguments, "--state-dir", str(self.state)), 3)
                self.assertIn("slack_not_connected", detail["message"])
                self.assertIn("slack connect", detail["next"])
        self.assertEqual(self.files(), {"profile.json", "server.lock"})

    def test_map_pending_unchecked_and_expired_credentials_have_distinct_recovery(self):
        for state, expired, detail_code, next_action in (
            ("identity_unchecked", False, "slack_identity_unchecked", "status"),
            ("identity_unchecked", True, "slack_reconnect_required", "connect"),
            ("refresh_pending", False, "slack_reconnect_required", "connect"),
        ):
            with self.subTest(state=state, expired=expired):
                self.connected()
                self.seed_connection(state=state, expired=expired)
                with patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No state")) as runtime:
                    detail = self.failure(self.map(), 3)
                self.assertIn(detail_code, detail["message"])
                self.assertIn("slack " + next_action, detail["next"])
                runtime.assert_not_called()
                self.assertEqual(self.files(), {"profile.json", "slack-connection"})

    def test_copied_link_must_match_saved_origin_before_crm_or_state(self):
        self.connected("invented-alias")
        self.seed_connection()
        with patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No state")) as runtime, \
                patch("gtm_connector.salesforce.SalesforceReadOnlyAdapter.from_sf",
                      side_effect=AssertionError("No CRM")) as crm:
            self.failure(self.invoke("slack", "map", "--state-dir", str(self.state),
                                     "--account-number", "invented-number", "--thread-url",
                                     "https://different-workspace.slack.com/archives/" + CHANNEL +
                                     "/p1790000000000001"), 2)
        runtime.assert_not_called()
        crm.assert_not_called()
        self.assertEqual(self.files(), {"profile.json", "slack-connection"})

    def test_map_connection_switch_after_link_validation_never_pins_or_resolves(self):
        from gtm_connector import slack_auth as auth, runtime
        from gtm_connector.slack_workflow import resolve_slack_account
        inspect = auth.inspect_slack_connection
        for copied_link in (False, True):
            with self.subTest(copied_link=copied_link):
                self.use(self.root / ("linked" if copied_link else "native"))
                self.connected()
                self.seed_connection()
                observations = []

                def switch_after_selection(directory):
                    selection = inspect(directory)
                    observations.append(selection)
                    if len(observations) == 1:
                        with auth._connection_lock(directory, create=False) as descriptor:
                            document = auth._read_credentials(descriptor)
                            document["identity"]["team_id"] = "T99999999"
                            document["workspace_origin"] = "https://another-invented.slack.com"
                            auth._write_credentials(descriptor, document)
                    return selection

                selector = (["--thread-url", ORIGIN + "/archives/" + CHANNEL + "/p1790000000000001"]
                            if copied_link else ["--channel", CHANNEL, "--thread-ts", STAMP])
                with patch.object(auth, "inspect_slack_connection", side_effect=switch_after_selection), \
                        patch.object(runtime, "_prepare_source_state", wraps=runtime._prepare_source_state) as source, \
                        patch("gtm_connector.slack_workflow.resolve_slack_account",
                              wraps=resolve_slack_account) as resolve:
                    detail = self.failure(self.invoke("slack", "map", "--state-dir", str(self.state),
                                                      "--account", "acc-003", *selector), 3)
                self.assertIn("slack_source_mismatch", detail["message"])
                source.assert_not_called()
                resolve.assert_not_called()
                self.assertEqual(len(observations), 2)
                self.assertEqual(self.files(), {"server.lock", "profile.json", "slack-connection"})

    def test_actual_map_is_audited_idempotent_and_never_reads_slack(self):
        from gtm_connector.audit import read_audit
        self.connected()
        self.seed_connection()
        with patch("gtm_connector.slack_auth.get_slack_access", side_effect=AssertionError("No token read")), \
                patch("gtm_connector.slack_reader.SlackThreadReader.read_slack_messages",
                      side_effect=AssertionError("No Slack read")):
            first = self.map()
            self.assertEqual(first[0], 0, first[2])
            original = (self.state / "slack-mappings.json").read_bytes()
            second = self.map("--replace", "--reassign")
        self.assertEqual(second[0], 0, second[2])
        self.assertEqual((self.state / "slack-mappings.json").read_bytes(), original)
        for result, expected in ((first, "saved"), (second, "unchanged")):
            output = json.loads(result[1])
            self.assertEqual(set(output), {"schema_version", "status", "selection", "thread_access", "counts",
                                           "duration_ms", "next"})
            self.assertEqual(output["status"], expected)
            self.assertEqual(output["thread_access"], "unchecked")
            self.assertEqual(output["counts"], {"selected_threads": 1})
            for private in (PRIVATE, CHANNEL, STAMP, ORIGIN, "acc-003", IDENTITY["team_id"]):
                self.assertNotIn(private, result[1] + result[2])
            self.assertTrue(all("progress" in json.loads(line) for line in result[2].splitlines()))
        receipts = read_audit(self.state / "audit.sqlite3")
        self.assertEqual(len(receipts), 2)
        self.assertEqual({row["tool"] for row in receipts}, {"get_account"})
        self.assertEqual({row["status"] for row in receipts}, {"ok"})
        self.assertFalse((self.state / "slack.sqlite3").exists())

    def test_both_explicit_initialization_orders_keep_imported_calls_and_live_slack(self):
        export = self.root / "invented.txt"
        export.write_text("Invented renewal discussion.\nKeep the next meeting tentative.\n")
        for map_first in (True, False):
            with self.subTest(map_first=map_first):
                self.use(self.root / ("map first" if map_first else "import first"))
                self.connected(calls=True)
                self.seed_connection()
                def imported():
                    return self.invoke("calls", "--state-dir", str(self.state), "import", str(export),
                                       "--account", "acc-003")
                actions = [self.map, imported]
                if not map_first:
                    actions.reverse()
                with patch("gtm_connector.slack_reader.SlackThreadReader.read_slack_messages",
                           side_effect=AssertionError("No Slack read")):
                    for action in actions:
                        code, out, err = action()
                        self.assertEqual(code, 0, err)
                        self.assertIn(json.loads(out)["status"], ("saved", "imported"))
                self.assertTrue((self.state / "imports.sqlite3").is_file())
                self.assertTrue((self.state / "slack-mappings.json").is_file())
                self.assertFalse((self.state / "calls.sqlite3").exists())
                self.assertFalse((self.state / "slack.sqlite3").exists())

    def test_live_brief_banner_names_selected_sources_once_without_changing_json(self):
        brief = {"brief_id": "brief-invented", "as_of": "2027-01-15", "text": "An invented checked brief."}
        self.connected(calls=True)
        for as_json in (False, True):
            with patch("gtm_connector.runtime.Runtime") as runtime, \
                    patch("gtm_connector.briefing.create_brief", return_value=brief) as create:
                code, out, err = self.invoke("brief", "acc-003", "--state-dir", str(self.state), as_json=as_json)
            self.assertEqual(code, 0, err)
            self.assertEqual(runtime.call_args.kwargs["slack_backend"], "slack")
            self.assertEqual(runtime.call_args.kwargs["slack_connection_dir"], self.connection)
            self.assertTrue(create.call_args.kwargs["include_slack"])
            if as_json:
                self.assertEqual(json.loads(out), brief)
                self.assertEqual(err, "")
            else:
                self.assertEqual(err.count("Sources:"), 1)
                self.assertIn("CRM is demo data", err)
                self.assertIn("calls are imported transcripts", err)
                self.assertIn("Slack is the mapped thread, whose access is checked when it is read", err)
                self.assertNotIn("Slack is demo", err)

    def test_map_human_result_discloses_unchecked_thread_without_private_selection(self):
        self.connected()
        self.seed_connection()
        code, out, err = self.map(as_json=False)
        self.assertEqual(code, 0, err)
        self.assertIn("selection saved", out)
        self.assertIn("Thread access has not been checked", out)
        self.assertIn("slack check --help", out)
        self.assertEqual(err.count("Sources:"), 1)
        for value in (PRIVATE, CHANNEL, STAMP, ORIGIN, "acc-003"):
            self.assertNotIn(value, out + err)

    def test_live_server_reports_selected_sources_without_reading_credentials_or_messages(self):
        from queue import SimpleQueue
        from gtm_connector.audit import read_audit
        self.connected()
        self.seed_connection()
        class InterruptQueue(SimpleQueue):
            def get(self, *args, **kwargs):
                raise KeyboardInterrupt
        first = True
        def queue_factory():
            nonlocal first
            if first:
                first = False
                return InterruptQueue()
            return SimpleQueue()
        with patch("queue.SimpleQueue", side_effect=queue_factory), \
                patch("gtm_connector.slack_auth.get_slack_access", side_effect=AssertionError("No token acquisition")), \
                patch("gtm_connector.slack_reader.SlackThreadReader.read_slack_messages",
                      side_effect=AssertionError("No messages")):
            code, out, err = self.invoke("serve", "--state-d", str(self.state))
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out)["sources"], {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "slack"})
        self.assertEqual(read_audit(self.state / "audit.sqlite3"), [])
        self.assertFalse((self.state / "slack.sqlite3").exists())
        for value in (PRIVATE, CHANNEL, STAMP, ORIGIN, IDENTITY["team_id"]):
            self.assertNotIn(value, out + err)

    def test_completed_check_passes_report_through_and_failed_report_exits_four(self):
        names = ("private_state", "mapping", "credential_binding", "thread_read", "receipt")
        report = {"schema_version": 1, "status": "passed", "crm_access_checked": False,
                  "calls_access_checked": False, "duration_ms": 2.5, "api_calls": 1, "response_bytes": 100,
                  "counts": {"messages": 2}, "next": "", "steps": [
                      {"name": name, "status": "pass", "count": 1, "duration_ms": 0.5, "api_calls": 0,
                       "response_bytes": 0, "code": "ok", "message": "The selected step passed.", "next": ""}
                      for name in names]}
        def checked(directory, *, connection_dir, account_id, progress):
            self.assertEqual(directory, self.state)
            self.assertEqual(connection_dir, self.connection)
            self.assertEqual(account_id, "acc-003")
            progress("private_state", "running")
            progress(PRIVATE, "running")
            progress("thread_read", "pass")
            return report
        for expected in (0, 4):
            report["status"] = "passed" if expected == 0 else "failed"
            with patch("gtm_connector.slack_workflow.run_slack_check", side_effect=checked), \
                    patch("gtm_connector.runtime.Runtime", side_effect=AssertionError("No normal Runtime")), \
                    patch("gtm_connector.salesforce.SalesforceReadOnlyAdapter.from_sf",
                          side_effect=AssertionError("No CRM")):
                code, out, err = self.invoke("slack", "check", "--state-dir", str(self.state),
                                             "--account", "acc-003")
            self.assertEqual(code, expected, err)
            self.assertEqual(json.loads(out), report)
            self.assertNotIn(PRIVATE, out + err)
            events = [json.loads(line) for line in err.splitlines()]
            self.assertEqual([row["progress"]["step"] for row in events[:2]], ["private_state", "thread_read"])
            if expected == 4:
                self.assertEqual(events[-1]["error"]["code"], "check_failed")
        self.assertFalse(self.state.exists())

    def test_source_errors_and_interrupt_do_not_inherit_auth_or_calls_recovery(self):
        from gtm_connector.runtime import SourceIdentityError
        from gtm_connector.slack_mapping import SlackMappingError
        for error, expected, text in ((SlackMappingError("slack_mapping_missing"), 3, "slack map"),
                                       (SourceIdentityError("The source selection does not match."), 3, "slack map"),
                                       (RuntimeError(PRIVATE), 1, "doctor"),
                                       (KeyboardInterrupt(), 130, "slack check")):
            with patch("gtm_connector.slack_workflow.run_slack_check", side_effect=error):
                detail = self.failure(self.invoke("slack", "check", "--state-dir", str(self.state),
                                                  "--account", "acc-003"), expected)
            self.assertIn(text, detail["next"])
            self.assertNotIn("calls import", detail["next"])
            self.assertNotIn("Repeat this command", detail["message"])
            self.assertNotIn("remote approval", detail["message"])

    def test_terminal_check_keeps_metrics_compact_and_failure_recovery_visible(self):
        from gtm_connector.cli import human_result
        report = {"status": "failed", "steps": [
            {"name": "thread_read", "status": "fail", "count": 0, "duration_ms": 20.5, "api_calls": 1,
             "response_bytes": 200, "message": "The selected thread was not accessible. Check its parent and access.",
             "next": "gtm-connector slack map --help"}]}
        with patch("sys.stdout.isatty", return_value=True), patch.dict("os.environ", {"NO_COLOR": "1"}):
            rendered = human_result("slack", report, action="check")
        self.assertIn("CRM and calls access were not checked", rendered)
        self.assertIn("20.5", rendered)
        self.assertIn("200", rendered)
        self.assertIn(report["steps"][0]["message"], rendered)
        self.assertIn("Next: gtm-connector slack map --help", rendered)
        self.assertLessEqual(max(map(len, rendered.splitlines())), 80)


if __name__ == "__main__":
    unittest.main()
