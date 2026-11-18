# Usage reporting

The report counts tool attempts from audit receipts and model tokens from saved briefs.

These commands report usage from the default renewal-demo folder. The
placeholder takes the generated brief ID:

```sh
gtm-connector usage --state-dir var/renewal-demo
gtm-connector usage --brief brief-REPLACE_WITH_ID --state-dir var/renewal-demo
```

The default output shows tool counts and model-token totals as tables. Add
`--json` to read every group and saved-brief entry described below:

```sh
gtm-connector usage --json --state-dir var/renewal-demo
```

The `--state-dir PATH` option selects another state directory. The command reads
local files without changing them and runs outside MCP authorization. The report
excludes arguments and source text. Missing state creates no files.

## Filter and follow usage

Usage accepts the same receipt filters as audit: `--tool`, `--caller`,
`--status`, `--since`, and `--until`, alongside `--brief`. Filters combine with
AND. Caller must be a complete lowercase SHA-256 fingerprint or `anonymous`.
Status is `ok`, `error`, or `pending`. Times require an explicit UTC offset;
`--since` includes the start and `--until` excludes the end, using the receipt's
start time. See [receipt filters](audit.md#filter-and-follow-receipts).

```sh
gtm-connector usage --state-dir var/session --tool get_account --status ok
gtm-connector usage --state-dir var/session --since 2026-11-17T00:00:00Z --until 2026-11-18T00:00:00Z --json
gtm-connector usage --state-dir var/session --follow --json
```

When receipt filters are active, the JSON report adds a `filters` object with
the selected values. Its times are normalized to UTC with `Z`. The existing
`brief_id` field records `--brief` separately.

`--follow` emits the initial summary and then only changed summaries. With
`--json`, each line is one complete report with the same shape as a one-time
report. Receipt changes and saved-brief changes can produce a new report;
unchanged totals and entries do not repeat. Polls run every half second and
hold no database connection while waiting. Ctrl-C exits `130` without changing
state.

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

With receipt filters, a saved brief contributes model tokens if at least one
of its receipts matches. Its complete model usage counts once, even when only
one of several calls matches a tool, caller, status, or time window. Tokens
are not apportioned to individual tool calls. Its `audit_calls` count includes
only matching receipts. If no receipts match, no saved brief contributes model
tokens. Without receipt filters, saved briefs with no surviving receipts still
contribute as before. `--brief` selects that saved brief directly.

[Audit, rate limits, and writes](audit.md) explains receipt contents, pending
outcomes, and retention. [Briefs and citations](briefs.md) describes the saved
evidence and model-usage fields.
