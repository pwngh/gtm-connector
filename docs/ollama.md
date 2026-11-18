# Local model drafting

Ollama lets a local model assemble a renewal brief. The citation checker must
accept it before publication.

The default demos and CI use fixed drafting (the fixture adapter), with no
inference service or model download. The optional adapter calls only
`http://127.0.0.1:11434`. It neither downloads missing models nor falls back to
fixture output. It calls no hosted model.

## Run

These commands require a running Ollama 0.30.0 or newer service and a model
already shown by `ollama list`. The measured run uses `llama3.2:latest`.
The commands run after repository setup:

```sh
.venv/bin/python -m gtm_connector brief acc-003 --adapter ollama --model llama3.2:latest --json
.venv/bin/python -m gtm_connector evaluate --adapter ollama --model llama3.2:latest --json > evaluation/RESULTS_OLLAMA.json
```

`--model` is required with `--adapter ollama` and is an error with the fixture
adapter. Invalid arguments exit `2`. An unavailable service, missing model, or
rejected model metadata fails preflight with exit `5`, before brief state is
created. No signup or key is needed.

`brief` prints only accepted output. A failed evidence check exits `4` without
candidate text; counts and fixed error codes remain in the private saved artifact
under `draft_diagnostics` and `model_usage`. By contrast, `evaluate` writes its
report even when rejection or incomplete coverage makes it exit `4`. Use `--json`
when saving that report for scripts. Without it, the CLI prints a readable summary.
Errors and recovery commands go to stderr. That measured failure does not permit
publication of a failed draft. See [the CLI exit codes](cli.md).

## What the model does

The model receives supported claims derived from authorized MCP evidence. It
must preserve their exact wording and references in a JSON `lines` array.
The prompt contains no owner credential, bearer token, audit fingerprint, or
tool interface. Quoted source instructions remain data.

This is constrained claim assembly, not unrestricted summarization. The model
cannot change the chosen reads or scopes, resolve conflicts, or execute an action.
The adapter neither repairs nor replaces its output. An unsupported line,
duplicate, or omitted mandatory disclosure rejects the whole brief. A supported
subset can pass without proving complete coverage.

Each generation adds Ollama's `:local` selector. If the tag now points to a remote
model, the server rejects it before forwarding a prompt. The report preserves
the returned identifier, including that selector when echoed. Its digest records
preflight identity, so keep the local tag unchanged during a run. The adapter
also rejects remote metadata in responses.

The request uses temperature `0`, seed `0`, an 8192-token context, and a
4096-token generation limit. Generation has a 180-second read timeout and no
retries. Results can vary with model files, hardware, and service version.

## Measurements and limits

The [recorded model report](../evaluation/RESULTS_OLLAMA.json) records the actual
command and UTC timestamps. It includes requested and returned model names,
cached digest, Ollama version, token counts, and per-account metrics. The
[fixture report](../evaluation/RESULTS.json) remains a separate baseline.
Those reports preserve the commands used before the CLI required `--json` for
JSON output; their recorded invocation and results have not been rewritten.

`candidate` counts returned lines before checking. Malformed or unavailable
candidates are explicitly unscored; published counts include only whole briefs
that passed. Rejected text stays in memory, outside the report. Input/output
tokens include rejected generations, with missing counts left as `null`.
These service reports are not billing measurements.

An unsupported paraphrase need not be false: the checker does not establish
semantic truth. No rejected lines means no evidence that hallucinations were
caught. With no published lines, invention rate is `null`, not a perfect score.
See [scoring rules](evaluation.md).

The operator must trust the local service and its model metadata. The adapter
rejects cloud suffixes and remote metadata, disables environment proxies, and
follows no redirects. Those checks cannot sandbox a malicious replacement service.
Existing snapshot, retention, and file-owner limits still apply.

This guide uses API references read on 2026-11-17:
[chat](https://docs.ollama.com/api/chat),
[structured output](https://docs.ollama.com/capabilities/structured-outputs),
and [model details](https://docs.ollama.com/api-reference/show-model-details).
The per-call local selector is checked against the
[Ollama 0.30.0 source](https://github.com/ollama/ollama/blob/v0.30.0/internal/modelref/modelref.go).
