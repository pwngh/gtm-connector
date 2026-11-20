"""profile.json says which sources a state folder is connected to, and nothing else does."""

import json
import os
from pathlib import Path
import tempfile
import unittest

from gtm_connector import profile

SALESFORCE = {"provider": "salesforce", "org": "invented-org", "environment": "developer"}


class ProfileTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.state = Path(directory.name) / "state"

    def test_a_folder_starts_with_nothing_connected_and_connect_changes_one_source(self):
        self.assertEqual(profile.load(self.state), {})
        with self.assertRaisesRegex(profile.NotConnected, "connect salesforce --org ALIAS"):
            profile.backends(profile.load(self.state))
        self.assertEqual(profile.connect(self.state, "crm", SALESFORCE), {"crm": SALESFORCE})
        self.assertEqual((self.state / profile.NAME).stat().st_mode & 0o777, 0o600)
        # Beside a real CRM a source is real or not connected. It is never demo data.
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "salesforce", "calls_backend": "none", "slack_backend": "none"})
        profile.connect(self.state, "calls", {"provider": "imported"})
        profile.connect(self.state, "slack", {"provider": "slack"})
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "salesforce", "calls_backend": "imported", "slack_backend": "slack"})
        self.assertEqual(profile.disconnect(self.state, "calls"), {"crm": SALESFORCE, "slack": {"provider": "slack"}})
        # Removing the CRM removes what belonged to it.
        self.assertEqual(profile.disconnect(self.state, "crm"), {})
        self.assertEqual(profile.load(self.state), {})

    def test_demo_data_never_sits_beside_salesforce_and_calls_or_slack_need_a_crm(self):
        demo = {kind: {"provider": "demo"} for kind in profile.KINDS}
        self.assertEqual(profile.DEMO, demo)
        profile.save(self.state, demo)
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "demo", "calls_backend": "fixture", "slack_backend": "fixture"})
        for sources in ({"crm": SALESFORCE, "calls": {"provider": "demo"}},
                        {"crm": SALESFORCE, "slack": {"provider": "demo"}},
                        {"crm": SALESFORCE, "calls": {"provider": "slack"}},
                        # Beside the demo CRM a source is demo data or connected. It is never missing.
                        {"crm": {"provider": "demo"}}, {"crm": {"provider": "demo"}, "slack": {"provider": "slack"}},
                        {"calls": {"provider": "imported"}}, {"slack": {"provider": "slack"}},
                        {"calls": {"provider": "demo"}, "slack": {"provider": "demo"}},
                        {"crm": {**SALESFORCE, "org": "bad alias"}},
                        {"crm": {**SALESFORCE, "environment": "moon"}},
                        {"crm": {**SALESFORCE, "token": "never"}},
                        {"mail": {"provider": "demo"}}):
            with self.subTest(sources=sources), self.assertRaises(profile.ProfileError):
                profile.save(self.state, sources)
        self.assertEqual(profile.load(self.state), demo)

    def test_real_calls_and_slack_can_sit_beside_the_demo_crm_and_removing_one_returns_it_to_demo(self):
        profile.connect(self.state, "crm", {"provider": "demo"})
        self.assertEqual(profile.connect(self.state, "calls", {"provider": "imported"}),
                         {**profile.DEMO, "calls": {"provider": "imported"}})
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "demo", "calls_backend": "imported", "slack_backend": "fixture"})
        profile.connect(self.state, "slack", {"provider": "slack"})
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "demo", "calls_backend": "imported", "slack_backend": "slack"})
        self.assertEqual(profile.disconnect(self.state, "calls"), {**profile.DEMO, "slack": {"provider": "slack"}})
        self.assertEqual(profile.backends(profile.load(self.state)),
                         {"crm": "demo", "calls_backend": "fixture", "slack_backend": "slack"})

    def test_another_crm_starts_the_profile_over_and_the_same_crm_keeps_its_sources(self):
        with self.assertRaises(profile.NotConnected):
            profile.connect(self.state, "calls", {"provider": "imported"})
        self.assertFalse(self.state.exists())
        profile.connect(self.state, "crm", {"provider": "demo"})
        profile.connect(self.state, "calls", {"provider": "imported"})
        # Salesforce over demo data keeps nothing of it: beside a real CRM nothing is demo.
        self.assertEqual(profile.connect(self.state, "crm", SALESFORCE), {"crm": SALESFORCE})
        profile.connect(self.state, "calls", {"provider": "imported"})
        sandbox = {**SALESFORCE, "environment": "sandbox"}
        self.assertEqual(profile.connect(self.state, "crm", sandbox),
                         {"crm": sandbox, "calls": {"provider": "imported"}})
        self.assertEqual(profile.connect(self.state, "crm", {"provider": "demo"}), profile.DEMO)

    def test_a_profile_that_is_not_private_or_not_this_versions_is_refused(self):
        profile.save(self.state, {"crm": SALESFORCE})
        path = self.state / profile.NAME
        written = path.read_bytes()
        for content in (b"not json", json.dumps({"schema_version": 2, "sources": {}}).encode(),
                        json.dumps({"schema_version": 1, "sources": {"crm": {"provider": "demo"},
                                                                     "calls": {"provider": "imported"}}}).encode(),
                        b" " * 5000):
            path.write_bytes(content)
            with self.subTest(content=content[:20]), self.assertRaisesRegex(profile.ProfileError, "not a profile"):
                profile.load(self.state)
        path.write_bytes(written)
        os.chmod(path, 0o644)
        with self.assertRaisesRegex(profile.ProfileError, "private regular file"):
            profile.load(self.state)
        path.unlink()
        path.symlink_to(self.state / "elsewhere.json")
        with self.assertRaisesRegex(profile.ProfileError, "not a link"):
            profile.load(self.state)
        path.unlink()
        path.write_bytes(written)
        os.chmod(path, 0o600)
        os.link(path, self.state / "second-name.json")
        with self.assertRaisesRegex(profile.ProfileError, "no other link"):
            profile.load(self.state)
        (self.state / "second-name.json").unlink()
        # A state folder that is a link is refused too.
        linked = self.state.parent / "linked-state"
        linked.symlink_to(self.state, target_is_directory=True)
        with self.assertRaisesRegex(profile.ProfileError, "symbolic link"):
            profile.load(linked)
        for version in (b"true", b"1.0", b"1e0"):
            path.write_bytes(written.replace(b'"schema_version": 1', b'"schema_version": ' + version))
            with self.subTest(version=version), self.assertRaisesRegex(profile.ProfileError, "not a profile"):
                profile.load(self.state)
