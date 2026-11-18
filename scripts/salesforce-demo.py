#!/usr/bin/env python3
"""Owner-run demo import binding, private recording, and exact-record cleanup."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time


ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT))
OBJECTS = ("Contact", "Opportunity", "Contract", "Account")
IDENTIFIER = re.compile(r"[A-Za-z][A-Za-z0-9_]{0,100}\Z")
NATIVE_ID = re.compile(r"[A-Za-z0-9]{18}\Z")
HISTORY = {"AccountHistory", "ContactHistory", "OpportunityHistory", "OpportunityFieldHistory", "ContractHistory"}
SHARES = {"AccountShare", "ContactShare", "OpportunityShare"}


class KitError(RuntimeError):
    """A fixed diagnostic without child output or org records."""


def private_json(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size > 1024 * 1024:
        raise KitError("Use a regular import receipt smaller than 1 MiB.")
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise KitError("The import receipt contains duplicate JSON keys.")
            result[key] = value
        return result
    try:
        return json.loads(path.read_bytes(), object_pairs_hook=unique)
    except (ValueError, RecursionError):
        raise KitError("The import receipt is not valid JSON.") from None


class SalesforceCLI:
    def __init__(self, alias):
        from gtm_connector.sf_credentials import SFCredentials
        SFCredentials(alias)  # Validate the explicit target without launching a child.
        self.alias = alias
        self.calls = 0
        self.writes_attempted = 0
        self.deleted = 0
        self.deadline = time.monotonic() + 240

    def run(self, arguments):
        if self.calls >= 80 or time.monotonic() >= self.deadline:
            raise KitError("The cleanup reached its command or time limit. Keep the binding and retry.")
        environment = dict(os.environ, SF_DISABLE_TELEMETRY="true", SF_AUTOUPDATE_DISABLE="true",
                           SF_DISABLE_AUTOUPDATE="true", SF_DISABLE_LOG_FILE="true", SFDX_DISABLE_LOG_FILE="true")
        self.calls += 1
        try:
            result = subprocess.run(["sf", *arguments, "--target-org", self.alias, "--json"],
                                    stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                    env=environment, timeout=min(30, self.deadline - time.monotonic()), check=False)
            if result.returncode or len(result.stdout) > 2 * 1024 * 1024:
                raise KitError("The Salesforce command failed. Check the selected login and read access, then retry.")
            document = json.loads(result.stdout)
            if type(document.get("status")) is not int or document["status"] != 0:
                raise ValueError
            return document["result"]
        except (OSError, subprocess.SubprocessError, ValueError, KeyError, TypeError, AttributeError):
            raise KitError("The Salesforce command did not return a usable result. Check the selected login.") from None

    def query(self, query):
        return self.run(["data", "query", "--query", query])

    def describe(self, object_name):
        return self.run(["sobject", "describe", "--sobject", object_name])

    def delete(self, object_name, identifier):
        self.writes_attempted += 1
        result = self.run(["data", "delete", "record", "--sobject", object_name, "--record-id", identifier])
        if not isinstance(result, dict) or result.get("success") is not True or result.get("id") != identifier:
            raise KitError("A delete did not confirm success. Keep the binding and retry; missing records are skipped.")
        self.deleted += 1


def rows(document, *, limit=100):
    if (not isinstance(document, dict) or document.get("done") is not True
            or not isinstance(document.get("records"), list)
            or type(document.get("totalSize")) is not int
            or document["totalSize"] != len(document["records"]) or document["totalSize"] > limit
            or any(not isinstance(row, dict) for row in document["records"])):
        raise KitError("A cleanup read was incomplete or outside its bound. Nothing further will be deleted.")
    return document["records"]


def id_list(values):
    if not values or any(not isinstance(value, str) or not NATIVE_ID.fullmatch(value) for value in values):
        raise KitError("The binding contains an invalid record identifier.")
    return "(" + ",".join("'" + value + "'" for value in sorted(values)) + ")"


def expected_records(binding):
    from gtm_connector.salesforce_demo import load_tree
    by_reference = {record["ref_id"]: record for record in binding.records}
    expected = {}
    for account in load_tree()["records"]:
        parent = by_reference[account["attributes"]["referenceId"]]
        children = []
        for value in account.values():
            if isinstance(value, dict) and "records" in value:
                children.extend(value["records"])
        for source in (account, *children):
            record = by_reference[source["attributes"]["referenceId"]]
            fields = {key: value for key, value in source.items() if key != "attributes"
                      and not isinstance(value, dict)}
            if record["object"] != "Account":
                fields["AccountId"] = parent["id"]
            if record["object"] == "Contract":
                fields["StatusCode"] = "Draft"
            expected[record["id"]] = {"object": record["object"], "fields": fields}
    return expected


def check_children(sf, object_name, parent_ids, expected):
    metadata = sf.describe(object_name)
    relationships = metadata.get("childRelationships") if isinstance(metadata, dict) else None
    if not isinstance(relationships, list) or len(relationships) > 250:
        raise KitError("Cleanup could not inspect the object's child relationships. No deletes have started.")
    selected = []
    for relationship in relationships:
        if (not isinstance(relationship, dict)
                or type(relationship.get("cascadeDelete")) is not bool
                or type(relationship.get("restrictedDelete")) is not bool):
            raise KitError("Cleanup received an invalid child relationship. No deletes have started.")
        if not (relationship.get("cascadeDelete") or relationship.get("restrictedDelete")):
            continue
        child, field, relation = (relationship.get(key) for key in ("childSObject", "field", "relationshipName"))
        if (not isinstance(child, str) or not IDENTIFIER.fullmatch(child)
                or not isinstance(field, str) or not IDENTIFIER.fullmatch(field)
                or (relation is not None and (not isinstance(relation, str) or not IDENTIFIER.fullmatch(relation)))):
            raise KitError("Cleanup cannot inspect a child relationship safely. No deletes have started.")
        if child in HISTORY:
            # These fixed histories are system-created derivatives of the kit records.
            continue
        selected.append((child, field, relation))

    def check(found, child):
        for row in found:
            identifier = row.get("Id")
            if not isinstance(identifier, str) or not NATIVE_ID.fullmatch(identifier):
                raise KitError("Cleanup returned an invalid child record. No deletes have started.")
            if child in SHARES and row.get("RowCause") == "Owner":
                continue
            if identifier not in expected or expected[identifier]["object"] != child:
                raise KitError("A demo record has an unexpected related record. Remove that link before retrying.")

    nested = [(child, relation) for child, _, relation in selected if relation is not None]
    for offset in range(0, len(nested), 10):
        group = nested[offset:offset + 10]
        fragments = [f"(SELECT {'Id,RowCause' if child in SHARES else 'Id'} FROM {relation} LIMIT 101)"
                     for child, relation in group]
        result = rows(sf.query(f"SELECT Id,{','.join(fragments)} FROM {object_name} "
                               f"WHERE Id IN {id_list(parent_ids)} LIMIT 4"), limit=3)
        if {row.get("Id") for row in result} != parent_ids:
            raise KitError("The demo records changed during cleanup checks. No deletes have started.")
        for parent in result:
            for child, relation in group:
                if relation not in parent:
                    raise KitError("A cleanup relationship could not be read. No deletes have started.")
                if parent[relation] is not None:
                    check(rows(parent[relation]), child)
    for child, field, relation in selected:
        if relation is None:
            fields = "Id,RowCause" if child in SHARES else "Id"
            check(rows(sf.query(f"SELECT {fields} FROM {child} WHERE {field} IN "
                                f"{id_list(parent_ids)} LIMIT 101")), child)


def remove(binding, sf, progress=None):
    def report(step, object_name):
        if progress is not None:
            progress({"action": "remove", "step": step, "object": object_name})

    report("checking edition", "Organization")
    edition = rows(sf.query("SELECT OrganizationType FROM Organization LIMIT 1"), limit=1)
    if len(edition) != 1 or edition[0].get("OrganizationType") != "Developer Edition":
        raise KitError("The demo cleanup requires the Developer Edition org used for the import.")
    expected = expected_records(binding)
    present = {}
    for object_name in OBJECTS:
        report("checking imported records", object_name)
        candidates = {identifier: record for identifier, record in expected.items() if record["object"] == object_name}
        fields = sorted({"Id"} | {field for record in candidates.values() for field in record["fields"]})
        result = rows(sf.query(f"SELECT {','.join(fields)} FROM {object_name} "
                               f"WHERE Id IN {id_list(candidates)} LIMIT 4"), limit=3)
        identifiers = set()
        for row in result:
            identifier = row.get("Id")
            if identifier not in candidates or identifier in identifiers:
                raise KitError("Cleanup did not identify the imported records. No deletes have started.")
            if any(row.get(field) != value for field, value in candidates[identifier]["fields"].items()):
                raise KitError("An imported record was edited or activated. Restore it or clean it up manually.")
            identifiers.add(identifier)
        present[object_name] = identifiers
    for object_name, identifiers in present.items():
        if identifiers:
            report("checking related records", object_name)
            check_children(sf, object_name, identifiers, expected)
    for object_name in OBJECTS:
        report("removing imported records", object_name)
        for identifier in sorted(present[object_name]):
            sf.delete(object_name, identifier)
    for object_name in OBJECTS:
        report("verifying absence", object_name)
        identifiers = {identifier for identifier, record in expected.items() if record["object"] == object_name}
        if rows(sf.query(f"SELECT Id FROM {object_name} WHERE Id IN {id_list(identifiers)} LIMIT 4"), limit=3):
            raise KitError("Cleanup could not verify that all imported records are absent. Keep the binding and retry.")
    return {"status": "removed", "deleted": sf.deleted, "already_absent": len(expected) - sf.deleted,
            "remaining": 0, "sf_cli_invocations": sf.calls}


def brief_arguments(args, binding):
    account = binding.accounts.get(args.account_number)
    if account is None:
        raise KitError("Choose an AccountNumber listed in the invented demo manifest.")
    return ["brief", account, "--crm-backend", "salesforce", "--sf-org", args.sf_org,
            "--salesforce-demo-binding", str(args.binding.resolve()), "--with-slack", "--as-of", binding.as_of,
            "--state-dir", str(args.state_dir.resolve()), *(["--json"] if args.json else [])]


def record(args, binding):
    output = args.output.absolute()
    private_root = ROOT / "var"
    if output.exists() or output.is_symlink() or not output.resolve().is_relative_to(private_root.resolve()):
        raise KitError("Choose a new recording filename under the repository's ignored var folder.")
    output.parent.mkdir(parents=True, mode=0o700, exist_ok=True)
    output.parent.chmod(0o700)
    command = [sys.executable, str(Path(__file__).resolve()), "brief", "--sf-org", args.sf_org,
               "--binding", str(args.binding.resolve()), "--account-number", args.account_number,
               "--state-dir", str(args.state_dir.resolve())]
    configuration = output.parent / (output.stem + "-config")
    configuration.mkdir(mode=0o700)
    (configuration / "config.toml").write_text('[session]\ncapture_input = false\ncapture_env = "TERM"\n')
    environment = dict(os.environ, ASCIINEMA_CONFIG_HOME=str(configuration), TERM="xterm-256color")
    result = subprocess.run(["asciinema", "rec", "--headless", "--quiet", "--return", "--output-format",
                             "asciicast-v2", "--window-size", "112x32", "--capture-env", "TERM", "--title",
                             "Private Salesforce demo: live CRM and invented local notes", "--command",
                             shlex.join(command), str(output)], env=environment, check=False)
    if output.exists():
        output.chmod(0o600)
    if result.returncode:
        raise KitError("The live recording did not complete. Its partial output remains private under var.")
    provenance = {"recorded_at_utc": datetime.now(timezone.utc).isoformat(),
                  "sources": {"crm": "salesforce", "calls": "invented local fixtures",
                              "slack": "invented local fixtures"},
                  "as_of": binding.as_of, "cast_sha256": hashlib.sha256(output.read_bytes()).hexdigest()}
    receipt = output.with_suffix(".provenance.json")
    with receipt.open("x") as stream:
        stream.write(json.dumps(provenance, indent=2) + "\n")
    return {"status": "recorded", "recording": str(output), "private": True}


def main(argv=None):
    parser = argparse.ArgumentParser(description="Prepare the Salesforce kit and remove imported records.",
                                     epilog="Example: python scripts/salesforce-demo.py bind --receipt var/import.json "
                                            "--output var/binding.json")
    parser.add_argument("action", choices=("prepare", "bind", "brief", "remove", "record"))
    parser.add_argument("--receipt", type=Path, help="Read the saved successful sf import JSON.")
    parser.add_argument("--output", type=Path,
                        help="Write exported kit files, a private binding, or a private recording.")
    parser.add_argument("--binding", type=Path, help="Read the private binding made from the import receipt.")
    parser.add_argument("--sf-org", help="Use this existing Salesforce CLI alias explicitly.")
    parser.add_argument("--account-number", default="GTM-DEMO-001", help="Choose an invented manifest AccountNumber.")
    parser.add_argument("--state-dir", type=Path, default=ROOT / "var/salesforce-demo/run",
                        help="Use this private connector state folder.")
    parser.add_argument("--json", action="store_true", help="Print the command result as JSON.")
    args = parser.parse_args(argv)
    os.umask(0o077)
    sf = None
    try:
        from gtm_connector.salesforce_demo import bind_import, kit_files, load_binding, write_binding
        if args.action in ("prepare", "bind", "record") and args.output is None:
            raise KitError("This action requires --output. Run this script with --help.")
        if args.action == "prepare":
            if args.output.exists():
                raise KitError("Choose a new output directory for the exported kit.")
            args.output.mkdir(parents=True, mode=0o700)
            for name, contents in kit_files().items():
                (args.output / name).write_bytes(contents)
            result = {"status": "prepared", "plan": str(args.output / "plan.json")}
        elif args.action == "bind":
            if args.receipt is None:
                raise KitError("Binding requires --receipt from the successful sf import command.")
            binding = bind_import(private_json(args.receipt))
            write_binding(args.output, binding)
            result = {"status": "bound", "accounts": len(binding["accounts"]), "records": len(binding["records"])}
        else:
            if args.binding is None or args.sf_org is None:
                raise KitError("This action requires --binding and --sf-org. Run this script with --help.")
            binding = load_binding(args.binding)
            if args.action == "brief":
                from gtm_connector.cli import main as connector_main
                print("Sources: live Salesforce CRM; invented local call notes and Slack messages.", file=sys.stderr)
                return connector_main(brief_arguments(args, binding))
            if args.action == "record":
                brief_arguments(args, binding)
                result = record(args, binding)
            else:
                sf = SalesforceCLI(args.sf_org)
                def progress(event):
                    print(json.dumps({"progress": event}) if args.json else
                          f"{event['object']}: {event['step']}.", file=sys.stderr, flush=True)
                result = remove(binding, sf, progress)
        print(json.dumps(result, sort_keys=True) if args.json else
              "; ".join(f"{key}: {value}" for key, value in result.items()))
        return 0
    except KeyboardInterrupt:
        print("Stopped. Keep the private import receipt and binding before retrying cleanup.", file=sys.stderr)
        return 130
    except (KitError, ValueError, OSError, RuntimeError, KeyError, TypeError, AttributeError) as exc:
        message = str(exc) if isinstance(exc, KitError) else "The kit input or local setup is invalid. Run --help."
        print(json.dumps({"error": {"message": message, "next": "python scripts/salesforce-demo.py --help",
                                    "delete_attempts": 0 if sf is None else sf.writes_attempted,
                                    "confirmed_deletes": 0 if sf is None else sf.deleted}}), file=sys.stderr)
        return 3


if __name__ == "__main__":
    raise SystemExit(main())
