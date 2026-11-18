# Decisions

## Measure process startup and audited calls (2026-11-17)

Use one fixture-only benchmark for fresh-process version, help, brief, server
readiness, and audited HTTPS calls. Record raw samples and nearest-rank p95 with
the source commit, script hash, date, and machine details. A fresh process does
not imply an empty operating-system cache. Include shutdown in the brief time;
stop the server-readiness timer at its flushed ready event. Exclude receipt
verification from the call timer, then require a committed successful receipt.
Compare each complete call with a direct SQLite read to state the connector's
added cost. Do not use the audit row's elapsed time, which precedes its final
commit. These measurements describe one machine, not a latency guarantee.

Generate a fresh P-256 certificate on each start using the existing cryptography
dependency. Certificate generation was a measured source of startup variation;
[before and after measurements](docs/performance.md) record the effect. Keep
the loopback subject alternative name, one-day validity, private key permissions,
and explicit client trust. Do not cache keys or skip authorization or audit work.
The [cryptography API](https://cryptography.io/en/latest/hazmat/primitives/asymmetric/ec/)
provides the key generator; local HTTPS and Inspector tests verify these clients.
No runtime dependency changes.

CI holds the mechanisms rather than timing a shared runner. It checks the fresh
key type and identity, help/version import boundaries, committed receipts, and a
small benchmark report. Existing HTTPS, Inspector, and audit tests still run.

## Running server and daily commands (2026-11-17)

Emit one bounded event after each recognized tool attempt. Use a separate
performance clock for its duration so logging does not alter request-check
clock reads or rate-limit decisions. Include both audit writes in that duration.
The caller is the audit credential fingerprint, not a verified person. Emit a
null receipt ID when the initial audit write fails. A callback failure cannot
change the response or audit outcome. Queue events to the CLI so terminal output
does not run inside the gateway. No arguments or source records enter this stream.

Keep audit and usage inspection read-only. Filters combine exact tool, caller,
status, and brief matching with aware timestamp bounds, normalized to UTC.
Follow emits changed rows or reports rather than treating the first pending row
as final. Usage keeps its narrow query without loading audit arguments. With
receipt filters, model totals count each matching saved brief once and describe
that entire brief; they are not token allocations to a tool or time interval.

Doctor checks private local files and matching HTTPS metadata without obtaining
a grant or reading source records. Its errors describe checks, never raw
configuration or transport exceptions. Connect prints setup without launching
an assistant. Only explicit scopes provision a private supplied-token config.
Reuse the existing owner authorization flow rather than adding login behavior.

Read current official Inspector, Claude Code, Claude Desktop, and Cursor docs
on 2026-11-17. The Inspector CLI supplied-token path is tested. Other apps stay
untested. Document the fixed client and callback, private owner header, missing
browser consent and registration, five-minute expiry, local certificate, and
Desktop cloud-to-loopback incompatibility. Do not add a tunnel or deployment.

Generate completion from argparse metadata with no new runtime dependency.
Exercise command, flag, and choice completion in bash, zsh, and fish; CI installs
fish and zsh only as test tools. Evaluation reports count/status progress without
source text. JSON mode uses JSON lines on stderr, while its result stays a single
stdout JSON object. Existing command names and unfiltered result shapes remain.

## Installed CLI and output contract (2026-11-17)

Install the existing package as `gtm-connector` with the same `python -m`
entry point. Keep argparse and small standard-library renderers because the
existing parser is sufficient and help must not import the server or SQLite.
No runtime dependency is added. Package the independent evaluation manifest
so installed evaluation works outside the source checkout. Dependency metadata
reads the existing exact pins; the checked-in locks retain their hashes.

Follow clig.dev and GitHub CLI's maintained Primer guidelines, read on
2026-11-17. Default output is human-readable. Terminal tables use headings;
pipes use complete plain tab-separated rows. `--json` preserves the existing
command-specific success shapes so scripts can opt in without a new envelope.
Errors use a small documented stderr object in JSON mode. Split usage, refusal,
failed-check, upstream, and local-operation exits so automation can respond.
Help remains text in JSON mode because it describes invocation, not a result.

Move runtime imports after argument parsing. Keep the fixed demo date loaded
only when a brief runs. Do not change the gateway's refusal codes or citation
rules. Update scripts that consume results to request JSON, including the
recording rerun checks; historical recordings retain their original provenance
until the separately requested narrative recording is made.

Use the manual stack fallback because tstack was not available on PATH or in
the local executable search. PR #19 was merged before this work began. Keep
one own branch per slice, Sol review on high, green CI, and no merge or auto-merge.
The user's six-slice instruction supersedes the older stop-after-one follow-up
and the restriction on plausible invented labels for the later demo slice.

## Generated audit receipt sequences (2026-11-17)

The audit implementation already commits a pending row before dispatch and
updates a receipt only while it is pending. Add generated sequences around those
rules without changing production code or schema. Hypothesis varies calls through
the actual gateway and six tool handlers using an in-process HTTP test client.
The existing demo continues to cover local HTTPS.

Observe each source connection opening, before its query runs. A separate
read-only SQLite connection must see exactly one matching pending receipt for
that request. Store any observation failure and assert it outside dispatch;
the SDK can otherwise convert an assertion into a tool error. Start each sequence
with a successful read so an all-denied sequence cannot pass without execution.

A test-only trigger records committed audit updates. Require at most one final
update per receipt and preserve every field of finalized rows. Generate explicit
finish retries against earlier receipt IDs. Inject failed inserts and updates
through SQLite triggers, and an exception after a finish commits through a test
wrapper. The latter exercises the gateway's fallback finish attempt: calling
finish twice is allowed, but committing two finalizations is not.

Bound each search to 50 examples and 20 state-machine steps. Use deterministic
generation and no example database, so prior runs do not change the inputs.
Sequences may change with Python, Hypothesis, or test revisions. Disable per-step
timing deadlines because SQLite latency depends on the host. This is a bounded
sequential search, not proof of every concurrent schedule or power-loss behavior.

Pin Hypothesis and its dependency only in the hash-checked development lock.
Keep the suite in tests_stateful and run its explicit command in CI on Python
3.11 and 3.14. Missing Hypothesis must fail that command, not silently skip it.
The default demo still installs the runtime lock and runs its existing tests.
Keep the next schema-constraint slice separate.

## Request refusal reasons (2026-11-17)

Give each gateway request-check refusal a fixed `error.data.reason`. A client
can branch on that field without parsing the message. One table supplies the
HTTP status and JSON-RPC code. Keep `1001` for invalid credentials and `1002` for
rate limits. Assign `1003`, `1004`, and `1005` to an untrusted origin or host,
insufficient scope, and disabled CRM writes. Preserve the standard negative
protocol codes; a reason distinguishes cases that share one of those codes.

Preserve existing messages, status codes, challenges, and decision order. Missing,
malformed, expired, and wrong-resource credentials all remain `invalid_token`.
Clients do not need the credential's failure details to request a new grant.
Scope and rate details remain in `WWW-Authenticate` and `Retry-After`.

The catalog covers request checks, not every error returned by the service.
Leave SDK-owned rejections, OAuth responses, and tool execution errors unchanged.
Keep the two HTTP 503 fallback bodies: an initial audit failure prevents execution,
while a later failure can follow a committed write. Audit rows still record their
existing outcomes. This slice adds no schema or permission changes.

Read the [MCP error contract](https://modelcontextprotocol.io/specification/2026-07-28/basic)
and [authorization requirements](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization)
on 2026-11-17. Use the optional JSON-RPC error data field. The idea of a stable
reason follows [AIP-193](https://google.aip.dev/193); this connector does not adopt
its protobuf response format or claim AIP compliance.

## Python checks and dependency hashes (2026-11-17)

Run Ruff and mypy in CI for pull requests and pushes to main. Ruff checks the
application and scripts for common errors and lines over 120 characters. Mypy
checks every application module, including unannotated function bodies. This is
an initial type-checking baseline, not strict typing: unannotated interfaces and
dynamic JSON still use Any. Keep existing tests byte-for-byte unchanged and
outside these static checks. The behavioral suite still runs on Python 3.11 and
3.14. Avoid a formatter pass that would obscure this change.

Use explicit re-exports for the shared seed and database functions. Keep the
Inspector script's path setup before its imports, with a scoped E402 exception.
Use the SDK's Python field names for ToolAnnotations; its wire aliases stay the
same. Mark the authority factory's one SDK protocol mismatch with a targeted
arg-type suppression. The protocol also requires an identity-assertion hook;
this issuer advertises only authorization_code and does not implement that hook.
Do not add a grant or change rejection behavior to satisfy a type checker.

Freeze the existing runtime versions in requirements.in and add published
artifact hashes to requirements.lock. Keep Ruff, mypy, and pip-tools in a separate
development input and lock. The default demo installs only the runtime lock.
Require hashes at each checked-in pip install entry point. Hashes detect an
artifact that differs from the reviewed lock; they do not establish that an
upstream package is safe or make source-build dependencies fully reproducible.
The locks include hashes for multiple platform wheels. Regenerate them with
Python 3.14 and the pinned tooling, then verify both supported CI versions.

The README badge follows the main branch. Its first push-triggered run starts
after the owner merges this change. The badge links to the workflow results;
it does not imply that CI or a type checker proves security.

## README navigation (2026-11-17)

Compare the README with [Soulbeet](https://github.com/terry90/soulbeet/blob/dfd46b90ac49a9571a8d4d36d17604b92233b7e7/README.md),
[Homelable](https://github.com/Pouzor/homelable/blob/645d1f719e9d6738e5a44ef0b042e656d6aea494/README.md),
and [HomeButler](https://github.com/Higangssh/homebutler/blob/923a0feac625510d521b5e7b6367f0717cedfc79/README.md).
These are examples of document structure, not evidence that a layout improves
hiring or adoption. Do not copy their prose or infer performance from popularity.

Use Homelable's section navigation to give readers a direct route to the example,
run command, guarantees, limits, and references. Put the local demo and its
walkthrough before Codespaces setup, following the separation of quick start
from configuration in Soulbeet and HomeButler. Give optional features their own
section so their requirements do not look like default prerequisites.

Keep the product definition, captured brief, and request diagram. They already
show the result and explain the sequence, as the report example in HomeButler
and the process explanation in Soulbeet do. Keep the reference pages unchanged;
they already separate purpose from implementation detail. Preserve all existing
commands, output, tables, numbers, links, and limits. This change adds navigation
and moves setup blocks. It does not change behavior or produce new benchmark data.

## Documentation for readers new to the project (2026-11-17)

Start with the renewal brief a reader would use, then explain the connector
behind it. The README keeps the evidence near that introduction so someone
evaluating the project can see what was tested before studying the protocol.
Reference pages introduce the purpose of each part before its mechanics.
Define unfamiliar product terms briefly. Let the source table explain the inputs
before the brief example, and let the request-path diagram show how the parts
connect. Do not turn each technical term into another introductory paragraph.

Use paragraphs to explain a sequence or a reason. Keep lists where readers need
to compare independent guarantees, and tables where values belong side by side.
Vary sentence structure without replacing precise terms with synonyms that
could change their meaning.

This pass changes the README and reference prose only, alongside these decision
notes and the agent work plan. Preserve commands, output blocks, tables, numbers,
links, and limits. The recordings and measured results remain historical evidence
from their documented runs. Do not recapture them or suggest this wording pass
produced a new benchmark. Runtime code, tests, and the security review stay unchanged.

The user requested council to compare the completed wording with main. Keep the
product definition and earlier source table. Restore precise qualifiers where
simplification blurs a boundary: the model run supplies no evidence of caught
hallucinations, usage reads audit receipts separately from saved model counts,
and the follow-up is a suggested next step rather than a drafted message. Keep
setup instructions actionable without fragment openings. The new pass does not
restore the abandoned prose rewrite from the parked branch.

## Pure request decisions (2026-11-17)

Move the request checks into check_request. Pass parsed request facts, policy,
token claims, clock readings, and the current rate windows as data. The function
does no I/O and changes none of those inputs. It returns a rejection or permission
to dispatch, along with replacement rate windows when the attempt consumes quota.
A rate-limit rejection also returns the pruned windows without adding a call.

Keep token verification and both clocks in the gateway. The checker requests each
missing input in the original order. The gateway supplies it and calls the same
function again. These explicit checkpoints preserve early refusals before
verification, wall-clock reads only for tokens with an expiry, and rate-clock
reads only for authenticated tool attempts. They add no callbacks or hidden I/O
to the checker. Read current rate windows after the verifier await so another
request's increment cannot be lost while verification is suspended.

Keep the surrounding work in order: read and parse, commit a pending receipt,
check, verify when needed, apply the returned rate windows, dispatch or reject,
finalize the receipt, and send. Apply window changes even if a later scope,
argument, or protocol check refuses the call. Preserve error codes, messages,
headers, SDK fallback, and the existing exception boundary. Do not add new policy
or change the existing tests, schemas, demo commands, or measured results.

## Shared tool definitions (2026-11-17)

Keep one entry per tool in tool_definitions.py. Each entry names its scope,
read/write mode, and string argument rules. Registration, the HTTP boundary,
and the handler scope check read that entry. This removes separate policy maps
and the special-case nullable filter from the gateway. Handlers still check
scope before opening a store. External CRM mode still omits the write tool and
refuses hardcoded write calls after authentication and scope checks.

Preserve the existing argument behavior. Limits count characters, not bytes.
Required strings must contain a non-whitespace character. Optional query text
can be empty. Only the optional opportunity account filter accepts null. Do not
trim or coerce values. Unknown tools still reach the SDK's existing rejection.
Keep audit, authentication, rate-limit, scope, argument, and protocol check order.

Leave handler signatures and descriptions unchanged so the SDK produces the
same public schemas. The gateway enforces length limits; this refactor does not
add schema constraints. Keep the authorization server's grant roster separate
from per-tool requirements. Its supported scopes and the protected resource's
minimal initial scope do not change. Independent scope expectations in tests
and Inspector checks remain independent of the table.

## Development container and Codespaces (2026-11-17)

Use the existing demo commands inside a Python development container. The README
badge opens GitHub's creation page for the default branch. The reader creates
and owns the codespace. This change creates no codespace or repository setting.

Pin the published Python 3.11 Bookworm image by its multi-architecture manifest
digest. Python 3.11 is the oldest version covered by CI. The digest makes the
base image repeatable; updating it requires another reviewed change. Run terminal
commands as the image's non-root vscode user. Reuse scripts/demo.sh for dependency
installation and testing instead of adding a second setup command or startup hook.

Keep the client and both HTTPS services inside the container. Configure no
forwarded ports and tell the editor to ignore automatic forwarding. The existing
loopback addresses and certificate remain unchanged. This is a development
environment, not a hosted connector or a new isolation guarantee. No keys, model
downloads, external adapters, or additional credential mounts are configured.

The image and first dependency install need internet. Codespaces compute and
storage follow the reader's account rules. Stopping ends running processes but
retains storage, so link to GitHub's billing explanation rather than promise
unlimited free use.

Validated the configuration with Dev Containers CLI 0.90.0 and Docker locally.
The clean-checkout five-minute demo passed all 231 tests, evaluation, usage, and
citation rejection on Linux arm64 with Python 3.11.18. Docker reported no
published ports or privileged mode. No hosted codespace was created. Keep that
untested browser path explicit in the README.

## Recorded walkthrough and Inspector output (2026-11-17)

Make the existing proof visible without asking every reader to install the demo.
Record a checked-in script running real fixture commands. Keep runtime code,
existing tests, demo behavior, and evaluation measurements unchanged. Codespaces
belongs in the next PR; neither this capture nor its tooling creates a codespace.

Use asciinema for terminal output and agg for repository-local GIFs. This avoids
a video-hosting account or external player. Keep the timed capture and a plain
transcript beside the images so a reader can inspect or replay the same output.
Select fields from captured results, label them as excerpts, and cap idle time
in playback. Do not present the recording duration as an execution benchmark.

Capture the official Inspector CLI tools/list response through the existing
gateway with an explicit local CRM grant. It lists six tools, including Slack.
Show a name/scope projection in the final frame and retain the complete response.
This proves a CLI exchange with a supplied grant, not an Inspector UI session or
interactive login. The existing integration test remains the broader protocol proof.

Run in fresh invented state. Keep raw logs, local credentials, and absolute paths
out of displayed output. Record source revision, commands, exits, tool versions,
and UTC capture times. Scan the published text before rendering and inspect the
images before filing. Private state remains under gitignored var/ with manual
retention. The local-owner boundary does not change.

Require a clean source tree before capture, excluding generated media, so the
recorded commit identifies the executed inputs. Use isolated Python for capture
and rendering. Keep privacy checks active under optimization and require the
captured terminal output to match the saved transcript before making images.

## Saved evidence and audit inspection (2026-11-17)

Finish the reliability work that was parked while the optional adapters shipped.
Port the persistence and inspection fixes onto current main. Keep the existing
source adapters, scopes, draft checker, evaluation results, and README structure.

Serialize a brief before opening any file. Write it to a private temporary file
in the same directory, close it, then replace the destination atomically. Readers
see a complete old or new snapshot. A failed write or replacement keeps the
previous snapshot, which may still say collected. A failed first save prevents
drafting. Ordinary failures remove the temporary file; a killed process can
leave one behind. Temporary names do not match saved-brief JSON files, so usage
reporting ignores them. Retention remains manual.

Do not claim power-loss durability or signed evidence. There is no file or
directory fsync. The local owner can still change or delete the files. This
change protects an existing snapshot from an interrupted replacement, not from
an untrusted operator. The SQLite audit keeps its separate durability behavior.

Audit inspection must not run the writer's initializer. Open the existing file
with SQLite mode=ro, select the expected receipt columns, and preserve row order
and brief filtering. Missing databases or schemas are errors, not empty logs to
initialize. Keep audit creation and write-time permission setup in AuditLog.
A malformed saved brief or evidence container must produce a CLI error instead
of an AttributeError traceback. The citation rules stay unchanged.

## Optional Salesforce reads (2026-11-17)

Select the external reader only with `serve --crm-backend salesforce`. Environment
variables alone never change the backend. Keep the default demo, evaluation,
and source fixtures unchanged. Pass an injected reader to Runtime and Connector;
the three existing CRM handlers retain their scope and audit boundary.

Use REST v68.0 and fixed GET queries for Account, Contact, and Opportunity.
Read OrganizationType on each operation and require `Developer Edition` before
CRM queries. Do not create an org, request credentials, or claim a live test.
Tests use invented HTTP responses. The constructor validates configuration
locally and sends no request. An edition lookup happens inside tool execution,
after the pending receipt and CRM grant check.

Accept one explicit HTTPS My Domain host and one access token from the environment.
Disable environment proxies, redirects, refresh, and retries. Bound query pages,
records, response bytes, and time. Follow only same-origin relative query
locators. Escape SOQL string and LIKE metacharacters; accept only namespaced
18-character IDs for account lookup. Errors expose fixed codes, not response
bodies or credentials. Scope failures never contact the external service.

Preserve native IDs as source_id and prefix adapter IDs with acc-sf-, con-sf-,
or opp-sf-. The gateway still supplies crm/... references and untrusted_data.
This prevents accidental joins with the fixture IDs. Do not invent a cross-org
identity system. The runtime is single-operator and configured for one org.

Standard Account fields do not identify this connector's ARR, renewal date,
or renewal risk. AnnualRevenue is not ARR. CloseDate is an opportunity field,
not an account renewal. Keep raw StageName and the source IsClosed and IsWon
flags. Preserve Amount as a decimal string or null; currency stays null because
CurrencyIsoCode is not available in every org. No USD assumption, stage rewrite,
or fabricated renewal field is allowed. Brief creation refuses the external
backend with renewal_fields_unmapped before reads or drafting. A later mapping
needs explicit field choices and its own evaluation.

Remove log_activity from the external tool registry and reject hardcoded calls
with HTTP 403 after authentication and scope validation. Do not write to the
local CRM either. Runtime still initializes the fixture files for local calls,
Slack, and direct owner administration. That startup work does not copy org
records. Existing SQLite writes and their idempotency behavior stay unchanged.

The local owner controls which MCP clients receive crm:read. The Salesforce
credential remains an operator credential, not delegated end-user identity.
An upstream HTTP 401 becomes a bounded MCP tool error; it must not masquerade
as a local OAuth challenge. Audit arguments and returned record references may
now identify real records, so retain the existing private-state and manual
retention limits and document this additional boundary.

## Optional account-channel context (2026-11-17)

Add a third isolated SQLite source for mock Slack account messages. Use stable
`msg-NNN` IDs and account-specific channel labels. Keep message text untrusted.
The new `slack:read` grant implies neither CRM nor call-note access. Register
one tool through the existing gateway so its reads and denials use the same
scope checks, rate limit, and two-phase audit path. All four database paths,
including audit, must resolve to distinct files.

Check existing device/inode identities before seeding as well as path names.
Different hardlinked paths can otherwise combine the Slack and audit tables
inside one file. Reject that configuration before either table is initialized.
This startup check does not defend against a malicious local file owner.

The clean-checkout run hit three 30-second CLI subprocess deadlines under local
host load. Those same tests passed unchanged in 11 seconds combined when run
alone. Give these CLI integration subprocesses 60 seconds, still bounded, and
rerun the clean walkthrough. This changes the test deadline, not runtime request
timeouts or a performance claim.

Select three-source briefing with `--with-slack`. Keep default commands and
the existing 182 expected claims unchanged so the recorded two-source model
benchmark remains reproducible. The source store and tool are available to
scoped clients regardless of which briefing mode is selected. The extended
demo proves denial in both source directions; Inspector tests all six pairs.
No Slack account or external API is used.

Version the evidence, not the outer artifact envelope. Missing version or
integer version 1 retains the three-read validator. Version 2 requires four
receipts and an explicit Slack outcome. Unknown or malformed versions fail.
Legacy snapshots reject Slack records, receipts, and status. A saved snapshot
does not silently acquire new evidence or follow-up text. Read-only failure
handling remains the same: only the matching insufficient-scope denial permits
a partial brief. Empty messages and denied messages cite distinct receipts.

Keep the old procurement conflict claim. Add one bounded three-source rule
matching that exact procurement note, `closed_won`, and the exact delayed
security-review message. Cite all three records and resolve none of them.
Do not infer arbitrary contradictions or claim that a channel message is true.

Version 2 requires one supported follow-up proposal with citations. For the
recognized conflict it asks for confirmation of procurement and security review.
If only the CRM/calls conflict is known, it asks about procurement with those two
citations and makes no security-review claim. Otherwise it asks for review of
the cited renewal context. It names no owner
and promises no action, date, or discount. Copy the checked line into
`proposed_follow_up` only after the whole brief passes. Add no sending tool,
unchecked model field, or approval prompt.

Keep the original claim objects in `evaluation/expected.json`. Store static
Slack additions in its extension and select them explicitly. The full mode
uses 80 read receipts. Keep the default Ollama prompt unchanged; only version 2
adds Slack and follow-up instructions. Existing model results cover the default
task, and no three-source model metric is claimed without a separate run.

## Optional local model drafting (2026-11-17)

Keep fixture drafting as the default. Select Ollama only with `--adapter ollama`
and an explicit `--model`. Reuse the installed HTTP dependency against fixed
loopback port 11434. Disable proxies and redirects. Require a cached completion
model with no remote metadata or cloud suffix. Never install or pull a model,
change the running service, or fall back to another drafter. The local service
and model files remain trusted operator software, not a sandbox boundary.

Preserve the existing exact-text checker. Give the model a catalog derived from
the authorized evidence and ask it to assemble those claim lines. Do not call
this unrestricted summarization or semantic verification. The external prompt
contains no credentials, audit fingerprints, or tool interface. The model's
actual reply must pass the existing whole-brief gate without repair or filtering.
Malformed output, unsupported wording, duplicate lines, and missing mandatory
disclosures reject publication. Supported subsets can still pass the checker;
the independent expected claims measure omitted facts.

Add a typed draft result without breaking custom adapters that return strings.
Record API-reported input and output tokens before checking, including rejected
generations. Unknown counts stay null. A trusted evaluation callback sees the
candidate and an evidence copy in memory. Persist only safe diagnostics and
usage when rejected, never raw candidate text. The original evidence remains
separate from adapter and observer copies.

Keep the twenty-account expected manifest unchanged. Score raw candidates and
published briefs separately. Candidate metrics cover only extractable candidate
text and report unscored cases. Unsupported wording is not proof of factual
falsity. Keep zero-output invention rates undefined. The existing evaluation
success condition still requires full coverage; a new publication-gate field
reports that rejected drafts stayed unpublished and no activities were created.
Record exact CLI arguments, UTC times, model digest, service version, and usage
in each measured model report. Do not fabricate a rejection to improve a table.

Use the already cached `llama3.2:latest` for the local benchmark. It is the
smallest cached completion model on this machine. No model download is required.
Default demos and automated tests remain model-free. Mock HTTP responses test
error handling; only an executed local CLI evaluation supplies model metrics.

The first local run rejected 19 responses as invalid JSON. A diagnostic request
showed the small model repeating the JSON schema before its answer. Keep the
schema in the API `format` field and use a short response-shape example in the
prompt. Do not strip prefixed objects or repair generated content. Rerun the
full evaluation after this prompt change and report its measured results.

Sol review identified two parser and routing gaps. Reject duplicate JSON keys
instead of accepting the last claim array. Require Ollama 0.30.0 or newer and
append its explicit `:local` selector for every generation. Preflight metadata
alone cannot prevent an ordinary tag update from selecting a remote model.
The local-only selector rejects that case before cloud forwarding. Reject
remote metadata in responses too. Record the actual echoed model identifier
and the preflight digest; stable local tags remain a reproducibility assumption.
The independently scored publication flag also checks citation coverage for
every published expected claim, even if the production checker regresses.

## External Inspector client (2026-11-17)

Re-read the current MCP specification, authorization section, and official
Inspector documentation on 2026-11-17. The current protocol remains 2026-07-28.
Inspector 2.15.0 supports it through `protocolEra: modern`. Modern discovery
uses `server/discover`, not the older `initialize` handshake. Keep the existing
Streamable HTTP endpoint and pinned Python SDK 2.3.0. Do not add a second tool
dispatch path or weaken protocol-header validation to accommodate older clients.

Add `inspector-config` as a local owner utility. It uses the existing S256 PKCE
flow to request exactly the scopes named by repeated `--scope` flags. It writes
a mode-0600 `inspector.json` containing the resource URL, modern protocol choice,
and bearer header. It prints paths and scopes, never a credential. The owner
credential stays out of that file. Scope denial does not acquire a broader grant.
The token expires after five minutes; re-run the utility to request another.
Restarting `serve` changes the endpoints and invalidates every old grant.

The external client trusts the generated certificate through
`NODE_EXTRA_CA_CERTS`. TLS verification stays enabled. This grants trust to that
Node process without changing the system trust store. Inspector handles MCP
requests; the included client only provisions its grant. This tests client
interoperability with a supplied bearer token, not Inspector's interactive
OAuth login. Browser origins remain rejected by the connector.

Inspector honors inherited HTTP proxy settings. Set a loopback bypass in both
`NO_PROXY` and `no_proxy`, preserving other entries. The integration check also
forces certificate verification on. Warm the pinned npm package before changing
an existing extra-CA setting so a private registry can use its configured trust.

Keep Inspector and Node optional. The Inspector integration test is a separate
CI job and explicit command. Neither default demo script installs or invokes
Node, Inspector, a model, or an external source API. The fixed twenty-account
evaluation stays unchanged. The documentation follow-up remains a separate PR.

References: [Inspector](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector),
[protocol eras](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector/protocol-eras),
and [configuration](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector/configuration).

## Scope and implementation

Use Python 3.11+ and SQLite. PR 1 used only the standard library; PR 2 adds the
official MCP Python SDK 2.3.0 and local HTTPS dependencies. No data or code comes
from an employer or client. Fixture currency values are integer USD cents.

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
package publishing, web UI, or multi-tenant features. External CRM reads are
limited to the explicitly requested adapter described above.

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

Snapshot saves now use atomic replacement, as described above. A failed write
keeps the previous complete snapshot. If no snapshot was saved, generate a new
brief. No power-loss durability is claimed. The separate SQLite audit receipts
retain their existing durability behavior.

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
