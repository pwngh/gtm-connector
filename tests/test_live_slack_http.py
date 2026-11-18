"""Real local provider TCP, fixed production URL assertions, and actual HTTPS MCP."""

from contextlib import contextmanager
from copy import deepcopy
from http.server import BaseHTTPRequestHandler
import json
import queue
import threading
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

import httpx2

from gtm_connector import slack_auth, slack_reader
from gtm_connector.briefing import check_saved_brief, create_brief
from gtm_connector.citations import BriefRejected, check_brief
from slack_live_fixtures import ACCOUNT, CHANNEL, PARENT, ROWS, REAL_READER, LiveSlackFixture
from test_slack_auth import ACCESS, CLIENT, NEW_ACCESS, REFRESH, identity, replacement, saved_document, seed
from test_slack_auth_integration import FakeServer, RoutedStream


class ReadLoopbackTransport(httpx2.AsyncBaseTransport):
    """Assert fixed requests, then route only this test's traffic to its local server."""

    def __init__(self, port):
        self.port = port
        self.requests = []
        self.open_streams = set()

    async def handle_async_request(self, request):
        url = request.url
        assert (url.scheme, url.host, url.port) == ("https", "slack.com", None), "unexpected provider origin"
        assert not url.username and not url.password and not url.fragment, "unexpected URL authority"
        allowed = {("GET", "/api/conversations.replies"),
                   ("POST", "/api/oauth.v2.access"), ("POST", "/api/auth.test")}
        assert (request.method, url.path) in allowed, "unexpected provider method"
        if request.method == "POST":
            assert not url.query, "credential-bearing query"
        body = await request.aread()
        self.requests.append((request.method, str(url), dict(request.headers), body))
        translated = httpx2.Request(request.method,
            url.copy_with(scheme="http", host="127.0.0.1", port=self.port),
            headers=request.headers, content=body, extensions=request.extensions)
        transport = httpx2.AsyncHTTPTransport(retries=0)
        try:
            response = await transport.handle_async_request(translated)
        except BaseException:
            await transport.aclose()
            raise
        stream = RoutedStream(response, transport, self)
        self.open_streams.add(stream)
        return httpx2.Response(response.status_code, headers=response.headers, stream=stream,
                               extensions=response.extensions)

    async def aclose(self):
        for stream in tuple(self.open_streams):
            await stream.aclose()


class LiveSlackHTTPTests(LiveSlackFixture, unittest.TestCase):
    def setUp(self):
        super().setUp()
        self.responses = []
        self.http_requests = []
        self.response_bodies = []
        self.budgets = []
        self.transport = None
        self.stream_started = threading.Event()
        self.stream_finished = threading.Event()
        self.stop_stream = threading.Event()
        self.handler_threads = []

    def reader_factory(self, mapping_path, **options):
        observer = options.pop("on_read", None)

        def observe(event):
            self.source_events.append(deepcopy(event))
            if observer is not None:
                observer(event)

        # Production clocks and transports are used. Only the endpoint routing
        # in the injected test transport reaches a local invented provider.
        return REAL_READER(mapping_path, **options, on_read=observe,
            _provider=slack_reader.SlackRepliesProvider(_transport=self.transport),
            _auth_provider=slack_auth.SlackProvider(_transport=self.transport))

    @contextmanager
    def provider_http(self):
        case = self
        original_begin = slack_reader.SlackOperationBudget.begin_request

        def begin(budget, *, kind):
            case.budgets.append((id(budget), budget.deadline, kind))
            return original_begin(budget, kind=kind)

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def do_GET(self):
                self.handle_scripted()

            def do_POST(self):
                self.handle_scripted()

            def handle_scripted(self):
                case.handler_threads.append(threading.current_thread())
                self.close_connection = True
                length = int(self.headers.get("Content-Length", "0"))
                assert length < 16384, "oversized invented request"
                body = self.rfile.read(length)
                assert self.headers.get("Host") == "slack.com", "production Host changed"
                assert self.headers.get("Accept-Encoding") == "identity", "encoding changed"
                assert "Cookie" not in self.headers, "ambient cookie"
                parsed = urlsplit(self.path)
                case.http_requests.append((self.command, parsed.path, parse_qs(parsed.query), dict(self.headers), body))
                assert case.responses, "unexpected additional request"
                expected_method, expected_path, status, payload, headers, streaming = case.responses.pop(0)
                assert (self.command, parsed.path) == (expected_method, expected_path), "wrong provider operation"
                encoded = json.dumps(payload, ensure_ascii=True, separators=(",", ":")).encode("ascii")
                case.response_bodies.append(encoded)
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(encoded)))
                self.send_header("Connection", "close")
                for key, value in headers.items():
                    self.send_header(key, value)
                self.end_headers()
                try:
                    if streaming:
                        case.stream_started.set()
                        for byte in encoded:
                            # Every read is timely; only the original operation deadline ends this body.
                            if case.stop_stream.wait(0.02):
                                break
                            self.wfile.write(bytes([byte]))
                            self.wfile.flush()
                    else:
                        self.wfile.write(encoded)
                        self.wfile.flush()
                except (BrokenPipeError, ConnectionResetError):
                    pass
                finally:
                    if streaming:
                        case.stream_finished.set()

        server = FakeServer(("127.0.0.1", 0), Handler)
        server.failures = queue.Queue()
        worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        worker.start()
        self.transport = ReadLoopbackTransport(server.server_port)
        try:
            with patch.object(slack_reader.SlackOperationBudget, "begin_request", new=begin):
                yield
        finally:
            self.stop_stream.set()
            server.shutdown()
            server.server_close()
            worker.join(5)
            self.assertFalse(worker.is_alive(), "local provider listener did not stop")
            for handler in self.handler_threads:
                handler.join(5)
                self.assertFalse(handler.is_alive(), "local provider handler did not stop")
            self.assertFalse(self.transport.open_streams, "provider stream remained open")
            if not server.failures.empty():
                self.fail(server.failures.get_nowait())

    def response(self, method, path, payload, *, status=200, headers=None, streaming=False):
        self.responses.append((method, path, status, payload, headers or {}, streaming))

    def prepare_reader(self):
        with self.runtime() as runtime:
            self.map_account(runtime)
            return self.reader_factory(runtime.slack_mapping_path, connection_dir=self.connection,
                                       crm_binding=runtime.crm_binding, slack_binding=runtime.slack_binding)

    def assert_one_budget(self, count):
        self.assertEqual(len(self.budgets), count)
        self.assertEqual(len({identity for identity, _, _ in self.budgets}), 1)
        self.assertEqual(len({deadline for _, deadline, _ in self.budgets}), 1)
        self.assertEqual(len(self.http_requests), count)
        self.assertEqual(len(self.transport.requests), count)
        self.assertFalse(self.responses)

    def test_real_tcp_expiry_refresh_identity_and_https_brief_keep_literal_evidence(self):
        seed(self.connection, saved_document(self.clock.now(), rotating=True))
        self.response("GET", "/api/conversations.replies", {"ok": False, "error": "token_expired"})
        self.response("POST", "/api/oauth.v2.access", replacement())
        self.response("POST", "/api/auth.test", identity())
        self.response("GET", "/api/conversations.replies", deepcopy(self.reply))
        with self.provider_http(), self.runtime() as runtime:
            self.map_account(runtime)
            artifact = create_brief(runtime, ACCOUNT, as_of="2027-01-15", include_slack=True)
            self.assertEqual(artifact["status"], "accepted")
            snapshot = artifact["evidence"]["thread_snapshot"]
            counts = snapshot["counts"]
            self.assertEqual((counts["http_attempts"], counts["auth_attempts"], counts["message_attempts"],
                              counts["refresh_attempts"], counts["token_retries"]), (4, 2, 2, 1, 1))
            self.assertEqual(counts["response_bytes"], sum(map(len, self.response_bodies)))
            self.assert_one_budget(4)
            self.assertEqual([kind for _, _, kind in self.budgets], ["messages", "auth", "auth", "messages"])
            forms = parse_qs(self.http_requests[1][4].decode("ascii"), strict_parsing=True)
            self.assertEqual(forms, {"grant_type": ["refresh_token"], "client_id": [CLIENT],
                                     "refresh_token": [REFRESH]})
            self.assertNotIn("Authorization", self.http_requests[1][3])
            self.assertEqual(self.http_requests[2][4], b"")
            self.assertEqual(self.http_requests[2][3]["Authorization"], "Bearer " + NEW_ACCESS)
            self.assertEqual(self.http_requests[0][3]["Authorization"], "Bearer " + ACCESS)
            self.assertEqual(self.http_requests[3][3]["Authorization"], "Bearer " + NEW_ACCESS)
            self.assertEqual(self.http_requests[0][2], self.http_requests[3][2])
            records = [row for row in artifact["evidence"]["records"] if row["record_ref"].startswith("slack/")]
            self.assertEqual([row["body"] for row in records], [row["text"] for row in ROWS])
            receipt = artifact["evidence"]["receipts"][-1]
            self.assertEqual((receipt["tool"], receipt["status"], receipt["row_count"]),
                             ("read_slack_messages", "ok", 3))
            self.assertEqual(set(receipt["rows"]), {row["record_ref"] for row in records})
            metric = self.source_events[-1]["counts"]
            self.assertEqual(metric["auth_response_bytes"], sum(map(len, self.response_bodies[1:3])))
            self.assertEqual(metric["message_response_bytes"], len(self.response_bodies[0]) + len(self.response_bodies[3]))
        self.reply["messages"][0]["text"] = "Later upstream text is not historical evidence."
        with patch.object(slack_reader.SlackRepliesProvider, "replies", side_effect=AssertionError("No network")):
            self.assertEqual(check_saved_brief(self.state, artifact["brief_id"], artifact["text"]), artifact["text"])
        changed = deepcopy(artifact["evidence"])
        next(row for row in changed["records"] if row["record_ref"].startswith("slack/"))["body"] = "Altered"
        with self.assertRaises(BriefRejected):
            check_brief(artifact["text"], changed)

    def test_real_tcp_rate_retry_then_cursor_pages_share_counts_and_cutoff(self):
        self.response("GET", "/api/conversations.replies", {"ok": False, "error": "ratelimited"},
                      status=429, headers={"Retry-After": "0"})
        self.response("GET", "/api/conversations.replies",
                      {"ok": True, "messages": [ROWS[0]], "response_metadata": {"next_cursor": "next-page"}})
        self.response("GET", "/api/conversations.replies",
                      {"ok": True, "messages": ROWS[1:], "response_metadata": {"next_cursor": ""}})
        with self.provider_http():
            source = self.prepare_reader()
            result = source.read_slack_messages(ACCOUNT)
            self.assert_one_budget(3)
            counts = result.snapshot["counts"]
            self.assertEqual((counts["http_attempts"], counts["message_attempts"], counts["pages"],
                              counts["raw_rows"], counts["messages"], counts["rate_retries"]), (3, 3, 2, 3, 3, 1))
            self.assertEqual(counts["response_bytes"], sum(map(len, self.response_bodies)))
            queries = [request[2] for request in self.http_requests]
            self.assertEqual(queries[0], queries[1])
            self.assertNotIn("cursor", queries[0])
            self.assertEqual(queries[2]["cursor"], ["next-page"])
            self.assertEqual(queries[2]["limit"], ["99"])
            self.assertEqual(len({query["latest"][0] for query in queries}), 1)
            self.assertTrue(all(query["channel"] == [CHANNEL] and query["ts"] == [PARENT] for query in queries))
            slack_reader.validate_slack_snapshot(result.records, result.snapshot, account_id=ACCOUNT,
                                                crm_binding=source.crm_binding, slack_binding=source.slack_binding)

    def test_real_tcp_dribbling_body_is_cancelled_without_partial_or_late_result(self):
        self.response("GET", "/api/conversations.replies", deepcopy(self.reply), streaming=True)
        with self.provider_http():
            source = self.prepare_reader()
            with patch.object(slack_reader, "OPERATION_SECONDS", 1.0):
                with self.assertRaises(slack_reader.SlackReadError) as caught:
                    source.read_slack_messages(ACCOUNT)
            self.assertEqual(caught.exception.code, "slack_timeout")
            self.assertTrue(self.stream_started.is_set())
            self.assertTrue(self.stream_finished.wait(2), "cancelled TCP response kept writing")
            self.assertFalse(self.transport.open_streams)
            self.assert_one_budget(1)
            self.assertEqual(len(self.source_events), 1)
            event = self.source_events[0]
            self.assertEqual((event["status"], event["code"], event["counts"]["returned_messages"]),
                             ("failed", "slack_timeout", 0))
            self.assertFalse(event["pagination_complete"])
            self.assertGreater(event["counts"]["response_bytes"], 0)
            self.assertLess(event["counts"]["response_bytes"], len(self.response_bodies[0]))
        # The handler and response stream have both ended; no producer remains to publish a late result.
        self.assertEqual(len(self.source_events), 1)
        self.assertEqual(self.source_events[0]["counts"]["returned_messages"], 0)
