# Project instructions

This is a small portfolio demo using invented CRM data. Keep the implementation
plain and finish one runnable PR slice at a time. Use Python and SQLite.

- Do not use employer/client code, data, names, documents, or real CRM APIs.
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

## Four pull requests

Keep the project to these four runnable slices. Do not add a web interface.

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

Keep fixture IDs stable and readable across reseeds. Use synthetic display
labels, not plausible company, person, or product names. Preserve examples of
a renewal within 30 days of the fixed demo date, an at-risk account, conflicting
CRM and call-note facts, an account without notes, and an instruction inside a
note. The two source systems are SQLite mocks; do not claim live integrations.

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
