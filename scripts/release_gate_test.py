"""Aggregate identity/scope controls using synthetic native report fixtures."""

from copy import deepcopy
import importlib.util
from pathlib import Path
import unittest

import release_native_test as fixture

SPEC = importlib.util.spec_from_file_location('release_gate', Path(__file__).with_name('release-gate.py'))
gate = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(gate)
m = gate.m


class AggregateControls(unittest.TestCase):
    def setUp(self):
        self.source = {key: value for key, value in fixture.source().items() if key != 'sha256'}
        self.source['files'] = {'scripts/testdata/release-process-helper.go': {'sha256': '9' * 64}}
        self.reports, archives = [], []
        for os_, arch in m.TARGETS:
            name = 'cutmy.exe' if os_ == 'windows' else 'cutmy'
            contents = {name: {'sha256': 'f' * 64, 'bytes': 100}}
            entry = {'os': os_, 'arch': arch, 'name': m.archive_name(self.source, os_, arch),
                     'sha256': 'e' * 64, 'bytes': 100, 'contents': contents, 'go_build_info': {'synthetic': True}}
            archives.append(entry)
            report = fixture.report(os_)
            report['target']['arch'] = report['runtime_version']['arch'] = arch
            report['source']['sha256'] = m.digest(m.canonical(self.source))
            report['artifact_receipt'].update(source_sha256=report['source']['sha256'],
                                               artifact_name='release-bundle-123-1')
            report['archive'] = {key: entry[key] for key in ('name', 'sha256', 'bytes')}
            report['binary']['absolute_path'] = 'C:\\private\\cutmy.exe' if os_ == 'windows' else '/private/cutmy'
            report['groups']['archive']['evidence'].update(contents=contents, go_build_info=entry['go_build_info'])
            report['run'] = {'id': '123', 'attempt': '1', 'job': 'native', 'workflow_sha': 'a' * 40}
            report['tools'] = {'python': '3.13', **{tool: {'version': 'synthetic control'} for tool in ('ffmpeg', 'ffprobe', 'psql')}}
            report['process_helper'] = {'source_path': 'scripts/testdata/release-process-helper.go',
                                        'source_sha256': '9' * 64, 'binary_sha256': '8' * 64,
                                        'binary_path': 'C:\\private\\helper.exe' if os_ == 'windows' else '/private/helper',
                                        'application_binary': False}
            self.reports.append(report)
        self.manifest = {'archives': archives}
        self.kwargs = {'manifest_sha256': 'd' * 64, 'repository': 'owner/repository', 'run_id': 123,
                       'run_attempt': 1, 'event_sha': 'a' * 40, 'artifact_id': 456, 'artifact_sha256': 'f' * 64}
        receipt = dict(self.reports[0]['artifact_receipt'])
        self.recovery = {'ok': True, 'checks': list(gate.ARCHIVE_RECOVERY_CHECKS),
                         'historical_revision': 'f2be62c42a6a0d3d7a017b40bd683596bc578b95',
                         'current_revision': self.source['commit'],
                         'go_version': f"go version {self.source['toolchain']} linux/amd64",
                         'schema_versions': ['20261005-storage-queue-v3', '20261010-cache-retention-v1',
                                             '20261010-ordered-migrations-v1', '20261010-storage-maintenance-v1',
                                             '20261011-source-retention-v1', '20261012-source-media-cleanup-v1'],
                         'upgrade_storage_defaults_checked': 3,
                         'artifacts': [{'label': label, 'bytes': 100, 'sha256': 'e' * 64, 'duration_seconds': 1,
                                        'codecs': {'audio': 'mp3'} if label == 'fresh-after-restore-mp3'
                                        else {'video': 'h264', 'audio': 'aac'}} for label in sorted(gate.RECOVERY_MEDIA_LABELS)],
                         'binary_sha256': {'current': 'f' * 64},
                         'current_external_archive': {'source_sha256': m.digest(m.canonical(self.source)),
                                                      'source_commit': self.source['commit'], 'source_tree': self.source['tree'],
                                                      'current_application_built': False, 'expected_binary_sha256': 'f' * 64,
                                                      'captured_binary_sha256_before': 'f' * 64, 'captured_binary_sha256_after': 'f' * 64,
                                                      'runtime_version': deepcopy(self.reports[0]['runtime_version']),
                                                      'archive_receipt': receipt}}

    def check(self, reports=None):
        return gate.validate_reports(self.reports if reports is None else reports, self.source, self.manifest, **self.kwargs)

    def reject(self, reports):
        with self.assertRaises((ValueError, RuntimeError, KeyError, TypeError, StopIteration)):
            self.check(reports)

    def test_exact_five_targets_and_required_scenarios(self):
        receipts = self.check()
        self.assertEqual(len(receipts), 5)
        self.assertTrue(all(receipt['required_groups'] == 7 and receipt['required_checks'] >= 37 for receipt in receipts))

    def test_missing_extra_duplicate_and_wrong_target(self):
        self.reject(self.reports[:-1])
        self.reject(self.reports + [deepcopy(self.reports[0])])
        self.reject([deepcopy(self.reports[0])] + self.reports[1:-1] + [deepcopy(self.reports[0])])
        wrong = deepcopy(self.reports)
        wrong[0]['target']['arch'] = wrong[0]['runtime_version']['arch'] = 'riscv64'
        self.reject(wrong)

    def test_wrong_source_manifest_run_attempt_event_job_and_artifact(self):
        mutations = [('source', 'tree'), ('manifest_sha256', None), ('run', 'id'), ('run', 'attempt'),
                     ('run', 'job'), ('run', 'workflow_sha'), ('artifact_receipt', 'repository'),
                     ('artifact_receipt', 'run_id'), ('artifact_receipt', 'artifact_id'),
                     ('artifact_receipt', 'artifact_name'), ('artifact_receipt', 'artifact_sha256')]
        for container, key in mutations:
            with self.subTest(container=container, key=key):
                wrong = deepcopy(self.reports)
                if key is None:
                    wrong[0][container] = '0' * 64
                else:
                    wrong[0][container][key] = 'wrong'
                self.reject(wrong)

    def test_wrong_archive_binary_document_helper_and_tools(self):
        mutations = [('archive', 'sha256'), ('binary', 'absolute_path'), ('binary', 'bytes'),
                     ('process_helper', 'source_sha256'), ('process_helper', 'application_binary')]
        for container, key in mutations:
            with self.subTest(container=container, key=key):
                wrong = deepcopy(self.reports)
                wrong[0][container][key] = True if key == 'application_binary' else 'wrong'
                self.reject(wrong)
        wrong = deepcopy(self.reports)
        wrong[0]['groups']['archive']['evidence']['contents'] = {}
        self.reject(wrong)
        wrong = deepcopy(self.reports)
        del wrong[0]['tools']['psql']
        self.reject(wrong)

    def test_missing_failed_skipped_group_or_named_check(self):
        for mutation in ('missing', 'failed', 'skip', 'missing_check', 'failed_check'):
            with self.subTest(mutation=mutation):
                wrong = deepcopy(self.reports)
                if mutation == 'missing':
                    del wrong[0]['groups']['service']
                elif mutation == 'missing_check':
                    wrong[0]['groups']['service']['checks'].pop()
                elif mutation == 'failed_check':
                    wrong[0]['groups']['service']['checks'][0]['ok'] = False
                else:
                    wrong[0]['groups']['service']['status'] = mutation
                self.reject(wrong)

    def test_actual_archive_recovery_passes(self):
        kwargs = {key: value for key, value in self.kwargs.items() if key not in ('manifest_sha256', 'event_sha')}
        value = gate.validate_recovery(self.recovery, self.source, self.manifest, **kwargs)
        self.assertEqual(value['required_groups'], 9)
        self.assertEqual(value['decoded_media_observations'], 11)
        self.assertEqual(value['schema_versions'], self.recovery['schema_versions'])

    def test_old_missing_changed_extra_or_duplicate_schema_versions_are_rejected(self):
        kwargs = {key: value for key, value in self.kwargs.items() if key not in ('manifest_sha256', 'event_sha')}
        for mutation in ('old005', 'missing', 'changed006', 'extra', 'duplicate'):
            with self.subTest(mutation=mutation):
                wrong = deepcopy(self.recovery)
                if mutation == 'old005':
                    wrong['schema_versions'].pop()
                elif mutation == 'missing':
                    wrong['schema_versions'].pop(0)
                elif mutation == 'changed006':
                    wrong['schema_versions'][-1] = '20261012-source-media-cleanup-v2'
                elif mutation == 'extra':
                    wrong['schema_versions'].append('20261013-unreviewed-v1')
                else:
                    wrong['schema_versions'].append(wrong['schema_versions'][-1])
                with self.assertRaises((ValueError, RuntimeError, KeyError)):
                    gate.validate_recovery(wrong, self.source, self.manifest, **kwargs)

    def test_missing_rebuilt_changed_wrong_source_or_old_recovery_is_rejected(self):
        kwargs = {key: value for key, value in self.kwargs.items() if key not in ('manifest_sha256', 'event_sha')}
        for mutation in ('missing', 'rebuilt', 'changed', 'source', 'old_report', 'missing_media', 'artifact'):
            with self.subTest(mutation=mutation):
                wrong = deepcopy(self.recovery)
                if mutation == 'missing':
                    del wrong['current_external_archive']
                elif mutation == 'rebuilt':
                    wrong['current_external_archive']['current_application_built'] = True
                elif mutation == 'changed':
                    wrong['current_external_archive']['captured_binary_sha256_after'] = '0' * 64
                elif mutation == 'source':
                    wrong['current_external_archive']['source_commit'] = '0' * 40
                elif mutation == 'old_report':
                    wrong['checks'][0] = 'immutable_v0.2.1_and_current_binaries_built'
                elif mutation == 'missing_media':
                    wrong['artifacts'].pop()
                else:
                    wrong['current_external_archive']['archive_receipt']['artifact_id'] = 999
                with self.assertRaises((ValueError, RuntimeError, KeyError)):
                    gate.validate_recovery(wrong, self.source, self.manifest, **kwargs)

    def test_duplicate_unlabeled_missing_audio_video_or_invalid_duration_is_rejected(self):
        kwargs = {key: value for key, value in self.kwargs.items() if key not in ('manifest_sha256', 'event_sha')}
        for mutation in ('duplicate', 'unlabeled', 'audio', 'video', 'duration'):
            with self.subTest(mutation=mutation):
                wrong = deepcopy(self.recovery)
                if mutation == 'duplicate':
                    wrong['artifacts'][0] = deepcopy(wrong['artifacts'][1])
                elif mutation == 'unlabeled':
                    del wrong['artifacts'][0]['label']
                elif mutation in ('audio', 'video'):
                    del wrong['artifacts'][0]['codecs'][mutation]
                else:
                    wrong['artifacts'][0]['duration_seconds'] = float('nan')
                with self.assertRaises((ValueError, RuntimeError, KeyError)):
                    gate.validate_recovery(wrong, self.source, self.manifest, **kwargs)


if __name__ == '__main__':
    unittest.main()
