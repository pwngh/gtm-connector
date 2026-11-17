"""Local administration, HTTPS demonstration, and audit inspection."""

import argparse
from contextlib import closing
import json
from pathlib import Path
import sqlite3
import sys
import time

from . import store
from .seed import DEMO_AS_OF


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
    for name in ("demo", "serve", "audit", "brief", "check-brief"):
        command = commands.add_parser(name)
        command.add_argument("--state-dir", type=Path, default=Path("var/v2"))
        if name == "audit":
            command.add_argument("--brief", help="filter the caller-supplied brief/run correlation ID")
        elif name == "brief":
            command.add_argument("account_id")
            command.add_argument("--without-calls", action="store_true", help="request only crm:read")
            command.add_argument("--as-of", default=DEMO_AS_OF)
            command.add_argument("--json", action="store_true", help="print the full evidence artifact")
        elif name == "check-brief":
            command.add_argument("brief_id")
            command.add_argument("candidate", type=Path, help="candidate text file to check against the saved evidence")
    args = parser.parse_args(argv)

    try:
        if args.command == "brief":
            from .briefing import create_brief
            from .runtime import Runtime
            with Runtime(args.state_dir) as runtime:
                result = create_brief(runtime, args.account_id, include_calls=not args.without_calls,
                                      as_of=args.as_of)
            if not args.json:
                print(f"Brief: {result['brief_id']}\nAs of: {result['as_of']}\n\n{result['text']}")
                return 0
        elif args.command == "check-brief":
            from .briefing import check_saved_brief
            check_saved_brief(args.state_dir, args.brief_id, args.candidate.read_text())
            result = {"status": "valid", "brief_id": args.brief_id}
        elif args.command == "demo":
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
