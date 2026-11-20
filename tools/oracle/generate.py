"""Write testdata/oracle/: what the Python package does, for the Go port to match.

Run from the repository root with the pinned interpreter:

    .venv/bin/python -I tools/oracle/generate.py

--out writes elsewhere. --part runs one part and writes no manifest; the drift test uses it.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import sys
import tempfile
import unicodedata


ROOT = Path(__file__).resolve().parents[2]
PYTHON = "3.14.6"
UNICODE = "16.0.0"
PARTS = ("unicode", "scanners", "library", "cards", "commands")


def _package_digest() -> str:
    """One digest of the package source, so fixtures and the code that made them cannot drift apart unseen."""
    digest = hashlib.sha256()
    for path in sorted((ROOT / "gtm_connector").glob("*.py")):
        digest.update(path.name.encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Write the oracle fixtures.")
    parser.add_argument("--out", type=Path, help="Write here instead of testdata/oracle/.")
    parser.add_argument("--part", choices=PARTS, action="append",
                        help="Write only this part, and no manifest. Repeat for more.")
    args = parser.parse_args(argv)
    if args.part and args.out is None:
        parser.error("--part writes a partial set, so it needs --out.")
    version = ".".join(map(str, sys.version_info[:3]))
    if version != PYTHON or sys.implementation.name != "cpython" or unicodedata.unidata_version != UNICODE:
        print(f"The oracle runs on CPython {PYTHON} with Unicode {UNICODE}; this is {sys.implementation.name} "
              f"{version} with Unicode {unicodedata.unidata_version}.", file=sys.stderr)
        return 2
    # The package and the tests' data helpers import from the repository root, and some test modules import
    # each other by their own names; tools/ holds the generator.
    sys.path[:0] = [str(ROOT), str(ROOT / "tools")]
    sys.path.append(str(ROOT / "tests"))
    # Help text wraps at the terminal width, so it is fixed.
    os.environ["COLUMNS"] = "100"
    from oracle import cards, commands, freeze, library, scanners, unicode_tables
    from oracle.output import Writer
    freeze.install()
    parts = args.part or list(PARTS)
    out = (args.out or ROOT / "testdata" / "oracle").resolve()
    # The fixtures are written beside their place and moved in whole, so a failed run leaves the old ones.
    out.parent.mkdir(parents=True, exist_ok=True)
    staging = Path(tempfile.mkdtemp(prefix=".oracle-", dir=out.parent))
    try:
        writer = Writer(staging)
        modules = {"unicode": unicode_tables, "scanners": scanners, "library": library, "cards": cards,
                   "commands": commands}
        for part in parts:
            modules[part].generate(writer)
        if args.part is None:
            writer.json("manifest.json", {
                "python_version": version, "python_build": sys.version, "unicode_version": unicodedata.unidata_version,
                "package_sha256": _package_digest(), "generator": "tools/oracle/generate.py",
                "files": dict(sorted(writer.files.items()))}, len(writer.files))
        if out.exists():
            shutil.rmtree(out)
        staging.rename(out)
    finally:
        if staging.exists():
            shutil.rmtree(staging)
    total = sum(path.stat().st_size for path in out.rglob("*") if path.is_file())
    print(json.dumps({"out": str(out), "files": len(writer.files), "bytes": total}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
