# Evaluation

```sh
gtm-connector evaluate
gtm-connector evaluate --json > evaluation.json
gtm-connector evaluate --with-slack --json
```

The evaluation checks which expected claims appear in renewal briefs and
whether each cites the right records.

- It briefs `acc-001` through `acc-020` in fresh temporary state. It does not
  touch the operator's CRM, and the default runs no model.
- The default makes 60 HTTPS tool calls and scores the 182 claims in
  [expected.json](../evaluation/expected.json). `--with-slack` makes 80 calls
  and scores 223. See [the three-source fixture](slack.md).
- `--json` prints the complete report, with per-account `cases`, as one object
  on stdout. The default prints a summary.
- A failed case or check exits `4`. The report still goes to stdout. Invalid
  arguments exit `2`. A failed model service preflight exits `5` with no
  report. See [exit codes](cli.md).

Progress goes to stderr before the first brief and after each of the twenty
cases, without account identifiers or source text. With `--json`, each line has
this shape. The first has `completed: 0` and `status: null`. Later values are
`accepted`, `rejected`, or `failed`.

```json
{"progress":{"command":"evaluate","completed":1,"total":20,"status":"accepted"}}
```

## Results

The tests assert these counts. Both runs use the as-of date `2027-01-15`.

| Metric | Default | `--with-slack` |
| --- | --- | --- |
| Accounts with accepted briefs | 20 / 20 | 20 / 20 |
| Facts found | 182 / 182 (100%) | 223 / 223 (100%) |
| Facts cited correctly | 182 / 182 (100%) | 223 / 223 (100%) |
| Facts invented | 0 / 182 emitted claims (0%) | 0 / 223 emitted claims (0%) |
| Rejected or failed briefs | 0 | 0 |
| Audit receipts | 60 | 80 |
| Activities created | 0 | 0 |
| Model input / output tokens | 0 / 0 | 0 / 0 |

Run `gtm-connector evaluate --json` to reproduce them. No model result exists
for the current fixture book.

## Scoring

A fact is one displayed claim line. A compound contact or opportunity statement
counts once.

- A fact is found when its expected text appears, even with a missing or wrong
  citation.
- A fact is cited correctly when its text appears with the exact reference set.
- A fact is invented when the emitted text is not among the expected claims.
- A duplicate fails the evaluation and adds no coverage.
- A failed or rejected brief keeps its expected claims in the denominator and
  publishes no text to score.

The score compares published fixture output with fixed wording. It cannot judge
model reasoning or paraphrase, or tell whether a source is true. See
[briefs and citations](briefs.md).

## Local model comparison

`evaluate --adapter ollama --model NAME --json` uses the same accounts and
claims and adds these fields. See [the local-model run](ollama.md).

| Field | Meaning |
| --- | --- |
| Top-level scores | Published text only. A rejected brief publishes none of its lines, and its expected claims stay in the denominator. `invention_rate` is `null` when nothing was published. |
| `candidate` | Scores of the returned lines before the publication check. Its `scored_briefs` and `unscored_briefs` show missing or malformed candidates. Rejected text is neither saved nor reported. |
| `publication_gate_passed` | The publication and read-only checks passed. `status` also requires full coverage. |
| `model_usage`, `model_metadata` | Summed token counts, including rejected generations, and the model digest and service version. A missing count makes its total `null`. |
