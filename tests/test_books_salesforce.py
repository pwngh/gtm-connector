"""Every book account, served from an invented Salesforce org, gives an exact cited brief or a documented refusal.

The upstream is an httpx2.MockTransport that answers the adapter's REST calls. With a Salesforce CRM the gateway
still reads call notes and Slack messages from the demo stores by account id, so each book account's notes and
messages are loaded there under its acc-sf- id.
"""

from collections import Counter
from contextlib import closing
from datetime import date, datetime, timezone
from decimal import Decimal
import hashlib
from itertools import islice
import json
from pathlib import Path
import re
import sqlite3
import tempfile
import unittest
import uuid

import httpx2

from gtm_connector.briefing import create_brief
from gtm_connector.citations import BriefRejected, check_brief
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from tests import varied
from tests.test_results import citations, corruptions


AS_OF = varied.AS_OF
INSTANCE = "https://invented-books.my.salesforce.com"
TOKEN = "invented-salesforce-token-for-book-fixtures"
QUERY_PATH = "/services/data/v68.0/query"
FIELDS = {
    "Account": ["Id", "Name", "Industry", "AccountNumber"],
    "Contact": ["Id", "AccountId", "Name", "Title", "Email"],
    "Opportunity": ["Id", "AccountId", "Name", "StageName", "Amount", "CloseDate", "IsClosed", "IsWon"],
    "Contract": ["Id", "AccountId", "Status", "StatusCode", "StartDate", "ContractTerm", "EndDate"],
}
SOQL = re.compile(r"SELECT ([A-Za-z, ]+) FROM (Account|Contact|Opportunity|Contract) "
                  r"WHERE (Id|AccountId) = '([A-Za-z0-9]{18})' ORDER BY Id LIMIT (101|1001)")
# docs/salesforce.md, Field mapping: object -> (evidence table, id prefix, evidence field -> upstream field).
MAPPING = {
    "Account": ("accounts", "acc", {"name": "Name", "industry": "Industry", "account_number": "AccountNumber"}),
    "Contact": ("contacts", "con", {"name": "Name", "title": "Title", "email": "Email"}),
    "Opportunity": ("opportunities", "opp", {"name": "Name", "stage": "StageName", "close_date": "CloseDate",
                                             "is_closed": "IsClosed", "is_won": "IsWon"}),
    "Contract": ("contracts", "ctr", {"status": "Status", "status_code": "StatusCode", "start_date": "StartDate",
                                      "end_date": "EndDate", "contract_term": "ContractTerm"}),
}
ACTIVATION = {"Draft": "Draft contract is a planning candidate and has not been activated.",
              "InApproval": "Contract in approval is a planning candidate and has not been activated.",
              "Activated": "Activated contract supplies the renewal planning date."}
# json.dumps cannot write a Decimal, so an amount is written as this marked string and then unquoted.
MARK = uuid.uuid4().hex


def sf_id(prefix, *key):
    """A deterministic 18-character id: 15 case-sensitive characters, then Salesforce's 3-character checksum."""
    body = prefix + hashlib.sha256(repr((prefix, key)).encode()).hexdigest()[:12].upper()
    alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
    return body + "".join(alphabet[sum(1 << bit for bit, character in enumerate(body[start:start + 5])
                                       if character.isupper())] for start in (0, 5, 10))


def org_rows(key, account):
    """One book account as the rows a real org holds: null for empty text, a decimal Amount, outcome flags."""
    native = sf_id("001", key)
    return {
        "Account": [{"Id": native, "Name": account["name"], "Industry": account["industry"] or None,
                     "AccountNumber": None}],
        "Contact": [{"Id": sf_id("003", key, index), "AccountId": native, "Name": row["name"],
                     "Title": row["title"] or None, "Email": row["email"] or None}
                    for index, row in enumerate(account["contacts"])],
        "Opportunity": [{"Id": sf_id("006", key, index), "AccountId": native, "Name": row["name"],
                         "StageName": row["salesforce_stage"],
                         "Amount": Decimal(f"{row['amount_cents'] // 100}.{row['amount_cents'] % 100:02d}"),
                         "CloseDate": row["close_date"], "IsClosed": row["stage"].startswith("closed_"),
                         "IsWon": row["stage"] == "closed_won"}
                        for index, row in enumerate(account["opportunities"])],
        "Contract": [{"Id": sf_id("800", key, index), "AccountId": native, "Status": row["status"],
                      "StatusCode": row["status"], "StartDate": row["start_date"],
                      "ContractTerm": row["term_months"], "EndDate": row["end_date"]}
                     for index, row in enumerate(account["contracts"])],
    }


def shape(*, contacts=0, opportunities=0, contracts=1):
    """A book account built in code with the given number of each record."""
    return {"name": "Sized Account", "industry": "Testing",
            "contacts": [{"name": f"Person {index}", "title": "Buyer", "email": f"person{index}@sized.example"}
                         for index in range(contacts)],
            "opportunities": [{"name": f"Deal {index}", "stage": "closed_won", "salesforce_stage": "Closed Won",
                               "amount_cents": index, "close_date": "2027-03-01"} for index in range(opportunities)],
            "contracts": [{"status": "Activated", "start_date": "2026-06-01", "end_date": "2027-06-01",
                           "term_months": 12} for _ in range(contracts)],
            "call_notes": [], "slack_messages": []}


class Org:
    """An invented Salesforce org over HTTP: the adapter's SOQL, describes, and paged results."""

    def __init__(self):
        self.rows = {name: {} for name in FIELDS}  # object -> account id -> rows
        self.page_size = {}  # (object, account id) -> rows per page, 2000 like a real org when absent
        self.cursors, self.gets, self.unexpected = {}, Counter(), []

    def add(self, rows, **page_size):
        native = rows["Account"][0]["Id"]
        for name in FIELDS:
            self.rows[name][native] = rows[name]
        self.page_size.update({(name, native): size for name, size in page_size.items()})
        return "acc-sf-" + native

    def respond(self, request):
        path = request.url.path
        if request.method != "GET" or request.headers.get("Authorization") != "Bearer " + TOKEN:
            return self.refuse(request)
        if path.endswith("/describe") and path.split("/")[-2] in FIELDS:
            name = path.split("/")[-2]
            self.gets["describe", name] += 1
            return httpx2.Response(200, json={"name": name, "queryable": True,
                                              "fields": [{"name": field} for field in FIELDS[name]]})
        if path in self.cursors:
            name, native, rows, start = self.cursors.pop(path)
        elif path != QUERY_PATH:
            return self.refuse(request)
        elif request.url.params["q"] == "SELECT Id, OrganizationType, IsSandbox FROM Organization LIMIT 1":
            name, native, start = "Organization", "", 0
            rows = [{"Id": sf_id("00D", "org"), "OrganizationType": "Enterprise Edition", "IsSandbox": False}]
        else:
            query = SOQL.fullmatch(request.url.params["q"])
            if (query is None or query[1].split(", ") != FIELDS[query[2]]
                    or (query[3] == "Id") != (query[2] == "Account") or (query[5] == "101") != (query[2] == "Account")):
                return self.refuse(request)
            name, native, start = query[2], query[4], 0
            # ORDER BY Id LIMIT 1001 for an account's records, and totalSize counts what the limit let through.
            rows = sorted(self.rows[name].get(native, []), key=lambda row: row["Id"])[:int(query[5])]
        self.gets[name, native] += 1
        end = start + self.page_size.get((name, native), 2000)
        records = [{"attributes": {"type": name, "url": f"/services/data/v68.0/sobjects/{name}/{row['Id']}"}, **row}
                   for row in rows[start:end]]
        document = {"totalSize": len(rows), "done": end >= len(rows), "records": records}
        if not document["done"]:
            document["nextRecordsUrl"] = f"{QUERY_PATH}/{sf_id('01g', name, native)}-{end}"
            self.cursors[document["nextRecordsUrl"]] = (name, native, rows, end)
        text = json.dumps(document, ensure_ascii=False, default=lambda amount: f"{MARK}{amount}{MARK}")
        return httpx2.Response(200, headers={"Content-Type": "application/json"},
                               content=re.sub(f'"{MARK}(-?[0-9.]+){MARK}"', r"\1", text).encode())

    def refuse(self, request):
        # A request this org does not know is kept for the test to fail on, not answered with data.
        self.unexpected.append(f"{request.method} {request.url}")
        return httpx2.Response(400, json=[{"errorCode": "MALFORMED_QUERY", "message": "unexpected"}])


def quote(text):
    return "not provided" if text is None else json.dumps(text, ensure_ascii=True)


def count(days):
    return f"{days} day" if days == 1 else f"{days} days"


def instant(text):
    """A call time as a UTC instant, or None when it has no usable time or offset."""
    try:
        moment = datetime.fromisoformat(text.replace("Z", "+00:00"))
        return moment.astimezone(timezone.utc) if moment.tzinfo else None
    except (ValueError, OverflowError):
        return None


def selected(contracts):
    """docs/salesforce.md, Briefs: the earliest non-null EndDate on or after the date, ties by id, or None."""
    eligible = [row for row in contracts if row["StatusCode"] in ACTIVATION and row["EndDate"] is not None
                and date.fromisoformat(row["EndDate"]) >= AS_OF]
    return min(eligible, key=lambda row: (row["EndDate"], row["Id"]), default=None)


def outcome(rows):
    """The documented path for one account: a brief, or the limit code."""
    if any(len(rows[name]) > 1000 for name in ("Contact", "Opportunity", "Contract")):
        return "salesforce_result_limit"
    return "brief"


def read_records(rows, notes, messages):
    """What the evidence must hold for every row that was served, keyed by its citation."""
    account_id = "acc-sf-" + rows["Account"][0]["Id"]
    records = {}
    for name, (table, prefix, fields) in MAPPING.items():
        for row in rows[name]:
            record = {field: row[source] for field, source in fields.items()}
            if name != "Account":
                record["account_id"] = account_id
            if name == "Opportunity":
                record.update(amount_decimal=None if row["Amount"] is None else str(row["Amount"]), currency=None)
            records[f"[crm/{table}/{prefix}-sf-{row['Id']}]"] = record
    for identifier, _, called_at, body in notes:
        records[f"[calls/call_notes/{identifier}]"] = {"account_id": account_id, "called_at": called_at, "body": body}
    for identifier, _, channel, author, sent_at, body in messages:
        records[f"[slack/messages/{identifier}]"] = {"account_id": account_id, "channel": channel, "author": author,
                                                     "sent_at": sent_at, "body": body}
    return records


def expected(rows, notes, messages, brief):
    """Return (the signal lines in order, every other line), written from the served rows and the docs."""
    run = f"[run/{brief['brief_id']}]"
    receipt = {row["tool"]: f"[audit/{row['audit_id']}]" for row in brief["evidence"]["receipts"]}
    account = rows["Account"][0]
    account_id = "acc-sf-" + account["Id"]
    ref = f"[crm/accounts/{account_id}]"
    contract = selected(rows["Contract"])
    lines = [f"- Account: {quote(account['Name'])}. {ref}", f"- Industry: {quote(account['Industry'])}. {ref}",
             f"- Annual recurring revenue is not mapped from standard Salesforce fields. {ref}",
             f"- CRM renewal risk is not mapped from standard Salesforce fields. {ref}"]
    lapsed, days = [], None
    if contract:
        contract_ref = f"[crm/contracts/ctr-sf-{contract['Id']}]"
        days = (date.fromisoformat(contract["EndDate"]) - AS_OF).days
        lines += [f"- Renewal planning candidate: Contract ctr-sf-{contract['Id']}; status {quote(contract['Status'])}; "
                  f"category {quote(contract['StatusCode'])}. {contract_ref}",
                  f"- Contract end date: {contract['EndDate']}. {contract_ref}",
                  (f"- Contract end is in {count(days)} as of {AS_OF}. " if days else
                   f"- Contract ends on the as-of date {AS_OF}. ") + f"{contract_ref} {run}",
                  f"- {ACTIVATION[contract['StatusCode']]} {contract_ref}"]
    else:
        # No contract is ahead: the brief says so, and the latest one that ended is a signal.
        contract_ref = ref
        lines.append(f"- Salesforce returned no contract with an end date on or after {AS_OF}. {ref} {run}")
        ended = [row for row in rows["Contract"] if row["EndDate"]]
        if ended:
            last = max(ended, key=lambda row: (row["EndDate"], row["Id"]))
            age = (AS_OF - date.fromisoformat(last["EndDate"])).days
            lapsed = [f"- Signal: the latest contract end date, {last['EndDate']} on contract ctr-sf-{last['Id']} with "
                      f"category {quote(last['StatusCode'])}, was {count(age)} ago. "
                      f"[crm/contracts/ctr-sf-{last['Id']}] {run}"]
    lines.append(f"- Proposed follow-up: Ask the account owner to review the cited renewal context. {contract_ref}")
    for row in rows["Contract"]:
        if row is not contract:
            ends = "no end date" if row["EndDate"] is None else f"end date {row['EndDate']}"
            lines.append(f"- Other contract ctr-sf-{row['Id']}: status {quote(row['Status'])}; "
                         f"category {quote(row['StatusCode'])}; {ends}. [crm/contracts/ctr-sf-{row['Id']}]")
    ending, roles, stale = [], [], []
    for row in sorted(rows["Opportunity"], key=lambda row: row["Id"]):
        identifier = "opp-sf-" + row["Id"]
        amount = "not provided" if row["Amount"] is None else str(row["Amount"])
        lines.append(f"- CRM opportunity {identifier} {quote(row['Name'])}: stage {quote(row['StageName'])}; amount {amount} "
                     f"(currency unknown); close date {row['CloseDate']}; closed {str(row['IsClosed']).lower()}; "
                     f"won {str(row['IsWon']).lower()}. [crm/opportunities/{identifier}]")
        if days is not None and days <= 30 and not row["IsClosed"]:
            ending.append(f"- Signal: contract end {f'is in {count(days)}' if days else 'is today'} and opportunity "
                          f"{identifier} {quote(row['Name'])} is still open at stage {quote(row['StageName'])}. "
                          f"{contract_ref} [crm/opportunities/{identifier}] {run}")
    if len(ending) > 5:
        # docs/briefs.md, Signals: more than five open opportunities are counted in one line that cites their read.
        ending = [f"- Signal: contract end {f'is in {count(days)}' if days else 'is today'} and {len(ending)} "
                  f"opportunities are still open. {contract_ref} {receipt['list_opportunities']} {run}"]
    if not rows["Opportunity"]:
        lines.append(f"- No opportunities were returned for {account_id}. {receipt['list_opportunities']}")
    for row in sorted(rows["Contact"], key=lambda row: row["Id"]):
        lines.append(f"- Contact {quote(row['Name'])}: role {quote(row['Title'])}; email {quote(row['Email'])}. "
                     f"[crm/contacts/con-sf-{row['Id']}]")
        if row["Title"] is None:
            roles.append(f"- Signal: contact {quote(row['Name'])} has no role recorded. "
                         f"[crm/contacts/con-sf-{row['Id']}]")
    if len(roles) > 5:
        roles = [f"- Signal: {len(roles)} contacts have no role recorded. {ref} {receipt['get_account']}"]
    for identifier, _, called_at, body in notes:
        lines.append(f"- Untrusted call note {identifier} at {quote(called_at)} says {quote(body)}. "
                     f"[calls/call_notes/{identifier}]")
    timed = [(moment, row[0]) for row in notes if (moment := instant(row[2])) is not None]
    if timed and (age := (AS_OF - max(timed)[0].date()).days) > 60:
        dated = "" if len(timed) == len(notes) else "dated "
        stale.append(f"- Signal: the latest {dated}call note {max(timed)[1]} is {count(age)} old as of {AS_OF}. "
                     f"[calls/call_notes/{max(timed)[1]}] {run}")
    if not notes:
        lines.append(f"- No call notes were returned for {account_id}. {receipt['read_call_notes']}")
    for identifier, _, channel, author, sent_at, body in messages:
        lines.append(f"- Untrusted Slack message {identifier} at {quote(sent_at)} in {quote(channel)} from "
                     f"{quote(author)} says {quote(body)}. [slack/messages/{identifier}]")
    if not messages:
        lines.append(f"- No Slack messages were returned for {account_id}. {receipt['read_slack_messages']}")
    # docs/briefs.md, Signals: the contract end, then contacts with no role, then the stale call note.
    return lapsed + ending + roles + stale, lines


class SalesforcePath(unittest.TestCase):
    """One running gateway whose CRM is the invented org. A production org, so startup reads the four describes."""

    @classmethod
    def setUpClass(cls):
        cls.org = Org()
        reader = SalesforceReadOnlyAdapter(INSTANCE, TOKEN, environment="production",
                                           transport=httpx2.MockTransport(cls.org.respond))
        cls.state = Path(cls.enterClassContext(tempfile.TemporaryDirectory())) / "state"
        cls.runtime = cls.enterClassContext(Runtime(cls.state, crm_reader=reader))
        cls.number = 0

    def serve(self, rows, account=None, **page_size):
        """Put the account in the org and its notes and messages in the demo stores under the Salesforce id."""
        account_id = self.org.add(rows, **page_size)
        type(self).number += 1
        account = account or {"call_notes": [], "slack_messages": []}
        notes = [(f"note-{self.number}-{index:03d}", account_id, row["called_at"], row["body"])
                 for index, row in enumerate(account["call_notes"])]
        messages = [(f"msg-{self.number}-{index:03d}", account_id, row["channel"], row["author"], row["sent_at"],
                     row["body"]) for index, row in enumerate(account["slack_messages"])]
        for database, table, loaded in (("calls", "call_notes", notes), ("slack", "messages", messages)):
            with closing(sqlite3.connect(self.state / f"{database}.sqlite3")) as connection, connection:
                for row in loaded:
                    connection.execute(f"INSERT INTO {table} VALUES ({', '.join('?' * len(row))})", row)
        return account_id, notes, messages

    def saved(self):
        return sorted(path.name for path in (self.state / "briefs").glob("*"))

    def brief(self, rows, notes=(), messages=(), corrupted=None):
        """Create the brief and hold it to everything the served rows imply. Return it and the uncited records."""
        account_id = "acc-sf-" + rows["Account"][0]["Id"]
        brief = create_brief(self.runtime, account_id, as_of=AS_OF.isoformat(), include_slack=True)
        self.assertEqual(brief["status"], "accepted")
        evidence, lines = brief["evidence"], brief["text"].splitlines()
        # Every row that was served is in the evidence with the values the org returned.
        read = read_records(rows, notes, messages)
        self.assertEqual({f"[{record['record_ref']}]" for record in evidence["records"]}, set(read))
        for record in evidence["records"]:
            wanted = read[f"[{record['record_ref']}]"]
            self.assertEqual({field: record[field] for field in wanted}, wanted)
        known = set(read) | {f"[audit/{row['audit_id']}]" for row in evidence["receipts"]}
        known.add(f"[run/{brief['brief_id']}]")
        cited = set()
        for line in lines:
            # Source text never breaks out of its line: one printable ASCII bullet that ends in known citations.
            self.assertTrue(line.startswith("- ") and line.isascii() and line.isprintable(), line[:80])
            self.assertTrue(citations(line), line[:80])
            self.assertLessEqual(set(citations(line)), known)
            cited.update(citations(line))
        # The signals open the brief in the documented order, and every other line is exactly one expected line.
        signals, others = expected(rows, notes, messages, brief)
        self.assertEqual(lines[:len(signals)], signals)
        self.assertEqual(sorted(lines[len(signals):]), sorted(others))
        # The checker returns the brief unchanged and refuses each way of spoiling it.
        self.assertEqual(check_brief(brief["text"], evidence), brief["text"])
        for kind, candidate in islice(corruptions(brief["text"]), corrupted):
            with self.assertRaises(BriefRejected, msg=kind):
                check_brief(candidate, evidence)
        return brief, sorted(set(read) - cited)

    def refused(self, rows, error, code):
        """The documented refusal and nothing else: no brief is saved and the failed read returned no rows."""
        account_id = "acc-sf-" + rows["Account"][0]["Id"]
        before, receipts = self.saved(), len(self.runtime.connector.audit.read())
        with self.assertRaises(error) as caught:
            create_brief(self.runtime, account_id, as_of=AS_OF.isoformat(), include_slack=True)
        self.assertEqual((type(caught.exception), caught.exception.code), (error, code))
        self.assertEqual(self.saved(), before)
        if error is SalesforceReadError:
            failed = self.runtime.connector.audit.read()[receipts:][-1]
            self.assertEqual((failed["status"], failed["rows"], failed["row_count"]), ("error", [], 0))

    def held(self, rows, notes=(), messages=(), *, to):
        if to == "brief":
            # Forty corrupted drafts per brief keep the largest ones quick.
            return self.brief(rows, notes, messages, corrupted=40 if max(map(len, rows.values())) > 100 else None)
        self.refused(rows, SalesforceReadError, to)

    def tearDown(self):
        self.assertEqual(self.org.unexpected, [])
        # Startup read each describe once, and no later read repeated it.
        self.assertEqual([self.org.gets["describe", name] for name in FIELDS], [1, 1, 1, 1])


class BookTests(SalesforcePath):
    def test_every_book_account_gives_its_exact_brief_or_its_documented_refusal(self):
        self.assertGreaterEqual(len(varied.BOOKS), 1)
        paths = Counter()
        for name in varied.BOOKS:
            uncited = {}
            for index, account in enumerate(varied.read(name)):
                rows = org_rows((name, index), account)
                _, notes, messages = self.serve(rows, account)
                paths[outcome(rows)] += 1
                with self.subTest(book=name, account=account["name"][:40], path=outcome(rows)):
                    if result := self.held(rows, notes, messages, to=outcome(rows)):
                        uncited[account["name"][:40]] = result[1]
            # One failure a book, not one an account: the cause is the same wherever it shows.
            with self.subTest(book=name, check="every record that was read is cited"):
                self.assertEqual({account: records for account, records in uncited.items() if records}, {})
        # Every book account is briefed, including the ones with no contract left to renew.
        self.assertEqual(set(paths), {"brief"})

    def test_two_contracts_are_both_read_and_both_cited(self):
        # A contract that was not selected is still stated and cited.
        account = shape(contracts=2)
        account["contracts"][1]["end_date"] = "2028-06-01"
        rows = org_rows("two contracts", account)
        self.serve(rows)
        self.assertEqual(self.brief(rows)[1], [])


class BuiltTests(SalesforcePath):
    """Accounts built in code. Each is held to its outcome and its exact brief."""

    def sized(self, key, account, *, to, **page_size):
        rows = org_rows(key, account)
        self.serve(rows, **page_size)
        with self.subTest(case=key, outcome=to):
            self.held(rows, to=to)
        return rows["Account"][0]["Id"]

    def test_row_counts_up_to_the_limit_give_a_brief_and_one_more_gives_the_limit_code(self):
        for record in ("contacts", "opportunities", "contracts"):
            for size in (0, 1, 100, 101, 1000, 1001):
                to = "salesforce_result_limit" if size == 1001 else "brief"
                self.sized((record, size), shape(**{"contracts": 1, record: size}), to=to)
        # 1,000 of each at once: get_account reads about 0.6 MB of contacts and contracts.
        self.sized("full", shape(contacts=1000, opportunities=1000, contracts=1000), to="brief")

    def test_a_thousand_rows_over_one_mebibyte_give_the_limit_code(self):
        account = shape(contacts=1000)
        for contact in account["contacts"]:
            contact["title"] = "t" * 900
        self.sized("wide", account, to="salesforce_result_limit")

    def test_a_thousand_rows_in_five_and_ten_pages_give_a_brief(self):
        for record, name in (("contacts", "Contact"), ("opportunities", "Opportunity"), ("contracts", "Contract")):
            for page in (200, 100):
                native = self.sized((record, "per page", page), shape(**{"contracts": 1, record: 1000}), to="brief",
                                    **{name: page})
                self.assertEqual(self.org.gets[name, native], 1000 // page)

    def test_six_open_opportunities_and_six_contacts_with_no_role_give_one_signal_each_written_out_by_hand(self):
        for size, grouped in ((5, False), (6, True)):
            account = shape(contacts=size + 1, opportunities=size + 1)
            account["contracts"][0]["end_date"] = "2027-01-24"
            for contact in account["contacts"][:size]:
                contact["title"] = ""
            for deal in account["opportunities"][:size]:
                deal.update(stage="proposal", salesforce_stage="Proposal")
            rows = org_rows(("grouped", size), account)
            self.serve(rows)
            brief = self.brief(rows)[0]
            receipt = {row["tool"]: f"[audit/{row['audit_id']}]" for row in brief["evidence"]["receipts"]}
            signals = [line for line in brief["text"].splitlines() if line.startswith("- Signal: ")]
            if grouped:
                native = rows["Account"][0]["Id"]
                self.assertEqual(signals, [
                    f"- Signal: contract end is in 9 days and 6 opportunities are still open. [crm/contracts/ctr-sf-"
                    f"{rows['Contract'][0]['Id']}] {receipt['list_opportunities']} [run/{brief['brief_id']}]",
                    f"- Signal: 6 contacts have no role recorded. [crm/accounts/acc-sf-{native}] "
                    f"{receipt['get_account']}"])
            else:
                self.assertEqual([" and opportunity opp-sf-" in line for line in signals], [True] * 5 + [False] * 5)
                self.assertEqual([line.startswith('- Signal: contact "Person ') for line in signals],
                                 [False] * 5 + [True] * 5)

    def test_text_of_one_character_and_of_200_000_characters(self):
        fields = [("Account", "Name"), ("Account", "Industry"), ("Contact", "Name"), ("Contact", "Title"),
                  ("Contact", "Email"), ("Opportunity", "Name"), ("Opportunity", "StageName"), ("Contract", "Status")]
        account = shape(contacts=1, opportunities=1)
        account["call_notes"] = [{"called_at": "2027-01-14T10:00:00Z", "body": "n"}]
        account["slack_messages"] = [{"channel": "c", "author": "a", "sent_at": "2027-01-14T11:00:00Z", "body": "m"}]
        short = org_rows("one character", account)
        for index, (name, field) in enumerate(fields):
            short[name][0][field] = "abcdefgh"[index]
        _, notes, messages = self.serve(short, account)
        with self.subTest(case="one character in every field"):
            self.brief(short, notes, messages)
        for name, field in fields:
            rows = org_rows((name, field, 200_000), account)
            rows[name][0][field] = "x" * 200_000
            _, notes, messages = self.serve(rows, account)
            with self.subTest(case=f"200,000 characters in {name}.{field}"):
                self.brief(rows, notes, messages)
        for record, text in (("call_notes", "body"), ("slack_messages", "body")):
            long = {**account, record: [{**account[record][0], text: "x" * 200_000}]}
            rows = org_rows((record, 200_000), long)
            _, notes, messages = self.serve(rows, long)
            with self.subTest(case=f"200,000 characters in {record}"):
                self.brief(rows, notes, messages)
        # Two bytes a character upstream is still under 1 MiB, and the line is quoted in ASCII.
        rows = org_rows("accented", account)
        rows["Account"][0]["Name"] = "é" * 200_000
        self.serve(rows)
        with self.subTest(case="200,000 non-ASCII characters in Account.Name"):
            self.brief(rows)
        # Every field at once makes get_account read 1.2 MB, over the 1 MiB an operation may decode.
        rows = org_rows("every field", account)
        for name, field in fields:
            rows[name][0][field] = "x" * 200_000
        self.serve(rows)
        with self.subTest(case="200,000 characters in every field"):
            self.refused(rows, SalesforceReadError, "salesforce_result_limit")

    def test_two_and_ten_pages_give_a_brief_and_eleven_pages_give_the_limit_code(self):
        for record, name in (("contacts", "Contact"), ("opportunities", "Opportunity"), ("contracts", "Contract")):
            for pages in (2, 10, 11):
                # One row a page. The adapter reads ten pages and never asks for the eleventh.
                native = self.sized((record, "pages", pages), shape(**{"contracts": 1, record: pages}),
                                    to="brief" if pages < 11 else "salesforce_result_limit", **{name: 1})
                self.assertEqual(self.org.gets[name, native], min(pages, 10))

    def test_get_account_reads_twelve_times_and_refuses_the_thirteenth(self):
        # get_account is one operation: the account, its contact pages, and its contract pages.
        for contacts, to in ((5, "brief"), (6, "salesforce_result_limit")):
            native = self.sized(("gets", contacts), shape(contacts=contacts, contracts=6), to=to, Contact=1, Contract=1)
            self.assertEqual(sum(self.org.gets[name, native] for name in ("Account", "Contact", "Contract")), 12)

    def test_the_selected_contract_is_the_earliest_end_on_or_after_the_date_with_ties_by_id(self):
        account = shape(opportunities=1, contracts=6)
        for contract, (status, end) in zip(account["contracts"], (
                ("Activated", "2027-01-14"), ("Draft", None), ("Draft", "2027-01-15"), ("InApproval", "2027-01-15"),
                ("Activated", "2027-01-15"), ("Activated", "2027-01-16"))):
            contract.update(status=status, end_date=end)
        account["opportunities"][0].update(stage="proposal", salesforce_stage="Proposal")
        rows = org_rows("selection", account)
        self.serve(rows)
        tied = sorted(row["Id"] for row in rows["Contract"] if row["EndDate"] == "2027-01-15")
        self.assertEqual(selected(rows["Contract"])["Id"], tied[0])
        brief = self.brief(rows)[0]
        deal = rows["Opportunity"][0]
        self.assertIn(f"- Signal: contract end is today and opportunity opp-sf-{deal['Id']} {quote(deal['Name'])} "
                      f'is still open at stage "Proposal". [crm/contracts/ctr-sf-{tied[0]}]',
                      brief["text"].splitlines()[0])

    def test_signals_at_their_boundaries_written_out_by_hand(self):
        # 30 days to the contract end and a call 61 days old by its UTC date give signals. 31 and 60 give none.
        for end, called_at, due in (("2027-02-14", "2026-11-16T04:30:00+05:00", True),
                                    ("2027-02-15", "2026-11-15T23:30:00-05:00", False)):
            account = shape(contacts=2, opportunities=2)
            account["contracts"][0]["end_date"] = end
            account["contacts"][0].update(name="Zoë", title="")
            account["opportunities"][0].update(stage="negotiation", salesforce_stage="Legal / Redlines")
            account["call_notes"] = [{"called_at": called_at, "body": "Dated."}, {"called_at": "last week", "body": ""}]
            rows = org_rows(("signals", end), account)
            _, notes, messages = self.serve(rows, account)
            lines = self.brief(rows, notes, messages)[0]["text"].splitlines()
            role = 'contact "Zo\\u00eb" has no role recorded.'
            self.assertEqual([line[len("- Signal: "):-len(" " + " ".join(citations(line)))] for line in lines
                              if line.startswith("- Signal: ")], [role] if not due else [
                f"contract end is in 30 days and opportunity opp-sf-{rows['Opportunity'][0]['Id']} "
                f'{quote(rows["Opportunity"][0]["Name"])} is still open at stage "Legal / Redlines".', role,
                f"the latest dated call note {notes[0][0]} is 61 days old as of 2027-01-15."])


if __name__ == "__main__":
    unittest.main()
