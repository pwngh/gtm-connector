"""The seven commands' --json output and exit codes against a demo state folder, and two folders a Go verifier
admits: a card saved by the CLI and a brief prepared by an assistant."""

from contextlib import closing, redirect_stderr, redirect_stdout
import hashlib
import io
import json
import os
from pathlib import Path
import re
import shlex
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


def _unquoted(text: str, path: str) -> str:
    """Where the package shell-quotes an argument that holds the path, as shlex.quote does when the path has a
    space or a quote, the argument is written as it would be without those quotes. So a folder whose name needs
    quoting normalizes as one that does not."""
    if shlex.quote(path) == path:
        return text
    quoted = re.compile("'" + re.escape(path.replace("'", "'\"'\"'")) + "((?:[^']|'\"'\"')*)'")
    return quoted.sub(lambda found: path + found[1].replace("'\"'\"'", "'"), text)


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
        # The state folder is inside the session folder, so one pass covers both.
        text = _unquoted(text, str(self.folder))
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


def _audit_log(directory: Path) -> dict:
    """The audit log as a verifier reads it: its schema as sqlite_master holds it, its column names, and its rows."""
    with closing(sqlite3.connect((directory / "audit.sqlite3").as_uri() + "?mode=ro", uri=True)) as database:
        schema = [list(row) for row in database.execute(
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")]
        cursor = database.execute("SELECT * FROM audit_log ORDER BY rowid")
        columns = [column[0] for column in cursor.description]
        rows = [dict(zip(columns, row)) for row in cursor]
    return {"schema": schema, "columns": columns, "rows": rows}


def _folder(writer: Writer, name: str, directory: Path, keep: tuple[str, ...]) -> None:
    """A state folder as a verifier reads it: the card files byte for byte, the audit log as a gzip of its SQLite
    file, and its rows as JSON. The SQLite file's bytes depend on the SQLite library, so the manifest digests its
    schema, column names and rows instead."""
    for folder in keep:
        for path in sorted((directory / folder).glob("*.json")):
            writer.raw(f"folders/{name}/{folder}/{path.name}", path.read_bytes())
    profile = directory / "profile.json"
    if profile.exists():
        writer.raw(f"folders/{name}/profile.json", profile.read_bytes())
    log = _audit_log(directory)
    writer.lines(f"folders/{name}/audit.jsonl", log["rows"])
    writer.raw(f"folders/{name}/audit.sqlite3.gz", (directory / "audit.sqlite3").read_bytes(), len(log["rows"]),
               digested=json.dumps(log, ensure_ascii=True).encode())


def _cli_card(writer: Writer, folder: Path) -> None:
    state = folder / "cli-card"
    for step, arguments in enumerate((("connect", "demo"), ("brief", "acc-003"))):
        with freeze.scope(f"folders/cli_card/{step}"), redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()):
            if cli.main([*arguments, "--state-dir", str(state), "--json"]):
                raise AssertionError(f"{arguments} failed while making the CLI card folder")
    _folder(writer, "cli_card", state, ("briefs",))


def _selection(path: Path, accounts: list[dict]) -> Any:
    path.write_text(json.dumps({"schema_version": 1, "accounts": accounts}))
    os.chmod(path, 0o600)
    return load_selection(path, crm_backend="sqlite")


class _Reads:
    """read_renewal_brief calls against the prepared brief: each one's change, grant, response and receipts."""

    def __init__(self, brief_id: str) -> None:
        self.brief_id = brief_id
        self.rows: list[dict] = []

    def read(self, runtime: Any, owner: Any, label: str, change: str, scopes: list[str],
             token: str | None = None) -> dict:
        token = owner.authorize(scopes) if token is None else token
        before = len(runtime.connector.audit.read())
        response = owner.call(token, "read_renewal_brief", {"brief_id": self.brief_id})
        row = {"label": label, "change": change, "scopes": scopes, "arguments": {"brief_id": self.brief_id},
               "http_status": response.status_code, "response": response.json(),
               "receipts": runtime.connector.audit.read()[before:]}
        self.rows.append(row)
        return row


def _prepared(writer: Writer, folder: Path) -> None:
    """A brief an assistant prepared in selected mode, with a grant that lacks Slack, saved as a verifier admits
    it. Then read_renewal_brief reads it again, and is refused when its bytes, its selection, its grant or the
    token's lifetime no longer cover it."""
    state, scopes = folder / "prepared", ["crm:read", "calls:read"]
    selection = _selection(folder / "selection.json", [{"account_id": "acc-007", "slack": True}])
    with freeze.scope("folders/prepared") as scope, Runtime(state, selection=selection) as runtime, \
            LocalClient(runtime.config) as owner:
        token = owner.authorize(scopes)
        response = owner.call(token, "prepare_renewal_brief", {"account_id": "acc-007"})
        result = response.json()["result"]
        if response.status_code != 200 or result.get("isError"):
            raise AssertionError(f"the prepared brief was refused: {response.status_code} {response.text[:500]}")
        writer.json("folders/prepared/tool_result.json", {
            "note": "the prepare_renewal_brief result as the assistant received it", "selection": json.loads(
                (folder / "selection.json").read_text()), "scopes": scopes, "result": result["structuredContent"]}, 1)
        # The folder is saved as preparing left it. The reads below add their own receipts, kept in reads.jsonl.
        _folder(writer, "prepared", state, ("selected-briefs",))
        brief_id = result["structuredContent"]["brief"]["brief_id"]
        saved = state / "selected-briefs" / f"{brief_id}.json"
        payload = saved.read_bytes()
        reads = _Reads(brief_id)
        row = reads.read(runtime, owner, "read again", "none", scopes, token)
        released = row["response"]["result"]["structuredContent"]
        if (row["http_status"] != 200 or row["response"]["result"].get("isError")
                or released["records"][0]["record_ref"] != f"briefs/{brief_id}/{hashlib.sha256(payload).hexdigest()}"
                or released["brief"]["text"] != json.loads(payload)["text"]):
            raise AssertionError(f"the prepared brief did not read back as saved: {row['response']}")
        reads.read(runtime, owner, "missing scope", "the grant lacks calls:read, which the brief's call notes need",
                   ["crm:read"])
        saved.write_bytes(payload + b"\n")
        reads.read(runtime, owner, "changed seal", "a newline is appended to the saved brief, so its bytes no "
                   "longer match the digest its seal receipt names", scopes)
        saved.write_bytes(payload)
        # The token's lifetime, by the clock the authority and the gateway read: admitted one second before it
        # ends, refused at the second it ends.
        token, lifetime = owner.authorize_with_lifetime(scopes)
        freeze.CLOCK.advance(lifetime - 1)
        reads.read(runtime, owner, "token one second before expiry", f"the clock advanced {lifetime - 1} seconds "
                   "after the token was issued", scopes, token)
        freeze.CLOCK.advance(1)
        reads.read(runtime, owner, "token at expiry", f"the clock advanced {lifetime} seconds after the token was "
                   "issued", scopes, token)
        receipts = scope.receipts
    other = _selection(folder / "other-selection.json", [{"account_id": "acc-003", "slack": True}])
    with freeze.scope("folders/prepared/other selection", receipts=receipts), \
            Runtime(state, selection=other) as runtime, LocalClient(runtime.config) as owner:
        reads.read(runtime, owner, "changed selection", "the connector now runs with a selection of acc-003 only",
                   scopes)
    if saved.read_bytes() != payload:
        raise AssertionError("the prepared brief changed while it was read")
    writer.lines("folders/prepared/reads.jsonl", reads.rows)


def _prepared_as_of(writer: Writer, folder: Path) -> None:
    """A brief prepared with an explicit as_of and every scope: the outer receipt records the arguments as given,
    and the card counts its dates from that day."""
    state, scopes = folder / "prepared-as-of", ["crm:read", "calls:read", "slack:read"]
    selection = _selection(folder / "as-of-selection.json", [{"account_id": "acc-003", "slack": True}])
    with freeze.scope("folders/prepared_as_of"), Runtime(state, selection=selection) as runtime, \
            LocalClient(runtime.config) as owner:
        token = owner.authorize(scopes)
        response = owner.call(token, "prepare_renewal_brief", {"account_id": "acc-003", "as_of": "2027-01-20"})
        result = response.json()["result"]
        if response.status_code != 200 or result.get("isError"):
            raise AssertionError(f"the prepared brief was refused: {response.status_code} {response.text[:500]}")
        writer.json("folders/prepared_as_of/tool_result.json", {
            "note": "the prepare_renewal_brief result as the assistant received it", "selection": json.loads(
                (folder / "as-of-selection.json").read_text()), "scopes": scopes,
            "arguments": {"account_id": "acc-003", "as_of": "2027-01-20"}, "result": result["structuredContent"]}, 1)
        _folder(writer, "prepared_as_of", state, ("selected-briefs",))


def _prepared_no_slack(writer: Writer, folder: Path) -> None:
    """A brief prepared for an account selected without Slack, under a grant that includes slack:read: no Slack
    read is made, and the card says Slack was not requested."""
    state, scopes = folder / "prepared-no-slack", ["crm:read", "calls:read", "slack:read"]
    selection = _selection(folder / "no-slack-selection.json", [{"account_id": "acc-003", "slack": False}])
    with freeze.scope("folders/prepared_no_slack"), Runtime(state, selection=selection) as runtime, \
            LocalClient(runtime.config) as owner:
        token = owner.authorize(scopes)
        response = owner.call(token, "prepare_renewal_brief", {"account_id": "acc-003"})
        result = response.json()["result"]
        if response.status_code != 200 or result.get("isError"):
            raise AssertionError(f"the prepared brief was refused: {response.status_code} {response.text[:500]}")
        writer.json("folders/prepared_no_slack/tool_result.json", {
            "note": "the prepare_renewal_brief result as the assistant received it", "selection": json.loads(
                (folder / "no-slack-selection.json").read_text()), "scopes": scopes,
            "arguments": {"account_id": "acc-003"}, "result": result["structuredContent"]}, 1)
        _folder(writer, "prepared_no_slack", state, ("selected-briefs",))


def generate(writer: Writer) -> None:
    with tempfile.TemporaryDirectory(prefix="oracle-") as name:
        folder = Path(name)
        rows = _session(folder)
        writer.lines("commands/session.jsonl", rows)
        _cli_card(writer, folder)
        _prepared(writer, folder)
        _prepared_as_of(writer, folder)
        _prepared_no_slack(writer, folder)
        shutil.rmtree(folder / "state", ignore_errors=True)
    writer.json("commands/index.json", {
        "note": ("commands run in order against one demo state folder; IDs, ports, token fingerprints, receipt "
                 "times and the folder are numbered placeholders by first appearance; a seal's digest is <sha256>"),
        "commands": len(rows)}, len(rows))
