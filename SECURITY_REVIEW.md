# Connector security review

Scope: one local connector process, an optional local Ollama service, invented
data by default, and one operator. This review records the boundary and the
evidence behind each guarantee. It does not grant production approval.

## Trust boundary

The operator's local file access is trusted. The owner can read or change every
database, marker, and snapshot. Possession of `owner.json` permits requesting
all scopes. Administration and usage commands read local files directly and
bypass MCP authorization. This is not multi-user isolation or production
identity.

Both services bind HTTPS to `127.0.0.1`. Each start generates a fresh P-256 key
and a one-day loopback certificate that the client trusts explicitly. Browser
origins are rejected.

Sources default to invented local fixtures with reserved `.example` email
domains. A real source is used only when the owner selects it.

| Source | Default fixture | Optional real source |
| --- | --- | --- |
| CRM | `crm.sqlite3`: 30 accounts, 30 contacts, 30 opportunities | Read-only Salesforce, `--crm-backend salesforce` |
| Calls | `calls.sqlite3`: 29 call notes | TXT or VTT exports loaded with `calls import` |
| Slack | `slack.sqlite3`: 29 account-channel messages | One mapped thread per account, after `slack connect` and `slack map` |

## Scopes

| Scope | Allowed tools |
| --- | --- |
| `crm:read` | `search_accounts`, `get_account`, `list_opportunities`; with `serve --selection`, also `prepare_renewal_brief` and `read_renewal_brief` |
| `crm:write` | `log_activity` |
| `calls:read` | `read_call_notes` |
| `slack:read` | `read_slack_messages` |

Scopes do not imply one another. The gateway and the handlers both enforce
them. The briefing workflow requests read scopes only and makes three fixed
reads. On `brief`, `verify`, and `evaluate`, `--with-slack` adds a fourth read
and an explicit Slack grant. Record or draft text cannot select a write.

## Guarantees and their tests

All tests use invented data, fake providers, and local HTTPS.

| Guarantee | Tests |
| --- | --- |
| Each scope reaches only its own tools. A missing scope returns HTTP 403. | [test_gateway.py](tests/test_gateway.py), [test_slack_gateway.py](tests/test_slack_gateway.py) |
| Invalid, expired, malformed, or wrong-resource tokens return HTTP 401. | [test_gateway.py](tests/test_gateway.py), [test_auth.py](tests/test_auth.py) |
| Codes are single use, PKCE is S256, and no refresh token is issued. | [test_auth.py](tests/test_auth.py) |
| Each recognizable tool attempt commits one `pending` receipt before execution. | [test_audit.py](tests/test_audit.py), [test_gateway.py](tests/test_gateway.py) |
| An initial audit failure returns HTTP 503 and the tool does not run. A finalization failure returns HTTP 503 and releases no result. | [test_gateway.py](tests/test_gateway.py), [test_slack_gateway.py](tests/test_slack_gateway.py) |
| An identical write retry reuses one activity. A changed payload returns `idempotency_conflict`. | [test_gateway.py](tests/test_gateway.py) |
| The rate limit returns HTTP 429 with `rate_limited` and `Retry-After`. | [test_gateway.py](tests/test_gateway.py), [test_request_checks.py](tests/test_request_checks.py) |
| Source text stays quoted data. An uncited or altered line rejects the brief. | [test_citations.py](tests/test_citations.py), [test_briefing.py](tests/test_briefing.py) |
| Keys rotate per start, the certificate names loopback, and state files are private. | [test_runtime.py](tests/test_runtime.py) |
| Server events carry no bearer token, argument, or source body. | [test_call_events.py](tests/test_call_events.py) |
| Snapshot saves replace atomically. Audit inspection is read-only. | [test_brief_persistence.py](tests/test_brief_persistence.py), [test_audit_reader.py](tests/test_audit_reader.py) |
| `demo` records six receipts, five successful reads and one refusal, quotes both injected instructions as data, and creates no activity. | [test_demo.py](tests/test_demo.py) |

## Local authority and client credentials

One preregistered public client uses S256 PKCE. Authorization needs the owner
credential and an explicit scope list. Codes expire after 60 seconds and permit
one exchange. Opaque tokens expire after five minutes. There are no refresh
tokens and no dynamic registration. A restart invalidates all grants.

`inspector-config` and `connect` write a bearer grant for only the named scopes
to a mode-0600 file in the state directory. The client receives that grant,
never the owner credential, and its calls pass through the same gateway, audit
log, and rate limit. Keep these files out of Git.

## Audit receipts and retention

`audit.sqlite3` stores, for each tool attempt, a SHA-256 token fingerprint, the
tool, its arguments, a UTC timestamp, request and brief IDs, the outcome, and
returned record references. Arguments can include activity text, search terms,
and IDs. Bearer tokens and response bodies are not logged. A missing credential
is recorded as `anonymous`. A fingerprint establishes neither identity nor a
valid grant.

A finalization failure can follow a committed activity. Retry the same
idempotency key and reconcile the pending receipt. Unparseable traffic without
a tool marker and requests disconnected before execution fall outside the
receipt guarantee. The default rate limit is 60 calls per valid token per
60-second window. Scope denials count. A new token or a restart resets it.

Runtime directories use mode 0700 and files use mode 0600. Nothing expires
automatically. Saved briefs include source evidence and receipt copies. See
[docs/audit.md](docs/audit.md).

## Untrusted text and drafting

Notes, messages, transcripts, CRM fields, and draft text are untrusted data. No
record text can request a tool or a grant. The verifier accepts exact supported
claims and references, not paraphrases, and one invalid line rejects the whole
brief. A citation attributes a source. It does not establish source truth.

The fixture drafter runs no model, and default demos and CI use no inference
service. The optional Ollama adapter sends only a supported-claim catalog to a
fixed loopback endpoint, with no credentials, tool interface, proxy, or
redirects. It requires Ollama 0.30.0 or newer, rejects cloud aliases, adds
`:local` to each generation request, and downloads no model
([test_ollama.py](tests/test_ollama.py)).

## Salesforce (optional, read-only)

`--crm-backend salesforce --sf-org ALIAS` on `serve`, `brief`, `accounts`, and
`account` captures an existing Salesforce CLI login inside the connector
process. The
provider invokes only `sf org display` and `sf org auth show-access-token`, with
an explicit alias and `--json`, using argument arrays and no shell. Stdin is
closed; stdout and stderr are captured. CLI telemetry, automatic updates, and
file logging are disabled for these child commands. A command has at most
10 seconds within the original 20-second credential deadline. Errors expose a
fixed code, never the alias, child output, arguments, or token. The connector
stores no Salesforce credential file. The owner's existing CLI login storage
and installed `sf` executable remain trusted.

Without `--sf-org`, the backend reads `SALESFORCE_INSTANCE_URL` and
`SALESFORCE_ACCESS_TOKEN`, and that path cannot refresh. There is no implicit
default CLI org. Both paths validate one HTTPS My Domain origin and a bounded
printable token.

| Area | Behavior |
| --- | --- |
| Environment | Developer Edition is the default. Sandbox and production need an explicit `--sf-environment`. Startup checks OrganizationType and IsSandbox before opening source stores. Sandbox and production also check that the required standard objects and fields are readable. |
| Identity pin | A mode-0600 `crm-connection.json` pins the validated origin, org ID, and environment. A restart must match it. It holds private org identity and must not be shared or committed. |
| Requests | GET only, with fixed SOQL fields, escaped search text, and no arbitrary query or URL input. TLS verification stays on. Proxies and redirects are disabled. |
| Bounds | Each operation has 12 GETs, 1 MiB, and 20 seconds, with at most 10 pages and 100 rows per query. |
| HTTP 401 | The first 401 in an operation may reacquire credentials once through `sf`, read the Organization again to confirm the same org, and retry the same GET within the original budget. A second 401, a lookup failure, or a changed origin or org returns `salesforce_auth_required`. Other errors are not retried. |
| Writes | The write tool is omitted. A write attempt returns HTTP 403. |
| `salesforce check` | A local-owner diagnostic. Its report holds fixed step names, outcomes, counts, timings, fixed errors, and missing required field names. It returns no source record, ID, org detail, or credential. |
| Demo kit | The owner-run helper `scripts/salesforce-demo.py` imports and removes twelve invented records with Salesforce CLI writes. It is separate from the connector, which stays GET-only. Cleanup deletes only the imported IDs and has no transaction, so a failure can leave a partial cleanup. |

The local owner grants CRM access. This is not Salesforce user delegation, and
the upstream token may hold broader permissions than these reads use. No
Salesforce credential enters `owner.json`, a client config, brief evidence, the
audit, or model input.

Tests, all with invented HTTP responses:
[credentials](tests/test_sf_credentials.py) with a fake CLI on PATH,
[adapter](tests/test_salesforce.py), [environments](tests/test_salesforce_environments.py),
[identity pin](tests/test_source_state.py), [gateway](tests/test_salesforce_gateway.py),
[check](tests/test_salesforce_check.py), [demo kit](tests/test_salesforce_demo.py),
and [kit helper](tests/test_salesforce_demo_script.py). See [docs/salesforce.md](docs/salesforce.md)
and [docs/salesforce-demo.md](docs/salesforce-demo.md).

## Imported transcripts

`calls import` is a local owner operation. It parses a selected TXT or VTT
export, obtains only `crm:read`, and resolves the account through the audited
gateway before committing an association. The Salesforce AccountNumber lookup
is exact, escaped, and bounded to two matches. A missing, ambiguous, changed,
or unreceipted association publishes nothing. A missing import refuses instead
of returning demo content.

An import is limited to 2 MiB and 10,000 units. A returned excerpt is limited
to 16 KiB of text, 256 units, and 128 KiB of ASCII-serialized record data.
Tools and briefs expose only selected content. Local `calls inspect --full`
returns the complete original and is not an MCP tool. Tests:
[parser and store](tests/test_calls_import.py), [gateway](tests/test_imported_gateway.py),
and [briefs](tests/test_imported_briefing.py). See [docs/calls.md](docs/calls.md).

## Slack authentication

`slack connect` authorizes an existing PKCE public app in the system browser.
It requests only user history scopes (`channels:history`, optionally
`groups:history`), validates the exact granted set, and verifies the returned
workspace and user.
Authentication does not enable a source or grant a caller `slack:read`. These
commands call no message endpoint and create no tool receipts.

| Area | Behavior |
| --- | --- |
| Browser flow | Fresh state and S256 PKCE. Only the exact registered localhost Host, port, and path are accepted. The code, state, and verifier stay in memory and out of results, progress, and browser HTML. |
| Storage | Tokens and identity sit in one private credential file in a separate folder, pinned to app, workspace, and user. A lock serializes processes. Symlinks, hard links, and unsafe modes are rejected. |
| Credential states | `ready` is released to reads only with its stored grants and expected identity. `refresh_pending` marks an ambiguous exchange, identity mismatch, or rejected credential unusable. `identity_unchecked` allows a check-only retry and is never released to reads. |
| Provider calls | Four fixed Slack methods: exchange, refresh, identity, and explicit revoke. No redirects, no ambient proxy, and bounded response bytes and HTTP time. Status, errors, and browser output omit tokens, provider IDs, and workspace details. |
| Disconnect | Local disconnect makes no request. `--revoke` reports only the access-token outcome and local deletion. It never claims app or full-grant removal. |

Tests: [provider](tests/test_slack_auth.py), [callback](tests/test_slack_callback.py),
[composed HTTP](tests/test_slack_auth_integration.py), and [CLI](tests/test_slack_cli.py).
See [docs/slack-auth.md](docs/slack-auth.md).

## Selected live Slack thread

`slack map` resolves one account through the audited `crm:read` tool and saves
one thread selection without reading messages. A copied thread URL must match
the saved workspace origin. It never supplies an HTTP destination or chooses
credentials. There is no query or channel-search tool, and live Slack has no
fixture fallback.

`read_slack_messages` keeps its `slack:read` check and commits its pending
receipt before mapping, credential, or provider work. The source makes only
fixed GET `conversations.replies` requests with verified TLS, no redirect, and
no environment proxy. The scope permits any mapped account, limited by
`--selection` when set. No tool starts the browser flow, sends a message, or
broadens a grant.

| Bound | Limit |
| --- | --- |
| Shared budget per read, covering pages, authentication, failures, and retries | Ten HTTP attempts, 1 MiB of response bytes, and a 20-second deadline |
| Authentication response | 64 KiB |
| Thread | 100 raw rows, 16 KiB per literal text, 64 KiB combined text, and 256 KiB for the normalized envelope |
| Rate-limit retry | One, with a valid delay of at most five seconds inside the remaining budget |
| Credential recovery | Only exact token expiry permits one retry, with at most one local refresh. An ambiguous refresh is not retried and then requires browser login. |

Missing text, unsupported subtypes, and hidden content refuse the whole
observation. Files, blocks, and attachments add omission counts only. A
completed read does not establish a full archive or an atomic view while
messages change. `slack check` reads the mapped thread through a temporary
local service that enables only the Slack read tool. Its report holds no
identifiers, paths, URLs, or message text.

Tests: [mapping](tests/test_slack_mapping.py), [reader](tests/test_slack_reader.py),
[runtime](tests/test_live_slack_runtime.py), [local HTTP](tests/test_live_slack_http.py),
[briefs](tests/test_live_slack_briefing.py), and [check](tests/test_slack_workflow.py).
See [docs/slack-live.md](docs/slack-live.md).

## Selected sources and caller-run briefs

`serve --selection FILE` limits every client read to the accounts and Slack
threads in an owner-written file. The file must be a regular file the owner
owns that others cannot write. `serve` validates it before opening any
credential or source and never reloads it.

| Check | Behavior |
| --- | --- |
| Unselected account | Refused with `account_not_selected` before any source access. The ID is never looked up. |
| Slack read | Also needs that account's Slack thread selected. |
| Searches and account-wide opportunity lists | The selected IDs go into the SQLite or Salesforce query itself, before ordering and the result limit. |
| Writes | No write tool is offered. A write attempt is refused as `crm_write_disabled`. |

`prepare_renewal_brief` runs under the bearer token the client presented,
never the owner credential. Each read inside it commits its own pending receipt
with the caller's credential fingerprint, verifies the token again, counts one
attempt against the caller's rate window, and checks its own scope and the
selection. A missing calls or Slack scope gives the existing denial disclosure.
Any other failure, including an audit write failure, an expired token, a rate
limit, or a 45-second brief deadline, leaves no readable brief.

A saved brief counts only if its own outer call finished successfully and that
receipt names the SHA-256 digest of the saved file. A brief ID alone grants
nothing. `read_renewal_brief` makes no source system call and reports that it
did not recheck the source systems. It releases the brief only while the
account and any Slack thread it contains are still selected, the caller's
current grant has every scope the brief's content needs, and the source
bindings, transcript revision, and Slack mapping are unchanged. For live Slack
it also requires a ready local credential for the same identity with at least
the scopes of each credential that returned a page of the brief's thread. That
decision is made while holding the Slack connection lock. A disconnect that
finishes after the decision does not recall the response.

Tests: [selection](tests/test_selected_sources.py) and
[live Slack selection](tests/test_selected_live_slack.py). See
[docs/selection.md](docs/selection.md).

## Known limits

- The local owner is trusted. An owner who rewrites snapshots and receipts can
  alter all evidence. Local records are neither tamper-proof nor billing data.
- Other processes with the owner's authority are not isolated. Anyone who can
  read the Slack credential file can use the saved grant.
- Retention is manual. There is no power-loss durability guarantee and no
  cross-file transaction.
- The rate limit is not a distributed quota.
- The verifier has a fixed vocabulary. Its conflict rules match exact fixture
  records and do not detect arbitrary contradictions.
- The selection chooses accounts and Slack threads, not fields. Without
  `--selection`, a connected server exposes its whole source to any `crm:read`
  grant.
- The final grant check runs when a tool finishes. A grant that expires before
  the receipt is written can still receive that one response.
- Credential filesystem proof is POSIX only.

## Not verified

No live org has been verified.

- Sandbox and production Salesforce behavior has only invented-response proof.
- One development `slack check` passed on November 18, 2026 against the
  owner's test workspace: one mapped message, one API call, all five steps. An
  agent ran it with the owner's permission. It is one observed run, not an
  owner verification report. No app rendering or hosted assistant connection
  has been verified.
- No live call provider or real transcript was accessed.
- Only the Inspector CLI with a supplied grant is tested. The Inspector UI,
  interactive app login, Claude Code, and Cursor are untested.
- The recorded Ollama run used an earlier fixture corpus. It proves no model
  result for the current book.
- One owner-authorized demo-kit run on November 17, 2026 imported and then
  deleted twelve invented records. It is one observed run, not an owner
  verification report.

A reviewer must inspect the scope-denial and invalid-token tests, the audit
failure and committed-write retry tests, the injection and citation-rejection
tests, and the 20-account evaluation with its fixed expected claims, and accept
the limits above explicitly. Real data or shared deployment requires a separate
review. A passing test run is not approval to read production or client data.
