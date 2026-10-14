#!/usr/bin/env python3
"""Execute only the verified, extracted native release executable.

No application build, PATH fallback, tag creation or publication is permitted.
The database, listeners, cookies, fixtures and processes belong to this run.
"""
import argparse
import base64
import hashlib
import http.client
from http.cookies import SimpleCookie
import json
import math
import os
from pathlib import Path
import platform
import re
import shutil
import signal
import socket
import struct
import subprocess
import sys
import tempfile
import time
from urllib.parse import parse_qsl, unquote, urlencode, urlsplit, urlunsplit
import uuid

import release_manifest as m

REQUIRED_GROUPS = ('archive', 'version', 'cli', 'media', 'process', 'service', 'deletion')
PROCESS_CHECKS = ('timeout', 'descendants_observed', 'descendants_closed_before_cleanup')
COLLECTION_MEDIA_CHECKS = (
    'collection_read_only_preflight', 'collection_draft_sequence_and_maps',
    'collection_selected_final_edit', 'collection_exclusive_output_preserved',
    'collection_partial_outputs_and_budget', 'collection_draft_final_crop_consistency',
)
REQUIRED_CHECKS = {
    'archive': ('external_source_and_manifest_hash', 'full_five_archive_bundle', 'safe_exact_native_six_files',
                'embedded_go_and_documents'),
    'version': ('actual_extracted_version', 'native_os_arch', 'source_version_time_go'),
    'cli': ('help', 'invalid_argument_single_json', 'missing_input_single_json', 'no_database_for_local_cli'),
    'media': ('real_fixture_oracles', 'inspect_coded_and_display_geometry', 'accurate_mp4', 'copy_mp4', 'mp3',
              'full_decode', 'default_audio_880hz', 'rotated_corners', 'existing_output_preserved',
              'cropped_accurate_mp4', 'crop_copy_refused', 'cropped_saved_plan_joined') + COLLECTION_MEDIA_CHECKS,
    'process': PROCESS_CHECKS,
    'service': ('real_server_and_worker', 'session_upload_exact_bytes', 'preview_miss_and_hit', 'queued_job_completion',
                'download_decode_geometry_audio', 'ownership_404', 'range_206', 'same_binary_healthchecks',
                'original_display_preview_metadata', 'waveform_selected_audio_private_cache', 'waveform_high_frequency_activity',
                'cropped_api_download', 'cleanup_preserves_source_metadata_jobs_and_downloads'),
    'deletion': ('acknowledged_source_delete', 'all_routes_404', 'physical_files_absent', 'zero_stored_and_reserved',
                 'zero_ledger_entries', 'no_preview_or_work_media_remaining'),
}
COLORS = ((240, 20, 20), (20, 240, 20), (20, 20, 240), (240, 240, 240))
RECEIPT_FIELDS = {'schema_version', 'repository', 'run_id', 'artifact_id', 'artifact_name',
                  'artifact_sha256', 'artifact_bytes', 'source_commit', 'source_sha256'}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def file_hash(path):
    with Path(path).open('rb') as file:
        h = hashlib.sha256()
        while block := file.read(1024 * 1024):
            h.update(block)
        return h.hexdigest()


def native_target():
    os_name = {'Linux': 'linux', 'Darwin': 'darwin', 'Windows': 'windows'}.get(platform.system())
    arch = {'x86_64': 'amd64', 'AMD64': 'amd64', 'arm64': 'arm64', 'aarch64': 'arm64'}.get(platform.machine())
    require((os_name, arch) in m.TARGETS, 'Unsupported native runner OS/architecture')
    return os_name, arch


def runtime_version(value, source, target):
    expected = {'version': source['version'], 'commit': source['commit'],
                'build_time': source['build_time'], 'go_version': source['toolchain'],
                'os': target[0], 'arch': target[1]}
    require(value == expected, 'Executed binary version/source/native-target identity mismatch')


def error_envelope(stdout, stderr, exit_code, expected_exit, code):
    require(exit_code == expected_exit and not stdout, 'CLI failure exit/stdout contract mismatch')
    try:
        value = json.loads(stderr)
    except (ValueError, UnicodeError):
        raise RuntimeError('CLI failure did not contain one JSON envelope') from None
    require(set(value) == {'error'} and isinstance(value['error'], dict)
            and set(value['error']) == {'code', 'message'} and value['error']['code'] == code
            and isinstance(value['error']['message'], str) and value['error']['message'],
            'CLI failure envelope/code mismatch')
    return value


def validate_receipt(path, source_sha, commit):
    value = m.read_json(path)
    require(set(value) == RECEIPT_FIELDS and type(value['schema_version']) is int
            and value['schema_version'] == 1, 'Unexpected packaging artifact receipt')
    require(type(value['run_id']) is int and value['run_id'] > 0
            and type(value['artifact_id']) is int and value['artifact_id'] > 0
            and type(value['artifact_bytes']) is int and value['artifact_bytes'] > 0,
            'Invalid packaging artifact receipt identity')
    require(isinstance(value['repository'], str) and re.fullmatch(r'[^/\s]+/[^/\s]+', value['repository'])
            and isinstance(value['artifact_name'], str) and value['artifact_name']
            and m.HASH.fullmatch(value['artifact_sha256'])
            and value['source_sha256'] == source_sha and value['source_commit'] == commit,
            'Packaging artifact receipt source/digest mismatch')
    return dict(value, sha256=file_hash(path))


def checked_binary(path, expected_sha):
    path = Path(path)
    require(path.is_absolute() and path.is_file() and not path.is_symlink(),
            'The verified extracted absolute executable path is required')
    require(file_hash(path) == expected_sha, 'Extracted executable changed')
    return path


def process_case_input(source, work, label):
    """Scope helper markers through ffprobe's explicit input, never its env."""
    source, work = Path(source), Path(work)
    require(source.is_absolute() and source.is_file() and not source.is_symlink()
            and work.is_absolute() and work.is_dir() and label in ('timeout', 'sigint', 'sigterm'),
            'Invalid owned process fixture input')
    destination = work / (label + '-process-input.mp4')
    with source.open('rb') as original, destination.open('xb') as copy:
        shutil.copyfileobj(original, copy)
    require(file_hash(destination) == file_hash(source), 'Process fixture copy changed')
    return destination, Path(str(destination) + '.tree.json')


def preserve_process_markers(marker, evidence, label):
    destination = Path(evidence) / 'process-markers'
    destination.mkdir(exist_ok=True)
    records = []
    for suffix in ('', '.1', '.2', '.pid.0', '.pid.1', '.pid.2'):
        path = Path(str(marker) + suffix)
        if not path.exists() and not path.is_symlink():
            continue
        require(path.is_file() and not path.is_symlink(), 'Process marker is not an owned regular file')
        with path.open('rb') as file:
            data = file.read((16 << 10) + 1)
        require(len(data) <= 16 << 10, 'Process marker exceeded its bounded evidence limit')
        retained = destination / (label + '-tree.json' + suffix)
        with retained.open('xb') as file:
            file.write(data)
        records.append({'path': str(retained.relative_to(evidence)), 'bytes': len(data),
                        'sha256': hashlib.sha256(data).hexdigest()})
    return records


def required_checks(target_os):
    result = dict(REQUIRED_CHECKS)
    if target_os != 'windows':
        result['process'] += ('sigint', 'sigterm')
    return result


def validate_report(report):
    """Fail closed; aggregate must additionally bind external CI/job identities."""
    require(report.get('schema_version') == 1 and report.get('ok') is True, 'Native report is unsuccessful')
    require(report.get('required_groups') == list(REQUIRED_GROUPS)
            and set(report.get('groups', {})) == set(REQUIRED_GROUPS), 'Missing or extra required native groups')
    expected_checks = required_checks(report['target']['os'])
    for name, group in report['groups'].items():
        require(set(group) == {'status', 'checks', 'evidence'} and group['status'] == 'pass'
                and isinstance(group['checks'], list) and group['checks']
                and isinstance(group['evidence'], dict), f'Native group did not pass: {name}')
        names = []
        for check in group['checks']:
            require(isinstance(check, dict) and set(check) == {'name', 'ok'} and check['ok'] is True
                    and isinstance(check['name'], str), f'Invalid native check: {name}')
            names.append(check['name'])
        require(len(names) == len(set(names)) and set(names) == set(expected_checks[name]),
                f'Missing or extra native checks: {name}')
    require(not report.get('errors') and report['binary']['sha256_before'] == report['binary']['sha256_after']
            and report['binary']['sha256_before'] == report['groups']['archive']['evidence']['binary_sha256'],
            'Native executable identity changed or failures were suppressed')
    source = report['source']
    runtime_version(report['runtime_version'], source, (report['target']['os'], report['target']['arch']))
    require(m.HASH.fullmatch(source['sha256']) and m.HASH.fullmatch(report['manifest_sha256'])
            and m.HASH.fullmatch(report['archive']['sha256'])
            and report['artifact_receipt']['source_sha256'] == source['sha256']
            and report['artifact_receipt']['source_commit'] == source['commit'], 'Native source/archive receipt identity mismatch')


class Commands:
    def __init__(self, evidence):
        self.evidence = evidence
        self.count = 0
        self.records = []

    def run(self, command, *, env=None, timeout=45, expected=0, stdin=None):
        self.count += 1
        label = f'call-{self.count:03d}'
        out_path, err_path = self.evidence / (label + '.stdout'), self.evidence / (label + '.stderr')
        started = time.monotonic()
        record = {'id': label, 'command': [str(arg) for arg in command], 'timeout_seconds': timeout,
                  'stdout': out_path.name, 'stderr': err_path.name}
        self.records.append(record)
        try:
            with out_path.open('xb') as out, err_path.open('xb') as err:
                result = subprocess.run([str(arg) for arg in command], env=env, input=stdin,
                                        stdout=out, stderr=err, timeout=timeout)
            record['exit'] = result.returncode
        except subprocess.TimeoutExpired:
            record['timeout'] = True
            raise RuntimeError(f'{label} exceeded its external watchdog') from None
        finally:
            record['seconds'] = time.monotonic() - started
            (self.evidence / 'calls.json').write_bytes(m.canonical(self.records))
        require(out_path.stat().st_size <= 8 << 20 and err_path.stat().st_size <= 8 << 20,
                f'{label} output exceeded the bounded smoke output limit')
        stdout, stderr = out_path.read_bytes(), err_path.read_bytes()
        if expected is not None:
            require(result.returncode == expected, f'{label} returned unexpected exit {result.returncode}; see raw call files')
        return stdout, stderr, result.returncode


class PostgreSQL:
    def __init__(self, url, commands, psql):
        self.url = urlsplit(url)
        require(self.url.scheme in ('postgres', 'postgresql') and self.url.hostname in ('127.0.0.1', 'localhost'),
                'Native distribution acceptance requires an isolated loopback PostgreSQL URL')
        require(all(key == 'sslmode' for key, _ in parse_qsl(self.url.query)),
                'Native test PostgreSQL URL accepts only sslmode; host/service overrides are forbidden')
        self.commands, self.psql = commands, psql
        self.name = 'cutmy_distribution_' + uuid.uuid4().hex
        self.created = False
        self.env = {key: value for key, value in os.environ.items() if not key.startswith('PG')}
        self.env.update(PGHOST=self.url.hostname, PGPORT=str(self.url.port or 5432),
                        PGUSER=unquote(self.url.username or 'postgres'), PGPASSWORD=unquote(self.url.password or ''),
                        PGSSLMODE=dict(parse_qsl(self.url.query)).get('sslmode', 'prefer'), PGCONNECT_TIMEOUT='5')
        # No connection strings or credentials are arguments or report fields.
        self.admin = unquote(self.url.path.lstrip('/') or 'postgres')
        require(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', self.admin), 'Native test PostgreSQL requires a plain database name')

    def sql(self, statement, admin=False):
        data, _, _ = self.commands.run([self.psql, '-X', '-A', '-t', '--no-password',
                                       '--dbname', self.admin if admin else self.name,
                                       '--set', 'ON_ERROR_STOP=1', '--command', statement], env=self.env, timeout=10)
        return data.decode().strip()

    def create(self):
        self.sql(f'CREATE DATABASE "{self.name}"', admin=True)
        self.created = True

    def dsn(self):
        query = [(key, value) for key, value in parse_qsl(self.url.query) if not key.startswith('pool_')]
        return urlunsplit((self.url.scheme, self.url.netloc, '/' + self.name, urlencode(query), ''))

    def close(self):
        if self.created:
            self.sql(f'DROP DATABASE "{self.name}" WITH (FORCE)', admin=True)
            self.created = False


class Client:
    def __init__(self, port, evidence, records=None):
        self.port, self.evidence = port, evidence
        self.cookie = ''
        self.records = records if records is not None else []

    def request(self, method, path, *, raw=None, data=None, headers=None, expected=200):
        fields = dict(headers or {})
        if self.cookie:
            fields['Cookie'] = self.cookie
        if data is not None:
            raw = json.dumps(data).encode()
            fields['Content-Type'] = 'application/json'
        connection = http.client.HTTPConnection('127.0.0.1', self.port, timeout=15)
        try:
            connection.request(method, path, raw, fields)
            response = connection.getresponse()
            content = response.read(8 * 1024 * 1024 + 1)
            require(len(content) <= 8 << 20, 'HTTP smoke response exceeded its size bound')
            kept_headers = {key: value for key, value in response.getheaders()
                            if key.lower() in ('content-type', 'content-length', 'content-range',
                                               'content-disposition', 'x-preview-cache', 'x-source-display-width',
                                               'x-source-display-height', 'x-waveform-step-ms')}
            self.records.append({'method': method, 'path': path, 'status': response.status,
                                 'bytes': len(content), 'sha256': m.digest(content), 'headers': kept_headers})
            (self.evidence / 'http.json').write_bytes(m.canonical(self.records))
            if response.getheader('Set-Cookie'):
                cookie = SimpleCookie(response.getheader('Set-Cookie'))
                require('cutmy_session' in cookie, 'Owner session cookie is absent')
                self.cookie = 'cutmy_session=' + cookie['cutmy_session'].value
            require(response.status == expected, f'{method} {path} returned {response.status}, expected {expected}')
            if content and 'application/json' in response.getheader('Content-Type', ''):
                return json.loads(content), kept_headers
            return content, kept_headers
        finally:
            connection.close()


class Native:
    def __init__(self, args):
        self.args = args
        self.target = tuple(args.target.split('/'))
        require(self.target == native_target(), 'Requested target differs from the actual native runner')
        self.directory = args.directory.resolve()
        self.evidence = args.evidence_dir.resolve()
        require(not self.evidence.exists(), 'Evidence directory must be new; stale evidence cannot substitute')
        require(not self.evidence.is_relative_to(self.directory), 'Evidence must be outside the sealed bundle')
        m.outside_repo(self.evidence, Path(__file__).resolve().parents[1])
        self.evidence.mkdir(parents=True)
        self.commands = Commands(self.evidence)
        self.work = Path(tempfile.mkdtemp(prefix='cutmy-native-')).resolve()
        m.outside_repo(self.work, Path(__file__).resolve().parents[1])
        self.binary = None
        self.expected_binary_sha = None
        self.processes = []
        self.service_records = []
        self.http_records = []
        self.pg = None
        self.service_env = None
        self.report = {'schema_version': 1, 'ok': False, 'required_groups': list(REQUIRED_GROUPS),
                       'groups': {name: {'status': 'not_run', 'checks': [], 'evidence': {}} for name in REQUIRED_GROUPS},
                       'target': {'os': self.target[0], 'arch': self.target[1]}, 'errors': [],
            'run': {'id': os.environ.get('GITHUB_RUN_ID', 'local'),
                               'attempt': os.environ.get('GITHUB_RUN_ATTEMPT', 'local'),
                               'job': os.environ.get('GITHUB_JOB', 'local'),
                               'workflow_sha': os.environ.get('GITHUB_SHA', 'local')},
                       'tools': {'python': platform.python_version()}}

    def stage(self, name, function):
        group = self.report['groups'][name]
        try:
            checks, evidence = function()
            require(checks and len(checks) == len(set(checks))
                    and set(checks) == set(required_checks(self.target[0])[name]), 'Native stage did not execute its required checks')
            group.update(status='pass', checks=[{'name': check, 'ok': True} for check in checks], evidence=evidence)
        except Exception as error:
            group['status'] = 'fail'
            # Own synthetic paths are safe; never serialize args/env/connection strings.
            self.report['errors'].append(f'{name}: {type(error).__name__}: {error}')
            raise
        finally:
            self.save()

    def save(self):
        (self.evidence / 'native-report.json').write_bytes(m.canonical(self.report))

    def archive(self):
        source = m.load_source(self.args.source, expected_sha256=self.args.expected_source_sha256)
        self.resolved_source = source
        require(m.HASH.fullmatch(self.args.expected_manifest_sha256)
                and file_hash(self.directory / 'MANIFEST.json') == self.args.expected_manifest_sha256,
                'Externally supplied MANIFEST digest mismatch')
        self.report['artifact_receipt'] = validate_receipt(self.args.artifact_receipt,
                                                         self.args.expected_source_sha256, source['commit'])
        if os.environ.get('GITHUB_RUN_ID'):
            require(self.report['artifact_receipt']['run_id'] == int(os.environ['GITHUB_RUN_ID'])
                    and self.report['artifact_receipt']['repository'] == os.environ.get('GITHUB_REPOSITORY'),
                    'Packaging artifact receipt belongs to a different hosted run/repository')
        self.report['source'] = {key: source[key] for key in ('commit', 'tree', 'mode', 'version', 'build_time', 'toolchain')}
        self.report['source']['sha256'] = self.args.expected_source_sha256
        self.report['manifest_sha256'] = self.args.expected_manifest_sha256
        manifest = m.verify(self.directory, source)
        self.report['tools']['go'] = {'path': str(Path(shutil.which('go')).resolve()),
                                       'version': source['toolchain'],
                                       'scope': 'Exact go env GOVERSION and all five embedded build infos checked by bundle verifier'}
        entry = next(item for item in manifest['archives'] if (item['os'], item['arch']) == self.target)
        contents = m.read_archive(self.directory / entry['name'], source, *self.target)
        extracted = self.work / 'extracted'
        extracted.mkdir(mode=0o700)
        for name, data in contents.items():
            file = extracted / name
            with file.open('xb') as out:
                out.write(data)
            file.chmod(0o755 if name in ('cutmy', 'cutmy.exe') else 0o644)
        binary_name = 'cutmy.exe' if self.target[0] == 'windows' else 'cutmy'
        self.expected_binary_sha = entry['contents'][binary_name]['sha256']
        self.binary = checked_binary(extracted / binary_name, self.expected_binary_sha)
        self.report['archive'] = {key: entry[key] for key in ('name', 'sha256', 'bytes')}
        self.report['binary'] = {'absolute_path': str(self.binary), 'bytes': self.binary.stat().st_size,
                                 'sha256_before': file_hash(self.binary), 'sha256_after': None}
        return ['external_source_and_manifest_hash', 'full_five_archive_bundle', 'safe_exact_native_six_files',
                'embedded_go_and_documents'], {'binary_sha256': self.expected_binary_sha,
                                               'contents': entry['contents'], 'go_build_info': entry['go_build_info']}

    def app_env(self, **extra):
        env = {key: value for key, value in os.environ.items() if not key.startswith('PG')}
        for key in ('DATABASE_URL', 'DATA_DIR', 'LISTEN_ADDR', 'PUBLIC_ORIGIN', 'WORKER_HEALTH_PATH',
                    'FFMPEG_PATH', 'FFPROBE_PATH', 'YTDLP_PATH', 'CUTMY_DIST_PROCESS_MARKER',
                    'COOKIE_SECURE', 'TRUST_PROXY', 'FFMPEG_PROFILE', 'FFMPEG_THREADS', 'WORKER_CONCURRENCY',
                    'MAX_SOURCE_BYTES', 'MAX_FETCH_BYTES', 'MAX_OUTPUT_BYTES', 'MAX_STORAGE_BYTES', 'MAX_OWNER_BYTES',
                    'MAX_RANGE_MS', 'MAX_JOB_MS', 'MAX_RANGES', 'MAX_ACTIVE_JOBS', 'MUTATIONS_PER_MINUTE',
                    'STORAGE_SAFETY_BYTES', 'STORAGE_WAIT_TIMEOUT', 'JOB_TIMEOUT', 'SOURCE_TIMEOUT',
                    'UPLOAD_TIMEOUT', 'SOURCE_TTL', 'ARTIFACT_TTL'):
            env.pop(key, None)
        env.update(extra)
        return env

    def app(self, *args, env=None, **kwargs):
        checked_binary(self.binary, self.expected_binary_sha)
        return self.commands.run([self.binary, *args], env=env or self.app_env(), **kwargs)

    def version(self):
        stdout, stderr, _ = self.app('version')
        require(not stderr, 'Version wrote unexpected diagnostics')
        value = json.loads(stdout)
        runtime_version(value, self.report['source'], self.target)
        self.report['runtime_version'] = value
        return ['actual_extracted_version', 'native_os_arch', 'source_version_time_go'], value

    def cli(self):
        stdout, _, _ = self.app('--help')
        require(b'cutmy' in stdout and b'clip' in stdout, 'Archived help is incomplete')
        for args, code, exit_code in ((('clip', '--unknown-distribution-flag'), 'invalid_argument', 2),
                                      (('inspect', '--input', str(self.work / 'missing.mp4')), 'input_not_found', 1)):
            out, err, status = self.app('--json-errors', *args, expected=None)
            error_envelope(out, err, status, exit_code, code)
        return ['help', 'invalid_argument_single_json', 'missing_input_single_json', 'no_database_for_local_cli'], {}

    def tools(self):
        for name in ('ffmpeg', 'ffprobe', 'psql'):
            path = shutil.which(name)
            require(path, f'Mandatory native acceptance tool is unavailable: {name}')
            # Multicall wrappers such as Debian's psql dispatch by argv[0].
            # Keep the selected invocation name when making it absolute.
            path = Path(path).absolute()
            setattr(self, name, path)
            out, err, _ = self.commands.run([path, '--version' if name == 'psql' else '-version'], timeout=10)
            lines = (out or err).decode(errors='replace').splitlines()
            require(lines, f'{name} did not report its version')
            self.report['tools'][name] = {'path': str(path), 'version': lines[0]}

    def ff(self, *args, **kwargs):
        return self.commands.run([self.ffmpeg, '-v', 'error', '-nostdin', '-y', '-filter_threads', '1',
                                  '-filter_complex_threads', '1', *args], **kwargs)[0]

    def probe(self, path):
        out, _, _ = self.commands.run([self.ffprobe, '-v', 'error', '-show_streams', '-show_format', '-of', 'json', path])
        return json.loads(out)

    def video(self, path):
        streams = self.probe(path)['streams']
        selected = [s for s in streams if s['codec_type'] == 'video' and not s.get('disposition', {}).get('attached_pic')]
        require(selected, 'Decoded fixture/output has no playable video')
        return selected[0]

    def frame(self, path, width, height, order, at=0):
        raw = self.ff('-ss', str(at), '-i', path, '-map', '0:v:0', '-frames:v', '1', '-pix_fmt', 'rgb24', '-f', 'rawvideo', '-')
        require(len(raw) == width * height * 3, 'Decoded video display geometry mismatch')
        actual = []
        for index, (x, y) in enumerate(((width // 4, height // 4), (3 * width // 4, height // 4),
                                        (width // 4, 3 * height // 4), (3 * width // 4, 3 * height // 4))):
            color = tuple(raw[(y * width + x) * 3:(y * width + x) * 3 + 3])
            require(all(abs(a - b) <= 35 for a, b in zip(color, COLORS[order[index]])),
                    'Decoded asymmetric video corner/color mismatch')
            actual.append(color)
        return actual

    def tone(self, path, frequency=880, selected=0):
        raw = self.ff('-ss', '0.2', '-i', path, '-map', f'0:a:{selected}', '-t', '0.3', '-ac', '1',
                      '-ar', '48000', '-f', 's16le', '-')
        require(len(raw) >= 4800 * 2 and len(raw) % 2 == 0, 'Decoded audio interval is missing')
        values = struct.unpack('<' + 'h' * (len(raw) // 2), raw)
        power = {}
        for hz in (440, 880):
            real = sum(v * math.cos(2 * math.pi * hz * i / 48000) for i, v in enumerate(values))
            imaginary = sum(v * math.sin(2 * math.pi * hz * i / 48000) for i, v in enumerate(values))
            power[hz] = real * real + imaginary * imaginary
        require(power[frequency] > 15 * power[1320 - frequency], 'Archived export selected the wrong audio track')
        return {'frequency_hz': frequency, 'relative_power': power[frequency] / max(1, power[1320 - frequency])}

    def decode(self, path):
        self.ff('-i', path, '-map', '0:v?', '-map', '0:a?', '-f', 'null', '-')

    def fixture(self):
        width, height = 160, 96
        image = self.work / 'corners.ppm'
        pixels = bytearray()
        for y in range(height):
            for x in range(width):
                pixels.extend(COLORS[(y // (height // 2)) * 2 + x // (width // 2)])
        image.write_bytes(f'P6\n{width} {height}\n255\n'.encode() + pixels)
        plain, rotated = self.work / 'unrotated.mp4', self.work / 'input.mp4'
        self.ff('-loop', '1', '-i', image, '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
                '-f', 'lavfi', '-i', 'sine=frequency=880:sample_rate=48000', '-t', '4', '-r', '25',
                '-map', '0:v:0', '-map', '1:a:0', '-map', '2:a:0', '-vf',
                'scale=in_range=full:out_color_matrix=bt709:out_range=tv,format=yuv420p,setsar=1',
                '-c:v', 'libx264', '-threads', '1', '-g', '25', '-bf', '0',
                '-x264-params', 'colorprim=bt709:transfer=bt709:colormatrix=bt709',
                '-color_primaries', 'bt709', '-color_trc', 'bt709', '-colorspace', 'bt709', '-color_range', 'tv',
                '-c:a', 'aac', '-b:a', '96k', '-disposition:a:0', '0', '-disposition:a:1', 'default', plain)
        info = self.probe(plain)
        video = self.video(plain)
        require(all(video.get(key) == value for key, value in {'pix_fmt': 'yuv420p', 'color_range': 'tv',
                    'color_space': 'bt709', 'color_transfer': 'bt709', 'color_primaries': 'bt709'}.items()),
                'Synthetic input BT.709/limited-range labels are invalid')
        raw_yuv = self.ff('-i', plain, '-map', '0:v:0', '-frames:v', '1', '-pix_fmt', 'yuv420p', '-f', 'rawvideo', '-')
        require(len(raw_yuv) == width * height * 3 // 2, 'Synthetic input native YUV planes are incomplete')
        # ITU-R BT.709-6 §§3.2-3.4; independently computed from authored RGB,
        # rather than a second invocation of the fixture conversion filter.
        for index, (x, y) in enumerate(((width // 4, height // 4), (3 * width // 4, height // 4),
                                       (width // 4, 3 * height // 4), (3 * width // 4, 3 * height // 4))):
            r, g, b = (value / 255 for value in COLORS[index])
            luma = .2126 * r + .7152 * g + .0722 * b
            expected = (16 + 219 * luma, 128 + 224 * (b - luma) / 1.8556,
                        128 + 224 * (r - luma) / 1.5748)
            chroma = (y // 2) * (width // 2) + x // 2
            actual = (raw_yuv[y * width + x], raw_yuv[width * height + chroma],
                      raw_yuv[width * height + width * height // 4 + chroma])
            require(all(abs(a - e) <= 4 for a, e in zip(actual, expected)), 'Synthetic input physical YUV matrix mismatch')
        audio = [s for s in info['streams'] if s['codec_type'] == 'audio']
        require(len(audio) == 2 and audio[0]['disposition']['default'] == 0 and audio[1]['disposition']['default'] == 1,
                'Synthetic fixture does not have distinct first/default audio streams')
        self.frame(plain, width, height, (0, 1, 2, 3))
        self.tone(plain, 440, 0)
        self.tone(plain, 880, 1)
        self.ff('-i', plain, '-map', '0', '-c', 'copy', '-metadata:s:v:0', 'rotate=90', rotated)
        rotation = lambda s: next((int(x['rotation']) % 360 for x in s.get('side_data_list', []) if 'rotation' in x), 0)
        if rotation(self.video(rotated)) != 90:
            self.ff('-display_rotation', '90', '-i', plain, '-map', '0', '-c', 'copy', rotated)
        require(rotation(self.video(rotated)) == 90, 'Synthetic fixture did not retain its real display matrix')
        require(len(self.probe(rotated)['streams']) == 3, 'Rotation fixture lost alternate audio streams')
        self.frame(rotated, height, width, (1, 3, 0, 2))
        self.input = rotated
        return {'sha256': file_hash(rotated), 'probe': self.probe(rotated)}

    def media(self):
        self.tools()
        fixture = self.fixture()
        env = self.app_env(FFMPEG_PATH=str(self.ffmpeg), FFPROBE_PATH=str(self.ffprobe))
        out, err, _ = self.app('inspect', '--input', self.input, env=env)
        info = json.loads(out)
        require(not err and info.get('width') == 160 and info.get('height') == 96
                and info.get('display_width') == 96 and info.get('display_height') == 160
                and 3900 <= info['duration_ms'] <= 4100, 'Archived inspection geometry/duration mismatch')
        outputs = {}
        for name, mode, suffix in (('accurate', 'accurate', '.mp4'), ('copy', 'copy', '.mp4'), ('mp3', 'accurate', '.mp3')):
            path = self.work / (name + suffix)
            out, err, _ = self.app('clip', '--input', self.input, '--output', path, '--start', '0', '--end', '2',
                                  '--mode', mode, '--threads', '1', '--timeout', '30s', env=env)
            result = json.loads(out)
            require(not err and result['size_bytes'] == path.stat().st_size and result['size_bytes'] > 0,
                    'Archived clip acknowledgement differs from actual output')
            require(0 <= result['actual_start_ms'] <= 100 and 1900 <= result['actual_end_ms'] <= 2100,
                    'Archived clip acknowledged invalid real time bounds')
            self.decode(path)
            output_info = self.probe(path)
            require(1.8 <= float(output_info['format']['duration']) <= 2.2, 'Decoded clip has incorrect duration')
            if suffix == '.mp4':
                self.frame(path, 96, 160, (1, 3, 0, 2))
                if mode == 'accurate':
                    video = self.video(path)
                    require((video['width'], video['height']) == (96, 160)
                            and video.get('sample_aspect_ratio') == '1:1', 'Accurate clip did not normalize geometry')
                else:
                    video = self.video(path)
                    require((video['width'], video['height']) == (160, 96), 'Copy changed coded video geometry')
            outputs[name] = {'sha256': file_hash(path), 'probe': output_info, 'tone': self.tone(path)}
            self.app('clip', '--input', self.input, '--output', path, '--start', '0', '--end', '1',
                     env=env, expected=1)
            require(file_hash(path) == outputs[name]['sha256'], 'Existing output was replaced by failed clip')
        crop = {'x': 0, 'y': 0, 'width': .5, 'height': .5}
        cropped = self.work / 'cropped.mp4'
        out, err, _ = self.app('clip', '--input', self.input, '--output', cropped, '--start', '0', '--end', '2',
                              '--crop', '0,0,0.5,0.5', '--threads', '1', '--timeout', '30s', env=env)
        require(not err and json.loads(out)['size_bytes'] == cropped.stat().st_size, 'Crop CLI publication mismatch')
        self.decode(cropped)
        self.frame(cropped, 48, 80, (1, 1, 1, 1))
        self.tone(cropped)
        refused = self.work / 'refused-crop-copy.mp4'
        out, err, code = self.app('--json-errors', 'clip', '--input', self.input, '--output', refused,
                                  '--end', '1', '--mode', 'copy', '--crop', '0,0,0.5,0.5', env=env, expected=None)
        error_envelope(out, err, code, 2, 'invalid_argument')
        require(not refused.exists(), 'Incompatible crop silently published copied media')
        plan = self.work / 'crop-plan.json'
        plan.write_text(json.dumps({'version': 1, 'input': str(self.input), 'output_mode': 'joined',
            'ranges': [{'start_ms': 2500, 'end_ms': 3500, 'label': 'Later'}, {'start_ms': 0, 'end_ms': 1000, 'label': 'Earlier'}],
            'options': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate', 'crop': crop}}), encoding='utf-8')
        joined = self.work / 'crop-joined.mp4'
        out, err, _ = self.app('batch', '--plan', plan, '--output', joined, '--threads', '1', '--timeout', '30s', env=env)
        batch = json.loads(out)
        require(not err and len(batch['outputs']) == 1 and
                [s['source_start_ms'] for s in batch['outputs'][0]['segments']] == [2500, 0], 'Saved crop plan order changed')
        self.decode(joined)
        self.frame(joined, 48, 80, (1, 1, 1, 1))
        self.tone(joined)
        outputs['cropped'] = {'sha256': file_hash(cropped), 'probe': self.probe(cropped)}
        outputs['cropped_joined'] = {'sha256': file_hash(joined), 'probe': self.probe(joined)}
        collection = self.collection_media(env)
        return ['real_fixture_oracles', 'inspect_coded_and_display_geometry', 'accurate_mp4', 'copy_mp4',
                'mp3', 'full_decode', 'default_audio_880hz', 'rotated_corners', 'existing_output_preserved',
                'cropped_accurate_mp4', 'crop_copy_refused', 'cropped_saved_plan_joined', *COLLECTION_MEDIA_CHECKS], {
                    'fixture': fixture, 'inspect': info, 'outputs': outputs, 'collection': collection}

    def collection_media(self, env):
        # Authored temporal colors are independent of the reported segment map:
        # a reversed/corrupt assembly cannot pass by returning plausible JSON.
        source = self.work / 'collection-source.mp4'
        self.ff('-f', 'lavfi', '-i', 'color=c=0x1414f0:s=960x1440:r=25:d=4',
                '-f', 'lavfi', '-i', 'sine=frequency=880:sample_rate=48000:duration=4',
                '-vf', "drawbox=color=0xf01414:t=fill:enable='lt(t,2)'",
                '-c:v', 'libx264', '-preset', 'ultrafast', '-threads', '1', '-g', '25', '-bf', '0',
                '-c:a', 'aac', '-b:a', '96k', source)
        self.frame(source, 960, 1440, (0, 0, 0, 0), at=.5)
        self.frame(source, 960, 1440, (2, 2, 2, 2), at=2.5)
        self.tone(source)
        root = self.work / 'collection'
        root.mkdir()
        plan_path = root / 'plan.json'
        destination = root / 'outputs'
        destination.mkdir()
        plan = {'version': 1, 'input': str(source),
                'options': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate'},
                'clips': [
                    {'id': 'assembled', 'label': 'Blue then red',
                     'ranges': [{'start_ms': 2500, 'end_ms': 3000, 'label': 'Blue'},
                                {'start_ms': 500, 'end_ms': 1000, 'label': 'Red'}],
                     'options': {'crop': {'x': 0, 'y': 0, 'width': .5, 'height': 1}}},
                    {'id': 'single', 'label': 'Red', 'ranges': [{'start_ms': 500, 'end_ms': 1500}]},
                ]}
        plan_path.write_bytes(m.canonical(plan))
        source_sha = file_hash(source)
        common = ('--plan', plan_path, '--output', destination, '--threads', '1', '--timeout', '30s')
        out, err, _ = self.app('collection', 'validate', *common, '--rendition', 'draft', env=env)
        validation = json.loads(out)
        require(not err and validation.get('version') == 1 and validation.get('valid') is True
                and validation.get('rendition') == 'draft'
                and [clip['id'] for clip in validation['clips']] == ['assembled', 'single']
                and all(clip['requested_duration_ms'] == 1000 for clip in validation['clips'])
                and not list(destination.iterdir()) and file_hash(source) == source_sha,
                'Collection preflight rendered media, changed input or returned the wrong plan')

        def verify_output(output, path, width, height, ranges, colors):
            require(Path(output['path']) == path and output['size_bytes'] == path.stat().st_size > 0,
                    'Collection output acknowledgement does not identify the real file')
            require([(segment['source_start_ms'], segment['source_end_ms']) for segment in output['segments']] == ranges,
                    'Collection segment map changed source order or accurate boundaries')
            require([(item['start_ms'], item['end_ms']) for item in output['requested_ranges']] == ranges,
                    'Collection output lost its requested source map')
            elapsed = 0
            for segment in output['segments']:
                require(segment['output_start_ms'] == elapsed
                        and segment['output_end_ms'] > elapsed
                        and segment['output_end_ms'] - elapsed == segment['source_end_ms'] - segment['source_start_ms'],
                        'Collection segment map has gaps, overlap or altered accurate boundaries')
                elapsed = segment['output_end_ms']
            self.decode(path)
            probe = self.probe(path)
            require(abs(float(probe['format']['duration']) - elapsed / 1000) <= .2,
                    'Collection independently decoded duration disagrees with its source map')
            video = self.video(path)
            require((video['width'], video['height']) == (width, height), 'Collection crop or rendition geometry changed')
            for at, color in colors:
                self.frame(path, width, height, (color,) * 4, at=at)
            return {'sha256': file_hash(path), 'probe': probe, 'tone': self.tone(path), 'segments': output['segments']}

        out, err, _ = self.app('collection', 'export', *common, '--rendition', 'draft', env=env)
        draft = json.loads(out)
        require(not err and draft.get('version') == 1 and draft.get('rendition') == 'draft'
                and [output['id'] for output in draft['outputs']] == ['assembled', 'single'],
                'Collection draft returned the wrong independent clips')
        files = {}
        files['assembled_draft'] = verify_output(draft['outputs'][0], destination / 'assembled.draft.mp4',
                                                 240, 720, [(2500, 3000), (500, 1000)], [(.2, 2), (.7, 0)])
        files['single_draft'] = verify_output(draft['outputs'][1], destination / 'single.draft.mp4',
                                              480, 720, [(500, 1500)], [(.2, 0)])
        draft_hashes = {path.name: file_hash(path) for path in destination.iterdir()}

        plan['clips'][0]['label'] = 'Edited red then blue'
        plan['clips'][0]['ranges'] = [{'start_ms': 500, 'end_ms': 1000, 'label': 'Red'},
                                     {'start_ms': 2500, 'end_ms': 3000, 'label': 'Blue'}]
        plan_path.write_bytes(m.canonical(plan))
        out, err, _ = self.app('collection', 'export', *common, '--clips', 'assembled', env=env)
        final = json.loads(out)
        require(not err and final.get('rendition') == 'final'
                and [output['id'] for output in final['outputs']] == ['assembled']
                and not (destination / 'single.final.mp4').exists()
                and all(file_hash(destination / name) == sha for name, sha in draft_hashes.items()),
                'Selected collection edit re-exported or modified an unselected/previous result')
        files['assembled_final'] = verify_output(final['outputs'][0], destination / 'assembled.final.mp4',
                                                 480, 1440, [(500, 1000), (2500, 3000)], [(.2, 0), (.7, 2)])
        protected = {path.name: file_hash(path) for path in destination.iterdir()}
        out, err, code = self.app('--json-errors', 'collection', 'export', *common, '--clips', 'assembled',
                                  env=env, expected=None)
        refusal = json.loads(err)
        require(code == 1 and not out and refusal.get('error', {}).get('code') == 'output_exists'
                and refusal['error'].get('clip_id') == 'assembled'
                and {path.name: file_hash(path) for path in destination.iterdir()} == protected,
                'Collection collision overwrote or acknowledged an existing output')

        # Measure a one-fragment output first, then leave only 128 bytes for the
        # next clip. This verifies actual partial publication, not a preflight
        # collision that must refuse the complete selection before rendering.
        partial_plan = {'version': 1, 'input': str(source), 'options': {'quality': '720p'},
                        'clips': [{'id': 'short', 'ranges': [{'start_ms': 500, 'end_ms': 1500}]},
                                  {'id': 'long', 'ranges': [{'start_ms': 0, 'end_ms': 4000}]}]}
        partial_path = root / 'partial-plan.json'
        partial_path.write_bytes(m.canonical(partial_plan))
        reference = root / 'reference'
        reference.mkdir()
        out, err, _ = self.app('collection', 'export', '--plan', partial_path, '--output', reference,
                               '--clips', 'short', '--threads', '1', '--timeout', '30s', env=env)
        reference_result = json.loads(out)
        require(not err and [output['id'] for output in reference_result['outputs']] == ['short'],
                'Collection budget reference did not finish its selected clip')
        budget = (reference / 'short.final.mp4').stat().st_size + 128
        partial_destination = root / 'partial'
        partial_destination.mkdir()
        out, err, code = self.app('--json-errors', 'collection', 'export', '--plan', partial_path,
                                  '--output', partial_destination, '--max-output-bytes', str(budget),
                                  '--threads', '1', '--timeout', '30s', env=env, expected=None)
        partial, failure = json.loads(out), json.loads(err)
        require(code == 1 and partial.get('incomplete') is True
                and [output['id'] for output in partial['outputs']] == ['short']
                and failure.get('error', {}).get('code') == 'output_limit'
                and failure['error'].get('clip_id') == 'long'
                and sorted(path.name for path in partial_destination.iterdir()) == ['short.final.mp4']
                and (partial_destination / 'short.final.mp4').stat().st_size <= budget,
                'Collection byte-limit failure lost its completed output or published an unfinished clip')
        files['partial_short'] = verify_output(partial['outputs'][0], partial_destination / 'short.final.mp4',
                                               480, 720, [(500, 1500)], [(.2, 0)])
        require(file_hash(source) == source_sha, 'Collection exports altered the source')
        return {'fixture_sha256': source_sha, 'validation': validation, 'draft': draft, 'selected_final': final,
                'collision': refusal, 'partial': partial, 'partial_error': failure, 'output_budget': budget, 'files': files}

    def listener(self, address):
        host, port = address.rsplit(':', 1)
        require(host == '127.0.0.1', 'Process helper must bind loopback only')
        try:
            with socket.create_connection((host, int(port)), timeout=.2) as connection:
                data = bytearray()
                while block := connection.recv(64):
                    data.extend(block)
                    if len(data) > 64:
                        return False
                return data == b'cutmy-distribution-helper-v1'
        except OSError:
            return False

    def process(self):
        helper = Path(self.args.process_helper)
        require(helper.is_absolute() and helper.is_file() and not helper.is_symlink(), 'An explicit native process helper is required')
        require(helper != self.binary and m.HASH.fullmatch(self.args.expected_process_helper_sha256)
                and file_hash(helper) == self.args.expected_process_helper_sha256, 'Process helper digest mismatch')
        source_path = 'scripts/testdata/release-process-helper.go'
        source_file = Path(__file__).resolve().parents[1] / source_path
        source_identity = self.resolved_source['files'].get(source_path)
        require(source_identity and file_hash(source_file) == source_identity['sha256'],
                'Native helper source differs from the independently resolved source')
        self.report['process_helper'] = {'binary_path': str(helper), 'binary_sha256': file_hash(helper),
                                         'source_path': source_path, 'source_sha256': source_identity['sha256'],
                                         'application_binary': False}
        results = {}
        cases = [('timeout', None, 124, 'timeout')]
        if self.target[0] != 'windows':
            cases += [('sigint', signal.SIGINT, 130, 'cancelled'), ('sigterm', signal.SIGTERM, 130, 'cancelled')]
        for label, requested_signal, wanted_exit, wanted_code in cases:
            case_input, marker = process_case_input(self.input, self.work, label)
            env = self.app_env(FFPROBE_PATH=str(helper))
            checked_binary(self.binary, self.expected_binary_sha)
            stdout = self.evidence / (label + '-process.stdout')
            stderr = self.evidence / (label + '-process.stderr')
            tree = None
            process = None
            observed_failure = False
            try:
                with stdout.open('xb') as out, stderr.open('xb') as err:
                    process = subprocess.Popen([str(self.binary), '--json-errors', 'inspect', '--input', str(case_input),
                                                '--timeout', '6s'], env=env, stdout=out, stderr=err)
                    until = time.monotonic() + 4
                    while time.monotonic() < until and not marker.exists() and process.poll() is None:
                        time.sleep(.02)
                    require(marker.exists(), 'Archived CLI did not spawn an observable complete process tree')
                    tree = json.loads(marker.read_bytes())
                    require(set(tree) == {'addresses', 'pids'} and len(tree['addresses']) == len(tree['pids']) == 3
                            and len(set(tree['pids'])) == 3 and all(type(pid) is int and pid > 0 for pid in tree['pids'])
                            and all(self.listener(address) for address in tree['addresses']), 'Process descendants were not alive before cancellation')
                    if requested_signal is not None:
                        process.send_signal(requested_signal)
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        raise RuntimeError('Archived CLI did not finish its own cancellation') from None
                error_envelope(stdout.read_bytes(), stderr.read_bytes(), process.returncode, wanted_exit, wanted_code)
                until = time.monotonic() + 2
                while time.monotonic() < until and any(self.listener(a) for a in tree['addresses']):
                    time.sleep(.02)
                require(not any(self.listener(a) for a in tree['addresses']), 'A descendant survived archived CLI cancellation')
                results[label] = {'exit': process.returncode, 'observed_descendants': 3, 'all_listeners_closed': True,
                                  'driver_cleanup_before_assertion': False, 'marker': tree,
                                  'input': {'path': str(case_input), 'sha256': file_hash(case_input)},
                                  'command': [str(self.binary), '--json-errors', 'inspect', '--input', str(case_input), '--timeout', '6s'],
                                  'stdout': stdout.name, 'stderr': stderr.name}
            except Exception:
                observed_failure = True
                raise
            finally:
                # Retain complete and partial identities before any rescue or
                # private-directory cleanup, including failed tree startup.
                capture_failure = None
                try:
                    retained = preserve_process_markers(marker, self.evidence, label)
                    (self.evidence / (label + '-process-markers.json')).write_bytes(m.canonical(retained))
                except Exception as error:
                    capture_failure = error
                    self.report.setdefault('errors', []).append('process marker evidence: ' + str(error))
                    observed_failure = True
                if process and process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
                # Rescue only on failure, after recording the assertion. Never
                # let driver cleanup create a successful termination result.
                if observed_failure and not tree:
                    # A helper can fail after opening one descendant but before
                    # publishing the complete root marker. Rescue only owned
                    # partial markers, without promoting this run to success.
                    partial = []
                    for suffix in ('.1', '.2', '.pid.0', '.pid.1', '.pid.2'):
                        try:
                            partial.extend(json.loads(Path(str(marker) + suffix).read_bytes())['pids'])
                        except (OSError, ValueError, KeyError):
                            pass
                    tree = {'pids': list(set(partial))}
                if observed_failure and tree:
                    for pid in reversed(tree['pids']):
                        try:
                            if self.target[0] == 'windows':
                                subprocess.run(['taskkill', '/PID', str(pid), '/T', '/F'], stdout=subprocess.DEVNULL,
                                               stderr=subprocess.DEVNULL, timeout=5)
                            else:
                                os.kill(pid, signal.SIGKILL)
                        except (OSError, subprocess.TimeoutExpired):
                            pass
                if capture_failure is not None:
                    raise capture_failure
        return list(PROCESS_CHECKS) + ([] if self.target[0] == 'windows' else ['sigint', 'sigterm']), results

    def start_service(self, command, env):
        checked_binary(self.binary, self.expected_binary_sha)
        log = self.evidence / (command + '.log')
        with log.open('xb') as file:
            process = subprocess.Popen([str(self.binary), command], env=env, stdout=file, stderr=file)
        self.processes.append((process, command))
        self.service_records.append({'command': command, 'pid': process.pid, 'log': log.name,
                                     'binary_sha256': self.expected_binary_sha})
        return process

    def stop_services(self):
        failures = []
        for process, command in reversed(self.processes):
            record = next(item for item in self.service_records if item['pid'] == process.pid)
            try:
                require(process.poll() is None, f'Archived {command} exited unexpectedly before intended shutdown')
                record['alive_before_intended_shutdown'] = True
                if self.target[0] == 'windows':
                    # Console SIGTERM is not available through Python on this
                    # runner. Stop between scenarios; timeout tree proof above
                    # independently checks runtime cancellation on Windows.
                    process.terminate()
                else:
                    process.send_signal(signal.SIGTERM)
                try:
                    process.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
                    raise RuntimeError('Archived service exceeded the shutdown watchdog') from None
                if self.target[0] != 'windows':
                    require(process.returncode == 0, 'Archived service failed graceful shutdown')
                record['exit'] = process.returncode
                record['shutdown'] = 'driver_terminate' if self.target[0] == 'windows' else 'sigterm_graceful'
            except Exception as error:
                failures.append(str(error))
                record['failure'] = str(error)
                if process.poll() is None:
                    try:
                        process.kill()
                        process.wait(timeout=5)
                    except Exception as cleanup_error:
                        failures.append(f'{command} rescue: {cleanup_error}')
        self.processes = []
        require(not failures, '; '.join(failures))

    def service(self):
        self.pg = PostgreSQL(self.args.database_url, self.commands, self.psql)
        self.pg.create()
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0))
            port = listener.getsockname()[1]
        media = self.work / 'service-media'
        media.mkdir()
        env = self.app_env(DATABASE_URL=self.pg.dsn(), DATA_DIR=str(media), LISTEN_ADDR=f'127.0.0.1:{port}',
                           PUBLIC_ORIGIN=f'http://127.0.0.1:{port}', COOKIE_SECURE='false', TRUST_PROXY='false',
                           FFMPEG_PATH=str(self.ffmpeg), FFPROBE_PATH=str(self.ffprobe),
                           WORKER_HEALTH_PATH=str(self.work / 'worker-health.json'), MAX_SOURCE_BYTES=str(8 << 20),
                           MAX_FETCH_BYTES=str(8 << 20), MAX_OUTPUT_BYTES=str(16 << 20), MAX_STORAGE_BYTES=str(1 << 30),
                           MAX_OWNER_BYTES=str(512 << 20), STORAGE_SAFETY_BYTES='0', SOURCE_TTL='24h', ARTIFACT_TTL='24h',
                           STORAGE_WAIT_TIMEOUT='2m', JOB_TIMEOUT='30s', SOURCE_TIMEOUT='30s', UPLOAD_TIMEOUT='30s',
                           FFMPEG_THREADS='1', WORKER_CONCURRENCY='1', MUTATIONS_PER_MINUTE='200', GOMAXPROCS='2')
        self.service_env = env
        server = self.start_service('server', env)
        client = Client(port, self.evidence, self.http_records)
        until = time.monotonic() + 20
        while time.monotonic() < until:
            require(server.poll() is None, 'Archived server exited before readiness')
            try:
                client.request('GET', '/readyz')
                break
            except (OSError, RuntimeError):
                time.sleep(.05)
        else:
            raise RuntimeError('Archived server did not become ready')
        self.app('healthcheck', env=env, timeout=10)
        data, _ = client.request('GET', '/api/v1/session')
        require(data.get('ok') is True and client.cookie, 'Archived server did not establish an owner session')
        boundary = 'cutmy-distribution-' + uuid.uuid4().hex
        upload = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="synthetic.mp4"\r\n'
                  'Content-Type: video/mp4\r\n\r\n').encode() + self.input.read_bytes() + f'\r\n--{boundary}--\r\n'.encode()
        source, _ = client.request('POST', '/api/v1/uploads', raw=upload,
                                   headers={'Content-Type': 'multipart/form-data; boundary=' + boundary}, expected=201)
        require(source.get('id') and source.get('preview_url') and source.get('width') == 160
                and source.get('height') == 96, 'Upload lost its selected coded source metadata')
        original, _ = client.request('GET', source['preview_url'])
        require(original == self.input.read_bytes(), 'Acknowledged uploaded bytes changed')
        preview_url = '/api/v1/sources/' + source['id'] + '/preview?start_ms=1000'
        preview, headers = client.request('GET', preview_url)
        require(headers.get('X-Preview-Cache') == 'miss', 'Preview did not execute its cache miss')
        cached, headers = client.request('GET', preview_url)
        require(headers.get('X-Preview-Cache') == 'hit' and cached == preview, 'Preview cache did not serve the same bytes')
        require(headers.get('X-Source-Display-Width') == '96' and headers.get('X-Source-Display-Height') == '160',
                'Warm preview lost trusted original display geometry')
        waveform_url = '/api/v1/sources/' + source['id'] + '/waveform?start_ms=0&audio_track=1'
        waveform, wave_headers = client.request('GET', waveform_url)
        require(wave_headers.get('X-Preview-Cache') == 'miss' and waveform.get('has_audio') is True
                and waveform.get('audio_track') == 1 and waveform.get('start_ms') == 0
                and waveform.get('end_ms') == source['duration_ms'] and waveform.get('step_ms') == 100
                and len(waveform.get('peaks', [])) == math.ceil(source['duration_ms']/100)
                and all(isinstance(p,(int,float)) and math.isfinite(p) and 0 <= p <= 1 for p in waveform['peaks'])
                and max(waveform['peaks']) > .05, 'Selected audio waveform lost bounded source-time activity')
        warm_wave, wave_headers = client.request('GET', waveform_url)
        require(wave_headers.get('X-Preview-Cache') == 'hit' and warm_wave == waveform, 'Waveform cache changed selected audio')
        high_audio = self.work / 'waveform-8khz.wav'
        self.ff('-f','lavfi','-i','sine=frequency=8000:sample_rate=48000:duration=0.5','-c:a','pcm_s16le',high_audio)
        high_boundary = 'cutmy-frequency-' + uuid.uuid4().hex
        high_upload = (f'--{high_boundary}\r\nContent-Disposition: form-data; name="file"; filename="8khz.wav"\r\n'
            'Content-Type: audio/wav\r\n\r\n').encode()+high_audio.read_bytes()+f'\r\n--{high_boundary}--\r\n'.encode()
        high_source,_=client.request('POST','/api/v1/uploads',raw=high_upload,
            headers={'Content-Type':'multipart/form-data; boundary='+high_boundary},expected=201)
        high_wave,_=client.request('GET','/api/v1/sources/'+high_source['id']+'/waveform?start_ms=0')
        require(high_wave.get('has_audio') is True and len(high_wave.get('peaks',[]))==5
            and all(abs(peak-math.sin(math.pi/3)/8)<.001 for peak in high_wave['peaks']),
            'Audible8kHz audio was drawn as silence')
        client.request('DELETE','/api/v1/sources/'+high_source['id'],expected=204)
        preview_path = self.work / 'preview.mp4'
        preview_path.write_bytes(preview)
        self.decode(preview_path)
        self.frame(preview_path, 96, 160, (1, 3, 0, 2))
        self.tone(preview_path)
        job, _ = client.request('POST', '/api/v1/jobs', data={'source_id': source['id'],
                                'ranges': [{'start_ms': 1000, 'end_ms': 3000, 'label': 'Archive smoke'}],
                                'format': 'mp4', 'quality': '720p', 'cut_mode': 'accurate'}, expected=202)
        require(job.get('status') == 'queued' and len(job.get('items', [])) == 1, 'Archived server did not queue an export')
        worker = self.start_service('worker', env)
        until = time.monotonic() + 40
        while time.monotonic() < until:
            require(worker.poll() is None, 'Archived worker exited before export completion')
            job, _ = client.request('GET', '/api/v1/jobs/' + job['id'])
            if job['status'] == 'succeeded':
                break
            require(job['status'] in ('queued', 'running'), 'Archived worker returned an unsuccessful export')
            time.sleep(.05)
        else:
            raise RuntimeError('Archived worker did not complete the real export')
        self.app('worker-healthcheck', env=env, timeout=10)
        artifact = job['items'][0].get('artifact')
        require(job['items'][0]['status'] == 'succeeded' and artifact and artifact['size_bytes'] > 32,
                'Successful archived job has no complete artifact')
        output, headers = client.request('GET', artifact['download_url'])
        require(len(output) == artifact['size_bytes'] and headers.get('Content-Disposition'), 'Artifact download size/header mismatch')
        partial, _ = client.request('GET', artifact['download_url'], headers={'Range': 'bytes=0-31'}, expected=206)
        require(partial == output[:32], 'Archived download did not preserve byte ranges')
        foreign = Client(port, self.evidence, self.http_records)
        foreign.cookie = 'cutmy_session=' + base64.urlsafe_b64encode(bytes([1]) * 32).decode().rstrip('=')
        inaccessible = ['/api/v1/jobs/' + job['id'], artifact['download_url'], source['preview_url'], preview_url, waveform_url]
        for path in inaccessible:
            foreign.request('GET', path, expected=404)
        path = self.work / 'download.mp4'
        path.write_bytes(output)
        self.decode(path)
        self.frame(path, 96, 160, (1, 3, 0, 2))
        self.tone(path)
        cleanup_job, _ = client.request('POST', '/api/v1/jobs', data={'source_id':source['id'],
            'ranges':[{'start_ms':0,'end_ms':1000,'label':'Cleanup crop'}], 'format':'mp4', 'quality':'best',
            'cut_mode':'accurate', 'crop':{'x':0,'y':0,'width':.5,'height':.5}, 'cleanup_source':True}, expected=202)
        until=time.monotonic()+40
        while time.monotonic()<until:
            cleanup_job,_=client.request('GET','/api/v1/jobs/'+cleanup_job['id'])
            if cleanup_job['status']=='succeeded':break
            require(cleanup_job['status'] in ('queued','running'), 'Cleanup export failed')
            time.sleep(.05)
        else:raise RuntimeError('Cleanup export did not finish')
        cleanup_artifact=cleanup_job['items'][0]['artifact']
        crop_bytes,_=client.request('GET',cleanup_artifact['download_url'])
        crop_path=self.work/'api-crop.mp4';crop_path.write_bytes(crop_bytes)
        self.decode(crop_path);self.frame(crop_path,48,80,(1,1,1,1));self.tone(crop_path)
        until=time.monotonic()+20
        while time.monotonic()<until:
            retained,_=client.request('GET','/api/v1/sources/'+source['id'])
            if retained.get('media_removed') is True:break
            time.sleep(.05)
        else:raise RuntimeError('Successful export did not clean original media')
        require(retained['id']==source['id'] and retained['duration_ms']==source['duration_ms'], 'Cleanup lost source project metadata')
        for route in (source['preview_url'],preview_url,waveform_url):client.request('GET',route,expected=410)
        kept_output,_=client.request('GET',artifact['download_url'])
        require(kept_output==output,'Source cleanup removed or altered an earlier completed clip')
        kept_job,_=client.request('GET','/api/v1/jobs/'+job['id'])
        require(kept_job['status']=='succeeded','Source cleanup removed completed job')
        inaccessible.extend(['/api/v1/jobs/'+cleanup_job['id'],cleanup_artifact['download_url']])
        for route in inaccessible:foreign.request('GET',route,expected=404)
        self.client, self.source, self.inaccessible = client, source, inaccessible
        self.media_root = media
        return ['real_server_and_worker', 'session_upload_exact_bytes', 'preview_miss_and_hit', 'queued_job_completion',
                'download_decode_geometry_audio', 'ownership_404', 'range_206', 'same_binary_healthchecks',
                'original_display_preview_metadata', 'waveform_selected_audio_private_cache', 'waveform_high_frequency_activity',
                'cropped_api_download', 'cleanup_preserves_source_metadata_jobs_and_downloads'], {
                    'source_geometry': [source['width'], source['height']], 'preview_sha256': file_hash(preview_path),
                    'output_sha256': file_hash(path), 'output_bytes': len(output), 'processes': self.service_records}

    def deletion(self):
        # Recreate the cache after startup housekeeping, then observe all exact
        # retained paths before the acknowledgement of explicit source deletion.
        self.client.request('GET', '/api/v1/sources/' + self.source['id'] + '/preview?start_ms=1000', expected=410)
        files = json.loads(self.pg.sql("SELECT COALESCE(json_agg(path),'[]') FROM storage_files"))
        require(len(files) >= 2, 'Service did not account for real source and artifact files')
        for path in files:
            require(Path(path).resolve().is_relative_to(self.media_root) and Path(path).is_file(),
                    'Accounted file is outside owned media or missing before deletion')
        self.client.request('DELETE', '/api/v1/sources/' + self.source['id'], expected=204)
        for path in self.inaccessible:
            self.client.request('GET', path, expected=404)
        require(all(not Path(path).exists() and not Path(path).is_symlink() for path in files),
                'Acknowledged deletion retained an accounted physical file')
        counters = json.loads(self.pg.sql("SELECT json_build_object('stored',stored_bytes,'reserved',reserved_bytes,"
                                          "'files',(SELECT count(*) FROM storage_files),'reservations',"
                                          "(SELECT count(*) FROM storage_reservations)) FROM storage_counters WHERE id=1"))
        require(set(counters) == {'stored', 'reserved', 'files', 'reservations'} and all(v == 0 for v in counters.values()),
                'Acknowledged source deletion retained storage bytes or ledger entries')
        self.stop_services()
        self.processes = []
        remaining = [str(path.relative_to(self.media_root)) for path in self.media_root.rglob('*') if path.is_file()]
        require(not remaining, 'Deletion retained a preview/work media file outside storage_files')
        return ['acknowledged_source_delete', 'all_routes_404', 'physical_files_absent', 'zero_stored_and_reserved',
                'zero_ledger_entries', 'no_preview_or_work_media_remaining'], {'accounted_files_removed': len(files), 'counters': counters,
                                                                            'service_shutdown': self.service_records}

    def preserve_media(self):
        destination = self.evidence / 'synthetic-media'
        destination.mkdir(exist_ok=True)
        for name in ('corners.ppm', 'input.mp4', 'unrotated.mp4', 'accurate.mp4', 'copy.mp4', 'mp3.mp3', 'preview.mp4', 'download.mp4'):
            path = self.work / name
            if path.is_file():
                shutil.copyfile(path, destination / name)

    def run(self):
        try:
            for name in REQUIRED_GROUPS:
                self.stage(name, getattr(self, name))
            self.report['binary']['sha256_after'] = file_hash(self.binary)
            self.report['ok'] = True
            validate_report(self.report)
        except Exception as error:
            self.report['ok'] = False
            if not self.report['errors']:
                self.report['errors'].append('verdict: ' + str(error))
            raise
        finally:
            if self.binary:
                self.report['binary']['sha256_after'] = file_hash(self.binary) if self.binary.exists() else None
            for cleanup in (self.preserve_media, self.stop_services, self.pg.close if self.pg else lambda: None,
                            lambda: shutil.rmtree(self.work)):
                try:
                    cleanup()
                except Exception as error:
                    self.report['ok'] = False
                    self.report['errors'].append('cleanup: ' + str(error))
            self.save()
        require(self.report['ok'], 'Native distribution cleanup failed')


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--expected-source-sha256', required=True)
    parser.add_argument('--expected-manifest-sha256', required=True)
    parser.add_argument('--target', required=True, choices=[os_ + '/' + arch for os_, arch in m.TARGETS])
    parser.add_argument('--artifact-receipt', type=Path, required=True)
    parser.add_argument('--database-url', default=os.environ.get('TEST_DATABASE_URL'))
    parser.add_argument('--evidence-dir', type=Path, required=True)
    parser.add_argument('--process-helper', required=True)
    parser.add_argument('--expected-process-helper-sha256', required=True)
    args = parser.parse_args()
    parser.error('Set --database-url or TEST_DATABASE_URL to a disposable loopback PostgreSQL') if not args.database_url else None
    return args


if __name__ == '__main__':
    try:
        Native(parse_args()).run()
    except Exception as error:
        print(f'Native archive acceptance failed: {type(error).__name__}: {error}', file=sys.stderr)
        sys.exit(1)
