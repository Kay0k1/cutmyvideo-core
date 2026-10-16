"""Offline consumer refusal controls; signatures/archive parsing are synthetic.

These tests never claim actual release verification or install network packages.
The real tagged native workflow exercises the signed downloaded distributions.
"""
from copy import deepcopy
import importlib.util
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('release_sdk_install_controls', Path(__file__).with_name('release-sdk-consumer.py'))
s = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(s)


class SDKInstallConsumerControls(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.evidence = self.root / 'downloads'
        self.assets = self.evidence / 'assets'
        self.assets.mkdir(parents=True)
        self.version, self.commit, self.target = 'v1.4.0', 'a' * 40, ('linux', 'amd64')
        self.source = {'version': self.version, 'commit': self.commit, 'tree': 'b' * 40, 'publishable': True}
        self.binary = b'synthetic-native-bytes-not-executable'
        for name in s.consumer.asset_names(self.version):
            (self.assets / name).write_bytes(('synthetic-' + name).encode())
        (self.assets / 'SOURCE.json').write_bytes(s.m.canonical(self.source))
        self.metadata = {'package_version': '1.4.0', 'packages': [
            {'kind': kind, 'name': name} for kind, name in s.sdk.package_names('1.4.0').items()]}
        self.receipt = {'schema_version': 1, 'ok': True, 'repository': s.REPOSITORY,
            'version': self.version, 'source_commit': self.commit, 'provenance_verified_subjects': 7,
            'spdx_verified_subjects': 5, 'sdk_provenance_verified_subjects': 4,
            'assets': {p.name: s.consumer.file_hash(p) for p in self.assets.iterdir()}}
        archive = s.m.archive_name(self.source, *self.target)
        self.status = {'pass': True, 'private_cleanup': True, 'failure': None, 'version': self.version,
            'expected_commit': self.commit, 'target': {'os': 'linux', 'arch': 'amd64'},
            'native_smoke': {'archive': archive, 'archive_sha256': self.receipt['assets'][archive],
                'binary_sha256_before': s.m.digest(self.binary), 'binary_sha256_after': s.m.digest(self.binary)}}
        self.persist()

    def persist(self):
        raw = s.m.canonical(self.receipt)
        (self.evidence / 'consumer-verification.json').write_bytes(raw)
        self.status['consumer'] = {'path': 'consumer-verification.json', 'sha256': s.m.digest(raw), 'bytes': len(raw)}
        (self.evidence / 'result.json').write_bytes(s.m.canonical(self.status))

    def checked(self):
        scratch = Path(tempfile.mkdtemp(dir=self.root))
        with patch.object(s.m, 'load_source', return_value=self.source), \
                patch.object(s.sdk, 'load_manifest', return_value=(self.metadata, {})), \
                patch.object(s.m, 'read_archive', return_value={'cutmy': self.binary}):
            return s.checked_inputs(self.evidence, scratch, self.version, self.commit, self.target)

    def test_reuses_exact_authenticated_inputs_and_detects_post_install_changes(self):
        source, metadata, hashes, controls, binary = self.checked()
        self.assertEqual(binary.read_bytes(), self.binary)
        self.assertEqual(len(hashes), 18)
        s.checked_unchanged(self.evidence, hashes, controls, binary, s.m.digest(self.binary))
        for path in (self.assets / s.sdk.package_names('1.4.0')['wheel'],
                     binary.parent / 'assets' / s.sdk.package_names('1.4.0')['npm'],
                     self.evidence / 'consumer-verification.json', binary):
            raw = path.read_bytes()
            path.write_bytes(raw + b'changed')
            with self.assertRaises(ValueError): s.checked_unchanged(self.evidence, hashes, controls, binary, s.m.digest(self.binary))
            path.write_bytes(raw)

    def test_missing_sdk_crypto_or_failed_native_download_never_allows_install(self):
        original_receipt, original_status = deepcopy(self.receipt), deepcopy(self.status)
        changes = [('receipt', 'sdk_provenance_verified_subjects', 0), ('receipt', 'source_commit', 'c' * 40),
                   ('receipt', 'ok', False), ('status', 'pass', False), ('status', 'private_cleanup', False),
                   ('status', 'target', {'os': 'windows', 'arch': 'amd64'}), ('status', 'native_smoke', {})]
        for where, key, value in changes:
            with self.subTest(where=where, key=key):
                self.receipt, self.status = deepcopy(original_receipt), deepcopy(original_status)
                (self.receipt if where == 'receipt' else self.status)[key] = value
                self.persist()
                with self.assertRaises(ValueError): self.checked()

    def test_changed_package_missing_asset_and_stale_receipt_digest_refuse(self):
        wheel = self.assets / s.sdk.package_names('1.4.0')['wheel']
        raw = wheel.read_bytes()
        wheel.write_bytes(raw + b'x')
        with self.assertRaises(ValueError): self.checked()
        wheel.write_bytes(raw)
        wheel.unlink()
        with self.assertRaises(ValueError): self.checked()
        wheel.write_bytes(raw)
        (self.evidence / 'consumer-verification.json').write_bytes(s.m.canonical({**self.receipt, 'extra': True}))
        with self.assertRaises(ValueError): self.checked()

    def test_historical_release_cannot_be_certified_as_having_sdk_assets(self):
        self.version = 'v1.3.0'
        with self.assertRaises(ValueError): self.checked()

    def exercise_install(self, *, corrupt=False):
        private = self.root / ('install-corrupt' if corrupt else 'install')
        private.mkdir()
        tools = private / 'tools'
        tools.mkdir()
        for name in ('node', 'npm'):
            (tools / name).touch()
        entry = tools / 'node_modules/npm/bin/npm-cli.js'
        entry.parent.mkdir(parents=True)
        entry.touch()
        commands = []
        _, subtitle = s.subtitle_case()
        verdict = {'version': '1.4.0', 'exports': True, 'no_runtime_dependencies': True, 'subtitle': subtitle}
        if corrupt: verdict['subtitle']['outputs'][0]['text'] += 'wrong clock'
        def command(argv, name, seconds):
            commands.append((argv, name, seconds))
            out = private / (name + '.json')
            out.write_bytes(s.m.canonical(verdict))
            return out
        with patch.object(s.shutil, 'which', side_effect=lambda name: str(tools / name)):
            result = s.installed_smoke(self.assets, self.metadata, private / 'cutmy', private, command)
        return commands, result

    def test_installation_is_offline_without_dependencies_or_package_lifecycle_scripts(self):
        commands, result = self.exercise_install()
        by_name = {name: argv for argv, name, seconds in commands}
        pip = by_name['python-install']
        for flag in ('-I', '--isolated', '--no-index', '--no-deps', '--no-cache-dir', '--no-compile'):
            self.assertIn(flag, pip)
        npm = by_name['npm-install']
        for flag in ('--offline', '--ignore-scripts', '--no-audit', '--no-fund', '--package-lock=false', '--omit=dev'):
            self.assertIn(flag, npm)
        self.assertTrue(pip[-1].endswith('cutmyvideo-1.4.0-py3-none-any.whl'))
        self.assertTrue(npm[-1].endswith('cutmyvideo-sdk-1.4.0.tgz'))
        self.assertIn('Привет, редактор!', s.python_program('1.4.0'))
        self.assertEqual(result['cue_count'], 1)
        self.assertIn('no-media-executable', by_name['python-smoke'][-1])
        self.assertIn('no-media-executable', by_name['typescript-smoke'][-1])

    def test_wrong_installed_unicode_text_is_refused(self):
        with self.assertRaises(ValueError): self.exercise_install(corrupt=True)

    def test_failure_still_cleans_private_installation_and_records_bounded_phase(self):
        args = SimpleNamespace(version=self.version, expected_commit=self.commit, target_os='linux', target_arch='amd64',
                               evidence=self.evidence, output=self.root / 'refused')
        seen = []
        def inputs(evidence, private, *rest):
            seen.append(private)
            binary = private / 'cutmy'
            binary.write_bytes(self.binary)
            return self.source, self.metadata, {}, {}, binary
        native = SimpleNamespace(native_target=lambda: ('linux', 'amd64'))
        with patch.object(s, 'load', return_value=native), patch.object(s, 'checked_inputs', side_effect=inputs), \
                patch.object(s, 'installed_smoke', side_effect=ValueError('secret-looking detail must not reach public receipt')):
            with self.assertRaises(ValueError): s.run(args)
        result = json.loads((args.output / 'result.json').read_bytes())
        self.assertFalse(result['pass'])
        self.assertTrue(result['private_cleanup'])
        self.assertEqual(result['failure'], 'offline_install_and_retime')
        self.assertTrue(all(not path.exists() for path in seen))
        self.assertNotIn('secret-looking', json.dumps(result))


if __name__ == '__main__':
    unittest.main()
