"""A fixed read-only MCP workflow with an untrusted draft and a citation gate."""

from copy import deepcopy
from datetime import date
import json
from pathlib import Path
import re
import uuid

import httpx2

from .citations import BriefRejected, build_facts, check_brief, diagnose_brief
from .drafting import DraftResult, FixtureDraftAdapter
from .seed import DEMO_AS_OF


class BriefingError(RuntimeError):
    """Required evidence could not be collected; no brief can be published."""


def collect_evidence(client, token, audit, account_id, brief_id, as_of=DEMO_AS_OF):
    """Read through MCP only; a scoped notes denial is the sole partial-success case."""
    if not isinstance(account_id, str) or not re.fullmatch(r"acc-[0-9]{3}", account_id):
        raise ValueError("account ID must have the form acc-001")
    if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
        raise ValueError("as-of date must use YYYY-MM-DD")
    records, notes_status = [], "available"
    for tool, source in (("get_account", "crm"), ("list_opportunities", "crm"),
                         ("read_call_notes", "calls")):
        try:
            response = client.call(token, tool, {"account_id": account_id}, brief_id=brief_id)
        except httpx2.HTTPError as exc:
            raise BriefingError(f"{tool}: transport failed; no brief generated") from exc
        if tool == "read_call_notes" and response.status_code == 403:
            challenge = response.headers.get("www-authenticate", "")
            if 'error="insufficient_scope"' in challenge and 'scope="calls:read"' in challenge:
                notes_status = "denied"
                continue
        if response.status_code != 200:
            raise BriefingError(f"{tool}: HTTP {response.status_code}; no brief generated")
        try:
            result = response.json()["result"]
            if result.get("isError") or result.get("resultType") != "complete":
                raise ValueError("incomplete or failed tool result")
            data = result["structuredContent"]
            if data["source"] != source or data.get("untrusted_data") is not True:
                raise ValueError("unexpected source result")
            rows = data["records"]
            if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
                raise ValueError("invalid record collection")
            records.extend(rows)
        except (KeyError, TypeError, ValueError, AttributeError) as exc:
            raise BriefingError(f"{tool}: invalid or failed tool result; no brief generated") from exc
    evidence = {"brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                "records": records, "receipts": audit.read(brief_id), "notes_status": notes_status}
    # This also verifies account associations and one matching receipt per read.
    build_facts(evidence)
    return evidence


def _artifact_path(directory, brief_id):
    if not isinstance(brief_id, str) or not re.fullmatch(r"brief-[0-9a-f]{32}", brief_id):
        raise ValueError("invalid brief ID")
    return Path(directory) / "briefs" / (brief_id + ".json")


def create_brief(runtime, account_id, *, include_calls=True, as_of=DEMO_AS_OF, adapter=None,
                 observe_draft=None):
    """Collect, draft, check, then publish; the draft adapter receives data only."""
    # Offline citation checks do not need to import the MCP server or TLS runtime.
    from .client import LocalClient
    from .runtime import private_file

    brief_id = "brief-" + uuid.uuid4().hex
    scopes = ["crm:read"] + (["calls:read"] if include_calls else [])
    try:
        with LocalClient(runtime.config) as client:
            token = client.authorize(scopes)
            evidence = collect_evidence(client, token, runtime.connector.audit, account_id, brief_id, as_of)
    except httpx2.HTTPError as exc:
        raise BriefingError("authorization transport failed; no brief generated") from exc
    adapter = FixtureDraftAdapter() if adapter is None else adapter
    fixture = type(adapter) is FixtureDraftAdapter
    artifact = {"schema_version": 1, "brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                "adapter": "fixture" if fixture else
                           "ollama" if getattr(adapter, "name", None) == "ollama" else "custom",
                "model_usage": {"model": None, "input_tokens": 0 if fixture else None,
                                "output_tokens": 0 if fixture else None},
                "status": "collected", "evidence": evidence}
    path = _artifact_path(runtime.directory, brief_id)
    if path.parent.is_symlink():
        raise ValueError("briefs directory must not be a symbolic link")
    path.parent.mkdir(mode=0o700, exist_ok=True)
    path.parent.chmod(0o700)

    def save():
        private_file(path, json.dumps(artifact, ensure_ascii=False, allow_nan=False, indent=2).encode())

    save()
    try:
        # Candidate text and mutations to this copy cannot change the trusted snapshot.
        candidate = adapter.draft(deepcopy(evidence))
        result = candidate if isinstance(candidate, DraftResult) else DraftResult(
            candidate, input_tokens=0 if fixture else None, output_tokens=0 if fixture else None)
        if (result.model is not None and not isinstance(result.model, str)) or any(
                value is not None and (type(value) is not int or value < 0)
                for value in (result.input_tokens, result.output_tokens)):
            raise BriefRejected("Draft adapter returned invalid usage.")
        artifact["model_usage"] = {"model": result.model, "input_tokens": result.input_tokens,
                                   "output_tokens": result.output_tokens}
        if isinstance(candidate, DraftResult):
            artifact["model_metadata"] = result.metadata
            artifact["draft_diagnostics"] = (
                diagnose_brief(result.text, evidence) if isinstance(result.text, str) else
                {"candidate_lines": None, "rejected_lines": None,
                 "missing_required_claims": None, "reason_counts": {}})
            if result.error_code:
                artifact["draft_diagnostics"]["error_code"] = result.error_code
        if observe_draft is not None:
            # Trusted evaluation code may inspect the raw candidate in memory.
            # Its copy cannot replace evidence used by the publication gate.
            observe_draft(result, deepcopy(evidence))
        if result.error_code:
            raise BriefRejected("Draft adapter did not return a complete candidate.")
        candidate = result.text
        checked = check_brief(candidate, evidence)
    except Exception as exc:
        artifact["status"] = "rejected"
        save()
        raise BriefRejected(f"brief {brief_id} rejected; no draft published") from exc
    artifact.update(status="accepted", text=checked)
    save()
    return artifact


def check_saved_brief(directory, brief_id, candidate):
    """Check candidate text against the stored MCP snapshot, never its own citations alone."""
    path = _artifact_path(directory, brief_id)
    artifact = json.loads(path.read_text())
    if (artifact.get("schema_version") != 1 or artifact.get("brief_id") != brief_id
            or artifact.get("evidence", {}).get("brief_id") != brief_id):
        raise ValueError("invalid saved brief evidence")
    return check_brief(candidate, artifact["evidence"])
