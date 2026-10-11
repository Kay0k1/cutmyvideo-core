#!/usr/bin/env python3
"""Pinned 004 -> 005 acceptance extension. Importing this module starts nothing.

Actual operations are reached only through the driver's disposable-host gate.
Receipts bind raw observations made by this run; old receipts are never inputs.
"""
from __future__ import annotations

from copy import deepcopy
import hashlib
import json
from pathlib import Path
import re
import stat
import time
from urllib.parse import quote

MODE = 'source-retention-004-to-005'
CANDIDATE = '6023dcbca14b7b6a6f09dd3f4b6da97821ff7e9a'
CANDIDATE_TREE = 'f49f729511d12d3458ca8e7e4c73ec82888647a3'
# SOURCE inventories include git_blob. Root670's 168-file map and its review
# hashes remain historical preparation roots; this pin is the separately
# reviewed recovery and native disk application commit, before the wrapper-only pin update.
APPLICATION_SHA256 = '5383852cb86732ae99b60e1392f85b7f32fd2c44f6a1764e3e32d527bdbb55f4'
OLD_APPLICATION_SHA256 = 'a1aa7b968c06eaeb075d2cd73f8afbf309a1bc12121d1a3e598ba2561e785f90'
OLD_COMMIT = '0f93cd654a998be7740ba1d6fee4a44a6f7b3464'
OLD_TREE = '58a431cf209a36b8acb0052ca77653b480814d48'
OLD_REFERENCE = '06bb74bca90a79fbf399c01cf71f7f44af186431'
POLICY_SHA256 = 'ad653eb4eff9fb8f40b1d733bcab7b6394b98f03a769bab18b126234d861aef0'
PREPARATION = {
    'plan_sha256': '35021688ee2e420ce7135efa2e45a3042f07fdc4533cff5a7bb120de08f5a2f5',
    'source_freeze_sha256': '499cbbaf12714ad68201b5549f4e5a3c2ec8304f1bdfb6461790585ebc37038a',
    'root_review_sha256': '703fba88d8acaea6a1befa5ffcd9ea52cf175b10a77bc292255be8fa805c99d3',
    'root670_review_sha256': '56759ddc1dd8289a4a834df466f6e7e85e23f6f68a29be0414a5ce5e744f8260',
    'root670_public_driver_binding_sha256': '5c6cb808cf900532a012d304c0f168e994a2afd0bfb614c5a961529e232e676f',
    'recovery_application_source_freeze_sha256': 'fc86f8ebd00761c01fb47b577dade04b477ebe04ffe477efd0d20621e80f25e5',
    'root_recovery_application_source_review_sha256': 'b0ca5a8023b85ba7afbf08e2519d146433f4a9933b9cc4913a108f032ff33de3',
    'root_recovery_application_committed_review_sha256': '1023ad3bcd365253782d863b6d059541e86b796ac7d4c48e2076d6cf428a8f6c',
}
MIGRATIONS = (
    ('20261005-storage-queue-v3', '001_20261005_storage_queue_v3.sql', '8630cebd9bf2bb2c038519d807f75456a60d8dea24cc1114304551270b3e949f'),
    ('20261010-ordered-migrations-v1', '002_20261010_ordered_migrations_v1.sql', '17caf6f352044a62dd4bbfee154d02965d9c411ca6783422b96db5eed6b40e57'),
    ('20261010-storage-maintenance-v1', '003_20261010_storage_maintenance_v1.sql', '1c06b5c041a45201496d20655078515b5ca4b7896e2a43c3c848d51b2aa00627'),
    ('20261010-cache-retention-v1', '004_20261010_cache_retention_v1.sql', 'd7654c6a981edea4506d7265057e9349ab9fee85cd3ce3a63412384374e4709a'),
    ('20261011-source-retention-v1', '005_20261011_source_retention_v1.sql', 'b460686dc29fa1a156cbf74ca73da8afd2e4394cc7f2a3d6465f72e047b7c0e0'),
)
SHAPES = {4: '4c5c907e01f11a0810739d614fed44f9c14b6d640b5f07f4f9eec79b6dc3425c',
          5: 'da0357d73e4697ddf656211cda0a6d9c6f0b0bda905673a0041dcd641664d4ae'}
TESTS = (
    'TestSourcePinMembershipTransitionsAndRollback',
    'TestSourcePinBirthBulkAndIDMoveIncludesHistoricalPreview',
    'TestSourcePinFenceSerializesDirectPreviewWithDeletion',
    'TestSourceRetentionFalseZerosContinueToHealthyTail',
    'TestSourcePinAuditPartialRestartRevisionAndFairness',
    'TestSourcePinUnderflowQuarantinesUntilAuthoritativeAudit',
    'TestSourceRetentionLostCommitAcknowledgementKeepsTombstoneCharge',
    'TestSourcePinMigrationBackfillHistoryCancellationAndRestart',
    'TestSourcePinCatalogRejectsBodyPrivilegeColumnAndForeignTargetDrift',
    'TestSourcePinInvokerTriggersWorkWithoutRuntimeFunctionExecute',
    'TestSourcePinCatalogManifestsDeclareExactManagedFunctionsAndTriggerColumns',
    'TestMigrationsConcurrentStartupsApplyOneOrderedChain',
    'TestMigrationsCancellationRollsBackPartialDDLAndRecovers',
)
PACKAGE = 'github.com/Kay0k1/cutmyvideo-core/internal/app'
EXTRA_GROUPS = {
    'transition_schema': ('old004_catalog_roles', 'stopped004_originals_backup',
                          'actual005_catalog_history_backfill', 'migration_deadline_and_rollback',
                          'migration_concurrency_and_lock_cancellation', 'runtime_invoker_and_catalog_drift'),
    'transition_retention': ('all_job_membership_idempotency', 'birth_and_historical_preview',
                             'preview_deletion_fence', 'indexed_false_zero_tail',
                             'partial_audit_restart_revision_fairness', 'underflow_quarantine',
                             'lost_commit_tombstone_charge', 'bounded_plan_and_audit_limits'),
    'transition_recovery': ('a1_guard_refuses005_without_mutation', 'fresh004_backup_reverse_only',
                            'fresh005_catalog_audit_restore', 'fresh005_api_export_accounting'),
}
GO_ORACLES = {
    'migration_deadline_and_rollback': (TESTS[7],),
    'migration_concurrency_and_lock_cancellation': (TESTS[11], TESTS[12]),
    'runtime_invoker_and_catalog_drift': (TESTS[8], TESTS[9]),
    'all_job_membership_idempotency': (TESTS[0],),
    'birth_and_historical_preview': (TESTS[1],),
    'preview_deletion_fence': (TESTS[2],),
    'indexed_false_zero_tail': (TESTS[3],),
    'partial_audit_restart_revision_fairness': (TESTS[4],),
    'underflow_quarantine': (TESTS[5],),
    'lost_commit_tombstone_charge': (TESTS[6],),
    'bounded_plan_and_audit_limits': (TESTS[3], TESTS[4], TESTS[10]),
}
CATALOG_PHASES = {'old004': 4, 'fault004_before': 4, 'fault004_after': 4, 'current005': 5,
                  'guard005_before': 5, 'guard005_after': 5, 'rollback004': 4, 'restored005': 5}
HASH = re.compile(r'[0-9a-f]{64}\Z')


def require(value, message):
    if not value:
        raise RuntimeError(message)


def canonical(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def strict_json(data):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, 'duplicate JSON key')
            result[key] = value
        return result
    return json.loads(data, object_pairs_hook=pairs)


def groups(original):
    result = dict(original)
    result['identity'] = tuple('approved_application_transition' if x == 'application_sources_unchanged' else x
                               for x in original['identity'])
    result.update(EXTRA_GROUPS)
    require(len(result) == 10 and sum(map(len, result.values())) == 53, 'transition group contract changed')
    return result


def artifact(directory, receipt):
    require(directory is not None, 'actual raw evidence directory required')
    require(isinstance(receipt, dict) and set(receipt) == {'path', 'bytes', 'sha256'}
            and isinstance(receipt['path'], str) and re.fullmatch(r'[a-z0-9_-]+\.(?:json|jsonl|txt)', receipt['path'])
            and type(receipt['bytes']) is int and 0 < receipt['bytes'] <= 32 * 1024 * 1024
            and HASH.fullmatch(receipt['sha256']), 'raw evidence identity invalid')
    path = Path(directory) / receipt['path']
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and not path.is_symlink() and info.st_size == receipt['bytes'],
            'raw evidence size/type changed')
    data = path.read_bytes()
    require(digest(data) == receipt['sha256'], 'raw evidence bytes changed')
    return data


def validate_go_events(data):
    events = [strict_json(line) for line in data.splitlines() if line.strip()]
    require(events, 'actual Go evidence empty')
    started, passed, package_pass = set(), {}, 0
    for event in events:
        require(event.get('Package') == PACKAGE and event.get('Action') in
                ('start', 'run', 'pause', 'cont', 'pass', 'output', 'fail', 'skip', 'bench'), 'Go evidence package/action differs')
        action, name = event['Action'], event.get('Test')
        require(action not in ('fail', 'skip'), 'Go failure/skip is missing evidence')
        if name:
            require(isinstance(name, str) and name.split('/')[0] in TESTS, 'unknown Go test oracle')
            if action == 'run':
                require(name not in started, 'duplicate Go test execution')
                started.add(name)
            if action == 'pass':
                require(name in started and name not in passed and type(event.get('Elapsed')) in (int, float)
                        and 0 <= event['Elapsed'] < 240, 'Go pass without unique bounded execution')
                passed[name] = event['Elapsed']
        elif action == 'pass':
            package_pass += 1
    require(package_pass == 1 and started == set(passed)
            and {name for name in passed if '/' not in name} == set(TESTS), 'required actual Go tests incomplete')
    return {name: {'pass': True, 'elapsed_seconds': passed[name]} for name in TESTS}


def normalize_shape(shape):
    shape = deepcopy(shape)
    for table in shape['Tables'].values():
        table['Constraints'].sort()
        for trigger in table['Triggers'].values():
            # Historical manifests omit these typed zero fields. Go's
            # schemaTrigger decoder supplies nil/empty values; the catalog
            # query writes explicit null/empty keys. Retain every nonempty
            # value and its type so selected-column/predicate drift refuses.
            trigger.setdefault('Columns', None)
            trigger.setdefault('When', '')
            target = trigger['Function']
            if target.startswith('public.') and target[len('public.'):] in shape['Functions']:
                trigger['Function'] = target[len('public.'):]
    return shape


def validate_catalog(value, position, expected):
    require(set(value) == {'position', 'shape', 'versions', 'migrations', 'context', 'phase', 'postgres_container_id'}
            and value['position'] == position and value['phase'] in CATALOG_PHASES
            and CATALOG_PHASES[value['phase']] == position
            and re.fullmatch(r'[0-9a-f]{64}', value['postgres_container_id']),
            'actual schema position differs')
    require(type(value['position']) is int and canonical(normalize_shape(value['shape'])) == canonical(normalize_shape(expected)),
            'actual PostgreSQL catalog differs from pinned manifest')
    require(value['versions'] == sorted(x[0] for x in MIGRATIONS[:position]), 'actual version markers differ')
    require(canonical(value['migrations']) == canonical([{'position': i + 1, 'version': v, 'checksum': checksum}
                                  for i, (v, _, checksum) in enumerate(MIGRATIONS[:position])]),
            'actual migration positions/checksums differ')


def validate_transition(report, directory, base=None):
    require(report.get('mode') == MODE and type(report.get('schema_version')) is int and report['schema_version'] == 2,
            'explicit transition receipt mode required')
    value = report.get('transition', {})
    require(value.get('candidate') == {'commit': CANDIDATE, 'tree': CANDIDATE_TREE,
                                      'application_files': 171, 'application_sha256': APPLICATION_SHA256}
            and value.get('preparation') == PREPARATION and value.get('policy_sha256') == POLICY_SHA256
            and value.get('from_position') == 4 and value.get('to_position') == 5
            and value.get('startup_timeout_seconds') == 30
            and value.get('receipt_counts') == {'groups': 10, 'checks': 53}, 'pinned transition identities/counts differ')
    require(all(type(value[k]) is int for k in ('from_position', 'to_position', 'startup_timeout_seconds'))
            and all(type(v) is int for v in value['receipt_counts'].values())
            and type(value['candidate']['application_files']) is int
            and all(type(v) is str for v in report['provider'].values()), 'transition identity scalar types differ')
    application = report['groups']['identity']['checks']['approved_application_transition'].get('evidence')
    require(application == {'old_commit': OLD_COMMIT, 'old_tree': OLD_TREE, 'old_reference': OLD_REFERENCE,
                            'old_application_sha256': OLD_APPLICATION_SHA256, 'old_files': 164,
                            'candidate_commit': CANDIDATE, 'candidate_tree': CANDIDATE_TREE,
                            'candidate_application_sha256': APPLICATION_SHA256, 'candidate_files': 171,
                            'schema_changing_upgrade': True}
            and report.get('runtime_application_sources_unchanged') is False, 'approved application transition differs')
    require(type(application['old_files']) is int and type(application['candidate_files']) is int
            and application['schema_changing_upgrade'] is True, 'application transition scalar types differ')
    freeze = strict_json(artifact(directory, value.get('context', {})))
    require(freeze.get('provider') == report['provider'] and freeze.get('checkout') == report['checkout']
            and freeze.get('candidate') == value['candidate'] and freeze.get('mode') == MODE
            and freeze.get('old_commit') == OLD_COMMIT and freeze.get('old_tree') == OLD_TREE
            and freeze.get('frozen_before_image_builds_and_database_media_changes') is True,
            'raw transition provider/source context differs')
    context = {'provider': report['provider'], 'checkout': report['checkout'], 'candidate': value['candidate'],
               'context_sha256': value['context']['sha256']}
    expected_drivers = {'scripts/self-hosting-acceptance.py', 'scripts/source-retention-transition.py',
                        '.github/workflows/self-hosting.yml'}
    require(set(freeze.get('drivers', {})) == expected_drivers
            and all(HASH.fullmatch(item.get('sha256', '')) and type(item.get('bytes')) is int and item['bytes'] > 0
                    for item in freeze['drivers'].values()), 'driver byte freeze missing')
    repo = Path(__file__).resolve().parents[1]
    for name, identity in freeze['drivers'].items():
        current = repo / name
        require(current.is_file() and not current.is_symlink() and current.stat().st_size == identity['bytes']
                and digest(current.read_bytes()) == identity['sha256'], 'executing verifier/driver bytes differ from frozen run')
    source = strict_json(artifact(directory, value.get('current_source', {})))
    require(source['commit'] == report['expected_checkout'] and source['tree'] == report['checkout']['tree']
            and digest(canonical({k: v for k, v in source['files'].items()
                                  if k.startswith(('cmd/', 'internal/', 'pkg/')) or k in ('go.mod', 'go.sum')})) == APPLICATION_SHA256
            and all(source['files'][path]['sha256'] == item['sha256'] and source['files'][path]['bytes'] == item['bytes']
                    for path, item in freeze['drivers'].items()), 'driver/runtime SOURCE bytes differ')
    require(digest(canonical(source)) == report['images']['current']['source_sha256'], 'actual current SOURCE hash differs')
    manifests = value.get('manifests', {})
    require(set(manifests) == {'004', '005'}, 'both pinned schema manifests required')
    expected = {}
    for position in (4, 5):
        raw = artifact(directory, manifests[f'{position:03d}'])
        require(digest(raw) == SHAPES[position], 'pinned schema manifest bytes differ')
        expected[position] = strict_json(raw)
    catalogs = value.get('catalogs', {})
    require(set(catalogs) == set(CATALOG_PHASES), 'actual ordered catalog observations missing')
    for name, position in CATALOG_PHASES.items():
        actual = strict_json(artifact(directory, catalogs[name]))
        require(actual.get('context') == context and actual.get('phase') == name, 'actual catalog provider/phase differs')
        validate_catalog(actual, position, expected[position])
    for before, after in (('fault004_before', 'fault004_after'), ('guard005_before', 'guard005_after')):
        records = [strict_json(artifact(directory, catalogs[name])) for name in (before, after)]
        require({k: v for k, v in records[0].items() if k != 'phase'} ==
                {k: v for k, v in records[1].items() if k != 'phase'},
                'cancellation/A1 refusal mutated actual catalog/history/container')
    initial = report['groups']['transition_schema']['checks']['actual005_catalog_history_backfill'].get('evidence', {})
    boundary = report['groups']['transition_schema']['checks']['old004_catalog_roles'].get('evidence', {})
    roles = strict_json(artifact(directory, boundary.get('roles', {})))
    require(roles.get('context') == context and roles.get('owners') == ['cutmy_migrate']
            and roles.get('postgres_container_id') == strict_json(artifact(directory, catalogs['old004']))['postgres_container_id']
            and type(roles.get('runtime_memberships')) is int and roles['runtime_memberships'] == 0
            and roles.get('roles') == [
                {'name': 'cutmy_admin', 'superuser': True, 'createrole': True, 'createdb': True, 'bypassrls': True},
                {'name': 'cutmy_migrate', 'superuser': False, 'createrole': False, 'createdb': False, 'bypassrls': False},
                {'name': 'cutmy_runtime', 'superuser': False, 'createrole': False, 'createdb': False, 'bypassrls': False}]
            and roles.get('refused_runtime_operations') == ['managed_table_ddl', 'migration_history_dml', 'role_ddl'],
            'actual old004 reduced-role ownership/refusal observations missing')
    require(all(type(role[k]) is bool for role in roles['roles']
                for k in ('superuser', 'createrole', 'createdb', 'bypassrls')),
            'old004 role privilege scalar types differ')
    observer = strict_json(artifact(directory, value.get('migration_observer', {})))
    require(observer.get('context') == context and observer.get('role') == 'cutmy_migrate'
            and observer.get('image_id') == report['images']['current']['image_id']
            and observer.get('source_commit') == report['expected_checkout'] and observer.get('command') == ['server']
            and observer.get('ports_published') is False and observer.get('schema_observation_before_maintenance') is True
            and observer.get('single_writer_stopped_and_removed') is True
            and re.fullmatch(r'[0-9a-f]{64}', observer.get('container_id', '')),
            'owned schema-owner observation before maintenance missing')
    constraints = observer.get('constraints', {})
    require(constraints.get('id') == observer['container_id'] and constraints.get('image_id') == observer['image_id']
            and constraints.get('role') == 'migrate' and constraints.get('user') == '1000:1000'
            and constraints.get('readonly') is True and constraints.get('capabilities_dropped') is True
            and constraints.get('no_new_privileges') is True and constraints.get('secrets_separated') is True
            and constraints.get('memory') == 512 << 20 and constraints.get('nano_cpus') == 10**9
            and constraints.get('pids_limit') == 128 and constraints.get('restart') == 'no'
            and constraints.get('mount_destinations') == ['/data'], 'schema-owner observer profile constraints differ')
    pins = strict_json(artifact(directory, initial.get('pins', {})))
    require(pins.get('context') == context and isinstance(pins.get('sources'), list) and pins['sources']
            and isinstance(pins.get('historical_preview_source'), str), 'actual initial backfill raw context missing')
    for source in pins['sources']:
        require(set(source) == {'id', 'jobs', 'previews', 'valid', 'revision', 'actual_jobs', 'actual_previews'}
                and source['valid'] is True and all(type(source[k]) is int and source[k] >= 0
                    for k in ('jobs', 'previews', 'revision', 'actual_jobs', 'actual_previews')) and source['revision'] == 0
                and source['jobs'] == source['actual_jobs'] and source['previews'] == source['actual_previews'],
                'actual initial all-job/exact-preview backfill differs')
    historical = [p for p in pins['sources'] if p['id'] == pins['historical_preview_source']]
    require(len(historical) == 1 and historical[0]['jobs'] == 0 and historical[0]['previews'] == 1,
            'actual historical expired foreign preview pin missing')
    raw_go = value.get('go', {})
    require(raw_go.get('provider') == report['provider'] and raw_go.get('checkout') == report['checkout']
            and raw_go.get('candidate') == value['candidate'] and raw_go.get('exit') == 0
            and raw_go.get('role') == 'cutmy_admin' and raw_go.get('postgres_version_major') == 17
            and raw_go.get('isolated_schemas') is True and raw_go.get('context') == context
            and raw_go.get('postgres_container_id') == strict_json(artifact(directory, catalogs['current005']))['postgres_container_id'],
            'actual Go/PG provider/role identity differs')
    require(type(raw_go['exit']) is int and type(raw_go['postgres_version_major']) is int,
            'Go exit/server scalar types differ')
    observed = validate_go_events(artifact(directory, raw_go.get('raw', {})))
    require(raw_go.get('tests') == observed, 'Go receipt differs from actual raw observations')
    for group in ('transition_schema', 'transition_retention'):
        for name, item in report['groups'][group]['checks'].items():
            if name in GO_ORACLES:
                proof = item.get('evidence', {})
                require(proof.get('tests') == list(GO_ORACLES[name]) and proof.get('raw') == raw_go['raw']
                        and all(observed[t]['pass'] is True for t in proof['tests']), 'named transition oracle missing/borrowed')
    observations = strict_json(artifact(directory, value.get('observations', {})))
    require(observations.get('provider') == report['provider'] and observations.get('checkout') == report['checkout'],
            'actual transition observation context differs')
    require(observations.get('context') == context, 'transition observation frozen driver context differs')
    runtime = strict_json(artifact(directory, value.get('runtime_dml', {})))
    require(runtime == {'context': context, 'role': 'cutmy_runtime', 'function_execute': False, 'security_definer': False,
                        'membership_and_idempotency_verified': True, 'rollback_originals_unchanged': True},
            'actual reduced runtime invoker DML proof missing')
    require(runtime['function_execute'] is False and runtime['security_definer'] is False
            and runtime['membership_and_idempotency_verified'] is True and runtime['rollback_originals_unchanged'] is True,
            'runtime observation booleans differ')
    ttl = strict_json(artifact(directory, value.get('ttl', {})))
    require(ttl.get('context') == context and ttl.get('role') == 'cutmy_runtime' and ttl.get('fixture_false_zero_sources') == 200
            and ttl.get('first_candidate_rows') == 200 and ttl.get('first_quarantined') == 200
            and ttl.get('second_candidate_rows') == 1 and ttl.get('healthy_tail_deleted') is True
            and ttl.get('pinned_sources_alive') == 200 and ttl.get('rollback_originals_unchanged') is True
            and ttl.get('candidate_cte_materialized') is True and ttl.get('performance_claimed') is False
            and isinstance(ttl.get('explain'), list) and len(ttl['explain']) == 1,
            'actual bounded TTL/authority/quarantine observations missing')
    require(all(type(ttl[k]) is int for k in ('fixture_false_zero_sources', 'first_candidate_rows', 'first_quarantined',
                                             'second_candidate_rows', 'pinned_sources_alive')), 'TTL count types differ')
    def candidate_rows(node):
        result = [node['Actual Rows']] if node.get('Subplan Name') == 'CTE source_retention_candidates' else []
        for child in node.get('Plans', []):
            result.extend(candidate_rows(child))
        return result
    plan_rows = candidate_rows(ttl['explain'][0]['Plan'])
    require(len(plan_rows) == 1 and type(plan_rows[0]) is int and plan_rows == [200], 'actual TTL candidate plan bound missing')
    bound = report['groups']['transition_retention']['checks']['bounded_plan_and_audit_limits'].get('evidence', {})
    require(bound.get('public_plan') == value['ttl'], 'bounded public plan oracle missing/borrowed')
    for name in ('old_writers_stopped', 'original_values_jobs_ledger_preserved', 'initial_pins_exact',
                 'fault_original_values_unchanged', 'a1_original_values_unchanged', 'rollback_fresh_resources',
                 'rollback_old_backup_unchanged', 'rollback_media_counters_ownership_jobs',
                 'restore005_derived_and_audit_unchanged', 'restore005_real_export_decode'):
        require(observations.get(name) is True, 'mandatory actual transition observation missing: ' + name)
    require(type(observations.get('migration_seconds')) in (int, float) and 0 <= observations['migration_seconds'] <= 30
            and type(observations.get('fault_seconds')) in (int, float) and 0 <= observations['fault_seconds'] <= 30
            and observations.get('fault_statement_timeout_ms') == 500
            and observations.get('a1_exit') == 1 and observations.get('a1_error_code') == 'schema_incompatible'
            and observations.get('reverse_method') == 'fresh_restore_immutable004_backup', 'migration/refusal/reversal oracle invalid')
    require(type(observations['fault_statement_timeout_ms']) is int and type(observations['a1_exit']) is int,
            'migration/refusal scalar types differ')
    require(report['cleanup'].get('private_state_backup_config_keys_removed') is True
            and report['cleanup'].get('private_workspace_retained') is False, 'transition private workspace cleanup incomplete')


def acceptance_class(base):
    """Keep the default driver flow unchanged; specialize only explicit mode."""
    class TransitionAcceptance(base.Acceptance):
        def __init__(self, args):
            require(getattr(args, 'mode', None) == MODE, 'explicit transition mode required')
            super().__init__(args)
            self.required_groups = groups(base.GROUPS)
            self.report.update(mode=MODE, schema_version=2, runtime_application_sources_unchanged=False)
            self.report['scope']['schema_changing_upgrade'] = True
            self.report['transition'] = {
                'candidate': {'commit': CANDIDATE, 'tree': CANDIDATE_TREE, 'application_files': 171,
                              'application_sha256': APPLICATION_SHA256},
                'preparation': PREPARATION, 'policy_sha256': POLICY_SHA256,
                'from_position': 4, 'to_position': 5, 'startup_timeout_seconds': 30,
                'receipt_counts': {'groups': 10, 'checks': 53}, 'catalogs': {}, 'manifests': {},
            }
            self.observations = {'provider': self.report['provider']}

        def raw(self, name, data):
            require(re.fullmatch(r'[a-z0-9_-]+\.(?:json|jsonl|txt)', name) and 0 < len(data) <= 32 * 1024 * 1024,
                    'new bounded raw artifact required')
            path = self.output / name
            require(not path.exists(), 'frozen evidence cannot be overwritten')
            path.write_bytes(data)
            return {'path': name, 'bytes': len(data), 'sha256': digest(data)}

        def raw_context(self):
            value = self.report['transition']
            return {'provider': self.report['provider'], 'checkout': self.report['checkout'],
                    'candidate': value['candidate'], 'context_sha256': value['context']['sha256']}

        def initialize_host(self):
            # Refuse unknown runtime source before base starts any host mutation.
            require(self.git(base.REPO, 'rev-parse', CANDIDATE + '^{tree}') == CANDIDATE_TREE, 'reviewed candidate tree differs')
            self.run(['git', '-c', 'safe.directory=' + str(base.REPO), '-C', base.REPO,
                      'merge-base', '--is-ancestor', CANDIDATE, self.args.revision])
            require(self.git(base.REPO, 'status', '--porcelain', '--untracked-files=all') == '', 'transition checkout is dirty')
            inventory = {}
            for entry in self.run(['git', '-C', base.REPO, 'ls-tree', '-rz', '--full-tree', self.args.revision]).split(b'\0'):
                if not entry:
                    continue
                header, raw_path = entry.split(b'\t', 1)
                mode, kind, blob = header.decode().split(); path = raw_path.decode()
                if path.startswith(('cmd/', 'internal/', 'pkg/')) or path in ('go.mod', 'go.sum'):
                    require(kind == 'blob' and mode in ('100644', '100755'), 'unsupported runtime source')
                    data = self.run(['git', '-C', base.REPO, 'cat-file', 'blob', blob])
                    inventory[path] = {'mode': mode, 'git_blob': blob, 'bytes': len(data), 'sha256': digest(data)}
            require(len(inventory) == 171 and digest(canonical(inventory)) == APPLICATION_SHA256,
                    'runtime differs from reviewed immutable candidate')
            super().initialize_host()
            self.fault_old = self.make_project(self.old_repo, 'fault004', 46)
            self.fault_new = self.make_project(self.current_repo, 'fault005', 46, same=self.fault_old)
            self.guard = self.make_project(self.current_repo, 'guard005', 48)
            self.rollback = self.make_project(self.old_repo, 'rollback004', 50)
            self.tls_projects = (self.old, self.restored, self.rollback)
            self.observations['checkout'] = self.report['checkout']
            value = self.report['transition']
            value['context'] = self.raw('transition-context.json', canonical({
                'mode': MODE, 'provider': self.report['provider'], 'checkout': self.report['checkout'],
                'candidate': value['candidate'], 'old_commit': OLD_COMMIT, 'old_tree': OLD_TREE,
                # Read-only host/daemon probes and owned private source/project
                # setup already ran; image, database and media changes have not.
                'frozen_before_image_builds_and_database_media_changes': True,
                'drivers': {str(p.relative_to(base.REPO)): {'sha256': base.file_hash(p), 'bytes': p.stat().st_size}
                            for p in (base.REPO / 'scripts/self-hosting-acceptance.py',
                                      base.REPO / 'scripts/source-retention-transition.py',
                                      base.REPO / '.github/workflows/self-hosting.yml')},
            }))
            self.observations['context'] = self.raw_context()
            for position in (4, 5):
                data = (self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes()
                require(digest(data) == SHAPES[position], 'declared schema bytes differ')
                value['manifests'][f'{position:03d}'] = self.raw(f'schema-manifest-{position:03d}.json', data)
            require(base.file_hash(self.current_repo / 'deploy/egress-policy.py') == POLICY_SHA256, 'reviewed policy helper changed')

        def application_identity(self):
            app = lambda source: {k: v for k, v in source['files'].items()
                                  if k.startswith(('cmd/', 'internal/', 'pkg/')) or k in ('go.mod', 'go.sum')}
            require(len(app(self.old_source)) == 164 and digest(canonical(app(self.old_source))) == OLD_APPLICATION_SHA256
                    and len(app(self.current_source)) == 171 and digest(canonical(app(self.current_source))) == APPLICATION_SHA256,
                    'old/current immutable application inventory differs')
            self.report['transition']['current_source'] = self.raw('transition-current-source.json', canonical(self.current_source))
            self.check('approved_application_transition', {
                'old_commit': OLD_COMMIT, 'old_tree': OLD_TREE, 'old_reference': OLD_REFERENCE,
                'old_application_sha256': OLD_APPLICATION_SHA256, 'old_files': 164,
                'candidate_commit': CANDIDATE, 'candidate_tree': CANDIDATE_TREE,
                'candidate_application_sha256': APPLICATION_SHA256, 'candidate_files': 171,
                'schema_changing_upgrade': True})

        def capture_catalog(self, name, project, position):
            require(name in CATALOG_PHASES and CATALOG_PHASES[name] == position, 'unknown catalog phase')
            expected = strict_json((self.current_repo / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())
            latest = strict_json((self.current_repo / 'internal/app/migrations/005_schema.json').read_bytes())
            source = (self.current_repo / 'internal/app/migration_catalog.go').read_text()
            query = re.search(r'const schemaShapeSQL = `([^`]+)`', source)[1]
            array = lambda values: 'ARRAY[' + ','.join("'" + x + "'" for x in sorted(values)) + ']::text[]'
            query = query.replace('$1::text[]', array(latest['Tables'])).replace('$2::text[]', array(latest['Functions']))
            shape = strict_json(self.sql(project, query))
            history = strict_json(self.sql(project, "SELECT jsonb_build_object('versions',"
                "(SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions),"
                "'migrations',(SELECT jsonb_agg(jsonb_build_object('position',position,'version',version,'checksum',checksum)"
                " ORDER BY position) FROM app_schema_migrations))"))
            value = {'position': position, 'shape': shape, **history,
                     'phase': name, 'context': self.raw_context(), 'postgres_container_id': self.container(project, 'postgres')['Id']}
            # Retain the captured bytes even when strict comparison refuses.
            # Diagnostic observations never substitute for validated catalogs.
            receipt = self.raw('catalog-' + name + '.json', canonical(value))
            self.report['transition'].setdefault('catalog_observations', {})[name] = receipt
            validate_catalog(value, position, expected)
            self.report['transition']['catalogs'][name] = receipt
            return value

        def original_snapshot(self, project):
            value = self.snapshot(project)
            # 005 may rewrite source xmin to seed derived counts. Original
            # values, jobs/artifacts/physical ledger and byte counters stay exact.
            for row in value['sources']:
                for key in tuple(row):
                    if key in ('retention_job_pins', 'retention_preview_pins', 'retention_pins_valid', 'retention_pin_revision'):
                        row.pop(key)
            return value

        def stopped(self, project):
            self.wrapper(project, 'stop')
            require(all(self.container(project, role)['State']['Running'] is False
                        and self.container(project, role)['State']['Pid'] == 0 for role in ('api', 'worker')),
                    'transition writers not fully stopped')
            self.policy(project, 'verify')

        def extra_fresh_restore(self, project, source, backup):
            require(self.project_resources(project) == [], 'transition restore requires fresh resources')
            self.wrapper(project, 'adopt-image', '--from-state', source['state'])
            self.wrapper(project, 'restore', '--directory', backup)
            self.policy(project, 'apply')

        def role_boundary(self):
            value = strict_json(self.sql(self.old, "SELECT jsonb_build_object('roles',"
                "(SELECT jsonb_agg(jsonb_build_object('name',rolname,'superuser',rolsuper,'createrole',rolcreaterole,"
                "'createdb',rolcreatedb,'bypassrls',rolbypassrls) ORDER BY rolname) FROM pg_roles"
                " WHERE rolname IN ('cutmy_admin','cutmy_migrate','cutmy_runtime')),"
                "'owners',(SELECT jsonb_agg(DISTINCT tableowner) FROM pg_tables WHERE schemaname='public'),"
                "'runtime_memberships',(SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.member WHERE r.rolname='cutmy_runtime'))"))
            require(value['owners'] == ['cutmy_migrate'] and value['runtime_memberships'] == 0
                    and [r['name'] for r in value['roles']] == ['cutmy_admin', 'cutmy_migrate', 'cutmy_runtime']
                    and value['roles'][0]['superuser'] is True
                    and all(not r[k] for r in value['roles'][1:] for k in ('superuser', 'createrole', 'createdb', 'bypassrls')),
                    'old004 administrator/migration/runtime separation differs')
            # Exercise reduced old grants before taking the pre-upgrade backup.
            for query in ('CREATE TABLE public.acceptance_old_forbidden(i integer)',
                          "UPDATE app_schema_migrations SET checksum=checksum", 'CREATE ROLE acceptance_old_forbidden'):
                code, stdout, stderr = self.sql(self.old, query, runtime=True, expected=None)
                require(code == 3 and re.search(rb'\b42501\b', stderr) and not stdout.strip(), 'old004 runtime grant refusal missing')
            value.update(context=self.raw_context(), postgres_container_id=self.container(self.old, 'postgres')['Id'],
                         refused_runtime_operations=['managed_table_ddl', 'migration_history_dml', 'role_ddl'])
            return self.raw('old004-role-boundary.json', canonical(value))

        def migration_fault(self):
            self.extra_fresh_restore(self.fault_old, self.old, self.old_backup)
            before = self.original_snapshot(self.fault_old)
            self.capture_catalog('fault004_before', self.fault_old, 4)
            self.wrapper(self.fault_new, 'adopt-image', '--from-state', self.current['state'])
            # Only this separate fault copy has an earlier statement deadline.
            # The accepted migration retains the binary's 30-second context.
            lock_name = 'cutmy-membership-' + self.token
            script = 'export PGPASSWORD="$POSTGRES_PASSWORD"; export PGAPPNAME=' + lock_name + '; exec psql -X -qAt -v ON_ERROR_STOP=1 -h 127.0.0.1 -U cutmy_admin -d cutmy -c '
            query = 'BEGIN; LOCK TABLE public.storage_reservations IN ROW EXCLUSIVE MODE; SELECT pg_sleep(45); ROLLBACK;'
            lock = self.start_process('transition-membership-lock', self.compose(self.fault_old) + ['exec', '-T', 'postgres', 'sh', '-c', script + base.shlex.quote(query)])
            override = self.work / 'fault-earlier-deadline.json'
            url = ('postgres://cutmy_migrate:' + self.fault_new['config']['CUTMY_MIGRATION_PASSWORD'] + '@postgres:5432/cutmy?sslmode=disable&pool_max_conns=4&statement_timeout=500')
            override.write_bytes(canonical({'services': {'migrate': {'environment': {'DATABASE_URL': url}}}})); override.chmod(0o600)
            try:
                self.wait(lambda: int(self.sql(self.fault_old, "SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a ON a.pid=l.pid WHERE a.application_name='"
                    + lock_name + "' AND l.relation='public.storage_reservations'::regclass AND l.mode='RowExclusiveLock' AND l.granted")) == 1, 5)
                start = time.monotonic()
                code, stdout, stderr = self.run(self.compose(self.fault_new) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate'], expected=None, timeout=30, terminate_grace=5)
                elapsed = time.monotonic() - start
                require(code == 1 and elapsed <= 30 and b'could not initialize the database' in stderr, 'membership-lock migration did not promptly refuse')
            finally:
                # Terminate only the captured backend; do not kill other DB
                # sessions or leave the direct lock until cleanup.
                self.sql(self.fault_old, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='" + lock_name + "'")
                require(lock.wait(timeout=5) in (0, 2, 3), 'owned membership-lock process failed to stop as expected')
                lock.acceptance_stopped = True
            self.capture_catalog('fault004_after', self.fault_old, 4)
            require(self.original_snapshot(self.fault_old) == before, 'cancelled migration changed original004 data')
            self.wrapper(self.fault_old, 'initialize')
            self.observations.update(fault_seconds=elapsed, fault_statement_timeout_ms=500, fault_original_values_unchanged=True)

        def observe_upgrade_before_maintenance(self):
            # The normal wrapper initializes with `maintenance`, which may
            # legitimately delete an expired preview immediately after startup.
            # Observe committed migration before that separate cleanup phase.
            # Only the command differs on this owned public migrate profile:
            # server performs the same guarded OpenStore, publishes no ports,
            # receives no requests, and is stopped before normal initialize.
            project = self.current
            self.policy(project, 'verify')
            self.run(self.compose(project) + ['up', '-d', '--wait', 'postgres'], timeout=150)
            self.pg_tcp_ready(project)
            name = project['config']['CUTMY_PROJECT'] + '_schema_observer'
            require(not self.run(['docker', 'ps', '-aq', '--no-trunc', '--filter', 'name=^/' + name + '$']).strip(),
                    'schema observer name already exists')
            start = time.monotonic()
            identifier = self.run(self.compose(project) + ['run', '--detach', '--no-deps', '--name', name, 'migrate', 'server'], timeout=15).decode().strip()
            require(re.fullmatch(r'[0-9a-f]{64}', identifier), 'schema observer container identity missing')
            try:
                def migrated():
                    actual = self.json(['docker', 'inspect', identifier])[0]
                    base.owned_resource(actual, project=project['config']['CUTMY_PROJECT'])
                    require(actual['State']['Running'] and actual['Image'] == self.report['images']['current']['image_id'],
                            'schema-owner observer exited or changed image')
                    return self.sql(project, 'SELECT count(*) FROM app_schema_migrations WHERE position=5', timeout=5).strip() == b'1'
                self.wait(migrated, max(0, 30 - (time.monotonic() - start)))
                elapsed = time.monotonic() - start
                require(elapsed <= 30, '005 migration/backfill exceeded existing30-second cap')
                actual = self.json(['docker', 'inspect', identifier])[0]
                constraints = self.constraint(actual, 'migrate', project)
                require(actual['Config']['Cmd'] == ['server'] and actual['Config']['Entrypoint'] == ['/usr/local/bin/cutmy']
                        and not actual['HostConfig'].get('PortBindings')
                        and set(actual['NetworkSettings']['Networks']) == {project['config']['CUTMY_PROJECT'] + '_database'},
                        'schema-owner observer changed command/network/port restrictions')
                self.capture_catalog('current005', project, 5)
                require(self.original_snapshot(project) == self.original004, '005 changed original values/jobs/ledger/accounting')
                pins = strict_json(self.sql(project, "SELECT COALESCE(jsonb_agg(jsonb_build_object('id',s.id,'jobs',retention_job_pins,"
                    "'previews',retention_preview_pins,'valid',retention_pins_valid,'revision',retention_pin_revision,'actual_jobs',(SELECT count(*) FROM jobs WHERE source_id=s.id),"
                    "'actual_previews',(SELECT count(*) FROM storage_reservations WHERE kind='preview' AND job_id=s.id)) ORDER BY s.id),'[]') FROM sources s"))
                require(pins and all(p['valid'] is True and p['revision'] == 0 and p['jobs'] == p['actual_jobs'] and p['previews'] == p['actual_previews'] for p in pins),
                        '005 initial all-job/exact-preview backfill differs')
                historical = [p for p in pins if p['id'] == self.historical_preview_source]
                require(len(historical) == 1 and historical[0]['jobs'] == 0 and historical[0]['previews'] == 1,
                        '005 historical expired foreign preview did not pin metadata source')
            finally:
                actual = self.json(['docker', 'inspect', identifier])[0]
                base.owned_resource(actual, project=project['config']['CUTMY_PROJECT'])
                require(actual['Id'] == identifier and actual['Image'] == self.report['images']['current']['image_id'],
                        'schema observer identity changed before stop')
                self.run(['docker', 'stop', '--time', '10', identifier], timeout=15)
                require(self.json(['docker', 'inspect', identifier])[0]['State']['Running'] is False, 'schema observer remained a writer')
                self.run(['docker', 'rm', identifier], timeout=10)
                require(not self.run(['docker', 'ps', '-aq', '--no-trunc', '--filter', 'name=^/' + name + '$']).strip(), 'schema observer removal unconfirmed')
            self.report['transition']['migration_observer'] = self.raw('migration-owner-observer.json', canonical({
                'context': self.raw_context(), 'role': 'cutmy_migrate', 'image_id': self.report['images']['current']['image_id'],
                'source_commit': self.args.revision, 'container_id': identifier, 'command': ['server'],
                'ports_published': False, 'schema_observation_before_maintenance': True,
                'single_writer_stopped_and_removed': True, 'constraints': constraints}))
            self.observations.update(migration_seconds=elapsed, original_values_jobs_ledger_preserved=True, initial_pins_exact=True)
            return elapsed, pins

        def go_postgres_oracles(self):
            # This one actual execution supplies named oracles, not a rerun of
            # each assertion in the public wrapper. The source and provider
            # identity are the same as the accepted image, pinned to root670.
            project = self.current
            major = int(self.sql(project, "SELECT current_setting('server_version_num')::integer/10000"))
            require(major == 17 and self.sql(project, 'SELECT current_user').strip() == b'cutmy_admin',
                    'actual Go database role/server differs')
            address = project['config']['CUTMY_DATABASE_PREFIX'] + '.2'
            raw_url = 'postgres://cutmy_admin:' + quote(project['config']['CUTMY_ADMIN_PASSWORD'], safe='') + '@' + address + ':5432/cutmy?sslmode=disable&pool_max_conns=2'
            require(self.run(['go', 'version']).decode().strip().startswith('go version go1.27.2 '), 'exact Go toolchain required')
            command = ['go', '-C', self.current_repo, 'test', '-json', '-race', '-count=1', '-timeout=4m', './internal/app',
                       '-run', '^(' + '|'.join(TESTS) + ')$']
            code, stdout, stderr = self.run(command, env={'TEST_DATABASE_URL': raw_url, 'GOTOOLCHAIN': 'local'}, timeout=300, expected=None)
            raw = self.raw('transition-go-postgres.jsonl', base.redact(stdout, self.secrets).encode())
            self.raw('transition-go-postgres-stderr.txt', (base.redact(stderr, self.secrets) or 'empty\n').encode())
            self.report['transition']['go'] = {'provider': self.report['provider'], 'checkout': self.report['checkout'],
                'candidate': self.report['transition']['candidate'], 'role': 'cutmy_admin', 'postgres_version_major': 17,
                'isolated_schemas': True, 'exit': code, 'raw': raw, 'tests': {}, 'context': self.raw_context(),
                'postgres_container_id': self.container(project, 'postgres')['Id']}
            require(code == 0, 'actual source-retention Go/PG tests failed')
            self.report['transition']['go']['tests'] = validate_go_events(stdout)

        def public_runtime_oracles(self):
            project = self.current
            before = self.snapshot(project)
            permission = strict_json(self.sql(project, "SELECT jsonb_build_object('role',current_user,"
                "'function_execute',bool_or(has_function_privilege(current_user,p.oid,'EXECUTE')),"
                "'security_definer',bool_or(p.prosecdef)) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public'", runtime=True))
            require(permission == {'role': 'cutmy_runtime', 'function_execute': False, 'security_definer': False},
                    'public source pin functions expanded runtime privileges')
            prefix = 'acceptance_' + self.token
            a, b = prefix + '_a', prefix + '_b'
            statement = f"""BEGIN;
INSERT INTO sources(id,owner,title,duration_ms,kind) VALUES('{a}','{prefix}','runtime fixture',1,'platform'),('{b}','{prefix}','runtime fixture',1,'platform');
INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('{prefix}_job','{prefix}','{a}','{{}}','[]','{prefix}');
INSERT INTO jobs(id,owner,source_id,request,items,idempotency_key) VALUES('{prefix}_duplicate','{prefix}','{a}','{{}}','[]','{prefix}') ON CONFLICT(owner,idempotency_key) DO NOTHING;
INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('{prefix}_preview','foreign','preview',0,now()-interval '1 day','{a}');
DO $$ BEGIN IF (SELECT retention_job_pins<>1 OR retention_preview_pins<>1 FROM sources WHERE id='{a}') THEN RAISE EXCEPTION 'initial membership mismatch'; END IF; END $$;
UPDATE jobs SET status='running',lease_until=now(),updated_at=now() WHERE id='{prefix}_job';
UPDATE jobs SET status='waiting_storage' WHERE id='{prefix}_job';
UPDATE jobs SET status='cancelled' WHERE id='{prefix}_job';
UPDATE jobs SET id='{prefix}_renamed',source_id='{b}' WHERE id='{prefix}_job';
UPDATE storage_reservations SET id='{prefix}_moved',job_id='{b}' WHERE id='{prefix}_preview';
DO $$ BEGIN IF (SELECT retention_job_pins<>0 OR retention_preview_pins<>0 FROM sources WHERE id='{a}') OR (SELECT retention_job_pins<>1 OR retention_preview_pins<>1 FROM sources WHERE id='{b}') THEN RAISE EXCEPTION 'moved membership mismatch'; END IF; END $$;
UPDATE storage_reservations SET kind='preview_cache' WHERE id='{prefix}_moved';
DELETE FROM jobs WHERE id='{prefix}_renamed';
DO $$ BEGIN IF (SELECT retention_job_pins<>0 OR retention_preview_pins<>0 FROM sources WHERE id='{b}') THEN RAISE EXCEPTION 'removed membership mismatch'; END IF; END $$;
SELECT true; ROLLBACK;"""
            require(self.sql(project, statement, runtime=True).strip() == b't', 'public runtime trigger mutation did not finish')
            require(self.snapshot(project) == before, 'public runtime trigger rollback changed durable data/accounting')
            self.report['transition']['runtime_dml'] = self.raw('public005-runtime-pins.json', canonical({**permission,
                'context': self.raw_context(), 'membership_and_idempotency_verified': True, 'rollback_originals_unchanged': True}))
            source = (self.current_repo / 'internal/app/source_retention.go').read_text()
            sql = re.search(r'const sourceRetentionSQL = `([^`]+)`', source)[1].replace('$1', '3600').replace('$2', '200')
            require('source_retention_candidates AS MATERIALIZED' in sql and sql.index('LIMIT 200') < sql.index('EXISTS('),
                    'candidate limit no longer precedes authority')
            # One actual small plan is an acceptance bound, not the deferred
            # 10k/100k latency/cost campaign or a healthy-TTL performance claim.
            fixture = f"""BEGIN;
INSERT INTO sources(id,owner,title,duration_ms,kind,created_at) SELECT '{prefix}-pin-'||lpad(i::text,4,'0'),'{prefix}','false zero fixture',1,'platform',now()-interval '3 hours' FROM generate_series(1,200) i;
INSERT INTO jobs(id,owner,source_id,request,items) SELECT '{prefix}-job-'||id,'{prefix}',id,'{{}}','[]' FROM sources WHERE id LIKE '{prefix}-pin-%';
UPDATE sources SET retention_job_pins=0 WHERE id LIKE '{prefix}-pin-%';
INSERT INTO sources(id,owner,title,duration_ms,kind,created_at) VALUES('{prefix}-tail','{prefix}','healthy tail fixture',1,'platform',now()-interval '2 hours');
EXPLAIN (ANALYZE, FORMAT JSON) {sql};
SELECT jsonb_build_object('first_quarantined',count(*) FILTER(WHERE NOT retention_pins_valid),'pinned_sources_alive',count(*)) FROM sources WHERE id LIKE '{prefix}-pin-%';
{sql};
SELECT NOT EXISTS(SELECT 1 FROM sources WHERE id='{prefix}-tail'); ROLLBACK;"""
            output = self.sql(project, fixture, runtime=True)
            # psql's JSON plan is multi-line; following scalar/tabular outputs
            # stay separate and are parsed without accepting substitute claims.
            text = output.decode(); explain, offset = json.JSONDecoder().raw_decode(text.lstrip())
            trailing = text.lstrip()[offset:].strip().splitlines()
            require(len(trailing) == 4, 'actual TTL observation output differs')
            counts = strict_json(trailing[0])
            rows = sorted(line.split('|') for line in trailing[1:-1])
            require(counts == {'first_quarantined': 200, 'pinned_sources_alive': 200}
                    and rows == [['deleted', '', '', prefix, '0', '0'], ['progress', '', '', '', '1', '0']]
                    and trailing[-1] == 't',
                    'false zeros hid healthy tail or deleted real pins')
            require(self.snapshot(project) == before, 'TTL rollback changed durable data/accounting')
            self.report['transition']['ttl'] = self.raw('public005-ttl-plan.json', canonical({
                'context': self.raw_context(), 'role': 'cutmy_runtime', 'fixture_false_zero_sources': 200, 'first_candidate_rows': 200,
                **counts, 'second_candidate_rows': 1, 'healthy_tail_deleted': True,
                'rollback_originals_unchanged': True, 'candidate_cte_materialized': True,
                'performance_claimed': False, 'explain': explain}))

        def go_check(self, name):
            proof = {'tests': list(GO_ORACLES[name]), 'raw': self.report['transition']['go']['raw']}
            if name == 'bounded_plan_and_audit_limits':
                proof['public_plan'] = self.report['transition']['ttl']
            self.check(name, proof)

        def recovery_before005_restore(self):
            self.extra_fresh_restore(self.guard, self.current, self.current_backup)
            self.capture_catalog('guard005_before', self.guard, 5)
            before = self.snapshot(self.guard)
            override = self.work / 'guard-unchanged-a1.json'
            override.write_bytes(canonical({'services': {'migrate': {'image': self.report['images']['old']['image_id']}}})); override.chmod(0o600)
            code, stdout, stderr = self.run(self.compose(self.guard) + ['-f', override, 'run', '--rm', '-T', '--no-deps', 'migrate', '--json-errors', 'maintenance'], expected=None, timeout=30)
            diagnostics = [strict_json(line) for line in stderr.splitlines() if line.startswith(b'{')]
            require(len(diagnostics) == 1, 'A1 JSON refusal diagnostic missing/ambiguous')
            diagnostic = diagnostics[0]
            require(code == 1 and not stdout.strip() and diagnostic['error']['code'] == 'schema_incompatible', 'unchanged A1 failed to refuse005')
            self.capture_catalog('guard005_after', self.guard, 5)
            require(self.snapshot(self.guard) == before, 'A1 guard refusal mutated005 application data')
            self.observations.update(a1_exit=code, a1_error_code=diagnostic['error']['code'], a1_original_values_unchanged=True)
            self.extra_fresh_restore(self.rollback, self.old, self.old_backup)
            self.capture_catalog('rollback004', self.rollback, 4)
            require(self.original_snapshot(self.rollback) == self.original004, 'fresh004 reversal changed original backup data')
            self.wrapper(self.rollback, 'initialize'); self.wrapper(self.rollback, 'up')
            ready = super().pg_tcp_ready(self.rollback, baseline=True)
            clients = [base.Client(self.rollback['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
            clients[0].cookie, clients[1].cookie = self.a.cookie, self.b.cookie
            results = self.verify_retained(self.rollback, *clients, prefix='rollback004')
            accounts, files = self.accounting(self.rollback)
            require(accounts == self.old_accounting and files == self.old_files
                    and self.backup_record(self.old_backup) == self.backup_a, 'fresh004 rollback changed media/accounting/backup')
            self.stopped(self.rollback)
            self.observations.update(rollback_fresh_resources=True, rollback_old_backup_unchanged=True,
                rollback_media_counters_ownership_jobs=True, reverse_method='fresh_restore_immutable004_backup')
            return {'a1': {'exit': code, 'error_code': diagnostic['error']['code'], 'image_id': self.report['images']['old']['image_id']},
                    'rollback': {'backup': self.backup_a, 'tcp': ready, 'retained': results, 'accounting': accounts}}

        def backup_and_upgrade(self):
            # Reuse the baseline media/idempotency/refusal/restore flow through
            # narrow phase hooks; its original 35-check default stays intact.
            self.old_accounting, self.old_files = self.accounting(self.old)
            self.group('transition_schema', lambda: self.check('old004_catalog_roles', {
                'catalog': self.capture_catalog('old004', self.old, 4), 'roles': self.role_boundary()}), complete=False)
            self.active_group = 'backup_restore'
            super().backup_and_upgrade()

        def wrapper(self, project, action, *extra, expected=0):
            if action == 'backup' and project is self.old:
                self.stopped(project)
                # Supplement the real API-published media/jobs with a recorded
                # reduced-role historical preview. It has no media/byte charge;
                # expiry and foreign ownership must still contribute one pin.
                self.historical_preview_source = 'acceptance_' + self.token + '_historical_preview'
                fixture = self.historical_preview_source
                self.sql(project, "BEGIN; INSERT INTO sources(id,owner,title,duration_ms,kind,created_at) VALUES('"
                    + fixture + "','acceptance_" + self.token + "','historical preview fixture',1,'platform',now()-interval '2 days'); "
                    "INSERT INTO storage_reservations(id,owner,kind,size_bytes,expires_at,job_id) VALUES('" + fixture
                    + "_reservation','foreign','preview',0,now()-interval '1 day','" + fixture + "'); COMMIT;", runtime=True)
                self.original004 = self.original_snapshot(project)
                self.observations['old_writers_stopped'] = all(not self.container(project, r)['State']['Running']
                                                              for r in ('api', 'worker'))
            if action == 'initialize' and project is self.current:
                self.backup_a = self.backup_record(self.old_backup)
                active = self.active_group
                self.group('transition_schema', lambda: self.check('stopped004_originals_backup', {
                    'original_values_sha256': digest(canonical(self.original004)), 'backup': self.backup_a}), complete=False)
                self.active_group = active
                self.migration_fault()
                self.migration_observation = self.observe_upgrade_before_maintenance()
            result = super().wrapper(project, action, *extra, expected=expected)
            if action == 'initialize' and project is self.current:
                elapsed, pins = self.migration_observation
                self.public_runtime_oracles()
                self.go_postgres_oracles()
                active = self.active_group
                def schema_checks():
                    self.check('actual005_catalog_history_backfill', {'catalog': self.report['transition']['catalogs']['current005'],
                        'pins': self.raw('initial005-pins.json', canonical({'context': self.raw_context(),
                            'historical_preview_source': self.historical_preview_source, 'sources': pins})), 'elapsed_seconds': elapsed})
                    for name in EXTRA_GROUPS['transition_schema'][3:]:
                        self.go_check(name)
                self.group('transition_schema', schema_checks)
                self.group('transition_retention', lambda: [self.go_check(name) for name in EXTRA_GROUPS['transition_retention']])
                self.active_group = active
            if action == 'restore' and project is self.restored and expected == 0:
                active = self.active_group
                evidence = self.recovery_before005_restore()
                def reverse_checks():
                    self.check('a1_guard_refuses005_without_mutation', evidence['a1'])
                    self.check('fresh004_backup_reverse_only', evidence['rollback'])
                self.group('transition_recovery', reverse_checks, complete=False)
                self.active_group = active
                self.capture_catalog('restored005', project, 5)
                require(self.snapshot(project) == self.restore_snapshot, 'fresh005 derived source state differs')
                audit = lambda p: strict_json(self.sql(p, "SELECT jsonb_build_object('cursor',(SELECT jsonb_agg(to_jsonb(t) ORDER BY id) FROM source_retention_audit t),"
                    "'partial',(SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY source_id),'[]') FROM source_pin_audit_progress t))"))
                require(audit(project) == audit(self.current), 'fresh005 durable audit state differs')
                self.observations['restore005_derived_and_audit_unchanged'] = True
            return result

        def check(self, name, evidence=None):
            # The preserved generic flow makes exactly the same media and
            # accounting assertions, but this upgrade has changed app/schema.
            if name == 'actual_same_project_image_upgrade':
                evidence = {**evidence, 'application_code_unchanged': False, 'schema_transition': [4, 5]}
            super().check(name, evidence)
            if name == 'restored_bytes_counters_and_new_exports':
                self.observations['restore005_real_export_decode'] = True
                active = self.active_group
                def recovery_checks():
                    self.check('fresh005_catalog_audit_restore', {'catalog': self.report['transition']['catalogs']['restored005']})
                    self.check('fresh005_api_export_accounting', evidence)
                self.group('transition_recovery', recovery_checks)
                self.active_group = active

        def failure_snapshot(self):
            # Diagnostics cannot become a success oracle or obstruct cleanup.
            # Only bounded logs of inspected, owned service IDs are exported;
            # environments, private bind files and backup data are never read.
            records, seen = [], set()
            for project in reversed(self.projects):
                name = project['config']['CUTMY_PROJECT']
                if name in seen:
                    continue
                seen.add(name)
                for role in ('api', 'worker', 'postgres', 'migrate', 'maintenance'):
                    record = {'project': name, 'role': role, 'status': 'unavailable'}
                    try:
                        container = self.container(project, role)
                        code, stdout, stderr = self.run(['docker', 'logs', '--tail', '200', container['Id']],
                                                        expected=None, timeout=5)
                        record.update(status='retained', container_id=container['Id'], exit=code,
                                      logs=base.redact(stdout + stderr, self.secrets)[-65536:], redacted=True)
                    except Exception:
                        record['reason'] = 'owned_service_log_unavailable'
                    records.append(record)
            data = canonical({'context': self.report['transition'].get('context'), 'services': records,
                              'diagnostic_only': True, 'redacted': True})
            require(len(data) <= 4 * 1024 * 1024, 'transition failure snapshot bound exceeded')
            self.report['transition']['failure_snapshot'] = self.raw('transition-failure-snapshot.json', data)

        def close(self):
            # Freeze partial/failure observations too; base retains bounded,
            # redacted policy snapshots and performs independent cleanup.
            try:
                if self.report['errors']:
                    self.cleaning = True
                    self.failure_snapshot()
                self.report['transition']['observations'] = self.raw('transition-observations.json', canonical(self.observations))
            except Exception as error:
                self.report['errors'].append('transition observation snapshot: ' + base.redact(str(error), self.secrets))
            finally:
                super().close()

    return TransitionAcceptance
