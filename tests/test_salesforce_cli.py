"""Explicit backend selection through invented Salesforce responses and local HTTPS."""

from contextlib import redirect_stderr, redirect_stdout
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
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.audit import read_audit
from gtm_connector.cli import main
from gtm_connector.salesforce import REQUIRED_FIELDS, SalesforceReadOnlyAdapter


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

    def invoke(self, arguments, *, reader=None, add_state=True):
        selected = reader or self.reader()
        arguments = [*arguments, *(["--state-dir", str(self.state)] if add_state else [])]
        with patch.dict(os.environ, self.env, clear=True), \
                patch.object(SalesforceReadOnlyAdapter, "from_env", return_value=selected) as factory, \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(arguments)
        return code, out.getvalue(), err.getvalue(), factory

    def stop_serve(self, arguments):
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
            return self.invoke(["serve", *arguments])

    def assert_private(self, text):
        for value in (TOKEN, ORIGIN, ORG_ID, ALIAS, "Avery Example", "@example"):
            self.assertNotIn(value, text)

    def test_external_selection_requires_environment_before_creating_state(self):
        result = self.run_cli("serve", "--crm-backend", "salesforce")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("salesforce_invalid_configuration", result.stderr)
        self.assertFalse(self.state.exists())

    def test_external_brief_requires_credentials_before_model_startup(self):
        result = self.run_cli("brief", ACCOUNT, "--crm-backend", "salesforce",
                              "--adapter", "ollama", "--model", "not-installed")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("salesforce_invalid_configuration", result.stderr)
        self.assertFalse(self.state.exists())

    def test_conflicts_refuse_before_credentials_or_state(self):
        cases = [
            ["serve", "--sf-org", ALIAS], ["brief", ACCOUNT, "--sf-org", ALIAS],
            ["accounts", "--sf-org", ALIAS], ["account", ACCOUNT, "--sf-org", ALIAS],
            ["serve", "--sf-environment", "production"], ["brief", ACCOUNT, "--sf-environment", "sandbox"],
            ["accounts", "--sf-environment", "sandbox"], ["account", ACCOUNT, "--sf-environment", "production"],
            ["serve", "--sf-environment", "developer"], ["brief", ACCOUNT, "--sf-environment", "developer"],
            ["accounts", "--sf-environment", "developer"], ["account", ACCOUNT, "--sf-environment", "developer"],
            ["accounts", "--crm-backend", "salesforce", "--db", "var/v2/crm.sqlite3"],
            ["account", ACCOUNT, "--crm-backend", "salesforce", "--db", "var/v2/crm.sqlite3"],
            ["accounts", "--state-dir", str(self.state)], ["account", "acc-001", "--state-dir", str(self.state)],
        ]
        for arguments in cases:
            with self.subTest(arguments=arguments):
                code, out, err, factory = self.invoke([*arguments, "--json"], add_state=False)
                self.assertEqual(code, 2, err)
                self.assertEqual(out, "")
                self.assertEqual(json.loads(err)["error"]["code"], "usage_error")
                factory.assert_not_called()
                self.assert_private(err)
                self.assertFalse(self.state.exists())
        self.assertEqual(self.requests, [])

    def test_malformed_connected_lookup_is_usage_before_credentials(self):
        for arguments in (["account", "acc-001"], ["accounts", "--query", "x" * 201],
                          ["accounts", "--query", "bad\nquery"]):
            code, out, err, factory = self.invoke([*arguments, "--crm-backend", "salesforce", "--json"])
            self.assertEqual(code, 2, err)
            self.assertEqual(out, "")
            factory.assert_not_called()
            self.assertFalse(self.state.exists())
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
            code, out, err, factory = self.invoke(["accounts", "--query", "Redwood", "--crm-backend", "salesforce",
                                                   "--json"])
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "")
        self.assertEqual([row["id"] for row in json.loads(out)], [ACCOUNT])
        self.assertNotIn("Cedarline", out)
        factory.assert_called_once_with(environment="developer")
        self.assertEqual(grants, [["crm:read"]])
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual((receipt["tool"], receipt["status"], receipt["rows"]),
                         ("search_accounts", "ok", ["crm/accounts/" + ACCOUNT]))
        self.assertEqual(len(self.requests), 2)
        self.assertIn("LIKE '%Redwood%'", self.requests[-1].url.params["q"])

    def test_accounts_past_one_hundred_matches_show_the_first_hundred_and_say_so(self):
        notice = "Showing the first 100 accounts. More match; narrow the search with --query."
        arguments = ["accounts", "--crm-backend", "salesforce"]
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
        code, out, err, _ = self.invoke(["account", ACCOUNT, "--crm-backend", "salesforce", "--json"])
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
        code, out, err, _ = self.invoke(["account", ACCOUNT, "--crm-backend", "salesforce",
                                        "--sf-environment", "sandbox"], reader=self.reader(environment="sandbox"))
        self.assertEqual(code, 0, err)
        self.assertEqual(err.count("Sources:"), 1)
        self.assertIn("Sources: live. CRM uses Salesforce (sandbox). Connection identity is verified.", err)
        self.assertIn("Contracts\n", out)
        self.assertIn("Draft\tDraft\t2026-02-01\t2027-01-31\tunknown", out)
        self.assertIn("Industry\tunknown", out)
        self.assertNotIn("{'", out)
        self.assertNotIn("\x1b", out)
        self.assert_private(err)
        self.assertEqual(len(self.requests), 8)

    def test_human_search_does_not_invent_a_renewal(self):
        code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce"])
        self.assertEqual(code, 0, err)
        self.assertIn("Invented Redwood Workshop\tunknown\tunknown", out)
        self.assertEqual(err.count("Sources:"), 1)

    def test_production_search_checks_capabilities_once_and_preserves_json_shape(self):
        code, out, err, factory = self.invoke(
            ["accounts", "--crm-backend", "salesforce", "--sf-environment", "production", "--json"],
            reader=self.reader(environment="production"))
        self.assertEqual(code, 0, err)
        self.assertEqual(err, "")
        self.assertEqual([row["id"] for row in json.loads(out)], [ACCOUNT])
        factory.assert_called_once_with(environment="production")
        self.assertEqual(len(self.requests), 6)
        self.assertEqual(sum(request.url.path.endswith("/describe") for request in self.requests), 4)
        self.assertEqual(len(read_audit(self.state / "audit.sqlite3")), 1)

    def test_connected_inspection_uses_its_own_default_state(self):
        previous = Path.cwd()
        try:
            os.chdir(self.root)
            code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce", "--json"], add_state=False)
        finally:
            os.chdir(previous)
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out)[0]["id"], ACCOUNT)
        self.assertTrue((self.root / "var/salesforce/audit.sqlite3").is_file())
        self.assertFalse((self.root / "var/v2").exists())

    def test_missing_account_has_receipt_and_no_local_fallback(self):
        code, out, err, _ = self.invoke(["account", ACCOUNT, "--crm-backend", "salesforce", "--json"],
                                        reader=self.reader(missing=True))
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
        code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce", "--json"],
                                        reader=self.reader(failure=403))
        self.assertEqual(code, 5, err)
        self.assertEqual(out, "")
        self.assertIn("salesforce_forbidden", json.loads(err)["error"]["message"])
        self.assert_private(err)
        receipt, = read_audit(self.state / "audit.sqlite3")
        self.assertEqual(receipt["status"], "error")

    def test_environment_mismatch_is_refusal_before_record_artifacts(self):
        reader = self.reader(environment="production")
        reader._transport = httpx2.MockTransport(lambda request: self.response(request, environment="sandbox"))
        code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce", "--sf-environment", "production",
                                        "--json"], reader=reader)
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        self.assertIn("salesforce_environment_mismatch", json.loads(err)["error"]["message"])
        self.assertFalse((self.state / "audit.sqlite3").exists())
        self.assert_private(err)

    def test_identity_mismatch_keeps_existing_state_and_names_unused_recovery_folder(self):
        self.assertEqual(self.invoke(["accounts", "--crm-backend", "salesforce", "--json"])[0], 0)
        audit = (self.state / "audit.sqlite3").read_bytes()
        (self.root / "salesforce-fresh").mkdir()
        code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce", "--json"],
                                        reader=self.reader(organization="00D000000000002AAA"))
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        error = json.loads(err)["error"]
        self.assertIn("source_identity_mismatch", error["message"])
        self.assertIn("--state-dir " + str(self.root / "salesforce-fresh-2"), error["message"])
        self.assertEqual(error["next"], "gtm-connector accounts --help")
        self.assertEqual((self.state / "audit.sqlite3").read_bytes(), audit)
        self.assert_private(err)

    def test_identity_marker_write_failure_retains_safe_specific_detail(self):
        secret = "invented-private-filesystem-secret"
        with patch("gtm_connector.runtime.os.replace", side_effect=OSError(secret)):
            code, out, err, _ = self.invoke(["accounts", "--crm-backend", "salesforce", "--json"])
        self.assertEqual(code, 3, err)
        self.assertEqual(out, "")
        error = json.loads(err)["error"]
        self.assertIn("The private source markers could not be saved.", error["message"])
        self.assertIn("Check the state folder permissions", error["message"])
        self.assertIn("Repeat this command with --state-dir " + str(self.root / "salesforce-fresh"),
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
            code, out, err, env_factory = self.stop_serve(["--json", "--crm-backend", "salesforce", "--sf-org", ALIAS])
        self.assertEqual(code, 0, err)
        factory.assert_called_once_with(ALIAS, environment="developer")
        env_factory.assert_not_called()
        ready = json.loads(out)
        self.assertEqual(ready["crm_backend"], "salesforce")
        self.assertEqual(ready["sf_environment"], "developer")
        self.assertIs(ready["crm_identity_verified"], True)
        self.assertEqual(len(self.requests), 1)
        invocations = [json.loads(line) for line in log.read_text().splitlines()]
        self.assertEqual(invocations, [["org", "display", "--target-org", ALIAS, "--json"],
                                      ["org", "auth", "show-access-token", "--target-org", ALIAS, "--json"]])
        self.assert_private(out + err + (self.state / "owner.json").read_text())

    def test_serve_human_mode_identifies_verified_mixed_sources(self):
        code, out, err, _ = self.stop_serve(["--crm-backend", "salesforce"])
        self.assertEqual(code, 0, err)
        self.assertEqual(out, "")
        self.assertIn("Sources: mixed. CRM uses Salesforce (developer); call notes and Slack use demo fixtures.", err)
        self.assertIn("Connection identity is verified.", err)
        self.assertEqual(err.count("Sources:"), 1)
        self.assert_private(err)

    def test_brief_human_mode_labels_mixed_sources_once(self):
        code, out, err, _ = self.invoke(["brief", ACCOUNT, "--crm-backend", "salesforce", "--as-of", "2027-01-15"])
        self.assertEqual(code, 0, err)
        self.assertIn("2027-01-31", out)
        self.assertEqual(err.count("Sources:"), 1)
        self.assertIn("Sources: mixed.", err)
        self.assert_private(err)

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
