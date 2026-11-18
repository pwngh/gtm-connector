"""Read-only Salesforce behavior against invented JSON, never a live org."""

from copy import deepcopy
import json
from pathlib import Path
import traceback
import unittest
from unittest.mock import Mock, call, patch

import httpx2

from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter


FIXTURES = Path(__file__).parent / "fixtures" / "salesforce"
ORIGIN = "https://fixture-dev-ed.develop.my.salesforce.com"
TOKEN = "fixture-only-salesforce-access-token"
ACCOUNT_ID = "acc-sf-001000000000001AAA"
ORG_QUERY = "SELECT Id, OrganizationType FROM Organization LIMIT 1"
QUERY_PATH = "/services/data/v68.0/query"
REAL_CLIENT = httpx2.Client


def fixture(name):
    return json.loads((FIXTURES / name).read_text())


class SalesforceTests(unittest.TestCase):
    def setUp(self):
        self.requests = []

    def adapter(self, *responses):
        pending = list(responses)

        def respond(request):
            self.requests.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(str(request.url).split("/services/", 1)[0], ORIGIN)
            self.assertEqual(request.headers["authorization"], "Bearer " + TOKEN)
            self.assertEqual(request.headers["accept-encoding"], "identity")
            self.assertTrue(pending, "Unexpected extra HTTP request")
            response = pending.pop(0)
            if isinstance(response, Exception):
                raise response
            if callable(response):
                return response(request)
            if isinstance(response, httpx2.Response):
                return response
            if isinstance(response, tuple):
                status, name = response
                return httpx2.Response(status, content=(FIXTURES / name).read_bytes())
            if isinstance(response, dict):
                return httpx2.Response(200, json=response)
            return httpx2.Response(200, content=(FIXTURES / response).read_bytes())

        return SalesforceReadOnlyAdapter(ORIGIN, TOKEN, transport=httpx2.MockTransport(respond))

    def assert_error(self, code, function):
        with self.assertRaises(SalesforceReadError) as caught:
            function()
        self.assertEqual(caught.exception.code, code)
        self.assertEqual(str(caught.exception), code)
        self.assertNotIn(TOKEN, str(caught.exception))
        return caught.exception

    def test_constructor_and_environment_selection_make_no_http_requests(self):
        with patch("gtm_connector.salesforce.httpx2.Client") as client:
            adapter = SalesforceReadOnlyAdapter(ORIGIN, TOKEN)
            self.assertEqual(adapter.name, "salesforce")
            environment = Mock()
            environment.get.side_effect = {"SALESFORCE_INSTANCE_URL": ORIGIN,
                                           "SALESFORCE_ACCESS_TOKEN": TOKEN}.get
            with patch("gtm_connector.salesforce.os.environ", environment):
                selected = SalesforceReadOnlyAdapter.from_env()
            self.assertEqual(selected.instance_url, ORIGIN)
            self.assertEqual(environment.get.call_args_list,
                             [call("SALESFORCE_INSTANCE_URL"), call("SALESFORCE_ACCESS_TOKEN")])
            client.assert_not_called()

    def test_only_explicit_https_mydomain_origins_and_safe_tokens_are_accepted(self):
        accepted = (ORIGIN, "https://fixture-dev-ed.my.salesforce.com",
                    "https://fixture-dev-ed.my.salesforce.com:443")
        for origin in accepted:
            with self.subTest(origin=origin):
                SalesforceReadOnlyAdapter(origin, TOKEN)
        invalid = (None, 12, "", "http://fixture.my.salesforce.com", "https://127.0.0.1",
                   "https://my.salesforce.com", "https://fixture.my.salesforce.com.evil.example",
                   "https://fixture.my.salesforce.com:444", "https://fixture.my.salesforce.com:",
                   "https://fixture.my.salesforce.com/", "https://fixture.my.salesforce.com/path",
                   "https://fixture.my.salesforce.com?", "https://fixture.my.salesforce.com#",
                   "https://fixture.my.salesforce.com?token=" + TOKEN,
                   "https://" + TOKEN + "@fixture.my.salesforce.com",
                   "https://fixture.my.salesforce.com:" + TOKEN,
                   "https://fixture%2emy.salesforce.com", "https://fixture.my.salesforce.com\n")
        for origin in invalid:
            with self.subTest(origin=origin):
                self.assert_error("salesforce_invalid_configuration",
                                  lambda: SalesforceReadOnlyAdapter(origin, TOKEN))
        for token in (None, "", "contains space", "has\nnewline", "nonascii-é", "x" * 4097):
            with self.subTest(token=token):
                self.assert_error("salesforce_invalid_configuration",
                                  lambda: SalesforceReadOnlyAdapter(ORIGIN, token))

    def test_search_maps_nullable_fields_without_inventing_renewal_or_arr(self):
        adapter = self.adapter("organization_developer.json", "accounts.json")
        with patch("gtm_connector.salesforce.httpx2.Client", wraps=REAL_CLIENT) as client:
            rows = adapter.search_accounts()
        self.assertEqual(rows, [
            {"id": "acc-sf-001000000000001AAA", "source_id": "001000000000001AAA",
             "backend": "salesforce", "name": "Fixture Salesforce account 001", "industry": "Software", "account_number": "FIXTURE-001"},
            {"id": "acc-sf-001000000000002AAA", "source_id": "001000000000002AAA",
             "backend": "salesforce", "name": "Fixture Salesforce account 002", "industry": None, "account_number": None},
        ])
        self.assertEqual(self.requests[0].url.params["q"], ORG_QUERY)
        self.assertEqual(self.requests[1].url.params["q"],
                         "SELECT Id, Name, Industry, AccountNumber FROM Account ORDER BY Id LIMIT 101")
        options = client.call_args.kwargs
        self.assertIs(options["verify"], True)
        self.assertIs(options["trust_env"], False)
        self.assertIs(options["follow_redirects"], False)
        for row in rows:
            self.assertTrue({"record_ref", "arr_cents", "renewal_date", "risk_status", "risk_reason"}.isdisjoint(row))

    def test_get_account_preserves_contact_nulls_and_source_associations(self):
        account = self.adapter("organization_developer.json", "account.json", "contacts.json", "empty.json").get_account(ACCOUNT_ID)
        self.assertEqual(account["id"], ACCOUNT_ID)
        self.assertEqual(account["contacts"], [
            {"id": "con-sf-003000000000001AAA", "source_id": "003000000000001AAA", "backend": "salesforce",
             "account_id": ACCOUNT_ID, "name": "Fixture Salesforce contact 001",
             "title": "Fixture contact role", "email": "contact001@salesforce-fixture.example"},
            {"id": "con-sf-003000000000002AAA", "source_id": "003000000000002AAA", "backend": "salesforce",
             "account_id": ACCOUNT_ID, "name": "Fixture Salesforce contact 002", "title": None, "email": None},
        ])
        self.assertEqual(self.requests[1].url.params["q"],
                         "SELECT Id, Name, Industry, AccountNumber FROM Account WHERE Id = '001000000000001AAA' ORDER BY Id LIMIT 101")
        self.assertEqual(self.requests[2].url.params["q"],
                         "SELECT Id, AccountId, Name, Title, Email FROM Contact WHERE AccountId = '001000000000001AAA' ORDER BY Id LIMIT 101")

    def test_missing_account_does_not_query_contacts(self):
        self.assertIsNone(self.adapter("organization_developer.json", "empty.json").get_account(ACCOUNT_ID))
        self.assertEqual(len(self.requests), 2)

    def test_opportunities_keep_raw_stage_decimal_and_unknown_currency(self):
        rows = self.adapter("organization_developer.json", "opportunities.json").list_opportunities()
        self.assertEqual(rows[0], {"id": "opp-sf-006000000000001AAA", "source_id": "006000000000001AAA",
            "backend": "salesforce", "account_id": ACCOUNT_ID, "name": "Fixture Salesforce opportunity 001",
            "stage": "Fixture review pending", "close_date": "2027-02-01", "is_closed": False,
            "is_won": False, "amount_decimal": "12345.67", "currency": None})
        self.assertIsNone(rows[1]["amount_decimal"])
        self.assertIsNone(rows[1]["account_id"])
        self.assertEqual(rows[1]["stage"], "Closed Won")
        self.assertTrue(rows[1]["is_closed"] and rows[1]["is_won"])
        self.assertNotIn("amount_cents", rows[0])
        exact = self.adapter("organization_developer.json", "opportunity_decimal.json").list_opportunities(ACCOUNT_ID)
        self.assertEqual(exact[0]["amount_decimal"], "0.10")
        self.assertIn("WHERE AccountId = '001000000000001AAA' ORDER BY Id LIMIT 101", self.requests[-1].url.params["q"])

    def test_only_successful_edition_checks_are_cached_for_this_adapter(self):
        adapter = self.adapter("organization_enterprise.json", "organization_developer.json", "accounts.json",
                               "opportunities.json", "account.json", "contacts.json", "empty.json")
        self.assert_error("salesforce_edition_required", adapter.check_edition)
        adapter.search_accounts()
        adapter.list_opportunities()
        adapter.get_account(ACCOUNT_ID)
        adapter.check_edition()
        self.assertEqual([request.url.params["q"] for request in self.requests].count(ORG_QUERY), 2)
        self.assertEqual(adapter.http_requests, 7)
        self.assertEqual(adapter.sf_cli_invocations, 0)

    def test_cold_and_warm_round_trip_counts_include_contracts(self):
        cases = (("search_accounts", (), ("accounts.json",), 2, 1),
                 ("list_opportunities", (), ("opportunities.json",), 2, 1),
                 ("get_account", (ACCOUNT_ID,), ("account.json", "contacts.json", "empty.json"), 4, 3))
        for name, arguments, responses, cold, warm in cases:
            adapter = self.adapter("organization_developer.json", *responses, *responses)
            getattr(adapter, name)(*arguments)
            self.assertEqual(adapter.http_requests, cold)
            getattr(adapter, name)(*arguments)
            self.assertEqual(adapter.http_requests, cold + warm)

    def test_all_non_developer_and_missing_edition_values_fail_before_crm(self):
        for edition in ("Enterprise Edition", "Trial", "Unlimited Edition", "Developer",
                        "developer edition", "Developer Edition ", None):
            organization = fixture("organization_developer.json")
            organization["records"][0]["OrganizationType"] = edition
            with self.subTest(edition=edition):
                before = len(self.requests)
                self.assert_error("salesforce_edition_required", self.adapter(organization).search_accounts)
                self.assertEqual(len(self.requests), before + 1)
                self.assertEqual(self.requests[-1].url.params["q"], ORG_QUERY)
        self.assert_error("salesforce_edition_required", self.adapter("empty.json").search_accounts)
        for field in ("Id", "OrganizationType"):
            organization = fixture("organization_developer.json")
            del organization["records"][0][field]
            code = "salesforce_invalid_response" if field == "Id" else "salesforce_edition_required"
            self.assert_error(code, self.adapter(organization).search_accounts)

    def test_search_escapes_literals_and_like_wildcards_before_url_encoding(self):
        adapter = self.adapter("organization_developer.json", "empty.json")
        query = "Fixture' OR Name LIKE '%' \\ 50%_ & é"
        adapter.search_accounts(query)
        self.assertEqual(self.requests[-1].url.params["q"],
            "SELECT Id, Name, Industry, AccountNumber FROM Account WHERE Name LIKE '%Fixture\\' OR Name LIKE \\'\\%\\' \\\\ 50\\%\\_ & é%' ORDER BY Id LIMIT 101")
        self.assertNotIn(" & ", str(self.requests[-1].url))

    def test_invalid_arguments_fail_locally_without_any_request(self):
        adapter = self.adapter()
        for query in (None, "x" * 201, "line\nbreak", "\x00", "\ud800"):
            self.assert_error("salesforce_invalid_arguments", lambda: adapter.search_accounts(query))
        for identifier in (None, "acc-001", "001000000000001AAA", "acc-sf-001000000000001AAA'", "acc-sf-short"):
            self.assert_error("salesforce_invalid_arguments", lambda: adapter.get_account(identifier))
            if identifier is not None:
                self.assert_error("salesforce_invalid_arguments", lambda: adapter.list_opportunities(identifier))
        self.assertEqual(self.requests, [])

    def test_pagination_uses_only_the_valid_relative_query_locator(self):
        rows = self.adapter("organization_developer.json", "accounts_page1.json", "accounts_page2.json").search_accounts()
        self.assertEqual([row["source_id"] for row in rows], ["001000000000001AAA", "001000000000002AAA"])
        self.assertEqual(self.requests[-1].url.path, "/services/data/v68.0/query/01g000000000001AAA-1")
        self.assertEqual(str(self.requests[-1].url.query), "b''")

    def test_hostile_pagination_never_receives_a_forwarded_credential(self):
        locators = ("https://untrusted.example/query/page", ORIGIN + QUERY_PATH + "/page",
                    "//untrusted.example/query/page", "/services/data/v67.0/query/page",
                    QUERY_PATH + "/../sobjects", QUERY_PATH + "/%2e%2e", QUERY_PATH + "/page?token=" + TOKEN,
                    QUERY_PATH + "/page#fragment", QUERY_PATH + "\\page", None)
        for locator in locators:
            response = fixture("accounts_page1.json")
            response["nextRecordsUrl"] = locator
            with self.subTest(locator=locator):
                before = len(self.requests)
                self.assert_error("salesforce_pagination_invalid",
                                  self.adapter("organization_developer.json", response).search_accounts)
                self.assertEqual(len(self.requests), before + 2)

    def test_partial_page_failure_does_not_return_partial_success(self):
        self.assert_error("salesforce_auth_required",
                          self.adapter("organization_developer.json", "accounts_page1.json",
                                       (401, "error_auth.json")).search_accounts)
        self.assertEqual(len(self.requests), 3)

    def test_duplicate_records_repeated_locators_and_inconsistent_totals_fail(self):
        first = fixture("accounts_page1.json")
        repeated = deepcopy(first)
        repeated["records"][0]["Id"] = "001000000000002AAA"
        first["totalSize"] = repeated["totalSize"] = 3
        cases = (
            (fixture("accounts_page1.json"), {**fixture("account.json"), "totalSize": 2}, "salesforce_invalid_response"),
            (first, repeated, "salesforce_pagination_invalid"),
            (fixture("accounts_page1.json"), {**fixture("accounts_page2.json"), "totalSize": 3}, "salesforce_invalid_response"),
        )
        for first_page, second_page, code in cases:
            with self.subTest(code=code):
                self.assert_error(code, self.adapter("organization_developer.json", first_page, second_page).search_accounts)

    def test_result_page_request_and_byte_limits_fail_explicitly(self):
        too_many = fixture("accounts_page1.json")
        too_many["totalSize"] = 101
        self.assert_error("salesforce_result_limit", self.adapter("organization_developer.json", too_many).search_accounts)
        with patch("gtm_connector.salesforce._MAX_PAGES", 1):
            self.assert_error("salesforce_result_limit",
                              self.adapter("organization_developer.json", "accounts_page1.json").search_accounts)
        with patch("gtm_connector.salesforce._MAX_REQUESTS", 1):
            self.assert_error("salesforce_result_limit", self.adapter("organization_developer.json").search_accounts)
        org_size = len((FIXTURES / "organization_developer.json").read_bytes())
        with patch("gtm_connector.salesforce._MAX_BYTES", org_size + 10):
            self.assert_error("salesforce_result_limit",
                              self.adapter("organization_developer.json", "accounts.json").search_accounts)

    def test_expiry_quota_permissions_and_http_errors_are_fixed_and_not_retried(self):
        cases = ((401, "error_auth.json", "salesforce_auth_required"),
                 (429, "error_limit.json", "salesforce_rate_limited"),
                 (403, "error_limit.json", "salesforce_rate_limited"),
                 (403, "error_forbidden.json", "salesforce_forbidden"),
                 (400, "error_forbidden.json", "salesforce_http_error"),
                 (500, "error_forbidden.json", "salesforce_http_error"))
        for status, body, code in cases:
            with self.subTest(status=status, code=code):
                before = len(self.requests)
                error = self.assert_error(code, self.adapter("organization_developer.json", (status, body)).search_accounts)
                self.assertNotIn("upstream message", str(error))
                self.assertEqual(len(self.requests), before + 2)
        redirect = httpx2.Response(302, headers={"Location": "https://untrusted.example/" + TOKEN})
        self.assert_error("salesforce_http_error", self.adapter("organization_developer.json", redirect).search_accounts)

    def test_transport_failures_and_deadline_do_not_expose_credentials(self):
        for error, code in ((httpx2.ConnectError(TOKEN), "salesforce_unavailable"),
                            (httpx2.ReadTimeout(TOKEN), "salesforce_timeout")):
            try:
                self.adapter(error).search_accounts()
            except SalesforceReadError as caught:
                self.assertEqual(caught.code, code)
                self.assertNotIn(TOKEN, "".join(traceback.format_exception(caught)))
            else:
                self.fail("Transport error was not rejected")
        clock = [0]

        def slow_response(_):
            clock[0] = 21
            return httpx2.Response(200, content=(FIXTURES / "accounts.json").read_bytes())

        with patch("gtm_connector.salesforce.time.monotonic", side_effect=lambda: clock[0]):
            self.assert_error("salesforce_timeout", self.adapter("organization_developer.json", slow_response).search_accounts)
        clock[0] = 0

        class SlowStream(httpx2.SyncByteStream):
            def __iter__(self):
                yield b"{"
                clock[0] = 21
                yield b'"records":'
                raise AssertionError("The adapter kept reading beyond its operation deadline")

        with patch("gtm_connector.salesforce.time.monotonic", side_effect=lambda: clock[0]):
            response = httpx2.Response(200, stream=SlowStream())
            self.assert_error("salesforce_timeout",
                              self.adapter("organization_developer.json", response).search_accounts)

    def test_malformed_json_and_records_fail_without_echoing_the_body(self):
        bodies = (b"not json " + TOKEN.encode(), b'{"records":[],"records":[]}',
                  b'{"totalSize":NaN,"done":true,"records":[]}',
                  b'{"totalSize":1e999999999999999999999999999,"done":true,"records":[]}')
        for body in bodies:
            with self.subTest(body=body):
                self.assert_error("salesforce_invalid_response",
                                  self.adapter("organization_developer.json", httpx2.Response(200, content=body)).search_accounts)
        for changes in ({"totalSize": True}, {"done": 1}, {"records": [None]}, {"totalSize": 2}):
            response = {**fixture("account.json"), **changes}
            self.assert_error("salesforce_invalid_response", self.adapter("organization_developer.json", response).search_accounts)
        for field, value in (("Id", "short"), ("Name", None), ("Industry", []),
                             ("Name", "\ud800"), ("Industry", "\udfff")):
            response = fixture("account.json")
            response["records"][0][field] = value
            encoded = httpx2.Response(200, content=json.dumps(response).encode())
            self.assert_error("salesforce_invalid_response", self.adapter("organization_developer.json", encoded).search_accounts)

    def test_cross_account_records_are_rejected_instead_of_reassociated(self):
        contacts = fixture("contacts.json")
        contacts["records"][0]["AccountId"] = "001000000000002AAA"
        self.assert_error("salesforce_invalid_response",
                          lambda: self.adapter("organization_developer.json", "account.json", contacts).get_account(ACCOUNT_ID))
        opportunity = fixture("opportunity_decimal.json")
        opportunity["records"][0]["AccountId"] = "001000000000002AAA"
        self.assert_error("salesforce_invalid_response",
                          lambda: self.adapter("organization_developer.json", opportunity).list_opportunities(ACCOUNT_ID))

    def test_invalid_opportunity_types_and_expanding_decimal_values_fail(self):
        for field, value in (("Amount", True), ("Amount", "100"), ("CloseDate", "20270101"),
                             ("CloseDate", "invalid"), ("IsWon", 1), ("StageName", [])):
            response = fixture("opportunity_decimal.json")
            response["records"][0][field] = value
            self.assert_error("salesforce_invalid_response",
                              self.adapter("organization_developer.json", response).list_opportunities)
        raw = (FIXTURES / "opportunity_decimal.json").read_bytes().replace(b'"Amount":0.10', b'"Amount":1e999999999')
        self.assert_error("salesforce_invalid_response",
                          self.adapter("organization_developer.json", httpx2.Response(200, content=raw)).list_opportunities)

    def contract(self, **changes):
        return {"Id": "800000000000001AAA", "AccountId": "001000000000001AAA",
                "Status": "Draft", "StatusCode": "Draft", "StartDate": "2026-01-31",
                "EndDate": "2027-01-30", "ContractTerm": 12, **changes}

    def contracts(self, *rows):
        return {"totalSize": len(rows), "done": True, "records": list(rows)}

    def test_contracts_use_server_end_date_and_preserve_nullable_fields(self):
        response = self.contracts(self.contract(), self.contract(
            Id="800000000000002AAA", Status="A custom label", StatusCode="InApproval",
            StartDate=None, EndDate=None, ContractTerm=None))
        adapter = self.adapter("organization_developer.json", "account.json", "empty.json", response)
        rows = adapter.get_account(ACCOUNT_ID)["contracts"]
        self.assertEqual(rows[0], {"id": "ctr-sf-800000000000001AAA", "source_id": "800000000000001AAA",
            "backend": "salesforce", "account_id": ACCOUNT_ID, "status": "Draft", "status_code": "Draft",
            "start_date": "2026-01-31", "end_date": "2027-01-30", "contract_term": 12})
        self.assertEqual(rows[1]["status"], "A custom label")
        for field in ("start_date", "end_date", "contract_term"):
            self.assertIsNone(rows[1][field])
        self.assertEqual(self.requests[-1].url.params["q"],
            "SELECT Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate FROM Contract "
            "WHERE AccountId = '001000000000001AAA' ORDER BY Id LIMIT 101")

    def test_contract_malformed_fields_and_cross_account_rows_fail(self):
        for field, value in (("Id", "short"), ("AccountId", "001000000000002AAA"),
                             ("StatusCode", "Terminated"), ("StatusCode", []), ("Status", None),
                             ("StartDate", "20260131"), ("EndDate", "2027-02-30"),
                             ("ContractTerm", True), ("ContractTerm", 1.5),
                             ("ContractTerm", 2 ** 31), ("ContractTerm", -(2 ** 31) - 1)):
            with self.subTest(field=field, value=value):
                adapter = self.adapter("organization_developer.json", "account.json", "empty.json",
                                       self.contracts(self.contract(**{field: value})))
                self.assert_error("salesforce_invalid_response", lambda: adapter.get_account(ACCOUNT_ID))
        for field in ("AccountNumber",):
            account = fixture("account.json")
            del account["records"][0][field]
            self.assert_error("salesforce_invalid_response",
                              self.adapter("organization_developer.json", account).search_accounts)
        for field in ("StartDate", "EndDate", "ContractTerm"):
            row = self.contract()
            del row[field]
            adapter = self.adapter("organization_developer.json", "account.json", "empty.json", self.contracts(row))
            self.assert_error("salesforce_invalid_response", lambda: adapter.get_account(ACCOUNT_ID))

    def test_describe_is_fixed_bounded_and_returns_only_field_names(self):
        adapter = self.adapter("organization_developer.json", {"fields": [{"name": "Id"}, {"name": "EndDate"}]})
        self.assertEqual(adapter.describe_fields("Contract"), {"Id", "EndDate"})
        self.assertEqual(self.requests[-1].url.path, "/services/data/v68.0/sobjects/Contract/describe")
        self.assertEqual(str(self.requests[-1].url.query), "b''")
        for name in (None, [], "User", "../Account", "Contract?token=bad"):
            self.assert_error("salesforce_invalid_arguments", lambda: self.adapter().describe_fields(name))
        for response in ({}, {"fields": [None]}, {"fields": [{"name": "../Id"}]},
                         {"fields": [{"name": "Id"}, {"name": "Id"}]}):
            adapter = self.adapter("organization_developer.json", response)
            self.assert_error("salesforce_invalid_response", lambda: adapter.describe_fields("Contract"))

    def test_candidate_selection_uses_fixed_eligible_query_and_validates_returned_date(self):
        adapter = self.adapter("organization_developer.json", self.contracts(self.contract()))
        self.assertEqual(adapter.candidate_account("2027-01-30"), ACCOUNT_ID)
        self.assertEqual(self.requests[-1].url.params["q"],
            "SELECT Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate FROM Contract "
            "WHERE EndDate >= 2027-01-30 AND StatusCode IN ('Draft', 'InApproval', 'Activated') "
            "ORDER BY EndDate, Id LIMIT 1")
        self.assertIsNone(self.adapter("organization_developer.json", "empty.json").candidate_account("2026-01-01"))
        for value in (None, "20270101", "2027-02-30", "2027-01-01 OR Id != null"):
            self.assert_error("salesforce_invalid_arguments", lambda: self.adapter().candidate_account(value))
        for end in (None, "2025-01-01"):
            adapter = self.adapter("organization_developer.json", self.contracts(self.contract(EndDate=end)))
            self.assert_error("salesforce_invalid_response", lambda: adapter.candidate_account("2026-01-01"))


if __name__ == "__main__":
    unittest.main()
