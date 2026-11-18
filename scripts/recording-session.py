#!/usr/bin/env python3
"""Record actual fixture commands and labeled excerpts, never credential files."""

from contextlib import closing, redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import shlex
import signal
import sqlite3
import subprocess
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
INSPECTOR = ["npx", "--yes", "@modelcontextprotocol/inspector@2.15.0", "--cli"]
SCOPES = {"search_accounts": "crm:read", "get_account": "crm:read",
          "list_opportunities": "crm:read", "read_call_notes": "calls:read",
          "read_slack_messages": "slack:read", "log_activity": "crm:write"}


def utc_now():
    return datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")


class Session:
    def __init__(self):
        os.umask(0o077)
        os.chdir(ROOT)
        (ROOT / "var").mkdir(exist_ok=True)
        self.private = Path(tempfile.mkdtemp(prefix="recording-", dir=ROOT / "var"))
        self.state = self.private / "state"
        self.terminal = sys.stdout
        self.transcript, self.commands, self.secrets = [], [], []
        # Do not pass model, Salesforce, or other API credentials to subprocesses.
        self.environment = {name: os.environ[name] for name in ("PATH", "HOME", "TMPDIR")
                            if name in os.environ}
        self.environment["PATH"] = str(ROOT / ".venv" / "bin") + os.pathsep + self.environment.get("PATH", "")
        self.environment["NO_COLOR"] = "1"
        self.started = utc_now()

    def relative(self, path):
        return str(path.relative_to(ROOT))

    def safe(self, value):
        if (not value.isascii() or any(ord(char) < 32 and char not in "\n\t" for char in value)
                or any(secret and secret in value for secret in self.secrets)
                or str(ROOT) in value or str(Path.home()) in value
                or "Bearer " in value or '"owner_secret"' in value):
            raise ValueError("unsafe presentation output")
        return value

    def show(self, value=""):
        value = self.safe(str(value))
        self.transcript.append(value)
        print(value, file=self.terminal, flush=True)

    def scene(self, title):
        print("\033[2J\033[H", end="", file=self.terminal, flush=True)
        self.show(title)
        self.show()

    def run(self, arguments, *, environment=None, timeout=300, display=False):
        if display:
            self.show("$ " + shlex.join(arguments))
        entry = {"argv": arguments, "started_at_utc": utc_now()}
        self.commands.append(entry)
        stem = self.private / f"command-{len(self.commands):02}"
        with subprocess.Popen(arguments, cwd=ROOT, env=environment or self.environment,
                              stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                              stderr=subprocess.PIPE, text=True, start_new_session=True) as process:
            try:
                stdout, stderr = process.communicate(timeout=timeout)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGKILL)
                stdout, stderr = process.communicate()
                stem.with_suffix(".stdout.txt").write_text(stdout)
                stem.with_suffix(".stderr.txt").write_text(stderr)
                raise RuntimeError("recorded command timed out") from None
        stem.with_suffix(".stdout.txt").write_text(stdout)
        stem.with_suffix(".stderr.txt").write_text(stderr)
        entry.update(exit_code=process.returncode, completed_at_utc=utc_now())
        return subprocess.CompletedProcess(arguments, process.returncode, stdout, stderr)

    def successful(self, arguments, **options):
        result = self.run(arguments, **options)
        if result.returncode != 0:
            raise RuntimeError("recorded command failed")
        return result

    def capture(self):
        changes = self.successful(["git", "status", "--porcelain", "--untracked-files=all",
                                   "--", ".", ":(exclude)docs/media"]).stdout
        if changes:
            raise ValueError("commit source changes before recording")
        revision = self.successful(["git", "rev-parse", "HEAD"]).stdout.strip()
        tree = self.successful(["git", "rev-parse", "HEAD^{tree}"]).stdout.strip()
        tests_run = self.successful([".venv/bin/python", "-m", "unittest", "discover", "-s", "tests"],
                                    timeout=600)
        tests = re.search(r"Ran (\d+) tests in ([0-9.]+)s\s+OK(?:\s|$)", tests_run.stderr)
        if not tests:
            raise ValueError("test proof did not match")
        self.scene("1. Prepare Juniper Crest Learning's renewal")
        demo_run = self.successful(["gtm-connector", "demo", "--state-dir", self.relative(self.state)],
                                   timeout=60, display=True)
        self.secrets.append(json.loads((self.state / "owner.json").read_text())["owner_secret"])
        artifacts = list((self.state / "briefs").glob("brief-*.json"))
        if len(artifacts) != 1:
            raise ValueError("demo must save one brief")
        full = json.loads(artifacts[0].read_text())
        from gtm_connector.audit import read_audit

        receipts = read_audit(self.state / "audit.sqlite3")
        expected = ["search_accounts", "read_call_notes", "get_account", "list_opportunities",
                    "read_call_notes", "read_slack_messages"]
        with closing(sqlite3.connect((self.state / "crm.sqlite3").as_uri() + "?mode=ro", uri=True)) as database:
            activities = database.execute("SELECT COUNT(*) FROM activities").fetchone()[0]
        if (full["status"] != "accepted" or full["account_id"] != "acc-007" or activities != 0
                or full["text"] not in demo_run.stdout or demo_run.stderr
                or [row["tool"] for row in receipts] != expected
                or [row["http_status"] for row in receipts] != [200, 403, 200, 200, 200, 200]
                or any(row["status"] == "pending" for row in receipts)):
            raise ValueError("renewal story proof did not match")
        self.show("Opening lines from the command's actual output:")
        opening = demo_run.stdout.split(full["text"], 1)[0].rstrip()
        if "Morgan Reed" not in opening or "Juniper Crest Learning" not in opening:
            raise ValueError("missing rep or account")
        self.show(opening)
        time.sleep(10)

        self.scene("2. Read the same account's checked brief")
        self.show("Exact CRM lines from that saved brief:")
        lines = [line for line in full["text"].splitlines() if line.startswith(
            ("- Account:", "- Renewal ", "- CRM opportunity ", "- Contact "))]
        if len(lines) < 3 or not any("[crm/accounts/acc-007]" in line for line in lines):
            raise ValueError("missing cited CRM context")
        for line in lines:
            self.show(line)
        time.sleep(10)

        self.scene("3. Read the call and Slack context as evidence")
        self.show("Exact lines from the same brief. Instructions remain quoted source data:")
        lines = [line for line in full["text"].splitlines() if line.startswith(
            ("- Untrusted call note note-007 ", "- Untrusted Slack message msg-007 "))]
        if (len(lines) != 2 or not any("[calls/call_notes/note-007]" in line for line in lines)
                or not any("[slack/messages/msg-007]" in line for line in lines)):
            raise ValueError("missing cited call or Slack context")
        for line in lines:
            self.show(line)
        self.show(f"Observed state: {activities} activities; {len(receipts)} completed tool receipts.")
        self.show("The fixed workflow requested read scopes only. Nothing was sent.")
        time.sleep(10)

        self.scene("4. Keep the follow-up proposed and the claims checked")
        self.show(full["proposed_follow_up"])
        candidate = self.private / "candidate.txt"
        candidate.write_text(full["text"])
        check_command = ["gtm-connector", "check-brief", "--json", full["brief_id"],
                         self.relative(candidate), "--state-dir", self.relative(self.state)]
        accepted = self.successful(check_command)
        if json.loads(accepted.stdout) != {"status": "valid", "brief_id": full["brief_id"]}:
            raise ValueError("generated brief was not accepted")
        self.show("Citation checker: accepted the saved brief.")
        candidate.write_text(full["text"] + "The renewal was approved.\n")
        self.show("Candidate: the same brief plus 'The renewal was approved.'")
        checked = self.run(check_command, display=True)
        error = json.loads(checked.stderr).get("error", {})
        if (checked.returncode != 4 or checked.stdout or error.get("message") !=
                "Brief contains an uncited, unsupported, or misattributed statement."
                or not error.get("next")):
            raise ValueError("citation rejection did not match")
        self.show(checked.stderr.rstrip())
        self.show("Exit: 4. Standard output is empty.")
        time.sleep(10)

        self.scene("5. List tools through the official Inspector CLI")
        node = self.successful(["node", "--version"]).stdout.strip()
        inspector = self.successful(INSPECTOR + ["--version"]).stdout.strip()
        if inspector != "2.15.0":
            raise ValueError("unexpected Inspector version")
        from gtm_connector.inspector import write_inspector_config
        from gtm_connector.runtime import Runtime
        from jsonschema.validators import validator_for

        environment = dict(self.environment, MCP_STORAGE_DIR=str(self.private / "storage"),
                           MCP_CLIENT_CONFIG_PATH=str(self.private / "client.json"),
                           MCP_INSPECTOR_OAUTH_STATE_PATH=str(self.private / "oauth.json"),
                           MCP_INSPECTOR_SECRET_STORE="memory", MCP_AUTO_OPEN_ENABLED="false",
                           NODE_TLS_REJECT_UNAUTHORIZED="1", NO_PROXY="127.0.0.1", no_proxy="127.0.0.1")
        with (self.private / "runtime.log").open("w") as log, redirect_stdout(log), redirect_stderr(log), \
                Runtime(self.private / "inspector-state") as runtime:
            summary = write_inspector_config(runtime.directory, ["crm:read"])
            self.secrets += [runtime.config["owner_secret"], *runtime.connector.authority.tokens]
            environment["NODE_EXTRA_CA_CERTS"] = summary["certificate"]
            self.show("Fixture backend. Fresh loopback TLS server and an explicit crm:read grant.")
            result = self.successful(INSPECTOR + ["--config", self.relative(Path(summary["config"])),
                "--server", "gtm-connector", "--stored-auth-only", "--quiet", "--format", "json",
                "--method", "tools/list"], environment=environment, timeout=60, display=True)
            self.safe(result.stdout)
            self.safe(result.stderr)
            tools = json.loads(result.stdout)["result"]["tools"]
            projection = {tool["name"]: tool["_meta"]["gtm.demo/requiredScope"] for tool in tools}
            if len(tools) != 6 or projection != SCOPES or result.stderr:
                raise ValueError("Inspector listing did not match")
            for tool in tools:
                for field in ("inputSchema", "outputSchema"):
                    validator_for(tool[field]).check_schema(tool[field])
        self.show("Name and required-scope projection of the actual Inspector JSON:")
        self.show(json.dumps(projection, indent=2))
        self.show("Six tools returned by Inspector CLI.")
        time.sleep(10)
        provenance = {"started_at_utc": self.started, "completed_at_utc": utc_now(),
            "git_revision": revision, "git_tree": tree,
            "recording_script_sha256": hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            "versions": {"python": sys.version.split()[0], "mcp_sdk": version("mcp"),
                         "node": node, "inspector": inspector},
            "commands": self.commands, "intentional_pause_seconds": 50,
            "draft_adapter": "fixture", "model": None, "inspector_scopes": ["crm:read"],
            "demo": {"account_id": "acc-007", "rep": "Morgan Reed", "receipt_count": len(receipts),
                     "activities": activities, "brief_id": full["brief_id"],
                     "sources": {"crm": "demo_fixture", "calls": "demo_fixture", "slack": "demo_fixture"}},
            "tests": {"run": int(tests[1]), "result": "OK", "elapsed_seconds": float(tests[2])}}
        artifacts = {"inspector-tools.json": result.stdout,
                     "recording-session.txt": "\n".join(self.transcript) + "\n",
                     "recording-provenance.json": json.dumps(provenance, indent=2) + "\n"}
        for content in artifacts.values():
            self.safe(content)
        media = ROOT / "docs" / "media"
        media.mkdir(parents=True, exist_ok=True)
        for name, content in artifacts.items():
            (media / name).write_text(content)


if __name__ == "__main__":
    try:
        Session().capture()
    except (Exception, KeyboardInterrupt):
        print("Recording stopped. A required command or output check failed; no raw logs were displayed.",
              file=sys.stderr)
        raise SystemExit(1) from None
