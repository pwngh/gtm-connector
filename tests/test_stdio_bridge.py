"""The stdio bridge as a real subprocess, driven by the MCP SDK's own stdio client."""

import asyncio
import json
import os
from pathlib import Path
import select
import stat
import subprocess
import sys
import tempfile
import unittest

from mcp import Client
from mcp.client.stdio import StdioServerParameters
from mcp.shared.exceptions import MCPError

from gtm_connector import profile
from gtm_connector.connections import connect, merge_client_config, stdio_entry
from gtm_connector.runtime import Runtime
from gtm_connector.selection import load_selection


ROOT = Path(__file__).resolve().parents[1]
DEFAULT_TOOLS = {"search_accounts", "get_account", "list_opportunities", "read_call_notes",
                 "read_slack_messages", "log_activity"}


def bridge(state, *scopes, cwd=ROOT, command=None, env=None):
    arguments = ["-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(state)]
    for scope in scopes:
        arguments += ["--scope", scope]
    return StdioServerParameters(command=command or sys.executable, args=arguments, cwd=str(cwd), env=env)


def run(parameters, scenario, mode="auto"):
    async def main():
        async with Client(parameters, mode=mode, read_timeout_seconds=90) as client:
            return await scenario(client)
    return asyncio.run(main())


def record_grants(runtime):
    """Record the scopes of every grant the local authority issues."""
    authority = runtime.connector.authority
    original, seen = authority.authorize, []

    async def authorize(client, params):
        seen.append(sorted(params.scopes or []))
        return await original(client, params)

    authority.authorize = authorize
    return seen


class StdioBridgeTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()
        self.state = self.root / "state"
        profile.save(self.state, profile.DEMO)

    def test_a_crm_only_bridge_reads_crm_through_the_gateway_and_never_widens(self):
        with Runtime(self.state) as runtime:
            grants = record_grants(runtime)

            async def scenario(client):
                tools = await client.list_tools()
                account = await client.call_tool("get_account", {"account_id": "acc-007"})
                with self.assertRaises(MCPError) as denied:
                    await client.call_tool("read_call_notes", {"account_id": "acc-007"})
                return tools, account, denied.exception

            tools, account, denied = run(bridge(self.state, "crm:read"), scenario)
            self.assertEqual({tool.name for tool in tools.tools}, DEFAULT_TOOLS)
            self.assertFalse(account.is_error)
            self.assertIn("crm/accounts/acc-007",
                          [row["record_ref"] for row in account.structured_content["records"]])
            self.assertEqual(denied.error.data["reason"], "insufficient_scope")
            self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", json.dumps(denied.error.model_dump()))
            receipts = [row for row in runtime.connector.audit.read() if row["tool"] != "<unparsed>"]
            self.assertEqual([(row["tool"], row["status"], row["http_status"]) for row in receipts],
                             [("get_account", "ok", 200), ("read_call_notes", "error", 403)])
            self.assertEqual(len({row["token_id"] for row in receipts}), 1)
            # One launch grant for exactly the named scope; the refusal was not retried with more.
            self.assertEqual(grants, [["crm:read"]])

    def test_a_refused_grant_is_renewed_once_for_the_same_scopes(self):
        with Runtime(self.state) as runtime:
            grants = record_grants(runtime)
            authority = runtime.connector.authority

            async def scenario(client):
                first = await client.call_tool("get_account", {"account_id": "acc-007"})
                with authority._lock:
                    authority.tokens.clear()
                second = await client.call_tool("get_account", {"account_id": "acc-007"})
                return first, second

            first, second = run(bridge(self.state, "calls:read", "crm:read"), scenario, mode="legacy")
            self.assertFalse(first.is_error)
            self.assertFalse(second.is_error)
            self.assertEqual(grants, [["calls:read", "crm:read"], ["calls:read", "crm:read"]])
            receipts = runtime.connector.audit.read()
            self.assertEqual([(row["tool"], row["http_status"]) for row in receipts],
                             [("get_account", 200), ("get_account", 401), ("get_account", 200)])

    def test_a_selected_server_prepares_a_brief_through_the_bridge(self):
        selection_file = self.root / "selection.json"
        selection_file.write_text(json.dumps({"schema_version": 1,
                                              "accounts": [{"account_id": "acc-007", "slack": True}]}))
        os.chmod(selection_file, 0o600)
        selection = load_selection(selection_file, crm_backend="sqlite")
        with Runtime(self.state, selection=selection) as runtime:
            async def scenario(client):
                tools = await client.list_tools()
                prepared = await client.call_tool("prepare_renewal_brief", {"account_id": "acc-007"})
                write = None
                try:
                    await client.call_tool("log_activity", {"account_id": "acc-007", "kind": "note", "body": "x",
                                                            "idempotency_key": "k"})
                except MCPError as exc:
                    write = exc
                return tools, prepared, write

            # Even a bridge holding crm:write cannot write to a selected server.
            tools, prepared, write = run(bridge(self.state, "crm:read", "calls:read", "slack:read", "crm:write"),
                                         scenario)
            self.assertIn("prepare_renewal_brief", {tool.name for tool in tools.tools})
            self.assertFalse(prepared.is_error, prepared)
            self.assertTrue(prepared.structured_content["brief"]["text"].startswith(
                'DEAL CARD: "Juniper Crest Learning" (acc-007) as of 2027-01-15\n'))
            self.assertEqual(prepared.structured_content["brief"]["units"][0]["record_ref"], "crm/accounts/acc-007")
            self.assertEqual(write.error.data["reason"], "crm_write_disabled")
            outer = [row for row in runtime.connector.audit.read() if row["tool"] == "prepare_renewal_brief"]
            self.assertEqual([(row["status"], row["http_status"]) for row in outer], [("ok", 200)])

    def test_stdout_carries_only_protocol_and_the_bridge_stops_at_end_of_input(self):
        with Runtime(self.state):
            process = subprocess.Popen(
                [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.state),
                 "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, text=True)
            requests = [
                {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "test", "version": "1"}}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                {"jsonrpc": "2.0", "id": 2, "method": "tools/list"},
            ]
            process.stdin.write("".join(json.dumps(item) + "\n" for item in requests))
            process.stdin.flush()
            lines = []
            while len(lines) < 2:
                ready, _, _ = select.select([process.stdout], [], [], 120)
                self.assertTrue(ready, "the bridge did not answer")
                line = process.stdout.readline()
                self.assertTrue(line, "the bridge closed stdout early")
                lines.append(line)
            # End of input stops the bridge promptly, and nothing else reaches stdout.
            process.stdin.close()
            self.assertEqual(process.wait(timeout=60), 0)
            self.assertEqual(process.stdout.read(), "")
            process.stdout.close()
            process.stderr.close()
            messages = [json.loads(line) for line in lines]
            self.assertTrue(all(message["jsonrpc"] == "2.0" for message in messages))
            self.assertEqual({tool["name"] for tool in messages[1]["result"]["tools"]}, DEFAULT_TOOLS)

    def test_a_call_the_bridge_itself_refuses_still_leaves_a_receipt(self):
        # The SDK refuses these two for their shape before any handler runs, and a header cannot carry the
        # third name. Each still reaches the gateway, which records it and refuses it.
        calls = [{"name": "get_account", "arguments": ["acc-001"]}, {"name": 7, "arguments": {}},
                 {"name": "get_acc\u00f8unt", "arguments": {"account_id": "acc-001"}}]
        with Runtime(self.state) as runtime:
            process = subprocess.Popen(
                [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.state),
                 "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, text=True)
            requests = [
                {"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {
                    "protocolVersion": "2025-06-18", "capabilities": {},
                    "clientInfo": {"name": "test", "version": "1"}}},
                {"jsonrpc": "2.0", "method": "notifications/initialized"},
                *({"jsonrpc": "2.0", "id": number, "method": "tools/call", "params": params}
                  for number, params in enumerate(calls, 2)),
            ]
            process.stdin.write("".join(json.dumps(item) + "\n" for item in requests))
            process.stdin.flush()
            answers = []
            while len(answers) < 4:
                ready, _, _ = select.select([process.stdout], [], [], 120)
                self.assertTrue(ready, "the bridge did not answer")
                answers.append(json.loads(process.stdout.readline()))
            process.stdin.close()
            self.assertEqual(process.wait(timeout=60), 0)
            process.stdout.close()
            process.stderr.close()
            receipts = runtime.connector.audit.read()
        # No call returned a record, and each has one finished receipt that is not ok.
        self.assertTrue(all("error" in answer or answer["result"].get("isError") for answer in answers[1:]))
        self.assertEqual([row["status"] for row in receipts], ["error"] * 3)
        self.assertEqual([row["rows"] for row in receipts], [[]] * 3)

    def test_without_a_running_connector_the_bridge_exits_with_a_message(self):
        missing = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.root / "missing"),
             "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=120, check=False)
        self.assertEqual((missing.returncode, missing.stdout), (2, ""))
        self.assertIn("No CRM is connected. Run gtm-connector connect", missing.stderr)
        self.assertFalse((self.root / "missing").exists())
        result = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.state),
             "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=120, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")
        self.assertIn("gtm-connector serve", result.stderr)
        with Runtime(self.state):
            pass
        stopped = subprocess.run(
            [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.state),
             "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.DEVNULL, capture_output=True, text=True,
            timeout=120, check=False)
        self.assertNotEqual(stopped.returncode, 0)
        self.assertEqual(stopped.stdout, "")
        self.assertIn("Start serve again", stopped.stderr)

    def test_the_bridge_refuses_a_connector_that_serves_other_sources_than_the_profile_names(self):
        with Runtime(self.state) as runtime:
            profile.connect(self.state, "calls", {"provider": "imported"})
            changed = subprocess.run(
                [sys.executable, "-m", "gtm_connector", "mcp", "stdio", "--state-dir", str(self.state),
                 "--scope", "crm:read"], cwd=ROOT, stdin=subprocess.DEVNULL, capture_output=True, text=True,
                timeout=120, check=False)
            self.assertEqual((changed.returncode, changed.stdout), (1, ""))
            self.assertIn("Start gtm-connector serve again", changed.stderr)
            self.assertEqual(runtime.connector.audit.read(), [])
            self.assertEqual(runtime.connector.authority.tokens, {})


class GrantTests(unittest.TestCase):
    def test_a_refused_token_that_another_call_already_replaced_is_not_renewed_again(self):
        from gtm_connector.stdio_bridge import Grant

        class Authority:
            issued = 0

            def authorize_with_lifetime(self, scopes):
                self.issued += 1
                return f"token-{self.issued}", 300

        authority = Authority()
        grant = Grant(authority, ["crm:read"])
        first = grant.renew()
        # Two calls were refused with the first token. One renews; the other reuses that renewal.
        second = grant.renew(refused=first)
        self.assertEqual((grant.renew(refused=first), authority.issued), (second, 2))
        self.assertEqual((grant.current(), authority.issued), (second, 2))


class ClientConfigurationTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.root = Path(directory.name).resolve()
        self.state = self.root / "state dir é"
        self.config = self.root / "client.json"

    def test_connect_merges_only_its_entry_after_a_same_mode_backup(self):
        original = json.dumps({"theme": "dark", "mcpServers": {"other": {"command": "keep"}}}).encode()
        self.config.write_bytes(original)
        os.chmod(self.config, 0o640)
        result = connect(self.state, "claude-desktop", ["crm:read", "calls:read"], transport="stdio",
                         config_file=self.config)
        document = json.loads(self.config.read_text())
        self.assertEqual(document["theme"], "dark")
        self.assertEqual(document["mcpServers"]["other"], {"command": "keep"})
        entry = document["mcpServers"]["gtm-connector"]
        # Python starts isolated, so the app's working folder cannot stand in for the package.
        self.assertEqual(entry["args"][0], "-I")
        self.assertEqual(entry["args"][-8:], ["mcp", "stdio", "--state-dir", str(self.state),
                                              "--scope", "calls:read", "--scope", "crm:read"])
        self.assertNotIn("env", entry)
        self.assertTrue(Path(entry["command"]).is_absolute())
        self.assertEqual(stat.S_IMODE(self.config.stat().st_mode), 0o640)
        backup = Path(result["backup"])
        self.assertEqual(backup.read_bytes(), original)
        self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o640)
        text = self.config.read_text()
        for secret in ("Bearer", "owner_secret", "access_token", "Authorization"):
            self.assertNotIn(secret, text)

        fresh = self.root / "fresh.json"
        created = connect(self.state, "claude-code", ["crm:read"], transport="stdio", config_file=fresh)
        self.assertIsNone(created["backup"])
        self.assertEqual(stat.S_IMODE(fresh.stat().st_mode), 0o600)
        self.assertEqual(json.loads(fresh.read_text())["mcpServers"]["gtm-connector"]["type"], "stdio")

    def test_connect_refuses_links_bad_files_and_concurrent_edits(self):
        target = self.root / "target.json"
        target.write_text('{"mcpServers": {}}')
        self.config.symlink_to(target)
        with self.assertRaises(ValueError):
            connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
        self.assertEqual(target.read_text(), '{"mcpServers": {}}')
        self.config.unlink()

        self.config.write_text('{"mcpServers": {}}')
        backup = self.root / "client.json.gtm-connector-backup"
        backup.symlink_to(target)
        with self.assertRaises(ValueError):
            connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
        self.assertEqual(self.config.read_text(), '{"mcpServers": {}}')
        backup.unlink()

        for bad in ('{"mcpServers": []}', "[]", "not json", '{"a": 1, "a": 2}'):
            self.config.write_text(bad)
            with self.subTest(bad=bad), self.assertRaises(ValueError):
                connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
            self.assertEqual(self.config.read_text(), bad)

        self.config.write_text('{"mcpServers": {}}')
        with self.assertRaises(ValueError):
            merge_client_config(self.config, {"command": "x", "args": []},
                                _after_read=lambda: self.config.write_text('{"edited": true}'))
        self.assertEqual(self.config.read_text(), '{"edited": true}')
        self.assertEqual(list(self.root.glob(".client.json.*")), [])
        # A conflict leaves the earlier backup as it was.
        self.config.write_text('{"mcpServers": {}}')
        backup.write_text("the earlier backup")
        with self.assertRaises(ValueError):
            merge_client_config(self.config, {"command": "x", "args": []},
                                _after_read=lambda: self.config.write_text('{"edited": true}'))
        self.assertEqual(backup.read_text(), "the earlier backup")
        # A file with a second name is refused. A backup that shares its file with another name is replaced,
        # never written through.
        self.config.write_text('{"mcpServers": {}}')
        twin = self.root / "twin.json"
        os.link(self.config, twin)
        with self.assertRaises(ValueError):
            connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
        twin.unlink()
        backup.unlink()
        shared = self.root / "shared.txt"
        shared.write_text("keep me")
        os.link(shared, backup)
        connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
        self.assertEqual((shared.read_text(), backup.read_text()), ("keep me", '{"mcpServers": {}}'))
        # Numbers that are not finite refuse, and so does a file the merge would push past 4 MiB.
        for bad in ('{"mcpServers": {}, "x": NaN}', '{"mcpServers": {}, "x": 1e999}',
                    '{"mcpServers": {}, "x": [' + "0," * 1_390_000 + "0]}"):
            self.config.write_text(bad)
            with self.subTest(bad=bad[:30]), self.assertRaises(ValueError):
                connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=self.config)
            self.assertEqual(self.config.read_text(), bad)
        self.assertEqual(list(self.root.glob(".client.json.*")), [])
        for args in (("inspector", ["crm:read"]), ("cursor", [])):
            with self.subTest(args=args), self.assertRaises(ValueError):
                connect(self.state, args[0], args[1], transport="stdio", config_file=self.config)
        with self.assertRaises(ValueError):
            connect(self.state, "cursor", ["crm:read"], transport="stdio", config_file=None)

    def test_the_generated_command_launches_from_another_folder_with_a_minimal_path(self):
        elsewhere = self.root / "elsewhere"
        # A package of the same name in the app's working folder must not be the one that runs.
        (elsewhere / "gtm_connector").mkdir(parents=True)
        (elsewhere / "gtm_connector" / "__init__.py").write_text("raise SystemExit('shadowed')\n")
        (elsewhere / "gtm_connector" / "__main__.py").write_text("raise SystemExit('shadowed')\n")
        profile.save(self.state, profile.DEMO)
        with Runtime(self.state):
            entry = stdio_entry(self.state, "cursor", ["crm:read"])
            parameters = StdioServerParameters(command=entry["command"], args=entry["args"], cwd=str(elsewhere),
                                               env={"PATH": "/usr/bin:/bin", "PYTHONPATH": str(elsewhere)})

            async def scenario(client):
                return await client.list_tools()

            tools = run(parameters, scenario)
            self.assertEqual({tool.name for tool in tools.tools}, DEFAULT_TOOLS)


if __name__ == "__main__":
    unittest.main()
