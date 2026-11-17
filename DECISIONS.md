# Decisions

## Scope and implementation

Use Python 3.11+ and SQLite. PR 1 used only the standard library; PR 2 adds the
official MCP Python SDK 2.3.0 and local HTTPS dependencies. No data or code comes
from a real CRM, employer, or client. Currency values are integer USD cents.

Reseeding must preserve existing content. Missing or extra fixture records are
an error; a new database path is the explicit way to start fresh. Reads use
SQLite read-only connections. Direct CLI inspection is a developer fixture
utility, not a claim of authorization enforcement.

The PR 3 briefing adapter uses a deterministic extractive fixture without a
model. Its output must still pass the citation checker. A hosted adapter is
optional future work and is not required by the demo command. No LLM quality
claim follows from fixture tests. PR 4 measures the twenty-account fixture
workflow against independent expected claims.

## Four reviewable slices

1. Seeded SQLite CRM, CLI inspection, and data tests.
2. Local authorization and MCP tools, separate CRM and calls scopes, durable
   audit receipts, rate limits, idempotent activity writes, and their tests.
   Include the small fixture update below. Run council on this core-logic PR.
3. Brief workflow with citations, an uncited-sentence checker, restricted-scope
   behavior, an injection test, and fixtures that run without a model.
4. Twenty-account evaluation and README scores, one-page security review, an
   audit-based usage report, README polish, and a five-minute clean-checkout demo.
   Report calls by token, tool, and day, plus model tokens per brief. PR 3 records
   known fixture usage as zero; this slice adds aggregation and evaluation.

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
PR 3 tests the briefing workflow's handling of it.

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

Protected-resource metadata advertises only `crm:read` for basic functionality.
The current specification's scope-selection strategy calls for this minimal
initial set; per-operation challenges advertise `calls:read` or `crm:write`
when required. The authorization server advertises all three supported scopes.
The client returns a denied operation without automatically acquiring more access.

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
the operator removes the chosen local state directory when finished. The
security review states that limit. Audit fingerprints distinguish presented
credentials, including invalid ones. Missing credentials use `anonymous`.
These groups are not an identity or billing system.

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

Council identified a parser difference across supported Python versions: older
versions raise `RecursionError` for deeply nested JSON within the body-size limit.
Treat that as malformed input so a marked tool attempt still receives a receipt.
Unknown-account writes also return an explicit tool error, rather than relying
on the SDK's generic handling of a foreign-key failure. Both have regression tests.

## Briefing and citation boundary

Use a fixed three-read workflow: `get_account`, `list_opportunities`, then
`read_call_notes`, all through the HTTPS MCP client. Each completed brief has
one correlated audit receipt per read. The workflow requests `crm:read` and
optionally `calls:read`; it never requests `crm:write`. Draft output cannot
select a tool, change scopes, or trigger another call. A recognized call-notes
scope denial is the only partial-success case. Transport failures, other HTTP
errors, failed tools, and incomplete evidence stop the brief instead of being
rewritten as missing notes.

Use `FixtureDraftAdapter` as the no-model default. It formats the same supported
fact catalog used by the checker. This deliberately tests the transport,
evidence, and publication boundary rather than pretending to measure model
reasoning. Independently written adverse candidate files exercise uncited,
invented, misattributed, appended, and action-shaped output. An optional hosted
adapter is not implemented or needed here. The adapter interface accepts an
evidence copy and returns text; it receives no client, owner credential, or
bearer token. A custom Python adapter is trusted application code, not a
sandbox for executing untrusted programs, and the CLI cannot load one.

Keep the original evidence snapshot separate from the adapter's deep copy.
Before drafting, validate record source prefixes, account associations, and
exact correspondence to the three audit receipts. After drafting, compare each
nonempty candidate line with a supported sentence and its exact citations.
Reject extra prose, invented values, missing or swapped references, repeated
statements, and unsupported action payloads. This small controlled vocabulary
cannot validate arbitrary paraphrases or establish semantic entailment; those
would require a different checker and evaluation. A supported subset may pass,
so grounding and coverage remain distinct measures for PR 4.

Quote source text as a JSON string inside its attributed statement. This keeps
newlines and terminal controls visibly escaped and keeps instructions inside
a source quote. Do not interpret the note or infer a resolution when it
contradicts a CRM field. A citation establishes what the source reported, not
whether that report is true. The injection test proves that this fixture
workflow returns the note as data, performs only its three planned reads, and
creates no activity; it makes no claim about an untested model's behavior.

Absence and lack of permission are different facts. A successful empty notes
query produces a statement that no notes were returned. A scoped HTTP 403
produces an explicit unavailable-notes statement. Both cite the matching audit
receipt, because there is no note record to cite. The checker requires either
outcome statement to remain in the brief, even if a candidate omits other
supported facts. Renewal-day calculations cite both the account and a saved
`run/<brief_id>` context containing the explicit as-of date; the clock does not
silently change fixture results.

Persist each evidence snapshot in `briefs/<brief_id>.json` under the selected
state directory, using a mode-0700 directory and mode-0600 files. Save
`collected` before invoking the adapter, then `accepted` with checked text or
`rejected` without draft text. An interrupted run may remain `collected`.
The saved evidence allows `check-brief` to verify another candidate against the
same records later; it does not consult a later, possibly changed CRM. The local
file owner remains trusted, so these snapshots are neither signed nor
tamper-proof. Retention is manual, as for the other local state.

Snapshot file replacement is not atomic. An interrupted write can leave partial
JSON instead of a usable snapshot; the checker rejects it and the operator must
generate a new brief. Crash-safe snapshot replacement is deferred. The separate
SQLite audit receipts retain their existing durability behavior.

Record fixture model usage as model `null` and zero input/output tokens because
no model ran. For custom Python adapters, record unknown token counts as `null`
rather than estimating them from text. PR 4 adds the audit usage report and
twenty-account fact/citation/invention scores. The default demo keeps the original
14 connector attempts and adds three briefs with three reads each, for 23
receipts; the full, restricted, and injection cases have separate brief IDs.

## README baseline after PR 3

Document merged behavior, not the earlier PR 2 snapshot. The README examples
initially came from running the CLI on main after PR 3 merged. Keep the 14
connector receipts distinct from the nine briefing receipts. PR 3 preserved the
two source statements for `opp-003` and `note-003` without flagging their conflict.
PR 4 adds that flag alongside evaluation, usage reporting, and security review.

Keep this documentation change on the PR 4 branch so it does not consume an
extra pull request. No runtime behavior changes in this documentation commit.
The demo uses no external runtime services or API keys. The first dependency
installation still needs internet, and the demo starts two local HTTPS services.

## Explicit conflict rule

Recognize one bounded condition: an opportunity's stage is `closed_won` and a
call note for the same account exactly matches the fixture's unapproved
procurement statement, including the unsigned-budget sentence. Require a
`Source conflict:` claim with both record references. Preserve the original
opportunity statement and note quote. Resolve neither source. Do not infer a
conflict from denied notes, another stage, a paraphrase, or a larger instruction
containing this text. This is a transparent fixture rule, not a general semantic
contradiction detector. The rule uses record content, not hard-coded record IDs.

The checker requires recognized conflicts even when a draft includes only a
supported subset. Otherwise an adapter could hide the known disagreement while
passing the grounding check. Existing PR 3 snapshots still contain usable
evidence, but an older `acc-003` text without the new flag fails the current
checker. Regenerate the brief. There is no promise to freeze verification rules
for old saved text.

## Evaluation and usage measurements

Keep 20 fixed cases in `evaluation/expected.json`, covering `acc-001` through
`acc-020` at `2027-01-15`. The expected text and source references are checked in
separately from the draft generator and citation checker. Runtime evaluation
does not derive expectations from either. Substitute only the generated run ID
and empty-notes receipt ID. Use fresh temporary source databases so operator
edits cannot silently change the benchmark. Remove that temporary state on exit;
the JSON result retains per-account metrics, not the temporary snapshots.

Measure published fixture output. One fact means one displayed claim line;
compound contact and opportunity statements each count once. Count coverage by
unique expected text and citation coverage by the exact reference set. Count
unsupported text as invented. Duplicates cannot inflate coverage. Rejected or
failed briefs retain their expected claims in the denominator. No output means
no invention rate to measure, not a fabricated perfect score. The evaluator
fails unless all 20 briefs pass with full coverage, correct citations, no
invented or repeated claims, 60 successful audit receipts, and no activities.
The recorded score is a fixture regression result, not an LLM quality claim.

Read usage directly from the existing audit database in SQLite read-only mode.
Count all receipts, including failed and pending calls, by credential fingerprint,
tool, and UTC day. Report saved brief status and token usage separately. A
fingerprint can represent an invalid credential and is not a person or verified
grant. Missing credentials use `anonymous`. Token totals cover saved artifacts
only; unknown values remain `null`, and deleted artifacts cannot contribute usage. Stop with
a clear error on partial or invalid artifact JSON instead of silently losing
its usage. The report never copies audit arguments or source text.

`scripts/five-minute-demo.sh` creates a private temporary output directory and
runs setup, tests, connector cases, evaluation, usage, and an actual CLI checker
rejection. It keeps the demo state and JSON output for inspection. The operator
chooses when to remove it. Five minutes is the presentation outline in DEMO.md;
first-install downloads have no fixed duration. A focused test also proves that
a write committed before audit-finalization failure is reused on retry while
the original receipt remains pending.

## Review configuration

The installed TStack source is [t3dotgg/tstack at 11e37a7](https://github.com/t3dotgg/tstack/tree/11e37a74d0a6868c121cd0910ab2b3bd9873b92a).
Full-stop uses Sol review for each slice. Council ran once on PR 2, which carries
the connector's core logic; later slices do not repeat the three-member council.
The user selected GPT Sol, Claude Opus, and Gemini 3.8 Flash at High as three full
members. Gemini Flash is not described as a flagship. Record actual model IDs
and scores in the PR table; three council rounds is the hard limit. See
AGENTS.md for the durable rules.

Google's supported personal-account route is Antigravity CLI; legacy Gemini CLI
sign-in is retired. Review tooling uses existing accounts and is separate from
the demo runtime. It must not create accounts, trials, subscriptions, or keys.
