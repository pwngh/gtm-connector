# Connect a client

`connect` prints setup commands and the limits of each connection. It does not
launch an application. Without `--scope`, it creates no grant or configuration.
With explicit scopes, it requests a local grant and saves it in a private file
without printing the bearer token. Repeat `--scope` for each source you want to
allow: `crm:read`, `calls:read`, or `slack:read`.

| Client | Status | What is established |
| --- | --- | --- |
| Inspector 2.15.0 CLI | Tested. | Supplied-grant discovery, source reads, denials, rate limits, and audit receipts. Its UI and interactive login are untested. |
| Claude Code | Untested. | Configuration follows its current HTTP and protocol documentation. No hosted assistant was connected. |
| Cursor | Untested. | Configuration follows its current URL and header documentation. Protocol and local certificate compatibility are unverified. |
| Claude Desktop | Untested; no working setup provided. | The remote connection cannot reach the loopback server. The connector has no stdio transport for a local server entry. |

The official app documentation linked below was checked on 2026-11-17. These
instructions describe configuration, not proof of assistant compatibility.

## Start and inspect the server

With `gtm-connector` installed, keep the server running in one terminal:

```sh
gtm-connector serve --state-dir var/clients
```

Use a second terminal in the same directory:

```sh
gtm-connector doctor --state-dir var/clients
gtm-connector connect inspector --state-dir var/clients
```

The second command only previews setup. Each command below that includes
`--scope` creates a grant. The printed commands contain absolute paths for the
selected state directory. The examples use `$PWD/var/clients` for those paths;
run them from the directory where you started the server.

## Inspector

```sh
gtm-connector connect inspector --state-dir var/clients --scope crm:read
```

This writes `var/clients/inspector.json` with mode 0600 and prints these commands
with absolute paths:

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --version
NODE_EXTRA_CA_CERTS="$PWD/var/clients/tls.pem" \
  NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" \
  no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" \
  NODE_TLS_REJECT_UNAUTHORIZED=1 \
  npx --yes @modelcontextprotocol/inspector@2.15.0 --cli \
  --config "$PWD/var/clients/inspector.json" --server gtm-connector \
  --stored-auth-only --quiet --format json --method tools/list
```

The first command downloads the pinned package before changing the certificate
setting for the local request. Node trusts the server's certificate only for
this process, and loopback bypasses inherited proxies. TLS verification stays
enabled. `--stored-auth-only` avoids Inspector's interactive login and prevents
it from requesting additional scopes. See the [Inspector walkthrough](inspector.md)
for Node requirements, actual tool calls, and the automated proof, and the
official [Inspector guide](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector)
for the client interface.

## Claude Code

```sh
gtm-connector connect claude-code --state-dir var/clients --scope crm:read
```

The command creates a private `var/clients/claude-code.json` with an HTTP server
entry and authorization header. It also creates the underlying Inspector grant
file. It prints this launch command with absolute paths:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/clients/tls.pem" \
  NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" \
  no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" \
  NODE_TLS_REJECT_UNAUTHORIZED=1 \
  MCP_SDK_GENERATION=v2 MCP_PROTOCOL_NEGOTIATION=auto \
  claude --mcp-config "$PWD/var/clients/claude-code.json"
```

Claude Code documents HTTP entries, `--mcp-config`, and the v2 runtime's
protocol negotiation settings in its [MCP guide](https://code.claude.com/docs/en/mcp).
Its [network guide](https://code.claude.com/docs/en/network-config) describes
process certificate and proxy settings. This setup has not been run against
Claude Code. Its documented options do not establish that the app accepts this
server's protocol or completes a request.

## Cursor

```sh
gtm-connector connect cursor --state-dir var/clients --scope crm:read
```

This creates private `var/clients/cursor.json` and Inspector grant files. Merge
only the `gtm-connector` entry from `cursor.json` into your project's
`.cursor/mcp.json`, preserving existing server entries. Keep that file private
and ignored by Git. Quit Cursor completely, then run the printed launch command:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/clients/tls.pem" \
  NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" \
  no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" \
  NODE_TLS_REJECT_UNAUTHORIZED=1 cursor .
```

Cursor's [MCP guide](https://cursor.com/docs/mcp) documents project configuration
with a server URL and headers. The generated entry follows that format. Cursor
has not been connected: its acceptance of MCP 2026-07-28 and the process's local
certificate setting remain unverified.

## Claude Desktop

```sh
gtm-connector connect claude-desktop --state-dir var/clients
```

This prints the missing requirements and an Inspector fallback. It does not
create a Desktop configuration or a grant, even when passed `--scope`.

Claude's [remote connector documentation](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp)
says requests originate in Anthropic's cloud and require public HTTPS on port
443. That route cannot reach this server on `127.0.0.1`. Desktop's separate
[local server configuration](https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers)
launches a local server process; this connector provides no stdio transport.
No tunnel, deployment, or hosted assistant connection is part of this setup.
Use the tested local client instead:

```sh
gtm-connector connect inspector --state-dir var/clients --scope crm:read
```

## Grants, login, and private files

The local authorization server registers only `gtm-demo-cli`, with callback
`http://127.0.0.1:8767/callback`, and requires the local owner header. It has no
dynamic client registration, browser consent page, or token refresh. It rejects
browser `Origin` headers. Those missing pieces prevent a normal assistant app's
interactive login with the current server. A supplied grant avoids that login
step; it does not prove protocol compatibility.

Grants expire after five minutes. Rerun the corresponding `connect` command
with explicit scopes to replace its private configuration. For Cursor, copy
the updated entry again; for Claude Code, restart with the updated file.
Restarting `serve` replaces its endpoints and certificate and invalidates all
previous grants, so regenerate configuration after a restart too.

Configuration files contain bearer credentials. Keep `inspector.json`,
`claude-code.json`, `cursor.json`, `.cursor/mcp.json`, and the state directory
out of commits, transcripts, and model prompts. This repository ignores them;
add equivalent ignore rules if using another project directory. Keep copied
configuration readable only by its owner. See [authorization](protocol.md) for
the local owner boundary and [CLI output](cli.md) for machine-readable setup.
