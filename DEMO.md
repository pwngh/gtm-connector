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

Open [README.md](README.md), then the [tools and their scopes](docs/tools.md).
Point to the independent `crm:read`, `crm:write`, and `calls:read` grants. The
default brief uses two of the connector's three SQLite sources. The third store
exposes mock Slack messages under `slack:read`. The local authorization server uses S256 PKCE.

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
model tokens. Unknown usage must not become zero. Token groups include rejected
credentials and `anonymous` for missing credentials. Counts describe attempts,
not people or bills.

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
not sandboxed. The default walkthrough uses no real CRM integration. The optional
Salesforce reader requires a separate Developer Edition org and refuses briefs.
No web interface or multi-tenant behavior is included.

Show the measured scores in the README and the passing test result.

## Optional: show the official Inspector

Follow [the Inspector setup](docs/inspector.md) in two terminals. It creates
`var/inspector/inspector.json` with a CRM-only grant. Then run:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/inspector/tls.pem" npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/list
```

Show the six tools and their scope metadata. The guide then reads `acc-003`,
denies a call-note request, and repeats it after an explicit `calls:read` grant.
Inspector uses `server/discover` and the same HTTPS gateway. No authorization or
audit check is bypassed. This optional client requires Node and a first npm
download. The default five-minute script remains independent of it.

## Optional: include account-channel messages

```sh
.venv/bin/python -m gtm_connector demo --with-slack --state-dir var/slack-demo
.venv/bin/python -m gtm_connector brief acc-003 --with-slack --json --state-dir var/slack-demo
.venv/bin/python -m gtm_connector evaluate --with-slack
```

Show `slack/messages/msg-003` alongside the opportunity and call note. The
three-source flag cites all three records. `proposed_follow_up` contains a
checked proposal to confirm the conflicting status. Nothing sends it.

The extended demo produces 18 connector receipts and 12 briefing receipts.
It shows three more scope denials, both injected records creating no activity,
and a restricted brief that reports both missing grants. The default script
retains its 23 receipts and 182-claim score. See [Slack context](docs/slack.md).

## Optional: use a local model

With Ollama 0.30.0 or newer running and `llama3.2:latest` already cached:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --adapter ollama --model llama3.2:latest --json
```

Inspect `adapter`, `model_usage`, `draft_diagnostics`, and the checked `text`.
The model receives the supported claim catalog and no tool interface. Its
response must pass the same checker. A rejected draft prints no candidate and
exits `2`; saved usage still counts that generation. Nothing downloads a model.
The default walkthrough stays model-free. See [model results and limits](docs/ollama.md).

## Optional: read a Developer Edition org

Follow [the Salesforce setup](docs/salesforce.md). This is separate from the
five-minute offline walkthrough. Select the backend explicitly, then use the
same scoped Inspector client to search accounts and read their opportunities.
The three CRM tools use external GET requests. Call notes and Slack stay local.
`log_activity` is absent and hardcoded write attempts return HTTP 403. Renewal
briefs stop with `renewal_fields_unmapped`. No live org result is claimed here.
