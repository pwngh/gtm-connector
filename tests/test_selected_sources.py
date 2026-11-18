"""Selected sources and caller-run briefs over real loopback HTTPS with invented fixtures."""

from concurrent.futures import ThreadPoolExecutor
from contextlib import closing
import json
import os
from pathlib import Path
import select
import signal
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest import mock

import httpx2

from gtm_connector.calls_import import parse_transcript, select_excerpt, store_import
from gtm_connector.call_workflow import resolve_call_account
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime, SourceIdentityError
from gtm_connector.salesforce import SalesforceReadOnlyAdapter
from gtm_connector.selected_briefs import SelectedContext
from gtm_connector.selection import SelectionError, load_selection, parse_selection


SELECTED_TOOLS = {"search_accounts", "get_account", "list_opportunities", "read_call_notes",
                  "read_slack_messages", "prepare_renewal_brief", "read_renewal_brief"}


def _result(response):
    if response.status_code != 200:
        raise AssertionError(f"HTTP {response.status_code}: {response.text}")
    return response.json()["result"]


class SelectedFixture:
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.state = self.root / "state"

    def selection(self, accounts, name="selection.json"):
        path = self.root / name
        path.write_text(json.dumps({"schema_version": 1, "accounts": accounts}))
        os.chmod(path, 0o600)
        return load_selection(path, crm_backend="sqlite")

    def ok(self, response):
        result = _result(response)
        self.assertFalse(result.get("isError"), result)
        self.assertTrue(result["structuredContent"]["untrusted_data"])
        return result["structuredContent"]

    def refused(self, response, code):
        result = _result(response)
        self.assertTrue(result.get("isError"), result)
        self.assertEqual(result["content"][0]["text"].rsplit(": ", 1)[-1], code)
        return response.text

    def artifacts(self):
        folder = self.state / "selected-briefs"
        return sorted(path.name for path in folder.iterdir()) if folder.exists() else []


class SelectedSourceTests(SelectedFixture, unittest.TestCase):
    def test_remote_brief_runs_under_the_callers_token_never_the_owners(self):
        selection = self.selection([{"account_id": "acc-007", "slack": True}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            # The owner provisions the grant before the remote call, as `connect` does.
            token = owner.authorize(["crm:read"])
            refuse_owner = AssertionError("the remote path used owner authority")
            with mock.patch.object(LocalClient, "authorize", side_effect=refuse_owner), \
                    mock.patch("gtm_connector.doctor.load_local_owner", side_effect=refuse_owner):
                prepared = self.ok(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}))
            brief = prepared["brief"]
            self.assertIn("Call notes were unavailable because the token lacks calls:read.", brief["text"])
            self.assertIn("Slack messages were unavailable because the token lacks slack:read.", brief["text"])
            self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", json.dumps(prepared))

            receipts = runtime.connector.audit.read()
            outer = [row for row in receipts if row["tool"] == "prepare_renewal_brief"]
            self.assertEqual(len(outer), 1)
            self.assertEqual((outer[0]["status"], outer[0]["http_status"]), ("ok", 200))
            self.assertEqual(outer[0]["rows"], [row["record_ref"] for row in prepared["records"]])
            children = [row for row in receipts if row["brief_id"] == brief["brief_id"]]
            self.assertEqual([row["tool"] for row in children],
                             ["get_account", "list_opportunities", "read_call_notes", "read_slack_messages"])
            self.assertEqual({row["token_id"] for row in children}, {outer[0]["token_id"]})
            self.assertEqual([row["request_id"] for row in children],
                             [{"kind": "brief_child", "parent_audit_id": outer[0]["audit_id"], "ordinal": number}
                              for number in range(1, 5)])
            self.assertEqual([(row["status"], row["http_status"]) for row in children],
                             [("ok", 200), ("ok", 200), ("error", 403), ("error", 403)])

    def test_unselected_accounts_never_reach_a_caller(self):
        selection = self.selection([{"account_id": "acc-007", "slack": False},
                                    {"account_id": "acc-012", "slack": True}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read", "crm:write"])
            listing = _result(owner.rpc(token, "tools/list"))
            self.assertEqual({tool["name"] for tool in listing["tools"]}, SELECTED_TOOLS)

            found = self.ok(owner.call(token, "search_accounts", {"query": ""}))
            self.assertEqual({row["id"] for row in found["records"]}, {"acc-007", "acc-012"})
            opportunities = self.ok(owner.call(token, "list_opportunities", {}))
            self.assertEqual({row["account_id"] for row in opportunities["records"]}, {"acc-007", "acc-012"})

            texts = []
            for tool in ("get_account", "list_opportunities", "read_call_notes", "read_slack_messages",
                         "prepare_renewal_brief"):
                texts.append(self.refused(owner.call(token, tool, {"account_id": "acc-001"}),
                                          "account_not_selected"))
            texts.append(self.refused(owner.call(token, "read_slack_messages", {"account_id": "acc-007"}),
                                      "slack_not_selected"))
            written = owner.call(token, "log_activity", {"account_id": "acc-007", "kind": "note",
                                                          "body": "x", "idempotency_key": "k-1"})
            self.assertEqual(written.status_code, 403, written.text)
            self.assertEqual(written.json()["error"]["data"]["reason"], "crm_write_disabled")
            for text in texts + [json.dumps(found), json.dumps(opportunities)]:
                self.assertNotIn("Alder Vale Packaging", text)
                self.assertNotIn("Mira Vale", text)
            self.assertEqual(runtime.connector.audit.read()[-1]["tool"], "log_activity")

    def test_a_saved_brief_needs_current_scopes_selection_and_bindings(self):
        both = self.selection([{"account_id": "acc-007", "slack": True}])
        with Runtime(self.state, selection=both) as runtime, LocalClient(runtime.config) as owner:
            full = owner.authorize(["crm:read", "calls:read", "slack:read"])
            prepared = self.ok(owner.call(full, "prepare_renewal_brief", {"account_id": "acc-007"}))["brief"]
            brief_id = prepared["brief_id"]
            released = self.ok(owner.call(full, "read_renewal_brief", {"brief_id": brief_id}))["brief"]
            self.assertEqual(released["text"], prepared["text"])
            self.assertIs(released["provider_rechecked"], False)
            self.assertIn("not rechecked", released["notice"])
            crm_only = owner.authorize(["crm:read"])
            self.refused(owner.call(crm_only, "read_renewal_brief", {"brief_id": brief_id}),
                         "brief_scope_not_covered")

        cases = [([{"account_id": "acc-012", "slack": True}], "account_not_selected"),
                 ([{"account_id": "acc-007", "slack": False}], "slack_not_selected"),
                 ([{"account_id": "acc-007", "slack": True}, {"account_id": "acc-012", "slack": False}], None)]
        for number, (accounts, refusal) in enumerate(cases):
            with self.subTest(accounts=accounts):
                selection = self.selection(accounts, name=f"selection-{number}.json")
                with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
                    full = owner.authorize(["crm:read", "calls:read", "slack:read"])
                    response = owner.call(full, "read_renewal_brief", {"brief_id": brief_id})
                    if refusal is None:
                        self.assertEqual(self.ok(response)["brief"]["text"], prepared["text"])
                    else:
                        self.assertNotIn(prepared["text"][:80], self.refused(response, refusal))


    def test_composite_reads_recheck_the_rate_window_and_the_token(self):
        selection = self.selection([{"account_id": "acc-007", "slack": True}])
        with Runtime(self.state, selection=selection, rate_limit=3) as runtime, \
                LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            # The outer call and the first two reads use the three attempts; the third read is refused.
            self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}), "rate_limited")
            receipts = runtime.connector.audit.read()
            self.assertEqual([(row["tool"], row["status"], row["http_status"]) for row in receipts],
                             [("prepare_renewal_brief", "error", 200), ("get_account", "ok", 200),
                              ("list_opportunities", "ok", 200), ("read_call_notes", "error", 429)])
            self.assertEqual(self.artifacts(), [])

        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read", "slack:read"])
            connector = runtime.connector
            real = connector.authority.verify_token_now
            reads = dict(connector._source_reads)
            expired = []

            def account_then_expire(account_id):
                result = reads["get_account"](account_id)
                expired.append(True)
                return result

            connector._source_reads = {**reads, "get_account": account_then_expire}
            with mock.patch.object(connector.authority, "verify_token_now",
                                   side_effect=lambda raw: None if expired else real(raw)):
                self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                             "invalid_token")
            children = [row for row in runtime.connector.audit.read() if row["request_id"] is not None
                        and isinstance(row["request_id"], dict)]
            self.assertEqual([(row["tool"], row["http_status"]) for row in children[-2:]],
                             [("get_account", 200), ("list_opportunities", 401)])
            self.assertEqual(self.artifacts(), [])

    def test_child_audit_failures_stop_before_source_work_or_release(self):
        selection = self.selection([{"account_id": "acc-007", "slack": False}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            connector = runtime.connector
            start = connector.audit.start
            opened = []
            reads = dict(connector._source_reads)

            def spy(name):
                def read(account_id):
                    opened.append(name)
                    return reads[name](account_id)
                return read

            connector._source_reads = {name: spy(name) for name in reads}
            starts = []

            def fail_second_child(**kwargs):
                starts.append(kwargs["tool"])
                if len(starts) == 3:
                    raise sqlite3.OperationalError("database is locked")
                return start(**kwargs)

            with mock.patch.object(connector.audit, "start", side_effect=fail_second_child):
                self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                             "brief_source_failed")
            self.assertEqual(starts, ["prepare_renewal_brief", "get_account", "list_opportunities"])
            self.assertEqual(opened, ["get_account"])
            self.assertEqual(self.artifacts(), [])

            finish = connector.audit.finish

            def fail_notes_finish(audit_id, **kwargs):
                if [row for row in connector.audit.read() if row["audit_id"] == audit_id][0]["tool"] \
                        == "read_call_notes":
                    raise sqlite3.OperationalError("disk I/O error")
                return finish(audit_id, **kwargs)

            with mock.patch.object(connector.audit, "finish", side_effect=fail_notes_finish):
                self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                             "brief_source_failed")
            self.assertEqual(self.artifacts(), [])
            pending = [row for row in connector.audit.read() if row["status"] == "pending"]
            self.assertEqual([row["tool"] for row in pending], ["read_call_notes"])

    def test_concurrent_callers_keep_their_own_grants(self):
        selection = self.selection([{"account_id": "acc-007", "slack": False},
                                    {"account_id": "acc-012", "slack": False}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            tokens = {"with_calls": owner.authorize(["crm:read", "calls:read"]),
                      "crm_only": owner.authorize(["crm:read"])}

            def prepare(name, account_id):
                with LocalClient(runtime.config) as client:
                    return name, self.ok(client.call(tokens[name], "prepare_renewal_brief",
                                                     {"account_id": account_id}))["brief"]

            jobs = [("with_calls", "acc-007"), ("crm_only", "acc-012")] * 3
            with ThreadPoolExecutor(max_workers=6) as pool:
                results = list(pool.map(lambda job: prepare(*job), jobs))
            receipts = runtime.connector.audit.read()
            fingerprints = {row["audit_id"]: row["token_id"] for row in receipts}
            for name, brief in results:
                self.assertEqual(brief["notes_status"], "available" if name == "with_calls" else "denied")
                children = [row for row in receipts if row["brief_id"] == brief["brief_id"]]
                self.assertEqual(len(children), 3)
                parents = {row["request_id"]["parent_audit_id"] for row in children}
                self.assertEqual(len(parents), 1)
                self.assertEqual({row["token_id"] for row in children}, {fingerprints[parents.pop()]})

    def test_tampered_spoofed_or_unaccepted_briefs_are_refused(self):
        selection = self.selection([{"account_id": "acc-007", "slack": False}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            # Caller metadata cannot choose the children's correlation or add receipts to a brief.
            prepared = self.ok(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"},
                                          brief_id="brief-" + "0" * 32))["brief"]
            brief_id = prepared["brief_id"]
            self.assertNotEqual(brief_id, "brief-" + "0" * 32)
            self.ok(owner.call(token, "read_call_notes", {"account_id": "acc-007"}, brief_id=brief_id))
            self.ok(owner.call(token, "read_renewal_brief", {"brief_id": brief_id}))

            for brief, code in (("brief-" + "f" * 32, "brief_not_found"), ("../owner", "brief_not_found")):
                self.refused(owner.call(token, "read_renewal_brief", {"brief_id": brief}), code)
            path = self.state / "selected-briefs" / (brief_id + ".json")
            original = path.read_bytes()
            path.write_bytes(original.replace(b"acc-007 has", b"acc-007 had", 1)
                             if b"acc-007 has" in original else original + b" ")
            self.refused(owner.call(token, "read_renewal_brief", {"brief_id": brief_id}), "brief_not_accepted")
            path.write_bytes(original)
            outer = [row for row in runtime.connector.audit.read() if row["tool"] == "prepare_renewal_brief"][0]
            with closing(sqlite3.connect(runtime.directory / "audit.sqlite3")) as connection, connection:
                connection.execute("UPDATE audit_log SET status = 'error' WHERE audit_id = ?", (outer["audit_id"],))
            self.refused(owner.call(token, "read_renewal_brief", {"brief_id": brief_id}), "brief_not_accepted")

    def test_a_replaced_transcript_denies_the_old_brief(self):
        def import_excerpt(runtime, line):
            association = resolve_call_account(runtime, account_id="acc-007")
            raw = b"Invented context.\n" + line + b"\nMore invented context.\n"
            parsed = parse_transcript(raw, format="txt")
            return store_import(runtime.imports_path, parsed, select_excerpt(parsed, requested_range=(2, 2)),
                                association=association, called_at=None, imported_at="2027-01-15T12:00:00Z")

        selection = self.selection([{"account_id": "acc-007", "slack": False}])
        self.state = self.root.resolve() / "imported-state"
        with Runtime(self.state, calls_backend="imported", selection=selection) as runtime, \
                LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                         "calls_not_imported")
            self.assertEqual(self.artifacts(), [])
            import_excerpt(runtime, b"A first invented renewal discussion.")
            prepared = self.ok(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}))["brief"]
            self.assertEqual(prepared["sources"]["calls"], "imported_transcript")
            self.ok(owner.call(token, "read_renewal_brief", {"brief_id": prepared["brief_id"]}))
            import_excerpt(runtime, b"A replacement invented renewal discussion.")
            self.refused(owner.call(token, "read_renewal_brief", {"brief_id": prepared["brief_id"]}),
                         "brief_source_changed")

    def test_selection_files_are_strict_and_owner_only(self):
        good = {"schema_version": 1, "accounts": [{"account_id": "acc-012", "slack": False},
                                                  {"account_id": "acc-007", "slack": True}]}
        selection = parse_selection(json.dumps(good).encode(), crm_backend="sqlite")
        self.assertEqual(selection.account_ids, ("acc-007", "acc-012"))
        self.assertEqual(selection.slack_accounts, frozenset({"acc-007"}))
        entry = {"account_id": "acc-007", "slack": False}
        bad = [
            b"{}", b"[]", b"not json", b'{"schema_version": 1, "accounts": [], "x": 1}',
            json.dumps({"schema_version": 2, "accounts": [entry]}).encode(),
            json.dumps({"schema_version": 1, "accounts": []}).encode(),
            json.dumps({"schema_version": 1, "accounts": [entry, entry]}).encode(),
            json.dumps({"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": 1}]}).encode(),
            json.dumps({"schema_version": 1, "accounts": [{"account_id": "acc-007"}]}).encode(),
            json.dumps({"schema_version": 1, "accounts": [{"account_id": "acc-sf-" + "A" * 18,
                                                          "slack": False}]}).encode(),
            json.dumps({"schema_version": 1, "accounts": [
                {"account_id": f"acc-{number:03}", "slack": False} for number in range(101)]}).encode(),
            b'{"schema_version": 1, "schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": false}]}',
            b'{"schema_version": NaN, "accounts": []}',
            b" " * 65537,
        ]
        for document in bad:
            with self.subTest(document=document[:60]), self.assertRaises(SelectionError):
                parse_selection(document, crm_backend="sqlite")
        with self.assertRaises(SelectionError):
            parse_selection(json.dumps(good).encode(), crm_backend="salesforce")

        path = self.root / "owner-only.json"
        path.write_text(json.dumps(good))
        os.chmod(path, 0o666)
        with self.assertRaises(SelectionError):
            load_selection(path, crm_backend="sqlite")
        os.chmod(path, 0o600)
        link = self.root / "link.json"
        link.symlink_to(path)
        with self.assertRaises(SelectionError):
            load_selection(link, crm_backend="sqlite")
        with self.assertRaises(SelectionError):
            load_selection(self.root / "missing.json", crm_backend="sqlite")
        self.assertEqual(load_selection(path, crm_backend="sqlite").accounts, frozenset({"acc-007", "acc-012"}))
        with self.assertRaises(SourceIdentityError):
            Runtime(self.state, selection=selection, crm_reader=SimpleNamespace(name="salesforce"))


class SelectedLimitsTests(SelectedFixture, unittest.TestCase):
    def test_the_deadline_discards_late_reads_and_saved_files_are_private(self):
        selection = self.selection([{"account_id": "acc-007", "slack": False}])
        with Runtime(self.state, selection=selection) as runtime, LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            connector = runtime.connector
            with mock.patch("gtm_connector.gateway.BRIEF_SECONDS", 0):
                self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                             "brief_deadline_exceeded")
            self.assertEqual([row["tool"] for row in connector.audit.read()], ["prepare_renewal_brief"])

            reads = dict(connector._source_reads)

            def slow_account(account_id):
                time.sleep(0.5)
                return reads["get_account"](account_id)

            connector._source_reads = {**reads, "get_account": slow_account}
            with mock.patch("gtm_connector.gateway.BRIEF_SECONDS", 0.3):
                self.refused(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}),
                             "brief_deadline_exceeded")
            connector._source_reads = reads
            self.assertEqual(self.artifacts(), [])

            brief = self.ok(owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"}))["brief"]
            path = self.state / "selected-briefs" / (brief["brief_id"] + ".json")
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            self.assertEqual(stat.S_IMODE(path.parent.stat().st_mode), 0o700)
            with mock.patch.object(SelectedContext, "bindings", return_value={"crm_backend": "changed"}):
                self.refused(owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}),
                             "brief_source_changed")
            self.ok(owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}))

    def test_serve_reports_the_selection_and_checks_the_file_before_credentials(self):
        path = self.root / "selection.json"
        path.write_text(json.dumps({"schema_version": 1, "accounts": [
            {"account_id": "acc-007", "slack": True}, {"account_id": "acc-012", "slack": False}]}))
        os.chmod(path, 0o600)
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--state-dir", str(self.state),
             "--selection", str(path)], cwd=ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            ready, _, _ = select.select([process.stdout], [], [], 60)
            self.assertTrue(ready, "serve did not report readiness")
            started = json.loads(process.stdout.readline())
            self.assertEqual(started["selection"], {"accounts": 2, "slack_accounts": 1,
                                                    "tools": sorted(SELECTED_TOOLS)})
        finally:
            process.send_signal(signal.SIGINT)
            process.communicate(timeout=60)

        fake = self.root / "bin"
        fake.mkdir()
        marker = self.root / "sf-was-run"
        (fake / "sf").write_text(f"#!/bin/sh\ntouch {marker}\nexit 1\n")
        (fake / "sf").chmod(0o700)
        bad = self.root / "bad.json"
        bad.write_text('{"schema_version": 1, "accounts": []}')
        os.chmod(bad, 0o600)
        result = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--crm-backend", "salesforce",
             "--sf-org", "invented-alias", "--state-dir", str(self.root / "sf-state"), "--selection", str(bad)],
            cwd=ROOT, capture_output=True, text=True, timeout=60, check=False,
            env={**os.environ, "PATH": f"{fake}{os.pathsep}{os.environ['PATH']}"})
        self.assertEqual(result.returncode, 2, result.stderr)
        self.assertIn("selection", (result.stdout + result.stderr).lower())
        self.assertFalse(marker.exists())
        self.assertFalse((self.root / "sf-state").exists())


ROOT = Path(__file__).resolve().parents[1]
SF_ACCOUNT, SF_OTHER = "001000000000001AAA", "001000000000002AAA"
SF_INSTANCE = "https://fixture-unit-test.my.salesforce.com"
SF_TOKEN = "invented-salesforce-token-for-http-fixtures"
SF_ORGANIZATION = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"


class SelectedSalesforceTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name)
        self.queries = []
        self.reader = SalesforceReadOnlyAdapter(SF_INSTANCE, SF_TOKEN, transport=httpx2.MockTransport(self.respond))

    def respond(self, request):
        query = request.url.params["q"]
        self.queries.append(query)
        if query == SF_ORGANIZATION:
            rows = [{"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition", "IsSandbox": False}]
        elif " FROM Account" in query:
            rows = [{"Id": SF_ACCOUNT, "Name": "Selected invented account", "Industry": None,
                     "AccountNumber": None}]
        elif " FROM Contact" in query:
            rows = []
        elif " FROM Opportunity" in query:
            rows = [{"Id": "006000000000001AAA", "AccountId": SF_ACCOUNT, "Name": "Invented renewal",
                     "StageName": "Proposal", "Amount": 1000, "CloseDate": "2027-01-31",
                     "IsClosed": False, "IsWon": False}]
        elif " FROM Contract" in query:
            rows = [{"Id": "800000000000001AAA", "AccountId": SF_ACCOUNT, "Status": "Draft",
                     "StatusCode": "Draft", "StartDate": "2026-02-01", "ContractTerm": 12,
                     "EndDate": "2027-01-31"}]
        else:
            raise AssertionError("unexpected query")
        return httpx2.Response(200, json={"totalSize": len(rows), "done": True, "records": rows})

    def test_selection_limits_salesforce_queries_and_rereads_make_no_requests(self):
        path = self.root / "selection.json"
        path.write_text(json.dumps({"schema_version": 1, "accounts": [
            {"account_id": "acc-sf-" + SF_ACCOUNT, "slack": False}]}))
        os.chmod(path, 0o600)
        selection = load_selection(path, crm_backend="salesforce")
        with Runtime(self.root / "state", crm_reader=self.reader, selection=selection) as runtime, \
                LocalClient(runtime.config) as owner:
            token = owner.authorize(["crm:read", "calls:read"])
            owner.call(token, "search_accounts", {"query": "invented"})
            owner.call(token, "list_opportunities", {})
            search, listing = self.queries[-2:]
            self.assertIn(f"WHERE Name LIKE '%invented%' AND Id IN ('{SF_ACCOUNT}')", search)
            self.assertIn(f"WHERE AccountId IN ('{SF_ACCOUNT}')", listing)
            before = len(self.queries)
            response = owner.call(token, "get_account", {"account_id": "acc-sf-" + SF_OTHER})
            self.assertIn("account_not_selected", response.text)
            self.assertEqual(len(self.queries), before)
            prepared = _result(owner.call(token, "prepare_renewal_brief",
                                          {"account_id": "acc-sf-" + SF_ACCOUNT, "as_of": "2027-01-15"}))
            self.assertFalse(prepared.get("isError"), prepared)
            brief = prepared["structuredContent"]["brief"]
            self.assertEqual(brief["sources"]["crm"], "salesforce")
            before = len(self.queries)
            reread = _result(owner.call(token, "read_renewal_brief", {"brief_id": brief["brief_id"]}))
            self.assertFalse(reread.get("isError"), reread)
            self.assertEqual(reread["structuredContent"]["brief"]["text"], brief["text"])
            self.assertEqual(len(self.queries), before)


if __name__ == "__main__":
    unittest.main()
