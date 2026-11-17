#!/usr/bin/env python3
"""Record actual fixture commands and labeled excerpts, never credential files."""

from contextlib import redirect_stderr, redirect_stdout
from datetime import datetime, timezone
import hashlib
from importlib.metadata import version
import json
import os
from pathlib import Path
import re
import shlex
import signal
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
        revision = self.successful(["git", "rev-parse", "HEAD"]).stdout.strip()
        tree = self.successful(["git", "rev-parse", "HEAD^{tree}"]).stdout.strip()
        self.scene("1. Run the fixture demo")
        self.show("Actual execution. Dependency and test logs are captured privately.")
        demo_run = self.successful(["./scripts/demo.sh", "--state-dir", self.relative(self.state)],
                                   timeout=600, display=True)
        demo = json.loads(demo_run.stdout)
        tests = re.search(r"Ran (\d+) tests in ([0-9.]+)s\s+OK(?:\s|$)", demo_run.stderr)
        if not tests or (demo["accounts"], demo["notes_without_scope"]["status"],
                         demo["injection_note"]["activities_created"],
                         demo["idempotent_write"]["requests"],
                         demo["idempotent_write"]["activities"]) != (30, 403, 0, 2, 1):
            raise ValueError("demo proof did not match")
        self.secrets.append(json.loads((self.state / "owner.json").read_text())["owner_secret"])
        self.show("Selected output:")
        self.show(f"Test suite: OK ({tests[1]} tests in {tests[2]} seconds)")
        self.show(f"Accounts: {demo['accounts']}")
        self.show(f"Call-note scope denial: HTTP {demo['notes_without_scope']['status']}")
        self.show(f"Injected-note activities: {demo['injection_note']['activities_created']}")
        self.show(f"Idempotent write: {demo['idempotent_write']['requests']} requests, "
                  f"{demo['idempotent_write']['activities']} activity")
        time.sleep(10)

        self.scene("2. Read the cited renewal conflict")
        full = next(brief for brief in demo["briefs"] if brief["account_id"] == "acc-003")
        lines = [line for line in full["text"].splitlines() if line.startswith(
            ("- CRM opportunity opp-003:", "- Untrusted call note note-003 ", "- Source conflict:"))]
        if len(lines) != 3:
            raise ValueError("missing cited conflict")
        self.show("Three exact lines from the demo's acc-003 brief:")
        for line in lines:
            self.show(line)
        candidate = self.private / "candidate.txt"
        candidate.write_text(full["text"])
        check_command = [".venv/bin/python", "-m", "gtm_connector", "check-brief", full["brief_id"],
                         self.relative(candidate), "--state-dir", self.relative(self.state)]
        accepted = self.successful(check_command)
        if json.loads(accepted.stdout) != {"status": "valid", "brief_id": full["brief_id"]}:
            raise ValueError("generated brief was not accepted")
        self.show("Citation checker: valid.")
        time.sleep(10)

        self.scene("3. Keep missing permission visible")
        self.show("Recorded response excerpt for a CRM-only grant:")
        self.show(json.dumps(demo["notes_without_scope"], indent=2))
        restricted = next(brief for brief in demo["briefs"] if brief["account_id"] == "acc-001")
        denied = [line for line in restricted["text"].splitlines()
                  if line.startswith("- Call notes were unavailable")]
        if len(denied) != 1:
            raise ValueError("missing scope-denial disclosure")
        self.show("Exact line from the restricted brief:")
        self.show(denied[0])
        time.sleep(10)

        self.scene("4. Reject an uncited sentence")
        candidate.write_text(full["text"] + "The renewal was approved.\n")
        self.show("Candidate: the generated brief plus 'The renewal was approved.'")
        checked = self.run(check_command, display=True)
        if (checked.returncode != 2 or checked.stdout or checked.stderr !=
                "error: Brief contains an uncited, unsupported, or misattributed statement.\n"):
            raise ValueError("citation rejection did not match")
        self.show(checked.stderr.rstrip())
        self.show("Exit: 2. Standard output is empty.")
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
