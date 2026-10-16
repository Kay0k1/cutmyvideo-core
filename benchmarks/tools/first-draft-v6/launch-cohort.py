#!/usr/bin/env python3
"""Explicit, detached run-once coordinator launch. No import-time operations."""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys

ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
SHA = re.compile(r'[a-f0-9]{64}\Z')
NONCE = re.compile(r'[a-f0-9]{32}\Z')
COHORT = re.compile(r'[a-z0-9][a-z0-9-]{0,19}\Z')
ENV = {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}
PROPERTIES = ('LoadState', 'Description', 'ActiveState', 'SubState', 'ControlGroup', 'MainPID',
              'ExecMainStatus', 'Result', 'MemoryMax', 'MemorySwapMax', 'TasksMax',
              'CPUQuotaPerSecUSec', 'RuntimeMaxUSec', 'RemainAfterExit', 'LimitFSIZE',
              'CPUUsageNSec', 'MemoryPeak')


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def path(raw, exists=True):
    value = Path(raw)
    need(value.is_absolute() and value.is_relative_to(ROOT) and '..' not in value.parts, 'outside_owned_cache')
    for ancestor in (value, *value.parents):
        if ancestor == ROOT.parent:
            break
        need(not ancestor.is_symlink(), 'symlink_owned_path')
        if ancestor.exists() and ancestor.is_dir():
            info = ancestor.stat()
            need(info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0, 'private_owned_directory_required')
    if exists:
        need(value.exists(), 'owned_input_missing')
    return value


def digest(raw):
    value = path(raw)
    before = value.stat()
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == os.geteuid()
         and before.st_mode & 0o077 == 0 and 0 < before.st_size <= 4 << 20, 'private_bounded_file_required')
    data = value.read_bytes()
    after = value.stat()
    need(len(data) <= 4 << 20 and (before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns, before.st_ctime_ns) ==
         (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns, after.st_ctime_ns), 'input_changed')
    return hashlib.sha256(data).hexdigest()


def read(raw):
    digest(raw)
    return json.loads(path(raw).read_bytes())


def save(raw, value):
    target = path(raw, False)
    data = json.dumps(value, sort_keys=True, ensure_ascii=True, allow_nan=False).encode() + b'\n'
    need(len(data) <= 1 << 20, 'output_budget')
    descriptor = os.open(target, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'wb') as output:
        output.write(data); output.flush(); os.fsync(output.fileno())
    descriptor = os.open(target.parent, os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)


def command(argv, timeout=20, check=True):
    # Only generated systemctl/systemd-run argv. No inherited deploy environment.
    result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            env=ENV, timeout=timeout, check=False)
    need(len(result.stdout) <= 1 << 20 and len(result.stderr) <= 1 << 20, 'unit_metadata_budget')
    need(not check or result.returncode == 0, 'owned_systemd_request_failed')
    return result


def policy(launcher_sha):
    return {'kind': 'detached_owned_systemd_coordinator', 'outside_measured_parents': True,
            'memory_max_bytes': 128 << 20, 'memory_swap_max_bytes': 0, 'cpu_quota_percent': 10,
            'tasks_max': 64, 'runtime_max_seconds': 14400, 'remain_after_exit': True,
            'stdout_file_max_bytes': 8 << 20, 'launcher_sha256': launcher_sha}


def validated(request_path):
    request = read(request_path)
    need(set(request) == {'kind', 'nonce', 'registration_path', 'registration_sha256',
         'controller_path', 'controller_sha256', 'window', 'python_path'}
         and request.get('kind') == 'cutmy_first_draft_detached_coordinator'
         and NONCE.fullmatch(request.get('nonce', ''))
         and all(SHA.fullmatch(request.get(key, '')) for key in ('registration_sha256', 'controller_sha256'))
         and request.get('python_path') == '/usr/bin/python3', 'invalid_launch_request')
    registration = path(request['registration_path'])
    controller = path(request['controller_path'])
    need(controller == ROOT / 'inputs/controller-cohort-v6.py' and digest(controller) == request['controller_sha256']
         and digest(registration) == request['registration_sha256'], 'exact_frozen_controller_or_registration_required')
    manifest = read(registration)
    launcher_sha = digest(Path(__file__))
    need(manifest.get('protocol_version') == 6 and manifest.get('samples') == 24
         and manifest.get('pairs_per_stratum') == 6
         and COHORT.fullmatch(manifest.get('config', {}).get('cohort_id', ''))
         and request['window'] == manifest['config']['window_id']
         and manifest.get('coordinator_instrumentation') == policy(launcher_sha)
         and manifest['config'].get('coordinator_launcher_path') == str(Path(__file__))
         and manifest['config'].get('coordinator_launcher_sha256') == launcher_sha
         and manifest['frozen_inputs'].get(str(controller)) == request['controller_sha256']
         and manifest['frozen_inputs'].get(str(Path(__file__))) == launcher_sha,
         'prospective_detached_instrumentation_not_registered')
    return request, manifest


def unit_name(request):
    return 'cutmyfirstdraftcoordinator' + request['nonce'] + '.service'


def description(request):
    return 'cutmy.first-draft=20261016 coordinator=' + request['nonce'] + ' registration=' + request['registration_sha256']


def unit(request):
    result = command(['/usr/bin/systemctl', 'show', unit_name(request),
                      *('--property=' + name for name in PROPERTIES)])
    rows = {}
    for line in result.stdout.decode('utf-8').splitlines():
        name, separator, value = line.partition('=')
        need(separator and name in PROPERTIES and name not in rows, 'invalid_unit_metadata')
        rows[name] = value
    need(rows.get('LoadState') == 'loaded' and rows.get('Description') == description(request)
         and rows.get('MemoryMax') == str(128 << 20) and rows.get('MemorySwapMax') == '0'
         and rows.get('TasksMax') == '64' and rows.get('CPUQuotaPerSecUSec') == '100ms'
         and rows.get('RuntimeMaxUSec') == '4h' and rows.get('RemainAfterExit') == 'yes'
         and rows.get('LimitFSIZE') == str(8 << 20), 'owned_coordinator_unit_or_policy_changed')
    group = rows.get('ControlGroup')
    if group:
        need(group == '/system.slice/' + unit_name(request), 'coordinator_must_be_outside_measured_parents')
    return rows


def locations(request, manifest):
    cohort = manifest['config']['cohort_id']
    return (ROOT / 'proofs' / ('cohort-' + cohort),
            ROOT / 'proofs' / ('coordinator-' + request['nonce']),
            ROOT / 'proofs' / ('COORDINATOR-' + cohort + '-LAUNCH.json'))


def intent(request_path, request, manifest):
    _, directory, _ = locations(request, manifest)
    return {'schema_version': 1, 'kind': 'immutable_owned_coordinator_launch_intent',
            'nonce': request['nonce'], 'unit_name': unit_name(request), 'description': description(request),
            'cohort_id': manifest['config']['cohort_id'], 'window': request['window'],
            'registration_path': request['registration_path'], 'registration_sha256': request['registration_sha256'],
            'controller_path': request['controller_path'], 'controller_sha256': request['controller_sha256'],
            'launcher_sha256': digest(Path(__file__)), 'request_sha256': digest(request_path),
            'log_path': str(directory / 'coordinator.log'), 'policy': policy(digest(Path(__file__))),
            'created_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat()}


def launch(request_path):
    need(os.geteuid() == 0, 'root_operator_required')
    request, manifest = validated(request_path)
    cohort, directory, intent_path = locations(request, manifest)
    lock_path = path(ROOT / '.first-draft-cohort-window.lock')
    with lock_path.open('rb') as lock:
        try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError: raise Refused('timing_or_quality_window_still_active') from None
        need(not cohort.exists() and not directory.exists() and not intent_path.exists(), 'new_run_once_coordinator_required')
        existing = command(['/usr/bin/systemctl', 'show', unit_name(request), '--property=LoadState', '--value'], check=False)
        need(existing.stdout.strip() == b'not-found', 'existing_unit_refused_no_takeover')
        directory.mkdir(mode=0o700)
        log = directory / 'coordinator.log'
        descriptor = os.open(log, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        os.close(descriptor)
        document = intent(request_path, request, manifest)
        save(intent_path, document)  # Durable run-once marker BEFORE any start.
    # Release shared cohort lock so the detached controller can acquire it.
    # A competing process makes the controller refuse; no lease takeover/retry.
    argv = ['/usr/bin/systemd-run', '--quiet', '--unit=' + unit_name(request),
            '--property=Description=' + description(request), '--property=User=root', '--property=Group=root',
            '--property=MemoryMax=128M', '--property=MemorySwapMax=0', '--property=CPUQuota=10%',
            '--property=TasksMax=64', '--property=RemainAfterExit=yes', '--property=RuntimeMaxSec=14400',
            '--property=MemoryAccounting=yes', '--property=CPUAccounting=yes', '--property=UMask=0077',
            '--property=LimitFSIZE=8388608', '--property=StandardOutput=append:' + document['log_path'],
            '--property=StandardError=append:' + document['log_path'],
            *('--setenv=' + name + '=' + value for name, value in ENV.items()),
            '--setenv=CUTMY_COORDINATOR_INTENT=' + str(intent_path), request['python_path'], '-I', '-B',
            request['controller_path'], 'run', '--registration', request['registration_path'], '--window', request['window']]
    command(argv)
    current = unit(request)
    save(directory / 'LAUNCH-ACCEPTED.json', {'schema_version': 1, 'launch_intent_sha256': digest(intent_path),
         'registration_sha256': request['registration_sha256'], 'controller_sha256': request['controller_sha256'],
         'launcher_sha256': document['launcher_sha256'], 'actual_unit': current,
         'detached_from_ssh_stdout': True, 'automatic_takeover_or_retry': False})
    return {'launched': True, 'active_state': current['ActiveState'], 'sub_state': current['SubState'],
            'detached_from_ssh_stdout': True, 'performance_claim_ready': False}


def status(request_path):
    request, manifest = validated(request_path)
    _, _, intent_path = locations(request, manifest)
    retained = read(intent_path)
    need(retained.get('request_sha256') == digest(request_path) and retained.get('nonce') == request['nonce']
         and retained.get('registration_sha256') == request['registration_sha256'], 'retained_launch_intent_changed')
    current = unit(request)
    return {'active_state': current['ActiveState'], 'sub_state': current['SubState'],
            'exit_code': int(current['ExecMainStatus']) if current['ExecMainStatus'].isdigit() else None,
            'result': current['Result'] if current['Result'] in ('success', 'exit-code', 'timeout', 'signal', 'resources') else 'other',
            'performance_claim_ready': False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('action', choices=('launch', 'status')); parser.add_argument('request', type=Path)
    args = parser.parse_args()
    os.umask(0o077)
    try:
        print(json.dumps(launch(args.request) if args.action == 'launch' else status(args.request), sort_keys=True))
        return 0
    except BaseException as error:
        code = str(error) if isinstance(error, Refused) else 'detached_coordinator_operation_failed'
        if not re.fullmatch('[a-z0-9_]{1,100}', code): code = 'detached_coordinator_operation_failed'
        print(json.dumps({'launched': False, 'code': code, 'performance_claim_ready': False}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
