# Project instructions

This is a local connector for one technical operator, such as a RevOps engineer,
founder, or sales engineer. It began as a small portfolio demo and keeps that
offline demo with invented CRM data as its default. Keep the implementation
plain and finish one runnable PR slice at a time. Use Python and SQLite.

- Do not use employer/client code, data, names, or documents. Real APIs belong
  only in explicitly selected adapters; the default stays offline.
- The default demo and evaluation must run without a paid model key.
- Back every README behavior claim with a test or recorded measurement.
- Use full-stop: finish, test, review, file the PR, watch checks, and leave it
  open for the user to merge. Never merge, enable auto-merge, or enter a queue.
- Only the initial README/license bootstrap may be pushed to main. All later
  pushes must be to this task's own branches. Create only a private pwngh repo.
- Inspect the full tree and history for private material before the first push
  and before sending source to external review providers.
- Record a material scope choice or limit as a dated entry at the top of
  DECISION_LOG.md, and add its title to the list in DECISIONS.md. Change the
  rest of DECISIONS.md only when the build order or review configuration
  changes. Put build logs, test counts, and review transcripts in the PR
  description, not in either file.
- Before a task, read this file and DECISIONS.md. Then read the documents and
  modules the task touches and what they depend on, such as callers and tests.
  Search DECISION_LOG.md for a past decision. Do not read it whole.
- Read the current MCP specification and authorization requirements before
  implementing the protocol. Both researched revisions are 2026-07-28, read on
  2026-11-17. Record revision and reading date in the README.

## Reviewable slices

The first four runnable slices are complete. Do not add a web interface.

1. Seeded CRM, CLI inspection, and data tests.
2. MCP tools, local authorization, separate CRM and calls permissions, audit,
   rate limits, and idempotent writes. Include the small fixture update here.
   Prove scope denial, one audit receipt per tool call, safe write retries, and
   a clear rate-limit error. Run council on this PR.
3. Brief workflow with source citations, an uncited-sentence checker, an
   injection test, and fixtures that run without a model.
4. Twenty-account evaluation with recorded README scores, one-page security
   review, audit-based usage by token/tool/day and model tokens per brief,
   README polish, and a five-minute clean-checkout demo.

## Requested follow-ups

Keep one PR per slice, in this order. The user merges. Each change gets its own
proof and Sol review; leave every PR open and unmerged. Stack dependent branches
when the earlier change is still under review.

5. Finish the README with test-linked guarantees, actual output, a request-path
   diagram, and reference docs. Change documentation only. Preserve all limits.
   Check commands from a clean checkout, links, and the rendered GitHub page.
   Run one reader council round using only the README. Ask what the project
   does, what is proven, and what confused each member. Fix findings raised by
   at least two members. Record model, effort, round, and score or `no score`.
6. Official MCP Inspector compatibility. Prove capability discovery, five tool
   schemas, reads, and scope denial through the existing audited gateway.
7. Optional local Ollama drafting and measured rejection results. No hosted
   model calls. Keep the default demo and CI model-free.
8. An isolated mock Slack store, its own read scope, and a cited follow-up draft.
9. An optional read-only Salesforce Developer Edition adapter. Never write to
   an external organization. Use HTTP fixtures in tests; request no keys.
10. Finish the parked local reliability fixes after the feature slices merge.
    Save brief snapshots by atomic replacement, make audit inspection read-only,
    and reject malformed saved evidence with a clean CLI error. Port only the
    relevant code and tests; keep the current README wording and measurements.
11. Record a rerunnable fixture walkthrough and the official Inspector CLI
    listing of all six tools. Keep credentials and local home paths off screen.
    Publish the script, recording, selected transcript, and capture provenance.
    Do not claim the Inspector UI or interactive login was tested.
12. Add a development container and an Open in Codespaces badge separately.
    Do not create a codespace or change repository settings. Run the container
    locally or label it unverified.

After these presentation slices, the user requested one branch and PR per item:
one tool-definition table; pure request decisions; pinned lint/type tooling and
main-branch CI; fixed refusal reasons; generated audit sequences; audit CHECK
constraints; tool-definition hashes; control-character handling; confirmation
of the minimal metadata scope; owner-approved write proposals; and, later,
Salesforce renewal-field mapping. Check main first and skip work already done.
The first three are behavior-preserving, with existing tests unchanged.
Stop after each open PR. The user decides when to continue.

The refusal catalog is merged. Add Hypothesis sequence tests for committed audit
receipts before tool execution and at-most-once receipt finalization. Keep
Hypothesis in the development lock and run it explicitly in CI. The default demo
and runtime dependencies stay unchanged. Use real SQLite observation and failure
injection. Leave production constraints and schema changes for their own slice.

The tooling slice is merged. Next, give request-check refusals fixed reason names
and one status/code table. Preserve messages, HTTP statuses, authorization
challenges, and check order. Keep SDK errors, tool errors, OAuth errors, and audit
failure responses outside this catalog. Add focused contract tests and document
the distinction. Do not change audit storage or add the later policy features.

The shared tool definitions, pure request decisions, and documentation passes are
merged. The tooling slice adds Ruff and mypy in pyproject.toml, hash-checked
runtime and development locks, and CI on main. Preserve runtime versions and
existing tests. Limit source edits to annotations, equivalent SDK field names,
explicit re-exports, and line wrapping. Check application and script lines at
120 characters. Keep test files outside static checks during this slice and
state that limit in the development guide. Do not broaden this into formatting,
new request policy, or additional adapters.

After pure request decisions, finish the separately requested README and reference
documentation pass before starting tooling. Lead with the reader's purpose, then
explain how the parts work and where their guarantees end. Define unfamiliar
product terms briefly. Use tables, diagrams, and examples where they explain
more directly than prose. Keep useful wording and vary sentence structure
without expanding every technical term. Preserve existing commands, captured output,
tables, numbers, links, and limits. Change no runtime behavior or tests. Record
the scope choice in DECISIONS.md and use the same full-stop workflow.
The user also requested an adversarial council comparison against main after
editing. Apply the roster and three-round limit below, then record the conclusion.

The later README comparison with Soulbeet, Homelable, and HomeButler changes
navigation only. Keep the merged prose and reference pages. Add section links,
put the local run before Codespaces, and separate optional features. Treat these
repositories as structural examples, not measured proof of README performance.
Use council against main and full-stop for this documentation-only change.

The user's later renewal-demo slice explicitly permits plausible invented names
and account-specific source text. Keep its independently authored expected claims
in sync when those fixtures change. Write approval belongs only in its separately
requested later slice. Do not add a web interface or speculative rollout documents.
Keep the parked reliability fixes in their own follow-up slice. The documentation
pass has its own scope above; do not restore the old prose rewrite or add unrelated
improvements.

Keep fixture IDs stable and readable across reseeds. Use the fixed invented
book and reserved `.example` email domains; never copy names or records from an
org or employer. Preserve examples of
a renewal within 30 days of the fixed demo date, an at-risk account, conflicting
CRM and call-note facts, an account without notes, and an instruction inside a
note. Preserve the missing contact role and dated old note in the fixture book.
CRM, calls, and Slack default to SQLite mocks. The optional Salesforce
adapter uses HTTP fixtures in tests; do not claim a live org was verified.

`demo` follows Morgan Reed preparing Juniper Crest Learning's renewal with one
checked CRM, calls, and Slack brief. `verify` retains the separate guarantee
checks and their JSON shape. The quick and long walkthroughs use the same
invented story. Keep verification state separate because its retry check creates
one local activity. The historical Ollama report remains evidence for its original
fixture corpus, not a new run against the current book.

## Authorized connected MVP sequence

After the original six CLI, performance, Salesforce, kit, and narrative PRs,
the user authorized a usable single-operator MVP. Finish one reviewed runnable
slice at a time: explicit Salesforce environments and connection identity;
transcript imports; existing Slack user authentication; bounded mapped Slack
threads; selected-source permissions and caller-safe briefs; a local stdio MCP
bridge; Deal card projections; MCP Apps and Slack rich-response contracts;
Windows, Linux, and macOS support; explicit authentication/configuration and
saved daily profiles; then the default interactive chooser. Check main before
each slice and stack on the last ready branch unless it has merged.

Keep authentication, source selection, and daily use separate. The no-setup demo
remains available, source labels distinguish fixtures from selected real data,
and failures never silently substitute fixtures. Local card previews and MCP
resources are explicitly authorized later; public deployment and Slack sending
remain outside scope. Use existing provider registrations only. Do not claim
real-host login or rendering from a fake provider or local protocol harness.

## Build order from 2026-11-18

The user asked for a product a person can use, not a coding exercise. This
section replaces the list of remaining slices in the sequence above. The rules
in that section still apply: keep authentication, source selection, and daily
use separate, label sources, never substitute fixtures silently, and use
existing provider registrations only. The earlier authorization for MCP
resources is withdrawn until the user asks for it again. DECISIONS.md records
the reasons, the deferred items with their triggers, and the dropped items.

Judge every item by one test. Does it shorten the path from install to a brief
someone would use before a real renewal call? Today a person still chooses the
transcript excerpt and the Slack thread by hand, and no drafter summarizes.
Close that gap before adding surfaces.

Two checks come before the numbered slices:

- The owner runs `salesforce check` and `slack check` against a real org and
  workspace. Agents never run them. `salesforce check` reads account, contact,
  contract, and opportunity records and makes one brief in temporary private
  state. `slack check` reads the mapped thread. Both print a sanitized report
  without source text. Keep "No live org has been verified." until the owner
  supplies the report.
- Run an extraction test outside the repository. A model proposes claims that
  cite numbered lines or cues, and a script prints the saved text for each
  cited range. An agent builds the script and proves it on invented messy
  transcripts only. An agent session is itself a hosted model, so an agent
  never reads a real transcript. The owner then runs the script on five real
  transcripts they are free to use, on their own machine, and reads the result
  as a rep would. If it is thin or wrong, stop and report before starting
  slice 1. The rules below on keys, hosted endpoints, pending records, and
  giving the model no tool apply to this test. The script commits its pending
  records to a private local file, so a hosted call is never sent without one.
  The gateway and its receipts apply to product calls, not to this standalone
  script. The owner chooses the model for the real run and may choose a hosted
  one. Commit only invented transcripts.

Then finish one reviewed PR per slice, in this order:

1. Selected sources and caller-safe briefs. The design notes are outside the
   repository, so this slice's PR records its design in DECISION_LOG.md. The
   requirements are fixed. A remote brief tool runs under the caller's own
   token and never the owner's. Every live read is limited to the caller's
   scopes, the saved selection, and what the provider shows. Each composite
   call commits one outer receipt and one receipt per attempted read. Reading a
   saved brief is not a live read, because it makes no provider call. It
   releases the brief only if the caller's current scopes, the current
   selection, and the current source bindings still cover everything the brief
   contains. It cannot recheck what the provider shows now, and its result
   says so.
2. The local stdio MCP bridge. Every request still goes through the existing
   gateway, with scopes named at launch, a receipt committed before execution,
   and the rate limit. At launch the bridge requests a grant for exactly the
   named scopes through the existing local owner flow, and keeps the token in
   memory only. It may renew that grant for the same scopes or fewer and never
   for more. An expired, malformed, or wrong-audience token is refused as it
   is today, and a request is never retried with wider authority.
   `connect --transport stdio`
   also writes the client configuration. It merges only the connector's entry,
   keeps the file's mode, and saves a backup with that mode first. It refuses
   a symlink for the file or the backup, and it refuses to write if the file
   changed after it was read. The file holds paths and scopes, never a token
   or an owner credential.
3. A Slack channel window. The operator pins one channel per account. Before
   choosing caps, the slice checks which Slack rate limit the operator's
   existing registration falls under. That depends on how the app is
   distributed and when it was created and installed, so the owner supplies
   those facts. The read keeps the existing shared budget of HTTP attempts,
   response bytes, and one deadline, and adds hard caps on days, messages, and
   thread replies.
4. Model-read claims. A model reads the selected evidence and proposes claims
   that cite numbered units. Repeat the extraction test with channel context
   before calling this slice ready.
5. Deal cards as terminal text and a local HTML preview. The preview is one
   static file with source text escaped, no script, no remote resource, and no
   server. No Block Kit export yet, because nothing sends it.
6. Explicit `auth` and `configure` commands and saved daily profiles. Split
   `cli.py` in this slice.
7. The default chooser, as a numbered menu from the standard library. Add a
   macOS CI runner. Test on Linux and macOS.
8. Transcript folder intake. One subfolder per selected account, so exported
   files need no renaming. Import runs on command. There is no watcher process.
   The account association uses the audited CRM read, as `calls import` does
   today. Never choose a file by its modification time. The slice's PR states
   how several files for one account are ordered and bounded.
9. A renewals-due command that prepares briefs for selected accounts renewing
   within a chosen number of days. The operator schedules it. It calls the
   audited tools with explicitly named scopes, like any other caller. It only
   reuses or silently renews an existing grant for those scopes or fewer.
   Unlike the bridge, it never starts the owner flow. The existing bounded,
   non-interactive credential refresh still applies. If the local token or a
   Slack or Salesforce credential is still unusable after that, it fails that
   account with a recorded receipt and a clear error. It never prompts, signs
   in, or substitutes fixtures.
10. A PyPI release. The owner publishes it.

Rules for model-read claims:

- The default demo, evaluation, and CI stay offline and use no model. The
  existing Ollama adapter stays local. For slice 4 only, these rules replace
  the "No hosted model calls" limit in follow-up 7 above.
- The operator selects the model. Until saved profiles exist in slice 6, that
  is an explicit command option, and slice 6 stores the same choice. Local
  Ollama needs no key. A hosted endpoint needs the operator's own key, read
  from an environment variable or a private regular file with mode 0600. Never
  accept the key as a command-line flag. The key never enters a selection,
  profile, saved brief, receipt, log, error message, or tool result.
- A hosted request uses HTTPS with verified TLS, no redirects, and no
  environment proxy. It sends the key only to the configured host, and it has
  a time limit and a response size limit.
- Selecting a hosted endpoint sends selected source text off the machine. Say
  so when it is selected, and show the endpoint host and model in the source
  labels.
- Commit a pending record before a model call is sent, and send nothing if
  that commit fails. Finish the record with the outcome and usage counts,
  including for a failed or rejected call. A pending record stays if finishing
  fails. The record names the endpoint host and model. Never record the key,
  the prompt, or rejected candidate text.
- The model receives only evidence that already passed the source selection and
  the caller's scope checks. It receives no credential, tool definition, or
  tool. A model-read claim is returned only to a caller that holds every scope
  the model call's input required.
- Today the saved evidence for a call is a selected excerpt. The model reads
  more than that excerpt only when the operator explicitly selects the whole
  transcript for that account. The whole transcript then reaches the model
  through the audited calls read into saved evidence, inside a stated unit and
  byte bound, and never by reading the import store directly. Bound the model's
  input and output. Never truncate evidence silently.
- The model cites numbered units, and the connector prints the saved text for
  the cited range. Do not accept a retyped quote by approximate matching.
  Numbers and dates in a claim must appear in its cited units.
- A cited range shows where a claim points. It does not prove the claim. Label
  model-read claims as model-proposed in briefs and tool results, and show the
  cited text beside each one. Source text reaches the model as quoted data.
  Source text and accepted claims stay data. They never select a tool, a
  grant, or a write.
- Never publish a claim that failed the check. The slice's PR decides whether
  a failed claim rejects the whole brief, as the current checker does, or is
  withheld with a visible count, and records the reason. Required disclosures
  for conflicts and for missing, denied, or incomplete sources stay required
  whatever the model writes.
- Keep the 182-claim and 223-claim fixed suites unchanged as the offline
  baseline. Add a separate evaluation track for model-read claims, and keep
  the instruction-inside-a-source case in it.

Rules for the Slack channel window:

- The channel is pinned explicitly in the saved selection. Do not guess a
  channel from its name. An agent never creates or changes a Slack app.
- Keep the current handling of message subtypes unless the slice's PR changes
  it with a recorded reason. If the slice excludes automated messages, the
  evidence records how many, without their text.
- A window that hit a cap is incomplete. The brief must say so. Do not present
  it as empty or complete.
- Slack limits new commercially distributed apps outside its Marketplace, and
  new installations of them, to 15 messages per request and one request per
  minute on `conversations.history` and `conversations.replies`. Marketplace
  apps, internal customer-built apps, and existing installations are outside
  that limit. Under it a window can be incomplete by design. Read Slack's
  current policy before choosing bounds.

Do not start these without the user's explicit request: MCP Apps and Slack
rich-response contracts, native Windows support, CRM writes, a Slack bot,
calendar triggers, provider transcript adapters, a second CRM, and the team
deployment bundle of Postgres, single sign-on, per-user tokens, a container,
a hash-chained audit log, and redaction. Do not add a `ROADMAP.md`; this
section and DECISIONS.md hold the plan.

## Review tiers and council

The user set the council on 2026-11-18 as "Fable, Sol, Grok", added Gemini 4
Argon the same day, and accepted two review tiers. This replaces the two-member
roster that followed the earlier instruction "Keep going without Gemini". PRs
#26 through #29 were reviewed by Sol and Opus under that roster.

Choose the tier from what the diff touches. State the tier and the reason in
the PR description. When unsure, use the council.

- **Sol loop only.** A change that touches none of the items in the next
  bullet. Run the `sol-loop` skill with GPT Sol at High.
- **Council.** A change that touches `gateway.py`, `auth.py`, `audit.py`,
  `citations.py`, `store.py`, `request_checks.py`, `tool_definitions.py`,
  `refusals.py`, `runtime.py`, `briefing.py`, `client.py`, credential or token
  handling, source selection or read bounds (for example `slack_reader.py`,
  `slack_mapping.py`, and `calls_import.py`), a new outside connection, a
  model call, the rules in this file, the build order or review configuration
  in DECISIONS.md, SECURITY_REVIEW.md, or the README's guarantees.

| Provider | Member | Effort | Gate |
| --- | --- | --- | --- |
| Anthropic | Claude Fable 5.2, model ID `claude-fable-5-2`, through the `claude` CLI | High | Blocking |
| OpenAI | GPT Sol; record the exact Sol model ID. PRs #25 through #29 used `gpt-6.2-sol` | High | Blocking |
| Google | Gemini 4 Argon, model ID `gemini-4-argon-high`, through the `agy` CLI | High | Blocking |
| xAI | Grok 4.7, model ID `grok-4.7`, through the `grok` CLI, version 1.0.13 or later | Low, or High for a diff of 150,000 bytes or more | Non-blocking |

Grok runs alongside the others under the same time limit for each reviewer.
Treat a finding it returns in time like any other finding: fix it or decline
it with a written reason. Its verdict does not gate the PR. A failed or late
Grok run does not stop the PR and gets no score.
Its first complete review took about twelve minutes, the longest of the four.
It now runs at Low in one pass, with the diff inside its prompt and no tools.
On a 25 KB prompt on 2026-11-18 that took 159 seconds and returned three valid
findings. The same prompt took 339 seconds at Medium and found no more, and a
review with tools at High takes 7 to 14 minutes. For a diff of 150,000 bytes
or more, Grok instead reads its exported copy with tools at High.
If a blocking member cannot be reached, stop on a local branch and report the
failure without substitution.

Each review is a separate, independent run. The orchestrating session does not
count as a member's review, even when it runs on a member's provider.

Give every member the same prompt, code, tests, and proof. Run each member
read-only or inside its tool's sandbox. Give each member its own exported copy
of the reviewed commit with the diff as a file, and do not run a reviewer in
the live checkout. The copy does not isolate a reviewer by itself. It keeps
every member on identical material and lets work continue while a slow member
reads. Tell reviewers to read the diff first and to search DECISION_LOG.md
instead of reading it whole.

Ask every member these questions. Put each member's answers and evidence in the
PR table, not just a general verdict:

- Can any tool return data outside the token's scopes?
- Can a tool call miss the audit log, including rejected calls and failures?
- What happens with an expired, malformed, or wrong-audience token?
- Is anything inside a source record ever treated as an instruction?
- Can a race, lock, timeout, or cancellation break one of the guarantees?
- Does the diff do anything its stated scope and contracts do not include?

Record actual model IDs as each tool reports them, effort, round numbers,
findings, and returned scores or `no score` in each PR description. Do not
count a failed provider completion as approval, invent consensus, or silently
substitute another provider.

Maximum three council rounds per PR. Round one reviews the whole change. Later
rounds review only changed text and open findings. If agreement is absent
after round three, report open findings and disagreements. Do not start a
fourth round or imply approval.

Before coding a council-tier slice that changes code, list the three most
likely ways it could break a guarantee and write a failing test for each one
first. After each edit
to a module in the council list, run the tests behind the README's guarantees:

```sh
.venv/bin/python -m unittest tests.test_audit tests.test_citations tests.test_gateway tests.test_slack_gateway tests.test_briefing
```

That ran 53 tests in about 35 seconds on a loaded machine on 2026-11-18.

Full stop ends each PR, then work continues through the build order above.
The user merges.
