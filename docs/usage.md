# Usage reporting

```sh
gtm-connector usage --state-dir var/renewal-demo
gtm-connector usage --state-dir var/renewal-demo --brief brief-REPLACE_WITH_ID --json
gtm-connector usage --state-dir var/session --tool get_account --status ok
gtm-connector usage --state-dir var/session --since 2026-11-17T00:00:00Z --follow --json
```

The report counts tool attempts from audit receipts and model tokens from saved
briefs. The command only reads local files and runs outside MCP authorization.
The report holds no arguments or source text, and it is not billing data.

| Option | Rule |
| --- | --- |
| `--brief ID` | Selects one brief or run. |
| `--tool NAME` | Matches the exact tool name. |
| `--caller VALUE` | A complete lowercase SHA-256 fingerprint, or `anonymous`. |
| `--status VALUE` | `ok`, `error`, or `pending`. |
| `--since TIME`, `--until TIME` | ISO 8601 with a UTC offset, compared with the receipt's start time. `--since` includes the start and `--until` excludes the end. |
| `--follow` | Prints the first report, then a report each time one changes. It polls every half second, and Ctrl-C exits `130`. |

Filters combine with AND. [`audit`](audit.md) accepts the same options.

## JSON report

| Field | Content |
| --- | --- |
| `brief_id`, `filters` | The `--brief` value, and the other active filters with times in UTC. `filters` is absent when none is set. |
| `calls` | `total`, `ok`, `error`, and `pending` counts. |
| `by_token`, `by_tool`, `by_day`, `by_token_tool_day` | The same counts for each group. Days are UTC. |
| `briefs` | One entry per saved brief, with `status`, `audit_calls`, and `model_usage`. |
| `model_tokens` | `saved_briefs`, `unknown_briefs`, `input_tokens`, and `output_tokens`. |

- A scope denial, a rate-limited call, and a retry each count as one attempt.
- A token group is a fingerprint of the presented credential, valid or not. A
  missing credential is `anonymous`. A group does not identify a caller or
  prove a grant.
- Model totals cover saved brief files only. The fixture and extractive
  formatters record zero tokens. Ollama counts include rejected drafts. An
  unknown count is `null`, and `unknown_briefs` counts those briefs.
- A malformed saved brief stops the report with an error.
- With receipt filters, a saved brief counts only if one of its receipts
  matches. Its whole model usage then counts once, and `audit_calls` counts only
  the matching receipts. Without filters, a saved brief with no receipts still
  counts.

See [audit](audit.md) for receipts and [briefs](briefs.md) for saved evidence.
