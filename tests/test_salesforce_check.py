"""Check reports use a fake sf executable and invented HTTP responses only."""

from contextlib import redirect_stderr, redirect_stdout
import io
import json
import math
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

import httpx2

from gtm_connector.cli import main
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from gtm_connector.salesforce_check import run_check


ORIGIN = "https://private-check-fixture.develop.my.salesforce.com"
ALIAS = "private-check-alias-never-echo"
TOKEN = "private-check-token-never-echo"
FRESH_TOKEN = "private-check-refreshed-token-never-echo"
ACCOUNT = "001000000000001AAA"
CONTACT = "003000000000001AAA"
OPPORTUNITY = "006000000000001AAA"
CONTRACT = "800000000000001AAA"
ORGANIZATION = "00D000000000001AAA"
NAME = "Invented private account name never echo"
EMAIL = "private-check-contact@invented.example"
USERNAME = "private-check-username@invented.example"
SECRET = "private upstream exception text never echo"
AS_OF = "2027-01-15"
STEPS = ["credentials", "edition", "account_fields", "contact_fields", "opportunity_fields", "contract_fields",
         "account_search", "renewal_candidate", "account_read", "opportunity_read", "brief"]
SCHEMA_FIELDS = {
    "Account": {"Id", "Name", "Industry", "AccountNumber"},
    "Contact": {"Id", "AccountId", "Name", "Title", "Email"},
    "Opportunity": {"Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"},
    "Contract": {"Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"},
}


class SalesforceCheckTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        script = self.directory / "sf"
        script.write_text(f"#!{sys.executable}\n" + f'''
import json, os, pathlib, sys
counter = pathlib.Path(__file__).with_name("count")
count = int(counter.read_text()) + 1 if counter.exists() else 1
counter.write_text(str(count))
assert sys.stdin.read() == ""
assert sys.argv[1:] in (["org", "display", "--target-org", {ALIAS!r}, "--json"],
    ["org", "auth", "show-access-token", "--target-org", {ALIAS!r}, "--json"])
for name in ("SF_DISABLE_TELEMETRY", "SF_AUTOUPDATE_DISABLE", "SF_DISABLE_LOG_FILE", "SFDX_DISABLE_LOG_FILE"):
    assert os.environ[name] == "true"
mode = os.environ["FAKE_SF_CHECK_MODE"]
if mode == "fail" or (mode == "token_failure" and sys.argv[2] == "auth"):
    print({SECRET!r}, file=sys.stderr)
    print({TOKEN!r})
    sys.exit(7)
result = {{"instanceUrl": {ORIGIN!r}, "username": {USERNAME!r}, "id": {ORGANIZATION!r}}}
if sys.argv[2] == "auth":
    result = {{"accessToken": {TOKEN!r} if count <= 2 else {FRESH_TOKEN!r}}}
print(json.dumps({{"status": 0, "result": result}}))
''')
        script.chmod(0o700)
        environment = patch.dict(os.environ, {"PATH": str(self.directory), "FAKE_SF_CHECK_MODE": "good"})
        environment.start()
        self.addCleanup(environment.stop)
        self.requests = []
        self.fail_edition = None
        self.missing_end_date = False
        self.no_candidate = False
        self.refresh_search = False
        self.refreshed = False
        self.refreshed_organization = {}
        self.fail_brief_read = None
        self.original_from_sf = SalesforceReadOnlyAdapter.from_sf

    def adapter(self):
        return self.original_from_sf(ALIAS, transport=httpx2.MockTransport(self.respond))

    def respond(self, request):
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "private-check-fixture.develop.my.salesforce.com")
        self.assertIn(request.headers["Authorization"], ("Bearer " + TOKEN, "Bearer " + FRESH_TOKEN))
        query = request.url.params.get("q", "")
        if self.refresh_search and self.refreshed and request.headers["Authorization"] == "Bearer " + TOKEN:
            return httpx2.Response(401, json=[{"message": SECRET}])
        if " FROM Organization" in query and self.fail_edition is not None:
            return httpx2.Response(self.fail_edition, json=[{"errorCode": "PRIVATE_CODE", "message": SECRET}])
        if request.url.path.endswith("/describe"):
            object_name = request.url.path.split("/")[-2]
            fields = SCHEMA_FIELDS[object_name].copy()
            if object_name == "Contract" and self.missing_end_date:
                fields.remove("EndDate")
            return httpx2.Response(200, json={"name": object_name, "label": NAME,
                                             "fields": [{"name": name} for name in sorted(fields)]})
        if " FROM Organization" in query:
            rows = [{"Id": ORGANIZATION, "OrganizationType": "Developer Edition", "IsSandbox": False, "Name": NAME}]
            if request.headers["Authorization"] == "Bearer " + FRESH_TOKEN:
                rows[0].update(self.refreshed_organization)
        elif " FROM Account" in query:
            account_reads = sum(" FROM Account WHERE Id" in r.url.params.get("q", "") for r in self.requests)
            if self.fail_brief_read is not None and " WHERE Id" in query and account_reads >= 2:
                return httpx2.Response(self.fail_brief_read, json=[{"errorCode": "PRIVATE_CODE", "message": SECRET}])
            if self.refresh_search and " WHERE Id" not in query and not self.refreshed:
                self.refreshed = True
                return httpx2.Response(401, json=[{"message": SECRET}])
            rows = [{"Id": ACCOUNT, "Name": NAME, "Industry": None, "AccountNumber": None}]
        elif " FROM Contact" in query:
            rows = [{"Id": CONTACT, "AccountId": ACCOUNT, "Name": NAME, "Title": None, "Email": EMAIL}]
        elif " FROM Contract" in query:
            self.contract_reads = getattr(self, "contract_reads", 0) + 1
            gone = self.no_candidate or self.contract_reads > getattr(self, "contract_gone_after", 99)
            rows = ([] if gone else
                    [{"Id": CONTRACT, "AccountId": ACCOUNT, "Status": "Draft", "StatusCode": "Draft",
                      "StartDate": "2026-02-01", "ContractTerm": 12, "EndDate": "2027-01-31"}])
        elif " FROM Opportunity" in query:
            rows = [{"Id": OPPORTUNITY, "AccountId": ACCOUNT, "Name": NAME, "StageName": "Closed Won",
                     "Amount": 1234.5, "CloseDate": "2027-01-31", "IsClosed": True, "IsWon": True}]
        else:
            self.fail("Unexpected fake upstream request")
        return httpx2.Response(200, json={"totalSize": len(rows), "done": True, "records": rows})

    def assert_safe(self, value):
        text = value if isinstance(value, str) else json.dumps(value)
        for secret in (ORIGIN, ALIAS, TOKEN, FRESH_TOKEN, ACCOUNT, CONTACT, OPPORTUNITY, CONTRACT,
                       ORGANIZATION, NAME, EMAIL, USERNAME, SECRET, "PRIVATE_CODE", str(self.directory)):
            self.assertNotIn(secret, text)

    def assert_report(self, report):
        self.assertEqual(report["schema_version"], 1)
        self.assertEqual(report["as_of"], AS_OF)
        self.assertEqual([step["name"] for step in report["steps"]], STEPS)
        self.assertEqual(report["api_calls"], len(self.requests))
        self.assertEqual(report["api_calls"], sum(row["api_calls"] for row in report["steps"]))
        self.assertEqual(report["sf_cli_invocations"], sum(row["sf_cli_invocations"] for row in report["steps"]))
        for row in report["steps"]:
            self.assertIn(row["status"], ("pass", "fail"))
            for field in ("count", "api_calls", "sf_cli_invocations"):
                self.assertIs(type(row[field]), int)
                self.assertGreaterEqual(row[field], 0)
            self.assertTrue(math.isfinite(row["duration_ms"]))
            self.assertGreaterEqual(row["duration_ms"], 0)
            self.assertIsInstance(row["message"], str)
            self.assertTrue(row["message"])
            self.assertEqual(bool(row["fix"]), row["status"] == "fail")
        self.assert_safe(report)

    def assert_progress(self, events, report):
        expected = []
        for row in report["steps"]:
            if row["code"] != "prerequisite_failed":
                expected.append((row["name"], "running", None))
            expected.append((row["name"], row["status"], row["count"]))
        self.assertEqual(events, expected)
        self.assert_safe(events)

    def test_complete_report_runs_real_brief_and_counts_fifteen_gets_and_two_cli_commands(self):
        created = []
        progress = []
        real_temporary_directory = tempfile.TemporaryDirectory

        def temporary(*args, **kwargs):
            directory = real_temporary_directory(*args, dir=self.directory, **kwargs)
            created.append(Path(directory.name))
            return directory

        from gtm_connector import briefing
        built, real_create_brief = [], briefing.create_brief

        def create_brief(*args, **kwargs):
            brief = real_create_brief(*args, **kwargs)
            built.append((brief["sources"], [row["tool"] for row in brief["evidence"]["receipts"]]))
            return brief

        with patch("gtm_connector.salesforce_check.TemporaryDirectory", side_effect=temporary), \
                patch("gtm_connector.briefing.create_brief", side_effect=create_brief), \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            report = run_check(sf_org=ALIAS, as_of=AS_OF, adapter_factory=self.adapter,
                               progress=lambda *event: progress.append(event))
        # The card is built from the org alone: calls are not connected, and no demo calls are read beside it.
        self.assertEqual(built, [({"crm": "salesforce", "calls": "not_connected"},
                                  ["get_account", "list_opportunities"])])
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(stderr.getvalue(), "")
        self.assert_report(report)
        self.assert_progress(progress, report)
        self.assertEqual(report["status"], "passed")
        self.assertTrue(all(row["status"] == "pass" for row in report["steps"]))
        self.assertEqual([row["count"] for row in report["steps"]], [2, 1, 4, 5, 8, 7, 1, 1, 3, 1, 4])
        self.assertEqual([row["api_calls"] for row in report["steps"]], [0, 1, 1, 1, 1, 1, 1, 1, 3, 1, 4])
        self.assertEqual([row["sf_cli_invocations"] for row in report["steps"]], [2] + [0] * 10)
        self.assertEqual((report["api_calls"], report["sf_cli_invocations"]), (15, 2))
        self.assertEqual((self.directory / "count").read_text(), "2")
        self.assertEqual(len(created), 1)
        self.assertFalse(created[0].exists())

    def test_missing_contract_end_date_names_the_field_and_blocks_data_queries(self):
        self.missing_end_date = True
        progress = []
        report = run_check(as_of=AS_OF, adapter_factory=self.adapter, progress=lambda *event: progress.append(event))
        self.assert_report(report)
        self.assert_progress(progress, report)
        self.assertEqual(report["status"], "failed")
        missing = report["steps"][5]
        self.assertEqual(missing["code"], "missing_fields")
        self.assertEqual(missing["message"], "Contract does not expose required fields: EndDate.")
        self.assertTrue(all(row["code"] == "prerequisite_failed" for row in report["steps"][6:]))
        self.assertEqual(report["api_calls"], 5)
        queries = [request.url.params["q"] for request in self.requests if "q" in request.url.params]
        self.assertEqual(queries, ["SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"])

    def test_a_candidate_whose_contract_is_gone_when_the_brief_reads_it_fails_the_brief_step(self):
        probe = run_check(as_of=AS_OF, adapter_factory=self.adapter)
        self.assertEqual(probe["status"], "passed")
        # The same run with the contract deleted before its last read, the one the brief makes.
        self.contract_gone_after, reads = self.contract_reads - 1, self.contract_reads
        self.contract_reads = 0
        report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
        self.assertEqual(self.contract_reads, reads)
        self.assertEqual(report["status"], "failed")
        self.assertEqual((report["steps"][-1]["name"], report["steps"][-1]["code"]), ("brief", "renewal_fields_unmapped"))

    def test_missing_candidate_reports_no_eligible_contract_and_blocks_dependent_reads(self):
        self.no_candidate = True
        report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
        self.assert_report(report)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["steps"][7]["code"], "renewal_fields_unmapped")
        self.assertIn("No eligible Contract has an EndDate", report["steps"][7]["message"])
        self.assertTrue(all(row["code"] == "prerequisite_failed" for row in report["steps"][8:]))
        self.assertEqual(report["api_calls"], 7)

    def test_auth_and_forbidden_failures_suppress_raw_bodies_and_count_retries(self):
        for status, code, gets, commands in ((401, "salesforce_auth_required", 2, 4),
                                             (403, "salesforce_forbidden", 1, 2)):
            with self.subTest(status=status):
                self.requests.clear()
                (self.directory / "count").unlink(missing_ok=True)
                self.fail_edition = status
                report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
                self.assert_report(report)
                self.assertEqual(report["steps"][1]["code"], code)
                self.assertEqual((report["api_calls"], report["sf_cli_invocations"]), (gets, commands))
                self.assertTrue(all(row["code"] == "prerequisite_failed" for row in report["steps"][2:]))

    def test_successful_refresh_is_attributed_to_the_step_that_needed_it(self):
        self.refresh_search = True
        report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
        self.assert_report(report)
        self.assertEqual(report["status"], "passed")
        self.assertEqual((report["api_calls"], report["sf_cli_invocations"]), (17, 4))
        search = report["steps"][6]
        self.assertEqual((search["api_calls"], search["sf_cli_invocations"]), (3, 2))

    def test_initial_refresh_is_not_retained_until_the_first_org_response_validates(self):
        responses = (
            (httpx2.Response(200, content=b"invalid private JSON"), "salesforce_invalid_response"),
            (httpx2.Response(403, json=[{"message": SECRET}]), "salesforce_forbidden"),
            (httpx2.Response(401, json=[{"message": SECRET}]), "salesforce_auth_required"),
            (httpx2.Response(200, json={"done": True, "totalSize": 1, "records": [
                {"Id": ORGANIZATION, "OrganizationType": "Developer Edition", "IsSandbox": True}]}),
             "salesforce_environment_mismatch"),
        )
        for rejected, code in responses:
            with self.subTest(code=code):
                self.requests.clear()
                (self.directory / "count").unlink(missing_ok=True)

                def respond(request):
                    self.requests.append(request)
                    self.assertEqual(request.method, "GET")
                    self.assertEqual(request.url.params["q"],
                                     "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1")
                    if request.headers["Authorization"] == "Bearer " + TOKEN:
                        return httpx2.Response(401, json=[{"message": SECRET}])
                    self.assertEqual(request.headers["Authorization"], "Bearer " + FRESH_TOKEN)
                    return rejected

                reader = self.original_from_sf(ALIAS, transport=httpx2.MockTransport(respond))
                for _ in range(2):
                    with self.assertRaises(SalesforceReadError) as failure:
                        reader.prepare_connection()
                    self.assertEqual(failure.exception.code, code)
                    self.assertEqual(reader._access_token, TOKEN)
                    self.assertIsNone(reader._organization_id)
                    self.assertFalse(reader._edition_verified)
                self.assertEqual(reader.http_requests, 4)
                self.assertEqual(reader.sf_cli_invocations, 6)

    def test_initial_refresh_is_retained_after_the_first_org_response_validates(self):
        def respond(request):
            if request.headers["Authorization"] == "Bearer " + TOKEN:
                self.requests.append(request)
                return httpx2.Response(401, json=[{"message": SECRET}])
            return self.respond(request)

        reader = self.original_from_sf(ALIAS, transport=httpx2.MockTransport(respond))
        identity = reader.prepare_connection()
        self.assertEqual(identity["organization_id"], ORGANIZATION)
        self.assertEqual(reader._access_token, FRESH_TOKEN)
        self.assertEqual(reader.http_requests, 2)
        self.assertEqual(reader.sf_cli_invocations, 4)
        self.assertEqual(len(reader.search_accounts()[0]), 1)
        self.assertEqual(reader.http_requests, 3)
        self.assertEqual(reader.sf_cli_invocations, 4)

    def test_same_origin_refresh_cannot_replace_the_pinned_org_or_environment(self):
        for change in ({"Id": "00D000000000002AAA"}, {"IsSandbox": True},
                       {"OrganizationType": "Enterprise Edition"}):
            with self.subTest(change=change):
                self.requests.clear()
                (self.directory / "count").unlink(missing_ok=True)
                self.refresh_search = False
                self.refreshed = False
                self.refreshed_organization = change
                reader = self.adapter()
                original = reader.prepare_connection()
                self.refresh_search = True
                for _ in range(2):
                    with self.assertRaises(SalesforceReadError) as failure:
                        reader.search_accounts()
                    self.assertEqual(failure.exception.code, "salesforce_auth_required")
                    self.assertEqual(reader.prepare_connection(), original)
                # Only the expired credential may attempt a record read. The new
                # credential must prove identity before it can retry that read.
                fresh = [request for request in self.requests
                         if request.headers["Authorization"] == "Bearer " + FRESH_TOKEN]
                self.assertEqual(len(fresh), 2)
                self.assertTrue(all(" FROM Organization" in request.url.params["q"] for request in fresh))
                self.assertEqual(reader.http_requests, 5)
                self.assertEqual(reader.sf_cli_invocations, 6)

    def test_failed_cli_attempts_are_preserved_without_returning_child_output(self):
        for mode, commands in (("fail", 1), ("token_failure", 2)):
            with self.subTest(mode=mode), patch.dict(os.environ, {"FAKE_SF_CHECK_MODE": mode}):
                (self.directory / "count").unlink(missing_ok=True)
                report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
                self.assert_report(report)
                self.assertEqual(report["steps"][0]["code"], "salesforce_auth_required")
                self.assertEqual((report["api_calls"], report["sf_cli_invocations"]), (0, commands))
                self.assertTrue(all(row["code"] == "prerequisite_failed" for row in report["steps"][1:]))

    def test_invalid_date_never_constructs_an_adapter_or_invokes_cli(self):
        factory = Mock()
        for value in ("2027-02-30", "20270115", "2027-01-15\n" + SECRET, "", 1, True):
            with self.subTest(date=value), self.assertRaisesRegex(ValueError, "planning date must use YYYY-MM-DD"):
                run_check(as_of=value, adapter_factory=factory)
        factory.assert_not_called()
        self.assertFalse((self.directory / "count").exists())
        self.assertEqual(self.requests, [])

    def test_unexpected_exception_text_is_replaced_with_a_fixed_diagnostic(self):
        factory = Mock(side_effect=RuntimeError(SECRET + " " + TOKEN + " " + ALIAS))
        report = run_check(as_of=AS_OF, adapter_factory=factory)
        self.assert_report(report)
        self.assertEqual(report["steps"][0]["code"], "check_failed")
        self.assertEqual(report["status"], "failed")

    def test_cli_json_stdout_contains_only_the_sanitized_report_and_stderr_has_next_command(self):
        for status, expected_code, outcome in ((None, 0, "passed"), (403, 4, "failed")):
            self.fail_edition = status
            self.requests.clear()
            (self.directory / "count").unlink(missing_ok=True)
            with self.subTest(status=status), patch.object(
                    SalesforceReadOnlyAdapter, "from_sf", side_effect=lambda alias, **options: self.adapter()) as factory, \
                    redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
                code = main(["debug", "salesforce", "check", "--sf-org", ALIAS, "--as-of", AS_OF, "--json"])
            self.assertEqual(code, expected_code)
            factory.assert_called_once_with(ALIAS, environment="developer")
            report = json.loads(stdout.getvalue())
            self.assert_report(report)
            self.assertEqual(report["status"], outcome)
            messages = [json.loads(line) for line in stderr.getvalue().splitlines()]
            progress = [message["progress"] for message in messages if "progress" in message]
            self.assert_progress([(event["step"], event["status"], event["count"]) for event in progress], report)
            for event in progress:
                self.assertEqual(set(event), {"command", "step", "status", "count"})
                self.assertEqual(event["command"], "debug salesforce check")
            if expected_code:
                self.assertEqual(len(messages), len(progress) + 1)
                error = messages[-1]["error"]
                self.assertEqual(error["code"], "check_failed")
                self.assertEqual(error["next"], "gtm-connector debug salesforce check --help")
            else:
                self.assertEqual(len(messages), len(progress))
            self.assert_safe(stdout.getvalue() + stderr.getvalue())

    def brief_cli(self):
        state = self.directory / "brief-state"
        def adapter_factory(alias, **kwargs):
            return self.adapter()

        from gtm_connector import profile
        profile.save(state, {"crm": {"provider": "salesforce", "org": ALIAS, "environment": "developer"}})
        with patch.object(SalesforceReadOnlyAdapter, "from_sf", side_effect=adapter_factory) as factory, \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["brief", "acc-sf-" + ACCOUNT, "--as-of", AS_OF, "--state-dir", str(state), "--json"])
        factory.assert_called_once_with(ALIAS, environment="developer")
        return code, stdout.getvalue(), stderr.getvalue(), state

    def test_cli_brief_without_a_contract_ahead_still_returns_a_card_that_says_so(self):
        self.no_candidate = True
        code, stdout, stderr, state = self.brief_cli()
        self.assertEqual((code, stderr), (0, ""))
        brief = json.loads(stdout)
        self.assertEqual(brief["status"], "accepted")
        self.assertIn(f"  Contract end: Missing: no contract ends on or after {AS_OF} [1]", brief["text"])
        self.assertEqual(len(list((state / "briefs").iterdir())), 1)
        self.assert_safe(stderr)

    def test_cli_brief_with_contract_returns_the_numbered_artifact_of_its_two_crm_reads(self):
        code, stdout, stderr, state = self.brief_cli()
        self.assertEqual(code, 0, stderr)
        self.assertEqual(stderr, "")
        result = json.loads(stdout)
        self.assertEqual(result["status"], "accepted")
        self.assertEqual(result["account_id"], "acc-sf-" + ACCOUNT)
        # Beside Salesforce nothing is demo data: calls and Slack are not connected, and neither is read.
        self.assertEqual(result["sources"], {"crm": "salesforce", "calls": "not_connected", "slack": "not_connected"})
        self.assertIn("  Contract end: 2027-01-31, in 16 days; status ", result["text"])
        self.assertIn("; a planning candidate, not activated [", result["text"])
        # The printed result holds the card and its units. The records and receipts stay in the private file.
        self.assertNotIn("evidence", result)
        saved = json.loads((state / "briefs" / (result["brief_id"] + ".json")).read_text())
        self.assertEqual({key: saved[key] for key in result}, result)
        receipts = saved["evidence"]["receipts"]
        self.assertEqual([row["tool"] for row in receipts], ["get_account", "list_opportunities"])
        self.assertIn("crm/contracts/ctr-sf-" + CONTRACT, receipts[0]["rows"])
        self.assertEqual(len(self.requests), 5)
        self.assertEqual((self.directory / "count").read_text(), "2")
        for private in (TOKEN, FRESH_TOKEN, ALIAS, ORIGIN, USERNAME, SECRET):
            self.assertNotIn(private, stdout)

    def test_check_preserves_safe_upstream_failure_seen_only_in_repeated_brief_read(self):
        for status, expected in ((401, "salesforce_auth_required"), (429, "salesforce_rate_limited")):
            with self.subTest(status=status):
                self.requests.clear()
                (self.directory / "count").unlink(missing_ok=True)
                self.fail_brief_read = status
                report = run_check(as_of=AS_OF, adapter_factory=self.adapter)
                self.assert_report(report)
                self.assertTrue(all(row["status"] == "pass" for row in report["steps"][:-1]))
                row = report["steps"][-1]
                self.assertEqual((row["name"], row["status"], row["code"]), ("brief", "fail", expected))
                self.assertNotIn("local HTTPS", row["fix"])
                self.assertIn("sf org login" if status == 401 else "API capacity", row["fix"])

    def test_cli_brief_preserves_upstream_code_without_private_error_text(self):
        for status, expected in ((401, "salesforce_auth_required"), (429, "salesforce_rate_limited")):
            with self.subTest(status=status):
                self.fail_edition = status
                code, stdout, stderr, state = self.brief_cli()
                self.assertEqual((code, stdout), (5, ""))
                error = json.loads(stderr)["error"]
                self.assertIn(expected, error["message"])
                self.assertEqual(error["next"], "gtm-connector debug salesforce check --help")
                self.assert_safe(stderr)
                self.assertFalse((state / "briefs").exists())


if __name__ == "__main__":
    unittest.main()
