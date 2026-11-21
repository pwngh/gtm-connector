# Roadmap

This page lists what is being built, in order, and what is planned. Everything on it is a plan,
not a guarantee. Planned work is stated only here. The other docs describe what the code does
today and claim only what a test shows; when a planned item ships, its test ships with it and the
item leaves this page. The last section lists the checks the other docs still need.

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

## v0.1.0: one Go binary replaces the Python package (in progress)

v0.1.0 replaces the Python package with one Go binary on PostgreSQL. It reads the same sources,
opens the cards the Python package made, and serves the demo Evidence and Readiness pages in the
browser. It is read-only: it writes nothing back to any source. It ports what the Python package
does today, and the package's recorded outputs in `testdata/oracle/` are the expected results. Card
text matches them byte for byte, and units and gaps match as values.

The parity tests build 208 of the 450 recorded cards. That is every card from the demo book, the
varied demo books and the test cases, and the 32 call-book cards whose accounts have no imported
transcript. Evidence from imported transcripts and live Slack messages is refused, not rendered,
until its checks are ported.

| Step | What it does | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirms that Go matches Python on what is ported so far | The parity tests pass at 208 of 450. `make verify-real` compares Python and Go on public text. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a card read |
| Evidence | Ports the checks for imported transcripts and live Slack messages. A link with an ambiguous host shows `[link unknown host]`, fixed in Python first so card text still matches. | Both kinds of evidence are refused as not ported | No evidence is refused as not ported, bad evidence is still refused, and real text still matches |
| Sources | Ports transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container. A cancel stops a read, including the `sf` process it started. | Go reads only the demo book | Go builds all 450 recorded cards, in memory and on PostgreSQL, and a cancelled read ends its process and publishes no card |
| Commands and MCP | Ports the read commands and `debug` to PostgreSQL. Serves MCP over HTTPS and stdio with the read tools, under the same scope, selection and expiry checks. Each tool call has one outer receipt, and a prepared brief adds a receipt for each read. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`. The MCP gate is written but not served: `/mcp` answers 404. | The recorded command session replays against the binary. The recorded request vectors pass on both transports, and the Inspector runs against the binary. |
| Existing cards | Opens the cards and receipts the Python package made, verified and kept byte for byte, never rebuilt | Go verifies both kinds of Python card in tests. No command admits them yet. | The recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing |
| Cutover | Ships one binary that embeds the web app (`go:embed`), built for macOS and Linux on arm64 and amd64, with checksums. `/healthz` also checks that migrations are current. Removes the Python package and its tests, and rewrites the docs for Go. | `serve` reads the web app from disk | The binary, run outside a checkout, serves a deep-linked Evidence page and its assets. The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

**What changes for you at cutover.**
- The binary, or `make up` with Docker Compose, replaces `pipx install`. The binary needs PostgreSQL 19 or 18.7, and `make up` runs one.
- The commands, their exit codes and their `--json` output stay. Card text stays the same byte for byte.
- State lives in PostgreSQL, not in a folder of SQLite files. Cards and receipts the Python package made still open and verify, and nothing else moves:
  - sources are connected again;
  - transcripts are imported again;
  - Slack threads are mapped again;
  - seller lists and selection files are set again.
- Assistant apps are set up again, because their saved entries start the Python package.
- `log_activity` and the `crm:write` scope are removed. v0.1.0 offers read tools only, and a write attempt is refused with a receipt.
- The web app shows Evidence and Readiness for the demo sign-in. Real cards are read with the CLI and the MCP tools.
- No live CRM org has been verified, and v0.1.0 does not wait for one.

## After v0.1.0, in order

Each milestone says who can do what once it ships. The order puts real use first: one operator on
real renewals, then a team, then the places people already work, then the controls RevOps needs.

### 1. One operator can watch real renewals

For RevOps or the owner, running it on their own machine against their own CRM.

- **What a deal is.** A deal is one renewal of an account. Today a card is for an account.
  - One handle per deal binds the IDs each tool uses:
    - Salesforce: the account, opportunity and contacts;
    - Slack: channels and threads;
    - calls: participant emails and the account domain;
    - your own database: its IDs.
  - New links are added as sources are found.
- **Field mapping.** Most CRM orgs keep the renewal date, recurring revenue and stage in custom fields or objects. Setup maps the deal's fields to the org's and checks the mapping. Today the connector reads fixed standard fields.
- **Watch rules.** A rule enrols deals on its own, for example open renewals closing within a set number of days, and a person can register a deal by hand.
- **History on day one.** When a deal is first watched, a backfill reads recent calls and threads, and the CRM's field history for close-date changes. "What changed" and the flags then work from the start.
- **Transcript folders.** A folder of transcripts is connected once. It replaces importing one file at a time and hand-picked excerpts.
- **A Slack channel window.** A bounded window of a channel is read. It replaces mapping one thread per account, and the card counts what was not read and says when more exists.
- **The matches queue.**
  - Calls and threads that match a deal by participant email, account domain or name wait for a person to approve or reject them.
  - Nothing joins a deal on its own, and a rejected match adds nothing to any card.
- **People.** A participant directory matches speakers to contacts by email or phone first, and by name only when the name is unique. An ambiguous speaker is marked. People whose words must never appear on a card are excluded.
- **Readiness flags.** Each flag cites its records and receipts, and records the rule version that raised it. Thresholds are set per workspace. A check shows one of four results: not evaluated, not applicable, failed, or no blocker found.
  - **Customer silence:** the last customer reply, against the days left to renewal.
  - **Close-date slippage:** counted from the record's versions and its field history.
  - **A single-threaded deal:** one contact says every customer sentence.
  - **A pricing or seat conflict:** a sentence states an amount that differs from the record. It is a candidate a person resolves, never a verdict.
  - **A legal or security blocker,** such as a rider, a redline or a security review holding up signature (see Open questions).
- **What changed since the last card:** the units, record versions and gaps that differ.
- **Refresh.** A button, a command and an API call read every source linked to the deal again, under the caller's grants.
  - A successful refresh publishes a new sealed card.
  - If a read fails or the evidence fails its checks, the last sealed card stays, with its read time and the failure stated. With no earlier card, only the failure shows.
- **Scheduled reads.** Background reads with leases, so one stuck read blocks nothing else. Temporary failures are retried with backoff.

**Then the trial.** The owner runs it on five real renewals before milestone 2. Each parser failure
becomes an invented test fixture. The README's first limit, that nothing was tried on a real deal,
changes only then.

### 2. A team can use it

For sales reps, CSMs and their managers, on one shared server.

- **Sign-in.**
  - Every person signs in through the company's identity provider, and every action is recorded under that sign-in.
  - One sign-in serves both areas.
  - A grant decides which deals a person reads and whether they see Ops.
- **Who sees what.**
  - Deals come from the CRM owner, team or territory: a rep sees their deals, and a manager sees their team's.
  - Restricted channels and excluded people stay off the cards of those not allowed to see them.
- **Tokens for scripts and assistants.** A token is limited to selected deals and scopes, and expires.
- **The Deals area.** Home for reps and CSMs. It shows quotes, sources and times, never receipts, digests or connections.
  - **The deal list,** on the left: the active deals, most urgent first. Red is a risk or a blocker, amber is a change since the last card, and green is on track. A badge at the top counts the calls and threads waiting for approval.
  - **The deal card,** in the main pane: what changed comes first, then the risks and blockers. It also shows:
    - days to renewal and the recurring revenue, or says that the source does not supply them;
    - the stakeholders, with each contact's role, when they last spoke, and who has gone quiet;
    - buyer quotes with names and titles;
    - the seller's commitments (see Open questions);
    - the sources that were not read.
  - **The proof drawer.** Any line opens its quote, its speaker and time, and the record, call or Slack message it came from. The read's receipt is in Ops.
  - **Communications.** The deal's calls, Slack threads and email in time order, each opening its sentences.
  - **The work queue.** Work items across the rep's deals, by due date and owner.
- **Playbooks and review.**
  - Each blocker gets a plan from a playbook. There are three: obtain the current rider, review a renewal condition, and reconcile an approval conflict.
  - A signed-in person reviews the plan and records the owner, the next step and the evidence required. A review is bound to one plan revision: an edit needs review again, and a change to the cited evidence marks it for re-review. A refresh of the same evidence keeps the review.
  - A document handoff records the version received and its receipt, and is accepted or rejected with a reason.
  - History shows who decided what, on which evidence, and what resolved each blocker.
- **Close and pause.** A deal closes when its opportunity reaches Closed Won or Closed Lost, or when a person closes it. The reads stop, the last card is sealed, and the deal leaves the active list. Its cards, receipts and history stay readable to those allowed. A person can pause a deal (see Open questions).

**A deal's lifecycle:**
1. Discovered by a watch rule, or registered by hand.
2. Watched.
3. Needs attention when a flag is raised, and watched again when it clears.
4. Paused.
5. Closed.

A watched deal can close without ever needing attention.

### 3. People hear about it where they work

- **Slack alerts.**
  - These are the connector's first outbound messages. They are off until configured, and the connector still writes nothing to the CRM. They need a Slack app with a posting scope, which the operator creates.
  - An alert is sent when a deal's readiness changes, not on every read. It names the deal, its days to renewal and the flag, and links to the deal.
  - The settings choose who gets it (the owner, a channel or the manager), a daily digest instead of single alerts, and quiet hours.
  - A failed delivery is retried, and Ops shows each attempt.
- **A feed of flagged deals.** The API lists the deals that need attention, with links, so a team's own bot or script can post them.
- **Webhooks.** Signed deliveries that subscribers verify, with retries and a delivery log. The first event is "readiness changed" (see Open questions for the second).
- **Assistants.** `setup` prints the MCP configuration for Claude Desktop, Claude Code and Cursor. The MCP tools read the same deals, cards and proof. The docs name a client only after it is tested live; Inspector is the tested client today.
- **Email.** Email becomes a source, because reps and CSMs write to customers there most (see Open questions).

### 4. RevOps runs it as a system

- **One configuration file** (planned name `gtm.yaml`). It holds:
  - sources and field mappings;
  - sellers and excluded people;
  - the workspace time zone;
  - watch rules, flag thresholds and alert rules.

  It is kept in Git, and checked before it applies.
- **Rule tuning.**
  - A changed threshold or rule is previewed against current and past deals before its alerts go live.
  - Every flag records the rule version that raised it.
- **Coverage.** Which watched deals have no matched calls or threads, which sources failed their last read, and how long the matches queue is.
- **Rollups.** Recurring revenue at risk by team and by flag, and slippage over time. These are computed by the connector from the flags, never by a model.
- **Read-only SQL.** Views over cards, units, gaps, receipts and readiness, through a reporting role that cannot see credentials or sign-ins. They answer, for example:
  - how often close dates slip;
  - how long customers go silent before renewal;
  - how often blockers recur;
  - how often a stated figure disagrees with the record.
- **Your own data.**
  - **The push API:** a published JSON schema for deals, contacts and messages. Each push gets a receipt, is idempotent by the caller's key, and is tracked by a cursor. A push that fails a check is refused with a receipt. Rows from another CRM, a spreadsheet or product usage come in this way.
  - **A customer's own database:** read through a read-only connection and declared queries.
- **Retractions.** A record entered by mistake is retracted by a new version. Later cards stop citing it, and its history stays.
- **Retention and deletion.** A retention period per workspace, and deletion on request, with a receipt of the deletion.
- **The Ops area.** It shows all of the plumbing:
  - sources and connections, with health, last read and credential state;
  - runs and failures, with the receipts, refusals and gaps of each;
  - receipts and seals, with a seal recheck;
  - rules and playbooks;
  - decision history;
  - grants;
  - alert subscriptions and deliveries.

### 5. A model proposes, a person decides

- **No model is needed to build a card.** Model assistance is opt-in, and it can run on a local model so that no source text leaves the machine. A hosted model receives source text only when the operator chooses one.
- **Proposals.** A model proposes units, labels and a playbook through two assistant tools on one sealed card.
  - The evidence tool returns numbered units.
  - The submit tool accepts unit numbers, a label from a fixed list checked against each speaker's role, and pairs marked in tension. A cut sentence, an unknown label or a unit of another account is refused with a receipt.
  - A person reviews every proposal.
- **The model, prompt and checks are chosen by measurement.**
  - Every run, local or hosted, gets a benchmark score: useful claims, wrong claims, facts covered, time and cost.
  - Judges come from more than one model family, and a person checks their disagreements and a sample of their agreements.
  - Tuning uses development data, and the choice is confirmed once on held-out data. Results are published only after that.
- **Facts covered,** measured two ways:
  - against a pool of confirmed facts from every run, re-scored when a new fact is confirmed, so a model that finds something new gets credit;
  - against a census, in which a grader who has seen no model output lists the important facts.
- **Evaluation.** `evaluate` runs the ten cases as a table, with a model's unchecked answers on the same deals kept as test inputs and scored offline. Two of the ten cases wait for features:
  - a blocker buried under routine messages waits for the assistant's ranking;
  - a channel over the cap waits for the Slack channel window.

### 6. Install and run it like any server

- A published container image and a Homebrew tap.
- systemd and launchd templates.
- Upgrades that apply migrations without stopping reads.
- The ten cases as ready-to-open demo workspaces, so a new user sees each one in the web app without credentials.
- **Three deployment shapes,** each documented once tested:
  - a local CLI, with an assistant app over stdio MCP;
  - a server in Docker Compose, for a team;
  - a headless API, for another system to call.

## Evidence changes after v0.1.0

Each of these differs from the Python package on purpose, so each waits until after the cutover:
- each unit binds to the receipt of the read that returned it, even when one tool reads twice;
- a Slack message keeps one identity across reads, and an edit or a verified deletion supersedes the version it changes;
- an import stays readable after a parser upgrade, and a reparse is a new revision;
- a bad unit is refused alone, and the card says how many were;
- HTML email is turned into text;
- one size cap on every card, stated on the card;
- an idempotent display for units sealed under older rules, labelled when it differs from the stored text.

## Docs, as each part ships

- A page for each group, with its flow drawn.
- A setup guide for each source.
- An inventory that marks each part built or planned.
- A deployment guide.
- An FAQ: other deal types, offline use, permissions, outages and retractions.
- A comparison with vendor MCP servers, revenue intelligence products and retrieval pipelines.

## Later, when a real user needs them

- Salesforce sign-in through a connected app.
- Transcript provider adapters.
- DOCX tracked changes as a source.
- Salesforce task creation.
- A second CRM.
- Windows.
- MCP Apps.

**Not planned:** language SDKs, WASM or CGO builds. Integrations use HTTP or MCP.

## Open questions

- **Legal and security blockers.** Does a person record each one on the playbook, or does a rule find it in the records? A rule may not judge wording, because source text never decides anything.
- **The seller's commitments.** Wording cannot decide which seller sentence is a commitment. Does a person mark it, or does the section hold every seller sentence with a date or an amount?
- **Silence and Slack.** A Slack author is never a customer today, so silence counts calls and email only, until Slack authors are matched to contacts.
- **What a deal is, at the edges.**
  - An account with two open renewals.
  - A deal with no CRM record.
  - A closed opportunity that reopens.
  - A renewal date that passes while the opportunity is still open.
  - An opportunity that is deleted or leaves the watch rule.
- **Pause.** Who pauses a deal, and what a paused deal still reads.
- **The approval badge.** Does it count only new calls and threads, or every item waiting on the rep?
- **Ops access.** Is a grant enough, or is Ops a separate role?
- **Alerts.** May an alert quote source text, or only name the flag?
- **The second webhook event.** A changed blocker, a changed card, or both?
- **Email.** A mailbox connector, or a forwarding address?
- **Names.** Does the API stay under `/api/v1` with deals as a new resource? Is the deal handle a new ID, or the CRM opportunity?

## Checks the docs still need

The other docs make the statements below about the Python package, but no test asserts them yet.
Each gets a test, or is reworded, when the docs are rewritten for Go. They are grouped by the
v0.1.0 step that ports the behavior.

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
  - exit status 1 on a difference;
  - its handling of unpinned datasets and datasets with no converter;
  - refusing an existing `FILE.r0` owned by someone else;
  - refusing an oracle package that differs from the recorded one.

</details>

<details>
<summary>Sources (Salesforce, transcripts, Slack)</summary>

- **Salesforce:**
  - each `sf` command's 120-second limit and the shared 180-second login deadline;
  - `debug salesforce check` without `--sf-org` reading `SALESFORCE_INSTANCE_URL` and `SALESFORCE_ACCESS_TOKEN`, and that path not refreshing the token;
  - each request's network timeout being the remaining deadline, up to 5 seconds;
  - `salesforce_invalid_configuration` exiting 2;
  - a brief prepared for a client fitting the tool's 4 MiB.
- **Transcripts:**
  - an import needing exactly one of `--account` and `--account-number`, with `--lines` only for TXT and `--cues` only for VTT and SRT;
  - the file extension choosing the parser, and an unsupported one refused through the CLI;
  - an import resolving the account through a local grant that holds only `crm:read`;
  - `connect calls --remove` detaching transcripts and deleting nothing;
  - `debug calls list` and `debug calls inspect` showing which imports state speaker names;
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
  - response bodies left out of the audit log.
- **Selection and prepared briefs:**
  - the selection file owned by you, and never reloaded;
  - a restart of `debug serve` replacing every grant;
  - `prepare_renewal_brief` and `read_renewal_brief` requiring `crm:read`;
  - `as_of` requiring `YYYY-MM-DD`, and Salesforce defaulting to today;
  - a prepared brief giving the same evidence and gaps as `brief` for the same inputs, scopes and cutoff;
  - the reads inside a prepared brief printing no `debug serve` event lines;
  - the 45-second deadline, with a read in progress keeping its 20-second limit;
  - the 4 MiB limit, with its `brief_too_large` and `brief_storage_failed` errors;
  - `brief_rejected` when evidence fails its checks;
  - logging out of `sf` not withholding saved Salesforce content;
  - an unmapped selected Slack thread stopping `debug serve` with exit 3;
  - an ordinary call, and a saved-brief read, still answering when their grant expires mid-call. The final check in `prepare_renewal_brief` is tested.
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
