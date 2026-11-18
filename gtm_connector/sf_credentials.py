"""Read an existing Salesforce CLI login without printing or storing secrets."""

import json
import os
import subprocess
import time


_COMMAND_SECONDS = 10
_MAX_OUTPUT_BYTES = 1024 * 1024


class SFCredentialsError(RuntimeError):
    """A fixed error that never includes child output, arguments, or credentials."""

    def __init__(self):
        super().__init__("salesforce_auth_required")


def _unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError
        result[key] = value
    return result


class SFCredentials:
    """Capture two explicit commands using the owner's existing CLI login."""

    def __init__(self, alias):
        if (not isinstance(alias, str) or not 1 <= len(alias) <= 255
                or alias.startswith("-") or any(ord(char) <= 32 or ord(char) == 127 for char in alias)):
            raise SFCredentialsError()
        self._alias = alias
        self.invocations = 0

    def _command(self, words, field, deadline):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise SFCredentialsError()
        environment = dict(os.environ)
        environment.update({"SF_DISABLE_TELEMETRY": "true", "SF_AUTOUPDATE_DISABLE": "true",
                            "SF_DISABLE_AUTOUPDATE": "true",
                            "SF_DISABLE_LOG_FILE": "true", "SFDX_DISABLE_LOG_FILE": "true"})
        self.invocations += 1
        try:
            result = subprocess.run(
                ["sf", "org", *words, "--target-org", self._alias, "--json"],
                stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                timeout=min(_COMMAND_SECONDS, remaining), env=environment, check=False)
            if (result.returncode != 0 or len(result.stdout) > _MAX_OUTPUT_BYTES
                    or time.monotonic() >= deadline):
                raise SFCredentialsError()
            document = json.loads(result.stdout, object_pairs_hook=_unique_object)
            if (not isinstance(document, dict) or type(document.get("status")) is not int
                    or document["status"] != 0 or not isinstance(document.get("result"), dict)):
                raise SFCredentialsError()
            value = document["result"].get(field)
            if not isinstance(value, str) or not value:
                raise SFCredentialsError()
            return value
        except (OSError, subprocess.SubprocessError, ValueError, RecursionError):
            raise SFCredentialsError() from None

    def read(self, deadline):
        """Return origin and token in memory within the caller's original deadline."""
        origin = self._command(["display"], "instanceUrl", deadline)
        token = self._command(["auth", "show-access-token"], "accessToken", deadline)
        return origin, token
