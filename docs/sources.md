# Sources

**A Deal card reads three sources: a CRM, call transcripts and Slack. Each one is connected to a
workspace with `connect`, to the invented demo book or to a real source.**

A workspace is a named set of connections and what was read through them (see [the CLI](cli.md)).
The demo book is invented data built into the binary. Salesforce, imported transcripts and Slack are
real sources, and each is connected by hand.

```sh
gtm-connector connect demo --state-dir renewals
gtm-connector connect calls --state-dir renewals --seller 'Morgan Reed'
gtm-connector debug calls import meeting.vtt --account acc-007 --state-dir renewals
gtm-connector brief acc-007 --state-dir renewals
gtm-connector connect --state-dir renewals
```

The last command shows what the workspace reads:

```text
crm	connected	demo data
calls	connected	imported transcripts
slack	connected	demo data
Sellers: "Morgan Reed"
```

| Source | Reads | Connect with |
| --- | --- | --- |
| [Demo data](#demo-data) | The invented book: 30 accounts with their contacts, opportunities, call notes and Slack messages | `connect demo` |
| [Salesforce](#salesforce) | Account, Contact, Opportunity and Contract records of one org, and its Standard users for speaker roles, through an `sf` login or a token handed to the container | `connect salesforce --org ALIAS --state-dir NAME`, or `--token-env` |
| [Call transcripts](#call-transcripts) | TXT, WebVTT and SRT files imported one at a time, one excerpt per account | `connect calls`, then `debug calls import` |
| [Slack](#slack) | One mapped thread per account, read live | `debug slack connect`, `connect slack`, then `debug slack map` |

## How a workspace holds its sources

A workspace has one connection for each of `crm`, `calls` and `slack`. A connection names the kind of
source it reads and what the operator chose, such as an `sf` alias. It holds no token, address or
organization ID.

```text
workspace ──▶ crm    (demo data | Salesforce)
          ├─▶ calls  (demo data | imported transcripts | not connected)
          └─▶ slack  (demo data | the saved Slack sign-in | not connected)
```

Calls and Slack belong to their CRM. `connect calls` and `connect slack` need a CRM first, and
removing the CRM detaches both. A command that reads a source opens it then, and a source that signs in
does so on its first read.

> **Demo data and real sources never share a workspace.** A workspace is made for one or the other
> and keeps that. Imported transcripts are the one exception: they may sit beside the demo CRM. Demo
> data is never read in place of a real source that is missing or failing.

> **A workspace keeps the identity it was first connected to.** Salesforce is pinned to the org,
> its environment and the user the login reads as, and Slack to the signed-in user. Connecting
> another identity is refused with exit 3, also after `--remove`. Connect it in a workspace of its
> own.

A read goes through the connection as it was when the command started. If the connection is
detached or connected again meanwhile, the read's receipt is finished as `source_changed` (HTTP 409)
and nothing is read. Receipts are described in [audit](audit.md).

### `connect`

`connect NAME` attaches one source, `connect NAME --remove` detaches it, and `connect` alone (or
`connect status`) shows what is connected and the seller list. Removing a source keeps its cards and
receipts.

| Option | Meaning |
| --- | --- |
| `--state-dir NAME` | The workspace, `demo` by default. Give every later command the same name. |
| `--seller NAME` | With `salesforce`, `calls` or `slack`: a seller's name or Slack user ID, for [speaker roles](cards.md#speaker-roles). Repeat it for each, at most 200, each of 1 to 200 characters. The list replaces the saved one. `connect demo` does not take it. |
| `--alias [CONTENT_SHA256:]LABEL=TARGET` | With `salesforce`, `calls` or `slack`: maps a speaker label to a person, an email address or a seller entry. See [the CLI](cli.md#speaker-settings). |
| `--company-domain DOMAIN` | With `salesforce`, `calls` or `slack`: one of your company's own mail domains. See [the CLI](cli.md#speaker-settings). |
| `--remove` | Detaches the source and takes no other option. Removing the CRM removes its calls and Slack. |
| `--org`, `--environment` | With `salesforce`. See [Salesforce](#salesforce). |

The result names the provider, a `status` of `connected`, `removed` or `not_connected`, and each
connected source. [The CLI guide](cli.md) lists its JSON keys and every exit code.

## Demo data

`connect demo` connects all three sources to the demo book. It uses no account, token or network
request, and every company, person and message in it is invented, with `.example` email domains.

```sh
gtm-connector connect demo --state-dir renewal-demo
gtm-connector accounts --query 'alder vale' --state-dir renewal-demo
gtm-connector brief acc-003 --state-dir renewal-demo
```

A card on the demo book is planned from the fixed demo date, `2027-01-15`, unless `--as-of` names
another. On demo data the book's own sellers (Morgan Reed, Quinn Moss and Avery Linden) always count
as sellers, beside the workspace's seller list. `demo --state-dir NAME` connects the book and opens acc-003; [the
CLI guide](cli.md) describes `demo` and its output.

| The book holds | Count |
| --- | ---: |
| Accounts, each with a renewal date, annual recurring revenue in a currency, and a risk status | 30 |
| Contacts | 30 |
| Opportunities, each amount with a currency | 30 |
| Call notes | 30 |
| Slack messages, `msg-001` to `msg-030` without `msg-005`, each with an author and a count of attached files | 29 |

Some records are written to show one property each:

| Record | Case | What the card shows |
| --- | --- | --- |
| `acc-001` | Alder Vale Packaging renews on `2027-02-01` | `Renewal date: 2027-02-01, in 17 days` |
| `acc-002` | Copper Finch Distribution is at risk | `CRM marks this account at risk.` under ATTENTION, and `CRM risk: at risk; reason "Renewal budget is not approved."` |
| `acc-003` | Thornfield Wren LLP, the account `demo` opens. Its opportunity is in negotiation for USD 450,000.00. | The renewal in 24 days, at stage "negotiation" |
| `note-003`, `note-003b` | One call of acc-003. Morgan Reed promises a date, and the contact Marian Holt sets a condition. | Morgan Reed as Seller, Marian Holt as Customer |
| `msg-003` | On acc-003, a seller's report with a channel mention, a link, a second-hand amount and one attached file | Three seller sentences under SAID. The mention and the link are neutralized, the amount stays inside its sentence, and NOT READ counts the file. |
| `acc-004` | Willow Reach Clinics has no call notes | `Calls: no call record was returned for this account.` |
| `acc-005` | Bracken Ridge Components has no Slack messages | `Slack: no message was returned for this account.` |
| `acc-006` | An October call note and a January Slack message disagree on seats | Both are quoted. Only the note's age is a signal. |
| `acc-007` | Juniper Crest Learning renews on `2027-01-29`. Its contact, Dana Crest, has no role. | `This contact has no role recorded.` |
| `note-007`, `msg-007` | Each asks for a call to `log_activity` | The request is quoted as sentences like any other. No tool writes, and a client's call to `log_activity` is refused with `crm_write_disabled` (see [MCP](mcp.md)). |

`msg-003` reads on the card as:

```text
    [7] Slack 2027-01-12T16:00:00Z; "Avery Linden"
        "Heads up [mention channel]: their general counsel will not sign until the zero-retention and client-consent rider is approved."
    [8] Slack 2027-01-12T16:00:00Z; "Avery Linden"
        "Their procurement lead mentioned USD 300,000 for a smaller seat count."
```

In a workspace connected to the demo book, `accounts` and `inspect ACCOUNT_ID` read the book
directly, as no tool call and under no receipt. The account view holds the account and its contacts,
and leaves out opportunities and call notes.

> **Demo data is read only beside the demo CRM.** A connection that names demo calls or demo Slack
> beside Salesforce is refused with exit 3, and nothing is read.

## Salesforce

**Read the standard Account, Contact, Opportunity and Contract records of one org through REST
v68.0, with the login of an existing `sf` alias. The connector never writes to Salesforce.**

Sign in with [`sf`](https://developer.salesforce.com/tools/salesforcecli) under an alias, then
connect a workspace of its own to that org. The default workspace holds demo data, so name one with
`--state-dir` and give every later command the same name.

```sh
sf org login web --alias acme-dev
gtm-connector debug salesforce check --sf-org acme-dev --state-dir acme-dev
gtm-connector connect salesforce --org acme-dev --state-dir acme-dev --seller 'Morgan Reed'
gtm-connector debug salesforce check --state-dir acme-dev
gtm-connector accounts --query SEARCH_TEXT --state-dir acme-dev
gtm-connector brief ACCOUNT_ID --state-dir acme-dev
gtm-connector mcp stdio --scope crm:read --state-dir acme-dev
```

The tests of this source run against an invented org and an invented `sf`. None of them reads a live
org.

### Connect an org

`connect salesforce --org ALIAS` reads the alias's login through `sf`, checks the org against
`--environment`, and pins the workspace to the org's address, ID and environment and to the user the
login reads as. It saves the alias, the environment and a digest of that identity, never the token,
address, org ID or user. It reads no record and starts no service.

```text
crm	connected	Salesforce (developer), identity verified
calls	not connected
slack	not connected
```

- `--org` takes an `sf` alias only: 1 to 64 letters, digits, dots, hyphens or underscores, starting
  with a letter or digit. Anything else, such as a username or a token, is refused with exit 2 before
  `sf` runs, and is not repeated in the error.
- The alias is saved only once `sf org display` names a login under it. A login under no alias is
  `salesforce_auth_required`.
- `connect salesforce` without `--state-dir`, or in a workspace that holds demo data, is refused.
- A workspace connected to Salesforce reads it in `accounts`, `inspect`, `brief`, `mcp stdio`,
  `serve`'s `/mcp`, `debug calls import` and `debug slack map` with no further option.
- `mcp stdio --state-dir NAME` and `serve`'s `/mcp` serve the workspace's CRM to MCP clients. `serve`
  answers each token in the workspace it was issued in. See [MCP](mcp.md).

| `--environment` | The org must report |
| --- | --- |
| `developer` (the default) | `OrganizationType` is `Developer Edition` and `IsSandbox` is false |
| `sandbox` | `IsSandbox` is true |
| `production` | `IsSandbox` is false and `OrganizationType` is not `Developer Edition` |

Outside Developer Edition, connect also checks that the four objects are queryable and expose every
mapped field. Missing or malformed organization metadata is refused.

Where there is no `sf`, as in a container, connect with `--token-env` instead: see
[a token handed to the container](#without-sf-a-token-handed-to-the-container).

### Sign-in and reads

Each command reads the alias's login on its first read: `sf org display` for the instance address
and `sf org auth show-access-token` for the token. The token stays in memory. The org and the user
are compared with the workspace's pin before any object is described or any record read.

> **No default `sf` org is ever selected, and no login flow is started.** `sf` runs with no input,
> its error output discarded, none of the connector's settings or credentials in its environment,
> and telemetry, updates and log files off. A command that is cancelled
> ends `sf` and everything it started.

The instance address must be an HTTPS My Domain origin ending in `.my.salesforce.com`, with no path,
query, credentials or port other than 443. The token must be 20 to 4,096 printable ASCII characters
with no space. A login that breaks either rule is `salesforce_auth_required`.

Every read is one bounded operation, listed under [Limits](#salesforce-limits). The first HTTP 401 in
an operation asks `sf` for the login once more, confirms it is the same org and user, and retries the
GET inside the same limits. A refreshed login for another org or user is refused. No other upstream
error is retried.

An answer that holds the token is refused whole, so a token never becomes a record. In a workspace,
`accounts` and `inspect` read the org as the `search_accounts` and `get_account` tools, each under a
receipt committed before the read, which `audit` lists.

### Without `sf`: a token handed to the container

A container has no `sf`. Give the connector the org's address and an access token instead, and
connect with `--token-env`:

| Variable | Holds |
| --- | --- |
| `SALESFORCE_INSTANCE_URL` | The org's HTTPS My Domain origin, `https://NAME.my.salesforce.com`. |
| `SALESFORCE_ACCESS_TOKEN_FILE` | The path of a file that holds the token on one line. |
| `SALESFORCE_ACCESS_TOKEN` | The token itself, in place of the file. Set one of the two, never both. |

- `connect salesforce --token-env --state-dir NAME` makes the checks `--org` makes, with the same
  `--environment` and `--seller`. It pins the workspace to the identity an `sf` login of that org and
  user would get. The row holds `{"credentials":"environment","environment":...}`: no token, address,
  org ID or user.
- Each command reads the variables on its first read. Missing or invalid variables fail with
  `salesforce_invalid_configuration` and exit 2, in one fixed line that names the variables and never
  their values.
- The token file must be a regular file that only its owner can read (`chmod 600`). A file the group
  or others can read is refused, and the refusal gives its mode. The file may be a symbolic link, as
  an orchestrator's mounted secret is, when it leads to a regular file in a folder that others cannot
  write to. A named pipe, or anything else, is refused at once and never waited on.
- When the org refuses the token (HTTP 401), the read reads the variables and the file once more. This
  also happens when the token expires between the first two checks of a command. A new token of the
  same org and user is checked and kept, and the read goes on. In every other case the read fails with
  `salesforce_auth_required` and exit 5: `The token expired or was refused; give the container a new
  one of the same org and user.` That covers the same token, a token of another org or user, and
  variables that are now missing or invalid. The connector never asks for a token itself.
- A running `serve` sees a new token only through the file, because the variables of a process do not
  change while it runs. A call in flight while the file is replaced can fail, and the next call reads
  the new file. A token the workspace's pin refused is let go, so once the file holds a token of the
  pinned user again, the next call reads it.
- `sf` is never given the connector's settings or credentials: `GTM_*`, `PG*`, the three variables
  above and any `*_TOKEN` or `*_TOKEN_FILE` are taken out of its environment.

In Docker Compose, keep the token in a folder of its own, outside the repository and outside
`GTM_HOME` so that no backup holds it. Write it without showing it on screen:

```sh
mkdir -p ~/.config/gtm-connector/salesforce && chmod 700 ~/.config/gtm-connector/salesforce
# Type or paste the token at the prompt; it is not shown. Then press Return.
(umask 077 && IFS= read -rs TOKEN && printf '%s\n' "$TOKEN" > ~/.config/gtm-connector/salesforce/token)
```

On macOS you can write it from the clipboard instead, and then clear the clipboard:

```sh
(umask 077 && pbpaste > ~/.config/gtm-connector/salesforce/token) && pbcopy < /dev/null
```

On Linux, `xclip -selection clipboard -o` takes the place of `pbpaste`, and
`xclip -selection clipboard < /dev/null` clears the clipboard. Mount the folder read-only with a
Compose file of your own, also kept outside the repository:

```yaml
# ~/.config/gtm-connector/salesforce.yaml
services:
  app: &salesforce
    environment:
      SALESFORCE_INSTANCE_URL: https://NAME.my.salesforce.com
      SALESFORCE_ACCESS_TOKEN_FILE: /run/salesforce/token
    volumes:
      - ${HOME}/.config/gtm-connector/salesforce:/run/salesforce:ro
  admin: *salesforce
```

```sh
export COMPOSE_FILE=compose.yaml:$HOME/.config/gtm-connector/salesforce.yaml
docker compose run --rm admin connect salesforce --token-env --environment production --state-dir acme
docker compose up -d app
```

- The token is never in `compose.yaml`, `.env`, the image or the container's environment.
  `docker inspect` shows the file's path and the mounted folder, not the token.
- To rotate the token, write the new one to a file in the same folder in the same way, then rename
  it over `token` (`mv token.new token`). The folder is mounted rather than the file, so the running
  container sees the new file, and its next HTTP 401 takes it.
- `SALESFORCE_ACCESS_TOKEN` is for a process started by hand. In a container it shows in
  `docker inspect`, so Compose uses the file.

### Records

Account IDs are `acc-sf-` followed by the 18-character Salesforce ID. Contacts, opportunities,
contracts and users are `con-sf-`, `opp-sf-`, `ctr-sf-` and `usr-sf-` the same way.

| Object | Returned fields |
| --- | --- |
| Account | `Name` to `name`; nullable `Industry` to `industry`; nullable `AccountNumber` to `account_number` |
| Contact | `Name` to `name`; nullable `Title` and `Email` to `title` and `email`; `AccountId` to `account_id` |
| Opportunity | `Name` to `name`; raw `StageName` to `stage`; `CloseDate` to `close_date`; `IsClosed` and `IsWon` to `is_closed` and `is_won`; nullable `AccountId` to `account_id`; `Amount` to a decimal string or null in `amount_decimal`; `currency` is always null, meaning unknown |
| Contract | `AccountId` to `account_id`; raw `Status` to `status`; `StatusCode` to `status_code`, one of `Draft`, `InApproval` or `Activated`; nullable `StartDate`, `EndDate` and `ContractTerm` to `start_date`, `end_date` and `contract_term` |
| User | `Name` to `name`; nullable `Email` to `email`; `IsActive` to `is_active`. Only users whose `UserType` is `Standard` are read. |

`accounts --query TEXT` finds the accounts whose name holds the words of the text in their order, each
literally, with anything between them. It returns the first 100 by name and then ID, and says when
more match.

### What a card reads

A [Deal card](cards.md) for a Salesforce account plans from the Contract with the earliest non-null
`EndDate` on or after the planning date, ties broken by Contract ID. Its status category must be
`Draft`, `InApproval` or `Activated`. Without `--as-of`, the planning date is today in UTC.

> **The connector uses the `EndDate` that Salesforce returns and never calculates one.** Salesforce
> fills `EndDate` from `StartDate` and `ContractTerm` only when the org's automatic calculation
> setting is on.

| The org returns | The card prints |
| --- | --- |
| A contract ending on or after the planning date | `Contract end` with the date, the days until it, and the status. A Draft or InApproval contract is called `a planning candidate, not activated`. |
| No such contract | `Contract end: Missing: no contract ends on or after DATE`, and a signal naming the latest earlier end date when there is one |
| Other contracts | One `Contract` line each with its status and end date, five at most |
| An opportunity | Name, stage, amount, close date and outcome. The amount prints `(currency Missing)`. |

A Salesforce card also reads the org's users, as the `list_users` read under a `crm:read` receipt of
its own: `SELECT Id, Name, Email, IsActive FROM User WHERE UserType = 'Standard'`, every page of it. A
user is a seller for [speaker roles](cards.md#speaker-roles), active or not, since a rep who has left
still speaks on an old call; a user is matched by user ID and email, and an active user's name only
within a call's attendee list. An org with more than 50,000 Standard users has none read: the read
finishes as an error with no rows, and the card is made without them and says so under NOT READ
(`CRM: the org's users were not read: ...`). The account's own read includes its `Website`, which
names the account's domain for speaker roles. A portal or community user is never read, because one can be on
the customer's side. The read binds every user it returned, and the card shows none of them; a
sentence whose speaker was matched to a user names that user's record, which `inspect BRIEF_ID N`
traces to the `list_users` receipt.

Every Salesforce card says under NOT READ that annual recurring revenue and renewal risk are not
mapped from standard Salesforce fields. Beside Salesforce, calls and Slack read nothing until
transcripts or Slack are connected: the card says `Calls: not connected.` and `Slack: not
connected.`, and a direct tool call is refused with `calls_not_connected` or `slack_not_connected`.

### `debug salesforce check`

`debug salesforce check` reports, step by step, what a connection needs. It holds counts, codes and
fixed text, never a record, name, ID or credential.

| Form | What it checks |
| --- | --- |
| `--sf-org ALIAS --state-dir NAME` | The sign-in, the environment and the four objects' fields, before anything is connected. It reads no record. A workspace that does not exist is made, for real sources and unconnected. |
| `--state-dir NAME` | The org a workspace is connected to: the same steps, then an account search, the contract a renewal plans from, and that account's account, contact, contract and opportunity reads |

Every request a step makes to `sf` or the org runs under a receipt committed first, in the named
workspace. A step whose earlier step failed is not run and reports `prerequisite_failed`. A failed
check prints its report, then fails with exit 4. As text, the report is a table with the columns
`STEP`, `STATUS`, `COUNT`, `MS`, `API GETS`, `SF COMMANDS`, `DETAIL` and `FIX`, and stderr names each
step as it runs.

| Option | Meaning |
| --- | --- |
| `--sf-org ALIAS` | Checks this alias before it is connected. It needs `--state-dir`, and is refused in a workspace that holds demo data or whose CRM is already connected. |
| `--sf-environment` | With `--sf-org` only: `developer` (the default), `sandbox` or `production`. A connected workspace is checked in the environment it was connected with. |
| `--as-of YYYY-MM-DD` | The planning date of the renewal candidate, today in UTC by default |
| `--json` | The report as JSON, with progress lines on stderr |

The steps are `credentials`, then `edition` (developer) or `environment`, then `account_fields`,
`contact_fields`, `opportunity_fields` and `contract_fields`. A connected workspace adds
`account_search`, `renewal_candidate`, `account_read` and `opportunity_read`. Each step has `name`,
`status`, `count`, `code`, `message`, `fix`, `duration_ms`, `api_calls` and `sf_cli_invocations`.

The report holds `schema_version`, `status` (`passed` or `failed`), `as_of`, `steps`, and the totals
`api_calls` (the connector's GET attempts, retries included) and `sf_cli_invocations` (every `sf` run
attempted). A step's `code` is `ok`, `missing_fields`, `renewal_fields_unmapped`,
`prerequisite_failed`, `check_failed` or one of the codes under [Errors](#salesforce-errors).

### Salesforce limits

| Limit | Value |
| --- | --- |
| GET attempts per operation | 12, retries included |
| Response data per operation | 1 MiB. A compressed response is refused, and so is a 200 response labelled as anything but `application/json`. |
| Pages per query | 10. A tenth page that is not the last is refused. |
| Standard users of the org (`list_users`) | 50,000, read in pages under a budget of their own. More and no user is read; the card is made without them and says so. |
| Contacts, opportunities and contracts of one account | 1,000 each. One more fails with `salesforce_result_limit`. The account, its contacts and its contracts are one read and share its 12 requests and 1 MiB, so wide rows or many pages can fail below 1,000. No partial records are returned. |
| Opportunities listed across accounts (`list_opportunities` with no account) | 100 |
| `--query` text | 200 characters, with no control characters |
| Rows of an account search | The first 100 by name and then ID, and whether more match |
| An `AccountNumber` lookup (`search_accounts` over MCP) | 40 characters, 2 rows |
| Operation deadline | 20 seconds of read time. A wait on `sf` for a refreshed login is not counted. |
| Network timeout per request | The time left of the deadline, up to 5 seconds |
| Each `sf` command | 120 seconds. Both commands of one login read share 180 seconds, apart from any read's 20. |
| `sf` output | 1 MiB |
| Retries | The first HTTP 401 in an operation, once, after the login is read again and the org and user are confirmed. Nothing else. |

### Salesforce errors

`salesforce_invalid_configuration` exits 2. `salesforce_environment_mismatch` and
`source_identity_mismatch` exit 3. Every other `salesforce_*` code exits 5. A failed
`debug salesforce check` exits 4. Over MCP, an upstream failure is a tool error.

| Code | Cause | Fix |
| --- | --- | --- |
| `salesforce_invalid_configuration` | The reader was given an environment that is not one of the three. | Repeat with `developer`, `sandbox` or `production`. |
| `salesforce_auth_required` | The `sf` login or refresh gave no usable address or token, the alias names no login, a second 401 arrived, or the org or user behind the alias changed. | Install `sf` if it is missing, run `sf org login web --alias acme-dev`, then repeat with the alias. |
| `salesforce_cli_timeout` | `sf` did not answer within its time limit. | Repeat the command. To time `sf` alone, run `sf org display` with the same alias. |
| `salesforce_edition_required` | `developer` saw another edition. | Connect a Developer Edition alias, or the intended `--environment`. |
| `salesforce_environment_mismatch` | The org is not of the selected environment. | Check the alias and repeat with the intended environment. |
| `salesforce_api_disabled` | HTTP 403 with `API_DISABLED_FOR_ORG`. | Use an API-enabled org and a user with the API Enabled permission. |
| `salesforce_schema_unavailable` | A required standard field is not exposed. | Run `debug salesforce check` with the same alias and environment to see the missing fields. |
| `salesforce_forbidden` | Another HTTP 403, or an object that is not queryable. | Grant the user read access to the four objects and their fields. |
| `salesforce_rate_limited` | HTTP 429, or HTTP 403 with `REQUEST_LIMIT_EXCEEDED`. | Wait for API capacity, then retry. |
| `salesforce_result_limit` | A read passed its request, page, row or size limit. | Use a narrower search or a smaller account book. |
| `salesforce_pagination_invalid` | Salesforce returned an unsupported page locator. | Retry on a smaller account book. |
| `salesforce_timeout` | The deadline passed. | Check connectivity and the `sf` login, then retry. |
| `salesforce_unavailable` | The endpoint could not be reached. | Check connectivity and the `sf` login, then retry. |
| `salesforce_http_error` | Another upstream HTTP status. | Check that the object is available and readable in this org. |
| `salesforce_invalid_response` | The answer was outside the supported shape, or held the token. | Check access to the mapped fields, then retry. |
| `salesforce_invalid_arguments` | An account ID, search text or date is invalid. | Use an `acc-sf-` ID, a shorter query, or a `YYYY-MM-DD` date. |
| `salesforce_account_number_ambiguous` | An AccountNumber lookup returned a number that is not a literal match. | Select the account by its `acc-sf-` ID. |
| `source_identity_mismatch` | The org or user behind the alias is not the one the workspace is pinned to. | Sign in to the pinned org again under the same alias, or connect this one in a workspace of its own. |
| `renewal_fields_unmapped` | A check step: no Contract has an `EndDate` on or after the planning date. Cards still work and say so. | Create a Draft Contract with `StartDate` and `ContractTerm`, or verify read access to `EndDate`. |

## Call transcripts

**Import a UTF-8 TXT, WebVTT or SRT export and bind it to one account. The card of that account
then quotes a selected excerpt as whole sentences.** Nothing contacts a call provider, records,
transcribes or summarizes.

```sh
gtm-connector connect demo --state-dir renewals
gtm-connector connect calls --state-dir renewals --seller 'Morgan Reed'
gtm-connector debug calls import meeting.vtt --account acc-007 --state-dir renewals
gtm-connector debug calls import meeting.vtt --account acc-007 --cues 8:14 --called-at 2027-01-14T15:00:00Z --state-dir renewals
gtm-connector debug calls import meeting.txt --account acc-007 --speaker-names --state-dir renewals
gtm-connector debug calls import meeting.txt --account acc-007 --speaker-names --attendees attendees.json --state-dir renewals
gtm-connector debug calls list --state-dir renewals
gtm-connector debug calls inspect IMPORT_ID --full --state-dir renewals
gtm-connector brief acc-007 --state-dir renewals
```

An import prints each step on stderr, then what it saved:

```text
Reading the transcript export.
Preparing the connected CRM and private state.
Resolving the account through an audited CRM read.
Saving the private transcript selection.
Transcript imported. The excerpt is selected for this account.
Import             note-a8e3c0d7...
Account            acc-007
Call time          unknown
Requested excerpt  cues 1:3
Cited excerpt      cues 1:3
Total units        3
Speaker names      not declared
```

### Connect and import

`connect calls` connects imported transcripts beside Salesforce or beside the demo CRM, and
`--seller` lists the sellers. `debug calls import` refuses with exit 2 until transcripts are
connected. `brief`, `serve` and `mcp stdio` then read the imports with no further option.

- The file is read and parsed, and its excerpt selected, before any source is opened.
- The account is resolved by one `get_account` read under a receipt of its own, in a run named
  `import-`, which `audit` lists. A refused resolution keeps its receipt.
- An import is saved only under a finished read that returned the account in the same workspace,
  and only while the CRM and the calls are connected as the command found them. One detached or
  connected again meanwhile refuses it, and nothing is saved.
- `--account` must match the connected CRM: `acc-NNN` beside the demo CRM, `acc-sf-` beside
  Salesforce.
- The file itself must not be a symbolic link. A folder on the way to it may be. A file replaced
  after it is checked, or changed while it is read, is refused.

`--account-number` is not available in this build: beside Salesforce it is refused with exit 3, and
beside the demo CRM with exit 2.

> **Demo call notes and imported transcripts never share a workspace.** A workspace whose cards
> already read the demo call notes refuses `connect calls` with exit 3. The database makes this
> check as the calls are connected, so a demo read that finishes meanwhile is either seen or refused.

`connect calls --remove` detaches the transcripts and deletes nothing. A card then says `Calls: not
connected.` `connect demo` keeps connected transcripts connected and leaves detached ones detached.
Only `connect calls` connects them again.

| Option | Meaning |
| --- | --- |
| `FILE` | `import`: the export. `.txt`, `.vtt` or `.srt`, in either case, selects the parser. |
| `--account ID` | `import` requires it. `list` takes it as a filter. |
| `--lines FIRST:LAST`, `--cues FIRST:LAST` | The excerpt, in TXT lines or in VTT or SRT cues. Ranges count from 1 and include the end. Each number is 1 to 99999 with no leading zero. Give one at most. |
| `--called-at TIMESTAMP` | The meeting time, ISO 8601 with a `T` and a UTC offset, such as `2027-01-15T10:00:00Z`. Without it the call time stays unknown, and the card prints `call time Missing`. |
| `--speaker-names` | States that every line or cue opens with its speaker's name and a colon. See [Speaker names](#speaker-names). |
| `--attendees FILE` | The call's attendee list: each speaker label with an email address. See [Attendees](#attendees). |
| `--reassign` | Moves content that belongs to another account to this one. |
| `--after IMPORT_ID` | `list`: continues after the `next_after` of the last page. A page holds 100 imports. |
| `--full` | `inspect`: every unit of the file and its original text. |
| `--state-dir NAME` | The workspace `connect calls` was given |
| `--json` | The result as JSON. Format characters, such as bidirectional controls, are written as `\uXXXX` escapes. |

### How a file is read

A transcript unit is one line of a TXT file, or one timed cue of a WebVTT or SRT file, in file order.
The excerpt is a range of those units: by default the first 20, or fewer if they pass a limit below.
Every excerpt adds one adjacent unit on each side where there is one.

> **An explicit range that passes a limit is refused, never shortened.** A range past the end of the
> file is `calls_import_invalid`.

- Cues may repeat or reorder start times, end where they start, and be separated by lines of spaces
  where a cue follows.
- A cue that ends before it starts is refused, and so is anything after an end time other than WebVTT
  cue settings.
- A WebVTT identifier may repeat. An SRT cue may be numbered and carries no other identifier.
- Inline tags such as `<v Name>` are stored as the file has them.
- Well-formed `Kind:`, `Language:` and `X-TIMESTAMP-MAP=` lines under the `WEBVTT` line are skipped,
  and any other text there is refused. `NOTE`, `STYLE` and `REGION` blocks hold no speech.
- A file with no spoken text, or with control characters other than tabs and line endings, is
  refused.

A revision is one saved import, named by its import ID (`note-` and 64 hex digits). Each account has
one active excerpt. The same bytes, account, call time and range reuse the revision and print
`status: unchanged`. Any change makes a new active revision, and older revisions stay inspectable.

The file's bytes, every revision and each account's active excerpt are kept in PostgreSQL and never
changed or deleted. Every read checks a revision against its stored bytes.

### Sentences and speakers

On a card, each whole sentence is one numbered unit, which is not a transcript unit. [Cards](cards.md)
gives the sentence and role rules. What a transcript adds:

- A WebVTT voice tag names the speaker of the text after it and is not part of the sentence. A cue
  with two tags has two speakers.
- A cue with no tag takes its identifier as the label when the identifier is not a number.
- TXT lines and SRT cues have no label, so their role is unknown, unless the import states speaker
  names.
- WebVTT styling tags (`<b>`, `<i>`, `<u>`, `<c>`, `<lang>`, `<ruby>`), the reading inside `<rt>`, and
  cue timestamps are not part of a sentence.

Neighbouring cues under one speaker label are read together, so a sentence that runs across them
stays whole. They join when they are at most two seconds apart, or at most ten when the earlier cue
stops mid-sentence and the later one goes on in lower case. Cues or lines with no label never join,
and neither do TXT lines, which have no times.

In a TXT file, keep each sentence on one line. NOT READ says which lines or cues of how many were
read, such as `Calls: one transcript excerpt was read, cues 1 to 3 of 3. Every other call was not
read.` An account with nothing
imported gets `Calls: no transcript is imported for this account.`, and demo call notes are never
read in its place.

### Speaker names

With `--speaker-names` the operator states that a file names its speakers at the start of each line.
The connector never guesses it. The whole file is checked, and the statement is saved with the import.

The name is the label and is not part of the sentence, and a cue identifier is not a label in such an
import. The file is refused with `calls_speaker_names_missing` unless every line or cue with text:

- is one line;
- opens with a name, a colon, a space and words;
- has a name that starts with a letter that is not lower case, is at most 100 characters, and holds
  no colon, question or exclamation mark, angle bracket or sentence end. In a WebVTT cue, no voice tag
  may follow the name.

"Dr. J. Vance" is a name. "We have signed. Dana Crest" and "final word on the matter" are not. The
checks on a name are made at import: an accepted import is read by its layout alone afterwards.
`debug calls list` and `debug calls inspect` show which imports state the layout.

> **A stated layout does not show who spoke.** It says how the file is read. Anyone who can start a
> line of the file can write a name there. State it only of an export whose lines the recorder wrote.

### Attendees

`--attendees FILE` gives the call's attendee list, which names the person behind a speaker label by
an email address. A card reads it before the label as a name: see [speaker roles](cards.md#speaker-roles).
The file is strict JSON of at most 64 KiB, read with the same checks as the export: not a symbolic
link, and not replaced or changed while it is read.

```json
{"schema_version": 1, "attendees": [
  {"label": "Dana Crest", "email": "dana@acme.example"},
  {"label": "Morgan Reed", "email": "morgan@northwind.example"}
]}
```

- The object has exactly `schema_version`, the integer `1`, and `attendees`, a list of 1 to 200 rows.
- A row has exactly `label` and `email`. The same label and email twice is refused.
- A label is the speaker label as the transcript writes it: text that is not empty on one line, at
  most 200 characters.
- An email is at most 254 bytes with one `@`. The part before it is 1 to 64 printable ASCII
  characters with no space, quote, angle bracket, comma, semicolon or backslash. The domain is
  lower-case letters, digits and hyphens in labels of at most 63 characters, with at least one dot.
- Several rows under one label are people behind one microphone, such as a meeting room. They give
  the label a side only when every one of them is on it.

The rows are kept in the import's manifest, so the import's ID and the `read_call_notes` receipt of a
card cover them. No command prints a label or an email of the list: the import result says only how
many rows it holds. Importing the same transcript with another list, or without one, makes a new
import, which becomes the account's.

### Results

`import` and `inspect` print a table of the import. `list` prints one block per import on a terminal
and one tab-separated row per import in a pipe. Each says `Source: user-supplied transcript export.
Provider identity and source statements are unverified.` on stderr.

| Result | JSON keys |
| --- | --- |
| `import` | `schema_version`, `source: "imported_transcript"`, `status` (`imported` or `unchanged`), `created`, `selected`, `reassigned`, `resolution_receipt_id`, then the import's metadata, and `attendees`, the number of rows, when `--attendees` was given |
| `list` | `schema_version`, `source`, `imports` (each the metadata with `active`), and `next_after` (an import ID or null) |
| `inspect` | `schema_version`, `source`, `import` (the metadata with `active`), `body`, `units`, and with `--full` the original text |
| Metadata | `import_id`, `account_id`, `format`, `called_at`, `imported_at`, `content_sha256`, `requested_range`, `cited_range`, `total_units`, `unit_kind` (`lines` or `cues`), `selection_mode` (`default` or `explicit`), `speaker_names` |

### Call transcript limits

| Limit | Value |
| --- | ---: |
| Original UTF-8 file | 2 MiB |
| Parsed lines or cues | 10,000 |
| Quoted text, context included | 16 KiB of UTF-8 |
| Returned units, context included | 256 |
| Quoted text with its units | 120 KiB of ASCII-escaped JSON. The saved record stays under 128 KiB. |
| A name under `--speaker-names` | 100 characters |
| An attendee file | 64 KiB, 1 to 200 rows |
| A page of `debug calls list` | 100 imports |

### Call transcript errors

A refusal prints its code and a fixed sentence. `debug calls list` or `inspect` in a workspace that
holds no imports is refused as `source_identity_mismatch` with exit 3.

| Code | Exit | Cause | Fix |
| --- | ---: | --- | --- |
| `calls_import_invalid` | 2 | The export or range is invalid, or the file is a link or was replaced. | Use a valid UTF-8 TXT, VTT or SRT file and range. |
| `calls_import_too_large` | 2 | The export passes 2 MiB or 10,000 units. | Use a smaller export. |
| `calls_excerpt_too_large` | 2 | The excerpt passes its text, unit or record limit. | Select fewer cues or lines. |
| `calls_speaker_names_missing` | 2 | `--speaker-names` was given and a line or cue does not open with a name and a colon. | Import the file without `--speaker-names`. |
| `calls_attendees_invalid` | 2 | The attendee file is not an attendee list as [Attendees](#attendees) gives it, or is a link or was replaced. | Give JSON with `schema_version` 1 and 1 to 200 attendees, each a label and an email, with no row twice. |
| `calls_attendees_too_large` | 2 | The attendee file passes 64 KiB. | Name fewer attendees. |
| `calls_import_not_found` | 2 | The import is not in this workspace. | Run `debug calls list`. |
| `calls_account_not_found` | 2 | The CRM holds no such account. | Check the ID with `accounts`. |
| `calls_reassign_required` | 3 | The content belongs to another account. | Review it. Add `--reassign` only to move it. |
| `calls_association_changed` | 3 | An AccountNumber now resolves to another account. | Review it before using `--reassign`. |
| `calls_source_mismatch` | 3 | The imports are bound to another CRM than the workspace's. | Connect the original sources. |
| `calls_account_ambiguous` | 3 | An AccountNumber is not one literal match. | Use `--account` with the canonical ID. |
| `calls_import_corrupt` | 4 | The stored bytes or excerpt no longer match the saved manifest. Every read checks this. | Keep this state and import again into a fresh workspace. |
| `calls_association_invalid` | 4 | The CRM answer and its receipt did not agree. Nothing was imported. | Check the CRM connection and retry. |
| `calls_association_failed` | 4 | The audited CRM read did not complete. Nothing was imported. | Check the CRM connection and retry. |
| `calls_source_unavailable` | 5 | The CRM connection could not be reached. | Check the setup and retry. |
| `calls_import_io` | 1 | The export, the attendee file or the store could not be read or written. | Check permissions and retry. |

Options that cannot go together, a missing `FILE` or `--account`, and an unknown action are refused
with exit 2 before anything is read.

## Slack

**Sign in to a Slack app you already have, connect that sign-in to a workspace with a real CRM, map
one thread to one account, and read it onto a Deal card.** The connector creates no app, asks for no
client secret, and accepts no pasted token. Its tests run against an invented Slack.

```sh
gtm-connector connect salesforce --org ALIAS --state-dir acme
gtm-connector debug slack connect --client-id PUBLIC_CLIENT_ID --redirect-uri http://localhost:8765/slack/callback --state-dir acme
gtm-connector connect slack --state-dir acme --seller 'Morgan Reed' --seller U12345678
gtm-connector debug slack map --account ACCOUNT_ID --thread-url 'COPIED_THREAD_LINK' --state-dir acme
gtm-connector debug slack check --account ACCOUNT_ID --state-dir acme
gtm-connector brief ACCOUNT_ID --state-dir acme
```

> **No fallback.** A failed live read never falls back to invented messages. Live Slack goes in a
> workspace of its own, and a workspace that holds demo data refuses it with exit 3.

### Sign in

`debug slack connect` opens the browser at a one-time page of its own loopback callback, which leads
on to Slack. Slack's address with its sign-in values never reaches a process argument, and the
browser is started with only what a desktop needs of the environment. The command waits up to ten
minutes for approval, exchanges the code with PKCE, and checks the identity the token is for. It then
prints the Slack user, team and workspace the approval came from and asks, at the terminal that runs
the command, whether to save the sign-in. Only `yes` typed there saves it. Any other answer, or no
terminal, refuses with `slack_login_unconfirmed` and saves nothing. The one-time page can be opened by
any program that reads the opener's argument first, and that program could approve as its own Slack
user. The question is what keeps such an approval from being saved.

Each request the sign-in commands make to Slack runs under a receipt of its step, committed first, in
the workspace the command names.

The Slack app needs:

- [PKCE](https://docs.slack.dev/authentication/using-pkce/) enabled as a public client;
- the exact redirect `http://localhost:PORT/slack/callback` registered, `PORT` from 1024 to 65535;
- the user scope `channels:history`, `groups:history` or both, approved for the workspace;
- for [speaker roles](cards.md#speaker-roles) by the author's profile, also `users:read`, which gives
  each author's Slack workspace, and `users:read.email`, which gives the author's profile email.

No other scope is requested, and a grant that returns other scopes is refused. A sign-in needs a
history scope: one that asks only for `users:read` or `users:read.email` is refused as
`slack_invalid_configuration`. A bot grant is refused
as `slack_token_type_unsupported`.

The sign-in is saved in the credentials folder, in `slack-WORKSPACE.json` (mode 0600, one file for
each workspace, replaced whole on every change), beside its lock `slack-WORKSPACE.lock`. `WORKSPACE`
is the workspace's slug. The database holds the mapped threads and the identity digest the workspace
is pinned to, never a token.

> **The sign-in is a credential, and a backup leaves it out.** Slack spends a refresh token once, so a
> restored copy is one Slack no longer accepts. After a restore, run `debug slack connect` again.

A workspace whose Slack was connected keeps the user it was connected to. A sign-in for another user
is refused with `slack_identity_mismatch`, and the saved one stays.

| Command | What it does | Slack requests |
| --- | --- | ---: |
| `debug slack connect --client-id ID --redirect-uri URI` | Signs in through the browser and saves the sign-in as ready | 2 |
| `debug slack status` | Reads the saved file. A missing sign-in exits 0. | 0 |
| `debug slack status --check` | Verifies authentication, and refreshes a token near its expiry once. It does not show that a thread is readable. | 1 or 2 |
| `debug slack disconnect` | Removes the local sign-in. Repeating it succeeds. | 0 |
| `debug slack disconnect --revoke` | Asks Slack to revoke the saved access token without refreshing, then removes the local sign-in. If Slack does not confirm, it exits 5. Remove the app itself in Slack settings. | At most 1 |

Ctrl-C exits 130. An approval or a save that already finished is not undone, so run
`debug slack status`. Only one command holds a workspace's sign-in at a time; another gets
`slack_connection_busy`.

| Option | Meaning |
| --- | --- |
| `--client-id`, `--redirect-uri` | `connect` only, required. The existing app's public client ID and its exact registered redirect. |
| `--user-scope` | `connect` only. `channels:history` (the default), `groups:history` for private channels, `users:read` and `users:read.email`. Repeat it for each. Passing it replaces the default. |
| `--team-id` | `connect` only. Requires this Slack workspace. It is never printed. |
| `--state-dir NAME` | Every `debug slack` action. The workspace, `demo` by default. |

### Connect, map, check and read

1. `connect slack` connects the sign-in saved for the workspace and pins the workspace to its
   identity. It contacts nobody. With no saved sign-in it exits 2 and names `debug slack connect`.
   With one that needs a new login, or whose identity is unchecked, it exits 3.
2. `debug slack map` needs Slack connected and a ready sign-in. It resolves the account by one
   `get_account` read under a receipt of its own, and saves the mapping. It fetches no messages.
3. `debug slack check` reads the mapped thread as `read_slack_messages` under a receipt, and checks
   the result against its snapshot and its receipt. It reads no CRM or calls data.
4. A card reads Slack whenever Slack is connected. An account with no mapped thread gets `Slack: no
   thread is selected for this account.` Any failed read gives no card, and the command exits with
   the code of its reason.

```text
Slack thread selection saved. Thread access has not been checked.
Next: gtm-connector debug slack check --help
```

- `connect slack --remove` detaches Slack and keeps the sign-in.
- Select the parent message. A reply selected as the parent fails the check.
- A mapping is saved only while the CRM and Slack are connected as the command found them.
- Threads are mapped one at a time.

`--account-number` is not available in this build: beside Salesforce it is refused with exit 3, and
beside another CRM as `slack_selection_invalid`.

| Option | Rule |
| --- | --- |
| `--thread-url` | The saved workspace's HTTPS origin under `slack.com` and the path `/archives/CHANNEL/pTIMESTAMP`. A reply link names its parent through `thread_ts`. An optional `cid` must match the channel. Other query fields, fragments and other workspaces are refused. |
| `--channel` with `--thread-ts` | The channel ID (`C` or `G`, then 1 to 31 capitals or digits) and the parent's timestamp: whole seconds, a dot, and exactly six digits. Use it when the saved sign-in has no workspace origin. |
| `--account` | The CRM account ID: `acc-007`, or the `acc-sf-` form with Salesforce. `check` requires it. |
| `--replace` | Needed when the account already has another thread |
| `--reassign` | Needed when the thread belongs to another account, which is left with none |

### Reading a thread

A read asks `GET https://slack.com/api/conversations.replies` for the selected thread up to the
moment the read began, follows cursors to the end, and returns every message or none. It never starts
a login.

> **A read is not an archive.** Earlier edits, deletions and retention are unknown. The card's NOT
> READ line says so: `Not read: files (N), attachments (N), blocks (N), earlier versions of edited
> messages, and every other thread.`

| A message | On the read |
| --- | --- |
| Plain, `bot_message`, `me_message`, `file_share` or `thread_broadcast` | Accepted |
| `tombstone` | Accepted only as the selected parent, written by Slack's system user `USLACKBOT`, for a deleted parent that still has replies |
| Any other hidden message, edit or delete wrapper, other subtype, or a message with no text | Refuses the whole read |
| Files, attachments and blocks | Counted and left out. A rich-text block that only repeats the message's text is not counted: Slack adds one to every message a person posts, and each of its text runs, links, emoji and mentions stands in the text, in order. |

On a card, a live message's speaker is the raw Slack user ID it carries. A deleted parent carries
`USLACKBOT`, and a message with no user, such as some app messages, has no speaker label.

A card's read of the thread then asks `GET https://slack.com/api/users.info` for each distinct
author, in user ID order, under the same sign-in, deadline and response bytes, at most 50 a read. It
keeps one member record for each author, `slack/members/mem-USER`, bound by the same
`read_slack_messages` receipt: the author's user ID, the Slack workspace (`team_id`) the profile names,
whether that is the signed-in user's own workspace (`home`), the profile email, `profile` (`read`,
`missing_scope`, `over_limit` or `not_read`) and `missing_scopes`. A profile email is kept only when it
is a valid address, with its domain in lower case; any other is no email.

- Without `users:read` no profile is asked for. Every member is `missing_scope`, with no team and no
  email, and its `missing_scopes` lists `users:read` and `users:read.email`.
- With `users:read` but not `users:read.email` the team is read and the email is not, and
  `missing_scopes` lists `users:read.email`.
- An author past the first 50 is `over_limit` and is not asked for.
- An author Slack does not find or show (`user_not_found`, `user_not_visible`), or whose profile answer
  is malformed or names another user or no valid team, is `not_read`, and the read goes on. A 429
  after the one retry, an expired token after the one refresh, and a failed request still refuse the
  read.

The read never fails for a missing profile scope: the author's role falls to what the card can still
match, the seller list. See [speaker roles](cards.md#speaker-roles). The `read_slack_messages` MCP
tool returns the messages alone. A `slack:read` grant can read every mapped account its token is
granted; see [MCP](mcp.md).

### `debug slack check`

The check reports in counts and codes. It holds no IDs, names, timestamps, message text, URLs, tokens
or paths. A failed check prints its report, then fails with exit 4.

| Result | Fields |
| --- | --- |
| Report | `schema_version: 1`, `status` (`passed` or `failed`), `crm_access_checked: false`, `calls_access_checked: false`, `api_calls`, `response_bytes`, `counts`, `steps`, `next` (the first failed step's recovery command), `duration_ms`. A request or byte count is null when unknown. |
| `counts` | `auth_calls`, `message_calls`, `pages`, `raw_rows`, `duplicates`, `refresh_attempts`, `token_retries`, `rate_retries`, `messages` |
| `steps` | `private_state`, `mapping`, `credential_binding`, `thread_read`, `receipt`. Each has `name`, `status` (`pass`, `fail` or `not_run`), `count`, `duration_ms`, `api_calls`, `response_bytes`, `code`, `message` and `next`. |

A step's `code` is `ok` or one of the reasons under [Errors](#slack-errors), or one the check adds:
`prerequisite_failed`, `slack_check_failed`, `slack_metrics_unavailable` (the source-work counts
could not be verified) or `slack_receipt_invalid` (no final receipt matches the result).

As text the report is a table. In a pipe each row ends with the step's code, its message and what to
run next. On a terminal a failed step's code and message follow the table.

### Other Slack results

| Result | Fields |
| --- | --- |
| `connect`, `status` | `schema_version: 1`, `configured`, `credential_state`, `verification`, `verified_component`, `requested_scopes`, `granted_scopes`, `rotation`, `access_expires_at`, `last_verified_at`, `needs_login` |
| `disconnect` | `schema_version`, `status: "disconnected"`, `local_credentials_removed`, `remote_revoke_requested`, `remote_access_token_revoked`, `revocation_error` (a code or null) |
| `map` | `schema_version`, `status` (`saved` or `unchanged`), `selection: "saved"`, `thread_access: "unchecked"`, `counts` with `selected_threads`, `duration_ms`, `next` |

| Field | Meaning |
| --- | --- |
| `configured` | Whether a saved sign-in exists |
| `credential_state` | `missing`, `ready`, `identity_unchecked` (run `status --check`), or `refresh_pending` (run `connect`; it does not mean a refresh is running) |
| `verification`, `verified_component` | `saved` for local inspection, or `live` when this command checked with Slack. Always `authentication`, never message access. |
| `rotation` | Whether the grant has a refresh token ([token rotation](https://docs.slack.dev/authentication/using-token-rotation/)), or null with no sign-in |
| `access_expires_at`, `last_verified_at` | UTC timestamps or null |
| `needs_login` | Whether `connect` is needed |

### Slack limits

| Limit | Value |
| --- | --- |
| Requests of one read | `conversations.replies`, with at most one refresh and the identity check after it, all sharing the budget below; then `users.info` for the authors |
| `users.info` requests of one read | 50, apart from the 10 attempts below, within the same deadline and response bytes. A 429 takes the read's one rate retry, and an expired token its one refresh. |
| HTTP attempts, authentication and failures included | 10 |
| Response bytes across those attempts | 1 MiB |
| One authentication response | 64 KiB |
| Deadline for one read, and for the requests of one sign-in command | 20 seconds |
| Browser approval | 10 minutes |
| Message rows, duplicates included | 100 |
| One message's text | 16 KiB of UTF-8 |
| All unique message text | 64 KiB of UTF-8 |
| Normalized result | 256 KiB of ASCII JSON |
| One cursor | 2,048 bytes |
| A saved sign-in | 64 KiB |
| A refresh token | Single use, usable for 30 days |
| Mappings in one workspace | 100, one thread per account and one account per thread |
| Retries | One after `token_expired`, with at most one refresh. One after HTTP 429 when its `Retry-After` is at most five seconds and fits the deadline. Nothing else retries. |

### Slack errors

A reason sets the exit code of a command, and is a step `code` in `debug slack check`.

| Reason | Exit | Fix |
| --- | ---: | --- |
| `slack_invalid_configuration` | 2 | Correct the options: an existing public app and its exact registered localhost redirect. |
| `slack_connection_busy` | 1 | Finish or cancel the other command, then retry. |
| `slack_private_state_invalid` | 3 | The credentials folder or the sign-in's lock is not private. Use the original folder. |
| `slack_credentials_invalid` | 4 | Keep the file private and reconnect separately. |
| `slack_not_connected`, `slack_reconnect_required` | 3 | Run `debug slack connect`. |
| `slack_browser_unavailable` | 1 | Retry where a system browser works. |
| `slack_callback_unavailable` | 1 | Free the registered localhost port. |
| `slack_login_timeout` | 1 | Retry and approve within ten minutes. |
| `slack_login_denied` | 3 | Retry only if you intend to approve. |
| `slack_login_unconfirmed` | 3 | The approval was not confirmed at the terminal, and nothing was saved. Retry, and type `yes` only for your own Slack user. |
| `slack_oauth_rejected` | 3 | Check the app's PKCE setting, redirect and workspace approval. |
| `slack_scope_mismatch` | 3 | Use a grant with exactly the selected scopes. |
| `slack_identity_mismatch` | 3 | Restore the original identity, or use another workspace. |
| `slack_token_type_unsupported` | 3 | Use a user grant from a public PKCE client. |
| `slack_selection_invalid`, `slack_crm_configuration_invalid`, `slack_account_not_found` | 2 | Correct the account, link, channel or timestamp. Find the account ID with `accounts`. |
| `slack_account_ambiguous` | 3 | Pass the account ID with `--account`. |
| `slack_association_failed`, `slack_association_invalid` | 4 | The CRM lookup failed, and nothing was saved. Check the CRM connection. |
| `slack_crm_unavailable` | 5 | Check CRM access, then retry. |
| `slack_mapping_missing`, `slack_mapping_conflict` | 3 | Run `debug slack map` for the account. A conflict says which flag it needs: `--replace` when the account has another thread, `--reassign` when the thread is another account's. |
| `slack_source_mismatch` | 3 | Connect the original CRM and Slack identity in the original workspace. |
| `slack_identity_unchecked` | 3 | Run `debug slack status --check`. |
| `slack_thread_unavailable`, `slack_forbidden`, `slack_thread_changed`, `slack_thread_incomplete` | 3 | Check the selected parent, the history scope, the profile scopes and channel access. For a changed or incomplete thread, retry when it is stable. |
| `slack_resource_limit`, `slack_output_too_large` | 3 | Select a smaller thread. |
| `slack_clock_invalid` | 3 | Correct the local clock, which moved backward. |
| `slack_mapping_invalid`, `slack_snapshot_invalid` | 4 | Keep the private state and inspect the setup before recreating it. |
| `slack_response_invalid`, `slack_rate_limited`, `slack_timeout`, `slack_unavailable` | 5 | Run `debug slack status`, wait, then retry. A profile answer for another user, or of another shape, is `slack_response_invalid` for the whole read. |
| `slack_storage_failed` | 1 | Fix access to the credentials folder, then run `debug slack status`. |

An error message starts with its reason. Slack's own error text is never included.
