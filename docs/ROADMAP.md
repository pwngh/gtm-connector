# Roadmap

The [README](../README.md) and the guides describe the product as designed. This page says what is
built, what is not, and the order it will be built in. An item leaves this page when it ships with
its test.

## v0.1.0: one Go binary replaces the Python package

This is in progress. v0.1.0 will be the Python package's behaviour in one Go binary on PostgreSQL,
and nothing more. It will read the same sources and write nothing back to any of them.

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
cards today. The 50 cards that read an imported transcript are built from the recorded file: Go
parses it, saves the import in PostgreSQL and reads it back. All 82 cards of that book are also made
through `debug calls import` and `brief`, and match apart from the recorder's clock and IDs and the
Slack line, which the recorder did not request. The 104 that read a live Slack thread
read the records the Python Slack reader made, and the binary makes the same 114 cards of those two
families through `connect slack`, `debug slack map` and `brief` against an invented Slack, with the
recorded text but for the line that gives when the sources were read. The 78 Salesforce
cards read the recorded rows through the Go reader, served by an invented org.

| Step | What it will do | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirm that Go matches Python on what is ported so far | The parity tests pass at 450 of 450. `make verify-real` compares Go on public text and JSON with the digests in `internal/realdata/recorded`. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a sealed card read |
| Evidence | Neutralize a bare mail address such as `dana@files.example`, which a GitHub Markdown client links. Show the host of an `xn--` label that decodes to a valid name, which takes the IDNA tables. | A link shows the host a browser opens, read as the URL Standard reads it and without a leading `www.`, or `[link unknown host]` when a browser would open another host or none: a backslash, a control character, a tab or a line break before the path, no `//` after the scheme, a scheme other than `http`, `https`, `ftp`, `ws` and `wss`, a numeric host a browser rewrites, a port past 65535, a host in brackets, or an `xn--` label. A Markdown link is replaced whole, and an angle-bracket link, a mail address in angle brackets and a reference definition where a paragraph starts are neutralized, with their escapes and character references read. Neutralizing twice gives the same text. Go checks imported transcripts and live Slack threads as Python does. Cards are made under neutralizer version 4, and `inspect`, the web API and the brief tools show a unit sealed under an older version under today's rules, with the card's bytes unchanged. | Each of those forms has a test, and real text still matches |
| Sources | Port transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container | Go reads the demo book and a Salesforce org through `sf`. `connect salesforce --org ALIAS --state-dir NAME` signs in, checks the org against `--environment` and pins the workspace to its identity and the user it reads as, and refuses another org or user there, also after a removal and after a login refresh. An `--org` that cannot be an `sf` alias, such as a token, never reaches `sf`. `accounts`, `inspect`, `brief` and `audit` then read it, each read under a receipt, and `debug salesforce check` reports the sign-in, the environment, each object's fields and, in a connected workspace, the contract a renewal plans from. Tests run every command against an invented org and an invented `sf`: `brief` makes the CRM part of all 78 recorded Salesforce cards byte for byte (the recording read calls and Slack from demo data, which Go never reads beside a real CRM), and a cancelled `connect` ends the `sf` process group and saves nothing. `connect salesforce --token-env` reads the org's address and a token from `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN` or the file `SALESFORCE_ACCESS_TOKEN_FILE` names, with the same checks and pin as an `sf` login, and on a first HTTP 401 reads them once more and keeps only a token of the same org and user, in tests against the invented org. `connect calls` connects imported transcripts beside the demo CRM, or beside a CRM a test registers, and `debug calls import`, `list` and `inspect` work as the Python CLI's do, except `--account-number`, which is refused as not in this build. An import's account is resolved by a `get_account` read under its own receipt, and `brief` reads the account's active import. `demo` and the MCP tools read calls through the same source, untested with imports. Go signs in to Slack (`debug slack connect`, with PKCE and the loopback callback), keeps the sign-in in the credentials folder, connects it (`connect slack`) beside a real CRM, maps one thread per account (`debug slack map`), checks its read (`debug slack check`) and reads it onto a card, in tests against an invented Slack. | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Port the seven commands and `debug` to PostgreSQL, where `--state-dir` names a workspace. Serve MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. One outer receipt per tool call, plus one per read inside a prepared brief. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`, and `connect`, `accounts`, `brief`, `inspect` and `audit` on demo data and Salesforce, where `--state-dir` names a workspace and the default is `demo`. `connect` attaches and detaches the demo data, Salesforce and imported transcripts and shows what is connected. Salesforce goes in a workspace named with `--state-dir`, because the default one holds demo data. `connect slack` connects the saved Slack sign-in beside a real CRM. `connect` refuses the assistant apps as not in this build, and nothing reads demo data in their place. `demo --state-dir` is the recorded demo: one renewal with a lookup and a refused read. The binary matches 33 of the 33 recorded steps, 8 of them apart from the recorder's clock. `debug calls`, `debug salesforce check` and `debug slack` are ported. The rest of `debug` is not. `serve` answers MCP over HTTPS at `/mcp` for each token in the workspace it was issued in: the read tools over the demo book or a Salesforce org, for the accounts the token's principal is granted, and with `--selection FILE` the two brief tools, for the demo workspace only. The recorded request vectors pass there, and the write vectors are refused with a receipt. A token that carries any scope but the three read scopes is refused. `token new` issues a read-only token for one workspace, shown once, and `token list` and `token revoke` manage them. `mcp stdio` serves a workspace's read tools on stdin and stdout under the same gate, reading the sources as they are connected at each call, and the recorded stdio bridge tests pass through it on PostgreSQL. The Inspector runs against the binary over HTTPS and stdio. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Open the cards and receipts the Python package made. They are verified and kept byte for byte, never rebuilt. | Go verifies both kinds of Python card in tests. No command admits them yet. | The four recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing |
| Cutover | Ship one binary with the web app inside it (`go:embed`), for macOS and Linux on arm64 and amd64, with checksums. Remove the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrite the docs for Go. | `make build` and the image build one binary with the web app inside it. `make release` builds it for macOS and Linux on arm64 and amd64 and writes `SHA256SUMS`; CI runs it and checks the four binaries against their sums. A version tag attaches the five files to a GitHub release. No tag has been pushed, so there is no release to download. The Python package, its tests, `tools/oracle/` and their CI jobs are removed, and the guides under `docs/` describe the Go binary. The Go tests pass without Python, and `make verify-real` compares Go on public text with the digests in `internal/realdata/recorded`. | The binary, run outside a checkout, serves a deep-linked Deal card and its assets. A backup taken before the cutover restores. |

### What changes for you at cutover

The Python package is gone from the repository. Build the binary with `make build`, or run
`make up` with Docker Compose, where you ran `pipx install`. The binary needs PostgreSQL 19 or 18.7;
`make up` runs one.

The commands, their exit codes and their `--json` output stay, and card text is the same byte for
byte. The one exception is writing: `log_activity` and the `crm:write` scope are removed, and a
write attempt is refused with a receipt.

State lives in PostgreSQL, so `--state-dir` names a workspace there and not a folder. The cards and
receipts the Python package made verify in tests, and no command admits them yet (Existing cards,
above). Nothing else moves. Connect your sources again, import your transcripts again, map your Slack
threads again, and set your seller lists and selection files again. Set up your assistant apps again
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

No live org has been verified. v0.1.0 does not wait for one.

## Not built yet, in order

Nothing below is started unless it says so. Each links to the guide that describes it.

### 1. First call

One rep reads one real renewal before a call.

- A deal as one renewal of an account, with a readable ID, registered by hand ([the API](api.md#one-deal-one-id)), the `/v1/deals` routes ([deal routes](api.md#deal-routes)) and the `deal` command.
- Sign-in through the company's identity provider, and each person reading only their deals.
- Calls and Slack threads matched to the deal ([setting it up](config.md#how-a-deal-gets-its-evidence)).
- The Deals page: the deal list, the card, the proof drawer and communications ([working a deal](deals.md#the-page)).
- What changed since the reader last looked, and marks for blockers and commitments ([working a deal](deals.md#the-card)).
- Refresh on demand, and close with a final sealed card.
- Underneath: a Slack message keeps one identity across reads, and an import stays readable after a parser upgrade.

Before real people sign in, the security review is redone for more than one person. Then the
trial: five real renewals, run outside the repository, with a rep given 30 seconds per renewal to
find the status, the change, who said what, the commitment and the blocker. Only counts are
recorded. The README's limit about real deals changes only then.

### 2. Whole book

RevOps covers every renewal and sees which have no current card.

- `gtm.yaml`, previewed and activated by exact revision ([setting it up](config.md)).
- Field mapping, watch rules, transcript folders, Slack channel windows, Gong and Zoom.
- A participant directory that matches speakers to contacts by email or phone first.
- The five flags, with thresholds and a preview ([what flags a deal](deals.md#what-flags-a-deal)).
- Scheduled reads under leases, each as a named service identity.
- The Ops screens: Setup; on Activity, what ran across the book and whether a call is safe to retry; and on Coverage each source's state, why a deal has no current card, and the matches left off by rule ([the Ops area](config.md#the-ops-area)).
- Pause and resume, and one size cap on every card.

### 3. Delivery

- Sending Slack alerts and signed webhooks for the three events: the destination and its secret, retries, the delivery log, alert rules, and the Slack link summary in a configured channel. The bodies and the signature are built ([what a delivery holds](api.md#what-a-delivery-holds)), from a deal status and rule results that the five flags will give.
- Tokens for scripts and assistants, the flagged-deals feed, and the OpenAPI file.
- `setup` printing the MCP configuration for each assistant client.
- MCP tools that read deals, cards and proof. Clients are named in the docs only after a live test: Claude Desktop, Claude Code, ChatGPT, Codex and Cursor are untested today.

### 4. Your own data

- The push API and retractions ([pushing your own data](api.md#pushing-your-own-data)).
- A read-only view of a customer's database as a source.
- Read-only SQL views for reporting.
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

- The release pipeline, Docker images and a Homebrew tap ([running it](operations.md#install)).
- systemd and launchd templates.
- The ten cases as ready-to-open demo workspaces.

## Later

Salesforce sign-in through a connected app, DOCX tracked changes as a source, Salesforce task
creation, a second CRM, Windows, and MCP Apps. Language SDKs, WASM and CGO builds are not planned:
integrations use HTTP or MCP.

## Open questions

- May a person close a deal, or does only the CRM stage close it?
- Should one bad unit stop the card, as today, or be refused alone with the card saying so?
- Should slippage count changes from before the connector's first read, using the CRM's field history?
- Should Slack authors be matched to contacts, so that silence can count Slack?
- A call or thread that fits two deals of one account. A deal with no CRM record. A closed opportunity that reopens.
- Reporting SQL through a database role that BI tools connect to, or through the API with a receipt for each query?
- Is a grant enough to see Ops, or is it a separate role?

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
