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
        for words, expected in ((["gtm-connector", "bri"], "brief"),
                                (["gtm-connector", "--json", "serve", "--crm"], "--crm-backend"),
                                (["gtm-connector", "brief", "--crm-backend", "sa"], "salesforce"),
                                (["gtm-connector", "connect", "cl"], "claude-code")):
            script = (f"source {path}; COMP_WORDS=({shlex.join(words)}); COMP_CWORD={len(words)-1}; "
                      '_gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"')
            result = subprocess.run([executable, "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, result.stdout.splitlines())
        # macOS still ships Bash 3.2. It must not need mapfile/readarray.
        if Path("/bin/bash").exists():
            result = subprocess.run(["/bin/bash", "-c", f"source {path}; COMP_WORDS=(gtm-connector bri); "
                                     'COMP_CWORD=1; _gtm_connector_complete; printf "%s\\n" "${COMPREPLY[@]}"'],
                                    capture_output=True, text=True)
            self.assertEqual(result.stdout.strip(), "brief", result.stderr)

    def test_zsh_completion_dispatch(self):
        executable, path = self.script("zsh")
        for words, expected in ((["gtm-connector", "bri"], "brief"),
                                (["gtm-connector", "brief", "--crm-backend", "sa"], "salesforce"),
                                (["gtm-connector", "serve", "--crm"], "--crm-backend")):
            # Run the shell function with zsh's real words/CURRENT variables.
            # Intercept compadd to observe its candidates outside the ZLE editor.
            script = (f"autoload -Uz compinit; compinit -D; source {path}; "
                      'compadd() { print -l -- "$@"; }; '
                      f"words=({shlex.join(words)}); CURRENT={len(words)}; _gtm_connector")
            result = subprocess.run([executable, "-fc", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, result.stdout.splitlines())

    def test_fish_command_flag_and_choice(self):
        executable, path = self.script("fish")
        for words, expected in (("gtm-connector bri", "brief"),
                                ("gtm-connector serve --crm", "--crm-backend"),
                                ("gtm-connector brief --crm-backend sa", "salesforce")):
            result = subprocess.run([executable, "-c", f"source {path}; complete -C {shlex.quote(words)}"],
                                    capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(expected, [line.split("\t")[0] for line in result.stdout.splitlines()])


if __name__ == "__main__":
    unittest.main()
