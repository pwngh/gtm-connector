# Use the command line

A person uses the connector through the `gtm-connector` command. This page gives its commands, what
each prints, and its exit codes.

```sh
gtm-connector demo                         # invented data, in its own folder
gtm-connector connect demo                 # connect the state folder to the invented data
gtm-connector brief acc-003                # a card from what is connected
gtm-connector inspect BRIEF_ID             # every unit of that card, one per line
gtm-connector inspect BRIEF_ID 5           # one number on that card
gtm-connector COMMAND --help
python -m gtm_connector COMMAND            # accepts the same arguments
```

The top level has seven commands: `demo`, `connect`, `accounts`, `brief`, `inspect`, `audit`, and
`mcp`. The rest are under `debug`: `debug serve`, `debug doctor`, `debug verify`, `debug completion`,
`debug salesforce check`, `debug calls`, and `debug slack`.

## Connect the sources

A state folder is the private folder that holds one connector's files, and `--state-dir` selects
it. Where this page says "the folder", it means a state folder.
`connect` saves what a state folder reads in its `profile.json`. `brief`, `accounts`,
`inspect ACCOUNT_ID`, `mcp`, `debug serve`, `debug calls import`, and `debug slack map` read that file
and take no source option. In a
folder with no connected CRM each of them exits `2`, creates nothing, and says:
"No CRM is connected. Run gtm-connector connect salesforce --org ALIAS, or gtm-connector connect
demo for invented data.", with the folder's `--state-dir` in both commands when it is not the
default.

| Command | What it does |
| --- | --- |
| `connect demo` | The invented demo data, for every source the folder has not already read for real: transcripts or a Slack thread the folder holds stay connected. Nothing is contacted. |
| `connect salesforce --org ALIAS` | Reads the `sf` login of that alias, checks the org's identity and environment, pins them to the folder, and saves the alias and environment. It starts no services and reads no record. `ALIAS` is an `sf` alias, never a username. `--environment sandbox` or `production` replaces the default, `developer`. Calls and Slack start not connected. See [Salesforce](salesforce.md). |
| `connect calls` | Connects imported call transcripts. Add files with `debug calls import`. See [calls](calls.md). |
| `connect slack` | Connects the Slack sign-in saved in the folder by `debug slack connect`. Without one it exits `2` and names that command. See [Slack](slack.md). |
| `connect status`, or `connect` alone | Lists `crm`, `calls`, and `slack` as connected or not connected, with the org alias and environment for Salesforce. It reads only `profile.json`. |
| `connect PROVIDER --remove` | Detaches that source when it is the one connected, and changes nothing otherwise. Removing the CRM (`demo` or `salesforce`) removes calls and Slack with it. No file of the folder is deleted. |
| `connect inspector`, `claude-code`, `claude-desktop`, `cursor` | Prints the setup of an assistant app. See [client setup](clients.md). |

- Beside Salesforce a source is connected or not connected. It is never demo data. A card then says
  "Calls: not connected." or "Slack: not connected.", and nothing is read from that source.
- Beside the demo CRM, calls and Slack are demo data until `connect calls` or `connect slack`
  replaces one. `--remove` returns it to demo data, and is refused in a folder that has already read
  transcripts or a Slack thread, because demo data and those never share a folder.
- Connecting Salesforce over demo data keeps nothing of it. Connecting the same kind of CRM again
  keeps calls and Slack.
- `--seller NAME` with `salesforce`, `calls`, or `slack` lists a seller's name or Slack user ID.
  Repeat it for each seller. The list replaces the saved one, so name every seller each time, and
  a connect without `--seller` keeps it. `connect status` shows the saved list. See
  [roles](briefs.md).
- `connect` changes the profile while it holds the folder's lock. A folder that `debug serve` or another
  command is using refuses with exit `1` until it is free. A `connect` that fails leaves the profile
  as it was, and removes a folder it created.
- `profile.json` holds the provider of each source and, for Salesforce, the alias and environment.
  It holds no credential, org ID, or address.
- A folder keeps the sources it first read. Demo data that was read cannot become Salesforce or
  transcripts in the same folder, and one org's folder refuses another org. The refusal exits `3`
  with `source_identity_mismatch` and names a fresh `--state-dir`.
- `demo` leaves its folder connected to the demo data, so `brief`, `inspect`, and `audit` work there.

## Output contract

- Data goes to stdout. Errors, progress, and server notices go to stderr.
- The default output is text, and its wording can change. A pipe gets tab-separated rows without
  headings or color. `NO_COLOR` turns color off on a terminal. Control characters in source values
  print as escapes.
- Every command, and every command under `debug`, accepts `--json` anywhere before `--`, with
  the same result. Keys can be added. Existing keys keep their meaning and type within version 0.1.
  Help stays plain text.
- A JSON error is one stderr object: `error.code`, `error.message`, and `error.next`, a command.
- `debug salesforce check`, `debug calls import`, and the `debug slack` commands write progress. With
  `--json`, a progress line is an object with one `progress` key, whose `command` is the command's
  full path, such as `debug calls import`.
- A failed `debug doctor`, `debug salesforce check`, or `debug slack check` prints its report on
  stdout.

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
| `accounts` | Array of account objects ordered by name and then ID, or `[]`. When more than 100 match: object with `accounts`, the first 100, and `has_more: true`. Text output then adds one line on stderr that says so. |
| `brief` | The saved [Deal card](briefs.md): object with `schema_version: 3`, `brief_id`, `account_id`, `as_of`, `status: "accepted"`, `rules_version` (the sentence rules the units were numbered under), `sealed_by` (the receipt that holds the saved file's digest), `sources`, `text` (the card), `units`, and `gaps`. The saved file adds `evidence`, the raw records and receipts, which no command prints. `as_of` is today's UTC date with Salesforce and `2027-01-15` with demo data, unless `--as-of YYYY-MM-DD` is given. `sources.crm` is `"demo_fixture"` or `"salesforce"`, `sources.calls` is `"demo_fixture"`, `"imported_transcript"`, or `"not_connected"`, and `sources.slack` is `"demo_fixture"`, `"slack"`, or `"not_connected"`. A card reads every connected source. `--without-calls` leaves `calls:read` out of the grant, and the card says so. Text output names the sources on stderr and prints the card, then `Brief:` and the two `inspect` commands for it. |
| `inspect ACCOUNT_ID` | Account object with a `contacts` array, read as `accounts` reads: the demo book's SQLite file directly, and Salesforce through the scoped, audited tools with the CRM alone, so a problem with calls or Slack never stops it. Its text, as the names `accounts` lists, is neutralized as a card's is. Salesforce adds `contracts`, `backend`, and `source_id`. See [Salesforce](salesforce.md). `newest_brief_id` is the newest card of the account saved in this folder by `brief` or `demo` that passes the checks `inspect` makes, by when it was sealed, or `null`, also when the audit log cannot be read. Text output ends with that card and the command that lists it. |
| `inspect BRIEF_ID` | Object with `brief_id`, `account_id`, and `units`: every unit of the saved card as sealed, in number order: the card's own text, and for a unit sealed under older neutralizer rules, its text and speaker label neutralized again by today's. `--role customer`, `seller`, or `unknown` keeps only the sentences of that role. Text output is one line per unit, never cut or wrapped. A record line holds its number, `record`, its table, and its ID. A sentence line holds its number, source, time (with `cues F-L` or `lines F-L` when it lies in an imported transcript), role, speaker label as a JSON literal or `no speaker label`, `shown` or why it is not on the card (`cut off`, `too long to show`, `dated after the card's day`, `past the card's places`, or `not recorded` for a card sealed before that was kept), and its text as a JSON literal. |
| `inspect BRIEF_ID N [N ...]` | Array with one object per number, in the order given: `brief_id`, `account_id`, `n`, `kind` (`"record"` or `"sentence"`), `source`, `record_ref`, `receipt` (`audit_id`, `tool`, `status`, `http_status`), and `next_by_speaker`. A record adds `fields`. A sentence adds `text`, `speaker`, `role`, `basis`, `contact`, `at`, `partial`, `shown`, `span`, `raw_sha256`, and `sanitizer_version`. `next_by_speaker` is the number of the next whole sentence under the same speaker label in the same record, or `null`; it is `null` for a record, for a sentence with no label, and for a label that holds what neutralizing put there, such as `(5)`, because two labels can then print alike. Text output is one table per number, separated by a blank line. |
| `inspect RECEIPT_ID` | The receipt object, as `audit` gives it, with the text in its `arguments` neutralized as a card's is. Text output is its `audit` row, then its arguments and the rows it returned. |
| `audit` | Array of receipts in stored order. `--follow` prints one JSON line per changed receipt until Ctrl-C, which exits `130`. See [audit](audit.md). |
| `demo` | One law-firm renewal on invented data. Object with `schema_version: 2`, `status: "accepted"`, `workflow: "renewal-demo"`, `data_mode: "demo"`, `sources` (each `"demo_fixture"`), `account`, `lookup` (`query`, `matches`, `receipt_id`), `denied_read` (`read_call_notes`, status `403`, required scope `calls:read`), `brief` (`brief_id`, `account_id`, `as_of`, `text`, `units`, and `gaps`, copied from the saved card), `audit` (`run_id`, `rows: 7`), and `activities_created: 0`. Each run adds one saved card and seven receipts, the last of which seals the card, and creates no activity. An older or edited book in the folder exits `4`. |
| `debug verify` | Object with the connector checks, `briefs`, receipt counts, and `injection_brief`. `--with-slack` adds `slack` and `workflow`. It runs on invented data, in a folder that is connected to nothing or to the demo data only. See [tools](tools.md). |
| `debug serve` | One `ready` line, then one `tool_call` line per tool attempt. See the server output below. |
| `connect status` | Object with `schema_version: 1`, `sources`, and `sellers`. `sources` is the `sources` object of `profile.json`: each of `crm`, `calls`, and `slack` that is connected, with its `provider` (`"demo"`, `"salesforce"`, `"imported"`, or `"slack"`) and, for Salesforce, `org` and `environment`. `sellers` is the saved seller list, or `[]`. |
| `connect demo`, `salesforce`, `calls`, `slack` | Object with `schema_version: 1`, `sources`, `provider` (the name given), and `status`: `"connected"`, `"removed"`, or `"not_connected"` when `--remove` named a source that was not connected. |
| `connect APP` | Prints MCP client configuration. Object with `app`, `status`, `setup`, `limits`, and `documentation`. A grant adds `config`, the private file's path. `--transport stdio` adds `transport`, `config`, and `backup`. See [client setup](clients.md). |
| `mcp stdio` | The local bridge an assistant app starts. stdout carries only MCP messages. `--json` changes only how a startup error is written to stderr. See [client setup](clients.md). |
| `debug doctor` | Checks Python, the dependencies, SQLite, the private state files, the owner configuration, the certificate dates, and the metadata of both local services. It changes nothing, grants nothing, and reads no CRM, call, or Slack record. Object with `status` (`"passed"` or `"failed"`) and `checks`, each with `name`, `status` (`"pass"` or `"fail"`), `message`, and `fix`. |
| `debug completion` | Object with `shell` and `script`. |
| `debug salesforce check` | Sanitized object with `schema_version: 1`, `status`, `as_of`, `steps`, and request counts. See [Salesforce](salesforce.md). |
| `debug slack connect`, `status`, `disconnect` | See [Slack authentication](slack.md). |
| `debug slack map`, `check` | See [live Slack](slack.md). |
| `debug calls import`, `list`, `inspect` | `import`: import metadata, including `speaker_names` (whether the import declared them), with the `created`, `selected`, and `reassigned` outcomes and no transcript text. `list`: object with an `imports` array and a nullable `next_after` cursor. `inspect`: import metadata and the excerpt units, and `--full` adds all units and the exact `original_text`. See [calls](calls.md). |

`inspect ACCOUNT_ID` reads the account as `accounts` does: the demo book's file directly, or
Salesforce through the local services and the audited tools, which commit a receipt. The other forms
read only the saved cards and the audit log of the folder. On the demo CRM, and in every other form,
`inspect` writes nothing. A saved card is read only when the file is the one its [seal](briefs.md)
names and every number on it resolves to its record and receipt, so a changed card, or one
copied from another folder, exits `2`. A brief prepared by an assistant with `prepare_renewal_brief`
is read with `read_renewal_brief`, not with `inspect`, and `inspect` says so. The numbers are the
digits printed on the card, given together after the brief ID. An ID that is none of the four forms, numbers
after anything but a brief ID, or `--role` with anything but a brief ID alone exits `2`. Many numbers
check the card once.

`--state-dir` selects the private state folder. The default is `var/v2`, except `var/renewal-demo`
for `demo` and `var/verification` for `debug verify`. One process holds a folder at a time, so while
`debug serve` runs, connect another folder for `brief`.

`debug serve --selection FILE` serves only the selected accounts and threads and turns writes off.
See [selection](selection.md).

## Server output

`gtm-connector debug serve` runs until Ctrl-C, which exits `0`. Startup text on
stderr gives the address, the sources, the state folder, and the connection command. No scope is
granted at startup. Stdout gets one line per tool attempt, with no arguments, source text, or bearer
token. With `--json`, the first line is `ready`, shown with placeholders:

```json
{"type":"ready","resource":"https://127.0.0.1:PORT/mcp","issuer":"https://127.0.0.1:PORT","crm_backend":"sqlite","owner_config":"/STATE/owner.json","state_dir":"/STATE","granted_scopes":[],"sources":{"crm":"demo_fixture","calls":"demo_fixture","slack":"demo_fixture"}}
```

| Line | Field | Values |
| --- | --- | --- |
| `ready` | `sources` | The connected sources. `crm` is `"demo_fixture"` or `"salesforce"`. `calls` is `"demo_fixture"`, `"imported_transcript"`, or `"not_connected"`. `slack` is `"demo_fixture"`, `"slack"`, or `"not_connected"`. Slack thread access is checked when a thread is read. |
| `ready` | `sf_environment`, `crm_identity_verified` | Salesforce only: `"developer"`, `"sandbox"`, or `"production"`, and `true` after the startup identity check. |
| `ready` | `selection` | With `--selection`: `accounts` and `slack_accounts` counts and the seven `tools` names, without account IDs. |
| `tool_call` | `time`, `duration_ms` | UTC ISO 8601 completion time, and a number that includes the audit write. |
| `tool_call` | `tool` | The tool name. A name outside the catalog is `"<unknown>"`. |
| `tool_call` | `caller` | A complete SHA-256 fingerprint of the presented bearer token, of the header value when it holds no parseable bearer token, or `"anonymous"` when the header is absent or empty. It does not identify a person or prove a valid grant. |
| `tool_call` | `decision` | `"allowed"`, `"refused"`, or `"failed"`. |
| `tool_call` | `receipt_id`, `reason` | String or `null`. A failed audit write can leave the receipt ID `null`. A refusal includes its reason. |

This stream is best effort, and the [audit receipts](audit.md) are the record. If the reader of
stdout closes, the server keeps running and still writes receipts. The JSON shutdown notice is one
stderr line: `{"message":"Connector stopped. Local state is retained."}`.

## Shell completion

```sh
eval "$(gtm-connector debug completion bash)"   # Bash: add to ~/.bashrc.
source <(gtm-connector debug completion zsh)    # Zsh: add to ~/.zshrc, after compinit has run.
mkdir -p ~/.config/fish/completions             # Fish.
gtm-connector debug completion fish > ~/.config/fish/completions/gtm-connector.fish
```

Completion does not start the connector or read its state.
