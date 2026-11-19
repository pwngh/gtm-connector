"""A brief opens with what needs attention. Each signal is derived from structured fields and cited."""

from pathlib import Path
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected, build_facts, check_brief
from gtm_connector.runtime import Runtime
from tests.test_citations import evidence
from tests.test_salesforce_briefing import CONTRACT_REF, contract_record, evidence as salesforce_evidence


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
        with self.assertRaisesRegex(BriefRejected, "every signal"):
            check_brief(candidates["dropped"], brief["evidence"])

    def test_signals_are_published_first_wherever_a_draft_puts_them(self):
        brief = self.brief("acc-002")
        signals = self.signals(brief)
        rest = [line for line in brief["text"].splitlines() if line not in signals]
        reordered = "\n".join(rest + signals[::-1]) + "\n"
        self.assertEqual(check_brief(reordered, brief["evidence"]), brief["text"])


class SignalRuleTests(unittest.TestCase):
    """The rules at their boundaries, on a small synthetic account."""

    def signals(self, source):
        return [line.split(" [")[0] for line in build_facts(source) if line.startswith("- Signal: ")]

    def open_renewal(self, renewal_date):
        source = evidence()
        source["records"][0]["renewal_date"] = renewal_date
        source["records"][2]["stage"] = "negotiation"
        return self.signals(source)

    def test_an_open_opportunity_signals_from_thirty_days_out_and_after_the_date_passes(self):
        stage = ' and opportunity opp-001 is still open at stage "negotiation".'
        for renewal_date, expected in (("2027-02-15", []),                                   # 31 days
                                       ("2027-02-14", ["- Signal: renewal is in 30 days" + stage]),
                                       ("2027-01-16", ["- Signal: renewal is in 1 day" + stage]),
                                       ("2027-01-15", ["- Signal: renewal is due today" + stage]),
                                       ("2027-01-14", ["- Signal: renewal was 1 day ago" + stage])):
            with self.subTest(renewal_date=renewal_date):
                self.assertEqual(self.open_renewal(renewal_date), expected)
        for stage_name in ("closed_won", "closed_lost"):
            source = evidence()
            source["records"][2]["stage"] = stage_name
            self.assertEqual(self.signals(source), [])

    def test_the_newest_call_note_decides_staleness_by_its_instant(self):
        def notes(*times):
            source = evidence()
            source["records"] = source["records"][:3] + [
                {"id": f"note-00{number}", "record_ref": f"calls/call_notes/note-00{number}",
                 "account_id": "acc-001", "called_at": called_at, "body": "A note."}
                for number, called_at in enumerate(times, 1)]
            receipt = source["receipts"][2]
            receipt["rows"] = [row["record_ref"] for row in source["records"][3:]]
            receipt["row_count"] = len(receipt["rows"])
            return self.signals(source)

        old = "- Signal: the latest call note {} is {} days old as of 2027-01-15."
        self.assertEqual(notes("2026-11-16T15:00:00Z"), [])                                   # 60 days
        self.assertEqual(notes("2026-11-15T15:00:00Z"), [old.format("note-001", 61)])
        # The later instant on the same day wins, and one recent note clears the signal.
        self.assertEqual(notes("2026-10-03T09:00:00Z", "2026-10-03T17:00:00Z"), [old.format("note-002", 104)])
        self.assertEqual(notes("2026-10-03T15:00:00Z", "2027-01-14T15:00:00Z"), [])
        # An offset is applied before the day is taken.
        self.assertEqual(notes("2026-11-15T23:30:00-05:00"), [])
        # A time that is malformed, out of range, or without an offset raises no signal.
        for unknown in ("2026-10-03", "2026-10-03T15:00:00", "2026-10-03Tnonsense", "0001-01-01T00:00:00+01:00"):
            self.assertEqual(notes(unknown), [])
        # A note that cannot be ordered beside a stale one is not counted as older.
        self.assertEqual(notes("2026-10-03T15:00:00Z", "2026-10-03"),
                         ["- Signal: the latest dated call note note-001 is 104 days old as of 2027-01-15."])

    def test_salesforce_signals_count_from_the_contract_end_and_put_the_open_opportunity_first(self):
        def signals(end, is_closed=False):
            source = salesforce_evidence([contract_record(end=end)])
            source["records"][2].update(stage="Negotiation", is_closed=is_closed, is_won=False)
            return [line for line in build_facts(source) if line.startswith("- Signal: ")]

        opportunity = "opp-sf-006000000000001AAA"
        still_open = (f' and opportunity {opportunity} is still open at stage "Negotiation". '
                      f"[{CONTRACT_REF}] [crm/opportunities/{opportunity}] [run/brief-sf-fixture]")
        no_role = '- Signal: contact "Invented contact" has no role recorded. [crm/contacts/con-sf-003000000000001AAA]'
        for end, when in (("2027-02-15", None), ("2027-02-14", "is in 30 days"),
                          ("2027-01-16", "is in 1 day"), ("2027-01-15", "is today")):
            with self.subTest(end=end):
                renewal = ["- Signal: contract end " + when + still_open] if when else []
                self.assertEqual(signals(end), renewal + [no_role])
        self.assertEqual(signals("2027-01-16", is_closed=True), [no_role])


if __name__ == "__main__":
    unittest.main()
