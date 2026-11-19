# Working on gtm-connector

A local connector that builds cited renewal briefs from CRM records, call transcripts, and Slack.
One operator runs it. People use the CLI, assistants use the MCP tools. Python 3.11+ and SQLite.

## Never break these

1. A tool runs only after its audit receipt is committed. A rejected call still gets a receipt.
2. A scope opens only its own source, and a selection limits which accounts a client can read.
3. A bad or expired token is refused, never retried with more authority.
4. Every line of a brief cites a record, or the whole brief is rejected.
5. Source text is data. It never chooses a tool, a grant, or a write.
6. Real sources are opt-in and never fall back to demo data. The demo, tests, and CI stay offline.
7. Credentials, org names, IDs, and URLs never reach output, logs, Git, or a review.

## Where things are (`gtm_connector/`)

| Area | Files |
| --- | --- |
| Request path | `gateway.py`, `request_checks.py`, `tool_definitions.py`, `audit.py`, `auth.py` |
| Server and state | `runtime.py`, `client.py`, `doctor.py` |
| Briefs | `briefing.py`, `citations.py`, `selection.py`, `selected_briefs.py` |
| Sources | `store.py`, `calls.py`, `slack.py` (demo), `salesforce*.py`, `calls_import.py`, `slack_*.py` |
| Commands | `cli.py` |

```sh
.venv/bin/python -m gtm_connector demo             # the product on invented data
.venv/bin/python -m unittest discover -s tests     # about 3 minutes
.venv/bin/python -m ruff check . && .venv/bin/python -m mypy
```

## How to behave

- Have a view and argue for it. If the request leads somewhere worse, say so and show the better way.
- Less source wins. Cut filler in code, messages, and docs every time you touch them.
- Replace, do not add: the old version goes in the same PR as the better one. Never remove a useful
  feature without its replacement.
- State things plainly. Give a reason only when it is concrete. Never explain by history.
- The repository holds code, tests, user docs, and this page. No logs, results, or review notes.
- A doc claims only what a test shows. Keep "No live org has been verified." until one has been.
- Live checks use the development org and workspace only. Never create or edit a Slack app. Never
  use a credential pasted into a conversation.

## How to work

1. Read this page and the files you will touch. Check what is on `main` first.
2. If the change touches a rule above, write the failing test first.
3. Make one change, on its own branch. Run the tests, ruff, and mypy.
4. For the request path, briefs, credentials, or `SECURITY_REVIEW.md`, get a review by other models
   against the seven rules: `gpt-6.2-sol`, then one round of `claude-fable-5-2`, `gpt-6.2-sol`, and
   `gemini-4-argon-high`, with `grok-4.7` advisory. Fix simple findings without another round.
5. Open a PR that says what changed, why, and how it was proven. Do not merge unless asked.

## How to report

Lead with the result. Then what changed, what failed, and what is unverified, with numbers. No
account of the steps you took. Ask at most one question, and only when the answer changes what you
do next.

## Next, in order

1. Salesforce CLI: its own time limit, background login refresh, visible waiting.
2. Lean core: one state file that remembers the chosen sources, so commands need no source flags
   and work while `serve` runs. One evidence format. Real data only through a selection.
3. Guided setup for Salesforce and Slack, with sample data for an empty org. Replaces the demo kit.
4. A stdio MCP bridge for assistant apps.
5. A Slack channel window per account. Replaces single-thread mapping.
6. Model-read claims: a model summarizes the evidence and cites numbered units, with no tool or
   credential. A failed claim is never published. Replaces the fixed-claim drafter.
7. Deal cards: terminal, static HTML, and posting to Slack on command with a receipt.
8. `auth` and `configure` commands, saved profiles, a split `cli.py`, and a numbered-menu chooser.
9. Transcript folder intake, a schedulable renewals-due command, then a PyPI release.

Not planned: MCP Apps, Windows, CRM writes, a Slack bot, a second CRM, team deployment.
