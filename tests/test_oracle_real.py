"""tools/oracle/real.py records what the package does to real text, for the Go port to match (R0)."""

import base64
import gzip
import hashlib
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unicodedata
import unittest

from gtm_connector import strict_json, units


ROOT = Path(__file__).resolve().parents[1]
REAL = ROOT / "tools" / "oracle" / "real.py"
PINNED = sys.implementation.name == "cpython" and sys.version_info[:3] == (3, 14, 6) \
    and unicodedata.unidata_version == "16.0.0"
# Stands for private text: nothing the runner prints may quote it.
SECRET = "Zanzibar"


def dumps(value):
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), allow_nan=False)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def lines(path):
    with gzip.open(path, "rb") as handle:
        return [json.loads(line) for line in handle]


@unittest.skipUnless(PINNED, "the oracle runs on CPython 3.14.6 with Unicode 16.0.0")
class RealOracleTest(unittest.TestCase):
    def setUp(self):
        folder = tempfile.TemporaryDirectory()
        self.addCleanup(folder.cleanup)
        self.folder = Path(folder.name)

    def run_oracle(self, items, *flags, isolated=True):
        out = self.folder / "out"
        command = [sys.executable, *(["-I"] if isolated else []), str(REAL), "--out", str(out), str(items), *flags]
        return subprocess.run(command, capture_output=True, text=True, cwd=ROOT), out

    def test_outputs_and_digests(self):
        text = f"<@U0ABC123> We sign Friday. {SECRET} see https://a.example/x?q=%41 ‮\n\nNo. 14 goes on."
        document = b'{"Amount": 450000.00, "a": [1, 2.5e-7]}'
        items = self.folder / "items.jsonl"
        items.write_bytes((json.dumps({"id": "t", "file": "f.xml", "kind": "natural", "text": text}) + "\n\n"
                           + json.dumps({"id": "j", "json": base64.b64encode(document).decode()}) + "\n").encode())
        result, out = self.run_oracle(items)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        outputs, digests = lines(out / "oracle.jsonl.gz"), lines(out / "oracle-digests.jsonl.gz")
        meta = json.loads((out / "oracle.json").read_text())
        safe, counts = units.safe_text(text)
        self.assertEqual(outputs[0]["safe_text"], [safe, [counts["mentions"], counts["links"], counts["controls"],
                                                         counts["brackets"]]])
        self.assertEqual(outputs[0]["sentences"], units.sentences(text))
        self.assertEqual(outputs[0]["sentences_lines"], units.sentences(text, lines=True))
        self.assertEqual(outputs[0]["sentences_safe_lines"], units.sentences(safe, lines=True))
        self.assertEqual(list(outputs[0]), meta["text_functions"])
        self.assertEqual(outputs[1], {
            "strict_json": {"ok": json.dumps(strict_json.loads(document), ensure_ascii=True)},
            "strict_json_decimal": {"ok": '{"Amount": {"decimal": "450000.00"}, "a": [1, {"decimal": "2.5E-7"}]}'}})
        for row, digest, data in zip(outputs, digests, (text.encode(), document)):
            self.assertEqual(digest, [sha256(data), {name: sha256(dumps(value).encode()) for name, value in row.items()}])
        self.assertEqual(meta["items_sha256"], sha256(items.read_bytes()))
        self.assertEqual((meta["items"], meta["python"]), (2, "3.14.6"))
        self.assertEqual(stat.S_IMODE(out.stat().st_mode), 0o700)
        for name in ("oracle.json", "oracle.jsonl.gz", "oracle-digests.jsonl.gz"):
            self.assertEqual(stat.S_IMODE((out / name).stat().st_mode), 0o600)

    def test_refusals_quote_nothing(self):
        for name, line, local in (
                ("not JSON", f'{{"id": "a", "text": "{SECRET}" x}}', True),
                ("a lone surrogate", f'{{"id": "a", "text": "{SECRET} \\ud800"}}', True),
                ("json in a local file", f'{{"id": "a", "json": "{SECRET}"}}', True),
                ("a number text", f'{{"id": "a", "text": 7, "note": "{SECRET}"}}', True),
                ("an unknown key", f'{{"id": "a", "text": "a", "{SECRET}": 1}}', False)):
            with self.subTest(name):
                items = self.folder / "items.jsonl"
                items.write_text('{"id": "ok", "text": "' + SECRET + ' first."}\n' + line + "\n")
                result, out = self.run_oracle(items, *(["--local"] if local else []))
                self.assertEqual(result.returncode, 1)
                self.assertIn("line 2 is not an item", result.stderr)
                self.assertNotIn(SECRET, result.stdout + result.stderr)
                self.assertEqual(sorted(path.name for path in out.iterdir()), [])

    def test_needs_isolated_mode(self):
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "x"}\n')
        result, _ = self.run_oracle(items, "--local", isolated=False)
        self.assertEqual(result.returncode, 2)
        self.assertIn("-I", result.stderr)


if __name__ == "__main__":
    unittest.main()
