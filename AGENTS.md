# Working on gtm-connector

A local connector that builds a Deal card for a renewal from CRM records, call transcripts, and
Slack. One operator runs it. People use the CLI and the web app, assistants use the MCP tools. It is
one Go program on PostgreSQL, with the web app inside it, and it is growing to run the renewal
workflow. There is no Python in the repository: `testdata/oracle/` holds the outputs the Python
package it replaced recorded, now Go's golden data.

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

## Where things are

The module is at the root, with its packages in `internal/`: `unicodetab` (generated from `testdata/oracle/unicode`),
`strictjson`, `evidence` (neutralized text, sentences, the card), `receipts` (the contract, the publication
rules every store applies, and `receiptstest`, which every store runs), `cards` (make, seal, verify, admit),
`sources/demo`, `sources/calls` (the transcript parser, the excerpt and the record of an import, kept by
`postgres.CallImports`) and `sources/salesforce` (the `sf` login and the REST reads, with an invented org in
`salesforcetest`). Its tests check it against `testdata/oracle/`. `transport/mcp` is the MCP gate.
`sources/slack` reads one mapped Slack thread and runs the Slack sign-in; `debug slack`,
`connect slack` and a card's read use it.
`people` resolves a speaker to a person and a side by the strongest identifier a source carries; its
package comment states the ladder and the rules, and nothing calls it yet.
`storage/postgres` holds the migrations and every query (its triggers enforce the receipt and card
rules for any writer), `authz` the grant checks on evidence reads, `transport/http` the browser
surface, and `cmd/gtm-connector` `setup`, `serve`, `demo`, `backup` and `restore`, with `connect` and the
read commands `accounts`, `brief`, `inspect` and `audit` on demo data and Salesforce, `admit` for the
cards a Python state folder holds, `debug salesforce
check`, `debug slack`, `token` for MCP client tokens and `mcp stdio`. Its `sources.go` is the one place a
source is wired into those commands. `postgres.ReceiptStore` runs the card maker on PostgreSQL.
`mcptools` holds the MCP gate's tokens, receipts and tools on the store, which `serve` mounts at `/mcp`
and `mcp stdio` serves on stdin and stdout.
Tests that need PostgreSQL skip without `GTM_TEST_DATABASE_URL`; `make test` sets it.
`realdata` (with `cmd/realdata`) fetches public datasets and compares Go's text functions on them with
the digests in `internal/realdata/recorded`. `web/` is the web app, which `make build` puts inside the
binary.

The guides in `docs/` describe the binary: `cli.md` (commands, workspaces, output, exit codes),
`cards.md` (how a card is built and read), `audit.md` (receipts), `sources.md` (Salesforce, call
transcripts, Slack, the demo book), `mcp.md` (tools, scopes, tokens, selections), `operations.md`
(install, setup, health, backup), `api.md` and `real-data.md`.

```sh
GOTOOLCHAIN=local go vet ./... && GOTOOLCHAIN=local go test ./...   # in seconds; PostgreSQL tests skip
make test                                          # the same, with PostgreSQL in Compose
cd web && npm ci && npm test && npm run test:browser   # the web app (Node is pinned in .nvmrc)
make build                                         # build/gtm-connector, with the web app inside it
make demo                                          # the product on invented data, on PostgreSQL in Compose
make release                                       # the four release binaries and SHA256SUMS
make verify-real                                   # real public text against recorded digests; downloads, never in CI
```

## How to work

1. State the PR as "after this, a user can ...". If you cannot, it is a cleanup and must shrink the
   source. Report source lines before and after.
2. Replace, do not add: the old way goes in the same PR as the better one. Never remove a useful
   feature without its replacement.
3. If the change touches a rule above, write the failing test first. Test through the CLI and the
   gateway, not internals.
4. Build it, run it as a user, then clean it before review: no duplicate helpers, leftover flags,
   or filler in code, messages, and docs. Run `go vet`, the Go tests and the web tests.
5. For the request path, briefs, credentials, or `SECURITY_REVIEW.md`, get one review round by other
   models against the seven rules: `claude-fable-5-2` and `gpt-6.2-sol`, with `grok-4.7` advisory.
   Fix the findings without another round.
6. Open a PR that says what changed, why, and how it was proven. Do not merge unless asked.

- Have a view and argue for it. If a request leads somewhere worse, say so and show the better way.
- State things plainly. Give a reason only when it is concrete. Never explain by history.
- The repository holds code, tests, user docs, and this page. No logs, results, or review notes.
- The README states the product as designed. `docs/ROADMAP.md` lists every part of it that is not
  built yet. Every other doc claims only what a test shows, and the roadmap's "Checks the docs still
  need" lists each guide statement no test asserts yet. Keep "No live org has been verified." on the
  roadmap until one has been.
- `testdata/oracle/cards/cases.jsonl.gz` holds the cards of ten invented deals, one property each,
  and `internal/cards/parity_test.go` builds them byte for byte. A change to cards keeps them
  passing.
- Live checks use the development org and workspace only. Never create or edit a Slack app. Never
  use a credential pasted into a conversation.
- Report the result first, then what changed, what failed, and what is unverified, with numbers.

## Next, in order

The goal is the renewal workflow: find what blocks a renewal in the evidence, turn it into reviewed
work, and keep the history of what resolved it. No model writes text on a card.

[docs/ROADMAP.md](docs/ROADMAP.md) lists the steps. v0.1.0 comes first: the Go program has replaced
the Python package and stays read-only. To finish it:

1. Keep the reference. `testdata/oracle/` holds the outputs the Python package recorded on CPython
   3.14.6 with clocks and IDs fixed, and a fixture that disagrees with the code is the reference.
   Card text matches byte for byte, and units and gaps match as values. A difference with a cause
   outside the product, such as the recorder's clock, is listed in its test with the reason.
   Character classes come from `internal/unicodetab`, never Go's or RE2's. A deliberate change to
   the card or text rules rewrites the golden files it changes with `go test -p 1 -v ./internal/cards
   ./internal/evidence ./internal/realdata ./internal/render/... ./cmd/gtm-connector -update` (never
   with `-run`; it prints what changed in each card family, and refuses a card that fails its own seal
   checks), with `GTM_TEST_DATABASE_URL` set for the command replay, and the real-data digests of the
   changed functions with `go run ./cmd/realdata -update=FUNCTION,... verify`. Its PR lists each
   rewritten file with how many cards, units or rows changed, and why.
2. Finish the Evidence row of the roadmap: neutralize a bare mail address and show the host of a
   valid `xn--` label.
3. Close the cutover: a tagged release attaches the binaries.

Sign-in, grants, selection, expiry and the Host check stay ahead of any evidence served. MCP passes
the recorded gateway vectors on both transports, HTTP and stdio.

Planned work is listed in `docs/ROADMAP.md`. A statement leaves it with the test that shows it.

After each step, use the product end to end as a stranger and fix what only shows from there.

Test on real data at every step. Public datasets are fetched into a cache outside the repository,
checked against a committed manifest of links and digests, and run with `make verify-real`, never
in CI. Real text goes through Go and must match the digests in `internal/realdata/recorded`. Reports hold counts, digests and positions, never text. A public corpus tests
parsing and scale, not usefulness: the README's first limit stays until the owner has run the
connector on five real renewals outside the repository. Agents never read those transcripts or
results; each parser failure they report becomes an invented fixture.
