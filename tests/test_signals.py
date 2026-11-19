"""A brief opens with what needs attention. Each signal is derived from structured fields and cited."""

from pathlib import Path
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected, build_facts, check_brief
from gtm_connector.runtime import Runtime
from tests.test_citations import evidence
from tests.test_salesforce_briefing import CONTRACT_REF, contract_record, evidence as salesforce_evidence


def repeated(source, table, count, **fields):
    """Evidence with `count` records in a table, copied from its one fixture record, and its receipt in step."""
    place = next(index for index, row in enumerate(source["records"]) if row["record_ref"].split("/")[1] == table)
    template = source["records"].pop(place)
    copies = []
    for number in range(1, count + 1):
        copy = {**template, **fields}
        if "source_id" in copy:
            copy["source_id"] = f"{template['source_id'][:3]}{number:012}AAA"
            copy["id"] = template["id"][:7] + copy["source_id"]
        else:
            copy["id"] = f"{template['id'][:4]}{number:03}"
        copy["record_ref"] = f"crm/{table}/{copy['id']}"
        copies.append(copy)
    source["records"][place:place] = copies
    receipt = next(row for row in source["receipts"] if template["record_ref"] in row["rows"])
    receipt["rows"] = [ref for ref in receipt["rows"] if ref != template["record_ref"]]
    receipt["rows"] += [copy["record_ref"] for copy in copies]
    receipt["row_count"] = len(receipt["rows"])
    return source


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
            "acc-001": ['- Signal: renewal is in 17 days and opportunity opp-001 "Alder Vale Packaging 2027 renewal" '
                        'is still open at stage "discovery". '
                        "[crm/accounts/acc-001] [crm/opportunities/opp-001] " + run],
            "acc-002": ['- Signal: renewal is in 24 days and opportunity opp-002 '
                        '"Copper Finch Distribution 2027 renewal" is still open at stage "evaluation". '
                        "[crm/accounts/acc-002] [crm/opportunities/opp-002] " + run,
                        '- Signal: CRM marks this account at risk: "Renewal budget is not approved.". '
                        "[crm/accounts/acc-002]"],
            "acc-006": ["- Signal: the latest call note note-006 is 104 days old as of 2027-01-15. "
                        "[calls/call_notes/note-006] " + run],
            "acc-007": ['- Signal: renewal is in 14 days and opportunity opp-007 "Juniper Crest Learning 2027 renewal" '
                        'is still open at stage "proposal". '
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

    def lines(self, source):
        return [line for line in build_facts(source) if line.startswith("- Signal: ")]

    def open_renewal(self, renewal_date):
        source = evidence()
        source["records"][0]["renewal_date"] = renewal_date
        source["records"][2]["stage"] = "negotiation"
        return self.signals(source)

    def test_an_open_opportunity_signals_from_thirty_days_out_and_after_the_date_passes(self):
        stage = ' and opportunity opp-001 "Fixture renewal" is still open at stage "negotiation".'
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
        still_open = (f' and opportunity {opportunity} "Invented opportunity" is still open at stage "Negotiation". '
                      f"[{CONTRACT_REF}] [crm/opportunities/{opportunity}] [run/brief-sf-fixture]")
        no_role = '- Signal: contact "Invented contact" has no role recorded. [crm/contacts/con-sf-003000000000001AAA]'
        for end, when in (("2027-02-15", None), ("2027-02-14", "is in 30 days"),
                          ("2027-01-16", "is in 1 day"), ("2027-01-15", "is today")):
            with self.subTest(end=end):
                renewal = ["- Signal: contract end " + when + still_open] if when else []
                self.assertEqual(signals(end), renewal + [no_role])
        self.assertEqual(signals("2027-01-16", is_closed=True), [no_role])

    def test_six_contacts_with_no_role_give_one_signal_and_five_give_one_each(self):
        native = "crm/contacts/con-sf-003{:012}AAA"
        for crm, source, title, each, account in (
                ("demo", evidence, "", '- Signal: contact "Fixture contact con-001" has no role recorded. '
                 "[crm/contacts/con-00{}]", "crm/accounts/acc-001"),
                ("salesforce", salesforce_evidence, None, '- Signal: contact "Invented contact" has no role recorded. ['
                 + native + "]", "crm/accounts/acc-sf-001000000000001AAA")):
            with self.subTest(crm=crm):
                self.assertEqual(self.lines(repeated(source(), "contacts", 5, title=title)),
                                 [each.format(number) for number in range(1, 6)])
                self.assertEqual(self.lines(repeated(source(), "contacts", 6, title=title)),
                                 [f"- Signal: 6 contacts have no role recorded. [{account}] [audit/receipt-get_account]"])
                # Only the contacts with no role are counted.
                mixed = repeated(source(), "contacts", 7, title=title)
                for record in mixed["records"][1:3]:
                    record["title"] = "Buyer"
                self.assertEqual(self.lines(mixed), [each.format(number) for number in range(3, 8)])

    def test_six_open_opportunities_give_one_signal_and_five_give_one_each_on_the_demo_crm(self):
        def source(count, renewal_date="2027-01-24"):
            crowded = repeated(evidence(), "opportunities", count, stage="negotiation")
            crowded["records"][0]["renewal_date"] = renewal_date
            return crowded

        cited = " [crm/accounts/acc-001] [audit/receipt-list_opportunities] [run/brief-fixture-001]"
        self.assertEqual(self.lines(source(5)), [
            f'- Signal: renewal is in 9 days and opportunity opp-00{number} "Fixture renewal" is still open at stage '
            f'"negotiation". [crm/accounts/acc-001] [crm/opportunities/opp-00{number}] [run/brief-fixture-001]'
            for number in range(1, 6)])
        for renewal_date, when in (("2027-01-24", "renewal is in 9 days"), ("2027-01-16", "renewal is in 1 day"),
                                   ("2027-01-15", "renewal is due today"), ("2027-01-12", "renewal was 3 days ago")):
            with self.subTest(renewal_date=renewal_date):
                self.assertEqual(self.lines(source(6, renewal_date)),
                                 [f"- Signal: {when} and 6 opportunities are still open." + cited])
        self.assertEqual(self.lines(source(6, "2027-02-15")), [])                             # 31 days
        # Only the open opportunities are counted.
        mixed = source(7)
        mixed["records"][2]["stage"], mixed["records"][3]["stage"] = "closed_won", "closed_lost"
        self.assertEqual(len(self.lines(mixed)), 5)
        self.assertEqual(self.lines(source(12)), ["- Signal: renewal is in 9 days and 12 opportunities are still open."
                                                  + cited])

    def test_six_open_opportunities_give_one_signal_and_five_give_one_each_on_salesforce(self):
        def source(count, end="2027-01-24"):
            crowded = repeated(salesforce_evidence([contract_record(end=end)]), "opportunities", count,
                               stage="Negotiation", is_closed=False, is_won=False)
            crowded["records"][1]["title"] = "Buyer"
            return crowded

        cited = f" [{CONTRACT_REF}] [audit/receipt-list_opportunities] [run/brief-sf-fixture]"
        deal = "opp-sf-006{:012}AAA"
        self.assertEqual(self.lines(source(5)), [
            f'- Signal: contract end is in 9 days and opportunity {deal.format(number)} "Invented opportunity" is still '
            f'open at stage "Negotiation". [{CONTRACT_REF}] [crm/opportunities/{deal.format(number)}] '
            "[run/brief-sf-fixture]" for number in range(1, 6)])
        for end, when in (("2027-01-24", "contract end is in 9 days"), ("2027-01-16", "contract end is in 1 day"),
                          ("2027-01-15", "contract end is today")):
            with self.subTest(end=end):
                self.assertEqual(self.lines(source(6, end)),
                                 [f"- Signal: {when} and 6 opportunities are still open." + cited])
        self.assertEqual(self.lines(source(6, "2027-02-15")), [])                             # 31 days
        mixed = source(7)
        for record in mixed["records"][2:4]:
            record["is_closed"] = True
        self.assertEqual(len(self.lines(mixed)), 5)

    def test_grouped_signals_keep_the_order_and_a_draft_cannot_drop_or_split_one(self):
        source = repeated(repeated(evidence(), "contacts", 6, title=""), "opportunities", 6, stage="proposal")
        source["records"][0].update(renewal_date="2027-01-24", risk_status="at_risk")
        facts = build_facts(source)
        deals, risk, roles = facts[:3]
        self.assertEqual([line.split(" [")[0] for line in facts if line.startswith("- Signal: ")], [
            "- Signal: renewal is in 9 days and 6 opportunities are still open.",
            "- Signal: CRM marks this account at risk.", "- Signal: 6 contacts have no role recorded."])
        # The grouped lines cite the reads that returned the records, and the facts still cite every record.
        receipts = {receipt["tool"]: receipt["rows"] for receipt in source["receipts"]}
        self.assertLessEqual({f"crm/opportunities/opp-00{number}" for number in range(1, 7)},
                             set(receipts["list_opportunities"]))
        self.assertLessEqual({f"crm/contacts/con-00{number}" for number in range(1, 7)}, set(receipts["get_account"]))
        for number in range(1, 7):
            self.assertEqual(sum(f"[crm/contacts/con-00{number}]" in line for line in facts), 1)
            self.assertEqual(sum(f"[crm/opportunities/opp-00{number}]" in line for line in facts), 1)
        rest = facts[3:]
        self.assertEqual(check_brief("\n".join(rest + [roles, risk, deals]), source), "\n".join(facts) + "\n")
        for dropped in (deals, roles):
            with self.assertRaisesRegex(BriefRejected, "every signal"):
                check_brief("\n".join(line for line in facts if line != dropped), source)
        one_each = ('- Signal: contact "Fixture contact con-001" has no role recorded. [crm/contacts/con-001]',
                    '- Signal: renewal is in 9 days and opportunity opp-001 "Fixture renewal" is still open at stage '
                    '"proposal". [crm/accounts/acc-001] [crm/opportunities/opp-001] [run/brief-fixture-001]',
                    "- Signal: 5 contacts have no role recorded. [crm/accounts/acc-001] [audit/receipt-get_account]")
        for invented in one_each:
            with self.assertRaises(BriefRejected):
                check_brief("\n".join(facts + [invented]), source)


if __name__ == "__main__":
    unittest.main()
