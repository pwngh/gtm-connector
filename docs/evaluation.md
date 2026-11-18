# Evaluation

The evaluation measures which expected claims appear in renewal briefs and
whether they cite the right records. Rejected drafts receive no credit for
unpublished text.

Evaluation runs from the repository root after setup:

```sh
.venv/bin/python -m gtm_connector evaluate
```

The command prints a readable summary. Add `--json` for the complete report,
including the per-account `cases` and model-usage fields described below:

```sh
.venv/bin/python -m gtm_connector evaluate --json
```

The default evaluation makes 60 HTTPS tool calls for `acc-001` through `acc-020` in fresh
temporary state. It neither reads nor changes the operator's CRM. The default
runs no model.
[expected.json](../evaluation/expected.json) defines 182 expected claims and
their references separately from the drafting code. The collected evidence
supplies only the generated run and audit IDs.

`--with-slack` includes account-channel context. It makes 80 tool calls and adds
fixed Slack expectations, preserving the original 182 claims. See
[the three-source fixture](slack.md).

## Progress

Evaluation writes progress to stderr before the first brief and after each of
the twenty cases. Progress contains only completed and total counts and the
case outcome, without account identifiers or source text. The default display
is a line such as `Evaluating briefs: 1/20 (accepted)`.

With `--json`, each stderr progress line has this shape:

```json
{"progress":{"command":"evaluate","completed":1,"total":20,"status":"accepted"}}
```

The initial line has `completed: 0` and `status: null`. Later status values are
`accepted`, `rejected`, or `failed`. Stdout still contains one complete JSON
report, so it can be redirected independently:

```sh
gtm-connector evaluate --json > evaluation.json
```

If evaluation fails, its final stderr error object can follow progress lines.
A preflight failure can stop before any progress or report. Consumers of stderr
must distinguish `progress` from `error`; stdout keeps the report shape below.
[Progress tests](../tests/test_progress.py) check the initial and completed
notifications and preserve the report when no callback is supplied.

## Recorded results

The fixture adapter produced these results on 2026-11-17 with as-of date `2027-01-15`:

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

[RESULTS.json](../evaluation/RESULTS.json) records the output and counts for each account.
The checked-in reports are historical runs from before explicit `--json` was
required. Their stored commands and results remain unchanged.

## How scoring works

For scoring, a fact is a displayed claim line. Compound contact and opportunity
statements count once:

- Facts found: expected text appears, even if its citation is missing or wrong.
- Facts cited correctly: expected text appears with the exact reference set.
- Facts invented: emitted text is not in the expected claims.

Duplicates fail evaluation without increasing coverage. Failed or rejected
briefs keep their expected claims in the denominator but publish no text to
score for invention. Any failed case or required check makes the CLI exit `4`.
The report still goes to stdout, and an error with a recovery command goes to
stderr. Invalid arguments exit `2`; a service preflight failure exits `5` before
a report is produced. See [CLI output and exit codes](cli.md).

Scorer tests cover missing facts, missing or wrong references, invented or
appended text, and duplicates. The score measures published fixture output
against fixed wording. It cannot assess model reasoning or arbitrary paraphrases,
or establish whether a source is true.

[Briefs and citations](briefs.md) explains the checker and its limits.

## Optional local model comparison

`evaluate --adapter ollama --model NAME --json` uses the same twenty accounts and
expected claims. Top-level scores describe published text. Before the gate runs,
`candidate` scores actual returned lines in memory. It counts uncited or
unsupported lines, duplicates, and omitted required disclosures. Unsupported
paraphrases need not be false. Rejected text is neither saved nor reported.

`scored_briefs` and `unscored_briefs` expose missing or malformed candidates.
Candidate totals cover only scored candidates. Published metrics keep every
selected expected claim in the denominator. A rejected brief publishes none of
its lines, even valid ones. Zero published lines means an undefined (`null`)
invention rate.

`publication_gate_passed` records publication and read-only checks. `status`
still requires full coverage and exits `4` otherwise.

`model_usage` sums service counts, including rejected generations; a missing
count makes that dimension unknown. `model_metadata` records the selected model
digest and service version. For reproduction, `run` records the command and UTC
start/end times. See [the local-model run](ollama.md).
