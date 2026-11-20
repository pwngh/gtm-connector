"""Public command-line behavior without relying on terminal layout details."""

from contextlib import redirect_stderr, redirect_stdout
import errno
import io
import json
import os
import select
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
TOP = ("demo", "connect", "accounts", "brief", "inspect", "audit", "mcp", "debug")
DEBUG = ("serve", "doctor", "verify", "completion", "salesforce", "calls", "slack")
COMMANDS = (*((name,) for name in TOP), *(("debug", name) for name in DEBUG))


class CLIContractTests(unittest.TestCase):
    def run_cli(self, *arguments, env=None):
        return subprocess.run(
            [sys.executable, "-m", "gtm_connector", *arguments], cwd=ROOT,
            env=env, capture_output=True, text=True, check=False, timeout=20,
        )

    def test_no_arguments_offer_a_short_starting_point(self):
        result = self.run_cli()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("demo", result.stdout)
        self.assertIn("--help", result.stdout)
        self.assertLessEqual(len(result.stdout.splitlines()), 24)
        self.assertEqual(result.stderr, "")

    def test_every_command_has_an_example_and_json_option(self):
        for command in COMMANDS:
            with self.subTest(command=command):
                result = self.run_cli(*command, "--help")
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("example", result.stdout.lower())
                self.assertIn("--json", result.stdout)
                self.assertIn(" ".join(command), result.stdout)
                self.assertEqual(result.stderr, "")

    def test_calls_action_help_only_shows_valid_options(self):
        for action in ("import", "list", "inspect"):
            result = self.run_cli("debug", "calls", action, "--help")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"Example: gtm-connector debug calls {action}", result.stdout)
            self.assertIn("--json", result.stdout)
            self.assertIn("--state-dir", result.stdout)
            if action != "import":
                for flag in ("--called-at", "--reassign", "--cues"):
                    self.assertNotIn(flag, result.stdout)
            if action == "inspect":
                self.assertIn("--full", result.stdout)
                self.assertNotIn("--account", result.stdout)
            elif action == "list":
                self.assertIn("--after", result.stdout)
                self.assertNotIn("--full", result.stdout)

    def test_slack_action_help_only_shows_valid_authentication_options(self):
        for action in ("connect", "status", "disconnect"):
            result = self.run_cli("debug", "slack", action, "--help")
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"Example: gtm-connector debug slack {action}", result.stdout)
            self.assertIn("--json", result.stdout)
            self.assertIn("--state-dir", result.stdout)
            self.assertNotIn("--connection-dir", result.stdout)
            if action == "connect":
                self.assertIn("--redirect-uri", result.stdout)
                self.assertIn("already be registered", result.stdout)
                self.assertNotIn("--revoke", result.stdout)
                self.assertNotIn("--check", result.stdout)
            else:
                for flag in ("--client-id", "--redirect-uri", "--team-id", "--user-scope"):
                    self.assertNotIn(flag, result.stdout)
                self.assertIn("--check" if action == "status" else "--revoke", result.stdout)
                self.assertNotIn("--revoke" if action == "status" else "--check", result.stdout)

    def test_demo_and_verification_have_separate_defaults_without_changing_daily_commands(self):
        from gtm_connector.cli import build_parser

        parser = build_parser()
        self.assertEqual(parser.parse_args(["demo"]).state_dir, Path("var/renewal-demo"))
        self.assertEqual(parser.parse_args(["debug", "verify"]).state_dir, Path("var/verification"))
        for arguments in (["brief", "acc-007"], ["debug", "serve"], ["debug", "doctor"], ["audit"],
                          ["inspect", "brief-x", "1"], ["accounts"], ["inspect", "acc-007"], ["connect"],
                          ["connect", "demo"], ["mcp", "stdio", "--scope", "crm:read"], ["debug", "calls", "list"],
                          ["debug", "slack", "status"],
                          ["debug", "calls", "import", "call.vtt", "--account", "acc-007"],
                          ["debug", "slack", "check", "--account", "acc-007"]):
            self.assertEqual(parser.parse_args(arguments).state_dir, Path("var/v2"))
        for command in (["demo"], ["debug", "verify"]):
            self.assertEqual(parser.parse_args([*command, "--state-dir", "var/chosen"]).state_dir, Path("var/chosen"))

    def test_version_and_help_do_not_attempt_runtime_imports(self):
        # Block import attempts, rather than merely checking the final module set.
        # This also catches an attempted heavy import that gets caught internally.
        program = r'''
import runpy
import sys
blocked = {"sqlite3", "_sqlite3", "mcp", "mcp_types", "httpx", "httpx2", "httpcore", "httpcore2", "uvicorn"}
class Guard:
    def find_spec(self, fullname, path=None, target=None):
        if fullname.split(".")[0] in blocked or fullname in {
            "gtm_connector.runtime", "gtm_connector.gateway", "gtm_connector.auth",
            "gtm_connector.slack_auth", "gtm_connector.slack_callback", "gtm_connector.slack_mapping",
            "gtm_connector.slack_reader", "gtm_connector.slack_workflow"
        }:
            raise AssertionError("forbidden startup import: " + fullname)
sys.meta_path.insert(0, Guard())
sys.argv = ["gtm-connector", *sys.argv[1:]]
runpy.run_module("gtm_connector", run_name="__main__")
'''
        for arguments in (("--version",), ("--help",),
                          *(("debug", "calls", action, "--help") for action in ("import", "list", "inspect")),
                          *(("debug", "slack", action, "--help")
                            for action in ("connect", "status", "disconnect", "map", "check")),
                          *(("debug", "completion", shell) for shell in ("bash", "zsh", "fish")),
                          *((*command, "--help") for command in COMMANDS)):
            with self.subTest(arguments=arguments):
                result = subprocess.run(
                    [sys.executable, "-c", program, *arguments], cwd=ROOT,
                    capture_output=True, text=True, check=False, timeout=20,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertNotIn("forbidden startup import", result.stdout + result.stderr)

    def test_interrupt_during_a_command_is_clean_and_machine_readable(self):
        from gtm_connector.cli import main

        with patch("gtm_connector.cli._run", side_effect=KeyboardInterrupt), \
                redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            self.assertEqual(main(["demo", "--json"]), 130)
        self.assertEqual(out.getvalue(), "")
        error = json.loads(err.getvalue())["error"]
        self.assertEqual(error["code"], "interrupted")
        self.assertIn("retained", error["message"])
        self.assertIn("gtm-connector", error["next"])

    def test_version_has_a_json_shape_as_well_as_text(self):
        from gtm_connector import __version__

        text_result = self.run_cli("--version")
        self.assertEqual(text_result.returncode, 0, text_result.stderr)
        self.assertEqual(text_result.stdout.strip(), "gtm-connector " + __version__)
        json_result = self.run_cli("--version", "--json")
        self.assertEqual(json_result.returncode, 0, json_result.stderr)
        self.assertEqual(json.loads(json_result.stdout), {"version": __version__})

    def test_mistyped_command_suggests_the_known_command(self):
        result = self.run_cli("breif")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        self.assertIn("brief", result.stderr)
        self.assertIn("gtm-connector", result.stderr)
        self.assertNotIn("Traceback", result.stderr)
        # A command under debug is suggested where it is.
        for arguments, suggestion in ((("serve",), "gtm-connector debug serve --help"),
                                      (("debug", "doctr"), "gtm-connector debug doctor --help"),
                                      (("debug",), "gtm-connector debug --help"),
                                      (("debug", "calls", "imprt"), "gtm-connector debug calls --help")):
            with self.subTest(arguments=arguments):
                result = self.run_cli(*arguments, "--json")
                self.assertEqual((result.returncode, result.stdout), (2, ""))
                self.assertEqual(json.loads(result.stderr)["error"]["next"], suggestion)

    def test_the_top_level_help_lists_the_seven_commands_and_debug_in_fifteen_lines(self):
        for arguments in (("--help",), ("-h",), ("help",)):
            with self.subTest(arguments=arguments):
                result = self.run_cli(*arguments, env={**os.environ, "COLUMNS": "80"})
                self.assertEqual((result.returncode, result.stderr), (0, ""))
                lines = result.stdout.splitlines()
                self.assertLessEqual(len(lines), 15)
                self.assertLessEqual(max(map(len, lines)), 80)
                self.assertEqual([line.split()[0] for line in lines if line.startswith("  ")], list(TOP))
        listed = self.run_cli("debug", "--help").stdout.splitlines()
        self.assertEqual([line.split()[0] for line in listed if line.startswith("    ") and line[4] != " "],
                         list(DEBUG))

    def test_json_before_or_after_a_command_gives_the_same_result(self):
        from gtm_connector.cli import main

        def run(*arguments):
            with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
                code = main(list(arguments))
            return code, out.getvalue(), err.getvalue()

        with tempfile.TemporaryDirectory() as directory:
            state = ("--state-dir", directory)
            self.assertEqual(run("--json", "connect", "demo", *state)[0], 0)
            brief = json.loads(run("--json", "brief", "acc-003", *state)[1])
            self.assertTrue(brief["text"].startswith("DEAL CARD"))
            receipt = json.loads(run("audit", *state, "--json")[1])[0]["audit_id"]
            for command in (("connect", "status", *state), ("accounts", "--query", "Thornfield", *state),
                            ("inspect", "acc-003", *state), ("inspect", brief["brief_id"], *state),
                            ("inspect", brief["brief_id"], "5", *state), ("inspect", receipt, *state),
                            ("inspect", "acc-missing", *state), ("audit", "--brief", brief["brief_id"], *state),
                            ("debug", "completion", "fish"), ("debug", "doctor", *state),
                            ("debug", "slack", "status", *state), ("debug", "calls", "list", *state)):
                with self.subTest(command=command[:2]):
                    before, after = run("--json", *command), run(*command, "--json")
                    self.assertEqual(before, after)
                    json.loads(before[1] if before[0] in (0, 4) else before[2])
            # Under debug, --json may also stand between debug and its command.
            self.assertEqual(run("debug", "--json", "completion", "fish"), run("debug", "completion", "fish", "--json"))
            self.assertEqual(run("debug", "calls", "--json", "list", *state), run("debug", "calls", "list", *state,
                                                                                 "--json"))

    def test_piped_output_has_no_escape_codes(self):
        environment = {key: value for key, value in os.environ.items() if key != "NO_COLOR"}
        environment["TERM"] = "xterm-256color"
        with tempfile.TemporaryDirectory() as directory:
            state = ("--state-dir", directory)
            self.assertEqual(self.run_cli("connect", "demo", *state).returncode, 0)
            brief = json.loads(self.run_cli("brief", "acc-003", *state, "--json").stdout)["brief_id"]
            receipt = json.loads(self.run_cli("audit", *state, "--json").stdout)[0]["audit_id"]
            for command in (("--help",), ("debug", "--help"), ("connect", "status", *state), ("accounts", *state),
                            ("brief", "acc-003", *state), ("inspect", "acc-003", *state), ("inspect", brief, *state),
                            ("inspect", brief, "5", "1", *state), ("inspect", receipt, *state), ("audit", *state),
                            ("inspect", "acc-missing", *state), ("debug", "doctor", *state),
                            ("debug", "slack", "status", *state), ("debug", "completion", "zsh")):
                with self.subTest(command=command[:2]):
                    result = self.run_cli(*command, env=environment)
                    self.assertTrue(result.stdout or result.stderr)
                    self.assertNotIn("\x1b", result.stdout + result.stderr)
                    self.assertNotIn("Traceback", result.stderr)

    def test_json_is_explicit_and_works_before_or_after_command(self):
        with tempfile.TemporaryDirectory() as directory:
            connected = self.run_cli("--json", "connect", "demo", "--state-dir", directory)
            self.assertEqual(connected.returncode, 0, connected.stderr)
            self.assertEqual(json.loads(connected.stdout)["sources"]["crm"], {"provider": "demo"})
            self.assertEqual(connected.stderr, "")
            accounts = self.run_cli("accounts", "--state-dir", directory, "--json")
            self.assertEqual(accounts.returncode, 0, accounts.stderr)
            self.assertEqual(len(json.loads(accounts.stdout)), 30)
            self.assertEqual(accounts.stderr, "")
            plain = self.run_cli("accounts", "--state-dir", directory)
            self.assertEqual(plain.returncode, 0, plain.stderr)
            self.assertIn("acc-001", plain.stdout)
            self.assertNotIn("\x1b", plain.stdout)
            with self.assertRaises(json.JSONDecodeError):
                json.loads(plain.stdout)

    def test_json_failure_is_on_stderr_and_has_a_recovery_command(self):
        result = self.run_cli("breif", "--json")
        self.assertEqual(result.returncode, 2)
        self.assertEqual(result.stdout, "")
        error = json.loads(result.stderr)["error"]
        self.assertEqual(set(error), {"code", "message", "next"})
        self.assertTrue(error["code"])
        self.assertTrue(error["message"])
        self.assertIn("gtm-connector", error["next"])

    def test_version_flag_respects_arguments_and_option_terminator(self):
        for arguments in (("breif", "--version"), ("accounts", "--query", "--", "--version")):
            with self.subTest(arguments=arguments):
                result = self.run_cli(*arguments)
                self.assertEqual(result.returncode, 2)
                self.assertEqual(result.stdout, "")
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(self.run_cli("connect", "demo", "--state-dir", directory).returncode, 0)
            result = self.run_cli("inspect", "--state-dir", directory, "--", "--version")
            self.assertEqual(result.returncode, 2)
            self.assertIn("inspect takes an account ID", result.stderr)
        typo = self.run_cli("--json", "breif")
        self.assertIn("gtm-connector brief --help", json.loads(typo.stderr)["error"]["next"])

    def test_closed_reader_is_quiet_for_help_version_and_command_output(self):
        with tempfile.TemporaryDirectory() as directory:
            for arguments in (("--version",), ("--help",),
                              ("connect", "demo", "--state-dir", directory)):
                read_fd, write_fd = os.pipe()
                os.close(read_fd)
                try:
                    process = subprocess.run([sys.executable, "-m", "gtm_connector", *arguments],
                                             cwd=ROOT, stdout=write_fd, stderr=subprocess.PIPE,
                                             text=True, timeout=20)
                finally:
                    os.close(write_fd)
                self.assertEqual(process.returncode, 0, process.stderr)
                self.assertEqual(process.stderr, "")

    def test_removed_commands_and_options_are_usage_errors(self):
        for arguments in (("evaluate",), ("usage",), ("check-brief", "brief-x", "candidate.txt"),
                          ("brief", "acc-001", "--adapter", "ollama"), ("brief", "acc-001", "--model", "local"),
                          ("trace", "brief-" + "0" * 32, "1"), ("account", "acc-003"), ("debug",),
                          ("debug", "trace"), ("debug", "inspect", "acc-003"),
                          *((name,) for name in DEBUG if name not in ("calls", "slack")),
                          ("calls", "list"), ("slack", "status")):
            with self.subTest(arguments=arguments):
                result = self.run_cli(*arguments)
                self.assertEqual(result.returncode, 2, result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertNotIn("Traceback", result.stderr)

    def test_empty_as_of_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            self.assertEqual(self.run_cli("connect", "demo", "--state-dir", directory).returncode, 0)
            result = self.run_cli("brief", "acc-001", "--as-of", "", "--state-dir", directory)
            self.assertNotIn("No CRM is connected", result.stderr)
            self.assertEqual(result.returncode, 2, result.stderr)
            self.assertEqual(result.stdout, "")

    def test_busy_state_and_failed_checks_keep_recovery_context(self):
        from contextlib import redirect_stderr, redirect_stdout
        import io
        from unittest.mock import patch
        from gtm_connector.cli import main
        from gtm_connector.runtime import Runtime
        from gtm_connector import profile
        with tempfile.TemporaryDirectory() as directory, Runtime(directory):
            profile.save(directory, profile.DEMO)
            result = self.run_cli("debug", "serve", "--state-dir", directory, "--json")
            self.assertEqual(result.returncode, 1, result.stderr)
            self.assertIn(directory, json.loads(result.stderr)["error"]["next"])
        with patch("gtm_connector.cli._run", return_value={"status": "failed"}), \
                redirect_stdout(io.StringIO()), redirect_stderr(io.StringIO()) as errors:
            self.assertEqual(main(["debug", "doctor", "--state-dir", "var/my state", "--json"]), 4)
        import shlex
        self.assertEqual(shlex.split(json.loads(errors.getvalue())["error"]["next"]),
                         ["gtm-connector", "debug", "doctor", "--state-dir", "var/my state"])

    def terminal_output(self, *arguments):
        master, slave = os.openpty()
        try:
            with subprocess.Popen(
                [sys.executable, "-m", "gtm_connector", *arguments], cwd=ROOT,
                env={**os.environ, "NO_COLOR": "1", "TERM": "xterm-256color"},
                stdin=subprocess.DEVNULL, stdout=slave, stderr=subprocess.PIPE,
            ) as process:
                os.close(slave)
                slave = None
                output = bytearray()
                deadline = time.monotonic() + 20
                try:
                    while time.monotonic() < deadline:
                        ready, _, _ = select.select([master], [], [], 0.1)
                        if not ready:
                            if process.poll() is not None:
                                break
                            continue
                        try:
                            part = os.read(master, 65536)
                        except OSError as exc:
                            if exc.errno == errno.EIO:
                                break
                            raise
                        if not part:
                            break
                        output.extend(part)
                    _, stderr = process.communicate(timeout=1)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.communicate()
                    raise
            self.assertEqual(process.returncode, 0, stderr)
            return bytes(output)
        finally:
            os.close(master)
            if slave is not None:
                os.close(slave)

    @unittest.skipUnless(hasattr(os, "openpty"), "The supported platform needs a pseudo-terminal.")
    def test_no_color_and_empty_results_work_on_a_terminal(self):
        with tempfile.TemporaryDirectory() as directory:
            connected = self.terminal_output("connect", "demo", "--state-dir", directory)
            self.assertIn(b"SOURCE", connected)
            self.assertIn(b"demo data", connected)
            self.assertNotIn(b"\x1b", connected)
            empty = self.terminal_output("accounts", "--query", "not-a-fixture-account", "--state-dir", directory)
            self.assertIn(b"ACCOUNT", empty)
            self.assertNotIn(b"\x1b", empty)


if __name__ == "__main__":
    unittest.main()
