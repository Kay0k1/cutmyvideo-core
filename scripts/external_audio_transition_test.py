"""Independent009 receipt/admission controls; no Docker or host mutations."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


audio = module('audio_controls', 'external-audio-transition.py')
existing = module('audio_current_fixture', 'current_installation_acceptance_test.py')
base, proof = existing.base, existing.proof


def baseline_files():
    files = proof.strict_json((ROOT / 'testdata/selfhosting-external-audio-baseline-source.txt').read_bytes())
    application = existing.current.application({'files': files})
    if (existing.current.source_tree(files) != audio.BASELINE_TREE
            or len(application) != audio.BASELINE_FILES
            or proof.digest(proof.canonical(application)) != audio.BASELINE_APPLICATION_SHA256):
        raise AssertionError('immutable1.4.2 schema008 baseline fixture changed')
    return files


def synthetic_fixture():
    primary, external, job, owner = 'retained', 'acceptance_audio_source_synthetic001', 'acceptance_audio_job_synthetic001', 'owner'
    source = dict(id=external, owner=owner, kind='upload', path='/data/source', media_removed=False,
                  retention_job_pins=1, retention_pins_valid=True)
    request = dict(source_id=primary, ranges=[dict(start_ms=1000, end_ms=2500)], format='mp4', quality='best',
                   cut_mode='accurate', output_mode='joined', audio_replace=dict(source_id=external, offset_ms=0))
    row = dict(id=job, owner=owner, source_id=primary, audio_source_id=external, request=request, status='failed')
    return dict(primary_source_id=primary, audio_source_id=external, job_id=job, owner=owner,
                source_row=source, job_row=row)


def synthetic_grants():
    return dict(role='cutmy_runtime', session_role='cutmy_runtime', schema_owner=False, schema_create=False,
                tables={table: dict(table_owner=False, select=True, insert=True, update=True, delete=True)
                        for table in ('sources', 'jobs')},
                checks={key: True for key in ('derived_reference', 'insert_pins', 'update_pins', 'delete_unpins', 'column_guard')})


def synthetic_render():
    return dict(source_id='new-primary', audio_source_id='new-audio', job_id='new-job', artifact_id='new-artifact',
                sha256='f' * 64, bytes=4096, duration_ms=1500, canvas=[180, 320], integrated_lufs=-16.0,
                true_peak_dbtp=-1.0, audio_frequency_hz=997.0, foreign_source_status=404, foreign_download_status=404, range_bytes=16)


class AudioFixture(existing.SyntheticCurrentReceipt):
    def __init__(self, directory):
        super().__init__(directory)
        report = self.report
        report.update(mode=audio.MODE, schema_version=7, runtime_application_sources_unchanged=False)
        report.setdefault('scope', {})['schema_changing_upgrade'] = True
        value = report.pop('current_installation')
        value.pop('baseline')
        admission = value['admission']
        admission.pop('schema_sources_unchanged')
        old_files = baseline_files()
        current_files = deepcopy(old_files)
        current_files['internal/app/synthetic-current.go'] = existing.blob(b'package app\n// independent009 runtime fixture\n')
        for name in (audio.SQL_NAME, audio.SHAPE_NAME):
            current_files[name] = existing.blob((ROOT.parent / name).read_bytes())
        old, now = report['images']['old'], report['images']['current']
        old.update(source_commit=audio.BASELINE, tree=audio.BASELINE_TREE,
                   version='v0.0.0-candidate.g' + audio.BASELINE)
        old['runtime_version'].update(commit=audio.BASELINE, version=old['version'])
        now['tree'] = existing.current.source_tree(current_files)
        report['checkout'].update(tree=now['tree'], head_tree=now['tree'])
        self.sources = {}
        for label, files in (('old', old_files), ('current', current_files)):
            image = report['images'][label]
            source = dict(commit=image['source_commit'], tree=image['tree'], files=files,
                          **{name: image[name] for name in ('version', 'build_time', 'toolchain')})
            self.sources[label] = source
            value[label + '_source'] = self.write(label, source)
            image['source_sha256'] = value[label + '_source']['sha256']
        report['images']['restored'] = deepcopy(now)
        for attempt in report['controlled_probe_attempts']:
            attempt['source_identity'] = {name: old[name] for name in ('source_commit', 'source_sha256', 'image_id')}
        report['baseline_tcp_readiness']['source_commit'] = audio.BASELINE
        admission.update(provider=deepcopy(report['provider']), checkout=deepcopy(report['checkout']),
                         baseline_commit=audio.BASELINE, baseline_tree=audio.BASELINE_TREE,
                         candidate_tree=now['tree'], schema_transition=[8, 9],
                         candidate_application_files=len(existing.current.application(self.sources['current'])),
                         candidate_application_sha256=proof.digest(proof.canonical(existing.current.application(self.sources['current']))))
        self.value = value
        report['external_audio_transition'] = value
        value.update(migrations=audio.migrations(), manifests={}, catalogs={}, guard_exit=1,
                     guard_code='schema_incompatible', reverse_method='fresh_restore_coordinated008_backup',
                     compatible_recovery_image_id=now['image_id'])
        shapes = {}
        for position in (8, 9):
            raw = (ROOT.parent / f'internal/app/migrations/{position:03d}_schema.json').read_bytes()
            shapes[position] = json.loads(raw)
            value['manifests'][str(position)] = self.raw('manifest-' + str(position), raw)
        for phase, position in audio.CATALOGS.items():
            value['catalogs'][phase] = self.raw('catalog-' + phase,
                dict(position=position, shape=shapes[position], context=admission, postgres_version_num=170011,
                     versions=sorted(m['version'] for m in value['migrations'][:position]),
                     migrations=value['migrations'][:position]))
        before = dict(sources=[dict(id='retained', owner='owner', kind='upload', path='/data/source', media_removed=False)],
                      jobs=[dict(id='old-job', source_id='retained', status='succeeded')],
                      artifacts=[dict(id='old-output', job_id='old-job', path='/data/output')],
                      storage_files=[dict(path='/data/source', size_bytes=10)], storage_reservations=[],
                      storage_counters=[dict(id=1, stored_bytes=10, reserved_bytes=0)],
                      source_media_cleanup=[], source_media_readers=[], source_metadata_cache=[],
                      source_metadata_resolutions=[], **{table: [] for table in audio.AUXILIARY_TABLES})
        after = deepcopy(before)
        for row in after['jobs']:
            row['audio_source_id'] = None
        value['originals_before'] = self.raw('originals-before', before)
        value['originals_after'] = self.raw('originals-after', after)
        value['rollback_originals'] = self.raw('rollback-originals', before)
        fixture = synthetic_fixture()
        value['audio_fixture'] = self.raw('audio-fixture', fixture)
        live = deepcopy(after)
        live['sources'].append(fixture['source_row'])
        live['jobs'].append(fixture['job_row'])
        for key in ('guard_before', 'guard_after', 'restore_before', 'restore_after', 'audio_after_startup'):
            value[key] = self.raw(key, live)
        files = {'/data/source': dict(bytes=10, sha256='a' * 64)}
        value['rollback_media'] = self.raw('rollback-media', dict(before=files, after=files,
            decoded_retained_outputs=[dict(sha256=c * 64) for c in 'abc']))
        report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence'] = {
            'results': [dict(sha256=c * 64) for c in 'abc']}
        value['runtime_grants'] = self.raw('runtime-grants', synthetic_grants())
        value['http_render'] = self.raw('http-render', synthetic_render())
        identity = report['groups']['identity']['checks']
        identity.pop('current_application_sources_bound')
        identity['audio_candidate_sources_bound'] = {'ok': True, 'evidence': {'schema_transition': [8, 9]}}
        report['groups']['external_audio_upgrade'] = {'status': 'pass',
            'checks': {name: {'ok': True} for name in audio.CHECKS}}

    def raw(self, name, value):
        data = value if isinstance(value, bytes) else proof.canonical(value)
        name = 'audio-' + name + '.json'
        (self.directory / name).write_bytes(data)
        return dict(path=name, bytes=len(data), sha256=proof.digest(data))

    def change_raw(self, key, change):
        value = proof.strict_json(proof.artifact(self.directory, self.value[key]))
        change(value)
        self.value[key] = self.raw(key + '-mutated', value)


class AudioReceiptControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)

    def fixture(self):
        return AudioFixture(self.temp.name)

    def test_exact009_accepts_full_source_catalogs_refusal_and_both_restores(self):
        self.fixture().validate()

    def test_partial_unknown_mode_schema_and_unchanged_claim_refuse(self):
        for fault in ('missing', 'extra', 'partial', 'mode', 'version', 'scope', 'unchanged'):
            with self.subTest(fault=fault):
                f = self.fixture()
                if fault == 'missing': f.report['groups'].pop('external_audio_upgrade')
                elif fault == 'extra': f.report['groups']['unreviewed'] = {'status': 'pass', 'checks': {}}
                elif fault == 'partial': f.report['groups']['external_audio_upgrade']['checks'].pop(audio.CHECKS[-1])
                elif fault == 'mode': f.report['mode'] = 'agent-projects-007-to-008'
                elif fault == 'version': f.report['schema_version'] = 6
                elif fault == 'scope': f.report['scope']['schema_changing_upgrade'] = False
                else: f.report['runtime_application_sources_unchanged'] = True
                with self.assertRaises(RuntimeError): f.validate()

    def test_source_inventory_baseline_candidate_and_exact009_pair_are_bound(self):
        for field, replacement in (('baseline_commit', '9' * 40), ('baseline_tree', '9' * 40),
                                   ('candidate_application_files', 1), ('candidate_application_sha256', '0' * 64),
                                   ('captured_before_image_builds', False)):
            f = self.fixture()
            f.value['admission'][field] = replacement
            with self.subTest(field=field), self.assertRaises(RuntimeError): f.validate()
        old = existing.current.application({'files': baseline_files()})
        current = deepcopy(old)
        for name in (audio.SQL_NAME, audio.SHAPE_NAME): current[name] = existing.blob((ROOT.parent / name).read_bytes())
        audio.validate_schema_inventory(old, current)
        for fault in ('old', 'missing', 'extra', 'sql', 'shape'):
            wrong = deepcopy(current)
            if fault == 'old': wrong['internal/app/migrations/008_schema.json']['sha256'] = '0' * 64
            elif fault == 'missing': wrong.pop(audio.SHAPE_NAME)
            elif fault == 'extra': wrong['internal/app/migrations/010_unreviewed.sql'] = existing.blob(b'SELECT 1;')
            else: wrong[audio.SQL_NAME if fault == 'sql' else audio.SHAPE_NAME]['sha256'] = '0' * 64
            with self.subTest(fault=fault), self.assertRaises(RuntimeError): audio.validate_schema_inventory(old, wrong)

    def test_additive_nullable_column_cannot_hide_changed_legacy_rows(self):
        for change in (lambda v: v['jobs'][0].update(audio_source_id='backfilled'),
                       lambda v: v['jobs'][0].update(status='failed'),
                       lambda v: v['sources'][0].update(media_removed=True),
                       lambda v: v['artifacts'].clear(), lambda v: v.update(unreviewed_table=[]),
                       lambda v: v['storage_counters'][0].update(stored_bytes=0)):
            f = self.fixture(); f.change_raw('originals_after', change)
            with self.assertRaises(RuntimeError): f.validate()

    def test_guard_and_both_restores_require_identical_complete_raw_rows(self):
        for key in ('guard_after', 'restore_after', 'rollback_originals'):
            f = self.fixture(); f.change_raw(key, lambda v: v['jobs'][0].update(status='cancelled'))
            with self.subTest(key=key), self.assertRaises(RuntimeError): f.validate()
        for fault in ('method', 'image', 'guard', 'private', 'manifest', 'files'):
            f = self.fixture()
            if fault == 'method': f.value['reverse_method'] = 'remove_version_markers'
            elif fault == 'image': f.value['compatible_recovery_image_id'] = 'sha256:' + '0' * 64
            elif fault == 'guard': f.value['guard_exit'] = 0
            elif fault == 'private': f.report['cleanup']['private_workspace_retained'] = True
            elif fault == 'manifest': (f.directory / f.value['manifests']['9']['path']).write_bytes(b'{}')
            else: f.change_raw('rollback_media', lambda v: v['after'].clear())
            with self.subTest(fault=fault), self.assertRaises(RuntimeError): f.validate()

    def test_actual_catalog_phase_and_admission_context_cannot_be_substituted(self):
        for fault in ('shape', 'position', 'checksum', 'context', 'missing'):
            f = self.fixture()
            if fault == 'missing': f.value['catalogs'].pop('guard009_after')
            else:
                row = proof.strict_json(proof.artifact(f.directory, f.value['catalogs']['current009']))
                if fault == 'shape': row['shape']['Tables']['jobs']['Columns'].pop('audio_source_id')
                elif fault == 'position': row['position'] = 8
                elif fault == 'checksum': row['migrations'][-1]['checksum'] = '0' * 64
                else: row['context']['candidate_commit'] = '9' * 40
                f.value['catalogs']['current009'] = f.raw('wrong-catalog', row)
            with self.subTest(fault=fault), self.assertRaises(RuntimeError): f.validate()

    def test_role_privileges_and_each_actual_pin_crud_guard_are_required(self):
        for key in synthetic_grants()['checks']:
            f = self.fixture(); f.change_raw('runtime_grants', lambda v: v['checks'].update({key: False}))
            with self.subTest(key=key), self.assertRaises(RuntimeError): f.validate()
        for table in ('sources', 'jobs'):
            for key in synthetic_grants()['tables'][table]:
                f = self.fixture(); f.change_raw('runtime_grants', lambda v: v['tables'][table].update({key: key == 'table_owner'}))
                with self.subTest(table=table, key=key), self.assertRaises(RuntimeError): f.validate()

    def test_restored_external_reference_request_and_truthful_pins_are_required(self):
        for phase in ('restore_before', 'restore_after', 'audio_after_startup'):
            for change in (lambda v: v['jobs'].pop(), lambda v: v['sources'].pop(),
                           lambda v: v['jobs'][-1].update(audio_source_id=None),
                           lambda v: v['sources'][-1].update(retention_job_pins=0)):
                f = self.fixture(); f.change_raw(phase, change)
                with self.subTest(phase=phase), self.assertRaises(RuntimeError): f.validate()
        f = self.fixture()
        f.change_raw('audio_fixture', lambda v: v['job_row']['request']['audio_replace'].update(offset_ms=123))
        for phase in ('restore_before', 'restore_after', 'audio_after_startup'):
            f.change_raw(phase, lambda v: v['jobs'][-1]['request']['audio_replace'].update(offset_ms=123))
        with self.assertRaises(RuntimeError): f.validate()

    def test_actual_https_worker_geometry_audio_and_owner_gates_cannot_be_omitted(self):
        for key, value in (('canvas', [160, 96]), ('duration_ms', 300), ('integrated_lufs', -30.0),
                           ('true_peak_dbtp', 1.0), ('audio_frequency_hz', 660.0), ('foreign_source_status', 202),
                           ('foreign_download_status', 200), ('range_bytes', 0), ('sha256', 'invalid')):
            f = self.fixture(); f.change_raw('http_render', lambda v: v.update({key: value}))
            with self.subTest(key=key), self.assertRaises(RuntimeError): f.validate()


class AudioAdmissionControls(unittest.TestCase):
    def test_reverse_restore_is_inherited_tls_route_before_host_start(self):
        # Exercise the adapter against the base driver's public hook: a third
        # restore domain must receive a matching SAN and Caddy stanza.
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / 'internal/app/migrations').mkdir(parents=True)
            for position in (8, 9):
                target = f'internal/app/migrations/{position:03d}_schema.json'
                (output / target).write_bytes((ROOT.parent / target).read_bytes())
            def initialize(value):
                value.old, value.restored = {'name': 'old'}, {'name': 'restored'}
                value.old_repo = value.current_repo = output
            fake = SimpleNamespace(Acceptance=type('FakeBase', (), {'initialize_host': initialize}),
                                   REPO=output, transition_module=lambda: proof,
                                   current_installation_module=lambda: existing.current,
                                   source_media_cleanup_module=lambda: base.source_media_cleanup_module())
            cls = audio.acceptance_class(fake)
            value = cls.__new__(cls)
            value.args = SimpleNamespace(revision='1' * 40)
            value.report = dict(provider={}, checkout=dict(tree='2' * 40), external_audio_transition=dict(catalogs={}, manifests={}))
            value.git = Mock(return_value=audio.BASELINE_TREE)
            value.run = Mock()
            value.make_project = Mock(return_value={'name': 'rollback008'})
            value.save = Mock()
            value.output = output
            files = existing.current.application({'files': baseline_files()})
            with patch.object(proof, 'application_inventory', return_value=files), patch.object(audio, 'validate_schema_inventory'):
                value.initialize_host()
            self.assertEqual(value.tls_projects, (value.old, value.restored, value.rollback))
            self.assertEqual([p['name'] for p in value.tls_projects], ['old', 'restored', 'rollback008'])

    def test_wrong_mode_refuses_before_parent_or_host_initialization(self):
        init = Mock()
        fake = SimpleNamespace(Acceptance=type('FakeBase', (), {'__init__': init}),
                               transition_module=lambda: proof, current_installation_module=lambda: existing.current,
                               source_media_cleanup_module=lambda: base.source_media_cleanup_module())
        cls = audio.acceptance_class(fake)
        for mode in (None, 'same-schema', 'agent-projects-007-to-008', 'current-installation'):
            with self.assertRaises(RuntimeError): cls(SimpleNamespace(mode=mode))
        init.assert_not_called()

    def test_new_default_and_exact_receipt_leave_old008_profile_pinned(self):
        args = base.parse_args(['--mode', audio.MODE, '--revision', '1' * 40, '--output', '/synthetic'])
        self.assertEqual(args.upgrade_from, audio.BASELINE)
        old = base.parse_args(['--mode', 'agent-projects-007-to-008', '--revision', '1' * 40, '--output', '/synthetic'])
        self.assertEqual(old.upgrade_from, base.agent_projects_module().BASELINE)
        workflow = (ROOT.parent / '.github/workflows/self-hosting.yml').read_text()
        self.assertIn('default: ' + audio.MODE, workflow)
        self.assertEqual(workflow.count("github.event_name != 'workflow_dispatch' && '" + audio.MODE + "'"), 2)
        for value in ('external_audio_transition_test.py', 'agent_projects_transition_test.py',
                      'public-audio-controls.log', '--verify-report "$RUNNER_TEMP/public-self-hosting-evidence/acceptance-report.json"'):
            self.assertIn(value, workflow)


if __name__ == '__main__':
    unittest.main()
