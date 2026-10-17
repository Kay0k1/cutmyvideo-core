"""Test-only Git/archive builders with synthetic executable metadata.

No assertions or test cases live here. Release policy expectations remain in
individual control suites; these helpers do not prove actual builds.
"""
import importlib.util
from pathlib import Path
import subprocess
import tempfile
from unittest import mock

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


class ReleaseFixture:
    """Reusable fixture state; the owning suite registers cleanup immediately."""

    def initialize(self, add_cleanup):
        self.temporary = tempfile.TemporaryDirectory(prefix='cutmy-release-controls-')
        add_cleanup(self.temporary.cleanup)
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
