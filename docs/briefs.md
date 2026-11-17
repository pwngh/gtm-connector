# Briefs and citations

After setup, run these commands from the repository root:

```sh
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector brief acc-001 --without-calls
.venv/bin/python -m gtm_connector brief acc-004
```

Each command starts the local services, calls `get_account`,
`list_opportunities`, and `read_call_notes` in that order, then stops the
services. The workflow requests `crm:read` and, unless `--without-calls` is set,
`calls:read`. It never requests `crm:write`. Draft text cannot change the tool
sequence or trigger another call. A deterministic fixture drafts the text;
no model runs.

Use `--as-of YYYY-MM-DD` to replace the fixed demo date, `--json` to print the
full saved result, or `--state-dir PATH` to use another state directory.

## What the citations mean

Without `calls:read`, the brief says call notes were unavailable. For an account
with no notes, it says none were returned. Both statements cite the audit receipt
that records the outcome. Other claims cite their source records. Renewal-day
calculations also cite the saved run date.

The brief keeps conflicting CRM fields and notes as separate, attributed claims.
It quotes note text as untrusted data, including any instructions. Newlines stay
visibly escaped. The injected note does not cause the workflow to create an
activity.

These lines come from `brief acc-003 --json`:

```text
- CRM opportunity opp-003: stage "closed_won"; amount USD 25,500.00; close date 2027-01-10. [crm/opportunities/opp-003]
- Untrusted call note note-003 at "2027-01-12T15:00:00Z" says "Procurement has not approved this renewal. The budget remains unsigned.". [calls/call_notes/note-003]
- Source conflict: CRM opportunity opp-003 reports stage "closed_won", while call note note-003 reports unapproved procurement. [crm/opportunities/opp-003] [calls/call_notes/note-003]
```

The conflict rule requires `closed_won` and that exact note body for the same
account. The flag must cite both records and stay in the brief. It leaves the
disagreement unresolved. The rule does not infer conflicts from other wording
or from notes the caller cannot read. It is not a general contradiction detector.
Regenerate an older `acc-003` brief if it lacks the flag. `check-brief` applies
the current rules to the saved evidence.

## Inspect the saved result

```sh
.venv/bin/python -m gtm_connector brief acc-003 --json
```

The JSON contains `schema_version`, `brief_id`, `account_id`, `as_of`, `adapter`,
`model_usage`, `status`, `evidence`, and `text`. This fixture run returns:

| Field | Value |
| --- | --- |
| `schema_version` | `1` |
| `account_id` | `"acc-003"` |
| `as_of` | `"2027-01-15"` |
| `adapter` | `"fixture"` |
| `status` | `"accepted"` |
| `model_usage.model` | `null` |
| `model_usage.input_tokens` | `0` |
| `model_usage.output_tokens` | `0` |

`evidence.receipts` contains the three read receipts. `text` contains the checked
brief. Brief and audit IDs change each run; fixture record IDs stay stable.

## Check another draft

The workflow saves accepted briefs and their evidence in
`var/v2/briefs/<brief_id>.json`. Replace `brief-REPLACE_WITH_ID` with the generated
brief ID to inspect its receipts or check a text file against the same evidence:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt
```

Put the cited bullet lines from the JSON `text` field in `candidate.txt`. Leave
out the CLI's `Brief:` and `As of:` display labels. Checking the original text
exits `0` and returns JSON with `status: "valid"` and the submitted `brief_id`.
Append `The renewal was approved.` and the same check exits `2`, prints nothing
to stdout, and writes this error to stderr:

```text
error: Brief contains an uncited, unsupported, or misattributed statement.
```

The checker accepts a fixed vocabulary of supported claims and exact record
references. It rejects uncited sentences, invented values, wrong citations,
appended claims, and direct requests to run a tool. It cannot assess arbitrary
paraphrases or establish semantic entailment.

A brief can pass while leaving supported facts out. The
[evaluation](evaluation.md) measures coverage separately. Unavailable or empty
notes and recognized conflicts must always remain visible.

## Evidence and storage limits

The drafting adapter receives an evidence copy with no owner credentials or
reusable bearer tokens. The checker keeps the original snapshot. Rejected draft
text is neither printed nor saved. Saved files record `collected`, `accepted`,
or `rejected` status and use owner-only permissions.

Snapshots preserve the records read for that brief. They do not reflect later
CRM changes and are not tamper-proof. Writes are not atomic. An interruption
can leave partial JSON, so the operator must generate a new brief. Retention is
manual, as described in [Audit, rate limits, and writes](audit.md).

Custom Python adapters are not sandboxed, and the CLI cannot load one. The
fixture records zero model tokens. Custom-adapter usage stays unknown rather
than being estimated. A hosted model adapter is not implemented. These fixture
checks do not establish how an untested model would handle injected instructions.
