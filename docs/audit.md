# Audit, rate limits, and writes

For each recognizable `tools/call` attempt, the gateway commits one durable
`pending` audit row before execution. It finalizes that same row after success
or rejection.

Each receipt records the token's SHA-256 fingerprint, tool, arguments, UTC time,
request and brief IDs, HTTP status, elapsed time, and the count and references
of returned records. The fingerprint cannot be reused as a bearer token.
Arguments can contain activity text. The log does not copy raw response bodies.

The receipt guarantee excludes unparseable traffic with no tool-call marker
and requests that disconnect before reaching execution.

## Audit failures

If the first audit write fails, the tool does not run. If finalization fails,
the server returns HTTP 503 and leaves the receipt pending. An activity may
already have committed, so retry with the same idempotency key. The pending row
still needs reconciliation. The log is not tamper-proof and is not a billing
system.

To inspect the receipts for one brief, run this from the repository root after
setup. Replace the placeholder with the generated brief ID:

```sh
.venv/bin/python -m gtm_connector audit --brief brief-REPLACE_WITH_ID
```

Omit `--brief` to inspect all receipts. Use `--state-dir PATH` for another state
directory. The audit command opens the store through its initializer. It can
set the database file's permissions to 0600 and create a missing `audit_log`
table in an existing database file. It is not a strictly read-only command.
[Usage reporting](usage.md) provides a read-only summary.

## Rate limits

The default limit is 60 tool calls per token in each fixed 60-second window.
Scope denials count toward the limit. Invalid tokens do not create counters.
A limited call returns HTTP 429 with `rate_limited` and `Retry-After`. The demo
uses a limit of four to show that response.

Limits live in process memory. A new token or server restart resets the
allowance. This is a local guard, not a distributed quota.

## Idempotent writes

`log_activity` requires an account ID, kind, body, and idempotency key. The same
key and payload return the original activity. Reusing the key with a different
payload returns `idempotency_conflict`. Keys are unique across this local CRM.
There are no update or delete tools. Record contents cannot trigger a write.

## Retention

Runtime directories use mode 0700, and files use mode 0600. Data does not expire
automatically. Remove the state directory when you are done with its fixtures,
audit arguments, and local credentials. See the
[security review](../SECURITY_REVIEW.md) for failure modes and sign-off evidence.
