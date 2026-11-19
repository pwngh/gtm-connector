"""Provision the external client without exporting the resource-owner credential."""

import asyncio
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest

from gtm_connector.client import LocalClient
from gtm_connector.connections import connect
from gtm_connector.runtime import Runtime


class InspectorConfigurationTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.state = Path(self.directory.name) / "state"

    def test_cli_exports_only_the_explicit_grant_in_a_private_config(self):
        with Runtime(self.state) as runtime:
            result = subprocess.run(
                [sys.executable, "-m", "gtm_connector", "connect", "inspector", "--json",
                 "--state-dir", str(self.state), "--scope", "crm:read"],
                capture_output=True, text=True, timeout=30,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            summary = json.loads(result.stdout)
            path = Path(summary["config"])
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            content = path.read_text()
            server = json.loads(content)["mcpServers"]["gtm-connector"]
            self.assertEqual(server["type"], "streamable-http")
            self.assertEqual(server["protocolEra"], "modern")
            self.assertEqual(server["url"], runtime.config["resource"])
            token = server["headers"]["Authorization"].removeprefix("Bearer ")
            grant = asyncio.run(runtime.connector.authority.verify_token(token))
            self.assertEqual(grant.scopes, ["crm:read"])
            self.assertNotIn(token, result.stdout + result.stderr)
            self.assertNotIn(runtime.config["owner_secret"], content + result.stdout + result.stderr)
            with LocalClient(runtime.config) as client:
                self.assertEqual(client.call(token, "read_call_notes", {"account_id": "acc-003"}).status_code, 403)

    def test_explicit_calls_grant_and_config_symlink_rejection(self):
        with Runtime(self.state) as runtime:
            connect(self.state, "inspector", ["crm:read", "calls:read"])
            path = self.state / "inspector.json"
            config = json.loads(path.read_text())
            token = config["mcpServers"]["gtm-connector"]["headers"]["Authorization"][7:]
            grant = asyncio.run(runtime.connector.authority.verify_token(token))
            self.assertEqual(set(grant.scopes), {"crm:read", "calls:read"})
            path.unlink()
            target = self.state / "untouched.txt"
            target.write_text("keep")
            path.symlink_to(target)
            with self.assertRaises(OSError):
                connect(self.state, "inspector", ["crm:read"])
            self.assertEqual(target.read_text(), "keep")

    def test_stopped_authority_returns_a_clear_error(self):
        with Runtime(self.state):
            pass
        result = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "connect", "inspector",
             "--state-dir", str(self.state), "--scope", "crm:read"],
            capture_output=True, text=True, timeout=30,
        )
        self.assertEqual(result.returncode, 5)
        self.assertIn("start serve", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertFalse((self.state / "inspector.json").exists())


if __name__ == "__main__":
    unittest.main()
