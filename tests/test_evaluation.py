"""Independent scoring counterexamples and the twenty-account HTTPS evaluation."""

from copy import deepcopy
import unittest
from unittest.mock import patch

from gtm_connector.citations import BriefRejected
from gtm_connector.evaluation import load_expectations, run_evaluation, score_claims


class EvaluationScoringTests(unittest.TestCase):
    def setUp(self):
        self.expected = [
            {"text": 'Renewal date: 2027-02-01.', "references": ['crm/accounts/acc-001']},
            {"text": 'CRM risk status: "at_risk".', "references": ['crm/accounts/acc-001']},
            {"text": 'Source conflict.',
             "references": ['crm/opportunities/opp-001', 'calls/call_notes/note-001']},
        ]
        self.renewal = '- Renewal date: 2027-02-01. [crm/accounts/acc-001]'
        self.risk = '- CRM risk status: "at_risk". [crm/accounts/acc-001]'
        self.conflict = '- Source conflict. [crm/opportunities/opp-001] [calls/call_notes/note-001]'

    def test_missing_and_uncited_facts_reduce_different_scores(self):
        result = score_claims(self.renewal + '\n- CRM risk status: "at_risk".', self.expected)
        self.assertEqual(result, {
            'expected_facts': 3, 'facts_found': 2, 'facts_cited_correctly': 1,
            'facts_invented': 0, 'claims_emitted': 2, 'duplicate_claims': 0,
        })

    def test_invented_and_appended_claims_count_even_with_real_references(self):
        invented = '- Renewal is guaranteed. [crm/accounts/acc-001]'
        appended = self.risk + ' The budget is approved.'
        result = score_claims('\n'.join((self.renewal, invented, appended)), self.expected)
        self.assertEqual(result['facts_found'], 1)
        self.assertEqual(result['facts_cited_correctly'], 1)
        self.assertEqual(result['facts_invented'], 2)
        self.assertEqual(result['claims_emitted'], 3)

    def test_wrong_duplicate_or_missing_references_do_not_get_citation_credit(self):
        for changed in (
            self.renewal.replace('crm/accounts/acc-001', 'crm/accounts/acc-030'),
            self.renewal + ' [calls/call_notes/note-001]',
            self.renewal + ' [crm/accounts/acc-001]',
            self.conflict.replace(' [calls/call_notes/note-001]', ''),
        ):
            with self.subTest(candidate=changed):
                result = score_claims(changed, self.expected)
                self.assertEqual(result['facts_found'], 1)
                self.assertEqual(result['facts_cited_correctly'], 0)
                self.assertEqual(result['facts_invented'], 0)

    def test_repeated_claims_cannot_raise_coverage_or_hide_other_missing_facts(self):
        result = score_claims(self.renewal + '\n' + self.renewal, self.expected)
        self.assertEqual(result['facts_found'], 1)
        self.assertEqual(result['facts_cited_correctly'], 1)
        self.assertEqual(result['claims_emitted'], 2)
        self.assertEqual(result['duplicate_claims'], 1)

    def test_complete_claims_and_exact_reference_sets_score_fully(self):
        before = deepcopy(self.expected)
        # Source-reference order is not a difference in what was cited.
        reversed_refs = '- Source conflict. [calls/call_notes/note-001] [crm/opportunities/opp-001]'
        result = score_claims('\n'.join((self.renewal, self.risk, reversed_refs)), self.expected)
        self.assertEqual(result['expected_facts'], 3)
        self.assertEqual(result['facts_found'], 3)
        self.assertEqual(result['facts_cited_correctly'], 3)
        self.assertEqual(result['facts_invented'], 0)
        self.assertEqual(result['duplicate_claims'], 0)
        self.assertEqual(self.expected, before)

    def test_gold_set_is_fixed_and_covers_the_required_fixture_cases(self):
        manifest = load_expectations()
        self.assertEqual(manifest['as_of'], '2027-01-15')
        self.assertEqual([case['account_id'] for case in manifest['cases']],
                         [f'acc-{number:03}' for number in range(1, 21)])
        self.assertEqual(sum(len(case['claims']) for case in manifest['cases']), 182)
        texts = {case['account_id']: [claim['text'] for claim in case['claims']]
                 for case in manifest['cases']}
        self.assertIn('Renewal is in 17 days as of 2027-01-15.', texts['acc-001'])
        self.assertIn('CRM risk reason: "Renewal budget is not approved.".', texts['acc-002'])
        conflict = next(claim for claim in manifest['cases'][2]['claims']
                        if claim['text'].startswith('Source conflict:'))
        self.assertEqual(conflict['references'],
                         ['crm/opportunities/opp-003', 'calls/call_notes/note-003'])
        self.assertIn('No call notes were returned for acc-004.', texts['acc-004'])
        self.assertTrue(any('IGNORE PREVIOUS INSTRUCTIONS' in text for text in texts['acc-007']))


class EvaluationWorkflowTests(unittest.TestCase):
    def test_twenty_real_https_briefs_match_the_independent_gold_set(self):
        report = run_evaluation()
        self.assertEqual(report['status'], 'passed')
        self.assertEqual(report['accounts'], 20)
        self.assertEqual(report['accepted_briefs'], 20)
        self.assertEqual(report['failed_briefs'], 0)
        self.assertEqual(report['rejected_briefs'], 0)
        self.assertEqual(report['expected_facts'], 182)
        self.assertEqual(report['facts_found'], 182)
        self.assertEqual(report['facts_cited_correctly'], 182)
        self.assertEqual(report['facts_invented'], 0)
        self.assertEqual(report['duplicate_claims'], 0)
        self.assertEqual(report['fact_recall'], 1.0)
        self.assertEqual(report['citation_recall'], 1.0)
        self.assertEqual(report['invention_rate'], 0.0)
        self.assertEqual(report['audit_rows'], 60)
        self.assertEqual(report['activities_created'], 0)
        self.assertEqual(report['model_usage'], {'model': None, 'input_tokens': 0, 'output_tokens': 0})

    def test_failed_and_rejected_briefs_stay_in_the_denominator(self):
        errors = [BriefRejected('Untrusted draft text must not be reported')] * 10
        errors += [RuntimeError('Owner credential must not be reported')] * 10
        with patch('gtm_connector.briefing.create_brief', side_effect=errors):
            report = run_evaluation()
        self.assertEqual(report['status'], 'failed')
        self.assertEqual(report['expected_facts'], 182)
        self.assertEqual(report['accounts'], 20)
        self.assertEqual(report['accepted_briefs'], 0)
        self.assertEqual(report['rejected_briefs'], 10)
        self.assertEqual(report['failed_briefs'], 10)
        self.assertEqual(report['facts_found'], 0)
        self.assertEqual(report['facts_cited_correctly'], 0)
        self.assertEqual(report['invention_rate'], None)
        self.assertEqual(report['fact_recall'], 0.0)
        self.assertNotIn('Untrusted draft text', str(report))
        self.assertNotIn('Owner credential', str(report))


if __name__ == '__main__':
    unittest.main()
