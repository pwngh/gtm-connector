# Connect a client

```sh
gtm-connector connect demo --state-dir var/clients    # or connect salesforce --org ALIAS
gtm-connector debug serve --state-dir var/clients     # keep it running in one terminal
gtm-connector debug doctor --state-dir var/clients    # second terminal, same directory
gtm-connector connect inspector --state-dir var/clients --scope crm:read
```

An assistant app needs `debug serve` running in its state folder, for the HTTP transport and for the
local bridge. `debug serve` serves the sources its state folder is [connected](cli.md) to.
`connect APP` prints the setup commands and limits for one client. It does not launch an
application. With `--scope`, an HTTP client (Inspector, Claude Code or Cursor) gets a local grant,
saved in `APP.json` in the state folder, mode 0600, without printing the bearer token. Claude Desktop
gets none over HTTP, and a stdio entry holds no grant (below). Without `--scope`, HTTP setup previews
the instructions and creates nothing; stdio setup requires explicit scopes. Repeat `--scope` for each of `crm:read`, `calls:read`, `slack:read`, and
`crm:write`.

| `APP` | Status | `REST_OF_COMMAND` |
| --- | --- | --- |
| `inspector` | Tested with the Inspector 2.15.0 CLI: supplied-grant discovery, source reads, denials, rate limits, and audit receipts. Its UI and interactive login are untested. | `npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config "$PWD/var/clients/inspector.json" --server gtm-connector --stored-auth-only --quiet --format json --method tools/list`. See Inspector below. |
| `claude-code` | Untested. The configuration follows Claude Code's [MCP guide](https://code.claude.com/docs/en/mcp) and [network guide](https://code.claude.com/docs/en/network-config). | `MCP_SDK_GENERATION=v2 MCP_PROTOCOL_NEGOTIATION=auto claude --mcp-config "$PWD/var/clients/claude-code.json"`. The file holds an HTTP server entry with an authorization header. |
| `cursor` | Untested. The configuration follows Cursor's [MCP guide](https://cursor.com/docs/mcp). Protocol and local certificate compatibility are unverified. | `cursor .` First merge only the `gtm-connector` entry from `cursor.json` into your project's `.cursor/mcp.json`, keep the other entries, and quit Cursor completely. |
| `claude-desktop` | Untested in the app. It starts local servers as processes, so it uses the [local bridge](#a-local-bridge-for-assistant-apps). Its [remote connectors](https://support.claude.com/en/articles/11175166-get-started-with-custom-connectors-using-remote-mcp) need public HTTPS on port 443 and cannot reach `127.0.0.1`. | None over HTTP. With `--transport stdio`, restart the app after `connect` writes its entry. |

The printed commands use absolute paths for the state folder. Start the HTTP clients below with
this environment, shown here with `$PWD/var/clients`:

```sh
NODE_EXTRA_CA_CERTS="$PWD/var/clients/tls.pem" \
  NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}" \
  no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}" \
  NODE_TLS_REJECT_UNAUTHORIZED=1 \
  REST_OF_COMMAND
```

Node trusts the server's certificate for this process only. Loopback requests skip inherited
proxies. TLS verification stays on.

## A local bridge for assistant apps

Some apps start a local MCP server as a process and speak to it on stdin and stdout. For those,
`connect` writes an entry that starts the connector's bridge:

```sh
gtm-connector debug serve --state-dir var/clients     # keep it running
gtm-connector connect claude-desktop --transport stdio --scope crm:read --scope calls:read \
  --state-dir var/clients --config-file "$HOME/Library/Application Support/Claude/claude_desktop_config.json"
```

- `--transport stdio` works for `claude-desktop`, `claude-code`, and `cursor`, and needs `--scope`
  and `--config-file`. It merges only the `gtm-connector` entry into that file and saves the
  previous file beside it first, with the same mode. Both are written under temporary names and
  renamed into place. It refuses a symbolic link, a file with a second name, a file that is not one
  strict JSON object, a result over 4 MiB, and a file that changed since it was read. Quit the app
  first: a save it makes in the same instant can be lost.
- The entry holds the Python path, `mcp stdio`, the state folder, and the scopes. It holds no token.
  Python starts isolated (`-I`), so a folder the app runs in cannot stand in for the connector.
- A call the bridge would refuse for its shape is still sent to the gateway, which records it and
  refuses it.
- The app starts that Python, isolated with `-I`, running the connector's `mcp stdio --state-dir DIR --scope ...`. The bridge attaches to the
  `debug serve` already running in that folder, obtains a grant for exactly those scopes, keeps it in
  memory, and renews only those scopes. Every call still goes through the HTTPS gateway, with its
  scope check, rate limit, and audit receipt. A call outside the scopes is refused.
- Start `debug serve` first. Without it the bridge exits with a message on stderr. After `debug serve` restarts,
  restart the app.
- The bridge refuses a folder with no connected CRM. After `connect` changes the folder's sources,
  it refuses the `debug serve` that was started before the change, so restart `debug serve` and then the app.
- Tested with the MCP SDK's own stdio client. No assistant app has been tested.

## Grants and private files

These apply to the HTTP grants `connect` saves. The bridge keeps its own grant in memory and renews
it.

- A grant expires after five minutes. Rerun the same `connect` command with its scopes to replace
  the file. For Cursor, copy the entry again. For Claude Code, restart with the updated file.
- Restarting `debug serve` replaces its endpoints and certificate and invalidates every earlier grant.
  Run `connect` again after a restart.
- The local authorization server registers only `gtm-demo-cli`, with callback
  `http://127.0.0.1:8767/callback`, and requires the local owner header. An assistant app cannot
  complete its own interactive login. See [authorization](tools.md).
- The configuration files hold bearer credentials. Keep `inspector.json`, `claude-code.json`,
  `cursor.json`, `.cursor/mcp.json`, and the state folder out of commits, transcripts, and model
  prompts, and readable only by their owner. This repository's `.gitignore` covers them. Add the
  same rules in another project.

## Inspector

Inspector 2.15.0 needs `npx` and Node 22.23.4+, 24.22.1+, or 26.12.1+.

```sh
gtm-connector connect demo --state-dir var/inspector
gtm-connector debug serve --state-dir var/inspector  # leave running; second terminal, same directory
gtm-connector connect inspector --state-dir var/inspector --scope crm:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --version   # downloads the pinned package
export NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}"
export no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}"
export NODE_TLS_REJECT_UNAUTHORIZED=1 NODE_EXTRA_CA_CERTS="$PWD/var/inspector/tls.pem"
inspect() { npx --yes @modelcontextprotocol/inspector@2.15.0 --cli \
  --config var/inspector/inspector.json --server gtm-connector \
  --stored-auth-only --quiet --format json "$@"; }
```

- Download Inspector before setting `NODE_EXTRA_CA_CERTS`, because a private npm registry may need
  its own certificate.
- `--stored-auth-only` prevents an interactive login and requests no further scopes.

```sh
inspect --method initialize
inspect --method tools/list
inspect --method tools/call --tool-name get_account --tool-arg account_id=acc-003
```

| Call | Grant | Result |
| --- | --- | --- |
| `initialize` | Any | `result.protocolVersion: "2026-07-28"`. Inspector sends `server/discover` on the wire, not a legacy handshake. |
| `tools/list` | Any | All six tools, with input and output schemas and required-scope metadata. |
| `get_account` | `crm:read` | `result.structuredContent.records` with `crm/accounts/acc-003` and `crm/contacts/con-003`. |
| `read_call_notes` | `crm:read` only | HTTP 403. Inspector exits 1 with empty stdout and reports `Insufficient scope: required "calls:read"` in its stderr JSON error. |
| `read_call_notes` | `calls:read` | `calls/call_notes/note-003` and `calls/call_notes/note-003b`, marked as untrusted data. |
| `read_slack_messages` | `slack:read` | `slack/messages/msg-003`. A Slack-only grant cannot read CRM or call notes. |

Rerun `connect` with other `--scope` values to replace the grant while the server runs.
`gtm-connector audit --state-dir var/inspector` lists the receipts.

CI runs `scripts/check-inspector.py` with the pinned Inspector CLI in temporary state. It requires
discovery, valid schemas for all six tools (twelve schemas), all three source reads, all six
cross-source denials, HTTP 429 at the rate limit, HTTP 401 for an invalid bearer token, ten
finalized receipts for ten tool attempts, and zero activities.
