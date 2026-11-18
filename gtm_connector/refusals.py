"""Stable identifiers for gateway request checks, not SDK or tool execution errors."""

from dataclasses import dataclass


@dataclass(frozen=True)
class Refusal:
    status: int
    code: int


REFUSALS = {
    "request_too_large": Refusal(413, -32600),
    "invalid_json": Refusal(400, -32700),
    "untrusted_origin_or_host": Refusal(403, 1003),
    "invalid_token": Refusal(401, 1001),
    "rate_limited": Refusal(429, 1002),
    "insufficient_scope": Refusal(403, 1004),
    "crm_write_disabled": Refusal(403, 1005),
    "invalid_arguments": Refusal(400, -32602),
    "missing_protocol_version": Refusal(400, -32602),
    "protocol_version_mismatch": Refusal(400, -32020),
    "unsupported_protocol_version": Refusal(400, -32022),
}
