"""Real localhost sockets and invented browser helpers prove the consent boundary."""

import contextlib
import errno
import io
import os
from pathlib import Path
import queue
import select
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlencode, urlsplit

from gtm_connector import slack_callback as callback


CODE = "invented-private-code"
SECRET = "invented-private-error-detail"
ROOT = Path(__file__).resolve().parents[1]
REAL_POPEN = subprocess.Popen


def free_port():
    for _ in range(20):
        with socket.socket() as ipv4:
            ipv4.bind(("127.0.0.1", 0))
            port = ipv4.getsockname()[1]
            try:
                with socket.socket(socket.AF_INET6) as ipv6:
                    ipv6.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                    ipv6.bind(("::1", port))
            except OSError as error:
                if error.errno == errno.EADDRINUSE:
                    continue
                if error.errno not in callback._UNAVAILABLE_FAMILY:
                    raise
            return port
    raise AssertionError("No free callback test port")


def request_bytes(port, state, *, query=None, path="/slack/callback", method="GET", headers=None):
    query = urlencode({"code": CODE, "state": state}) if query is None else query
    headers = f"Host: localhost:{port}\r\n" if headers is None else headers
    return f"{method} {path}?{query} HTTP/1.1\r\n{headers}\r\n".encode("ascii")


def send_request(port, data, host="127.0.0.1"):
    result = bytearray()
    with socket.create_connection((host, port), timeout=3) as client:
        client.sendall(data)
        while True:
            try:
                block = client.recv(4096)
            except ConnectionResetError:
                break
            if not block:
                break
            result.extend(block)
    return bytes(result)


class CallbackTests(unittest.TestCase):
    def assert_closed(self, port):
        for host in ("127.0.0.1", "::1"):
            with self.subTest(host=host):
                try:
                    connection = socket.create_connection((host, port), timeout=.3)
                except OSError:
                    continue
                connection.close()
                self.fail("Callback listener remained open")

    def drive(self, action, *, finish=None, timeout=3):
        port = free_port()
        errors = queue.Queue()
        workers = []

        def open_browser(url):
            state = parse_qs(urlsplit(url).query)["state"][0]
            self.assertGreaterEqual(len(state), 43)

            def run():
                try:
                    action(port, state)
                except BaseException as error:
                    errors.put(error)
            worker = threading.Thread(target=run)
            workers.append(worker)
            worker.start()
            return True

        try:
            return callback.receive_consent(
                f"http://localhost:{port}/slack/callback",
                authorization_url_for_state=lambda state: "https://slack.com/oauth/v2/authorize?" + urlencode({"state": state}),
                finish=finish or (lambda code, *, deadline: {"saved": True}),
                open_browser=open_browser, timeout_seconds=timeout)
        finally:
            for worker in workers:
                worker.join(4)
                self.assertFalse(worker.is_alive(), "Local callback test worker did not finish")
            self.assert_closed(port)
            if not errors.empty():
                raise errors.get()

    def test_exact_redirect_validation_has_no_side_effects(self):
        for value in (None, "", "https://localhost:8765/slack/callback", "http://localhost:80/slack/callback",
                      "http://localhost:08765/slack/callback", "http://localhost:65536/slack/callback",
                      "http://127.0.0.1:8765/slack/callback", "http://localhost:8765/slack/callback/",
                      "http://localhost:8765/slack/callback?secret=" + SECRET,
                      "http://user@localhost:8765/slack/callback", "http://localhost:8765/slack/%63allback"):
            with self.subTest(value=value), patch.object(callback, "_listen") as listen:
                with self.assertRaises(callback.CallbackError) as caught:
                    callback.receive_consent(value, authorization_url_for_state=lambda state: "unused",
                                             finish=lambda *args, **kwargs: None)
                self.assertEqual(str(caught.exception), "invalid_redirect")
                listen.assert_not_called()
        self.assertEqual(callback.validate_redirect("http://localhost:8765/slack/callback"),
                         "http://localhost:8765/slack/callback")

    def test_finish_runs_on_calling_thread_and_success_waits_for_commit(self):
        caller_thread = threading.get_ident()
        entered, allow_commit, committed = threading.Event(), threading.Event(), threading.Event()
        result = {"saved": True}
        responses = []
        active_port = []

        def finish(code, *, deadline):
            self.assertEqual(code, CODE)
            self.assertEqual(threading.get_ident(), caller_thread)
            self.assertGreater(deadline, time.monotonic())
            self.assert_closed(active_port[0])
            entered.set()
            self.assertTrue(allow_commit.wait(2))
            committed.set()
            return result

        def action(port, state):
            active_port.append(port)
            with socket.create_connection(("127.0.0.1", port), timeout=2) as client:
                client.sendall(request_bytes(port, state))
                try:
                    self.assertTrue(entered.wait(2))
                    client.setblocking(False)
                    with self.assertRaises(BlockingIOError):
                        client.recv(1)
                finally:
                    allow_commit.set()
                client.settimeout(2)
                response = bytearray()
                while block := client.recv(4096):
                    self.assertTrue(committed.is_set())
                    response.extend(block)
                responses.append(bytes(response))

        self.assertIs(self.drive(action, finish=finish), result)
        self.assertIn(b"HTTP/1.1 200 OK", responses[0])
        for header in (b"Cache-Control: no-store", b"Referrer-Policy: no-referrer",
                       b"Content-Security-Policy: default-src 'none'", b"Connection: close"):
            self.assertIn(header, responses[0])
        self.assertNotIn(CODE.encode(), responses[0])

    def test_invalid_strays_preserve_pending_attempt(self):
        finished = []
        responses = []

        def action(port, state):
            valid_query = urlencode({"state": state, "code": CODE})
            requests = [
                request_bytes(port, state, query="state=wrong&code=" + CODE),
                request_bytes(port, state, query=valid_query + "&code=duplicate"),
                request_bytes(port, state, query=valid_query + "&error=access_denied"),
                request_bytes(port, state, query=valid_query + "&extra=" + SECRET),
                request_bytes(port, state, query=valid_query + "&error_description=" + SECRET),
                request_bytes(port, state, query="state=" + state + "&code=%QQ"),
                request_bytes(port, state, query="state=" + state + "&code=%FF"),
                request_bytes(port, state, query="state=" + state + "&code=%0A"),
                request_bytes(port, state, query="state=" + state + "&code="),
                request_bytes(port, state, query="state=" + state + "&code=" + "x" * 4097),
                request_bytes(port, state, path="/favicon.ico"),
                request_bytes(port, state, method="POST"),
                request_bytes(port, state, headers="Host: wrong.example\r\n"),
                request_bytes(port, state, headers=f"Host: localhost:{port}\r\nHost: localhost:{port}\r\n"),
                request_bytes(port, state, headers=f"Host: localhost:{port}\r\nContent-Length: 1\r\n"),
                request_bytes(port, state, headers=f"Host: localhost:{port}\r\nTransfer-Encoding: chunked\r\n"),
                request_bytes(port, state, headers=f"Host: localhost:{port}\r\n folded: bad\r\n"),
                request_bytes(port, state).replace(b"GET /", b"GET https://outside.example/", 1),
                request_bytes(port, state).replace(b" HTTP/1.1", b" HTTP/2.0"),
            ]
            for raw in requests:
                response = send_request(port, raw)
                self.assertNotIn(b"200 OK", response)
                self.assertNotIn(SECRET.encode(), response)
                self.assertEqual(finished, [])
                responses.append(response)
            responses.append(send_request(port, request_bytes(port, state)))

        self.drive(action, finish=lambda code, **kwargs: finished.append(code), timeout=5)
        self.assertEqual(finished, [CODE])
        self.assertIn(b"200 OK", responses[-1])

    def test_oversized_lines_headers_and_field_count_refuse_without_consuming(self):
        finished = []

        def action(port, state):
            invalid = [b"G" * (callback.MAX_REQUEST_LINE + 1),
                       request_bytes(port, state, headers=f"Host: localhost:{port}\r\nX: " + "x" * 16400 + "\r\n"),
                       request_bytes(port, state, headers=f"Host: localhost:{port}\r\n" +
                                     "".join(f"X-{i}: a\r\n" for i in range(33))),
                       request_bytes(port, state, query="state=" + state + "&code=a&a=1&b=2&c=3&d=4")]
            for raw in invalid:
                send_request(port, raw)
                self.assertEqual(finished, [])
            self.assertIn(b"200 OK", send_request(port, request_bytes(port, state)))

        self.drive(action, finish=lambda code, **kwargs: finished.append(code))
        self.assertEqual(finished, [CODE])

    def test_valid_oauth_errors_are_fixed_and_never_reflected(self):
        for supplied, expected in (("access_denied", "login_denied"), (SECRET, "oauth_rejected")):
            responses = []

            def action(port, state):
                query = urlencode({"state": state, "error": supplied, "error_description": SECRET,
                                   "error_uri": "https://outside.example/" + SECRET})
                responses.append(send_request(port, request_bytes(port, state, query=query)))

            with self.subTest(error=supplied), self.assertRaises(callback.CallbackError) as caught:
                self.drive(action, finish=lambda *args, **kwargs: self.fail("OAuth failure invoked finish"))
            self.assertEqual(str(caught.exception), expected)
            self.assertNotIn(SECRET.encode(), responses[0])
            self.assertIn(b"400 Bad Request", responses[0])

    def test_finish_failure_preserves_exception_without_browser_details(self):
        error = RuntimeError(SECRET)
        responses = []

        def finish(code, *, deadline):
            raise error

        def action(port, state):
            responses.append(send_request(port, request_bytes(port, state)))

        stdout, stderr = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(stdout), contextlib.redirect_stderr(stderr):
            with self.assertRaises(RuntimeError) as caught:
                self.drive(action, finish=finish)
        self.assertIs(caught.exception, error)
        self.assertIn(b"500 Internal Server Error", responses[0])
        self.assertNotIn(SECRET.encode(), responses[0])
        self.assertEqual(stdout.getvalue() + stderr.getvalue(), "")

    def test_disconnected_browser_does_not_undo_finish_and_replay_is_closed(self):
        finished = []

        def action(port, state):
            with socket.create_connection(("127.0.0.1", port), timeout=2) as client:
                client.sendall(request_bytes(port, state))

        self.assertEqual(self.drive(action, finish=lambda code, **kwargs: finished.append(code) or "committed"),
                         "committed")
        self.assertEqual(finished, [CODE])

    def test_occupied_ipv4_or_ipv6_refuses_before_browser(self):
        tested = []
        for family, host in ((socket.AF_INET, "127.0.0.1"), (socket.AF_INET6, "::1")):
            try:
                blocker = socket.socket(family)
                if not hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
                    blocker.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
                if family == socket.AF_INET6:
                    blocker.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
                blocker.bind((host, 0))
                blocker.listen(1)
            except OSError as error:
                blocker.close()
                if error.errno in callback._UNAVAILABLE_FAMILY:
                    continue
                raise
            with blocker:
                tested.append(family)
                port = blocker.getsockname()[1]
                with patch.object(callback, "_open_system_browser") as browser:
                    with self.assertRaises(callback.CallbackError) as caught:
                        callback.receive_consent(f"http://localhost:{port}/slack/callback",
                                                 authorization_url_for_state=lambda state: "unused", finish=lambda: None)
                    self.assertEqual(caught.exception.code, "callback_unavailable")
                    browser.assert_not_called()
        self.assertIn(socket.AF_INET, tested)

    def test_immediate_second_consent_reuses_only_a_closed_listener(self):
        port = free_port()
        responses, workers, codes = [], [], []

        def browser(url):
            state = parse_qs(urlsplit(url).query)["state"][0]
            worker = threading.Thread(target=lambda: responses.append(
                send_request(port, request_bytes(port, state))))
            workers.append(worker)
            worker.start()
            return True

        try:
            for _ in range(2):
                result = callback.receive_consent(
                    f"http://localhost:{port}/slack/callback",
                    authorization_url_for_state=lambda state: "https://slack.com/?" + urlencode({"state": state}),
                    finish=lambda code, **kwargs: codes.append(code) or "saved", open_browser=browser,
                    timeout_seconds=3)
                self.assertEqual(result, "saved")
                workers[-1].join(3)
                self.assertFalse(workers[-1].is_alive())
            self.assertEqual(codes, [CODE, CODE])
            self.assertEqual(len(responses), 2)
            self.assertTrue(all(b"200 OK" in response for response in responses))
            self.assert_closed(port)
        finally:
            for worker in workers:
                worker.join(3)

    def test_expiry_after_finish_does_not_claim_browser_success_or_rollback(self):
        committed, responses = [], []

        def finish(code, *, deadline):
            time.sleep(max(0, deadline - time.monotonic()) + .01)
            committed.append(True)
            return "saved"

        with self.assertRaises(callback.CallbackError) as caught:
            self.drive(lambda port, state: responses.append(send_request(port, request_bytes(port, state))),
                       finish=finish, timeout=.15)
        self.assertEqual(caught.exception.code, "login_timeout")
        self.assertEqual(committed, [True])
        self.assertNotIn(b"200 OK", responses[0])

    def test_available_localhost_families_accept_actual_connections(self):
        for host in ("127.0.0.1", "::1"):
            if host == "::1":
                try:
                    with socket.socket(socket.AF_INET6) as probe:
                        probe.bind((host, 0))
                except OSError as error:
                    if error.errno in callback._UNAVAILABLE_FAMILY:
                        continue
                    raise
            responses = []
            with self.subTest(host=host):
                self.drive(lambda port, state: responses.append(send_request(port, request_bytes(port, state), host)))
                self.assertIn(b"200 OK", responses[0])

    def test_browser_failure_and_consent_timeout_close_listeners(self):
        for opens, timeout, expected in ((False, 1, "browser_unavailable"), (True, .05, "login_timeout")):
            port = free_port()
            with self.assertRaises(callback.CallbackError) as caught:
                callback.receive_consent(f"http://localhost:{port}/slack/callback",
                                         authorization_url_for_state=lambda state: "https://slack.com/",
                                         finish=lambda **kwargs: self.fail("No callback should finish"),
                                         open_browser=lambda url: opens, timeout_seconds=timeout)
            self.assertEqual(caught.exception.code, expected)
            self.assert_closed(port)

    def test_slow_request_has_absolute_deadline(self):
        finished = []

        def action(port, state):
            with socket.create_connection(("127.0.0.1", port), timeout=2) as client:
                try:
                    for byte in b"GET /slack/callback":
                        client.sendall(bytes([byte]))
                        time.sleep(.015)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        with patch.object(callback, "REQUEST_SECONDS", .04):
            with self.assertRaises(callback.CallbackError) as caught:
                self.drive(action, finish=lambda code, **kwargs: finished.append(code), timeout=.12)
        self.assertEqual(caught.exception.code, "login_timeout")
        self.assertEqual(finished, [])

    def test_request_budget_is_finite(self):
        def action(port, state):
            for _ in range(callback.MAX_REQUESTS):
                send_request(port, request_bytes(port, state, path="/favicon.ico"))
        with self.assertRaises(callback.CallbackError) as caught:
            self.drive(action, finish=lambda **kwargs: self.fail("Invalid requests finished"), timeout=5)
        self.assertEqual(caught.exception.code, "callback_unavailable")

    @unittest.skipUnless(os.name == "posix", "POSIX signal proof; native platform work is separate")
    def test_actual_sigint_during_wait_and_finish_has_no_later_commit(self):
        for during_finish in (False, True):
            port = free_port()
            with self.subTest(during_finish=during_finish), tempfile.TemporaryDirectory() as directory:
                marker = Path(directory) / "unexpected-commit"
                program = """
import socket, sys, threading, time
from pathlib import Path
from urllib.parse import urlencode, parse_qs, urlsplit
from gtm_connector.slack_callback import receive_consent
port, marker, during_finish = int(sys.argv[1]), Path(sys.argv[2]), sys.argv[3] == 'yes'
def finish(code, *, deadline):
    print('READY', flush=True)
    threading.Event().wait(60)
    marker.write_text('must not happen')
def open_browser(url):
    if not during_finish:
        print('READY', flush=True)
        return True
    state = parse_qs(urlsplit(url).query)['state'][0]
    def request():
        with socket.create_connection(('127.0.0.1', port)) as client:
            query = urlencode({'code': 'invented-code', 'state': state})
            client.sendall(f'GET /slack/callback?{query} HTTP/1.1\\r\\nHost: localhost:{port}\\r\\n\\r\\n'.encode())
            client.recv(4096)
    threading.Thread(target=request, daemon=True).start()
    return True
try:
    receive_consent(f'http://localhost:{port}/slack/callback',
        authorization_url_for_state=lambda state: 'https://slack.com/?' + urlencode({'state':state}),
        finish=finish, open_browser=open_browser)
except KeyboardInterrupt:
    print('INTERRUPTED', flush=True)
    sys.exit(130)
"""
                process = subprocess.Popen([sys.executable, "-c", program, str(port), str(marker),
                                            "yes" if during_finish else "no"], cwd=ROOT,
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    self.assertTrue(select.select([process.stdout], [], [], 5)[0])
                    self.assertEqual(process.stdout.readline().strip(), "READY")
                    process.send_signal(signal.SIGINT)
                    stdout, stderr = process.communicate(timeout=5)
                    self.assertEqual(process.returncode, 130)
                    self.assertEqual(stdout.strip(), "INTERRUPTED")
                    self.assertEqual(stderr, "")
                    self.assertFalse(marker.exists())
                    self.assert_closed(port)
                finally:
                    if process.poll() is None:
                        process.kill()
                    process.communicate(timeout=3)


class BrowserHelperTests(unittest.TestCase):
    def test_real_helper_receives_url_via_stdin_not_its_arguments(self):
        url = "https://slack.com/oauth/v2/authorize?state=invented-private-state"
        with tempfile.TemporaryDirectory() as directory:
            capture = Path(directory) / "input"
            program = ("import sys\nfrom pathlib import Path\nraw = sys.stdin.buffer.read()\n"
                       "assert raw.decode() not in repr(sys.argv)\n"
                       f"Path({str(capture)!r}).write_bytes(raw)\n")
            with patch.object(callback, "_BROWSER_PROGRAM", program):
                self.assertTrue(callback._open_system_browser(url, deadline=time.monotonic() + 3))
            self.assertEqual(capture.read_text(), url)

    def test_failed_hung_and_interrupted_helpers_are_reaped(self):
        for mode in ("failed", "hung", "interrupted"):
            children = []

            def create(*args, **kwargs):
                self.assertNotIn(SECRET, repr(args))
                self.assertNotIn("env", kwargs)
                process = REAL_POPEN(*args, **kwargs)
                children.append(process)
                if mode == "interrupted":
                    process.communicate = lambda **kwargs: (_ for _ in ()).throw(KeyboardInterrupt())
                return process

            program = "import sys; sys.exit(7)" if mode == "failed" else "import time; time.sleep(60)"
            with self.subTest(mode=mode), patch.object(callback, "_BROWSER_PROGRAM", program), \
                    patch.object(callback, "BROWSER_SECONDS", .2), patch.object(callback.subprocess, "Popen", create):
                if mode == "failed":
                    self.assertFalse(callback._open_system_browser(SECRET, deadline=time.monotonic() + 3))
                elif mode == "hung":
                    with self.assertRaises(callback.CallbackError) as caught:
                        callback._open_system_browser(SECRET, deadline=time.monotonic() + 3)
                    self.assertEqual(caught.exception.code, "browser_unavailable")
                else:
                    with self.assertRaises(KeyboardInterrupt):
                        callback._open_system_browser(SECRET, deadline=time.monotonic() + 3)
                self.assertEqual(len(children), 1)
                self.assertIsNotNone(children[0].poll())
                self.assertTrue(children[0].stdin.closed)


if __name__ == "__main__":
    unittest.main()
