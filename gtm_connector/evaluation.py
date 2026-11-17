"""Measure the fixture workflow against a fixed, independent claim manifest.

A fact is one displayed statement, including compound contact/opportunity
statements. This is exact-text coverage and citation checking, not an LLM or
semantic-entailment benchmark. Expected text never comes from the draft code.
"""

from contextlib import closing
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import tempfile


EXPECTED_PATH = Path(__file__).resolve().parents[1] / "evaluation" / "expected.json"
_REFERENCES = re.compile(r"(?: \[[^\[\]\r\n]+\])+$")


def load_expectations(*, include_slack=False):
    """Load the checked-in twenty-account gold set, separate from source seeding."""
    expected = json.loads(EXPECTED_PATH.read_text())
    if include_slack:
        extension = expected["extensions"]["slack"]
        cases = extension["cases"]
        by_account = {case["account_id"]: case["claims"] for case in cases}
        if len(by_account) != len(cases) or set(by_account) != {
                case["account_id"] for case in expected["cases"]}:
            raise ValueError("Slack expectations must match every evaluation account once.")
        expected["cases"] = [{**case, "claims": case["claims"] + by_account[case["account_id"]]}
                             for case in expected["cases"]]
        expected["name"] = extension["name"]
        expected["scopes"] = expected["scopes"] + ["slack:read"]
        expected["workflow_version"] = 2
    return expected


def score_claims(candidate, expected):
    """Score each nonempty line without using the production citation checker.

    Coverage counts unique expected text, even when uncited or miscited.
    Citation coverage requires the exact reference set, with no repeated refs.
    Unsupported text counts as invented. Repetition never increases coverage.
    """
    known = {claim["text"]: claim["references"] for claim in expected}
    if len(known) != len(expected) or not known:
        raise ValueError("Expected claims must be nonempty and have unique text.")
    if not isinstance(candidate, str):
        raise ValueError("Candidate must be text.")
    found, cited = set(), set()
    invented = duplicates = emitted = 0
    for raw in candidate.splitlines():
        line = raw.strip()
        if not line:
            continue
        emitted += 1
        line = line.removeprefix("- ")
        suffix = _REFERENCES.search(line)
        references = re.findall(r"\[([^\[\]]+)\]", suffix.group()) if suffix else []
        text = line[:suffix.start()] if suffix else line
        if text not in known:
            invented += 1
            continue
        duplicates += text in found
        found.add(text)
        if len(references) == len(known[text]) and set(references) == set(known[text]):
            cited.add(text)
    return {
        "expected_facts": len(expected), "facts_found": len(found),
        "facts_cited_correctly": len(cited), "facts_invented": invented,
        "claims_emitted": emitted, "duplicate_claims": duplicates,
    }


def _resolve_references(case, artifact):
    """Only run/receipt IDs vary; source record IDs and expected text stay fixed."""
    substitutions = {"{brief_id}": artifact["brief_id"]}
    notes = [row for row in artifact["evidence"]["receipts"]
             if row["tool"] == "read_call_notes"]
    if len(notes) != 1:
        raise ValueError("Evaluation requires one call-notes receipt per brief.")
    substitutions["{read_call_notes_audit_id}"] = notes[0]["audit_id"]
    if artifact["evidence"].get("workflow_version", 1) == 2:
        slack = [row for row in artifact["evidence"]["receipts"]
                 if row["tool"] == "read_slack_messages"]
        if len(slack) != 1:
            raise ValueError("Evaluation requires one Slack receipt per extended brief.")
        substitutions["{read_slack_messages_audit_id}"] = slack[0]["audit_id"]
    claims = []
    for expected in case["claims"]:
        references = []
        for reference in expected["references"]:
            for placeholder, value in substitutions.items():
                reference = reference.replace(placeholder, value)
            references.append(reference)
        claims.append({"text": expected["text"], "references": references})
    return claims


def run_evaluation(*, adapter=None, command=None, include_slack=False):
    """Run twenty HTTPS briefs in disposable state, never in an operator's CRM."""
    from .briefing import create_brief
    from .citations import BriefRejected, diagnose_brief
    from .runtime import Runtime
    from . import store

    expected = load_expectations(include_slack=include_slack)
    expected_receipts = 80 if include_slack else 60
    started_at = datetime.now(timezone.utc).isoformat()
    model_run = adapter is not None
    cases = []
    with tempfile.TemporaryDirectory(prefix="gtm-evaluation-") as directory:
        with Runtime(directory) as runtime:
            for case in expected["cases"]:
                observed = {}

                def observe(result, evidence):
                    observed["model_usage"] = {
                        "model": result.model, "input_tokens": result.input_tokens,
                        "output_tokens": result.output_tokens}
                    if isinstance(result.text, str):
                        claims = _resolve_references(case, {"brief_id": evidence["brief_id"],
                                                            "evidence": evidence})
                        observed["candidate"] = {**score_claims(result.text, claims),
                                                 **diagnose_brief(result.text, evidence)}
                    else:
                        observed["candidate"] = None
                    if result.error_code:
                        observed["draft_error"] = result.error_code

                try:
                    artifact = create_brief(runtime, case["account_id"], as_of=expected["as_of"],
                                            adapter=adapter, observe_draft=observe if model_run else None,
                                            include_slack=include_slack)
                    claims = _resolve_references(case, artifact)
                    scored = score_claims(artifact["text"], claims)
                    cases.append({"account_id": case["account_id"], "status": "accepted", **scored})
                except (ValueError, RuntimeError, OSError) as exc:
                    # A rejected/failed brief has no published text. Its expected
                    # facts remain in the denominator rather than being omitted.
                    scored = score_claims("", case["claims"])
                    cases.append({"account_id": case["account_id"],
                                  "status": "rejected" if isinstance(exc, BriefRejected) else "failed",
                                  "error": type(exc).__name__, **scored})
                if model_run:
                    cases[-1].update({"candidate": None,
                                      "model_usage": {"model": None, "input_tokens": None,
                                                      "output_tokens": None}}, **observed)
            receipts = runtime.connector.audit.read()
            with closing(store.connect(runtime.directory / "crm.sqlite3", readonly=True)) as connection:
                activities = connection.execute("SELECT count(*) FROM activities").fetchone()[0]

    sums = {key: sum(case[key] for case in cases) for key in (
        "expected_facts", "facts_found", "facts_cited_correctly", "facts_invented",
        "claims_emitted", "duplicate_claims")}
    outcomes = {status: sum(case["status"] == status for case in cases)
                for status in ("accepted", "rejected", "failed")}
    passed = (outcomes["accepted"] == 20
              and sums["facts_found"] == sums["facts_cited_correctly"] == sums["expected_facts"]
              and sums["facts_invented"] == sums["duplicate_claims"] == activities == 0
              and len(receipts) == expected_receipts and all(row["status"] == "ok" for row in receipts))
    report = {
        "status": "passed" if passed else "failed", "evaluation": expected["name"],
        "as_of": expected["as_of"], "accounts": len(cases),
        "claim_unit": "one displayed statement; compound statements count once",
        **sums,
        "fact_recall": sums["facts_found"] / sums["expected_facts"],
        "citation_recall": sums["facts_cited_correctly"] / sums["expected_facts"],
        "invention_rate": (sums["facts_invented"] / sums["claims_emitted"]
                           if sums["claims_emitted"] else None),
        "accepted_briefs": outcomes["accepted"], "rejected_briefs": outcomes["rejected"],
        "failed_briefs": outcomes["failed"], "audit_rows": len(receipts),
        "activities_created": activities,
        "model_usage": {"model": None, "input_tokens": 0, "output_tokens": 0},
        "cases": cases,
    }
    if model_run:
        candidates = [case["candidate"] for case in cases if case["candidate"] is not None]
        candidate_keys = ("facts_found", "facts_cited_correctly", "facts_invented",
                          "claims_emitted", "duplicate_claims", "rejected_lines",
                          "missing_required_claims")
        reasons = {key: sum(candidate["reason_counts"][key] for candidate in candidates)
                   for key in ("uncited", "unsupported_or_miscited", "duplicate",
                               "missing_required", "empty")}
        models = sorted({case["model_usage"]["model"] for case in cases
                         if case["model_usage"]["model"] is not None})
        usage = {"model": models[0] if len(models) == 1 else None}
        for field in ("input_tokens", "output_tokens"):
            counts = [case["model_usage"][field] for case in cases]
            usage[field] = None if any(value is None for value in counts) else sum(counts)
        report.update(
            adapter=getattr(adapter, "name", "custom"),
            model_metadata=getattr(adapter, "metadata", {}), returned_models=models,
            candidate={"scored_briefs": len(candidates), "unscored_briefs": len(cases) - len(candidates),
                       **{key: sum(candidate[key] for candidate in candidates) for key in candidate_keys},
                       "reason_counts": reasons},
            model_usage=usage,
            publication_gate_passed=(outcomes["failed"] == 0 and sums["facts_invented"] == 0
                                     and sums["facts_found"] == sums["facts_cited_correctly"]
                                     and sums["duplicate_claims"] == activities == 0
                                     and len(receipts) == expected_receipts
                                     and all(row["status"] == "ok" for row in receipts)))
    if include_slack:
        report.update(workflow="crm-calls-slack", workflow_version=2)
    report["run"] = {"started_at": started_at,
                     "completed_at": datetime.now(timezone.utc).isoformat(), "command": command}
    return report
