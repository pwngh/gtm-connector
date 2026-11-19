"""Owner transcript commands use invented files, scoped local HTTPS, and fake Salesforce."""

from contextlib import redirect_stderr, redirect_stdout
import io
import json
import os
from pathlib import Path
from queue import SimpleQueue
import tempfile
import unittest
from unittest.mock import patch

import httpx2

from gtm_connector.audit import read_audit
from gtm_connector.cli import main
from gtm_connector.salesforce import SalesforceReadOnlyAdapter


ACCOUNT = 'acc-sf-001000000000001AAA'
ORIGIN = 'https://invented-calls.my.salesforce.com'
TOKEN = 'invented-private-call-token'
VTT = b'WEBVTT\r\n\r\ncue-one\r\n00:00.000 --> 00:03.000\r\n<v Avery>Confirm seats.\r\nKeep the context.\r\n\r\ncue-two\r\n00:03.000 --> 00:06.000\r\nThe date is still unknown.\r\n'


class CallsCLITests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.state = self.root / 'state'
        self.export = self.root / 'private-source-name.txt'
        self.export.write_text('\n'.join(f'Invented line {number}.' for number in range(1, 31)) + '\n')
        self.env = {key: value for key, value in os.environ.items() if not key.startswith('SALESFORCE_')}
        self.requests = []

    def invoke(self, *arguments, json_output=True):
        args = [*arguments, '--state-dir', str(self.state)]
        if json_output:
            args.append('--json')
        with patch.dict(os.environ, self.env, clear=True), redirect_stdout(io.StringIO()) as out, \
                redirect_stderr(io.StringIO()) as err:
            code = main(args)
        return code, out.getvalue(), err.getvalue()

    def import_file(self, *options, account='acc-003', json_output=True):
        return self.invoke('calls', 'import', str(self.export), '--account', account, *options,
                           json_output=json_output)

    def success(self, result):
        code, out, err = result
        self.assertEqual(code, 0, err)
        for line in err.splitlines():
            self.assertEqual(json.loads(line)['progress']['command'], 'calls import')
        return json.loads(out)

    def error(self, result, code):
        actual, out, err = result
        self.assertEqual(actual, code, err)
        self.assertEqual(out, '')
        error = json.loads(err.splitlines()[-1])['error']
        self.assertIn('gtm-connector', error['next'])
        self.assertNotIn(str(self.export), err)
        self.assertNotIn(TOKEN, err)
        self.assertNotIn(ORIGIN, err)
        return error

    def reader(self, *, duplicate=False, missing=False, organization='00D000000000001AAA'):
        def respond(request):
            self.requests.append(request)
            self.assertEqual(request.method, 'GET')
            self.assertEqual(request.url.host, 'invented-calls.my.salesforce.com')
            query = request.url.params['q']
            if ' FROM Organization ' in query:
                rows = [{'Id': organization, 'OrganizationType': 'Developer Edition', 'IsSandbox': False}]
            elif ' FROM Account ' in query:
                rows = [] if missing else [{'Id': ACCOUNT[7:], 'Name': 'Invented imported account',
                                           'Industry': None, 'AccountNumber': 'INVENTED-001'}]
                if duplicate:
                    rows.append({**rows[0], 'Id': '001000000000002AAA'})
            elif ' FROM Contact ' in query or ' FROM Contract ' in query:
                rows = []
            else:
                self.fail('Unexpected invented Salesforce query')
            return httpx2.Response(200, json={'done': True, 'totalSize': len(rows), 'records': rows})
        return SalesforceReadOnlyAdapter(ORIGIN, TOKEN, transport=httpx2.MockTransport(respond))

    def test_local_import_has_audited_association_and_private_separate_store(self):
        result = self.success(self.import_file())
        self.assertEqual(result['status'], 'imported')
        self.assertEqual(result['source'], 'imported_transcript')
        self.assertEqual(result['account_id'], 'acc-003')
        self.assertIsNone(result['called_at'])
        self.assertEqual(result['cited_range'], {'first': 1, 'last': 21})
        self.assertNotIn('body', result)
        self.assertNotIn('original_text', result)
        receipt, = read_audit(self.state / 'audit.sqlite3')
        self.assertEqual((receipt['tool'], receipt['status']), ('get_account', 'ok'))
        self.assertEqual(receipt['audit_id'], result['resolution_receipt_id'])
        self.assertFalse((self.state / 'calls.sqlite3').exists())
        self.assertEqual((self.state / 'imports.sqlite3').stat().st_mode & 0o777, 0o600)
        self.assertTrue((self.state / 'calls-source.json').is_file())

    def test_repeat_import_preserves_revision_and_date_but_audits_new_resolution(self):
        first = self.success(self.import_file('--called-at', '2027-01-15T09:30:00-06:00'))
        second = self.success(self.import_file('--called-at', '2027-01-15T15:30:00Z'))
        self.assertEqual(first['called_at'], '2027-01-15T15:30:00Z')
        self.assertEqual((second['import_id'], second['imported_at']), (first['import_id'], first['imported_at']))
        self.assertFalse(second['created'])
        self.assertTrue(second['selected'])
        self.assertNotEqual(first['resolution_receipt_id'], second['resolution_receipt_id'])
        self.assertEqual(len(read_audit(self.state / 'audit.sqlite3')), 2)

    def test_inspection_hides_unselected_original_until_full_is_explicit(self):
        imported = self.success(self.import_file('--lines', '3:4'))
        inspected = self.success(self.invoke('calls', 'inspect', imported['import_id']))
        self.assertEqual([unit['index'] for unit in inspected['units']], [2, 3, 4, 5])
        self.assertNotIn('original_text', inspected)
        self.assertNotIn('Invented line 30.', json.dumps(inspected))
        full = self.success(self.invoke('calls', 'inspect', imported['import_id'], '--full'))
        self.assertEqual(full['original_text'], self.export.read_text())
        self.assertEqual(len(full['units']), 30)

    def test_list_and_inspect_are_offline_and_leave_state_bytes_unchanged(self):
        imported = self.success(self.import_file())
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        with patch.object(SalesforceReadOnlyAdapter, 'from_sf', side_effect=AssertionError('No credentials')), \
                patch.object(SalesforceReadOnlyAdapter, 'from_env', side_effect=AssertionError('No credentials')), \
                patch('gtm_connector.runtime.Runtime', side_effect=AssertionError('No server')):
            listed = self.success(self.invoke('calls', 'list', '--account', 'acc-003'))
            inspected = self.success(self.invoke('calls', 'inspect', imported['import_id']))
        self.assertEqual(listed['imports'][0]['import_id'], imported['import_id'])
        self.assertTrue(listed['imports'][0]['active'])
        self.assertIsNone(listed['next_after'])
        self.assertEqual(inspected['import']['import_id'], imported['import_id'])
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()})

    def test_common_options_work_before_or_after_the_action(self):
        imported = self.success(self.import_file())
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(['calls', '--json', '--state-dir', str(self.state), 'list'])
        self.assertEqual(code, 0, err.getvalue())
        self.assertEqual(json.loads(out.getvalue())['imports'][0]['import_id'], imported['import_id'])
        self.assertEqual(err.getvalue(), '')

    def test_import_defaults_to_separate_import_state(self):
        previous = Path.cwd()
        try:
            os.chdir(self.root)
            with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
                code = main(['calls', 'import', str(self.export), '--account', 'acc-003', '--json'])
        finally:
            os.chdir(previous)
        self.assertEqual(code, 0, err.getvalue())
        self.assertEqual(json.loads(out.getvalue())['account_id'], 'acc-003')
        self.assertTrue((self.root / 'var/imported-calls/imports.sqlite3').is_file())
        self.assertFalse((self.root / 'var/v2').exists())

    def test_list_paginates_metadata_without_transcript_text(self):
        from gtm_connector.call_workflow import resolve_call_account
        from gtm_connector.calls_import import parse_transcript, select_excerpt, store_import
        from gtm_connector.runtime import Runtime
        with Runtime(self.state, calls_backend='imported') as runtime:
            association = resolve_call_account(runtime, account_id='acc-003')
            for number in range(101):
                parsed = parse_transcript(f'Private invented revision {number}.'.encode(), format='txt')
                store_import(runtime.imports_path, parsed, select_excerpt(parsed), association=association,
                             called_at=None, imported_at='2027-01-15T12:00:00Z')
        first = self.success(self.invoke('calls', 'list'))
        self.assertEqual(len(first['imports']), 100)
        self.assertIsNotNone(first['next_after'])
        second = self.success(self.invoke('calls', 'list', '--after', first['next_after']))
        self.assertEqual(len(second['imports']), 1)
        self.assertIsNone(second['next_after'])
        self.assertEqual(len({item['import_id'] for item in first['imports'] + second['imports']}), 101)
        self.assertNotIn('Private invented revision', json.dumps([first, second]))

    def test_reassignment_is_explicit_and_does_not_mutate_history(self):
        first = self.success(self.import_file())
        self.error(self.import_file(account='acc-002'), 3)
        second = self.success(self.import_file('--reassign', account='acc-002'))
        self.assertTrue(second['reassigned'])
        self.assertNotEqual(second['import_id'], first['import_id'])
        old = self.success(self.invoke('calls', 'inspect', first['import_id']))
        self.assertEqual(old['import']['account_id'], 'acc-003')
        self.assertFalse(old['import']['active'])

    def test_vtt_and_human_full_output_escape_source_controls(self):
        self.export = self.root / 'invented.vtt'
        self.export.write_bytes(VTT.replace(b'Confirm seats.', 'Confirm seats.\u202e'.encode()))
        imported = self.success(self.import_file('--cues', '1:1'))
        code, out, err = self.invoke('calls', 'inspect', imported['import_id'], '--full', json_output=False)
        self.assertEqual(code, 0, err)
        self.assertIn('0 to 3000 ms', out)
        self.assertIn('Original export text (quoted)', out)
        self.assertIn('\\u202e', out)
        self.assertNotIn('\u202e', out)
        self.assertNotIn('\x1b', out)
        self.assertEqual(err.count('Source:'), 1)
        self.assertIn('unverified', err)

    def test_import_progress_precedes_runtime_and_account_resolution(self):
        from gtm_connector.runtime import Runtime
        original = Runtime.__enter__
        observed = []
        def enter(runtime):
            import sys
            observed.extend(json.loads(line)['progress']['step'] for line in sys.stderr.getvalue().splitlines())
            return original(runtime)
        with patch.object(Runtime, '__enter__', enter):
            result = self.import_file()
        self.success(result)
        self.assertEqual(observed, ['reading_export', 'preparing_state'])
        self.assertEqual([json.loads(line)['progress']['step'] for line in result[2].splitlines()],
                         ['reading_export', 'preparing_state', 'resolving_account', 'saving_import'])

    def test_human_import_labels_demo_crm_and_imported_calls_without_dumping_text(self):
        code, out, err = self.import_file(json_output=False)
        self.assertEqual(code, 0, err)
        self.assertEqual(err.count('Sources:'), 1)
        self.assertIn('CRM uses demo fixtures; calls use user-supplied transcript exports.', err)
        self.assertIn('Transcript provider identity and statements are unverified.', err)
        self.assertIn('Transcript imported.', out)
        self.assertNotIn('Invented line 1.', out + err)

    def test_terminal_list_stacks_full_identifiers_and_preserves_piped_tsv(self):
        from gtm_connector.cli import human_result
        self.success(self.import_file('--lines', '3:4'))
        listed = self.success(self.invoke('calls', 'list'))
        row = listed['imports'][0]
        with patch('sys.stdout.isatty', return_value=True), patch.dict(os.environ, {'NO_COLOR': '1'}):
            terminal = human_result('calls', listed)
        self.assertIn(row['import_id'], terminal.splitlines())
        self.assertLessEqual(max(map(len, terminal.splitlines())), 112)
        self.assertIn('Requested: lines 3:4', terminal)
        self.assertIn('Cited: lines 2:5', terminal)
        with patch('sys.stdout.isatty', return_value=False):
            piped = human_result('calls', listed)
        self.assertEqual(piped, '\t'.join([row['import_id'], row['account_id'], 'True', 'unknown',
                                           row['imported_at'], 'lines 2:5 (includes adjacent context)']))
        listed['next_after'] = row['import_id']
        with patch('sys.stdout.isatty', return_value=True), patch.dict(os.environ, {'NO_COLOR': '1'}):
            paginated = human_result('calls', listed, state_dir=Path('var/private-calls'))
        self.assertIn('gtm-connector calls list --after ' + row['import_id'] + ' \\', paginated.splitlines())
        self.assertIn('  --state-dir var/private-calls', paginated.splitlines())
        self.assertLessEqual(max(map(len, paginated.splitlines())), 112)

    def test_import_and_inspection_show_requested_and_cited_ranges(self):
        from gtm_connector.cli import human_result
        imported = self.success(self.import_file('--lines', '3:4'))
        inspected = self.success(self.invoke('calls', 'inspect', imported['import_id']))
        for result in (imported, inspected):
            with patch('sys.stdout.isatty', return_value=False):
                rendered = human_result('calls', result)
            self.assertIn('Requested excerpt\tlines 3:4', rendered)
            self.assertIn('Cited excerpt\tlines 2:5', rendered)
            self.assertIn('One adjacent unit on each available side is included in the cited range.', rendered)

    def test_speaker_names_are_read_only_when_declared_and_only_from_a_file_laid_out_that_way(self):
        # The fixture's lines open with no name, so the statement is refused before any state is opened.
        self.error(self.import_file('--speaker-names'), 2)
        self.assertFalse(self.state.exists())
        self.export.write_text('Jordan Lee: We will sign on Friday.\nMorgan Reed: Noted.\n')
        plain = self.success(self.import_file())
        named = self.success(self.import_file('--speaker-names'))
        self.assertEqual((plain['speaker_names'], named['speaker_names']), (False, True))
        from gtm_connector.cli import human_result
        with patch('sys.stdout.isatty', return_value=False):
            self.assertIn('Speaker names\tthe name that opens each line', human_result('calls', named))
            self.assertIn('Speaker names\tnot declared', human_result('calls', plain))
        self.assertNotEqual(plain['import_id'], named['import_id'])
        code, out, err = self.invoke('brief', 'acc-003', '--calls-backend', 'imported')
        self.assertEqual(code, 0, err)
        self.assertEqual([(unit['speaker'], unit['text']) for unit in json.loads(out)['units']
                          if unit['kind'] == 'sentence'],
                         [('Jordan Lee', 'We will sign on Friday.'), ('Morgan Reed', 'Noted.')])

    def test_imported_brief_banner_names_slack_only_when_requested(self):
        self.success(self.import_file())
        for include_slack in (False, True):
            code, out, err = self.invoke('brief', 'acc-003', '--calls-backend', 'imported',
                                        *(['--with-slack'] if include_slack else []), json_output=False)
            self.assertEqual(code, 0, err)
            self.assertIn('Brief:', out)
            self.assertEqual(err.count('Sources:'), 1)
            self.assertEqual('Slack' in err, include_slack)

    def test_salesforce_brief_banner_follows_requested_slack_for_each_calls_mode(self):
        artifact = {'brief_id': 'invented-brief', 'as_of': '2027-01-15', 'text': 'Invented checked text.'}
        for calls_backend in ('fixture', 'imported'):
            for include_slack in (False, True):
                with self.subTest(calls=calls_backend, slack=include_slack), \
                        patch.object(SalesforceReadOnlyAdapter, 'from_env', return_value=object()), \
                        patch('gtm_connector.runtime.Runtime'), \
                        patch('gtm_connector.briefing.create_brief', return_value=artifact) as create:
                    code, out, err = self.invoke('brief', ACCOUNT, '--crm-backend', 'salesforce',
                                                '--calls-backend', calls_backend,
                                                *(['--with-slack'] if include_slack else []), json_output=False)
                self.assertEqual(code, 0, err)
                self.assertEqual(err.count('Sources:'), 1)
                self.assertEqual('Slack' in err, include_slack)
                self.assertEqual(create.call_args.kwargs['include_slack'], include_slack)
        self.assertFalse(self.state.exists())

    def test_action_conflicts_fail_before_source_or_credentials_or_state(self):
        cases = [
            ['calls', 'import', str(self.export)],
            ['calls', 'import', str(self.export), '--account', 'acc-003', '--full'],
            ['calls', 'import', str(self.export), '--account', 'acc-003', '--after', 'note-' + 'a' * 64],
            ['calls', 'import', str(self.export), '--account', 'acc-003', '--sf-environment', 'developer'],
            ['calls', 'import', str(self.export), '--account', 'acc-003', '--crm-backend', 'salesforce'],
            ['calls', 'import', str(self.export), '--account-number', 'INVENTED-001'],
            ['calls', 'list', str(self.export)], ['calls', 'list', '--full'],
            ['calls', 'list', '--crm-backend', 'sqlite'], ['calls', 'list', '--sf-org', 'private-alias'],
            ['calls', 'inspect', 'note-' + 'a' * 64, '--account', 'acc-003'],
            ['calls', 'inspect', 'note-' + 'a' * 64, '--lines', '1:2'],
            ['calls', 'inspect'], ['calls', 'list', '--after', 'private-cursor'],
        ]
        for arguments in cases:
            with self.subTest(arguments=arguments), \
                    patch.object(Path, 'lstat', side_effect=AssertionError('Export must remain unopened')), \
                    patch.object(SalesforceReadOnlyAdapter, 'from_env') as factory:
                self.error(self.invoke(*arguments), 2)
                factory.assert_not_called()
            self.assertFalse(self.state.exists())

    def test_invalid_metadata_and_ranges_fail_before_source_access(self):
        cases = [('--called-at', '2027-01-15'), ('--called-at', '2027-01-15T10:00:00'),
                 ('--called-at', 'private-date-text'), ('--lines', '0:2'), ('--lines', '4:2'),
                 ('--lines', '1:private-range'), ('--cues', '1:2')]
        for options in cases:
            with self.subTest(options=options), patch.object(Path, 'lstat', side_effect=AssertionError('No source read')):
                self.error(self.import_file(*options), 2)
            self.assertFalse(self.state.exists())

    def test_account_number_controls_fail_before_credentials_runtime_or_resolution(self):
        for character in ('\x7f', '\x80', '\x85', '\x9f', '\ud800'):
            with self.subTest(character=repr(character)), \
                    patch.object(SalesforceReadOnlyAdapter, 'from_env',
                                 side_effect=AssertionError('No credential load')) as credentials, \
                    patch('gtm_connector.runtime.Runtime', side_effect=AssertionError('No state')) as runtime, \
                    patch('gtm_connector.call_workflow.resolve_call_account',
                          side_effect=AssertionError('No CRM resolution')) as resolve:
                error = self.error(self.invoke('calls', 'import', str(self.export), '--crm-backend', 'salesforce',
                                               '--account-number', 'INVENTED' + character + '001'), 2)
                self.assertIn('AccountNumber', error['message'])
                credentials.assert_not_called()
                runtime.assert_not_called()
                resolve.assert_not_called()
            self.assertFalse(self.state.exists())

    def test_blank_vtt_identifier_fails_before_runtime_or_resolution(self):
        self.export = self.root / 'blank-id.vtt'
        self.export.write_bytes('WEBVTT\n\n\u00a0\n00:00.000 --> 00:01.000\nInvented cue.\n'.encode())
        with patch('gtm_connector.runtime.Runtime', side_effect=AssertionError('No state')) as runtime, \
                patch('gtm_connector.call_workflow.resolve_call_account',
                      side_effect=AssertionError('No CRM resolution')) as resolve:
            error = self.error(self.import_file(), 2)
            self.assertIn('calls_import_invalid', error['message'])
            runtime.assert_not_called()
            resolve.assert_not_called()
        self.assertFalse(self.state.exists())

    def test_invalid_binary_timing_and_limits_fail_without_state(self):
        cases = [('bad.txt', b'private-text\x00'), ('bad.vtt', b'WEBVTT\n\n00:02.000 --> 00:01.000\nprivate\n'),
                 ('large.txt', b'x' * (2 * 1024 * 1024 + 1)), ('quote.txt', b'x' * (16 * 1024 + 1))]
        for filename, raw in cases:
            with self.subTest(filename=filename):
                self.export = self.root / filename
                self.export.write_bytes(raw)
                self.error(self.import_file(), 2)
                self.assertFalse(self.state.exists())

    def test_symlink_and_fifo_refuse_without_opening_them(self):
        target = self.export
        for kind in ('symlink', 'fifo'):
            path = self.root / (kind + '.txt')
            path.symlink_to(target) if kind == 'symlink' else os.mkfifo(path)
            self.export = path
            with patch('gtm_connector.cli.os.open', side_effect=AssertionError('Must not open nonregular input')):
                self.error(self.import_file(), 2)
            self.assertFalse(self.state.exists())

    def test_unreadable_file_error_is_fixed_without_raw_exception(self):
        with patch.object(Path, 'lstat', side_effect=PermissionError('private filesystem secret')):
            error = self.error(self.import_file(), 1)
        self.assertNotIn('private filesystem secret', error['message'])
        self.assertFalse(self.state.exists())

    def test_missing_offline_state_never_initializes_it(self):
        for action in [('list',), ('inspect', 'note-' + 'a' * 64)]:
            with self.subTest(action=action):
                error = self.error(self.invoke('calls', *action), 3)
                self.assertIn('existing import state folder', error['message'])
                self.assertNotIn('fresh', error['message'])
                self.assertNotIn('Repeat this command with --state-dir', error['message'])
                self.assertEqual(error['next'], 'gtm-connector calls import --help')
                self.assertFalse(self.state.exists())

    def test_imported_brief_on_fixture_state_recovers_to_existing_imports(self):
        from gtm_connector.runtime import Runtime
        with Runtime(self.state):
            pass
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        error = self.error(self.invoke('brief', 'acc-003', '--calls-backend', 'imported'), 3)
        self.assertIn('Imported transcripts cannot reuse fixture calls', error['message'])
        self.assertIn('existing import state folder', error['message'])
        self.assertNotIn('fresh', error['message'])
        self.assertNotIn('Repeat this command with --state-dir', error['message'])
        self.assertEqual(error['next'], 'gtm-connector calls import --help')
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()})
        # An explicit import can initialize a new folder before daily use.
        import_error = self.error(self.import_file(), 3)
        self.assertIn('Repeat this command with --state-dir', import_error['message'])

    def test_imported_brief_with_wrong_org_recovers_to_original_connection_and_state(self):
        with patch.object(SalesforceReadOnlyAdapter, 'from_sf', return_value=self.reader()):
            self.success(self.import_file('--crm-backend', 'salesforce', '--sf-org', 'invented-original',
                                          account=ACCOUNT))
        before = {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()}
        prior_requests = len(self.requests)
        other = self.reader(organization='00D000000000002AAA')
        with patch.object(SalesforceReadOnlyAdapter, 'from_sf', return_value=other):
            error = self.error(self.invoke('brief', ACCOUNT, '--crm-backend', 'salesforce',
                                           '--sf-org', 'invented-other', '--calls-backend', 'imported'), 3)
        self.assertEqual(error['code'], 'refused')
        self.assertIn('source_identity_mismatch', error['message'])
        self.assertIn('different Salesforce connection or environment', error['message'])
        self.assertIn('original CRM connection', error['message'])
        self.assertIn('existing import state folder', error['message'])
        self.assertIn('original --sf-org or environment credentials', error['message'])
        self.assertIn('original --sf-environment value', error['message'])
        self.assertNotIn('fresh', error['message'])
        self.assertEqual(error['next'], 'gtm-connector calls import --help')
        for private in ('invented-original', 'invented-other', '00D000000000001AAA', '00D000000000002AAA'):
            self.assertNotIn(private, json.dumps(error))
        self.assertEqual(before, {path.name: path.read_bytes() for path in self.state.iterdir() if path.is_file()})
        self.assertEqual(len(self.requests), prior_requests + 1)
        self.assertIn(' FROM Organization ', self.requests[-1].url.params['q'])

    def test_salesforce_account_number_resolves_once_through_audited_search(self):
        reader = self.reader()
        with patch.object(SalesforceReadOnlyAdapter, 'from_env', return_value=reader):
            result = self.success(self.invoke('calls', 'import', str(self.export), '--account-number', 'INVENTED-001',
                                             '--crm-backend', 'salesforce'))
        self.assertEqual(result['account_id'], ACCOUNT)
        self.assertEqual(len(self.requests), 2)
        query = self.requests[-1].url.params['q']
        self.assertIn("AccountNumber = 'INVENTED-001'", query)
        self.assertIn('LIMIT 2', query)
        receipt, = read_audit(self.state / 'audit.sqlite3')
        self.assertEqual(receipt['tool'], 'search_accounts')
        self.assertEqual(receipt['arguments'], {'account_number': 'INVENTED-001'})
        self.assertEqual(receipt['audit_id'], result['resolution_receipt_id'])

    def test_native_salesforce_fallback_uses_audited_account_read(self):
        with patch.object(SalesforceReadOnlyAdapter, 'from_env', return_value=self.reader()):
            result = self.success(self.import_file('--crm-backend', 'salesforce', account=ACCOUNT))
        self.assertEqual(result['account_id'], ACCOUNT)
        receipt, = read_audit(self.state / 'audit.sqlite3')
        self.assertEqual(receipt['tool'], 'get_account')
        self.assertEqual(len(self.requests), 4)

    def test_ambiguous_salesforce_number_never_publishes_import(self):
        with patch.object(SalesforceReadOnlyAdapter, 'from_env', return_value=self.reader(duplicate=True)):
            error = self.error(self.invoke('calls', 'import', str(self.export), '--account-number', 'INVENTED-001',
                                          '--crm-backend', 'salesforce'), 3)
        self.assertIn('calls_account_ambiguous', error['message'])
        listed = self.success(self.invoke('calls', 'list'))
        self.assertEqual(listed['imports'], [])

    def test_direct_sqlite_inspection_refuses_calls_backend_flags(self):
        for command, extra in [('accounts', []), ('account', ['acc-003'])]:
            code, out, err = self.invoke(command, *extra, '--calls-backend', 'imported')
            self.assertEqual(code, 2, err)
            self.assertEqual(out, '')
            self.assertIn('--calls-backend', err)
            self.assertFalse(self.state.exists())

    def test_imported_daily_commands_require_explicit_state_before_credentials_or_runtime(self):
        previous = Path.cwd()
        try:
            os.chdir(self.root)
            for command in (['serve'], ['brief', ACCOUNT], ['accounts'], ['account', ACCOUNT]):
                with self.subTest(command=command), \
                        patch.object(SalesforceReadOnlyAdapter, 'from_env',
                                     side_effect=AssertionError('No credential load')) as credentials, \
                        patch('gtm_connector.runtime.Runtime', side_effect=AssertionError('No state')) as runtime, \
                        redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
                    code = main([*command, '--crm-backend', 'salesforce', '--calls-backend', 'imported', '--json'])
                    error = self.error((code, out.getvalue(), err.getvalue()), 2)
                    self.assertIn('--state-dir', error['message'])
                    self.assertIn('same import state folder', error['message'])
                    credentials.assert_not_called()
                    runtime.assert_not_called()
                self.assertFalse((self.root / 'var').exists())
        finally:
            os.chdir(previous)

    def test_abbreviated_explicit_state_uses_the_existing_import(self):
        self.success(self.import_file())
        with redirect_stdout(io.StringIO()) as out, redirect_stderr(io.StringIO()) as err:
            code = main(['brief', 'acc-003', '--calls-backend', 'imported',
                         '--state-d', str(self.state), '--json'])
        self.assertEqual(code, 0, err.getvalue())
        self.assertEqual(json.loads(out.getvalue())['status'], 'accepted')

    def test_imported_serve_labels_sources_without_reading_transcripts(self):
        class InterruptQueue(SimpleQueue):
            def get(self, *args, **kwargs):
                raise KeyboardInterrupt
        first = True
        def queue_factory():
            nonlocal first
            if first:
                first = False
                return InterruptQueue()
            return SimpleQueue()
        with patch('queue.SimpleQueue', side_effect=queue_factory):
            code, out, err = self.invoke('serve', '--calls-backend', 'imported')
        self.assertEqual(code, 0, err)
        ready = json.loads(out)
        self.assertEqual(ready['sources'], {'crm': 'demo_fixture', 'calls': 'imported_transcript',
                                           'slack': 'demo_fixture'})
        self.assertFalse((self.state / 'calls.sqlite3').exists())
        self.assertEqual(read_audit(self.state / 'audit.sqlite3'), [])

    def test_bound_demo_and_imported_calls_conflict_before_binding_or_state(self):
        code, out, err = self.invoke('serve', '--crm-backend', 'salesforce', '--calls-backend', 'imported',
                                    '--salesforce-demo-binding', str(self.root / 'does-not-exist'))
        self.assertEqual(code, 2, err)
        self.assertEqual(out, '')
        self.assertIn('cannot be combined', err)
        self.assertFalse(self.state.exists())


if __name__ == '__main__':
    unittest.main()
