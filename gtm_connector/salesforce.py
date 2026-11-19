"""Optional, bounded reads from an explicitly selected Salesforce environment."""

from contextlib import contextmanager
from datetime import date
from decimal import Decimal, DecimalException
import os
import re
import time
from typing import Any, cast
from urllib.parse import urlsplit

import httpx2

from . import strict_json
from .sf_credentials import SFCredentials, SFCredentialsError
from .tool_definitions import valid_account_search


API_VERSION = "v68.0"
_QUERY_PATH = f"/services/data/{API_VERSION}/query"
_ORGANIZATION_QUERY = "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1"
_ACCOUNT_FIELDS = "Id, Name, Industry, AccountNumber"
_CONTRACT_FIELDS = "Id, AccountId, Status, StatusCode, StartDate, ContractTerm, EndDate"
_CONTRACT_STATUSES = frozenset({"Draft", "InApproval", "Activated"})
_DESCRIBE_OBJECTS = frozenset({"Account", "Contact", "Opportunity", "Contract"})
_FIELD_NAME = re.compile(r"[A-Za-z][A-Za-z0-9_]{0,255}\Z")
_CONTACT_FIELDS = "Id, AccountId, Name, Title, Email"
_OPPORTUNITY_FIELDS = "Id, AccountId, Name, StageName, Amount, CloseDate, IsClosed, IsWon"
REQUIRED_FIELDS = {
    "Account": frozenset({"Id", "Name", "Industry", "AccountNumber"}),
    "Contact": frozenset({"Id", "AccountId", "Name", "Title", "Email"}),
    "Opportunity": frozenset({"Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"}),
    "Contract": frozenset({"Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"}),
}
_ENVIRONMENTS = frozenset({"developer", "sandbox", "production"})
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
    "salesforce_environment_mismatch", "salesforce_api_disabled", "salesforce_schema_unavailable",
    "salesforce_account_number_ambiguous",
})


class SalesforceReadError(RuntimeError):
    """A fixed diagnostic code; upstream messages and credentials stay private."""

    def __init__(self, code):
        self.code = code if code in _ERROR_CODES else "salesforce_invalid_response"
        self.sf_cli_invocations = 0
        super().__init__(self.code)


def _native_id(value):
    if not isinstance(value, str) or not _NATIVE_ID.fullmatch(value):
        raise SalesforceReadError("salesforce_invalid_response")
    return value


def _environment(value):
    if not isinstance(value, str) or value not in _ENVIRONMENTS:
        raise SalesforceReadError("salesforce_invalid_configuration")
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


def _selected_ids(account_ids, field):
    """A fixed IN clause from at most 100 validated canonical account IDs, or "" for no selection."""
    if account_ids is None:
        return ""
    identifiers = tuple(account_ids)
    if not 1 <= len(identifiers) <= 100:
        raise SalesforceReadError("salesforce_invalid_arguments")
    native = sorted({_account_id(value) for value in identifiers})
    return f"{field} IN (" + ", ".join(f"'{value}'" for value in native) + ")"


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

    Construction validates local configuration only. The first operation verifies
    the selected environment and, outside Developer Edition, the required schema.
    Successful metadata checks are cached for this pinned origin and lifetime.
    The optional transport supports invented HTTP fixtures, not live test data.
    """

    name = "salesforce"

    def __init__(self, instance_url, access_token, *, environment="developer", transport=None):
        self.environment = _environment(environment)
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
        self._credentials: SFCredentials | None = None
        self._edition_verified = False
        self._organization_id: str | None = None
        self._checked_objects: set[str] = set()
        self.http_requests = 0

    @classmethod
    def from_env(cls, *, environment="developer"):
        """Read only the explicitly named instance and credential variables."""
        return cls(os.environ.get("SALESFORCE_INSTANCE_URL"),
                   os.environ.get("SALESFORCE_ACCESS_TOKEN"), environment=environment)

    @classmethod
    def from_sf(cls, alias, *, environment="developer", transport=None):
        """Capture credentials from an existing CLI login inside this process."""
        _environment(environment)
        credentials = None
        try:
            credentials = SFCredentials(alias)
            origin, token = credentials.read(time.monotonic() + _OPERATION_SECONDS)
            adapter = cls(origin, token, environment=environment, transport=transport)
        except (SFCredentialsError, SalesforceReadError):
            error = SalesforceReadError("salesforce_auth_required")
            error.sf_cli_invocations = credentials.invocations if credentials is not None else 0
            raise error from None
        adapter._credentials = credentials
        return adapter

    @property
    def sf_cli_invocations(self):
        return self._credentials.invocations if self._credentials is not None else 0

    @staticmethod
    def _new_budget():
        return {"deadline": time.monotonic() + _OPERATION_SECONDS, "bytes": 0, "requests": 0}

    def _metadata_check_budget(self):
        """Check all startup metadata together without retaining partial capabilities."""
        self._checked_objects.clear()
        return {**self._new_budget(), "checked_objects": set()}

    def _refresh(self, client, budget):
        if self._credentials is None or budget.get("refreshed"):
            raise SalesforceReadError("salesforce_auth_required")
        budget["refreshed"] = True
        try:
            origin, token = self._credentials.read(budget["deadline"])
            validated = type(self)(origin, token, environment=self.environment)
            if validated.instance_url != self.instance_url:
                raise SalesforceReadError("salesforce_auth_required")
        except (SFCredentialsError, SalesforceReadError):
            raise SalesforceReadError("salesforce_auth_required") from None
        client.headers["Authorization"] = "Bearer " + validated._access_token
        return validated._access_token

    @contextmanager
    def _operation(self, *, require_capabilities=True, _budget=None):
        budget = self._new_budget() if _budget is None else _budget
        with httpx2.Client(
            verify=True, trust_env=False, follow_redirects=False, transport=self._transport,
            headers={"Authorization": "Bearer " + self._access_token, "Accept": "application/json",
                     "Accept-Encoding": "identity"},
            timeout=httpx2.Timeout(5, connect=3, write=5, pool=3),
        ) as client:
            if not self._edition_verified:
                organization = self._query(client, _ORGANIZATION_QUERY, budget, max_rows=1)
                self._validate_environment(organization)
                if "initial_refreshed_token" in budget:
                    self._access_token = budget.pop("initial_refreshed_token")
                self._edition_verified = True
            if require_capabilities and self.environment != "developer":
                self._check_capabilities(client, budget)
            yield client, budget

    def _validate_environment(self, organization):
        edition = organization[0].get("OrganizationType") if len(organization) == 1 else None
        if self.environment == "developer" and edition != "Developer Edition":
            raise SalesforceReadError("salesforce_edition_required")
        if (len(organization) != 1 or not isinstance(edition, str) or not 1 <= len(edition) <= 128
                or edition.strip() != edition or not edition.isprintable()):
            raise SalesforceReadError("salesforce_invalid_response")
        sandbox = organization[0].get("IsSandbox")
        organization_id = _native_id(organization[0].get("Id"))
        if type(sandbox) is not bool or not organization_id.startswith("00D"):
            raise SalesforceReadError("salesforce_invalid_response")
        if ((self.environment == "developer" and sandbox)
                or (self.environment == "sandbox" and not sandbox)
                or (self.environment == "production" and (sandbox or edition == "Developer Edition"))):
            raise SalesforceReadError("salesforce_environment_mismatch")
        if self._organization_id is not None and organization_id != self._organization_id:
            raise SalesforceReadError("salesforce_auth_required")
        self._organization_id = organization_id

    def _check_capabilities(self, client, budget):
        checked = set(self._checked_objects)
        for object_name, required in REQUIRED_FIELDS.items():
            if object_name in checked:
                continue
            fields = self._describe_fields(client, budget, object_name)
            if not required <= fields:
                raise SalesforceReadError("salesforce_schema_unavailable")
            checked.add(object_name)
        # Do not retain partial results from a failed preparation operation.
        self._checked_objects = checked

    def _request(self, client, path, budget, *, query=None):
        try:
            return self._request_once(client, path, budget, query=query)
        except SalesforceReadError as error:
            if error.code != "salesforce_auth_required":
                raise
        # The response is closed before invoking sf. Retry shares the original
        # deadline, request count and response-byte budget, including pagination.
        refreshed_token = self._refresh(client, budget)
        if self._edition_verified:
            try:
                organization = self._query(client, _ORGANIZATION_QUERY, budget, max_rows=1)
                self._validate_environment(organization)
            except SalesforceReadError as error:
                if error.code in ("salesforce_edition_required", "salesforce_environment_mismatch",
                                  "salesforce_invalid_response"):
                    raise SalesforceReadError("salesforce_auth_required") from None
                raise
            # This credential has proved the existing identity.
            self._access_token = refreshed_token
        else:
            # The initial Organization response still needs query and environment
            # validation in _operation before this credential may be retained.
            budget["initial_refreshed_token"] = refreshed_token
        return self._request_once(client, path, budget, query=query)

    def _request_once(self, client, path, budget, *, query=None):
        remaining = budget["deadline"] - time.monotonic()
        if remaining <= 0:
            raise SalesforceReadError("salesforce_timeout")
        if budget["bytes"] > _MAX_BYTES:
            raise SalesforceReadError("salesforce_result_limit")
        budget["requests"] += 1
        if budget["requests"] > _MAX_REQUESTS:
            raise SalesforceReadError("salesforce_result_limit")
        self.http_requests += 1
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
                    document = strict_json.loads(b"".join(chunks), parse_float=Decimal)
                except (ValueError, UnicodeError, RecursionError, DecimalException):
                    code = "salesforce_forbidden" if status == 403 else "salesforce_invalid_response"
                    raise SalesforceReadError(code) from None
                if time.monotonic() >= budget["deadline"]:
                    raise SalesforceReadError("salesforce_timeout")
                if status == 403:
                    codes = {item.get("errorCode") for item in document
                             if isinstance(item, dict) and isinstance(item.get("errorCode"), str)} \
                        if isinstance(document, list) else set()
                    if "REQUEST_LIMIT_EXCEEDED" in codes:
                        raise SalesforceReadError("salesforce_rate_limited")
                    if "API_DISABLED_FOR_ORG" in codes:
                        raise SalesforceReadError("salesforce_api_disabled")
                    raise SalesforceReadError("salesforce_forbidden")
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
                "name": _text(row, "Name"), "industry": _text(row, "Industry", nullable=True),
                "account_number": _text(row, "AccountNumber", nullable=True)}

    @staticmethod
    def _contact(row, account_id):
        native_id = _native_id(row.get("Id"))
        if _native_id(row.get("AccountId")) != account_id:
            raise SalesforceReadError("salesforce_invalid_response")
        return {"id": "con-sf-" + native_id, "source_id": native_id, "backend": "salesforce",
                "account_id": "acc-sf-" + account_id, "name": _text(row, "Name"),
                "title": _text(row, "Title", nullable=True), "email": _text(row, "Email", nullable=True)}

    @staticmethod
    def _contract(row, account_id):
        native_id = _native_id(row.get("Id"))
        if _native_id(row.get("AccountId")) != account_id:
            raise SalesforceReadError("salesforce_invalid_response")
        status_code = _text(row, "StatusCode")
        if status_code not in _CONTRACT_STATUSES:
            raise SalesforceReadError("salesforce_invalid_response")
        dates = {}
        for field in ("StartDate", "EndDate"):
            value = _text(row, field, nullable=True)
            if value is not None:
                try:
                    if date.fromisoformat(value).isoformat() != value:
                        raise ValueError
                except ValueError:
                    raise SalesforceReadError("salesforce_invalid_response") from None
            dates[field] = value
        term = row.get("ContractTerm")
        if ("ContractTerm" not in row or (term is not None
                and (type(term) is not int or not -(2 ** 31) <= term < 2 ** 31))):
            raise SalesforceReadError("salesforce_invalid_response")
        return {"id": "ctr-sf-" + native_id, "source_id": native_id, "backend": "salesforce",
                "account_id": "acc-sf-" + account_id, "status": _text(row, "Status"),
                "status_code": status_code, "start_date": dates["StartDate"], "end_date": dates["EndDate"],
                "contract_term": term}

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

    def search_accounts(self, query="", account_number=None, *, account_ids=None):
        if not valid_account_search(query, account_number):
            raise SalesforceReadError("salesforce_invalid_arguments")
        selected = _selected_ids(account_ids, "Id")
        if account_number is not None:
            escaped = account_number.replace("\\", "\\\\").replace("'", "\\'")
            where, limit, max_rows = f" WHERE AccountNumber = '{escaped}'", 2, 2
        else:
            escaped = _like_literal(query)
            where, limit, max_rows = (f" WHERE Name LIKE '%{escaped}%'" if query else ""), 101, _MAX_ROWS
        if selected:
            where += (" AND " if where else " WHERE ") + selected
        with self._operation() as (client, budget):
            rows = self._query(client, f"SELECT {_ACCOUNT_FIELDS} FROM Account{where} ORDER BY Id LIMIT {limit}",
                               budget, max_rows=max_rows)
            accounts = [self._account(row) for row in rows]
            if account_ids is not None and any(row["id"] not in account_ids for row in accounts):
                raise SalesforceReadError("salesforce_invalid_response")
            if account_number is not None:
                if any(row["account_number"] is None for row in accounts):
                    raise SalesforceReadError("salesforce_invalid_response")
                if any(row["account_number"] != account_number for row in accounts):
                    raise SalesforceReadError("salesforce_account_number_ambiguous")
            return accounts

    def probe_accounts(self):
        """Read at most one Account for an owner-selected setup check."""
        with self._operation() as (client, budget):
            rows = self._query(client, f"SELECT {_ACCOUNT_FIELDS} FROM Account ORDER BY Id LIMIT 1",
                               budget, max_rows=1)
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
            contract_query = (f"SELECT {_CONTRACT_FIELDS} FROM Contract "
                              f"WHERE AccountId = '{native_id}' ORDER BY Id LIMIT 101")
            contracts = self._query(client, contract_query, budget)
            account["contracts"] = [self._contract(row, native_id) for row in contracts]
            return account

    def list_opportunities(self, account_id=None, *, account_ids=None):
        native_id = _account_id(account_id) if account_id is not None else None
        where = f" WHERE AccountId = '{native_id}'" if native_id is not None else ""
        if native_id is None and account_ids is not None:
            where = " WHERE " + _selected_ids(account_ids, "AccountId")
        with self._operation() as (client, budget):
            rows = self._query(
                client, f"SELECT {_OPPORTUNITY_FIELDS} FROM Opportunity{where} ORDER BY Id LIMIT 101", budget)
            opportunities = [self._opportunity(row, native_id) for row in rows]
            if (native_id is None and account_ids is not None
                    and any(row["account_id"] not in account_ids for row in opportunities)):
                raise SalesforceReadError("salesforce_invalid_response")
            return opportunities

    def check_edition(self, *, _budget=None):
        """Validate the selected environment without the capability checks."""
        with self._operation(require_capabilities=False, _budget=_budget):
            pass

    def prepare_connection(self):
        """Return the private identity only after bounded metadata checks pass."""
        with self._operation():
            return {"schema_version": 1, "backend": "salesforce", "instance_url": self.instance_url,
                    "organization_id": self._organization_id, "environment": self.environment}

    def _describe_fields(self, client, budget, object_name):
        document = self._request(client, f"/services/data/{API_VERSION}/sobjects/{object_name}/describe", budget)
        if not isinstance(document, dict) or not isinstance(document.get("fields"), list):
            raise SalesforceReadError("salesforce_invalid_response")
        if self.environment != "developer":
            if document.get("name") != object_name or type(document.get("queryable")) is not bool:
                raise SalesforceReadError("salesforce_invalid_response")
            if not document["queryable"]:
                raise SalesforceReadError("salesforce_forbidden")
        names = set()
        for field in document["fields"]:
            name = field.get("name") if isinstance(field, dict) else None
            if not isinstance(name, str) or not _FIELD_NAME.fullmatch(name) or name in names:
                raise SalesforceReadError("salesforce_invalid_response")
            names.add(name)
        return names

    def describe_fields(self, object_name, *, _budget=None):
        """Return field names for one fixed object without exposing its schema."""
        if not isinstance(object_name, str) or object_name not in _DESCRIBE_OBJECTS:
            raise SalesforceReadError("salesforce_invalid_arguments")
        self._checked_objects.discard(object_name)
        with self._operation(require_capabilities=False, _budget=_budget) as (client, budget):
            names = self._describe_fields(client, budget, object_name)
        if self.environment != "developer" and REQUIRED_FIELDS[object_name] <= names:
            if _budget is None:
                self._checked_objects.add(object_name)
            else:
                checked = _budget["checked_objects"]
                checked.add(object_name)
                if REQUIRED_FIELDS.keys() <= checked:
                    self._checked_objects.update(checked)
        return names

    def candidate_account(self, as_of):
        """Select the earliest standard Contract renewal candidate privately."""
        try:
            if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
                raise ValueError
        except ValueError:
            raise SalesforceReadError("salesforce_invalid_arguments") from None
        query = (f"SELECT {_CONTRACT_FIELDS} FROM Contract WHERE EndDate >= {as_of} "
                 "AND StatusCode IN ('Draft', 'InApproval', 'Activated') ORDER BY EndDate, Id LIMIT 1")
        with self._operation() as (client, budget):
            rows = self._query(client, query, budget, max_rows=1)
        if not rows:
            return None
        account_id = _native_id(rows[0].get("AccountId"))
        contract = self._contract(rows[0], account_id)
        if contract["end_date"] is None or contract["end_date"] < as_of:
            raise SalesforceReadError("salesforce_invalid_response")
        return "acc-sf-" + account_id
