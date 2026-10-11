#!/usr/bin/env python3
"""Bounded, Linux-only, externally built binary mixed-media experiment.

This driver never builds cutmy, pulls images, changes an existing database, or
uses production media. Fixtures are frozen before running. N20 nearest-rank
quantiles describe these observations; they are not a capacity limit or SLA.
"""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
from fractions import Fraction
import gzip
import hashlib
import http.client
from http.cookies import SimpleCookie
import io
import ipaddress
import json
import math
import os
from pathlib import Path
import platform
import random
import re
import select
import shutil
import signal
import socket
import stat
import struct
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import uuid

HASH = re.compile(r'^[0-9a-f]{64}$')
COMMIT = re.compile(r'^[0-9a-f]{40}$')
ID = re.compile(r'^[A-Za-z0-9_-]{1,128}$')
VERSION_KEYS = {'version', 'commit', 'build_time', 'go_version', 'os', 'arch'}
F2 = {'version': 'v0.2.1', 'commit': 'f2be62c42a6a0d3d7a017b40bd683596bc578b95',
      'build_time': '2026-10-10T19:33:28Z', 'go_version': 'go1.27.2', 'os': 'linux', 'arch': 'amd64'}
F2_ARCHIVE = '89e7832aaa29e25e6794a9d6e9e14eb9027549bdfc92270eecfd983aac1a8a21'
F2_BINARY = 'a80cf0504fc4c9c9719c7be8fcd291375dd66637316b7a06b3242187fed8dbf8'
CELLS = (('F1', 'fast', 1, 1), ('C1', 'compact', 1, 1),
         ('F2', 'fast', 2, 4), ('C2', 'compact', 2, 4))
MODES = ('accurate', 'copy', 'mp3')
PREVIEW_FPS = 24  # Both immutable f2 and the reviewed PR30 archive append fps=24.
PREVIEW_GEOMETRY = {
    'f2be62c42a6a0d3d7a017b40bd683596bc578b95': (854, 480, '1280:1281'),
    '3f0102d915b292401d7880bed3c34a8ac671d060': (852, 480, '1:1'),
}
CHECKS = ('session', 'upload', 'original_sha256', 'preview_miss', 'preview_hit',
          'jobs', 'queue_bounds', 'downloads', 'full_decode', 'geometry_pts_audio_quality',
          'delete_204', 'deleted_routes_404', 'physical_and_accounting')
SOURCE_LIMIT, OUTPUT_LIMIT = 256 << 20, 128 << 20
TOOLS_LIMIT, JSON_LIMIT = 8 << 20, 256 << 10
LABEL = 'cutmy.task'
DOCKER_OVERRIDES = ('DOCKER_HOST', 'DOCKER_CONTEXT', 'DOCKER_TLS_VERIFY', 'DOCKER_CERT_PATH', 'DOCKER_API_VERSION')


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def api_endpoint_from_proof(proof):
    require(isinstance(proof, dict) and set(proof) == {'task_label', 'network_name', 'api_name', 'network', 'api'},
            'Actual owned network/API proof is required')
    token, network_name, api_name = proof['task_label'], proof['network_name'], proof['api_name']
    require(isinstance(token, str) and re.fullmatch(r'cutmy-capacity-[0-9a-f]{16}', token)
            and isinstance(network_name, str) and network_name.startswith(token + '-')
            and api_name == token + '-api',
            'Endpoint identity must belong to this unique task')
    network, api = proof['network'], proof['api']
    require(isinstance(network, dict) and isinstance(network.get('id'), str) and HASH.fullmatch(network['id'])
            and network.get('name') == network_name and network.get('driver') == 'bridge'
            and network.get('internal') is True and isinstance(network.get('labels'), dict)
            and network['labels'].get(LABEL) == token, 'API network must be the exact owned internal bridge')
    ipam = network.get('ipam')
    require(isinstance(ipam, list) and len(ipam) == 1 and isinstance(ipam[0], dict),
            'Owned bridge must have one unambiguous IPv4 subnet')
    try:
        subnet = ipaddress.IPv4Network(ipam[0]['Subnet'], strict=True)
    except (KeyError, ValueError, TypeError):
        raise RuntimeError('Owned bridge requires an exact IPv4 subnet') from None
    require(any(subnet.subnet_of(ipaddress.IPv4Network(value)) for value in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
            and subnet.prefixlen < 31,
            'Owned bridge subnet must be private and usable')
    require(isinstance(api, dict) and isinstance(api.get('id'), str) and HASH.fullmatch(api['id'])
            and api.get('name') == '/' + api_name
            and api.get('running') is True and isinstance(api.get('labels'), dict)
            and api['labels'].get(LABEL) == token, 'Archived API must be owned and running')
    memberships = api.get('networks')
    require(isinstance(memberships, dict) and set(memberships) == {network_name}
            and isinstance(memberships[network_name], dict), 'API must belong only to its exact owned bridge')
    membership = memberships[network_name]
    require(membership.get('NetworkID') == network['id'] and membership.get('IPPrefixLen') == subnet.prefixlen,
            'API actual membership must match the owned network ID and subnet')
    try:
        address = ipaddress.IPv4Address(membership['IPAddress'])
    except (KeyError, ValueError, TypeError):
        raise RuntimeError('Actual owned API IPv4 is required') from None
    require(address in subnet and address not in (subnet.network_address, subnet.broadcast_address)
            and not address.is_loopback and not address.is_unspecified and not address.is_multicast
            and str(address) != ipam[0].get('Gateway'), 'API address is outside its exact owned usable subnet')
    ports = api.get('ports')
    require(ports is None or isinstance(ports, dict) and all(value in (None, []) for value in ports.values()),
            'Internal API must not publish host ports')
    return {'host': str(address), 'port': 8080, 'proof': proof}


def endpoint_address(endpoint):
    require(isinstance(endpoint, dict) and set(endpoint) == {'host', 'port', 'proof'},
            'HTTP requires an actual owned API endpoint; no host/default fallback')
    checked = api_endpoint_from_proof(endpoint['proof'])
    require(endpoint['host'] == checked['host'] and type(endpoint['port']) is int and endpoint['port'] == 8080,
            'HTTP endpoint differs from actual owned API proof')
    return checked['host'], checked['port']


def canonical(value):
    return (json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False) + '\n').encode()


def sha(path):
    result = hashlib.sha256()
    with Path(path).open('rb') as file:
        while block := file.read(1 << 20):
            result.update(block)
    return result.hexdigest()


def regular(path):
    path = Path(path)
    require(path.is_absolute() and path.is_file() and not path.is_symlink(), 'Expected absolute regular owned input')
    return path


def read_json(path, expected=None):
    path = regular(path)
    require(path.stat().st_size <= 4 << 20, 'JSON identity exceeds bound')
    if expected is not None:
        require(HASH.fullmatch(expected) and sha(path) == expected, 'External JSON identity mismatch')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            require(key not in result, 'Duplicate JSON key')
            result[key] = value
        return result
    return json.loads(path.read_bytes(), object_pairs_hook=unique,
                      parse_constant=lambda _: require(False, 'Nonfinite JSON number'))


def nearest_rank(values, probability):
    require(values and 0 < probability <= 1 and all(type(v) in (int, float) and math.isfinite(v) and v >= 0 for v in values),
            'Invalid empirical samples')
    ordered = sorted(values)
    return ordered[max(0, math.ceil(len(ordered) * probability) - 1)]


def describe(values, final=False):
    require(values, 'Missing observations')
    result = {'n': len(values), 'raw': values, 'min': min(values), 'max': max(values),
              'median_nearest_rank': nearest_rank(values, .5), 'scope': 'descriptive; no SLA or confidence interval'}
    if final:
        require(len(values) >= 20, 'Tail descriptor requires all 20 planned observations')
        result['p95_nearest_rank'] = nearest_rank(values, .95)
    return result


def validate_transaction(row, uhd=False):
    require(row.get('ok') is True and set(row.get('checks', [])) == set(CHECKS)
            and len(row.get('checks', [])) == len(CHECKS), 'Transaction missing/duplicated/failed checks')
    expected = set(MODES) | ({'native_uhd'} if uhd else set())
    jobs = row.get('jobs', [])
    require(len(jobs) == len(expected) and {job.get('mode') for job in jobs} == expected, 'Missing or duplicated output modes')
    for job in jobs:
        require(HASH.fullmatch(job.get('sha256', '')) and 0 < job.get('bytes', 0) <= OUTPUT_LIMIT,
                'Output bytes/hash missing')
        queue = job.get('queue', {})
        require(0 <= queue.get('lower_ms', -1) <= queue.get('upper_ms', -1), 'Missing or invalid queue bounds')
        value = job.get('validation', {})
        require(value.get('full_decode') is True and value.get('stereo_tones') and value.get('audio_alignment')
                and value.get('audio_span', {}).get('ok') is True,
                'Media validation was skipped')
        if job['mode'] != 'mp3':
            require(value.get('ssim_mean', 0) >= (.99 if job['mode'] == 'copy' else .90), 'Decoded-content quality failed')
    preview = row.get('preview_validation', {})
    require(preview.get('full_decode') is True and preview.get('stereo_tones') and preview.get('audio_alignment')
            and len(preview.get('audio_windows', [])) == 3
            and preview.get('ssim_mean', 0) >= .80 and preview.get('frame_count', 0) > 0,
            'Preview content validation was skipped or failed')


def validate_summary(report):
    require(report.get('schema_version') == 1 and report.get('ok') is True and not report.get('errors'), 'Unsuccessful experiment')
    require(report.get('shared_host') is True and report.get('dedicated_verifier_cpu') is False, 'Incorrect isolation claim')
    require(report.get('mode') in ('pilot', 'descriptive'), 'Unknown sample mode')
    expected = {(label, name) for label in ('before', 'after') for name, _, _, _ in CELLS}
    expected |= {(label, 'E-' + name) for label in ('before', 'after') for name in report.get('extensions', [])}
    cells = report.get('cells', [])
    require(len(cells) == len(expected) and {(cell.get('binary'), cell.get('cell')) for cell in cells} == expected,
            'Missing/duplicated/extra matched cells')
    for cell in cells:
        count = 3 if report['mode'] == 'pilot' else (5 if cell['cell'].startswith('E-') else 20)
        rows = cell.get('transactions', [])
        require(cell.get('ok') is True and not cell.get('cleanup_errors') and len(rows) == count
                and {row.get('index') for row in rows} == set(range(count)) and not any(row.get('warmup') for row in rows),
                'Cell dropped, substituted or failed observations')
        for row in rows:
            validate_transaction(row, cell['cell'] == 'E-uhd')


def queue_bounds(request_start, request_end, last_null_start, first_claim_end):
    require(0 <= request_start <= request_end and first_claim_end >= request_start,
            'Invalid queue observation clock ordering')
    lower = max(0, (last_null_start if last_null_start is not None else request_end) - request_end)
    upper = first_claim_end - request_start
    require(lower <= upper, 'Queue interval is inverted')
    return {'lower_ms': lower / 1e6, 'upper_ms': upper / 1e6,
            'scope': 'HTTP enqueue interval to first persisted lease; client monotonic interval bounds'}


def job_request(source_id, fixture, mode):
    require(mode in (*MODES, 'native_uhd'), 'Unknown matched job mode')
    end = fixture['start_ms'] + 2000 if mode == 'native_uhd' else fixture['end_ms']
    return {'source_id': source_id, 'ranges': [{'start_ms': fixture['start_ms'], 'end_ms': end, 'label': mode}],
            'format': 'mp3' if mode == 'mp3' else 'mp4', 'quality': '1080p' if mode == 'accurate' else 'best',
            'cut_mode': 'copy' if mode == 'copy' else 'accurate'}


def config_shape(config):
    require(set(config) == {'schema_version', 'binaries', 'runtime_image', 'postgres_image', 'tools',
            'resources', 'fixture_manifest', 'extensions', 'seed'}, 'Unknown configuration fields')
    require(config['schema_version'] == 1 and type(config['seed']) is int, 'Invalid configuration version/seed')
    for image in ('runtime_image', 'postgres_image'):
        require(isinstance(config[image], str) and re.fullmatch(r'sha256:[0-9a-f]{64}', config[image]),
                'Existing immutable Docker image ID required; pulling/tag fallback is forbidden')
    require(set(config['binaries']) == {'before', 'after'}, 'Exactly before and after executables are required')
    for label, binary in config['binaries'].items():
        require(set(binary) == {'archive', 'archive_sha256', 'binary_sha256', 'version', 'tree', 'runtime_source_sha256'},
                'Unexpected executable identity shape')
        require(Path(binary['archive']).is_absolute() and HASH.fullmatch(binary['archive_sha256'])
                and HASH.fullmatch(binary['binary_sha256']) and COMMIT.fullmatch(binary['tree'])
                and HASH.fullmatch(binary['runtime_source_sha256']), 'Invalid external executable identity')
        require(set(binary['version']) == VERSION_KEYS and COMMIT.fullmatch(binary['version']['commit'])
                and binary['version']['os'] == 'linux' and binary['version']['arch'] == 'amd64'
                and binary['version']['go_version'] == 'go1.27.2', 'Unsupported actual executable version contract')
        if label == 'before':
            require(binary['version'] == F2 and binary['archive_sha256'] == F2_ARCHIVE
                    and binary['binary_sha256'] == F2_BINARY, 'Historical baseline cannot be replaced')
    require(set(config['tools']) == {'ffmpeg', 'ffprobe'}, 'Both frozen media tools are required')
    for tool in config['tools'].values():
        require(set(tool) == {'path', 'sha256', 'version_line'} and Path(tool['path']).is_absolute()
                and HASH.fullmatch(tool['sha256']) and isinstance(tool['version_line'], str) and tool['version_line'],
                'Invalid media tool identity')
    resources = config['resources']
    require(resources == {'api_cpus': .5, 'worker_cpus': 2, 'postgres_cpus': .5,
                          'api_memory_bytes': 512 << 20, 'worker_memory_bytes': 1536 << 20,
                          'postgres_memory_bytes': 768 << 20, 'ffmpeg_threads': 2,
                          'gomaxprocs': 2, 'gomemlimit': '128MiB'}, 'Resource configuration differs from the reviewed 3-CPU matrix')
    require(set(config['fixture_manifest']) == {'path', 'sha256'}
            and Path(config['fixture_manifest']['path']).is_absolute()
            and HASH.fullmatch(config['fixture_manifest']['sha256']), 'Fixture manifest must be independently frozen')
    require(isinstance(config['extensions'], list) and len(set(config['extensions'])) == len(config['extensions'])
            and set(config['extensions']) <= {'uhd', 'long'}, 'Invalid bounded extensions')
    return config


def fixture_shape(value, public=False):
    require(set(value) == {'schema_version', 'tools', 'fixtures'} and value['schema_version'] == 1,
            'Unknown fixture manifest schema')
    require(set(value['tools']) == {'ffmpeg', 'ffprobe'}, 'Missing fixture tool identities')
    for tool in value['tools'].values():
        require(set(tool) == {'path', 'sha256', 'version_line'} and Path(tool['path']).is_absolute()
                and HASH.fullmatch(tool['sha256']) and isinstance(tool['version_line'], str) and tool['version_line'],
                'Invalid frozen fixture generation tool identity')
    generator_sha256 = {name: tool['sha256'] for name, tool in value['tools'].items()}
    require('main' in value['fixtures'] and set(value['fixtures']) <= {'main', 'uhd', 'long'}, 'Unknown fixture workload')
    for name, fixture in value['fixtures'].items():
        require(set(fixture) == {'path', 'sha256', 'bytes', 'width', 'height', 'duration_ms', 'fps',
                                'start_ms', 'end_ms', 'preview_start_ms', 'kind', 'provenance', 'proof'},
                'Fixture metadata is incomplete')
        require(Path(fixture['path']).is_absolute() and HASH.fullmatch(fixture['sha256'])
                and type(fixture['bytes']) is int and 0 < fixture['bytes'] <= SOURCE_LIMIT,
                'Fixture size/path/hash mismatch')
        expected = {'main': (1920, 1080, 20000, 2000, 8000, 0),
                    'uhd': (3840, 2160, 12000, 2000, 8000, 0),
                    'long': (1920, 1080, 180000, 120000, 126000, 120000)}[name]
        require(tuple(fixture[k] for k in ('width', 'height', 'duration_ms', 'start_ms', 'end_ms', 'preview_start_ms')) == expected
                and fixture['fps'] == 30, 'Fixture changed the reviewed duration/geometry/CFR workload')
        require(fixture['kind'] in ('synthetic', 'natural-camera-derived', 'repeated-natural-camera-derived'),
                'Undeclared fixture content origin')
        require(set(fixture['proof']) == {'metadata', 'cfr_pts', 'keyframes', 'physical_bt709', 'stereo_tones', 'full_decode'}
                and all(v is True for v in fixture['proof'].values()), 'Fixture evidence missing or skipped')
        provenance = fixture['provenance']
        require(isinstance(provenance, dict) and provenance.get('generator_argv') and provenance.get('tool_sha256'),
                'Missing actual fixture generation provenance')
        require(provenance['tool_sha256'] == generator_sha256,
                'Fixture provenance differs from frozen generation tools')
        if 'natural' in fixture['kind']:
            require(all(provenance.get(k) for k in ('author', 'source_url', 'license_url', 'changes', 'original_sha256'))
                    and HASH.fullmatch(provenance['original_sha256']), 'Natural-camera attribution is incomplete')
    if public:
        require(value['fixtures']['main']['kind'] == 'natural-camera-derived',
                'Synthetic pilot cannot become a representative natural-camera public result')
    return value


def extract_binary(identity, destination):
    archive = regular(identity['archive'])
    require(archive.stat().st_size <= 32 << 20 and sha(archive) == identity['archive_sha256'], 'Archive identity mismatch')
    with gzip.open(archive, 'rb') as file:
        expanded = file.read((64 << 20) + 1)
    require(len(expanded) <= 64 << 20, 'Archive inflated beyond bound')
    expected = {'cutmy', 'README.txt', 'LICENSE', 'THIRD_PARTY_NOTICES.md', 'CHANGELOG.md', 'BUILD.json'}
    contents = {}
    prefix = 'cutmyvideo-core_' + identity['version']['version'] + '_linux_amd64/'
    with tarfile.open(fileobj=io.BytesIO(expanded), mode='r:') as tar:
        members = tar.getmembers()
        require(len(members) == 6, 'Archive must contain exactly six regular payload members')
        for member in members:
            name = member.name.removeprefix(prefix)
            require(member.name == prefix + name and name in expected and name not in contents
                    and member.isfile() and member.size > 0 and member.pax_headers in ({}, {'path': member.name})
                    and member.mode == (0o755 if name == 'cutmy' else 0o644),
                    'Unsafe or unexpected archive member')
            contents[name] = tar.extractfile(member).read()
    require(set(contents) == expected and hashlib.sha256(contents['cutmy']).hexdigest() == identity['binary_sha256'],
            'Archived binary identity mismatch')
    build = json.loads(contents['BUILD.json'])
    for key in ('version', 'commit', 'build_time', 'os', 'arch'):
        require(build.get(key) == identity['version'][key], 'BUILD source/version differs from external identity')
    require(build.get('toolchain') == identity['version']['go_version'] and build.get('binary_sha256') == identity['binary_sha256'],
            'BUILD binary/toolchain differs from external identity')
    if 'source_tree' in build:
        require(build['source_tree'] == identity['tree'], 'BUILD tree differs from external identity')
    destination.mkdir(exist_ok=False)
    for name, data in contents.items():
        path = destination / name
        with path.open('xb') as file:
            file.write(data)
        path.chmod(0o755 if name == 'cutmy' else 0o644)
    return regular(destination / 'cutmy')


class Evidence:
    def __init__(self, root):
        self.root = Path(root).absolute()
        require(not self.root.exists(), 'Evidence directory must be new')
        self.root.mkdir(parents=True)
        self.lock = threading.Lock()
        self.next_id = 0

    def event(self, stream, value):
        with self.lock, (self.root / (stream + '.jsonl')).open('ab') as file:
            file.write(canonical(value))

    def identifier(self, prefix):
        with self.lock:
            self.next_id += 1
            return f'{prefix}-{self.next_id:05d}'


class Commands:
    def __init__(self, evidence):
        self.evidence = evidence

    def run(self, args, timeout=45, expected=0, stdin=None, env=None):
        label = self.evidence.identifier('command')
        out, err = self.evidence.root / (label + '.stdout'), self.evidence.root / (label + '.stderr')
        started = time.monotonic_ns()
        record = {'id': label, 'argv': list(map(str, args)), 'start_ns': started, 'timeout_seconds': timeout}
        process = None
        failure = None
        try:
            with out.open('xb') as stdout, err.open('xb') as stderr:
                process = subprocess.Popen(list(map(str, args)), stdin=subprocess.PIPE if stdin is not None else subprocess.DEVNULL,
                                           stdout=stdout, stderr=stderr, start_new_session=True, env=env)
                if stdin is not None:
                    process.stdin.write(stdin)
                    process.stdin.close()
                deadline = time.monotonic() + timeout
                while process.poll() is None:
                    require(time.monotonic() < deadline, 'Command external watchdog exceeded')
                    require(out.stat().st_size <= TOOLS_LIMIT and err.stat().st_size <= TOOLS_LIMIT,
                            'Command output exceeded streaming bound')
                    time.sleep(.02)
                record['exit'] = process.returncode
            require(out.stat().st_size <= TOOLS_LIMIT and err.stat().st_size <= TOOLS_LIMIT, 'Command output exceeded bound')
            require(process.returncode == expected, f'{label} unexpected exit; see exact raw output')
            return out.read_bytes(), err.read_bytes()
        except BaseException as error:
            failure = error
            record['error'] = type(error).__name__ + ': ' + str(error)
            raise
        finally:
            if process is not None and process.poll() is None:
                os.killpg(process.pid, signal.SIGKILL)
                process.wait(timeout=5)
            record['end_ns'] = time.monotonic_ns()
            record['ok'] = failure is None
            self.evidence.event('commands', record)


class Docker:
    def __init__(self, commands, executable, token):
        self.commands, self.executable, self.token = commands, executable, token
        self.containers, self.networks = set(), set()
        self.environment = self.local_environment(os.environ)
        self.argv = [self.executable, '--host', 'unix:///var/run/docker.sock']

    @staticmethod
    def local_environment(environment):
        require(not any(environment.get(key) for key in DOCKER_OVERRIDES),
                'Docker host/context/TLS/API environment overrides are forbidden')
        return {key: value for key, value in environment.items() if key not in DOCKER_OVERRIDES}

    def verify_local(self):
        mode = Path('/var/run/docker.sock').lstat()
        require(stat.S_ISSOCK(mode.st_mode) and mode.st_uid == 0, 'Local root-owned Docker socket is required')
        value = self.json('info', '--format', '{"cpus":{{.NCPU}},"memory":{{.MemTotal}},"driver":"{{.Driver}}",'
                          '"os":"{{.OSType}}","architecture":"{{.Architecture}}","security":{{json .SecurityOptions}}}')
        require(value['os'] == 'linux' and value['architecture'] in ('x86_64', 'amd64')
                and not any('rootless' in item for item in value['security']), 'Local rootful Linux/amd64 daemon is required')
        return value

    def run(self, *args, **kwargs):
        return self.commands.run([*self.argv, *args], env=self.environment, **kwargs)[0]

    def json(self, *args):
        return json.loads(self.run(*args))

    def verify_image(self, image):
        value = self.json('image', 'inspect', '--format', '{{json .Id}}', image)
        require(value == image, 'Pinned existing image does not match; no pull/build fallback')

    def network(self, name):
        self.run('network', 'create', '--internal', '--label', f'{LABEL}={self.token}', name)
        self.networks.add(name)
        return name

    def start(self, role, image, network, cpus, memory, args, mounts=(), envfile=None):
        name = self.token + '-' + role
        command = ['run', '--pull=never', '-d', '--name', name, '--label', f'{LABEL}={self.token}',
                   '--network', network, '--cpus', str(cpus), '--memory', str(memory), '--memory-swap', str(memory),
                   '--pids-limit', '128', '--security-opt', 'no-new-privileges', '--cap-drop', 'ALL']
        command += ['--user', 'postgres' if role == 'postgres' else '65532:65532']
        if role != 'postgres':
            command += ['--read-only', '--tmpfs', '/tmp:rw,noexec,nosuid,size=128m']
        if envfile:
            command += ['--env-file', str(envfile)]
        for source, destination, readonly in mounts:
            require(',' not in str(source), 'Comma in bind path cannot be safely represented')
            command += ['--mount', f'type=bind,src={source},dst={destination}' + (',readonly' if readonly else '')]
        if role != 'postgres':
            command += ['--entrypoint', str(args[0])]
            args = args[1:]
        self.run(*command, image, *args)
        self.containers.add(name)
        return name

    def info(self, name):
        # Never collect Config.Env; only owned identity and resource fields.
        value = self.json('inspect', '--format', '{{json .State}}', name)
        labels = self.json('inspect', '--format', '{{json .Config.Labels}}', name)
        require(labels.get(LABEL) == self.token, 'Container is not owned by this run')
        return value

    def api_endpoint(self, name, network):
        require(network in self.networks and name in self.containers, 'API endpoint resources must be owned by this run')
        actual_network = self.json('network', 'inspect', '--format',
                                  '{"id":{{json .Id}},"name":{{json .Name}},"driver":{{json .Driver}},'
                                  '"internal":{{json .Internal}},"labels":{{json .Labels}},"ipam":{{json .IPAM.Config}}}', network)
        actual_api = self.json('inspect', '--format',
                              '{"id":{{json .Id}},"name":{{json .Name}},"running":{{json .State.Running}},'
                              '"labels":{{json .Config.Labels}},"networks":{{json .NetworkSettings.Networks}},'
                              '"ports":{{json .NetworkSettings.Ports}}}', name)
        endpoint = api_endpoint_from_proof({'task_label': self.token, 'network_name': network,
                                           'api_name': name, 'network': actual_network, 'api': actual_api})
        self.commands.evidence.event('api_endpoint', endpoint)
        return endpoint

    def remove(self, name):
        self.info(name)
        self.run('stop', '--time', '15', name, timeout=20)
        state = self.info(name)
        require(not state['Running'] and state['ExitCode'] == 0 and not state['OOMKilled'], 'Owned service failed graceful stop/OOM')
        self.run('rm', name)
        self.containers.remove(name)

    def cleanup(self):
        failures = []
        # A daemon can create a resource even when its client returns an error.
        try:
            self.containers.update(self.run('ps', '-a', '--filter', f'label={LABEL}={self.token}', '--format', '{{.Names}}').decode().splitlines())
            self.networks.update(self.run('network', 'ls', '--filter', f'label={LABEL}={self.token}', '--format', '{{.Name}}').decode().splitlines())
        except Exception as error:
            failures.append('ownership discovery: ' + type(error).__name__ + ': ' + str(error))
        for name in sorted(self.containers):
            try:
                self.info(name)
                try:
                    stdout, stderr = self.commands.run([*self.argv, 'logs', name], env=self.environment, timeout=10)
                    (self.commands.evidence.root / (self.commands.evidence.identifier('cleanup-service-log') + '.log')).write_bytes(stdout + stderr)
                except Exception as error:
                    failures.append('cleanup log: ' + type(error).__name__ + ': ' + str(error))
                self.run('rm', '-f', name, timeout=20)
                self.containers.remove(name)
            except Exception as error:
                failures.append(type(error).__name__ + ': ' + str(error))
        for name in sorted(self.networks):
            try:
                labels = self.json('network', 'inspect', '--format', '{{json .Labels}}', name)
                require(labels.get(LABEL) == self.token, 'Network is not owned by this run')
                self.run('network', 'rm', name)
                self.networks.remove(name)
            except Exception as error:
                failures.append(type(error).__name__ + ': ' + str(error))
        try:
            require(not self.run('ps', '-a', '--filter', f'label={LABEL}={self.token}', '--format', '{{.Names}}').strip()
                    and not self.run('network', 'ls', '--filter', f'label={LABEL}={self.token}', '--format', '{{.Name}}').strip(),
                    'Owned resources remain after cleanup')
        except Exception as error:
            failures.append('cleanup confirmation: ' + type(error).__name__ + ': ' + str(error))
        require(not failures, 'Owned cleanup failed: ' + '; '.join(failures))


def wait_postgres_ready(docker, name, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        require(docker.info(name)['Running'], 'Owned PostgreSQL exited during startup')
        # The official image runs a temporary socket-only server during initdb.
        # TCP readiness excludes that server and waits for the final listener.
        result = subprocess.run([*docker.argv, 'exec', name, 'pg_isready', '-h', '127.0.0.1',
                                 '-p', '5432', '-U', 'postgres', '-d', 'cutmy'],
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                timeout=min(3, max(.001, deadline - time.monotonic())), env=docker.environment)
        if result.returncode == 0:
            return
        time.sleep(min(.1, max(0, deadline - time.monotonic())))
    raise RuntimeError('Owned PostgreSQL did not become TCP-ready')


class HTTP:
    def __init__(self, endpoint, evidence, transaction, deadline=None):
        self.host, self.port = endpoint_address(endpoint)
        self.evidence, self.transaction = evidence, transaction
        self.deadline = deadline
        self.cookie = ''

    def request(self, method, path, *, data=None, upload=None, destination=None, expected=200, headers=None, timeout=120):
        require(path.startswith('/api/v1/') or path == '/readyz', 'Unexpected endpoint')
        if self.deadline is not None:
            remaining = self.deadline - time.monotonic()
            require(remaining > 0, 'Cell watchdog expired before HTTP request')
            timeout = min(timeout, remaining)
        request_id = self.evidence.identifier('http')
        record = {'id': request_id, 'transaction': self.transaction, 'method': method, 'path': path,
                  'endpoint': {'host': self.host, 'port': self.port},
                  'start_ns': time.monotonic_ns(), 'expected_status': expected}
        connection = http.client.HTTPConnection(self.host, self.port, timeout=timeout)
        fields = dict(headers or {})
        if self.cookie:
            fields['Cookie'] = self.cookie
        result = None
        try:
            if upload is not None:
                upload = regular(upload)
                require(upload.stat().st_size <= SOURCE_LIMIT, 'Upload exceeded bounded source size')
                boundary = 'cutmy-capacity-' + uuid.uuid4().hex
                prefix = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="fixture.mp4"\r\n'
                          'Content-Type: video/mp4\r\n\r\n').encode()
                suffix = f'\r\n--{boundary}--\r\n'.encode()
                fields.update({'Content-Type': 'multipart/form-data; boundary=' + boundary,
                               'Content-Length': str(len(prefix) + upload.stat().st_size + len(suffix))})
                connection.putrequest(method, path)
                for key, value in fields.items():
                    connection.putheader(key, value)
                connection.endheaders()
                connection.send(prefix)
                with upload.open('rb') as file:
                    while block := file.read(1 << 20):
                        connection.send(block)
                connection.send(suffix)
                record['request_bytes'] = int(fields['Content-Length'])
            else:
                body = canonical(data) if data is not None else None
                if body:
                    fields['Content-Type'] = 'application/json'
                connection.request(method, path, body, fields)
                record['request_bytes'] = len(body or b'')
            response = connection.getresponse()
            record['status'] = response.status
            record['headers'] = {k.lower(): v for k, v in response.getheaders() if k.lower() in
                                 ('content-type', 'content-length', 'content-range', 'x-preview-cache', 'retry-after',
                                  'x-preview-start-ms', 'x-preview-end-ms')}
            cookie = response.getheader('Set-Cookie')
            if cookie:
                values = SimpleCookie(cookie)
                require('cutmy_session' in values, 'Session cookie is absent')
                self.cookie = 'cutmy_session=' + values['cutmy_session'].value
            is_json = 'application/json' in response.getheader('Content-Type', '')
            limit = JSON_LIMIT if is_json or destination is None else SOURCE_LIMIT
            digest, count, blocks = hashlib.sha256(), 0, []
            file = Path(destination).open('xb') if destination is not None else None
            try:
                while block := response.read(min(1 << 20, limit + 1 - count)):
                    if self.deadline is not None:
                        require(time.monotonic() < self.deadline, 'Cell watchdog expired during response')
                    count += len(block)
                    require(count <= limit, 'HTTP response exceeded streaming bound')
                    digest.update(block)
                    if file:
                        file.write(block)
                    else:
                        blocks.append(block)
            finally:
                if file:
                    file.close()
            record.update(bytes=count, sha256=digest.hexdigest())
            if is_json:
                body = b''.join(blocks) if destination is None else Path(destination).read_bytes()
                result = json.loads(body)
                record['response'] = result
            elif destination is None:
                result = b''.join(blocks)
            require(response.status == expected, f'HTTP {method} {path} status {response.status}; sample is retained')
            return result, record
        except BaseException as error:
            record['error'] = type(error).__name__ + ': ' + str(error)
            raise
        finally:
            record['end_ns'] = time.monotonic_ns()
            record['ok'] = 'error' not in record
            self.evidence.event('http', record)
            connection.close()


class PSQL:
    """One persistent local psql client, used only with this run's owned DB."""
    def __init__(self, docker, container, evidence):
        docker.info(container)
        self.evidence, self.container = evidence, container
        self.error_file = (evidence.root / evidence.identifier('psql-stderr')).open('xb')
        try:
            self.process = subprocess.Popen([*docker.argv, 'exec', '-i', container, 'psql', '-X', '-A', '-t',
                                             '--no-password', '-U', 'postgres', '-d', 'cutmy', '-v', 'ON_ERROR_STOP=1'],
                                            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=self.error_file,
                                            start_new_session=True, env=docker.environment)
        except BaseException:
            self.error_file.close()
            raise
        self.buffer = b''
        self.lock = threading.Lock()

    def query(self, sql, timeout=2):
        require('\n' not in sql and sql.startswith('SELECT ') and sql.endswith(';'), 'Observer permits bounded read-only SQL only')
        with self.lock:
            started = time.monotonic_ns()
            self.process.stdin.write(sql.encode() + b'\n')
            self.process.stdin.flush()
            deadline = time.monotonic() + timeout
            while b'\n' not in self.buffer:
                require(self.process.poll() is None, 'Read-only observer client exited')
                remaining = deadline - time.monotonic()
                require(remaining > 0, 'Read-only observer watchdog exceeded')
                ready, _, _ = select.select([self.process.stdout], [], [], min(remaining, .1))
                if ready:
                    block = os.read(self.process.stdout.fileno(), 65536)
                    require(block, 'Read-only observer lost its output')
                    self.buffer += block
                    require(len(self.buffer) <= JSON_LIMIT, 'Read-only query output exceeded bound')
            line, self.buffer = self.buffer.split(b'\n', 1)
            result = json.loads(line)
            ended = time.monotonic_ns()
            self.evidence.event('database', {'start_ns': started, 'end_ns': ended, 'sql': sql, 'result': result})
            return result, started, ended

    def close(self):
        try:
            if self.process.poll() is None:
                self.process.stdin.write(b'\\q\n')
                self.process.stdin.close()
                self.process.wait(timeout=3)
            require(self.process.returncode == 0, 'Observer client exited unsuccessfully')
        finally:
            if self.process.poll() is None:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=3)
            for file in (self.process.stdin, self.process.stdout, self.error_file):
                if file is not None and not file.closed:
                    file.close()


class QueueObserver:
    def __init__(self, pg, evidence):
        self.pg, self.evidence = pg, evidence
        self.pending, self.finished = {}, {}
        self.lock, self.stop_event = threading.Lock(), threading.Event()
        self.failure = None
        self.thread = threading.Thread(target=self.run, name='queue-observer', daemon=True)

    def register(self, job, request):
        require(ID.fullmatch(job), 'Job ID cannot enter observer SQL')
        with self.lock:
            require(job not in self.pending and job not in self.finished and len(self.pending) < 12, 'Observer active-ID bound exceeded')
            self.pending[job] = {'request_start': request['start_ns'], 'request_end': request['end_ns'], 'last_null': None}

    def run(self):
        try:
            while not self.stop_event.is_set():
                with self.lock:
                    ids = list(self.pending)
                if ids:
                    literals = ','.join("'" + value + "'" for value in ids)
                    sql = "SELECT COALESCE(json_agg(json_build_object('id',id,'claimed',lease_token IS NOT NULL)),'[]'::json) FROM jobs WHERE id IN (" + literals + ');'
                    rows, started, ended = self.pg.query(sql)
                    require(ended - started <= 1_000_000_000, 'Observer snapshot too wide for useful queue bounds')
                    with self.lock:
                        for row in rows:
                            require(set(row) == {'id', 'claimed'} and row['id'] in ids and type(row['claimed']) is bool,
                                    'Observer returned an unknown/malformed row')
                            item = self.pending[row['id']]
                            if row['claimed']:
                                self.finished[row['id']] = queue_bounds(item['request_start'], item['request_end'], item['last_null'], ended)
                                del self.pending[row['id']]
                            else:
                                item['last_null'] = started
                self.stop_event.wait(.1)
        except BaseException as error:
            self.failure = error
            self.stop_event.set()

    def bounds(self, job):
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            with self.lock:
                if job in self.finished or self.failure is not None:
                    break
            time.sleep(.01)
        if self.failure:
            raise RuntimeError('Queue observer failed: ' + str(self.failure))
        with self.lock:
            require(job in self.finished, 'Completed job has no persisted-lease observation; cannot invent queue time')
            return dict(self.finished[job])

    def close(self):
        self.stop_event.set()
        if self.thread.ident is not None:
            self.thread.join(timeout=3)
        require(not self.thread.is_alive() and self.failure is None, 'Queue observer failed bounded shutdown')


def process_stat(text, page_size, ticks):
    end = text.rfind(')')
    require(end > 0, 'Invalid Linux process stat')
    fields = text[end + 2:].split()
    require(len(fields) >= 22, 'Truncated Linux process stat')
    return {'start_ticks': int(fields[19]), 'own_cpu_seconds': (int(fields[11]) + int(fields[12])) / ticks,
            'reaped_children_cpu_seconds': (int(fields[13]) + int(fields[14])) / ticks,
            'rss_bytes': int(fields[21]) * page_size}


def key_values(path):
    return {line.split()[0]: int(line.split()[1]) for line in path.read_text().splitlines()}


def cgroup_memory(path):
    try:
        lifetime_peak = int((path / 'memory.peak').read_text())
    except FileNotFoundError:
        lifetime_peak = None  # Linux 5.15 has current/events but no memory.peak.
    events = key_values(path / 'memory.events')
    require('oom' in events and 'oom_kill' in events and events['oom'] == events['oom_kill'] == 0,
            'Fresh owned cgroup reported OOM/kill; do not publish a faster failed workload')
    return {'memory_current_bytes': int((path / 'memory.current').read_text()),
            'memory_lifetime_peak_bytes': lifetime_peak, 'memory_lifetime_peak_available': lifetime_peak is not None,
            'memory_events': events, 'memory_stat': key_values(path / 'memory.stat'),
            'swap_current_bytes': int((path / 'memory.swap.current').read_text())}


def close_all(closers):
    failures = []
    while closers:
        function = closers.pop(0)
        try:
            function()
        except BaseException as error:
            failures.append(type(error).__name__ + ': ' + str(error))
    return failures


def cleanup_owned_work(docker, work, keep_work=False):
    # Failure must retain private mounts. Never erase a live PostgreSQL/media root.
    docker.cleanup()
    if not keep_work:
        shutil.rmtree(work)


def retain_failed_media(root, evidence, label, bound=512 << 20):
    records, total = [], 0
    for directory in sorted(root.iterdir()) if root.exists() else []:
        # Never collect app.env, pg.env, database files or unowned directories.
        if directory.name != 'media' and not directory.name.startswith('transaction-'):
            continue
        require(not directory.is_symlink() and directory.is_dir(), 'Owned failure-media directory was replaced')
        for path in sorted(directory.rglob('*')):
            if not path.is_file() or path.is_symlink():
                continue
            size = path.stat().st_size
            row = {'relative': str(path.relative_to(root)), 'bytes': size, 'retained': False}
            if 0 < size <= SOURCE_LIMIT and total + size <= bound:
                target = evidence.root / (label + '-' + evidence.identifier('failure-media') + path.suffix)
                shutil.copyfile(path, target)
                row.update(retained=True, file=target.name, sha256=sha(target))
                require(row['sha256'] == sha(path), 'Owned failed media changed during preservation')
                total += size
            records.append(row)
    return {'scope': 'bounded owned source/export/preview bytes only; excludes env and PostgreSQL',
            'retained_bytes': total, 'bound_bytes': bound, 'files': records}


def cpu_delta(before, after):
    result = {}
    require(set(before) == set(after) == {'api', 'worker', 'postgres'}, 'Incomplete measured resource endpoints')
    for role in before:
        old, new = before[role]['cpu'], after[role]['cpu']
        fields = ('usage_usec', 'user_usec', 'system_usec', 'nr_periods', 'nr_throttled', 'throttled_usec')
        require(all(key in old and key in new and new[key] >= old[key] for key in fields), 'Cgroup CPU counter reset/missing')
        result[role] = {key: new[key] - old[key] for key in fields}
    return result


def copy_timeline_witness(frames, fixture, artifact, container_duration):
    """Validate the requested CFR prefix separately from copied packet tail.

    Both immutable revisions publish a rounded output-container duration and
    preserve packet timestamps. Only frames after the requested end may be
    sparse; the entire requested prefix still has its original 30Hz grid.
    """
    start, end = artifact.get('actual_start_ms'), artifact.get('actual_end_ms')
    require(type(start) is int and type(end) is int
            and 0 <= fixture['start_ms'] - start <= 1000
            and abs(end - fixture['end_ms']) <= 100,
            'Copy bounds are outside frozen GOP contract')
    # Runtime input preflight independently proves keyframes at every integer
    # second. This receipt must identify one of those original keyframes.
    require(start >= 0 and start % 1000 == 0, 'Copy start is not a verified source keyframe')
    duration = (end - start) / 1000
    require(math.isfinite(container_duration) and duration > 0
            and math.floor(container_duration * 1000 + .5) == end - start,
            'Copy acknowledgement is not the rounded measured container duration')
    require(frames and all('best_effort_timestamp_time' in frame for frame in frames),
            'Copy frame timestamps are missing')
    pts = [float(frame['best_effort_timestamp_time']) for frame in frames]
    require(all(math.isfinite(value) for value in pts)
            and len(pts) >= duration * 30 - 3 and len(pts) <= duration * 30 + 3,
            'Copy frame count is outside the existing acknowledged-duration bound')
    grid = [round(value * 30) for value in pts]
    # ffprobe prints microsecond-rounded timestamps; this is the frozen input's
    # original CFR grid, rather than an invented uniformly re-encoded tail.
    tolerance = .000002
    require(abs(pts[0]) <= tolerance and frames[0].get('key_frame') == 1
            and all(b > a for a, b in zip(pts, pts[1:]))
            and all(index >= 0 and abs(value - index / 30) <= tolerance for value, index in zip(pts, grid))
            and all(b > a for a, b in zip(grid, grid[1:])),
            'Copy timestamps must start at a keyframe and increase on the original 30Hz grid')
    prefix_duration = Fraction(fixture['end_ms'] - start, 1000)
    prefix_count = math.ceil(prefix_duration * 30)
    require(prefix_count > 0 and grid[:prefix_count] == list(range(prefix_count)),
            'Copy lost or shifted a frame inside the requested prefix')
    tail_limit = min(container_duration, duration) + 1 / 30
    require(pts[-1] <= tail_limit + tolerance,
            'Copy packet tail exceeds acknowledged/container end plus one frame')
    return {'requested_prefix_seconds': float(prefix_duration), 'requested_prefix_frames': prefix_count,
            'frame_count': len(pts), 'first_pts_seconds': pts[0], 'first_keyframe': frames[0]['key_frame'],
            'grid_hz': 30, 'grid_tolerance_seconds': tolerance,
            'post_request_tail_pts_seconds': pts[prefix_count:],
            'post_request_tail_frame_count': len(pts) - prefix_count,
            'container_duration_seconds': container_duration, 'acknowledged_duration_seconds': duration,
            'tail_limit_seconds': tail_limit, 'last_pts_seconds': pts[-1],
            'scope': 'dense requested source prefix; bounded packet-copy tail may be sparse'}


def preview_metadata_witness(probe, frames, fixture, headers, cadence, revision):
    start = fixture['preview_start_ms'] // 30000 * 30000
    end = min(start + 30000, fixture['duration_ms'])
    require(headers.get('x-preview-start-ms') == str(start) and headers.get('x-preview-end-ms') == str(end),
            'Preview did not declare its exact source window')
    streams = probe.get('streams', [])
    video = [s for s in streams if s.get('codec_type') == 'video']
    audio = [s for s in streams if s.get('codec_type') == 'audio']
    require(len(video) == len(audio) == 1 and audio[0].get('channels') == 2,
            'Preview lost required playable video/stereo audio')
    require(video[0].get('codec_name') == 'h264' and video[0].get('pix_fmt') == 'yuv420p'
            and video[0].get('color_range') == 'tv'
            and all(video[0].get(key) == 'bt709' for key in ('color_space', 'color_transfer', 'color_primaries'))
            and audio[0].get('codec_name') == 'aac' and audio[0].get('sample_rate') == '48000',
            'Preview SDR colour/codec/audio contract changed')
    require(revision in PREVIEW_GEOMETRY, 'Preview geometry must be bound to the verified immutable revision')
    width, height, sar = PREVIEW_GEOMETRY[revision]
    require((video[0].get('width'), video[0].get('height')) == (width, height)
            and video[0].get('sample_aspect_ratio') == sar, 'Preview geometry/SAR is wrong for the verified revision')
    numerator, denominator = map(int, sar.split(':'))
    displayed_dar = Fraction(video[0]['width'], video[0]['height']) * Fraction(numerator, denominator)
    source_dar = Fraction(fixture['width'], fixture['height'])  # Frozen source has square pixels.
    # Historical scale preserves 16:9 exactly through SAR; current square-pixel
    # even dimensions have at most two pixels of aspect-ratio quantization.
    dar_tolerance = Fraction(2, height)
    require(abs(displayed_dar - source_dar) <= dar_tolerance, 'Preview displayed aspect differs from the actual source')
    duration = (end - start) / 1000
    require(abs(float(probe.get('format', {}).get('duration', -1)) - duration) <= .35,
            'Preview duration changed')
    pts = [float(frame['best_effort_timestamp_time']) for frame in frames]
    require(len(pts) >= duration * cadence - 2 and len(pts) <= duration * cadence + 2
            and pts and abs(pts[0]) <= .05
            and all(abs(b - a - 1 / cadence) <= .002 for a, b in zip(pts, pts[1:])),
            'Preview skipped frames or changed the common 24fps contract')
    return {'source_start_ms': start, 'source_end_ms': end, 'cadence': cadence, 'frame_count': len(pts),
            'geometry': [width, height], 'sample_aspect_ratio': video[0]['sample_aspect_ratio'],
            'revision': revision, 'displayed_aspect_ratio': str(displayed_dar),
            'source_display_aspect_ratio': str(source_dar), 'displayed_aspect_tolerance': str(dar_tolerance)}


def resource_binding(docker, role, name):
    state = docker.info(name)
    require(state['Running'] and type(state.get('Pid')) is int and state['Pid'] > 0,
            'Resource binding requires an owned live service PID')
    pid = state['Pid']
    container_id = docker.json('inspect', '--format', '{{json .Id}}', name)
    require(isinstance(container_id, str) and HASH.fullmatch(container_id), 'Actual container identity is missing')
    raw_cgroup = Path(f'/proc/{pid}/cgroup').read_text()
    cgroups = raw_cgroup.splitlines()
    require(len(cgroups) == 1 and cgroups[0].startswith('0::/'), 'Dedicated cgroup-v2 evidence is required')
    root = Path('/sys/fs/cgroup').resolve()
    path = (root / cgroups[0][4:].lstrip('/')).resolve()
    require(path.is_dir() and path != root and path.is_relative_to(root), 'Refuse shared host/chat or outside cgroup')
    raw_cpu_max = (path / 'cpu.max').read_text()
    quota, period = raw_cpu_max.split()
    expected = {'api': .5, 'worker': 2, 'postgres': .5}[role]
    require(quota != 'max' and int(period) > 0 and int(quota) / int(period) == expected, 'Actual cgroup CPU quota differs')
    memory_max = int((path / 'memory.max').read_text())
    swap_max = int((path / 'memory.swap.max').read_text())
    require(memory_max == {'api': 512 << 20, 'worker': 1536 << 20, 'postgres': 768 << 20}[role]
            and swap_max == 0, 'Actual cgroup memory/swap limit differs')
    process = process_stat(Path(f'/proc/{pid}/stat').read_text(), os.sysconf('SC_PAGE_SIZE'), os.sysconf('SC_CLK_TCK'))
    final = docker.info(name)
    require(final['Running'] and final['Pid'] == pid, 'Owned service changed while binding actual resources')
    return {'container_name': name, 'container_id': container_id, 'container_started_at': state['StartedAt'],
            'task_label': docker.token, 'root_pid': pid, 'root_start_ticks': process['start_ticks'],
            'cgroup_path': str(path), 'proc_cgroup_raw': raw_cgroup, 'cpu_max_raw': raw_cpu_max,
            'cpu_quota_usec': int(quota), 'cpu_period_usec': int(period), 'effective_cpus': expected,
            'memory_max_bytes': memory_max, 'memory_swap_max_bytes': swap_max}


class Resources:
    def __init__(self, docker, roles, evidence, context):
        self.roles, self.evidence = {}, evidence
        self.context, self.peaks = context, {}
        self.failure, self.phase = None, 'startup'
        self.failure_details, self.failure_diagnostic_error = None, None
        self.last_sample_ns = None
        self.stop_event = threading.Event()
        self.ticks, self.pages = os.sysconf('SC_CLK_TCK'), os.sysconf('SC_PAGE_SIZE')
        bindings = {}
        for role, name in roles.items():
            bindings[role] = resource_binding(docker, role, name)
            self.roles[role] = {'container': name, 'root_pid': bindings[role]['root_pid'],
                                'cgroup': Path(bindings[role]['cgroup_path'])}
        require(set(bindings) == {'api', 'worker', 'postgres'}
                and len({item['container_id'] for item in bindings.values()}) == 3
                and len({item['cgroup_path'] for item in bindings.values()}) == 3,
                'Resource roles must use three distinct actual containers and cgroups')
        self.evidence.event('resource_binding', {'at_ns': time.monotonic_ns(), **context, 'roles': bindings})
        self.thread = threading.Thread(target=self.run, name='resource-observer', daemon=True)

    def snapshot(self):
        result = {}
        for role, value in self.roles.items():
            path = value['cgroup']
            pids = [int(pid) for pid in (path / 'cgroup.procs').read_text().split()]
            processes = []
            for pid in pids:
                try:
                    item = process_stat(Path(f'/proc/{pid}/stat').read_text(), self.pages, self.ticks)
                    item.update(pid=pid, comm=Path(f'/proc/{pid}/comm').read_text().strip(), root=pid == value['root_pid'])
                    processes.append(item)
                except (FileNotFoundError, ProcessLookupError) as error:
                    if pid == value['root_pid']:
                        raise RuntimeError('Owned service root disappeared during resource sample') from error
                    # Short children may disappear between reads; cgroup CPU captures them.
            require(any(p['root'] for p in processes), 'Owned service root exited unexpectedly')
            result[role] = {'cpu': key_values(path / 'cpu.stat'), **cgroup_memory(path), 'processes': processes}
        return result

    def run(self):
        try:
            while not self.stop_event.is_set():
                snapshot = self.snapshot()
                for role, value in snapshot.items():
                    key = (self.phase, role)
                    peak = self.peaks.setdefault(key, {'samples': 0, 'root_rss_bytes': 0, 'children_rss_sum_bytes': 0,
                                                       'all_processes_rss_sum_bytes': 0, 'cgroup_memory_current_bytes': 0})
                    peak['samples'] += 1
                    root = sum(p['rss_bytes'] for p in value['processes'] if p['root'])
                    children = sum(p['rss_bytes'] for p in value['processes'] if not p['root'])
                    for name, number in (('root_rss_bytes', root), ('children_rss_sum_bytes', children),
                                         ('all_processes_rss_sum_bytes', root + children),
                                         ('cgroup_memory_current_bytes', value['memory_current_bytes'])):
                        peak[name] = max(peak[name], number)
                at_ns = time.monotonic_ns()
                self.evidence.event('resources', {'at_ns': at_ns, 'phase': self.phase,
                                                 **self.context, 'roles': snapshot})
                self.last_sample_ns = at_ns
                self.stop_event.wait(.1)
        except BaseException as error:
            self.failure = error
            self.failure_details = {'at_ns': time.monotonic_ns(), **self.context, 'phase': self.phase,
                                    'exception_class': type(error).__name__, 'errno': getattr(error, 'errno', None),
                                    'message': str(error)[:512], 'last_completed_sample_ns': self.last_sample_ns,
                                    'thread_native_id': threading.get_native_id()}
            try:
                self.evidence.event('observer_errors', self.failure_details)
            except BaseException as diagnostic_error:
                self.failure_diagnostic_error = type(diagnostic_error).__name__ + ': ' + str(diagnostic_error)[:512]
            finally:
                self.stop_event.set()

    def close(self):
        self.stop_event.set()
        if self.thread.ident is not None:
            self.thread.join(timeout=3)
        if self.thread.is_alive():
            raise RuntimeError('Resource sampler thread still alive after 3s bounded shutdown') from self.failure
        if self.failure is not None:
            raise RuntimeError('Resource sampler failed: ' + type(self.failure).__name__ + ': ' + str(self.failure)[:512]) from self.failure
        self.final_snapshot = self.snapshot()  # Final liveness/OOM witness before intended service stop.

    def summary(self):
        return {phase: {role: dict(value) for (p, role), value in self.peaks.items() if p == phase}
                for phase, _ in self.peaks}


def tone_witness(pcm):
    require(len(pcm) >= 4000 and len(pcm) % 8 == 0, 'PCM stereo witness is missing/truncated')
    samples = struct.unpack('<' + 'f' * (len(pcm) // 4), pcm)
    result = []
    for channel, expected in ((0, 440), (1, 880)):
        sequence = samples[channel::2]
        require(all(math.isfinite(x) for x in sequence), 'Nonfinite decoded audio')
        powers = {}
        for frequency in (440, 880):
            step = 2 * math.pi * frequency / 48000
            real = sum(x * math.cos(step * index) for index, x in enumerate(sequence))
            imag = sum(x * math.sin(step * index) for index, x in enumerate(sequence))
            powers[frequency] = real * real + imag * imag
        require(powers[expected] > 1 and powers[expected] > 15 * powers[1320 - expected], 'Decoded stereo channels/tone selection changed')
        result.append(powers)
    return result


def audio_alignment_witness(pcm, source_start_ms, tolerance_ms):
    require(len(pcm) >= 48000 * 8 * .85 and len(pcm) % 8 == 0, 'Audio alignment witness is truncated')
    samples = struct.unpack('<' + 'f' * (len(pcm) // 4), pcm)
    bins = []
    for offset in range(0, len(samples) - 960 + 1, 960):
        block = samples[offset:offset + 960]
        require(all(math.isfinite(value) for value in block), 'Nonfinite alignment samples')
        bins.append(sum(value * value for value in block) / len(block))
    peak = max(bins)
    require(peak > .001, 'Audio pulse has no useful signal')
    active = [index for index, power in enumerate(bins) if power > peak * .2]
    require(active and max(active) < len(bins) - 1, 'Audio pulse does not end inside the witness')
    observed = [min(active) * 10, (max(active) + 1) * 10]
    phase = source_start_ms % 1000
    expected = [200 - phase, 700 - phase]
    require(0 <= expected[0] < expected[1] <= 850, 'Fixture GOP phase cannot support the declared pulse oracle')
    require(all(abs(a - b) <= tolerance_ms for a, b in zip(observed, expected)), 'Decoded audio pulse shifted relative to source/video time')
    return {'rms_squared_10ms': bins, 'observed_edges_ms': observed, 'source_edges_ms': expected,
            'tolerance_ms': tolerance_ms}


def audio_span_witness(pcm, duration, source_start_ms, tolerance_ms):
    require(len(pcm) % 8 == 0 and abs(len(pcm) / (48000 * 8) - duration) <= .12,
            'Decoded audio does not cover the complete published interval')
    windows = []
    for second in range(math.floor(duration)):
        offset = second * 48000 * 8
        block = pcm[offset:offset + 43200 * 8]
        windows.append({'offset_seconds': second, 'tones': tone_witness(block),
                        'alignment': audio_alignment_witness(block, source_start_ms + second * 1000, tolerance_ms)})
    require(len(windows) >= 2, 'Audio-span oracle requires at least two complete seconds')
    return {'ok': True, 'decoded_duration_seconds': len(pcm) / (48000 * 8), 'windows': windows,
            'scope': 'full decoded sample span and per-second stereo pulse witnesses'}


def physical_709(yuv, rgb):
    width, height = 256, 64
    require(len(yuv) == width * height * 3 and len(rgb) == width * height * 3, 'Physical input witness size changed')
    colors = ((240, 20, 20), (20, 240, 20), (20, 20, 240), (240, 240, 240))
    records = []
    for index, color in enumerate(colors):
        x, y = index * 64 + 32, 32
        actual_yuv = [yuv[plane * width * height + y * width + x] for plane in range(3)]
        red, green, blue = (value / 255 for value in color)
        luminance = .2126 * red + .7152 * green + .0722 * blue
        expected = [16 + 219 * luminance, 128 + 224 * (blue - luminance) / 1.8556,
                    128 + 224 * (red - luminance) / 1.5748]
        require(all(abs(a - b) <= 4 for a, b in zip(actual_yuv, expected)), 'Tags alone do not prove physical BT709 conversion')
        actual_rgb = list(rgb[(y * width + x) * 3:(y * width + x) * 3 + 3])
        require(all(abs(a - b) <= 35 for a, b in zip(actual_rgb, color)), 'Decoded input RGB colors changed')
        records.append({'actual_yuv': actual_yuv, 'formula_yuv': expected, 'actual_rgb': actual_rgb, 'source_rgb': color})
    return records


class Experiment:
    def __init__(self, args):
        require(platform.system() == 'Linux' and platform.machine() == 'x86_64', 'This resource experiment is Linux/amd64 only')
        self.args = args
        self.config = config_shape(read_json(args.config, args.expected_config_sha256))
        frozen = self.config['fixture_manifest']
        self.fixtures = fixture_shape(read_json(frozen['path'], frozen['sha256']), public=args.mode == 'descriptive')
        # Fixture tools describe historical generation; CONFIG tools are shared by both binaries.
        # prepare() independently validates the frozen input with the actual common runtime image.
        for name in ('main', *self.config['extensions']):
            require(name in self.fixtures['fixtures'], 'Required extension fixture is absent')
            item = self.fixtures['fixtures'][name]
            path = regular(item['path'])
            require(path.stat().st_size == item['bytes'] and sha(path) == item['sha256'], 'Frozen fixture bytes changed')
        self.evidence = Evidence(args.evidence_dir)
        self.commands = Commands(self.evidence)
        self.token = 'cutmy-capacity-' + uuid.uuid4().hex[:16]
        self.docker = Docker(self.commands, str(regular(args.docker)), self.token)
        self.work = Path(tempfile.mkdtemp(prefix=self.token + '-', dir=args.scratch_dir)).absolute()
        require(not self.work.is_relative_to(Path(__file__).resolve().parents[1]), 'Private work must be outside checkout')
        self.harness_sha = sha(Path(__file__).absolute())
        self.binaries, self.cells, self.errors = {}, [], []
        self.local_closers = []
        self.retain_work = False

    def container_tool(self, name, arguments, work, timeout=120):
        # Separate verifier cgroup in an idle phase, on the same shared host.
        if hasattr(self, 'cell_deadline'):
            timeout = min(timeout, self.cell_deadline - time.monotonic())
            require(timeout > 0, 'Cell watchdog expired before validation command')
        cidfile = self.work / self.evidence.identifier('verifier-cid')
        command = ['run', '--pull=never', '--rm', '--cidfile', str(cidfile), '--label', f'{LABEL}={self.token}',
                   '--network', 'none', '--read-only', '--cpus', '.5', '--memory', str(512 << 20), '--memory-swap', str(512 << 20),
                   '--tmpfs', '/tmp:rw,nosuid,size=32m', '--security-opt', 'no-new-privileges', '--cap-drop', 'ALL',
                   '--mount', f'type=bind,src={work},dst=/work', '--entrypoint', self.config['tools'][name]['path'], self.config['runtime_image'], *arguments]
        try:
            return self.commands.run([*self.docker.argv, *command], timeout=timeout, env=self.docker.environment)
        finally:
            if cidfile.exists():
                cid = cidfile.read_text().strip()
                if re.fullmatch(r'[0-9a-f]{64}', cid):
                    # --rm normally removes it; inspect first, do not touch unrelated containers.
                    inspect = subprocess.run([*self.docker.argv, 'inspect', '--format', '{{json .Config.Labels}}', cid],
                                             stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5, env=self.docker.environment)
                    if inspect.returncode == 0:
                        require(json.loads(inspect.stdout).get(LABEL) == self.token, 'Verifier cleanup ownership mismatch')
                        self.docker.run('rm', '-f', cid)

    def prepare(self):
        require(os.geteuid() == 0, 'Root is required for owned bind permissions and cross-UID proc inspection')
        require(shutil.disk_usage(self.work).free >= 8 << 30, 'At least 8GiB owned scratch space is required')
        info = self.docker.verify_local()
        for image in ('runtime_image', 'postgres_image'):
            self.docker.verify_image(self.config[image])
        require(info['cpus'] >= 4 and info['memory'] >= 6 << 30, 'Reviewed host minimum is 4CPUs/6GiB')
        self.evidence.event('context', {'host': info, 'kernel': platform.release(), 'python': platform.python_version(),
                                     'cpu_affinity': sorted(os.sched_getaffinity(0)), 'shared_host': True,
                                     'dedicated_verifier_cpu': False, 'config_sha256': self.args.expected_config_sha256,
                                     'harness_sha256': self.harness_sha})
        for name, tool in self.config['tools'].items():
            out = self.docker.run('run', '--pull=never', '--rm', '--label', f'{LABEL}={self.token}', '--network', 'none',
                                 '--entrypoint', 'sha256sum', self.config['runtime_image'], tool['path'])
            require(out.decode().split()[0] == tool['sha256'], 'Service media tool binary changed')
            out = self.docker.run('run', '--pull=never', '--rm', '--label', f'{LABEL}={self.token}', '--network', 'none',
                                 '--entrypoint', tool['path'], self.config['runtime_image'], '-version')
            require(out.decode().splitlines()[0] == tool['version_line'], 'Service media tool version changed')
        uid = self.docker.run('run', '--pull=never', '--rm', '--label', f'{LABEL}={self.token}', '--network', 'none',
                             '--entrypoint', 'id', self.config['postgres_image'], '-u', 'postgres')
        self.pg_uid = int(uid.decode().strip())
        require(0 < self.pg_uid < 65536, 'Unexpected PostgreSQL image UID')
        for label in ('before', 'after'):
            identity = self.config['binaries'][label]
            binary = extract_binary(identity, self.work / label)
            out = self.docker.run('run', '--pull=never', '--rm', '--label', f'{LABEL}={self.token}', '--network', 'none',
                                 '--read-only', '--mount', f'type=bind,src={binary.parent},dst=/binary,readonly',
                                 '--entrypoint', '/binary/cutmy', self.config['runtime_image'], 'version')
            require(json.loads(out) == identity['version'], 'Actual archive executable version differs; no fallback permitted')
            self.binaries[label] = binary
        for name in ('main', *self.config['extensions']):
            self.verify_input(name, self.fixtures['fixtures'][name])

    def verify_input(self, name, fixture):
        # Host-generated input is decoded again with the actual fixed image ABI.
        work = self.work / ('input-proof-' + name)
        work.mkdir()
        path = work / 'input.mp4'
        shutil.copyfile(fixture['path'], path)
        require(sha(path) == fixture['sha256'], 'Image preflight input copy changed')
        probe = self.probe(path, work)
        video = [s for s in probe['streams'] if s['codec_type'] == 'video']
        audio = [s for s in probe['streams'] if s['codec_type'] == 'audio']
        require(len(video) == len(audio) == 1 and video[0]['codec_name'] == 'h264'
                and (video[0]['width'], video[0]['height']) == (fixture['width'], fixture['height'])
                and video[0]['pix_fmt'] == 'yuv420p' and video[0]['sample_aspect_ratio'] == '1:1'
                and video[0]['color_range'] == 'tv'
                and all(video[0][key] == 'bt709' for key in ('color_space', 'color_transfer', 'color_primaries'))
                and audio[0]['codec_name'] == 'aac' and audio[0]['channels'] == 2 and audio[0]['sample_rate'] == '48000'
                and abs(float(probe['format']['duration']) * 1000 - fixture['duration_ms']) <= 100,
                'Runtime image decoded different fixture metadata')
        frames = self.probe(path, work, frames=True)['frames']
        pts = [float(frame['best_effort_timestamp_time']) for frame in frames]
        keys = [float(frame['best_effort_timestamp_time']) for frame in frames if frame['key_frame'] == 1]
        duration = fixture['duration_ms'] // 1000
        require(len(pts) == duration * 30 and abs(pts[0]) <= .002
                and all(abs(b - a - 1 / 30) <= .001 for a, b in zip(pts, pts[1:]))
                and len(keys) == duration and all(abs(value - second) <= .002 for second, value in enumerate(keys)),
                'Runtime image decoded different input cadence/keyframes')
        colors = []
        for format_ in ('yuv444p', 'rgb24'):
            out, _ = self.container_tool('ffmpeg', ['-v', 'error', '-i', '/work/input.mp4', '-frames:v', '1',
                                        '-vf', 'crop=256:64:0:0,format=' + format_, '-pix_fmt', format_, '-f', 'rawvideo', '-'], work)
            colors.append(out)
        color_proof = physical_709(*colors)
        if duration <= 20:
            pcm, _ = self.container_tool('ffmpeg', ['-v', 'error', '-i', '/work/input.mp4', '-vn', '-ac', '2',
                                                   '-ar', '48000', '-f', 'f32le', '-'], work)
            audio_proof = audio_span_witness(pcm, duration, 0, 25)
        else:
            windows = []
            for offset in (0, duration // 2, duration - 1):
                pcm, _ = self.container_tool('ffmpeg', ['-v', 'error', '-ss', str(offset), '-i', '/work/input.mp4',
                                                       '-t', '0.9', '-vn', '-ac', '2', '-ar', '48000', '-f', 'f32le', '-'], work)
                windows.append({'offset_seconds': offset, 'tones': tone_witness(pcm),
                                'alignment': audio_alignment_witness(pcm, offset * 1000, 25)})
            audio_proof = {'ok': True, 'scope': 'beginning/middle/end plus full decode', 'windows': windows}
        self.container_tool('ffmpeg', ['-v', 'error', '-xerror', '-i', '/work/input.mp4', '-f', 'null', '-'], work, timeout=600)
        require(sha(path) == fixture['sha256'], 'Image preflight fixture changed during verification')
        self.evidence.event('runtime_input_proof', {'fixture': name, 'sha256': fixture['sha256'], 'probe': probe,
            'frame_count': len(pts), 'keyframes': keys, 'physical_bt709': color_proof, 'audio_span': audio_proof, 'full_decode': True})
        shutil.rmtree(work)

    def env_file(self, path, values):
        require(all('\n' not in key + str(value) for key, value in values.items()), 'Invalid owned environment serialization')
        with path.open('x') as file:
            file.write(''.join(key + '=' + str(value) + '\n' for key, value in values.items()))
        path.chmod(0o600)
        return path

    def start_cell(self, label, cell):
        name, profile, concurrency, clients = cell
        root = self.work / (label + '-' + name)
        root.mkdir()
        media, pgdata = root / 'media', root / 'pgdata'
        media.mkdir(mode=0o777)
        media.chmod(0o777)
        pgdata.mkdir(mode=0o700)
        os.chown(pgdata, self.pg_uid, self.pg_uid)
        network = self.docker.network(self.token + '-' + label + '-' + name)
        pg_env = self.env_file(root / 'pg.env', {'POSTGRES_DB': 'cutmy', 'POSTGRES_HOST_AUTH_METHOD': 'trust'})
        resources = self.config['resources']
        pg = self.docker.start('postgres', self.config['postgres_image'], network, .5, resources['postgres_memory_bytes'],
                               ['postgres', '-c', 'fsync=on', '-c', 'synchronous_commit=on', '-c', 'full_page_writes=on'],
                               [(pgdata, '/var/lib/postgresql/data', False)], pg_env)
        wait_postgres_ready(self.docker, pg)
        values = {'DATABASE_URL': 'postgres://postgres@' + pg + ':5432/cutmy?sslmode=disable',
                  'DATA_DIR': '/media', 'LISTEN_ADDR': ':8080', 'COOKIE_SECURE': 'false', 'TRUST_PROXY': 'false',
                  'FFMPEG_PATH': self.config['tools']['ffmpeg']['path'], 'FFPROBE_PATH': self.config['tools']['ffprobe']['path'],
                  'WORKER_HEALTH_PATH': '/tmp/worker-health.json', 'MAX_SOURCE_BYTES': SOURCE_LIMIT, 'MAX_FETCH_BYTES': SOURCE_LIMIT,
                  'MAX_OUTPUT_BYTES': OUTPUT_LIMIT, 'MAX_STORAGE_BYTES': 4 << 30, 'MAX_OWNER_BYTES': 1 << 30,
                  'STORAGE_SAFETY_BYTES': 256 << 20, 'MAX_RANGES': 4, 'MAX_ACTIVE_JOBS': 32, 'MUTATIONS_PER_MINUTE': 200,
                  'STORAGE_WAIT_TIMEOUT': '2m', 'JOB_TIMEOUT': '120s', 'UPLOAD_TIMEOUT': '120s', 'SOURCE_TIMEOUT': '120s',
                  'SOURCE_TTL': '24h', 'ARTIFACT_TTL': '24h', 'FFMPEG_THREADS': 2, 'WORKER_CONCURRENCY': concurrency,
                  'FFMPEG_PROFILE': profile, 'GOMAXPROCS': 2, 'GOMEMLIMIT': '128MiB'}
        envfile = self.env_file(root / 'app.env', values)
        mounts = [(self.binaries[label].parent, '/binary', True), (media, '/media', False)]
        api = self.docker.start('api', self.config['runtime_image'], network, .5, resources['api_memory_bytes'],
                                ['/binary/cutmy', 'server'], mounts, envfile)
        endpoint = self.docker.api_endpoint(api, network)
        client = HTTP(endpoint, self.evidence, 'startup')
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            require(self.docker.info(api)['Running'], 'Archived API exited during startup')
            try:
                value, _ = client.request('GET', '/readyz', timeout=2)
                if value == {'ok': True}:
                    break
            except (OSError, RuntimeError):
                time.sleep(.1)
        else:
            raise RuntimeError('Archived API did not become ready')
        worker = self.docker.start('worker', self.config['runtime_image'], network, 2, resources['worker_memory_bytes'],
                                   ['/binary/cutmy', 'worker'], mounts, envfile)
        pgclient = PSQL(self.docker, pg, self.evidence)
        self.local_closers.append(pgclient.close)
        observer = QueueObserver(pgclient, self.evidence)
        self.local_closers.insert(0, observer.close)
        sampler = Resources(self.docker, {'api': api, 'worker': worker, 'postgres': pg}, self.evidence,
                            {'binary': label, 'cell': name})
        self.local_closers.insert(0, sampler.close)
        observer.thread.start()
        sampler.thread.start()
        return {'root': root, 'media': media, 'network': network, 'api': api, 'worker': worker, 'postgres': pg, 'binary': label,
                'endpoint': endpoint, 'pg': pgclient, 'observer': observer, 'sampler': sampler, 'clients': clients}

    def transaction(self, cell, fixture, index, warmup=False):
        row = {}
        try:
            result = self._transaction(cell, fixture, index, warmup, row)
            self.evidence.event('transactions_ready', {k: v for k, v in row.items() if k != '_client'})
            return result
        except BaseException as error:
            row.pop('_client', None)
            row.update(ok=False, error=type(error).__name__ + ': ' + str(error), failed_at_ns=time.monotonic_ns())
            self.evidence.event('transactions', row)
            raise

    def _transaction(self, cell, fixture, index, warmup, row):
        tx = self.evidence.identifier('transaction')
        started = time.monotonic_ns()
        row.update(id=tx, index=index, warmup=warmup, start_ns=started, checks=[], jobs=[], ok=False)
        client = HTTP(cell['endpoint'], self.evidence, tx, self.cell_deadline)
        root = cell['root'] / tx
        root.mkdir()
        value, session = client.request('GET', '/api/v1/session')
        require(value.get('ok') is True and client.cookie, 'Real owner session is missing')
        row['checks'].append('session')
        source, upload = client.request('POST', '/api/v1/uploads', upload=fixture['path'], expected=201)
        require(ID.fullmatch(source.get('id', '')) and source.get('width') == fixture['width']
                and source.get('height') == fixture['height'] and abs(source.get('duration_ms', 0) - fixture['duration_ms']) <= 100,
                'Upload metadata differs from frozen input')
        row.update(source_id=source['id'], source_duration_ms=source['duration_ms'],
                   upload_ms=(upload['end_ns'] - upload['start_ns']) / 1e6)
        row['checks'].append('upload')
        original_path = root / 'original.mp4'
        _, original = client.request('GET', source['preview_url'], destination=original_path)
        require(original['sha256'] == fixture['sha256'] and original['bytes'] == fixture['bytes'], 'Acknowledged upload bytes changed')
        original_path.unlink()
        row['checks'].append('original_sha256')
        preview_url = '/api/v1/sources/' + source['id'] + '/preview?start_ms=' + str(fixture['preview_start_ms'])
        gate_started = time.monotonic_ns()
        with self.preview_gate:
            row['preview_client_gate_ms'] = (time.monotonic_ns() - gate_started) / 1e6
            preview_path = root / 'preview.mp4'
            _, miss = client.request('GET', preview_url, destination=preview_path)
            require(miss['bytes'] <= 16 << 20 and miss['headers'].get('x-preview-cache') == 'miss', 'Preview miss was not performed')
            row['checks'].append('preview_miss')
            hit_path = root / 'preview-hit.mp4'
            _, hit = client.request('GET', preview_url, destination=hit_path)
            require(hit['headers'].get('x-preview-cache') == 'hit' and hit['sha256'] == miss['sha256'] and hit['bytes'] == miss['bytes'],
                    'Preview cache hit returned different bytes')
            hit_path.unlink()
            row['checks'].append('preview_hit')
        row.update(preview_miss_ms=(miss['end_ns'] - miss['start_ns']) / 1e6,
                   preview_hit_ms=(hit['end_ns'] - hit['start_ns']) / 1e6, preview=miss)
        modes = list(MODES)
        random.Random(self.config['seed'] + index).shuffle(modes)
        for stage in (modes, ['native_uhd']) if fixture['height'] > 1080 else (modes,):
            active = {}
            for mode in stage:
                request = job_request(source['id'], fixture, mode)
                job, response = client.request('POST', '/api/v1/jobs', data=request, expected=202,
                                               headers={'Idempotency-Key': self.token + '-' + tx + '-' + mode})
                require(ID.fullmatch(job.get('id', '')) and job.get('status') in ('queued', 'running', 'succeeded'), 'Job was not accepted')
                cell['observer'].register(job['id'], response)
                active[job['id']] = {'mode': mode, 'submission': response, 'job_id': job['id']}
            deadline = time.monotonic() + 120
            while active:
                require(time.monotonic() < deadline, 'Accepted job exceeded external 120s test watchdog')
                listing, _ = client.request('GET', '/api/v1/jobs')
                require(isinstance(listing.get('jobs'), list), 'Aggregate job listing is missing')
                for summary in listing['jobs']:
                    if summary.get('id') not in active:
                        continue
                    require(summary['status'] in ('queued', 'running', 'succeeded'), 'Job unsuccessful/storage-wait; preserve failed sample')
                    if summary['status'] != 'succeeded':
                        continue
                    item = active.pop(summary['id'])
                    job, status_response = client.request('GET', '/api/v1/jobs/' + summary['id'])
                    require(job['status'] == 'succeeded' and len(job.get('items', [])) == 1 and job['items'][0]['status'] == 'succeeded',
                            'Successful job lost its required single item')
                    artifact = job['items'][0].get('artifact')
                    require(artifact and 0 < artifact.get('size_bytes', 0) <= OUTPUT_LIMIT, 'Artifact is absent or exceeds output bound')
                    destination = root / (item['mode'] + ('.mp3' if item['mode'] == 'mp3' else '.mp4'))
                    _, download = client.request('GET', artifact['download_url'], destination=destination)
                    require(download['bytes'] == artifact['size_bytes'], 'Download size differs from published artifact')
                    item.update(artifact=artifact, path=str(destination), sha256=download['sha256'], bytes=download['bytes'],
                                ready_observed_ns=status_response['end_ns'], download_end_ns=download['end_ns'],
                                submission_to_download_ms=(download['end_ns'] - item['submission']['start_ns']) / 1e6,
                                queue=cell['observer'].bounds(item['job_id']))
                    row['jobs'].append(item)
                if active:
                    require(cell['observer'].failure is None, 'Queue observer failed during export')
                    time.sleep(.5)
        row['checks'] += ['jobs', 'queue_bounds', 'downloads']
        row.update(download_ready_ns=time.monotonic_ns(), ready_ms=(time.monotonic_ns() - started) / 1e6,
                   preview_path=str(preview_path), preview_url=preview_url, source_url=source['preview_url'])
        row['_client'] = client
        return row

    def probe(self, path, work, frames=False):
        entries = 'stream=index,codec_type,codec_name,width,height,pix_fmt,sample_aspect_ratio,color_range,color_space,color_transfer,color_primaries,channels,sample_rate:format=duration'
        if frames:
            entries += ':frame=best_effort_timestamp_time,key_frame'
        options = ['-select_streams', 'v:0'] if frames else []
        out, _ = self.container_tool('ffprobe', ['-v', 'error', *options, '-show_entries', entries, '-of', 'json', '/work/' + path.name], work)
        return json.loads(out)

    def validate_media(self, path, fixture, mode, artifact, work):
        probe = self.probe(path, work)
        streams = probe.get('streams', [])
        audio = [s for s in streams if s['codec_type'] == 'audio']
        video = [s for s in streams if s['codec_type'] == 'video']
        require(len(audio) == 1 and audio[0].get('channels') == 2, 'Output stereo audio changed')
        duration = (artifact['actual_end_ms'] - artifact['actual_start_ms']) / 1000
        copy_timeline = None
        require(duration > 0 and abs(float(probe['format']['duration']) - duration) <= .12, 'Output duration disagrees with published bounds')
        if mode != 'copy':
            end = fixture['start_ms'] + 2000 if mode == 'native_uhd' else fixture['end_ms']
            require(artifact['actual_start_ms'] == fixture['start_ms'] and artifact['actual_end_ms'] == end,
                    'Accurate reported range changed')
        if mode != 'mp3':
            height = min(fixture['height'], 1080) if mode == 'accurate' else fixture['height']
            width = round(fixture['width'] * height / fixture['height'])
            require(len(video) == 1 and (video[0].get('width'), video[0].get('height')) == (width, height), 'Output geometry changed')
            frames = self.probe(path, work, frames=True).get('frames', [])
            pts = [float(frame['best_effort_timestamp_time']) for frame in frames
                   if 'best_effort_timestamp_time' in frame]
            if mode == 'copy':
                copy_timeline = copy_timeline_witness(frames, fixture, artifact, float(probe['format']['duration']))
            else:
                require(len(pts) >= duration * 30 - 3 and len(pts) <= duration * 30 + 3
                        and all(abs(b - a - 1 / 30) <= .002 for a, b in zip(pts, pts[1:])), 'Output lost CFR frames/timing')
        self.container_tool('ffmpeg', ['-hide_banner', '-v', 'error', '-xerror', '-i', '/work/' + path.name, '-f', 'null', '-'], work)
        pcm, _ = self.container_tool('ffmpeg', ['-v', 'error', '-i', '/work/' + path.name,
                                               '-map', '0:a:0', '-vn', '-ac', '2', '-ar', '48000', '-f', 'f32le', '-'], work)
        span = audio_span_witness(pcm, duration, artifact['actual_start_ms'], 80 if mode == 'copy' else 45)
        tones, alignment = span['windows'][0]['tones'], span['windows'][0]['alignment']
        quality = None
        quality_record = None
        if mode != 'mp3':
            source = work / 'frozen-source.mp4'
            if not source.exists():
                os.link(fixture['path'], source) if Path(fixture['path']).stat().st_dev == work.stat().st_dev else shutil.copyfile(fixture['path'], source)
            stats = path.stem + '-ssim.txt'
            height = min(fixture['height'], 1080) if mode == 'accurate' else fixture['height']
            filters = (f'[0:v]setpts=PTS-STARTPTS,scale=-2:{height}[out];'
                       f'[1:v]setpts=PTS-STARTPTS,scale=-2:{height}[src];[out][src]ssim=stats_file=/work/{stats}')
            self.container_tool('ffmpeg', ['-v', 'error', '-i', '/work/' + path.name, '-ss', str(artifact['actual_start_ms'] / 1000),
                                           '-i', '/work/frozen-source.mp4', '-t', str(duration), '-filter_complex', filters,
                                           '-an', '-f', 'null', '-'], work)
            qualities = [float(value) for value in re.findall(r'All:([0-9.]+)', (work / stats).read_text())]
            require(len(qualities) >= duration * 30 - 3, 'Quality verification skipped frames')
            quality = sum(qualities) / len(qualities)
            require(quality >= (.99 if mode == 'copy' else .90), 'Faster output failed fixed decoded-content quality witness')
            retained_stats = self.evidence.root / (self.evidence.identifier('ssim-stats') + '.txt')
            shutil.copyfile(work / stats, retained_stats)
            quality_record = {'file': retained_stats.name, 'sha256': sha(retained_stats), 'frames': len(qualities)}
        return {'probe': probe, 'stereo_tones': tones, 'audio_alignment': alignment, 'audio_span': span,
                'ssim_mean': quality, 'ssim_stats': quality_record, 'full_decode': True,
                'copy_timeline': copy_timeline}

    def finish_round(self, cell, fixture, rows):
        for row in rows:
            started = time.monotonic_ns()
            work = Path(row['preview_path']).parent
            preview = Path(row['preview_path'])
            inspected_fixture = {**fixture, 'duration_ms': row['source_duration_ms']}
            row['preview_validation'] = self.validate_preview(preview, inspected_fixture, row['preview'], cell['binary'], work)
            retained_preview = self.evidence.root / (row['id'] + '-preview.mp4')
            shutil.copyfile(preview, retained_preview)
            require(sha(retained_preview) == row['preview']['sha256'], 'Retained preview changed')
            row['preview_path'] = retained_preview.name
            for job in row['jobs']:
                job['validation'] = self.validate_media(Path(job['path']), fixture, job['mode'], job['artifact'], work)
                retained = self.evidence.root / (row['id'] + '-' + Path(job['path']).name)
                shutil.copyfile(job['path'], retained)
                require(sha(retained) == job['sha256'], 'Retained output changed')
                job['path'] = retained.name
            row['checks'] += ['full_decode', 'geometry_pts_audio_quality']
            client = row.pop('_client')
            if row['index'] == 0 or row['warmup']:
                job = row['jobs'][0]
                partial, _ = client.request('GET', job['artifact']['download_url'], expected=206, headers={'Range': 'bytes=0-31'})
                with (self.evidence.root / job['path']).open('rb') as file:
                    require(partial == file.read(32), 'Range did not return actual output bytes')
                foreign = HTTP(cell['endpoint'], self.evidence, row['id'] + '-foreign', self.cell_deadline)
                foreign.cookie = 'cutmy_session=' + base64.urlsafe_b64encode(bytes([1]) * 32).decode().rstrip('=')
                foreign.request('GET', job['artifact']['download_url'], expected=404)
            client.request('DELETE', '/api/v1/sources/' + row['source_id'], expected=204)
            row['checks'].append('delete_204')
            for path in (row['source_url'], row['preview_url'], *[job['artifact']['download_url'] for job in row['jobs']]):
                client.request('GET', path, expected=404)
            row['checks'].append('deleted_routes_404')
            row.update(validation_ms=(time.monotonic_ns() - started) / 1e6, validated_delete_ns=time.monotonic_ns(),
                       validated_workflow_ms=(time.monotonic_ns() - row['start_ns']) / 1e6)
        sql = "SELECT json_build_object('stored',stored_bytes,'reserved',reserved_bytes,'files',(SELECT count(*) FROM storage_files),'reservations',(SELECT count(*) FROM storage_reservations),'sources',(SELECT count(*) FROM sources),'jobs',(SELECT count(*) FROM jobs),'artifacts',(SELECT count(*) FROM artifacts)) FROM storage_counters WHERE id=1;"
        accounting, _, _ = cell['pg'].query(sql)
        require(set(accounting) == {'stored', 'reserved', 'files', 'reservations', 'sources', 'jobs', 'artifacts'}
                and all(value == 0 for value in accounting.values()), 'Delete round retained charged bytes, reservations or application records')
        require(not any(path.is_file() or path.is_symlink() for path in cell['media'].rglob('*')), 'Delete round retained real source/artifact/preview/work media')
        for row in rows:
            row['checks'].append('physical_and_accounting')
            require(set(row['checks']) == set(CHECKS) and len(row['checks']) == len(CHECKS), 'Transaction skipped or duplicated mandatory checks')
            row['ok'] = True
            self.evidence.event('transactions', row)
            shutil.rmtree(Path(cell['root']) / row['id'])

    def validate_preview(self, path, fixture, response, label, work):
        cadence = PREVIEW_FPS
        proof = preview_metadata_witness(self.probe(path, work), self.probe(path, work, frames=True).get('frames', []),
                                         fixture, response['headers'], cadence, self.config['binaries'][label]['version']['commit'])
        self.container_tool('ffmpeg', ['-v', 'error', '-xerror', '-i', '/work/' + path.name, '-f', 'null', '-'], work)
        duration = (proof['source_end_ms'] - proof['source_start_ms']) / 1000
        proof['audio_windows'] = []
        for offset in (0, math.floor(duration / 2), math.floor(duration) - 1):
            pcm, _ = self.container_tool('ffmpeg', ['-v', 'error', '-ss', str(offset), '-i', '/work/' + path.name,
                                                   '-t', '0.9', '-map', '0:a:0', '-vn', '-ac', '2', '-ar', '48000', '-f', 'f32le', '-'], work)
            proof['audio_windows'].append({'offset_seconds': offset, 'tones': tone_witness(pcm),
                'alignment': audio_alignment_witness(pcm, proof['source_start_ms'] + offset * 1000, 45)})
        proof.update(stereo_tones=proof['audio_windows'][0]['tones'],
                     audio_alignment=proof['audio_windows'][0]['alignment'], full_decode=True)
        source = work / 'frozen-source.mp4'
        if not source.exists():
            shutil.copyfile(fixture['path'], source)
        width, height = proof['geometry']
        stats = path.stem + '-ssim.txt'
        filters = (f'[0:v]setpts=PTS-STARTPTS,scale={width}:{height},setsar=1,fps={cadence}[out];'
                   f'[1:v]setpts=PTS-STARTPTS,scale={width}:{height},setsar=1,fps={cadence}[src];'
                   f'[out][src]ssim=stats_file=/work/{stats}')
        self.container_tool('ffmpeg', ['-v', 'error', '-i', '/work/' + path.name,
                                       '-ss', str(proof['source_start_ms'] / 1000), '-i', '/work/frozen-source.mp4',
                                       '-t', str((proof['source_end_ms'] - proof['source_start_ms']) / 1000),
                                       '-filter_complex', filters, '-an', '-f', 'null', '-'], work)
        qualities = [float(value) for value in re.findall(r'All:([0-9.]+)', (work / stats).read_text())]
        require(len(qualities) >= proof['frame_count'] - 2 and sum(qualities) / len(qualities) >= .80,
                'Decoded preview content is wrong or quality verification skipped frames')
        proof['ssim_mean'] = sum(qualities) / len(qualities)
        retained_stats = self.evidence.root / (self.evidence.identifier('preview-ssim-stats') + '.txt')
        shutil.copyfile(work / stats, retained_stats)
        proof['ssim_stats'] = {'file': retained_stats.name, 'sha256': sha(retained_stats), 'frames': len(qualities)}
        return proof

    def run_cell(self, label, definition, fixture, count, extension=False):
        result = {'binary': label, 'cell': definition[0], 'fixture_sha256': fixture['sha256'], 'transactions': [], 'rounds': [], 'ok': False}
        cell = None
        try:
            cell = self.start_cell(label, definition)
            cell['sampler'].phase = 'warmup'
            self.finish_round(cell, fixture, [self.transaction(cell, fixture, -1, True)])
            for first in range(0, count, cell['clients']):
                require(time.monotonic() < self.cell_deadline, 'Cell exceeded 10min external watchdog')
                indices = range(first, min(count, first + cell['clients']))
                cell['sampler'].phase = 'measured'
                before = cell['sampler'].snapshot()
                started = time.monotonic_ns()
                with ThreadPoolExecutor(max_workers=cell['clients']) as pool:
                    rows = list(pool.map(lambda index: self.transaction(cell, fixture, index), indices))
                ended = time.monotonic_ns()
                after = cell['sampler'].snapshot()
                require(cell['sampler'].failure is None, 'Resource observer failed during processing')
                idle_deadline = min(self.cell_deadline, time.monotonic() + 5)
                while True:
                    idle = cell['sampler'].snapshot()
                    if all(all(p['root'] for p in idle[role]['processes']) for role in ('api', 'worker')):
                        break
                    require(time.monotonic() < idle_deadline, 'Application media children still active before idle validation')
                    time.sleep(.05)
                self.evidence.event('idle', {'at_ns': time.monotonic_ns(), 'cell': definition[0], 'binary': label, 'roles': idle})
                cell['sampler'].phase = 'validation_idle'
                self.finish_round(cell, fixture, rows)
                result['transactions'] += rows
                result['rounds'].append({'start_ns': started, 'download_end_ns': ended, 'count': len(rows),
                                         'resource_before': before, 'resource_after': after,
                                         'cgroup_cpu_delta': cpu_delta(before, after)})
            require(len(result['transactions']) == count and all(row['ok'] for row in result['transactions']), 'Missing successful transactions')
            result['ok'] = True
        finally:
            failures = []
            if not result['ok'] and cell:
                try:
                    result['failure_media'] = retain_failed_media(cell['root'], self.evidence, label + '-' + definition[0])
                except BaseException as error:
                    failures.append('failure evidence: ' + type(error).__name__ + ': ' + str(error))
                    self.retain_work = True
            failures += close_all(self.local_closers)
            if cell:
                result['sampled_memory'] = cell['sampler'].summary()
                if cell['sampler'].failure_details is not None:
                    result['resource_observer_error'] = cell['sampler'].failure_details
                if cell['sampler'].failure_diagnostic_error is not None:
                    result['resource_observer_diagnostic_error'] = cell['sampler'].failure_diagnostic_error
                for name in (cell['worker'], cell['api'], cell['postgres']):
                    try:
                        stdout, stderr = self.commands.run([*self.docker.argv, 'logs', name], env=self.docker.environment)
                        logs = stdout + stderr
                        (self.evidence.root / (label + '-' + definition[0] + '-' + name.rsplit('-', 1)[-1] + '.log')).write_bytes(logs)
                        self.docker.remove(name)
                    except BaseException as error:
                        failures.append(type(error).__name__ + ': ' + str(error))
                try:
                    self.docker.run('network', 'rm', cell['network'])
                    self.docker.networks.remove(cell['network'])
                except BaseException as error:
                    failures.append(type(error).__name__ + ': ' + str(error))
            if failures:
                result['ok'] = False
                result['cleanup_errors'] = failures
            self.cells.append(result)
            self.evidence.event('cells', result)
            require(not failures, 'Cell cleanup/liveness failed; evidence retained')

    def report(self, ok):
        report = {'schema_version': 1, 'ok': ok, 'mode': self.args.mode, 'config_sha256': self.args.expected_config_sha256,
                  'harness_sha256': self.harness_sha, 'binaries': self.config['binaries'], 'fixtures': self.fixtures,
                  'runtime_tools': self.config['tools'], 'runtime_image': self.config['runtime_image'],
                  'postgres_image': self.config['postgres_image'],
                  'runtime_method_scope': 'Common CONFIG tools/image for both binaries; fixture tools retain generation identity; independent input revalidation before measurement',
                  'resource_config': self.config['resources'], 'extensions': self.config['extensions'],
                  'shared_host': True, 'dedicated_verifier_cpu': False,
                  'source_metadata_scope': 'tree/runtime source hashes externally supplied and independently preflight-receipted; executable SHA/version verified here',
                  'limitations': ['descriptive nearest-rank observations, no capacity limit/SLA',
                                  '100ms sampled RSS may miss short child peaks; summed RSS includes shared pages',
                                  'cgroup memory includes page cache and differs from RSS; lifetime peak includes startup/warmup',
                                  'memory.peak may be unavailable on this kernel; null is not inferred from RSS/current samples',
                                  'read-only SQL observer overhead is part of PG CPU; queue times are interval bounds',
                                  'idle-phase validator has no dedicated CPU on this shared host',
                                  'existing failed 32x5ms storage gate is unchanged; old-source timings are not current media timings'],
                  'cells': self.cells, 'errors': self.errors}
        for cell in report['cells']:
            if not cell['ok']:
                continue
            rows = [row for row in cell['transactions'] if not row['warmup']]
            final = self.args.mode == 'descriptive' and not cell['cell'].startswith('E-')
            cell['descriptors'] = {key: describe([row[key] for row in rows], final) for key in
                                   ('upload_ms', 'preview_miss_ms', 'preview_hit_ms', 'ready_ms', 'validated_workflow_ms')}
            cell['job_descriptors'] = {}
            for mode in (*MODES, *(['native_uhd'] if cell['cell'] == 'E-uhd' else [])):
                jobs = [job for row in rows for job in row['jobs'] if job['mode'] == mode]
                cell['job_descriptors'][mode] = {
                    'submission_to_download_ms': describe([job['submission_to_download_ms'] for job in jobs], final),
                    'queue_lower_ms': describe([job['queue']['lower_ms'] for job in jobs], final),
                    'queue_upper_ms': describe([job['queue']['upper_ms'] for job in jobs], final)}
            cell['measured_cgroup_cpu_seconds'] = {
                role: sum(round_['cgroup_cpu_delta'][role]['usage_usec'] for round_ in cell['rounds']) / 1e6
                for role in ('api', 'worker', 'postgres')}
            cell['round_download_ready_wall_ms'] = [
                (round_['download_end_ns'] - round_['start_ns']) / 1e6 for round_ in cell['rounds']]
            revision = self.config['binaries'][cell['binary']]['version']['commit']
            width, height, sar = PREVIEW_GEOMETRY[revision]
            cell['preview_policy'] = {'cadence': PREVIEW_FPS, 'revision': revision,
                                      'geometry': [width, height], 'sample_aspect_ratio': sar,
                                      'comparison_scope': '24fps window workflow with declared per-revision even-rounded geometry; accurate exports retain source CFR30'}
        return report

    def run(self):
        success = False
        self.preview_gate = threading.BoundedSemaphore(2)
        deadline = time.monotonic() + 90 * 60
        try:
            self.prepare()
            count = 3 if self.args.mode == 'pilot' else 20
            for index, definition in enumerate(CELLS):
                for label in (('before', 'after') if index % 2 == 0 else ('after', 'before')):
                    require(time.monotonic() < deadline, 'Pair exceeded 90min external test watchdog')
                    self.cell_deadline = min(deadline, time.monotonic() + 600)
                    self.run_cell(label, definition, self.fixtures['fixtures']['main'], count)
            for extension in self.config['extensions']:
                for label in ('before', 'after'):
                    self.cell_deadline = min(deadline, time.monotonic() + 600)
                    self.run_cell(label, ('E-' + extension, 'fast', 1, 1), self.fixtures['fixtures'][extension], 3 if self.args.mode == 'pilot' else 5, True)
            for label, binary in self.binaries.items():
                require(sha(binary) == self.config['binaries'][label]['binary_sha256'], 'Executable changed during experiment')
            require(sha(Path(__file__).absolute()) == self.harness_sha, 'Harness changed during experiment')
            success = True
        except BaseException as error:
            self.errors.append(type(error).__name__ + ': ' + str(error))
            raise
        finally:
            self.errors += ['local client cleanup: ' + error for error in close_all(self.local_closers)]
            try:
                cleanup_owned_work(self.docker, self.work, keep_work=self.retain_work)
                if self.retain_work:
                    self.errors.append('private work retained after preservation failure: ' + str(self.work))
            except BaseException as error:
                success = False
                self.errors.append('cleanup: ' + type(error).__name__ + ': ' + str(error))
                if self.work.exists():
                    self.errors.append('private work retained; inspect owned mounts before removal: ' + str(self.work))
            report = self.report(success and not self.errors)
            if report['ok']:
                try:
                    validate_summary(report)
                except BaseException as error:
                    report['ok'] = False
                    success = False
                    self.errors.append('verdict: ' + str(error))
            (self.evidence.root / 'summary.json').write_bytes(canonical(report))
            for path in self.evidence.root.glob('*.jsonl'):
                with path.open('rb') as source, (self.evidence.root / (path.name + '.gz')).open('xb') as target:
                    with gzip.GzipFile(filename='', mode='wb', fileobj=target, mtime=0) as compressed:
                        shutil.copyfileobj(source, compressed)
            manifest = {path.name: {'sha256': sha(path), 'bytes': path.stat().st_size}
                        for path in sorted(self.evidence.root.iterdir()) if path.is_file()}
            (self.evidence.root / 'MANIFEST.json').write_bytes(canonical(manifest))
        require(success and not self.errors, 'Mixed-media experiment or owned cleanup failed')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, required=True)
    parser.add_argument('--expected-config-sha256', required=True)
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--scratch-dir', type=Path, required=True)
    parser.add_argument('--docker', type=Path, required=True)
    parser.add_argument('--mode', choices=('pilot', 'descriptive'), required=True)
    args = parser.parse_args()
    require(args.scratch_dir.is_absolute() and args.scratch_dir.is_dir() and not args.scratch_dir.is_symlink(), 'Owned absolute scratch directory required')
    Experiment(args).run()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Mixed-media experiment failed: ' + type(error).__name__ + ': ' + str(error), file=sys.stderr)
        sys.exit(1)
