# Use the command line

```sh
gtm-connector demo                         # invented data
gtm-connector brief acc-003 --with-slack   # invented data
gtm-connector trace BRIEF_ID 5             # one number on that card
gtm-connector COMMAND --help
python -m gtm_connector COMMAND            # accepts the same arguments
```

## Output contract

- Data goes to stdout. Errors, progress, and server notices go to stderr.
- The default output is text, and its wording can change. A pipe gets tab-separated rows without
  headings or color. `NO_COLOR` turns color off on a terminal. Control characters in source values
  print as escapes.
- Every command accepts `--json`, before or after its name. Keys can be added. Existing keys keep
  their meaning and type within version 0.1. Help stays plain text.
- A JSON error is one stderr object: `error.code`, `error.message`, and `error.next`, a command.
- `salesforce check`, `calls import`, and the `slack` commands write progress. With `--json`, a
  progress line is an object with one `progress` key.
- A failed `doctor`, `salesforce check`, or `slack check` prints its report on stdout.

| Exit | Meaning | JSON error code |
| --- | --- | --- |
| 0 | The command succeeded, or a running server stopped cleanly. | None. |
| 1 | A local file, database, or runtime operation failed. | `local_failure` |
| 2 | Arguments or local configuration are missing or invalid. | `usage_error` |
| 3 | The requested operation was refused. | `refused` |
| 4 | The evidence check, demo, verification, doctor, Salesforce check, or completed Slack check failed. | `check_failed` |
| 5 | A source service or its transport failed. | `upstream_failure` |
| 130 | Ctrl-C interrupted a command. Completed local writes remain. | `interrupted` |

## Commands

| Command | JSON result on stdout |
| --- | --- |
| No command, `--version` | Object with `description`, `try` (string array), and `help`. Object with `version`. |
| `seed` | Object with `counts`, integer counts by table. |
| `accounts` | Array of account objects ordered by name and then ID, or `[]`. When more than 100 match: object with `accounts`, the first 100, and `has_more: true`. Text output then adds one line on stderr that says so. |
| `account` | Account object with a `contacts` array. Salesforce adds `contracts`, `backend`, and `source_id`. See [Salesforce](salesforce.md). |
| `brief` | The saved [Deal card](briefs.md): object with `schema_version: 2`, `brief_id`, `account_id`, `as_of`, `status: "accepted"`, `sources`, `text` (the card), `units`, and `evidence`. `as_of` is today's local date with Salesforce and `2027-01-15` with fixtures, unless `--as-of YYYY-MM-DD` is given. `sources.crm` is `"demo_fixture"` or `"salesforce"`, `sources.calls` is `"demo_fixture"` or `"imported_transcript"`, and `--with-slack` adds `sources.slack`. Text output prints the card, then `Brief:` and the `trace` command. |
| `trace BRIEF_ID NUMBER` | One number on a saved card, resolved from its stored evidence: object with `brief_id`, `account_id`, `n`, `kind` (`"record"` or `"sentence"`), `source`, `record_ref`, and `receipt` (`audit_id`, `tool`, `status`, `http_status`). A record adds `fields`. A sentence adds `text`, `speaker`, `role`, `basis`, `at`, `raw_sha256`, and `sanitizer_version`. An unknown brief or a number that is not on the card exits `2`. |
| `audit` | Array of receipts in stored order. `--follow` prints one JSON line per changed receipt until Ctrl-C, which exits `130`. See [audit](audit.md). |
| `demo` | One law-firm renewal on invented data. Object with `schema_version: 2`, `status: "accepted"`, `workflow: "renewal-demo"`, `data_mode: "demo"`, `sources` (each `"demo_fixture"`), `account`, `lookup` (`query`, `matches`, `receipt_id`), `denied_read` (`read_call_notes`, status `403`, required scope `calls:read`), `brief` (`brief_id`, `account_id`, `as_of`, `text`, and `units`, copied from the saved artifact), `audit` (`run_id`, `rows: 6`), and `activities_created: 0`. Each run adds one saved card and six receipts and creates no activity. An older or edited book in the folder exits `4`. |
| `verify` | Object with the connector checks, `briefs`, receipt counts, and `injection_brief`. `--with-slack` adds `slack` and `workflow`. See [tools](tools.md). |
| `serve` | One `ready` line, then one `tool_call` line per tool attempt. See the server output below. |
| `connect` | Prints MCP client configuration. Object with `app`, `status`, `setup`, `limits`, and `documentation`. A grant adds `config`, the private file's path. `--transport stdio` adds `transport`, `config`, and `backup`. See [client setup](clients.md). |
| `mcp stdio` | The local bridge an assistant app starts. stdout carries only MCP messages. `--json` changes only how a startup error is written to stderr. See [client setup](clients.md). |
| `doctor` | Checks Python, the dependencies, SQLite, the private state files, the owner configuration, the certificate dates, and the metadata of both local services. It changes nothing, grants nothing, and reads no CRM, call, or Slack record. Object with `status` (`"passed"` or `"failed"`) and `checks`, each with `name`, `status` (`"pass"` or `"fail"`), `message`, and `fix`. |
| `completion` | Object with `shell` and `script`. |
| `salesforce check` | Sanitized object with `schema_version: 1`, `status`, `as_of`, `steps`, and request counts. See [Salesforce](salesforce.md). |
| `slack connect`, `slack status`, `slack disconnect` | See [Slack authentication](slack.md). |
| `slack map`, `slack check` | See [live Slack](slack.md). |
| `calls import`, `calls list`, `calls inspect` | `import`: import metadata with the `created`, `selected`, and `reassigned` outcomes and no transcript text. `list`: object with an `imports` array and a nullable `next_after` cursor. `inspect`: import metadata and the excerpt units, and `--full` adds all units and the exact `original_text`. See [calls](calls.md). |

`--state-dir` defaults: `var/renewal-demo` for `demo`, `var/verification` for `verify`, `var/v2` for
`serve`, `brief`, `trace`, `audit`, `doctor`, `connect`, and `mcp`.

## Choose the sources

SQLite fixtures are the default for CRM, calls, and Slack. A missing or conflicting source option
exits `2` before credentials or state are opened. A Salesforce environment mismatch, or state
created for other sources, refuses with exit `3`.

| Flags | Commands | Rule |
| --- | --- | --- |
| `--crm-backend salesforce --sf-org ALIAS` | `serve`, `brief`, `accounts`, `account` | Uses an existing Salesforce CLI login. `--sf-environment sandbox` or `production` replaces the default, `developer`. See [Salesforce](salesforce.md). |
| `--calls-backend imported` | `serve`, `brief`, and Salesforce `accounts` and `account` | Requires an explicit `--state-dir`, the folder used by `calls import`. See [calls](calls.md). |
| `--slack-backend slack --slack-connection-dir PATH` | The same, and `calls import` | Requires an explicit `--state-dir`. `brief` also requires `--with-slack`. See [live Slack](slack.md). |
| `--salesforce-demo-binding FILE` | `serve`, `brief` | Requires Salesforce. See [the demo kit](salesforce.md). |
| `--selection FILE` | `serve` | Serves only the selected accounts and threads and turns writes off. See [selection](selection.md). |

## Server output

`gtm-connector serve --state-dir var/session` runs until Ctrl-C, which exits `0`. Startup text on
stderr gives the address, the sources, the state folder, and the connection command. No scope is
granted at startup. Stdout gets one line per tool attempt, with no arguments, source text, or bearer
credential. With `--json`, the first line is `ready`, shown with placeholders:

```json
{"type":"ready","resource":"https://127.0.0.1:PORT/mcp","issuer":"https://127.0.0.1:PORT","crm_backend":"sqlite","owner_config":"/STATE/owner.json","state_dir":"/STATE","granted_scopes":[],"sources":{"crm":"demo_fixture","calls":"demo_fixture","slack":"demo_fixture"}}
```

| Line | Field | Values |
| --- | --- | --- |
| `ready` | `sources` | The selected sources, not a verified connection. `crm` is `"demo_fixture"` or `"salesforce"`. `calls` is `"demo_fixture"` or `"imported_transcript"`. `slack` is `"demo_fixture"` or `"slack"`. |
| `ready` | `sf_environment`, `crm_identity_verified` | Salesforce only: `"developer"`, `"sandbox"`, or `"production"`, and `true` after the startup identity check. |
| `ready` | `demo_kit_id` | With `--salesforce-demo-binding`: the public kit identifier. |
| `ready` | `selection` | With `--selection`: `accounts` and `slack_accounts` counts and the seven `tools` names, without account IDs. |
| `tool_call` | `time`, `duration_ms` | UTC ISO 8601 completion time, and a number that includes the audit write. |
| `tool_call` | `tool` | The tool name. A name outside the catalog is `"<unknown>"`. |
| `tool_call` | `caller` | A complete SHA-256 fingerprint of the presented credential, or `"anonymous"`. It does not identify a person or prove a valid grant. |
| `tool_call` | `decision` | `"allowed"`, `"refused"`, or `"failed"`. |
| `tool_call` | `receipt_id`, `reason` | String or `null`. A failed audit write can leave the receipt ID `null`. A refusal includes its reason. |

This stream is best effort, and the [audit receipts](audit.md) are the record. If the reader of
stdout closes, the server keeps running and still writes receipts. The JSON shutdown notice is one
stderr line: `{"message":"Connector stopped. Local state is retained."}`.

## Shell completion

```sh
eval "$(gtm-connector completion bash)"   # Bash: add to ~/.bashrc.
source <(gtm-connector completion zsh)    # Zsh: add to ~/.zshrc, after compinit has run.
mkdir -p ~/.config/fish/completions       # Fish.
gtm-connector completion fish > ~/.config/fish/completions/gtm-connector.fish
```

Completion does not start the connector or read its state.
