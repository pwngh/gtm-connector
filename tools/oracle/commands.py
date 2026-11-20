"""The seven commands' --json output and exit codes against a demo state folder, and two folders a Go verifier
admits: a card saved by the CLI and a brief prepared by an assistant."""

from contextlib import closing, redirect_stderr, redirect_stdout
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shutil
import sqlite3
import tempfile
from typing import Any

from gtm_connector import cli
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime
from gtm_connector.selection import load_selection
from oracle import freeze
from oracle.output import Writer


# What an ID, a port, or the state folder looks like in output. Each distinct value is numbered by where it first
# appears in the session, so Go output is compared after the same normalization.
_SHAPES = (
    ("uuid", re.compile(r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}")),
    ("brief", re.compile(r"brief-[0-9a-f]{32}")),
    ("run", re.compile(r"(?:demo|inspect|import)-[0-9a-f]{32}")),
    ("port", re.compile(r"(?<=127\.0\.0\.1:)[0-9]{1,5}")),
)
# A card's digest follows its brief ID in a seal receipt's row.
_SEALED = re.compile(r"(briefs/<brief-[0-9]+>/)[0-9a-f]{64}")


class Session:
    """Runs commands in process, as a user would, and keeps each one's normalized output and exit code."""

    def __init__(self, folder: Path) -> None:
        self.folder, self.state = folder, folder / "state"
        self.receipts = 0
        self.numbers: dict[str, str] = {}
        self.tokens: set[str] = set()
        self.times: set[str] = set()
        self.rows: list[dict] = []
        self.count = 0

    def normalize(self, text: str) -> str:
        text = text.replace(str(self.state), "<state>").replace(str(self.folder), "<folder>")
        found: list[tuple[int, str, str]] = []
        for name, pattern in _SHAPES:
            found += [(match.start(), name, match.group()) for match in pattern.finditer(text)]
        for value in sorted(self.tokens | self.times, key=len, reverse=True):
            found += [(match.start(), "token" if value in self.tokens else "time", value)
                      for match in re.finditer(re.escape(value), text)]
        for _, name, value in sorted(found):
            if value not in self.numbers:
                count = sum(item.startswith(f"<{name}-") for item in self.numbers.values())
                self.numbers[value] = f"<{name}-{count + 1}>"
        for value in sorted(self.numbers, key=len, reverse=True):
            text = text.replace(value, self.numbers[value])
        return _SEALED.sub(r"\1<sha256>", text)

    def run(self, label: str, *arguments: str) -> dict:
        """One command. Its IDs come from its own scope, and the receipt clock runs on through the session."""
        self.count += 1
        output, errors = io.StringIO(), io.StringIO()
        with freeze.scope(f"commands/{self.count}", receipts=self.receipts) as scope, redirect_stdout(output), \
                redirect_stderr(errors):
            code = cli.main(list(arguments))
        self.receipts = scope.receipts
        for kind, value in scope.issued:
            if kind == "token":
                self.tokens.add(hashlib.sha256(value.encode()).hexdigest())
            elif kind == "time":
                self.times.add(value)
        stdout, stderr = output.getvalue(), errors.getvalue()
        normalized = {"stdout": self.normalize(stdout), "stderr": self.normalize(stderr)}
        row = {"label": label, "argv": [self.normalize(argument) for argument in arguments], "exit": code,
               "stdout": normalized["stdout"], "stderr": normalized["stderr"],
               "json": _json(normalized["stdout"]), "compared": "help text is reviewed, not compared"
               if "--help" in arguments or arguments[:1] == ("help",) else "exactly"}
        self.rows.append(row)
        return {"exit": code, "json": _json(stdout)}


def _json(text: str) -> Any:
    """The output as JSON values: one document, or one per line, or null when it is not JSON."""
    if not text.strip():
        return None
    try:
        return json.loads(text)
    except ValueError:
        pass
    try:
        return [json.loads(line) for line in text.splitlines() if line.strip()]
    except ValueError:
        return None


def _session(folder: Path) -> list[dict]:
    session = Session(folder)
    state = session.state
    where = ("--state-dir", str(state))
    session.run("no arguments as JSON", "--json")
    session.run("version", "--version", "--json")
    session.run("unknown command", "nosuch", "--json")
    session.run("brief without an account", "--json", "brief")
    session.run("connect demo", "connect", "demo", *where, "--json")
    session.run("connect status", "connect", *where, "--json")
    session.run("demo", "demo", *where, "--json")
    session.run("accounts", "accounts", *where, "--json")
    session.run("accounts by name", "accounts", "--query", "Thorn", *where, "--json")
    session.run("accounts by a literal pattern character", "accounts", "--query", "%_", *where, "--json")
    brief = session.run("brief", "brief", "acc-003", *where, "--json")
    session.run("brief without calls", "--json", "brief", "acc-007", "--without-calls", *where)
    session.run("brief as of another day", "brief", "acc-007", "--as-of", "2027-03-01", *where, "--json")
    session.run("brief of an unknown account", "brief", "acc-999", *where, "--json")
    session.run("brief of a malformed account", "brief", "acct-3", *where, "--json")
    session.run("brief on an impossible day", "brief", "acc-003", "--as-of", "2027-02-30", *where, "--json")
    brief_id = brief["json"]["brief_id"]
    receipt = brief["json"]["sealed_by"]
    session.run("inspect an account", "inspect", "acc-003", *where, "--json")
    session.run("inspect a card", "inspect", brief_id, *where, "--json")
    session.run("inspect numbers", "inspect", brief_id, "1", "5", *where, "--json")
    session.run("inspect by role", "inspect", brief_id, "--role", "customer", *where, "--json")
    session.run("inspect a number off the card", "inspect", brief_id, "999", *where, "--json")
    session.run("inspect an unknown card", "inspect", "brief-" + "0" * 32, *where, "--json")
    session.run("inspect a receipt", "inspect", receipt, *where, "--json")
    session.run("audit", "audit", *where, "--json")
    session.run("audit by tool", "audit", "--tool", "save_brief", *where, "--json")
    session.run("audit by brief", "audit", "--brief", brief_id, *where, "--json")
    session.run("audit of a missing folder", "audit", "--state-dir", str(folder / "missing"), "--json")
    session.run("mcp help", "mcp", "--help")
    session.run("help mcp", "help", "mcp")
    session.run("mcp without its action", "mcp", "--json")
    session.run("mcp without a scope", "mcp", "stdio", "--json")
    return session.rows


def _audit_rows(directory: Path) -> list[dict]:
    with closing(sqlite3.connect((directory / "audit.sqlite3").as_uri() + "?mode=ro", uri=True)) as database:
        database.row_factory = sqlite3.Row
        return [dict(row) for row in database.execute("SELECT * FROM audit_log ORDER BY rowid")]


def _folder(writer: Writer, name: str, directory: Path, keep: tuple[str, ...]) -> None:
    """A state folder as a verifier reads it: the card files byte for byte, the audit log as a gzip of its SQLite
    file, and its rows as JSON. The SQLite file's bytes depend on the SQLite library, so the manifest digests its
    rows instead."""
    for folder in keep:
        for path in sorted((directory / folder).glob("*.json")):
            writer.raw(f"folders/{name}/{folder}/{path.name}", path.read_bytes())
    profile = directory / "profile.json"
    if profile.exists():
        writer.raw(f"folders/{name}/profile.json", profile.read_bytes())
    rows = _audit_rows(directory)
    writer.lines(f"folders/{name}/audit.jsonl", rows)
    writer.raw(f"folders/{name}/audit.sqlite3.gz", (directory / "audit.sqlite3").read_bytes(), len(rows),
               digested=json.dumps(rows, ensure_ascii=True).encode())


def _cli_card(writer: Writer, folder: Path) -> None:
    state = folder / "cli-card"
    for step, arguments in enumerate((("connect", "demo"), ("brief", "acc-003"))):
        with freeze.scope(f"folders/cli_card/{step}"), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            if cli.main([*arguments, "--state-dir", str(state), "--json"]):
                raise AssertionError(f"{arguments} failed while making the CLI card folder")
    _folder(writer, "cli_card", state, ("briefs",))


def _prepared(writer: Writer, folder: Path) -> None:
    """A brief an assistant prepared in selected mode, with a grant that lacks Slack."""
    state, path = folder / "prepared", folder / "selection.json"
    path.write_text(json.dumps({"schema_version": 1, "accounts": [{"account_id": "acc-007", "slack": True}]}))
    os.chmod(path, 0o600)
    selection = load_selection(path, crm_backend="sqlite")
    with freeze.scope("folders/prepared"), Runtime(state, selection=selection) as runtime, \
            LocalClient(runtime.config) as owner:
        token = owner.authorize(["crm:read", "calls:read"])
        response = owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"})
        result = response.json()["result"]
        if response.status_code != 200 or result.get("isError"):
            raise AssertionError(f"the prepared brief was refused: {response.status_code} {response.text[:500]}")
        writer.json("folders/prepared/tool_result.json", {
            "note": "the prepare_renewal_brief result as the assistant received it", "selection": json.loads(
                path.read_text()), "scopes": ["crm:read", "calls:read"], "result": result["structuredContent"]}, 1)
    _folder(writer, "prepared", state, ("selected-briefs",))


def generate(writer: Writer) -> None:
    with tempfile.TemporaryDirectory(prefix="oracle-") as name:
        folder = Path(name)
        rows = _session(folder)
        writer.lines("commands/session.jsonl", rows)
        _cli_card(writer, folder)
        _prepared(writer, folder)
        shutil.rmtree(folder / "state", ignore_errors=True)
    writer.json("commands/index.json", {
        "note": ("commands run in order against one demo state folder; IDs, ports, token fingerprints, receipt "
                 "times and the folder are numbered placeholders by first appearance; a seal's digest is <sha256>"),
        "commands": len(rows)}, len(rows))
