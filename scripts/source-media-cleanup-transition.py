#!/usr/bin/env python3
"""Explicit 005 -> 006 public-profile acceptance; historical modes stay immutable.

Only the existing disposable-host driver may execute host operations. This mode
binds the exact candidate, retains original SQL/manifests 001-005, validates the
actual catalog and tests coordinated old restoration plus compatible006 recovery.
"""
from __future__ import annotations

from copy import deepcopy
import importlib.util
import json
from pathlib import Path
import re

MODE = 'source-media-cleanup-005-to-006'
BASELINE = '1d684b4b0918138190439fc4ce223ff1bc22de40'
BASELINE_TREE = 'c568f55433cd2be92e9e5dffc956e1f1dca6cadb'
BASELINE_APPLICATION_SHA256 = 'd1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6'
BASELINE_FILES = 173
MIGRATION = '20261012-source-media-cleanup-v1'
SQL_NAME = 'internal/app/migrations/006_20261012_source_media_cleanup_v1.sql'
SHAPE_NAME = 'internal/app/migrations/006_schema.json'
SQL_SHA256 = 'e2d36a97a35a158dead1ed47519209ca67eb92fe0a9fffef4ceb9ad50c34d7bb'
CHECKS = ('old005_actual_catalog', 'additive006_actual_catalog_and_originals',
          'old005_guard_refuses006_without_mutation', 'fresh005_coordinated_reverse',
          'compatible006_fresh_restore', 'automatic_cleanup_preserves_downloads')


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def groups(original):
    result = dict(original)
    result['identity'] = tuple('cleanup_candidate_sources_bound' if name == 'application_sources_unchanged'
                               else name for name in original['identity'])
    result['source_media_cleanup_upgrade'] = CHECKS
    return result


def validate_schema_inventory(old, current):
    proof = module('cleanup_inventory_proof', 'source-retention-transition.py')
    frozen = {name: value for name, value in old.items()
              if re.fullmatch(r'internal/app/migrations/00[1-5]_[^/]+\.(sql|json)', name)}
    proof.require(len(frozen) == 10 and all(current.get(name) == value for name, value in frozen.items()),
                  'historical001-005 schema inputs changed')
    additions = {name for name in current if re.fullmatch(r'internal/app/migrations/[0-9]{3}_[^/]+\.(sql|json)', name)} - set(frozen)
    proof.require(additions == {SQL_NAME, SHAPE_NAME} and current[SQL_NAME]['sha256'] == SQL_SHA256,
                  'only the explicitly reviewed migration006 is admitted')


def project_originals(before, after):
    """Require every original record unchanged; only false source media flag is added."""
    proof = module('cleanup_original_proof', 'source-retention-transition.py')
    for table, rows in before.items():
        indexed = {row.get('id', row.get('path')): row for row in after[table]}
        for row in rows:
            observed = deepcopy(indexed.get(row.get('id', row.get('path'))))
            proof.require(observed is not None, 'original record removed by migration006')
            if table == 'sources':
                proof.require(observed.pop('media_removed', None) is False,
                              'migration006 removed original media')
            proof.require(proof.canonical(observed) == proof.canonical(row), 'migration006 changed historical record values')


def original_media_deleted(sql, source_path, physical_exists):
    proof=module('cleanup_deleted_proof','source-retention-transition.py')
    literal="'"+source_path.replace("'","''")+"'"
    # psql prints a bare SQL boolean as t/f. Request a JSON scalar explicitly
    # so strict decoding preserves the same charged-file and physical-file gate.
    charged=proof.strict_json(sql("SELECT to_jsonb(NOT EXISTS(SELECT 1 FROM storage_files WHERE path="+literal+"))"))
    present=physical_exists(source_path)
    proof.require(type(charged) is bool and type(present) is bool,'original deletion observation must contain booleans')
    return charged is True and present is False


def validate_catalog(value, position, expected, migrations):
    proof = module('cleanup_catalog_proof', 'source-retention-transition.py')
    proof.require(value['position'] == position and type(value['position']) is int
                  and type(value['postgres_version_num']) is int and value['postgres_version_num'] == 170011
                  and proof.canonical(proof.normalize_shape(value['shape'])) == proof.canonical(proof.normalize_shape(expected)),
                  'actual catalog differs from declared migration position')
    proof.require(value['versions'] == sorted(m['version'] for m in migrations[:position])
                  and proof.canonical(value['migrations']) == proof.canonical(migrations[:position]), 'actual ordered history/checksums differ')


def validate_release_transition(report, directory):
    proof = module('cleanup_receipts', 'source-retention-transition.py')
    current = module('cleanup_current_sources', 'current-installation-acceptance.py')
    value = report.get('source_media_cleanup_transition', {})
    proof.require(report.get('mode') == MODE and type(report.get('schema_version')) is int and report.get('schema_version') == 4,
                  'explicit migration006 mode required')
    admission = value['admission']
    proof.require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
                  and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
                  and admission['candidate_commit'] == report['expected_checkout']
                  and admission['candidate_tree'] == report['checkout']['tree']
                  and admission['captured_before_image_builds'] is True
                  and admission['schema_transition'] == [5, 6], 'candidate source/transition binding differs')
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
                  'immutable005 predecessor changed')
    proof.require(type(admission['candidate_application_files']) is int and admission['candidate_application_files'] == len(sources['current'])
                  and admission['candidate_application_sha256'] == proof.digest(proof.canonical(sources['current'])),
                  'candidate application inventory differs')
    validate_schema_inventory(sources['old'], sources['current'])
    proof.require(report['runtime_application_sources_unchanged'] is False
                  and report['groups']['identity']['checks']['cleanup_candidate_sources_bound']['evidence']['schema_transition'] == [5, 6],
                  'schema-changing application claim differs')
    shapes = {p: proof.strict_json(proof.artifact(directory, value['manifests'][str(p)])) for p in (5, 6)}
    for p in (5, 6):
        path = f'internal/app/migrations/{p:03d}_schema.json'
        proof.require(value['manifests'][str(p)]['sha256'] == sources['current'][path]['sha256'], 'catalog manifest source differs')
    for phase, position in (('old005', 5), ('current006', 6), ('restored006', 6), ('rollback005', 5)):
        catalog = proof.strict_json(proof.artifact(directory, value['catalogs'][phase]))
        validate_catalog(catalog, position, shapes[position], value['migrations'])
        proof.require(catalog['context'] == admission, 'catalog was borrowed from another candidate')
    before = proof.strict_json(proof.artifact(directory, value['originals_before']))
    after = proof.strict_json(proof.artifact(directory, value['originals_after']))
    project_originals(before, after)
    proof.require(proof.artifact(directory, value['guard_before']) == proof.artifact(directory, value['guard_after']),
                  'guard refusal mutated006 records')
    proof.require(proof.strict_json(proof.artifact(directory, value['rollback_originals'])) == before,
                  'coordinated005 reversal changed original records')
    proof.require(proof.artifact(directory, value['restore_before']) == proof.artifact(directory, value['restore_after']),
                  'compatible006 restore changed durable lifecycle state')
    cleanup = proof.strict_json(proof.artifact(directory, value['cleanup']))
    proof.require(cleanup['source']['media_removed'] is True and cleanup['job']['status'] == 'succeeded'
                  and cleanup['source_id'] == cleanup['job']['request']['source_id']
                  and cleanup['decoded']['sha256'] == cleanup['download_sha256']
                  and cleanup['foreign_status'] == 404 and cleanup['removed_status'] == 410
                  and cleanup['retained_job_id'] == cleanup['job']['id']
                  and cleanup['physical_original_removed'] is True,
                  'automatic cleanup/source ownership/result download proof incomplete')
    proof.require(type(value['guard_exit']) is int and value['guard_exit'] == 1 and value['guard_code'] == 'schema_incompatible'
                  and value['reverse_method'] == 'fresh_restore_coordinated005_backup'
                  and value['compatible_recovery_image_id'] == report['images']['current']['image_id'],
                  'guard/reverse/compatible recovery method differs')


def acceptance_class(base):
    proof = base.transition_module()
    current = base.current_installation_module()

    class CleanupTransition(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(getattr(args, 'mode', None) == MODE, 'explicit cleanup transition required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=4, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['source_media_cleanup_transition'] = {'catalogs': {}, 'manifests': {}}

        def raw(self, name, value):
            data = value if isinstance(value, bytes) else proof.canonical(value)
            path = self.output / name
            proof.require(not path.exists(), 'immutable transition receipt cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'immutable005 predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'immutable005 predecessor application differs')
            validate_schema_inventory(self.admitted_old, self.admitted_current)
            super().initialize_host()
            self.rollback = self.make_project(self.old_repo, 'rollback005', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.transition = self.report['source_media_cleanup_transition']
            self.transition['admission'] = {'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                'baseline_commit': BASELINE, 'baseline_tree': BASELINE_TREE,
                'candidate_commit': self.args.revision, 'candidate_tree': self.report['checkout']['tree'],
                'candidate_application_files': len(self.admitted_current),
                'candidate_application_sha256': proof.digest(proof.canonical(self.admitted_current)),
                'captured_before_image_builds': True, 'schema_transition': [5, 6]}
            self.transition['migrations'] = [dict(position=i+1, version=v, checksum=d) for i,(v,_,d) in enumerate(proof.MIGRATIONS)] + [dict(position=6,version=MIGRATION,checksum=SQL_SHA256)]
            for p in (5, 6):
                self.transition['manifests'][str(p)] = self.raw('cleanup-manifest-' + str(p) + '.json',
                    (self.current_repo / f'internal/app/migrations/{p:03d}_schema.json').read_bytes())
            self.save()

        def application_identity(self):
            proof.require(current.application(self.old_source) == self.admitted_old
                          and current.application(self.current_source) == self.admitted_current,
                          'built source differs from admitted candidate')
            for name, project in (('old', self.old), ('current', self.current)):
                self.transition[name + '_source'] = self.raw('cleanup-' + name + '-source.json', (project['state'] / 'SOURCE.json').read_bytes())
            self.check('cleanup_candidate_sources_bound', {'baseline_commit': BASELINE, 'candidate_commit': self.args.revision,
                'candidate_application_sha256': self.transition['admission']['candidate_application_sha256'], 'schema_transition': [5, 6]})

        def capture_catalog(self, phase, project, position):
            latest = proof.strict_json((self.current_repo / SHAPE_NAME).read_bytes())
            query = re.search(r'const schemaShapeSQL = `([^`]+)`', (self.current_repo / 'internal/app/migration_catalog.go').read_text())[1]
            array = lambda names: 'ARRAY[' + ','.join("'" + n + "'" for n in sorted(names)) + ']::text[]'
            query = query.replace('$1::text[]', array(latest['Tables'])).replace('$2::text[]', array(latest['Functions']))
            shape = proof.strict_json(self.sql(project, query))
            history = proof.strict_json(self.sql(project, "SELECT jsonb_build_object('versions',(SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions),'migrations',(SELECT jsonb_agg(jsonb_build_object('position',position,'version',version,'checksum',checksum) ORDER BY position) FROM app_schema_migrations))"))
            value = dict(position=position,shape=shape,context=self.transition['admission'],postgres_version_num=int(self.sql(project,"SELECT current_setting('server_version_num')")),**history)
            expected = proof.strict_json((self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            validate_catalog(value, position, expected, self.transition['migrations'])
            receipt = self.raw('cleanup-catalog-' + phase + '.json', value)
            self.transition['catalogs'][phase] = receipt
            return receipt

        def snapshot(self, project):
            result = super().snapshot(project)
            if self.sql(project, "SELECT to_regclass('source_media_cleanup') IS NOT NULL").strip() == b't':
                for name in ('source_media_cleanup', 'source_media_readers'):
                    result[name] = proof.strict_json(self.sql(project, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]') FROM " + name + ' t'))
            return result

        def backup_and_upgrade(self):
            super().backup_and_upgrade()
            # Supplemental checks execute after the standard actual image+media
            # upgrade and fresh006 dump/media restore, with retained downloads.
            self.group('source_media_cleanup_upgrade', self.supplemental_checks)

        def supplemental_checks(self):
            self.wrapper(self.current, 'stop')
            project_originals(self.before005, self.initial006)
            self.capture_catalog('restored006', self.restored, 6)
            self.check('additive006_actual_catalog_and_originals')
            self.transition['guard_before'] = self.raw('cleanup-guard-before.json', self.snapshot(self.current))
            # Same project/volumes; old005 maintenance must refuse before writes.
            override=self.work/'cleanup-old005-guard.json'
            override.write_bytes(proof.canonical({'services':{'migrate':{'image':self.report['images']['old']['image_id']}}}));override.chmod(0o600)
            code,stdout,stderr=self.run(self.compose(self.current)+['-f',override,'run','--rm','-T','--no-deps','migrate','--json-errors','maintenance'],expected=None,timeout=30)
            diagnostics=[proof.strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            proof.require(code==1 and not stdout.strip() and len(diagnostics)==1 and diagnostics[0]['error']['code']=='schema_incompatible','old005 guard did not refuse006')
            self.transition.update(guard_exit=code,guard_code='schema_incompatible')
            self.transition['guard_after'] = self.raw('cleanup-guard-after.json', self.snapshot(self.current))
            self.check('old005_guard_refuses006_without_mutation')
            proof.require(self.project_resources(self.rollback) == [], 'rollback must use fresh resources')
            self.wrapper(self.rollback, 'adopt-image', '--from-state', self.old['state'])
            self.wrapper(self.rollback, 'restore', '--directory', self.old_backup)
            restored = self.snapshot(self.rollback)
            proof.require(restored == self.before005, 'coordinated005 backup changed records')
            self.transition['rollback_originals'] = self.raw('cleanup-rollback005.json', restored)
            self.capture_catalog('rollback005', self.rollback, 5)
            self.policy(self.rollback,'apply');self.wrapper(self.rollback,'initialize');self.wrapper(self.rollback,'up')
            owner,foreign=[base.Client(self.rollback['config']['CUTMY_DOMAIN'],self.ca,self.http.append,self.secret) for _ in range(2)]
            owner.cookie,foreign.cookie=self.a.cookie,self.b.cookie
            self.verify_retained(self.rollback,owner,foreign,prefix='rollback005')
            self.transition['reverse_method']='fresh_restore_coordinated005_backup'
            self.wrapper(self.rollback,'stop')
            self.check('fresh005_coordinated_reverse')
            self.transition['restore_before']=self.raw('cleanup-restore006-before.json', self.restore_snapshot)
            # Read the fresh restore snapshot captured by the standard flow,
            # before subsequent new test exports modify it.
            self.transition['restore_after']=self.raw('cleanup-restore006-after.json',self.actual_restore006)
            self.transition['compatible_recovery_image_id']=self.report['images']['restored']['image_id']
            self.check('compatible006_fresh_restore')
            self.wrapper(self.current,'up')
            source=self.a.upload(self.input)
            source_path=proof.strict_json(self.sql(self.current,"SELECT to_jsonb(path) FROM sources WHERE id='"+source['id']+"'"))
            proof.require(source_path.startswith('/data/sources/'),'unexpected original source path')
            body={'source_id':source['id'],'ranges':[{'start_ms':0,'end_ms':1000,'label':'cleanup'}], 'format':'mp4','quality':'best','cut_mode':'accurate','cleanup_source':True}
            job,_=self.a.request('POST','/api/v1/jobs',data=body,headers={'Idempotency-Key':'cleanup-'+self.token},expected=202)
            result={}
            def finished():
                value,_=self.a.request('GET','/api/v1/jobs/'+job['id']);result.update(value);return value['status']=='succeeded'
            self.wait(finished,60)
            removed={}
            def cleaned():
                value,_=self.a.request('GET','/api/v1/sources/'+source['id']);removed.update(value);return value.get('media_removed') is True
            self.wait(cleaned,30)
            content,_=self.a.request('GET',result['items'][0]['artifact']['download_url'])
            self.b.request('GET',result['items'][0]['artifact']['download_url'],expected=404)
            self.a.request('GET','/api/v1/sources/'+source['id']+'/media',expected=410)
            retained,_=self.a.request('GET','/api/v1/jobs/'+job['id'])
            def deleted_physically():
                return original_media_deleted(lambda query:self.sql(self.current,query),source_path,
                    lambda path:self.json(self.compose(self.current)+['exec','-T','api','python3','-c','import os,json,sys;print(json.dumps(os.path.exists(sys.argv[1])))',path]))
            self.wait(deleted_physically,30)
            physical=True
            decoded=self.decode(content,'cleanup-result.mp4',seconds=1.0)
            self.transition['cleanup']=self.raw('cleanup-download-proof.json',dict(source_id=source['id'],source=removed,job=result,decoded=decoded,download_sha256=proof.digest(content),foreign_status=404,removed_status=410,retained_job_id=retained['id'],physical_original_removed=physical))
            self.check('automatic_cleanup_preserves_downloads')

        def wrapper(self,project,action,*extra,expected=0):
            result=super().wrapper(project,action,*extra,expected=expected)
            if action=='backup' and project is self.old and expected==0:
                proof.require(all(not self.container(project,role)['State']['Running'] for role in ('api','worker')),'old005 backup writers still active')
                self.before005=self.snapshot(project)
                self.transition['originals_before']=self.raw('cleanup-originals005.json',self.before005)
                self.capture_catalog('old005',project,5)
                active=self.active_group
                self.group('source_media_cleanup_upgrade',lambda:self.check('old005_actual_catalog'),complete=False)
                self.active_group=active
            if action=='initialize' and project is self.current and expected==0 and 'originals_after' not in self.transition:
                self.initial006=self.snapshot(project)
                project_originals(self.before005,self.initial006)
                self.transition['originals_after']=self.raw('cleanup-originals006.json',self.initial006)
                self.capture_catalog('current006',project,6)
            if action=='restore' and project is self.restored and expected==0:
                self.actual_restore006=self.snapshot(project)
            return result

    return CleanupTransition
