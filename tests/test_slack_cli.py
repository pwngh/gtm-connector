"""Slack owner commands keep authentication separate and never print provider secrets."""

from contextlib import redirect_stderr, redirect_stdout
from copy import deepcopy
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import gtm_connector
from gtm_connector.cli import main, SLACK_FAILURES


CLIENT = '123456789.987654321'
REDIRECT = 'http://localhost:8765/slack/callback'
PRIVATE = 'invented-private-token-code-team-and-user'
READY = {
    'schema_version': 1, 'configured': True, 'credential_state': 'ready', 'verification': 'live',
    'verified_component': 'authentication', 'requested_scopes': ['channels:history'],
    'granted_scopes': ['channels:history'], 'rotation': True,
    'access_expires_at': '2027-01-15T20:00:00Z', 'last_verified_at': '2027-01-15T08:00:00Z', 'needs_login': False,
}
DISCONNECTED = {
    'schema_version': 1, 'status': 'disconnected', 'local_credentials_removed': True,
    'remote_revoke_requested': False, 'remote_access_token_revoked': False, 'revocation_error': None,
}


class SlackCLITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.directory = self.root / 'private Slack connection'

    def invoke(self, *arguments, as_json=True, directory=True):
        args = list(arguments)
        if directory:
            args += ['--connection-dir', str(self.directory)]
        if as_json:
            args += ['--json']
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(args)
        return code, out.getvalue(), err.getvalue()

    def connect(self, *extra, **kwargs):
        return self.invoke('slack', 'connect', '--client-id', CLIENT, '--redirect-uri', REDIRECT, *extra, **kwargs)

    def failure(self, result, expected):
        code, out, err = result
        self.assertEqual(code, expected, err)
        self.assertEqual(out, '')
        detail = json.loads(err.splitlines()[-1])['error']
        self.assertEqual(set(detail), {'code', 'message', 'next'})
        self.assertNotIn(PRIVATE, err)
        self.assertNotIn(CLIENT, err)
        self.assertNotIn(REDIRECT, err)
        self.assertIn('gtm-connector slack', detail['next'])
        return detail

    def test_action_only_options_are_rejected_without_importing_provider(self):
        import sys
        class Guard:
            def find_spec(self, fullname, path=None, target=None):
                if fullname == 'gtm_connector.slack_auth':
                    raise AssertionError('Provider must not load for invalid flags')
        cases = [[], ['unknown-' + PRIVATE], ['status', '--client-id', PRIVATE],
                 ['status', '--revoke'], ['disconnect', '--check'], ['disconnect', '--team-id', PRIVATE],
                 ['connect', '--client-id', CLIENT],
                 ['connect', '--client-id', CLIENT, '--redirect-uri', REDIRECT, '--user-scope', PRIVATE],
                 ['connect', '--client-id', CLIENT, '--redirect-uri', REDIRECT, '--client-secret', PRIVATE],
                 ['status', '--state-dir', PRIVATE]]
        guard = Guard()
        with patch.object(sys, 'meta_path', [guard, *sys.meta_path]):
            for arguments in cases:
                with self.subTest(arguments=arguments):
                    self.failure(self.invoke('slack', *arguments), 2)
                    self.assertFalse(self.directory.exists())

    def test_global_parent_and_action_json_and_directory_positions(self):
        for args in (['--json', 'slack', '--connection-dir', str(self.directory), 'status'],
                     ['slack', '--json', '--connection-dir', str(self.directory), 'status'],
                     ['slack', 'status', '--connection-dir', str(self.directory), '--json']):
            with self.subTest(args=args), patch('gtm_connector.slack_auth.slack_status', return_value=READY) as status:
                code, out, err = self.invoke(*args, directory=False, as_json=False)
            self.assertEqual(code, 0, err)
            self.assertEqual(json.loads(out), READY)
            self.assertEqual(err, '')
            self.assertEqual(status.call_args.args, (self.directory,))
            self.assertFalse(status.call_args.kwargs['check'])

    def test_connect_scope_default_and_explicit_private_selection_remain_distinct(self):
        for arguments, expected in (([], ('channels:history',)),
                                    (['--user-scope', 'groups:history'], ('groups:history',)),
                                    (['--user-scope', 'channels:history', '--user-scope', 'groups:history'],
                                     ('channels:history', 'groups:history'))):
            with self.subTest(arguments=arguments), \
                    patch('gtm_connector.slack_auth.connect_slack', return_value=READY) as connect:
                code, out, err = self.connect(*arguments, '--team-id', 'T12345678')
            self.assertEqual(code, 0, err)
            self.assertEqual(json.loads(out), READY)
            self.assertEqual(connect.call_args.args, (self.directory,))
            self.assertEqual(connect.call_args.kwargs['user_scopes'], expected)
            self.assertEqual(connect.call_args.kwargs['client_id'], CLIENT)
            self.assertEqual(connect.call_args.kwargs['redirect_uri'], REDIRECT)
            self.assertEqual(connect.call_args.kwargs['expected_team_id'], 'T12345678')
            self.assertNotIn('T12345678', out + err)

    def test_invalid_configuration_is_rejected_before_any_private_state(self):
        cases = [('--client-id', PRIVATE + '\n'), ('--redirect-uri', 'https://example.invalid/' + PRIVATE),
                 ('--redirect-uri', REDIRECT + '?private=' + PRIVATE),
                 ('--user-scope', 'channels:history', '--user-scope', 'channels:history'),
                 ('--team-id', PRIVATE)]
        for arguments in cases:
            with self.subTest(arguments=arguments):
                error = self.failure(self.connect(*arguments), 2)
                self.assertIn('slack_invalid_configuration', error['message'])
                self.assertFalse(self.directory.exists())

    def test_progress_is_bounded_stderr_data_and_ignores_private_unknown_events(self):
        def connected(*args, progress, **kwargs):
            progress('browser', 'waiting')
            progress(PRIVATE, 'running')
            progress([], PRIVATE)
            progress('identity', 'passed')
            progress('save', 'passed')
            return READY
        with patch('gtm_connector.slack_auth.connect_slack', side_effect=connected):
            code, out, err = self.connect()
        self.assertEqual(code, 0, err)
        self.assertEqual(json.loads(out), READY)
        events = [json.loads(line)['progress'] for line in err.splitlines()]
        self.assertEqual([event['step'] for event in events], ['browser', 'identity', 'save'])
        self.assertTrue(all(event['command'] == 'slack connect' for event in events))
        self.assertNotIn(PRIVATE, out + err)
        with patch('gtm_connector.slack_auth.connect_slack', side_effect=connected):
            code, out, err = self.connect(as_json=False)
        self.assertEqual(code, 0, err)
        self.assertIn('Waiting for Slack browser approval', err)
        self.assertIn('No messages were read.', out)
        self.assertNotIn(PRIVATE, out + err)

    def test_every_provider_error_has_fixed_safe_recovery_and_expected_exit(self):
        from gtm_connector.slack_auth import SLACK_AUTH_ERROR_CODES, SlackAuthError
        self.assertEqual(set(SLACK_FAILURES), set(SLACK_AUTH_ERROR_CODES))
        for name in SLACK_AUTH_ERROR_CODES:
            error = SlackAuthError(name)
            with self.subTest(name=name), patch('gtm_connector.slack_auth.slack_status', side_effect=error):
                detail = self.failure(self.invoke('slack', 'status', '--check'), error.exit_code)
            self.assertIn(name, detail['message'])
            self.assertFalse(self.directory.exists())

    def test_unknown_provider_exceptions_never_echo_their_values(self):
        for error in (OSError(PRIVATE), ValueError(PRIVATE), RuntimeError(PRIVATE)):
            with self.subTest(error=type(error).__name__), \
                    patch('gtm_connector.slack_auth.slack_status', side_effect=error):
                detail = self.failure(self.invoke('slack', 'status', '--check'), 1)
            self.assertIn('slack_storage_failed', detail['message'])

    def test_progress_can_precede_a_safe_final_error(self):
        from gtm_connector.slack_auth import SlackAuthError
        def failed(*args, progress, **kwargs):
            progress('refresh', 'running')
            raise SlackAuthError('slack_reconnect_required')
        with patch('gtm_connector.slack_auth.slack_status', side_effect=failed):
            result = self.invoke('slack', 'status', '--check')
        self.failure(result, 3)
        self.assertEqual(json.loads(result[2].splitlines()[0])['progress']['step'], 'refresh')

    def test_interrupt_uses_local_status_and_keeps_provider_details_private(self):
        with patch('gtm_connector.slack_auth.connect_slack', side_effect=KeyboardInterrupt):
            detail = self.failure(self.connect(), 130)
        self.assertEqual(detail['code'], 'interrupted')
        self.assertIn('slack status --connection-dir', detail['next'])
        self.assertNotIn('audit', detail['next'])
        self.assertNotIn('--check', detail['next'])
        self.assertFalse(self.directory.exists())

    def test_local_missing_status_and_disconnect_never_create_state(self):
        for action in ('status', 'disconnect', 'disconnect'):
            code, out, err = self.invoke('slack', action)
            self.assertEqual(code, 0, err)
            self.assertEqual(err, '')
            result = json.loads(out)
            if action == 'status':
                self.assertFalse(result['configured'])
                self.assertEqual(result['credential_state'], 'missing')
                self.assertEqual(result['verification'], 'saved')
                self.assertIsNone(result['rotation'])
            else:
                self.assertFalse(result['local_credentials_removed'])
                self.assertFalse(result['remote_revoke_requested'])
            self.assertFalse(self.directory.exists())

    def test_local_status_and_failures_never_import_source_runtime_or_http(self):
        program = r'''
import sys
sys.path.insert(0, sys.argv.pop(1))
class Guard:
    def find_spec(self, fullname, path=None, target=None):
        if fullname.split('.')[0] in {'sqlite3','_sqlite3','mcp','mcp_types','httpx','httpx2','httpcore','httpcore2','uvicorn'} or fullname in {'gtm_connector.runtime','gtm_connector.units','gtm_connector.gateway'}:
            raise AssertionError('Source runtime must not load')
sys.meta_path.insert(0,Guard())
from gtm_connector.cli import main
raise SystemExit(main(sys.argv[1:]))
'''
        cases = [(['slack', 'status'], 0), (['slack', 'disconnect'], 0),
                 (['slack', 'status', '--check'], 3),
                 (['slack', 'connect', '--client-id', CLIENT, '--redirect-uri', REDIRECT + '?private=' + PRIVATE], 2)]
        for args, expected in cases:
            package_root = str(Path(gtm_connector.__file__).resolve().parent.parent)
            result = subprocess.run([sys.executable, '-c', program, package_root, *args,
                                     '--connection-dir', str(self.directory), '--json'], cwd=self.root,
                                    capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, expected, result.stderr)
            self.assertNotIn('Source runtime must not load', result.stdout + result.stderr)
            self.assertNotIn(PRIVATE, result.stdout + result.stderr)
            self.assertFalse(self.directory.exists())

    def test_local_status_and_disconnect_preserve_unrelated_files(self):
        self.directory.mkdir(mode=0o700)
        unrelated = self.directory / 'unrelated.txt'
        unrelated.write_text(PRIVATE)
        unrelated.chmod(0o600)
        for action in ('status', 'disconnect'):
            self.failure(self.invoke('slack', action), 3)
            self.assertEqual(unrelated.read_text(), PRIVATE)

    def test_status_check_and_revoke_flags_reach_only_their_operation(self):
        with patch('gtm_connector.slack_auth.slack_status', return_value=READY) as status:
            code, out, err = self.invoke('slack', 'status', '--check')
        self.assertEqual(code, 0, err)
        self.assertTrue(status.call_args.kwargs['check'])
        confirmed = {**DISCONNECTED, 'remote_revoke_requested': True, 'remote_access_token_revoked': True}
        with patch('gtm_connector.slack_auth.disconnect_slack', return_value=confirmed) as disconnect:
            code, out, err = self.invoke('slack', 'disconnect', '--revoke')
        self.assertEqual(code, 0, err)
        self.assertTrue(disconnect.call_args.kwargs['revoke'])
        self.assertEqual(json.loads(out), confirmed)

    def test_unconfirmed_revoke_keeps_local_outcome_and_exits_five(self):
        unconfirmed = {**DISCONNECTED, 'remote_revoke_requested': True, 'revocation_error': 'slack_unavailable'}
        with patch('gtm_connector.slack_auth.disconnect_slack', return_value=unconfirmed):
            code, out, err = self.invoke('slack', 'disconnect', '--revoke')
        self.assertEqual(code, 5)
        self.assertEqual(json.loads(out), unconfirmed)
        detail = json.loads(err)['error']
        self.assertEqual(detail['code'], 'upstream_failure')
        self.assertIn('unconfirmed', detail['message'])
        self.assertIn('Slack settings', detail['message'])
        self.assertEqual(detail['next'], 'gtm-connector slack disconnect --help')

    def test_human_saved_and_unfinished_status_never_claim_live_source_access(self):
        for state in ('ready', 'identity_unchecked', 'refresh_pending'):
            saved = {**deepcopy(READY), 'verification': 'saved', 'credential_state': state,
                     'needs_login': state == 'refresh_pending'}
            with self.subTest(state=state), patch('gtm_connector.slack_auth.slack_status', return_value=saved), \
                    patch.dict(os.environ, {'NO_COLOR': '1'}):
                code, out, err = self.invoke('slack', 'status', as_json=False)
            self.assertEqual(code, 0, err)
            self.assertEqual(err, '')
            self.assertNotIn('\x1b', out)
            self.assertNotIn('Slack authentication is verified.', out)
            self.assertIn('Next: gtm-connector slack', out)
            if state == 'identity_unchecked':
                self.assertIn('--check', out)
            elif state == 'refresh_pending':
                self.assertIn('connect --help', out)
            else:
                self.assertIn('did not contact Slack', out)

    def test_unchecked_credentials_needing_login_do_not_recommend_another_check(self):
        saved = {**READY, 'verification': 'saved', 'credential_state': 'identity_unchecked', 'needs_login': True}
        with patch('gtm_connector.slack_auth.slack_status', return_value=saved):
            code, out, err = self.invoke('slack', 'status', as_json=False)
        self.assertEqual(code, 0, err)
        self.assertEqual(err, '')
        self.assertIn('Next: gtm-connector slack connect --help', out)
        self.assertNotIn('--check', out)
        self.assertIn('Browser authorization is required.', out)

    def test_human_disconnect_distinguishes_local_and_remote_results(self):
        for requested, revoked in ((False, False), (True, True), (True, False)):
            result = {**DISCONNECTED, 'remote_revoke_requested': requested, 'remote_access_token_revoked': revoked,
                      'revocation_error': 'slack_unavailable' if requested and not revoked else None}
            with self.subTest(requested=requested, revoked=revoked), \
                    patch('gtm_connector.slack_auth.disconnect_slack', return_value=result):
                code, out, err = self.invoke('slack', 'disconnect', *(['--revoke'] if requested else []), as_json=False)
            self.assertEqual(code, 5 if requested and not revoked else 0, err)
            self.assertIn('Local Slack credentials were removed.', out)
            if not requested:
                self.assertIn('Remote access was not revoked.', out)
            elif revoked:
                self.assertIn('this access token', out)
                self.assertIn('managed separately', out)
            else:
                self.assertIn('unconfirmed', out)


if __name__ == '__main__':
    unittest.main()
