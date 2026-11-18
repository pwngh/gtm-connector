# Use the official MCP Inspector

Inspector is a test client for MCP servers. Inspector 2.15.0 discovers and calls
the connector's tools through its existing Streamable HTTP endpoint, using
MCP 2026-07-28. TLS verification stays on. Tool calls use an explicitly scoped
bearer token and pass through the same authorization, audit, and rate-limit checks
as the included client.

Install `gtm-connector` as described in the [README](../README.md). In a repository
checkout, `./scripts/demo.sh` installs the Python dependencies and
`.venv/bin/python -m gtm_connector` accepts the same commands. This optional
client also requires Node 22.23.4+, 24.22.1+, or 26.12.1+ and `npx`. The first
Inspector invocation downloads its pinned npm package. Neither default demo
script needs Node or Inspector.

## Start the server

The server must stay running in one terminal while you use the client:

```sh
gtm-connector serve --state-dir var/inspector
```

The connection command requests CRM access from a second terminal in the same
directory. It prints an exact discovery command for the current server:

```sh
gtm-connector connect inspector --state-dir var/inspector --scope crm:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --version
export NO_PROXY="127.0.0.1,localhost${NO_PROXY:+,$NO_PROXY}"
export no_proxy="127.0.0.1,localhost${no_proxy:+,$no_proxy}"
export NODE_TLS_REJECT_UNAUTHORIZED=1
export NODE_EXTRA_CA_CERTS="$PWD/var/inspector/tls.pem"
```

The command prints setup and its tested limits. It writes the bearer token to
`var/inspector/inspector.json` with mode 0600, without printing it or exporting
the owner credential. The config selects `protocolEra: modern`. Git ignores
this file; keep it private. Omitting `--scope` previews setup without creating
a grant. The existing `inspector-config` command remains available to write
the same Inspector configuration and report its path, certificate, and scopes.

`NODE_EXTRA_CA_CERTS` lets this Node process trust the generated certificate
without changing system trust or disabling verification. The proxy bypass keeps
loopback requests out of inherited HTTP proxies. Download Inspector before
replacing an extra-CA setting; a private npm registry may need its own certificate.

## Discover and call tools

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method initialize
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/list
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name get_account --tool-arg account_id=acc-003
```

Inspector calls its connection-summary command `initialize`. This modern config
sends `server/discover` on the wire, not a legacy handshake. The summary reports
`result.protocolVersion: "2026-07-28"`.

The listing describes all six tools with input and output schemas and
required-scope metadata. The account read returns
`result.structuredContent.records` with references `crm/accounts/acc-003` and
`crm/contacts/con-003`.

This next call fails because the token has no calls grant:

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name read_call_notes --tool-arg account_id=acc-003
```

The gateway returns HTTP 403. Inspector exits 1 with empty stdout and reports
`Insufficient scope: required "calls:read"` in its stderr JSON error.
`--stored-auth-only` prevents interactive login. With that flag, the client also
requests no additional scopes.

An explicit calls grant lets the same Inspector command read the notes:

```sh
gtm-connector connect inspector --state-dir var/inspector --scope crm:read --scope calls:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name read_call_notes --tool-arg account_id=acc-003
```

The successful response contains `calls/call_notes/note-003` and marks its data
as untrusted. Inspect the receipts with:

```sh
gtm-connector audit --state-dir var/inspector
```

After five minutes the token expires; rerun `connect inspector` with scopes for another
grant. A server restart also requires a fresh config because its ports change
and old tokens become invalid. Credentials and receipts remain in the state
directory until removed.

A Slack-only grant replaces the config while the server stays running:

```sh
gtm-connector connect inspector --state-dir var/inspector --scope slack:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name read_slack_messages --tool-arg account_id=acc-003
```

The result contains `slack/messages/msg-003`. The same grant cannot read CRM
or call notes. Stop the server with Ctrl-C when finished.

## Automated proof and limits

```sh
.venv/bin/python scripts/check-inspector.py
```

This separate CI check uses the pinned official CLI in fresh temporary state.
Inspector sends MCP requests; the included client only provisions grants. The
check observes actual HTTP protocol headers and metadata, validating discovery
and all twelve tool schemas.

It requires all three source reads and all six cross-source denials, plus
HTTP 429 and an invalid-bearer HTTP 401. It confirms ten finalized receipts for
ten tool attempts with zero activities, then removes test state.

This proves Inspector CLI interoperability with a supplied bearer token, not
another assistant application's compatibility or Inspector's interactive OAuth
flow. The authorization server remains a local owner emulator with one
preregistered public client. Browser origins and legacy initialization remain
unsupported, as do subscriptions and dynamic client registration.
The [client setup guide](clients.md) records the untested Claude Code and Cursor
configurations and the missing requirements for Claude Desktop.

The official [Inspector guide](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector)
and [protocol-era reference](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector/protocol-eras)
explain the client and its protocol modes.
