# gtm-connector

[![Tests](https://github.com/pwngh/gtm-connector/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/pwngh/gtm-connector/actions/workflows/test.yml?query=branch%3Amain)

A Deal card for a renewal, built from Salesforce, call transcripts, and Slack. It prints no figure,
date, name, or quotation it cannot point to in a stored record. It labels who said each sentence and
on what basis, and it says what it did not read. No model writes any of it.

`gtm-connector demo`, on an invented law firm whose CRM record looks healthy:

```text
DEAL CARD: "Thornfield Wren LLP" (acc-003) as of 2027-01-15

ATTENTION, from record fields and dates
  The renewal is in 24 days and this opportunity is still open at stage "negotiation". [1][3]

RECORD VALUES, from CRM fields and connector arithmetic
  Renewal date: 2027-02-08, in 24 days [1]
  Annual recurring revenue: USD 450,000.00 [1]
  CRM risk: healthy [1]
  Account: "Thornfield Wren LLP"; industry "Legal services" [1]
  Opportunity: "Thornfield Wren LLP 2027 renewal"; stage "negotiation"; amount USD 450,000.00; close date 2027-02-01 [3]
  Contact: "Marian Holt"; role "Deputy general counsel"; email "marian@thornfield-wren.example" [2]

SAID, whole sentences as recorded; not record values
  [7] seller (listed seller identity); Slack 2027-01-12T16:00:00Z; speaker "Avery Linden"
      "Heads up [mention channel]: their general counsel will not sign until the zero-retention and client-consent rider is approved."
  [8] seller (listed seller identity); Slack 2027-01-12T16:00:00Z; speaker "Avery Linden"
      "Their procurement lead mentioned USD 300,000 for a smaller seat count."
  [9] seller (listed seller identity); Slack 2027-01-12T16:00:00Z; speaker "Avery Linden"
      "The redline is attached, see rider redline v4 [link files.thornfield-wren.example]."
  [4] seller (listed seller identity); call 2027-01-12T15:00:00Z; speaker "Morgan Reed"
      "We will have the order form countersigned by January 30."
  [5] customer (label matches contact [2]); call 2027-01-12T15:00:00Z; speaker "Marian Holt"
      "Our general counsel will not sign until the zero-retention and client-consent rider for AI processing is approved."
  [6] customer (label matches contact [2]); call 2027-01-12T15:00:00Z; speaker "Marian Holt"
      "Please do not put a date on the order form."

NOT READ
  Calls: 2 call records were read. Recordings and anything outside those records were not read.
  Slack: 1 message was read. 1 attached file was not read (messages starting at [7]). Other channels were not read.
  Neutralized in source text: 1 mention, 1 link.
```

- **Record values** come from typed CRM fields and connector arithmetic. The seller's "January 30"
  and the second-hand "USD 300,000" are sentences someone said, so they stay under SAID.
- **A speaker role** comes from identity, never from wording. A seller is a name or ID the operator
  listed. A customer is a label equal to exactly one contact of the account. Anything else is unknown.
- **A quotation** is one whole sentence. A sentence too long to show is counted, never cut.
- **Source text is data.** The channel mention and the link were neutralized before anything
  displayed them, and the card says how many were.

```mermaid
flowchart LR
    S["Salesforce · transcripts · Slack"] -->|"scoped read, receipt first"| U["Numbered units<br/>typed fields · whole sentences · speaker roles"]
    U --> C(["Deal card<br/>every character written by the connector"])
```

Every number on a card resolves to its record and the receipt of the read that returned it:

```text
$ gtm-connector trace brief-… 5
Number               5
Kind                 sentence
Record               calls/call_notes/note-003b
Receipt              b63a52fb-… (read_call_notes, ok)
Text                 "Our general counsel will not sign until the zero-retention and client-consent
                     rider for AI processing is approved."
Speaker              Marian Holt
Role                 customer (label matches contact [2])
Time                 2027-01-12T15:00:00Z
Source text SHA-256  105807d2…
```

## What it is tested against

[Ten invented deals](tests/test_cases.py), each built to break one property. These pass today:

| Deal | The card must |
| --- | --- |
| Contradiction | show the CRM amount and the amount a message reports, and blend neither |
| Speaker trap | keep a seller's promised date, and a seller's retelling, as the seller's |
| Missing data | print Missing for an empty field and no currency the record lacks |
| Injection | show mentions, links, and control characters as inert text, and keep a message's figure out of the record values |
| Long sentence | show a sentence whole or not at all |
| Name collision | call a speaker unknown when a seller and a contact share the name |
| Foreign number | resolve a number only inside its own card |
| Law-firm renewal | equal the card above, byte for byte |

## Try it

Python 3.11 or later and pipx. Invented data, no model, no external service.

```sh
pipx run --spec 'git+https://github.com/pwngh/gtm-connector.git' gtm-connector demo
```

`pipx install` with the same source keeps the command on your PATH. From a checkout,
`./scripts/demo.sh` installs the pinned dependencies, runs the tests, and runs the demo.
`gtm-connector --help` lists every command, and `--json` gives [stable output](docs/cli.md).

## Use your own sources

Each source is selected explicitly. Nothing falls back to demo data.

| Source | Commands | Guide |
| --- | --- | --- |
| Salesforce, read-only | `salesforce check --sf-org ALIAS`, `serve --crm-backend salesforce --sf-org ALIAS` | [Salesforce](docs/salesforce.md) |
| Call transcripts (TXT, VTT, SRT) | `calls import meeting.vtt --account acc-007` | [Calls](docs/calls.md) |
| Slack sign-in | `slack connect`, `slack status`, `slack disconnect` | [Slack sign-in](docs/slack.md) |
| One Slack thread per account | `slack map`, `slack check` | [Slack threads](docs/slack.md) |
| Selected accounts for a client | `serve --selection selection.json` | [Selection](docs/selection.md) |

Salesforce uses an existing `sf` CLI login. Slack uses an existing public PKCE app. Tests use
invented provider responses. No live org has been verified.

## Daily commands

```sh
gtm-connector serve --state-dir var/v2
gtm-connector connect inspector --scope crm:read --state-dir var/v2
gtm-connector brief acc-003 --with-slack --state-dir var/brief   # its own folder while serve runs
gtm-connector trace brief-REPLACE 5 --state-dir var/brief
gtm-connector audit --tool get_account --follow --state-dir var/v2
gtm-connector doctor --state-dir var/v2
```

`serve` prints one line per tool attempt with its decision and receipt ID. The Inspector CLI is the
tested client. Other assistant apps are untested. [Clients](docs/clients.md)

## What it guarantees

| Guarantee | Tests |
| --- | --- |
| One source's grant does not open another. A direct call without the scope returns HTTP 403. | [gateway](tests/test_gateway.py), [Slack](tests/test_slack_gateway.py) |
| A tool runs only after its audit receipt is committed. | [audit](tests/test_audit.py), [gateway](tests/test_gateway.py) |
| Retrying a write with the same key does not duplicate it. | [gateway](tests/test_gateway.py) |
| A note cannot direct the workflow. An instruction inside one appears quoted and creates nothing. | [briefing](tests/test_briefing.py) |
| A card is built only from checked evidence. A record of another account or a missing receipt gives no card. | [units](tests/test_units.py) |
| With `serve --selection`, a client reads only the selected accounts, under its own grant. A source it lacks the scope for is stated as not read on the card. | [selection](tests/test_selected_sources.py) |

The card shows what the records say, not whether a source is true.

## Limits

- Nothing here was tried on a real deal. The cases test correctness on invented data, not usefulness.
- The card lists sentences newest first. It does not rank them or relate one to another.
- A seller is whoever `sellers.json` in the state folder lists. Slack authors are never shown as
  customers.
- Authorization is a local owner emulator. The file owner can request every scope and edit the
  stores. Audit records are not tamper-proof. [Security review](SECURITY_REVIEW.md)
- macOS and Linux. No web interface, hosted model, or multi-user mode.

More: [commands and output](docs/cli.md), [tools, scopes, and protocol](docs/tools.md),
[Deal cards](docs/briefs.md), [audit](docs/audit.md),
[clients and Inspector](docs/clients.md), [working on the code](AGENTS.md).

MCP revision 2026-07-28, Python SDK 2.3.0. All checked-in companies, people, and records are
invented. MIT licensed.
