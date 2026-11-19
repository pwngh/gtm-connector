"""Selected Salesforce environments use invented HTTP responses, never an org."""

from copy import deepcopy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import httpx2

from gtm_connector.briefing import create_brief
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from gtm_connector.salesforce_check import run_check


ORIGIN = "https://invented-connection.my.salesforce.com"
TOKEN = "invented-connection-secret-never-echo"
ORGANIZATION = "00D000000000001AAA"
ACCOUNT = "001000000000001AAA"
ACCOUNT_ID = "acc-sf-" + ACCOUNT
AS_OF = "2027-01-15"
QUERY_PATH = "/services/data/v68.0/query"
ORG_QUERY = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"
FIELDS = {
    "Account": {"Id", "Name", "Industry", "AccountNumber"},
    "Contact": {"Id", "AccountId", "Name", "Title", "Email"},
    "Opportunity": {"Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"},
    "Contract": {"Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"},
}


class SalesforceEnvironmentTests(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.organization = {"Id": ORGANIZATION, "OrganizationType": "Enterprise Edition", "IsSandbox": False}
        self.describes = {name: {"name": name, "queryable": True,
                                "fields": [{"name": field} for field in sorted(fields)]}
                          for name, fields in FIELDS.items()}
        self.fail_request = None

    def respond(self, request):
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "invented-connection.my.salesforce.com")
        self.assertEqual(request.headers["Authorization"], "Bearer " + TOKEN)
        self.assertEqual(request.headers["accept-encoding"], "identity")
        if self.fail_request is not None:
            response = self.fail_request(request)
            if response is not None:
                return response
        if request.url.path.endswith("/describe"):
            name = request.url.path.split("/")[-2]
            self.assertIn(name, FIELDS)
            self.assertEqual(request.url.path, f"/services/data/v68.0/sobjects/{name}/describe")
            self.assertFalse(request.url.query)
            return httpx2.Response(200, json=self.describes[name])
        self.assertEqual(request.url.path, QUERY_PATH)
        query = request.url.params["q"]
        if " FROM Organization" in query:
            self.assertEqual(query, ORG_QUERY)
            rows = [deepcopy(self.organization)]
        elif " FROM Account" in query:
            rows = [{"Id": ACCOUNT, "Name": "Invented connected account", "Industry": "Education",
                     "AccountNumber": "INVENTED-ACCOUNT-001"}]
        elif " FROM Contact" in query:
            rows = [{"Id": "003000000000001AAA", "AccountId": ACCOUNT, "Name": "Invented account contact",
                     "Title": None, "Email": "contact@invented.example"}]
        elif " FROM Opportunity" in query:
            rows = [{"Id": "006000000000001AAA", "AccountId": ACCOUNT, "Name": "Invented renewal opportunity",
                     "StageName": "Review", "Amount": 1000, "CloseDate": "2027-01-31",
                     "IsClosed": False, "IsWon": False}]
        elif " FROM Contract" in query:
            rows = [{"Id": "800000000000001AAA", "AccountId": ACCOUNT, "Status": "Draft", "StatusCode": "Draft",
                     "StartDate": "2026-02-01", "ContractTerm": 12, "EndDate": "2027-01-31"}]
        else:
            self.fail("Unexpected fixed query")
        return httpx2.Response(200, json={"done": True, "totalSize": len(rows), "records": rows})

    def adapter(self, environment="production"):
        return SalesforceReadOnlyAdapter(ORIGIN, TOKEN, environment=environment,
                                         transport=httpx2.MockTransport(self.respond))

    def assert_error(self, code, operation):
        with self.assertRaises(SalesforceReadError) as failure:
            operation()
        self.assertEqual(failure.exception.code, code)
        self.assertEqual(str(failure.exception), code)
        for secret in (ORIGIN, TOKEN, ORGANIZATION, ACCOUNT):
            self.assertNotIn(secret, str(failure.exception))

    def assert_metadata_only(self):
        for request in self.requests:
            self.assertTrue(request.url.path.endswith("/describe") or request.url.params.get("q") == ORG_QUERY)

    def test_environment_is_explicit_and_invalid_selection_never_invokes_sf_or_http(self):
        with patch("gtm_connector.salesforce.SFCredentials") as credentials, \
                patch("gtm_connector.salesforce.httpx2.Client") as client:
            default = SalesforceReadOnlyAdapter(ORIGIN, TOKEN)
            self.assertEqual(default.environment, "developer")
            for environment in (None, "", "unknown", "Production", " production", [], False):
                with self.subTest(environment=environment):
                    self.assert_error("salesforce_invalid_configuration",
                                      lambda: SalesforceReadOnlyAdapter(ORIGIN, TOKEN, environment=environment))
                    self.assert_error(
                        "salesforce_invalid_configuration",
                        lambda: SalesforceReadOnlyAdapter.from_sf("invented-alias", environment=environment),
                    )
            credentials.assert_not_called()
            client.assert_not_called()

    def test_environment_token_factory_preserves_explicit_selection_without_using_cli_defaults(self):
        with patch.dict("gtm_connector.salesforce.os.environ",
                        {"SALESFORCE_INSTANCE_URL": ORIGIN, "SALESFORCE_ACCESS_TOKEN": TOKEN}), \
                patch("gtm_connector.salesforce.SFCredentials") as credentials:
            reader = SalesforceReadOnlyAdapter.from_env(environment="sandbox")
        self.assertEqual(reader.environment, "sandbox")
        self.assertEqual(reader.instance_url, ORIGIN)
        credentials.assert_not_called()

    def test_developer_preparation_is_one_metadata_read_and_returns_a_private_identity_copy(self):
        self.organization.update(OrganizationType="Developer Edition", IsSandbox=False)
        reader = self.adapter("developer")
        expected = {"schema_version": 1, "backend": "salesforce", "instance_url": ORIGIN,
                    "organization_id": ORGANIZATION, "environment": "developer"}
        identity = reader.prepare_connection()
        self.assertEqual(identity, expected)
        identity["organization_id"] = "changed"
        self.assertEqual(reader.prepare_connection(), expected)
        self.assertEqual(len(self.requests), 1)
        self.assertEqual(self.requests[0].url.params["q"], ORG_QUERY)

    def test_supported_api_responses_use_capabilities_not_an_edition_allowlist(self):
        for edition in ("Enterprise Edition", "Unlimited Edition", "Performance Edition", "Professional Edition",
                        "Invented future edition"):
            for environment in ("sandbox", "production"):
                with self.subTest(edition=edition, environment=environment):
                    self.requests.clear()
                    self.organization.update(OrganizationType=edition, IsSandbox=environment == "sandbox")
                    reader = self.adapter(environment)
                    identity = reader.prepare_connection()
                    self.assertEqual(identity["environment"], environment)
                    self.assertEqual(identity["organization_id"], ORGANIZATION)
                    self.assertEqual(reader.http_requests, 5)
                    self.assertEqual(reader.get_account(ACCOUNT_ID)["id"], ACCOUNT_ID)
                    self.assertEqual(reader.http_requests, 8)
                    self.assertEqual(len(reader.list_opportunities(ACCOUNT_ID)), 1)
                    self.assertEqual(reader.http_requests, 9)
                    self.assertEqual(len(reader.search_accounts("Invented")[0]), 1)
                    self.assertEqual(reader.http_requests, 10)
                    self.assertEqual(sum(r.url.path.endswith("/describe") for r in self.requests), 4)
                    self.assertEqual(sum(r.url.params.get("q") == ORG_QUERY for r in self.requests), 1)

    def test_cold_direct_read_cannot_skip_capabilities_and_does_not_repeat_them(self):
        reader = self.adapter()
        reader.search_accounts()
        self.assertEqual(reader.http_requests, 6)
        reader.search_accounts()
        self.assertEqual(reader.http_requests, 7)
        self.assertEqual(sum(r.url.path.endswith("/describe") for r in self.requests), 4)

    def test_environment_mismatches_and_malformed_metadata_never_read_records(self):
        cases = (
            ("developer", {"OrganizationType": "Enterprise Edition"}, "salesforce_edition_required"),
            ("developer", {"OrganizationType": "Developer Edition", "IsSandbox": True},
             "salesforce_environment_mismatch"),
            ("sandbox", {"IsSandbox": False}, "salesforce_environment_mismatch"),
            ("production", {"IsSandbox": True}, "salesforce_environment_mismatch"),
            ("production", {"OrganizationType": "Developer Edition"}, "salesforce_environment_mismatch"),
        )
        original = deepcopy(self.organization)
        for environment, changes, code in cases:
            with self.subTest(environment=environment, changes=changes):
                self.requests.clear()
                self.organization = {**original, **changes}
                self.assert_error(code, self.adapter(environment).search_accounts)
                self.assertEqual(len(self.requests), 1)
                self.assert_metadata_only()
        for environment in ("developer", "sandbox", "production"):
            for field, value in (("IsSandbox", None), ("IsSandbox", "false"), ("IsSandbox", 0),
                                 ("Id", "001000000000001AAA"), ("Id", "short")):
                self.organization = {**original, "OrganizationType": "Developer Edition"
                                     if environment == "developer" else "Enterprise Edition",
                                     "IsSandbox": environment == "sandbox", field: value}
                with self.subTest(environment=environment, field=field, value=value):
                    self.assert_error("salesforce_invalid_response", self.adapter(environment).prepare_connection)
            for field in ("IsSandbox", "Id"):
                self.organization = {**original, "OrganizationType": "Developer Edition"
                                     if environment == "developer" else "Enterprise Edition"}
                del self.organization[field]
                self.assert_error("salesforce_invalid_response", self.adapter(environment).prepare_connection)
        for edition in (None, "", "   ", "Enterprise Edition\n", 1):
            self.organization = {**original, "OrganizationType": edition}
            self.assert_error("salesforce_invalid_response", self.adapter().prepare_connection)

    def test_failed_capability_preparation_does_not_cache_partial_schema_or_read_records(self):
        self.describes["Contract"]["fields"] = [{"name": field} for field in sorted(FIELDS["Contract"] - {"EndDate"})]
        reader = self.adapter()
        self.assert_error("salesforce_schema_unavailable", reader.prepare_connection)
        self.assertEqual(reader.http_requests, 5)
        self.assert_metadata_only()
        self.describes["Contract"]["fields"].append({"name": "EndDate"})
        reader.prepare_connection()
        self.assertEqual(reader.http_requests, 9)
        self.assertEqual(sum(r.url.params.get("q") == ORG_QUERY for r in self.requests), 1)
        reader.search_accounts()
        self.assertEqual(reader.http_requests, 10)

    def test_nonqueryable_or_malformed_object_schema_is_not_an_enabled_capability(self):
        original = deepcopy(self.describes["Account"])
        for changes, code in (({"queryable": False}, "salesforce_forbidden"),
                              ({"queryable": None}, "salesforce_invalid_response"),
                              ({"queryable": "true"}, "salesforce_invalid_response"),
                              ({"name": "Contact"}, "salesforce_invalid_response")):
            with self.subTest(changes=changes):
                self.requests.clear()
                self.describes["Account"] = {**original, **changes}
                self.assert_error(code, self.adapter().search_accounts)
                self.assertEqual(len(self.requests), 2)
                self.assert_metadata_only()

    def test_api_disabled_permission_and_later_permission_loss_have_fixed_separate_errors(self):
        for upstream, code in (("API_DISABLED_FOR_ORG", "salesforce_api_disabled"),
                               ("INSUFFICIENT_ACCESS", "salesforce_forbidden"),
                               ("REQUEST_LIMIT_EXCEEDED", "salesforce_rate_limited")):
            self.fail_request = lambda request, value=upstream: httpx2.Response(
                403, json=[{"errorCode": value, "message": TOKEN + " " + ORIGIN}])
            with self.subTest(upstream=upstream):
                self.assert_error(code, self.adapter().prepare_connection)
        self.fail_request = None
        reader = self.adapter()
        reader.prepare_connection()
        self.fail_request = lambda request: httpx2.Response(403, json=[{"errorCode": "INSUFFICIENT_ACCESS",
                                                                      "message": TOKEN}])
        before = reader.http_requests
        self.assert_error("salesforce_forbidden", reader.search_accounts)
        self.assertEqual(reader.http_requests, before + 1)

    def test_capability_reads_and_data_share_the_original_operation_request_budget(self):
        reader = self.adapter()
        with patch("gtm_connector.salesforce._MAX_REQUESTS", 5):
            self.assert_error("salesforce_result_limit", reader.search_accounts)
        self.assertEqual(reader.http_requests, 5)
        self.assert_metadata_only()

    def test_capability_descriptions_share_the_operation_byte_and_time_bounds(self):
        for description in self.describes.values():
            description["unusedPadding"] = "x" * 300_000
        reader = self.adapter()
        self.assert_error("salesforce_result_limit", reader.prepare_connection)
        self.assertEqual(reader.http_requests, 5)
        self.assert_metadata_only()
        for description in self.describes.values():
            del description["unusedPadding"]
        now = [0.0]

        def expire(request):
            if request.url.path.endswith("/Contact/describe"):
                now[0] = 21.0
            return None

        self.fail_request = expire
        reader = self.adapter()
        with patch("gtm_connector.salesforce.time.monotonic", side_effect=lambda: now[0]):
            self.assert_error("salesforce_timeout", reader.prepare_connection)
        self.assertEqual(reader.http_requests, 3)

    def test_explicit_schema_check_warms_preparation_without_repeating_describes(self):
        reader = self.adapter()
        reader.check_edition()
        for name, expected in FIELDS.items():
            self.assertEqual(reader.describe_fields(name), expected)
        self.assertEqual(reader.http_requests, 5)
        reader.prepare_connection()
        self.assertEqual(reader.http_requests, 5)

    def test_paid_environment_brief_uses_real_local_mcp_and_nine_total_gets(self):
        for environment in ("sandbox", "production"):
            with self.subTest(environment=environment), tempfile.TemporaryDirectory() as temporary:
                self.requests.clear()
                self.organization["IsSandbox"] = environment == "sandbox"
                reader = self.adapter(environment)
                with Runtime(Path(temporary) / "state", crm_reader=reader) as runtime:
                    brief = create_brief(runtime, ACCOUNT_ID, as_of=AS_OF)
                    self.assertEqual(brief["status"], "accepted")
                    self.assertIn('  Contract end: 2027-01-31, in 16 days; status "Draft"; a planning candidate, '
                                  "not activated [3]", brief["text"])
                    self.assertIn("  The contract end is in 16 days and this opportunity is still open at stage "
                                  '"Review". [3][4]', brief["text"])
                    self.assertEqual([row["tool"] for row in brief["evidence"]["receipts"]],
                                     ["get_account", "list_opportunities", "read_call_notes"])
                    self.assertTrue(all(row["status"] == "ok" for row in runtime.connector.audit.read()))
                    self.assertNotIn(TOKEN, json.dumps(brief))
                    self.assertNotIn(ORIGIN, json.dumps(brief))
                self.assertEqual(reader.http_requests, 9)
                self.assertTrue(all(request.method == "GET" for request in self.requests))

    def test_sanitized_paid_environment_check_reports_missing_fields_and_reuses_capabilities(self):
        for environment in ("sandbox", "production"):
            with self.subTest(environment=environment):
                self.requests.clear()
                self.organization["IsSandbox"] = environment == "sandbox"
                report = run_check(environment=environment, as_of=AS_OF,
                                   adapter_factory=lambda: self.adapter(environment))
                self.assertEqual(report["status"], "passed")
                self.assertEqual(report["steps"][1]["name"], "environment")
                self.assertIn(environment, report["steps"][1]["message"])
                self.assertEqual(report["api_calls"], 15)
                self.assertEqual(sum(request.url.path.endswith("/describe") for request in self.requests), 4)
                private_values = (ORIGIN, TOKEN, ORGANIZATION, ACCOUNT,
                                  "Invented connected account", "contact@invented.example")
                for private in private_values:
                    self.assertNotIn(private, json.dumps(report))
        self.requests.clear()
        self.describes["Contract"]["fields"] = [{"name": field} for field in sorted(FIELDS["Contract"] - {"EndDate"})]
        report = run_check(environment="production", as_of=AS_OF, adapter_factory=self.adapter)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["steps"][5]["code"], "missing_fields")
        self.assertEqual(report["steps"][5]["message"], "Contract does not expose required fields: EndDate.")
        self.assertEqual(report["api_calls"], 5)
        self.assert_metadata_only()

    def test_paid_check_and_startup_share_the_same_combined_metadata_byte_bound(self):
        for description in self.describes.values():
            description["unusedPadding"] = "x" * 300_000
        self.assert_error("salesforce_result_limit", self.adapter().prepare_connection)
        self.requests.clear()
        reader = self.adapter()
        report = run_check(environment="production", as_of=AS_OF, adapter_factory=lambda: reader)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["steps"][5]["code"], "salesforce_result_limit")
        self.assertEqual(report["api_calls"], 5)
        self.assert_metadata_only()
        self.assertEqual(reader._checked_objects, set())
        for description in self.describes.values():
            del description["unusedPadding"]
        reader.prepare_connection()
        self.assertEqual(reader.http_requests, 9)

    def test_paid_check_metadata_steps_share_one_deadline_and_request_limit(self):
        for bound, code, calls in (("time", "salesforce_timeout", 4),
                                   ("requests", "salesforce_result_limit", 4)):
            with self.subTest(bound=bound):
                self.requests.clear()
                now = [0.0]

                def elapsed(request):
                    if request.url.path.endswith("/describe"):
                        now[0] += 7.0
                    return None

                self.fail_request = elapsed if bound == "time" else None
                reader = self.adapter()
                # Replace only this module's clock; local HTTPS service clocks remain real.
                with patch("gtm_connector.salesforce.time", Mock(monotonic=lambda: now[0])), \
                        patch("gtm_connector.salesforce._MAX_REQUESTS", 4 if bound == "requests" else 12):
                    report = run_check(environment="production", as_of=AS_OF, adapter_factory=lambda: reader)
                self.assertEqual(report["status"], "failed")
                self.assertIn(code, [step["code"] for step in report["steps"]])
                self.assertEqual(report["api_calls"], calls)
                self.assert_metadata_only()
                self.assertEqual(reader._checked_objects, set())
                self.fail_request = None
                reader.prepare_connection()
                self.assertEqual(reader.http_requests, calls + 4)

    def test_invalid_check_environment_does_not_construct_an_adapter(self):
        factory = Mock()
        for value in (None, "", "unknown", [], False):
            with self.subTest(value=value), self.assertRaisesRegex(ValueError, "environment must be"):
                run_check(environment=value, adapter_factory=factory)
        factory.assert_not_called()

    def test_large_account_book_search_returns_its_first_hundred_and_setup_probes_a_single_account(self):
        large_book = [{"Id": f"001{index:012d}AAA", "Name": f"Invented account {index}",
                       "Industry": None, "AccountNumber": None} for index in range(101)]

        def unfiltered_book(request):
            query = request.url.params.get("q", "")
            if query == "SELECT Id, Name, Industry, AccountNumber FROM Account ORDER BY Name, Id LIMIT 101":
                return httpx2.Response(200, json={"done": True, "totalSize": len(large_book), "records": large_book})
            return None

        self.fail_request = unfiltered_book
        for environment in ("sandbox", "production"):
            with self.subTest(environment=environment):
                self.organization["IsSandbox"] = environment == "sandbox"
                rows, more = self.adapter(environment).search_accounts()
                self.assertEqual([row["source_id"] for row in rows], [row["Id"] for row in large_book[:100]])
                self.assertIs(more, True)
                self.requests.clear()
                report = run_check(environment=environment, as_of=AS_OF,
                                   adapter_factory=lambda: self.adapter(environment))
                self.assertEqual(report["status"], "passed")
                self.assertEqual(report["api_calls"], 15)
                sample = report["steps"][6]
                self.assertEqual((sample["name"], sample["status"], sample["count"]), ("account_search", "pass", 1))
                self.assertIn("one-account read probe", sample["message"])
                self.assertIn("sample, not the account book size", sample["message"])
                account_queries = [request.url.params["q"] for request in self.requests
                                   if " FROM Account " in request.url.params.get("q", "")]
                self.assertIn("SELECT Id, Name, Industry, AccountNumber FROM Account ORDER BY Id LIMIT 1",
                              account_queries)
                self.assertNotIn("SELECT Id, Name, Industry, AccountNumber FROM Account ORDER BY Name, Id LIMIT 101",
                                 account_queries)
                for private in (ORIGIN, TOKEN, ORGANIZATION, ACCOUNT, "Invented account", "contact@invented.example"):
                    self.assertNotIn(private, json.dumps(report))
        self.fail_request = lambda request: httpx2.Response(
            200, json={"done": True, "totalSize": 2, "records": large_book[:2]}) \
            if request.url.params.get("q", "").endswith("FROM Account ORDER BY Id LIMIT 1") else None
        self.assert_error("salesforce_result_limit", self.adapter().probe_accounts)


if __name__ == "__main__":
    unittest.main()
