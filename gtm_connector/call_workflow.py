"""Associate an owner-selected transcript through the scoped CRM gateway."""

from copy import deepcopy
import hashlib
import re
import uuid

from .calls_import import CallsImportError
from .tool_definitions import valid_account_search


def resolve_call_account(runtime, *, account_id=None, account_number=None):
    """Resolve once under crm:read; later calls reads need no CRM authority."""
    from .client import LocalClient
    from .salesforce import SalesforceReadError, _ERROR_CODES
    import httpx2

    backend = runtime.crm_backend
    if getattr(runtime, "calls_backend", None) != "imported":
        raise CallsImportError("calls_source_mismatch")
    if (account_id is None) == (account_number is None):
        raise ValueError("Select exactly one --account or --account-number. Run gtm-connector calls --help.")
    if account_number is not None:
        if backend != "salesforce" or not valid_account_search("", account_number):
            raise ValueError("Use a valid --account-number with Salesforce. Run gtm-connector calls --help.")
        tool, arguments = "search_accounts", {"account_number": account_number}
    else:
        pattern = r"acc-sf-[A-Za-z0-9]{18}" if backend == "salesforce" else r"acc-[0-9]{3}"
        if not isinstance(account_id, str) or re.fullmatch(pattern, account_id) is None:
            raise ValueError("Use a canonical --account ID from accounts. Run gtm-connector calls --help.")
        tool, arguments = "get_account", {"account_id": account_id}
    correlation = "import-" + uuid.uuid4().hex
    try:
        with LocalClient(runtime.config) as client:
            token = client.authorize(["crm:read"])
            response = client.call(token, tool, arguments, brief_id=correlation)
    except httpx2.HTTPError:
        raise CallsImportError("calls_source_unavailable") from None
    if response.status_code != 200:
        raise CallsImportError("calls_association_failed")
    try:
        result = response.json()["result"]
        if result.get("isError"):
            content = result.get("content")
            if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                    and content[0].get("type") == "text"):
                message = content[0].get("text")
                if backend == "salesforce":
                    for code in _ERROR_CODES:
                        if message == f"Error executing tool {tool}: {code}":
                            if code == "salesforce_account_number_ambiguous":
                                raise CallsImportError("calls_account_ambiguous")
                            raise SalesforceReadError(code)
                if message == "Error executing tool get_account: account not found":
                    raise CallsImportError("calls_account_not_found")
            raise CallsImportError("calls_association_failed")
        data = result["structuredContent"]
        if (result.get("resultType") != "complete" or data["source"] != "crm"
                or data.get("untrusted_data") is not True):
            raise ValueError
        rows = data["records"]
        if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
            raise ValueError
        accounts, references = [], []
        for row in rows:
            identifier, reference = row["id"], row["record_ref"]
            table = next((name for name in ("accounts", "contacts", "contracts")
                          if reference == f"crm/{name}/{identifier}"), None)
            if table is None or not isinstance(identifier, str) or reference in references:
                raise ValueError
            if backend == "salesforce":
                prefix = {"accounts": "acc", "contacts": "con", "contracts": "ctr"}[table]
                native = row.get("source_id")
                if (row.get("backend") != "salesforce" or not isinstance(native, str)
                        or re.fullmatch(r"[A-Za-z0-9]{18}", native) is None
                        or identifier != f"{prefix}-sf-{native}"):
                    raise ValueError
            if table == "accounts":
                accounts.append(row)
            elif account_number is not None or row.get("account_id") != account_id:
                raise ValueError
            references.append(reference)
        receipts = runtime.connector.audit.read(correlation)
        if len(receipts) != 1:
            raise ValueError
        receipt = receipts[0]
        receipt_id = receipt["audit_id"]
        if (not isinstance(receipt_id, str) or re.fullmatch(r"[0-9a-f-]{36}", receipt_id) is None
                or receipt["brief_id"] != correlation or receipt["tool"] != tool
                or receipt["arguments"] != arguments or receipt["status"] != "ok"
                or receipt["http_status"] != 200 or type(receipt["row_count"]) is not int
                or receipt["row_count"] != len(references)
                or not isinstance(receipt["rows"], list) or sorted(receipt["rows"]) != sorted(references)
                or receipt["token_id"] != hashlib.sha256(token.encode()).hexdigest()):
            raise ValueError
        if account_number is not None:
            if any(row.get("account_number") != account_number for row in accounts) or len(accounts) > 2:
                raise ValueError
            if len(accounts) == 2:
                raise CallsImportError("calls_account_ambiguous")
        elif len(accounts) != 1 or accounts[0]["id"] != account_id:
            raise ValueError
        if not accounts:
            raise CallsImportError("calls_account_not_found")
        return {"crm_binding": deepcopy(runtime.calls_binding), "account_id": accounts[0]["id"],
                "method": "account_number" if account_number is not None else "account_id",
                "account_number": account_number, "resolution_receipt_id": receipt_id}
    except CallsImportError:
        raise
    except (KeyError, TypeError, ValueError, AttributeError, UnicodeError):
        raise CallsImportError("calls_association_invalid") from None
