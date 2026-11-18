# Read a selected Slack thread

Authenticate an existing Slack app, map one thread to a CRM account, then check
or read that selection. These are separate operations. The demo continues to
use invented messages. Live reads require an explicit source mode and private
state folder; a failed read never substitutes fixtures.

No real Slack login or thread read has been verified. The tests use invented
responses and local HTTPS. Actual use requires an existing approved public
PKCE app, the exact registered localhost callback, and a user history grant.
The connector does not create an app or connect a hosted assistant.

## Authenticate, select, and use

First complete [Slack authentication](slack-auth.md). Replace uppercase values
with your existing app configuration and your selected thread link. Run these
commands yourself in a session with a working browser:

```sh
gtm-connector slack connect --client-id PUBLIC_CLIENT_ID \
  --redirect-uri http://localhost:REGISTERED_PORT/slack/callback \
  --connection-dir var/slack-connection
gtm-connector slack status --connection-dir var/slack-connection
```

The default grant is `channels:history`. Select `groups:history` explicitly for
private-channel history, or request both as described in the authentication
guide. The connector does not infer visibility from a channel ID. Saved status
is offline; `slack status --check` checks authentication without reading messages.

This example combines the invented CRM and call-note book with your selected
Slack thread. Start with an unused source folder:

```sh
gtm-connector slack map --state-dir var/slack-selected \
  --connection-dir var/slack-connection --account acc-007 \
  --thread-url 'COPIED_THREAD_LINK'
gtm-connector slack check --state-dir var/slack-selected \
  --connection-dir var/slack-connection --account acc-007
gtm-connector brief acc-007 --with-slack --slack-backend slack \
  --slack-connection-dir var/slack-connection --state-dir var/slack-selected
```

`slack map` validates the saved Slack identity and makes one audited CRM lookup.
It saves a selection without fetching Slack messages. A successful map says
that access remains unchecked. `slack check` then reads the selected thread
through the existing `slack:read` tool and validates its committed receipt.
It does not access CRM or calls, refresh Salesforce credentials, or initialize
missing source state. A brief performs the ordinary CRM, calls, and Slack reads.

The human source line identifies the chosen sources. JSON source labels use
`demo_fixture`, `salesforce`, `imported_transcript`, and `slack` as applicable.
A selected source label is not a claim that its records are readable. The read
establishes that narrower result. `serve` accepts the same live Slack flags and
requires explicit client grants before returning records.

Authentication uses `--connection-dir`. Daily `brief`, `serve`, Salesforce
account inspection, and `calls import` use `--slack-connection-dir`. Both name
the same separate credential folder. Always reuse the explicit source folder
and source modes chosen during initialization.

## Select the parent and resolve conflicts

A copied link must use the saved workspace's HTTPS origin and its supported
`/archives/CHANNEL/pTIMESTAMP` path. A reply link can identify its parent through
`thread_ts`; an optional `cid` must match the path. Unknown query fields,
fragments, credentials in URLs, and other workspace origins refuse. A copied
URL never chooses a credential or HTTP destination.

If the saved connection has no supported workspace origin, use the native
channel and exact parent timestamp instead:

```sh
gtm-connector slack map --state-dir var/slack-selected --account acc-007 \
  --channel CHANNEL_ID --thread-ts PARENT_TIMESTAMP
```

The timestamp has integer seconds and exactly six fractional digits. The first
page must contain that selected parent. Selecting a reply without its parent
fails the subsequent check; the connector does not silently move the selection.

There is one selected thread per account, and one account per selected thread
within the pinned source state. Repeating the same selection is unchanged.
Replacing an account's thread requires `--replace`. Moving an already selected
thread to another account requires `--reassign`. If the destination account also
has a different thread, both flags are required. These flags change only local
selection metadata. They never write to CRM or Slack. Historical brief snapshots
retain their original mapping.

Salesforce selection accepts `--account-number` or the canonical `--account`
identifier. AccountNumber must resolve to one literal match through the bounded
CRM tool; blank, missing, duplicated, or case-ambiguous values refuse. Native IDs
are the fallback. No account name or domain is inferred from Slack text. The
raw AccountNumber remains in the private owner-side lookup receipt; it is not
copied into Slack source provenance.

## Combine Salesforce, a transcript, and Slack

Use an existing signed-in Salesforce alias and an unused private source folder.
This example selects a sandbox; use `developer` or `production` only when that
is the intended environment. Obtain `ACCOUNT_ID` from the account commands or
an earlier import result. It has the canonical `acc-sf-` prefix. Replace the
transcript path and copied link with your own selections.

```sh
gtm-connector accounts --crm-backend salesforce --sf-org EXISTING_ALIAS \
  --sf-environment sandbox --state-dir var/salesforce-lookup
```

To map first, select imported calls during that first initialization:

```sh
gtm-connector slack map --account ACCOUNT_ID --thread-url 'COPIED_THREAD_LINK' \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --calls-backend imported --state-dir var/connected-sources \
  --connection-dir var/slack-connection
gtm-connector calls import meeting.vtt --account ACCOUNT_ID \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --slack-backend slack --slack-connection-dir var/slack-connection \
  --state-dir var/connected-sources
```

To import first instead, choose a different unused folder and select live Slack
on that first import. Do not run both initialization examples into one folder:

```sh
gtm-connector calls import meeting.vtt --account ACCOUNT_ID \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --slack-backend slack --slack-connection-dir var/slack-connection \
  --state-dir var/connected-sources-import-first
gtm-connector slack map --account ACCOUNT_ID --thread-url 'COPIED_THREAD_LINK' \
  --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox \
  --calls-backend imported --state-dir var/connected-sources-import-first \
  --connection-dir var/slack-connection
```

After either order, use that same folder for checks and daily work. The following
commands use the first example's folder:

```sh
gtm-connector slack check --account ACCOUNT_ID --state-dir var/connected-sources \
  --connection-dir var/slack-connection --json
gtm-connector brief ACCOUNT_ID --crm-backend salesforce --sf-org EXISTING_ALIAS \
  --sf-environment sandbox --calls-backend imported --with-slack \
  --slack-backend slack --slack-connection-dir var/slack-connection \
  --state-dir var/connected-sources
```

The Salesforce account needs an eligible standard Contract for a renewal brief.
The transcript must be imported for that same account. See the separate
[Salesforce check](salesforce.md#run-the-shareable-setup-check) and
[transcript guide](calls.md). A Slack check alone proves neither requirement.
The Salesforce demo-kit binding cannot be combined with live Slack or imported
calls; use fresh explicitly selected state for this flow.

## Share the check report, keep source evidence private

`slack check --json` writes one report to stdout and fixed progress objects to
stderr. Its five steps are `private_state`, `mapping`, `credential_binding`,
`thread_read`, and `receipt`. Each has a status, count, duration, API-call count,
response-byte count, fixed reason, and recovery command. A correctly committed
error receipt can pass its step while the overall read fails.
The exact step keys are `name`, `status` (`pass`, `fail`, or `not_run`), `count`,
`duration_ms`, `api_calls`, `response_bytes`, `code`, `message`, and `next`.

The report uses `schema_version: 1`, `status: "passed"` or `"failed"`, total
`duration_ms`, `api_calls`, `response_bytes`, `counts`, `steps`, and `next`.
`crm_access_checked` and `calls_access_checked` are always false. `counts` has
`auth_calls`, `message_calls`, `pages`, `raw_rows`, `messages`, `duplicates`,
`refresh_attempts`, `token_retries`, and `rate_retries`. If missing telemetry leaves
source work uncertain, request and byte counts are null rather than invented
zeroes. Failed checks report zero accepted messages.

This report contains no account or workspace IDs, names, thread timestamps,
message text, URLs, tokens, or private paths. It is designed to be shareable.
It counts connector HTTP attempts, not a workspace's total quota usage. Overall
duration also includes local HTTPS setup and cleanup. A completed failed check
prints the report and exits 4; invalid input, missing state, lock contention,
and startup failures keep their ordinary CLI categories before a report exists.

`slack map --json` returns `schema_version: 1`, `status: "saved"` or
`"unchanged"`, `selection: "saved"`, `thread_access: "unchecked"`,
`counts: {"selected_threads": 1}`, `duration_ms`, and a fixed next command.
It does not print the selected account, thread, or CRM receipt.

Outside a completed check report, source failures retain the
[ordinary CLI exit categories](cli.md). Fixed reasons point to a specific step:

| Reason | Meaning and recovery |
| --- | --- |
| `slack_selection_invalid`, `slack_account_not_found` | Correct the link/native selector or find a canonical account ID. |
| `slack_account_ambiguous` | Use a canonical account ID instead of an ambiguous AccountNumber. |
| `slack_mapping_missing`, `slack_mapping_conflict` | Map the account, or review explicit replacement and reassignment. |
| `slack_source_mismatch` | Restore the original pinned source choices and identity. |
| `slack_identity_unchecked` | Run `slack status --check`; expired or pending credentials instead require `slack connect`. |
| `slack_thread_unavailable`, `slack_forbidden` | Check the selected parent, history grant, and visibility. No empty success is inferred. |
| `slack_thread_changed`, `slack_thread_incomplete` | Retry a stable, accessible parent selection; no partial thread is accepted. |
| `slack_resource_limit`, `slack_output_too_large` | Select a smaller thread that fits the fixed bounds. |
| `slack_mapping_invalid`, `slack_snapshot_invalid` | Preserve invalid private evidence and inspect its setup before recreating it. |
| `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | Inspect saved authentication as directed, then explicitly retry when available. |

Ordinary MCP results and saved brief artifacts are different: they contain
literal selected source text, native message identifiers, the private mapping,
and receipt references. Keep them, credentials, source state, and audit files
private. For the requested account, a Slack-only grant returns its selected
thread without CRM fields, transcript content, or credentials. This scope can
request any mapped account in the connector; it does not yet enforce per-caller
account selections. No Slack sending tool exists.

## Bounds and what an observation means

The read uses only GET `https://slack.com/api/conversations.replies`, with the
selected channel, parent, and one fixed creation cutoff. It follows cursors
until completion. Short pages do not mean completion. All pages, permitted
retries, refresh, and identity checks share one operation budget:

| Bound | Limit |
| --- | ---: |
| HTTP attempts, including authentication and failed attempts | 10. |
| Accepted response bytes across those attempts | 1 MiB. |
| Cancellable HTTP operation deadline | 20 seconds. |
| Raw message rows, including duplicates | 100. |
| One literal message | 16 KiB of UTF-8. |
| Combined unique literal text | 64 KiB of UTF-8. |
| Normalized source envelope, including record references | 256 KiB of ASCII JSON. |
| One cursor | 2,048 UTF-8 bytes without controls. |

Authentication responses additionally retain their 64 KiB individual bound.
The normalized envelope bound is not an exact MCP wire-size promise; SDK text
and structured content can duplicate data. Synchronous filesystem operations
cannot be forcibly interrupted by the HTTP deadline.
Shutdown waits for active reads and their receipt handling before removing the
temporary check service. Ctrl-C can therefore wait for an in-flight read to
finish or reach its deadline. It does not promise rollback of completed work.

One exact `token_expired` response can retry after credential recovery under
the same budget. A different verified token from another process is reused
only for the same identity. At most one local refresh occurs. A second expiry,
revocation, or invalid grant does not start a login flow. One message 429 may
retry when its valid integer delay is at most five seconds and fits the remaining
budget. Other failures do not retry. OAuth refresh itself is never rate-retried.
An expired unchecked credential requires browser authorization.

The normalizer accepts plain message text and the supported `bot_message`,
`me_message`, legacy `file_share`, and `thread_broadcast` forms. Empty text is
preserved; missing or malformed text refuses the entire observation. Hidden
messages, edit/delete event wrappers, and unsupported subtypes refuse. Supplied
file, attachment, and block arrays contribute omission counts only. Their
contents and URLs are not fetched. App, participant, and unknown author labels
come from validated fields, not display-name lookup.

A successful snapshot proves cursor completion through its fixed creation
cutoff for the text returned by that API read. It is not a full archive or an
atomic historical view. Retention, visibility, earlier edits, and deleted content
remain unknown; text can change while paging. Reported edit metadata stays
visible, and absent metadata means only that no edit was reported. Conflicting
repeated messages refuse instead of choosing a version.

Live Slack briefs use evidence workflow 5. The default extractive formatter
quotes every returned message and requires edit/omission disclosures plus a
receipt-cited observation limit. It runs no model, sends nothing, and creates
no activity. A follow-up remains a proposal. Saved checks validate the copied
mapping, hashes, records, and receipts
offline. They neither reread Slack nor prove source truth or a provider
signature. The trusted local owner can rewrite all local evidence.

## Proof and provider limits

[Reader and shared-budget tests](../tests/test_slack_reader.py) cover fixed
requests, normalized text, cursor completion, retries, identity checks, output
bounds, and offline snapshots. [CLI tests](../tests/test_slack_source_cli.py)
cover source flags, safe output, and recovery.
[Mapping tests](../tests/test_slack_mapping.py) cover explicit selection and
private publication; [runtime tests](../tests/test_live_slack_runtime.py) cover
source separation and the isolated check service.
[Brief tests](../tests/test_live_slack_briefing.py) and
[check tests](../tests/test_slack_workflow.py) exercise composed local flows with
invented source responses. [Local HTTP tests](../tests/test_live_slack_http.py)
also exercise real socket requests, refresh, pagination, and streaming deadlines.
These are implementation checks, not live-service
verification. Current filesystem and process-lock proof is POSIX; native Windows
support remains a separate slice.

Slack's generic method reference lists Tier 3, while its distribution policy
restricts some new commercially distributed non-Marketplace installations to
one request per minute and 15 objects. Internal customer-built apps have a
different allowance. This connector cannot infer that policy from login. A long
rate delay can make a small paginated thread exceed its budget. Existing app
registration, approval, actual response compatibility, retention, and applicable
rate policy remain owner-run live checks.

Primary references read November 18, 2026:
[thread replies](https://docs.slack.dev/reference/methods/conversations.replies/),
[cursor completion](https://docs.slack.dev/apis/web-api/pagination/),
[message fields](https://docs.slack.dev/reference/events/message/),
[bot messages](https://docs.slack.dev/reference/events/message/bot_message/),
[broadcast references](https://docs.slack.dev/reference/events/message/thread_broadcast/),
[distribution rate policy](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps/),
and [internal-app clarification](https://docs.slack.dev/changelog/2025/06/03/rate-limits-clarity/).
