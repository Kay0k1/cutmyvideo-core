#!/usr/bin/env python3
"""Pinned 008 -> 009 public-profile acceptance; historical modes stay fixed.

Host operations remain in the existing explicitly disposable-host driver. This
mode binds all current application sources, an immutable008 predecessor and
the exact009 SQL/catalog pair, then exercises both restoration directions.
"""
from __future__ import annotations

from copy import deepcopy
import importlib.util
from pathlib import Path
import re
import struct

MODE = 'external-audio-008-to-009'
BASELINE = '106d0d2d7887540768cc21e3bd4e2c71629af6c4'
BASELINE_TREE = '93a0d8b88a6570042ea21f0f6a3fcc28df81f04f'
BASELINE_APPLICATION_SHA256 = '975b17eef624e36f6fe77849199ecde6fc9bb16bba2e4e5b0e297c9638a1a4f0'
BASELINE_FILES = 411
MIGRATION = '20261018-external-audio-refs-v1'
SQL_NAME = 'internal/app/migrations/009_20261018_external_audio_refs_v1.sql'
SHAPE_NAME = 'internal/app/migrations/009_schema.json'
SQL_SHA256 = '3304ef39020de23eb9546e27bb9ecd74eea0cb7b67ec5620cb111afa8f41d58c'
SHAPE_SHA256 = 'd9a7bed95e55ef83fb6f23db9af34776af6a77ca568546dc7d64e8da5f241033'
CHECKS = ('old008_actual_catalog', 'additive009_actual_catalog_and_originals',
          'old008_guard_refuses009_without_mutation', 'fresh008_coordinated_reverse',
          'compatible009_fresh_restore', 'runtime_external_audio_pin_crud',
          'external_audio_references_and_results_restored',
          'external_audio_http_worker_and_owner_guards')
CATALOGS = {'old008': 8, 'current009': 9, 'guard009_before': 9,
            'guard009_after': 9, 'rollback008': 8, 'restored009': 9}
AUXILIARY_TABLES = ('source_preparations', 'source_fingerprints', 'agent_projects',
              'agent_project_jobs', 'agent_project_handoffs', 'agent_project_grants',
              'agent_project_retries')


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def migrations():
    previous = module('audio_previous_history', 'agent-projects-transition.py')
    return previous.migrations() + [dict(position=9, version=MIGRATION, checksum=SQL_SHA256)]


def groups(original):
    result = dict(original)
    result['identity'] = tuple('audio_candidate_sources_bound' if name == 'application_sources_unchanged'
                               else name for name in original['identity'])
    result['external_audio_upgrade'] = CHECKS
    return result


def validate_schema_inventory(old, current):
    proof = module('metadata_schema_inventory', 'source-retention-transition.py')
    frozen = {name: value for name, value in old.items()
              if re.fullmatch(r'internal/app/migrations/00[1-8]_[^/]+\.(sql|json)', name)}
    proof.require(len(frozen) == 16 and all(current.get(name) == value for name, value in frozen.items()),
                  'historical001-008 schema inputs changed')
    schema_files = {name for name in current if name.startswith('internal/app/migrations/')
                    and name.endswith(('.sql', '.json'))}
    proof.require(schema_files - set(frozen) == {SQL_NAME, SHAPE_NAME}
                  and current[SQL_NAME]['sha256'] == SQL_SHA256
                  and current[SHAPE_NAME]['sha256'] == SHAPE_SHA256,
                  'only the explicitly reviewed migration009 SQL and catalog are admitted')


def audio_originals(before, after):
    proof = module('audio_original_rows', 'source-retention-transition.py')
    proof.require(isinstance(before, dict) and isinstance(after, dict)
                  and set(before) == set(after), '009 changed the managed table inventory')
    expected = deepcopy(before)
    proof.require(isinstance(expected.get('jobs'), list), 'legacy job rows missing')
    for row in expected['jobs']:
        proof.require('audio_source_id' not in row, '008 unexpectedly contains009 membership')
        row['audio_source_id'] = None
    proof.require(after == expected, '009 changed old rows or backfilled external references')


def validate_runtime_grants(value):
    proof = module('audio_grant_proof', 'source-retention-transition.py')
    checks = ('derived_reference', 'insert_pins', 'update_pins', 'delete_unpins', 'column_guard')
    proof.require(isinstance(value, dict)
                  and set(value) == {'role', 'session_role', 'schema_owner', 'schema_create', 'tables', 'checks'}
                  and value['role'] == value['session_role'] == 'cutmy_runtime'
                  and value['schema_owner'] is False and value['schema_create'] is False
                  and set(value['tables']) == {'sources', 'jobs'}
                  and set(value['checks']) == set(checks)
                  and all(value['checks'][key] is True for key in checks),
                  'restricted external-audio role or actual pin CRUD/guard proof differs')
    for row in value['tables'].values():
        proof.require(row == dict(table_owner=False, select=True, insert=True, update=True, delete=True)
                      and all(type(item) is bool for item in row.values()),
                      'restricted runtime source/job privileges differ')


def validate_audio_state(snapshot, fixture):
    proof = module('audio_state_proof', 'source-retention-transition.py')
    proof.require(isinstance(fixture, dict)
                  and set(fixture) == {'primary_source_id', 'audio_source_id', 'owner', 'job_id', 'source_row', 'job_row'}
                  and re.fullmatch(r'acceptance_audio_source_[a-z0-9]+', fixture['audio_source_id'])
                  and re.fullmatch(r'acceptance_audio_job_[a-z0-9]+', fixture['job_id'])
                  and fixture['primary_source_id'] != fixture['audio_source_id'],
                  'external-audio fixture identity differs')
    source = [row for row in snapshot['sources'] if row['id'] == fixture['audio_source_id']]
    primary = [row for row in snapshot['sources'] if row['id'] == fixture['primary_source_id']]
    job = [row for row in snapshot['jobs'] if row['id'] == fixture['job_id']]
    proof.require(source == [fixture['source_row']] and job == [fixture['job_row']]
                  and len(primary) == 1
                  and source[0]['owner'] == primary[0]['owner'] == job[0]['owner'] == fixture['owner']
                  and source[0]['kind'] == primary[0]['kind'] == 'upload'
                  and source[0]['path'] == primary[0]['path'] and source[0]['path'].startswith('/data/')
                  and source[0]['media_removed'] is False
                  and source[0]['retention_job_pins'] == 1 and source[0]['retention_pins_valid'] is True
                  and job[0]['source_id'] == fixture['primary_source_id']
                  and job[0]['audio_source_id'] == fixture['audio_source_id']
                  and job[0]['status'] == 'failed'
                  and job[0]['request'] == {'source_id': fixture['primary_source_id'],
                      'ranges': [{'start_ms': 1000, 'end_ms': 2500}], 'format': 'mp4',
                      'quality': 'best', 'cut_mode': 'accurate', 'output_mode': 'joined',
                      'audio_replace': {'source_id': fixture['audio_source_id'], 'offset_ms': 0}},
                  'external recording, retained request, derived reference or truthful pins changed')


def validate_http_render(value):
    proof = module('audio_http_proof', 'source-retention-transition.py')
    proof.require(isinstance(value, dict) and set(value) == {'source_id', 'audio_source_id', 'job_id',
                  'artifact_id', 'sha256', 'bytes', 'duration_ms', 'canvas', 'integrated_lufs',
                  'true_peak_dbtp', 'audio_frequency_hz', 'foreign_source_status', 'foreign_download_status', 'range_bytes'}
                  and value['source_id'] != value['audio_source_id']
                  and all(isinstance(value[k], str) and value[k] for k in ('source_id', 'audio_source_id', 'job_id', 'artifact_id'))
                  and proof.HASH.fullmatch(value['sha256'])
                  and type(value['bytes']) is int and value['bytes'] > 32
                  and type(value['duration_ms']) is int and abs(value['duration_ms'] - 1500) <= 250
                  and value['canvas'] == [180, 320]
                  and type(value['integrated_lufs']) is float and abs(value['integrated_lufs'] + 16) <= 1
                  and type(value['true_peak_dbtp']) is float and value['true_peak_dbtp'] <= -0.5
                  and type(value['audio_frequency_hz']) is float and abs(value['audio_frequency_hz'] - 997) <= 5
                  and value['foreign_source_status'] == value['foreign_download_status'] == 404
                  and value['range_bytes'] == 16, 'actual HTTPS external-audio render/owner/media proof differs')


def validate_release_transition(report, directory):
    proof = module('metadata_receipts', 'source-retention-transition.py')
    current = module('metadata_source_tree', 'current-installation-acceptance.py')
    cleanup = module('metadata_catalogs', 'source-media-cleanup-transition.py')
    value = report.get('external_audio_transition', {})
    proof.require(report.get('mode') == MODE and type(report.get('schema_version')) is int
                  and report['schema_version'] == 7, 'explicit migration009 mode required')
    proof.require(report['cleanup'].get('private_state_backup_config_keys_removed') is True
                  and report['cleanup'].get('private_workspace_retained') is False,
                  'external-audio transition private cleanup incomplete')
    admission = value['admission']
    proof.require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
                  and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
                  and admission['candidate_commit'] == report['expected_checkout']
                  and admission['candidate_tree'] == report['checkout']['tree']
                  and admission['captured_before_image_builds'] is True
                  and admission['schema_transition'] == [8, 9], 'candidate source/transition binding differs')
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
                  'immutable008 predecessor changed')
    proof.require(type(admission['candidate_application_files']) is int
                  and admission['candidate_application_files'] == len(sources['current'])
                  and admission['candidate_application_sha256'] == proof.digest(proof.canonical(sources['current'])),
                  'candidate application inventory differs')
    validate_schema_inventory(sources['old'], sources['current'])
    proof.require(report['runtime_application_sources_unchanged'] is False
                  and report['scope']['schema_changing_upgrade'] is True
                  and report['groups']['identity']['checks']['audio_candidate_sources_bound']['evidence']['schema_transition'] == [8, 9],
                  'schema-changing application claim differs')
    proof.require(value['migrations'] == migrations() and set(value['manifests']) == {'8', '9'}
                  and set(value['catalogs']) == set(CATALOGS), 'exact migration history/catalog phases missing')
    shapes = {}
    for position in (8, 9):
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
    audio_originals(before, after)
    for first, second in (('guard_before', 'guard_after'), ('restore_before', 'restore_after')):
        proof.require(proof.artifact(directory, value[first]) == proof.artifact(directory, value[second]),
                      'guard refusal or compatible009 restore changed durable records')
    proof.require(proof.artifact(directory, value['catalogs']['guard009_before'])
                  == proof.artifact(directory, value['catalogs']['guard009_after']), 'old008 refusal changed009 catalog/history')
    proof.require(proof.strict_json(proof.artifact(directory, value['rollback_originals'])) == before,
                  'coordinated008 reversal changed original records')
    rollback = proof.strict_json(proof.artifact(directory, value['rollback_media']))
    prior_outputs = report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence']['results']
    proof.require(rollback['before'] == rollback['after'] and rollback['before']
                  and isinstance(rollback['decoded_retained_outputs'], list)
                  and len(rollback['decoded_retained_outputs']) == 3
                  and [item['sha256'] for item in rollback['decoded_retained_outputs']]
                  == [item['sha256'] for item in prior_outputs]
                  and all(proof.HASH.fullmatch(item['sha256']) for item in rollback['decoded_retained_outputs']),
                  'coordinated008 reverse lost registered media or retained downloads')
    validate_runtime_grants(proof.strict_json(proof.artifact(directory, value['runtime_grants'])))
    validate_http_render(proof.strict_json(proof.artifact(directory, value['http_render'])))
    fixture = proof.strict_json(proof.artifact(directory, value['audio_fixture']))
    validate_audio_state(proof.strict_json(proof.artifact(directory, value['restore_before'])), fixture)
    validate_audio_state(proof.strict_json(proof.artifact(directory, value['restore_after'])), fixture)
    validate_audio_state(proof.strict_json(proof.artifact(directory, value['audio_after_startup'])), fixture)
    proof.require(type(value['guard_exit']) is int and value['guard_exit'] == 1
                  and value['guard_code'] == 'schema_incompatible'
                  and value['reverse_method'] == 'fresh_restore_coordinated008_backup'
                  and value['compatible_recovery_image_id'] == report['images']['current']['image_id'],
                  'guard/reverse/compatible recovery method differs')


def acceptance_class(base):
    proof = base.transition_module()
    current = base.current_installation_module()
    cleanup = base.source_media_cleanup_module()

    class ExternalAudioTransition(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(getattr(args, 'mode', None) == MODE, 'explicit external-audio transition required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=7, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['external_audio_transition'] = {'catalogs': {}, 'manifests': {}}

        def raw(self, name, value):
            data = value if isinstance(value, bytes) else proof.canonical(value)
            path = self.output / name
            proof.require(not path.exists(), 'immutable transition receipt cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'immutable008 predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'immutable008 predecessor application differs')
            validate_schema_inventory(self.admitted_old, self.admitted_current)
            super().initialize_host()
            self.rollback = self.make_project(self.old_repo, 'rollback008', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.transition = self.report['external_audio_transition']
            self.transition['admission'] = {'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                'baseline_commit': BASELINE, 'baseline_tree': BASELINE_TREE,
                'candidate_commit': self.args.revision, 'candidate_tree': self.report['checkout']['tree'],
                'candidate_application_files': len(self.admitted_current),
                'candidate_application_sha256': proof.digest(proof.canonical(self.admitted_current)),
                'captured_before_image_builds': True, 'schema_transition': [8, 9]}
            self.transition['migrations'] = migrations()
            for position in (8, 9):
                self.transition['manifests'][str(position)] = self.raw(f'audio-manifest-{position}.json',
                    (self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            self.save()

        def application_identity(self):
            proof.require(current.application(self.old_source) == self.admitted_old
                          and current.application(self.current_source) == self.admitted_current,
                          'built source differs from admitted candidate')
            for name, project in (('old', self.old), ('current', self.current)):
                self.transition[name + '_source'] = self.raw('audio-' + name + '-source.json', (project['state'] / 'SOURCE.json').read_bytes())
            self.check('audio_candidate_sources_bound', {'baseline_commit': BASELINE, 'candidate_commit': self.args.revision,
                'candidate_application_sha256': self.transition['admission']['candidate_application_sha256'], 'schema_transition': [8, 9]})

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
            receipt = self.raw('audio-catalog-' + phase + '.json', value)
            self.transition['catalogs'][phase] = receipt
            return receipt

        def snapshot(self, project):
            result = super().snapshot(project)
            for name in ('source_media_cleanup', 'source_media_readers', 'source_metadata_cache', 'source_metadata_resolutions', *AUXILIARY_TABLES):
                if self.sql(project, "SELECT to_regclass('" + name + "') IS NOT NULL").strip() == b't':
                    result[name] = proof.strict_json(self.sql(project, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + name + ' t'))
            return result

        def seed_runtime_audio(self):
            audio, job = 'acceptance_audio_source_' + self.token, 'acceptance_audio_job_' + self.token
            primary = self.source_a['id']
            # The controlled database-only pin fixture references the already
            # registered media file. It creates no uncharged duplicate bytes and
            # is never passed through physical deletion or new worker encoding.
            statement = f"""BEGIN;
INSERT INTO sources(id,owner,title,duration_ms,kind,path,width,height)
 SELECT '{audio}',owner,'Synthetic external recording009',duration_ms,kind,path,width,height FROM sources WHERE id='{primary}';
INSERT INTO jobs(id,owner,source_id,request,items,status,stage)
 SELECT '{job}',owner,'{primary}','{{"source_id":"{primary}","ranges":[{{"start_ms":1000,"end_ms":2500}}],"format":"mp4","quality":"best","cut_mode":"accurate","output_mode":"joined","audio_replace":{{"source_id":"{audio}","offset_ms":0}}}}','[]','failed','finished' FROM sources WHERE id='{primary}';
DO $$ BEGIN
 IF (SELECT audio_source_id FROM jobs WHERE id='{job}') IS DISTINCT FROM '{audio}' OR (SELECT retention_job_pins FROM sources WHERE id='{audio}')<>1 THEN RAISE EXCEPTION 'runtime009 derived INSERT pin failed'; END IF;
END $$;
SAVEPOINT inserted_audio_fixture;
UPDATE jobs SET request=jsonb_set(request,'{{audio_replace,offset_ms}}','123'::jsonb) WHERE id='{job}';
DO $$ BEGIN
 IF (SELECT request#>>'{{audio_replace,offset_ms}}' FROM jobs WHERE id='{job}')<>'123' OR (SELECT retention_job_pins FROM sources WHERE id='{audio}')<>1 THEN RAISE EXCEPTION 'runtime009 UPDATE pin failed'; END IF;
 BEGIN UPDATE jobs SET audio_source_id=NULL WHERE id='{job}'; RAISE EXCEPTION 'runtime009 column guard bypassed'; EXCEPTION WHEN check_violation THEN NULL; END;
END $$;
DELETE FROM jobs WHERE id='{job}';
DO $$ BEGIN IF EXISTS(SELECT 1 FROM jobs WHERE id='{job}') OR (SELECT retention_job_pins FROM sources WHERE id='{audio}')<>0 THEN RAISE EXCEPTION 'runtime009 DELETE pin failed'; END IF; END $$;
ROLLBACK TO SAVEPOINT inserted_audio_fixture;
SELECT jsonb_build_object('role',current_user,'session_role',session_user,'schema_owner',(SELECT nspowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_namespace WHERE nspname='public'),'schema_create',has_schema_privilege('public','CREATE'),'tables',jsonb_build_object(
 'sources',jsonb_build_object('table_owner',(SELECT relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class WHERE oid='sources'::regclass),'select',has_table_privilege('sources','SELECT'),'insert',has_table_privilege('sources','INSERT'),'update',has_table_privilege('sources','UPDATE'),'delete',has_table_privilege('sources','DELETE')),
 'jobs',jsonb_build_object('table_owner',(SELECT relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class WHERE oid='jobs'::regclass),'select',has_table_privilege('jobs','SELECT'),'insert',has_table_privilege('jobs','INSERT'),'update',has_table_privilege('jobs','UPDATE'),'delete',has_table_privilege('jobs','DELETE'))),
 'checks',jsonb_build_object('derived_reference',true,'insert_pins',true,'update_pins',true,'delete_unpins',true,'column_guard',true));
COMMIT;"""
            observed = proof.strict_json(self.sql(self.current, statement, runtime=True))
            validate_runtime_grants(observed)
            self.transition['runtime_grants'] = self.raw('audio-runtime-grants.json', observed)
            snapshot = self.snapshot(self.current)
            source_row = next(row for row in snapshot['sources'] if row['id'] == audio)
            job_row = next(row for row in snapshot['jobs'] if row['id'] == job)
            fixture = dict(primary_source_id=primary, audio_source_id=audio, owner=source_row['owner'],
                           job_id=job, source_row=source_row, job_row=job_row)
            validate_audio_state(snapshot, fixture)
            self.transition['audio_fixture'] = self.raw('audio-synthetic-fixture.json', fixture)

        def external_http_render(self):
            owner, foreign = self.restore_a, self.restore_b
            replacement = self.work / 'external-audio-input.mp4'
            self.run(['ffmpeg', '-v', 'error', '-nostdin', '-i', self.input, '-f', 'lavfi', '-i',
                      'sine=frequency=997:sample_rate=48000:duration=4', '-map', '0:v:0', '-map', '1:a:0',
                      '-c:v', 'copy', '-c:a', 'aac', '-t', '4', replacement])
            primary, audio = owner.upload(self.input), owner.upload(replacement)
            body = dict(source_id=primary['id'], ranges=[dict(start_ms=1000, end_ms=2500)],
                        format='mp4', quality='best', cut_mode='accurate', output_mode='joined',
                        audio_replace=dict(source_id=audio['id'], offset_ms=0),
                        audio_normalize=dict(integrated_lufs=-16, true_peak_dbtp=-1, loudness_range_lu=11),
                        canvas=dict(width=180, height=320, fit='contain', background='#000000'))
            foreign.request('POST', '/api/v1/jobs', data=body, expected=404)
            created, _ = owner.request('POST', '/api/v1/jobs', data=body, expected=202)
            completed = None
            def finished():
                nonlocal completed
                completed, _ = owner.request('GET', '/api/v1/jobs/' + created['id'])
                proof.require(completed['status'] in ('queued', 'running', 'succeeded'), 'external HTTPS worker failed')
                return completed['status'] == 'succeeded'
            self.wait(finished, 90)
            artifact = completed['items'][0]['artifact']
            foreign.request('GET', artifact['download_url'], expected=404)
            data, _ = owner.request('GET', artifact['download_url'])
            partial, _ = owner.request('GET', artifact['download_url'], headers={'Range': 'bytes=0-15'}, expected=206)
            path = self.output / 'external-audio-restored.mp4'
            proof.require(not path.exists() and len(data) == artifact['size_bytes'], 'new external artifact bytes differ')
            path.write_bytes(data)
            media = self.json(['ffprobe', '-v', 'error', '-show_streams', '-show_format', '-of', 'json', path])
            pictures = [s for s in media['streams'] if s.get('codec_type') == 'video']
            tracks = [s for s in media['streams'] if s.get('codec_type') == 'audio']
            proof.require(len(pictures) == len(tracks) == 1, 'new external streams missing')
            self.run(['ffmpeg', '-v', 'error', '-xerror', '-i', path, '-f', 'null', '-'])
            code, _, stats = self.run(['ffmpeg', '-hide_banner', '-nostats', '-i', path, '-af', 'ebur128=peak=true', '-f', 'null', '-'], expected=None)
            summary = stats.decode('utf-8', 'replace').rsplit('Summary:', 1)[-1]
            integrated = re.search(r'I:\s+(-?[0-9.]+) LUFS', summary)
            peak = re.search(r'Peak:\s+(-?[0-9.]+) dBFS', summary)
            proof.require(code == 0 and integrated and peak, 'independent external output loudness unavailable')
            pcm = self.run(['ffmpeg', '-v', 'error', '-nostdin', '-i', path, '-map', '0:a:0', '-ac', '1',
                            '-ar', '8000', '-f', 's16le', '-'])
            samples = struct.unpack('<' + 'h' * (len(pcm) // 2), pcm)
            middle = samples[len(samples)//4:3*len(samples)//4]
            proof.require(len(middle) >= 4000, 'external frequency witness is too short')
            frequency = sum(left <= 0 < right for left, right in zip(middle, middle[1:])) * 8000 / len(middle)
            value = dict(source_id=primary['id'], audio_source_id=audio['id'], job_id=created['id'], artifact_id=artifact['id'],
                         sha256=proof.digest(data), bytes=len(data), duration_ms=round(float(media['format']['duration'])*1000),
                         canvas=[pictures[0]['width'], pictures[0]['height']], integrated_lufs=float(integrated[1]),
                         true_peak_dbtp=float(peak[1]), audio_frequency_hz=float(frequency),
                         foreign_source_status=404, foreign_download_status=404, range_bytes=len(partial))
            validate_http_render(value)
            self.transition['http_render'] = self.raw('audio-https-render.json', value)
            self.check('external_audio_http_worker_and_owner_guards', value)

        def backup_and_upgrade(self):
            super().backup_and_upgrade()
            self.group('external_audio_upgrade', self.supplemental_checks)

        def supplemental_checks(self):
            self.wrapper(self.current, 'stop')
            audio_originals(self.before008, self.initial009)
            self.capture_catalog('restored009', self.restored, 9)
            self.check('additive009_actual_catalog_and_originals')
            self.transition['guard_before'] = self.raw('audio-guard-before.json', self.snapshot(self.current))
            self.capture_catalog('guard009_before', self.current, 9)
            override = self.work / 'audio-old008-guard.json'
            override.write_bytes(proof.canonical({'services': {'migrate': {'image': self.report['images']['old']['image_id']}}}))
            override.chmod(0o600)
            code, stdout, stderr = self.run(self.compose(self.current) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate', '--json-errors', 'maintenance'], expected=None, timeout=30)
            diagnostics = [proof.strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            proof.require(code == 1 and not stdout.strip() and len(diagnostics) == 1
                          and diagnostics[0]['error']['code'] == 'schema_incompatible', 'old008 guard did not refuse009')
            self.transition.update(guard_exit=code, guard_code='schema_incompatible')
            self.transition['guard_after'] = self.raw('audio-guard-after.json', self.snapshot(self.current))
            self.capture_catalog('guard009_after', self.current, 9)
            self.check('old008_guard_refuses009_without_mutation')
            proof.require(self.project_resources(self.rollback) == [], 'rollback must use fresh resources')
            self.wrapper(self.rollback, 'adopt-image', '--from-state', self.old['state'])
            self.wrapper(self.rollback, 'restore', '--directory', self.old_backup)
            restored = self.snapshot(self.rollback)
            proof.require(restored == self.before008, 'coordinated008 backup changed records')
            self.transition['rollback_originals'] = self.raw('audio-rollback008.json', restored)
            self.capture_catalog('rollback008', self.rollback, 8)
            self.policy(self.rollback, 'apply'); self.wrapper(self.rollback, 'initialize'); self.wrapper(self.rollback, 'up')
            owner, foreign = [base.Client(self.rollback['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
            owner.cookie, foreign.cookie = self.a.cookie, self.b.cookie
            decoded = self.verify_retained(self.rollback, owner, foreign, prefix='rollback008')
            _, actual_files = self.accounting(self.rollback)
            proof.require(actual_files == self.before008_files, 'coordinated008 restore changed media bytes')
            self.transition['rollback_media'] = self.raw('audio-rollback008-media.json',
                dict(before=self.before008_files, after=actual_files, decoded_retained_outputs=decoded))
            self.transition['reverse_method'] = 'fresh_restore_coordinated008_backup'
            self.wrapper(self.rollback, 'stop')
            self.check('fresh008_coordinated_reverse')
            self.transition['restore_before'] = self.raw('audio-restore009-before.json', self.restore_snapshot)
            self.transition['restore_after'] = self.raw('audio-restore009-after.json', self.actual_restore009)
            self.transition['compatible_recovery_image_id'] = self.report['images']['restored']['image_id']
            self.check('compatible009_fresh_restore')
            fixture = proof.strict_json(proof.artifact(self.output, self.transition['audio_fixture']))
            validate_audio_state(self.restore_snapshot, fixture)
            validate_audio_state(self.actual_restore009, fixture)
            running_state = self.snapshot(self.restored)
            validate_audio_state(running_state, fixture)
            self.transition['audio_after_startup'] = self.raw('audio-after-restored-startup.json', running_state)
            self.check('external_audio_references_and_results_restored')
            self.check('runtime_external_audio_pin_crud')
            self.external_http_render()
            self.wrapper(self.current, 'up')

        def wrapper(self, project, action, *extra, expected=0):
            if action == 'backup' and project is self.old and expected == 0:
                _, self.before008_files = self.accounting(project)
            result = super().wrapper(project, action, *extra, expected=expected)
            if action == 'backup' and project is self.old and expected == 0:
                proof.require(all(not self.container(project, role)['State']['Running'] for role in ('api', 'worker')),
                              'old008 backup writers still active')
                self.before008 = self.snapshot(project)
                self.transition['originals_before'] = self.raw('audio-originals008.json', self.before008)
                self.capture_catalog('old008', project, 8)
                active = self.active_group
                self.group('external_audio_upgrade', lambda: self.check('old008_actual_catalog'), complete=False)
                self.active_group = active
            if action == 'initialize' and project is self.current and expected == 0 and 'originals_after' not in self.transition:
                self.initial009 = self.snapshot(project)
                audio_originals(self.before008, self.initial009)
                self.transition['originals_after'] = self.raw('audio-originals009.json', self.initial009)
                self.capture_catalog('current009', project, 9)
                self.seed_runtime_audio()
            if action == 'restore' and project is self.restored and expected == 0:
                self.actual_restore009 = self.snapshot(project)
            return result

    return ExternalAudioTransition
