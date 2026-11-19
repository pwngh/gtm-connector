# Slack

Sign in to a Slack app you already have, map one thread to one CRM account, then read it onto a
[Deal card](briefs.md).
The connector creates no app, asks for no client secret, and accepts no pasted token. A failed live
read never falls back to invented messages. The tests use invented responses.

## Sign in

The app needs:

- [PKCE](https://docs.slack.dev/authentication/using-pkce/) enabled as a public client.
- The exact redirect `http://localhost:PORT/slack/callback` registered, `PORT` from 1024 to 65535.
- The user scope `channels:history`, `groups:history`, or both, approved for the workspace.

| Command | What it does | Slack requests |
| --- | --- | ---: |
| `slack connect --client-id PUBLIC_CLIENT_ID --redirect-uri http://localhost:REGISTERED_PORT/slack/callback` | Opens the browser, waits up to ten minutes for approval, exchanges the code with PKCE, checks the identity, and saves the credentials. | At most 2 |
| `slack status` | Reads the saved file. A missing connection exits 0. A corrupt file is an error. | 0 |
| `slack status --check` | Verifies authentication and refreshes an expiring token once. It does not show that a thread is readable. | 1 or 2 |
| `slack disconnect` | Removes local credentials. Repeating it succeeds. | 0 |
| `slack disconnect --revoke` | Asks Slack to revoke the saved access token, then removes local credentials. If Slack does not confirm, it exits 5. Remove the app itself in Slack settings. | At most 1 |

| Flag | Meaning |
| --- | --- |
| `--client-id`, `--redirect-uri` | `connect` only, required. The existing app's public client ID and exact registered redirect. |
| `--user-scope` | `connect` only. `channels:history` (the default) or `groups:history` for private channels. Repeat it for both. Passing it replaces the default. |
| `--team-id` | `connect` only. Require this workspace. It is never printed. |
| `--connection-dir` | Every `slack` action. The credential folder, default `var/slack-connection`. |

- No other scope is requested, and a grant that returns other scopes is refused.
- Slack requests in one command share a 20-second deadline. Each response is limited to 64 KiB.
- Ctrl-C exits 130. An approval or save that already finished is not undone, so run `slack status`.
- A refresh token is single use and usable for 30 days.
- Bot grants, organization-wide installs, GovSlack, confidential clients, and Slack's separate
  user-centric OAuth endpoints are not supported.
- Keep the credential folder apart from source state and audit folders, and never commit it.

## Map, check, and brief

Choose an unused `--state-dir`.

```sh
gtm-connector slack map --state-dir var/slack-selected --account acc-007 \
  --thread-url 'COPIED_THREAD_LINK'
gtm-connector slack check --state-dir var/slack-selected --account acc-007
gtm-connector brief acc-007 --with-slack --slack-backend slack \
  --slack-connection-dir var/slack-connection --state-dir var/slack-selected
```

| Command or flag | Rule |
| --- | --- |
| `slack map` | Requires a `ready` connection, makes one audited CRM lookup, and saves the selection. It fetches no messages. |
| `slack check` | Reads the mapped thread through `read_slack_messages` and validates its receipt. It reads no CRM or calls data. |
| `--thread-url` | The saved workspace's HTTPS origin and the path `/archives/CHANNEL/pTIMESTAMP`. A reply link names its parent through `thread_ts`. An optional `cid` must match. Other query fields, fragments, and other workspaces are refused. |
| `--channel` with `--thread-ts` | Needed when the saved connection has no workspace origin. The timestamp is whole seconds, a dot, and exactly six digits. |
| `--account`, `--account-number` | The CRM account ID: `acc-007`, or the `acc-sf-` form with Salesforce. Or, Salesforce only, a number that matches exactly one account. |
| `--replace`, `--reassign` | Needed when the account already has another thread, and when the thread belongs to another account. |

- `slack` actions take `--connection-dir`. Other commands take `--slack-connection-dir`.
- Reuse the same state folder and source flags every time. Stop a `serve` running on it first.
- Select the parent message. A reply selected as the parent fails the check.
- With Salesforce or an imported transcript, `slack map`, `calls import`, and `brief` must each name
  all three sources on one unused folder. See [calls](calls.md). `--salesforce-demo-binding` cannot
  be combined with live Slack or imported calls.

## Limits

| Bound | Limit |
| --- | --- |
| Slack request | GET `https://slack.com/api/conversations.replies` only, following cursors to the end. Pages, retries, refresh, and identity checks share the budget below. |
| HTTP attempts, including authentication and failures | 10 |
| Response bytes across those attempts | 1 MiB |
| One authentication response | 64 KiB |
| Deadline for the whole operation | 20 seconds |
| Message rows, including duplicates | 100 |
| One message's text | 16 KiB of UTF-8 |
| All unique message text | 64 KiB of UTF-8 |
| Normalized result | 256 KiB of ASCII JSON |
| One cursor | 2,048 UTF-8 bytes |
| Selections in one state folder | 100, one thread per account and one account per thread |
| Retries | One after `token_expired`, with at most one refresh. One after HTTP 429 when its delay is at most five seconds. Nothing else retries, and a read never starts a login. |
| Accepted messages | Plain messages, `bot_message`, `me_message`, `file_share`, `thread_broadcast`, and, as the selected parent only, the `tombstone` that Slack's system user leaves for a deleted message |
| Refuse the whole read | Other hidden messages, edit or delete wrappers, other subtypes, and missing text |
| Counted and left out | Files, attachments, and blocks |
| Speaker on a card | The raw Slack user ID. A message from an app, a deleted message, or one with no user has no speaker label. |
| Role on a card | `seller` when that user ID is listed in `sellers.json`, otherwise `unknown`. A Slack author is never a customer. |
| Slack's rate policy | Slack limits some commercially distributed apps outside its Marketplace to one request per minute and 15 messages per request on this method. The connector cannot tell which [policy](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/) applies. |

- A read is not an archive. Earlier edits, deletions, and retention are unknown.
- Every returned message becomes whole-sentence units. The card shows the newest and counts the
  rest, and its `NOT READ` section gives the files, attachments, and blocks that were left out.
- Ctrl-C can wait for the read in flight. Windows is not supported.

## JSON results

`disconnect` returns `schema_version`, `status: "disconnected"`, `local_credentials_removed`,
`remote_revoke_requested`, `remote_access_token_revoked`, and `revocation_error` (a fixed code or
`null`). An error message starts with one of the reasons under Errors, and provider error text
is never included. `connect` and `status` return:

| Field | Meaning |
| --- | --- |
| `schema_version`, `configured` | Integer `1`, and whether a saved credential file exists. |
| `credential_state` | `missing`, `ready`, `identity_unchecked` (run `status --check`), or `refresh_pending` (run `connect`; it does not mean a refresh is running). |
| `verification`, `verified_component` | `saved` for local inspection, or `live` when this command checked with Slack. Always `authentication`, never message access. |
| `requested_scopes`, `granted_scopes` | Arrays in a fixed order. |
| `rotation`, `access_expires_at`, `last_verified_at` | Whether the grant has a refresh token ([token rotation](https://docs.slack.dev/authentication/using-token-rotation/)), or `null`. UTC timestamps or `null`. |
| `needs_login` | Whether `connect` is needed. An expired `ready` credential with a usable refresh token can be checked instead. |

| Result | Fields |
| --- | --- |
| `slack map` | `schema_version`, `status` (`saved` or `unchanged`), `selection`, `thread_access: "unchecked"`, `counts`, `duration_ms`, and `next`. |
| `slack check` | `schema_version: 1`, `status` (`passed` or `failed`), `crm_access_checked: false`, `calls_access_checked: false`, `duration_ms`, `api_calls`, `response_bytes`, `counts`, `steps`, and `next`, the first failed step's recovery command. Request and byte counts are `null` when unknown. |
| `counts` | `auth_calls`, `message_calls`, `pages`, `raw_rows`, `messages`, `duplicates`, `refresh_attempts`, `token_retries`, `rate_retries`. |
| `steps` | `private_state`, `mapping`, `credential_binding`, `thread_read`, `receipt`. Each has `name`, `status` (`pass`, `fail`, `not_run`), `count`, `duration_ms`, `api_calls`, `response_bytes`, `code`, `message`, `next`. |

The check report holds no IDs, names, timestamps, message text, URLs, tokens, or paths. Tool results
and saved briefs hold message text, Slack message IDs, and the mapping, so keep them and the state
folder private. A `slack:read` grant can read every mapped account the server serves.

## Errors

A reason sets the exit code of a command, and is a step `code` in `slack check`.

| Reason | Exit | Fix |
| --- | ---: | --- |
| `slack_invalid_configuration` | 2 | Correct the flags. |
| `slack_connection_busy` | 1 | Finish or cancel the other command, then retry. |
| `slack_private_state_invalid` | 3 | Use the original connection folder or an unused one. |
| `slack_credentials_invalid` | 4 | Keep the file private and reconnect in an unused folder. |
| `slack_not_connected`, `slack_reconnect_required` | 3 | Run `slack connect`. |
| `slack_browser_unavailable` | 1 | Retry where a system browser works. |
| `slack_callback_unavailable` | 1 | Free the registered localhost port. |
| `slack_login_timeout` | 1 | Retry and approve within ten minutes. |
| `slack_login_denied` | 3 | Retry only if you intend to approve. |
| `slack_oauth_rejected` | 3 | Check the app's PKCE setting, redirect, and workspace approval. |
| `slack_scope_mismatch` | 3 | Use a grant with exactly the selected scopes. |
| `slack_identity_mismatch` | 3 | Restore the original identity or use another folder. |
| `slack_token_type_unsupported` | 3 | Use a user grant from a public PKCE client. |
| `slack_selection_invalid`, `slack_crm_configuration_invalid`, `slack_account_not_found` | 2 | Correct the account, link, channel, timestamp, or CRM flags. Find the account ID with `accounts`. |
| `slack_account_ambiguous` | 3 | Pass the account ID with `--account`. |
| `slack_association_failed`, `slack_association_invalid` | 4 | The CRM lookup failed. Nothing was saved. Check the CRM connection. |
| `slack_crm_unavailable` | 5 | Check CRM access, then retry. |
| `slack_mapping_missing`, `slack_mapping_conflict` | 3 | Run `slack map` for the account. For a conflict, review `--replace` and `--reassign`. |
| `slack_source_mismatch` | 3 | Restore the original source flags, folders, and Slack identity. |
| `slack_identity_unchecked` | 3 | Run `slack status --check`. |
| `slack_thread_unavailable`, `slack_forbidden`, `slack_thread_changed`, `slack_thread_incomplete` | 3 | Check the selected parent, the history scope, and channel access. For a changed or incomplete thread, retry when it is stable. |
| `slack_resource_limit`, `slack_output_too_large` | 3 | Select a smaller thread. |
| `slack_clock_invalid` | 3 | Correct the local clock. |
| `slack_mapping_invalid`, `slack_snapshot_invalid` | 4 | Keep the private files and inspect the setup before recreating it. |
| `slack_response_invalid`, `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | 5 | Run `slack status`, wait, then retry. |
| `slack_storage_failed` | 1 | Fix access to the private folders, then run `slack status`. |

## Invented messages in the demo

`--with-slack` adds an account channel's invented Slack messages to a card. It uses no Slack
account, token, or network request.

```sh
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json
.venv/bin/python -m gtm_connector verify --with-slack --state-dir var/slack-verification
.venv/bin/python -m gtm_connector demo --state-dir var/renewal-demo   # always includes Slack
```

- `read_slack_messages` requires `slack:read`. A Slack-only token cannot read CRM or call notes.
- A card with Slack makes four audited reads and never requests `crm:write`.
- Messages live in `slack.sqlite3`: 29 of them, `msg-001` through `msg-030` without `msg-005`.
  Each has an author name, which is a seller only when `sellers.json` lists it, and a count of
  attached files that were not read.

| Case | What the card says |
| --- | --- |
| `msg-003`, a seller's report with a channel mention, a link, and a second-hand amount | Three seller sentences under `SAID`. The mention and link are neutralized, the amount stays inside its sentence, and `NOT READ` counts the attached file. |
| `msg-007`, an instruction to call `log_activity` | Quoted sentences. No activity is created. |
| No messages | `Slack: no message was returned for this account.` |
| HTTP 403 for missing `slack:read` | `Slack: not read. The grant lacks slack:read.` |
| `brief` without `--with-slack` | `Slack: not requested for this card.` |
| Any other Slack failure | No card. The command stops. |
