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

from . import __version__


COMMANDS = {
    "salesforce": ("Check Salesforce setup and a renewal brief without printing org records.",
                   "salesforce check --sf-org gtm-de"),
    "doctor": ("Check the local setup and show a fix for each failure.", "doctor --state-dir var/v2"),
    "connect": ("Print client setup and its tested or untested limits.",
                "connect inspector --scope crm:read --state-dir var/v2"),
    "completion": ("Print shell completion without reading local state.", "completion bash"),
    "demo": ("Follow Morgan Reed through a renewal brief using invented demo data.", "demo"),
    "verify": ("Check scopes, citations, injection handling, retries, and rate limits on invented data.",
               "verify --with-slack"),
    "brief": ("Create a cited renewal brief from authorized source reads.", "brief acc-003 --with-slack"),
    "serve": ("Start the local HTTPS connector and authorization server.", "serve --state-dir var/v2"),
    "seed": ("Create the local CRM fixture without replacing existing records.", "seed --db var/v2/crm.sqlite3"),
    "accounts": ("Search the selected CRM by account name.", "accounts --query Cedarline"),
    "account": ("Read one account, its contacts, and available contracts.", "account acc-003"),
    "audit": ("Read tool-call receipts from the local audit database.", "audit --state-dir var/renewal-demo"),
    "usage": ("Summarize tool calls and saved model token counts.", "usage --state-dir var/renewal-demo"),
    "evaluate": ("Check briefs for twenty fixed accounts in temporary state.", "evaluate --with-slack --json"),
    "check-brief": ("Check candidate text against a saved evidence snapshot.",
                    "check-brief brief-REPLACE candidate.txt --state-dir var/renewal-demo"),
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
                    epilog="Example: gtm-connector demo\n"
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
            command.add_argument("--db", type=Path, default=Path("var/v2/crm.sqlite3") if name == "seed" else None,
                                 help="Read or seed this local CRM database (default: var/v2/crm.sqlite3).")
            if name != "seed":
                command.add_argument("--state-dir", type=Path,
                                     help="Use this private Salesforce state folder (default: var/salesforce).")
        elif name not in ("evaluate", "completion", "salesforce"):
            state_dir = {"demo": "var/renewal-demo", "verify": "var/verification"}.get(name, "var/v2")
            command.add_argument("--state-dir", type=Path, default=Path(state_dir),
                                 help=f"Use this private state folder (default: {state_dir}).")
        if name == "accounts":
            command.add_argument("--query", default="", help="Match this literal part of an account name.")
        if name in ("account", "brief"):
            command.add_argument("account_id", help="Select an account, such as acc-003.")
        if name in ("serve", "brief", "accounts", "account"):
            command.add_argument("--crm-backend", choices=("sqlite", "salesforce"), default="sqlite",
                                 help="Select local fixtures or the read-only Salesforce CRM reader.")
        if name in ("serve", "brief"):
            command.add_argument("--salesforce-demo-binding", type=Path,
                                 help="Load private imported account bindings for invented call and Slack fixtures.")
        if name in ("serve", "brief", "accounts", "account", "salesforce"):
            command.add_argument("--sf-org", help="Read Salesforce credentials from this signed-in sf CLI alias.")
            command.add_argument("--sf-environment", choices=("developer", "sandbox", "production"),
                                 default="developer" if name == "salesforce" else None,
                                 help="Require this Salesforce environment (default: developer).")
        if name == "salesforce":
            command.add_argument("action", choices=("check",), help="Run the private, read-only setup check.")
        if name in ("brief", "salesforce"):
            command.add_argument("--as-of", help="Use YYYY-MM-DD for renewal planning. Salesforce uses today; "
                                 "fixtures use 2027-01-15.")
        if name == "demo":
            command.add_argument("--with-slack", action="store_true",
                                 help="Keep this compatible spelling. The demo always includes Slack.")
        if name in ("verify", "brief", "evaluate"):
            command.add_argument("--with-slack", action="store_true",
                                 help="Include Slack evidence and a checked follow-up proposal.")
        if name in ("brief", "evaluate"):
            command.add_argument("--adapter", choices=("fixture", "ollama"), default="fixture",
                                 help="Choose the fixture drafter or an already installed local model.")
            command.add_argument("--model", help="Name an already installed local Ollama model.")
        if name in ("inspector-config", "connect"):
            command.add_argument("--scope", action="append", required=name == "inspector-config",
                                 choices=("crm:read", "calls:read", "crm:write", "slack:read"),
                                 help="Grant this scope. Repeat for each explicit grant.")
        if name == "connect":
            command.add_argument("app", choices=("inspector", "claude-code", "claude-desktop", "cursor"),
                                 help="Choose the client whose setup to print.")
        if name == "completion":
            command.add_argument("shell", choices=("bash", "zsh", "fish"), help="Choose the shell to configure.")
        if name in ("audit", "usage"):
            command.add_argument("--brief", help="Select this brief or run correlation ID.")
            command.add_argument("--tool", help="Select the exact tool name.")
            command.add_argument("--caller", help="Select a full credential fingerprint or anonymous.")
            command.add_argument("--status", choices=("ok", "error", "pending"), help="Select this receipt outcome.")
            command.add_argument("--since", help="Include calls at or after this ISO timestamp with a UTC offset.")
            command.add_argument("--until", help="Include calls before this ISO timestamp with a UTC offset.")
            command.add_argument("--follow", action="store_true", help="Follow new or changed results until Ctrl-C.")
        if name == "brief":
            command.add_argument("--without-calls", action="store_true", help="Omit the calls:read grant.")
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


def human_result(command, result, *, crm_backend="sqlite"):
    if command == "salesforce":
        return table(["STEP", "STATUS", "COUNT", "MS", "API GETS", "SF COMMANDS", "DETAIL", "FIX"],
                     [[row[key] for key in ("name", "status", "count", "duration_ms", "api_calls",
                                            "sf_cli_invocations", "message", "fix")]
                      for row in result["steps"]])
    if command == "completion":
        return result["script"].rstrip("\n")
    if command == "doctor":
        return table(["CHECK", "STATUS", "DETAIL", "FIX"],
                     [[row[key] for key in ("name", "status", "message", "fix")] for row in result["checks"]])
    if command == "connect":
        return (f"{result['app']}: {result['status']}\n\n" + "\n".join(result["setup"]) +
                "\n\nLimits\n" + "\n".join(result["limits"]) +
                "\n\nDocumentation\n" + "\n".join(result["documentation"]))
    if command == "brief":
        return f"Brief: {result['brief_id']}\nAs of: {result['as_of']}\n\n{result['text']}"
    if command == "seed":
        return table(["RECORDS", "COUNT"], list(result["counts"].items()))
    if command == "accounts":
        return table(["ID", "ACCOUNT", "INDUSTRY", "RENEWAL"],
                     [[row.get(key) for key in ("id", "name", "industry", "renewal_date")] for row in result])
    if command == "account":
        keys = ("id", "name", "industry", "account_number") if crm_backend == "salesforce" else result
        fields = [[key.replace("_", " ").capitalize(), result.get(key)] for key in keys
                  if key not in ("contacts", "contracts")]
        contacts = result["contacts"]
        displayed = table(["FIELD", "VALUE"], fields) + "\n\nContacts\n" + table(
            ["ID", "NAME", "TITLE", "EMAIL"], [[row.get(key) for key in ("id", "name", "title", "email")]
                                              for row in contacts])
        if "contracts" in result:
            displayed += "\n\nContracts\n" + table(
                ["ID", "STATUS", "CATEGORY", "START", "END", "TERM MONTHS"],
                [[row.get(key) for key in ("id", "status", "status_code", "start_date", "end_date", "contract_term")]
                 for row in result["contracts"]])
        return displayed
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
        account = result["account"]
        return (f"{safe_text(result['rep']['name'])} prepares {safe_text(account['name'])}'s renewal.\n"
                "Sources: CRM, call notes, and Slack use invented demo fixtures.\n"
                f"Found {safe_text(account['name'])}. Renewal: {safe_text(account['renewal_date'])}.\n"
                f"Call-note access was refused (HTTP {result['denied_read']['status']}) "
                "because the CRM-only grant lacks calls:read.\n"
                "The local demo owner grants read access to CRM, calls, and Slack.\n\n"
                f"Brief: {result['brief']['brief_id']}\nAs of: {result['brief']['as_of']}\n\n"
                f"{result['brief']['text']}\n"
                "No activities were created. The follow-up remains a proposal.")
    if command == "verify":
        return ("Connector checks on invented demo data\n\n" + result["briefs"][0]["text"] + "\n" +
                table(["CHECK", "RESULT"], [["Read without calls:read", "Denied (HTTP 403)"],
                    ["Injected instruction", "Quoted as data; zero activities created"],
                    ["Uncited claim", "Rejected"], ["Retried write", "One activity"],
                    ["Rate limit", "HTTP 429"], ["Audit receipts", result["audit_rows"] + result["brief_audit_rows"]]]))
    raise ValueError("No display is available for this command.")


def _print_event(event, as_json):
    if as_json:
        print(json.dumps(event, ensure_ascii=True), flush=True)
    else:
        fields = [event["time"], event["tool"], f"caller={event['caller']}", event["decision"],
                  f"{event['duration_ms']:.2f}ms", f"receipt={event['receipt_id'] or 'none'}"]
        if event["reason"]:
            fields.append(f"reason={event['reason']}")
        print(" ".join(safe_text(field) for field in fields), flush=True)


def _progress(as_json):
    def report(completed, total, status):
        if as_json:
            print(json.dumps({"progress": {"command": "evaluate", "completed": completed,
                                           "total": total, "status": status}}), file=sys.stderr, flush=True)
        else:
            print(f"Evaluating briefs: {completed}/{total}" + (f" ({status})" if status else ""),
                  file=sys.stderr, flush=True)
    return report


def _salesforce_progress(as_json):
    def report(name, status, count):
        if as_json:
            print(json.dumps({"progress": {"command": "salesforce check", "step": name,
                                           "status": status, "count": count}}), file=sys.stderr, flush=True)
        else:
            print(f"Salesforce {name.replace('_', ' ')}: {status}." +
                  (f" Count: {count}." if count is not None else ""), file=sys.stderr, flush=True)
    return report


def _salesforce_sources(environment, *, mixed=False):
    if mixed:
        return (f"Sources: mixed. CRM uses Salesforce ({environment}); call notes and Slack use demo fixtures. "
                "Connection identity is verified.")
    return f"Sources: live. CRM uses Salesforce ({environment}). Connection identity is verified."


def _salesforce_account_read(args, reader):
    """Inspect through the same scoped, audited HTTPS tools as a brief."""
    import uuid
    from .briefing import BriefingError
    from .client import LocalClient
    from .runtime import Runtime
    from .salesforce import SalesforceReadError, _ERROR_CODES

    tool = "search_accounts" if args.command == "accounts" else "get_account"
    arguments = {"query": args.query} if args.command == "accounts" else {"account_id": args.account_id}
    with Runtime(args.state_dir, crm_reader=reader) as runtime, LocalClient(runtime.config) as client:
        if not args.json:
            print(_salesforce_sources(args.sf_environment), file=sys.stderr)
        token = client.authorize(["crm:read"])
        response = client.call(token, tool, arguments, brief_id="inspect-" + uuid.uuid4().hex)
        if response.status_code != 200:
            raise BriefingError(f"{tool}: HTTP {response.status_code}; no account data returned")
        try:
            result = response.json()["result"]
            if result.get("isError"):
                content = result.get("content")
                if (isinstance(content, list) and len(content) == 1 and isinstance(content[0], dict)
                        and content[0].get("type") == "text"):
                    text = content[0].get("text")
                    for code in _ERROR_CODES:
                        if text == f"Error executing tool {tool}: {code}":
                            raise SalesforceReadError(code)
                    if tool == "get_account" and text == "Error executing tool get_account: account not found":
                        raise LookupError("account not found in the selected Salesforce CRM")
                raise ValueError
            data = result["structuredContent"]
            if (result.get("resultType") != "complete" or data["source"] != "crm"
                    or data.get("untrusted_data") is not True):
                raise ValueError
            rows = data["records"]
            if not isinstance(rows, list) or any(not isinstance(row, dict) for row in rows):
                raise ValueError
            groups: dict[str, list[dict]] = {"accounts": [], "contacts": [], "contracts": []}
            for row in rows:
                group = next((name for name in groups if row.get("record_ref") ==
                              f"crm/{name}/{row.get('id')}"), None)
                if group is None or row.get("backend") != "salesforce":
                    raise ValueError
                groups[group].append({key: value for key, value in row.items() if key != "record_ref"})
            if args.command == "accounts":
                if groups["contacts"] or groups["contracts"]:
                    raise ValueError
                return groups["accounts"]
            accounts = groups["accounts"]
            if len(accounts) != 1 or accounts[0].get("id") != args.account_id:
                raise ValueError
            if any(row.get("account_id") != args.account_id for row in groups["contacts"] + groups["contracts"]):
                raise ValueError
            return {**accounts[0], "contacts": groups["contacts"], "contracts": groups["contracts"]}
        except LookupError as exc:
            if isinstance(exc, KeyError):
                raise SalesforceReadError("salesforce_invalid_response") from None
            raise ValueError(str(exc)) from None
        except (TypeError, ValueError, AttributeError):
            raise SalesforceReadError("salesforce_invalid_response") from None


def _run(args, argv):
    if args.command == "completion":
        from .completion import generate
        return {"shell": args.shell, "script": generate(build_parser(), args.shell)}
    # Dependencies belong to the command that uses them, after help is handled.
    crm_reader = None
    demo_binding = None
    if args.command in ("serve", "brief", "accounts", "account"):
        if args.sf_org is not None and args.crm_backend != "salesforce":
            raise ValueError("--sf-org requires --crm-backend salesforce")
        if args.sf_environment is not None and args.crm_backend != "salesforce":
            raise ValueError("--sf-environment requires --crm-backend salesforce")
        args.sf_environment = args.sf_environment or "developer"
        if args.command in ("accounts", "account"):
            if args.crm_backend == "salesforce":
                if args.db is not None:
                    raise ValueError("--db cannot be combined with --crm-backend salesforce; use --state-dir")
                args.state_dir = args.state_dir or Path("var/salesforce")
                from .salesforce import SalesforceReadError, _account_id, _like_literal
                try:
                    _like_literal(args.query) if args.command == "accounts" else _account_id(args.account_id)
                except SalesforceReadError:
                    message = ("The search must contain at most 200 characters and no control characters."
                               if args.command == "accounts" else
                               "The account ID must use acc-sf- followed by its 18-character Salesforce ID.")
                    raise ValueError(message) from None
            else:
                if args.state_dir is not None:
                    raise ValueError("--state-dir requires --crm-backend salesforce for account inspection; use --db")
                args.db = args.db or Path("var/v2/crm.sqlite3")
        if getattr(args, "salesforce_demo_binding", None) is not None:
            if args.crm_backend != "salesforce":
                raise ValueError("--salesforce-demo-binding requires --crm-backend salesforce")
            from .salesforce_demo import load_binding
            demo_binding = load_binding(args.salesforce_demo_binding)
        if args.crm_backend == "salesforce":
            from .salesforce import SalesforceReadOnlyAdapter
            crm_reader = (SalesforceReadOnlyAdapter.from_sf(args.sf_org, environment=args.sf_environment)
                          if args.sf_org is not None else
                          SalesforceReadOnlyAdapter.from_env(environment=args.sf_environment))
    if args.command == "salesforce":
        from .salesforce_check import run_check
        return run_check(sf_org=args.sf_org, as_of=args.as_of, environment=args.sf_environment,
                         progress=_salesforce_progress(args.json))
    if args.command in ("accounts", "account") and crm_reader is not None:
        return _salesforce_account_read(args, crm_reader)
    adapter = None
    if args.command in ("brief", "evaluate"):
        if args.adapter == "fixture" and args.model is not None:
            raise ValueError("--model requires --adapter ollama")
        if args.adapter == "ollama":
            if not args.model:
                raise ValueError("--adapter ollama requires --model with an installed local model")
            from .ollama import OllamaAdapter
            adapter = OllamaAdapter(args.model)
    if args.command == "doctor":
        from .doctor import doctor
        return doctor(args.state_dir)
    if args.command == "connect":
        from .connections import connect
        return connect(args.state_dir, args.app, args.scope)
    if args.command == "inspector-config":
        from .inspector import write_inspector_config
        return write_inspector_config(args.state_dir, args.scope)
    if args.command == "evaluate":
        from .evaluation import run_evaluation
        return run_evaluation(adapter=adapter, include_slack=args.with_slack, progress=_progress(args.json),
                              command=sys.orig_argv if argv is None else [sys.executable, "-m", "gtm_connector", *argv])
    if args.command in ("usage", "audit"):
        filters = {key: getattr(args, key) for key in ("tool", "caller", "since", "until", "status")}
        if args.follow:
            from .inspection import follow_audit, follow_usage
            stream = (follow_audit(args.state_dir / "audit.sqlite3", args.brief, **filters)
                      if args.command == "audit" else follow_usage(args.state_dir, args.brief, **filters))
            for result in stream:
                print(json.dumps(result, ensure_ascii=False) if args.json else
                      human_result(args.command, [result] if args.command == "audit" else result), flush=True)
            return None
        if args.command == "audit":
            from .audit import read_audit
            return read_audit(args.state_dir / "audit.sqlite3", args.brief, **filters)
        from .usage import build_usage_report
        return build_usage_report(args.state_dir, args.brief, **filters)
    if args.command == "brief":
        from datetime import date
        from .briefing import create_brief
        from .runtime import Runtime
        from .seed import DEMO_AS_OF
        as_of = args.as_of
        if as_of is None:
            as_of = date.today().isoformat() if crm_reader is not None else DEMO_AS_OF
        with Runtime(args.state_dir, crm_reader=crm_reader, demo_binding=demo_binding) as runtime:
            if crm_reader is not None and not args.json:
                print(_salesforce_sources(args.sf_environment, mixed=True), file=sys.stderr)
            return create_brief(runtime, args.account_id, include_calls=not args.without_calls,
                                as_of=as_of,
                                adapter=adapter, include_slack=args.with_slack)
    if args.command == "check-brief":
        from .briefing import check_saved_brief
        check_saved_brief(args.state_dir, args.brief_id, args.candidate.read_text())
        return {"status": "valid", "brief_id": args.brief_id}
    if args.command == "demo":
        from .demo import run
        return run(args.state_dir)
    if args.command == "verify":
        from .verification import run as run_verification
        return run_verification(args.state_dir, include_slack=args.with_slack)
    if args.command == "serve":
        from queue import Empty, SimpleQueue
        from .runtime import Runtime
        events: SimpleQueue = SimpleQueue()
        def emit(event):
            try:
                _print_event(event, args.json)
            except BrokenPipeError:
                _discard_output(sys.stdout)
                message = "Event output closed. The server continues until Ctrl-C."
                _server_notice(message, args.json)
        with Runtime(args.state_dir, crm_reader=crm_reader, on_call=events.put, demo_binding=demo_binding) as runtime:
            ready = {"type": "ready", "resource": runtime.config["resource"], "issuer": runtime.config["issuer"],
                     "crm_backend": runtime.crm_backend, "owner_config": str(runtime.directory / "owner.json"),
                     "state_dir": str(runtime.directory), "granted_scopes": [],
                     "sources": {"crm": "salesforce" if runtime.crm_backend == "salesforce" else "demo_fixture",
                                 "calls": "demo_fixture", "slack": "demo_fixture"}}
            if demo_binding is not None:
                ready["demo_kit_id"] = demo_binding.kit_id
            if runtime.crm_backend == "salesforce":
                ready.update(sf_environment=args.sf_environment, crm_identity_verified=True)
            if args.json:
                print(json.dumps(ready), flush=True)
            else:
                sources = (_salesforce_sources(args.sf_environment, mixed=True)
                           if runtime.crm_backend == "salesforce" else
                           "Sources: CRM, call notes, and Slack use demo fixtures.")
                if demo_binding is not None:
                    sources += f" The bound demo kit is {demo_binding.kit_id}."
                _server_notice(f"Connector ready at {ready['resource']}\nBackend: {runtime.crm_backend}\n"
                               f"{sources}\n"
                               f"State folder: {runtime.directory}\n"
                               "Granted scopes: none. Each client needs an explicit grant.\n"
                               "Connect: gtm-connector connect inspector --scope crm:read "
                               f"--state-dir {shlex.quote(str(runtime.directory))}\nStop with Ctrl-C.", False)
            try:
                while True:
                    try:
                        emit(events.get(timeout=0.5))
                    except Empty:
                        pass
            except KeyboardInterrupt:
                pass
        while not events.empty():
            emit(events.get_nowait())
        stopped = "Connector stopped. Local state is retained."
        _server_notice(stopped, args.json)
        return None
    from contextlib import closing
    from . import store
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
    if isinstance(exc, BriefRejected) or (command in ("demo", "verify") and message.startswith("demo ")):
        code = 4
    elif getattr(exc, "code", None) == "renewal_fields_unmapped":
        code = 3
        message = "renewal_fields_unmapped: " + message
        next_command = "gtm-connector salesforce check --help"
    elif getattr(exc, "code", None) in ("salesforce_environment_mismatch", "source_identity_mismatch"):
        code = 3
    elif (type(exc).__name__ in ("SalesforceReadError", "BriefingError", "OllamaError",
                                   "InspectorConnectionError") or
          type(exc).__module__.startswith(("httpx", "httpcore"))):
        code = 2 if message.startswith("salesforce_invalid_configuration") else 5
    elif type(exc).__name__ == "StateInUseError":
        code = 1
        next_command = f"gtm-connector audit --state-dir {shlex.quote(str(args.state_dir))}"
    elif isinstance(exc, sqlite3.Error):
        code = 1
    if getattr(exc, "code", None) == "demo_state_mismatch":
        code = 4
        fresh = args.state_dir.parent / "renewal-demo-fresh"
        suffix = 2
        while os.path.lexists(fresh):
            fresh = args.state_dir.parent / f"renewal-demo-fresh-{suffix}"
            suffix += 1
        next_command = shlex.join(["gtm-connector", "demo", "--state-dir", str(fresh)])
    if getattr(exc, "code", None) == "source_identity_mismatch":
        fresh = args.state_dir.parent / "salesforce-fresh"
        suffix = 2
        while os.path.lexists(fresh):
            fresh = args.state_dir.parent / f"salesforce-fresh-{suffix}"
            suffix += 1
        message = (f"source_identity_mismatch: {message} "
                   f"Repeat this command with --state-dir {shlex.quote(str(fresh))}. Existing files are retained.")
    if "audit database not found" in message:
        next_command = f"gtm-connector demo --state-dir {shlex.quote(str(args.state_dir))}"
    elif "database not found" in message:
        next_command = f"gtm-connector seed --db {shlex.quote(str(args.db))}"
    elif "account not found" in message:
        next_command = ("gtm-connector accounts --help" if getattr(args, "crm_backend", None) == "salesforce" else
                        f"gtm-connector accounts --db {shlex.quote(str(args.db))}")
    elif command in ("inspector-config", "connect") and (
            isinstance(exc, FileNotFoundError) or type(exc).__name__ == "InspectorConnectionError"):
        next_command = f"gtm-connector serve --state-dir {shlex.quote(str(args.state_dir))}"
    if command == "connect" and code == 2:
        next_command = f"gtm-connector doctor --state-dir {shlex.quote(str(args.state_dir))}"
    if type(exc).__name__ == "SalesforceReadError":
        from .salesforce_check import _FAILURES
        detail = _FAILURES.get(exc.code, _FAILURES["check_failed"])[0]
        message = f"{exc.code}: {detail}"
        next_command = "gtm-connector salesforce check --help"
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
        quickstart = {"description": parser.description, "try": ["gtm-connector demo",
                      "gtm-connector brief acc-007 --with-slack"], "help": "gtm-connector --help"}
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
            print(json.dumps(result, indent=2, ensure_ascii=False) if args.json else
                  human_result(args.command, result, crm_backend=getattr(args, "crm_backend", "sqlite")))
        if args.command == "doctor" and result["status"] != "passed":
            return _error("Setup checks failed. Apply the fix beside each failed check.", 4,
                          f"gtm-connector doctor --state-dir {shlex.quote(str(args.state_dir))}", as_json=args.json)
        if args.command == "salesforce" and result["status"] != "passed":
            return _error("Salesforce checks failed. Apply the fix beside each failed step.", 4,
                          "gtm-connector salesforce check --help", as_json=args.json)
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


def _discard_output(stream):
    descriptor = os.open(os.devnull, os.O_WRONLY)
    try:
        os.dup2(descriptor, stream.fileno())
    finally:
        os.close(descriptor)


def _server_notice(message, as_json):
    try:
        print(json.dumps({"message": message}) if as_json else message, file=sys.stderr, flush=True)
    except BrokenPipeError:
        _discard_output(sys.stderr)


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
        _discard_output(sys.stdout)
        return 0
