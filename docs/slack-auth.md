# Authenticate an existing Slack app

`slack connect` saves a user grant for a Slack app you already have. It reads
no messages. Afterwards, [select and read a thread](slack-live.md). The tests
use invented responses.

The connector does not create an app, ask for a client secret, or accept a
pasted token. The existing app needs:

- PKCE enabled as a public client.
- The exact redirect `http://localhost:PORT/slack/callback` registered, with
  `PORT` from 1024 to 65535.
- The user scope `channels:history`, `groups:history`, or both, approved for
  the workspace.

## Commands

```sh
gtm-connector slack connect --client-id PUBLIC_CLIENT_ID \
  --redirect-uri http://localhost:REGISTERED_PORT/slack/callback
gtm-connector slack status
gtm-connector slack status --check --json
gtm-connector slack disconnect
gtm-connector slack disconnect --revoke
```

| Command | What it does | Slack requests |
| --- | --- | ---: |
| `connect` | Opens the browser, waits up to ten minutes for approval, exchanges the code with PKCE, checks the identity, and saves the credentials. | At most 2 |
| `status` | Reads the saved file. A missing connection exits 0. A corrupt file is an error. | 0 |
| `status --check` | Verifies authentication and refreshes an expiring token once. It does not show that a thread is readable. | 1 or 2 |
| `disconnect` | Removes local credentials. Repeating it succeeds. | 0 |
| `disconnect --revoke` | Asks Slack to revoke the saved access token, then removes local credentials. If Slack does not confirm, it exits 5. Remove the app itself in Slack settings. | At most 1 |

| `connect` flag | Meaning |
| --- | --- |
| `--client-id`, `--redirect-uri` | Required. The existing app's public client ID and exact registered redirect. |
| `--user-scope` | `channels:history` (the default) or `groups:history` for private channels. Repeat it for both. Passing it replaces the default. |
| `--team-id` | Require this workspace. It is never printed. |

Every action takes `--connection-dir` (default `var/slack-connection`) and
`--json`.

## Rules

- No other scope is requested, and a grant that returns other scopes is refused.
- Reconnecting in a folder must return the same app, workspace, and user. Use
  another folder for another connection.
- Slack requests in one command share a 20-second deadline, and each response
  is limited to 64 KiB. No command retries in a loop.
- Ctrl-C exits 130. Run `slack status` afterwards, because an approval or save
  that already finished is not undone.
- A refresh token is single use and usable for 30 days. An unclear refresh
  outcome requires `slack connect`.
- Bot grants, organization-wide installs, GovSlack, confidential clients, and
  Slack's separate user-centric OAuth endpoints are not supported.
- Keep the credential folder apart from source state and audit folders, and
  never commit it. Tokens and workspace identities are never printed.

## JSON results

Results go to stdout. Progress and errors go to stderr. `connect` and `status`
return:

| Field | Meaning |
| --- | --- |
| `schema_version` | Integer `1`. |
| `configured` | Whether a saved credential file exists. |
| `credential_state` | `missing`, `ready`, `identity_unchecked` (run `status --check`), or `refresh_pending` (run `connect`; it does not mean a refresh is running). |
| `verification` | `saved` for local inspection. `live` when this command checked with Slack. |
| `verified_component` | Always `authentication`, never message access. |
| `requested_scopes`, `granted_scopes` | Arrays in a fixed order. |
| `rotation` | Whether the grant has a refresh token, or `null`. |
| `access_expires_at`, `last_verified_at` | UTC timestamps or `null`. |
| `needs_login` | Whether `connect` is needed. An expired `ready` credential with a usable refresh token can be checked instead. |

`disconnect` returns `schema_version`, `status: "disconnected"`,
`local_credentials_removed`, `remote_revoke_requested`,
`remote_access_token_revoked`, and `revocation_error` (a fixed code or `null`).
A progress line has `progress.command`, `step`, and `status`. An error has
`error.code`, `error.message`, and `error.next`, and its message starts with
one of the reasons below. Provider error text is never included.

## Errors

| Reason | Exit | Fix |
| --- | ---: | --- |
| `slack_invalid_configuration` | 2 | Correct the flags. |
| `slack_connection_busy` | 1 | Finish or cancel the other command, then retry. |
| `slack_private_state_invalid` | 3 | Use the original connection folder or an unused one. |
| `slack_credentials_invalid` | 4 | Keep the file private and reconnect in an unused folder. |
| `slack_not_connected` | 3 | Run `slack connect`. |
| `slack_browser_unavailable` | 1 | Retry where a system browser works. |
| `slack_callback_unavailable` | 1 | Free the registered localhost port. |
| `slack_login_timeout` | 1 | Retry and approve within ten minutes. |
| `slack_login_denied` | 3 | Retry only if you intend to approve. |
| `slack_oauth_rejected` | 3 | Check the app's PKCE setting, redirect, and workspace approval. |
| `slack_scope_mismatch` | 3 | Use a grant with exactly the selected scopes. |
| `slack_identity_mismatch` | 3 | Restore the original identity or use another folder. |
| `slack_token_type_unsupported` | 3 | Use a user grant from a public PKCE client. |
| `slack_reconnect_required` | 3 | Run `slack connect` again. |
| `slack_response_invalid`, `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | 5 | Run `slack status`, wait, then retry. |
| `slack_storage_failed` | 1 | Fix access to the folder, then run `slack status`. |

Slack references, read November 18, 2026:
[PKCE](https://docs.slack.dev/authentication/using-pkce/) and
[token rotation](https://docs.slack.dev/authentication/using-token-rotation/).
