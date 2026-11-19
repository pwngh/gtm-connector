"""Read one account through the audited tools and render its Deal card."""

from copy import deepcopy
from datetime import date
import json
import os
from pathlib import Path
import re
from tempfile import NamedTemporaryFile
import uuid

import httpx2

from . import strict_json
from .seed import DEMO_AS_OF
from .units import BriefRejected, build, trace


class BriefingError(RuntimeError):
    """Required evidence could not be collected; no brief can be published."""


def collect_evidence(client, token, audit, account_id, brief_id, as_of=DEMO_AS_OF,
                     *, include_slack=False, crm_backend="sqlite", calls_backend="fixture", calls_binding=None,
                     slack_backend="fixture", crm_binding=None, slack_binding=None, sellers=()):
    """Read through MCP and return the checked evidence, its units, and the card.

    A calls or Slack scope denial is kept as an outcome the card states.
    """
    if crm_backend not in ("sqlite", "salesforce"):
        raise ValueError("unsupported CRM backend")
    if calls_backend not in ("fixture", "imported"):
        raise ValueError("unsupported calls backend")
    if slack_backend not in ("fixture", "slack"):
        raise ValueError("unsupported Slack backend")
    pattern = r"acc-sf-[A-Za-z0-9]{18}" if crm_backend == "salesforce" else r"acc-[0-9]{3}"
    if not isinstance(account_id, str) or not re.fullmatch(pattern, account_id):
        raise ValueError("account ID must use acc-sf- followed by the 18-character Salesforce ID"
                         if crm_backend == "salesforce" else "account ID must have the form acc-001")
    if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
        raise ValueError("as-of date must use YYYY-MM-DD")
    records, statuses = [], {"notes_status": "available", "slack_status": "not_requested"}
    thread_snapshot = None
    read_plan = [("get_account", "crm"), ("list_opportunities", "crm"),
                 ("read_call_notes", "calls")]
    optional_scopes = {"read_call_notes": ("notes_status", "calls:read")}
    if include_slack:
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
                "thread_snapshot": thread_snapshot, "sellers": list(sellers)}
    units, text = build(evidence)
    return evidence, units, text


def _artifact_path(directory, brief_id):
    if not isinstance(brief_id, str) or not re.fullmatch(r"brief-[0-9a-f]{32}", brief_id):
        raise ValueError("invalid brief ID")
    return Path(directory) / "briefs" / (brief_id + ".json")


def _save_artifact(path, artifact):
    """Replace a complete snapshot atomically, without a power-loss guarantee."""
    payload = json.dumps(artifact, ensure_ascii=False, allow_nan=False, indent=2).encode()
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
    return {"crm": "salesforce" if crm_backend == "salesforce" else "demo_fixture",
            "calls": "imported_transcript" if calls_backend == "imported" else "demo_fixture",
            **({"slack": "slack" if slack_backend == "slack" else "demo_fixture"} if include_slack else {})}


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
            evidence, units, text = collect_evidence(
                client, token, runtime.connector.audit, account_id, brief_id, as_of, include_slack=include_slack,
                crm_backend=runtime.crm_backend, calls_backend=runtime.calls_backend,
                calls_binding=runtime.calls_binding, slack_backend=runtime.slack_backend,
                crm_binding=runtime.crm_binding, slack_binding=runtime.slack_binding, sellers=runtime.sellers)
    except httpx2.HTTPError as exc:
        raise BriefingError("authorization transport failed; no brief generated") from exc
    except BriefRejected as exc:
        raise BriefRejected(f"brief {brief_id} rejected; no card published") from exc
    artifact = {"schema_version": 2, "brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                "status": "accepted",
                "sources": source_names(runtime.crm_backend, runtime.calls_backend, runtime.slack_backend,
                                        include_slack),
                "text": text, "units": units, "evidence": evidence}
    path = _artifact_path(runtime.directory, brief_id)
    if path.parent.is_symlink():
        raise ValueError("briefs directory must not be a symbolic link")
    path.parent.mkdir(mode=0o700, exist_ok=True)
    path.parent.chmod(0o700)
    _save_artifact(path, artifact)
    return artifact


def trace_saved(directory, brief_id, number):
    """Resolve one number on a saved card from its stored evidence, never from the card's own text.

    The saved receipts must equal the ones committed to the audit log for this brief, so an edited
    file does not trace. The file's owner can still edit both; the audit log is not tamper-proof.
    """
    try:
        artifact = strict_json.loads(_artifact_path(directory, brief_id).read_text())
    except FileNotFoundError:
        raise ValueError("No saved card has this brief ID in this state folder.") from None
    if (not isinstance(artifact, dict) or artifact.get("schema_version") != 2
            or artifact.get("brief_id") != brief_id or not isinstance(artifact.get("evidence"), dict)
            or artifact["evidence"].get("brief_id") != brief_id
            or artifact.get("account_id") != artifact["evidence"].get("account_id")):
        raise ValueError("invalid saved brief evidence")
    from .audit import read_audit
    if read_audit(Path(directory) / "audit.sqlite3", brief_id) != artifact["evidence"].get("receipts"):
        raise ValueError("The saved card does not match the audit log of this state folder.")
    if artifact.get("units") != build(artifact["evidence"])[0]:
        raise ValueError("The saved card was made under other rules for sentences, or was changed. Make it again.")
    return {"brief_id": brief_id, "account_id": artifact["account_id"], **trace(artifact["evidence"], number)}
