"""Synchronous CLI client for this connector's pinned local OAuth and MCP endpoints."""

import base64
import hashlib
import re
import secrets
import ssl
from urllib.parse import parse_qsl, urlsplit, urlunsplit

import httpx2

from .auth import SCOPES
from .sf_credentials import CREDENTIAL_SECONDS
from .tool_definitions import TOOL_DEFINITIONS

# The tools that read the CRM. read_renewal_brief only returns a saved brief.
_CRM_TOOLS = frozenset(name for name, tool in TOOL_DEFINITIONS.items()
                       if tool.scope == "crm:read") - {"read_renewal_brief"}


PROTOCOL_VERSION = "2026-07-28"
_OPAQUE = re.compile(r"[A-Za-z0-9_-]{43}\Z")


class AuthorizationError(RuntimeError):
    """An authorization response did not satisfy the local client's policy."""


class LocalClient:
    def __init__(self, config: dict, *, transport=None):
        self.issuer = config["issuer"]
        self.resource = config["resource"]
        self.redirect_uri = config["redirect_uri"]
        self.client_id = config["client_id"]
        self._owner_secret = config["owner_secret"]
        for url in (self.issuer, self.resource):
            parts = urlsplit(url)
            if (parts.scheme != "https" or parts.hostname != "127.0.0.1"
                    or parts.username is not None or parts.password is not None
                    or parts.query or parts.fragment):
                raise ValueError("The client requires HTTPS loopback endpoints.")
            parts.port  # Reject an invalid port before making any request.
        issuer, resource = urlsplit(self.issuer), urlsplit(self.resource)
        callback = urlsplit(self.redirect_uri)
        if (issuer.path or resource.path != "/mcp" or self.client_id != "gtm-demo-cli"
                or callback.scheme != "http" or callback.hostname != "127.0.0.1"
                or callback.username is not None or callback.password is not None
                or not callback.path or callback.query or callback.fragment
                or not isinstance(self._owner_secret, str) or not self._owner_secret):
            raise ValueError("Invalid local client configuration.")
        callback.port
        self._resource_metadata_url = urlunsplit(resource._replace(
            path="/.well-known/oauth-protected-resource" + resource.path))
        context = ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)
        context.load_verify_locations(cafile=str(config["cert_path"]))
        # A Salesforce or Slack read can take 20 seconds. Let the gateway finish the read and
        # its receipt before timing out.
        self._salesforce = config.get("crm_backend") == "salesforce"
        timeout = httpx2.Timeout(10, read=25 if self._salesforce or config.get("slack_backend") == "slack" else 10)
        self._http = httpx2.Client(verify=context, trust_env=False, follow_redirects=False,
                                   timeout=timeout, transport=transport)
        self._request_id = 0

    def close(self):
        self._http.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()

    @staticmethod
    def _object(response, label):
        if response.status_code != 200:
            raise AuthorizationError(f"{label} returned HTTP {response.status_code}.")
        try:
            value = response.json()
        except ValueError:
            raise AuthorizationError(f"{label} did not return JSON.") from None
        if not isinstance(value, dict):
            raise AuthorizationError(f"{label} did not return an object.")
        return value

    def authorize(self, scopes: list[str]) -> str:
        if (not isinstance(scopes, list) or not scopes
                or any(not isinstance(scope, str) or scope not in SCOPES for scope in scopes)):
            raise ValueError("Request one or more supported scopes explicitly.")
        requested = sorted(set(scopes))
        protected = self._object(self._http.get(self._resource_metadata_url), "Resource metadata")
        if (protected.get("resource") != self.resource
                or protected.get("authorization_servers") != [self.issuer]
                or protected.get("bearer_methods_supported") != ["header"]):
            raise AuthorizationError("Resource metadata does not match the configured connector.")
        metadata = self._object(self._http.get(self.issuer + "/.well-known/oauth-authorization-server"),
                                "Authorization metadata")
        expected = {
            "issuer": self.issuer,
            "authorization_endpoint": self.issuer + "/authorize",
            "token_endpoint": self.issuer + "/token",
            "code_challenge_methods_supported": ["S256"],
            "response_types_supported": ["code"],
            "grant_types_supported": ["authorization_code"],
            "token_endpoint_auth_methods_supported": ["none"],
            "authorization_response_iss_parameter_supported": True,
        }
        if any(metadata.get(key) != value for key, value in expected.items()):
            raise AuthorizationError("Authorization metadata does not match the configured authority.")
        supported = metadata.get("scopes_supported")
        if not isinstance(supported, list) or any(scope not in supported for scope in requested):
            raise AuthorizationError("The authority does not support the requested scopes.")

        verifier, state = secrets.token_urlsafe(48), secrets.token_urlsafe(32)
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).decode().rstrip("=")
        response = self._http.get(self.issuer + "/authorize", params={
            "response_type": "code", "client_id": self.client_id, "redirect_uri": self.redirect_uri,
            "resource": self.resource, "scope": " ".join(requested), "state": state,
            "code_challenge": challenge, "code_challenge_method": "S256",
        }, headers={"X-Demo-Owner": self._owner_secret})
        if response.status_code != 302:
            raise AuthorizationError(f"Owner authorization returned HTTP {response.status_code}.")
        location = urlsplit(response.headers.get("location", ""))
        if urlunsplit(location._replace(query="")) != self.redirect_uri:
            raise AuthorizationError("Authorization returned an unexpected callback URI.")
        parameters = parse_qsl(location.query, keep_blank_values=True)
        values = dict(parameters)
        if (len(parameters) != len(values) or values.get("state") != state
                or values.get("iss") != self.issuer):
            raise AuthorizationError("Authorization state or issuer did not match.")
        if "error" in values or not _OPAQUE.fullmatch(values.get("code", "")):
            raise AuthorizationError("The local authority did not grant an authorization code.")
        result = self._object(self._http.post(self.issuer + "/token", data={
            "grant_type": "authorization_code", "client_id": self.client_id,
            "redirect_uri": self.redirect_uri, "resource": self.resource,
            "code": values["code"], "code_verifier": verifier,
        }), "Token exchange")
        token, granted, lifetime = result.get("access_token"), result.get("scope"), result.get("expires_in")
        if (not isinstance(token, str) or not _OPAQUE.fullmatch(token)
                or not isinstance(granted, str) or set(granted.split()) != set(requested)
                or not isinstance(result.get("token_type"), str)
                or result["token_type"].lower() != "bearer"
                or type(lifetime) is not int or not 1 <= lifetime <= 3600
                or "refresh_token" in result):
            raise AuthorizationError("The token response did not match the requested grant.")
        return token

    def rpc(self, token: str, method: str, params: dict | None = None,
            brief_id: str | None = None) -> httpx2.Response:
        supplied = dict(params or {})
        meta = dict(supplied.get("_meta", {}))
        meta.update({
            "io.modelcontextprotocol/protocolVersion": PROTOCOL_VERSION,
            "io.modelcontextprotocol/clientCapabilities": {},
            "io.modelcontextprotocol/clientInfo": {"name": "gtm-demo-cli", "version": "0.2.0"},
        })
        if method == "tools/call" and brief_id is not None:
            if not isinstance(brief_id, str) or not 1 <= len(brief_id) <= 128:
                raise ValueError("Brief IDs must contain 1 to 128 characters.")
            meta["gtm.demo/briefId"] = brief_id
        supplied["_meta"] = meta
        headers = {"Authorization": "Bearer " + token,
                   "Accept": "application/json, text/event-stream",
                   "MCP-Protocol-Version": PROTOCOL_VERSION, "Mcp-Method": method}
        if method == "tools/call":
            headers["Mcp-Name"] = supplied["name"]
        self._request_id += 1
        timeout = self._http.timeout
        if self._salesforce and method == "tools/call" and supplied["name"] in _CRM_TOOLS:
            # A CRM read can also wait for sf to refresh the login.
            timeout = httpx2.Timeout(10, read=CREDENTIAL_SECONDS + 25)
        return self._http.post(self.resource, headers=headers, timeout=timeout, json={
            "jsonrpc": "2.0", "id": self._request_id, "method": method, "params": supplied})

    def call(self, token: str, tool: str, arguments: dict,
             brief_id: str | None = None) -> httpx2.Response:
        return self.rpc(token, "tools/call", {"name": tool, "arguments": arguments}, brief_id)
