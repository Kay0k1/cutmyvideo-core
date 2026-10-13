#!/usr/bin/env python3
"""Pinned 006 -> 007 public-profile acceptance; historical modes stay fixed.

Host operations remain in the existing explicitly disposable-host driver. This
mode binds complete image sources, an immutable006 predecessor and only the
reviewed007 addition, then exercises both coordinated restoration directions.
"""
from __future__ import annotations

from copy import deepcopy
import importlib.util
from pathlib import Path
import re

MODE = 'metadata-resolution-006-to-007'
BASELINE = 'a9c75df83f965bf1d56e2263ca585ab77aeaf305'
BASELINE_TREE = '807d53bc6c19fac787fa0a57415b2ae0aa3a4dd2'
BASELINE_APPLICATION_SHA256 = '50ebb065fe02ce69e3adf190f111eb42a6426f4573173d090a878ef568b2ef9e'
BASELINE_FILES = 218
MIGRATION = '20261014-metadata-resolution-leases-v1'
SQL_NAME = 'internal/app/migrations/007_20261014_metadata_resolution_leases_v1.sql'
SHAPE_NAME = 'internal/app/migrations/007_schema.json'
SQL_SHA256 = '01f421ef7efdf02386f7d284491ccf1bccee2dc3014a24f24b85bcd356dcbc0f'
SHAPE_SHA256 = 'caf5aa640b9003dfd2538b43001a994c8153fa35ea3f6541d6bd9f6e53335df0'
CHECKS = ('old006_actual_catalog', 'additive007_actual_catalog_and_originals',
          'old006_guard_refuses007_without_mutation', 'fresh006_coordinated_reverse',
          'compatible007_fresh_restore', 'runtime_metadata_lease_crud',
          'metadata_cache_and_pending_lease_restored')
CATALOGS = {'old006': 6, 'current007': 7, 'guard007_before': 7,
            'guard007_after': 7, 'rollback006': 6, 'restored007': 7}
PAYLOAD_HEX = b'metadata-resolution-007-synthetic'.hex()
TOKEN = 'acceptance-metadata-007-retained'


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def migrations():
    proof = module('metadata_history', 'source-retention-transition.py')
    cleanup = module('metadata_previous_history', 'source-media-cleanup-transition.py')
    return ([dict(position=i + 1, version=v, checksum=d) for i, (v, _, d) in enumerate(proof.MIGRATIONS)]
            + [dict(position=6, version=cleanup.MIGRATION, checksum=cleanup.SQL_SHA256),
               dict(position=7, version=MIGRATION, checksum=SQL_SHA256)])


def groups(original):
    result = dict(original)
    result['identity'] = tuple('metadata_candidate_sources_bound' if name == 'application_sources_unchanged'
                               else name for name in original['identity'])
    result['metadata_resolution_upgrade'] = CHECKS
    return result


def validate_schema_inventory(old, current):
    proof = module('metadata_schema_inventory', 'source-retention-transition.py')
    frozen = {name: value for name, value in old.items()
              if re.fullmatch(r'internal/app/migrations/00[1-6]_[^/]+\.(sql|json)', name)}
    proof.require(len(frozen) == 12 and all(current.get(name) == value for name, value in frozen.items()),
                  'historical001-006 schema inputs changed')
    schema_files = {name for name in current if name.startswith('internal/app/migrations/')
                    and name.endswith(('.sql', '.json'))}
    proof.require(schema_files - set(frozen) == {SQL_NAME, SHAPE_NAME}
                  and current[SQL_NAME]['sha256'] == SQL_SHA256
                  and current[SHAPE_NAME]['sha256'] == SHAPE_SHA256,
                  'only the explicitly reviewed migration007 SQL and catalog are admitted')


def project_originals(before, after):
    proof = module('metadata_original_rows', 'source-retention-transition.py')
    proof.require(isinstance(before, dict) and isinstance(after, dict)
                  and 'source_metadata_resolutions' not in before
                  and set(after) == set(before) | {'source_metadata_resolutions'}
                  and after['source_metadata_resolutions'] == [],
                  'migration007 must add only an empty coordination table')
    proof.require(all(proof.canonical(after[table]) == proof.canonical(rows)
                      for table, rows in before.items()),
                  'migration007 changed historical application records')


def validate_runtime_grants(value):
    proof = module('metadata_grant_proof', 'source-retention-transition.py')
    booleans = ('schema_owner', 'table_owner', 'schema_create', 'select', 'insert', 'update', 'delete', 'crud_assertions')
    proof.require(isinstance(value, dict) and all(type(value.get(key)) is bool for key in booleans)
                  and all(type(value.get(key)) is int for key in ('retained_cache_rows', 'retained_lease_rows'))
                  and value == {'role': 'cutmy_runtime', 'session_role': 'cutmy_runtime',
                           'schema_owner': False, 'table_owner': False, 'schema_create': False,
                           'select': True, 'insert': True, 'update': True, 'delete': True,
                           'crud_assertions': True, 'retained_cache_rows': 1, 'retained_lease_rows': 1},
                  'actual restricted-runtime metadata lease CRUD proof differs')


def validate_metadata_state(snapshot, fixture):
    proof = module('metadata_state_proof', 'source-retention-transition.py')
    proof.require(set(fixture) == {'source_id', 'owner', 'token', 'payload_hex', 'cache_expires_at', 'lease_expires_at'}
                  and re.fullmatch(r'acceptance_metadata_[a-z0-9]+', fixture['source_id'])
                  and re.fullmatch(r'acceptance_metadata_owner_[a-z0-9]+', fixture['owner'])
                  and fixture['token'] == TOKEN and fixture['payload_hex'] == PAYLOAD_HEX
                  and isinstance(fixture['cache_expires_at'], str) and fixture['cache_expires_at']
                  and isinstance(fixture['lease_expires_at'], str) and fixture['lease_expires_at'],
                  'synthetic metadata fixture identity differs')
    source = [row for row in snapshot['sources'] if row['id'] == fixture['source_id']]
    cache = [row for row in snapshot['source_metadata_cache'] if row['source_id'] == fixture['source_id']]
    lease = [row for row in snapshot['source_metadata_resolutions'] if row['source_id'] == fixture['source_id']]
    proof.require(len(source) == len(cache) == len(lease) == 1
                  and all(row['owner'] == fixture['owner'] for row in (source[0], cache[0], lease[0]))
                  and source[0]['kind'] == 'platform' and source[0]['path'] == ''
                  and cache[0]['payload'] == '\\x' + PAYLOAD_HEX
                  and lease[0]['token'] == TOKEN
                  and cache[0]['expires_at'] == fixture['cache_expires_at']
                  and lease[0]['expires_at'] == fixture['lease_expires_at'],
                  'actual cache payload, owner or pending resolution lease was not restored')


def validate_release_transition(report, directory):
    proof = module('metadata_receipts', 'source-retention-transition.py')
    current = module('metadata_source_tree', 'current-installation-acceptance.py')
    cleanup = module('metadata_catalogs', 'source-media-cleanup-transition.py')
    value = report.get('metadata_resolution_transition', {})
    proof.require(report.get('mode') == MODE and type(report.get('schema_version')) is int
                  and report['schema_version'] == 5, 'explicit migration007 mode required')
    proof.require(report['cleanup'].get('private_state_backup_config_keys_removed') is True
                  and report['cleanup'].get('private_workspace_retained') is False,
                  'metadata transition private cleanup incomplete')
    admission = value['admission']
    proof.require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
                  and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
                  and admission['candidate_commit'] == report['expected_checkout']
                  and admission['candidate_tree'] == report['checkout']['tree']
                  and admission['captured_before_image_builds'] is True
                  and admission['schema_transition'] == [6, 7], 'candidate source/transition binding differs')
    sources = {}
    for name in ('old', 'current'):
        raw = proof.artifact(directory, value[name + '_source'])
        source = proof.strict_json(raw)
        image = report['images'][name]
        proof.require(source['commit'] == image['source_commit'] and source['tree'] == image['tree']
                      and current.source_tree(source['files']) == source['tree']
                      and proof.digest(raw) == image['source_sha256'], 'actual source receipt differs')
        sources[name] = current.application(source)
    proof.require(len(sources['old']) == BASELINE_FILES
                  and proof.digest(proof.canonical(sources['old'])) == BASELINE_APPLICATION_SHA256,
                  'immutable006 predecessor changed')
    proof.require(type(admission['candidate_application_files']) is int
                  and admission['candidate_application_files'] == len(sources['current'])
                  and admission['candidate_application_sha256'] == proof.digest(proof.canonical(sources['current'])),
                  'candidate application inventory differs')
    validate_schema_inventory(sources['old'], sources['current'])
    proof.require(report['runtime_application_sources_unchanged'] is False
                  and report['scope']['schema_changing_upgrade'] is True
                  and report['groups']['identity']['checks']['metadata_candidate_sources_bound']['evidence']['schema_transition'] == [6, 7],
                  'schema-changing application claim differs')
    proof.require(value['migrations'] == migrations() and set(value['manifests']) == {'6', '7'}
                  and set(value['catalogs']) == set(CATALOGS), 'exact migration history/catalog phases missing')
    shapes = {}
    for position in (6, 7):
        receipt = value['manifests'][str(position)]
        shapes[position] = proof.strict_json(proof.artifact(directory, receipt))
        proof.require(receipt['sha256'] == sources['current'][f'internal/app/migrations/{position:03d}_schema.json']['sha256'],
                      'catalog manifest source differs')
    for phase, position in CATALOGS.items():
        catalog = proof.strict_json(proof.artifact(directory, value['catalogs'][phase]))
        cleanup.validate_catalog(catalog, position, shapes[position], migrations())
        proof.require(catalog['context'] == admission, 'catalog was borrowed from another candidate')
    before = proof.strict_json(proof.artifact(directory, value['originals_before']))
    after = proof.strict_json(proof.artifact(directory, value['originals_after']))
    project_originals(before, after)
    for first, second in (('guard_before', 'guard_after'), ('restore_before', 'restore_after')):
        proof.require(proof.artifact(directory, value[first]) == proof.artifact(directory, value[second]),
                      'guard refusal or compatible007 restore changed durable records')
    proof.require(proof.artifact(directory, value['catalogs']['guard007_before'])
                  == proof.artifact(directory, value['catalogs']['guard007_after']), 'old006 refusal changed007 catalog/history')
    proof.require(proof.strict_json(proof.artifact(directory, value['rollback_originals'])) == before,
                  'coordinated006 reversal changed original records')
    rollback = proof.strict_json(proof.artifact(directory, value['rollback_media']))
    prior_outputs = report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence']['results']
    proof.require(rollback['before'] == rollback['after'] and rollback['before']
                  and isinstance(rollback['decoded_retained_outputs'], list)
                  and len(rollback['decoded_retained_outputs']) == 3
                  and [item['sha256'] for item in rollback['decoded_retained_outputs']]
                  == [item['sha256'] for item in prior_outputs]
                  and all(proof.HASH.fullmatch(item['sha256']) for item in rollback['decoded_retained_outputs']),
                  'coordinated006 reverse lost registered media or retained downloads')
    validate_runtime_grants(proof.strict_json(proof.artifact(directory, value['runtime_grants'])))
    fixture = proof.strict_json(proof.artifact(directory, value['metadata_fixture']))
    validate_metadata_state(proof.strict_json(proof.artifact(directory, value['restore_before'])), fixture)
    validate_metadata_state(proof.strict_json(proof.artifact(directory, value['restore_after'])), fixture)
    validate_metadata_state(proof.strict_json(proof.artifact(directory, value['metadata_after_startup'])), fixture)
    proof.require(type(value['guard_exit']) is int and value['guard_exit'] == 1
                  and value['guard_code'] == 'schema_incompatible'
                  and value['reverse_method'] == 'fresh_restore_coordinated006_backup'
                  and value['compatible_recovery_image_id'] == report['images']['current']['image_id'],
                  'guard/reverse/compatible recovery method differs')


def acceptance_class(base):
    proof = base.transition_module()
    current = base.current_installation_module()
    cleanup = base.source_media_cleanup_module()

    class MetadataTransition(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(getattr(args, 'mode', None) == MODE, 'explicit metadata transition required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=5, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['metadata_resolution_transition'] = {'catalogs': {}, 'manifests': {}}

        def raw(self, name, value):
            data = value if isinstance(value, bytes) else proof.canonical(value)
            path = self.output / name
            proof.require(not path.exists(), 'immutable transition receipt cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'immutable006 predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'immutable006 predecessor application differs')
            validate_schema_inventory(self.admitted_old, self.admitted_current)
            super().initialize_host()
            self.rollback = self.make_project(self.old_repo, 'rollback006', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.transition = self.report['metadata_resolution_transition']
            self.transition['admission'] = {'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                'baseline_commit': BASELINE, 'baseline_tree': BASELINE_TREE,
                'candidate_commit': self.args.revision, 'candidate_tree': self.report['checkout']['tree'],
                'candidate_application_files': len(self.admitted_current),
                'candidate_application_sha256': proof.digest(proof.canonical(self.admitted_current)),
                'captured_before_image_builds': True, 'schema_transition': [6, 7]}
            self.transition['migrations'] = migrations()
            for position in (6, 7):
                self.transition['manifests'][str(position)] = self.raw(f'metadata-manifest-{position}.json',
                    (self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            self.save()

        def application_identity(self):
            proof.require(current.application(self.old_source) == self.admitted_old
                          and current.application(self.current_source) == self.admitted_current,
                          'built source differs from admitted candidate')
            for name, project in (('old', self.old), ('current', self.current)):
                self.transition[name + '_source'] = self.raw('metadata-' + name + '-source.json', (project['state'] / 'SOURCE.json').read_bytes())
            self.check('metadata_candidate_sources_bound', {'baseline_commit': BASELINE, 'candidate_commit': self.args.revision,
                'candidate_application_sha256': self.transition['admission']['candidate_application_sha256'], 'schema_transition': [6, 7]})

        def capture_catalog(self, phase, project, position):
            latest = proof.strict_json((self.current_repo / SHAPE_NAME).read_bytes())
            query = re.search(r'const schemaShapeSQL = `([^`]+)`', (self.current_repo / 'internal/app/migration_catalog.go').read_text())[1]
            array = lambda names: 'ARRAY[' + ','.join("'" + n + "'" for n in sorted(names)) + ']::text[]'
            query = query.replace('$1::text[]', array(latest['Tables'])).replace('$2::text[]', array(latest['Functions']))
            shape = proof.strict_json(self.sql(project, query))
            history = proof.strict_json(self.sql(project, "SELECT jsonb_build_object('versions',(SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions),'migrations',(SELECT jsonb_agg(jsonb_build_object('position',position,'version',version,'checksum',checksum) ORDER BY position) FROM app_schema_migrations))"))
            value = dict(position=position, shape=shape, context=self.transition['admission'],
                         postgres_version_num=int(self.sql(project, "SELECT current_setting('server_version_num')")), **history)
            expected = proof.strict_json((self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            cleanup.validate_catalog(value, position, expected, migrations())
            receipt = self.raw('metadata-catalog-' + phase + '.json', value)
            self.transition['catalogs'][phase] = receipt
            return receipt

        def snapshot(self, project):
            result = super().snapshot(project)
            for name in ('source_media_cleanup', 'source_media_readers', 'source_metadata_cache', 'source_metadata_resolutions'):
                if self.sql(project, "SELECT to_regclass('" + name + "') IS NOT NULL").strip() == b't':
                    result[name] = proof.strict_json(self.sql(project, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + name + ' t'))
            return result

        def seed_runtime_metadata(self):
            source, owner = 'acceptance_metadata_' + self.token, 'acceptance_metadata_owner_' + self.token
            # Only controlled, synthetic payload/token values enter public raw
            # state evidence. No signed upstream URLs, cookies or user data.
            statement = f"""BEGIN;
INSERT INTO sources(id,owner,title,duration_ms,kind,path,url,provider_id) VALUES('{source}','{owner}','Synthetic metadata007',10000,'platform','','https://www.youtube.com/watch?v=acceptance007','acceptance007');
INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at) VALUES('{source}','{owner}',decode('{PAYLOAD_HEX}','hex'),clock_timestamp()+interval '24 hours');
INSERT INTO source_metadata_resolutions(source_id,owner,token,expires_at) VALUES('{source}','{owner}','acceptance-inserted',clock_timestamp()+interval '3 minutes');
UPDATE source_metadata_resolutions SET token='acceptance-updated' WHERE source_id='{source}' AND token='acceptance-inserted';
DO $$ BEGIN IF (SELECT count(*) FROM source_metadata_resolutions WHERE source_id='{source}' AND token='acceptance-updated')<>1 THEN RAISE EXCEPTION 'runtime metadata UPDATE/SELECT failed'; END IF; END $$;
DELETE FROM source_metadata_resolutions WHERE source_id='{source}' AND token='acceptance-updated';
DO $$ BEGIN IF EXISTS(SELECT 1 FROM source_metadata_resolutions WHERE source_id='{source}') THEN RAISE EXCEPTION 'runtime metadata DELETE failed'; END IF; END $$;
INSERT INTO source_metadata_resolutions(source_id,owner,token,expires_at) VALUES('{source}','{owner}','{TOKEN}',clock_timestamp()+interval '3 minutes');
SELECT jsonb_build_object('role',current_user,'session_role',session_user,
'schema_owner',(SELECT nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_namespace WHERE nspname='public'),
'table_owner',(SELECT relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class WHERE oid='source_metadata_resolutions'::regclass),
'schema_create',has_schema_privilege('public','CREATE'),
'select',has_table_privilege('source_metadata_resolutions','SELECT'),'insert',has_table_privilege('source_metadata_resolutions','INSERT'),
'update',has_table_privilege('source_metadata_resolutions','UPDATE'),'delete',has_table_privilege('source_metadata_resolutions','DELETE'),
'crud_assertions',true,'retained_cache_rows',(SELECT count(*) FROM source_metadata_cache WHERE source_id='{source}'),
'retained_lease_rows',(SELECT count(*) FROM source_metadata_resolutions WHERE source_id='{source}'));
COMMIT;"""
            observed = proof.strict_json(self.sql(self.current, statement, runtime=True))
            validate_runtime_grants(observed)
            self.transition['runtime_grants'] = self.raw('metadata-runtime-grants.json', observed)
            deadlines = proof.strict_json(self.sql(self.current,
                f"SELECT jsonb_build_object('cache_expires_at',(SELECT expires_at FROM source_metadata_cache WHERE source_id='{source}'),'lease_expires_at',(SELECT expires_at FROM source_metadata_resolutions WHERE source_id='{source}'))", runtime=True))
            self.transition['metadata_fixture'] = self.raw('metadata-synthetic-fixture.json',
                dict(source_id=source, owner=owner, token=TOKEN, payload_hex=PAYLOAD_HEX, **deadlines))

        def backup_and_upgrade(self):
            super().backup_and_upgrade()
            self.group('metadata_resolution_upgrade', self.supplemental_checks)

        def supplemental_checks(self):
            self.wrapper(self.current, 'stop')
            project_originals(self.before006, self.initial007)
            self.capture_catalog('restored007', self.restored, 7)
            self.check('additive007_actual_catalog_and_originals')
            self.transition['guard_before'] = self.raw('metadata-guard-before.json', self.snapshot(self.current))
            self.capture_catalog('guard007_before', self.current, 7)
            override = self.work / 'metadata-old006-guard.json'
            override.write_bytes(proof.canonical({'services': {'migrate': {'image': self.report['images']['old']['image_id']}}}))
            override.chmod(0o600)
            code, stdout, stderr = self.run(self.compose(self.current) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate', '--json-errors', 'maintenance'], expected=None, timeout=30)
            diagnostics = [proof.strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            proof.require(code == 1 and not stdout.strip() and len(diagnostics) == 1
                          and diagnostics[0]['error']['code'] == 'schema_incompatible', 'old006 guard did not refuse007')
            self.transition.update(guard_exit=code, guard_code='schema_incompatible')
            self.transition['guard_after'] = self.raw('metadata-guard-after.json', self.snapshot(self.current))
            self.capture_catalog('guard007_after', self.current, 7)
            self.check('old006_guard_refuses007_without_mutation')
            proof.require(self.project_resources(self.rollback) == [], 'rollback must use fresh resources')
            self.wrapper(self.rollback, 'adopt-image', '--from-state', self.old['state'])
            self.wrapper(self.rollback, 'restore', '--directory', self.old_backup)
            restored = self.snapshot(self.rollback)
            proof.require(restored == self.before006, 'coordinated006 backup changed records')
            self.transition['rollback_originals'] = self.raw('metadata-rollback006.json', restored)
            self.capture_catalog('rollback006', self.rollback, 6)
            self.policy(self.rollback, 'apply'); self.wrapper(self.rollback, 'initialize'); self.wrapper(self.rollback, 'up')
            owner, foreign = [base.Client(self.rollback['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
            owner.cookie, foreign.cookie = self.a.cookie, self.b.cookie
            decoded = self.verify_retained(self.rollback, owner, foreign, prefix='rollback006')
            _, actual_files = self.accounting(self.rollback)
            proof.require(actual_files == self.before006_files, 'coordinated006 restore changed media bytes')
            self.transition['rollback_media'] = self.raw('metadata-rollback006-media.json',
                dict(before=self.before006_files, after=actual_files, decoded_retained_outputs=decoded))
            self.transition['reverse_method'] = 'fresh_restore_coordinated006_backup'
            self.wrapper(self.rollback, 'stop')
            self.check('fresh006_coordinated_reverse')
            self.transition['restore_before'] = self.raw('metadata-restore007-before.json', self.restore_snapshot)
            self.transition['restore_after'] = self.raw('metadata-restore007-after.json', self.actual_restore007)
            self.transition['compatible_recovery_image_id'] = self.report['images']['restored']['image_id']
            self.check('compatible007_fresh_restore')
            fixture = proof.strict_json(proof.artifact(self.output, self.transition['metadata_fixture']))
            validate_metadata_state(self.restore_snapshot, fixture)
            validate_metadata_state(self.actual_restore007, fixture)
            running_state = self.snapshot(self.restored)
            validate_metadata_state(running_state, fixture)
            self.transition['metadata_after_startup'] = self.raw('metadata-after-restored-startup.json', running_state)
            self.check('metadata_cache_and_pending_lease_restored')
            self.check('runtime_metadata_lease_crud')
            self.wrapper(self.current, 'up')

        def wrapper(self, project, action, *extra, expected=0):
            if action == 'backup' and project is self.old and expected == 0:
                _, self.before006_files = self.accounting(project)
            result = super().wrapper(project, action, *extra, expected=expected)
            if action == 'backup' and project is self.old and expected == 0:
                proof.require(all(not self.container(project, role)['State']['Running'] for role in ('api', 'worker')),
                              'old006 backup writers still active')
                self.before006 = self.snapshot(project)
                self.transition['originals_before'] = self.raw('metadata-originals006.json', self.before006)
                self.capture_catalog('old006', project, 6)
                active = self.active_group
                self.group('metadata_resolution_upgrade', lambda: self.check('old006_actual_catalog'), complete=False)
                self.active_group = active
            if action == 'initialize' and project is self.current and expected == 0 and 'originals_after' not in self.transition:
                self.initial007 = self.snapshot(project)
                project_originals(self.before006, self.initial007)
                self.transition['originals_after'] = self.raw('metadata-originals007.json', self.initial007)
                self.capture_catalog('current007', project, 7)
                self.seed_runtime_metadata()
            if action == 'restore' and project is self.restored and expected == 0:
                self.actual_restore007 = self.snapshot(project)
            return result

    return MetadataTransition
