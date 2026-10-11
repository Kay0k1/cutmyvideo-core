"""Draft transport negative controls; signatures and native execution are mocked."""
import copy
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock
import urllib.request

ATTEMPTS = 0
def forbidden(*args, **kwargs):
    global ATTEMPTS
    ATTEMPTS += 1
    raise AssertionError('actual child/network forbidden')

SCRIPTS = Path(__file__).resolve().parent
transport = None
VERSION = 'v1.0.0'
COMMIT = 'a' * 40
NAMES = set()

def metadata():
    return {'id': 17, 'tag_name': VERSION, 'draft': True, 'prerelease': False,
            'published_at': None, 'target_commitish': COMMIT,
            'assets': [{'id': i, 'name': name, 'size': 3, 'digest': 'sha256:' + 'b' * 64,
                        'state': 'uploaded', 'url': transport.API + '/releases/assets/' + str(i)}
                       for i, name in enumerate(sorted(NAMES), 1)]}

class TransportControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        global ATTEMPTS, transport, NAMES
        ATTEMPTS = 0
        cls.guards = [mock.patch.object(subprocess, 'Popen', forbidden),
                      mock.patch.object(urllib.request, 'urlopen', forbidden),
                      mock.patch.object(socket.socket, 'connect', forbidden),
                      mock.patch.object(socket.socket, 'connect_ex', forbidden),
                      mock.patch.object(socket, 'create_connection', forbidden),
                      mock.patch.object(os, 'system', forbidden)]
        for name in ('posix_spawn', 'posix_spawnp'):
            if hasattr(os, name):
                cls.guards.append(mock.patch.object(os, name, forbidden))
        # Class cleanup also runs if guarded candidate import fails before
        # tearDownClass. Discovery itself never installs process-wide patches.
        cls.addClassCleanup(cls.stop_guards)
        for guard in cls.guards:
            guard.start()
        with mock.patch.object(sys, 'path', [str(SCRIPTS), *sys.path]):
            spec = importlib.util.spec_from_file_location('reviewed_download_transport',
                                                        SCRIPTS / 'release-consumer-download.py')
            transport = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(transport)
        NAMES = {transport.manifest.archive_name({'version': VERSION}, os_, arch)
                 for os_, arch in transport.manifest.TARGETS} | {
            'SOURCE.json', 'MANIFEST.json', 'SHA256SUMS', 'SBOM.spdx.json', 'TRUST.json',
            'provenance.sigstore.json', 'sbom.sigstore.json', 'ATTESTATIONS.json'}

    @classmethod
    def stop_guards(cls):
        while cls.guards:
            cls.guards.pop().stop()

    @classmethod
    def tearDownClass(cls):
        try:
            if ATTEMPTS:
                raise AssertionError('actual child/network attempt')
            print('SIGNED TRANSPORT PURE CONTROL actual_child_network_attempts=0', flush=True)
        finally:
            cls.stop_guards()

    def test_draft_identity_and_exact_thirteen_asset_set_refuse_mixed_releases(self):
        good = metadata()
        self.assertEqual(len(transport.validate_metadata(good, VERSION, 17, NAMES)['assets']), 13)
        cases = []
        for key, value in (('id', True), ('tag_name', 'v1.0.1'), ('draft', False), ('prerelease', True), ('published_at', 'now')):
            changed = copy.deepcopy(good); changed[key] = value; cases.append(changed)
        changed = copy.deepcopy(good); changed['assets'].pop(); cases.append(changed)
        changed = copy.deepcopy(good); changed['assets'].append(copy.deepcopy(changed['assets'][0])); cases.append(changed)
        changed = copy.deepcopy(good); changed['assets'][1]['name'] = changed['assets'][0]['name']; cases.append(changed)
        changed = copy.deepcopy(good); changed['assets'][1]['id'] = changed['assets'][0]['id']; cases.append(changed)
        changed = copy.deepcopy(good); changed['assets'][0]['name'] = '../outside'; cases.append(changed)
        for case in cases:
            with self.subTest(case=case), self.assertRaises(transport.Refused):
                transport.validate_metadata(case, VERSION, 17, NAMES)

    def test_missing_digest_oversized_download_and_changed_asset_identity_are_visible(self):
        good = metadata(); initial = transport.validate_metadata(good, VERSION, 17, NAMES)
        for key, value in (('digest', None), ('digest', 'sha256:' + 'g' * 64), ('size', 0),
                           ('size', True), ('size', 129 * 1024 * 1024), ('state', 'new'), ('url', 'https://foreign.invalid/asset')):
            changed = copy.deepcopy(good); changed['assets'][0][key] = value
            with self.subTest(key=key), self.assertRaises(transport.Refused):
                transport.validate_metadata(changed, VERSION, 17, NAMES)
        changed = copy.deepcopy(good)
        for asset in changed['assets']:
            if asset['name'].endswith(('.tar.gz', '.zip')):
                asset['size'] = 128 * 1024 * 1024
        with self.assertRaises(transport.Refused):
            transport.validate_metadata(changed, VERSION, 17, NAMES)
        for key, value in (('digest', 'sha256:' + 'c' * 64), ('size', 4), ('id', 1000)):
            changed = copy.deepcopy(good); changed['assets'][0][key] = value
            if key == 'id': changed['assets'][0]['url'] = transport.API + '/releases/assets/1000'
            self.assertNotEqual(transport.validate_metadata(changed, VERSION, 17, NAMES), initial)

    def test_only_exact_tag_workflow_attempt_one_and_minimal_command_environment(self):
        env = {'GITHUB_ACTIONS': 'true', 'RUNNER_ENVIRONMENT': 'github-hosted',
               'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_REPOSITORY': transport.REPOSITORY,
               'GITHUB_REF': 'refs/tags/' + VERSION, 'GITHUB_SHA': COMMIT, 'GITHUB_WORKFLOW_SHA': COMMIT,
               'GITHUB_WORKFLOW_REF': transport.REPOSITORY + '/.github/workflows/release-consumer.yml@refs/tags/' + VERSION,
               'GITHUB_RUN_ATTEMPT': '1', 'GITHUB_RUN_ID': '19'}
        with mock.patch.dict(os.environ, env, clear=True):
            self.assertEqual(transport.workflow_identity(VERSION, COMMIT)['GITHUB_RUN_ID'], '19')
            for key, value in (('GITHUB_REF', 'refs/heads/main'), ('GITHUB_WORKFLOW_SHA', 'c' * 40),
                               ('GITHUB_RUN_ATTEMPT', '2'), ('RUNNER_ENVIRONMENT', 'self-hosted'), ('GITHUB_RUN_ID', '0')):
                with self.subTest(key=key), mock.patch.dict(os.environ, {key: value}), self.assertRaises(transport.Refused):
                    transport.workflow_identity(VERSION, COMMIT)
        with mock.patch.dict(os.environ, {'PATH': '/test', 'GH_TOKEN': 'synthetic', 'GH_DEBUG': 'api',
                                        'GH_HOST': 'foreign.invalid', 'GIT_CONFIG_PARAMETERS': 'hostile',
                                        'PRODUCTION_PASSWORD': 'synthetic-unrelated'}, clear=True):
            authenticated = transport.command_env(Path('/owned'), True)
            smoke = transport.command_env(Path('/owned'), False)
        self.assertEqual(authenticated['GH_HOST'], 'github.com')
        self.assertEqual(authenticated['GH_TOKEN'], 'synthetic')
        for key in ('GH_DEBUG', 'GIT_CONFIG_PARAMETERS', 'PRODUCTION_PASSWORD'):
            self.assertNotIn(key, authenticated)
        self.assertNotIn('GH_TOKEN', smoke)

    def test_spawn_failure_retains_first_original_terminal_record(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve(); evidence = root / 'evidence'; evidence.mkdir(); private = root / 'private'; private.mkdir()
            with mock.patch.object(transport, 'WindowsJob', return_value=mock.Mock()), \
                 mock.patch.object(subprocess, 'Popen', side_effect=OSError('synthetic spawn refusal')) as spawn:
                with self.assertRaises(transport.Refused):
                    transport.capture(['synthetic'], evidence, 'once', private, transport.time.monotonic() + 10, limit=4)
            spawn.assert_called_once()
            result = json.loads((evidence / 'once.result.json').read_bytes())
            self.assertFalse(result['spawned']); self.assertFalse(result['reaped'])
            self.assertIsNone(result['pid']); self.assertIsNone(result['exit_code'])
            self.assertEqual(result['error_class'], 'OSError')
            for label in ('stdout', 'stderr'):
                self.assertEqual(result[label]['bytes'], 0)
                self.assertEqual(result[label]['sha256'], hashlib.sha256(b'').hexdigest())
                self.assertEqual((evidence / result[label]['path']).read_bytes(), b'')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve(); evidence = root / 'evidence'; evidence.mkdir(); private = root / 'private'; private.mkdir()
            empty = {'bytes': 0, 'sha256': hashlib.sha256(b'').hexdigest()}
            with mock.patch.object(transport, 'WindowsJob', return_value=mock.Mock()), \
                 mock.patch.object(subprocess, 'Popen', side_effect=OSError('first spawn refusal')) as spawn, \
                 mock.patch.object(transport, 'file_identity', side_effect=[ValueError('changed original stream'), empty]):
                with self.assertRaises(transport.Refused):
                    transport.capture(['synthetic'], evidence, 'once', private, transport.time.monotonic() + 10, limit=4)
            spawn.assert_called_once()
            result = json.loads((evidence / 'once.result.json').read_bytes())
            self.assertEqual(result['error_class'], 'OSError')
            self.assertEqual(result['record_error_class'], 'ValueError')
            self.assertIsNone(result['stdout']['sha256'])
            self.assertEqual((evidence / result['stdout']['path']).read_bytes(), b'')

    def test_stdout_overflow_and_timeout_refuse_after_owned_group_cleanup(self):
        for reason in ('overflow', 'timeout'):
            with self.subTest(reason=reason), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); evidence = root / 'evidence'; evidence.mkdir(); private = root / 'private'; private.mkdir()
                child = mock.Mock(); child.pid = 4242; child.returncode = 0; child.poll.return_value = None
                child.wait.return_value = 0
                def spawn(*args, **kwargs):
                    kwargs['stdout'].write(b'12345' if reason == 'overflow' else b'partial')
                    kwargs['stdout'].flush()
                    kwargs['stderr'].write(b'original refusal'); kwargs['stderr'].flush()
                    return child
                ticks = iter([0, 0, 0, 11, 11, 11]) if reason == 'timeout' else None
                clock = mock.patch.object(transport.time, 'monotonic', side_effect=lambda: next(ticks, 11)) if ticks else mock.patch.object(transport.time, 'sleep')
                kill_signal = getattr(transport.signal, 'SIGKILL', 9)
                # Construct native paths before selecting a modeled Unix branch.
                # Windows still exercises the same refusal/cleanup assertions;
                # its actual Job Object is tested separately by native CI.
                with mock.patch.object(transport.os, 'name', 'posix'), \
                     mock.patch.object(transport.signal, 'SIGKILL', kill_signal, create=True), \
                     mock.patch.object(subprocess, 'Popen', side_effect=spawn) as called, \
                     mock.patch.object(os, 'killpg', side_effect=[None, ProcessLookupError()], create=True) as close, clock:
                    with self.assertRaises(transport.Refused):
                        transport.capture(['synthetic'], evidence, 'once', private, 10 if ticks else transport.time.monotonic() + 10,
                                          limit=4 if reason == 'overflow' else 32, seconds=10)
                called.assert_called_once(); child.wait.assert_called_once_with(timeout=5)
                self.assertEqual(close.call_args_list[0].args, (4242, kill_signal))
                self.assertEqual(close.call_args_list[1].args, (4242, 0))
                result = json.loads((evidence / 'once.result.json').read_bytes())
                self.assertTrue(result[reason]); self.assertTrue(result['reaped']); self.assertTrue(result['contained_cleanup'])
                self.assertEqual((evidence / result['stderr']['path']).read_bytes(), b'original refusal')
                self.assertEqual((evidence / result['stdout']['path']).read_bytes(), b'12345' if reason == 'overflow' else b'partial')

    def test_windows_suspended_assignment_precedes_execution_and_failure_still_closes_job(self):
        for assignment_failure in (False, True):
            with self.subTest(assignment_failure=assignment_failure), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve(); evidence = root / 'evidence'; evidence.mkdir(); private = root / 'private'; private.mkdir()
                events = []; child = mock.Mock(); child.pid = 4242; child.returncode = 0
                child.poll.side_effect = lambda: events.append('poll') or 0
                job = mock.Mock()
                def attach(process):
                    events.append('attach')
                    if assignment_failure: raise OSError('synthetic assignment refusal')
                job.attach_and_resume.side_effect = attach
                job.close.side_effect = lambda process: events.append('close')
                with mock.patch.object(transport.os, 'name', 'nt'), mock.patch.object(transport, 'WindowsJob', return_value=job), mock.patch.object(subprocess, 'Popen', return_value=child) as spawn:
                    if assignment_failure:
                        with self.assertRaises(transport.Refused):
                            transport.capture(['synthetic'], evidence, 'once', private, transport.time.monotonic() + 10, limit=4)
                    else:
                        transport.capture(['synthetic'], evidence, 'once', private, transport.time.monotonic() + 10, limit=4)
                self.assertEqual(spawn.call_args.kwargs['creationflags'], 0x4)
                job.attach_and_resume.assert_called_once_with(child); job.close.assert_called_once_with(child)
                self.assertEqual(events, ['attach', 'close'] if assignment_failure else ['attach', 'poll', 'close'])
                result = json.loads((evidence / 'once.result.json').read_bytes())
                self.assertTrue(result['reaped']); self.assertTrue(result['contained_cleanup'])
                self.assertEqual(result['error_class'], 'OSError' if assignment_failure else None)

    def test_pipeline_refuses_crypto_version_changed_release_and_cleanup_failures(self):
        # This models transport bytes and trust/native helpers; it does not
        # pretend to authenticate signatures or run a native executable.
        source = {'version': VERSION}
        payloads = {name: b'synthetic signed asset' for name in NAMES}
        payloads['SOURCE.json'] = transport.manifest.canonical(source)
        payloads['MANIFEST.json'] = b'{}\n'
        release = metadata()
        for asset in release['assets']:
            raw = payloads[asset['name']]
            asset.update(size=len(raw), digest='sha256:' + hashlib.sha256(raw).hexdigest())
        by_id = {asset['id']: asset['name'] for asset in release['assets']}
        real_temporary = tempfile.TemporaryDirectory
        for failure in ('none', 'crypto', 'version', 'changed_release', 'cleanup'):
            with self.subTest(failure=failure), real_temporary() as temporary:
                root = Path(temporary).resolve(); evidence = root / 'evidence'; events = []
                consumer = SimpleNamespace(asset_names=lambda version: NAMES,
                    plain_json=lambda raw, limit: json.loads(raw), control=lambda directory, name: source,
                    command_output=lambda *args: None)
                native = SimpleNamespace(native_target=lambda: ('linux', 'amd64'),
                    checked_binary=mock.Mock(), runtime_version=mock.Mock())
                if failure == 'version':
                    native.runtime_version.side_effect = RuntimeError('synthetic native mismatch')
                def verify(directory, commit, version):
                    events.append('verify')
                    if failure == 'crypto':
                        raise ValueError('synthetic cryptographic refusal')
                    return {'ok': True, 'source_commit': COMMIT, 'version': VERSION,
                            'repository': transport.REPOSITORY,
                            'assets': {name: hashlib.sha256(raw).hexdigest() for name, raw in payloads.items()},
                            'provenance_verified_subjects': 7, 'spdx_verified_subjects': 5}
                consumer.verify = verify
                def capture(argv, evidence, name, private, deadline, *, limit, seconds=60,
                            authenticated=False, stdout_path=None):
                    path = stdout_path or evidence / (name + '.stdout.bin')
                    if argv[:2] == ['gh', 'api']:
                        identifier = argv[2].rsplit('/', 1)[-1]
                        if '/releases/assets/' in argv[2]:
                            raw = payloads[by_id[int(identifier)]]
                        else:
                            events.append('metadata')
                            current = copy.deepcopy(release)
                            if failure == 'changed_release' and events.count('metadata') == 2:
                                current['assets'][0]['digest'] = 'sha256:' + 'f' * 64
                            raw = json.dumps(current).encode()
                    elif argv[0] == 'git' and 'rev-parse' in argv:
                        raw = (COMMIT + '\n').encode()
                    elif argv[-1] == 'version':
                        events.append('execute'); raw = b'{}'
                    else:
                        raw = b'synthetic tool version\n'
                    path.write_bytes(raw)
                    return path
                class Private:
                    def __init__(self, *args, **kwargs):
                        self.actual = real_temporary(*args, **kwargs)
                        self.name = self.actual.name
                    def cleanup(self):
                        self.actual.cleanup()
                        if failure == 'cleanup':
                            raise OSError('synthetic cleanup failure')
                args = SimpleNamespace(version=VERSION, expected_commit=COMMIT, release_id=17,
                    source_sha256=hashlib.sha256(payloads['SOURCE.json']).hexdigest(),
                    manifest_sha256=hashlib.sha256(payloads['MANIFEST.json']).hexdigest(),
                    target_os='linux', target_arch='amd64', evidence=evidence)
                with mock.patch.object(transport, 'workflow_identity', return_value={'GITHUB_RUN_ID': '19'}), \
                     mock.patch.object(transport, 'capture', side_effect=capture), \
                     mock.patch.object(transport, 'load_tool', side_effect=[consumer, native]), \
                     mock.patch.object(transport.tempfile, 'TemporaryDirectory', Private), \
                     mock.patch.object(transport.manifest, 'read_archive', return_value={'cutmy': b'verified binary'}) as extract, \
                     mock.patch('shutil.disk_usage', return_value=SimpleNamespace(free=transport.MINIMUM_FREE)):
                    if failure == 'none':
                        self.assertTrue(transport.run(args)['pass'])
                    else:
                        with self.assertRaises(transport.Refused):
                            transport.run(args)
                report = json.loads((evidence / 'result.json').read_bytes())
                self.assertEqual(report['pass'], failure == 'none')
                self.assertEqual(report['private_cleanup'], failure != 'cleanup')
                self.assertEqual(events[:2], ['metadata', 'verify'])
                if failure == 'crypto':
                    self.assertNotIn('execute', events); extract.assert_not_called()
                    native.runtime_version.assert_not_called()
                    self.assertEqual(report['failure'], 'signed_consumer_failed')
                else:
                    self.assertEqual(events[2], 'execute'); extract.assert_called_once()
                if failure == 'version':
                    self.assertEqual(report['failure'], 'native_smoke_failed')
                if failure == 'changed_release':
                    self.assertEqual(report['failure'], 'release_changed')
                if failure == 'cleanup':
                    self.assertEqual(report['cleanup_error_class'], 'OSError')
