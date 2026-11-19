# Install the command

```sh
pipx run --spec 'git+https://github.com/pwngh/gtm-connector.git@main' gtm-connector demo
pipx install 'git+https://github.com/pwngh/gtm-connector.git@main'
```

The first line runs the fixture demo without a checkout. The second keeps
`gtm-connector` on your PATH.

- You need Python 3.11 or newer on macOS or Linux, pipx, and Git with
  permission to read the repository. The first run downloads the dependencies.
- The demo uses local HTTPS services and invented data. It needs no model,
  Salesforce login, or external service. It writes private state under
  `var/renewal-demo` in the current directory unless `--state-dir PATH` is
  given.
- Replace `main` with a reviewed commit to pin the source. pipx installs the
  versions pinned in `requirements.in` but does not apply the artifact hashes
  in `requirements.lock`.
- `python -m gtm_connector` is the same CLI, run from the environment where the
  package is installed.

## Work from a checkout

```sh
./scripts/demo.sh
./scripts/five-minute-demo.sh
```

Both scripts install the dependencies with the checked-in hashes, run the test
suite, and run the renewal demo on invented fixtures. The second also inspects
that brief's usage and runs its citation checks. `gtm-connector verify` is the
separate guarantee check for scopes, retries, and rate limits.

CI builds the wheel, installs it, and runs `demo`, `verify`, and `evaluate`
from a directory outside the checkout, on Python 3.11 and 3.14. `evaluate`
works there because the package includes its expected claims.
