#!/usr/bin/env python3
"""Read-only draft transport and native smoke; reuse the signed consumer protocol.

No build, tag, publication, database, media dependency or application service.
Partial provider bytes and command results are retained on the first refusal.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

import release_manifest as manifest
from release_consumer_process import WindowsJob

HERE = Path(__file__).resolve().parent
REPOSITORY = 'Kay0k1/cutmyvideo-core'
API = 'https://api.github.com/repos/' + REPOSITORY
METADATA_LIMIT = 2 * 1024 * 1024
ASSET_TOTAL_LIMIT = 512 * 1024 * 1024
STDERR_LIMIT = 128 * 1024
TOTAL_SECONDS = 1500
MINIMUM_FREE = 2 * 1024 * 1024 * 1024
INTERRUPTED = False


class Refused(ValueError):
    pass


def require(condition, code):
    if not condition:
        raise Refused(code)


def load_tool(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def file_identity(path):
    digest, size = hashlib.sha256(), 0
    with path.open('rb') as stream:
        remaining = os.fstat(stream.fileno()).st_size
        expected = remaining
        while remaining:
            block = stream.read(min(1024 * 1024, remaining))
            require(bool(block), 'file_short_read')
            digest.update(block)
            size += len(block)
            remaining -= len(block)
        require(os.fstat(stream.fileno()).st_size == expected, 'file_changed')
    return {'bytes': size, 'sha256': digest.hexdigest()}


def exclusive(path, value):
    with path.open('xb') as stream:
        os.chmod(path, 0o600)
        stream.write(manifest.canonical(value))


def command_env(private, authenticated):
    # Do not inherit debug/config/credential overrides or unrelated operator secrets.
    keys = ('PATH', 'HOME', 'USERPROFILE', 'SystemRoot', 'WINDIR', 'COMSPEC', 'PATHEXT',
            'TEMP', 'TMP', 'TMPDIR', 'LANG', 'LC_ALL', 'SSL_CERT_FILE', 'SSL_CERT_DIR')
    env = {key: os.environ[key] for key in keys if key in os.environ}
    env.update(GH_HOST='github.com', GH_PROMPT_DISABLED='1', GH_CONFIG_DIR=str(private / 'gh-config'),
               GIT_TERMINAL_PROMPT='0', GIT_CONFIG_NOSYSTEM='1', GIT_CONFIG_GLOBAL=os.devnull,
               GIT_CONFIG_SYSTEM=os.devnull, GIT_CONFIG_COUNT='0',
               GIT_CEILING_DIRECTORIES=os.pathsep.join((str(private), str(private.parent))))
    if authenticated:
        require(bool(os.environ.get('GH_TOKEN')), 'read_token_missing')
        env['GH_TOKEN'] = os.environ['GH_TOKEN']
    return env


def capture(argv, evidence, name, private, deadline, *, limit, seconds=60,
            authenticated=False, stdout_path=None):
    """Observed file caps plus a finite watchdog; kill/reap only the owned child.

    Files can cross a cap between polls. Such original bytes remain a refusal,
    not an instantaneous disk-quota claim. No command is retried.
    """
    stop = min(deadline, time.monotonic() + seconds)
    require(stop > time.monotonic(), 'total_deadline')
    stdout_path = stdout_path or evidence / (name + '.stdout.bin')
    stderr_path = evidence / (name + '.stderr.bin')
    invocation = {'argv': argv, 'stdout': str(stdout_path.relative_to(evidence)),
                  'stderr': stderr_path.name, 'stdout_limit': limit,
                  'stderr_limit': STDERR_LIMIT, 'allocated_seconds': seconds}
    exclusive(evidence / (name + '.invocation.json'), invocation)
    started, process, job = time.monotonic(), None, None
    result = {'spawned': False, 'pid': None, 'exit_code': None, 'reaped': False,
              'contained_cleanup': False, 'timeout': False, 'overflow': False,
              'error_class': None, 'cleanup_error_class': None, 'record_error_class': None}
    try:
        with stdout_path.open('xb') as out, stderr_path.open('xb') as err:
            os.chmod(stdout_path, 0o600)
            os.chmod(stderr_path, 0o600)
            try:
                options = {'creationflags': 0x4} if os.name == 'nt' else {'start_new_session': True}
                if os.name == 'nt':
                    job = WindowsJob()
                process = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=out, stderr=err,
                                           cwd=private, env=command_env(private, authenticated), **options)
                result.update(spawned=True, pid=process.pid)
                if job:
                    job.attach_and_resume(process)
                while True:
                    result['overflow'] = os.fstat(out.fileno()).st_size > limit or os.fstat(err.fileno()).st_size > STDERR_LIMIT
                    result['timeout'] = time.monotonic() >= stop
                    if result['overflow'] or result['timeout']:
                        break
                    code = process.poll()
                    if code is not None:
                        result['exit_code'] = code
                        break
                    time.sleep(0.02)
            except (OSError, ValueError, subprocess.SubprocessError) as error:
                result['error_class'] = type(error).__name__
            finally:
                try:
                    if job:
                        job.close(process)
                        result['contained_cleanup'] = True
                    elif process:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        process.wait(timeout=5)
                        until = time.monotonic() + 5
                        while True:
                            try:
                                os.killpg(process.pid, 0)
                            except ProcessLookupError:
                                result['contained_cleanup'] = True
                                break
                            require(time.monotonic() < until, 'child_group_cleanup')
                            time.sleep(0.02)
                    if process:
                        result.update(exit_code=process.returncode, reaped=process.returncode is not None)
                except (OSError, ValueError, subprocess.SubprocessError) as error:
                    result['cleanup_error_class'] = type(error).__name__
    finally:
        result['elapsed_seconds'] = time.monotonic() - started
        for label, path in (('stdout', stdout_path), ('stderr', stderr_path)):
            try:
                result[label] = dict(path=str(path.relative_to(evidence)), **file_identity(path)) if path.exists() else None
            except (OSError, ValueError) as error:
                result['record_error_class'] = result['record_error_class'] or type(error).__name__
                result[label] = {'path': str(path.relative_to(evidence)), 'bytes': None, 'sha256': None}
        if result['stdout'] and result['stderr']:
            result['overflow'] = result['overflow'] or any(
                row['bytes'] is not None and row['bytes'] > cap
                for row, cap in ((result['stdout'], limit), (result['stderr'], STDERR_LIMIT)))
        exclusive(evidence / (name + '.result.json'), result)
    require(result['exit_code'] == 0 and result['reaped'] and result['contained_cleanup']
            and not any(result[k] for k in ('timeout', 'overflow', 'error_class', 'cleanup_error_class', 'record_error_class')),
            'command_refused')
    return stdout_path


def validate_metadata(value, version, release_id, expected_names):
    require(type(value) is dict and value.get('id') == release_id and type(value.get('id')) is int
            and value.get('tag_name') == version and value.get('draft') is True
            and value.get('prerelease') is ('-' in version) and value.get('published_at') is None,
            'draft_release_identity')
    assets = value.get('assets')
    require(type(assets) is list and len(assets) == 13, 'release_asset_count')
    seen_ids, records = set(), {}
    for asset in assets:
        require(type(asset) is dict, 'asset_metadata')
        name, identifier = asset.get('name'), asset.get('id')
        require(type(name) is str and name in expected_names and name not in records
                and type(identifier) is int and identifier > 0 and identifier not in seen_ids,
                'release_asset_identity')
        size, digest = asset.get('size'), asset.get('digest')
        cap = 128 * 1024 * 1024 if name.endswith(('.tar.gz', '.zip')) else 16 * 1024 * 1024
        require(type(size) is int and 0 < size <= cap and type(digest) is str
                and digest.startswith('sha256:') and manifest.HASH.fullmatch(digest[7:])
                and asset.get('state') == 'uploaded' and asset.get('url') == API + '/releases/assets/' + str(identifier),
                'release_asset_digest_or_size')
        seen_ids.add(identifier)
        records[name] = {'id': identifier, 'bytes': size, 'sha256': digest[7:]}
    require(set(records) == expected_names and sum(row['bytes'] for row in records.values()) <= ASSET_TOTAL_LIMIT,
            'release_asset_set_or_total')
    return {'id': release_id, 'version': version, 'draft': True, 'prerelease': value['prerelease'],
            'target_commitish': value.get('target_commitish'), 'assets': records}


def workflow_identity(version, commit):
    require(os.environ.get('GITHUB_ACTIONS') == 'true' and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted'
            and os.environ.get('GITHUB_EVENT_NAME') == 'workflow_dispatch'
            and os.environ.get('GITHUB_REPOSITORY') == REPOSITORY
            and os.environ.get('GITHUB_REF') == 'refs/tags/' + version
            and os.environ.get('GITHUB_SHA') == commit and os.environ.get('GITHUB_WORKFLOW_SHA') == commit
            and os.environ.get('GITHUB_WORKFLOW_REF') == REPOSITORY + '/.github/workflows/release-consumer.yml@refs/tags/' + version
            and os.environ.get('GITHUB_RUN_ATTEMPT') == '1'
            and os.environ.get('GITHUB_RUN_ID', '').isdigit() and int(os.environ['GITHUB_RUN_ID']) > 0,
            'reviewed_workflow_identity')
    return {key: os.environ[key] for key in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT', 'GITHUB_SHA', 'GITHUB_WORKFLOW_SHA')}


def interrupt(signum, frame):
    global INTERRUPTED
    INTERRUPTED = True
    raise Refused('interrupted')


def run(args):
    manifest.release_version(args.version)
    require(manifest.SHA.fullmatch(args.expected_commit) and manifest.HASH.fullmatch(args.source_sha256)
            and manifest.HASH.fullmatch(args.manifest_sha256) and args.release_id > 0, 'expected_identity')
    context = workflow_identity(args.version, args.expected_commit)
    evidence = args.evidence.absolute()
    require(not evidence.exists() and not evidence.is_symlink() and evidence.parent.is_dir()
            and evidence.resolve() == evidence and not evidence.is_relative_to(HERE.parent), 'fresh_evidence')
    evidence.mkdir(mode=0o700)
    (evidence / 'assets').mkdir(mode=0o700)
    deadline = time.monotonic() + TOTAL_SECONDS
    report = {'schema_version': 1, 'pass': False, 'failure': None, 'private_cleanup': True,
              'repository': REPOSITORY, 'version': args.version, 'expected_commit': args.expected_commit,
              'release_id': args.release_id, 'source_sha256': args.source_sha256,
              'manifest_sha256': args.manifest_sha256, 'run': context,
              'target': {'os': args.target_os, 'arch': args.target_arch},
              'total_seconds': TOTAL_SECONDS, 'asset_total_limit': ASSET_TOTAL_LIMIT,
              'scope': 'Actual draft downloads, existing signed consumer verification and selected native version only; no media/recovery replay or build.'}
    stage, private = 'prepare', None
    try:
        import shutil
        require(shutil.disk_usage(evidence.parent).free >= MINIMUM_FREE, 'free_disk_admission')
        consumer = load_tool('download_consumer', 'release-consumer.py')
        native = load_tool('download_native', 'release-native.py')
        require(native.native_target() == (args.target_os, args.target_arch), 'native_target')
        private = tempfile.TemporaryDirectory(prefix='cutmy-signed-consumer-')
        scratch = Path(private.name)
        (scratch / 'gh-config').mkdir(mode=0o700)
        counter = 0
        def command(argv, *, limit=METADATA_LIMIT, seconds=60, authenticated=False, destination=None):
            nonlocal counter
            counter += 1
            return capture(argv, evidence, 'command-%03d' % counter, scratch, deadline, limit=limit,
                           seconds=seconds, authenticated=authenticated, stdout_path=destination)
        stage = 'tools'
        for label, argv in (('python', [sys.executable, '--version']), ('git', ['git', '--version']), ('gh', ['gh', '--version'])):
            path = command(argv, limit=65536, seconds=30)
            report.setdefault('tools', {})[label] = dict(path=path.name, **file_identity(path))
        stage = 'checkout'
        head = command(['git', '-C', str(HERE.parent), 'rev-parse', 'HEAD'], limit=4096, seconds=30)
        require(head.read_bytes().decode('ascii').strip() == args.expected_commit, 'checkout_commit')
        expected_names = consumer.asset_names(args.version)
        metadata_command = ['gh', 'api', 'repos/' + REPOSITORY + '/releases/' + str(args.release_id)]
        stage = 'metadata_before'
        before = command(metadata_command, authenticated=True)
        metadata = validate_metadata(consumer.plain_json(before.read_bytes(), METADATA_LIMIT), args.version, args.release_id, expected_names)
        report['metadata_before'] = dict(path=before.name, **file_identity(before))
        stage = 'downloads'
        for name, asset in sorted(metadata['assets'].items()):
            path = command(['gh', 'api', 'repos/' + REPOSITORY + '/releases/assets/' + str(asset['id']),
                            '-H', 'Accept: application/octet-stream'], limit=asset['bytes'], authenticated=True,
                           destination=evidence / 'assets' / name)
            require(file_identity(path) == {'bytes': asset['bytes'], 'sha256': asset['sha256']}, 'download_bytes')
        assets = evidence / 'assets'
        require(file_identity(assets / 'SOURCE.json')['sha256'] == args.source_sha256
                and file_identity(assets / 'MANIFEST.json')['sha256'] == args.manifest_sha256, 'reviewed_source_and_manifest')
        stage = 'signed_consumer'
        # Reuse every verification rule. Only replace its command transport so
        # original Git/gh stdout, stderr and owned-process results survive failure.
        # Running the verifier in this process also lets handled interruption
        # close its exact active child; no nested independent Unix session escapes.
        original_output = consumer.command_output
        def recorded_output(argv, limit, verification_deadline=None):
            nonlocal counter
            counter += 1
            stop = min(deadline, verification_deadline) if verification_deadline is not None else deadline
            path = capture(argv, evidence, 'command-%03d' % counter, scratch, stop,
                           limit=limit, seconds=60, authenticated=True)
            return path.read_bytes()
        try:
            consumer.command_output = recorded_output
            verified = consumer.verify(assets, args.expected_commit, args.version)
        finally:
            consumer.command_output = original_output
        verified_path = evidence / 'consumer-verification.json'
        exclusive(verified_path, verified)
        require(verified.get('ok') is True and verified.get('source_commit') == args.expected_commit
                and verified.get('version') == args.version and verified.get('repository') == REPOSITORY
                and verified.get('assets') == {name: item['sha256'] for name, item in metadata['assets'].items()}
                and verified.get('provenance_verified_subjects') == 7 and verified.get('spdx_verified_subjects') == 5,
                'signed_consumer_result')
        report['consumer'] = dict(path=verified_path.name, **file_identity(verified_path))
        stage = 'native_smoke'
        source = consumer.control(assets, 'SOURCE.json')
        archive = manifest.archive_name(source, args.target_os, args.target_arch)
        contents = manifest.read_archive(assets / archive, source, args.target_os, args.target_arch)
        name = 'cutmy.exe' if args.target_os == 'windows' else 'cutmy'
        binary = scratch / name
        with binary.open('xb') as stream:
            stream.write(contents[name])
        binary.chmod(0o700)
        binary_sha = manifest.digest(contents[name])
        native.checked_binary(binary, binary_sha)
        version_path = command([str(binary), 'version'], limit=65536, seconds=30)
        native.runtime_version(consumer.plain_json(version_path.read_bytes(), 65536), source, (args.target_os, args.target_arch))
        native.checked_binary(binary, binary_sha)
        report['native_smoke'] = {'archive': archive, 'archive_sha256': metadata['assets'][archive]['sha256'],
                                  'binary_sha256_before': binary_sha, 'binary_sha256_after': file_identity(binary)['sha256'],
                                  'version': dict(path=version_path.name, **file_identity(version_path))}
        stage = 'metadata_after'
        after = command(metadata_command, authenticated=True)
        require(validate_metadata(consumer.plain_json(after.read_bytes(), METADATA_LIMIT), args.version, args.release_id, expected_names) == metadata,
                'release_changed')
        require(all(file_identity(assets / name) == {'bytes': item['bytes'], 'sha256': item['sha256']}
                    for name, item in metadata['assets'].items()), 'downloads_changed')
        report.update(metadata_after=dict(path=after.name, **file_identity(after)), asset_identity=metadata,
                      commands=counter, pass_candidate=True)
        require(time.monotonic() < deadline, 'total_deadline')
    except (ValueError, RuntimeError, OSError, KeyError, TypeError, AttributeError, subprocess.SubprocessError) as error:
        report['failure'] = 'interrupted' if INTERRUPTED else (str(error) if isinstance(error, Refused) else stage + '_failed')
        report['error_class'] = type(error).__name__
    finally:
        if private is not None:
            try:
                path = Path(private.name)
                private.cleanup()
                report['private_cleanup'] = not path.exists()
            except OSError:
                report['private_cleanup'] = False
                report['cleanup_error_class'] = 'OSError'
        report['pass'] = bool(report.pop('pass_candidate', False) and not report['failure'] and report['private_cleanup'])
        report['elapsed_seconds'] = TOTAL_SECONDS - (deadline - time.monotonic())
        exclusive(evidence / 'result.json', report)
    require(report['pass'], report['failure'] or 'private_cleanup')
    return report


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('--version', required=True)
    parser.add_argument('--expected-commit', required=True)
    parser.add_argument('--release-id', type=int, required=True)
    parser.add_argument('--source-sha256', required=True)
    parser.add_argument('--manifest-sha256', required=True)
    parser.add_argument('--target-os', choices=['linux', 'darwin', 'windows'], required=True)
    parser.add_argument('--target-arch', choices=['amd64', 'arm64'], required=True)
    parser.add_argument('--evidence', type=Path, required=True)
    args = parser.parse_args()
    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGTERM, interrupt)
    try:
        report = run(args)
        print(json.dumps({'pass': True, 'release_id': report['release_id'], 'target': report['target']}, sort_keys=True))
    except (ValueError, RuntimeError, OSError, KeyError, TypeError, AttributeError) as error:
        code = str(error) if isinstance(error, Refused) else 'operation_failed'
        print('Signed release consumer refused: ' + code, file=sys.stderr)
        raise SystemExit(1)


if __name__ == '__main__':
    main()
