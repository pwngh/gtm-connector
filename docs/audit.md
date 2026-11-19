# Audit, rate limits, and writes

An audit receipt is a saved row for one tool attempt and its outcome.

```sh
gtm-connector audit --brief brief-REPLACE_WITH_ID --state-dir var/renewal-demo
gtm-connector audit --state-dir var/session --tool get_account --status error
gtm-connector audit --state-dir var/session --since 2026-11-17T00:00:00Z --until 2026-11-18T00:00:00Z
gtm-connector audit --state-dir var/session --caller anonymous --follow --json
```

| Flag | Selects |
| --- | --- |
| `--state-dir PATH` | The state folder. The default is `var/v2`. |
| `--brief ID`, `--tool NAME` | One brief or run, or one exact tool name. |
| `--caller VALUE` | A complete lowercase SHA-256 fingerprint from a receipt, or `anonymous`. Never a bearer token. |
| `--status` | `ok`, `error`, or `pending`. |
| `--since`, `--until` | Receipts started at or after `--since` and before `--until`, in ISO 8601 with `Z` or a UTC offset. `--since` must be the earlier one. |
| `--follow` | Current matches, then new or changed ones, until Ctrl-C, which exits `130`. |
| `--json` | The receipt array, or with `--follow` one receipt object per line. |

Filters combine with AND. The command opens the database read-only and
creates nothing. A missing database or receipt table is an error.

`--follow` polls every half second and can miss a change made and reversed
between polls. A pending receipt and its final version share an `audit_id`
and can both appear. `--status pending` never prints the later `ok` version.

A receipt has `audit_id`, `started_at`, `token_id`, `tool`, `arguments`,
`request_id`, `brief_id`, `status`, `http_status`, `row_count`, `rows`,
`elapsed_ms`, and `error`. `token_id` is the SHA-256 fingerprint of the
presented bearer token, or `anonymous`. It identifies a presented credential,
even a rejected one, not a person or a valid grant. `rows` holds record
references. Arguments can contain activity text. Response bodies are not
stored. [Usage reporting](usage.md) summarizes the same receipts.

## What is recorded

For each recognizable `tools/call` attempt, the gateway commits one `pending`
row before the tool runs. Success or rejection finalizes that same row.

- If the first audit write fails, the tool does not run and the response is
  HTTP 503.
- If finalizing fails, the response is HTTP 503 and no result is released.
  The row stays `pending` if the error cannot be recorded either.
- After a 503 on `log_activity`, the activity may already exist. Retry with
  the same idempotency key and reconcile any pending row.
- Unparseable traffic with no tool-call marker and requests that disconnect
  before execution get no receipt.
- The log is not tamper-proof evidence or a billing system, and crash
  durability is not proven.

## Rate limits, writes, and retention

- Each token gets 60 tool calls per fixed 60-second window. Scope denials
  count, and invalid tokens do not. A limited call returns HTTP 429 with
  `rate_limited` and `Retry-After`. `verify` uses a limit of four.
- The counters live in process memory. A new token or a restart resets them.
- `log_activity` requires an account ID, kind, body, and idempotency key. The
  same key and payload return the original activity. The same key with a
  different payload returns `idempotency_conflict`.
- Record contents cannot trigger a write. There are no update or delete tools.
- Runtime directories use mode 0700 and files use mode 0600. Nothing expires,
  so remove the state directory when you no longer need it. See the
  [security review](../SECURITY_REVIEW.md).
