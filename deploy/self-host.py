#!/usr/bin/env python3
"""Prepare/manage one Linux public profile; all lifecycle calls retain HTTPS.

Builds require a clean immutable checkout. State and credentials live outside
Git. No registry publication, volume removal, firewall installation or Caddy
configuration is implicit. Run egress-policy.py explicitly before starting apps.
"""
from __future__ import annotations

import argparse
import datetime
import gzip
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import secrets
import shutil
import sys
import tarfile
import tempfile

# Lifecycle entrypoints can run from the sealed immutable source snapshot.
# Imports must not add untracked __pycache__ files to that inventory.
sys.dont_write_bytecode = True

from public_common import (PROFILE, Refused, canonical, compose, digest, json_command, local_engine,
                           parse_config, private_directory, private_read, private_write, require,
                           run, runtime_environment, project_lock)

REPO = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location('release_manifest', REPO / 'scripts/release_manifest.py')
manifest = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(manifest)
PUBLIC_KEYS = ('CUTMY_PROJECT', 'CUTMY_DOMAIN', 'CUTMY_HTTP_PORT', 'CUTMY_DATABASE_PREFIX', 'CUTMY_PROCESSING_PREFIX')


def config_text(values):
    return ''.join(key + '=' + values[key] + '\n' for key in sorted(values)).encode()


def state_snapshot(state, source):
    directory = state / 'source'
    require(directory.is_dir() and not directory.is_symlink(), 'immutable_snapshot_missing')
    actual = set()
    for path in directory.rglob('*'):
        require(not path.is_symlink(), 'snapshot_symlink_refused')
        if path.is_file():
            relative = path.relative_to(directory).as_posix()
            require(relative in source['files'] and digest(path.read_bytes()) == source['files'][relative]['sha256'],
                    'immutable_snapshot_changed')
            actual.add(relative)
    require(actual == set(source['files']), 'immutable_snapshot_incomplete')
    return directory


def expected_version(source):
    return {'version': source['version'], 'commit': source['commit'], 'build_time': source['build_time'],
            'go_version': source['toolchain'], 'os': 'linux', 'arch': 'amd64'}


def image_verify(image, source):
    require(re.fullmatch(r'sha256:[0-9a-f]{64}', image), 'image_id_invalid')
    result = json_command(['docker', 'image', 'inspect', image])
    require(len(result) == 1, 'image_identity_missing')
    info = result[0]
    require(info['Id'] == image and info.get('Os') == 'linux' and info.get('Architecture') == 'amd64'
            and info['Config'].get('User') in ('node', '1000', '1000:1000'), 'image_platform_or_user_invalid')
    labels = info['Config'].get('Labels', {})
    require(all(labels.get(key) == value for key, value in {
        'org.opencontainers.image.version': source['version'], 'org.opencontainers.image.revision': source['commit'],
        'org.opencontainers.image.created': source['build_time'],
        'org.opencontainers.image.source': 'https://github.com/Kay0k1/cutmyvideo-core'}.items()), 'image_labels_differ')
    version = json.loads(run(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--user', '1000:1000',
                             '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '256m',
                             '--cpus', '1', '--pids-limit', '32', image, 'version']))
    require(version == expected_version(source), 'actual_image_binary_identity_differ')
    return version


def load_state(state, config, verify_image=True):
    state = private_directory(state)
    record = json.loads(private_read(state / 'image.json'))
    source_raw = private_read(state / 'SOURCE.json')
    source = json.loads(source_raw)
    manifest.validate_source_shape(source)
    require(source_raw == manifest.canonical(source) and record['source_sha256'] == digest(source_raw)
            and record['profile'] == PROFILE and record['source_commit'] == source['commit']
            and record['configuration'] == {k: config[k] for k in PUBLIC_KEYS}, 'state_identity_differ')
    snapshot = state_snapshot(state, source)
    env_file = state / 'compose.env'
    require(private_read(env_file, 8192) == config_text(runtime_environment(config, record['image_id'])),
            'private_configuration_differ')
    if verify_image:
        image_verify(record['image_id'], source)
    return source, record, snapshot, env_file


def save_state(state, config, source, image, tools, snapshot_exists=False):
    if not snapshot_exists:
        manifest.materialize_source(REPO, source, state / 'source')
    state_snapshot(state, source)
    private_write(state / 'SOURCE.json', manifest.canonical(source))
    private_write(state / 'compose.env', config_text(runtime_environment(config, image)))
    private_write(state / 'image.json', canonical({
        'schema_version': 1, 'profile': PROFILE, 'source_sha256': digest(manifest.canonical(source)),
        'source_commit': source['commit'], 'image_id': image, 'runtime_version': expected_version(source),
        'configuration': {k: config[k] for k in PUBLIC_KEYS}, 'tools': tools,
        'image_byte_reproducibility_claimed': False,
        'recorded_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}))


def build(state, config, revision, release):
    require(re.fullmatch(r'[0-9a-f]{40}', revision), 'full_source_sha_required')
    source = manifest.immutable_source(REPO, 'release' if release else 'candidate', release or revision)
    require(source['commit'] == revision, 'release_tag_does_not_match_revision')
    manifest.validate_source(REPO, source)
    state = private_directory(state, create=True)
    try:
        snapshot = manifest.materialize_source(REPO, source, state / 'source')
        iid = state / 'built-image-id'
        run(['docker', 'build', '--platform', 'linux/amd64', '--iidfile', iid,
             '--build-arg', 'VERSION=' + source['version'], '--build-arg', 'VCS_REF=' + source['commit'],
             '--build-arg', 'BUILD_TIME=' + source['build_time'], snapshot], timeout=1800)
        image = iid.read_text().strip()
        image_verify(image, source)
        tools_code = ('import json,subprocess; print(json.dumps({k:subprocess.check_output(v,stderr=subprocess.STDOUT)'
                      '.decode().splitlines()[0] for k,v in {"ffmpeg":["ffmpeg","-version"],'
                      '"ffprobe":["ffprobe","-version"],"yt_dlp":["yt-dlp","--version"],'
                      '"node":["node","--version"]}.items()}))')
        tools = json.loads(run(['docker', 'run', '--rm', '--network', 'none', '--read-only', '--user', '1000:1000',
                               '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '256m',
                               '--pids-limit', '32', '--entrypoint', 'python3', image, '-c', tools_code]))
        manifest.validate_source(REPO, source)
        save_state(state, config, source, image, tools, snapshot_exists=True)
    except BaseException:
        # Preserve only our failed build state for diagnosis; never erase an
        # image or someone else's installation to make a retry appear clean.
        raise


def containers(config):
    ids = run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + config['CUTMY_PROJECT']]).decode().split()
    require(all(re.fullmatch(r'[0-9a-f]{12,64}', cid) for cid in ids), 'container_identity_invalid')
    return json_command(['docker', 'inspect', *ids]) if ids else []


def no_writers(config):
    for row in containers(config):
        labels = row.get('Config', {}).get('Labels', {})
        role = labels.get('com.docker.compose.service')
        require(role in ('postgres', 'api', 'worker', 'migrate', 'maintenance'), 'foreign_project_container')
        if role != 'postgres':
            require(row['State'].get('Running') is False, 'application_writers_must_be_stopped')


def postgres_sql(command, snapshot, path):
    sql = (snapshot / path).read_bytes()
    run(command + ['exec', '-T', 'postgres', 'psql', '-X', '-q', '-v', 'ON_ERROR_STOP=1',
                   '-U', 'cutmy_admin', '-d', 'cutmy'], input=sql)


def verify_policy(snapshot, config_path):
    run([sys.executable, '-B', snapshot / 'deploy/egress-policy.py', '--config', config_path, 'verify'])


def initialize(command, config, snapshot, config_path):
    validate_volumes(config)
    no_writers(config)
    verify_policy(snapshot, config_path)
    run(command + ['up', '-d', '--wait', 'postgres'])
    run(command + ['run', '--rm', '-T', '--no-deps', 'migrate'], timeout=180)
    postgres_sql(command, snapshot, 'deploy/public-grants.sql')


def checked_up(command, config, snapshot, config_path):
    validate_volumes(config)
    verify_policy(snapshot, config_path)
    try:
        run(command + ['up', '-d', '--wait', 'api', 'worker'], timeout=180)
        verify_policy(snapshot, config_path)
    except BaseException:
        run(command + ['stop', 'api', 'worker'])
        raise


def volume(config, kind):
    name = config['CUTMY_PROJECT'] + '_' + kind
    rows = json_command(['docker', 'volume', 'inspect', name])
    require(len(rows) == 1 and rows[0]['Name'] == name and rows[0].get('Driver') == 'local'
            and rows[0].get('Labels', {}).get('video.cutmy.profile') == PROFILE
            and rows[0].get('Labels', {}).get('video.cutmy.project') == config['CUTMY_PROJECT']
            and rows[0].get('Labels', {}).get('com.docker.compose.project') == config['CUTMY_PROJECT']
            and rows[0].get('Labels', {}).get('com.docker.compose.volume') == kind
            and not rows[0].get('Options'),
            'foreign_or_unsupported_volume')
    return name


def validate_volumes(config, *, allow_missing=False):
    names = set(run(['docker', 'volume', 'ls', '--format', '{{.Name}}']).decode().splitlines())
    for kind in ('media', 'postgres'):
        if config['CUTMY_PROJECT'] + '_' + kind in names:
            volume(config, kind)
        else:
            require(allow_missing, 'owned_persistent_volume_missing')


def prepare(command, config):
    no_writers(config)
    # Compose's varying warnings about preexisting explicit names are not an
    # ownership fence. Refuse foreign data before creating/mounting anything.
    validate_volumes(config, allow_missing=True)
    run(command + ['create', 'postgres', 'api', 'worker'])
    validate_volumes(config)


def maintenance(command, config, snapshot, config_path):
    validate_volumes(config)
    no_writers(config)
    verify_policy(snapshot, config_path)
    run(command + ['run', '--rm', '-T', '--no-deps', 'maintenance'], timeout=60)


def tar_command(image, media, *, restore=False):
    return ['docker', 'run', '--rm', '-i', '--network', 'none', '--read-only', '--user', '1000:1000',
            '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges:true', '--memory', '256m',
            '--cpus', '1', '--pids-limit', '32', '--mount', 'type=volume,src=' + media + ',dst=/data' + ('' if restore else ',readonly'),
            '--entrypoint', 'tar', image, '-C', '/data'] + (
                ['-xzf', '-', '--no-same-owner', '--no-same-permissions'] if restore else ['--format=gnu', '-czf', '-', '.'])


def file_digest(path):
    import hashlib
    result = hashlib.sha256()
    with Path(path).open('rb') as stream:
        while chunk := stream.read(1024 * 1024):
            result.update(chunk)
    return result.hexdigest()


def backup(state, config, source, record, command, destination):
    destination = private_directory(destination, create=True)
    validate_volumes(config)
    run(command + ['stop', 'api', 'worker'])
    no_writers(config)
    media = volume(config, 'media')
    volume(config, 'postgres')
    for name, argv in (
        ('database.dump', command + ['exec', '-T', 'postgres', 'pg_dump', '-U', 'cutmy_migrate', '-d', 'cutmy', '-Fc', '--no-owner', '--no-acl']),
        ('media.tar.gz', tar_command(record['image_id'], media))):
        with (destination / name).open('xb') as output:
            os.chmod(destination / name, 0o600)
            run(argv, output=output, timeout=1800)
            output.flush()
            os.fsync(output.fileno())
        require((destination / name).stat().st_size > 0, 'backup_file_empty')
    no_writers(config)
    private_write(destination / 'backup.json', canonical({
        'schema_version': 1, 'profile': PROFILE, 'writers_stopped': True, 'data_dir': '/data',
        'source_sha256': record['source_sha256'], 'source_commit': source['commit'], 'image_id': record['image_id'],
        'files': {name: {'sha256': file_digest(destination / name), 'bytes': (destination / name).stat().st_size}
                  for name in ('database.dump', 'media.tar.gz')},
        'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat()}))
    # Remain stopped even on success. Starting traffic is a separate checked up.


def preflight_media_tar(path):
    # Bound GNU long-name records before tarfile can allocate their payload.
    # The backup producer explicitly chooses GNU format; PAX/sparse formats
    # are refused rather than interpreted as arbitrary restored metadata.
    total, headers, zeros = 0, 0, 0
    with gzip.open(path, 'rb') as stream:
        while True:
            header = stream.read(512)
            if not header:
                require(zeros >= 2, 'media_tar_end_marker_missing')
                break
            total += len(header)
            require(len(header) == 512 and total <= 34 * 1024**3, 'media_tar_block_bounds')
            if header == bytes(512):
                zeros += 1
                continue
            require(zeros == 0, 'media_tar_trailing_nonzero_content')
            headers += 1
            require(headers <= 2000000, 'media_tar_header_count')
            checksum = int(header[148:156].strip(b'\x00 '), 8)
            require(sum(header[:148]) + 8 * 32 + sum(header[156:]) == checksum, 'media_tar_header_checksum')
            field = header[124:136].strip(b'\x00 ')
            require(field and all(c in b'01234567' for c in field), 'media_tar_size_encoding')
            size = int(field, 8)
            kind = header[156:157]
            require(kind in (b'0', b'\x00', b'1', b'5', b'L', b'K'), 'media_tar_format_refused')
            require(size <= (4096 if kind in (b'L', b'K') else 2 * 1024**3), 'media_tar_record_too_large')
            remaining = (size + 511) // 512 * 512
            while remaining:
                chunk = stream.read(min(remaining, 1024 * 1024))
                require(chunk, 'media_tar_payload_truncated')
                total += len(chunk)
                require(total <= 34 * 1024**3, 'media_tar_expansion_limit')
                remaining -= len(chunk)


def validate_media_archive(path):
    preflight_media_tar(path)
    files, total, seen = 0, 0, {}
    with tarfile.open(path, mode='r|gz') as archive:
        for member in archive:
            pure = PurePosixPath(member.name)
            require(not pure.is_absolute() and '..' not in pure.parts and '\\' not in member.name
                    and len(member.name.encode()) <= 4096, 'unsafe_media_archive_path')
            normalized = str(pure)
            require(normalized not in seen and member.mode & 0o7000 == 0, 'duplicate_or_privileged_media_member')
            require(member.isfile() or member.isdir() or member.islnk(), 'media_archive_special_member_refused')
            if member.islnk():
                target = str(PurePosixPath(member.linkname))
                require(target in seen and seen[target] == 'file', 'unsafe_media_hardlink')
            require(0 <= member.size <= 2 * 1024**3, 'media_member_too_large')
            total += member.size
            files += 1
            require(files <= 1000000 and total <= 32 * 1024**3, 'media_archive_bounds_exceeded')
            seen[normalized] = 'file' if member.isfile() else 'other'
    require(files > 0, 'empty_media_archive')


def fresh_project(config):
    require(not containers(config), 'restore_requires_fresh_project')
    for kind in ('media', 'postgres'):
        names = run(['docker', 'volume', 'ls', '--format', '{{.Name}}']).decode().splitlines()
        require(config['CUTMY_PROJECT'] + '_' + kind not in names, 'restore_volume_already_exists')
    names = run(['docker', 'network', 'ls', '--format', '{{.Name}}']).decode().splitlines()
    require(not any(config['CUTMY_PROJECT'] + '_' + kind in names for kind in ('database', 'processing')),
            'restore_network_already_exists')


def restore(config, source, record, command, snapshot, destination):
    destination = private_directory(destination)
    backup_record = json.loads(private_read(destination / 'backup.json'))
    require(backup_record['profile'] == PROFILE and backup_record['schema_version'] == 1
            and backup_record['writers_stopped'] is True and backup_record['data_dir'] == '/data'
            and backup_record['source_sha256'] == record['source_sha256'] and backup_record['image_id'] == record['image_id']
            and backup_record['source_commit'] == source['commit'], 'backup_identity_differ')
    for name in ('database.dump', 'media.tar.gz'):
        path = destination / name
        require(path.is_file() and not path.is_symlink() and path.stat().st_mode & 0o077 == 0
                and path.stat().st_size == backup_record['files'][name]['bytes'] > 0
                and file_digest(path) == backup_record['files'][name]['sha256'], 'backup_content_differ')
    validate_media_archive(destination / 'media.tar.gz')
    fresh_project(config)
    run(command + ['create', 'postgres', 'api', 'worker'])
    validate_volumes(config)
    run(command + ['up', '-d', '--wait', 'postgres'])
    with (destination / 'database.dump').open('rb') as stream:
        # Pass the descriptor as stdin rather than loading an entire dump.
        import subprocess
        from public_common import command_environment
        result = subprocess.run(command + ['exec', '-T', 'postgres', 'pg_restore', '--exit-on-error', '--no-owner',
                                          '--no-privileges', '-U', 'cutmy_migrate', '-d', 'cutmy'], stdin=stream,
                                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=command_environment(), timeout=1800)
        require(result.returncode == 0, 'database_restore_failed')
    media = volume(config, 'media')
    with (destination / 'media.tar.gz').open('rb') as stream:
        result = subprocess.run(tar_command(record['image_id'], media, restore=True), stdin=stream,
                                stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, env=command_environment(), timeout=1800)
        require(result.returncode == 0, 'media_restore_failed')
    # No app/migration container starts before the fresh project's explicitly
    # applied policy. Operator runs initialize after apply, then checked up.


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--state', type=Path, required=True)
    commands = parser.add_subparsers(dest='command', required=True)
    commands.add_parser('init-config')
    b = commands.add_parser('build'); b.add_argument('--revision', required=True); b.add_argument('--release')
    a = commands.add_parser('adopt-image'); a.add_argument('--from-state', type=Path, required=True)
    for name in ('prepare', 'initialize', 'up', 'stop', 'status', 'maintenance'):
        commands.add_parser(name)
    for name in ('backup', 'restore'):
        command = commands.add_parser(name); command.add_argument('--directory', type=Path, required=True)
    args = parser.parse_args()
    manifest.outside_repo(args.state, REPO); manifest.outside_repo(args.config, REPO)
    if args.command == 'init-config':
        private_write(args.config, config_text({'CUTMY_PROJECT': 'cutmy_public', 'CUTMY_DOMAIN': 'video.example.org',
            'CUTMY_HTTP_PORT': '8080', 'CUTMY_DATABASE_PREFIX': '172.30.88', 'CUTMY_PROCESSING_PREFIX': '172.30.89',
            **{key: secrets.token_urlsafe(48) for key in ('CUTMY_ADMIN_PASSWORD', 'CUTMY_MIGRATION_PASSWORD', 'CUTMY_APP_PASSWORD')}}))
        print('Private configuration created; review hostname, unused subnets and loopback port.')
        return
    config = parse_config(private_read(args.config, 8192))
    local_engine()
    if args.command == 'build':
        build(args.state, config, args.revision, args.release)
        print('Immutable image state recorded: ' + str(args.state))
        return
    if args.command == 'adopt-image':
        old = private_directory(args.from_state)
        old_record = json.loads(private_read(old / 'image.json'))
        source = json.loads(private_read(old / 'SOURCE.json'))
        manifest.validate_source_shape(source)
        require(old_record['source_sha256'] == digest(manifest.canonical(source)), 'adopted_source_differ')
        snapshot = state_snapshot(old, source)
        image_verify(old_record['image_id'], source)
        state = private_directory(args.state, create=True)
        shutil.copytree(snapshot, state / 'source')
        save_state(state, config, source, old_record['image_id'], old_record['tools'], snapshot_exists=True)
        print('Existing verified image adopted; no new image build performed.')
        return
    source, record, snapshot, env_file = load_state(args.state, config)
    command = compose(snapshot, env_file, config)
    with project_lock(config):
        if args.command == 'prepare':
            prepare(command, config)
        elif args.command == 'initialize':
            initialize(command, config, snapshot, args.config)
        elif args.command == 'up':
            checked_up(command, config, snapshot, args.config)
        elif args.command == 'stop':
            run(command + ['stop', 'api', 'worker'])
            no_writers(config)
        elif args.command == 'status':
            # Compose ps prints service state only, not interpolated credentials.
            print(run(command + ['ps']).decode(), end='')
        elif args.command == 'maintenance':
            maintenance(command, config, snapshot, args.config)
        elif args.command == 'backup':
            backup(args.state, config, source, record, command, args.directory)
            print('Coordinated backup complete; writers remain stopped.')
        elif args.command == 'restore':
            restore(config, source, record, command, snapshot, args.directory)
            print('Fresh-project files restored; apply policy, then initialize before checked up.')


if __name__ == '__main__':
    try:
        main()
    except (Refused, manifest.InvalidRelease, OSError, ValueError, KeyError, TypeError, tarfile.TarError):
        print('Public installation operation refused; check prerequisites and private configuration.', file=sys.stderr)
        raise SystemExit(1)
