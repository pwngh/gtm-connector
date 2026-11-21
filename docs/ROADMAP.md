# Roadmap

This page lists what is being built and what is planned. Everything on it is a plan, not a
guarantee. The other docs describe what the code does today, and they claim only what a test shows.
When a planned item ships, its test ships with it, and the item leaves this page. The last section
lists the statements in the other docs that no test shows yet.

## v0.1.0: the Go program replaces the Python package (in progress)

v0.1.0 is read-only. It reads sources and writes nothing back to them. It ports what the Python
package does today, and adds nothing the package's recorded outputs cannot check. Those outputs, in
`testdata/oracle/`, are the expected results. Card text must match them byte for byte, and units
and gaps must match as values.

Today the Go program builds 208 of the 450 recorded cards:
- every card from the demo book, the varied demo books and the ten cases;
- 32 of the 82 cards built from imported calls.

Evidence from imported transcripts and live Slack messages is refused, not rendered, until its
checks are ported.

| Step | What it does | Done when |
| --- | --- | --- |
| Gate | Records that the Go program matches the Python package on what is ported so far, on recorded cards and on real public text. | The parity tests and `make verify-real` pass. The time to publish a card is measured and recorded. |
| Evidence | Ports the checks for imported transcripts and live Slack messages. | No evidence is refused as not ported, and real text still matches. |
| Sources | Ports the three sources:<br>- transcript import (TXT, VTT and SRT);<br>- one mapped Slack thread per account;<br>- Salesforce, through the `sf` CLI or a token given to the container.<br>A cancel stops a read, including the `sf` process it started. | Go builds all 450 recorded cards. |
| Commands and MCP | Ports the seven commands and `debug` to PostgreSQL. Serves MCP over HTTP and stdio, with today's read tools. Each tool call has one receipt. | The recorded command session replays against the binary. MCP passes the recorded request vectors and the Inspector. |
| Existing cards | Verifies cards the Python package made and keeps them byte for byte; they are never rebuilt. Sources are connected again and transcripts imported again. | The recorded card folders are admitted, and a changed byte is refused. |
| Cutover | Ships one binary with the web app inside it, built for macOS and Linux on arm64 and amd64, with checksums. Removes the Python package and its tests, and rewrites the docs for Go. | The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

**What changes for a user at cutover.**
- The binary, or `make up` with Docker Compose, replaces `pipx install`.
- The binary needs PostgreSQL 19 or 18.7, and `make up` runs one.
- The web app signs in the demo sign-in only. Real cards are read with the CLI and the MCP tools.

## After v0.1.0 (planned)

The work after v0.1.0 serves three groups:
- sales reps and CSMs, who work deals;
- RevOps and GTM engineers, who set up the sources and the rules;
- operators, who install and run the server.

One web app and one API serve all three. The CLI, the MCP tools and the alerts use the same API.

### For sales reps and CSMs: the daily loop

The **Deals** area of the web app. It never shows the plumbing (receipts, digests, connections).

1. **Approve suggested calls and Slack threads.** Calls and threads that match an account by participant email, domain or name wait for a person to approve or reject them. A badge counts the ones waiting. Nothing joins a deal on its own.
2. **Get a Slack alert when a renewal turns risky.**
   - It is sent when a deal's state changes, not on every read.
   - It links straight to the deal.
3. **See why the deal flagged.** Each flag cites its records and receipts:
   - customer silence against the days left to renewal;
   - close-date slippage, counted from the record's earlier versions;
   - a single-threaded deal, where one contact says every customer sentence;
   - a pricing or seat conflict, where a sentence states an amount that differs from the record. This is a candidate a person resolves, never a verdict;
   - a legal or security blocker, such as a rider, a redline or a security review holding up signature (see Open questions).
4. **Refresh before a call.** A button, a command and an API call read every source bound to the deal again and publish a new card.
5. **Read the deal card.**
   - Days to renewal and the recurring revenue.
   - What changed since the last card.
   - Buyer quotes with names and titles.
   - The seller's promises.
   - The sources that were not read.

   Today's card has the record values, the sentences grouped by speaker role, and what was not read. "What changed" and a section for the seller's promises are new.
6. **Open the proof behind any line.** A drawer shows the record, the call time or the Slack message, and the receipt of the read. On the CLI, `inspect` does this today.
7. **See the deal's communications.** The deal's calls, Slack threads and email, in time order, each opening its sentences.
8. **Ask an assistant.** Claude, Claude Code, Cursor or Codex, through MCP. Answers cite stored records.
9. **Work the playbook.** Each blocker becomes a work item, and it is resolved with cited proof. A review is bound to one plan revision.
10. **The deal closes.** Closed Won or Closed Lost stops the reads, seals the final card and archives the deal.

### For RevOps and GTM engineers: setup and control

The **Ops** area of the web app. It shows all of the plumbing:
- sources and connections, with their health;
- runs and failures;
- receipts and seals;
- rules and playbooks;
- history;
- access;
- alert deliveries.

1. **One configuration file** (planned name `gtm.yaml`). It holds:
   - the sources, the sellers and the excluded roles (for example minors or opposing parties in a legal matter);
   - the workspace time zone;
   - the watch rules and the alert rules.

   Today `connect` and `setup` configure one thing per command.
2. **Watch rules.** A rule enrols deals on its own, for example open renewals closing within a set number of days. A deal can also be registered by hand.
3. **One handle per deal.** The handle binds the IDs each tool uses:
   - Salesforce: the account, opportunity and contacts;
   - Slack: dedicated channels and threads in shared channels;
   - calls: participant emails and the account domain;
   - your own database: its IDs.
4. **Two ways data comes in.**
   - **Scheduled reads:** the CRM, Slack channels, call folders and transcript providers.
   - **Pushed data:**
     - a push API for records, transcripts, email or SMS;
     - a customer's own database, read through a read-only connection and declared queries.

   Each gets receipts, behind the same grants as the existing sources.
5. **The matches queue.** Discovered calls and threads wait for approval, as in step 1 of the daily loop, before any of their sentences reach a card.
6. **The checks between source and card.**
   - On main today:
     - whole sentences;
     - neutralized mentions and links;
     - speaker and role matching;
     - receipts before every read.
   - Planned:
     - HTML email turned into text;
     - a bad unit refused alone, with the card counting how many were;
     - the readiness and change rules.
7. **Outputs.**
   - An HTTP API with an OpenAPI description:
     - the deals;
     - refresh;
     - the card as JSON;
     - what changed;
     - the proof for each line.
   - MCP over HTTP and stdio, with `setup` printing the configuration for Claude Desktop, Claude Code and Cursor. The docs name a client only after it is tested live.
   - Slack alerts, and signed webhooks for two events: a deal's readiness changes, and its card changes.
   - Read-only SQL views over cards, units, gaps, receipts and readiness, for reporting tools. They answer, for example, how often close dates slip, how long customers go silent before renewal, how often blockers recur, and how often a stated figure disagrees with the record.
8. **Retractions.** A record entered by mistake is retracted by a new version. Later cards stop citing it, and its history stays.
9. **Outages.** When a source is down or rate-limited, the last sealed card is served with its read time, and the failure is stated.
10. **Close.** On Closed Won or Closed Lost, the reads stop and the final card is sealed.

**A deal's lifecycle:** discovered or registered, then watched, then needs attention, then paused or closed. A closed deal's last card is sealed.

### For operators: install, deployment and operations

| Item | Today | Planned |
| --- | --- | --- |
| Install | `make up` (Docker Compose) or `make dev` | The v0.1.0 binary, then a published container image and a Homebrew tap |
| First run | `setup` writes `.env` (0600), the credentials folder (0700), the local TLS certificate and the demo sign-in | `setup` also prints MCP client configuration |
| Database roles | An owner role for setup, backup and restore; an app role that cannot change the schema | Unchanged |
| Health | `/healthz` pings the database | `/healthz` also checks that migrations are current |
| Backups | `backup` and `restore`, with digests that detect corruption | Unchanged |
| Running as a service | None | systemd and launchd templates |
| Background work | None: reads happen in the request | Scheduled reads with leases, so one stuck read blocks nothing else |
| Upgrades | Migrations run at setup | Upgrades without downtime |

**Deployment shapes, each documented once tested:**
- a local CLI, with an assistant app over stdio MCP;
- a server in Docker Compose, for a team;
- a headless API, for another system to call.

### Model assistance and measurement

- **A model proposes, a person decides.** A model proposes units, labels and a playbook for a person to review, through two assistant tools on one sealed card: evidence and submit. No model writes text on a card.
- **The configuration is chosen by measurement.** The model, prompt and claim checks come from a benchmark on graded public and private transcripts, run locally and through hosted APIs.
- **More than one model family judges.** Judges come from more than one family, and disagreements go to a person.
- **Facts covered** is measured two ways:
  - against facts pooled from every run, so a model that finds something new gets credit;
  - against a census, in which a grader lists every important fact without seeing any model's output.
- **Evaluation.**
  - The ten cases as a table, with a model's unchecked answers on the same deals.
  - A trial on real renewals before the README's first limit changes.
- **Publishing.** Results are published only after they are confirmed on a held-out set.

### Evidence changes

Each of these differs from the Python package on purpose, so each waits until after the cutover:
- a Slack message keeps one identity across reads;
- an import stays readable after a parser upgrade;
- a bad unit is refused alone, and the card says how many were;
- one size cap on every card, stated on the card;
- an idempotent display for units sealed under older rules.

### Docs

These are written as each part ships, because a doc claims only what a test shows:
- a page for each group, with its flow drawn;
- an inventory that marks each part built or planned;
- a deployment guide;
- an FAQ;
- a comparison with vendor MCP servers, revenue intelligence products and retrieval pipelines.

### Later, when a real user needs them

- real sign-in in the web app, and team deployment;
- DOCX tracked changes as a source;
- Salesforce task creation;
- a second CRM;
- Windows;
- MCP Apps.

**Not planned:** language-specific SDKs, WASM or CGO builds. Every language talks to the binary over HTTP or MCP.

### Open questions

- **Legal and security blockers.** Does a person record each one on the playbook, or does a rule find it in the records? A rule may not judge wording, because source text never decides anything.
- **The approval badge.** Does it count only new calls and threads, or every item waiting on the rep?
- **Names.** Does the API stay under `/api/v1`, with deals as a new resource, or move to `/v1/deals`? Is the deal handle a new ID, or the CRM opportunity?
- **Paused.** Who pauses a deal, and what a paused deal still reads.

## Docs statements no test shows yet

An audit of the README, `SECURITY_REVIEW.md` and `docs/` checked 893 statements against the tests
on main. Tests show 759 of them. The audit found 13 that the code contradicts, and this change corrects them.
The 91 below describe the code as it reads, but no test asserts them yet. Each gets a test, or is
reworded, when the docs are rewritten for Go at cutover. Install and command instructions describe
the Python package, and the cutover rewrites them too.

<details>
<summary>README.md (7)</summary>

- `make dev`, `make demo` and `make test` do what the Go server section says.
- `POSTGRES=18.7` keeps the fallback database on its own volume.
- `make dev` and `make test` refuse a Go other than 1.27.4, and `make dev` a Node other than 22.23.4.
- `make verify-real` caches datasets in `~/.cache/gtm-connector/real` by default.
- The first run writes `.env` with mode 0600 and random passwords for two database roles.
- On Colima, the Makefile forwards a port that does not answer.
- It runs on macOS and Linux.

</details>

<details>
<summary>SECURITY_REVIEW.md (12)</summary>

- Each start generates a one-day loopback certificate.
- With `--selection`, `crm:read` gates `prepare_renewal_brief` and `read_renewal_brief`.
- A changed write payload returns `idempotency_conflict`.
- Response bodies are not stored in the audit log.
- `debug calls import` obtains only `crm:read`.
- A missing association publishes nothing.
- An `sf` command has at most 120 seconds, and the commands share a 180-second login deadline.
- `debug salesforce check` without `--sf-org` reads the environment's token, and that path cannot refresh it.
- Slack requests use verified TLS and ignore proxy settings from the environment.
- `debug serve` never reloads the selection file.
- `prepare_renewal_brief` has a 45-second deadline.
- A grant that expires while a tool runs still gets that one response, and both brief tools check the grant again before returning.

</details>

<details>
<summary>docs/audit.md (5)</summary>

- `--tool`, `--caller`, `--since` and `--until` select receipts, and `--tool save_brief` lists seals.
- `--follow` polls every half second, and `--status pending` never prints the later `ok` version.
- A request that disconnects before it runs gets no receipt.
- The rate limit is 60 calls per 60 seconds per token, `debug verify` uses four, and a restart resets the counts.
- `log_activity` requires an account ID, a kind, a body and an idempotency key.

</details>

<details>
<summary>docs/briefs.md (2)</summary>

- `brief` exits 1 with `local_failure` when a running connector holds the state folder.
- `brief` exits 4 with `check_failed` when the evidence fails a check.

</details>

<details>
<summary>docs/calls.md (10)</summary>

- An import needs exactly one of `--account` and `--account-number`; `--lines` is for TXT and `--cues` for VTT and SRT.
- `connect calls --remove` detaches transcripts and deletes nothing.
- An import resolves the account through a local `crm:read` grant.
- The file extension selects the parser.
- `debug calls list` and `debug calls inspect` show which imports state speaker names.
- An import saved by an earlier parser version reads as `calls_import_corrupt`.
- `calls_import_not_found` exits 2.
- `calls_account_not_found` exits 2.
- `calls_association_changed` and `calls_source_mismatch` exit 3, and `calls_import_corrupt`, `calls_association_invalid` and `calls_association_failed` exit 4.
- `calls_source_unavailable` exits 5.

</details>

<details>
<summary>docs/cli.md (9)</summary>

- JSON keys can be added, and existing keys keep their meaning and type within 0.1.
- Help stays plain text with `--json`.
- Exit 4, `check_failed`, means the evidence failed a check.
- With `--json` and no command, the output is an object with `description`, `try` and `help`.
- `inspect ACCOUNT_ID` and the names `accounts` lists are neutralized as a card's text is.
- `mcp stdio --json` changes only how a startup error is written.
- `debug completion --json` returns an object with `shell` and `script`.
- `debug serve` prints its address, state folder and connect command at start, and a fixed line at shutdown.
- `connect demo` keeps a Slack thread the folder already read connected.

</details>

<details>
<summary>docs/clients.md (10)</summary>

- `connect APP` does not launch an application.
- The printed commands use absolute paths for the state folder.
- Each printed command starts with the same environment variables.
- The merged configuration and its backup are written under temporary names and renamed into place.
- The bridge keeps its grant in memory.
- Every bridge call goes through the gateway, with its scope check, rate limit and receipt.
- After `debug serve` restarts, the app must restart too.
- Restarting `debug serve` replaces its endpoints and certificate and ends every earlier grant.
- Inspector 2.15.0 needs `npx` and Node 22.23.4, 24.22.1 or 26.12.1 or later.
- `read_call_notes` with only `crm:read` makes Inspector exit 1 with an insufficient-scope error.

</details>

<details>
<summary>docs/real-data.md (9)</summary>

- `make verify-real` takes `DATASETS`, `LOCAL` and `PYTHON`.
- The oracle runner refuses a package that differs from the one `testdata/oracle` was made from.
- The cache folder's default, and what `items.json` holds.
- The report gives, per function, how many items gave a non-empty output.
- The fields of a printed difference line.
- `report.json` lists the first 1,000 differences and counts the rest.
- The exit status is 1 when there are differences.
- How unpinned datasets and datasets with no converter are handled.
- An existing `FILE.r0` must be owned by you.

</details>

<details>
<summary>docs/salesforce.md (5)</summary>

- Each `sf` command may take 120 seconds, and the commands share a 180-second login deadline.
- `debug salesforce check` without `--sf-org` reads `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN`.
- A brief prepared for a client must also fit that tool's 4 MiB.
- Each request's network timeout is the remaining deadline, up to 5 seconds.
- `salesforce_invalid_configuration` exits 2.

</details>

<details>
<summary>docs/selection.md (11)</summary>

- The selection file must be a file you own.
- A restart of `debug serve` replaces every grant.
- `prepare_renewal_brief` and `read_renewal_brief` require `crm:read`.
- `as_of` must be `YYYY-MM-DD`, and Salesforce defaults to today.
- `prepare_renewal_brief` returns the same card as `brief`.
- The reads inside a prepared brief print no `debug serve` event lines.
- Preparing a brief has 45 seconds, and a read in progress keeps its 20-second limit.
- A saved brief is at most 4 MiB, with `brief_too_large` and `brief_storage_failed` as its errors.
- Logging out of the `sf` CLI does not withhold saved Salesforce content.
- An unmapped selected Slack thread stops `debug serve` with exit 3.
- `brief_rejected` is returned when the evidence fails its checks.

</details>

<details>
<summary>docs/slack.md (7)</summary>

- The redirect port must be 1024 to 65535.
- `--team-id` requires that workspace.
- The Slack requests in one `debug slack` command share a 20-second deadline.
- What `connect slack` does for a sign-in that needs a new login, and that `--remove` keeps the sign-in.
- The fields `disconnect` returns, and the order of scope lists.
- A `slack:read` grant can read every mapped account the server serves.
- The exit code of each error.

</details>

<details>
<summary>docs/tools.md (4)</summary>

- A demo database without the Slack file-count column is rejected.
- Subscriptions and legacy initialization sessions are not supported.
- An authorization code lasts 60 seconds.
- A restart ends every code and token.

</details>
