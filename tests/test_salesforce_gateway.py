"""Exercise optional upstream reads through real local OAuth and MCP HTTPS."""

import asyncio
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest
from unittest.mock import Mock, patch

import httpx2
from mcp.server.mcpserver.exceptions import ToolError

from gtm_connector.briefing import create_brief
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter


ACCOUNT = "001000000000001AAA"
CONTACT = "003000000000001AAA"
OPPORTUNITY = "006000000000001AAA"
CONTRACT = "800000000000001AAA"
ORGANIZATION = "00D000000000001AAA"
ACCOUNT_ID = "acc-sf-" + ACCOUNT
UPSTREAM_TOKEN = "invented-salesforce-token-for-http-fixtures"
INSTANCE = "https://fixture-unit-test.my.salesforce.com"
ORGANIZATION_QUERY = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"


class SalesforceGatewayTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "state"
        self.requests = []
        self.edition = "Developer Edition"
        self.failure = None
        self.book = None
        self.contracts = [{"Id": CONTRACT, "AccountId": ACCOUNT, "Status": "Draft", "StatusCode": "Draft",
                           "StartDate": "2026-02-01", "ContractTerm": 12, "EndDate": "2027-01-31"}]
        self.runtime = None
        self.reader = SalesforceReadOnlyAdapter(
            INSTANCE, UPSTREAM_TOKEN, transport=httpx2.MockTransport(self.respond))

    def respond(self, request):
        # The remote service is a mock; the OAuth/MCP sockets remain real HTTPS.
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "fixture-unit-test.my.salesforce.com")
        self.assertEqual(request.headers["Authorization"], "Bearer " + UPSTREAM_TOKEN)
        query = request.url.params["q"]
        if self.runtime is not None:
            receipts = self.runtime.connector.audit.read()
            self.assertEqual(len([row for row in receipts if row["status"] == "pending"]), 1)
        if query == ORGANIZATION_QUERY:
            # Owner startup checks metadata before a tool or source store exists.
            rows = [{"Id": ORGANIZATION, "OrganizationType": self.edition, "IsSandbox": False}]
            return httpx2.Response(200, json={"totalSize": 1, "done": True, "records": rows})
        if self.failure is not None:
            if self.failure == "timeout":
                raise httpx2.ReadTimeout(UPSTREAM_TOKEN, request=request)
            if self.failure == "malformed":
                return httpx2.Response(200, content=UPSTREAM_TOKEN)
            return httpx2.Response(self.failure, json=[{
                "message": UPSTREAM_TOKEN + " private upstream detail", "errorCode": "FIXTURE_ERROR"}])
        elif " FROM Account" in query and self.book is not None:
            rows = self.book[:int(query.rsplit(" LIMIT ", 1)[1])]
        elif " FROM Account" in query:
            rows = [{"Id": ACCOUNT, "Name": "Fixture Salesforce account", "Industry": None, "AccountNumber": None}]
        elif " FROM Contact" in query:
            rows = [{"Id": CONTACT, "AccountId": ACCOUNT, "Name": "Fixture Salesforce contact",
                     "Title": None, "Email": "contact@fixture.example"}]
        elif " FROM Opportunity" in query:
            rows = [{"Id": OPPORTUNITY, "AccountId": ACCOUNT, "Name": "Fixture Salesforce opportunity",
                     "StageName": "Fixture selected stage", "Amount": 1234.5,
                     "CloseDate": "2027-01-31", "IsClosed": False, "IsWon": False}]
        elif " FROM Contract" in query:
            rows = self.contracts
        else:
            self.fail("Unexpected upstream query")
        return httpx2.Response(200, json={"totalSize": len(rows), "done": True, "records": rows})

    def open_runtime(self):
        self.runtime = self.enterContext(Runtime(self.state, crm_reader=self.reader))
        client = self.enterContext(LocalClient(self.runtime.config))
        self.assertEqual([request.url.params["q"] for request in self.requests], [ORGANIZATION_QUERY])
        self.startup_requests = list(self.requests)
        self.assertEqual(self.runtime.connector.audit.read(), [])
        return client

    def records(self, response):
        self.assertEqual(response.status_code, 200, response.text)
        result = response.json()["result"]
        self.assertFalse(result.get("isError"), response.text)
        data = result["structuredContent"]
        self.assertEqual(data["source"], "crm")
        self.assertIs(data["untrusted_data"], True)
        return data["records"]

    def activities(self):
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection:
            return connection.execute("SELECT count(*) FROM activities").fetchone()[0]

    def test_scoped_reads_are_qualified_and_audited_before_every_record_request(self):
        client = self.open_runtime()
        token = client.authorize(["crm:read"])
        search = self.records(client.call(token, "search_accounts", {"query": "Fixture"}))
        account = self.records(client.call(token, "get_account", {"account_id": ACCOUNT_ID}))
        opportunity = self.records(client.call(token, "list_opportunities", {"account_id": ACCOUNT_ID}))
        self.assertEqual(search[0]["record_ref"], "crm/accounts/" + ACCOUNT_ID)
        self.assertEqual([row["record_ref"] for row in account],
                         ["crm/accounts/" + ACCOUNT_ID, "crm/contacts/con-sf-" + CONTACT,
                          "crm/contracts/ctr-sf-" + CONTRACT])
        self.assertEqual(opportunity[0]["record_ref"], "crm/opportunities/opp-sf-" + OPPORTUNITY)
        for row in search + account + opportunity:
            self.assertEqual(row["backend"], "salesforce")
            self.assertEqual(len(row["source_id"]), 18)
            self.assertNotIn("renewal_date", row)
            self.assertNotIn("risk", row)
            self.assertNotIn("arr_cents", row)
        self.assertEqual(opportunity[0]["amount_decimal"], "1234.5")
        self.assertIsNone(opportunity[0]["currency"])
        self.assertNotIn("amount_cents", opportunity[0])
        self.assertEqual(opportunity[0]["stage"], "Fixture selected stage")
        self.assertEqual(sum(request.url.params["q"] == ORGANIZATION_QUERY for request in self.requests), 1)
        receipts = self.runtime.connector.audit.read()
        self.assertEqual(len(receipts), 3)
        self.assertEqual([row["status"] for row in receipts], ["ok"] * 3)
        self.assertEqual([row["rows"] for row in receipts],
                         [[row["record_ref"] for row in result] for result in (search, account, opportunity)])
        self.assertEqual(self.activities(), 0)

    def test_a_search_past_one_hundred_matches_returns_the_first_hundred_and_says_more_exist(self):
        client = self.open_runtime()
        token = client.authorize(["crm:read"])
        for matching in (100, 101, 250):
            self.book = [{"Id": f"001{number:012d}AAA", "Name": f"Fixture account {number:03d}", "Industry": None,
                          "AccountNumber": None} for number in range(matching)]
            with self.subTest(matching=matching):
                response = client.call(token, "search_accounts", {"query": "Fixture"})
                records = self.records(response)
                self.assertEqual(self.requests[-1].url.params["q"],
                                 "SELECT Id, Name, Industry, AccountNumber FROM Account "
                                 "WHERE Name LIKE '%Fixture%' ORDER BY Name, Id LIMIT 101")
                self.assertEqual([row["source_id"] for row in records], [row["Id"] for row in self.book[:100]])
                # A complete result has the same three keys as before.
                self.assertEqual(response.json()["result"]["structuredContent"],
                                 {"source": "crm", "records": records, "untrusted_data": True,
                                  **({"has_more": True} if matching > 100 else {})})
                receipt = self.runtime.connector.audit.read()[-1]
                self.assertEqual((receipt["tool"], receipt["status"], receipt["row_count"]),
                                 ("search_accounts", "ok", 100))
                self.assertEqual(receipt["rows"], ["crm/accounts/acc-sf-" + row["Id"] for row in self.book[:100]])

    def test_missing_crm_scope_makes_no_upstream_request_after_owner_preflight(self):
        client = self.open_runtime()
        token = client.authorize(["calls:read"])
        for tool, arguments in (("search_accounts", {}), ("get_account", {"account_id": ACCOUNT_ID}),
                                ("list_opportunities", {"account_id": ACCOUNT_ID})):
            response = client.call(token, tool, arguments)
            self.assertEqual(response.status_code, 403)
            self.assertIn('scope="crm:read"', response.headers["WWW-Authenticate"])
        self.assertEqual(self.requests, self.startup_requests)
        receipts = self.runtime.connector.audit.read()
        self.assertEqual(len(receipts), 3)
        self.assertTrue(all(row["http_status"] == 403 and row["rows"] == [] for row in receipts))

    def test_refresh_identity_query_stays_under_the_tool_pending_receipt(self):
        client = self.open_runtime()
        self.reader._credentials = Mock(read=Mock(return_value=(INSTANCE, UPSTREAM_TOKEN)))
        token = client.authorize(["crm:read"])
        self.failure = 401
        response = client.call(token, "search_accounts", {"query": "Fixture"})
        self.assertEqual(response.status_code, 200)
        self.assertTrue(response.json()["result"]["isError"])
        self.assertEqual(sum(request.url.params["q"] == ORGANIZATION_QUERY for request in self.requests), 2)
        self.reader._credentials.read.assert_called_once()
        receipts = self.runtime.connector.audit.read()
        self.assertEqual(len(receipts), 1)
        self.assertEqual(receipts[0]["status"], "error")
        self.assertEqual(receipts[0]["rows"], [])
        self.assertEqual(self.activities(), 0)

    def test_external_writer_is_absent_from_sdk_and_hardcoded_calls_cannot_mutate(self):
        client = self.open_runtime()
        read_token = client.authorize(["crm:read"])
        listing = client.rpc(read_token, "tools/list").json()["result"]["tools"]
        self.assertNotIn("log_activity", {tool["name"] for tool in listing})
        for tool in listing:
            self.assertTrue(tool["annotations"]["readOnlyHint"])
            self.assertEqual(tool["annotations"]["openWorldHint"],
                             tool["name"] in {"search_accounts", "get_account", "list_opportunities"})
        self.assertNotIn("log_activity", {tool.name for tool in asyncio.run(self.runtime.connector.mcp.list_tools())})
        arguments = {"account_id": ACCOUNT_ID, "kind": "fixture", "body": "Fixture attempted write",
                     "idempotency_key": "fixture-salesforce-write"}
        with self.assertRaises(ToolError):
            asyncio.run(self.runtime.connector.mcp.call_tool("log_activity", arguments))
        write_token = client.authorize(["crm:write"])
        cases = (("invalid-fixture-token", 401, "invalid or expired", "invalid_token", 1001),
                 (read_token, 403, "insufficient_scope", "insufficient_scope", 1004),
                 (write_token, 403, "crm_write_disabled", "crm_write_disabled", 1005))
        for token, status, message, reason, code in cases:
            response = client.call(token, "log_activity", arguments)
            self.assertEqual(response.status_code, status)
            self.assertIn(message, response.text)
            self.assertEqual(response.json()["error"]["data"]["reason"], reason)
            self.assertEqual(response.json()["error"]["code"], code)
        self.assertEqual(self.requests, self.startup_requests)
        self.assertEqual(self.activities(), 0)
        receipts = self.runtime.connector.audit.read()
        self.assertEqual(len(receipts), 3)
        self.assertEqual([row["http_status"] for row in receipts], [401, 403, 403])
        self.assertTrue(all(row["rows"] == [] and row["status"] == "error" for row in receipts))

    def test_upstream_failures_return_only_safe_codes_and_one_error_receipt(self):
        client = self.open_runtime()
        token = client.authorize(["crm:read"])
        cases = ((401, "salesforce_auth_required"), (403, "salesforce_forbidden"),
                 (429, "salesforce_rate_limited"), (500, "salesforce_http_error"),
                 ("timeout", "salesforce_timeout"), ("malformed", "salesforce_invalid_response"))
        for count, (failure, code) in enumerate(cases, 1):
            self.failure = failure
            response = client.call(token, "search_accounts", {})
            self.assertEqual(response.status_code, 200)
            self.assertTrue(response.json()["result"]["isError"])
            self.assertEqual(response.json()["result"]["content"][0]["text"],
                             "Error executing tool search_accounts: " + code)
            receipts = self.runtime.connector.audit.read()
            self.assertEqual(len(receipts), count)
            self.assertEqual((receipts[-1]["status"], receipts[-1]["rows"]), ("error", []))
            self.assertNotIn(UPSTREAM_TOKEN, response.text + json.dumps(receipts))
        owner = (self.state / "owner.json").read_text()
        self.assertNotIn(UPSTREAM_TOKEN, owner)
        self.assertNotIn(INSTANCE, owner)
        self.assertEqual(json.loads(owner)["crm_backend"], "salesforce")
        self.assertNotIn(UPSTREAM_TOKEN.encode(), (self.state / "audit.sqlite3").read_bytes())

    def test_wrong_or_missing_edition_stops_startup_before_crm_queries_or_artifacts(self):
        for index, edition in enumerate(("Enterprise Edition", "Developer", None)):
            self.edition = edition
            state = self.state / str(index)
            with self.subTest(edition=edition), self.assertRaises(SalesforceReadError) as failure:
                with Runtime(state, crm_reader=self.reader):
                    self.fail("An incompatible environment must not start.")
            self.assertEqual(failure.exception.code, "salesforce_edition_required")
            self.assertFalse((state / "crm.sqlite3").exists())
            self.assertFalse((state / "owner.json").exists())
        self.assertEqual([request.url.params["q"] for request in self.requests], [ORGANIZATION_QUERY] * 3)

    def test_initial_audit_failure_prevents_upstream_reads_after_owner_preflight(self):
        client = self.open_runtime()
        token = client.authorize(["crm:read"])
        with patch.object(self.runtime.connector.audit, "start", side_effect=sqlite3.OperationalError("fixture")):
            response = client.call(token, "get_account", {"account_id": ACCOUNT_ID})
        self.assertEqual(response.status_code, 503)
        self.assertEqual(self.requests, self.startup_requests)
        self.assertEqual(self.runtime.connector.audit.read(), [])

    def test_external_account_without_contract_gets_a_card_that_says_so(self):
        self.open_runtime()
        self.contracts = []
        for include_slack in (False, True):
            brief = create_brief(self.runtime, ACCOUNT_ID, as_of="2027-01-15", include_slack=include_slack)
            self.assertEqual(brief["status"], "accepted")
            self.assertIn("  Contract end: Missing: no contract ends on or after 2027-01-15 [1]", brief["text"])
        self.assertEqual(len(self.runtime.connector.audit.read()), 9)
        self.assertTrue(all(request.method == "GET" for request in self.requests))

    def test_default_runtime_keeps_closed_world_tools_and_local_writer(self):
        with Runtime(self.state) as runtime, LocalClient(runtime.config) as client:
            self.assertEqual(runtime.crm_backend, "sqlite")
            self.assertEqual(runtime.connector.crm_backend, "sqlite")
            token = client.authorize(["crm:read"])
            listing = client.rpc(token, "tools/list").json()["result"]["tools"]
            self.assertIn("log_activity", {tool["name"] for tool in listing})
            self.assertTrue(all(not tool["annotations"]["openWorldHint"] for tool in listing))
            account = self.records(client.call(token, "get_account", {"account_id": "acc-001"}))
            self.assertEqual(account[0]["record_ref"], "crm/accounts/acc-001")
        self.assertEqual(self.requests, [])


if __name__ == "__main__":
    unittest.main()
