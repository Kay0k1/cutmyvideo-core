"""Independent008 admission and receipt negatives; no host mutation or Docker."""
from copy import deepcopy
import importlib.util
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock

ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


projects = module('projects_controls', 'agent-projects-transition.py')
existing = module('projects_current_fixture', 'current_installation_acceptance_test.py')
base, proof = existing.base, existing.proof


def baseline_files():
    value = proof.strict_json((ROOT / 'testdata/selfhosting-agent-projects-baseline-source.txt').read_bytes())
    if existing.current.source_tree(value) != projects.BASELINE_TREE:
        raise AssertionError('immutable007 baseline tree fixture changed')
    inventory = existing.current.application({'files': value})
    if len(inventory) != projects.BASELINE_FILES or proof.digest(proof.canonical(inventory)) != projects.BASELINE_APPLICATION_SHA256:
        raise AssertionError('immutable007 application fixture changed')
    return value


def synthetic_fixture():
    source, owner, project, job = 'acceptance_projects_synthetic001', 'acceptance_projects_owner_synthetic001', 'acceptance_project_synthetic001', 'acceptance_project_job_synthetic001'
    fingerprint = dict(kind='provider', canonical_url_sha256='c' * 64)
    rows = {
        'source_preparations': [dict(owner=owner, key=projects.PREPARATION_KEY, intent_hash='a' * 64, kind='import', source_id=source, state='ready', fingerprint=fingerprint)],
        'source_fingerprints': [dict(source_id=source, owner=owner, fingerprint=fingerprint)],
        'agent_projects': [dict(id=project, owner=owner, source_id=source, revision=1, rendition='final', collection_key=projects.COLLECTION_KEY, plan=dict(version=1, clips=[dict(id='opening', ranges=[dict(start_ms=1000, end_ms=2000)])]))],
        'agent_project_jobs': [dict(project_id=project, job_id=job, clip_id='opening')],
        'agent_project_handoffs': [dict(token_hash=projects.TOKEN_HASH, project_id=project, recipient='acceptance-recipient-008', redeemed_at='2026-10-16T00:00:00Z', expires_at='2026-10-17T00:00:00Z')],
        'agent_project_grants': [dict(project_id=project, recipient='acceptance-recipient-008', expires_at='2026-10-17T00:00:00Z')],
        'agent_project_retries': [dict(project_id=project, key=projects.RETRY_KEY, intent_hash='d' * 64, request=dict(revision=1, clip_ids=['opening']), attempt_key=projects.ATTEMPT_KEY, clip_intents={'opening': 'e' * 64}, clip_job_keys={'opening': projects.ATTEMPT_KEY})],
    }
    return dict(source_id=source, owner=owner, project_id=project, job_id=job, rows=rows)


class ProjectFixture(existing.SyntheticCurrentReceipt):
    def __init__(self, directory):
        super().__init__(directory)
        report = self.report
        report.update(mode=projects.MODE, schema_version=6, runtime_application_sources_unchanged=False)
        report.setdefault('scope', {})['schema_changing_upgrade'] = True
        value = report.pop('current_installation'); value.pop('baseline')
        admission = value['admission']; admission.pop('schema_sources_unchanged')
        old_files = baseline_files(); current_files = deepcopy(old_files)
        current_files['internal/app/synthetic-current.go'] = existing.blob(b'package app\n// synthetic008 runtime change is admitted and bound\n')
        for name in (projects.SQL_NAME, projects.SHAPE_NAME):
            current_files[name] = existing.blob((ROOT.parent / name).read_bytes())
        old, now = report['images']['old'], report['images']['current']
        old.update(source_commit=projects.BASELINE, tree=projects.BASELINE_TREE, version='v0.0.0-candidate.g' + projects.BASELINE)
        old['runtime_version'].update(commit=projects.BASELINE, version=old['version'])
        now['tree'] = existing.current.source_tree(current_files)
        report['checkout'].update(tree=now['tree'], head_tree=now['tree'])
        self.sources = {}
        for label, files in (('old', old_files), ('current', current_files)):
            image = report['images'][label]
            source = dict(commit=image['source_commit'], tree=image['tree'], files=files,
                          **{name: image[name] for name in ('version', 'build_time', 'toolchain')})
            self.sources[label] = source; value[label + '_source'] = self.write(label, source)
            image['source_sha256'] = value[label + '_source']['sha256']
        report['images']['restored'] = deepcopy(now)
        for attempt in report['controlled_probe_attempts']:
            attempt['source_identity'] = {name: old[name] for name in ('source_commit', 'source_sha256', 'image_id')}
        report['baseline_tcp_readiness']['source_commit'] = projects.BASELINE
        admission.update(provider=deepcopy(report['provider']), checkout=deepcopy(report['checkout']), baseline_commit=projects.BASELINE, baseline_tree=projects.BASELINE_TREE, candidate_tree=now['tree'], schema_transition=[7, 8], candidate_application_files=len(existing.current.application(self.sources['current'])), candidate_application_sha256=proof.digest(proof.canonical(existing.current.application(self.sources['current']))))
        self.value = value; report['agent_projects_transition'] = value
        value.update(migrations=projects.migrations(), manifests={}, catalogs={}, guard_exit=1, guard_code='schema_incompatible', reverse_method='fresh_restore_coordinated007_backup', compatible_recovery_image_id=now['image_id'])
        shapes = {}
        for position in (7, 8):
            raw = (ROOT.parent / f'internal/app/migrations/{position:03d}_schema.json').read_bytes(); shapes[position] = json.loads(raw)
            value['manifests'][str(position)] = self.raw('manifest-' + str(position), raw)
        for phase, position in projects.CATALOGS.items():
            value['catalogs'][phase] = self.raw('catalog-' + phase, dict(position=position, shape=shapes[position], context=admission, postgres_version_num=170011, versions=sorted(m['version'] for m in value['migrations'][:position]), migrations=value['migrations'][:position]))
        before = {'sources': [dict(id='retained', owner='owner', kind='upload', path='/data/source', media_removed=False)], 'jobs': [dict(id='job', source_id='retained', status='succeeded')], 'artifacts': [dict(id='output', job_id='job', path='/data/output')], 'storage_files': [dict(path='/data/source', size_bytes=10)], 'storage_reservations': [], 'storage_counters': [dict(id=1, stored_bytes=10, reserved_bytes=0)], 'source_media_cleanup': [], 'source_media_readers': [], 'source_metadata_cache': [], 'source_metadata_resolutions': []}
        after = dict(deepcopy(before), **{table: [] for table in projects.NEW_TABLES})
        value['originals_before'] = self.raw('originals-before', before); value['originals_after'] = self.raw('originals-after', after); value['rollback_originals'] = self.raw('rollback-originals', before)
        fixture = synthetic_fixture(); value['project_fixture'] = self.raw('synthetic-fixture', fixture)
        live = deepcopy(after); live.update(deepcopy(fixture['rows']))
        live['sources'].append(dict(id=fixture['source_id'], owner=fixture['owner'], kind='platform', path=''))
        live['jobs'].append(dict(id=fixture['job_id'], source_id=fixture['source_id'], owner=fixture['owner'], status='failed'))
        for key in ('guard_before', 'guard_after', 'restore_before', 'restore_after', 'project_after_startup'):
            value[key] = self.raw(key, live)
        files = {'/data/source': dict(bytes=10, sha256='a' * 64)}
        value['rollback_media'] = self.raw('rollback-media', dict(before=files, after=files, decoded_retained_outputs=[dict(sha256=c * 64) for c in 'abc']))
        report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence'] = {'results': [dict(sha256=c * 64) for c in 'abc']}
        value['runtime_grants'] = self.raw('runtime-grants', dict(role='cutmy_runtime', session_role='cutmy_runtime', schema_owner=False, schema_create=False, tables={table: dict(table_owner=False, select=True, insert=True, update=True, delete=True, crud_assertions=True, retained_rows=1) for table in projects.NEW_TABLES}))
        identity = report['groups']['identity']['checks']; identity.pop('current_application_sources_bound')
        identity['projects_candidate_sources_bound'] = {'ok': True, 'evidence': {'schema_transition': [7, 8]}}
        report['groups']['agent_projects_upgrade'] = {'status': 'pass', 'checks': {name: {'ok': True} for name in projects.CHECKS}}

    def raw(self, name, value):
        data = value if isinstance(value, bytes) else proof.canonical(value); name = 'projects-' + name + '.json'
        (self.directory / name).write_bytes(data)
        return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

    def change_raw(self, key, change):
        value = proof.strict_json(proof.artifact(self.directory, self.value[key])); change(value)
        self.value[key] = self.raw(key + '-mutated', value)


class ProjectReceiptControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.fixture = ProjectFixture(self.temp.name)

    def test_exact008_accepts_changed_runtime_full_sources_catalog_roles_and_restore(self):
        self.fixture.validate()

    def test_missing_extra_partial_historical_or_unchanged_claim_refused(self):
        for fault in ('missing', 'extra', 'partial', 'mode', 'version', 'scope', 'unchanged'):
            with self.subTest(fault=fault):
                f = ProjectFixture(self.temp.name)
                if fault == 'missing': f.report['groups'].pop('agent_projects_upgrade')
                elif fault == 'extra': f.report['groups']['unreviewed'] = {'status': 'pass', 'checks': {}}
                elif fault == 'partial': f.report['groups']['agent_projects_upgrade']['checks'].pop(projects.CHECKS[-1])
                elif fault == 'mode': f.report['mode'] = 'metadata-resolution-006-to-007'
                elif fault == 'scope': f.report['scope']['schema_changing_upgrade'] = False
                elif fault == 'unchanged': f.report['runtime_application_sources_unchanged'] = True
                else: f.report['schema_version'] = 5
                with self.assertRaises(RuntimeError): f.validate()

    def test_changed_runtime_inventory_is_complete_and_bound_before_builds(self):
        for field, replacement in (('baseline_commit', '9' * 40), ('baseline_tree', '9' * 40),
                                   ('candidate_application_files', 1), ('candidate_application_sha256', '0' * 64),
                                   ('captured_before_image_builds', False)):
            with self.subTest(field=field):
                f = ProjectFixture(self.temp.name)
                f.value['admission'][field] = replacement
                with self.assertRaises(RuntimeError): f.validate()
        f = ProjectFixture(self.temp.name)
        source = deepcopy(f.sources['current'])
        source['files']['internal/app/synthetic-current.go']['sha256'] = '0' * 64
        f.value['current_source'] = f.raw('changed-runtime-source', source)
        f.report['images']['current']['source_sha256'] = f.value['current_source']['sha256']
        f.report['images']['restored']['source_sha256'] = f.value['current_source']['sha256']
        with self.assertRaises(RuntimeError): f.validate()

    def test_every_new_table_is_empty_at_migration_and_every_old_row_preserved(self):
        changes = [lambda v: v['sources'][0].update(media_removed=True), lambda v: v['jobs'][0].update(status='failed'), lambda v: v['artifacts'].clear(), lambda v: v['storage_counters'][0].update(stored_bytes=0), lambda v: v.update(unreviewed_table=[])]
        changes += [lambda v, table=table: v[table].append({'unexpected': True}) for table in projects.NEW_TABLES]
        for change in changes:
            with self.subTest(change=change):
                f = ProjectFixture(self.temp.name); f.change_raw('originals_after', change)
                with self.assertRaises(RuntimeError): f.validate()

    def test_guard_and_both_restore_directions_require_unchanged_raw_rows(self):
        for key in ('guard_after', 'restore_after', 'rollback_originals'):
            with self.subTest(key=key):
                f = ProjectFixture(self.temp.name); f.change_raw(key, lambda v: v['jobs'][0].update(status='failed'))
                with self.assertRaises(RuntimeError): f.validate()

    def test_catalog_shape_position_checksum_and_candidate_context_are_exact(self):
        for fault in ('shape', 'position', 'checksum', 'context', 'missing'):
            with self.subTest(fault=fault):
                f = ProjectFixture(self.temp.name)
                if fault == 'missing': f.value['catalogs'].pop('guard008_after')
                else:
                    row = proof.strict_json(proof.artifact(f.directory, f.value['catalogs']['current008']))
                    if fault == 'shape': row['shape']['Tables'].pop('agent_project_retries')
                    elif fault == 'position': row['position'] = 7
                    elif fault == 'checksum': row['migrations'][-1]['checksum'] = '0' * 64
                    else: row['context']['candidate_commit'] = '9' * 40
                    f.value['catalogs']['current008'] = f.raw('wrong-catalog', row)
                with self.assertRaises(RuntimeError): f.validate()

    def test_all_seven_runtime_tables_require_actual_crud_and_no_ownership(self):
        for table in projects.NEW_TABLES:
            for key in ('table_owner', 'select', 'insert', 'update', 'delete', 'crud_assertions', 'retained_rows'):
                with self.subTest(table=table, key=key):
                    f = ProjectFixture(self.temp.name)
                    f.change_raw('runtime_grants', lambda v: v['tables'][table].update({key: 0 if key == 'retained_rows' else not v['tables'][table][key]}))
                    with self.assertRaises(RuntimeError): f.validate()
        for field in ('role', 'session_role', 'schema_owner', 'schema_create'):
            f = ProjectFixture(self.temp.name); f.change_raw('runtime_grants', lambda v: v.update({field: 'cutmy_admin' if 'role' in field else True}))
            with self.assertRaises(RuntimeError): f.validate()
        f = ProjectFixture(self.temp.name); f.change_raw('runtime_grants', lambda v: v['tables']['agent_projects'].update(retained_rows=True))
        with self.assertRaises(RuntimeError): f.validate()

    def test_each_preparation_project_attachment_handoff_grant_and_retry_row_is_retained(self):
        for table in projects.NEW_TABLES:
            for phase in ('restore_before', 'restore_after', 'project_after_startup'):
                with self.subTest(table=table, phase=phase):
                    f = ProjectFixture(self.temp.name); f.change_raw(phase, lambda v: v[table].clear())
                    with self.assertRaises(RuntimeError): f.validate()
        for table, key, replacement in (('source_preparations', 'state', 'processing'), ('source_fingerprints', 'owner', 'foreign'), ('agent_projects', 'revision', 2), ('agent_project_jobs', 'clip_id', 'other'), ('agent_project_handoffs', 'recipient', 'other'), ('agent_project_grants', 'recipient', 'other'), ('agent_project_retries', 'attempt_key', 'other')):
            f = ProjectFixture(self.temp.name)
            f.change_raw('project_fixture', lambda v: v['rows'][table][0].update({key: replacement}))
            for phase in ('restore_before', 'restore_after', 'project_after_startup'):
                f.change_raw(phase, lambda v: v[table][0].update({key: replacement}))
            with self.assertRaises(RuntimeError): f.validate()

    def test_fresh_restored_upload_preserves_its_own_fingerprint_and_retained_identity(self):
        def fresh(value):
            value['source_fingerprints'].append(dict(source_id='new-https-upload', owner='other-https-owner',
                                                    fingerprint=dict(kind='content', sha256='f' * 64, size_bytes=4096)))
        f = ProjectFixture(self.temp.name)
        f.change_raw('project_after_startup', fresh)
        f.validate()
        for fault in ('missing', 'owner', 'fingerprint', 'duplicate'):
            with self.subTest(fault=fault):
                f = ProjectFixture(self.temp.name)
                f.change_raw('project_after_startup', fresh)
                def corrupt(value):
                    rows = value['source_fingerprints']
                    if fault == 'missing': rows.pop(0)
                    elif fault == 'owner': rows[0]['owner'] = 'other-https-owner'
                    elif fault == 'fingerprint': rows[0]['fingerprint']['canonical_url_sha256'] = '0' * 64
                    else: rows.append(deepcopy(rows[0]))
                f.change_raw('project_after_startup', corrupt)
                with self.assertRaises(RuntimeError): f.validate()

    def test_reverse_media_guard_image_private_cleanup_and_raw_integrity_required(self):
        for fault in ('files', 'decoded', 'method', 'image', 'guard', 'private', 'manifest'):
            with self.subTest(fault=fault):
                f = ProjectFixture(self.temp.name)
                if fault == 'files': f.change_raw('rollback_media', lambda v: v['after'].clear())
                elif fault == 'decoded': f.change_raw('rollback_media', lambda v: v['decoded_retained_outputs'].pop())
                elif fault == 'method': f.value['reverse_method'] = 'remove_version_markers'
                elif fault == 'image': f.value['compatible_recovery_image_id'] = 'sha256:' + '0' * 64
                elif fault == 'guard': f.value['guard_exit'] = 0
                elif fault == 'private': f.report['cleanup']['private_workspace_retained'] = True
                else: (f.directory / f.value['manifests']['8']['path']).write_bytes(b'{}')
                with self.assertRaises(RuntimeError): f.validate()


class ProjectAdmissionControls(unittest.TestCase):
    def test_wrong_mode_never_initializes_parent_or_host(self):
        init = Mock()
        fake = SimpleNamespace(Acceptance=type('FakeBase', (), {'__init__': init}), transition_module=lambda: proof,
                               current_installation_module=lambda: existing.current,
                               source_media_cleanup_module=lambda: base.source_media_cleanup_module())
        value = projects.acceptance_class(fake)
        for mode in (None, 'same-schema', 'metadata-resolution-006-to-007', 'current-installation'):
            with self.assertRaises(RuntimeError): value(SimpleNamespace(mode=mode))
        init.assert_not_called()

    def test_mode_pins_published007_and_only008_pair_preserving_historical_modes(self):
        args = base.parse_args(['--mode', projects.MODE, '--revision', '1' * 40, '--output', '/synthetic-output'])
        self.assertEqual(args.upgrade_from, projects.BASELINE)
        self.assertEqual(base.parse_args(['--mode', 'metadata-resolution-006-to-007', '--revision', '1' * 40, '--output', '/synthetic-output']).upgrade_from, base.metadata_resolution_module().BASELINE)
        old = existing.current.application({'files': baseline_files()}); candidate = deepcopy(old)
        for name in (projects.SQL_NAME, projects.SHAPE_NAME): candidate[name] = existing.blob((ROOT.parent / name).read_bytes())
        candidate['internal/app/new-release-runtime.go'] = existing.blob(b'package app\n')
        projects.validate_schema_inventory(old, candidate)
        for fault in ('extra', 'unversioned', 'old', 'sql', 'shape', 'missing'):
            wrong = deepcopy(candidate)
            if fault == 'extra': wrong['internal/app/migrations/009_future.sql'] = existing.blob(b'SELECT 1;')
            elif fault == 'unversioned': wrong['internal/app/migrations/unreviewed.sql'] = existing.blob(b'SELECT 1;')
            elif fault == 'old': wrong['internal/app/migrations/007_schema.json']['sha256'] = '0' * 64
            elif fault == 'sql': wrong[projects.SQL_NAME]['sha256'] = '0' * 64
            elif fault == 'shape': wrong[projects.SHAPE_NAME]['sha256'] = '0' * 64
            else: wrong.pop(projects.SHAPE_NAME)
            with self.subTest(fault=fault), self.assertRaises(RuntimeError): projects.validate_schema_inventory(old, wrong)

    def test_workflow_requires_new_default_mode_controls_and_exact_receipt_verification(self):
        workflow = (ROOT.parent / '.github/workflows/self-hosting.yml').read_text()
        self.assertIn('default: ' + projects.MODE, workflow)
        self.assertEqual(workflow.count("github.event_name != 'workflow_dispatch' && '" + projects.MODE + "'"), 2)
        self.assertIn('agent_projects_transition_test.py', workflow)
        self.assertIn('public-projects-controls.log', workflow)
        self.assertIn('--verify-report "$RUNNER_TEMP/public-self-hosting-evidence/acceptance-report.json"', workflow)
        self.assertIn('metadata_resolution_transition_test.py', workflow)
        self.assertIn('timeout-minutes: 55', workflow)


if __name__ == '__main__':
    unittest.main()
