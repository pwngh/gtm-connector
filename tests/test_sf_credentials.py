"""Only invented HTTP and a fake sf executable on PATH are used here."""

from argparse import Namespace
from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time
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
       "records": [{"Id": "00D000000000001AAA", "OrganizationType": "Developer Edition", "IsSandbox": False}]}


class SFCredentialsTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        script = self.directory / "sf"
        script.write_text(f"#!{sys.executable}\n" + '''
import json, os, pathlib, sys, time
directory = pathlib.Path(__file__).parent
counter = directory / "count"
number = int(counter.read_text()) + 1 if counter.exists() else 1
counter.write_text(str(number))
if number >= int(os.environ.get("FAKE_SF_DELAY_FROM", "1")):
    time.sleep(float(os.environ.get("FAKE_SF_DELAY", "0")))
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

    def adapter(self, *responses, environment="developer"):
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

        return SalesforceReadOnlyAdapter.from_sf("invented-alias", environment=environment,
                                                 transport=httpx2.MockTransport(respond))

    def assert_error(self, code, function):
        with self.assertRaises(SalesforceReadError) as caught:
            function()
        self.assertEqual(caught.exception.code, code)
        output = "".join(traceback.format_exception(caught.exception))
        for secret in (TOKEN, FRESH_TOKEN, "fixture-only-child-secret", "invented-alias", ORIGIN):
            self.assertNotIn(secret, output)
        return caught.exception

    def assert_auth_error(self, function):
        return self.assert_error("salesforce_auth_required", function)

    def test_from_sf_captures_two_commands_and_constructs_without_http(self):
        with patch("gtm_connector.sf_credentials.subprocess.Popen", wraps=subprocess.Popen) as run:
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
            self.assertNotIn("shell", options)
        self.assertEqual(sorted(path.name for path in self.directory.iterdir()), ["count", "sf"])

    def test_first_401_refreshes_once_and_retries_same_get_with_new_token(self):
        adapter = self.adapter(ORG, httpx2.Response(401), ORG, EMPTY, EMPTY)
        self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.sf_cli_invocations, 4)
        self.assertEqual(adapter.http_requests, 5)
        self.assertEqual(self.requests[1].url, self.requests[3].url)
        self.assertIn(" FROM Organization", self.requests[2].url.params["q"])
        self.assertEqual([request.headers["Authorization"] for request in self.requests],
                         ["Bearer " + TOKEN] * 2 + ["Bearer " + FRESH_TOKEN] * 3)

    def test_explicit_paid_environments_keep_identity_and_cached_capabilities_after_fake_cli_refresh(self):
        fields = {
            "Account": ("Id", "Name", "Industry", "AccountNumber"),
            "Contact": ("Id", "AccountId", "Name", "Title", "Email"),
            "Opportunity": ("Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"),
            "Contract": ("Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"),
        }
        for environment in ("sandbox", "production"):
            with self.subTest(environment=environment):
                (self.directory / "count").unlink(missing_ok=True)
                self.requests.clear()
                organization = {"totalSize": 1, "done": True, "records": [{
                    "Id": "00D000000000001AAA", "OrganizationType": "Enterprise Edition",
                    "IsSandbox": environment == "sandbox"}]}
                descriptions = [{"name": name, "queryable": True,
                                 "fields": [{"name": field} for field in names]}
                                for name, names in fields.items()]
                adapter = self.adapter(organization, *descriptions, httpx2.Response(401), organization, EMPTY,
                                       environment=environment)
                identity = adapter.prepare_connection()
                self.assertEqual(identity["environment"], environment)
                self.assertEqual(adapter.http_requests, 5)
                self.assertEqual(adapter.search_accounts(), [])
                self.assertEqual(adapter.prepare_connection(), identity)
                self.assertEqual(adapter.sf_cli_invocations, 4)
                self.assertEqual(adapter.http_requests, 8)
                self.assertEqual(self.requests[-3].url, self.requests[-1].url)
                self.assertIn(" FROM Organization", self.requests[-2].url.params["q"])
                self.assertEqual(self.requests[-3].headers["Authorization"], "Bearer " + TOKEN)
                self.assertEqual(self.requests[-1].headers["Authorization"], "Bearer " + FRESH_TOKEN)

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

    def test_a_cli_timeout_has_its_own_code_and_suppresses_child_output(self):
        # An sf that outlasts its limit is a timeout, not a missing login, and it is stopped.
        started = time.monotonic()
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "30"}), \
                patch("gtm_connector.sf_credentials._COMMAND_SECONDS", 0.3):
            self.assertEqual(self.assert_error("salesforce_cli_timeout", self.adapter).sf_cli_invocations, 1)
        self.assertLess(time.monotonic() - started, 10)
        # A descendant that keeps sf's pipes open cannot hold up the timeout.
        (self.directory / "sf").write_text(f"#!{sys.executable}\n" + """
import subprocess, sys, time
subprocess.Popen([sys.executable, "-c", "import time; time.sleep(30)"])
time.sleep(30)
""")
        started = time.monotonic()
        with patch("gtm_connector.sf_credentials._COMMAND_SECONDS", 0.3):
            self.assert_error("salesforce_cli_timeout", self.adapter)
        self.assertLess(time.monotonic() - started, 10)

    def test_the_login_read_has_one_limit_and_a_failing_notice_changes_nothing(self):
        from gtm_connector.sf_credentials import reporting

        def broken(seconds):
            raise RuntimeError("invented-alias must not appear")

        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.7"}), reporting(broken):
            self.assertEqual(self.adapter().instance_url, ORIGIN)
        (self.directory / "count").unlink()
        # The whole login read has one limit across both commands.
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.4"}), \
                patch("gtm_connector.sf_credentials.CREDENTIAL_SECONDS", 0.6):
            self.assertEqual(self.assert_error("salesforce_cli_timeout", self.adapter).sf_cli_invocations, 2)

    def test_credential_time_does_not_spend_the_read_budget(self):
        # Each sf command takes 0.6 seconds, so one login read outlasts the whole patched budget.
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.6"}), \
                patch("gtm_connector.salesforce._OPERATION_SECONDS", 1):
            adapter = self.adapter(ORG, EMPTY, httpx2.Response(401), ORG, EMPTY)
            self.assertEqual(adapter.search_accounts(), [])
            # A refresh as slow as the budget still leaves the retried read its own time.
            self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.sf_cli_invocations, 4)
        self.assertEqual([request.headers["Authorization"] for request in self.requests],
                         ["Bearer " + TOKEN] * 3 + ["Bearer " + FRESH_TOKEN] * 2)

    def test_a_slow_refresh_finishes_the_read_and_announces_itself(self):
        notices = []
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.6", "FAKE_SF_DELAY_FROM": "3"}), \
                patch("gtm_connector.salesforce._OPERATION_SECONDS", 1):
            adapter = self.adapter(ORG, httpx2.Response(401), ORG, EMPTY)
            adapter.on_refresh = lambda status, seconds: notices.append(status)
            self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(notices, ["running", "ready"])
        self.assertEqual(adapter.sf_cli_invocations, 4)
        # A refresh that fails reports its own code.
        notices.clear()
        (self.directory / "count").unlink()
        with patch.dict(os.environ, {"FAKE_SF_MODE": "refresh_failure"}):
            adapter = self.adapter(ORG, httpx2.Response(401))
            adapter.on_refresh = lambda status, seconds: notices.append(status)
            self.assert_auth_error(adapter.search_accounts)
        self.assertEqual(notices, ["running", "salesforce_auth_required"])

    def test_the_cli_says_it_is_waiting_for_sf_and_serve_announces_refreshes(self):
        from gtm_connector import cli
        for as_json in (True, False):
            with self.subTest(as_json=as_json):
                (self.directory / "count").unlink(missing_ok=True)
                args = Namespace(sf_org="invented-alias", sf_environment="developer", json=as_json, command="serve")
                with patch.dict(os.environ, {"FAKE_SF_DELAY": "1.3"}), redirect_stderr(io.StringIO()) as stderr:
                    reader = cli._salesforce_reader(args)
                    reader.on_refresh("running", 0.0)
                    reader.on_refresh("ready", 31.4)
                    reader.on_refresh("salesforce_cli_timeout", 180.2)
                lines = stderr.getvalue().splitlines()
                expected = ["Salesforce login refresh: sf is running.",
                            "Salesforce login refresh: finished in 31 s.",
                            "Salesforce login refresh: salesforce_cli_timeout after 180 s."]
                if as_json:
                    # JSON stderr gets no waiting lines; serve notices keep their JSON form.
                    self.assertEqual([json.loads(line)["message"] for line in lines], expected)
                else:
                    self.assertRegex(lines[0], r"^Waiting for the Salesforce CLI \(sf\) to return the saved login: "
                                               r"[0-9]+ s\.$")
                    self.assertEqual(lines[1:], expected)
                self.assertNotIn("invented-alias", stderr.getvalue())
        # A one-shot command announces a refresh too, except with --json.
        for as_json, announces in ((False, True), (True, False)):
            args = Namespace(sf_org="invented-alias", sf_environment="developer", json=as_json, command="accounts")
            with redirect_stderr(io.StringIO()):
                self.assertEqual(cli._salesforce_reader(args).on_refresh is not None, announces)

    def test_waiting_lines_come_after_two_seconds_and_then_every_ten(self):
        from gtm_connector import cli
        with redirect_stderr(io.StringIO()) as stderr:
            report = cli._sf_waiting(False)
            for seconds in (0.5, 1.9, 2.0, 2.5, 11.9, 12.1, 12.6, 22.4):
                report(seconds)
        self.assertEqual([line.rsplit(": ", 1)[1] for line in stderr.getvalue().splitlines()],
                         ["2 s.", "12 s.", "22 s."])

    def test_waiting_for_sf_reports_progress(self):
        from gtm_connector.sf_credentials import reporting
        seen = []
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.7"}), reporting(seen.append):
            adapter = self.adapter()
        self.assertEqual(adapter.instance_url, ORIGIN)
        self.assertGreaterEqual(len(seen), 2)
        self.assertEqual(seen, sorted(seen))
        self.assertTrue(all(isinstance(value, float) and value > 0 for value in seen))
        # Without a reporting context nothing is called back.
        count = len(seen)
        with patch.dict(os.environ, {"FAKE_SF_DELAY": "0.7"}):
            self.adapter()
        self.assertEqual(len(seen), count)

    def test_refresh_shares_original_request_byte_and_time_budgets(self):
        adapter = self.adapter(ORG, httpx2.Response(401))
        with patch("gtm_connector.salesforce._MAX_REQUESTS", 2):
            with self.assertRaises(SalesforceReadError) as caught:
                adapter.search_accounts()
        self.assertEqual(caught.exception.code, "salesforce_result_limit")
        self.assertEqual(adapter.http_requests, 2)
        # With no attempts left to confirm the org and retry, sf is not run again.
        self.assertEqual(adapter.sf_cli_invocations, 2)

        adapter = self.adapter(ORG, httpx2.Response(401), ORG, EMPTY)
        with patch("gtm_connector.salesforce._MAX_BYTES", len(json.dumps(ORG).encode()) + 1):
            with self.assertRaises(SalesforceReadError) as caught:
                adapter.search_accounts()
        self.assertEqual(caught.exception.code, "salesforce_result_limit")

        # Time spent waiting for sf is added back to the read's deadline, not taken from it.
        adapter = self.adapter(ORG, httpx2.Response(401), ORG, EMPTY)
        clock = [0]

        def refreshed():
            clock[0] = 21
            return ORIGIN, FRESH_TOKEN

        with patch("gtm_connector.salesforce.time.monotonic", side_effect=lambda: clock[0]):
            with patch.object(adapter._credentials, "read", side_effect=refreshed):
                self.assertEqual(adapter.search_accounts(), [])
        self.assertEqual(adapter.http_requests, 4)



if __name__ == "__main__":
    unittest.main()
