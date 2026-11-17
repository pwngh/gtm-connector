# Usage reporting

After setup, run these commands from the repository root. Replace the placeholder
with a generated brief ID:

```sh
.venv/bin/python -m gtm_connector usage
.venv/bin/python -m gtm_connector usage --brief brief-REPLACE_WITH_ID
```

Use `--state-dir PATH` for another state directory. This command reads local
files without changing them. It runs outside MCP authorization and excludes
arguments and source text from the report. Missing state creates no files.

## Tool calls

Receipts are grouped in `by_token`, `by_tool`, `by_day`, and
`by_token_tool_day`. Days use UTC boundaries. Each group and the overall `calls`
field contain `total`, `ok`, `error`, and `pending` counts. Scope denials,
limited calls, and retries each count as an attempt.

Token groups use fingerprints of presented credentials, including rejected
ones. Missing credentials use `anonymous`. A group does not establish who made
the call or whether the credential was valid. These counts are not billing data.

## Model usage

`briefs` lists each saved brief's status, matching `audit_calls`, and
`model_usage`. The `model_tokens` totals cover saved files only. Fixture counts
are zero. Unknown counts stay `null`, including in the corresponding total.
`unknown_briefs` reports how many briefs have that gap. Deleted files cannot
supply usage. Ollama artifacts include reported counts for rejected drafts too.
A malformed or partial saved result stops the report with a clear
error.

See [Audit, rate limits, and writes](audit.md) for receipt contents, pending
outcomes, and retention. [Briefs and citations](briefs.md) describes the saved
evidence and model-usage fields.
