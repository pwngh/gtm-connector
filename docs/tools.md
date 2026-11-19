# Tools and source data

The connector serves six tools over CRM, calls, and Slack. By default each source is its own local
SQLite store of invented records.

| Tool | Scope | Returns |
| --- | --- | --- |
| `search_accounts` | `crm:read` | The first 100 accounts whose name contains the literal query, ordered by name and then ID, or at most two exact Salesforce AccountNumber matches. Salesforce orders names by the user's locale, and the demo CRM ignores ASCII case. |
| `get_account` | `crm:read` | One account, its contacts, and with Salesforce its standard Contracts. |
| `list_opportunities` | `crm:read` | Opportunities, for one account when given. |
| `read_call_notes` | `calls:read` | Fixture notes, or the selected excerpt of an [imported transcript](calls.md). |
| `read_slack_messages` | `slack:read` | Fixture messages, or the mapped [live thread](slack.md). |
| `log_activity` | `crm:write` | The activity created, or the existing one for that idempotency key. |

- Each tool declares its scope in `gtm.demo/requiredScope` metadata, and a call without it gets
  HTTP 403.
- Every record carries a reference such as `crm/accounts/acc-001`, `calls/call_notes/note-007`, or
  `slack/messages/msg-007`. Results are marked as untrusted data.
- When more than 100 accounts match a name search, `search_accounts` returns the first 100 and adds
  `has_more: true` beside `records`. Otherwise the field is absent. The receipt lists the records
  returned.
- `serve --selection FILE` limits reads to the selected accounts and replaces `log_activity` with
  `prepare_renewal_brief` and `read_renewal_brief`, which return a [Deal card](briefs.md). See
  [selection](selection.md).
- `serve --crm-backend salesforce` reads CRM from Salesforce and omits `log_activity`. See
  [Salesforce](salesforce.md).

## Fixtures

Every company, person, and message is invented, with `.example` email domains. `crm.sqlite3` holds
30 accounts, 30 contacts, and 30 opportunities, each amount with a currency. `calls.sqlite3` holds
30 notes, and `slack.sqlite3` holds 29 messages. The fixed demo date is `2027-01-15`.

| Record | Case |
| --- | --- |
| `acc-001`, `acc-002` | `acc-001` renews in 17 days. `acc-002` is at risk. |
| `acc-003` | Thornfield Wren LLP, the demo account. Its opportunity is in negotiation for USD 450,000.00. On one call (`note-003`, `note-003b`) a named seller promises a date and a named contact sets a condition. `msg-003` is a seller's report with a channel mention, a link, a second-hand figure, and one attached file that is not read. |
| `acc-004`, `acc-005` | `acc-004` has no call notes. `acc-005` has no Slack messages. |
| `acc-006` | An October call note and a January Slack message disagree on seats. Both are quoted, and only the note's age is a signal. |
| `acc-007` | Juniper Crest Learning renews on `2027-01-29`. Its contact has no role. |
| `note-007`, `msg-007` | Each asks for a write. Both stay quoted data, and no activity is created. |

Reseeding keeps record IDs, edits, activities, and accounts added beside the book. An incomplete
fixture set is rejected, and so are a combined CRM and calls database and a database without the
currency, speaker, or file-count column. Nothing is migrated, so use a fresh `--state-dir`.

`seed`, `accounts --query 'Alder Vale'`, and `account acc-003` read the SQLite file directly,
outside MCP authorization. `--db PATH` selects the file, and only `seed` creates it. The account
view leaves out opportunities and call notes. With `--crm-backend salesforce`, `accounts` and
`account` read through the scoped, audited gateway instead.

## Run the guarantee checks

`verify` creates one fixture activity, so keep its state apart from the demo's.

```sh
.venv/bin/python -m gtm_connector verify --state-dir var/verification --json
```

| Field | Result |
| --- | --- |
| `accounts` | `30` |
| `notes_without_scope.status`, `rate_limit.status` | HTTP `403` and HTTP `429` |
| `empty_notes` | `[]` for `acc-004` |
| `idempotent_write` | `requests: 2`, `activities: 1` |
| `audit_rows`, `brief_audit_rows` | `12` connector receipts, and `9` reads across the three cards |
| `briefs` | Two [Deal cards](briefs.md): `acc-003` with calls access, `acc-001` without it |
| `injection_note.activities_created`, `injection_brief.activities_created` | `0`, the second across all three cards, including `acc-007` |

`verify --with-slack` adds a Slack read to each card and checks the CRM-to-Slack, Slack-to-CRM,
and Slack-to-calls denials. `audit_rows` becomes `16` and `brief_audit_rows` becomes `12`. See
[Slack context](slack.md).

## Protocol and authorization

The server implements
[MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28) and its
[authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
section. The official Python SDK is pinned to **2.3.0**.

- The transport is stateless Streamable HTTP with per-request protocol metadata and
  `server/discover`. Only revision 2026-07-28 is accepted. Subscriptions and legacy initialization
  sessions are not supported.
- Requests with an `Origin` header or a body over 65,536 bytes are refused.
- Authorization is a local owner emulator, not production login or consent. `serve` grants no
  scopes at startup. See the [security review](../SECURITY_REVIEW.md).
- S256 PKCE, state, and issuer are checked. A code lasts 60 seconds and works once. A token is
  opaque, bound to the resource, and lasts five minutes.
- A restart invalidates every code and token. There are no refresh tokens and no dynamic client
  registration.
- Keep `owner.json` and bearer tokens out of model inputs.
- Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an explicit bearer grant. See
  [clients](clients.md).

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
