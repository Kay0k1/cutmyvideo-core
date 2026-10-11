"""Pure controls. Never invoke Docker, firewall, media tools or a database."""
from copy import deepcopy
import importlib.util
import io
import json
import math
import os
from pathlib import Path
import signal
import ssl
import subprocess
import struct
import tempfile
import time
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


ROOT = Path(__file__).resolve().parent
spec = importlib.util.spec_from_file_location('self_hosting_acceptance', ROOT / 'self-hosting-acceptance.py')
a = importlib.util.module_from_spec(spec)
spec.loader.exec_module(a)


def successful_report():
    current, head = '1' * 40, '2' * 40
    def image(commit, tree, image_hash, source_hash):
        version, stamp = 'v0.0.0-candidate.g' + commit, '2026-10-11T01:00:00Z'
        return {'source_commit': commit, 'tree': tree, 'source_sha256': source_hash * 64,
                'image_id': 'sha256:' + image_hash * 64, 'version': version, 'build_time': stamp,
                'toolchain': 'go1.27.2', 'runtime_version': {'version': version, 'commit': commit,
                 'build_time': stamp, 'go_version': 'go1.27.2', 'os': 'linux', 'arch': 'amd64'},
                'tools': {'ffmpeg': 'FFmpeg fixture', 'ffprobe': 'ffprobe fixture', 'yt_dlp': 'fixture', 'node': 'fixture'}}
    now = image(current, '3' * 40, 'b', 'c')
    report = {'schema_version': 1, 'ok': True, 'errors': [], 'expected_checkout': current,
            'provider': {'GITHUB_REPOSITORY': 'Kay0k1/cutmyvideo-core', 'GITHUB_EVENT_NAME': 'pull_request',
                         'GITHUB_RUN_ID': '123456', 'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_SHA': current},
            'checkout': {'commit': current, 'tree': '3' * 40, 'parents': ['4' * 40, head],
                         'head_commit': head, 'head_tree': '3' * 40, 'head_parents': [a.UPGRADE_FROM],
                         'approved_A_is_ancestor': True, 'clean': True},
            'images': {'old': image(a.UPGRADE_FROM, a.UPGRADE_TREE, 'a', 'd'),
                       'current': now, 'restored': deepcopy(now)},
            'groups': {name: {'status': 'pass', 'checks': {check: {'ok': True} for check in checks}}
                       for name, checks in a.GROUPS.items()},
            'cleanup': {'ok': True, 'errors': [], 'remaining_resources': [], 'live_owned_processes': []},
            'runtime_application_sources_unchanged': True}
    for label, baseline, container in (('baseline', True, 'a'), ('current', False, 'b'), ('restored', False, 'c')):
        report[label + '_tcp_readiness'] = {'container_id': container * 64,
            'source_commit': a.UPGRADE_FROM if baseline else current, 'tcp_sql_verified': True,
            'healthcheck': a.SOCKET_HEALTHCHECK_A if baseline else a.TCP_HEALTHCHECK,
            'baseline_socket_healthcheck_workaround': baseline, 'attempts': [{'exit': 0, 'tcp_sql_ready': True}]}
    return report


class ReceiptControls(unittest.TestCase):
    def test_complete_report_and_actual_merge_head_pass(self):
        a.validate_report(successful_report())

    def test_missing_extra_or_partial_groups_and_checks_fail_closed(self):
        mutations = [lambda r: r['groups'].pop('roles'),
                     lambda r: r['groups'].update({'unknown': {'status': 'pass', 'checks': {}}}),
                     lambda r: r['groups']['egress'].update(status='running'),
                     lambda r: r['groups']['roles']['checks'].pop('runtime_privilege_denials'),
                     lambda r: r['groups']['roles']['checks'].update(extra={'ok': True}),
                     lambda r: r['groups']['roles']['checks']['runtime_privilege_denials'].update(ok=False)]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                report = successful_report(); mutation(report)
                with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_failed_or_incomplete_cleanup_cannot_be_pass(self):
        for field, value in [('ok', False), ('errors', ['timeout']), ('remaining_resources', ['container']),
                             ('live_owned_processes', ['listener'])]:
            with self.subTest(field=field):
                report = successful_report(); report['cleanup'][field] = value
                with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_wrong_provider_head_ancestor_tree_and_checkout_fail(self):
        mutations = [lambda r: r['provider'].update(GITHUB_REPOSITORY='other/repo'),
                     lambda r: r['provider'].update(GITHUB_RUN_ID='0'),
                     lambda r: r['provider'].update(GITHUB_RUN_ATTEMPT=''),
                     lambda r: r['provider'].update(GITHUB_SHA='9' * 40),
                     lambda r: r['checkout'].update(clean=False),
                     lambda r: r['checkout'].update(approved_A_is_ancestor=False),
                     lambda r: r['checkout'].update(head_tree='8' * 40),
                     lambda r: r['checkout'].update(parents=['4' * 40, '8' * 40]),
                     lambda r: r['checkout'].update(head_parents=[]),
                     lambda r: r['images']['current'].update(tree='8' * 40)]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                report = successful_report(); mutation(report)
                with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_mixed_image_source_version_tools_and_unperformed_upgrade_fail(self):
        mutations = [lambda r: r['images']['restored'].update(source_sha256='0' * 64),
                     lambda r: r['images']['restored']['runtime_version'].update(commit='0' * 40),
                     lambda r: r['images']['old'].update(tree='0' * 40),
                     lambda r: r['images']['old'].update(image_id=r['images']['current']['image_id']),
                     lambda r: r['images']['current'].update(version=''),
                     lambda r: r['images']['current'].update(build_time=''),
                     lambda r: r['images']['current']['tools'].pop('node'),
                     lambda r: r.update(runtime_application_sources_unchanged=False)]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                report = successful_report(); mutation(report)
                with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_push_must_use_checkout_as_head(self):
        report = successful_report(); report['provider']['GITHUB_EVENT_NAME'] = 'push'
        with self.assertRaises(RuntimeError): a.validate_report(report)
        report['checkout'].update(head_commit=report['expected_checkout'], head_tree=report['checkout']['tree'])
        a.validate_report(report)

    def test_socket_only_current_health_missing_tcp_witness_or_helper_stage_cannot_pass(self):
        for mutation in (lambda r: r['current_tcp_readiness'].update(healthcheck=a.SOCKET_HEALTHCHECK_A),
                         lambda r: r.pop('restored_tcp_readiness'),
                         lambda r: r['baseline_tcp_readiness'].update(baseline_socket_healthcheck_workaround=False),
                         lambda r: r['current_tcp_readiness']['attempts'][-1].update(tcp_sql_ready=False),
                         lambda r: r['groups']['container_constraints']['checks'].pop('owned_helper_partial_create_cleanup')):
            report = successful_report(); mutation(report)
            with self.assertRaises(RuntimeError): a.validate_report(report)


class HTTPAndRetentionControls(unittest.TestCase):
    def test_decode_oracle_rejects_truncation_and_silent_full_span(self):
        with tempfile.TemporaryDirectory() as directory:
            obj = a.Acceptance.__new__(a.Acceptance); obj.output = Path(directory)
            obj.json = Mock(return_value={'format': {'duration': '0.21'}, 'streams': [{'codec_type': 'audio'}]})
            obj.run = Mock()
            with self.assertRaisesRegex(RuntimeError, 'requested span'):
                obj.decode(b'controlled non-media placeholder', 'truncated.mp3', video=False)
            obj.run.assert_not_called()
            obj.json.return_value['format']['duration'] = '1.5'
            pcm = struct.pack('<' + 'h' * 12000, *[int(1500 * math.sin(2 * math.pi * 660 * i / 8000)) for i in range(12000)])
            obj.run.side_effect = [b'', pcm]
            self.assertEqual(obj.decode(b'controlled placeholder', 'full.mp3', video=False)['decoded_audio_samples_8000hz'], 12000)
            obj.run.side_effect = [b'', b'\0' * 24000]
            with self.assertRaisesRegex(RuntimeError, 'selected-span audio'):
                obj.decode(b'controlled placeholder', 'silent.mp3', video=False)

    def test_real_client_delete_requires_204_and_rejects_200(self):
        client = a.Client.__new__(a.Client)
        client.domain, client.cookie, client.context = 'fixture.acceptance.test', '', ssl.create_default_context()
        records = []; client.record, client.secret = records.append, lambda x: None
        for status in (204, 200):
            response = Mock(status=status)
            response.read.return_value = b''; response.getheaders.return_value = []
            response.getheader.return_value = None
            connection = Mock(); connection.getresponse.return_value = response
            with patch.object(a, 'TLSConnection', return_value=connection):
                if status == 204:
                    client.delete('fixture-id')
                else:
                    with self.assertRaises(RuntimeError): client.delete('fixture-id')
            self.assertEqual(connection.request.call_args.args[:2], ('DELETE', '/api/v1/sources/fixture-id'))
            connection.close.assert_called_once()
        self.assertEqual([r['status'] for r in records], [204, 200])

    def test_retained_outputs_can_be_verified_and_corruption_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            obj = a.Acceptance.__new__(a.Acceptance)
            obj.input = Path(directory) / 'input'; obj.input.write_bytes(b'actual retained source fixture')
            body = {'format': 'mp4'}; payload = b'actual retained artifact fixture'
            original = {'source': {'id': 'source-id'}, 'body': body, 'key': 'old-key',
                        'job': {'id': 'job-id', 'items': [{'id': 'item-id'}]},
                        'artifact': {'id': 'artifact-id', 'download_url': '/download'},
                        'decoded': {'sha256': a.digest(payload)}}
            obj.retained_outputs = [original]
            obj.decode = Mock(return_value={'actual': 'decoded'})
            owner, foreign = Mock(), Mock()
            owner.request.side_effect = [({'id': 'source-id'}, {}), (obj.input.read_bytes(), {}),
                                        ({'id': 'job-id'}, {}), ({'items': [{'id': 'item-id', 'artifact': {'id': 'artifact-id'}}]}, {}),
                                        (payload, {})]
            self.assertEqual(obj.verify_retained({}, owner, foreign, prefix='upgraded'), [{'actual': 'decoded'}])
            self.assertEqual(owner.request.call_args_list[2].kwargs['headers'], {'Idempotency-Key': 'old-key'})
            foreign.request.assert_called_once_with('GET', '/download', expected=404)
            owner.request.side_effect = [({'id': 'source-id'}, {}), (b'corrupt source', {})]
            with self.assertRaisesRegex(RuntimeError, 'source bytes changed'):
                obj.verify_retained({}, owner, foreign, prefix='restored')
            # The media stage must store its outputs without shadowing this
            # verifier. This catches the original list-as-method regression.
            import ast
            media = next(n for n in ast.walk(ast.parse((ROOT / 'self-hosting-acceptance.py').read_text()))
                         if isinstance(n, ast.FunctionDef) and n.name == 'old_media')
            assigned = [n.attr for n in ast.walk(media) if isinstance(n, ast.Attribute) and isinstance(n.ctx, ast.Store)]
            self.assertIn('retained_outputs', assigned)
            self.assertNotIn('verify_retained', assigned)

    def test_two_project_duplicate_apply_and_remove_require_stopped_writers(self):
        with tempfile.TemporaryDirectory() as directory:
            obj = a.Acceptance.__new__(a.Acceptance); obj.output = Path(directory)
            obj.current, obj.restored = {'id': 'current'}, {'id': 'restored'}
            running = {'current': True, 'restored': True}; removed = set(); probes = []
            def wrapper(project, operation):
                running[project['id']] = operation == 'up'
            def policy(project, operation):
                if operation in ('apply', 'remove'):
                    self.assertFalse(running[project['id']], 'policy mutation while writers run')
                if operation == 'remove': removed.add(project['id'])
                if operation == 'apply': removed.discard(project['id'])
                if operation == 'verify': self.assertNotIn(project['id'], removed)
            def probe(project):
                self.assertTrue(running[project['id']]); self.assertNotIn(project['id'], removed)
                probes.append(project['id']); return {'actual': True}
            obj.wrapper, obj.policy, obj.probes = wrapper, policy, probe
            obj.run = Mock(return_value=b'*filter\n:INPUT ACCEPT [0:0]\nCOMMIT\n')
            obj.unrelated, obj.check = Mock(), Mock()
            obj.two_project_policy()
            self.assertTrue(all(running.values())); self.assertFalse(removed)
            self.assertGreaterEqual(probes.count('current'), 3)
            self.assertGreaterEqual(probes.count('restored'), 3)


class CleanupControls(unittest.TestCase):
    def test_baseline_orphan_helper_requires_exact_image_source_command_mount_and_network(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.initial_resources = {'container': set(), 'network': set(), 'volume': set()}
        obj.projects = [{'config': {'CUTMY_PROJECT': 'cutmy_owned'}}]
        image = successful_report()['images']['old']; obj.report = {'images': {'old': image}}
        actual = {'Id': image['image_id'], 'Os': 'linux', 'Architecture': 'amd64', 'Config': {'Labels': {
            'org.opencontainers.image.revision': image['source_commit'], 'org.opencontainers.image.version': image['version'],
            'org.opencontainers.image.created': image['build_time'],
            'org.opencontainers.image.source': 'https://github.com/Kay0k1/cutmyvideo-core'}}}
        value = {'Id': 'f' * 64, 'Image': image['image_id'], 'Config': {'User': '1000:1000',
                 'Entrypoint': ['tar'], 'Cmd': ['-C', '/data', '--format=gnu', '-czf', '-', '.']},
                 'HostConfig': {'NetworkMode': 'none', 'ReadonlyRootfs': True, 'Privileged': False,
                                'CapDrop': ['ALL'], 'CapAdd': [], 'SecurityOpt': ['no-new-privileges:true']},
                 'Mounts': [{'Type': 'volume', 'Name': 'cutmy_owned_media', 'Destination': '/data', 'RW': False}]}
        obj.json = Mock(return_value=[actual])
        proof = obj.baseline_helper(value)
        self.assertEqual(proof['operation'], 'media-backup'); self.assertEqual(proof['project'], 'cutmy_owned')
        mutations = [lambda v: v.update(Image='sha256:' + '0' * 64),
                     lambda v: v['Mounts'][0].update(Name='foreign_media'),
                     lambda v: v['Mounts'][0].update(RW=True),
                     lambda v: v['Mounts'].append({'Type': 'bind', 'Destination': '/secret'}),
                     lambda v: v['Config'].update(Labels={'video.cutmy.project': 'cutmy_foreign'}),
                     lambda v: v['Config'].update(Cmd=['-C', '/data', '-czf', '-', '/etc']),
                     lambda v: v.update(NetworkSettings={'Networks': {'foreign-bridge': {}}}),
                     lambda v: v['HostConfig'].update(NetworkMode='bridge')]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                candidate = deepcopy(value); mutation(candidate)
                self.assertIsNone(obj.baseline_helper(candidate))
        actual['Config']['Labels']['org.opencontainers.image.revision'] = '0' * 40
        self.assertIsNone(obj.baseline_helper(value))

    def test_current_helper_labels_name_identity_and_source_are_all_bound(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.initial_resources = {'container': set(), 'network': set(), 'volume': set()}
        obj.projects = [{'config': {'CUTMY_PROJECT': 'cutmy_owned'}}]
        image = successful_report()['images']['current']; obj.report = {'images': {'current': image}}
        actual = {'Id': image['image_id'], 'Os': 'linux', 'Architecture': 'amd64', 'Config': {'Labels': {
            'org.opencontainers.image.revision': image['source_commit'], 'org.opencontainers.image.version': image['version'],
            'org.opencontainers.image.created': image['build_time'],
            'org.opencontainers.image.source': 'https://github.com/Kay0k1/cutmyvideo-core'}}}
        identity = '1' * 32
        labels = {'video.cutmy.profile': 'public-v1', 'video.cutmy.project': 'cutmy_owned',
                  'video.cutmy.helper': 'version', 'video.cutmy.helper.identity': identity,
                  'video.cutmy.helper.source': image['source_commit'], 'video.cutmy.helper.source-sha256': image['source_sha256']}
        value = {'Id': 'f' * 64, 'Image': image['image_id'], 'Name': '/cutmy-helper-cutmy_owned-version-' + identity,
                 'Config': {'User': '1000:1000', 'Entrypoint': ['/usr/local/bin/cutmy'], 'Cmd': ['version'], 'Labels': labels},
                 'HostConfig': {'NetworkMode': 'none', 'ReadonlyRootfs': True, 'Privileged': False,
                                'Memory': 256 << 20, 'MemorySwap': 256 << 20, 'NanoCpus': 10**9, 'PidsLimit': 32,
                                'CapDrop': ['ALL'], 'CapAdd': [], 'SecurityOpt': ['no-new-privileges:true']}, 'Mounts': []}
        obj.json = Mock(return_value=[actual])
        self.assertEqual(obj.baseline_helper(value)['explicit_helper_identity'], identity)
        for key, wrong in [('video.cutmy.project', 'cutmy_foreign'), ('video.cutmy.profile', 'foreign'),
                           ('video.cutmy.helper', 'tools'), ('video.cutmy.helper.identity', '2' * 32),
                           ('video.cutmy.helper.source', '0' * 40), ('video.cutmy.helper.source-sha256', '0' * 64)]:
            with self.subTest(key=key):
                candidate = deepcopy(value); candidate['Config']['Labels'][key] = wrong
                self.assertIsNone(obj.baseline_helper(candidate))
        for field, wrong in [('MemorySwap', 512 << 20), ('NanoCpus', 0), ('PidsLimit', 0)]:
            candidate = deepcopy(value); candidate['HostConfig'][field] = wrong
            self.assertIsNone(obj.baseline_helper(candidate))
        actual['Config']['Labels']['org.opencontainers.image.revision'] = image['source_commit']
        obj.initial_resources['container'].add(value['Id'])
        self.assertIsNone(obj.baseline_helper(value))

    def test_untracked_baseline_helper_is_discovered_but_unknown_new_container_is_preserved(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.projects, obj.extra_resources, obj.token = [], [], 'owned-fixture-token'
        obj.initial_resources = {'container': set(), 'network': set(), 'volume': set()}
        obj.recovered_helpers = {}
        known, foreign = 'a' * 64, 'b' * 64
        def run(command, **kwargs):
            if command == ['docker', 'ps', '-aq', '--no-trunc']: return (known + '\n' + foreign + '\n').encode()
            return b''
        obj.run = run
        obj.json = lambda command: [{'Id': command[-1]}]
        obj.baseline_helper = lambda value: {'id': known, 'operation': 'media-backup'} if value['Id'] == known else None
        self.assertEqual(obj.discovered_resources(), [('container', known)])
        self.assertEqual(obj.unknown_new_containers, [foreign])
        self.assertIn(known, obj.recovered_helpers)

    def test_partial_command_failure_before_tracking_is_discovered_and_removed(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.projects, obj.extra_resources, obj.token = [], [], 'owned-fixture-token'
        obj.recovered_helpers = {}
        obj.initial_resources = {'container': set(), 'network': set(), 'volume': set()}
        resource = 'a' * 64; calls = []
        def run(command, **kwargs):
            calls.append(command)
            if command[:3] == ['docker', 'ps', '-aq']: return (resource + '\n').encode()
            return b''
        obj.run = run
        self.assertEqual(obj.discovered_resources(), [('container', resource)])
        obj.json = Mock(return_value=[{'Id': resource, 'Config': {'Labels': {'video.cutmy.acceptance': obj.token}},
                                      'State': {'Running': True}}])
        obj.remove_resource('container', resource)
        self.assertIn(['docker', 'stop', '--time', '10', resource], calls)
        self.assertIn(['docker', 'rm', resource], calls)
        self.assertEqual(obj.extra_resources, [])

    def test_preexisting_or_foreign_resources_are_never_removed(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.projects, obj.token = [], 'owned-fixture-token'
        obj.recovered_helpers = {}
        resource = 'a' * 64
        obj.run = Mock(); obj.initial_resources = {'container': {resource}, 'network': set(), 'volume': set()}
        value = {'Id': resource, 'Config': {'Labels': {'video.cutmy.acceptance': obj.token}}, 'State': {'Running': True}}
        obj.json = Mock(return_value=[value])
        with self.assertRaisesRegex(RuntimeError, 'preexisting'): obj.remove_resource('container', resource)
        obj.initial_resources['container'].clear(); value['Config']['Labels'] = {'video.cutmy.acceptance': 'foreign'}
        with self.assertRaisesRegex(RuntimeError, 'unknown project'): obj.remove_resource('container', resource)
        obj.run.assert_not_called()

    def test_every_cleanup_stage_is_attempted_after_failures(self):
        called = []
        def failed(): called.append('failed'); raise RuntimeError('controlled failure')
        errors = a.cleanup_all([('first', failed), ('second', lambda: called.append('second'))])
        self.assertEqual(called, ['failed', 'second']); self.assertEqual(len(errors), 1)

    def test_log_failure_does_not_prevent_later_process_shutdown(self):
        with tempfile.TemporaryDirectory() as directory:
            obj = a.Acceptance.__new__(a.Acceptance)
            obj.work, obj.output, obj.secrets = Path(directory), Path(directory), []
            logs = [io.BytesIO(), io.BytesIO()]; first, second = Mock(), Mock()
            first.acceptance_stopped = second.acceptance_stopped = False
            first.poll.return_value = second.poll.return_value = None
            obj.processes = [('missing', first, logs[0]), ('second', second, logs[1])]
            (obj.work / 'second.log').write_text('safe diagnostic')
            stopped = []; obj.stop_process = stopped.append
            with self.assertRaisesRegex(RuntimeError, 'missing evidence'): obj.cleanup_processes()
            self.assertEqual(stopped, [first, second])
            self.assertTrue(all(log.closed for log in logs))

    def test_timed_out_command_terminates_then_kills_owned_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            obj = a.Acceptance.__new__(a.Acceptance)
            obj.work, obj.started, obj.secrets, obj.commands = Path(directory), time.monotonic(), [], []
            child = Mock(pid=81234)
            child.communicate.side_effect = subprocess.TimeoutExpired('fixture', 1)
            child.wait.side_effect = [subprocess.TimeoutExpired('fixture', 2), 0]
            with patch.object(a.subprocess, 'Popen', return_value=child) as start, patch.object(a.os, 'killpg') as kill:
                with self.assertRaisesRegex(RuntimeError, 'timed out'): obj.run(['fixture-cli'], timeout=1)
            self.assertTrue(start.call_args.kwargs['start_new_session'])
            self.assertEqual(kill.call_args_list[0].args, (81234, signal.SIGTERM))
            self.assertEqual(kill.call_args_list[1].args, (81234, signal.SIGKILL))
            self.assertTrue(obj.commands[0]['timeout'])

    def test_failed_ip_add_is_cleaned_after_address_exists_without_receipt(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.address_add_attempted, obj.network_address = True, '169.254.247.99'
        obj.json = Mock(return_value=[{'addr_info': [{'local': obj.network_address}]}]); obj.run = Mock()
        obj.cleanup_address()
        obj.run.assert_called_once_with(['ip', 'address', 'delete', '169.254.247.99/32', 'dev', 'lo'])
        self.assertFalse(obj.address_add_attempted)

    def test_initial_prerequisite_failure_still_saves_false_receipt_and_removes_private_work(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); args = SimpleNamespace(allow_disposable_host=True, revision='1' * 40,
                 pr_head='2' * 40, upgrade_from=a.UPGRADE_FROM, output=root / 'public', temp_directory=root)
            environment = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted',
                           **successful_report()['provider']}
            with patch.dict(os.environ, environment), patch.object(a.os, 'geteuid', return_value=0), \
                    patch.object(a.os, 'uname', return_value=SimpleNamespace(sysname='Linux', machine='x86_64')):
                obj = a.Acceptance(args)
            obj.initialize_host = Mock(side_effect=RuntimeError('controlled prerequisite failure'))
            obj.stop_services, obj.cleanup_policy, obj.cleanup_resources = Mock(), Mock(), Mock()
            obj.discovered_resources = Mock(return_value=[])
            self.assertFalse(obj.execute())
            self.assertFalse(obj.work.exists())
            report = json.loads((obj.output / 'acceptance-report.json').read_bytes())
            self.assertFalse(report['ok']); self.assertTrue(report['cleanup']['ok'])
            self.assertIn('prerequisite failure', report['errors'][0])

    def test_cleanup_error_discovery_failure_or_live_process_keeps_private_bind_tree(self):
        for mode in ('resource-error', 'discovery-unavailable', 'live-process', 'unknown-container'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                obj = a.Acceptance.__new__(a.Acceptance)
                obj.work = Path(directory) / 'cutmy-self-hosting-owned'; obj.work.mkdir()
                (obj.work / 'private-source').write_text('retained private bind source')
                obj.report = {'errors': []}; obj.secrets = []; obj.processes = []; obj.witnesses = []
                obj.recovered_helpers, obj.unknown_new_containers = {}, []
                for method in ('stop_services', 'cleanup_processes', 'cleanup_policy', 'cleanup_resources', 'cleanup_address', 'final_filter'):
                    setattr(obj, method, Mock())
                obj.discovered_resources = Mock(return_value=[])
                if mode == 'resource-error': obj.cleanup_resources.side_effect = RuntimeError('controlled remove failure')
                if mode == 'discovery-unavailable': obj.discovered_resources.side_effect = RuntimeError('controlled Docker failure')
                if mode == 'live-process':
                    child = Mock(); child.poll.return_value = None
                    obj.processes = [('controlled-child', child, io.BytesIO())]
                if mode == 'unknown-container': obj.unknown_new_containers = ['foreign-container']
                obj.close()
                self.assertTrue((obj.work / 'private-source').is_file())
                self.assertFalse(obj.report['cleanup']['ok'])
                self.assertTrue(obj.report['cleanup']['private_workspace_retained'])


class HostAndEvidenceControls(unittest.TestCase):
    def test_socket_ready_baseline_waits_for_actual_tcp_and_current_definition_is_required(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.report = successful_report()
        pg = {'Id': 'a' * 64, 'Config': {'Healthcheck': {'Test': a.SOCKET_HEALTHCHECK_A}},
              'State': {'Running': True, 'Health': {'Status': 'healthy'}}}
        obj.container = Mock(return_value=pg)
        obj.sql = Mock(side_effect=[(2, b'', b'connection refused'), (0, b'1\n', b'')])
        def wait(callback, timeout):
            self.assertFalse(callback(), 'socket-ready temporary init server must not satisfy TCP barrier')
            self.assertTrue(callback()); self.assertEqual(timeout, 120)
        obj.wait = wait
        proof = obj.pg_tcp_ready({}, baseline=True)
        self.assertEqual(len(proof['attempts']), 2)
        self.assertTrue(proof['baseline_socket_healthcheck_workaround']); self.assertFalse(proof['baseline_defect_fixed'])
        obj.sql.reset_mock()
        with self.assertRaisesRegex(RuntimeError, 'lacks exact TCP'): obj.pg_tcp_ready({})
        obj.sql.assert_not_called()

    def test_partial_helper_witness_uses_actual_profile_cleanup_and_requires_absence(self):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.current = {'state': Path('/owned/current'), 'config_file': Path('/owned/private.env')}
        obj.report = successful_report(); obj.check = Mock()
        identifier = 'f' * 64
        spec = {'spec': {'public': 'bound helper spec'}, 'command': ['docker', 'run', '--rm', '--name', 'exact-helper']}
        value = {'Id': identifier, 'State': {'Running': False, 'Pid': 0},
                 'HostConfig': {'Memory': 256 << 20, 'MemorySwap': 256 << 20, 'NanoCpus': 10**9, 'PidsLimit': 32}}
        obj.json = Mock(side_effect=[spec, [value]])
        obj.baseline_helper = Mock(return_value={'explicit_helper_identity': '1' * 32})
        obj.run = Mock(side_effect=[(identifier + '\n').encode(), b'', b''])
        obj.owned_helper_cleanup_witness()
        self.assertEqual(obj.run.call_args_list[0].args[0], ['docker', 'create', '--name', 'exact-helper'])
        self.assertIn('cleanup_helper', obj.run.call_args_list[1].args[0][3])
        self.assertEqual(obj.run.call_args_list[1].kwargs['input'], a.canonical(spec['spec']))
        obj.check.assert_called_once()
        obj.json.side_effect = [spec, [value]]
        obj.run.side_effect = [(identifier + '\n').encode(), b'', (identifier + '\n').encode()]
        obj.check.reset_mock()
        with self.assertRaisesRegex(RuntimeError, 'removal not confirmed'): obj.owned_helper_cleanup_witness()
        obj.check.assert_not_called()

    def test_operator_host_without_explicit_disposable_gate_cannot_create_files(self):
        args = SimpleNamespace(allow_disposable_host=False)
        with patch.dict(os.environ, {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted'}), \
                patch.object(a.tempfile, 'mkdtemp') as temp, patch.object(a.subprocess, 'Popen') as popen:
            with self.assertRaisesRegex(RuntimeError, 'disposable'): a.Acceptance(args)
        temp.assert_not_called(); popen.assert_not_called()

    def test_filter_comparison_preserves_semantics_and_rule_order(self):
        original = b'# Generated first\n*filter\n:INPUT ACCEPT [0:0]\n-A INPUT -i cm_first -j DROP\n-A INPUT -i cm_second -j DROP\nCOMMIT\n'
        counters = original.replace(b'Generated first', b'Generated later').replace(b'[0:0]', b'[500:600]')
        self.assertEqual(a.filter_rules(original), a.filter_rules(counters))
        reordered = original.replace(b'cm_first', b'cm_temporary').replace(b'cm_second', b'cm_first').replace(b'cm_temporary', b'cm_second')
        self.assertNotEqual(a.filter_rules(original), a.filter_rules(reordered))

    def test_private_credentials_cookies_and_keys_are_removed_from_public_diagnostics(self):
        raw = 'safe diagnostic\nCookie: cutmy_session=known-cookie\nSet-Cookie: future-cookie\npassword=known-password\n-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n'
        clean = a.redact(raw, ['known-cookie', 'known-password'])
        self.assertIn('safe diagnostic', clean)
        for secret in ('known-cookie', 'known-password', 'future-cookie', 'PRIVATE KEY-----', '\nsecret\n'):
            self.assertNotIn(secret, clean)


if __name__ == '__main__':
    unittest.main()
