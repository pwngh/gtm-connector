# Local model drafting

Ollama is optional. The default demos and CI use the fixture adapter and require
no inference service or model download. This adapter calls only
`http://127.0.0.1:11434`. It does not call a hosted model, pull a missing model,
or fall back to fixture output.

## Run

Start your existing Ollama 0.30.0 or newer service. Choose a model already listed by
`ollama list`. The measured run uses `llama3.2:latest`. After repository setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --adapter ollama --model llama3.2:latest --json
.venv/bin/python -m gtm_connector evaluate --adapter ollama --model llama3.2:latest > evaluation/RESULTS_OLLAMA.json
```

`--model` is required with `--adapter ollama`. It is an error with the default
fixture adapter. If Ollama is unavailable, the model is missing, or its metadata
identifies a cloud model, the command stops with a clear error. It creates no
brief state on this preflight failure. No signup or key is needed.

The `brief` command prints an accepted artifact only. Rejection exits `2` and
prints no candidate text. Inspect `draft_diagnostics` and `model_usage` in the
private saved artifact for counts and fixed error codes. The `evaluate` command
writes its report even when it exits `2` for incomplete coverage or rejection.
That exit status is a measured outcome, not permission to publish a failed draft.

## What the model does

The model assembles the supported claim catalog derived from authorized MCP
evidence. It must preserve exact wording and source references. The prompt
contains no owner credential, bearer token, audit fingerprint, or tool interface.
Quoted source instructions remain data. It returns a JSON `lines` array.

This is constrained claim assembly, not unrestricted summarization. The model
does not choose reads, change scopes, resolve conflicting sources, or execute
an action. The adapter does not repair or replace its output. One unsupported
line, duplicate, or omitted mandatory disclosure rejects the whole brief.
Passing a subset does not prove complete coverage.

Each generation adds Ollama's `:local` selector. The server rejects a model tag
that now points to a remote model before forwarding any prompt. The report
preserves the actual returned model identifier, including that selector when
echoed. The recorded digest is the preflight identity; keep the local model tag
unchanged during a run. The adapter also rejects remote metadata in a response.

The request sets temperature `0`, seed `0`, an 8192-token context, and a
4096-token generation limit. Generation has a 180-second read timeout. There
are no retries. Results can vary with model files, hardware, and service version.

## Measurements and limits

The [recorded model report](../evaluation/RESULTS_OLLAMA.json) contains the actual
command, UTC timestamps, requested and returned model names, cached digest,
Ollama version, token counts, and per-account metrics. The
[fixture report](../evaluation/RESULTS.json) remains a separate baseline.

`candidate` counts actual returned claim lines before checking. Malformed or
unavailable candidates are explicitly unscored. Published counts include only
whole briefs that passed. Rejected text stays in memory and is not included in
the report. Reported input/output tokens include rejected generations; missing
counts remain `null`. These are service reports, not billing measurements.

An unsupported line can be a paraphrase rather than a false claim. The checker
does not establish semantic truth. A run with no rejected lines is not evidence
that hallucinations were caught. If no lines are published, the published
invention rate is `null`, not a perfect score. See [scoring rules](evaluation.md).

The local service and model metadata are trusted operator software. The adapter
rejects cloud suffixes and remote-model metadata, disables environment proxies,
and follows no redirects. Those checks do not sandbox a malicious replacement
service. Existing snapshot, retention, and file-owner limits still apply.

API references read on 2026-11-17:
[chat](https://docs.ollama.com/api/chat),
[structured output](https://docs.ollama.com/capabilities/structured-outputs),
and [model details](https://docs.ollama.com/api-reference/show-model-details).
The per-call local selector is checked against the
[Ollama 0.30.0 source](https://github.com/ollama/ollama/blob/v0.30.0/internal/modelref/modelref.go).
