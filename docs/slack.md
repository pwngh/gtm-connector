# Slack context from invented messages

`--with-slack` adds an account channel's invented Slack messages to a renewal
brief. It uses no Slack account, token, or network request. To read a real
thread, see [Slack authentication](slack-auth.md) and [live Slack](slack-live.md).

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json
.venv/bin/python -m gtm_connector evaluate --with-slack
.venv/bin/python -m gtm_connector verify --with-slack --state-dir var/slack-verification
.venv/bin/python -m gtm_connector demo --state-dir var/renewal-demo
```

`demo` always includes Slack. The other three include it only with the flag.

## Rules

- `read_slack_messages` requires `slack:read`. CRM and calls grants do not
  include it, and a Slack-only token cannot read CRM or call notes.
  `verify --with-slack` checks all three denials.
- A Slack brief makes four audited reads and never requests `crm:write`.
- Messages live in `slack.sqlite3`, apart from CRM, calls, and audit. Their
  text is quoted as untrusted data, and the drafter receives no tool.
- No model runs by default, and no model score is claimed for this workflow.

## Source data

There are 29 messages, `msg-001` through `msg-030` without `msg-005`.
References look like `slack/messages/msg-003`.

| Message | Result in the brief |
| --- | --- |
| `msg-003`, a delayed security review | A three-source conflict flag with `opp-003` (`closed_won`) and `note-003` (unapproved procurement). The two-source flag stays, and neither source is resolved. |
| `msg-006`, a January seat update | Shown beside the October call note it disagrees with. Nothing is flagged. |
| `msg-007`, an instruction to call `log_activity` | Quoted as data. No activity is created. |

The conflict rule matches the exact fixture text only. There is no general
contradiction or staleness detector.

## Required lines

The checker rejects the whole brief if a required line is missing, altered, or
uncited.

| Slack read | Required line |
| --- | --- |
| Messages returned | Each message, quoted with its reference. |
| No messages | That none were returned, citing the audit receipt. |
| HTTP 403 for missing `slack:read` | That the token lacks `slack:read`, citing the denial receipt. |
| Any other failure | None. The brief stops. |

A Slack brief also needs exactly one `Proposed follow-up:` line. An accepted
brief copies it into `proposed_follow_up`. It names no owner, deadline,
discount, or promise, and nothing sends it.

| Conflict found | Proposal | Cites |
| --- | --- | --- |
| CRM, calls, and Slack | Confirm procurement and security review status. | All three records. |
| CRM and calls | Confirm procurement status. | Both records. |
| None | Review the cited renewal context. | The account. |

## Saved evidence and evaluation

A four-read fixture brief saves `workflow_version: 2`. A missing version or
`1` means three-read evidence, which may hold no Slack records, receipts, or
status. An unknown version fails. Checking a saved brief fetches nothing.

`evaluate --with-slack` checks 20 accounts with 80 tool calls and no model. It
scores exact wording and references, not semantic truth. The
[recorded run](../evaluation/RESULTS_SLACK.json) accepted 20 of 20 briefs,
found and correctly cited 223 of 223 claims, invented 0, and created 0
activities. Without the flag, `evaluate` keeps its 182-claim baseline.
