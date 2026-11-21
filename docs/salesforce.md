# Read an existing Salesforce org

The Salesforce backend reads standard Account, Contact, Opportunity, and Contract records through
REST v68.0 and never writes to Salesforce. Beside Salesforce, calls and Slack are not connected
until you connect [imported transcripts](calls.md) or [live Slack](slack.md): a card says so, a
direct tool call is refused with `calls_not_connected` or `slack_not_connected`, and demo data
never stands in. Tests use invented HTTP responses and a fake `sf` executable.
No live org has been verified.

## Commands

Sign in with [`sf`](https://developer.salesforce.com/tools/salesforcecli) under an alias, then
connect a [state folder](cli.md) to it once:

```sh
sf org login web --alias gtm-de
gtm-connector debug salesforce check --sf-org gtm-de
gtm-connector connect salesforce --org gtm-de --seller 'Morgan Reed'
gtm-connector accounts --query SEARCH_TEXT
gtm-connector inspect ACCOUNT_ID
gtm-connector brief ACCOUNT_ID
gtm-connector debug serve
```

| Option | Meaning |
| --- | --- |
| `connect salesforce --org ALIAS` | Reads credentials from this signed-in `sf` alias, checks the org's identity and environment, pins them to the folder, and saves the alias and environment in the folder's `profile.json`. It starts no services and reads no record. `accounts`, `inspect`, `brief`, `debug serve`, `debug calls import`, and `debug slack map` then read that org with no further option. An alias is never a username. |
| `--environment` | `developer` (the default): OrganizationType is Developer Edition and IsSandbox is false. `sandbox`: IsSandbox is true. `production`: IsSandbox is false and OrganizationType is not Developer Edition. Missing or malformed metadata refuses. Sandbox and production also require that the four objects are queryable and expose the mapped fields. |
| `--seller NAME` | Lists a seller's name or Slack user ID for [speaker roles](briefs.md). Repeat it for each. |
| `--remove` | Detaches Salesforce, with its calls and Slack. The folder's files stay. |
| `--state-dir PATH` | The private state folder, `var/v2` by default. Give `connect` and every later command the same one. |
| `debug salesforce check --sf-org ALIAS`, `--sf-environment` | The setup check takes the alias and environment itself and reads no profile, so it runs before anything is connected. |

Account IDs are `acc-sf-` followed by the 18-character Salesforce ID. `--query` is a literal part of
the account name, at most 200 characters. `accounts` returns the first 100 matches by name and then
ID and says when more match. To connect a client, see [client setup](clients.md). To serve only
named accounts, see [selection](selection.md).

`debug salesforce check` tests the credentials, the edition or environment, the four objects'
fields, an account search, and one renewal candidate's account, opportunities, and card. A failed
step fails the steps that depend on it with `prerequisite_failed`. A failed report exits `4`.

`connect salesforce` writes the instance origin, org ID, and environment to private
`crm-connection.json` in the state folder and saves nothing when the check fails. Every later
command must match all three, or it fails with `source_identity_mismatch`. A folder that has read
demo data also refuses, so connect Salesforce in a folder of its own. One process holds a state
folder at a time, so while `debug serve` runs, connect another folder for the other commands.

## Credentials

Each command captures `sf org display` for the instance address and `sf org auth show-access-token`
for the token of the connected alias. Each command may take 120 seconds, and both share a 180-second login deadline that
is separate from each read's 20 seconds. Without `--json`, a command that waits on `sf` at startup
says so on stderr after 2 seconds and then every 10. If a token expires during a read that still
has time and requests left, the read runs `sf` again and then continues. stderr says when `sf`
starts and when it answers, and `debug serve` says so with `--json` too. An `sf` that outlasts its limit
is stopped with anything it started, and so is one still running when the connector exits. The
connector keeps the token in memory and does not launch a login flow. `debug salesforce check` without
`--sf-org` reads `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` from its environment, and
no other command does. No default CLI org is ever selected. The instance address must be an HTTPS My Domain origin ending in
`.my.salesforce.com`, with no path, query, credentials, or nonstandard port.

## Cards

A [Deal card](briefs.md) for a Salesforce account plans from the Contract with the earliest non-null
`EndDate` on or after the planning date, the UTC day the card counts from, breaking ties by
Contract ID. Its status category must be
`Draft`, `InApproval`, or `Activated`. The connector uses the `EndDate` that Salesforce returns and
never calculates one. Salesforce fills `EndDate` from `StartDate` and `ContractTerm` only when the
org's automatic calculation setting is on.

| The org returns | The card prints |
| --- | --- |
| A contract ending on or after the date | `Contract end` with the date, the days until it, and the status. A Draft or InApproval contract is called a planning candidate, not activated. |
| No such contract | `Contract end: Missing`, and a signal naming the latest earlier end date when there is one. |
| Other contracts | One `Contract` line each with status and end date, five at most. |
| An opportunity | Name, raw stage, amount, close date, closed, and won. The amount prints `(currency Missing)`, because the reader returns no currency. |
| A null industry, title, email, or amount | `Missing` |

Every card says under `NOT READ` that annual recurring revenue and renewal risk are not mapped from
standard fields.

## Field mapping

| Source | Returned fields |
| --- | --- |
| Account | `Name` to `name`; nullable `Industry` to `industry`; nullable `AccountNumber` to `account_number`. |
| Contact | `Name` to `name`; nullable `Title` and `Email` to `title` and `email`; `AccountId` to namespaced `account_id`. |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; nullable `AccountId` to namespaced `account_id`; `Amount` to a decimal string or null in `amount_decimal`; `currency` stays null, meaning unknown. |
| Contract | `AccountId` to namespaced `account_id`; raw `Status` to `status`; `StatusCode` to `status_code`; nullable `StartDate`, `EndDate`, and `ContractTerm` to `start_date`, `end_date`, and `contract_term`. |

## Limits

| Limit | Value |
| --- | --- |
| GET attempts per operation | 12, including retries. |
| Decoded response data per operation | 1 MiB. Compressed responses are rejected, and so is a 200 response labelled as anything but `application/json`. |
| Pages per query | 10. |
| Contacts, opportunities, and contracts of one account | 1,000 each. One more fails with `salesforce_result_limit`. The account, its contacts, and its contracts are one read and share its 12 requests and 1 MiB, so wide rows or many small pages can fail below 1,000. A brief prepared for a client also has to fit that tool's 4 MiB. No partial records are returned. |
| Rows of an opportunity list across accounts | 100. Excess rows fail the same way. |
| Rows of an account search | The first 100 by name and then ID. When more match, the result adds `has_more: true`, and the match count alone no longer refuses the search. The read budgets above still apply. |
| AccountNumber lookup | 2 rows. |
| Operation deadline | 20 seconds of read time, checked around reads. A wait on `sf` for a login refresh is not counted. It is not a hard cancellation. |
| Network timeout per request | The remaining deadline, up to 5 seconds. |
| Local CLI client | 25 seconds for a response, and 205 seconds for a tool call that reads the CRM, which can wait for a login refresh. |
| Retries | The first HTTP 401 in an operation asks `sf` for credentials once and confirms the org identity before retrying the GET, inside the same request and size limits. The `sf` wait has its own 180-second limit. No other upstream error is retried. |

## Errors

`salesforce_invalid_configuration` exits `2`. `salesforce_environment_mismatch` and
`source_identity_mismatch` exit `3`. Other `salesforce_*` codes exit `5`. Over MCP, an upstream failure is a tool error (`isError: true` over HTTP 200).

| Code | Cause | Fix |
| --- | --- | --- |
| `salesforce_invalid_configuration` | The instance address, token, or environment is missing or invalid. | Run `sf org login web --alias gtm-de`, then `connect salesforce --org gtm-de`. For `debug salesforce check`, pass `--sf-org gtm-de` or set both variables. |
| `salesforce_auth_required` | The `sf` login or refresh gave no usable credentials, a second 401 arrived, or the org behind the alias changed. | Sign in again with `sf`. |
| `salesforce_edition_required` | Developer mode saw another edition. | Connect a Developer Edition alias, or connect with the intended `--environment`. |
| `salesforce_environment_mismatch` | The org does not match the connected environment. | Check the alias and connect again with the intended `--environment`. |
| `salesforce_api_disabled` | API access is disabled for the org. | Use an API-enabled org and a user with the API Enabled permission. |
| `salesforce_schema_unavailable` | A required standard field is not exposed. | Run `debug salesforce check` with the same alias and environment to see the missing fields. |
| `salesforce_forbidden` | Salesforce denied the read with another HTTP 403. | Grant the user read access to the four objects and their fields. |
| `salesforce_rate_limited` | HTTP 429, or HTTP 403 with `REQUEST_LIMIT_EXCEEDED`. | Wait for API capacity and retry. |
| `salesforce_result_limit` | A read exceeded the request, page, row, or size limit. | Use a narrower search or a smaller account book. |
| `salesforce_pagination_invalid` | Salesforce returned an unsupported page locator. | Retry with a narrower search. |
| `salesforce_cli_timeout` | `sf` did not answer within its time limit. | Repeat the command. To time `sf` alone, run `time sf org display --target-org ALIAS --json > /dev/null 2>&1`. |
| `salesforce_timeout`, `salesforce_unavailable` | The deadline passed, or the endpoint could not be reached. | Check connectivity and the `sf` login, then retry. |
| `salesforce_http_error` | Another upstream HTTP status. | Check that the object is available and readable in this org. |
| `salesforce_invalid_response` | The response was outside the supported shape. | Check access to the mapped fields, then retry. |
| `salesforce_invalid_arguments` | An account ID, search text, or date is invalid. | Use an `acc-sf-` ID, a shorter query, or a `YYYY-MM-DD` date. |
| `salesforce_account_number_ambiguous` | An AccountNumber lookup returned a number that is not a literal match. `debug calls import` reports `calls_account_ambiguous`. | Select the account with `--account` and its canonical ID. |
| `source_identity_mismatch` | The state folder belongs to another connection or cannot prove its identity. | Connect the original org again, or connect in an unused `--state-dir`. |
| `renewal_fields_unmapped` | `debug salesforce check` found no account with a Contract `EndDate` on or after the planning date. Cards still work and say so. | Create a Draft Contract with `StartDate` and `ContractTerm`, or verify read access to `EndDate`. |
| `crm_write_disabled` | A client attempted a write. | Do not retry. The Salesforce backend is read-only. |
