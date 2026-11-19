# Deal cards

A Deal card is what the connector prints about one account before a renewal conversation. The
connector writes every character of it. No model takes part.

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack
.venv/bin/python -m gtm_connector trace brief-REPLACE_WITH_ID 5
```

| Option | Meaning |
| --- | --- |
| `--without-calls` | Omits the `calls:read` grant. The card says calls were not read. |
| `--with-slack` | Adds a fourth read under `slack:read`. See [Slack context](slack.md). |
| `--as-of YYYY-MM-DD` | Sets the planning date. Fixtures default to `2027-01-15`. |
| `--json` | Prints the saved result with its `units`. |

Each `brief` starts local services, calls `get_account`, `list_opportunities`, and
`read_call_notes` in that order, then stops the services. It requests `crm:read` and `calls:read`,
never `crm:write`.

## Units

Evidence becomes numbered units in a fixed order: the account, its contacts, contracts, and
opportunities by ID, then call text by time, then Slack messages by time. A CRM record is one unit.
Call and Slack text is one unit for each whole sentence. Every number on a card is one of these, and
`trace` resolves it to the source record, the receipt of the read that returned it, and, for a
sentence, its neutralized text, its speaker, and the SHA-256 of the source text.

## Sections

| Section | What it holds |
| --- | --- |
| `ATTENTION` | Signals derived from record fields and dates. Omitted when there are none. |
| `RECORD VALUES` | Typed CRM fields and connector arithmetic. An empty field prints `Missing`. An amount prints a currency only when the record carries one. At most five opportunities, contracts, and contacts are shown, and the rest are counted under `NOT READ`. |
| `SAID` | Whole sentences, newest record first, each with a role, its basis, the time, and the speaker label. At most 12 are shown. A sentence over 300 characters is counted and not shown, because a quotation is never cut. A figure inside a sentence stays there and never becomes a record value. |
| `NOT READ` | What each source did not return or was not asked for, the fields that are empty, how many sentences are not shown, how many speakers have no role, and what was neutralized. |

## Signals

| Signal | When |
| --- | --- |
| The renewal is in N days and this opportunity is still open | The opportunity is not closed and the renewal, or the Salesforce contract end, is 30 days away or less. More than five are counted in one line. |
| The renewal date passed N days ago | The demo CRM's renewal date is before the planning date. |
| The latest contract end date was N days ago | Salesforce returned no contract ending on or after the date, and at least one earlier end date. |
| CRM marks this account at risk | The demo CRM's risk status is `at_risk`. |
| This contact has no role recorded | The contact has no title. More than five are counted in one line. |
| The latest dated call that was read is N days old | The newest call with a known time is more than 60 days old, counted from its UTC date. |

## Speaker roles

A role comes from identity, never from what a sentence says about itself.

| Role | When |
| --- | --- |
| `seller` | The speaker label or Slack author equals an entry in `sellers.json` in the state folder: `{"schema_version": 1, "sellers": ["a name or Slack ID"]}`. Demo data lists its own invented sellers. |
| `customer` | A call's speaker label equals the name of exactly one contact of the account, and no listed seller. Slack authors are never customers. |
| `unknown` | Everything else: no label, a label that matches nobody, two contacts with the name, or a name shared by a seller and a contact. |

A report of what someone else said keeps the role of the person reporting it. In a WebVTT transcript
the label is the voice tag at the start of a cue, or a cue identifier that is not a number.

## Neutralized text

Source text is neutralized once, before anything displays it. A Slack mention becomes
`[mention channel]`, `[user U123]`, or `[channel C123]`. A link becomes its label and
`[link host]`. A control or direction-changing character becomes `[U+202E]`. Letters in any script
are kept. A quotation is exact against this text, and `trace` gives the SHA-256 of the original.

## Saved result

Each card and its evidence are saved in `var/v2/briefs/<brief_id>.json`, with `schema_version`
(`2`), `brief_id`, `account_id`, `as_of`, `status`, `sources`, `text`, `units`, and `evidence`.
`evidence.receipts` holds one receipt per read. The snapshot preserves the records read, not later
CRM changes. Evidence that fails a check gives no card and saves nothing.

## Errors

| [Exit](cli.md) | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | A running connector holds the state folder. | Stop the server or pass another `--state-dir`. |
| 2 | `usage_error` | The account ID, date, brief ID, or number is invalid. | Use an ID such as `acc-003`, a `YYYY-MM-DD` date, and a number printed on the card. |
| 3 | `refused` | An account has no imported transcript, or the state folder belongs to another source. | See [Salesforce](salesforce.md) and [transcript imports](calls.md). |
| 4 | `check_failed` | The evidence failed a check, so no card was produced. | Repeat the command. If it repeats, inspect the audit receipts. |
| 5 | `upstream_failure` | A read failed or the account was not found. | Check the account ID with `accounts`, then retry. |
