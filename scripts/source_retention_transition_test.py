"""Pure source-only controls with synthetic receipts; no acceptance is executed.

Only local source bytes and temporary JSON/text files are read. These fixtures
are verifier controls, never hosted, PostgreSQL, media or performance evidence.
"""
from copy import deepcopy
import ast
import hashlib
import importlib.util
import io
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

import yaml


ROOT = Path(__file__).resolve().parent
REPO = ROOT.parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


base = module('transition_control_base', ROOT / 'self-hosting-acceptance.py')
transition = module('transition_control_helper', ROOT / 'source-retention-transition.py')
REFUSALS = (RuntimeError, OSError, ValueError, KeyError, TypeError)


def workflow_expression(expression, event, mode):
    """Evaluate only the workflow's string comparisons and short-circuit routing."""
    if expression.startswith('${{ ') and expression.endswith(' }}'):
        expression = expression[4:-3]
    expression = (expression.replace('github.event_name', 'event')
                  .replace('inputs.acceptance_mode', 'mode')
                  .replace('&&', ' and ').replace('||', ' or '))
    tree = ast.parse(expression.strip(), mode='eval')
    values = {'event': event, 'mode': mode}

    def read(node):
        if isinstance(node, ast.Name) and node.id in values:
            return values[node.id]
        if isinstance(node, ast.Constant) and node.value in (
                'workflow_dispatch', 'same-schema', transition.MODE):
            return node.value
        if isinstance(node, ast.Compare) and len(node.ops) == len(node.comparators) == 1:
            left, right = read(node.left), read(node.comparators[0])
            if isinstance(node.ops[0], ast.Eq):
                return left == right
            if isinstance(node.ops[0], ast.NotEq):
                return left != right
        if isinstance(node, ast.BoolOp) and isinstance(node.op, (ast.And, ast.Or)):
            result = read(node.values[0])
            for value in node.values[1:]:
                if (isinstance(node.op, ast.And) and not result
                        or isinstance(node.op, ast.Or) and result):
                    return result
                result = read(value)
            return result
        raise ValueError('workflow selector uses an unreviewed input or operator')

    return read(tree.body)


def go_events():
    result = [{'Package': transition.PACKAGE, 'Action': 'start'}]
    for name in transition.TESTS:
        result.extend([{'Package': transition.PACKAGE, 'Action': 'run', 'Test': name},
                       {'Package': transition.PACKAGE, 'Action': 'pass', 'Test': name, 'Elapsed': 0.01}])
    result.append({'Package': transition.PACKAGE, 'Action': 'pass', 'Elapsed': 0.13})
    return result


def event_bytes(events):
    return b''.join(json.dumps(event).encode() + b'\n' for event in events)


def source_inventory():
    """Purely reconstruct the pinned map from current immutable application files."""
    paths = [path for prefix in ('cmd', 'internal', 'pkg')
             for path in (REPO / prefix).rglob('*') if path.is_file()]
    paths.extend(REPO / name for name in ('go.mod', 'go.sum'))
    result = {}
    for path in sorted(paths):
        data = path.read_bytes()
        result[path.relative_to(REPO).as_posix()] = {
            'mode': '100755' if path.stat().st_mode & 0o111 else '100644',
            'git_blob': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest(),
            'bytes': len(data), 'sha256': transition.digest(data),
        }
    return result


class SyntheticReceipt:
    """A complete helper fixture made from local source, never real observations."""
    def __init__(self, directory):
        self.directory = Path(directory)
        candidate = {'commit': transition.CANDIDATE, 'tree': transition.CANDIDATE_TREE,
                     'application_files': 173, 'application_sha256': transition.APPLICATION_SHA256}
        self.report = {
            'mode': transition.MODE, 'schema_version': 2,
            'provider': {'GITHUB_REPOSITORY': 'Kay0k1/cutmyvideo-core', 'GITHUB_EVENT_NAME': 'workflow_dispatch',
                         'GITHUB_RUN_ID': '987654', 'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_SHA': '1' * 40},
            'checkout': {'commit': '1' * 40, 'tree': '2' * 40}, 'expected_checkout': '1' * 40,
            'runtime_application_sources_unchanged': False,
            'groups': {group: {'status': 'pass', 'checks': {name: {'ok': True} for name in names}}
                       for group, names in transition.groups(base.GROUPS).items()},
            'images': {'current': {'image_id': 'sha256:' + 'b' * 64}},
            'cleanup': {'private_state_backup_config_keys_removed': True, 'private_workspace_retained': False},
            'transition': {'candidate': candidate, 'preparation': deepcopy(transition.PREPARATION),
                           'policy_sha256': transition.POLICY_SHA256,
                           'from_position': 4, 'to_position': 5, 'startup_timeout_seconds': 30,
                           'receipt_counts': {'groups': 10, 'checks': 53}, 'manifests': {}, 'catalogs': {}},
        }
        application = {
            'old_commit': transition.OLD_COMMIT, 'old_tree': transition.OLD_TREE,
            'old_reference': transition.OLD_REFERENCE, 'old_application_sha256': transition.OLD_APPLICATION_SHA256,
            'old_files': 164, 'candidate_commit': transition.CANDIDATE, 'candidate_tree': transition.CANDIDATE_TREE,
            'candidate_application_sha256': transition.APPLICATION_SHA256, 'candidate_files': 173,
            'schema_changing_upgrade': True,
        }
        self.report['groups']['identity']['checks']['approved_application_transition']['evidence'] = application
        source = {'commit': '1' * 40, 'tree': '2' * 40, 'files': source_inventory()}
        drivers = {}
        for name in ('scripts/self-hosting-acceptance.py', 'scripts/source-retention-transition.py',
                     '.github/workflows/self-hosting.yml'):
            data = (REPO / name).read_bytes()
            drivers[name] = {'bytes': len(data), 'sha256': transition.digest(data)}
            source['files'][name] = deepcopy(drivers[name])
        value = self.report['transition']
        value['context'] = self.write('transition-context.json', {
            'mode': transition.MODE, 'provider': self.report['provider'], 'checkout': self.report['checkout'],
            'candidate': candidate, 'old_commit': transition.OLD_COMMIT, 'old_tree': transition.OLD_TREE,
            'frozen_before_image_builds_and_database_media_changes': True, 'drivers': drivers,
        })
        self.context = {'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
                        'candidate': deepcopy(candidate), 'context_sha256': value['context']['sha256']}
        self.report['groups']['transition_schema']['checks']['old004_catalog_roles']['evidence'] = {
            'roles': self.write('old-roles.json', {
                'context': self.context, 'postgres_container_id': 'c' * 64,
                'owners': ['cutmy_migrate'], 'runtime_memberships': 0,
                'roles': [
                    {'name': 'cutmy_admin', 'superuser': True, 'createrole': True, 'createdb': True, 'bypassrls': True},
                    {'name': 'cutmy_migrate', 'superuser': False, 'createrole': False, 'createdb': False, 'bypassrls': False},
                    {'name': 'cutmy_runtime', 'superuser': False, 'createrole': False, 'createdb': False, 'bypassrls': False}],
                'refused_runtime_operations': ['managed_table_ddl', 'migration_history_dml', 'role_ddl'],
            }),
        }
        value['migration_observer'] = self.write('migration-observer.json', {
            'context': self.context, 'role': 'cutmy_migrate',
            'image_id': self.report['images']['current']['image_id'], 'source_commit': self.report['expected_checkout'],
            'container_id': 'e' * 64, 'command': ['server'], 'ports_published': False,
            'schema_observation_before_maintenance': True, 'single_writer_stopped_and_removed': True,
            'constraints': {'id': 'e' * 64, 'image_id': self.report['images']['current']['image_id'],
                            'role': 'migrate', 'user': '1000:1000', 'readonly': True,
                            'capabilities_dropped': True, 'no_new_privileges': True,
                            'secrets_separated': True, 'memory': 512 << 20, 'nano_cpus': 10**9,
                            'pids_limit': 128, 'restart': 'no', 'mount_destinations': ['/data']},
        })
        value['current_source'] = self.write('transition-source.json', source)
        self.report['images']['current']['source_sha256'] = value['current_source']['sha256']
        shapes = {}
        for position in (4, 5):
            raw = (REPO / f'internal/app/migrations/{position:03d}_schema.json').read_bytes()
            shapes[position] = transition.strict_json(raw)
            value['manifests'][f'{position:03d}'] = self.write(f'manifest-{position:03d}.json', raw)
        for phase, position in transition.CATALOG_PHASES.items():
            value['catalogs'][phase] = self.write('catalog-' + phase + '.json', {
                'position': position, 'shape': shapes[position],
                'context': self.context, 'phase': phase, 'postgres_container_id': 'c' * 64,
                'versions': sorted(item[0] for item in transition.MIGRATIONS[:position]),
                'migrations': [{'position': i + 1, 'version': version, 'checksum': checksum}
                               for i, (version, _, checksum) in enumerate(transition.MIGRATIONS[:position])],
            })
        self.report['groups']['transition_schema']['checks']['actual005_catalog_history_backfill']['evidence'] = {
            'pins': self.write('initial-pins.json', {
                'context': self.context, 'historical_preview_source': 'historical_fixture',
                'sources': [{'id': 'historical_fixture', 'jobs': 0, 'previews': 1, 'valid': True,
                             'revision': 0, 'actual_jobs': 0, 'actual_previews': 1}],
            }),
        }
        raw_go = self.write('go-events.jsonl', event_bytes(go_events()))
        value['go'] = {
            'provider': deepcopy(self.report['provider']), 'checkout': deepcopy(self.report['checkout']),
            'candidate': candidate, 'exit': 0, 'role': 'cutmy_admin', 'postgres_version_major': 17,
            'context': self.context, 'postgres_container_id': 'c' * 64,
            'isolated_schemas': True, 'raw': raw_go, 'tests': transition.validate_go_events(event_bytes(go_events())),
        }
        for group in ('transition_schema', 'transition_retention'):
            for name, item in self.report['groups'][group]['checks'].items():
                if name in transition.GO_ORACLES:
                    item['evidence'] = {'tests': list(transition.GO_ORACLES[name]), 'raw': deepcopy(raw_go)}
        observations = {name: True for name in (
            'old_writers_stopped', 'original_values_jobs_ledger_preserved', 'initial_pins_exact',
            'fault_original_values_unchanged', 'a1_original_values_unchanged', 'rollback_fresh_resources',
            'rollback_old_backup_unchanged', 'rollback_media_counters_ownership_jobs',
            'restore005_derived_and_audit_unchanged', 'restore005_real_export_decode',
        )}
        observations.update(provider=deepcopy(self.report['provider']), checkout=deepcopy(self.report['checkout']),
                            context=self.context,
                            migration_seconds=1, fault_seconds=0.5, fault_statement_timeout_ms=500,
                            a1_exit=1, a1_error_code='schema_incompatible',
                            reverse_method='fresh_restore_immutable004_backup')
        value['observations'] = self.write('transition-observations.json', observations)
        value['runtime_dml'] = self.write('runtime-dml.json', {
            'context': self.context, 'role': 'cutmy_runtime', 'function_execute': False, 'security_definer': False,
            'membership_and_idempotency_verified': True, 'rollback_originals_unchanged': True,
        })
        value['ttl'] = self.write('ttl.json', {
            'context': self.context, 'role': 'cutmy_runtime', 'fixture_false_zero_sources': 200, 'first_candidate_rows': 200,
            'first_quarantined': 200, 'second_candidate_rows': 1, 'healthy_tail_deleted': True,
            'pinned_sources_alive': 200, 'rollback_originals_unchanged': True,
            'candidate_cte_materialized': True, 'performance_claimed': False,
            'explain': [{'Plan': {'Subplan Name': 'CTE source_retention_candidates', 'Actual Rows': 200}}],
        })
        self.report['groups']['transition_retention']['checks']['bounded_plan_and_audit_limits']['evidence']['public_plan'] = deepcopy(value['ttl'])

    def write(self, name, value):
        data = value if isinstance(value, bytes) else transition.canonical(value)
        (self.directory / name).write_bytes(data)
        return {'path': name, 'bytes': len(data), 'sha256': transition.digest(data)}

    def change_raw(self, receipt, mutation):
        value = transition.strict_json((self.directory / receipt['path']).read_bytes())
        mutation(value)
        receipt.update(self.write(receipt['path'], value))

    def validate(self):
        transition.validate_transition(self.report, self.directory, base)


class ModeAndDefaultControls(unittest.TestCase):
    def test_same_schema_default_counts_and_original_group_names_remain(self):
        self.assertEqual(len(base.GROUPS), 7)
        self.assertEqual(sum(map(len, base.GROUPS.values())), 35)
        self.assertIn('application_sources_unchanged', base.GROUPS['identity'])
        args = base.parse_args(['--revision', '1' * 40, '--output', '/synthetic-control-output'])
        self.assertEqual(args.mode, 'same-schema')

    def test_explicit_known_mode_and_unknown_cli_mode(self):
        args = base.parse_args(['--mode', transition.MODE, '--revision', '1' * 40,
                                '--output', '/synthetic-control-output'])
        self.assertEqual(args.mode, transition.MODE)
        with patch('sys.stderr', new_callable=io.StringIO), self.assertRaises(SystemExit):
            base.parse_args(['--mode', 'unknown', '--revision', '1' * 40,
                             '--output', '/synthetic-control-output'])

    def test_specialization_refuses_missing_unknown_and_same_schema_before_base_init(self):
        fake = SimpleNamespace(Acceptance=type('PureBase', (), {'__init__': Mock()}), GROUPS=base.GROUPS)
        specialized = transition.acceptance_class(fake)
        for mode in (None, '', 'unknown', 'same-schema'):
            with self.subTest(mode=mode), self.assertRaises(RuntimeError):
                specialized(SimpleNamespace(mode=mode))
        fake.Acceptance.__init__.assert_not_called()

    def test_default_driver_refuses_changed_application_without_runtime_calls(self):
        acceptance = object.__new__(base.Acceptance)
        acceptance.old_source = {'files': {'internal/app/frozen.go': {'sha256': 'a' * 64}}}
        acceptance.current_source = {'files': {'internal/app/frozen.go': {'sha256': 'b' * 64}}}
        acceptance.report, acceptance.check, acceptance.run = {}, Mock(), Mock()
        with self.assertRaisesRegex(RuntimeError, 'application sources differ'):
            acceptance.application_identity()
        acceptance.check.assert_not_called()
        acceptance.run.assert_not_called()

    def test_transition_groups_are_separate_and_do_not_mutate_default_inventory(self):
        original = deepcopy(base.GROUPS)
        actual = transition.groups(base.GROUPS)
        self.assertEqual(len(actual), 10)
        self.assertEqual(sum(map(len, actual.values())), 53)
        self.assertEqual(sum(map(len, transition.EXTRA_GROUPS.values())), 18)
        self.assertEqual(len({name for names in actual.values() for name in names}), 53)
        self.assertIn('approved_application_transition', actual['identity'])
        self.assertNotIn('application_sources_unchanged', actual['identity'])
        self.assertEqual(base.GROUPS, original)

    def test_dispatcher_rejects_missing_unknown_partial_and_legacy_groups_or_checks(self):
        original = {
            'mode': transition.MODE, 'schema_version': 2, 'ok': True, 'errors': [],
            'groups': {group: {'status': 'pass', 'checks': {name: {'ok': True} for name in names}}
                       for group, names in transition.groups(base.GROUPS).items()},
        }
        mutations = [lambda report: report['groups'].pop('transition_retention'),
                     lambda report: report['groups'].update(unknown={'status': 'pass', 'checks': {}}),
                     lambda report: report['groups']['transition_schema'].update(status='running'),
                     lambda report: report['groups']['transition_retention']['checks'].pop('indexed_false_zero_tail'),
                     lambda report: report['groups']['transition_retention']['checks'].update(extra={'ok': True}),
                     lambda report: report['groups']['transition_retention']['checks']['indexed_false_zero_tail'].update(ok=False),
                     lambda report: report['groups']['transition_retention']['checks']['indexed_false_zero_tail'].update(ok='skip'),
                     lambda report: report['groups']['identity']['checks'].update(application_sources_unchanged={'ok': True})]
        for mutation in mutations:
            report = deepcopy(original); mutation(report)
            with self.subTest(mutation=mutation), self.assertRaisesRegex(RuntimeError, 'required group|check incomplete'):
                base.validate_report(report)


class WorkflowModeControls(unittest.TestCase):
    def setUp(self):
        self.workflow = yaml.load((REPO / '.github/workflows/self-hosting.yml').read_bytes(),
                                  Loader=yaml.BaseLoader)
        self.steps = self.workflow['jobs']['public-profile']['steps']
        self.selectors = [step['env']['ACCEPTANCE_MODE'] for step in self.steps
                          if 'ACCEPTANCE_MODE' in step.get('env', {})]
        self.go = [step for step in self.steps
                   if step.get('uses', '').startswith('actions/setup-go@')]

    def test_automatic_pull_request_and_push_select_transition_with_go(self):
        self.assertEqual(set(self.workflow['on']), {'pull_request', 'push', 'workflow_dispatch'})
        self.assertEqual(self.workflow['on']['push']['branches'], ['main'])
        self.assertEqual(len(self.selectors), 2)
        self.assertEqual(len(self.go), 1)
        for event in ('pull_request', 'push'):
            for supplied in ('', 'same-schema', transition.MODE, 'unknown'):
                with self.subTest(event=event, supplied=supplied):
                    for selector in self.selectors:
                        self.assertEqual(workflow_expression(selector, event, supplied), transition.MODE)
                    self.assertIs(workflow_expression(self.go[0]['if'], event, supplied), True)

    def test_dispatch_defaults_transition_and_preserves_explicit_legacy_choice(self):
        option = self.workflow['on']['workflow_dispatch']['inputs']['acceptance_mode']
        self.assertEqual(option['type'], 'choice')
        self.assertEqual(option['options'], ['same-schema', transition.MODE])
        self.assertEqual(option['required'], 'true')
        self.assertEqual(option['default'], transition.MODE)
        for supplied, needs_go in ((option['default'], True), ('same-schema', False)):
            with self.subTest(mode=supplied):
                for selector in self.selectors:
                    selected = workflow_expression(selector, 'workflow_dispatch', supplied)
                    self.assertEqual(selected, supplied)
                    self.assertEqual(base.parse_args(['--mode', selected, '--revision', '1' * 40,
                                                     '--output', '/synthetic-control-output']).mode, supplied)
                self.assertIs(workflow_expression(self.go[0]['if'], 'workflow_dispatch', supplied), needs_go)

    def test_missing_unknown_dispatch_mode_does_not_fall_back_or_start_go(self):
        for supplied in ('', 'unknown'):
            with self.subTest(mode=supplied):
                for selector in self.selectors:
                    selected = workflow_expression(selector, 'workflow_dispatch', supplied)
                    self.assertEqual(selected, supplied)
                    with patch('sys.stderr', new_callable=io.StringIO), self.assertRaises(SystemExit):
                        base.parse_args(['--mode', selected, '--revision', '1' * 40,
                                         '--output', '/synthetic-control-output'])
                self.assertIs(workflow_expression(self.go[0]['if'], 'workflow_dispatch', supplied), False)

    def test_flow_and_verification_share_bounded_selector_and_pinned_go_setup(self):
        self.assertEqual(len(self.selectors), 2)
        self.assertEqual(self.selectors[0], self.selectors[1])
        self.assertEqual(self.go[0]['uses'],
                         'actions/setup-go@924ae3a1cded613372ab5595356fb5720e22ba16')
        self.assertEqual(self.go[0]['with'], {'go-version': '1.27.2', 'cache': 'false'})
        mode_steps = [step for step in self.steps if 'ACCEPTANCE_MODE' in step.get('env', {})]
        self.assertTrue(all('--mode "$ACCEPTANCE_MODE"' in step['run'] for step in mode_steps))
        for expression in ('${{ github.sha }}', '${{ github.event.pull_request.head.sha }}',
                           '${{ inputs.unreviewed }}', '${{ contains(mode, "same-schema") }}'):
            with self.subTest(expression=expression), self.assertRaises(ValueError):
                workflow_expression(expression, 'pull_request', '')
        # The formerly implicit routing is a real counterexample for automatic events.
        self.assertEqual(workflow_expression("${{ inputs.acceptance_mode || 'same-schema' }}",
                                             'pull_request', ''), 'same-schema')
        self.assertIs(workflow_expression("inputs.acceptance_mode == 'source-retention-004-to-005'",
                                         'pull_request', ''), False)


class CandidateSourceControls(unittest.TestCase):
    """Mock every process and stop at the first base host-setup boundary."""
    def acceptance(self, files=None):
        files = files if files is not None else {
            name: (REPO / name).read_bytes() for name in source_inventory()}
        inventory = {name: {
            'mode': '100755' if (REPO / name).exists() and (REPO / name).stat().st_mode & 0o111 else '100644',
            'git_blob': hashlib.sha1(b'blob ' + str(len(data)).encode() + b'\0' + data).hexdigest(),
            'bytes': len(data), 'sha256': transition.digest(data),
        } for name, data in files.items()}
        blobs = {inventory[name]['git_blob']: data for name, data in files.items()}
        host_setup = Mock(side_effect=RuntimeError('synthetic base initialization boundary'))
        pure_base = SimpleNamespace(Acceptance=type('PureCandidateBase', (), {'initialize_host': host_setup}),
                                    GROUPS=base.GROUPS, REPO=REPO)
        value = object.__new__(transition.acceptance_class(pure_base))
        value.args = SimpleNamespace(revision='1' * 40)

        def git(repo, *args):
            if args == ('rev-parse', transition.CANDIDATE + '^{tree}'):
                return transition.CANDIDATE_TREE
            if args == ('status', '--porcelain', '--untracked-files=all'):
                return ''
            raise AssertionError('unexpected synthetic Git query')

        def run(argv):
            if 'merge-base' in argv:
                return b''
            if 'ls-tree' in argv:
                return b''.join((item['mode'] + ' blob ' + item['git_blob'] + '\t' + name + '\0').encode()
                                for name, item in inventory.items())
            if 'cat-file' in argv:
                return blobs[argv[-1]]
            raise AssertionError('unexpected synthetic process command')

        value.git, value.run = Mock(side_effect=git), Mock(side_effect=run)
        return value, host_setup

    def test_exact_application_descendant_reaches_only_mocked_host_setup(self):
        value, host_setup = self.acceptance()
        with self.assertRaisesRegex(RuntimeError, 'synthetic base initialization boundary'):
            value.initialize_host()
        host_setup.assert_called_once()
        self.assertIn((['git', '-c', 'safe.directory=' + str(REPO), '-C', REPO,
                        'merge-base', '--is-ancestor', transition.CANDIDATE, value.args.revision],),
                      [call.args for call in value.run.call_args_list])

    def test_wrong_tree_dirty_or_unrelated_checkout_refuses_before_host_setup(self):
        for fault in ('tree', 'dirty', 'unrelated'):
            value, host_setup = self.acceptance()
            if fault == 'tree':
                value.git.side_effect = lambda repo, *args: '0' * 40
            elif fault == 'dirty':
                value.git.side_effect = lambda repo, *args: (transition.CANDIDATE_TREE if args[0] == 'rev-parse'
                                                           else ' M internal/app/http.go')
            else:
                value.run.side_effect = RuntimeError('synthetic ancestry refusal')
            with self.subTest(fault=fault), self.assertRaises(RuntimeError):
                value.initialize_host()
            host_setup.assert_not_called()
            self.assertFalse(any('ls-tree' in call.args[0] for call in value.run.call_args_list))

    def test_missing_extra_or_changed_application_bytes_refuse_before_host_setup(self):
        original = {name: (REPO / name).read_bytes() for name in source_inventory()}
        changes = [lambda files: files.pop('internal/app/http_recovery_test.go'),
                   lambda files: files.update({'internal/app/unreviewed.go': b'package app\n'}),
                   lambda files: files.update({'internal/app/http.go': files['internal/app/http.go'] + b'\n'})]
        for change in changes:
            files = dict(original)
            change(files)
            value, host_setup = self.acceptance(files)
            with self.subTest(change=change), self.assertRaisesRegex(RuntimeError, 'reviewed immutable candidate'):
                value.initialize_host()
            host_setup.assert_not_called()


class JSONAndArtifactControls(unittest.TestCase):
    def test_strict_json_refuses_duplicates_at_every_receipt_depth(self):
        for data in (b'{"mode":"same-schema","mode":"source-retention-004-to-005"}',
                     b'{"groups":{"identity":{},"identity":{}}}',
                     b'{"checks":{"membership":{"ok":true},"membership":{"ok":true}}}',
                     b'{"raw":{"path":"a.json","path":"b.json"}}'):
            with self.subTest(data=data), self.assertRaisesRegex(RuntimeError, 'duplicate JSON key'):
                transition.strict_json(data)
        self.assertEqual(transition.strict_json(b'{"groups":{"identity":{"ok":true}}}'),
                         {'groups': {'identity': {'ok': True}}})

    def test_artifact_requires_actual_directory_and_exact_regular_file_bytes(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'raw.json'
            data = b'{"synthetic_control":true}\n'
            path.write_bytes(data)
            receipt = {'path': path.name, 'bytes': len(data), 'sha256': transition.digest(data)}
            self.assertEqual(transition.artifact(directory, receipt), data)
            with self.assertRaises(RuntimeError):
                transition.artifact(None, receipt)
            path.write_bytes(b'x' * len(data))
            with self.assertRaisesRegex(RuntimeError, 'bytes changed'):
                transition.artifact(directory, receipt)

    def test_artifact_refuses_traversal_absolute_size_type_hash_and_extra_fields(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'raw.json'
            path.write_bytes(b'{}\n')
            original = {'path': path.name, 'bytes': 3, 'sha256': transition.digest(b'{}\n')}
            mutations = ({'path': '../raw.json'}, {'path': str(path)}, {'path': 'sub/raw.json'},
                         {'path': 'Raw.json'}, {'path': 'raw.sql'}, {'bytes': 0}, {'bytes': True},
                         {'bytes': 3.0}, {'bytes': 32 * 1024 * 1024 + 1}, {'bytes': 4},
                         {'sha256': 'g' * 64}, {'sha256': 'f' * 64}, {'unexpected': True})
            for changes in mutations:
                receipt = {**original, **changes}
                with self.subTest(changes=changes), self.assertRaises(REFUSALS):
                    transition.artifact(directory, receipt)
            path.unlink()
            path.mkdir()
            with self.assertRaises(RuntimeError):
                transition.artifact(directory, original)

    def test_artifact_refuses_symlink_even_with_matching_target_bytes(self):
        import tempfile
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / 'target.json'
            target.write_bytes(b'{}\n')
            path = Path(directory) / 'raw.json'
            path.symlink_to(target)
            with self.assertRaises(RuntimeError):
                transition.artifact(directory, {'path': path.name, 'bytes': 3,
                                               'sha256': transition.digest(b'{}\n')})


class GoEventControls(unittest.TestCase):
    def test_thirteen_unique_bounded_passes_and_one_package_pass(self):
        observed = transition.validate_go_events(event_bytes(go_events()))
        self.assertEqual(set(observed), set(transition.TESTS))
        self.assertEqual(len(observed), 13)
        self.assertTrue(all(item == {'pass': True, 'elapsed_seconds': 0.01} for item in observed.values()))

    def test_valid_subtest_requires_run_and_does_not_replace_top_level(self):
        events = go_events()
        name = transition.TESTS[0] + '/synthetic_subtest'
        events[-1:-1] = [{'Package': transition.PACKAGE, 'Action': 'run', 'Test': name},
                         {'Package': transition.PACKAGE, 'Action': 'pass', 'Test': name, 'Elapsed': 0}]
        self.assertEqual(set(transition.validate_go_events(event_bytes(events))), set(transition.TESTS))
        events = [event for event in events if event.get('Test') != transition.TESTS[0]]
        with self.assertRaises(RuntimeError):
            transition.validate_go_events(event_bytes(events))

    def test_empty_malformed_duplicate_key_and_wrong_package_refuse(self):
        for data in (b'', b'\n', b'{', b'{"Package":"x","Package":"y"}\n'):
            with self.subTest(data=data), self.assertRaises(REFUSALS):
                transition.validate_go_events(data)
        for package in ('other/package', '', None):
            events = go_events(); events[0]['Package'] = package
            with self.subTest(package=package), self.assertRaises(RuntimeError):
                transition.validate_go_events(event_bytes(events))

    def test_failure_skip_missing_unknown_or_unfinished_test_cannot_pass(self):
        changes = [lambda events: events.pop(2),
                   lambda events: events.__setitem__(2, {**events[2], 'Action': 'fail'}),
                   lambda events: events.__setitem__(2, {**events[2], 'Action': 'skip'}),
                   lambda events: events.append({'Package': transition.PACKAGE, 'Action': 'run', 'Test': 'TestUnknown'}),
                   lambda events: events.append({'Package': transition.PACKAGE, 'Action': 'run',
                                                  'Test': 'TestUnknown/known_name'}),
                   lambda events: events.append({'Package': transition.PACKAGE, 'Action': 'invented_action'}),
                   lambda events: events.append({'Package': transition.PACKAGE, 'Action': 'run',
                                                  'Test': transition.TESTS[0] + '/unfinished'}),
                   lambda events: events.pop(),
                   lambda events: events.append(deepcopy(events[-1])),
                   lambda events: events.insert(1, deepcopy(events[1])),
                   lambda events: events.insert(3, deepcopy(events[2]))]
        for change in changes:
            events = go_events(); change(events)
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                transition.validate_go_events(event_bytes(events))

    def test_pass_without_run_or_unbounded_elapsed_cannot_pass(self):
        events = go_events(); events.pop(1)
        with self.assertRaises(RuntimeError):
            transition.validate_go_events(event_bytes(events))
        for elapsed in (-1, 240, float('inf'), float('nan'), True, '0.1', None):
            events = go_events(); events[2]['Elapsed'] = elapsed
            with self.subTest(elapsed=elapsed), self.assertRaises(RuntimeError):
                transition.validate_go_events(event_bytes(events))


class CatalogRepresentationControls(unittest.TestCase):
    """Model catalog JSON in pure controls; no SQL or hosted acceptance runs."""
    def manifest(self, position):
        return transition.strict_json((REPO / f'internal/app/migrations/{position:03d}_schema.json').read_bytes())

    def query_shape(self, position):
        shape = self.manifest(position)
        # jsonb_build_object in schemaShapeSQL always emits these keys. This
        # synthetic fixture models its representation, not observed DB output.
        for table in shape['Tables'].values():
            for trigger in table['Triggers'].values():
                trigger.setdefault('Columns', None)
                trigger.setdefault('When', '')
                trigger['Function'] = 'public.' + trigger['Function']
        return shape

    def catalog(self, position, shape):
        return {'position': position, 'shape': shape,
                'versions': sorted(item[0] for item in transition.MIGRATIONS[:position]),
                'migrations': [{'position': i + 1, 'version': version, 'checksum': checksum}
                               for i, (version, _, checksum) in enumerate(transition.MIGRATIONS[:position])],
                'context': {'synthetic_control_context': True},
                'phase': 'old004' if position == 4 else 'current005', 'postgres_container_id': 'c' * 64}

    def capture(self, position, shape):
        import tempfile
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        value = object.__new__(transition.acceptance_class(base))
        value.current_repo, value.output = REPO, Path(temp.name)
        value.report = {'transition': {'catalogs': {}}}
        catalog = self.catalog(position, shape)
        value.sql = Mock(side_effect=[transition.canonical(shape),
                                     transition.canonical({key: catalog[key] for key in ('versions', 'migrations')})])
        value.container = Mock(return_value={'Id': catalog['postgres_container_id']})
        value.raw_context = Mock(return_value=catalog['context'])
        value.run = Mock(side_effect=AssertionError('pure catalog controls cannot launch a subprocess'))
        return value, catalog

    def test_legacy004_and005_explicit_null_empty_fields_match_without_mutating_inputs(self):
        for position in (4, 5):
            expected, actual = self.manifest(position), self.query_shape(position)
            originals = deepcopy((expected, actual))
            with self.subTest(position=position):
                self.assertEqual(transition.normalize_shape(actual), transition.normalize_shape(expected))
                transition.validate_catalog(self.catalog(position, actual), position, expected)
                self.assertEqual((expected, actual), originals)
                for trigger in expected['Tables']['storage_files']['Triggers'].values():
                    self.assertNotIn('Columns', trigger)
                    self.assertNotIn('When', trigger)

    def test_legacy_trigger_nonempty_wrong_type_and_unknown_fields_still_refuse(self):
        changes = [lambda trigger: trigger.update(Columns=['kind']),
                   lambda trigger: trigger.update(Columns=[]),
                   lambda trigger: trigger.update(Columns='kind'),
                   lambda trigger: trigger.update(Columns=False),
                   lambda trigger: trigger.update(When='WHEN (old.kind <> new.kind)'),
                   lambda trigger: trigger.update(When=None),
                   lambda trigger: trigger.update(When=False),
                   lambda trigger: trigger.update(Unexpected='catalog drift')]
        for position in (4, 5):
            for change in changes:
                actual = self.query_shape(position)
                trigger = actual['Tables']['storage_files']['Triggers']['storage_file_update']
                change(trigger)
                with self.subTest(position=position, change=change), self.assertRaisesRegex(RuntimeError, 'catalog differs'):
                    transition.validate_catalog(self.catalog(position, actual), position, self.manifest(position))

    def test_exact005_membership_selected_columns_and_predicates_remain_authoritative(self):
        expected, actual = self.manifest(5), self.query_shape(5)
        selected = {'jobs': {'job_pin_fence_update': ['id', 'source_id']},
                    'sources': {'source_pin_fence_update': ['id'], 'source_pin_seed_update': ['id']},
                    'storage_reservations': {'preview_pin_fence_update': ['id', 'kind', 'job_id']}}
        for table, triggers in selected.items():
            for name, columns in triggers.items():
                normalized = transition.normalize_shape(actual)['Tables'][table]['Triggers'][name]
                self.assertEqual(normalized['Columns'], columns)
                self.assertEqual(normalized['When'], '')
                for field, value in (('Columns', None), ('Columns', []), ('Columns', columns + ['owner']),
                                     ('Columns', 'id'), ('When', 'WHEN (true)'), ('When', None)):
                    changed = deepcopy(actual)
                    changed['Tables'][table]['Triggers'][name][field] = value
                    with self.subTest(table=table, trigger=name, field=field, value=value), self.assertRaisesRegex(RuntimeError, 'catalog differs'):
                        transition.validate_catalog(self.catalog(5, changed), 5, expected)

    def test_captured_catalog_keeps_unmodified_query_json_and_validated_receipt(self):
        for position in (4, 5):
            value, captured = self.capture(position, self.query_shape(position))
            phase = captured['phase']
            with self.subTest(position=position):
                result = value.capture_catalog(phase, object(), position)
                receipt = value.report['transition']['catalogs'][phase]
                self.assertEqual(result, captured)
                self.assertEqual(value.report['transition']['catalog_observations'][phase], receipt)
                self.assertEqual(transition.strict_json(transition.artifact(value.output, receipt)), captured)
                self.assertTrue(captured['shape']['Tables']['storage_files']['Triggers']['storage_file_update']['Function'].startswith('public.'))
                self.assertEqual(value.sql.call_count, 2)
                value.run.assert_not_called()

    def test_mismatched_catalog_is_frozen_before_refusal_and_never_becomes_validated(self):
        shape = self.query_shape(4)
        shape['Tables']['storage_files']['Triggers']['storage_file_update']['When'] = 'WHEN (true)'
        value, captured = self.capture(4, shape)
        validate = transition.validate_catalog

        def validate_after_raw(actual, position, expected):
            self.assertEqual(value.report['transition']['catalogs'], {})
            receipt = value.report['transition']['catalog_observations']['old004']
            self.assertEqual(transition.strict_json(transition.artifact(value.output, receipt)), captured)
            validate(actual, position, expected)

        with patch.object(transition, 'validate_catalog', side_effect=validate_after_raw), self.assertRaisesRegex(RuntimeError, 'catalog differs'):
            value.capture_catalog('old004', object(), 4)
        receipt = value.report['transition']['catalog_observations']['old004']
        data = transition.artifact(value.output, receipt)
        self.assertEqual(transition.strict_json(data)['context'], captured['context'])
        self.assertEqual(transition.strict_json(data)['phase'], 'old004')
        self.assertEqual(value.report['transition']['catalogs'], {})
        with self.assertRaisesRegex(RuntimeError, 'cannot be overwritten'):
            value.raw(receipt['path'], b'{"replacement":true}')
        self.assertEqual(transition.artifact(value.output, receipt), data)
        value.run.assert_not_called()


class PublicRuntimeOutputControls(unittest.TestCase):
    """Exercise output parsing with mocks; never execute the generated SQL."""
    def acceptance(self, output):
        value = object.__new__(transition.acceptance_class(base))
        value.current, value.current_repo, value.token = object(), REPO, 'a' * 32
        value.report = {'transition': {}}
        value.snapshot = Mock(return_value={'synthetic_control_snapshot': True})
        value.sql = Mock(side_effect=[transition.canonical({'role': 'cutmy_runtime',
                                                          'function_execute': False, 'security_definer': False}),
                                      b't\n', output])
        value.run = Mock(side_effect=AssertionError('pure controls cannot launch a subprocess'))
        value.raw_context = Mock(return_value={'synthetic_parser_context': True})
        value.raw = Mock(side_effect=lambda name, data: {'path': name, 'bytes': len(data),
                                                        'sha256': transition.digest(data)})
        return value

    def output(self, *, reverse=False):
        prefix = 'acceptance_' + 'a' * 32
        plan = json.dumps([{'Plan': {'Subplan Name': 'CTE source_retention_candidates',
                                    'Actual Rows': 200}}], indent=2)
        counts = json.dumps({'first_quarantined': 200, 'pinned_sources_alive': 200})
        rows = ['deleted|||' + prefix + '|0|0', 'progress||||1|0']
        if reverse:
            rows.reverse()
        return (' \n' + plan + '\n' + counts + '\n' + '\n'.join(rows) + '\nt\n').encode()

    def test_four_line_tail_output_and_exact_owner_parse_in_either_row_order(self):
        for reverse in (False, True):
            value = self.acceptance(self.output(reverse=reverse))
            with self.subTest(reverse=reverse):
                value.public_runtime_oracles()
                self.assertEqual(value.sql.call_count, 3)
                self.assertTrue(all(call.kwargs == {'runtime': True} for call in value.sql.call_args_list))
                value.run.assert_not_called()
                self.assertEqual(value.raw.call_count, 2)
                ttl = transition.strict_json(value.raw.call_args_list[1].args[1])
                self.assertEqual(ttl['first_quarantined'], 200)
                self.assertEqual(ttl['second_candidate_rows'], 1)
                self.assertIs(ttl['healthy_tail_deleted'], True)
                self.assertIs(ttl['performance_claimed'], False)

    def test_missing_deleted_row_wrong_owner_progress_or_tail_result_refuse(self):
        output = self.output()
        prefix = ('acceptance_' + 'a' * 32).encode()
        outputs = [output.replace(b'deleted|||' + prefix + b'|0|0\n', b''),
                   output.replace(prefix + b'|0|0', b'foreign|0|0'),
                   output.replace(b'progress||||1|0', b'progress||||201|0'),
                   output[:-2] + b'f\n']
        for data in outputs:
            value = self.acceptance(data)
            with self.subTest(data=data), self.assertRaises(RuntimeError):
                value.public_runtime_oracles()
            value.run.assert_not_called()
            self.assertNotIn('ttl', value.report['transition'])


class FailureCleanupControls(unittest.TestCase):
    def test_failed_observation_write_still_runs_base_cleanup_and_redacts_error(self):
        cleanup = Mock()
        pure_base = SimpleNamespace(Acceptance=type('PureCleanupBase', (), {'close': cleanup}),
                                    GROUPS=base.GROUPS, redact=base.redact)
        value = object.__new__(transition.acceptance_class(pure_base))
        value.report = {'transition': {}, 'errors': []}
        value.observations = {'synthetic_control': True}
        value.secrets = ['synthetic-private-token']
        value.raw = Mock(side_effect=RuntimeError('synthetic-private-token failed write'))
        value.close()
        cleanup.assert_called_once()
        self.assertEqual(len(value.report['errors']), 1)
        self.assertIn('transition observation snapshot: [REDACTED] failed write', value.report['errors'][0])
        self.assertNotIn('synthetic-private-token', value.report['errors'][0])
        self.assertNotIn('observations', value.report['transition'])


class TransitionReceiptControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        inventory = source_inventory()
        if len(inventory) != 173 or transition.digest(transition.canonical(inventory)) != transition.APPLICATION_SHA256:
            raise AssertionError('source-only controls require exact reviewed application inventory')

    def setUp(self):
        import tempfile
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.fixture = SyntheticReceipt(self.temp.name)

    def refused(self, mutation):
        mutation(self.fixture.report)
        with self.assertRaises(REFUSALS):
            self.fixture.validate()

    def test_complete_synthetic_helper_receipt_passes_without_actual_operations(self):
        self.fixture.validate()

    def test_missing_unknown_mode_and_legacy_receipt_version_refuse(self):
        for field, value in (('mode', None), ('mode', 'same-schema'), ('mode', 'unknown'),
                             ('schema_version', 1), ('schema_version', 3)):
            original = deepcopy(self.fixture.report)
            self.refused(lambda report: report.update({field: value}))
            self.fixture.report = original
        self.refused(lambda report: report.pop('mode'))

    def test_candidate_tree_map_freeze_policy_counts_and_exact_positions_refuse_changes(self):
        original = deepcopy(self.fixture.report)
        changes = [lambda value: value['candidate'].update(commit='0' * 40),
                   lambda value: value['candidate'].update(tree='0' * 40),
                   lambda value: value['candidate'].update(application_files=164),
                   lambda value: value['candidate'].update(application_sha256='0988f39c1fcba6189acf57f2167aa72f79fc426c4af9e16981956b35676817db'),
                   lambda value: value['preparation'].update(source_freeze_sha256='0' * 64),
                   lambda value: value.update(policy_sha256='0' * 64),
                   lambda value: value.update(from_position=3), lambda value: value.update(to_position=4),
                   lambda value: value.update(startup_timeout_seconds=31),
                   lambda value: value.update(receipt_counts={'groups': 7, 'checks': 35})]
        for change in changes:
            with self.subTest(change=change):
                self.refused(lambda report: change(report['transition']))
                self.fixture.report = deepcopy(original)

    def test_historical670_pin_cannot_supply_current_candidate_or_application_evidence(self):
        original = deepcopy(self.fixture.report)
        historical = {'commit': '670089aa7cc99fe65e2a0336ff05d7c6b3047bd0',
                      'tree': 'dc83b8f861b2b6b823b8476e3c9ad273f7aa6bba',
                      'application_files': 168,
                      'application_sha256': 'f22a2be71284389a47631e11474dcee272c5278d7a5a0cd85c273dace7e22ccd'}
        self.refused(lambda report: report['transition'].update(candidate=historical))
        self.fixture.report = deepcopy(original)
        self.refused(lambda report: report['groups']['identity']['checks']['approved_application_transition']['evidence'].update(
            candidate_commit=historical['commit'], candidate_tree=historical['tree'],
            candidate_files=historical['application_files'], candidate_application_sha256=historical['application_sha256']))

    def test_recovery_review_roots_are_required_in_addition_to_historical_roots(self):
        historical = {
            'plan_sha256': '35021688ee2e420ce7135efa2e45a3042f07fdc4533cff5a7bb120de08f5a2f5',
            'source_freeze_sha256': '499cbbaf12714ad68201b5549f4e5a3c2ec8304f1bdfb6461790585ebc37038a',
            'root_review_sha256': '703fba88d8acaea6a1befa5ffcd9ea52cf175b10a77bc292255be8fa805c99d3',
            'root670_review_sha256': '56759ddc1dd8289a4a834df466f6e7e85e23f6f68a29be0414a5ce5e744f8260',
            'root670_public_driver_binding_sha256': '5c6cb808cf900532a012d304c0f168e994a2afd0bfb614c5a961529e232e676f',
        }
        reviewed = {
            'recovery_application_source_freeze_sha256': 'fc86f8ebd00761c01fb47b577dade04b477ebe04ffe477efd0d20621e80f25e5',
            'root_recovery_application_source_review_sha256': 'b0ca5a8023b85ba7afbf08e2519d146433f4a9933b9cc4913a108f032ff33de3',
            'root_recovery_application_committed_review_sha256': '1023ad3bcd365253782d863b6d059541e86b796ac7d4c48e2076d6cf428a8f6c',
        }
        self.assertEqual(transition.PREPARATION, {**historical, **reviewed})
        original = deepcopy(self.fixture.report)
        for name in reviewed:
            with self.subTest(root=name, fault='missing'):
                self.refused(lambda report: report['transition']['preparation'].pop(name))
                self.fixture.report = deepcopy(original)
            with self.subTest(root=name, fault='borrowed'):
                self.refused(lambda report: report['transition']['preparation'].update({name: historical['root670_review_sha256']}))
                self.fixture.report = deepcopy(original)
        self.refused(lambda report: report['transition'].update(preparation=historical))

    def test_transition_identity_counts_and_provider_require_exact_scalar_types(self):
        original = deepcopy(self.fixture.report)
        changes = [lambda report: report.update(schema_version=2.0),
                   lambda report: report['transition'].update(from_position=4.0),
                   lambda report: report['transition'].update(to_position=5.0),
                   lambda report: report['transition'].update(startup_timeout_seconds=30.0),
                   lambda report: report['transition']['candidate'].update(application_files=173.0),
                   lambda report: report['transition']['receipt_counts'].update(checks=53.0),
                   lambda report: report['provider'].update(GITHUB_RUN_ID=987654),
                   lambda report: report['groups']['identity']['checks']['approved_application_transition']['evidence'].update(old_files=164.0),
                   lambda report: report['groups']['identity']['checks']['approved_application_transition']['evidence'].update(schema_changing_upgrade=1)]
        for change in changes:
            with self.subTest(change=change):
                self.refused(change)
                self.fixture.report = deepcopy(original)

    def test_old_sealed_identity_and_same_application_equality_cannot_substitute(self):
        original = deepcopy(self.fixture.report)
        changes = {'old_commit': transition.CANDIDATE, 'old_tree': transition.CANDIDATE_TREE,
                   'old_reference': transition.CANDIDATE, 'old_application_sha256': transition.APPLICATION_SHA256,
                   'old_files': 173, 'schema_changing_upgrade': False}
        for field, value in changes.items():
            with self.subTest(field=field):
                self.refused(lambda report: report['groups']['identity']['checks']['approved_application_transition']['evidence'].update({field: value}))
                self.fixture.report = deepcopy(original)
        self.refused(lambda report: report.update(runtime_application_sources_unchanged=True))

    def test_context_requires_frozen_actual_provider_run_attempt_checkout_and_drivers(self):
        receipt = self.fixture.report['transition']['context']
        raw = (self.fixture.directory / receipt['path']).read_bytes()
        original = deepcopy(receipt)
        changes = [lambda value: value['provider'].update(GITHUB_RUN_ID='123456'),
                   lambda value: value['provider'].update(GITHUB_RUN_ATTEMPT='2'),
                   lambda value: value['provider'].update(GITHUB_REPOSITORY='other/repo'),
                   lambda value: value['checkout'].update(commit='9' * 40),
                   lambda value: value['checkout'].update(tree='9' * 40),
                   lambda value: value.update(frozen_before_image_builds_and_database_media_changes=False),
                   lambda value: value.update(old_commit=transition.CANDIDATE),
                   lambda value: value['drivers'].pop('scripts/source-retention-transition.py'),
                   lambda value: value['drivers'].update({'borrowed.py': {'bytes': 1, 'sha256': 'a' * 64}}),
                   lambda value: value['drivers']['scripts/self-hosting-acceptance.py'].update(bytes=0)]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_running_verifier_bytes_must_equal_the_frozen_driver_identity(self):
        receipt = self.fixture.report['transition']['context']
        self.fixture.change_raw(receipt, lambda value: value['drivers']['scripts/source-retention-transition.py'].update(sha256='0' * 64))
        with self.assertRaisesRegex(RuntimeError, 'executing verifier/driver bytes differ'):
            self.fixture.validate()

    def test_raw_source_hash_file_map_or_driver_bytes_cannot_be_replaced(self):
        receipt = self.fixture.report['transition']['current_source']
        raw = (self.fixture.directory / receipt['path']).read_bytes()
        original = deepcopy(receipt)
        changes = [lambda value: value.update(commit=transition.CANDIDATE),
                   lambda value: value.update(tree='0' * 40),
                   lambda value: value['files'].pop('go.mod'),
                   lambda value: value['files']['go.mod'].update(git_blob='0' * 40),
                   lambda value: value['files']['scripts/source-retention-transition.py'].update(sha256='0' * 64)]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_actual_catalog_missing_phase_position_checksum_version_and_body_refuse(self):
        report = self.fixture.report
        old = report['transition']['catalogs'].pop('fault004_after')
        with self.assertRaises(RuntimeError): self.fixture.validate()
        report['transition']['catalogs']['fault004_after'] = old
        receipt = report['transition']['catalogs']['current005']
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda value: value.update(position=4), lambda value: value.update(position=5.0),
                   lambda value: value.update(phase='old004'),
                   lambda value: value.update(postgres_container_id='mutable_container_name'),
                   lambda value: value['versions'].pop(),
                   lambda value: value['migrations'][0].update(checksum='0' * 64),
                   lambda value: value['migrations'][4].update(position=4),
                   lambda value: value['migrations'][4].update(position=5.0),
                   lambda value: value['shape']['Functions'].update({'borrowed_function': {}})]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_cancelled_migration_and_a1_guard_require_same_actual_postgres_container(self):
        for phase in ('fault004_after', 'guard005_after'):
            receipt = self.fixture.report['transition']['catalogs'][phase]
            raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
            self.fixture.change_raw(receipt, lambda value: value.update(postgres_container_id='d' * 64))
            with self.subTest(phase=phase), self.assertRaisesRegex(RuntimeError, 'mutated actual catalog/history/container'):
                self.fixture.validate()
            (self.fixture.directory / receipt['path']).write_bytes(raw)
            receipt.update(original)

    def test_initial_backfill_requires_exact_historical_preview_and_actual_counts(self):
        receipt = self.fixture.report['groups']['transition_schema']['checks']['actual005_catalog_history_backfill']['evidence']['pins']
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda value: value.update(sources=[]),
                   lambda value: value.update(historical_preview_source='missing_source'),
                   lambda value: value['sources'][0].update(previews=0, actual_previews=0),
                   lambda value: value['sources'][0].update(previews=2),
                   lambda value: value['sources'][0].update(actual_jobs=1),
                   lambda value: value['sources'][0].update(valid=False),
                   lambda value: value['sources'][0].update(valid=1),
                   lambda value: value['sources'][0].update(revision=1),
                   lambda value: value['sources'][0].update(revision=0.0),
                   lambda value: value['sources'][0].pop('revision'),
                   lambda value: value['sources'][0].update(previews=1.0),
                   lambda value: value['sources'].append(deepcopy(value['sources'][0]))]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_old004_role_raw_requires_context_ownership_memberships_denials_and_boolean_privileges(self):
        boundary = self.fixture.report['groups']['transition_schema']['checks']['old004_catalog_roles']['evidence']
        receipt = boundary.pop('roles')
        with self.assertRaises(REFUSALS): self.fixture.validate()
        boundary['roles'] = receipt
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda value: value.pop('context'),
                   lambda value: value.update(postgres_container_id='d' * 64),
                   lambda value: value.update(owners=['cutmy_runtime']),
                   lambda value: value.update(runtime_memberships=1),
                   lambda value: value.update(runtime_memberships=False),
                   lambda value: value.update(runtime_memberships=0.0),
                   lambda value: value['refused_runtime_operations'].pop(),
                   lambda value: value.update(refused_runtime_operations=['asserted_no_privileges']),
                   lambda value: value['roles'][0].update(superuser=False),
                   lambda value: value['roles'][0].update(superuser=1),
                   lambda value: value['roles'][0].update(createrole=1),
                   lambda value: value['roles'][1].update(createrole=True),
                   lambda value: value['roles'][2].update(superuser=0)]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_before_maintenance_observer_requires_owned_identity_role_ports_cleanup_and_constraints(self):
        value = self.fixture.report['transition']
        receipt = value.pop('migration_observer')
        with self.assertRaises(REFUSALS): self.fixture.validate()
        value['migration_observer'] = receipt
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda proof: proof.update(role='cutmy_admin'),
                   lambda proof: proof.update(image_id='sha256:' + '0' * 64),
                   lambda proof: proof.update(source_commit=transition.OLD_COMMIT),
                   lambda proof: proof.update(container_id='mutable_container_name'),
                   lambda proof: proof.update(command=['maintenance']),
                   lambda proof: proof.update(ports_published=True),
                   lambda proof: proof.update(ports_published=0),
                   lambda proof: proof.update(schema_observation_before_maintenance=False),
                   lambda proof: proof.update(single_writer_stopped_and_removed=False),
                   lambda proof: proof['constraints'].update(id='d' * 64),
                   lambda proof: proof['constraints'].update(user='0:0'),
                   lambda proof: proof['constraints'].update(readonly=False),
                   lambda proof: proof['constraints'].update(capabilities_dropped=False),
                   lambda proof: proof['constraints'].update(no_new_privileges=False),
                   lambda proof: proof['constraints'].update(secrets_separated=False),
                   lambda proof: proof['constraints'].update(memory=1024 << 20),
                   lambda proof: proof['constraints'].update(nano_cpus=2 * 10**9),
                   lambda proof: proof['constraints'].update(pids_limit=256),
                   lambda proof: proof['constraints'].update(restart='always'),
                   lambda proof: proof['constraints'].update(mount_destinations=['/data', '/operator-secrets'])]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_missing_or_changed_manifest_is_not_catalog_proof(self):
        value = self.fixture.report['transition']
        receipt = value['manifests'].pop('005')
        with self.assertRaises(RuntimeError): self.fixture.validate()
        value['manifests']['005'] = receipt
        data = (self.fixture.directory / receipt['path']).read_bytes()
        receipt.update(self.fixture.write(receipt['path'], data + b'\n'))
        with self.assertRaisesRegex(RuntimeError, 'manifest bytes differ'):
            self.fixture.validate()

    def test_go_provider_role_version_isolation_exit_and_named_proof_cannot_be_borrowed(self):
        original = deepcopy(self.fixture.report)
        changes = [lambda value: value['provider'].update(GITHUB_RUN_ATTEMPT='2'),
                   lambda value: value['checkout'].update(commit='0' * 40),
                   lambda value: value['context'].update(context_sha256='0' * 64),
                   lambda value: value.update(postgres_container_id='mutable_postgres_name'),
                   lambda value: value.update(postgres_container_id='d' * 64),
                   lambda value: value.update(role='cutmy_runtime'),
                   lambda value: value.update(postgres_version_major=16),
                   lambda value: value.update(postgres_version_major=17.0),
                   lambda value: value.update(isolated_schemas=False), lambda value: value.update(exit=1),
                   lambda value: value.update(exit=False),
                   lambda value: value['tests'].pop(transition.TESTS[0])]
        for change in changes:
            with self.subTest(change=change):
                self.refused(lambda report: change(report['transition']['go']))
                self.fixture.report = deepcopy(original)
        self.refused(lambda report: report['groups']['transition_retention']['checks']['indexed_false_zero_tail']['evidence'].update(tests=[]))
        self.fixture.report = deepcopy(original)
        self.refused(lambda report: report['groups']['transition_retention']['checks']['indexed_false_zero_tail']['evidence']['raw'].update(path='borrowed.jsonl'))

    def test_bounded_plan_and_audit_requires_actual_plan_and_child_tests(self):
        original = deepcopy(self.fixture.report)
        for change in (lambda proof: proof.pop('public_plan'),
                       lambda proof: proof['public_plan'].update(path='borrowed-plan.json'),
                       lambda proof: proof.update(tests=[transition.TESTS[10]])):
            with self.subTest(change=change):
                self.refused(lambda report: change(report['groups']['transition_retention']['checks']['bounded_plan_and_audit_limits']['evidence']))
                self.fixture.report = deepcopy(original)

    def test_observed_preservation_deadline_a1_guard_and_fresh_reverse_are_required(self):
        receipt = self.fixture.report['transition']['observations']
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda value: value.update(old_writers_stopped=False),
                   lambda value: value.update(initial_pins_exact=False),
                   lambda value: value.update(fault_original_values_unchanged=False),
                   lambda value: value.update(rollback_old_backup_unchanged=False),
                   lambda value: value.update(restore005_derived_and_audit_unchanged=False),
                   lambda value: value.update(restore005_real_export_decode=False),
                   lambda value: value.update(migration_seconds=30.01),
                   lambda value: value.update(fault_seconds=31),
                   lambda value: value.update(fault_statement_timeout_ms=30000),
                   lambda value: value.update(fault_statement_timeout_ms=500.0),
                   lambda value: value.update(a1_exit=0),
                   lambda value: value.update(a1_exit=True),
                   lambda value: value.update(a1_error_code='other'),
                   lambda value: value.update(reverse_method='drop005_columns_and_markers'),
                   lambda value: value['provider'].update(GITHUB_RUN_ID='123456')]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_private_state_and_workspace_cleanup_cannot_be_partial(self):
        original = deepcopy(self.fixture.report)
        self.refused(lambda report: report['cleanup'].update(private_state_backup_config_keys_removed=False))
        self.fixture.report = original
        self.refused(lambda report: report['cleanup'].update(private_workspace_retained=True))

    def test_runtime_dml_requires_denied_execute_invoker_membership_and_rollback(self):
        receipt = self.fixture.report['transition']['runtime_dml']
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        for field, value in (('role', 'cutmy_admin'), ('function_execute', True), ('security_definer', True),
                             ('membership_and_idempotency_verified', False), ('rollback_originals_unchanged', False),
                             ('function_execute', 0), ('security_definer', 0), ('membership_and_idempotency_verified', 1)):
            with self.subTest(field=field):
                self.fixture.change_raw(receipt, lambda item: item.update({field: value}))
                with self.assertRaisesRegex(RuntimeError, 'runtime invoker DML proof missing|runtime observation booleans differ'):
                    self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_ttl_requires_false_zero_quarantine_healthy_tail_actual_plan_and_no_performance_claim(self):
        receipt = self.fixture.report['transition']['ttl']
        raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
        changes = [lambda value: value.update(role='cutmy_admin'),
                   lambda value: value.update(first_candidate_rows=201),
                   lambda value: value.update(first_candidate_rows=200.0),
                   lambda value: value.update(first_quarantined=199),
                   lambda value: value.update(second_candidate_rows=0),
                   lambda value: value.update(healthy_tail_deleted=False),
                   lambda value: value.update(pinned_sources_alive=199),
                   lambda value: value.update(rollback_originals_unchanged=False),
                   lambda value: value.update(candidate_cte_materialized=False),
                   lambda value: value.update(performance_claimed=True),
                   lambda value: value.update(explain=[]),
                   lambda value: value['explain'][0]['Plan'].update({'Actual Rows': 201}),
                   lambda value: value['explain'][0]['Plan'].update({'Actual Rows': 200.0}),
                   lambda value: value['explain'][0]['Plan'].update({'Subplan Name': 'borrowed CTE'}),
                   lambda value: value['explain'][0]['Plan'].update({'Plans': [deepcopy(value['explain'][0]['Plan'])]})]
        for change in changes:
            with self.subTest(change=change):
                self.fixture.change_raw(receipt, change)
                with self.assertRaises(REFUSALS): self.fixture.validate()
                (self.fixture.directory / receipt['path']).write_bytes(raw)
                receipt.update(original)

    def test_each_phase_raw_proof_binds_provider_checkout_candidate_and_frozen_context(self):
        value = self.fixture.report['transition']
        receipts = [value['runtime_dml'], value['ttl'], value['observations'], value['catalogs']['current005'],
                    value['migration_observer'],
                    self.fixture.report['groups']['transition_schema']['checks']['old004_catalog_roles']['evidence']['roles'],
                    self.fixture.report['groups']['transition_schema']['checks']['actual005_catalog_history_backfill']['evidence']['pins']]
        changes = [lambda context: context['provider'].update(GITHUB_RUN_ID='123456'),
                   lambda context: context['checkout'].update(tree='0' * 40),
                   lambda context: context['candidate'].update(commit=transition.OLD_COMMIT),
                   lambda context: context.update(context_sha256='0' * 64)]
        for receipt in receipts:
            raw, original = (self.fixture.directory / receipt['path']).read_bytes(), deepcopy(receipt)
            for change in changes:
                with self.subTest(path=receipt['path'], change=change):
                    self.fixture.change_raw(receipt, lambda proof: change(proof['context']))
                    with self.assertRaises(REFUSALS): self.fixture.validate()
                    (self.fixture.directory / receipt['path']).write_bytes(raw)
                    receipt.update(original)

    def test_raw_evidence_directory_is_mandatory_and_changed_bytes_refuse(self):
        with self.assertRaises(RuntimeError):
            transition.validate_transition(self.fixture.report, None, base)
        receipt = self.fixture.report['transition']['go']['raw']
        path = self.fixture.directory / receipt['path']
        data = path.read_bytes()
        path.write_bytes(b'x' + data[1:])
        with self.assertRaisesRegex(RuntimeError, 'bytes changed'):
            self.fixture.validate()


if __name__ == '__main__':
    unittest.main()
