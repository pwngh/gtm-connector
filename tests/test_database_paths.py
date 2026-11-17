"""Source separation checks physical files before any source is seeded."""

from pathlib import Path
import tempfile
import unittest

from gtm_connector.auth import LocalAuthority
from gtm_connector.gateway import Connector
from gtm_connector.runtime import Runtime


class DatabasePathTests(unittest.TestCase):
    def test_runtime_rejects_empty_slack_audit_hardlink_before_seeding(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            slack = root / "slack.sqlite3"
            slack.touch()
            (root / "audit.sqlite3").hardlink_to(slack)
            with self.assertRaisesRegex(ValueError, "different database files"):
                with Runtime(root):
                    self.fail("aliased stores started")
            self.assertEqual(slack.read_bytes(), b"")
            self.assertFalse((root / "crm.sqlite3").exists())
            self.assertFalse((root / "calls.sqlite3").exists())

    def test_standalone_connector_rejects_existing_hardlink_without_initializing_audit(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            slack = root / "slack.sqlite3"
            slack.touch()
            audit = root / "audit.sqlite3"
            audit.hardlink_to(slack)
            authority = LocalAuthority("https://127.0.0.1:9001", "https://127.0.0.1:9002/mcp")
            with self.assertRaisesRegex(ValueError, "different database files"):
                Connector(root / "crm.sqlite3", root / "calls.sqlite3", audit, authority)
            self.assertEqual(audit.read_bytes(), b"")


if __name__ == "__main__":
    unittest.main()
