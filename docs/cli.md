# Use the command line

Run `gtm-connector` for a short starting point. Run `gtm-connector COMMAND --help`
for each command's description, options, and example. `python -m gtm_connector`
accepts the same arguments. `gtm-connector --version` prints the installed version.

Start with invented data:

```sh
gtm-connector demo
gtm-connector brief acc-007 --with-slack
```

The CLI uses readable text by default. Terminal tables have headings; pipes have
complete tab-separated rows without headings or color. `NO_COLOR` disables color
on terminals too. Source control characters are displayed as escapes. Brief
source text already uses quoted, escaped literals. Data goes to stdout. Errors,
server startup messages, and shutdown notices go to stderr.

Every command accepts `--json`, before or after its name. Use it when parsing
output. JSON results use the command-specific shapes below. `verify` retains
the former demo's guarantee-check result; `demo` now returns the renewal story.
Update scripts that consumed the former demo fields to call `verify`. Keys
may be added; their existing meaning and type stay stable within version 0.1.
Human text is intended for reading and can change independently.

| Command | JSON result on stdout |
| --- | --- |
| No command | Object with `description` (string), `try` (string array), and `help` (string). |
| `--version` | Object with `version` (string). |
| `seed` | Object with `counts`, an object of integer counts by table. |
| `accounts` | Array of account objects, or `[]`. |
| `account` | Account object with a `contacts` array. SQLite includes fixture renewal fields; Salesforce includes `contracts`, `backend`, and `source_id` as defined in its [field mapping](salesforce.md#field-mapping). |
| `brief` | Saved artifact with `schema_version: 1`, identifiers, `as_of`, `status`, `evidence`, `text`, and `model_usage`. Slack mode adds `proposed_follow_up`. |
| `check-brief` | Object with `status: "valid"` and `brief_id`. |
| `audit` | Array of receipts, ordered as stored. With `--follow`, each changed receipt is one JSON line. Fields and filters are documented in [audit](audit.md). |
| `usage` | Object with `brief_id`, `day_basis`, `calls`, `by_token`, `by_tool`, `by_day`, `by_token_tool_day`, `briefs`, and `model_tokens`. Active receipt filters add `filters`. With `--follow`, each changed report is one JSON line. See [usage](usage.md). |
| `evaluate` | Object with `status`, account and claim counts, per-account `cases`, receipt counts, and model usage. See [evaluation](evaluation.md). |
| `demo` | Narrative result with `schema_version: 1`, `status`, `workflow`, `data_mode`, source labels, rep, account, lookup, denied read, checked brief, injection outcome, audit counts, and `activities_created`. See [the renewal demo](#renewal-demo). |
| `verify` | Object containing the connector checks, `briefs`, receipt counts, and `injection_brief`. `--with-slack` adds `slack` and `workflow`, retaining the former demo shape. See [guarantee checks](tools.md#run-the-guarantee-checks). |
| `serve` | A `ready` JSON line, followed by one `tool_call` JSON line per observed tool attempt. See [server output](#server-output). |
| `inspector-config` | Object with `config`, `certificate`, and `scopes`. Credentials stay in the private file. |
| `connect` | Object with `app`, `status`, `setup`, `limits`, and `documentation`. Creating a grant adds `config`, the private file's path. See [client setup](clients.md). |
| `doctor` | Object with `status` and a `checks` array. Each check has `name`, `status`, `message`, and `fix`. |
| `completion` | Object with `shell` and `script`. Without `--json`, stdout contains only the shell script. |
| `salesforce check` | Sanitized object with `schema_version: 1`, `status`, `as_of`, `steps`, request counts, and count definitions. [Salesforce checks](salesforce.md#run-the-shareable-setup-check) defines each field. |
| `slack connect` / `slack status` | Versioned sanitized authentication state, selected scopes, expiry, and verification status. Local status creates nothing. See [Slack authentication](slack-auth.md). |
| `slack disconnect` | Versioned local-deletion outcome and separate requested/confirmed access-token revocation outcome. Unconfirmed explicit revocation returns this result before exit `5`. |
| `slack map` | Versioned `saved` or `unchanged` selection outcome, `thread_access: "unchecked"`, selected-thread count, duration, and fixed next command. It omits private selectors. |
| `slack check` | Versioned sanitized five-step report with counts, timings, fixed reasons, and `crm_access_checked: false` / `calls_access_checked: false`. See [live Slack report fields](slack-live.md#share-the-check-report-keep-source-evidence-private). |
| `calls import` | Versioned import metadata, its selected revision ID, account association, and `created`/`selected`/`reassigned` outcomes. It contains no transcript text. |
| `calls list` | Versioned object with an `imports` array and nullable `next_after` cursor. Rows identify historical revisions and the active selection. |
| `calls inspect` | Versioned selected import metadata and excerpt units. Explicit `--full` also returns all units and exact decoded `original_text`. |

Salesforce account inspection keeps the CLI array/object shape. Its row `id`
values are namespaced; `source_id` is the native provider ID. The CLI projection
omits the MCP envelope and `record_ref`; briefs retain full cited evidence.

Help remains plain help text, even with `--json`. JSON errors are one object on
stderr, with an empty stdout unless the command has already produced results:

```json
{"error":{"code":"usage_error","message":"account not found: acc-missing","next":"gtm-connector accounts --db 'var/v2/crm.sqlite3'"}}
```

Treat `error.code` and the process exit as the machine contract. Read `message`
for the cause and `next` for a suggested command. A failed evaluation, Salesforce, Slack, or doctor
check still prints its complete report on stdout before reporting failure on
stderr. Evaluation also writes progress to stderr, so its error object can
follow progress lines. Salesforce checks also report progress on stderr.
See [evaluation progress](evaluation.md#progress) and [Salesforce checks](salesforce.md#run-the-shareable-setup-check).

| Exit | Meaning | JSON error code |
| --- | --- | --- |
| 0 | The command succeeded, or a running server stopped cleanly. | None. |
| 1 | A local file, database, or runtime operation failed. | `local_failure` |
| 2 | Arguments or local configuration are missing or invalid. | `usage_error` |
| 3 | The requested operation was refused. | `refused` |
| 4 | The evidence checker, demo, verification, evaluation, doctor, Salesforce check, or completed Slack check failed. | `check_failed` |
| 5 | A source service or its transport failed. | `upstream_failure` |
| 130 | Ctrl-C interrupted a command. Completed local writes remain. | `interrupted` |

A command typo suggests the nearest command without running it. A broken pipe
closes output quietly. Help and version build no runtime and import neither
SQLite, the MCP SDK, nor the HTTP client. The CLI contract tests hold that import
boundary without depending on elapsed time.

Use `serve`, `brief`, `accounts`, or `account` with
`--crm-backend salesforce --sf-org ALIAS` to read credentials from an existing
Salesforce CLI login. SQLite stays the default. Salesforce expects Developer
Edition unless `--sf-environment sandbox` or `--sf-environment production` is
explicit. For these four commands, any explicit `--sf-environment` also requires
`--crm-backend salesforce`; it cannot silently select fixtures. A mismatched
environment or unsafe connection-state reuse refuses
with exit `3`. Salesforce account commands use `var/salesforce` by default;
`--state-dir` selects their private state. `--db` remains a SQLite option, and
conflicting selections fail before credentials are loaded.
Salesforce briefs use today's local date unless `--as-of` is supplied; fixture
briefs use `2027-01-15`. A missing eligible Contract refuses with
`renewal_fields_unmapped` and exit `3`. The
[Salesforce guide](salesforce.md) explains mapping, refresh, and private output.

`calls import` reads a user-selected TXT or VTT export into separate private
state. `calls list` and `calls inspect` inspect that saved state without a CRM
connection. Daily `serve` and `brief` use `--calls-backend imported` with the same
explicit `--state-dir`; Salesforce account inspection has the same requirement
in imported mode. Omitting the folder refuses before credentials or state are
opened, preserving the ordinary fixture defaults. SQLite's direct account `--db` reads retain their existing
behavior. [Transcript imports](calls.md) defines ranges, limits, identity,
reassignment, and the distinction between an excerpt and a complete original.

Imported briefs add `sources`, use evidence workflow version 4 with fixture
Slack, and label the default formatter `extractive`. The source names are `demo_fixture` or
`salesforce` for CRM, `imported_transcript` for calls, and `demo_fixture` for
Slack when requested. Import progress goes to stderr; with `--json`, each line
has `progress.command: "calls import"` and a fixed `step`. Stdout remains the
single final import result. Fixed import reasons use the existing exit categories.

`slack connect`, `slack status`, and `slack disconnect` manage authentication
separately from source selection. They use `--connection-dir` rather than server
state. The existing top-level `connect` still prints MCP client configuration.
Authentication progress is emitted immediately on stderr. With `--json`, each
progress line has `progress.command`, `step`, and `status`; stdout contains the
single sanitized result. Ctrl-C exits `130` and points to saved connection
status because a completed credential save can survive interruption. The
[Slack guide](slack-auth.md) defines fields, fixed errors and recovery.

`slack map` saves one thread for an explicitly selected account after an audited
CRM lookup. `slack check` uses only the saved association and the Slack tool;
it does not reopen CRM or calls. Both require `--state-dir` and accept the Slack
credential folder as `--connection-dir`. Mapping supports a copied `--thread-url`
or the native `--channel` plus `--thread-ts` pair. Conflicting selections require
the relevant explicit `--replace` and `--reassign` flags.

Daily `serve`, `brief`, Salesforce account inspection, and `calls import` accept
`--slack-backend slack --slack-connection-dir PATH` with an explicit source
`--state-dir`. A live Slack brief additionally requires `--with-slack`. The
same flags must be reused for existing state; fixtures remain the default.
Live Slack evidence uses workflow version 5 and source label `slack`. Its default
formatter is `extractive` and uses zero model tokens. [Selection, check results,
limits, and both initialization orders](slack-live.md) describe the complete flow.

Map and check progress uses JSON lines on stderr with `progress.command`,
`step`, and `status`. Stdout contains the single final result. A completed
failed Slack check prints all five steps before exit 4. Invalid arguments and
startup failures keep their ordinary categories instead of fabricating a report.

## Renewal demo

`demo` follows Morgan Reed preparing Juniper Crest Learning's renewal. All people,
companies, CRM records, call notes, and Slack messages are invented. It includes
Slack by default; the existing `demo --with-slack` spelling has the same result.
The quick and longer walkthroughs use this same story. `verify` runs the separate
guarantee checks and intentionally creates one local activity for the retry test.

The default state folder is `var/renewal-demo` for `demo` and `var/verification`
for `verify`. Other state-based commands retain `var/v2`. These separate defaults
leave an older demo in `var/v2` untouched. `--state-dir` still selects an explicit
folder. If that folder contains an older or edited book that cannot demonstrate
the story, the error identifies the mismatch and gives a concrete unused folder
in its retry command. Existing source records are not replaced or upgraded.
Demo source and draft checks run before publication. A failed check can retain
a rejected evidence artifact, but it publishes no accepted brief or text.

```sh
gtm-connector demo --state-dir var/renewal-demo --json
gtm-connector verify --state-dir var/verification --json
```

The narrative JSON has these fields:

| Field | Meaning and type |
| --- | --- |
| `schema_version`, `status`, `workflow`, `data_mode` | Integer `1` and strings `"accepted"`, `"renewal-demo"`, and `"demo"`. |
| `sources` | Object whose `crm`, `calls`, and `slack` strings are each `"demo_fixture"`. |
| `rep` | Object with invented `name` and `role` strings. |
| `account` | Object with string `id`, `name`, `record_ref`, and `renewal_date`. |
| `lookup` | Object with the search `query` string, integer `matches`, and `receipt_id` string. |
| `denied_read` | Object with `tool: "read_call_notes"`, integer `status: 403`, `required_scope: "calls:read"`, and a `receipt_id` string. |
| `brief` | Object with string `brief_id`, `account_id`, `as_of`, `text`, and `proposed_follow_up`, copied from the accepted saved artifact. |
| `injection` | Object with call-note `record_ref` and `slack_record_ref` strings, boolean `quoted: true`, and integer `activities_created: 0`. |
| `audit` | Object with `run_id` string and integer `rows: 6`. |
| `activities_created` | Integer activity-count change during the narrative run, required to be `0`. |

The lookup and refused read use `audit.run_id`. The four briefing reads use
`brief.brief_id`. Together they produce five successful receipts and one error
receipt, with no pending receipt. The accepted brief quotes the injected source
text and contains a checked follow-up proposal. It sends nothing and requests
no write grant. Run IDs and receipt IDs change on each run.

Each run adds one saved brief and six receipts to its selected state folder.
A fresh folder has zero activities. A reused folder retains existing activities;
`activities_created` measures only this run's change. Seeding preserves existing
fixture edits and older names, so choose a fresh state folder for the current
story rather than overwriting an existing book.

## Server output

Start the local services and leave this terminal open:

```sh
gtm-connector serve --state-dir var/session
```

Startup on stderr identifies the address, CRM backend, sources, state folder,
granted scopes, connection command, and Ctrl-C stop instruction. The source
summary labels CRM as demo fixtures or Salesforce data with the selected
environment. Call notes default to demo fixtures; imported mode labels them as
user-supplied transcript exports. Slack defaults to demo fixtures; explicit live
mode labels the selected mapped thread and states that access is checked on read.
A bound Salesforce demo also names its public kit ID. Salesforce startup checks
Organization metadata and the private connection identity; sandbox and
production also check the required standard-object capabilities. This is an
owner setup check, not a client record read or proof of access to every record.
No scope is granted at startup. A client needs an explicit grant from the local
owner. Shutdown also goes to stderr and leaves local state in place.

While the server runs, stdout receives one line for each observed tool attempt:
UTC completion time, tool, caller fingerprint, decision, duration in milliseconds,
and receipt ID. A refusal includes its reason. This stream includes no raw
arguments, source text, or bearer credentials. A fingerprint groups a presented
credential; it does not identify a person or establish a valid grant.

`serve --json` uses JSON lines on stdout. The first line has this shape; addresses
and paths here are placeholders:

```json
{"type":"ready","resource":"https://127.0.0.1:PORT/mcp","issuer":"https://127.0.0.1:PORT","crm_backend":"sqlite","owner_config":"/STATE/owner.json","state_dir":"/STATE","granted_scopes":[],"sources":{"crm":"demo_fixture","calls":"demo_fixture","slack":"demo_fixture"}}
```

`sources` describes the selected data sources, not connection verification.
`sources.crm` is `"demo_fixture"` for SQLite or `"salesforce"` for Salesforce.
`sources.calls` is `"demo_fixture"` or `"imported_transcript"` for the selected
mode, and `sources.slack` is `"demo_fixture"` or `"slack"`. Supplying
`--salesforce-demo-binding` adds `demo_kit_id`, the public kit identifier, to the
ready object. Salesforce additionally includes `sf_environment`
(`"developer"`, `"sandbox"`, or `"production"`) and `crm_identity_verified: true`
after successful startup metadata and identity checks. These fields do not
promise access to every record. The source summary contains no Salesforce alias,
address, org ID, or record IDs. The binding workflow is documented in [the Salesforce demo kit](salesforce-demo.md).
`serve --selection FILE` adds a `selection` object with `accounts` and
`slack_accounts` counts and the seven `tools` names; it contains no account IDs.
See [the selection guide](selection.md).

Subsequent lines have `type: "tool_call"`, `time` (UTC ISO 8601), `tool`,
`caller` (a complete SHA-256 fingerprint or `"anonymous"`), `decision`
(`"allowed"`, `"refused"`, or `"failed"`), `duration_ms` (number),
`receipt_id` (string or `null`), and `reason` (string or `null`). An audit write
failure can leave the event without a receipt ID. Duration includes the audit
write. The event observer is best effort; [durable receipts](audit.md) remain
the record to inspect. Unparseable traffic without a `tools/call` marker and
requests that disconnect before recognition are outside this stream. A marked
attempt can still produce an event when its payload cannot be parsed.
Tool names outside the registered catalog appear as `"<unknown>"`; malformed
names use fixed markers. Closing the event pipe leaves the server running and
discards further event output. The private audit still records each attempt.

The JSON shutdown notice is a separate stderr line:
`{"message":"Connector stopped. Local state is retained."}`. Ctrl-C stops
`serve` with exit `0`; it stops an inspection `--follow` with exit `130`.

## Check the setup

```sh
gtm-connector doctor --state-dir var/session
gtm-connector doctor --state-dir var/session --json
```

Doctor checks Python, installed dependencies, SQLite support, private state
files, owner configuration, certificate validity, and both local services'
metadata. It stops before dependent checks if private state or configuration
is unsafe to read. Each failure gives a suggested fix; doctor does not apply
fixes, create state, grant access, or read CRM, call, or Slack records. Its
network checks are GET requests to validated local HTTPS endpoints.

Output includes check names and bounded messages, never owner secrets, tokens,
or source values. The JSON check `status` is `"pass"` or `"fail"`; `fix` is an
empty string for a pass. The top-level `status` is `"passed"` or `"failed"`.
A failed report exits `4`. [Doctor tests](../tests/test_doctor.py) cover failures,
redaction, and unchanged state.

## Shell completion

These commands use an installed `gtm-connector`; no repository clone is needed.
Completion lists command names, options, fixed choices, and paths without
starting the connector or reading its state.

For the current Bash session:

```bash
eval "$(gtm-connector completion bash)"
```

Add that line to `~/.bashrc` to load it in new interactive Bash sessions. For
Zsh, initialize its completion system before loading the script:

```zsh
autoload -Uz compinit
compinit
source <(gtm-connector completion zsh)
```

Put those lines in `~/.zshrc`, or put the final line after an existing `compinit`.
For Fish, save the script in its completion directory and load it now:

```fish
mkdir -p ~/.config/fish/completions
gtm-connector completion fish > ~/.config/fish/completions/gtm-connector.fish
source ~/.config/fish/completions/gtm-connector.fish
```

The checked behavior includes Bash and Zsh completion-function dispatch locally
and Bash, Zsh, and Fish dispatch in the Linux development container. These are
shell execution checks, not an interactive terminal UI test. The
[completion tests](../tests/test_completion.py) exercise the generated scripts.

The interface follows [Command Line Interface Guidelines](https://clig.dev/)
and GitHub's [Primer CLI guidelines](https://github.com/cli/cli/tree/trunk/docs/primer).
The older `primer.style/cli` address was unavailable when checked on 2026-11-17;
the official GitHub CLI repository contains the maintained guidance.
