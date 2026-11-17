# Connector security review

Scope: one local process, invented data, one operator. This review records the
boundary and required evidence. It does not grant production approval.

## Data and permissions

`crm.sqlite3` holds 30 accounts, contacts, opportunities, and activities.
`calls.sqlite3` holds 29 call notes. Labels are synthetic; email addresses use
reserved `.example` domains. The connector uses no real CRM, external identity,
or recording service.

| Scope | Allowed tools |
| --- | --- |
| `crm:read` | `search_accounts`, `get_account`, `list_opportunities` |
| `crm:write` | `log_activity` |
| `calls:read` | `read_call_notes` |

Scopes do not imply one another. The gateway and handlers enforce them. A scope
denial returns HTTP 403. The briefing workflow requests read scopes only and
makes three fixed calls. It cannot select a write from record or draft text.

## Trust boundary

The operator's local file access is trusted. The owner can read or change every
database and snapshot. Possession of `owner.json` permits requesting all scopes.
Direct administration and usage commands bypass MCP authorization by reading
local files. This is not multi-user isolation or production identity.

Both services bind HTTPS to `127.0.0.1`. The client explicitly trusts the
generated certificate. It validates the issuer, resource, redirect, and state.
Authorization uses a preregistered public client and S256 PKCE. Codes expire
after 60 seconds and permit one exchange. Opaque tokens expire after five
minutes. Invalid, expired, or wrong-resource tokens fail with HTTP 401.
Restart invalidates grants. Browser origins are rejected. No refresh tokens,
dynamic registration, system trust changes, or external keys are required.

## Audit and retention

`audit.sqlite3` stores token fingerprints, tool arguments, UTC timestamps,
request and brief IDs, outcomes, and returned record references. Arguments can
include activity text. Fingerprints use SHA-256; reusable bearer tokens and raw
response bodies are not logged. Saved briefs include source evidence and
receipt copies. Never put owner credentials or bearer tokens into a draft input.

Runtime directories use mode 0700; files use mode 0600. Retention has no automatic
expiry. The operator removes the chosen state directory when finished. The
evaluation uses disposable state. Usage counts audit attempts by token, tool,
and day. Fingerprints identify grants, not people. Model usage comes from saved
briefs: fixture counts are zero; unknown counts remain unknown. These local
records are neither tamper-proof evidence nor billing data.

## Failure modes and untrusted text

Each recognizable tool attempt commits one `pending` receipt before execution.
An initial audit failure returns HTTP 503 and aborts the tool. Finalization
failure also returns HTTP 503 but can follow a committed activity. Retry the
same idempotency key and reconcile the pending receipt. Changed payloads return
`idempotency_conflict`. Unparseable traffic without a tool marker and requests
disconnected before execution fall outside the receipt guarantee.

HTTP 429 includes `rate_limited` and `Retry-After`. The default limit is 60 calls
per valid token per 60-second window. Scope denials count. New tokens and process
restarts reset the allowance; this is not a distributed quota.

Notes and draft text remain untrusted data. The fixture adapter runs no model.
The verifier accepts exact supported claims and references, not arbitrary
paraphrases. A citation attributes a source; it does not establish source truth.
The conflict rule matches `closed_won` against the exact fixture note reporting
unapproved procurement. Its required flag cites both sources and resolves
neither. It does not detect arbitrary contradictions.

Snapshot writes are not atomic. An interrupted write can leave unusable JSON;
generate a new brief. Custom Python adapters are trusted application code and
are not sandboxed. The CLI cannot load them.

## Required sign-off evidence

A reviewer must inspect the scope-denial and invalid-token tests, audit failure
and committed-write retry tests, injection and citation-rejection tests, and
the 20-account evaluation with its fixed expected claims. Confirm that the
clean-checkout demo passes, the injected note creates zero activities, and usage
totals match receipts. Review [DECISIONS.md](DECISIONS.md) and accept the local
owner, manual-retention, snapshot, and fixture-only limits explicitly. Real data
or shared deployment requires a separate review.
