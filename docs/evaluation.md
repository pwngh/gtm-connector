# Evaluation

```sh
gtm-connector evaluate
gtm-connector evaluate --json > evaluation.json
gtm-connector evaluate --with-slack --json
```

The evaluation briefs `acc-001` through `acc-020` in fresh temporary state, as of `2027-01-15`,
and checks which claims in [expected.json](../evaluation/expected.json) appear with the right
citations. The default runs no model. `--json` prints the complete report with per-account
`cases`. A failed case or check exits `4` and still prints the report. A failed model service
preflight exits `5` with no report. The tests assert these counts:

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

## Scoring

- A fact is one displayed claim line. A compound contact or opportunity statement counts once.
- Found: its expected text appears, even with a missing or wrong citation.
- Cited correctly: its text appears with the exact reference set.
- Invented: the emitted text is not among the expected claims.
- A duplicate fails the evaluation and adds no coverage.
- A failed or rejected brief keeps its expected claims in the denominator.
- The score compares fixed wording. It cannot judge paraphrase or tell whether a source is true.

## Local model fields

`evaluate --adapter ollama --model NAME --json` adds these fields. See [Ollama](ollama.md).

| Field | Meaning |
| --- | --- |
| Top-level scores | Published text only. A rejected brief publishes none of its lines. `invention_rate` is `null` when nothing was published. |
| `candidate` | Scores of the returned lines before the publication check. Its `scored_briefs` and `unscored_briefs` show missing or malformed candidates. Rejected text is neither saved nor reported. |
| `publication_gate_passed` | The publication and read-only checks passed. `status` also requires full coverage. |
| `model_usage`, `model_metadata` | Summed token counts, including rejected generations, and the model digest and service version. A missing count makes its total `null`. |
