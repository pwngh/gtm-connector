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

    def test_external_brief_refuses_before_credentials_or_model_startup(self):
        result = self.run_cli("brief", "acc-sf-001000000000001AAA", "--crm-backend", "salesforce",
                              "--adapter", "ollama", "--model", "not-installed")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertEqual(result.stderr,
                         "error: renewal_fields_unmapped: Salesforce brief generation is unavailable\n")
        self.assertFalse(self.state.exists())

    def test_default_ignores_external_environment_and_explicit_serve_is_lazy(self):
        for selected in (False, True):
            with self.subTest(salesforce=selected):
                self.env.update(SALESFORCE_INSTANCE_URL="https://fixture-only.my.salesforce.com",
                                SALESFORCE_ACCESS_TOKEN="synthetic-test-credential")
                arguments = ["--crm-backend", "salesforce"] if selected else []
                process = subprocess.Popen(
                    [sys.executable, "-m", "gtm_connector", "serve", *arguments,
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
