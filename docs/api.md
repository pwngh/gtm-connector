# The API

**One deal ID gets you the card, its proof and its changes, from a script, an assistant or the web app.**

The web app, the CLI, the MCP server and the alerts all read the same API. Every route checks the
caller's grant, and a refusal is returned as a refusal, never as an empty list.

```sh
curl https://HOST/v1/deals/thornfield-2027
gtm deal thornfield-2027
```

> **What the binary serves today.** The `/api/v1` routes under
> [the server today](#the-server-today), the three [deal routes](#deal-routes) marked as served,
> MCP at `/mcp` ([MCP](mcp.md)) and `/healthz` ([running it](operations.md#health)). A deal binds
> the CRM's IDs only. The other deal routes, pushing your own data, webhooks, Slack alerts and the
> read-only SQL views below are the design, and the [roadmap](ROADMAP.md) lists them as not built.

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
workspace's CRM, each as a tool call under its own receipt, and the deal binds what those reads
returned:

| Binding | What it is |
| --- | --- |
| `account` | The account. |
| `opportunity` | The open opportunity that closes first, by ID on a tie, or `null`. |
| `contract` | On Salesforce, the contract a card counts the renewal to: the earliest that ends on or after the day of the read, by ID on a tie. Otherwise `null`. |
| `renews_on` | The account's renewal date, or else that contract's end, or else that opportunity's close date, or `null`. |

The database refuses a deal that binds a record its reads did not return.

**The ID** is the first word of the account's name and the year the deal renews:
`thornfield-2027` for Thornfield Wren LLP, renewing on 2027-02-08. The word is the first one that
holds an ASCII letter or digit once its accents are taken off, lowered, with every other character
dropped and cut to 40 characters, or `deal` when no word holds one. The year is that of `renews_on`,
or of the day of the read when it is `null`. An ID that a deal of the workspace already holds, open or
closed, takes the lowest free number from 2: `thornfield-2027-2`. An ID is never given twice.

An account has one open deal at a time. While it has one, every card `brief` makes for the account
belongs to that deal, and `GET /v1/deals/{id}` returns the newest. With none, `brief` makes the
account's own card, which belongs to no deal. A card's bytes are the same either way: a deal is a
binding around cards, not a new card format. A closed deal keeps its cards, and is never opened
again or deleted.

## Deal routes

| Route | What it does | Today |
| --- | --- | --- |
| `GET /v1/deals` | Lists the deals you may read, with their colour. `?status=needs_attention` keeps the flagged ones. | Served, without the colour or the filter |
| `POST /v1/deals` | Registers a deal and binds its IDs | Served, for an account of the CRM |
| `GET /v1/deals/{id}` | Returns the current card as JSON, with what changed | Served, without what changed |
| `POST /v1/deals/{id}/refresh` | Reads every source of the deal again, on demand | Not built |
| `GET /v1/deals/{id}/proof/{line}` | Returns the record or sentence behind one line, and its receipt | Not built |
| `GET /v1/matches` | Lists the matches waiting for a person, when the queue is on | Not built |
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
| `GET /v1/deals/{id}` | `{"deal": DEAL, "card": CARD}`. `CARD` is the deal's newest card exactly as `GET /api/v1/cards/{card}` gives it, or `null` before `brief` has made one. |

```json
{"id": "thornfield-2027", "account": "acc-003", "account_name": "Thornfield Wren LLP",
 "state": "open", "opportunity": "opp-003", "contract": null, "renews_on": "2027-02-08",
 "registered_by": "PRINCIPAL_ID", "registered_at": "2026-11-21T15:04:05.123456Z", "closed_at": null,
 "reads": {"get_account": "RECEIPT_ID", "list_opportunities": "RECEIPT_ID"},
 "latest_card": {"id": "CARD_ID", "brief_id": "brief-...", "published_at": "..."}}
```

`account_name` is source text, neutralized. `registered_by` is the principal that registered the deal
through the API, or `null` for one `deal new` registered. `reads` are the receipts of the two reads it
was registered on, which `GET /api/v1/receipts/{receipt}` serves. `latest_card` is `null` until
`brief` makes the deal's card.

`POST /v1/deals` registers the deal as the signed-in person: both reads name them. Without a live
grant on the account the first read is refused under its receipt, nothing is read, and the answer is
`403 not_permitted`.

| Refusal | When |
| --- | --- |
| `400 invalid_json` | The body is not one JSON object with `account` |
| `403 not_permitted` | No live grant on the account |
| `409 deal_open` | The account already has an open deal |
| `409 source_changed` | The CRM was connected again while it was read |
| `502 source_failed` | The CRM read failed, or did not return the account; its receipt says why |

## The server today

Beside the deal routes, the server serves accounts and cards by their own IDs, to a person signed in to
the web app. The API takes the session cookie and nothing else.

| Route | What it returns |
| --- | --- |
| `GET /api/v1/session` | Whether the caller is signed in, who as, and the CSRF token for the next write |
| `POST /api/v1/session` | Signs in with `login` and `password`, the demo sign-in that `setup` writes |
| `DELETE /api/v1/session` | Signs out |
| `GET /api/v1/accounts` | The accounts the person may read, each with its neutralized name |
| `GET /api/v1/accounts/{account}/cards` | The account's published cards: ID, brief ID, SHA-256, size, seal receipt and when it was published |
| `GET /api/v1/cards/{card}` | The card as JSON: its head, `as_of`, `sources`, `units`, `gaps` and `lines` |
| `GET /api/v1/cards/{card}/text` | The text card whole, as `text/plain` |
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
| `404 not_found` | Any other path under `/api/`, or a unit number that is not a positive integer |

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
4. A staged record joins its deal at once. It waits for a person only when the matches queue is on.

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

Today `serve`'s `/mcp` and `mcp stdio` serve five read tools over a workspace's sources, not deals,
and the Inspector CLI is the one client tested. [MCP](mcp.md) describes them.

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

## Read-only SQL

Reporting views cover cards, units, gaps, receipts and rule results. Each view states what it
counts, its time basis and its as-of time, and each row leads back to its records. The views never
expose credentials or sign-ins.
