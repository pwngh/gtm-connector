# The command line

A person runs the connector with the `gtm-connector` command: connect a workspace to its sources, make
a Deal card for one account, and trace any number on that card to its record and receipt.

```sh
gtm-connector setup                              # once: credentials, migrations, the demo workspace
gtm-connector demo --state-dir renewal-demo      # one invented renewal, end to end
gtm-connector connect demo                       # connect the default workspace to the demo data
gtm-connector brief acc-003                      # make and print the card of one account
gtm-connector deal new acc-003                   # register the account's renewal as thornfield-2027-2e3hke
gtm-connector inspect BRIEF_ID                   # every unit of that card, one per line
gtm-connector inspect BRIEF_ID 5                 # where number 5 on that card came from
gtm-connector audit --brief BRIEF_ID             # the reads the card rests on
gtm-connector COMMAND --help
```

`help COMMAND` is `COMMAND --help` for `demo`, `connect`, `accounts`, `brief`, `deal`, `inspect`,
`audit`, `admit`, `grant`, `grants`, `mcp` and `token`. For any other name it prints the list of commands, as `--help` does. Run with no
command, the binary prints one sentence on what it does and three commands to try first.

[Deal cards](cards.md) explains what a card says and how to read it. This page covers what each
command takes and prints.

## Workspaces

A workspace is one named set of connected sources and everything read through them: the seller list,
the accounts, the deals, the cards, the receipts, the MCP client tokens, and the imported transcripts and mapped
Slack threads. It lives in PostgreSQL. `--state-dir NAME` names it, and every command that reads or
connects a workspace takes it. The default is `demo`.

```text
--state-dir renewals         workspace renewals
--state-dir 'Q3 renewals'    workspace dir- and the first 32 hex digits of the SHA-256 of "Q3 renewals"
(none)                       workspace demo
```

A name of 1 to 63 lowercase letters, digits and hyphens, starting with a letter or digit, is the
workspace's own name. Any other name, such as a path or one with spaces, names the workspace `dir-`
followed by the first 32 hex digits of the SHA-256 of the name as typed. A name already in that
`dir-` form is hashed like any other, so no typed name can reach a workspace another name was hashed to.

`setup` makes the `demo` workspace. `connect`, `demo --state-dir` and `debug salesforce check --sf-org`
make the workspace they name when it does not exist. Every other command finds a workspace and never
makes one: in a workspace that does not exist, nothing is connected and nothing is saved.

A workspace sees only its own rows. A card or receipt ID from another workspace is not found in this one.

## Connect the sources

`connect` is the one command that changes what a workspace reads. It attaches one source, detaches one
with `--remove`, or with `status` (or no name) shows what is connected. It saves which kind of source
each of `crm`, `calls` and `slack` is connected to, and the seller list. It saves no credential, org ID
or address.

```sh
gtm-connector connect demo --state-dir renewals
gtm-connector connect calls --state-dir renewals --seller 'Morgan Reed'
gtm-connector connect --state-dir renewals
```

```text
crm     connected  demo data
calls   connected  imported transcripts
slack   connected  demo data
Sellers: "Morgan Reed"
```

| Command | What it does |
| --- | --- |
| `connect demo` | Connects the CRM, calls and Slack to the invented demo book. Imported transcripts that are connected stay connected. Nothing is contacted. |
| `connect salesforce --org ALIAS --state-dir NAME` | Reads the `sf` login of that alias, checks the org against `--environment`, pins the workspace to the org and the user the login reads as, and saves the alias and the environment. It reads no record. See [Salesforce](sources.md). |
| `connect salesforce --token-env --state-dir NAME` | Does the same with the org address and token in `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` or `SALESFORCE_ACCESS_TOKEN_FILE`, where there is no `sf`, as in a container. It saves no alias. See [a token handed to the container](sources.md#without-sf-a-token-handed-to-the-container). |
| `connect calls` | Connects imported call transcripts. Add them with `debug calls import`. See [call transcripts](sources.md). |
| `connect slack` | Connects the Slack sign-in that `debug slack connect` saved for this workspace. Without one it exits `2` and names that command. See [Slack](sources.md). |
| `connect status`, or `connect` alone | Lists `crm`, `calls` and `slack` as connected or not connected, with the org alias and environment for Salesforce, then the seller list. It reads only the database. |
| `connect NAME --remove` | Detaches that source when it is the one connected, and changes nothing otherwise. Removing the CRM detaches calls and Slack with it. Nothing is deleted: the workspace keeps its cards and receipts. |
| `connect inspector`, `claude-code`, `claude-desktop`, `cursor` | Not in this build. Exits `3` and grants nothing. [MCP](mcp.md) covers how an assistant app connects instead. |

### The rules connect keeps

> **Demo data and real sources never share a workspace.** A workspace is made for demo data or for
> real sources and keeps that. `connect demo` in a workspace of real sources, and `connect salesforce`
> or `connect slack` in a demo workspace, each exit `3`, change nothing, and name `--state-dir NAME`.

- **Imported transcripts are the one exception.** `connect calls` may sit beside the demo CRM. A
  workspace that has already read the demo call notes refuses it with exit `3`: import into a
  workspace of its own.
- **A workspace keeps the identity it was pinned to.** A Salesforce or Slack connection is pinned to the
  identity it was first connected to, and keeps that pin after `--remove`. Connecting another org or user
  there exits `3` with "This workspace belongs to a different connection of this source", and changes
  nothing.
- **Salesforce needs a workspace of its own.** `connect salesforce` without `--state-dir` exits `2`: the
  default workspace holds demo data.
- **Calls and Slack belong to their CRM.** They connect only once a CRM is connected, and removing the CRM
  detaches them. Connecting the same CRM again keeps them.
- **Beside Salesforce**, calls and Slack start not connected. A card then says "Calls: not connected." or
  "Slack: not connected.", and nothing is read from that source. Demo data is never read in its place.
- **Beside the demo CRM**, `connect calls --remove` detaches imported transcripts and leaves calls not
  connected. `connect demo` then makes them demo data again only when the workspace holds no imports.
  With imports, only `connect calls` connects them again.
- **`demo --state-dir` connects a new workspace** to the demo data, as `connect demo` does.

A `connect` that is refused or fails changes no connection. One refused for its arguments saves
nothing, and a workspace it would have made is not made. `connect salesforce` makes its workspace
before its first request to `sf` or the org, to hold that request's
[receipt](audit.md#what-each-command-adds), so once it is made the workspace remains after a sign-in
that fails, with any receipt already committed, and no connection is saved. The rows of one `connect`
are saved together or not at all. If another command changed the workspace's sources while `connect`
ran, it exits `1`, saves nothing, and asks you to run it again.

### The seller list

`--seller NAME` lists a seller's name or Slack user ID, which decides who a card quotes as the seller
(see [speaker roles](cards.md#speaker-roles)). Repeat it for each seller. It is taken with `salesforce`, `calls` and
`slack`, never with `demo`.

The list is the workspace's: Salesforce, calls and Slack share it, whichever `connect` set it. It
replaces the saved one, so name every seller each time. A `connect` without `--seller` keeps the saved
list, `--seller ''` given once clears it, and `connect status` shows it. A list holds at most 200
entries of 1 to 200 characters each; a longer one exits `2`.

| Option | Taken with | Meaning |
| --- | --- | --- |
| `--state-dir NAME` | every form | The workspace. The default is `demo`. |
| `--org ALIAS` | `salesforce` | An `sf` alias: 1 to 64 letters, digits, dots, hyphens or underscores. A username or a token is refused before `sf` runs. |
| `--environment` | `salesforce` | `developer` (the default), `sandbox` or `production`. The org must be of this kind. |
| `--seller NAME` | `salesforce`, `calls`, `slack` | A seller's name or Slack user ID. Repeat for each. |
| `--alias [CONTENT_SHA256:]LABEL=TARGET` | `salesforce`, `calls`, `slack` | Maps a speaker label to a person. Repeat for each. See [speaker settings](#speaker-settings). |
| `--company-domain DOMAIN` | `salesforce`, `calls`, `slack` | One of your company's own mail domains. Repeat for each. |
| `--remove` | `demo`, `salesforce`, `calls`, `slack` | Detach the source. It takes no other option. |
| `--scope`, `--transport`, `--config-file` | an assistant app | Accepted by the parser for the app forms, which are not in this build. |

An option the named form does not take exits `2` and names it, as in "connect demo does not take
--seller."

### Speaker settings

The speaker settings are the workspace's company domains and aliases. They decide, with the seller
list, who a card quotes for which side (see [speaker roles](cards.md#speaker-roles)). `connect
salesforce`, `connect calls` and `connect slack` take them, never `connect demo`.

```sh
gtm-connector connect calls --state-dir renewals --company-domain northwind.example \
  --alias 'M. Reed=morgan@northwind.example' \
  --alias '3f1c...64 HEX DIGITS...:Speaker 1=dana@acme.example'
```

- `--company-domain DOMAIN` names one of your company's own mail domains: lower-case letters, digits
  and hyphens, with at least one dot. An address on it shows the seller side. At most 50.
- `--alias LABEL=TARGET` maps a speaker label to a person in every call. `TARGET` is an email address,
  which names the contact or Salesforce user who holds it, or an entry of the seller list. The label
  and the target are split at the last `=`. At most 200.
- `--alias CONTENT_SHA256:LABEL=TARGET` maps the label in one call only: the transcript whose
  `content_sha256` `debug calls import` printed. A label that names no one, such as `Speaker 1`,
  takes only this form.
- An alias whose target names no person of the account's card changes nothing.

Each list replaces the saved one when it is given, so name every entry each time. A `connect` without
`--company-domain` keeps the saved domains, and one without `--alias` keeps the saved aliases. Either
given once with an empty value, as `--alias ''`, clears its list.
`connect status` shows both when they are set.

| Refusal (exit `2`) | When |
| --- | --- |
| `A --company-domain is a mail domain in lower case, such as example.com.` | A domain of another form. |
| `Name at most 50 company domains with --company-domain.` | More than 50. |
| `An --alias is LABEL=EMAIL or LABEL=SELLER, with CONTENT_SHA256: before the label for one call's transcript. The label and a seller have 1 to 200 characters.` | An alias of another form. |
| `Name at most 200 aliases with --alias.` | More than 200. |
| `The speaker settings would take N bytes, and a workspace holds at most 65536. Name fewer or shorter aliases. Nothing was saved.` | The domains and aliases together, encoded, are over 64 KiB. |

## Output

Data goes to stdout. Errors, progress and notices go to stderr, so piped output is data alone.

- **Text** is the default, and its wording can change; scripts read `--json`. On a terminal a table has
  a bold heading row. In a pipe it is tab-separated rows with no heading and no color. `NO_COLOR`, or
  `TERM=dumb`, leaves the heading plain.
- **Source values print on one line.** A character that does not print, such as a control character,
  is written as its JSON escape.
- **`--json`** may stand anywhere before `--`. A result is one JSON value, indented by two spaces, on
  stdout. `setup`, `serve`, `backup`, `restore`, `health` and `version` write text only and refuse
  `--json` with exit `2`.
- **A read command names what it reads** on stderr in text mode: `brief` prints a `Sources:` line, and
  `accounts` and `inspect ACCOUNT_ID` print one for any CRM but the demo book. With `--json`, stderr
  holds only an error.

An error is two lines on stderr, or with `--json` one JSON object on one line:

```text
Error: No CRM is connected. Run gtm-connector connect salesforce --org ALIAS --state-dir NAME, or gtm-connector connect demo for invented data.
Next: gtm-connector connect --help
```

```json
{"error": {"code": "usage_error", "message": "No CRM is connected. Run gtm-connector connect salesforce --org ALIAS --state-dir NAME, or gtm-connector connect demo for invented data.", "next": "gtm-connector connect --help"}}
```

`error.next` is the command to run next, which is the command's own `--help` unless a better one exists.
What was typed is not repeated back in a refusal, since it may be a URL or a token. An error that the
connector did not word itself prints as a fixed line that names its kind, such as `PostgreSQL error
42501`, `the database could not be reached` or `internal error`, and holds none of the error's own text.

`setup`, `serve`, `backup`, `restore` and `health` print a failure as one line,
`gtm-connector: MESSAGE`, and exit `1`.

### Progress lines

`debug calls import`, `debug salesforce check` and the `debug slack` commands say on stderr which step
they have reached. With `--json`, each progress line is one object with a single `progress` key:

```json
{"progress": {"command": "debug calls import", "step": "resolving_account"}}
```

`command` is the command's full path. `debug salesforce check` adds `status` and `count`, and the
`debug slack` commands add `status`.

### Exit codes

| Exit | Meaning | JSON `error.code` |
| --- | --- | --- |
| `0` | The command succeeded, or `serve` stopped cleanly. | None. |
| `1` | A local file, database or runtime operation failed, or `setup`, `serve`, `backup`, `restore` or `health` failed. | `local_failure` |
| `2` | The arguments are missing or invalid, or what they name does not exist: no CRM connected, no such card, number or receipt, an account `inspect` cannot find. | `usage_error` |
| `3` | The operation was refused: not in this build, a workspace of the other kind, another identity than the pinned one, a source connected again while it was read, a second open deal of one account. | `refused` |
| `4` | A check failed: the evidence of a card, a step of the demo, `debug salesforce check` or `debug slack check`. | `check_failed` |
| `5` | A source or its transport failed, or returned what a read could not use. | `upstream_failure` |
| `130` | Ctrl-C interrupted the command. Completed writes are kept. | `interrupted` |

## Commands

| Command | JSON result on stdout |
| --- | --- |
| No command | Object with `description`, `try` (an array of three commands) and `help`. |
| `--version` | Object with `version`. Text: `gtm-connector 0.1.0`. |
| [`accounts`](#accounts) | Object with `accounts` and `has_more`. |
| [`brief`](#brief) | The saved card, without its evidence. |
| [`deal`](#deal) | A deal's object, an array of them, or a deal and its newest card. |
| [`inspect`](#inspect) | One of four results, by the ID given. |
| [`audit`](#audit) | Array of receipts. |
| [`demo`](#demo) | With `--state-dir`: one invented renewal. Without it: text only. |
| [`admit`](#admit) | Object with `cards`, `cards_added` and `receipts_added`. |
| [`connect`](#connect-results) | Object with `schema_version`, `sources`, and `sellers` or `provider` and `status`. |
| [`mcp stdio`](#mcp-stdio-and-token) | MCP messages only. |
| [`token`](#mcp-stdio-and-token) | A token's object, or an array of them. |
| [`grant`, `grants`](#grant-and-grants) | A grant's object, or an array of people. |
| [`debug calls`, `debug salesforce check`, `debug slack`](#debug-commands) | See below. |
| [`setup`, `serve`, `backup`, `restore`, `health`, `version`](#install-commands) | Text only. |

### `accounts`

`accounts` searches the connected CRM by account name. `--query TEXT` matches a name that holds the
words of the text in their order, each as literal text, with anything between them, so `Alder Mills`
finds `Alder  Mills plc`. On the demo book only the letters A to Z match in either case; Salesforce
compares letters as its own `LIKE` does. Without `--query`, or with a text of no words, every account
matches. Rows are ordered by name and then ID.

`--account-number NUMBER`, in a Salesforce workspace, gives the accounts with exactly that
`AccountNumber` instead, at most two. It is not taken with `--query`, and the demo book has no account
numbers: both exit `2`.

```text
ID       ACCOUNT              INDUSTRY        RENEWAL
acc-003  Thornfield Wren LLP  Legal services  2027-02-08
```

The table of a Salesforce workspace has the column `NUMBER`, the account number, in place of
`RENEWAL`: a Salesforce account carries no renewal date of its own.

`--json` gives one object, with or without `--query`: `accounts`, an array of at most 100 account
objects as the CRM returns them, and `has_more`, which is `true` when more accounts match and
`false` otherwise. Text output then adds one line on stderr that says more match, or `No account
matches.` when none does, without the text searched for. Names are neutralized as a card's text is.

On the demo book the search is no tool call. On any other CRM it is a `search_accounts` call under a
receipt that `audit` lists.

### `brief`

`brief ACCOUNT` reads every connected source for one account, publishes the card, and prints it.
`ACCOUNT` is the account's ID, or a name that `accounts --query` finds exactly one account for; text
output then names it on stderr, as `Account: acc-003 "Thornfield Wren LLP".` A name that several
accounts hold exits `2` and lists up to five of them by ID, and a text that is no ID and no account's
name is refused as an ID.
`--without-calls` leaves `calls:read` out of the card's grant: the call notes read is refused under
its receipt, and the card says "Calls: not read. The grant lacks calls:read."
`--as-of YYYY-MM-DD` sets the day the card plans from.

When the account has an open [deal](#deal), the card belongs to it, and text output names it on
stderr, after the sources line: `Deal: thornfield-2027-2e3hke.` With none, the card is the account's own and
belongs to no deal, as before deals existed. A deal's card reads the account's imported call and mapped
Slack thread only when they are matched to the deal ([setting it up](config.md#what-is-built-today)),
and says when one is not. A card that read a call or thread for no deal, while a deal was registered for
the account, is not published and exits `3`; run `brief` again.

```text
Sources: CRM, calls, and Slack are demo data.          (stderr)
DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15
...
Brief: brief-bf6f10de3a9647ce996ec78242205f34
List every unit: gtm-connector inspect brief-bf6f10de3a9647ce996ec78242205f34 --state-dir demo
Resolve a number: gtm-connector inspect brief-bf6f10de3a9647ce996ec78242205f34 N --state-dir demo
```

`--json` gives an object with `schema_version: 3`, `brief_id`, `account_id`, `as_of`,
`status: "accepted"`, `rules_version`, `sealed_by`, `sources`, `text` (the card), `units` and `gaps`.
The saved card also holds `evidence`, the raw records and receipts, which no command prints.

| Key | Value |
| --- | --- |
| `as_of` | `--as-of`, or else `2027-01-15` on the demo book and today's UTC date on Salesforce. |
| `rules_version` | The sentence rules the units were numbered under. |
| `sealed_by` | The ID of the `save_brief` receipt that holds the saved card's digest. See [the seal](audit.md#the-seal). |
| `sources.crm` | `"demo_fixture"` or `"salesforce"`. |
| `sources.calls` | `"demo_fixture"`, `"imported_transcript"` or `"not_connected"`. |
| `sources.slack` | `"demo_fixture"`, `"slack"` or `"not_connected"`. |

The account ID and the day are checked before anything is read or written. An ID of the wrong form
for the CRM (`acc-001` for the demo book, `acc-sf-` and 18 characters for Salesforce) or a day that is
not `YYYY-MM-DD` exits `2`. An account the CRM does not hold exits `5` with `get_account: invalid or
failed tool result; no brief generated`, and no card is published.

### `deal`

A deal is one renewal of one account, under a readable ID such as `thornfield-2027-2e3hke`. [The
API](api.md#one-deal-one-id) gives the rule for the ID, what a deal binds, and the deal's object,
which `--json` prints as the routes give it.

| Command | What it does |
| --- | --- |
| `deal new ACCOUNT_ID [--as-of YYYY-MM-DD]` | Lists the account in the workspace, as `brief` does, then reads the account and its opportunities from the connected CRM, each as a tool call under its own receipt (on the demo book too), and registers the deal on what they returned. It then matches the account's imported call and mapped thread ([setting it up](config.md#what-is-built-today)). `--as-of` sets the day the contract is chosen from: by default `2027-01-15` on the demo book and today's UTC date on Salesforce. |
| `deal list` | Lists the workspace's deals, the open ones first: ID, account, name, state and the day it renews. |
| `deal show DEAL_ID` | Shows the deal's bindings, then its newest card as `brief` prints it, verified against its seal as `inspect` verifies one. A `Read call` or `Read thread` row names each call and thread that card read, by its match, with `decided again` when a later decision replaced it. `--json` gives `{"deal": DEAL, "card": CARD, "matches": [MATCH, ...]}`, the card without its evidence or `null` before `brief` has made one, and the matches as [the API](api.md#matches) gives them. When the newest card is a brief an assistant prepared through MCP, the command shows no card for it: `card` is `null`, the deal carries `latest_card_prepared: true`, and text output names `brief` as the way to make a card to read here. |
| `deal close DEAL_ID` | Closes the deal. Its cards and receipts are kept, and the account's next card is its own again. Closing a closed deal prints it as it is. |
| `deal matches [--all]` | Lists the matches that wait for a person on a call or thread a card could still read: ID, kind, item, account, state, deal and candidate deals. `--all` lists every call's and thread's current decision, and marks a stale one, whose item is no longer the account's current import or mapping. |
| `deal decide MATCH_ID (--deal DEAL_ID \| --reject)` | Decides a match as the owner: onto one deal of its account, or off every card. The decision gets a `decide_match` receipt and supersedes the match. |

```text
$ gtm-connector deal new acc-003
FIELD        VALUE
ID           thornfield-2027-2e3hke
Account      acc-003
Name         "Thornfield Wren LLP"
State        open
Opportunity  opp-003
Contract     none
Renews       2027-02-08
```

An account has one open deal at a time: `deal new` for an account with one exits `3`, and names
`deal list`. An account the CRM does not hold exits `2` with `account not found: ACCOUNT_ID`, and an
ID of the wrong form for the CRM exits `2`, as `brief` does. `deal show` and `deal close` of an ID the
workspace does not hold exit `2`. `deal decide` of an unknown match, or onto a deal of another
account, exits `2`, and of a match a later decision superseded exits `3`. Each action takes
`--state-dir NAME`, `--json` and `--help`.

### `inspect`

`inspect` takes one of four IDs. The account form reads the CRM. The other three read only the cards
and receipts the workspace has saved, and write nothing.

| Form | Shows |
| --- | --- |
| `inspect ACCOUNT_ID` | The account as the CRM returns it, its contacts, and its newest card. |
| `inspect BRIEF_ID` | Every unit of a saved card, one per line. `--role` keeps one role's sentences. |
| `inspect BRIEF_ID N [N ...]` | Where each number came from: its record, its receipt, and for a sentence its speaker and the next sentence by that speaker. |
| `inspect RECEIPT_ID` | One receipt, its arguments, and the rows it returned. |

An account ID is `acc-` and three digits, or `acc-sf-` and 18 letters or digits. A brief ID is `brief-`
and 32 lowercase hex digits. A receipt ID is the UUID `audit` prints. Anything else, numbers after
anything but a brief ID, or `--role` with anything but a brief ID alone, exits `2`.

> **A card opens only when it checks out.** `inspect` reads a saved card only when its bytes are the
> ones its seal names, its reads are receipts this workspace holds, it was read from the sources the
> workspace is bound to, and every number on it resolves. Otherwise it exits `2`, as does a brief ID
> this workspace holds no card for: "No saved card has this brief ID in this state folder."

A brief that an assistant prepared here with `prepare_renewal_brief` is read with `read_renewal_brief`,
not with `inspect`, and `inspect` exits `2` and says so. See [MCP](mcp.md). A card or prepared brief
the Python package saved, brought in with [`admit`](#admit), opens with `inspect` in all four forms,
checked against the receipts admitted with it. Its result carries `admitted`, with the `from_org` and
`export_sha256` it was admitted on and the note `admitted from an export, not re-read`, and text output
says so on its first line. A receipt admitted with it carries `admitted` with `from_org` and the note.

#### `inspect ACCOUNT_ID`

`--json` gives the account object with a `contacts` array and `newest_brief_id`. Its text is
neutralized as a card's is. Salesforce adds a `contracts` array. On Salesforce the read is a
`get_account` call under its own receipt; on the demo book it is no tool call.

`newest_brief_id` is the most recently published card of the account in this workspace that `inspect`
can open, or `null`. Text output prints the fields, the contacts, and the line
`Newest card: BRIEF_ID  (gtm-connector inspect BRIEF_ID --state-dir NAME)`.

An account the CRM does not hold exits `2` with `account not found: ID`, and names `accounts` next.

#### `inspect BRIEF_ID`

`--json` gives an object with `brief_id`, `account_id` and `units`: every unit of the card as sealed,
in number order. A unit sealed under older neutralizing rules shows its text and speaker neutralized
again by today's. `--role` with `customer`, `customer_side_not_in_crm`, `seller`, `other_party` or
`unknown` keeps only the sentences whose role is exactly that one. Text
output is one line per unit, never cut or wrapped:

```text
1  record  accounts  acc-003
5  calls  2027-01-12T15:00:00Z  customer  "Marian Holt"  shown  "Our general counsel will not sign until the zero-retention and client-consent rider for AI processing is approved."
6  calls  time Missing cues 2-2  unknown  "Priya Natarajan"  shown  "We need the security review finished before we sign."
```

- A record line holds its number, `record`, its table and its ID.
- A sentence line holds its number, source, time, role, speaker label, whether it is on the card, and
  its text. The time is `time Missing` when the record has none, and is followed by `cues F-L` or
  `lines F-L` when the sentence lies in an imported transcript. The label is a JSON literal, or
  `no speaker label`.
- A sentence not on the card says why: `cut off`, `too long to show`, `dated after the card's day`,
  `past the card's places`, or `not recorded` for any other value.

When no sentence has the role asked for, text output says "No sentence on this card has that role."

#### `inspect BRIEF_ID N [N ...]`

The numbers are the digits printed on the card, given together after the brief ID. The card is checked
once for all of them. A number outside the card exits `2` with "This card has numbers 1 to N."

`--json` gives an array with one object per number, in the order given. Each has `brief_id`,
`account_id`, the unit's own keys as `inspect BRIEF_ID` lists them (`n`, `kind`, `source`,
`record_ref`, and for a sentence `at`, `speaker`, `role`, `basis`, `contact`, `text`, `partial`,
`shown`, `span`, `raw_sha256` and `sanitizer_version`, and for a sentence sealed under the speaker
rules `speaker_rules`, `rung` and `matched`), then `receipt` (`audit_id`, `tool`, `status`,
`http_status`) and `next_by_speaker`. A record adds `fields`, its plain values by name. A `matched`
that names a record adds the `receipt` of the read that returned that record, with the same keys. A
number whose `matched` names a record the card's evidence does not hold exits `2`.

`next_by_speaker` is the number of the next whole sentence under the same speaker label in the same
record, or `null`. It is `null` for a record, for a sentence with no label, and for a label that holds
what neutralizing can put there: a bracketed mention, user, channel, link or character code, or a
number in parentheses such as `(5)`. Two such labels can print alike.

Text output is one table per number, separated by a blank line:

```text
FIELD                 VALUE
Number                5
Kind                  sentence
Record                calls/call_notes/note-003b
Receipt               b63a52fb-… (read_call_notes, ok)
Text                  "Our general counsel will not sign until the zero-retention and client-consent rider for AI processing is approved."
Speaker               "Marian Holt"
Next by this speaker  6
Role                  customer (by name)
Time                  2027-01-12T15:00:00Z
On the card           yes
Source text SHA-256   105807d242e8f55965e938a24bc94a445f157178cbb28569e9b5ce4234bc100c
Neutralizer version   6
Speaker rules         1
Rung                  4
Matched               name "Marian Holt"
Matched from          crm/contacts/con-003, receipt 5e0c41d7-… (get_account, ok)
```

A sentence in an imported transcript adds a `Cues or lines` row. A sentence sealed under the speaker
rules adds `Speaker rules`, `Rung`, and, when an identifier decided, `Matched` (its kind and value) and
`Matched from` (the record and its read's receipt, or the workspace's aliases). A record's table lists
its fields by name after `Receipt`: a text as a quoted literal, a number as it is, and an empty value
as `Missing`. The example shortens the receipt IDs and the digest, which inspect prints whole.

#### `inspect RECEIPT_ID`

`--json` gives the receipt object, as `audit` gives it, with the text in its `arguments` neutralized as
a card's is. Text output is the receipt's `audit` row, then `Arguments` and the arguments as JSON,
then `Returned rows` and one row reference per line, or `none`. [Receipts](audit.md) lists the fields.

### `audit`

`audit` lists the workspace's receipts in the order they started. Filters select by card or run, tool,
caller, outcome and time, and `--follow` keeps printing new and changed receipts until Ctrl-C, which
exits `130`.

`--json` gives an array of receipt objects, or with `--follow` one object per line. A workspace with
no receipt gives `[]`. One that does not exist exits `2` with `audit database not found; run the demo
first`. [Receipts and the audit log](audit.md) covers the filters, the fields and when receipts are
written.

### `demo`

`demo` is two commands, told apart by `--state-dir`.

**`demo --state-dir NAME`** opens one invented renewal, Thornfield Wren LLP (`acc-003`), in that
workspace. It looks the account up with `search_accounts`, has a read of its call notes refused for a
grant without `calls:read`, makes its card, and prints:

```text
Call-note access was refused (HTTP 403) for a grant without calls:read.
Every source below is an invented demo fixture.

DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15
...
```

A workspace that does not exist is made and connected to the demo data, as is a demo workspace that is
connected to nothing. A workspace connected to anything else, or made for real sources, exits `2`: "This workspace is connected to other sources. Give
the demo its own --state-dir." Each run adds one card and seven receipts: the lookup, the refused read,
the card's four reads, and the seal. It writes nothing to a source. A step that does not go as
described exits `4` with `demo invariant failed: ...`.

`--json` gives an object with `schema_version: 2`, `status: "accepted"`, `workflow: "renewal-demo"`,
`data_mode: "demo"`, `activities_created: 0`, and the keys below.

| Key | Value |
| --- | --- |
| `sources` | `crm`, `calls` and `slack`, each `"demo_fixture"`. |
| `account` | `id`, `name`, `record_ref`, `renewal_date`. |
| `lookup` | `query`, `matches`, `receipt_id`. |
| `denied_read` | `tool: "read_call_notes"`, `status: 403`, `required_scope: "calls:read"`, `receipt_id`. |
| `brief` | `brief_id`, `account_id`, `as_of`, `text`, `units` and `gaps`, copied from the saved card. |
| `audit` | `run_id`, the run's ID that `audit --brief` selects, and `rows: 7`. |

**`demo` alone** fills the default `demo` workspace for the web app's demo sign-in. It connects each
source of the workspace that has no connection yet to the demo data, and leaves a source someone
removed as it is. It grants the demo sign-in each account of the demo book it was never granted, and
publishes a card for each account that holds none. It prints one line per card, then a count:

```text
acc-001  brief-7f121b379c65471babfba70ffe80fabc  5 units  sha256 4c1215e5...
...
30 cards published for demo-reviewer; 0 accounts hold one already.
```

`--again` publishes a new card for every account, also those that hold one. A grant someone revoked
stays revoked, and its account gets no card. This form writes text only: with `--json` it exits `2`.
`--again` with `--state-dir` exits `2`. It needs `setup` first, and exits `1` without the demo sign-in.

### `admit`

`admit FOLDER --from-org IDENTITY --export-sha256 DIGEST` brings the cards a state folder of the
Python package holds into a workspace, with the receipts they rest on. It reads the folder's `briefs/`
and `selected-briefs/` files and its audit log as `audit.jsonl`, one receipt row a line, as exported
from `audit.sqlite3`; the database itself is not read.

```sh
gtm-connector connect demo --state-dir old-cards
gtm-connector admit ./state --state-dir old-cards --from-org IDENTITY --export-sha256 "$(shasum -a 256 < ./state/audit.jsonl | cut -c1-64)"
gtm-connector inspect BRIEF_ID --state-dir old-cards
```

> **Admission trusts the export folder.** The folder supplies each card, the receipts of its reads and
> its seal, so a seal that names a card's bytes shows only that the folder agrees with itself. `admit`
> does not read the cards' records again from Salesforce or any other source, and cannot show offline
> which org a folder came from. It admits a folder on the operator's word: `--from-org` names the CRM
> identity the cards were read from (the `identity_sha256` a card records under `crm_binding`), which
> must be the one each card records and the one the workspace is connected to, and `--export-sha256`
> is the SHA-256 of `audit.jsonl` as the operator exported it. Both are kept with each card, and
> `inspect` says the card was admitted from an export and not read again.

Within that, each card is checked as `inspect` checks a card: its bytes are the ones its seal receipt
names, its reads are rows of the log, it was read from the sources the workspace is connected to, and
every number on it resolves. Then:

- every receipt line the card rests on is written the one way the export writes a receipt, and each
  read's line holds exactly the receipt the card embeds, so no byte of a line can change unseen; the
  seal's time and duration, which no digest in the folder covers, are covered by `--export-sha256`;
- every quotation on the card is a sentence its record holds under that speaker, as today's rules
  split and neutralize the record;
- every imported transcript the card quotes is one the workspace holds for that account, whose stored
  bytes hash to the `content_sha256` the card's record names; import it with `debug calls import`
  first;
- every card and prepared brief file of the folder is checked, so a good file does not hide a damaged
  or oversized one.

The card is then kept as the folder holds it, byte for byte, never made again, and the receipts are
kept as the log's rows. A card keeps the neutralizer version it was sealed under, and `inspect` shows
its units as it shows any card's.

A folder any card of which fails a check admits nothing and exits `4` with the card's brief ID and
the check's sentence, for example:

```text
Error: brief-2430d066000040008000000000000001: This file is not the card that was sealed: the card or its seal receipt was changed. Nothing was admitted.
```

A `--from-org` that is not the workspace's CRM identity, and an `audit.jsonl` whose digest is not
`--export-sha256`, also exit `4`. Either flag missing, or not 64 lowercase hex digits, exits `2`.
Admitting a folder again adds nothing. A card or receipt whose ID the workspace holds with other
bytes, or a card made here with the same brief ID, exits `3` and admits nothing. A workspace that is
connected to no CRM exits `2`, as `brief` does.

`--json` gives an object with `cards`, one object per card with `brief_id`, `account_id`, `kind`
(`cli` for a card saved by `brief` or `demo`, `prepared` for a brief saved by
`prepare_renewal_brief`) and `added`, then `cards_added` and `receipts_added`. Text output is a table
of the cards, then the counts:

```text
brief-2430d066000040008000000000000001  acc-003  saved by brief or demo  added
1 cards and 5 receipts added.
List every unit: gtm-connector inspect BRIEF_ID --state-dir old-cards
```

Admitted cards open with `inspect` and travel in a backup. `audit`, `inspect ACCOUNT_ID`, the web app
and the MCP tools show only the cards and receipts made here.

### Connect results

| Command | JSON result |
| --- | --- |
| `connect status`, or `connect` alone | Object with `schema_version: 1`, `sources` and `sellers`, then `company_domains` and `aliases` when they are set. |
| `connect demo`, `salesforce`, `calls`, `slack`, with or without `--remove` | Object with `schema_version: 1`, `provider` (the name given), `status` and `sources`. |

`sources` holds each connected source by name, `crm`, `calls` or `slack`, with its `provider`:
`"demo"`, `"salesforce"`, `"imported"` or `"slack"`. Salesforce adds `environment` and `org`, the alias.
A source that is not connected is absent. `sellers` is the saved seller list, or `[]`.
`company_domains` is the saved list of domains, and `aliases` the saved aliases, each with `label`,
`call` (a `content_sha256`, or `null` for every call) and `target`. Text output ends with
`Sellers:`, then `Company domains:` and `Aliases:`, one alias a line, when they are set.

`status` is `"connected"`, `"removed"`, or `"not_connected"` when `--remove` named a source that was
not connected. Text output opens with one line, such as `Demo data connected.`, `Call transcripts
removed. Its cards and receipts are kept.` or `Slack was not connected. Nothing changed.`, then the
table.

### `mcp stdio` and `token`

`mcp stdio --scope SCOPE` serves the workspace's MCP tools on stdin and stdout, for an assistant app
that starts it. Its stdout carries only MCP messages, and `--json` changes only how a startup error is
written. `--scope` takes `crm:read`, `calls:read` or `slack:read`, repeated for each, and is required.
`--selection FILE` serves only the accounts and threads in a selection file and adds the two brief
tools.

`token new --scope SCOPE` makes a bearer token for `serve`'s `/mcp` and prints it once. `--minutes N`
sets its lifetime, from 1 to 60, and the default is 5. `token list` and `token revoke ID` list and
revoke a workspace's tokens.

| Command | JSON result |
| --- | --- |
| `token new` | Object with `token`, `workspace`, `id`, `scopes`, `state`, `created_at`, `expires_at`, `revoked_at`, `caller` and `url`. |
| `token list` | Array of objects with `id`, `scopes`, `state` (`live`, `expired` or `revoked`), `created_at`, `expires_at`, `revoked_at` and `caller`, oldest first, or `[]`. |
| `token revoke ID` | The revoked token's object, as `token list` gives it. |

`caller` is the token's SHA-256, which its receipts name as `token_id`: `audit --caller CALLER` lists
its calls. [MCP](mcp.md) covers scopes, grants, lifetimes and both transports.

### `grant` and `grants`

`grant ACCOUNT` gives a person who signs in through the identity provider read access to one account the
workspace holds, and `--revoke` ends it. `grant --disable` takes no account: it ends the person's
sessions at once and refuses their sign-ins, and keeps their grants, until `grant --enable`. `grants`
lists the people who have signed in to the workspace and every grant each holds or held, or one person.
A person is listed from their first sign-in, with no grant.
[Running it](operations.md#sign-in-through-your-identity-provider) covers the sign-in.

Every form names one person: `--to EMAIL` by an email their provider verified, in any case, or
`--subject SUBJECT`. The people are those of the issuer in `GTM_OIDC_ISSUER`, set as `serve` has it;
without it the commands exit `1`. In the `demo` workspace, an account of the demo book can be granted
before any command has read it.

| Command | JSON result |
| --- | --- |
| `grant ACCOUNT` | Object with `account`, `account_name`, `person`, `state` (`live`, or `revoked` with `--revoke`), `changed` (false when the grant already stood so) and `receipt_id`. |
| `grant --disable`, `grant --enable` | Object with `person` (its `state` the new one), `changed`, `sessions_ended` and `receipt_id`. |
| `grants` | Array of people, first signed in first, each with `id`, `name`, `email`, `email_verified`, `subject`, `state` (`enabled` or `disabled`), `first_signed_in` and `grants`, each grant with `account`, `account_name`, `state`, `granted_at` and `revoked_at`. `[]` when no one has signed in. |

`grant` exits `2` for an ACCOUNT that is not an account ID, none where one is due or one with `--disable`
or `--enable`, both or neither of `--to` and `--subject`, a value that is not one line of printable
text, a workspace no one has signed in to, a person or an account the workspace does not have, and an
email two people share, which names the subjects it matched. A refused person or account, and an email
two people share, keep their receipt, finished as an error. See [audit](audit.md#what-each-command-adds).

### Debug commands

These work on one source by hand. [Sources](sources.md) covers each in full.

| Command | Options | JSON result |
| --- | --- | --- |
| `debug calls import FILE` | `--account ID`, `--called-at TIMESTAMP`, `--speaker-names`, `--attendees FILE`, `--cues FIRST:LAST` or `--lines FIRST:LAST`, `--reassign` | Import metadata: `schema_version`, `source`, `status`, `created`, `selected`, `reassigned`, `resolution_receipt_id`, `import_id`, `account_id`, `format`, `called_at`, `imported_at`, `content_sha256`, `requested_range`, `cited_range`, `total_units`, `unit_kind`, `selection_mode`, `speaker_names`, and with `--attendees` the number of `attendees`. No transcript text, label or email. |
| `debug calls list` | `--account ID`, `--after IMPORT_ID` | Object with `schema_version`, `source`, `imports` and `next_after`, a cursor or `null`. |
| `debug calls inspect IMPORT_ID` | `--full` | Object with `schema_version`, `source`, `import`, `body` and `units`. `--full` adds `original_text`. |
| `debug salesforce check` | `--sf-org ALIAS`, `--sf-environment`, `--as-of YYYY-MM-DD` | Object with `schema_version`, `status` (`passed` or `failed`), `as_of`, `steps`, `api_calls`, `sf_cli_invocations` and their definitions. No record. |
| `debug slack connect` | `--client-id ID`, `--redirect-uri URI`, `--user-scope` (`channels:history`, `groups:history`, `users:read`, `users:read.email`; repeat for each), `--team-id` | The sign-in's status, as `debug slack status` gives it. |
| `debug slack status` | `--check` | Object with `schema_version`, `configured`, `credential_state`, `verification`, `verified_component`, `requested_scopes`, `granted_scopes`, `rotation`, `access_expires_at`, `last_verified_at` and `needs_login`. No token and no identity. |
| `debug slack disconnect` | `--revoke` | Object with `schema_version`, `status: "disconnected"`, `local_credentials_removed`, `remote_revoke_requested`, `remote_access_token_revoked` and `revocation_error`. |
| `debug slack map` | `--account ID`, `--thread-url URL` or `--channel ID --thread-ts TS`, `--replace`, `--reassign` | Object with `schema_version`, `status`, `selection`, `thread_access`, `counts`, `duration_ms`, `next`. |
| `debug slack check` | `--account ID` | Object with `schema_version`, `status`, `steps`, `counts` and request counts. |

All of them take `--state-dir NAME` and `--json`. A failed `debug salesforce check` or `debug slack
check` prints its report on stdout and exits `4`. A `debug slack disconnect --revoke` whose revocation
Slack did not confirm prints its result and exits `5`. `--account-number`, on `debug calls import` and
`debug slack map`, is not in this build and exits `3`.

### Install commands

Each writes text only, prints a failure as `gtm-connector: MESSAGE`, and exits `1` when it fails.
With `--help` or `-h` each prints what it takes and does nothing. [Running it](operations.md) covers
them.

| Command | What it does |
| --- | --- |
| `setup` | Makes missing credentials, migrates the database, sets up the app role, and makes the `demo` workspace. Safe to run again. |
| `serve [--selection FILE]` | Runs the HTTPS server, with the web app, the API and MCP at `/mcp`, until Ctrl-C. |
| `backup --out FILE` | Writes the database and the credentials folder to one file. |
| `restore --in FILE` | Restores a backup into an empty database. |
| `health` | Asks the local server's `/healthz`, and exits `1` unless it answers ok. |
| `version` | Prints the commit the binary was built from and the Go version. |

### Not in this build

- `debug serve`, `debug doctor`, `debug verify` and `debug completion` exit `3` with "debug NAME is not
  available in this build." `serve` and `mcp stdio` serve MCP instead.
- `connect inspector`, `connect claude-code`, `connect claude-desktop` and `connect cursor` exit `3`,
  print no setup, and grant nothing.
