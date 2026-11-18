# Protocol and authorization

The connector grants client access through a local owner emulator, without
production login or user identity.

The server implements [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28)
and its [authorization section, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).
Both were read on **2026-11-17**. The official Python SDK is pinned to **2.3.0**.

The transport is stateless Streamable HTTP with per-request protocol metadata
and `server/discover`. Only this protocol revision is accepted.
Subscriptions and legacy initialization sessions are not supported.
[DECISION_LOG.md](../DECISION_LOG.md) explains the checks the gateway adds to the SDK.

The official Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an
explicit bearer grant. It sends `server/discover`
with matching version headers and per-request protocol metadata. See
[the Inspector walkthrough](inspector.md) to reproduce the exchange. This proves
MCP transport and tool access. Inspector's interactive OAuth login and
untested assistant applications remain unverified. Compatibility with arbitrary
clients is not claimed.

## Request refusals

A client can identify a gateway refusal through `error.data.reason` instead of
parsing its message. The [request-check tests](../tests/test_request_checks.py)
cover each row below. The [HTTP tests](../tests/test_gateway.py) check the response
and authorization headers; [external-backend tests](../tests/test_salesforce_gateway.py)
cover disabled writes.

| Reason | HTTP status | JSON-RPC code |
| --- | --- | --- |
| `request_too_large` | 413 | -32600 |
| `invalid_json` | 400 | -32700 |
| `untrusted_origin_or_host` | 403 | 1003 |
| `invalid_token` | 401 | 1001 |
| `rate_limited` | 429 | 1002 |
| `insufficient_scope` | 403 | 1004 |
| `crm_write_disabled` | 403 | 1005 |
| `invalid_arguments` | 400 | -32602 |
| `missing_protocol_version` | 400 | -32602 |
| `protocol_version_mismatch` | 400 | -32020 |
| `unsupported_protocol_version` | 400 | -32022 |

Missing, malformed, expired, and wrong-resource credentials share `invalid_token`.
The response does not reveal which credential check failed. Scope challenges still
name the required grant in `WWW-Authenticate`; rate limits still send `Retry-After`.
Messages and HTTP statuses are unchanged. The three 403 refusals now have distinct
application codes instead of sharing `1001`.

This table covers the gateway's request checks. SDK rejections, OAuth errors,
and tool execution errors retain their own formats. The two audit or execution
failure responses remain HTTP 503 with an error string, without `error.data.reason`.
An activity may already have committed before the latter response. See
[audit failure handling](audit.md#audit-failures) before retrying a write.

## Local authorization

The local owner approves explicit scopes for one pre-registered public CLI
client. S256 PKCE requires the code exchange to present a verifier matching the
original challenge. Codes expire after 60 seconds and can be used once; opaque tokens last five minutes.
The flow binds access to the resource and checks state and issuer.

Both services use HTTPS on `127.0.0.1`. The client trusts only the locally
generated certificate without changing system trust or disabling verification.
Browser origins are rejected. A restart invalidates all tokens and
authorization codes. Refresh tokens and dynamic client registration are not
supported.

The generated credential lives in owner-only `owner.json`. Anyone who can read
it can request every supported scope. The CLI validates the authorization
redirect without a browser or callback listener. This is not production consent.

## Keep the services running

You can start the installed command and leave the services running:

```sh
gtm-connector serve
```

The command prints the connector address, backend, state folder, granted scopes,
connection command, and stop instruction on stderr. Startup grants no scopes.
Stdout receives one line per observed tool attempt with time, tool, caller
fingerprint, decision, duration, receipt ID, and any refusal reason. It excludes
raw arguments, source text, and bearer credentials. Fingerprints do not identify
people or establish a valid grant.

For a script, `serve --json` writes a `type: "ready"` startup line with
`resource`, `issuer`, `crm_backend`, `owner_config`, `state_dir`, and
`granted_scopes: []`, followed by `type: "tool_call"` lines. The startup object
contains paths, not the owner credential. Shutdown notices remain on stderr.
The complete [stream contract](cli.md#server-output) describes nullable fields
and its best-effort delivery; durable receipts remain the audit record.

In a second terminal, inspect setup and create an explicit Inspector grant:

```sh
gtm-connector doctor
gtm-connector connect inspector --scope crm:read
```

The [client setup guide](clients.md) gives the exact commands, tested status,
and missing interactive login requirements for each supported setup target.

For Python development from a repository checkout, the included client can also
request a CRM read:

```sh
.venv/bin/python - <<'PY'
import json
from gtm_connector.client import LocalClient
with open('var/v2/owner.json') as source:
    config = json.load(source)
with LocalClient(config) as client:
    token = client.authorize(['crm:read'])
    print(client.call(token, 'get_account', {'account_id': 'acc-001'}).json())
PY
```

The services stop when you press Ctrl-C. The file owner can inspect SQLite directly and
change the fixtures, so local file access remains the trust boundary. Keep
owner configuration and bearer tokens out of model inputs. The
[security review](../SECURITY_REVIEW.md) describes the remaining boundaries.
