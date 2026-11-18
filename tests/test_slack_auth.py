"""Invented provider grants and private state; no external Slack requests."""

from copy import deepcopy
from datetime import datetime, timedelta, timezone
import json
import multiprocessing
import os
from pathlib import Path
import stat
import tempfile
import time
import unittest
from unittest.mock import patch
from urllib.parse import parse_qs, urlsplit

import httpx2

from gtm_connector import slack_auth as auth


CLIENT = "123456789.987654321"
APP, TEAM, USER = "A12345678", "T12345678", "U12345678"
REDIRECT = "http://localhost:8765/slack/callback"
ACCESS, REFRESH = "invented-private-access-token", "invented-private-refresh-token"
NEW_ACCESS, NEW_REFRESH = "invented-replacement-access-token", "invented-replacement-refresh-token"
IDENTITY = {"app_id": APP, "team_id": TEAM, "user_id": USER, "enterprise_id": None}


class Clock:
    def __init__(self):
        self.wall = datetime(2026, 11, 18, 12, tzinfo=timezone.utc)
        self.tick = 1000.0

    def now(self):
        return self.wall

    def monotonic(self):
        return self.tick

    def advance(self, seconds):
        self.wall += timedelta(seconds=seconds)
        self.tick += seconds


def grant(*, rotating=True):
    user = {"id": USER, "token_type": "user", "scope": "channels:history", "access_token": ACCESS}
    if rotating:
        user.update(refresh_token=REFRESH, expires_in=3600)
    return {"ok": True, "app_id": APP, "team": {"id": TEAM}, "authed_user": user}


def replacement():
    return {"ok": True, "token_type": "user", "scope": "channels:history", "access_token": NEW_ACCESS,
            "refresh_token": NEW_REFRESH, "expires_in": 3600}


def identity():
    return {"ok": True, "team_id": TEAM, "user_id": USER, "url": "https://invented-workspace.slack.com/"}


class Provider:
    def __init__(self):
        self.calls = []
        self.initial = grant()
        self.replacement = replacement()
        self.identified = identity()
        self.revoked = {"ok": True, "revoked": True}

    def exchange(self, code, verifier, client_id, redirect_uri, deadline):
        self.calls.append(("exchange", deadline))
        return deepcopy(self.initial)

    def refresh(self, refresh_token, client_id, deadline):
        self.calls.append(("refresh", deadline))
        if isinstance(self.replacement, Exception):
            raise self.replacement
        return deepcopy(self.replacement)

    def identity(self, access_token, deadline):
        self.calls.append(("identity", deadline))
        if isinstance(self.identified, Exception):
            raise self.identified
        return deepcopy(self.identified)

    def revoke(self, access_token, deadline):
        self.calls.append(("revoke", deadline))
        if isinstance(self.revoked, Exception):
            raise self.revoked
        return deepcopy(self.revoked)


def saved_document(now, *, rotating=True, expired=False):
    obtained = now - timedelta(hours=2) if expired else now
    config = auth.validate_configuration(client_id=CLIENT, redirect_uri=REDIRECT)
    document = auth._parse_initial(grant(rotating=rotating), configuration=config, obtained_at=obtained)
    document["last_verified_at"] = auth._stamp(obtained)
    document["workspace_origin"] = "https://invented-workspace.slack.com"
    return document


def seed(directory, document):
    with auth._connection_lock(directory, create=True) as descriptor:
        auth._write_credentials(descriptor, document)


def _refresh_process(directory, started, release, output, request_log):
    class BlockingProvider(Provider):
        def refresh(self, refresh_token, client_id, deadline):
            with open(request_log, "a") as log:
                log.write("refresh\n")
            started.set()
            if not release.wait(5):
                raise RuntimeError("test release did not arrive")
            return super().refresh(refresh_token, client_id, deadline)

    try:
        access = auth.get_slack_access(Path(directory), required_scopes=auth.DEFAULT_USER_SCOPES,
                                       _provider=BlockingProvider())
        output.put(("first", access.access_token == NEW_ACCESS))
    except Exception as error:
        output.put(("first", type(error).__name__))


def _competing_process(directory, retry, output, request_log):
    class NeverProvider(Provider):
        def identity(self, access_token, deadline):
            with open(request_log, "a") as log:
                log.write("unexpected identity\n")
            return super().identity(access_token, deadline)

        def refresh(self, refresh_token, client_id, deadline):
            with open(request_log, "a") as log:
                log.write("unexpected refresh\n")
            return super().refresh(refresh_token, client_id, deadline)

    try:
        auth.get_slack_access(Path(directory), required_scopes=auth.DEFAULT_USER_SCOPES, _provider=NeverProvider())
    except auth.SlackAuthError as error:
        output.put(("busy", error.code))
    if retry.wait(5):
        try:
            access = auth.get_slack_access(Path(directory), required_scopes=auth.DEFAULT_USER_SCOPES,
                                           _provider=NeverProvider())
            output.put(("retry", access.access_token == NEW_ACCESS))
        except Exception as error:
            output.put(("retry", type(error).__name__))


class _StateCase(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name) / "connection"
        self.clock = Clock()
        self.provider = Provider()

    def save(self, **options):
        seed(self.directory, saved_document(self.clock.now(), **options))

    def stored(self):
        return json.loads((self.directory / "credentials.json").read_text())

    def expect_error(self, code, function, *args, **kwargs):
        with self.assertRaises(auth.SlackAuthError) as caught:
            function(*args, **kwargs)
        self.assertEqual(caught.exception.code, code)
        self.assertEqual(str(caught.exception), code)


class StateTests(_StateCase):
    def test_missing_status_and_disconnect_are_read_only(self):
        status = auth.slack_status(self.directory)
        self.assertFalse(status["configured"])
        self.assertEqual(status["credential_state"], "missing")
        self.assertTrue(status["needs_login"])
        self.assertIsNone(status["rotation"])
        result = auth.disconnect_slack(self.directory)
        self.assertFalse(result["local_credentials_removed"])
        self.assertFalse(self.directory.exists())
        self.expect_error("slack_not_connected", auth.slack_status, self.directory, check=True)
        self.assertFalse(self.directory.exists())

    def test_local_status_and_ready_access_make_no_provider_calls_or_disclosures(self):
        self.save()
        before = (self.directory / "credentials.json").read_bytes()
        status = auth.slack_status(self.directory, _provider=self.provider, _clock=self.clock)
        access = auth.get_slack_access(self.directory, required_scopes=auth.DEFAULT_USER_SCOPES,
                                       _provider=self.provider, _clock=self.clock)
        self.assertEqual(access.access_token, ACCESS)
        self.assertEqual(self.provider.calls, [])
        self.assertEqual((self.directory / "credentials.json").read_bytes(), before)
        public = json.dumps(status) + repr(access)
        for value in (ACCESS, REFRESH, TEAM, USER, APP, CLIENT, "invented-workspace"):
            self.assertNotIn(value, public)
        self.assertEqual(stat.S_IMODE(self.directory.stat().st_mode), 0o700)
        for path in self.directory.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_ready_check_identity_mismatch_blocks_rotating_and_nonrotating_access(self):
        for rotating in (True, False):
            with self.subTest(rotating=rotating):
                directory = self.directory / str(rotating)
                seed(directory, saved_document(self.clock.now(), rotating=rotating))
                provider = Provider()
                provider.identified["team_id"] = "T87654321"
                self.expect_error("slack_identity_mismatch", auth.slack_status, directory, check=True,
                                  _provider=provider, _clock=self.clock)
                self.expect_error("slack_reconnect_required", auth.get_slack_access, directory,
                                  required_scopes=auth.DEFAULT_USER_SCOPES, _provider=provider, _clock=self.clock)
                status = auth.slack_status(directory, _clock=self.clock)
                self.assertEqual(status["credential_state"], "refresh_pending")
                self.assertTrue(status["needs_login"])
                self.assertEqual([call[0] for call in provider.calls], ["identity"])

    def test_refresh_rotates_once_then_restart_uses_new_token(self):
        self.save(expired=True)
        status = auth.slack_status(self.directory, check=True, _provider=self.provider, _clock=self.clock)
        self.assertEqual(status["verification"], "live")
        self.assertEqual([call[0] for call in self.provider.calls], ["refresh", "identity"])
        self.assertEqual(len(set(call[1] for call in self.provider.calls)), 1)
        self.assertEqual(self.stored()["refresh_token"], NEW_REFRESH)
        access = auth.get_slack_access(self.directory, required_scopes=auth.DEFAULT_USER_SCOPES,
                                       _provider=self.provider, _clock=self.clock)
        self.assertEqual(access.access_token, NEW_ACCESS)
        self.assertEqual(len(self.provider.calls), 2)

    def test_definitive_saved_authentication_rejection_requires_login(self):
        for rotating in (True, False):
            for code in ("slack_reconnect_required", "slack_token_type_unsupported"):
                with self.subTest(rotating=rotating, code=code):
                    directory = self.directory / (str(rotating) + code)
                    seed(directory, saved_document(self.clock.now(), rotating=rotating))
                    provider = Provider()
                    provider.identified = auth.SlackAuthError(code)
                    self.expect_error(code, auth.slack_status, directory, check=True,
                                      _provider=provider, _clock=self.clock)
                    self.assertTrue(auth.slack_status(directory, _clock=self.clock)["needs_login"])
                    self.expect_error("slack_reconnect_required", auth.get_slack_access, directory,
                                      required_scopes=auth.DEFAULT_USER_SCOPES, _provider=provider, _clock=self.clock)
                    self.assertEqual([call[0] for call in provider.calls], ["identity"])

    def test_explicit_check_cannot_contact_provider_before_unchecked_state_is_durable(self):
        self.save(rotating=False)
        before = (self.directory / "credentials.json").read_bytes()
        with patch.object(auth, "_write_credentials", side_effect=auth.SlackAuthError("slack_storage_failed")):
            self.expect_error("slack_storage_failed", auth.slack_status, self.directory, check=True,
                              _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.provider.calls, [])
        self.assertEqual((self.directory / "credentials.json").read_bytes(), before)

    def test_failed_quarantine_write_still_requires_identity_on_restart(self):
        self.save(rotating=False)
        self.provider.identified["team_id"] = "T87654321"
        write = auth._write_credentials

        def refuse_blocking_marker(descriptor, document):
            if document["credential_state"] == "refresh_pending":
                raise auth.SlackAuthError("slack_storage_failed")
            return write(descriptor, document)

        with patch.object(auth, "_write_credentials", side_effect=refuse_blocking_marker):
            self.expect_error("slack_storage_failed", auth.slack_status, self.directory, check=True,
                              _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.stored()["credential_state"], "identity_unchecked")
        self.expect_error("slack_identity_mismatch", auth.get_slack_access, self.directory,
                          required_scopes=auth.DEFAULT_USER_SCOPES, _provider=self.provider, _clock=self.clock)
        self.assertEqual([call[0] for call in self.provider.calls], ["identity", "identity"])
        self.assertEqual(self.stored()["credential_state"], "refresh_pending")

    def test_refresh_save_failures_leave_safe_restart_states(self):
        for failing_write, expected_state, expected_calls in ((1, "ready", []),
                (2, "refresh_pending", ["refresh"]), (3, "identity_unchecked", ["refresh", "identity"])):
            with self.subTest(failing_write=failing_write):
                directory = self.directory / str(failing_write)
                seed(directory, saved_document(self.clock.now(), expired=True))
                provider = Provider()
                write = auth._write_credentials
                count = 0

                def fail_selected(descriptor, document):
                    nonlocal count
                    count += 1
                    if count == failing_write:
                        raise auth.SlackAuthError("slack_storage_failed")
                    return write(descriptor, document)

                with patch.object(auth, "_write_credentials", side_effect=fail_selected):
                    self.expect_error("slack_storage_failed", auth.slack_status, directory, check=True,
                                      _provider=provider, _clock=self.clock)
                stored = json.loads((directory / "credentials.json").read_text())
                self.assertEqual(stored["credential_state"], expected_state)
                self.assertEqual([call[0] for call in provider.calls], expected_calls)
                if failing_write == 2:
                    self.expect_error("slack_reconnect_required", auth.slack_status, directory, check=True,
                                      _provider=provider, _clock=self.clock)
                    self.assertEqual(len(provider.calls), 1)
                elif failing_write == 3:
                    self.assertEqual(stored["refresh_token"], NEW_REFRESH)
                    auth.slack_status(directory, check=True, _provider=provider, _clock=self.clock)
                    self.assertEqual([call[0] for call in provider.calls], ["refresh", "identity", "identity"])

    def test_ambiguous_refresh_and_temporary_identity_failure_have_different_recovery(self):
        self.save(expired=True)
        self.provider.replacement = auth.SlackAuthError("slack_timeout")
        self.expect_error("slack_timeout", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.stored()["credential_state"], "refresh_pending")
        self.expect_error("slack_reconnect_required", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        self.assertEqual(len(self.provider.calls), 1)
        seed(self.directory, saved_document(self.clock.now(), expired=True))
        self.provider = Provider()
        self.provider.identified = auth.SlackAuthError("slack_unavailable")
        self.expect_error("slack_unavailable", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.stored()["credential_state"], "identity_unchecked")
        self.assertEqual(self.stored()["refresh_token"], NEW_REFRESH)
        self.provider.identified = identity()
        auth.slack_status(self.directory, check=True, _provider=self.provider, _clock=self.clock)
        self.assertEqual([call[0] for call in self.provider.calls], ["refresh", "identity", "identity"])

    def test_expired_unchecked_replacement_requires_login_without_more_http(self):
        self.save(expired=True)
        self.provider.identified = auth.SlackAuthError("slack_unavailable")
        self.expect_error("slack_unavailable", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.stored()["credential_state"], "identity_unchecked")
        self.assertEqual(self.stored()["access_token"], NEW_ACCESS)
        self.clock.advance(3600)
        self.provider.identified = identity()
        with self.subTest(operation="local status"):
            result = auth.slack_status(self.directory, _clock=self.clock)
            self.assertEqual(result["credential_state"], "identity_unchecked")
            self.assertTrue(result["needs_login"])
        for operation in ("check", "access"):
            with self.subTest(operation=operation):
                before = len(self.provider.calls)
                if operation == "check":
                    self.expect_error("slack_reconnect_required", auth.slack_status, self.directory, check=True,
                                      _provider=self.provider, _clock=self.clock)
                else:
                    self.expect_error("slack_reconnect_required", auth.get_slack_access, self.directory,
                                      required_scopes=auth.DEFAULT_USER_SCOPES,
                                      _provider=self.provider, _clock=self.clock)
                self.assertEqual(len(self.provider.calls), before)
        self.assertEqual(self.stored()["credential_state"], "identity_unchecked")
        self.assertEqual(self.stored()["refresh_token"], NEW_REFRESH)

    def test_refresh_mismatch_is_quarantined_and_never_releases_replacement(self):
        self.save(expired=True)
        self.provider.identified["user_id"] = "U87654321"
        self.expect_error("slack_identity_mismatch", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.stored()["credential_state"], "refresh_pending")
        self.assertEqual(self.stored()["access_token"], NEW_ACCESS)
        self.expect_error("slack_reconnect_required", auth.get_slack_access, self.directory,
                          required_scopes=auth.DEFAULT_USER_SCOPES, _provider=self.provider, _clock=self.clock)

    def test_expiry_margin_refresh_lifetime_and_clock_rollback(self):
        self.save()
        self.clock.advance(3570)
        auth.get_slack_access(self.directory, required_scopes=auth.DEFAULT_USER_SCOPES,
                              _provider=self.provider, _clock=self.clock)
        self.assertEqual(self.provider.calls[0][0], "refresh")
        self.clock.advance(31 * 86400)
        self.expect_error("slack_reconnect_required", auth.slack_status, self.directory, check=True,
                          _provider=self.provider, _clock=self.clock)
        seed(self.directory, saved_document(self.clock.now()))
        self.clock.advance(-1)
        self.expect_error("slack_reconnect_required", auth.get_slack_access, self.directory,
                          required_scopes=auth.DEFAULT_USER_SCOPES, _provider=self.provider, _clock=self.clock)

    def test_clock_rollback_after_later_verification_does_not_extend_saved_access(self):
        self.save()
        self.clock.advance(100)
        auth.slack_status(self.directory, check=True, _provider=self.provider, _clock=self.clock)
        self.clock.advance(-1)
        self.expect_error("slack_reconnect_required", auth.get_slack_access, self.directory,
                          required_scopes=auth.DEFAULT_USER_SCOPES, _provider=self.provider, _clock=self.clock)

    def test_local_disconnect_and_unconfirmed_revoke_do_not_refresh(self):
        self.save(expired=True)
        result = auth.disconnect_slack(self.directory, _provider=self.provider, _clock=self.clock)
        self.assertTrue(result["local_credentials_removed"])
        self.assertFalse(result["remote_access_token_revoked"])
        self.assertEqual(self.provider.calls, [])
        self.assertTrue((self.directory / "connection.lock").is_file())
        self.save(expired=True)
        self.provider.revoked = auth.SlackAuthError("slack_unavailable")
        result = auth.disconnect_slack(self.directory, revoke=True, _provider=self.provider, _clock=self.clock)
        self.assertTrue(result["local_credentials_removed"])
        self.assertEqual(result["revocation_error"], "slack_unavailable")
        self.assertEqual([call[0] for call in self.provider.calls], ["revoke"])
        self.assertFalse((self.directory / "credentials.json").exists())

    def test_explicit_revoke_requires_explicit_provider_confirmation(self):
        self.save()
        self.provider.revoked = {"ok": True, "revoked": False}
        result = auth.disconnect_slack(self.directory, revoke=True, _provider=self.provider, _clock=self.clock)
        self.assertFalse(result["remote_access_token_revoked"])
        self.assertEqual(result["revocation_error"], "slack_response_invalid")
        self.save()
        self.provider.revoked = {"ok": True, "revoked": True}
        result = auth.disconnect_slack(self.directory, revoke=True, _provider=self.provider, _clock=self.clock)
        self.assertTrue(result["remote_access_token_revoked"])

    def test_corrupt_oversized_duplicate_and_unknown_credentials_are_preserved(self):
        self.save()
        path = self.directory / "credentials.json"
        original = path.read_bytes()
        invalid = [b"not json", b" " * 65537, b'{"schema_version":1,"schema_version":1}',
                   json.dumps({**self.stored(), "unexpected": "private"}).encode()]
        for raw in invalid:
            with self.subTest(kind=len(raw)):
                path.write_bytes(raw)
                self.expect_error("slack_credentials_invalid", auth.slack_status, self.directory)
                self.assertEqual(path.read_bytes(), raw)
        path.write_bytes(original)

    def test_private_permissions_links_ownership_and_unrelated_directories_refuse(self):
        self.save()
        path = self.directory / "credentials.json"
        path.chmod(0o644)
        self.expect_error("slack_private_state_invalid", auth.slack_status, self.directory)
        path.chmod(0o600)
        link = Path(self.temp.name) / "hardlink"
        os.link(path, link)
        self.expect_error("slack_private_state_invalid", auth.slack_status, self.directory)
        link.unlink()
        with patch.object(auth.os, "getuid", return_value=os.getuid() + 1):
            self.expect_error("slack_private_state_invalid", auth.slack_status, self.directory)
        alias = Path(self.temp.name) / "alias"
        alias.symlink_to(self.directory, target_is_directory=True)
        self.expect_error("slack_private_state_invalid", auth.slack_status, alias)
        unrelated = Path(self.temp.name) / "unrelated"
        unrelated.mkdir(mode=0o700)
        (unrelated / "owner.json").write_text('{}')
        self.expect_error("slack_private_state_invalid", auth.slack_status, unrelated)
        self.assertEqual([p.name for p in unrelated.iterdir()], ["owner.json"])

    def test_atomic_replace_failure_keeps_old_credentials_and_cleans_temporary_file(self):
        self.save()
        before = (self.directory / "credentials.json").read_bytes()
        with patch.object(auth.os, "replace", side_effect=OSError("invented-private-path")):
            self.expect_error("slack_storage_failed", auth.slack_status, self.directory, check=True,
                              _provider=self.provider, _clock=self.clock)
        self.assertEqual((self.directory / "credentials.json").read_bytes(), before)
        self.assertEqual(set(p.name for p in self.directory.iterdir()), {"connection.lock", "credentials.json"})

    def test_two_processes_refresh_once_and_retry_reads_latest_token(self):
        seed(self.directory, saved_document(datetime.now(timezone.utc), expired=True))
        context = multiprocessing.get_context("spawn")
        started, release, retry, output = context.Event(), context.Event(), context.Event(), context.Queue()
        request_log = str(Path(self.temp.name) / "requests.txt")
        first = context.Process(target=_refresh_process, args=(str(self.directory), started, release, output, request_log))
        second = context.Process(target=_competing_process, args=(str(self.directory), retry, output, request_log))
        first.start()
        try:
            self.assertTrue(started.wait(5))
            second.start()
            self.assertEqual(output.get(timeout=5), ("busy", "slack_connection_busy"))
            release.set()
            self.assertEqual(output.get(timeout=5), ("first", True))
            first.join(5)
            self.assertEqual(first.exitcode, 0)
            retry.set()
            self.assertEqual(output.get(timeout=5), ("retry", True))
            second.join(5)
            self.assertEqual(second.exitcode, 0)
            self.assertEqual(Path(request_log).read_text(), "refresh\n")
        finally:
            release.set()
            retry.set()
            for process in (first, second):
                if process.pid is not None:
                    process.join(5)
                    if process.is_alive():
                        process.terminate()
                        process.join(5)


class ValidationTests(unittest.TestCase):
    def setUp(self):
        self.clock = Clock()
        self.config = auth.validate_configuration(client_id=CLIENT, redirect_uri=REDIRECT)

    def test_initial_bot_mixed_enterprise_and_scope_mismatches_refuse(self):
        variants = []
        for key, value in (("access_token", "invented-bot"), ("bot_user_id", "U87654321"),
                           ("is_enterprise_install", True)):
            variants.append(({**grant(), key: value}, "slack_token_type_unsupported"))
        for scope in ("channels:history,chat:write", "channels:history,channels:history", " channels:history", ""):
            document = grant()
            document["authed_user"]["scope"] = scope
            variants.append((document, "slack_scope_mismatch"))
        for document, code in variants:
            with self.subTest(code=code):
                with self.assertRaises(auth.SlackAuthError) as caught:
                    auth._parse_initial(document, configuration=self.config, obtained_at=self.clock.now())
                self.assertEqual(caught.exception.code, code)

    def test_nonrotating_private_scope_and_workspace_origin_normalization(self):
        config = auth.validate_configuration(client_id=CLIENT, redirect_uri=REDIRECT, user_scopes=("groups:history",))
        document = grant(rotating=False)
        document["authed_user"]["scope"] = "groups:history"
        parsed = auth._parse_initial(document, configuration=config, obtained_at=self.clock.now())
        self.assertIsNone(parsed["refresh_token"])
        self.assertIsNone(parsed["access_expires_at"])
        self.assertEqual(auth._origin("https://invented.slack.com:443/"), "https://invented.slack.com")
        for value in (None, "https://slack.com", "https://invented.slack.com.example/", "http://invented.slack.com/",
                      "https://user@invented.slack.com/", "https://invented.slack.com/?secret=1",
                      "https://invented.slack.com:444/", "https://one.two.slack.com/", "https://invented.slack.com/\n",
                      "\x00https://invented.slack.com/"):
            self.assertIsNone(auth._origin(value))

    def test_refresh_shapes_and_pin_fields_are_strict(self):
        previous = saved_document(self.clock.now())
        flat = replacement()
        nested = {"ok": True, "authed_user": {key: value for key, value in flat.items() if key != "ok"}}
        for document in (flat, nested):
            parsed = auth._parse_refresh(document, previous=previous, obtained_at=self.clock.now())
            self.assertEqual(parsed["credential_state"], "identity_unchecked")
            self.assertEqual(parsed["refresh_token"], NEW_REFRESH)
        for change in ({"app_id": "A87654321"}, {"team": {"id": "T87654321"}}, {"user_id": "U87654321"}):
            with self.assertRaises(auth.SlackAuthError) as caught:
                auth._parse_refresh({**flat, **change}, previous=previous, obtained_at=self.clock.now())
            self.assertEqual(caught.exception.code, "slack_identity_mismatch")
        for change in ({"expires_in": True}, {"expires_in": 86401}, {"refresh_token": None}, {"token_type": "bearer"}):
            with self.assertRaises(auth.SlackAuthError):
                auth._parse_refresh({**flat, **change}, previous=previous, obtained_at=self.clock.now())

    def test_every_returned_refresh_identity_must_match_without_overwriting_conflicts(self):
        previous = saved_document(self.clock.now())
        user = {key: value for key, value in replacement().items() if key != "ok"}
        document = {"ok": True, "authed_user": {**user, "id": "U87654321"}, "user_id": USER}
        with self.assertRaises(auth.SlackAuthError) as caught:
            auth._parse_refresh(document, previous=previous, obtained_at=self.clock.now())
        self.assertEqual(caught.exception.code, "slack_identity_mismatch")


class ProviderTests(unittest.TestCase):
    def test_fixed_requests_bearer_and_forms(self):
        requests = []

        def respond(request):
            requests.append(request)
            return httpx2.Response(200, json={"ok": True})

        provider = auth.SlackProvider(_transport=httpx2.MockTransport(respond))
        deadline = time.monotonic() + 10
        provider.exchange("invented-code", "v" * 43, CLIENT, REDIRECT, deadline)
        provider.refresh(REFRESH, CLIENT, deadline)
        provider.identity(ACCESS, deadline)
        provider.revoke(ACCESS, deadline)
        self.assertEqual([request.method for request in requests], ["POST", "POST", "POST", "GET"])
        self.assertEqual([str(request.url) for request in requests], [auth._TOKEN_URL, auth._TOKEN_URL,
                                                                    auth._IDENTITY_URL, auth._REVOKE_URL])
        first = parse_qs(requests[0].content.decode())
        self.assertEqual(set(first), {"grant_type", "client_id", "code", "redirect_uri", "code_verifier"})
        second = parse_qs(requests[1].content.decode())
        self.assertEqual(set(second), {"grant_type", "client_id", "refresh_token"})
        for request in requests[2:]:
            self.assertEqual(request.headers["Authorization"], "Bearer " + ACCESS)
            self.assertFalse(request.url.query)
            self.assertFalse(request.content)

    def test_bounded_decoding_and_sanitized_provider_failures(self):
        responses = [httpx2.Response(302, headers={"location": "https://secret.invalid/"}),
                     httpx2.Response(429), httpx2.Response(500),
                     httpx2.Response(200, content=b'{"ok":true,"ok":false}'),
                     httpx2.Response(200, content=b'{"ok":true,"value":NaN}'),
                     httpx2.Response(200, content=b'\xff'),
                     httpx2.Response(200, content=b'x' * 65537),
                     httpx2.Response(200, json={"ok": False, "error": "invented-private-token-error"})]
        for response in responses:
            with self.subTest(status=response.status_code, size=len(response.content)):
                provider = auth.SlackProvider(_transport=httpx2.MockTransport(lambda request: response))
                with self.assertRaises(auth.SlackAuthError) as caught:
                    provider.identity(ACCESS, time.monotonic() + 1)
                self.assertIn(caught.exception.code, auth.SLACK_AUTH_ERROR_CODES)
                self.assertNotIn("secret", str(caught.exception))
                self.assertNotIn("invented-private", str(caught.exception))

    def test_exhausted_deadline_does_not_start_request(self):
        requests = []
        provider = auth.SlackProvider(_transport=httpx2.MockTransport(lambda request: requests.append(request)))
        with self.assertRaises(auth.SlackAuthError) as caught:
            provider.identity(ACCESS, time.monotonic() - 1)
        self.assertEqual(caught.exception.code, "slack_timeout")
        self.assertEqual(requests, [])


class ConnectTests(_StateCase):
    def connect(self, **options):
        def consent(redirect_uri, *, authorization_url_for_state, finish, open_browser, timeout_seconds):
            url = authorization_url_for_state("invented-state")
            fields = parse_qs(urlsplit(url).query, keep_blank_values=True)
            self.assertEqual(fields["scope"], [""])
            self.assertEqual(fields["code_challenge_method"], ["S256"])
            self.assertEqual(fields["redirect_uri"], [REDIRECT])
            self.assertEqual(timeout_seconds, 600)
            return finish("invented-code", deadline=self.clock.monotonic() + 600)

        with patch("gtm_connector.slack_callback.receive_consent", side_effect=consent):
            return auth.connect_slack(self.directory, client_id=CLIENT, redirect_uri=REDIRECT,
                                       _provider=self.provider, _clock=self.clock, **options)

    def test_connect_publishes_only_verified_private_state(self):
        events = []
        result = self.connect(progress=lambda *event: events.append(event))
        self.assertEqual(result["verification"], "live")
        self.assertEqual(self.stored()["identity"], IDENTITY)
        self.assertEqual([call[0] for call in self.provider.calls], ["exchange", "identity"])
        self.assertEqual(events[-1], ("save", "passed"))
        stored = (self.directory / "credentials.json").read_text()
        self.assertNotIn("invented-code", stored)
        self.assertNotIn("invented-state", stored)
        for secret in (ACCESS, REFRESH, TEAM, USER, APP):
            self.assertNotIn(secret, json.dumps(result) + repr(events))

    def test_failed_reauthentication_preserves_original_file(self):
        self.save()
        before = (self.directory / "credentials.json").read_bytes()
        self.provider.identified["team_id"] = "T87654321"
        self.expect_error("slack_identity_mismatch", self.connect)
        self.assertEqual((self.directory / "credentials.json").read_bytes(), before)

    def test_invalid_configuration_precedes_state_or_browser(self):
        with patch("gtm_connector.slack_callback.receive_consent", side_effect=AssertionError("browser started")):
            self.expect_error("slack_invalid_configuration", auth.connect_slack, self.directory,
                              client_id="private\ninvalid", redirect_uri=REDIRECT)
        self.assertFalse(self.directory.exists())


if __name__ == "__main__":
    unittest.main()
