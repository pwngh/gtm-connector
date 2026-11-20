"""R0: run the Python package over real text, for the Go port to match (backend plan section 8a).

cmd/realdata runs this; by hand it is:

    .venv/bin/python -I tools/oracle/real.py [--local] --out DIR ITEMS

ITEMS is an items file: cmd/realdata's conversion of a dataset, or with --local a private JSON Lines file of
{"id", "text"} objects. DIR receives:

- oracle.jsonl.gz: each item's outputs, {function: value}, one line per item in order;
- oracle-digests.jsonl.gz: [SHA-256 of the input, {function: SHA-256 of its output}], one line per item, which
  the comparison keeps using after the Python package is gone;
- oracle.json: the interpreter, the package digest, the items file's digest and the functions.

An output is encoded as oracle/output.py's dumps writes it, and its digest is of those bytes. The items are read
by the rules internal/realdata/items.go states. Nothing printed holds item text: a refused line is named by its
number, and any other failure by its exception type only.
"""

import argparse
import base64
import binascii
from decimal import Decimal
from functools import partial
import gzip
import hashlib
import io
import json
import os
from pathlib import Path
import sys
import time
from typing import IO, Any, Callable, Iterator


ROOT = Path(__file__).resolve().parents[2]
TEXT_FUNCTIONS = ("safe_text", "sentences", "sentences_lines", "sentences_safe", "sentences_safe_lines",
                  "units_target", "units_raw_link", "units_sentence_end", "units_opener", "units_named",
                  "slack_mapping_percent", "slack_callback_percent")
JSON_FUNCTIONS = ("strict_json", "strict_json_decimal")


class Refused(Exception):
    """An input this runner will not read. The message names the line and never quotes it."""


class Items:
    """The items of a file in order, as (line number, text or a JSON document's bytes). Every byte read is
    digested."""

    def __init__(self, path: Path, local: bool) -> None:
        self.path, self.local = path, local
        self.digest = hashlib.sha256()

    def __iter__(self) -> Iterator[tuple[int, str | bytes]]:
        handle: io.BufferedIOBase = (gzip.GzipFile(self.path, "rb") if self.path.name.endswith(".gz")
                                     else open(self.path, "rb"))
        with handle:
            for number, raw in enumerate(handle, 1):
                self.digest.update(raw)
                if raw.strip(b" \t\r\n"):
                    yield number, self._parse(raw, number)

    def _parse(self, raw: bytes, number: int) -> str | bytes:
        from gtm_connector import strict_json

        def refuse(reason: str) -> Refused:
            return Refused(f"line {number} is not an item: {reason}")
        try:
            value = strict_json.loads(raw)
        except Exception:
            raise refuse("not JSON that strict_json reads") from None
        if not isinstance(value, dict):
            raise refuse("not a JSON object")
        if self.local and "json" in value:
            raise refuse('a local file holds text items only, not "json"')
        keys = ("id", "text") if self.local else ("id", "file", "kind", "text", "json")
        if not self.local and set(value) - set(keys):
            raise refuse("a key other than id, file, kind, text and json")
        for key in keys:
            if key in value and not isinstance(value[key], str):
                raise refuse(f'"{key}" is not a string')
        if "id" not in value:
            raise refuse('no "id"')
        if ("text" in value) == ("json" in value):
            raise refuse('not exactly one of "text" and "json"')
        if "json" in value:
            try:
                return base64.b64decode(value["json"], validate=True)
            except binascii.Error:
                raise refuse('"json" is not standard base64') from None
        return value["text"]


def _outcome(function: Callable[[], Any]) -> Any:
    try:
        return {"ok": function()}
    except Exception as error:  # A refusal is part of the output, compared by type and message.
        return {"error": type(error).__name__, "message": str(error)}


def document_outputs(raw: bytes) -> dict[str, Any]:
    """strict_json as library/strict_json.json records it, without and with parse_float=Decimal."""
    from gtm_connector import strict_json
    return {
        "strict_json": _outcome(lambda: json.dumps(strict_json.loads(raw), ensure_ascii=True)),
        "strict_json_decimal": _outcome(lambda: json.dumps(
            strict_json.loads(raw, parse_float=Decimal), ensure_ascii=True,
            default=lambda value: {"decimal": str(value)})),
    }


def text_outputs(text: str) -> dict[str, Any]:
    """safe_text, the sentence splitter with and without lines, of the text and of its safe text, and each
    scanner, as the oracle fixtures record them. A function that raises gives its exception type only."""
    from gtm_connector import units
    from oracle.scanners import SCANNERS
    found: dict[str, Any] = {}
    safe: list[str] = []

    def run(name: str, function: Callable[[], Any]) -> None:
        try:
            found[name] = function()
        except Exception as error:  # The message could quote the text; the type is enough to differ.
            found[name] = {"error": type(error).__name__}

    def neutralized() -> list:
        value, counts = units.safe_text(text)
        safe.append(value)
        return [value, [counts["mentions"], counts["links"], counts["controls"], counts["brackets"]]]

    def of_safe(lines: bool) -> list[str]:
        if not safe:
            raise ValueError("no safe_text")
        return units.sentences(safe[0], lines=lines)

    run("safe_text", neutralized)
    run("sentences", lambda: units.sentences(text))
    run("sentences_lines", lambda: units.sentences(text, lines=True))
    run("sentences_safe", lambda: of_safe(False))
    run("sentences_safe_lines", lambda: of_safe(True))
    for name, (_, scan) in SCANNERS.items():
        run(name, partial(scan, text))
    return found


def _create(path: Path) -> IO[bytes]:
    return os.fdopen(os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run the Python package over an items file for R0.")
    parser.add_argument("items", type=Path)
    parser.add_argument("--out", type=Path, required=True, help="The folder for the outputs.")
    parser.add_argument("--local", action="store_true", help="A private file of {id, text} lines.")
    args = parser.parse_args(argv)
    if not sys.flags.isolated:
        print("Run the oracle with -I.", file=sys.stderr)
        return 2
    sys.path[:0] = [str(ROOT), str(ROOT / "tools")]
    import unicodedata
    from oracle.generate import PYTHON, UNICODE, _package_digest
    from oracle.output import dumps
    from oracle.scanners import SCANNERS
    version = ".".join(map(str, sys.version_info[:3]))
    if version != PYTHON or sys.implementation.name != "cpython" or unicodedata.unidata_version != UNICODE:
        print(f"The oracle runs on CPython {PYTHON} with Unicode {UNICODE}; this is {sys.implementation.name} "
              f"{version} with Unicode {unicodedata.unidata_version}.", file=sys.stderr)
        return 2
    if TEXT_FUNCTIONS[5:] != tuple(SCANNERS):
        raise SystemExit("The scanners changed; update TEXT_FUNCTIONS here and in internal/realdata.")
    started = time.monotonic()
    out = args.out.resolve()
    out.mkdir(mode=0o700, parents=True, exist_ok=True)
    items = Items(args.items.resolve(), args.local)
    counts = {"text": 0, "json": 0}
    names = ("oracle.jsonl.gz", "oracle-digests.jsonl.gz")
    unfinished = [out / (name + ".partial") for name in names]
    try:
        with _create(unfinished[0]) as raw_outputs, _create(unfinished[1]) as raw_digests, \
                gzip.GzipFile(filename="", mode="wb", fileobj=raw_outputs, mtime=0) as outputs, \
                gzip.GzipFile(filename="", mode="wb", fileobj=raw_digests, mtime=0) as digests:
            for _, value in items:
                if isinstance(value, bytes):
                    counts["json"] += 1
                    found, data = document_outputs(value), value
                else:
                    counts["text"] += 1
                    found, data = text_outputs(value), value.encode()
                encoded = {name: dumps(output) for name, output in found.items()}
                outputs.write(("{" + ",".join(f"{dumps(name)}:{text}" for name, text in encoded.items())
                               + "}\n").encode())
                sums = {name: hashlib.sha256(text.encode()).hexdigest() for name, text in encoded.items()}
                digests.write((dumps([hashlib.sha256(data).hexdigest(), sums]) + "\n").encode())
    except BaseException:
        # What was written so far holds item text; a failed run leaves none of it.
        for path in unfinished:
            path.unlink(missing_ok=True)
        raise
    for path, name in zip(unfinished, names):
        path.replace(out / name)
    meta = {"python": version, "unicode": unicodedata.unidata_version, "package_sha256": _package_digest(),
            "items_sha256": items.digest.hexdigest(), "items": counts["text"] + counts["json"],
            "text_functions": list(TEXT_FUNCTIONS), "json_functions": list(JSON_FUNCTIONS)}
    with _create(out / "oracle.json.partial") as handle:
        handle.write((json.dumps(meta, indent=1) + "\n").encode())
    (out / "oracle.json.partial").replace(out / "oracle.json")
    print(f"oracle: {counts['text']} text items and {counts['json']} JSON documents in "
          f"{time.monotonic() - started:.1f} s")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except Refused as refusal:
        print(f"oracle: {refusal}", file=sys.stderr)
        sys.exit(1)
    except Exception as error:  # A message or a traceback could quote an item, so only the type is shown.
        detail = f" ({error.strerror})" if isinstance(error, OSError) and error.strerror else ""
        print(f"oracle: stopped by {type(error).__name__}{detail}", file=sys.stderr)
        sys.exit(1)
    except KeyboardInterrupt:
        print("oracle: interrupted", file=sys.stderr)
        sys.exit(130)
