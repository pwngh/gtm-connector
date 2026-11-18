# Development checks

The project runs linting and type checks alongside its behavioral tests. Install
the development lock from the repository root:

```sh
python3 -m venv .venv
.venv/bin/python -m pip install --require-hashes -r requirements-dev.lock
.venv/bin/python -m pip check
.venv/bin/python -m ruff check .
.venv/bin/python -m mypy
```

Ruff checks the application, scripts, and stateful tests for errors and lines over
120 characters. The Inspector script has one import-order exception because it adds
the checkout root before importing the application. Mypy checks all application
modules, including function bodies without annotations. It is not strict mode;
unannotated interfaces and dynamic JSON still use `Any`. One targeted suppression
covers an SDK protocol hook for an authorization grant this issuer does not offer.

The test suite runs separately from the static checks. Run it with the full
fixture walkthrough:

```sh
./scripts/five-minute-demo.sh
```

The generated receipt test requires the development lock. Run it separately:

```sh
.venv/bin/python -m unittest discover -s tests_stateful -v
```

Hypothesis generates sequences of tool calls and receipt retries. The test uses
the real gateway and SQLite stores, with invented credentials. It checks that
source access sees a committed pending receipt and that a receipt cannot finalize
twice. The default demo does not install Hypothesis or discover this separate suite.

CI runs the checks on Python 3.11 and 3.14 for pull requests and pushes to main.
It runs generated sequences as well as the separate Inspector check. These checks
use fixtures and require no model or external account. The
[workflow](../.github/workflows/test.yml) defines the commands.

## Dependency locks

The runtime lock pins package versions and artifact hashes. The default demo
installs it without development tools. The development lock includes the same
runtime versions, Ruff, mypy, Hypothesis, and the lock generator. Installation
needs internet or a populated local cache. Hash checking rejects an artifact
that does not match the lock. It does not audit upstream code or fully pin the
build environment when pip needs to build a source distribution.

Edit the input files deliberately, then regenerate with Python 3.14 and the
pinned pip-tools from the development environment. Use the command in each
lock's header. For a byte-identical header, set `CUSTOM_COMPILE_COMMAND` to the full
command shown in that header before running it. Review every changed
version and hash before committing. Run installation and checks on both CI Python
versions; a lock generated on one interpreter can miss a conditional dependency.

The lock inputs record the chosen versions. The reasons and static check
boundaries are recorded in [DECISIONS.md](../DECISIONS.md).
