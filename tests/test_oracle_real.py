"""tools/oracle/real.py records what the package does to real text, for the Go port to match (R0)."""

import base64
import gzip
import hashlib
import importlib.util
import json
import os
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
        for row, digest, line, data in zip(outputs, digests, (1, 3), (text.encode(), document)):
            sums = {name: sha256(dumps(value).encode()) for name, value in row.items()}
            self.assertEqual(digest, [line, sha256(data), sums])
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

    def test_outputs_stay_in_folder(self):
        """Links and files left in the folder are replaced, never written through, and every output is private."""
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "' + SECRET + ' first."}\n')
        out = self.folder / "out"
        out.mkdir(mode=0o700)
        sentinel = self.folder / "outside" / "sentinel.txt"
        sentinel.parent.mkdir()
        sentinel.write_text("untouched")
        for name in ("oracle.jsonl.gz", "oracle-digests.jsonl.gz", "oracle.json"):
            (out / (name + ".partial")).symlink_to(sentinel)
        os.link(sentinel, out / "oracle-digests.jsonl.gz")
        (out / "oracle.json").write_text("{}")
        (out / "oracle.json").chmod(0o644)
        result, _ = self.run_oracle(items, "--local")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(sentinel.read_text(), "untouched")
        for name in ("oracle.jsonl.gz", "oracle-digests.jsonl.gz", "oracle.json"):
            mode = (out / name).lstat().st_mode
            self.assertTrue(stat.S_ISREG(mode), name)
            self.assertEqual(stat.S_IMODE(mode), 0o600, name)
        self.assertEqual(len(lines(out / "oracle-digests.jsonl.gz")), 1)

    def test_local_folder_must_be_private(self):
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "x"}\n')
        out = self.folder / "out"
        elsewhere = self.folder / "elsewhere"
        elsewhere.mkdir(mode=0o700)
        out.symlink_to(elsewhere)
        result, _ = self.run_oracle(items, "--local")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(list(elsewhere.iterdir()), [])
        out.unlink()
        out.mkdir(mode=0o755)
        out.chmod(0o755)
        result, _ = self.run_oracle(items, "--local")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(list(out.iterdir()), [])

    def test_item_line_vectors(self):
        """Both readers accept and refuse the same lines (internal/realdata/items_test.go reads the same vectors)."""
        spec = importlib.util.spec_from_file_location("oracle_real", REAL)
        real = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(real)
        vectors = json.loads((ROOT / "internal" / "realdata" / "testdata" / "item-lines.json").read_text())
        for vector in vectors["lines"]:
            with self.subTest(vector["why"]):
                path = self.folder / "line.jsonl"
                path.write_bytes(bytes.fromhex(vector["hex"]) + b"\n")
                try:
                    list(real.Items(path, vector["local"]))
                    accepted = True
                except real.Refused:
                    accepted = False
                self.assertEqual(accepted, vector["ok"])

    def run_patched(self, patch, items, *flags):
        """real.py under -I, after a patch to the oracle's modules, as cmd/realdata would run it."""
        out = self.folder / "out"
        code = (f"import runpy, sys; sys.path[:0] = [{str(ROOT)!r}, {str(ROOT / 'tools')!r}]; {patch}; "
                f"sys.argv = [{str(REAL)!r}, '--out', {str(out)!r}, {str(items)!r}, *{list(flags)!r}]; "
                f"runpy.run_path({str(REAL)!r}, run_name='__main__')")
        return subprocess.run([sys.executable, "-I", "-c", code], capture_output=True, text=True, cwd=ROOT), out

    def test_interpreter_pin(self):
        """Another interpreter or Unicode version is refused before anything is written."""
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "x"}\n')
        for patch, version in (("import oracle.generate as g; g.PYTHON = '3.99.0'", "3.99.0"),
                               ("import oracle.generate as g; g.UNICODE = '15.0.0'", "15.0.0")):
            with self.subTest(version):
                result, out = self.run_patched(patch, items, "--local")
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertIn(version, result.stderr)
                self.assertIn("3.14.6" if version == "3.99.0" else "16.0.0", result.stderr)
                self.assertFalse(out.exists())

    def test_failure_names_type_only(self):
        """An exception whose message quotes an item shows only its type, and leaves no file behind."""
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "' + SECRET + ' first."}\n')
        patch = "import oracle.output as o; o.dumps = lambda value: (_ for _ in ()).throw(ValueError(repr(value)))"
        result, out = self.run_patched(patch, items, "--local")
        self.assertEqual(result.returncode, 1)
        self.assertIn("ValueError", result.stderr)
        self.assertNotIn(SECRET, result.stdout + result.stderr)
        self.assertEqual(list(out.iterdir()), [])

    def test_needs_isolated_mode(self):
        items = self.folder / "items.jsonl"
        items.write_text('{"id": "a", "text": "x"}\n')
        result, _ = self.run_oracle(items, "--local", isolated=False)
        self.assertEqual(result.returncode, 2)
        self.assertIn("-I", result.stderr)


if __name__ == "__main__":
    unittest.main()
