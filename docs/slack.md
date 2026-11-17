# Account-channel context

A renewal brief summarizes an account before a contract-renewal call. This option
adds the team's account-channel context using invented Slack messages.

`read_slack_messages` requires an independent `slack:read` grant. Its
`slack.sqlite3` store stays separate from CRM, calls, and audit. It uses no Slack
account, API token, or network request, and sends no messages.

## Run

These commands run from the repository root after setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json
.venv/bin/python -m gtm_connector evaluate --with-slack
.venv/bin/python -m gtm_connector demo --with-slack --state-dir var/slack-demo
```

The full brief requests `crm:read`, `calls:read`, and `slack:read`. After the
default three reads, it calls `read_slack_messages`, never requesting
`crm:write`. The demo proves CRM grants cannot read Slack and a Slack-only token
cannot read CRM or call notes. Every attempt is audited.

Default briefs, evaluation, and the five-minute script retain their two-source
workflow and measured 182-claim baseline. Slack selects four-read briefs and
extended expectations; fixture drafting still runs without a model. Ollama
accepts `--with-slack`, but its recorded benchmark covers the default two-source
task only. No three-source model score is claimed.

## Source data and claims

The 29 messages belong to synthetic account channels. Stable IDs run from
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

`msg-007` asks the reader to invoke `log_activity`. The workflow quotes it as
data, performs its fixed reads, and creates no activity. The model receives no
tool interface. This test proves the boundary, not the truth of arbitrary source
text.

An empty successful read produces a statement that no messages were returned,
citing its audit receipt. A matching HTTP 403 instead cites the denial receipt
and reports missing `slack:read`. The checker requires the matching disclosure.
Other failures stop the brief.

## Follow-up proposal

The extended brief requires exactly one supported `Proposed follow-up:` line,
a draft request to the account owner. For the recognized conflict, it asks to
confirm procurement and security review status, citing all three records. If
only the CRM/calls conflict is known, it covers procurement and cites those two
records. Otherwise it asks for review of renewal context and cites the account.
It invents no owner, deadline, discount, or promise.

After the entire brief passes, the artifact copies that complete cited line
into `proposed_follow_up`. An uncited or altered proposal rejects the whole
brief. No separate unchecked model field or sending tool exists.

## Snapshots and evaluation

Four-read evidence uses `workflow_version: 2`. Missing version or explicit
version `1` means existing three-read evidence. A saved snapshot fetches no new
sources. Version 1 rejects Slack records, receipts, or status; unknown versions
fail. Rejected and legacy artifacts gain no follow-up field.

The expected manifest retains its original 182 claim objects. Static Slack
expectations add messages, conflicts, and follow-ups for the same 20 accounts.
Extended evaluation uses 80 tool calls, scoring supported wording and exact
references rather than semantic truth. Local-owner access and manual retention
retain their limits. Atomic snapshot replacement does not guarantee power-loss
durability.

The fixture adapter produced these results with `.venv/bin/python -m gtm_connector evaluate --with-slack`
on 2026-11-17 from `21:11:19` to `21:11:21` UTC, using no model:

| Measure | Result |
| --- | --- |
| Accepted briefs | 20 / 20 |
| Claims found and correctly cited | 223 / 223 |
| Invented claims | 0 |
| Audit receipts | 80 |
| Activities created | 0 |
| Input / output model tokens | 0 / 0 |

The [Recorded output and exact invocation](../evaluation/RESULTS_SLACK.json) file preserves the run details.
