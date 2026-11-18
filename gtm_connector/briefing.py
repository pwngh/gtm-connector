"""A fixed read-only MCP workflow with an untrusted draft and a citation gate."""

from copy import deepcopy
from datetime import date
import json
import os
from pathlib import Path
import re
from tempfile import NamedTemporaryFile
import uuid

import httpx2

from .citations import BriefRejected, NoRenewalCandidate, build_facts, check_brief, diagnose_brief
from .drafting import DraftResult, FixtureDraftAdapter
from .seed import DEMO_AS_OF


class BriefingError(RuntimeError):
    """Required evidence could not be collected; no brief can be published."""


class RenewalFieldsUnmapped(BriefingError):
    """The account has no standard Contract date suitable for renewal planning."""

    code = "renewal_fields_unmapped"


def collect_evidence(client, token, audit, account_id, brief_id, as_of=DEMO_AS_OF,
                     *, include_slack=False, crm_backend="sqlite"):
    """Read through MCP; recognized calls/Slack scope denials may remain visible."""
    if crm_backend not in ("sqlite", "salesforce"):
        raise ValueError("unsupported CRM backend")
    pattern = r"acc-sf-[A-Za-z0-9]{18}" if crm_backend == "salesforce" else r"acc-[0-9]{3}"
    if not isinstance(account_id, str) or not re.fullmatch(pattern, account_id):
        raise ValueError("account ID must use acc-sf- followed by the 18-character Salesforce ID"
                         if crm_backend == "salesforce" else "account ID must have the form acc-001")
    if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
        raise ValueError("as-of date must use YYYY-MM-DD")
    records, statuses = [], {"notes_status": "available"}
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
            records.extend(rows)
        except (KeyError, TypeError, ValueError, AttributeError) as exc:
            raise BriefingError(f"{tool}: invalid or failed tool result; no brief generated") from exc
    evidence = {"brief_id": brief_id, "account_id": account_id, "as_of": as_of,
                "records": records, "receipts": audit.read(brief_id), **statuses}
    if crm_backend == "salesforce":
        evidence.update(workflow_version=3, crm_backend="salesforce")
    elif include_slack:
        evidence["workflow_version"] = 2
    # This also verifies account associations and one matching receipt per read.
    try:
        build_facts(evidence)
    except NoRenewalCandidate as exc:
        raise RenewalFieldsUnmapped(str(exc)) from None
    return evidence


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


def create_brief(runtime, account_id, *, include_calls=True, as_of=DEMO_AS_OF, adapter=None,
                 observe_draft=None, include_slack=False, grant_slack=True):
    """Collect, draft, check, then publish; the draft adapter receives data only."""
    crm_backend = getattr(runtime, "crm_backend", "sqlite")
    # Offline citation checks do not need to import the MCP server or TLS runtime.
    from .client import LocalClient

    brief_id = "brief-" + uuid.uuid4().hex
    scopes = ["crm:read"] + (["calls:read"] if include_calls else [])
    if include_slack and grant_slack:
        scopes.append("slack:read")
    try:
        with LocalClient(runtime.config) as client:
            token = client.authorize(scopes)
            evidence = collect_evidence(client, token, runtime.connector.audit, account_id, brief_id,
                                        as_of, include_slack=include_slack, crm_backend=crm_backend)
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
        _save_artifact(path, artifact)

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
        if include_slack:
            follow_ups = [line for line in checked.splitlines()
                          if line.startswith("- Proposed follow-up:")]
            if len(follow_ups) != 1:
                raise BriefRejected("The Slack workflow requires one checked proposed follow-up.")
    except Exception as exc:
        artifact["status"] = "rejected"
        save()
        raise BriefRejected(f"brief {brief_id} rejected; no draft published") from exc
    artifact.update(status="accepted", text=checked)
    if include_slack:
        artifact["proposed_follow_up"] = follow_ups[0]
    save()
    return artifact


def check_saved_brief(directory, brief_id, candidate):
    """Check candidate text against the stored MCP snapshot, never its own citations alone."""
    path = _artifact_path(directory, brief_id)
    artifact = json.loads(path.read_text())
    if (not isinstance(artifact, dict) or artifact.get("schema_version") != 1
            or artifact.get("brief_id") != brief_id
            or not isinstance(artifact.get("evidence"), dict)
            or artifact["evidence"].get("brief_id") != brief_id):
        raise ValueError("invalid saved brief evidence")
    evidence = artifact["evidence"]
    try:
        return check_brief(candidate, evidence)
    except BriefRejected:
        if (evidence.get("workflow_version", 1) not in (1, 2)
                or not isinstance(candidate, str) or 'role "";' not in candidate):
            raise
        # Older fixture briefs quoted an empty role as "". Check one whole
        # vocabulary at a time so aliases cannot admit duplicate claims.
        return check_brief(candidate, evidence, _legacy_empty_roles=True)
