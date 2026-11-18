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
- Record material scope choices and limitations in DECISIONS.md.
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
order replaces the remaining part of the sequence above. DECISIONS.md records
the reasons, the deferred items with their triggers, and the dropped items.

Judge every item by one test. Does it shorten the path from install to a brief
someone would use before a real renewal call? Today a person still chooses the
transcript excerpt and the Slack thread by hand, and no drafter summarizes.
Close that gap before adding surfaces.

Two checks come before the numbered slices:

- The owner runs `salesforce check` and `slack check` against a real org and
  workspace. This checks sign-in, visibility, and bounds only. Agents never run
  it. Keep "No live org has been verified." until the owner supplies the report.
- Run an extraction test outside the repository on five messy transcripts. A
  model proposes claims that cite numbered lines or cues, and a script prints
  the saved text for each cited range. The user reads the result as a rep
  would. If it is thin or wrong, stop and report before starting slice 1.
  Never commit a real transcript. Use a local model unless the user explicitly
  chooses a hosted one for this test.

Then finish one reviewed PR per slice, in this order:

1. Selected sources and caller-safe briefs, as already designed.
2. The local stdio MCP bridge. `connect --transport stdio` also writes the
   client configuration, after saving a backup of the existing file.
3. A Slack channel window. The operator pins one channel per account. The read
   has hard caps on days, messages, and thread replies.
4. Model-read claims. A model reads the whole selected transcript and channel
   window and proposes claims. The checker accepts a claim only when its cited
   units exist in the saved evidence. Repeat the five-transcript test with
   channel context before calling this slice ready.
5. Deal cards as terminal text and a local HTML preview. No Block Kit export
   yet, because nothing sends it.
6. Explicit `auth` and `configure` commands and saved daily profiles. Split
   `cli.py` in this slice.
7. The default chooser, as a numbered menu from the standard library. Add a
   macOS CI runner. Test on Linux and macOS.
8. Transcript folder intake. One subfolder per selected account, so exported
   files need no renaming. Import runs on command. There is no watcher process.
9. A renewals-due command that prepares briefs for selected accounts renewing
   within a chosen number of days. The operator schedules it. It follows the
   profile's model choice and writes its normal receipts.
10. A PyPI release. The owner publishes it.

Rules for model-read claims:

- The default demo, evaluation, and CI stay offline and use no model.
- The operator selects a model per profile. Local Ollama needs no key. A hosted
  endpoint needs the operator's own key, read from an environment variable or a
  private file. Never accept the key as a command-line flag. The key never
  enters a selection, profile, receipt, log, or tool result.
- Selecting a hosted endpoint sends selected source text off the machine. Say
  so when it is selected, show the endpoint host and model in the source
  labels, and record them with the brief.
- The model receives only evidence that already passed the source selection and
  the caller's scope checks. It receives no credential, tool definition, or
  tool. Its output is untrusted until the checker accepts it.
- The model cites numbered units. The connector prints the saved text for the
  cited range. Do not accept a retyped quote by approximate matching. Numbers
  and dates in a claim must appear in its cited units.
- Keep the 182-claim and 223-claim fixed suites unchanged as the offline
  baseline. Add a separate evaluation track for model-read claims.

Rules for the Slack channel window:

- The channel is pinned explicitly in the saved selection. Do not guess a
  channel from its name.
- Count excluded automated messages in the evidence instead of dropping them
  silently. A window that hit a cap is incomplete, not empty.
- Slack limits commercially distributed apps outside its Marketplace to 15
  messages per request and one request per minute on `conversations.history`
  and `conversations.replies`. Internal customer-built apps are exempt. Read
  Slack's current policy before choosing bounds and state the requirement.

Do not start these without the user's explicit request: MCP Apps and Slack
rich-response contracts, native Windows support, CRM writes, a Slack bot,
calendar triggers, provider transcript adapters, a second CRM, and the team
deployment bundle of Postgres, single sign-on, per-user tokens, a container,
a hash-chained audit log, and redaction. Do not add a `ROADMAP.md`; this
section and DECISIONS.md hold the plan.

## Council

The user chose the roster "Fable, Sol, Grok" on 2026-11-18 for the remaining
authorized PRs. It replaces the two-member roster that followed the earlier
instruction "Keep going without Gemini". PRs #26 through #29 were reviewed by
Sol and Opus under that earlier roster. Use these three full members from the
next PR onward:

| Provider | Member | Effort |
| --- | --- | --- |
| Anthropic | Claude Fable 5.2, model ID `claude-fable-5-2`, through the `claude` CLI | High |
| OpenAI | GPT Sol; record the exact available Sol model ID | High |
| xAI | Grok 4.7, model ID `grok-4.7`, through the `grok` CLI, version 1.0.13 or later | High |

Each review is a separate, independent run. The orchestrating session does not
count as a member's review, even when it runs on a member's provider.

Record actual model IDs, effort, round numbers, findings, and returned scores
or `no score` in each PR description. Do not count a failed provider completion
as approval, invent consensus, or silently substitute another provider.

Give all three members the same code, tests, and these attack questions. Put each
member's answers and evidence in the PR table, not just a general verdict:

- Can any tool return data outside the token's scopes?
- Can a tool call miss the audit log, including rejected calls and failures?
- What happens with an expired, malformed, or wrong-audience token?
- Is anything inside a source record ever treated as an instruction?

Maximum three council rounds per PR. If agreement is absent after round three,
report open findings and disagreements. Do not start a fourth round or imply
approval. If any selected member cannot be reached, stop on a
local branch and report the failure without substitution. Full stop ends each
PR, then work continues through the authorized sequence. The user merges.
