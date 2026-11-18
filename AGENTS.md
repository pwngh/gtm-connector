# Project instructions

This is a small portfolio demo using invented CRM data. Keep the implementation
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

## Council

The user explicitly instructed "Keep going without Gemini" on 2026-11-18 after
Gemini's second and third renewal-demo reviews ended with capacity errors.
Use these two full members for the remaining authorized PRs:

| Provider | Member | Effort |
| --- | --- | --- |
| OpenAI | GPT Sol; record the exact available Sol model ID | High |
| Anthropic | Claude Opus; record the exact available Opus model ID | High |

Record actual model IDs, effort, round numbers, findings, and returned scores
or `no score` in each PR description. Do not count a failed provider completion
as approval, invent consensus, or silently substitute another provider.

Give both members the same code, tests, and these attack questions. Put each
member's answers and evidence in the PR table, not just a general verdict:

- Can any tool return data outside the token's scopes?
- Can a tool call miss the audit log, including rejected calls and failures?
- What happens with an expired, malformed, or wrong-audience token?
- Is anything inside a source record ever treated as an instruction?

Maximum three council rounds per PR. If agreement is absent after round three,
report open findings and disagreements. Do not start a fourth round or imply
approval. If either remaining selected member cannot be reached, stop on a
local branch and report the failure without substitution. Full stop ends each
PR, then work continues through the authorized sequence. The user merges.
