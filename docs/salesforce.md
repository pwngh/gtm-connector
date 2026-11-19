# Read an existing Salesforce org

The Salesforce backend reads standard Account, Contact, Opportunity, and Contract records through
REST v68.0 and never writes to Salesforce. Call-note and Slack reads for its accounts return no
records unless you select [imported transcripts](calls.md), [live Slack](slack-live.md), or the
[demo kit](salesforce-demo.md). Tests use invented HTTP responses and a fake `sf` executable.
No live org has been verified.

## Commands

Sign in with [`sf`](https://developer.salesforce.com/tools/salesforcecli) under an alias:

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
| `--crm-backend salesforce` | Selects Salesforce for `accounts`, `account`, `brief`, and `serve`. `--sf-org` and `--sf-environment` require it. Environment variables alone never select Salesforce. |
| `--sf-org ALIAS` | Reads credentials from this signed-in `sf` alias. |
| `--sf-environment` | `developer` (the default): OrganizationType is Developer Edition and IsSandbox is false. `sandbox`: IsSandbox is true. `production`: IsSandbox is false and OrganizationType is not Developer Edition. Missing or malformed metadata refuses. Sandbox and production also require that the four objects are queryable and expose the mapped fields. |
| `--state-dir PATH` | Private state folder. `accounts` and `account` default to `var/salesforce`. `brief` and `serve` default to `var/v2`, which fixture commands also use, so pass a separate folder. `--db` cannot be combined with Salesforce. |

Account IDs are `acc-sf-` followed by the 18-character Salesforce ID. `--query` is a literal part of
the account name, at most 200 characters, and `accounts` returns at most 100 rows. To connect a
client, see [client setup](clients.md). To serve only named accounts, see [selection](selection.md).

`salesforce check` tests the credentials, the edition or environment, the four objects' fields, an
account search, and one renewal candidate's account, opportunities, and brief. A failed step fails
the steps that depend on it with `prerequisite_failed`. A failed report exits `4`.

Startup writes the instance origin, org ID, and environment to private `crm-connection.json` in the
state folder. A later run must match all three, or it fails with `source_identity_mismatch`. A
folder that holds fixture state also refuses. One process holds a state folder at a time, so while
`serve` runs, give the other commands another folder.

## Credentials

`--sf-org` captures `sf org display` for the instance address and `sf org auth show-access-token`
for the token. Each command has a timeout of at most 10 seconds within one 20-second credential
deadline. The connector keeps the token in memory and does not launch a login flow. Without
`--sf-org`, the backend reads `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` from its
environment. This fallback cannot refresh an expired token, so replace the variables and restart. No
default CLI org is ever selected. The instance address must be an HTTPS My Domain origin ending in
`.my.salesforce.com`, with no path, query, credentials, or nonstandard port.

## Briefs

A brief selects the Contract with the earliest non-null `EndDate` on or after the planning date,
breaking ties by Contract ID. Its status category must be `Draft`, `InApproval`, or `Activated`. The
connector uses the `EndDate` that Salesforce returns and never calculates one. Salesforce fills
`EndDate` from `StartDate` and `ContractTerm` only when the org's automatic calculation setting is
on. Every brief states the contract's status, category, and end date, that a Draft or InApproval
contract has not been activated, and that ARR and CRM renewal risk are not mapped from standard
fields. Amounts are printed with `(currency unknown)`.

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
| Decoded response data per operation | 1 MiB. Compressed responses are rejected. |
| Pages per query | 10. |
| Rows per query | 100. Excess rows fail and no partial records are returned. |
| AccountNumber lookup | 2 rows. |
| Operation deadline | 20 seconds, checked around reads. It is not a hard cancellation. |
| Network timeout per request | The remaining deadline, up to 5 seconds. |
| Local CLI client | 25 seconds for a Salesforce MCP response. |
| Retries | With `--sf-org`, the first HTTP 401 in an operation asks `sf` for credentials once and confirms the org identity before retrying the GET, inside the same budget. No other upstream error is retried. |

## Errors

`salesforce_invalid_configuration` exits `2`. `salesforce_environment_mismatch`,
`source_identity_mismatch`, and `renewal_fields_unmapped` exit `3`. Other `salesforce_*` codes exit
`5`. Over MCP, an upstream failure is a tool error (`isError: true` over HTTP 200).

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

## Demo book

The kit imports three invented accounts into an existing Developer Edition org. The connector
reads those CRM records and joins invented local call notes and Slack messages to them by
AccountNumber. It stays GET-only. The import and removal use your Salesforce CLI permissions.
The kit is tested with invented responses and a fake CLI.

### Import and read a brief

Run these from the checkout root with an existing `sf` login.

```sh
umask 077
.venv/bin/python scripts/salesforce-demo.py prepare --output var/salesforce-demo/kit
sf data import tree --target-org gtm-de --plan var/salesforce-demo/kit/plan.json --json > var/salesforce-demo/import.json
.venv/bin/python scripts/salesforce-demo.py bind --receipt var/salesforce-demo/import.json --output var/salesforce-demo/binding.json
.venv/bin/python scripts/salesforce-demo.py brief --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

- Use an empty Developer Edition demo org with no customer data or automation.
- Keep the receipt and binding until removal finishes. AccountNumber is not unique across an org,
  so it cannot recover a lost receipt.
- The import creates twelve records: three Accounts, each with one Contact, one Opportunity, and
  one Draft Contract. The file supplies no EndDate. Salesforce computes it from StartDate and
  ContractTerm, and no contract is activated.

| Helper action | Options | Result |
| --- | --- | --- |
| `prepare` | `--output NEW_FOLDER` | Writes `plan.json`, `accounts.json`, and `manifest.json`. |
| `bind` | `--receipt FILE --output FILE` | Checks the complete receipt against the packaged manifest and writes a private binding of the twelve IDs. |
| `brief` | `--sf-org`, `--binding`, optional `--account-number` (`GTM-DEMO-001` by default, `GTM-DEMO-002`, or `GTM-DEMO-003`), `--state-dir` (default `var/salesforce-demo/run`), `--json` | Runs the ordinary `brief` with `--with-slack` and the kit's planning date, `2027-01-15`. |
| `remove` | `--sf-org`, `--binding` | Deletes the imported records. |

### Use the MCP server

```sh
gtm-connector serve --crm-backend salesforce --sf-org gtm-de --salesforce-demo-binding var/salesforce-demo/binding.json --state-dir var/salesforce-demo/server
```

- The first bound run needs an empty state folder. A restart must use the same binding and
  connection. Use a fresh `--state-dir` for a new import.
- `--salesforce-demo-binding` requires `--crm-backend salesforce`. It cannot be combined with
  `--selection`, `--calls-backend imported`, or `--slack-backend slack`.
- The binding adds no scope and does not prove which org produced the receipt.

### Remove the imported book

Stop the demo server and stop editing the book, then run:

```sh
.venv/bin/python scripts/salesforce-demo.py remove --sf-org gtm-de --binding var/salesforce-demo/binding.json
```

A failed check stops the helper before any delete: the org must be Developer Edition, every kit
field must still have its original value, the contracts must still be Draft, and cascading and
restricted child relationships may hold only kit records, system history rows, and Owner sharing
rows. Only the exact imported IDs are deleted, Accounts last, and a retry skips records that are
already gone. A helper error exits `3`. One removal stops after 80 `sf` commands or 240 seconds. The
checks and deletes are not a transaction, so a late failure can leave some records removed. Removal
does not erase local files under `var/`.
