"""Diagnostics inspect local setup without grants, source reads, or secret output."""

from datetime import datetime, timedelta, timezone
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from gtm_connector.doctor import doctor
from gtm_connector.runtime import Runtime


class DoctorTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name).resolve() / "state"

    def test_new_state_reports_fix_without_creating_any_file(self):
        result = doctor(self.state)
        self.assertEqual(result["status"], "failed")
        failures = [check for check in result["checks"] if check["status"] == "fail"]
        self.assertEqual([row["name"] for row in failures], ["state_folder"])
        # debug serve reads the folder's profile, so a folder with none is connected first.
        self.assertIn(f"gtm-connector connect demo --state-dir {self.state} (or connect salesforce --org ALIAS), "
                      f"then gtm-connector debug serve --state-dir {self.state}", failures[0]["fix"])
        self.assertFalse(self.state.exists())

    def test_running_services_pass_without_changing_state_or_authorizing(self):
        with Runtime(self.state) as runtime:
            before = {path.name: (path.read_bytes(), path.stat().st_mtime_ns, path.stat().st_mode)
                      for path in self.state.iterdir() if path.is_file()}
            result = doctor(self.state)
            self.assertEqual(result["status"], "passed", result)
            self.assertTrue(all(row["status"] == "pass" for row in result["checks"]))
            self.assertEqual(runtime.connector.authority.tokens, {})
            self.assertEqual(runtime.connector.authority.codes, {})
            self.assertEqual(runtime.connector.audit.read(), [])
            after = {path.name: (path.read_bytes(), path.stat().st_mtime_ns, path.stat().st_mode)
                     for path in self.state.iterdir() if path.is_file()}
            self.assertEqual(after, before)
            self.assertNotIn(runtime.config["owner_secret"], json.dumps(result))

    def test_ancestor_alias_works_but_a_symlinked_state_folder_is_rejected(self):
        real_parent = self.state.parent / "real-parent"
        real_parent.mkdir(mode=0o700)
        alias_parent = self.state.parent / "alias-parent"
        alias_parent.symlink_to(real_parent, target_is_directory=True)
        with Runtime(alias_parent / "state") as runtime:
            result = doctor(alias_parent / "state")
            self.assertEqual(result["status"], "passed", result)
            state_alias = self.state.parent / "state-alias"
            state_alias.symlink_to(runtime.directory, target_is_directory=True)
            result = doctor(state_alias)
            self.assertEqual(result["status"], "failed")
            self.assertEqual(result["checks"][-1]["name"], "state_folder")
            self.assertEqual(runtime.connector.authority.tokens, {})
            self.assertEqual(runtime.connector.audit.read(), [])

    def test_parent_traversal_after_symlink_matches_runtime_resolution(self):
        from gtm_connector.connections import connect

        real_parent = self.state.parent / "nested"
        target = real_parent / "child"
        target.mkdir(parents=True, mode=0o700)
        alias = self.state.parent / "jump"
        alias.symlink_to(target, target_is_directory=True)
        supplied = alias / ".." / "state"
        with Runtime(supplied) as runtime:
            self.assertEqual(runtime.directory, real_parent / "state")
            result = doctor(supplied)
            self.assertEqual(result["status"], "passed", result)
            config = connect(supplied, "inspector", ["crm:read"])
            self.assertEqual(Path(config["config"]), runtime.directory / "inspector.json")
            child = runtime.directory / "inside"
            child.mkdir(mode=0o700)
            result = doctor(child / "..")
            self.assertEqual(result["status"], "passed", result)
            self.assertEqual(runtime.connector.audit.read(), [])

    def test_stopped_services_report_restart_without_exception_details(self):
        with Runtime(self.state):
            pass
        result = doctor(self.state)
        self.assertEqual(result["status"], "failed")
        failed = [row for row in result["checks"] if row["status"] == "fail"]
        self.assertEqual([row["name"] for row in failed], ["services"])
        self.assertIn("gtm-connector debug serve", failed[0]["fix"])
        self.assertNotIn("Connection refused", json.dumps(result))

    def test_salesforce_restart_fixes_name_the_serve_command_without_disclosing_the_connection(self):
        identity = {"schema_version": 1, "backend": "salesforce",
                    "instance_url": "https://private-invented.my.salesforce.com",
                    "organization_id": "00D000000000001AAA", "environment": "production"}
        reader = SimpleNamespace(name="salesforce", prepare_connection=lambda: dict(identity))
        with Runtime(self.state, crm_reader=reader) as runtime:
            secret = runtime.config["owner_secret"]
        expired = Mock(not_valid_before_utc=datetime.now(timezone.utc) - timedelta(days=2),
                       not_valid_after_utc=datetime.now(timezone.utc) - timedelta(days=1))
        results = [doctor(self.state)]
        with patch("cryptography.x509.load_pem_x509_certificate", return_value=expired):
            results.append(doctor(self.state))
        for result, name in zip(results, ("services", "certificate")):
            self.assertEqual(result["checks"][-1]["name"], name)
            fix = result["checks"][-1]["fix"]
            # debug serve reads the folder's profile, so the one command restarts the same connection.
            self.assertIn("gtm-connector debug serve --state-dir " + str(self.state.resolve()), fix)
            self.assertNotIn("--help", fix)
            for private in (secret, identity["instance_url"], identity["organization_id"]):
                self.assertNotIn(private, json.dumps(result))

    def test_hostile_owner_values_never_reach_network_or_diagnostic_output(self):
        with Runtime(self.state) as runtime:
            owner = self.state / "owner.json"
            original = dict(runtime.config)
            cases = [
                {**original, "issuer": "https://private-credential.example:443"},
                {**original, "resource": "https://127.0.0.1:443/mcp?private-credential"},
                {**original, "cert_path": "/private-credential/file"},
                {**original, "redirect_uri": "http://127.0.0.1:444/private-credential"},
                {**original, "client_id": {"private-credential": "value"}},
                ["private-credential"],
            ]
            for case in cases:
                with self.subTest(case=cases.index(case)):
                    owner.write_text(json.dumps(case))
                    with patch("gtm_connector.client.LocalClient") as client:
                        result = doctor(self.state)
                    client.assert_not_called()
                    self.assertEqual(result["status"], "failed")
                    self.assertNotIn("private-credential", json.dumps(result))
                    self.assertNotIn(original["owner_secret"], json.dumps(result))

    def test_shared_or_symlinked_files_fail_before_client_creation(self):
        with Runtime(self.state):
            owner = self.state / "owner.json"
            owner.chmod(0o644)
            with patch("gtm_connector.client.LocalClient") as client:
                result = doctor(self.state)
            client.assert_not_called()
            self.assertEqual(result["checks"][-1]["name"], "state_files")
            owner.chmod(0o600)
            target = self.state / "saved-owner.json"
            owner.rename(target)
            owner.symlink_to(target)
            with patch("gtm_connector.client.LocalClient") as client:
                result = doctor(self.state)
            client.assert_not_called()
            self.assertEqual(result["checks"][-1]["status"], "fail")

    def test_expired_certificate_and_missing_dependencies_provide_fixes(self):
        with Runtime(self.state):
            expired = Mock(not_valid_before_utc=datetime.now(timezone.utc) - timedelta(days=2),
                           not_valid_after_utc=datetime.now(timezone.utc) - timedelta(days=1))
            with patch("cryptography.x509.load_pem_x509_certificate", return_value=expired), \
                    patch("gtm_connector.client.LocalClient") as client:
                result = doctor(self.state)
            client.assert_not_called()
            self.assertEqual(result["checks"][-1]["name"], "certificate")
            self.assertIn("restart", result["checks"][-1]["fix"])
            with patch("gtm_connector.doctor.importlib.util.find_spec", return_value=None), \
                    patch("gtm_connector.client.LocalClient") as client:
                result = doctor(self.state)
            client.assert_not_called()
            failed = [row for row in result["checks"] if row["status"] == "fail"]
            self.assertEqual([row["name"] for row in failed], ["dependencies"])
            self.assertTrue(failed[0]["fix"])


if __name__ == "__main__":
    unittest.main()
