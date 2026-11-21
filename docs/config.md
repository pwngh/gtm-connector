# Setting it up

**Connect your tools once, say which renewals to watch, and decide what counts as risk.**

Everything RevOps and GTM engineers set lives in one file, `gtm.yaml`. It is kept in Git, previewed
against the book, and activated by exact revision. It holds no credential.

```yaml
sources:
  crm:   {kind: salesforce}
  chat:  {kind: slack, channels: ["#deal-*", "#ext-*", "#renewals", "#deal-desk"]}
  calls: {kind: transcripts, folder: calls/}
  gong:  {kind: gong}
  inbox: {kind: push}                       # internal email, SMS, webhook push
  app:   {kind: sql, view: renewal_contacts}  # a read-only view in your own database
watch:
  renewals_closing_within_days: 90
rules:
  customer_silence_days: 14
sellers: [Morgan Reed, Avery Linden]
timezone: America/Chicago
```

## How a deal gets its evidence

```mermaid
flowchart LR
    W["Watch rule"] --> P["Opportunities, accounts,<br/>contact emails"]
    P --> D["Discover calls and Slack threads<br/>by email and domain"]
    D -->|"matched by your rules"| C["Deal card"]
```

1. **The watch rule pulls** every open renewal opportunity that closes inside the window, with its account and contact emails.
2. **The connector discovers** matching calls and Slack messages by those emails and domains. In Slack it looks in a dedicated internal or Slack Connect channel, such as `#deal-thornfield` or `#ext-thornfield`, and in threads of shared channels, such as `#renewals` or `#deal-desk`, that mention the account's name or domain. No thread is hard-coded.
3. **Each match joins its deal.** Nobody clicks. A match that fits more than one deal goes on neither card, and both cards say so under what was not read.

## The matches queue is off

Matching is automatic, and that is the point: nobody trains the connector by confirming its work.
The queue exists for a team that wants a person in the loop, and the engineer chooses how much.

| `approval` in `gtm.yaml` | What waits for a person |
| --- | --- |
| `off`, the default | Nothing |
| `ambiguous` | Only a match that fits more than one deal |
| `all` | Every match, before it reaches a card |

The setting can also be given per source, for example `all` for pushed records and `off` for the CRM.

The engineer turns it on in two steps: set it in `gtm.yaml`, then sign in and confirm the change when
the draft is previewed. Until both are done, the queue stays off. The web app only reads this
setting. It shows the queue and its badge when the setting is on, and has no switch of its own.

A deal can also be registered by hand, with the IDs it binds. See [the API](api.md).

## Two ways data enters

| Way | What it covers |
| --- | --- |
| Scheduled background pulls | The CRM, Slack channels and threads, Gong and Zoom |
| Push API or custom SQL | Internal email, SMS, or tables in your own database |

A scheduled pull runs under a lease, so a stuck read blocks nothing else. Temporary failures are
retried. A bad credential stops the pull, and Setup shows why.

## Field mapping

Map the renewal date, recurring revenue and stage to the fields your org really uses. Setup checks
each mapping and refuses an ambiguous or mistyped one.

> **Opportunity amount never stands in for recurring revenue.** If no field is mapped, the card
> says the value is missing.

## Rules

Pick the rules and set their thresholds. Preview a change against the book before it applies: who
turns red, and what becomes unknown. Every result cites its records, its receipts and the rule
version that produced it.

## Deterministic parsing and guardrails

Under the hood:

- **Whole-sentence splitter.** A quotation is one whole sentence.
- **HTML and link neutralizer.** Mentions, links and control characters become inert words before anything shows them.
- **Speaker and role matching.** Sellers come from your list, buyers from the account's contacts. Anyone else is unknown.
- **Versioned PostgreSQL store with audit receipts.** A read gets its receipt before it runs.
- **Risk and diff rules.** Silence, slippage and conflicts, computed by the connector.

## The Ops area

| Screen | What it answers |
| --- | --- |
| Coverage | Which deals have a current card, which do not, and why |
| Setup | Each source's state and last read, and the active file beside a draft |
| Activity | What ran, with its receipt, and whether it is safe to retry |

Any line on any card opens a receipt drawer in Ops: the read that returned it, and its seal. Diffs
between two cards of a deal open the same way.

## Access

People sign in through your company's identity provider. The connector stores no password for
them. A grant names the deals a person reads, and whether they see Ops. A script or an assistant
gets a token limited to selected deals and scopes, and the token expires.

## Auto-stop and archive

On Closed Won or Closed Lost, polling halts and the final card is sealed. The deal leaves the
active list. Its cards, receipts and history stay readable.

Pause a deal to stop its scheduled reads and alerts without closing it, and resume it later.
