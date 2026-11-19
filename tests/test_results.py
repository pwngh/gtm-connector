"""The measured results quoted in the README."""

from collections import Counter
from pathlib import Path
import re
import statistics
import tempfile
import unittest

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected, check_brief
from gtm_connector.runtime import Runtime


REFERENCE = re.compile(r"\[([^\]]+)\]")


def corruptions(text):
    """One bad line added to an otherwise complete brief, so only that line can cause the rejection."""
    lines = text.splitlines()
    references = sorted({ref for line in lines for ref in REFERENCE.findall(line)})

    def added(index, line):
        return "\n".join(lines[:index + 1] + [line] + lines[index + 1:]) + "\n"

    for index, line in enumerate(lines):
        own = REFERENCE.findall(line)
        other = next(ref for ref in references if ref not in own)
        head, separator, tail = line.partition(". [")
        yield "citations removed", added(index, re.sub(r" \[[^\]]+\]", "", line))
        yield "citation changed", added(index, line.replace(f"[{own[0]}]", f"[{other}]", 1))
        yield "wording changed", added(index, head + " and approved" + separator + tail)
        yield "line repeated", added(index, line)
    yield "uncited sentence added", text + "- The renewal was approved.\n"
    yield "cited invention added", text + f"- The renewal was approved. [{references[0]}]\n"


def cites_real_records(candidate, brief):
    """The comparison check: every line has a reference, and each reference names something that was read."""
    evidence = brief["evidence"]
    known = {row["record_ref"] for row in evidence["records"]}
    known |= {f"audit/{row['audit_id']}" for row in evidence["receipts"]} | {f"run/{brief['brief_id']}"}
    return all((cited := REFERENCE.findall(line)) and set(cited) <= known for line in candidate.splitlines())


class ResultsTests(unittest.TestCase):
    def test_the_readme_example_is_the_real_brief_for_acc_003(self):
        with tempfile.TemporaryDirectory() as directory, Runtime(Path(directory) / "state") as runtime:
            brief = create_brief(runtime, "acc-003", include_slack=True)
            receipts = [row for row in runtime.connector.audit.read() if row["brief_id"] == brief["brief_id"]]
        lines = brief["text"].splitlines()
        self.assertEqual(len(lines), 13)
        references = "[crm/opportunities/opp-003] [calls/call_notes/note-003] [slack/messages/msg-003]"
        for expected in (
                "- Renewal is in 31 days as of 2027-01-15. [crm/accounts/acc-003] [run/" + brief["brief_id"] + "]",
                '- CRM opportunity opp-003: stage "closed_won"; amount USD 25,500.00; close date 2027-01-10. '
                "[crm/opportunities/opp-003]",
                '- Three-source conflict: CRM opportunity opp-003 reports stage "closed_won", call note note-003 '
                "reports unapproved procurement, and Slack message msg-003 reports a delayed security review. "
                + references,
                "- Proposed follow-up: Ask the account owner to confirm procurement and security review status. "
                + references):
            self.assertIn(expected, lines)
        records = {row["record_ref"]: row for row in brief["evidence"]["records"]}
        self.assertEqual(len(records), 5)
        self.assertEqual(records["calls/call_notes/note-003"], {
            "id": "note-003", "account_id": "acc-003", "called_at": "2027-01-12T15:00:00Z",
            "body": "Procurement has not approved this renewal. The budget remains unsigned.",
            "record_ref": "calls/call_notes/note-003"})
        self.assertEqual(records["slack/messages/msg-003"]["body"],
                         "Security review is delayed. Procurement approval is still pending.")
        self.assertEqual([(row["tool"], row["status"], row["rows"]) for row in receipts], [
            ("get_account", "ok", ["crm/accounts/acc-003", "crm/contacts/con-003"]),
            ("list_opportunities", "ok", ["crm/opportunities/opp-003"]),
            ("read_call_notes", "ok", ["calls/call_notes/note-003"]),
            ("read_slack_messages", "ok", ["slack/messages/msg-003"])])
        self.assertEqual((brief["status"], brief["as_of"], brief["model_usage"]),
                         ("accepted", "2027-01-15", {"model": None, "input_tokens": 0, "output_tokens": 0}))

    def test_every_corrupted_draft_is_rejected_and_every_call_has_a_receipt(self):
        tried, rejected, caught_by_reference_check, size = Counter(), Counter(), Counter(), Counter()
        with tempfile.TemporaryDirectory() as directory, Runtime(Path(directory) / "state") as runtime:
            for number in range(1, 21):
                brief = create_brief(runtime, f"acc-{number:03d}", include_slack=True)
                self.assertEqual(check_brief(brief["text"], brief["evidence"]), brief["text"])
                lines = brief["text"].splitlines()
                size.update(records=len(brief["evidence"]["records"]), lines=len(lines),
                            words=len(brief["text"].split()),
                            signals=sum(line.startswith("- Signal: ") for line in lines),
                            cited_lines=sum(bool(REFERENCE.search(line)) for line in lines),
                            citations=sum(len(REFERENCE.findall(line)) for line in lines))
                for kind, candidate in corruptions(brief["text"]):
                    tried[kind] += 1
                    caught_by_reference_check[kind] += not cites_real_records(candidate, brief)
                    try:
                        check_brief(candidate, brief["evidence"])
                    except BriefRejected:
                        rejected[kind] += 1
            receipts = runtime.connector.audit.read()
        self.assertEqual(rejected, tried)
        self.assertEqual(dict(tried), {"citations removed": 229, "citation changed": 229, "wording changed": 229,
                                       "line repeated": 229, "uncited sentence added": 20,
                                       "cited invention added": 20})
        # The README comparison: 956 caught here, 249 by checking only that each line cites a real record.
        self.assertEqual(sum(tried.values()), 956)
        self.assertEqual(+caught_by_reference_check, Counter({"citations removed": 229, "uncited sentence added": 20}))
        # What goes in and what comes out, across the twenty briefs.
        self.assertEqual(dict(size), {"records": 98, "lines": 229, "words": 2579, "signals": 6,
                                      "cited_lines": 229, "citations": 261})
        # Twenty briefs make eighty tool calls. Each has one finished receipt.
        self.assertEqual([row["status"] for row in receipts], ["ok"] * 80)
        # Observed at a 1.6 ms median on a laptop. This bound only guards the order of magnitude.
        self.assertLess(statistics.median(row["elapsed_ms"] for row in receipts), 250)


if __name__ == "__main__":
    unittest.main()
