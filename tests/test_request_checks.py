"""Decisions are repeatable and leave caller-owned request and rate state intact."""

from copy import deepcopy
from dataclasses import replace
import hashlib
from types import MappingProxyType
import unittest

from mcp.server.auth.provider import AccessToken

from gtm_connector.request_checks import RequestFacts, RequestPolicy, check_request
from gtm_connector.tool_definitions import TOOL_DEFINITIONS


class RequestCheckTests(unittest.TestCase):
    def setUp(self):
        self.raw = "fixture-pure-check-token"
        self.key = hashlib.sha256(self.raw.encode()).hexdigest()
        self.policy = RequestPolicy("https://127.0.0.1:8765/mcp", "https://127.0.0.1:8765/metadata", 2, 60, False)
        self.request = RequestFacts(
            body_size=100, parsed={}, scheme="https",
            headers={"host": "127.0.0.1:8765", "mcp-protocol-version": "2026-07-28"},
            header_names=(b"host", b"authorization"), raw_token=self.raw, is_call=True,
            tool="get_account", arguments={"account_id": "acc-001"},
            meta={"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
        )
        self.token = AccessToken(token=self.raw, client_id="fixture-client", scopes=["crm:read"],
                                 expires_at=2000, resource=self.policy.resource)

    def test_refusals_have_fixed_reasons_statuses_and_codes(self):
        write_token = AccessToken(token=self.raw, client_id="fixture-client", scopes=["crm:write"],
                                  expires_at=2000, resource=self.policy.resource)
        cases = (
            ("request_too_large", 413, -32600, {"request": replace(self.request, body_size=65537)}),
            ("invalid_json", 400, -32700, {"request": replace(self.request, parsed=None)}),
            ("untrusted_origin_or_host", 403, 1003, {"request": replace(self.request, scheme="http")}),
            ("invalid_token", 401, 1001, {"token": None}),
            ("rate_limited", 429, 1002, {"windows": {self.key: (90.0, 2)}}),
            ("insufficient_scope", 403, 1004, {"request": replace(self.request, tool="read_call_notes")}),
            ("crm_write_disabled", 403, 1005, {
                "request": replace(self.request, tool="log_activity"),
                "policy": replace(self.policy, writes_disabled=True), "token": write_token,
            }),
            ("invalid_arguments", 400, -32602, {"request": replace(self.request, arguments={})}),
            ("missing_protocol_version", 400, -32602, {"request": replace(self.request, meta={})}),
            ("protocol_version_mismatch", 400, -32020, {
                "request": replace(self.request, headers={"host": "127.0.0.1:8765"}),
            }),
            ("unsupported_protocol_version", 400, -32022, {
                "request": replace(self.request,
                                   headers={"host": "127.0.0.1:8765", "mcp-protocol-version": "2099-01-01"},
                                   meta={"io.modelcontextprotocol/protocolVersion": "2099-01-01"}),
            }),
        )
        for reason, status, code, overrides in cases:
            with self.subTest(reason=reason):
                decision = check_request(
                    overrides.get("request", self.request), overrides.get("policy", self.policy),
                    overrides.get("windows", {}), TOOL_DEFINITIONS,
                    token=overrides.get("token", self.token), wall_time=1000, rate_time=100.0,
                )
                self.assertEqual((decision.error.reason, decision.error.status, decision.error.code),
                                 (reason, status, code))

    def test_a_missing_or_expired_token_is_refused_without_charging_the_window(self):
        windows = MappingProxyType({self.key: (90.0, 1)})
        missing = check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                token=None, wall_time=1000, rate_time=100.0)
        expired = check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                token=self.token, wall_time=2000, rate_time=100.0)
        for decision in (missing, expired):
            self.assertEqual(decision.error.status, 401)
            self.assertIsNone(decision.windows)
        self.assertEqual(dict(windows), {self.key: (90.0, 1)})

    def test_repeated_denial_returns_same_quota_update_without_mutating_inputs(self):
        request = replace(self.request, tool="read_call_notes")
        windows = {self.key: (90.0, 0), "expired-fixture-window": (40.0, 1)}
        before = deepcopy((request, self.policy, self.token, windows, TOOL_DEFINITIONS))
        first = check_request(request, self.policy, windows, TOOL_DEFINITIONS,
                              token=self.token, wall_time=1000, rate_time=100.0)
        second = check_request(request, self.policy, windows, TOOL_DEFINITIONS,
                               token=self.token, wall_time=1000, rate_time=100.0)
        self.assertEqual(first, second)
        self.assertEqual(first.error.status, 403)
        self.assertIn('scope="calls:read"', first.error.headers["WWW-Authenticate"])
        self.assertEqual(first.windows, {self.key: (90.0, 1)})
        self.assertIsNot(first.windows, windows)
        self.assertEqual((request, self.policy, self.token, windows, TOOL_DEFINITIONS), before)

    def test_limited_call_prunes_expired_windows_without_incrementing(self):
        windows = MappingProxyType({self.key: (90.0, 2), "expired-fixture-window": (40.0, 1)})
        decision = check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                 token=self.token, wall_time=1000, rate_time=100.0)
        self.assertEqual(decision.error.status, 429)
        self.assertEqual(decision.error.headers, {"Retry-After": "50"})
        self.assertEqual(decision.windows, {self.key: (90.0, 2)})
        self.assertEqual(dict(windows), {self.key: (90.0, 2), "expired-fixture-window": (40.0, 1)})


if __name__ == "__main__":
    unittest.main()
