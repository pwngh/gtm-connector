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
from gtm_connector.salesforce import SalesforceReadOnlyAdapter
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
            rows = [{"Id": ORGANIZATION, "OrganizationType": "Developer Edition", "Name": NAME}]
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
            rows = ([] if self.no_candidate else
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

        with patch("gtm_connector.salesforce_check.TemporaryDirectory", side_effect=temporary), \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            report = run_check(sf_org=ALIAS, as_of=AS_OF, adapter_factory=self.adapter,
                               progress=lambda *event: progress.append(event))
        self.assertEqual(stdout.getvalue(), "")
        self.assertEqual(stderr.getvalue(), "")
        self.assert_report(report)
        self.assert_progress(progress, report)
        self.assertEqual(report["status"], "passed")
        self.assertTrue(all(row["status"] == "pass" for row in report["steps"]))
        self.assertEqual([row["count"] for row in report["steps"]], [2, 1, 4, 5, 8, 7, 1, 1, 3, 1, 11])
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
        self.assertEqual(queries, ["SELECT Id, OrganizationType FROM Organization LIMIT 1"])

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
        self.assertEqual((report["api_calls"], report["sf_cli_invocations"]), (16, 4))
        search = report["steps"][6]
        self.assertEqual((search["api_calls"], search["sf_cli_invocations"]), (2, 2))

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
                    SalesforceReadOnlyAdapter, "from_sf", side_effect=lambda alias: self.adapter()) as factory, \
                    redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
                code = main(["salesforce", "check", "--sf-org", ALIAS, "--as-of", AS_OF, "--json"])
            self.assertEqual(code, expected_code)
            factory.assert_called_once_with(ALIAS)
            report = json.loads(stdout.getvalue())
            self.assert_report(report)
            self.assertEqual(report["status"], outcome)
            messages = [json.loads(line) for line in stderr.getvalue().splitlines()]
            progress = [message["progress"] for message in messages if "progress" in message]
            self.assert_progress([(event["step"], event["status"], event["count"]) for event in progress], report)
            for event in progress:
                self.assertEqual(set(event), {"command", "step", "status", "count"})
                self.assertEqual(event["command"], "salesforce check")
            if expected_code:
                self.assertEqual(len(messages), len(progress) + 1)
                error = messages[-1]["error"]
                self.assertEqual(error["code"], "check_failed")
                self.assertEqual(error["next"], "gtm-connector salesforce check --help")
            else:
                self.assertEqual(len(messages), len(progress))
            self.assert_safe(stdout.getvalue() + stderr.getvalue())

    def brief_cli(self):
        state = self.directory / "brief-state"
        with patch.object(SalesforceReadOnlyAdapter, "from_sf", side_effect=lambda alias: self.adapter()) as factory, \
                redirect_stdout(io.StringIO()) as stdout, redirect_stderr(io.StringIO()) as stderr:
            code = main(["brief", "acc-sf-" + ACCOUNT, "--crm-backend", "salesforce", "--sf-org", ALIAS,
                         "--as-of", AS_OF, "--state-dir", str(state), "--json"])
        factory.assert_called_once_with(ALIAS)
        return code, stdout.getvalue(), stderr.getvalue(), state

    def test_cli_brief_without_candidate_refuses_with_exit_three_before_drafting(self):
        self.no_candidate = True
        with patch("gtm_connector.briefing.FixtureDraftAdapter.draft", autospec=True) as draft:
            code, stdout, stderr, state = self.brief_cli()
        self.assertEqual(code, 3)
        self.assertEqual(stdout, "")
        error = json.loads(stderr)["error"]
        self.assertEqual(error["code"], "refused")
        self.assertIn("renewal_fields_unmapped", error["message"])
        self.assertEqual(error["next"], "gtm-connector salesforce check --help")
        self.assert_safe(stderr)
        draft.assert_not_called()
        self.assertFalse((state / "briefs").exists())
        self.assertEqual(len(self.requests), 5)
        self.assertEqual((self.directory / "count").read_text(), "2")

    def test_cli_brief_with_contract_returns_the_cited_three_tool_artifact(self):
        code, stdout, stderr, state = self.brief_cli()
        self.assertEqual(code, 0, stderr)
        self.assertEqual(stderr, "")
        result = json.loads(stdout)
        self.assertEqual(result["status"], "accepted")
        self.assertEqual(result["account_id"], "acc-sf-" + ACCOUNT)
        self.assertEqual(result["evidence"]["workflow_version"], 3)
        self.assertIn("Contract end date: 2027-01-31.", result["text"])
        self.assertIn("Draft contract is a planning candidate and has not been activated.", result["text"])
        receipts = result["evidence"]["receipts"]
        self.assertEqual([row["tool"] for row in receipts],
                         ["get_account", "list_opportunities", "read_call_notes"])
        self.assertIn("crm/contracts/ctr-sf-" + CONTRACT, receipts[0]["rows"])
        self.assertEqual(json.loads((state / "briefs" / (result["brief_id"] + ".json")).read_text()), result)
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
                self.assertEqual(error["next"], "gtm-connector salesforce check --help")
                self.assert_safe(stderr)
                self.assertFalse((state / "briefs").exists())


if __name__ == "__main__":
    unittest.main()
