#!/usr/bin/env python3
"""Pinned schema009 ->010 public-profile upgrade and coordinated recovery."""
from __future__ import annotations

from copy import deepcopy
import importlib.util
from pathlib import Path
import re
import stat
import zlib

MODE = 'poster-artifacts-009-to-010'
BASELINE = 'ca334380ef6f8a8aa9e336b2817eb183501528b6'
BASELINE_TREE = '5ddccece0576288d819c79e2fdf06794eb09b956'
BASELINE_APPLICATION_SHA256 = 'b1d450ea60c9284f3f6ecf478b5dfd70613b2ff414717218c3fab7b532ca1a37'
BASELINE_FILES = 456
MIGRATION = '20261018-poster-artifacts-v1'
SQL_NAME = 'internal/app/migrations/010_20261018_poster_artifacts_v1.sql'
SHAPE_NAME = 'internal/app/migrations/010_schema.json'
# Filled from the reviewed, final migration bytes before acceptance can run.
SQL_SHA256 = '29bbb3c1c0c2de5e9fb3232c9b9e6098e50ec8b0287c476087e39ffa27012cbf'
SHAPE_SHA256 = '29cf7670809ec69362084b443106e1ea89e20032f1514b8ffcb4a2e139624c31'
NEW_TABLES = ('image_artifacts', 'artifact_readers')
CHECKS = ('old009_actual_catalog', 'additive010_actual_catalog_and_originals',
          'old009_guard_refuses010_without_mutation', 'fresh009_coordinated_reverse',
          'compatible010_fresh_restore', 'poster_metadata_bytes_and_ownership_restored',
          'restricted_runtime_poster_grants')
CATALOGS = {'old009': 9, 'current010': 10, 'guard010_before': 10,
            'guard010_after': 10, 'rollback009': 9, 'restored010': 10}


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(filename))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def migrations():
    return module('poster_previous_history', 'external-audio-transition.py').migrations() + [
        dict(position=10, version=MIGRATION, checksum=SQL_SHA256)]


def groups(original):
    result = dict(original)
    result['identity'] = tuple('poster_candidate_sources_bound' if key == 'application_sources_unchanged'
                               else key for key in original['identity'])
    result['poster_upgrade'] = CHECKS
    return result


def validate_schema_inventory(old, current):
    proof = module('poster_inventory', 'source-retention-transition.py')
    frozen = {name: value for name, value in old.items()
              if re.fullmatch(r'internal/app/migrations/00[1-9]_[^/]+\.(sql|json)', name)}
    proof.require(len(frozen) == 18 and all(current.get(name) == value for name, value in frozen.items()),
                  'historical001-009 migration inputs changed')
    schema = {name for name in current if name.startswith('internal/app/migrations/')
              and name.endswith(('.sql', '.json'))}
    proof.require(schema - set(frozen) == {SQL_NAME, SHAPE_NAME}
                  and current[SQL_NAME]['sha256'] == SQL_SHA256
                  and current[SHAPE_NAME]['sha256'] == SHAPE_SHA256,
                  'only reviewed migration010 SQL/catalog inputs are admitted')


def originals(before, after):
    proof = module('poster_originals', 'source-retention-transition.py')
    proof.require(isinstance(before, dict) and isinstance(after, dict)
                  and not set(NEW_TABLES) & set(before)
                  and set(after) == set(before) | set(NEW_TABLES)
                  and all(after[name] == [] for name in NEW_TABLES)
                  and {name: after[name] for name in before} == before,
                  '010 changed original rows or created initial poster state')



def poster_image_artifact(directory, receipt):
    """Read only the bounded PNG emitted by this poster recovery operation."""
    proof = module('poster_binary_receipt', 'source-retention-transition.py')
    proof.require(directory is not None and isinstance(receipt, dict)
                  and set(receipt) == {'path', 'bytes', 'sha256'}
                  and receipt['path'] == 'poster-ready.png'
                  and type(receipt['bytes']) is int and 33 <= receipt['bytes'] <= 16 * 1024 * 1024
                  and isinstance(receipt['sha256'], str) and proof.HASH.fullmatch(receipt['sha256']),
                  'poster image evidence identity invalid')
    path = Path(directory) / receipt['path']
    info = path.lstat()
    proof.require(stat.S_ISREG(info.st_mode) and not path.is_symlink()
                  and info.st_size == receipt['bytes'], 'poster image evidence size/type changed')
    data = path.read_bytes()
    proof.require(proof.digest(data) == receipt['sha256']
                  and data[:16] == b'\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR'
                  and zlib.crc32(data[12:29]) == int.from_bytes(data[29:33], 'big'),
                  'poster image evidence bytes/header changed')
    width, height = int.from_bytes(data[16:20], 'big'), int.from_bytes(data[20:24], 'big')
    proof.require(0 < width <= 16384 and 0 < height <= 16384 and width * height <= 16777216,
                  'poster image evidence dimensions invalid')
    return data


def validate_restored_poster(poster, image, retained):
    proof = module('poster_restore_binding', 'source-retention-transition.py')
    proof.require(poster['before'] == poster['after'] and poster['before']['sha256']
                  == proof.digest(image)
                  and poster['owner_download'] == 200 and poster['foreign_download'] == 404
                  and poster['range_bytes'] == 16 and poster['range_sha256'] == proof.digest(image[:16])
                  and poster['decoded_width'] == poster['after']['width'] == int.from_bytes(image[16:20], 'big')
                  and poster['decoded_height'] == poster['after']['height'] == int.from_bytes(image[20:24], 'big'), 'restored poster bytes/ownership/decoding missing')
    rows = [row for row in retained['image_artifacts'] if row['id'] == poster['after']['id']]
    proof.require(len(rows) == 1 and all(rows[0][key] == poster['after'][key] for key in
                  ('id', 'parent_artifact_id', 'filename', 'format', 'size_bytes', 'width', 'height', 'timestamp_ms'))
                  and any(parent['id'] == rows[0]['parent_artifact_id'] and parent['owner'] == rows[0]['owner']
                          for parent in retained['artifacts'])
                  and poster['after']['size_bytes'] == len(image),
                  'restored HTTP poster metadata differs from the actual restored parent/image rows')


def validate_release_transition(report, directory):
    proof = module('poster_receipts', 'source-retention-transition.py')
    current = module('poster_sources', 'current-installation-acceptance.py')
    catalog = module('poster_catalogs', 'source-media-cleanup-transition.py')
    value = report.get('poster_transition', {})
    admission = value['admission']
    proof.require(report.get('mode') == MODE and report['schema_version'] == 8
                  and report['runtime_application_sources_unchanged'] is False
                  and report['scope']['schema_changing_upgrade'] is True,
                  'explicit schema010 transition scope missing')
    proof.require(admission['provider'] == report['provider'] and admission['checkout'] == report['checkout']
                  and admission['baseline_commit'] == BASELINE and admission['baseline_tree'] == BASELINE_TREE
                  and admission['candidate_commit'] == report['expected_checkout']
                  and admission['candidate_tree'] == report['checkout']['tree']
                  and admission['schema_transition'] == [9, 10]
                  and admission['captured_before_image_builds'] is True,
                  'actual poster transition source admission differs')
    inventories = {}
    for label in ('old', 'current'):
        raw = proof.artifact(directory, value[label + '_source'])
        source = proof.strict_json(raw)
        image = report['images'][label]
        proof.require(source['commit'] == image['source_commit'] and source['tree'] == image['tree']
                      and current.source_tree(source['files']) == source['tree']
                      and proof.digest(raw) == image['source_sha256'], 'poster image/source receipt differs')
        inventories[label] = current.application(source)
    proof.require(len(inventories['old']) == BASELINE_FILES
                  and proof.digest(proof.canonical(inventories['old'])) == BASELINE_APPLICATION_SHA256,
                  'immutable schema009 predecessor differs')
    proof.require(admission['candidate_application_files'] == len(inventories['current'])
                  and admission['candidate_application_sha256'] == proof.digest(proof.canonical(inventories['current'])),
                  'poster candidate application inventory differs')
    validate_schema_inventory(inventories['old'], inventories['current'])
    proof.require(value['migrations'] == migrations() and set(value['manifests']) == {'9', '10'}
                  and set(value['catalogs']) == set(CATALOGS), 'poster migration phases missing')
    shapes = {}
    for position in (9, 10):
        receipt = value['manifests'][str(position)]
        shapes[position] = proof.strict_json(proof.artifact(directory, receipt))
        proof.require(receipt['sha256'] == inventories['current'][f'internal/app/migrations/{position:03d}_schema.json']['sha256'],
                      'poster catalog manifest differs from tested source')
    for phase, position in CATALOGS.items():
        observed = proof.strict_json(proof.artifact(directory, value['catalogs'][phase]))
        catalog.validate_catalog(observed, position, shapes[position], migrations())
        proof.require(observed['context'] == admission, 'poster catalog borrowed from another context')
    before = proof.strict_json(proof.artifact(directory, value['originals_before']))
    originals(before, proof.strict_json(proof.artifact(directory, value['originals_after'])))
    proof.require(proof.strict_json(proof.artifact(directory, value['rollback_originals'])) == before,
                  'coordinated009 reversal changed original records')
    for first, second in (('guard_before', 'guard_after'), ('restore_before', 'restore_after')):
        proof.require(proof.artifact(directory, value[first]) == proof.artifact(directory, value[second]),
                      'old009 refusal or compatible010 restore changed durable rows')
    proof.require(proof.artifact(directory, value['catalogs']['guard010_before'])
                  == proof.artifact(directory, value['catalogs']['guard010_after']),
                  'old009 refusal changed schema010 catalog or migration history')
    poster = proof.strict_json(proof.artifact(directory, value['poster']))
    image = poster_image_artifact(directory, value['poster_image'])
    retained = proof.strict_json(proof.artifact(directory, value['restore_after']))
    validate_restored_poster(poster, image, retained)
    grants = proof.strict_json(proof.artifact(directory, value['runtime_grants']))
    proof.require(grants['role'] == grants['session_role'] == 'cutmy_runtime'
                  and grants['schema_create'] is False and set(grants['tables']) == set(NEW_TABLES)
                  and all(row == dict(owner=False, select=True, insert=True, update=True, delete=True)
                          for row in grants['tables'].values()), 'restricted poster runtime privileges differ')
    rollback = proof.strict_json(proof.artifact(directory, value['rollback_media']))
    prior_outputs = report['groups']['https_media']['checks']['mp4_mp3_copy_decode_ownership_range']['evidence']['results']
    proof.require(rollback['before'] == rollback['after'] and rollback['before']
                  and len(rollback['decoded_retained_outputs']) == 3
                  and [row['sha256'] for row in rollback['decoded_retained_outputs']]
                  == [row['sha256'] for row in prior_outputs],
                  'coordinated009 reversal lost retained media')
    proof.require(value['guard_exit'] == 1 and value['guard_code'] == 'schema_incompatible'
                  and value['reverse_method'] == 'fresh_restore_coordinated009_backup'
                  and value['compatible_recovery_image_id'] == report['images']['current']['image_id'],
                  'poster refusal/recovery method differs')


def acceptance_class(base):
    proof = base.transition_module()
    current = base.current_installation_module()
    catalogs = base.source_media_cleanup_module()
    prior = base.external_audio_module()

    class PosterTransition(base.Acceptance):
        upgrade_from = BASELINE

        def __init__(self, args):
            proof.require(args.mode == MODE, 'explicit poster transition mode required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=8, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['poster_transition'] = {'catalogs': {}, 'manifests': {}}

        def raw(self, name, value):
            data = value if isinstance(value, bytes) else proof.canonical(value)
            path = self.output / name
            proof.require(not path.exists(), 'transition evidence cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': proof.digest(data)}

        def initialize_host(self):
            proof.require(self.git(base.REPO, 'rev-parse', BASELINE + '^{tree}') == BASELINE_TREE,
                          'schema009 predecessor tree differs')
            self.admitted_old = proof.application_inventory(self.run, base.REPO, BASELINE)
            self.admitted_current = proof.application_inventory(self.run, base.REPO, self.args.revision)
            proof.require(len(self.admitted_old) == BASELINE_FILES
                          and proof.digest(proof.canonical(self.admitted_old)) == BASELINE_APPLICATION_SHA256,
                          'schema009 predecessor application differs')
            validate_schema_inventory(self.admitted_old, self.admitted_current)
            super().initialize_host()
            self.rollback = self.make_project(self.old_repo, 'rollback009', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.transition = self.report['poster_transition']
            self.transition['admission'] = dict(provider=deepcopy(self.report['provider']), checkout=deepcopy(self.report['checkout']),
                baseline_commit=BASELINE, baseline_tree=BASELINE_TREE, candidate_commit=self.args.revision,
                candidate_tree=self.report['checkout']['tree'], candidate_application_files=len(self.admitted_current),
                candidate_application_sha256=proof.digest(proof.canonical(self.admitted_current)),
                captured_before_image_builds=True, schema_transition=[9, 10])
            self.transition['migrations'] = migrations()
            for position in (9, 10):
                self.transition['manifests'][str(position)] = self.raw(f'poster-manifest-{position}.json',
                    (self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            self.save()

        def application_identity(self):
            proof.require(current.application(self.old_source) == self.admitted_old
                          and current.application(self.current_source) == self.admitted_current,
                          'built poster sources differ from admitted sources')
            for label, project in (('old', self.old), ('current', self.current)):
                self.transition[label + '_source'] = self.raw('poster-' + label + '-source.json',
                    (project['state'] / 'SOURCE.json').read_bytes())
            self.check('poster_candidate_sources_bound', dict(baseline_commit=BASELINE,
                candidate_commit=self.args.revision, schema_transition=[9, 10],
                candidate_application_sha256=self.transition['admission']['candidate_application_sha256']))

        def capture_catalog(self, phase, project, position):
            latest = proof.strict_json((self.current_repo / SHAPE_NAME).read_bytes())
            query = re.search(r'const schemaShapeSQL = `([^`]+)`',
                              (self.current_repo / 'internal/app/migration_catalog.go').read_text())[1]
            array = lambda names: 'ARRAY[' + ','.join("'" + name + "'" for name in sorted(names)) + ']::text[]'
            query = query.replace('$1::text[]', array(latest['Tables'])).replace('$2::text[]', array(latest['Functions']))
            shape = proof.strict_json(self.sql(project, query))
            history = proof.strict_json(self.sql(project, "SELECT jsonb_build_object('versions',(SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions),'migrations',(SELECT jsonb_agg(jsonb_build_object('position',position,'version',version,'checksum',checksum) ORDER BY position) FROM app_schema_migrations))"))
            value = dict(position=position, shape=shape, context=self.transition['admission'],
                         postgres_version_num=int(self.sql(project, "SELECT current_setting('server_version_num')")), **history)
            expected = proof.strict_json((self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            catalogs.validate_catalog(value, position, expected, migrations())
            self.transition['catalogs'][phase] = self.raw('poster-catalog-' + phase + '.json', value)

        def snapshot(self, project):
            result = super().snapshot(project)
            for name in ('source_media_cleanup', 'source_media_readers', 'source_metadata_cache',
                         'source_metadata_resolutions', *prior.AUXILIARY_TABLES, *NEW_TABLES):
                if self.sql(project, "SELECT to_regclass('" + name + "') IS NOT NULL").strip() == b't':
                    result[name] = proof.strict_json(self.sql(project,
                        "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + name + ' t'))
            return result

        def export(self, client, source, name, **kwargs):
            result = super().export(client, source, name, **kwargs)
            if name == 'upgraded-new-output':
                image, _ = client.request('POST', '/api/v1/artifacts/' + result['artifact']['id'] + '/poster',
                                          data=dict(timestamp_ms=500, format='png'), expected=201)
                data, _ = client.request('GET', image['download_url'])
                self.poster = image
                self.poster_bytes = data
                self.transition['poster_image'] = self.raw('poster-ready.png', data)
            return result

        def wrapper(self, project, action, *extra, expected=0):
            if action == 'backup' and project is self.old and expected == 0:
                _, self.before009_files = self.accounting(project)
            result = super().wrapper(project, action, *extra, expected=expected)
            if action == 'backup' and project is self.old and expected == 0:
                self.before009 = self.snapshot(project)
                self.transition['originals_before'] = self.raw('poster-originals009.json', self.before009)
                self.capture_catalog('old009', project, 9)
                active = self.active_group
                self.group('poster_upgrade', lambda: self.check('old009_actual_catalog'), complete=False)
                self.active_group = active
            if action == 'backup' and project is self.current and expected == 0:
                proof.require(all(not self.container(project, role)['State']['Running'] for role in ('api', 'worker')),
                              'current010 backup writers still active')
                self.restore_snapshot = self.snapshot(project)
                # The base flow hashes all acknowledged files immediately
                # before stopping writers. Validate the quiescent SQL ledger
                # against that inventory without exec'ing the stopped API.
                counter = self.restore_snapshot['storage_counters']
                proof.require(len(counter) == 1
                    and int(counter[0]['stored_bytes']) == sum(int(row['size_bytes']) for row in self.restore_snapshot['storage_files'])
                    and int(counter[0]['reserved_bytes']) == sum(int(row['size_bytes']) for row in self.restore_snapshot['storage_reservations'])
                    and int(counter[0]['stored_bytes']) == self.restore_accounting['stored_bytes']
                    and int(counter[0]['reserved_bytes']) == self.restore_accounting['reserved_bytes'],
                    'stopped010 backup ledger differs from the pre-stop physical inventory')
            if action == 'initialize' and project is self.current and expected == 0 and 'originals_after' not in self.transition:
                initial = self.snapshot(project)
                originals(self.before009, initial)
                self.transition['originals_after'] = self.raw('poster-originals010.json', initial)
                self.capture_catalog('current010', project, 10)
            if action == 'restore' and project is self.restored and expected == 0:
                self.actual_restore010 = self.snapshot(project)
            return result

        def backup_and_upgrade(self):
            super().backup_and_upgrade()
            self.group('poster_upgrade', self.supplemental_checks)

        def supplemental_checks(self):
            self.wrapper(self.current, 'stop')
            self.capture_catalog('restored010', self.restored, 10)
            self.check('additive010_actual_catalog_and_originals')
            self.transition['guard_before'] = self.raw('poster-guard-before.json', self.snapshot(self.current))
            self.capture_catalog('guard010_before', self.current, 10)
            override = self.work / 'poster-old009-guard.json'
            override.write_bytes(proof.canonical({'services': {'migrate': {'image': self.report['images']['old']['image_id']}}}))
            override.chmod(0o600)
            code, stdout, stderr = self.run(self.compose(self.current) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate', '--json-errors', 'maintenance'], expected=None, timeout=30)
            diagnostics = [proof.strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            proof.require(code == 1 and not stdout.strip() and len(diagnostics) == 1
                          and diagnostics[0]['error']['code'] == 'schema_incompatible', 'old009 failed to refuse010')
            self.transition.update(guard_exit=code, guard_code='schema_incompatible')
            self.transition['guard_after'] = self.raw('poster-guard-after.json', self.snapshot(self.current))
            self.capture_catalog('guard010_after', self.current, 10)
            self.check('old009_guard_refuses010_without_mutation')
            self.wrapper(self.rollback, 'adopt-image', '--from-state', self.old['state'])
            self.wrapper(self.rollback, 'restore', '--directory', self.old_backup)
            restored = self.snapshot(self.rollback)
            proof.require(restored == self.before009, 'coordinated009 restore changed records')
            self.transition['rollback_originals'] = self.raw('poster-rollback009.json', restored)
            self.capture_catalog('rollback009', self.rollback, 9)
            self.policy(self.rollback, 'apply'); self.wrapper(self.rollback, 'initialize'); self.wrapper(self.rollback, 'up')
            owner, foreign = [base.Client(self.rollback['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
            owner.cookie, foreign.cookie = self.a.cookie, self.b.cookie
            decoded = self.verify_retained(self.rollback, owner, foreign, prefix='rollback009')
            _, files = self.accounting(self.rollback)
            proof.require(files == self.before009_files, 'coordinated009 restore changed file bytes')
            self.transition['rollback_media'] = self.raw('poster-rollback009-media.json',
                dict(before=self.before009_files, after=files, decoded_retained_outputs=decoded))
            self.transition['reverse_method'] = 'fresh_restore_coordinated009_backup'
            self.wrapper(self.rollback, 'stop')
            self.check('fresh009_coordinated_reverse')
            self.transition['restore_before'] = self.raw('poster-restore010-before.json', self.restore_snapshot)
            self.transition['restore_after'] = self.raw('poster-restore010-after.json', self.actual_restore010)
            self.transition['compatible_recovery_image_id'] = self.report['images']['restored']['image_id']
            self.check('compatible010_fresh_restore')
            data, _ = self.restore_a.request('GET', self.poster['download_url'])
            metadata, _ = self.restore_a.request('GET', '/api/v1/image-artifacts/' + self.poster['id'])
            self.restore_b.request('GET', self.poster['download_url'], expected=404)
            partial, _ = self.restore_a.request('GET', self.poster['download_url'], headers={'Range': 'bytes=0-15'}, expected=206)
            proof.require(data == self.poster_bytes and partial == data[:16] and len(partial) == 16, 'restored poster bytes/range differ')
            info = self.json(['ffprobe', '-v', 'error', '-show_streams', '-of', 'json', self.output / 'poster-ready.png'])
            self.run(['ffmpeg', '-v', 'error', '-xerror', '-i', self.output / 'poster-ready.png', '-frames:v', '1', '-f', 'null', '-'])
            before_image = dict(self.poster, sha256=proof.digest(self.poster_bytes))
            after_image = dict(metadata, sha256=proof.digest(data))
            proof.require(before_image == after_image, 'restored poster HTTP metadata differs')
            self.transition['poster'] = self.raw('poster-restored-proof.json', dict(before=before_image, after=after_image,
                owner_download=200, foreign_download=404, range_bytes=len(partial), range_sha256=proof.digest(partial),
                decoded_width=info['streams'][0]['width'], decoded_height=info['streams'][0]['height']))
            self.check('poster_metadata_bytes_and_ownership_restored')
            grants = proof.strict_json(self.sql(self.restored, "SELECT jsonb_build_object('role',current_user,'session_role',session_user,'schema_create',has_schema_privilege('public','CREATE'),'tables',(SELECT jsonb_object_agg(name,jsonb_build_object('owner',(SELECT relowner=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_class WHERE oid=name::regclass),'select',has_table_privilege(name,'SELECT'),'insert',has_table_privilege(name,'INSERT'),'update',has_table_privilege(name,'UPDATE'),'delete',has_table_privilege(name,'DELETE'))) FROM unnest(ARRAY['image_artifacts','artifact_readers']) AS names(name)))", runtime=True))
            self.transition['runtime_grants'] = self.raw('poster-runtime-grants.json', grants)
            self.check('restricted_runtime_poster_grants')
            self.wrapper(self.current, 'up')

    return PosterTransition
