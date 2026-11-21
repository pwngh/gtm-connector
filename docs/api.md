# The API

**One deal ID gets you the card, its proof and its changes, from a script, an assistant or the web app.**

The web app, the CLI, the MCP server and the alerts all read the same API. Every route checks the
caller's grant, and a refusal is returned as a refusal, never as an empty list.

```sh
curl https://HOST/v1/deals/thornfield-2027
gtm deal thornfield-2027
```

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

## Routes

| Route | What it does |
| --- | --- |
| `GET /v1/deals` | Lists the deals you may read, with their colour. `?status=needs_attention` keeps the flagged ones. |
| `POST /v1/deals` | Registers a deal and binds its IDs |
| `GET /v1/deals/{id}` | Returns the current card as JSON, with what changed |
| `POST /v1/deals/{id}/refresh` | Reads every source of the deal again, on demand |
| `GET /v1/deals/{id}/proof/{line}` | Returns the record or sentence behind one line, and its receipt |
| `GET /v1/matches` | Lists the matches waiting for a person, when the queue is on |
| `POST /v1/deals/{id}/sources` | Pushes records or messages from your own system |

The routes are described in an OpenAPI file served with the API.

## A card on the Go server today

Until deals exist, the Go server serves cards by card ID. Each route needs a signed-in person with a
grant on the card's account. Anyone else gets `403 not_permitted`, whether the card exists or not.

| Route | What it returns |
| --- | --- |
| `GET /api/v1/cards/{card}` | The card as JSON: its head, `as_of`, `sources`, `units`, `gaps` and `lines` |
| `GET /api/v1/cards/{card}/text` | The text card whole, as `text/plain` |
| `GET /api/v1/cards/{card}/bytes` | The sealed bytes exactly, so a client can hash them again |
| `GET /api/v1/cards/{card}/units/{n}` | One unit and the receipt of the read that returned it |

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
