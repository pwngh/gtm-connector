"""Explicit backend selection without any real organization or credentials."""

import json
import os
from pathlib import Path
import select
import signal
import subprocess
import sys
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[1]


class SalesforceCLITests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "state"
        self.env = {key: value for key, value in os.environ.items()
                    if not key.startswith("SALESFORCE_")}

    def run_cli(self, *arguments):
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments,
             "--state-dir", str(self.state)], cwd=ROOT, env=self.env,
            capture_output=True, text=True, timeout=30, check=False,
        )

    def test_external_selection_requires_environment_before_creating_state(self):
        result = self.run_cli("serve", "--crm-backend", "salesforce")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("salesforce_invalid_configuration", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertFalse(self.state.exists())

    def test_external_brief_requires_credentials_before_model_startup(self):
        result = self.run_cli("brief", "acc-sf-001000000000001AAA", "--crm-backend", "salesforce",
                              "--adapter", "ollama", "--model", "not-installed")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("salesforce_invalid_configuration", result.stderr)
        self.assertIn("gtm-connector", result.stderr)
        self.assertFalse(self.state.exists())

    def test_sf_alias_requires_the_explicit_backend(self):
        result = self.run_cli("serve", "--sf-org", "invented-alias")
        self.assertEqual(result.returncode, 2)
        self.assertIn("--sf-org requires --crm-backend salesforce", result.stderr)
        self.assertFalse(self.state.exists())

    def test_local_client_allows_the_upstream_operation_to_finish(self):
        import httpx2
        from gtm_connector.client import LocalClient
        from gtm_connector.runtime import Runtime
        from gtm_connector.salesforce import SalesforceReadOnlyAdapter, _OPERATION_SECONDS
        reader = SalesforceReadOnlyAdapter("https://fixture-timeout.my.salesforce.com", "invented-token",
                                           transport=httpx2.MockTransport(lambda _: self.fail("Unexpected GET")))
        with Runtime(self.state, crm_reader=reader) as runtime, LocalClient(runtime.config) as client:
            self.assertGreater(client._http.timeout.read, _OPERATION_SECONDS)
            self.assertEqual(client._http.timeout.connect, 10)

    def test_serve_captures_fake_sf_credentials_without_exports_or_org_output(self):
        binary = self.state.parent / "sf"
        log = self.state.parent / "invocations.jsonl"
        binary.write_text(f"#!{sys.executable}\n" + '''import json, os, sys
with open(os.environ["FAKE_SF_LOG"], "a") as stream:
    stream.write(json.dumps(sys.argv[1:]) + "\\n")
field = "instanceUrl" if sys.argv[1:3] == ["org", "display"] else "accessToken"
value = "https://invented-cli.my.salesforce.com" if field == "instanceUrl" else "invented-cli-private-token"
print(json.dumps({"status": 0, "result": {field: value}}))
''')
        binary.chmod(0o700)
        self.env.update(PATH=str(binary.parent) + os.pathsep + os.defpath, FAKE_SF_LOG=str(log))
        process = subprocess.Popen(
            [sys.executable, "-m", "gtm_connector", "serve", "--json", "--crm-backend", "salesforce",
             "--sf-org", "invented-alias", "--state-dir", str(self.state)],
            cwd=ROOT, env=self.env, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
            stderr=subprocess.PIPE, text=True)
        try:
            readable, _, _ = select.select([process.stdout], [], [], 20)
            self.assertTrue(readable)
            ready = json.loads(process.stdout.readline())
            self.assertEqual(ready["crm_backend"], "salesforce")
            invocations = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(invocations, [
                ["org", "display", "--target-org", "invented-alias", "--json"],
                ["org", "auth", "show-access-token", "--target-org", "invented-alias", "--json"]])
            process.send_signal(signal.SIGINT)
            stdout, stderr = process.communicate(timeout=15)
            self.assertEqual(process.returncode, 0, stderr)
            output = json.dumps(ready) + stdout + stderr + (self.state / "owner.json").read_text()
            for secret in ("invented-cli-private-token", "invented-cli.my.salesforce.com", "invented-alias"):
                self.assertNotIn(secret, output)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)
            process.stdout.close()
            process.stderr.close()

    def test_default_ignores_external_environment_and_explicit_serve_is_lazy(self):
        for selected in (False, True):
            with self.subTest(salesforce=selected):
                self.env.update(SALESFORCE_INSTANCE_URL="https://fixture-only.my.salesforce.com",
                                SALESFORCE_ACCESS_TOKEN="synthetic-test-credential")
                arguments = ["--crm-backend", "salesforce"] if selected else []
                process = subprocess.Popen(
                    [sys.executable, "-m", "gtm_connector", "serve", "--json", *arguments,
                     "--state-dir", str(self.state)], cwd=ROOT, env=self.env,
                    stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE, text=True,
                )
                try:
                    ready, _, _ = select.select([process.stdout], [], [], 20)
                    self.assertTrue(ready, "serve did not publish its local endpoints")
                    endpoints = json.loads(process.stdout.readline())
                    backend = "salesforce" if selected else "sqlite"
                    self.assertEqual(endpoints["crm_backend"], backend)
                    owner = json.loads((self.state / "owner.json").read_text())
                    self.assertEqual(owner["crm_backend"], backend)
                    public = json.dumps([endpoints, owner])
                    self.assertNotIn(self.env["SALESFORCE_ACCESS_TOKEN"], public)
                    self.assertNotIn(self.env["SALESFORCE_INSTANCE_URL"], public)
                    process.send_signal(signal.SIGINT)
                    stdout, stderr = process.communicate(timeout=15)
                    self.assertEqual(process.returncode, 0, stderr)
                    self.assertEqual(stdout, "")
                    self.assertNotIn("Traceback", stderr)
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.communicate(timeout=5)
                    process.stdout.close()
                    process.stderr.close()


if __name__ == "__main__":
    unittest.main()
