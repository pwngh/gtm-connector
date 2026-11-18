# Usage reporting

The report counts tool attempts from audit receipts and model tokens from saved briefs.

These commands report usage from the repository root after setup. The
placeholder takes a generated brief ID:

```sh
.venv/bin/python -m gtm_connector usage
.venv/bin/python -m gtm_connector usage --brief brief-REPLACE_WITH_ID
```

The default output shows tool counts and model-token totals as tables. Add
`--json` to read every group and saved-brief entry described below:

```sh
.venv/bin/python -m gtm_connector usage --json
```

The `--state-dir PATH` option selects another state directory. The command reads
local files without changing them and runs outside MCP authorization. The report
excludes arguments and source text. Missing state creates no files.

## Tool calls

In the JSON report, receipts appear in `by_token`, `by_tool`, and `by_day`, with UTC day boundaries.
`by_token_tool_day` combines those views. Each group and the overall `calls`
field report `total`, `ok`, `error`, and `pending` counts. Scope denials, limited
calls, and retries each count as an attempt.

Token groups use fingerprints of presented credentials, including rejected ones.
Missing credentials appear as `anonymous`. Groups establish neither a caller's
identity nor a valid grant. These counts are not billing data.

## Model usage

`briefs` lists each saved result's status, matching `audit_calls`, and
`model_usage`. The `model_tokens` totals cover saved files only; deleted files
cannot supply usage. Ollama counts include rejected drafts.

Fixture counts are zero. Unknown counts remain `null` in both the brief and the
corresponding total; `unknown_briefs` reports how many briefs have that gap.
A malformed or partial saved result stops the report with a clear error.

[Audit, rate limits, and writes](audit.md) explains receipt contents, pending
outcomes, and retention. [Briefs and citations](briefs.md) describes the saved
evidence and model-usage fields.
