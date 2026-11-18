# Authenticate an existing Slack app

`slack connect` saves a user grant for an existing Slack app. It does not select
channels, read messages, start the connector, or connect an assistant. The
default demo and `--with-slack` still use invented local messages.

The app must already be configured as a public PKCE client, with the exact
`http://localhost:PORT/slack/callback` redirect registered and the requested
user history scopes approved for the workspace. A Slack browser login alone
does not provide this registration. The connector does not create an app,
request a client secret, or accept a pasted token.

Replace the two named values with the existing app configuration:

```sh
gtm-connector slack connect --client-id PUBLIC_CLIENT_ID \
  --redirect-uri http://localhost:REGISTERED_PORT/slack/callback
```

The command opens the system browser and waits for approval for up to ten
minutes. It exchanges the code with PKCE, checks the returned identity, and
saves private credentials before showing success in the browser. Ctrl-C stops
the local attempt. A browser tab already opened can remain visible. Check
`gtm-connector slack status` afterwards because an interruption immediately
after a successful save does not undo that save or remote authorization.

Only `channels:history` is requested by default. Explicit `--user-scope`
options replace that default. Pass `--user-scope groups:history` for only
private-channel history, or both flags for both grants. No bot, message-writing,
search, direct-message or administrative scope is requested. An existing grant
with extra scopes is refused instead of silently widening the selection.

`--team-id` optionally pins the expected workspace during authorization. The
command does not print that identity. Reconnecting an existing directory must
preserve its original app, workspace and user. Use a different private directory
for a different connection.

## Check authentication

```sh
gtm-connector slack status
gtm-connector slack status --check --json
```

Local status creates nothing and makes no provider call. It reports saved
scopes, expiry, verification time and whether login is needed. A missing
connection is a normal result. A corrupt file is an error.

`--check` contacts Slack to verify authentication. It can rotate an expiring
token once. It does not establish access to a particular thread. Transient
failures return an actionable error; the command does not retry or sleep in a
loop. A later bounded source read will establish actual message access.

Credentials use `var/slack-connection` by default. Select another folder with
`--connection-dir` before or after the action. This is separate from connector
source state, imported transcripts and audit storage. Do not reuse those
folders. Never commit credential files or put them in shared storage.

## Disconnect

```sh
gtm-connector slack disconnect
gtm-connector slack disconnect --revoke
```

Plain disconnect removes local credentials without contacting Slack. Repeating
it succeeds and reports that credentials were already absent. `--revoke` makes
one request to revoke the saved access token, then removes local credentials.
It does not refresh merely to revoke. If Slack does not confirm revocation,
the command reports the local outcome on stdout, explains the uncertainty on
stderr, and exits 5. App removal and full authorization cleanup remain in Slack
settings. Existing source snapshots are not deleted.

## Recovery and limits

A rotating token is marked as pending before the refresh request. An ambiguous
refresh outcome requires browser authorization because reusing a single-use
refresh token could lose the connection. A successfully received replacement
is saved before its identity check. A temporary check failure can therefore
resume with `slack status --check` while the replacement is unexpired, without
repeating the refresh. Identity
mismatch or a definitive rejection of saved credentials refuses subsequent
use and requires reconnecting the original identity. The saved `refresh_pending`
state also represents this unsafe credential outcome, including a nonrotating
grant. It does not mean a refresh is currently running.

An explicit check first marks saved credentials as `identity_unchecked`. If
that save fails, no check request starts. A temporary failure leaves a check-only
retry, and a failed attempt to mark rejected credentials cannot restore a usable
`ready` state. A later read must finish verification before receiving the token.
If an unchecked access token expires before verification can finish, status
requires browser authorization. It does not recommend another check or blindly
refresh an unverified credential.

One local process holds the connection lock. A competing command fails promptly
with a retry instruction. Existing private files are preserved on invalid
configuration, denied consent, a scope mismatch or failed reauthentication
before publication. Files are replaced atomically; this does not promise
rollback of an authorization or refresh already completed by Slack.

Provider HTTP uses a shared 20-second deadline and a 64 KiB response bound.
Connect uses at most two API requests; a fresh check uses one, and a rotating
check uses at most two. Status and local disconnect use zero. Explicit revoke
uses at most one. Filesystem synchronization is synchronous and is not covered
by the cancellable HTTP deadline.

The implemented flow uses Slack's standard PKCE authorization endpoints and
user tokens. It supports rotating and nonrotating grants, with the documented
30-day local refresh deadline for PKCE. Bot grants, organization-wide installs,
GovSlack, confidential clients and Slack's separate user-centric OAuth endpoint
pair are outside this flow. Tokens and workspace identities never appear in
ordinary results, progress, browser responses or fixed error messages.

## JSON results and errors

Every action accepts `--json`. Successful data goes to stdout. Progress and
errors go to stderr. Connection and status results have this stable shape:

| Field | Meaning |
| --- | --- |
| `schema_version` | Integer `1`. |
| `configured` | Whether a saved credential document exists. |
| `credential_state` | `missing`, `ready`, `refresh_pending`, or `identity_unchecked`. |
| `verification` | `saved` for local inspection; `live` only after this operation checked authentication over HTTP. |
| `verified_component` | `authentication`; it does not claim message access. |
| `requested_scopes`, `granted_scopes` | Arrays in stable history-scope order. Missing state has empty arrays. |
| `rotation` | Whether the grant has a refresh credential; `null` for missing state. |
| `access_expires_at`, `last_verified_at` | UTC timestamps or `null`. |
| `needs_login` | Whether browser authorization is needed. An expired ready credential with a usable refresh token can be checked instead. An expired unchecked credential needs browser authorization. |

Missing local status exits `0` with `configured: false`, `credential_state:
"missing"`, `verification: "saved"`, null timestamps, and `needs_login: true`.
Here `saved` means local inspection, not proof that a credential exists. A
pending replacement can remain configured while being unusable for a read.

Disconnect returns integer `schema_version: 1`, string `status:
"disconnected"`, booleans `local_credentials_removed`, `remote_revoke_requested`
and `remote_access_token_revoked`, and `revocation_error`, a fixed code or null.
The reported local outcome is independent of unconfirmed remote revocation.

JSON progress is one object per stderr line, with `progress.command`, `step`
and `status`. Errors retain the CLI envelope `error.code`, `error.message` and
`error.next`. The message begins with one fixed Slack reason below. Provider
error text and supplied private values are never interpolated.

| Fixed reason | Exit | Recovery |
| --- | ---: | --- |
| `slack_invalid_configuration` | 2 | Correct the existing app's supported flags using `slack connect --help`. |
| `slack_connection_busy` | 1 | Finish or cancel the competing connection command, then retry. |
| `slack_private_state_invalid` | 3 | Use the original private connection folder or an unused folder. |
| `slack_credentials_invalid` | 4 | Preserve the invalid file privately and reconnect in an unused folder. |
| `slack_not_connected` | 3 | Connect an existing registered app. |
| `slack_browser_unavailable` | 1 | Retry in a session with a working system browser. |
| `slack_callback_unavailable` | 1 | Free the registered localhost port and retry. |
| `slack_login_timeout` | 1 | Retry and finish browser approval within ten minutes. |
| `slack_login_denied` | 3 | Retry only when the operator intends to approve the selected grants. |
| `slack_oauth_rejected` | 3 | Check the existing app's PKCE, callback and workspace approval. |
| `slack_scope_mismatch` | 3 | Use a grant with exactly the selected history scopes. |
| `slack_identity_mismatch` | 3 | Restore the original identity or use another private directory. |
| `slack_token_type_unsupported` | 3 | Use a user-only public-client PKCE grant. |
| `slack_reconnect_required` | 3 | Repeat browser authorization; ambiguous refresh state cannot be reused. |
| `slack_response_invalid` | 5 | Check saved status, then retry or reconnect as indicated. |
| `slack_rate_limited` | 5 | Wait before retrying the explicit command. |
| `slack_timeout`, `slack_unavailable` | 5 | Retry when reachable; inspect saved status first. |
| `slack_storage_failed` | 1 | Correct private storage access and inspect saved status before retrying. |

## Proof

[Provider and private-state tests](../tests/test_slack_auth.py),
[callback tests](../tests/test_slack_callback.py), and
[composed HTTP tests](../tests/test_slack_auth_integration.py) exercise exact
requests, grants, identity, atomic-save failures, process locks, immediate retry,
streaming deadlines and cancellation. [CLI tests](../tests/test_slack_cli.py)
check lazy help, safe output and recovery. These invented responses and actual
loopback sockets prove only the implemented request and state contracts. No real Slack login or thread
read has been verified. Actual use requires the existing app registration,
matching callback and workspace approval described above. No app, account,
message, tunnel, deployment or hosted assistant was created for this slice.

References read on November 18, 2026: [Slack PKCE](https://docs.slack.dev/authentication/using-pkce/),
[token exchange](https://docs.slack.dev/reference/methods/oauth.v2.access/),
[token rotation](https://docs.slack.dev/authentication/using-token-rotation/),
[identity check](https://docs.slack.dev/reference/methods/auth.test/), and
[access-token revocation](https://docs.slack.dev/reference/methods/auth.revoke/).
