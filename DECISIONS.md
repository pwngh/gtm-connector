# Decisions

## Scope and implementation

Use Python 3.11+ and SQLite. PR 1 used only the standard library; PR 2 adds the
official MCP Python SDK 2.3.0 and local HTTPS dependencies. No data or code comes
from a real CRM, employer, or client. Currency values are integer USD cents.

Reseeding must preserve existing content. Missing or extra fixture records are
an error; a new database path is the explicit way to start fresh. Reads use
SQLite read-only connections. Direct CLI inspection is a developer fixture
utility, not a claim of authorization enforcement.

The PR 3 briefing adapter will use deterministic fixtures without a paid model.
It must still pass the citation checker and PR 4 evaluation. A hosted adapter
is optional and will not be required by the demo command. PR 2 does not generate
briefs, run an LLM, or claim an evaluation score.

## Four reviewable slices

1. Seeded SQLite CRM, CLI inspection, and data tests.
2. Local authorization and MCP tools, separate CRM and calls scopes, durable
   audit receipts, rate limits, idempotent activity writes, and their tests.
   Include the small fixture update below. Run council on this core-logic PR.
3. Brief workflow with citations, an uncited-sentence checker, restricted-scope
   behavior, an injection test, and fixtures that run without a model.
4. Twenty-account evaluation and README scores, one-page security review, an
   audit-based usage report, README polish, and a five-minute clean-checkout demo.
   Report calls by token, tool, and day, plus model tokens per brief. Model usage
   belongs to the later workflow; PR 2 does not invent usage fields or counts.

Each slice must run and pass its tests before full-stop handoff. The user merges.
Only the initial README/license bootstrap goes directly to main. No deployments,
package publishing, real CRM APIs, web UI, or multi-tenant features.

## Protocol research (2026-11-17)

Read the current specification and authorization documents before implementation.
The latest URL resolved to MCP revision 2026-07-28:

- [Specification](https://modelcontextprotocol.io/specification/2026-07-28)
- [Key changes](https://modelcontextprotocol.io/specification/2026-07-28/changelog)
- [Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
- [Discovery](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/authorization-server-discovery)
- [Client registration](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/client-registration)
- [Authorization security](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization/security-considerations)

Both the implemented MCP specification and authorization section are revision
2026-07-28, read on 2026-11-17. This revision uses stateless requests and
`server/discover` rather than the old initialization handshake. The SDK handles
the protocol and tool schemas; the connector uses stateless Streamable HTTP
with JSON responses and explicit protocol metadata. Subscriptions are disabled.
This is a bounded local implementation, not a claim of conformance to every
optional MCP feature or compatibility with arbitrary clients.

The pinned SDK does not by itself enforce every local policy. Its omitted-scope
default can grant all registered client scopes, and its authorization-error
redirects omit the issuer parameter. The wrapper requires explicit scopes and
adds `iss` to those redirects. It also rejects ambiguous parameters, enforces
the exact resource and redirect, requires S256 PKCE and state, and checks the
request's protocol version. These checks remain explicit and tested when the
SDK is upgraded.

## Two source systems and fixed fixtures

Use three distinct files: `crm.sqlite3` for accounts, contacts, opportunities,
and activities; `calls.sqlite3` for call notes; and `audit.sqlite3` for gateway
receipts. CRM account reads return accounts and contacts only. Opportunities
have a separate tool. No CRM table or response hides a copy of call notes.
These are two SQLite source adapters behind one connector, not two live APIs.
There is no cross-database foreign key; the fixed seed data validates its IDs.

The 30 accounts retain readable IDs `acc-001` through `acc-030`, with matching
`con-`, `opp-`, and `note-` IDs. Display names are labels such as
`Fixture account acc-001` and `Fixture contact con-001`; emails use reserved
`.example` domains. Avoid plausible invented proper names because they may
coincide with real companies or people. No named product is needed.

Use `2027-01-15` as the fixed demo date so examples do not age out of the test.
`acc-001` renews on `2027-02-01`, 17 days later. `acc-002` is explicitly at risk
because its renewal budget is not approved. `opp-003` is `closed_won`, while
`note-003` says procurement has not approved the renewal and the budget remains
unsigned. Keep both source claims; do not silently reconcile them. `acc-004`
has no notes, leaving 29 notes total. `note-007` contains an instruction to call
the write tool. The connector returns that text unchanged as untrusted data;
PR 3 will test the briefing workflow's handling of it.

Reseeding is atomic and preserves edits and activities. Unexpected IDs, broken
fixture associations, or partial seeds fail instead of silently resetting data.
A PR 1 database containing the old combined call-notes table is rejected with
an instruction to use a fresh path. A migration would obscure the separation
and retain old display names; the demo does not need one.

## Local authorization boundary

Tools declare one scope each: `search_accounts`, `get_account`, and
`list_opportunities` need `crm:read`; `read_call_notes` needs `calls:read`;
`log_activity` needs `crm:write`. Calls access does not follow from CRM access.
The HTTP boundary checks scope before execution, and each handler checks it
again. Tool metadata declares the scope using the private
`gtm.demo/requiredScope` key; the metadata itself is not an enforcement mechanism.

Run one local authorization server and one connector on separate HTTPS
loopback ports. Generate a short-lived certificate locally; the CLI explicitly
trusts that certificate and validates the pinned issuer and resource metadata.
It does not disable TLS verification, follow arbitrary discovery URLs, or use
environment proxy settings. No system trust-store change is needed.

Use one pre-registered public CLI client with authorization code and S256 PKCE,
an exact HTTP loopback callback URI, state, issuer validation, and a required
resource parameter. The CLI validates the redirect response without opening a
browser or running a callback listener. A generated local owner credential
approves explicit scopes. This is a resource-owner emulator, not production
identity, a login page, or a consent service. Anyone who can read that owner
credential can request all three scopes; it must never enter a model input.

Access tokens are random opaque values with a five-minute default lifetime.
The authority keeps grants in process memory and checks expiry, client, issuer,
scope, and the exact connector resource. Restart invalidates all tokens and
authorization codes. Codes are single-use and expire after 60 seconds. There
are no refresh tokens, dynamic client registration, external identity accounts,
or delegated downstream credentials. Browser origins are rejected.

## Audit, limits, and writes

For a recognizable `tools/call` request, commit one pending audit receipt before
authorization or tool execution. Finalize that same row after success or
rejection. Store a full SHA-256 fingerprint of the presented bearer credential
or `anonymous`, never the bearer credential itself. Record UTC time, tool,
arguments, request ID, optional brief ID, status, HTTP status, returned record
count and references, elapsed time, and a bounded error category. Returned
records carry references such as `crm/accounts/acc-001`; the audit stores those
references rather than copying all returned record bodies.

Arguments are retained verbatim, including an activity body. The log is local
data, not a redacted telemetry feed. Runtime state uses an owner-only directory
and owner-readable/writable files. There is no automatic retention expiry yet;
the operator removes the chosen local state directory when finished. PR 4's
security review must state that limit. Audit fingerprints distinguish grants,
not people, and are not an identity or billing system.

If the pending receipt cannot be committed, do not execute the call. If audit
finalization fails, withhold the result with HTTP 503; the pending receipt
survives. A process failure can also leave a pending receipt. CRM writes and
audit updates are not one cross-database transaction: an activity may already
be committed when its response is withheld. Retrying its idempotency key is
the recovery path. The audit is not tamper-proof against the local file owner.
Unparseable traffic without a tool-call marker and transport disconnects are
not claimed as completed tool calls.

Rate limits count recognized tool calls per valid token in a fixed in-memory
window, defaulting to 60 calls per 60 seconds. Scope denials also consume that
token's allowance. Invalid tokens do not create rate buckets. A limited call
still receives an audit row and returns HTTP 429 with `rate_limited` and
`Retry-After`. Restart or a new token resets its allowance; this is a small local
abuse guard, not distributed or per-person quota enforcement.

The activity idempotency key is globally unique within this single-tenant CRM.
Repeated calls with the same key and identical account, kind, and body return
the original activity. Reusing the key with different arguments is a conflict.
The unique constraint and transaction enforce this across concurrent retries.
There is no update or delete tool.

## Review configuration

The installed TStack source is [t3dotgg/tstack at 11e37a7](https://github.com/t3dotgg/tstack/tree/11e37a74d0a6868c121cd0910ab2b3bd9873b92a).
Full-stop uses Sol review for each slice. Council runs once on the core-logic PR.
The user selected GPT Sol, Claude Opus, and Gemini 3.8 Flash at High as three full
members. Gemini Flash is not described as a flagship. Record actual model IDs
and scores in the PR table; three council rounds is the hard limit. See
AGENTS.md for the durable rules.

Google's supported personal-account route is Antigravity CLI; legacy Gemini CLI
sign-in is retired. Review tooling uses existing accounts and is separate from
the demo runtime. It must not create accounts, trials, subscriptions, or keys.
