# Development checks

Run these from the repository root:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install --require-hashes -r requirements-dev.lock
.venv/bin/python -m pip check
.venv/bin/python -m ruff check .
.venv/bin/python -m mypy
.venv/bin/python -m unittest discover -s tests -v
.venv/bin/python -m unittest discover -s tests_stateful -v
./scripts/five-minute-demo.sh
```

| Check | What it covers |
| --- | --- |
| `ruff check .` | Errors and lines over 120 characters in the application, `scripts/`, and `tests_stateful/`. It skips `tests/`. |
| `mypy` | Every module in `gtm_connector/`, including unannotated function bodies. It is not strict mode. |
| `tests_stateful` | Hypothesis sequences of tool calls and receipt retries against the real gateway and SQLite stores. A source read must see a committed pending receipt, and a receipt must not finalize twice. |
| `five-minute-demo.sh` | Installs the runtime lock, runs the suite in `tests/`, then runs the fixture walkthrough. |

CI runs these on Python 3.11 and 3.14 for pull requests and pushes to main, and
runs the [Inspector check](inspector.md) as a separate job. Every check uses
fixtures and needs no model or external account. The
[workflow](../.github/workflows/test.yml) defines the commands.

Record a design choice or a limit in the description of the pull request that
makes it.

## Dependency locks

`requirements.lock` pins the runtime versions and artifact hashes, and the demo
scripts install it. `requirements-dev.lock` adds Ruff, mypy, Hypothesis, and
pip-tools to the same runtime versions. The `.in` files hold the chosen
versions.

- Hash checking rejects an artifact that does not match the lock. It does not
  audit upstream code.
- To change a dependency, edit the `.in` file and regenerate with Python 3.14,
  using the command in each lock's header. Set `CUSTOM_COMPILE_COMMAND` to that
  full command for a byte-identical header.
- Review every changed version and hash, then run the checks on both CI Python
  versions. A lock generated on one interpreter can miss a conditional
  dependency.
