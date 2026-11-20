"""Exact CRM associations and imported excerpts use the existing scoped MCP tools."""

import asyncio
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import httpx2
from mcp.server.mcpserver.exceptions import ToolError

from gtm_connector.calls_import import parse_transcript, select_excerpt, store_import
from gtm_connector.call_workflow import resolve_call_account
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter


ORIGIN = "https://invented-exact-lookup.my.salesforce.com"
TOKEN = "invented-exact-lookup-token"
ORG_QUERY = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"
ACCOUNT = "001000000000001AAA"
ACCOUNT_ID = "acc-sf-" + ACCOUNT


class ExactAccountNumberTests(unittest.TestCase):
    def setUp(self):
        self.requests = []
        self.number = "INVENTED-001"
        self.count = 1

    def respond(self, request):
        self.requests.append(request)
        self.assertEqual(request.method, "GET")
        self.assertEqual(request.url.host, "invented-exact-lookup.my.salesforce.com")
        self.assertEqual(request.headers["Authorization"], "Bearer " + TOKEN)
        if request.url.params["q"] == ORG_QUERY:
            rows = [{"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition", "IsSandbox": False}]
        else:
            rows = [{"Id": f"001{index + 1:012d}AAA", "Name": "Invented exact account", "Industry": None,
                     "AccountNumber": self.number} for index in range(self.count)]
        return httpx2.Response(200, json={"done": True, "totalSize": len(rows), "records": rows})

    def reader(self):
        return SalesforceReadOnlyAdapter(ORIGIN, TOKEN, transport=httpx2.MockTransport(self.respond))

    def test_exact_literals_are_escaped_without_like_wildcards_or_normalization(self):
        cases = [("Acme'1", "Acme\\'1"), ("A\\B", "A\\\\B"), ("A%_1", "A%_1"),
                 (" 制造-7 ", " 制造-7 "), ("x' OR Name != 'x", "x\\' OR Name != \\'x")]
        for supplied, escaped in cases:
            with self.subTest(supplied=supplied):
                self.requests.clear()
                self.number = supplied
                reader = self.reader()
                rows, more = reader.search_accounts(account_number=supplied)
                self.assertIs(more, False)
                self.assertEqual(rows[0]["account_number"], supplied)
                self.assertEqual(reader.http_requests, 2)
                self.assertEqual(self.requests[-1].url.params["q"],
                                 "SELECT Id, Name, Industry, AccountNumber FROM Account "
                                 f"WHERE AccountNumber = '{escaped}' ORDER BY Id LIMIT 2")
                reader.search_accounts(account_number=supplied)
                self.assertEqual(reader.http_requests, 3)

    def test_zero_and_duplicate_matches_are_bounded_data_and_mismatches_refuse(self):
        for count in (0, 1, 2):
            self.count = count
            self.assertEqual(len(self.reader().search_accounts(account_number=self.number)[0]), count)
        self.count = 3
        with self.assertRaises(SalesforceReadError) as failure:
            self.reader().search_accounts(account_number=self.number)
        self.assertEqual(failure.exception.code, "salesforce_result_limit")
        self.count = 1
        for wrong in ("invented-001", None, "", "INVENTED-001 "):
            self.number = wrong
            with self.subTest(wrong=wrong), self.assertRaises(SalesforceReadError) as failure:
                self.reader().search_accounts(account_number="INVENTED-001")
            self.assertEqual(failure.exception.code, "salesforce_invalid_response" if wrong is None
                             else "salesforce_account_number_ambiguous")
        self.count, self.number = 2, "INVENTED-001"

        def case_variants(request):
            response = self.respond(request)
            if request.url.params["q"] != ORG_QUERY:
                document = response.json()
                document["records"][1]["AccountNumber"] = "invented-001"
                return httpx2.Response(200, json=document)
            return response

        reader = SalesforceReadOnlyAdapter(ORIGIN, TOKEN, transport=httpx2.MockTransport(case_variants))
        with self.assertRaises(SalesforceReadError) as failure:
            reader.search_accounts(account_number="INVENTED-001")
        self.assertEqual(failure.exception.code, "salesforce_account_number_ambiguous")

    def test_invalid_or_combined_selectors_make_no_request(self):
        for value in ("", " ", "x" * 41, "x\n", "x\x00", "x\x7f", "\ud800", 7, [], False):
            with self.subTest(value=repr(value)), self.assertRaises(SalesforceReadError) as failure:
                self.reader().search_accounts(account_number=value)
            self.assertEqual(failure.exception.code, "salesforce_invalid_arguments")
        for query in ("Acme", " "):
            with self.assertRaises(SalesforceReadError):
                self.reader().search_accounts(query, account_number="INVENTED-001")
        self.assertEqual(self.requests, [])

    def test_exact_http_lookup_is_scoped_and_audited_without_contacts_or_contracts(self):
        with tempfile.TemporaryDirectory() as directory:
            reader = self.reader()
            with Runtime(Path(directory) / "state", crm_reader=reader, calls_backend="imported") as runtime, \
                    LocalClient(runtime.config) as client:
                denied = client.authorize(["calls:read"])
                denied_response = client.call(denied, "search_accounts", {"account_number": self.number})
                self.assertEqual(denied_response.status_code, 403)
                self.assertEqual(reader.http_requests, 1)
                crm = client.authorize(["crm:read"])
                for invalid in ({"query": "x", "account_number": self.number}, {"account_number": " "}):
                    self.assertEqual(client.call(crm, "search_accounts", invalid).status_code, 400)
                self.assertEqual(reader.http_requests, 1)
                result = client.call(crm, "search_accounts", {"account_number": self.number}).json()["result"]
                self.assertFalse(result.get("isError"))
                self.assertEqual(result["structuredContent"]["records"][0]["record_ref"], "crm/accounts/" + ACCOUNT_ID)
                self.assertEqual(reader.http_requests, 2)
                receipt = runtime.connector.audit.read()[-1]
                self.assertEqual(receipt["status"], "ok")
                self.assertEqual(receipt["arguments"], {"account_number": self.number})
                self.assertEqual(receipt["rows"], ["crm/accounts/" + ACCOUNT_ID])
                self.assertEqual([request.url.params["q"] for request in self.requests][0], ORG_QUERY)
                self.assertTrue(all("Contact" not in request.url.params["q"] and
                                    "Contract" not in request.url.params["q"] for request in self.requests))

    def test_calls_only_result_never_discloses_the_private_crm_account_number(self):
        with tempfile.TemporaryDirectory() as directory:
            reader = self.reader()
            with Runtime(Path(directory) / "state", crm_reader=reader, calls_backend="imported") as runtime, \
                    LocalClient(runtime.config) as client:
                association = resolve_call_account(runtime, account_number=self.number)
                parsed = parse_transcript(b"Invented authorized transcript excerpt.", format="txt")
                store_import(runtime.imports_path, parsed, select_excerpt(parsed), association=association,
                             called_at=None, imported_at="2027-01-15T12:00:00Z")
                crm = client.authorize(["crm:read"])
                lookup = client.call(crm, "search_accounts", {"account_number": self.number}).json()["result"]
                self.assertEqual(lookup["structuredContent"]["records"][0]["account_number"], self.number)
                self.assertEqual(reader.http_requests, 3)  # One preflight, two authorized exact lookups.
                calls = client.authorize(["calls:read"])
                denied = client.call(calls, "search_accounts", {"account_number": self.number})
                self.assertEqual(denied.status_code, 403)
                response = client.call(calls, "read_call_notes", {"account_id": ACCOUNT_ID})
                self.assertEqual(response.status_code, 200)
                result = response.json()["result"]
                self.assertFalse(result.get("isError"), result)
                self.assertEqual(reader.http_requests, 3)
                self.assertNotIn(self.number, json.dumps(result["content"]))
                self.assertNotIn(self.number, json.dumps(result["structuredContent"]))
                record = result["structuredContent"]["records"][0]
                self.assertEqual(set(record["manifest"]["association"]),
                                 {"crm_binding", "account_id", "method", "resolution_receipt_id"})
                self.assertEqual(record["manifest"]["association"]["method"], "account_number")
                self.assertEqual(runtime.connector.audit.read()[-1]["rows"], [record["record_ref"]])
                self.assertTrue(any(row["arguments"] == {"account_number": self.number}
                                    for row in runtime.connector.audit.read()))
                with closing(sqlite3.connect(runtime.imports_path)) as connection:
                    self.assertEqual(connection.execute("SELECT * FROM number_associations").fetchall(),
                                     [(self.number, ACCOUNT_ID)])
                    self.assertNotIn(self.number, connection.execute("SELECT manifest FROM revisions").fetchone()[0])


class ImportedGatewayTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.state = Path(temporary.name).resolve() / "state"

    def open_runtime(self):
        runtime = self.enterContext(Runtime(self.state, calls_backend="imported"))
        client = self.enterContext(LocalClient(runtime.config))
        return runtime, client

    def import_excerpt(self, runtime):
        association = resolve_call_account(runtime, account_id="acc-007")
        raw = b"Adjacent invented context.\nA literal renewal discussion.\nMore adjacent context.\n" + \
            b"Unselected private content.\n" * 30
        parsed = parse_transcript(raw, format="txt")
        excerpt = select_excerpt(parsed, requested_range=(2, 2))
        return store_import(runtime.imports_path, parsed, excerpt, association=association,
                            called_at=None, imported_at="2027-01-15T12:00:00Z")

    def test_granted_calls_read_returns_only_the_selected_revision_and_a_committed_receipt(self):
        runtime, client = self.open_runtime()
        self.import_excerpt(runtime)
        token = client.authorize(["calls:read"])
        with patch("gtm_connector.gateway.store.connect", side_effect=AssertionError("No CRM read is permitted")):
            response = client.call(token, "read_call_notes", {"account_id": "acc-007"})
        self.assertEqual(response.status_code, 200)
        result = response.json()["result"]
        self.assertFalse(result.get("isError"), result)
        rows = result["structuredContent"]["records"]
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0]["record_kind"], "imported_transcript")
        self.assertEqual(rows[0]["body"],
                         "Adjacent invented context.\nA literal renewal discussion.\nMore adjacent context.")
        self.assertNotIn("Unselected private content", response.text)
        self.assertNotIn("raw_base64", response.text)
        receipt = runtime.connector.audit.read()[-1]
        self.assertEqual(receipt["status"], "ok")
        self.assertEqual(receipt["rows"], [rows[0]["record_ref"]])
        self.assertFalse((self.state / "calls.sqlite3").exists())
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection:
            self.assertEqual(connection.execute("SELECT COUNT(*) FROM activities").fetchone()[0], 0)

    def test_denied_calls_and_initial_audit_failure_never_read_blobs_or_fixture_calls(self):
        runtime, client = self.open_runtime()
        self.import_excerpt(runtime)
        crm = client.authorize(["crm:read"])
        calls = client.authorize(["calls:read"])
        with patch("gtm_connector.calls_import._load_record") as blobs, \
                patch("gtm_connector.gateway.calls.connect") as fixture:
            self.assertEqual(client.call(crm, "read_call_notes", {"account_id": "acc-007"}).status_code, 403)
            failure = sqlite3.OperationalError("private failure")
            with patch.object(runtime.connector.audit, "start", side_effect=failure):
                response = client.call(calls, "read_call_notes", {"account_id": "acc-007"})
                self.assertEqual(response.status_code, 503)
            blobs.assert_not_called()
            fixture.assert_not_called()

    def test_missing_import_is_a_fixed_audited_error_not_empty_fixture_history(self):
        runtime, client = self.open_runtime()
        token = client.authorize(["calls:read"])
        result = client.call(token, "read_call_notes", {"account_id": "acc-007"}).json()["result"]
        self.assertEqual((result.get("isError", False), result["structuredContent"]["records"]), (False, []))
        self.assertEqual(runtime.connector.audit.read()[-1]["status"], "ok")
        self.assertEqual(runtime.connector.audit.read()[-1]["rows"], [])

    def test_direct_sdk_scope_and_selector_checks_precede_source_io(self):
        runtime, client = self.open_runtime()
        with patch("gtm_connector.gateway.get_access_token", return_value=None), \
                patch.object(runtime.calls_reader, "read_call_notes") as reader:
            with self.assertRaises(ToolError):
                asyncio.run(runtime.connector.mcp.call_tool("read_call_notes", {"account_id": "acc-007"}))
            reader.assert_not_called()
        with patch("gtm_connector.gateway.get_access_token", return_value=SimpleNamespace(scopes=["crm:read"])), \
                patch("gtm_connector.gateway.store.connect") as store:
            for arguments in ({"query": "x", "account_number": "INVENTED"}, {"account_number": "INVENTED"}):
                with self.assertRaises(ToolError):
                    asyncio.run(runtime.connector.mcp.call_tool("search_accounts", arguments))
            store.assert_not_called()
        token = client.authorize(["crm:read"])
        tools = client.rpc(token, "tools/list").json()["result"]["tools"]
        self.assertEqual(len(tools), 6)
        search = next(tool for tool in tools if tool["name"] == "search_accounts")
        self.assertIn("account_number", search["inputSchema"]["properties"])
        self.assertEqual(search["_meta"]["gtm.demo/requiredScope"], "crm:read")
        self.assertEqual(json.loads(json.dumps(runtime.config))["calls_backend"], "imported")


if __name__ == "__main__":
    unittest.main()
