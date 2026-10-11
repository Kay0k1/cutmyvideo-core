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
    identity, project, group = 'a' * 32, 'cutmy_test_old', 208574011
    receiver = {'topology': 'owned_routed_veth_outside_docker', 'identity': identity,
                'namespace': 'cutmy-forward-' + identity, 'namespace_identity': {'uid': 0, 'device': 4, 'inode': 5},
                'link_group': group,
                'address': '10.205.45.2', 'port': 35445, 'nonce_sha256': a.digest(b'routed-witness'),
                'host_link': {'name': 'cwf_' + identity[:10], 'ifindex': 101, 'alias': 'cutmy-acceptance:' + identity + ':host', 'group': str(group)},
                'peer_link': {'name': 'cwp_' + identity[:10], 'ifindex': 102, 'alias': 'cutmy-acceptance:' + identity + ':peer', 'group': str(group)},
                'host_route': {'dev': 'cwf_' + identity[:10]},
                'receiver_return_route': {'dev': 'cwp_' + identity[:10], 'gateway': '10.205.45.1'},
                'preexisting_collision_checked': True, 'application_networks_unchanged': True,
                'host_nonce_verified': True, 'listener_alive': True}
    report['forwarded_receiver'] = receiver
    report['cleanup']['owned_forward_namespace_removed'] = True
    report['controlled_probe_attempts'] = []
    for before in (True, False):
        for role in ('api', 'worker'):
            index = len(report['controlled_probe_attempts'])
            attempt = {'project': project, 'role': role, 'before_policy': before,
                       'source_identity': {k: report['images']['old'][k] for k in ('source_commit', 'source_sha256', 'image_id')},
                       'destinations': {'forwarded_private': {'connected': True, 'nonce_sha256': receiver['nonce_sha256']}
                                          if before else {'connected': False}},
                       'receiver_live_after_probe': True,
                       'packet_rule_snapshots': {phase: {'path': 'filter-probe-' + str(index) + '-' + role + '-' + phase + '.txt',
                                                         'sha256': 'f' * 64} for phase in ('before', 'after')}}
            if not before:
                attempt['private_forward_drop_counter'] = {'chain': 'CMF_' + a.digest(project.encode())[:10],
                    'destination_cidr': '10.0.0.0/8', 'before_packets': 8, 'after_packets': 9, 'increase': True}
            report['controlled_probe_attempts'].append(attempt)
    for label, baseline, container in (('baseline', True, 'a'), ('current', False, 'b'), ('restored', False, 'c')):
        report[label + '_tcp_readiness'] = {'container_id': container * 64,
            'source_commit': a.UPGRADE_FROM if baseline else current, 'tcp_sql_verified': True,
            'healthcheck': a.TCP_HEALTHCHECK, 'attempts': [{'exit': 0, 'tcp_sql_ready': True}]}
    report['groups']['identity']['checks']['application_sources_unchanged']['evidence'] = {
        'reference_commit': a.APPLICATION_REFERENCE, 'sha256': a.APPLICATION_FILES_SHA256,
        'schema_changing_upgrade': False}
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
        for mutation in (lambda r: r['current_tcp_readiness'].update(healthcheck=['CMD-SHELL', 'pg_isready -U cutmy_admin -d cutmy']),
                         lambda r: r.pop('restored_tcp_readiness'),
                         lambda r: r['baseline_tcp_readiness'].update(healthcheck=['CMD-SHELL', 'pg_isready -U cutmy_admin -d cutmy']),
                         lambda r: r['current_tcp_readiness']['attempts'][-1].update(tcp_sql_ready=False),
                         lambda r: r['groups']['container_constraints']['checks'].pop('owned_helper_partial_create_cleanup')):
            report = successful_report(); mutation(report)
            with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_failed_policy_snapshot_or_wrong_application_reference_cannot_be_pass(self):
        for mutation in (lambda r: r.update(failed_policy_snapshots=[{'status': 'retained'}]),
                         lambda r: r['groups']['identity']['checks']['application_sources_unchanged']['evidence'].update(reference_commit='9' * 40),
                         lambda r: r['groups']['identity']['checks']['application_sources_unchanged']['evidence'].update(sha256='9' * 64),
                         lambda r: r['groups']['identity']['checks']['application_sources_unchanged']['evidence'].update(schema_changing_upgrade=True)):
            with self.subTest(mutation=mutation):
                report = successful_report(); mutation(report)
                with self.assertRaises(RuntimeError): a.validate_report(report)

    def test_routed_receiver_counters_and_source_identities_are_mandatory(self):
        mutations = [lambda r: r.pop('forwarded_receiver'),
                     lambda r: r['forwarded_receiver'].update(host_nonce_verified=False),
                     lambda r: r['forwarded_receiver'].update(link_group=0),
                     lambda r: r['forwarded_receiver']['host_link'].update(group='0'),
                     lambda r: r['forwarded_receiver']['peer_link'].update(group=r['forwarded_receiver']['link_group']),
                     lambda r: r['forwarded_receiver']['host_link'].update(alias='foreign'),
                     lambda r: r['forwarded_receiver']['receiver_return_route'].update(gateway='10.205.44.1'),
                     lambda r: r['cleanup'].update(owned_forward_namespace_removed=False),
                     lambda r: r['controlled_probe_attempts'][0]['destinations']['forwarded_private'].update(nonce_sha256='0' * 64),
                     lambda r: r['controlled_probe_attempts'][2]['source_identity'].update(source_commit='0' * 40),
                     lambda r: r['controlled_probe_attempts'][2].pop('private_forward_drop_counter'),
                     lambda r: r['controlled_probe_attempts'][2]['private_forward_drop_counter'].update(chain='CMF_foreign'),
                     lambda r: r['controlled_probe_attempts'][2]['private_forward_drop_counter'].update(after_packets=8),
                     lambda r: r['controlled_probe_attempts'][2].update(receiver_live_after_probe=False),
                     lambda r: r['controlled_probe_attempts'][2]['packet_rule_snapshots']['before'].update(path='../foreign')]
        for mutation in mutations:
            with self.subTest(mutation=mutation):
                report = successful_report(); mutation(report)
                with self.assertRaises((RuntimeError, ValueError)): a.validate_report(report)

    def test_packet_snapshot_bytes_and_actual_rule_counters_bind_the_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); report = successful_report()
            for attempt in report['controlled_probe_attempts']:
                chain = 'CMF_' + a.digest(attempt['project'].encode())[:10]
                for phase, receipt in attempt['packet_rule_snapshots'].items():
                    packets = 8 if phase == 'before' else 9
                    data = ('[' + str(packets) + ':60] -A ' + chain + ' -d 10.0.0.0/8 -j DROP\n').encode()
                    (root / receipt['path']).write_bytes(data); receipt['sha256'] = a.digest(data)
            a.validate_report(report, root)
            receipt = report['controlled_probe_attempts'][2]['packet_rule_snapshots']['after']
            path = root / receipt['path']; original = path.read_bytes()
            path.write_bytes(original + b'# changed\n')
            with self.assertRaisesRegex(RuntimeError, 'bytes changed'): a.validate_report(report, root)
            changed = original.replace(b'[9:', b'[8:')
            path.write_bytes(changed); receipt['sha256'] = a.digest(changed)
            with self.assertRaisesRegex(RuntimeError, 'actual rule snapshot'): a.validate_report(report, root)
            changed = original.replace(b' -j DROP', b' -j RETURN')
            path.write_bytes(changed); receipt['sha256'] = a.digest(changed)
            with self.assertRaisesRegex(RuntimeError, 'counter missing or changed'): a.validate_report(report, root)


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


class RoutedReceiverControls(unittest.TestCase):
    @staticmethod
    def receiver():
        owner = SimpleNamespace(subnet_octet=205, initial_namespace_names=set(), initial_host_link_names={'lo'},
                                initial_host_routes=[], report={}, calls=[], present=False, pair=False, foreign=False,
                                aliases={'host': None, 'peer': None}, saved_reports=[])
        receiver = a.RoutedForwardReceiver(owner)
        identity = {'device': 4, 'inode': 900, 'uid': 0}
        receiver.namespace_identity = Mock(return_value=identity)
        def link(side):
            row = {'ifname': receiver.host_link if side == 'host' else receiver.peer_link,
                   'group': str(receiver.link_group), 'ifindex': 101 if side == 'host' else 102,
                   'linkinfo': {'info_kind': 'veth'}}
            if owner.aliases[side] is not None:
                row['ifalias'] = owner.aliases[side]
            row.update(getattr(owner, side + '_changes', {}))
            return row
        def run(command, **kwargs):
            owner.calls.append(command)
            if command == ['ip', 'netns', 'list']:
                return ((receiver.namespace + '\n').encode() if owner.present else b'')
            if command[:3] == ['ip', 'netns', 'pids']:
                return getattr(owner, 'namespace_processes', b'')
            if command[:3] == ['ip', 'netns', 'add']: owner.present = True
            if command[:3] == ['ip', 'link', 'add']: owner.pair = True
            if 'alias' in command and 'set' in command:
                side = 'peer' if '-n' in command else 'host'
                owner.aliases[side] = command[-1]
            if command[:3] == ['ip', 'link', 'delete']: owner.pair = False
            if command[:3] == ['ip', 'netns', 'delete']: owner.present = False
            if command[:3] == getattr(owner, 'fail_after_creation', None):
                raise RuntimeError('controlled CLI failure after daemon/kernel creation')
            if command == getattr(owner, 'fail_after_command', None):
                raise RuntimeError('controlled CLI timeout after alias mutation')
            return b''
        def queried(command, **kwargs):
            if 'route' in command:
                return [{'dev': receiver.peer_link, 'gateway': receiver.gateway}] if '-n' in command else [{'dev': receiver.host_link}]
            rows = [{'ifname': 'lo'}]
            if owner.pair:
                row = link('peer' if '-n' in command else 'host')
                if owner.foreign: row['ifalias'] = 'foreign'
                rows.append(row)
            return rows
        owner.run, owner.json = Mock(side_effect=run), Mock(side_effect=queried)
        owner.save = Mock(side_effect=lambda: owner.saved_reports.append(deepcopy(owner.report)))
        process = Mock(); process.poll.return_value = None
        owner.start_process = Mock(return_value=process)
        owner.socket_open = Mock(return_value=True); owner.wait = lambda check, timeout: a.require(check(), 'listener unavailable')
        owner.echo = Mock()
        return receiver, owner

    def test_collisions_refuse_before_any_creation(self):
        fixed = 'a' * 32
        for mode in ('namespace', 'link', 'route'):
            with self.subTest(mode=mode):
                owner = SimpleNamespace(subnet_octet=205, initial_namespace_names=set(), initial_host_link_names=set(),
                                        initial_host_routes=[], run=Mock(), json=Mock())
                if mode == 'namespace': owner.initial_namespace_names.add('cutmy-forward-' + fixed)
                if mode == 'link': owner.initial_host_link_names.add('cwf_' + fixed[:10])
                if mode == 'route': owner.initial_host_routes.append({'dst': '10.205.45.0/24'})
                with patch.object(a.secrets, 'token_hex', side_effect=[fixed, 'b' * 40]):
                    with self.assertRaisesRegex(RuntimeError, 'collision'): a.RoutedForwardReceiver(owner)
                owner.run.assert_not_called(); owner.json.assert_not_called()
        receiver, owner = self.receiver(); owner.present = True
        with self.assertRaisesRegex(RuntimeError, 'collision before creation'): receiver.create()
        self.assertFalse(any(c[:3] == ['ip', 'netns', 'add'] for c in owner.calls))

    def test_pair_is_atomically_owned_and_return_route_stays_inside_namespace(self):
        receiver, owner = self.receiver(); receiver.create()
        creation = next(c for c in owner.calls if c[:3] == ['ip', 'link', 'add'])
        self.assertEqual(creation, ['ip', 'link', 'add', 'name', receiver.host_link, 'group', str(receiver.link_group),
            'type', 'veth', 'peer', 'name', receiver.peer_link, 'group', str(receiver.link_group), 'netns', receiver.namespace])
        self.assertNotIn('alias', creation)
        self.assertGreater(receiver.link_group, 0); self.assertLess(receiver.link_group, 2**31)
        aliases = [c for c in owner.calls if 'alias' in c]
        self.assertEqual(aliases, [['ip', 'link', 'set', 'dev', receiver.host_link, 'alias', receiver.alias + ':host'],
            ['ip', '-n', receiver.namespace, 'link', 'set', 'dev', receiver.peer_link, 'alias', receiver.alias + ':peer']])
        inventories = owner.report['routed_witness_inventories']
        initial_pair = next(row for row in inventories if row['host_links'])
        self.assertNotIn('ifalias', initial_pair['host_links'][0])
        self.assertEqual(initial_pair['host_links'][0]['group'], str(receiver.link_group))
        self.assertEqual(initial_pair['namespace_links'][-1]['group'], str(receiver.link_group))
        self.assertTrue(receiver.aliases_complete)
        self.assertTrue(all('-N' in call.args[0] for call in owner.json.call_args_list if 'link' in call.args[0]))
        routes = [c for c in owner.calls if 'route' in c]
        self.assertEqual(routes, [['ip', '-n', receiver.namespace, 'route', 'add', 'default', 'via', receiver.gateway, 'dev', receiver.peer_link]])
        self.assertFalse(any(c[0].startswith(('docker', 'iptables', 'sysctl', 'modprobe')) for c in owner.calls))
        proof = owner.report['forwarded_receiver']
        self.assertTrue(proof['host_nonce_verified']); self.assertTrue(proof['application_networks_unchanged'])
        self.assertEqual(proof['namespace_identity'], receiver.namespace_record)
        self.assertEqual(proof['link_group'], receiver.link_group)
        self.assertEqual(proof['host_link']['group'], str(receiver.link_group))
        self.assertEqual(proof['peer_link']['group'], str(receiver.link_group))
        self.assertEqual(owner.start_process.call_args.args[1][:4], ['ip', 'netns', 'exec', receiver.namespace])
        receiver.process.poll.return_value = 0
        receiver.close(); self.assertTrue(receiver.removed); self.assertEqual(receiver.remaining(), [])

    def test_partial_kernel_creation_after_cli_failure_is_discovered_and_removed(self):
        for stage in (['ip', 'netns', 'add'], ['ip', 'link', 'add']):
            with self.subTest(stage=stage):
                receiver, owner = self.receiver(); owner.fail_after_creation = stage
                with self.assertRaisesRegex(RuntimeError, 'CLI failure'): receiver.create()
                self.assertTrue(receiver.remaining())
                owner.fail_after_creation = None
                receiver.close()
                self.assertEqual(receiver.remaining(), []); self.assertTrue(receiver.removed)
                self.assertFalse(any(c[0] != 'ip' for c in owner.calls))

    def test_partial_alias_timeout_is_removed_by_atomic_group_and_frozen_indices(self):
        for side in ('host', 'peer'):
            with self.subTest(side=side):
                receiver, owner = self.receiver()
                owner.fail_after_command = (['ip', 'link', 'set', 'dev', receiver.host_link] if side == 'host'
                                            else ['ip', '-n', receiver.namespace, 'link', 'set', 'dev', receiver.peer_link]) + [
                                            'alias', receiver.alias + ':' + side]
                with self.assertRaisesRegex(RuntimeError, 'timeout after alias'): receiver.create()
                self.assertFalse(receiver.aliases_complete)
                self.assertEqual(receiver.link_indices, [101, 102])
                self.assertEqual(owner.aliases['host'], receiver.alias + ':host')
                self.assertEqual(owner.aliases['peer'], receiver.alias + ':peer' if side == 'peer' else None)
                owner.fail_after_command = None
                receiver.close()
                self.assertTrue(receiver.removed); self.assertEqual(receiver.remaining(), [])

    def test_partial_pair_group_is_strict_numeric_string_and_raw_inventory_precedes_refusal(self):
        for side in ('host', 'peer'):
            for group in (None, '', '0', 'foreign', '01', 123, True):
                with self.subTest(side=side, group=group):
                    receiver, owner = self.receiver(); owner.fail_after_creation = ['ip', 'link', 'add']
                    with self.assertRaisesRegex(RuntimeError, 'CLI failure'): receiver.create()
                    setattr(owner, side + '_changes', {'group': group})
                    owner.fail_after_creation = None
                    before = len(owner.calls)
                    with self.assertRaisesRegex(RuntimeError, 'ownership differs'): receiver.close()
                    self.assertFalse(any('delete' in c for c in owner.calls[before:]))
                    inventory = owner.saved_reports[-1]['routed_witness_inventories'][-1]
                    rows = inventory['host_links'] if side == 'host' else inventory['namespace_links']
                    self.assertEqual(rows[-1]['group'], group)
                    self.assertFalse(receiver.removed)

    def test_foreign_replacement_before_alias_mutation_refuses_all_mutations(self):
        for field, value in (('group', 'foreign'), ('ifindex', 909), ('ifalias', 'foreign'),
                             ('master', 'foreign-bridge'), ('linkinfo', {'info_kind': 'dummy'})):
            with self.subTest(field=field):
                receiver, owner = self.receiver()
                original_run = owner.run.side_effect
                def changed(command, **kwargs):
                    result = original_run(command, **kwargs)
                    if command[:3] == ['ip', 'link', 'add']:
                        # First inventory binds the genuine pair before it is replaced.
                        receiver.guarded_inventory()
                        owner.host_changes = {field: value}
                    return result
                owner.run.side_effect = changed
                with self.assertRaises(RuntimeError): receiver.create()
                self.assertFalse(any('alias' in c for c in owner.calls))
                before = len(owner.calls)
                with self.assertRaises(RuntimeError): receiver.close()
                self.assertFalse(any('delete' in c for c in owner.calls[before:]))

    def test_foreign_peer_after_first_alias_refuses_second_alias_and_cleanup(self):
        for field, value in (('group', 'foreign'), ('ifindex', 909), ('ifalias', 'foreign'),
                             ('master', 'foreign-bridge')):
            with self.subTest(field=field):
                receiver, owner = self.receiver()
                original_run = owner.run.side_effect
                def changed(command, **kwargs):
                    result = original_run(command, **kwargs)
                    if command[:4] == ['ip', 'link', 'set', 'dev'] and 'alias' in command:
                        owner.peer_changes = {field: value}
                    return result
                owner.run.side_effect = changed
                with self.assertRaises(RuntimeError): receiver.create()
                self.assertEqual(owner.aliases['host'], receiver.alias + ':host')
                self.assertIsNone(owner.aliases['peer'])
                self.assertEqual(len([c for c in owner.calls if 'alias' in c]), 1)
                before = len(owner.calls)
                with self.assertRaises(RuntimeError): receiver.close()
                self.assertFalse(any('delete' in c for c in owner.calls[before:]))

    def test_partial_pair_namespace_replacement_is_saved_and_never_removed(self):
        receiver, owner = self.receiver(); owner.fail_after_creation = ['ip', 'link', 'add']
        with self.assertRaisesRegex(RuntimeError, 'CLI failure'): receiver.create()
        owner.fail_after_creation = None
        receiver.namespace_identity.return_value = {'device': 4, 'inode': 901, 'uid': 0}
        before = len(owner.calls)
        with self.assertRaisesRegex(RuntimeError, 'namespace was replaced'): receiver.close()
        self.assertFalse(any('delete' in c for c in owner.calls[before:]))
        self.assertEqual(owner.saved_reports[-1]['routed_witness_inventories'][-1]['namespace_identity']['inode'], 901)

    def test_complete_aliases_cannot_disappear_and_group_collisions_refuse_creation(self):
        for side in ('host', 'peer'):
            with self.subTest(side=side):
                receiver, owner = self.receiver(); receiver.create(); receiver.process.poll.return_value = 0
                owner.aliases[side] = None
                with self.assertRaisesRegex(RuntimeError, 'ownership differs'): receiver.close()
                self.assertFalse(any('delete' in c for c in owner.calls))
        receiver, owner = self.receiver()
        original = owner.json.side_effect
        owner.json.side_effect = lambda command, **kwargs: original(command, **kwargs) + [
            {'ifname': 'foreign', 'group': str(receiver.link_group)}]
        with self.assertRaisesRegex(RuntimeError, 'collision before creation'): receiver.create()
        self.assertFalse(any('add' in c for c in owner.calls))

    def test_foreign_alias_inode_ifindex_or_interface_refuses_removal(self):
        for mode in ('alias', 'inode', 'ifindex', 'interface', 'host-peer', 'group', 'master'):
            with self.subTest(mode=mode):
                receiver, owner = self.receiver(); receiver.create(); receiver.process.poll.return_value = 0
                original_json = owner.json.side_effect
                if mode == 'alias': owner.foreign = True
                if mode == 'inode': receiver.namespace_identity.return_value = {'device': 4, 'inode': 901, 'uid': 0}
                if mode == 'ifindex': receiver.link_indices = [991, 992]
                if mode == 'group': owner.peer_changes = {'group': '0'}
                if mode == 'master': owner.host_changes = {'master': 'foreign-bridge'}
                if mode in ('interface', 'host-peer'):
                    def altered(command, **kwargs):
                        rows = original_json(command, **kwargs)
                        if mode == 'interface' and '-n' in command and 'link' in command: rows.append({'ifname': 'foreign'})
                        if mode == 'host-peer' and '-n' not in command and 'link' in command: rows.append({'ifname': receiver.peer_link})
                        return rows
                    owner.json.side_effect = altered
                before = len(owner.calls)
                with self.assertRaises(RuntimeError): receiver.close()
                self.assertFalse(receiver.removed)
                self.assertFalse(any(c[:3] in (['ip', 'link', 'delete'], ['ip', 'netns', 'delete']) for c in owner.calls[before:]))
                self.assertTrue(receiver.remaining())

    def test_listener_liveness_discovery_failure_and_unconfirmed_delete_fail_closed(self):
        receiver, owner = self.receiver(); receiver.create()
        with self.assertRaisesRegex(RuntimeError, 'listener still alive'): receiver.close()
        self.assertFalse(any('delete' in c for c in owner.calls))
        receiver.process.poll.return_value = 0
        owner.namespace_processes = b'91234\n'
        with self.assertRaisesRegex(RuntimeError, 'namespace still has processes'): receiver.close()
        self.assertFalse(any('delete' in c for c in owner.calls))
        owner.namespace_processes = b''
        owner.run.side_effect = RuntimeError('inventory unavailable')
        with self.assertRaisesRegex(RuntimeError, 'inventory unavailable'): receiver.close()
        self.assertFalse(receiver.removed)
        receiver, owner = self.receiver(); receiver.create(); receiver.process.poll.return_value = 0
        original_run = owner.run.side_effect
        owner.run.side_effect = lambda c, **kw: b'' if 'delete' in c else original_run(c, **kw)
        with self.assertRaisesRegex(RuntimeError, 'deletion not confirmed'): receiver.close()
        self.assertTrue(receiver.remaining()); self.assertFalse(receiver.removed)

    def test_acceptance_stops_receiver_before_removal_and_retains_work_on_uncertainty(self):
        for mode in ('success', 'live-listener', 'foreign-alias', 'discovery-unavailable', 'delete-unconfirmed'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                receiver, owner = self.receiver(); receiver.create()
                obj = a.Acceptance.__new__(a.Acceptance)
                obj.work = Path(directory) / 'cutmy-self-hosting-owned'; obj.work.mkdir()
                (obj.work / 'private-bind-source').write_text('controlled private bytes')
                obj.report, obj.secrets, obj.processes, obj.witnesses = {'errors': []}, [], [], []
                obj.recovered_helpers, obj.unknown_new_containers, obj.forward_receiver = {}, [], receiver
                for method in ('stop_services', 'cleanup_policy', 'cleanup_resources', 'cleanup_address', 'final_filter'):
                    setattr(obj, method, Mock())
                obj.discovered_resources = Mock(return_value=[])
                def stopped():
                    if mode != 'live-listener': receiver.process.poll.return_value = 0
                obj.cleanup_processes = Mock(side_effect=stopped)
                if mode == 'foreign-alias': owner.foreign = True
                if mode == 'discovery-unavailable': owner.run.side_effect = RuntimeError('owned netns discovery unavailable')
                if mode == 'delete-unconfirmed':
                    original_run = owner.run.side_effect
                    owner.run.side_effect = lambda c, **kw: b'' if 'delete' in c else original_run(c, **kw)
                obj.close()
                self.assertEqual(obj.report['cleanup']['ok'], mode == 'success')
                self.assertEqual(obj.work.exists(), mode != 'success')
                self.assertEqual(obj.report['cleanup']['owned_forward_namespace_removed'], mode == 'success')
                if mode != 'success':
                    self.assertTrue(obj.report['cleanup']['remaining_resources'])
                    self.assertTrue((obj.work / 'private-bind-source').exists())


class HostAndEvidenceControls(unittest.TestCase):
    @staticmethod
    def policy_fixture(directory):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.output = Path(directory)
        obj.report, obj.commands, obj.http = {'groups': {}, 'errors': []}, [], []
        obj.secrets = ['super_private_password']
        project = {'state': Path('/owned/old-state'), 'config_file': Path('/owned/private.env'),
                   'config': {'CUTMY_PROJECT': 'cutmy_owned'}}
        return obj, project

    def test_failed_apply_and_remove_save_bounded_redacted_filter_before_exit_assertion(self):
        for action in ('apply', 'remove'):
            with self.subTest(action=action), tempfile.TemporaryDirectory() as directory:
                obj, project = self.policy_fixture(directory)
                raw = b'*filter\n:INPUT ACCEPT [0:0]\n# super_private_password\nCOMMIT\n'
                obj.run = Mock(side_effect=[(1, b'', b'fixed refusal'), (0, raw, b'')])
                original_require = a.require
                def require_after_snapshot(value, message):
                    if message == 'bounded command returned unexpected exit':
                        self.assertTrue((obj.output / 'acceptance-report.json').is_file())
                        self.assertEqual(obj.report['failed_policy_snapshots'][0]['status'], 'retained')
                    return original_require(value, message)
                with patch.object(a, 'require', side_effect=require_after_snapshot), \
                        self.assertRaisesRegex(RuntimeError, 'unexpected exit'):
                    obj.policy(project, action)
                snapshot = obj.report['failed_policy_snapshots'][0]
                saved = json.loads((obj.output / 'acceptance-report.json').read_bytes())
                self.assertEqual(saved['failed_policy_snapshots'], [snapshot])
                self.assertEqual(snapshot['policy_command_exit'], 1)
                self.assertEqual(snapshot['action'], action)
                self.assertEqual(snapshot['status'], 'retained')
                retained = (obj.output / snapshot['path']).read_bytes()
                self.assertNotIn(b'super_private_password', retained)
                self.assertIn(b'[REDACTED]', retained)
                self.assertEqual(snapshot['sha256'], a.digest(retained))
                self.assertEqual(snapshot['bytes'], len(retained))
                self.assertEqual(obj.run.call_args_list[0].kwargs, {'expected': None})
                self.assertEqual(obj.run.call_args_list[1].args[0], ['iptables-save', '-t', 'filter'])
                self.assertEqual(obj.run.call_args_list[1].kwargs, {'timeout': 10, 'expected': None})

    def test_timed_out_policy_retains_snapshot_without_replacing_original_exception(self):
        with tempfile.TemporaryDirectory() as directory:
            obj, project = self.policy_fixture(directory)
            original = RuntimeError('bounded command timed out')
            obj.run = Mock(side_effect=[original, (0, b'*filter\nCOMMIT\n', b'')])
            with self.assertRaises(RuntimeError) as raised:
                obj.policy(project, 'apply')
            self.assertIs(raised.exception, original)
            snapshot = obj.report['failed_policy_snapshots'][0]
            self.assertIsNone(snapshot['policy_command_exit'])
            self.assertEqual(snapshot['status'], 'retained')
            self.assertTrue((obj.output / snapshot['path']).is_file())

    def test_failed_filter_capture_keeps_original_failure_and_fixed_unavailable_reason(self):
        failures = [(2, b'', b'private filter error'), RuntimeError('super_private_password /private/path'),
                    (0, b'x' * (a.POLICY_FILTER_LIMIT + 1), b''), (0, 'invalid bytes output', b''),
                    (0, b'\xff' * (a.POLICY_FILTER_LIMIT // 2), b'')]
        reasons = ['filter_snapshot_command_failed', 'filter_snapshot_unavailable',
                   'filter_snapshot_size_exceeded', 'filter_snapshot_invalid_output', 'filter_snapshot_size_exceeded']
        for failure, reason in zip(failures, reasons):
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as directory:
                obj, project = self.policy_fixture(directory)
                original = RuntimeError('bounded command timed out')
                obj.run = Mock(side_effect=[original, failure])
                with self.assertRaises(RuntimeError) as raised:
                    obj.policy(project, 'remove')
                self.assertIs(raised.exception, original)
                snapshot = obj.report['failed_policy_snapshots'][0]
                self.assertEqual(snapshot['status'], 'unavailable')
                self.assertEqual(snapshot['reason'], reason)
                self.assertNotIn('sha256', snapshot)
                self.assertEqual(list(obj.output.glob('filter-failed-policy-*')), [])
                saved = (obj.output / 'acceptance-report.json').read_bytes()
                self.assertNotIn(b'super_private_password', saved)
                self.assertNotIn(b'/private/path', saved)

    def test_successful_policy_keeps_stdout_and_does_not_capture_failure_filter(self):
        with tempfile.TemporaryDirectory() as directory:
            obj, project = self.policy_fixture(directory)
            obj.run = Mock(return_value=(0, b'Owned policy complete\n', b''))
            self.assertEqual(obj.policy(project, 'apply'), b'Owned policy complete\n')
            self.assertNotIn('failed_policy_snapshots', obj.report)
            self.assertEqual(obj.run.call_count, 1)
            self.assertEqual(list(obj.output.iterdir()), [])

    def test_pre_policy_filter_snapshot_follows_bridge_setup_and_precedes_failing_probe(self):
        with tempfile.TemporaryDirectory() as tmp:
            obj = a.Acceptance.__new__(a.Acceptance)
            obj.output, obj.report, obj.commands, obj.http = Path(tmp), {'groups': {}, 'errors': []}, [], []
            obj.old = {'state': Path('/owned/old-state')}
            obj.wrapper, obj.compose = Mock(), Mock(return_value=['compose'])
            obj.pg_tcp_ready = Mock(return_value={'tcp_sql_verified': True})
            obj.container = Mock(return_value={'State': {'Running': False}, 'Id': 'a' * 64})
            events = []
            obj.setup_witnesses = Mock(side_effect=lambda: events.append('witness_bridges'))
            obj.pg_listener = Mock(side_effect=lambda project: events.append('pg_listener'))
            rules = b'*filter\n:INPUT ACCEPT [0:0]\n-A FORWARD -j DOCKER-USER\nCOMMIT\n'
            def run(command, **kwargs):
                if command == ['iptables-save', '-t', 'filter']:
                    events.append('filter_snapshot'); return rules
                return b''
            obj.run = Mock(side_effect=run)
            def probe(project, *, before):
                self.assertTrue(before)
                self.assertEqual((Path(tmp) / 'filter-pre-policy.txt').read_bytes(), rules)
                events.append('failing_probe')
                raise RuntimeError('required pre-policy witness failed')
            obj.probes = Mock(side_effect=probe)
            with self.assertRaisesRegex(RuntimeError, 'witness failed'):
                obj.group('egress', obj.initial_egress, complete=False)
            self.assertEqual(events, ['witness_bridges', 'pg_listener', 'filter_snapshot', 'failing_probe'])
            saved = json.loads((Path(tmp) / 'acceptance-report.json').read_text())
            self.assertEqual(saved['groups']['egress'], {'status': 'fail', 'checks': {}})
            self.assertEqual([call.args[0] for call in obj.run.call_args_list if 'iptables' in call.args[0][0]],
                             [['iptables-save', '-t', 'filter']])

    @staticmethod
    def probe_acceptance(output, *, before, change=None):
        obj = a.Acceptance.__new__(a.Acceptance)
        obj.output, obj.report, obj.commands, obj.http = Path(output), {'groups': {}, 'errors': []}, [], []
        obj.compose, obj.verify_witnesses = Mock(return_value=['compose']), Mock()
        obj.host_witness = SimpleNamespace(token=b'host-witness', port=35001)
        obj.metadata_witness = SimpleNamespace(token=b'metadata-witness', port=35002)
        obj.forward_receiver = SimpleNamespace(address='10.205.45.2', port=35445, token='routed-witness', verify=Mock())
        obj.network_address, obj.control_ip, obj.control_port, obj.control_token = '169.254.247.20', '10.205.44.2', 35003, 'private-witness'
        project = {'config': {'CUTMY_PROJECT': 'cutmy_test_old', 'CUTMY_DATABASE_PREFIX': '10.205.40',
                              'CUTMY_PROCESSING_PREFIX': '10.205.41'},
                   'pg_witness': {'address': '10.205.40.2', 'port': 35004, 'token': 'pg-witness'}}
        obj.probe_source_identity = Mock(return_value={k: successful_report()['images']['old'][k]
                                                      for k in ('source_commit', 'source_sha256', 'image_id')})
        chain = 'CMF_' + a.digest(project['config']['CUTMY_PROJECT'].encode())[:10]
        obj.run = Mock(side_effect=[('[' + str(8 + index // 2 + index % 2) + ':60] -A ' + chain
                                    + ' -d 10.0.0.0/8 -j DROP\n').encode() for index in range(8)])
        def returned(command, **kwargs):
            role = next(value for value in command if value in ('api', 'worker', 'migrate', 'maintenance'))
            nonces = {'db_gateway': a.digest(obj.host_witness.token), 'pg_wrong_port': a.digest(b'pg-witness')}
            if role in ('api', 'worker'):
                nonces.update(processing_gateway=a.digest(obj.host_witness.token),
                              controlled_metadata=a.digest(obj.metadata_witness.token),
                              forwarded_private=a.digest(obj.forward_receiver.token.encode()))
            value = {name: {'connected': True, 'nonce_sha256': nonce} if before else {'connected': False}
                     for name, nonce in nonces.items()}
            value['own_pg'] = {'connected': True}
            return change(role, value) if change is not None else value
        obj.json = Mock(side_effect=returned)
        return obj, project

    def test_each_pre_policy_probe_failure_is_saved_before_group_can_pass(self):
        for role in ('api', 'worker', 'migrate', 'maintenance'):
            names = ['own_pg', 'db_gateway', 'pg_wrong_port']
            if role in ('api', 'worker'):
                names += ['processing_gateway', 'controlled_metadata', 'forwarded_private']
            for name in names:
                for failure in ('disconnected', 'wrong_nonce') if name != 'own_pg' else ('disconnected',):
                    with self.subTest(role=role, destination=name, failure=failure), tempfile.TemporaryDirectory() as tmp:
                        def change(current, value):
                            if current == role:
                                value[name] = {'connected': False} if failure == 'disconnected' else {
                                    'connected': True, 'nonce_sha256': '0' * 64}
                            return value
                        obj, project = self.probe_acceptance(tmp, before=True, change=change)
                        with self.assertRaisesRegex(RuntimeError, 'role=' + role + ' destination=' + name) as error:
                            obj.group('egress', lambda: obj.check('controlled_listener_witnesses',
                                      {'before': obj.probes(project, before=True)}), complete=False)
                        saved = json.loads((Path(tmp) / 'acceptance-report.json').read_text())
                        self.assertEqual(saved['groups']['egress'], {'status': 'fail', 'checks': {}})
                        attempt = saved['controlled_probe_attempts'][-1]
                        self.assertEqual((attempt['project'], attempt['role'], attempt['before_policy']),
                                         ('cutmy_test_old', role, True))
                        self.assertEqual(attempt['destinations'][name]['connected'], failure != 'disconnected')
                        self.assertIn('connected=' + str(failure != 'disconnected').lower(), str(error.exception))
                        if name != 'own_pg': self.assertIn('nonce_match=false', str(error.exception))

    def test_malformed_probe_results_are_saved_and_refused(self):
        def variants(value):
            missing = deepcopy(value); del missing['db_gateway']
            extra = deepcopy(value); extra['unknown'] = {'connected': True}
            wrong_bool = deepcopy(value); wrong_bool['db_gateway']['connected'] = 'true'
            wrong_hash = deepcopy(value); wrong_hash['db_gateway']['nonce_sha256'] = 'invalid'
            extra_fields = deepcopy(value); extra_fields['db_gateway']['unexpected'] = True
            false_with_nonce = deepcopy(value); false_with_nonce['db_gateway']['connected'] = False
            return [None, missing, extra, wrong_bool, wrong_hash, extra_fields, false_with_nonce]
        for index in range(7):
            with self.subTest(variant=index), tempfile.TemporaryDirectory() as tmp:
                obj, project = self.probe_acceptance(tmp, before=True, change=lambda role, value: variants(value)[index])
                with self.assertRaisesRegex(RuntimeError, 'shape invalid role=api'):
                    obj.group('egress', lambda: obj.check('controlled_listener_witnesses',
                              {'before': obj.probes(project, before=True)}), complete=False)
                saved = json.loads((Path(tmp) / 'acceptance-report.json').read_text())
                self.assertEqual(saved['groups']['egress'], {'status': 'fail', 'checks': {}})
                self.assertEqual(len(saved['controlled_probe_attempts']), 1)

    def test_valid_probe_allow_and_deny_oracles_remain_strict(self):
        for before in (True, False):
            with self.subTest(before=before), tempfile.TemporaryDirectory() as tmp:
                obj, project = self.probe_acceptance(tmp, before=before)
                values = obj.probes(project, before=before)
                self.assertEqual(set(values), {'api', 'worker', 'migrate', 'maintenance'})
                self.assertEqual(len(json.loads((Path(tmp) / 'acceptance-report.json').read_text())['controlled_probe_attempts']), 4)
                for role, destinations in values.items():
                    self.assertTrue(destinations['own_pg']['connected'])
                    self.assertTrue(all(value['connected'] is before for name, value in destinations.items() if name != 'own_pg'))
        for role in ('api', 'worker', 'migrate', 'maintenance'):
            names = ['own_pg', 'db_gateway', 'pg_wrong_port']
            if role in ('api', 'worker'): names += ['processing_gateway', 'controlled_metadata', 'forwarded_private']
            for name in names:
                with self.subTest(role=role, forbidden=name), tempfile.TemporaryDirectory() as tmp:
                    def change(current, value):
                        if current == role:
                            value[name] = {'connected': False} if name == 'own_pg' else {'connected': True, 'nonce_sha256': '0' * 64}
                        return value
                    obj, project = self.probe_acceptance(tmp, before=False, change=change)
                    with self.assertRaisesRegex(RuntimeError, 'role=' + role + ' destination=' + name):
                        obj.probes(project)
                    self.assertEqual(json.loads((Path(tmp) / 'acceptance-report.json').read_text())['controlled_probe_attempts'][-1]['role'], role)

    def test_missing_changed_or_nonincreasing_private_drop_rule_stays_failed_and_saved(self):
        for mode in ('missing', 'changed', 'duplicate', 'no-increase'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                obj, project = self.probe_acceptance(tmp, before=False)
                chain = 'CMF_' + a.digest(project['config']['CUTMY_PROJECT'].encode())[:10]
                before = ('[8:60] -A ' + chain + ' -d 10.0.0.0/8 -j DROP\n').encode()
                after = before.replace(b'[8:', b'[9:')
                if mode == 'missing': after = b'*filter\nCOMMIT\n'
                if mode == 'changed': after = after.replace(b' -j DROP', b' -j RETURN')
                if mode == 'duplicate': after += after
                if mode == 'no-increase': after = before
                obj.run.side_effect = [before, after]
                with self.assertRaisesRegex(RuntimeError, 'counter'):
                    obj.group('egress', lambda: obj.check('both_bridges_and_operator_denials', obj.probes(project)), complete=False)
                saved = json.loads((Path(tmp) / 'acceptance-report.json').read_bytes())
                self.assertEqual(saved['groups']['egress'], {'status': 'fail', 'checks': {}})
                attempt = saved['controlled_probe_attempts'][0]
                self.assertTrue(attempt['receiver_live_after_probe'])
                self.assertFalse(attempt['destinations']['forwarded_private']['connected'])
                for phase, data in (('before', before), ('after', after)):
                    receipt = attempt['packet_rule_snapshots'][phase]
                    self.assertEqual((Path(tmp) / receipt['path']).read_bytes(), data)
                    self.assertEqual(receipt['sha256'], a.digest(data))

    def test_probe_identity_reads_exact_sealed_source_and_image(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory); source = a.canonical({'commit': a.UPGRADE_FROM})
            (state / 'SOURCE.json').write_bytes(source)
            (state / 'image.json').write_bytes(a.canonical({'image_id': 'sha256:' + 'a' * 64}))
            obj = a.Acceptance.__new__(a.Acceptance)
            identity = {'source_commit': a.UPGRADE_FROM, 'source_sha256': a.digest(source), 'image_id': 'sha256:' + 'a' * 64}
            obj.report = {'images': {'old': identity}}
            self.assertEqual(obj.probe_source_identity({'state': state}), identity)
            (state / 'SOURCE.json').write_bytes(a.canonical({'commit': '0' * 40}))
            with self.assertRaisesRegex(RuntimeError, 'SOURCE/image identity'): obj.probe_source_identity({'state': state})

    def test_host_none_null_ipam_and_bridge_ranges_preserve_collision_detection(self):
        networks = [
            {'Name': 'host', 'Driver': 'host', 'IPAM': {'Driver': 'default', 'Options': None, 'Config': None}},
            {'Name': 'none', 'Driver': 'null', 'IPAM': {'Driver': 'default', 'Options': None, 'Config': None}},
            {'Name': 'no-declared-config', 'IPAM': {}},
            {'Name': 'bridge', 'Driver': 'bridge', 'IPAM': {'Config': [
                {'Subnet': '10.205.0.0/16', 'Gateway': '10.205.0.1'},
                {'Subnet': '172.17.0.0/16', 'Gateway': '172.17.0.1'},
                {'Subnet': 'fd00:1234::/64', 'Gateway': 'fd00:1234::1'}]}},
        ]
        ranges = a.network_subnets(networks)
        self.assertEqual([str(value) for value in ranges], ['10.205.0.0/16', '172.17.0.0/16', 'fd00:1234::/64'])
        self.assertTrue(any(a.ipaddress.ip_network('10.205.40.0/24').overlaps(value)
                            for value in ranges if value.version == 4), 'occupied project range must still collide')
        self.assertFalse(any(a.ipaddress.ip_network('10.206.40.0/24').overlaps(value)
                             for value in ranges if value.version == 4), 'unused project range must remain eligible')
        self.assertTrue(any(a.ipaddress.ip_network('fd00:1234::/80').overlaps(value)
                            for value in ranges if value.version == 6), 'declared IPv6 range must not disappear')

    def test_non_null_malformed_ipam_or_declared_subnet_is_refused(self):
        for config in (False, 0, '', '10.205.0.0/16', {}, [None], [False], [{}],
                       [{'Subnet': None}], [{'Subnet': 123}], [{'Subnet': ''}],
                       [{'Subnet': 'not-a-subnet'}], [{'Subnet': '10.205.1.2/16'}]):
            with self.subTest(config=config), self.assertRaises((RuntimeError, ValueError)):
                a.network_subnets([{'IPAM': {'Config': config}}])
        with self.assertRaisesRegex(RuntimeError, 'IPAM is not an object'):
            a.network_subnets([{'IPAM': None}])
        with self.assertRaisesRegex(RuntimeError, 'inventory entry is not an object'):
            a.network_subnets([None])
        with self.assertRaisesRegex(RuntimeError, 'inventory is not a list'):
            a.network_subnets({})

    def test_all_images_require_exact_tcp_healthcheck_and_real_sql_barrier(self):
        for baseline in (True, False):
            with self.subTest(baseline=baseline):
                obj = a.Acceptance.__new__(a.Acceptance)
                obj.report = successful_report()
                pg = {'Id': 'a' * 64, 'Config': {'Healthcheck': {'Test': a.TCP_HEALTHCHECK}},
                      'State': {'Running': True, 'Health': {'Status': 'healthy'}}}
                obj.container = Mock(return_value=pg)
                obj.sql = Mock(side_effect=[(2, b'', b'connection refused'), (0, b'1\n', b'')])
                def wait(callback, timeout):
                    self.assertFalse(callback(), 'health must not replace the real TCP SQL barrier')
                    self.assertTrue(callback()); self.assertEqual(timeout, 120)
                obj.wait = wait
                proof = obj.pg_tcp_ready({}, baseline=baseline)
                self.assertEqual(len(proof['attempts']), 2)
                self.assertTrue(proof['tcp_sql_verified'])
                self.assertEqual(proof['healthcheck'], a.TCP_HEALTHCHECK)
                self.assertEqual(proof['source_commit'], a.UPGRADE_FROM if baseline else '1' * 40)
                self.assertNotIn('baseline_socket_healthcheck_workaround', proof)
                self.assertNotIn('baseline_defect_fixed', proof)
                pg['Config']['Healthcheck']['Test'] = ['CMD-SHELL', 'pg_isready -U cutmy_admin -d cutmy']
                obj.sql.reset_mock()
                with self.assertRaisesRegex(RuntimeError, 'lacks exact TCP'):
                    obj.pg_tcp_ready({}, baseline=baseline)
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
