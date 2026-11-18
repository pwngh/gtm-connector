"""App setup grants only requested scopes and keeps bearer credentials in files."""

import asyncio
import json
from pathlib import Path
import stat
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.connections import connect
from gtm_connector.runtime import Runtime


class ConnectionTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name).resolve() / "private state"

    def test_instructions_without_scopes_are_read_only_and_name_current_limits(self):
        for app in ("inspector", "claude-code", "cursor", "claude-desktop"):
            with self.subTest(app=app), patch("gtm_connector.inspector.write_inspector_config") as write:
                result = connect(self.state, app)
                write.assert_not_called()
                self.assertEqual(result["status"], "tested" if app == "inspector" else "untested")
                self.assertNotIn("config", result)
                text = json.dumps(result)
                self.assertIn("--scope", text)
                self.assertIn("crm:read", text)
                self.assertIn("gtm-demo-cli", text)
                self.assertIn("five minutes", text)
                self.assertTrue(result["documentation"])
        self.assertFalse(self.state.exists())

    def test_explicit_grants_make_private_app_configs_without_leaking_tokens(self):
        with Runtime(self.state) as runtime:
            for app in ("inspector", "claude-code", "cursor"):
                with self.subTest(app=app):
                    result = connect(self.state, app, ["crm:read", "calls:read"])
                    path = Path(result["config"])
                    content = path.read_text()
                    server = json.loads(content)["mcpServers"]["gtm-connector"]
                    token = server["headers"]["Authorization"].removeprefix("Bearer ")
                    granted = asyncio.run(runtime.connector.authority.verify_token(token))
                    self.assertEqual(set(granted.scopes), {"crm:read", "calls:read"})
                    self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
                    self.assertNotIn(token, json.dumps(result))
                    self.assertNotIn(runtime.config["owner_secret"], content + json.dumps(result))
                    self.assertEqual(server["url"], runtime.config["resource"])
                    if app == "inspector":
                        self.assertEqual(server["type"], "streamable-http")
                        self.assertEqual(server["protocolEra"], "modern")
                        command = result["setup"][-1]
                        for expected in ("NODE_EXTRA_CA_CERTS=", "NO_PROXY=", "no_proxy=",
                                         "NODE_TLS_REJECT_UNAUTHORIZED=1", "@modelcontextprotocol/inspector@2.15.0",
                                         "--stored-auth-only", "--quiet --format json --method tools/list"):
                            self.assertIn(expected, command)
                    elif app == "claude-code":
                        self.assertEqual(server["type"], "http")
                        self.assertIn("MCP_SDK_GENERATION=v2 MCP_PROTOCOL_NEGOTIATION=auto", result["setup"][-1])
                        self.assertIn("claude --mcp-config", result["setup"][-1])
                    else:
                        self.assertEqual(set(server), {"url", "headers"})
                        self.assertIn("Merge the gtm-connector entry", result["setup"][0])
                        self.assertFalse((self.state / ".cursor").exists())
            self.assertEqual(runtime.connector.audit.read(), [])

    def test_ancestor_alias_provisions_config_but_final_state_symlink_is_refused(self):
        real_parent = self.state.parent / "real-parent"
        real_parent.mkdir(mode=0o700)
        alias_parent = self.state.parent / "alias-parent"
        alias_parent.symlink_to(real_parent, target_is_directory=True)
        with Runtime(alias_parent / "state") as runtime:
            result = connect(alias_parent / "state", "inspector", ["crm:read"])
            self.assertEqual(Path(result["config"]), runtime.directory / "inspector.json")
            self.assertEqual(len(runtime.connector.authority.tokens), 1)
            state_alias = self.state.parent / "state-alias"
            state_alias.symlink_to(runtime.directory, target_is_directory=True)
            with self.assertRaisesRegex(ValueError, "regular, owner-only files"):
                connect(state_alias, "inspector", ["crm:read"])
            self.assertEqual(len(runtime.connector.authority.tokens), 1)

    def test_desktop_stays_blocked_without_minting_an_unusable_grant(self):
        with Runtime(self.state) as runtime:
            result = connect(self.state, "claude-desktop", ["crm:read"])
            self.assertEqual(runtime.connector.authority.tokens, {})
            self.assertEqual(result["status"], "untested")
            self.assertNotIn("config", result)
            self.assertIn("public HTTPS on port 443", " ".join(result["limits"]))
            self.assertIn("no stdio transport", " ".join(result["limits"]))

    def test_bad_config_and_output_symlinks_are_not_followed(self):
        with Runtime(self.state) as runtime:
            target = self.state / "untouched.txt"
            target.write_text("keep")
            (self.state / "claude-code.json").symlink_to(target)
            with self.assertRaises(OSError):
                connect(self.state, "claude-code", ["crm:read"])
            self.assertEqual(target.read_text(), "keep")
            owner = self.state / "owner.json"
            owner.write_text(json.dumps({**runtime.config, "issuer": "https://external.example"}))
            with patch("gtm_connector.inspector.write_inspector_config") as write:
                with self.assertRaisesRegex(ValueError, "Owner configuration is invalid"):
                    connect(self.state, "cursor", ["crm:read"])
                write.assert_not_called()


if __name__ == "__main__":
    unittest.main()
