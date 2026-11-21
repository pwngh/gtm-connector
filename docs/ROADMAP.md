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
for byte, and units and gaps must match as values. The parity tests build 372 of the 450 recorded
cards today: every card but the 78 that read Salesforce. The 50 cards that read an imported
transcript and the 104 that read a live Slack thread read the records the Python import and the
Python Slack reader made, because Go does not import a transcript or read Slack yet.

| Step | What it will do | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirm that Go matches Python on what is ported so far | The parity tests pass at 372 of 450. `make verify-real` compares Python and Go on public text and JSON. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a sealed card read |
| Evidence | Show `[link unknown host]` for a link with an ambiguous host; that fix goes into Python first, so card text still matches. | Go checks imported transcripts and live Slack threads as Python does. | The link fix is in, and real text still matches |
| Sources | Port transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container | Go reads only the demo book | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Port the seven commands and `debug` to PostgreSQL, where `--state-dir` names a workspace. Serve MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. One outer receipt per tool call, plus one per read inside a prepared brief. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`. The MCP gate is written but not served: `/mcp` answers 404. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Open the cards and receipts the Python package made. They are verified and kept byte for byte, never rebuilt. | Go verifies both kinds of Python card in tests. No command admits them yet. | The four recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing |
| Cutover | Ship one binary with the web app inside it (`go:embed`), for macOS and Linux on arm64 and amd64, with checksums. Remove the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrite the docs for Go. | `make build` and the image build one binary with the web app inside it. `make release` builds it for macOS and Linux on arm64 and amd64 and writes `SHA256SUMS`; CI runs it and checks the four binaries against their sums. A version tag attaches the five files to a GitHub release. No tag has been pushed, so there is no release to download, and the Python package is still here. | The binary, run outside a checkout, serves a deep-linked Evidence page and its assets. A backup taken before the cutover restores. The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

### What changes for you at cutover

You will install the binary, or run `make up` with Docker Compose, where you ran `pipx install`.
The binary needs PostgreSQL 19 or 18.7; `make up` runs one.

The commands, their exit codes and their `--json` output will stay, and card text will be the same
byte for byte. The one exception is writing: `log_activity` and the `crm:write` scope will be
removed, and a write attempt will be refused with a receipt.

State will live in PostgreSQL, so `--state-dir` will name a workspace there and not a folder. The
cards and receipts the Python package made will still open and verify. Nothing else will move. You
will connect your sources again, import your transcripts again, map your Slack threads again, and
set your seller lists and selection files again. You will also set up your assistant apps again,
because their saved entries start the Python package.

The web app will show Evidence and Readiness for the demo sign-in and hide the walkthrough pages.
Real cards will be read with the CLI and the MCP tools.

No live org has been verified. v0.1.0 does not wait for one.

## Not built yet, in order

Nothing below is started unless it says so. Each links to the guide that describes it.

### 1. First call

One rep reads one real renewal before a call.

- A deal as one renewal of an account, with a readable ID, registered by hand ([the API](api.md#one-deal-one-id)).
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
- The Ops screens: Coverage, Setup and Activity ([the Ops area](config.md#the-ops-area)).
- Pause and resume, and one size cap on every card.

### 3. Delivery

- Slack alerts, signed webhooks and the three events ([the API](api.md#webhooks-and-slack-alerts)).
- Tokens for scripts and assistants, the flagged-deals feed, and the OpenAPI file.
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

The other docs make the statements below, and no test asserts them yet. Each will get a test, or
narrower wording, when the docs are rewritten for Go. The statements are grouped by the v0.1.0 step
that owns the check.

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
  - its `DATASETS`, `LOCAL` and `PYTHON` options;
  - its default cache folder and the counts in `items.json`;
  - the count of non-empty outputs per function;
  - the fields of a printed difference line;
  - `report.json` listing only the first 1,000 differences;
  - the `realdata` command exiting 1 on a difference;
  - `realdata fetch` for a pinned dataset with no converter, and an unnamed run reporting the datasets it skips;
  - refusing an existing `FILE.r0` owned by someone else;
  - refusing an oracle package that differs from the recorded one.

</details>

<details>
<summary>Sources (Salesforce, transcripts, Slack)</summary>

- **Salesforce:**
  - each `sf` command's 120-second limit and the shared 180-second login deadline;
  - `debug salesforce check` without `--sf-org` reading `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN`, and that path not refreshing the token;
  - each request's network timeout being the remaining deadline, up to 5 seconds;
  - `salesforce_invalid_configuration` exiting 2.
- **Transcripts:**
  - an import needing exactly one of `--account` and `--account-number`, with `--lines` only for TXT and `--cues` only for VTT and SRT;
  - the file extension choosing the parser, and an unsupported one refused through the CLI;
  - an import resolving the account through a local grant that holds only `crm:read`;
  - `connect calls --remove` detaching transcripts and deleting nothing;
  - `debug calls inspect` showing whether an import states speaker names;
  - an import saved by an earlier parser version reading as `calls_import_corrupt`;
  - a missing association publishing nothing;
  - the exit codes:
    - `calls_import_not_found` and `calls_account_not_found` exit 2;
    - `calls_association_changed` and `calls_source_mismatch` exit 3;
    - `calls_import_corrupt`, `calls_association_invalid` and `calls_association_failed` exit 4;
    - `calls_source_unavailable` exits 5.
- **Slack:**
  - the redirect port range, 1024 to 65535;
  - `--team-id` refusing a grant for another workspace;
  - one 20-second deadline across the Slack requests of one `debug slack` command;
  - `connect slack`:
    - exiting 3, without contacting Slack, when the sign-in needs a new login;
    - working beside Salesforce;
    - keeping the sign-in on `--remove`;
  - the fields `disconnect` returns, the order of scope lists, and `check` naming the first failed step's recovery command;
  - a `slack:read` grant reading every mapped account the server serves;
  - the exit code of each error reason in the Slack guide;
  - verified TLS for Slack requests, ignoring proxy settings from the environment;
  - live messages ordered by their Slack timestamp, and demo times with different UTC offsets sorting by their text;
  - a demo database without the Slack file-count column being rejected.

</details>

<details>
<summary>Commands and MCP (CLI, audit, selection, clients, protocol)</summary>

- **The CLI:**
  - JSON keys added within 0.1 keeping existing keys' meaning and type;
  - help staying plain text with `--json`;
  - `brief` exiting 1 with `local_failure` when a running connector holds the state folder, and 4 with `check_failed` when evidence fails a check;
  - `brief` with a connected calls source and no imported transcript exiting 0 with the gap on the card;
  - `inspect ACCOUNT_ID` and the names `accounts` lists being neutralized as card text is;
  - `mcp stdio --json` changing only how a startup error is written;
  - `debug completion --json` returning `shell` and `script`;
  - `debug serve`'s startup lines and its fixed shutdown line;
  - `connect demo` keeping an already-read Slack thread connected.
- **Audit and the request path:**
  - `audit --caller`, `--since` and `--until`, and a tool filter other than `save_brief`;
  - `audit --follow` polling every half second, and `--status pending` never printing the later `ok` version;
  - an incomplete request that disconnects before it is classified getting no receipt;
  - the default rate limit of 60 calls per 60 seconds per token, reset by a restart;
  - response bodies left out of the audit log;
  - `log_activity` requiring its four arguments, and a changed payload returning `idempotency_conflict`. Both leave with the tool at cutover.
- **Selection and prepared briefs:**
  - the selection file owned by you, and never reloaded;
  - `prepare_renewal_brief` and `read_renewal_brief` requiring `crm:read`;
  - `as_of` requiring `YYYY-MM-DD`, and Salesforce defaulting to today;
  - a prepared brief giving the same evidence and gaps as `brief` for the same inputs, scopes and cutoff;
  - the reads inside a prepared brief printing no `debug serve` event lines;
  - the 45-second deadline, with a read in progress keeping its 20-second limit;
  - the 4 MiB limit, with its `brief_too_large` and `brief_storage_failed` errors;
  - `brief_rejected` when evidence fails its checks;
  - logging out of `sf` not withholding saved Salesforce content;
  - an unmapped selected Slack thread stopping `debug serve` with exit 3;
  - a saved-brief read refusing release when its grant expired after admission, and what an ordinary call does when its grant expires mid-call. The final check in `prepare_renewal_brief` is tested.
- **Clients and the protocol:**
  - `connect APP` launching no application;
  - absolute state-folder paths in printed commands;
  - the merged configuration and its backup written under temporary names and renamed into place;
  - the bridge keeping its grant in memory and applying the rate limit;
  - a restarted `debug serve` ending earlier grants, endpoints and certificates, so the app must restart too;
  - Inspector 2.15.0's `npx` and Node requirements, and its exact exit code and error text for a missing scope;
  - an authorization code lasting 60 seconds, and a restart ending every code and token;
  - subscriptions and legacy initialization sessions being unsupported;
  - each start generating a one-day loopback certificate.

</details>
