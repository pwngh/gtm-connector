"""JSON parsing that refuses duplicate keys and non-finite numbers."""

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
    """Parse one document. Raises ValueError, which covers bad UTF-8, or RecursionError."""
    options.setdefault("parse_float", _float)
    return json.loads(document, object_pairs_hook=_unique, parse_constant=_finite, **options)
