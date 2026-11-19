"""Any argument list to the offline commands exits with a documented code and prints no traceback."""

from contextlib import redirect_stderr, redirect_stdout
import io
import os
from pathlib import Path
import tempfile
import unittest

from hypothesis import given, HealthCheck, settings, strategies as st

from gtm_connector.cli import main


EXAMPLES = int(os.environ.get("GTM_FUZZ_EXAMPLES", "1500"))
# Commands that reach no network, browser, or long-running server.
COMMANDS = ("brief", "trace", "accounts", "account", "audit", "doctor", "calls", "completion", "--help",
            "--version")
WORDS = (
    "acc-001", "acc-007", "acc-999", "", " ", "-", "--", "--json", "--with-slack", "--without-calls", "--as-of",
    "2027-01-15", "2027-13-45", "--state-dir", "STATE", "--db", "DB", "--query", "x" * 300, "é", "\x00", "\ud800",
    "1", "0", "list", "import", "show", "FILE.txt", "FILE.vtt", "FILE.srt", "--account",
    "--lines", "1:2", "9:1", "--cues", "0:0", "--called-at", "2027-01-15T10:00:00Z", "yesterday", "--tool",
    "get_account", "--limit", "-1", "99999999999999999999", "brief-" + "0" * 32, "--reassign", "--calls-backend",
    "imported", "--account-number", "A" * 41, "bash", "zsh", "fish", "--help",
)
FILES = {"FILE.txt": "Dana: hello\nSam: forty seats\n", "FILE.vtt": "WEBVTT\n\n00:01.000 --> 00:02.000\nhi\n",
         "FILE.srt": "1\n00:00:01,000 --> 00:00:02,000\nhi\n"}
# Words that would start a server, a browser, or a real source.
LIVE = {"--follow", "serve", "connect", "salesforce", "slack", "demo", "verify"}


class CommandLineFuzz(unittest.TestCase):
    @settings(max_examples=EXAMPLES, deadline=None, derandomize=True, database=None,
              suppress_health_check=list(HealthCheck))
    @given(st.sampled_from(COMMANDS), st.lists(st.one_of(st.sampled_from(WORDS), st.text(max_size=8)), max_size=7))
    def test_any_arguments_exit_with_a_documented_code_and_no_traceback(self, command, rest):
        if LIVE & set(rest):
            return
        with tempfile.TemporaryDirectory() as folder:
            for name, body in FILES.items():
                Path(folder, name).write_text(body)
            places = {"STATE": folder + "/state", "DB": folder + "/crm.sqlite3",
                      **{name: folder + "/" + name for name in FILES}}
            argv = [command, *(places.get(word, word) for word in rest)]
            out, err = io.StringIO(), io.StringIO()
            here = os.getcwd()
            os.chdir(folder)  # Default state folders land in the temporary folder.
            try:
                with redirect_stdout(out), redirect_stderr(err):
                    try:
                        code = main(argv)
                    except SystemExit as stop:
                        code = stop.code
            finally:
                os.chdir(here)
        # docs/cli.md: 0 done, 1 local failure, 2 usage, 3 refused, 4 check failed, 5 upstream.
        self.assertIn(code, (0, 1, 2, 3, 4, 5, None), argv)
        self.assertNotIn("Traceback", out.getvalue() + err.getvalue(), argv)


if __name__ == "__main__":
    unittest.main()
