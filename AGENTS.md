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

Preserve the synthetic labels and fixed evaluation assertions until the
explicitly scoped source expansion. Write approval belongs only in its separately
requested later slice. Do not add a web interface or speculative rollout documents.
Keep the parked reliability fixes in their own follow-up slice. The documentation
pass has its own scope above; do not restore the old prose rewrite or add unrelated
improvements.

Keep fixture IDs stable and readable across reseeds. Use synthetic display
labels, not plausible company, person, or product names. Preserve examples of
a renewal within 30 days of the fixed demo date, an at-risk account, conflicting
CRM and call-note facts, an account without notes, and an instruction inside a
note. CRM, calls, and Slack default to SQLite mocks. The optional Salesforce
adapter uses HTTP fixtures in tests; do not claim a live org was verified.

## Council

Run council once on PR 2, which carries MCP and authorization core logic; skip
it for the seed/CLI foundation and housekeeping. Use all three full members:

| Provider | Member | Effort |
| --- | --- | --- |
| OpenAI | GPT Sol; record the exact available Sol model ID | High |
| Anthropic | Claude Opus; record the exact available Opus model ID | High |
| Google | Gemini 3.8 Flash (`gemini-3.8-flash-high`) | High |

Gemini 3.8 Flash is explicitly selected by the user as a full member. Do not
describe it as a flagship or treat its review as non-blocking. Include actual
model names and effort in the PR council table, with each round's returned score
or `no score`. No fabricated usage, scores, or agreement.

Give all members the same code, tests, and these attack questions. Put each
member's answers and evidence in the PR table, not just a general verdict:

- Can any tool return data outside the token's scopes?
- Can a tool call miss the audit log, including rejected calls and failures?
- What happens with an expired, malformed, or wrong-audience token?
- Is anything inside a source record ever treated as an instruction?

Maximum three council rounds total. If the council has not agreed after three,
stop and show the user the open findings and disagreements with evidence. Do
not run more rounds. If any selected member cannot be reached, stop on a local
branch and report it without substituting a different model.
