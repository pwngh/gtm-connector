"""Run generated completion in the actual supported shells."""

from pathlib import Path
import os
import shlex
import shutil
import subprocess
import tempfile
import unittest

from gtm_connector.cli import build_parser
from gtm_connector.completion import generate


class CompletionTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)

    def script(self, shell):
        executable = shutil.which(shell)
        if executable is None:
            if os.environ.get("GTM_REQUIRE_SHELLS") == "1":
                self.fail(f"Required completion test shell is missing: {shell}")
            self.skipTest(f"{shell} is not installed")
        path = self.root / ("complete." + shell)
        path.write_text(generate(build_parser(), shell))
        result = subprocess.run([executable, "-n", str(path)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        return executable, shlex.quote(str(path))

    def test_bash_command_flag_and_choice(self):
        executable, path = self.script("bash")
        executables = {executable}
        # macOS still ships Bash 3.2. Exercise every case on that version too.
        if Path("/bin/bash").exists():
            executables.add("/bin/bash")
        cases = ((["gtm-connector", "bri"], "brief"),
                 (["gtm-connector", "ins"], "inspect"),
                 (["gtm-connector", "inspect", "brief-x", "--ro"], "--role"),
                 (["gtm-connector", "inspect", "brief-x", "--role", "cu"], "customer"),
                 (["gtm-connector", "de"], "debug"),
                 (["gtm-connector", "debug", "se"], "serve"),
                 (["gtm-connector", "debug", "--json", "ve"], "verify"),
                 (["gtm-connector", "--json", "debug", "serve", "--sel"], "--selection"),
                 (["gtm-connector", "connect", "--environment", "sa"], "sandbox"),
                 (["gtm-connector", "connect", "salesforce", "--environment", "pr"], "production"),
                 (["gtm-connector", "debug", "salesforce", "--sf-environment", "sandbox", "ch"], "check"),
                 (["gtm-connector", "debug", "calls", "--json", "im"], "import"),
                 (["gtm-connector", "debug", "calls", "--state-dir", "import", "ins"], "inspect"),
                 (["gtm-connector", "debug", "calls", "import", "--sp"], "--speaker-names"),
                 (["gtm-connector", "debug", "calls", "inspect", "--f"], "--full"),
                 (["gtm-connector", "debug", "slack", "--json", "con"], "connect"),
                 (["gtm-connector", "debug", "slack", "--state-dir", "connect", "sta"], "status"),
                 (["gtm-connector", "debug", "slack", "status", "--ch"], "--check"),
                 (["gtm-connector", "debug", "slack", "disconnect", "--re"], "--revoke"),
                 (["gtm-connector", "debug", "slack", "--json", "ma"], "map"),
                 (["gtm-connector", "debug", "slack", "map", "--thread-u"], "--thread-url"),
                 (["gtm-connector", "debug", "slack", "check", "--acc"], "--account"),
                 (["gtm-connector", "connect", "sal"], "salesforce"),
                 (["gtm-connector", "connect", "st"], "status"),
                 (["gtm-connector", "debug", "slack", "connect", "--user-scope", "groups", ":", "hi"], "history"),
                 (["gtm-connector", "connect", "calls", "--se"], "--seller"),
                 (["gtm-connector", "connect", "--state-dir", "demo", "sl"], "slack"),
                 (["gtm-connector", "connect", "cl"], "claude-code"),
                 (["gtm-connector", "connect", "--json", "ins"], "inspector"),
                 (["gtm-connector", "connect", "--state-dir", "inspector", "cu"], "cursor"),
                 (["gtm-connector", "connect", "--state-dir=inspector", "cu"], "cursor"),
                 (["gtm-connector", "connect", "--scope", "crm", ":", "read", "--json", "ins"], "inspector"),
                 (["gtm-connector", "connect", "inspector", "--scope", "crm", ":", "r"], "read"),
                 (["gtm-connector", "connect", "inspector", "--scope", "crm", ":", ""], "read"),
                 (["gtm-connector", "debug", "completion", "--json", "ba"], "bash"))
        for shell in sorted(executables):
            for words, expected in cases:
                with self.subTest(shell=shell, words=words):
                    script = (f"source {path}; COMP_WORDS=({shlex.join(words)}); COMP_CWORD={len(words)-1}; "
                              '_gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"')
                    result = subprocess.run([shell, "-c", script], capture_output=True, text=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(expected, result.stdout.splitlines())

    def test_bash_colon_prefix_and_supplied_positional(self):
        executable, path = self.script("bash")
        # Do not remove the prefix if the user has removed ':' from Bash's word breaks.
        script = (f"source {path}; "
                  'COMP_WORDBREAKS=${COMP_WORDBREAKS//:}; '
                  'COMP_WORDS=(gtm-connector connect inspector --scope crm:r); COMP_CWORD=4; '
                  '_gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"')
        result = subprocess.run([executable, "-c", script], capture_output=True, text=True)
        self.assertEqual(result.stdout.strip(), "crm:read", result.stderr)
        script = (f"source {path}; "
                  'COMP_WORDS=(gtm-connector connect --json inspector cl); COMP_CWORD=4; '
                  '_gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"')
        result = subprocess.run([executable, "-c", script], capture_output=True, text=True)
        self.assertEqual(result.stdout.strip(), "", result.stderr)

    def test_bash_nested_options_and_import_filename(self):
        executable, path = self.script("bash")
        exported = self.root / "invented transcript.vtt"
        exported.write_text("WEBVTT\n")
        for words, expected, forbidden in (
            (["gtm-connector", "debug", "calls", "inspect", "--"], "--full", "--account"),
            (["gtm-connector", "debug", "calls", "list", "--"], "--after", "--full"),
            (["gtm-connector", "debug", "slack", "status", "--"], "--check", "--client-id"),
            (["gtm-connector", "debug", "slack", "disconnect", "--"], "--revoke", "--user-scope"),
            (["gtm-connector", "debug", "slack", "map", "--"], "--thread-url", "--client-id"),
            (["gtm-connector", "debug", "slack", "check", "--"], "--account", "--thread-url"),
            (["gtm-connector", "brief", "acc-003", "--"], "--as-of", "--query"),
            (["gtm-connector", "debug", "serve", "--"], "--selection", "--as-of"),
            (["gtm-connector", "accounts", "--"], "--query", "--as-of"),
            (["gtm-connector", "connect", "--"], "--org", "--check"),
            (["gtm-connector", "debug", "calls", "import", str(self.root / "invented")], str(exported), "--full"),
        ):
            script = (f"source {path}; COMP_WORDS=({shlex.join(words)}); COMP_CWORD={len(words)-1}; "
                      '_gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"')
            result = subprocess.run([executable, "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, result.stdout.splitlines())
            self.assertNotIn(forbidden, result.stdout.splitlines())

    def test_zsh_completion_dispatch(self):
        executable, path = self.script("zsh")
        for words, expected in ((["gtm-connector", "bri"], "brief"),
                                (["gtm-connector", "inspect", "brief-x", "--role", "se"], "seller"),
                                (["gtm-connector", "debug", "--json", "do"], "doctor"),
                                (["gtm-connector", "connect", "--environment", "sa"], "sandbox"),
                                (["gtm-connector", "connect", "sal"], "salesforce"),
                                (["gtm-connector", "debug", "calls", "--json", "ins"], "inspect"),
                                (["gtm-connector", "debug", "calls", "--state-dir", "import", "li"], "list"),
                                (["gtm-connector", "debug", "calls", "import", "--sp"], "--speaker-names"),
                                (["gtm-connector", "debug", "calls", "inspect", "--f"], "--full"),
                                (["gtm-connector", "debug", "slack", "--json", "sta"], "status"),
                                (["gtm-connector", "debug", "slack", "--state-dir", "status", "dis"], "disconnect"),
                                (["gtm-connector", "debug", "slack", "status", "--ch"], "--check"),
                                (["gtm-connector", "debug", "slack", "--json", "ma"], "map"),
                                (["gtm-connector", "debug", "slack", "map", "--thread-u"], "--thread-url"),
                                (["gtm-connector", "connect", "calls", "--se"], "--seller"),
                                (["gtm-connector", "debug", "slack", "connect", "--user-scope", "groups:hi"], "groups:history"),
                                (["gtm-connector", "connect", "st"], "status"),
                                (["gtm-connector", "debug", "serve", "--sel"], "--selection"),
                                (["gtm-connector", "connect", "--json", "ins"], "inspector"),
                                (["gtm-connector", "connect", "--state-dir", "inspector", "cu"], "cursor"),
                                (["gtm-connector", "connect", "--scope", "crm:read", "ins"], "inspector"),
                                (["gtm-connector", "connect", "inspector", "--scope", "crm:r"], "crm:read")):
            # Run the shell function with zsh's real words/CURRENT variables.
            # Intercept compadd to observe its candidates outside the ZLE editor.
            script = (f"autoload -Uz compinit; compinit -D; source {path}; "
                      'compadd() { print -l -- "$@"; }; '
                      f"words=({shlex.join(words)}); CURRENT={len(words)}; _gtm_connector")
            result = subprocess.run([executable, "-fc", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, result.stdout.splitlines())
        script = (f"autoload -Uz compinit; compinit -D; source {path}; "
                  'compadd() { print -l -- "$@"; }; '
                  'words=(gtm-connector connect --json inspector cl); CURRENT=5; _gtm_connector')
        result = subprocess.run([executable, "-fc", script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("claude-code", result.stdout.splitlines())

    def test_fish_command_flag_and_choice(self):
        executable, path = self.script("fish")
        for words, expected in (("gtm-connector bri", "brief"),
                                ("gtm-connector inspect brief-x --role un", "unknown"),
                                ("gtm-connector debug co", "completion"),
                                ("gtm-connector debug serve --sel", "--selection"),
                                ("gtm-connector connect sal", "salesforce"),
                                ("gtm-connector connect --environment pr", "production"),
                                ("gtm-connector debug calls --json li", "list"),
                                ("gtm-connector debug calls --state-dir import ins", "inspect"),
                                ("gtm-connector debug calls import --sp", "--speaker-names"),
                                ("gtm-connector debug calls inspect --f", "--full"),
                                ("gtm-connector debug slack --json con", "connect"),
                                ("gtm-connector debug slack --state-dir status dis", "disconnect"),
                                ("gtm-connector debug slack status --ch", "--check"),
                                ("gtm-connector debug slack --json ma", "map"),
                                ("gtm-connector debug slack check --acc", "--account"),
                                ("gtm-connector connect calls --se", "--seller"),
                                ("gtm-connector debug slack connect --user-scope groups:hi", "groups:history"),
                                ("gtm-connector connect st", "status"),
                                ("gtm-connector connect --json ins", "inspector"),
                                ("gtm-connector connect --state-dir inspector cu", "cursor"),
                                ("gtm-connector connect --scope crm:read ins", "inspector"),
                                ("gtm-connector connect inspector --scope crm:r", "crm:read")):
            result = subprocess.run([executable, "-c", f"source {path}; complete -C {shlex.quote(words)}"],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, [line.split("\t")[0] for line in result.stdout.splitlines()])
        result = subprocess.run([executable, "-c", f"source {path}; "
                                 "complete -C 'gtm-connector connect --json inspector cl'"],
                                capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertNotIn("claude-code", [line.split("\t")[0] for line in result.stdout.splitlines()])


if __name__ == "__main__":
    unittest.main()
