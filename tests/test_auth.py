"""OAuth requests go through SDK handlers and the local authority boundary."""

import asyncio
import base64
import hashlib
import secrets
import time
import unittest
from urllib.parse import parse_qs, urlencode, urlsplit

from mcp.server.auth.provider import TokenError
from starlette.testclient import TestClient

from gtm_connector.auth import LocalAuthority, SCOPES, create_auth_app


class AuthorizationTests(unittest.TestCase):
    def setUp(self):
        self.authority = LocalAuthority("https://127.0.0.1:8766", "https://127.0.0.1:8765/mcp")
        self.client = TestClient(create_auth_app(self.authority), base_url=self.authority.issuer,
                                 follow_redirects=False)
        self.addCleanup(self.client.close)
        self.verifier = secrets.token_urlsafe(48)
        self.challenge = base64.urlsafe_b64encode(hashlib.sha256(self.verifier.encode()).digest()).decode().rstrip("=")

    def parameters(self, **changes):
        params = {
            "client_id": self.authority.client_id, "response_type": "code",
            "redirect_uri": self.authority.redirect_uri, "resource": self.authority.resource,
            "scope": "crm:read calls:read", "state": "local-demo-state",
            "code_challenge": self.challenge, "code_challenge_method": "S256",
        }
        params.update(changes)
        return params

    def authorize(self, **changes):
        return self.client.get("/authorize", params=self.parameters(**changes),
                               headers={"X-Demo-Owner": self.authority.owner_secret})

    def code(self):
        response = self.authorize()
        self.assertEqual(response.status_code, 302, response.text)
        query = parse_qs(urlsplit(response.headers["location"]).query)
        self.assertEqual(query["state"], ["local-demo-state"])
        self.assertEqual(query["iss"], [self.authority.issuer])
        return query["code"][0]

    def exchange(self, code, **changes):
        data = {"grant_type": "authorization_code", "client_id": self.authority.client_id,
                "code": code, "code_verifier": self.verifier,
                "redirect_uri": self.authority.redirect_uri, "resource": self.authority.resource}
        data.update(changes)
        return self.client.post("/token", data=data)

    def test_metadata_matches_public_client_and_supported_grants(self):
        metadata = self.client.get("/.well-known/oauth-authorization-server").json()
        self.assertEqual(metadata["issuer"], self.authority.issuer)
        self.assertEqual(metadata["scopes_supported"], list(SCOPES))
        self.assertEqual(metadata["grant_types_supported"], ["authorization_code"])
        self.assertEqual(metadata["token_endpoint_auth_methods_supported"], ["none"])
        self.assertEqual(metadata["code_challenge_methods_supported"], ["S256"])
        self.assertTrue(metadata["authorization_response_iss_parameter_supported"])
        self.assertNotIn("registration_endpoint", metadata)
        self.assertEqual(self.client.post("/register", json={}).status_code, 404)

    def test_owner_approval_is_required_for_get_and_post(self):
        for method in ("get", "post"):
            for secret in (None, "wrong-owner"):
                with self.subTest(method=method, secret=secret):
                    kwargs = {"params" if method == "get" else "data": self.parameters()}
                    if secret is not None:
                        kwargs["headers"] = {"X-Demo-Owner": secret}
                    response = getattr(self.client, method)("/authorize", **kwargs)
                    self.assertEqual(response.status_code, 401)
                    self.assertEqual(response.json()["error"], "access_denied")
        self.assertEqual(self.authority.codes, {})

    def test_success_is_scoped_resource_bound_short_lived_and_has_no_refresh(self):
        response = self.exchange(self.code())
        self.assertEqual(response.status_code, 200, response.text)
        issued = response.json()
        self.assertEqual(issued["token_type"], "Bearer")
        self.assertEqual(issued["expires_in"], 300)
        self.assertEqual(set(issued["scope"].split()), {"crm:read", "calls:read"})
        self.assertNotIn("refresh_token", issued)
        self.assertEqual(response.headers["cache-control"], "no-store")
        token = asyncio.run(self.authority.verify_token(issued["access_token"]))
        self.assertEqual(token.resource, self.authority.resource)
        self.assertNotIn("crm:write", token.scopes)
        self.assertGreater(token.expires_at, time.time())

    def test_post_authorization_requires_an_explicit_scope_choice(self):
        headers = {"X-Demo-Owner": self.authority.owner_secret}
        for value in (None, "", " "):
            params = self.parameters(scope=value)
            if value is None:
                del params["scope"]
            response = self.client.post("/authorize", data=params, headers=headers)
            self.assertEqual(response.status_code, 400)
            self.assertEqual(response.json()["error"], "invalid_scope")
        self.assertEqual(self.authority.codes, {})
        response = self.client.post("/authorize", data=self.parameters(scope="crm:read"), headers=headers)
        self.assertEqual(response.status_code, 302)
        code = parse_qs(urlsplit(response.headers["location"]).query)["code"][0]
        self.assertEqual(self.exchange(code).json()["scope"], "crm:read")

    def test_wrong_verifier_and_replayed_code_are_rejected(self):
        code = self.code()
        wrong = self.exchange(code, code_verifier="x" * 64)
        self.assertEqual(wrong.status_code, 400)
        self.assertEqual(wrong.json()["error"], "invalid_grant")
        self.assertEqual(self.exchange(code).status_code, 200)
        replay = self.exchange(code)
        self.assertEqual(replay.status_code, 400)
        self.assertEqual(replay.json()["error"], "invalid_grant")
        self.assertEqual(len(self.authority.tokens), 1)

    def test_code_exchange_is_single_use_under_concurrency(self):
        code = self.code()

        async def redeem_twice():
            record = await self.authority.load_authorization_code(self.authority.client, code)
            return await asyncio.gather(
                self.authority.exchange_authorization_code(self.authority.client, record),
                self.authority.exchange_authorization_code(self.authority.client, record),
                return_exceptions=True,
            )

        outcomes = asyncio.run(redeem_twice())
        self.assertEqual(sum(isinstance(result, TokenError) for result in outcomes), 1)
        self.assertEqual(len(self.authority.tokens), 1)

    def test_expired_authorization_code_cannot_be_redeemed(self):
        code = self.code()
        self.authority.codes[code].expires_at = time.time() - 1
        response = self.exchange(code)
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"], "invalid_grant")
        self.assertEqual(self.authority.tokens, {})

    def test_scope_redirect_resource_client_and_pkce_are_validated(self):
        response = self.authorize(scope="crm:admin")
        self.assertEqual(response.status_code, 302)
        error = parse_qs(urlsplit(response.headers["location"]).query)
        self.assertEqual(error["error"], ["invalid_scope"])
        self.assertEqual(error["iss"], [self.authority.issuer])
        for changes in (
            {"redirect_uri": "http://127.0.0.1:8768/callback"},
            {"redirect_uri": "https://attacker.example/callback"},
            {"resource": "https://127.0.0.1:9999/mcp"},
            {"resource": ""}, {"client_id": "unregistered-client"},
            {"code_challenge_method": "plain"}, {"code_challenge": "short"},
            {"state": ""},
        ):
            with self.subTest(changes=changes):
                self.assertEqual(self.authorize(**changes).status_code, 400)
        self.assertEqual(self.authority.codes, {})

    def test_token_request_requires_exact_resource_and_redirect(self):
        code = self.code()
        for changes in ({"resource": ""}, {"resource": "https://other.example/mcp"},
                        {"redirect_uri": "http://127.0.0.1:8768/callback"},
                        {"code_verifier": "too-short"}):
            with self.subTest(changes=changes):
                response = self.exchange(code, **changes)
                self.assertEqual(response.status_code, 400)
        self.assertEqual(self.authority.tokens, {})
        missing_resource = {"grant_type": "authorization_code", "client_id": self.authority.client_id,
                            "code": code, "code_verifier": self.verifier,
                            "redirect_uri": self.authority.redirect_uri}
        self.assertEqual(self.client.post("/token", data=missing_resource).status_code, 400)
        self.assertEqual(self.exchange(code).status_code, 200)

    def test_invalid_expired_wrong_audience_and_wrong_issuer_tokens_fail(self):
        token = self.exchange(self.code()).json()["access_token"]
        for raw in ("", "malformed.token", secrets.token_urlsafe(32)):
            self.assertIsNone(asyncio.run(self.authority.verify_token(raw)))
        stored = self.authority.tokens[token]
        for field, value in (("expires_at", int(time.time()) - 1),
                             ("resource", "https://other.example/mcp"),
                             ("claims", {"iss": "https://other.example"})):
            with self.subTest(field=field):
                original = getattr(stored, field)
                setattr(stored, field, value)
                self.assertIsNone(asyncio.run(self.authority.verify_token(token)))
                setattr(stored, field, original)
        self.assertIsNotNone(asyncio.run(self.authority.verify_token(token)))

    def test_http_wrong_host_browser_origin_and_duplicate_parameters_fail(self):
        for headers in ({"Host": "attacker.example"}, {"Origin": "https://attacker.example"}):
            self.assertIn(self.client.get("/.well-known/oauth-authorization-server", headers=headers).status_code,
                          (400, 403))
        with TestClient(create_auth_app(self.authority), base_url="http://127.0.0.1:8766") as client:
            self.assertEqual(client.get("/.well-known/oauth-authorization-server").status_code, 400)
        query = urlencode(self.parameters()) + "&scope=crm%3Awrite"
        response = self.client.get("/authorize?" + query,
                                   headers={"X-Demo-Owner": self.authority.owner_secret})
        self.assertEqual(response.status_code, 400)
        body = urlencode({"grant_type": "authorization_code", "resource": self.authority.resource})
        body += "&resource=" + self.authority.resource
        response = self.client.post("/token", content=body,
                                    headers={"Content-Type": "application/x-www-form-urlencoded"})
        self.assertEqual(response.status_code, 400)

    def test_refresh_is_not_supported_and_plaintext_configuration_is_rejected(self):
        response = self.client.post("/token", data={
            "grant_type": "refresh_token", "client_id": self.authority.client_id,
            "resource": self.authority.resource, "redirect_uri": self.authority.redirect_uri,
            "refresh_token": "not-issued",
        })
        self.assertEqual(response.status_code, 400)
        self.assertEqual(response.json()["error"], "unsupported_grant_type")
        with self.assertRaises(ValueError):
            LocalAuthority("http://127.0.0.1:8766", self.authority.resource)
        with self.assertRaises(ValueError):
            LocalAuthority(self.authority.issuer, "http://127.0.0.1:8765/mcp")


if __name__ == "__main__":
    unittest.main()
