# Audit, rate limits, and writes

An audit receipt is a saved row for one tool attempt and its outcome. It has `audit_id`,
`started_at`, `token_id`, `tool`, `arguments`, `request_id`, `brief_id`, `status`, `http_status`,
`row_count`, `rows`, `elapsed_ms`, and `error`. `token_id` is the SHA-256 fingerprint of the
presented bearer token, even a rejected one, or `anonymous`. `rows` holds record references.
`trace` shows the receipt behind any number on a [Deal card](briefs.md).

Saving a card adds one receipt that is not a tool attempt: `save_brief`, the card's seal. Its
`arguments` name the account and the card, its `rows` hold `briefs/<brief_id>/<SHA-256 of the saved
file>`, and its `brief_id` is empty, so `--brief ID` lists the card's reads only. `--tool save_brief`
lists the seals.

```sh
gtm-connector audit --brief brief-REPLACE_WITH_ID --state-dir var/renewal-demo
gtm-connector audit --caller anonymous --follow --json
```

| Flag | Selects |
| --- | --- |
| `--state-dir PATH` | The state folder. The default is `var/v2`. |
| `--brief ID`, `--tool NAME` | One card's reads or one run, or one exact tool name. |
| `--caller VALUE` | A complete lowercase SHA-256 fingerprint from a receipt, or `anonymous`. Never a bearer token. |
| `--status` | `ok`, `error`, or `pending`. |
| `--since`, `--until` | Receipts started at or after `--since` and before `--until`, in ISO 8601 with `Z` or a UTC offset. `--since` must be the earlier one. |
| `--follow` | Current matches, then new or changed ones, until Ctrl-C, which exits `130`. It polls every half second and can miss a change made and reversed between polls. A pending receipt and its final version share an `audit_id` and can both appear. `--status pending` never prints the later `ok` version. |
| `--json` | The receipt array, or with `--follow` one receipt object per line. |

Filters combine with AND. The command opens the database read-only and creates nothing. A missing
database or receipt table is an error.

## What is recorded

For each recognizable `tools/call` attempt, the gateway commits one `pending` row before the tool
runs. Success or rejection finalizes that same row.

- If the first audit write fails, the tool does not run and the response is HTTP 503.
- If finalizing fails, the response is HTTP 503 and no result is released. The row stays `pending`
  if the error cannot be recorded either.
- After a 503 on `log_activity`, the activity may already exist. Retry with the same idempotency
  key and reconcile any pending row.
- Unparseable traffic with no tool-call marker and requests that disconnect before execution get
  no receipt.

## Rate limits and writes

- Each token gets 60 tool calls per fixed 60-second window. Scope denials count, and invalid
  tokens do not. `verify` uses a limit of four.
- The counters live in process memory. A new token or a restart resets them.
- `log_activity` requires an account ID, kind, body, and idempotency key. The same key with a
  different payload returns `idempotency_conflict`. There are no update or delete tools.
