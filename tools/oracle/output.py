"""Deterministic fixture files and the manifest that lists them."""

import gzip
import hashlib
import json
from pathlib import Path
from typing import Any, Iterable


def dumps(value: Any) -> str:
    """One fixture value as ASCII JSON. Escapes keep every code point, lone surrogates included."""
    return json.dumps(value, ensure_ascii=True, separators=(",", ":"), allow_nan=False)


def ranges(flags: Iterable[bool]) -> list[list[int]]:
    """The code points where a predicate holds, as inclusive [first, last] runs."""
    found: list[list[int]] = []
    for code, flag in enumerate(flags):
        if not flag:
            continue
        if found and found[-1][1] == code - 1:
            found[-1][1] = code
        else:
            found.append([code, code])
    return found


class Writer:
    """Writes fixture files under one folder. Every file is listed in the manifest with its digest and count."""

    def __init__(self, root: Path) -> None:
        self.root = root
        self.files: dict[str, dict[str, Any]] = {}

    def _write(self, name: str, data: bytes, records: int, digested: bytes | None = None) -> None:
        if name in self.files:
            raise ValueError(f"fixture written twice: {name}")
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        # The digest is of the content, so a gzip file is checked by what it holds.
        self.files[name] = ({"sha256": hashlib.sha256(data).hexdigest(), "bytes": len(data), "records": records}
                            if digested is None else
                            {"sha256_of_rows": hashlib.sha256(digested).hexdigest(), "records": records})
        if name.endswith(".gz"):
            # No name and no time in the header, so the same content gives the same file.
            with open(path, "wb") as raw, gzip.GzipFile(filename="", mode="wb", fileobj=raw, mtime=0,
                                                         compresslevel=9) as output:
                output.write(data)
        else:
            path.write_bytes(data)

    def json(self, name: str, value: Any, records: int) -> None:
        self._write(name, (json.dumps(value, ensure_ascii=True, indent=1, allow_nan=False) + "\n").encode(),
                    records)

    def lines(self, name: str, rows: Iterable[Any]) -> int:
        """JSON Lines, one value a line. Returns the number of lines."""
        encoded = [dumps(row) for row in rows]
        self._write(name, "".join(line + "\n" for line in encoded).encode(), len(encoded))
        return len(encoded)

    def raw(self, name: str, data: bytes, records: int = 1, *, digested: bytes | None = None) -> None:
        """Bytes as they are. `digested` stands in for them in the manifest where the bytes depend on a library."""
        self._write(name, data, records, digested)
