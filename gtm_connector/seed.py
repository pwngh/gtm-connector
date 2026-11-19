"""A deterministic book of invented accounts, contacts, and renewal opportunities."""

from datetime import date, timedelta
import sqlite3


DEMO_AS_OF = "2027-01-15"
ACCOUNT_COUNT = 30

# The invented people who sell. A speaker or Slack author is a seller only when listed.
SELLERS = ("Morgan Reed", "Quinn Moss", "Avery Linden")
# The account the demo opens: a law firm whose CRM record looks healthy.
SHOWCASE = 3

# Company, industry, contact, role, and reserved email domain. All are invented.
_BOOK = (
    ("Alder Vale Packaging", "Manufacturing", "Mira Vale", "Operations director", "alder-vale.example"),
    ("Copper Finch Distribution", "Distribution", "Rowan Bell", "Purchasing director", "copper-finch.example"),
    ("Thornfield Wren LLP", "Legal services", "Marian Holt", "Deputy general counsel", "thornfield-wren.example"),
    ("Willow Reach Clinics", "Healthcare", "Jordan Hale", "Practice operations lead", "willow-reach.example"),
    ("Bracken Ridge Components", "Manufacturing", "Casey North", "Plant systems manager", "bracken-ridge.example"),
    ("Harborlight Field Services", "Transportation", "Ellis Rowan",
     "Field operations manager", "harborlight-field.example"),
    ("Juniper Crest Learning", "Education", "Dana Crest", "", "juniper-crest.example"),
    ("Mossbank Builders", "Construction", "Avery Lane", "Project controls manager", "mossbank.example"),
    ("Granite Kite Energy", "Energy", "Remy Brook", "Asset operations lead", "granite-kite.example"),
    ("Orchard Span Foods", "Food production", "Taylor Reed", "Supply planning manager", "orchard-span.example"),
    ("Birch Current Systems", "Software", "Cameron Pike", "Customer operations lead", "birch-current.example"),
    ("Silver Alder Academy", "Education", "Jamie Wren", "Learning systems manager", "silver-alder.example"),
    ("Redfern Equipment", "Manufacturing", "Quinn Hart", "Service operations director", "redfern-equipment.example"),
    ("Meadow Arc Supply", "Distribution", "Sage Flynn", "Inventory planning lead", "meadow-arc.example"),
    ("Lantern Bay Studios", "Media", "Alex Rowan", "Studio operations manager", "lantern-bay.example"),
    ("Pine Hollow Housing", "Property management", "Sam Ellis", "Resident services director", "pine-hollow.example"),
    ("Amber Trail Logistics", "Transportation", "Drew Linden", "Fleet operations manager", "amber-trail.example"),
    ("Cobalt Fern Research", "Research", "Robin Vale", "Lab operations director", "cobalt-fern.example"),
    ("Maple Shore Software", "Software", "Lee Mercer", "Business systems owner", "maple-shore.example"),
    ("Stonepath Community College", "Education", "Noel Avery", "Student services lead", "stonepath.example"),
    ("Dovetail Grove Fabrication", "Manufacturing", "Arden Lake", "Production planning lead", "dovetail-grove.example"),
    ("Baywillow Retail Group", "Retail", "Parker Moss", "Retail operations director", "baywillow.example"),
    ("Sunridge Care Network", "Healthcare", "Reese Morgan", "Scheduling systems owner", "sunridge-care.example"),
    ("Blue Elm Engineering", "Professional services", "Finley Ash", "Delivery operations manager", "blue-elm.example"),
    ("Copperleaf Farm Cooperative", "Agriculture", "Devon Shore", "Member services manager", "copperleaf-farm.example"),
    ("Windward Finch Travel", "Travel", "Hayden Reed", "Service operations lead", "windward-finch.example"),
    ("Northgrove Arts Center", "Arts", "Riley Finch", "Program operations director", "northgrove-arts.example"),
    ("Aspen Loom Textiles", "Manufacturing", "Skyler Bell", "Production systems manager", "aspen-loom.example"),
    ("Hearthline Advisory", "Professional services", "Marlow Quinn", "Client operations lead", "hearthline.example"),
    ("Riverbend Civic Services", "Public services", "Jules Hart",
     "Service delivery manager", "riverbend-civic.example"),
)


def _existing_fixture(connection: sqlite3.Connection) -> bool:
    """Recognize the complete book by identity. Later edits and added accounts are kept."""
    prefixes = {"accounts": "acc", "contacts": "con", "opportunities": "opp"}
    actual = {
        table: {row[0] for row in connection.execute(f"SELECT id FROM {table}")}
        for table in prefixes
    }
    if not any(actual.values()):
        if connection.execute("SELECT COUNT(*) FROM activities").fetchone()[0]:
            raise ValueError("Refusing to seed a database with orphan activities.")
        return False
    for table, prefix in prefixes.items():
        expected = {f"{prefix}-{number:03}" for number in range(1, ACCOUNT_COUNT + 1)}
        if not expected <= actual[table]:
            raise ValueError(
                "Database contains a partial or unknown fixture; use a fresh database path."
            )
        if table != "accounts":
            for row in connection.execute(f"SELECT id, account_id FROM {table}"):
                if row[0] in expected and row[1] != "acc-" + row[0].rsplit("-", 1)[1]:
                    raise ValueError("Fixture record has an unexpected account association.")
    if connection.execute("PRAGMA foreign_key_check").fetchone() is not None:
        raise ValueError("Database has invalid foreign-key references.")
    return True


def seed(connection: sqlite3.Connection) -> None:
    """Insert CRM rows atomically or preserve an existing complete fixture.

    Call store.initialize() first. IDs remain stable across fresh databases and
    reseeds. Names and business details are invented; email domains are reserved.
    """
    connection.execute("SAVEPOINT seed_fixture")
    try:
        if not _existing_fixture(connection):
            stages = ("discovery", "evaluation", "proposal", "negotiation")
            for number in range(1, ACCOUNT_COUNT + 1):
                name, industry, contact, role, domain = _BOOK[number - 1]
                suffix = f"{number:03}"
                account_id = f"acc-{suffix}"
                renewal = (date(2027, 1, 29) if number == 7 else date(2027, 2, 8) if number == SHOWCASE else
                           date(2027, 2, 1) + timedelta(days=(number - 1) * 7))
                arr = 450_000_00 if number == SHOWCASE else (18_000 + number * 2_500) * 100
                risk_status = "at_risk" if number == 2 else "healthy"
                risk_reason = "Renewal budget is not approved." if number == 2 else ""
                connection.execute(
                    "INSERT INTO accounts "
                    "(id, name, industry, renewal_date, arr_cents, currency, risk_status, risk_reason) "
                    "VALUES (?, ?, ?, ?, ?, 'USD', ?, ?)",
                    (account_id, name, industry, renewal.isoformat(),
                     arr, risk_status, risk_reason),
                )
                connection.execute(
                    "INSERT INTO contacts VALUES (?, ?, ?, ?, ?)",
                    (f"con-{suffix}", account_id, contact, role,
                     f"{contact.split()[0].lower()}@{domain}"),
                )
                stage = "negotiation" if number == SHOWCASE else stages[(number - 1) % len(stages)]
                connection.execute(
                    "INSERT INTO opportunities VALUES (?, ?, ?, ?, ?, 'USD', ?)",
                    (f"opp-{suffix}", account_id, f"{name} 2027 renewal",
                     stage, arr, (renewal - timedelta(days=7)).isoformat()),
                )
        connection.execute("RELEASE SAVEPOINT seed_fixture")
    except Exception:
        connection.execute("ROLLBACK TO SAVEPOINT seed_fixture")
        connection.execute("RELEASE SAVEPOINT seed_fixture")
        raise
