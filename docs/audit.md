# Audit, rate limits, and writes

An audit receipt is a saved row describing a tool attempt and its outcome.
For each recognizable `tools/call` attempt, the gateway commits one durable
`pending` row before execution. Success or rejection finalizes that same row.

Receipts identify the tool, arguments, request and brief IDs, and token's
SHA-256 fingerprint. They record UTC time, elapsed time, HTTP status, and the
count and references of returned records. The fingerprint is not a reusable
bearer token. Arguments can contain activity text; raw response bodies are not
copied into the log.

The receipt guarantee excludes unparseable traffic with no tool-call marker
and requests that disconnect before reaching execution.

The [generated-sequence test](../tests_stateful/test_audit_receipts.py) checks
receipt ordering and repeated finalization across calls and injected audit
failures. It reads committed state through a separate SQLite connection before
source access. This supplements the fixed tests; it does not prove crash durability
or every possible concurrent schedule. See [development checks](development.md)
to run it.

## Audit failures

If the first audit write fails, the tool does not run. A finalization failure
returns HTTP 503. The server then tries to finalize an error receipt, leaving
the row pending if that attempt also fails.

An activity may already have committed when this happens. Retry with the same
idempotency key and reconcile any pending row. The log is neither tamper-proof
evidence nor a billing system.

The audit command lists receipts for a brief. It runs from the repository root
after setup. Replace the placeholder with the generated brief ID:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
```

The default output is a receipt table. Add `--json` for the complete receipt
array, including arguments and returned record references. Omitting `--brief`
lists all receipts; `--state-dir PATH` selects another state directory.
The audit command opens SQLite read-only without initializing tables or changing
permissions. A missing database or receipt schema is an error.
[Inspection tests](../tests/test_audit_reader.py) cover these cases;
[Usage reporting](usage.md) provides a read-only summary.

## Rate limits

Each token has a default allowance of 60 tool calls per fixed 60-second window.
Scope denials count; invalid tokens create no counters. A limited call returns
HTTP 429 with `rate_limited` and `Retry-After`. The demo uses a limit of four.

The counters live in process memory, so a new token or server restart resets
the allowance. This local guard does not enforce a distributed quota.

## Idempotent writes

`log_activity` requires an account ID, kind, body, and idempotency key. Retrying the
same key and payload returns the original activity; changing the payload returns
`idempotency_conflict`. Keys are unique across this local CRM. Record contents
cannot trigger writes, and there are no update or delete tools.

## Retention

Runtime directories use mode 0700, and files use mode 0600. Stored data does not
expire automatically: remove the state directory when you no longer need its
fixtures, audit arguments, or local credentials. The
[security review](../SECURITY_REVIEW.md) covers failure modes and sign-off evidence.
