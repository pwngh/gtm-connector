# Use the command line

```sh
gtm-connector demo
gtm-connector brief acc-007 --with-slack
gtm-connector COMMAND --help
```

Both examples use invented data. `python -m gtm_connector` accepts the same
arguments. `--version` prints the installed version.

## Output contract

- Data goes to stdout. Errors, progress, and server notices go to stderr.
- The default output is text for reading, and its wording can change. A pipe
  gets tab-separated rows without headings or color. `NO_COLOR` turns color off
  on a terminal. Control characters in source values print as escapes.
- Every command accepts `--json`, before or after its name. Keys can be added.
  Existing keys keep their meaning and type within version 0.1. Help stays
  plain text.
- A JSON error is one object on stderr. Read `error.code` and the exit status.
  `next` suggests a command.

```json
{"error":{"code":"usage_error","message":"account not found: acc-missing","next":"gtm-connector accounts --db 'var/v2/crm.sqlite3'"}}
```

| Exit | Meaning | JSON error code |
| --- | --- | --- |
| 0 | The command succeeded, or a running server stopped cleanly. | None. |
| 1 | A local file, database, or runtime operation failed. | `local_failure` |
| 2 | Arguments or local configuration are missing or invalid. | `usage_error` |
| 3 | The requested operation was refused. | `refused` |
| 4 | The evidence checker, demo, verification, evaluation, doctor, Salesforce check, or completed Slack check failed. | `check_failed` |
| 5 | A source service or its transport failed. | `upstream_failure` |
| 130 | Ctrl-C interrupted a command. Completed local writes remain. | `interrupted` |

A failed `evaluate`, `doctor`, `salesforce check`, or `slack check` prints its
complete report on stdout, then the error on stderr. `evaluate`,
`salesforce check`, `calls import`, and the `slack` commands write progress to
stderr. With `--json`, each progress line is an object with one `progress` key,
so a stderr reader must tell `progress` from `error`.

## JSON results

| Command | JSON result on stdout |
| --- | --- |
| No command, `--version` | Object with `description`, `try` (string array), and `help`. Object with `version`. |
| `seed` | Object with `counts`, integer counts by table. |
| `accounts` | Array of account objects, or `[]`. |
| `account` | Account object with a `contacts` array. Salesforce adds `contracts`, `backend`, and `source_id`, and its `id` is `acc-sf-` plus the 18-character ID. See [Salesforce](salesforce.md). |
| `brief` | Saved artifact with `schema_version: 1`, identifiers, `as_of`, `status`, `evidence`, `text`, and `model_usage`. `--with-slack` adds `proposed_follow_up`. |
| `check-brief` | Object with `status: "valid"` and `brief_id`. |
| `audit` | Array of receipts in stored order. `--follow` prints one JSON line per changed receipt. See [audit](audit.md). |
| `usage`, `evaluate`, `demo` | See [usage](usage.md), [evaluation](evaluation.md), and [renewal demo](#renewal-demo). |
| `verify` | Object with the connector checks, `briefs`, receipt counts, and `injection_brief`. `--with-slack` adds `slack` and `workflow`. See [tools](tools.md). |
| `serve` | One `ready` line, then one `tool_call` line per tool attempt. See [server output](#server-output). |
| `inspector-config` | Object with `config`, `certificate`, and `scopes`. |
| `connect` | Object with `app`, `status`, `setup`, `limits`, and `documentation`. A grant adds `config`, the private file's path. See [client setup](clients.md). |
| `doctor` | Object with `status` and `checks`. Each check has `name`, `status`, `message`, and `fix`. `fix` is an empty string for a pass. |
| `completion` | Object with `shell` and `script`. |
| `salesforce check` | Sanitized object with `schema_version: 1`, `status`, `as_of`, `steps`, and request counts. See [Salesforce](salesforce.md). |
| `slack connect`, `slack status` | Sanitized authentication state, granted scopes, expiry, and verification status. See [Slack authentication](slack-auth.md). |
| `slack disconnect` | The local deletion outcome and the access-token revocation outcome. An unconfirmed `--revoke` prints this result, then exits `5`. |
| `slack map` | `status` `saved` or `unchanged`, `thread_access: "unchecked"`, and the selected-thread count. It holds no private selector. |
| `slack check` | Sanitized five-step report with `crm_access_checked: false` and `calls_access_checked: false`. See [live Slack](slack-live.md). |
| `calls import` | Import metadata with the `created`, `selected`, and `reassigned` outcomes. It holds no transcript text. |
| `calls list` | Object with an `imports` array and a nullable `next_after` cursor. |
| `calls inspect` | Import metadata and the excerpt units. `--full` adds all units and the exact `original_text`. |

## Choose the sources

SQLite fixtures are the default for CRM, calls, and Slack.

| Flags | Commands | Rule |
| --- | --- | --- |
| `--crm-backend salesforce --sf-org ALIAS` | `serve`, `brief`, `accounts`, `account` | Uses an existing Salesforce CLI login. See [Salesforce](salesforce.md). |
| `--sf-environment sandbox` or `production` | The same four | The default is `developer`. Requires `--crm-backend salesforce`. |
| `--calls-backend imported` | `serve`, `brief`, and Salesforce `accounts` and `account` | Requires an explicit `--state-dir`, the folder used by `calls import`. See [calls](calls.md). |
| `--slack-backend slack --slack-connection-dir PATH` | The same, and `calls import` | Requires an explicit `--state-dir`. `brief` also requires `--with-slack`. See [live Slack](slack-live.md). |
| `--salesforce-demo-binding FILE` | `serve`, `brief` | Requires Salesforce. See [the demo kit](salesforce-demo.md). |
| `--selection FILE` | `serve` | Serves only the selected accounts and threads and turns writes off. See [selection](selection.md). |

- A missing or conflicting source option exits `2` before credentials or state
  are opened. A Salesforce environment mismatch, or state created for other
  sources, refuses with exit `3`.
- Salesforce `accounts` and `account` use `var/salesforce` unless `--state-dir`
  is given. `--db` is a SQLite option.
- A Salesforce brief uses today's local date and a fixture brief uses
  `2027-01-15`, unless `--as-of YYYY-MM-DD` is given. A Salesforce account
  without an eligible Contract refuses with `renewal_fields_unmapped` and
  exit `3`.
- A brief with imported calls uses evidence workflow version 4, and one with
  live Slack uses version 5. Both add a `sources` object. With the default
  drafter, both record the adapter `extractive` and zero model tokens.

## Slack and calls commands

| Command | What it does | State |
| --- | --- | --- |
| `slack connect`, `slack status`, `slack disconnect` | Manage Slack authentication only. See [Slack authentication](slack-auth.md). | `--connection-dir`, default `var/slack-connection` |
| `slack map` | Saves one thread for one account after an audited CRM lookup. Takes `--thread-url`, or `--channel` with `--thread-ts`. A conflict needs `--replace` or `--reassign`. | `--state-dir`, required |
| `slack check` | Reads only the saved thread through the Slack tool. It opens neither CRM nor calls. | `--state-dir`, required |
| `calls import`, `calls list`, `calls inspect` | Import a TXT or VTT export that you select, then read the saved state without a CRM connection. See [calls](calls.md). | `--state-dir`, default `var/imported-calls` |

The top-level `connect` prints MCP client configuration. It is not
`slack connect`. Ctrl-C during Slack authentication exits `130`, and a finished
credential save survives it, so run `slack status` afterwards.

## Renewal demo

```sh
gtm-connector demo --json
gtm-connector verify --state-dir var/verification --json
```

`demo` follows Morgan Reed preparing Juniper Crest Learning's renewal from CRM,
call notes, and Slack. Every person, company, and record is invented. `verify`
runs the separate guarantee checks and creates one local activity for its retry
test. The default state folder is `var/renewal-demo` for `demo`,
`var/verification` for `verify`, and `var/v2` for `serve`, `brief`, `audit`,
`usage`, `doctor`, `connect`, `check-brief`, and `inspector-config`.

Each `demo` run adds one saved brief and six receipts and creates no activity.
If the folder holds an older or edited book, `demo` exits `4` and its `next`
command names an unused folder. Existing records are never replaced.

| Field | Meaning and type |
| --- | --- |
| `schema_version`, `status`, `workflow`, `data_mode` | Integer `1` and strings `"accepted"`, `"renewal-demo"`, and `"demo"`. |
| `sources` | Object whose `crm`, `calls`, and `slack` strings are each `"demo_fixture"`. |
| `rep` | Object with invented `name` and `role` strings. |
| `account` | Object with string `id`, `name`, `record_ref`, and `renewal_date`. |
| `lookup` | Object with the search `query` string, integer `matches`, and `receipt_id` string. |
| `denied_read` | Object with `tool: "read_call_notes"`, integer `status: 403`, `required_scope: "calls:read"`, and a `receipt_id` string. |
| `brief` | Object with string `brief_id`, `account_id`, `as_of`, `text`, and `proposed_follow_up`, copied from the saved artifact. |
| `injection` | Object with call-note `record_ref` and `slack_record_ref` strings, boolean `quoted: true`, and integer `activities_created: 0`. |
| `audit` | Object with `run_id` string and integer `rows: 6`. |
| `activities_created` | Integer change in the activity count during the run. It must be `0`. |

The lookup and the refused read carry `audit.run_id`, and the four briefing
reads carry `brief.brief_id`: five successful receipts and one error receipt.
The brief quotes the instruction planted in the source text as data. Its
follow-up is a proposal, and nothing is sent.

## Server output

```sh
gtm-connector serve --state-dir var/session
```

Leave this terminal open. Startup text on stderr gives the address, the
sources, the state folder, and the connection command. No scope is granted at
startup. Each client needs an explicit grant from the local owner. Ctrl-C stops
`serve` with exit `0` and keeps local state. Ctrl-C stops `audit --follow` or
`usage --follow` with exit `130`.

Stdout gets one line per observed tool attempt, with no arguments, source text,
or bearer credential. With `--json`, the first line is the `ready` object. The
address and paths below are placeholders:

```json
{"type":"ready","resource":"https://127.0.0.1:PORT/mcp","issuer":"https://127.0.0.1:PORT","crm_backend":"sqlite","owner_config":"/STATE/owner.json","state_dir":"/STATE","granted_scopes":[],"sources":{"crm":"demo_fixture","calls":"demo_fixture","slack":"demo_fixture"}}
```

| `ready` field | Values |
| --- | --- |
| `sources` | The selected sources, not a verified connection. `crm` is `"demo_fixture"` or `"salesforce"`. `calls` is `"demo_fixture"` or `"imported_transcript"`. `slack` is `"demo_fixture"` or `"slack"`. |
| `sf_environment`, `crm_identity_verified` | Salesforce only: `"developer"`, `"sandbox"`, or `"production"`, and `true` after the startup identity check. Neither promises access to every record. |
| `demo_kit_id` | With `--salesforce-demo-binding`: the public kit identifier. |
| `selection` | With `--selection`: `accounts` and `slack_accounts` counts and the seven `tools` names, without account IDs. |

| `tool_call` field | Values |
| --- | --- |
| `time` | UTC ISO 8601 completion time. |
| `tool` | The tool name. A name outside the catalog is `"<unknown>"`. |
| `caller` | A complete SHA-256 fingerprint of the presented credential, or `"anonymous"`. It does not identify a person or prove a valid grant. |
| `decision` | `"allowed"`, `"refused"`, or `"failed"`. |
| `duration_ms` | Number. It includes the audit write. |
| `receipt_id`, `reason` | String or `null`. A failed audit write can leave the receipt ID `null`. A refusal includes its reason. |

This stream is best effort. The [audit receipts](audit.md) are the record. If
the reader of stdout closes, the server keeps running and still writes
receipts. The JSON shutdown notice is one stderr line:
`{"message":"Connector stopped. Local state is retained."}`.

## Check the setup

```sh
gtm-connector doctor --state-dir var/session
```

`doctor` checks Python, the installed dependencies, SQLite, the private state
files, the owner configuration, the certificate dates, and the metadata of both
local services. Each failure comes with a suggested fix. `doctor` changes
nothing, grants nothing, and reads no CRM, call, or Slack record. In JSON, each
check `status` is `"pass"` or `"fail"`, and the top-level `status` is
`"passed"` or `"failed"`. A failed report exits `4`.

## Shell completion

```sh
# Bash: add to ~/.bashrc.
eval "$(gtm-connector completion bash)"
# Zsh: add to ~/.zshrc, after compinit has run.
source <(gtm-connector completion zsh)
# Fish.
mkdir -p ~/.config/fish/completions
gtm-connector completion fish > ~/.config/fish/completions/gtm-connector.fish
```

Completion does not start the connector or read its state. CI runs the Bash,
Zsh, and Fish scripts through each shell's completion function. No interactive
terminal session has been tested.
