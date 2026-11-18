# Connector security review

Scope: one local connector process, an optional local Ollama service, invented
data by default, and one operator. This review records the boundary and required evidence.
It does not grant production approval.

## Data and permissions

`crm.sqlite3` holds 30 accounts, contacts, opportunities, and activities.
`calls.sqlite3` holds 29 call notes. `slack.sqlite3` holds 29 account-channel
messages. Company names, people, and source text are invented; email addresses use
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

The default `demo` follows one invented rep and account. A CRM-only lookup and
call-note refusal precede one four-read brief with explicit CRM, calls, and Slack
grants. Its six receipts record five successful reads and one refusal. It
requests no write scope and creates no activity. The separate `verify` command
keeps the broader guarantee checks, including an explicitly authorized local
activity and its idempotent retry. The walkthrough scripts inspect narrative
state; verification runs separately with its own state in CI. The
[narrative tests](tests/test_demo.py) check the saved artifact, receipts, quoted
injections, repeated runs, and unchanged activities through local HTTPS.

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

`connect` writes supplied-token configs only for explicit scopes. Inspector
uses its existing private config; Claude Code and Cursor get separate private
files. No command launches a hosted assistant. Those app paths are untested,
and their ordinary login does not work with this local owner emulator. Keep
all generated app configs out of Git. `doctor` inspects file types, permissions,
certificate dates, and validated loopback metadata without obtaining a grant,
reading CRM records, or printing raw configuration and transport errors.

The running server's event stream contains time, tool, caller fingerprint,
decision, duration, receipt ID, and a fixed reason. It contains neither bearer
tokens nor arguments or source bodies. Tool names are untrusted and the human
renderer escapes controls. JSON escapes its string values. These events are
operator diagnostics; the SQLite receipt remains the persisted audit record.
An event callback failure cannot bypass a receipt failure or alter a response.

Both services bind HTTPS to `127.0.0.1`. The client explicitly trusts the
generated certificate. Each start generates a fresh P-256 key and a one-day
certificate with the loopback address as its subject alternative name. The
[runtime tests](tests/test_runtime.py) verify key rotation, certificate identity,
private file permissions, and explicit trust. The client validates the issuer,
resource, redirect, and state.
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

The fixture book contains a missing contact role and a dated old note whose
seat advice differs from a later Slack message. The renderer states the missing
role and preserves source dates and quotations. It does not infer the right
role, classify freshness, or resolve that disagreement. The recorded Ollama run
uses the earlier synthetic-label fixture corpus; it proves no model result for
the current book. The narrative demo uses fixed drafting without a model.

Slack messages remain untrusted data in a separate store and scope. The optional
four-read workflow requires a matching receipt for each source. It distinguishes
empty messages from a denied grant. Its additional conflict rule matches the
exact delayed-review message against the existing procurement conflict and
cites all three records. The follow-up is a checked proposal, never a sent
message or a promise. It cannot create an activity or contact an external service.
Legacy three-read snapshots cannot introduce Slack evidence or a follow-up.

Snapshot saves write a mode-0600 temporary file, then replace the destination
atomically. A failed replacement keeps the previous complete snapshot. There is
no power-loss durability guarantee. A killed process can leave a private temporary
file, which follows the same manual retention rule. Audit inspection opens the
existing database read-only without initializing tables or changing permissions.
Custom Python adapters are trusted application code and are not sandboxed. The
CLI cannot load them.

## Optional Salesforce boundary

`serve --crm-backend salesforce --sf-org ALIAS` and the matching `brief` option
capture an existing Salesforce CLI login inside the connector process. The
provider invokes only `sf org display` and `sf org auth show-access-token`, with
an explicit alias and `--json`, using argument arrays and no shell. Stdin is
closed; stdout and stderr are captured. CLI telemetry, automatic updates, and
file logging are disabled for these child commands. A command has at most
10 seconds within the original 20-second credential deadline. Errors expose a
fixed code, never the alias, child output, arguments, or token. The connector
stores no Salesforce credential file. The owner's existing CLI login storage
and installed `sf` executable remain trusted.

Without `--sf-org`, the explicit backend uses `SALESFORCE_INSTANCE_URL` and
`SALESFORCE_ACCESS_TOKEN`; an explicit alias takes precedence. There is no
implicit selection of a default CLI org. Both paths validate one HTTPS My Domain origin and a
bounded printable token. The constructor performs no adapter REST query.
After the local grant checks and pending audit commit, the first CRM tool
requires `Developer Edition`. Only that successful edition result is cached,
for this adapter's pinned origin and lifetime. CRM records are not cached.
The local owner can grant CRM access; this is not Salesforce user delegation.
The upstream token may have broader permissions. The adapter implements GET only.

Requests use fixed SOQL fields, escaped search text, and bounded results.
TLS verification stays on; proxies and redirects are disabled. Pagination must
stay on the same query endpoint. Each operation retains the 12-GET, 1-MiB,
20-second bounds, with at most 10 pages and 100 rows per query. The first HTTP
401 in an operation may reacquire credentials once through `sf` and retry the
same GET. Credential lookup and retry share the original budget. A second 401,
lookup failure, or changed origin returns `salesforce_auth_required`. Other
errors are not retried. The environment-token path cannot refresh. This is
separate from local MCP tokens, which still expire after five minutes.

The adapter exposes no arbitrary query or URL input. External mode omits the
write tool and rejects hardcoded writes with HTTP 403. Standard Contract records
join the existing `get_account` result and receipt. Their IDs, associations,
dates, category, and nullable integer term are validated. Briefs use the actual
returned EndDate, never an inferred date. Draft and InApproval contracts require
an explicit non-activation disclosure; Activated is a distinct category. An
account with no eligible dated Contract refuses before drafting. ARR, CRM risk,
and opportunity currency stay unknown rather than being inferred from unrelated
fields. [Briefing tests](tests/test_salesforce_briefing.py) cover required claims
and rejection of altered or omitted disclosures using invented records.

Account, contact, opportunity, and contract fields reach the authorized client
as untrusted data. `source_id` and namespaced `crm/...` references preserve
provenance. A Salesforce snapshot uses workflow version 3 inside schema version
1; fixture workflows remain unchanged. No record text can request an additional
tool or grant. AccountNumber creates no implicit join; the demo kit requires an
explicit private import binding. Calls and Slack still use isolated invented
sources and independent scopes.

Audit arguments can contain real search terms and IDs. Returned refs persist,
but response bodies and the upstream token are not copied to the audit. A
saved brief does contain source evidence and receipt copies in private state.
No Salesforce credential enters owner.json, Inspector config, brief evidence,
or model input. Existing private permissions and manual retention apply.

`salesforce check` is an explicit local-owner diagnostic. It probes credentials,
edition, a fixed allowlist of describe fields, and bounded reads directly, then
runs a brief through the local HTTPS gateway in temporary private state. Normal
completion removes that state. Its report contains only fixed step names,
pass/fail outcomes, counts, timings, fixed errors and fixes, and missing names
from the required-field allowlist. It returns no source records, native or
namespaced IDs, org details, credential values, or raw describe metadata. Its
`api_calls` count covers the connector's REST GET attempts, including retries;
it cannot measure the CLI's internal HTTP requests or total org quota usage.
The `sf_cli_invocations` count includes failed child-launch attempts. The
ordinary tool output, brief, and audit are not sanitized by this command.
Progress on stderr contains fixed step names, status, and counts only. In JSON
mode it uses JSON lines while stdout contains one final report.

[Credential tests](tests/test_sf_credentials.py) use only a fake CLI on PATH;
[adapter tests](tests/test_salesforce.py) use invented HTTP responses. The
[gateway tests](tests/test_salesforce_gateway.py) exercise scope denial, audit
failure, disabled writes, and accepted reads through local HTTPS. The separate
lead-agent live demo observation is recorded below; an owner-provided check
report is still required for verification. See [setup, report shape, and
limits](docs/salesforce.md).

No live org has been verified.

## Bound Salesforce demo kit

The packaged demo plan contains three invented Accounts, each with one Contact,
Opportunity, and Draft Contract, twelve records in total. Its local call and
Slack fixtures use standard AccountNumber values as public fixture keys. A
successful import receipt supplies the private Salesforce IDs. Binding requires
exactly the declared references, distinct IDs with the expected object prefixes,
and complete account associations. The import-plan type column can carry the
root Account type for nested records, so it cannot determine a child's type.
Asset hashes and the binding digest detect a different local kit or mapping;
they do not authenticate an org or turn the owner-supplied receipt into a signed
attestation.

Binding files are bounded regular mode-0600 files owned by the local user.
Writes replace a private file atomically. A source-profile marker under the
state lock pins the kit and binding before source initialization. Bound mode
requires fresh state initially and refuses a changed profile or default-fixture
reuse. Each source validates its complete row identities and associations.
These checks prevent accidental mixing; the local owner can still alter the
files. They do not add multi-user isolation or cross-database crash atomicity.

The gateway compares a bound AccountNumber with the already authorized
get_account result and refuses a mismatch without an additional CRM query.
Calls and Slack use their own local stores and grants, so reading only those
fixtures does not require or imply CRM access. The bridge is to invented local
context, not a live Slack workspace or call-recording service. All fixture text,
including the injection note, remains untrusted data. The usual receipts and
citation checks still apply.

The owner-run helper under scripts is separate from the installed connector.
Its import and removal commands use Salesforce CLI writes at the owner's request;
the connector stays GET-only. Cleanup reads only the imported IDs, verifies the
kit's supplied fields, three intentionally null fields, and parent associations across all four object types,
and requires imported Contracts to remain Draft. It inspects described
cascading and restricted relationships before starting deletes. The fixed
AccountHistory, ContactHistory, OpportunityHistory, OpportunityFieldHistory,
and ContractHistory objects are treated as system-derived history. AccountShare,
ContactShare, and OpportunityShare rows are allowed only with RowCause Owner.
Checked fields must be present in the response. Fields outside the kit are not
compared. Ordinary children must have passed the direct inventory and field
checks and be scheduled for deletion. Other related records or sharing causes
refuse cleanup. No broad name, marker,
or prefix query substitutes for a missing imported ID.

Cleanup then deletes Contact, Opportunity, Contract, and Account records in
that order and checks that the imported IDs are absent. Preflight and delete
are separate calls. Changes between them, restricted visibility, and triggers
can invalidate a prior check or cause effects outside its view. There is no
transaction or concurrency guarantee. A failure after deletion starts can
leave a partial cleanup; keep the private binding for reconciliation. The
operator must use the intended Developer Edition org, sufficient read access,
and a quiet demo book. The helper does not establish exclusive ownership of
all server-side relationships.

Import receipts, bindings, local evidence, and recordings can contain org IDs,
record IDs, and source data. They stay under ignored private var storage and
have manual retention. The recording script does not commit or publish a cast.
The owner decides whether a reviewed recording may be shared. This kit adds no
runtime dependency. [Kit and binding tests](tests/test_salesforce_demo.py) use
invented IDs; [runtime tests](tests/test_salesforce_demo_runtime.py) use invented
HTTP responses and local HTTPS.

An owner-authorized lead-agent run on November 17, 2026 imported twelve records,
bound three accounts, and produced thirteen checked brief lines with four audit
receipts. Evidence contained four CRM records plus one invented local call note
and one invented local Slack message. The Draft disclosure and returned
EndDate 2027-01-31 remained in the brief. The private cast and provenance files
completed with mode-0600 permissions. Cleanup preflight covered all four object
types, then confirmed twelve deletes and zero remaining imported records using
74 captured CLI commands. No org-bearing artifact was committed. This is one
observed demo run, not production approval or an owner verification report.
See the [owner-run guide](docs/salesforce-demo.md) for timings, prerequisites, and
the cleanup visibility and concurrency limits.

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
