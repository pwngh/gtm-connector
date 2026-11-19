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
    "slack": ("Manage Slack authentication, select a thread, or check its audited read.", "slack status"),
    "calls": ("Import and inspect private transcript exports without contacting a call provider.",
              "calls import call.vtt --account acc-003"),
    "salesforce": ("Check Salesforce setup and a renewal brief without printing org records.",
                   "salesforce check --sf-org gtm-de"),
    "doctor": ("Check the local setup and show a fix for each failure.", "doctor --state-dir var/v2"),
    "connect": ("Print client setup and its tested or untested limits.",
                "connect inspector --scope crm:read --state-dir var/v2"),
    "mcp": ("Run a local stdio MCP bridge to a running connector for an assistant app.",
            "mcp stdio --state-dir var/v2 --scope crm:read"),
    "completion": ("Print shell completion without reading local state.", "completion bash"),
    "demo": ("Open one invented law-firm renewal as a Deal card.", "demo"),
    "verify": ("Check scopes, injection handling, retries, and rate limits on invented data.",
               "verify --with-slack"),
    "brief": ("Print the Deal card for one account from authorized source reads.", "brief acc-003 --with-slack"),
    "trace": ("Resolve one number on a saved card to its record and audit receipt.",
              "trace brief-REPLACE 4 --state-dir var/renewal-demo"),
    "serve": ("Start the local HTTPS connector and authorization server.", "serve --state-dir var/v2"),
    "seed": ("Create the local CRM fixture without replacing existing records.", "seed --db var/v2/crm.sqlite3"),
    "accounts": ("Search the selected CRM by account name.", "accounts --query Thornfield"),
    "account": ("Read one account, its contacts, and available contracts.", "account acc-003"),
    "audit": ("Read tool-call receipts from the local audit database.", "audit --state-dir var/renewal-demo"),
}
EXIT_NAMES = {1: "local_failure", 2: "usage_error", 3: "refused", 4: "check_failed", 5: "upstream_failure",
              130: "interrupted"}
CALL_FAILURES = {
    "calls_import_invalid": "The export or range is invalid. Use a valid UTF-8 TXT, VTT, or SRT transcript and range.",
    "calls_import_too_large": "The export exceeds 2 MiB or 10,000 units. Choose a smaller export.",
    "calls_excerpt_too_large": "The excerpt exceeds its text, unit, or metadata limit. Select fewer cues or lines.",
    "calls_speaker_names_missing": "Not every line or cue of the file opens with a name and a colon. "
                                   "Import it without --speaker-names.",
    "calls_import_not_found": "This import is not in the selected state folder. Run calls list to find its identifier.",
    "calls_account_not_found": "No CRM account matched the selection. Check its ID or exact AccountNumber.",
    "calls_not_imported": "This account has no selected transcript. Import an export before reading its calls.",
    "calls_reassign_required": "This content belongs to another account. Use --reassign only to move that association.",
    "calls_association_changed": "AccountNumber now resolves to another account. Review it before using --reassign.",
    "calls_source_mismatch": "The calls mode or CRM binding differs from this state. Use its original selection.",
    "calls_account_ambiguous": "AccountNumber did not resolve to one literal match. "
                               "Select its canonical ID with --account.",
    "calls_import_corrupt": "The import failed integrity checks. Retain this state and reimport into a fresh folder.",
    "calls_association_invalid": "The CRM response and audit receipt did not agree. No transcript was imported.",
    "calls_association_failed": "The authorized CRM read did not complete. No transcript was imported.",
    "calls_source_unavailable": "The local CRM connection could not be reached. Check the setup and repeat the import.",
    "calls_import_io": "The export or private store could not be read or written. Check permissions and retry.",
}
SLACK_FAILURES = {
    "slack_invalid_configuration": "The Slack connection options are invalid. Use an existing public app and its "
                                   "exact registered localhost redirect.",
    "slack_connection_busy": "Another Slack connection command is running. Finish or cancel it, then retry.",
    "slack_private_state_invalid": "The connection folder is not valid private Slack state. "
                                   "Use the original private connection folder or an unused one.",
    "slack_credentials_invalid": "The saved credentials failed validation. Retain the file privately and "
                                 "reconnect in an unused connection folder.",
    "slack_not_connected": "No usable Slack connection is saved. Connect with an existing registered public app.",
    "slack_browser_unavailable": "The system browser could not open. Retry from a session with a working browser.",
    "slack_callback_unavailable": "The registered localhost callback could not start. Free its port and retry.",
    "slack_login_timeout": "Browser approval did not finish within ten minutes. Repeat the connect command.",
    "slack_login_denied": "Slack authorization was declined. Retry only when you intend to approve the user scopes.",
    "slack_oauth_rejected": "Slack rejected authorization. Check the existing app's PKCE, registered redirect, "
                           "and workspace approval settings.",
    "slack_scope_mismatch": "The grant does not match the selected history scopes. "
                            "Use an existing grant with exactly those scopes.",
    "slack_identity_mismatch": "The connection identity does not match. Restore the original identity or "
                               "use another private connection folder.",
    "slack_token_type_unsupported": "The grant is not a supported user token. Use a user-only PKCE desktop grant.",
    "slack_reconnect_required": "Saved Slack authentication is no longer usable. Browser authorization is required.",
    "slack_response_invalid": "Slack returned an invalid response. Inspect the saved state before retrying; "
                              "an unsafe credential update requires reconnecting.",
    "slack_rate_limited": "Slack limited this request. Wait before explicitly retrying the operation.",
    "slack_timeout": "The Slack request exceeded its deadline. Check connectivity and explicitly retry.",
    "slack_unavailable": "Slack could not complete the request. Check connectivity and explicitly retry.",
    "slack_storage_failed": "The private Slack operation could not finish. Check private storage access. "
                            "Completed remote authorization may remain active and can be removed in Slack settings.",
}


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


class StateDirectory(argparse.Action):
    """Distinguish an explicit folder option from the parser's default."""

    def __call__(self, parser, namespace, values, option_string=None):
        setattr(namespace, self.dest, values)
        namespace.state_dir_explicit = True


def _slack_source_options(command):
    command.add_argument("--slack-backend", choices=("fixture", "slack"),
                         help="Select demo messages or a saved Slack thread (default: fixture). "
                              "Live Slack requires an explicit --state-dir.")
    command.add_argument("--slack-connection-dir", type=Path,
                         help="Use this private connection with --slack-backend slack "
                              "(default: var/slack-connection).")


def _slack_commands(command):
    command.add_argument("--connection-dir", type=Path, default=Path("var/slack-connection"),
                         help="Use this private Slack credential folder (default: var/slack-connection).")
    actions = command.add_subparsers(dest="action", required=True, metavar="ACTION")
    descriptions = {
        "connect": "Authorize an existing public Slack app through the system browser.",
        "status": "Inspect saved authentication, or explicitly verify it with Slack.",
        "disconnect": "Remove local credentials, with an optional access-token revocation attempt.",
        "map": "Save one thread selection for an account after an audited CRM lookup.",
        "check": "Read the selected thread through the audited Slack tool without accessing CRM or calls.",
    }
    for action, description in descriptions.items():
        example = ("slack connect --client-id EXISTING_PUBLIC_CLIENT_ID "
                   "--redirect-uri http://localhost:8765/slack/callback" if action == "connect" else
                   "slack map --state-dir var/selected-sources --account acc-003 "
                   "--channel C12345678 --thread-ts 1790000000.000001" if action == "map" else
                   "slack check --state-dir var/selected-sources --account acc-003" if action == "check" else
                   f"slack {action} --connection-dir var/slack-connection")
        epilog = f"Example: gtm-connector {example}"
        if action == "connect":
            epilog += ("\nReplace EXISTING_PUBLIC_CLIENT_ID with your existing app's public client ID. "
                       "PKCE and that exact localhost redirect must already be registered. "
                       "Browser approval has a ten-minute deadline. No messages are read.")
        elif action == "disconnect":
            epilog += ("\nLocal removal does not revoke remote access. Even confirmed access-token revocation "
                       "does not prove app removal or revocation of other credentials. "
                       "Manage the whole grant in Slack.")
        child = actions.add_parser(action, help=description, description=description, epilog=epilog,
                                   formatter_class=argparse.RawDescriptionHelpFormatter)
        child.add_argument("--json", action="store_true", default=argparse.SUPPRESS,
                           help="Write the documented JSON result for scripts.")
        child.add_argument("--connection-dir", type=Path, default=argparse.SUPPRESS,
                           help="Use this private Slack credential folder (default: var/slack-connection).")
        if action == "connect":
            child.add_argument("--client-id", required=True, help="Select an existing PKCE-enabled public Slack app.")
            child.add_argument("--redirect-uri", required=True,
                               help="Use the exact registered http://localhost:PORT/slack/callback redirect.")
            child.add_argument("--user-scope", action="append", choices=("channels:history", "groups:history"),
                               help="Request this user scope. Repeat for both; default: channels:history.")
            child.add_argument("--team-id", help="Require this workspace identity without displaying it.")
        elif action == "status":
            child.add_argument("--check", action="store_true",
                               help="Verify authentication with Slack, refreshing once if needed. "
                                    "No messages are read.")
        elif action == "disconnect":
            child.add_argument("--revoke", action="store_true",
                               help="Attempt access-token revocation before local removal, without refreshing.")
        else:
            child.add_argument("--state-dir", type=Path, required=True,
                               help="Use this explicitly selected private source state folder.")
            if action == "check":
                child.add_argument("--account", required=True, help="Check this mapped canonical CRM account ID.")
                continue
            account = child.add_mutually_exclusive_group(required=True)
            account.add_argument("--account", help="Associate this canonical CRM account ID.")
            account.add_argument("--account-number", help="Resolve this exact Salesforce AccountNumber.")
            thread = child.add_mutually_exclusive_group(required=True)
            thread.add_argument("--thread-url", help="Select a copied thread link from the saved Slack workspace.")
            thread.add_argument("--channel", help="Select this channel ID together with --thread-ts.")
            child.add_argument("--thread-ts", help="Use the exact parent timestamp with --channel.")
            child.add_argument("--crm-backend", choices=("sqlite", "salesforce"), default="sqlite",
                               help="Select the CRM used for association (default: sqlite).")
            child.add_argument("--sf-org", help="Read Salesforce credentials from this signed-in sf CLI alias.")
            child.add_argument("--sf-environment", choices=("developer", "sandbox", "production"),
                               help="Require this Salesforce environment (default: developer).")
            child.add_argument("--calls-backend", choices=("fixture", "imported"), default="fixture",
                               help="Select demo notes or imported transcripts in this state (default: fixture).")
            child.add_argument("--replace", action="store_true", help="Replace this account's selected thread.")
            child.add_argument("--reassign", action="store_true", help="Move a thread from another account.")


def _calls_commands(command):
    command.add_argument("--state-dir", type=Path, default=Path("var/imported-calls"), action=StateDirectory,
                         help="Use this private import state folder (default: var/imported-calls).")
    actions = command.add_subparsers(dest="action", required=True, metavar="ACTION")
    descriptions = {"import": "Import an existing transcript and select an account excerpt.",
                    "list": "List private import metadata without reading transcripts or credentials.",
                    "inspect": "Inspect a selected transcript excerpt or explicitly show the complete export."}
    examples = {"import": "calls import call.vtt --account acc-003",
                "list": "calls list --account acc-003", "inspect": "calls inspect IMPORT_ID --full"}
    for action, description in descriptions.items():
        child = actions.add_parser(action, help=description, description=description,
                                   epilog=f"Example: gtm-connector {examples[action]}")
        child.set_defaults(value=None, account=None, account_number=None, called_at=None, cues=None, lines=None,
                           reassign=False, full=False, after=None, crm_backend=None, sf_org=None, sf_environment=None)
        child.add_argument("--json", action="store_true", default=argparse.SUPPRESS,
                           help="Write the documented JSON result for scripts.")
        child.add_argument("--state-dir", type=Path, default=argparse.SUPPRESS, action=StateDirectory,
                           help="Use this private import state folder (default: var/imported-calls).")
        if action == "import":
            _slack_source_options(child)
            child.add_argument("value", type=Path, metavar="FILE", help="Read a UTF-8 TXT, VTT, or SRT export.")
            account = child.add_mutually_exclusive_group(required=True)
            account.add_argument("--account", help="Associate this canonical CRM account ID.")
            account.add_argument("--account-number", help="Resolve this exact Salesforce AccountNumber.")
            child.add_argument("--crm-backend", choices=("sqlite", "salesforce"),
                               help="Select the CRM used for association (default: sqlite).")
            child.add_argument("--sf-org", help="Read Salesforce credentials from this signed-in sf CLI alias.")
            child.add_argument("--sf-environment", choices=("developer", "sandbox", "production"),
                               help="Require this Salesforce environment (default: developer).")
            child.add_argument("--called-at", help="Record the supplied meeting timestamp with its UTC offset.")
            child.add_argument("--speaker-names", action="store_true",
                               help='State that every line or cue opens with its speaker\'s name and a colon '
                                    '("Dana Crest: We will sign."). Each name is then read as the speaker.')
            excerpt = child.add_mutually_exclusive_group()
            excerpt.add_argument("--cues", help="Select an inclusive one-based VTT or SRT cue range, such as 3:8.")
            excerpt.add_argument("--lines", help="Select an inclusive one-based TXT line range, such as 3:8.")
            child.add_argument("--reassign", action="store_true", help="Explicitly reassign imported content locally.")
        elif action == "list":
            child.add_argument("--account", help="Filter by this canonical CRM account ID.")
            child.add_argument("--after", help="Continue after the import ID returned as next_after.")
        else:
            child.add_argument("value", metavar="IMPORT_ID",
                               help="Select the import identifier returned by calls list.")
            child.add_argument("--full", action="store_true", help="Show all units and the original export text.")


def build_parser():
    parser = Parser(prog="gtm-connector",
                    description="Build a Deal card for a renewal from scoped CRM, call, and Slack reads.",
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
        if name == "calls":
            command.epilog = ("Examples:\n  gtm-connector calls import call.vtt --account acc-003\n"
                              "  gtm-connector calls list\n  gtm-connector calls inspect IMPORT_ID --full")
        elif name == "slack":
            command.epilog = ("Examples:\n  gtm-connector slack connect --help\n"
                              "  gtm-connector slack status\n  gtm-connector slack map --help\n"
                              "  gtm-connector slack check --help\n  gtm-connector slack disconnect")
        command.add_argument("--json", action="store_true", default=argparse.SUPPRESS,
                             help="Write the documented JSON result for scripts.")
        if name == "calls":
            _calls_commands(command)
            continue
        if name == "slack":
            _slack_commands(command)
            continue
        if name in ("seed", "accounts", "account"):
            command.add_argument("--db", type=Path, default=Path("var/v2/crm.sqlite3") if name == "seed" else None,
                                 help="Read or seed this local CRM database (default: var/v2/crm.sqlite3).")
            if name != "seed":
                command.add_argument("--state-dir", type=Path, action=StateDirectory,
                                     help="Use this private Salesforce state folder (default: var/salesforce).")
        elif name not in ("completion", "salesforce"):
            state_dir = {"demo": "var/renewal-demo", "verify": "var/verification"}.get(name, "var/v2")
            command.add_argument("--state-dir", type=Path, default=Path(state_dir), action=StateDirectory,
                                 help=f"Use this private state folder (default: {state_dir}).")
        if name == "accounts":
            command.add_argument("--query", default="", help="Match this literal part of an account name.")
        if name in ("account", "brief"):
            command.add_argument("account_id", help="Select an account, such as acc-003.")
        if name in ("serve", "brief", "accounts", "account"):
            command.add_argument("--crm-backend", choices=("sqlite", "salesforce"),
                                 default="sqlite",
                                 help="Select local fixtures or the read-only Salesforce CRM reader.")
        if name in ("serve", "brief", "accounts", "account"):
            _slack_source_options(command)
            command.add_argument("--calls-backend", choices=("fixture", "imported"),
                                 help="Select demo notes or imported transcripts (default: fixture). "
                                      "Imported transcripts require an explicit --state-dir.")
        if name in ("serve", "brief"):
            command.add_argument("--salesforce-demo-binding", type=Path,
                                 help="Load private imported account bindings for invented call and Slack fixtures.")
        if name == "serve":
            command.add_argument("--selection", type=Path,
                                 help="Serve only the accounts and Slack threads in this private selection file. "
                                      "Adds the caller-run brief tools and turns writes off.")
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
        if name in ("verify", "brief"):
            command.add_argument("--with-slack", action="store_true", help="Include Slack messages.")
        if name == "mcp":
            command.add_argument("action", choices=("stdio",),
                                 help="Serve MCP on stdin and stdout. An assistant app starts this command.")
        if name in ("connect", "mcp"):
            command.add_argument("--scope", action="append", required=name == "mcp",
                                 choices=("crm:read", "calls:read", "crm:write", "slack:read"),
                                 help="Grant this scope. Repeat for each explicit grant.")
        if name == "connect":
            command.add_argument("app", choices=("inspector", "claude-code", "claude-desktop", "cursor"),
                                 help="Choose the client whose setup to print.")
            command.add_argument("--transport", choices=("http", "stdio"), default="http",
                                 help="Use a supplied HTTP grant (default) or a local stdio bridge.")
            command.add_argument("--config-file", type=Path,
                                 help="With --transport stdio, merge the connector's entry into this client "
                                      "configuration file. A backup is saved first.")
        if name == "completion":
            command.add_argument("shell", choices=("bash", "zsh", "fish"), help="Choose the shell to configure.")
        if name == "audit":
            command.add_argument("--brief", help="Select this brief or run correlation ID.")
            command.add_argument("--tool", help="Select the exact tool name.")
            command.add_argument("--caller", help="Select a full credential fingerprint or anonymous.")
            command.add_argument("--status", choices=("ok", "error", "pending"), help="Select this receipt outcome.")
            command.add_argument("--since", help="Include calls at or after this ISO timestamp with a UTC offset.")
            command.add_argument("--until", help="Include calls before this ISO timestamp with a UTC offset.")
            command.add_argument("--follow", action="store_true", help="Follow new or changed results until Ctrl-C.")
        if name == "brief":
            command.add_argument("--without-calls", action="store_true", help="Omit the calls:read grant.")
        if name == "trace":
            command.add_argument("brief_id", help="Select the saved brief identifier.")
            command.add_argument("number", type=int, help="Select a number printed on its card.")
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


def human_result(command, result, *, crm_backend="sqlite", state_dir=None, action=None, connection_dir=None):
    if command == "slack":
        return _human_slack(result, action=action, connection_dir=connection_dir)
    if command == "calls":
        if "imports" in result:
            if sys.stdout.isatty():
                display = "\n\n".join("\n".join(safe_text(line) for line in [
                    row["import_id"], f"  Account: {row['account_id']}. Active: {'yes' if row['active'] else 'no'}.",
                    f"  Called: {row['called_at'] or 'unknown'}. Imported: {row['imported_at']}.",
                    f"  Requested: {_call_range(row, 'requested_range', context=False)}. "
                    f"Cited: {_call_range(row, context=False)}.",
                    *(["  Speaker names: the name that opens each line."] if row["speaker_names"] else [])])
                    for row in result["imports"])
                if display:
                    display += "\nOne adjacent unit on each available side is included in the cited range."
            else:
                display = table(["IMPORT", "ACCOUNT", "ACTIVE", "CALLED AT", "IMPORTED AT", "RANGE", "SPEAKER NAMES"],
                                [[row["import_id"], row["account_id"], row["active"], row["called_at"],
                                  row["imported_at"], _call_range(row), row["speaker_names"]]
                                 for row in result["imports"]])
            if result["next_after"] is not None:
                if sys.stdout.isatty():
                    resume = "gtm-connector calls list --after " + result["next_after"]
                    if state_dir is not None and Path(state_dir) != Path("var/imported-calls"):
                        resume += " \\\n  --state-dir " + shlex.quote(str(state_dir))
                    display += "\nMore imports are available. Continue with:\n" + resume
                else:
                    display += ("\nMore imports are available. Repeat calls list with --after " +
                                result["next_after"] + ".")
            return display or "No transcript imports are stored."
        metadata = result.get("import", result)
        display = table(["FIELD", "VALUE"], [
            ["Import", metadata["import_id"]], ["Account", metadata["account_id"]],
            ["Call time", metadata["called_at"]], ["Import time", metadata["imported_at"]],
            ["Requested excerpt", _call_range(metadata, "requested_range", context=False)],
            ["Cited excerpt", _call_range(metadata, context=False)],
            ["Context", "One adjacent unit on each available side is included in the cited range."],
            ["Total units", metadata["total_units"]],
            ["Speaker names", "the name that opens each line" if metadata["speaker_names"] else "not declared"]])
        if "import" not in result:
            return f"Transcript {result['status']}. The excerpt is selected for this account.\n" + display
        display += "\n\nExact excerpt units" if "original_text" not in result else "\n\nAll imported units"
        for unit in result["units"]:
            timing = "" if unit["start_ms"] is None else f" ({unit['start_ms']} to {unit['end_ms']} ms)"
            display += f"\n{unit['index']}{timing}: " + json.dumps(unit["text"], ensure_ascii=True)
        if "original_text" in result:
            display += "\n\nOriginal export text (quoted)\n" + json.dumps(result["original_text"], ensure_ascii=True)
        return display
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
        return (f"{result['text']}\nBrief: {result['brief_id']}\n"
                f"Resolve any number: gtm-connector trace {result['brief_id']} N" +
                (f" --state-dir {shlex.quote(str(state_dir))}" if state_dir is not None else ""))
    if command == "trace":
        receipt = result["receipt"]
        rows = [["Number", result["n"]], ["Kind", result["kind"]], ["Record", result["record_ref"]],
                ["Receipt", f"{receipt['audit_id']} ({receipt['tool']}, {receipt['status']})"]]
        if result["kind"] == "sentence":
            rows += [["Text", json.dumps(result["text"], ensure_ascii=False)],
                     ["Speaker", result["speaker"] or "no speaker label"],
                     ["Role", f"{result['role']} ({result['basis']})"], ["Time", result["at"] or "Missing"],
                     ["On the card", {"yes": "yes", "cut": "no: cut off", "long": "no: too long to show",
                                      "more": "no: the card shows 12"}.get(result.get("shown"), "not recorded")],
                     *([["Cues or lines", f"{span['first']} to {span['last']}"]]
                       if (span := result.get("span")) else []),
                     ["Source text SHA-256", result["raw_sha256"]],
                     ["Neutralizer version", result["sanitizer_version"]]]
        else:
            rows += [[key, "Missing" if value in (None, "") else value] for key, value in result["fields"].items()]
        return table(["FIELD", "VALUE"], rows)
    if command == "seed":
        return table(["RECORDS", "COUNT"], list(result["counts"].items()))
    if command == "accounts":
        rows = result["accounts"] if isinstance(result, dict) else result
        return table(["ID", "ACCOUNT", "INDUSTRY", "RENEWAL"],
                     [[row.get(key) for key in ("id", "name", "industry", "renewal_date")] for row in rows])
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
    if command == "demo":
        brief = result["brief"]
        return (f"Call-note access was refused (HTTP {result['denied_read']['status']}) for a grant without "
                "calls:read.\nEvery source below is an invented demo fixture.\n\n"
                f"{brief['text']}\nBrief: {brief['brief_id']}\n"
                f"Resolve any number: gtm-connector trace {brief['brief_id']} N"
                f" --state-dir {shlex.quote(str(state_dir))}")
    if command == "verify":
        return ("Connector checks on invented demo data\n\n" + result["briefs"][0]["text"] + "\n" +
                table(["CHECK", "RESULT"], [["Read without calls:read", "Denied (HTTP 403)"],
                    ["Injected instruction", "Quoted as data; zero activities created"],
                    ["Retried write", "One activity"],
                    ["Rate limit", "HTTP 429"],
                    ["Audit receipts", sum(result[key] for key in ("audit_rows", "brief_audit_rows",
                                                                    "seal_audit_rows"))]]))
    raise ValueError("No display is available for this command.")


def _call_range(metadata, field="cited_range", *, context=True):
    selected = metadata[field]
    return (f"{metadata['unit_kind']} {selected['first']}:{selected['last']}" +
            (" (includes adjacent context)" if context else ""))


def _slack_status_command(connection_dir, *, check=False):
    path = str(connection_dir or "var/slack-connection")
    if any(not character.isprintable() for character in path):
        return "gtm-connector slack status --help"
    return shlex.join(["gtm-connector", "slack", "status", *(["--check"] if check else []),
                       "--connection-dir", path])


def _human_slack(result, *, action, connection_dir):
    if action == "map":
        return (f"Slack thread selection {result['status']}. Thread access has not been checked.\n"
                "Next: gtm-connector slack check --help")
    if action == "check":
        headline = f"Slack thread check {result['status']}. CRM and calls access were not checked.\n"
        keys = ("name", "status", "count", "duration_ms", "api_calls", "response_bytes")
        headers = ["STEP", "STATUS", "COUNT", "MS", "API CALLS", "BYTES"]
        if not sys.stdout.isatty():
            return headline + table(headers + ["DETAIL", "NEXT"],
                                    [[row[key] for key in (*keys, "message", "next")] for row in result["steps"]])
        display = headline + table(headers, [[row[key] for key in keys] for row in result["steps"]])
        from shutil import get_terminal_size
        from textwrap import fill
        width = max(40, get_terminal_size().columns)
        for row in result["steps"]:
            if row["status"] == "fail":
                display += "\n\n" + fill(safe_text(row["message"]), width=width)
                if row["next"]:
                    display += "\nNext: " + safe_text(row["next"])
        return display
    if action == "disconnect":
        local = ("Local Slack credentials were removed." if result["local_credentials_removed"] else
                 "No local Slack credentials were present.")
        if not result["remote_revoke_requested"]:
            return local + "\nRemote access was not revoked."
        remote = ("Slack confirmed revocation of this access token." if result["remote_access_token_revoked"] else
                  "Remote access-token revocation is unconfirmed.")
        return local + "\n" + remote + "\nApp removal and other credentials are managed separately in Slack."
    if not result["configured"]:
        return ("No Slack connection is saved. An existing registered public app is required.\n"
                "Next: gtm-connector slack connect --help")
    next_command = None
    if result["credential_state"] == "identity_unchecked" and not result["needs_login"]:
        headline = "Saved credentials require an authentication check."
        next_command = _slack_status_command(connection_dir, check=True)
    elif result["credential_state"] == "refresh_pending":
        headline = ("A previous credential update or authentication check did not finish safely. "
                    "Browser authorization is required.")
        next_command = "gtm-connector slack connect --help"
    elif result["needs_login"]:
        headline = "Saved Slack authentication is no longer usable. Browser authorization is required."
        next_command = "gtm-connector slack connect --help"
    elif result["verification"] == "live":
        headline = ("Slack authentication is saved and verified." if action == "connect" else
                    "Slack authentication is verified.") + " No messages were read."
    else:
        headline = "Saved Slack authentication is available. This command did not contact Slack."
        next_command = _slack_status_command(connection_dir, check=True)
    rows = [["Granted user scopes", ", ".join(result["granted_scopes"])],
            ["Token rotation", "yes" if result["rotation"] else "no"],
            ["Access expires", result["access_expires_at"] or "No scheduled expiry; access can still be revoked."],
            ["Last authentication check", result["last_verified_at"]]]
    return headline + "\n" + table(["FIELD", "VALUE"], rows) + (
        "\nNext: " + next_command if next_command else "")


def _slack_progress(args):
    messages = {
        ("browser", "waiting"): "Waiting for Slack browser approval. Complete it within ten minutes.",
        ("exchange", "running"): "Exchanging the approved Slack authorization.",
        ("refresh", "running"): "Refreshing Slack authentication once.",
        ("identity", "running"): "Checking Slack authentication.",
        ("identity", "passed"): "Slack authentication matches the selected connection.",
        ("save", "running"): "Saving private Slack credentials.",
        ("save", "passed"): "Private Slack credentials are saved.",
        ("revoke", "running"): "Requesting revocation of the saved Slack access token.",
        ("revoke", "passed"): "Slack confirmed access-token revocation.",
    }

    def report(step, status):
        if not isinstance(step, str) or not isinstance(status, str) or (step, status) not in messages:
            return
        if not args.json and (step == "save" or status == "passed"):
            return
        output = {"progress": {"command": f"slack {args.action}", "step": step, "status": status}}
        print(json.dumps(output) if args.json else messages[step, status], file=sys.stderr, flush=True)
    return report


def _run_slack(args):
    if args.action in ("map", "check"):
        return _run_slack_source(args)
    from .slack_auth import connect_slack, disconnect_slack, slack_status
    progress = _slack_progress(args)
    if args.action == "connect":
        scopes = tuple(args.user_scope) if args.user_scope is not None else ("channels:history",)
        return connect_slack(args.connection_dir, client_id=args.client_id, redirect_uri=args.redirect_uri,
                             user_scopes=scopes,
                             expected_team_id=args.team_id, progress=progress)
    if args.action == "status":
        return slack_status(args.connection_dir, check=args.check, progress=progress)
    return disconnect_slack(args.connection_dir, revoke=args.revoke, progress=progress)


def _slack_source_progress(args):
    steps = {"preparing_state": "Preparing the selected sources and private state.",
             "resolving_account": "Resolving the account through an audited CRM read.",
             "saving_selection": "Saving the private Slack thread selection.",
             "private_state": "Checking the existing private source state.",
             "mapping": "Checking the saved thread selection.",
             "credential_binding": "Checking the selected Slack authentication.",
             "thread_read": "Reading the selected Slack thread through the audited tool.",
             "receipt": "Checking the committed tool receipt."}
    allowed = ({"preparing_state", "resolving_account", "saving_selection"} if args.action == "map" else
               {"private_state", "mapping", "credential_binding", "thread_read", "receipt"})

    def report(step, status):
        if (not isinstance(step, str) or step not in allowed or not isinstance(status, str)
                or status not in ("running", "pass", "fail", "not_run")):
            return
        if args.json:
            print(json.dumps({"progress": {"command": f"slack {args.action}", "step": step, "status": status}}),
                  file=sys.stderr, flush=True)
        elif status == "running":
            print(steps[step], file=sys.stderr, flush=True)
    return report


def _slack_map_arguments(args):
    """Validate selectors without opening credentials, state, or a network client."""
    import re
    from urllib.parse import urlsplit
    from .slack_mapping import SlackMappingError, parse_thread_selection
    from .tool_definitions import valid_account_search
    if ((args.sf_org is not None or args.sf_environment is not None) and args.crm_backend != "salesforce"
            or args.account_number is not None and (args.crm_backend != "salesforce"
                                                   or not valid_account_search("", args.account_number))):
        raise SlackMappingError("slack_selection_invalid")
    pattern = r"acc-sf-[A-Za-z0-9]{18}" if args.crm_backend == "salesforce" else r"acc-[0-9]{3}"
    if args.account is not None and re.fullmatch(pattern, args.account) is None:
        raise SlackMappingError("slack_selection_invalid")
    args.sf_environment = args.sf_environment or "developer"
    origin = None
    if args.thread_url is not None:
        try:
            url = urlsplit(args.thread_url)
            origin = f"{url.scheme}://{url.netloc}"
        except ValueError:
            raise SlackMappingError("slack_selection_invalid") from None
    # The URL's origin is used only to validate syntax. It never selects a
    # credential or request destination. The saved origin is checked below.
    return parse_thread_selection(thread_url=args.thread_url, channel_id=args.channel,
                                  thread_ts=args.thread_ts, workspace_origin=origin)


def _run_slack_source(args):
    import re
    from datetime import datetime, timezone
    from time import monotonic
    from .slack_mapping import SlackMappingError, parse_thread_selection, save_slack_mapping, slack_identity_binding
    progress = _slack_source_progress(args)
    if args.action == "check":
        if re.fullmatch(r"acc-(?:[0-9]{3}|sf-[A-Za-z0-9]{18})", args.account) is None:
            raise SlackMappingError("slack_selection_invalid")
        from .slack_workflow import run_slack_check
        if not args.json:
            print("Source: live Slack. This check reads only the selected mapped thread.", file=sys.stderr)
        return run_slack_check(args.state_dir, connection_dir=args.connection_dir,
                               account_id=args.account, progress=progress)
    started = monotonic()
    selection = _slack_map_arguments(args)
    from .slack_auth import SlackAuthError, inspect_slack_connection
    connection = inspect_slack_connection(args.connection_dir)
    if connection.needs_login or connection.credential_state == "refresh_pending":
        raise SlackAuthError("slack_reconnect_required")
    if connection.credential_state != "ready":
        raise SlackMappingError("slack_identity_unchecked")
    if args.thread_url is not None:
        selection = parse_thread_selection(thread_url=args.thread_url, workspace_origin=connection.workspace_origin)
    progress("preparing_state", "running")
    crm_reader = None
    if args.crm_backend == "salesforce":
        crm_reader = _salesforce_reader(args)
    from .runtime import Runtime
    from .slack_workflow import resolve_slack_account
    with Runtime(args.state_dir, crm_reader=crm_reader, calls_backend=args.calls_backend,
                 slack_backend="slack", slack_connection_dir=args.connection_dir,
                 slack_expected_binding=slack_identity_binding(connection.identity)) as runtime:
        if not args.json:
            print(_live_slack_sources(args), file=sys.stderr)
        progress("resolving_account", "running")
        association = resolve_slack_account(runtime, account_id=args.account, account_number=args.account_number)
        progress("saving_selection", "running")
        saved = save_slack_mapping(runtime.slack_mapping_path, association=association,
                                   slack_binding=runtime.slack_binding, selection=selection,
                                   selected_at=datetime.now(timezone.utc).isoformat().replace("+00:00", "Z"),
                                   replace=args.replace, reassign=args.reassign)
    return {"schema_version": 1, "status": "saved" if saved["changed"] else "unchanged", "selection": "saved",
            "thread_access": "unchecked", "counts": {"selected_threads": 1},
            "duration_ms": round((monotonic() - started) * 1000, 3), "next": "gtm-connector slack check --help"}


def _slack_failure(exc, args):
    from .slack_auth import SlackAuthError
    if isinstance(exc, SlackAuthError) and exc.code in SLACK_FAILURES:
        code, detail = exc.exit_code, exc.code
    else:
        code, detail = 1, "slack_storage_failed"
    next_command = "gtm-connector slack connect --help"
    if detail in ("slack_response_invalid", "slack_rate_limited", "slack_timeout", "slack_unavailable"):
        next_command = _slack_status_command(args.connection_dir)
    elif detail in ("slack_connection_busy", "slack_storage_failed"):
        next_command = f"gtm-connector slack {args.action} --help"
    return _error(detail + ": " + SLACK_FAILURES[detail], code, next_command, as_json=args.json)


def _slack_source_failure(exc, args):
    from .slack_workflow import slack_failure_details
    kind = type(exc).__name__
    detail = getattr(exc, "code", "")
    if kind == "SlackAuthError":
        if args.command == "slack":
            return _slack_failure(exc, args)
        message = SLACK_FAILURES.get(detail, SLACK_FAILURES["slack_storage_failed"])
        return _error(f"{detail}: {message}", exc.exit_code, "gtm-connector slack connect --help", as_json=args.json)
    if kind in ("SlackMappingError", "SlackReadError"):
        message, next_command = slack_failure_details(detail)
        return _error(f"{detail}: {message}", exc.exit_code, next_command, as_json=args.json)
    if detail == "source_identity_mismatch":
        message = ("source_identity_mismatch: " + str(exc).split(". ", 1)[0].rstrip(".") +
                   ". Restore the original CRM backend and connection, Slack connection, source modes, "
                   "and existing state folder. Existing files are retained.")
        return _error(message, 3, "gtm-connector slack map --help", as_json=args.json)
    if kind == "StateInUseError":
        return _error("The selected state is in use. Stop its server or finish the other command, then retry.",
                      1, f"gtm-connector slack {args.action} --help", as_json=args.json)
    if kind == "SalesforceReadError":
        from .salesforce_check import _FAILURES
        message = _FAILURES.get(detail, _FAILURES["check_failed"])[0]
        code = 2 if detail == "salesforce_invalid_configuration" else (
            3 if detail == "salesforce_environment_mismatch" else 5)
        return _error(f"{detail}: {message}", code, "gtm-connector salesforce check --help", as_json=args.json)
    message, next_command = slack_failure_details("slack_storage_failed")
    return _error("slack_storage_failed: " + message, 1, next_command, as_json=args.json)


def _print_event(event, as_json):
    if as_json:
        print(json.dumps(event, ensure_ascii=True), flush=True)
    else:
        fields = [event["time"], event["tool"], f"caller={event['caller']}", event["decision"],
                  f"{event['duration_ms']:.2f}ms", f"receipt={event['receipt_id'] or 'none'}"]
        if event["reason"]:
            fields.append(f"reason={event['reason']}")
        print(" ".join(safe_text(field) for field in fields), flush=True)


def _salesforce_progress(as_json):
    def report(name, status, count):
        if as_json:
            print(json.dumps({"progress": {"command": "salesforce check", "step": name,
                                           "status": status, "count": count}}), file=sys.stderr, flush=True)
        else:
            print(f"Salesforce {name.replace('_', ' ')}: {status}." +
                  (f" Count: {count}." if count is not None else ""), file=sys.stderr, flush=True)
    return report


def _sf_waiting(as_json):
    """Say that sf is still working: after two seconds, then every ten.

    JSON output stays unchanged, so a script still reads one error object on stderr.
    """
    if as_json:
        return None
    due, last = [2.0], [0.0]

    def report(seconds):
        if seconds < last[0]:
            due[0] = 2.0  # a new login read started
        last[0] = seconds
        if seconds < due[0]:
            return
        due[0] = seconds + 10
        try:
            print(f"Waiting for the Salesforce CLI (sf) to return the saved login: {int(seconds)} s.",
                  file=sys.stderr, flush=True)
        except BrokenPipeError:
            _discard_output(sys.stderr)
    return report


def _sf_refresh_notice(as_json):
    """Say when a read has to refresh the login through sf, and how that ended."""
    def notice(status, seconds):
        if status == "running":
            message = "Salesforce login refresh: sf is running."
        elif status == "ready":
            message = f"Salesforce login refresh: sf answered in {round(seconds)} s."
        else:
            message = f"Salesforce login refresh: {status} after {round(seconds)} s."
        _server_notice(message, as_json)
    return notice


def _salesforce_reader(args):
    """Open the explicit Salesforce backend, and say so while sf is slow to answer."""
    from .salesforce import SalesforceReadOnlyAdapter
    if args.sf_org is None:
        return SalesforceReadOnlyAdapter.from_env(environment=args.sf_environment)
    from .sf_credentials import reporting
    with reporting(_sf_waiting(getattr(args, "json", False))):
        reader = SalesforceReadOnlyAdapter.from_sf(args.sf_org, environment=args.sf_environment)
    as_json = getattr(args, "json", False)
    if args.command == "serve" or not as_json:
        # A one-shot JSON command keeps stderr to its single error object.
        reader.on_refresh = _sf_refresh_notice(as_json)
    return reader


def _salesforce_sources(environment, *, mixed=False, calls_backend="fixture", include_slack=True):
    if mixed:
        if calls_backend == "imported":
            slack = "Slack uses demo fixtures when requested. " if include_slack else ""
            return (f"Sources: mixed. CRM uses Salesforce ({environment}); "
                    "calls use user-supplied transcript exports. " +
                    slack + "Connection identity is verified for CRM.")
        fixtures = "call notes and Slack" if include_slack else "call notes"
        return (f"Sources: mixed. CRM uses Salesforce ({environment}); {fixtures} use demo fixtures. "
                "Connection identity is verified.")
    return f"Sources: live. CRM uses Salesforce ({environment}). Connection identity is verified."


def _live_slack_sources(args):
    crm = f"Salesforce ({args.sf_environment})" if args.crm_backend == "salesforce" else "demo fixtures"
    calls = "user-supplied transcript exports" if args.calls_backend == "imported" else "demo fixtures"
    mode = "live" if args.crm_backend == "salesforce" and args.calls_backend == "imported" else "mixed"
    return (f"Sources: {mode}. CRM uses {crm}; calls use {calls}; Slack uses the selected mapped thread. "
            "Slack thread access is checked when it is read.")


def _slack_source_arguments(args):
    """Require deliberate live-source state before files, credentials, or initialization."""
    if args.command in ("accounts", "account") and args.crm_backend == "sqlite" and (
            args.slack_backend is not None or args.slack_connection_dir is not None):
        raise ValueError("Slack source options require --crm-backend salesforce for account inspection.")
    if args.slack_connection_dir is not None and args.slack_backend != "slack":
        raise ValueError("--slack-connection-dir requires --slack-backend slack.")
    if args.slack_backend == "slack":
        if not getattr(args, "state_dir_explicit", False):
            raise ValueError("--slack-backend slack requires an explicit --state-dir. "
                             "Select the same private source state folder used by slack map.")
        if args.command == "brief" and not args.with_slack:
            raise ValueError("--slack-backend slack requires --with-slack for a brief.")
        if getattr(args, "salesforce_demo_binding", None) is not None:
            raise ValueError("--salesforce-demo-binding cannot be combined with --slack-backend slack.")
    args.slack_backend = args.slack_backend or "fixture"
    args.slack_connection_dir = args.slack_connection_dir or Path("var/slack-connection")


def _calls_arguments(args):
    """Validate owner options and the bounded export before opening state or credentials."""
    import re
    from datetime import datetime, timezone
    import stat
    from .calls_import import CallsImportError, MAX_RAW_BYTES, parse_transcript, select_excerpt

    def invalid(message):
        raise ValueError(message)

    if args.action == "inspect" and not re.fullmatch(r"note-[0-9a-f]{64}", args.value):
        invalid("The import ID must be the note- identifier returned by calls import or calls list.")
    if args.after is not None and not re.fullmatch(r"note-[0-9a-f]{64}", args.after):
        invalid("--after requires the import ID returned as next_after by calls list.")
    if args.account is not None and not re.fullmatch(r"acc-(?:[0-9]{3}|sf-[A-Za-z0-9]{18})", args.account):
        invalid("The account must be a canonical acc-NNN or acc-sf- Salesforce identifier.")
    if args.action != "import":
        return None
    args.crm_backend = args.crm_backend or "sqlite"
    if (args.sf_org is not None or args.sf_environment is not None) and args.crm_backend != "salesforce":
        invalid("Salesforce options require --crm-backend salesforce.")
    if args.account_number is not None:
        if args.crm_backend != "salesforce":
            invalid("--account-number requires --crm-backend salesforce.")
        from .tool_definitions import valid_account_search
        if not valid_account_search("", args.account_number):
            invalid("AccountNumber must be nonblank valid Unicode, at most 40 characters, "
                    "and contain no control characters.")
    if args.account is not None and args.account.startswith("acc-sf-") != (args.crm_backend == "salesforce"):
        invalid("The canonical account ID must match the selected CRM backend.")
    called_at = None
    if args.called_at is not None:
        try:
            timestamp = datetime.fromisoformat(args.called_at)
            if "T" not in args.called_at or timestamp.utcoffset() is None:
                raise ValueError
            called_at = timestamp.astimezone(timezone.utc).isoformat().replace("+00:00", "Z")
        except (ValueError, OverflowError):
            invalid("--called-at requires an ISO timestamp with a UTC offset, such as 2027-01-15T10:00:00Z.")
    path = Path(args.value)
    format_name = path.suffix.lower().lstrip(".")
    if format_name not in ("txt", "vtt", "srt"):
        invalid("Import an existing UTF-8 .txt, .vtt, or .srt transcript export.")
    if (args.cues is not None and format_name == "txt") or (args.lines is not None and format_name != "txt"):
        invalid("Use --cues with VTT or SRT, or --lines with TXT.")
    selected_range = None
    supplied = args.cues if args.cues is not None else args.lines
    if supplied is not None:
        if not re.fullmatch(r"[1-9][0-9]{0,4}:[1-9][0-9]{0,4}", supplied):
            invalid("Select an inclusive one-based range such as 3:8.")
        first, last = map(int, supplied.split(":"))
        if first > last:
            invalid("The first selected unit must not follow the last selected unit.")
        selected_range = first, last
    _calls_progress(args.json, "reading_export")
    try:
        if not stat.S_ISREG(path.lstat().st_mode):
            raise CallsImportError("calls_import_invalid")
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode):
                raise CallsImportError("calls_import_invalid")
            if info.st_size > MAX_RAW_BYTES:
                raise CallsImportError("calls_import_too_large")
            raw = source.read(MAX_RAW_BYTES + 1)
    except OSError:
        raise CallsImportError("calls_import_io") from None
    parsed = parse_transcript(raw, format=format_name, speaker_names=args.speaker_names)
    excerpt = select_excerpt(parsed, requested_range=selected_range)
    return parsed, excerpt, called_at


def _calls_progress(as_json, step):
    messages = {"reading_export": "Reading the transcript export.",
                "preparing_state": "Preparing the selected CRM and private state.",
                "resolving_account": "Resolving the account through an audited CRM read.",
                "saving_import": "Saving the private transcript selection."}
    output = {"progress": {"command": "calls import", "step": step}}
    print(json.dumps(output) if as_json else messages[step], file=sys.stderr, flush=True)


def _run_calls(args, prepared, crm_reader):
    from datetime import datetime, timezone
    from .calls_import import inspect_import, list_imports, store_import
    from .runtime import Runtime, open_import_state
    if args.action == "import":
        from .call_workflow import resolve_call_account
        parsed, excerpt, called_at = prepared
        with Runtime(args.state_dir, crm_reader=crm_reader, calls_backend="imported",
                     slack_backend=args.slack_backend, slack_connection_dir=args.slack_connection_dir) as runtime:
            _calls_progress(args.json, "resolving_account")
            association = resolve_call_account(runtime, account_id=args.account, account_number=args.account_number)
            _calls_progress(args.json, "saving_import")
            result = store_import(runtime.imports_path, parsed, excerpt, association=association,
                                  called_at=called_at, imported_at=datetime.now(timezone.utc).isoformat().replace(
                                      "+00:00", "Z"), reassign=args.reassign)
    else:
        with open_import_state(args.state_dir) as state:
            result = (list_imports(state.imports_path, crm_binding=state.calls_binding, account_id=args.account,
                                   limit=100, after=args.after) if args.action == "list" else
                      inspect_import(state.imports_path, args.value, crm_binding=state.calls_binding, full=args.full))
    if not args.json:
        if args.action == "import":
            crm = f"Salesforce ({args.sf_environment})" if args.crm_backend == "salesforce" else "demo fixtures"
            label = (f"Sources: CRM uses {crm}; calls use user-supplied transcript exports. "
                     "Transcript provider identity and statements are unverified.")
            if args.slack_backend == "slack":
                label += " Slack uses the selected mapped thread when requested. Thread access remains unchecked."
        else:
            label = "Source: user-supplied transcript export. Provider identity and source statements are unverified."
        print(label, file=sys.stderr)
    return result


def _account_list(rows, more):
    """A complete list stays a plain array. A cut one is an object that says so."""
    return {"accounts": rows, "has_more": True} if more else rows


def _salesforce_account_read(args, reader):
    """Inspect through the same scoped, audited HTTPS tools as a brief."""
    import uuid
    from .briefing import BriefingError
    from .client import LocalClient
    from .runtime import Runtime
    from .salesforce import SalesforceReadError, _ERROR_CODES

    tool = "search_accounts" if args.command == "accounts" else "get_account"
    arguments = {"query": args.query} if args.command == "accounts" else {"account_id": args.account_id}
    with Runtime(args.state_dir, crm_reader=reader, calls_backend=args.calls_backend,
                 slack_backend=args.slack_backend, slack_connection_dir=args.slack_connection_dir) as runtime, \
            LocalClient(runtime.config) as client:
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
                more = data.get("has_more", False)
                if groups["contacts"] or groups["contracts"] or type(more) is not bool:
                    raise ValueError
                return _account_list(groups["accounts"], more)
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
    if args.command == "slack":
        return _run_slack(args)
    if args.command == "mcp":
        # stdout carries only MCP messages from here on; diagnostics go to stderr.
        from .stdio_bridge import run_bridge
        run_bridge(args.state_dir, args.scope)
        return None
    # Dependencies belong to the command that uses them, after help is handled.
    if (args.command in ("serve", "brief", "accounts", "account")
            or (args.command == "calls" and args.action == "import")):
        _slack_source_arguments(args)
    prepared_calls = _calls_arguments(args) if args.command == "calls" else None
    crm_reader = None
    demo_binding = None
    if args.command in ("serve", "brief", "accounts", "account"):
        if args.command in ("accounts", "account") and args.crm_backend == "sqlite" and args.calls_backend is not None:
            raise ValueError("--calls-backend is available only with --crm-backend salesforce for account inspection.")
        if args.calls_backend == "imported" and not getattr(args, "state_dir_explicit", False):
            raise ValueError("--calls-backend imported requires an explicit --state-dir. "
                             "Select the same import state folder, such as --state-dir var/imported-calls.")
        args.calls_backend = args.calls_backend or "fixture"
        if getattr(args, "salesforce_demo_binding", None) is not None and args.calls_backend == "imported":
            raise ValueError("--salesforce-demo-binding cannot be combined with --calls-backend imported.")
    if args.command == "calls" and args.action == "import":
        _calls_progress(args.json, "preparing_state")
    if (args.command in ("serve", "brief", "accounts", "account")
            or (args.command == "calls" and args.action == "import")):
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
            if getattr(args, "selection", None) is not None:
                raise ValueError("--selection cannot be combined with --salesforce-demo-binding.")
            from .salesforce_demo import load_binding
            demo_binding = load_binding(args.salesforce_demo_binding)
        if getattr(args, "selection", None) is not None:
            # Check the owner's file before any credential or source is opened.
            from .selection import load_selection
            args.loaded_selection = load_selection(args.selection, crm_backend=args.crm_backend)
        if args.crm_backend == "salesforce":
            crm_reader = _salesforce_reader(args)
    if args.command == "calls":
        return _run_calls(args, prepared_calls, crm_reader)
    if args.command == "salesforce":
        from .salesforce_check import run_check
        from .sf_credentials import reporting
        with reporting(_sf_waiting(args.json)):
            return run_check(sf_org=args.sf_org, as_of=args.as_of, environment=args.sf_environment,
                             progress=_salesforce_progress(args.json),
                             on_refresh=None if args.json else _sf_refresh_notice(False))
    if args.command in ("accounts", "account") and crm_reader is not None:
        return _salesforce_account_read(args, crm_reader)
    if args.command == "doctor":
        from .doctor import doctor
        return doctor(args.state_dir)
    if args.command == "connect":
        from .connections import connect
        return connect(args.state_dir, args.app, args.scope, transport=args.transport,
                       config_file=args.config_file)
    if args.command == "audit":
        from .audit import read_audit
        from .inspection import follow_audit
        filters = {key: getattr(args, key) for key in ("tool", "caller", "since", "until", "status")}
        if args.follow:
            for result in follow_audit(args.state_dir / "audit.sqlite3", args.brief, **filters):
                print(json.dumps(result, ensure_ascii=False) if args.json else human_result("audit", [result]),
                      flush=True)
            return None
        return read_audit(args.state_dir / "audit.sqlite3", args.brief, **filters)
    if args.command == "brief":
        from datetime import date
        from .briefing import create_brief
        from .runtime import Runtime
        from .seed import DEMO_AS_OF
        as_of = args.as_of
        if as_of is None:
            as_of = date.today().isoformat() if crm_reader is not None else DEMO_AS_OF
        with Runtime(args.state_dir, crm_reader=crm_reader, demo_binding=demo_binding,
                     calls_backend=args.calls_backend, slack_backend=args.slack_backend,
                     slack_connection_dir=args.slack_connection_dir) as runtime:
            if args.slack_backend == "slack" and not args.json:
                print(_live_slack_sources(args), file=sys.stderr)
            elif crm_reader is not None and not args.json:
                print(_salesforce_sources(args.sf_environment, mixed=True, calls_backend=args.calls_backend,
                                          include_slack=args.with_slack),
                      file=sys.stderr)
            elif args.calls_backend == "imported" and not args.json:
                fixtures = "CRM and Slack use" if args.with_slack else "CRM uses"
                print(f"Sources: mixed. Calls use user-supplied transcript exports; {fixtures} demo fixtures.",
                      file=sys.stderr)
            brief = create_brief(runtime, args.account_id, include_calls=not args.without_calls,
                                 as_of=as_of, include_slack=args.with_slack)
            # The raw records stay in the private saved file. Output holds neutralized text only.
            return {key: value for key, value in brief.items() if key != "evidence"}
    if args.command == "trace":
        from .briefing import trace_saved
        return trace_saved(args.state_dir, args.brief_id, args.number)
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
        selection = getattr(args, "loaded_selection", None)
        with Runtime(args.state_dir, crm_reader=crm_reader, on_call=events.put, demo_binding=demo_binding,
                     calls_backend=args.calls_backend, slack_backend=args.slack_backend,
                     slack_connection_dir=args.slack_connection_dir, selection=selection) as runtime:
            ready = {"type": "ready", "resource": runtime.config["resource"], "issuer": runtime.config["issuer"],
                     "crm_backend": runtime.crm_backend, "owner_config": str(runtime.directory / "owner.json"),
                     "state_dir": str(runtime.directory), "granted_scopes": [],
                     "sources": {"crm": "salesforce" if runtime.crm_backend == "salesforce" else "demo_fixture",
                                 "calls": "imported_transcript" if args.calls_backend == "imported" else "demo_fixture",
                                 "slack": "slack" if args.slack_backend == "slack" else "demo_fixture"}}
            if selection is not None:
                ready["selection"] = {"accounts": len(selection.accounts),
                                      "slack_accounts": len(selection.slack_accounts),
                                      "tools": sorted(runtime.connector.enabled_tools)}
            if demo_binding is not None:
                ready["demo_kit_id"] = demo_binding.kit_id
            if runtime.crm_backend == "salesforce":
                ready.update(sf_environment=args.sf_environment, crm_identity_verified=True)
            if args.json:
                print(json.dumps(ready), flush=True)
            else:
                sources = (_live_slack_sources(args) if args.slack_backend == "slack" else
                           _salesforce_sources(args.sf_environment, mixed=True, calls_backend=args.calls_backend)
                           if runtime.crm_backend == "salesforce" else
                           "Sources: mixed. Calls use user-supplied transcript exports; "
                           "CRM and Slack use demo fixtures."
                           if args.calls_backend == "imported" else
                           "Sources: CRM, call notes, and Slack use demo fixtures.")
                if demo_binding is not None:
                    sources += f" The bound demo kit is {demo_binding.kit_id}."
                if selection is not None:
                    sources += (f"\nSelection: {len(selection.accounts)} accounts, "
                                f"{len(selection.slack_accounts)} with a Slack thread. Callers can read only these "
                                "and can prepare and reread briefs for them. Writes are off.")
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
            return _account_list(*store.search_accounts(connection, args.query))
        result = store.get_account(connection, args.account_id)
        if result is None:
            raise ValueError(f"account not found: {args.account_id}")
        return result


def _failure(exc, args):
    if args.command == "slack":
        return (_slack_source_failure(exc, args) if args.action in ("map", "check") else
                _slack_failure(exc, args))
    if (type(exc).__name__ in ("SlackMappingError", "SlackReadError", "SlackAuthError") or
            (getattr(exc, "code", None) == "source_identity_mismatch"
             and getattr(args, "slack_backend", None) == "slack")):
        return _slack_source_failure(exc, args)
    from .units import BriefRejected
    import sqlite3
    command = args.command
    next_command = f"gtm-connector {command} --help"
    message = str(exc)
    code = 2 if isinstance(exc, ValueError) else 1
    if type(exc).__name__ == "CallsImportError":
        code = exc.exit_code
        message = f"{exc.code}: {CALL_FAILURES[exc.code]}"
        next_command = "gtm-connector calls import --help"
        if exc.code == "calls_import_not_found":
            next_command = "gtm-connector calls list --state-dir " + shlex.quote(str(args.state_dir))
        elif exc.code in ("calls_account_not_found", "calls_account_ambiguous"):
            next_command = "gtm-connector accounts --help"
        return _error(message, code, next_command, as_json=args.json)
    elif isinstance(exc, BriefRejected) or (command in ("demo", "verify") and message.startswith("demo ")):
        code = 4
    elif getattr(exc, "code", None) in ("salesforce_environment_mismatch", "source_identity_mismatch"):
        code = 3
    elif (type(exc).__name__ in ("SalesforceReadError", "BriefingError", "InspectorConnectionError") or
          type(exc).__module__.startswith(("httpx", "httpcore"))):
        code = 2 if message.startswith("salesforce_invalid_configuration") else 5
    elif type(exc).__name__ == "StateInUseError":
        code = 1
        next_command = f"gtm-connector audit --state-dir {shlex.quote(str(args.state_dir))}"
    elif isinstance(exc, sqlite3.Error):
        code = 1
    if command == "calls" and isinstance(exc, (OSError, sqlite3.Error)):
        message = "The private transcript operation could not finish. Check the source and state folder permissions."
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
        if ((command == "calls" and args.action in ("list", "inspect"))
                or getattr(args, "calls_backend", None) == "imported"):
            # Keep the first sentence. The fresh-state advice after it applies only to import.
            message = message.split(". ", 1)[0].rstrip(".") + "."
            recovery = ("Select the original existing import state folder with --state-dir. " if command == "calls"
                        else "Restore the original CRM connection and select its existing import state folder "
                             "with --state-dir. ")
            if command != "calls" and getattr(args, "crm_backend", None) == "salesforce":
                recovery += ("Use the original --sf-org or environment credentials and "
                             "the original --sf-environment value. ")
            message = (f"source_identity_mismatch: {message} "
                       + recovery +
                       "To start importing, run calls import --help. Existing files are retained.")
            next_command = "gtm-connector calls import --help"
        else:
            imported = command == "calls" or getattr(args, "calls_backend", None) == "imported"
            folder = "imported-calls-fresh" if imported else "salesforce-fresh"
            fresh = args.state_dir.parent / folder
            suffix = 2
            while os.path.lexists(fresh):
                fresh = args.state_dir.parent / f"{folder}-{suffix}"
                suffix += 1
            message = (f"source_identity_mismatch: {message} "
                       f"Repeat this command with --state-dir {shlex.quote(str(fresh))}. Existing files are retained.")
    if "audit database not found" in message:
        next_command = f"gtm-connector demo --state-dir {shlex.quote(str(args.state_dir))}"
    elif "database not found" in message:
        next_command = f"gtm-connector seed --db {shlex.quote(str(args.db))}"
    elif "account not found" in message:
        next_command = ("gtm-connector accounts --help" if command == "calls" or
                        getattr(args, "crm_backend", None) == "salesforce" else
                        f"gtm-connector accounts --db {shlex.quote(str(args.db))}")
    elif command == "connect" and (
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
        if candidate == "slack":
            index = arguments.index("slack") + 1
            action = arguments[index] if index < len(arguments) else ""
            child = " " + action if action in ("connect", "status", "disconnect", "map", "check") else ""
            return _error("The Slack arguments do not match this action. Check its required arguments and options.",
                          2, "gtm-connector slack" + child + " --help", as_json=as_json)
        if candidate == "calls":
            index = arguments.index("calls") + 1
            action = arguments[index] if index < len(arguments) else ""
            suggestion = "gtm-connector calls" + (" " + action if action in ("import", "list", "inspect") else "")
            return _error("The calls arguments do not match this action. Check its required arguments and options.",
                          2, suggestion + " --help", as_json=as_json)
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
                  human_result(args.command, result, crm_backend=getattr(args, "crm_backend", "sqlite"),
                               state_dir=getattr(args, "state_dir", None), action=getattr(args, "action", None),
                               connection_dir=getattr(args, "connection_dir", None)))
        if args.command == "accounts" and isinstance(result, dict) and not args.json:
            # On stderr, so piped rows stay rows. The flush keeps it the closing line when both share a stream.
            sys.stdout.flush()
            print(f"Showing the first {len(result['accounts'])} accounts. More match; narrow the search "
                  "with --query.", file=sys.stderr)
        if (args.command == "slack" and args.action == "disconnect" and result["remote_revoke_requested"]
                and not result["remote_access_token_revoked"]):
            reason = result["revocation_error"]
            detail = SLACK_FAILURES.get(reason, SLACK_FAILURES["slack_unavailable"])
            return _error("Remote access-token revocation is unconfirmed. " + detail +
                          " Manage the whole grant in Slack settings.", 5,
                          "gtm-connector slack disconnect --help", as_json=args.json)
        if args.command == "slack" and args.action == "check" and result["status"] != "passed":
            return _error("Slack thread checks failed. Apply the next action beside each failed step.", 4,
                          "gtm-connector slack check --help", as_json=args.json)
        if args.command == "doctor" and result["status"] != "passed":
            return _error("Setup checks failed. Apply the fix beside each failed check.", 4,
                          f"gtm-connector doctor --state-dir {shlex.quote(str(args.state_dir))}", as_json=args.json)
        if args.command == "salesforce" and result["status"] != "passed":
            return _error("Salesforce checks failed. Apply the fix beside each failed step.", 4,
                          "gtm-connector salesforce check --help", as_json=args.json)
        return 0
    except KeyboardInterrupt:
        if args.command == "slack":
            if args.action in ("map", "check"):
                return _error("The Slack source operation was interrupted. Existing state and committed receipts "
                              "are retained. Check the selected state before retrying.", 130,
                              f"gtm-connector slack {args.action} --help", as_json=args.json)
            return _error("Slack authorization was interrupted. Inspect saved status; completed remote approval "
                          "or local saves may remain.", 130, _slack_status_command(args.connection_dir),
                          as_json=args.json)
        raise
    except BrokenPipeError:
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
