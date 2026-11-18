"""Only invented HTTP and a fake sf executable on PATH are used here."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import traceback
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from gtm_connector.sf_credentials import SFCredentials, SFCredentialsError


ORIGIN = "https://credential-fixture.develop.my.salesforce.com"
TOKEN = "fixture-only-first-token"
FRESH_TOKEN = "fixture-only-fresh-token"
EMPTY = {"totalSize": 0, "done": True, "records": []}
ORG = {"totalSize": 1, "done": True,
       "records": [{"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition"}]}
REAL_RUN = subprocess.run


class SFCredentialsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        script = self.directory / "sf"
        script.write_text(f"#!{sys.executable}\n" + '''
import json, os, pathlib, sys
directory = pathlib.Path(__file__).parent
counter = directory / "count"
number = int(counter.read_text()) + 1 if counter.exists() else 1
counter.write_text(str(number))
assert sys.stdin.read() == ""
assert sys.argv[1:] in (["org", "display", "--target-org", "invented-alias", "--json"],
    ["org", "auth", "show-access-token", "--target-org", "invented-alias", "--json"])
for key in ("SF_DISABLE_TELEMETRY", "SF_AUTOUPDATE_DISABLE", "SF_DISABLE_AUTOUPDATE",
            "SF_DISABLE_LOG_FILE", "SFDX_DISABLE_LOG_FILE"):
    assert os.environ[key] == "true"
mode = os.environ.get("FAKE_SF_MODE", "good")
if mode == "nonzero" or (mode == "refresh_failure" and number > 2):
    print("fixture-only-child-secret", file=sys.stderr)
    print("fixture-only-child-secret")
    sys.exit(7)
if mode == "malformed":
    print("fixture-only-child-secret")
    sys.exit(0)
if mode == "missing":
    print('{"status":0,"result":{}}')
    sys.exit(0)
if mode == "bad_status":
    print('{"status":true,"result":{"accessToken":"fixture-only-child-secret"}}')
    sys.exit(0)
if mode == "duplicate":
    print('{"status":0,"status":0,"result":{}}')
    sys.exit(0)
origin = "https://credential-fixture.develop.my.salesforce.com"
if mode == "changed_origin" and number > 2:
    origin = "https://different-fixture.develop.my.salesforce.com"
if mode == "invalid_origin":
    origin = "https://untrusted.example"
token = "fixture-only-first-token" if number <= 2 else "fixture-only-fresh-token"
value = {"instanceUrl": origin} if sys.argv[2] == "display" else {"accessToken": token}
print(json.dumps({"status": 0, "result": value}))
''')
        script.chmod(0o700)
        environment = patch.dict(os.environ, {"PATH": str(self.directory), "FAKE_SF_MODE": "good"})
        environment.start()
        self.addCleanup(environment.stop)
        self.requests = []

    def adapter(self, *responses):
        pending = list(responses)

        def respond(request):
            self.requests.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(str(request.url).split("/services/")[0], ORIGIN)
            self.assertTrue(pending, "Unexpected HTTP attempt")
            response = pending.pop(0)
            if callable(response):
                response = response(request)
            return response if isinstance(response, httpx2.Response) else httpx2.Response(200, json=response)

        return SalesforceReadOnlyAdapter.from_sf("invented-alias", transport=httpx2.MockTransport(respond))

    def assert_auth_error(self, function):
        with self.assertRaises(SalesforceReadError) as caught:
            function()
        self.assertEqual(caught.exception.code, "salesforce_auth_required")
        output = "".join(traceback.format_exception(caught.exception))
        for secret in (TOKEN, FRESH_TOKEN, "fixture-only-child-secret", "invented-alias", ORIGIN):
            self.assertNotIn(secret, output)
        return caught.exception

    def test_from_sf_captures_two_commands_and_constructs_without_http(self):
        with patch("gtm_connector.sf_credentials.subprocess.run", wraps=REAL_RUN) as run:
            adapter = self.adapter()
        self.assertEqual(adapter.instance_url, ORIGIN)
        self.assertEqual(adapter.sf_cli_invocations, 2)
        self.assertEqual(adapter.http_requests, 0)
        self.assertEqual(self.requests, [])
        for arguments in run.call_args_list:
            options = arguments.kwargs
            self.assertIs(options["stdin"], subprocess.DEVNULL)
            self.assertIs(options["stdout"], subprocess.PIPE)
            self.assertIs(options["stderr"], subprocess.PIPE)
            self.assertLessEqual(options["timeout"], 10)
            self.assertGreater(options["timeout"], 0)
            self.assertNotIn("shell", options)
        self.assertEqual(sorted(path.name for path in self.directory.iterdir()), ["count", "sf"])

    def test_first_401_refreshes_once_and_retries_same_get_with_new_token(self):
        adapter = self.adapter(ORG, httpx2.Response(401), EMPTY, EMPTY)
        self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.sf_cli_invocations, 4)
        self.assertEqual(adapter.http_requests, 4)
        self.assertEqual(self.requests[1].url, self.requests[2].url)
        self.assertEqual([request.headers["Authorization"] for request in self.requests],
                         ["Bearer " + TOKEN] * 2 + ["Bearer " + FRESH_TOKEN] * 2)

    def test_second_401_and_later_query_401_do_not_refresh_twice_in_one_operation(self):
        for responses in ((httpx2.Response(401), httpx2.Response(401)),
                          (httpx2.Response(401), ORG, httpx2.Response(401))):
            with self.subTest(requests=len(responses)):
                adapter = self.adapter(*responses)
                self.assert_auth_error(adapter.search_accounts)
                self.assertEqual(adapter.sf_cli_invocations, 4)
                self.assertEqual(adapter.http_requests, len(responses))

    def test_refresh_failure_or_changed_origin_stops_before_retry(self):
        for mode, count in (("refresh_failure", 3), ("changed_origin", 4)):
            with self.subTest(mode=mode), patch.dict(os.environ, {"FAKE_SF_MODE": mode}):
                (self.directory / "count").unlink(missing_ok=True)
                adapter = self.adapter(ORG, httpx2.Response(401))
                self.assert_auth_error(adapter.search_accounts)
                self.assertEqual(adapter.http_requests, 2)
                self.assertEqual(adapter.sf_cli_invocations, count)

    def test_failed_initial_cli_is_fixed_and_reports_attempt_count(self):
        for mode, count in (("nonzero", 1), ("malformed", 1), ("missing", 1), ("bad_status", 1),
                            ("duplicate", 1), ("invalid_origin", 2)):
            with self.subTest(mode=mode), patch.dict(os.environ, {"FAKE_SF_MODE": mode}):
                error = self.assert_auth_error(self.adapter)
                self.assertEqual(error.sf_cli_invocations, count)
        (self.directory / "sf").unlink()
        self.assertEqual(self.assert_auth_error(self.adapter).sf_cli_invocations, 1)

    def test_invalid_aliases_never_invoke_cli(self):
        for alias in (None, "", "--target-org", "line\nbreak", "has space", "x" * 256):
            with self.subTest(alias=alias):
                error = self.assert_auth_error(lambda: SalesforceReadOnlyAdapter.from_sf(alias))
                self.assertEqual(error.sf_cli_invocations, 0)
        self.assertFalse((self.directory / "count").exists())

    def test_subprocess_timeout_suppresses_child_output(self):
        timeout = subprocess.TimeoutExpired(["sf", "invented-alias"], 1,
                                            output=TOKEN.encode(), stderr=FRESH_TOKEN.encode())
        with patch("gtm_connector.sf_credentials.subprocess.run", side_effect=timeout):
            self.assertEqual(self.assert_auth_error(self.adapter).sf_cli_invocations, 1)

    def test_refresh_shares_original_request_byte_and_time_budgets(self):
        adapter = self.adapter(ORG, httpx2.Response(401))
        with patch("gtm_connector.salesforce._MAX_REQUESTS", 2):
            with self.assertRaises(SalesforceReadError) as caught:
                adapter.search_accounts()
        self.assertEqual(caught.exception.code, "salesforce_result_limit")
        self.assertEqual(adapter.http_requests, 2)
        self.assertEqual(adapter.sf_cli_invocations, 4)

        adapter = self.adapter(ORG, httpx2.Response(401), EMPTY)
        with patch("gtm_connector.salesforce._MAX_BYTES", len(json.dumps(ORG).encode()) + 1):
            with self.assertRaises(SalesforceReadError) as caught:
                adapter.search_accounts()
        self.assertEqual(caught.exception.code, "salesforce_result_limit")

        adapter = self.adapter(ORG, httpx2.Response(401))
        clock = [0]

        def refreshed(deadline):
            self.assertEqual(deadline, 20)
            clock[0] = 21
            return ORIGIN, FRESH_TOKEN

        with patch("gtm_connector.salesforce.time.monotonic", side_effect=lambda: clock[0]):
            with patch.object(adapter._credentials, "read", side_effect=refreshed):
                with self.assertRaises(SalesforceReadError) as caught:
                    adapter.search_accounts()
        self.assertEqual(caught.exception.code, "salesforce_timeout")
        self.assertEqual(adapter.http_requests, 2)

    def test_each_cli_timeout_uses_remaining_original_deadline(self):
        provider = SFCredentials("invented-alias")
        clock = [0]
        observed = []

        def run(command, **options):
            observed.append(options["timeout"])
            clock[0] += 4
            result = {"instanceUrl": ORIGIN} if command[2] == "display" else {"accessToken": TOKEN}
            return subprocess.CompletedProcess(command, 0, json.dumps({"status": 0, "result": result}).encode())

        with patch("gtm_connector.sf_credentials.time.monotonic", side_effect=lambda: clock[0]):
            with patch("gtm_connector.sf_credentials.subprocess.run", side_effect=run):
                with self.assertRaises(SFCredentialsError):
                    provider.read(7)
        self.assertEqual(observed, [7, 3])
        self.assertEqual(provider.invocations, 2)


if __name__ == "__main__":
    unittest.main()
