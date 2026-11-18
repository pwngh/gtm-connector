"""Invented selections prove private tuple uniqueness and immutable mapping revisions."""

from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import uuid

from gtm_connector import slack_mapping as mapping


CRM = {"schema_version": 1, "crm_backend": "sqlite", "identity_sha256": "c" * 64}
IDENTITY = {"app_id": "A000001", "team_id": "T000001", "user_id": "U000001", "enterprise_id": None}
SLACK = mapping.slack_identity_binding(IDENTITY)
WHEN = "2026-11-18T10:00:00Z"


class SlackMappingTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)
        self.path = self.directory / "slack-mappings.json"
        mapping.initialize_slack_mapping_store(self.path, crm_binding=CRM, slack_binding=SLACK)

    def save(self, account="acc-001", channel="CONE", ts="1700000000.000001", **options):
        return mapping.save_slack_mapping(self.path,
            association={"account_id": account, "crm_binding": CRM, "resolution_receipt_id": str(uuid.uuid4()),
                         "method": "account_number", "account_number": "PRIVATE-ACCOUNT-NUMBER"},
            slack_binding=SLACK, selection={"channel_id": channel, "thread_ts": ts}, selected_at=WHEN, **options)

    def load(self, account="acc-001"):
        return mapping.load_slack_mapping(self.path, account_id=account, crm_binding=CRM, slack_binding=SLACK)

    def assert_code(self, code, function, *args, **kwargs):
        with self.assertRaises(mapping.SlackMappingError) as caught:
            function(*args, **kwargs)
        self.assertEqual(caught.exception.code, code)

    def test_exact_mapping_projection_hash_and_detached_idempotency(self):
        first = self.save()
        before = self.path.read_bytes()
        second = self.save()
        self.assertTrue(first["changed"])
        self.assertFalse(second["changed"])
        self.assertEqual(self.path.read_bytes(), before)
        entry = first["mapping"]
        manifest = {key: value for key, value in entry.items() if key != "mapping_id"}
        serialized = json.dumps(manifest, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()
        self.assertEqual(entry["mapping_id"], "map-" + hashlib.sha256(serialized).hexdigest())
        for private in ("PRIVATE-ACCOUNT-NUMBER", "account_number", "method", "workspace_origin", "connection_dir"):
            self.assertNotIn(private, before.decode())
            self.assertNotIn(private, serialized.decode())
        second["mapping"]["crm_binding"]["identity_sha256"] = "a" * 64
        self.assertEqual(self.load(), entry)
        self.assertEqual(self.path.stat().st_mode & 0o777, 0o600)

    def test_replace_reassign_and_both_flags_are_distinct_atomic_choices(self):
        a = self.save()["mapping"]
        self.save("acc-002", "CTWO")
        before = self.path.read_bytes()
        self.assert_code("slack_mapping_conflict", self.save, "acc-001", "CTHREE")
        self.assert_code("slack_mapping_conflict", self.save, "acc-003", "CONE")
        self.assert_code("slack_mapping_conflict", self.save, "acc-002", "CONE", replace=True)
        self.assert_code("slack_mapping_conflict", self.save, "acc-002", "CONE", reassign=True)
        self.assertEqual(self.path.read_bytes(), before)
        moved = self.save("acc-002", "CONE", replace=True, reassign=True)["mapping"]
        self.assert_code("slack_mapping_missing", self.load, "acc-001")
        self.assertEqual(self.load("acc-002"), moved)
        back = self.save("acc-001", "CONE", reassign=True)["mapping"]
        self.assertNotEqual(back["mapping_id"], a["mapping_id"])
        self.assert_code("slack_mapping_missing", self.load, "acc-002")
        self.assertEqual(mapping.validate_slack_mapping(a, account_id="acc-001", crm_binding=CRM,
                                                        slack_binding=SLACK), a)

    def test_failed_replacement_keeps_the_complete_prior_document(self):
        self.save()
        before = self.path.read_bytes()
        with patch.object(mapping.os, "replace", side_effect=OSError("private invented path")):
            self.assert_code("slack_storage_failed", self.save, "acc-001", "CTWO", replace=True)
        self.assertEqual(self.path.read_bytes(), before)
        self.assertEqual({path.name for path in self.directory.iterdir()}, {self.path.name})

    def test_copied_links_and_native_selection_use_exact_parent_and_verified_host(self):
        expected = {"channel_id": "CONE", "thread_ts": "1700000000.000001"}
        self.assertEqual(mapping.parse_thread_selection(channel_id="CONE", thread_ts=expected["thread_ts"]), expected)
        origin = "https://invented.slack.com"
        self.assertEqual(mapping.parse_thread_selection(
            thread_url=origin + "/archives/CONE/p1700000000000001", workspace_origin=origin), expected)
        self.assertEqual(mapping.parse_thread_selection(thread_url=origin +
            "/archives/CONE/p1700000001000002?thread_ts=1700000000.000001&cid=CONE", workspace_origin=origin), expected)
        self.assertEqual(mapping.timestamp_microseconds("1700000000.000001"), 1700000000000001)
        invalid = ("https://wrong.slack.com/archives/CONE/p1700000000000001",
                   origin + "/archives/CONE/p1700000000000001?cid=CTWO",
                   origin + "/archives/CONE/p1700000000000001?cid=CONE&cid=CONE",
                   origin + "/archives/CONE/p1700000000000001?thread_ts=%QQ",
                   origin + "/archives/CONE%2fother/p1700000000000001",
                   "https://user@invented.slack.com/archives/CONE/p1700000000000001",
                   origin + "/archives/CONE/p1700000000000001#fragment")
        for link in invalid:
            self.assert_code("slack_selection_invalid", mapping.parse_thread_selection,
                             thread_url=link, workspace_origin=origin)
        self.assert_code("slack_selection_invalid", mapping.parse_thread_selection,
                         thread_url=origin + "/archives/CONE/p1700000000000001")
        self.assert_code("slack_selection_invalid", mapping.parse_thread_selection,
                         channel_id="DONE", thread_ts=expected["thread_ts"])
        for stamp in ("1e10.000001", "01700000000.000001", "1700000000.1", "253402300800.000000", 1.0):
            self.assert_code("slack_selection_invalid", mapping.validate_thread_timestamp, stamp)

    def test_bindings_are_stable_identity_only_and_never_silently_reassigned(self):
        self.save()
        changed = mapping.slack_identity_binding({**IDENTITY, "user_id": "U000002"})
        self.assertNotEqual(changed, SLACK)
        self.assert_code("slack_source_mismatch", mapping.load_slack_mapping, self.path,
                         account_id="acc-001", crm_binding=CRM, slack_binding=changed)
        self.assert_code("slack_source_mismatch", mapping.slack_identity_binding, {**IDENTITY, "token": "private"})
        self.assertNotIn(IDENTITY["team_id"], json.dumps(SLACK))

    def test_strict_manifest_rejects_isolated_tampering_and_duplicate_active_keys(self):
        entry = self.save()["mapping"]
        for field, value in (("channel_id", "CTWO"), ("thread_ts", "1700000000.000002"),
                             ("selected_at", "2026-11-19T10:00:00Z"), ("schema_version", True),
                             ("account_number", "PRIVATE"), ("resolution_receipt_id", str(uuid.uuid4()))):
            damaged = {**entry, field: value}
            self.assert_code("slack_mapping_invalid", mapping.validate_slack_mapping, damaged,
                             account_id="acc-001", crm_binding=CRM, slack_binding=SLACK)
        document = json.loads(self.path.read_text())
        document["mappings"].append(deepcopy(entry))
        self.path.write_text(json.dumps(document))
        self.assert_code("slack_mapping_invalid", self.load)

    def test_private_files_and_bounded_strict_json_are_required(self):
        raw = self.path.read_bytes()
        for damaged in (b'{}', b'{"schema_version":1,"schema_version":1}', b"x" * (mapping.MAX_MAPPING_BYTES + 1)):
            self.path.write_bytes(damaged)
            self.assert_code("slack_mapping_invalid", self.load)
        self.path.write_bytes(raw)
        self.path.chmod(0o644)
        self.assert_code("slack_mapping_invalid", self.load)
        self.path.chmod(0o600)
        other = self.directory / "other"
        os.link(self.path, other)
        self.assert_code("slack_mapping_invalid", self.load)
        other.unlink()
        self.path.rename(other)
        self.path.symlink_to(other)
        self.assert_code("slack_mapping_invalid", self.load)

    def test_one_hundred_mapping_bound_refuses_without_partial_publication(self):
        for number in range(1, 101):
            self.save(f"acc-{number:03}", f"C{number}")
        before = self.path.read_bytes()
        self.assertEqual(mapping.validate_slack_mapping_store(self.path, crm_binding=CRM, slack_binding=SLACK), 100)
        self.assert_code("slack_mapping_conflict", self.save, "acc-101", "C101")
        self.assertEqual(self.path.read_bytes(), before)


if __name__ == "__main__":
    unittest.main()
