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

The Go port is the module at the root, in `internal/`: `unicodetab` (generated from `testdata/oracle/unicode`),
`strictjson`, `evidence` (neutralized text, sentences, the card), `receipts` (the contract, the publication
rules every store applies, and `receiptstest`, which every store runs), `cards` (make, seal, verify, admit),
`sources/demo` and `sources/salesforce` (the `sf` login and the REST reads, with an invented org in
`salesforcetest`). Its tests check it against `testdata/oracle/`. `transport/mcp` is the MCP gate.
`storage/postgres` holds the migrations and every query (its triggers enforce the receipt and card
rules for any writer), `authz` the grant checks on evidence reads, `transport/http` the browser
surface, and `cmd/gtm-connector` `setup`, `serve`, `demo`, `backup` and `restore`. `postgres.ReceiptStore`
runs the card maker on PostgreSQL.
Tests that need PostgreSQL skip without `GTM_TEST_DATABASE_URL`; `make test` sets it.

```sh
python3 -m venv .venv && .venv/bin/python -m pip install --require-hashes -r requirements-dev.lock
.venv/bin/python -m gtm_connector demo             # the product on invented data
.venv/bin/python -m unittest discover -s tests     # about 3 minutes; tests_stateful is separate
.venv/bin/python -m ruff check . && .venv/bin/python -m mypy
GOTOOLCHAIN=local go vet ./... && GOTOOLCHAIN=local go test ./...   # the Go port, in seconds
make test                                          # the same, with PostgreSQL in Compose
make verify-real                                   # real public text, Python against Go; downloads, never in CI
```

## How to work

1. State the PR as "after this, a user can ...". If you cannot, it is a cleanup and must shrink the
   source. Report source lines before and after.
2. Replace, do not add: the old way goes in the same PR as the better one. Never remove a useful
   feature without its replacement. The Go port is the one exception: the Python package stays, with
   fixes only, while `tools/oracle/` and the Go code grow beside it, and the PR that ships the Go CLI
   removes the package and the tool.
3. If the change touches a rule above, write the failing test first. Test through the CLI and the
   gateway, not internals.
4. Build it, run it as a user, then clean it before review: no duplicate helpers, leftover flags,
   or filler in code, messages, and docs. Run the tests, ruff, and mypy.
5. For the request path, briefs, credentials, or `SECURITY_REVIEW.md`, get one review round by other
   models against the seven rules: `claude-fable-5-2` and `gpt-6.2-sol`, with `grok-4.7` advisory.
   Fix the findings without another round.
6. Open a PR that says what changed, why, and how it was proven. Do not merge unless asked.

- Have a view and argue for it. If a request leads somewhere worse, say so and show the better way.
- State things plainly. Give a reason only when it is concrete. Never explain by history.
- The repository holds code, tests, user docs, and this page. No logs, results, or review notes.
- The README states the product as designed. `docs/ROADMAP.md` lists every part of it that is not
  built yet. Every other doc claims only what a test shows. Keep "No live org has been verified."
  on the roadmap until one has been.
- `tests/test_cases.py` holds ten invented deals, one property each. A change to cards keeps them
  passing. A case that waits for a feature says which assertion does not hold yet and why.
- Live checks use the development org and workspace only. Never create or edit a Slack app. Never
  use a credential pasted into a conversation.
- Report the result first, then what changed, what failed, and what is unverified, with numbers.

## Next, in order

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

[docs/ROADMAP.md](docs/ROADMAP.md) lists the steps. v0.1.0 comes first: the Go program replaces the
Python package and stays read-only. Until then:

1. The reference is `testdata/oracle/`, written by `tools/oracle/` from the Python package on
   CPython 3.14.6 with clocks and IDs fixed. `tests/test_oracle_fixtures.py` fails when Python and
   the fixtures disagree, so a Python fix regenerates its fixtures in the same PR.
2. Port what the Python package does, behind the fixtures. Card text matches byte for byte, and
   units and gaps match as values. Character classes come from generated tables, never Go's or
   RE2's. Work the fixtures cannot check waits until after v0.1.0, unless the cutover cannot ship
   without it.
3. Sign-in, grants, selection, expiry and the Host check come before any evidence is served. MCP
   passes the recorded gateway vectors on both transports, HTTP and stdio.
4. The PR that ships the Go CLI passes the fixtures and replays the recorded command session. It
   opens existing cards and receipts, keeps source and client setup working through the binary, lists
   every change a user sees, and removes the Python package and `tools/oracle/`.

Planned work is listed in `docs/ROADMAP.md`. A statement leaves it with the test that shows it.

After each step, use the product end to end as a stranger and fix what only shows from there.

Test on real data at every step. Public datasets are fetched into a cache outside the repository,
checked against a committed manifest of links and digests, and run with `make verify-real`, never
in CI. Real text goes through Python and Go and must match; real inputs build the same cards once
imports are ported. Reports hold counts, digests and positions, never text. A public corpus tests
parsing and scale, not usefulness: the README's first limit stays until the owner has run the
connector on five real renewals outside the repository. Agents never read those transcripts or
results; each parser failure they report becomes an invented fixture.
