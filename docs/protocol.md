# Protocol and authorization

The server implements [MCP revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28)
and its [authorization section, revision 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).
Both were read on **2026-11-17**. The official Python SDK is pinned to **2.3.0**.

The transport is stateless Streamable HTTP with per-request protocol metadata
and `server/discover`. The server accepts only this protocol revision. It does
not support subscriptions or legacy initialization sessions. Compatibility with
arbitrary clients is not claimed. [DECISIONS.md](../DECISIONS.md) explains the
checks the gateway adds to the SDK.

The official Inspector CLI 2.15.0 is tested with `protocolEra: modern` and an
explicit bearer grant. It sends `server/discover`, matching version headers,
and per-request protocol metadata. See [the Inspector walkthrough](inspector.md).
This proves MCP transport and tool access, not its interactive OAuth login or
compatibility with an untested assistant application.

## Local authorization

The flow uses one pre-registered public CLI client, explicit scope approval,
and S256 PKCE. Authorization codes expire after 60 seconds and can be used once.
Opaque access tokens last five minutes. The flow binds access to the resource
and checks state and issuer.

Both services use HTTPS on `127.0.0.1`. The client trusts only the locally
generated certificate. It does not change system trust or disable verification.
The connector rejects browser origins. Restarting invalidates all tokens and
authorization codes. Refresh tokens and dynamic client registration are not
supported.

The resource owner is a local fixture emulator. Its generated credential lives
in owner-only `owner.json`. Anyone who can read it can request every supported
scope. The CLI validates the authorization redirect directly, without opening
a browser or running a callback listener. This does not provide production
login, consent, or user identity.

## Keep the services running

After setup, run this from the repository root:

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

Stop the services with Ctrl-C. Keep owner configuration and bearer tokens out
of model inputs. Local file access is the trust boundary. The file owner can
inspect SQLite directly and change the fixtures. See the
[security review](../SECURITY_REVIEW.md) for the remaining boundaries.
