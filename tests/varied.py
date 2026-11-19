"""Data books: invented accounts, some written by other models, shaped for each source.

A book is one JSON file in tests/books. `demo_rows` turns one of its accounts into rows for the three
demo stores. `expected` restates, from those rows alone, what a brief must say about them.
"""

from contextlib import closing
from datetime import date, datetime, timezone
import json
from pathlib import Path
import sqlite3

from gtm_connector import strict_json


AS_OF = date(2027, 1, 15)
BOOKS = {path.stem: path for path in sorted((Path(__file__).parent / "books").glob("*.json"))}
STAGES = ("discovery", "evaluation", "proposal", "negotiation", "closed_won", "closed_lost")
# A line that no single row states: it is derived from several rows, a date, or an empty read.
DERIVED = ("- Signal: ", "- Renewal is in ", "- Renewal was ", "- Renewal is due on ", "- No ",
           "- Source conflict: ", "- Three-source conflict: ", "- Proposed follow-up: ")


def read(name):
    return strict_json.loads(BOOKS[name].read_bytes())["accounts"]


def demo_rows(number, account):
    """One account as rows for the crm, calls, and slack stores, under the id acc-<number>."""
    identifier = f"acc-{number}"
    return {
        "crm": [("accounts", (identifier, account["name"], account["industry"], account["renewal_date"],
                              account["arr_cents"], account["risk_status"], account["risk_reason"]))]
               + [("contacts", (f"con-{number}-{index:03d}", identifier, row["name"], row["title"], row["email"]))
                  for index, row in enumerate(account["contacts"])]
               + [("opportunities", (f"opp-{number}-{index:03d}", identifier, row["name"], row["stage"],
                                     row["amount_cents"], row["close_date"]))
                  for index, row in enumerate(account["opportunities"])],
        "calls": [("call_notes", (f"note-{number}-{index:03d}", identifier, row["called_at"], row["body"]))
                  for index, row in enumerate(account["call_notes"])],
        "slack": [("messages", (f"msg-{number}-{index:03d}", identifier, row["channel"], row["author"],
                                row["sent_at"], row["body"]))
                  for index, row in enumerate(account["slack_messages"])],
    }


def load(state, *accounts):
    for database in ("crm", "calls", "slack"):
        with closing(sqlite3.connect(Path(state) / f"{database}.sqlite3")) as connection, connection:
            for rows in accounts:
                for table, row in rows[database]:
                    connection.execute(f"INSERT INTO {table} VALUES ({', '.join('?' * len(row))})", row)


def _money(cents):
    return f"USD {cents // 100:,}.{cents % 100:02}"


def _days(count):
    return f"{count} day" if count == 1 else f"{count} days"


def _instant(text):
    try:
        moment = datetime.fromisoformat(text.replace("Z", "+00:00"))
        return moment.astimezone(timezone.utc) if moment.tzinfo else None
    except (ValueError, OverflowError):
        return None


def expected(rows, brief_id):
    """Return (lines every brief must contain, the signal lines in order), each with its citations."""
    quote = lambda text: json.dumps(text, ensure_ascii=True)  # noqa: E731
    run = f"[run/{brief_id}]"
    (_, (account_id, name, industry, renewal, arr, risk, reason)), *others = rows["crm"]
    ref = f"[crm/accounts/{account_id}]"
    lines = [f"- Account: {quote(name)}. {ref}", f"- Industry: {quote(industry)}. {ref}",
             f"- Annual recurring revenue: {_money(arr)}. {ref}", f"- Renewal date: {renewal}. {ref}",
             f"- CRM risk status: {quote(risk)}. {ref}"]
    if reason:
        lines.append(f"- CRM risk reason: {quote(reason)}. {ref}")
    days = (date.fromisoformat(renewal) - AS_OF).days
    when = (f"renewal is in {_days(days)}" if days > 0 else
            f"renewal was {_days(-days)} ago" if days < 0 else "renewal is due today")
    renewals, roles = [], []
    for table, row in others:
        if table == "contacts":
            identifier, _, person, title, email = row
            role = quote(title) if title else "not provided"
            lines.append(f"- Contact {quote(person)}: role {role}; email {quote(email)}. [crm/contacts/{identifier}]")
            if not title:
                roles.append(f"- Signal: contact {quote(person)} has no role recorded. [crm/contacts/{identifier}]")
        else:
            identifier, _, deal, stage, cents, close = row
            lines.append(f"- CRM opportunity {identifier} {quote(deal)}: stage {quote(stage)}; amount {_money(cents)}; "
                         f"close date {close}. [crm/opportunities/{identifier}]")
            if days <= 30 and not stage.startswith("closed_"):
                renewals.append(f"- Signal: {when} and opportunity {identifier} {quote(deal)} is still open at stage "
                                f"{quote(stage)}. {ref} [crm/opportunities/{identifier}] {run}")
    signals = renewals
    if risk == "at_risk":
        signals.append("- Signal: CRM marks this account at risk" + (f": {quote(reason)}. " if reason else ". ") + ref)
    signals += roles
    timed = []
    for _, (identifier, _, called_at, body) in rows["calls"]:
        lines.append(f"- Untrusted call note {identifier} at {quote(called_at)} says {quote(body)}. "
                     f"[calls/call_notes/{identifier}]")
        if (moment := _instant(called_at)) is not None:
            timed.append((moment, identifier))
    if timed:
        moment, identifier = max(timed)
        age = (AS_OF - moment.date()).days
        if age > 60:
            dated = "" if len(timed) == len(rows["calls"]) else "dated "
            signals.append(f"- Signal: the latest {dated}call note {identifier} is {_days(age)} old as of {AS_OF}. "
                           f"[calls/call_notes/{identifier}] {run}")
    for _, (identifier, _, channel, author, sent_at, body) in rows["slack"]:
        lines.append(f"- Untrusted Slack message {identifier} at {quote(sent_at)} in {quote(channel)} from "
                     f"{quote(author)} says {quote(body)}. [slack/messages/{identifier}]")
    return lines, signals
