# Tools and source data

The connector serves six tools over CRM, calls, and Slack. A scope is the access a tool requires. A
grant is a bearer token issued for a list of scopes.

| Tool | Scope | Returns |
| --- | --- | --- |
| `search_accounts` | `crm:read` | The first 100 accounts whose name contains the literal query, ordered by name and then ID, or at most two exact Salesforce AccountNumber matches. |
| `get_account` | `crm:read` | One account, its contacts, and with Salesforce its standard Contracts. |
| `list_opportunities` | `crm:read` | Opportunities, for one account when given. |
| `read_call_notes` | `calls:read` | Demo call notes, or the selected excerpt of an [imported transcript](calls.md). |
| `read_slack_messages` | `slack:read` | Demo Slack messages, or the mapped [live thread](slack.md). |
| `log_activity` | `crm:write` | The activity created, or the existing one for that idempotency key. |

> **A call without the tool's scope gets HTTP 403.** Each tool declares its scope in
> `gtm.demo/requiredScope` metadata.

- Every record carries a reference such as `crm/accounts/acc-001`, `calls/call_notes/note-007`, or
  `slack/messages/msg-007`. Results are marked as untrusted data.
- For `search_accounts`, Salesforce orders names by the user's locale, and the demo CRM ignores
  ASCII case. When more than 100 accounts match a name search, it adds `has_more: true` beside
  `records`. Otherwise the field is absent. The receipt lists the records returned.
- `debug serve --selection FILE` limits reads to the selected accounts and replaces `log_activity` with
  `prepare_renewal_brief` and `read_renewal_brief`, which return a [Deal card](briefs.md) as `text`
  with its numbered `units` and its `gaps`. See [selection](selection.md).
- `debug serve` in a [state folder](cli.md) connected to Salesforce reads CRM from Salesforce and omits `log_activity`.
  See [Salesforce](salesforce.md).

## Fixtures

`connect demo` gives each of the three sources its own local SQLite store of invented records. The
fixtures are the records of the demo data. The JSON value `"demo_fixture"` names them as a source.

Every company, person, and message is invented, with `.example` email domains. The fixed demo date
is `2027-01-15`.

- `crm.sqlite3` holds 30 accounts, 30 contacts, and 30 opportunities, each amount with a currency.
- `calls.sqlite3` holds 30 notes.
- `slack.sqlite3` holds 29 messages.

| Record | Case |
| --- | --- |
| `acc-001` | Renews in 17 days. |
| `acc-002` | Is at risk. |
| `acc-003` | Thornfield Wren LLP, the demo account. Its opportunity is in negotiation for USD 450,000.00. |
| `note-003`, `note-003b` | One call of `acc-003`. A named seller promises a date and a named contact sets a condition. |
| `msg-003` | On `acc-003`, a seller's report with a channel mention, a link, a second-hand figure, and one attached file that is not read. |
| `acc-004` | Has no call notes. |
| `acc-005` | Has no Slack messages. |
| `acc-006` | An October call note and a January Slack message disagree on seats. Both are quoted, and only the note's age is a signal. |
| `acc-007` | Juniper Crest Learning renews on `2027-01-29`. Its contact has no role. |
| `note-007`, `msg-007` | Each asks for a write. Both stay quoted data, and no activity is created. |

Reseeding keeps record IDs, edits, activities, and accounts added beside the book. An incomplete
fixture set is rejected, and so are a combined CRM and calls database and a database without the
currency, speaker, or file-count column. Nothing is migrated, so use a fresh `--state-dir`.

In a folder connected to the demo data, `accounts --query 'Alder Vale'` and `inspect acc-003` read
the demo book directly, outside MCP authorization. They read the folder's own `crm.sqlite3` once its
services have run, and otherwise a copy in memory, so reading accounts writes nothing.

A book that is a link, or a folder pinned to Salesforce, is refused. The account view leaves out
opportunities and call notes. In a folder connected to Salesforce, `accounts` and `inspect` read the
account through the scoped, audited gateway instead.

## Run the guarantee checks

`debug verify` checks scope refusals, source text that asks for a write, idempotent activity writes,
rate limits and card receipts, on demo data. It creates one demo activity, so keep its state folder
apart from the demo's.

```sh
.venv/bin/python -m gtm_connector debug verify --state-dir var/verification --json
```

| Field | Result |
| --- | --- |
| `accounts` | `30` |
| `notes_without_scope.status` | HTTP `403` |
| `rate_limit.status` | HTTP `429` |
| `empty_notes` | `[]` for `acc-004` |
| `idempotent_write` | `requests: 2`, `activities: 1` |
| `audit_rows` | `12` connector receipts |
| `brief_audit_rows` | `9` reads across the three cards |
| `seal_audit_rows` | `3` receipts that seal the saved cards |
| `briefs` | Two [Deal cards](briefs.md): `acc-003` with calls access, `acc-001` without it |
| `injection_note.activities_created` | `0` |
| `injection_brief.activities_created` | `0` across all three cards, including `acc-007` |

`debug verify --with-slack` adds a Slack read to each card and checks the CRM-to-Slack, Slack-to-CRM,
and Slack-to-calls denials. `audit_rows` becomes `16` and `brief_audit_rows` becomes `12`. See
[Slack context](slack.md).

## Protocol and authorization

The server implements
[MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28) and its
[authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
section. The official Python SDK is pinned to **2.3.0**.

> **Authorization is a local owner emulator, not production login or consent.** `debug serve`
> grants no scopes at startup. See the [security review](../SECURITY_REVIEW.md).

- The transport is stateless Streamable HTTP with per-request protocol metadata and
  `server/discover`. Only revision 2026-07-28 is accepted. Subscriptions and legacy initialization
  sessions are not supported.
- Requests with an `Origin` header are refused.
- S256 PKCE, state, and issuer are checked. A token is opaque and bound to the resource.
- A restart invalidates every code and token. There are no refresh tokens and no dynamic client
  registration.
- Keep `owner.json` and bearer tokens out of model inputs.
- Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an explicit bearer grant. See
  [clients](clients.md).

| Limit | Value |
| --- | --- |
| Request body | Over 65,536 bytes is refused. |
| Code | Lasts 60 seconds and works once. |
| Token | Lasts five minutes. |

### Request refusals

A gateway refusal carries `error.data.reason`.

| Reason | HTTP status | JSON-RPC code |
| --- | --- | --- |
| `request_too_large` | 413 | -32600 |
| `invalid_json` | 400 | -32700 |
| `untrusted_origin_or_host` | 403 | 1003 |
| `invalid_token` | 401 | 1001 |
| `rate_limited` | 429 | 1002 |
| `unsupported_media_type` | 415 | -32600 |
| `call_id_required` | 400 | -32600 |
| `insufficient_scope` | 403 | 1004 |
| `crm_write_disabled` | 403 | 1005 |
| `invalid_arguments` | 400 | -32602 |
| `missing_protocol_version` | 400 | -32602 |
| `protocol_version_mismatch` | 400 | -32020 |
| `unsupported_protocol_version` | 400 | -32022 |
| `method_not_found` | 404 | -32601 |

- A missing, malformed, expired, or wrong-resource credential returns `invalid_token` without
  saying which check failed.
- `insufficient_scope` names the scope in `WWW-Authenticate`. `rate_limited` sends `Retry-After`.
- SDK rejections, OAuth errors, and tool errors keep their own formats.
- `GET /mcp` returns HTTP 405. The connector opens no listening stream.
- An audit or execution failure returns HTTP 503 with an error string and no reason. Read
  [audit](audit.md) before retrying a write.
