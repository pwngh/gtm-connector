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
operator's existing org with an explicit environment expectation. Calls default
to fixtures and can instead use explicitly imported exports. Slack defaults to
local fixtures and can instead read an explicitly mapped thread. Separate owner
commands authenticate an existing Slack app and select that source;
authentication alone does not enable message reads.

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

Fixture Slack messages remain untrusted data in a separate store and scope. The optional
four-read workflow requires a matching receipt for each source. It distinguishes
empty messages from a denied grant. Its additional conflict rule matches the
exact delayed-review message against the existing procurement conflict and
cites all three records. The follow-up is a checked proposal, never a sent
message or a promise. The fixture workflow cannot create an activity or contact an external service.
Legacy three-read snapshots cannot introduce Slack evidence or a follow-up.

Snapshot saves write a mode-0600 temporary file, then replace the destination
atomically. A failed replacement keeps the previous complete snapshot. There is
no power-loss durability guarantee. A killed process can leave a private temporary
file, which follows the same manual retention rule. Audit inspection opens the
existing database read-only without initializing tables or changing permissions.
Custom Python adapters are trusted application code and are not sandboxed. The
CLI cannot load them.

## Optional Salesforce boundary

`serve --crm-backend salesforce --sf-org ALIAS` and the matching `brief`,
`accounts`, and `account` options capture an existing Salesforce CLI login
inside the connector process. The
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
Developer Edition remains the default environment expectation. Sandbox and
production require explicit `--sf-environment` selection. Startup checks
OrganizationType and the typed IsSandbox field before opening source stores.
Sandbox and production additionally require queryable standard objects and the
fixed required fields. Successful environment and capability metadata is cached
only for the adapter lifetime. CRM records are not cached. Later provider
permission failures still refuse.

The owner startup check writes a mode-0600 `crm-connection.json` under the state
lock. It pins the validated origin, org ID, and selected environment. Restart
must match that identity; aliases are not stored as authority. Connected state
cannot silently switch org, environment, or fixture mode. Older unpinned state
with artifacts requires a fresh folder. Malformed, linked, or nonprivate markers
refuse before source initialization. Marker replacement is atomic per file;
there is no cross-file transaction or power-loss guarantee. The independent
kit binding must also match before source data opens. Existing private owner
metadata also prevents fixture fallback after accidental removal of only the
connection marker. The trusted local owner can still edit or remove both files.
The complete owner format written before Salesforce support can still reopen
fixture state; incomplete or unknown metadata cannot select that legacy path.

Metadata preflight is an explicit local-owner operation outside the MCP audit.
Client CRM record reads still require a grant and committed receipt before any
provider read. Denied tools and failed initial audit writes make no additional
provider request after startup. The local owner can grant CRM access; this is
not Salesforce user delegation. The upstream token may have broader permissions.
The adapter implements GET only.

Requests use fixed SOQL fields, escaped search text, and bounded results.
TLS verification stays on; proxies and redirects are disabled. Pagination must
stay on the same query endpoint. Each operation retains the 12-GET, 1-MiB,
20-second bounds, with at most 10 pages and 100 rows per query. The first HTTP
401 in an operation may reacquire credentials once through `sf` and retry the
same GET. If identity is already known, it rereads Organization first and
requires the same org and selected environment, so a replaced sandbox cannot
reuse old identity through the same origin. This adds one GET only after refresh
and shares the original budget. A second 401, lookup failure, or changed origin
or org identity returns `salesforce_auth_required`. Other
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
provenance. A Salesforce snapshot with fixture calls uses workflow version 3
inside schema version 1; imported calls use version 4. No record text can request an additional
tool or grant. AccountNumber creates no implicit join; the demo kit requires an
explicit private import binding. Transcript imports resolve an explicit exact
AccountNumber or native account selection through a separate audited CRM read.
Calls and Slack retain independent scopes.

Audit arguments can contain real search terms and IDs. Returned refs persist,
but response bodies and the upstream token are not copied to the audit. A
saved brief does contain source evidence and receipt copies in private state.
No Salesforce credential enters owner.json, Inspector config, brief evidence,
or model input. The connection marker does contain private org identity and
must not be shared or committed. Startup labels and shareable diagnostics omit
that identity. Existing private permissions and manual retention apply.

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
[adapter tests](tests/test_salesforce.py) use invented HTTP responses. Sandbox
and production behavior has only invented-response proof; no paid or production
org has been exercised. The
[gateway tests](tests/test_salesforce_gateway.py) exercise scope denial, audit
failure, disabled writes, and accepted reads through local HTTPS. The separate
lead-agent live demo observation is recorded below; an owner-provided check
report is still required for verification. See [setup, report shape, and
limits](docs/salesforce.md).

No live org has been verified.

## Imported transcript boundary

`calls import` is an explicit local owner operation. It parses a selected TXT
or VTT export, obtains only `crm:read`, and resolves the selected account through
the audited gateway before committing an association. The exact Salesforce
AccountNumber lookup is fixed, escaped, and bounded to two matches. There is no
name-based inference. A missing, ambiguous, changed, or unreceipted association
cannot publish an import. No provider record is written.
The raw AccountNumber selector stays in private reassignment state and owner
CRM audit arguments. The public transcript manifest omits it, so a calls-only
grant does not reveal that CRM field through import provenance.

Imported mode pins the selected CRM identity and uses private `imports.sqlite3`
under the existing state lock. It does not seed fixture call notes. Mode and
identity mismatches refuse before source initialization. Construction validates
metadata without reading transcript blobs. `read_call_notes` checks `calls:read`
before opening a selected blob and makes no CRM request. Missing imports refuse
instead of returning demo content. The source permissions remain independent.

Original bytes, manifests, and active selections commit transactionally. Import
bounds are 2 MiB and 10,000 units; returned excerpts are at most 16 KiB of text,
256 units, and 128 KiB of ASCII-serialized record data. MCP wrappers add overhead.
The reader rehashes and reparses the original before returning selected units.
Ordinary tools, briefs, and inspection do not expose unselected content. Explicit
local `calls inspect --full` returns the complete original; it is not an MCP tool.

Workflow 4 validates the selected manifest, canonical ID, account binding, and
the brief's matching committed call-read receipt. The association receipt ID
is carried as provenance. Saved checks do not reopen the original bytes, prove
source truth, or authenticate a provider or speaker. An owner able to rewrite
all snapshots and receipts can alter that evidence. Reassignment creates a new
association revision and preserves accepted historical snapshots. Unknown call
time remains unknown; import time is not a meeting timestamp.

[Parser and store tests](tests/test_calls_import.py) use invented files.
[Brief integration tests](tests/test_imported_briefing.py) exercise local HTTPS,
an invented Salesforce AccountNumber/Contract flow, failed receipts, quoted
injection, bounded selection, snapshot tampering, and historical checks. No live
call provider or real transcript was accessed. The existing manual retention
policy applies to original exports, imported state, and saved brief evidence.

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

## Optional Slack authentication boundary

The owner explicitly invokes `slack connect` for an existing PKCE public app.
It requests user history grants only, validates their exact set, and verifies
the returned workspace and user. Credentials are pinned to app, workspace and
user in a separate private folder. Authentication does not enable a source,
change the demo or grant a caller `slack:read`. These authentication commands
call no message endpoint.
The existing local MCP token, scope and audit boundaries are unchanged.

The browser flow uses fresh state and S256 PKCE. Only the exact registered
localhost Host, port, path and unambiguous callback are accepted. The listener
owns supported localhost address families exclusively. The authorization code,
state and verifier stay in memory and are absent from results, progress and
fixed browser HTML. Publication runs synchronously before browser success.
Cancellation closes the listener and reaps only the bounded browser-launch
helper. It does not claim to close the user's browser or undo a completed grant.

Saved tokens and identity remain in an owned private credential file. The
connection lock serializes processes. No-follow descriptor-relative I/O rejects
symlinks, hard links, unsafe modes and unrelated directories. Atomic replacement
preserves complete state before publication; it cannot roll back a remote
refresh. `refresh_pending` marks an ambiguous exchange, identity mismatch or
definitively rejected saved credential unusable, including nonrotating grants.
`identity_unchecked` retains a received replacement or an explicitly checked
credential for a check-only retry, without exposing it to reads. The unchecked
marker is saved before checking existing credentials, so a failed rejection
save cannot leave an old ready file usable. A ready credential can be released
only with its stored grants and expected identity.

The provider uses four fixed Slack methods: exchange, refresh, identity and
explicit revoke. It follows no redirects, uses no ambient proxy or credentials,
and bounds response bytes and total HTTP time. Public errors discard provider
messages and all unknown fields. Status, errors and browser output omit tokens,
provider IDs, callback values and workspace details. Authentication is owner
administration rather than an MCP tool, so it does not create tool receipts.

Local disconnect makes no request. Explicit revoke reports only the access
token outcome and local deletion; it never claims app or full-grant removal.
Existing snapshots remain private local records under the existing retention
policy. A local owner who can read credential files can use the saved grant.
Other processes with that owner's authority are not isolated by this design.

The [provider tests](tests/test_slack_auth.py),
[callback tests](tests/test_slack_callback.py),
[composed HTTP tests](tests/test_slack_auth_integration.py), and
[CLI tests](tests/test_slack_cli.py) cover these boundaries with invented
provider responses and credentials.
No real Slack login, message read, app rendering or hosted assistant connection
has been verified. Actual use requires an existing approved public app with the
matching registered callback. Current credential filesystem proof is POSIX;
native Windows support requires its separately authorized platform slice.

## Selected live Slack boundary

`slack map` is an explicit owner operation. It resolves one account through the
existing audited `crm:read` tool and saves one thread selection, without reading
messages. Private source markers pin the selected CRM identity and the Slack
app/workspace/user/enterprise identity digest. Tokens, scopes, workspace origin,
and credential path are not identity keys. The copied URL must match the saved
workspace origin; it never supplies an HTTP destination or chooses credentials.
The native channel and parent timestamp are the fallback when no supported
origin is saved. There is no arbitrary query or channel-search tool.

The saved mapping contains the canonical account, selected channel and parent,
bindings, selection time, and the audited CRM association receipt reference.
It contains no raw AccountNumber or selector hash. Same-selection retries keep
the original entry. Replacement and reassignment require explicit flags and
publish one complete bounded document under the existing state lock. Changing
a source mode or identity refuses before seeding; live Slack has no fixture
message database or fallback. Imported calls and live Slack can initialize
together in either order without mixing fixture and selected-source state.

The existing `read_slack_messages` tool keeps its independent `slack:read`
guard and committed pending receipt before mapping, credential, or provider
work. Initial audit failure does no source work; final audit failure releases
no source result. The source makes only fixed GET conversations.replies requests
with a bearer header, verified TLS, no redirect, and no environment proxy.
Source text, file links, cursor values, and copied URLs cannot redirect it.
The scope permits requests for any mapped account in this connector. Per-caller
account and resource selections remain a separate slice; a mapping is not a
caller-specific grant.

One shared budget includes message pages, authentication, failed requests, and
retries: ten HTTP attempts, 1 MiB of response bytes, and an absolute 20-second
cancellable HTTP deadline. Authentication also keeps its individual 64 KiB
response bound. The reader allows at most 100 raw rows, 16 KiB per literal text,
64 KiB combined text, and 256 KiB for the normalized source envelope. MCP text
and structured wrappers can duplicate that envelope. Filesystem operations
remain synchronous and have no hard preemption guarantee.
Shutdown retains TLS and the state lock until active services finish. There is
no guaranteed total shutdown time.

Only exact token expiry permits one recovery retry. Credential recovery rereads
under the connection lock and checks the expected identity before verification
or rotation, including initial acquisition. Mapping setup also carries the
initially inspected identity forward, so a concurrent connection switch refuses
before source pinning or CRM resolution. Another process's
different verified token can be reused without a duplicate refresh. One local
refresh is the maximum. Terminal rejection quarantines only the exact saved
token and identity; it preserves a concurrently saved replacement. Rotation
requires room for both refresh and identity HTTP attempts before a pending
marker is written. With insufficient request slots, the reader leaves credentials
unchanged and reports the resource limit instead of requiring another login.
Only request slots are reserved. The shared byte or time budget can still run
out after rotation starts; an ambiguous refresh then requires browser login.
One message rate-limit retry must have a valid delay
of at most five seconds within the remaining budget. An ambiguous refresh is
not retried. Expired unchecked credentials require browser authorization.
No tool starts that browser flow, sends a message, or broadens a grant.

The reader requires the selected parent on the first page, strict membership,
valid timestamps, and completed cursor pagination through one creation cutoff.
Identical duplicates consume bounds; conflicting duplicates refuse. Missing
text, unsupported subtypes, hidden content, and event wrappers refuse the whole
observation. Files, blocks, and attachments contribute omission counts only.
Their inline content is received in the API response and omitted from evidence;
no separate non-text resource requests are made.
Literal text remains untrusted; author labels do not authenticate a person.
Cursor completion does not establish a full archive, earlier edits/deletions,
current source truth, or an atomic view while messages change.

Workflow 5 binds the selected mapping, ordered message hashes, counters,
observation times, record IDs, and matching tool receipts. The extractive brief
requires literal quotes, edit and omission disclosures, and the observation
limits. Saved checks use copied evidence only. They do not reopen credentials,
source files, or Slack, and they cannot resist an owner rewriting all evidence.
Older evidence versions cannot accept live Slack metadata by dropping its
workflow version. Source snapshots and ordinary tool output remain private;
they contain selected text and provider identifiers.

`slack check` prepares only the existing saved Slack association and audit
boundary. Its temporary local HTTPS service enables only the Slack read tool,
including when a caller presents a broader local grant. It opens no CRM or call
source, loads no Salesforce credential, and preserves the ordinary owner/TLS
files. Its shareable report contains fixed steps, outcomes, counts, timings,
and recovery commands, with no account/workspace identifiers, paths, URLs, or
message text. It explicitly does not check current CRM or call access. Missing
telemetry never becomes an invented successful result or a zero-work claim.
Shutdown waits for active service threads before releasing temporary TLS files
and the state lock. Cancellation can wait for an in-flight read and receipt
handling; filesystem operations still have no hard interruption guarantee.

[Mapping tests](tests/test_slack_mapping.py),
[runtime tests](tests/test_live_slack_runtime.py),
[reader tests](tests/test_slack_reader.py),
[CLI tests](tests/test_slack_source_cli.py),
[brief tests](tests/test_live_slack_briefing.py), and
[check tests](tests/test_slack_workflow.py) exercise these implementation
contracts with invented inputs and local HTTPS. [Local HTTP tests](tests/test_live_slack_http.py)
exercise actual socket requests and streamed-body cancellation. No real Slack login, thread
read, app rendering, or hosted assistant connection has been verified. Existing
registration, workspace approval, response compatibility, retention, and the
app's distribution-specific rate policy remain live-use prerequisites.
See [owner commands and limits](docs/slack-live.md).

## Selected sources and caller-run briefs

`serve --selection FILE` limits every client read to the accounts and Slack
threads in an owner-written file. The file must be a regular file the owner owns
that others cannot write. `serve` validates it before opening any credential or
source, and the running server never reloads it. Each read checks the selection
before any source access. An unselected account is refused with
`account_not_selected` and is never looked up. A Slack read also needs that
account's Slack thread selected. Searches and account-wide opportunity lists add
the selected IDs to the SQLite or Salesforce query itself, before ordering and
the result limit. A selected server offers no write, and a write attempt is
refused as `crm_write_disabled`.

`prepare_renewal_brief` runs under the bearer token the client presented. The
remote path never reads `owner.json`, uses the owner credential, or calls the
local server over HTTP. Each read inside it commits its own pending receipt with
the caller's credential fingerprint, verifies the token again, counts one
attempt against the caller's rate window, and checks its own scope and the
selection. It then runs the same source code as the direct tool and finishes the
receipt before the data reaches the brief. A missing calls or Slack scope gives
the existing denial disclosure. Any other failure, including an audit write
failure, an expired token, a rate limit, or a 45-second brief deadline, leaves
no readable brief. Caller-supplied metadata cannot change which receipts a
brief's evidence uses, and an object-valued JSON-RPC id is recorded as a fixed
marker, so it cannot imitate the reads' correlation.

A saved brief counts only if its own outer call finished successfully and that
receipt names the SHA-256 digest of the saved file. `read_renewal_brief` makes
no source system call. It releases the brief only while the account and any
Slack thread it contains are still selected, the caller's current grant has
every scope the brief's content needs, the CRM, calls, and Slack bindings are
unchanged, and imported calls and live Slack still use the same transcript
revision and mapping. For live Slack it also requires the local connection to
hold a ready credential for the same identity with at least the scopes the
brief's read used, and it decides while holding the connection lock, so a
disconnect or narrower reconnect withholds saved Slack text. It reports that it
did not recheck the source systems. A brief ID alone grants nothing.

The selection chooses accounts and Slack threads, not fields. Without
`--selection`, a connected server still exposes its whole source to any
`crm:read` grant. The final grant check runs when the tool finishes, so a grant
that expires before the receipt is written can still receive that one response.
[Selection tests](tests/test_selected_sources.py) and
[live Slack selection tests](tests/test_selected_live_slack.py) exercise these
boundaries over local HTTPS with invented sources. See [the selection guide](docs/selection.md).

## Required sign-off evidence

A reviewer must inspect the scope-denial and invalid-token tests, audit failure
and committed-write retry tests, injection and citation-rejection tests, and
the 20-account evaluation with its fixed expected claims. Confirm that the
clean-checkout demo passes, the injected note creates zero activities, and usage
totals match receipts. Review [DECISION_LOG.md](DECISION_LOG.md) and accept the local
owner, manual-retention, snapshot, and fixed-vocabulary limits explicitly. Real data
or shared deployment requires a separate review. Before using an org, inspect
its permissions and data, the adapter's fixed fields and read-only tests, and
the local owner's ability to grant access. The test result is not approval to
read production or client data.
