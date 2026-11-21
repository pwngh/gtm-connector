# Roadmap

This page is the plan: what is being built, in order. Nothing on it is a promise. The other docs
describe what the code does today and claim only what a test shows. An item leaves this page when
it ships with its test. The last section lists statements in the other docs that no test shows yet.

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

**Terms used on this page.**
- A **unit** is one numbered record or whole sentence on a card.
- A **gap** is something the card states it did not read or show.
- A **receipt** is the stored record of one read or tool call, committed before it runs.
- A **sealed card** is a card whose stored bytes are named by a receipt's digest, so a changed byte is detected.
- A **grant** is what lets one person or token read one source, for selected accounts.

## v0.1.0: one Go binary replaces the Python package (in progress)

It runs on PostgreSQL, reads the same sources and writes nothing back to any source. It ports what
the Python package does today. `testdata/oracle/` is the reference: with the recorded clocks and
IDs, card text matches byte for byte, and units and gaps match as values. The parity tests build
208 of the 450 recorded cards:
- every card from the demo book, the varied demo books and the test cases;
- the 32 call-book cards whose accounts have no imported transcript.

| Step | What it does | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirms that Go matches Python on what is ported so far | The parity tests pass at 208 of 450. `make verify-real` compares Python and Go on public text and JSON. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a sealed card read |
| Evidence | Ports the checks for imported transcripts and live Slack messages. Uses one fixed JSON nesting limit. A link with an ambiguous host shows `[link unknown host]`, fixed in Python first so card text still matches. | Evidence that needs those checks is refused as not ported | Both paths accept valid evidence and refuse invalid evidence, and real text still matches |
| Sources | Ports transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container | Go reads only the demo book | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Ports the seven commands and `debug` to PostgreSQL, with `--state-dir` naming a workspace. Serves MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. Each tool call has one outer receipt, and a prepared brief adds a receipt for each read. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`. The MCP gate is written but not served: `/mcp` answers 404. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Opens the cards and receipts the Python package made, verified and kept byte for byte, never rebuilt | Go verifies both kinds of Python card in tests. No command admits them yet. | The four recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing |
| Cutover | Ships one binary that embeds the web app (`go:embed`), built for macOS and Linux on arm64 and amd64, with checksums. `/healthz` also checks that migrations are current. Removes the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrites the docs for Go. | `serve` reads the web app from disk | The binary, run outside a checkout, serves a deep-linked Evidence page and its assets. A backup taken before the cutover restores. The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

**What changes for you at cutover.**
- The binary, or `make up` with Docker Compose, replaces `pipx install`. The binary needs PostgreSQL 19 or 18.7, and `make up` runs one.
- The commands, their exit codes and their `--json` output stay, except that `--scope crm:write` is refused. Card text stays the same byte for byte.
- `log_activity` and the `crm:write` scope are removed. A write attempt is refused with a receipt.
- State lives in PostgreSQL, and `--state-dir` names a workspace there, not a folder. Cards and receipts the Python package made still open and verify, and nothing else moves:
  - sources are connected again;
  - transcripts are imported again;
  - Slack threads are mapped again;
  - seller lists and selection files are set again.
- Assistant apps are set up again, because their saved entries start the Python package.
- The web app shows Evidence and Readiness for the demo sign-in, and hides the walkthrough pages. Real cards are read with the CLI and the MCP tools.
- No live org has been verified. v0.1.0 does not wait for one.

## After v0.1.0, in order

One web app holds two areas, Deals and Ops, behind one sign-in. Both read one HTTP API, and the
CLI, the MCP tools and alerts carry the same facts. Each milestone names who can do what once it
ships.

### 1. First call

One rep or CSM reads a real renewal before a call. A person who may see the deal and its sources
approves what goes on it, and the operator registers the deal and grants it.

- **A deal is one renewal of an account.** Today a card is for an account.
  - A person with the Ops grant registers the deal by hand, with a command or an API call, and grants it to named people.
  - One handle binds the deal's Salesforce account, opportunity, contract and contacts, its Slack threads, and the transcripts imported for it.
  - It reads the standard opportunity fields. A renewal field the org keeps elsewhere shows as a gap until milestone 2.
- **Sign-in.**
  - Named people sign in, and every action is recorded under that sign-in.
  - A person reads only the deals they are granted, and sees a source's sentences only if their own grant opens that source. Otherwise the card shows it as not read.
  - The security review is redone for more than one person before real people sign in.
- **Approval, with a boundary.**
  - A call or a Slack thread proposed for the deal waits for a person who may see both the deal and the source to approve or reject it.
  - An approval covers what was shown: one transcript revision, or the exact messages of a thread up to the one displayed.
  - New and edited messages come back for review, and until then the card counts them as not read.
  - Approving an account does not approve all its renewals. A rejected match adds nothing to any card.
- **Stable evidence.** Approvals, marks and "what changed" need two things the Python package lacks:
  - a Slack message keeps one identity across reads, and an edit or a verified deletion supersedes the version it changes;
  - an import stays readable after a parser upgrade, and a reparse is a new revision.
- **The Deals area.** Home for reps and CSMs. It shows quotes, sources and times, never receipts, digests or connections.
  - **The deal list,** on the left, in four bands, the nearest renewal first within each:
    - red: an open blocker. Milestone 2 adds failed readiness rules.
    - amber: a change since this reader last saw the card;
    - unknown: a source was not read, or the last refresh failed;
    - green: no open blocker, shown only when every source was read. Until readiness rules run, green does not say the renewal is on track.

    A deal shows its most urgent band, and still says when a source was not read. A badge at the top counts the calls and threads waiting for this person's approval, and opens them.
  - **The deal card,** in the main pane: what changed comes first, then the risks and blockers. It also shows:
    - days to renewal, and the recurring revenue when the source supplies it;
    - buyer quotes with names and titles;
    - the seller's commitments;
    - the sources that were not read.
  - **The proof drawer.** Any line opens its record and field, or its whole sentence with the speaker, time and source. The read's receipt is not shown here: `inspect` and `audit` show it, and Ops does from milestone 2.
  - **Communications.** The deal's approved calls and Slack threads in time order, each opening its sentences.
  - **Help in place.** Each label and status carries its definition, and what it leads to, beside the value.
- **Marks.**
  - A rep marks one whole, uncut sentence as a blocker or as a seller's commitment. The label comes from a fixed list and is checked against the speaker's role, and a mark holds no free text.
  - A mark records who made it and the sentence it cites. It is stored beside the sealed card, not in it. It follows its sentence across refreshes, and comes back for review if that sentence changes or is withdrawn.
  - Resolving a blocker needs later proof, a person's decision and a reason from a fixed list, such as resolved or no longer applies. The resolved blocker stays in the deal's history with its proof, who decided and when.
  - Wording alone never makes a blocker, because source text decides nothing.
- **What changed:** the units, record versions and gaps that differ between the current card and the one this reader last saw. Each person's position is their own, so one rep reading a card does not clear a teammate's amber.
- **Refresh.** A button, a command and an API call read every approved source again, under the caller's grants, with a receipt before each read.
  - A refresh that finds new evidence, versions or gaps publishes a new sealed card. One that finds none records the time and publishes nothing.
  - If a read fails or the evidence fails its checks, no card is published. The last sealed card stays for those still allowed to see it, with its read time and a fixed failure reason, and the deal moves to the unknown band. With no earlier card, only the failure shows.
- **Close.** When a refresh finds the opportunity Closed Won or Closed Lost, it publishes the final card and the deal is closing. It is archived once that card verifies; if not, an allowed person retries. Later refreshes are refused. Its cards, receipts and history stay readable to those allowed. A renewal date that passes leaves an open opportunity active.

**The trial.** The owner runs the connector on five real renewals the team consents to, outside the
repository. A rep who has not read the sources has 30 seconds per renewal to find the status, the
change, who said what, the commitment and the blocker, with proof or a stated gap. Blockers found
only because someone marked them first are counted apart. Only counts are recorded, and each parser
failure becomes an invented test fixture. The README's first limit, that nothing was tried on a
real deal, changes only then. Milestone 2 does not wait for the trial. Milestone 6 does.

### 2. Whole book

RevOps covers every renewal, not one, and sees which have no current card.

- **Field mapping.** RevOps maps the renewal date, recurring revenue and stage to the org's typed fields or objects. Setup refuses an ambiguous or mistyped mapping. Opportunity amount never stands in for recurring revenue.
- **Watch rules.** A rule proposes deals on its own, for example open renewals closing within a set number of days. A person still approves each deal. Setup sets how far back the first read goes, and the card says where its history begins.
- **Transcript folders.** A folder of transcripts is connected once. It replaces importing one file at a time and hand-picked excerpts. Each unit binds to the receipt of the read that returned it, even when one tool reads twice.
- **A Slack channel window.** A bounded window of a channel is read. It replaces mapping one thread per account, and the card counts what was not read and says when more exists.
- **People.** A participant directory matches call speakers to contacts by email or phone first, and by name only when the name is unique. With it, a call can be proposed for a deal by participant email or account domain. An ambiguous speaker is marked. People whose words must never appear on a card are excluded.
- **Readiness rules.** RevOps picks from a short list and sets its thresholds. Each result cites its records, receipts and rule version, and is one of: not evaluated, not applicable, unknown, failed, or no blocker found. A failed rule is a risk, and turns the deal red.
  - **Customer silence:** no customer reply in the named sources during the window before renewal. A missing read makes it unknown, not silence.
  - **Close-date slippage:** counted from the versions of the close-date field the connector has seen.
  - **One speaking contact:** one identified customer contact spoke in the window read.
  - **A possible price or seat conflict:** a sentence states an amount that differs from the record, for the same item, currency and period. A person resolves it.
- **Preview.** A changed mapping, rule or threshold is previewed against the book before it applies: who would turn red, and what would become unknown.
- **Scheduled reads.** Background reads with leases, so one stuck read blocks nothing else. Each runs as a named service identity with its own grant, and its receipts name it. Temporary failures are retried with backoff, and a bad or expired credential stops the read.
- **One configuration file** (planned name `gtm.yaml`). It holds sources and field mappings, sellers and excluded people, the time zone, watch rules, thresholds and access. It holds no credential. It is kept in Git, previewed, and activated by exact revision.
- **One size cap on every card,** stated on the card when it leaves evidence unshown.
- **The Ops area.** Three screens:
  - **Coverage,** the home screen. Every renewal a watch rule proposed or a person registered is listed with one state: proposed, paused, closing, no current card, or current card. Each gap opens the pending match or the failed read behind it.
  - **Setup.** Each source's state (not connected, denied, partial, failed, stale or current), its last read and what to do about it. The active configuration sits beside a draft, with the preview and the activation.
  - **Activity.** What ran and whether it is safe to retry: reads, approvals, refusals and refreshes, each with its receipt. A seal can be rechecked here.
- **Pause.** RevOps pauses a deal, which stops its scheduled reads, and resumes it.

**A deal's lifecycle:** proposed, active, paused, closing, archived. Risk and freshness are not lifecycle states.

### 3. Delivery

A rep hears about a new risk in Slack, and a team's scripts and assistants read the same deals.

- **Slack alerts.**
  - These are the connector's first outbound messages. They are off until configured, and the connector still writes nothing to the CRM. They need a Slack app with a posting scope, which the operator creates.
  - An alert is sent when a risk opens, not on every read. It carries the deal, the rule and a link to the card and its proof, never a quotation. Every name in it is neutralized as card text is.
  - A paused deal sends none.
- **Webhooks.** Signed deliveries, each with a stable event ID so a receiver can drop repeats. A subscription is limited to selected deals and scopes. There are three events:
  - a risk changed;
  - a card changed;
  - a deal closed.
- **Every delivery.** The audience and its grants are checked again before each attempt. A temporary failure is retried, and a refused credential stops delivery. An attempt whose outcome is unknown is shown as uncertain, and is not sent again until it is checked. Activity shows each attempt.
- **Tokens for scripts and assistants.** A token is limited to selected deals and scopes, and expires.
- **A feed of flagged deals.** The deal list's route, filtered to red and unknown, with links, so a team's own bot or script can post them.
- **The API, described.** A published OpenAPI description of the deal, card, change, proof and refresh routes.
- **Assistants.** The MCP tools read deals, cards and proof, not only accounts. The docs name a client only after it is tested live; Inspector is the tested client today.

### 4. Your own data

An engineer sends in records the built-in sources do not read, and RevOps reports on what is stored.

- **The push API.**
  - A published JSON schema for field records and messages, each with a stable key and a revision.
  - Each push gets a receipt before it is staged or refused. A repeat with the same key returns the same result, and the same key with different content is refused. A cursor tracks progress.
  - A pushed record names its deal by the handle, or by a key the handle binds. It is staged, and joins the deal only when a person approves it.
  - Rows from another CRM, a spreadsheet, email, SMS or product usage come in this way. HTML email is turned into text first.
- **A customer's own database.** Read through a read-only connection and an approved view, with an ordered cursor and explicit deletions. Its records are staged and approved like pushed ones.
- **Retractions.** A record entered by mistake is retracted by a new version. Later cards stop citing it, and its history stays.
- **Read-only SQL.** Views over cards, units, gaps, receipts and readiness that never expose credentials or sign-ins. Each view states what it counts, its time basis and its as-of time, and each row leads back to its records. They answer, for example:
  - how often close dates slip;
  - how long customers go silent before renewal;
  - how often blockers recur;
  - how often a stated figure disagrees with the record.
- **Retention and deletion.** A retention period per workspace, and deletion on request with a receipt of the deletion. A card that stays readable still resolves every citation it shows.

### 5. A model proposes, a person decides

An operator may turn on a model that proposes marks, and a person accepts or rejects each.

- **Opt-in.** It can run on a local model, so no source text leaves the machine. A hosted model receives source text only when the operator chooses one.
- **Proposals.** Two assistant tools work on one sealed card.
  - The evidence tool returns numbered units.
  - The submit tool accepts unit numbers, a label from the fixed list checked against each speaker's role, and pairs marked in tension. A cut sentence, an unknown label or a unit of another card is refused with a receipt.
  - An accepted proposal becomes a mark under the reviewer's sign-in.
- **Measurement.**
  - Every run, local or hosted, gets a benchmark score on the same frozen inputs: useful claims, wrong claims, facts covered, time and cost.
  - Judges come from more than one model family, and a person checks their disagreements and a sample of their agreements.
  - Tuning uses development data. A ceiling for wrong claims and a floor for facts covered are fixed before one confirming run on held-out data. Results are published only after that.
- **Facts covered,** measured two ways:
  - against a pool of confirmed facts from every run, re-scored when a new fact is confirmed, so a model that finds something new gets credit;
  - against a census, in which a grader who has seen no model output lists the important facts.
- **Evaluation.** `evaluate` runs the ten cases as a table, with a model's unchecked answers on the same deals kept as test inputs and scored offline. Two of the ten cases wait for features:
  - a blocker buried under routine messages waits for a mark or an accepted proposal, which puts it first;
  - a channel over the cap waits for the Slack channel window.

### 6. Only if the first calls show the need

Reps work blockers through plans and reviews, if marks prove too little.

- **The workflow package:**
  - a rep opens work on a marked blocker from one of three playbooks: obtain the current rider, review a renewal condition, or reconcile an approval conflict;
  - a reviewer approves one plan revision and the evidence it cites, and a change to either reopens the review;
  - a document handoff or other resolution is recorded with cited proof;
  - a work queue lists the work across a rep's deals.
- **Stakeholders on the card:** each contact's role, when they last spoke, and who has gone quiet.
- **An email connector,** if pushing email is not enough.
- **Rollups:** recurring revenue at risk by team and by rule, computed by the connector from the results, never by a model.
- **Alert digests and quiet hours.**

### 7. Install and run it like any server

An operator installs, runs and upgrades the connector without a checkout.

- A published container image and a Homebrew tap.
- systemd and launchd templates.
- Backup, restore, and an upgrade rehearsed on the restored copy, checked as one loop.
- The ten cases as ready-to-open demo workspaces, so a new user sees each one in the web app without credentials.
- **Three deployment shapes,** each documented once tested:
  - a local CLI, with an assistant app over stdio MCP;
  - a server in Docker Compose, for a team;
  - a headless API, for another system to call.

## Docs, as each part ships

- A page for each audience, with its flow drawn.
- A setup guide for each source.
- An inventory that marks each part built or planned.
- A deployment guide.
- An FAQ: other deal types, offline use, permissions, outages and retractions.
- A comparison with vendor MCP servers, revenue intelligence products and retrieval pipelines.

## Later, when a real user needs them

- An idempotent display for units sealed under older rules, labelled when it differs from the stored text.
- Salesforce sign-in through a connected app.
- Transcript provider adapters.
- DOCX tracked changes as a source.
- Salesforce task creation.
- A second CRM.
- Windows.
- MCP Apps.

**Not planned:** language SDKs, WASM or CGO builds. Integrations use HTTP or MCP.

## Open questions

- **Playbooks.** Are marks enough, or do reps need plans, reviews and handoffs?
- **Sign-in.** A password the connector stores, or an identity provider?
- **Ops access.** Is a grant enough, or is Ops a separate role?
- **Closing by hand.** May a person close a deal, or does only the CRM stage close it?
- **A bad unit.** Today one unit that fails a check stops the card. Should it be refused alone, with the card saying how many were?
- **Slippage before a deal is watched.** Should the connector also read the CRM's field history, so changes before its first read count?
- **Silence and Slack.** Should Slack authors be matched to contacts? Until they are, silence counts calls only.
- **What a deal is, at the edges.**
  - A call or thread that fits two open renewals of one account.
  - A deal with no CRM record.
  - A closed opportunity that reopens: the same deal, or a new one?
- **Reporting SQL.** A database role that BI tools connect to, or queries through the API so each one has a receipt?
- **Names.** Does the API stay under `/api/v1` with deals as a new resource? Is the deal handle a new ID, or the CRM opportunity?

## Checks the docs still need

The other docs make the statements below, and no test asserts them yet. Each gets a test, or
narrower wording, when the docs are rewritten for Go. They are grouped by the v0.1.0 step that
owns the check.

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
