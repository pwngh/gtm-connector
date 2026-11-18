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
    parser = argparse.ArgumentParser(description="Run the scoped CRM, calls, and Slack connector.")
    commands = parser.add_subparsers(dest="command", required=True)
    for name in ("seed", "accounts", "account"):
        command = commands.add_parser(name, help="local CRM administration (direct file access)")
        command.add_argument("--db", type=Path, default=Path("var/v2/crm.sqlite3"))
        if name == "accounts":
            command.add_argument("--query", default="")
        elif name == "account":
            command.add_argument("account_id")
    evaluate = commands.add_parser("evaluate", help="score 20 fixed accounts in a fresh temporary fixture")
    evaluate.add_argument("--adapter", choices=("fixture", "ollama"), default="fixture")
    evaluate.add_argument("--model", help="an already installed local Ollama model")
    evaluate.add_argument("--with-slack", action="store_true", help="include the isolated Slack fixture")
    for name in ("demo", "serve", "audit", "brief", "check-brief", "usage", "inspector-config"):
        command = commands.add_parser(name)
        command.add_argument("--state-dir", type=Path, default=Path("var/v2"))
        if name in ("serve", "brief"):
            command.add_argument("--crm-backend", choices=("sqlite", "salesforce"), default="sqlite",
                                 help="Salesforce supports read tools only; renewal briefs require SQLite fixtures")
        if name in ("demo", "brief"):
            command.add_argument("--with-slack", action="store_true",
                                 help="include Slack reads and a checked follow-up draft")
        if name == "inspector-config":
            command.add_argument("--scope", action="append", required=True,
                                 choices=("crm:read", "calls:read", "crm:write", "slack:read"),
                                 help="grant only these scopes; repeat for each grant")
        elif name in ("audit", "usage"):
            command.add_argument("--brief", help="filter the caller-supplied brief/run correlation ID")
        elif name == "brief":
            command.add_argument("account_id")
            command.add_argument("--without-calls", action="store_true", help="request only crm:read")
            command.add_argument("--as-of", default=DEMO_AS_OF)
            command.add_argument("--json", action="store_true", help="print the full evidence artifact")
            command.add_argument("--adapter", choices=("fixture", "ollama"), default="fixture")
            command.add_argument("--model", help="an already installed local Ollama model")
        elif name == "check-brief":
            command.add_argument("brief_id")
            command.add_argument("candidate", type=Path, help="candidate text file to check against the saved evidence")
    args = parser.parse_args(argv)

    try:
        if args.command == "brief" and args.crm_backend != "sqlite":
            raise ValueError("renewal_fields_unmapped: Salesforce brief generation is unavailable")
        adapter = None
        if args.command in ("brief", "evaluate"):
            if args.adapter == "fixture" and args.model is not None:
                raise ValueError("--model requires --adapter ollama")
            if args.adapter == "ollama":
                if not args.model:
                    raise ValueError("--adapter ollama requires --model with an installed local model")
                from .ollama import OllamaAdapter
                adapter = OllamaAdapter(args.model)
        if args.command == "inspector-config":
            from .inspector import write_inspector_config
            result = write_inspector_config(args.state_dir, args.scope)
        elif args.command == "evaluate":
            from .evaluation import run_evaluation
            result = run_evaluation(
                adapter=adapter, include_slack=args.with_slack,
                command=sys.orig_argv if argv is None else [sys.executable, "-m", "gtm_connector", *argv],
            )
        elif args.command == "usage":
            from .usage import build_usage_report
            result = build_usage_report(args.state_dir, args.brief)
        elif args.command == "brief":
            from .briefing import create_brief
            from .runtime import Runtime
            with Runtime(args.state_dir) as runtime:
                result = create_brief(runtime, args.account_id, include_calls=not args.without_calls,
                                      as_of=args.as_of, adapter=adapter, include_slack=args.with_slack)
            if not args.json:
                print(f"Brief: {result['brief_id']}\nAs of: {result['as_of']}\n\n{result['text']}")
                return 0
        elif args.command == "check-brief":
            from .briefing import check_saved_brief
            check_saved_brief(args.state_dir, args.brief_id, args.candidate.read_text())
            result = {"status": "valid", "brief_id": args.brief_id}
        elif args.command == "demo":
            from .demo import run
            result = run(args.state_dir, include_slack=args.with_slack)
        elif args.command == "audit":
            from .audit import read_audit
            result = read_audit(args.state_dir / "audit.sqlite3", args.brief)
        elif args.command == "serve":
            from .runtime import Runtime
            crm_reader = None
            if args.crm_backend == "salesforce":
                from .salesforce import SalesforceReadOnlyAdapter
                crm_reader = SalesforceReadOnlyAdapter.from_env()
            with Runtime(args.state_dir, crm_reader=crm_reader) as runtime:
                print(json.dumps({"resource": runtime.config["resource"], "issuer": runtime.config["issuer"],
                                  "crm_backend": runtime.crm_backend,
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
        return 2 if args.command == "evaluate" and result["status"] != "passed" else 0
    except (OSError, sqlite3.Error, ValueError, RuntimeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
