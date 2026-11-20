"""Write testdata/oracle/: what the Python package does, for the Go port to match.

Run from the repository root with the pinned interpreter:

    .venv/bin/python -I tools/oracle/generate.py

--out writes elsewhere. --part runs one part and writes no manifest; the drift test uses it.

The generator runs again under a fixed host: UTF-8 mode, the C locale, UTC, no color, a fixed PATH and terminal
width, and none of the other variables it was started with. GTM_BOOKS, which narrows the tests' books, is ignored:
the oracle always reads every book.
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
# The only host settings the generator sees. TMPDIR and HOME pass through; output never names either.
HOST = {"PATH": "/usr/bin:/bin", "LC_ALL": "C", "LANG": "C", "TZ": "UTC", "NO_COLOR": "1", "TERM": "dumb",
        "COLUMNS": "100", "LINES": "50", "GTM_ORACLE_HOST": "fixed"}
PASSED = ("HOME", "TMPDIR")
# Variables that would change what is recorded if the generator kept them. Each one set is named when ignored.
IGNORED = ("GTM_BOOKS", "FORCE_COLOR", "CLICOLOR_FORCE", "PYTHON_COLORS")


def _host() -> dict[str, str]:
    return {**{name: os.environ[name] for name in PASSED if name in os.environ}, **HOST}


def _fix_host(argv: list[str]) -> None:
    """Start again under the fixed host unless this run already is under it."""
    if os.environ.get("GTM_ORACLE_HOST") == "fixed":
        # macOS adds its text encoding variable to every process; nothing the package does reads it.
        seen = {name: value for name, value in os.environ.items() if name != "__CF_USER_TEXT_ENCODING"}
        if seen != _host() or not sys.flags.utf8_mode or not sys.flags.isolated:
            raise SystemExit("the oracle's fixed host was changed after it started")
        return
    ignored = [name for name in IGNORED if name in os.environ]
    if ignored:
        print(f"Ignored: {', '.join(ignored)}. The oracle reads every book and writes no color.", file=sys.stderr)
    sys.stderr.flush()
    os.execve(sys.executable, [sys.executable, "-I", "-X", "utf8", str(Path(__file__).resolve()), *argv], _host())


def _package_digest() -> str:
    """One digest of the package source, so fixtures and the code that made them cannot drift apart unseen."""
    digest = hashlib.sha256()
    for path in sorted((ROOT / "gtm_connector").glob("*.py")):
        digest.update(path.name.encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
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
    _fix_host(argv)
    # The package and the tests' data helpers import from the repository root, and some test modules import
    # each other by their own names; tools/ holds the generator.
    sys.path[:0] = [str(ROOT), str(ROOT / "tools")]
    sys.path.append(str(ROOT / "tests"))
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
