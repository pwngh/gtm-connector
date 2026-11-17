# Record the walkthrough

The README recording shows the connector before a reader installs it. It runs
the fixture demo on invented data, then shows three cited lines from `acc-003`.
It refuses a call-note read without its scope and rejects an appended uncited
sentence. The final frame lists six tools from the official Inspector CLI by
name and scope; the full response is saved separately. No model, real CRM,
Inspector UI, or interactive login is involved.

The script checks real command outcomes before displaying selected fields and
pausing for the reader. Playback caps idle periods at twelve seconds, so install
and test waits do not determine recording length. It is a walkthrough, not a
performance measurement.

## Regenerate it

Recording requires macOS or Linux with Python 3.11+. Set up Node and npx through the
[Inspector guide](inspector.md). Recording also requires asciinema 3.2.1 and
agg 1.9.0; the default demo and CI need neither. On macOS, these optional tools
are available from Homebrew:

```sh
brew install asciinema agg
```

The recording script runs from the repository root:

```sh
./scripts/record-demo.sh
```

The script requires committed source changes so the recorded revision identifies
what ran. It refuses a changed working tree, except for generated files in `docs/media/`.

The script installs existing Python requirements and downloads the pinned
Inspector package if uncached. It runs `./scripts/demo.sh` in fresh fixture
state. The recording script controls what reaches the terminal; the capture excludes the
desktop, a user's shell prompt, and keyboard input.

Generated files live in `docs/media/`:

| File | Contents |
| --- | --- |
| `walkthrough.gif` | The terminal recording rendered for GitHub. |
| `walkthrough.cast` | The original timed terminal output, with an idle limit. |
| `inspector-cli.gif` | The last terminal frame, showing the CLI tool listing. |
| `inspector-tools.json` | The validated, full Inspector `tools/list` response. |
| `recording-session.txt` | The displayed text without terminal clear sequences. |
| `recording-provenance.json` | Source revision, commands, exits, times, versions, and artifact hashes. |

Raw logs, fixture databases, and credentials stay in private, gitignored `var/`
directories. Only selected output is published. Before rendering, the renderer
checks text artifacts for credential patterns, local home paths, and issued
credentials still in local config files.

The capture must match the saved transcript after removing terminal clears and
normalizing line endings. These checks remain active under Python optimization.
Review the transcript and both images before committing; exclude the private
directories. Retention stays manual under the [security review](../SECURITY_REVIEW.md).

Failed commands or assertions stop generation. Read the private logs under
`var/recording-*` and `var/capture.*`, but do not paste them into an issue or
recording. Check Python dependencies, Node version, and cached Inspector
installation, fix the failure, and rerun. Partial media from a failed run is
not a completed recording; do not publish it as one.

This command replays the original capture locally:

```sh
asciinema play docs/media/walkthrough.cast
```
