#!/usr/bin/env python3
"""Offline safety controls only; no actual Docker/systemd/backend launched."""
import importlib.util
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('backend', Path(__file__).with_name('backend.py'))
b = importlib.util.module_from_spec(spec)
spec.loader.exec_module(b)


class OfflineControls(unittest.TestCase):
    def test_flat_units_do_not_create_production_ancestor(self):
        n = b.names('a' * 32)
        self.assertEqual(n['slice'], 'cutmyfirstdraft' + 'a' * 32 + '.slice')
        self.assertEqual(n['client'], 'cutmyfirstdraftclient' + 'a' * 32 + '.service')
        self.assertNotIn('-', n['slice'])
        self.assertNotIn('-', n['client'])

    def test_fixed_env_preserves_ordinary_admission(self):
        self.assertEqual(b.APP_ENV['MUTATIONS_PER_MINUTE'], '20')
        self.assertEqual(b.APP_ENV['WORKER_CONCURRENCY'], '1')
        self.assertEqual(b.APP_ENV['WORKER_PREPARATION_CONCURRENCY'], '0')
        self.assertEqual(b.APP_ENV['FFMPEG_THREADS'], '2')
        self.assertEqual(b.APP_ENV['FFMPEG_PATH'], '/usr/bin/ffmpeg')
        self.assertNotIn('POSTGRES_PASSWORD', b.safe_environment())
        self.assertNotIn('DATABASE_URL', b.safe_environment())

    def test_task_launch_overrides_inherited_compose_labels(self):
        args = b.label_args('a' * 32, 'worker')
        self.assertIn('com.docker.compose.project=cutmy-firstdraft-bench', args)
        self.assertIn('com.docker.compose.service=worker', args)

    def test_inherited_production_label_requires_full_retained_task_binding(self):
        identity = 'e' * 32
        expected = b.names(identity)
        sample = b.TASK_ROOT / 'samples' / 'pilot-baseline-fresh-01'
        data = {'kind': 'owned_first_draft_backend', 'identity': identity, 'names': expected,
                'sample_dir': str(sample), 'container_ids': {'worker': 'owned-id'}, 'core_image_id': 'owned-image'}
        value = {'id': 'owned-id', 'name': '/' + expected['containers']['worker'], 'image': 'owned-image',
                 'labels': {**b.labels(identity, 'worker'), 'com.docker.compose.project': 'cutmy',
                            'com.docker.compose.service': 'api'},
                 'cgroup_parent': expected['slice'], 'memory': 768 << 20, 'nano_cpus': 1000000000,
                 'pids_limit': 128, 'networks': {expected['network']: {}},
                 'mounts': [{'Type': 'bind', 'Source': str(sample / 'media'), 'Destination': '/data'}]}
        self.assertTrue(b.task_matches_retained_state(value, data))
        for key,replacement in [('id','other-id'),('image','other-image'),('name','/cutmy-api-1'),
                                ('cgroup_parent','system.slice'),('memory',1024),
                                ('networks',{'production': {}}),
                                ('mounts',[{'Type':'bind','Source':'/opt/cutmy/shared/media','Destination':'/data'}])]:
            changed = {**value, key: replacement}
            self.assertFalse(b.task_matches_retained_state(changed, data), key)

    def test_retained_task_is_excluded_but_mislabeled_non_task_is_not(self):
        identity = 'f' * 32
        expected = b.names(identity)
        sample = b.TASK_ROOT / 'samples' / 'pilot-baseline-fresh-01'
        task = {'id': 'owned-id', 'name': '/' + expected['containers']['api'], 'image': 'owned-image',
                'labels': {**b.labels(identity, 'api'), 'com.docker.compose.project': 'cutmy',
                           'com.docker.compose.service': 'api'},
                'cgroup_parent': expected['slice'], 'memory': 512 << 20, 'nano_cpus': 500000000,
                'pids_limit': 128, 'networks': {expected['network']: {}},
                'mounts': [{'Type': 'bind', 'Source': str(sample / 'media'), 'Destination': '/data'}]}
        state = {'kind': 'owned_first_draft_backend', 'identity': identity, 'names': expected,
                 'sample_dir': str(sample), 'container_ids': {'api': 'owned-id'}, 'core_image_id': 'owned-image'}
        def row(identifier):
            if identifier == 'task':return task
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'}, 'mounts': []}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'load', return_value=state), \
             patch.object(b, 'docker', return_value=subprocess.CompletedProcess([],0,' '.join(roles+['task']).encode(),b'')), \
             patch.object(b, 'container', side_effect=row):
            self.assertEqual(set(b.production_snapshot()['roles']), b.PRODUCTION_ROLES)
        with patch.object(b, 'load', return_value={**state,'core_image_id':'wrong-image'}):
            self.assertFalse(b.retained_task_container(task))

    def test_no_actual_peak_counter_is_a_refusal(self):
        with tempfile.TemporaryDirectory(prefix='offline-cg-') as directory:
            p = Path(directory)
            (p / 'memory.current').write_text('123')
            with self.assertRaisesRegex(b.Refused, '^actual_kernel_peak_required$'):
                b.counters(p)
            (p / 'memory.peak').write_text('456')
            self.assertEqual(b.counters(p)['memory_peak_bytes'], 456)

    def test_container_wrong_owner_stops_before_mutation(self):
        identity = 'b' * 32
        data = {'identity': identity, 'names': b.names(identity)}
        value = {'labels': b.labels('c' * 32, 'worker')}
        with patch.object(b, 'container', return_value=value), patch.object(b, 'docker') as docker:
            with self.assertRaisesRegex(b.Refused, '^container_ownership_mismatch$'):
                b.owned_container(data, 'worker')
            docker.assert_not_called()

    def test_network_rejects_an_unowned_attached_member(self):
        identity = 'd' * 32
        data = {'identity': identity, 'names': b.names(identity)}
        value = {'id': 'x', 'labels': b.labels(identity, 'network'), 'internal': False,
                 'containers': {'other': {'Name': 'cutmy-api-1'}}}
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, json.dumps(value).encode(), b'')):
            with self.assertRaisesRegex(b.Refused, '^unowned_network_member$'):
                b.owned_network(data)

    def test_missing_published_port_is_a_fixed_refusal(self):
        with self.assertRaisesRegex(b.Refused, '^api_port_not_published$'):
            b.published_loopback_port({'8080/tcp': None}, 56845)
        with self.assertRaisesRegex(b.Refused, '^loopback_only_api_required$'):
            b.published_loopback_port({'8080/tcp': [{'HostIp': '0.0.0.0', 'HostPort': '56845'}]}, 56845)
        with self.assertRaisesRegex(b.Refused, '^api_port_changed$'):
            b.published_loopback_port({'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '56846'}]}, 56845)
        self.assertEqual(b.published_loopback_port({'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '56845'}]}, 56845), '56845')

    def test_production_requires_five_distinct_healthy_roles(self):
        def row(identifier):
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'}, 'mounts': []}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, ' '.join(roles).encode(), b'')), \
             patch.object(b, 'container', side_effect=row):
            self.assertEqual(set(b.production_snapshot()['roles']), b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, b'api worker', b'')), \
             patch.object(b, 'container', side_effect=row):
            with self.assertRaisesRegex(b.Refused, '^production_not_five_healthy$'):
                b.production_snapshot()

    def test_changed_production_identity_is_rejected(self):
        with patch.object(b, 'production_snapshot', return_value={'roles': {'api': {'id': 'new'}}}):
            with self.assertRaisesRegex(b.Refused, '^production_baseline_changed$'):
                b.assert_production({'roles': {'api': {'id': 'old'}}})

    def test_production_mount_identity_survives_private_json_roundtrip(self):
        def row(identifier):
            return {'id': identifier, 'image': identifier, 'labels': {'com.docker.compose.service': identifier},
                    'state': {'Running': True, 'OOMKilled': False, 'Health': {'Status': 'healthy'},
                              'StartedAt': 'unchanged'},
                    'mounts': [{'Type': 'volume', 'Name': 'untouched', 'Source': '/never-modified', 'Destination': '/data'}]}
        roles = sorted(b.PRODUCTION_ROLES)
        with patch.object(b, 'docker', return_value=subprocess.CompletedProcess([], 0, ' '.join(roles).encode(), b'')), \
             patch.object(b, 'container', side_effect=row):
            before = b.production_snapshot()
            stored = json.loads(json.dumps(before))
            self.assertEqual(b.assert_production(stored)['roles'], stored['roles'])

    def test_raw_subprocess_error_never_becomes_public_exception(self):
        secret = b'postgres://user:never-print-this@host/db'
        with patch.object(b.subprocess, 'run', return_value=subprocess.CompletedProcess([], 1, b'', secret)):
            with self.assertRaisesRegex(b.Refused, '^example_failed$') as raised:
                b.run(['false'], 'example')
            self.assertNotIn('never-print-this', str(raised.exception))


if __name__ == '__main__':
    unittest.main()
