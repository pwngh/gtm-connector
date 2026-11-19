# Project instructions

A local connector for one technical operator, such as a RevOps engineer,
founder, or sales engineer. It builds cited renewal briefs from CRM records,
call transcripts, and Slack. The owner wants a product a person can use. Keep
the implementation plain. Use Python and SQLite. Do not add a web interface.

## Standing rules

- Use no employer or client code, data, names, or documents. Fixtures are
  invented and use reserved `.example` addresses.
- The default demo, evaluation, and CI run offline, with no model and no key.
  A real source is used only when it is selected explicitly, and a failure
  never falls back to fixtures silently.
- Keep authentication, source selection, and daily use separate. Label sources
  as fixtures or selected real data.
- Back every README and SECURITY_REVIEW.md claim with a test. Keep the sentence
  "No live org has been verified." until the owner supplies a check report.
- The repository holds code, tests, and the docs a user needs. Do not commit
  run logs, result files, review notes, dated observations, decision records,
  or project history. Review evidence goes in the PR description. Working
  notes stay outside the repository.
- Finish one reviewed PR per slice. Push only to the task's own branch. Leave
  the PR open for the owner to merge, unless the owner said "full send" for the
  task. Never enable auto-merge, tag, publish, or change repository settings.
- Check the tree and history for private material before every push and before
  sending source to a review provider. Keep org names, IDs, emails, URLs,
  credentials, and `var/` out of Git, reviews, and reports.
- Before a task, read this file, then the modules and docs the task touches and
  their callers and tests.
- The server implements MCP and authorization revision 2026-07-28. Read the
  current specification before changing the protocol, and keep the revision in
  the README.
- Keep fixture IDs stable. Keep the fixture cases the tests rely on: a renewal
  within 30 days of the fixed date, an at-risk account, conflicting CRM and
  call-note facts, an account without notes, an instruction inside a note, a
  missing contact role, and a dated old note. `demo` follows one rep and one
  account. `verify` keeps the separate guarantee checks and their JSON shape.

## Live checks in development

The owner allowed agents to run `salesforce check`, `slack check`, and briefs
against the owner's Developer Edition org and development Slack workspace.
Never touch production or client data. Print statuses and counts only, and keep
reports private. An agent never creates or changes a Slack app, and never uses
a credential pasted into a conversation. End users keep the product's rules:
they run checks themselves, and the connector signs no one in or out silently.

## Build order

Judge every item by one test: does it shorten the path from install to a brief
someone would use before a real renewal call? Selected sources and caller-run
briefs are done. Finish these in order:

1. Salesforce CLI timing. Give `sf` its own longer time limit outside each
   read's budget, refresh a login in the background, show that it is waiting,
   and report a timeout as its own error.
2. Guided setup. The CLI checks each prerequisite and signs in to Salesforce and
   Slack. For Slack it opens a prefilled create-app link, uses a fixed set of
   callback ports, and takes the client ID once. Status separates "login saved"
   from "read check passed", and each guided step prints its plain command.
   Flags and `--json` never prompt.
3. The local stdio MCP bridge. Every request goes through the gateway. The
   bridge holds one grant for the scopes named at launch, in memory only, and
   renews it for the same scopes or fewer. `connect --transport stdio` merges
   only the connector's entry into the client configuration, keeps the file's
   mode, saves a backup first, and refuses a symlink or a file that changed
   after it was read. The file never holds a token.
4. A Slack channel window. The operator pins one channel per account. The read
   keeps the shared budget of attempts, bytes, and one deadline, and adds hard
   caps on days, messages, and replies. A window that hit a cap is incomplete,
   and the brief says so. Check Slack's current rate-limit policy for the
   operator's app before choosing caps.
5. Model-read claims, under the rules below. Before calling it ready, run an
   extraction test on invented messy transcripts, then have the owner run it on
   real ones. An agent never reads a real transcript.
6. Deal cards as terminal text and a local static HTML preview, with source
   text escaped and no script or remote resource. The owner also wants a card
   posted to Slack as the operator. Posting happens only on an explicit
   command, needs the `chat:write` user scope that the operator turned on, and
   commits a receipt before it sends.
7. Explicit `auth` and `configure` commands and saved daily profiles. Split
   `cli.py` here.
8. The default chooser as a numbered menu from the standard library, with
   corrections for misplaced arguments. Add a macOS CI runner.
9. Transcript folder intake: one subfolder per selected account, imported on
   command, associated through the audited CRM read, never chosen by
   modification time.
10. A renewals-due command the operator schedules. It reuses or silently renews
    an existing grant for the same scopes or fewer and never starts the owner
    flow. It fails an account with a receipt and a clear error instead of
    prompting or substituting fixtures.
11. A PyPI release. The owner publishes it.

Do not start these without the owner's request: MCP Apps, native Windows
support, CRM writes, a Slack bot, calendar triggers, provider transcript
adapters, a second CRM, or a team deployment (Postgres, single sign-on,
per-user tokens, a container, a hash-chained audit log, redaction).

## Rules for model-read claims

- The operator selects the model. Local Ollama needs no key. A hosted endpoint
  needs the operator's own key from an environment variable or a private file
  with mode 0600, never a flag. The key never enters a selection, profile,
  brief, receipt, log, error, or tool result.
- A hosted request uses HTTPS with verified TLS, no redirects, no environment
  proxy, a time limit, and a response size limit. Selecting a hosted endpoint
  sends source text off the machine: say so, and show the host and model.
- Commit a pending record before a model call and send nothing if that fails.
  Finish it with the outcome and usage. Never record the key, the prompt, or
  rejected text.
- The model receives only evidence that passed the selection and the caller's
  scope checks, and no credential or tool. A claim returns only to a caller
  that holds every scope its input required.
- The model cites numbered units, and the connector prints the saved text for
  the cited range. Accept no retyped quote by approximate matching. Numbers and
  dates in a claim must appear in its cited units. Never truncate evidence
  silently.
- Label model-read claims as model-proposed and show the cited text beside
  each. Source text and accepted claims stay data. They never select a tool, a
  grant, or a write. Never publish a claim that failed the check.
- Keep the 182-claim and 223-claim fixed suites unchanged as the offline
  baseline, and add a separate track for model-read claims.

## Reviews

Choose the tier from what the diff touches and state it in the PR description.

- **Sol loop only.** Anything outside the next bullet. Run the `sol-loop` skill
  with GPT Sol at High.
- **Council.** A change to `gateway.py`, `auth.py`, `audit.py`, `citations.py`,
  `store.py`, `request_checks.py`, `tool_definitions.py`, `refusals.py`,
  `runtime.py`, `briefing.py`, `client.py`, credential or token handling,
  source selection or read bounds, a new outside connection, a model call,
  this file, SECURITY_REVIEW.md, or the README's guarantees.

| Member | Model ID | Effort | Gate |
| --- | --- | --- | --- |
| Claude Fable 5.2, `claude` CLI | `claude-fable-5-2` | High | Blocking |
| GPT Sol, `codex` CLI | `gpt-6.2-sol` | High | Blocking |
| Gemini 4 Argon, `agy` CLI | `gemini-4-argon-high` | High | Blocking |
| Grok 4.7, `grok` CLI | `grok-4.7` | Low with the diff inline; High with tools for a diff of 150,000 bytes or more | Non-blocking |

Run a Sol pass first, then one council round. Fix simple findings and go on
without another round. Run another round only for a redesign or a blocker
whose fix is not obvious, and never more than three. Each review is a separate
run on its own exported copy of the commit, read-only, with the same prompt and
proof. The orchestrating session is not a member. If a blocking member cannot
be reached, stop and report. Never substitute a model or count a failed run as
approval. In the PR description, give each member's model ID, effort, verdict,
and score, the findings and what was done about them, and say when a fix was
not re-reviewed. Keep it short.

Ask every member:

- Can any tool return data outside the token's scopes?
- Can a tool call miss the audit log, including rejected calls and failures?
- What happens with an expired, malformed, or wrong-audience token?
- Is anything inside a source record ever treated as an instruction?
- Can a race, lock, timeout, or cancellation break one of the guarantees?
- Does the diff do anything its stated scope and contracts do not include?

Before coding a council-tier change, write a failing test for each of the three
most likely ways it could break a guarantee. After editing a council module,
run the tests behind the README's guarantees:

```sh
.venv/bin/python -m unittest tests.test_audit tests.test_citations tests.test_gateway tests.test_slack_gateway tests.test_briefing
```
