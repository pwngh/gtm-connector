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
| `audit` | Array of receipts, ordered as stored. Fields are documented in [audit](audit.md). |
| `usage` | Object with `brief_id`, `day_basis`, `calls`, `by_token`, `by_tool`, `by_day`, `by_token_tool_day`, `briefs`, and `model_tokens`. See [usage](usage.md). |
| `evaluate` | Object with `status`, account and claim counts, per-account `cases`, receipt counts, and model usage. See [evaluation](evaluation.md). |
| `demo` | Object containing the connector checks, `briefs`, receipt counts, and `injection_brief`. Slack mode adds `slack` and `workflow`. |
| `serve` | One compact JSON startup object with `resource`, `issuer`, `crm_backend`, and `owner_config`. It contains no credential. |
| `inspector-config` | Object with `config`, `certificate`, and `scopes`. Credentials stay in the private file. |

Help remains plain help text, even with `--json`. JSON errors are one object on
stderr, with an empty stdout unless the command has already produced results:

```json
{"error":{"code":"usage_error","message":"account not found: acc-missing","next":"gtm-connector accounts --db 'var/v2/crm.sqlite3'"}}
```

Treat `error.code` and the process exit as the machine contract. Read `message`
for the cause and `next` for a suggested command. A failed evaluation still
prints its complete result on stdout before reporting failure on stderr.

| Exit | Meaning | JSON error code |
| --- | --- | --- |
| 0 | The command succeeded, or a running server stopped cleanly. | None. |
| 1 | A local file, database, or runtime operation failed. | `local_failure` |
| 2 | Arguments or local configuration are missing or invalid. | `usage_error` |
| 3 | The requested operation was refused. | `refused` |
| 4 | The evidence checker, demo check, or evaluation failed. | `check_failed` |
| 5 | A source service or its transport failed. | `upstream_failure` |
| 130 | Ctrl-C interrupted a command. Completed local writes remain. | `interrupted` |

A command typo suggests the nearest command without running it. A broken pipe
closes output quietly. Help and version build no runtime and import neither
SQLite, the MCP SDK, nor the HTTP client. The CLI contract tests hold that import
boundary without depending on elapsed time.

The interface follows [Command Line Interface Guidelines](https://clig.dev/)
and GitHub's [Primer CLI guidelines](https://github.com/cli/cli/tree/trunk/docs/primer).
The older `primer.style/cli` address was unavailable when checked on 2026-11-17;
the official GitHub CLI repository contains the maintained guidance.
