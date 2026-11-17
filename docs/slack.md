# Account-channel context

The connector exposes mock Slack messages through `read_slack_messages` and an
independent `slack:read` grant. `slack.sqlite3` is separate from CRM, calls, and
audit. No Slack account, API token, network request, or message sending is used.

## Run

After setup, run from the repository root:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json
.venv/bin/python -m gtm_connector evaluate --with-slack
.venv/bin/python -m gtm_connector demo --with-slack --state-dir var/slack-demo
```

The full brief requests `crm:read`, `calls:read`, and `slack:read`. It makes the
same three reads as the default, then `read_slack_messages`. It never requests
`crm:write`. The demo also proves that CRM grants cannot read Slack, and a
Slack-only token cannot read CRM or call notes. Every attempt is audited.

The default brief, evaluation, and five-minute script retain their two-source
workflow and measured 182-claim baseline. Selecting Slack adds four-read briefs
and the extended expectation set. Model-free fixture drafting remains the default.
Ollama accepts the same `--with-slack` flag, but the recorded Ollama benchmark
covers the default two-source task only. No three-source model score is claimed.

## Source data and claims

There are 29 messages with stable IDs `msg-001` through `msg-030`, excluding
`msg-005`. Each belongs to one synthetic account channel. References use
`slack/messages/msg-003`. `msg-002` reports a departed champion. `msg-003` says:

```text
Security review is delayed. Procurement approval is still pending.
```

For the same account, `opp-003` reports `closed_won` and `note-003` reports
unapproved procurement. The additional conflict flag requires those exact
record contents and cites all three records. It preserves the existing two-source
flag and resolves neither source. Other wording is not a detected conflict.

These lines were captured from the documented `acc-003` command:

```text
- Three-source conflict: CRM opportunity opp-003 reports stage "closed_won", call note note-003 reports unapproved procurement, and Slack message msg-003 reports a delayed security review. [crm/opportunities/opp-003] [calls/call_notes/note-003] [slack/messages/msg-003]
- Proposed follow-up: Ask the account owner to confirm procurement and security review status. [crm/opportunities/opp-003] [calls/call_notes/note-003] [slack/messages/msg-003]
```

`msg-007` contains an instruction to invoke `log_activity`. The workflow quotes
it as data, performs only its fixed reads, and creates no activity. The model
receives no tool interface. The test checks this boundary, not the truth of
arbitrary source text.

Empty and denied Slack reads differ. A successful empty result cites its audit
receipt and says no messages were returned. A matching HTTP 403 cites its denial
receipt and says the token lacks `slack:read`. The checker requires the matching
disclosure. Other failures stop the brief.

## Follow-up proposal

The extended brief requires exactly one supported `Proposed follow-up:` line.
For the recognized conflict, it asks the account owner to confirm procurement
and security review status and cites all three records. If only the CRM/calls
procurement conflict is known, it asks to confirm procurement and cites those
two records. Otherwise it asks for review of the cited renewal context and cites the account. It names no owner,
deadline, discount, or promise that the sources did not provide.

After the entire brief passes, the artifact copies that complete cited line into
`proposed_follow_up`. An uncited or altered proposal rejects the whole brief.
There is no separate unchecked model field and no sending tool.

## Snapshots and evaluation

Four-read evidence uses `workflow_version: 2`. Missing version or explicit
version `1` means the existing three-read evidence. The checker does not fetch
new sources while reading a saved snapshot. Version 1 rejects Slack records,
receipts, or status. Unknown versions fail. Rejected and legacy artifacts do not
gain a follow-up field.

The expected manifest preserves its original 182 claim objects. Its static
Slack extension adds message, conflict, and follow-up expectations for the same
20 accounts. Extended evaluation uses 80 tool calls. The score still measures
supported wording and exact references, not semantic truth. Local-owner access,
and manual retention retain their existing limits. Snapshot replacement is atomic;
it does not guarantee power-loss durability.

Measured with `.venv/bin/python -m gtm_connector evaluate --with-slack` on
2026-11-17 from `21:11:19` to `21:11:21` UTC, using the fixture adapter and no model:

| Measure | Result |
| --- | --- |
| Accepted briefs | 20 / 20 |
| Claims found and correctly cited | 223 / 223 |
| Invented claims | 0 |
| Audit receipts | 80 |
| Activities created | 0 |
| Input / output model tokens | 0 / 0 |

[Recorded output and exact invocation](../evaluation/RESULTS_SLACK.json)
