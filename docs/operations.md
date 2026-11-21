# Running it

**Install one binary, point it at PostgreSQL, and it serves the web app, the API and MCP on one port.**

```sh
gtm setup     # migrations, credentials, client configuration
gtm serve     # the web app, the API and MCP, over HTTPS
```

`gtm` is the short name of the `gtm-connector` binary.

## Install

A GitHub Actions release pipeline (GoReleaser) publishes, with each release, prebuilt static binaries for macOS and Linux, on arm64 and amd64, and Docker
images. The web app is embedded in the binary, so there is nothing else to deploy.

| Way | What you run |
| --- | --- |
| Binary | Download it, check its checksum, and give it a PostgreSQL 19 or 18.7 URL |
| Docker | Run the image with the same URL |
| Docker Compose | `make up` starts PostgreSQL and the server together |

## First run

`gtm setup` does three things:

1. It runs the migrations as the database owner.
2. It writes the credentials folder (mode 0700, files 0600): the demo password, the session key, and the TLS certificate and key. An `owner-secret` file an earlier setup wrote is left as it is, with a note: no command uses it, a backup does not carry it, and it may be deleted.
3. It makes the `demo` workspace and the demo sign-in, and prints the folder and the file that holds the demo password.

The server then connects as an app role. That role reads and writes rows. It cannot change the
schema or switch off a check.

### The app role's password

The operator chooses it. `gtm setup` reads the app role's name and password from
`GTM_DATABASE_URL`, connects as the owner through `GTM_OWNER_DATABASE_URL`, and makes the role with
that password, or sets the existing role's password to it. It does this on every run. Setup refuses
a URL that gives no password. The credentials folder holds no copy of it: the server and every command connect
with `GTM_DATABASE_URL` as they find it when they start.

With `make`, the first run writes `.env` (mode 0600) with a random `GTM_APP_PASSWORD`, and Compose
and `scripts/host.sh` build `GTM_DATABASE_URL` from it for the `gtm_app` role.

To rotate it:

1. Put the new password in `GTM_DATABASE_URL`. With `make`, change `GTM_APP_PASSWORD` in `.env`.
2. Run `gtm setup` with the owner URL set (`make dev` or `make up` run it). The old password is
   refused from then on.
3. Restart `gtm serve`, and give the new URL to anything else that holds the old one, such as an
   assistant app's `mcp stdio` configuration.

The database must be encoded as UTF8. Setup refuses any other encoding before it runs a migration,
with one line: `the database is encoded as SQL_ASCII; create it with ENCODING 'UTF8' TEMPLATE template0`.

> **Demo data and real sources never share a workspace.** The database refuses a real connection in
> the demo workspace. Imported call transcripts are the one exception: they may sit beside the demo CRM.

## Sources and assistants

`setup` leaves one workspace, `demo`, connected to the invented demo book. A real source goes in a
workspace of its own, named with `--state-dir`:

- [Sources](sources.md): Salesforce through an `sf` alias, call transcripts imported one file at a
  time, and one mapped Slack thread per account.
- [MCP](mcp.md): bearer tokens for `serve`'s `/mcp`, and `mcp stdio` for an assistant app that starts
  the binary itself.
- [The command line](cli.md): workspaces, `connect`, and every command's output and exit codes.

> **The database URL is the authority.** Anyone who can run the binary with `GTM_DATABASE_URL` set to
> the app role's URL can issue a token for any connected workspace and start `mcp stdio` for one. The
> [MCP guide](mcp.md) says what that grants.

## Health

`/healthz` answers 200 `{"status":"ok"}` when the database is reachable and its applied migrations
are exactly the ones the binary carries. Otherwise it answers 503 with one of four statuses:

| Status | Meaning |
| --- | --- |
| `migrations_behind` | A migration the binary carries is not applied. Run `gtm setup`. |
| `migrations_ahead` | A migration is applied that the binary does not carry. The binary is older than the database. |
| `migrations_changed` | An applied migration's contents differ from the binary's file of that name. |
| `database_unavailable` | The database did not answer within three seconds. |

`gtm health` asks the same question from a terminal. It exits 1 with the status unless the answer
is ok.

`gtm serve` asks it before it starts. On a database that is behind, ahead or changed it does not
start, and it prints one line that says which. `gtm setup` migrates a database that is behind, and
refuses one that is ahead or changed with the same line.

## Backup and restore

```sh
gtm backup --out gtm-2027-01-15.tar.gz
gtm restore --in gtm-2027-01-15.tar.gz
```

A backup holds the database and the credentials folder in one file. Its digests detect corruption
only: they are stored in the backup itself, so they do not prove who made it.

A restore needs an empty database. It refuses any row the server would refuse on writing, such as a
card whose seal does not name its bytes. If a restore stops part way, run the same command again.
Until it finishes, setup and the server refuse to start.

A backup names the migrations its database had applied. A restore applies those, loads the rows,
then applies the rest of the binary's migrations and checks the rows again. It refuses a backup
that names a migration the binary does not carry. A backup that gives a schema number from 1 to 13
in place of the names, as v0.1.0 writes, restores the same way.

> **A backup leaves the Slack sign-in out.** Slack spends a refresh token once, so a restored copy is
> one Slack no longer accepts. After a restore, run `debug slack connect` again for each workspace
> that reads Slack.

## Upgrade

1. Restore last night's backup into a spare database.
2. Run the new binary's `setup` against it.
3. Open a sealed card and check that it verifies.
4. Then upgrade the running server the same way.

An older binary refuses a database a newer binary's setup has migrated. To go back, restore the
backup into an empty database.

## Adding a migration

```sh
make migration NAME=deal_matches   # writes internal/storage/postgres/migrations/20261121T1830_deal_matches.sql
```

A migration file is named by the UTC minute it was written and lower-case words:
`YYYYMMDDTHHMM_words.sql`. The first thirteen files keep the names `0001_...` to `0013_...` and
sort before every stamp. The binary refuses any other name, a fourteenth number among them.

Setup applies every migration the database has not applied, in name order, each in its own
transaction, and records its name and the SHA-256 of its contents. Two branches that each add a
migration merge in either order with no rename. A database that applied the later-stamped file
first applies the earlier one at its next setup, so a migration must apply whether or not files
merged after it was written have run. Two migrations that each add a column to one table leave the
columns in the order they ran; a restore loads columns by name.

Never edit a merged migration: setup and the server refuse a database whose recorded digest differs
from the file. Write a new migration instead.

## Limits

| Limit | Value |
| --- | --- |
| Platforms | macOS and Linux |
| Database | PostgreSQL 19, or 18.7, encoded as UTF8 |
| Listeners | One HTTPS port for the web app, the API and MCP |
