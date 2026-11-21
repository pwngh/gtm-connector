# Connector security review

This review covers one local connector process and one operator. No model takes part.
It records the boundary and the evidence behind each guarantee. It is not production approval.
Real data or shared deployment requires a separate review.

## Trust boundary

The operator's local file access is trusted. `owner.json` holds the owner credential, and
possession of it permits requesting all scopes. Administration commands read local files directly
and bypass MCP authorization. This is not multi-user isolation or production identity.

The connector runs two local services, the authorization server and the MCP gateway. Both bind
HTTPS to `127.0.0.1`. Each start generates a fresh P-256 key and a one-day loopback certificate
that the client trusts explicitly. Browser origins are rejected. A state folder is the private
folder that `--state-dir` selects, and a new one has no connected source. Daily reads and
`debug serve` require a connected CRM, `demo` uses invented data, and live sources are opt-in.

## Scopes

A scope is the access a tool requires. A grant is a bearer token issued for a list of scopes.

| Scope | Allowed tools |
| --- | --- |
| `crm:read` | `search_accounts`, `get_account`, `list_opportunities`; with `debug serve --selection`, also `prepare_renewal_brief` and `read_renewal_brief` |
| `crm:write` | `log_activity` |
| `calls:read` | `read_call_notes` |
| `slack:read` | `read_slack_messages` |

Scopes do not imply one another. The gateway and the handlers both enforce them. Record text is
untrusted data and cannot request a tool, a grant, or a write.

## Guarantees and their tests

| Guarantee | Tests |
| --- | --- |
| Each scope reaches only its own tools. A missing scope returns HTTP 403. | [test_gateway.py](tests/test_gateway.py), [test_slack_gateway.py](tests/test_slack_gateway.py) |
| Invalid, expired, malformed, or wrong-resource tokens return HTTP 401. | [test_gateway.py](tests/test_gateway.py), [test_auth.py](tests/test_auth.py) |
| Codes are single use, PKCE is S256, and no refresh token is issued. | [test_auth.py](tests/test_auth.py) |
| Each recognizable tool attempt commits one `pending` receipt before execution. | [test_audit.py](tests/test_audit.py), [test_gateway.py](tests/test_gateway.py) |
| An initial audit failure returns HTTP 503 and the tool does not run. A finalization failure returns HTTP 503 and releases no result. | [test_gateway.py](tests/test_gateway.py), [test_slack_gateway.py](tests/test_slack_gateway.py) |
| An identical write retry reuses one activity. A changed payload returns `idempotency_conflict`. | [test_gateway.py](tests/test_gateway.py) |
| The rate limit returns HTTP 429 with `rate_limited` and `Retry-After`. | [test_gateway.py](tests/test_gateway.py), [test_request_checks.py](tests/test_request_checks.py) |
| The connector writes every character of a card. Each value and quotation carries a number that resolves to a stored record and the receipt of the read that returned it. | [test_units.py](tests/test_units.py), [test_cases.py](tests/test_cases.py) |
| Evidence with a record of another account, a missing or mismatched receipt, a receipt with no valid start time, a changed transcript or Slack snapshot, or an unknown shape gives no card. | [test_units.py](tests/test_units.py), [test_imported_briefing.py](tests/test_imported_briefing.py), [test_live_slack_briefing.py](tests/test_live_slack_briefing.py) |
| Source text is neutralized before anything displays it, in the card and in printed JSON: Slack mentions, links, control characters, and bracketed numbers become inert words. The raw records stay in the saved file. A quotation is one whole sentence, and a figure inside it never becomes a record value. | [test_units.py](tests/test_units.py), [test_cases.py](tests/test_cases.py), [test_briefing.py](tests/test_briefing.py), [test_cli.py](tests/test_cli.py) |
| A speaker's role comes from a label, never from wording: a seller the operator listed, or a call label equal to exactly one contact of the account. Anything else is unknown, and a Slack author is never a customer. | [test_units.py](tests/test_units.py), [test_cases.py](tests/test_cases.py) |
| Keys rotate per start, the certificate names loopback, and state files are private. | [test_runtime.py](tests/test_runtime.py) |
| Server events carry no bearer token, argument, or source body. | [test_call_events.py](tests/test_call_events.py) |
| A saved card's file is replaced atomically. Audit inspection is read-only. | [test_brief_persistence.py](tests/test_brief_persistence.py), [test_audit_reader.py](tests/test_audit_reader.py) |
| `demo` records seven receipts, five successful reads, one refusal and the seal of the saved card (its `save_brief` receipt), and creates no activity. | [test_demo.py](tests/test_demo.py) |

## Local authority, audit receipts, cards, and transcripts

| Area | Behavior |
| --- | --- |
| Local authority | Authorization needs the owner credential and an explicit scope list, for one preregistered public client. [docs/tools.md](docs/tools.md) has the code and token lifetimes. |
| Client grants | With `--scope`, `connect` writes an HTTP client's bearer grant for only the named scopes to a mode-0600 file in the state folder; a stdio entry holds no grant. The client receives that grant, never the owner credential, and its calls pass through the same gateway, audit log, and rate limit. Keep these files out of Git. |
| Audit receipts | `audit.sqlite3` stores, for each tool attempt, a SHA-256 token fingerprint, the tool, its arguments, a UTC timestamp, request and brief IDs, the outcome, and returned record references. Saving a card adds one more receipt, `save_brief`, that holds the saved file's SHA-256. Arguments can include activity text, search terms, and IDs. Bearer tokens and response bodies are not logged. A fingerprint establishes neither identity nor a valid grant. [docs/audit.md](docs/audit.md) has the HTTP 503 cases, the requests that get no receipt, and the rate limit. |
| Retention | Runtime directories use mode 0700 and files use mode 0600. Nothing expires automatically. A brief is a saved card with its evidence: saved briefs include source evidence and receipt copies. |
| Cards | `units.py` builds a card from checked evidence alone. No model runs and no inference service is called. A seller is whoever the operator lists with `connect ... --seller NAME`, saved as `sellers.json`, and that list is the operator's own claim. Each demo source adds the demo's invented sellers; a live source uses the operator's list. See [docs/briefs.md](docs/briefs.md). |
| Imported transcripts | `debug calls import` is a local operator command. It parses a selected TXT, VTT, or SRT export, obtains only `crm:read`, and resolves the account through the audited gateway before committing an association. A missing, ambiguous, changed, or unreceipted association publishes nothing. An account with nothing imported gets a card that says so, and demo content never stands in. Local `debug calls inspect --full` returns the complete original and is not an MCP tool. See [docs/calls.md](docs/calls.md). |

## Salesforce (optional, read-only)

`connect salesforce --org ALIAS` saves the alias and environment in the state folder's
`profile.json`, which holds no credential, org ID, or address. Each command in that folder then
captures the existing Salesforce CLI login of that alias inside the connector process. The provider invokes only `sf org display` and `sf org auth show-access-token`,
with an explicit alias and `--json`, using argument arrays and no shell. Stdin and stderr go to
the null device, and the connector captures stdout. CLI telemetry, automatic updates, and file logging are disabled for these
child commands. Errors expose a fixed code, never the alias, child output, arguments, or token.
A command has at most 120 seconds, and both share a 180-second login deadline that is separate from
each read's budget. An `sf` that outlasts its limit is stopped and reported as
`salesforce_cli_timeout`.

| Area | Behavior |
| --- | --- |
| Authority and storage | The local owner credential grants CRM access. This is not Salesforce user delegation, and the upstream token may hold broader permissions than these reads use. The connector stores no Salesforce credential file. The operator's existing CLI login storage and installed `sf` executable remain trusted. No Salesforce credential enters `owner.json`, a client config, brief evidence, or the audit log. |
| `debug salesforce check` without `--sf-org` | The check reads `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN`, and that path cannot refresh. No other command reads them. There is no implicit default CLI org. Both paths validate one HTTPS My Domain origin and a bounded printable token. |
| Environment and identity pin | Developer Edition is the default. Sandbox and production need an explicit `--environment` at `connect salesforce`. `connect` and every later startup check OrganizationType and IsSandbox before opening source stores. Sandbox and production also check that the required standard objects and fields are readable. A mode-0600 `crm-connection.json` pins the validated origin, org ID, and environment. A restart must match it. It holds private org identity and must not be shared or committed. |
| Requests | GET only, with fixed SOQL fields, escaped search text, and no arbitrary query or URL input. TLS verification stays on. Proxies and redirects are disabled. Each operation has 12 GETs, 1 MiB, and 20 seconds, with at most 10 pages per query. One account's contacts, opportunities, and contracts are read up to 1,000 rows each, and a list across accounts up to 100. An account search returns its first 100 rows by name and then ID and states when more match. The write tool is omitted. A write attempt returns HTTP 403. |
| HTTP 401 | The first 401 in an operation may reacquire credentials once through `sf`, read the Organization again to confirm the same org, and retry the same GET. Time spent waiting for `sf` is added back to the read's deadline. An `sf` that does not answer in time returns `salesforce_cli_timeout`. A second 401, a lookup failure, or a changed origin or org returns `salesforce_auth_required`. Other errors are not retried. |
| `debug salesforce check` | A local diagnostic the operator runs. Its report holds fixed step names, outcomes, counts, timings, fixed errors, and missing required field names. It returns no source record, ID, org detail, or credential. |
| Tests and guide | [test_sf_credentials.py](tests/test_sf_credentials.py), with a fake CLI on PATH. [docs/salesforce.md](docs/salesforce.md). |

## Slack (optional, read-only)

| Area | Behavior |
| --- | --- |
| Sign-in | `debug slack connect` authorizes an existing PKCE public app in the system browser. It requests only user history scopes (`channels:history`, optionally `groups:history`), validates the exact granted set, and verifies the returned workspace and user. Authentication does not enable a source or grant a caller `slack:read`. `connect slack` enables the source for a state folder, and only where a sign-in is saved. The authentication commands call no message endpoint and create no tool receipts. |
| Browser flow | Fresh state and S256 PKCE. Only the exact registered localhost Host, port, and path are accepted. The code, state, and verifier stay in memory and out of results, progress, and browser HTML. |
| Storage | Tokens and identity sit in one private credential file in its own folder, `slack-connection` inside the state folder, pinned to app, workspace, and user. `profile.json` holds none of them. A lock serializes processes. Symlinks, hard links, and unsafe modes are rejected. |
| Credential states | `ready` is released to reads only with its stored grants and expected identity. `refresh_pending` marks an ambiguous exchange, identity mismatch, or rejected credential unusable. `identity_unchecked` allows a check-only retry and is never released to reads. |
| Provider calls | Four fixed Slack methods: exchange, refresh, identity, and explicit revoke. No redirects, no ambient proxy, and bounded response bytes and HTTP time. Status, errors, and browser output omit tokens, provider IDs, and workspace details. |
| Disconnect | Local disconnect makes no request. `--revoke` reports only the access-token outcome and local deletion. It never claims app or full-grant removal. |
| `debug slack map` | Resolves one account through the audited `crm:read` tool and saves one thread mapping without reading messages. A copied thread URL must match the saved workspace origin. It never supplies an HTTP destination or chooses credentials. |
| `read_slack_messages` | Keeps its `slack:read` check and commits its pending receipt before mapping, credential, or provider work. The source makes only fixed GET `conversations.replies` requests with verified TLS, no redirect, and no environment proxy. The scope permits any mapped account, limited by `--selection` when set. There is no query or channel-search tool, and live Slack has no fixture fallback. No tool starts the browser flow, sends a message, or broadens a grant. |
| Read budget | Ten HTTP attempts, 1 MiB of response bytes, and a 20-second deadline per read, covering pages, authentication, failures, and retries. One rate-limit retry, with a valid delay of at most five seconds. Only exact token expiry permits one retry, with at most one local refresh. An ambiguous refresh is not retried and then requires browser login. |
| Content | Missing text, unsupported subtypes, and hidden content refuse the whole observation. Files, blocks, and attachments add omission counts only. A completed read does not establish a full archive or an atomic view while messages change. |
| `debug slack check` | Reads the mapped thread through a temporary local service that enables only the Slack read tool. Its report holds no identifiers, paths, URLs, or message text. |
| Tests and guides | [test_slack_auth.py](tests/test_slack_auth.py) and [test_slack_reader.py](tests/test_slack_reader.py). [docs/slack.md](docs/slack.md). |

## Selected sources and prepared briefs

A selection names the accounts and Slack threads a client may read, in a file the operator writes.
A prepared brief is one a client makes with `prepare_renewal_brief`.

| Area | Behavior |
| --- | --- |
| Selection file | `debug serve --selection FILE` limits every client read to the accounts and Slack threads in a file the operator writes. It must be a regular file the operator owns that others cannot write. `debug serve` validates it before opening any credential or source and never reloads it. |
| Unselected account | Refused with `account_not_selected` before any source access. The ID is never looked up. A Slack read also needs that account's Slack thread selected. |
| Searches and account-wide opportunity lists | The selected IDs go into the SQLite or Salesforce query itself, before ordering and the result limit. |
| Writes | No write tool is offered. A write attempt is refused as `crm_write_disabled`. |
| `prepare_renewal_brief` | Runs under the bearer token the client presented, never the owner credential. Each read inside it commits its own pending receipt with the caller's credential fingerprint, verifies the token again, counts one attempt against the caller's rate window, and checks its own scope and the selection. A missing calls or Slack scope makes the card say that source was not read. Any other failure, including an audit write failure, an expired token, a rate limit, or a 45-second brief deadline, leaves no readable brief. |
| `read_renewal_brief` | A saved brief counts only if its own outer call finished successfully and that receipt names the SHA-256 digest of the saved file. A brief ID alone grants nothing. The tool makes no source system call and reports that it did not recheck the source systems. It releases the brief only while the account and any Slack thread it contains are still selected, the caller's current grant has every scope the brief's content needs, and the source bindings, transcript revision, and Slack mapping are unchanged. For live Slack it also requires a ready local credential for the same identity with at least the scopes of each credential that returned a page of the brief's thread. That decision is made while holding the Slack connection lock. A disconnect that finishes after the decision does not recall the response. |
| Tests and guide | [test_selected_sources.py](tests/test_selected_sources.py). [docs/selection.md](docs/selection.md). |

## Known limits

- An operator who rewrites snapshots and receipts can alter all evidence. Local records are neither
  tamper-proof nor billing data.
- Other processes with the operator's authority are not isolated. Anyone who can read the Slack
  credential file can use the saved grant.
- Retention is manual. There is no power-loss durability guarantee and no cross-file transaction.
- The rate limit is not a distributed quota. Credential filesystem proof is POSIX only.
- A card does not relate one record to another, so it flags no contradiction. A number attributes
  a source, not its truth.
- `inspect` refuses a saved card unless a finished receipt in the audit log names the SHA-256 of the
  saved file. The owner of the state folder can rewrite the file and the log together, so this
  catches a changed card, not a determined owner.
- A speaker label is taken as the source gives it. The connector cannot tell that a transcript
  mislabelled a speaker. In a transcript imported with `--speaker-names`, the label is the name
  that opens a line: whoever can start a line of that file can write a contact's name there, and a
  wrapped line that opens like a name reads as one. Sentences are split at punctuation, which can
  join or divide them wrongly.
- The selection chooses accounts and Slack threads, not fields. Without `--selection`, a connected
  server exposes its whole source to any `crm:read` grant.
- A call is authorized before it runs. A grant that expires while a tool runs still receives that
  one response. The two brief tools check the grant again before they return.

## Not verified

No live org has been verified. All tests use invented data, fake providers, and local HTTPS.

- Sandbox and production Salesforce behavior has only invented-response proof.
- No Slack app rendering or hosted assistant connection has been verified.
- No live call provider or real transcript was accessed.
- Only the Inspector CLI with a supplied grant is tested. The Inspector UI, interactive app login,
  Claude Code, and Cursor are untested.
- The cards were tried on invented deals only. Nothing here shows that a card is useful to a seller.
