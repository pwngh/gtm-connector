"""A hostile-discovery fixture verifies that the CLI never follows supplied URLs."""

import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import ssl
import tempfile
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlencode, urlsplit

from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.x509.oid import NameOID
import httpx2

from gtm_connector.auth import SCOPES
from gtm_connector.client import AuthorizationError, LocalClient, PROTOCOL_VERSION


class ClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.directory.cleanup)
        cls.cert_path = Path(cls.directory.name) / "test-ca.pem"
        key = ec.generate_private_key(ec.SECP256R1())
        name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, "local-client-test")])
        now = datetime.now(timezone.utc)
        cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
                .public_key(key.public_key()).serial_number(x509.random_serial_number())
                .not_valid_before(now - timedelta(minutes=1)).not_valid_after(now + timedelta(days=1))
                .add_extension(x509.BasicConstraints(ca=True, path_length=None), critical=True)
                .sign(key, hashes.SHA256()))
        cls.cert_path.write_bytes(cert.public_bytes(serialization.Encoding.PEM))

    def setUp(self):
        self.config = {
            "issuer": "https://127.0.0.1:8766", "resource": "https://127.0.0.1:8765/mcp",
            "redirect_uri": "http://127.0.0.1:8767/callback", "client_id": "gtm-demo-cli",
            "owner_secret": "local-fixture-owner-credential", "cert_path": self.cert_path,
        }
        issuer = self.config["issuer"]
        self.protected = {"resource": self.config["resource"], "authorization_servers": [issuer],
                          "bearer_methods_supported": ["header"]}
        self.metadata = {
            "issuer": issuer, "authorization_endpoint": issuer + "/authorize",
            "token_endpoint": issuer + "/token", "code_challenge_methods_supported": ["S256"],
            "response_types_supported": ["code"], "grant_types_supported": ["authorization_code"],
            "token_endpoint_auth_methods_supported": ["none"],
            "authorization_response_iss_parameter_supported": True, "scopes_supported": list(SCOPES),
        }
        self.token = {"access_token": "t" * 43, "token_type": "Bearer", "expires_in": 300,
                      "scope": "crm:read"}
        self.requests = []
        self.callback_changes = {}
        self.callback_base = self.config["redirect_uri"]
        self.callback_suffix = ""
        self.rpc_status = 200
        self.client = self.enterContext(LocalClient(self.config, transport=httpx2.MockTransport(self.respond)))

    def respond(self, request):
        self.requests.append(request)
        path = request.url.path
        if path == "/.well-known/oauth-protected-resource/mcp":
            return httpx2.Response(200, json=self.protected)
        if path == "/.well-known/oauth-authorization-server":
            return httpx2.Response(200, json=self.metadata)
        if path == "/authorize":
            self.authorization = dict(request.url.params)
            values = {"code": "c" * 43, "state": self.authorization["state"],
                      "iss": self.config["issuer"], **self.callback_changes}
            return httpx2.Response(302, headers={
                "Location": self.callback_base + "?" + urlencode(values) + self.callback_suffix})
        if path == "/token":
            self.exchange = {key: value[0] for key, value in parse_qs(request.content.decode()).items()}
            return httpx2.Response(200, json=self.token)
        if path == "/mcp":
            return httpx2.Response(self.rpc_status, json={"result": {}})
        self.fail("Client requested an unexpected endpoint.")

    def test_pkce_resource_state_issuer_and_scope_are_checked(self):
        self.assertEqual(self.client.authorize(["crm:read"]), "t" * 43)
        self.assertEqual(self.authorization["resource"], self.config["resource"])
        self.assertEqual(self.authorization["scope"], "crm:read")
        self.assertEqual(self.authorization["code_challenge_method"], "S256")
        self.assertEqual(self.exchange["resource"], self.config["resource"])
        self.assertEqual(self.exchange["redirect_uri"], self.config["redirect_uri"])
        self.assertEqual(self.exchange["client_id"], self.config["client_id"])
        challenge = base64.urlsafe_b64encode(hashlib.sha256(self.exchange["code_verifier"].encode()).digest())
        self.assertEqual(challenge.decode().rstrip("="), self.authorization["code_challenge"])
        for request in self.requests:
            self.assertEqual(request.headers.get("X-Demo-Owner"),
                             self.config["owner_secret"] if request.url.path == "/authorize" else None)
            self.assertNotIn("authorization", request.headers)
            self.assertNotIn(self.config["owner_secret"], str(request.url))
        self.assertFalse(any(request.url.scheme == "http" for request in self.requests))

    def test_discovery_cannot_change_resource_issuer_or_endpoints(self):
        cases = ((self.protected, "resource", "https://other.example/mcp"),
                 (self.protected, "authorization_servers", ["https://other.example"]),
                 (self.metadata, "issuer", "https://other.example"),
                 (self.metadata, "authorization_endpoint", "https://other.example/authorize"),
                 (self.metadata, "token_endpoint", "https://other.example/token"),
                 (self.metadata, "code_challenge_methods_supported", ["plain"]),
                 (self.metadata, "authorization_response_iss_parameter_supported", False))
        for document, field, bad in cases:
            with self.subTest(field=field):
                previous = document[field]
                document[field] = bad
                self.requests.clear()
                with self.assertRaises(AuthorizationError):
                    self.client.authorize(["crm:read"])
                self.assertFalse(any(request.url.path in ("/authorize", "/token") for request in self.requests))
                self.assertTrue(all(request.url.host == "127.0.0.1" for request in self.requests))
                document[field] = previous

    def test_callback_state_issuer_redirect_fragment_and_duplicates_fail_before_exchange(self):
        for field, bad in (("state", "wrong-state"), ("iss", "https://other.example"),
                           ("code", ""), ("error", "access_denied")):
            with self.subTest(field=field):
                self.callback_changes = {field: bad}
                self.requests.clear()
                with self.assertRaises(AuthorizationError):
                    self.client.authorize(["crm:read"])
                self.assertFalse(any(request.url.path == "/token" for request in self.requests))
        self.callback_changes = {}
        for callback, suffix in (("https://other.example/callback", ""),
                                 ("http://127.0.0.1:9999/callback", ""),
                                 (self.config["redirect_uri"], "#unexpected"),
                                 (self.config["redirect_uri"], "&code=duplicate")):
            with self.subTest(callback=callback, suffix=suffix):
                self.callback_base, self.callback_suffix = callback, suffix
                self.requests.clear()
                with self.assertRaises(AuthorizationError):
                    self.client.authorize(["crm:read"])
                self.assertFalse(any(request.url.path == "/token" for request in self.requests))

    def test_token_must_match_requested_grant(self):
        for field, bad in (("scope", "crm:read crm:write"), ("scope", ""),
                           ("access_token", "malformed"), ("token_type", None),
                           ("expires_in", 0), ("expires_in", True), ("refresh_token", "unexpected")):
            with self.subTest(field=field):
                previous = self.token.get(field)
                self.token[field] = bad
                with self.assertRaises(AuthorizationError):
                    self.client.authorize(["crm:read"])
                if previous is None:
                    self.token.pop(field)
                else:
                    self.token[field] = previous

    def test_scope_choice_is_explicit_and_invalid_scope_makes_no_request(self):
        for scopes in ([], ["crm:admin"], "crm:read", [None]):
            with self.subTest(scopes=scopes), self.assertRaises(ValueError):
                self.client.authorize(scopes)
        self.assertEqual(self.requests, [])

    def test_rpc_carries_2026_metadata_and_returns_denials_without_retry(self):
        arguments = {"account_id": "acc-001"}
        response = self.client.call("t" * 43, "get_account", arguments, "brief-001")
        self.assertEqual(response.status_code, 200)
        request = self.requests[-1]
        payload = json.loads(request.content)
        self.assertEqual(request.headers["Authorization"], "Bearer " + "t" * 43)
        self.assertEqual(request.headers["MCP-Protocol-Version"], PROTOCOL_VERSION)
        self.assertEqual(request.headers["Mcp-Method"], "tools/call")
        self.assertEqual(request.headers["Mcp-Name"], "get_account")
        self.assertNotIn("X-Demo-Owner", request.headers)
        self.assertEqual(payload["params"]["arguments"], arguments)
        self.assertEqual(payload["params"]["_meta"]["gtm.demo/briefId"], "brief-001")
        self.assertEqual(payload["params"]["_meta"]["io.modelcontextprotocol/protocolVersion"], PROTOCOL_VERSION)
        self.rpc_status = 429
        response = self.client.rpc("t" * 43, "tools/list")
        self.assertEqual(response.status_code, 429)
        self.assertEqual(len(self.requests), 2)
        self.assertEqual(json.loads(self.requests[-1].content)["id"], payload["id"] + 1)

    def test_only_a_salesforce_crm_read_waits_long_enough_for_a_login_refresh(self):
        def waits(client, method, **params):
            client.rpc("t" * 43, method, params or None)
            return self.requests[-1].extensions["timeout"]["read"]

        salesforce = self.enterContext(LocalClient({**self.config, "crm_backend": "salesforce"},
                                                   transport=httpx2.MockTransport(self.respond)))
        for tool in ("get_account", "list_opportunities", "search_accounts", "prepare_renewal_brief"):
            self.assertEqual(waits(salesforce, "tools/call", name=tool, arguments={}), 205)
        for tool in ("read_renewal_brief", "read_call_notes", "read_slack_messages", "log_activity"):
            self.assertEqual(waits(salesforce, "tools/call", name=tool, arguments={}), 25)
        self.assertEqual(waits(salesforce, "tools/list"), 25)
        self.assertEqual(waits(self.client, "tools/call", name="get_account", arguments={}), 10)

    def test_tls_uses_explicit_trust_and_never_follows_redirects_or_environment_proxies(self):
        with patch("gtm_connector.client.httpx2.Client") as factory:
            with LocalClient(self.config):
                options = factory.call_args.kwargs
                context = options["verify"]
                self.assertEqual(context.verify_mode, ssl.CERT_REQUIRED)
                self.assertTrue(context.check_hostname)
                self.assertEqual(len(context.get_ca_certs()), 1)
                self.assertFalse(options["trust_env"])
                self.assertFalse(options["follow_redirects"])
            factory.return_value.close.assert_called_once()
        for field, value in (("issuer", "http://127.0.0.1:8766"),
                             ("resource", "https://other.example/mcp"),
                             ("redirect_uri", "https://other.example/callback")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                LocalClient({**self.config, field: value})


if __name__ == "__main__":
    unittest.main()
