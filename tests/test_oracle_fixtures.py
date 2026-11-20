"""testdata/oracle/ holds what the Python package does. Regenerating it must give the committed fixtures."""

import gzip
import json
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import unicodedata
import unittest


ROOT = Path(__file__).resolve().parents[1]
ORACLE = ROOT / "testdata" / "oracle"
PINNED = sys.implementation.name == "cpython" and sys.version_info[:3] == (3, 14, 6) \
    and unicodedata.unidata_version == "16.0.0"


def files(folder):
    return sorted(str(path.relative_to(folder)) for path in folder.rglob("*") if path.is_file())


def content(path):
    """What a fixture holds. A gzip file is read through, as zlib builds may compress alike content differently. An
    SQLite file is read as its rows, as its header names the SQLite library. The manifest's build line names the
    interpreter build."""
    if path.name == "audit.sqlite3.gz":
        with tempfile.TemporaryDirectory() as folder:
            database = Path(folder) / "audit.sqlite3"
            database.write_bytes(gzip.decompress(path.read_bytes()))
            connection = sqlite3.connect(database)
            try:
                return connection.execute("SELECT * FROM audit_log ORDER BY rowid").fetchall()
            finally:
                connection.close()
    data = gzip.decompress(path.read_bytes()) if path.suffix == ".gz" else path.read_bytes()
    if path.name == "manifest.json":
        manifest = json.loads(data)
        del manifest["python_build"]
        return manifest
    return data


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
