#!/usr/bin/env python3
"""Real Git/archive negative controls; synthetic executables do not prove builds.

Only the embedded-Go reader is mocked for package fixture tests. Production
packaging always reads actual Go executable metadata and builds every target.
"""
import copy
from datetime import datetime, timezone
import gzip
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import warnings
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parent))
import release_manifest as release

spec = importlib.util.spec_from_file_location('package_release', Path(__file__).with_name('package-release.py'))
package = importlib.util.module_from_spec(spec)
spec.loader.exec_module(package)


def info_for(source, target_os, target_arch):
    settings = {'CGO_ENABLED': '0', 'GOOS': target_os, 'GOARCH': target_arch,
                '-trimpath': 'true', '-buildmode': 'exe', '-compiler': 'gc',
                'GOAMD64' if target_arch == 'amd64' else 'GOARM64': 'v1' if target_arch == 'amd64' else 'v8.0'}
    return {'GoVersion': source['toolchain'], 'Path': source['module'] + '/cmd/cutmy',
            'Main': {'Path': source['module'], 'Version': '(devel)'}, 'Deps': [],
            'Settings': [{'Key': key, 'Value': value} for key, value in settings.items()]}


class ReleaseControls(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix='cutmy-release-controls-')
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.repo = self.root / 'repo'
        self.repo.mkdir()
        self.command('init', '-q')
        self.command('config', 'user.name', 'Release controls')
        self.command('config', 'user.email', 'release-controls@example.invalid')
        fixture = {'go.mod': 'module example.invalid/cutmy\n\ngo 1.26.9\ntoolchain go1.27.2\n',
                   'go.sum': '', 'CHANGELOG.md': '# Changelog\n\n## 1.2.3 - 2026-10-10\n',
                   'LICENSE': 'fixture license\n', 'THIRD_PARTY_NOTICES.md': '# Fixture notices\n',
                   'docs/release-artifact.txt': 'External FFmpeg\n',
                   'scripts/release.sh': '# fixture\n', 'scripts/package-release.py': '# fixture\n',
                   'scripts/release_manifest.py': '# fixture\n', '.github/workflows/release.yml': '# fixture\n',
                   'internal/app/migrations/001.sql': '-- immutable fixture\n',
                   'internal/app/migrations/001_schema.json': '{}\n'}
        for path, text in fixture.items():
            target = self.repo / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(text)
        self.commit()
        self.sha = self.command('rev-parse', 'HEAD')
        self.source = release.immutable_source(self.repo, 'candidate', self.sha)
        self.source_path = self.root / 'SOURCE.json'
        self.source_path.write_bytes(release.canonical(self.source))

    def command(self, *args):
        return subprocess.check_output(['git', '-C', str(self.repo), *args], stderr=subprocess.PIPE).decode().strip()

    def commit(self):
        self.command('add', '.')
        self.command('commit', '-qm', 'fixture')

    def rejects(self, fn, *args, **kwargs):
        with self.assertRaises((release.InvalidRelease, OSError, subprocess.CalledProcessError)):
            fn(*args, **kwargs)

    def go_reader(self, binary, source, target_os, target_arch, go='go'):
        # Payload bytes are intentionally synthetic; no actual executable claim.
        return info_for(source, target_os, target_arch)

    def make_set(self, name='first', source=None, payload_suffix=b''):
        source = source or self.source
        output = self.root / name
        output.mkdir()
        for target_os, target_arch in release.TARGETS:
            stage = self.root / f'stage-{name}' / release.prefix(source, target_os, target_arch)
            stage.mkdir(parents=True)
            binary = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
            (stage / binary).write_bytes(f'fixture:{target_os}/{target_arch}'.encode() + payload_suffix)
            for file, path in release.DOCUMENTS.items():
                (stage / file).write_bytes((self.repo / path).read_bytes())
            with mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
                package.package(stage, output, source, target_os, target_arch, release.digest((stage / binary).read_bytes()))
        return output

    def sealed(self):
        first, second = self.make_set(), self.make_set('second')
        with mock.patch.object(release, 'go_identity', return_value=self.source['toolchain']), \
                mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
            manifest = release.seal(first, second, self.source)
        return first, manifest

    def verify(self, directory, **kwargs):
        with mock.patch.object(release, 'go_identity', return_value=self.source['toolchain']), \
                mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
            return release.verify(directory, self.source, **kwargs)

    def rewrite_tar(self, directory, mutate):
        path = directory / release.archive_name(self.source, 'linux', 'amd64')
        contents = release.read_archive(path, self.source, 'linux', 'amd64')
        records = []
        for name, data in sorted(contents.items()):
            entry = tarfile.TarInfo(release.prefix(self.source, 'linux', 'amd64') + '/' + name)
            entry.mode, entry.mtime, entry.size = 0o755 if name == 'cutmy' else 0o644, self.source['source_epoch'], len(data)
            records.append((entry, data))
        mutate(records)
        with path.open('wb') as out, gzip.GzipFile(filename='', fileobj=out, mode='wb', mtime=self.source['source_epoch']) as compressed:
            with tarfile.open(fileobj=compressed, mode='w', format=tarfile.PAX_FORMAT) as tar:
                for entry, data in records:
                    entry.size = len(data)
                    tar.addfile(entry, io.BytesIO(data) if entry.isfile() else None)

    def test_resolve_candidate_binds_all_tracked_inputs(self):
        release.validate_source(self.repo, self.source)
        self.assertFalse(self.source['publishable'])
        self.assertEqual(self.source['version'], 'v0.0.0-candidate.g' + self.sha)
        for path in ('scripts/release_manifest.py', '.github/workflows/release.yml',
                     'internal/app/migrations/001.sql', 'internal/app/migrations/001_schema.json'):
            self.assertEqual(self.source['files'][path]['sha256'], release.digest((self.repo / path).read_bytes()))
        self.assertEqual(release.load_source(self.source_path, self.repo, release.digest(self.source_path.read_bytes())), self.source)

    def test_named_or_abbreviated_candidate_is_rejected(self):
        for value in ('HEAD', self.sha[:12], 'main', 'f' * 40):
            with self.subTest(value=value):
                self.rejects(release.immutable_source, self.repo, 'candidate', value)

    def test_dirty_tracked_and_untracked_checkout_rejected(self):
        path = self.repo / 'LICENSE'
        original = path.read_bytes()
        path.write_bytes(b'changed')
        self.rejects(release.validate_source, self.repo, self.source)
        path.write_bytes(original)
        (self.repo / 'untracked').write_text('not a build input')
        self.rejects(release.validate_source, self.repo, self.source)

    def test_wrong_head_and_moved_tag_rejected(self):
        self.command('tag', 'v1.2.3')
        source = release.immutable_source(self.repo, 'release', 'v1.2.3')
        (self.repo / 'LICENSE').write_text('new commit')
        self.commit()
        self.rejects(release.validate_source, self.repo, source)
        self.command('tag', '-f', 'v1.2.3')
        self.rejects(release.validate_source, self.repo, source)

    def test_release_requires_changelog_and_semver(self):
        self.command('tag', 'v1.2.4')
        self.rejects(release.immutable_source, self.repo, 'release', 'v1.2.4')
        for version in ('1.2.3', 'v01.2.3', 'v1.2.3-01', 'v0.0.0-candidate.g' + self.sha):
            with self.subTest(version=version):
                self.rejects(release.release_version, version)
        release.release_version('v1.2.3-rc.1')

    def test_source_tampering_and_duplicate_keys_rejected(self):
        for field, value in (('tree', '0' * 40), ('toolchain', 'go1.27.1'), ('source_epoch', 0)):
            source = copy.deepcopy(self.source)
            source[field] = value
            self.rejects(release.validate_source, self.repo, source)
        self.rejects(release.load_source, self.source_path, expected_sha256='0' * 64)
        self.source_path.write_text('{"mode":"candidate","mode":"release"}')
        self.rejects(release.read_json, self.source_path)

    def test_source_and_output_must_be_outside_checkout(self):
        for path in (self.repo, self.repo / 'dist', self.root / 'link' / 'dist'):
            if path.parts[-2:] == ('link', 'dist'):
                (self.root / 'link').symlink_to(self.repo, target_is_directory=True)
            with self.subTest(path=path):
                self.rejects(release.outside_repo, path, self.repo)

    def test_candidate_cannot_claim_publishability(self):
        source = copy.deepcopy(self.source)
        source['publishable'] = True
        self.rejects(release.validate_source_shape, source)
        first, _ = self.sealed()
        self.rejects(self.verify, first, publishable=True)

    def test_exact_five_targets_seal_and_verify(self):
        first, manifest = self.sealed()
        self.assertEqual(self.verify(first), manifest)
        self.assertEqual(len(manifest['archives']), 5)
        self.assertEqual(len((first / 'SHA256SUMS').read_text().splitlines()), 5)
        self.assertEqual({p.name for p in first.iterdir()}, {item['name'] for item in manifest['archives']} | {'SOURCE.json', 'MANIFEST.json', 'SHA256SUMS'})
        self.assertEqual(manifest['reproducibility']['builds'], 2)
        self.assertEqual(manifest['reproducibility']['first_manifest_sha256'], manifest['reproducibility']['second_manifest_sha256'])

    def test_nonreproducible_build_refuses_seal(self):
        first, second = self.make_set(), self.make_set('second', payload_suffix=b'changed')
        with mock.patch.object(release, 'go_identity', return_value=self.source['toolchain']), \
                mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
            self.rejects(release.seal, first, second, self.source)
        self.assertFalse((first / 'MANIFEST.json').exists())

    def test_absent_extra_and_symlink_archives_rejected(self):
        first, _ = self.sealed()
        stale = first / 'stale.tar.gz'
        stale.write_bytes(b'stale')
        self.rejects(self.verify, first)
        stale.unlink()
        archive = first / release.archive_name(self.source, 'linux', 'amd64')
        archive.rename(self.root / 'saved')
        self.rejects(self.verify, first)
        archive.symlink_to(self.root / 'saved')
        self.rejects(self.verify, first)

    def test_binary_document_and_build_identity_tampering_rejected(self):
        cases = ('cutmy', 'LICENSE', 'BUILD.json')
        for name in cases:
            with self.subTest(name=name):
                directory = self.make_set('tamper-' + name)
                def alter(records):
                    for index, (entry, data) in enumerate(records):
                        if entry.name.endswith('/' + name):
                            records[index] = (entry, data + b'changed')
                self.rewrite_tar(directory, alter)
                self.rejects(release.read_archive, directory / release.archive_name(self.source, 'linux', 'amd64'), self.source, 'linux', 'amd64')

    def test_unsafe_duplicate_nonregular_and_wrong_modes_rejected(self):
        for mutation in ('parent', 'absolute', 'duplicate', 'symlink', 'hardlink', 'mode', 'owner', 'mtime', 'extra'):
            with self.subTest(mutation=mutation):
                directory = self.make_set('unsafe-' + mutation)
                def alter(records):
                    entry, data = records[0]
                    if mutation == 'parent':
                        entry.name = '../escaped'
                    elif mutation == 'absolute':
                        entry.name = '/escaped'
                    elif mutation == 'duplicate':
                        records.append((copy.copy(entry), data))
                    elif mutation in ('symlink', 'hardlink'):
                        entry.type = tarfile.SYMTYPE if mutation == 'symlink' else tarfile.LNKTYPE
                        entry.linkname = '/escaped'
                    elif mutation == 'mode':
                        entry.mode = 0o777
                    elif mutation == 'owner':
                        entry.uid = 1000
                    elif mutation == 'mtime':
                        entry.mtime += 1
                    else:
                        entry.name = release.prefix(self.source, 'linux', 'amd64') + '/extra'
                self.rewrite_tar(directory, alter)
                self.rejects(release.read_archive, directory / release.archive_name(self.source, 'linux', 'amd64'), self.source, 'linux', 'amd64')

    def test_zip_symlink_duplicate_and_nonexecutable_rejected(self):
        for mutation in ('symlink', 'duplicate', 'nonexecutable'):
            with self.subTest(mutation=mutation):
                directory = self.make_set('zip-' + mutation)
                path = directory / release.archive_name(self.source, 'windows', 'amd64')
                with zipfile.ZipFile(path) as original:
                    entries = [(item, original.read(item)) for item in original.infolist()]
                binary = next(item for item, _ in entries if item.filename.endswith('/cutmy.exe'))
                if mutation == 'symlink':
                    binary.external_attr = (stat.S_IFLNK | 0o755) << 16
                elif mutation == 'nonexecutable':
                    binary.external_attr = (stat.S_IFREG | 0o644) << 16
                else:
                    entries.append((copy.copy(entries[0][0]), entries[0][1]))
                with warnings.catch_warnings(), zipfile.ZipFile(path, 'w') as zipped:
                    warnings.simplefilter('ignore', UserWarning)
                    for entry, data in entries:
                        zipped.writestr(entry, data)
                self.rejects(release.read_archive, path, self.source, 'windows', 'amd64')

    def test_embedded_target_toolchain_and_build_flags_rejected(self):
        baseline = info_for(self.source, 'linux', 'amd64')
        for field, value in (('GoVersion', 'go1.27.1'), ('Path', 'example.invalid/other')):
            changed = copy.deepcopy(baseline)
            changed[field] = value
            self.rejects(release.validate_binary_info, changed, self.source, 'linux', 'amd64')
        for key, value in (('CGO_ENABLED', '1'), ('GOOS', 'windows'), ('GOARCH', 'arm64'),
                           ('-trimpath', 'false'), ('GOAMD64', 'v3')):
            changed = copy.deepcopy(baseline)
            next(item for item in changed['Settings'] if item['Key'] == key)['Value'] = value
            self.rejects(release.validate_binary_info, changed, self.source, 'linux', 'amd64')
        changed = copy.deepcopy(baseline)
        changed['Settings'].append({'Key': '-race', 'Value': 'true'})
        self.rejects(release.validate_binary_info, changed, self.source, 'linux', 'amd64')

    def test_arch_only_dependency_without_notice_rejected(self):
        directory = self.make_set()
        def extra_module(binary, source, os_, arch, go='go'):
            info = self.go_reader(binary, source, os_, arch)
            if (os_, arch) == ('darwin', 'arm64'):
                info['Deps'] = [{'Path': 'example.invalid/missing', 'Version': 'v1.0.0'}]
            return info
        with mock.patch.object(release, 'go_identity', return_value=self.source['toolchain']), \
                mock.patch.object(release, 'binary_info', side_effect=extra_module):
            self.rejects(release.set_manifest, directory, self.source)

    def test_manifest_checksum_and_mixed_source_rejected(self):
        first, _ = self.sealed()
        for name in ('SOURCE.json', 'MANIFEST.json', 'SHA256SUMS'):
            path = first / name
            original = path.read_bytes()
            path.write_bytes(original + b'changed')
            with self.subTest(name=name), self.assertRaises((ValueError, release.InvalidRelease)):
                self.verify(first)
            path.write_bytes(original)
        different = copy.deepcopy(self.source)
        different['commit'] = '0' * 40
        different['version'] = 'v0.0.0-candidate.g' + '0' * 40
        self.rejects(release.exact_directory, first, different, True)

    def test_output_lock_preserves_existing_files_and_refuses_concurrency(self):
        output = self.root / 'output'
        output.mkdir()
        (output / 'old').write_bytes(b'preserve')
        with self.assertRaises(release.InvalidRelease), release.output_lock(output, self.repo):
            self.fail('stale output accepted')
        self.assertEqual((output / 'old').read_bytes(), b'preserve')
        (output / 'old').unlink()
        with release.output_lock(output, self.repo):
            with self.assertRaises(FileExistsError), release.output_lock(output, self.repo):
                self.fail('concurrent output accepted')
        self.assertFalse((self.root / '.output.release-lock').exists())

    def test_package_refuses_extra_staging_and_overwrite(self):
        directory = self.make_set()
        stage = self.root / 'stage-first' / release.prefix(self.source, 'linux', 'amd64')
        (stage / 'unrelated').write_text('extra')
        self.rejects(package.package, stage, directory, self.source, 'linux', 'amd64', release.digest((stage / 'cutmy').read_bytes()))
        (stage / 'unrelated').unlink()
        with mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
            self.rejects(package.package, stage, directory, self.source, 'linux', 'amd64', release.digest((stage / 'cutmy').read_bytes()))

    def test_resolver_cli_never_overwrites_or_creates_a_tag(self):
        script = Path(__file__).with_name('release_manifest.py')
        output = self.root / 'cli-source.json'
        command = [sys.executable, str(script), 'resolve', '--repo', str(self.repo),
                   '--candidate', self.sha, '--output', str(output)]
        first = subprocess.run(command, capture_output=True, text=True)
        self.assertEqual(first.returncode, 0, first.stderr)
        self.assertEqual(first.stdout.strip(), release.digest(release.canonical(self.source)))
        self.assertEqual(self.command('tag', '--list'), '')
        self.assertEqual(output.read_bytes(), release.canonical(self.source))
        second = subprocess.run(command, capture_output=True, text=True)
        self.assertNotEqual(second.returncode, 0)
        self.assertEqual(output.read_bytes(), release.canonical(self.source))

    def test_verifier_cli_refuses_foreign_source_and_manifest_before_go(self):
        first, manifest = self.sealed()
        script = Path(__file__).with_name('release_manifest.py')
        base = [sys.executable, str(script), 'verify', '--directory', str(first), '--source', str(self.source_path)]
        expected_source = release.digest(release.canonical(self.source))
        expected_manifest = release.digest(release.canonical(manifest))
        for source_sha, manifest_sha, message in (('0' * 64, expected_manifest, 'SOURCE.json digest mismatch'),
                                                  (expected_source, '0' * 64, 'MANIFEST.json digest mismatch')):
            result = subprocess.run(base + ['--expected-source-sha256', source_sha,
                                           '--expected-manifest-sha256', manifest_sha], capture_output=True, text=True)
            self.assertEqual(result.returncode, 1)
            self.assertIn(message, result.stderr)
            self.assertEqual(result.stdout, '')

    def test_tagged_release_archives_have_same_contract_as_candidate(self):
        self.command('tag', 'v1.2.3')
        source = release.immutable_source(self.repo, 'release', 'v1.2.3')
        first, second = self.make_set('release-first', source), self.make_set('release-second', source)
        with mock.patch.object(release, 'go_identity', return_value=source['toolchain']), \
                mock.patch.object(release, 'binary_info', side_effect=self.go_reader):
            manifest = release.seal(first, second, source)
            self.assertEqual(release.verify(first, source, publishable=True), manifest)
        self.assertTrue(manifest['publishable'])
        self.assertEqual(manifest['mode'], 'release')

    def test_just_built_binary_receipt_refuses_stale_payload(self):
        directory = self.make_set()
        stage = self.root / 'stage-first' / release.prefix(self.source, 'linux', 'amd64')
        expected = release.digest((stage / 'cutmy').read_bytes())
        (stage / 'cutmy').write_bytes(b'stale executable with identical Go target')
        with mock.patch.object(release, 'binary_info', side_effect=AssertionError('receipt checked too late')):
            with self.assertRaisesRegex(release.InvalidRelease, 'digest captured immediately'):
                package.package(stage, directory, self.source, 'linux', 'amd64', expected)

    def test_source_build_requires_independent_digest_before_any_go(self):
        for expected in (None, '0' * 64):
            with self.subTest(expected=expected), \
                    mock.patch.object(release, 'go_identity', side_effect=AssertionError('Go invoked before identity check')):
                self.rejects(release.build, self.repo, source_path=self.source_path, expected_source_sha256=expected)

    def test_pax_control_and_decompression_bounded_before_tar_parser(self):
        directory = self.make_set()
        path = directory / release.archive_name(self.source, 'linux', 'amd64')
        with mock.patch.object(release, 'MAX_UNCOMPRESSED', 64), \
                mock.patch.object(release.tarfile, 'open', side_effect=AssertionError('unbounded archive reached tar parser')):
            with self.assertRaisesRegex(release.InvalidRelease, 'decompression size bound'):
                release.read_archive(path, self.source, 'linux', 'amd64')
        extension = tarfile.TarInfo('PaxHeaders/fixture')
        extension.type = tarfile.XHDTYPE
        extension.size = release.MAX_CONTROL_HEADER + 512
        with path.open('wb') as out, gzip.GzipFile(filename='', fileobj=out, mode='wb', mtime=self.source['source_epoch']) as compressed:
            compressed.write(extension.tobuf() + b'\0' * (extension.size + 1024))
        with mock.patch.object(release.tarfile, 'open', side_effect=AssertionError('oversized PAX reached tar parser')):
            with self.assertRaisesRegex(release.InvalidRelease, 'Oversized PAX'):
                release.read_archive(path, self.source, 'linux', 'amd64')

    def test_zip_index_bounded_before_zip_parser(self):
        directory = self.make_set()
        path = directory / release.archive_name(self.source, 'windows', 'amd64')
        data = bytearray(path.read_bytes())
        # Central directory count is in EOCD, before ZipFile constructs members.
        data[-14:-10] = b'\xff\xff\xff\xff'
        path.write_bytes(data)
        with mock.patch.object(release.zipfile, 'ZipFile', side_effect=AssertionError('unbounded index reached ZIP parser')):
            with self.assertRaisesRegex(release.InvalidRelease, 'ZIP central directory'):
                release.read_archive(path, self.source, 'windows', 'amd64')

    def test_immutable_snapshot_excludes_ignored_inputs_and_preserves_blobs(self):
        (self.repo / '.git/info/exclude').write_text('internal/app/ignored.go\n')
        (self.repo / 'internal/app/ignored.go').write_text('package app // must not enter compiler input\n')
        release.validate_source(self.repo, self.source)
        destination = self.root / 'snapshot'
        release.materialize_source(self.repo, self.source, destination)
        self.assertFalse((destination / 'internal/app/ignored.go').exists())
        self.assertEqual({p.relative_to(destination).as_posix() for p in destination.rglob('*') if p.is_file()},
                         set(self.source['files']))
        for name, identity in self.source['files'].items():
            self.assertEqual(release.digest((destination / name).read_bytes()), identity['sha256'])
        self.rejects(release.materialize_source, self.repo, self.source, destination)

    def test_package_inspection_and_archive_share_captured_binary_bytes(self):
        existing = self.make_set()
        stage = self.root / 'stage-first' / release.prefix(self.source, 'linux', 'amd64')
        original = (stage / 'cutmy').read_bytes()
        destination = self.root / 'captured-payload'
        def inspect_then_replace(binary, source, os_, arch, go='go'):
            self.assertEqual(Path(binary).read_bytes(), original)
            self.assertNotEqual(Path(binary), stage / 'cutmy')
            (stage / 'cutmy').write_bytes(b'replaced during embedded-info inspection')
            return info_for(source, os_, arch)
        with mock.patch.object(release, 'binary_info', side_effect=inspect_then_replace):
            archive = package.package(stage, destination, self.source, 'linux', 'amd64', release.digest(original))
        self.assertEqual(release.read_archive(archive, self.source, 'linux', 'amd64')['cutmy'], original)


if __name__ == '__main__':
    unittest.main()
