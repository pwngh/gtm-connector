# Read a Salesforce Developer Edition org

The optional Salesforce backend reads standard Account, Contact, Opportunity,
and Contract records through REST v68.0. It can produce a cited renewal-planning
brief without custom fields. Call notes and Slack remain separate local fixtures;
there is no mapping from an org account to those fixtures yet.

The tests use invented HTTP responses and a fake `sf` executable. No live org
has been verified. Default demos and evaluation still use invented local records.

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
automatically. Startup obtains credentials but performs no adapter REST query.
The first authorized CRM call checks `OrganizationType` for exactly
`Developer Edition`. Only a successful check is cached for that adapter's lifetime.

## Read records and create a brief

In a second terminal, create the Inspector's explicit read grant:

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
Slack read; until an explicit fixture mapping exists, local call and Slack
reads for these namespaced IDs are empty. Their audit receipts still distinguish
empty results from denied grants.

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
status labels and nullable values. `AccountNumber` is available for a future
explicit fixture mapping; it does not create a join by itself.

`Account.AnnualRevenue` is not ARR, and opportunity `CloseDate` is not a renewal
date. Briefs must state that ARR and CRM renewal risk are unmapped. The adapter
leaves opportunity currency unknown because `CurrencyIsoCode` is not available
in every org. Unknown values are never replaced by zero or USD.

Salesforce evidence uses `workflow_version: 3` and `crm_backend: salesforce`
inside the existing `schema_version: 1` snapshot. Fixture evidence keeps its
existing versions and evaluation claims. A saved brief contains source fields,
record references, receipt copies, and the planning date. Keep that private
state out of Git; the ordinary brief is not a shareable sanitized report.

## Run the shareable setup check

```sh
gtm-connector salesforce check --sf-org gtm-de --json
```

The check reports credentials, edition, required fields for all four objects,
bounded account search, a renewal candidate, account/contact/contract reads,
opportunities, and a brief through local HTTPS. It uses private temporary state
for the brief and removes that state on normal completion. It prints no record
names, IDs, emails, org address, username, token, raw describe metadata, or
upstream error body. Missing fields are named from a fixed required-field list.
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
without an org request. Initial audit failure returns HTTP 503 without an org
request. Successful reads and failed queries finalize the same receipt. The
owner-only setup check also performs explicit adapter probes outside MCP; its
brief step exercises the normal MCP boundary.

The adapter sends fixed GET queries with escaped search text. TLS verification
stays on. Proxies and redirects are disabled. Only same-origin relative query
locators are accepted. The external backend lists five read tools, omits
`log_activity`, and rejects a direct write attempt with `crm_write_disabled`.
The connector never writes Salesforce records.

For `--sf-org`, the first upstream HTTP 401 in an operation asks `sf` for
credentials once and retries that same GET. A second 401, failed credential
lookup, or changed instance origin fails with `salesforce_auth_required`.
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
| Edition is not Developer Edition | `salesforce_edition_required`; no CRM query. |
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
add requests within the existing bounds. No result or schema cache is added.

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
establish fake-response behavior, not production compatibility or a live org test.

The local owner can grant CRM access, and the upstream token may allow more than
reads. Audit arguments and record references can identify org records. Saved
briefs include source data. Retention is manual. See [the security review](../SECURITY_REVIEW.md).
