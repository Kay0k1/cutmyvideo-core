#!/usr/bin/env python3
"""Pinned 007 -> 008 public-profile acceptance; historical modes stay fixed.

Host operations remain in the existing explicitly disposable-host driver. This
mode binds all current application sources, an immutable007 predecessor and
the exact008 SQL/catalog pair, then exercises both restoration directions.
"""
from __future__ import annotations

from copy import deepcopy
import importlib.util
from pathlib import Path
import re

MODE = 'agent-projects-007-to-008'
BASELINE = 'e4a0477b727ccfbbf21f86027fdd07f6ffe78419'
BASELINE_TREE = '7bdd2f71eaf9398fb8548ed59834dba88e716a98'
BASELINE_APPLICATION_SHA256 = '4ee34018d25013281f81bc0fb09bd166ca6ede8a5b850b07c04f610f0fef54cb'
BASELINE_FILES = 259
MIGRATION = '20261016-agent-projects-v1'
SQL_NAME = 'internal/app/migrations/008_20261016_agent_projects_v1.sql'
SHAPE_NAME = 'internal/app/migrations/008_schema.json'
SQL_SHA256 = '40990aab92ee4bcdadeae7b7f0f21ad308804f30c054372907c15d2c14a51a92'
SHAPE_SHA256 = 'c064e4ba3251a5bf6968cec9ed1170038b8811eb36e29367bf67f06b15362dad'
CHECKS = ('old007_actual_catalog', 'additive008_actual_catalog_and_originals',
          'old007_guard_refuses008_without_mutation', 'fresh007_coordinated_reverse',
          'compatible008_fresh_restore', 'runtime_agent_project_tables_crud',
          'preparation_project_grants_and_retry_restored')
CATALOGS = {'old007': 7, 'current008': 8, 'guard008_before': 8,
            'guard008_after': 8, 'rollback007': 7, 'restored008': 8}
NEW_TABLES = ('source_preparations', 'source_fingerprints', 'agent_projects',
              'agent_project_jobs', 'agent_project_handoffs', 'agent_project_grants',
              'agent_project_retries')
PREPARATION_KEY = 'acceptance-preparation-008'
COLLECTION_KEY = 'acceptance-collection-008'
RETRY_KEY = 'acceptance-retry-008'
ATTEMPT_KEY = 'acceptance-attempt-008'
TOKEN_HASH = 'b' * 64  # Controlled synthetic hash, never a redeemable capability.


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def migrations():
    previous = module('projects_previous_history', 'metadata-resolution-transition.py')
    return previous.migrations() + [dict(position=8, version=MIGRATION, checksum=SQL_SHA256)]


def groups(original):
    result = dict(original)
    result['identity'] = tuple('projects_candidate_sources_bound' if name == 'application_sources_unchanged'
                               else name for name in original['identity'])
    result['agent_projects_upgrade'] = CHECKS
    return result


def validate_schema_inventory(old, current):
    proof = module('metadata_schema_inventory', 'source-retention-transition.py')
    frozen = {name: value for name, value in old.items()
              if re.fullmatch(r'internal/app/migrations/00[1-7]_[^/]+\.(sql|json)', name)}
    proof.require(len(frozen) == 14 and all(current.get(name) == value for name, value in frozen.items()),
                  'historical001-007 schema inputs changed')
    schema_files = {name for name in current if name.startswith('internal/app/migrations/')
                    and name.endswith(('.sql', '.json'))}
    proof.require(schema_files - set(frozen) == {SQL_NAME, SHAPE_NAME}
                  and current[SQL_NAME]['sha256'] == SQL_SHA256
                  and current[SHAPE_NAME]['sha256'] == SHAPE_SHA256,
                  'only the explicitly reviewed migration008 SQL and catalog are admitted')


def project_originals(before, after):
    proof = module('metadata_original_rows', 'source-retention-transition.py')
    proof.require(isinstance(before, dict) and isinstance(after, dict)
                  and not set(NEW_TABLES).intersection(before)
                  and set(after) == set(before) | set(NEW_TABLES)
                  and all(after[table] == [] for table in NEW_TABLES),
                  'migration008 must add exactly seven empty project/preparation tables')
    proof.require(all(proof.canonical(after[table]) == proof.canonical(rows)
                      for table, rows in before.items()),
                  'migration008 changed historical application records')


def validate_runtime_grants(value):
    proof = module('projects_grant_proof', 'source-retention-transition.py')
    expected_table = dict(table_owner=False, select=True, insert=True, update=True,
                          delete=True, crud_assertions=True, retained_rows=1)
    proof.require(isinstance(value, dict) and set(value) == {'role', 'session_role', 'schema_owner', 'schema_create', 'tables'}
                  and value['role'] == value['session_role'] == 'cutmy_runtime'
                  and value['schema_owner'] is False and value['schema_create'] is False
                  and isinstance(value['tables'], dict) and set(value['tables']) == set(NEW_TABLES),
                  'restricted runtime identity or project table inventory differs')
    for table in NEW_TABLES:
        row = value['tables'][table]
        proof.require(isinstance(row, dict) and row == expected_table
                      and all(type(row.get(key)) is bool for key in expected_table if key != 'retained_rows')
                      and type(row.get('retained_rows')) is int,
                      'actual restricted-runtime project table CRUD proof differs')


def validate_project_state(snapshot, fixture):
    proof = module('projects_state_proof', 'source-retention-transition.py')
    proof.require(isinstance(fixture, dict) and set(fixture) == {'source_id', 'owner', 'project_id', 'job_id', 'rows'}
                  and all(re.fullmatch(pattern + r'[a-z0-9]+', fixture[key]) for key, pattern in
                          (('source_id', 'acceptance_projects_'), ('owner', 'acceptance_projects_owner_'),
                           ('project_id', 'acceptance_project_'), ('job_id', 'acceptance_project_job_')))
                  and isinstance(fixture['rows'], dict) and set(fixture['rows']) == set(NEW_TABLES),
                  'synthetic project fixture identity differs')
    source = [row for row in snapshot['sources'] if row['id'] == fixture['source_id']]
    job = [row for row in snapshot['jobs'] if row['id'] == fixture['job_id']]
    proof.require(len(source) == len(job) == 1 and source[0]['owner'] == job[0]['owner'] == fixture['owner']
                  and source[0]['kind'] == 'platform' and source[0]['path'] == ''
                  and job[0]['source_id'] == fixture['source_id'] and job[0]['status'] == 'failed',
                  'synthetic source and failed job identity was not retained')
    for table in NEW_TABLES:
        rows = fixture['rows'][table]
        proof.require(isinstance(rows, list) and len(rows) == 1
                      and snapshot.get(table) == rows, 'project/preparation/retry row changed during recovery')
    prep, fingerprint, project, attached, handoff, grant, retry = (fixture['rows'][table][0] for table in NEW_TABLES)
    proof.require(prep['owner'] == fingerprint['owner'] == project['owner'] == fixture['owner']
                  and prep['source_id'] == fingerprint['source_id'] == project['source_id'] == fixture['source_id']
                  and prep['key'] == PREPARATION_KEY and prep['state'] == 'ready' and prep['kind'] == 'import'
                  and prep['intent_hash'] == 'a' * 64 and prep['fingerprint'] == fingerprint['fingerprint']
                  and fingerprint['fingerprint'] == {'kind': 'provider', 'canonical_url_sha256': 'c' * 64}
                  and project['id'] == attached['project_id'] == handoff['project_id'] == grant['project_id'] == retry['project_id'] == fixture['project_id']
                  and project['revision'] == 1 and project['rendition'] == 'final' and project['collection_key'] == COLLECTION_KEY
                  and project['plan'] == {'version': 1, 'clips': [{'id': 'opening', 'ranges': [{'start_ms': 1000, 'end_ms': 2000}]}]}
                  and attached['job_id'] == fixture['job_id'] and attached['clip_id'] == 'opening'
                  and handoff['token_hash'] == TOKEN_HASH and handoff['recipient'] == grant['recipient'] == 'acceptance-recipient-008'
                  and handoff['redeemed_at'] and retry['key'] == RETRY_KEY and retry['attempt_key'] == ATTEMPT_KEY
                  and retry['intent_hash'] == 'd' * 64 and retry['request'] == {'revision': 1, 'clip_ids': ['opening']}
                  and retry['clip_intents'] == {'opening': 'e' * 64} and retry['clip_job_keys'] == {'opening': ATTEMPT_KEY},
                  'preparation fingerprint, project plan, handoff grant or pending retry differs')


def validate_release_transition(report, directory):
    proof = module('metadata_receipts', 'source-retention-transition.py')
    current = module('metadata_source_tree', 'current-installation-acceptance.py')
    cleanup = module('metadata_catalogs', 'source-media-cleanup-transition.py')
    value = report.get('agent_projects_transition', {})
    proof.require(report.get('mode') == MODE and type(report.get('schema_version')) is int
                  and report['schema_version'] == 6, 'explicit migration008 mode required')
    proof.require(report['cleanup'].get('private_state_backup_config_keys_removed') is True
                  and report['cleanup'].get('private_workspace_retained') is False,
                  'agent-project transition private cleanup incomplete')
    admission = value['admission']
    proof.require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
                  and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
                  and admission['candidate_commit'] == report['expected_checkout']
                  and admission['candidate_tree'] == report['checkout']['tree']
                  and admission['captured_before_image_builds'] is True
                  and admission['schema_transition'] == [7, 8], 'candidate source/transition binding differs')
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
                  'immutable007 predecessor changed')
    proof.require(type(admission['candidate_application_files']) is int
                  and admission['candidate_application_files'] == len(sources['current'])
                  and admission['candidate_application_sha256'] == proof.digest(proof.canonical(sources['current'])),
                  'candidate application inventory differs')
    validate_schema_inventory(sources['old'], sources['current'])
    proof.require(report['runtime_application_sources_unchanged'] is False
                  and report['scope']['schema_changing_upgrade'] is True
                  and report['groups']['identity']['checks']['projects_candidate_sources_bound']['evidence']['schema_transition'] == [7, 8],
                  'schema-changing application claim differs')
    proof.require(value['migrations'] == migrations() and set(value['manifests']) == {'7', '8'}
                  and set(value['catalogs']) == set(CATALOGS), 'exact migration history/catalog phases missing')
    shapes = {}
    for position in (7, 8):
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
                      'guard refusal or compatible008 restore changed durable records')
    proof.require(proof.artifact(directory, value['catalogs']['guard008_before'])
                  == proof.artifact(directory, value['catalogs']['guard008_after']), 'old007 refusal changed008 catalog/history')
    proof.require(proof.strict_json(proof.artifact(directory, value['rollback_originals'])) == before,
                  'coordinated007 reversal changed original records')
    rollback = proof.strict_json(proof.artifact(directory, value['rollback_media']))
    prior_outputs = report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence']['results']
    proof.require(rollback['before'] == rollback['after'] and rollback['before']
                  and isinstance(rollback['decoded_retained_outputs'], list)
                  and len(rollback['decoded_retained_outputs']) == 3
                  and [item['sha256'] for item in rollback['decoded_retained_outputs']]
                  == [item['sha256'] for item in prior_outputs]
                  and all(proof.HASH.fullmatch(item['sha256']) for item in rollback['decoded_retained_outputs']),
                  'coordinated007 reverse lost registered media or retained downloads')
    validate_runtime_grants(proof.strict_json(proof.artifact(directory, value['runtime_grants'])))
    fixture = proof.strict_json(proof.artifact(directory, value['project_fixture']))
    validate_project_state(proof.strict_json(proof.artifact(directory, value['restore_before'])), fixture)
    validate_project_state(proof.strict_json(proof.artifact(directory, value['restore_after'])), fixture)
    validate_project_state(proof.strict_json(proof.artifact(directory, value['project_after_startup'])), fixture)
    proof.require(type(value['guard_exit']) is int and value['guard_exit'] == 1
                  and value['guard_code'] == 'schema_incompatible'
                  and value['reverse_method'] == 'fresh_restore_coordinated007_backup'
                  and value['compatible_recovery_image_id'] == report['images']['current']['image_id'],
                  'guard/reverse/compatible recovery method differs')


def acceptance_class(base):
    proof = base.transition_module()
    current = base.current_installation_module()
    cleanup = base.source_media_cleanup_module()

    class AgentProjectsTransition(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(getattr(args, 'mode', None) == MODE, 'explicit agent-project transition required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=6, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['agent_projects_transition'] = {'catalogs': {}, 'manifests': {}}

        def raw(self, name, value):
            data = value if isinstance(value, bytes) else proof.canonical(value)
            path = self.output / name
            proof.require(not path.exists(), 'immutable transition receipt cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'immutable007 predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'immutable007 predecessor application differs')
            validate_schema_inventory(self.admitted_old, self.admitted_current)
            super().initialize_host()
            self.rollback = self.make_project(self.old_repo, 'rollback007', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.transition = self.report['agent_projects_transition']
            self.transition['admission'] = {'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                'baseline_commit': BASELINE, 'baseline_tree': BASELINE_TREE,
                'candidate_commit': self.args.revision, 'candidate_tree': self.report['checkout']['tree'],
                'candidate_application_files': len(self.admitted_current),
                'candidate_application_sha256': proof.digest(proof.canonical(self.admitted_current)),
                'captured_before_image_builds': True, 'schema_transition': [7, 8]}
            self.transition['migrations'] = migrations()
            for position in (7, 8):
                self.transition['manifests'][str(position)] = self.raw(f'projects-manifest-{position}.json',
                    (self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            self.save()

        def application_identity(self):
            proof.require(current.application(self.old_source) == self.admitted_old
                          and current.application(self.current_source) == self.admitted_current,
                          'built source differs from admitted candidate')
            for name, project in (('old', self.old), ('current', self.current)):
                self.transition[name + '_source'] = self.raw('projects-' + name + '-source.json', (project['state'] / 'SOURCE.json').read_bytes())
            self.check('projects_candidate_sources_bound', {'baseline_commit': BASELINE, 'candidate_commit': self.args.revision,
                'candidate_application_sha256': self.transition['admission']['candidate_application_sha256'], 'schema_transition': [7, 8]})

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
            receipt = self.raw('projects-catalog-' + phase + '.json', value)
            self.transition['catalogs'][phase] = receipt
            return receipt

        def snapshot(self, project):
            result = super().snapshot(project)
            for name in ('source_media_cleanup', 'source_media_readers', 'source_metadata_cache', 'source_metadata_resolutions', *NEW_TABLES):
                if self.sql(project, "SELECT to_regclass('" + name + "') IS NOT NULL").strip() == b't':
                    result[name] = proof.strict_json(self.sql(project, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + name + ' t'))
            return result

        def seed_runtime_projects(self):
            source, owner = 'acceptance_projects_' + self.token, 'acceptance_projects_owner_' + self.token
            project, job = 'acceptance_project_' + self.token, 'acceptance_project_job_' + self.token
            # These identifiers, hashes and keys belong only to a synthetic
            # unexposed fixture. No session cookie or usable handoff is exported.
            statement = f"""BEGIN;
INSERT INTO sources(id,owner,title,duration_ms,kind,path,url) VALUES('{source}','{owner}','Synthetic project008',10000,'platform','','https://media.example/synthetic008');
INSERT INTO jobs(id,owner,source_id,request,items,status,stage) VALUES('{job}','{owner}','{source}','{{"source_id":"{source}","ranges":[{{"start_ms":1000,"end_ms":2000}}],"format":"mp4","quality":"best","cut_mode":"accurate","output_mode":"joined"}}','[]','failed','finished');
INSERT INTO source_preparations(owner,key,intent_hash,kind,source_id,claim_token,state,lease_until,fingerprint) VALUES('{owner}','{PREPARATION_KEY}',repeat('a',64),'import','{source}','synthetic-claim-008','ready',clock_timestamp()+interval '24 hours','{{"kind":"provider","canonical_url_sha256":"{'c' * 64}"}}');
INSERT INTO source_fingerprints(source_id,owner,fingerprint) VALUES('{source}','{owner}','{{"kind":"provider","canonical_url_sha256":"{'c' * 64}"}}');
INSERT INTO agent_projects(id,owner,source_id,plan,source_snapshot,rendition,collection_key) VALUES('{project}','{owner}','{source}','{{"version":1,"clips":[{{"id":"opening","ranges":[{{"start_ms":1000,"end_ms":2000}}]}}]}}','{{"id":"{source}","kind":"platform","duration_ms":10000}}','final','{COLLECTION_KEY}');
INSERT INTO agent_project_jobs(project_id,job_id,clip_id) VALUES('{project}','{job}','opening');
INSERT INTO agent_project_handoffs(token_hash,project_id,recipient,redeemed_at,expires_at) VALUES('{TOKEN_HASH}','{project}','acceptance-recipient-008',clock_timestamp(),clock_timestamp()+interval '24 hours');
INSERT INTO agent_project_grants(project_id,recipient,expires_at) VALUES('{project}','acceptance-recipient-008',clock_timestamp()+interval '24 hours');
INSERT INTO agent_project_retries(project_id,key,intent_hash,request,attempt_key,clip_intents,clip_job_keys) VALUES('{project}','{RETRY_KEY}',repeat('d',64),'{{"revision":1,"clip_ids":["opening"]}}','{ATTEMPT_KEY}','{{"opening":"{'e' * 64}"}}','{{"opening":"{ATTEMPT_KEY}"}}');
SAVEPOINT inserted_project_fixture;
"""
            updates = {'source_preparations': ("error_code='synthetic-updated'", "error_code='synthetic-updated'"),
                       'source_fingerprints': ("owner='synthetic-updated'", "owner='synthetic-updated'"),
                       'agent_projects': ('revision=2', 'revision=2'),
                       'agent_project_jobs': ("clip_id='synthetic-updated'", "clip_id='synthetic-updated'"),
                       'agent_project_handoffs': ("recipient='synthetic-updated'", "recipient='synthetic-updated'"),
                       'agent_project_grants': ("recipient='synthetic-updated'", "recipient='synthetic-updated'"),
                       'agent_project_retries': ("attempt_key='synthetic-updated'", "attempt_key='synthetic-updated'")}
            for table, (update, condition) in updates.items():
                statement += f"UPDATE {table} SET {update};\nDO $$ BEGIN IF (SELECT count(*) FROM {table} WHERE {condition})<>1 THEN RAISE EXCEPTION 'runtime008 UPDATE SELECT failed'; END IF; END $$;\n"
            for table in ('agent_project_retries', 'agent_project_grants', 'agent_project_handoffs', 'agent_project_jobs', 'agent_projects', 'source_fingerprints', 'source_preparations'):
                statement += f"DELETE FROM {table};\nDO $$ BEGIN IF EXISTS(SELECT 1 FROM {table}) THEN RAISE EXCEPTION 'runtime008 DELETE failed'; END IF; END $$;\n"
            statement += "ROLLBACK TO SAVEPOINT inserted_project_fixture;\nSELECT jsonb_build_object('role',current_user,'session_role',session_user,'schema_owner',(SELECT nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_namespace WHERE nspname='public'),'schema_create',has_schema_privilege('public','CREATE'),'tables',jsonb_build_object("
            tables = []
            for table in NEW_TABLES:
                tables.append(f"'{table}',jsonb_build_object('table_owner',(SELECT relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class WHERE oid='{table}'::regclass),'select',has_table_privilege('{table}','SELECT'),'insert',has_table_privilege('{table}','INSERT'),'update',has_table_privilege('{table}','UPDATE'),'delete',has_table_privilege('{table}','DELETE'),'crud_assertions',true,'retained_rows',(SELECT count(*) FROM {table}))")
            statement += ','.join(tables) + '));\nCOMMIT;'
            observed = proof.strict_json(self.sql(self.current, statement, runtime=True))
            validate_runtime_grants(observed)
            self.transition['runtime_grants'] = self.raw('projects-runtime-grants.json', observed)
            snapshot = self.snapshot(self.current)
            fixture = dict(source_id=source, owner=owner, project_id=project, job_id=job,
                           rows={table: snapshot[table] for table in NEW_TABLES})
            validate_project_state(snapshot, fixture)
            self.transition['project_fixture'] = self.raw('projects-synthetic-fixture.json', fixture)

        def backup_and_upgrade(self):
            super().backup_and_upgrade()
            self.group('agent_projects_upgrade', self.supplemental_checks)

        def supplemental_checks(self):
            self.wrapper(self.current, 'stop')
            project_originals(self.before007, self.initial008)
            self.capture_catalog('restored008', self.restored, 8)
            self.check('additive008_actual_catalog_and_originals')
            self.transition['guard_before'] = self.raw('projects-guard-before.json', self.snapshot(self.current))
            self.capture_catalog('guard008_before', self.current, 8)
            override = self.work / 'projects-old007-guard.json'
            override.write_bytes(proof.canonical({'services': {'migrate': {'image': self.report['images']['old']['image_id']}}}))
            override.chmod(0o600)
            code, stdout, stderr = self.run(self.compose(self.current) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate', '--json-errors', 'maintenance'], expected=None, timeout=30)
            diagnostics = [proof.strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            proof.require(code == 1 and not stdout.strip() and len(diagnostics) == 1
                          and diagnostics[0]['error']['code'] == 'schema_incompatible', 'old007 guard did not refuse008')
            self.transition.update(guard_exit=code, guard_code='schema_incompatible')
            self.transition['guard_after'] = self.raw('projects-guard-after.json', self.snapshot(self.current))
            self.capture_catalog('guard008_after', self.current, 8)
            self.check('old007_guard_refuses008_without_mutation')
            proof.require(self.project_resources(self.rollback) == [], 'rollback must use fresh resources')
            self.wrapper(self.rollback, 'adopt-image', '--from-state', self.old['state'])
            self.wrapper(self.rollback, 'restore', '--directory', self.old_backup)
            restored = self.snapshot(self.rollback)
            proof.require(restored == self.before007, 'coordinated007 backup changed records')
            self.transition['rollback_originals'] = self.raw('projects-rollback007.json', restored)
            self.capture_catalog('rollback007', self.rollback, 7)
            self.policy(self.rollback, 'apply'); self.wrapper(self.rollback, 'initialize'); self.wrapper(self.rollback, 'up')
            owner, foreign = [base.Client(self.rollback['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
            owner.cookie, foreign.cookie = self.a.cookie, self.b.cookie
            decoded = self.verify_retained(self.rollback, owner, foreign, prefix='rollback007')
            _, actual_files = self.accounting(self.rollback)
            proof.require(actual_files == self.before007_files, 'coordinated007 restore changed media bytes')
            self.transition['rollback_media'] = self.raw('projects-rollback007-media.json',
                dict(before=self.before007_files, after=actual_files, decoded_retained_outputs=decoded))
            self.transition['reverse_method'] = 'fresh_restore_coordinated007_backup'
            self.wrapper(self.rollback, 'stop')
            self.check('fresh007_coordinated_reverse')
            self.transition['restore_before'] = self.raw('projects-restore008-before.json', self.restore_snapshot)
            self.transition['restore_after'] = self.raw('projects-restore008-after.json', self.actual_restore008)
            self.transition['compatible_recovery_image_id'] = self.report['images']['restored']['image_id']
            self.check('compatible008_fresh_restore')
            fixture = proof.strict_json(proof.artifact(self.output, self.transition['project_fixture']))
            validate_project_state(self.restore_snapshot, fixture)
            validate_project_state(self.actual_restore008, fixture)
            running_state = self.snapshot(self.restored)
            validate_project_state(running_state, fixture)
            self.transition['project_after_startup'] = self.raw('projects-after-restored-startup.json', running_state)
            self.check('preparation_project_grants_and_retry_restored')
            self.check('runtime_agent_project_tables_crud')
            self.wrapper(self.current, 'up')

        def wrapper(self, project, action, *extra, expected=0):
            if action == 'backup' and project is self.old and expected == 0:
                _, self.before007_files = self.accounting(project)
            result = super().wrapper(project, action, *extra, expected=expected)
            if action == 'backup' and project is self.old and expected == 0:
                proof.require(all(not self.container(project, role)['State']['Running'] for role in ('api', 'worker')),
                              'old007 backup writers still active')
                self.before007 = self.snapshot(project)
                self.transition['originals_before'] = self.raw('projects-originals007.json', self.before007)
                self.capture_catalog('old007', project, 7)
                active = self.active_group
                self.group('agent_projects_upgrade', lambda: self.check('old007_actual_catalog'), complete=False)
                self.active_group = active
            if action == 'initialize' and project is self.current and expected == 0 and 'originals_after' not in self.transition:
                self.initial008 = self.snapshot(project)
                project_originals(self.before007, self.initial008)
                self.transition['originals_after'] = self.raw('projects-originals008.json', self.initial008)
                self.capture_catalog('current008', project, 8)
                self.seed_runtime_projects()
            if action == 'restore' and project is self.restored and expected == 0:
                self.actual_restore008 = self.snapshot(project)
            return result

    return AgentProjectsTransition
