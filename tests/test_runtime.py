"""Use real loopback TLS sockets and the CLI OAuth client, without paid services."""

from contextlib import closing
import json
from pathlib import Path
import sqlite3
import stat
import tempfile
import unittest

import httpx2

from gtm_connector.audit import AuditLog
from gtm_connector.client import LocalClient
from gtm_connector.runtime import Runtime


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "state"

    def database_rows(self, name, table):
        # The table names below are fixed test constants, never caller input.
        with closing(sqlite3.connect(self.state / f"{name}.sqlite3")) as connection:
            connection.row_factory = sqlite3.Row
            return [dict(row) for row in connection.execute(f"SELECT * FROM {table} ORDER BY id")]

    def source_snapshot(self):
        return {
            **{table: self.database_rows("crm", table)
               for table in ("accounts", "contacts", "opportunities", "activities")},
            "call_notes": self.database_rows("calls", "call_notes"),
        }

    def records(self, response):
        self.assertEqual(response.status_code, 200, response.text)
        result = response.json()["result"]
        self.assertFalse(result.get("isError"), response.text)
        self.assertTrue(result["structuredContent"]["untrusted_data"])
        return result["structuredContent"]["records"]

    def test_oauth_scopes_and_idempotent_write_over_real_https(self):
        brief = "brief-live-https-fixture"
        with Runtime(self.state) as runtime, LocalClient(runtime.config) as client:
            crm_token = client.authorize(["crm:read"])
            account = self.records(client.call(crm_token, "get_account", {"account_id": "acc-007"}, brief))
            self.assertEqual({row["record_ref"] for row in account}, {"crm/accounts/acc-007", "crm/contacts/con-007"})
            denied = client.call(crm_token, "read_call_notes", {"account_id": "acc-007"}, brief)
            self.assertEqual(denied.status_code, 403, denied.text)
            self.assertIn('scope="calls:read"', denied.headers["WWW-Authenticate"])
            self.assertNotIn("IGNORE PREVIOUS INSTRUCTIONS", denied.text)

            calls_token = client.authorize(["calls:read"])
            notes = self.records(client.call(calls_token, "read_call_notes", {"account_id": "acc-007"}, brief))
            self.assertIn("IGNORE PREVIOUS INSTRUCTIONS", notes[0]["body"])
            self.assertEqual(self.database_rows("crm", "activities"), [])

            write_token = client.authorize(["crm:write"])
            arguments = {"account_id": "acc-007", "kind": "renewal_call",
                         "body": "Fixture owner explicitly requested this activity.",
                         "idempotency_key": "live-https-write-001"}
            first = self.records(client.call(write_token, "log_activity", arguments, brief))
            second = self.records(client.call(write_token, "log_activity", arguments, brief))
            self.assertEqual(first, second)
            self.assertEqual(len(self.database_rows("crm", "activities")), 1)
            rows = runtime.connector.audit.read(brief)
            self.assertEqual([row["http_status"] for row in rows], [200, 403, 200, 200, 200])
            self.assertEqual(rows[1]["rows"], [])
            self.assertEqual(rows[3]["rows"], rows[4]["rows"])
            for token in (crm_token, calls_token, write_token):
                self.assertNotIn(token, json.dumps(rows))
        # Audit evidence survives service shutdown rather than living in memory.
        self.assertEqual(len(AuditLog(self.state / "audit.sqlite3").read(brief)), 5)

    def test_https_certificate_must_be_explicitly_trusted(self):
        with Runtime(self.state) as runtime:
            with httpx2.Client(trust_env=False, timeout=5) as untrusted:
                with self.assertRaises(httpx2.ConnectError) as failure:
                    untrusted.get(runtime.config["issuer"] + "/.well-known/oauth-authorization-server")
            self.assertIn("certificate", str(failure.exception).lower())
            # The same endpoint works with the generated certificate pinned by LocalClient.
            with LocalClient(runtime.config) as trusted:
                token = trusted.authorize(["crm:read"])
                response = trusted.rpc(token, "server/discover")
                self.assertEqual(response.status_code, 200, response.text)

    def test_private_state_lock_and_restart_preserve_source_records(self):
        with Runtime(self.state) as first:
            self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o700)
            for name in ("server.lock", "owner.json", "tls.key", "tls.pem",
                         "crm.sqlite3", "calls.sqlite3", "audit.sqlite3"):
                with self.subTest(file=name):
                    self.assertEqual(stat.S_IMODE((self.state / name).stat().st_mode), 0o600)
            owner = json.loads((self.state / "owner.json").read_text())
            self.assertEqual(owner, first.config)
            with self.assertRaisesRegex(ValueError, "already has a running connector"):
                with Runtime(self.state):
                    self.fail("A second runtime acquired the same state directory.")
            with LocalClient(first.config) as client:
                token = client.authorize(["crm:read"])
                self.records(client.call(token, "get_account", {"account_id": "acc-001"}))
            before = self.source_snapshot()
        with Runtime(self.state):
            self.assertEqual(self.source_snapshot(), before)
        self.assertEqual(len(before["accounts"]), 30)
        self.assertEqual(len(before["call_notes"]), 29)


if __name__ == "__main__":
    unittest.main()
