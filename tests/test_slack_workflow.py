"""Real local HTTPS checks with invented Slack HTTP and independent receipt assertions."""

from contextlib import ExitStack
from copy import deepcopy
import json
import math
import sqlite3
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.audit import AuditLog, read_audit
from gtm_connector.slack_mapping import SlackMappingError
from gtm_connector.slack_workflow import resolve_slack_account, run_slack_check, slack_failure_details
from slack_live_fixtures import ACCOUNT, LiveSlackFixture
from test_slack_auth import NEW_ACCESS, NEW_REFRESH, identity, replacement, saved_document, seed


STEPS = ["private_state", "mapping", "credential_binding", "thread_read", "receipt"]
PRIVATE = "invented-private-workflow-failure"


class SlackCheckTests(LiveSlackFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        with self.runtime() as runtime:
            self.map_account(runtime)
        self.before = self.preserved_files()

    def preserved_files(self):
        return {path.relative_to(self.state).as_posix(): path.read_bytes()
                for path in self.state.rglob("*") if path.is_file() and not path.name.startswith("audit.sqlite3")}

    def check(self, *, account=ACCOUNT, reader_factory=None, progress=None):
        with patch("gtm_connector.slack_reader.SlackThreadReader", reader_factory or self.reader_factory):
            result = run_slack_check(self.state, connection_dir=self.connection, account_id=account, progress=progress)
        self.assert_report(result)
        self.assertEqual(self.preserved_files(), self.before)
        return result

    def assert_report(self, result):
        self.assertEqual(set(result), {"schema_version", "status", "crm_access_checked", "calls_access_checked",
                                       "duration_ms", "api_calls", "response_bytes", "counts", "steps", "next"})
        self.assertFalse(result["crm_access_checked"])
        self.assertFalse(result["calls_access_checked"])
        self.assertEqual([row["name"] for row in result["steps"]], STEPS)
        self.assertTrue(math.isfinite(result["duration_ms"]))
        self.assertGreaterEqual(result["duration_ms"], 0)
        self.assertEqual(set(result["counts"]), {"auth_calls", "message_calls", "pages", "raw_rows", "messages",
                                                "duplicates", "refresh_attempts", "token_retries", "rate_retries"})
        for row in result["steps"]:
            self.assertEqual(set(row), {"name", "status", "count", "duration_ms", "api_calls", "response_bytes",
                                        "code", "message", "next"})
            self.assertIn(row["status"], ("pass", "fail", "not_run"))
            self.assertTrue(math.isfinite(row["duration_ms"]))
            self.assertGreaterEqual(row["duration_ms"], 0)
        encoded = json.dumps(result, allow_nan=False)
        self.assert_no_private_report_values(encoded)
        for value in (PRIVATE, NEW_ACCESS, NEW_REFRESH, str(self.state), str(self.connection)):
            self.assertNotIn(value, encoded)
        if result["status"] == "failed":
            self.assertEqual(result["counts"]["messages"], 0)

    def slack_receipts(self):
        return read_audit(self.state / "audit.sqlite3", tool="read_slack_messages")

    def assert_terminal_error(self, report, phase, code):
        self.assertEqual(report["status"], "failed")
        step = report["steps"][STEPS.index(phase)]
        self.assertEqual((step["status"], step["code"]), ("fail", code))
        self.assertEqual(report["steps"][-1]["status"], "pass")
        receipt = self.slack_receipts()[-1]
        self.assertEqual((receipt["status"], receipt["http_status"], receipt["rows"], receipt["row_count"]),
                         ("error", 200, [], 0))

    def test_success_counts_actual_work_and_never_opens_crm_calls_or_sf(self):
        sentinels = ["gtm_connector.runtime.Runtime.__enter__", "gtm_connector.runtime._prepare_source_state",
                     "gtm_connector.store.connect", "gtm_connector.store.get_account",
                     "gtm_connector.calls.read_call_notes", "gtm_connector.calls.seed",
                     "gtm_connector.calls_import.ImportedCallsReader.read_call_notes",
                     "gtm_connector.salesforce.SalesforceReadOnlyAdapter.from_sf",
                     "gtm_connector.salesforce.SalesforceReadOnlyAdapter.from_env"]
        progress = []
        with ExitStack() as stack:
            mocks = [stack.enter_context(patch(name, side_effect=AssertionError("Forbidden source access")))
                     for name in sentinels]
            report = self.check(progress=lambda step, status: progress.append((step, status)))
        for mock in mocks:
            mock.assert_not_called()
        expected_bytes = len(httpx2.Response(200, json=self.reply).content)
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["api_calls"], 1)
        self.assertEqual(report["response_bytes"], expected_bytes)
        self.assertEqual(report["counts"], {"auth_calls": 0, "message_calls": 1, "pages": 1,
                                           "raw_rows": 3, "messages": 3, "duplicates": 0,
                                           "refresh_attempts": 0, "token_retries": 0, "rate_retries": 0})
        self.assertEqual([row["status"] for row in report["steps"]], ["pass"] * 5)
        self.assertEqual([row["count"] for row in report["steps"]], [1, 1, 1, 3, 1])
        self.assertEqual(report["steps"][3]["response_bytes"], expected_bytes)
        self.assertEqual(progress[0], ("private_state", "running"))
        self.assertIn(("thread_read", "running"), progress)
        self.assertEqual(progress[-1], ("receipt", "pass"))
        receipt, = self.slack_receipts()
        self.assertEqual((receipt["status"], receipt["http_status"], receipt["row_count"]), ("ok", 200, 3))
        self.assertEqual(receipt["arguments"], {"account_id": ACCOUNT})
        self.assertTrue(all(ref.startswith("slack/messages/msg-") for ref in receipt["rows"]))
        self.assertEqual([request.url.path for request in self.requests], ["/api/conversations.replies"])

    def test_missing_account_mapping_is_audited_without_credential_or_provider_work(self):
        with patch("gtm_connector.slack_reader.get_slack_access", side_effect=AssertionError("No credential")) as access:
            report = self.check(account="acc-006")
        self.assert_terminal_error(report, "mapping", "slack_mapping_missing")
        self.assertEqual(report["api_calls"], 0)
        self.assertEqual(report["response_bytes"], 0)
        self.assertEqual(self.requests, [])
        access.assert_not_called()

    def test_expired_unchecked_credential_is_audited_without_any_http_request(self):
        document = saved_document(self.clock.now(), rotating=True, expired=True)
        document["credential_state"] = "identity_unchecked"
        seed(self.connection, document)
        report = self.check()
        self.assert_terminal_error(report, "credential_binding", "slack_reconnect_required")
        self.assertEqual(report["steps"][1]["status"], "pass")
        self.assertEqual(report["steps"][3]["status"], "not_run")
        self.assertEqual(report["api_calls"], 0)
        self.assertEqual(self.requests, [])

    def test_forbidden_provider_response_retains_failed_request_counts_and_error_receipt(self):
        self.reply = {"ok": False, "error": "not_in_channel", "private_detail": PRIVATE}
        report = self.check()
        self.assert_terminal_error(report, "thread_read", "slack_forbidden")
        self.assertEqual(report["api_calls"], 1)
        self.assertEqual(report["response_bytes"], len(httpx2.Response(200, json=self.reply).content))
        self.assertEqual(report["counts"]["message_calls"], 1)
        self.assertEqual(report["counts"]["pages"], 0)

    def test_refresh_and_identity_requests_are_counted_separately_from_message_read(self):
        seed(self.connection, saved_document(self.clock.now(), rotating=True, expired=True))
        report = self.check()
        self.assertEqual(report["status"], "passed")
        self.assertEqual([request.url.path for request in self.requests],
                         ["/api/oauth.v2.access", "/api/auth.test", "/api/conversations.replies"])
        auth_bytes = sum(len(httpx2.Response(200, json=value).content) for value in (replacement(), identity()))
        message_bytes = len(httpx2.Response(200, json=self.reply).content)
        self.assertEqual(report["api_calls"], 3)
        self.assertEqual(report["response_bytes"], auth_bytes + message_bytes)
        self.assertEqual(report["counts"]["auth_calls"], 2)
        self.assertEqual(report["counts"]["message_calls"], 1)
        self.assertEqual(report["counts"]["refresh_attempts"], 1)
        self.assertEqual(report["steps"][2]["response_bytes"], auth_bytes)
        self.assertEqual(report["steps"][3]["response_bytes"], message_bytes)

    def test_failed_authentication_counts_its_work_without_requesting_messages(self):
        seed(self.connection, saved_document(self.clock.now(), rotating=True, expired=True))
        original = self.provider
        failed = {"ok": False, "error": "invalid_auth", "private_detail": PRIVATE}
        def provider(request):
            if request.url.path == "/api/auth.test":
                self.requests.append(request)
                return httpx2.Response(200, json=failed)
            return original(request)
        self.provider = provider
        report = self.check()
        self.assert_terminal_error(report, "credential_binding", "slack_reconnect_required")
        self.assertEqual(report["api_calls"], 2)
        self.assertEqual(report["counts"]["auth_calls"], 2)
        self.assertEqual(report["counts"]["message_calls"], 0)
        expected = sum(len(httpx2.Response(200, json=value).content) for value in (replacement(), failed))
        self.assertEqual(report["response_bytes"], expected)
        self.assertEqual(report["steps"][2]["response_bytes"], expected)
        self.assertEqual(report["steps"][3]["status"], "not_run")

    def test_pending_audit_failure_prevents_source_execution_and_reports_no_success(self):
        with patch.object(AuditLog, "start", side_effect=sqlite3.OperationalError(PRIVATE)), \
                patch("gtm_connector.slack_reader.get_slack_access", side_effect=AssertionError("No credential")) as access:
            report = self.check()
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["steps"][-1]["status"], "fail")
        self.assertEqual(self.slack_receipts(), [])
        self.assertEqual(self.requests, [])
        self.assertEqual(self.source_events, [])
        access.assert_not_called()

    def test_final_audit_failure_withholds_observed_messages_and_leaves_pending_receipt(self):
        from gtm_connector.client import LocalClient
        original_call = LocalClient.call
        responses = []
        def observed(client, *args, **kwargs):
            response = original_call(client, *args, **kwargs)
            responses.append(response)
            return response
        with patch.object(AuditLog, "finish", side_effect=sqlite3.OperationalError(PRIVATE)), \
                patch.object(LocalClient, "call", observed):
            report = self.check()
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["api_calls"], 1)
        self.assertEqual(report["steps"][3]["status"], "fail")
        self.assertEqual(report["steps"][-1]["code"], "slack_receipt_invalid")
        receipt, = self.slack_receipts()
        self.assertEqual(receipt["status"], "pending")
        self.assertEqual(receipt["rows"], [])
        self.assertEqual(len(self.requests), 1)
        response, = responses
        self.assertEqual(response.status_code, 503)
        self.assertEqual(response.json(), {"error": "execution_or_audit_unavailable; no result released"})

    def test_missing_or_malformed_metrics_fail_without_claiming_zero_provider_work(self):
        def missing(event):
            return []
        def duplicate(event):
            return [event, deepcopy(event)]
        def boolean_count(event):
            event["counts"]["api_calls"] = True
            return [event]
        def private_extra(event):
            event[PRIVATE] = PRIVATE
            return [event]
        def inconsistent(event):
            event["counts"]["response_bytes"] += 1
            return [event]
        for mutation in (missing, duplicate, boolean_count, private_extra, inconsistent):
            def factory(path, **options):
                observe = options.pop("on_read")
                def changed(event):
                    for value in mutation(deepcopy(event)):
                        observe(value)
                return self.reader_factory(path, **options, on_read=changed)
            with self.subTest(mutation=mutation.__name__):
                before = len(self.requests)
                report = self.check(reader_factory=factory)
                self.assertEqual(len(self.requests), before + 1)
                self.assertEqual(report["status"], "failed")
                self.assertIsNone(report["api_calls"])
                self.assertIsNone(report["response_bytes"])
                self.assertEqual(report["steps"][3]["code"], "slack_metrics_unavailable")
                self.assertEqual(report["steps"][-1]["status"], "pass")

    def test_valid_but_snapshot_inconsistent_metrics_cannot_report_success(self):
        def factory(path, **options):
            observe = options.pop("on_read")
            def changed(event):
                event = deepcopy(event)
                event["counts"]["messages_seen"] = 2
                event["counts"]["returned_messages"] = 2
                observe(event)
            return self.reader_factory(path, **options, on_read=changed)
        report = self.check(reader_factory=factory)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["api_calls"], 1)
        self.assertEqual(report["steps"][3]["code"], "slack_metrics_unavailable")
        self.assertEqual(report["steps"][-1]["status"], "pass")

    def test_transport_ambiguity_has_unknown_counts_and_no_invented_receipt(self):
        with patch("gtm_connector.client.LocalClient.call", side_effect=httpx2.ReadError(PRIVATE)):
            report = self.check()
        self.assertEqual(report["status"], "failed")
        self.assertIsNone(report["api_calls"])
        self.assertIsNone(report["response_bytes"])
        self.assertEqual(report["steps"][-1]["status"], "fail")
        self.assertEqual(self.slack_receipts(), [])

    def test_forged_receipt_cannot_make_the_check_pass(self):
        original_read = AuditLog.read
        for field, value in (("token_id", "0" * 64), ("arguments", {"account_id": "acc-006"}),
                             ("rows", []), ("status", "pending"), ("audit_id", PRIVATE)):
            def changed(audit, correlation=None):
                rows = original_read(audit, correlation)
                if correlation and correlation.startswith("slack-check-"):
                    rows[0][field] = value
                return rows
            with self.subTest(field=field), patch.object(AuditLog, "read", changed):
                report = self.check()
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["steps"][-1]["code"], "slack_receipt_invalid")
            self.assertEqual(report["api_calls"], 1)

    def test_modified_success_envelope_is_rejected_after_the_actual_audited_read(self):
        from gtm_connector.client import LocalClient
        original_call = LocalClient.call
        def missing_snapshot(value):
            del value["result"]["structuredContent"]["thread_snapshot"]
        def altered_source_text(value):
            value["result"]["structuredContent"]["records"][0]["body"] = PRIVATE
        def partial(value):
            value["result"]["resultType"] = "partial"
        for mutation in (missing_snapshot, altered_source_text, partial):
            def modified(client, *args, **kwargs):
                response = original_call(client, *args, **kwargs)
                value = response.json()
                mutation(value)
                return httpx2.Response(response.status_code, json=value)
            with self.subTest(mutation=mutation.__name__), patch.object(LocalClient, "call", modified):
                report = self.check()
                self.assertEqual(report["status"], "failed")
                self.assertEqual(report["api_calls"], 1)
                self.assertEqual(report["steps"][3]["status"], "fail")
                self.assertEqual(report["steps"][-1]["status"], "fail")
                self.assertEqual(self.slack_receipts()[-1]["status"], "ok")

    def test_invalid_selector_and_missing_state_fail_before_credential_or_service(self):
        with patch("gtm_connector.runtime.SlackCheckRuntime", side_effect=AssertionError("No service")) as runtime:
            with self.assertRaises(SlackMappingError) as error:
                run_slack_check(self.state, connection_dir=self.connection, account_id=PRIVATE)
        self.assertEqual(error.exception.code, "slack_selection_invalid")
        runtime.assert_not_called()
        with patch("gtm_connector.slack_auth.inspect_slack_connection",
                   side_effect=AssertionError("No credential inspection")) as inspect, \
                patch("gtm_connector.slack_reader.get_slack_access", side_effect=AssertionError("No token")) as access:
            missing = self.directory / "missing"
            with self.assertRaises(SlackMappingError):
                run_slack_check(missing, connection_dir=self.connection, account_id=ACCOUNT)
        inspect.assert_not_called()
        access.assert_not_called()
        self.assertFalse(missing.exists())


class SlackResolverTests(LiveSlackFixture, unittest.TestCase):
    def test_real_resolver_projects_only_association_and_requires_matching_receipt(self):
        with self.runtime() as runtime:
            association = resolve_slack_account(runtime, account_id=ACCOUNT)
            self.assertEqual(set(association), {"crm_binding", "account_id", "resolution_receipt_id"})
            self.assertEqual(association["account_id"], ACCOUNT)
            receipt, = runtime.connector.audit.read()
            self.assertEqual(association["resolution_receipt_id"], receipt["audit_id"])
            self.assertEqual((receipt["status"], receipt["tool"]), ("ok", "get_account"))
            original = runtime.connector.audit.read
            def corrupt(correlation=None):
                rows = original(correlation)
                rows[0]["token_id"] = "0" * 64
                return rows
            with patch.object(runtime.connector.audit, "read", corrupt):
                with self.assertRaises(SlackMappingError) as failure:
                    resolve_slack_account(runtime, account_id=ACCOUNT)
            self.assertEqual(failure.exception.code, "slack_association_invalid")
            with self.assertRaises(SlackMappingError) as failure:
                resolve_slack_account(runtime, account_id="acc-999")
            self.assertEqual(failure.exception.code, "slack_account_not_found")
        self.assertEqual(self.requests, [])

    def test_fixed_resolution_failures_keep_slack_recovery_and_do_not_echo_source_values(self):
        from gtm_connector.call_workflow import CRMResolutionError
        from gtm_connector.salesforce import SalesforceReadError
        runtime = SimpleNamespace(
            slack_backend="slack",
            crm_binding={"schema_version": 1, "crm_backend": "sqlite", "identity_sha256": "a" * 64},
            slack_binding={"schema_version": 1, "provider": "slack", "identity_sha256": "b" * 64},
        )
        cases = [(CRMResolutionError(code), expected, exit_code) for code, expected, exit_code in (
            ("crm_selector_required", "slack_selection_invalid", 2),
            ("crm_account_number_invalid", "slack_selection_invalid", 2),
            ("crm_account_id_invalid", "slack_selection_invalid", 2),
            ("crm_source_mismatch", "slack_source_mismatch", 3),
            ("crm_account_not_found", "slack_account_not_found", 2),
            ("crm_account_ambiguous", "slack_account_ambiguous", 3),
            ("crm_association_failed", "slack_association_failed", 4),
            ("crm_association_invalid", "slack_association_invalid", 4),
            ("crm_source_unavailable", "slack_crm_unavailable", 5))]
        cases.extend((SalesforceReadError(code), expected, exit_code) for code, expected, exit_code in (
            ("salesforce_invalid_configuration", "slack_crm_configuration_invalid", 2),
            ("salesforce_environment_mismatch", "slack_source_mismatch", 3),
            ("salesforce_account_number_ambiguous", "slack_account_ambiguous", 3),
            ("salesforce_forbidden", "slack_crm_unavailable", 5)))
        for exception, expected, exit_code in cases:
            with self.subTest(code=exception.code), \
                    patch("gtm_connector.call_workflow.resolve_crm_account", side_effect=exception):
                with self.assertRaises(SlackMappingError) as failure:
                    resolve_slack_account(runtime, account_number=PRIVATE)
            self.assertEqual((failure.exception.code, failure.exception.exit_code), (expected, exit_code))
            message, next_command = slack_failure_details(expected)
            self.assertNotIn(PRIVATE, message + next_command)
            self.assertNotIn("calls import", message + next_command)


if __name__ == "__main__":
    unittest.main()
