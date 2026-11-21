# Slack

Sign in to a Slack app you already have, connect that sign-in, map one thread to one CRM account,
then read it onto a [Deal card](briefs.md).

```sh
gtm-connector connect slack --seller 'Morgan Reed' --seller U12345678   # after debug slack connect; name every seller
gtm-connector debug slack map --account acc-007 --thread-url 'COPIED_THREAD_LINK'
gtm-connector debug slack check --account acc-007
gtm-connector brief acc-007
```

The connector creates no app, asks for no client secret, and accepts no pasted token. The tests use
invented responses.

> **No fallback.** A failed live read never falls back to invented messages.

## Sign in

`debug slack connect` saves a Slack sign-in in the [state folder](cli.md): the Slack user
credentials and the identity they were verified for.

> **The sign-in is a credential.** Keep the state folder private and never commit it.

The Slack app needs:

- [PKCE](https://docs.slack.dev/authentication/using-pkce/) enabled as a public client.
- The exact redirect `http://localhost:PORT/slack/callback` registered, `PORT` from 1024 to 65535.
- The Slack user scope `channels:history`, `groups:history`, or both, approved for the workspace.

No other Slack scope is requested, and a Slack grant that returns other scopes is refused. Slack
bot grants, organization-wide installs, GovSlack, confidential clients, and Slack's separate
user-centric OAuth endpoints are not supported.

| Command | What it does | Slack requests |
| --- | --- | ---: |
| `debug slack connect --client-id PUBLIC_CLIENT_ID --redirect-uri http://localhost:REGISTERED_PORT/slack/callback` | Opens the browser, waits up to ten minutes for approval, exchanges the code with PKCE, checks the identity, and saves the credentials. | At most 2 |
| `debug slack status` | Reads the saved file. A missing sign-in exits 0. A corrupt file is an error. | 0 |
| `debug slack status --check` | Verifies authentication and refreshes an expiring token once. It does not show that a thread is readable. | 1 or 2 |
| `debug slack disconnect` | Removes local credentials. Repeating it succeeds. | 0 |
| `debug slack disconnect --revoke` | Asks Slack to revoke the saved access token, then removes local credentials. If Slack does not confirm, it exits 5. Remove the app itself in Slack settings. | At most 1 |

Ctrl-C exits 130. An approval or save that already finished is not undone, so run
`debug slack status`.

| Flag | Meaning |
| --- | --- |
| `--client-id`, `--redirect-uri` | `connect` only, required. The existing app's public client ID and exact registered redirect. |
| `--user-scope` | `connect` only. `channels:history` (the default) or `groups:history` for private channels. Repeat it for both. Passing it replaces the default. |
| `--team-id` | `connect` only. Require this workspace. It is never printed. |
| `--state-dir` | Every `debug slack` action. The state folder, `var/v2` by default. Its sign-in is saved inside it, in `slack-connection`. |

| Bound | Limit |
| --- | --- |
| Slack requests in one command | One shared 20-second deadline |
| Each response | 64 KiB |
| A refresh token | Single use, usable for 30 days |

## Connect, map, check, and brief

1. `connect slack` connects the sign-in saved in the folder, beside Salesforce or beside the demo
   CRM. It contacts nobody.
2. `debug slack map` requires Slack connected and a `ready` sign-in. It makes one audited CRM
   lookup and saves the mapping. It fetches no messages.
3. `debug slack check` reads the mapped thread through `read_slack_messages` and validates its
   receipt. It reads no CRM or calls data.
4. A card reads Slack whenever Slack is connected. An account with no mapped thread gets a card
   that says so.

Slack threads are added one at a time with `debug slack map`. A Slack channel window is on the
[roadmap](ROADMAP.md).

- `connect slack --remove` detaches Slack and keeps the sign-in.
- Use the same state folder every time. Stop a `debug serve` running on it first.
- Select the parent message. A reply selected as the parent fails the check.
- A folder that has read demo Slack messages refuses live Slack. Connect it in a fresh folder.

| Command or flag | Rule |
| --- | --- |
| `connect slack` | With no saved sign-in it exits `2` and names `debug slack connect`. With one that needs a new login it exits `3`. |
| `--thread-url` | The saved workspace's HTTPS origin and the path `/archives/CHANNEL/pTIMESTAMP`. A reply link names its parent through `thread_ts`. An optional `cid` must match. Other query fields, fragments, and other workspaces are refused. |
| `--channel` with `--thread-ts` | Needed when the saved sign-in has no workspace origin. The timestamp is whole seconds, a dot, and exactly six digits. |
| `--account`, `--account-number` | The CRM account ID: `acc-007`, or the `acc-sf-` form with Salesforce. Or, Salesforce only, a number that matches exactly one account. |
| `--replace`, `--reassign` | Needed when the account already has another thread, and when the thread belongs to another account. |

## Limits

> **A read is not an archive.** Earlier edits, deletions, and retention are unknown.

- Every returned message becomes whole-sentence units. The card shows the newest and counts the
  rest, and its `NOT READ` section gives the files, attachments, and blocks that were left out.
- Ctrl-C can wait for the read in flight. Windows is not supported.

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
| Mappings in one state folder | 100, one thread per account and one account per thread |
| Retries | One after `token_expired`, with at most one refresh. One after HTTP 429 when its delay is at most five seconds. Nothing else retries, and a read never starts a login. |
| Accepted messages | Plain messages, `bot_message`, `me_message`, `file_share`, `thread_broadcast`, and, as the selected parent only, the `tombstone` that Slack's system user leaves for a deleted message |
| Refuse the whole read | Other hidden messages, edit or delete wrappers, other subtypes, and missing text |
| Counted and left out | Files, attachments, and blocks |
| Speaker on a card | The raw Slack user ID the message carries. A deleted parent carries `USLACKBOT`, which becomes its label. A message with no user, such as some app messages, has no speaker label. |
| Role on a card | `seller` when that user ID is listed in `sellers.json`, otherwise `unknown`. A Slack author is never a customer. |
| Slack's rate policy | Slack limits some commercially distributed apps outside its Marketplace to one request per minute and 15 messages per request on this method. The connector cannot tell which [policy](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/) applies. |

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
| `rotation`, `access_expires_at`, `last_verified_at` | Whether the Slack grant has a refresh token ([token rotation](https://docs.slack.dev/authentication/using-token-rotation/)), or `null`. UTC timestamps or `null`. |
| `needs_login` | Whether `connect` is needed. An expired `ready` credential with a usable refresh token can be checked instead. |

| Result | Fields |
| --- | --- |
| `debug slack map` | `schema_version`, `status` (`saved` or `unchanged`), `selection`, `thread_access: "unchecked"`, `counts` with `selected_threads`, `duration_ms`, and `next`. |
| `debug slack check` | `schema_version: 1`, `status` (`passed` or `failed`), `crm_access_checked: false`, `calls_access_checked: false`, `duration_ms`, `api_calls`, `response_bytes`, `counts`, `steps`, and `next`, the first failed step's recovery command. Request and byte counts are `null` when unknown. |
| `counts` of `debug slack check` | `auth_calls`, `message_calls`, `pages`, `raw_rows`, `messages`, `duplicates`, `refresh_attempts`, `token_retries`, `rate_retries`. |
| `steps` | `private_state`, `mapping`, `credential_binding`, `thread_read`, `receipt`. Each has `name`, `status` (`pass`, `fail`, `not_run`), `count`, `duration_ms`, `api_calls`, `response_bytes`, `code`, `message`, `next`. |

The check report holds no IDs, names, timestamps, message text, URLs, tokens, or paths. Tool results
and saved briefs hold message text, Slack message IDs, and the mapping, so keep them and the state
folder private. A `slack:read` [grant](tools.md) can read every mapped account the server serves.

## Errors

A reason sets the exit code of a command, and is a step `code` in `debug slack check`.

| Reason | Exit | Fix |
| --- | ---: | --- |
| `slack_invalid_configuration` | 2 | Correct the flags. |
| `slack_connection_busy` | 1 | Finish or cancel the other command, then retry. |
| `slack_private_state_invalid` | 3 | Use the original connection folder or an unused one. |
| `slack_credentials_invalid` | 4 | Keep the file private and reconnect in an unused folder. |
| `slack_not_connected`, `slack_reconnect_required` | 3 | Run `debug slack connect`. |
| `slack_browser_unavailable` | 1 | Retry where a system browser works. |
| `slack_callback_unavailable` | 1 | Free the registered localhost port. |
| `slack_login_timeout` | 1 | Retry and approve within ten minutes. |
| `slack_login_denied` | 3 | Retry only if you intend to approve. |
| `slack_oauth_rejected` | 3 | Check the app's PKCE setting, redirect, and workspace approval. |
| `slack_scope_mismatch` | 3 | Use a grant with exactly the selected scopes. |
| `slack_identity_mismatch` | 3 | Restore the original identity or use another folder. |
| `slack_token_type_unsupported` | 3 | Use a user grant from a public PKCE client. |
| `slack_selection_invalid`, `slack_crm_configuration_invalid`, `slack_account_not_found` | 2 | Correct the account, link, channel, or timestamp. Find the account ID with `accounts`. |
| `slack_account_ambiguous` | 3 | Pass the account ID with `--account`. |
| `slack_association_failed`, `slack_association_invalid` | 4 | The CRM lookup failed. Nothing was saved. Check the CRM connection. |
| `slack_crm_unavailable` | 5 | Check CRM access, then retry. |
| `slack_mapping_missing`, `slack_mapping_conflict` | 3 | Run `debug slack map` for the account. For a conflict, review `--replace` and `--reassign`. |
| `slack_source_mismatch` | 3 | Connect the original sources and Slack identity in the original folder. |
| `slack_identity_unchecked` | 3 | Run `debug slack status --check`. |
| `slack_thread_unavailable`, `slack_forbidden`, `slack_thread_changed`, `slack_thread_incomplete` | 3 | Check the selected parent, the history scope, and channel access. For a changed or incomplete thread, retry when it is stable. |
| `slack_resource_limit`, `slack_output_too_large` | 3 | Select a smaller thread. |
| `slack_clock_invalid` | 3 | Correct the local clock. |
| `slack_mapping_invalid`, `slack_snapshot_invalid` | 4 | Keep the private files and inspect the setup before recreating it. |
| `slack_response_invalid`, `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | 5 | Run `debug slack status`, wait, then retry. |
| `slack_storage_failed` | 1 | Fix access to the private folders, then run `debug slack status`. |

## Invented messages in the demo

A folder connected with `connect demo` reads an account channel's invented Slack messages onto every
card. It uses no Slack account, token, or network request.

```sh
.venv/bin/python -m gtm_connector connect demo
.venv/bin/python -m gtm_connector brief acc-003 --json
.venv/bin/python -m gtm_connector debug verify --with-slack --state-dir var/slack-verification
.venv/bin/python -m gtm_connector demo --state-dir var/renewal-demo   # always includes Slack
```

- `read_slack_messages` requires `slack:read`. A grant that holds only `slack:read` cannot read CRM or call notes.
- A card with Slack makes four audited reads and never requests `crm:write`.
- Messages live in `slack.sqlite3`: 29 of them, `msg-001` through `msg-030` without `msg-005`.
  Each has an author name and a count of attached files that were not read.
- On demo data the demo's own sellers are sellers without a `sellers.json`.

| Case | What the card says |
| --- | --- |
| `msg-003`, a seller's report with a channel mention, a link, and a second-hand amount | Three seller sentences under `SAID`. The mention and link are neutralized, the amount stays inside its sentence, and `NOT READ` counts the attached file. |
| `msg-007`, an instruction to call `log_activity` | Quoted sentences. No activity is created. |
| No messages | `Slack: no message was returned for this account.` |
| HTTP 403 for missing `slack:read` | `Slack: not read. The grant lacks slack:read.` |
| A card prepared without Slack, as `debug verify` does without `--with-slack` | `Slack: not requested for this card.` |
| Any other Slack failure | No card. The command stops. |
