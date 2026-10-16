#!/usr/bin/env python3
"""Owned, isolated Docker lifecycle and actual kernel resource receipts.

Cache-only helper. It never reads production configuration or controls production
containers. start/client/collect-stop are explicit operator actions; importing or
checking this file performs no Docker/systemd request. Outputs contain fixed
status codes only. Private state/receipts retain diagnostics and resource IDs.
"""
from __future__ import annotations

import argparse
import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import socket
import stat
import subprocess
import sys
import time
import urllib.request

TASK_ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
LABEL = 'cutmy.first-draft'
DATE = '20261016'
NETWORK_INTERNAL = False
PG_IMAGE = 'postgres:17-alpine@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24'
BASELINE_IMAGE_ID = 'sha256:4e794f76aeb89fd4838f5509bc1890fc2fce7196ea189e11282bb15eedbec96d'
DOCKER = ['docker', '--host', 'unix:///var/run/docker.sock']
SHA = re.compile(r'[a-f0-9]{64}\Z')
ID = re.compile(r'[a-f0-9]{32}\Z')
SAMPLE = re.compile(r'[a-z0-9][a-z0-9-]{0,63}\Z')
IMAGE = re.compile(r'[A-Za-z0-9][A-Za-z0-9._/:@-]{0,255}\Z')
CG_FILES = ('memory.peak', 'memory.current', 'memory.events', 'memory.stat',
            'memory.max', 'memory.swap.max', 'memory.pressure', 'cpu.stat',
            'cpu.max', 'cpu.weight', 'cpu.pressure', 'io.stat', 'pids.current',
            'pids.peak', 'pids.max', 'cgroup.events', 'cgroup.procs')
ROLES = ('pg', 'api', 'worker')
PRODUCTION_ROLES = {'postgres', 'api', 'worker', 'web', 'edge'}
LIMITS = {
    'parent': {'cpu_quota': '150%', 'memory_max': '1536M', 'swap_max': '0', 'cpu_weight': '10', 'tasks_max': '512'},
    'api': {'cpus': '0.5', 'memory': '512m', 'memory_bytes': 512 << 20, 'pids': 128},
    'worker': {'cpus': '1', 'memory': '768m', 'memory_bytes': 768 << 20, 'pids': 128},
    'pg': {'cpus': '0.25', 'memory': '384m', 'memory_bytes': 384 << 20, 'pids': 128},
    'client': {'cpu_quota': '25%', 'memory_max': '256M', 'tasks_max': '64'},
}
APP_ENV = {
    'DATA_DIR': '/data', 'LISTEN_ADDR': ':8080', 'GOMEMLIMIT': '128MiB',
    'GOMAXPROCS': '2', 'WORKER_CONCURRENCY': '1',
    'WORKER_PREPARATION_CONCURRENCY': '0', 'FFMPEG_THREADS': '2',
    'FFMPEG_PROFILE': 'fast', 'MAX_SOURCE_BYTES': str(128 << 20),
    'MAX_FETCH_BYTES': str(128 << 20), 'MAX_OUTPUT_BYTES': str(32 << 20),
    'MAX_STORAGE_BYTES': str(2 << 30), 'MAX_OWNER_BYTES': str(512 << 20),
    'STORAGE_SAFETY_BYTES': str(64 << 20), 'STORAGE_WAIT_TIMEOUT': '10m',
    'MAX_RANGES': '32', 'MAX_ACTIVE_JOBS': '32', 'MUTATIONS_PER_MINUTE': '20',
    'MAX_RANGE_MS': '600000', 'MAX_JOB_MS': '3600000', 'JOB_TIMEOUT': '10m',
    'SOURCE_TIMEOUT': '2m', 'UPLOAD_TIMEOUT': '10m', 'SOURCE_TTL': '24h',
    'ARTIFACT_TTL': '24h', 'WORKER_HEALTH_PATH': '/tmp/cutmy-worker-health',
    'COOKIE_SECURE': 'false', 'TRUST_PROXY': 'false', 'XDG_CACHE_HOME': '/tmp/cache',
    'FFMPEG_PATH': '/usr/bin/ffmpeg', 'FFPROBE_PATH': '/usr/bin/ffprobe',
}
DIAGNOSTIC_DIR = None
COMMAND_SEQUENCE = 0
SELECT_CONTAINER = ('{"id":{{json .Id}},"name":{{json .Name}},"image":{{json .Image}},'
                    '"labels":{{json .Config.Labels}},"state":{{json .State}},'
                    '"cgroup_parent":{{json .HostConfig.CgroupParent}},'
                    '"memory":{{json .HostConfig.Memory}},"nano_cpus":{{json .HostConfig.NanoCpus}},'
                    '"pids_limit":{{json .HostConfig.PidsLimit}},"mounts":{{json .Mounts}},'
                    '"networks":{{json .NetworkSettings.Networks}},"ports":{{json .NetworkSettings.Ports}}}')
QUERY = """SELECT json_build_object(
 'sources',(SELECT count(*) FROM sources),
 'jobs',(SELECT count(*) FROM jobs),
 'draft_jobs',(SELECT count(*) FROM jobs WHERE request->>'rendition'='draft'),
 'final_jobs',(SELECT count(*) FROM jobs WHERE coalesce(request->>'rendition','')<>'draft'),
 'succeeded_jobs',(SELECT count(*) FROM jobs WHERE status='succeeded'),
 'unfinished_jobs',(SELECT count(*) FROM jobs WHERE status NOT IN('succeeded','failed','cancelled')),
 'failed_jobs',(SELECT count(*) FROM jobs WHERE status='failed'),
 'cancelled_jobs',(SELECT count(*) FROM jobs WHERE status='cancelled'),
 'render_attempts',(SELECT coalesce(sum(attempts),0) FROM jobs),
 'artifacts',(SELECT count(*) FROM artifacts),
 'artifact_bytes',(SELECT coalesce(sum(size_bytes),0) FROM artifacts),
 'reservations',(SELECT count(*) FROM storage_reservations),
 'reserved_bytes',(SELECT coalesce(sum(size_bytes),0) FROM storage_reservations),
 'schema_versions',(SELECT json_agg(version ORDER BY version) FROM app_schema_versions));"""


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def safe_environment():
    # Do not propagate a loaded deployment environment to Docker/systemd/psql.
    return {'PATH': '/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
            'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}


def contained(path, root=TASK_ROOT, exists=False):
    path = Path(path)
    need(path.is_absolute() and path.is_relative_to(root) and '..' not in path.parts,
         'outside_task_directory')
    for ancestor in [path, *path.parents]:
        if ancestor == root.parent:
            break
        need(not ancestor.is_symlink(), 'symlink_task_path')
    if exists:
        need(path.exists(), 'missing_task_file')
    return path


def private_file(path):
    path = contained(path, exists=True)
    info = path.lstat()
    need(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid()
         and info.st_mode & 0o077 == 0 and info.st_size <= (4 << 20), 'private_file_required')
    return path


def load(path):
    return json.loads(private_file(path).read_bytes())


def save(path, data, fresh=False):
    path = contained(path)
    need(path.parent.is_dir(), 'output_parent_required')
    need(not fresh or not path.exists(), 'new_output_required')
    part = path.with_name(path.name + '.' + secrets.token_hex(6) + '.part')
    raw = json.dumps(data, ensure_ascii=True, sort_keys=True, indent=2).encode() + b'\n'
    with part.open('xb') as output:
        os.chmod(part, 0o600)
        output.write(raw)
        output.flush()
        os.fsync(output.fileno())
    os.replace(part, path)
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def run(argv, step, timeout=30, check=True, limit=4 << 20):
    # No shell, inherited credentials, console output or raw errors.
    try:
        result = subprocess.run(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE,
                                stderr=subprocess.PIPE, timeout=timeout,
                                env=safe_environment(), check=False)
    except (OSError, subprocess.TimeoutExpired):
        raise Refused(step + '_transport') from None
    need(len(result.stdout) <= limit and len(result.stderr) <= limit, step + '_response_limit')
    if DIAGNOSTIC_DIR is not None:
        global COMMAND_SEQUENCE
        COMMAND_SEQUENCE += 1
        record = {'sequence': COMMAND_SEQUENCE, 'at_utc': utc(), 'step': step,
                  'exit_code': result.returncode, 'stdout_bytes': len(result.stdout),
                  'stderr_bytes': len(result.stderr),
                  'stdout_sha256': hashlib.sha256(result.stdout).hexdigest(),
                  'stderr_sha256': hashlib.sha256(result.stderr).hexdigest()}
        with (DIAGNOSTIC_DIR / 'private-command-journal.jsonl').open('a', encoding='utf-8') as journal:
            os.chmod(journal.name, 0o600)
            journal.write(json.dumps(record, sort_keys=True) + '\n')
        if result.returncode:
            for suffix, raw in [('stdout', result.stdout), ('stderr', result.stderr)]:
                filename = f'command-{COMMAND_SEQUENCE:05}-{step}-{secrets.token_hex(3)}.{suffix}'
                with (DIAGNOSTIC_DIR / filename).open('xb') as output:
                    os.chmod(output.name, 0o600)
                    output.write(raw)
    if check:
        need(result.returncode == 0, step + '_failed')
    return result


def docker(argv, step, **kwargs):
    return run([*DOCKER, *argv], step, **kwargs)


def container(identifier, required=True):
    result = docker(['inspect', '--format', SELECT_CONTAINER, identifier], 'inspect_container', check=False)
    if result.returncode:
        need(not required, 'owned_container_missing')
        return None
    return json.loads(result.stdout)


def labels(identity, role):
    return {LABEL: DATE, LABEL + '.sample': identity, LABEL + '.role': role}


def label_args(identity, role):
    values = labels(identity, role)
    if role in ROLES:
        # Compose-built image labels are inherited unless explicitly replaced.
        values.update({'com.docker.compose.project': 'cutmy-firstdraft-bench',
                       'com.docker.compose.service': role})
    return [arg for key, value in values.items() for arg in ('--label', key + '=' + value)]


def names(identity):
    stem = 'fd20261016-' + identity
    return {'containers': {role: stem + '-' + role for role in ROLES},
            'network': stem + '-net', 'slice': 'cutmyfirstdraft' + identity + '.slice',
            'anchor': 'cutmyfirstdraftanchor' + identity + '.service',
            'client': 'cutmyfirstdraftclient' + identity + '.service'}


def image(reference, expected):
    need(isinstance(reference, str) and IMAGE.fullmatch(reference), 'invalid_image_reference')
    need(isinstance(expected, str) and expected.startswith('sha256:') and SHA.fullmatch(expected[7:]),
         'exact_image_id_required')
    result = docker(['image', 'inspect', '--format', '{{.Id}}', reference], 'cached_image')
    need(result.stdout.decode().strip() == expected, 'cached_image_identity_changed')
    return expected


def unit(unit_name):
    need(re.fullmatch(r'(cutmyfirstdraft(anchor|client)[a-f0-9]{32}\.service|cutmyfirstdraft[a-f0-9]{32}\.slice)', unit_name),
         'invalid_owned_unit')
    fields = 'ActiveState,SubState,ControlGroup,Description,ExecMainCode,ExecMainStatus,Result,MemoryPeak,CPUUsageNSec'
    result = run(['systemctl', 'show', unit_name, '--property=' + fields], 'inspect_unit')
    return dict(line.split('=', 1) for line in result.stdout.decode().splitlines() if '=' in line)


def production_snapshot():
    ids = docker(['ps', '-a', '--filter', 'label=com.docker.compose.project=cutmy', '--format', '{{.ID}}'],
                 'production_list').stdout.decode().split()
    rows = {}
    for identifier in ids:
        value = container(identifier)
        if retained_task_container(value):
            continue
        role = value.get('labels', {}).get('com.docker.compose.service')
        if role not in PRODUCTION_ROLES:
            continue
        need(role not in rows, 'production_duplicate_role')
        current = value.get('state', {})
        need(current.get('Running') is True and current.get('Health', {}).get('Status') == 'healthy'
             and current.get('OOMKilled') is False, 'production_not_five_healthy')
        rows[role] = {'id': value['id'], 'image': value['image'], 'started_at': current.get('StartedAt'),
                      'healthy': True, 'mounts': sorted([m['Type'], m.get('Name', ''), m['Source'], m['Destination']]
                                                       for m in value.get('mounts', []))}
    need(set(rows) == PRODUCTION_ROLES, 'production_not_five_healthy')
    return {'checked_at_utc': utc(), 'roles': rows}


def task_matches_retained_state(value, data):
    identity = data.get('identity')
    role = value.get('labels', {}).get(LABEL + '.role')
    if not isinstance(identity, str) or not ID.fullmatch(identity) or role not in ROLES:
        return False
    expected = names(identity)
    if (data.get('kind') != 'owned_first_draft_backend' or data.get('names') != expected
            or not all(value.get('labels', {}).get(k) == v for k, v in labels(identity, role).items())
            or value.get('name', '').removeprefix('/') != expected['containers'][role]
            or value.get('id') != data.get('container_ids', {}).get(role)
            or value.get('image') != data.get('pg_image_id' if role == 'pg' else 'core_image_id')
            or value.get('cgroup_parent') != expected['slice']
            or value.get('memory') != LIMITS[role]['memory_bytes']
            or value.get('nano_cpus') != int(float(LIMITS[role]['cpus']) * 1_000_000_000)
            or value.get('pids_limit') != LIMITS[role]['pids']
            or set(value.get('networks', {})) != {expected['network']}):
        return False
    sample = Path(data.get('sample_dir', ''))
    if sample.parent != TASK_ROOT / 'samples' or not SAMPLE.fullmatch(sample.name):
        return False
    binds = [m for m in value.get('mounts', []) if m.get('Type') == 'bind']
    if role != 'pg' and (len(binds) != 1 or binds[0].get('Destination') != '/data'
                         or Path(binds[0].get('Source', '')) != sample / 'media'):
        return False
    return all(m.get('Type') in ('bind', 'tmpfs') for m in value.get('mounts', []))


def retained_task_container(value):
    # Never exclude by label alone. Recover only tasks whose exact immutable
    # IDs/placement/mounts match their private retained backend state.
    role = value.get('labels', {}).get(LABEL + '.role')
    identity = value.get('labels', {}).get(LABEL + '.sample', '')
    if value.get('labels', {}).get(LABEL) != DATE or role not in ('api', 'worker') or not ID.fullmatch(identity):
        return False
    binds = [m for m in value.get('mounts', []) if m.get('Type') == 'bind' and m.get('Destination') == '/data']
    if len(binds) != 1:
        return False
    sample = Path(binds[0].get('Source', '')).parent
    if sample.parent != TASK_ROOT / 'samples':
        return False
    try:
        data = load(sample / 'backend-state.json')
    except (Refused, OSError, ValueError, TypeError):
        return False
    return task_matches_retained_state(value, data)


def assert_production(before):
    after = production_snapshot()
    need(after['roles'] == before['roles'], 'production_baseline_changed')
    return after


def cgroup_path(raw, parent=None):
    need(isinstance(raw, str) and raw.startswith('/') and '..' not in Path(raw).parts,
         'invalid_cgroup_path')
    path = Path('/sys/fs/cgroup') / raw.lstrip('/')
    need(not path.is_symlink() and path.is_dir(), 'owned_cgroup_missing')
    if parent is not None:
        need(path.is_relative_to(parent) and path != parent, 'cgroup_outside_owned_slice')
    return path


def counters(path):
    need((path / 'memory.peak').is_file(), 'actual_kernel_peak_required')
    result = {key: (path / key).read_text().strip() if (path / key).is_file() else None for key in CG_FILES}
    need(result['memory.peak'].isdigit() and result['memory.current'].isdigit(), 'invalid_kernel_memory_counter')
    result['memory_peak_bytes'] = int(result['memory.peak'])
    result['recorded_at_utc'] = utc()
    return result


def own_state(path):
    data = load(path)
    identity = data.get('identity')
    need(data.get('schema_version') == 1 and data.get('kind') == 'owned_first_draft_backend'
         and ID.fullmatch(identity or '') and data.get('names') == names(identity), 'invalid_backend_state')
    directory = contained(data.get('sample_dir', ''), exists=True)
    need(directory.parent == TASK_ROOT / 'samples' and private_file(path).parent == directory,
         'state_scope_mismatch')
    need(data.get('limits') == LIMITS and data.get('application_config') == APP_ENV,
         'frozen_config_changed')
    need(data.get('network_internal') is NETWORK_INTERNAL, 'network_profile_changed')
    global DIAGNOSTIC_DIR
    DIAGNOSTIC_DIR = directory
    return data


def owned_container(data, role, required=True):
    value = container(data['names']['containers'][role], required=required)
    if value is None:
        return None
    need(all(value.get('labels', {}).get(k) == v for k, v in labels(data['identity'], role).items()),
         'container_ownership_mismatch')
    expected_id = data.get('container_ids', {}).get(role)
    need(expected_id is None or value['id'] == expected_id, 'container_id_changed')
    expected_image = data['pg_image_id'] if role == 'pg' else data['core_image_id']
    need(value['image'] == expected_image and value['cgroup_parent'] == data['names']['slice'],
         'container_image_or_parent_changed')
    need(value['memory'] == LIMITS[role]['memory_bytes']
         and value['nano_cpus'] == int(float(LIMITS[role]['cpus']) * 1_000_000_000)
         and value['pids_limit'] == LIMITS[role]['pids'], 'container_resource_limits_changed')
    for mount in value.get('mounts', []):
        if mount['Type'] == 'bind':
            need(role != 'pg' and mount['Destination'] == '/data'
                 and Path(mount['Source']) == Path(data['sample_dir']) / 'media', 'unexpected_bind_mount')
        else:
            need(mount['Type'] == 'tmpfs', 'unexpected_persistent_volume')
    if role != 'pg':
        need(sum(m['Type'] == 'bind' and m['Destination'] == '/data' for m in value.get('mounts', [])) == 1,
             'owned_media_bind_required')
    need(set(value.get('networks', {})) == {data['names']['network']}, 'unexpected_network_attachment')
    if role != 'api':
        need(all(not rows for rows in value.get('ports', {}).values()), 'non_api_port_published')
    return value


def owned_unit(data, role):
    current = unit(data['names'][role])
    need(current.get('Description') == f'{LABEL}={DATE} sample={data["identity"]} role={role}',
         'unit_ownership_mismatch')
    parent = Path('/sys/fs/cgroup') / data['names']['slice']
    if current.get('ControlGroup'):
        cgroup_path(current['ControlGroup'], parent)
    return current


def owned_network(data, required=True):
    fmt = '{"id":{{json .Id}},"labels":{{json .Labels}},"internal":{{json .Internal}},"containers":{{json .Containers}}}'
    result = docker(['network', 'inspect', '--format', fmt, data['names']['network']], 'inspect_network', check=False)
    if result.returncode:
        need(not required, 'owned_network_missing')
        return None
    value = json.loads(result.stdout)
    need(all(value.get('labels', {}).get(k) == v for k, v in labels(data['identity'], 'network').items())
         and value.get('internal') is NETWORK_INTERNAL, 'network_ownership_mismatch')
    need(data.get('network_id') is None or value['id'] == data['network_id'], 'network_id_changed')
    expected_names = set(data['names']['containers'].values())
    need(all(v.get('Name') in expected_names for v in value.get('containers', {}).values()),
         'unowned_network_member')
    return value


def runtime_cgroup(pid, data):
    raw = Path('/proc', str(pid), 'cgroup').read_text().splitlines()
    rows = [line[3:] for line in raw if line.startswith('0::')]
    need(len(rows) == 1, 'unified_cgroup_required')
    return cgroup_path(rows[0], Path('/sys/fs/cgroup') / data['names']['slice'])


def snapshot(data):
    parent = cgroup_path('/' + data['names']['slice'])
    result = {'schema_version': 1, 'identity': data['identity'], 'at_utc': utc(),
              'scope': 'Actual task-owned kernel high-water since fresh slice/container startup, including bootstrap/source upload/untimed priming; sampled RSS is not used.',
              'parent': counters(parent), 'roles': {}, 'limits': data['limits']}
    owned_unit(data, 'anchor')
    for role in ROLES:
        value = owned_container(data, role)
        current = value['state']
        need(current.get('Running') is True and current.get('OOMKilled') is False, 'role_not_running')
        group = runtime_cgroup(current['Pid'], data)
        result['roles'][role] = {'container_id': value['id'], 'cgroup': str(group), 'kernel': counters(group),
                                 'healthy': current.get('Health', {}).get('Status') == 'healthy'}
    if data.get('client_launched'):
        current = owned_unit(data, 'client')
        if current.get('ControlGroup'):
            group = cgroup_path(current['ControlGroup'], parent)
            result['roles']['client'] = {'cgroup': str(group), 'kernel': counters(group), 'unit': current}
        else:
            peak = current.get('MemoryPeak', '')
            need(peak.isdigit() and int(peak) > 0, 'actual_client_peak_unavailable')
            result['roles']['client'] = {'unit': current, 'kernel_memory_peak_bytes': int(peak),
                                         'scope': 'systemd retained kernel MemoryPeak after owned service exit'}
    return result


def wait_healthy(data, role, seconds):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        current = owned_container(data, role)['state']
        need(current.get('Running') is True and current.get('OOMKilled') is False, 'startup_role_exited')
        status = current.get('Health', {}).get('Status')
        if status == 'healthy':
            return
        need(status != 'unhealthy', 'startup_role_unhealthy')
        time.sleep(.25)
    raise Refused('startup_health_deadline')


def published_loopback_port(ports, expected):
    # Internal Docker networks can retain HostConfig bindings while suppressing
    # published ports in NetworkSettings. Never len(None) or silently substitute
    # an internal/container IP for the registered SDK loopback endpoint.
    need(isinstance(ports, dict), 'loopback_only_api_required')
    rows = ports.get('8080/tcp')
    need(isinstance(rows, list) and len(rows) == 1 and isinstance(rows[0], dict),
         'api_port_not_published')
    row = rows[0]
    need(row.get('HostIp') == '127.0.0.1' and isinstance(row.get('HostPort'), str)
         and row['HostPort'].isdigit() and 1 <= int(row['HostPort']) <= 65535,
         'loopback_only_api_required')
    need(row['HostPort'] == str(expected), 'api_port_changed')
    return row['HostPort']


def save_state(path, data, event):
    data.setdefault('journal', []).append({'at_utc': utc(), 'event': event})
    save(path, data)


def launch_container(data, state_path, role, env_path):
    limit = LIMITS[role]
    name = data['names']['containers'][role]
    need(container(name, required=False) is None, 'existing_container_refused')
    common = ['run', '-d', '--pull=never', '--name', name, *label_args(data['identity'], role),
              '--network', data['names']['network'], '--cgroup-parent', data['names']['slice'],
              '--cpus', limit['cpus'], '--memory', limit['memory'], '--memory-swap', limit['memory'],
              '--pids-limit', str(limit['pids']), '--restart=no', '--read-only',
              '--cap-drop=ALL', '--security-opt', 'no-new-privileges:true',
              '--sysctl', 'net.ipv6.conf.all.disable_ipv6=1', '--log-driver=json-file',
              '--log-opt', 'max-size=4m', '--log-opt', 'max-file=2', '--env-file', str(env_path)]
    if role == 'pg':
        command = [*common, '--user', '70:70', '--network-alias', 'postgres',
                   '--tmpfs', '/var/lib/postgresql/data:rw,nosuid,nodev,noexec,size=268435456,mode=0700,uid=70,gid=70',
                   '--tmpfs', '/var/run/postgresql:rw,nosuid,nodev,noexec,size=16777216,mode=0775,uid=70,gid=70',
                   '--tmpfs', '/tmp:rw,nosuid,nodev,noexec,size=33554432,mode=1777',
                   '--health-cmd', 'pg_isready -h 127.0.0.1 -U cutmy -d cutmy',
                   '--health-interval=5s', '--health-timeout=3s', '--health-retries=8',
                   data['pg_image_id'], 'postgres', '-c', 'shared_buffers=32MB', '-c', 'work_mem=4MB',
                   '-c', 'max_connections=24', '-c', 'max_wal_size=64MB', '-c', 'min_wal_size=32MB']
    else:
        command = [*common, '--init', '--user', '1000:1000',
                   '--mount', 'type=bind,source=' + str(Path(data['sample_dir']) / 'media') + ',target=/data',
                   '--tmpfs', '/tmp:rw,nosuid,nodev,noexec,size=134217728,mode=1777',
                   '--health-cmd', '/usr/local/bin/cutmy ' + ('healthcheck' if role == 'api' else 'worker-healthcheck'),
                   '--health-interval=5s', '--health-timeout=3s', '--health-retries=12',
                   '--health-start-period=5s']
        if role == 'api':
            command += ['--publish', '127.0.0.1:' + str(data['api_port']) + ':8080']
        command += [data['core_image_id'], 'server' if role == 'api' else 'worker']
    # Journal intent first; a lost docker reply can be recovered only by exact labels.
    save_state(state_path, data, 'launch_' + role + '_intent')
    identifier = docker(command, 'launch_' + role, timeout=60).stdout.decode().strip()
    need(re.fullmatch(r'[a-f0-9]{64}', identifier), 'invalid_created_container_id')
    data.setdefault('container_ids', {})[role] = identifier
    save_state(state_path, data, 'launch_' + role + '_accepted')
    owned_container(data, role)


def start(args):
    need(os.geteuid() == 0, 'root_operator_required')
    config = load(args.config)
    allowed = {'sample_id', 'side', 'core_image', 'core_image_id', 'pg_image', 'pg_image_id', 'protocol_sha256'}
    need(set(config) <= allowed and config.get('side') in ('baseline', 'candidate')
         and SAMPLE.fullmatch(config.get('sample_id', ''))
         and SHA.fullmatch(config.get('protocol_sha256', '')), 'invalid_backend_configuration')
    need(config.get('pg_image') == PG_IMAGE, 'pinned_pg_image_required')
    if config['side'] == 'baseline':
        need(config.get('core_image_id') == BASELINE_IMAGE_ID, 'published_baseline_image_required')
    need(shutil.disk_usage(TASK_ROOT).free >= 768 << 20, 'disk_floor')
    info = TASK_ROOT.lstat()
    need(stat.S_ISDIR(info.st_mode) and not TASK_ROOT.is_symlink() and info.st_uid == 0
         and info.st_mode & 0o077 == 0, 'private_task_root_required')
    driver = docker(['info', '--format', '{{.CgroupDriver}} {{.CgroupVersion}}'], 'docker_driver').stdout.decode().strip()
    need(driver == 'systemd 2', 'systemd_cgroup_v2_required')
    core_id = image(config['core_image'], config['core_image_id'])
    pg_id = image(config['pg_image'], config['pg_image_id'])
    before = production_snapshot()
    parent = contained(TASK_ROOT / 'samples', exists=True)
    directory = contained(parent / config['sample_id'])
    need(not directory.exists(), 'new_sample_directory_required')
    directory.mkdir(mode=0o700)
    global DIAGNOSTIC_DIR
    DIAGNOSTIC_DIR = directory
    media = directory / 'media'
    media.mkdir(mode=0o700)
    os.chown(media, 1000, 1000)
    identity = secrets.token_hex(16)
    data = {'schema_version': 1, 'kind': 'owned_first_draft_backend', 'identity': identity,
            'sample_id': config['sample_id'], 'sample_dir': str(directory), 'side': config['side'],
            'names': names(identity), 'core_image_id': core_id, 'pg_image_id': pg_id,
            'protocol_sha256': config['protocol_sha256'], 'limits': LIMITS, 'application_config': APP_ENV,
            'network_internal': NETWORK_INTERNAL,
            'production_before': before, 'backend_script_sha256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'host_kernel': os.uname().release, 'status': 'creating', 'container_ids': {}, 'created_at_utc': utc()}
    path = directory / 'backend-state.json'
    save(path, data, fresh=True)
    try:
        slice_path = Path('/sys/fs/cgroup') / data['names']['slice']
        need(not slice_path.exists(), 'existing_slice_refused')
        for key in ('anchor', 'client'):
            listed = run(['systemctl', 'list-units', '--all', '--plain', '--no-legend', data['names'][key]],
                         'preexisting_unit').stdout.decode().strip()
            need(not listed, 'existing_unit_refused')
        save_state(path, data, 'anchor_intent')
        description = f'{LABEL}={DATE} sample={identity} role=anchor'
        run(['systemd-run', '--quiet', '--unit', data['names']['anchor'], '--slice', data['names']['slice'],
             '--property=Description=' + description, '--property=MemoryMax=16M', '--property=TasksMax=8',
             '--property=MemoryAccounting=yes', '--property=CPUAccounting=yes', '/bin/sleep', 'infinity'], 'anchor_start')
        owned_unit(data, 'anchor')
        run(['systemctl', 'set-property', '--runtime', data['names']['slice'], 'CPUQuota=150%',
             'MemoryMax=1536M', 'MemorySwapMax=0', 'CPUWeight=10', 'TasksMax=512',
             'MemoryAccounting=yes', 'CPUAccounting=yes'], 'slice_limits')
        data['parent_before'] = counters(cgroup_path('/' + data['names']['slice']))
        need(data['parent_before']['memory.max'] == str(1536 << 20)
             and data['parent_before']['memory.swap.max'] == '0'
             and data['parent_before']['pids.max'] == '512'
             and data['parent_before']['cpu.max'].split() == ['150000', '100000']
             and data['parent_before']['cpu.weight'] == '10', 'parent_limits_not_enforced')
        save_state(path, data, 'anchor_and_limits_ready')
        need(owned_network(data, required=False) is None, 'existing_network_refused')
        save_state(path, data, 'network_intent')
        data['network_id'] = docker(['network', 'create', '--driver', 'bridge',
                                     *label_args(identity, 'network'), data['names']['network']], 'network_create').stdout.decode().strip()
        owned_network(data)
        save_state(path, data, 'network_ready')
        password = secrets.token_hex(32)
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as reservation:
            reservation.bind(('127.0.0.1', 0))
            data['api_port'] = reservation.getsockname()[1]
        data['base_url'] = 'http://127.0.0.1:' + str(data['api_port'])
        save_state(path, data, 'loopback_port_reserved')
        pg_env = {'POSTGRES_USER': 'cutmy', 'POSTGRES_DB': 'cutmy', 'POSTGRES_PASSWORD': password,
                  'POSTGRES_INITDB_ARGS': '--auth-host=scram-sha-256 --auth-local=trust'}
        app_env = {**APP_ENV, 'PUBLIC_ORIGIN': data['base_url'], 'DATABASE_URL': 'postgres://cutmy:' + password + '@postgres:5432/cutmy?sslmode=disable&pool_max_conns=4'}
        for filename, values in [('postgres.env', pg_env), ('app.env', app_env)]:
            target = directory / filename
            with target.open('x', encoding='utf-8') as out:
                os.chmod(target, 0o600)
                out.write(''.join(k + '=' + v + '\n' for k, v in sorted(values.items())))
                out.flush()
                os.fsync(out.fileno())
        launch_container(data, path, 'pg', directory / 'postgres.env')
        wait_healthy(data, 'pg', 60)
        launch_container(data, path, 'api', directory / 'app.env')
        wait_healthy(data, 'api', 120)
        launch_container(data, path, 'worker', directory / 'app.env')
        wait_healthy(data, 'worker', 60)
        published_loopback_port(owned_container(data, 'api')['ports'], data['api_port'])
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        with opener.open(data['base_url'] + '/readyz', timeout=5) as response:
            need(response.status == 200, 'isolated_api_not_ready')
        data['start_resources'] = snapshot(data)
        data['production_after_start'] = assert_production(before)
        data['status'] = 'ready'
        save_state(path, data, 'backend_ready')
        return {'ok': True, 'action': 'start', 'roles': 3, 'state_written': True}
    except BaseException as error:
        data['status'] = 'creation_failed'
        data['failure_code'] = str(error) if isinstance(error, Refused) else 'unusable_backend_input'
        save_state(path, data, 'creation_failed_resources_preserved')
        raise


def client(args):
    data = own_state(args.state)
    need(data.get('status') == 'ready' and not data.get('client_launched'), 'new_client_required')
    spec = load(args.client)
    need(set(spec) == {'script', 'input', 'timeout_seconds'} and type(spec['timeout_seconds']) is int
         and 30 <= spec['timeout_seconds'] <= 1800, 'invalid_client_configuration')
    script = private_file(spec['script'])
    need(script.parent == TASK_ROOT / 'inputs' and script.name == 'run-python.py', 'trusted_timing_driver_required')
    request = private_file(spec['input'])
    need(request.is_relative_to(Path(data['sample_dir'])), 'client_request_outside_sample')
    body = json.loads(request.read_bytes())
    need(body.get('base_url') == data['base_url'] and body.get('role') == data['side']
         and body.get('cleanup_source') is False, 'client_endpoint_or_scope_changed')
    run_dir = contained(body.get('run_dir', ''))
    need(run_dir == Path(data['sample_dir']) / 'sdkrun' and not run_dir.exists(),
         'new_owned_client_directory_required')
    executable = TASK_ROOT / 'sdk/bin/python'
    need(executable.is_file(), 'installed_sdk_python_required')
    destination = Path(data['sample_dir']) / 'client.log'
    need(not destination.exists(), 'new_client_log_required')
    destination.touch(mode=0o600)
    data['client_launched'] = True
    save_state(args.state, data, 'client_intent')
    description = f'{LABEL}={DATE} sample={data["identity"]} role=client'
    run(['systemd-run', '--quiet', '--unit', data['names']['client'], '--slice', data['names']['slice'],
         '--property=Description=' + description, '--property=RemainAfterExit=yes',
         '--property=MemoryMax=256M', '--property=MemorySwapMax=0', '--property=CPUQuota=25%',
         '--property=TasksMax=64', '--property=MemoryAccounting=yes', '--property=CPUAccounting=yes',
         '--property=RuntimeMaxSec=' + str(spec['timeout_seconds']),
         '--property=StandardOutput=append:' + str(destination), '--property=StandardError=append:' + str(destination),
         '--setenv=PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin',
         '--setenv=LANG=C.UTF-8', '--setenv=LC_ALL=C.UTF-8',
         str(executable), '-I', str(script), str(request)], 'client_launch')
    data['client_before'] = owned_unit(data, 'client')
    save_state(args.state, data, 'client_started')
    deadline = time.monotonic() + spec['timeout_seconds'] + 10
    while time.monotonic() < deadline:
        current = owned_unit(data, 'client')
        if current.get('SubState') in ('exited', 'failed') or current.get('ActiveState') == 'failed':
            data['client_finished'] = current
            data['client_finished_at_utc'] = utc()
            data['post_client_resources'] = snapshot(data)
            save_state(args.state, data, 'client_complete')
            need(current.get('SubState') == 'exited' and current.get('ExecMainStatus') == '0'
                 and current.get('Result') == 'success', 'client_run_failed')
            return {'ok': True, 'action': 'client', 'kernel_resources_written': True}
        time.sleep(.1)
    raise Refused('client_deadline')


def database_counts(data):
    owned_container(data, 'pg')
    result = docker(['exec', data['names']['containers']['pg'], 'psql', '-U', 'cutmy', '-d', 'cutmy',
                     '-X', '-A', '-t', '-v', 'ON_ERROR_STOP=1', '-c', QUERY], 'owned_database_counts')
    return json.loads(result.stdout)


def logs(data):
    hashes = {}
    for role in ROLES:
        if owned_container(data, role, required=False) is None:
            continue
        result = docker(['logs', '--tail=4000', data['names']['containers'][role]], 'owned_role_logs', check=False)
        target = Path(data['sample_dir']) / (role + '-private.log')
        if target.exists():
            private_file(target)
            target = target.with_name(role + '-private-retry-' + secrets.token_hex(8) + '.log')
        raw = result.stdout + result.stderr
        with target.open('xb') as out:
            os.chmod(target, 0o600)
            out.write(raw)
        hashes[role] = {'file': target.name, 'sha256': hashlib.sha256(raw).hexdigest(), 'bytes': len(raw), 'truncated_to_last_lines': 4000}
    return hashes


def clean_owned(data):
    # Every delete is bound to retained state plus labels/image/mount/parent.
    for role in ('worker', 'api', 'pg'):
        value = owned_container(data, role, required=False)
        if value is None:
            continue
        if value['state'].get('Running'):
            docker(['stop', '--time=' + ('30' if role == 'pg' else '20'), value['id']], 'stop_' + role, timeout=40)
            stopped = owned_container(data, role)
            need(stopped['state'].get('Running') is False, 'owned_role_failed_to_stop')
        docker(['rm', value['id']], 'remove_' + role)
    network = owned_network(data, required=False)
    if network is not None:
        need(not network.get('containers'), 'owned_network_not_empty')
        docker(['network', 'rm', network['id']], 'remove_network')
    parent_path = Path('/sys/fs/cgroup') / data['names']['slice']
    if data.get('client_launched'):
        current = owned_unit(data, 'client')
        need(current.get('SubState') == 'exited' or current.get('ActiveState') in ('inactive', 'failed'),
             'client_still_running_preserve')
        run(['systemctl', 'stop', data['names']['client']], 'stop_client_unit')
    owned_unit(data, 'anchor')
    # Capture parent through role shutdown while owned sleep keeps it alive.
    data['parent_after_container_stop'] = counters(parent_path)
    run(['systemctl', 'stop', data['names']['anchor']], 'stop_anchor')
    if parent_path.exists():
        # No untracked task is killed; populated child is a cleanup failure.
        populated = (parent_path / 'cgroup.events').read_text()
        need(re.search(r'^populated 0$', populated, re.M) is not None, 'slice_has_untracked_processes')
    run(['systemctl', 'stop', data['names']['slice']], 'stop_owned_slice')
    data['resources_removed'] = {'containers': True, 'network': True, 'anchor': True, 'slice': True}


def collect_stop(args):
    data = own_state(args.state)
    need(data.get('status') in ('ready', 'creation_failed', 'collecting_failed'), 'backend_not_collectable')
    output = contained(args.output)
    need(output.parent == Path(data['sample_dir']) and not output.exists(), 'new_owned_result_required')
    report = {'schema_version': 1, 'kind': 'first_draft_backend_resource_receipt', 'identity': data['identity'],
              'sample_id': data['sample_id'], 'side': data['side'], 'created_at_utc': utc(),
              'production_before': data['production_before'], 'status': 'collecting', 'ok': False}
    try:
        if data['status'] == 'ready':
            report['resources_before_stop'] = snapshot(data)
            report['database_counts'] = database_counts(data)
            counts = report['database_counts']
            report['full30_workload_verified'] = (
                data.get('client_finished', {}).get('ExecMainStatus') == '0'
                and counts.get('sources') == 1 and counts.get('jobs') == counts.get('artifacts') == 30
                and counts.get('draft_jobs') == counts.get('final_jobs') == 15
                and counts.get('succeeded_jobs') == counts.get('render_attempts') == 30
                and counts.get('unfinished_jobs') == counts.get('failed_jobs') == counts.get('cancelled_jobs') == 0
                and counts.get('reservations') == counts.get('reserved_bytes') == 0)
        report['private_log_hashes'] = logs(data)
        report['production_before_cleanup'] = assert_production(data['production_before'])
        save(output, report, fresh=True)
        save_state(args.state, data, 'resource_receipt_saved_before_cleanup')
        clean_owned(data)
        report['parent_after_container_stop'] = data.get('parent_after_container_stop')
        report['production_after_cleanup'] = assert_production(data['production_before'])
        report['resources_removed'] = data['resources_removed']
        report['status'], report['ok'] = 'collected_and_stopped', True
        report['finished_at_utc'] = utc()
        report['application_config'] = data['application_config']
        report['limits'] = data['limits']
        report['protocol_sha256'] = data['protocol_sha256']
        report['core_image_id'], report['pg_image_id'] = data['core_image_id'], data['pg_image_id']
        report['network_internal'] = NETWORK_INTERNAL
        report['network_scope'] = 'Fresh owned standard Docker bridge; only API is published and only on127.0.0.1. PG/worker have zero published ports. Inputs are frozen local uploads, no URL import/remote source operation.'
        report['source_setup_included_in_kernel_peak'] = True
        report['scope'] = 'Role actual memory.peak captured before shutdown after workload; parent actual memory.peak also captured after shutdown. Both include fresh boot/upload/warm priming. Files/media retained for oracle, no production resources controlled, no sampled-RSS peak substitute.'
        save(output, report)
        data['status'] = 'collected_and_stopped'
        save_state(args.state, data, 'owned_resources_removed_production_unchanged')
        return {'ok': True, 'action': 'collect-stop', 'roles': 3, 'production_healthy': 5,
                'actual_kernel_resources_written': True, 'owned_resources_removed': True}
    except BaseException as error:
        report['status'] = 'failed_resources_preserved'
        report['failure_code'] = str(error) if isinstance(error, Refused) else 'unusable_collection_input'
        save(output, report)
        data['status'] = 'collecting_failed'
        save_state(args.state, data, 'collection_or_cleanup_failed_evidence_preserved')
        raise


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    first = sub.add_parser('start')
    first.add_argument('--config', type=Path, required=True)
    first_client = sub.add_parser('client')
    first_client.add_argument('--state', type=Path, required=True)
    first_client.add_argument('--client', type=Path, required=True)
    collect = sub.add_parser('collect-stop')
    collect.add_argument('--state', type=Path, required=True)
    collect.add_argument('--output', type=Path, required=True)
    snap = sub.add_parser('snapshot')
    snap.add_argument('--state', type=Path, required=True)
    snap.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.action == 'start':
            result = start(args)
        elif args.action == 'client':
            result = client(args)
        elif args.action == 'collect-stop':
            result = collect_stop(args)
        else:
            data = own_state(args.state)
            save(args.output, snapshot(data), fresh=True)
            result = {'ok': True, 'action': 'snapshot', 'actual_kernel_resources_written': True}
        print(json.dumps(result, sort_keys=True))
        return 0
    except (Refused, ValueError, TypeError, KeyError, OSError) as error:
        print(json.dumps({'ok': False, 'code': str(error) if isinstance(error, Refused) else 'unusable_private_input'},
                         sort_keys=True), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
