"""Actual localhost callbacks and HTTP streaming against an invented provider."""

import base64
from contextlib import contextmanager, redirect_stderr, redirect_stdout
import hashlib
from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import json
from pathlib import Path
import queue
import socket
import stat
import tempfile
import threading
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlencode, urlsplit

import httpx2 as httpx

from gtm_connector import slack_auth


CLIENT = "123456789.987654321"
APP = "A000000001"
TEAM = "T000000001"
USER = "U000000001"
ORIGIN = "https://invented-gtm.slack.com"
CODE = "invented-private-authorization-code"
TOKEN = "invented-private-access-token"


class RoutedStream(httpx.AsyncByteStream):
    def __init__(self, response, transport, owner):
        self.response, self.transport, self.owner = response, transport, owner

    async def __aiter__(self):
        async for chunk in self.response.stream:
            yield chunk

    async def aclose(self):
        try:
            await self.response.aclose()
        finally:
            await self.transport.aclose()
            self.owner.open_streams.discard(self)


class LoopbackTransport(httpx.AsyncBaseTransport):
    """Only the test transport changes the destination; production URLs stay fixed."""

    def __init__(self, port):
        self.port = port
        self.requests = []
        self.open_streams = set()

    async def handle_async_request(self, request):
        url = request.url
        assert url.scheme == "https" and url.host == "slack.com", "unexpected provider origin"
        assert not url.query and not url.username and not url.password, "private query or userinfo"
        assert request.method == "POST", "unexpected provider method"
        assert url.path in ("/api/oauth.v2.access", "/api/auth.test"), "unexpected provider path"
        body = await request.aread()
        self.requests.append((request.method, str(url), dict(request.headers), body))
        translated = httpx.Request(request.method,
                                   url.copy_with(scheme="http", host="127.0.0.1", port=self.port),
                                   headers=request.headers, content=body, extensions=request.extensions)
        transport = httpx.AsyncHTTPTransport(retries=0)
        try:
            response = await transport.handle_async_request(translated)
        except BaseException:
            await transport.aclose()
            raise
        stream = RoutedStream(response, transport, self)
        self.open_streams.add(stream)
        return httpx.Response(response.status_code, headers=response.headers, stream=stream,
                              extensions=response.extensions)

    async def aclose(self):
        for stream in tuple(self.open_streams):
            await stream.aclose()


class FakeServer(ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        self.failures.put("fake HTTP handler failed")


class SlackAuthIntegrationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name) / "connection"
        self.response_token = TOKEN
        self.challenge = None
        self.state = None
        self.verifier = None
        self.browser_responses = []
        self.browser_errors = queue.Queue()
        self.browser_workers = []
        self.stop_stream = threading.Event()
        self.streaming = False
        self.publication_done = threading.Event()
        self.require_publication = False
        self.exchange_requests = 0
        self.identity_requests = 0
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as reservation:
            reservation.bind(("127.0.0.1", 0))
            self.callback_port = reservation.getsockname()[1]
        self.redirect = f"http://localhost:{self.callback_port}/slack/callback"

    def tearDown(self):
        self.stop_stream.set()
        for worker in self.browser_workers:
            worker.join(5)
            self.assertFalse(worker.is_alive(), "callback client did not stop")
        if not self.browser_errors.empty():
            self.fail(self.browser_errors.get_nowait())

    @contextmanager
    def fake_provider(self):
        case = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *args):
                pass

            def do_POST(self):
                self.close_connection = True
                length = int(self.headers.get("Content-Length", "0"))
                assert length < 16384, "oversize test request"
                body = self.rfile.read(length)
                assert self.headers["Host"] == "slack.com", "original Host changed"
                assert "Cookie" not in self.headers, "ambient cookie sent"
                if self.path == "/api/oauth.v2.access":
                    case.exchange_requests += 1
                    form = parse_qs(body.decode("ascii"), strict_parsing=True)
                    assert set(form) == {"code", "code_verifier", "client_id", "redirect_uri", "grant_type"}
                    assert form["code"] == [CODE], "wrong invented code"
                    assert form["client_id"] == [CLIENT], "wrong client selection"
                    assert form["redirect_uri"] == [case.redirect], "redirect not preserved"
                    assert form["grant_type"] == ["authorization_code"], "wrong grant type"
                    assert "Authorization" not in self.headers, "ambient authorization sent"
                    case.verifier = form["code_verifier"][0]
                    challenge = base64.urlsafe_b64encode(hashlib.sha256(case.verifier.encode("ascii")).digest())
                    assert challenge.rstrip(b"=").decode("ascii") == case.challenge, "PKCE does not match"
                    result = {"ok": True, "app_id": APP, "team": {"id": TEAM}, "enterprise": None,
                              "is_enterprise_install": False,
                              "authed_user": {"id": USER, "access_token": case.response_token,
                                              "token_type": "user", "scope": "channels:history"}}
                elif self.path == "/api/auth.test":
                    case.identity_requests += 1
                    assert self.headers.get("Authorization") == "Bearer " + case.response_token, "wrong bearer"
                    assert not body, "identity request body contains data"
                    result = {"ok": True, "team_id": TEAM, "user_id": USER, "url": ORIGIN + "/",
                              "is_enterprise_install": False}
                else:
                    raise AssertionError("unexpected fake provider path")
                encoded = json.dumps(result).encode("utf-8")
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.send_header("Connection", "close")
                self.end_headers()
                try:
                    if case.streaming and self.path == "/api/auth.test":
                        # Each byte is timely for a read timeout; the whole body exceeds the shared budget.
                        for byte in encoded:
                            if case.stop_stream.wait(0.04):
                                break
                            self.wfile.write(bytes([byte]))
                            self.wfile.flush()
                    else:
                        self.wfile.write(encoded)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass

        server = FakeServer(("127.0.0.1", 0), Handler)
        server.failures = queue.Queue()
        worker = threading.Thread(target=server.serve_forever, daemon=True)
        worker.start()
        transport = LoopbackTransport(server.server_port)
        try:
            yield slack_auth.SlackProvider(_transport=transport), transport
        finally:
            self.stop_stream.set()
            server.shutdown()
            server.server_close()
            worker.join(5)
            self.assertFalse(worker.is_alive(), "fake provider did not stop")
            self.assertFalse(transport.open_streams, "provider response stream remained open")
            if not server.failures.empty():
                self.fail(server.failures.get_nowait())

    def open_browser(self, url):
        parsed = urlsplit(url)
        self.assertEqual((parsed.scheme, parsed.netloc, parsed.path),
                         ("https", "slack.com", "/oauth/v2/authorize"))
        fields = parse_qs(parsed.query, keep_blank_values=True, strict_parsing=True)
        self.assertEqual(fields["client_id"], [CLIENT])
        self.assertEqual(fields["redirect_uri"], [self.redirect])
        self.assertEqual(fields["scope"], [""])
        self.assertEqual(fields["user_scope"], ["channels:history"])
        self.assertEqual(fields["code_challenge_method"], ["S256"])
        self.challenge = fields["code_challenge"][0]
        self.state = fields["state"][0]

        def request_callback():
            connection = HTTPConnection("localhost", self.callback_port, timeout=5)
            try:
                connection.request("GET", "/slack/callback?" + urlencode({"code": CODE, "state": self.state}))
                response = connection.getresponse()
                payload = response.read()
                if response.status == 200 and self.require_publication and not self.publication_done.is_set():
                    self.browser_errors.put("browser success preceded private publication")
                self.browser_responses.append((response.status, dict(response.getheaders()), payload))
            except Exception:
                self.browser_errors.put("callback client failed")
            finally:
                connection.close()

        worker = threading.Thread(target=request_callback, daemon=True)
        self.browser_workers.append(worker)
        worker.start()
        return True

    def connect(self, provider, progress=None):
        return slack_auth.connect_slack(self.directory, client_id=CLIENT, redirect_uri=self.redirect,
                                        _provider=provider, _open_browser=self.open_browser, progress=progress)

    def joined_browser(self):
        for worker in self.browser_workers:
            worker.join(5)
            self.assertFalse(worker.is_alive(), "callback client did not finish")
        return self.browser_responses[-1]

    def assert_public(self, *values):
        public = "\n".join(str(value) for value in values)
        for forbidden in (CLIENT, APP, TEAM, USER, ORIGIN, CODE, TOKEN, self.response_token,
                          self.redirect, self.state, self.verifier):
            if forbidden:
                self.assertNotIn(forbidden, public, "private value reached public output")

    def test_real_callback_exchange_identity_and_publication_are_composed(self):
        progress = []
        output, errors = io.StringIO(), io.StringIO()
        self.require_publication = True
        actual_write = slack_auth._write_credentials

        def observe_write(directory_fd, document):
            self.assertEqual(self.identity_requests, 1, "save preceded identity verification")
            self.assertFalse(self.browser_responses, "browser completed before save")
            actual_write(directory_fd, document)
            self.publication_done.set()

        with self.fake_provider() as (provider, transport), patch.object(
                slack_auth, "_write_credentials", side_effect=observe_write), redirect_stdout(output), redirect_stderr(errors):
            result = self.connect(provider, lambda step, status: progress.append((step, status)))
            status, headers, html = self.joined_browser()
            self.assertEqual(status, 200)
            self.assertIn("no-store", headers.get("Cache-Control", ""))
            self.assertEqual(result["credential_state"], "ready")
            self.assertEqual(result["verification"], "live")
            self.assertEqual(result["verified_component"], "authentication")
            self.assertEqual((self.exchange_requests, self.identity_requests), (1, 1))
            self.assertEqual(len(transport.requests), 2)
            saved = slack_auth.slack_status(self.directory)
            self.assertEqual(saved["verification"], "saved")
            self.assertEqual(len(transport.requests), 2)
        private_path = self.directory / "credentials.json"
        private = private_path.read_text()
        self.assertIn(TOKEN, private)
        for ephemeral in (CODE, self.state, self.verifier):
            self.assertNotIn(ephemeral, private)
        self.assertEqual(stat.S_IMODE(self.directory.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(private_path.stat().st_mode), 0o600)
        self.assert_public(result, saved, progress, output.getvalue(), errors.getvalue(), html)

    def test_remote_reauthorization_does_not_hide_a_failed_local_save(self):
        with self.fake_provider() as (provider, transport):
            self.connect(provider)
            self.joined_browser()
            before = (self.directory / "credentials.json").read_bytes()
            self.response_token = "invented-private-replacement-token"
            with patch.object(slack_auth, "_write_credentials",
                              side_effect=slack_auth.SlackAuthError("slack_storage_failed")):
                with self.assertRaises(slack_auth.SlackAuthError) as caught:
                    self.connect(provider)
            self.assertEqual(caught.exception.code, "slack_storage_failed")
            status, _, html = self.joined_browser()
            self.assertEqual(len(self.browser_responses), 2)
            self.assertNotEqual(status, 200)
            self.assertEqual((self.exchange_requests, self.identity_requests), (2, 2))
            self.assertEqual(len(transport.requests), 4)
            self.assertEqual((self.directory / "credentials.json").read_bytes(), before)
            saved = slack_auth.slack_status(self.directory)
            self.assertTrue(saved["configured"])
            self.assertEqual(len(transport.requests), 4)
        self.assert_public(str(caught.exception), html, saved)

    def test_shared_deadline_cancels_a_real_streaming_identity_response(self):
        self.streaming = True
        with self.fake_provider() as (provider, transport), patch.object(slack_auth, "OPERATION_SECONDS", 1.0):
            with self.assertRaises(slack_auth.SlackAuthError) as caught:
                self.connect(provider)
            status, _, html = self.joined_browser()
            self.assertEqual(caught.exception.code, "slack_timeout")
            self.assertNotEqual(status, 200)
            self.assertEqual((self.exchange_requests, self.identity_requests), (1, 1))
            self.assertEqual(len(transport.requests), 2)
            self.assertFalse((self.directory / "credentials.json").exists())
            self.assertFalse(transport.open_streams)
        self.assert_public(str(caught.exception), html)
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            probe.settimeout(0.2)
            self.assertNotEqual(probe.connect_ex(("127.0.0.1", self.callback_port)), 0)


if __name__ == "__main__":
    unittest.main()
