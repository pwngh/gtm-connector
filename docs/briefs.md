# Briefs and citations

A renewal brief summarizes an account before a contract renewal conversation.
Its claims cite saved records and must pass the checker before publication.
Citations identify sources; they do not prove those sources are true.

These brief commands run from the repository root after setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector brief acc-001 --without-calls
.venv/bin/python -m gtm_connector brief acc-004
```

Each command starts local services, calls `get_account`, `list_opportunities`,
and `read_call_notes` in order, then stops the services. The default workflow
requests `crm:read` and, unless `--without-calls` is set, `calls:read`. It never
requests `crm:write`. Draft text cannot change the sequence or trigger another call.

The default draft uses fixed wording from the invented data.
[Optional Ollama drafting](ollama.md) must pass the same check before publication.

Only a recognized call-note scope denial allows partial success by default.
Slack mode also permits its matching `slack:read` scope denial. Other HTTP
errors, transport failures, failed tools, or incomplete evidence stop the brief;
they cannot be presented as an unavailable source.

The `--as-of YYYY-MM-DD` option replaces the fixed demo date. `--json` prints
the full saved result, and `--state-dir PATH` selects another state directory.

The `--with-slack` option adds a fourth read under `slack:read`, a three-source
conflict flag, and a cited `proposed_follow_up`. This suggested message to the account
owner is checked but never sent. The default three-read workflow stays unchanged.
See [Slack context](slack.md).

## What the citations mean

Claims cite source records. Without `calls:read`, the brief says notes were
unavailable; empty notes or opportunities produce a statement that none were
returned. These outcomes cite the audit receipt because there is no record to
cite. Renewal-day calculations also cite the saved run date.

Conflicting CRM fields and notes remain separate claims with their own sources.
Note text is quoted as untrusted data, including any instructions, and newlines
stay visibly escaped. The injected note does not cause the workflow to create
an activity.

These lines come from `brief acc-003 --json`:

```text
- CRM opportunity opp-003: stage "closed_won"; amount USD 25,500.00; close date 2027-01-10. [crm/opportunities/opp-003]
- Untrusted call note note-003 at "2027-01-12T15:00:00Z" says "Procurement has not approved this renewal. The budget remains unsigned.". [calls/call_notes/note-003]
- Source conflict: CRM opportunity opp-003 reports stage "closed_won", while call note note-003 reports unapproved procurement. [crm/opportunities/opp-003] [calls/call_notes/note-003]
```

The flag is required when `closed_won` appears with that exact note body for the
same account. Both records must be cited; their disagreement stays unresolved.
The rule does not infer conflicts from other wording or unreadable notes. It is
not a general contradiction detector. Regenerate older `acc-003` briefs without
the flag: `check-brief` applies current rules to saved evidence.

## Inspect the saved result

```sh
.venv/bin/python -m gtm_connector brief acc-003 --json
```

The JSON includes `schema_version`, `brief_id`, `account_id`, `as_of`, and
`adapter`, followed by `model_usage`, `status`, `evidence`, and `text`.
This fixture run returns:

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

`evidence.receipts` holds the three read receipts; `text` holds the checked brief.
Brief and audit IDs change each run, while fixture record IDs stay stable.

## Check another draft

The workflow saves accepted briefs and their evidence in
`var/v2/briefs/<brief_id>.json`. Replace `brief-REPLACE_WITH_ID` with the generated
brief ID to inspect its receipts or check a text file against the same evidence:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt
```

The cited bullet lines from the JSON `text` field belong in `candidate.txt`. Leave
out the CLI's `Brief:` and `As of:` display labels. Checking the original text
exits `0` and returns JSON with `status: "valid"` and the submitted `brief_id`.
Append `The renewal was approved.` and the same check exits `2`, prints nothing
to stdout, and writes this error to stderr:

```text
error: Brief contains an uncited, unsupported, or misattributed statement.
```

The checker accepts fixed supported claims with exact record references.
Uncited sentences fail, as do invented values or wrong citations. Appended claims
and direct tool requests also fail. It cannot assess arbitrary paraphrases or
establish semantic entailment.

A passing brief can still omit supported facts; the
[evaluation](evaluation.md) measures that coverage separately. Unavailable or
empty notes and recognized conflicts must always remain visible.

## Evidence and storage limits

The checker keeps the original evidence snapshot. The drafting adapter gets a
copy without owner credentials or reusable bearer tokens. Rejected text is
neither printed nor saved. Owner-only files record `collected`, `accepted`, or
`rejected` status.

Snapshots preserve the records read, not later CRM changes, and are not
tamper-proof. Saves write a private temporary file and replace the snapshot
atomically. A failed replacement leaves the previous complete snapshot, possibly
with `collected` status. An initial save failure prevents drafting; a failed
final save can leave model usage unknown.

There is no power-loss durability guarantee. A killed process can leave a private
temporary file. Retention is manual, as described in
[Audit, rate limits, and writes](audit.md). [Persistence tests](../tests/test_brief_persistence.py)
cover interrupted writes and replacement failures.

The fixture records zero model tokens. Ollama records the service's reported
input and output counts even when a generation is rejected. Missing counts
stay unknown. A custom adapter returning only a string also leaves usage unknown;
a `DraftResult` can carry reported counts.

Custom Python adapters are not sandboxed, and the CLI cannot load one. There is
no hosted model adapter. Fixture checks do not establish how an untested model
would handle injected instructions.
