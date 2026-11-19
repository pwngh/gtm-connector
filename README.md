# gtm-connector

[![Tests](https://github.com/pwngh/gtm-connector/actions/workflows/test.yml/badge.svg?branch=main)](https://github.com/pwngh/gtm-connector/actions/workflows/test.yml?query=branch%3Amain)

A local connector that builds cited renewal briefs from CRM records, call transcripts, and Slack
threads. People use its CLI. Assistants use its MCP tools. Every read needs a named scope and leaves
an audit receipt, and every line of a brief cites the record it came from.

## Try it

Python 3.11 or later and pipx. Invented data, no model, no external service.

```sh
pipx run --spec 'git+https://github.com/pwngh/gtm-connector.git' gtm-connector demo
```

One rep prepares one renewal from CRM records, a call note, and a Slack message.
`gtm-connector --help` lists every command, and `--json` gives [stable output](docs/cli.md).

## What it guarantees

| Guarantee | Tests |
| --- | --- |
| One source's grant does not open another. A direct call without the scope returns HTTP 403. | [gateway](tests/test_gateway.py), [Slack](tests/test_slack_gateway.py) |
| A tool runs only after its audit receipt is committed. | [audit](tests/test_audit.py), [gateway](tests/test_gateway.py) |
| Retrying a write with the same key does not duplicate it. | [gateway](tests/test_gateway.py) |
| A note cannot direct the workflow. An instruction inside one appears quoted and creates nothing. | [briefing](tests/test_briefing.py) |
| A brief publishes only cited, supported lines. One bad line rejects the draft. | [citations](tests/test_citations.py) |
| With `serve --selection`, a client reads only the selected accounts, under its own grant. A source it lacks the scope for is disclosed as denied in the brief. | [selection](tests/test_selected_sources.py) |

The checker verifies wording and citations, not whether a source is true.

## Use your own sources

Each source is selected explicitly. Nothing falls back to demo data.

| Source | Commands | Guide |
| --- | --- | --- |
| Salesforce, read-only | `salesforce check --sf-org ALIAS`, `serve --crm-backend salesforce --sf-org ALIAS` | [Salesforce](docs/salesforce.md) |
| Call transcripts (TXT, VTT) | `calls import meeting.vtt --account acc-007` | [Calls](docs/calls.md) |
| Slack sign-in | `slack connect`, `slack status`, `slack disconnect` | [Slack sign-in](docs/slack-auth.md) |
| One Slack thread per account | `slack map`, `slack check` | [Slack threads](docs/slack-live.md) |
| Selected accounts for a client | `serve --selection selection.json` | [Selection](docs/selection.md) |

Salesforce uses an existing `sf` CLI login. Slack uses an existing public PKCE app. Tests use
invented provider responses. No live org has been verified.

## Daily commands

```sh
gtm-connector serve --state-dir var/v2
gtm-connector connect inspector --scope crm:read --state-dir var/v2
gtm-connector brief acc-007 --with-slack
gtm-connector audit --tool get_account --follow --state-dir var/v2
gtm-connector doctor --state-dir var/v2
```

`serve` prints one line per tool attempt with its decision and receipt ID. The Inspector CLI is the
tested client. Other assistant apps are untested. [Clients](docs/clients.md)

## How a request flows

```mermaid
flowchart LR
    Client["CLI or MCP client"] -->|"PKCE"| Auth["Local authorization server"]
    Auth -->|"Scoped token"| Client
    Client -->|"Tool call"| Gateway["Gateway: receipt, token, scope, rate limit"]
    Gateway --> Audit[("Audit")]
    Gateway --> Sources[("CRM, calls, Slack")]
    Client --> Draft["Drafter"] --> Check["Citation checker"] --> Brief["Brief"]
```

## Limits

- The drafter assembles fixed claims. It does not summarize free text yet.
- Authorization is a local owner emulator. The file owner can request every scope and edit the
  stores. Audit records are not tamper-proof. [Security review](SECURITY_REVIEW.md)
- macOS and Linux. No web interface, hosted model, or multi-user mode.

`evaluate` scores briefs for 20 invented accounts against fixed claims: 182 of 182 found and cited,
223 of 223 with `--with-slack`, none invented. [Scoring](docs/evaluation.md)

More: [tools and scopes](docs/tools.md), [briefs](docs/briefs.md), [audit](docs/audit.md),
[usage](docs/usage.md), [protocol](docs/protocol.md), [install](docs/install.md),
[Inspector](docs/inspector.md), [local model](docs/ollama.md), [demo kit](docs/salesforce-demo.md),
[demo Slack store](docs/slack.md), [development](docs/development.md), [agent guide](AGENTS.md).

MCP revision 2026-07-28, Python SDK 2.3.0. All checked-in companies, people, and records are
invented. MIT licensed.
