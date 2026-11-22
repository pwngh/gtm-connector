# The API

**One deal ID gets you the card, its proof and its changes, from a script, an assistant or the web app.**

The web app, the CLI, the MCP server and the alerts all read the same API. Every route checks the
caller's grant, and a refusal is returned as a refusal, never as an empty list.

> **What the binary serves today.** The `/api/v1` routes under
> [the server today](#the-server-today), the [deal routes](#deal-routes) and [matches](#matches) marked as served,
> MCP at `/mcp` ([MCP](mcp.md)), `/healthz` ([running it](operations.md#health)) and the
> [read-only SQL](#read-only-sql) views. A deal binds the CRM's IDs only. The other deal routes,
> pushing your own data, and sending webhooks and Slack alerts are the design, and the
> [roadmap](ROADMAP.md) lists them as not built. The bodies of a webhook delivery and a Slack alert are built
> ([what a delivery holds](#what-a-delivery-holds)); nothing sends them yet.

```sh
curl https://HOST/v1/deals/thornfield-2027-2e3hke   # with a script's token, once tokens for scripts are built
gtm deal thornfield-2027-2e3hke
```

Today the deal and match routes know the caller by the session cookie of a person signed in to the
web app, refuse a bearer token, and need the CSRF token and the public origin for a write
([deal routes](#deal-routes)).

## One deal, one ID

A deal is a bundle of IDs from many tools. Register it and bind them once. After that the deal ID
is all you pass.

| Tool | IDs the deal binds |
| --- | --- |
| Salesforce | account, opportunity and contact IDs |
| Slack | channel ID and thread, or channel name |
| Gong or Zoom | participant emails and the account domain |
| Your own database | contact, client or contract IDs |

A watch rule registers most deals for you. Register one by hand when its records live in your own
system.

Today a deal is registered by hand, with `gtm-connector deal new ACCOUNT_ID` or `POST /v1/deals`, and
binds the CRM's IDs alone. Registering reads the account and then its opportunities from the
workspace's CRM, each as a tool call under its own receipt that names the account, and the deal binds
what those reads returned:

| Binding | What it is |
| --- | --- |
| `account` | The account. |
| `opportunity` | The open opportunity that closes first, by ID on a tie, or `null`. |
| `contract` | On Salesforce, the contract a card counts the renewal to: the earliest that ends on or after the planning date, by ID on a tie. The planning date is the day `deal new --as-of` names, and without it `2027-01-15` on the demo book and today's UTC date on Salesforce. Otherwise `null`. |
| `renews_on` | The account's renewal date, or else that contract's end, or else that opportunity's close date, or `null`. It is set once, when the deal is registered. A card's countdown is read again for every card and never counts to a close date, so the two can differ. |

The database refuses a deal that binds a record its reads did not return.

**The ID** is the first word of the account's name, the year the deal renews, and a key of the
account: `thornfield-2027-2e3hke` for Thornfield Wren LLP (`acc-003`), renewing on 2027-02-08.

- The word is the first one that holds an ASCII letter or digit once its accents are taken off,
  lowered, with every other character dropped and cut to 40 characters, or `deal` when no word holds
  one.
- The year is that of `renews_on`, or of the planning date when it is `null`.
- The key is the first six characters of the base32 of the SHA-256 of the account's ID, in lower case.
  It depends on the account alone, so an ID says nothing about another account's deals: two accounts
  whose names start with the same word get different IDs, whatever else the workspace holds. Only when
  two accounts' six-character keys are the same, about one pair in a billion, does the later one take
  twelve characters.
- The account's next deal of the same year takes the lowest free number from 2:
  `thornfield-2027-2e3hke-2`. An ID is never given twice.

An account has one open deal at a time. While it has one, every card `brief` makes for the account
belongs to that deal, and `GET /v1/deals/{id}` returns the newest. The deal is found when the card is
published, in the same transaction, so a deal registered while `brief` reads the account still gets
the card, unless the card read an imported call or a mapped thread for no deal or for another one: then
it is not published, and `brief` says to run it again. With none, `brief` makes the account's own card,
which belongs to no deal. A deal is a binding around cards, not a new card format: a deal's card reads
the account's call and thread only when they are [matched](#matches) to the deal, and is otherwise
the card the account would get. A closed deal keeps
its cards, and is never opened again or deleted. The database refuses a card bound to a deal it was
not published during, and a restore checks the same.

## Deal routes

| Route | What it does | Today |
| --- | --- | --- |
| `GET /v1/deals` | Lists the deals you may read, with their colour. `?status=needs_attention` keeps the flagged ones. | Served, without the colour or the filter |
| `POST /v1/deals` | Registers a deal and binds its IDs | Served, for an account of the CRM |
| `GET /v1/deals/{id}` | Returns the current card as JSON, with what changed | Served, without what changed |
| `POST /v1/deals/{id}/refresh` | Reads every source of the deal again, on demand | Served |
| `GET /v1/deals/{id}/proof/{line}` | Returns the record or sentence behind one line, and its receipt | Not built |
| `GET /v1/matches` | Lists the matches waiting for a person ([matches](#matches)) | Served |
| `POST /v1/matches/{id}` | Decides one match: onto one deal of its account, or onto none | Served |
| `POST /v1/deals/{id}/sources` | Pushes records or messages from your own system | Not built |

The routes are described in an OpenAPI file served with the API. That file is not built yet.

The served routes take what `/api/v1` takes: the session cookie of a person signed in to the web app,
and nothing else. A request with an `Authorization` header is refused, the Host must be an allowed
one, a write needs the CSRF token of `GET /api/v1/session` in `X-CSRF-Token` and the public origin
in `Origin`, and no answer is kept by a browser or a proxy (`Cache-Control: no-store`).

A person reads a deal through their grant on its account, whoever registered it. A deal of an
account they hold no live grant on answers `403 not_permitted`, as an unknown ID does, and the list
leaves it out.

| Route | Answer |
| --- | --- |
| `GET /v1/deals` | `{"deals": [DEAL, ...]}`, the open deals first, each group by ID |
| `POST /v1/deals` with `{"account": "acc-003"}` | `201` and `{"deal": DEAL}` |
| `GET /v1/deals/{id}` | `{"deal": DEAL, "card": CARD, "matches": [MATCH, ...]}`. `CARD` is the deal's newest card exactly as `GET /api/v1/cards/{card}` gives it, or `null` before `brief` or a refresh has made one. `matches` are the matches of the calls and threads that card read. |
| `POST /v1/deals/{id}/refresh`, with no body | `200` and the deal as `GET /v1/deals/{id}` gives it then: with the card this refresh published, unless another refresh finished after it |

```json
{"id": "thornfield-2027-2e3hke", "account": "acc-003", "account_name": "Thornfield Wren LLP",
 "state": "open", "opportunity": "opp-003", "contract": null, "renews_on": "2027-02-08",
 "registered_by": "PRINCIPAL_ID", "registered_at": "2026-11-21T15:04:05.123456Z", "closed_at": null,
 "reads": {"get_account": "RECEIPT_ID", "list_opportunities": "RECEIPT_ID"},
 "latest_card": {"id": "CARD_ID", "brief_id": "brief-...", "published_at": "..."}}
```

`account_name` is source text, neutralized. `registered_by` is the principal that registered the deal
through the API, or `null` for one `deal new` registered. `reads` are the receipts of the two reads it
was registered on, which `GET /api/v1/receipts/{receipt}` serves. `latest_card` is `null` until
`brief` makes the deal's card.

`POST /v1/deals` registers the deal as the signed-in person: both reads name them. The grant is checked
before any source is opened, and again just before each read. Without a live grant on the account,
the read that would come next is refused under its receipt, nothing more is read, no deal is saved,
and the answer is `403 not_permitted`. With no CRM connected the answer is
`409 crm_not_connected` and nothing is written.

| Refusal | When |
| --- | --- |
| `400 invalid_json` | The body is not one JSON object with `account` |
| `403 not_permitted` | No live grant on the account |
| `409 deal_open` | The account already has an open deal |
| `409 crm_not_connected` | The workspace has no CRM the server can read; nothing was read or written |
| `409 source_changed` | The CRM was connected again while it was read |
| `502 source_failed` | The CRM read failed, or did not return the account; its receipt says why |

`POST /v1/deals/{id}/refresh` makes the deal's card again, as `brief` does, as the signed-in person:
it reads the CRM, and the account's call and thread when they are matched to the deal, and publishes a
new card that belongs to the deal. Each read and the seal name the person. The grant on the deal's
account is checked before any source is opened, before each read and as the card is published. The
card is that deal's or is not published: when the deal closes while it is read, nothing is published,
also when the account's next deal is open by then. The cards before it stay. A refresh that publishes
no card leaves the deal's last card as its newest. The answer is read after the card is published, so
an answer that fails then, as for a grant revoked in between, does not undo the card.

| Refusal | When |
| --- | --- |
| `403 not_permitted` | The deal is unknown, or of an account the person holds no live grant on. Either way the refusal has a receipt: the `get_account` read it would have made, refused as error/403 with no rows, which names the account only for a known deal. A grant that ends while the card is read stops the refresh too: the read that comes next is finished as error/401, and no card is published. |
| `409 deal_closed` | The deal is closed. It keeps its cards and gets no new one. |
| `409 crm_not_connected` | The workspace has no CRM the server can read |
| `409 source_changed` | A source was connected again while it was read |
| `409 deal_changed` | The deal, or a match of its call or thread, changed while the card was read |
| `422 evidence_refused` | The evidence failed a check, so no card was made |
| `502 source_failed` | A read failed; its receipt says why |

## Matches

A match says which deal of its account an imported call or a mapped Slack thread belongs to. [Setting
it up](config.md#what-is-built-today) gives the rule. The connector decides each one once, and a second
time only to make a thread or a call with no time wait between a closed deal and the open one. A person
may decide it again. Each later decision is a new match that supersedes the one before, under its own
`decide_match` receipt, whose `match` argument names the one it supersedes.

```json
{"id": "MATCH_ID", "kind": "call", "item": "note-...", "account": "acc-007", "account_name": "...",
 "at": null, "candidates": ["copper-2027-xxxxxx", "copper-2027-xxxxxx-2"], "state": "waiting",
 "deal": null, "basis": "time_unknown", "decided_by": null, "decided_at": "...", "supersedes": null,
 "receipt": "RECEIPT_ID", "superseded": false, "stale": false}
```

| Field | What it holds |
| --- | --- |
| `kind`, `item` | `call` and the import ID (`debug calls inspect` shows it), or `thread` and the mapping's ID |
| `at` | The call's time, or the time of the thread's parent message; `null` for a call with none |
| `candidates` | The deals of the account that hold that time, oldest first. For `deal_closed`, the closed deal the item was on and the deal open when it was decided. A person's decision keeps the candidates of the match it supersedes |
| `state` | `matched` to `deal`, `waiting` for a person, or `rejected`, which keeps it off every card |
| `basis` | `only_deal` (the account has had one deal), `within_deal` (the time is that deal's), `time_unknown` (it fits more than one deal), `deal_closed` (a thread, or a call with no time, whose deal is closed while the account's next deal is open), or `person` |
| `decided_by` | The principal who decided, or `null` for the connector or the owner |
| `supersedes` | The match this decision replaced, or `null` |
| `receipt` | The `decide_match` receipt of the decision, which `GET /api/v1/receipts/{receipt}` serves |
| `superseded` | Whether a later decision of the item replaced this one, as a deal's card can show for a match it read |
| `stale` | Whether the item is no longer the account's current import or mapping, so no card reads it |

`GET /v1/matches` gives `{"matches": [MATCH, ...]}`: the current matches that wait, and are not stale,
oldest first, of the accounts the person holds a live grant on. `GET /v1/matches?all=1` gives every
current decision of those accounts, stale ones included, so a wrong one can be found and decided again. `POST /v1/matches/{id}` takes `{"decision": "match", "deal":
"DEAL_ID"}` or `{"decision": "reject"}` and answers `200` with `{"match": MATCH}`, the new decision. The
grant is checked again as the decision commits. Both routes take what the deal routes take: the session
cookie, an allowed Host, and for the write the CSRF token and the public origin; no answer is cached.

| Refusal | When |
| --- | --- |
| `400 invalid_json` | The body is not one of the two objects above |
| `400 invalid_deal` | The deal is not one of the match's account |
| `403 not_permitted` | The match is unknown, or of an account the person holds no live grant on. Either way the refusal has a `decide_match` receipt, refused as error/403, that names no account. |
| `409 match_decided` | A later decision has superseded this match, or did so while this one was made; decide the current one |

## The server today

Beside the deal routes, the server serves accounts and cards by their own IDs, to a person signed in to
the web app. The API takes the session cookie and nothing else.

| Route | What it returns |
| --- | --- |
| `GET /api/v1/session` | Whether the caller is signed in, who as, the CSRF token for the next write, and `sign_in`: whether the identity provider (`oidc`) and the demo sign-in (`demo`) are offered |
| `POST /api/v1/session` | Signs in with `login` and `password`, the demo sign-in that `setup` writes, unless `GTM_DEMO_SIGN_IN` is off |
| `GET /api/v1/session/oidc?return=PAGE` | Starts a sign-in through the identity provider and sends the browser there. It comes back to `PAGE` when that is a page of the web app, else to `/`. |
| `GET /api/v1/session/oidc/callback` | Where the provider sends the browser back: it signs the person in and sends the browser on to the page, or back to it with `sign_in_error` set to `denied`, `not_allowed`, `disabled`, `expired`, `unavailable` or `failed` |
| `DELETE /api/v1/session` | Signs out. For a person of the identity provider, `end_session_url` is the provider's own sign-out page, when it publishes one. |
| `GET /api/v1/accounts` | The accounts the person may read, each with its neutralized name |
| `GET /api/v1/accounts/{account}/cards` | The account's published cards: ID, brief ID, SHA-256, size, seal receipt and when it was published |
| `GET /api/v1/cards/{card}` | The card as JSON: its head, `as_of`, `sources`, `units`, `gaps` and `lines`. A unit sealed under an older neutralizer version has its text and speaker neutralized again. |
| `GET /api/v1/cards/{card}/text` | The text card whole, exactly as sealed, as `text/plain`, so a reader can check it against the seal |
| `GET /api/v1/cards/{card}/bytes` | The sealed bytes exactly, with their SHA-256 in `X-Content-SHA256`, so a client can hash them again |
| `GET /api/v1/cards/{card}/units/{n}` | One unit and the receipt of the read that returned it |
| `GET /api/v1/receipts/{receipt}` | One receipt |

Each read needs a signed-in person with a grant on the account. Anyone else gets
`403 not_permitted`, whether the card or receipt exists or not, so a refusal says nothing about what
the person may not read.

| Refusal | When |
| --- | --- |
| `401 sign_in_required` | A read or a sign-out with no live session |
| `401 sign_in_refused` | The login or password is not right |
| `401 authorization_refused` | A request under `/api/` carries an `Authorization` header |
| `403 csrf_invalid` | A write without the CSRF token of `GET /api/v1/session` in `X-CSRF-Token` |
| `403 demo_sign_in_off` | The demo sign-in, while `GTM_DEMO_SIGN_IN` is off |
| `404 not_found` | Any other path under `/api/`, a unit number that is not a positive integer, or a sign-in path of the identity provider where none is set up |

Each of `lines` is one line under ATTENTION or RECORD VALUES, as data, so a page, an alert or a
script reads a value without reading the text. The renewal date line of the README's card:

```json
{"amount":null,"cites":[1],"code":"renewal_date","contact":null,
 "countdown":{"counts_to":"renewal","date":"2027-02-08","days":24},
 "label":"Renewal date","more":0,"section":"value","text":"2027-02-08, in 24 days"}
```

| Field | What it holds |
| --- | --- |
| `section` | `signal` for a line under ATTENTION, `value` for one under RECORD VALUES |
| `code` | What the line states, such as `renewal_date`, `arr`, `contact`, `still_open` or `crm_at_risk` |
| `label` | A value's label as the card prints it; null for a signal |
| `text` | The value or the sentence, in the connector's words |
| `cites` | The numbers of the units it cites |
| `more` | How many more a signal counts than it cites, the card's "and N more"; 0 for none |
| `countdown` | On the renewal date or contract end line: `date`, `days` from `as_of` (negative once it has passed), and `counts_to`, `renewal` or `contract_end` |
| `amount` | On the annual recurring revenue line: `amount`, a decimal such as `"450000.00"`, and `currency`, null when the record has none |
| `contact` | On a contact line: the contact's `name` and `title`, each null when the record's field is empty |

Rendered in order, the lines are the text card's lines under those two headings, byte for byte. A
card whose lines differ is not published. A card published before lines were kept answers
`"lines": null`; publish it again to get them.

## Pushing your own data

Push rows from another CRM, a spreadsheet, internal email, SMS or product usage. Each push carries
an idempotency key and gets a receipt.

1. A push with a new key is staged.
2. A repeat with the same key and the same content returns the same result.
3. A repeat with the same key and different content is refused.
4. A staged record joins its deal at once. It waits for a person when it fits more than one deal.

To take a record back, push a retraction. Later cards stop citing it, and its history stays.

## MCP

The MCP server runs over stdio or HTTP and serves the same deals, cards and proof. Ask it "what is
blocking thornfield?" from any client below.

| Client | How it connects |
| --- | --- |
| Claude Desktop | stdio |
| Claude Code | stdio or HTTP |
| ChatGPT | HTTP |
| Codex | stdio or HTTP |
| Cursor | stdio or HTTP |

`gtm setup` prints the configuration for each client.

Today `serve`'s `/mcp` and `mcp stdio` serve five read tools over a workspace's sources and two that
read its deals and their cards, and the Inspector CLI is the one client tested. [MCP](mcp.md) describes them.

## Webhooks and Slack alerts

Both are off until you configure them. Each delivery carries the deal, the rule and a deep link to
the flagged deal. It never carries a quotation.

| Event | Sent when |
| --- | --- |
| A risk changed | A rule starts or stops failing, or a blocker is marked or resolved |
| A card changed | A refresh published a new card |
| A deal closed | The final card was sealed |

Webhook deliveries are signed, and each carries a stable event ID so a receiver can drop repeats.
A failed delivery is retried. If an attempt's outcome is unknown, then it is shown as uncertain and
not sent again until it is checked.

### What a delivery holds

The binary builds both bodies from a card's units, gaps and lines, the deal's status and the rules'
results, never from the card's text. Sending them is not built: no credential, address, retry,
delivery log or alert rule exists yet, and no command calls the builders. A body is refused, not
cut, when its event does not hold together, such as an at-risk deal with no raised rule or a rule
unknown while every source it reads was read.

Neither body carries a quotation or any part of a sentence, a contact's name, title or email, an
amount, a receipt ID, or another deal's name. A rule's sentence is the connector's own words, with
its numbers: ASCII letters, digits and plain punctuation, holding no sentence or contact of the
card. The pricing or seat conflict's sentence holds no digit at all.

**The Slack alert** for a deal that turned at risk, changed, back on track, or closed:

| Block | What it holds |
| --- | --- |
| Notification text | The status word, the deal ID, the days to renewal, the raised flags' names and how many rules are unknown, with `&`, `<` and `>` written as entities. The message sets `mrkdwn`, `unfurl_links` and `unfurl_media` to false. |
| Header | The status word and the deal ID, such as `At risk: acc-003`, as plain text |
| Account | The account's name, alone in a `plain_text` object with emoji off, so Slack formats none of it. A name longer than a section allows is left out, and a context line says so. |
| Fields | `Renewal`: the days and the date it counts to. `Card`: the card's time in UTC. |
| Flags | At risk: one section per raised flag, in the guide's order, with the connector's sentence, the threshold and a link to the first unit it cites. Changed: what is new since the card before, counted by speaker role and record values. On track: which flags no longer hold. Closed: the final card's time. |
| Unknown | One section per unknown rule with the connector's reason for each source it did not read, then one line: unknown is not the same as no risk |
| Shared | One line when some evidence is also on another deal of the account, which it does not name |
| Last | The card link, then who may open it and one link to [what flags a deal](deals.md#what-flags-a-deal) at the binary's revision |

Every `mrkdwn` object sets `verbatim` and holds only the connector's words, dates, numbers and the
links it built. The status is the first word of the header, and each flag section starts with a
Slack emoji name, `:red_circle:`, `:large_orange_diamond:`, `:white_check_mark:`, `:grey_question:` or
`:lock:`, so colour is never the only signal. A message is refused when it would pass one of Slack's
limits: 50 blocks, a header of 150 characters, 10 fields of 2,000 characters, 10 context elements,
or a text object outside 1 to 3,000 characters. There are no buttons or menus: nothing in a message
needs Slack to call the server.

The card summary under a pasted link to the deal holds no more: the account's name, the status,
the flags' names, the countdown, the card's time, which sources were not read, and a link to the
card.

**The webhook body** is JSON in a fixed field order, so one event gives the same bytes on every
build:

```json
{"id":"evt_01","type":"risk.changed","occurred_at":"2027-01-15T15:00:05Z",
 "deal":{"id":"acc-003","status":"red","previous_status":"amber"},
 "card":{"id":"card-8522b0661ccb","sha256":"8522b066...","as_of":"2027-01-15"},
 "renewal":{"days":24,"date":"2027-02-08","counts_to":"renewal"},
 "rules":[{"code":"close_date_slippage","state":"raised","previous_state":"clear","units":[3],
           "help":"https://github.com/pwngh/gtm-connector/blob/REVISION/docs/deals.md#what-flags-a-deal"}],
 "unread":[{"source":"slack","code":"slack_denied"}],
 "changes":null,
 "links":{"card":"https://HOST/deals/acc-003?card=card-8522b0661ccb",
          "api":"https://HOST/api/v1/cards/card-8522b0661ccb"}}
```

`type` is `risk.changed`, `card.changed` or `deal.closed`. `status` is `red`, `amber`, `green`,
`closed_won` or `closed_lost`. `rules` lists every rule's result; `state` is `raised`, `clear` or
`unknown`. `changes` counts what is new since the card before on `card.changed`: `sentences` by
`customer`, `seller` and `unknown` role, and `values`. A unit number resolves at `links.api` followed
by `/units/N`, under the reader's own sign-in and grant.

**The signature.** Each delivery carries one header:

```text
Gtm-Signature: t=1800025205,v1=14b8d148bb4dfd45a04acf5360dcd45758796f93fc7a99789c93a7d666c4473f
```

`t` is the time it was signed, in Unix seconds. `v1` is the hex HMAC-SHA-256, under the secret you
share with the connector, of `t`, a full stop and the body's exact bytes. The secret is at least 32
bytes. To check a delivery:

1. Read `t` and `v1` from the header. Refuse any other form.
2. Refuse it when `t` is more than five minutes from your clock, either way.
3. Compute the HMAC of `t`, `.` and the raw body as received, before parsing it, and compare it with
   `v1` in constant time.
4. Drop an `id` you have already handled.

## Read-only SQL

**A reporting tool reads deals, cards, their lines and receipts with SQL, through one role that can
read nothing else.**

The views are in the schema `reporting` of the connector's database. Each database has its own reporting
role, which may select from that database's views and from no table, and cannot log in.
`gtm_reporting_role()` names it: `gtm_reporting_`, the database's name in lower case with any other
character as `_`, and 8 hex digits of its digest, such as `gtm_reporting_gtm_ccdd0722` for a database
named `gtm`. To make a reader for one database, connect to it as the owner and give that role to a login
role of your own:

```sql
SELECT gtm_reporting_role();                       -- gtm_reporting_gtm_ccdd0722
CREATE ROLE analyst LOGIN PASSWORD 'choose-one';
GRANT gtm_reporting_gtm_ccdd0722 TO analyst;
```

The reader reads the views of that database only, since no other database grants its role anything.
Whether it may connect to the server's other databases is PostgreSQL's own `CONNECT` privilege, which
the connector does not change.

```sql
-- Open deals, the days to each renewal, and the card that counts it.
SELECT d.deal_id, d.renews_on, l.countdown_days, c.card_id, c.as_of
  FROM reporting.deals d
  JOIN reporting.cards c ON c.card_id = d.latest_card_id
  JOIN reporting.card_lines l ON l.card_id = c.card_id AND l.code = 'renewal_date'
 WHERE d.state = 'open';
```

| View | One row per | Leads back to |
| --- | --- | --- |
| `reporting.deals` | Deal | Its account, its two CRM reads (`account_read_id`, `opportunities_read_id`), and its newest card (`latest_card_id`). `card_count` counts its cards. |
| `reporting.cards` | Sealed card | Its deal (`deal_id`, or null), its seal (`seal_receipt_id`), and the SHA-256 of its stored bytes. `as_of` is the day its bytes state. |
| `reporting.card_lines` | Line under ATTENTION or RECORD VALUES | Its card and, in `cites`, the numbers of the units it cites. A renewal line has `countdown_date` and `countdown_days`, an ARR line `amount` and `currency`, a contact line `contact_name` and `contact_title`. |
| `reporting.units` | Numbered unit of a card, without its text | The record it rests on (`record_ref`), the read that returned it (`receipt_id`) and that record's version (`record_version_id`). |
| `reporting.receipts` | Receipt of a tool call, or of a read inside one | The record references it returned (`rows`), its parent call, and its principal. `tool` is `<invalid>` for a name the connector does not define, `brief_id` is null unless it has a brief ID's form, and `error_code` is set when the failure was a code. |

Every view holds every workspace of the database, each row with its `workspace_id`: filter on it to
report on one.

Every time is a `timestamptz` the database's clock wrote. `as_of` and `renews_on` are days as the
card and the CRM state them. Each view and each of its time columns carries a comment saying what a row
is and which clock its times are on: `\d+ reporting.cards` in `psql` prints it.

> **What the views leave out.** No column holds a credential, a token or its fingerprint, a session, a
> lease, a call's arguments or request ID, a card's bytes, or a unit's body. The only text that came from
> a source is what a sealed card already prints: a line's text, label and contact. A sentence the card
> read but does not show is in no view.

A query through the role gets no receipt: the database answers it, not the connector. A restore makes
the views again with the schema, and grants them to the role of the database it fills; it needs a
database with no `reporting` schema.

A migration takes a role that already holds the name only when it can do nothing more than the role it
would make: no login, no password, no member, a member of no role, and no other attribute. Any other
stops the migration with an error that names the role. So a restore into a new database with the name of
an old one, whose role still has a reader as a member, stops until that role is dropped
(`DROP OWNED BY gtm_reporting_gtm_ccdd0722` in each database that granted it anything, then `DROP ROLE gtm_reporting_gtm_ccdd0722`) or its members are
revoked.
