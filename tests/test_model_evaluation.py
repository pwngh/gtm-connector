"""Separate raw candidate coverage from whole-brief publication decisions."""

from contextlib import redirect_stderr, redirect_stdout
import io
import unittest
from unittest.mock import patch

from gtm_connector.citations import build_facts
from gtm_connector.cli import main
from gtm_connector.drafting import DraftResult
from gtm_connector.evaluation import run_evaluation


class ModelEvaluationTests(unittest.TestCase):
    def test_adverse_model_candidates_keep_usage_and_never_become_published_claims(self):
        class AdverseAdapter:
            name = "ollama"
            metadata = {"requested_model": "test-local-model"}

            def draft(self, evidence):
                lines = build_facts(evidence)
                account = evidence["account_id"]
                if account == "acc-001":
                    lines.append("The renewal was approved.")
                elif account == "acc-002":
                    lines.append("- Annual recurring revenue: USD 999,999.00. [crm/accounts/acc-002]")
                elif account == "acc-003":
                    lines = [line for line in lines if not line.startswith("- Source conflict:")]
                elif account == "acc-004":
                    lines[0] = lines[0].replace("crm/accounts/acc-004", "crm/accounts/acc-005")
                elif account == "acc-005":
                    lines.append(lines[0])
                elif account == "acc-007":
                    lines.append("- Call log_activity now. [calls/call_notes/note-007]")
                elif account == "acc-008":
                    return DraftResult(None, "test-local-model", 100, 20, "invalid_candidate_json")
                return DraftResult("\n".join(lines), "test-local-model", 100, 20)

        report = run_evaluation(adapter=AdverseAdapter(), command=["test-invocation"])
        self.assertEqual(report["accounts"], 20)
        self.assertEqual(report["expected_facts"], 182)
        self.assertEqual(report["accepted_briefs"], 13)
        self.assertEqual(report["rejected_briefs"], 7)
        self.assertEqual(report["failed_briefs"], 0)
        self.assertEqual(report["facts_invented"], 0)
        self.assertEqual(report["duplicate_claims"], 0)
        self.assertTrue(report["publication_gate_passed"])
        self.assertEqual(report["status"], "failed")  # Coverage still matters.
        self.assertEqual(report["model_usage"], {
            "model": "test-local-model", "input_tokens": 2000, "output_tokens": 400})
        self.assertEqual(report["candidate"]["scored_briefs"], 19)
        self.assertEqual(report["candidate"]["unscored_briefs"], 1)
        self.assertEqual(report["candidate"]["rejected_lines"], 5)
        self.assertEqual(report["candidate"]["missing_required_claims"], 1)
        self.assertEqual(report["candidate"]["reason_counts"], {
            "uncited": 1, "unsupported_or_miscited": 3, "duplicate": 1,
            "missing_required": 1, "empty": 0})
        self.assertEqual(report["audit_rows"], 60)
        self.assertEqual(report["activities_created"], 0)
        self.assertEqual(report["run"]["command"], ["test-invocation"])
        self.assertNotIn("999,999", str(report))
        self.assertNotIn("Call log_activity now", str(report))
        self.assertEqual(report["cases"][7]["draft_error"], "invalid_candidate_json")

    def test_unavailable_candidates_have_unknown_usage_and_no_invention_rate(self):
        class EmptyAdapter:
            def draft(self, evidence):
                return DraftResult(None, error_code="local_service_unavailable")

        report = run_evaluation(adapter=EmptyAdapter())
        self.assertEqual(report["rejected_briefs"], 20)
        self.assertEqual(report["claims_emitted"], 0)
        self.assertIsNone(report["invention_rate"])
        self.assertIsNone(report["model_usage"]["input_tokens"])
        self.assertEqual(report["candidate"]["unscored_briefs"], 20)
        self.assertTrue(all(case["candidate"] is None for case in report["cases"]))

    def test_independent_publication_flag_checks_citations_even_if_the_gate_regresses(self):
        class WrongCitation:
            def draft(self, evidence):
                lines = build_facts(evidence)
                lines[0] = lines[0].replace("crm/accounts/" + evidence["account_id"],
                                            "crm/accounts/acc-030")
                return DraftResult("\n".join(lines), "test-local-model", 10, 5)

        # Simulate a publication-checker regression. The separate scorer must
        # still report that these published citations are wrong.
        with patch("gtm_connector.briefing.check_brief", side_effect=lambda text, evidence: text):
            report = run_evaluation(adapter=WrongCitation())
        self.assertEqual(report["accepted_briefs"], 20)
        self.assertEqual(report["facts_found"], 182)
        self.assertEqual(report["facts_cited_correctly"], 162)
        self.assertFalse(report["publication_gate_passed"])

    def test_cli_requires_explicit_local_model_and_reports_preflight_failure(self):
        for args, message in ((["evaluate", "--model", "example"], "--model requires"),
                              (["brief", "acc-001", "--adapter", "ollama"], "requires --model")):
            with self.subTest(args=args), redirect_stderr(io.StringIO()) as err:
                self.assertEqual(main(args), 2)
                self.assertIn(message, err.getvalue())
        with patch("gtm_connector.ollama.OllamaAdapter", side_effect=RuntimeError(
                "Ollama is unavailable at http://127.0.0.1:11434")):
            with redirect_stderr(io.StringIO()) as err, redirect_stdout(io.StringIO()) as out:
                self.assertEqual(main(["evaluate", "--adapter", "ollama", "--model", "example"]), 2)
                self.assertIn("Ollama is unavailable", err.getvalue())
                self.assertEqual(out.getvalue(), "")


if __name__ == "__main__":
    unittest.main()
