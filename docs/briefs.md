# Briefs and citations

A renewal brief summarizes an account before a renewal conversation. Every line cites saved
evidence and must pass the checker before it is printed.

```sh
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector brief acc-007 --with-slack
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt --json
```

| Option | Meaning |
| --- | --- |
| `--without-calls` | Omits the `calls:read` grant. The brief states that notes were unavailable. |
| `--with-slack` | Adds a fourth read under `slack:read` and one cited `Proposed follow-up` line, which is checked but never sent. See [Slack context](slack.md). |
| `--as-of YYYY-MM-DD` | Sets the planning date. Fixtures default to `2027-01-15`. |
| `--json` | Prints the full saved result. |
| `--adapter ollama --model NAME` | Drafts with a local model. See Local model drafting below. |

Each `brief` starts local services, calls `get_account`, `list_opportunities`, and
`read_call_notes` in that order, then stops the services. It requests `crm:read` and `calls:read`,
never `crm:write`. The default draft uses fixed wording and no model.

## Signals

A brief opens with what needs attention. Each signal is one derived fact from structured fields and
dates, never from reading source text. It cites its records. A draft that omits one is rejected,
and signals are published first wherever a draft puts them.

| Signal | When |
| --- | --- |
| `renewal is in N days and opportunity X is still open at stage S` | Demo CRM: the opportunity is not closed and the renewal is 30 days away or less. It reads `renewal is due today` on the day and `renewal was N days ago` after it. |
| `contract end is in N days and opportunity X is still open at stage S` | Salesforce: the selected contract ends in 30 days or less, or `is today`, and the opportunity is open. Every open opportunity on the account counts. A contract that has ended is never selected. |
| `CRM marks this account at risk` | The demo CRM's risk status is `at_risk`. Its reason follows when present. |
| `contact NAME has no role recorded` | The contact has no title. |
| `the latest call note X is N days old` | The newest call note with a known time is more than 60 days old, counted from its UTC date. It reads `latest dated` when another note has no usable time, and `the selected call excerpt` for an imported transcript. |

## Citation rules

- A fact cites its source record. A renewal-day count also cites the saved run.
- A source that was denied for a missing scope, or that returned nothing, is stated in a line that
  cites the audit receipt of that read.
- Only a `calls:read` or `slack:read` scope denial allows a partial brief. Any other failed read
  or incomplete evidence stops the brief.
- Note and message text is quoted as untrusted data with newlines escaped.
- Conflicting records stay separate lines with their own sources and timestamps. The brief does
  not decide which is current.
- An empty contact role is printed as `not provided`.

A `Source conflict` line citing both records is required when a `closed_won` opportunity appears
with the exact fixture note body about unapproved procurement for the same account. The rule does
not infer conflicts from other wording. With `--with-slack`, `msg-003` adds a three-source line.

## Saved result and checking

Each brief and its evidence are saved in `var/v2/briefs/<brief_id>.json`, with `schema_version`
(`1`), `brief_id`, `account_id`, `as_of`, `adapter`, `model_usage`, `status`, `evidence`, and
`text`. `--with-slack` adds `proposed_follow_up`. `evidence.receipts` holds one receipt per read.
`status` is `collected`, `accepted`, or `rejected`. Rejected text is neither printed nor saved. A
snapshot preserves the records read, not later CRM changes.

To check a draft, put the cited bullet lines from the JSON `text` field in `candidate.txt`, without
the `Brief:` and `As of:` display labels. Checking the original text exits `0` with
`status: "valid"` and the `brief_id`. Append `The renewal was approved.` and the check exits `4`.

- The checker accepts only the fixed supported lines with their exact references. It cannot
  assess paraphrases.
- A passing brief can omit supported facts, which the evaluation below measures. Signals,
  lines for unavailable or empty sources, recognized conflicts, and the Slack follow-up cannot be
  omitted.

## Errors

| [Exit](cli.md) | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | A running connector holds the state folder, or the saved brief file is missing. | Stop the server or pass another `--state-dir`. Check the brief ID. |
| 2 | `usage_error` | The account ID, date, or option combination is invalid. | Use an ID such as `acc-003` and a `YYYY-MM-DD` date. |
| 3 | `refused` | A Salesforce account has no eligible Contract, an account has no imported transcript, or the state folder belongs to another source. | See [Salesforce](salesforce.md) and [transcript imports](calls.md). |
| 4 | `check_failed` | A draft or candidate has an uncited, unsupported, misattributed, or repeated line, or omits a required one. | Use only lines from the saved `text`, or create a new brief. |
| 5 | `upstream_failure` | A read failed, the account was not found, or the local model failed. | Check the account ID with `accounts`, then retry. |

## Evaluation

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
| Facts found | 188 / 188 (100%) | 229 / 229 (100%) |
| Facts cited correctly | 188 / 188 (100%) | 229 / 229 (100%) |
| Facts invented | 0 / 188 emitted claims (0%) | 0 / 229 emitted claims (0%) |
| Rejected or failed briefs | 0 | 0 |
| Audit receipts | 60 | 80 |
| Activities created | 0 | 0 |
| Model input / output tokens | 0 / 0 | 0 / 0 |

### Scoring

- A fact is one displayed claim line. A compound contact or opportunity statement counts once.
- Found: its expected text appears, even with a missing or wrong citation.
- Cited correctly: its text appears with the exact reference set.
- Invented: the emitted text is not among the expected claims.
- A duplicate fails the evaluation and adds no coverage.
- A failed or rejected brief keeps its expected claims in the denominator.
- The score compares fixed wording. It cannot judge paraphrase or tell whether a source is true.

### Local model fields

`evaluate --adapter ollama --model NAME --json` adds these fields. See Local model drafting below.

| Field | Meaning |
| --- | --- |
| Top-level scores | Published text only. A rejected brief publishes none of its lines. `invention_rate` is `null` when nothing was published. |
| `candidate` | Scores of the returned lines before the publication check. Its `scored_briefs` and `unscored_briefs` show missing or malformed candidates. Rejected text is neither saved nor reported. |
| `publication_gate_passed` | The publication and read-only checks passed. `status` also requires full coverage. |
| `model_usage`, `model_metadata` | Summed token counts, including rejected generations, and the model digest and service version. A missing count makes its total `null`. |

## Local model drafting

```sh
gtm-connector brief acc-003 --adapter ollama --model llama3.2:latest --json
gtm-connector evaluate --adapter ollama --model llama3.2:latest --json > ollama-evaluation.json
```

With `--adapter ollama`, a local model assembles the renewal brief, and the citation checker must
accept the draft before it is published. On the fixture book, `llama3.2` drafted 20 of 20 accepted
briefs with 188 of 188 claims, from 9,067 tokens in and 5,829 out.

- You need a running Ollama 0.30.0 or newer and a model that `ollama list` already shows.
- The adapter calls only `http://127.0.0.1:11434`. It does not download a model, fall back to
  fixture output, or call a hosted model.
- `--model` is required with `--adapter ollama` and is an error without it.
- An unavailable service, a missing model, or rejected model metadata exits `5` before any brief
  state is created.
- A rejected draft exits `4`. `brief` prints no candidate text. The counts and fixed error codes
  stay in the private saved artifact under `draft_diagnostics` and `model_usage`. `evaluate` still
  writes its report. See Evaluation above.

### What the model does

- The model receives the supported claims derived from authorized evidence and must return them
  with their exact wording and references in a JSON `lines` array. The prompt holds no owner
  credential, bearer token, audit fingerprint, or tool interface.
- An unsupported line, a duplicate, or an omitted signal or required disclosure rejects the whole brief. A
  supported subset can pass. The adapter does not repair or replace model output.
- Each request adds Ollama's `:local` selector, so the server rejects a tag that now points to a
  remote model. The adapter also rejects cloud suffixes and remote metadata, disables environment
  proxies, and follows no redirects. It cannot sandbox a malicious replacement service.
- The request uses temperature `0`, seed `0`, an 8192-token context, and a 4096-token generation
  limit. Generation has a 180-second read timeout and no retries.
- Token counts include rejected generations and are not billing measurements.
