# GTM connector demo

A local CRM fixture for a renewal-call briefing connector. Portfolio project
for Preston Neal. All accounts, contacts, opportunities, and notes are invented.

This first slice contains the SQLite data and a command-line inspector. The MCP
server, authorization, briefing workflow, citation checker, audit viewer, and
evaluation are planned in the next three pull requests. It is not yet the
complete connector demo.

## Run

Requires Python 3.11 or newer with SQLite support. No dependency installation,
database service, account, model, or API key is needed for this slice.

From a clean checkout:

```sh
./scripts/demo.sh
```

The command runs the tests, creates `var/crm.sqlite3`, and prints JSON containing
record counts and account `acc-001` with its contacts, opportunities, and notes.
The database has 30 accounts, each with a contact, opportunity, renewal date,
and call note. Amounts are integer cents in USD. Dates are fixed fixture values,
so results do not change with the clock.

To use a different database path:

```sh
./scripts/demo.sh --db /tmp/gtm-demo.sqlite3
```

## Inspect records

```sh
python3 -m gtm_connector seed
python3 -m gtm_connector accounts --query ember
python3 -m gtm_connector account acc-007
```

Each command accepts `--db PATH` after the subcommand. Search matches account
names by case-insensitive literal substring; SQL wildcards are treated as text.
Empty searches return all accounts. Unknown account IDs return exit code 2 and
an error on stderr. Read commands open an existing database in read-only mode;
use `seed` to create one.

Running `seed` again preserves existing records and activities. An incomplete
or incompatible fixture is rejected instead of being silently overwritten.
Use a new database path for a fresh fixture. Generated databases are gitignored.

Note `note-007` contains an instruction aimed at a future model. For now it is
stored and displayed as ordinary text. This slice does not claim to test model
resistance to prompt injection; that test belongs with the briefing workflow.

## Verify

```sh
python3 -m unittest discover -s tests -v
```

Tests exercise fixture relationships, repeatability, preservation on reseeding,
literal search, foreign keys, read-only access, and CLI behavior. Test databases
are temporary. CI runs the same demo on Python 3.11 and 3.14.

Evaluation scores: not measured yet. No model or brief generator exists in this
slice, so there is no accuracy claim. The planned 20-account evaluation will
report facts found, facts cited correctly, and facts invented.

See [DECISIONS.md](DECISIONS.md) for scope and protocol research, and
[AGENTS.md](AGENTS.md) for the review and shipping rules. MIT licensed.
