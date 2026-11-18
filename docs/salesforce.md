# Read a Salesforce Developer Edition org

This option reads accounts, contacts, and opportunities from a Developer Edition
org, a free Salesforce development org you supply. The three CRM read tools use Salesforce
REST v68.0; call notes and Slack keep their isolated SQLite stores.

Default demos, evaluation, and model benchmarks still use invented records.
Adapter tests use invented HTTP responses, with no live org result or production
compatibility claimed.

## Prepare an org yourself

Your Developer Edition org needs invented data and API access. Salesforce
provides a [free Developer Edition signup](https://developer.salesforce.com/docs/platform/api-rest/guide/quickstart-dev-org.html).
The connector creates no org, user, application, or key. Do not use a production
org or employer/client data. Before querying CRM records, every operation checks
`OrganizationType` for exactly `Developer Edition`. Other editions fail.

Setup requires the current [Salesforce CLI](https://developer.salesforce.com/tools/salesforcecli).
The command authenticates your org under a separate alias:

```sh
sf org login web --alias gtm-de
```

The server reads `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` from its
terminal environment. These commands capture the values privately, without
printing the credential or placing its value in shell history:

```sh
export SALESFORCE_INSTANCE_URL="$(sf org display --target-org gtm-de --json | .venv/bin/python -c 'import json,sys; print(json.load(sys.stdin)["result"]["instanceUrl"])')"
export SALESFORCE_ACCESS_TOKEN="$(sf org auth show-access-token --target-org gtm-de --json | .venv/bin/python -c 'import json,sys; print(json.load(sys.stdin)["result"]["accessToken"])')"
```

Current CLI versions redact the token in `org display`; use the explicit
[show-access-token command](https://developer.salesforce.com/docs/platform/salesforce-cli-reference/guide/cli_reference_org_auth_show-access-token.html).
Keep shell tracing off and do not paste the credential into a tool argument.

The connector accepts an HTTPS My Domain origin ending in `.my.salesforce.com`,
with no path, query, credentials, or nonstandard port. A login URL such as
`login.salesforce.com` is not an instance URL; use the CLI's `instanceUrl`.
No credential file loads automatically.

## Start and read

The server runs after the Python dependencies are installed:

```sh
.venv/bin/python -m gtm_connector serve --crm-backend salesforce --state-dir var/salesforce
```

Startup validates configuration locally. The first authorized CRM tool checks
the edition and queries data. Environment variables alone never activate the
adapter; without a backend flag, `serve` uses SQLite.

A second terminal uses the [Inspector setup](inspector.md) with
`var/salesforce` in place of `var/inspector`. Grant `crm:read`. Set that state's
certificate in `NODE_EXTRA_CA_CERTS`. Search first:

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/salesforce/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name search_accounts
```

Subsequent reads take an actual returned `id` as `account_id` for `get_account`
or `list_opportunities` through the same Inspector command. Account IDs use
`acc-sf-` followed by the 18-character Salesforce ID. Do not use the fixture's
`acc-003`.

Search returns at most 100 accounts; refine the query after
`salesforce_result_limit`. Opportunity and contact queries also reject results
over 100 rows rather than return partial answers.

The external backend lists five read tools; the write tool `log_activity` is absent.
A direct write attempt with a valid write grant returns HTTP 403 and `crm_write_disabled`,
creating no activity locally or remotely. Mock call notes and Slack have no
ID mapping from external accounts to fixture notes or messages.

Ctrl-C stops the server. This command then removes the variables from that terminal:

```sh
unset SALESFORCE_INSTANCE_URL SALESFORCE_ACCESS_TOKEN
```

The adapter does not refresh tokens. Obtain a new access token and restart when
needed. Local MCP tokens have a separate five-minute expiry.

## Field mapping

Results carry `source: crm`, `untrusted_data: true`, and a `record_ref` under
`crm/accounts/`, `crm/contacts/`, or `crm/opportunities/`. Each row has
`backend: salesforce`, its native `source_id`, and an `id` prefixed with
`acc-sf-`, `con-sf-`, or `opp-sf-`. Nullable mapped fields preserve null values.

| Source | Returned fields |
| --- | --- |
| Account | `Name` to `name`; `Industry` to `industry` |
| Contact | `Name` to `name`; `Title` to `title`; `Email` to `email`; `AccountId` to namespaced `account_id` |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; `AccountId` to namespaced `account_id` or null |
| Opportunity amount | `Amount` to `amount_decimal`, a decimal string or null; `currency` is null, meaning unknown |

`CurrencyIsoCode` is unavailable in some orgs. The adapter always leaves currency unknown.
The adapter does not treat `Account.AnnualRevenue` as ARR or `CloseDate` as a
renewal date. It preserves custom stages and invents no renewal risk.

Those missing mappings prevent renewal briefs from this backend. This command
exits `3` with `renewal_fields_unmapped`, before reading data or invoking a model:

```sh
.venv/bin/python -m gtm_connector brief acc-sf-REPLACE_WITH_RETURNED_ID --crm-backend salesforce
```

The refusal writes its reason and a next command to stderr, leaving stdout
empty. Add `--json` for a structured error with `error.code: "refused"`.
Missing or invalid Salesforce configuration remains a usage error with exit `2`.
See [the CLI contract](cli.md).

The local `seed`, `accounts`, and `account` commands remain direct SQLite
administration. They never query or modify Salesforce. The runtime initializes
its fixture files but does not copy org records into them.

## Request and failure boundary

Before tool execution, the gateway commits a pending receipt, checks the bearer
grant, and applies its rate limit. Missing `crm:read` returns HTTP 403 without
contacting the org. Initial audit failure returns HTTP 503 without an upstream
request either.

Successful reads and failed queries finalize the same receipt. Audit rows keep
returned references, not record bodies. Arguments retain search terms and
supplied IDs.

The adapter sends GET requests with fixed field lists and escaped search text.
TLS verification stays on. It uses no proxies, follows no redirects, and makes
no retries. Pagination accepts only relative query locators on the configured
instance.

Each operation allows 12 requests and 1 MiB of decoded response data, with
10 pages per query. A 20-second deadline is checked around reads. Each request's
network timeout uses the remaining deadline at request start, up to 5 seconds.
This is not hard wall-clock cancellation. Requests ask for identity encoding;
compressed responses are rejected.

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

An upstream failure arrives as an MCP tool error (`isError: true` over HTTP 200),
not a local OAuth challenge. The SDK may prefix the fixed code with the tool
name. Raw upstream messages and credentials are withheld.

The connector keeps its own failure responses: authentication returns HTTP 401,
scope and disabled-write failures return HTTP 403, and the local rate limit
returns HTTP 429 with `Retry-After`.

## Verification and limits

```sh
.venv/bin/python -m unittest discover -s tests -p 'test_salesforce*.py' -v
```

Tests check editions and field mapping, including decimal preservation. Hostile
search text, pagination, and response limits exercise the request boundary.
Other cases cover credential-safe errors and disabled writes, HTTPS MCP scope
enforcement, pending audit receipts, and offline defaults.

Responses are synthetic, not recorded from an org; no Salesforce token is
required. [Adapter tests](../tests/test_salesforce.py),
[Gateway tests](../tests/test_salesforce_gateway.py), and [CLI tests](../tests/test_salesforce_cli.py)
cover these cases.

The trusted local file owner can grant CRM access. Read-only behavior comes
from the adapter's GET-only interface and absent write tool; the upstream token
may have broader permissions. This is not delegated Salesforce user identity.

Operators must review org permissions and data before use. Real search terms and record
references can persist in the local audit. Retention remains manual. See
[the security review](../SECURITY_REVIEW.md).

This guide uses protocol references read on 2026-11-17: [Query](https://developer.salesforce.com/docs/platform/api-rest/guide/resources-query.html),
[HTTP errors](https://developer.salesforce.com/docs/platform/api-rest/guide/errorcodes.html),
[Organization fields](https://developer.salesforce.com/docs/platform/object-reference/references/standard-objects/object-organization.html),
and [currency fields](https://developer.salesforce.com/docs/platform/object-reference/guide/field-types.html).
