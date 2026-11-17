# Connector security review

Scope: one local connector process, an optional local Ollama service, invented
data by default, and one operator. This review records the boundary and required evidence.
It does not grant production approval.

## Data and permissions

`crm.sqlite3` holds 30 accounts, contacts, opportunities, and activities.
`calls.sqlite3` holds 29 call notes. `slack.sqlite3` holds 29 account-channel
messages. Labels are synthetic; email addresses use
reserved `.example` domains. The default runtime uses no real CRM, external
identity, or recording service. An explicit Salesforce backend can read an
operator's Developer Edition org; calls and Slack still use local fixtures.

| Scope | Allowed tools |
| --- | --- |
| `crm:read` | `search_accounts`, `get_account`, `list_opportunities` |
| `crm:write` | `log_activity` |
| `calls:read` | `read_call_notes` |
| `slack:read` | `read_slack_messages` |

Scopes do not imply one another. The gateway and handlers enforce them. A scope
denial returns HTTP 403. The briefing workflow requests read scopes only and
makes three fixed calls by default. `--with-slack` adds a fourth read and an
explicit Slack grant. It cannot select a write from record or draft text.

## Trust boundary

The operator's local file access is trusted. The owner can read or change every
database and snapshot. Possession of `owner.json` permits requesting all scopes.
Direct administration and usage commands bypass MCP authorization by reading
local files. This is not multi-user isolation or production identity.

`inspector-config` is another local owner utility. It requests only the named
scopes and writes a bearer credential to mode-0600 `inspector.json`. The external
Inspector client receives that grant, not the owner credential. Keep the file
private and remove it with the state directory. The five-minute token expires
normally; no refresh or automatic scope increase is added. Inspector sends its
tool calls through the same gateway, audit log, and rate limit. It trusts the
generated certificate through `NODE_EXTRA_CA_CERTS`; TLS verification remains on.
This path tests supplied-token MCP access, not an external client's login flow.

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
and day. Fingerprints include rejected credentials; missing credentials use
`anonymous`. Groups do not establish identity or a valid grant. Model usage
comes from saved briefs: fixture counts are zero; unknown counts remain unknown. These local
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
The optional Ollama adapter sends only a supported-claim catalog to a fixed
loopback endpoint. It checks cached model metadata and rejects cloud aliases.
It requires Ollama 0.30.0 or newer and adds `:local` to each generation request
so a changed alias cannot trigger cloud forwarding. The recorded digest is a
preflight identity; keep local tags stable for reproducible measurements.
It has no credentials or tool interface, downloads no models, and uses no proxy
or redirects. The local Ollama service and its model files are operator-trusted;
these checks cannot constrain a malicious replacement service. Rejected drafts
retain usage and bounded counts, never their candidate text. One invalid line
rejects the whole brief. Duplicate JSON keys also reject the response instead
of silently discarding an earlier claim array. Default demos and CI use no inference service.
The verifier accepts exact supported claims and references, not arbitrary
paraphrases. A citation attributes a source; it does not establish source truth.
The conflict rule matches `closed_won` against the exact fixture note reporting
unapproved procurement. Its required flag cites both sources and resolves
neither. It does not detect arbitrary contradictions.

Slack messages remain untrusted data in a separate store and scope. The optional
four-read workflow requires a matching receipt for each source. It distinguishes
empty messages from a denied grant. Its additional conflict rule matches the
exact delayed-review message against the existing procurement conflict and
cites all three records. The follow-up is a checked proposal, never a sent
message or a promise. It cannot create an activity or contact an external service.
Legacy three-read snapshots cannot introduce Slack evidence or a follow-up.

Snapshot writes are not atomic. An interrupted write can leave unusable JSON;
generate a new brief. Custom Python adapters are trusted application code and
are not sandboxed. The CLI cannot load them.

## Optional Salesforce boundary

`serve --crm-backend salesforce` reads a privately supplied instance URL and
access token from the environment. The constructor performs no HTTP request.
After the local token and scope checks and pending audit commit, each CRM tool
queries the org edition and requires `Developer Edition`. The existing local
owner can grant CRM access to that org. This is not Salesforce user delegation.
The upstream token may have broader permissions; the adapter implements GET only.

Requests use one configured HTTPS My Domain origin, fixed SOQL fields, escaped
search text, and bounded results. TLS verification stays on. Proxies, redirects,
retries, and token refresh are disabled. Pagination must stay on the same query
endpoint. The adapter exposes no arbitrary query or URL input. External mode
omits the write tool and rejects hardcoded write attempts with HTTP 403.

Account names, contacts, and opportunity fields reach the authorized client.
They remain untrusted data. Audit arguments may contain real search terms and
record IDs. Returned refs persist, but response bodies and the upstream token
are not copied to the audit. Existing manual retention and local-owner limits
still apply. No Salesforce credential enters owner.json, Inspector config,
brief evidence, or model input. Briefing refuses this backend because renewal
fields are unmapped. Read output has no implicit join to the fixture accounts.
Tests use invented HTTP responses, not a live org. See [setup and limits](docs/salesforce.md).

## Required sign-off evidence

A reviewer must inspect the scope-denial and invalid-token tests, audit failure
and committed-write retry tests, injection and citation-rejection tests, and
the 20-account evaluation with its fixed expected claims. Confirm that the
clean-checkout demo passes, the injected note creates zero activities, and usage
totals match receipts. Review [DECISIONS.md](DECISIONS.md) and accept the local
owner, manual-retention, snapshot, and fixed-vocabulary limits explicitly. Real data
or shared deployment requires a separate review. Before using an org, inspect
its permissions and data, the adapter's fixed fields and read-only tests, and
the local owner's ability to grant access. The test result is not approval to
read production or client data.
