"""JSON parsing that refuses duplicate keys, non-finite numbers, and lone surrogates."""

import json
from typing import Any


def _unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key")
        result[key] = value
    return result


def _finite(_):
    raise ValueError("non-finite JSON number")


def _float(text):
    value = float(text)
    if value in (float("inf"), float("-inf")):
        raise ValueError("non-finite JSON number")
    return value


def loads(document: str | bytes, **options: Any) -> Any:
    """Parse one UTF-8 document. Raises ValueError, which covers bad UTF-8, or RecursionError."""
    options.setdefault("parse_float", _float)
    # json decodes bytes leniently and lets surrogate bytes through, so decode strictly first.
    text = document if isinstance(document, str) else bytes(document).decode("utf-8-sig")
    text.encode()
    result = json.loads(text, object_pairs_hook=_unique, parse_constant=_finite, **options)
    if "\\u" in text:
        # An escaped lone surrogate parses to text that cannot be stored or sent as UTF-8.
        pending = [result]
        while pending:
            value = pending.pop()
            if isinstance(value, str):
                value.encode()
            elif isinstance(value, dict):
                pending.extend(value)
                pending.extend(value.values())
            elif isinstance(value, list):
                pending.extend(value)
    return result
