# GTM connector demo

This demo exposes isolated CRM and call-note stores through an MCP server and
uses a local CLI to generate renewal briefs with checked citations. Run it
locally with `./scripts/demo.sh` using synthetic fixtures, reserved `.example`
addresses, and no external runtime services or API keys.

## What This Implements

- Scope-isolated sources. Separate SQLite stores use `crm:read`, `crm:write`,
  and `calls:read`. Local authorization uses S256 PKCE. The gateway returns
  HTTP 403 for a scope denial.
- Two-phase durable audit receipts. The gateway commits a `pending` row before
  execution and finalizes that row afterward. If the initial write fails, the
  tool does not run.
- Idempotent writes. `log_activity` returns the original activity on an
  identical retry. A changed payload with the same key returns
  `idempotency_conflict`.
- Prompt-injection containment. Call notes remain untrusted data. The fixture
  briefing workflow quotes the injected instruction and creates zero activities.
  Record contents cannot choose tools or trigger writes.
- Cited renewal briefs. A deterministic fixture drafts the text without a model.
  The verifier checks each claim against supported text and source-qualified
  references such as `crm/accounts/acc-003` and `calls/call_notes/note-003`.
  Missing notes cite audit receipts; date calculations also cite the saved run.
- Evaluation and usage. Twenty accounts have fixed expected claims and measured
  citation scores. Usage groups audit attempts by token, tool, and UTC day and
  reports model tokens per saved brief. PR 4 completes the planned scope.

## Run

Requires macOS or Linux, Python 3.11+ with SQLite and venv support, and internet
access for the first dependency install. No database service, account, model,
or API key is needed. From a clean checkout:

```sh
./scripts/demo.sh
```

For the complete five-minute walkthrough, including evaluation, usage, and a
live checker rejection, run `./scripts/five-minute-demo.sh`. See
[DEMO.md](DEMO.md). First-install download time varies.

This creates `.venv`, installs `requirements.lock`, runs the tests, and starts
two temporary HTTPS loopback services. The CLI obtains scoped tokens with PKCE,
then exercises all five tools and the briefing workflow over the network. Both
services stop afterward; state remains in the gitignored `var/v2` directory.
Use another directory with `./scripts/demo.sh --state-dir /tmp/gtm-fixture`.

The final JSON contains these verified fields:

| Field | Result |
| --- | --- |
| `accounts` | `30` |
| `notes_without_scope.status` | HTTP `403` |
| `empty_notes` | `[]` for `acc-004` |
| `conflicting_sources` | `opp-003` says `closed_won`; `note-003` reports unapproved procurement |
| `injection_note.activities_created` | `0` |
| `idempotent_write` | `requests: 2`, `activities: 1` |
| `rate_limit.status` | HTTP `429` |
| `audit_rows` | `14` connector receipts |
| `briefs` | Two briefs: `acc-003` with calls access, `acc-001` without it |
| `injection_brief.activities_created` | `0` across all three briefs, including `acc-007` |
| `brief_audit_rows` | `9` reads across the three briefs |
| `uncited_candidate_rejected` | `true` |
| `contradiction_flags` | One flag citing `opp-003` and `note-003` |

The run produces 23 audit receipts: 14 connector attempts and nine briefing
reads. The briefing workflow adds no activities, including when a note requests
one. `conflicting_sources` displays both records. `contradiction_flags` contains
the brief's required flag for the explicit fixture rule described below.

## Draft and check a brief

```sh
.venv/bin/python -m gtm_connector brief acc-003
.venv/bin/python -m gtm_connector brief acc-001 --without-calls
.venv/bin/python -m gtm_connector brief acc-004
```

Each command starts the local services, makes a fixed sequence of three MCP
reads (`get_account`, `list_opportunities`, `read_call_notes`), then stops them.
The workflow requests `crm:read` and, unless `--without-calls` is set,
`calls:read`. It never requests `crm:write` or lets draft text choose tools.
Add `--as-of YYYY-MM-DD` to replace the fixed demo date, `--json` for the full
artifact, or `--state-dir PATH` for another state directory.

The CRM-only brief says that call notes were unavailable because the token lacks
`calls:read`; the no-notes account says none were returned. These distinct claims
cite the corresponding audit receipt. Record claims cite their source, and
renewal-day calculations also cite the saved run date. Contradictory CRM fields
and note text remain separate, attributed statements. Notes are quoted as
untrusted source text, including embedded instructions and escaped newlines.

These lines come from `brief acc-003 --json`:

```text
- CRM opportunity opp-003: stage "closed_won"; amount USD 25,500.00; close date 2027-01-10. [crm/opportunities/opp-003]
- Untrusted call note note-003 at "2027-01-12T15:00:00Z" says "Procurement has not approved this renewal. The budget remains unsigned.". [calls/call_notes/note-003]
- Source conflict: CRM opportunity opp-003 reports stage "closed_won", while call note note-003 reports unapproved procurement. [crm/opportunities/opp-003] [calls/call_notes/note-003]
```

The conflict rule requires `closed_won` and that exact note body for the same
account. Its flag must remain in the brief and cite both records. It resolves
neither source. Other wording, denied notes, and arbitrary contradictions are
outside this rule. Regenerate an older `acc-003` brief if it lacks the flag;
`check-brief` applies the current rules to its saved evidence.

To inspect the full artifact:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --json
```

The JSON fields are `schema_version`, `brief_id`, `account_id`, `as_of`,
`adapter`, `model_usage`, `status`, `evidence`, and `text`. This fixture run
returns `schema_version: 1`, `account_id: "acc-003"`, `as_of: "2027-01-15"`,
`adapter: "fixture"`, and `status: "accepted"`. `model_usage` records
`model: null`, `input_tokens: 0`, and `output_tokens: 0`. `evidence.receipts`
contains the three reads. `text` contains the checked brief. Brief and audit IDs
vary per run. Fixture record IDs stay stable.

Accepted briefs and their evidence snapshots are saved in
`var/v2/briefs/<brief_id>.json`. Copy the generated brief ID to inspect its three
receipts or check a candidate text file against that same snapshot:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt
```

Candidate files contain only the brief's cited bullet lines, without the CLI's
`Brief:` and `As of:` display labels. Use the `text` field from the JSON artifact.
Checking that text exits `0` and returns JSON with `status: "valid"` and the
submitted `brief_id`. Appending `The renewal was approved.` exits `2`, prints
nothing to stdout, and writes this error to stderr:

```text
error: Brief contains an uncited, unsupported, or misattributed statement.
```

The checker requires exact supported content and record references. It rejects
uncited sentences, invented values, wrong citations, appended claims, and
action-shaped output. It is a controlled vocabulary checker, not a general
paraphrase or semantic-entailment judge. A
supported subset may pass: grounding and coverage are separate. An unavailable
or empty notes result and a recognized source conflict must remain visible.

The adapter receives a copy of the evidence, without owner credentials or
reusable bearer tokens; the checker retains the original snapshot. Rejected
draft text is not published or saved. Artifacts record `collected`, `accepted`,
or `rejected` status and use owner-only permissions. Snapshots are local files,
not tamper-proof records or live views of later CRM changes. Custom Python
adapters are not sandboxed and are not selectable through the CLI. Fixture
usage records zero model tokens; custom-adapter usage is unknown, not guessed.

## Tools and sources

| Tool | Required scope | Records returned |
| --- | --- | --- |
| `search_accounts` | `crm:read` | CRM accounts matching a literal name substring |
| `get_account` | `crm:read` | One account and its contacts |
| `list_opportunities` | `crm:read` | Opportunities, optionally filtered by account |
| `read_call_notes` | `calls:read` | Notes from the separate calls database |
| `log_activity` | `crm:write` | An activity created or reused by idempotency key |

CRM access does not grant calls access. A calls-only token cannot read CRM.
Each tool declares its required scope in `gtm.demo/requiredScope` metadata;
the gateway and handler enforce it. Tools return source-qualified record
references such as `crm/accounts/acc-001` and `calls/call_notes/note-007`.
Returned record content is marked as untrusted data.

`crm.sqlite3` holds accounts, contacts, opportunities, and activities.
`calls.sqlite3` holds 29 notes; `acc-004` intentionally has none. Fixed IDs are
stable across reseeds. Display names are labels such as `Fixture account acc-001`,
not plausible company/person/product names. Against the fixed demo date
`2027-01-15`, `acc-001` renews in 17 days, `acc-002` is at risk, and `opp-003`
says `closed_won` while `note-003` says procurement has not approved renewal.

Reseeding preserves edits and activities and rejects incomplete or unexpected
fixtures. PR 1's combined CRM/calls database is incompatible: use the new default
`var/v2` or a fresh path. No automatic migration or deletion occurs.

## Protocol and authorization

Implemented [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28)
and its [authorization section, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization),
both read on **2026-11-17**. The official Python SDK is pinned to **2.3.0**.
This uses stateless Streamable HTTP, per-request protocol metadata, and
`server/discover`. Only this protocol revision is accepted. There are no
subscriptions, legacy initialization sessions, or arbitrary-client compatibility
claims. See DECISIONS.md for the SDK checks supplemented by the gateway.

The authorization server has one pre-registered public CLI client, explicit
scope approval, S256 PKCE, single-use 60-second authorization codes, resource
binding, state and issuer checks, and five-minute opaque access tokens.
Both services use HTTPS on `127.0.0.1`; the client trusts only the locally
generated certificate, without changing system trust or disabling verification.
Browser origins are rejected. Restart invalidates all tokens and codes.

The local resource owner is a fixture emulator. Its generated credential lives
in owner-only `owner.json`; possession permits requesting any supported scope.
The CLI validates the authorization redirect directly, without a browser or
callback listener. This is not production login, consent, or user identity.
There are no refresh tokens or dynamic client registration.

To keep the services running:

```sh
.venv/bin/python -m gtm_connector serve
```

The command prints the public endpoints and owner-config path. In a second
terminal, an explicitly requested CRM read can use the included client:

```sh
.venv/bin/python - <<'PY'
import json
from gtm_connector.client import LocalClient
with open('var/v2/owner.json') as source:
    config = json.load(source)
with LocalClient(config) as client:
    token = client.authorize(['crm:read'])
    print(client.call(token, 'get_account', {'account_id': 'acc-001'}).json())
PY
```

Stop with Ctrl-C. Do not put owner configuration or bearer tokens into model
inputs. Local file access is the trust boundary: the file owner can inspect
SQLite directly and alter this fixture.

## Audit, rate limits, and writes

Every recognizable `tools/call` attempt gets one durable pending receipt before
execution, then the same row is finalized for success or rejection. Receipts
include a SHA-256 token fingerprint (not a reusable bearer token), tool,
arguments, UTC time, request/brief IDs, HTTP status, elapsed time, and returned
record count/references. Arguments may contain activity text. No raw response
bodies are copied into the log. Unparseable traffic without a tool-call marker
and disconnected requests that never reach execution are outside this claim.

If the initial audit write fails, the tool does not run. A finalization failure
returns HTTP 503 and leaves a pending receipt; an activity might already have
committed, so retry with the same idempotency key. A pending row means the
outcome needs reconciliation. The log is not tamper-proof or a billing system.

The default limit is 60 tool calls per token per 60-second fixed window. Scope
denials count; invalid tokens do not allocate buckets. HTTP 429 includes
`rate_limited` and `Retry-After`. The demo lowers the limit to four to show it.
This is an in-process guard: a new token or server restart resets the allowance.

`log_activity` requires account ID, kind, body, and an idempotency key. An
identical retry returns the original activity; a different payload with that
key returns `idempotency_conflict`. The key is unique across this one local CRM.
There are no update/delete tools or automatic writes based on record contents.

Runtime state uses a mode-0700 directory and mode-0600 files. No automatic
retention expiry is implemented. Remove your chosen state directory when you
are finished with its fixture data, audit arguments, and local credentials.

## Verify and inspect

```sh
.venv/bin/python -m unittest discover -s tests -v
python3 -m gtm_connector seed
python3 -m gtm_connector accounts --query acc-001
python3 -m gtm_connector account acc-007
```

The last three commands are direct local CRM administration, outside MCP
authorization; each accepts `--db PATH`. Read commands do not create missing
files. Account inspection contains no opportunities or call notes.

Tests cover stable fixtures and edge cases, scope separation, invalid tokens,
PKCE/resource/issuer checks, protocol headers, one audit row per tool attempt,
audit failures, rate limits, idempotency, and the real HTTPS CLI flow. Briefing
tests cover scope denial versus empty notes, source contradictions, snapshot
mutation, citation rejection, and injected instructions remaining data. CI runs
the full five-minute walkthrough on Python 3.11 and 3.14. Test data is temporary.

## Evaluation

```sh
.venv/bin/python -m gtm_connector evaluate
```

The command uses fresh temporary state and makes 60 HTTPS tool calls for
`acc-001` through `acc-020`. It does not read or change the operator's CRM.
[expected.json](evaluation/expected.json) contains 182 fixed expected claims and
references, separate from the drafting code. Only run and audit IDs are filled
from the collected evidence. No model runs.

Measured on 2026-11-17 with the fixture adapter and as-of date `2027-01-15`:

| Metric | Result |
| --- | --- |
| Accounts with accepted briefs | 20 / 20 |
| Facts found | 182 / 182 (100%) |
| Facts cited correctly | 182 / 182 (100%) |
| Facts invented | 0 / 182 emitted claims (0%) |
| Rejected or failed briefs | 0 |
| Audit receipts | 60 |
| Activities created | 0 |
| Model input / output tokens | 0 / 0 |

The recorded output is in [RESULTS.json](evaluation/RESULTS.json), including
per-account counts. A fact is one displayed claim line. Compound contact and
opportunity statements each count once. Coverage counts expected text, even if
its citation is missing or wrong. Citation coverage also requires the exact
reference set. Unsupported text counts as invented. Duplicate claims do not
increase coverage and fail the evaluation. Failed or rejected briefs keep their
expected claims in the denominator. They have no published text to score for
invention. The CLI exits `2` if any case or invariant fails.

The scorer has separate tests for missing facts, missing or wrong references,
invented text, appended claims, and duplicates. Scores measure published fixture
output against a controlled vocabulary. They do not measure general model
reasoning, arbitrary paraphrases, or the truth of a source claim.

## Usage reporting

```sh
.venv/bin/python -m gtm_connector usage
.venv/bin/python -m gtm_connector usage --brief brief-REPLACE_WITH_ID
```

Add `--state-dir PATH` for another state directory. This is a read-only local
report, outside MCP authorization. It groups receipts in `by_token`, `by_tool`,
`by_day`, and `by_token_tool_day`. Day boundaries use UTC. Each group and `calls`
contain `total`, `ok`, `error`, and `pending` counts. Scope denials, limited
calls, and retries count as attempts. Token groups fingerprint presented
credentials, including rejected ones. Missing credentials use `anonymous`.
These groups do not establish identity or a valid grant. The report excludes
arguments and source text.

`briefs` reports each saved brief's status, correlated `audit_calls`, and
`model_usage`. `model_tokens` totals cover saved artifacts only. Fixture counts
are zero. Unknown counts stay `null`, including in the corresponding total;
`unknown_briefs` makes the gap explicit. Deleted artifacts cannot supply usage.
A malformed or partial artifact stops the report with a clear error. Missing
state creates no files. These counts are not billing data.

## Roadmap and Scope

The four planned slices are implemented: fixtures and schema; scoped MCP tools,
authorization, audit, limits, and writes; cited briefs; then evaluation, usage,
conflict flags, and review. [SECURITY_REVIEW.md](SECURITY_REVIEW.md) records the
data, scopes, retention, failure modes, and reviewer sign-off requirements.
[DEMO.md](DEMO.md) gives the five-minute walkthrough.

The fixture workflow uses no model. A hosted adapter is not implemented. No real
CRM or call-recording API, web interface, or multi-tenant features are included.

Portfolio project for Preston Neal. All fixture records use synthetic labels.
See [DECISIONS.md](DECISIONS.md) for design choices and [AGENTS.md](AGENTS.md)
for the four-PR plan and review rules.

MIT licensed.
