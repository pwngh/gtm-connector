"""Actual local HTTPS proves selected Slack scope and check-only source isolation."""

from contextlib import ExitStack
from copy import deepcopy
import asyncio
import fcntl
import json
from pathlib import Path
import sqlite3
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch
import uuid

from gtm_connector import slack_mapping
from gtm_connector.call_workflow import CRMResolutionError, resolve_crm_account, resolve_call_account
from gtm_connector.client import LocalClient
from gtm_connector.doctor import doctor, load_local_owner
from gtm_connector.runtime import Runtime, SlackCheckRuntime, SourceIdentityError, StateInUseError, _calls_binding
from gtm_connector.slack_auth import SlackConnectionSelection
from gtm_connector.slack_reader import ThreadRead


IDENTITY = {"app_id": "A000001", "team_id": "T000001", "user_id": "U000001", "enterprise_id": None}
SLACK_BINDING = slack_mapping.slack_identity_binding(IDENTITY)
SF_IDENTITY = {"schema_version": 1, "backend": "salesforce", "environment": "sandbox",
               "instance_url": "https://invented-slack-org.my.salesforce.com", "organization_id": "00D000000000001AAA"}
SELECTION = SlackConnectionSelection(IDENTITY, "https://invented.slack.com", ("channels:history",), "ready", False)


class SelectedReader:
    """An invented selected source; no credential/provider integration is claimed here."""
    name = "slack"

    def __init__(self, state, connection, *, crm_binding=None):
        self.mapping_path = state / "slack-mappings.json"
        self.connection_dir = connection
        self.crm_binding = crm_binding or _calls_binding(None)
        self.slack_binding = deepcopy(SLACK_BINDING)
        self.reads = []

    def read_slack_messages(self, account_id):
        selected = slack_mapping.load_slack_mapping(self.mapping_path, account_id=account_id,
            crm_binding=self.crm_binding, slack_binding=self.slack_binding)
        self.reads.append(account_id)
        return ThreadRead([{"id": "msg-" + "a" * 64, "account_id": account_id,
                            "body": "Invented selected source text."}], {"mapping": selected})


class LiveSlackRuntimeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name).resolve()
        self.state, self.connection = self.root / "state", self.root / "connection"

    def open_normal(self, *, calls_backend="fixture", sf=False):
        crm_binding = _calls_binding(SF_IDENTITY if sf else None)
        reader = SelectedReader(self.state, self.connection, crm_binding=crm_binding)
        crm = SimpleNamespace(name="salesforce", prepare_connection=Mock(return_value=deepcopy(SF_IDENTITY))) if sf else None
        runtime = Runtime(self.state, crm_reader=crm, calls_backend=calls_backend, slack_backend="slack",
                          slack_connection_dir=self.connection, slack_reader=reader)
        return runtime, reader

    def save_mapping(self, runtime, *, sf=False):
        if sf:
            association = {"account_id": "acc-sf-001000000000001AAA", "crm_binding": runtime.crm_binding,
                           "resolution_receipt_id": str(uuid.uuid4())}
        else:
            association = resolve_crm_account(runtime, account_id="acc-001")
        slack_mapping.save_slack_mapping(runtime.slack_mapping_path, association=association,
            slack_binding=runtime.slack_binding, selection={"channel_id": "CSELECTED", "thread_ts": "1700000000.000001"},
            selected_at="2026-11-18T10:00:00Z")
        return association["account_id"]

    def snapshot(self):
        return {path.name: path.read_bytes() for path in self.state.iterdir()
                if path.is_file() and path.name not in ("audit.sqlite3", "server.lock")}

    def initialize(self, **options):
        runtime, reader = self.open_normal(**options)
        with patch("gtm_connector.slack_auth.inspect_slack_connection", return_value=SELECTION), runtime:
            account = self.save_mapping(runtime, sf=options.get("sf", False))
        return account, reader

    def test_fresh_imported_calls_and_live_slack_initialize_together_without_fixtures(self):
        runtime, reader = self.open_normal(calls_backend="imported")
        with patch("gtm_connector.slack_auth.inspect_slack_connection", return_value=SELECTION), \
                patch("gtm_connector.runtime.slack.seed") as seed, runtime:
            self.assertFalse((self.state / "slack.sqlite3").exists())
            self.assertFalse((self.state / "calls.sqlite3").exists())
            self.assertTrue((self.state / "imports.sqlite3").is_file())
            self.assertEqual(runtime.crm_binding, runtime.calls_binding)
            self.assertEqual(runtime.config["slack_backend"], "slack")
            self.assertEqual(slack_mapping.validate_slack_mapping_store(runtime.slack_mapping_path,
                crm_binding=runtime.crm_binding, slack_binding=runtime.slack_binding), 0)
            self.assertEqual(reader.reads, [])
            seed.assert_not_called()
            with patch("gtm_connector.slack_auth.inspect_slack_connection", side_effect=AssertionError("credentials")):
                self.assertEqual(load_local_owner(self.state)["slack_backend"], "slack")
                self.assertEqual(doctor(self.state)["status"], "passed")

    def test_shared_resolver_works_without_imported_calls_and_retains_compatibility_guard(self):
        from gtm_connector.calls_import import CallsImportError
        runtime, _ = self.open_normal()
        with patch("gtm_connector.slack_auth.inspect_slack_connection", return_value=SELECTION), runtime:
            association = resolve_crm_account(runtime, account_id="acc-001")
            self.assertEqual(association["account_id"], "acc-001")
            self.assertEqual(association["crm_binding"], runtime.crm_binding)
            self.assertEqual(len(runtime.connector.audit.read()), 1)
            with self.assertRaises(CallsImportError) as caught:
                resolve_call_account(runtime, account_id="acc-001")
            self.assertEqual(caught.exception.code, "calls_source_mismatch")
            with patch("gtm_connector.client.LocalClient") as client:
                with self.assertRaises(CRMResolutionError) as invalid:
                    resolve_crm_account(runtime, account_id="private malformed selector")
                self.assertEqual(invalid.exception.code, "crm_account_id_invalid")
                client.assert_not_called()

    def test_source_switches_refuse_before_source_preflight_and_keep_private_state(self):
        self.initialize(sf=True)
        before = self.snapshot()
        changed = SlackConnectionSelection({**IDENTITY, "team_id": "T000002"}, None,
                                            ("channels:history",), "ready", False)
        runtime, _ = self.open_normal(sf=True)
        with patch("gtm_connector.slack_auth.inspect_slack_connection", return_value=changed):
            with self.assertRaises(slack_mapping.SlackMappingError) as caught:
                with runtime:
                    self.fail("Changed Slack identity was adopted")
        self.assertEqual(caught.exception.code, "slack_source_mismatch")
        runtime.crm_reader.prepare_connection.assert_not_called()
        with self.assertRaises(slack_mapping.SlackMappingError):
            with Runtime(self.state):
                self.fail("Live Slack became a fixture")
        self.assertEqual(self.snapshot(), before)
        (self.state / "slack-mappings.json").unlink()
        runtime, _ = self.open_normal(sf=True)
        with patch("gtm_connector.slack_auth.inspect_slack_connection") as inspect:
            with self.assertRaises(slack_mapping.SlackMappingError):
                with runtime:
                    self.fail("Missing established mappings were silently recreated")
            inspect.assert_not_called()

    def test_slack_only_https_check_never_opens_disabled_sources_or_rewrites_owner_tls(self):
        account, reader = self.initialize(sf=True)
        (self.state / "crm.sqlite3").unlink()
        (self.state / "calls.sqlite3").unlink()
        before = self.snapshot()
        sentinels = ("gtm_connector.runtime._prepare_source_state", "gtm_connector.runtime._seed_demo_sources",
                     "gtm_connector.gateway.store.connect", "gtm_connector.gateway.calls.connect",
                     "gtm_connector.gateway.slack.connect", "gtm_connector.salesforce.SFCredentials",
                     "gtm_connector.slack_auth.inspect_slack_connection")
        with ExitStack() as guards:
            for target in sentinels:
                guards.enter_context(patch(target, side_effect=AssertionError("Disabled source was touched")))
            with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader) as runtime:
                cert = Path(runtime.config["cert_path"])
                self.assertNotEqual(cert.parent, self.state)
                self.assertTrue(cert.exists())
                with LocalClient(runtime.config) as client:
                    token = client.authorize(["crm:read", "calls:read", "slack:read", "crm:write"])
                    metadata = client._http.get(client._resource_metadata_url).json()
                    self.assertEqual(metadata["scopes_supported"], ["slack:read"])
                    tools = client.rpc(token, "tools/list").json()["result"]["tools"]
                    self.assertEqual([tool["name"] for tool in tools], ["read_slack_messages"])
                    blocked = {"search_accounts": {"query": ""}, "get_account": {"account_id": account},
                               "list_opportunities": {"account_id": account}, "read_call_notes": {"account_id": account},
                               "log_activity": {"account_id": account, "kind": "note", "body": "never",
                                                "idempotency_key": "disabled"}}
                    for tool, arguments in blocked.items():
                        response = client.call(token, tool, arguments, brief_id="disabled-tools")
                        payload = response.json()
                        self.assertTrue(response.status_code >= 400 or payload.get("error")
                                        or payload.get("result", {}).get("isError"))
                    self.assertEqual(reader.reads, [])
                    rows = runtime.connector.audit.read("disabled-tools")
                    self.assertEqual(len(rows), 5)
                    self.assertTrue(all(row["status"] == "error" for row in rows))
                    response = client.call(token, "read_slack_messages", {"account_id": account})
                    self.assertEqual(response.status_code, 200)
                    self.assertEqual(response.json()["result"]["structuredContent"]["records"][0]["account_id"], account)
                with patch("gtm_connector.gateway.get_access_token") as token_getter:
                    with self.assertRaises(PermissionError):
                        runtime.connector._require_tool("get_account")
                    token_getter.assert_not_called()
            self.assertFalse(cert.exists())
        self.assertEqual(self.snapshot(), before)
        self.assertEqual(reader.reads, [account])
        self.assertFalse((self.state / "crm.sqlite3").exists())
        self.assertFalse((self.state / "calls.sqlite3").exists())

    def test_scope_and_pending_audit_failure_precede_mapping_or_reader_work(self):
        account, reader = self.initialize()
        with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader) as runtime:
            with LocalClient(runtime.config) as client:
                denied = client.authorize(["crm:read"])
                allowed = client.authorize(["slack:read"])
                with patch.object(slack_mapping, "load_slack_mapping", side_effect=AssertionError("Mapping opened")):
                    self.assertEqual(client.call(denied, "read_slack_messages", {"account_id": account}).status_code, 403)
                    with patch.object(runtime.connector.audit, "start", side_effect=sqlite3.OperationalError("private")):
                        self.assertEqual(client.call(allowed, "read_slack_messages", {"account_id": account}).status_code, 503)
                self.assertEqual(reader.reads, [])
                with patch.object(runtime.connector.audit, "finish", side_effect=sqlite3.OperationalError("private")):
                    response = client.call(allowed, "read_slack_messages", {"account_id": account})
                self.assertEqual(response.status_code, 503)
                self.assertNotIn("Invented selected source text", response.text)
                self.assertEqual(reader.reads, [account])
                self.assertEqual(runtime.connector.audit.read()[-1]["status"], "pending")

    def test_check_requires_existing_audit_and_lock_without_recreating_them(self):
        account, reader = self.initialize()
        with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
            with self.assertRaises(StateInUseError):
                with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
                    self.fail("Second owner acquired the same check state")
        for name in ("audit.sqlite3", "server.lock"):
            path = self.state / name
            saved = path.read_bytes()
            path.unlink()
            with self.assertRaises(slack_mapping.SlackMappingError):
                with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
                    self.fail("Missing established check state was created")
            self.assertFalse(path.exists())
            path.write_bytes(saved)
            path.chmod(0o600)
        self.assertEqual(reader.reads, [])

    def test_check_start_failure_and_interrupt_clean_ephemeral_tls_and_release_lock(self):
        from gtm_connector import runtime as module
        account, reader = self.initialize()
        before = self.snapshot()
        certificates = []
        original = module.certificate

        def observe(directory):
            result = original(directory)
            certificates.append(result[1])
            return result

        with patch.object(module, "certificate", side_effect=observe), \
                patch.object(module.uvicorn.Server, "run", return_value=None):
            with self.assertRaisesRegex(RuntimeError, "local HTTPS services did not start"):
                with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
                    self.fail("Failed service startup returned a check context")
        self.assertEqual(len(certificates), 1)
        self.assertFalse(certificates[0].exists())
        with self.assertRaises(KeyboardInterrupt):
            with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader) as runtime:
                certificate = Path(runtime.config["cert_path"])
                raise KeyboardInterrupt
        self.assertFalse(certificate.exists())
        self.assertEqual(self.snapshot(), before)
        with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
            pass

    def test_interrupt_waits_for_active_provider_and_final_audit_before_cleanup(self):
        import httpx2
        from gtm_connector import runtime as module
        from gtm_connector.slack_auth import SlackAccess
        from gtm_connector.slack_reader import SlackThreadReader, SlackRepliesProvider

        account, selected = self.initialize()
        before = self.snapshot()
        started, closed = threading.Event(), threading.Event()
        service_instances, responses, errors, close_state = [], [], [], {}
        original = module._LocalHTTPS
        client = caller = None
        test = self

        class SlowResponse(httpx2.AsyncByteStream):
            async def __aiter__(self):
                started.set()
                while True:
                    yield b" "
                    await asyncio.sleep(.015)

            async def aclose(self):
                close_state["tls_exists"] = certificate.exists()
                with (test.state / "server.lock").open("r+b") as lock:
                    try:
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    except BlockingIOError:
                        close_state["lock_held"] = True
                    else:
                        close_state["lock_held"] = False
                closed.set()

        def capture(*args, **kwargs):
            services = original(*args, **kwargs)
            service_instances.append(services)
            return services

        def call():
            try:
                responses.append(client.call(token, "read_slack_messages", {"account_id": account},
                                             brief_id="interrupted-slack-check"))
            except BaseException as error:
                errors.append(type(error).__name__)

        transport = httpx2.MockTransport(lambda request: httpx2.Response(200, stream=SlowResponse()))
        reader = SlackThreadReader(self.state / "slack-mappings.json", connection_dir=self.connection,
            crm_binding=selected.crm_binding, slack_binding=SLACK_BINDING,
            _provider=SlackRepliesProvider(_transport=transport))
        access = SlackAccess("invented-not-a-real-token", IDENTITY, ("channels:history",), None)
        try:
            with patch.object(module, "_LocalHTTPS", side_effect=capture), \
                    patch("gtm_connector.slack_reader.get_slack_access", return_value=access), \
                    patch("gtm_connector.slack_reader.OPERATION_SECONDS", 1):
                with self.assertRaises(KeyboardInterrupt):
                    with SlackCheckRuntime(self.state, slack_connection_dir=self.connection,
                                           _slack_reader=reader) as runtime:
                        certificate = Path(runtime.config["cert_path"])
                        client = LocalClient(runtime.config)
                        token = client.authorize(["slack:read"])
                        caller = threading.Thread(target=call)
                        caller.start()
                        self.assertTrue(started.wait(5), "The invented source never started.")
                        # The owner client closes while unwinding an interrupted check.
                        # Its active server-side read must still finish before cleanup.
                        client.close()
                        raise KeyboardInterrupt
                self.assertTrue(closed.is_set())
                self.assertEqual(close_state, {"tls_exists": True, "lock_held": True})
                self.assertTrue(all(not thread.is_alive() for thread in service_instances[0].threads))
                self.assertFalse(certificate.exists())
                rows = runtime.connector.audit.read("interrupted-slack-check")
                self.assertEqual(len(rows), 1)
                self.assertEqual(rows[0]["status"], "error")
        finally:
            if caller is not None:
                caller.join(30)
            if client is not None:
                client.close()
            for services in service_instances:
                for thread in services.threads:
                    thread.join(30)
        self.assertFalse(caller.is_alive())
        self.assertEqual(len(errors) + len(responses), 1)
        if responses:
            self.assertIn("slack_timeout", responses[0].text)
        self.assertEqual(self.snapshot(), before)
        with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=selected):
            pass

    def test_corrupt_saved_crm_pin_and_audit_refuse_without_source_or_tls_work(self):
        account, reader = self.initialize()
        marker = self.state / "slack-source.json"
        source = marker.read_bytes()
        corrupt = json.loads(source)
        corrupt["crm_binding"]["identity_sha256"] = "f" * 64
        marker.write_text(json.dumps(corrupt))
        runtime, _ = self.open_normal(calls_backend="imported")
        with patch("gtm_connector.slack_auth.inspect_slack_connection") as credentials:
            with self.assertRaises(slack_mapping.SlackMappingError):
                with runtime:
                    self.fail("Invalid CRM pin was adopted")
            credentials.assert_not_called()
        self.assertFalse((self.state / "calls-source.json").exists())
        marker.write_bytes(source)
        audit = self.state / "audit.sqlite3"
        audit.write_bytes(b"invented corrupt database")
        with patch("gtm_connector.runtime.certificate") as tls:
            with self.assertRaises(slack_mapping.SlackMappingError) as caught:
                with SlackCheckRuntime(self.state, slack_connection_dir=self.connection, _slack_reader=reader):
                    self.fail("Invalid audit was initialized")
            self.assertEqual(caught.exception.code, "slack_mapping_invalid")
            tls.assert_not_called()
        self.assertEqual(audit.read_bytes(), b"invented corrupt database")


if __name__ == "__main__":
    unittest.main()
