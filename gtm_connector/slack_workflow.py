"""Resolve owner selections and report one audited Slack read without source text."""

from copy import deepcopy
import hashlib
import math
import re
import time
import uuid


_MAP_HELP = "gtm-connector slack map --help"
_CHECK_HELP = "gtm-connector slack check --help"
_STATUS_HELP = "gtm-connector slack status --help"
_CONNECT_HELP = "gtm-connector slack connect --help"
_DOCTOR_HELP = "gtm-connector doctor --help"
_DIAGNOSTICS = {
    "slack_selection_invalid": ("The account or thread selection is invalid. Check the account and link, "
                                "or use --channel with --thread-ts.",
                                _MAP_HELP),
    "slack_mapping_missing": ("No thread is selected for this account. Save a thread selection first.", _MAP_HELP),
    "slack_mapping_conflict": ("The selection conflicts with a saved mapping. Review --replace and --reassign.",
                               _MAP_HELP),
    "slack_mapping_invalid": ("The saved mapping is invalid. Preserve the private state and inspect its setup.",
                              _MAP_HELP),
    "slack_source_mismatch": ("The source identity does not match the saved selection. Restore the original setup.",
                              _MAP_HELP),
    "slack_identity_unchecked": ("The saved Slack identity needs verification. Run slack status --check first.",
                                 _STATUS_HELP),
    "slack_account_not_found": ("CRM did not return the selected account. Find its canonical account ID.",
                                "gtm-connector accounts --help"),
    "slack_account_ambiguous": ("CRM could not resolve one literal account match. Use its canonical --account ID.",
                                _MAP_HELP),
    "slack_association_failed": ("The audited CRM lookup did not succeed. Check the selected CRM connection.",
                                 _MAP_HELP),
    "slack_association_invalid": ("The CRM result or its receipt did not validate. No selection was saved.", _MAP_HELP),
    "slack_crm_unavailable": ("The CRM lookup could not finish. Check CRM access before selecting a thread.",
                              "gtm-connector salesforce check --help"),
    "slack_crm_configuration_invalid": ("The CRM connection options are invalid. Correct the selected connection.",
                                        _MAP_HELP),
    "slack_snapshot_invalid": ("The Slack snapshot failed its consistency checks. Preserve it and repeat the read.",
                               _CHECK_HELP),
    "slack_thread_unavailable": ("The selected thread was not accessible. Check its parent selection and visibility.",
                                 _MAP_HELP),
    "slack_forbidden": ("Slack did not permit the selected thread read. Check the history grant and thread access.",
                         _CONNECT_HELP),
    "slack_thread_changed": ("Repeated messages disagreed during paging. Retry the read when the thread is stable.",
                              _CHECK_HELP),
    "slack_thread_incomplete": ("The selected thread could not be read completely. Check its parent and retry.",
                                _MAP_HELP),
    "slack_resource_limit": ("The read reached its request, row or byte bound. Select a smaller thread.", _MAP_HELP),
    "slack_output_too_large": ("The thread text exceeds the source-output bound. Select a smaller thread.", _MAP_HELP),
    "slack_clock_invalid": ("The local clock moved backward. Correct the clock before retrying.", _CHECK_HELP),
    "slack_response_invalid": ("Slack returned an unsupported response. Retry or select a supported text thread.",
                                _CHECK_HELP),
    "slack_rate_limited": ("Slack's rate limit could not be retried within the read budget. Wait before retrying.",
                            _CHECK_HELP),
    "slack_timeout": ("The Slack operation exceeded its deadline. Inspect authentication and retry.", _STATUS_HELP),
    "slack_unavailable": ("Slack could not be reached. Retry when the connection is available.", _CHECK_HELP),
    "slack_reconnect_required": ("Saved Slack authentication is unusable. Browser authorization is required.",
                                  _CONNECT_HELP),
    "slack_not_connected": ("No usable Slack connection is saved. Authorize an existing public app.", _CONNECT_HELP),
    "slack_invalid_configuration": ("The Slack connection options are invalid. Check the registered app setup.",
                                     _CONNECT_HELP),
    "slack_oauth_rejected": ("Slack rejected the authorization request. Check the existing app and its approval.",
                             _CONNECT_HELP),
    "slack_connection_busy": ("Another command holds the Slack connection. Finish or cancel it before retrying.",
                               _STATUS_HELP),
    "slack_private_state_invalid": ("The Slack credential folder is unsafe. Use the original private folder.",
                                     _STATUS_HELP),
    "slack_credentials_invalid": ("The saved Slack credentials are invalid. Preserve them and reconnect separately.",
                                   _CONNECT_HELP),
    "slack_identity_mismatch": ("The Slack identity changed. Restore the identity selected for this source.",
                                _MAP_HELP),
    "slack_scope_mismatch": ("The Slack grant does not match the selected history scopes. Authorize them explicitly.",
                              _CONNECT_HELP),
    "slack_token_type_unsupported": ("The Slack credential is not a supported user grant. Authorize a user grant.",
                                      _CONNECT_HELP),
    "slack_storage_failed": ("Private Slack state could not be read or saved. Correct local storage access.",
                             _DOCTOR_HELP),
    "slack_check_failed": ("The Slack check could not validate a complete result. Inspect setup before retrying.",
                            _CHECK_HELP),
    "slack_local_gateway_failed": ("The local check connection failed. Inspect local HTTPS setup before retrying.",
                                    _DOCTOR_HELP),
    "slack_metrics_unavailable": ("The check could not verify source-work counts. No successful check is reported.",
                                   _CHECK_HELP),
    "slack_receipt_invalid": ("The tool attempt has no matching final receipt. No successful check is reported.",
                               _DOCTOR_HELP),
    "prerequisite_failed": ("This step could not finish because an earlier required step failed.", ""),
}


def slack_failure_details(code):
    """Return fixed public wording, never provider text or a supplied value."""
    return _DIAGNOSTICS.get(code, _DIAGNOSTICS["slack_check_failed"])


def resolve_slack_account(runtime, *, account_id=None, account_number=None):
    """Project one audited CRM association without exposing the private selector."""
    from .call_workflow import CRMResolutionError, resolve_crm_account
    from .salesforce import SalesforceReadError
    from .slack_mapping import SlackMappingError

    binding = getattr(runtime, "slack_binding", None)
    if (getattr(runtime, "slack_backend", None) != "slack" or not isinstance(binding, dict)
            or set(binding) != {"schema_version", "provider", "identity_sha256"}
            or type(binding["schema_version"]) is not int or binding["schema_version"] != 1
            or binding["provider"] != "slack" or not isinstance(binding["identity_sha256"], str)
            or re.fullmatch(r"[0-9a-f]{64}", binding["identity_sha256"]) is None):
        raise SlackMappingError("slack_source_mismatch")
    neutral = {"crm_selector_required": "slack_selection_invalid",
               "crm_account_number_invalid": "slack_selection_invalid",
               "crm_account_id_invalid": "slack_selection_invalid", "crm_source_mismatch": "slack_source_mismatch",
               "crm_account_not_found": "slack_account_not_found", "crm_account_ambiguous": "slack_account_ambiguous",
               "crm_association_failed": "slack_association_failed",
               "crm_association_invalid": "slack_association_invalid",
               "crm_source_unavailable": "slack_crm_unavailable"}
    try:
        association = resolve_crm_account(runtime, account_id=account_id, account_number=account_number)
    except CRMResolutionError as exc:
        raise SlackMappingError(neutral.get(exc.code, "slack_association_invalid")) from None
    except SalesforceReadError as exc:
        mapped = {"salesforce_invalid_configuration": "slack_crm_configuration_invalid",
                  "salesforce_environment_mismatch": "slack_source_mismatch",
                  "salesforce_edition_required": "slack_source_mismatch",
                  "salesforce_account_number_ambiguous": "slack_account_ambiguous"}
        raise SlackMappingError(mapped.get(exc.code, "slack_crm_unavailable")) from None
    return {key: deepcopy(association[key]) for key in ("crm_binding", "account_id", "resolution_receipt_id")}


_PHASES = ("mapping", "credential_binding", "thread_read")
_COUNTS = ("api_calls", "auth_calls", "message_calls", "response_bytes", "auth_response_bytes",
           "message_response_bytes", "pages", "raw_rows", "messages_seen", "returned_messages", "duplicates",
           "refresh_attempts", "token_retries", "rate_retries")
_STEP_MESSAGES = {
    "private_state": "Existing private source state and the local check gateway are ready.",
    "mapping": "The saved account association and thread selection are valid.",
    "credential_binding": "The selected Slack credential matches the saved connection identity.",
    "thread_read": "The selected thread text passed the bounded paging and source checks.",
    "receipt": "The tool attempt has a matching committed audit receipt.",
}


def _number(value):
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


def _metrics(events):
    """Validate a trusted observer's fixed schema before projecting any diagnostics."""
    if len(events) != 1:
        return None
    event = events[0]
    expected = {"schema_version", "status", "code", "failure_phase", "mapping_validated",
                "credential_binding_validated", "pagination_complete", "duration_ms", "durations_ms", "counts"}
    try:
        if (not isinstance(event, dict) or set(event) != expected or type(event["schema_version"]) is not int
                or event["schema_version"] != 1 or event["status"] not in ("passed", "failed")
                or event["code"] not in {"ok", *_DIAGNOSTICS}
                or event["failure_phase"] not in (None, *_PHASES) or not _number(event["duration_ms"])
                or any(type(event[key]) is not bool for key in
                       ("mapping_validated", "credential_binding_validated", "pagination_complete"))
                or not isinstance(event["durations_ms"], dict) or set(event["durations_ms"]) != set(_PHASES)
                or any(not _number(value) for value in event["durations_ms"].values())
                or not isinstance(event["counts"], dict) or set(event["counts"]) != set(_COUNTS)):
            return None
        counts = event["counts"]
        if (any(type(value) is not int or value < 0 for value in counts.values())
                or counts["api_calls"] != counts["auth_calls"] + counts["message_calls"]
                or counts["response_bytes"] != counts["auth_response_bytes"] + counts["message_response_bytes"]
                or counts["pages"] > counts["message_calls"] or counts["returned_messages"] > counts["messages_seen"]
                or counts["messages_seen"] + counts["duplicates"] > counts["raw_rows"]):
            return None
        passed = event["status"] == "passed"
        if passed != (event["code"] == "ok") or passed != (event["failure_phase"] is None):
            return None
        if passed and (not all(event[key] for key in ("mapping_validated", "credential_binding_validated",
                                                     "pagination_complete"))
                       or counts["returned_messages"] != counts["messages_seen"]):
            return None
        if not passed and counts["returned_messages"] != 0:
            return None
        return deepcopy(event)
    except (TypeError, ValueError, OverflowError):
        return None


def _step(name, *, status="not_run", count=0, duration=0, api_calls=0, response_bytes=0,
          code="prerequisite_failed"):
    message, next_command = ((_STEP_MESSAGES[name], "") if status == "pass" else slack_failure_details(code))
    return {"name": name, "status": status, "count": count, "duration_ms": round(duration, 3),
            "api_calls": api_calls, "response_bytes": response_bytes, "code": "ok" if status == "pass" else code,
            "message": message, "next": next_command}


def _receipt_valid(rows, *, correlation, account_id, token, response_status, records, success):
    if token is None or not isinstance(rows, list) or len(rows) != 1:
        return False
    row = rows[0]
    try:
        references = [record["record_ref"] for record in records] if success else []
        if (not isinstance(row["audit_id"], str) or str(uuid.UUID(row["audit_id"])) != row["audit_id"]
                or row["brief_id"] != correlation or row["tool"] != "read_slack_messages"
                or row["arguments"] != {"account_id": account_id}
                or row["token_id"] != hashlib.sha256(token.encode()).hexdigest()
                or row["status"] != ("ok" if success else "error")
                or type(row["http_status"]) is not int or not 200 <= row["http_status"] <= 599
                or (response_status is not None and row["http_status"] != response_status)
                or type(row["row_count"]) is not int or row["row_count"] != len(references)
                or not isinstance(row["rows"], list) or any(not isinstance(ref, str) for ref in row["rows"])
                or sorted(row["rows"]) != sorted(references)):
            return False
        return True
    except (KeyError, TypeError, ValueError, AttributeError):
        return False


def _read_result(response, runtime, account_id):
    from .slack_reader import SLACK_READ_ERROR_CODES, SlackReadError, validate_slack_snapshot

    if response.status_code != 200:
        return [], None, "slack_local_gateway_failed"
    try:
        result = response.json()["result"]
        if result.get("isError"):
            content = result.get("content")
            if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                    and content[0].get("type") == "text"):
                for code in SLACK_READ_ERROR_CODES:
                    if content[0].get("text") == f"Error executing tool read_slack_messages: {code}":
                        return [], None, code
            return [], None, "slack_check_failed"
        data = result["structuredContent"]
        if (result.get("resultType") != "complete"
                or set(data) != {"source", "records", "untrusted_data", "thread_snapshot"}
                or data["source"] != "slack" or data["untrusted_data"] is not True):
            raise ValueError
        validate_slack_snapshot(data["records"], data["thread_snapshot"], account_id=account_id,
                                crm_binding=runtime.crm_binding, slack_binding=runtime.slack_binding)
        return data["records"], data["thread_snapshot"], "ok"
    except SlackReadError:
        return [], None, "slack_snapshot_invalid"
    except (KeyError, TypeError, ValueError, AttributeError):
        return [], None, "slack_check_failed"


def run_slack_check(directory, *, connection_dir, account_id, progress=None):
    """Read one mapped thread through HTTPS; return only fixed diagnostics and counts."""
    from .client import LocalClient
    from .runtime import SlackCheckRuntime
    from .slack_mapping import SlackMappingError
    import httpx2
    import sqlite3

    if not isinstance(account_id, str) or re.fullmatch(r"acc-(?:[0-9]{3}|sf-[A-Za-z0-9]{18})", account_id) is None:
        raise SlackMappingError("slack_selection_invalid")
    started = time.perf_counter()
    events: list[object] = []

    def observe(event):
        if len(events) < 2:
            events.append(deepcopy(event))

    def emit(name, status):
        if progress is not None:
            progress(name, status)

    emit("private_state", "running")
    with SlackCheckRuntime(directory, slack_connection_dir=connection_dir, on_source_read=observe) as runtime:
        pattern = r"acc-sf-[A-Za-z0-9]{18}" if runtime.crm_backend == "salesforce" else r"acc-[0-9]{3}"
        if re.fullmatch(pattern, account_id) is None:
            raise SlackMappingError("slack_selection_invalid")
        steps = [_step("private_state", status="pass", count=1, duration=(time.perf_counter() - started) * 1000)]
        emit("private_state", "pass")
        correlation = "slack-check-" + uuid.uuid4().hex
        token, response, snapshot = None, None, None
        records = []
        code = "slack_local_gateway_failed"
        call_started = False
        emit("thread_read", "running")
        try:
            with LocalClient(runtime.config) as client:
                token = client.authorize(["slack:read"])
                call_started = True
                response = client.call(token, "read_slack_messages", {"account_id": account_id}, brief_id=correlation)
                records, snapshot, code = _read_result(response, runtime, account_id)
        except (httpx2.HTTPError, ValueError, RuntimeError):
            pass
        metrics = _metrics(events)
        source_ok = code == "ok"
        receipt_started = time.perf_counter()
        try:
            receipt_ok = _receipt_valid(runtime.connector.audit.read(correlation), correlation=correlation,
                                        account_id=account_id, token=token,
                                        response_status=None if response is None else response.status_code,
                                        records=records, success=source_ok)
        except (OSError, ValueError, RuntimeError, sqlite3.Error):
            receipt_ok = False
        receipt_ms = (time.perf_counter() - receipt_started) * 1000
        if metrics is not None:
            counts = metrics["counts"]
            for phase, flag in (("mapping", "mapping_validated"),
                                ("credential_binding", "credential_binding_validated"),
                                ("thread_read", "pagination_complete")):
                api_calls = counts["auth_calls"] if phase == "credential_binding" else (
                    counts["message_calls"] if phase == "thread_read" else 0)
                response_bytes = counts["auth_response_bytes"] if phase == "credential_binding" else (
                    counts["message_response_bytes"] if phase == "thread_read" else 0)
                passed = metrics[flag] and (phase != "thread_read" or source_ok)
                failed = (metrics["failure_phase"] == phase or api_calls > 0 or
                          (phase == "thread_read" and metrics[flag] and not source_ok))
                phase_code = (metrics["code"] if metrics["failure_phase"] == phase else
                              code if phase == "thread_read" and not source_ok else "prerequisite_failed")
                steps.append(_step(phase, status="pass" if passed else "fail" if failed else "not_run",
                                   count=(len(records) if phase == "thread_read" else 1) if passed else 0,
                                   duration=metrics["durations_ms"][phase], api_calls=api_calls,
                                   response_bytes=response_bytes, code=phase_code))
            shared_counts = {"api_calls": "http_attempts", "auth_calls": "auth_attempts",
                             "message_calls": "message_attempts", "response_bytes": "response_bytes",
                             "pages": "pages", "raw_rows": "raw_rows", "returned_messages": "messages",
                             "messages_seen": "messages", "duplicates": "duplicates",
                             "refresh_attempts": "refresh_attempts", "token_retries": "token_retries",
                             "rate_retries": "rate_retries"}
            if source_ok and (snapshot is None or metrics["status"] != "passed"
                              or counts["returned_messages"] != len(records)
                              or any(snapshot["counts"][field] != counts[key] for key, field in shared_counts.items())):
                steps[-1] = _step("thread_read", status="fail", code="slack_metrics_unavailable",
                                  duration=metrics["durations_ms"]["thread_read"],
                                  api_calls=counts["message_calls"], response_bytes=counts["message_response_bytes"])
        else:
            # No source observer means work may have happened after a transport failure.
            # Only a call that was never dispatched establishes zero source work here.
            unknown = None if call_started else 0
            counts = {key: unknown for key in _COUNTS}
            steps.extend(_step(phase, status="fail" if phase == "thread_read" else "not_run",
                               api_calls=unknown, response_bytes=unknown,
                               code="slack_metrics_unavailable" if phase == "thread_read" else "prerequisite_failed")
                         for phase in _PHASES)
        steps.append(_step("receipt", status="pass" if receipt_ok else "fail", count=int(receipt_ok),
                           duration=receipt_ms, code="slack_receipt_invalid"))
        for step in steps[1:]:
            emit(step["name"], step["status"])
        passed = all(step["status"] == "pass" for step in steps)
        next_command = next((step["next"] for step in steps if step["status"] == "fail" and step["next"]), "")
        report = {"schema_version": 1, "status": "passed" if passed else "failed",
                  "crm_access_checked": False, "calls_access_checked": False,
                  "api_calls": counts["api_calls"], "response_bytes": counts["response_bytes"],
                  "counts": {key: counts[key] for key in ("auth_calls", "message_calls", "pages", "raw_rows",
                                                          "duplicates", "refresh_attempts", "token_retries",
                                                          "rate_retries")},
                  "steps": steps, "next": next_command}
        report["counts"]["messages"] = len(records) if passed else 0
    report["duration_ms"] = round((time.perf_counter() - started) * 1000, 3)
    return report
