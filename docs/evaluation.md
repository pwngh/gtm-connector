# Evaluation

After setup, run this from the repository root:

```sh
.venv/bin/python -m gtm_connector evaluate
```

The evaluation makes 60 HTTPS tool calls for `acc-001` through `acc-020` in fresh
temporary state. It does not read or change the operator's CRM and runs no model.
[expected.json](../evaluation/expected.json) defines 182 expected claims and
their references separately from the drafting code. Only generated run and
audit IDs come from the collected evidence.

## Recorded results

Measured on 2026-11-17 with the fixture adapter and as-of date `2027-01-15`:

| Metric | Result |
| --- | --- |
| Accounts with accepted briefs | 20 / 20 |
| Facts found | 182 / 182 (100%) |
| Facts cited correctly | 182 / 182 (100%) |
| Facts invented | 0 / 182 emitted claims (0%) |
| Rejected or failed briefs | 0 |
| Audit receipts | 60 |
| Activities created | 0 |
| Model input / output tokens | 0 / 0 |

See [RESULTS.json](../evaluation/RESULTS.json) for the recorded output and counts
for each account.

## How scoring works

One fact means one displayed claim line. A compound contact or opportunity
statement counts once.

- Facts found: expected text appears, even if its citation is missing or wrong.
- Facts cited correctly: expected text appears with the exact reference set.
- Facts invented: emitted text is not in the expected claims.

Duplicate claims do not increase coverage and fail the evaluation. Failed or
rejected briefs keep their expected claims in the denominator. They have no
published text to score for invention. The CLI exits `2` if any case or required
check fails.

Separate scorer tests cover missing facts, missing or wrong references,
invented text, appended claims, and duplicates. The score measures published
fixture output against the fixed vocabulary. It does not measure model
reasoning, assess arbitrary paraphrases, or establish whether a source is true.

See [Briefs and citations](briefs.md) for the checker and its limits.
