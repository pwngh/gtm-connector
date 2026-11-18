"""Local administration, HTTPS demonstration, and audit inspection.

Keep parser construction independent of the server and database dependencies.
"""

import argparse
import difflib
import json
import os
from pathlib import Path
import sys
import shlex
import time

from . import __version__


COMMANDS = {
    "demo": ("Run the connector demonstration on invented records.", "demo --with-slack"),
    "brief": ("Create a cited renewal brief from authorized source reads.", "brief acc-003 --with-slack"),
    "serve": ("Start the local HTTPS connector and authorization server.", "serve --state-dir var/v2"),
    "seed": ("Create the local CRM fixture without replacing existing records.", "seed --db var/v2/crm.sqlite3"),
    "accounts": ("Search the local CRM by account name.", "accounts --query acc-003"),
    "account": ("Read one local CRM account and its contacts.", "account acc-003"),
    "audit": ("Read tool-call receipts from the local audit database.", "audit --state-dir var/v2"),
    "usage": ("Summarize tool calls and saved model token counts.", "usage --state-dir var/v2"),
    "evaluate": ("Check briefs for twenty fixed accounts in temporary state.", "evaluate --with-slack --json"),
    "check-brief": ("Check candidate text against a saved evidence snapshot.",
                    "check-brief brief-REPLACE candidate.txt --state-dir var/v2"),
    "inspector-config": ("Write a private Inspector configuration with explicit scopes.",
                         "inspector-config --scope crm:read --scope calls:read"),
}
EXIT_NAMES = {1: "local_failure", 2: "usage_error", 3: "refused", 4: "check_failed", 5: "upstream_failure",
              130: "interrupted"}


def safe_text(value):
    """Keep source values and errors on one visible terminal line."""
    text = str(value)
    return "".join(character if character.isprintable() else
                   json.dumps(character, ensure_ascii=True)[1:-1] for character in text)


def _error(message, code, next_command, *, as_json=False):
    if as_json:
        print(json.dumps({"error": {"code": EXIT_NAMES[code], "message": str(message), "next": next_command}}),
              file=sys.stderr)
    else:
        print(f"Error: {safe_text(message)}\nNext: {next_command}", file=sys.stderr)
    return code


class ParserError(ValueError):
    pass


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise ParserError(message)


def build_parser():
    parser = Parser(prog="gtm-connector",
                    description="Create cited renewal briefs from scoped CRM, calls, and Slack reads.",
                    epilog="Example: gtm-connector demo --with-slack\n"
                           "Documentation: https://github.com/pwngh/gtm-connector#readme",
                    formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--version", action="store_true", help="Show the installed version and exit.")
    parser.add_argument("--json", action="store_true", help="Write the documented JSON result for scripts.")
    commands = parser.add_subparsers(dest="command", metavar="COMMAND")
    for name, (description, example) in COMMANDS.items():
        command = commands.add_parser(name, help=description, description=description,
                                      epilog=f"Example: gtm-connector {example}",
                                      formatter_class=argparse.RawDescriptionHelpFormatter)
        command.add_argument("--json", action="store_true", default=argparse.SUPPRESS,
                             help="Write the documented JSON result for scripts.")
        if name in ("seed", "accounts", "account"):
            command.add_argument("--db", type=Path, default=Path("var/v2/crm.sqlite3"),
                                 help="Read or seed this local CRM database.")
        elif name != "evaluate":
            command.add_argument("--state-dir", type=Path, default=Path("var/v2"),
                                 help="Use this private state folder (default: var/v2).")
        if name == "accounts":
            command.add_argument("--query", default="", help="Match this literal part of an account name.")
        if name in ("account", "brief"):
            command.add_argument("account_id", help="Select an account, such as acc-003.")
        if name in ("serve", "brief"):
            command.add_argument("--crm-backend", choices=("sqlite", "salesforce"), default="sqlite",
                                 help="Select the CRM reader. Salesforce renewal fields are not mapped yet.")
        if name in ("demo", "brief", "evaluate"):
            command.add_argument("--with-slack", action="store_true",
                                 help="Include Slack evidence and a checked follow-up proposal.")
        if name in ("brief", "evaluate"):
            command.add_argument("--adapter", choices=("fixture", "ollama"), default="fixture",
                                 help="Choose the fixture drafter or an already installed local model.")
            command.add_argument("--model", help="Name an already installed local Ollama model.")
        if name == "inspector-config":
            command.add_argument("--scope", action="append", required=True,
                                 choices=("crm:read", "calls:read", "crm:write", "slack:read"),
                                 help="Grant this scope. Repeat for each explicit grant.")
        if name in ("audit", "usage"):
            command.add_argument("--brief", help="Select this brief or run correlation ID.")
        if name == "brief":
            command.add_argument("--without-calls", action="store_true", help="Omit the calls:read grant.")
            command.add_argument("--as-of",
                                 help="Calculate renewal days from YYYY-MM-DD (fixture default: 2027-01-15).")
        if name == "check-brief":
            command.add_argument("brief_id", help="Select the saved brief identifier.")
            command.add_argument("candidate", type=Path, help="Read candidate text from this file.")
    return parser


def table(headers, rows):
    """Use readable headings on terminals and complete TSV rows in pipes."""
    rows = [[safe_text(cell if cell is not None else "unknown") for cell in row] for row in rows]
    if not sys.stdout.isatty():
        return "\n".join("\t".join(row) for row in rows)
    widths = [max([len(header)] + [len(row[i]) for row in rows]) for i, header in enumerate(headers)]
    heading = "  ".join(header.ljust(widths[i]) for i, header in enumerate(headers)).rstrip()
    if "NO_COLOR" not in os.environ and os.environ.get("TERM") != "dumb":
        heading = "\033[1m" + heading + "\033[0m"
    return "\n".join([heading] + ["  ".join(cell.ljust(widths[i]) for i, cell in enumerate(row)).rstrip()
                                for row in rows])


def human_result(command, result):
    if command == "brief":
        return f"Brief: {result['brief_id']}\nAs of: {result['as_of']}\n\n{result['text']}"
    if command == "seed":
        return table(["RECORDS", "COUNT"], list(result["counts"].items()))
    if command == "accounts":
        return table(["ID", "ACCOUNT", "INDUSTRY", "RENEWAL"],
                     [[row.get(key) for key in ("id", "name", "industry", "renewal_date")] for row in result])
    if command == "account":
        fields = [[key.replace("_", " ").capitalize(), value] for key, value in result.items() if key != "contacts"]
        contacts = result["contacts"]
        return table(["FIELD", "VALUE"], fields) + "\n\nContacts\n" + table(
            ["ID", "NAME", "TITLE", "EMAIL"], [[row.get(key) for key in ("id", "name", "title", "email")]
                                              for row in contacts])
    if command == "audit":
        return table(["TIME", "TOOL", "CALLER", "DECISION", "MS", "RECEIPT", "REASON"],
                     [[row["started_at"], row["tool"], row["token_id"], row["status"],
                       round(row["elapsed_ms"], 2) if row["elapsed_ms"] is not None else None,
                       row["audit_id"], row["error"] or ""] for row in result])
    if command == "usage":
        return (table(["TOOL", "TOTAL", "OK", "ERROR", "PENDING"],
                      [[row[key] for key in ("tool", "total", "ok", "error", "pending")]
                       for row in result["by_tool"]]) + "\n\n" + table(
                           ["MODEL USAGE", "COUNT"], list(result["model_tokens"].items())))
    if command == "check-brief":
        return f"Valid: {result['brief_id']}. Every candidate statement is supported by its saved evidence."
    if command == "evaluate":
        return table(["MEASURE", "RESULT"], [
            ["Status", result["status"]], ["Accounts", result["accounts"]],
            ["Claims found", f"{result['facts_found']}/{result['expected_facts']}"],
            ["Claims cited", f"{result['facts_cited_correctly']}/{result['expected_facts']}"],
            ["Invented claims", result["facts_invented"]], ["Audit receipts", result["audit_rows"]],
            ["Activities created", result["activities_created"]]])
    if command == "inspector-config":
        return (f"Inspector configuration: {result['config']}\nCertificate: {result['certificate']}\n"
                f"Granted scopes: {', '.join(result['scopes'])}")
    if command == "demo":
        return ("Connector demonstration on invented records\n\n" + result["briefs"][0]["text"] + "\n" +
                table(["CHECK", "RESULT"], [["Read without calls:read", "Denied (HTTP 403)"],
                    ["Injected instruction", "Quoted as data; zero activities created"],
                    ["Uncited claim", "Rejected"], ["Retried write", "One activity"],
                    ["Rate limit", "HTTP 429"], ["Audit receipts", result["audit_rows"] + result["brief_audit_rows"]]]))
    raise ValueError("No display is available for this command.")


def _run(args, argv):
    # Dependencies belong to the command that uses them, after help is handled.
    from contextlib import closing
    from . import store

    if args.command == "brief" and args.crm_backend != "sqlite":
        raise PermissionError("renewal_fields_unmapped: Salesforce brief generation is unavailable")
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
        return write_inspector_config(args.state_dir, args.scope)
    if args.command == "evaluate":
        from .evaluation import run_evaluation
        return run_evaluation(adapter=adapter, include_slack=args.with_slack,
                              command=sys.orig_argv if argv is None else [sys.executable, "-m", "gtm_connector", *argv])
    if args.command == "usage":
        from .usage import build_usage_report
        return build_usage_report(args.state_dir, args.brief)
    if args.command == "brief":
        from .briefing import create_brief
        from .runtime import Runtime
        from .seed import DEMO_AS_OF
        with Runtime(args.state_dir) as runtime:
            return create_brief(runtime, args.account_id, include_calls=not args.without_calls,
                                as_of=DEMO_AS_OF if args.as_of is None else args.as_of,
                                adapter=adapter, include_slack=args.with_slack)
    if args.command == "check-brief":
        from .briefing import check_saved_brief
        check_saved_brief(args.state_dir, args.brief_id, args.candidate.read_text())
        return {"status": "valid", "brief_id": args.brief_id}
    if args.command == "demo":
        from .demo import run
        return run(args.state_dir, include_slack=args.with_slack)
    if args.command == "audit":
        from .audit import read_audit
        return read_audit(args.state_dir / "audit.sqlite3", args.brief)
    if args.command == "serve":
        from .runtime import Runtime
        crm_reader = None
        if args.crm_backend == "salesforce":
            from .salesforce import SalesforceReadOnlyAdapter
            crm_reader = SalesforceReadOnlyAdapter.from_env()
        with Runtime(args.state_dir, crm_reader=crm_reader) as runtime:
            ready = {"resource": runtime.config["resource"], "issuer": runtime.config["issuer"],
                     "crm_backend": runtime.crm_backend, "owner_config": str(runtime.directory / "owner.json")}
            if args.json:
                print(json.dumps(ready), flush=True)
            else:
                print(f"Connector ready at {ready['resource']}\nBackend: {runtime.crm_backend}\n"
                      f"State folder: {runtime.directory}\n"
                      "Next: gtm-connector inspector-config --scope crm:read "
                      f"--state-dir {shlex.quote(str(runtime.directory))}\nStop with Ctrl-C.",
                      file=sys.stderr, flush=True)
            try:
                while True:
                    time.sleep(0.5)
            except KeyboardInterrupt:
                print("Connector stopped. Local state is retained.", file=sys.stderr)
                return None
    create = args.command == "seed"
    if not create and not args.db.is_file():
        raise ValueError("database not found; run the seed command first")
    with closing(store.connect(args.db, readonly=not create)) as connection:
        if create:
            store.initialize(connection)
            store.seed(connection)
            return {"counts": store.counts(connection)}
        if args.command == "accounts":
            return store.search_accounts(connection, args.query)
        result = store.get_account(connection, args.account_id)
        if result is None:
            raise ValueError(f"account not found: {args.account_id}")
        return result


def _failure(exc, args):
    from .citations import BriefRejected
    import sqlite3
    command = args.command
    next_command = f"gtm-connector {command} --help"
    message = str(exc)
    code = 2 if isinstance(exc, ValueError) else 1
    if isinstance(exc, BriefRejected) or (command == "demo" and message.startswith("demo ")):
        code = 4
    elif isinstance(exc, PermissionError) and message.startswith("renewal_fields_unmapped"):
        code = 3
        next_command = "gtm-connector brief acc-003 --crm-backend sqlite --with-slack"
    elif (type(exc).__name__ in ("SalesforceReadError", "BriefingError", "OllamaError",
                                   "InspectorConnectionError") or
          type(exc).__module__.startswith(("httpx", "httpcore"))):
        code = 2 if message.startswith("salesforce_invalid_configuration") else 5
    elif type(exc).__name__ == "StateInUseError":
        code = 1
        next_command = f"gtm-connector audit --state-dir {shlex.quote(str(args.state_dir))}"
    elif isinstance(exc, sqlite3.Error):
        code = 1
    if "audit database not found" in message:
        next_command = f"gtm-connector demo --state-dir {shlex.quote(str(args.state_dir))}"
    elif "database not found" in message:
        next_command = f"gtm-connector seed --db {shlex.quote(str(args.db))}"
    elif "account not found" in message:
        next_command = f"gtm-connector accounts --db {shlex.quote(str(args.db))}"
    elif command == "inspector-config" and (
            isinstance(exc, FileNotFoundError) or type(exc).__name__ == "InspectorConnectionError"):
        next_command = f"gtm-connector serve --state-dir {shlex.quote(str(args.state_dir))}"
    return _error(message, code, next_command, as_json=args.json)


def _json_requested(arguments):
    # A flag after the option terminator is a literal positional argument.
    options = arguments[:arguments.index("--")] if "--" in arguments else arguments
    return "--json" in options


def _main(argv):
    arguments = list(sys.argv[1:] if argv is None else argv)
    as_json = _json_requested(arguments)
    parser = build_parser()
    if not arguments or arguments == ["--json"]:
        quickstart = {"description": parser.description, "try": ["gtm-connector demo --with-slack",
                      "gtm-connector brief acc-003 --with-slack"], "help": "gtm-connector --help"}
        print(json.dumps(quickstart) if as_json else
              f"{quickstart['description']}\n\nTry first:\n  " + "\n  ".join(quickstart["try"]) +
              "\n\nRun gtm-connector --help for all commands.")
        return 0
    if arguments[0] == "help":
        arguments = arguments[1:] + ["--help"]
    try:
        args = parser.parse_args(arguments)
        if args.version:
            print(json.dumps({"version": __version__}) if args.json else f"gtm-connector {__version__}")
            return 0
        if args.command is None:
            raise ParserError("A command is required.")
    except ParserError as exc:
        suggestion = "gtm-connector --help"
        candidate = next((argument for argument in arguments if not argument.startswith("-")), "")
        if candidate not in COMMANDS:
            close = difflib.get_close_matches(candidate, COMMANDS, n=1)
            if close:
                suggestion = f"gtm-connector {close[0]} --help"
        return _error(str(exc), 2, suggestion, as_json=as_json)
    except SystemExit as exc:
        return int(exc.code or 0)
    try:
        result = _run(args, argv)
        if result is not None:
            print(json.dumps(result, indent=2, ensure_ascii=False) if args.json else human_result(args.command, result))
        if args.command == "evaluate" and result["status"] != "passed":
            retry = ["gtm-connector", "evaluate", "--adapter", args.adapter]
            if args.model is not None:
                retry += ["--model", args.model]
            if args.with_slack:
                retry += ["--with-slack"]
            return _error("Evaluation failed. Inspect the result for failed cases.", 4,
                          shlex.join([*retry, "--json"]), as_json=args.json)
        return 0
    except (KeyboardInterrupt, BrokenPipeError):
        raise
    except Exception as exc:
        return _failure(exc, args)


def main(argv: list[str] | None = None) -> int:
    try:
        code = _main(argv)
        # Catch a closed reader here instead of during interpreter shutdown.
        sys.stdout.flush()
        return code
    except KeyboardInterrupt:
        arguments = list(sys.argv[1:] if argv is None else argv)
        return _error("Interrupted. Completed local writes are retained.", 130,
                      "gtm-connector audit --help", as_json=_json_requested(arguments))
    except BrokenPipeError:
        descriptor = os.open(os.devnull, os.O_WRONLY)
        try:
            os.dup2(descriptor, sys.stdout.fileno())
        finally:
            os.close(descriptor)
        return 0
