# Briefs and citations

A renewal brief summarizes an account before a renewal conversation. Every line
cites saved evidence and must pass the checker before it is printed. Citations
identify sources. They do not prove those sources are true.

## Commands

Run these from the repository root after setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector brief acc-001 --without-calls
.venv/bin/python -m gtm_connector brief acc-007 --with-slack
.venv/bin/python -m gtm_connector brief acc-003 --json
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt --json
```

| Option | Meaning |
| --- | --- |
| `--without-calls` | Omits the `calls:read` grant. The brief states that notes were unavailable. |
| `--with-slack` | Adds a fourth read under `slack:read` and one cited `Proposed follow-up` line, which is checked but never sent. See [Slack context](slack.md). |
| `--as-of YYYY-MM-DD` | Sets the planning date. Fixtures default to `2027-01-15`. |
| `--json` | Prints the full saved result. |
| `--state-dir PATH` | Selects another state folder. The default is `var/v2`. |
| `--adapter ollama --model NAME` | Drafts with a local model. See [Ollama drafting](ollama.md). |

Each `brief` starts local services, calls `get_account`, `list_opportunities`,
and `read_call_notes` in that order, then stops the services. It requests
`crm:read` and `calls:read`, never `crm:write`. Draft text cannot change the
sequence or trigger another call. The default draft uses fixed wording and no
model. `gtm-connector demo` runs the four-read workflow for `acc-007`.

## Citation rules

- A fact cites its source record. A renewal-day count also cites the saved run.
- A source that was denied for a missing scope, or that returned nothing, is
  stated in a line that cites the audit receipt of that read.
- Only a `calls:read` or `slack:read` scope denial allows a partial brief. Any
  other failed read or incomplete evidence stops the brief.
- Note and message text is quoted as untrusted data with newlines escaped. An
  instruction inside a note does not create an activity.
- Conflicting records stay separate lines with their own sources and
  timestamps. The brief does not decide which is current.
- An empty contact role is printed as `not provided`.

These lines are from the `text` field of `brief acc-003 --json`:

```text
- CRM opportunity opp-003: stage "closed_won"; amount USD 25,500.00; close date 2027-01-10. [crm/opportunities/opp-003]
- Untrusted call note note-003 at "2027-01-12T15:00:00Z" says "Procurement has not approved this renewal. The budget remains unsigned.". [calls/call_notes/note-003]
- Source conflict: CRM opportunity opp-003 reports stage "closed_won", while call note note-003 reports unapproved procurement. [crm/opportunities/opp-003] [calls/call_notes/note-003]
```

The conflict line is required when `closed_won` appears with that exact note
body for the same account. With `--with-slack`, the fixture message about a
delayed security review adds a three-source conflict line. The rule does not
infer conflicts from other wording. It is not a general contradiction detector.

## Saved result

Each brief and its evidence are saved in `var/v2/briefs/<brief_id>.json`. The
JSON contains `schema_version` (`1`), `brief_id`, `account_id`, `as_of`,
`adapter`, `model_usage`, `status`, `evidence`, and `text`. `--with-slack` adds
`proposed_follow_up`. `evidence.receipts` holds one receipt per read.

- `status` is `collected`, `accepted`, or `rejected`. Rejected text is neither
  printed nor saved.
- A fixture brief has `adapter: "fixture"`, a null model, and zero tokens.
  Ollama records the counts its service reports. Missing counts stay unknown.
- A snapshot preserves the records read, not later CRM changes. It is not
  tamper-proof.
- Saves replace the file atomically, without a power-loss durability guarantee.
  Retention is manual. See [audit, rate limits, and writes](audit.md).

## Check another draft

Put the cited bullet lines from the JSON `text` field in `candidate.txt`, without
the `Brief:` and `As of:` display labels. Checking the original text exits `0`
with `status: "valid"` and the `brief_id`. Append `The renewal was approved.`
and the check exits `4`, prints nothing to stdout, and writes this to stderr:

```json
{"error": {"code": "check_failed", "message": "Brief contains an uncited, unsupported, or misattributed statement.", "next": "gtm-connector check-brief --help"}}
```

- The checker accepts only the fixed supported lines with their exact
  references. It rejects uncited sentences, invented values, wrong citations,
  appended claims, and repeated lines. It cannot assess paraphrases.
- A passing brief can omit supported facts, which the [evaluation](evaluation.md)
  measures. Lines for unavailable or empty sources, recognized conflicts, and
  the Slack follow-up cannot be omitted.

## Errors

Errors go to stderr with a next command. With `--json` they are a JSON `error`
object with `code`, `message`, and `next`. See
[CLI output and exit codes](cli.md).

| Exit | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | A running connector holds the state folder, or the saved brief file is missing. | Stop the server or pass another `--state-dir`. Check the brief ID. |
| 2 | `usage_error` | The account ID, date, or option combination is invalid. | Use an ID such as `acc-003` and a `YYYY-MM-DD` date. |
| 3 | `refused` | A Salesforce account has no eligible Contract, an account has no imported transcript, or the state folder belongs to another source. | See [Salesforce](salesforce.md) and [transcript imports](calls.md). |
| 4 | `check_failed` | A draft or candidate has an uncited, unsupported, misattributed, or repeated line, or omits a required one. | Use only lines from the saved `text`, or create a new brief. |
| 5 | `upstream_failure` | A read failed, the account was not found, or the local model failed. | Check the account ID with `accounts`, then retry. |
