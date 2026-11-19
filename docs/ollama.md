# Local model drafting

```sh
gtm-connector brief acc-003 --adapter ollama --model llama3.2:latest --json
gtm-connector evaluate --adapter ollama --model llama3.2:latest --json > ollama-evaluation.json
```

With `--adapter ollama`, a local model assembles the renewal brief, and the citation checker must
accept the draft before it is published. No model run has been measured on the current fixture
book.

- You need a running Ollama 0.30.0 or newer and a model that `ollama list` already shows.
- The adapter calls only `http://127.0.0.1:11434`. It does not download a model, fall back to
  fixture output, or call a hosted model.
- `--model` is required with `--adapter ollama` and is an error without it.
- An unavailable service, a missing model, or rejected model metadata exits `5` before any brief
  state is created.
- A rejected draft exits `4`. `brief` prints no candidate text. The counts and fixed error codes
  stay in the private saved artifact under `draft_diagnostics` and `model_usage`. `evaluate` still
  writes its report. See [scoring](evaluation.md).

## What the model does

- The model receives the supported claims derived from authorized evidence and must return them
  with their exact wording and references in a JSON `lines` array. The prompt holds no owner
  credential, bearer token, audit fingerprint, or tool interface.
- An unsupported line, a duplicate, or an omitted required disclosure rejects the whole brief. A
  supported subset can pass. The adapter does not repair or replace model output.
- Each request adds Ollama's `:local` selector, so the server rejects a tag that now points to a
  remote model. The adapter also rejects cloud suffixes and remote metadata, disables environment
  proxies, and follows no redirects. It cannot sandbox a malicious replacement service.
- The request uses temperature `0`, seed `0`, an 8192-token context, and a 4096-token generation
  limit. Generation has a 180-second read timeout and no retries.
- Token counts include rejected generations and are not billing measurements.
