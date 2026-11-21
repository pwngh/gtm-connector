# Deal cards

A Deal card is what the connector prints about one account before a renewal conversation. The
connector writes every character of it. No model takes part.

```sh
.venv/bin/python -m gtm_connector connect demo
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector inspect brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector inspect brief-REPLACE_WITH_ID 5
```

A card reads the sources its state folder is [connected](cli.md) to, and `brief` takes no source
option.

| Option | Meaning |
| --- | --- |
| `--without-calls` | Omits the `calls:read` grant. The card says calls were not read. |
| `--as-of YYYY-MM-DD` | Sets the planning date: the UTC day the card counts from. With no flag it is today in UTC, or `2027-01-15` on the demo CRM. A sentence of a call or message dated after it is counted and not shown. The date is the record's own time read in UTC, not a cue's place in a recording, and a time with no offset decides nothing. It is not a view of the past: the records are the ones read now, and the card's second line says when. |
| `--json` | Prints the card as `text` with its `units` and `gaps`. The raw records stay in the saved file. |

Each `brief` starts local services, calls `get_account` and `list_opportunities`, then
`read_call_notes` and `read_slack_messages` for each of the two that is connected, in that order,
and stops the services. It requests `crm:read`, `calls:read`, and `slack:read`, never `crm:write`.
A source that is not connected is never called, and the card says "Calls: not connected." or
"Slack: not connected."

## Units

Evidence becomes numbered units in a fixed order: the account, its contacts, contracts, and
opportunities by ID, then call text in the order of its time as written and then its ID, then Slack
messages: demo messages in the order of their time as written, live messages in the order Slack
returns them. Times written with different UTC offsets sort by their text, not by the moment they name. A CRM record is one unit.
Call and Slack text is one unit for each whole sentence, and a call or message with no sentence is
one unit, so every record that was read has a number. Every number on a card is one of these.
`inspect BRIEF_ID` lists them all, one per line. `inspect BRIEF_ID N` resolves a number to the source
record, the receipt of the read that returned it, and, for a sentence, its neutralized text, its
speaker, the next sentence under the same speaker label in the same record, and the SHA-256 of the
record's text as stored.

A saved card is sealed. Saving it commits one more receipt, `save_brief`, that holds the SHA-256 of
the saved file: the evidence, the units, the card text, the seller list, and the date. `inspect`
reads the file, and refuses it unless that receipt is finished and names the digest of the file as
it is now, and the receipts of the card's reads are the ones in the audit log. A changed amount,
sentence, speaker, seller, date, or card text is refused. A card keeps the numbers it was sealed
with: `inspect` answers from the sealed units and rebuilds nothing, so a later change to the rules
cannot move or refuse them. It shows a sentence as sealed, and neutralizes one sealed under older
neutralizer rules again under today's before showing it. A card saved
before sealing says so and has to be made again. A save that does not finish leaves no file, and
its receipt ends as an error; after a hard stop the receipt stays `pending` and names the card in
`arguments`. The owner of the state folder can still rewrite the file and the audit log together:
the log is not tamper-proof.

A record unit has `n`, `kind`, `source`, `record_ref`, and `tool`. A sentence unit adds `at`,
`speaker`, `role`, `basis`, `text`, `partial`, `raw_sha256`, `sanitizer_version`, and three more.
`contact` is the number of the one contact a customer's label equals, or `null`. `shown` says
whether the sentence is under `SAID`: `yes`, or why not: `cut` (not a whole sentence), `later`
(its record is dated after the planning date), `long` (over 300 characters), `more` (whole, and past the 12
places). Exactly one holds, in that order. `span` is `null` except in an imported
transcript, where it names the first and last cue or line the sentence lies in and their times.
The times of a `span` cover every cue the sentence lies in, whatever order the file wrote them in.
`gaps` holds one entry for each line under `NOT READ`, in order: a `code` from a fixed list, a
`disposition` (`not_read`, `read_not_shown`, or `note`), a `count`, `by_role`, and the line itself
as `text`. `count` is how many records or sentences the line says were read and are not shown, how
many its note is about, or how many files, attachments and blocks of a Slack read it says were not
read; it is `null` when the line gives no such number. `by_role` is that count by speaker role, for
a line that counts sentences. The codes are the keys of `GAP_CODES` in `gtm_connector/units.py`. A gap has no
number. Read a unit's `kind` before any other key, and name a gap by its `code`, never by its
text. A card sealed by an earlier version keeps the keys it was sealed with: `inspect` shows it
without `contact`, `shown`, or `span`, and says `not recorded` for whether a sentence is shown.

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
second goes on in lower case. Cues or lines with no label, or with no times, never join. In a
transcript, a run that stops mid-sentence is counted and not shown. So is what may be its other
half: the start of that speaker's next run, the next words of anyone when they go on in lower
case, and each speaker's first words in an excerpt that starts inside a call. The end of a Slack
message or a call note is an end. A sentence over 300 characters is counted and not shown, because
a quotation is never cut.

## Sections

| Section | What it holds |
| --- | --- |
| First two lines | The account as a quoted literal, its ID, and the planning date; then when the sources were read, to the second, from the receipts of the reads that were allowed. |
| `ATTENTION` | Signals derived from record fields and dates. Omitted when there are none. |
| `RECORD VALUES` | Typed CRM fields and connector arithmetic. An empty field prints `Missing`. An amount prints a currency only when the record carries one. At most five opportunities, contracts, and contacts are shown, and the rest are counted under `NOT READ`. |
| `SAID` | Whole sentences under one line per role: the customer's first, then the seller's, then those of no established role. Each has its source, time, and speaker label. A customer's also has the title and number of the contact its label equals; one of no role says why. At most 12 are shown. The places are dealt one at a time to each role that still has a sentence, newest first (inside one call a later cue time is newer, and at one time the later sentence is), so sixty messages from a seller cannot push the customer's one sentence off the card. What is kept is printed in the order it was said. `NOT READ` counts what is not shown, by role. When sentences were read and none can be shown, the section says so. A figure inside a sentence stays there and never becomes a record value. |
| `NOT READ` | What each source did not return or was not asked for, the fields that are empty, how many sentences are not shown, how many speakers have no role, and what was neutralized. When call sentences carry speaker labels and none equals any contact's name, one line says so. |

## Signals

| Signal | When |
| --- | --- |
| The renewal is in N days and this opportunity is still open | The opportunity is not closed and the renewal, or the Salesforce contract end, is 30 days away or less, today, or past. More than five are counted in one line that cites the first five and ends `and N more`. |
| The renewal date passed N days ago | The demo CRM's renewal date is before the planning date and no opportunity is open. |
| The latest contract end date was N days ago | Salesforce returned no contract ending on or after the date, and at least one earlier end date. |
| CRM marks this account at risk | The demo CRM's risk status is `at_risk`. |
| This open opportunity's close date is after the renewal date | An opportunity that is not closed has a close date later than the renewal date, or than the Salesforce contract end the card counts from. More than five are counted in one line. |
| This contact has no role recorded | The contact has no title. More than five are counted in one line that cites the first five and ends `and N more`. |
| The latest dated call that was read is N days old | The newest call with a known time is more than 60 days old, counted from its UTC date. |

## Speaker roles

A role comes from a label, never from what a sentence says about itself. A label is what the
source wrote. It does not prove who spoke: another person of the same name, or a name typed into a
file, gets the same role.

| Role | When |
| --- | --- |
| `seller` | The speaker label or Slack author equals a seller you listed with `--seller NAME` on `connect salesforce`, `connect calls`, or `connect slack`. The list is saved as `sellers.json` in the state folder: `{"schema_version": 1, "sellers": ["a name or Slack ID"]}`, at most 200 entries of 1 to 200 characters. Demo calls and demo Slack also count the demo's own invented sellers. An imported transcript or live Slack never does. |
| `customer` | A call's speaker label equals the name of exactly one contact of the account, and no listed seller. Slack authors are never customers. |
| `unknown` | Everything else: no label, a label that matches nobody, two contacts with the name, or a name shared by a seller and a contact. |

A report of what someone else said keeps the role of the person reporting it. In a WebVTT transcript
the label is a voice tag, or a cue identifier that is not a number. A cue with two voice tags gives
each span its own speaker, and text after a closing tag belongs to the cue again. The tag is the
label and is not part of the sentence. In a transcript imported with
[`--speaker-names`](calls.md), the label is the name that opens each line, and a cue identifier
is not a label.

## Neutralized text

Source text is neutralized once, before anything displays it. That covers record fields, account
names, speaker labels, and times as well as call and Slack text.

- A Slack mention becomes `[mention channel]`, `[user U123]`, or `[channel C123]`.
- A link of any scheme, a `//host` address, or a `www.` address becomes its label and `[link host]`.
- A control, direction-changing, or invisible format character becomes `[U+202E]`.
- A bracketed number such as `[3]` becomes `(3)`, because a card cites its units that way.

Letters in any script are kept. A quotation is exact against this text, and `inspect` gives the
SHA-256 of the record's stored text. The account name in the heading is a quoted literal on one line.

## Saved result

Each card and its evidence are saved in `briefs/<brief_id>.json` in the state folder, with `schema_version`
(`3`), `brief_id`, `account_id`, `as_of`, `status`, `rules_version`, `sealed_by`, `sources`, `text`,
`units`, `gaps`, and `evidence`.
`brief --json` prints every key but `evidence`, which holds the raw records and one receipt per
read. The snapshot preserves the records read, not later CRM changes. Evidence that fails a check
gives no card and saves nothing.

## Errors

| [Exit](cli.md) | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | A running connector holds the state folder. | Stop the server or pass another `--state-dir`. |
| 2 | `usage_error` | The account ID, date, brief ID, or number is invalid, or a saved card does not match the audit log. | Use an ID such as `acc-003`, a `YYYY-MM-DD` date, and a number printed on the card. Create the card again if its file was changed. |
| 3 | `refused` | The state folder belongs to another source. An account with no imported transcript still gets a card, which states the gap. | See [Salesforce](salesforce.md) and [transcript imports](calls.md). |
| 4 | `check_failed` | The evidence failed a check, so no card was produced. | Repeat the command. If it repeats, inspect the audit receipts. |
| 5 | `upstream_failure` | A read failed or the account was not found. | Check the account ID with `accounts`, then retry. |
