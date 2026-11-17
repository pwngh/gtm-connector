# Use the official MCP Inspector

Inspector 2.15.0 connects to the existing Streamable HTTP endpoint. This path
uses MCP 2026-07-28, TLS verification, and a bearer token with explicit scopes.
All tool calls pass through the same authorization, audit, and rate-limit checks.

Install the Python dependencies with `./scripts/demo.sh` first. This optional
client also requires Node 22.23.4+, 24.22.1+, or 26.12.1+ and `npx`. The first
Inspector invocation downloads its pinned npm package. Neither default demo
script needs Node or Inspector.

## Start the server

Keep this command running in one terminal:

```sh
.venv/bin/python -m gtm_connector serve --state-dir var/inspector
```

In a second terminal, at the repository root, request CRM access:

```sh
.venv/bin/python -m gtm_connector inspector-config --state-dir var/inspector --scope crm:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --version
export NO_PROXY="${NO_PROXY:+$NO_PROXY,}127.0.0.1"
export no_proxy="${no_proxy:+$no_proxy,}127.0.0.1"
export NODE_TLS_REJECT_UNAUTHORIZED=1
export NODE_EXTRA_CA_CERTS="$PWD/var/inspector/tls.pem"
```

The utility prints the config path, certificate path, and granted scopes. It
writes the bearer token to `var/inspector/inspector.json` with mode 0600. It
does not print that token or export the owner credential. The config selects
`protocolEra: modern`. Keep it private. It is ignored by git.

`NODE_EXTRA_CA_CERTS` adds the generated certificate to this Node process's
trust. It does not change system trust or disable certificate verification.
The proxy-bypass entries keep loopback requests out of inherited HTTP proxies.
Download Inspector before replacing an existing extra-CA setting, since a
private npm registry may need its own certificate.

## Discover and call tools

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method initialize
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/list
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name get_account --tool-arg account_id=acc-003
```

Inspector calls its connection-summary command `initialize`. With this modern
config, it sends `server/discover` on the wire, not a legacy handshake. The
summary reports `result.protocolVersion: "2026-07-28"`. Tool listing returns
all five tools, their input and output schemas, and required-scope metadata.
Account output appears in `result.structuredContent.records` with references
`crm/accounts/acc-003` and `crm/contacts/con-003`.

This next call fails because the token has no calls grant:

```sh
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name read_call_notes --tool-arg account_id=acc-003
```

The gateway returns HTTP 403. Inspector exits 1, leaves stdout empty, and reports
`Insufficient scope: required "calls:read"` in its stderr JSON error. The
`--stored-auth-only` flag prevents an interactive login attempt. It does not
request more scopes.

Request calls access explicitly, then repeat that exact Inspector command:

```sh
.venv/bin/python -m gtm_connector inspector-config --state-dir var/inspector --scope crm:read --scope calls:read
npx --yes @modelcontextprotocol/inspector@2.15.0 --cli --config var/inspector/inspector.json --server gtm-connector --stored-auth-only --quiet --format json --method tools/call --tool-name read_call_notes --tool-arg account_id=acc-003
```

The successful response contains `calls/call_notes/note-003` and marks its data
as untrusted. Inspect the receipts with:

```sh
.venv/bin/python -m gtm_connector audit --state-dir var/inspector
```

Stop the server with Ctrl-C. Tokens expire after five minutes; re-run
`inspector-config` to request another grant. Restarting the server changes its
ports and invalidates old tokens, so regenerate the config after every restart.
The state directory retains credentials and receipts until you remove it.

## Automated proof and limits

```sh
.venv/bin/python scripts/check-inspector.py
```

This separate CI check uses the pinned official CLI in fresh temporary state.
It validates discovery, all ten tool schemas, both source reads, both directions
of scope denial, HTTP 429, an invalid-bearer HTTP 401, five finalized receipts
for five tool attempts, and zero activities. It observes actual HTTP protocol
headers and metadata. The included client only provisions grants; Inspector
sends the MCP requests. Test state is removed afterward.

This proves Inspector CLI interoperability with a supplied bearer token. It
does not prove another assistant application's compatibility or Inspector's
interactive OAuth flow. The authorization server remains a local owner emulator
with one preregistered public client. Browser origins, legacy initialization,
subscriptions, and dynamic client registration remain unsupported.

Read the official [Inspector guide](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector)
and [protocol-era reference](https://modelcontextprotocol.io/docs/2026-07-28/tools/inspector/protocol-eras).
