# Protocol and authorization

The server implements [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28)
and its [authorization section, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).
The official Python SDK is pinned to **2.3.0**.

```sh
gtm-connector serve
gtm-connector doctor
gtm-connector connect inspector --scope crm:read
```

Leave `serve` running and use a second terminal for the other two, which
check the setup and create an explicit grant. Ctrl-C stops the services.
[Client setup](clients.md) covers each client.

`serve` grants no scopes at startup. Stdout gets one line per tool attempt:
time, tool, caller fingerprint, decision, duration, receipt ID, and any
refusal reason. It never holds arguments, source text, or bearer tokens. With
`--json`, a `type: "ready"` line with `resource`, `issuer`, `crm_backend`,
`owner_config`, `state_dir`, `granted_scopes: []`, and `sources` comes first,
then `type: "tool_call"` lines. See [the CLI contract](cli.md). The stream is
best effort, and the audit receipts remain the record.

## Rules

- The transport is stateless Streamable HTTP with per-request protocol
  metadata and `server/discover`. Only revision 2026-07-28 is accepted.
  Subscriptions and legacy initialization sessions are not supported.
- Both services use HTTPS on `127.0.0.1` with a locally generated certificate.
  Requests with an `Origin` header or a body over 65,536 bytes are refused.
- Authorization is a local owner emulator, not production login or consent.
  The owner approves explicit scopes for one pre-registered public CLI client.
- S256 PKCE, state, and issuer are checked. A code lasts 60 seconds and works
  once. A token is opaque, bound to the resource, and lasts five minutes.
- A restart invalidates every code and token. There are no refresh tokens and
  no dynamic client registration.
- Anyone who can read owner-only `owner.json` can request every scope. Keep
  it and bearer tokens out of model inputs. Local file access is the trust
  boundary. See the [security review](../SECURITY_REVIEW.md).
- Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an explicit
  bearer grant. See [Inspector](inspector.md). Inspector's interactive OAuth
  login and untested assistant applications are unverified.

## Request refusals

A gateway refusal carries `error.data.reason`.

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

- A missing, malformed, expired, or wrong-resource credential returns
  `invalid_token` without saying which check failed.
- `insufficient_scope` names the scope in `WWW-Authenticate`. `rate_limited`
  sends `Retry-After`.
- SDK rejections, OAuth errors, and tool errors keep their own formats.
- An audit or execution failure returns HTTP 503 with an error string and no
  reason. An activity may already have committed. Read [audit](audit.md)
  before retrying a write.
