# Read a Salesforce Developer Edition org

This optional adapter replaces the three CRM read tools. It uses Salesforce
REST v68.0. Call notes and Slack stay in their isolated SQLite stores. The
default demo, evaluation, and model benchmark still use invented records.
Tests exercise the adapter with invented HTTP responses. No live org result
or production compatibility is claimed.

## Prepare an org yourself

Use your own Developer Edition org with invented data and API access. Salesforce
provides a [free Developer Edition signup](https://developer.salesforce.com/docs/platform/api-rest/guide/quickstart-dev-org.html).
The connector creates no org, user, application, or key. Do not use a production
org or employer/client data. Every CRM operation checks `OrganizationType` and
requires exactly `Developer Edition`; other editions fail before CRM queries.

Install the current [Salesforce CLI](https://developer.salesforce.com/tools/salesforcecli).
Authenticate your org, using a separate alias:

```sh
sf org login web --alias gtm-de
```

Supply `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` privately in the
terminal that will run the server. These commands capture the values without
printing the credential or placing its value in shell history:

```sh
export SALESFORCE_INSTANCE_URL="$(sf org display --target-org gtm-de --json | .venv/bin/python -c 'import json,sys; print(json.load(sys.stdin)["result"]["instanceUrl"])')"
export SALESFORCE_ACCESS_TOKEN="$(sf org auth show-access-token --target-org gtm-de --json | .venv/bin/python -c 'import json,sys; print(json.load(sys.stdin)["result"]["accessToken"])')"
```

Current CLI versions redact the token in `org display`; use the explicit
[show-access-token command](https://developer.salesforce.com/docs/platform/salesforce-cli-reference/guide/cli_reference_org_auth_show-access-token.html).
Do not enable shell tracing or paste the credential into a tool argument.
The connector accepts an HTTPS My Domain origin ending in `.my.salesforce.com`,
without a path, query, credentials, or nonstandard port. A login URL such as
`login.salesforce.com` is not an instance URL. Use the CLI's `instanceUrl`.
No credential file is loaded automatically.

## Start and read

After installing the Python dependencies, keep this command running:

```sh
.venv/bin/python -m gtm_connector serve --crm-backend salesforce --state-dir var/salesforce
```

Startup validates configuration locally. The first authorized CRM tool performs
the edition check and data query. Environment variables alone never activate
the adapter. With no backend flag, `serve` still uses SQLite.

In another terminal, follow the [Inspector setup](inspector.md) with
`var/salesforce` in place of `var/inspector`. Grant `crm:read`. Set that state's
certificate in `NODE_EXTRA_CA_CERTS`. Search first:

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/salesforce/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name search_accounts --tool-arg query=
```

Copy an actual returned `id`, then pass it as `account_id` to `get_account` or
`list_opportunities` using the same Inspector command. An account ID has the
form `acc-sf-` followed by its 18-character Salesforce ID. Do not use `acc-003`;
that belongs to the fixture. Search returns at most 100 accounts. Refine the
query if it reports `salesforce_result_limit`. Opportunity and contact queries
also reject results over 100 rows instead of returning a partial answer.

The external backend lists five read tools. `log_activity` is absent. A direct
write attempt with a valid write grant returns HTTP 403 and `crm_write_disabled`.
No activity is created locally or remotely. Call notes and Slack remain mocks;
there is no ID mapping from external accounts to fixture notes or messages.

Stop with Ctrl-C. Remove the variables from that terminal when finished:

```sh
unset SALESFORCE_INSTANCE_URL SALESFORCE_ACCESS_TOKEN
```

The adapter does not refresh tokens. Obtain a new access token and restart the
server when needed. The local MCP tokens have their separate five-minute expiry.

## Field mapping

The gateway returns `source: crm`, `untrusted_data: true`, and a `record_ref`
under `crm/accounts/`, `crm/contacts/`, or `crm/opportunities/`. Each row also has
`backend: salesforce`, its native `source_id`, and an `id` prefixed with
`acc-sf-`, `con-sf-`, or `opp-sf-`. Nullable mapped fields preserve null values.

| Source | Returned fields |
| --- | --- |
| Account | `Name` to `name`; `Industry` to `industry` |
| Contact | `Name` to `name`; `Title` to `title`; `Email` to `email`; `AccountId` to namespaced `account_id` |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; `AccountId` to namespaced `account_id` or null |
| Opportunity amount | `Amount` to `amount_decimal`, a decimal string or null; `currency` is null, meaning unknown |

`CurrencyIsoCode` is not available in every org. This adapter does not infer a
currency. It does not treat `Account.AnnualRevenue` as ARR, relabel `CloseDate`
as a renewal date, normalize custom stages, or invent renewal risk.

Those missing mappings prevent renewal briefs from this backend. This command
exits 2 with `renewal_fields_unmapped`, before reading data or invoking a model:

```sh
.venv/bin/python -m gtm_connector brief acc-sf-REPLACE_WITH_RETURNED_ID --crm-backend salesforce
```

The local `seed`, `accounts`, and `account` commands remain direct SQLite
administration. They never query or modify Salesforce. The runtime initializes
its fixture files but does not copy org records into them.

## Request and failure boundary

The local gateway still commits a pending receipt, checks the bearer grant,
and applies its rate limit before tool execution. A missing `crm:read` grant
returns HTTP 403 without an upstream request. Initial audit failure returns
HTTP 503 without an upstream request. A successful external read or failed
query finalizes the same receipt. Audit rows store returned references, not
record bodies. Search terms and supplied IDs remain in arguments.

The adapter sends only GET requests with fixed field lists and escaped search
text. It uses TLS verification, no proxies, no redirects, and no retries.
Pagination accepts only relative query locators on the configured instance.
Each operation has limits of 12 requests, 1 MiB of decoded response data,
10 pages per query, and a 20-second deadline checked around reads. Each request sets its network timeout from the remaining deadline at request
start, up to 5 seconds. This is not a hard wall-clock cancellation. Compressed
responses are rejected; requests explicitly ask for identity encoding.

| Condition | Result |
| --- | --- |
| Missing or invalid configuration | `salesforce_invalid_configuration`; CLI exits 2 before startup |
| Org edition is not Developer Edition | `salesforce_edition_required`; no CRM query |
| Upstream HTTP 401 | `salesforce_auth_required` |
| Upstream HTTP 429 or 403 with `REQUEST_LIMIT_EXCEEDED` | `salesforce_rate_limited` |
| Other upstream HTTP 403 | `salesforce_forbidden` |
| Timeout or transport failure | `salesforce_timeout` or `salesforce_unavailable` |
| Other upstream HTTP status | `salesforce_http_error` |
| Invalid payload, locator, arguments, or exceeded bound | Fixed `salesforce_*` error; no partial records |

Upstream failures are MCP tool errors (`isError: true` over HTTP 200). They do
not become local OAuth challenges. The SDK may prefix the fixed code with the
tool name. The connector's own authentication failures remain HTTP 401, scope
and disabled-write failures remain HTTP 403, and its own rate limit remains
HTTP 429 with `Retry-After`. Raw upstream messages and credentials are withheld.

## Verification and limits

```sh
.venv/bin/python -m unittest discover -s tests -p 'test_salesforce*.py' -v
```

Tests cover edition checks, field mapping, decimal preservation, hostile search
text, response limits, pagination, credential-safe errors, disabled writes,
HTTPS MCP scope enforcement, pending audit receipts, and offline defaults.
They use synthetic responses, not recordings from an org. No Salesforce token
is required. [Adapter tests](../tests/test_salesforce.py) ·
[Gateway tests](../tests/test_salesforce_gateway.py) · [CLI tests](../tests/test_salesforce_cli.py)

The local file owner remains trusted and can grant CRM access. The upstream
token may carry broader permissions; read-only behavior comes from this
adapter's GET-only interface and missing write tool. This is not delegated
Salesforce user identity. The operator must review org permissions and data
before using it. Real search terms and record references can persist in the
local audit. Retention stays manual. See [the security review](../SECURITY_REVIEW.md).

Protocol references read on 2026-11-17: [Query](https://developer.salesforce.com/docs/platform/api-rest/guide/resources-query.html),
[HTTP errors](https://developer.salesforce.com/docs/platform/api-rest/guide/errorcodes.html),
[Organization fields](https://developer.salesforce.com/docs/platform/object-reference/references/standard-objects/object-organization.html),
and [currency fields](https://developer.salesforce.com/docs/platform/object-reference/guide/field-types.html).
