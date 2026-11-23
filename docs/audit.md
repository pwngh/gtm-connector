# Receipts and the audit log

Every tool call, every source read and every card seal leaves a receipt in PostgreSQL, committed
before its work starts. `audit` lists the receipts of a workspace, and every number on a card leads
back to one.

```sh
gtm-connector audit --brief BRIEF_ID                 # the reads one card rests on
gtm-connector audit --tool save_brief                # the seals of the saved cards
gtm-connector audit --caller anonymous --follow --json
gtm-connector inspect RECEIPT_ID                     # one receipt, its arguments and the rows it returned
```

```text
TIME                         TOOL                 CALLER      DECISION  MS    RECEIPT                               REASON
2026-11-21T12:54:01.486717Z  get_account          7f7266c3…  ok        0.06  581d633c-c347-4d77-9cd4-686877dc1ae3
2026-11-21T12:54:01.498367Z  list_opportunities   7f7266c3…  ok        0.03  1b72f01c-dab1-47df-b1c7-87f4e9140cd0
2026-11-21T12:54:01.506540Z  read_call_notes      7f7266c3…  ok        0.05  1e875f11-307e-4174-b70a-6c3699b855a1
2026-11-21T12:54:01.514717Z  read_slack_messages  7f7266c3…  ok        0.05  d2f19500-934c-44a8-b00b-93f8028510de
```

The caller is shortened here. `audit` prints all 64 hex digits.

## A receipt comes first

A receipt starts as `pending` in its own transaction, before the work it records. The work runs only
once that commit succeeds, and its outcome finishes the same receipt as `ok` or `error`. A receipt
goes from pending to finished once.

```text
call ──▶ receipt committed (pending) ──▶ checks, read ──▶ receipt finished (ok or error) ──▶ result released
```

> **A refused call keeps its receipt.** A bad token, a missing scope, a rate limit or invalid arguments
> each finish the call's receipt as `error`. A request that is no tool call gets none. A tool call
> whose receipt cannot be committed does not run and keeps none: the two cases are below, and in the
> [security review](../SECURITY_REVIEW.md#1-a-receipt-comes-before-every-tool-call).

When the receipt cannot be committed, the call does not run. `serve`'s `/mcp` answers HTTP 503 with
`{"error":"audit_unavailable; call was not executed"}`, and `mcp stdio` answers a JSON-RPC error in the
same words. `serve` answers the same way when it cannot read which workspace a bearer was issued in,
since each workspace keeps its own receipts.

When a call cannot finish cleanly, nothing is released: the receipt cannot be finished, the tool
failed, its result was malformed, or the caller left during the call. The answer is HTTP 503 with
`{"error":"execution_or_audit_unavailable; no result released"}`, or the same words over stdio. The
receipt finishes as error/503 `execution_or_audit_failure` when the database allows it.

A receipt left pending by a process that stopped is finished as error/503 `interrupted` once its
five-minute lease has expired. `setup` and `mcp stdio` do this when they start, and `serve` when it
starts and every 30 seconds.

## Read the log

`audit` lists one workspace's receipts in the order they started. Filters combine with AND and are
checked before anything is read. A workspace with no receipt gives `[]`. A workspace that does not
exist exits `2` with `audit database not found; run the demo first`. `audit` writes nothing.

| Option | Selects |
| --- | --- |
| `--state-dir NAME` | The [workspace](cli.md#workspaces). The default is `demo`. |
| `--brief ID` | Receipts whose `brief_id` is exactly this: one card's reads, or one run's calls. |
| `--tool NAME` | One exact tool name. |
| `--caller VALUE` | A complete SHA-256 fingerprint of 64 lowercase hex digits, or `anonymous`. Never a bearer token. |
| `--status` | `ok`, `error` or `pending`. |
| `--since`, `--until` | Receipts started at or after `--since` and before `--until`. Each is an ISO 8601 time with `Z` or a UTC offset, and `--since` must be the earlier. |
| `--follow` | The current matches, then each new or changed one, until Ctrl-C, which exits `130`. |
| `--json` | An array of receipt objects, or with `--follow` one receipt object per line. |

| Refused | Message, exit `2` |
| --- | --- |
| `--tool`, `--caller`, `--status`, `--since` or `--until` given blank | `Receipt filters must be nonempty text.` |
| A caller that is not a full fingerprint or `anonymous` | `--caller must be a complete SHA-256 fingerprint or anonymous, not a bearer token.` |
| A day, or a time without its offset | `--since must include its UTC offset.` |
| Any other time | `--since must be an ISO 8601 timestamp with a UTC offset.` |
| `--since` not before `--until` | `--since must be earlier than --until.` |

Text output is one row per receipt: `TIME`, `TOOL`, `CALLER`, `DECISION` (the status), `MS` (the
elapsed time, to two places), `RECEIPT` and `REASON` (the error code, or empty). A tool name the
connector does not know prints as a quoted literal. On a terminal the table has a heading. In a pipe
it is tab-separated rows.

`--follow` reads the log every half second and compares whole receipts. A pending receipt prints again
when it finishes, under the same `audit_id`. One that starts and finishes between two reads prints once,
finished. With `--status pending`, the finished version no longer matches and is not printed.

## What a receipt holds

```json
{
  "audit_id": "4a6e2167-5330-4619-8b2a-a5026bdf5ccc",
  "started_at": "2026-11-21T12:54:01.479006Z",
  "token_id": "4169e6768dadf5684fb2cf054a2673ee2046bbc08e33076509e829e3a682acad",
  "tool": "read_call_notes",
  "arguments": {"account_id": "acc-003"},
  "request_id": 2,
  "brief_id": "demo-297d6d58062b4831a034398103a4b78d",
  "status": "error",
  "http_status": 403,
  "row_count": 0,
  "rows": [],
  "elapsed_ms": 0.005,
  "error": "request_failed"
}
```

This is the read the demo has refused for a grant without `calls:read`.

| Field | Value |
| --- | --- |
| `audit_id` | The receipt's UUID. `inspect RECEIPT_ID` takes it. |
| `started_at` | When the receipt was committed, by the database's clock, in UTC. Microseconds appear only when there are any. |
| `token_id` | The SHA-256 fingerprint of the bearer the call presented, or `anonymous`. See below. |
| `tool` | The tool name, or a check's own step name. |
| `arguments` | The arguments as the call sent them. |
| `request_id` | The JSON-RPC id of an MCP call, the call's number within its run for a command, `null` for a seal, `"<invalid>"` for an id that is neither an integer nor a string, or a `brief_child` object for a read inside a prepared brief. |
| `brief_id` | The card or run the receipt belongs to, or `null`. See below. |
| `status` | `pending`, `ok` or `error`. |
| `http_status` | The HTTP status of the outcome, or `null` while pending. |
| `row_count`, `rows` | The record references the call returned, such as `calls/call_notes/note-003`. Only an `ok` receipt has rows. |
| `elapsed_ms` | How long the work took, or `null` while pending. |
| `error` | The error code of an `error` receipt, or `null`. |

| `error` | Means |
| --- | --- |
| `request_failed` | The call was refused, the read failed, or the tool returned an error. A missing scope is error/403. |
| `source_changed` | The source was connected again or detached after the read started. Nothing was read or bound. Error/409. |
| `interrupted` | The process holding the receipt stopped before finishing it. Error/503. |
| `execution_or_audit_failure` | The call could not finish cleanly, and no result was released. Error/503. |

### Who called: `token_id`

For an MCP call, `token_id` is the SHA-256 of the bearer token presented, even a rejected one. A header
that holds no bearer token is fingerprinted whole. A call with no Authorization header is
`anonymous`. The fingerprint identifies a credential, not a person, and does not prove a valid grant.

`token list` prints the same fingerprint as `CALLER`, so `audit --caller CALLER` lists one token's
calls. A command such as `brief` makes a bearer for its own run, never stored or shown, and its
receipts name that bearer's fingerprint. See [MCP](mcp.md).

### What a call sent: `tool` and `arguments`

A call's arguments are kept as it sent them, with three exceptions:

- A body that could not be parsed is recorded as `{"unparsed_sha256": DIGEST}`, under the tool name the
  `Mcp-Name` header gave, or `<unparsed>`.
- A request that repeats its own credential is recorded as
  `{"unrecorded": "the request repeated its credential"}`, and refused.
- Arguments that hold a NUL character are recorded as
  `{"unrecorded": "the arguments hold a NUL character"}`, and refused.

A tool name that is not 1 to 100 characters is recorded as `<invalid>`.

### Which card or run: `brief_id`

| Receipt | `brief_id` |
| --- | --- |
| A read of a card made by `brief` or `demo` | The card's brief ID. |
| A call of `demo --state-dir` outside the card | The run ID, `demo-` and 32 hex digits. The demo prints it as `audit.run_id`. |
| A read by `accounts` or `inspect ACCOUNT_ID` on a real CRM | `inspect-` and 32 hex digits. |
| A read that registers a deal, by `deal new` or `POST /v1/deals` | `deal-` and 32 hex digits. |
| A decision of a match, by the connector, `deal decide` or `POST /v1/matches/{id}` | `match-` and 32 hex digits. |
| The account lookup of `debug calls import` and `debug slack map` | `import-` and 32 hex digits. |
| A step of `debug salesforce check` | `check-` and 32 hex digits. |
| The read of `debug slack check` | `slack-check-` and 32 hex digits. |
| A request `connect salesforce` or a Slack sign-in command makes to its source | `sign-in-` and 32 hex digits. |
| An MCP call | The `gtm.demo/briefId` value of the call's `_meta`, up to 128 characters, or `null`. A `prepare_renewal_brief` call records none. |
| A seal | `null`. |

So `audit --brief BRIEF_ID` lists the four reads of a demo card: `get_account`,
`list_opportunities`, `read_call_notes` and `read_slack_messages`. A Salesforce card adds
`list_users`, the org's users, after `list_opportunities`. `audit --brief RUN_ID` lists the
demo's lookup and its refused read.

## The seal

Saving a card adds one receipt that is no source read: `save_brief`, the card's seal. The card's
`sealed_by` names it.

| Field | Value |
| --- | --- |
| `tool` | `save_brief` |
| `arguments` | `{"account_id": ACCOUNT_ID, "brief_id": BRIEF_ID}` |
| `rows` | `["briefs/BRIEF_ID/SHA256"]`, the SHA-256 of the saved card's bytes. |
| `request_id`, `brief_id` | `null` |
| `token_id` | The same fingerprint as the card's reads. |

Because its `brief_id` is `null`, `--brief BRIEF_ID` lists a card's reads and not its seal.
`--tool save_brief` lists the seals. [Deal cards](cards.md) explains how `inspect` checks a card
against its seal.

A brief an assistant prepares with `prepare_renewal_brief` is sealed by that call's own receipt. The
reads inside it are its child receipts, whose `request_id` is
`{"kind": "brief_child", "parent_audit_id": AUDIT_ID, "ordinal": N}`. See [MCP](mcp.md).

## What each command adds

| Command | Receipts |
| --- | --- |
| `brief` | One per read: `get_account`, `list_opportunities`, `list_users` on Salesforce, and `read_call_notes` and `read_slack_messages` when calls and Slack are connected. Then the seal. `--without-calls` keeps the call read, refused as error/403. On a real CRM, one `get_account` comes first, in an `inspect-` run, to list the account in the workspace. Given a name in place of an ID, on a real CRM one `search_accounts` comes before all of these, in an `inspect-` run of its own. |
| `demo --state-dir NAME` | Seven: the lookup, the refused call read, the card's four reads, and the seal. |
| `demo` | Five for each card it publishes. |
| `accounts`, `inspect ACCOUNT_ID` | One on a real CRM. None on the demo book, which is read without a tool call. |
| `inspect BRIEF_ID`, `inspect BRIEF_ID NUMBERS`, `deal show`, and `inspect ACCOUNT_ID` looking for the account's newest card | None for a card that passes its check. For each stored card that fails it, one `verify_card` in a `verify-` run, naming the brief and the card's account, finished as error/409 with what failed ([a card that fails its seal](cards.md#a-card-that-fails-its-seal)). This receipt is written after the check, to put the failure on record; it is not a tool call receipted before it runs. |
| `POST /v1/deals/{id}/refresh` | What `brief` leaves, each receipt naming the signed-in principal: one per read, then the seal. A refresh of an unknown deal, or of one whose account the person holds no grant on, leaves one `get_account` receipt in a `deal-` run, finished as error/403, which names the account only for a known deal. |
| `deal new`, `POST /v1/deals` | Two in a `deal-` run, also on the demo book: `get_account`, then `list_opportunities`. On a real CRM, `deal new` makes one `get_account` first, in an `inspect-` run, to list the account in the workspace. Through the API both name the signed-in principal, the grant is checked again just before each, and a read made without a live grant is refused as error/403, with no read after it. An API registration refused before any source is opened has one `get_account` receipt, refused as error/403 with no connection. |
| `POST /v1/deals/{id}/close` | What a refresh leaves for the final card, each receipt naming the signed-in principal, then one `close_deal` in a `close-` run that names them, the deal, the basis `person`, the reason and the final card. A close refused before its card leaves what a refused refresh leaves. One that loses to a newer card or to another close is finished as error/409 `deal_changed`, and one whose grant ends as it commits as error/403. When the final card reads the deal's opportunity as closed, the connector's own close, in the row below, takes this one's place. |
| `deal list`, `deal matches` | None. |
| `config activate` | One `activate_config` in a `config-` run, naming the revision and no account or person. It finishes ok in the transaction that activates the file. A refused activation has one too, finished as an error: error/404 `revision_not_found` for a revision the workspace never stored, error/409 `setting_not_applied` or `source_not_connected` for a file this build cannot activate, and error/409 `already_active` for the later of two activations of one revision at once. Activating the active file again, on its own, leaves none, and so does a revision that is not 64 characters. |
| `config preview`, `config show`, `config history` | None. |
| `deal close` | What `brief` leaves for the final card: one per read, then the seal. Then one `close_deal` in a `close-` run, which names the deal, the basis `person`, the reason and the final card, and finishes ok in the transaction that closes the deal. With `--no-card`, the `close_deal` alone. When the final card reads the deal's opportunity as closed, the connector's own close, in the next row, takes this one's place. A close that loses to a newer card or to another close is finished as error/409 `deal_changed`, and one of an unknown deal as error/403 naming no account. Closing a closed deal leaves none. |
| The connector's own close | One `close_deal` in a `close-` run, naming no principal, when a card that `brief`, a refresh, `deal close` or `POST /v1/deals/{id}/close` publishes for a deal reads its opportunity as closed in the CRM. It names the deal, the basis `crm_stage`, the opportunity, the outcome `won` or `lost` and the final card, with a null reason, and finishes ok in the transaction that closes the deal. One that loses to a newer card or to another close is finished as error/409 `deal_changed`. |
| `deal decide`, `POST /v1/matches/{id}` | One `decide_match`, naming the match, the decision, the deal, and for a known match its kind, item and account. Through the API it names the signed-in principal. A refused decision is finished as an error: error/403 naming no account for an unknown or ungranted match, error/403 naming the account for a grant revoked while the decision commits, error/400 `invalid_deal` for a deal of another account, error/409 `already_decided` for a superseded match. |
| The connector's own decisions | One `decide_match` per call or thread it decides, naming no principal, when `deal new`, `POST /v1/deals`, `debug calls import`, `debug slack map` or `brief` finds an undecided one while its account has an open deal. It decides a thread, or a call with no time, a second time when that item is on a closed deal and the account has an open one: the receipt then names the match it supersedes, and the decision is `waiting`. |
| `debug calls import`, `debug slack map` | One `get_account` in an `import-` run, also on the demo book. On a real CRM, one more in an `inspect-` run before it. |
| `grant` | One: `grant_account`, `revoke_grant` with `--revoke`, `disable_person` with `--disable` or `enable_person` with `--enable`, whose arguments are the account and the `to` or `subject` given. It finishes ok with the row `grants/PRINCIPAL_ID/ACCOUNT_ID`, or `people/PRINCIPAL_ID` for a person, in the transaction that makes the change, or as an error that changes nothing: `person_not_found` or `account_not_found` (404), or `person_ambiguous` (409). |
| `connect salesforce` | One per request it makes to sign in and check the org, in a `sign-in-` run. With `--org`, `connect_salesforce_login_display` and `connect_salesforce_login_token` for the two `sf` commands; with `--token-env`, neither. Then `connect_salesforce_org` and `connect_salesforce_userinfo`, and, where it checks the objects' fields, a `connect_salesforce_describe_` step for each object. That is the path of a sign-in that succeeds at once: a request the org refuses for an expired login may be retried once, under a receipt of its own. A request that fails has its receipt finished as an error, unless the store fails too, when the receipt stays pending until it is swept. |
| `debug slack connect`, `debug slack status --check`, `debug slack disconnect --revoke` | One per request to Slack, in a `sign-in-` run: `slack_sign_in_exchange`, `slack_sign_in_refresh`, `slack_sign_in_identity` or `slack_sign_in_revoke`. |
| `inspect RECEIPT_ID`, `audit`, `grants`, and every other form of `connect` | None. |
| MCP calls through `serve` or `mcp stdio` | One per tool call, and one per read inside a prepared brief. |

## Rate limit and writes

- **Each token gets 60 tool calls in a 60-second window**, which starts with the first call it counts.
  Every call made with a valid token counts, refused or not, so a scope denial counts. A token that does
  not verify is refused before it is counted.
- **A call over the limit** is refused with HTTP 429 `rate_limited` and a `Retry-After` header, and its
  receipt finishes as error/429.
- **The counts live in the memory** of the process that serves the call, `serve` or `mcp stdio`. A
  restart starts them again.
- **No tool writes to a source.** A call of `log_activity` is refused with HTTP 403
  `crm_write_disabled`, its receipt finishes as an error, and nothing is written. There are no update
  or delete tools.
