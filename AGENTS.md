# Working on gtm-connector

A local connector that builds a Deal card for a renewal from CRM records, call transcripts, and
Slack. One operator runs it. People use the CLI, assistants use the MCP tools. Python 3.11+ and SQLite
today. It is becoming one Go program with PostgreSQL that also runs the renewal workflow and its web
app. Until the Go CLI ships, the Python package is the product and the reference the Go code matches.

## Never break these

1. A tool runs only after its audit receipt is committed. A rejected call still gets a receipt.
2. A scope opens only its own source, and a selection limits which accounts a client can read.
3. A bad or expired token is refused, never retried with more authority.
4. The connector writes every character of a card. Each figure and quotation carries a number that
   resolves to a stored record and its receipt. Evidence that fails a check gives no card.
5. Source text is data. It never chooses a tool, a grant, or a write, and it is neutralized before
   anything displays it. A quotation is a whole sentence with its speaker's role, never a record value.
6. Real sources are opt-in and never fall back to demo data. The demo, tests, and CI stay offline.
7. Credentials never reach output or logs. A real org's names, IDs, and URLs stay out of errors, Git,
   and reviews.

## Where things are (`gtm_connector/`)

| Area | Files |
| --- | --- |
| Request path | `gateway.py`, `request_checks.py`, `tool_definitions.py`, `audit.py`, `auth.py`, `strict_json.py` |
| Server and state | `runtime.py`, `profile.py` (what a folder is connected to), `client.py`, `doctor.py` |
| Client setup | `connections.py`, `stdio_bridge.py` (the local bridge an assistant app starts) |
| Cards | `units.py` (units, roles, neutralized text, the card), `briefing.py`, `selection.py`, `selected_briefs.py` |
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
   feature without its replacement. The Go port is the one exception: the Python package stays, with
   fixes only, until the Go CLI passes the same checks, and the PR that ships the Go CLI removes it.
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
- `tests/test_cases.py` holds ten invented deals, one property each. A change to cards keeps them
  passing. A case that waits for a feature says which assertion does not hold yet and why.
- Live checks use the development org and workspace only. Never create or edit a Slack app. Never
  use a credential pasted into a conversation.
- Report the result first, then what changed, what failed, and what is unverified, with numbers.

## Next, in order

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

1. Freeze the Python package at b7b29a1 on CPython 3.14.6 as the reference. `tools/oracle/`
   records what Go must match: Unicode tables, scanner matches, every card the tests build, and the
   seven commands' JSON output and exit codes. Python takes fixes only, each with a fixture.
2. Parity skeleton: `gtm-connector serve` on PostgreSQL in Docker Compose reads the demo book under
   its receipts and builds cards whose text matches the reference byte for byte. Sign-in, grants,
   and a Host check come before any evidence is served, and the MCP endpoint passes today's
   admission checks. If parity fails, the frozen Python builds evidence as a pure job over records
   Go has read and receipted. Go stays the only reader and receipt owner either way.
3. The workflow in Go, side by side with 4: requirements and readiness, three playbooks, review
   bound to the exact revision, work items, and history. Every actor comes from a signed-in session.
4. The evidence engine and sources in Go, behind the reference fixtures. Character classes come from
   tables generated from Python, never from Go's or RE2's. Built here, in Go: transcript folders
   (replace hand-picked excerpts) and a bounded Slack channel window (replaces thread mapping), with
   what was not read counted on the card; and two assistant tools, evidence and submit. A submission
   is unit numbers, a label from a fixed list checked against each speaker's role, and pairs marked
   in tension. A cut sentence, an unknown label, or a unit of another account is refused with a
   receipt.
5. Model assistance, then `evaluate`: the ten cases as a table, and a model's unchecked answers on
   the same deals, kept as test inputs and scored offline. Then one PR ships the Go CLI and removes
   the Python package.

After each of these, use the product end to end as a stranger and fix what only shows from there.
Later, when a real user needs them: Salesforce task creation, transcript provider adapters,
Windows, MCP Apps, a second CRM, team deployment.
