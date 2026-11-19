"""A card opens with what needs attention. Each signal is derived from record fields and dates and cites units."""

from pathlib import Path
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.runtime import Runtime
from gtm_connector.units import build
from tests import varied
from tests.test_units import evidence


ACCOUNT_ID = "acc-sf-001000000000001AAA"
NO_ROLE = "This contact has no role recorded. [{}]"


def demo(*, stage="closed_won", **account):
    """The demo fixture evidence. Its one opportunity is closed unless a test opens it."""
    source = evidence()
    source["records"][0].update(account)
    source["records"][2]["stage"] = stage
    return source


def salesforce(end="2027-06-01", *, is_closed=True, title=None):
    """One invented Salesforce account: a contact, an opportunity, and a contract that ends on `end`."""
    native = {"backend": "salesforce", "account_id": ACCOUNT_ID}
    records = [
        {"id": ACCOUNT_ID, "source_id": ACCOUNT_ID[7:], "backend": "salesforce",
         "record_ref": "crm/accounts/" + ACCOUNT_ID, "name": "Invented contract account", "industry": None,
         "account_number": None},
        {**native, "id": "con-sf-003000000000001AAA", "source_id": "003000000000001AAA",
         "record_ref": "crm/contacts/con-sf-003000000000001AAA", "name": "Invented contact", "title": title,
         "email": None},
        {**native, "id": "opp-sf-006000000000001AAA", "source_id": "006000000000001AAA",
         "record_ref": "crm/opportunities/opp-sf-006000000000001AAA", "name": "Invented opportunity",
         "stage": "Negotiation", "is_closed": is_closed, "is_won": False, "amount_decimal": "1234.500",
         "currency": None, "close_date": "2027-01-31"},
        {**native, "id": "ctr-sf-800000000000001AAA", "source_id": "800000000000001AAA",
         "record_ref": "crm/contracts/ctr-sf-800000000000001AAA", "status": "Activated", "status_code": "Activated",
         "start_date": "2026-01-01", "end_date": end, "contract_term": 12},
    ]
    receipts = []
    for tool, prefixes in (("get_account", ("crm/accounts/", "crm/contacts/", "crm/contracts/")),
                           ("list_opportunities", ("crm/opportunities/",)), ("read_call_notes", ("calls/",))):
        rows = [record["record_ref"] for record in records if record["record_ref"].startswith(prefixes)]
        receipts.append({"audit_id": "receipt-" + tool, "brief_id": "brief-fixture", "tool": tool,
                         "arguments": {"account_id": ACCOUNT_ID}, "rows": rows, "row_count": len(rows),
                         "status": "ok", "http_status": 200})
    return {**evidence(), "account_id": ACCOUNT_ID, "crm_backend": "salesforce", "records": records,
            "receipts": receipts}


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

    def test_each_signal_states_one_derived_fact_and_cites_its_records(self):
        still_open = 'The renewal is in {} days and this opportunity is still open at stage "{}". [1][3]'
        expected = {
            "acc-001": [(still_open.format(17, "discovery"), ["accounts/acc-001", "opportunities/opp-001"])],
            "acc-002": [("CRM marks this account at risk. [1]", ["accounts/acc-002"]),
                        (still_open.format(24, "evaluation"), ["accounts/acc-002", "opportunities/opp-002"])],
            "acc-006": [("The latest dated call that was read is 104 days old. [4]", ["call_notes/note-006"])],
            "acc-007": [(still_open.format(14, "proposal"), ["accounts/acc-007", "opportunities/opp-007"]),
                        (NO_ROLE.format(2), ["contacts/con-007"])],
            # 80 days out, a role on the contact, and a call this week: nothing needs attention.
            "acc-010": [],
        }
        for account_id, signals in expected.items():
            with self.subTest(account=account_id):
                brief = create_brief(self.runtime, account_id, include_slack=True)
                card = varied.hold(self, brief)
                self.assertEqual([line.strip() for line in card["ATTENTION"]], [line for line, _ in signals])
                for line, (_, records) in zip(card["ATTENTION"], signals):
                    self.assertEqual([brief["units"][number - 1]["record_ref"].split("/", 1)[1]
                                      for number in varied.cited(line)], records)

    def test_signals_open_the_card_and_the_heading_is_absent_without_one(self):
        lines = create_brief(self.runtime, "acc-007", include_slack=True)["text"].split("\n")
        self.assertTrue(lines[0].startswith("DEAL CARD: "))
        self.assertEqual(lines[1:3], ["", "ATTENTION, from record fields and dates"])
        self.assertEqual(lines[5:7], ["", "RECORD VALUES, from CRM fields and connector arithmetic"])
        quiet = create_brief(self.runtime, "acc-010", include_slack=True)["text"].split("\n")
        self.assertEqual(quiet[1:3], ["", "RECORD VALUES, from CRM fields and connector arithmetic"])
        self.assertNotIn("ATTENTION", "\n".join(quiet))


class SignalRuleTests(unittest.TestCase):
    """The rules at their boundaries, on a small synthetic account."""

    def card(self, source):
        return varied.sections(self, build(source)[1])

    def signals(self, source):
        return [line.strip() for line in self.card(source)["ATTENTION"]]

    def test_an_open_opportunity_signals_from_thirty_days_out_and_a_passed_date_is_its_own_signal(self):
        stage = ' and this opportunity is still open at stage "negotiation". [1][3]'
        for renewal_date, expected in (("2027-02-15", []),                                   # 31 days
                                       ("2027-02-14", ["The renewal is in 30 days" + stage]),
                                       ("2027-01-16", ["The renewal is in 1 day" + stage]),
                                       ("2027-01-15", ["The renewal is today" + stage]),
                                       ("2027-01-14", ["The renewal date passed 1 day ago. [1]"])):
            with self.subTest(renewal_date=renewal_date):
                self.assertEqual(self.signals(demo(stage="negotiation", renewal_date=renewal_date)), expected)
        for stage_name in ("closed_won", "closed_lost"):
            self.assertEqual(self.signals(demo(stage=stage_name)), [])
            self.assertEqual(self.signals(demo(stage=stage_name, renewal_date="2027-01-12")),
                             ["The renewal date passed 3 days ago. [1]"])

    def test_the_newest_call_decides_staleness_by_its_instant(self):
        def notes(*times, body="A note."):
            source = demo()
            source["records"] = source["records"][:3] + [
                {"id": f"note-00{number}", "record_ref": f"calls/call_notes/note-00{number}",
                 "account_id": "acc-001", "called_at": called_at, "speaker": None, "body": body}
                for number, called_at in enumerate(times, 1)]
            receipt = source["receipts"][2]
            receipt["rows"] = [row["record_ref"] for row in source["records"][3:]]
            receipt["row_count"] = len(receipt["rows"])
            return self.signals(source)

        old = "The latest dated call that was read is {} days old. [{}]"
        self.assertEqual(notes("2026-11-16T15:00:00Z"), [])                                   # 60 days
        self.assertEqual(notes("2026-11-15T15:00:00Z"), [old.format(61, 4)])
        # The later instant on the same day wins, and one recent call clears the signal.
        self.assertEqual(notes("2026-10-03T09:00:00Z", "2026-10-03T17:00:00Z"), [old.format(104, 5)])
        self.assertEqual(notes("2026-10-03T15:00:00Z", "2027-01-14T15:00:00Z"), [])
        # An offset is applied before the day is taken.
        self.assertEqual(notes("2026-11-15T23:30:00-05:00"), [])
        # A time that is malformed, out of range, or without an offset raises no signal.
        for unknown in ("2026-10-03", "2026-10-03T15:00:00", "2026-10-03Tnonsense", "0001-01-01T00:00:00+01:00"):
            self.assertEqual(notes(unknown), [])
        # A call that cannot be ordered beside a stale one is not counted as newer. Units follow the time text.
        self.assertEqual(notes("2026-10-03T15:00:00Z", "2026-10-03"), [old.format(104, 5)])
        # A call with no sentence has no unit to cite, and so raises no signal.
        self.assertEqual(notes("2026-10-03T15:00:00Z", body=""), [])

    def test_salesforce_signals_count_from_the_contract_end_and_put_the_open_opportunity_first(self):
        still_open = ' and this opportunity is still open at stage "Negotiation". [3][4]'
        for end, when in (("2027-02-15", None), ("2027-02-14", "is in 30 days"),
                          ("2027-01-16", "is in 1 day"), ("2027-01-15", "is today")):
            with self.subTest(end=end):
                renewal = ["The contract end " + when + still_open] if when else []
                self.assertEqual(self.signals(salesforce(end, is_closed=False)), renewal + [NO_ROLE.format(2)])
        self.assertEqual(self.signals(salesforce("2027-01-16")), [NO_ROLE.format(2)])
        self.assertEqual(self.card(salesforce("2027-01-16"))["RECORD VALUES"][0],
                         '  Contract end: 2027-01-16, in 1 day; status "Activated" [3]')

    def test_a_contract_that_ended_is_a_signal_and_the_contract_end_is_missing(self):
        for end, age in (("2027-01-14", "1 day"), ("2026-12-31", "15 days")):
            card = self.card(salesforce(end, is_closed=False, title="Buyer"))
            self.assertEqual(card["ATTENTION"], [f"  The latest contract end date, {end}, was {age} ago. [3]"])
            self.assertEqual(card["RECORD VALUES"][0],
                             "  Contract end: Missing: no contract ends on or after 2027-01-15 [1]")

    def test_six_contacts_with_no_role_give_one_signal_and_five_give_one_each(self):
        for crm, source, title in (("demo", demo, ""), ("salesforce", salesforce, None)):
            with self.subTest(crm=crm):
                self.assertEqual(self.signals(repeated(source(), "contacts", 5, title=title)),
                                 [NO_ROLE.format(number) for number in range(2, 7)])
                crowded = self.card(repeated(source(), "contacts", 6, title=title))
                self.assertEqual(crowded["ATTENTION"], ["  6 contacts have no role recorded. [1]"])
                self.assertIn("  CRM: 1 more contacts were read and are not shown (6 in all).", crowded["NOT READ"])
                # Only the contacts with no role are counted.
                mixed = repeated(source(), "contacts", 7, title=title)
                for record in mixed["records"][1:3]:
                    record["title"] = "Buyer"
                self.assertEqual(self.signals(mixed), [NO_ROLE.format(number) for number in range(4, 9)])

    def test_six_open_opportunities_give_one_signal_and_five_give_one_each_on_the_demo_crm(self):
        def source(count, renewal_date="2027-01-24"):
            return repeated(demo(renewal_date=renewal_date), "opportunities", count, stage="negotiation")

        self.assertEqual(self.signals(source(5)), [
            f'The renewal is in 9 days and this opportunity is still open at stage "negotiation". [1][{number}]'
            for number in range(3, 8)])
        for renewal_date, when in (("2027-01-24", "in 9 days"), ("2027-01-16", "in 1 day"), ("2027-01-15", "today")):
            with self.subTest(renewal_date=renewal_date):
                self.assertEqual(self.signals(source(6, renewal_date)),
                                 [f"The renewal is {when} and 6 opportunities are still open. [1]"])
        self.assertEqual(self.signals(source(6, "2027-01-12")), ["The renewal date passed 3 days ago. [1]"])
        self.assertEqual(self.signals(source(6, "2027-02-15")), [])                             # 31 days
        # Only the open opportunities are counted.
        mixed = source(7)
        mixed["records"][2]["stage"], mixed["records"][3]["stage"] = "closed_won", "closed_lost"
        self.assertEqual(len(self.signals(mixed)), 5)
        self.assertEqual(self.signals(source(12)),
                         ["The renewal is in 9 days and 12 opportunities are still open. [1]"])

    def test_six_open_opportunities_give_one_signal_and_five_give_one_each_on_salesforce(self):
        def source(count, end="2027-01-24"):
            return repeated(salesforce(end, title="Buyer"), "opportunities", count, is_closed=False)

        self.assertEqual(self.signals(source(5)), [
            f'The contract end is in 9 days and this opportunity is still open at stage "Negotiation". [3][{number}]'
            for number in range(4, 9)])
        for end, when in (("2027-01-24", "in 9 days"), ("2027-01-16", "in 1 day"), ("2027-01-15", "today")):
            with self.subTest(end=end):
                self.assertEqual(self.signals(source(6, end)),
                                 [f"The contract end is {when} and 6 opportunities are still open. [3]"])
        self.assertEqual(self.signals(source(6, "2027-02-15")), [])                             # 31 days
        mixed = source(7)
        for record in mixed["records"][2:4]:
            record["is_closed"] = True
        self.assertEqual(len(self.signals(mixed)), 5)

    def test_grouped_signals_keep_the_order_and_every_record_stays_a_unit(self):
        source = repeated(repeated(demo(renewal_date="2027-01-24", risk_status="at_risk"), "contacts", 6, title=""),
                          "opportunities", 6, stage="proposal")
        units, text = build(source)
        card = varied.sections(self, text)
        self.assertEqual(card["ATTENTION"], [
            "  CRM marks this account at risk. [1]",
            "  The renewal is in 9 days and 6 opportunities are still open. [1]",
            "  6 contacts have no role recorded. [1]"])
        # A counted signal cites the account. Each record is still its own unit, and what is not shown is counted.
        self.assertEqual([unit["record_ref"] for unit in units if unit["kind"] == "record"],
                         ["crm/accounts/acc-001", *(f"crm/contacts/con-00{number}" for number in range(1, 7)),
                          *(f"crm/opportunities/opp-00{number}" for number in range(1, 7))])
        self.assertLessEqual({"  CRM: 1 more opportunities were read and are not shown (6 in all).",
                              "  CRM: 1 more contacts were read and are not shown (6 in all)."}, set(card["NOT READ"]))


if __name__ == "__main__":
    unittest.main()
