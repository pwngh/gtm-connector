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
    """Every way a draft can go wrong that the checker promises to catch, for every line."""
    lines = text.splitlines()
    references = sorted({ref for line in lines for ref in REFERENCE.findall(line)})

    def replaced(index, line):
        return "\n".join(lines[:index] + [line] + lines[index + 1:]) + "\n"

    for index, line in enumerate(lines):
        own = REFERENCE.findall(line)
        other = next(ref for ref in references if ref not in own)
        head, separator, tail = line.partition(". [")
        yield "citation removed", replaced(index, re.sub(r" \[[^\]]+\]", "", line))
        yield "citation changed", replaced(index, line.replace(f"[{own[0]}]", f"[{other}]", 1))
        yield "wording changed", replaced(index, head + " and approved" + separator + tail)
        yield "line repeated", "\n".join(lines[:index + 1] + [line] + lines[index + 1:]) + "\n"
    yield "uncited sentence added", text + "The renewal was approved.\n"
    yield "cited invention added", text + f"- The renewal was approved. [{references[0]}]\n"


class ResultsTests(unittest.TestCase):
    def test_every_corrupted_draft_is_rejected_and_every_call_has_a_receipt(self):
        tried, rejected, size = Counter(), Counter(), Counter()
        with tempfile.TemporaryDirectory() as directory, Runtime(Path(directory) / "state") as runtime:
            for number in range(1, 21):
                brief = create_brief(runtime, f"acc-{number:03d}", include_slack=True)
                self.assertEqual(check_brief(brief["text"], brief["evidence"]), brief["text"])
                lines = brief["text"].splitlines()
                size.update(records=len(brief["evidence"]["records"]), lines=len(lines),
                            words=len(brief["text"].split()),
                            cited_lines=sum(bool(REFERENCE.search(line)) for line in lines),
                            citations=sum(len(REFERENCE.findall(line)) for line in lines))
                for kind, candidate in corruptions(brief["text"]):
                    tried[kind] += 1
                    try:
                        check_brief(candidate, brief["evidence"])
                    except BriefRejected:
                        rejected[kind] += 1
            receipts = runtime.connector.audit.read()
        self.assertEqual(rejected, tried)
        self.assertEqual(dict(tried), {"citation removed": 223, "citation changed": 223, "wording changed": 223,
                                       "line repeated": 223, "uncited sentence added": 20,
                                       "cited invention added": 20})
        # What goes in and what comes out, across the twenty briefs.
        self.assertEqual(dict(size), {"records": 98, "lines": 223, "words": 2482, "cited_lines": 223,
                                      "citations": 248})
        # Twenty briefs make eighty tool calls. Each has one finished receipt.
        self.assertEqual([row["status"] for row in receipts], ["ok"] * 80)
        # Measured at a 1.6 ms median on a laptop. This bound only guards the order of magnitude.
        self.assertLess(statistics.median(row["elapsed_ms"] for row in receipts), 250)


if __name__ == "__main__":
    unittest.main()
