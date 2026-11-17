# Protocol and authorization

The connector grants client access through a local owner emulator, without
production login or user identity.

The server implements [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28)
and its [authorization section, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).
Both were read on **2026-11-17**. The official Python SDK is pinned to **2.3.0**.

The transport is stateless Streamable HTTP with per-request protocol metadata
and `server/discover`. Only this protocol revision is accepted.
Subscriptions and legacy initialization sessions are not supported.
[DECISIONS.md](../DECISIONS.md) explains the checks the gateway adds to the SDK.

The official Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an
explicit bearer grant. It sends `server/discover`
with matching version headers and per-request protocol metadata. See
[the Inspector walkthrough](inspector.md) to reproduce the exchange. This proves
MCP transport and tool access. Inspector's interactive OAuth login and
untested assistant applications remain unverified. Compatibility with arbitrary
clients is not claimed.

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

You can start the services from the repository root after setup and leave them running:

```sh
.venv/bin/python -m gtm_connector serve
```

The command prints the public endpoints and the path to the owner configuration.
In a second terminal, use the included client to request a CRM read:

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
