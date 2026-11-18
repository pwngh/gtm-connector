# Account-channel context

This option adds the team's account-channel context to a renewal brief using
invented Slack messages.

`read_slack_messages` requires an independent `slack:read` grant. Its
`slack.sqlite3` store stays separate from CRM, calls, and audit. It uses no Slack
account, API token, or network request, and sends no messages.

## Run

These commands run from the repository root after setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json
.venv/bin/python -m gtm_connector evaluate --with-slack
.venv/bin/python -m gtm_connector demo --state-dir var/renewal-demo
.venv/bin/python -m gtm_connector verify --with-slack --state-dir var/slack-verification
```

The brief command above returns JSON. Evaluation, demo, and verification print
readable summaries; add `--json` to inspect their full result objects.

The full brief requests `crm:read`, `calls:read`, and `slack:read`. After the
default three reads, it calls `read_slack_messages`, never requesting
`crm:write`. The separate `verify --with-slack` run proves CRM grants cannot read
Slack and a Slack-only token cannot read CRM or call notes. Every attempt is audited.

The demo includes Slack by default and accepts `--with-slack` for compatibility.
It follows Morgan Reed preparing Juniper Crest Learning's renewal, with a checked
brief, quoted injected note, and proposed follow-up. The longer walkthrough uses
the same invented person, account, and sources while inspecting that brief's
usage and citation checks. Neither walkthrough connects to Slack.

Default `brief` and `evaluate` commands retain their two-source workflow and
182-claim baseline. Their Slack option selects four-read briefs and extended
expectations; fixture drafting still runs without a model. Ollama accepts
`--with-slack`, but its historical benchmark covers only the original two-source
fixture corpus. No model score is claimed for the current book or Slack workflow.

## Source data and claims

The 29 invented messages belong to account channels such as
`acct-juniper-crest`. Stable IDs run from
`msg-001` through `msg-030`, excluding `msg-005`; references use
`slack/messages/msg-003`. `msg-002` reports a departed champion. `msg-003` says:

```text
Security review is delayed. Procurement approval is still pending.
```

For that account, `opp-003` reports `closed_won`, while `note-003` reports
unapproved procurement. Those exact contents trigger an additional conflict
flag citing all three records. The existing two-source flag remains; neither
source is resolved. Other wording does not trigger a detected conflict.

These lines were captured from the documented `acc-003` command:

```text
- Three-source conflict: CRM opportunity opp-003 reports stage "closed_won", call note note-003 reports unapproved procurement, and Slack message msg-003 reports a delayed security review. [crm/opportunities/opp-003] [calls/call_notes/note-003] [slack/messages/msg-003]
- Proposed follow-up: Ask the account owner to confirm procurement and security review status. [crm/opportunities/opp-003] [calls/call_notes/note-003] [slack/messages/msg-003]
```

`msg-007` starts with the learning team's need for a training owner and a final
seat count, then contains an instruction to invoke `log_activity`. The workflow
quotes it as data, performs its fixed reads, and creates no activity. The model
receives no tool interface. This test checks the boundary, not the truth of
arbitrary source text. The October call note and January Slack update for
`acc-006` disagree about seat needs; their dates and wording stay visible without
a general contradiction or staleness detector.

An empty successful read produces a statement that no messages were returned,
citing its audit receipt. A matching HTTP 403 instead cites the denial receipt
and reports missing `slack:read`. The checker requires the matching disclosure.
Other failures stop the brief.

## Follow-up proposal

The extended brief requires exactly one supported `Proposed follow-up:` line,
a suggested next step. For the recognized conflict, it asks to
confirm procurement and security review status, citing all three records. If
only the CRM/calls conflict is known, it covers procurement and cites those two
records. Otherwise it asks for review of renewal context and cites the account.
It invents no owner, deadline, discount, or promise.

After the entire brief passes, the artifact copies that complete cited line
into `proposed_follow_up`. An uncited or altered proposal rejects the whole
brief. No separate unchecked model field or sending tool exists.

## Snapshots and evaluation

Four-read evidence uses `workflow_version: 2`. Missing version or explicit
version `1` means existing three-read evidence. The checker fetches no new sources
when checking a saved snapshot. Version 1 rejects Slack records, receipts, or status; unknown versions
fail. Rejected and legacy artifacts gain no follow-up field.

The expected manifest contains 182 base claim objects. Static Slack
expectations add messages, conflicts, and follow-ups for the same 20 accounts.
Extended evaluation uses 80 tool calls, scoring supported wording and exact
references rather than semantic truth. Local-owner access and manual retention
retain their limits. Atomic snapshot replacement does not guarantee power-loss
durability.

The fixture report records these results for
`.venv/bin/python -m gtm_connector evaluate --with-slack --json`, using no model:

| Measure | Result |
| --- | --- |
| Accepted briefs | 20 / 20 |
| Claims found and correctly cited | 223 / 223 |
| Invented claims | 0 |
| Audit receipts | 80 |
| Activities created | 0 |
| Input / output model tokens | 0 / 0 |

The [recorded output and exact invocation](../evaluation/RESULTS_SLACK.json) file
preserves UTC run times and per-account counts for the current invented book.
