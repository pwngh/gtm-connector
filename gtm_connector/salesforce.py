"""Optional, bounded Salesforce Developer Edition reads. No external writes."""

from contextlib import contextmanager
from datetime import date
from decimal import Decimal, DecimalException
import json
import os
import re
import time
from typing import Any, cast
from urllib.parse import urlsplit

import httpx2


API_VERSION = "v68.0"
_QUERY_PATH = f"/services/data/{API_VERSION}/query"
_ORGANIZATION_QUERY = "SELECT Id, OrganizationType FROM Organization LIMIT 1"
_ACCOUNT_FIELDS = "Id, Name, Industry"
_CONTACT_FIELDS = "Id, AccountId, Name, Title, Email"
_OPPORTUNITY_FIELDS = "Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, IsWon"
_NATIVE_ID = re.compile(r"[A-Za-z0-9]{18}\Z")
_ACCOUNT_ID = re.compile(r"acc-sf-([A-Za-z0-9]{18})\Z")
_HOST = re.compile(r"(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+my\.salesforce\.com\Z")
_LOCATOR = re.compile(re.escape(_QUERY_PATH) + r"/[A-Za-z0-9_-]{1,128}\Z")
_MAX_ROWS = 100
_MAX_PAGES = 10
_MAX_REQUESTS = 12
_MAX_BYTES = 1024 * 1024
_OPERATION_SECONDS = 20
_ERROR_CODES = frozenset({
    "salesforce_invalid_configuration", "salesforce_invalid_arguments",
    "salesforce_edition_required", "salesforce_auth_required",
    "salesforce_rate_limited", "salesforce_forbidden", "salesforce_http_error",
    "salesforce_unavailable", "salesforce_timeout", "salesforce_invalid_response",
    "salesforce_result_limit", "salesforce_pagination_invalid",
})


class SalesforceReadError(RuntimeError):
    """A fixed diagnostic code; upstream messages and credentials stay private."""

    def __init__(self, code):
        self.code = code if code in _ERROR_CODES else "salesforce_invalid_response"
        super().__init__(self.code)


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate_json_key")
        result[key] = value
    return result


def _reject_constant(_):
    raise ValueError("nonfinite_json_number")


def _native_id(value):
    if not isinstance(value, str) or not _NATIVE_ID.fullmatch(value):
        raise SalesforceReadError("salesforce_invalid_response")
    return value


def _text(row, field, *, nullable=False):
    if field not in row or not (isinstance(row[field], str) or (nullable and row[field] is None)):
        raise SalesforceReadError("salesforce_invalid_response")
    if isinstance(row[field], str):
        try:
            row[field].encode("utf-8")
        except UnicodeError:
            raise SalesforceReadError("salesforce_invalid_response") from None
    return row[field]


def _account_id(value):
    matched = _ACCOUNT_ID.fullmatch(value) if isinstance(value, str) else None
    if matched is None:
        raise SalesforceReadError("salesforce_invalid_arguments")
    return matched[1]


def _like_literal(value):
    if (not isinstance(value, str) or len(value) > 200
            or any(ord(character) < 32 or ord(character) == 127 for character in value)):
        raise SalesforceReadError("salesforce_invalid_arguments")
    try:
        value.encode("utf-8")
    except UnicodeError:
        raise SalesforceReadError("salesforce_invalid_arguments") from None
    escapes = {"\\": "\\\\", "'": "\\'", "%": "\\%", "_": "\\_"}
    return "".join(escapes.get(character, character) for character in value)


class SalesforceReadOnlyAdapter:
    """Three fixed read operations over one explicitly configured HTTPS origin.

    Construction validates local configuration only. Every operation verifies
    Developer Edition again before reading Account, Contact, or Opportunity.
    The optional transport supports invented HTTP fixtures, not live test data.
    """

    name = "salesforce"

    def __init__(self, instance_url, access_token, *, transport=None):
        try:
            if not isinstance(instance_url, str) or len(instance_url) > 512:
                raise ValueError
            parts = urlsplit(instance_url)
            host = parts.hostname or ""
            if (parts.scheme != "https" or not _HOST.fullmatch(host) or len(host) > 253
                    or parts.username is not None or parts.password is not None
                    or parts.path or parts.query or parts.fragment
                    or parts.port not in (None, 443)
                    or parts.netloc.lower() not in (host, host + ":443")
                    or instance_url.lower() not in ("https://" + host, "https://" + host + ":443")
                    or any(ord(character) <= 32 for character in instance_url)):
                raise ValueError
            if (not isinstance(access_token, str) or not 1 <= len(access_token) <= 4096
                    or any(not 33 <= ord(character) <= 126 for character in access_token)):
                raise ValueError
        except (ValueError, UnicodeError):
            raise SalesforceReadError("salesforce_invalid_configuration") from None
        self.instance_url = "https://" + host
        self._access_token = access_token
        self._transport = transport

    @classmethod
    def from_env(cls):
        """Read only the explicitly named instance and credential variables."""
        return cls(os.environ.get("SALESFORCE_INSTANCE_URL"),
                   os.environ.get("SALESFORCE_ACCESS_TOKEN"))

    @contextmanager
    def _operation(self):
        budget = {"deadline": time.monotonic() + _OPERATION_SECONDS, "bytes": 0, "requests": 0}
        with httpx2.Client(
            verify=True, trust_env=False, follow_redirects=False, transport=self._transport,
            headers={"Authorization": "Bearer " + self._access_token, "Accept": "application/json",
                     "Accept-Encoding": "identity"},
            timeout=httpx2.Timeout(5, connect=3, write=5, pool=3),
        ) as client:
            organization = self._query(client, _ORGANIZATION_QUERY, budget, max_rows=1)
            if (len(organization) != 1
                    or organization[0].get("OrganizationType") != "Developer Edition"):
                raise SalesforceReadError("salesforce_edition_required")
            yield client, budget

    def _request(self, client, path, budget, *, query=None):
        remaining = budget["deadline"] - time.monotonic()
        if remaining <= 0:
            raise SalesforceReadError("salesforce_timeout")
        budget["requests"] += 1
        if budget["requests"] > _MAX_REQUESTS:
            raise SalesforceReadError("salesforce_result_limit")
        try:
            with client.stream("GET", self.instance_url + path,
                               params={"q": query} if query is not None else None,
                               timeout=httpx2.Timeout(min(5, remaining), connect=min(3, remaining),
                                                      write=min(5, remaining), pool=min(3, remaining))) as response:
                status = response.status_code
                if status == 401:
                    raise SalesforceReadError("salesforce_auth_required")
                if status == 429:
                    raise SalesforceReadError("salesforce_rate_limited")
                if status not in (200, 403):
                    raise SalesforceReadError("salesforce_http_error")
                if response.headers.get("content-encoding", "identity").lower() != "identity":
                    raise SalesforceReadError("salesforce_invalid_response")
                chunks = []
                for chunk in response.iter_bytes():
                    budget["bytes"] += len(chunk)
                    if budget["bytes"] > _MAX_BYTES:
                        raise SalesforceReadError("salesforce_result_limit")
                    if time.monotonic() >= budget["deadline"]:
                        raise SalesforceReadError("salesforce_timeout")
                    chunks.append(chunk)
                try:
                    document = json.loads(b"".join(chunks), parse_float=Decimal,
                                          parse_constant=_reject_constant,
                                          object_pairs_hook=_unique_object)
                except (ValueError, UnicodeError, RecursionError, DecimalException):
                    code = "salesforce_forbidden" if status == 403 else "salesforce_invalid_response"
                    raise SalesforceReadError(code) from None
                if time.monotonic() >= budget["deadline"]:
                    raise SalesforceReadError("salesforce_timeout")
                if status == 403:
                    limited = isinstance(document, list) and any(
                        isinstance(item, dict) and item.get("errorCode") == "REQUEST_LIMIT_EXCEEDED"
                        for item in document)
                    raise SalesforceReadError("salesforce_rate_limited" if limited else "salesforce_forbidden")
                return document
        except httpx2.TimeoutException:
            raise SalesforceReadError("salesforce_timeout") from None
        except httpx2.HTTPError:
            raise SalesforceReadError("salesforce_unavailable") from None

    def _query(self, client, query, budget, *, max_rows=_MAX_ROWS):
        rows: list[dict[str, Any]] = []
        seen_ids, seen_locators = set(), set()
        path, total = _QUERY_PATH, None
        for page in range(_MAX_PAGES):
            document = self._request(client, path, budget, query=query if page == 0 else None)
            if (not isinstance(document, dict) or type(document.get("done")) is not bool
                    or type(document.get("totalSize")) is not int or document["totalSize"] < 0
                    or not isinstance(document.get("records"), list)):
                raise SalesforceReadError("salesforce_invalid_response")
            if total is None:
                total = document["totalSize"]
            if document["totalSize"] != total:
                raise SalesforceReadError("salesforce_invalid_response")
            if total > max_rows or len(rows) + len(document["records"]) > max_rows:
                raise SalesforceReadError("salesforce_result_limit")
            for row in document["records"]:
                if not isinstance(row, dict):
                    raise SalesforceReadError("salesforce_invalid_response")
                native_id = _native_id(row.get("Id"))
                if native_id in seen_ids:
                    raise SalesforceReadError("salesforce_invalid_response")
                seen_ids.add(native_id)
                rows.append(row)
            if document["done"]:
                if len(rows) != total or document.get("nextRecordsUrl") is not None:
                    raise SalesforceReadError("salesforce_invalid_response")
                return rows
            locator = document.get("nextRecordsUrl")
            if (not isinstance(locator, str) or not _LOCATOR.fullmatch(locator)
                    or locator in seen_locators or not document["records"] or len(rows) >= total):
                raise SalesforceReadError("salesforce_pagination_invalid")
            seen_locators.add(locator)
            path = locator
        raise SalesforceReadError("salesforce_result_limit")

    @staticmethod
    def _account(row):
        native_id = _native_id(row.get("Id"))
        return {"id": "acc-sf-" + native_id, "source_id": native_id, "backend": "salesforce",
                "name": _text(row, "Name"), "industry": _text(row, "Industry", nullable=True)}

    @staticmethod
    def _contact(row, account_id):
        native_id = _native_id(row.get("Id"))
        if _native_id(row.get("AccountId")) != account_id:
            raise SalesforceReadError("salesforce_invalid_response")
        return {"id": "con-sf-" + native_id, "source_id": native_id, "backend": "salesforce",
                "account_id": "acc-sf-" + account_id, "name": _text(row, "Name"),
                "title": _text(row, "Title", nullable=True), "email": _text(row, "Email", nullable=True)}

    @staticmethod
    def _opportunity(row, account_id):
        native_id = _native_id(row.get("Id"))
        if "AccountId" not in row:
            raise SalesforceReadError("salesforce_invalid_response")
        related_id = _native_id(row["AccountId"]) if row["AccountId"] is not None else None
        if account_id is not None and related_id != account_id:
            raise SalesforceReadError("salesforce_invalid_response")
        amount = row.get("Amount")
        if "Amount" not in row or (amount is not None and type(amount) not in (int, Decimal)):
            raise SalesforceReadError("salesforce_invalid_response")
        if amount is not None:
            amount = Decimal(amount)
            # The finite check short-circuits before the exponent, which is then an int.
            if (not amount.is_finite() or len(amount.as_tuple().digits) > 28
                    or abs(cast(int, amount.as_tuple().exponent)) > 28):
                raise SalesforceReadError("salesforce_invalid_response")
        close_date = _text(row, "CloseDate")
        try:
            if date.fromisoformat(close_date).isoformat() != close_date:
                raise ValueError
        except ValueError:
            raise SalesforceReadError("salesforce_invalid_response") from None
        if type(row.get("IsClosed")) is not bool or type(row.get("IsWon")) is not bool:
            raise SalesforceReadError("salesforce_invalid_response")
        return {"id": "opp-sf-" + native_id, "source_id": native_id, "backend": "salesforce",
                "account_id": "acc-sf-" + related_id if related_id is not None else None,
                "name": _text(row, "Name"), "stage": _text(row, "StageName"),
                "close_date": close_date, "is_closed": row["IsClosed"], "is_won": row["IsWon"],
                "amount_decimal": format(amount, "f") if amount is not None else None, "currency": None}

    def search_accounts(self, query=""):
        escaped = _like_literal(query)
        where = f" WHERE Name LIKE '%{escaped}%'" if query else ""
        with self._operation() as (client, budget):
            rows = self._query(client, f"SELECT {_ACCOUNT_FIELDS} FROM Account{where} ORDER BY Id LIMIT 101", budget)
            return [self._account(row) for row in rows]

    def get_account(self, account_id):
        native_id = _account_id(account_id)
        with self._operation() as (client, budget):
            rows = self._query(
                client, f"SELECT {_ACCOUNT_FIELDS} FROM Account WHERE Id = '{native_id}' ORDER BY Id LIMIT 101",
                budget, max_rows=1)
            if not rows:
                return None
            if rows[0]["Id"] != native_id:
                raise SalesforceReadError("salesforce_invalid_response")
            account = self._account(rows[0])
            contacts = self._query(
                client, f"SELECT {_CONTACT_FIELDS} FROM Contact WHERE AccountId = '{native_id}' ORDER BY Id LIMIT 101",
                budget)
            account["contacts"] = [self._contact(row, native_id) for row in contacts]
            return account

    def list_opportunities(self, account_id=None):
        native_id = _account_id(account_id) if account_id is not None else None
        where = f" WHERE AccountId = '{native_id}'" if native_id is not None else ""
        with self._operation() as (client, budget):
            rows = self._query(
                client, f"SELECT {_OPPORTUNITY_FIELDS} FROM Opportunity{where} ORDER BY Id LIMIT 101", budget)
            return [self._opportunity(row, native_id) for row in rows]
