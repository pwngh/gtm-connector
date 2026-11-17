"""Measure the fixture workflow against a fixed, independent claim manifest.

A fact is one displayed statement, including compound contact/opportunity
statements. This is exact-text coverage and citation checking, not an LLM or
semantic-entailment benchmark. Expected text never comes from the draft code.
"""

from contextlib import closing
import json
from pathlib import Path
import re
import tempfile


EXPECTED_PATH = Path(__file__).resolve().parents[1] / "evaluation" / "expected.json"
_REFERENCES = re.compile(r"(?: \[[^\[\]\r\n]+\])+$")


def load_expectations():
    """Load the checked-in twenty-account gold set, separate from source seeding."""
    return json.loads(EXPECTED_PATH.read_text())


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
    claims = []
    for expected in case["claims"]:
        references = []
        for reference in expected["references"]:
            for placeholder, value in substitutions.items():
                reference = reference.replace(placeholder, value)
            references.append(reference)
        claims.append({"text": expected["text"], "references": references})
    return claims


def run_evaluation():
    """Run twenty HTTPS briefs in disposable state, never in an operator's CRM."""
    from .briefing import create_brief
    from .citations import BriefRejected
    from .runtime import Runtime
    from . import store

    expected = load_expectations()
    cases = []
    with tempfile.TemporaryDirectory(prefix="gtm-evaluation-") as directory:
        with Runtime(directory) as runtime:
            for case in expected["cases"]:
                try:
                    artifact = create_brief(runtime, case["account_id"], as_of=expected["as_of"])
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
              and len(receipts) == 60 and all(row["status"] == "ok" for row in receipts))
    return {
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
