# Roadmap

This page is the plan. It lists what is being built, in the order it will be built. Nothing on it
is a promise. The other docs describe what the code does today and claim only what a test shows. An
item leaves this page when it ships with its test. The last section lists statements in the other
docs that no test shows yet.

On this page "will" states what is planned, and the present tense states what is on main today.

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

**Terms used on this page.** The first six exist today. The last five are planned.
- A **card** is what the connector publishes for one account after it reads that account's sources. A card holds units and gaps.
- A **unit** is one numbered record or one whole sentence on a card.
- A **gap** is something the card states it did not read or did not show.
- A **receipt** is the stored record of one read or one tool call. It is committed before the read or the call runs.
- A **sealed card** is a card whose stored bytes are named by a receipt's digest. The seal is there so that a changed byte is detected.
- A **grant** is what lets one person or one token read one source, for selected accounts. Later a grant will also name the deals a person reads, and whether they see Ops.
- A **deal** will be one renewal of an account. An account may have more than one deal.
- A **match** will be one call or one Slack thread proposed for a deal.
- An **approval** will be a person's decision to accept something proposed and shown to them: a match in milestone 1, a proposed deal in milestone 2, a staged record in milestone 4.
- A **mark** will be a person's label on one whole sentence of a card. The sentence will be marked as a blocker or as a seller's commitment.
- A **blocker** will be a sentence a person marked as one. A **risk** will be a failed readiness rule. Either will turn a deal red.
- A **band** will be one of the four groups of the deal list: red, amber, unknown and green. A deal will show one band.

## v0.1.0: one Go binary replaces the Python package (in progress)

v0.1.0 will ship one Go binary in place of the Python package. The binary will run on PostgreSQL,
read the same sources and write nothing back to any source. It will port what the Python package
does today.

`testdata/oracle/` is the reference for the port. With the recorded clocks and IDs, card text is to
match byte for byte, and units and gaps are to match as values. The parity tests on main build 208
of the 450 recorded cards:
- every card from the demo book, the varied demo books and the test cases;
- the 32 call-book cards whose accounts have no imported transcript.

| Step | What it does | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirms that Go matches Python on what is ported so far. | The parity tests pass at 208 of 450. `make verify-real` compares Python and Go on public text and JSON. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a sealed card read. |
| Evidence | Will port the checks for imported transcripts and live Slack messages, with one fixed JSON nesting limit. A link with an ambiguous host will show `[link unknown host]`. The fix is made in Python first, so that card text still matches. | Evidence that needs those checks is refused as not ported. | Both paths accept valid evidence and refuse invalid evidence, and real text still matches. |
| Sources | Will port transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container. | Go reads only the demo book. | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Will port the seven commands and `debug` to PostgreSQL, with `--state-dir` naming a workspace. Will serve MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. Each tool call will have one outer receipt, and a prepared brief will add a receipt for each read. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`. The MCP gate is written but not served: `/mcp` answers 404. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Will open the cards and receipts the Python package made. They will be verified and kept byte for byte, never rebuilt. | Go verifies both kinds of Python card in tests. No command admits them yet. | The four recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing. |
| Cutover | Will ship one binary that embeds the web app (`go:embed`), built for macOS and Linux on arm64 and amd64, with checksums. `/healthz` will also check that migrations are current. Will remove the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrite the docs for Go. | `serve` reads the web app from disk. | The binary, run outside a checkout, serves a deep-linked Evidence page and its assets. A backup taken before the cutover restores. The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

**What changes for you at cutover.**
- The binary, or `make up` with Docker Compose, will replace `pipx install`. The binary will need PostgreSQL 19 or 18.7, and `make up` runs one.
- The commands, their exit codes and their `--json` output will stay, except that `--scope crm:write` will be refused. Card text will stay the same byte for byte.
- `log_activity` and the `crm:write` scope will be removed. A write attempt will be refused with a receipt.
- State will live in PostgreSQL, and `--state-dir` will name a workspace there, not a folder. Cards and receipts the Python package made will still open and verify. Nothing else will move:
  - sources will be connected again;
  - transcripts will be imported again;
  - Slack threads will be mapped again;
  - seller lists and selection files will be set again.
- Assistant apps will be set up again, because their saved entries start the Python package.
- The web app will show Evidence and Readiness for the demo sign-in, and will hide the walkthrough pages. Real cards will be read with the CLI and the MCP tools.
- No live org has been verified. v0.1.0 will not wait for one.

## After v0.1.0, in order

One web app will hold two areas, Deals and Ops, behind one sign-in. Both areas will read one HTTP
API, and the CLI, the MCP tools and alerts will carry the same facts. Each milestone names who will
be able to do what once it ships.

### 1. First call

One rep or CSM will read a real renewal before a call. A person who may see the deal and its
sources will approve what goes on it, and a person with the Ops grant will register the deal and grant it.

- **A deal will be one renewal of an account.** Today a card is for an account.
  - A person with the Ops grant will register the deal by hand, with a command or an API call, and will grant it to named people.
  - One handle will bind the deal's Salesforce account, opportunity, contract and contacts, its Slack threads, and the transcripts imported for it.
  - The deal will read the standard opportunity fields. If the org keeps a renewal field elsewhere, then that field will show as a gap until milestone 2.
- **Sign-in.**
  - Named people will sign in, and every action will be recorded under that sign-in.
  - A person will read only the deals they are granted. A person will see a source's sentences only if their own grant opens that source. If it does not, then the card will show the source as not read.
  - The security review will be redone for more than one person before real people sign in.
- **Approval, with a boundary.**
  - A match will wait for a person who may see both the deal and the source to approve or reject it.
  - An approval will cover what was shown: one transcript revision, or the exact messages of a thread up to the one displayed.
  - If a message is new or edited after the approval, then it will come back for review. Until then the card will count it as not read.
  - Approving a match for one renewal of an account will not approve it for the account's other renewals. A rejected match will add nothing to any card.
- **Stable evidence.** Approvals, marks and "what changed" will need two things the Python package lacks:
  - a Slack message will keep one identity across reads, and an edit or a verified deletion will supersede the version it changes;
  - an import will stay readable after a parser upgrade, and a reparse will be a new revision.
- **The Deals area** will be the home for reps and CSMs. It will show quotes, sources and times. It will never show receipts, digests or connections.
  - **The deal list** will sit on the left and will group the deals into four bands, the nearest renewal first within each band:
    - Red will mean an open blocker. Milestone 2 will add failed readiness rules.
    - Amber will mean a change since this reader last saw the card.
    - Unknown will mean a source was not read, or the last refresh failed.
    - Green will mean no open blocker. Green will be shown only when every source was read. Until readiness rules run, green will not say the renewal is on track.

    A deal will show its most urgent band, and will still say when a source was not read. A badge at the top will count the matches waiting for this person's approval, and will open them.
  - **The deal card** will fill the main pane. What changed will come first, then the risks and blockers. The deal card will also show:
    - days to renewal, and the recurring revenue when the source supplies it;
    - buyer quotes with names and titles;
    - the seller's commitments;
    - the sources that were not read.
  - **The proof drawer** will show where a line on the card comes from. Any line will open its record and field, or its whole sentence with the speaker, time and source. The proof drawer will not show the read's receipt. `inspect` and `audit` show it, and Ops will from milestone 2.
  - **Communications** will list the deal's approved calls and Slack threads in time order. Each will open its sentences.
  - **Help in place.** Each label and status will carry its definition, and what it leads to, beside the value.
- **Marks.**
  - A rep will mark one whole, uncut sentence as a blocker or as a seller's commitment. The label will come from a fixed list and will be checked against the speaker's role. A mark will hold no free text.
  - A mark will record who made it and the sentence it cites. It will be stored beside the sealed card, not in it. It will follow its sentence across refreshes. If that sentence changes or is withdrawn, then the mark will come back for review.
  - Resolving a blocker will need later proof, a person's decision and a reason from a fixed list, such as resolved or no longer applies. The resolved blocker will stay in the deal's history with its proof, who decided and when.
  - Wording alone will never make a blocker, because source text decides nothing.
- **What changed** will list the units, record versions and gaps that differ between the current card and the one this reader last saw. Each person's position will be their own, so one rep reading a card will not clear a teammate's amber.
- **Refresh.** A button, a command and an API call will each read every approved source again, under the caller's grants, with a receipt before each read.
  - If a refresh finds new evidence, versions or gaps, then it will publish a new sealed card. If it finds none, then it will record the time and publish nothing.
  - If a read fails or the evidence fails its checks, then no card will be published. The last sealed card will stay for those still allowed to see it, with its read time and a fixed failure reason, and the deal will move to the unknown band. If there is no earlier card, then only the failure will show.
- **Close.** If a refresh finds the opportunity Closed Won or Closed Lost, then it will publish the final card and the deal will be closing. The deal will be archived once that card verifies. If the card does not verify, then an allowed person will retry. Later refreshes will be refused. The deal's cards, receipts and history will stay readable to those allowed. If a renewal date passes while the opportunity is still open, then the deal will stay active.

**The trial.** The owner will run the connector on five real renewals the team consents to, outside
the repository. A rep who has not read the sources will have 30 seconds per renewal to find the
status, the change, who said what, the commitment and the blocker, with proof or a stated gap.
Blockers found only because someone marked them first will be counted apart. Only counts will be
recorded, and each parser failure will become an invented test fixture. The README's first limit,
that nothing was tried on a real deal, will change only then. Milestone 2 will not wait for the
trial. Milestone 6 will.

### 2. Whole book

RevOps will cover every renewal, not one, and will see which renewals have no current card.

- **Field mapping.** RevOps will map the renewal date, recurring revenue and stage to the org's typed fields or objects. Setup will refuse an ambiguous or mistyped mapping. Opportunity amount will never stand in for recurring revenue.
- **Watch rules.** A watch rule will propose deals on its own, for example open renewals closing within a set number of days. A person will still approve each deal. Setup will set how far back the first read goes, and the card will say where its history begins.
- **Transcript folders.** A folder of transcripts will be connected once. It will replace importing one file at a time and hand-picked excerpts. Each unit will bind to the receipt of the read that returned it, even when one tool reads twice.
- **A Slack channel window.** A bounded window of a channel will be read. It will replace mapping one thread per account. The card will count what was not read and will say when more exists.
- **People.** A participant directory will match call speakers to contacts by email or phone first, and by name only when the name is unique. The directory will let a call be proposed for a deal by participant email or account domain. An ambiguous speaker will be flagged as ambiguous. People whose words must never appear on a card will be excluded.
- **Readiness rules.** RevOps will pick from a short list and set the thresholds. Each result will cite its records, receipts and rule version, and will be one of: not evaluated, not applicable, unknown, failed, or no blocker found. A failed rule will be a risk.
  - **Customer silence:** no customer reply in the named sources during the window before renewal. If a read is missing, then the result will be unknown, not silence.
  - **Close-date slippage:** counted from the versions of the close-date field the connector has seen.
  - **One speaking contact:** one identified customer contact spoke in the window read.
  - **A possible price or seat conflict:** a sentence states an amount that differs from the record, for the same item, currency and period. A person will resolve it.
- **Preview.** A changed mapping, rule or threshold will be previewed against the book before it applies. The preview will show who would turn red and what would become unknown.
- **Scheduled reads.** Background reads will run with leases, so one stuck read will block nothing else. Each read will run as a named service identity with its own grant, and its receipts will name that identity. If a failure is temporary, then the read will be retried with backoff. If a credential is bad or expired, then the read will stop.
- **One configuration file** (planned name `gtm.yaml`). It will hold sources and field mappings, sellers and excluded people, the time zone, watch rules, thresholds and access. It will hold no credential. It will be kept in Git, previewed, and activated by exact revision.
- **One size cap will apply to every card.** The card will state the cap when the cap leaves evidence unshown.
- **The Ops area** will have three screens:
  - **Coverage** will be the home screen. It will list every renewal a watch rule proposed or a person registered, each with its lifecycle state. An active deal will also show whether it has a current card. A deal without one will open the pending match or the failed read behind it.
  - **Setup** will show each source's state (not connected, denied, partial, failed, stale or current), its last read and what to do about it. The active configuration will sit beside a draft, with the preview and the activation.
  - **Activity** will show what ran and whether it is safe to retry: reads, approvals, refusals and refreshes, each with its receipt. A person will be able to recheck a seal here.
- **Pause.** RevOps will pause a deal, which will stop its scheduled reads, and will resume it.

**A deal's lifecycle** will be: proposed, active, paused, closing, archived. Risk and freshness will
not be lifecycle states.

### 3. Delivery

A rep will hear about a new risk in Slack, and a team's scripts and assistants will read the same
deals.

- **Slack alerts.**
  - Slack alerts will be the connector's first outbound messages. They will be off until configured, and the connector will still write nothing to the CRM. They will need a Slack app with a posting scope, which the operator will create.
  - An alert will be sent when a risk opens, not on every read. It will carry the deal, the rule and a link to the card and its proof, never a quotation. Every name in it will be neutralized as card text is.
  - A paused deal will send none.
- **Webhooks** will be signed deliveries, each with a stable event ID so that a receiver can drop repeats. A subscription will be limited to selected deals and scopes. There will be three events:
  - a risk changed;
  - a card changed;
  - a deal closed.
- **Every delivery.** The audience and its grants will be checked again before each attempt. If a failure is temporary, then the attempt will be retried. If a credential is refused, then delivery will stop. If the outcome of an attempt is unknown, then the attempt will be shown as uncertain, and will not be sent again until it is checked. Activity will show each attempt.
- **Tokens for scripts and assistants.** A token will be limited to selected deals and scopes, and will expire.
- **A feed of flagged deals.** The feed will be the deal list's route, filtered to red and unknown, with links, so that a team's own bot or script can post them.
- **The API, described.** An OpenAPI description of the deal, card, change, proof and refresh routes will be published.
- **Assistants.** The MCP tools will read deals, cards and proof, not only accounts. The docs name a client only after it is tested live. Inspector is the tested client today.

### 4. Your own data

An engineer will send in records the built-in sources do not read, and RevOps will report on what
is stored.

- **The push API** will be how those records come in.
  - A JSON schema will be published for field records and messages, each with a stable key and a revision.
  - Each push will get a receipt before it is staged or refused. A repeat with the same key will return the same result. If the same key arrives with different content, then the push will be refused. A cursor will track progress.
  - A pushed record will name its deal by the handle, or by a key the handle binds. It will be staged, and will join the deal only when a person approves it.
  - Rows from another CRM, a spreadsheet, email, SMS or product usage will come in this way. HTML email will be turned into text first.
- **A customer's own database** will be read through a read-only connection and an approved view, with an ordered cursor and explicit deletions. Its records will be staged and approved like pushed ones.
- **Retractions.** A record entered by mistake will be retracted by a new version. Later cards will stop citing it, and its history will stay.
- **Read-only SQL.** There will be views over cards, units, gaps, receipts and readiness. The views will never expose credentials or sign-ins. Each view will state what it counts, its time basis and its as-of time, and each row will lead back to its records. The views will answer, for example:
  - how often close dates slip;
  - how long customers go silent before renewal;
  - how often blockers recur;
  - how often a stated figure disagrees with the record.
- **Retention and deletion.** Each workspace will have a retention period. Deletion will be on request, with a receipt of the deletion. A card that stays readable will still resolve every citation it shows.

### 5. A model proposes, a person decides

An operator will be able to turn on a model that proposes marks, and a person will accept or reject
each proposal.

- **Opt-in.** Proposals will be able to run on a local model, so that no source text leaves the machine. A hosted model will receive source text only when the operator chooses one.
- **Proposals.** Two assistant tools will work on one sealed card.
  - The evidence tool will return numbered units.
  - The submit tool will accept unit numbers, a label from the fixed list checked against each speaker's role, and pairs marked in tension. A cut sentence, an unknown label or a unit of another card will be refused with a receipt.
  - An accepted proposal will become a mark under the reviewer's sign-in.
- **Measurement.**
  - Every run, local or hosted, will get a benchmark score on the same frozen inputs: useful claims, wrong claims, facts covered, time and cost.
  - Judges will come from more than one model family, and a person will check their disagreements and a sample of their agreements.
  - Tuning will use development data. A ceiling for wrong claims and a floor for facts covered will be fixed before one confirming run on held-out data. Results will be published only after that run.
- **Facts covered** will be measured two ways:
  - against a pool of confirmed facts from every run, re-scored when a new fact is confirmed, so that a model that finds something new gets credit;
  - against a census, in which a grader who has seen no model output lists the important facts.
- **Evaluation.** `evaluate` will run the ten cases as a table, with a model's unchecked answers on the same deals kept as test inputs and scored offline. Two of the ten cases wait for features:
  - a blocker buried under routine messages waits for a mark or an accepted proposal, which will put it first;
  - a channel over the cap waits for the Slack channel window.

### 6. Only if the first calls show the need

Reps will work blockers through plans and reviews, if marks prove too little.

- **The workflow package:**
  - a rep will open work on a marked blocker from one of three playbooks: obtain the current rider, review a renewal condition, or reconcile an approval conflict;
  - a reviewer will approve one plan revision and the evidence it cites, and a change to either will reopen the review;
  - a document handoff or other resolution will be recorded with cited proof;
  - a work queue will list the work across a rep's deals.
- **Stakeholders on the card:** each contact's role, when they last spoke, and who has gone quiet.
- **An email connector,** if pushing email is not enough.
- **Rollups:** recurring revenue at risk by team and by rule, computed by the connector from the results, never by a model.
- **Alert digests and quiet hours.**

### 7. Install and run it like any server

An operator will install, run and upgrade the connector without a checkout. This milestone will
deliver:

- A published container image and a Homebrew tap.
- systemd and launchd templates.
- Backup, restore, and an upgrade rehearsed on the restored copy, checked as one loop.
- The ten cases as ready-to-open demo workspaces, so that a new user sees each one in the web app without credentials.
- **Three deployment shapes,** each documented once tested:
  - a local CLI, with an assistant app over stdio MCP;
  - a server in Docker Compose, for a team;
  - a headless API, for another system to call.

## Docs, as each part ships

- A page for each audience, with its flow drawn.
- A setup guide for each source.
- An inventory that states whether each part is built or planned.
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
- **Silence and Slack.** Should Slack authors be matched to contacts? Until they are, silence will count calls only.
- **What a deal is, at the edges.**
  - A call or thread that fits two open renewals of one account.
  - A deal with no CRM record.
  - A closed opportunity that reopens: the same deal, or a new one?
- **Reporting SQL.** A database role that BI tools connect to, or queries through the API so each one has a receipt?
- **Names.** Does the API stay under `/api/v1` with deals as a new resource? Is the deal handle a new ID, or the CRM opportunity?

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
