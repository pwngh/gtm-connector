"""Read an existing Salesforce CLI login without printing or storing secrets."""

from collections.abc import Callable, Iterator
from contextlib import contextmanager
from contextvars import ContextVar
import os
import subprocess
import time

from . import strict_json


# sf can take tens of seconds to start on a busy machine.
_COMMAND_SECONDS = 120
# Both commands of one login read share this limit. It is separate from the budget of any
# Salesforce read, so time spent in sf never shortens a read.
CREDENTIAL_SECONDS = 180
_MAX_OUTPUT_BYTES = 1024 * 1024
_PROGRESS_SECONDS = 0.5
_CODES = frozenset({"salesforce_auth_required", "salesforce_cli_timeout"})
_PROGRESS: ContextVar[Callable[[float], None] | None] = ContextVar("sf_progress", default=None)


class SFCredentialsError(RuntimeError):
    """A fixed error that never includes child output, arguments, or credentials."""

    def __init__(self, code="salesforce_auth_required"):
        self.code = code if code in _CODES else "salesforce_auth_required"
        super().__init__(self.code)


@contextmanager
def reporting(progress: Callable[[float], None] | None) -> Iterator[None]:
    """Call progress(seconds) about twice a second while a login read in this context waits on sf."""
    token = _PROGRESS.set(progress)
    try:
        yield
    finally:
        _PROGRESS.reset(token)


class SFCredentials:
    """Capture two explicit commands using the owner's existing CLI login."""

    def __init__(self, alias):
        if (not isinstance(alias, str) or not 1 <= len(alias) <= 255
                or alias.startswith("-") or any(ord(char) <= 32 or ord(char) == 127 for char in alias)):
            raise SFCredentialsError()
        self._alias = alias
        self.invocations = 0

    def _command(self, words, field, started, deadline):
        limit = min(deadline, time.monotonic() + _COMMAND_SECONDS)
        if time.monotonic() >= limit:
            raise SFCredentialsError("salesforce_cli_timeout")
        environment = dict(os.environ)
        environment.update({"SF_DISABLE_TELEMETRY": "true", "SF_AUTOUPDATE_DISABLE": "true",
                            "SF_DISABLE_AUTOUPDATE": "true",
                            "SF_DISABLE_LOG_FILE": "true", "SFDX_DISABLE_LOG_FILE": "true"})
        progress = _PROGRESS.get()
        self.invocations += 1
        try:
            process = subprocess.Popen(
                ["sf", "org", *words, "--target-org", self._alias, "--json"],
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=environment)
        except (OSError, subprocess.SubprocessError):
            raise SFCredentialsError() from None
        try:
            while True:
                try:
                    wait = max(0.01, min(_PROGRESS_SECONDS, limit - time.monotonic()))
                    stdout, _ = process.communicate(timeout=wait)
                    break
                except subprocess.TimeoutExpired:
                    if time.monotonic() >= limit:
                        # sf did not answer in time. That says nothing about the saved login.
                        raise SFCredentialsError("salesforce_cli_timeout") from None
                    if progress is not None:
                        progress(time.monotonic() - started)
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate()
        try:
            if process.returncode != 0 or len(stdout) > _MAX_OUTPUT_BYTES:
                raise SFCredentialsError()
            document = strict_json.loads(stdout)
            if (not isinstance(document, dict) or type(document.get("status")) is not int
                    or document["status"] != 0 or not isinstance(document.get("result"), dict)):
                raise SFCredentialsError()
            value = document["result"].get(field)
            if not isinstance(value, str) or not value:
                raise SFCredentialsError()
            return value
        except (ValueError, RecursionError):
            raise SFCredentialsError() from None

    def read(self):
        """Return origin and token in memory. sf is stopped if it outlasts its limit."""
        started = time.monotonic()
        deadline = started + CREDENTIAL_SECONDS
        origin = self._command(["display"], "instanceUrl", started, deadline)
        token = self._command(["auth", "show-access-token"], "accessToken", started, deadline)
        return origin, token
