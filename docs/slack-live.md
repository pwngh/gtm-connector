# Read a selected Slack thread

Map one Slack thread to one CRM account, check that it can be read, then use it
in a brief. A failed live read never falls back to invented messages.

The tests use invented responses. One development `slack check` passed against
a test workspace on November 18, 2026, as recorded in the
[security review](../SECURITY_REVIEW.md). A real app's registration, retention,
and rate policy remain for the owner to check.

## Map, check, and brief

First complete [Slack authentication](slack-auth.md). Start with an unused
`--state-dir`. This example joins your thread to the invented CRM and call notes:

```sh
gtm-connector slack map --state-dir var/slack-selected \
  --connection-dir var/slack-connection --account acc-007 \
  --thread-url 'COPIED_THREAD_LINK'
gtm-connector slack check --state-dir var/slack-selected \
  --connection-dir var/slack-connection --account acc-007
gtm-connector brief acc-007 --with-slack --slack-backend slack \
  --slack-connection-dir var/slack-connection --state-dir var/slack-selected
```

| Command | What it does |
| --- | --- |
| `slack map` | Requires a `ready` connection, makes one audited CRM lookup, and saves the selection. It fetches no messages. |
| `slack check` | Reads the mapped thread through `read_slack_messages` and validates its receipt. It reads no CRM or calls data. |
| `brief` | Makes the usual CRM, calls, and Slack reads. |

- `--slack-backend slack` requires an explicit `--state-dir`. `brief` also
  requires `--with-slack`. `serve` accepts the same flags.
- `slack` actions name the credential folder with `--connection-dir`. Other
  commands name it with `--slack-connection-dir`.
- Reuse the same state folder and source flags every time. A folder keeps the
  sources it was created with. Stop a `serve` running on it before these commands.
- JSON source labels are `demo_fixture`, `salesforce`, `imported_transcript`,
  and `slack`. A label names the selection. Only a read shows it is readable.

## Select the thread

| Selector | Rule |
| --- | --- |
| `--thread-url` | The saved workspace's HTTPS origin and the path `/archives/CHANNEL/pTIMESTAMP`. A reply link names its parent through `thread_ts`. An optional `cid` must match. Other query fields, fragments, and other workspaces are refused. |
| `--channel` with `--thread-ts` | Needed when the saved connection has no workspace origin. The timestamp is whole seconds, a dot, and exactly six digits. |
| `--account` | The CRM account ID: `acc-007`, or the `acc-sf-` form with Salesforce. |
| `--account-number` | Salesforce only. It must match exactly one account. |
| `--replace` | Needed when the account already has another thread. |
| `--reassign` | Needed when the thread belongs to another account. |

- Select the parent message. A reply selected as the parent fails the check.
- A state folder holds one thread per account, one account per thread, and at
  most 100 selections. Repeating a selection returns `unchanged`.
- Selection changes are local. Saved briefs keep the mapping they were made with.

## Combine Salesforce, a transcript, and Slack

Use a signed-in Salesforce alias and an unused state folder. `slack map` and
`calls import` can run in either order, and both must name all three sources:

```sh
gtm-connector slack map --account ACCOUNT_ID --thread-url 'COPIED_THREAD_LINK' \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --calls-backend imported --state-dir var/connected-sources \
  --connection-dir var/slack-connection
gtm-connector calls import meeting.vtt --account ACCOUNT_ID \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --slack-backend slack --slack-connection-dir var/slack-connection \
  --state-dir var/connected-sources
gtm-connector brief ACCOUNT_ID --crm-backend salesforce --sf-org EXISTING_ALIAS \
  --sf-environment sandbox --calls-backend imported --with-slack \
  --slack-backend slack --slack-connection-dir var/slack-connection \
  --state-dir var/connected-sources
```

Find `ACCOUNT_ID` with `accounts --crm-backend salesforce`. The brief needs an
eligible standard Contract on the account and a transcript imported for it.
`slack check` proves neither. See [Salesforce](salesforce.md) and
[transcripts](calls.md). `--salesforce-demo-binding` cannot be combined with
live Slack or imported calls.

## Check report

`slack check --json` writes one report to stdout and progress to stderr. A
completed check that failed prints the report and exits 4.

| Field | Meaning |
| --- | --- |
| `schema_version`, `status` | `1`, and `passed` or `failed`. |
| `crm_access_checked`, `calls_access_checked` | Always `false`. |
| `duration_ms`, `api_calls`, `response_bytes` | Totals. Request and byte counts are `null` when unknown. |
| `counts` | `auth_calls`, `message_calls`, `pages`, `raw_rows`, `messages`, `duplicates`, `refresh_attempts`, `token_retries`, `rate_retries`. |
| `steps` | `private_state`, `mapping`, `credential_binding`, `thread_read`, `receipt`. Each has `name`, `status` (`pass`, `fail`, `not_run`), `count`, `duration_ms`, `api_calls`, `response_bytes`, `code`, `message`, `next`. |
| `next` | The first failed step's recovery command. |

The report holds no IDs, names, timestamps, message text, URLs, tokens, or
paths, so it can be shared. `slack map --json` returns `schema_version`,
`status` (`saved` or `unchanged`), `selection`, `thread_access: "unchecked"`,
`counts`, `duration_ms`, and `next`.

Tool results and saved briefs do contain message text, Slack message IDs, and
the mapping. Keep them, the credentials, and the state folder private. A
`slack:read` grant can read every mapped account the server serves. No tool
sends to Slack.

## Limits

A read uses only GET `https://slack.com/api/conversations.replies` and follows
cursors to the end. Pages, retries, refresh, and identity checks share one budget:

| Bound | Limit |
| --- | ---: |
| HTTP attempts, including authentication and failures | 10 |
| Response bytes across those attempts | 1 MiB |
| One authentication response | 64 KiB |
| Deadline for the whole operation | 20 seconds |
| Message rows, including duplicates | 100 |
| One message's text | 16 KiB of UTF-8 |
| All unique message text | 64 KiB of UTF-8 |
| Normalized result | 256 KiB of ASCII JSON |
| One cursor | 2,048 UTF-8 bytes |

- One `token_expired` response allows one retry with at most one refresh. One
  HTTP 429 allows one retry when its delay is at most five seconds. Nothing
  else retries, and a read never starts a login.
- A credential that Slack rejects is unusable until `slack connect` runs again.
- Plain messages, `bot_message`, `me_message`, `file_share`, and
  `thread_broadcast` are accepted. Hidden messages, edit or delete wrappers,
  other subtypes, and missing text refuse the whole read.
- Files, attachments, and blocks are counted and left out. Authors are labeled
  `Slack app`, `Slack participant`, or `Unknown Slack author`.
- A successful read means paging completed for the text the API returned up to
  the read's start. It is not an archive. Earlier edits, deletions, and
  retention are unknown.
- A live Slack brief saves `workflow_version: 5`, runs no model, and quotes
  every returned message. Checking a saved brief does not reread Slack.
- Slack limits some commercially distributed apps outside its Marketplace to
  one request per minute and 15 messages per request on this method. The
  connector cannot tell which
  [policy](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/)
  applies (read November 18, 2026).
- Ctrl-C exits 130 and can wait for the read in flight. Windows is not supported.

## Errors

In `slack map` and `brief` the reason sets the exit code. In a completed
`slack check` it is a step `code`. [Slack authentication](slack-auth.md) lists
connection errors.

| Reason | Exit | Fix |
| --- | ---: | --- |
| `slack_selection_invalid`, `slack_crm_configuration_invalid` | 2 | Correct the account, link, channel, timestamp, or CRM flags. |
| `slack_account_not_found` | 2 | Find the account ID with `accounts`. |
| `slack_account_ambiguous` | 3 | Pass the account ID with `--account`. |
| `slack_association_failed`, `slack_association_invalid` | 4 | The CRM lookup failed. Nothing was saved. Check the CRM connection. |
| `slack_crm_unavailable` | 5 | Check CRM access, then retry. |
| `slack_mapping_missing` | 3 | Run `slack map` for the account. |
| `slack_mapping_conflict` | 3 | Review `--replace` and `--reassign`. |
| `slack_source_mismatch` | 3 | Restore the original source flags, folders, and Slack identity. |
| `slack_identity_unchecked` | 3 | Run `slack status --check`. |
| `slack_thread_unavailable`, `slack_forbidden` | 3 | Check the selected parent, the history scope, and channel access. |
| `slack_thread_changed`, `slack_thread_incomplete` | 3 | Check the parent and retry when the thread is stable. |
| `slack_resource_limit`, `slack_output_too_large` | 3 | Select a smaller thread. |
| `slack_clock_invalid` | 3 | Correct the local clock. |
| `slack_mapping_invalid`, `slack_snapshot_invalid` | 4 | Keep the private files and inspect the setup before recreating it. |
| `slack_response_invalid`, `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | 5 | Run `slack status`, wait, then retry. |
| `slack_storage_failed` | 1 | Fix access to the private folders. |
