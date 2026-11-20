"""testdata/oracle/ holds what the Python package does. Regenerating it must give the committed fixtures."""

import gzip
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unicodedata
import unittest

from gtm_connector.audit import read_audit, read_ids, read_receipt


ROOT = Path(__file__).resolve().parents[1]
ORACLE = ROOT / "testdata" / "oracle"
PINNED = sys.implementation.name == "cpython" and sys.version_info[:3] == (3, 14, 6) \
    and unicodedata.unidata_version == "16.0.0"
# CI sets GTM_REQUIRE_ORACLE=1 on its 3.14.6 job, so a mismatched interpreter fails there instead of skipping.
if os.environ.get("GTM_REQUIRE_ORACLE") == "1" and not PINNED:
    raise RuntimeError("GTM_REQUIRE_ORACLE=1 needs CPython 3.14.6 with Unicode 16.0.0, not "
                       f"{sys.implementation.name} {sys.version.split()[0]} "
                       f"with Unicode {unicodedata.unidata_version}.")


def files(folder):
    return sorted(str(path.relative_to(folder)) for path in folder.rglob("*") if path.is_file())


def audit_log(path):
    """A gzipped audit database as a verifier reads it: its schema and column names beside its rows, and what the
    package's own receipt reader returns from it, or the error it raises."""
    with tempfile.TemporaryDirectory() as folder:
        database = Path(folder) / "audit.sqlite3"
        database.write_bytes(gzip.decompress(path.read_bytes()))
        connection = sqlite3.connect(database)
        try:
            schema = connection.execute("SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")
            cursor = connection.execute("SELECT * FROM audit_log ORDER BY rowid")
            log = {"schema": schema.fetchall(), "columns": [column[0] for column in cursor.description],
                   "rows": cursor.fetchall()}
        finally:
            connection.close()
        try:
            log["read_audit"] = read_audit(database)
        except Exception as error:  # A schema the reader cannot read is a difference to report, not a crash.
            log["read_audit"] = repr(error)
        return log


def content(path):
    """What a fixture holds. A gzip file is read through, as zlib builds may compress alike content differently. An
    SQLite file is read as its schema, columns and rows, as its header names the SQLite library. The manifest's
    build line names the interpreter build."""
    if path.name == "audit.sqlite3.gz":
        return audit_log(path)
    data = gzip.decompress(path.read_bytes()) if path.suffix == ".gz" else path.read_bytes()
    if path.name == "manifest.json":
        manifest = json.loads(data)
        del manifest["python_build"]
        return manifest
    return data


class CommittedAuditLogTests(unittest.TestCase):
    """The committed audit databases read through the package's receipt reader, as a verifier would read them."""

    def test_the_receipt_reader_returns_each_committed_row(self):
        databases = sorted(ORACLE.glob("folders/*/audit.sqlite3.gz"))
        self.assertTrue(databases, "testdata/oracle/ has no audit databases")
        for path in databases:
            with self.subTest(folder=path.parent.name), tempfile.TemporaryDirectory() as folder:
                database = Path(folder) / "audit.sqlite3"
                database.write_bytes(gzip.decompress(path.read_bytes()))
                rows = [json.loads(line) for line in (path.parent / "audit.jsonl").read_text().splitlines()]
                for row in rows:
                    for field in ("arguments", "request_id", "rows"):
                        row[field] = json.loads(row[field])
                self.assertTrue(rows)
                self.assertEqual(read_audit(database), rows)
                ids = [row["audit_id"] for row in rows]
                self.assertEqual([found for start in range(0, len(ids), 16)
                                  for found in read_ids(database, ids[start:start + 16])], rows)
                self.assertEqual(read_receipt(database, ids[-1]), rows[-1])


@unittest.skipUnless(PINNED, "The oracle fixtures are made on CPython 3.14.6 with Unicode 16.0.0. This interpreter is "
                     f"{sys.implementation.name} {sys.version.split()[0]} with Unicode {unicodedata.unidata_version}, "
                     "so it cannot regenerate them.")
class OracleFixtureTests(unittest.TestCase):
    def test_regenerating_gives_the_committed_fixtures(self):
        self.assertTrue((ORACLE / "manifest.json").is_file(), "testdata/oracle/ is missing; run tools/oracle")
        with tempfile.TemporaryDirectory() as folder:
            out = Path(folder) / "oracle"
            result = subprocess.run([sys.executable, "-I", str(ROOT / "tools" / "oracle" / "generate.py"),
                                     "--out", str(out)], cwd=ROOT, capture_output=True, text=True, timeout=600)
            self.assertEqual(result.returncode, 0, result.stderr[-3000:])
            self.assertEqual(files(out), files(ORACLE))
            # Name the files that differ, not their megabytes.
            changed = [name for name in files(ORACLE) if content(out / name) != content(ORACLE / name)]
            self.assertEqual(changed, [], "Python behaviour or a test input changed. If that is intended, "
                             "regenerate with tools/oracle/generate.py in the same change.")


if __name__ == "__main__":
    unittest.main()
