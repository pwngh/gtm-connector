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
| `--json` | Prints the card as `text` with its `units`. The raw records stay in the saved file. |

Each `brief` starts local services, calls `get_account`, `list_opportunities`, and
`read_call_notes` in that order, then stops the services. It requests `crm:read` and `calls:read`,
never `crm:write`.

## Units

Evidence becomes numbered units in a fixed order: the account, its contacts, contracts, and
opportunities by ID, then call text by time, then Slack messages by time. A CRM record is one unit.
Call and Slack text is one unit for each whole sentence, and a call or message with no sentence is
one unit, so every record that was read has a number. Every number on a card is one of these, and
`trace` resolves it to the source record, the receipt of the read that returned it, and, for a
sentence, its neutralized text, its speaker, and the SHA-256 of the record's text as stored.

A saved card is sealed. Saving it commits one more receipt, `save_brief`, that holds the SHA-256 of
the saved file: the evidence, the units, the card text, the seller list, and the date. `trace`
reads the file, and refuses it unless that receipt is finished and names the digest of the file as
it is now, and the receipts of the card's reads are the ones in the audit log. A changed amount,
sentence, speaker, seller, date, or card text does not trace. A card keeps the numbers it was sealed
with; a card saved before sealing says so and has to be made again. The owner of the state folder
can still rewrite the file and the audit log together: the log is not tamper-proof.

## Sentences

A sentence ends at closing punctuation or a blank line. A single line break inside a call note or
a cue is a wrap, so a sentence written over two lines stays whole. In a Slack message each line is
the author's own, so a line break ends a sentence there. A period after a title, a legal or
state short form, a list marker, or a dotted short form such as "e.g." does not end a sentence, so
"28 U.S.C. § 1332" and "Fed. R. Civ. P. 26(b)(3)" stay in one piece. A firm suffix or a capitalized
initialism ("LLP.", "U.S.A.") ends one only before a word that opens a sentence. Where the text does not settle it, two sentences
stay joined: a fragment is never shown as a sentence.

Two transcript cues join into one run only when they have the same speaker label and the second
starts within two seconds of the first, or within ten when the first stops mid-sentence and the
second goes on in lower case. Cues or lines with no label never join. In a transcript, a run that
stops mid-sentence is counted and not shown. So is what may be its other half: the start of that
speaker's next run, the next words of anyone when they go on in lower case, and each speaker's
first words in an excerpt that starts inside a call. The end of a Slack message or a call
note is an end. A sentence over 300 characters is counted and not shown, because a quotation is
never cut.

## Sections

| Section | What it holds |
| --- | --- |
| `ATTENTION` | Signals derived from record fields and dates. Omitted when there are none. |
| `RECORD VALUES` | Typed CRM fields and connector arithmetic. An empty field prints `Missing`. An amount prints a currency only when the record carries one. At most five opportunities, contracts, and contacts are shown, and the rest are counted under `NOT READ`. |
| `SAID` | Whole sentences, each with a role, its basis, the time, and the speaker label: the customer's first, then the seller's, then those of no established role, newest record first within each. At most 12 are shown, and the places are dealt one at a time to each role that still has a sentence, so sixty messages from a seller cannot push the customer's one sentence off the card. `NOT READ` counts what is not shown, by role. When sentences were read and none can be shown, the section says so. A figure inside a sentence stays there and never becomes a record value. |
| `NOT READ` | What each source did not return or was not asked for, the fields that are empty, how many sentences are not shown, how many speakers have no role, and what was neutralized. |

## Signals

| Signal | When |
| --- | --- |
| The renewal is in N days and this opportunity is still open | The opportunity is not closed and the renewal, or the Salesforce contract end, is 30 days away or less, today, or past. More than five are counted in one line that cites the first five and ends `and N more`. |
| The renewal date passed N days ago | The demo CRM's renewal date is before the planning date and no opportunity is open. |
| The latest contract end date was N days ago | Salesforce returned no contract ending on or after the date, and at least one earlier end date. |
| CRM marks this account at risk | The demo CRM's risk status is `at_risk`. |
| This contact has no role recorded | The contact has no title. More than five are counted in one line that cites the first five and ends `and N more`. |
| The latest dated call that was read is N days old | The newest call with a known time is more than 60 days old, counted from its UTC date. |

## Speaker roles

A role comes from identity, never from what a sentence says about itself.

| Role | When |
| --- | --- |
| `seller` | The speaker label or Slack author equals an entry in `sellers.json` in the state folder: `{"schema_version": 1, "sellers": ["a name or Slack ID"]}`. Demo calls and demo Slack also count the demo's own invented sellers. An imported transcript or live Slack never does. |
| `customer` | A call's speaker label equals the name of exactly one contact of the account, and no listed seller. Slack authors are never customers. |
| `unknown` | Everything else: no label, a label that matches nobody, two contacts with the name, or a name shared by a seller and a contact. |

A report of what someone else said keeps the role of the person reporting it. In a WebVTT transcript
the label is a voice tag, or a cue identifier that is not a number. A cue with two voice tags gives
each span its own speaker, and text after a closing tag belongs to the cue again. The tag is the
label and is not part of the sentence.

## Neutralized text

Source text is neutralized once, before anything displays it. That covers record fields, account
names, speaker labels, and times as well as call and Slack text.

- A Slack mention becomes `[mention channel]`, `[user U123]`, or `[channel C123]`.
- A link of any scheme, a `//host` address, or a `www.` address becomes its label and `[link host]`.
- A control, direction-changing, or invisible format character becomes `[U+202E]`.
- A bracketed number such as `[3]` becomes `(3)`, because a card cites its units that way.

Letters in any script are kept. A quotation is exact against this text, and `trace` gives the
SHA-256 of the record's stored text. The account name in the heading is a quoted literal on one line.

## Saved result

Each card and its evidence are saved in `var/v2/briefs/<brief_id>.json`, with `schema_version`
(`2`), `brief_id`, `account_id`, `as_of`, `status`, `sources`, `text`, `units`, and `evidence`.
`brief --json` prints every key but `evidence`, which holds the raw records and one receipt per
read. The snapshot preserves the records read, not later CRM changes. Evidence that fails a check
gives no card and saves nothing.

## Errors

| [Exit](cli.md) | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | A running connector holds the state folder. | Stop the server or pass another `--state-dir`. |
| 2 | `usage_error` | The account ID, date, brief ID, or number is invalid, or a saved card does not match the audit log. | Use an ID such as `acc-003`, a `YYYY-MM-DD` date, and a number printed on the card. Create the card again if its file was changed. |
| 3 | `refused` | An account has no imported transcript, or the state folder belongs to another source. | See [Salesforce](salesforce.md) and [transcript imports](calls.md). |
| 4 | `check_failed` | The evidence failed a check, so no card was produced. | Repeat the command. If it repeats, inspect the audit receipts. |
| 5 | `upstream_failure` | A read failed or the account was not found. | Check the account ID with `accounts`, then retry. |
