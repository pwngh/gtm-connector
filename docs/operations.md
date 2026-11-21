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
2. It writes the credentials folder (mode 0700, files 0600): the owner secret, the session key, and the TLS certificate and key.
3. It makes the `demo` workspace and the demo sign-in, and prints the folder and the file that holds the demo password.

The server then connects as an app role. That role reads and writes rows. It cannot change the
schema or switch off a check.

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

## Sign-in through your identity provider

People sign in to the web app with their company account, through your identity provider, with OpenID
Connect. The connector stores no password for them. They sign in to one workspace, and read only the
accounts you grant them.

1. Register a client at the provider. Its redirect URL is the server's public origin followed by
   `/api/v1/session/oidc/callback`, such as `https://127.0.0.1:8443/api/v1/session/oidc/callback`.
2. For a confidential client, put its secret in the file `oidc-client-secret` in the credentials folder,
   with mode 0600. Leave the file out for a public client, which signs in with PKCE alone. The secret is
   never an environment variable.
3. Set the variables below and start `gtm serve`.

```sh
GTM_OIDC_ISSUER=https://login.invented.example \
GTM_OIDC_CLIENT_ID=gtm-connector \
GTM_OIDC_WORKSPACE=acme \
GTM_OIDC_ALLOWED_DOMAINS=invented.example \
GTM_DEMO_SIGN_IN=off \
gtm serve
```

| Variable | What it sets |
| --- | --- |
| `GTM_OIDC_ISSUER` | The provider's issuer, an https URL exactly as the provider names itself. Setting it turns this sign-in on. The connector reads the provider's discovery document under it. |
| `GTM_OIDC_CLIENT_ID` | The client's ID at the provider. |
| `GTM_OIDC_WORKSPACE` | The workspace people sign in to. It must exist. |
| `GTM_OIDC_ALLOWED_DOMAINS` | The email domains that may sign in, separated by commas. A person needs an email in one of them that the provider marked verified. |
| `GTM_OIDC_TENANT_CLAIM`, `GTM_OIDC_TENANT` | A claim, `hd` or `tid`, and the value it must have, for a provider that vouches for its tenant rather than for emails. Set this, the domains, or both. |
| `GTM_OIDC_REDIRECT_URL` | The redirect URL, if you set it: it must be the public origin followed by `/api/v1/session/oidc/callback`, which is the default. |
| `GTM_OIDC_SESSION_MINUTES` | How long a session lasts, from 5 to 720 minutes. The default is 60. |
| `GTM_DEMO_SIGN_IN` | `on`, the default, or `off`. |

`gtm serve` checks every setting before it listens, and refuses one it cannot serve with one line that
does not repeat the issuer. It refuses the demo sign-in beside a provider that signs in a workspace of
real sources: set `GTM_DEMO_SIGN_IN=off`. The line it prints when it starts names the sign-ins it offers.

A person appears in the workspace the first time they sign in, found by the provider's issuer and
subject, never by their email, and with no grant. Grant them accounts the workspace holds:

```sh
gtm grants --state-dir acme                         # who has signed in, and what each reads
gtm grant acc-sf-001000000000000AAA --to dana@invented.example --state-dir acme
gtm grant acc-sf-001000000000000AAA --to dana@invented.example --state-dir acme --revoke
```

`--to` takes an email the provider verified, or the subject `grants` lists. An email two people share,
or one the provider did not verify, names nobody: give the subject. Each change gets a receipt, and so
does a refused person or account. [The command line](cli.md#grant-and-grants) gives each command's
output.

A person the provider disables keeps the session they hold until it expires. Signing out ends the
session here, and offers the provider's own sign-out page when the provider publishes one.

## Health

`/healthz` answers 200 `{"status":"ok"}` when the database is reachable and its applied migrations
are exactly the ones the binary carries. Otherwise it answers 503 with one of four statuses:

| Status | Meaning |
| --- | --- |
| `migrations_behind` | A migration the binary carries is not applied. Run `gtm setup`. |
| `migrations_ahead` | A migration is applied that the binary does not carry. The binary is older than the database. |
| `migrations_changed` | An applied migration's contents differ from the binary's file of that number. |
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

> **A backup leaves the Slack sign-in out.** Slack spends a refresh token once, so a restored copy is
> one Slack no longer accepts. After a restore, run `debug slack connect` again for each workspace
> that reads Slack.

A backup also leaves the identity provider's client secret out. After a restore, put
`oidc-client-secret` back in the credentials folder.

## Upgrade

1. Restore last night's backup into a spare database.
2. Run the new binary's `setup` against it.
3. Open a sealed card and check that it verifies.
4. Then upgrade the running server the same way.

## Limits

| Limit | Value |
| --- | --- |
| Platforms | macOS and Linux |
| Database | PostgreSQL 19, or 18.7, encoded as UTF8 |
| Listeners | One HTTPS port for the web app, the API and MCP |
