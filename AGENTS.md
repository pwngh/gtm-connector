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
python3 -m venv .venv && .venv/bin/python -m pip install --require-hashes -r requirements-dev.lock
.venv/bin/python -m gtm_connector demo             # the product on invented data
.venv/bin/python -m unittest discover -s tests     # about 3 minutes; tests_stateful is separate
.venv/bin/python -m ruff check . && .venv/bin/python -m mypy
```

## How to work

1. State the PR as "after this, a user can ...". If you cannot, it is a cleanup and must shrink the
   source. Report source lines before and after.
2. Replace, do not add: the old way goes in the same PR as the better one. Never remove a useful
   feature without its replacement.
3. If the change touches a rule above, write the failing test first. Test through the CLI and the
   gateway, not internals.
4. Build it, run it as a user, then clean it before review: no duplicate helpers, leftover flags,
   or filler in code, messages, and docs. Run the tests, ruff, and mypy.
5. For the request path, briefs, credentials, or `SECURITY_REVIEW.md`, get a review by other models
   against the seven rules: `gpt-6.2-sol`, then one round of `claude-fable-5-2`, `gpt-6.2-sol`, and
   `gemini-4-argon-high`, with `grok-4.7` advisory. Fix simple findings without another round.
6. Open a PR that says what changed, why, and how it was proven. Do not merge unless asked.

- Have a view and argue for it. If a request leads somewhere worse, say so and show the better way.
- State things plainly. Give a reason only when it is concrete. Never explain by history.
- The repository holds code, tests, user docs, and this page. No logs, results, or review notes.
- A doc claims only what a test shows. Keep "No live org has been verified." until one has been.
- Live checks use the development org and workspace only. Never create or edit a Slack app. Never
  use a credential pasted into a conversation.
- Report the result first, then what changed, what failed, and what is unverified, with numbers.

## Next, in order

1. Salesforce CLI timing, then the stdio bridge so assistant apps can connect.
2. Set up once: saved sources so commands need no source flags, commands that work while `serve`
   runs, guided `setup` for Salesforce and Slack with sample data (replaces the demo kit), renewal
   dates from a Contract or a renewal Opportunity, and `renewals` to list and select accounts.
3. A brief worth reading: numbered evidence and a check tool so an assistant writes the summary and
   the connector verifies it, a Slack channel window (replaces thread mapping), transcript folders
   (replace hand-picked excerpts), then built-in drafting with a model the operator selects.
4. Where people work: a Deal card in the terminal and HTML, posting to Slack on command with a
   receipt, and scheduled briefs for renewals that are due.
5. Release: a menu chooser, a split `cli.py`, macOS CI, and PyPI.

After each of these, use the product end to end as a stranger and fix what only shows from there.
Later, when a real user needs them: Salesforce task creation, transcript provider adapters,
Windows, MCP Apps, a second CRM, team deployment.
