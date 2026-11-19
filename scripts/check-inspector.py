#!/usr/bin/env python3
"""Optional interoperability proof using the pinned official Inspector CLI.

Requires Node 22 and npx. This downloads Inspector if it is not cached.
The default demo and Python test suite do not run this script.
"""

from contextlib import closing
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
from unittest.mock import patch

from jsonschema.validators import validator_for

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
from gtm_connector import store
from gtm_connector.gateway import Connector
from gtm_connector.connections import connect
from gtm_connector.runtime import Runtime, private_file

INSPECTOR = "2.15.0"
PROTOCOL = "2026-07-28"
COMMAND = ["npx", "--yes", f"@modelcontextprotocol/inspector@{INSPECTOR}", "--cli"]
SCOPES = {"search_accounts": "crm:read", "get_account": "crm:read",
          "list_opportunities": "crm:read", "read_call_notes": "calls:read",
          "log_activity": "crm:write", "read_slack_messages": "slack:read"}


def invoke(arguments, environment, timeout=60):
    """Bound each subprocess, including its npx child, without printing secrets."""
    with subprocess.Popen(COMMAND + arguments, cwd=ROOT, env=environment,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                          stdin=subprocess.DEVNULL, start_new_session=True) as process:
        try:
            stdout, stderr = process.communicate(timeout=timeout)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
            raise RuntimeError(f"Inspector timed out after {timeout} seconds") from None
        return process.returncode, stdout, stderr


def check():
    traffic = []
    original = Connector.__call__

    async def observe(connector, scope, receive, send):
        if scope["type"] != "http":
            return await original(connector, scope, receive, send)
        headers = dict(scope.get("headers", []))
        entry = {"http_method": scope["method"], "path": scope["path"],
                 "protocol_header": headers.get(b"mcp-protocol-version", b"").decode(),
                 "method_header": headers.get(b"mcp-method", b"").decode()}
        traffic.append(entry)
        parts = []

        async def receiving():
            message = await receive()
            if message["type"] == "http.request":
                parts.append(message.get("body", b""))
                if not message.get("more_body") and parts:
                    payload = json.loads(b"".join(parts))
                    entry["method"] = payload.get("method")
                    entry["protocol_meta"] = payload.get("params", {}).get("_meta", {}).get(
                        "io.modelcontextprotocol/protocolVersion")
            return message

        async def sending(message):
            if message["type"] == "http.response.start":
                entry["status"] = message["status"]
            await send(message)

        await original(connector, scope, receiving, sending)

    with tempfile.TemporaryDirectory(prefix="gtm-inspector-") as temporary:
        directory = Path(temporary)
        environment = dict(os.environ, MCP_STORAGE_DIR=str(directory / "storage"),
                           MCP_CLIENT_CONFIG_PATH=str(directory / "client.json"),
                           MCP_INSPECTOR_OAUTH_STATE_PATH=str(directory / "oauth.json"),
                           MCP_INSPECTOR_SECRET_STORE="memory", MCP_AUTO_OPEN_ENABLED="false",
                           NODE_TLS_REJECT_UNAUTHORIZED="1")
        for name in ("NO_PROXY", "no_proxy"):
            inherited = os.environ.get(name, os.environ.get(name.swapcase()))
            environment[name] = ",".join(filter(None, (inherited, "127.0.0.1")))
        # Permit a first npm download; actual RPC commands keep a shorter timeout.
        version = invoke(["--version"], environment, timeout=240)
        assert version[0] == 0 and version[1].strip() == INSPECTOR, "Inspector version mismatch"
        with patch.object(Connector, "__call__", observe), Runtime(directory / "state", rate_limit=2) as runtime:
            summary = connect(runtime.directory, "inspector", ["crm:read"])
            config_path = Path(summary["config"])
            environment["NODE_EXTRA_CA_CERTS"] = str(runtime.directory / "tls.pem")
            base = ["--config", str(config_path), "--server", "gtm-connector", "--format", "json",
                    "--stored-auth-only", "--quiet"]

            def run(method, tool=None, status=200):
                start = len(traffic)
                arguments = base + ["--method", method]
                if tool:
                    arguments += ["--tool-name", tool, "--tool-arg", "account_id=acc-003"]
                code, stdout, stderr = invoke(arguments, environment)
                secrets = [runtime.config["owner_secret"], *runtime.connector.authority.tokens]
                assert not any(secret in stdout + stderr for secret in secrets), "Credential in CLI output"
                observed = traffic[start:]
                assert observed and observed[-1]["status"] == status, "Unexpected HTTP outcome"
                if status == 200:
                    assert code == 0 and not stderr, "Inspector command failed"
                    return json.loads(stdout)["result"]
                assert code != 0 and not stdout, "Denied command returned data"
                error = json.loads(stderr)
                assert set(error) == {"error"} and "message" in error["error"], "Missing CLI error envelope"
                assert not any(text in stderr for text in (
                    "Cedarline Analytics", "ellis@cedarline.example", "Procurement has not approved", "record_ref",
                    "Security review is delayed", "Avery Linden"
                )), "Denied command leaked source data"
                return error

            discovery = run("initialize")
            assert discovery["protocolVersion"] == PROTOCOL, "Wrong discovered protocol"
            assert "tools" in discovery["capabilities"], "Missing tools capability"
            assert traffic[-1]["method"] == "server/discover", "Legacy initialization on the wire"
            tools = run("tools/list")["tools"]
            assert {tool["name"]: tool["_meta"]["gtm.demo/requiredScope"] for tool in tools} == SCOPES
            assert len(tools) == 6, "Unexpected tool count"
            for tool in tools:
                for field in ("inputSchema", "outputSchema"):
                    validator_for(tool[field]).check_schema(tool[field])
            account = run("tools/call", "get_account")
            denied_calls = run("tools/call", "read_call_notes", 403)
            assert "calls:read" in denied_calls["error"]["message"]
            run("tools/call", "get_account", 429)
            connect(runtime.directory, "inspector", ["calls:read"])
            notes = run("tools/call", "read_call_notes")
            denied_crm = run("tools/call", "get_account", 403)
            assert "crm:read" in denied_crm["error"]["message"]
            connect(runtime.directory, "inspector", ["crm:read"])
            assert "slack:read" in run("tools/call", "read_slack_messages", 403)["error"]["message"]
            connect(runtime.directory, "inspector", ["calls:read"])
            assert "slack:read" in run("tools/call", "read_slack_messages", 403)["error"]["message"]
            connect(runtime.directory, "inspector", ["slack:read"])
            messages = run("tools/call", "read_slack_messages")
            assert "crm:read" in run("tools/call", "get_account", 403)["error"]["message"]
            connect(runtime.directory, "inspector", ["slack:read"])
            assert "calls:read" in run("tools/call", "read_call_notes", 403)["error"]["message"]
            for result, source, refs in (
                (account, "crm", ["crm/accounts/acc-003", "crm/contacts/con-003"]),
                (notes, "calls", ["calls/call_notes/note-003"]),
                (messages, "slack", ["slack/messages/msg-003"]),
            ):
                data = result["structuredContent"]
                assert result["isError"] is False and data["source"] == source and data["untrusted_data"] is True
                assert [row["record_ref"] for row in data["records"]] == refs, "Wrong source references"
            config = json.loads(config_path.read_text())
            config["mcpServers"]["gtm-connector"]["headers"]["Authorization"] = "Bearer invalid-fixture-token"
            private_file(config_path, json.dumps(config).encode())
            run("initialize", status=401)
            receipts = runtime.connector.audit.read()
            assert len(receipts) == 10 and len({row["audit_id"] for row in receipts}) == 10
            assert [row["http_status"] for row in receipts] == [200, 403, 429, 200, 403, 403, 403, 200, 403, 403]
            assert [row["row_count"] for row in receipts] == [2, 0, 0, 1, 0, 0, 0, 1, 0, 0]
            assert all(row["status"] != "pending" for row in receipts)
            assert [row["audit_id"] for row in receipts if row["status"] == "ok"] == [
                account["_meta"]["gtm.demo/auditId"], notes["_meta"]["gtm.demo/auditId"],
                messages["_meta"]["gtm.demo/auditId"]]
            posts = [row for row in traffic if row["http_method"] == "POST" and row["path"] == "/mcp"]
            assert sum(row["method"] == "tools/call" for row in posts) == len(receipts)
            assert all(row["protocol_header"] == row["protocol_meta"] == PROTOCOL
                       and row["method_header"] == row["method"] for row in posts), "Protocol metadata mismatch"
            with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
                assert store.counts(connection)["activities"] == 0, "Unexpected activity"
    return {"status": "passed", "inspector_version": INSPECTOR, "protocol_version": PROTOCOL,
            "discovery_method": "server/discover", "tools": sorted(SCOPES),
            "schemas_checked": 12, "record_counts": {"crm": 2, "calls": 1, "slack": 1},
            "scope_denials": {"crm_only_calls": 403, "calls_only_crm": 403,
                             "crm_only_slack": 403, "calls_only_slack": 403,
                             "slack_only_crm": 403, "slack_only_calls": 403},
            "rate_limit": 429, "invalid_bearer": 401, "audit_receipts": 10,
            "activities_created": 0}


if __name__ == "__main__":
    try:
        print(json.dumps(check(), indent=2))
    except (AssertionError, OSError, RuntimeError, ValueError, KeyError) as error:
        # Do not echo external process output, configuration, or credentials.
        message = (str(error) if isinstance(error, (AssertionError, RuntimeError)) else "")
        print(json.dumps({
            "status": "failed", "error": type(error).__name__,
            "message": message or "Inspector check failed; verify Node 22, npx, and Python dependencies.",
        }), file=sys.stderr)
        raise SystemExit(1)
