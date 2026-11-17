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

    def test_inputs_are_requested_in_order_without_changing_windows(self):
        windows = MappingProxyType({self.key: (90.0, 1)})
        self.assertEqual(check_request(self.request, self.policy, windows, TOOL_DEFINITIONS).needs, "token")
        self.assertEqual(check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                       token=self.token).needs, "wall_time")
        self.assertEqual(check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                       token=self.token, wall_time=1000).needs, "rate_time")
        missing = check_request(self.request, self.policy, windows, TOOL_DEFINITIONS, token=None)
        expired = check_request(self.request, self.policy, windows, TOOL_DEFINITIONS,
                                token=self.token, wall_time=2000)
        for decision in (missing, expired):
            self.assertIsNone(decision.needs)
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
