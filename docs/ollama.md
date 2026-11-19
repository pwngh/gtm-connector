# Local model drafting

```sh
gtm-connector brief acc-003 --adapter ollama --model llama3.2:latest --json
gtm-connector evaluate --adapter ollama --model llama3.2:latest --json > ollama-evaluation.json
```

With `--adapter ollama`, a local model assembles the renewal brief. The
citation checker must accept the draft before it is published. The default
demos and CI use the fixture drafter and no model.

- You need a running Ollama 0.30.0 or newer and a model that `ollama list`
  already shows. No signup or key is needed.
- The adapter calls only `http://127.0.0.1:11434`. It does not download a
  model, fall back to fixture output, or call a hosted model.
- `--model` is required with `--adapter ollama` and is an error without it.
- Invalid arguments exit `2`. An unavailable service, a missing model, or
  rejected model metadata exits `5` before any brief state is created.
- A rejected draft exits `4`. `brief` prints no candidate text. The counts and
  fixed error codes stay in the private saved artifact under
  `draft_diagnostics` and `model_usage`. `evaluate` still writes its report.
  See [exit codes](cli.md) and [scoring](evaluation.md).

## What the model does

The model receives the supported claims derived from authorized evidence. It
must return them with their exact wording and references in a JSON `lines`
array. The prompt holds no owner credential, bearer token, audit fingerprint,
or tool interface. Instructions quoted from a source stay data.

- The model cannot change the reads or scopes, resolve a conflict, or run an
  action. The adapter does not repair or replace its output.
- An unsupported line, a duplicate, or an omitted required disclosure rejects
  the whole brief. A supported subset can pass.
- Each request adds Ollama's `:local` selector, so the server rejects a tag
  that now points to a remote model before it forwards a prompt. The adapter
  also rejects cloud suffixes and remote metadata, disables environment
  proxies, and follows no redirects.
- The request uses temperature `0`, seed `0`, an 8192-token context, and a
  4096-token generation limit. Generation has a 180-second read timeout and no
  retries.

## Measurements and limits

No model run has been measured on the current fixture book, and the fixture
scores say nothing about model performance.

- Results can vary with model files, hardware, and service version. Keep the
  local tag unchanged during a run.
- Token counts include rejected generations and are not billing measurements.
- The checker does not establish semantic truth. Zero rejected lines is not
  evidence that hallucinations were caught.
- You must trust the local service and its model metadata. The adapter's checks
  cannot sandbox a malicious replacement service.
