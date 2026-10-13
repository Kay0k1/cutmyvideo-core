"""Synthetic007 admission/receipt controls; no Docker, Git, media or database."""
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


metadata = module('metadata_controls', 'metadata-resolution-transition.py')
existing = module('metadata_current_fixture', 'current_installation_acceptance_test.py')
base, proof = existing.base, existing.proof


def baseline_files():
    value = proof.strict_json((ROOT / 'testdata/selfhosting-metadata-baseline-source.txt').read_bytes())
    if existing.current.source_tree(value) != metadata.BASELINE_TREE:
        raise AssertionError('immutable006 baseline tree fixture changed')
    inventory = existing.current.application({'files': value})
    if len(inventory) != metadata.BASELINE_FILES or proof.digest(proof.canonical(inventory)) != metadata.BASELINE_APPLICATION_SHA256:
        raise AssertionError('immutable006 application fixture changed')
    return value


class MetadataFixture(existing.SyntheticCurrentReceipt):
    def __init__(self, directory):
        super().__init__(directory)
        report = self.report
        report.update(mode=metadata.MODE, schema_version=5, runtime_application_sources_unchanged=False)
        report.setdefault('scope', {})['schema_changing_upgrade'] = True
        value = report.pop('current_installation')
        value.pop('baseline')
        admission = value['admission']
        admission.pop('schema_sources_unchanged')
        old_files = baseline_files()
        current_files = deepcopy(old_files)
        current_files['internal/app/synthetic-current.go'] = existing.blob(b'package app\n// synthetic007 control only\n')
        for name in (metadata.SQL_NAME, metadata.SHAPE_NAME):
            current_files[name] = existing.blob((ROOT.parent / name).read_bytes())
        old, now = report['images']['old'], report['images']['current']
        old.update(source_commit=metadata.BASELINE, tree=metadata.BASELINE_TREE,
                   version='v0.0.0-candidate.g' + metadata.BASELINE)
        old['runtime_version'].update(commit=metadata.BASELINE, version=old['version'])
        now['tree'] = existing.current.source_tree(current_files)
        report['checkout'].update(tree=now['tree'], head_tree=now['tree'])
        self.sources = {}
        for label, files in (('old', old_files), ('current', current_files)):
            image = report['images'][label]
            source = {'commit': image['source_commit'], 'tree': image['tree'], 'files': files,
                      **{name: image[name] for name in ('version', 'build_time', 'toolchain')}}
            self.sources[label] = source
            value[label + '_source'] = self.write(label, source)
            image['source_sha256'] = value[label + '_source']['sha256']
        report['images']['restored'] = deepcopy(now)
        for attempt in report['controlled_probe_attempts']:
            attempt['source_identity'] = {name: old[name] for name in ('source_commit', 'source_sha256', 'image_id')}
        report['baseline_tcp_readiness']['source_commit'] = metadata.BASELINE
        admission.update(provider=deepcopy(report['provider']), checkout=deepcopy(report['checkout']),
                         baseline_commit=metadata.BASELINE, baseline_tree=metadata.BASELINE_TREE,
                         candidate_tree=now['tree'], schema_transition=[6, 7],
                         candidate_application_files=len(existing.current.application(self.sources['current'])),
                         candidate_application_sha256=proof.digest(proof.canonical(existing.current.application(self.sources['current']))))
        self.value = value
        report['metadata_resolution_transition'] = value
        value.update(migrations=metadata.migrations(), manifests={}, catalogs={}, guard_exit=1,
                     guard_code='schema_incompatible', reverse_method='fresh_restore_coordinated006_backup',
                     compatible_recovery_image_id=now['image_id'])
        shapes = {}
        for position in (6, 7):
            raw = (ROOT.parent / f'internal/app/migrations/{position:03d}_schema.json').read_bytes()
            shapes[position] = json.loads(raw)
            value['manifests'][str(position)] = self.raw('manifest-' + str(position), raw)
        for phase, position in metadata.CATALOGS.items():
            value['catalogs'][phase] = self.raw('catalog-' + phase,
                dict(position=position, shape=shapes[position], context=admission, postgres_version_num=170011,
                     versions=sorted(m['version'] for m in value['migrations'][:position]),
                     migrations=value['migrations'][:position]))
        before = {'sources': [dict(id='retained', owner='owner', kind='upload', path='/data/source', media_removed=False)],
                  'jobs': [dict(id='job', source_id='retained', status='succeeded')],
                  'artifacts': [dict(id='output', job_id='job', path='/data/output')],
                  'storage_files': [dict(path='/data/source', size_bytes=10)],
                  'storage_reservations': [], 'storage_counters': [dict(id=1, stored_bytes=10, reserved_bytes=0)],
                  'source_media_cleanup': [], 'source_media_readers': [], 'source_metadata_cache': []}
        after = dict(deepcopy(before), source_metadata_resolutions=[])
        value['originals_before'] = self.raw('originals-before', before)
        value['originals_after'] = self.raw('originals-after', after)
        value['rollback_originals'] = self.raw('rollback-originals', before)
        fixture = dict(source_id='acceptance_metadata_synthetic001', owner='acceptance_metadata_owner_synthetic001',
                       token=metadata.TOKEN, payload_hex=metadata.PAYLOAD_HEX,
                       cache_expires_at='2026-10-15T01:00:00+00:00', lease_expires_at='2026-10-14T01:03:00+00:00')
        value['metadata_fixture'] = self.raw('synthetic-fixture', fixture)
        live = deepcopy(after)
        live['sources'].append(dict(id=fixture['source_id'], owner=fixture['owner'], kind='platform', path=''))
        live['source_metadata_cache'].append(dict(source_id=fixture['source_id'], owner=fixture['owner'],
            payload='\\x' + metadata.PAYLOAD_HEX, expires_at=fixture['cache_expires_at']))
        live['source_metadata_resolutions'].append(dict(source_id=fixture['source_id'], owner=fixture['owner'],
            token=metadata.TOKEN, expires_at=fixture['lease_expires_at']))
        for key in ('guard_before', 'guard_after', 'restore_before', 'restore_after', 'metadata_after_startup'):
            value[key] = self.raw(key, live)
        files = {'/data/source': dict(bytes=10, sha256='a' * 64)}
        value['rollback_media'] = self.raw('rollback-media', dict(before=files, after=files,
            decoded_retained_outputs=[dict(sha256=c * 64) for c in 'abc']))
        report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence'] = {
            'results': [dict(sha256=c * 64) for c in 'abc']}
        value['runtime_grants'] = self.raw('runtime-grants',
            dict(role='cutmy_runtime', session_role='cutmy_runtime', schema_owner=False, table_owner=False,
                 schema_create=False, select=True, insert=True, update=True, delete=True,
                 crud_assertions=True, retained_cache_rows=1, retained_lease_rows=1))
        identity = report['groups']['identity']['checks']
        identity.pop('current_application_sources_bound')
        identity['metadata_candidate_sources_bound'] = {'ok': True, 'evidence': {'schema_transition': [6, 7]}}
        report['groups']['metadata_resolution_upgrade'] = {'status': 'pass', 'checks': {name: {'ok': True} for name in metadata.CHECKS}}

    def raw(self, name, value):
        data = value if isinstance(value, bytes) else proof.canonical(value)
        name = 'metadata-' + name + '.json'
        (self.directory / name).write_bytes(data)
        return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

    def change_raw(self, key, change):
        value = proof.strict_json(proof.artifact(self.directory, self.value[key]))
        change(value)
        self.value[key] = self.raw(key + '-mutated', value)


class MetadataReceiptControls(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.fixture = MetadataFixture(self.temp.name)

    def test_explicit007_accepts_complete_exact_source_catalog_role_and_restore_evidence(self):
        self.fixture.validate()

    def test_missing_extra_partial_or_historical_mode_cannot_claim007(self):
        for fault in ('missing', 'extra', 'partial', 'mode', 'version', 'scope'):
            with self.subTest(fault=fault):
                f = MetadataFixture(self.temp.name)
                if fault == 'missing': f.report['groups'].pop('metadata_resolution_upgrade')
                elif fault == 'extra': f.report['groups']['unreviewed'] = {'status': 'pass', 'checks': {}}
                elif fault == 'partial': f.report['groups']['metadata_resolution_upgrade']['checks'].pop(metadata.CHECKS[-1])
                elif fault == 'mode': f.report['mode'] = 'source-media-cleanup-005-to-006'
                elif fault == 'scope': f.report['scope']['schema_changing_upgrade'] = False
                else: f.report['schema_version'] = 4
                with self.assertRaises(RuntimeError): f.validate()

    def test_existing_rows_and_new_empty_table_are_preserved_at_migration(self):
        changes = (lambda v: v['sources'][0].update(media_removed=True), lambda v: v['jobs'][0].update(status='failed'),
                   lambda v: v['artifacts'].clear(), lambda v: v['storage_counters'][0].update(stored_bytes=0),
                   lambda v: v['source_metadata_resolutions'].append({'source_id': 'unexpected'}),
                   lambda v: v.update(unreviewed_table=[]))
        for change in changes:
            with self.subTest(change=change):
                f = MetadataFixture(self.temp.name); f.change_raw('originals_after', change)
                with self.assertRaises(RuntimeError): f.validate()

    def test_old_guard_and_both_coordinated_restore_directions_require_unchanged_raw_records(self):
        for key in ('guard_after', 'restore_after', 'rollback_originals'):
            with self.subTest(key=key):
                f = MetadataFixture(self.temp.name); f.change_raw(key, lambda v: v['jobs'][0].update(status='failed'))
                with self.assertRaises(RuntimeError): f.validate()

    def test_catalog_shape_position_checksum_and_candidate_context_are_exact(self):
        for fault in ('shape', 'position', 'checksum', 'context', 'missing'):
            with self.subTest(fault=fault):
                f = MetadataFixture(self.temp.name)
                if fault == 'missing': f.value['catalogs'].pop('guard007_after')
                else:
                    value = proof.strict_json(proof.artifact(f.directory, f.value['catalogs']['current007']))
                    if fault == 'shape': value['shape']['Tables'].pop('source_metadata_resolutions')
                    elif fault == 'position': value['position'] = 6
                    elif fault == 'checksum': value['migrations'][-1]['checksum'] = '0' * 64
                    else: value['context']['candidate_commit'] = '9' * 40
                    f.value['catalogs']['current007'] = f.raw('wrong-catalog', value)
                with self.assertRaises(RuntimeError): f.validate()

    def test_actual_runtime_identity_privileges_and_crud_are_required(self):
        for key in ('role', 'session_role', 'schema_owner', 'table_owner', 'schema_create',
                    'select', 'insert', 'update', 'delete', 'crud_assertions', 'retained_cache_rows', 'retained_lease_rows'):
            with self.subTest(key=key):
                f = MetadataFixture(self.temp.name)
                def change(v):
                    if isinstance(v[key], bool): v[key] = not v[key]
                    elif isinstance(v[key], int): v[key] = 0
                    else: v[key] = 'cutmy_admin'
                f.change_raw('runtime_grants', change)
                with self.assertRaises(RuntimeError): f.validate()
        for key in ('schema_owner', 'select', 'crud_assertions', 'retained_lease_rows'):
            with self.subTest(malformed_type=key):
                f = MetadataFixture(self.temp.name)
                f.change_raw('runtime_grants', lambda v: v.update({key: True if key == 'retained_lease_rows' else int(v[key])}))
                with self.assertRaises(RuntimeError): f.validate()

    def test_restored_cache_owner_payload_and_pending_token_expiry_are_bound_to_fixture(self):
        faults = (('source_metadata_cache', 'owner', 'foreign'), ('source_metadata_cache', 'payload', '\\x00'),
                  ('source_metadata_cache', 'expires_at', 'changed'), ('source_metadata_resolutions', 'owner', 'foreign'),
                  ('source_metadata_resolutions', 'token', 'replacement'), ('source_metadata_resolutions', 'expires_at', 'changed'))
        for table, key, replacement in faults:
            with self.subTest(table=table, key=key):
                f = MetadataFixture(self.temp.name)
                # Even mutually matching before/after snapshots must preserve
                # the independently captured synthetic fixture's exact values.
                for phase in ('restore_before', 'restore_after'):
                    f.change_raw(phase, lambda v: v[table][0].update({key: replacement}))
                with self.assertRaises(RuntimeError): f.validate()
        f = MetadataFixture(self.temp.name)
        f.change_raw('metadata_after_startup', lambda v: v['source_metadata_resolutions'].clear())
        with self.assertRaises(RuntimeError): f.validate()

    def test_reverse_media_and_matching_recovery_image_are_required(self):
        for fault in ('files', 'decoded', 'method', 'image', 'guard'):
            with self.subTest(fault=fault):
                f = MetadataFixture(self.temp.name)
                if fault == 'files': f.change_raw('rollback_media', lambda v: v['after'].clear())
                elif fault == 'decoded': f.change_raw('rollback_media', lambda v: v['decoded_retained_outputs'].pop())
                elif fault == 'method': f.value['reverse_method'] = 'remove_version_markers'
                elif fault == 'image': f.value['compatible_recovery_image_id'] = 'sha256:' + '0' * 64
                else: f.value['guard_exit'] = 0
                with self.assertRaises(RuntimeError): f.validate()

    def test_private_state_cleanup_and_raw_manifest_integrity_are_required(self):
        for fault in ('private', 'manifest'):
            with self.subTest(fault=fault):
                f = MetadataFixture(self.temp.name)
                if fault == 'private': f.report['cleanup']['private_workspace_retained'] = True
                else: (f.directory / f.value['manifests']['7']['path']).write_bytes(b'{}')
                with self.assertRaises(RuntimeError): f.validate()


class MetadataAdmissionControls(unittest.TestCase):
    def test_explicit_mode_selects_exact006_predecessor_and_only007_pair(self):
        args = base.parse_args(['--mode', metadata.MODE, '--revision', '1' * 40, '--output', '/synthetic-output'])
        self.assertEqual(args.upgrade_from, metadata.BASELINE)
        old = existing.current.application({'files': baseline_files()})
        candidate = deepcopy(old)
        for name in (metadata.SQL_NAME, metadata.SHAPE_NAME): candidate[name] = existing.blob((ROOT.parent / name).read_bytes())
        metadata.validate_schema_inventory(old, candidate)
        for fault in ('extra', 'unversioned', 'old', 'sql', 'shape', 'missing'):
            with self.subTest(fault=fault):
                wrong = deepcopy(candidate)
                if fault == 'extra': wrong['internal/app/migrations/008_future.sql'] = existing.blob(b'SELECT 1;')
                elif fault == 'unversioned': wrong['internal/app/migrations/unreviewed.sql'] = existing.blob(b'SELECT 1;')
                elif fault == 'old': wrong['internal/app/migrations/006_schema.json']['sha256'] = '0' * 64
                elif fault == 'sql': wrong[metadata.SQL_NAME]['sha256'] = '0' * 64
                elif fault == 'shape': wrong[metadata.SHAPE_NAME]['sha256'] = '0' * 64
                else: wrong.pop(metadata.SHAPE_NAME)
                with self.assertRaises(RuntimeError): metadata.validate_schema_inventory(old, wrong)

    def test_wrong_mode_never_initializes_parent_or_host(self):
        init = Mock()
        fake = SimpleNamespace(Acceptance=type('FakeBase', (), {'__init__': init}), transition_module=lambda: proof,
                               current_installation_module=lambda: existing.current,
                               source_media_cleanup_module=lambda: base.source_media_cleanup_module())
        value = metadata.acceptance_class(fake)
        for mode in (None, 'same-schema', 'current-installation', 'source-media-cleanup-005-to-006'):
            with self.assertRaises(RuntimeError): value(SimpleNamespace(mode=mode))
        init.assert_not_called()

    def test_historical_modes_continue_to_refuse007_inputs(self):
        cleanup = base.source_media_cleanup_module()
        old = existing.current.application({'files': existing.baseline_files()})
        candidate = deepcopy(old)
        for name in (cleanup.SQL_NAME, cleanup.SHAPE_NAME, metadata.SQL_NAME, metadata.SHAPE_NAME):
            candidate[name] = existing.blob((ROOT.parent / name).read_bytes())
        with self.assertRaises(RuntimeError): cleanup.validate_schema_inventory(old, candidate)


if __name__ == '__main__':
    unittest.main()
