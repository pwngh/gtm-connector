"""A private runtime cannot silently reuse another Salesforce connection's state."""

from contextlib import closing
import copy
import json
import os
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.runtime import Runtime, SourceIdentityError
from gtm_connector.slack_mapping import SlackMappingError
from gtm_connector.salesforce_demo import bind_import, load_binding, write_binding


IDENTITY = {"schema_version": 1, "backend": "salesforce",
            "instance_url": "https://invented-pinned-org.my.salesforce.com",
            "organization_id": "00D000000000001AAA", "environment": "developer"}


class InventedReader:
    name = "salesforce"

    def __init__(self, identity=None, alias="first-invented-alias"):
        self.identity = copy.deepcopy(IDENTITY if identity is None else identity)
        self.alias = alias
        self.prepared = 0

    def prepare_connection(self):
        self.prepared += 1
        return copy.deepcopy(self.identity)

    def search_accounts(self, _query):
        raise AssertionError("State startup must not read account records")

    def get_account(self, _account):
        raise AssertionError("State startup must not read account records")

    def list_opportunities(self, _account=None):
        raise AssertionError("State startup must not read opportunities")


class SourceStateTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.state = self.directory / "state"
        self.marker = self.state / "crm-connection.json"

    def snapshot(self):
        return {str(path.relative_to(self.state)): path.read_bytes()
                for path in self.state.rglob("*") if path.is_file() and path.name != "server.lock"}

    def binding(self):
        records = {"status": 0, "result": [
            {"refId": f"{name}Ref{number}", "type": "Account", "id": prefix + f"{number:012}" + "AAA"}
            for number in range(1, 4)
            for name, prefix in (("Account", "001"), ("Contact", "003"), ("Opportunity", "006"), ("Contract", "800"))
        ]}
        path = self.directory / "binding.json"
        write_binding(path, bind_import(records))
        return load_binding(path)

    def test_pin_precedes_sources_and_survives_same_identity_with_another_alias(self):
        reader = InventedReader()
        from gtm_connector import store
        original_connect = store.connect

        def observed_connect(*args, **kwargs):
            self.assertEqual(json.loads(self.marker.read_text()), IDENTITY)
            self.assertEqual(reader.prepared, 1)
            return original_connect(*args, **kwargs)

        with patch("gtm_connector.runtime.store.connect", side_effect=observed_connect):
            with Runtime(self.state, crm_reader=reader) as runtime:
                self.assertEqual(runtime.crm_connection_identity, IDENTITY)
                self.assertEqual(runtime.config["sf_environment"], "developer")
                for private in (IDENTITY["instance_url"], IDENTITY["organization_id"], reader.alias):
                    self.assertNotIn(private, json.dumps(runtime.config))
                self.assertEqual(stat.S_IMODE(self.marker.stat().st_mode), 0o600)
                with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection, connection:
                    connection.execute("UPDATE call_notes SET body = 'Invented local edit' WHERE id = 'note-001'")
        before = self.marker.read_bytes(), self.marker.stat().st_mtime_ns
        second = InventedReader(alias="second-invented-alias")
        with Runtime(self.state, crm_reader=second):
            with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection:
                self.assertEqual(connection.execute("SELECT body FROM call_notes WHERE id = 'note-001'").fetchone(),
                                 ("Invented local edit",))
        self.assertEqual((self.marker.read_bytes(), self.marker.stat().st_mtime_ns), before)
        self.assertEqual(second.prepared, 1)

    def test_changed_connection_environment_or_fixture_mode_preserves_existing_state(self):
        with Runtime(self.state, crm_reader=InventedReader()):
            pass
        before = self.snapshot()
        alternatives = [None]
        for key, value in (("organization_id", "00D000000000002AAA"),
                           ("instance_url", "https://another-invented-org.my.salesforce.com"),
                           ("environment", "production")):
            alternatives.append(InventedReader({**IDENTITY, key: value}))
        for reader in alternatives:
            with self.subTest(reader=reader), self.assertRaises(SourceIdentityError) as failure:
                with Runtime(self.state, crm_reader=reader):
                    self.fail("A different source reused the state")
            self.assertEqual(failure.exception.code, "source_identity_mismatch")
            for value in (IDENTITY["instance_url"], IDENTITY["organization_id"]):
                self.assertNotIn(value, str(failure.exception))
            self.assertEqual(self.snapshot(), before)

    def test_preexisting_unpinned_artifacts_are_never_adopted(self):
        with Runtime(self.state):
            pass
        before = self.snapshot()
        reader = InventedReader()
        with self.assertRaisesRegex(SourceIdentityError, "fresh state"):
            with Runtime(self.state, crm_reader=reader):
                self.fail("Existing artifacts acquired an assumed org identity")
        self.assertEqual(reader.prepared, 0)
        self.assertEqual(self.snapshot(), before)
        self.assertFalse(self.marker.exists())

    def test_removed_pin_cannot_be_recreated_over_existing_sources(self):
        with Runtime(self.state, crm_reader=InventedReader()):
            pass
        self.marker.unlink()
        before = self.snapshot()
        reader = InventedReader()
        with self.assertRaisesRegex(SourceIdentityError, "fresh state"):
            with Runtime(self.state, crm_reader=reader):
                self.fail("A missing pin was silently reconstructed")
        self.assertEqual(reader.prepared, 0)
        self.assertEqual(self.snapshot(), before)

        with self.assertRaisesRegex(SourceIdentityError, "belongs to a Salesforce connection"):
            with Runtime(self.state):
                self.fail("Deleting only the pin silently selected fixtures")
        self.assertEqual(self.snapshot(), before)

    def test_unknown_or_malformed_private_owner_metadata_does_not_select_fixtures(self):
        with Runtime(self.state):
            pass
        owner = self.state / "owner.json"
        for content in (b'not JSON', b'{"owner_secret": "invented-private-secret"}'):
            with self.subTest(content=content):
                owner.write_bytes(content)
                before = self.snapshot()
                with self.assertRaisesRegex(SourceIdentityError, "prior local backend") as failure:
                    with Runtime(self.state):
                        self.fail("Unknown previous source mode selected fixtures")
                self.assertNotIn("invented-private-secret", str(failure.exception))
                self.assertEqual(self.snapshot(), before)

    def test_an_owner_file_without_a_backend_does_not_select_fixtures(self):
        with Runtime(self.state) as runtime:
            complete = dict(runtime.config)
        for missing in ("crm_backend", "calls_backend", "slack_backend"):
            with self.subTest(missing=missing):
                (self.state / "owner.json").write_text(
                    json.dumps({key: value for key, value in complete.items() if key != missing}))
                before = self.snapshot()
                with self.assertRaises((SourceIdentityError, SlackMappingError)):
                    with Runtime(self.state):
                        self.fail("An owner file without a backend selected fixtures")
                self.assertEqual(self.snapshot(), before)

    def test_corrupt_private_markers_are_rejected_without_rewriting_them(self):
        with Runtime(self.state, crm_reader=InventedReader()):
            pass
        valid = self.marker.read_bytes()
        for content in (b"invalid JSON", b"[]", b"x" * 4097,
                        valid.replace(b'"schema_version": 1', b'"schema_version": true'),
                        valid.replace(b'"schema_version": 1', b'"schema_version": 2'),
                        valid.replace(b'"organization_id":', b'"missing_organization_id":'),
                        valid.replace(b'"schema_version": 1', b'"schema_version": 1, "schema_version": 1')):
            with self.subTest(content=content[:30]):
                self.marker.write_bytes(content)
                before = self.snapshot()
                reader = InventedReader()
                with self.assertRaisesRegex(SourceIdentityError, "marker is invalid"):
                    with Runtime(self.state, crm_reader=reader):
                        self.fail("Invalid state was accepted")
                self.assertEqual(reader.prepared, 0)
                self.assertEqual(self.snapshot(), before)

    def test_private_marker_permissions_links_and_owner_are_checked_before_preflight(self):
        with Runtime(self.state, crm_reader=InventedReader()):
            pass
        valid = self.marker.read_bytes()
        self.marker.chmod(0o644)
        reader = InventedReader()
        with self.assertRaisesRegex(SourceIdentityError, "marker is invalid"):
            with Runtime(self.state, crm_reader=reader):
                self.fail("Readable-by-others marker was accepted")
        self.assertEqual(reader.prepared, 0)
        self.assertEqual(stat.S_IMODE(self.marker.stat().st_mode), 0o644)
        self.marker.chmod(0o600)
        with patch("gtm_connector.runtime.os.getuid", return_value=self.marker.stat().st_uid + 1):
            with self.assertRaisesRegex(SourceIdentityError, "marker is invalid"):
                with Runtime(self.state, crm_reader=reader):
                    self.fail("Another owner's marker was accepted")
        self.marker.unlink()
        outside = self.directory / "outside.json"
        outside.write_bytes(valid)
        self.marker.symlink_to(outside)
        with self.assertRaisesRegex(SourceIdentityError, "marker is invalid"):
            with Runtime(self.state, crm_reader=reader):
                self.fail("A marker symlink was followed")
        self.assertEqual(outside.read_bytes(), valid)
        self.assertEqual(reader.prepared, 0)
        self.marker.unlink()
        os.mkfifo(self.marker, 0o600)
        with self.assertRaisesRegex(SourceIdentityError, "marker is invalid"):
            with Runtime(self.state, crm_reader=reader):
                self.fail("A marker FIFO was opened")
        self.assertEqual(reader.prepared, 0)

    def test_failed_pin_write_publishes_no_partial_marker_or_data_and_can_retry(self):
        with patch("gtm_connector.runtime.os.replace", side_effect=OSError("invented failure")):
            with self.assertRaisesRegex(SourceIdentityError, "could not be saved"):
                with Runtime(self.state, crm_reader=InventedReader()):
                    self.fail("A failed identity write started sources")
        self.assertEqual({path.name for path in self.state.iterdir()}, {"server.lock"})
        with Runtime(self.state, crm_reader=InventedReader()):
            self.assertEqual(json.loads(self.marker.read_text()), IDENTITY)

    def test_second_marker_failure_preserves_connection_and_retries_before_source_initialization(self):
        binding = self.binding()
        original_replace = os.replace

        def fail_binding(source, destination):
            if Path(destination).name == "source-profile.json":
                raise OSError("invented second-marker failure")
            original_replace(source, destination)

        with patch("gtm_connector.runtime.os.replace", side_effect=fail_binding):
            with self.assertRaisesRegex(SourceIdentityError, "could not be saved"):
                with Runtime(self.state, crm_reader=InventedReader(), demo_binding=binding):
                    self.fail("Partial marker creation initialized sources")
        self.assertEqual({path.name for path in self.state.iterdir()}, {"server.lock", "crm-connection.json"})
        changed = InventedReader({**IDENTITY, "organization_id": "00D000000000002AAA"})
        with self.assertRaises(SourceIdentityError):
            with Runtime(self.state, crm_reader=changed, demo_binding=binding):
                self.fail("A partial pin was repointed")
        self.assertFalse((self.state / "source-profile.json").exists())
        with Runtime(self.state, crm_reader=InventedReader(), demo_binding=binding):
            self.assertTrue((self.state / "source-profile.json").is_file())
            with closing(sqlite3.connect(self.state / "calls.sqlite3")) as connection:
                self.assertEqual(connection.execute("SELECT COUNT(*) FROM call_notes").fetchone()[0], 3)

    def test_unusable_identity_and_bad_binding_never_publish_a_connection_pin(self):
        for changed in ({}, {**IDENTITY, "schema_version": True}, {**IDENTITY, "organization_id": "missing"}):
            with self.subTest(identity=changed), self.assertRaises(SourceIdentityError):
                with Runtime(self.state, crm_reader=InventedReader(changed)):
                    self.fail("An unusable identity initialized source data")
            self.assertEqual({path.name for path in self.state.iterdir()}, {"server.lock"})
        marker = self.state / "source-profile.json"
        marker.write_text("invalid binding")
        marker.chmod(0o600)
        reader = InventedReader()
        with self.assertRaisesRegex(ValueError, "source profile"):
            with Runtime(self.state, crm_reader=reader, demo_binding=self.binding()):
                self.fail("Invalid binding created a connection pin")
        self.assertFalse(self.marker.exists())
        self.assertEqual(reader.prepared, 0)


if __name__ == "__main__":
    unittest.main()
