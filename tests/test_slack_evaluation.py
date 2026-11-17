"""Measure the optional fourth read without changing the 182-claim baseline."""

import hashlib
import json
import unittest
from unittest.mock import patch

from gtm_connector.citations import BriefRejected, build_facts
from gtm_connector.drafting import DraftResult
from gtm_connector.evaluation import load_expectations, run_evaluation


class SlackEvaluationTests(unittest.TestCase):
    def test_extension_keeps_every_original_claim_and_adds_independent_expectations(self):
        base = load_expectations()
        original = [case["claims"] for case in base["cases"]]
        digest = hashlib.sha256(json.dumps(original, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
        self.assertEqual(digest, "de7bda02d0126aa1c59628cac7ffcd8b9e8c7df216e006018f91c15a1558a73c")
        self.assertEqual(sum(map(len, original)), 182)
        extended = load_expectations(include_slack=True)
        self.assertEqual(sum(len(case["claims"]) for case in extended["cases"]), 223)
        for old, new in zip(base["cases"], extended["cases"]):
            self.assertEqual(new["account_id"], old["account_id"])
            self.assertEqual(new["claims"][:len(old["claims"])], old["claims"])
        self.assertEqual(load_expectations()["cases"], base["cases"])
        self.assertEqual(base["scopes"], ["crm:read", "calls:read"])
        self.assertEqual(extended["scopes"], ["crm:read", "calls:read", "slack:read"])
        self.assertEqual(extended["workflow_version"], 2)
        conflict = next(claim for claim in extended["cases"][2]["claims"]
                        if claim["text"].startswith("Three-source conflict:"))
        self.assertEqual(conflict["references"],
                         ["crm/opportunities/opp-003", "calls/call_notes/note-003", "slack/messages/msg-003"])

    def test_twenty_extended_https_briefs_match_the_static_gold_set(self):
        report = run_evaluation(include_slack=True)
        self.assertEqual(report["status"], "passed")
        self.assertEqual((report["workflow"], report["workflow_version"]), ("crm-calls-slack", 2))
        self.assertEqual(report["evaluation"], "fixture-20-slack")
        self.assertEqual(report["accepted_briefs"], 20)
        self.assertEqual(report["expected_facts"], 223)
        self.assertEqual(report["facts_found"], 223)
        self.assertEqual(report["facts_cited_correctly"], 223)
        self.assertEqual(report["facts_invented"], 0)
        self.assertEqual(report["audit_rows"], 80)
        self.assertEqual(report["activities_created"], 0)
        self.assertEqual(report["model_usage"], {"model": None, "input_tokens": 0, "output_tokens": 0})

    def test_rejected_and_failed_extended_briefs_keep_all_expected_claims(self):
        with patch("gtm_connector.briefing.create_brief",
                   side_effect=[BriefRejected("private candidate")] * 10 + [RuntimeError("private failure")] * 10):
            report = run_evaluation(include_slack=True)
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["expected_facts"], 223)
        self.assertEqual((report["rejected_briefs"], report["failed_briefs"]), (10, 10))
        self.assertEqual(report["facts_found"], 0)
        self.assertIsNone(report["invention_rate"])
        self.assertNotIn("private", json.dumps(report))

    def test_fake_model_without_follow_up_is_scored_but_never_published(self):
        class OmittingModel:
            name = "test-model"

            def draft(self, evidence):
                candidate = "\n".join(line for line in build_facts(evidence)
                                      if not line.startswith("- Proposed follow-up:"))
                return DraftResult(candidate, model="fixture-model", input_tokens=7, output_tokens=3)

        report = run_evaluation(include_slack=True, adapter=OmittingModel())
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["rejected_briefs"], 20)
        self.assertEqual(report["accepted_briefs"], 0)
        self.assertEqual(report["claims_emitted"], 0)
        self.assertIsNone(report["invention_rate"])
        self.assertEqual(report["candidate"]["claims_emitted"], 203)
        self.assertEqual(report["candidate"]["missing_required_claims"], 20)
        self.assertEqual(report["candidate"]["reason_counts"]["missing_required"], 20)
        self.assertEqual(report["candidate"]["facts_invented"], 0)
        self.assertEqual(report["audit_rows"], 80)
        self.assertTrue(report["publication_gate_passed"])
        self.assertEqual(report["model_usage"], {"model": "fixture-model", "input_tokens": 140, "output_tokens": 60})


if __name__ == "__main__":
    unittest.main()
