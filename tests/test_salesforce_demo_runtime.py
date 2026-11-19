"""Private demo bindings keep source reads separate after connection metadata preflight."""

from contextlib import closing, redirect_stderr, redirect_stdout
import io
import json
from pathlib import Path
from queue import SimpleQueue
import re
import sqlite3
import stat
import tempfile
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.briefing import BriefingError, create_brief
from gtm_connector.cli import main
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadOnlyAdapter
from gtm_connector.salesforce_demo import bind_import, load_binding, write_binding


OBJECTS = (("Account", "001"), ("Contact", "003"), ("Opportunity", "006"), ("Contract", "800"))
TOKEN = "invented-demo-runtime-token"
UNKNOWN_ACCOUNT = "acc-sf-001000000000099AAA"


def receipt(offset=0):
    return {"status": 0, "result": [
        {"refId": f"{name}Ref{number}", "type": "Account", "id": prefix + f"{offset + number:012}" + "AAA"}
        for number in range(1, 4) for name, prefix in OBJECTS
    ]}


class SalesforceDemoRuntimeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.state = self.directory / "state"
        self.binding_path = self.directory / "binding.json"
        write_binding(self.binding_path, bind_import(receipt()))
        self.binding = load_binding(self.binding_path)
        self.account_id = self.binding.accounts["GTM-DEMO-001"]
        self.requests = []
        self.account_number_override = None
        self.reader = SalesforceReadOnlyAdapter("https://fixture-demo-runtime.my.salesforce.com", TOKEN,
                                               transport=httpx2.MockTransport(self.respond))

    def respond(self, request):
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "fixture-demo-runtime.my.salesforce.com")
        self.assertEqual(request.headers["Authorization"], "Bearer " + TOKEN)
        query = request.url.params["q"]
        selected = re.search(r"WHERE (?:Id|AccountId) = '([A-Za-z0-9]{18})'", query)
        native = selected[1] if selected is not None else self.account_id.removeprefix("acc-sf-")
        number = next((key for key, value in self.binding.accounts.items() if value == "acc-sf-" + native),
                      "UNBOUND-ACCOUNT")
        if self.account_number_override is not None:
            number = self.account_number_override
        if " FROM Organization" in query:
            rows = [{"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition", "IsSandbox": False}]
        elif " FROM Account" in query:
            rows = [{"Id": native, "Name": "Invented imported account", "Industry": "Manufacturing",
                     "AccountNumber": number}]
        elif " FROM Contact" in query:
            rows = [{"Id": "003" + native[3:], "AccountId": native, "Name": "Invented contact",
                     "Title": "Operations director", "Email": "contact@invented.example"}]
        elif " FROM Contract" in query:
            rows = [{"Id": "800" + native[3:], "AccountId": native, "Status": "Draft", "StatusCode": "Draft",
                     "StartDate": "2026-02-01", "ContractTerm": 12, "EndDate": "2027-01-31"}]
        elif " FROM Opportunity" in query:
            rows = [{"Id": "006" + native[3:], "AccountId": native, "Name": "Invented renewal opportunity",
                     "StageName": "Closed Won", "Amount": 24000, "CloseDate": "2027-01-10",
                     "IsClosed": True, "IsWon": True}]
        else:
            self.fail("Unexpected invented Salesforce query")
        return httpx2.Response(200, json={"totalSize": len(rows), "done": True, "records": rows})

    def runtime(self, binding=None):
        return Runtime(self.state, crm_reader=self.reader, demo_binding=self.binding if binding is None else binding)

    def rows(self, database, table):
        path = self.state / f"{database}.sqlite3"
        with closing(sqlite3.connect(path.as_uri() + "?mode=ro", uri=True)) as connection:
            connection.row_factory = sqlite3.Row
            return [dict(row) for row in connection.execute(f"SELECT * FROM {table} ORDER BY id")]

    def source_bytes(self):
        return {path.name: path.read_bytes() for path in self.state.iterdir()
                if path.is_file() and path.name != "server.lock"}

    def assert_connection_check_only(self):
        self.assertEqual(len(self.requests), 1)
        self.assertIn(" FROM Organization", self.requests[0].url.params["q"])

    def records(self, response):
        self.assertEqual(response.status_code, 200, response.text)
        result = response.json()["result"]
        self.assertFalse(result.get("isError"), response.text)
        self.assertTrue(result["structuredContent"]["untrusted_data"])
        return result["structuredContent"]["records"]

    def test_bound_card_numbers_all_three_sources_and_has_no_default_source_overlay(self):
        with self.runtime() as runtime:
            self.assert_connection_check_only()
            notes = self.rows("calls", "call_notes")
            messages = self.rows("slack", "messages")
            self.assertEqual([row["id"] for row in notes], ["note-demo-001", "note-demo-002", "note-demo-003"])
            self.assertEqual([row["id"] for row in messages], ["msg-demo-001", "msg-demo-002", "msg-demo-003"])
            self.assertEqual({row["account_id"] for row in notes}, set(self.binding.accounts.values()))
            self.assertEqual({row["account_id"] for row in messages}, set(self.binding.accounts.values()))
            brief = create_brief(runtime, self.account_id, as_of=self.binding.as_of, include_slack=True)
            self.assertEqual(brief["status"], "accepted")
            cited = {int(number) for number in re.findall(r"\[(\d+)\]", brief["text"])}
            self.assertLessEqual({"crm/accounts/" + self.account_id, "crm/contracts/ctr-sf-800000000000001AAA",
                                  "calls/call_notes/note-demo-001", "slack/messages/msg-demo-001"},
                                 {unit["record_ref"] for unit in brief["units"] if unit["n"] in cited})
            self.assertEqual(len(self.requests), 5)
            receipts = runtime.connector.audit.read(brief["brief_id"])
            self.assertEqual([row["tool"] for row in receipts],
                             ["get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
            self.assertEqual(receipts[2]["rows"], ["calls/call_notes/note-demo-001"])
            self.assertEqual(receipts[3]["rows"], ["slack/messages/msg-demo-001"])
            self.assertEqual(len(self.rows("crm", "accounts")), 30)
            self.assertEqual(self.rows("crm", "activities"), [])
            self.assertNotIn(TOKEN, json.dumps(brief))
        marker = self.state / "source-profile.json"
        profile = json.loads(marker.read_text())
        self.assertEqual(profile, {"schema_version": 1, "kit_id": self.binding.kit_id,
                                   "manifest_sha256": self.binding.manifest_sha256,
                                   "binding_sha256": self.binding.binding_sha256})
        self.assertEqual(stat.S_IMODE(marker.stat().st_mode), 0o600)
        self.assertNotIn(self.account_id, marker.read_text())

    def test_calls_and_slack_only_grants_make_no_crm_reads_even_for_unknown_ids(self):
        with self.runtime() as runtime, LocalClient(runtime.config) as client:
            for scope, tool, expected in (("calls:read", "read_call_notes", "note-demo-001"),
                                           ("slack:read", "read_slack_messages", "msg-demo-001")):
                token = client.authorize([scope])
                rows = self.records(client.call(token, tool, {"account_id": self.account_id}))
                self.assertEqual([row["id"] for row in rows], [expected])
                for identifier in (UNKNOWN_ACCOUNT, "acc-001"):
                    self.assertEqual(self.records(client.call(token, tool, {"account_id": identifier})), [])
                self.assertEqual(client.call(token, "get_account", {"account_id": self.account_id}).status_code, 403)
            self.assert_connection_check_only()
            self.assertEqual(len(runtime.connector.audit.read()), 8)

    def test_account_number_mismatch_is_safe_and_does_not_add_a_crm_round_trip(self):
        with self.runtime() as runtime, LocalClient(runtime.config) as client:
            token = client.authorize(["crm:read"])
            self.account_number_override = "PRIVATE-MISMATCH-NUMBER"
            response = client.call(token, "get_account", {"account_id": self.account_id})
            self.assertEqual(response.status_code, 200)
            self.assertTrue(response.json()["result"]["isError"])
            self.assertIn("salesforce_demo_binding_mismatch", response.text)
            self.assertNotIn(self.account_number_override, response.text)
            self.assertEqual(len(self.requests), 4)
            self.assertEqual(runtime.connector.audit.read()[-1]["rows"], [])
            self.account_number_override = "GTM-DEMO-001"
            response = client.call(token, "get_account", {"account_id": UNKNOWN_ACCOUNT})
            self.assertTrue(response.json()["result"]["isError"])
            self.assertIn("salesforce_demo_binding_mismatch", response.text)
            self.assertEqual(len(self.requests), 7)
            with self.assertRaises(BriefingError):
                create_brief(runtime, UNKNOWN_ACCOUNT, as_of=self.binding.as_of, include_slack=True)
            self.assertFalse((self.state / "briefs").exists())

    def test_unbound_account_keeps_empty_sources_and_never_borrows_a_kit_note(self):
        with self.runtime() as runtime:
            brief = create_brief(runtime, UNKNOWN_ACCOUNT, as_of=self.binding.as_of, include_slack=True)
            self.assertIn("  Calls: no call record was returned for this account.", brief["text"])
            self.assertIn("  Slack: no message was returned for this account.", brief["text"])
            self.assertIn("  No sentence was read.", brief["text"])
            self.assertEqual({unit["source"] for unit in brief["units"]}, {"crm"})
            self.assertEqual(len(self.requests), 5)

    def test_same_binding_restart_preserves_source_edits(self):
        with self.runtime():
            with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection, connection:
                connection.execute("UPDATE call_notes SET body = ? WHERE id = ?",
                                   ("Invented owner edit", "note-demo-001"))
            before = self.rows("calls", "call_notes"), self.rows("slack", "messages")
            profile = (self.state / "source-profile.json").read_bytes()
        reloaded = load_binding(self.binding_path)
        with self.runtime(reloaded):
            self.assertEqual((self.rows("calls", "call_notes"), self.rows("slack", "messages")), before)
            self.assertEqual((self.state / "source-profile.json").read_bytes(), profile)
            self.assert_connection_check_only()

    def test_changed_binding_and_default_mode_refuse_before_database_or_owner_mutation(self):
        with self.runtime():
            pass
        other_path = self.directory / "other-binding.json"
        write_binding(other_path, bind_import(receipt(10)))
        other = load_binding(other_path)
        before = self.source_bytes()
        for candidate in (Runtime(self.state, crm_reader=self.reader, demo_binding=other), Runtime(self.state),
                          Runtime(self.state, crm_reader=self.reader)):
            with self.subTest(binding=candidate.demo_binding is not None), self.assertRaisesRegex(
                    ValueError, "binding"):
                with candidate:
                    self.fail("A different source profile started")
            self.assertEqual(self.source_bytes(), before)
        self.assert_connection_check_only()

    def test_existing_state_without_marker_is_not_converted_to_a_bound_profile(self):
        with Runtime(self.state):
            pass
        before = self.source_bytes()
        with self.assertRaisesRegex(ValueError, "fresh state"):
            with self.runtime():
                self.fail("Default state was overlaid with imported IDs")
        self.assertEqual(self.source_bytes(), before)
        self.assertFalse((self.state / "source-profile.json").exists())

    def test_missing_marker_on_previously_bound_state_cannot_be_recreated_implicitly(self):
        with self.runtime():
            pass
        (self.state / "source-profile.json").unlink()
        before = self.source_bytes()
        with self.assertRaisesRegex(ValueError, "fresh state"):
            with self.runtime():
                self.fail("Existing imported sources were silently rebound")
        self.assertEqual(self.source_bytes(), before)
        self.assertFalse((self.state / "source-profile.json").exists())

    def test_later_source_mismatch_rolls_back_earlier_seed_changes(self):
        with self.runtime():
            pass
        with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection, connection:
            connection.execute("DELETE FROM call_notes")
        with closing(sqlite3.connect(self.state / "slack.sqlite3")) as connection, connection:
            connection.execute("UPDATE messages SET account_id = ? WHERE id = ?", (UNKNOWN_ACCOUNT, "msg-demo-001"))
        notes, messages = self.rows("calls", "call_notes"), self.rows("slack", "messages")
        with self.assertRaisesRegex(ValueError, "another or incomplete demo"):
            with self.runtime():
                self.fail("A mismatched second source accepted an earlier seed")
        self.assertEqual(self.rows("calls", "call_notes"), notes)
        self.assertEqual(self.rows("slack", "messages"), messages)
        with closing(sqlite3.connect(self.state / "slack.sqlite3")) as connection, connection:
            connection.execute("UPDATE messages SET account_id = ? WHERE id = ?", (self.account_id, "msg-demo-001"))
        with self.runtime():
            self.assertEqual(len(self.rows("calls", "call_notes")), 3)
        self.assert_connection_check_only()

    def test_invalid_or_symlink_profile_fails_without_reading_or_changing_sources(self):
        with self.runtime():
            pass
        marker = self.state / "source-profile.json"
        original = marker.read_bytes()
        for changed in (b"not JSON", original.replace(b'"schema_version": 1', b'"schema_version": true')):
            marker.write_bytes(changed)
            before = self.source_bytes()
            with self.assertRaisesRegex(ValueError, "source profile"):
                with self.runtime():
                    self.fail("An invalid marker was accepted")
            self.assertEqual(self.source_bytes(), before)
        marker.unlink()
        outside = self.directory / "outside-profile.json"
        outside.write_bytes(original)
        marker.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "source profile"):
            with self.runtime():
                self.fail("A linked marker was accepted")
        self.assertEqual(outside.read_bytes(), original)

    def test_binding_requires_salesforce_before_state_creation(self):
        with self.assertRaisesRegex(ValueError, "requires --crm-backend salesforce"):
            Runtime(self.state, demo_binding=self.binding)
        self.assertFalse(self.state.exists())

    def test_cli_rejects_wrong_backend_or_unknown_binding_before_credentials_or_runtime(self):
        for arguments in (["brief", "acc-001", "--salesforce-demo-binding", str(self.binding_path)],
                          ["serve", "--salesforce-demo-binding", str(self.binding_path)],
                          ["serve", "--crm-backend", "salesforce", "--sf-org", "invented",
                           "--salesforce-demo-binding", str(self.directory / "missing.json")]):
            with self.subTest(command=arguments[0]), \
                    patch.object(SalesforceReadOnlyAdapter, "from_sf") as credentials, \
                    patch("gtm_connector.runtime.Runtime") as runtime, \
                    redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
                code = main([*arguments, "--state-dir", str(self.state), "--json"])
            self.assertEqual(code, 2)
            self.assertEqual(stdout.getvalue(), "")
            self.assertEqual(json.loads(stderr.getvalue())["error"]["code"], "usage_error")
            credentials.assert_not_called()
            runtime.assert_not_called()
        self.assertFalse(self.state.exists())

    def test_cli_bound_brief_uses_loaded_sources_through_actual_runtime(self):
        with patch.object(SalesforceReadOnlyAdapter, "from_sf", return_value=self.reader) as credentials, \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["brief", self.account_id, "--crm-backend", "salesforce", "--sf-org", "invented",
                         "--salesforce-demo-binding", str(self.binding_path), "--state-dir", str(self.state),
                         "--with-slack", "--as-of", self.binding.as_of, "--json"])
        self.assertEqual(code, 0, stderr.getvalue())
        self.assertEqual(stderr.getvalue(), "")
        credentials.assert_called_once_with("invented", environment="developer")
        result = json.loads(stdout.getvalue())
        self.assertEqual(result["status"], "accepted")
        self.assertLessEqual({"calls/call_notes/note-demo-001", "slack/messages/msg-demo-001"},
                             {unit["record_ref"] for unit in result["units"]})
        self.assertEqual(len(self.requests), 5)
        self.assertNotIn(TOKEN, stdout.getvalue())

    def test_cli_serve_labels_verified_connection_without_reading_crm_records(self):
        class InterruptQueue(SimpleQueue):
            def get(self, *args, **kwargs):
                raise KeyboardInterrupt

        for backend, bound in (("sqlite", False), ("salesforce", False), ("salesforce", True)):
            for as_json in (False, True):
                with self.subTest(backend=backend, bound=bound, as_json=as_json):
                    self.state = self.directory / f"state-{backend}-{bound}-{as_json}"
                    first_queue = True

                    def queue_factory():
                        nonlocal first_queue
                        if first_queue:
                            first_queue = False
                            return InterruptQueue()
                        return SimpleQueue()

                    arguments = ["serve", "--state-dir", str(self.state)]
                    if backend == "salesforce":
                        arguments += ["--crm-backend", "salesforce", "--sf-org", "private-invented-alias"]
                    if bound:
                        arguments += ["--salesforce-demo-binding", str(self.binding_path)]
                    if as_json:
                        arguments += ["--json"]
                    with patch.object(SalesforceReadOnlyAdapter, "from_sf", return_value=self.reader) as credentials, \
                            patch("queue.SimpleQueue", side_effect=queue_factory), \
                            redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
                        code = main(arguments)
                    self.assertEqual(code, 0, stderr.getvalue())
                    if backend == "salesforce":
                        credentials.assert_called_once_with("private-invented-alias", environment="developer")
                    else:
                        credentials.assert_not_called()
                    if as_json:
                        ready = json.loads(stdout.getvalue())
                        self.assertEqual(ready["type"], "ready")
                        self.assertEqual(ready["crm_backend"], backend)
                        self.assertEqual(ready["granted_scopes"], [])
                        self.assertEqual(ready["sources"], {
                            "crm": "salesforce" if backend == "salesforce" else "demo_fixture",
                            "calls": "demo_fixture", "slack": "demo_fixture"})
                        if bound:
                            self.assertEqual(ready["demo_kit_id"], self.binding.kit_id)
                        else:
                            self.assertNotIn("demo_kit_id", ready)
                        if backend == "salesforce":
                            self.assertEqual(ready["sf_environment"], "developer")
                            self.assertIs(ready["crm_identity_verified"], True)
                        else:
                            self.assertNotIn("sf_environment", ready)
                            self.assertNotIn("crm_identity_verified", ready)
                        self.assertIn("Connector stopped", json.loads(stderr.getvalue())["message"])
                    else:
                        self.assertEqual(stdout.getvalue(), "")
                        sources = ("mixed. CRM uses Salesforce (developer); call notes and Slack use demo fixtures. "
                                   "Connection identity is verified."
                                   if backend == "salesforce" else "CRM, call notes, and Slack use demo fixtures.")
                        self.assertIn("Sources: " + sources, stderr.getvalue())
                        self.assertEqual(stderr.getvalue().count("Sources:"), 1)
                        self.assertEqual("The bound demo kit is " + self.binding.kit_id in stderr.getvalue(), bound)
                        self.assertEqual("Connection identity is verified." in stderr.getvalue(), backend == "salesforce")
                    self.assertEqual(len(self.rows("calls", "call_notes")), 3 if bound else 30)
                    self.assertEqual(len(self.rows("slack", "messages")), 3 if bound else 29)
                    self.assertEqual((self.state / "source-profile.json").is_file(), bound)
                    if backend == "salesforce":
                        self.assert_connection_check_only()
                    else:
                        self.assertEqual(self.requests, [])
                    for secret in (TOKEN, "private-invented-alias", "fixture-demo-runtime.my.salesforce.com",
                                   self.account_id, "GTM-DEMO-001"):
                        self.assertNotIn(secret, stdout.getvalue() + stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
