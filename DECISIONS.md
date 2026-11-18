# Decisions

This file holds what governs work now: the current build order, the review
configuration, and a list of earlier decisions. The earlier dated entries are
in [DECISION_LOG.md](DECISION_LOG.md). They were moved there unchanged on
2026-11-18. Every agent session and every reviewer read this file in full,
and most of it described finished slices.

Search the log for the entry a task needs. Do not read it whole. The README
lists each guarantee with its tests under "What it guarantees". The log
records why each choice was made.

## A build order for one operator's real use (2026-11-18)

The user asked for a workable product, not a coding exercise, and reviewed a
proposed 40-item roadmap against the code on main at `8263d27`. This entry
records what was kept, what waits, and what was dropped. AGENTS.md holds the
resulting order and rules. No behavior changes in this entry's PR.

**The gap that decides usefulness.** A brief quotes one transcript excerpt per
account, and the person chooses it. The default is up to the first 20 lines or
cues plus one adjacent unit of context, and fewer when the quote bound requires
it. Slack contributes one thread, mapped by hand. The default drafter formats
fixed facts, and the optional local model assembles the same fixed claims. The
brief therefore saves formatting time. It does not save reading time, because
the person still finds the important moment in the call and the right thread.

**The test for each item.** Does it shorten the path from install to a brief
someone would use before a real renewal call, or remove a reason they would
stop using it? Items that only add infrastructure for users who do not exist
yet fail this test.

**Who it serves.** One technical operator runs it locally. Reaching people who
do not use a terminal needs hosting, sign-on, and per-user tokens. That work
waits for a team that commits to deploy it.

### Kept and reordered

The order is in AGENTS.md. These are the reasons for the changes from the
earlier sequence.

- The extraction test runs before slice 1. It needs only the transcript parser,
  a model, and a range check, and it decides whether the later slices are worth
  building. Spending two security-heavy PRs first would test the cheapest
  assumption last. An agent proves the script on invented transcripts and the
  owner runs it on real ones, because an agent session is itself a hosted
  model and the rule against employer or client material still applies. The
  test keeps the pending-record rule instead of requiring a local model, so
  the owner still chooses the model and every call leaves a record.
- The Slack channel window comes before model-read claims. Otherwise the model
  reads a whole call but the person still hunts for the thread.
- The model cites numbered units instead of retyping quotes. Transcripts are
  already stored as indexed lines or cues. A model that retypes a quote drops
  filler words and changes punctuation, and loosening the match to tolerate
  that would weaken the check. A cited range needs no approximate matching.
- A hosted model is allowed only by the operator's choice and with their own
  key. For slice 4 this replaces the "No hosted model calls" limit of the
  earlier local-model slice. The existing Ollama adapter stays local and the
  default stays offline. A hosted endpoint sends selected source text off the
  machine, so the choice is explicit and labeled. A pending record is
  committed before each call is sent, so a crash or a failed call still leaves
  a record that a call to that host and model was attempted. The record never
  holds the prompt or source text, and only a finished outcome can show that
  the call was sent.
- The key comes from an environment variable or a private file with mode 0600.
  A flag would leak it into shell history and the process list.
- A cited range shows where a claim points. It does not prove the claim. An
  instruction inside a transcript could still steer the wording of a claim
  that cites a real line. Model-read claims are therefore labeled as
  model-proposed and shown beside their cited text, and the instruction case
  stays in the evaluation.
- One model call can read CRM, call, and Slack evidence together, and the
  citation the model picks is not proof of where a sentence came from. A
  model-read claim is returned only to a caller holding every scope the call's
  input required.
- Saved call evidence is a selected excerpt today. Reading a whole transcript
  is a separate explicit choice, so that selecting a model does not quietly
  widen what leaves the audited calls read.
- Deal cards ship as terminal text and local HTML. A Block Kit export proves
  nothing until something sends it.
- The chooser uses a numbered menu from the standard library. A prompt library
  would add runtime dependencies and startup cost for arrow keys.
- Transcript intake uses one subfolder per account. A filename prefix would
  still make the person rename every export.
- The current thread reader includes bot messages and refuses unsupported
  subtypes. The channel slice keeps that unless it records a reason to change
  it. If it excludes automated messages, it counts them, because silent
  removal would make an incomplete window look complete.

Slack limits new commercially distributed apps outside its Marketplace, and new
installations of them, to 15 messages per request and one request per minute on
the two history methods. Marketplace apps, internal customer-built apps, and
existing installations are outside that limit. The operator's existing
registration decides which limit applies, and the channel slice checks it. An
agent never creates or changes an app, and under the restricted limit a window
can be incomplete by design. Read on 2026-11-18:
[rate limit changes](https://docs.slack.dev/changelog/2025/05/29/rate-limit-changes-for-non-marketplace-apps),
[rate limits](https://docs.slack.dev/apis/web-api/rate-limits).

### Waiting for a trigger

| Item | Trigger |
| --- | --- |
| MCP Apps and Slack rich-response contracts | A real host session that can be tested. |
| Native Windows support | A Windows user. |
| CRM writes with owner approval | Reads used on a real org. Start with approving one task from the CLI. |
| Slack bot and calendar-triggered briefs | Scheduled briefs proving useful. |
| Provider transcript adapters and a second CRM | A real user naming their tool. |
| Postgres, single sign-on, per-user tokens, a container, a hash-chained audit log, redaction | A specific team committing to deploy. |

A hash-chained log protects against someone other than the owner. A single
owner can rewrite their own local database either way, so it belongs with the
team deployment.

### Dropped

- A ports-and-adapters refactor as its own PR. Extract an interface when a
  second implementation arrives.
- A snapshot cache with background refresh. Slice 1 requires its saved-brief
  read to return without provider calls, and `check-brief` already validates a
  saved brief offline.
- Email, support, and product-usage connectors.
- Follow-up question threads in Slack. A connected MCP client can call the
  read tools again to follow up, so a separate thread feature is not needed.
- A real-time drift watcher. It needs public ingress.
- A policy file, a model router with failover, a telemetry dashboard, and a
  plugin SDK.
- Additional workflows, sub-agent token attenuation, watchdogs, PDF and
  compliance exports, cross-account analytics, marketplace packaging,
  multi-region workers, and a published protocol specification.
- An admin web console, a React bundle, and a Go launcher.

### Not yet observed

No extraction test has run. No live org or workspace has been read by the
owner's check. The channel window, model-read claims, and every later slice are
plans, not implemented behavior. The review tiers have not been used on a slice
yet. README, SECURITY_REVIEW.md, and two reference pages change one link each,
to point at the decision log. They still describe only what exists.

## Review configuration

The installed TStack source is [t3dotgg/tstack at 11e37a7](https://github.com/t3dotgg/tstack/tree/11e37a74d0a6868c121cd0910ab2b3bd9873b92a).
TStack supplies the installed full-stop, council, review, filing, and CI-watching
skills. Full stop ends each ready PR, then work continues on the next authorized
slice. The user merges.

The renewal-demo PR used OpenAI `gpt-6.2-sol`, Anthropic `claude-opus-5-5`, and
Google `gemini-3.8-flash-high`, each at High. Gemini's second and third review
responses included approval text but ended with an API 503 capacity error. Those
failed completions were not counted as approval. The user then explicitly said
"Keep going without Gemini" on 2026-11-18. The completed Sol and Opus reviews
satisfied the revised gate; no fourth review round ran.

Sol and Opus were the two full members for PRs #26 through #29.

The user chose the roster "Fable, Sol, Grok" on 2026-11-18, added Gemini 4
Argon through the `agy` CLI the same day, and accepted two review tiers.
AGENTS.md holds the roster, the tier rule, and the questions. These are the
reasons.

- **Two tiers.** A four-member council on every PR costs time and adds little
  on a change that is far from the guarantees. Those changes get the Sol loop.
- **Rule changes are council-tier.** Round one of this entry's PR was a
  two-file documentation change, and every member returned real findings
  about the rules it added.
- **One prompt for every member.** Independent reviews of identical material
  are the point of a council. Giving each member a different question would
  leave each area with one reviewer.
- **Grok is non-blocking.** Its first two non-interactive runs were cancelled
  at a tool approval, and its first complete review took about twelve minutes.
  Its findings were as useful as the others', so it still runs every time.
  It now runs at Low with the brief and the diff inside its prompt and no
  tools. On a 25 KB prompt a Medium run took about twice as long and found no
  more, and a High review with tools takes 7 to 14 minutes. The Low run gets
  no exported copy, the one exception to identical material. A diff of
  150,000 bytes or more still goes to Grok's copy with tools at High.
- **Exported copies, not the live checkout.** `grok` and `agy` cannot ask for
  approval in a non-interactive run, so they run with automatic approval
  inside their sandboxes. The sandbox is the isolation. The private copy keeps
  every member that reads files on identical material and out of the live
  checkout.
- **The guarantee tests stay in unittest.** A proposal to add a pytest marker
  suite that runs in under two seconds did not fit this repository. It has no
  pytest dependency, and the five test files behind the README's guarantees
  ran 53 tests in about 35 seconds on 2026-11-18.
- **The log was moved, not deleted.** Lines 3 through 1400 of the previous
  DECISIONS.md are byte-identical in DECISION_LOG.md. Reviewers in round one
  cited those entries to catch contradictions, so they stay searchable.

Record actual model IDs as each tool reports them, effort, round numbers,
findings, and returned scores or `no score`. Maximum three rounds per PR.
Report unresolved findings or disagreements after round three instead of
implying approval. Do not invent usage, scores, identities, or a successful
provider completion.

Review tooling uses existing accounts and is separate from the product runtime.
It must not create accounts, trials, subscriptions, or keys. The connector calls
no hosted model today. The 2026-11-18 build order above allows one later, only
by the operator's choice and with their own key.

## Entries in the decision log

The entries added after the original slices come first, newest first, and all
are dated. The entries for the original slices follow in the order they were
built. They are undated except the third, "Protocol research (2026-11-17)".
Each title links to its entry in DECISION_LOG.md.

- [Selected sources and caller-run briefs (2026-11-18)](DECISION_LOG.md#selected-sources-and-caller-run-briefs-2026-11-18)
- [The owner waived the pre-slice checks (2026-11-18)](DECISION_LOG.md#the-owner-waived-the-pre-slice-checks-2026-11-18)
- [Explicit mapped Slack reads and source observations (2026-11-18)](DECISION_LOG.md#explicit-mapped-slack-reads-and-source-observations-2026-11-18)
- [Separate Slack authentication from source selection (2026-11-18)](DECISION_LOG.md#separate-slack-authentication-from-source-selection-2026-11-18)
- [Imported call excerpts with explicit account associations (2026-11-18)](DECISION_LOG.md#imported-call-excerpts-with-explicit-account-associations-2026-11-18)
- [Explicit Salesforce environments and private connection state (2026-11-18)](DECISION_LOG.md#explicit-salesforce-environments-and-private-connection-state-2026-11-18)
- [A renewal story and separate guarantee checks (2026-11-17)](DECISION_LOG.md#a-renewal-story-and-separate-guarantee-checks-2026-11-17)
- [An explicitly bound Salesforce demo book (2026-11-17)](DECISION_LOG.md#an-explicitly-bound-salesforce-demo-book-2026-11-17)
- [Standard Salesforce renewal planning and CLI credentials (2026-11-17)](DECISION_LOG.md#standard-salesforce-renewal-planning-and-cli-credentials-2026-11-17)
- [Measure process startup and audited calls (2026-11-17)](DECISION_LOG.md#measure-process-startup-and-audited-calls-2026-11-17)
- [Running server and daily commands (2026-11-17)](DECISION_LOG.md#running-server-and-daily-commands-2026-11-17)
- [Installed CLI and output contract (2026-11-17)](DECISION_LOG.md#installed-cli-and-output-contract-2026-11-17)
- [Generated audit receipt sequences (2026-11-17)](DECISION_LOG.md#generated-audit-receipt-sequences-2026-11-17)
- [Request refusal reasons (2026-11-17)](DECISION_LOG.md#request-refusal-reasons-2026-11-17)
- [Python checks and dependency hashes (2026-11-17)](DECISION_LOG.md#python-checks-and-dependency-hashes-2026-11-17)
- [README navigation (2026-11-17)](DECISION_LOG.md#readme-navigation-2026-11-17)
- [Documentation for readers new to the project (2026-11-17)](DECISION_LOG.md#documentation-for-readers-new-to-the-project-2026-11-17)
- [Pure request decisions (2026-11-17)](DECISION_LOG.md#pure-request-decisions-2026-11-17)
- [Shared tool definitions (2026-11-17)](DECISION_LOG.md#shared-tool-definitions-2026-11-17)
- [Development container and Codespaces (2026-11-17)](DECISION_LOG.md#development-container-and-codespaces-2026-11-17)
- [Recorded walkthrough and Inspector output (2026-11-17)](DECISION_LOG.md#recorded-walkthrough-and-inspector-output-2026-11-17)
- [Saved evidence and audit inspection (2026-11-17)](DECISION_LOG.md#saved-evidence-and-audit-inspection-2026-11-17)
- [Optional Salesforce reads (2026-11-17)](DECISION_LOG.md#optional-salesforce-reads-2026-11-17)
- [Optional account-channel context (2026-11-17)](DECISION_LOG.md#optional-account-channel-context-2026-11-17)
- [Optional local model drafting (2026-11-17)](DECISION_LOG.md#optional-local-model-drafting-2026-11-17)
- [External Inspector client (2026-11-17)](DECISION_LOG.md#external-inspector-client-2026-11-17)
- [Scope and implementation](DECISION_LOG.md#scope-and-implementation)
- [Four reviewable slices](DECISION_LOG.md#four-reviewable-slices)
- [Protocol research (2026-11-17)](DECISION_LOG.md#protocol-research-2026-11-17)
- [Two source systems and fixed fixtures](DECISION_LOG.md#two-source-systems-and-fixed-fixtures)
- [Local authorization boundary](DECISION_LOG.md#local-authorization-boundary)
- [Audit, limits, and writes](DECISION_LOG.md#audit-limits-and-writes)
- [Briefing and citation boundary](DECISION_LOG.md#briefing-and-citation-boundary)
- [README baseline after PR 3](DECISION_LOG.md#readme-baseline-after-pr-3)
- [Explicit conflict rule](DECISION_LOG.md#explicit-conflict-rule)
- [Evaluation and usage measurements](DECISION_LOG.md#evaluation-and-usage-measurements)
