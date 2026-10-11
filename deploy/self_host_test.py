"""Prepared profile controls: no Docker, firewall, media tools or database calls."""
from copy import deepcopy
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import yaml
import public_common as c

ROOT = Path(__file__).resolve().parent


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, ROOT / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


e = module('public_egress', 'egress-policy.py')
s = module('public_self_host', 'self-host.py')


def configuration():
    return {'CUTMY_PROJECT': 'cutmy_fixture', 'CUTMY_DOMAIN': 'video.example.org', 'CUTMY_HTTP_PORT': '8080',
            'CUTMY_DATABASE_PREFIX': '172.30.88', 'CUTMY_PROCESSING_PREFIX': '172.30.89',
            'CUTMY_ADMIN_PASSWORD': 'a' * 48, 'CUTMY_MIGRATION_PASSWORD': 'b' * 48, 'CUTMY_APP_PASSWORD': 'c' * 48}


def rules(config):
    desired = e.policy(config)
    lines = ['*filter', ':INPUT ACCEPT [0:0]', ':FORWARD DROP [0:0]', ':DOCKER-USER - [0:0]', ':DOCKER - [0:0]']
    lines += ['-A FORWARD -j DOCKER-USER']
    lines += [':' + chain + ' - [0:0]' for chain in desired['chains']]
    lines += ['-A DOCKER -o ' + bridge + ' -j RETURN'
              for bridge in (desired['bridge'], desired['database_bridge'])]
    for chain, rows in {**desired['chains'], **desired['hooks']}.items():
        lines += ['-A ' + chain + ' ' + shlex.join(row) for row in rows]
    return ('\n'.join(lines + ['COMMIT', ''])).encode()


def networks(config):
    rows, containers = [], {}
    for kind, prefix, internal in (('database', config['CUTMY_DATABASE_PREFIX'], True),
                                    ('processing', config['CUTMY_PROCESSING_PREFIX'], False)):
        row = {'Id': kind + '_network_id', 'Name': config['CUTMY_PROJECT'] + '_' + kind, 'Driver': 'bridge', 'Scope': 'local',
               'Internal': internal, 'EnableIPv6': False,
               'IPAM': {'Config': [{'Subnet': prefix + '.0/24', 'Gateway': prefix + '.1'}]},
               'Options': {'com.docker.network.bridge.name': c.bridge_name(config) + ('_d' if internal else '')},
               'Labels': {'video.cutmy.profile': c.PROFILE, 'video.cutmy.project': config['CUTMY_PROJECT'],
                          'com.docker.compose.project': config['CUTMY_PROJECT']}, 'Containers': {}}
        roles = {'postgres': 2, 'api': 10, 'worker': 11, 'migrate': 12, 'maintenance': 13} if internal else {'api': 10, 'worker': 11}
        for role, last in roles.items():
            row['Containers'][role] = {'IPv4Address': prefix + '.' + str(last) + '/24', 'IPv6Address': ''}
            containers[role] = {'Config': {'Labels': {'com.docker.compose.project': config['CUTMY_PROJECT'],
                                                     'com.docker.compose.service': role}},
                                'HostConfig': {'NetworkMode': config['CUTMY_PROJECT'] + '_database',
                                               'Sysctls': {'net.ipv6.conf.all.disable_ipv6': '1',
                                                           'net.ipv6.conf.default.disable_ipv6': '1'}},
                                'NetworkSettings': {'Networks': {config['CUTMY_PROJECT'] + '_database': {}}},
                                'State': {'Running': role == 'postgres'}}
        rows.append(row)
    for role in ('api', 'worker'):
        containers[role]['NetworkSettings']['Networks'][config['CUTMY_PROJECT'] + '_processing'] = {}
    return rows, containers


def archive(path, entries, format=tarfile.GNU_FORMAT):
    with tarfile.open(path, mode='w:gz', format=format) as output:
        for name, kind, data in entries:
            member = tarfile.TarInfo(name)
            member.mode = 0o600
            member.type = kind
            if kind == tarfile.REGTYPE:
                member.size = len(data)
                output.addfile(member, io.BytesIO(data))
            elif kind == tarfile.LNKTYPE:
                member.linkname = data
                output.addfile(member)
            else:
                output.addfile(member)


class PublicControls(unittest.TestCase):
    def test_real_python_entrypoint_imports_preserve_sealed_snapshot_without_env_override(self):
        with tempfile.TemporaryDirectory() as directory:
            snapshot = Path(directory) / 'source'
            (snapshot / 'deploy').mkdir(parents=True)
            (snapshot / 'scripts').mkdir()
            for filename in ('public_common.py', 'self-host.py', 'egress-policy.py'):
                shutil.copyfile(ROOT / filename, snapshot / 'deploy' / filename)
            shutil.copyfile(ROOT.parent / 'scripts/release_manifest.py', snapshot / 'scripts/release_manifest.py')
            def inventory():
                return {path.relative_to(snapshot).as_posix(): path.read_bytes()
                        for path in snapshot.rglob('*') if path.is_file()}
            before = inventory()
            # Do not let the outer test runner's setting conceal entrypoint
            # behavior. --help imports real modules but invokes no host tools.
            env = {key: value for key, value in os.environ.items()
                   if key not in ('PYTHONDONTWRITEBYTECODE', 'PYTHONPYCACHEPREFIX', 'PYTHONPATH')}
            for filename in ('self-host.py', 'egress-policy.py'):
                result = subprocess.run([sys.executable, snapshot / 'deploy' / filename, '--help'],
                                        env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr.decode())
                self.assertEqual(inventory(), before)
                self.assertFalse(any(path.name == '__pycache__' for path in snapshot.rglob('*')))

    def test_configuration_rejects_shell_interpolation_uri_delimiters_and_duplicate_roles(self):
        original = configuration()
        self.assertEqual(c.parse_config(s.config_text(original)), original)
        for key, value in (('CUTMY_APP_PASSWORD', 'x' * 48 + '$OTHER'),
                           ('CUTMY_APP_PASSWORD', 'x' * 48 + '@postgres'),
                           ('CUTMY_DOMAIN', 'https://video.example.org/path'),
                           ('CUTMY_DOMAIN', 'video.example.org:8080'),
                           ('CUTMY_PROJECT', 'another-project'),
                           ('CUTMY_PROCESSING_PREFIX', '127.0.0'),
                           ('CUTMY_PROCESSING_PREFIX', '172.30.88'),
                           ('CUTMY_APP_PASSWORD', original['CUTMY_ADMIN_PASSWORD'])):
            values = {**original, key: value}
            with self.subTest(key=key, value=value[:16]), self.assertRaises(c.Refused):
                c.parse_config(s.config_text(values))
        with self.assertRaises(c.Refused):
            c.parse_config(s.config_text(original) + b'CUTMY_DOMAIN=evil.example.org\n')

    def test_shell_environment_cannot_override_compose_or_select_remote_docker(self):
        with patch.dict(os.environ, {'CUTMY_DOMAIN': 'evil.example.org', 'CUTMY_APP_PASSWORD': 'leak',
                                     'DOCKER_HOST': 'ssh://production', 'DOCKER_CONTEXT': 'production',
                                     'COMPOSE_FILE': '/foreign.yml', 'PGPASSWORD': 'leak', 'PATH': '/tools'}, clear=True):
            self.assertEqual(c.command_environment(), {'PATH': '/tools'})

    def test_every_lifecycle_command_uses_same_two_https_files(self):
        command = c.compose(Path('/private/immutable-source'), '/private/compose.env', configuration())
        self.assertEqual(command.count('-f'), 2)
        self.assertIn('/private/immutable-source/deploy/compose.public.yml', command)
        self.assertIn('/private/immutable-source/deploy/compose.https.example.yml', command)
        self.assertIn('cutmy_fixture', command)

    def test_policy_missing_or_changed_prevents_any_application_start(self):
        with patch.object(s, 'validate_volumes'), patch.object(s, 'run', side_effect=c.Refused('missing_policy')) as run:
            with self.assertRaises(c.Refused):
                s.checked_up(['compose'], configuration(), Path('/source'), Path('/private.env'))
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][-1], 'verify')

    def test_policy_poststart_failure_stops_both_writers(self):
        with patch.object(s, 'validate_volumes'), patch.object(s, 'run', side_effect=[b'', b'', c.Refused('changed_policy'), b'']) as run:
            with self.assertRaises(c.Refused):
                s.checked_up(['compose'], configuration(), Path('/source'), Path('/private.env'))
        self.assertEqual(run.call_args.args[0], ['compose', 'stop', 'api', 'worker'])

    def test_policy_missing_prevents_schema_owner_container_start(self):
        with patch.object(s, 'validate_volumes'), patch.object(s, 'no_writers'), patch.object(s, 'run', side_effect=c.Refused('missing_policy')) as run:
            with self.assertRaises(c.Refused):
                s.initialize(['compose'], configuration(), Path('/source'), Path('/private.env'))
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0][-1], 'verify')

    def test_foreign_volume_collision_refuses_before_any_compose_mutation(self):
        config = configuration(); name = config['CUTMY_PROJECT'] + '_media'
        row = {'Name': name, 'Driver': 'local', 'Options': {}, 'Labels': {
            'video.cutmy.profile': c.PROFILE, 'video.cutmy.project': config['CUTMY_PROJECT'],
            'com.docker.compose.project': config['CUTMY_PROJECT'], 'com.docker.compose.volume': 'media'}}
        for mutation in ('profile', 'project', 'component', 'options', 'driver'):
            value = deepcopy(row)
            if mutation == 'profile': value['Labels']['video.cutmy.profile'] = 'foreign'
            elif mutation == 'project': value['Labels']['com.docker.compose.project'] = 'foreign'
            elif mutation == 'component': value['Labels']['com.docker.compose.volume'] = 'postgres'
            elif mutation == 'options': value['Options'] = {'device': '/foreign/data'}
            else: value['Driver'] = 'foreign'
            for operation in (
                lambda: s.prepare(['compose'], config),
                lambda: s.initialize(['compose'], config, Path('/source'), Path('/private.env')),
                lambda: s.checked_up(['compose'], config, Path('/source'), Path('/private.env')),
                lambda: s.maintenance(['compose'], config, Path('/source'), Path('/private.env'))):
                with self.subTest(mutation=mutation), patch.object(s, 'no_writers'), \
                        patch.object(s, 'run', return_value=(name + '\n').encode()) as run, \
                        patch.object(s, 'json_command', return_value=[value]), self.assertRaises(c.Refused):
                    operation()
                self.assertTrue(all(call.args[0][:3] == ['docker', 'volume', 'ls'] for call in run.call_args_list))
        with patch.object(s, 'run', return_value=b''), self.assertRaises(c.Refused):
            s.validate_volumes(config)
        with patch.object(s, 'run', return_value=b''):
            s.validate_volumes(config, allow_missing=True)

    def test_egress_exact_policy_rejects_removed_denial_reordered_hook_and_foreign_reference(self):
        config = configuration(); desired = e.policy(config); data = rules(config)
        e.validate_backend(data, desired, 'iptables v1.8.9 (nf_tables)\n')
        e.require_exact_policy(data, desired)
        for changed in (data.replace(b'-d 169.254.0.0/16 -j DROP', b'-d 169.254.0.0/16 -j RETURN'),
                        data.replace(b'-A INPUT ', b'-A INPUT -j ACCEPT\n-A INPUT ', 1),
                        data + ('-A FOREIGN -j ' + next(iter(desired['chains'])) + '\n').encode()):
            with self.assertRaises(c.Refused): e.require_exact_policy(changed, desired)
        with self.assertRaises(c.Refused): e.validate_backend(data.replace(b':DOCKER-USER - [0:0]\n', b''), desired, 'iptables v1.8.9 (nf_tables)')
        with self.assertRaises(c.Refused): e.validate_backend(data, desired, 'nftables v1.1')

    def test_both_owned_bridges_have_host_denial_and_only_postgres_database_exception(self):
        desired = e.policy(configuration())
        self.assertEqual({r[1] for r in desired['hooks']['INPUT']}, {desired['bridge'], desired['database_bridge']})
        database = next(rows for chain, rows in desired['chains'].items() if chain.startswith('CMD_'))
        accepts = [r for r in database if '--dport' in r]
        self.assertEqual(len(accepts), 4)
        self.assertEqual({r[r.index('--dport') + 1] for r in accepts}, {'5432'})
        self.assertEqual({r[r.index('-d') + 1] for r in accepts}, {'172.30.88.2/32'})
        self.assertEqual({r[r.index('-s') + 1] for r in accepts}, {'172.30.88.' + str(n) + '/32' for n in (10, 11, 12, 13)})
        self.assertEqual(database[-1], ['-j', 'DROP'])

    def test_forward_dispatch_cannot_be_missing_conditional_or_shadowed(self):
        config = configuration(); desired = e.policy(config); data = rules(config)
        for changed in (data.replace(b'-A FORWARD -j DOCKER-USER\n', b''),
                        data.replace(b'-A FORWARD -j DOCKER-USER', b'-A FORWARD -j ACCEPT\n-A FORWARD -j DOCKER-USER'),
                        data.replace(b'-A FORWARD -j DOCKER-USER', b'-A FORWARD -i foreign -j DOCKER-USER'),
                        data + b'-A FORWARD -j DOCKER-USER\n'):
            with self.assertRaises(c.Refused):
                e.validate_backend(changed, desired, 'iptables v1.8.9 (nf_tables)')

    def test_two_project_policy_hooks_coexist_and_either_can_be_removed(self):
        first = e.policy(configuration())
        second = e.policy({**configuration(), 'CUTMY_PROJECT': 'cutmy_restore'})
        def apply_script(state, desired, remove=False):
            # Interpret the exact generated insert/delete commands without
            # invoking a host firewall. Hook-order mistakes remain observable.
            for line in e.restore_script(desired, remove=remove).decode().splitlines():
                if line.startswith(':'):
                    state[line[1:].split()[0]] = []
                    continue
                words = shlex.split(line)
                if not words or words[0] in ('*filter', 'COMMIT'):
                    continue
                operation, chain = words[:2]
                if operation == '-A': state[chain].append(words[2:])
                elif operation == '-I': state[chain].insert(int(words[2]) - 1, words[3:])
                elif operation == '-D': state[chain].remove(words[2:])
                elif operation == '-F': state[chain] = []
                elif operation == '-X':
                    self.assertEqual(state[chain], [])
                    del state[chain]
                else: self.fail('Unknown generated firewall operation')
        def rendered(state):
            lines = ['*filter']
            for chain, rows in state.items():
                lines.append(':' + chain + ' - [0:0]')
                lines += ['-A ' + chain + ' ' + shlex.join(row) for row in rows]
            return ('\n'.join(lines + ['COMMIT', ''])).encode()
        state = {'FORWARD': [['-j', 'DOCKER-USER']], 'INPUT': [], 'DOCKER-USER': []}
        apply_script(state, first)
        first_only = rendered(state)
        e.require_exact_policy(first_only, first)
        apply_script(state, second)
        both = rendered(state)
        for desired in (first, second):
            e.require_exact_policy(both, desired)
        for removed, remaining in ((first, second), (second, first)):
            # Exact removal deletes only the removed project's chains/hooks;
            # the retained project's complete validation still succeeds.
            removal = e.restore_script(removed, remove=True).decode()
            for chain in remaining['chains']:
                self.assertNotIn('-F ' + chain, removal)
                self.assertNotIn('-X ' + chain, removal)
            after = deepcopy(state)
            apply_script(after, removed, remove=True)
            e.require_exact_policy(rendered(after), remaining)
        valid = second['hooks']['INPUT'][0]
        for changed in (
                ['-j', 'ACCEPT'], ['-j', 'RETURN'],
                ['!', *valid], [*valid[:1], valid[1] + '+', *valid[2:]],
                [*valid[:-1], 'ACCEPT'], [*valid, '-s', '127.0.0.1/32'],
                [valid[0], first['bridge'], *valid[2:]],
                [*valid[:5], 'cutmy:cutmy_fake:public-v1', *valid[6:]]):
            data = first_only.replace(b'-A INPUT ', ('-A INPUT ' + shlex.join(changed) + '\n-A INPUT ').encode(), 1)
            with self.assertRaises(c.Refused):
                e.require_exact_policy(data, first)

    def test_firewall_scripts_never_flush_foreign_chains(self):
        desired = e.policy(configuration())
        for remove in (False, True):
            lines = e.restore_script(desired, remove=remove).decode().splitlines()
            for line in lines:
                if line.startswith(('-F ', '-X ')):
                    self.assertIn(line.split()[1], desired['chains'])
            self.assertNotIn('-F INPUT', lines)
            self.assertNotIn('-F DOCKER-USER', lines)

    def test_network_validation_rejects_ipv6_foreign_members_wrong_bridge_and_running_operators(self):
        config = configuration(); rows, members = networks(config)
        e.validate_networks(config, rows, members, stopped=True)
        for mutation in ('ipv6', 'foreign', 'wrong_bridge', 'wrong_subnet', 'running_migrate', 'running_maintenance',
                         'enabled_ipv6', 'third_bridge', 'host_namespace', 'shared_namespace', 'ipv6_endpoint'):
            values, containers = deepcopy(rows), deepcopy(members)
            if mutation == 'ipv6': values[1]['EnableIPv6'] = True
            elif mutation == 'foreign': containers['api']['Config']['Labels']['com.docker.compose.project'] = 'foreign'
            elif mutation == 'wrong_bridge': values[0]['Options']['com.docker.network.bridge.name'] = 'foreign'
            elif mutation == 'wrong_subnet': values[0]['IPAM']['Config'][0]['Subnet'] = '172.30.99.0/24'
            elif mutation.startswith('running_'): containers[mutation.removeprefix('running_')]['State']['Running'] = True
            elif mutation == 'third_bridge': containers['api']['NetworkSettings']['Networks']['foreign_bridge'] = {}
            elif mutation == 'host_namespace': containers['api']['HostConfig']['NetworkMode'] = 'host'
            elif mutation == 'shared_namespace': containers['api']['HostConfig']['NetworkMode'] = 'container:other'
            elif mutation == 'ipv6_endpoint': containers['worker']['NetworkSettings']['Networks']['cutmy_fixture_database']['GlobalIPv6Address'] = 'fd00::10'
            else: containers['postgres']['HostConfig']['Sysctls'] = {}
            with self.subTest(mutation=mutation), self.assertRaises(c.Refused):
                e.validate_networks(config, values, containers, stopped=True)

    def test_shared_project_lock_serializes_different_installation_states(self):
        with tempfile.TemporaryDirectory() as directory:
            with c.project_lock(configuration(), Path(directory)):
                with self.assertRaises(c.Refused):
                    with c.project_lock(configuration(), Path(directory)): pass
            with c.project_lock(configuration(), Path(directory)): pass

    def test_private_credentials_refuse_world_readable_and_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); path = root / 'private.env'
            c.private_write(path, b'test')
            self.assertEqual(c.private_read(path), b'test')
            link = root / 'link'; link.symlink_to(path)
            with self.assertRaises(OSError): c.private_read(link)
            path.chmod(0o644)
            with self.assertRaises(c.Refused): c.private_read(path)

    def test_actual_image_cli_identity_is_required_despite_matching_labels(self):
        source = {'version': 'v0.0.0-candidate.g' + 'a' * 40, 'commit': 'a' * 40,
                  'build_time': '2026-10-11T00:00:00Z', 'toolchain': 'go1.27.2'}
        image = 'sha256:' + 'b' * 64
        labels = {'org.opencontainers.image.version': source['version'], 'org.opencontainers.image.revision': source['commit'],
                  'org.opencontainers.image.created': source['build_time'], 'org.opencontainers.image.source': 'https://github.com/Kay0k1/cutmyvideo-core'}
        info = [{'Id': image, 'Os': 'linux', 'Architecture': 'amd64', 'Config': {'User': 'node', 'Labels': labels}}]
        version = s.expected_version(source)
        with patch.object(s, 'json_command', return_value=info), patch.object(s, 'run', return_value=json.dumps(version).encode()):
            self.assertEqual(s.image_verify(image, source), version)
        for field, value in (('commit', 'c' * 40), ('version', 'dev'), ('go_version', 'go1.27.1'), ('arch', 'arm64')):
            with patch.object(s, 'json_command', return_value=info), patch.object(s, 'run', return_value=json.dumps({**version, field: value}).encode()), self.assertRaises(c.Refused):
                s.image_verify(image, source)

    def test_restore_accepts_real_gnu_hardlink_and_rejects_escape_special_and_duplicate_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); good = root / 'good.gz'
            archive(good, [('./source', tarfile.REGTYPE, b'media'), ('./alias', tarfile.LNKTYPE, './source')])
            s.validate_media_archive(good)
            for number, rows in enumerate((
                [('../outside', tarfile.REGTYPE, b'x')], [('/absolute', tarfile.REGTYPE, b'x')],
                [('link', tarfile.SYMTYPE, '')], [('pipe', tarfile.FIFOTYPE, '')],
                [('a', tarfile.REGTYPE, b'x'), ('./a', tarfile.REGTYPE, b'x')],
                [('link', tarfile.LNKTYPE, '../../outside')])):
                path = root / str(number); archive(path, rows)
                with self.assertRaises(c.Refused): s.validate_media_archive(path)

    def test_media_long_headers_are_bounded_before_tarfile_allocation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            path = root / 'long.gz';archive(path, [('a' * 5000, tarfile.REGTYPE, b'x')])
            with self.assertRaises(c.Refused): s.validate_media_archive(path)
            path = root / 'pax.gz';archive(path, [('a' * 110, tarfile.REGTYPE, b'x')], format=tarfile.PAX_FORMAT)
            with self.assertRaises(c.Refused): s.validate_media_archive(path)

    def test_profile_constraints_keep_admin_credentials_out_of_apps_and_preserve_https(self):
        profile = yaml.safe_load((ROOT / 'compose.public.yml').read_bytes())
        overlay = yaml.safe_load((ROOT / 'compose.https.example.yml').read_bytes())
        for name in ('api', 'worker', 'migrate', 'maintenance'):
            service = profile['services'][name]
            self.assertEqual(service['user'], '1000:1000')
            self.assertTrue(service['read_only'])
            self.assertEqual(service['restart'], 'no')
            self.assertEqual(service['cap_drop'], ['ALL'])
            self.assertEqual(len(service['tmpfs']), 1)
            self.assertIn('size=268435456', service['tmpfs'][0])
            self.assertIn('mode=0700,uid=1000,gid=1000', service['tmpfs'][0])
            environment = json.dumps(service['environment'])
            self.assertNotIn('CUTMY_ADMIN_PASSWORD', environment)
            self.assertEqual('cutmy_migrate:' in environment, name == 'migrate')
            self.assertNotIn('CUTMY_MIGRATION_PASSWORD', environment if name != 'migrate' else '')
            for field in ('mem_limit', 'cpus', 'pids_limit', 'logging', 'stop_grace_period'):
                self.assertIn(field, service)
        for name in ('api', 'worker'):
            self.assertEqual(overlay['services'][name]['environment']['COOKIE_SECURE'], 'true')
            self.assertEqual(overlay['services'][name]['environment']['TRUST_PROXY'], 'true')
            self.assertEqual(overlay['services'][name]['environment']['PUBLIC_ORIGIN'], 'https://${CUTMY_DOMAIN:?Set CUTMY_DOMAIN to the hostname served by Caddy}')
        self.assertTrue(profile['networks']['database']['internal'])
        self.assertNotIn('ports', profile['services']['postgres'])
        self.assertTrue(profile['services']['api']['ports'][0].startswith('127.0.0.1:'))


if __name__ == '__main__':
    unittest.main()
