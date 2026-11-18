# Use the command line

Run `gtm-connector` for a short starting point. Run `gtm-connector COMMAND --help`
for each command's description, options, and example. `python -m gtm_connector`
accepts the same arguments. `gtm-connector --version` prints the installed version.

Start with invented data:

```sh
gtm-connector demo --with-slack
gtm-connector brief acc-003 --with-slack
```

The CLI uses readable text by default. Terminal tables have headings; pipes have
complete tab-separated rows without headings or color. `NO_COLOR` disables color
on terminals too. Source control characters are displayed as escapes. Brief
source text already uses quoted, escaped literals. Data goes to stdout. Errors,
server startup messages, and shutdown notices go to stderr.

Every command accepts `--json`, before or after its name. Use it when parsing
output. JSON results retain the existing command-specific shapes below. Keys
may be added; their existing meaning and type stay stable within version 0.1.
Human text is intended for reading and can change independently.

| Command | JSON result on stdout |
| --- | --- |
| No command | Object with `description` (string), `try` (string array), and `help` (string). |
| `--version` | Object with `version` (string). |
| `seed` | Object with `counts`, an object of integer counts by table. |
| `accounts` | Array of account objects, or `[]`. |
| `account` | Account object with `id`, `name`, `industry`, renewal fields, and a `contacts` array. |
| `brief` | Saved artifact with `schema_version: 1`, identifiers, `as_of`, `status`, `evidence`, `text`, and `model_usage`. Slack mode adds `proposed_follow_up`. |
| `check-brief` | Object with `status: "valid"` and `brief_id`. |
| `audit` | Array of receipts, ordered as stored. With `--follow`, each changed receipt is one JSON line. Fields and filters are documented in [audit](audit.md). |
| `usage` | Object with `brief_id`, `day_basis`, `calls`, `by_token`, `by_tool`, `by_day`, `by_token_tool_day`, `briefs`, and `model_tokens`. Active receipt filters add `filters`. With `--follow`, each changed report is one JSON line. See [usage](usage.md). |
| `evaluate` | Object with `status`, account and claim counts, per-account `cases`, receipt counts, and model usage. See [evaluation](evaluation.md). |
| `demo` | Object containing the connector checks, `briefs`, receipt counts, and `injection_brief`. Slack mode adds `slack` and `workflow`. |
| `serve` | A `ready` JSON line, followed by one `tool_call` JSON line per observed tool attempt. See [server output](#server-output). |
| `inspector-config` | Object with `config`, `certificate`, and `scopes`. Credentials stay in the private file. |
| `connect` | Object with `app`, `status`, `setup`, `limits`, and `documentation`. Creating a grant adds `config`, the private file's path. See [client setup](clients.md). |
| `doctor` | Object with `status` and a `checks` array. Each check has `name`, `status`, `message`, and `fix`. |
| `completion` | Object with `shell` and `script`. Without `--json`, stdout contains only the shell script. |
| `salesforce check` | Sanitized object with `schema_version: 1`, `status`, `as_of`, `steps`, request counts, and count definitions. [Salesforce checks](salesforce.md#run-the-shareable-setup-check) defines each field. |

Help remains plain help text, even with `--json`. JSON errors are one object on
stderr, with an empty stdout unless the command has already produced results:

```json
{"error":{"code":"usage_error","message":"account not found: acc-missing","next":"gtm-connector accounts --db 'var/v2/crm.sqlite3'"}}
```

Treat `error.code` and the process exit as the machine contract. Read `message`
for the cause and `next` for a suggested command. A failed evaluation, Salesforce, or doctor
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
| 4 | The evidence checker, demo, evaluation, doctor, or Salesforce check failed. | `check_failed` |
| 5 | A source service or its transport failed. | `upstream_failure` |
| 130 | Ctrl-C interrupted a command. Completed local writes remain. | `interrupted` |

A command typo suggests the nearest command without running it. A broken pipe
closes output quietly. Help and version build no runtime and import neither
SQLite, the MCP SDK, nor the HTTP client. The CLI contract tests hold that import
boundary without depending on elapsed time.

Use `serve` or `brief` with `--crm-backend salesforce --sf-org ALIAS` to read
credentials from an existing Salesforce CLI login. SQLite stays the default.
Salesforce briefs use today's local date unless `--as-of` is supplied; fixture
briefs use `2027-01-15`. A missing eligible Contract refuses with
`renewal_fields_unmapped` and exit `3`. The
[Salesforce guide](salesforce.md) explains mapping, refresh, and private output.

## Server output

Start the local services and leave this terminal open:

```sh
gtm-connector serve --state-dir var/session
```

Startup on stderr identifies the address, CRM backend, sources, state folder,
granted scopes, connection command, and Ctrl-C stop instruction. The source
summary labels CRM as demo fixtures or selected Salesforce data. Call notes and
Slack are always demo fixtures in this version. A bound Salesforce demo also
names its public kit ID. Selecting Salesforce does not verify its connection;
startup makes no CRM request. No scope is granted at startup. A client needs an
explicit grant from the local owner. Shutdown also goes to stderr and leaves
local state in place.

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
`sources.calls` and `sources.slack` are `"demo_fixture"`. Supplying
`--salesforce-demo-binding` adds `demo_kit_id`, the public kit identifier, to the
ready object. The source summary contains no Salesforce alias, address, or
record IDs. The binding workflow is documented in [the Salesforce demo kit](salesforce-demo.md).

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
