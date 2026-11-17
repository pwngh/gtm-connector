# GTM connector demo

One MCP connector over two local source systems: a CRM and call notes, each
with separate permissions. Portfolio project for Preston Neal. The 30 accounts
and all related records use synthetic labels and reserved `.example` addresses.

This is PR 3 of four. Alongside the scoped tools, authorization, audit, rate
limits, and retry-safe writes, it now drafts a renewal-call brief and checks its
citations before saving or printing it. Drafting uses a deterministic extractive fixture,
not an LLM. Evaluation, usage reporting, and security review remain in PR 4.

## Run

Requires macOS or Linux, Python 3.11+ with SQLite and venv support, and internet
access for the first dependency install. No database service, account, model,
or API key is needed. From a clean checkout:

```sh
./scripts/demo.sh
```

This creates `.venv`, installs `requirements.lock`, runs the tests, and starts
two temporary HTTPS loopback services. The CLI obtains scoped tokens with PKCE,
then exercises all five tools and the briefing workflow over the network. Both
services stop afterward; state remains in the gitignored `var/v2` directory.
Use another directory with `./scripts/demo.sh --state-dir /tmp/gtm-fixture`.

The final JSON retains the connector demonstration: 30 accounts, notes denied
with HTTP 403, an empty result, conflicting sources, two writes producing one
activity, and a clear HTTP 429. It adds three briefs: full access for `acc-003`,
CRM-only access for `acc-001`, and the injected note for `acc-007`. There are
14 connector attempts plus nine briefing reads, producing 23 audit receipts.
The briefing workflow adds no activities, including when the note requests one.

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

Accepted briefs and their evidence snapshots are saved in
`var/v2/briefs/<brief_id>.json`. Copy the generated brief ID to inspect its three
receipts or check a candidate text file against that same snapshot:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
.venv/bin/python -m gtm_connector check-brief brief-REPLACE_WITH_ID candidate.txt
```

Candidate files contain only the brief's cited bullet lines, without the CLI's
`Brief:` and `As of:` display labels. The checker requires exact supported
content and record references. It rejects uncited sentences, invented values,
wrong citations, appended claims, and action-shaped output. It is a controlled
vocabulary checker, not a general paraphrase or semantic-entailment judge. A
supported subset may pass: grounding and coverage are separate. An unavailable
or empty notes result must remain visible.

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
the same demo on Python 3.11 and 3.14. Test data is temporary.

Evaluation scores: not measured yet. PR 4 will report facts found, facts cited
correctly, and facts invented across 20 accounts, plus model tokens per brief.
See [DECISIONS.md](DECISIONS.md) for design choices and [AGENTS.md](AGENTS.md)
for the four-PR plan and review rules. MIT licensed.
