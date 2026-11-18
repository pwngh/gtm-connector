"""Run owner-selected Salesforce GET checks and release only bounded diagnostics."""

from datetime import date
from pathlib import Path
from tempfile import TemporaryDirectory
import time
from typing import Any

from .salesforce import REQUIRED_FIELDS as REQUIRED_FIELDS, SalesforceReadOnlyAdapter


_FAILURES = {
    "salesforce_invalid_configuration": (
        "Salesforce credentials or the sf CLI configuration are unavailable.",
        "Run sf org login web --alias gtm-de, then repeat with --sf-org gtm-de."),
    "salesforce_edition_required": (
        "The selected org did not report Developer Edition.",
        "Select a Developer Edition alias with --sf-org."),
    "salesforce_environment_mismatch": (
        "The selected org does not match the expected Salesforce environment.",
        "Check the selected alias and repeat with the intended --sf-environment."),
    "salesforce_api_disabled": (
        "Salesforce reports that API access is disabled for this org.",
        "Use an existing API-enabled org and a user with the API Enabled permission."),
    "salesforce_schema_unavailable": (
        "The selected org does not expose all required standard fields.",
        "Run gtm-connector salesforce check with the same alias and environment to see the missing fields."),
    "salesforce_auth_required": (
        "The sf CLI login or token refresh did not provide usable credentials.",
        "Install Salesforce CLI if missing. Run sf org login web --alias gtm-de, then repeat with --sf-org gtm-de."),
    "salesforce_forbidden": (
        "Salesforce denied this read.",
        "Grant the selected user read access to the required standard objects and fields."),
    "salesforce_rate_limited": (
        "Salesforce refused a request because its API limit was reached.",
        "Wait for API capacity, then repeat the check."),
    "salesforce_result_limit": (
        "The read exceeded the connector's request, row, or response-size bound.",
        "Select a smaller account book or narrower search, then repeat the check."),
    "salesforce_timeout": (
        "The Salesforce read or credential refresh exceeded its time limit.",
        "Check connectivity and sf login, then repeat the check."),
    "salesforce_unavailable": (
        "The Salesforce endpoint could not be reached.",
        "Check connectivity and sf login, then repeat the check."),
    "salesforce_http_error": (
        "Salesforce did not accept the required GET request.",
        "Check that the required standard object is available and readable in this org."),
    "salesforce_invalid_response": (
        "Salesforce returned data outside the supported response shape.",
        "Check access to the listed standard fields, then repeat the check."),
    "salesforce_pagination_invalid": (
        "Salesforce returned an unsupported result-page locator.",
        "Repeat the check on a smaller account book."),
    "salesforce_invalid_arguments": (
        "A read could not use the returned account or date.",
        "Run gtm-connector salesforce check --help and repeat with a valid date."),
    "renewal_fields_unmapped": (
        "No eligible Contract has an EndDate on or after the planning date.",
        "Create a Draft Contract with StartDate and ContractTerm; verify its EndDate and read access."),
    "check_failed": (
        "The check could not finish with the available data or local setup.",
        "Run the repository tests and check local HTTPS setup, then repeat this check."),
}


class MissingFields(Exception):
    def __init__(self, object_name, fields):
        self.object_name, self.fields = object_name, fields


class NoCandidate(Exception):
    code = "renewal_fields_unmapped"


def run_check(*, sf_org=None, as_of=None, environment="developer", adapter_factory=None, progress=None):
    """Never return records, configuration, credentials, exception text, or identifiers."""
    as_of = date.today().isoformat() if as_of is None else as_of
    try:
        if not isinstance(as_of, str) or date.fromisoformat(as_of).isoformat() != as_of:
            raise ValueError
    except (TypeError, ValueError):
        raise ValueError("The planning date must use YYYY-MM-DD.") from None
    if not isinstance(environment, str) or environment not in ("developer", "sandbox", "production"):
        raise ValueError("The Salesforce environment must be developer, sandbox, or production.")

    def configured_adapter():
        # Preserve the earlier default factory call for existing Python callers.
        options = {} if environment == "developer" else {"environment": environment}
        return (SalesforceReadOnlyAdapter.from_sf(sf_org, **options) if sf_org is not None
                else SalesforceReadOnlyAdapter.from_env(**options))

    factory = adapter_factory or configured_adapter
    state: dict[str, Any] = {}
    steps: list[dict[str, Any]] = []

    def counters():
        reader = state.get("reader")
        return (getattr(reader, "http_requests", 0), getattr(reader, "sf_cli_invocations", 0))

    def step(name, operation, *, ready=True):
        before_api, before_sf = counters()
        started = time.perf_counter()
        row = {"name": name, "status": "fail", "count": 0, "code": "prerequisite_failed",
               "message": "Not run because a required earlier step failed.",
               "fix": "Apply the fix for the earlier failed step, then repeat the check."}
        extra_sf = 0
        if ready:
            if progress is not None:
                progress(name, "running", None)
            try:
                count, message = operation()
                row.update(status="pass", count=count, code="ok", message=message, fix="")
            except MissingFields as exc:
                # Both the object and missing names come from our fixed allowlist.
                row.update(code="missing_fields", message=f"{exc.object_name} does not expose required fields: "
                           + ", ".join(sorted(exc.fields)) + ".",
                           fix="Grant read access to these standard fields, then repeat the check.")
            except Exception as exc:
                code = getattr(exc, "code", "check_failed")
                code = code if code in _FAILURES else "check_failed"
                message, fix = _FAILURES[code]
                row.update(code=code, message=message, fix=fix)
                if "reader" not in state:
                    extra_sf = getattr(exc, "sf_cli_invocations", 0)
        after_api, after_sf = counters()
        row.update(duration_ms=round((time.perf_counter() - started) * 1000, 3),
                   api_calls=after_api - before_api, sf_cli_invocations=after_sf - before_sf + extra_sf)
        steps.append(row)
        if progress is not None:
            progress(name, row["status"], row["count"])
        return row["status"] == "pass"

    def credentials():
        state["reader"] = factory()
        if state["reader"].environment != environment:
            raise ValueError("The reader does not match the selected environment.")
        return 2, "Instance and token were loaded in memory."

    credential_ok = step("credentials", credentials)

    def edition():
        state["reader"].check_edition()
        return 1, ("Developer Edition was confirmed." if environment == "developer"
                   else f"The selected {environment} environment was confirmed.")

    edition_ok = step("edition" if environment == "developer" else "environment", edition, ready=credential_ok)
    schema_ok = edition_ok
    for object_name, fields in REQUIRED_FIELDS.items():
        def schema(name=object_name, required=fields):
            missing = required - state["reader"].describe_fields(name)
            if missing:
                raise MissingFields(name, missing)
            return len(required), f"Required {name} fields are exposed by describe."
        passed = step(object_name.lower() + "_fields", schema, ready=edition_ok)
        schema_ok = schema_ok and passed

    def search():
        if environment != "developer":
            rows = state["reader"].probe_accounts()
            return len(rows), "The one-account read probe completed. Its count is a sample, not the account book size."
        rows = state["reader"].search_accounts("")
        return len(rows), "The bounded Account search completed."

    step("account_search", search, ready=schema_ok)

    def candidate():
        state["account_id"] = state["reader"].candidate_account(as_of)
        if state["account_id"] is None:
            raise NoCandidate
        return 1, "A dated renewal-planning candidate was found."

    candidate_ok = step("renewal_candidate", candidate, ready=schema_ok)

    def account():
        value = state["reader"].get_account(state["account_id"])
        if value is None:
            raise NoCandidate
        return 1 + len(value["contacts"]) + len(value["contracts"]), "Account, Contact, and Contract reads completed."

    account_ok = step("account_read", account, ready=candidate_ok)

    def opportunities():
        rows = state["reader"].list_opportunities(state["account_id"])
        return len(rows), "The bounded Opportunity read completed."

    opportunity_ok = step("opportunity_read", opportunities, ready=candidate_ok)

    def brief():
        from .briefing import create_brief
        from .runtime import Runtime
        with TemporaryDirectory(prefix="gtm-salesforce-check-") as temporary:
            with Runtime(Path(temporary) / "state", crm_reader=state["reader"]) as runtime:
                result = create_brief(runtime, state["account_id"], as_of=as_of)
                count = len([line for line in result["text"].splitlines() if line.startswith("- ")])
        return count, "A cited brief passed its evidence check. Private temporary evidence was removed."

    step("brief", brief, ready=account_ok and opportunity_ok)
    return {"schema_version": 1, "status": "passed" if all(row["status"] == "pass" for row in steps) else "failed",
            "as_of": as_of, "steps": steps, "api_calls": sum(row["api_calls"] for row in steps),
            "sf_cli_invocations": sum(row["sf_cli_invocations"] for row in steps),
            "api_call_definition": "Connector REST GET attempts, including retries. sf CLI internal HTTP requests "
                                   "and other org activity are not counted.",
            "sf_cli_definition": "Attempted sf subprocess invocations, including failed launches."}
