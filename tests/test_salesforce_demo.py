"""Invented tree imports bind local fixtures without invoking Salesforce."""

from contextlib import closing
from copy import deepcopy
import hashlib
import json
import os
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector import calls, slack
from gtm_connector.salesforce_demo import (
    BoundDemo, DemoBindingError, bind_import, kit_document, kit_files,
    load_binding, load_manifest, load_tree, write_binding,
)


def invented_receipt(offset=0):
    return {"status": 0, "result": [
        {"refId": row["ref_id"], "type": "Account",
         "id": row["id_prefix"] + f"{offset + number:012d}" + "AAA"}
        for number, row in enumerate(load_manifest()["records"], 1)
    ], "warnings": []}


class DemoKitTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name).resolve()
        self.document = bind_import(invented_receipt())
        self.bound = BoundDemo(self.document)

    def test_packaged_tree_is_one_plan_twelve_standard_records_and_no_enddate_write(self):
        assets = kit_files()
        self.assertEqual(set(assets), {"manifest.json", "accounts.json", "plan.json"})
        self.assertEqual(kit_document("plan"), [{"sobject": "Account", "files": ["accounts.json"]}])
        manifest, tree = load_manifest(), load_tree()
        self.assertEqual(manifest["files_sha256"],
                         {name: hashlib.sha256(assets[name]).hexdigest() for name in ("plan.json", "accounts.json")})
        self.assertEqual(len(manifest["records"]), 12)
        self.assertEqual(len(tree["records"]), 3)
        self.assertEqual({row["object"] for row in manifest["records"]},
                         {"Account", "Contact", "Opportunity", "Contract"})
        refs = set()
        for root in tree["records"]:
            rows = [root] + [root[name]["records"][0] for name in ("Contacts", "Opportunities", "Contracts")]
            for row in rows:
                self.assertNotIn("Id", row)
                self.assertNotIn("AccountId", row)
                refs.add(row["attributes"]["referenceId"])
            contract = root["Contracts"]["records"][0]
            self.assertEqual(contract["Status"], "Draft")
            self.assertNotIn("EndDate", contract)
            self.assertTrue(root["Contacts"]["records"][0]["Email"].endswith(".example"))
        self.assertEqual(refs, {row["ref_id"] for row in manifest["records"]})
        self.assertEqual(tree["records"][0]["Contracts"]["records"][0]["StartDate"], "2026-02-01")
        self.assertEqual(tree["records"][0]["Contracts"]["records"][0]["ContractTerm"], 12)
        self.assertNotIn("Industry", tree["records"][2])
        self.assertNotIn("Title", tree["records"][2]["Contacts"]["records"][0])
        self.assertNotIn("Amount", tree["records"][2]["Opportunities"]["records"][0])
        self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", manifest["notes"][2]["body"])
        self.assertEqual(manifest["notes"][1]["called_at"], "2026-10-03T15:00:00Z")
        self.assertIn("not approved", manifest["notes"][0]["body"])
        self.assertIn("Security review is delayed", manifest["messages"][0]["body"])
        self.assertEqual(tree["records"][0]["Opportunities"]["records"][0]["StageName"], "Closed Won")

    def test_asset_documents_and_bound_properties_are_independent_copies(self):
        manifest = load_manifest()
        manifest["notes"][0]["body"] = "changed"
        assets = kit_files()
        assets["manifest.json"] = b"changed"
        self.assertNotEqual(load_manifest()["notes"][0]["body"], "changed")
        accounts, records = self.bound.accounts, self.bound.records
        accounts.clear()
        records[0]["id"] = "changed"
        self.assertEqual(len(self.bound.accounts), 3)
        self.assertNotEqual(self.bound.records[0]["id"], "changed")
        for name in ("../manifest", "manifest.json", "unknown", None):
            with self.assertRaises(DemoBindingError):
                kit_document(name)

    def test_receipt_root_type_quirk_uses_reference_and_native_id_prefix(self):
        document = bind_import(invented_receipt())
        self.assertEqual(len(document["records"]), 12)
        self.assertEqual(len(document["accounts"]), 3)
        self.assertEqual({row["object"] for row in document["records"]},
                         {"Account", "Contact", "Opportunity", "Contract"})
        self.assertEqual(self.bound.as_of, "2027-01-15")
        self.assertEqual(len(self.bound.manifest_sha256), 64)
        actual_types = invented_receipt()
        for row, declaration in zip(actual_types["result"], load_manifest()["records"]):
            row["type"] = declaration["object"]
        self.assertEqual(bind_import(actual_types), document)
        shuffled = invented_receipt()
        shuffled["result"].reverse()
        self.assertEqual(bind_import(shuffled), document)

    def test_failed_partial_extra_duplicate_and_wrong_prefix_receipts_are_rejected(self):
        cases = []
        for status in (1, True, "0", None):
            value = invented_receipt(); value["status"] = status; cases.append(value)
        value = invented_receipt(); value["result"].pop(); cases.append(value)
        value = invented_receipt(); value["result"].append(deepcopy(value["result"][0])); cases.append(value)
        for field, bad in (("refId", "UnknownRef"), ("id", "short"), ("id", "003000000000001AAA"),
                           ("type", "User"), ("refId", [])):
            value = invented_receipt(); value["result"][0][field] = bad; cases.append(value)
        value = invented_receipt(); value["result"][4] = deepcopy(value["result"][0]); cases.append(value)
        value = invented_receipt(); value["result"][4]["id"] = value["result"][0]["id"]; cases.append(value)
        value = invented_receipt(); value["result"][0]["success"] = False; cases.append(value)
        for value in cases:
            with self.subTest(value=value), self.assertRaises(DemoBindingError):
                bind_import(value)

    def test_binding_metadata_and_mapping_must_match_complete_import(self):
        cases = []
        for field, bad in (("schema_version", True), ("kit_id", "another-kit"),
                           ("manifest_sha256", "0" * 64), ("accounts", {})):
            value = deepcopy(self.document); value[field] = bad; cases.append(value)
        value = deepcopy(self.document); value["records"][0]["object"] = "Contract"; cases.append(value)
        value = deepcopy(self.document); value["records"][0]["account_number"] = "OTHER"; cases.append(value)
        value = deepcopy(self.document); value["records"].pop(); cases.append(value)
        value = deepcopy(self.document); value["extra"] = "secret"; cases.append(value)
        for value in cases:
            with self.subTest(value=value), self.assertRaises(DemoBindingError):
                BoundDemo(value)
        reordered = deepcopy(self.document); reordered["records"].reverse()
        self.assertEqual(BoundDemo(reordered).binding_sha256, self.bound.binding_sha256)
        self.assertNotEqual(BoundDemo(bind_import(invented_receipt(100))).binding_sha256, self.bound.binding_sha256)

    def test_private_binding_round_trip_and_failed_replace_preserve_previous_file(self):
        path = self.directory / "binding.json"
        write_binding(path, self.document)
        before = path.read_bytes()
        self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
        self.assertEqual(load_binding(path).binding_sha256, self.bound.binding_sha256)
        with patch("gtm_connector.salesforce_demo.os.replace", side_effect=OSError("private detail")):
            with self.assertRaises(DemoBindingError) as caught:
                write_binding(path, bind_import(invented_receipt(100)))
        self.assertNotIn("private detail", str(caught.exception))
        self.assertEqual(path.read_bytes(), before)
        self.assertEqual([p.name for p in self.directory.iterdir()], ["binding.json"])

    def test_nonprivate_symlink_hardlink_directory_and_oversized_bindings_are_rejected(self):
        path = self.directory / "binding.json"
        write_binding(path, self.document)
        before = path.read_bytes()
        path.chmod(0o644)
        with self.assertRaises(DemoBindingError): load_binding(path)
        with self.assertRaises(DemoBindingError): write_binding(path, self.document)
        path.chmod(0o600)
        linked = self.directory / "linked.json"; linked.symlink_to(path)
        with self.assertRaises(DemoBindingError): load_binding(linked)
        with self.assertRaises(DemoBindingError): write_binding(linked, self.document)
        self.assertEqual(path.read_bytes(), before)
        hard = self.directory / "hard.json"; os.link(path, hard)
        with self.assertRaises(DemoBindingError): load_binding(path)
        hard.unlink()
        with self.assertRaises(DemoBindingError): load_binding(self.directory)
        large = self.directory / "large.json"; large.write_bytes(b" " * (128 * 1024 + 1)); large.chmod(0o600)
        with self.assertRaises(DemoBindingError): load_binding(large)
        public = self.directory / "public"; public.mkdir(mode=0o755)
        with self.assertRaises(DemoBindingError): write_binding(public / "binding.json", self.document)
        with patch("gtm_connector.salesforce_demo.os.getuid", return_value=os.getuid() + 1):
            with self.assertRaises(DemoBindingError): load_binding(path)

    def test_duplicate_keys_and_failed_json_do_not_leak_input(self):
        path = self.directory / "binding.json"
        for raw in (b'{"secret":"invented-private-detail",', b'{"schema_version":1,"schema_version":1}',
                    b'{"schema_version":NaN}', b'\xff'):
            path.write_bytes(raw); path.chmod(0o600)
            with self.assertRaises(DemoBindingError) as caught:
                load_binding(path)
            self.assertNotIn("invented-private-detail", str(caught.exception))
            self.assertNotIn(str(path), str(caught.exception))

    def test_seeded_sources_use_binding_ids_and_preserve_edits(self):
        for module, table, seed in ((calls, "call_notes", self.bound.seed_calls),
                                    (slack, "messages", self.bound.seed_slack)):
            with self.subTest(table=table), closing(module.connect(":memory:")) as connection:
                module.initialize(connection)
                seed(connection)
                rows = [dict(row) for row in connection.execute(f"SELECT * FROM {table} ORDER BY id")]
                self.assertEqual(len(rows), 3)
                self.assertEqual({row["account_id"] for row in rows}, set(self.bound.accounts.values()))
                connection.execute(f"UPDATE {table} SET body = 'Owner edit' WHERE id = ?", (rows[0]["id"],))
                seed(connection)
                self.assertEqual(connection.execute(f"SELECT body FROM {table} WHERE id = ?",
                                                    (rows[0]["id"],)).fetchone()[0], "Owner edit")

    def test_partial_wrong_association_and_default_source_reuse_refuse_without_mutation(self):
        for module, table, seed in ((calls, "call_notes", self.bound.seed_calls),
                                    (slack, "messages", self.bound.seed_slack)):
            for mutation in ("partial", "association", "default"):
                with self.subTest(table=table, mutation=mutation), closing(module.connect(":memory:")) as connection:
                    module.initialize(connection)
                    if mutation == "default":
                        module.seed(connection)
                    else:
                        seed(connection)
                        if mutation == "partial": connection.execute(f"DELETE FROM {table} WHERE id LIKE '%003'")
                        else: connection.execute(f"UPDATE {table} SET account_id = 'acc-sf-001999999999999AAA'")
                    before = [tuple(row) for row in connection.execute(f"SELECT * FROM {table}")]
                    with self.assertRaises(DemoBindingError): seed(connection)
                    self.assertEqual([tuple(row) for row in connection.execute(f"SELECT * FROM {table}")], before)

    def test_seeding_keeps_outer_transaction_and_rolls_back_partial_insert(self):
        with closing(calls.connect(":memory:")) as connection:
            calls.initialize(connection)
            connection.execute("BEGIN")
            self.bound.seed_calls(connection)
            self.assertTrue(connection.in_transaction)
            connection.rollback()
            self.assertEqual(calls.counts(connection), 0)
            connection.execute("CREATE TRIGGER fail_second BEFORE INSERT ON call_notes "
                               "WHEN NEW.id = 'note-demo-002' BEGIN SELECT RAISE(ABORT, 'test failure'); END")
            with self.assertRaises(sqlite3.IntegrityError): self.bound.seed_calls(connection)
            self.assertEqual(calls.counts(connection), 0)

    def test_account_check_uses_both_binding_directions_without_any_lookup(self):
        number, identifier = next(iter(self.bound.accounts.items()))
        self.bound.validate_account({"id": identifier, "account_number": number})
        self.bound.validate_account({"id": "acc-sf-001999999999999AAA", "account_number": "UNRELATED"})
        self.bound.validate_account({"id": "acc-sf-001999999999999AAA", "account_number": None})
        for value in ({"id": identifier, "account_number": None},
                      {"id": identifier, "account_number": "WRONG"},
                      {"id": "acc-sf-001999999999999AAA", "account_number": number},
                      {"id": [], "account_number": number}):
            with self.assertRaises(DemoBindingError) as caught: self.bound.validate_account(value)
            self.assertNotIn(identifier, str(caught.exception))


if __name__ == "__main__":
    unittest.main()
