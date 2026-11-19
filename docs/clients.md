# Connect a client

Keep the server running in one terminal:

```sh
gtm-connector serve --state-dir var/clients
```

In a second terminal in the same directory:

```sh
gtm-connector doctor --state-dir var/clients
gtm-connector connect inspector --state-dir var/clients --scope crm:read
```

`connect APP` prints the setup commands and limits for one client. It does not
launch an application. With `--scope`, it requests a local grant and saves it
in `APP.json` in the state folder, mode 0600, without printing the bearer
token. Without `--scope`, it previews the setup and creates nothing. Repeat
`--scope` for each of `crm:read`, `calls:read`, `slack:read`, and `crm:write`.

| `APP` | Status | What is established |
| --- | --- | --- |
| `inspector` | Tested with the Inspector 2.15.0 CLI. | Supplied-grant discovery, source reads, denials, rate limits, and audit receipts. Its UI and interactive login are untested. |
| `claude-code` | Untested. | The configuration follows Claude Code's HTTP and protocol documentation. No hosted assistant was connected. |
| `cursor` | Untested. | The configuration follows Cursor's URL and header documentation. Protocol and local certificate compatibility are unverified. |
| `claude-desktop` | Untested. No working setup exists. | The remote connection cannot reach the loopback server, and the connector has no stdio transport. |

These instructions describe configuration. They do not prove that an assistant works with the
connector.

## Run the printed command

The printed commands use absolute paths for the selected state folder. Each
starts with the same environment, shown here with `$PWD/var/clients`:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/clients/tls.pem" \
  NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" \
  no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" \
  NODE_TLS_REJECT_UNAUTHORIZED=1 \
  REST_OF_COMMAND
```

Node trusts the server's certificate for this process only. Loopback requests
skip inherited proxies. TLS verification stays on.

| `APP` | `REST_OF_COMMAND` | Notes |
| --- | --- | --- |
| `inspector` | `npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config "$PWD/var/clients/inspector.json" --server gtm-connector --stored-auth-only --quiet --format json --method tools/list` | First run the printed `npx --yes @modelcontextprotocol/inspector@2.15.0 --version`, which downloads the pinned package before the certificate setting changes. `--stored-auth-only` skips the interactive login and requests no further scopes. See the [Inspector walkthrough](inspector.md) and the official [Inspector guide](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector). |
| `claude-code` | `MCP_SDK_GENERATION=v2 MCP_PROTOCOL_NEGOTIATION=auto claude --mcp-config "$PWD/var/clients/claude-code.json"` | The file holds an HTTP server entry with an authorization header. See Claude Code's [MCP guide](https://code.claude.com/docs/en/mcp) and [network guide](https://code.claude.com/docs/en/network-config). This setup has not been run against Claude Code. |
| `cursor` | `cursor .` | First merge only the `gtm-connector` entry from `cursor.json` into your project's `.cursor/mcp.json`, keep the other entries, and quit Cursor completely. See Cursor's [MCP guide](https://cursor.com/docs/mcp). Cursor has not been connected. |

`connect claude-desktop` prints the missing requirements and the Inspector
command. It creates no configuration and no grant, even with `--scope`.
Claude's [remote connector documentation](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp)
says requests come from Anthropic's cloud and need public HTTPS on port 443, so
they cannot reach `127.0.0.1`. Desktop's
[local server configuration](https://modelcontextprotocol.io/docs/2026-07-28/develop/connect-local-servers)
launches a local process, and this connector has no stdio transport. The setup
includes no tunnel and no deployment.

## Grants and private files

- A grant expires after five minutes. Rerun the same `connect` command with
  its scopes to replace the file. For Cursor, copy the entry again. For Claude
  Code, restart with the updated file.
- Restarting `serve` replaces its endpoints and certificate and invalidates
  every earlier grant. Run `connect` again after a restart.
- The local authorization server registers only `gtm-demo-cli`, with callback
  `http://127.0.0.1:8767/callback`, and requires the local owner header. It
  has no dynamic client registration, browser consent page, or token refresh,
  and it rejects browser `Origin` headers. An assistant app therefore cannot
  complete its own interactive login. A supplied grant skips that login. It
  does not prove protocol compatibility.
- The configuration files hold bearer credentials. Keep `inspector.json`,
  `claude-code.json`, `cursor.json`, `.cursor/mcp.json`, and the state folder
  out of commits, transcripts, and model prompts, and readable only by their
  owner. This repository's `.gitignore` covers them. Add the same rules in
  another project.

See [authorization](protocol.md) for the local owner boundary and
[CLI output](cli.md) for the JSON form of `connect`.
