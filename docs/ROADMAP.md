# Roadmap

The [README](../README.md) and the guides describe the product as designed. This page says what is
built, what is not, and the order it will be built in. An item leaves this page when it ships with
its test.

## v0.1.0: one Go binary replaces the Python package

v0.1.0 is released. It is the Python package's behaviour in one Go binary on PostgreSQL, and
nothing more: it reads the same sources and writes nothing back to any of them.

What has to survive the move is the card: the renewal evidence the connector prints for one
account. Every figure and sentence on a card carries a number, and each number is a unit, one CRM
record or one sentence of a call or Slack message. What the card did not read or did not show, it
says, in lines it calls gaps. Behind the card is the audit log. A tool attempt gets a row there
before it runs, and that row is its receipt. Saving a card adds a receipt that names the SHA-256
of the saved bytes, which is all "sealed" means: change a byte and the seal no longer matches.
Access is by grant. Today that is a bearer token issued for one or more scopes, each scope opening
one source, and in the Go server it is also one signed-in person's access to one account.

`testdata/oracle/` is the reference. With the recorded clocks and IDs, card text must match byte
for byte, and units and gaps must match as values. The parity tests build all 450 recorded
cards today, compare 90 of them byte for byte, and check every one against the card properties. The 50 cards that read an imported transcript are built from the recorded file: Go
parses it, saves the import in PostgreSQL and reads it back. All 82 cards of that book are also made
through `debug calls import` and `brief`, and match apart from the recorder's clock and IDs and the
Slack line, which the recorder did not request. The 104 that read a live Slack thread
read the records the Python Slack reader made, and the binary makes the same 114 cards of those two
families through `connect slack`, `debug slack map` and `brief` against an invented Slack, with the
recorded text but for the line that gives when the sources were read. The 78 Salesforce
cards read the recorded rows through the Go reader, served by an invented org.

| Step | What it will do | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirm that Go matches Python on what is ported so far | The parity tests build 450 of 450 cards: 90 are compared byte for byte, and every one is checked against the card properties. `make verify-real` compares Go on public text and JSON with the digests in `internal/realdata/recorded`. `BenchmarkPublishCard` measures the time to publish a card of 2,000 and 20,000 sentence units, and 100,000 with `GTM_BENCH_LARGE=1`. After `make up` on a new volume, `scripts/smoke.sh` checks the sign-in, and `scripts/smoke.sh demo` then `scripts/smoke.sh card` read a sealed card and check its bytes against both digests the API states. | Both pass at the final head |
| Evidence | Neutralize a bare mail address such as `dana@files.example`, which a GitHub Markdown client links, and show the host of a valid `xn--` label. | A link shows the host a browser opens, read as the URL Standard reads it and without a leading `www.`, or `[link unknown host]` when a browser would open another host or none: a backslash, a control character, a tab or a line break before the path, no `//` after the scheme, a scheme other than `http`, `https`, `ftp`, `ws` and `wss`, a numeric host a browser rewrites, a port past 65535, a host in brackets, a target with no scheme and no `//`, or an `xn--` label that fails UTS #46 with the URL Standard's flags or the joiner rules of RFC 5892. A valid `xn--` label is shown in its ASCII form. On 20,000 generated hosts, every host a link shows is the host Node's URL parser (ada 3.4.4) gives for it, except six right-to-left labels holding a letter Unicode 14.0 added, which ada refuses because its bidi table lacks those letters, and which each show their ASCII host. The test does not show that a link shows every host a browser opens: ada opens 1,025 more, which the bidi, joiner and Punycode rules or a `_` refuse. A Markdown link is replaced whole, and an angle-bracket link, a mail address in angle brackets and a reference definition where a paragraph starts are neutralized, with their escapes and character references read. A bare mail address such as `dana@files.example` in the text a quotation is made from shows `[link mail]`, and a contact's email stays a record value. Neutralizing twice gives the same text. Go checks imported transcripts and live Slack threads as Python does. Cards are made under neutralizer version 6, and `inspect`, the web API and the brief tools show a unit sealed under an older version under today's rules, with the card's bytes unchanged. | Each of those forms has a test, and real text still matches |
| Sources | Port transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container | Go reads the demo book and a Salesforce org through `sf`. `connect salesforce --org ALIAS --state-dir NAME` signs in, checks the org against `--environment` and pins the workspace to its identity and the user it reads as, and refuses another org or user there, also after a removal and after a login refresh. An `--org` that cannot be an `sf` alias, such as a token, never reaches `sf`. `accounts`, `inspect`, `brief` and `audit` then read it, each read under a receipt, and `debug salesforce check` reports the sign-in, the environment, each object's fields and, in a connected workspace, the contract a renewal plans from. Tests run every command against an invented org and an invented `sf`: `brief` makes the CRM part of all 78 recorded Salesforce cards, 4 of them compared with their recorded cards (the recording read calls and Slack from demo data, which Go never reads beside a real CRM), and a cancelled `connect` ends the `sf` process group and saves nothing. `connect salesforce --token-env` reads the org's address and a token from `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` or the file `SALESFORCE_ACCESS_TOKEN_FILE` names, with the same checks and pin as an `sf` login, and on a first HTTP 401 reads them once more and keeps only a token of the same org and user, in tests against the invented org. `connect calls` connects imported transcripts beside the demo CRM, or beside a CRM a test registers, and `debug calls import`, `list` and `inspect` work as the Python CLI's do, except `--account-number`, which is refused as not in this build. An import's account is resolved by a `get_account` read under its own receipt, and `brief` reads the account's active import. `demo` and the MCP tools read calls through the same source, untested with imports. Go signs in to Slack (`debug slack connect`, with PKCE and the loopback callback), keeps the sign-in in the credentials folder, connects it (`connect slack`) beside a real CRM, maps one thread per account (`debug slack map`), checks its read (`debug slack check`) and reads it onto a card, in tests against an invented Slack. | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Port the seven commands and `debug` to PostgreSQL, where `--state-dir` names a workspace. Serve MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. One outer receipt per tool call, plus one per read inside a prepared brief. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`, and `connect`, `accounts`, `brief`, `inspect` and `audit` on demo data and Salesforce, where `--state-dir` names a workspace and the default is `demo`. `connect` attaches and detaches the demo data, Salesforce and imported transcripts and shows what is connected. Salesforce goes in a workspace named with `--state-dir`, because the default one holds demo data. `connect slack` connects the saved Slack sign-in beside a real CRM. `connect` refuses the assistant apps as not in this build, and nothing reads demo data in their place. `demo --state-dir` is the recorded demo: one renewal with a lookup and a refused read. The binary matches 30 of the 30 recorded steps, 6 of them apart from the recorder's clock. `debug calls`, `debug salesforce check` and `debug slack` are ported. The rest of `debug` is not. `serve` answers MCP over HTTPS at `/mcp` for each token in the workspace it was issued in: the read tools over the demo book or a Salesforce org, for the accounts the token's principal is granted, and with `--selection FILE` the two brief tools, for the demo workspace only. The recorded request vectors pass there, and the write vectors are refused with a receipt. A token that carries any scope but the three read scopes is refused. `token new` issues a read-only token for one workspace, shown once, and `token list` and `token revoke` manage them. `mcp stdio` serves a workspace's read tools on stdin and stdout under the same gate, reading the sources as they are connected at each call, and the recorded stdio bridge tests pass through it on PostgreSQL. The Inspector runs against the binary over HTTPS and stdio. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Open the cards and receipts the Python package made. They are verified and kept byte for byte, never rebuilt. | `admit FOLDER --state-dir NAME --from-org IDENTITY --export-sha256 DIGEST` admits a folder's cards and the receipts they rest on into a workspace. Admission trusts the export folder: it reads nothing again from a source, and takes the CRM identity and the export's digest on the operator's word, keeps both with each card, and `inspect` says the card was admitted from an export. Within that, each card is verified against its seal, every receipt line it rests on byte for byte, the workspace's sources, its own evidence for every quotation and speaker, and the workspace's imports for every transcript it quotes, and every file of the folder is checked. The database refuses an admitted card whose seal did not finish ok and 2xx naming its bytes or whose reads are not admitted with it, also on restore. `inspect` opens admitted cards in all four forms. Tests run the command on PostgreSQL: the four recorded card folders are admitted and inspected, a changed byte in the card or the export is refused with a fixed message and admits nothing, admitting twice adds nothing, and a backup carries them. `audit`, `inspect ACCOUNT_ID`, the web app and the MCP tools show only cards made here. | The four recorded card folders are admitted from their export and inspected, and a changed byte admits nothing |
| Cutover | Ship one binary with the web app inside it (`go:embed`), for macOS and Linux on arm64 and amd64, with checksums. Remove the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrite the docs for Go. | `make build` and the image build one binary with the web app inside it. `make release` builds it for macOS and Linux on arm64 and amd64 and writes `SHA256SUMS`; CI runs it and checks the four binaries against their sums. A version tag attaches the five files to a GitHub release, as `v0.1.0` did. The Python package, its tests, `tools/oracle/` and their CI jobs are removed, and the guides under `docs/` describe the Go binary. The Go tests pass without Python, and `make verify-real` compares Go on public text with the digests in `internal/realdata/recorded`. The build inside the binary, served from a folder that holds no checkout, answers `/`, `/deals/acc-003`, its communications, `/ops/coverage` and `/ops/activity` with the app's page and serves the assets it names, and the web job in CI runs that check on its own build. A backup an older binary made restores into this one and migrates: tests restore backups of schema 1 through two generations. | A version tag is pushed and its release attaches the binaries |

### What changes for you at cutover

The Python package is gone from the repository. Build the binary with `make build`, or run
`make up` with Docker Compose, where you ran `pipx install`. The binary needs PostgreSQL 19 or 18.7;
`make up` runs one.

The commands, their exit codes and their `--json` output stay, and card text is the same byte for
byte. The one exception is writing: `log_activity` and the `crm:write` scope are removed, and a
write attempt is refused with a receipt.

State lives in PostgreSQL, so `--state-dir` names a workspace there and not a folder. Bring the cards
and receipts the Python package made with `admit FOLDER --state-dir NAME`, after connecting that
workspace to the sources they were read from and importing the transcripts they quote; it reads the
audit log as `audit.jsonl`, exported from `audit.sqlite3` one row a line, and admits on your word for
the org and the export ([admit](cli.md#admit)). Nothing else moves. Connect your sources again,
import your transcripts again, map your Slack threads again, and set your seller lists and selection
files again. Set up your assistant apps again
too: their saved entries start the Python package, which is no longer here. `token new` gives an
HTTPS client its token, and `mcp stdio` is the command an assistant app starts.

The web app the binary serves shows the deal list at `/deals`, which `/` opens, each deal's card at
`/deals/{account}`, every sentence the card read at `/deals/{account}/communications`, its plain-text
card at `/deals/{account}/text`, and the Ops pages Coverage and
Activity at `/ops/coverage` and `/ops/activity`, which `/ops` opens with Coverage. It shows nothing
else after sign-in. A deal's card shows, under its title, the days to the renewal or contract end
and the ARR line, then the card's fixed signals under Flags, what people said with each buyer's
title, the record values in the card's order, and what the card did not read. A card published
before lines were kept says its countdown, ARR and record values are on the plain-text card and
links to it. The Communications tab lists every sentence unit of the card, those it does not show
with the reason, one group per call or Slack message, oldest first, the groups with no usable time
last. The Text tab shows the plain-text card whole and copies it. The browser hashes the
card's sealed bytes first, and a card whose bytes do not match its seal shows nothing on any
tab, only that it failed its seal check. Coverage lists each granted deal with its newest card time
and number of cards, counts the deals with a card and with none, and says whether `/healthz`
answers. Activity, for one card, lists its seal receipt and then each read receipt its units name,
opens any of them with every field, and checks the card's bytes against the digest its seal names. Any other address shows a page that says there is no page there and links back. Served
from a static host, it says only that it needs its server.
Real cards will be read with the CLI and the MCP tools.

One development Salesforce org, read-only, and one development Slack workspace have been read, both on
invented records, on 2026-11-21. No sandbox, no production org and no real identity provider has been
verified. v0.1.0 did not wait for one.

## Not built yet, in order

Nothing below is started unless it says so. Each links to the guide that describes it.

### 1. First call

One rep reads one real renewal before a call.

Started: a deal is registered by hand with `deal new` or `POST /v1/deals`, read with `deal list`,
`deal show`, `GET /v1/deals` and `GET /v1/deals/{id}`, and closed with `deal close`, and `brief` makes
an account's card for its open deal ([one deal, one ID](api.md#one-deal-one-id)). People sign in through
the company's identity provider and read the accounts `grant` gives them
([running it](operations.md#sign-in-through-your-identity-provider)). An imported call and a mapped thread
are matched to a deal of their account by time, and one that fits two deals waits in the matches queue
([setting it up](config.md#what-is-built-today)). What is left:

- The proof route of a deal ([deal routes](api.md#deal-routes)), and on the served routes the deal list's colour, `?status=needs_attention` and what changed. A deal that binds IDs of other tools than the CRM ([one deal, one ID](api.md#one-deal-one-id)), and `deal DEAL_ID` printing the card as [working a deal](deals.md) shows.
- A live check of the sign-in against a real identity provider. The sign-in is built, and has run against an invented provider only.
- Calls and Slack threads discovered by email, domain and channel, with one that fits deals of two accounts on neither card and both cards saying so ([setting it up](config.md#how-a-deal-gets-its-evidence)). `approval: all`, which holds every match for a person, also per source.
- A call or thread that waits for a person, one a person rejected and one matched to another deal each saying so on the card in its own words.
- The Deals page naming a deal by its deal ID, with the list in order of urgency and in its colours. Today the page lists accounts by name at `/deals/{account}`, with the card, the proof drawer and communications ([working a deal](deals.md#the-page)).
- What changed since the reader last looked, beside what is new on the card since the one before it, which the delivery bodies already count. Marks for blockers and commitments ([working a deal](deals.md#the-card)).
- Close with a final sealed card: by a person, with a reason and a receipt, or by the CRM's closed stage, whichever comes first. A closed deal leaves the active list, and a reopened opportunity is a new deal.
- Every call of a deal, whole. Today a card reads at most one imported excerpt per account, and a deal's card only when the excerpt is matched to that deal: up to 20 lines or cues unless a range is given, and at most 256 with the lines or cues beside them ([call transcripts](sources.md#how-a-file-is-read)).
- On Salesforce, the still-open and close-date signals counted against a contract that has ended ([signals](cards.md#signals)).
- A receipt when a card fails its seal.
- Underneath: a Slack message keeps one identity across reads, and an import stays readable after a parser upgrade.

Before real people sign in, the security review is redone for more than one person. Then the
trial: five real renewals, run outside the repository, with a rep given 30 seconds per renewal to
find the status, the change, who said what, the commitment and the blocker. Only counts are
recorded. The README's limit about real deals changes only then.

### 2. Whole book

RevOps covers every renewal and sees which have no current card.

- `gtm.yaml`, previewed and activated by exact revision ([setting it up](config.md)).
- Field mapping, watch rules, transcript folders, Slack channel windows, Gong and Zoom.
- `timezone`: the day a card counts from, and the day a contract ends on. A contract's status deciding whether it sets the renewal date.
- The opportunity's currency, read from the org.
- Threads and channels of any size: today a Slack read over 100 messages is refused whole. Read live below a size the operator sets and sync in the background above it, keep every message, show a bounded set on the card, and say how far a read got. More than 100 mapped threads in a workspace.
- A participant directory that matches speakers to contacts by email or phone first.
- Accounts related as parent and subsidiary, so that a contact of one is a customer on the other's card.
- One copy of a call that arrives from two sources.
- Grants made from the CRM: owning an account's open opportunity gives a person the grant on that account.
- One list of rules: the seven the card raises today and the five flags, with thresholds and a preview ([what flags a deal](deals.md#what-flags-a-deal)). Any raised rule turns a deal red. A rule answers unknown, not clear, when a source it needs was not read or was read in part, when the connector holds too few reads to compare, and when the sentences it counts have no established role.
- Scheduled reads under leases, each as a named service identity.
- The Ops screens: Setup; on Activity, what ran across the book and whether a call is safe to retry; and on Coverage each source's state, why a deal has no current card, and the matches left off by rule ([the Ops area](config.md#the-ops-area)). A diff of any two cards of a deal.
- Pause and resume, and one size cap on every card.

### 3. Delivery

- Sending Slack alerts and signed webhooks for the three events: the destination and its secret, retries, the delivery log, alert rules, and the Slack link summary in a configured channel. The bodies and the signature are built ([what a delivery holds](api.md#what-a-delivery-holds)), from a deal status and rule results that the rule list will give. A body names a deal by its deal ID; the recorded examples supply an account's ID in its place.
- Tokens for scripts and assistants, each limited to selected deals, the flagged-deals feed, and the OpenAPI file. Today every client token of a workspace shares the same account grants ([security review](../SECURITY_REVIEW.md#known-limits)).
- The command line for a rep: `gtm deal DEAL_ID` reading the API under a person's own sign-in, so that reading a deal needs no database URL.
- `setup` printing the MCP configuration for each assistant client, and the four `connect APP` forms removed with it.
- An MCP tool that reads proof. `list_deals` and `read_deal_card` are built ([MCP](mcp.md#deals-and-their-cards)). A client is marked as tested only after a live test: Claude Desktop, Claude Code, ChatGPT, Codex and Cursor are untested today.

### 4. Your own data

- The push API, `POST /v1/deals/{id}/sources`, and retractions ([pushing your own data](api.md#pushing-your-own-data)).
- A read-only view of a customer's database as a source.
- A retention period, and deletion on request with a receipt.

### 5. The council and measurement

- Models that answer from the card and propose marks ([ask the council](deals.md#ask-the-council)).
- The model, prompt and checks chosen by measurement: a benchmark score per run, judges from more than one model family, and one confirming run on held-out data.
- Facts covered measured against a pool of confirmed facts and against a census made without model output.
- `evaluate`, running the ten cases as a table. Two of the ten wait: a buried blocker waits for marks, and a channel over the cap waits for the channel window.

### 6. Playbooks

- The checklist and the three playbooks, with review of one plan revision ([the checklist and playbooks](deals.md#the-checklist-and-playbooks)).
- The work queue across a rep's deals.
- As reps ask: stakeholders on the card, an email connector, rollups by team and rule, alert digests and quiet hours.

### 7. Running it anywhere

- Docker images published with each release, and a Homebrew tap ([running it](operations.md#install)). The release of the four binaries with their checksums is built.
- systemd and launchd templates.
- The ten cases as ready-to-open demo workspaces.
- The binary installed as `gtm`.
- Messages that say workspace where they say state folder, and MCP metadata keys without `demo` in their name.

## Later

Salesforce sign-in through a connected app, DOCX tracked changes as a source, Salesforce task
creation, a second CRM, Windows, and MCP Apps. Language SDKs, WASM and CGO builds are not planned:
integrations use HTTP or MCP.

## Open questions

- Should slippage count changes from before the connector's first read, using the CRM's field history?
- Does a deal survive its account being merged into another, or renamed, in the CRM?
- Which languages do the sentence rules hold for?

## Checks the docs still need

The other docs make the statements below, and no test asserts them yet. Each was checked against the
Go source or by running the binary, and each will get a test or narrower wording. The statements are
grouped by the v0.1.0 step that owns the check.

<details>
<summary>Gate and the Go server today (README, real-data guide)</summary>

- `make dev`, `make demo` and `make test` running as the README describes.
- `POSTGRES=18.7` keeping the fallback database on its own volume.
- `make dev` and `make test` refusing a Go other than 1.27.4, and `make dev` a Node other than 22.23.4.
- The first run writing `.env` with mode 0600 and random passwords for the two database roles.
- Compose and `scripts/host.sh` building `GTM_DATABASE_URL` from `GTM_APP_PASSWORD`, and a changed
  `GTM_APP_PASSWORD` rotating the app role's password through `make dev` or `make up`.
- The Makefile forwarding a port on Colima from a cold start.
- macOS as a supported platform alongside Linux.
- `serve` refusing to start while a restore is unfinished (setup and `demo` are tested).
- `make verify-real`:
  - its `DATASETS` and `LOCAL` options;
  - its default cache folder and the counts in `items.json`;
  - the count of non-empty outputs per function;
  - the fields of a printed difference line;
  - `report.json` listing only the first 1,000 differences;
  - the `realdata` command exiting 1 on a difference;
  - `realdata fetch` for a pinned dataset with no converter, and an unnamed run reporting the datasets it skips;
  - refusing an existing `FILE.r0` owned by someone else.

</details>

<details>
<summary>Sources (sources guide)</summary>

- **Salesforce:**
  - the request, byte, page and row limits of one operation;
  - each `sf` command's 120-second limit, the shared 180-second login deadline, and the 1 MiB limit on `sf` output;
  - the rules for the instance address and the token, and the one retry after an HTTP 401;
  - the error code each HTTP 403 answer maps to;
  - the `con-sf-`, `opp-sf-` and `ctr-sf-` record IDs;
  - `salesforce_invalid_configuration` exiting 2, and `salesforce_cli_timeout`.
- **Transcripts:**
  - the parsing rules: cue settings, the lines under `WEBVTT`, `NOTE`, `STYLE` and `REGION` blocks, and control characters;
  - cues under one label joining within two seconds, or ten when the first stops mid-sentence;
  - the exit codes:
    - `calls_association_changed` and `calls_source_mismatch` exit 3;
    - `calls_import_corrupt`, `calls_association_invalid` and `calls_association_failed` exit 4;
    - `calls_source_unavailable` exits 5.
- **Slack:**
  - the accepted and refused message subtypes, and every read limit;
  - one retry after HTTP 429 when its `Retry-After` is at most five seconds.
- **Demo data:** the cases of `acc-001`, `acc-002` and `acc-004` to `acc-007`, checked by running `brief`.

</details>

<details>
<summary>Commands, cards and MCP (CLI, cards, audit and MCP guides)</summary>

- **The CLI:**
  - the text output: the bold heading row and `NO_COLOR`, the `accounts` table and its ASCII-case search, the `connect status` lines, and an `inspect` trace of a record;
  - the JSON keys of `token`, `debug calls list`, `debug calls inspect`, `debug salesforce check` and the `debug slack` commands, and the keys of the progress lines;
  - which commands make a workspace, and a `dir-` name being hashed again;
  - the fixed line printed for an error the connector did not word itself;
  - `demo` alone leaving a removed source as it is;
  - `debug slack disconnect --revoke` exiting 5 when Slack does not confirm;
  - `debug calls import` and `debug slack map` adding an `inspect-` receipt on a real CRM.
- **Cards:**
  - `brief` exiting 4 with `check_failed`, and exit 130;
  - `brief` exiting 2 for a bad `--as-of` or brief ID;
  - `brief_save_failed` on a save that does not finish;
  - the seller list replaced or kept by `connect`, and `connect demo --seller` refused;
  - the extra account read of `brief` on a real CRM.
- **Deals:**
  - `POST /v1/deals` answering `409 source_changed` when the CRM is connected again during its reads;
  - `deal new` on Salesforce choosing the contract from today's UTC date without `--as-of`.
- **Audit:**
  - the five-minute lease of a pending receipt, and `serve` finishing interrupted receipts every 30 seconds after its sweep at start;
  - a restart resetting the rate counts;
  - a tool name over 100 characters recorded as `<invalid>`, and `brief_id` taken from `_meta`, up to 128 characters;
  - `audit --since` and `--until`, and `--follow` reading every half second.
- **MCP:**
  - the tool error text, and `Unknown tool: NAME` for a tool that is not served;
  - `account_number_requires_salesforce`, and `invalid_as_of` through `serve`;
  - `tools/list` not counting against the rate;
  - the two `.well-known` paths answering 200, and `GET /mcp` answering 405 with `Allow: POST`;
  - `token revoke` of a revoked token exiting 0, and of an unknown ID exiting 2;
  - `read_renewal_brief` answering `brief_not_found` for a card saved by `demo`;
  - the keys of a prepared brief and its `notice`;
  - a stdio call still running when input ends getting a receipt and no answer;
  - the `--selection` refusals, and the stdio `initialize` of a client that sends no protocol metadata;
  - the 45-second deadline and the 4 MiB limit of a prepared brief, with `brief_deadline_exceeded`, `brief_too_large` and `brief_storage_failed`.
- **Delivery bodies (API guide):** Slack showing an alert and a summary as the guide says, with the account's name unformatted, nothing linked by itself, and the limits as Slack counts them. No body has been posted to a Slack workspace.

</details>
