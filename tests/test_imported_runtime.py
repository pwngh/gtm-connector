"""Imported calls pin their private source mode without reading transcript bytes."""

from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from gtm_connector import calls_import
from gtm_connector.doctor import doctor, load_local_owner
from gtm_connector.runtime import Runtime, SourceIdentityError, StateInUseError, open_import_state


IDENTITY = {"schema_version": 1, "backend": "salesforce",
            "instance_url": "https://invented-import-org.my.salesforce.com",
            "organization_id": "00D000000000001AAA", "environment": "developer"}


class ImportedRuntimeTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name).resolve()
        self.state = self.directory / "state"

    def reader(self, identity=None):
        return SimpleNamespace(name="salesforce", prepare_connection=Mock(
            return_value=deepcopy(IDENTITY if identity is None else identity)))

    def snapshot(self):
        return {path.name: (path.read_bytes(), stat.S_IMODE(path.stat().st_mode))
                for path in self.state.iterdir() if path.is_file() and path.name != "server.lock"}

    def test_imported_runtime_creates_no_fixture_calls_and_pins_the_fixed_fixture_book(self):
        runtime = Runtime(self.state, calls_backend="imported")
        self.assertFalse(self.state.exists())
        with patch("gtm_connector.runtime.calls.seed") as seed, \
                patch("gtm_connector.calls_import._load_record") as blobs, runtime:
            expected = {"schema_version": 1, "crm_backend": "sqlite", "identity_sha256": hashlib.sha256(
                b'{"backend":"sqlite","fixture_book":"renewal-book-v2","schema_version":1}').hexdigest()}
            self.assertEqual(runtime.calls_backend, "imported")
            self.assertEqual(runtime.calls_binding, expected)
            self.assertEqual(runtime.imports_path, self.state / "imports.sqlite3")
            self.assertTrue(runtime.imports_path.is_file())
            self.assertFalse((self.state / "calls.sqlite3").exists())
            self.assertEqual(runtime.config["calls_backend"], "imported")
            self.assertEqual(json.loads((self.state / "calls-source.json").read_text()),
                             {"schema_version": 1, "backend": "imported", "crm_binding": expected})
            for name in ("calls-source.json", "imports.sqlite3"):
                self.assertEqual(stat.S_IMODE((self.state / name).stat().st_mode), 0o600)
            seed.assert_not_called()
            blobs.assert_not_called()

    def test_salesforce_binding_uses_validated_identity_and_never_exports_org_details(self):
        reader = self.reader()
        with Runtime(self.state, crm_reader=reader, calls_backend="imported") as runtime:
            expected = hashlib.sha256(json.dumps(IDENTITY, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            self.assertEqual(runtime.calls_binding["identity_sha256"], expected)
            self.assertEqual(runtime.calls_binding["crm_backend"], "salesforce")
            for value in (IDENTITY["instance_url"], IDENTITY["organization_id"]):
                self.assertNotIn(value, json.dumps(runtime.calls_binding))
                self.assertNotIn(value, (self.state / "calls-source.json").read_text())
            reader.prepare_connection.assert_called_once()

    def test_mode_switches_refuse_before_preflight_or_seeding_and_preserve_state(self):
        with Runtime(self.state, calls_backend="imported"):
            pass
        before = self.snapshot()
        with patch("gtm_connector.runtime.calls.seed") as seed:
            with self.assertRaises(SourceIdentityError):
                with Runtime(self.state):
                    self.fail("Imported state switched to fixtures")
            seed.assert_not_called()
        self.assertEqual(self.snapshot(), before)
        fixture = self.directory / "fixture"
        with Runtime(fixture):
            pass
        reader = self.reader()
        with self.assertRaises(SourceIdentityError):
            with Runtime(fixture, crm_reader=reader, calls_backend="imported"):
                self.fail("Fixture state was adopted")
        reader.prepare_connection.assert_not_called()
        self.assertFalse((fixture / "imports.sqlite3").exists())

    def test_demo_binding_conflict_does_not_preflight_or_create_any_source(self):
        reader = self.reader()
        with self.assertRaises(SourceIdentityError):
            with Runtime(self.state, crm_reader=reader, demo_binding=Mock(), calls_backend="imported"):
                self.fail("Two source binding systems were combined")
        reader.prepare_connection.assert_not_called()
        self.assertEqual({path.name for path in self.state.iterdir()}, {"server.lock"})

    def test_changed_crm_identity_or_missing_calls_pin_preserves_imported_state(self):
        with Runtime(self.state, crm_reader=self.reader(), calls_backend="imported"):
            pass
        before = self.snapshot()
        with self.assertRaises(SourceIdentityError):
            with Runtime(self.state, crm_reader=self.reader({**IDENTITY, "organization_id": "00D000000000002AAA"}),
                         calls_backend="imported"):
                self.fail("Another org reused imports")
        self.assertEqual(self.snapshot(), before)
        (self.state / "calls-source.json").unlink()
        before = self.snapshot()
        reader = self.reader()
        with self.assertRaises(SourceIdentityError):
            with Runtime(self.state, crm_reader=reader, calls_backend="imported"):
                self.fail("Missing pin was reconstructed from records")
        reader.prepare_connection.assert_not_called()
        self.assertEqual(self.snapshot(), before)

    def test_partial_pin_and_store_initialization_retry_only_the_same_selection(self):
        from gtm_connector import runtime as module
        original = module._write_marker

        def fail_calls(path, marker):
            if path.name == "calls-source.json":
                raise OSError("invented private write failure")
            return original(path, marker)

        with patch.object(module, "_write_marker", side_effect=fail_calls), self.assertRaises(SourceIdentityError):
            with Runtime(self.state, crm_reader=self.reader(), calls_backend="imported"):
                self.fail("A partial pin started sources")
        self.assertEqual({path.name for path in self.state.iterdir()}, {"server.lock", "crm-connection.json"})
        failure = calls_import.CallsImportError("calls_import_io")
        with patch.object(calls_import, "initialize_import_store", side_effect=failure):
            with self.assertRaises(calls_import.CallsImportError):
                with Runtime(self.state, crm_reader=self.reader(), calls_backend="imported"):
                    self.fail("A failed import store started services")
        self.assertTrue((self.state / "calls-source.json").is_file())
        self.assertFalse((self.state / "calls.sqlite3").exists())
        with Runtime(self.state, crm_reader=self.reader(), calls_backend="imported"):
            pass

    def test_marker_corruption_shared_permissions_and_symlinks_never_open_the_import_store(self):
        with Runtime(self.state, calls_backend="imported"):
            pass
        marker = self.state / "calls-source.json"
        original = marker.read_bytes()
        cases = [b"not JSON", b'{"schema_version":1,"schema_version":1}',
                 original.replace(b'"identity_sha256": "', b'"identity_sha256": "0')]
        for content in cases:
            marker.write_bytes(content)
            before = self.snapshot()
            with patch.object(calls_import, "initialize_import_store") as initialize:
                with self.assertRaises(SourceIdentityError):
                    with Runtime(self.state, calls_backend="imported"):
                        self.fail("Corrupt marker was accepted")
                initialize.assert_not_called()
            self.assertEqual(self.snapshot(), before)
        marker.write_bytes(original)
        marker.chmod(0o644)
        with self.assertRaises(SourceIdentityError):
            with open_import_state(self.state):
                self.fail("Shared marker was accepted")
        marker.chmod(0o600)
        moved = self.state / "saved-marker"
        marker.rename(moved)
        marker.symlink_to(moved)
        with self.assertRaises(SourceIdentityError):
            with open_import_state(self.state):
                self.fail("Symlinked marker was accepted")

    def test_offline_inspection_is_locked_read_only_and_contacts_no_provider(self):
        with Runtime(self.state, crm_reader=self.reader(), calls_backend="imported"):
            with self.assertRaises(StateInUseError):
                with open_import_state(self.state):
                    self.fail("Offline command bypassed the running state owner")
        before = self.snapshot()
        with patch("gtm_connector.runtime.certificate") as certificate, \
                patch("gtm_connector.salesforce.SFCredentials") as credentials, \
                patch("gtm_connector.salesforce.httpx2.Client") as network, \
                patch.object(calls_import, "_load_record") as blobs:
            with open_import_state(self.state) as state:
                self.assertEqual(state.directory, self.state)
                self.assertEqual(state.crm_backend, "salesforce")
                self.assertEqual(state.imports_path, self.state / "imports.sqlite3")
            certificate.assert_not_called()
            credentials.assert_not_called()
            network.assert_not_called()
            blobs.assert_not_called()
        self.assertEqual(self.snapshot(), before)
        missing = self.directory / "missing"
        with self.assertRaises(SourceIdentityError):
            with open_import_state(missing):
                self.fail("Missing state was created")
        self.assertFalse(missing.exists())

    def test_import_database_cannot_alias_any_other_source_database(self):
        with Runtime(self.state, calls_backend="imported"):
            pass
        (self.state / "crm.sqlite3").unlink()
        os.link(self.state / "imports.sqlite3", self.state / "crm.sqlite3")
        before = self.snapshot()
        with patch.object(calls_import, "initialize_import_store") as initialize:
            with self.assertRaises(ValueError):
                with Runtime(self.state, calls_backend="imported"):
                    self.fail("Hardlinked source files were accepted")
            initialize.assert_not_called()
        self.assertEqual(self.snapshot(), before)
        with self.assertRaises(SourceIdentityError):
            with open_import_state(self.state):
                self.fail("Offline inspection accepted aliased source files")

    def test_supplied_reader_cannot_select_another_path_or_crm_binding(self):
        with Runtime(self.state, calls_backend="imported") as runtime:
            binding = dict(runtime.calls_binding)
        before = self.snapshot()
        readers = (
            calls_import.ImportedCallsReader(self.directory / "other.sqlite3", crm_binding=binding),
            calls_import.ImportedCallsReader(self.state / "imports.sqlite3",
                                            crm_binding={**binding, "identity_sha256": "0" * 64}),
        )
        for reader in readers:
            with self.subTest(path=reader.path), patch.object(calls_import, "initialize_import_store") as initialize:
                with self.assertRaises(SourceIdentityError):
                    with Runtime(self.state, calls_backend="imported", calls_reader=reader):
                        self.fail("An injected reader changed the selected source")
                initialize.assert_not_called()
            self.assertEqual(self.snapshot(), before)

    def test_imported_doctor_uses_only_fixed_source_files_and_does_not_read_blobs(self):
        with Runtime(self.state, calls_backend="imported") as runtime:
            before = self.snapshot()
            with patch.object(calls_import, "_load_record") as blobs:
                self.assertEqual(doctor(self.state)["status"], "passed")
                self.assertEqual(load_local_owner(self.state)["calls_backend"], "imported")
                blobs.assert_not_called()
            self.assertFalse((self.state / "calls.sqlite3").exists())
            self.assertEqual(self.snapshot(), before)
            self.assertEqual(runtime.connector.audit.read(), [])
            self.assertEqual(runtime.connector.authority.tokens, {})
        result = doctor(self.state)
        self.assertEqual(result["checks"][-1]["name"], "services")
        self.assertIn("--calls-backend imported", result["checks"][-1]["fix"])
        (self.state / "imports.sqlite3").unlink()
        with patch.object(calls_import, "initialize_import_store") as initialize:
            with self.assertRaises(SourceIdentityError):
                with Runtime(self.state, calls_backend="imported"):
                    self.fail("A missing existing import database was replaced")
            initialize.assert_not_called()
        with patch("gtm_connector.client.LocalClient") as network:
            result = doctor(self.state)
            self.assertEqual(result["checks"][-1]["name"], "state_files")
            network.assert_not_called()


if __name__ == "__main__":
    unittest.main()
