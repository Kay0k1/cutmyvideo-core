"""Pure synthetic controls; never execute Docker, Git, media or a database."""
from copy import deepcopy
import hashlib
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


current = module('current_controls', ROOT / 'current-installation-acceptance.py')
legacy = module('legacy_controls_fixture', ROOT / 'self_hosting_acceptance_test.py')
base = legacy.a
proof = base.transition_module()


def baseline_files():
    value = proof.strict_json((ROOT / 'testdata/selfhosting-current-baseline-source.json').read_bytes())
    if current.source_tree(value) != current.BASELINE_TREE:
        raise AssertionError('immutable baseline metadata fixture changed')
    inventory = current.application({'files': value})
    if len(inventory) != current.BASELINE_FILES or proof.digest(proof.canonical(inventory)) != current.BASELINE_APPLICATION_SHA256:
        raise AssertionError('baseline application fixture differs')
    return value


def blob(data):
    return {'mode': '100644', 'git_blob': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest(),
            'bytes': len(data), 'sha256': proof.digest(data)}


class SyntheticCurrentReceipt:
    def __init__(self, directory):
        self.directory = Path(directory)
        self.report = report = legacy.successful_report()
        report.update(mode=current.MODE, schema_version=3, runtime_application_sources_unchanged=False)
        report['cleanup'].update(private_state_backup_config_keys_removed=True, private_workspace_retained=False)
        old_files = baseline_files()
        current_files = deepcopy(old_files)
        current_files['internal/app/synthetic-current.go'] = blob(b'package app\n// synthetic control only\n')
        old, now = report['images']['old'], report['images']['current']
        old.update(source_commit=current.BASELINE, tree=current.BASELINE_TREE,
                   version='v0.0.0-candidate.g' + current.BASELINE)
        old['runtime_version'].update(commit=current.BASELINE, version=old['version'])
        now['tree'] = current.source_tree(current_files)
        report['checkout'].update(tree=now['tree'], head_tree=now['tree'])
        self.sources = {}
        receipts = {}
        for label, files in (('old', old_files), ('current', current_files)):
            image = report['images'][label]
            source = {'commit': image['source_commit'], 'tree': image['tree'], 'files': files,
                      **{name: image[name] for name in ('version', 'build_time', 'toolchain')}}
            self.sources[label] = source
            receipts[label + '_source'] = self.write(label, source)
            image['source_sha256'] = receipts[label + '_source']['sha256']
        report['images']['restored'] = deepcopy(now)
        report['groups']['backup_restore']['checks']['actual_same_project_image_upgrade']['evidence'] = {
            'application_code_unchanged': False}
        for attempt in report['controlled_probe_attempts']:
            attempt['source_identity'] = {name: old[name] for name in ('source_commit', 'source_sha256', 'image_id')}
            chain = 'CMF_' + base.digest(attempt['project'].encode())[:10]
            for phase, receipt in attempt['packet_rule_snapshots'].items():
                packets = 8 if phase == 'before' else 9
                raw = f'[{packets}:60] -A {chain} -d 10.0.0.0/8 -j DROP\n'.encode()
                (self.directory / receipt['path']).write_bytes(raw)
                receipt['sha256'] = base.digest(raw)
        report['baseline_tcp_readiness']['source_commit'] = current.BASELINE
        admission = {'provider': deepcopy(report['provider']), 'checkout': deepcopy(report['checkout']),
                     'baseline_commit': current.BASELINE, 'baseline_tree': current.BASELINE_TREE,
                     'candidate_commit': report['expected_checkout'], 'candidate_tree': now['tree'],
                     'candidate_application_files': len(current.application(self.sources['current'])),
                     'candidate_application_sha256': proof.digest(proof.canonical(current.application(self.sources['current']))),
                     'schema_sources_unchanged': True, 'captured_before_image_builds': True}
        report['current_installation'] = {'baseline': {'commit': current.BASELINE, 'tree': current.BASELINE_TREE,
                'application_files': current.BASELINE_FILES, 'application_sha256': current.BASELINE_APPLICATION_SHA256},
                'admission': admission, **receipts}
        checks = report['groups']['identity']['checks']
        checks.pop('application_sources_unchanged')
        checks['current_application_sources_bound'] = {'ok': True, 'evidence': {
                'baseline_commit': current.BASELINE, 'candidate_commit': report['expected_checkout'],
                'candidate_application_files': admission['candidate_application_files'],
                'candidate_application_sha256': admission['candidate_application_sha256'], 'schema_changing_upgrade': False}}

    def write(self, label, source):
        name = 'current-' + label + '-source.json'
        raw = proof.canonical(source)
        (self.directory / name).write_bytes(raw)
        return {'path': name, 'bytes': len(raw), 'sha256': proof.digest(raw)}

    def refresh_source(self, label):
        source = self.sources[label]
        receipt = self.write(label, source)
        self.report['current_installation'][label + '_source'] = receipt
        self.report['images'][label]['source_sha256'] = receipt['sha256']
        if label == 'current':
            self.report['images']['restored']['source_sha256'] = receipt['sha256']

    def validate(self):
        base.validate_report(self.report, self.directory)


class CurrentReceiptControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.fixture = SyntheticCurrentReceipt(self.temp.name)

    def test_changed_application_from_pinned005_baseline_has_own_mode_and_raw_binding(self):
        self.fixture.validate()
        self.assertNotEqual(self.fixture.report['current_installation']['admission']['candidate_application_sha256'],
                            current.BASELINE_APPLICATION_SHA256)
        self.assertEqual(len(current.groups(base.GROUPS)), 7)
        self.assertEqual(sum(map(len, current.groups(base.GROUPS).values())), 35)
        self.assertIn('application_sources_unchanged', base.GROUPS['identity'])

    def test_missing_raw_source_directory_is_refused(self):
        with self.assertRaises(RuntimeError):
            base.validate_report(self.fixture.report)

    def test_current_mode_cannot_relabel_historical_transition_or_legacy_receipts(self):
        for mode in ('same-schema', proof.MODE, 'unknown'):
            with self.subTest(mode=mode):
                report = deepcopy(self.fixture.report)
                report['mode'] = mode
                with self.assertRaises(RuntimeError):
                    base.validate_report(report, self.fixture.directory)

    def test_checkout_baseline_receipt_context_and_private_cleanup_tampering_refuse(self):
        changes = [lambda r: r['current_installation']['baseline'].update(commit='0' * 40),
                   lambda r: r['current_installation']['admission'].update(candidate_commit='0' * 40),
                   lambda r: r['current_installation']['admission']['provider'].update(GITHUB_RUN_ID='1'),
                   lambda r: r['current_installation']['admission']['checkout'].update(tree='0' * 40),
                   lambda r: r['current_installation']['admission'].update(captured_before_image_builds=False),
                   lambda r: r['current_installation']['admission'].update(candidate_application_files=174.0),
                   lambda r: r.update(runtime_application_sources_unchanged=True),
                   lambda r: r['groups']['backup_restore']['checks']['actual_same_project_image_upgrade']['evidence']
                   .update(application_code_unchanged=True),
                   lambda r: r['cleanup'].update(private_workspace_retained=True),
                   lambda r: r['groups']['roles']['checks'].pop('runtime_privilege_denials')]
        for change in changes:
            with self.subTest(change=change):
                report = deepcopy(self.fixture.report)
                change(report)
                with self.assertRaises(RuntimeError):
                    base.validate_report(report, self.fixture.directory)

    def test_source_blob_or_missing_file_cannot_be_hidden_by_recomputed_receipt_hash(self):
        for change in (lambda files: files['internal/app/http.go'].update(git_blob='0' * 40),
                       lambda files: files.pop('internal/app/http.go')):
            with self.subTest(change=change):
                fixture = SyntheticCurrentReceipt(self.temp.name)
                change(fixture.sources['current']['files'])
                fixture.refresh_source('current')
                with self.assertRaises(RuntimeError):
                    fixture.validate()

    def test_schema_change_requires_a_separate_reviewed_transition(self):
        source = self.fixture.sources['current']
        source['files']['internal/app/migrations/006_unreviewed.sql'] = blob(b'SELECT 1;\n')
        tree = current.source_tree(source['files'])
        source['tree'] = tree
        report = self.fixture.report
        report['checkout'].update(tree=tree, head_tree=tree)
        for label in ('current', 'restored'):
            report['images'][label]['tree'] = tree
        admission = report['current_installation']['admission']
        admission.update(checkout=deepcopy(report['checkout']), candidate_tree=tree,
                         candidate_application_files=len(current.application(source)),
                         candidate_application_sha256=proof.digest(proof.canonical(current.application(source))))
        report['groups']['identity']['checks']['current_application_sources_bound']['evidence'].update(
            candidate_application_files=admission['candidate_application_files'],
            candidate_application_sha256=admission['candidate_application_sha256'])
        self.fixture.refresh_source('current')
        with self.assertRaisesRegex(RuntimeError, 'unchanged schema'):
            self.fixture.validate()

    def test_mode_default_and_wrong_specialization_refuse_before_host_work(self):
        args = base.parse_args(['--mode', current.MODE, '--revision', '1' * 40, '--output', '/synthetic-output'])
        self.assertEqual(args.upgrade_from, current.BASELINE)
        legacy_args = base.parse_args(['--revision', '1' * 40, '--output', '/synthetic-output'])
        self.assertEqual(legacy_args.upgrade_from, base.UPGRADE_FROM)
        init = Mock()
        fake = SimpleNamespace(Acceptance=type('PureBase', (), {'__init__': init}), transition_module=lambda: proof)
        specialized = current.acceptance_class(fake)
        for mode in (None, '', 'same-schema', proof.MODE, 'unknown'):
            with self.subTest(mode=mode), self.assertRaises(RuntimeError):
                specialized(SimpleNamespace(mode=mode))
        init.assert_not_called()

    def test_complete_tree_fixture_and_file_directory_collision_fail_closed(self):
        self.assertEqual(current.source_tree(baseline_files()), current.BASELINE_TREE)
        with self.assertRaises(RuntimeError):
            current.source_tree({'a': blob(b'one'), 'a/b': blob(b'two')})
        for mode in ('120000', '160000'):
            invalid = blob(b'one'); invalid['mode'] = mode
            with self.subTest(mode=mode), self.assertRaises(RuntimeError):
                current.source_tree({'a': invalid})


class CurrentAdmissionControls(unittest.TestCase):
    """Prove admission happens before host setup and actual image builds."""
    def acceptance(self, old=None, candidate=None):
        setup = Mock()
        fake = SimpleNamespace(Acceptance=type('PureBase', (), {'initialize_host': setup}),
                               transition_module=lambda: proof, GROUPS=base.GROUPS, REPO=base.REPO)
        value = object.__new__(current.acceptance_class(fake))
        value.args = SimpleNamespace(revision='1' * 40)
        value.git, value.run, value.save = Mock(return_value=current.BASELINE_TREE), Mock(), Mock()
        value.report = {'provider': {'synthetic': True}, 'checkout': {'tree': '2' * 40}, 'current_installation': {}}
        old = current.application({'files': baseline_files()}) if old is None else old
        candidate = deepcopy(old) if candidate is None else candidate
        return value, setup, old, candidate

    def test_changed_application_is_admitted_before_parent_host_setup_without_building_images(self):
        value, setup, old, candidate = self.acceptance()
        candidate['internal/app/synthetic-current.go'] = blob(b'package app\n')
        with patch.object(proof, 'application_inventory', side_effect=[old, candidate]) as inventory:
            value.initialize_host()
        self.assertEqual(inventory.call_count, 2)
        setup.assert_called_once()
        value.run.assert_not_called()
        self.assertEqual(value.report['current_installation']['admission']['candidate_application_files'], 174)
        self.assertIs(value.report['current_installation']['admission']['captured_before_image_builds'], True)
        candidate['internal/app/synthetic-current.go']['sha256'] = '0' * 64
        self.assertNotEqual(value.report['current_installation']['admission']['candidate_application_sha256'],
                            proof.digest(proof.canonical(candidate)))

    def test_invalid_predecessor_or_schema_refuses_before_parent_host_setup(self):
        for fault in ('tree', 'baseline', 'schema'):
            value, setup, old, candidate = self.acceptance()
            if fault == 'tree':
                value.git.return_value = '0' * 40
            elif fault == 'baseline':
                old.pop('internal/app/http.go')
            else:
                candidate['internal/app/migrations/006_unreviewed.sql'] = blob(b'SELECT 1;\n')
            with self.subTest(fault=fault), patch.object(proof, 'application_inventory', side_effect=[old, candidate]), \
                    self.assertRaises(RuntimeError):
                value.initialize_host()
            setup.assert_not_called()
            value.run.assert_not_called()
            value.save.assert_not_called()

    def test_unadmitted_actual_image_application_refuses_before_receipt_creation(self):
        value, _, old, candidate = self.acceptance()
        value.admitted_old, value.admitted_current = old, candidate
        value.old_source = {'files': deepcopy(old)}
        value.current_source = {'files': deepcopy(candidate)}
        value.current_source['files']['internal/app/http.go']['sha256'] = '0' * 64
        with self.assertRaisesRegex(RuntimeError, 'built current installation application'):
            value.application_identity()
        value.save.assert_not_called()


if __name__ == '__main__':
    unittest.main()
