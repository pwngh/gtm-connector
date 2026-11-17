"""Local fixture inspection; the authenticated MCP interface comes later."""

import argparse
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys

from . import store


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Inspect an invented CRM fixture.")
    commands = parser.add_subparsers(dest="command", required=True)
    for name, help_text in (
        ("seed", "create and seed a local database without overwriting records"),
        ("accounts", "search account names by literal substring"),
        ("account", "print one account and its related records"),
        ("demo", "seed the database and print the foundation demonstration"),
    ):
        command = commands.add_parser(name, help=help_text)
        command.add_argument("--db", type=Path, default=Path("var/crm.sqlite3"))
        if name == "accounts":
            command.add_argument("--query", default="")
        elif name == "account":
            command.add_argument("account_id")
    args = parser.parse_args(argv)

    try:
        create = args.command in {"seed", "demo"}
        if not create and not args.db.is_file():
            raise ValueError("database not found; run the seed command first")
        with closing(store.connect(args.db, readonly=not create)) as conn:
            if create:
                store.initialize(conn)
                store.seed(conn)
            if args.command == "seed":
                result = {"counts": store.counts(conn)}
            elif args.command == "accounts":
                result = store.search_accounts(conn, args.query)
            elif args.command == "account":
                result = store.get_account(conn, args.account_id)
                if result is None:
                    raise ValueError(f"account not found: {args.account_id}")
            else:
                result = {
                    "status": "foundation",
                    "counts": store.counts(conn),
                    "account": store.get_account(conn, "acc-001"),
                }
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    except (OSError, sqlite3.Error, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
