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
3. It prints the MCP configuration for each assistant client.

The server then connects as an app role. That role reads and writes rows. It cannot change the
schema or switch off a check.

> **Demo data and real sources never share a workspace.** The database refuses a real connection in
> the demo workspace.

## Health

`/healthz` answers 200 `{"status":"ok"}` when the database is reachable and its applied migrations
are exactly the ones the binary carries. Otherwise it answers 503 with one of two statuses:

| Status | Meaning |
| --- | --- |
| `migrations_not_current` | The database answers, but its schema is behind the binary, ahead of it, or from other migration files. |
| `database_unavailable` | The database did not answer within three seconds. |

`gtm health` asks the same question from a terminal. It exits 1 with the status unless the answer
is ok.

## MCP clients

An assistant reads the connector's tools over MCP in one of two ways: it calls `serve`'s `/mcp` over
HTTPS with a bearer token, or it starts `gtm-connector mcp stdio` itself. Both serve the read tools
under the same gate: one receipt committed before each call runs, a refused call still receipted,
each scope opening only its own source, and the accounts the token's principal is granted.

> **The database URL is the authority.** Anyone who can run the binary with `GTM_DATABASE_URL` set
> to the app role's URL can issue a token for any connected workspace, with any read scope, and can
> start `mcp stdio` for one. Neither asks for a browser session or any other sign-in. Give that URL,
> and the password behind it, only to people who may read every connected workspace.

### A token for `/mcp`

```sh
gtm-connector setup
gtm-connector demo
gtm-connector token new --scope crm:read      # prints the token once
gtm-connector serve                           # leave it running
```

`token new` prints the token on its first line and never again. Send it as
`Authorization: Bearer TOKEN` to `https://127.0.0.1:8443/mcp`, trusting the certificate in the
credentials folder:

```sh
curl --cacert "$GTM_CREDENTIALS_DIR/tls.pem" https://127.0.0.1:8443/mcp \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -H 'Mcp-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/call' -H 'Mcp-Name: get_account' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_account",
       "arguments":{"account_id":"acc-003"},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28",
       "io.modelcontextprotocol/clientCapabilities":{}}}}'
```

| A token | |
| --- | --- |
| Workspace | One: the one `--state-dir` names, `demo` by default. `serve` answers `/mcp` for the demo workspace only. |
| Scopes | Only those given, from `crm:read`, `calls:read` and `slack:read`. Repeat `--scope` for each. |
| Lifetime | Five minutes, or `--minutes N` up to 60, by the database's clock, which also decides when it has expired. Run `token new` again for another. |
| Accounts | Those the workspace's `MCP clients` principal is granted. Every client token of a workspace belongs to that principal. |
| Storage | Only its SHA-256, which is the token ID its receipts name. `gtm-connector audit --caller CALLER` lists its calls. |

`gtm-connector token list` shows each token's ID, scopes, state (`live`, `expired` or `revoked`),
expiry and `CALLER`, never the token. `gtm-connector token revoke ID` refuses it from the next call.

> **Issuing widens every client token of the workspace.** Issuing a token, or starting `mcp stdio`,
> grants the shared `MCP clients` principal every account of the connected CRM it was never granted.
> So when the CRM holds a new account, every live client token of that workspace can read it after
> the next issuance. A grant revoked for the principal stays revoked. The grants and the token are
> written in one transaction: an issuance that fails adds no grant.

### Stdio, started by the app

An app that starts a local MCP server runs `gtm-connector mcp stdio --scope crm:read`, with
`GTM_DATABASE_URL` set to the app role's URL. No server needs to run. The command reads the
workspace from PostgreSQL itself and serves it as the workspace's `MCP clients` principal, with a
token made for the process and never shown: it carries exactly the given scopes, lasts two
minutes, is renewed every 30 seconds while the process runs, and is revoked when it ends, within
five seconds; a revocation that fails is said on stderr in one line. A killed process leaves its
token usable for at most two minutes. A token that expired or was revoked is not renewed, and the
app must start the command again. At start the command also finishes, as interrupted, any receipt
an earlier process left pending; `setup` and `serve` do the same.
`--selection FILE` limits it to a selection's accounts and adds the two brief tools, as it does for
`serve`.

Keep the database password out of the app's configuration: give the URL without it, and put it in
`~/.pgpass`, or the file `PGPASSFILE` names, which the binary reads. The binary refuses that file
when anyone but its owner has access to it (make it mode 0600). A password that ends in a space is
not supported there: the driver trims each line of the file.

```json
{"mcpServers": {"gtm-connector": {"type": "stdio", "command": "/usr/local/bin/gtm-connector",
  "args": ["mcp", "stdio", "--scope", "crm:read"],
  "env": {"GTM_DATABASE_URL": "postgres://gtm_app@127.0.0.1:55432/gtm"}}}}
```

The process writes only MCP messages on stdout. It stops when its input ends, when the app closes
its end of stdout, or when one line has waited ten seconds to be written because the app stopped
reading. A call it was still running gets a finished receipt all the same, and a line it was still
admitting is waited for, up to ten seconds, before it stops. A line is measured as written: a call
over 65,536 bytes is refused with its receipt, and a call over 4 MiB with one too, under the tool
name its start shows.

CI runs the pinned Inspector CLI (2.15.0) against the binary: `setup`, `demo` and `token new`,
then `/mcp` over HTTPS with each token, then `mcp stdio` started by the Inspector. It checks
discovery, the five tools and their schemas, the three source reads, the cross-source denials, an
unknown bearer refused with HTTP 401, one finished receipt for each tool call, and that no token reaches the Inspector's
output, the audit log or the server's log. No assistant app has been tested.

## Backup and restore

```sh
gtm backup
gtm restore gtm-2027-01-15.tar.gz
```

A backup holds the database and the credentials folder in one file. Its digests detect corruption
only: they are stored in the backup itself, so they do not prove who made it.

A restore needs an empty database. It refuses any row the server would refuse on writing, such as a
card whose seal does not name its bytes. If a restore stops part way, run the same command again.
Until it finishes, setup and the server refuse to start.

## Upgrade

1. Restore last night's backup into a spare database.
2. Run the new binary's `setup` against it.
3. Open a sealed card and check that it verifies.
4. Then upgrade the running server the same way.

## Limits

| Limit | Value |
| --- | --- |
| Platforms | macOS and Linux |
| Database | PostgreSQL 19, or 18.7 |
| Listeners | One HTTPS port for the web app, the API and MCP |
