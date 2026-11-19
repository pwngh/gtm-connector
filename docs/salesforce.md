# Read an existing Salesforce org

The optional Salesforce backend reads standard Account, Contact, Opportunity,
and Contract records through REST v68.0. It builds a cited renewal brief without
custom fields and never writes to Salesforce. Call notes and Slack stay local
fixtures unless you select [imported transcripts](calls.md) or
[live Slack](slack-live.md). The [demo kit](salesforce-demo.md) adds an invented
account book to a Developer Edition org.

Tests use invented HTTP responses and a fake `sf` executable. No live org has
been verified.

## Commands

Sign in with the [Salesforce CLI](https://developer.salesforce.com/tools/salesforcecli)
under an alias, then pass that alias to each command:

```sh
sf org login web --alias gtm-de
gtm-connector salesforce check --sf-org gtm-de
gtm-connector accounts --crm-backend salesforce --sf-org gtm-de --query SEARCH_TEXT
gtm-connector account ACCOUNT_ID --crm-backend salesforce --sf-org gtm-de
gtm-connector brief ACCOUNT_ID --crm-backend salesforce --sf-org gtm-de --state-dir var/salesforce-brief
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --state-dir var/salesforce
```

| Option | Meaning |
| --- | --- |
| `--crm-backend salesforce` | Selects Salesforce for `accounts`, `account`, `brief`, and `serve`. On those commands `--sf-org` and `--sf-environment` require it. Environment variables alone never select Salesforce. |
| `--sf-org ALIAS` | Reads credentials from this signed-in `sf` alias. |
| `--sf-environment` | `developer` (the default), `sandbox`, or `production`. |
| `--state-dir PATH` | Private state folder. `accounts` and `account` default to `var/salesforce`. `brief` and `serve` default to `var/v2`, which fixture commands also use, so pass a separate folder. `--db` cannot be combined with Salesforce. |
| `--query TEXT` | For `accounts`: a literal part of the account name, at most 200 characters. |
| `--as-of YYYY-MM-DD` | For `brief` and `salesforce check`: the planning date. The default is today's local date. |
| `--with-slack` | For `brief`: adds the separate Slack read. |

Account IDs are `acc-sf-` followed by the 18-character Salesforce ID. Fixture IDs
such as `acc-003` do not identify org records. `accounts` returns at most 100
rows, so use a narrower query in a larger org. The connector creates no org,
user, application, key, or record.

## Credentials

`--sf-org` makes the connector capture `sf org display --target-org gtm-de --json`
for the instance address and `sf org auth show-access-token --target-org gtm-de --json`
for the token. Both commands run inside the connector process with closed stdin,
captured output, telemetry and automatic updates disabled, and CLI file logging
disabled. Each command has a timeout of at most 10 seconds within one 20-second
credential deadline. The connector keeps the token in memory and prints neither
child output nor the alias in a credential error. It does not launch a login flow.

Without `--sf-org`, the Salesforce backend reads `SALESFORCE_INSTANCE_URL` and
`SALESFORCE_ACCESS_TOKEN` from its environment. Supply both privately. This
fallback cannot refresh an expired token, so replace the variables and restart.
An explicit `--sf-org` takes precedence, and no default CLI org is ever selected.

The instance address must be an HTTPS My Domain origin ending in
`.my.salesforce.com`, with no path, query, credentials, or nonstandard port.
`login.salesforce.com` is not an instance address.

## Environments

| Selection | Metadata required |
| --- | --- |
| `developer` | OrganizationType is Developer Edition and IsSandbox is false. |
| `sandbox` | IsSandbox is true and OrganizationType is a valid nonempty label. |
| `production` | IsSandbox is false and OrganizationType is a valid non-Developer Edition label. |

Missing or malformed metadata refuses. The environment is never inferred from an
alias or hostname. Sandbox and production also require that the four objects are
queryable and expose the fields in the [field mapping](#field-mapping). These
checks do not promise support for every
[Salesforce edition](https://developer.salesforce.com/docs/platform/api-rest/guide/intro-rest-compatible-editions.html)
or custom schema.

## State folder

Startup checks Organization metadata before it opens any source store. It then
writes the instance origin, org ID, and environment to private
`crm-connection.json` in the state folder. A later run must match all three, or
it fails with `source_identity_mismatch`. Another alias for the same org works.
A folder that holds fixture state, or earlier Salesforce state without this
file, also refuses. Choose an unused `--state-dir` and keep the original files.

One process holds a state folder at a time. While `serve` runs, use an MCP
client, or give `accounts`, `account`, and `brief` another folder.

## Server and tools

For a running server, create a read grant in a second terminal:

```sh
gtm-connector connect inspector --scope crm:read --state-dir var/salesforce
```

The command prints the Inspector setup. See [Inspector setup](inspector.md) and
[client setup](clients.md). A grant expires after five minutes, and refreshing
Salesforce credentials does not renew it. The Salesforce server lists the five
read tools and omits `log_activity`. Missing `crm:read` returns HTTP 403 without
a Salesforce request. `accounts` and `account` use the same audited gateway.
To limit a server to named accounts, add a [selection](selection.md).

## Briefs

A brief selects the Contract with the earliest non-null `EndDate` on or after
the planning date, breaking ties by Contract ID. Its status category must be
`Draft`, `InApproval`, or `Activated`. The connector uses the `EndDate` that
Salesforce returns and never calculates one. Salesforce fills `EndDate` from
`StartDate` and `ContractTerm` only when the org's automatic calculation setting
is on. See the
[Contract reference](https://developer.salesforce.com/docs/platform/object-reference/references/standard-objects/object-contract.html).

Every Salesforce brief states the contract's status, category, and end date. It
states that a Draft or InApproval contract has not been activated, and that ARR
and CRM renewal risk are not mapped from standard fields. A checked candidate
cannot omit or reword these lines. `Account.AnnualRevenue` is not ARR, and an
opportunity `CloseDate` is not a renewal date. Amounts are printed with
`(currency unknown)`, never as zero or USD.

An account without a dated eligible Contract refuses with
`renewal_fields_unmapped` and exit `3`, after the reads and before drafting.
Without a demo binding, imported transcripts, or live Slack, call-note and Slack
reads for `acc-sf-` IDs return no records. Salesforce evidence uses
`workflow_version: 3` and `crm_backend: salesforce`. See [briefs](briefs.md).

## Field mapping

Each result has `source: crm` and `untrusted_data: true`. Each row has a
`record_ref`, `backend: salesforce`, and its native `source_id`. IDs use
`acc-sf-`, `con-sf-`, `opp-sf-`, or `ctr-sf-`, and references live under
`crm/accounts/`, `crm/contacts/`, `crm/opportunities/`, or `crm/contracts/`.
Contacts and contracts are returned by `get_account`.

| Source | Returned fields |
| --- | --- |
| Account | `Name` to `name`; nullable `Industry` to `industry`; nullable `AccountNumber` to `account_number`. |
| Contact | `Name` to `name`; nullable `Title` and `Email` to `title` and `email`; `AccountId` to namespaced `account_id`. |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; nullable `AccountId` to namespaced `account_id`. |
| Opportunity amount | `Amount` to a decimal string or null in `amount_decimal`; `currency` stays null, meaning unknown. |
| Contract | `AccountId` to namespaced `account_id`; raw `Status` to `status`; `StatusCode` to `status_code`; nullable `StartDate`, `EndDate`, and `ContractTerm` to `start_date`, `end_date`, and `contract_term`. |

The adapter validates IDs, account associations, dates, status categories, and a
nullable signed 32-bit term. It preserves custom status labels and null values.

## Setup check

```sh
gtm-connector salesforce check --sf-org gtm-de --json
```

The check runs these steps in order: `credentials`, `edition` (`environment` in
sandbox and production), `account_fields`, `contact_fields`,
`opportunity_fields`, `contract_fields`, `account_search`, `renewal_candidate`,
`account_read`, `opportunity_read`, and `brief`. A failed step marks the steps
that depend on it as failed with `prerequisite_failed`. A failed report exits
`4` after printing every step.

The report prints no record names, IDs, emails, org address, username, token, or
upstream error body. In developer mode `account_search` lists accounts and fails
above 100. In sandbox and production it reads one account, so its count is a
sample. The later steps exercise one renewal candidate, not every account. In
sandbox and production, Organization and the four descriptions share one
12-request, 1-MiB, 20-second budget, as a server startup does.

The JSON report has `schema_version: 1`, `status` (`passed` or `failed`),
`as_of`, `steps`, `api_calls`, `sf_cli_invocations`, `api_call_definition`, and
`sf_cli_definition`. Each step has `name`, `status` (`pass` or `fail`), `count`,
`code`, `message`, `fix`, `duration_ms`, `api_calls`, and `sf_cli_invocations`.
A failed step has count zero. `api_calls` counts this adapter's GET attempts and
`sf_cli_invocations` its `sf` commands, not Salesforce's total API quota use.

Without `--json`, the steps print as a table. Progress goes to stderr. In JSON
mode each progress line has this shape, and stdout holds one final report:

```json
{"progress": {"command": "salesforce check", "step": "edition", "status": "running", "count": null}}
```

## Limits

| Limit | Value |
| --- | --- |
| GET attempts per operation | 12, including retries. |
| Decoded response data per operation | 1 MiB. Compressed responses are rejected. |
| Pages per query | 10. |
| Rows per query | 100. Excess rows fail and no partial records are returned. |
| AccountNumber lookup | 2 rows. |
| Operation deadline | 20 seconds, checked around reads. It is not a hard cancellation. |
| Network timeout per request | The remaining deadline, up to 5 seconds. |

The local CLI client allows 25 seconds for a Salesforce MCP response so the
adapter can finish its 20-second operation and audit receipt. Its connection,
write, and pool timeouts remain 10 seconds.

With `--sf-org`, the first HTTP 401 in an operation asks `sf` for credentials
once and confirms the org identity before retrying the GET, inside the same
budget. No other upstream error is retried. TLS verification stays on, and
proxies and redirects are disabled.

On single-page responses, `search_accounts` uses 1 GET, `get_account` 3, and
`list_opportunities` 1. Startup adds 1 GET in developer mode and 5 in sandbox or
production, so a fresh brief uses 5 or 9. CRM records are not cached.

## Errors

`salesforce_invalid_configuration` exits `2`. `salesforce_environment_mismatch`,
`source_identity_mismatch`, and `renewal_fields_unmapped` exit `3`. Other
`salesforce_*` codes exit `5`. Over MCP, an upstream failure is a tool error
(`isError: true` over HTTP 200) whose text may carry the tool name before the
code. Upstream messages are withheld. See [the CLI contract](cli.md).

| Code | Cause | Fix |
| --- | --- | --- |
| `salesforce_invalid_configuration` | The instance address, token, or environment is missing or invalid. | Run `sf org login web --alias gtm-de` and pass `--sf-org gtm-de`, or set both variables. |
| `salesforce_auth_required` | The `sf` login or refresh gave no usable credentials, a second 401 arrived, or the org behind the alias changed. | Sign in again with `sf`, or replace the environment token and restart. |
| `salesforce_edition_required` | Developer mode saw another edition. | Select a Developer Edition alias, or pass the intended `--sf-environment`. |
| `salesforce_environment_mismatch` | The org does not match `--sf-environment`. | Check the alias and repeat with the intended environment. |
| `salesforce_api_disabled` | API access is disabled for the org. | Use an API-enabled org and a user with the API Enabled permission. |
| `salesforce_schema_unavailable` | A required standard field is not exposed. | Run `salesforce check` with the same alias and environment to see the missing fields. |
| `salesforce_forbidden` | Salesforce denied the read with another HTTP 403. | Grant the user read access to the four objects and their fields. |
| `salesforce_rate_limited` | HTTP 429, or HTTP 403 with `REQUEST_LIMIT_EXCEEDED`. | Wait for API capacity and retry. |
| `salesforce_result_limit` | A read exceeded the request, page, row, or size limit. | Use a narrower search or a smaller account book. |
| `salesforce_pagination_invalid` | Salesforce returned an unsupported page locator. | Retry with a narrower search. |
| `salesforce_timeout`, `salesforce_unavailable` | The deadline passed, or the endpoint could not be reached. | Check connectivity and the `sf` login, then retry. |
| `salesforce_http_error` | Another upstream HTTP status. | Check that the object is available and readable in this org. |
| `salesforce_invalid_response` | The response was outside the supported shape. | Check access to the mapped fields, then retry. |
| `salesforce_invalid_arguments` | An account ID, search text, or date is invalid. | Use an `acc-sf-` ID, a shorter query, or a `YYYY-MM-DD` date. |
| `salesforce_account_number_ambiguous` | An AccountNumber lookup returned a number that is not a literal match. `calls import` reports `calls_account_ambiguous`. | Select the account with `--account` and its canonical ID. |
| `source_identity_mismatch` | The state folder belongs to another connection or cannot prove its identity. | Restore the original connection, or use an unused `--state-dir`. |
| `renewal_fields_unmapped` | No eligible Contract has an `EndDate` on or after the planning date. | Run `salesforce check`. Verify the contract's `EndDate` and read access. |
| `crm_write_disabled` | A client attempted a write. | Do not retry. The Salesforce backend is read-only. |

The connector sends only GET requests, but the upstream token may allow more.
Account results, briefs, audit arguments, and the state folder contain org data.
Keep them out of Git. Retention is manual. See
[the security review](../SECURITY_REVIEW.md).
