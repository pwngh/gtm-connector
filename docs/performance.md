# Performance measurements

Run all measurements with the repository's existing environment:

```sh
.venv/bin/python scripts/benchmark.py
```

The script prints a JSON report to stdout and progress to stderr. Use `--output
report.json` to save a copy. `--samples` sets the process and certificate sample
count; `--calls` accepts 1 through 50 measured calls after five warmups. The
small CI run uses `--samples 1 --calls 1`. No timing threshold gates CI.

## Recorded before and after

Measured on November 17, 2026 in America/Chicago, November 18 in UTC, on an Apple
M1 Max with 10 logical CPUs and 32 GiB RAM. The machine ran macOS 26.6.2
(Darwin 25.6.0), CPython 3.14.6, cryptography 50.0.2 with OpenSSL 4.0.3, and
Python's ssl module with OpenSSL 3.6.4. This was a shared development machine.
The benchmark runs were sequential, with no concurrent repository tests.

Before: `62a394236f0ff0d5da44247613337e72763a493e`, which generated RSA-2048
certificates. After: `264467a22593549d428a9439f7719f09fbff35b2`, which generates
P-256 certificates. Both source checkouts were clean. The same script,
interpreter, dependency environment, and sample counts measured both commits.
The script SHA-256 is
`1bd1669b983439115a8827fcf262a58a6b6565543393a2be0f19051cef6f7721`.

All times below are milliseconds. Raw samples, UTC timestamps, and provenance
are in [before.json](performance/before.json) and [after.json](performance/after.json).

| Measurement | Samples per run | Before p50 | Before p95 | After p50 | After p95 |
| --- | ---: | ---: | ---: | ---: | ---: |
| `--version` process | 20 | 73.743 | 80.808 | 77.941 | 81.521 |
| `--help` process | 20 | 75.287 | 78.269 | 78.009 | 82.625 |
| Fixture `brief` process | 20 | 1065.558 | 1111.369 | 1017.862 | 1044.081 |
| Server time to ready | 20 | 720.697 | 795.226 | 637.399 | 655.146 |
| Certificate generation and writes | 20 | 47.193 | 106.850 | 0.439 | 2.364 |
| Complete audited HTTPS call | 50 | 4.243 | 6.643 | 3.895 | 6.179 |
| Direct SQLite read | 50 | 0.311 | 0.452 | 0.321 | 0.522 |
| HTTPS call minus paired direct read | 50 | 3.956 | 6.370 | 3.575 | 5.885 |

The fix replaces repeated RSA key generation with a fresh P-256 key. Certificate
generation fell from 47.193 to 0.439 ms at p50. The complete fixture brief fell
from 1,065.558 to 1,017.862 ms, and server readiness from 720.697 to 637.399 ms.
Version and help code did not change; their slightly higher observations show
why these samples are descriptive, not a promise for another run. The change
does not alter the steady-state call path, so the call differences are not a
claimed optimization. No dependency was added.

## What the timers include

- Each version, help, and brief observation starts a new Python process. This
  is process cold start; operating-system filesystem caches are not cleared.
  It does not measure dependency installation. Every brief uses fresh fixtures
  and includes startup, output, and normal server shutdown.
- Server readiness runs `serve --json` with fresh state and stops at the parsed,
  flushed ready event. Cleanup follows outside that timer.
- Certificate timing calls the repository's actual certificate function after
  imports, including key generation, signing, and both private file writes.
- Each HTTPS call uses `get_account` for the same invented account through a
  granted `crm:read` token. Its timer includes response JSON decoding, the
  source read, and both audit commits. Authorization and five warmups precede
  the 50 measured calls. The same client reuses its connection.
- Each direct read opens and closes a read-only SQLite connection and calls the
  same account reader after its paired HTTPS call. Connector overhead is the
  per-sample HTTPS duration minus that direct duration. It includes transport,
  policy, serialization, and audit work. Negative differences would be retained.
  The direct read can benefit from the preceding read's cache state.
- Receipt verification follows both timers through an independent read-only
  connection. Both runs observed 55 distinct committed successful receipts,
  including warmups, and zero pending receipts. The audit row's `elapsed_ms`
  is not the benchmark timer because that value precedes the final commit.

The median is p50. The p95 is the sorted sample at rank `ceil(0.95 * count)`.
The report preserves every observation. A single run does not characterize
contention, a large database, a different machine, or Salesforce latency.
Use `--source PATH` to run this same script against another checkout with the
same interpreter and dependencies. Keep the source unchanged during each run.

## What CI holds

[Runtime tests](../tests/test_runtime.py) require fresh P-256 keys, matching
self-signed certificates, the loopback subject alternative name, private file
permissions, and successful explicitly trusted HTTPS. The
[CLI import tests](../tests/test_cli_contract.py) keep help and version independent
of server, MCP, HTTP, and SQLite imports. The
[benchmark smoke test](../tests/test_benchmark.py) checks metric/sample structure
and committed receipts. Existing generated audit sequences and official
Inspector integration continue to run. These checks preserve the measured
mechanisms without rejecting a busy runner for taking longer.
