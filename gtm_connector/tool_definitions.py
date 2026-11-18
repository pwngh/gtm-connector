"""Tool permissions and gateway argument rules, shared by registration and checks."""

from dataclasses import dataclass


@dataclass(frozen=True)
class StringArgument:
    max_length: int
    required: bool = True
    nullable: bool = False


@dataclass(frozen=True)
class ToolDefinition:
    scope: str
    read_only: bool
    arguments: dict[str, StringArgument]


TOOL_DEFINITIONS = {
    "search_accounts": ToolDefinition(
        scope="crm:read", read_only=True,
        arguments={"query": StringArgument(200, required=False),
                   "account_number": StringArgument(40, required=False, nullable=True)},
    ),
    "get_account": ToolDefinition(
        scope="crm:read", read_only=True,
        arguments={"account_id": StringArgument(64)},
    ),
    "list_opportunities": ToolDefinition(
        scope="crm:read", read_only=True,
        arguments={"account_id": StringArgument(64, required=False, nullable=True)},
    ),
    "read_call_notes": ToolDefinition(
        scope="calls:read", read_only=True,
        arguments={"account_id": StringArgument(64)},
    ),
    "read_slack_messages": ToolDefinition(
        scope="slack:read", read_only=True,
        arguments={"account_id": StringArgument(64)},
    ),
    "log_activity": ToolDefinition(
        scope="crm:write", read_only=False,
        arguments={"account_id": StringArgument(64), "kind": StringArgument(40),
                   "body": StringArgument(2000), "idempotency_key": StringArgument(128)},
    ),
}


def valid_account_search(query, account_number):
    """An exact account number cannot be combined with a name query or normalized."""
    if account_number is None:
        return True
    if (query != "" or not isinstance(account_number, str) or not account_number.strip()
            or len(account_number) > 40
            or any(ord(character) < 32 or 127 <= ord(character) <= 159 for character in account_number)):
        return False
    try:
        account_number.encode("utf-8")
    except UnicodeError:
        return False
    return True


def valid_arguments(definition: ToolDefinition, arguments: object) -> bool:
    """Check string lengths in characters without coercing or trimming values."""
    if not isinstance(arguments, dict) or not arguments.keys() <= definition.arguments.keys():
        return False
    for name, rule in definition.arguments.items():
        if name not in arguments:
            if rule.required:
                return False
            continue
        value = arguments[name]
        if value is None and rule.nullable:
            continue
        if (not isinstance(value, str) or len(value) > rule.max_length
                or (rule.required and not value.strip())):
            return False
    return (definition != TOOL_DEFINITIONS["search_accounts"]
            or valid_account_search(arguments.get("query", ""), arguments.get("account_number")))
