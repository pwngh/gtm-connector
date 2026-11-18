# Read an existing Salesforce org

The optional Salesforce backend reads standard Account, Contact, Opportunity,
and Contract records through REST v68.0. It can produce a cited renewal-planning
brief without custom fields. Call notes and Slack remain separate local fixtures.
The optional [demo kit](salesforce-demo.md) binds invented context to imported
accounts through their standard AccountNumber values. It reads no call provider
or live Slack workspace.

The tests use invented HTTP responses and a fake `sf` executable. A separate
owner-authorized lead-agent run exercised the live demo kit, as described below.
An owner-provided verification report remains pending. Default demos and
evaluation still use invented local records.

## Use an existing CLI login

Use a Developer Edition org with API access and invented data. The connector
creates no org, user, application, key, or record. The current
[Salesforce CLI](https://developer.salesforce.com/tools/salesforcecli) can log in
under an alias:

```sh
sf org login web --alias gtm-de
gtm-connector salesforce check --sf-org gtm-de
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --state-dir var/salesforce
```

`--sf-org` makes the connector capture `sf org display --target-org gtm-de --json`
for the instance address and `sf org auth show-access-token --target-org gtm-de --json`
for the token. Both commands run inside the connector process with closed stdin,
captured output, telemetry and automatic updates disabled, and CLI file logging
disabled. Each command has a timeout of at most 10 seconds within one 20-second
credential deadline. The connector keeps the token in memory and prints neither
child output nor the alias in a credential error. It does not launch a login flow.

Current CLI versions redact tokens from `org display`; the dedicated
[show-access-token command](https://developer.salesforce.com/docs/platform/salesforce-cli-reference/guide/cli_reference_org_auth_show-access-token.html)
returns the credential. No token needs to be exported, pasted, or written to a
connector file. The owner's existing Salesforce CLI login storage remains trusted.

Without `--sf-org`, the explicit Salesforce backend still reads
`SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` from its environment.
Supply both privately and keep shell tracing off. An explicit `--sf-org` takes
precedence over these variables. The fallback cannot refresh an expired token
and never selects a default CLI org; replace the variables and restart. Variables alone never
select Salesforce. Without `--crm-backend salesforce`, `serve` and `brief` use SQLite.

The adapter accepts an HTTPS My Domain origin ending in `.my.salesforce.com`,
with no path, query, credentials, or nonstandard port. A login URL such as
`login.salesforce.com` is not an instance address. No credential file loads
automatically. The constructor obtains credentials without an adapter REST query. Starting a
Salesforce runtime then checks Organization metadata before opening source
stores and pins the selected connection to private state. Only successful
checks are cached for that adapter's lifetime.

## Choose the expected environment

Without `--sf-environment`, the connector still requires Developer Edition.
Choose an existing sandbox or production org explicitly:

```sh
gtm-connector salesforce check --sf-org EXISTING_ALIAS --sf-environment sandbox --json
gtm-connector serve --crm-backend salesforce --sf-org EXISTING_ALIAS --sf-environment sandbox --state-dir var/salesforce-sandbox
```

Replace `EXISTING_ALIAS` with your existing signed-in alias. Use `production`
only when that is the environment you intend to read. No command creates an org,
changes permissions, or writes provider records. The default fixture commands
never choose a CLI org.

| Selection | Metadata required |
| --- | --- |
| `developer` | OrganizationType is Developer Edition and IsSandbox is false. |
| `sandbox` | IsSandbox is true and OrganizationType is a valid nonempty label. |
| `production` | IsSandbox is false and OrganizationType is a valid non-Developer Edition label. |

Missing or malformed environment metadata refuses. The typed IsSandbox field is
authoritative; there is no inferred environment based on an alias or hostname.
Sandbox and production modes also check that the four standard objects are
queryable and expose the required fixed fields. These successful checks are
cached for the adapter lifetime. Provider permission failures on later reads
still refuse. API-disabled orgs have a separate diagnostic from denied user
access. These checks do not promise support for every Salesforce edition or
custom schema. [Salesforce API requirements](https://developer.salesforce.com/docs/platform/api-rest/guide/intro-rest-compatible-editions.html)

Startup writes the validated origin, org ID, and selected environment only to
private `crm-connection.json` under the state lock. A restart must match all
three. Changing an alias to another org cannot reuse that state; another alias
for the same identity can. Fixture mode cannot open connected state. Earlier
state without this marker requires a fresh folder because its source identity
cannot be proved. Keep the original files; the refusal explains how to select
an unused folder. A demo binding remains a separate required identity check.

The startup source line confirms the environment and connection identity, not
access to every record. Neither that line nor the shareable check prints the
org ID, instance address, alias, or username. Ordinary account results, briefs,
audit arguments, and the private connection marker can contain real data.
Sandbox and production behavior is tested with invented HTTP responses and a
fake CLI. No paid or production org has been exercised.

## Read records and create a brief

Search an existing account book with the installed CLI:

```sh
gtm-connector accounts --crm-backend salesforce --sf-org gtm-de --query SEARCH_TEXT
```

Replace `SEARCH_TEXT` with a literal part of the account name. Use a narrow
query when the org contains more than the 100-row bound. Use a returned account
ID with `gtm-connector account ACCOUNT_ID --crm-backend salesforce --sf-org gtm-de`.
Add the same explicit `--sf-environment` for sandbox or production. These
commands obtain a local `crm:read` grant and use the normal audited gateway;
they do not bypass its scope or receipt checks. Their separate default state
folder is `var/salesforce`. An explicit `--state-dir` selects another folder;
`--db` belongs to SQLite and cannot be mixed with Salesforce.

The account results go to stdout as readable tables or the documented JSON
array/object. A human source label goes to stderr once. These are local commands
that start and stop their own runtime. To use a concurrently running `serve`,
use an MCP client rather than opening its locked state in another CLI runtime.

In a second terminal for a running server, create the Inspector's explicit read grant:

```sh
gtm-connector connect inspector --scope crm:read --state-dir var/salesforce
```

The command prints the exact Inspector setup, including local certificate trust.
To search after that setup, run:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/salesforce/tls.pem" NO_PROXY=127.0.0.1 NODE_TLS_REJECT_UNAUTHORIZED=1 npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/salesforce/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name search_accounts
```

Use a returned account `id` for `get_account` or `list_opportunities`. IDs have
an `acc-sf-` prefix followed by the 18-character Salesforce ID; fixture IDs such
as `acc-003` do not identify org records. See [Inspector setup](inspector.md).

Set `ACCOUNT_ID` to a returned account ID in your terminal, then create a brief
in its own private state folder:

```sh
gtm-connector brief "$ACCOUNT_ID" --crm-backend salesforce --sf-org gtm-de --state-dir var/salesforce-brief
```

The Salesforce CLI brief defaults to today's local calendar date. Add
`--as-of YYYY-MM-DD` to use a specific planning date. Local fixture briefs retain
`2027-01-15` as their default so recordings and evaluation remain repeatable.
The default drafter uses fixed supported claims and calls no model.

A brief selects the Contract with the earliest non-null `EndDate` on or after
the planning date, breaking ties by Contract ID. Its status category must be
`Draft`, `InApproval`, or `Activated`. Draft and InApproval contracts count as
planning candidates because a rep can plan before activation. The brief must
state that they have not been activated. An Activated contract is labeled with
that category; the brief does not turn it into an ARR or risk estimate.

Salesforce calculates standard `EndDate` from `StartDate` and `ContractTerm`
when its automatic calculation setting is enabled. The connector always uses
the returned `EndDate`, including null, and never calculates a substitute.
Salesforce creates a contract as Draft; activation is a later update. The
connector performs neither write. See the official
[Contract reference](https://developer.salesforce.com/docs/platform/object-reference/references/standard-objects/object-contract.html).

An account without a dated eligible Contract refuses with
`renewal_fields_unmapped` and exit `3`, after the authorized reads and before
drafting. Run `gtm-connector salesforce check --sf-org gtm-de` for the missing
setup or data. Required date and category disclosures cannot be omitted or
rewritten in a checked candidate. `--with-slack` adds the existing independent
Slack read. Without an explicit demo binding, local call and Slack reads for
these namespaced IDs are empty. A bound demo supplies invented local context;
its audit receipts still distinguish returned records, empty results, and denied
grants.

Ctrl-C stops a running server. Local MCP grants have a separate five-minute
expiry. Refreshing Salesforce credentials does not refresh a local MCP grant.

## Field mapping

Each result has `source: crm` and `untrusted_data: true`. Each returned row has
a `record_ref`, `backend: salesforce`, and its native `source_id`. Adapter IDs use `acc-sf-`,
`con-sf-`, `opp-sf-`, or `ctr-sf-`; references live under the matching
`crm/accounts/`, `crm/contacts/`, `crm/opportunities/`, or `crm/contracts/` path.
Contracts and contacts share the existing `get_account` tool receipt.

| Source | Returned fields |
| --- | --- |
| Account | `Name` to `name`; nullable `Industry` to `industry`; nullable `AccountNumber` to `account_number`. |
| Contact | `Name` to `name`; nullable `Title` and `Email` to `title` and `email`; `AccountId` to namespaced `account_id`. |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; nullable `AccountId` to namespaced `account_id`. |
| Opportunity amount | `Amount` to a decimal string or null in `amount_decimal`; `currency` stays null, meaning unknown. |
| Contract | `AccountId` to namespaced `account_id`; raw `Status` to `status`; `StatusCode` to `status_code`; nullable `StartDate`, `EndDate`, and `ContractTerm` to `start_date`, `end_date`, and `contract_term`. |

The adapter validates IDs, account associations, canonical dates, known status
categories, and a nullable signed 32-bit integer term. It preserves custom
status labels and nullable values. `AccountNumber` supports the explicit demo
binding below; returning the field alone does not create a join.

`Account.AnnualRevenue` is not ARR, and opportunity `CloseDate` is not a renewal
date. Briefs must state that ARR and CRM renewal risk are unmapped. The adapter
leaves opportunity currency unknown because `CurrencyIsoCode` is not available
in every org. Unknown values are never replaced by zero or USD.

Salesforce evidence uses `workflow_version: 3` and `crm_backend: salesforce`
inside the existing `schema_version: 1` snapshot. Fixture evidence keeps its
existing versions and evaluation claims. A saved brief contains source fields,
record references, receipt copies, and the planning date. Keep that private
state out of Git; the ordinary brief is not a shareable sanitized report.

## Bind the invented demo book

The [demo-kit guide](salesforce-demo.md) covers exporting the packaged assets,
one `sf data import tree --plan` command, binding its JSON receipt, creating a
brief, recording it privately, and one cleanup command. The book has three
invented accounts and one Contact, Opportunity, and Draft Contract per account,
for twelve imported records. Its local notes and Slack messages include a
procurement disagreement, a stale note, and an injected instruction quoted as
data. One account lacks an industry, one contact lacks a title, and one
opportunity has no amount. These unknown values remain visible.

The first Draft Contract supplies StartDate `2026-02-01` and ContractTerm `12`.
No import record supplies EndDate. The owner-run brief helper uses the fixed
planning date `2027-01-15`; it still reads the actual EndDate returned by
Salesforce. Ordinary Salesforce `brief` continues to default to today unless
`--as-of` is given.

The binding validates a complete successful import receipt against the packaged
manifest's references and expected object ID prefixes. The CLI's import-plan
result can label nested records with the root Account type, so that label is
not the authority for child identity. The private binding records all twelve
imported IDs, three AccountNumber associations, and the manifest digest.
It is an owner assertion, not proof of which org produced the receipt.

Pass the binding explicitly to the read-only server or brief:

```sh
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --salesforce-demo-binding var/salesforce-demo/binding.json --state-dir var/salesforce-demo/run
```

The first bound run needs an empty state folder. A private source-profile marker
pins the kit, manifest digest, and binding digest while the server holds its
state lock. A different binding, default fixture mode, or preexisting unmarked
state refuses instead of mixing local source rows. Complete existing note and
message sets preserve owner edits; partial or mismatched sets refuse.

The authorized `get_account` result must match the selected bound AccountNumber
in both directions. This check adds no GET. Call-only or Slack-only clients read
their independently granted local source without an additional Salesforce query
after the owner's startup metadata check. An
unrelated account has no demo notes or messages. The binding does not add a
scope, grant a tool, or establish Salesforce user identity.

The separate owner helper imports and deletes through `sf`; the installed
connector still has no Salesforce write path. Cleanup selects only imported
IDs, checks supplied fields and parent associations for all four object types,
requires the kit contracts to remain Draft, and inspects described cascading
and restricted child relationships before its first delete. It permits a fixed
list of system history records and owner-generated sharing rows. Unexpected
children or other share causes refuse cleanup. Deletes run children before
accounts, then the helper checks that the imported IDs are absent.

These reads and deletes are separate operations. Preflight cannot exclude
concurrent edits, hidden relationships, or trigger effects, and a later failure
can leave some records already removed. There is no transaction or race-safety
guarantee. Use the intended Developer Edition org with sufficient read access,
stop concurrent changes, and retain the receipt and binding until cleanup is
complete. The helper never broadens a failed lookup to a name or prefix delete.
Recordings, snapshots, receipts, and bindings can contain org identifiers even
when the source names were invented; keep them in ignored private storage and
review any sharing yourself.

## Run the shareable setup check

```sh
gtm-connector salesforce check --sf-org gtm-de --json
```

The check reports credentials, edition (or `environment` in sandbox/production
mode), required fields for all four objects, an account-read probe, a renewal
candidate, account/contact/contract reads,
opportunities, and a brief through local HTTPS. It uses private temporary state
for the brief and removes that state on normal completion. It prints no record
names, IDs, emails, org address, username, token, raw describe metadata, or
upstream error body. Missing fields are named from a fixed required-field list.
Developer mode retains its bounded account search. Sandbox and production sample
at most one account for this step, so a large healthy book does not fail solely
because it has more than 100 accounts. The count describes the sample, not the
size of the org. Ordinary `accounts` searches still enforce the 100-row bound.
The remaining steps exercise one renewal candidate, not every account.
In sandbox and production modes, Organization and all four descriptions share
the same 12-request, 1-MiB, 20-second budget as a cold server startup. Descriptions
that each fit but exceed that combined bound fail the check. Reducing an account
search does not reduce schema metadata; this connection cannot start within
these limits. Individual field failures still name the missing standard fields.
A prerequisite failure marks dependent steps as failed with a next action.

JSON has `schema_version: 1`, `status` (`passed` or `failed`), `as_of`, `steps`,
`api_calls`, `sf_cli_invocations`, `api_call_definition`, and `sf_cli_definition`.
Each step has `name`, `status` (`pass` or `fail`), `count`, `code`, `message`,
`fix`, `duration_ms`, `api_calls`, and `sf_cli_invocations`. Counts describe
values loaded, fields present, records read, or accepted brief lines for that
step. A failed step has count zero; it may still have attempted GETs.
A failed report exits `4` after printing all steps. Without `--json`, the same
steps appear as a table. Progress goes to stderr; JSON mode emits lines shaped
as `{"progress":{"command":"salesforce check","step":"edition","status":"running","count":null}}`.
Finished steps use status `pass` or `fail` and an integer count. Stdout remains
one final JSON report. Progress contains no org data. See [the CLI contract](cli.md).

`api_calls` counts this adapter's REST GET attempts, including retries, describe,
and edition queries. `sf_cli_invocations` counts attempted child commands,
including failed launches. It does not measure the CLI's internal HTTP traffic,
other org activity, or Salesforce's total API quota consumption. Timings include
the work of the named step and do not establish a performance guarantee.

## Request and failure boundary

Before a tool executes, the gateway commits a pending receipt, checks the bearer
grant, and applies its local rate limit. Missing `crm:read` returns HTTP 403
without an additional provider request. Initial audit failure returns HTTP 503
without a provider read. The owner's earlier startup metadata check is separate
from these client attempts. Successful reads and failed queries finalize the same receipt. The
owner-only setup check also performs explicit adapter probes outside MCP; its
brief step exercises the normal MCP boundary.

The adapter sends fixed GET queries with escaped search text. TLS verification
stays on. Proxies and redirects are disabled. Only same-origin relative query
locators are accepted. The external backend lists five read tools, omits
`log_activity`, and rejects a direct write attempt with `crm_write_disabled`.
The connector never writes Salesforce records.

For `--sf-org`, the first upstream HTTP 401 in an operation asks `sf` for
credentials once. If an org identity was already checked, it rereads Organization
before retrying the original GET. This detects an org replacement behind an
unchanged origin, such as a refreshed sandbox. The identity check shares the
same request, byte, and deadline budget. A second 401, failed credential lookup,
or changed instance or org identity fails with `salesforce_auth_required`.
There is no retry for other upstream errors. The retry and credential lookup
share the original operation's deadline and request budget; they do not reset it.

Each operation allows 12 GET attempts and 1 MiB of decoded response data, with
10 pages and 100 rows per query. Account lookup expects at most one account.
Contacts, contracts, and opportunities reject excess rows rather than return
partial records. A 20-second deadline is checked around reads. Each request's
network timeout uses the remaining deadline, up to 5 seconds. This is not hard
wall-clock cancellation. Requests ask for identity encoding; compressed responses
are rejected.
The local CLI client allows 25 seconds for a Salesforce MCP response so the
adapter can finish its 20-second operation and audit receipt. Its connection,
write, and pool timeouts remain 10 seconds.

| Condition | Result |
| --- | --- |
| Missing or invalid environment configuration | `salesforce_invalid_configuration`. |
| Missing CLI login, unusable CLI credentials, or failed refresh | `salesforce_auth_required`. |
| Default developer mode sees another edition | `salesforce_edition_required`; no CRM record query. |
| Selected environment does not match returned metadata | `salesforce_environment_mismatch`; no CRM record query. |
| API access is disabled for the org | `salesforce_api_disabled`. |
| Required standard schema is unavailable | `salesforce_schema_unavailable`; run the setup check for missing fields. |
| Private state belongs to another connection or cannot establish identity | `source_identity_mismatch`; select a fresh state folder. |
| Upstream HTTP 401 after the permitted refresh, or environment-token expiry | `salesforce_auth_required`. |
| Upstream HTTP 429 or 403 with `REQUEST_LIMIT_EXCEEDED` | `salesforce_rate_limited`. |
| Other upstream HTTP 403 | `salesforce_forbidden`. |
| Timeout or transport failure | `salesforce_timeout` or `salesforce_unavailable`. |
| Other upstream HTTP status | `salesforce_http_error`. |
| Invalid payload, locator, arguments, or exceeded bound | Fixed `salesforce_*` error; no partial records. |

An upstream failure is an MCP tool error (`isError: true` over HTTP 200), not a
local OAuth challenge. The SDK may prefix the fixed code with the tool name.
Raw upstream messages and credentials are withheld. Local authentication,
scope, disabled-write, and rate-limit failures keep their existing HTTP codes.

## Measured request counts and verification

The request-count tests use single-page invented responses, no token expiry,
and one adapter instance. Cold means its first call; warm means the successful
edition check is already cached. These are observed request counts, not timings.

| Tool | Previous count per call | New cold count | New warm count |
| --- | --- | --- | --- |
| `search_accounts` | 2 | 2 | 1 |
| `get_account` | 3 | 4 | 3 |
| `list_opportunities` | 2 | 2 | 1 |

The previous `get_account` read Account and Contact only. The new call also
reads Contract, so its warm total stays at three while removing the repeated
edition query. A fresh Salesforce brief uses five GETs for edition, account,
contacts, contracts, and opportunities. Pagination and a permitted 401 retry
add requests within the existing bounds. CRM records are not cached.

With the explicit-environment extension, a Developer Edition runtime moves its
single Organization GET to owner startup. Its later search, account, and
opportunity tool reads use 1, 3, and 1 GETs respectively on single-page responses.
A fresh sandbox or production runtime uses five metadata GETs: Organization
plus four object descriptions. Its later tool counts are the same. Repeated
successful metadata checks in the same adapter add no describe requests. A
fresh brief therefore uses five GETs in developer mode and nine in sandbox or
production mode, including owner startup. These bounds and counts are tested
with invented responses, not measured live-org performance.

```sh
.venv/bin/python -m unittest discover -s tests -p 'test_salesforce*.py' -v
.venv/bin/python -m unittest tests.test_sf_credentials -v
```

[Adapter tests](../tests/test_salesforce.py) check mappings, nulls, associations,
query escaping, edition caching, and bounds. [Credential tests](../tests/test_sf_credentials.py)
use a fake CLI on PATH to check capture, refresh, pinned origins, fixed errors,
and original budgets. [Gateway tests](../tests/test_salesforce_gateway.py) and
[briefing tests](../tests/test_salesforce_briefing.py) check local HTTPS scopes,
receipts, standard Contract citations, and required disclosures. These results
establish fake-response behavior, not production compatibility.

An owner-authorized lead-agent run on November 17, 2026 imported the twelve-record
book and bound all three accounts. Its accepted brief contained thirteen checked
lines, four CRM records, one local call note, one local Slack message, and four
audit receipts. It retained the Draft disclosure and Salesforce-returned
EndDate `2027-01-31`. A private recording and provenance file completed with
mode-0600 permissions. Cleanup preflight covered all four object types; the
helper used 74 captured CLI commands, confirmed twelve deletes, and verified
zero remaining imported records. These command counts are not total API quota
usage. [Run details and timings](salesforce-demo.md) state the prerequisites and
limits. No live call provider, Slack workspace, or hosted assistant was used,
and no org-bearing artifact was committed. Owner verification remains pending.

On November 18, 2026, an owner-authorized lead-agent check ran the new `accounts`
CLI twice against the existing Developer Edition login with an invented no-match
search. Both reads returned zero records and one successful audit receipt each.
The second process reused the same private connection identity. No provider
records were created or modified. This observes the Developer Edition command
and restart path, not sandbox or production compatibility. Private source state
and org identity were not committed.

The local owner can grant CRM access, and the upstream token may allow more than
reads. Audit arguments and record references can identify org records. Saved
briefs include source data. Retention is manual. See [the security review](../SECURITY_REVIEW.md).
