"""Prepared profile controls: no Docker, firewall, media tools or database calls."""
from copy import deepcopy
import contextlib
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
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


def saved_profile(state, config, supported=False):
    """Small sealed source/state fixture; image execution is mocked by callers."""
    c.private_directory(state, create=True)
    inputs = {'deploy/public_common.py': (ROOT / 'public_common.py').read_bytes() if supported else
              b"KEYS = {'CUTMY_PROJECT'}\ndef parse_config(data):\n    return {}\n",
              'deploy/compose.public.yml': (ROOT / 'compose.public.yml').read_bytes() if supported else
              b'services:\n  worker:\n    environment:\n      WORKER_CONCURRENCY: "1"\n'}
    for name in (*s.manifest.DOCUMENTS.values(), 'go.mod', 'go.sum', 'scripts/release.sh',
                 'scripts/package-release.py', 'scripts/release_manifest.py', '.github/workflows/release.yml'):
        inputs.setdefault(name, b'fixture\n')
    source = {'schema_version': 1, 'mode': 'candidate', 'publishable': False,
              'version': 'v0.0.0-candidate.g' + 'a' * 40, 'commit': 'a' * 40, 'tree': 'b' * 40,
              'source_epoch': 0, 'build_time': s.manifest.timestamp(0), 'toolchain': 'go1.26.1', 'module': 'fixture',
              'files': {name: {'git_blob': 'c' * 40, 'mode': '100644', 'sha256': c.digest(data), 'bytes': len(data)}
                        for name, data in inputs.items()}}
    s.manifest.validate_source_shape(source)
    for name, data in inputs.items():
        path = state / 'source' / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
    s.save_state(state, config, source, 'sha256:' + 'd' * 64, {}, snapshot_exists=True)
    return source


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


def helper_row(spec):
    return {'Id': 'd' * 64, 'Name': '/' + spec['name'], 'Image': spec['image'],
            'Config': {'Labels': spec['labels'].copy(), 'User': '1000:1000',
                       'Entrypoint': [spec['entrypoint']], 'Cmd': spec['argv'][:]},
            'HostConfig': {'NetworkMode': 'none', 'ReadonlyRootfs': True, 'CapDrop': ['ALL'],
                           'SecurityOpt': ['no-new-privileges:true'], 'Memory': 256 * 1024**2,
                           'MemorySwap': 256 * 1024**2, 'NanoCpus': 1000000000, 'PidsLimit': 32},
            'NetworkSettings': {'Networks': {'none': {}}},
            'Mounts': [{'Type': 'volume', 'Name': spec['media'], 'Destination': '/data',
                        'RW': not spec['readonly_media']}] if spec['media'] else []}
class PublicControls(unittest.TestCase):
    def test_generated_legacy_config_can_adopt_unsupported_old_snapshot(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_path, state = root / 'private.env', root / 'new-state'
            with patch.object(sys, 'argv', ['self-host.py', '--config', str(config_path), '--state', str(state), 'init-config']), \
                    patch.object(s.secrets, 'token_urlsafe', side_effect=['a' * 48, 'b' * 48, 'c' * 48]), \
                    contextlib.redirect_stdout(io.StringIO()):
                s.main()
            config = c.parse_config(c.private_read(config_path))
            self.assertEqual(set(config), c.KEYS)
            self.assertNotIn('CUTMY_WORKER_PREPARATION_CONCURRENCY', config)
            old = root / 'old-state'
            source = saved_profile(old, config)
            with patch.object(s, 'image_verify') as image_verify, patch.object(s, 'run') as run:
                s.adopt_image(state, config, old)
                actual_source, record, snapshot, _ = s.load_state(state, config, verify_image=False)
            self.assertEqual(actual_source, source)
            self.assertEqual(record['configuration'], s.public_configuration(config))
            self.assertEqual(s.state_snapshot(old, source).joinpath('deploy/public_common.py').read_bytes(),
                             snapshot.joinpath('deploy/public_common.py').read_bytes())
            self.assertEqual(image_verify.call_count, 1)
            run.assert_not_called()

    def test_explicit_preparation_refuses_old_adoption_before_image_or_state_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old = root / 'old-state'
            saved_profile(old, configuration())
            before = {p.relative_to(old): p.read_bytes() for p in old.rglob('*') if p.is_file()}
            for value in ('0', '1'):
                target = root / ('new-state-' + value)
                with self.subTest(value=value), patch.object(s, 'image_verify') as image_verify, \
                        patch.object(s, 'save_state') as save, patch.object(s.shutil, 'copytree') as copy, \
                        self.assertRaisesRegex(c.Refused, 'worker_preparation_requires_supported_snapshot'):
                    s.adopt_image(target, {**configuration(), 'CUTMY_WORKER_PREPARATION_CONCURRENCY': value}, old)
                self.assertFalse(target.exists())
                image_verify.assert_not_called()
                save.assert_not_called()
                copy.assert_not_called()
            self.assertEqual(before, {p.relative_to(old): p.read_bytes() for p in old.rglob('*') if p.is_file()})

    def test_preparation_support_requires_both_parser_and_compose_mapping(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            enabled = {**configuration(), 'CUTMY_WORKER_PREPARATION_CONCURRENCY': '1'}
            saved_profile(root / 'supported', enabled, supported=True)
            snapshot = root / 'supported/source'
            s.require_preparation_support(enabled, snapshot)
            for name, unsupported in (('deploy/public_common.py', b"KEYS = {'CUTMY_PROJECT'}\ndef parse_config(data):\n    return {}\n"),
                                      ('deploy/compose.public.yml', b'services: {}\n')):
                path = snapshot / name
                original = path.read_bytes()
                path.write_bytes(unsupported)
                with self.subTest(name=name), self.assertRaisesRegex(c.Refused, 'worker_preparation_requires_supported_snapshot'):
                    s.require_preparation_support(enabled, snapshot)
                path.write_bytes(original)
            with patch.object(s, 'image_verify'):
                s.adopt_image(root / 'new-supported', enabled, root / 'supported')
                s.load_state(root / 'new-supported', enabled, verify_image=False)

    def test_explicit_preparation_refuses_unsupported_build_before_state_materialization(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = saved_profile(root / 'old', configuration())
            state = root / 'build-state'
            with patch.object(s, 'REPO', root / 'old/source'), patch.object(s.manifest, 'immutable_source', return_value=source), \
                    patch.object(s.manifest, 'validate_source') as validate, \
                    patch.object(s.manifest, 'materialize_source') as materialize, patch.object(s, 'run') as run, \
                    self.assertRaisesRegex(c.Refused, 'worker_preparation_requires_supported_snapshot'):
                s.build(state, {**configuration(), 'CUTMY_WORKER_PREPARATION_CONCURRENCY': '0'}, source['commit'], None)
            validate.assert_called_once()
            materialize.assert_not_called()
            run.assert_not_called()
            self.assertFalse(state.exists())

    def test_existing_unsupported_preparation_state_refuses_before_restore_or_image_access(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            state, config_path = root / 'state', root / 'private.env'
            config = {**configuration(), 'CUTMY_WORKER_PREPARATION_CONCURRENCY': '0'}
            saved_profile(state, config)
            c.private_write(config_path, s.config_text(config))
            before = {p.relative_to(state): p.read_bytes() for p in state.rglob('*') if p.is_file()}
            with patch.object(sys, 'argv', ['self-host.py', '--config', str(config_path), '--state', str(state),
                                          'restore', '--directory', str(root / 'backup')]), \
                    patch.object(s, 'local_engine'), patch.object(s, 'image_verify') as image_verify, \
                    patch.object(s, 'restore') as restore, patch.object(s, 'run') as run, \
                    self.assertRaisesRegex(c.Refused, 'worker_preparation_requires_supported_snapshot'):
                s.main()
            image_verify.assert_not_called()
            restore.assert_not_called()
            run.assert_not_called()
            self.assertEqual(before, {p.relative_to(state): p.read_bytes() for p in state.rglob('*') if p.is_file()})

    def test_optional_worker_preparation_keeps_old_configs_and_bounds_explicit_values(self):
        original = configuration()
        self.assertEqual(c.parse_config(s.config_text(original)), original)
        for value in ('0', '1', '8'):
            requested = {**original, 'CUTMY_WORKER_PREPARATION_CONCURRENCY': value}
            self.assertEqual(c.parse_config(s.config_text(requested)), requested)
            self.assertEqual(c.runtime_environment(requested, 'sha256:' + 'a' * 64)['CUTMY_WORKER_PREPARATION_CONCURRENCY'], value)
        for value in ('', '-1', '9', '01', '1 ', '$OTHER', '1;false'):
            with self.subTest(value=value), self.assertRaises(c.Refused):
                c.parse_config(s.config_text({**original, 'CUTMY_WORKER_PREPARATION_CONCURRENCY': value}))
        with self.assertRaises(c.Refused):
            c.parse_config(s.config_text(original) + b'CUTMY_WORKER_PREPARATION_CONCURRENCY=0\nCUTMY_WORKER_PREPARATION_CONCURRENCY=1\n')

    def test_optional_worker_preparation_is_sealed_and_compose_reads_only_private_config(self):
        original = configuration()
        legacy = {key: original[key] for key in s.PUBLIC_KEYS if key in original}
        self.assertEqual(s.public_configuration(original), legacy)
        enabled = {**original, 'CUTMY_WORKER_PREPARATION_CONCURRENCY': '1'}
        self.assertNotEqual(s.public_configuration(enabled), legacy)
        self.assertNotEqual(s.public_configuration(enabled), s.public_configuration({**enabled, 'CUTMY_WORKER_PREPARATION_CONCURRENCY': '0'}))
        public = yaml.safe_load((ROOT / 'compose.public.yml').read_text())
        self.assertEqual(public['x-environment']['WORKER_PREPARATION_CONCURRENCY'], '${CUTMY_WORKER_PREPARATION_CONCURRENCY:-0}')
        local = yaml.safe_load((ROOT / 'compose.example.yml').read_text())
        self.assertEqual(local['services']['worker']['environment']['WORKER_PREPARATION_CONCURRENCY'], '${WORKER_PREPARATION_CONCURRENCY:-0}')
        with patch.dict(os.environ, {'CUTMY_WORKER_PREPARATION_CONCURRENCY': '8', 'WORKER_PREPARATION_CONCURRENCY': '8'}, clear=True):
            self.assertEqual(c.command_environment(), {})

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

    def test_installation_entrypoint_distinguishes_configuration_failures_before_docker(self):
        config = configuration()
        for key, value, reason in (('CUTMY_HTTP_PORT', 'invalid', 'loopback_port_invalid'),
                                   ('CUTMY_PROCESSING_PREFIX', config['CUTMY_DATABASE_PREFIX'], 'networks_overlap')):
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / 'private.env'
                c.private_write(path, s.config_text({**config, key: value}))
                args = ['self-host.py', '--config', str(path), '--state', str(Path(directory) / 'state'), 'prepare']
                out, err = io.StringIO(), io.StringIO()
                with patch.object(sys, 'argv', args), patch.object(s, 'local_engine') as engine, \
                        contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as exit:
                    s.entrypoint()
                self.assertEqual(exit.exception.code, 1)
                self.assertEqual(out.getvalue(), '')
                self.assertEqual(err.getvalue(), 'Public installation operation refused (' + reason + '); '
                                 'check prerequisites and private configuration.\n')
                engine.assert_not_called()
                self.assertFalse((Path(directory) / 'state').exists())
                for secret in (str(path), *(config[key] for key in c.PASSWORDS)):
                    self.assertNotIn(secret, err.getvalue())

    def test_installation_entrypoint_redacts_foreign_refusals_and_other_exception_messages(self):
        private = 'password=super_private_value /root/private/config.env\nsecond line'
        for error in (c.Refused(private), c.Refused('a' * 81), c.Refused('UPPERCASE'), c.Refused(''),
                      s.manifest.InvalidRelease(private), OSError(private), ValueError(private),
                      KeyError(private), TypeError(private), tarfile.ReadError(private)):
            out, err = io.StringIO(), io.StringIO()
            with self.subTest(kind=type(error).__name__), patch.object(s, 'main', side_effect=error), \
                    contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as exit:
                s.entrypoint()
            self.assertEqual(exit.exception.code, 1)
            self.assertEqual(out.getvalue(), '')
            self.assertEqual(err.getvalue(), 'Public installation operation refused (operation_refused); '
                             'check prerequisites and private configuration.\n')
            self.assertNotIn('/root/private/config.env', err.getvalue())
            self.assertNotIn('super_private_value', err.getvalue())

    def test_egress_entrypoint_exposes_only_fixed_refusal_code_and_keeps_exit_contract(self):
        out, err = io.StringIO(), io.StringIO()
        with patch.object(e, 'main', side_effect=c.Refused('owned_policy_missing_or_modified')), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as exit:
            e.entrypoint()
        self.assertEqual(exit.exception.code, 1)
        self.assertEqual(out.getvalue(), '')
        self.assertEqual(err.getvalue(), 'Public egress operation refused (owned_policy_missing_or_modified); '
                         'check prerequisites and private configuration.\n')

    def test_egress_entrypoint_redacts_foreign_refusals_and_other_exception_messages(self):
        private = 'password=super_private_value /root/private/config.env\nsecond line'
        for error in (c.Refused(private), c.Refused('a' * 81), c.Refused('UPPERCASE'),
                      c.Refused(''), OSError(private), ValueError(private), KeyError(private), TypeError(private)):
            out, err = io.StringIO(), io.StringIO()
            with self.subTest(kind=type(error).__name__), patch.object(e, 'main', side_effect=error), \
                    contextlib.redirect_stdout(out), contextlib.redirect_stderr(err), self.assertRaises(SystemExit) as exit:
                e.entrypoint()
            self.assertEqual(exit.exception.code, 1)
            self.assertEqual(out.getvalue(), '')
            self.assertEqual(err.getvalue(), 'Public egress operation refused (operation_refused); '
                             'check prerequisites and private configuration.\n')
            self.assertNotIn('/root/private/config.env', err.getvalue())
            self.assertNotIn('super_private_value', err.getvalue())

    def test_conntrack_state_order_matches_same_exact_policy_on_both_backends(self):
        config = configuration(); desired = e.policy(config); original = rules(config)
        self.assertEqual(original.count(b'--ctstate ESTABLISHED,RELATED'), 3)
        for count in (1, 2, 3):
            data = original.replace(b'--ctstate ESTABLISHED,RELATED', b'--ctstate RELATED,ESTABLISHED', count)
            for backend in ('nf_tables', 'legacy'):
                with self.subTest(count=count, backend=backend):
                    e.validate_backend(data, desired, 'iptables v1.8.9 (' + backend + ')\n')
                    e.require_exact_policy(data, desired)
        self.assertEqual(rules(config), original)

    def test_conntrack_equivalence_refuses_changed_duplicate_or_extra_states(self):
        config = configuration(); desired = e.policy(config); original = rules(config)
        for states in ('ESTABLISHED', 'RELATED', 'NEW', 'INVALID', 'ESTABLISHED,RELATED,NEW',
                       'RELATED,ESTABLISHED,NEW', 'RELATED,RELATED', 'ESTABLISHED,ESTABLISHED',
                       'RELATED,ESTABLISHED,RELATED', 'RELATED,ESTABLISHED,', 'related,ESTABLISHED'):
            changed = original.replace(b'--ctstate ESTABLISHED,RELATED', ('--ctstate ' + states).encode(), 1)
            with self.subTest(states=states), self.assertRaises(c.Refused):
                e.require_exact_policy(changed, desired)

    def test_conntrack_equivalence_refuses_negation_flags_target_rule_order_and_duplicates(self):
        config = configuration(); desired = e.policy(config); original = rules(config)
        prefix = next(iter(desired['chains']))
        canonical = '-A ' + prefix + ' -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN\n'
        reordered = canonical.replace('ESTABLISHED,RELATED', 'RELATED,ESTABLISHED')
        for replacement in (reordered.replace('--ctstate', '! --ctstate'),
                            reordered.replace('-m conntrack', '! -m conntrack'),
                            reordered.replace('-j RETURN', '--ctstatus CONFIRMED -j RETURN'),
                            reordered.replace('-j RETURN', '-j ACCEPT'),
                            reordered.replace('-m conntrack', '-s 127.0.0.1/32 -m conntrack'),
                            reordered + reordered,
                            '-A ' + prefix + ' -j RETURN\n' + reordered):
            changed = original.replace(canonical.encode(), replacement.encode(), 1)
            with self.subTest(replacement=replacement), self.assertRaises(c.Refused):
                e.require_exact_policy(changed, desired)
        # A parent-chain shadow remains a refusal even when all owned state
        # matches use the accepted serialization order.
        normalized = original.replace(b'ESTABLISHED,RELATED', b'RELATED,ESTABLISHED')
        for parent in ('INPUT', 'DOCKER-USER'):
            changed = normalized.replace(('-A ' + parent + ' ').encode(),
                                         ('-A ' + parent + ' -j ACCEPT\n-A ' + parent + ' ').encode(), 1)
            with self.subTest(parent=parent), self.assertRaises(c.Refused):
                e.require_exact_policy(changed, desired)

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
        with patch.object(s, 'json_command', return_value=info), patch.object(s, 'helper_run', return_value=json.dumps(version).encode()):
            self.assertEqual(s.image_verify(image, source, configuration()), version)
        for field, value in (('commit', 'c' * 40), ('version', 'dev'), ('go_version', 'go1.27.1'), ('arch', 'arm64')):
            with patch.object(s, 'json_command', return_value=info), patch.object(s, 'helper_run', return_value=json.dumps({**version, field: value}).encode()), self.assertRaises(c.Refused):
                s.image_verify(image, source, configuration())

    def test_all_daemon_helpers_bind_project_source_image_operation_and_private_mount(self):
        source = {'commit': 'a' * 40}; config = configuration(); image = 'sha256:' + 'b' * 64
        identities = set()
        for purpose in ('version', 'tools', 'backup-tar', 'restore-tar'):
            media = config['CUTMY_PROJECT'] + '_media' if purpose.endswith('-tar') else None
            spec = s.helper_spec(config, source, image, purpose, media)
            identities.add(spec['name'])
            s.validate_helper(helper_row(spec), spec)
            argv = s.helper_command(spec)
            self.assertIn('--rm', argv)
            self.assertEqual(argv[argv.index('--name') + 1], spec['name'])
            self.assertEqual(argv[argv.index('--network') + 1], 'none')
            self.assertEqual(spec['labels']['video.cutmy.helper.source-sha256'], c.digest(s.manifest.canonical(source)))
            self.assertNotIn('com.docker.compose.project', spec['labels'])
            for key, value in spec['labels'].items():
                self.assertIn(key + '=' + value, argv)
        self.assertEqual(len(identities), 4)

    def test_daemon_partial_creation_is_discovered_and_removed_after_failed_client(self):
        with patch.object(s.secrets, 'token_hex', return_value='c' * 32):
            spec = s.helper_spec(configuration(), {'commit': 'a' * 40}, 'sha256:' + 'b' * 64, 'version')
            created = False
            calls = []
            def daemon(argv, **kwargs):
                nonlocal created
                calls.append(argv)
                if argv[:2] == ['docker', 'run']:
                    created = True
                    raise c.Refused('lost_client_reply')
                if argv[:3] == ['docker', 'ps', '-aq']:
                    self.assertEqual(argv[-1], 'name=^/' + spec['name'] + '$')
                    return ('d' * 64 + '\n').encode() if created else b''
                if argv[:3] == ['docker', 'rm', '-f']:
                    self.assertEqual(argv[-1], 'd' * 64)
                    created = False
                    return b''
                self.fail('unexpected mutation')
            with patch.object(s, 'run', side_effect=daemon), patch.object(s, 'json_command', return_value=[helper_row(spec)]), \
                    self.assertRaisesRegex(c.Refused, 'lost_client_reply'):
                s.helper_run(configuration(), {'commit': 'a' * 40}, spec['image'], 'version')
            self.assertFalse(created)
            self.assertEqual(sum(argv[:3] == ['docker', 'rm', '-f'] for argv in calls), 1)

    def test_helper_cleanup_refuses_foreign_image_labels_name_operation_network_or_mount(self):
        spec = s.helper_spec(configuration(), {'commit': 'a' * 40}, 'sha256:' + 'b' * 64, 'backup-tar', 'cutmy_fixture_media')
        for attack in ('name', 'image', 'project', 'source', 'identity', 'command', 'entrypoint', 'network', 'mount', 'readonly'):
            row = helper_row(spec)
            if attack == 'name': row['Name'] = '/foreign'
            elif attack == 'image': row['Image'] = 'sha256:' + 'e' * 64
            elif attack in ('project', 'source', 'identity'): row['Config']['Labels']['video.cutmy.' + ('helper.' if attack != 'project' else '') + attack] = 'foreign'
            elif attack == 'command': row['Config']['Cmd'] = ['-C', '/etc', '-czf', '-', '.']
            elif attack == 'entrypoint': row['Config']['Entrypoint'] = ['sh']
            elif attack == 'network': row['HostConfig']['NetworkMode'] = 'host'
            elif attack == 'mount': row['Mounts'][0]['Name'] = 'foreign_media'
            else: row['Mounts'][0]['RW'] = True
            with self.subTest(attack=attack), patch.object(s, 'run', return_value=('d' * 64 + '\n').encode()) as run, \
                    patch.object(s, 'json_command', return_value=[row]), self.assertRaises(c.Refused):
                s.cleanup_helper(spec)
            self.assertTrue(all(argv.args[0][:2] == ['docker', 'ps'] for argv in run.call_args_list))

    def test_helper_discovery_failure_is_not_absence_and_confirmed_cleanup_is_required(self):
        spec = s.helper_spec(configuration(), {'commit': 'a' * 40}, 'sha256:' + 'b' * 64, 'version')
        with patch.object(s, 'run', side_effect=c.Refused('discovery_failed')), self.assertRaises(c.Refused):
            s.cleanup_helper(spec)

    def test_helper_automatic_removal_race_requires_new_successful_absence_query(self):
        spec = s.helper_spec(configuration(), {'commit': 'a' * 40}, 'sha256:' + 'b' * 64, 'version')
        with patch.object(s, 'run', side_effect=[('d' * 64 + '\n').encode(), b'']) as run, \
                patch.object(s, 'json_command', side_effect=c.Refused('auto_removed')):
            s.cleanup_helper(spec)
        self.assertEqual(run.call_count, 2)
        with patch.object(s, 'run', return_value=('d' * 64 + '\n').encode()), \
                patch.object(s, 'json_command', side_effect=c.Refused('inspect_unavailable')), self.assertRaises(c.Refused):
            s.cleanup_helper(spec)
        mismatched = helper_row(spec); mismatched['Id'] = 'e' * 64
        with patch.object(s, 'run', return_value=('d' * 64 + '\n').encode()) as run, \
                patch.object(s, 'json_command', return_value=[mismatched]), self.assertRaises(c.Refused):
            s.cleanup_helper(spec)
        self.assertEqual(run.call_count, 1)
        with patch.object(s, 'run', side_effect=[('d' * 64 + '\n').encode(), b'', ('d' * 64 + '\n').encode()]), \
                patch.object(s, 'json_command', return_value=[helper_row(spec)]), self.assertRaisesRegex(c.Refused, 'unconfirmed'):
            s.cleanup_helper(spec)

    def test_shared_runner_streams_owned_input_descriptor_without_closing_it(self):
        with tempfile.TemporaryFile() as source, tempfile.TemporaryFile() as destination:
            data = b'bounded-dump-bytes' * 1000
            source.write(data); source.seek(0)
            c.run([sys.executable, '-c', 'import shutil,sys; shutil.copyfileobj(sys.stdin.buffer,sys.stdout.buffer)'],
                  stdin=source, output=destination, timeout=5)
            self.assertFalse(source.closed)
            destination.seek(0)
            self.assertEqual(destination.read(), data)
        with self.assertRaises(c.Refused): c.run([sys.executable, '-c', 'pass'], input=b'x', stdin=source)

    def test_restore_streams_dump_and_media_through_bounded_shared_paths(self):
        config = configuration(); source = {'commit': 'a' * 40}
        record = {'source_sha256': 'b' * 64, 'image_id': 'sha256:' + 'c' * 64}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ('database.dump', 'media.tar.gz'):
                c.private_write(root / name, b'private-backup-bytes')
            c.private_write(root / 'backup.json', c.canonical({
                'profile': c.PROFILE, 'schema_version': 1, 'writers_stopped': True, 'data_dir': '/data',
                'source_sha256': record['source_sha256'], 'image_id': record['image_id'], 'source_commit': source['commit'],
                'files': {name: {'sha256': s.file_digest(root / name), 'bytes': (root / name).stat().st_size}
                          for name in ('database.dump', 'media.tar.gz')}}))
            captured = []
            def bounded(argv, **kwargs):
                if 'pg_restore' in argv:
                    self.assertEqual(kwargs['stdin'].read(), b'private-backup-bytes')
                    self.assertEqual(kwargs['timeout'], 1800)
                    captured.append('database')
                return b''
            def helper(*args, **kwargs):
                self.assertEqual(args[3], 'restore-tar')
                self.assertEqual(kwargs['stdin'].read(), b'private-backup-bytes')
                self.assertEqual(kwargs['media'], 'cutmy_fixture_media')
                self.assertEqual(kwargs['timeout'], 1800)
                captured.append('media')
            with patch.object(s, 'validate_media_archive'), patch.object(s, 'fresh_project'), patch.object(s, 'validate_volumes'), \
                    patch.object(s, 'volume', return_value='cutmy_fixture_media'), patch.object(s, 'run', side_effect=bounded), \
                    patch.object(s, 'helper_run', side_effect=helper), patch.object(s.subprocess, 'run', side_effect=AssertionError('unbounded restore runner')):
                s.restore(config, source, record, ['compose'], Path('/snapshot'), root)
            self.assertEqual(captured, ['database', 'media'])

    def test_shared_runner_timeout_kills_actual_owned_parent_and_descendant(self):
        # Real Python process groups only. No Docker, application or host rules.
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'pids'
            script = ('import os,subprocess,sys,time; '
                      'p=subprocess.Popen([sys.executable,"-c","import time; time.sleep(30)"]); '
                      'open(sys.argv[1],"w").write(str(os.getpid())+" "+str(p.pid)); time.sleep(30)')
            with self.assertRaisesRegex(c.Refused, 'timed_out'):
                c.run([sys.executable, '-c', script, marker], timeout=.5)
            pids = marker.read_text().split()
            self.assertEqual(len(pids), 2)
            deadline = time.monotonic() + 3
            def live(pid):
                path = Path('/proc') / pid / 'stat'
                return path.exists() and path.read_text().split(')')[-1].strip().split()[0] != 'Z'
            while time.monotonic() < deadline and any(live(pid) for pid in pids):
                time.sleep(.02)
            self.assertFalse(any(live(pid) for pid in pids))

    def test_shared_runner_rejects_oversized_diagnostics(self):
        with self.assertRaisesRegex(c.Refused, 'diagnostics_too_large'):
            c.run([sys.executable, '-c', 'import os; os.write(1,b"x"*(9*1024**2))'], timeout=5)

    def test_sigterm_reaches_owned_nested_runner_groups_and_restores_signal_handlers(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / 'pids'
            child = ('import os,subprocess,sys,time; '
                     'p=subprocess.Popen([sys.executable,"-c","import time; time.sleep(30)"]); '
                     'open(sys.argv[1],"w").write(str(os.getpid())+" "+str(p.pid)); time.sleep(30)')
            parent = ('import sys; sys.path.insert(0,sys.argv[1]); import public_common as c; '
                      '\ntry: c.run([sys.executable,"-c",sys.argv[2],sys.argv[3]],timeout=20)'
                      '\nexcept c.Refused as e: print(str(e)); raise SystemExit(31)')
            process = subprocess.Popen([sys.executable, '-c', parent, str(ROOT), child, str(marker)],
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            pids = []
            try:
                deadline = time.monotonic() + 3
                while not marker.exists() and time.monotonic() < deadline:
                    time.sleep(.02)
                pids = marker.read_text().split()
                os.killpg(process.pid, signal.SIGTERM)
                stdout, stderr = process.communicate(timeout=5)
                self.assertEqual(process.returncode, 31, stderr.decode())
                self.assertEqual(stdout.strip(), b'operation_interrupted')
                def live(pid):
                    path = Path('/proc') / pid / 'stat'
                    return path.exists() and path.read_text().split(')')[-1].strip().split()[0] != 'Z'
                self.assertFalse(any(live(pid) for pid in pids))
            finally:
                # Rescue only the exact process groups started by this test.
                for pid in [process.pid, *([int(pids[0])] if pids else [])]:
                    try: os.killpg(pid, signal.SIGKILL)
                    except ProcessLookupError: pass
                process.communicate(timeout=5)
        previous = {number: signal.getsignal(number) for number in (signal.SIGINT, signal.SIGTERM)}
        c.run([sys.executable, '-c', 'pass'], timeout=5)
        self.assertEqual(previous, {number: signal.getsignal(number) for number in previous})

    def test_sigint_uses_fixed_diagnostic_and_restores_prior_handler_on_failure(self):
        previous = signal.getsignal(signal.SIGINT)
        def interrupted(*args, **kwargs):
            signal.raise_signal(signal.SIGINT)
        with patch.object(c, '_run_group', side_effect=interrupted), self.assertRaisesRegex(c.Refused, '^operation_interrupted$'):
            c.run(['unused'])
        self.assertEqual(signal.getsignal(signal.SIGINT), previous)

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

    def test_postgres_ready_requires_final_tcp_listener_not_initialization_socket(self):
        profile = yaml.safe_load((ROOT / 'compose.public.yml').read_bytes())
        self.assertEqual(profile['services']['postgres']['healthcheck']['test'],
                         ['CMD', 'pg_isready', '-h', '127.0.0.1', '-p', '5432', '-U', 'cutmy_admin', '-d', 'cutmy'])


if __name__ == '__main__':
    unittest.main()
