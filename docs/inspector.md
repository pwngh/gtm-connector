# Use the official MCP Inspector

[Inspector](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector) is
a test client for MCP servers. Inspector 2.15.0 discovers and calls the
connector's tools over Streamable HTTP with MCP 2026-07-28. Its calls pass the
same authorization, audit, and rate-limit checks as the included client.

It needs `gtm-connector` installed (see the [README](../README.md)), `npx`, and
Node 22.23.4+, 24.22.1+, or 26.12.1+. The first run downloads the pinned npm
package. The demo scripts need neither Node nor Inspector.

## Connect

Keep `gtm-connector serve --state-dir var/inspector` running in one terminal.
In a second terminal in the same directory:

```sh
gtm-connector connect inspector --state-dir var/inspector --scope crm:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --version
export NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}"
export no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}"
export NODE_TLS_REJECT_UNAUTHORIZED=1
export NODE_EXTRA_CA_CERTS="$PWD/var/inspector/tls.pem"
inspect() {
  npx --yes @modelcontextprotocol/inspector@2.15.0 --cli \
    --config var/inspector/inspector.json --server gtm-connector \
    --stored-auth-only --quiet --format json "$@"
}
```

`connect` writes the bearer token to `var/inspector/inspector.json` with mode
0600 and does not print it. `inspector-config --scope SCOPE` writes the same
file. `NODE_EXTRA_CA_CERTS` makes this Node process trust the generated
certificate, and TLS verification stays on. Download Inspector before setting
it, because a private npm registry may need its own certificate.
`--stored-auth-only` prevents an interactive login and requests no further
scopes.

## Discover and call tools

```sh
inspect --method initialize
inspect --method tools/list
inspect --method tools/call --tool-name get_account --tool-arg account_id=acc-003
inspect --method tools/call --tool-name read_call_notes --tool-arg account_id=acc-003
inspect --method tools/call --tool-name read_slack_messages --tool-arg account_id=acc-003
```

| Call | Grant | Result |
| --- | --- | --- |
| `initialize` | Any | `result.protocolVersion: "2026-07-28"`. Inspector sends `server/discover` on the wire, not a legacy handshake. |
| `tools/list` | Any | All six tools, with input and output schemas and required-scope metadata. |
| `get_account` | `crm:read` | `result.structuredContent.records` with `crm/accounts/acc-003` and `crm/contacts/con-003`. |
| `read_call_notes` | `crm:read` only | HTTP 403. Inspector exits 1 with empty stdout and reports `Insufficient scope: required "calls:read"` in its stderr JSON error. |
| `read_call_notes` | `calls:read` | `calls/call_notes/note-003`, marked as untrusted data. |
| `read_slack_messages` | `slack:read` | `slack/messages/msg-003`. A Slack-only grant cannot read CRM or call notes. |

Rerun `connect` with other `--scope` values to replace the grant while the
server runs. `gtm-connector audit --state-dir var/inspector` lists the
receipts. A token expires after five minutes, and a server restart changes the
ports and invalidates old tokens, so rerun `connect` in both cases. Credentials
and receipts stay in the state folder until you remove them.

## Automated check and limits

```sh
.venv/bin/python scripts/check-inspector.py
```

CI runs this check with the pinned Inspector CLI in temporary state. Inspector
sends the MCP requests, and the included client only provisions grants. The
check requires discovery, valid schemas for all six tools (twelve schemas),
all three source reads, all six cross-source denials, HTTP 429 at the rate
limit, HTTP 401 for an invalid bearer token, ten finalized receipts for ten
tool attempts, and zero activities.

This shows that the Inspector CLI works with a supplied bearer token. It does
not show that Inspector's UI, its interactive OAuth flow, or any other
assistant application works. Browser origins, legacy initialization,
subscriptions, and dynamic client registration are unsupported. The
[client setup guide](clients.md) covers Claude Code, Cursor, and Claude Desktop.
