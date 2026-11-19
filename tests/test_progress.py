"""Evaluation reports count-only progress without changing its result contract."""

import unittest
from unittest.mock import patch

from gtm_connector.citations import BriefRejected
from gtm_connector.evaluation import run_evaluation


class EvaluationProgressTests(unittest.TestCase):
    def test_fixture_evaluation_reports_start_and_every_completed_case(self):
        events = []
        report = run_evaluation(progress=lambda *event: events.append(event))
        self.assertEqual(report["status"], "passed")
        self.assertEqual(report["facts_cited_correctly"], 188)
        self.assertEqual(events, [(0, 20, None)] + [(number, 20, "accepted") for number in range(1, 21)])

    def test_failed_cases_still_advance_progress_without_leaking_source_or_error_text(self):
        events = []
        failures = [BriefRejected("invented private candidate text")] * 10
        failures += [RuntimeError("invented private service credential")] * 10
        with patch("gtm_connector.briefing.create_brief", side_effect=failures):
            report = run_evaluation(progress=lambda *event: events.append(event))
        self.assertEqual(report["status"], "failed")
        self.assertEqual(events, [(0, 20, None)] + [(number, 20, "rejected") for number in range(1, 11)]
                         + [(number, 20, "failed") for number in range(11, 21)])
        self.assertNotIn("private", str(events))
        self.assertNotIn("acc-", str(events))


if __name__ == "__main__":
    unittest.main()
