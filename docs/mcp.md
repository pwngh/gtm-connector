# MCP

**An assistant reads the connector's CRM, call and Slack tools and each deal's sealed card over MCP, and
every call it makes is checked, scoped and receipted.**

An assistant reaches the tools in one of two ways. It calls `serve`'s `/mcp` over HTTPS with a bearer
token that `token new` printed, or it starts `gtm-connector mcp stdio` itself as a local process. Both
pass the same gate, and each tool call gets one [receipt](audit.md), committed before the call runs.

```sh
gtm-connector setup
gtm-connector demo
gtm-connector token new --scope crm:read      # prints the token once
gtm-connector serve                           # leave it running
```

```sh
curl --cacert "$GTM_CREDENTIALS_DIR/tls.pem" https://127.0.0.1:8443/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H 'Mcp-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/call' -H 'Mcp-Name: get_account' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account",
       "arguments":{"account_id":"acc-003"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",
       "io.modelcontextprotocol/clientCapabilities":{}}}}'
```

The answer holds `crm/accounts/acc-003` (Thornfield Wren LLP) and `crm/contacts/con-003` (Marian
Holt), marked as untrusted data. Setup and the settings in the environment are in
[operations](operations.md). Workspaces and exit codes are in the [CLI guide](cli.md).

## How a call goes through the gate

```text
tools/call ──▶ receipt committed ──▶ gate checks ──▶ tool reads its own source ──▶ receipt finished ──▶ result
                                          │
                                          └──▶ refused ──▶ receipt finished with the refusal's status ──▶ error
```

A tool call gets its receipt before any check runs, so a refused call is receipted too. The gate then
checks the Host, the token, the rate, the scope, the arguments and the protocol revision, in a fixed
order. A result leaves only after its receipt is finished.

> **A scope opens only its own source, and a token reads only the accounts its principal is granted.**
> An account outside that grant is refused before any read and left out of every search and list.

`tools/list` and the other requests that call no tool get no receipt.

## Tools and scopes

A scope is the access a tool requires. A token carries one or more of the three read scopes, and each
scope opens one source.

| Tool | Scope | Returns |
| --- | --- | --- |
| `search_accounts` | `crm:read` | The first 100 accounts whose name contains `query` as literal text, ordered by name and then ID. With `account_number` and an empty `query`, at most two accounts with exactly that Salesforce AccountNumber. |
| `get_account` | `crm:read` | One account and its contacts, and with Salesforce its standard Contracts. |
| `list_opportunities` | `crm:read` | The opportunities of one account, or with no `account_id` those of every account the token may read. |
| `read_call_notes` | `calls:read` | The account's demo call notes, or its imported transcript excerpt. |
| `read_slack_messages` | `slack:read` | The account's demo Slack messages, or its mapped Slack thread. |
| `list_deals` | `crm:read` | The deals of the accounts the token may read, open ones first and each group by ID. No source is read. |
| `read_deal_card` | `crm:read`, and the scopes the card's content needs | One deal and its newest [card](cards.md), whole. No source is read. See [deals and their cards](#deals-and-their-cards). |
| `prepare_renewal_brief` | `crm:read` | A Deal card, saved as a prepared brief. Served only with a [selection](#selections-and-the-two-brief-tools). |
| `read_renewal_brief` | `crm:read`, and the scopes the brief's content needs | A prepared brief again, with no source read. Served only with a selection. |

`tools/list` gives each tool's input and output schema and its scope in `_meta`, as
`gtm.demo/requiredScope`. Without a selection it lists the five read tools and the two deal tools.

> **The tools are read-only.** There is no `log_activity` tool and no `crm:write` scope. A call to
> `log_activity` is refused with HTTP 403, JSON-RPC code 1005 and reason `crm_write_disabled`, under a
> receipt of its own. `token new --scope crm:write` is refused before anything is issued.

A read answers with `structuredContent` holding `source`, `records` and `untrusted_data: true`. Every
record carries a `record_ref`, such as `crm/accounts/acc-003`, `calls/call_notes/note-003` or
`slack/messages/msg-003`. When more than 100 accounts match a name search, the result adds
`has_more: true`, and otherwise the field is absent.

The result's `_meta` holds `gtm.demo/auditId`, the ID of the call's receipt. [Sources](sources.md)
describes what each source holds.

### Tool errors

A call that passes the gate but cannot be answered is a tool error: HTTP 200, `isError: true`, and the
text `Error executing tool TOOL: CODE`. Its receipt is finished as an error.

| Code | Cause |
| --- | --- |
| `account_not_granted` | The token's principal holds no live grant on the account. |
| `account_not_selected`, `slack_not_selected` | A selection is served, and the account, or its Slack thread, is not in it. |
| `deal_not_permitted` | `read_deal_card` named a deal that does not exist, one of an account the token's principal holds no live grant on, or one outside the selection. The three answer alike. |
| `card_scope_not_covered` | The token lacks `calls:read` or `slack:read`, and the deal's card read that source, when the call was admitted or as the card is released. |
| `brief_source_changed` | The deal's card, or the saved brief, was read from other sources than the workspace is bound to now. |
| `invalid_token` | A deal's card or a saved brief is released only while the bearer still verifies, and it no longer does. |
| `crm_not_connected`, `calls_not_connected`, `slack_not_connected` | The workspace has no such source connected at the moment of the call. |
| `crm_not_served` | The workspace's CRM is one the MCP tools do not serve in this build. |
| `calls_not_matched`, `slack_not_matched` | The account's imported call, or its mapped thread, waits for a person in the matches queue or was kept off every card ([setting it up](config.md#what-is-built-today)). |
| `account_number_requires_salesforce` | `search_accounts` was given `account_number` on the demo CRM, which has no account numbers. |
| A source's own code, such as `source_identity_mismatch` | The source refused the read. See [sources](sources.md). |

A tool that is not served, such as `prepare_renewal_brief` without a selection, answers
`Unknown tool: NAME` as a tool error.

Each call reads the sources as the workspace is connected when the call starts. A source detached or
connected again while `serve` or `mcp stdio` runs is not read through what was opened before.

## Deals and their cards

A [deal](api.md#one-deal-one-id) is one renewal of one account, registered with `deal new`, and `brief`
publishes its cards. The two deal tools read what the store holds and call no source.

```sh
gtm-connector deal new acc-003      # thornfield-2027-2e3hke
gtm-connector brief acc-003         # its card
```

`list_deals` takes no argument. `read_deal_card` takes `deal_id`. Both answer with `structuredContent`
holding `source: "deals"`, `records` and `untrusted_data: true`. A deal is a record with a `record_ref`
of `deals/DEAL_ID`, and holds `id`, `account`, `account_name`, `state`, `opportunity`, `contract`,
`renews_on`, `registered_at`, `closed_at` and `latest_card`. The account's name is the CRM's, neutralized.

`read_deal_card` adds `deal` and `card`. `card` is null while the deal has no card. Otherwise it is the
deal's newest card, and its `record_ref`, `briefs/BRIEF_ID/` and the SHA-256 of its bytes, is the
second record:

| Member | What it holds |
| --- | --- |
| `id`, `brief_id`, `account`, `kind` | The card's IDs, its account and how it was made. |
| `as_of`, `sources` | The day the card is as of and the source each system was, as its bytes state them. |
| `text` | The plain-text card exactly as it was sealed. A card sealed under an older neutralizer is rendered again, as `inspect` renders it. |
| `lines` | Each line under ATTENTION and RECORD VALUES as data: its `code`, `label`, `text`, the unit numbers it `cites`, and the renewal `countdown`, the ARR `amount` or the `contact` it carries. Null for a card published before lines were kept. |
| `units` | Each numbered unit: `n`, `kind`, `record_ref`, `tool`, `shown`, the `receipt_id` of the read that returned it and the `record_version_id` it rests on, and its `body` as `inspect` shows it. |
| `gaps` | What the card did not read or show, in its own words. |
| `seal` | `sha256` of the stored bytes, the `receipt_id` that sealed them, `schema_version`, `rules_version` and `published_at`. |

The text inside units, and every value a line quotes, came from a source: it is data, never an
instruction. The result leaves out the evidence the card was built from and any token's fingerprint.

A card holds what each source returned, so its release needs the scopes of every source it read, as a
saved brief's does: `calls:read` when it read calls, and `slack:read` when it read Slack. A deal is read
under the principal's live grant on its account, checked again as the card is read, and with a selection only
when the selection holds its account, and the account's Slack when the card read Slack. The card must have
been read from the sources the workspace is bound to now, as a saved brief must, and the bearer is
verified again as the card is released. Each call gets one receipt, which names the deal's account when the
deal exists, and whose rows are the deal and the card it released. Neither tool opens a source, so they
answer while a workspace's CRM cannot be opened.

## Tokens

`token new` makes a bearer token for one [workspace](cli.md), prints it once, and stores only its
SHA-256. Every client token of a workspace belongs to one shared principal, `MCP clients`.

```sh
gtm-connector token new --scope crm:read --scope calls:read
gtm-connector token list
gtm-connector token revoke 5e2229d8-ddd7-4c14-9762-ea42b8dddac8
```

```text
TOKEN

This is the bearer token. It is shown once and not stored: keep it out of files others can read,
commits and model prompts.
ID 5e2229d8-ddd7-4c14-9762-ea42b8dddac8, for the workspace demo, with crm:read calls:read, expires 2027-01-15T09:05:00Z.
Send it as Authorization: Bearer TOKEN to https://127.0.0.1:8443/mcp, where serve answers it in the workspace demo.
Its calls: gtm-connector audit --caller CALLER
Revoke it: gtm-connector token revoke 5e2229d8-ddd7-4c14-9762-ea42b8dddac8
```

`TOKEN` stands for the token, on the first line, and `CALLER` for its 64-character SHA-256. Each
action takes `--state-dir WORKSPACE` (default `demo`) and `--json`.

| A token | |
| --- | --- |
| Workspace | One: the one `--state-dir` names. A workspace with no CRM connected gets no token (exit 2). |
| Scopes | Only those given, from `crm:read`, `calls:read` and `slack:read`. Repeat `--scope` for each. |
| Lifetime | Five minutes, or `--minutes N` from 1 to 60. The database's clock decides when it has expired. Run `token new` again for another. |
| Accounts | Those the workspace's `MCP clients` principal is granted. |
| Storage | A row in PostgreSQL holding its SHA-256, so it outlives a restart of `serve`. That SHA-256 is the `caller` its receipts name: `gtm-connector audit --caller CALLER` lists its calls. |

`token list` shows each token's `id`, `scopes`, `state` (`live`, `expired` or `revoked`), expiry and
`caller`, oldest first, never the token. `token revoke ID` takes the UUID that `token list` prints, and
a client presenting that token is refused from its next call. An ID the workspace does not hold is
refused with exit 2.

With `--json`, `token new` writes `token`, `workspace`, `id`, `scopes`, `state`, `created_at`,
`expires_at`, `revoked_at`, `caller` and `url`. `token list --json` writes an array of the same objects
without `token`, `workspace` and `url`, and `token revoke --json` writes one of them. Times are UTC, to
the second.

> **The database URL is the authority.** Anyone who can run the binary with `GTM_DATABASE_URL` set to
> the app role's URL can issue a token for any connected workspace, with any read scope, and can start
> `mcp stdio` for one. Neither asks for a browser session or any other sign-in. Give that URL, and the
> password behind it, only to people who may read every connected workspace.

> **Issuing widens every client token of the workspace.** Issuing a token, or starting `mcp stdio`,
> grants the `MCP clients` principal every account of the connected CRM it was never granted: the demo
> book's accounts beside the demo CRM, and beside Salesforce the accounts the workspace holds. So when
> the CRM holds a new account, every live client token of that workspace can read it after the next
> issuance. A grant revoked for the principal stays revoked. The grants and the token are written in
> one transaction, so an issuance that fails adds no grant.

There is no OAuth sign-in and no client registration: a client cannot obtain a token from the server.
Tokens come only from `token new`, or from `mcp stdio` for its own process. If the workspace's
`MCP clients` principal is disabled, `token new` and `mcp stdio` refuse with exit 3.

## Over HTTPS: `serve`'s `/mcp`

`serve` answers MCP at `/mcp` on its HTTPS port, `https://127.0.0.1:8443/mcp` by default. A client
sends its token as `Authorization: Bearer TOKEN` and trusts the certificate `setup` wrote to
`tls.pem` in the credentials folder.

`serve` answers each token in the workspace it was issued in, with that workspace's own sources, and
receipts its calls there. Started with `--selection FILE`, it serves the demo workspace only, and a
token of any other workspace is refused as `invalid_token`.

- The transport is stateless Streamable HTTP with JSON responses: one POST, one answer, and no session.
- `GET /mcp` answers HTTP 405 with `Allow: POST`, because the connector opens no listening stream.
  `DELETE` answers 405 too.
- A request must come over TLS with the Host of the public origin (`GTM_PUBLIC_ORIGIN`, by default
  `https://127.0.0.1:8443`) named once, and with no `Origin` header. Any other Host, such as
  `localhost:8443`, is refused with `untrusted_origin_or_host`.
- `/.well-known/oauth-protected-resource` and `/.well-known/oauth-protected-resource/mcp` describe the
  resource. A refusal for the token names the latter in its `WWW-Authenticate` header.

## Stdio: started by the app

An app that starts a local MCP server runs `gtm-connector mcp stdio --scope crm:read`, with
`GTM_DATABASE_URL` set to the app role's URL. No server needs to run: the command reads the workspace
from PostgreSQL itself.

```json
{"mcpServers": {"gtm-connector": {"type": "stdio", "command": "/usr/local/bin/gtm-connector",
  "args": ["mcp", "stdio", "--scope", "crm:read"],
  "env": {"GTM_DATABASE_URL": "postgres://gtm_app@127.0.0.1:55432/gtm"}}}}
```

The process serves the workspace `--state-dir` names (default `demo`) as its `MCP clients` principal,
with a token made for the process and never shown. Each request passes the same gate as an HTTPS
request with that token, so its expiry, revocation, scopes and grants apply on stdio too.

```text
start ──▶ token made (2 minutes) ──▶ renewed every 30 seconds ──▶ input ends ──▶ token revoked (within 5 seconds)
```

- The token carries exactly the scopes given and lasts two minutes unless renewed. A killed process
  leaves it usable for at most two minutes.
- A token that expired or was revoked is not renewed, and nothing is retried with another one. The app
  must start the command again.
- A revocation that fails is said on stderr in one line, and the token stops working within two
  minutes.
- At start the command finishes, as interrupted, any receipt an earlier process left pending. `setup`
  and `serve` do the same.

Keep the database password out of the app's configuration. Give the URL without it, and put it in
`~/.pgpass`, or the file `PGPASSFILE` names, which the binary reads. The binary refuses that file, with
exit 1, when anyone but its owner has access to it, so make it mode 0600. A password that ends in a
space is not supported there, because the driver trims each line of the file.

The process writes only MCP messages on stdout, one JSON-RPC message per line, and refuses a batch.
It stops when its input ends, when the app closes its end of stdout, or when one line has waited ten
seconds to be written because the app stopped reading. A call still running then gets a finished
receipt but no answer, and a line still being admitted is waited for, up to ten seconds, before the
process stops.

| `mcp stdio` | |
| --- | --- |
| `--scope SCOPE` | Required. One of `crm:read`, `calls:read`, `slack:read`. Repeat for each. |
| `--selection FILE` | Serve only the accounts in a [selection file](#selections-and-the-two-brief-tools), and add the two brief tools. |
| `--state-dir WORKSPACE` | The workspace to serve (default `demo`). |
| `--json` | Changes only how a startup error is written. |
| Exit 2 | An argument, a selection file, or a workspace with no CRM is refused before anything is served. |
| Exit 3 | The workspace's CRM is not served by the MCP tools, or its `MCP clients` principal is disabled. |
| Exit 1 | A password file others can read. The [CLI guide](cli.md) lists the other codes. |

## Selections and the two brief tools

A selection is a file you write that lists the accounts a client may read, and whether each account's
Slack thread is included. A server started with one also serves `prepare_renewal_brief` and
`read_renewal_brief`, which save a [Deal card](cards.md) under the client's own token and read it back.

```json
{"schema_version": 1, "accounts": [
  {"account_id": "acc-003", "slack": true}, {"account_id": "acc-007", "slack": false}]}
```

```sh
chmod 600 selection.json
gtm-connector serve --selection selection.json
gtm-connector mcp stdio --scope crm:read --scope calls:read --selection selection.json
```

The file is read once, at start. To change the selection, restart `serve` or the stdio process. A restart
of `serve` leaves the tokens `token new` issued as they are.

- `accounts` lists 1 to 100 distinct accounts. Each entry has exactly `account_id` and `slack`, and
  `schema_version` is `1`. Any other key, and a repeated key or account, is refused.
- An `account_id` is a demo book ID, `acc-` and three digits, so a selection names demo accounts.
  `serve --selection` serves the demo workspace only. `mcp stdio --selection` serves the workspace
  `--state-dir` names, and reads only the selected accounts there.
- The file must be a regular file you own that group and others cannot write, not a symbolic link, and
  at most 65,536 bytes.
- A bad selection file stops `serve` with exit 1 and `mcp stdio` with exit 2, before either opens the
  database. An empty `--selection ''` is refused, never read as "no selection".

Each read still requires its scope and its grant. With a selection, the tools behave this way:

| Tool | With a selection |
| --- | --- |
| `search_accounts`, `list_opportunities` with no account | The selected accounts are part of the query, before ordering and the result limit, so a search never reports more matches than the selection holds. |
| `get_account`, `list_opportunities` with an account, `read_call_notes` | An unselected account is refused with `account_not_selected` and not looked up. |
| `read_slack_messages` | Reads only accounts whose `slack` is `true`, and refuses the others with `slack_not_selected`. |
| `prepare_renewal_brief(account_id, as_of)` | Makes and saves the account's card. `as_of` is optional, `YYYY-MM-DD`, and defaults to `2027-01-15`, the demo book's day. |
| `read_renewal_brief(brief_id)` | Returns a saved prepared brief without calling any source. |
| `list_deals` | Lists only the deals of selected accounts. |
| `read_deal_card(deal_id)` | A deal of an unselected account answers `deal_not_permitted`, as an unknown deal does. A card that read Slack, of an account whose `slack` is `false`, answers `slack_not_selected`. |

### A prepared brief

`prepare_renewal_brief` reads the account and its opportunities, then its call notes when calls are
connected, then its Slack thread when the account's `slack` is `true`. Each read gets a receipt of its
own, linked to the call's receipt by `{"kind": "brief_child", "parent_audit_id": ..., "ordinal": N}`,
checks the token again, and counts against its rate.

A read the token has no scope for is receipted as refused, with HTTP 403, and the card says so: a token
without `calls:read` gets a card whose `NOT READ` section says `Calls: not read. The grant lacks
calls:read.` The call's own receipt is the brief's seal: it is finished with the digest of the saved
bytes.

```text
prepare_renewal_brief ──▶ get_account ──▶ list_opportunities ──▶ read_call_notes ──▶ read_slack_messages ──▶ card saved and sealed
     (receipt)              (child 1)         (child 2)             (child 3)            (child 4)
```

Both brief tools answer with `structuredContent` holding `source: "brief"`, one record (the brief, with a
`record_ref` of `briefs/BRIEF_ID/` and the SHA-256 of its bytes), `untrusted_data: true`, and `brief`.

| `brief` key | Holds |
| --- | --- |
| `brief_id`, `account_id`, `as_of` | Which card this is. |
| `text` | The card as the `brief` command prints it. A card with a sentence sealed under an older neutralizer version is rendered again for the model, with a first line that says so, and its sealed text stays in its bytes. See [cards](cards.md#units). |
| `units` | Each number on the card with its record reference, and for a sentence its neutralized text, speaker and role. See [cards](cards.md). |
| `gaps` | What the card did not read, as on the card. |
| `sources` | Each source read, such as `"crm": "demo_fixture"`. |
| `notes_status`, `slack_status` | From `prepare_renewal_brief` only: what became of each read, such as `available`, `denied` or `not_requested`. |
| `provider_rechecked`, `notice` | From `read_renewal_brief` only: `false`, and a sentence saying the brief was not rechecked against the source systems. |

The prepared brief is kept in PostgreSQL. `inspect` reads cards saved by `brief` or `demo`, and refuses a
prepared brief by name, so a caller resolves a number from `units`.

`read_renewal_brief` releases a brief only while all of these still hold, checked at the moment of the
call:

1. Its own `prepare_renewal_brief` call finished ok for these exact bytes.
2. The account is still selected, and its Slack thread too when the brief holds Slack messages.
3. The token carries `crm:read`, and `calls:read` or `slack:read` when the brief holds call notes or
   Slack messages.
4. The workspace is still bound to the sources the brief was read from.
5. The principal still holds its grant on the account, and the token is still live.

A card saved by `brief` or `demo` is not a prepared brief, and `read_renewal_brief` answers it with
`brief_not_found`.

### Brief errors

Each of these is a tool error, under the call's receipt.

| Code | Cause | Fix |
| --- | --- | --- |
| `account_not_selected`, `slack_not_selected` | The account, or its Slack thread, is not selected. | Edit the file and restart. |
| `account_not_granted` | The principal holds no grant on the account. | Use an account the workspace holds. A grant revoked for the principal stays revoked. |
| `invalid_as_of` | `as_of` is not a `YYYY-MM-DD` date. | Correct the date. |
| `rate_limited`, `brief_deadline_exceeded` | A read inside the brief reached the rate limit, or the 45 seconds a brief has passed. | Retry. |
| `invalid_token`, `brief_scope_not_covered` | The token expired or was revoked during the call, or it lacks a scope the brief needs. | Issue a token with those scopes. |
| `brief_source_failed`, `brief_evidence_invalid`, `brief_rejected` | A read failed, or the evidence did not pass its checks, so no card was made. | Read the receipts with `audit`, then retry. |
| `brief_too_large`, `brief_storage_failed` | The brief would exceed 4 MiB, or it could not be saved. | Read the receipts with `audit`. |
| `brief_not_found`, `brief_invalid`, `brief_not_accepted`, `brief_source_changed` | No prepared brief has this ID, its bytes or receipts do not check, or a source binding changed. | Prepare a new brief. |

A token refused at the gate, for its authentication or its rate, is the HTTP refusal below, not a tool
error.

## Protocol revision and request refusals

The connector implements [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28).
Over HTTPS it admits only that revision, and every request carries it twice: in the
`Mcp-Protocol-Version` header and in `params._meta` as `io.modelcontextprotocol/protocolVersion`, beside
`io.modelcontextprotocol/clientCapabilities`. Every request names its method in the `Mcp-Method`
header, and a tool call names its tool in `Mcp-Name`.

On stdio, a request that carries that metadata must name 2026-07-28, or it is refused with JSON-RPC code
-32022, `Unsupported protocol version`. A client that sends no metadata starts with `initialize`, and is
then answered as an older client is.

The gate refuses a request with `error.data.reason`. On stdio the same reasons come with the same
JSON-RPC codes, and the receipt records the HTTP status.

| Reason | HTTP status | JSON-RPC code |
| --- | --- | --- |
| `request_too_large` | 413 | -32600 |
| `invalid_json` | 400 | -32700 |
| `untrusted_origin_or_host` | 403 | 1003 |
| `invalid_token` | 401 | 1001 |
| `credential_in_request` | 400 | -32600 |
| `rate_limited` | 429 | 1002 |
| `unsupported_media_type` | 415 | -32600 |
| `call_id_required` | 400 | -32600 |
| `crm_write_disabled` | 403 | 1005 |
| `insufficient_scope` | 403 | 1004 |
| `invalid_arguments` | 400 | -32602 |
| `missing_protocol_version` | 400 | -32602 |
| `protocol_version_mismatch` | 400 | -32020 |
| `unsupported_protocol_version` | 400 | -32022 |
| `method_not_found` | 404 | -32601 |

- A missing, unknown, revoked or expired token, a token of another workspace, one with a scope outside
  the three read scopes, and a second `Authorization` header all return `invalid_token`, without saying
  which check failed.
- `credential_in_request` refuses a request that repeats its own bearer in its id, its tool name or its
  arguments. Its receipt records none of them.
- `insufficient_scope` names the missing scope in `WWW-Authenticate`. `rate_limited` sends `Retry-After`.
- Every tool call a token makes counts against its rate, refused or not, once the token has passed.
  `crm_write_disabled` comes after the rate check and before the scope check, so no scope opens a write.
- `prompts/get` and `resources/read` are `method_not_found`: the connector serves tools only.
- After the gate admits a request, the protocol's own checks of the `Accept` header, the routing
  headers and the `_meta` keys refuse with their own messages and no `reason`.
- If a receipt cannot be committed, the call is not run, and the answer is HTTP 503 with
  `{"error":"audit_unavailable; call was not executed"}`. If it cannot be finished, the result is
  withheld with HTTP 503 and `{"error":"execution_or_audit_unavailable; no result released"}`. Over stdio
  both are JSON-RPC code -32603 with the same words. Read [audit](audit.md) before retrying.

## Limits

| Limit | Value |
| --- | --- |
| Request body over HTTPS | Over 65,536 bytes is refused with `request_too_large`. |
| Line over stdio | Measured as written. Over 65,536 bytes is refused with `request_too_large`. A line over 4 MiB is read to its end and discarded, and a tool call it starts still gets a receipt, under the tool name its start shows. |
| Rate | 60 tool calls per token in 60 seconds, counted by the serving process, so a restart starts the count again. |
| Arguments | `account_id` and `brief_id` up to 64 characters, `query` up to 200, `account_number` up to 40, `as_of` up to 10. A required one cannot be blank, and an unknown one is refused. |
| Search | The first 100 accounts, or two for an account number. |
| Token | 5 minutes by default, 1 to 60 with `--minutes`. A stdio process's token: 2 minutes, renewed every 30 seconds. |
| Prepared brief | 45 seconds to prepare, and at most 4 MiB saved. |
| Selection | 1 to 100 accounts, in a file of at most 65,536 bytes. |
| Stdio output | One line may wait ten seconds to be written before the process stops. |

## Assistant clients

The one client CI runs is the [MCP Inspector](https://github.com/modelcontextprotocol/inspector) CLI,
pinned at 2.15.0, on the Node version in `.nvmrc`. The CI job runs `setup`, `demo`, `deal new acc-003`,
`brief acc-003` and `token new`, then the Inspector against `/mcp` over HTTPS with each token, then the
Inspector starting `mcp stdio` itself.

It checks discovery, the seven tools and their schemas, the three source reads, the two deal tools, the
cross-source denials, an unknown bearer refused with HTTP 401, one finished receipt for each tool call, and that no token reaches
the Inspector's output, the audit log or the server's log. No assistant app has been tested.

```json
{"mcpServers": {"gtm-connector": {"type": "streamable-http", "url": "https://127.0.0.1:8443/mcp",
  "headers": {"Authorization": "Bearer TOKEN"}, "protocolEra": "modern"}}}
```

```sh
chmod 600 inspector.json
NODE_EXTRA_CA_CERTS="$GTM_CREDENTIALS_DIR/tls.pem" NODE_TLS_REJECT_UNAUTHORIZED=1 NO_PROXY=127.0.0.1 \
  npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config inspector.json --server gtm-connector \
  --stored-auth-only --quiet --format json --method tools/call --tool-name get_account --tool-arg account_id=acc-003
```

For stdio, the entry is `"type": "stdio"` with `"command"`, `"args": ["mcp", "stdio", "--scope",
"crm:read"]` and `"env"` holding `GTM_DATABASE_URL`, as in the app entry above.

> **A configuration file with a bearer token in it is a credential.** Keep it out of commits,
> transcripts and model prompts, and readable only by its owner.

| Call | Token | Result |
| --- | --- | --- |
| `initialize` | Any | `protocolVersion: "2026-07-28"`, with the tools capability. |
| `tools/list` | Any | The seven tools, each with its schemas and `gtm.demo/requiredScope`. |
| `get_account` | `crm:read` | `crm/accounts/acc-003` and `crm/contacts/con-003`, with `source: "crm"` and `untrusted_data: true`. |
| `read_call_notes` | `crm:read` only | Refused for `calls:read`: the Inspector exits non-zero, with nothing on stdout and the error on stderr. |
| `read_call_notes` | `calls:read` | `calls/call_notes/note-003` and `calls/call_notes/note-003b`. |
| `read_slack_messages` | `slack:read` | `slack/messages/msg-003`. A Slack-only token cannot read CRM or call notes. |
| `list_deals` | `crm:read` | `deals/thornfield-2027-2e3hke`, with `source: "deals"`. |
| `read_deal_card` | `crm:read`, `calls:read` and `slack:read` | The deal and its card, whose `text` starts `DEAL CARD:`, the same over HTTPS and stdio. |
| Any | An unknown bearer | Refused as unauthorized, HTTP 401. |

`connect APP` does not set up an assistant app in this build. For `inspector`, `claude-code`,
`claude-desktop` and `cursor` it prints `connect APP is not available in this build. No setup was printed
and nothing was granted.` and exits 3. Issue a token with `token new`, or point the app at `mcp stdio`,
instead.
