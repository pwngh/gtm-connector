"""A brief opens with what needs attention. Each signal is derived from structured fields and cited."""

from pathlib import Path
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected, check_brief
from gtm_connector.runtime import Runtime


class SignalTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.runtime = Runtime(Path(directory.name) / "state")
        self.runtime.__enter__()
        self.addCleanup(self.runtime.__exit__, None, None, None)

    def brief(self, account_id):
        return create_brief(self.runtime, account_id, include_slack=True)

    def signals(self, brief):
        return [line for line in brief["text"].splitlines() if line.startswith("- Signal: ")]

    def test_each_signal_states_one_derived_fact_with_its_sources(self):
        run = "[run/{}]"
        expected = {
            "acc-001": ['- Signal: renewal is in 17 days and opportunity opp-001 is still open at stage "discovery". '
                        "[crm/accounts/acc-001] [crm/opportunities/opp-001] " + run],
            "acc-002": ['- Signal: renewal is in 24 days and opportunity opp-002 is still open at stage "evaluation". '
                        "[crm/accounts/acc-002] [crm/opportunities/opp-002] " + run,
                        '- Signal: CRM marks this account at risk: "Renewal budget is not approved.". '
                        "[crm/accounts/acc-002]"],
            "acc-006": ["- Signal: the latest call note note-006 is 104 days old as of 2027-01-15. "
                        "[calls/call_notes/note-006] " + run],
            "acc-007": ['- Signal: renewal is in 14 days and opportunity opp-007 is still open at stage "proposal". '
                        "[crm/accounts/acc-007] [crm/opportunities/opp-007] " + run,
                        '- Signal: contact "Dana Crest" has no role recorded. [crm/contacts/con-007]'],
            # 31 days out and closed won: nothing needs attention on these fields.
            "acc-003": [],
        }
        for account_id, lines in expected.items():
            with self.subTest(account=account_id):
                brief = self.brief(account_id)
                self.assertEqual(self.signals(brief), [line.format(brief["brief_id"]) for line in lines])

    def test_signals_open_the_brief(self):
        lines = self.brief("acc-007")["text"].splitlines()
        self.assertTrue(all(line.startswith("- Signal: ") for line in lines[:2]))
        self.assertTrue(lines[2].startswith("- Account: "))

    def test_a_draft_cannot_drop_change_or_invent_a_signal(self):
        brief = self.brief("acc-002")
        signal = self.signals(brief)[0]
        candidates = {
            "dropped": brief["text"].replace(signal + "\n", ""),
            "changed": brief["text"].replace("is in 24 days", "is in 54 days"),
            "invented": brief["text"] + '- Signal: contact "Rowan Bell" has no role recorded. [crm/contacts/con-002]\n',
        }
        for name, candidate in candidates.items():
            with self.subTest(candidate=name), self.assertRaises(BriefRejected):
                check_brief(candidate, brief["evidence"])
        self.assertEqual(check_brief(brief["text"], brief["evidence"]), brief["text"])


if __name__ == "__main__":
    unittest.main()
