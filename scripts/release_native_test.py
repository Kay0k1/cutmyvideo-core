"""Synthetic negative controls for the external archive consumer; no Go/PG."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock
from unittest.mock import patch

from release_native_test_fixtures import n, receipt, report, source


class NativeControls(unittest.TestCase):
    def test_exact_required_report_passes_for_unix_and_windows(self):
        n.validate_report(report())
        n.validate_report(report('windows'))

    def reject(self, value):
        with self.assertRaises((RuntimeError, KeyError, TypeError)):
            n.validate_report(value)

    def test_missing_extra_and_not_run_group_rejected(self):
        for mutation in ('missing', 'extra', 'not_run', 'skip', 'fail'):
            with self.subTest(mutation=mutation):
                value = report()
                if mutation == 'missing':
                    del value['groups']['service']
                elif mutation == 'extra':
                    value['groups']['optional'] = deepcopy(value['groups']['service'])
                else:
                    value['groups']['service']['status'] = mutation
                self.reject(value)

    def test_missing_extra_duplicate_failed_and_skipped_check_rejected(self):
        for mutation in ('missing', 'extra', 'duplicate', 'failed', 'skip'):
            with self.subTest(mutation=mutation):
                value = report()
                checks = value['groups']['media']['checks']
                if mutation == 'missing':
                    checks.pop()
                elif mutation == 'extra':
                    checks.append({'name': 'wishful_check', 'ok': True})
                elif mutation == 'duplicate':
                    checks.append(dict(checks[0]))
                elif mutation == 'failed':
                    checks[0]['ok'] = False
                else:
                    checks[0]['skip'] = True
                self.reject(value)

    def test_no_unix_signal_substitution_by_timeout(self):
        value = report()
        value['groups']['process']['checks'] = value['groups']['process']['checks'][:3]
        self.reject(value)

    def test_every_archived_collection_observation_is_required_on_all_targets(self):
        self.assertEqual(len(n.COLLECTION_MEDIA_CHECKS), 6)
        for target in ('linux', 'darwin', 'windows'):
            for name in n.COLLECTION_MEDIA_CHECKS:
                self.assertIn(name, n.required_checks(target)['media'])
                for mutation in ('missing', 'failed', 'skip'):
                    with self.subTest(target=target, name=name, mutation=mutation):
                        value = report(target)
                        checks = value['groups']['media']['checks']
                        check = next(item for item in checks if item['name'] == name)
                        if mutation == 'missing':
                            checks.remove(check)
                        elif mutation == 'failed':
                            check['ok'] = False
                        else:
                            check['skip'] = True
                        self.reject(value)

    def test_all_six_core15_observations_are_required_on_every_archived_target(self):
        self.assertEqual(len(n.CORE15_MEDIA_CHECKS), 6)
        for target, arch in n.m.TARGETS:
            for name in n.CORE15_MEDIA_CHECKS:
                self.assertIn(name, n.required_checks(target)['media'])
                for mutation in ('missing', 'failed', 'skip'):
                    with self.subTest(target=target, arch=arch, name=name, mutation=mutation):
                        value = report(target)
                        value['target']['arch'] = value['runtime_version']['arch'] = arch
                        checks = value['groups']['media']['checks']
                        check = next(item for item in checks if item['name'] == name)
                        if mutation == 'missing':
                            checks.remove(check)
                        elif mutation == 'failed':
                            check['ok'] = False
                        else:
                            check['skip'] = True
                        self.reject(value)

    def test_changed_binary_and_suppressed_errors_rejected(self):
        for mutation in ('before', 'after', 'archive', 'errors', 'ok'):
            with self.subTest(mutation=mutation):
                value = report()
                if mutation in ('before', 'after'):
                    value['binary']['sha256_' + mutation] = '0' * 64
                elif mutation == 'archive':
                    value['groups']['archive']['evidence']['binary_sha256'] = '0' * 64
                elif mutation == 'errors':
                    value['errors'].append('service failed')
                else:
                    value['ok'] = False
                self.reject(value)

    def test_wrong_executed_version_target_commit_and_extra_field_rejected(self):
        for key in ('version', 'commit', 'build_time', 'go_version', 'os', 'arch', 'extra'):
            with self.subTest(key=key):
                value = report()
                value['runtime_version'][key] = 'wrong'
                self.reject(value)

    def test_wrong_source_and_artifact_hash_rejected(self):
        for container, key in (('source', 'sha256'), ('artifact_receipt', 'source_sha256'),
                               ('artifact_receipt', 'source_commit'), ('archive', 'sha256')):
            with self.subTest(container=container, key=key):
                value = report()
                value[container][key] = 'invalid'
                self.reject(value)

    def test_executable_requires_absolute_regular_exact_path_and_hash(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            file = root / 'cutmy'
            file.write_bytes(b'not an application; structural test fixture only')
            expected = n.file_hash(file)
            self.assertEqual(n.checked_binary(file, expected), file)
            for path, sha in ((Path('cutmy'), expected), (root / 'missing', expected), (root, expected), (file, '0' * 64)):
                with self.subTest(path=str(path), sha=sha), self.assertRaises(RuntimeError):
                    n.checked_binary(path, sha)
            file.write_bytes(b'replaced executable')
            with self.assertRaises(RuntimeError):
                n.checked_binary(file, expected)

    def test_app_invocation_cannot_fall_back_to_path(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = object.__new__(n.Native)
            runner.binary = Path(directory).resolve() / 'missing-cutmy'
            runner.expected_binary_sha = '0' * 64
            runner.commands = Mock()
            with self.assertRaises(RuntimeError):
                runner.app('version')
            runner.commands.run.assert_not_called()

    def test_process_inputs_are_explicit_separate_exact_copies(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = root / 'synthetic.mp4'
            source.write_bytes(b'owned input bytes; structural fixture only')
            paths = []
            for label in ('timeout', 'sigint', 'sigterm'):
                copied, marker = n.process_case_input(source, root, label)
                self.assertEqual(copied.read_bytes(), source.read_bytes())
                self.assertEqual(marker, Path(str(copied) + '.tree.json'))
                self.assertTrue(copied.is_absolute())
                paths.append(copied)
            self.assertEqual(len(set(paths)), 3)
            with self.assertRaises(FileExistsError):
                n.process_case_input(source, root, 'timeout')

    def test_process_input_rejects_unowned_path_or_unknown_case(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = root / 'synthetic.mp4'
            source.write_bytes(b'owned fixture')
            for original, work, label in ((Path('relative.mp4'), root, 'timeout'),
                                          (root / 'missing.mp4', root, 'timeout'),
                                          (source, Path('relative'), 'timeout'),
                                          (source, root, '../elsewhere')):
                with self.subTest(source=str(original), work=str(work), label=label), self.assertRaises(RuntimeError):
                    n.process_case_input(original, work, label)

    def test_partial_process_markers_survive_before_private_directory_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            work, evidence = root / 'work', root / 'evidence'
            work.mkdir()
            evidence.mkdir()
            marker = work / 'input.mp4.tree.json'
            partial = Path(str(marker) + '.pid.0')
            data = b'{"pids":[123],"addresses":["127.0.0.1:1234"]}'
            partial.write_bytes(data)
            records = n.preserve_process_markers(marker, evidence, 'timeout')
            self.assertEqual(len(records), 1)
            self.assertEqual((evidence / records[0]['path']).read_bytes(), data)
            self.assertEqual(records[0]['sha256'], n.file_hash(partial))
            partial.unlink()
            work.rmdir()
            self.assertEqual((evidence / records[0]['path']).read_bytes(), data)

    def test_artifact_receipt_immutable_source_and_fields(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'download.json'
            value = receipt()
            path.write_text(json.dumps(value))
            self.assertEqual(n.validate_receipt(path, 'c' * 64, 'a' * 40)['artifact_id'], 456)
            for mutation in ('source_commit', 'source_sha256', 'artifact_sha256', 'run_id', 'artifact_bytes', 'extra'):
                changed = dict(value)
                changed[mutation] = 0 if mutation in ('run_id', 'artifact_bytes') else 'wrong'
                path.write_text(json.dumps(changed))
                with self.subTest(mutation=mutation), self.assertRaises(RuntimeError):
                    n.validate_receipt(path, 'c' * 64, 'a' * 40)

    def test_error_json_must_be_one_envelope_with_exact_exit_and_empty_stdout(self):
        valid = b'{"error":{"code":"timeout","message":"deadline exceeded"}}\n'
        n.error_envelope(b'', valid, 124, 124, 'timeout')
        for stdout, stderr, status in ((b'success', valid, 124), (b'', valid + valid, 124),
                                       (b'', b'plain diagnostic', 124), (b'', valid, 1),
                                       (b'', valid.replace(b'timeout', b'cancelled'), 124)):
            with self.subTest(stdout=stdout, stderr=stderr, status=status), self.assertRaises(RuntimeError):
                n.error_envelope(stdout, stderr, status, 124, 'timeout')

    def test_native_target_mismatch_rejected_before_evidence_or_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            args = argparse.Namespace(target='windows/amd64', directory=Path(directory),
                                      evidence_dir=Path(directory) / 'evidence')
            with patch.object(n, 'native_target', return_value=('linux', 'amd64')), self.assertRaises(RuntimeError):
                n.Native(args)
            self.assertFalse(args.evidence_dir.exists())

    def test_no_stale_or_bundle_internal_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            bundle, evidence = root / 'bundle', root / 'evidence'
            bundle.mkdir()
            evidence.mkdir()
            for destination in (evidence, bundle / 'evidence'):
                args = argparse.Namespace(target='linux/amd64', directory=bundle, evidence_dir=destination)
                with patch.object(n, 'native_target', return_value=('linux', 'amd64')), self.assertRaises(RuntimeError):
                    n.Native(args)

    def test_manifest_digest_failure_precedes_bundle_verification_or_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / 'MANIFEST.json').write_bytes(b'{"wrong":true}')
            runner = object.__new__(n.Native)
            runner.directory = root
            runner.args = argparse.Namespace(source=root / 'SOURCE.json', expected_source_sha256='c' * 64,
                                            expected_manifest_sha256='0' * 64)
            with patch.object(n.m, 'load_source', return_value=source()), patch.object(n.m, 'verify') as verify:
                with self.assertRaises(RuntimeError):
                    runner.archive()
                verify.assert_not_called()

    def test_stage_cannot_claim_missing_checks_as_pass(self):
        runner = object.__new__(n.Native)
        runner.target = ('linux', 'amd64')
        runner.report = report()
        with patch.object(runner, 'save'), self.assertRaises(RuntimeError):
            runner.stage('service', lambda: (['real_server_and_worker'], {}))
        self.assertEqual(runner.report['groups']['service']['status'], 'fail')
        self.assertTrue(runner.report['errors'])

    def test_post_smoke_binary_change_fails_final_verdict(self):
        value = report()
        value['binary']['sha256_after'] = '0' * 64
        self.reject(value)

    def test_database_cannot_override_the_loopback_isolation(self):
        for url in ('postgres://test@production.example/test',
                    'postgres://test@127.0.0.1/test?host=production.example',
                    'postgres://test@127.0.0.1/test?service=production',
                    'postgres://test@127.0.0.1/host%3Dproduction.example'):
            with self.subTest(url=url), self.assertRaises(RuntimeError):
                n.PostgreSQL(url, Mock(), 'psql')
        with patch.dict(n.os.environ, {'PGHOSTADDR': '203.0.113.1', 'PGSERVICE': 'unrelated', 'PGOPTIONS': 'unrelated'}):
            pg = n.PostgreSQL('postgres://test@127.0.0.1/test?sslmode=disable', Mock(), 'psql')
            self.assertNotIn('PGHOSTADDR', pg.env)
            self.assertNotIn('PGSERVICE', pg.env)
            self.assertNotIn('PGOPTIONS', pg.env)
            self.assertEqual(pg.env['PGHOST'], '127.0.0.1')

    def test_real_multicall_symlinks_keep_invocation_name_for_version_and_sql(self):
        # These release controls run in the Linux resolver/build job. Execute a
        # real tiny multicall fixture; no database or product binary is used.
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            evidence = root / 'evidence'
            evidence.mkdir()
            dispatch = root / 'multicall.py'
            dispatch.write_text(f'#!{sys.executable}\n' + '''
from pathlib import Path
import sys
name = Path(sys.argv[0]).name
if name not in ('ffmpeg', 'ffprobe', 'psql'):
    raise SystemExit(19)
if sys.argv[1:] == (['--version'] if name == 'psql' else ['-version']):
    print(name + ' synthetic multicall version')
elif name == 'psql' and sys.argv[-2:] == ['--command', "SELECT 'synthetic-control'"]:
    print('synthetic-control')
else:
    raise SystemExit(20)
''')
            dispatch.chmod(0o755)
            selected = {}
            for name in ('ffmpeg', 'ffprobe', 'psql'):
                path = root / name
                path.symlink_to(dispatch)
                selected[name] = str(path)
            runner = object.__new__(n.Native)
            runner.commands = n.Commands(evidence)
            runner.report = {'tools': {}}
            with patch.object(n.shutil, 'which', side_effect=lambda name: selected[name]):
                runner.tools()
            for name in selected:
                self.assertEqual(getattr(runner, name), Path(selected[name]))
                self.assertEqual(runner.report['tools'][name]['path'], selected[name])
            pg = n.PostgreSQL('postgres://test@127.0.0.1/test?sslmode=disable', runner.commands, runner.psql)
            self.assertEqual(pg.sql("SELECT 'synthetic-control'"), 'synthetic-control')
            self.assertEqual(runner.commands.records[-1]['command'][0], selected['psql'])
            # Resolving the same file really changes dispatch and fails, so
            # this control detects the original wrapper invocation defect.
            _, _, status = runner.commands.run([dispatch, '--version'], expected=19)
            self.assertEqual(status, 19)

    def test_shared_http_transcript_retains_both_owner_and_foreign_requests(self):
        with tempfile.TemporaryDirectory() as directory:
            shared = []
            owner = n.Client(1234, Path(directory), shared)
            foreign = n.Client(1234, Path(directory), shared)
            for client, status in ((owner, 200), (foreign, 404), (owner, 204)):
                response = Mock()
                response.status = status
                response.read.return_value = b''
                response.getheaders.return_value = []
                response.getheader.return_value = None
                connection = Mock()
                connection.getresponse.return_value = response
                with patch.object(n.http.client, 'HTTPConnection', return_value=connection):
                    client.request('GET', '/owned-synthetic', expected=status)
            actual = json.loads((Path(directory) / 'http.json').read_text())
            self.assertEqual([row['status'] for row in actual], [200, 404, 204])

    def test_shutdown_attempts_all_services_after_one_failure(self):
        runner = object.__new__(n.Native)
        runner.target = ('linux', 'amd64')
        server, worker = Mock(pid=100, returncode=0), Mock(pid=101, returncode=1)
        server.poll.return_value = None
        worker.poll.side_effect = [None, 1]
        runner.processes = [(server, 'server'), (worker, 'worker')]
        runner.service_records = [{'pid': 100}, {'pid': 101}]
        with self.assertRaises(RuntimeError):
            runner.stop_services()
        server.send_signal.assert_called_once_with(n.signal.SIGTERM)
        server.wait.assert_called_once_with(timeout=15)
        worker.send_signal.assert_called_once_with(n.signal.SIGTERM)
        self.assertEqual(runner.processes, [])

    def test_already_exited_worker_is_failure_and_server_still_stops(self):
        runner = object.__new__(n.Native)
        runner.target = ('linux', 'amd64')
        server, worker = Mock(pid=100, returncode=0), Mock(pid=101, returncode=1)
        server.poll.return_value = None
        worker.poll.return_value = 1
        runner.processes = [(server, 'server'), (worker, 'worker')]
        runner.service_records = [{'pid': 100}, {'pid': 101}]
        with self.assertRaisesRegex(RuntimeError, 'worker exited unexpectedly'):
            runner.stop_services()
        server.wait.assert_called_once_with(timeout=15)

    def test_directory_cleanup_failure_cannot_leave_success_report(self):
        with tempfile.TemporaryDirectory() as directory:
            runner = object.__new__(n.Native)
            runner.target = ('linux', 'amd64')
            runner.evidence = Path(directory)
            runner.work = Path(directory) / 'work'
            runner.work.mkdir()
            runner.binary = runner.work / 'cutmy'
            runner.binary.write_bytes(b'synthetic fixture; never executed')
            runner.pg = None
            runner.report = report()
            for name in n.REQUIRED_GROUPS:
                setattr(runner, name, lambda name=name: (list(n.required_checks('linux')[name]), {}))
            with patch.object(runner, 'preserve_media'), patch.object(runner, 'stop_services'), \
                    patch.object(n, 'validate_report'), patch.object(n.shutil, 'rmtree', side_effect=OSError('owned directory busy')):
                with self.assertRaises(RuntimeError):
                    runner.run()
            actual = json.loads((Path(directory) / 'native-report.json').read_text())
            self.assertIs(actual['ok'], False)
            self.assertTrue(any('owned directory busy' in value for value in actual['errors']))


if __name__ == '__main__':
    unittest.main()
