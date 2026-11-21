# Deal cards

A Deal card is what the connector prints about one account before a renewal conversation. The
connector writes every character of it, and no model takes part. Each figure and each quotation
carries a number that leads back to a stored record and the receipt of the read that returned it.

```sh
gtm-connector setup
gtm-connector demo
gtm-connector brief acc-003
gtm-connector inspect BRIEF_ID
gtm-connector inspect BRIEF_ID 5
```

The card in the [README](../README.md) is `acc-003`, Thornfield Wren LLP, from the demo book. A
brief is a saved card with its evidence, and its brief ID is `brief-` and 32 lowercase hex digits.

```text
source record ──▶ read, under a receipt ──▶ numbered unit ──▶ card text ──▶ saved card, sealed
```

[The CLI guide](cli.md) has every command, workspaces and exit codes. [Sources](sources.md) says how
each source is connected, and [receipts](audit.md) covers the audit log.

## What `brief` reads

A card reads the sources its [workspace](cli.md) is connected to. `brief` takes no source option.
It names them once on stderr, such as `Sources: CRM, calls, and Slack are demo data.`

Each `brief` makes these reads, each under its own receipt, committed before the read runs:

1. `get_account` and `list_opportunities`, from the CRM.
2. `read_call_notes`, when calls are connected.
3. `read_slack_messages`, when Slack is connected.

It asks only for the read scopes `crm:read`, `calls:read` and `slack:read`. A source that is not
connected is never called, and the card says `Calls: not connected.` or `Slack: not connected.` On a
real CRM, `brief` first reads the account once in a run of its own, under its own receipt, to record
it in the workspace.

After the card, `brief` prints its brief ID and the two `inspect` commands that list its units and
resolve one of its numbers.

| Option | Meaning |
| --- | --- |
| `--as-of YYYY-MM-DD` | The planning date: the UTC day the card counts from. Without it, the day is `2027-01-15` on the demo CRM and today in UTC on a real CRM. |
| `--without-calls` | Leaves `calls:read` out of the grant. The call read is refused with its receipt (HTTP 403), and the card says `Calls: not read. The grant lacks calls:read.` |
| `--json` | Prints the saved card without `evidence`: the card as `text`, with its `units` and `gaps`. |
| `--state-dir NAME` | The workspace, `demo` by default. |

The planning date is not a view of the past. The records are the ones read now, and the card's
second line says when they were read.

## Units

A unit is one numbered piece of evidence, and every number on a card cites one.

- A CRM record is one unit.
- Call and Slack text is one unit for each whole sentence.
- A call or message with no sentence is one unit, so every record that was read has a number.

Units are numbered in a fixed order:

1. The account, then its contacts, contracts and opportunities, each by ID.
2. Call text, by its time as written and then its ID.
3. Slack text: demo messages by their time as written and then their ID, and a live thread by
   Slack's timestamp.

On demo data, two times written with different UTC offsets sort by their text, not by the moment
they name. An imported transcript stores its time in UTC.

`inspect BRIEF_ID` lists every unit, one per line. `--role` with `customer`,
`customer_side_not_in_crm`, `seller`, `other_party` or `unknown` lists one role's sentences.

```text
1  record  accounts  acc-003
5  calls  2027-01-12T15:00:00Z  customer  "Marian Holt"  shown  "Our general counsel will not sign until ..."
```

`inspect BRIEF_ID N` takes one or more numbers. For each, it gives the unit, the receipt of the read
that returned its record (`audit_id`, `tool`, `status` and `http_status`), and `next_by_speaker`.

A record also gets its `fields`, each neutralized and on one line. A sentence gives its text, its
speaker and role, whether it is on the card, and `raw_sha256`, the SHA-256 of the record's text as
stored. A sentence sealed under the speaker rules also gives the rung that decided its role and the
identifier that decided it, with the receipt of the read that returned that identifier's record. `next_by_speaker` is the number of the next whole sentence under the same speaker label in
the same record, or `null`.

`inspect` answers from the sealed units and evidence, never from the card's text, and rebuilds
nothing. A later change to the rules cannot move or refuse a number. A sentence sealed under an
older version of the neutralizer rules (`sanitizer_version` below 5) has its text and speaker
neutralized again under today's before `inspect`, the web API or a brief tool shows it. The card's
bytes stay as sealed, so it still verifies, and the sentence keeps the version it was sealed under.

A card's text goes where it is read in one of two forms:

- As sealed, exactly: the web app's Text tab, its copy, and `GET /api/v1/cards/{card}/text` and
  `/bytes`. These are where a reader checks the card against its seal, and the browser shows the
  text as inert text.
- Rendered for a model or a terminal: the `text` the brief tools return and the card the CLI prints.
  A card made under today's rules is its sealed text exactly. A card with a sentence sealed under an
  older neutralizer version is rendered again, with a first line that says so: each quotation under
  `SAID` from its unit, neutralized again; `NOT READ` from its gaps; and every other quoted literal,
  such as a speaker or a record value, neutralized again. Its sealed text stays in its bytes.

### Unit keys

A record unit has `n`, `kind` (`record`), `source` (`crm`, `calls` or `slack`), `record_ref` and
`tool`. A sentence unit (`kind` `sentence`) has those and these:

| Key | Value |
| --- | --- |
| `at` | The record's time as the source wrote it, or `null`. |
| `speaker` | The speaker label, neutralized and on one line, or `null`. |
| `role` | `customer`, `customer_side_not_in_crm`, `seller`, `other_party` or `unknown`. See [speaker roles](#speaker-roles). |
| `basis` | How the role was decided, in the words the card prints, such as `by attendee list` or `names no listed seller, contact or user`. A unit sealed before the speaker rules keeps its own, such as `label matches contact [2]`. |
| `contact` | The number of the contact a customer is, or `null`. |
| `text` | The sentence, neutralized. |
| `partial` | `true` when the sentence is not whole. |
| `quoted` | `true` when the sentence is in a block quote or a speaker's turn of a Slack message: words its author relayed, with `role` `unknown` and `basis` `quoted by the message's author`. A card sealed under rules version 3 has no `quoted` key. |
| `shown` | `yes`, or why the sentence is not under `SAID` (below). |
| `span` | `null`, except in an imported transcript: `first` and `last`, the cues or lines the sentence lies in, and `start_ms` and `end_ms`, which cover every cue it lies in, whatever order the file wrote them in. Lines have no times, so both are `null` for a TXT file. |
| `raw_sha256` | The SHA-256 of the record's text as stored. |
| `sanitizer_version` | The version of the neutralizer rules, `5`. |
| `speaker_rules` | The version of the speaker rules the role was decided under, `1`. A unit sealed before them has no `speaker_rules`, `rung` or `matched`, and shows its role and basis as sealed. |
| `rung` | The rung of the ladder that decided, `1` to `5`, or `0` when none did. |
| `matched` | The identifier that decided, or `null`: `kind` (`email`, `slack_user`, `salesforce_contact`, `salesforce_user`, `name`, `listed_seller` or `alias`), `value`, neutralized, and `record_ref`, the record it came from, or `null` for a listed seller or an alias. |

| `shown` | Why the sentence is not shown |
| --- | --- |
| `cut` | It is not a whole sentence. |
| `later` | Its record is dated after the planning date. |
| `long` | It is over 300 characters. |
| `more` | It is whole and fell outside the 12 places. |

Exactly one holds, in that order.

Read a unit's `kind` before any other key.

## The seal and the saved card

A card is saved in PostgreSQL, in the workspace that made it, as the exact bytes of one JSON
document. Once the evidence is built, `brief` commits one more receipt, `save_brief`, as `pending`.
It then writes the card and finishes that receipt in one transaction.

The finished `save_brief` receipt is the card's seal. Its one row names the card and the SHA-256 of
the saved bytes:

```text
briefs/brief-b2d18969b48d451183e6cbdaf3ee980c/26c2702e8eaf69083b183806b1b95519c332d1b86b5a797e837fd28001d0f866
```

Its `arguments` are the account ID and the brief ID. The card's `sealed_by` names the seal's
`audit_id`, and each of the card's read receipts carries its brief ID. `audit --brief BRIEF_ID`
lists those reads, and `inspect AUDIT_ID` shows the seal.

> **A changed card is refused.** `inspect` re-hashes the stored bytes and compares them with the
> digest the seal names before it shows anything. That covers a changed amount, sentence, speaker,
> seller, date or card text.

Before `inspect BRIEF_ID` shows a card, it checks that:

- the workspace holds a card with that brief ID,
- the seal is a finished `save_brief` receipt (`ok`, HTTP 200) for this account and brief, and its
  row names the SHA-256 of the bytes as they are now,
- each read receipt the card sealed matches the one in the workspace's audit log, column for
  column, and was made with the same token as the seal,
- the workspace is still connected to the sources the card was read from,
- every number on the card resolves: its tool's receipt returned its record, and a sentence's
  `raw_sha256` is the digest of that record's stored text.

PostgreSQL holds the same rules for any writer. It refuses to publish a card whose seal has not
finished naming the digest of its bytes, whose unit and gap rows are not exactly the ones in its
bytes, or whose cited reads did not finish `ok` on the card's account and brief. A card, its units,
its gaps and its lines are never changed or deleted once written.

> **The database owner is not held off.** A role that can switch off triggers can rewrite a card and
> its seal together. The checks catch a changed card, not a determined owner of the database.

Evidence that fails a check gives no card and starts no seal. A save that does not finish leaves no
card, and its seal ends as an error, `brief_save_failed`. After a hard stop the seal stays `pending`
with the account and brief in its `arguments`, and [receipts](audit.md) says how it is finished.

### The saved card

| Key | Value |
| --- | --- |
| `schema_version` | `3` |
| `brief_id`, `account_id`, `as_of` | The card's identity and planning date. |
| `status` | `accepted` |
| `rules_version` | `5`, the version of the rules the text was made under. |
| `sealed_by` | The `audit_id` of the seal. |
| `sources` | `crm`: `demo_fixture` or `salesforce`. `calls`: `demo_fixture`, `imported_transcript` or `not_connected`. `slack`: `demo_fixture`, `slack` or `not_connected`. |
| `text` | The card. |
| `units` | The [units](#units). |
| `gaps` | The [gaps](#gaps). |
| `evidence` | The records as read, one receipt per read, each source's binding, the seller list and the `people` object: `rules_version`, the workspace's `company_domains` and its `aliases`. |

`brief --json` prints every key but `evidence`. The evidence keeps the records read when the card
was made, not later CRM changes.

## Gaps

A gap is one line under `NOT READ`. It has no number. `gaps` holds one entry for each, in the order
the card prints them.

| Key | Value |
| --- | --- |
| `code` | One of a fixed list: the keys of `GapCodes` in `internal/evidence/units.go`. The database's `gaps` table accepts only these codes. |
| `disposition` | `not_read`, `read_not_shown` or `note`. Each code has one. |
| `count` | How many records or sentences the line says were read and are not shown, how many its note is about, or how many files, attachments and blocks of a Slack read it says were not read. It is `null` when the line gives no such number. |
| `by_role` | That count by side (`customer`, which counts customers and customer-side speakers not in the CRM, `seller`, `other_party` and `unknown`), for a line that counts sentences, or `null`. A card sealed before the speaker rules has no `other_party`. |
| `text` | The line itself. |

Name a gap by its `code`, never by its text.

## Sentences

A sentence ends at closing punctuation or at a blank line.

- A single line break is a wrap, so a sentence written over two lines stays whole.
- In a Slack message a line break also ends a sentence before each of these, which starts a block of
  its own:
  - a list item: after any spaces, `-`, `+`, `*`, `•`, `◦` or `▪`, or a number with `.` or `)`, then
    any space;
  - a speaker's turn: a line whose text up to its first colon names a known person of the card, a
    contact of the account or a listed seller, compared as a speaker label is compared for a role,
    then a space and more, as in `Dana Ito: we can sign`. A label that names nobody known, such as
    `Update:` or `Blocker:`, leaves the line the author's own;
  - a block quote: a run of lines that start with `>` or `&gt;`;
  - a code block, from the line that opens a ```` ``` ```` fence to the line that closes it. The line
    after a block quote or a code block starts a new block too.

  The sentences of a block quote or of a speaker's turn are marked `quoted`: the author relayed someone
  else's words, so the author's role is not theirs. The card shows them under no established role, as
  quoted by the message's author, and a speaker's turn keeps its label in the text, so the reader sees
  whose words they are.
- A line that ends in closing punctuation ends its sentence there as anywhere else.
- In a call note or a Slack message, a sentence without closing punctuation that more of the record
  follows is not whole. It is counted as `cut` and never shown as a quotation. The end of the record
  is an end, so a one-line message with no punctuation is whole.
- A period after a title, a legal or state short form, a list marker, or a dotted short form such as
  "e.g." does not end a sentence, so "28 U.S.C. § 1332" and "Fed. R. Civ. P. 26(b)(3)" stay in one
  piece.
- A firm suffix or a capitalized initialism ("LLP.", "U.S.A.") ends a sentence only before a word
  that opens one, such as "We" or "The".
- Closing punctuation followed by a lower-case letter does not end a sentence.

Where the text does not settle it, two sentences stay joined, so a fragment is never shown as a
sentence.

In an imported transcript, two cues join into one run only when they have the same speaker label
and the second starts no more than two seconds from the end of the first. The limit is ten seconds
when the first stops mid-sentence and the second goes on in lower case. Cues or lines with no label,
or with no times, never join.

A run that stops mid-sentence is counted and not shown. So is what may be its other half:

- the start of that speaker's next run,
- the next words of anyone, when they go on in lower case,
- each speaker's first words, in an excerpt that starts inside a call.

The end of a Slack message or a call note is an end.

> **A quotation is never cut.** A sentence over 300 characters is counted and not shown.

A sentence of a call or message dated after the planning date is counted and not shown. That date
is the record's own time read in UTC, not a cue's place in a recording. A time with no UTC offset
decides nothing.

## Sections

| Section | What it holds |
| --- | --- |
| First two lines | `DEAL CARD:`, the account name as a quoted literal, its ID and the planning date. Then `Sources read` and the time, to the second, from the start times of the reads that finished `ok`: one time, or the first and last. |
| `ATTENTION` | The [signals](#signals). Left out when there are none. |
| `RECORD VALUES` | Typed CRM fields and connector arithmetic. An empty field prints `Missing`. An amount prints its currency code when the record carries one, and `(currency Missing)` when it does not. At most five opportunities, five contracts and five contacts are shown, and the rest are counted under `NOT READ`. |
| `SAID` | Whole sentences under one heading per side: `Customer side`, `Seller side`, `Other party`, then `No established role`. At most 12 are shown. |
| `NOT READ` | What each source did not return or was not asked for, how many fields are empty, how many sentences are not shown and why, how many have no speaker role, and what was neutralized. When call sentences carry speaker labels and none equals a contact's name, one line says so. |

The demo CRM gives `Renewal date`, `Annual recurring revenue` and `CRM risk`. Salesforce gives
`Contract end`: the contract that ends first on or after the planning date, and its status, marked
`a planning candidate, not activated` unless it is activated. With no such contract the line says
`Missing: no contract ends on or after` the date. On Salesforce a `NOT READ` line always says annual
recurring revenue and renewal risk are not mapped from standard Salesforce fields.

Open opportunities come first, by close date, then closed ones, latest first.

Under `SAID`:

- Each sentence has its number, its source (`call` or `Slack`), its time and its speaker label, then
  how its role was decided. A customer's also has the contact's title, the day the account was read,
  and the contact's number, as in `"Marian Holt", role "Deputy general counsel" read 2027-01-15 [2], by
  name`. A title that is `Missing` prints with no day. A labelled sentence of no role says why in
  parentheses.
- The 12 places are dealt one at a time to each side that still has a sentence, in the order above,
  newest first, so
  sixty messages from a seller cannot push the customer's one sentence off the card. Inside one call
  a later cue time is newer, and at one time the later sentence is. A sentence with no time is
  dealt last.
- Under each side the kept sentences print oldest first, and one with no time prints last.
  `NOT READ` counts what is not shown, by side.
- When sentences were read and none can be shown, the section says so, and `NOT READ` says why.
- A figure inside a sentence stays there and never becomes a record value.

## Signals

A signal is one line under `ATTENTION`, made from record fields and dates. It cites the units it
comes from.

| Signal | When |
| --- | --- |
| The renewal is in N days (or is today, or passed N days ago) and this opportunity is still open at stage "S" | An opportunity is not closed, and the renewal date, or the Salesforce contract end, is 30 days away or less, today, or past. More than five make one line that cites the first five and ends `and N more`. |
| The renewal date passed N days ago | The demo CRM's renewal date is before the planning date and no opportunity is open. |
| The latest contract end date, D, was N days ago | Salesforce returned no contract ending on or after the planning date, and at least one that ended before it. |
| CRM marks this account at risk | The demo CRM's risk status is `at_risk`. |
| This open opportunity's close date, D, is after the renewal date, D | An opportunity that is not closed closes after the renewal date, or after the Salesforce contract end the card counts from. More than five make one line. |
| This contact has no role recorded | The contact has no title. More than five make one line that cites the first five and ends `and N more`. |
| The latest dated call that was read is N days old | The newest call with a valid time, dated on or before the planning date, is more than 60 days old, counted from its UTC date. |

On Salesforce, the first signal and the close date signal say "contract end" where the demo says
"renewal".

## Speaker roles

A role comes from who the speaker is, never from what a sentence says. The card names the person by
the strongest identifier its evidence holds for the speaker, and the role is that person's side.

| Rung | Identifier | `basis` |
| ---: | --- | --- |
| 1 | A live Slack author's user ID in the signed-in workspace, or the profile email Slack gave for it ([Slack](sources.md#reading-a-thread)) | `by Slack profile` |
| 2 | The row of the call's attendee list under the speaker's label: its email ([attendees](sources.md#attendees)) | `by attendee list` |
| 3 | An alias of the label, set with `connect --alias` ([the CLI](cli.md#speaker-settings)) | `by alias` |
| 4 | The label equals a contact's name, a user's name or a seller list entry, each on one line | `by name` |
| 5 | The label, without a title, a suffix, a device name or the bracketed organization, and with "Last, First" turned around, equals such a name of exactly one person the call's attendee list names by email | `by name variant on the attendee list` |

The first rung that finds exactly one person decides, and no weaker rung is read after it. So the
attendee list's email beats the label: a label that equals a listed seller's name is the customer when
the attendee list gives that label a contact's address. A rung that finds two or more people decides
the role is unknown, as `N people match by name`, or `N people on both sides match by name`.

A person's side comes from their records:

| Role | The person |
| --- | --- |
| `customer` | A contact of the account. |
| `seller` | A Salesforce user of the org (a `Standard` user, active or not) or an entry of the workspace's seller list. |
| `unknown` | A contact who is also a user or a listed seller. |

A Slack author is a seller when the author's user ID equals a name or ID on the workspace's seller list.
A Slack author's name is compared with sellers only, so it never makes a customer: only a profile
email that a contact holds does. A call's label is compared with contacts, users and sellers.

Records of one person are one person: a contact and a user with the same email address, and a seller
list entry that equals the name of exactly one user.

An email that names no person still shows a side by its domain, and the sentence names the domain:

| Role | The address is on | `basis` |
| --- | --- | --- |
| `seller` | One of the company's domains (`connect --company-domain`). In Slack only an author of the signed-in workspace counts. | `seller side by domain D` |
| `customer_side_not_in_crm` | One of the account's domains: the domains of its contacts' email addresses. The person is not a contact. | `customer side by domain D, not a contact` |
| `other_party` | Any other domain | `other party by domain D` |

A free or shared mail domain, such as `gmail.com`, never shows a side, and neither does a domain on
both the company's list and the account's. A person a weaker rung names must be on the side a domain
showed, or the role is unknown. A label under several rows of the attendee list is several people
behind one microphone, such as a meeting room: it takes a side only when every one of them is on it.

The role is unknown for no label, a label that matches nobody, two or more contacts with the name, a
name shared by a seller and a contact, or a label over 200 characters, and for the other reasons
below. `unknown` says why in its `basis`:

| `basis` | Why |
| --- | --- |
| `no speaker label` | The sentence has no label. |
| `names no listed seller, contact or user` | No rung found anyone. |
| `anonymous label with no alias for this call` | A label such as `Speaker 2`, which names no one, and no alias for that call. Only an alias for the one call gives it a person. |
| `Slack author not identified; a Slack name never shows a customer` | A Slack author no profile, alias or seller entry names. |
| `email on a free or shared domain shows no side` | The only email is on such a domain. |
| `N people match by ...`, `N people on both sides match by ...` | The deciding rung found more than one person: two or more contacts with the name, or a name shared by a seller and a contact. |
| `speaker label over 200 characters` | The label is too long to print, so no rule reads it. |
| `a stronger identifier puts the speaker on another side, by ...` | A domain showed one side and the person a weaker rung named is on the other. |
| `the email's domain contradicts the person's side, by ...` | A contact's address is on the company's domain, or a seller's on the account's. |
| `the person is both a contact and a seller, by ...` | The person is a contact and also a user or a listed seller. |
| `the attendees behind this label are on different sides` | The rows of the attendee list under the label are not all on one side. |

A label is what the source wrote, and an attendee list or an alias is what the operator gave. Neither
proves who spoke: another person of the same name, or a name typed into a file, gets the same role. A
report of what someone else said keeps the role of the person reporting it, because the role comes
from the speaker.

In a WebVTT transcript the label is a voice tag, or a cue identifier that is not a number. A cue
with two voice tags gives each span its own speaker, and text after a closing tag belongs to the
cue again. The tag is the label and is not part of the sentence. In a transcript imported with
`--speaker-names` ([sources](sources.md)), the label is the name that opens each line, and a cue
identifier is not a label. In a live Slack thread the label is the author's Slack user ID.

### The seller list

The seller list belongs to the workspace and is kept in PostgreSQL. `--seller NAME` on
`connect salesforce`, `connect calls` or `connect slack` sets it: repeat the option for each name or
Slack ID. A `connect` with `--seller` replaces the saved list, and one without it keeps the list.
`connect demo` does not take `--seller`.

```sh
gtm-connector connect calls --seller 'Morgan Reed' --seller U0123ABC --state-dir renewals
gtm-connector connect status --state-dir renewals --json
```

`connect status` lists it under `sellers`. A list holds at most 200 entries of 1 to 200 characters,
and `connect` refuses any other with exit 2. Every card seals the list it was read with in its
evidence.

Demo calls and demo Slack also count the demo's own invented sellers: Morgan Reed, Quinn Moss and
Avery Linden. An imported transcript or live Slack never does.

The company's domains and the aliases are the workspace's speaker settings, kept beside the seller
list and set by the same `connect` forms with `--company-domain` and `--alias` ([the
CLI](cli.md#speaker-settings)). Every card seals them in its evidence's `people` object, with the
version of the speaker rules. Evidence with no `people` object, made before the speaker rules, is read
with what it holds: its records and its seller list. A Salesforce card's evidence also holds the org's users, as
`crm/users/usr-sf-ID` records of the `list_users` read, and a live Slack card's the thread's authors,
as `slack/members/mem-USER` records of the `read_slack_messages` read. Neither is a unit.

## Neutralized text

Source text is neutralized once, before anything displays it. That covers record fields, account
names, speaker labels and times as well as call and Slack text. Neutralizing replaces the parts
listed below with inert words.

- A Slack mention becomes `[mention channel]`, `[mention here]`, `[mention everyone]` or
  `[mention group]`, `[user U123]` or `[channel C123]`.
- A link of any scheme (`scheme://`), a `mailto:` address, a `//host` address or a `www.` address
  becomes its label, if it has one, and `[link host]`. A `mailto:` link shows `[link mail]`. The host
  is shown without a leading `www.`, which a browser keeps: `https://www.files.example/` shows
  `[link files.example]`.
- In the text a quotation is made from, a bare mail address such as `dana@files.example`, which a
  Markdown client links, shows `[link mail]` too, and a `]` right before a `(` gets a space between, so
  no quotation is a Markdown link. A contact's email is a record value and is shown as the CRM holds it.
- A link with a backslash or a control character before the end of its host shows
  `[link unknown host]`, because a browser may open a different host than the one written. So does
  a Slack link `<target|label>` whose target names a scheme with no `//` after it, and a Slack link
  or a Markdown reference definition whose target has no scheme and no `//`, such as
  `<files.example/x|deck>`: a browser opens it on the page's own host.
- A host with an internationalised label (`xn--` and Punycode) is shown in that ASCII form, lowercased,
  as `[link xn--bcher-kva.example]`: the name a browser looks up, never the Unicode form it decodes to,
  whose letters can look like other letters. A label shows `[link unknown host]` when it fails the
  checks the URL Standard's host parser makes (UTS #46 with Unicode 16.0.0 characters): the Punycode
  does not decode or encode back to the same label, the decoded name holds a character the mapping
  changes or refuses, starts with a combining mark or with `xn--`, is only ASCII, or breaks the
  right-to-left rule or the joiner rules of RFC 5892.
- A control, direction-changing or invisible format character becomes its code, such as `[U+202E]`.
- A bracketed number such as `[3]` becomes `(3)`, because a card cites its units that way.

Letters in any script are kept. Text is put in Unicode NFC form, a tab becomes a space, and every
kind of line break becomes one line feed. A quotation is exact against the neutralized text, and
`inspect` gives the SHA-256 of the record's stored text. The account name in the heading is a quoted
literal on one line. The last `NOT READ` line counts what was replaced, such as
`Neutralized in source text: 1 mention, 1 link.`

## Errors

`brief` and `inspect` exit with these codes. With `--json` the error is one object on stderr, with
`code`, `message` and `next`. [The CLI guide](cli.md) has the full table.

| Exit | `code` | Cause | Fix |
| ---: | --- | --- | --- |
| 1 | `local_failure` | The settings or the database failed, such as `the database could not be reached`. | See [operations](operations.md). |
| 2 | `usage_error` | The account ID is not of the CRM's form (`account ID must have the form acc-001`, or `acc-sf-` and the 18-character Salesforce ID), the date is not one (`as-of date must use YYYY-MM-DD`), the brief ID is not one (`inspect takes an account ID ...`), the brief ID is not a card of this workspace (`No saved card has this brief ID in this state folder.`), or the number is not on the card (`This card has numbers 1 to 9.`). Also no CRM connected, and a card that fails its seal. | Use an ID such as `acc-003`, a `YYYY-MM-DD` date, and a number printed on the card. Make the card again if its seal fails. |
| 3 | `refused` | The workspace belongs to another connection of its source, a source is not read by this build, or a source was connected again while the card was read. | See [sources](sources.md). Run the command again after a reconnect. |
| 4 | `check_failed` | The evidence failed a check, so no card was made (`brief BRIEF_ID rejected; no card published`). | Repeat the command. If it repeats, inspect the [receipts](audit.md). |
| 5 | `upstream_failure` | A read failed, or the account was not found (`get_account: invalid or failed tool result; no brief generated`). | Check the account ID with `accounts`, then retry. |
| 130 | `interrupted` | The command was interrupted. Completed writes are kept. | Check `audit`. |

`inspect` on an account the CRM does not hold exits 2 with `account not found: ID`.

A card that fails its seal exits 2 with one of these messages:

| Message | Meaning |
| --- | --- |
| `This card's seal was never finished: saving it was interrupted. Make the card again.` | The seal is still `pending`. |
| `This file is not the card that was sealed: the card or its seal receipt was changed.` | The bytes are not the ones the seal names, or the seal is not this card's. |
| `This card's seal receipt cannot be read from the audit log of this state folder. A card traces only in the folder that made it.` | The seal is not in this workspace. |
| `The sealed card's read receipts are not the ones in the audit log of this state folder.` | A read receipt differs from the logged one. |
| `This card was read from sources this folder is not bound to.` | The workspace was connected to other sources since. |
| `A number on this card does not resolve to a record and a receipt of its own card.` | A unit does not resolve. |
| `invalid saved brief evidence` | The bytes are not a card. |

A brief an assistant prepared with `prepare_renewal_brief` is read back with `read_renewal_brief`,
not `inspect`, which refuses it with exit 2. [MCP](mcp.md) has those tools.
