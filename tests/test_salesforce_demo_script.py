"""Owner cleanup tests use invented imports and a fake sf executable only."""

from contextlib import redirect_stderr, redirect_stdout
from copy import deepcopy
import importlib.util
import io
import json
import os
from pathlib import Path
import re
import sys
import tempfile
import unittest
from unittest.mock import patch

from gtm_connector.salesforce_demo import BoundDemo, bind_import, load_manifest, load_tree


ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("salesforce_demo_script", ROOT / "scripts/salesforce-demo.py")
demo_script = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(demo_script)


def result(rows):
    return {"done": True, "totalSize": len(rows), "records": deepcopy(rows)}


def imported_binding():
    return BoundDemo(bind_import({"status": 0, "result": [
        {"refId": row["ref_id"], "type": row["object"],
         "id": row["id_prefix"] + f"{number:012d}" + "AAA"}
        for number, row in enumerate(load_manifest()["records"], 1)
    ]}))


class FakeSF:
    """A bounded invented org. Every request is recorded before its response."""

    def __init__(self, binding):
        self.calls = self.writes_attempted = self.deleted = 0
        self.events = []
        self.edition = "Developer Edition"
        self.inventory = {name: {} for name in ("Account", "Contact", "Opportunity", "Contract")}
        by_ref = {row["ref_id"]: row for row in binding.records}
        for account in load_tree()["records"]:
            parent = by_ref[account["attributes"]["referenceId"]]["id"]
            for relation in (None, "Contacts", "Opportunities", "Contracts"):
                records = [account] if relation is None else account[relation]["records"]
                for source in records:
                    bound = by_ref[source["attributes"]["referenceId"]]
                    row = {key: value for key, value in source.items() if not isinstance(value, dict)}
                    row["Id"] = bound["id"]
                    if relation is not None:
                        row["AccountId"] = parent
                    if bound["object"] == "Contract":
                        row["StatusCode"] = "Draft"
                    # Salesforce returns explicitly queried nullable fields, even
                    # when the tree import omitted them.
                    if bound["object"] == "Account": row.setdefault("Industry", None)
                    if bound["object"] == "Contact": row.setdefault("Title", None)
                    if bound["object"] == "Opportunity": row.setdefault("Amount", None)
                    self.inventory[bound["object"]][bound["id"]] = row
        self.metadata = {name: {"childRelationships": []} for name in self.inventory}
        for child, relation in (("Contact", "Contacts"), ("Opportunity", "Opportunities"),
                                ("Contract", "Contracts")):
            self.add_relation("Account", child, "AccountId", relation)
        self.extra_children = {}
        self.direct_children = {}
        self.transform = lambda query, response: response
        self.describe_error = None
        self.retain_after_delete = set()
        self.fail_delete_at = None

    def add_relation(self, parent, child, field, relation, *, cascade=True, restricted=False):
        self.metadata[parent]["childRelationships"].append({
            "childSObject": child, "field": field, "relationshipName": relation,
            "cascadeDelete": cascade, "restrictedDelete": restricted,
        })

    def query(self, query):
        self.calls += 1
        self.events.append(("query", query))
        if query == "SELECT OrganizationType FROM Organization LIMIT 1":
            response = result([{"OrganizationType": self.edition}])
        else:
            match = re.search(r" FROM (\w+) WHERE (\w+) IN \(([^)]*)\) LIMIT (\d+)$", query)
            if match is None:
                raise AssertionError(f"The cleanup made an unbounded or unsupported query: {query}")
            name, field, raw_ids, _ = match.groups()
            identifiers = set(re.findall(r"'([A-Za-z0-9]{18})'", raw_ids))
            if field != "Id":
                response = result([row for row in self.direct_children.get((name, field), [])
                                   if row.get(field) in identifiers])
            else:
                parents = [deepcopy(row) for identifier, row in sorted(self.inventory[name].items())
                           if identifier in identifiers]
                if "(SELECT " in query:
                    relations = re.findall(r"\(SELECT Id(?:,RowCause)? FROM (\w+) LIMIT 101\)", query)
                    if not relations:
                        raise AssertionError("Child reads must fetch enough rows to detect the bound.")
                    for parent in parents:
                        for relation in relations:
                            metadata = next(row for row in self.metadata[name]["childRelationships"]
                                            if row["relationshipName"] == relation)
                            children = [row for row in self.inventory.get(metadata["childSObject"], {}).values()
                                        if row.get(metadata["field"]) == parent["Id"]]
                            children += self.extra_children.get((parent["Id"], relation), [])
                            parent[relation] = result(children)
                response = result(parents)
        return self.transform(query, response)

    def describe(self, name):
        self.calls += 1
        self.events.append(("describe", name))
        if self.describe_error:
            raise self.describe_error
        return deepcopy(self.metadata[name])

    def delete(self, name, identifier):
        self.calls += 1
        self.writes_attempted += 1
        self.events.append(("delete", name, identifier))
        if self.writes_attempted == self.fail_delete_at:
            raise demo_script.KitError("The invented delete failed.")
        if identifier not in self.retain_after_delete:
            del self.inventory[name][identifier]
        self.deleted += 1


class DemoCleanupTests(unittest.TestCase):
    def setUp(self):
        self.binding = imported_binding()
        self.sf = FakeSF(self.binding)

    def identifier(self, name):
        return next(iter(self.sf.inventory[name]))

    def assert_refuses_without_delete(self, message=None):
        with self.assertRaises(demo_script.KitError) as caught:
            demo_script.remove(self.binding, self.sf)
        if message:
            self.assertIn(message, str(caught.exception))
        self.assertEqual(self.sf.writes_attempted, 0)
        self.assertFalse(any(event[0] == "delete" for event in self.sf.events))

    def test_exact_imported_ids_are_deleted_child_first_after_all_preflights(self):
        report = demo_script.remove(self.binding, self.sf)
        deletes = [event for event in self.sf.events if event[0] == "delete"]
        expected = {(row["object"], row["id"]) for row in self.binding.records}
        self.assertEqual({event[1:] for event in deletes}, expected)
        self.assertEqual(len(deletes), 12)
        self.assertTrue(all(event[1] != "Account" for event in deletes[:9]))
        self.assertEqual([event[1] for event in deletes[9:]], ["Account"] * 3)
        first_delete = next(index for index, event in enumerate(self.sf.events) if event[0] == "delete")
        inspected = {event[1] for event in self.sf.events[:first_delete] if event[0] == "describe"}
        self.assertEqual(inspected, {"Account", "Contact", "Opportunity", "Contract"})
        self.assertTrue(all(not rows for rows in self.sf.inventory.values()))
        self.assertEqual((report["deleted"], report["already_absent"], report["remaining"]), (12, 0, 0))
        verified = self.sf.events[-4:]
        self.assertTrue(all(event[0] == "query" and event[1].startswith("SELECT Id FROM ")
                            for event in verified))
        self.assertEqual({re.search(r" FROM (\w+) ", event[1])[1] for event in verified}, inspected)

    def test_unexpected_children_of_every_target_kind_refuse_before_any_delete(self):
        for parent, child, relation, field in (
            ("Account", "Opportunity", "ExtraOpportunities", "AccountId"),
            ("Contact", "Task", "Tasks", "WhoId"),
            ("Opportunity", "OpportunityLineItem", "OpportunityLineItems", "OpportunityId"),
            ("Contract", "Attachment", "Attachments", "ParentId"),
        ):
            with self.subTest(parent=parent):
                self.sf = FakeSF(self.binding)
                self.sf.add_relation(parent, child, field, relation)
                self.sf.extra_children[(self.identifier(parent), relation)] = [{"Id": "00T999999999999AAA"}]
                self.assert_refuses_without_delete("unexpected related record")

    def test_intentionally_blank_fields_must_remain_null_and_present(self):
        for name, field, value in (("Account", "Industry", "New industry"),
                                   ("Contact", "Title", "New title"), ("Opportunity", "Amount", 500)):
            for missing in (False, True):
                with self.subTest(object=name, field=field, missing=missing):
                    self.sf = FakeSF(self.binding)
                    identifier = next(row["id"] for row in self.binding.records
                                      if row["ref_id"] == name + "Ref3")
                    row = self.sf.inventory[name][identifier]
                    self.assertIsNone(row[field])
                    if missing:
                        del row[field]
                    else:
                        row[field] = value
                    self.assert_refuses_without_delete("kit field changed")

    def test_cascade_child_missing_from_direct_inventory_cannot_be_deleted(self):
        hidden = self.identifier("Contact")
        self.sf.inventory["Contact"][hidden]["Title"] = "Edited but hidden from the direct read"
        def hide_direct_contact(query, response):
            if "(SELECT " not in query and " FROM Contact WHERE Id IN " in query:
                return result([row for row in response["records"] if row["Id"] != hidden])
            return response
        self.sf.transform = hide_direct_contact
        self.assert_refuses_without_delete("unexpected related record")
        self.assertIn(hidden, self.sf.inventory["Contact"])

    def test_manual_share_after_owner_share_is_not_hidden(self):
        for parent in ("Account", "Contact", "Opportunity"):
            with self.subTest(parent=parent):
                self.sf = FakeSF(self.binding)
                self.sf.add_relation(parent, parent + "Share", parent + "Id", "Shares")
                self.sf.extra_children[(self.identifier(parent), "Shares")] = [
                    {"Id": "00r000000000001AAA", "RowCause": "Owner"},
                    {"Id": "00r000000000002AAA", "RowCause": "Manual"},
                ]
                self.assert_refuses_without_delete("unexpected related record")
                self.assertTrue(any("SELECT Id,RowCause FROM Shares LIMIT 101" in event[1]
                                    for event in self.sf.events if event[0] == "query"))

    def test_only_known_history_objects_and_owner_shares_are_allowed(self):
        for parent, histories in (("Account", ["AccountHistory"]), ("Contact", ["ContactHistory"]),
                                  ("Opportunity", ["OpportunityHistory", "OpportunityFieldHistory"]),
                                  ("Contract", ["ContractHistory"])):
            for index, history in enumerate(histories):
                self.sf.add_relation(parent, history, "ParentId", f"History{index}")
            if parent != "Contract":
                self.sf.add_relation(parent, parent + "Share", parent + "Id", "Shares")
                self.sf.extra_children[(self.identifier(parent), "Shares")] = [
                    {"Id": "00r000000000001AAA", "RowCause": "Owner"}]
        self.assertEqual(demo_script.remove(self.binding, self.sf)["deleted"], 12)
        for child in ("CustomHistory__c", "FeedItem"):
            with self.subTest(child=child):
                self.sf = FakeSF(self.binding)
                self.sf.add_relation("Account", child, "ParentId", "OtherChildren")
                self.sf.extra_children[(self.identifier("Account"), "OtherChildren")] = [
                    {"Id": "00T999999999999AAA", "Type": "TrackedChange"}]
                self.assert_refuses_without_delete("unexpected related record")

    def test_unnamed_relationship_uses_exact_parent_ids_and_checks_restricted_deletes(self):
        self.sf.add_relation("Contact", "Task", "WhoId", None, cascade=False, restricted=True)
        self.sf.direct_children[("Task", "WhoId")] = [
            {"Id": "00T999999999999AAA", "WhoId": self.identifier("Contact")}]
        self.assert_refuses_without_delete("unexpected related record")
        query = self.sf.events[-1][1]
        self.assertIn("FROM Task WHERE WhoId IN (", query)
        self.assertEqual(set(re.findall(r"'([^']+)'", query)), set(self.sf.inventory["Contact"]))
        self.assertTrue(query.endswith("LIMIT 101"))

    def test_incomplete_or_malformed_inventory_refuses(self):
        for corruption in ("unfinished", "count", "record", "duplicate", "wrong_id"):
            with self.subTest(corruption=corruption):
                self.sf = FakeSF(self.binding)
                def corrupt(query, response):
                    if " FROM Contact WHERE " not in query:
                        return response
                    if corruption == "unfinished": response["done"] = False
                    if corruption == "count": response["totalSize"] += 1
                    if corruption == "record": response["records"][0] = "not a record"
                    if corruption == "duplicate": response["records"][1] = response["records"][0]
                    if corruption == "wrong_id": response["records"][0]["Id"] = "003999999999999AAA"
                    return response
                self.sf.transform = corrupt
                self.assert_refuses_without_delete()

    def test_incomplete_missing_invalid_or_excess_child_results_refuse(self):
        for corruption in ("unfinished", "count", "missing", "invalid_id", "excess", "missing_parent"):
            with self.subTest(corruption=corruption):
                self.sf = FakeSF(self.binding)
                def corrupt(query, response):
                    if "(SELECT " not in query:
                        return response
                    parent = response["records"][0]
                    child_result = parent["Contacts"]
                    if corruption == "unfinished": child_result["done"] = False
                    if corruption == "count": child_result["totalSize"] += 1
                    if corruption == "missing": del parent["Contacts"]
                    if corruption == "invalid_id": child_result["records"][0]["Id"] = "invalid"
                    if corruption == "excess": parent["Contacts"] = result(child_result["records"] * 101)
                    if corruption == "missing_parent":
                        response["records"].pop(); response["totalSize"] -= 1
                    return response
                self.sf.transform = corrupt
                self.assert_refuses_without_delete()

    def test_unusable_metadata_refuses_before_deletes(self):
        relation = deepcopy(self.sf.metadata["Account"]["childRelationships"][0])
        malformed = [None, {}, {"childRelationships": None}, {"childRelationships": [None]},
                     {"childRelationships": [relation] * 251}]
        for field, value in (("cascadeDelete", 1), ("restrictedDelete", None),
                             ("childSObject", "Contact WHERE"), ("field", "AccountId)"),
                             ("relationshipName", "Contacts LIMIT 1")):
            bad = deepcopy(relation); bad[field] = value
            malformed.append({"childRelationships": [bad]})
        for metadata in malformed:
            with self.subTest(metadata=metadata):
                self.sf = FakeSF(self.binding)
                self.sf.metadata["Contact"] = metadata
                self.assert_refuses_without_delete()
        self.sf = FakeSF(self.binding)
        self.sf.describe_error = demo_script.KitError("The metadata read failed.")
        self.assert_refuses_without_delete("metadata read failed")

    def test_edited_and_activated_imports_refuse(self):
        for name, field, value in (("Account", "Name", "Invented edited name"),
                                    ("Opportunity", "StageName", "Prospecting"),
                                    ("Contract", "Status", "Activated"),
                                    ("Contract", "StatusCode", "Activated"),
                                    ("Contact", "AccountId", "001999999999999AAA")):
            with self.subTest(name=name, field=field):
                self.sf = FakeSF(self.binding)
                self.sf.inventory[name][self.identifier(name)][field] = value
                self.assert_refuses_without_delete("kit field changed")

    def test_retry_skips_already_absent_records_and_verifies_all_ids(self):
        self.sf.fail_delete_at = 5
        with self.assertRaises(demo_script.KitError):
            demo_script.remove(self.binding, self.sf)
        self.assertEqual((self.sf.writes_attempted, self.sf.deleted), (5, 4))
        previously_deleted = {event[2] for event in self.sf.events if event[0] == "delete"} - {
            self.sf.events[-1][2]}
        self.sf.events.clear()
        self.sf.calls = self.sf.writes_attempted = self.sf.deleted = 0
        self.sf.fail_delete_at = None
        report = demo_script.remove(self.binding, self.sf)
        self.assertEqual((report["deleted"], report["already_absent"]), (8, 4))
        retried = {event[2] for event in self.sf.events if event[0] == "delete"}
        self.assertFalse(retried & previously_deleted)
        self.sf.events.clear()
        self.sf.calls = self.sf.writes_attempted = self.sf.deleted = 0
        report = demo_script.remove(self.binding, self.sf)
        self.assertEqual((report["deleted"], report["already_absent"]), (0, 12))
        self.assertFalse(any(event[0] in ("delete", "describe") for event in self.sf.events))

    def test_wrong_edition_stops_before_reading_or_deleting_records(self):
        self.sf.edition = "Enterprise Edition"
        self.assert_refuses_without_delete("Developer Edition")
        self.assertEqual(self.sf.events, [("query", "SELECT OrganizationType FROM Organization LIMIT 1")])

    def test_report_requires_postdelete_absence(self):
        self.sf.retain_after_delete.add(self.identifier("Account"))
        with self.assertRaisesRegex(demo_script.KitError, "verify.*absent"):
            demo_script.remove(self.binding, self.sf)
        self.assertEqual(self.sf.deleted, 12)
        self.assertTrue(self.sf.inventory["Account"])


class FakeExecutableTests(unittest.TestCase):
    def test_sf_uses_literal_arguments_closed_input_disabled_side_effects_and_private_errors(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            log = path / "invocations.jsonl"
            executable = path / "sf"
            executable.write_text("#!" + sys.executable + "\n" + '''
import json, os, sys
arguments = sys.argv[1:]
flags = ["SF_DISABLE_TELEMETRY", "SF_AUTOUPDATE_DISABLE", "SF_DISABLE_AUTOUPDATE",
         "SF_DISABLE_LOG_FILE", "SFDX_DISABLE_LOG_FILE"]
with open(os.environ["FAKE_SF_LOG"], "a") as stream:
    stream.write(json.dumps({"args": arguments, "stdin": sys.stdin.read(),
                             "flags": {name: os.environ.get(name) for name in flags}}) + "\\n")
mode = os.environ.get("FAKE_SF_MODE", "ok")
if mode == "fail":
    print("invented-private-output")
    print("invented-private-error", file=sys.stderr)
    sys.exit(1)
if mode == "invalid":
    print(json.dumps({"status": False, "result": "invented-private-output"}))
    sys.exit(0)
if arguments[:3] == ["data", "delete", "record"]:
    identifier = arguments[arguments.index("--record-id") + 1]
    result = {"success": True, "id": identifier if mode != "wrong_id" else "001999999999999AAA"}
elif arguments[:2] == ["sobject", "describe"]:
    result = {"childRelationships": []}
else:
    result = {"done": True, "totalSize": 0, "records": []}
print(json.dumps({"status": 0, "result": result}))
''')
            executable.chmod(0o700)
            environment = {"PATH": str(path), "FAKE_SF_LOG": str(log), "FAKE_SF_MODE": "ok",
                           "SF_DISABLE_TELEMETRY": "false", "SF_DISABLE_LOG_FILE": "false"}
            stdout, stderr = io.StringIO(), io.StringIO()
            with patch.dict(os.environ, environment), redirect_stdout(stdout), redirect_stderr(stderr):
                sf = demo_script.SalesforceCLI("invented-demo")
                query = "SELECT Id FROM Account WHERE Name = '$(invented-not-a-command)'"
                self.assertEqual(sf.query(query), result([]))
                self.assertEqual(sf.describe("Account"), {"childRelationships": []})
                sf.delete("Account", "001000000000001AAA")
                for mode in ("fail", "invalid", "wrong_id"):
                    with self.subTest(mode=mode), patch.dict(os.environ, {"FAKE_SF_MODE": mode}):
                        with self.assertRaises(demo_script.KitError) as caught:
                            sf.delete("Account", "001000000000001AAA")
                        self.assertNotIn("invented-private", str(caught.exception))
                        self.assertNotIn("001", str(caught.exception))
                self.assertEqual((sf.calls, sf.writes_attempted, sf.deleted), (6, 4, 1))
            invocations = [json.loads(line) for line in log.read_text().splitlines()]
            suffix = ["--target-org", "invented-demo", "--json"]
            self.assertEqual(invocations[0]["args"], ["data", "query", "--query", query, *suffix])
            self.assertEqual(invocations[1]["args"], ["sobject", "describe", "--sobject", "Account", *suffix])
            self.assertEqual(invocations[2]["args"], ["data", "delete", "record", "--sobject", "Account",
                                                    "--record-id", "001000000000001AAA", *suffix])
            for invocation in invocations:
                self.assertEqual(invocation["stdin"], "")
                self.assertEqual(set(invocation["flags"].values()), {"true"})
            self.assertEqual((stdout.getvalue(), stderr.getvalue()), ("", ""))


if __name__ == "__main__":
    unittest.main()
