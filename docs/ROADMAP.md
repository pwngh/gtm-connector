# Roadmap

What we are building, in the order we will build it. None of it is a promise. The other docs say
what the code does today and claim only what a test shows, so planned work is written here and
nowhere else. An item leaves this page when it ships with its test.

On this page "will" means planned. The present tense means it is on main today.

The goal has not changed: find what blocks a renewal in the evidence, turn it into reviewed work,
and keep the history of what resolved it. No model writes text on a card.

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
for byte, and units and gaps must match as values. The parity tests build 208 of the 450 recorded
cards today: every card from the demo book, the varied demo books and the test cases, and the 32
call-book cards whose accounts have no imported transcript.

| Step | What it will do | On main today | Done when |
| --- | --- | --- | --- |
| Gate | Confirm that Go matches Python on what is ported so far | The parity tests pass at 208 of 450. `make verify-real` compares Python and Go on public text and JSON. | Both pass at the final head, the time to publish a card is measured, and a cold Compose start reaches sign-in and a sealed card read |
| Evidence | Port the checks for imported transcripts and live Slack messages. Use one fixed JSON nesting limit. Show `[link unknown host]` for a link with an ambiguous host; that fix goes into Python first, so card text still matches. | Evidence that needs those checks is refused as not ported | Both paths accept valid evidence and refuse invalid evidence, and real text still matches |
| Sources | Port transcript import (one TXT, VTT or SRT file, with an excerpt), one mapped Slack thread per account, and Salesforce through the `sf` CLI or a token given to the container | Go reads only the demo book | Go builds all 450 recorded cards, in memory and on PostgreSQL. Recorded source requests and responses replay offline. A cancelled read ends the `sf` process group and publishes no card. |
| Commands and MCP | Port the seven commands and `debug` to PostgreSQL, where `--state-dir` names a workspace. Serve MCP over HTTPS and stdio with the read tools, under the same Host, scope, selection and expiry checks. One outer receipt per tool call, plus one per read inside a prepared brief. | Go has `setup`, `serve`, `demo`, `backup`, `restore`, `health` and `version`. The MCP gate is written but not served: `/mcp` answers 404. | The recorded command session replays against the binary, and source and client setup work through it. The recorded request vectors pass on both transports, except the write vectors, which are refused with a receipt. The Inspector runs against the binary. |
| Existing cards | Open the cards and receipts the Python package made. They are verified and kept byte for byte, never rebuilt. | Go verifies both kinds of Python card in tests. No command admits them yet. | The four recorded card folders are admitted and inspected, a changed byte is refused, and admitting twice adds nothing |
| Cutover | Ship one binary with the web app inside it (`go:embed`), for macOS and Linux on arm64 and amd64, with checksums. Make `/healthz` check that migrations are current. Remove the Python package, its tests, `tools/oracle/` and their CI jobs, and rewrite the docs for Go. | `serve` reads the web app from disk | The binary, run outside a checkout, serves a deep-linked Evidence page and its assets. A backup taken before the cutover restores. The Go tests pass with Python gone, and real text still matches the digests recorded before the removal. |

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

## After v0.1.0, in order

There will be one web app with two areas, Deals and Ops, behind one sign-in. Both will read one
HTTP API, and the CLI, the MCP tools and the alerts will carry the same facts. Reps will never see
the plumbing. Ops will show all of it.

### 1. First call

A rep or CSM will read one real renewal before a call. Everything in this milestone is here
because that reading needs it.

**The deal.** Today a card is for an account. A deal will be one renewal of an account, so an
account with two renewals will have two deals. A person with the Ops grant will register a deal by
hand and grant it to named people. A deal ID will be readable, such as `thornfield-2027`: `gtm deal
thornfield-2027` will print its card, and the API will serve it at `/v1/deals/thornfield-2027`. The deal's handle will bind its
Salesforce account, opportunity, contract and contacts, its Slack threads and its imported
transcripts. The card will read the standard opportunity fields. If the org keeps a renewal field
somewhere else, the card will show a gap until milestone 2.

**Sign-in.** Named people will sign in through the company's identity provider, and every action
will be recorded under that sign-in. The connector will store no password for them. A
person will read only the deals granted to them. They will see a source's sentences only if their
own grant opens that source; otherwise the card will show the source as not read. The security
review covers one operator today. It will be redone for more than one person before real people
sign in.

**Matching.** A call or a Slack thread that fits a deal by the rules in the configuration is a
match, and it will join the deal on its own. Nobody has to click. A match that fits more than one
deal will wait for a person. A match will cover exactly what was read: one transcript revision, or
the messages of a thread up to the last one read. If a message is added or edited afterwards, then
the next read will pick it up.

A team will be able to turn approval on. Then every match will wait for a person who may see both
the deal and the source, and a rejected match will add nothing to any card.

This needs two things the Python package does not have. A Slack message will keep one identity
across reads, with an edit or a verified deletion superseding the version it changes. An import
will stay readable after a parser upgrade, and a reparse will be a new revision.

**The Deals area** will be home for reps and CSMs. It will show quotes, sources and times. It will
not show receipts, digests or connections.

The deal list will sit on the left, sorted by urgency, nearest renewal first within each colour:

- red: a risk or a blocker;
- amber: new changes since this reader last saw the card;
- green: on track.

A deal whose source was not read, or whose last refresh failed, will say so on its row and will
not show green. Until the readiness rules run, red will mean a sentence someone marked as a blocker
and has not resolved. When approval is on, a badge at the top will count
the matches waiting for this person, and open them.

The deal card will fill the main pane. What changed will come first, then the risks and blockers.
Below them: days to renewal, the recurring revenue when the source supplies it, buyer quotes with
names and titles, the seller's commitments, and the sources that were not read. "What changed"
will compare the current card with the one this reader last saw: the units, record versions and
gaps that differ. Each person's position will be their own, so one rep reading a card will not
clear a teammate's amber.

Any line will open the proof drawer: the record and field behind a figure, or the whole sentence
with its speaker, time and source behind a quote. The drawer will not show the read's receipt.
`inspect` and `audit` show it today, and Ops will from milestone 2. Communications will list the
deal's calls and Slack threads in time order, each opening its sentences. Each label and
status will carry its definition, and what it leads to, beside the value.

**Marks.** A mark is a person's label on a sentence, and it is how a blocker gets onto a deal. A
rep will mark one whole, uncut sentence as a blocker or as a seller's commitment. The
label will come from a fixed list and be checked against the speaker's role, and a mark will hold
no free text. Wording alone will never make a blocker, because source text decides nothing. A mark
will record who made it and the sentence it cites. It will be stored beside the sealed card, not
in it, and will follow its sentence across refreshes. If the sentence changes or is withdrawn,
then the mark will come back for review. Resolving a blocker will take later proof, a person's
decision, and a reason from a fixed list such as resolved or no longer applies. The resolved
blocker will stay in the deal's history with its proof, who decided and when.

**The checklist.** The card will carry a renewal checklist: its open blockers, tracked until each
is resolved. Playbooks, in milestone 6, add plans and reviews to it.

**Refresh.** A button, a command and an API call will read every source of the deal again, under the
caller's grants, with a receipt before each read. If the refresh finds new evidence, versions or
gaps, then it will publish a new sealed card. If it finds none, then it will record the time and
publish nothing. If a read fails or the evidence fails its checks, then no card will be published:
the last sealed card will stay for those still allowed to see it, with its read time and a fixed
failure reason, and the deal's row will say so. With no earlier card, only the failure
will show.

**Close.** If a refresh finds the opportunity Closed Won or Closed Lost, then it will publish the
final card and the deal will be closing. The deal will be archived once that card verifies. If it
does not verify, then an allowed person will retry. After that, refreshes will be refused, and the
deal's cards, receipts and history will stay readable to those allowed. If the renewal date passes
while the opportunity is still open, then the deal will stay active.

**The trial.** The owner will run the connector on five real renewals the team consents to,
outside the repository. A rep who has not read the sources will have 30 seconds per renewal to
find the status, the change, who said what, the commitment and the blocker, with proof or a stated
gap. Blockers found only because someone marked them first will be counted apart. Only counts will
be recorded, and each parser failure will become an invented test fixture. The README's first
limit, that nothing was tried on a real deal, will change only then. No later milestone waits for
the trial.

### 2. Whole book

RevOps will cover every renewal, and see which ones have no current card.

**Field mapping.** RevOps will map the renewal date, recurring revenue and stage to the org's own
typed fields or objects. Setup will refuse an ambiguous or mistyped mapping. Opportunity amount
will never stand in for recurring revenue.

**Finding deals and evidence.** A watch rule will propose deals on its own, for example open
renewals closing within a set number of days. A proposed deal will be watched at once. Setup will set
how far back the first read goes, and the card will say where its history begins. A folder of
transcripts will be connected once, replacing one-file imports and hand-picked excerpts. A bounded
window of a Slack channel will be read, replacing one mapped thread per account; the card will
count what was not read and say when more exists. Threads will be found by dedicated channel, such
as `#deal-thornfield`, or in a shared channel where the account's name or domain is mentioned.
Calls from Gong and Zoom will be read on the same schedule. Because a tool may then read twice, each unit
will bind to the receipt of the read that returned it.

**People.** A participant directory will match call speakers to contacts by email or phone first,
and by name only when the name is unique. With it, a call will be proposed for a deal by
participant email or account domain. The card will say when a speaker is ambiguous. People whose
words must never appear on a card will be excluded.

**Readiness rules.** RevOps will pick rules from a short list and set their thresholds. Each
result will cite its records, receipts and rule version, and will be one of: not evaluated, not
applicable, unknown, failed or passed. A failed rule is what the rest of this page calls a risk,
and it turns the deal red. What can flag a deal:

- Customer silence will fail when no customer replied in the named sources during the window before renewal. If a read is missing, then the result will be unknown, not silence.
- Close-date slippage will be counted from the versions of the close-date field the connector has seen.
- One speaking contact will fail when only one identified customer contact spoke in the window read.
- A possible price or seat conflict will be raised when a sentence states an amount that differs from the record, for the same item, currency and period. A person will resolve it.
- A legal or security blocker, such as redlines or riders holding up signature, will flag the deal when a person marks it. No rule will read it from wording.

A changed mapping, rule or threshold will be previewed against the book before it applies: who
would turn red, and what would become unknown.

**Scheduled reads** will run in the background under leases, so one stuck read blocks nothing
else. Each will run as a named service identity with its own grant, and its receipts will name it.
Temporary failures will be retried with backoff. A bad or expired credential will stop the read.

**One configuration file** (planned name `gtm.yaml`) will hold sources and field mappings, sellers
and excluded people, the time zone, watch rules, thresholds and access. It will hold no
credential. It will be kept in Git, previewed, and activated by exact revision.

**One size cap** will apply to every card, stated on the card when it leaves evidence unshown.

**The Ops area** will have three screens.

- Coverage will be the home screen. It will list every deal a watch rule proposed or a person registered, each in one state: proposed, paused, closing, no current card, or current card. A deal with no current card will open the pending match or the failed read behind it.
- Setup will show each source's state (not connected, denied, partial, failed, stale or current), its last read, and what to do about it. The active configuration will sit beside a draft, with the preview and the activation.
- Activity will show what ran and whether it is safe to retry: reads, matches, refusals and refreshes, each with its receipt. A seal will be rechecked here.

RevOps will be able to pause a deal, which stops its scheduled reads, and resume it. A deal's
lifecycle will be proposed, active, paused, closing, archived. Risk and freshness will not be
lifecycle states.

### 3. Delivery

A rep will hear about a new risk in Slack, and a team's scripts and assistants will read the same
deals.

**Slack alerts** will be the first thing the connector sends anywhere. They will be off until
configured, and the connector will still write nothing to the CRM. They will need a Slack app with
a posting scope, which the operator will create. An alert will go out when a risk opens, not on
every read. It will carry the deal, the rule and a link to the card and its proof, never a
quotation, and every name in it will be neutralized as card text is. A paused deal will send none.

**Webhooks** will be signed, and each delivery will carry a stable event ID so a receiver can drop
repeats. A subscription will be limited to selected deals and scopes. There will be three events:
a risk changed, a card changed, a deal closed.

For every delivery, the audience and its grants will be checked again before each attempt. A
temporary failure will be retried, and a refused credential will stop delivery. If the outcome of
an attempt is unknown, then it will be shown as uncertain and not sent again until it is checked.
Activity will show each attempt.

**For scripts and assistants:**

- A token will be limited to selected deals and scopes, and will expire.
- The deal list's route, filtered to red and unknown, will be a feed a team's own bot can post from.
- The deal, card, change, proof and refresh routes will be published as an OpenAPI description.
- The MCP tools will read deals, cards and proof, not only accounts. The clients to test are Claude Desktop, Claude Code, ChatGPT and Codex. The docs name a client only after it is tested live; Inspector is the tested client today.

### 4. Your own data

An engineer will send in records the built-in sources do not read, and RevOps will report on what
is stored.

**The push API** will take field records and messages in a published JSON schema, each with a
stable key and a revision. Each push will get a receipt before it is staged or refused. If a push
repeats a key with the same content, then it will get the same result. If it repeats a key with
different content, then it will be refused. A cursor will track progress. A pushed record will
name its deal by the handle, or by a key the handle binds, and it will join that deal on its own, or wait for a
person when approval is on. Rows from another CRM, a spreadsheet, email, SMS or product usage will come in
this way, and HTML email will be turned into text first.

**A customer's own database** will be read through a read-only connection and an approved view,
with an ordered cursor and explicit deletions. Its records will join their deals like pushed
ones.

**Retractions.** A record entered by mistake will be retracted by a new version. Later cards will
stop citing it, and its history will stay.

**Read-only SQL** will be views over cards, units, gaps, receipts and readiness. They will never
expose credentials or sign-ins. Each view will state what it counts, its time basis and its as-of
time, and each row will lead back to its records. They will answer questions such as how often
close dates slip, how long customers go silent before renewal, how often blockers recur, and how
often a stated figure disagrees with the record.

**Retention and deletion.** There will be a retention period per workspace, and deletion on
request with a receipt of the deletion. A card that stays readable will still resolve every
citation it shows.

### 5. A model proposes, a person decides

An operator will be able to turn on a model that proposes marks. A person will accept or reject
each one, and an accepted proposal will become a mark under that person's sign-in.

It will be opt-in. A local model will be able to make the proposals, so that no source text leaves
the machine. A hosted model will receive source text only when the operator chooses one.

Two assistant tools will work on one sealed card. The evidence tool will return numbered units.
The submit tool will accept unit numbers, a label from the fixed list checked against each
speaker's role, and pairs labelled as in tension. A cut sentence, an unknown label or a unit of
another card will be refused with a receipt.

The model, prompt and checks will be chosen by measurement:

- Every run, local or hosted, will get a benchmark score on the same frozen inputs: useful claims, wrong claims, facts covered, time and cost.
- Judges will come from more than one model family. A person will check their disagreements and a sample of their agreements.
- Facts covered will be measured two ways. One is against a pool of confirmed facts from every run, re-scored when a new fact is confirmed, so a model that finds something new gets credit. The other is against a census, in which a grader who has seen no model output lists the important facts.
- Tuning will use development data. A ceiling for wrong claims and a floor for facts covered will be fixed before one confirming run on held-out data, and results will be published only after it.

`evaluate` will run the ten cases as a table, with a model's unchecked answers on the same deals
kept as test inputs and scored offline. Two of the ten cases wait for features: a blocker buried
under routine messages waits for a mark or an accepted proposal, which will put it first, and a
channel over the cap waits for the Slack channel window.

### 6. Playbooks

Reps will work a blocker through a plan and a review, from the checklist on the card:

- a rep will open work on a marked blocker from one of three playbooks: obtain the current rider, review a renewal condition, or reconcile an approval conflict;
- a reviewer will approve one plan revision and the evidence it cites, and a change to either will reopen the review;
- a document handoff or other resolution will be recorded with cited proof;
- a work queue will list the work across a rep's deals.

Later in this milestone, as reps ask for them: stakeholders on the card (each contact's role, when
they last spoke, and who has gone quiet), an email connector if pushing email is not enough,
rollups of recurring revenue at risk by team and by rule (computed by the connector, never by a
model), and alert digests and quiet hours.

### 7. Install and run it like any server

An operator will install, run and upgrade the connector without a checkout:

- a GitHub Actions release pipeline that publishes prebuilt static binaries for each platform and Docker images, with the web app embedded;
- a Homebrew tap;
- systemd and launchd templates;
- backup, restore, and an upgrade rehearsed on the restored copy, checked as one loop;
- the ten cases as ready-to-open demo workspaces, so a new user sees each one in the web app without credentials.

Three deployment shapes will each be documented once tested: a local CLI with an assistant app
over stdio MCP, a server in Docker Compose for a team, and a headless API for another system to
call.

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
- DOCX tracked changes as a source.
- Salesforce task creation.
- A second CRM.
- Windows.
- MCP Apps.

Not planned: language SDKs, WASM or CGO builds. Integrations use HTTP or MCP.

## Open questions

- **Ops access.** Is a grant enough, or is Ops a separate role?
- **Closing by hand.** May a person close a deal, or does only the CRM stage close it?
- **A bad unit.** Today one unit that fails a check stops the card. Should it be refused alone, with the card saying how many were?
- **Slippage before a deal is watched.** Should the connector also read the CRM's field history, so changes before its first read count?
- **Silence and Slack.** Should Slack authors be matched to contacts? Until they are, silence will count calls only.
- **The edges of a deal.** A call or thread that fits two deals of one account. A deal with no CRM record. A closed opportunity that reopens: the same deal, or a new one?
- **Reporting SQL.** A database role that BI tools connect to, or queries through the API so each one has a receipt?

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
