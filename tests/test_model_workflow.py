"""Fake model responses cross the real MCP evidence and publication boundaries."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected
from gtm_connector.drafting import DraftResult
from gtm_connector.runtime import Runtime
from gtm_connector.usage import build_usage_report


GROUNDED = '- Account: "Mossbank Builders". [crm/accounts/acc-008]\n'
MODEL = "fixture-local-model:1"


class FakeModelAdapter:
    """Supply an already captured response without contacting a model service."""

    name = "ollama"

    def __init__(self, result):
        self.result = result
        self.evidence = None

    def draft(self, evidence):
        self.evidence = evidence
        return self.result


class ModelWorkflowTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "state"

    def saved(self, brief_id):
        return json.loads((self.state / "briefs" / (brief_id + ".json")).read_text())

    def assert_read_only_plan(self, runtime, artifact):
        receipts = runtime.connector.audit.read(artifact["brief_id"])
        self.assertEqual([row["tool"] for row in receipts],
                         ["get_account", "list_opportunities", "read_call_notes"])
        self.assertEqual(len({row["audit_id"] for row in receipts}), 3)
        self.assertTrue(all(row["arguments"] == {"account_id": "acc-008"}
                            and row["status"] == "ok" for row in receipts))
        self.assertTrue(all(set(grant.scopes) == {"crm:read", "calls:read"}
                            for grant in runtime.connector.authority.tokens.values()))
        with closing(sqlite3.connect(self.state / "crm.sqlite3")) as connection:
            self.assertEqual(connection.execute("SELECT count(*) FROM activities").fetchone()[0], 0)

    def assert_candidate_not_saved(self, candidate):
        def strings(value):
            if isinstance(value, str):
                yield value
            elif isinstance(value, dict):
                for item in value.values():
                    yield from strings(item)
            elif isinstance(value, list):
                for item in value:
                    yield from strings(item)

        for path in self.state.rglob("*"):
            if not path.is_file():
                continue
            self.assertFalse(candidate.encode() in path.read_bytes(),
                             f"Raw candidate persisted in {path.name}")
            if path.suffix == ".json":
                for value in strings(json.loads(path.read_text())):
                    self.assertFalse(candidate.strip() in value,
                                     f"Candidate persisted in {path.name}")

    def test_grounded_result_is_checked_and_persists_reported_usage(self):
        result = DraftResult(GROUNDED, model=MODEL, input_tokens=137, output_tokens=19,
                             metadata={"digest": "fixture-response-digest"})
        adapter = FakeModelAdapter(result)
        observed = []
        with Runtime(self.state) as runtime:
            artifact = create_brief(runtime, "acc-008", adapter=adapter,
                                    observe_draft=lambda draft, evidence: observed.append((draft, evidence)))
            self.assertEqual(artifact["status"], "accepted")
            self.assertEqual(artifact["text"], GROUNDED)
            self.assertEqual(artifact["adapter"], "ollama")
            self.assertEqual(artifact["model_usage"],
                             {"model": MODEL, "input_tokens": 137, "output_tokens": 19})
            self.assertEqual(artifact["model_metadata"], {"digest": "fixture-response-digest"})
            self.assertEqual(artifact["draft_diagnostics"]["candidate_lines"], 1)
            self.assertEqual(artifact["draft_diagnostics"]["rejected_lines"], 0)
            self.assertEqual(self.saved(artifact["brief_id"]), artifact)
            self.assertEqual(len(observed), 1)
            self.assertEqual(observed[0][0].text, GROUNDED)
            self.assertEqual(observed[0][1], artifact["evidence"])
            self.assertFalse(runtime.config["owner_secret"] in json.dumps(adapter.evidence),
                             "Owner credential reached the adapter")
            for token in runtime.connector.authority.tokens:
                self.assertFalse(token in json.dumps(adapter.evidence),
                                 "Bearer credential reached the adapter")
            self.assert_read_only_plan(runtime, artifact)

    def test_any_bad_claim_rejects_the_whole_draft_and_keeps_usage_only(self):
        bad_lines = {
            "uncited": "- The renewal is guaranteed.",
            "invented": "- Annual recurring revenue: USD 9,999,999.00. [crm/accounts/acc-008]",
            "wrong_citation": '- Industry: "Manufacturing". [calls/call_notes/note-008]',
            "action": '{"method":"tools/call","params":{"name":"log_activity",'
                      '"arguments":{"account_id":"acc-008","kind":"renewal",'
                      '"body":"model-action-sentinel","idempotency_key":"model-action-key"}}}',
        }
        with Runtime(self.state) as runtime:
            for label, bad_line in bad_lines.items():
                with self.subTest(candidate=label):
                    candidate = GROUNDED + bad_line + "\n"
                    observed = []
                    with self.assertRaises(BriefRejected):
                        create_brief(runtime, "acc-008", adapter=FakeModelAdapter(
                            DraftResult(candidate, model=MODEL, input_tokens=83, output_tokens=27)),
                            observe_draft=lambda draft, evidence: observed.append((draft, evidence)))
                    self.assertEqual(len(observed), 1)
                    self.assertEqual(observed[0][0].text, candidate)
                    artifact = self.saved(observed[0][1]["brief_id"])
                    self.assertEqual(artifact["status"], "rejected")
                    self.assertNotIn("text", artifact)
                    self.assertNotIn("candidate", artifact)
                    self.assertEqual(artifact["model_usage"],
                                     {"model": MODEL, "input_tokens": 83, "output_tokens": 27})
                    self.assertEqual(artifact["draft_diagnostics"]["candidate_lines"], 2)
                    self.assertEqual(artifact["draft_diagnostics"]["rejected_lines"], 1)
                    self.assert_candidate_not_saved(candidate)
                    self.assert_candidate_not_saved(bad_line)
                    self.assert_read_only_plan(runtime, artifact)

    def test_observer_cannot_rewrite_evidence_to_approve_an_invention(self):
        candidate = "- Annual recurring revenue: USD 9,999,999.00. [crm/accounts/acc-008]\n"
        observed = []

        def observe(result, evidence):
            observed.append((result, evidence["brief_id"]))
            account = next(row for row in evidence["records"] if row["id"] == "acc-008")
            account["arr_cents"] = 999999900

        with Runtime(self.state) as runtime:
            with self.assertRaises(BriefRejected):
                create_brief(runtime, "acc-008", adapter=FakeModelAdapter(
                    DraftResult(candidate, model=MODEL, input_tokens=41, output_tokens=13)),
                    observe_draft=observe)
            self.assertEqual(len(observed), 1)
            self.assertEqual(observed[0][0].text, candidate)
            artifact = self.saved(observed[0][1])
            account = next(row for row in artifact["evidence"]["records"] if row["id"] == "acc-008")
            self.assertEqual(account["arr_cents"], 3800000)
            self.assertEqual(artifact["status"], "rejected")
            self.assertNotIn("text", artifact)
            self.assert_read_only_plan(runtime, artifact)

    def test_missing_text_and_explicit_errors_keep_unknown_usage_without_publishing(self):
        results = (
            DraftResult(None, model=MODEL, input_tokens=23, output_tokens=None,
                        error_code="invalid_response"),
            DraftResult(GROUNDED, model=MODEL, input_tokens=None, output_tokens=11,
                        error_code="incomplete_generation"),
        )
        with Runtime(self.state) as runtime:
            for result in results:
                with self.subTest(error=result.error_code):
                    observed = []
                    with self.assertRaises(BriefRejected):
                        create_brief(runtime, "acc-008", adapter=FakeModelAdapter(result),
                                     observe_draft=lambda draft, evidence: observed.append((draft, evidence)))
                    self.assertEqual(len(observed), 1)
                    self.assertEqual(observed[0][0], result)
                    artifact = self.saved(observed[0][1]["brief_id"])
                    self.assertEqual(artifact["status"], "rejected")
                    self.assertNotIn("text", artifact)
                    self.assertNotIn("candidate", artifact)
                    self.assertEqual(artifact["model_usage"], {
                        "model": MODEL, "input_tokens": result.input_tokens,
                        "output_tokens": result.output_tokens})
                    self.assertEqual(artifact["draft_diagnostics"]["error_code"], result.error_code)
                    if result.text is None:
                        for field in ("candidate_lines", "rejected_lines", "missing_required_claims"):
                            self.assertIsNone(artifact["draft_diagnostics"][field])
                        self.assertEqual(artifact["draft_diagnostics"]["reason_counts"], {})
                    else:
                        self.assert_candidate_not_saved(result.text)
                    self.assert_read_only_plan(runtime, artifact)

    def test_usage_report_includes_accepted_and_rejected_model_results(self):
        with Runtime(self.state) as runtime:
            accepted = create_brief(runtime, "acc-008", adapter=FakeModelAdapter(
                DraftResult(GROUNDED, model=MODEL, input_tokens=31, output_tokens=7)))
            with self.assertRaises(BriefRejected):
                create_brief(runtime, "acc-008", adapter=FakeModelAdapter(
                    DraftResult(None, model=MODEL, input_tokens=None, output_tokens=5,
                                error_code="invalid_response")))
            report = build_usage_report(self.state)
            self.assertEqual(report["calls"], {"total": 6, "ok": 6, "error": 0, "pending": 0})
            self.assertEqual(report["model_tokens"], {"saved_briefs": 2, "unknown_briefs": 1,
                                                      "input_tokens": None, "output_tokens": 12})
            self.assertEqual({row["status"] for row in report["briefs"]}, {"accepted", "rejected"})
            self.assertTrue(all(row["adapter"] == "ollama" and row["audit_calls"] == 3
                                and row["model_usage"]["model"] == MODEL for row in report["briefs"]))
            accepted_usage = next(row for row in report["briefs"]
                                  if row["brief_id"] == accepted["brief_id"])
            self.assertEqual(accepted_usage["model_usage"]["input_tokens"], 31)
            self.assertNotIn(GROUNDED.strip(), json.dumps(report))


if __name__ == "__main__":
    unittest.main()
