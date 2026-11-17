# Record the walkthrough

The README recording runs the fixture demo, shows three cited lines from
`acc-003`, refuses a call-note read without its scope, and rejects an appended
uncited sentence. The final frame shows the six tools returned by the official
Inspector CLI. It displays their names and scopes; the full response is saved
separately. No model, real CRM, Inspector UI, or interactive login is involved.

The session script runs real commands and checks their outcomes before showing
selected fields. It adds reading pauses. Playback caps idle periods at twelve
seconds, so installation and test waits do not determine the recording length.
The recording is a walkthrough, not a performance measurement.

## Regenerate it

Use macOS or Linux with Python 3.11+, Node and npx as described in the
[Inspector guide](inspector.md), asciinema 3.2.1, and agg 1.9.0. These are optional
documentation tools. The default demo and CI do not need the recording tools.
On macOS, the tools are available from Homebrew:

```sh
brew install asciinema agg
```

From the repository root, run:

```sh
./scripts/record-demo.sh
```

The script installs the existing Python requirements and downloads the pinned
Inspector package if it is not cached. It then runs `./scripts/demo.sh` in fresh
fixture state. The recording script controls what reaches the terminal. It does
not capture the desktop, a user's shell prompt, or keyboard input.

Generated files live in `docs/media/`:

| File | Contents |
| --- | --- |
| `walkthrough.gif` | The terminal recording rendered for GitHub. |
| `walkthrough.cast` | The original timed terminal output, with an idle limit. |
| `inspector-cli.gif` | The last terminal frame, showing the CLI tool listing. |
| `inspector-tools.json` | The validated, full Inspector `tools/list` response. |
| `recording-session.txt` | The displayed text without terminal clear sequences. |
| `recording-provenance.json` | Source revision, commands, exits, times, versions, and artifact hashes. |

The capture script keeps raw logs, fixture databases, and credentials under
private, gitignored `var/` directories. Only selected output is published. The
renderer checks the text artifacts for credential patterns, issued credentials
still in local config files, and local home paths before rendering. Review the
transcript and both images before committing. Do not add the private directories.
Retention remains manual, as described in the [security review](../SECURITY_REVIEW.md).

If a command or assertion fails, generation stops. Read the private logs under
`var/recording-*` and `var/capture.*`; do not paste them into an issue or recording.
Check the Python dependencies, Node version, and cached Inspector installation.
Fix the failure and rerun the script. A failed run can leave partial media files,
so do not publish them as a completed recording.

To replay the original capture locally:

```sh
asciinema play docs/media/walkthrough.cast
```
