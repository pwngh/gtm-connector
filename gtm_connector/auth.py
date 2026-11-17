"""Local OAuth emulator: one owner, one public client, no external identity service."""

import re
import secrets
import threading
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from mcp.server.auth.provider import (
    AccessToken, AuthorizationCode, AuthorizationParams, AuthorizeError, TokenError,
)
from mcp.server.auth.routes import create_auth_routes
from mcp.server.auth.settings import ClientRegistrationOptions
from mcp.shared.auth import OAuthClientInformationFull, OAuthToken
from pydantic import AnyHttpUrl, AnyUrl
from starlette.applications import Starlette
from starlette.responses import JSONResponse
from starlette.routing import Route


SCOPES = ("crm:read", "calls:read", "crm:write")
_OPAQUE_TOKEN = re.compile(r"[A-Za-z0-9_-]{43}\Z")
_VERIFIER = re.compile(r"[A-Za-z0-9._~-]{43,128}\Z")
_MAX_FORM_BYTES = 8192


def _https_loopback(url: str, *, origin: bool = False) -> str:
    parts = urlsplit(url)
    if (parts.scheme != "https" or parts.hostname != "127.0.0.1"
            or parts.username is not None or parts.password is not None
            or parts.query or parts.fragment or (origin and parts.path not in ("", "/"))):
        raise ValueError("Authorization and resource URLs must use HTTPS on 127.0.0.1.")
    # Accessing port also rejects malformed port values.
    if parts.port is not None and not 1 <= parts.port <= 65535:
        raise ValueError("Invalid loopback port.")
    return url.rstrip("/") if origin else url


class LocalAuthority:
    """A process-local issuer; restarting it invalidates all codes and tokens.

    The CLI explicitly approves requested scopes with a generated owner credential.
    That credential belongs in an owner-readable local file, never in a model input.
    This simulates the resource owner, not production user authentication.
    """

    client_id = "gtm-demo-cli"

    def __init__(self, issuer: str, resource: str,
                 redirect_uri: str = "http://127.0.0.1:8767/callback",
                 token_ttl: int = 300, owner_secret: str | None = None):
        self.issuer = _https_loopback(issuer, origin=True)
        self.resource = _https_loopback(resource)
        redirect = urlsplit(redirect_uri)
        if (redirect.scheme != "http" or redirect.hostname != "127.0.0.1"
                or redirect.username is not None or redirect.password is not None
                or not redirect.path or redirect.query or redirect.fragment):
            raise ValueError("The pre-registered redirect must be an HTTP loopback callback.")
        if redirect.port is not None and not 1 <= redirect.port <= 65535:
            raise ValueError("Invalid callback port.")
        if not isinstance(token_ttl, int) or not 1 <= token_ttl <= 3600:
            raise ValueError("Token lifetime must be 1 to 3600 seconds.")
        self.redirect_uri = redirect_uri
        self.token_ttl = token_ttl
        self.owner_secret = owner_secret or secrets.token_urlsafe(32)
        self.client = OAuthClientInformationFull(
            client_id=self.client_id, client_name="Local fixture CLI",
            redirect_uris=[AnyUrl(redirect_uri)], token_endpoint_auth_method="none",
            grant_types=["authorization_code"], response_types=["code"],
            scope=" ".join(SCOPES), application_type="native", issuer=self.issuer,
        )
        self.codes: dict[str, AuthorizationCode] = {}
        self.tokens: dict[str, AccessToken] = {}
        self._lock = threading.Lock()

    async def get_client(self, client_id: str):
        return self.client if client_id == self.client_id else None

    async def register_client(self, client_info):
        raise NotImplementedError("Dynamic client registration is disabled.")

    async def authorize(self, client, params: AuthorizationParams) -> str:
        if params.resource != self.resource:
            raise AuthorizeError("invalid_target", "Request this connector's resource URI.")
        if str(params.redirect_uri) != self.redirect_uri:
            raise AuthorizeError("invalid_request", "Redirect URI is not registered.")
        scopes = params.scopes or []
        if not scopes or not set(scopes) <= set(SCOPES):
            raise AuthorizeError("invalid_scope", "Request one or more supported scopes.")
        code = secrets.token_urlsafe(32)
        record = AuthorizationCode(
            code=code, scopes=sorted(set(scopes)), expires_at=time.time() + 60,
            client_id=client.client_id, code_challenge=params.code_challenge,
            redirect_uri=params.redirect_uri, redirect_uri_provided_explicitly=True,
            resource=self.resource,
        )
        with self._lock:
            self.codes[code] = record
        return self.redirect_uri + "?" + urlencode({
            "code": code, "state": params.state, "iss": self.issuer,
        })

    async def load_authorization_code(self, client, authorization_code: str):
        with self._lock:
            record = self.codes.get(authorization_code)
            if record is None or record.client_id != client.client_id:
                return None
            return record.model_copy(deep=True)

    async def exchange_authorization_code(self, client, authorization_code) -> OAuthToken:
        with self._lock:
            record = self.codes.pop(authorization_code.code, None)
            if (record is None or record.client_id != client.client_id
                    or record.expires_at <= time.time() or record.resource != self.resource):
                raise TokenError("invalid_grant", "Authorization code is expired or already used.")
            raw = secrets.token_urlsafe(32)
            self.tokens[raw] = AccessToken(
                token=raw, client_id=client.client_id, scopes=list(record.scopes),
                expires_at=int(time.time()) + self.token_ttl, resource=self.resource,
                claims={"iss": self.issuer},
            )
        return OAuthToken(access_token=raw, expires_in=self.token_ttl,
                          scope=" ".join(record.scopes))

    async def verify_token(self, raw: str) -> AccessToken | None:
        if not isinstance(raw, str) or not _OPAQUE_TOKEN.fullmatch(raw):
            return None
        with self._lock:
            token = self.tokens.get(raw)
            if (token is None or token.expires_at is None or token.expires_at <= time.time()
                    or token.resource != self.resource or token.client_id != self.client_id
                    or token.claims != {"iss": self.issuer}
                    or not set(token.scopes) <= set(SCOPES)):
                return None
            return token.model_copy(deep=True)

    async def load_access_token(self, token: str):
        return await self.verify_token(token)

    async def load_refresh_token(self, client, refresh_token: str):
        return None

    async def exchange_refresh_token(self, client, refresh_token, scopes):
        raise TokenError("unsupported_grant_type", "Refresh tokens are not issued.")

    async def revoke_token(self, token):
        with self._lock:
            self.tokens.pop(token.token, None)


class _AuthorizationBoundary:
    """Reject browser origins, ambiguous parameters, and unapproved owner requests."""

    def __init__(self, app, authority: LocalAuthority):
        self.app, self.authority = app, authority

    async def __call__(self, scope, receive, send):
        if scope["type"] != "http":
            return await self.app(scope, receive, send)

        async def reject(status, error, description):
            response = JSONResponse({"error": error, "error_description": description},
                                    status_code=status,
                                    headers={"Cache-Control": "no-store", "Pragma": "no-cache"})
            await response(scope, receive, send)

        pairs = scope.get("headers", [])
        headers = {key.lower(): value for key, value in pairs}
        if any(sum(key.lower() == name for key, _ in pairs) > 1
               for name in (b"host", b"origin", b"x-demo-owner", b"content-type")):
            return await reject(400, "invalid_request", "Duplicate security header.")
        expected_host = urlsplit(self.authority.issuer).netloc.encode("ascii")
        if scope.get("scheme") != "https" or headers.get(b"host", b"").lower() != expected_host:
            return await reject(400, "invalid_request", "Use this authority's HTTPS loopback URL.")
        if b"origin" in headers:
            return await reject(403, "access_denied", "Browser origins are not allowed; use the local CLI.")

        path, method = scope["path"], scope["method"]
        if path not in ("/authorize", "/token") or method not in ("GET", "POST"):
            return await self.app(scope, receive, send)
        if path == "/authorize":
            owner = headers.get(b"x-demo-owner", b"")
            if not secrets.compare_digest(owner, self.authority.owner_secret.encode()):
                return await reject(401, "access_denied", "Local owner approval is required.")

        raw = scope.get("query_string", b"")
        if method == "POST":
            if raw or headers.get(b"content-type", b"").split(b";", 1)[0] != b"application/x-www-form-urlencoded":
                return await reject(400, "invalid_request", "Use a form body without query parameters.")
            chunks = []
            size = 0
            while True:
                message = await receive()
                if message["type"] == "http.disconnect":
                    return
                chunk = message.get("body", b"")
                size += len(chunk)
                if size > _MAX_FORM_BYTES:
                    return await reject(413, "invalid_request", "OAuth request is too large.")
                chunks.append(chunk)
                if not message.get("more_body", False):
                    break
            raw = b"".join(chunks)
            original_receive = receive
            replayed = False

            async def receive():
                nonlocal replayed
                if not replayed:
                    replayed = True
                    return {"type": "http.request", "body": raw, "more_body": False}
                return await original_receive()

        try:
            if len(raw) > _MAX_FORM_BYTES:
                return await reject(413, "invalid_request", "OAuth request is too large.")
            parameters = parse_qsl(raw.decode("utf-8"), keep_blank_values=True, max_num_fields=20)
            params = dict(parameters)
            if len(params) != len(parameters):
                raise ValueError("Duplicate parameter.")
        except (UnicodeError, ValueError):
            return await reject(400, "invalid_request", "Invalid or duplicate OAuth parameter.")
        if params.get("resource") != self.authority.resource:
            return await reject(400, "invalid_target", "Request this connector's exact resource URI.")
        if params.get("redirect_uri") != self.authority.redirect_uri:
            return await reject(400, "invalid_request", "Use the exact registered redirect URI.")
        if path == "/authorize":
            # The SDK defaults an omitted scope to every client scope. Require an
            # explicit choice so this local approval never silently grants writes.
            if not params.get("scope", "").strip():
                return await reject(400, "invalid_scope", "Request explicit scopes.")
            if (params.get("code_challenge_method") != "S256"
                    or not _OPAQUE_TOKEN.fullmatch(params.get("code_challenge", ""))
                    or not 1 <= len(params.get("state", "")) <= 256):
                return await reject(400, "invalid_request", "S256 PKCE and state are required.")
        elif params.get("grant_type") == "authorization_code":
            if not _VERIFIER.fullmatch(params.get("code_verifier", "")):
                return await reject(400, "invalid_request", "Invalid PKCE verifier.")

        async def send_with_issuer(message):
            # The SDK adds state to authorization errors, but not RFC 9207 iss.
            if path == "/authorize" and message["type"] == "http.response.start":
                updated = []
                for key, value in message.get("headers", []):
                    if key.lower() == b"location":
                        location = urlsplit(value.decode("latin-1"))
                        query = [(k, v) for k, v in parse_qsl(location.query) if k != "iss"]
                        query.append(("iss", self.authority.issuer))
                        value = urlunsplit(location._replace(query=urlencode(query))).encode("latin-1")
                    updated.append((key, value))
                message = {**message, "headers": updated}
            await send(message)

        await self.app(scope, receive, send_with_issuer)


def create_auth_app(authority: LocalAuthority):
    async def metadata(request):
        return JSONResponse({
            "issuer": authority.issuer,
            "authorization_endpoint": authority.issuer + "/authorize",
            "token_endpoint": authority.issuer + "/token",
            "response_types_supported": ["code"],
            "grant_types_supported": ["authorization_code"],
            "scopes_supported": list(SCOPES),
            "code_challenge_methods_supported": ["S256"],
            "token_endpoint_auth_methods_supported": ["none"],
            "authorization_response_iss_parameter_supported": True,
        }, headers={"Cache-Control": "no-store"})

    routes = create_auth_routes(
        authority, AnyHttpUrl(authority.issuer),
        client_registration_options=ClientRegistrationOptions(enabled=False, valid_scopes=list(SCOPES)),
    )
    routes[0] = Route("/.well-known/oauth-authorization-server", metadata, methods=["GET"])
    return _AuthorizationBoundary(Starlette(routes=routes), authority)
