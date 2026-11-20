"""Caller-run renewal briefs in selected mode.

A remote caller prepares a brief with its own grant. Each source read inside the
call is a separate audited read under that same grant, done in this process
rather than through another HTTP request. A saved brief is released again only
while the caller's current scopes, the current selection, and the current source
bindings still cover everything it contains. That check makes no source system call.
"""

from copy import deepcopy
from dataclasses import dataclass, field
from datetime import date, datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import time
import uuid

from mcp.server.mcpserver.exceptions import ToolError

from . import strict_json
from .briefing import BriefingError, assemble_evidence, source_names
from .seed import DEMO_AS_OF
from .selection import Selection
from .units import BriefRejected


BRIEF_SECONDS = 45
MAX_ARTIFACT_BYTES = 4 * 1024 * 1024
ARTIFACT_DIRECTORY = "selected-briefs"
NOTICE = ("This saved brief was not rechecked against what the source systems show now. "
          "Only the caller's current scopes, the current selection, and the current source bindings were checked.")
_BRIEF_ID = re.compile(r"brief-[0-9a-f]{32}\Z")
_DIGEST = re.compile(r"[0-9a-f]{64}\Z")
_ARTIFACT_KEYS = {"kind", "schema_version", "brief_id", "account_id", "as_of", "prepared_by",
                  "bindings", "slack_scopes", "sources", "text", "units", "gaps", "evidence"}


@dataclass(frozen=True)
class Invocation:
    """Server-established facts about one admitted tool call. Callers cannot supply any of them."""

    raw_token: str = field(repr=False)
    audit_id: str
    tool: str
    deadline: float


@dataclass(frozen=True)
class SelectedContext:
    """The immutable selection and the source bindings of the running server."""

    selection: Selection
    state_directory: Path
    crm_binding: dict | None
    calls_backend: str
    calls_binding: dict | None
    slack_backend: str
    slack_binding: dict | None
    slack_mapping_path: Path | None
    slack_connection_dir: Path | None = None
    sellers: tuple = ()

    def bindings(self, crm_backend: str) -> dict:
        return {"crm_backend": crm_backend, "crm": deepcopy(self.crm_binding),
                "calls_backend": self.calls_backend, "calls": deepcopy(self.calls_binding),
                "slack_backend": self.slack_backend, "slack": deepcopy(self.slack_binding)}


@dataclass(frozen=True)
class ChildRead:
    audit_id: str
    envelope: dict | None  # None when the caller's grant lacks this source's scope.
    private: dict = field(default_factory=dict)  # Facts for the brief only, never for the caller.


def _directory(context: SelectedContext) -> Path:
    path = context.state_directory / ARTIFACT_DIRECTORY
    if path.is_symlink():
        raise ToolError("brief_storage_failed")
    path.mkdir(mode=0o700, exist_ok=True)
    info = os.lstat(path)
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.getuid():
        raise ToolError("brief_storage_failed")
    path.chmod(0o700)
    return path


def _as_of(value, crm_backend: str) -> str:
    if value is None:
        return datetime.now(timezone.utc).date().isoformat() if crm_backend == "salesforce" else DEMO_AS_OF
    try:
        if date.fromisoformat(value).isoformat() == value:
            return value
    except (TypeError, ValueError):
        pass
    raise ToolError("invalid_as_of")


def _child(connector, invocation, tool, account_id, brief_id, ordinal) -> ChildRead:
    try:
        return connector.child_read(invocation, tool, account_id, brief_id=brief_id, ordinal=ordinal)
    except ToolError:
        raise
    except Exception:
        # Internal error text never reaches the caller.
        raise ToolError("brief_source_failed") from None


def prepare_brief(connector, invocation: Invocation, account_id: str, as_of) -> dict:
    """Read, check, and save one brief. Only the caller's grant authorizes each read."""
    context: SelectedContext = connector.selected
    as_of = _as_of(as_of, connector.crm_backend)
    include_slack = context.selection.includes_slack(account_id)
    brief_id = "brief-" + uuid.uuid4().hex
    # A source that is not connected is never called. The card says so.
    with_calls, with_slack = context.calls_backend != "none", context.slack_backend != "none"
    include_slack = include_slack and with_slack
    plan = [("get_account", "crm"), ("list_opportunities", "crm")]
    if with_calls:
        plan.append(("read_call_notes", "calls"))
    if include_slack:
        plan.append(("read_slack_messages", "slack"))
    statuses = {"notes_status": "available" if with_calls else "not_connected",
                "slack_status": ("available" if include_slack else "not_requested" if with_slack
                                 else "not_connected")}
    records, snapshot, children, slack_scopes = [], None, [], None
    for ordinal, (tool, source) in enumerate(plan, 1):
        if time.monotonic() >= invocation.deadline:
            raise ToolError("brief_deadline_exceeded")
        child = _child(connector, invocation, tool, account_id, brief_id, ordinal)
        children.append(child.audit_id)
        if tool == "read_slack_messages" and context.slack_backend == "slack" and child.envelope is not None:
            # The scopes of the credentials that returned the thread to the audited read. A
            # reader that reports none would let any later credential release the text.
            from .slack_auth import ALLOWED_USER_SCOPES
            reported = child.private.get("slack_scopes")
            if (not isinstance(reported, list) or not reported
                    or any(scope not in ALLOWED_USER_SCOPES for scope in reported)):
                raise ToolError("brief_evidence_invalid")
            slack_scopes = sorted(set(reported))
        if child.envelope is None:
            statuses["notes_status" if tool == "read_call_notes" else "slack_status"] = "denied"
            continue
        envelope = child.envelope
        if (envelope.get("source") != source or envelope.get("untrusted_data") is not True
                or not isinstance(envelope.get("records"), list)):
            raise ToolError("brief_evidence_invalid")
        if source == "slack" and context.slack_backend == "slack":
            snapshot = deepcopy(envelope.get("thread_snapshot"))
        records.extend(deepcopy(envelope["records"]))
    try:
        evidence, units, text, gaps = assemble_evidence(
            brief_id, account_id, as_of, records, connector.audit.read_ids(children), statuses, snapshot,
            crm_backend=connector.crm_backend, calls_backend=context.calls_backend,
            calls_binding=context.calls_binding, slack_backend=context.slack_backend,
            crm_binding=context.crm_binding, slack_binding=context.slack_binding, sellers=context.sellers)
    except BriefRejected:
        raise ToolError("brief_rejected") from None
    except (BriefingError, KeyError, TypeError, ValueError):
        raise ToolError("brief_evidence_invalid") from None
    if time.monotonic() >= invocation.deadline:
        raise ToolError("brief_deadline_exceeded")
    sources = source_names(connector.crm_backend, context.calls_backend, context.slack_backend,
                           include_slack or not with_slack)
    artifact = {
        "kind": "selected_brief", "schema_version": 3, "brief_id": brief_id, "account_id": account_id,
        "as_of": as_of,
        "prepared_by": {"outer_audit_id": invocation.audit_id,
                        "token_id": hashlib.sha256(invocation.raw_token.encode()).hexdigest()},
        "bindings": context.bindings(connector.crm_backend), "slack_scopes": slack_scopes,
        "sources": sources,
        "text": text, "units": units, "gaps": gaps, "evidence": evidence,
    }
    payload = json.dumps(artifact, ensure_ascii=False, allow_nan=False, sort_keys=True, separators=(",", ":")).encode()
    if len(payload) > MAX_ARTIFACT_BYTES:
        raise ToolError("brief_too_large")
    digest = hashlib.sha256(payload).hexdigest()
    path = _directory(context) / (brief_id + ".json")
    try:
        descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    except OSError:
        raise ToolError("brief_storage_failed") from None
    try:
        with os.fdopen(descriptor, "wb") as output:
            output.write(payload)
            output.flush()
            os.fsync(output.fileno())
    except OSError:
        # A partial file could never match a receipt digest; remove it anyway.
        path.unlink(missing_ok=True)
        raise ToolError("brief_storage_failed") from None
    if time.monotonic() >= invocation.deadline:
        path.unlink(missing_ok=True)
        raise ToolError("brief_deadline_exceeded")
    if connector.authority.verify_token_now(invocation.raw_token) is None:
        path.unlink(missing_ok=True)
        raise ToolError("invalid_token")
    reference = f"briefs/{brief_id}/{digest}"
    return {"source": "brief", "records": [{"id": brief_id, "record_ref": reference}], "untrusted_data": True,
            "brief": {"brief_id": brief_id, "account_id": account_id, "as_of": as_of, "text": text,
                      "units": units, "gaps": gaps, "sources": sources, **statuses}}


def _load(context: SelectedContext, brief_id) -> tuple[dict, str]:
    if not isinstance(brief_id, str) or not _BRIEF_ID.fullmatch(brief_id):
        raise ToolError("brief_not_found")
    path = context.state_directory / ARTIFACT_DIRECTORY / (brief_id + ".json")
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except OSError:
        raise ToolError("brief_not_found") from None
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_size > MAX_ARTIFACT_BYTES:
            raise ToolError("brief_invalid")
        with os.fdopen(descriptor, "rb", closefd=False) as source:
            payload = source.read(MAX_ARTIFACT_BYTES + 1)
    finally:
        os.close(descriptor)
    if len(payload) > MAX_ARTIFACT_BYTES:
        raise ToolError("brief_invalid")
    try:
        artifact = strict_json.loads(payload)
    except (ValueError, UnicodeError, RecursionError):
        raise ToolError("brief_invalid") from None
    # A brief prepared before gaps were saved has none. It still reads, under the digest it was saved with.
    if (not isinstance(artifact, dict) or set(artifact) | {"gaps"} != _ARTIFACT_KEYS
            or artifact["kind"] != "selected_brief" or artifact["schema_version"] != 3
            or artifact["brief_id"] != brief_id or not isinstance(artifact["evidence"], dict)
            or artifact["evidence"].get("brief_id") != brief_id
            or artifact["evidence"].get("account_id") != artifact["account_id"]
            or not isinstance(artifact["prepared_by"], dict)
            or not isinstance(artifact["prepared_by"].get("outer_audit_id"), str)
            or not isinstance(artifact["text"], str) or not isinstance(artifact["units"], list)
            or not isinstance(artifact.get("gaps", []), list)):
        raise ToolError("brief_invalid")
    return artifact, hashlib.sha256(payload).hexdigest()


def read_brief(connector, token, brief_id) -> dict:
    """Release a saved brief only while current permissions cover all of it. No source system is called."""
    context: SelectedContext = connector.selected
    artifact, digest = _load(context, brief_id)
    reference = f"briefs/{brief_id}/{digest}"
    account_id, evidence = artifact["account_id"], artifact["evidence"]
    # A prepared file counts only if its own outer call finished successfully with this exact content.
    try:
        [outer] = connector.audit.read_ids([artifact["prepared_by"]["outer_audit_id"]])
    except ValueError:
        raise ToolError("brief_not_accepted") from None
    if (outer["tool"] != "prepare_renewal_brief" or (outer["status"], outer["http_status"]) != ("ok", 200)
            or outer["rows"] != [reference] or not isinstance(outer["arguments"], dict)
            or outer["arguments"].get("account_id") != account_id
            or outer["token_id"] != artifact["prepared_by"].get("token_id")):
        raise ToolError("brief_not_accepted")
    notes = evidence.get("notes_status") == "available"
    slack = evidence.get("slack_status") == "available"
    if not context.selection.includes(account_id):
        raise ToolError("account_not_selected")
    if slack and not context.selection.includes_slack(account_id):
        raise ToolError("slack_not_selected")
    required = {"crm:read"} | ({"calls:read"} if notes else set()) | ({"slack:read"} if slack else set())
    if token is None or not required <= set(token.scopes):
        raise ToolError("brief_scope_not_covered")
    if artifact["bindings"] != context.bindings(connector.crm_backend):
        raise ToolError("brief_source_changed")
    try:
        if notes and context.calls_backend == "imported":
            note_ids = [record["id"] for record in evidence["records"]
                        if record["record_ref"].startswith("calls/call_notes/")]
            # The brief holds the account's selected transcript, or none when none was imported.
            if (len(note_ids) > 1
                    or connector.calls_reader.active_import_id(account_id) != (note_ids[0] if note_ids else None)):
                raise ToolError("brief_source_changed")
        if not (slack and context.slack_backend == "slack"):
            return _release(connector, token, brief_id, artifact, reference)
        from .slack_auth import SlackAuthError, held_slack_connection
        from .slack_mapping import load_slack_mapping, slack_identity_binding
        try:
            # Make the release decision while holding the connection lock, so a disconnect or
            # reconnect cannot land between the checks. Sending the response happens after the
            # lock is released. This reads local metadata only.
            with held_slack_connection(context.slack_connection_dir) as connection:
                if (connection.needs_login or connection.credential_state != "ready"
                        or slack_identity_binding(connection.identity) != context.slack_binding
                        or not isinstance(artifact["slack_scopes"], list) or not artifact["slack_scopes"]
                        or not set(artifact["slack_scopes"]) <= set(connection.granted_scopes)):
                    raise ToolError("brief_source_changed")
                mapping = load_slack_mapping(context.slack_mapping_path, account_id=account_id,
                                             crm_binding=context.crm_binding, slack_binding=context.slack_binding)
                if mapping["mapping_id"] != evidence["thread_snapshot"]["mapping"]["mapping_id"]:
                    raise ToolError("brief_source_changed")
                return _release(connector, token, brief_id, artifact, reference)
        except SlackAuthError as exc:
            if exc.code == "slack_connection_busy":
                raise ToolError("brief_source_busy") from None
            raise ToolError("brief_source_changed") from None
    except ToolError:
        raise
    except Exception:
        raise ToolError("brief_source_changed") from None


def _release(connector, token, brief_id, artifact, reference) -> dict:
    if connector.authority.verify_token_now(token.token) is None:
        raise ToolError("invalid_token")
    return {"source": "brief", "records": [{"id": brief_id, "record_ref": reference}], "untrusted_data": True,
            "brief": {"brief_id": brief_id, "account_id": artifact["account_id"], "as_of": artifact["as_of"],
                      "text": artifact["text"], "units": artifact["units"], "gaps": artifact.get("gaps"),
                      "sources": artifact["sources"],
                      "provider_rechecked": False,
                      "notice": NOTICE}}
