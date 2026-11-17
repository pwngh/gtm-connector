"""Local administration, HTTPS demonstration, and audit inspection."""

import argparse
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys
import time

from . import store


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run the invented CRM/calls connector.")
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("seed", "accounts", "account"):
        command = commands.add_parser(name, help="local CRM administration (direct file access)")
        command.add_argument("--db", type=Path, default=Path("var/v2/crm.sqlite3"))
        if name == "accounts":
            command.add_argument("--query", default="")
        elif name == "account":
            command.add_argument("account_id")
    for name in ("demo", "serve", "audit"):
        command = commands.add_parser(name)
        command.add_argument("--state-dir", type=Path, default=Path("var/v2"))
        if name == "audit":
            command.add_argument("--brief", help="filter the caller-supplied brief/run correlation ID")
    args = parser.parse_args(argv)

    try:
        if args.command == "demo":
            from .demo import run
            result = run(args.state_dir)
        elif args.command == "audit":
            from .audit import AuditLog
            path = args.state_dir / "audit.sqlite3"
            if not path.is_file():
                raise ValueError("audit database not found; run the demo first")
            result = AuditLog(path).read(args.brief)
        elif args.command == "serve":
            from .runtime import Runtime
            with Runtime(args.state_dir) as runtime:
                print(json.dumps({"resource": runtime.config["resource"], "issuer": runtime.config["issuer"],
                                  "owner_config": str(runtime.directory / "owner.json")}), flush=True)
                try:
                    while True:
                        time.sleep(0.5)
                except KeyboardInterrupt:
                    return 0
        else:
            create = args.command == "seed"
            if not create and not args.db.is_file():
                raise ValueError("database not found; run the seed command first")
            with closing(store.connect(args.db, readonly=not create)) as connection:
                if create:
                    store.initialize(connection)
                    store.seed(connection)
                    result = {"counts": store.counts(connection)}
                elif args.command == "accounts":
                    result = store.search_accounts(connection, args.query)
                else:
                    result = store.get_account(connection, args.account_id)
                    if result is None:
                        raise ValueError(f"account not found: {args.account_id}")
        print(json.dumps(result, indent=2, ensure_ascii=False))
        return 0
    except (OSError, sqlite3.Error, ValueError, RuntimeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
