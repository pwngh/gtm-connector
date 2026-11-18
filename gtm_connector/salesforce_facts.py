"""Extract bounded renewal-planning facts from standard Salesforce fields."""

import re

from .citations import BriefRejected, NoRenewalCandidate, _day, _text


def _nullable_text(value):
    return "not provided" if value is None else _text(value)


def _amount(opportunity):
    value = opportunity["amount_decimal"]
    if opportunity["currency"] is not None:
        raise BriefRejected("Standard Salesforce evidence must preserve unknown currency.")
    if value is None:
        return "not provided (currency unknown)"
    if not isinstance(value, str) or len(value) > 128 or not re.fullmatch(r"-?[0-9]+(?:\.[0-9]+)?", value):
        raise BriefRejected("Salesforce amounts must be finite decimal strings or null.")
    return value + " (currency unknown)"


def add_salesforce_facts(groups, as_of, brief_id, add):
    """Select an actual EndDate; Draft and InApproval remain planning candidates."""
    eligible = []
    for contract in groups["contracts"]:
        if contract["status_code"] not in ("Draft", "InApproval", "Activated"):
            raise BriefRejected("Unknown Salesforce Contract status category.")
        _text(contract["status"])
        end = None if contract["end_date"] is None else _day(contract["end_date"])
        if end is not None and end >= as_of:
            eligible.append((end, contract["id"], contract))
    if not eligible:
        raise NoRenewalCandidate("No Draft, InApproval, or Activated Contract has an EndDate "
                                 "on or after the as-of date. Run gtm-connector salesforce check.")
    end, _, contract = min(eligible, key=lambda candidate: (candidate[0], candidate[1]))
    account = groups["accounts"][0]
    account_ref, contract_ref = account["record_ref"], contract["record_ref"]
    required = set()
    add(f"Account: {_text(account['name'])}.", account_ref)
    add(f"Industry: {_nullable_text(account['industry'])}.", account_ref)
    required.add(add("Annual recurring revenue is not mapped from standard Salesforce fields.", account_ref))
    required.add(add("CRM renewal risk is not mapped from standard Salesforce fields.", account_ref))
    required.add(add(f"Renewal planning candidate: Contract {contract['id']}; "
                     f"status {_text(contract['status'])}; category {_text(contract['status_code'])}.", contract_ref))
    required.add(add(f"Contract end date: {end.isoformat()}.", contract_ref))
    days = (end - as_of).days
    timing = (f"Contract end is in {days} {'day' if days == 1 else 'days'} as of {as_of}." if days else
              f"Contract ends on the as-of date {as_of}.")
    required.add(add(timing, contract_ref, f"run/{brief_id}"))
    if contract["status_code"] == "Draft":
        required.add(add("Draft contract is a planning candidate and has not been activated.", contract_ref))
    elif contract["status_code"] == "InApproval":
        required.add(add("Contract in approval is a planning candidate and has not been activated.", contract_ref))
    else:
        required.add(add("Activated contract supplies the renewal planning date.", contract_ref))
    for contact in sorted(groups["contacts"], key=lambda row: row["id"]):
        add(f"Contact {_text(contact['name'])}: role {_nullable_text(contact['title'])}; "
            f"email {_nullable_text(contact['email'])}.", contact["record_ref"])
    for opportunity in sorted(groups["opportunities"], key=lambda row: row["id"]):
        if type(opportunity["is_closed"]) is not bool or type(opportunity["is_won"]) is not bool:
            raise BriefRejected("Salesforce opportunity outcomes must be booleans.")
        add(f"CRM opportunity {opportunity['id']}: stage {_text(opportunity['stage'])}; "
            f"amount {_amount(opportunity)}; close date {_day(opportunity['close_date'])}; "
            f"closed {str(opportunity['is_closed']).lower()}; won {str(opportunity['is_won']).lower()}.",
            opportunity["record_ref"])
    return required, contract_ref
