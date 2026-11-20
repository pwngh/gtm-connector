"""Every book account, served from an invented Salesforce org, gives its exact card or a documented refusal.

The upstream is an httpx2.MockTransport that answers the adapter's REST calls. With a Salesforce CRM the gateway
still reads call notes and Slack messages from the demo stores by account id, so each book account's notes and
messages are loaded there under its acc-sf- id.
"""

from collections import Counter
from contextlib import closing
from datetime import date
from decimal import Decimal
import hashlib
import json
from pathlib import Path
import re
import sqlite3
import tempfile
import unittest
import uuid

import httpx2

from gtm_connector.briefing import create_brief
from gtm_connector.runtime import Runtime
from gtm_connector.salesforce import SalesforceReadError, SalesforceReadOnlyAdapter
from tests import varied
from tests.varied import days, quoted


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
STATUS_CODES = ("Draft", "InApproval", "Activated")
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
                               "amount_cents": index, "close_date": "2026-12-01"} for index in range(opportunities)],
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


def selected(contracts):
    """docs/salesforce.md, Briefs: the earliest non-null EndDate on or after the date, ties by id, or None."""
    eligible = [row for row in contracts if row["StatusCode"] in STATUS_CODES and row["EndDate"] is not None
                and date.fromisoformat(row["EndDate"]) >= AS_OF]
    return min(eligible, key=lambda row: (row["EndDate"], row["Id"]), default=None)


def outcome(rows):
    """The documented path for one account: a brief, or the limit code."""
    if any(len(rows[name]) > 1000 for name in ("Contact", "Opportunity", "Contract")):
        return "salesforce_result_limit"
    return "brief"


def read_records(rows, notes, messages):
    """What the evidence must hold for every row that was served, keyed by its record reference."""
    account_id = "acc-sf-" + rows["Account"][0]["Id"]
    records = {}
    for name, (table, prefix, fields) in MAPPING.items():
        for row in rows[name]:
            record = {field: row[source] for field, source in fields.items()}
            if name != "Account":
                record["account_id"] = account_id
            if name == "Opportunity":
                record.update(amount_decimal=None if row["Amount"] is None else str(row["Amount"]), currency=None)
            records[f"crm/{table}/{prefix}-sf-{row['Id']}"] = record
    for identifier, _, called_at, speaker, body in notes:
        records[f"calls/call_notes/{identifier}"] = {"account_id": account_id, "called_at": called_at,
                                                    "speaker": speaker, "body": body}
    for identifier, _, channel, author, sent_at, body, files in messages:
        records[f"slack/messages/{identifier}"] = {"account_id": account_id, "channel": channel, "author": author,
                                                  "sent_at": sent_at, "body": body, "files": files}
    return records


def numbers(rows):
    """docs/briefs.md, Units: the account is 1, then contacts, contracts, and opportunities, each by ID."""
    ordered = [row["Id"] for name in ("Account", "Contact", "Contract", "Opportunity")
               for row in sorted(rows[name], key=lambda row: row["Id"])]
    return {native: number for number, native in enumerate(ordered, 1)}


def expected(rows, notes, messages, brief):
    """What a card must say about the served rows, written from them and docs/briefs.md: the fixed value lines,
    one line per opportunity and contact, the other contracts that are shown, the signals in order, and what
    was not read."""
    number = numbers(rows)
    account, contract = rows["Account"][0], selected(rows["Contract"])
    by_id = lambda name: sorted(rows[name], key=lambda row: row["Id"])  # noqa: E731
    signals, left, when = [], None, None
    if contract:
        left = (date.fromisoformat(contract["EndDate"]) - AS_OF).days
        when = f"in {days(left)}" if left else "today"
        candidate = "" if contract["StatusCode"] == "Activated" else "; a planning candidate, not activated"
        fixed = [f"  Contract end: {contract['EndDate']}, {when}; status {quoted(contract['Status'])}{candidate} "
                 f"[{number[contract['Id']]}]"]
    else:
        # No contract is ahead: the card says so, and the latest one that ended is a signal.
        fixed = [f"  Contract end: Missing: no contract ends on or after {AS_OF} [1]"]
        ended = [row for row in rows["Contract"] if row["EndDate"]]
        if ended:
            last = max(ended, key=lambda row: (row["EndDate"], row["Id"]))
            age = (AS_OF - date.fromisoformat(last["EndDate"])).days
            signals.append(f"  The latest contract end date, {last['EndDate']}, was {days(age)} ago. "
                           f"[{number[last['Id']]}]")
    fixed.append(f"  Account: {quoted(account['Name'])}; industry {quoted(account['Industry'])} [1]")
    offers, still_open = [], []
    for row in by_id("Opportunity"):
        amount = "Missing" if row["Amount"] is None else f"{row['Amount']} (currency Missing)"
        offers.append(f"  Opportunity: {quoted(row['Name'])}; stage {quoted(row['StageName'])}; amount {amount}; "
                      f"close date {row['CloseDate']}; closed {str(row['IsClosed']).lower()}; "
                      f"won {str(row['IsWon']).lower()} [{number[row['Id']]}]")
        if left is not None and left <= 30 and not row["IsClosed"]:
            still_open.append(row)
    # More than five records raising one signal are counted in one line that cites the first five of them.
    if len(still_open) > varied.SHOWN:
        still_open = [f"  The contract end is {when} and {len(still_open)} opportunities are still open. "
                      f"[{number[contract['Id']]}]" + varied.counted(number[row["Id"]] for row in still_open)]
    else:
        still_open = [f"  The contract end is {when} and this opportunity is still open at stage "
                      f"{quoted(row['StageName'])}. [{number[contract['Id']]}][{number[row['Id']]}]"
                      for row in still_open]
    # An open opportunity that is to close after the contract end the card counts from.
    late = [row for row in by_id("Opportunity")
            if contract and not row["IsClosed"] and row["CloseDate"] > contract["EndDate"]]
    if len(late) > varied.SHOWN:
        late = [f"  {len(late)} open opportunities have a close date after the contract end date, "
                f"{contract['EndDate']}. [{number[contract['Id']]}]"
                + varied.counted(number[row["Id"]] for row in late)]
    else:
        late = [f"  This open opportunity's close date, {row['CloseDate']}, is after the contract end date, "
                f"{contract['EndDate']}. [{number[contract['Id']]}][{number[row['Id']]}]" for row in late]
    # The contract that sets the contract end has its own line. Of the others, the first five by ID are shown.
    unselected = [row for row in by_id("Contract") if row is not contract]
    others = [f"  Contract: status {quoted(row['Status'])}; "
              f"{'no end date' if row['EndDate'] is None else 'ends ' + row['EndDate']} [{number[row['Id']]}]"
              for row in unselected[:varied.SHOWN]]
    people, roles = [], []
    for row in by_id("Contact"):
        people.append(f"  Contact: {quoted(row['Name'])}; role {quoted(row['Title'])}; email {quoted(row['Email'])} "
                      f"[{number[row['Id']]}]")
        if row["Title"] is None:
            roles.append(number[row["Id"]])
    roles = ([f"  {len(roles)} contacts have no role recorded. " + varied.counted(roles)] if len(roles) > varied.SHOWN
             else [f"  This contact has no role recorded. [{unit}]" for unit in roles])
    stale = varied.stale_call([(f"calls/call_notes/{row[0]}", row[2]) for row in notes], brief)
    not_read = ["  CRM: annual recurring revenue and renewal risk are not mapped from standard Salesforce fields.",
                "  Calls: no call record was returned for this account." if not notes else
                f"  Calls: {len(notes)} call {'record was' if len(notes) == 1 else 'records were'} read. "
                "Recordings and anything outside those records were not read.",
                "  Slack: no message was returned for this account." if not messages else
                f"  Slack: {len(messages)} {'message was' if len(messages) == 1 else 'messages were'} read. "
                "Other channels were not read."]
    if not rows["Opportunity"]:
        not_read.append("  CRM: no opportunity was returned for this account.")
    if len(unselected) > varied.SHOWN:
        not_read.append(varied.more(len(unselected), "contracts"))
    # docs/briefs.md, Signals: the contract end, a late close date, contacts with no role, then the stale call.
    return {"fixed": fixed, "opportunities": offers, "contracts": others, "contacts": people,
            "signals": signals + still_open + late + roles + stale, "not_read": not_read}


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
        notes = [(f"note-{self.number}-{index:03d}", account_id, row["called_at"], None, row["body"])
                 for index, row in enumerate(account["call_notes"])]
        messages = [(f"msg-{self.number}-{index:03d}", account_id, row["channel"], row["author"], row["sent_at"],
                     row["body"], 0) for index, row in enumerate(account["slack_messages"])]
        for database, table, loaded in (("calls", "call_notes", notes), ("slack", "messages", messages)):
            with closing(sqlite3.connect(self.state / f"{database}.sqlite3")) as connection, connection:
                for row in loaded:
                    connection.execute(f"INSERT INTO {table} VALUES ({', '.join('?' * len(row))})", row)
        return account_id, notes, messages

    def saved(self):
        return sorted(path.name for path in (self.state / "briefs").glob("*"))

    def brief(self, rows, notes=(), messages=()):
        """Create the card and hold it to everything the served rows imply."""
        account_id = "acc-sf-" + rows["Account"][0]["Id"]
        brief = create_brief(self.runtime, account_id, as_of=AS_OF.isoformat(), include_slack=True)
        card, evidence = varied.hold(self, brief), brief["evidence"]
        # Every row that was served is in the evidence with the values the org returned.
        read = read_records(rows, notes, messages)
        self.assertEqual({record["record_ref"] for record in evidence["records"]}, set(read))
        for record in evidence["records"]:
            wanted = read[record["record_ref"]]
            self.assertEqual({field: record[field] for field in wanted}, wanted)
        # The signals, the values, and what was not read are exactly those the rows imply.
        wanted = expected(rows, notes, messages, brief)
        self.assertEqual(card["ATTENTION"], wanted["signals"])
        contracts = [line for line in card["RECORD VALUES"] if line.startswith("  Contract: ")]
        self.assertEqual(contracts, wanted["contracts"])
        rest = {**card, "RECORD VALUES": [line for line in card["RECORD VALUES"] if line not in contracts]}
        varied.hold_values(self, rest, wanted["fixed"], (("Opportunity", "opportunities", wanted["opportunities"]),
                                                         ("Contact", "contacts", wanted["contacts"])))
        self.assertLessEqual(set(wanted["not_read"]), set(card["NOT READ"]))
        self.assertEqual([line for line in card["NOT READ"]
                          if re.search(r" more contracts? w", line) or "no opportunity" in line],
                         wanted["not_read"][3:])
        return brief

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
            return self.brief(rows, notes, messages)
        self.refused(rows, SalesforceReadError, to)

    def tearDown(self):
        self.assertEqual(self.org.unexpected, [])
        # Startup read each describe once, and no later read repeated it.
        self.assertEqual([self.org.gets["describe", name] for name in FIELDS], [1, 1, 1, 1])


class BookTests(SalesforcePath):
    def test_every_book_account_gives_its_exact_card_or_its_documented_refusal(self):
        self.assertGreaterEqual(len(varied.BOOKS), 1)
        paths = Counter()
        for name in varied.BOOKS:
            for index, account in enumerate(varied.read(name)):
                rows = org_rows((name, index), account)
                _, notes, messages = self.serve(rows, account)
                paths[outcome(rows)] += 1
                with self.subTest(book=name, account=account["name"][:40], path=outcome(rows)):
                    self.held(rows, notes, messages, to=outcome(rows))
        # Every book account gets a card, including the ones with no contract left to renew.
        self.assertEqual(set(paths), {"brief"})

    def test_two_contracts_are_both_read_and_both_shown(self):
        # A contract that was not selected is still a unit with its own line.
        account = shape(contracts=2)
        account["contracts"][1]["end_date"] = "2028-06-01"
        rows = org_rows("two contracts", account)
        self.serve(rows)
        brief = self.brief(rows)
        later = next(row["Id"] for row in rows["Contract"] if row["EndDate"] == "2028-06-01")
        self.assertIn(f'  Contract: status "Activated"; ends 2028-06-01 [{numbers(rows)[later]}]',
                      brief["text"].split("\n"))
        self.assertEqual(sum(unit["record_ref"].startswith("crm/contracts/") for unit in brief["units"]), 2)


class BuiltTests(SalesforcePath):
    """Accounts built in code. Each is held to its outcome and its exact card."""

    def test_the_contract_that_sets_the_end_is_not_one_of_the_five_other_contracts_shown(self):
        rows = org_rows("seven contracts", shape(contracts=7))
        # The last contract by ID ends first, so it sets the contract end from outside the first five.
        last = max(rows["Contract"], key=lambda row: row["Id"])
        for row in rows["Contract"]:
            row["EndDate"] = "2027-06-01" if row is last else "2028-06-01"
        self.serve(rows)
        card = varied.sections(self, self.brief(rows)["text"])
        unit = numbers(rows)
        self.assertEqual(card["RECORD VALUES"][0],
                         f'  Contract end: 2027-06-01, in 137 days; status "Activated" [{unit[last["Id"]]}]')
        others = [varied.cited(line)[0] for line in card["RECORD VALUES"] if line.startswith("  Contract: ")]
        self.assertEqual(others, sorted(unit[row["Id"]] for row in rows["Contract"] if row is not last)[:5])
        # Six other contracts, five shown, one counted: the selected one is in neither number.
        self.assertIn("  CRM: 1 more contract was read and is not shown (6 in all).", card["NOT READ"])

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
            signals = varied.sections(self, self.brief(rows)["text"])["ATTENTION"]
            if grouped:
                # Units: the account, seven contacts, the contract, seven opportunities. Each counted signal
                # cites five of the six records that raise it, which here are five of the first six by ID or not.
                unit = numbers(rows)
                deals = sorted(unit[row["Id"]] for row in rows["Opportunity"] if not row["IsClosed"])
                people = sorted(unit[row["Id"]] for row in rows["Contact"] if row["Title"] is None)
                self.assertEqual((len(deals), len(people)), (6, 6))
                self.assertEqual(signals, [
                    "  The contract end is in 9 days and 6 opportunities are still open. "
                    f"[{unit[rows['Contract'][0]['Id']]}]" + "".join(f"[{deal}]" for deal in deals[:5])
                    + " and 1 more",
                    "  6 contacts have no role recorded. " + "".join(f"[{person}]" for person in people[:5])
                    + " and 1 more"])
            else:
                self.assertEqual(["The contract end is in 9 days and this opportunity is still open" in line
                                  for line in signals], [True] * 5 + [False] * 5)
                self.assertEqual([line.startswith("  This contact has no role recorded. [") for line in signals],
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
        # Two bytes a character upstream is still under 1 MiB.
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
        card = varied.sections(self, self.brief(rows)["text"])
        number = numbers(rows)
        self.assertEqual(card["ATTENTION"], [
            '  The contract end is today and this opportunity is still open at stage "Proposal". '
            f"[{number[tied[0]]}][{number[rows['Opportunity'][0]['Id']]}]"])
        self.assertRegex(card["RECORD VALUES"][0],
                         rf"\A  Contract end: 2027-01-15, today; status .* \[{number[tied[0]]}\]\Z")

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
            brief = self.brief(rows, notes, messages)
            number = numbers(rows)
            role = f"This contact has no role recorded. [{number[rows['Contact'][0]['Id']]}]"
            spoken = next(unit["n"] for unit in brief["units"] if unit["kind"] == "sentence")
            self.assertEqual([line.strip() for line in varied.sections(self, brief["text"])["ATTENTION"]],
                             [role] if not due else [
                'The contract end is in 30 days and this opportunity is still open at stage "Legal / Redlines". '
                f"[{number[rows['Contract'][0]['Id']]}][{number[rows['Opportunity'][0]['Id']]}]", role,
                f"The latest dated call that was read is 61 days old. [{spoken}]"])


if __name__ == "__main__":
    unittest.main()
