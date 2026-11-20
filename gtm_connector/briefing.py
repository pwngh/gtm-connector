"""Read one account through the audited tools and render its Deal card."""

from copy import deepcopy
from datetime import date
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
from tempfile import NamedTemporaryFile
import time
import uuid

import httpx2

from . import strict_json
from .seed import DEMO_AS_OF, SELLERS
from .units import SANITIZER_VERSION, BriefRejected, build, neutralized, trace


class BriefingError(RuntimeError):
    """Required evidence could not be collected; no brief can be published."""


def collect_evidence(client, token, audit, account_id, brief_id, as_of=DEMO_AS_OF,
                     *, include_slack=False, crm_backend="sqlite", calls_backend="fixture", calls_binding=None,
                     slack_backend="fixture", crm_binding=None, slack_binding=None, sellers=()):
    """Read through MCP and return the checked evidence, its units, the card, and what it did not read or show.

    A calls or Slack scope denial is kept as an outcome the card states.
    """
    if crm_backend not in ("sqlite", "salesforce"):
        raise ValueError("unsupported CRM backend")
    if calls_backend not in ("fixture", "imported", "none"):
        raise ValueError("unsupported calls backend")
    if slack_backend not in ("fixture", "slack", "none"):
        raise ValueError("unsupported Slack backend")
    pattern = r"acc-sf-[A-Za-z0-9]{18}" if crm_backend == "salesforce" else r"acc-[0-9]{3}"
    if not isinstance(account_id, str) or not re.fullmatch(pattern, account_id):
        raise ValueError("account ID must use acc-sf- followed by the 18-character Salesforce ID"
                         if crm_backend == "salesforce" else "account ID must have the form acc-001")
    if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
        raise ValueError("as-of date must use YYYY-MM-DD")
    # A source that is not connected is never called. The card says so.
    records, statuses = [], {"notes_status": "not_connected" if calls_backend == "none" else "available",
                             "slack_status": "not_connected" if slack_backend == "none" else "not_requested"}
    thread_snapshot = None
    read_plan = [("get_account", "crm"), ("list_opportunities", "crm")]
    optional_scopes = {}
    if calls_backend != "none":
        read_plan.append(("read_call_notes", "calls"))
        optional_scopes["read_call_notes"] = ("notes_status", "calls:read")
    if include_slack and slack_backend != "none":
        read_plan.append(("read_slack_messages", "slack"))
        statuses["slack_status"] = "available"
        optional_scopes["read_slack_messages"] = ("slack_status", "slack:read")
    for tool, source in read_plan:
        try:
            response = client.call(token, tool, {"account_id": account_id}, brief_id=brief_id)
        except httpx2.HTTPError as exc:
            raise BriefingError(f"{tool}: transport failed; no brief generated") from exc
        if tool in optional_scopes and response.status_code == 403:
            status_field, required_scope = optional_scopes[tool]
            challenge = response.headers.get("www-authenticate", "")
            if 'error="insufficient_scope"' in challenge and f'scope="{required_scope}"' in challenge:
                statuses[status_field] = "denied"
                continue
        if response.status_code != 200:
            raise BriefingError(f"{tool}: HTTP {response.status_code}; no brief generated")
        try:
            result = response.json()["result"]
            if result.get("isError") is True and slack_backend == "slack" and source == "slack":
                from .slack_reader import SLACK_READ_ERROR_CODES, SlackReadError
                content = result.get("content")
                if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                        and content[0].get("type") == "text"):
                    if content[0].get("text") == f"Error executing tool {tool}: slack_mapping_missing":
                        # No thread is selected for this account. The card states it.
                        statuses["slack_status"] = "not_selected"
                        continue
                    for code in SLACK_READ_ERROR_CODES:
                        if content[0].get("text") == f"Error executing tool {tool}: {code}":
                            raise SlackReadError(code)
            if result.get("isError") is True and calls_backend == "imported" and source == "calls":
                from .calls_import import CallsImportError, ERROR_CODES
                content = result.get("content")
                if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                        and content[0].get("type") == "text"):
                    for code in ERROR_CODES:
                        if content[0].get("text") == f"Error executing tool {tool}: {code}":
                            raise CallsImportError(code)
            if result.get("isError") is True and crm_backend == "salesforce" and source == "crm":
                from .salesforce import SalesforceReadError, _ERROR_CODES
                # Only exact SDK messages for our fixed adapter codes are diagnostics.
                # Never surface arbitrary tool content or an upstream response body.
                content = result.get("content")
                if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                        and content[0].get("type") == "text"):
                    for code in _ERROR_CODES:
                        if content[0].get("text") == f"Error executing tool {tool}: {code}":
                            raise SalesforceReadError(code)
            if result.get("isError") or result.get("resultType") != "complete":
                raise ValueError("incomplete or failed tool result")
            data = result["structuredContent"]
            if data["source"] != source or data.get("untrusted_data") is not True:
                raise ValueError("unexpected source result")
            rows = data["records"]
            if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
                raise ValueError("invalid record collection")
            if source == "slack" and slack_backend == "slack":
                if set(data) != {"source", "records", "untrusted_data", "thread_snapshot"}:
                    raise ValueError("invalid Slack source envelope")
                thread_snapshot = deepcopy(data["thread_snapshot"])
            records.extend(rows)
        except (KeyError, TypeError, ValueError, AttributeError) as exc:
            raise BriefingError(f"{tool}: invalid or failed tool result; no brief generated") from exc
    return assemble_evidence(brief_id, account_id, as_of, records, audit.read(brief_id), statuses,
                             thread_snapshot, crm_backend=crm_backend, calls_backend=calls_backend,
                             calls_binding=calls_binding, slack_backend=slack_backend, crm_binding=crm_binding,
                             slack_binding=slack_binding, sellers=sellers)


def assemble_evidence(brief_id, account_id, as_of, records, receipts, statuses, thread_snapshot, *,
                      crm_backend, calls_backend, calls_binding, slack_backend, crm_binding, slack_binding, sellers):
    """Build one evidence snapshot and check it, including one matching receipt per read."""
    evidence = {"brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                "records": records, "receipts": receipts, **statuses,
                "crm_backend": crm_backend, "crm_binding": deepcopy(crm_binding),
                "calls_backend": calls_backend, "calls_binding": deepcopy(calls_binding),
                "slack_backend": slack_backend, "slack_binding": deepcopy(slack_binding),
                "thread_snapshot": thread_snapshot, "sellers": list(sellers),
                # Demo text names its own invented sellers. Where demo text was read they are an input to
                # roles, so the evidence holds them.
                "demo_sellers": list(SELLERS) if calls_backend == "fixture" or (
                    slack_backend == "fixture" and statuses["slack_status"] != "not_requested") else []}
    units, text, gaps = build(evidence)
    return evidence, units, text, gaps


def _artifact_path(directory, brief_id):
    if not isinstance(brief_id, str) or not re.fullmatch(r"brief-[0-9a-f]{32}", brief_id):
        raise ValueError("invalid brief ID")
    return Path(directory) / "briefs" / (brief_id + ".json")


# The receipt that seals a saved card. It names the digest of the saved bytes and belongs to no brief, so the
# receipts of the card's own reads stay exactly the ones in its evidence.
SEAL_TOOL = "save_brief"


def _save_artifact(path, payload):
    """Replace a complete snapshot atomically, without a power-loss guarantee."""
    if path.is_symlink():
        raise ValueError("brief artifact must not be a symbolic link")
    temporary = None
    try:
        with NamedTemporaryFile(mode="wb", dir=path.parent, prefix=f".{path.name}.",
                                suffix=".tmp", delete=False) as output:
            temporary = Path(output.name)
            os.fchmod(output.fileno(), 0o600)
            output.write(payload)
        os.replace(temporary, path)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def source_names(crm_backend, calls_backend, slack_backend, include_slack):
    """Which kind of source supplied each part of a card."""
    names = {"fixture": "demo_fixture", "sqlite": "demo_fixture", "none": "not_connected"}
    return {"crm": names.get(crm_backend, "salesforce"), "calls": names.get(calls_backend, "imported_transcript"),
            **({"slack": names.get(slack_backend, "slack")} if include_slack else {})}


def create_brief(runtime, account_id, *, include_calls=True, as_of=DEMO_AS_OF, include_slack=False,
                 grant_slack=True):
    """Collect, check, render, and save one card. No model takes part."""
    # Offline checks do not need to import the MCP server or TLS runtime.
    from .client import LocalClient

    brief_id = "brief-" + uuid.uuid4().hex
    scopes = ["crm:read"] + (["calls:read"] if include_calls else [])
    if include_slack and grant_slack:
        scopes.append("slack:read")
    try:
        with LocalClient(runtime.config) as client:
            token = client.authorize(scopes)
            evidence, units, text, gaps = collect_evidence(
                client, token, runtime.connector.audit, account_id, brief_id, as_of, include_slack=include_slack,
                crm_backend=runtime.crm_backend, calls_backend=runtime.calls_backend,
                calls_binding=runtime.calls_binding, slack_backend=runtime.slack_backend,
                crm_binding=runtime.crm_binding, slack_binding=runtime.slack_binding, sellers=runtime.sellers)
    except httpx2.HTTPError as exc:
        raise BriefingError("authorization transport failed; no brief generated") from exc
    except BriefRejected as exc:
        raise BriefRejected(f"brief {brief_id} rejected; no card published") from exc
    # The seal receipt is committed before the file exists and finished only after the file is whole, so a
    # card with no finished seal is never traced. A receipt left pending by a hard stop names its card.
    audit, started = runtime.connector.audit, time.monotonic()
    seal = audit.start(token=token, tool=SEAL_TOOL, arguments={"account_id": account_id, "brief_id": brief_id})
    path = _artifact_path(runtime.directory, brief_id)
    try:
        artifact = {"schema_version": 3, "brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                    "status": "accepted", "rules_version": SANITIZER_VERSION, "sealed_by": seal,
                    "sources": source_names(runtime.crm_backend, runtime.calls_backend, runtime.slack_backend,
                                            include_slack),
                    "text": text, "units": units, "gaps": gaps, "evidence": evidence}
        payload = json.dumps(artifact, ensure_ascii=False, allow_nan=False, indent=2).encode()
        if path.parent.is_symlink():
            raise ValueError("briefs directory must not be a symbolic link")
        path.parent.mkdir(mode=0o700, exist_ok=True)
        path.parent.chmod(0o700)
        _save_artifact(path, payload)
        audit.finish(seal, status="ok", http_status=200, elapsed_ms=(time.monotonic() - started) * 1000,
                     rows=[f"briefs/{brief_id}/{hashlib.sha256(payload).hexdigest()}"])
    except BaseException:
        try:
            path.unlink(missing_ok=True)  # A file whose seal did not finish is not a card.
            audit.finish(seal, status="error", http_status=500, rows=[], error="brief_save_failed",
                         elapsed_ms=(time.monotonic() - started) * 1000)
        except Exception:  # The save already failed; its own error is the one to report.
            pass
        raise
    return artifact


def _sealed(directory, brief_id):
    """A saved card, only when its bytes are the ones its seal receipt names.

    The receipt holds the digest of the whole file: the evidence, the units, the card text, the sellers, and
    the date. The owner of the folder can still rewrite the file and the audit log together; the log is not
    tamper-proof.
    """
    try:
        payload = _artifact_path(directory, brief_id).read_bytes()
    except FileNotFoundError:
        raise ValueError("No saved card has this brief ID in this state folder.") from None
    artifact = strict_json.loads(payload)
    evidence = artifact.get("evidence") if isinstance(artifact, dict) else None
    if isinstance(artifact, dict) and artifact.get("schema_version") == 2:
        raise ValueError("This card was saved before cards were sealed. Make the card again.")
    if (not isinstance(artifact, dict) or artifact.get("schema_version") != 3
            or artifact.get("brief_id") != brief_id or not isinstance(evidence, dict)
            or evidence.get("brief_id") != brief_id or artifact.get("account_id") != evidence.get("account_id")
            or not isinstance(artifact.get("units"), list) or not isinstance(artifact.get("text"), str)):
        raise ValueError("invalid saved brief evidence")
    from .audit import read_ids
    log = Path(directory) / "audit.sqlite3"
    reference = f"briefs/{brief_id}/{hashlib.sha256(payload).hexdigest()}"
    try:
        [seal] = read_ids(log, [str(artifact.get("sealed_by"))])
    except (ValueError, sqlite3.Error):
        raise ValueError("This card's seal receipt cannot be read from the audit log of this state folder. "
                         "A card traces only in the folder that made it.") from None
    if (seal["tool"], seal["status"]) == (SEAL_TOOL, "pending"):
        raise ValueError("This card's seal was never finished: saving it was interrupted. Make the card again.")
    if (seal["tool"], seal["status"], seal["rows"]) != (SEAL_TOOL, "ok", [reference]):
        raise ValueError("This file is not the card that was sealed: the card or its seal receipt was changed.")
    # The file is the sealed one, so its receipts are well formed. The log must still hold each of them. A
    # receipt that names this brief ID later is not one of the card's and does not stop it tracing.
    try:
        reads = read_ids(log, [row["audit_id"] for row in evidence["receipts"]])
    except ValueError:
        reads = None
    if reads != evidence["receipts"]:
        raise ValueError("The sealed card's read receipts are not the ones in the audit log of this state folder.")
    return artifact


def trace_saved(directory, brief_id, number):
    """Resolve one number on a saved card from its sealed units and evidence, never from the card's text.

    A card keeps the numbers it was sealed with. Nothing is rebuilt: a later rule fix, another Python, or a
    changed seller list would give other units for the same evidence, and the sealed ones are the card.
    """
    artifact = _sealed(directory, brief_id)
    units = artifact["units"]
    traced = trace(artifact["evidence"], number, units)
    # The next sentence under the same speaker label in the same record, where a condition often is. A sentence
    # with no label has none: no label is not one speaker.
    sealed, following = units[number - 1], None
    if sealed["kind"] == "sentence" and sealed["speaker"] is not None:
        following = next((unit["n"] for unit in units[number:] if unit["kind"] == "sentence"
                          and unit["record_ref"] == sealed["record_ref"] and unit["speaker"] == sealed["speaker"]),
                         None)
    return {"brief_id": brief_id, "account_id": artifact["account_id"], **traced, "next_by_speaker": following}


def units_saved(directory, brief_id):
    """Every unit of a saved card in number order, as sealed. Each must resolve as trace resolves it."""
    artifact = _sealed(directory, brief_id)
    for number in range(1, len(artifact["units"]) + 1):
        trace(artifact["evidence"], number, artifact["units"])
    return {"brief_id": brief_id, "account_id": artifact["account_id"],
            "units": [neutralized(unit) for unit in artifact["units"]]}


def newest_saved(directory, account_id):
    """The newest saved card of this account that the folder holds, by when it was sealed, or None.

    The seal receipts are in the order they were committed. The card itself is checked when it is inspected.
    """
    log = Path(directory) / "audit.sqlite3"
    if not os.path.lexists(log):
        return None
    from .audit import read_audit
    for seal in reversed(read_audit(log, tool=SEAL_TOOL, status="ok")):
        arguments = seal["arguments"]
        if isinstance(arguments, dict) and arguments.get("account_id") == account_id:
            try:
                if _artifact_path(directory, arguments.get("brief_id")).is_file():
                    return arguments["brief_id"]
            except ValueError:
                continue
    return None
