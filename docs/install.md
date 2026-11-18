# Install the command

The command needs Python 3.11 or newer on macOS or Linux. With pipx and Git
installed, this line downloads the repository into pipx's isolated environment
and runs the fixture demo. You do not need a working checkout or a published
package:

```sh
pipx run --spec 'git+https://github.com/pwngh/gtm-connector.git@main' gtm-connector demo
```

Git must already have permission to read the repository. The first run downloads
Python dependencies. The fixture demo uses local HTTPS services and invented
data; it needs no model, Salesforce login, or external service at runtime.
It writes private state under `var/v2` in the current directory. Pass
`--state-dir PATH` to choose another location.

Replace `main` in the URL with a reviewed commit to choose an exact source
revision. The package keeps the runtime versions from `requirements.in`.
The pipx path uses those version pins, but it does not apply the artifact hashes
in the repository's lock file.

For a command that stays on your PATH:

```sh
pipx install 'git+https://github.com/pwngh/gtm-connector.git@main'
gtm-connector --help
```

Both the installed command and `python -m gtm_connector` use the same CLI. The
Python invocation must use the interpreter in the environment where the package
is installed. Evaluation includes its fixed expected claims in the package, so
`gtm-connector evaluate --json` also works outside a checkout.

## Work from a checkout

The existing scripts remain available. They install dependencies using the
checked-in hashes and run the fixture checks:

```sh
./scripts/demo.sh
./scripts/five-minute-demo.sh
```

The package build uses the same setuptools version already pinned in the
development lock. It adds no runtime dependency. CI installs the wheel and runs
the demo and evaluation from a directory outside the checkout, on Python 3.11
and 3.14.
