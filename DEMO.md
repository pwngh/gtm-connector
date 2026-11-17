# Five-minute demo

From a clean checkout, run:

```sh
./scripts/five-minute-demo.sh
```

Use macOS or Linux with Python 3.11+, SQLite, and venv support. The first
dependency installation needs internet. Download time varies and sits outside
the five-minute walkthrough. The runtime needs no external services, account,
model, or API key. The script runs the tests, connector demo, evaluation, and
usage report. A failed check stops it.

The script prints its private output directory. Set `demo_state` to that
directory's `state` subdirectory. Replace this placeholder with the actual path:

```sh
demo_state='/replace/with/printed/output/directory/state'
```

## 0:00 to 1:00: show the boundary

Open [README.md](README.md). Point to the five tools and their independent
`crm:read`, `crm:write`, and `calls:read` scopes. One MCP connector fronts two
SQLite source stores. The local authorization server uses S256 PKCE.

Read the connector results. There are 30 accounts. The CRM token receives
HTTP 403 when it requests call notes. `acc-004` has an empty notes result, which
differs from a permission denial.

## 1:00 to 2:00: show controlled writes and receipts

Show two `log_activity` requests producing one activity. Reusing the key with
a different payload is an error. The rate-limit example returns HTTP 429.

There are 14 connector receipts and nine briefing receipts. Each attempt first
commits a pending row, then finalizes the same row. Explain the HTTP 503 recovery
case: audit finalization can fail after an activity commits, so the caller must
retry the same key. Inspect the audit failure and idempotency tests.

## 2:00 to 3:00: read a brief

```sh
.venv/bin/python -m gtm_connector brief acc-003 --state-dir "$demo_state"
.venv/bin/python -m gtm_connector brief acc-001 --without-calls --state-dir "$demo_state"
```

The first brief preserves `opp-003` reporting `closed_won` and `note-003`
reporting unapproved procurement. Its conflict flag cites both records. The
rule recognizes this exact fixture pattern; it is not a general contradiction
detector. The second brief says call notes were unavailable and cites the
denial receipt.

Point to `crm/...` and `calls/call_notes/...` references. The injected note for
`acc-007` remains quoted data and creates zero activities. The draft cannot
choose tools or request more scopes. The checker rejects an appended uncited
claim before publication.

## 3:00 to 4:00: inspect evidence and measurements

Copy the actual `Brief:` ID printed above. Replace `brief-REPLACE_WITH_ID` in
these commands with that value:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID --state-dir "$demo_state"
.venv/bin/python -m gtm_connector usage --brief brief-REPLACE_WITH_ID --state-dir "$demo_state"
```

The audit lists the three planned reads. Usage groups attempts by token, tool,
and UTC day and reports model tokens for the saved brief. The fixture uses zero
model tokens. Unknown usage must not become zero. Counts describe grants and
attempts, not people or bills.

Read the evaluation result from the script. To reproduce it separately:

```sh
.venv/bin/python -m gtm_connector evaluate
```

The evaluator uses 20 accounts in fresh disposable state. It compares briefs
with checked-in expected claims and citations. Show facts found, facts cited
correctly, and facts invented. The unit is a supported claim line. These scores
test fixture coverage and citation handling, not model reasoning quality.

## 4:00 to 5:00: state the limits

Open [SECURITY_REVIEW.md](SECURITY_REVIEW.md). Local file ownership is the trust
boundary. The owner credential can request every scope. Audit arguments and
evidence snapshots persist until the operator removes the state directory.
Snapshot files are not tamper-proof or crash-safe. Custom Python adapters are
not sandboxed. No real CRM integration, web interface, or multi-tenant behavior
is included.

Show the measured scores in the README and the passing test result. Leave the
pull request open for the reviewer to inspect and merge.
