#!/usr/bin/env python3
"""Resolve immutable source and verify exact release/candidate archive bytes.

No command creates a tag, release or publication. Native consumers must supply
the SOURCE.json digest from the separate resolve stage. Checksums bind stages;
they are not signatures or independent attestations.
"""
import argparse
from contextlib import contextmanager
from datetime import datetime, timezone
import gzip
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import shutil
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import zipfile
import zlib

TARGETS = (('linux', 'amd64'), ('linux', 'arm64'), ('darwin', 'amd64'),
           ('darwin', 'arm64'), ('windows', 'amd64'))
DOCUMENTS = {'LICENSE': 'LICENSE', 'THIRD_PARTY_NOTICES.md': 'THIRD_PARTY_NOTICES.md',
             'CHANGELOG.md': 'CHANGELOG.md', 'README.txt': 'docs/release-artifact.txt'}
SHA = re.compile(r'[0-9a-f]{40}\Z')
HASH = re.compile(r'[0-9a-f]{64}\Z')
VERSION = re.compile(r'v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?\Z')
DEPENDENCIES = ('FFmpeg and ffprobe are external; server platform sources also '
                'require yt-dlp and Node.js.')
REPRO_SCOPE = ('Two builds from one immutable source snapshot in separate output directories on one runner '
               'with identical Go, Python and zlib; build cache may be reused; not independent hermetic rebuilds.')
MAX_UNCOMPRESSED = 149 * 1024 * 1024  # 128 MiB executable + five <=4 MiB files + headers.
MAX_CONTROL_HEADER = 16 * 1024


class InvalidRelease(ValueError):
    pass


def require(condition, message):
    if not condition:
        raise InvalidRelease(message)


def canonical(value):
    return (json.dumps(value, indent=2, sort_keys=True, ensure_ascii=True) + '\n').encode()


def digest(data):
    return hashlib.sha256(data).hexdigest()


def read_json(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, f'Duplicate JSON key: {key}')
            result[key] = value
        return result
    return json.loads(Path(path).read_bytes(), object_pairs_hook=unique)


def run(*args, cwd=None, env=None):
    return subprocess.check_output(args, cwd=cwd, env=env).decode().strip()


def git(repo, *args):
    return subprocess.check_output(['git', '-C', str(repo), *args])


def timestamp(epoch):
    return datetime.fromtimestamp(epoch, timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')


def release_version(version):
    match = VERSION.fullmatch(version)
    require(match is not None, 'Expected vX.Y.Z[-prerelease].')
    require(not version.startswith('v0.0.0-candidate.'), 'Candidate versions cannot be release tags.')
    if match[4]:
        require(all(not item.isdigit() or item == '0' or not item.startswith('0')
                    for item in match[4].split('.')), 'Numeric prerelease identifiers cannot have leading zeros.')


def immutable_source(repo, mode, value):
    repo = Path(repo).resolve()
    require(mode in ('release', 'candidate'), 'Unknown source mode.')
    if mode == 'release':
        release_version(value)
        commit = git(repo, 'rev-parse', '--verify', f'refs/tags/{value}^{{commit}}').decode().strip()
        version = value
    else:
        require(SHA.fullmatch(value) is not None, 'Candidate requires an explicit full 40-character commit SHA.')
        commit = git(repo, 'rev-parse', '--verify', f'{value}^{{commit}}').decode().strip()
        require(commit == value, 'Candidate SHA must name the exact commit.')
        version = f'v0.0.0-candidate.g{commit}'
    files = {}
    for entry in git(repo, 'ls-tree', '-rz', '--full-tree', commit).split(b'\0'):
        if not entry:
            continue
        header, raw_path = entry.split(b'\t', 1)
        mode_bits, kind, blob = header.decode().split()
        path = raw_path.decode('utf-8')
        require(kind == 'blob' and mode_bits in ('100644', '100755'), f'Unsupported tracked source entry: {path}')
        data = git(repo, 'cat-file', 'blob', blob)
        files[path] = {'git_blob': blob, 'mode': mode_bits, 'sha256': digest(data), 'bytes': len(data)}
    mod = git(repo, 'show', f'{commit}:go.mod').decode()
    toolchain = re.search(r'^toolchain (go[0-9.]+)$', mod, re.M)
    module = re.search(r'^module (\S+)$', mod, re.M)
    require(toolchain is not None and module is not None, 'go.mod must pin a toolchain and module identity.')
    if mode == 'release':
        changelog = git(repo, 'show', f'{commit}:CHANGELOG.md').decode()
        require(re.search(r'^## ' + re.escape(version[1:]) + r' - \d{4}-\d{2}-\d{2}$', changelog, re.M),
                'Release tag needs a dated CHANGELOG entry.')
    epoch = int(git(repo, 'show', '-s', '--format=%ct', commit))
    source = {'schema_version': 1, 'mode': mode, 'publishable': mode == 'release',
              'version': version, 'commit': commit,
              'tree': git(repo, 'rev-parse', f'{commit}^{{tree}}').decode().strip(),
              'source_epoch': epoch, 'build_time': timestamp(epoch), 'toolchain': toolchain[1],
              'module': module[1], 'files': files}
    validate_source_shape(source)
    return source


def validate_source_shape(source):
    require(set(source) == {'schema_version', 'mode', 'publishable', 'version', 'commit', 'tree',
                           'source_epoch', 'build_time', 'toolchain', 'module', 'files'}, 'Unexpected SOURCE.json fields.')
    require(source['schema_version'] == 1 and type(source['schema_version']) is int, 'Unsupported source schema.')
    require(SHA.fullmatch(source['commit']) and SHA.fullmatch(source['tree']), 'Invalid immutable Git identity.')
    require(type(source['source_epoch']) is int and 0 <= source['source_epoch'] <= 0xffffffff,
            'Source epoch is outside deterministic gzip timestamp bounds.')
    require(source['build_time'] == timestamp(source['source_epoch']), 'Source timestamp mismatch.')
    require(re.fullmatch(r'go[0-9]+\.[0-9]+\.[0-9]+', source['toolchain']), 'Invalid Go toolchain identity.')
    if source['mode'] == 'candidate':
        require(source['publishable'] is False and source['version'] == f"v0.0.0-candidate.g{source['commit']}",
                'Candidate must be explicitly nonpublishable and contain the full source SHA.')
    else:
        require(source['mode'] == 'release' and source['publishable'] is True, 'Invalid publishable source mode.')
        release_version(source['version'])
    require(isinstance(source['files'], dict) and source['files'], 'Empty source input identity.')
    for path, identity in source['files'].items():
        require(PurePosixPath(path).as_posix() == path and not path.startswith('/')
                and '..' not in PurePosixPath(path).parts, f'Unsafe source path: {path}')
        require(set(identity) == {'git_blob', 'mode', 'sha256', 'bytes'} and SHA.fullmatch(identity['git_blob'])
                and HASH.fullmatch(identity['sha256']) and identity['mode'] in ('100644', '100755')
                and type(identity['bytes']) is int and identity['bytes'] >= 0, f'Invalid source input: {path}')
    for path in (*DOCUMENTS.values(), 'go.mod', 'go.sum', 'scripts/release.sh',
                 'scripts/package-release.py', 'scripts/release_manifest.py', '.github/workflows/release.yml'):
        require(path in source['files'], f'Missing source input: {path}')


def validate_source(repo, source, clean=True):
    validate_source_shape(source)
    repo = Path(repo).resolve()
    # Rechecking a tag can only refuse a moved tag, never choose new build input.
    value = source['version'] if source['mode'] == 'release' else source['commit']
    require(immutable_source(repo, source['mode'], value) == source, 'Resolved source/tag binding has changed.')
    require(git(repo, 'rev-parse', 'HEAD').decode().strip() == source['commit'], 'Checkout must equal resolved source SHA.')
    if clean:
        require(not git(repo, 'status', '--porcelain', '--untracked-files=all'),
                'Builds require a clean checkout, including untracked files.')
        for path, identity in source['files'].items():
            file = repo / path
            require(file.is_file() and not file.is_symlink() and digest(file.read_bytes()) == identity['sha256'],
                    f'Checkout input differs from immutable source: {path}')


def load_source(path, repo=None, expected_sha256=None, clean=True):
    raw = Path(path).read_bytes()
    source = read_json(path)
    require(raw == canonical(source), 'SOURCE.json must use canonical serialization.')
    validate_source_shape(source)
    if expected_sha256 is not None:
        require(HASH.fullmatch(expected_sha256) and digest(raw) == expected_sha256, 'SOURCE.json digest mismatch.')
    if repo is not None:
        validate_source(repo, source, clean=clean)
    return source


def materialize_source(repo, source, destination):
    """Build from verified Git blobs, never ignored or concurrently edited files."""
    validate_source(repo, source)
    destination = Path(destination)
    outside_repo(destination, repo)
    destination.mkdir()  # Must be a new private snapshot, without stale files.
    for name, identity in source['files'].items():
        data = git(repo, 'cat-file', 'blob', identity['git_blob'])
        require(len(data) == identity['bytes'] and digest(data) == identity['sha256'],
                f'Immutable Git blob mismatch: {name}')
        path = destination / name
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open('xb') as out:
            out.write(data)
        path.chmod(0o755 if identity['mode'] == '100755' else 0o644)
    return destination


def prefix(source, target_os, target_arch):
    require((target_os, target_arch) in TARGETS, 'Unknown release target.')
    return f"cutmyvideo-core_{source['version']}_{target_os}_{target_arch}"


def archive_name(source, target_os, target_arch):
    return prefix(source, target_os, target_arch) + ('.zip' if target_os == 'windows' else '.tar.gz')


def expected_contents(target_os):
    return {'cutmy.exe' if target_os == 'windows' else 'cutmy', 'BUILD.json', *DOCUMENTS}


def build_metadata(source, target_os, target_arch, binary):
    return {'schema_version': 1, 'mode': source['mode'], 'publishable': source['publishable'],
            'source_sha256': digest(canonical(source)), 'version': source['version'], 'commit': source['commit'],
            'source_tree': source['tree'], 'source_epoch': source['source_epoch'],
            'os': target_os, 'arch': target_arch, 'build_time': source['build_time'],
            'toolchain': source['toolchain'], 'binary_sha256': digest(binary), 'dependencies': DEPENDENCIES}


def contents_identity(contents, target_os):
    binary = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
    return {name: {'sha256': digest(data), 'bytes': len(data), 'mode': '0755' if name == binary else '0644'}
            for name, data in sorted(contents.items())}


def validate_contents(contents, source, target_os, target_arch):
    require(set(contents) == expected_contents(target_os), 'Package must contain exactly its six expected files.')
    binary = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
    for name, path in DOCUMENTS.items():
        require(digest(contents[name]) == source['files'][path]['sha256']
                and len(contents[name]) == source['files'][path]['bytes'], f'Immutable document mismatch: {name}')
    require(contents['BUILD.json'] == canonical(build_metadata(source, target_os, target_arch, contents[binary])),
            'BUILD.json identity mismatch.')


def preflight_tar(expanded):
    """Bound extension headers before tarfile can allocate from their size field."""
    expanded.seek(0)
    records, files = 0, 0
    while True:
        header = expanded.read(512)
        require(len(header) == 512, 'Truncated tar header.')
        if header == b'\0' * 512:
            require(expanded.read(512) == b'\0' * 512, 'Tar needs two end blocks.')
            while tail := expanded.read(1024 * 1024):
                require(not tail.strip(b'\0'), 'Unexpected data after tar end blocks.')
            break
        records += 1
        require(records <= 12, 'Too many tar records.')
        item = tarfile.TarInfo.frombuf(header, 'utf-8', 'strict')
        require(item.size >= 0, 'Negative tar member size.')
        if item.type == tarfile.XHDTYPE:
            require(item.size <= MAX_CONTROL_HEADER, 'Oversized PAX control header.')
        else:
            require(item.isfile() and item.size <= 128 * 1024 * 1024, 'Unsupported tar record.')
            files += 1
        expanded.seek((item.size + 511) // 512 * 512, 1)
    require(files == 6, 'Tar must contain exactly six regular files.')
    expanded.seek(0)


def preflight_zip(path):
    # Production archives are small, single-disk ZIPs with six entries and no
    # comments/ZIP64. Bound the central directory before ZipFile allocates it.
    with Path(path).open('rb') as file:
        file.seek(-22, 2)
        signature, disk, directory_disk, entries_disk, entries, size, offset, comment = struct.unpack('<4s4H2IH', file.read(22))
        require(signature == b'PK\x05\x06' and disk == directory_disk == 0
                and entries_disk == entries == 6 and comment == 0 and size <= MAX_CONTROL_HEADER
                and offset + size == file.tell() - 22, 'Unexpected or oversized ZIP central directory.')


def read_archive(path, source, target_os, target_arch):
    package_prefix = prefix(source, target_os, target_arch)
    contents = {}
    expected = expected_contents(target_os)
    require(Path(path).stat().st_size <= 128 * 1024 * 1024, 'Archive exceeds verification size bound.')

    def member_name(name, size):
        parts = name.split('/')
        require(len(parts) == 2 and parts[0] == package_prefix and parts[1] in expected,
                f'Unsafe or unexpected archive member: {name}')
        short = parts[1]
        require(short not in contents, f'Duplicate archive member: {name}')
        limit = 128 * 1024 * 1024 if short in ('cutmy', 'cutmy.exe') else 4 * 1024 * 1024
        require(0 <= size <= limit, f'Archive member exceeds verification size bound: {name}')
        return short

    def add(short, data, mode):
        want_mode = 0o755 if short in ('cutmy', 'cutmy.exe') else 0o644
        require(mode == want_mode, f'Unexpected archive file mode: {short}')
        contents[short] = data

    if target_os == 'windows':
        preflight_zip(path)
        with zipfile.ZipFile(path) as zipped:
            for member in zipped.infolist():
                short = member_name(member.filename, member.file_size)
                require(member.create_system == 3 and stat.S_ISREG(member.external_attr >> 16),
                        f'Nonregular archive member: {member.filename}')
                require(not member.flag_bits & 1, 'Encrypted archive entries are not supported.')
                require(member.compress_type == zipfile.ZIP_DEFLATED, 'Unexpected ZIP compression.')
                stamp = datetime.fromtimestamp(max(source['source_epoch'], 315532800), timezone.utc).timetuple()[:6]
                require(member.date_time == (*stamp[:5], stamp[5] // 2 * 2), 'Non-source ZIP timestamp.')
                add(short, zipped.read(member), (member.external_attr >> 16) & 0o7777)
    else:
        with Path(path).open('rb') as file:
            header = file.read(10)
        require(len(header) == 10 and header[:3] == b'\x1f\x8b\x08' and header[3] == 0
                and int.from_bytes(header[4:8], 'little') == source['source_epoch'], 'Non-source gzip metadata.')
        with tempfile.TemporaryFile() as expanded:
            total = 0
            with gzip.open(path, 'rb') as compressed:
                while block := compressed.read(1024 * 1024):
                    total += len(block)
                    require(total <= MAX_UNCOMPRESSED, 'Archive exceeds decompression size bound.')
                    expanded.write(block)
            preflight_tar(expanded)
            with tarfile.open(fileobj=expanded, mode='r:') as tar:
                for member in tar:
                    short = member_name(member.name, member.size)
                    require(member.isfile() and not member.issym() and not member.islnk(),
                            f'Nonregular archive member: {member.name}')
                    require(member.uid == 0 and member.gid == 0 and not member.uname and not member.gname
                            and member.mtime == source['source_epoch'], 'Non-source tar owner/timestamp.')
                    require(member.pax_headers in ({}, {'path': member.name}), 'Unexpected PAX metadata.')
                    add(short, tar.extractfile(member).read(), member.mode)
    validate_contents(contents, source, target_os, target_arch)
    return contents


def tool_environment():
    return dict(os.environ, CGO_ENABLED='0', GOENV='off', GOTOOLCHAIN='local', GOAMD64='v1',
                GOARM64='v8.0', GOFLAGS='', GOEXPERIMENT='', GOWORK='off', GO111MODULE='on')


def go_identity(go='go'):
    return run(go, 'env', 'GOVERSION', env=tool_environment())


def validate_binary_info(info, source, target_os, target_arch):
    require(info.get('GoVersion') == source['toolchain'] and info.get('Path') == source['module'] + '/cmd/cutmy'
            and info.get('Main', {}).get('Path') == source['module'], 'Unexpected embedded Go/module identity.')
    settings = {item['Key']: item.get('Value', '') for item in info.get('Settings', [])}
    require(len(settings) == len(info.get('Settings', [])), 'Duplicate embedded build setting.')
    for key, value in {'CGO_ENABLED': '0', 'GOOS': target_os, 'GOARCH': target_arch,
                       '-trimpath': 'true', '-buildmode': 'exe', '-compiler': 'gc'}.items():
        require(settings.get(key) == value, f'Unexpected embedded build setting: {key}')
    require(not any(key.startswith('vcs.') or key == '-race' for key in settings), 'Unexpected VCS/race build settings.')
    if target_arch == 'amd64':
        require(settings.get('GOAMD64') == 'v1', 'Unexpected amd64 baseline.')
    else:
        require(settings.get('GOARM64') == 'v8.0', 'Unexpected arm64 baseline.')
    require('Replace' not in info.get('Main', {}) and all('Replace' not in dep for dep in info.get('Deps', [])),
            'Module replacements require explicit license/provenance review.')


def binary_info(binary, source, target_os, target_arch, go='go'):
    # Inspect embedded data on the host; do not execute cross-built binaries.
    info = json.loads(run(go, 'version', '-m', '-json', str(binary), env=tool_environment()))
    validate_binary_info(info, source, target_os, target_arch)
    return info


def set_manifest(directory, source, go='go'):
    directory = Path(directory)
    require(go_identity(go) == source['toolchain'], 'Verifier needs the exact pinned Go toolchain.')
    archives = []
    for target_os, target_arch in TARGETS:
        name = archive_name(source, target_os, target_arch)
        path = directory / name
        require(path.is_file() and not path.is_symlink(), f'Missing archive: {name}')
        contents = read_archive(path, source, target_os, target_arch)
        notices = contents['THIRD_PARTY_NOTICES.md'].decode()
        binary = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
        with tempfile.TemporaryDirectory(prefix='cutmy-build-info-') as temporary:
            extracted = Path(temporary) / binary
            extracted.write_bytes(contents[binary])
            info = binary_info(extracted, source, target_os, target_arch, go)
        for dep in info.get('Deps', []):
            require(f"## {dep['Path']} {dep['Version']}\n" in notices,
                    f"Missing dependency notice: {dep['Path']} {dep['Version']}")
        archives.append({'name': name, 'os': target_os, 'arch': target_arch,
                         'sha256': digest(path.read_bytes()), 'bytes': path.stat().st_size,
                         'contents': contents_identity(contents, target_os), 'go_build_info': info})
    return {'schema_version': 1, 'source_sha256': digest(canonical(source)),
            'mode': source['mode'], 'publishable': source['publishable'], 'version': source['version'],
            'commit': source['commit'], 'tree': source['tree'], 'source_epoch': source['source_epoch'],
            'build_time': source['build_time'], 'toolchain': source['toolchain'],
            'tools': {'go': go_identity(go), 'python': platform.python_version(),
                      'zlib_compile': zlib.ZLIB_VERSION, 'zlib_runtime': zlib.ZLIB_RUNTIME_VERSION},
            'archives': archives}


def checksums(manifest):
    return ''.join(f"{item['sha256']}  {item['name']}\n" for item in sorted(manifest['archives'], key=lambda x: x['name'])).encode()


def exact_directory(directory, source, sealed):
    expected = {archive_name(source, os_, arch) for os_, arch in TARGETS}
    if sealed:
        expected |= {'SOURCE.json', 'MANIFEST.json', 'SHA256SUMS'}
    directory = Path(directory)
    require(directory.is_dir() and not directory.is_symlink(), 'Archive set must be a regular directory.')
    require({p.name for p in directory.iterdir()} == expected, 'Archive directory has missing, extra or stale files.')
    require(all(p.is_file() and not p.is_symlink() for p in directory.iterdir()), 'Archive set contains nonregular files.')


def seal(first, second, source, go='go'):
    exact_directory(first, source, False)
    exact_directory(second, source, False)
    a, b = set_manifest(first, source, go), set_manifest(second, source, go)
    require(canonical(a) == canonical(b), 'Two-build archive/content manifests differ.')
    manifest = dict(a, reproducibility={'builds': 2, 'scope': REPRO_SCOPE,
                                       'first_manifest_sha256': digest(canonical(a)),
                                       'second_manifest_sha256': digest(canonical(b))})
    for name, data in {'SOURCE.json': canonical(source), 'MANIFEST.json': canonical(manifest),
                       'SHA256SUMS': checksums(manifest)}.items():
        with (Path(first) / name).open('xb') as out:
            out.write(data)
    return manifest


def verify(directory, source, go='go', publishable=False):
    validate_source_shape(source)
    if publishable:
        require(source['mode'] == 'release' and source['publishable'] is True,
                'Candidate archives cannot be published.')
    exact_directory(directory, source, True)
    directory = Path(directory)
    require((directory / 'SOURCE.json').read_bytes() == canonical(source), 'Archive set source identity mismatch.')
    manifest = read_json(directory / 'MANIFEST.json')
    require((directory / 'MANIFEST.json').read_bytes() == canonical(manifest), 'Noncanonical artifact manifest.')
    base = set_manifest(directory, source, go)
    # Native validators may have different Python/zlib. Preserve recorded builder
    # values; archive metadata/contents and embedded Go identity are verified.
    tools = manifest.get('tools')
    require(isinstance(tools, dict) and set(tools) == set(base['tools']) and tools['go'] == source['toolchain']
            and all(isinstance(v, str) and v for v in tools.values()), 'Missing builder tool identities.')
    base['tools'] = tools
    expected = dict(base, reproducibility={'builds': 2, 'scope': REPRO_SCOPE,
                                          'first_manifest_sha256': digest(canonical(base)),
                                          'second_manifest_sha256': digest(canonical(base))})
    require(manifest == expected, 'Artifact/content/build manifest mismatch.')
    require((directory / 'SHA256SUMS').read_bytes() == checksums(manifest), 'Checksum set mismatch.')
    return manifest


def outside_repo(path, repo):
    require(not Path(path).resolve().is_relative_to(Path(repo).resolve()),
            'Source manifests and build outputs must be outside the source checkout.')


@contextmanager
def output_lock(output, repo):
    output = Path(output).absolute()
    outside_repo(output, repo)
    require(not output.is_symlink(), 'Output cannot be a symlink.')
    output.parent.mkdir(parents=True, exist_ok=True)
    lock = output.parent / f'.{output.name}.release-lock'
    fd = os.open(lock, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    identity = os.fstat(fd)
    try:
        require(not output.exists() or output.is_dir() and not any(output.iterdir()), 'Output must be new or empty.')
        yield output
    finally:
        os.close(fd)
        if lock.exists() and os.path.samestat(identity, lock.stat(follow_symlinks=False)):
            lock.unlink()


def build(repo, version=None, candidate=None, source_path=None, output=None, expected_source_sha256=None):
    repo = Path(repo).resolve()
    require(sum(value is not None for value in (version, candidate, source_path)) == 1,
            'Choose one release tag, --candidate SHA, or --source SOURCE.json.')
    if source_path:
        outside_repo(source_path, repo)
        require(expected_source_sha256 is not None, '--source requires the independent --expected-source-sha256.')
        source = load_source(source_path, repo, expected_source_sha256)
    else:
        require(expected_source_sha256 is None, '--expected-source-sha256 requires --source.')
        source = immutable_source(repo, 'candidate' if candidate else 'release', candidate or version)
        validate_source(repo, source)
    output = Path(output or Path(tempfile.gettempdir()) / f"cutmy-release-{source['version']}")
    env = tool_environment()
    env['SOURCE_DATE_EPOCH'] = str(source['source_epoch'])
    require(go_identity() == source['toolchain'], f"Build requires {source['toolchain']}.")
    with output_lock(output, repo), tempfile.TemporaryDirectory(prefix='cutmy-release-') as temporary:
        temporary = Path(temporary)
        outside_repo(temporary, repo)
        build_root = materialize_source(repo, source, temporary / 'source')
        resolved = temporary / 'SOURCE.json'
        resolved.write_bytes(canonical(source))
        subprocess.run(['python3', str(build_root / 'scripts/update-notices.py'), '--check'], cwd=build_root, env=env, check=True)
        sets = []
        for attempt in (1, 2):
            destination = temporary / f'build-{attempt}'
            destination.mkdir()
            packages = temporary / f'packages-{attempt}'
            packages.mkdir()
            validate_source(repo, source)
            for target_os, target_arch in TARGETS:
                package = packages / prefix(source, target_os, target_arch)
                package.mkdir()
                binary = 'cutmy.exe' if target_os == 'windows' else 'cutmy'
                target_env = dict(env, GOOS=target_os, GOARCH=target_arch)
                ldflags = (f"-s -w -buildid= -X main.version={source['version']} "
                           f"-X main.commit={source['commit']} -X main.buildTime={source['build_time']}")
                subprocess.run(['go', 'build', '-p=2', '-mod=readonly', '-trimpath', '-buildvcs=false',
                                '-ldflags', ldflags, '-o', str(package / binary), './cmd/cutmy'],
                               cwd=build_root, env=target_env, check=True)
                expected_binary_sha256 = digest((package / binary).read_bytes())
                for name, path in DOCUMENTS.items():
                    shutil.copyfile(build_root / path, package / name)
                subprocess.run(['python3', str(build_root / 'scripts/package-release.py'), str(package), str(destination),
                                '--source', str(resolved), '--os', target_os, '--arch', target_arch,
                                '--expected-binary-sha256', expected_binary_sha256],
                               cwd=build_root, env=env, check=True)
            sets.append(destination)
        validate_source(repo, source)
        manifest = seal(*sets, source)
        verify(sets[0], source)
        # Rename a complete set staged on the destination filesystem.
        publish_stage = Path(tempfile.mkdtemp(prefix=f'.{output.name}-', dir=output.parent))
        try:
            for path in sets[0].iterdir():
                shutil.copyfile(path, publish_stage / path.name)
            verify(publish_stage, source)
            if output.exists():
                output.rmdir()  # Refuses any files created while building.
            publish_stage.rename(output)
        finally:
            if publish_stage.exists():
                shutil.rmtree(publish_stage)
        print(f"{'Nonpublishable candidate' if not source['publishable'] else 'Release'} archives: {output}")
        print(f"SOURCE.json SHA256: {digest(canonical(source))}")
        print(f"MANIFEST.json SHA256: {digest(canonical(manifest))}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    resolver = commands.add_parser('resolve', help='Resolve once; output outside checkout.')
    resolver.add_argument('--repo', type=Path, required=True)
    mode = resolver.add_mutually_exclusive_group(required=True)
    mode.add_argument('--release')
    mode.add_argument('--candidate')
    resolver.add_argument('--output', type=Path, required=True)
    validator = commands.add_parser('verify-source')
    validator.add_argument('--repo', type=Path, required=True)
    validator.add_argument('--source', type=Path, required=True)
    validator.add_argument('--publishable', action='store_true')
    verifier = commands.add_parser('verify', help='Verify against independently supplied source digest.')
    verifier.add_argument('--directory', type=Path, required=True)
    verifier.add_argument('--source', type=Path, required=True)
    verifier.add_argument('--expected-source-sha256', required=True)
    verifier.add_argument('--expected-manifest-sha256', required=True)
    verifier.add_argument('--repo', type=Path)
    verifier.add_argument('--publishable', action='store_true')
    builder = commands.add_parser('build')
    builder.add_argument('--repo', type=Path, required=True)
    mode = builder.add_mutually_exclusive_group()
    mode.add_argument('--candidate')
    mode.add_argument('--source', type=Path)
    builder.add_argument('--expected-source-sha256')
    builder.add_argument('arguments', nargs='*', help='[release-tag] [output-directory]; output outside checkout.')
    args = parser.parse_args()
    if args.command == 'resolve':
        outside_repo(args.output, args.repo)
        source = immutable_source(args.repo, 'candidate' if args.candidate else 'release', args.candidate or args.release)
        validate_source(args.repo, source)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with args.output.open('xb') as out:
            out.write(canonical(source))
        print(digest(canonical(source)))
    elif args.command == 'verify-source':
        source = load_source(args.source, args.repo)
        require(not args.publishable or source['mode'] == 'release' and source['publishable'] is True,
                'Candidate source cannot be published.')
        print(digest(canonical(source)))
    elif args.command == 'verify':
        source = load_source(args.source, args.repo, args.expected_source_sha256)
        require(HASH.fullmatch(args.expected_manifest_sha256)
                and digest((args.directory / 'MANIFEST.json').read_bytes()) == args.expected_manifest_sha256,
                'MANIFEST.json digest mismatch.')
        manifest = verify(args.directory, source, publishable=args.publishable)
        print(digest(canonical(manifest)))
    else:
        limit = 1 if args.candidate or args.source else 2
        require(len(args.arguments) <= limit, 'Unexpected build arguments.')
        version = None if args.candidate or args.source else (args.arguments[0] if args.arguments else None)
        output = (args.arguments[-1] if len(args.arguments) == limit else None)
        build(args.repo, version, args.candidate, args.source, output, args.expected_source_sha256)


if __name__ == '__main__':
    try:
        main()
    except (InvalidRelease, OSError, subprocess.CalledProcessError, ValueError, KeyError, TypeError,
            tarfile.TarError, zipfile.BadZipFile, struct.error) as error:
        print(f'Release verification failed: {error}', file=sys.stderr)
        sys.exit(1)
