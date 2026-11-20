"""A folder connected to Salesforce, through invented Salesforce responses and local HTTPS."""

from contextlib import nullcontext, redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import io
import json
import os
from pathlib import Path
from queue import SimpleQueue
import select
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector import profile
from gtm_connector.audit import read_audit
from gtm_connector.cli import main
from gtm_connector.salesforce import REQUIRED_FIELDS, SalesforceReadOnlyAdapter
from gtm_connector.selected_briefs import _as_of


ROOT = Path(__file__).resolve().parents[1]
ORIGIN = "https://invented-cli.my.salesforce.com"
TOKEN = "invented-cli-private-token"
ORG_ID = "00D000000000001AAA"
ACCOUNT_ID = "001000000000001AAA"
CONTACT_ID = "003000000000001AAA"
CONTRACT_ID = "800000000000001AAA"
ACCOUNT = "acc-sf-" + ACCOUNT_ID
ALIAS = "invented-private-alias"


def records(rows):
    return {"done": True, "totalSize": len(rows), "records": rows}


class SalesforceCLITests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.state = self.root / "state"
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("SALESFORCE_")}
        self.requests = []

    def run_cli(self, *arguments):
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments, "--state-dir", str(self.state)],
            cwd=ROOT, env=self.env, capture_output=True, text=True, timeout=30, check=False,
        )

    def response(self, request, *, environment="developer", missing=False, failure=None, organization=ORG_ID,
                 book=None):
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "invented-cli.my.salesforce.com")
        query = request.url.params.get("q", "")
        if " FROM Organization " in query:
            data = records([{"Id": organization, "OrganizationType": "Developer Edition"
                             if environment == "developer" else "Enterprise Edition",
                             "IsSandbox": environment == "sandbox"}])
        elif request.url.path.endswith("/describe"):
            name = request.url.path.split("/")[-2]
            data = {"name": name, "queryable": True, "fields": [{"name": key} for key in REQUIRED_FIELDS[name]]}
        elif failure:
            return httpx2.Response(failure, json=[{"errorCode": "FORBIDDEN", "message": TOKEN + ORIGIN + ALIAS}])
        elif " FROM Account " in query and book is not None:
            data = records(book[:int(query.rsplit(" LIMIT ", 1)[1])])
        elif " FROM Account " in query:
            data = records([] if missing else [{"Id": ACCOUNT_ID, "Name": "Invented Redwood Workshop",
                                               "Industry": None, "AccountNumber": "INVENTED-001"}])
        elif " FROM Contact " in query:
            data = records([{"Id": CONTACT_ID, "AccountId": ACCOUNT_ID, "Name": "Avery Example",
                             "Title": None, "Email": None}])
        elif " FROM Contract " in query:
            data = records([{"Id": CONTRACT_ID, "AccountId": ACCOUNT_ID, "Status": "Draft",
                             "StatusCode": "Draft", "StartDate": "2026-02-01", "EndDate": "2027-01-31",
                             "ContractTerm": None}])
        elif " FROM Opportunity " in query:
            data = records([])
        else:
            self.fail("Unexpected invented Salesforce request")
        return httpx2.Response(200, json=data)

    def reader(self, *, environment="developer", **options):
        return SalesforceReadOnlyAdapter(
            ORIGIN, TOKEN, environment=environment,
            transport=httpx2.MockTransport(lambda request: self.response(request, environment=environment, **options)))

    def connected(self, environment="developer", state=None):
        """Save the profile connect salesforce saves, without opening the folder."""
        profile.save(state or self.state, {"crm": {"provider": "salesforce", "org": ALIAS,
                                                   "environment": environment}})

    def snapshot(self):
        return {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}

    def invoke(self, arguments, *, reader=None, add_state=True, environment="developer", connect=True, sf=True):
        """Run one command in a folder connected to Salesforce. Every sf login returns the invented reader."""
        selected = reader or self.reader(environment=environment)
        if connect:
            self.connected(environment)
        arguments = [*arguments, *(["--state-dir", str(self.state)] if add_state else [])]
        login = patch.object(SalesforceReadOnlyAdapter, "from_sf", return_value=selected) if sf else nullcontext()
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(SalesforceReadOnlyAdapter, "from_env", side_effect=AssertionError("No environment")), \
                login as factory, \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(arguments)
        return code, out.getvalue(), err.getvalue(), factory

    def stop_serve(self, arguments, **options):
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

        with patch("queue.SimpleQueue", side_effect=queue_factory):
            return self.invoke(["serve", *arguments], **options)

    def assert_private(self, text):
        for value in (TOKEN, ORIGIN, ORG_ID, ALIAS, "Avery Example", "@example"):
            self.assertNotIn(value, text)

    def test_connect_checks_the_org_and_saves_only_its_alias_and_environment(self):
        code, out, err, factory = self.invoke(["connect", "salesforce", "--org", ALIAS, "--seller", "Morgan Reed",
                                               "--json"], connect=False)
        self.assertEqual((code, err), (0, ""))
        factory.assert_called_once_with(ALIAS, environment="developer")
        crm = {"provider": "salesforce", "org": ALIAS, "environment": "developer"}
        self.assertEqual(json.loads(out), {"schema_version": 1, "provider": "salesforce", "status": "connected",
                                          "sources": {"crm": crm}})
        saved = (self.state / "profile.json").read_text()
        self.assertEqual(json.loads(saved), {"schema_version": 1, "sources": {"crm": crm}})
        self.assertEqual(json.loads((self.state / "sellers.json").read_text())["sellers"], ["Morgan Reed"])
        for private in (TOKEN, ORIGIN, ORG_ID, "invented-cli"):
            self.assertNotIn(private, out + saved)
        # The identity check ran against the org and pinned it. No record was read.
        self.assertEqual(len(self.requests), 1)
        self.assertIn(" FROM Organization ", self.requests[0].url.params["q"])
        self.assertEqual(json.loads((self.state / "crm-connection.json").read_text())["organization_id"], ORG_ID)
        self.assertEqual(read_audit(self.state / "audit.sqlite3"), [])
        code, out, err, _ = self.invoke(["connect", "status"], connect=False)
        self.assertEqual((code, err), (0, ""))
        self.assertEqual(out, f"crm\tconnected\tSalesforce org alias {ALIAS} (developer)\n"
                              "calls\tnot connected\t\nslack\tnot connected\t\n")

    def test_after_connect_a_brief_with_no_flags_says_calls_and_slack_are_not_connected(self):
        self.assertEqual(self.invoke(["connect", "salesforce", "--org", ALIAS], connect=False)[0], 0)
        code, out, err, factory = self.invoke(["brief", ACCOUNT, "--as-of", "2027-01-15", "--json"], connect=False)
        self.assertEqual((code, err), (0, ""))
        factory.assert_called_once_with(ALIAS, environment="developer")
        brief = json.loads(out)
        self.assertEqual(brief["sources"], {"crm": "salesforce", "calls": "not_connected", "slack": "not_connected"})
        lines = brief["text"].splitlines()
        self.assertIn("  Calls: not connected.", lines)
        self.assertIn("  Slack: not connected.", lines)
        self.assertEqual([row["tool"] for row in read_audit(self.state / "audit.sqlite3", brief["brief_id"])],
                         ["get_account", "list_opportunities"])
        for demo in ("calls.sqlite3", "slack.sqlite3"):
            self.assertFalse((self.state / demo).exists())
        # Detached, the folder is not connected again. Its files stay.
        code, out, err, _ = self.invoke(["connect", "salesforce", "--remove", "--json"], connect=False)
        self.assertEqual((code, json.loads(out)["sources"]), (0, {}))
        code, out, err, factory = self.invoke(["brief", ACCOUNT, "--json"], connect=False)
        self.assertEqual((code, out), (2, ""))
        self.assertEqual(json.loads(err)["error"]["message"], str(profile.NotConnected()))
        factory.assert_not_called()
        self.assertTrue((self.state / "crm-connection.json").is_file())

    def test_a_connect_that_fails_its_check_saves_no_profile(self):
        from gtm_connector.salesforce import SalesforceReadError
        wrong = self.reader(environment="production")
        wrong._transport = httpx2.MockTransport(lambda request: self.response(request, environment="sandbox"))
        arguments = ["connect", "salesforce", "--org", ALIAS, "--seller", "Morgan Reed", "--json"]
        code, out, err, _ = self.invoke([*arguments, "--environment", "production"], reader=wrong, connect=False)
        self.assertEqual((code, out), (3, ""))
        self.assertIn("salesforce_environment_mismatch", json.loads(err)["error"]["message"])
        self.assert_private(err)
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(SalesforceReadOnlyAdapter, "from_sf",
                             side_effect=SalesforceReadError("salesforce_auth_required")), \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main([*arguments, "--state-dir", str(self.state)])
        self.assertEqual((code, out.getvalue()), (5, ""))
        error = json.loads(err.getvalue())["error"]
        self.assertIn("salesforce_auth_required", error["message"])
        self.assertEqual(error["next"], "gtm-connector salesforce check --help")
        self.assert_private(err.getvalue())
        for name in ("profile.json", "sellers.json", "crm-connection.json", "audit.sqlite3"):
            self.assertFalse((self.state / name).exists(), name)

    def test_connect_without_the_sf_cli_fails_before_creating_state(self):
        empty = self.root / "empty-path"
        empty.mkdir()
        self.env["PATH"] = str(empty)
        result = self.run_cli("connect", "salesforce", "--org", ALIAS, "--json")
        self.assertEqual((result.returncode, result.stdout), (5, ""))
        self.assertIn("salesforce_auth_required", json.loads(result.stderr)["error"]["message"])
        self.assert_private(result.stderr)
        self.assertFalse(self.state.exists())

    def test_a_connected_brief_reads_the_login_before_creating_state(self):
        empty = self.root / "empty-path"
        empty.mkdir()
        self.env["PATH"] = str(empty)
        self.connected()
        result = self.run_cli("brief", ACCOUNT, "--json")
        self.assertEqual((result.returncode, result.stdout), (5, ""))
        self.assertIn("salesforce_auth_required", json.loads(result.stderr)["error"]["message"])
        self.assertEqual({path.name for path in self.state.iterdir()}, {"profile.json"})

    def test_salesforce_replaces_an_unused_demo_profile_and_refuses_a_folder_that_read_demo_data(self):
        profile.save(self.state, profile.DEMO)
        code, out, err, _ = self.invoke(["connect", "salesforce", "--org", ALIAS, "--json"], connect=False)
        self.assertEqual(code, 0, err)
        self.assertEqual(set(json.loads(out)["sources"]), {"crm"})
        self.state = self.root / "demo-state"
        profile.save(self.state, profile.DEMO)
        with patch.dict(os.environ, self.env, clear=True), redirect_stdout(io.StringIO()), \
                redirect_stderr(io.StringIO()) as err:
            self.assertEqual(main(["brief", "acc-003", "--json", "--state-dir", str(self.state)]), 0, err.getvalue())
        before = self.snapshot()
        code, out, err, _ = self.invoke(["connect", "salesforce", "--org", ALIAS, "--json"], connect=False)
        self.assertEqual((code, out), (3, ""))
        message = json.loads(err)["error"]["message"]
        self.assertIn("source_identity_mismatch", message)
        self.assertIn("Repeat this command with --state-dir " + str(self.root / "demo-state-fresh"), message)
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(profile.load(self.state), profile.DEMO)

    def test_another_org_cannot_be_connected_over_a_folder_that_belongs_to_the_first(self):
        self.assertEqual(self.invoke(["connect", "salesforce", "--org", ALIAS], connect=False)[0], 0)
        saved = (self.state / "profile.json").read_bytes()
        code, out, err, _ = self.invoke(["connect", "salesforce", "--org", "another-invented-alias", "--json"],
                                        reader=self.reader(organization="00D000000000002AAA"), connect=False)
        self.assertEqual((code, out), (3, ""))
        self.assertIn("source_identity_mismatch", json.loads(err)["error"]["message"])
        self.assertEqual((self.state / "profile.json").read_bytes(), saved)

    def test_malformed_connected_lookup_is_usage_before_credentials(self):
        for arguments in (["account", "acc-001"], ["accounts", "--query", "x" * 201],
                          ["accounts", "--query", "bad\nquery"]):
            code, out, err, factory = self.invoke([*arguments, "--json"])
            self.assertEqual(code, 2, err)
            self.assertEqual(out, "")
            factory.assert_not_called()
            self.assertEqual({path.name for path in self.state.iterdir()}, {"profile.json"})
        self.assertEqual(self.requests, [])

    def test_local_client_allows_the_upstream_operation_to_finish(self):
        from gtm_connector.client import LocalClient
        from gtm_connector.runtime import Runtime
        from gtm_connector.salesforce import _OPERATION_SECONDS
        with Runtime(self.state, crm_reader=self.reader()) as runtime, LocalClient(runtime.config) as client:
            self.assertGreater(client._http.timeout.read, _OPERATION_SECONDS)
            self.assertEqual(client._http.timeout.connect, 10)
        self.assertEqual(len(self.requests), 1)

    def test_search_uses_only_crm_read_and_finalizes_one_receipt(self):
        from gtm_connector.client import LocalClient
        original = LocalClient.authorize
        grants = []
        def authorize(client, scopes):
            grants.append(scopes)
            return original(client, scopes)
        with patch.object(LocalClient, "authorize", authorize):
            code, out, err, factory = self.invoke(["accounts", "--query", "Redwood", "--json"])
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "")
        self.assertEqual([row["id"] for row in json.loads(out)], [ACCOUNT])
        self.assertNotIn("Cedarline", out)
        factory.assert_called_once_with(ALIAS, environment="developer")
        self.assertEqual(grants, [["crm:read"]])
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual((receipt["tool"], receipt["status"], receipt["rows"]),
                         ("search_accounts", "ok", ["crm/accounts/" + ACCOUNT]))
        self.assertEqual(len(self.requests), 2)
        self.assertIn("LIKE '%Redwood%'", self.requests[-1].url.params["q"])

    def test_accounts_past_one_hundred_matches_show_the_first_hundred_and_say_so(self):
        notice = "Showing the first 100 accounts. More match; narrow the search with --query."
        arguments = ["accounts"]
        for matching in (100, 101, 250):
            book = [{"Id": f"001{number:012d}AAA", "Name": f"Invented account {number:03d}", "Industry": None,
                     "AccountNumber": None} for number in range(matching)]
            first = [{"id": "acc-sf-" + row["Id"], "source_id": row["Id"], "backend": "salesforce",
                      "name": row["Name"], "industry": None, "account_number": None} for row in book[:100]]
            with self.subTest(matching=matching):
                self.state = self.root / f"state-{matching}"
                code, out, err, _ = self.invoke([*arguments, "--json"], reader=self.reader(book=book))
                self.assertEqual((code, err), (0, ""))
                listed = {"accounts": first, "has_more": True} if matching > 100 else first
                self.assertEqual(out, json.dumps(listed, indent=2, ensure_ascii=False) + "\n")
                code, out, err, _ = self.invoke(arguments, reader=self.reader(book=book))
                self.assertEqual(code, 0, err)
                self.assertEqual(out, "".join(f"{row['id']}\t{row['name']}\tunknown\tunknown\n" for row in first))
                # The source label is the only other line on stderr.
                self.assertEqual(err.splitlines()[1:], [notice] if matching > 100 else [])
                receipts = read_audit(self.state / "audit.sqlite3")
                self.assertEqual([receipt["row_count"] for receipt in receipts], [100, 100])

    def test_account_json_groups_contacts_and_contracts_without_new_tool(self):
        code, out, err, _ = self.invoke(["account", ACCOUNT, "--json"])
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "")
        account = json.loads(out)
        self.assertEqual(account["id"], ACCOUNT)
        self.assertIsNone(account["industry"])
        self.assertEqual(account["contacts"][0]["id"], "con-sf-" + CONTACT_ID)
        self.assertEqual(account["contracts"][0]["end_date"], "2027-01-31")
        self.assertIsNone(account["contracts"][0]["contract_term"])
        self.assertNotIn("record_ref", out)
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual((receipt["tool"], receipt["status"], len(receipt["rows"])), ("get_account", "ok", 3))
        self.assertEqual(len(self.requests), 4)

    def test_human_account_formats_contracts_and_unknown_values(self):
        code, out, err, _ = self.invoke(["account", ACCOUNT], environment="sandbox")
        self.assertEqual(code, 0, err)
        self.assertEqual(err.count("Sources:"), 1)
        self.assertIn("Sources: CRM is Salesforce (sandbox), identity verified.", err)
        self.assertIn("Contracts\n", out)
        self.assertIn("Draft\tDraft\t2026-02-01\t2027-01-31\tunknown", out)
        self.assertIn("Industry\tunknown", out)
        self.assertNotIn("{'", out)
        self.assertNotIn("\x1b", out)
        self.assert_private(err)
        self.assertEqual(len(self.requests), 8)

    def test_human_search_does_not_invent_a_renewal(self):
        code, out, err, _ = self.invoke(["accounts"])
        self.assertEqual(code, 0, err)
        self.assertIn("Invented Redwood Workshop\tunknown\tunknown", out)
        self.assertEqual(err.count("Sources:"), 1)

    def test_production_search_checks_capabilities_once_and_preserves_json_shape(self):
        code, out, err, factory = self.invoke(["accounts", "--json"], environment="production")
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "")
        self.assertEqual([row["id"] for row in json.loads(out)], [ACCOUNT])
        factory.assert_called_once_with(ALIAS, environment="production")
        self.assertEqual(len(self.requests), 6)
        self.assertEqual(sum(request.url.path.endswith("/describe") for request in self.requests), 4)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 1)

    def test_connected_inspection_uses_the_one_default_state_folder(self):
        self.connected(state=self.root / "var/v2")
        previous = Path.cwd()
        try:
            os.chdir(self.root)
            code, out, err, _ = self.invoke(["accounts", "--json"], add_state=False, connect=False)
        finally:
            os.chdir(previous)
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out)[0]["id"], ACCOUNT)
        self.assertTrue((self.root / "var/v2/audit.sqlite3").is_file())
        self.assertEqual([path.name for path in (self.root / "var").iterdir()], ["v2"])

    def test_missing_account_has_receipt_and_no_local_fallback(self):
        code, out, err, _ = self.invoke(["account", ACCOUNT, "--json"], reader=self.reader(missing=True))
        self.assertEqual(code, 2, err)
        self.assertEqual(out, "")
        error = json.loads(err)["error"]
        self.assertIn("account not found", error["message"])
        self.assertEqual(error["next"], "gtm-connector accounts --help")
        self.assertNotIn(ACCOUNT_ID, err)
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual(receipt["status"], "error")
        self.assertEqual(len(self.requests), 2)

    def test_tool_upstream_error_is_fixed_and_audited(self):
        code, out, err, _ = self.invoke(["accounts", "--json"], reader=self.reader(failure=403))
        self.assertEqual(code, 5, err)
        self.assertEqual(out, "")
        self.assertIn("salesforce_forbidden", json.loads(err)["error"]["message"])
        self.assert_private(err)
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual(receipt["status"], "error")

    def test_environment_mismatch_is_refusal_before_record_artifacts(self):
        reader = self.reader(environment="production")
        reader._transport = httpx2.MockTransport(lambda request: self.response(request, environment="sandbox"))
        code, out, err, _ = self.invoke(["accounts", "--json"], reader=reader, environment="production")
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        self.assertIn("salesforce_environment_mismatch", json.loads(err)["error"]["message"])
        self.assertFalse((self.state / "audit.sqlite3").exists())
        self.assert_private(err)

    def test_identity_mismatch_keeps_existing_state_and_names_unused_recovery_folder(self):
        self.assertEqual(self.invoke(["accounts", "--json"])[0], 0)
        audit = (self.state / "audit.sqlite3").read_bytes()
        (self.root / "state-fresh").mkdir()
        code, out, err, _ = self.invoke(["accounts", "--json"], reader=self.reader(organization="00D000000000002AAA"))
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        error = json.loads(err)["error"]
        self.assertIn("source_identity_mismatch", error["message"])
        self.assertIn("Connect the sources again with --state-dir " + str(self.root / "state-fresh-2"),
                      error["message"])
        self.assertEqual(error["next"], "gtm-connector accounts --help")
        self.assertEqual((self.state / "audit.sqlite3").read_bytes(), audit)
        self.assert_private(err)

    def test_identity_marker_write_failure_retains_safe_specific_detail(self):
        secret = "invented-private-filesystem-secret"
        self.connected()
        with patch("gtm_connector.runtime.os.replace", side_effect=OSError(secret)):
            code, out, err, _ = self.invoke(["accounts", "--json"], connect=False)
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        error = json.loads(err)["error"]
        self.assertIn("The private source markers could not be saved.", error["message"])
        self.assertIn("Check the state folder permissions", error["message"])
        self.assertIn("Connect the sources again with --state-dir " + str(self.root / "state-fresh"),
                      error["message"])
        self.assertEqual(error["next"], "gtm-connector accounts --help")
        self.assertNotIn(secret, err)
        self.assert_private(err)
        self.assertFalse((self.state / "crm-connection.json").exists())
        self.assertFalse((self.state / "audit.sqlite3").exists())
        self.assertFalse((self.state / "crm.sqlite3").exists())
        self.assertEqual(len(self.requests), 1)

    def test_serve_captures_fake_sf_and_verifies_before_printing_public_ready(self):
        binary = self.root / "sf"
        log = self.root / "invocations.jsonl"
        binary.write_text(f"#!{sys.executable}\n" + '''import json, os, sys
with open(os.environ["FAKE_SF_LOG"], "a") as stream:
    stream.write(json.dumps(sys.argv[1:]) + "\\n")
field = "instanceUrl" if sys.argv[1:3] == ["org", "display"] else "accessToken"
value = "https://invented-cli.my.salesforce.com" if field == "instanceUrl" else "invented-cli-private-token"
print(json.dumps({"status": 0, "result": {field: value}}))
''')
        binary.chmod(0o700)
        self.env.update(PATH=str(self.root) + os.pathsep + os.defpath, FAKE_SF_LOG=str(log))
        original = SalesforceReadOnlyAdapter.from_sf
        def from_sf(alias, *, environment):
            return original(alias, environment=environment, transport=httpx2.MockTransport(self.response))
        with patch.object(SalesforceReadOnlyAdapter, "from_sf", side_effect=from_sf) as factory:
            code, out, err, _ = self.stop_serve(["--json"], sf=False)
        self.assertEqual(code, 0, err)
        factory.assert_called_once_with(ALIAS, environment="developer")
        ready = json.loads(out)
        self.assertEqual(ready["crm_backend"], "salesforce")
        self.assertEqual(ready["sf_environment"], "developer")
        self.assertIs(ready["crm_identity_verified"], True)
        self.assertEqual(len(self.requests), 1)
        invocations = [json.loads(line) for line in log.read_text().splitlines()]
        self.assertEqual(invocations, [["org", "display", "--target-org", ALIAS, "--json"],
                                      ["org", "auth", "show-access-token", "--target-org", ALIAS, "--json"]])
        self.assert_private(out + err + (self.state / "owner.json").read_text())

    def test_serve_human_mode_names_the_verified_org_and_what_is_not_connected(self):
        code, out, err, _ = self.stop_serve([])
        self.assertEqual(code, 0, err)
        self.assertEqual(out, "")
        self.assertIn("Sources: CRM is Salesforce (developer), identity verified; calls are not connected; "
                      "Slack is not connected.", err)
        self.assertNotIn("demo", err)
        self.assertEqual(err.count("Sources:"), 1)
        self.assert_private(err)

    def test_brief_human_mode_names_its_sources_once(self):
        code, out, err, _ = self.invoke(["brief", ACCOUNT, "--as-of", "2027-01-15"])
        self.assertEqual(code, 0, err)
        self.assertIn("2027-01-31", out)
        self.assertEqual(err.count("Sources:"), 1)
        self.assertIn("CRM is Salesforce (developer), identity verified; calls are not connected", err)
        self.assert_private(err)

    def test_the_default_planning_day_is_the_utc_day_in_any_time_zone(self):
        # At any instant one of these two zones is on another calendar day than UTC.
        self.addCleanup(time.tzset)
        for zone in ("Pacific/Kiritimati", "Pacific/Pago_Pago"):
            with self.subTest(zone=zone), patch.dict(os.environ, {"TZ": zone}):
                time.tzset()
                days = {datetime.now(timezone.utc).date().isoformat()}
                code, out, err, _ = self.invoke(["brief", ACCOUNT])
                days.add(datetime.now(timezone.utc).date().isoformat())
                self.assertEqual(code, 0, err)
                self.assertIn(out.splitlines()[0].rsplit(" as of ", 1)[1], days)
                # The assistant's brief tool takes the same day.
                self.assertIn(_as_of(None, "salesforce"), days)

    def test_check_passes_environment_without_changing_report_or_exit_contract(self):
        report = {"status": "failed", "steps": []}
        with patch("gtm_connector.salesforce_check.run_check", return_value=report) as check, \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(["salesforce", "check", "--sf-environment", "production", "--sf-org", ALIAS, "--json"])
        self.assertEqual(code, 4)
        self.assertEqual(json.loads(out.getvalue()), report)
        self.assertEqual(check.call_args.kwargs["environment"], "production")
        self.assertEqual(check.call_args.kwargs["sf_org"], ALIAS)
        self.assert_private(out.getvalue() + err.getvalue())

    def test_default_stays_offline_with_external_credentials_in_environment(self):
        self.env.update(SALESFORCE_INSTANCE_URL=ORIGIN, SALESFORCE_ACCESS_TOKEN=TOKEN)
        profile.save(self.state, profile.DEMO)
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--state-dir", str(self.state)],
            cwd=ROOT, env=self.env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            readable, _, _ = select.select([process.stdout], [], [], 20)
            self.assertTrue(readable)
            ready = json.loads(process.stdout.readline())
            self.assertEqual(ready["crm_backend"], "sqlite")
            self.assertNotIn("sf_environment", ready)
            self.assertNotIn("crm_identity_verified", ready)
            process.send_signal(signal.SIGINT)
            out, err = process.communicate(timeout=15)
            self.assertEqual(process.returncode, 0, err)
            self.assertEqual(out, "")
            self.assert_private(json.dumps(ready) + err + (self.state / "owner.json").read_text())
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)
            process.stdout.close()
            process.stderr.close()


if __name__ == "__main__":
    unittest.main()
