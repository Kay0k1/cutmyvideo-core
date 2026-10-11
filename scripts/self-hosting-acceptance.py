#!/usr/bin/env python3
"""Destructive acceptance ONLY on an explicitly disposable Linux CI host.

Uses the immutable public lifecycle/Compose/firewall profile without replacing
its restrictions. Evidence excludes private state, credentials and backup data.
There is deliberately no production mode, skip, fallback or daemon restart.
"""
from __future__ import annotations

import argparse
import ast
import contextlib
import hashlib
import http.client
from http.cookies import SimpleCookie
import importlib.util
import io
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import secrets
import shutil
import signal
import shlex
import socket
import ssl
import stat
import struct
import subprocess
import sys
import tempfile
import tarfile
import threading
import time
from urllib.parse import urlsplit

sys.dont_write_bytecode = True
REPO = Path(__file__).resolve().parents[1]
UPGRADE_FROM = 'e59413c017d501d8c2ba3829fe4dd76267d127b2'
UPGRADE_TREE = 'f68f158099b0273e8c02046d4a8de15b7e30f426'
GROUPS = {
    'identity': ('immutable_old_image', 'dirty_and_wrong_revision_refused', 'immutable_new_image',
                 'application_sources_unchanged', 'changed_state_and_remote_engine_refused'),
    'https_media': ('verified_tls_and_secure_origin', 'two_owners_and_exact_upload', 'preview_miss_hit_decode',
                    'mp4_mp3_copy_decode_ownership_range', 'deletion_physical_and_accounting'),
    'roles': ('runtime_tcp_identity', 'runtime_privilege_denials', 'unchanged_schema_and_history',
              'reduced_grant_counter_and_maintenance'),
    'container_constraints': ('actual_apps_and_postgres_constraints', 'operator_constraints_and_secret_separation',
                              'private_tmp_and_readonly_roots', 'graceful_stopped_services', 'owned_helper_partial_create_cleanup'),
    'egress': ('controlled_listener_witnesses', 'both_bridges_and_operator_denials', 'exact_pg_and_public_https',
               'unrelated_traffic_preserved', 'missing_modified_reordered_start_refusal', 'two_project_remove_either'),
    'backup_restore': ('coordinated_backup_old_image', 'actual_same_project_image_upgrade',
                       'retained_ids_sessions_hashes_idempotency', 'backup_refusals_without_mutation',
                       'actual_dump_media_fresh_restore', 'restored_bytes_counters_and_new_exports'),
    'lifecycle': ('old_new_state_shared_lock', 'stopped_restart_requires_policy',
                  'checked_restart_retains_https', 'sealed_snapshots_unchanged'),
}
SHA = re.compile(r'[0-9a-f]{40}\Z')
HASH = re.compile(r'[0-9a-f]{64}\Z')
TCP_HEALTHCHECK = ['CMD', 'pg_isready', '-h', '127.0.0.1', '-p', '5432', '-U', 'cutmy_admin', '-d', 'cutmy']
SOCKET_HEALTHCHECK_A = ['CMD-SHELL', 'pg_isready -U cutmy_admin -d cutmy']


def require(value, message):
    if not value:
        raise RuntimeError(message)


def canonical(value):
    return (json.dumps(value, indent=2, sort_keys=True) + '\n').encode()


def network_subnets(networks):
    """Keep declared ranges; Docker host/none IPAM has a null Config."""
    require(isinstance(networks, list), 'network inventory is not a list')
    ranges = []
    for network in networks:
        require(isinstance(network, dict), 'network inventory entry is not an object')
        ipam = network.get('IPAM', {})
        require(isinstance(ipam, dict), 'network IPAM is not an object')
        config = ipam.get('Config')
        if config is None:
            continue
        require(isinstance(config, list), 'network IPAM Config is not a list')
        for entry in config:
            require(isinstance(entry, dict) and isinstance(entry.get('Subnet'), str)
                    and entry['Subnet'], 'network IPAM subnet entry is invalid')
            ranges.append(ipaddress.ip_network(entry['Subnet']))
    return ranges


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_hash(path):
    result = hashlib.sha256()
    with Path(path).open('rb') as stream:
        while chunk := stream.read(1024 * 1024):
            result.update(chunk)
    return result.hexdigest()


def validate_report(report):
    """Fail closed even if all visible partial check records say PASS."""
    require(report.get('schema_version') == 1 and report.get('ok') is True
            and report.get('errors') == [], 'acceptance did not finish successfully')
    require(set(report.get('groups', {})) == set(GROUPS), 'required group missing or unknown')
    for group, checks in GROUPS.items():
        value = report['groups'][group]
        require(value.get('status') == 'pass' and set(value.get('checks', {})) == set(checks)
                and all(item.get('ok') is True for item in value['checks'].values()), 'required check incomplete')
    cleanup = report.get('cleanup', {})
    require(cleanup.get('ok') is True and cleanup.get('errors') == []
            and cleanup.get('remaining_resources') == [] and cleanup.get('live_owned_processes') == [],
            'cleanup not independently completed')
    images = report.get('images', {})
    provider = report.get('provider', {})
    require(provider.get('GITHUB_REPOSITORY') == 'Kay0k1/cutmyvideo-core'
            and provider.get('GITHUB_EVENT_NAME') in ('pull_request', 'push', 'workflow_dispatch')
            and all(re.fullmatch(r'[1-9][0-9]*', str(provider.get(k, ''))) for k in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT')),
            'hosted provider identity missing')
    binding = report.get('checkout', {})
    require(binding.get('commit') == report.get('expected_checkout') == provider.get('GITHUB_SHA')
            and SHA.fullmatch(binding.get('commit', '')) and SHA.fullmatch(binding.get('tree', ''))
            and SHA.fullmatch(binding.get('head_commit', '')) and SHA.fullmatch(binding.get('head_tree', ''))
            and binding.get('approved_A_is_ancestor') is True
            and binding.get('clean') is True
            and isinstance(binding.get('parents'), list) and binding['parents']
            and all(SHA.fullmatch(p) for p in binding['parents'])
            and isinstance(binding.get('head_parents'), list) and binding['head_parents']
            and all(SHA.fullmatch(p) for p in binding['head_parents']), 'immutable checkout/head binding incomplete')
    if provider['GITHUB_EVENT_NAME'] == 'pull_request':
        require(len(binding['parents']) == 2 and binding['head_commit'] in binding['parents']
                and binding['head_tree'] == binding['tree'], 'actual PR merge/head tree binding mismatch')
    else:
        require(binding['head_commit'] == binding['commit'] and binding['head_tree'] == binding['tree'],
                'non-PR checkout identity mismatch')
    require(set(images) == {'old', 'current', 'restored'} and images['old']['source_commit'] == UPGRADE_FROM
            and SHA.fullmatch(images['current']['source_commit'])
            and images['current']['source_commit'] == report.get('expected_checkout') == provider.get('GITHUB_SHA')
            and images['current']['source_commit'] != UPGRADE_FROM
            and images['old']['tree'] == UPGRADE_TREE
            and images['current']['tree'] == binding['tree']
            and images['old']['image_id'] != images['current']['image_id']
            and images['restored']['image_id'] == images['current']['image_id']
            and images['restored']['source_sha256'] == images['current']['source_sha256'],
            'actual old/new/restored image identity mismatch')
    for label, image in images.items():
        require(SHA.fullmatch(image.get('source_commit', '')) and SHA.fullmatch(image.get('tree', ''))
                and HASH.fullmatch(image.get('source_sha256', ''))
                and re.fullmatch(r'sha256:[0-9a-f]{64}', image.get('image_id', ''))
                and image.get('toolchain') == 'go1.27.2'
                and image.get('version') == 'v0.0.0-candidate.g' + image['source_commit']
                and re.fullmatch(r'\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z', image.get('build_time', ''))
                and image.get('runtime_version') == {'version': image.get('version'), 'commit': image['source_commit'],
                      'build_time': image.get('build_time'), 'go_version': image['toolchain'], 'os': 'linux', 'arch': 'amd64'}
                and set(image.get('tools', {})) == {'ffmpeg', 'ffprobe', 'yt_dlp', 'node'}
                and all(isinstance(x, str) and x for x in image['tools'].values()), 'image/source/version/tool identity incomplete')
    require(images['restored']['runtime_version'] == images['current']['runtime_version']
            and images['restored']['tree'] == images['current']['tree'], 'restored runtime/tree identity changed')
    require(report.get('runtime_application_sources_unchanged') is True,
            'A/B application-source scope missing')
    for label, baseline in (('baseline', True), ('current', False), ('restored', False)):
        ready = report.get(label + '_tcp_readiness', {})
        require(re.fullmatch(r'[0-9a-f]{64}', ready.get('container_id', ''))
                and ready.get('source_commit') == images['old' if baseline else 'current']['source_commit']
                and ready.get('tcp_sql_verified') is True
                and ready.get('healthcheck') == (SOCKET_HEALTHCHECK_A if baseline else TCP_HEALTHCHECK)
                and ready.get('baseline_socket_healthcheck_workaround') is baseline
                and ready.get('attempts') and ready['attempts'][-1].get('tcp_sql_ready') is True,
                'actual PostgreSQL TCP readiness/workaround evidence missing')


def owned_resource(value, *, project=None, token=None, component=None):
    labels = value.get('Labels') or value.get('Config', {}).get('Labels') or {}
    if project:
        require(labels.get('com.docker.compose.project') == project, 'resource is not owned project')
        if component:
            require(labels.get('video.cutmy.profile') == 'public-v1'
                    and labels.get('video.cutmy.project') == project
                    and labels.get('com.docker.compose.volume') == component
                    and value.get('Driver') == 'local' and not value.get('Options'), 'foreign volume refused')
    else:
        require(token and labels.get('video.cutmy.acceptance') == token, 'resource is not owned witness')


def redact(data, values):
    if isinstance(data, bytes):
        data = data.decode('utf-8', 'replace')
    for value in sorted((str(v) for v in values if v), key=len, reverse=True):
        data = data.replace(value, '[REDACTED]')
    data = re.sub(r'(?im)^(?:set-cookie|cookie):.*$', '[REDACTED AUTH HEADER]', data)
    data = re.sub(r'-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----',
                  '[REDACTED PRIVATE KEY]', data, flags=re.S)
    return data


def cleanup_all(actions):
    errors = []
    for name, callback in actions:
        try:
            callback()
        except Exception as error:
            errors.append(name + ': ' + type(error).__name__ + ': ' + str(error))
    return errors


def filter_rules(data):
    """Compare actual rule semantics/order, excluding timestamps/counters."""
    result = {}
    for line in data.decode().splitlines():
        if line.startswith(':'):
            words = line[1:].split(); result[words[0]] = {'policy': words[1], 'rules': []}
        elif line.startswith('-A '):
            words = shlex.split(line); result[words[1]]['rules'].append(words[2:])
    return result


class TLSConnection(http.client.HTTPSConnection):
    def connect(self):
        sock = socket.create_connection(('127.0.0.1', self.port), self.timeout)
        try:
            self.sock = self._context.wrap_socket(sock, server_hostname=self.host)
        except BaseException:
            sock.close()
            raise


class Client:
    def __init__(self, domain, ca, record, secret):
        self.domain, self.cookie = domain, ''
        self.context = ssl.create_default_context(cafile=str(ca))
        require(self.context.verify_mode == ssl.CERT_REQUIRED and self.context.check_hostname,
                'TLS verification must remain enabled')
        self.record, self.secret = record, secret

    def request(self, method, path, *, data=None, raw=None, headers=None, expected=200):
        fields = {'Origin': 'https://' + self.domain, **(headers or {})}
        if self.cookie:
            fields['Cookie'] = self.cookie
        if data is not None:
            raw, fields['Content-Type'] = canonical(data), 'application/json'
        connection = TLSConnection(self.domain, 443, timeout=20, context=self.context)
        try:
            connection.request(method, path, raw, fields)
            response = connection.getresponse()
            body = response.read(20 * 1024 * 1024 + 1)
            require(len(body) <= 20 * 1024 * 1024, 'HTTP response bound exceeded')
            result_headers = dict(response.getheaders())
            if response.getheader('Set-Cookie'):
                cookies = SimpleCookie(response.getheader('Set-Cookie'))
                entry = cookies['cutmy_session']
                require(entry['secure'] and entry['httponly'] and entry['samesite'].lower() == 'lax',
                        'session cookie HTTPS restrictions missing')
                self.cookie = 'cutmy_session=' + entry.value
                self.secret(entry.value); self.secret(self.cookie)
            self.record({'method': method, 'path': path, 'status': response.status,
                         'bytes': len(body), 'body_sha256': digest(body),
                         'tls_verified_domain': self.domain,
                         'headers': {k: v for k, v in result_headers.items()
                                     if k.lower() not in ('set-cookie', 'cookie')}})
            require(response.status == expected, 'unexpected HTTPS response status')
            if 'application/json' in result_headers.get('Content-Type', '') and body:
                return json.loads(body), result_headers
            return body, result_headers
        finally:
            connection.close()

    def session(self):
        value, _ = self.request('GET', '/api/v1/session')
        require(value['ok'] and self.cookie, 'session missing')

    def upload(self, fixture):
        boundary = 'acceptance-' + secrets.token_hex(16)
        data = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="synthetic.mp4"\r\n'
                'Content-Type: video/mp4\r\n\r\n').encode() + fixture.read_bytes() + f'\r\n--{boundary}--\r\n'.encode()
        return self.request('POST', '/api/v1/uploads', raw=data,
                            headers={'Content-Type': 'multipart/form-data; boundary=' + boundary}, expected=201)[0]

    def delete(self, source_id):
        return self.request('DELETE', '/api/v1/sources/' + source_id, expected=204)


class Witness:
    """A nonce-emitting owned listener; denial cannot pass because nobody listens."""
    def __init__(self, address):
        self.token = secrets.token_hex(20).encode()
        self.sock = socket.socket()
        self.sock.bind(address); self.sock.listen(16); self.sock.settimeout(.2)
        self.port, self.stopping = self.sock.getsockname()[1], False
        self.thread = threading.Thread(target=self.loop, daemon=True)
        self.thread.start()

    def loop(self):
        while not self.stopping:
            try:
                client, _ = self.sock.accept()
                with client:
                    client.sendall(self.token)
            except socket.timeout:
                pass
            except OSError:
                if not self.stopping:
                    self.stopping = True

    def verify(self, address):
        require(self.thread.is_alive() and not self.stopping, 'owned listener exited')
        with socket.create_connection((address, self.port), timeout=2) as client:
            require(client.recv(128) == self.token, 'controlled listener witness unavailable')

    def close(self):
        self.stopping = True; self.sock.close(); self.thread.join(timeout=2)
        require(not self.thread.is_alive(), 'owned listener survived cleanup')


class Acceptance:
    def __init__(self, args):
        require(args.allow_disposable_host and os.environ.get('GITHUB_ACTIONS') == 'true'
                and os.environ.get('RUNNER_ENVIRONMENT') == 'github-hosted',
                'only an explicitly disposable GitHub-hosted runner is permitted')
        require(os.geteuid() == 0 and os.uname().sysname == 'Linux' and os.uname().machine == 'x86_64',
                'root Linux amd64 runner required')
        require(args.upgrade_from == UPGRADE_FROM and SHA.fullmatch(args.revision), 'approved A/current SHA required')
        self.args = args
        self.output = args.output.absolute()
        require(not self.output.exists() and not self.output.is_relative_to(REPO), 'new outside-Git output required')
        self.output.mkdir(mode=0o700, parents=False)
        self.work = Path(tempfile.mkdtemp(prefix='cutmy-self-hosting-', dir=args.temp_directory))
        self.secrets, self.commands, self.http, self.projects = [], [], [], []
        self.processes, self.witnesses, self.extra_resources = [], [], []
        self.network_address = None
        self.initial_resources = {'container': set(), 'network': set(), 'volume': set()}
        self.initial_filter = None
        self.unknown_new_containers, self.recovered_helpers = [], {}
        self.token = secrets.token_hex(5)
        self.report = {'schema_version': 1, 'ok': False, 'errors': [], 'groups': {}, 'images': {},
                       'scope': {'disposable_host': True, 'local_ca_https': True, 'acme': False,
                                 'public_dns': False, 'internet_inbound_https': False,
                                 'host_or_daemon_reboot': False, 'schema_changing_upgrade': False,
                                 'dump_state_and_private_keys_uploaded': False},
                       'provider': {k: os.environ.get(k) for k in ('GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT',
                                   'GITHUB_REPOSITORY', 'GITHUB_SHA', 'GITHUB_EVENT_NAME')},
                       'upgrade_from': args.upgrade_from, 'expected_checkout': args.revision}
        self.active_group = None
        self.started = time.monotonic()

    def secret(self, value):
        self.secrets.append(value)

    def save(self):
        (self.output / 'acceptance-report.json').write_bytes(canonical(self.report))
        (self.output / 'commands.json').write_bytes(canonical(self.commands))
        (self.output / 'https.json').write_bytes(canonical(self.http))

    def run(self, command, *, input=None, timeout=120, expected=0, env=None, terminate_grace=2):
        remaining = 2400 - (time.monotonic() - self.started)
        require(getattr(self, 'cleaning', False) or remaining > 0, 'overall acceptance deadline exceeded')
        if not getattr(self, 'cleaning', False):
            timeout = min(timeout, remaining)
        fields = {k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'LANG', 'LC_ALL', 'TMPDIR', 'DOCKER_CONFIG')}
        fields.update(env or {})
        record = {'command': [redact(str(x), self.secrets) for x in command], 'expected_exit': expected}
        self.commands.append(record)
        with tempfile.TemporaryFile(dir=self.work) as out, tempfile.TemporaryFile(dir=self.work) as err:
            try:
                process = subprocess.Popen([str(x) for x in command], stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,
                                           stdout=out, stderr=err, env=fields, start_new_session=True)
                process.communicate(input=input, timeout=timeout)
            except subprocess.TimeoutExpired:
                record['timeout'] = True
                with contextlib.suppress(ProcessLookupError):
                    os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=terminate_grace)
                except subprocess.TimeoutExpired:
                    with contextlib.suppress(ProcessLookupError):
                        os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=3)
                raise RuntimeError('bounded command timed out') from None
            record['exit'] = process.returncode
            require(out.tell() <= 32 * 1024 * 1024 and err.tell() <= 32 * 1024 * 1024, 'command output bound exceeded')
            out.seek(0); err.seek(0); stdout, stderr = out.read(), err.read()
        record['stdout_sha256'], record['stderr_sha256'] = digest(stdout), digest(stderr)
        # Never publish private config/state/dump contents or command input.
        record['stderr'] = redact(stderr, self.secrets)[-20000:]
        if expected is None:
            return process.returncode, stdout, stderr
        require(process.returncode == expected, 'bounded command returned unexpected exit')
        return stdout

    def json(self, command, **kwargs):
        return json.loads(self.run(command, **kwargs))

    def check(self, name, evidence=None):
        require(name in GROUPS[self.active_group], 'unknown required check')
        values = self.report['groups'][self.active_group]['checks']
        require(name not in values, 'duplicate required check')
        values[name] = {'ok': True, 'evidence': evidence or {}}
        self.save()

    def group(self, name, callback, *, complete=True):
        self.active_group = name
        group = self.report['groups'].setdefault(name, {'status': 'running', 'checks': {}})
        require(group['status'] == 'running', 'completed group cannot be reopened')
        self.save()
        try:
            callback()
            if complete:
                require(set(group['checks']) == set(GROUPS[name]), 'required group check incomplete')
                group['status'] = 'pass'
        except Exception:
            group['status'] = 'fail'
            raise
        finally:
            self.save(); self.active_group = None

    def git(self, repo, *args):
        return self.run(['git', '-c', 'safe.directory=' + str(repo), '-C', repo, *args]).decode().strip()

    def clone(self, name, revision):
        destination = self.work / name
        self.run(['git', '-c', 'safe.directory=' + str(REPO), 'clone', '--quiet', '--no-hardlinks', '--no-checkout', REPO, destination])
        self.git(destination, 'checkout', '--quiet', '--detach', revision)
        require(self.git(destination, 'rev-parse', 'HEAD') == revision
                and self.git(destination, 'status', '--porcelain', '--untracked-files=all') == '', 'owned clone identity/cleanliness mismatch')
        return destination

    def wrapper(self, project, action, *extra, expected=0):
        return self.run([sys.executable, '-B', project['repo'] / 'deploy/self-host.py',
                         '--config', project['config_file'], '--state', project['state'], action, *extra],
                        timeout=1900 if action in ('build', 'backup', 'restore') else 240, expected=expected,
                        terminate_grace=75)

    def policy(self, project, action, expected=0):
        return self.run([sys.executable, '-B', project['state'] / 'source/deploy/egress-policy.py',
                         '--config', project['config_file'], action], expected=expected)

    def compose(self, project):
        return ['docker', 'compose', '--project-name', project['config']['CUTMY_PROJECT'],
                '--env-file', project['state'] / 'compose.env', '-f', project['state'] / 'source/deploy/compose.public.yml',
                '-f', project['state'] / 'source/deploy/compose.https.example.yml']

    def container(self, project, role):
        ids = self.run(self.compose(project) + ['ps', '-a', '-q', role]).decode().split()
        require(len(ids) == 1 and re.fullmatch(r'[0-9a-f]{12,64}', ids[0]), 'one exact owned service required')
        value = self.json(['docker', 'inspect', ids[0]])[0]
        owned_resource(value, project=project['config']['CUTMY_PROJECT'])
        require(value['Config']['Labels']['com.docker.compose.service'] == role, 'wrong owned service role')
        return value

    def sql(self, project, query, *, runtime=False, expected=0, timeout=120):
        # Use PostgreSQL's own matching client over TCP. The password remains
        # in the private container environment, never a persisted argument.
        role, key = ('cutmy_runtime', 'CUTMY_APP_PASSWORD') if runtime else ('cutmy_admin', 'POSTGRES_PASSWORD')
        script = 'export PGCONNECT_TIMEOUT=2; export PGPASSWORD="$' + key + '"; exec psql -X -qAt -v ON_ERROR_STOP=1 -h 127.0.0.1 -U ' + role + ' -d cutmy'
        return self.run(self.compose(project) + ['exec', '-T', 'postgres', 'sh', '-c', script],
                        input=b'\\set VERBOSITY verbose\n' + query.encode() + b'\n', expected=expected, timeout=timeout)

    def snapshot(self, project):
        tables = ('sources', 'jobs', 'artifacts', 'storage_files', 'storage_reservations', 'storage_counters')
        return {name: json.loads(self.sql(project, "SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb) FROM " + name + ' t'))
                for name in tables}

    def wait(self, callback, timeout=60):
        until = time.monotonic() + timeout
        while time.monotonic() < until:
            if callback():
                return
            time.sleep(.2)
        raise RuntimeError('required condition did not become true within deadline')

    def make_project(self, repo, name, prefix_index, *, same=None):
        project = {'repo': repo, 'state': self.work / (name + '-state'), 'config_file': self.work / (name + '.env')}
        config = dict(same['config']) if same else {
            'CUTMY_PROJECT': 'cutmy_' + self.token + '_' + name,
            'CUTMY_DOMAIN': name + '-' + self.token + '.acceptance.test',
            'CUTMY_HTTP_PORT': str(self.free_port()),
            'CUTMY_DATABASE_PREFIX': '10.' + str(self.subnet_octet) + '.' + str(prefix_index),
            'CUTMY_PROCESSING_PREFIX': '10.' + str(self.subnet_octet) + '.' + str(prefix_index + 1),
            **{key: secrets.token_urlsafe(48) for key in ('CUTMY_ADMIN_PASSWORD', 'CUTMY_MIGRATION_PASSWORD', 'CUTMY_APP_PASSWORD')}}
        for key, value in config.items():
            if key.endswith('_PASSWORD'):
                self.secret(value)
        project['config'] = config
        project['config_file'].write_text(''.join(k + '=' + v + '\n' for k, v in sorted(config.items())))
        project['config_file'].chmod(0o600)
        self.projects.append(project)
        return project

    @staticmethod
    def free_port():
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0)); return sock.getsockname()[1]

    def image_record(self, project, label):
        source = json.loads((project['state'] / 'SOURCE.json').read_bytes())
        record = json.loads((project['state'] / 'image.json').read_bytes())
        value = {k: source[k] for k in ('commit', 'tree', 'version', 'build_time', 'toolchain')}
        value.update(source_commit=source['commit'], source_sha256=file_hash(project['state'] / 'SOURCE.json'),
                     image_id=record['image_id'], runtime_version=record['runtime_version'], tools=record['tools'])
        self.report['images'][label] = value
        return source

    def initialize_host(self):
        require(self.git(REPO, 'rev-parse', 'HEAD') == self.args.revision
                and self.git(REPO, 'status', '--porcelain', '--untracked-files=all') == '', 'authoritative source is not exact and clean')
        self.run(['git', '-c', 'safe.directory=' + str(REPO), '-C', REPO, 'merge-base', '--is-ancestor', UPGRADE_FROM, self.args.revision])
        head = self.args.pr_head
        require(SHA.fullmatch(head or ''), 'explicit actual provider head SHA required')
        self.run(['git', '-c', 'safe.directory=' + str(REPO), '-C', REPO, 'merge-base', '--is-ancestor', UPGRADE_FROM, head])
        self.report['checkout'] = {'commit': self.args.revision, 'tree': self.git(REPO, 'rev-parse', self.args.revision + '^{tree}'),
                                   'parents': self.git(REPO, 'show', '-s', '--format=%P', self.args.revision).split(),
                                   'head_commit': head, 'head_tree': self.git(REPO, 'rev-parse', head + '^{tree}'),
                                   'head_parents': self.git(REPO, 'show', '-s', '--format=%P', head).split(),
                                   'approved_A_is_ancestor': True, 'clean': True}
        require(Path('/proc/sys/net/bridge/bridge-nf-call-iptables').read_text().strip() == '1', 'bridge filtering prerequisite missing')
        for program in ('docker', 'iptables', 'iptables-save', 'iptables-restore', 'ip', 'nsenter', 'caddy', 'openssl', 'ffmpeg', 'ffprobe'):
            require(shutil.which(program), 'required host tool missing')
        for address in ('127.0.0.1', '0.0.0.0'):
            with socket.socket() as sock:
                sock.bind((address, 443))
        self.report['host'] = {'uname': list(os.uname()), 'docker': self.json(['docker', 'info', '--format', '{{json .}}'])['ServerVersion'],
                               'iptables': self.run(['iptables', '--version']).decode().strip(),
                               'caddy': self.run(['caddy', 'version']).decode().strip(),
                               'ffmpeg': self.run(['ffmpeg', '-version']).decode().splitlines()[0],
                               'ffprobe': self.run(['ffprobe', '-version']).decode().splitlines()[0]}
        networks = self.json(['docker', 'network', 'inspect', *self.run(['docker', 'network', 'ls', '-q']).decode().split()])
        self.initial_resources = {'container': set(self.run(['docker', 'ps', '-aq', '--no-trunc']).decode().split()),
                                  'network': {n['Id'] for n in networks},
                                  'volume': set(self.run(['docker', 'volume', 'ls', '--format', '{{.Name}}']).decode().splitlines())}
        ranges = network_subnets(networks)
        for octet in range(200 + secrets.randbelow(20), 240):
            if not any(ipaddress.ip_network(f'10.{octet}.0.0/16').overlaps(n) for n in ranges if n.version == 4):
                self.subnet_octet = octet; break
        else:
            raise RuntimeError('no unused owned subnet range available')
        self.initial_images = set(self.run(['docker', 'image', 'ls', '--no-trunc', '-q']).decode().split())
        self.initial_filter = self.run(['iptables-save', '-t', 'filter'])
        (self.output / 'filter-before.txt').write_bytes(self.initial_filter)
        self.old_repo, self.current_repo = self.clone('old-source', UPGRADE_FROM), self.clone('current-source', self.args.revision)
        self.old = self.make_project(self.old_repo, 'old', 40)
        self.current = self.make_project(self.current_repo, 'new', 40, same=self.old)
        self.restored = self.make_project(self.current_repo, 'restored', 42)
        self.control_name = 'cutmy-witness-' + self.token
        names = set(self.run(['docker', 'ps', '-a', '--format', '{{.Names}}']).decode().splitlines())
        volumes = set(self.run(['docker', 'volume', 'ls', '--format', '{{.Name}}']).decode().splitlines())
        existing_networks = {n['Name'] for n in networks}
        for project in (self.old, self.restored):
            prefix = project['config']['CUTMY_PROJECT']
            require(not any(n.startswith(prefix) for n in names)
                    and not self.run(['docker', 'ps', '-aq', '--filter', 'label=com.docker.compose.project=' + prefix]).strip()
                    and not {prefix + '_media', prefix + '_postgres'} & volumes
                    and not {prefix + '_processing', prefix + '_database'} & existing_networks, 'preexisting project collision')
        require(self.control_name not in names and self.control_name not in existing_networks, 'witness resource collision')

    def old_identity(self):
        self.wrapper(self.old, 'build', '--revision', UPGRADE_FROM)
        self.old_source = self.image_record(self.old, 'old')
        self.check('immutable_old_image', self.report['images']['old'])
        dirty = self.old_repo / 'acceptance-dirty.txt'
        dirty.write_text('owned dirty-checkout witness\n')
        attempted = {**self.old, 'state': self.work / 'dirty-state'}
        try:
            self.wrapper(attempted, 'build', '--revision', UPGRADE_FROM, expected=1)
            require(not attempted['state'].exists(), 'dirty source created build state')
        finally:
            dirty.unlink()
        attempted['state'] = self.work / 'wrong-revision-state'
        wrong = '0' * 40
        self.wrapper(attempted, 'build', '--revision', wrong, expected=1)
        require(not attempted['state'].exists(), 'wrong revision created build state')
        self.check('dirty_and_wrong_revision_refused')

    def current_identity(self):
        self.wrapper(self.current, 'build', '--revision', self.args.revision)
        self.current_source = self.image_record(self.current, 'current')
        require(self.report['images']['old']['image_id'] != self.report['images']['current']['image_id'],
                'image upgrade reused the old image')
        self.check('immutable_new_image', self.report['images']['current'])
        app = lambda s: {k: v for k, v in s['files'].items()
                         if k.startswith(('cmd/', 'internal/', 'pkg/')) or k in ('go.mod', 'go.sum')}
        require(app(self.old_source) == app(self.current_source), 'A/B application sources unexpectedly changed')
        self.report['runtime_application_sources_unchanged'] = True
        self.check('application_sources_unchanged', {'files': len(app(self.old_source)),
                    'sha256': digest(canonical(app(self.old_source))), 'schema_changing_upgrade': False})
        saved = self.current['state'] / 'source/README.md'
        original = saved.read_bytes()
        try:
            saved.write_bytes(original + b'\nowned mismatch\n')
            self.wrapper(self.current, 'status', expected=1)
        finally:
            saved.write_bytes(original)
        saved = self.current['state'] / 'compose.env'; original = saved.read_bytes()
        try:
            saved.write_bytes(original + b'CUTMY_UNREVIEWED_OVERRIDE=1\n')
            self.wrapper(self.current, 'status', expected=1)
        finally:
            saved.write_bytes(original)
        saved = self.current['state'] / 'image.json'; original = saved.read_bytes()
        value = json.loads(original); value['image_id'] = 'sha256:' + '0' * 64
        try:
            saved.write_bytes(canonical(value)); self.wrapper(self.current, 'status', expected=1)
        finally:
            saved.write_bytes(original)
        private_docker = self.work / 'remote-docker'; private_docker.mkdir(mode=0o700)
        remote_env = {'DOCKER_CONFIG': str(private_docker)}
        self.run(['docker', 'context', 'create', 'owned-unreachable', '--docker', 'host=tcp://127.0.0.1:1'], env=remote_env)
        self.run(['docker', 'context', 'use', 'owned-unreachable'], env=remote_env)
        self.run([sys.executable, '-B', self.current_repo / 'deploy/self-host.py', '--config', self.current['config_file'],
                  '--state', self.current['state'], 'status'], env=remote_env, expected=1)
        self.check('changed_state_and_remote_engine_refused')

    def start_process(self, label, command):
        log = (self.work / (label + '.log')).open('wb')
        try:
            process = subprocess.Popen([str(x) for x in command], stdout=log, stderr=subprocess.STDOUT,
                                       start_new_session=True,
                                       env={k: v for k, v in os.environ.items() if k in ('PATH', 'HOME', 'LANG', 'LC_ALL', 'TMPDIR', 'DOCKER_CONFIG')})
        except BaseException:
            log.close()
            raise
        self.processes.append((label, process, log))
        return process

    def stop_process(self, process):
        require(process.poll() is None, 'owned long process exited before expected stop')
        os.killpg(process.pid, signal.SIGTERM)
        try:
            process.wait(timeout=5)
        except subprocess.TimeoutExpired:
            os.killpg(process.pid, signal.SIGKILL); process.wait(timeout=3)
            raise RuntimeError('owned process required rescue kill') from None
        require(process.returncode in (0, -signal.SIGTERM), 'owned long process shutdown failed')
        process.acceptance_stopped = True

    def tls(self):
        self.ca, cert, key = self.work / 'ca.crt', self.work / 'server.crt', self.work / 'server.key'
        ca_key, request = self.work / 'ca.key', self.work / 'server.csr'
        self.run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=Disposable cutmy acceptance CA',
                  '-keyout', ca_key, '-out', self.ca])
        domains = [p['config']['CUTMY_DOMAIN'] for p in (self.old, self.restored)]
        self.run(['openssl', 'req', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=' + domains[0], '-keyout', key, '-out', request])
        extension = self.work / 'server.ext'
        extension.write_text('subjectAltName=' + ','.join('DNS:' + n for n in domains) + '\n')
        self.run(['openssl', 'x509', '-req', '-in', request, '-CA', self.ca, '-CAkey', ca_key, '-CAcreateserial',
                  '-days', '1', '-extfile', extension, '-out', cert])
        ca_key.chmod(0o600); key.chmod(0o600)
        self.caddyfile = self.work / 'Caddyfile'
        # Render the shipped host-Caddy proxy stanza. Only hostname, loopback
        # port and local test certificate authority differ from the example.
        example = (self.old_repo / 'deploy/Caddyfile.example').read_text()
        stanzas = []
        for project in (self.old, self.restored):
            stanza = example.replace('video.example.org', project['config']['CUTMY_DOMAIN']).replace(
                '127.0.0.1:8080', '127.0.0.1:' + project['config']['CUTMY_HTTP_PORT'])
            stanza = stanza.replace(project['config']['CUTMY_DOMAIN'] + ' {', project['config']['CUTMY_DOMAIN'] + ' {\n'
                                    '\tbind 127.0.0.1\n\ttls ' + str(cert) + ' ' + str(key), 1)
            stanzas.append(stanza)
        # A controlled extra SNI vhost presents the same trusted certificate
        # without a matching SAN. Thus the negative check exercises the client
        # hostname verifier rather than an unknown-vhost server alert.
        self.wrong_domain = 'wrong-' + self.token + '.acceptance.test'
        stanzas.append(stanzas[0].replace(domains[0], self.wrong_domain))
        self.caddyfile.write_text('{\n\tadmin off\n\tauto_https disable_redirects\n}\n' + '\n'.join(stanzas))
        self.run(['caddy', 'validate', '--config', self.caddyfile])
        self.caddy = self.start_process('caddy', ['caddy', 'run', '--config', self.caddyfile])
        self.wait(lambda: self.socket_open('127.0.0.1', 443), 15)
        require(self.caddy.poll() is None, 'owned Caddy exited')
        self.a, self.b = [Client(domains[0], self.ca, self.http.append, self.secret) for _ in range(2)]
        self.report['https'] = {'domains': domains, 'local_ca_sha256': file_hash(self.ca),
                                'server_certificate_sha256': file_hash(cert), 'caddyfile_sha256': file_hash(self.caddyfile),
                                'public_acme_dns_or_inbound_proof': False}

    @staticmethod
    def socket_open(host, port):
        try:
            with socket.create_connection((host, port), timeout=.3):
                return True
        except OSError:
            return False

    def fixture(self):
        directory = self.work / 'fixture'; directory.mkdir(mode=0o755); os.chown(directory, 1000, 1000)
        image = self.report['images']['old']['image_id']
        self.run(['docker', 'run', '--rm', '--label', 'video.cutmy.acceptance=' + self.token,
                  '--network', 'none', '--read-only', '--user', '1000:1000', '--cap-drop', 'ALL',
                  '--security-opt', 'no-new-privileges:true', '--memory', '256m', '--cpus', '1', '--pids-limit', '32',
                  '--mount', 'type=bind,src=' + str(directory) + ',dst=/fixture', '--entrypoint', 'ffmpeg', image,
                  '-v', 'error', '-nostdin', '-f', 'lavfi', '-i', 'testsrc2=size=160x96:rate=24', '-f', 'lavfi',
                  '-i', 'sine=frequency=660:sample_rate=48000:duration=4', '-t', '4', '-c:v', 'libx264', '-threads', '1',
                  '-pix_fmt', 'yuv420p', '-g', '24', '-keyint_min', '24', '-sc_threshold', '0',
                  '-c:a', 'aac', '-b:a', '128k', '-movflags', '+faststart', '/fixture/input.mp4'])
        self.input = directory / 'input.mp4'
        require(32 < self.input.stat().st_size < 2 * 1024 * 1024, 'synthetic fixture bounds failed')
        self.report['fixture'] = {'bytes': self.input.stat().st_size, 'sha256': file_hash(self.input),
                                   'generated_by_actual_old_image': image}
        frames = self.json(['ffprobe', '-v', 'error', '-select_streams', 'v:0', '-skip_frame', 'nokey', '-show_frames',
                            '-show_entries', 'frame=best_effort_timestamp_time', '-of', 'json', self.input])['frames']
        times = [float(x['best_effort_timestamp_time']) for x in frames]
        require(len(times) == 4 and all(abs(value - i) < 1/24 for i, value in enumerate(times)), 'fixture keyframe oracle mismatch')
        self.report['fixture']['physical_keyframe_seconds'] = times

    def decode(self, content, name, *, video=True, seconds=1.5):
        path = self.output / name
        require(not path.exists() and re.fullmatch(r'[a-z0-9_-]+\.(?:mp4|mp3)', name), 'new bounded output name required')
        path.write_bytes(content)
        probe = self.json(['ffprobe', '-v', 'error', '-show_streams', '-show_format', '-of', 'json', path])
        duration = float(probe['format']['duration'])
        require(abs(duration - seconds) <= .25, 'decoded duration violates requested span/padding bounds')
        audio = [s for s in probe['streams'] if s.get('codec_type') == 'audio']
        pictures = [s for s in probe['streams'] if s.get('codec_type') == 'video']
        require(len(audio) == 1 and (len(pictures) == 1 if video else len(pictures) == 0), 'selected output streams missing')
        self.run(['ffmpeg', '-v', 'error', '-nostdin', '-i', path, '-f', 'null', '-'])
        pcm = self.run(['ffmpeg', '-v', 'error', '-nostdin', '-i', path, '-vn', '-ac', '1', '-ar', '8000', '-f', 's16le', '-'])
        samples = struct.unpack('<' + 'h' * (len(pcm) // 2), pcm)
        require(abs(len(samples)/8000 - seconds) <= .25, 'full-span audio was truncated')
        for offset in (0, len(samples)//2, len(samples)-2000):
            window = samples[offset:offset+2000]
            require(len(window) == 2000 and math.sqrt(sum(x*x for x in window)/len(window)) > 300,
                    'selected-span audio is missing or silent')
        frame_count = 0
        if video:
            require((pictures[0]['width'], pictures[0]['height']) == (160, 96), 'unexpected decoded output geometry')
            rgb = self.run(['ffmpeg', '-v', 'error', '-nostdin', '-i', path, '-an', '-fps_mode', 'passthrough',
                            '-pix_fmt', 'rgb24', '-f', 'rawvideo', '-'])
            size = 160*96*3; frame_count = len(rgb)//size
            require(len(rgb) % size == 0 and abs(frame_count - 24*seconds) <= 3
                    and digest(rgb[:size]) != digest(rgb[-size:]), 'full-span moving video witness failed')
        return {'name': name, 'sha256': file_hash(path), 'bytes': len(content),
                'duration_seconds': duration, 'expected_seconds': seconds, 'padding_tolerance_seconds': .25,
                'decoded_audio_samples_8000hz': len(samples), 'decoded_video_frames': frame_count,
                'independent_host_decode': True}

    def export(self, client, source, name, *, fmt='mp4', mode='accurate'):
        body = {'source_id': source['id'], 'ranges': [{'start_ms': 1000, 'end_ms': 2500, 'label': 'Disposable acceptance'}],
                'format': fmt, 'quality': 'best', 'cut_mode': mode}
        job, _ = client.request('POST', '/api/v1/jobs', data=body, headers={'Idempotency-Key': name}, expected=202)
        require(job['status'] in ('queued', 'running', 'succeeded') and len(job['items']) == 1, 'export was not accepted')
        result = None
        def finished():
            nonlocal result
            result, _ = client.request('GET', '/api/v1/jobs/' + job['id'])
            require(result['status'] in ('queued', 'running', 'succeeded'), 'export failed')
            return result['status'] == 'succeeded'
        self.wait(finished, 60)
        artifact = result['items'][0]['artifact']
        require(result['items'][0]['status'] == 'succeeded' and artifact['size_bytes'] > 32, 'successful export has no artifact')
        content, _ = client.request('GET', artifact['download_url'])
        require(len(content) == artifact['size_bytes'], 'acknowledged artifact size mismatch')
        decoded = self.decode(content, name + '.' + fmt, video=fmt == 'mp4')
        decoded['requested_range_ms'] = [body['ranges'][0]['start_ms'], body['ranges'][0]['end_ms']]
        decoded['acknowledged_actual_range_ms'] = [artifact['actual_start_ms'], artifact['actual_end_ms']]
        decoded['cut_mode'] = mode
        return {'source': source, 'body': body, 'key': name, 'job': result, 'artifact': artifact, 'decoded': decoded}

    def accounting(self, project):
        snap = self.snapshot(project)
        counter = snap['storage_counters']
        require(len(counter) == 1 and int(counter[0]['stored_bytes']) == sum(int(x['size_bytes']) for x in snap['storage_files'])
                and int(counter[0]['reserved_bytes']) == sum(int(x['size_bytes']) for x in snap['storage_reservations']),
                'storage counters disagree with ledger')
        paths = {x['path']: int(x['size_bytes']) for x in snap['storage_files']}
        for row in snap['storage_reservations']:
            if row['kind'] == 'preview_cache':
                paths['/data/previews/' + row['id'] + '/preview.mp4'] = int(row['size_bytes'])
        code = ('import json,os,sys,hashlib; expected=json.loads(sys.stdin.read()); result={}; '
                '\nfor p,n in expected.items():\n'
                ' assert p.startswith("/data/") and os.path.isfile(p) and not os.path.islink(p) and os.stat(p).st_size==n\n'
                ' result[p]={"bytes":n,"sha256":hashlib.sha256(open(p,"rb").read()).hexdigest()}\n'
                'print(json.dumps(result,sort_keys=True))')
        files = self.json(self.compose(project) + ['exec', '-T', 'api', 'python3', '-c', code], input=canonical(paths))
        return {'stored_bytes': int(counter[0]['stored_bytes']), 'reserved_bytes': int(counter[0]['reserved_bytes']),
                'registered_file_count': len(paths), 'file_inventory_sha256': digest(canonical(files))}, files

    def echo(self, address, port, token):
        with socket.create_connection((address, port), timeout=2) as sock:
            require(sock.recv(128).decode() == token, 'controlled listener nonce was not observed')

    def pg_listener(self, project):
        readiness = self.pg_tcp_ready(project, baseline=project is self.old)
        pg = self.container(project, 'postgres')
        require(pg['State']['Running'] and pg['State']['Pid'] > 1, 'owned PG namespace is not running')
        token = secrets.token_hex(20)
        address, port = project['config']['CUTMY_DATABASE_PREFIX'] + '.2', 35443
        code = ('import socket; s=socket.socket(); s.bind(("' + address + '",' + str(port) + ')); s.listen(16); '
                '\nwhile True:\n c,_=s.accept(); c.sendall(b"' + token + '"); c.close()\n')
        process = self.start_process('pg-witness-' + project['state'].name,
                                     ['nsenter', '--target', str(pg['State']['Pid']), '--net', sys.executable, '-u', '-c', code])
        self.wait(lambda: self.socket_open(address, port), 10)
        self.echo(address, port, token)
        project['pg_witness'] = {'address': address, 'port': port, 'token': token, 'process': process}
        project['pg_tcp_readiness'] = readiness

    def pg_tcp_ready(self, project, *, baseline=False):
        pg = self.container(project, 'postgres')
        test = pg['Config'].get('Healthcheck', {}).get('Test')
        if baseline:
            require(test == SOCKET_HEALTHCHECK_A,
                    'immutable A socket-healthcheck definition changed')
        else:
            require(test == TCP_HEALTHCHECK,
                    'current PostgreSQL lacks exact TCP healthcheck')
        attempts = []
        def ready():
            code, stdout, _ = self.sql(project, 'SELECT 1', expected=None, timeout=8)
            attempts.append({'exit': code, 'tcp_sql_ready': code == 0 and stdout.strip() == b'1'})
            return attempts[-1]['tcp_sql_ready']
        self.wait(ready, 120)
        observed = self.container(project, 'postgres')
        require(observed['Id'] == pg['Id'] and observed['State']['Running']
                and observed['State'].get('Health', {}).get('Status') == 'healthy', 'PG readiness identity/health changed')
        return {'container_id': pg['Id'], 'healthcheck': test, 'tcp_sql_verified': True, 'attempts': attempts,
                'source_commit': self.report['images']['old' if baseline else 'current']['source_commit'],
                'baseline_socket_healthcheck_workaround': baseline,
                'baseline_defect_fixed': False if baseline else None}

    def setup_witnesses(self):
        self.host_witness = Witness(('0.0.0.0', 0)); self.witnesses.append(self.host_witness)
        # A unique controlled unused link-local address, never a cloud metadata
        # address. Only our nonce listener is contacted; no metadata/token fetch.
        self.network_address = '169.254.247.' + str(20 + secrets.randbelow(200))
        addresses = self.json(['ip', '-j', 'address', 'show'])
        require(not any(a.get('local') == self.network_address for i in addresses for a in i.get('addr_info', [])),
                'synthetic metadata address already in use')
        self.address_add_attempted = True
        self.run(['ip', 'address', 'add', self.network_address + '/32', 'dev', 'lo'])
        self.metadata_witness = Witness((self.network_address, 0)); self.witnesses.append(self.metadata_witness)
        self.control_token = secrets.token_hex(20)
        self.control_ip, self.control_port = f'10.{self.subnet_octet}.44.2', 35444
        label = 'video.cutmy.acceptance=' + self.token
        network = self.run(['docker', 'network', 'create', '--label', label, '--subnet', f'10.{self.subnet_octet}.44.0/24',
                            self.control_name]).decode().strip()
        self.extra_resources.append(('network', network))
        code = ('import socket; s=socket.socket(); s.bind(("0.0.0.0",' + str(self.control_port) + ')); s.listen(16); '
                '\nwhile True:\n c,_=s.accept(); c.sendall(b"' + self.control_token + '"); c.close()\n')
        cid = self.run(['docker', 'create', '--name', self.control_name, '--label', label, '--network', self.control_name,
                        '--ip', self.control_ip, '--read-only', '--user', '1000:1000', '--cap-drop', 'ALL',
                        '--security-opt', 'no-new-privileges:true', '--memory', '128m', '--pids-limit', '16',
                        '--entrypoint', 'python3', self.report['images']['old']['image_id'], '-u', '-c', code]).decode().strip()
        self.extra_resources.append(('container', cid))
        self.control_cid = cid
        self.run(['docker', 'start', cid]); self.wait(lambda: self.socket_open(self.control_ip, self.control_port), 10)
        self.echo(self.control_ip, self.control_port, self.control_token)

    def verify_witnesses(self, project):
        self.host_witness.verify('127.0.0.1'); self.metadata_witness.verify(self.network_address)
        self.echo(self.control_ip, self.control_port, self.control_token)
        value = project['pg_witness']
        require(value['process'].poll() is None, 'controlled PG wrong-port listener exited')
        self.echo(value['address'], value['port'], value['token'])

    def probes(self, project, *, before=False):
        self.verify_witnesses(project)
        cfg = project['config']; pg = project['pg_witness']
        expected = {'db_gateway': digest(self.host_witness.token), 'pg_wrong_port': digest(pg['token'].encode())}
        destinations = {'db_gateway': [cfg['CUTMY_DATABASE_PREFIX'] + '.1', self.host_witness.port],
                        'pg_wrong_port': [pg['address'], pg['port']], 'own_pg': [pg['address'], 5432]}
        results = {}
        for role in ('api', 'worker', 'migrate', 'maintenance'):
            targets, nonces = dict(destinations), dict(expected)
            if role in ('api', 'worker'):
                targets.update(processing_gateway=[cfg['CUTMY_PROCESSING_PREFIX'] + '.1', self.host_witness.port],
                               controlled_metadata=[self.network_address, self.metadata_witness.port],
                               forwarded_private=[self.control_ip, self.control_port])
                nonces.update(processing_gateway=digest(self.host_witness.token), controlled_metadata=digest(self.metadata_witness.token),
                              forwarded_private=digest(self.control_token.encode()))
            code = ('import socket,json,hashlib; targets=' + repr(targets) + '; result={};\n'
                    'for name,address in targets.items():\n'
                    ' try:\n  s=socket.create_connection(tuple(address),timeout=1); result[name]={"connected":True}; '
                    '\n  if name!="own_pg": result[name]["nonce_sha256"]=hashlib.sha256(s.recv(128)).hexdigest()\n'
                    '  s.close()\n except OSError: result[name]={"connected":False}\n'
                    'print(json.dumps(result,sort_keys=True))')
            if role in ('api', 'worker') and not before:
                command = self.compose(project) + ['exec', '-T', role, 'python3', '-c', code]
            else:
                command = self.compose(project) + ['run', '--rm', '-T', '--no-deps', '--entrypoint', 'python3', role, '-c', code]
            value = self.json(command, timeout=30)
            self.report.setdefault('controlled_probe_attempts', []).append(
                {'project': cfg['CUTMY_PROJECT'], 'role': role, 'before_policy': before, 'destinations': value})
            self.save()
            require(isinstance(value, dict) and set(value) == set(targets),
                    'controlled probe destination shape invalid role=' + role)
            for name, observed in value.items():
                require(isinstance(observed, dict) and isinstance(observed.get('connected'), bool),
                        'controlled probe result shape invalid role=' + role + ' destination=' + name)
                keys = {'connected', 'nonce_sha256'} if observed['connected'] and name != 'own_pg' else {'connected'}
                require(set(observed) == keys and ('nonce_sha256' not in observed
                        or isinstance(observed['nonce_sha256'], str) and HASH.fullmatch(observed['nonce_sha256'])),
                        'controlled probe nonce shape invalid role=' + role + ' destination=' + name)
            require(value['own_pg']['connected'], 'exact own PG5432 was blocked role=' + role
                    + ' destination=own_pg connected=false')
            for name, nonce in nonces.items():
                state = (' role=' + role + ' destination=' + name
                         + ' connected=' + str(value[name]['connected']).lower()
                         + ' nonce_match=' + str(value[name].get('nonce_sha256') == nonce).lower())
                if before:
                    require(value[name]['connected'] and value[name].get('nonce_sha256') == nonce,
                            'required pre-policy controlled socket witness unavailable' + state)
                else:
                    require(value[name]['connected'] is False, 'policy allowed a forbidden controlled socket' + state)
            results[role] = value
        self.verify_witnesses(project)
        return results

    def public_https(self, project):
        # Real public HTTPS and real certificate validation, never a private
        # endpoint impersonating public traffic or an unavailable-witness skip.
        code = ('import socket,ssl,ipaddress,json,hashlib; '
                'a=socket.getaddrinfo("github.com",443,socket.AF_INET,socket.SOCK_STREAM)[0][4]; '
                'assert ipaddress.ip_address(a[0]).is_global; '
                's=ssl.create_default_context().wrap_socket(socket.create_connection(a,timeout=5),server_hostname="github.com"); '
                's.sendall(b"HEAD / HTTP/1.1\\r\\nHost: github.com\\r\\nConnection: close\\r\\n\\r\\n"); '
                'r=s.recv(256); assert r.startswith(b"HTTP/"); '
                'print(json.dumps({"peer_ipv4":a[0],"certificate_sha256":hashlib.sha256(s.getpeercert(binary_form=True)).hexdigest(),'
                '"validated_https":True})); s.close()')
        return {role: self.json(self.compose(project) + ['exec', '-T', role, 'python3', '-c', code], timeout=15)
                for role in ('api', 'worker')}

    def unrelated(self):
        code = ('import socket,hashlib,json; s=socket.create_connection(("10.' + str(self.subnet_octet) + '.44.1",'
                + str(self.host_witness.port) + '),timeout=2); print(json.dumps({"nonce_sha256":hashlib.sha256(s.recv(128)).hexdigest()})); s.close()')
        value = self.json(['docker', 'exec', self.control_cid, 'python3', '-c', code])
        require(value['nonce_sha256'] == digest(self.host_witness.token), 'unrelated namespace traffic changed')
        return value

    def initial_egress(self):
        self.wrapper(self.old, 'prepare')
        # Only database and bounded probe commands run before installation;
        # no API/worker media service starts ahead of the policy.
        self.run(self.compose(self.old) + ['up', '-d', '--wait', 'postgres'], timeout=150)
        # Immutable A's socket-only pg_isready can observe the temporary init
        # server. This disclosed harness barrier waits for final TCP explicitly.
        self.report['baseline_tcp_readiness'] = self.pg_tcp_ready(self.old, baseline=True)
        for role in ('api', 'worker'):
            value = self.container(self.old, role)
            require(value['State']['Running'] is False, 'pre-policy media service already running')
            self.run(['docker', 'rm', value['Id']])
        self.setup_witnesses(); self.pg_listener(self.old)
        (self.output / 'filter-pre-policy.txt').write_bytes(self.run(['iptables-save', '-t', 'filter']))
        before = self.probes(self.old, before=True)
        unrelated_before = self.unrelated()
        self.check('controlled_listener_witnesses', {'before': before, 'synthetic_link_local': self.network_address,
                                                    'real_cloud_metadata_contacted': False})
        self.wrapper(self.old, 'prepare'); self.policy(self.old, 'apply'); self.policy(self.old, 'verify')
        self.wrapper(self.old, 'initialize'); self.wrapper(self.old, 'up')
        after = self.probes(self.old)
        self.check('both_bridges_and_operator_denials', {'after': after})
        self.check('exact_pg_and_public_https', self.public_https(self.old))
        require(self.unrelated() == unrelated_before, 'unrelated traffic differs')
        self.check('unrelated_traffic_preserved', unrelated_before)

    def old_media(self):
        self.tls(); self.fixture()
        self.a.session(); self.b.session()
        require(self.a.cookie != self.b.cookie, 'owners share a session')
        bad = Client(self.wrong_domain, self.ca, self.http.append, self.secret)
        try:
            bad.request('GET', '/api/v1/session')
        except ssl.SSLCertVerificationError:
            pass
        else:
            raise RuntimeError('wrong TLS hostname was accepted')
        untrusted = Client(self.old['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret)
        untrusted.context = ssl.create_default_context()
        try:
            untrusted.request('GET', '/api/v1/session')
        except ssl.SSLCertVerificationError:
            pass
        else:
            raise RuntimeError('untrusted TLS CA was accepted')
        self.a.request('POST', '/api/v1/jobs', data={}, headers={'Origin': 'https://wrong.example.test'}, expected=403)
        self.check('verified_tls_and_secure_origin')
        self.source_a, self.source_b = self.a.upload(self.input), self.b.upload(self.input)
        original, _ = self.a.request('GET', '/api/v1/sources/' + self.source_a['id'] + '/media')
        require(digest(original) == file_hash(self.input), 'uploaded source bytes changed')
        self.b.request('GET', '/api/v1/sources/' + self.source_a['id'], expected=404)
        self.check('two_owners_and_exact_upload', {'source_ids': [self.source_a['id'], self.source_b['id']], 'sha256': digest(original)})
        url = '/api/v1/sources/' + self.source_a['id'] + '/preview?start_ms=0'
        preview, headers = self.a.request('GET', url)
        require(headers.get('X-Preview-Cache') == 'miss', 'preview did not encode a miss')
        cached, headers = self.a.request('GET', url)
        require(headers.get('X-Preview-Cache') == 'hit' and cached == preview, 'preview cache differs')
        self.b.request('GET', url, expected=404)
        self.check('preview_miss_hit_decode', self.decode(preview, 'old-preview.mp4', seconds=4.0))
        self.retained_outputs = [self.export(self.a, self.source_a, 'old-accurate'),
                         self.export(self.a, self.source_a, 'old-mp3', fmt='mp3'),
                         self.export(self.b, self.source_b, 'old-copy', mode='copy')]
        for index, result in enumerate(self.retained_outputs):
            owner, foreign = (self.b, self.a) if index == 2 else (self.a, self.b)
            foreign.request('GET', result['artifact']['download_url'], expected=404)
            partial, headers = owner.request('GET', result['artifact']['download_url'], headers={'Range': 'bytes=0-15'}, expected=206)
            require(len(partial) == 16 and headers.get('Content-Range', '').startswith('bytes 0-15/'), 'download range failed')
        self.check('mp4_mp3_copy_decode_ownership_range', {'results': [r['decoded'] for r in self.retained_outputs]})
        disposable = self.a.upload(self.input)
        removed = self.export(self.a, disposable, 'deleted-output')
        deleted_paths = json.loads(self.sql(self.old, "SELECT COALESCE(jsonb_agg(path),'[]') FROM storage_files WHERE resource_id IN ('"
                                            + disposable['id'] + "','" + removed['artifact']['id'] + "')"))
        require(deleted_paths, 'deletion has no real registered files')
        self.a.delete(disposable['id'])
        self.a.request('GET', '/api/v1/sources/' + disposable['id'], expected=404)
        self.a.request('GET', removed['artifact']['download_url'], expected=404)
        # Deletion is asynchronous but must converge physically and in ledger.
        def deleted():
            rows = json.loads(self.sql(self.old, "SELECT jsonb_build_object('sources',(SELECT count(*) FROM sources WHERE id='" + disposable['id']
                                      + "'),'artifacts',(SELECT count(*) FROM artifacts WHERE id='" + removed['artifact']['id'] + "'))"))
            charged = self.sql(self.old, "SELECT count(*) FROM storage_files WHERE resource_id IN ('" + disposable['id']
                               + "','" + removed['artifact']['id'] + "')").decode().strip()
            return rows == {'sources': 0, 'artifacts': 0} and charged == '0'
        self.wait(deleted, 30)
        accounts, files = self.accounting(self.old)
        require(not set(deleted_paths) & set(files), 'deleted media remains registered')
        paths = self.json(self.compose(self.old) + ['exec', '-T', 'api', 'python3', '-c',
                           'import os,json; print(json.dumps([os.path.join(r,f) for r,ds,fs in os.walk("/data") for f in fs]))'])
        require(not set(deleted_paths) & set(paths), 'deleted physical files remain')
        self.check('deletion_physical_and_accounting', accounts)

    def backup_record(self, directory):
        record = json.loads((directory / 'backup.json').read_bytes())
        for name, item in record['files'].items():
            require(file_hash(directory / name) == item['sha256'] and (directory / name).stat().st_size == item['bytes'],
                    'actual private backup hash/size mismatch')
        # Only identities/hashes/sizes are exported. Dump, tar, config and
        # private state remain in the owned private directory until cleanup.
        return record

    def verify_retained(self, project, owner, foreign, *, prefix):
        results = []
        for index, original in enumerate(self.retained_outputs):
            a, b = (foreign, owner) if index == 2 else (owner, foreign)
            source, _ = a.request('GET', '/api/v1/sources/' + original['source']['id'])
            require(source['id'] == original['source']['id'], 'retained source identity changed')
            content, _ = a.request('GET', '/api/v1/sources/' + source['id'] + '/media')
            require(digest(content) == file_hash(self.input), 'retained source bytes changed')
            replay, _ = a.request('POST', '/api/v1/jobs', data=original['body'], headers={'Idempotency-Key': original['key']}, expected=202)
            require(replay['id'] == original['job']['id'], 'retained idempotency replay duplicated job')
            current, _ = a.request('GET', '/api/v1/jobs/' + replay['id'])
            require(current['items'][0]['id'] == original['job']['items'][0]['id']
                    and current['items'][0]['artifact']['id'] == original['artifact']['id'], 'retained item/artifact identity changed')
            b.request('GET', original['artifact']['download_url'], expected=404)
            content, _ = a.request('GET', original['artifact']['download_url'])
            require(digest(content) == original['decoded']['sha256'], 'acknowledged output bytes changed')
            results.append(self.decode(content, prefix + '-' + str(index) + '.' + original['body']['format'],
                                       video=original['body']['format'] == 'mp4'))
        return results

    def backup_and_upgrade(self):
        self.old_accounting, old_files = self.accounting(self.old)
        self.old_backup = self.work / 'backup-A'
        self.wrapper(self.old, 'backup', '--directory', self.old_backup)
        self.check('coordinated_backup_old_image', self.backup_record(self.old_backup))
        require(all(self.container(self.old, role)['State']['Running'] is False for role in ('api', 'worker')),
                'old backup did not stop writers')
        # Build B only after actual A media + backup. This new image has the
        # hosted merge SOURCE identity; the app source is intentionally equal.
        self.group('identity', self.current_identity)
        self.active_group = 'backup_restore'
        pg_before = self.container(self.old, 'postgres')
        self.stop_process(self.old['pg_witness']['process'])
        self.wrapper(self.current, 'initialize'); self.wrapper(self.current, 'up')
        pg_after = self.container(self.current, 'postgres')
        persistent = self.current['config']['CUTMY_PROJECT'] + '_postgres'
        pg_volume = lambda value: [m.get('Name') for m in value['Mounts']
                                  if m.get('Type') == 'volume' and m.get('Destination') == '/var/lib/postgresql/data']
        require(pg_volume(pg_before) == pg_volume(pg_after) == [persistent], 'image upgrade replaced the persistent PG volume')
        self.pg_listener(self.current)
        require(all(self.container(self.current, role)['Image'] == self.report['images']['current']['image_id']
                    for role in ('api', 'worker')), 'same-project upgrade did not execute the new image')
        self.check('actual_same_project_image_upgrade', {'old': self.report['images']['old'], 'current': self.report['images']['current'],
                                                        'application_code_unchanged': True,
                                                        'postgres_transition': {'before_id': pg_before['Id'], 'after_id': pg_after['Id'],
                                                                               'before_pid': pg_before['State']['Pid'], 'after_pid': pg_after['State']['Pid'],
                                                                               'persistent_volume': persistent,
                                                                               'old_netns_witness_stopped_before_recreate': True}})
        self.report['current_tcp_readiness'] = self.current['pg_tcp_readiness']
        results = self.verify_retained(self.current, self.a, self.b, prefix='upgraded')
        accounting, files = self.accounting(self.current)
        require(files == old_files and accounting == self.old_accounting, 'image upgrade changed retained physical ledger')
        self.check('retained_ids_sessions_hashes_idempotency', {'outputs': results, 'accounting': accounting})
        self.upgraded_fresh = self.export(self.a, self.source_a, 'upgraded-new-output')
        self.current_backup = self.work / 'backup-B'
        self.restore_snapshot = self.snapshot(self.current)
        self.restore_accounting, self.restore_files = self.accounting(self.current)
        self.wrapper(self.current, 'backup', '--directory', self.current_backup)
        self.backup_b = self.backup_record(self.current_backup)
        self.wrapper(self.restored, 'adopt-image', '--from-state', self.current['state'])
        self.image_record(self.restored, 'restored')
        failures = []
        for kind in ('partial', 'wrong-image', 'unsafe-tar'):
            directory = self.work / ('invalid-backup-' + kind)
            shutil.copytree(self.current_backup, directory)
            if kind == 'partial':
                (directory / 'database.dump').write_bytes(b'incomplete')
            else:
                meta = json.loads((directory / 'backup.json').read_bytes())
                if kind == 'wrong-image':
                    meta['image_id'] = self.report['images']['old']['image_id']
                else:
                    with tarfile.open(directory / 'media.tar.gz', 'w:gz', format=tarfile.GNU_FORMAT) as archive:
                        member = tarfile.TarInfo('../outside-owned-restore'); member.size = 1
                        archive.addfile(member, io.BytesIO(b'x'))
                    os.chmod(directory / 'media.tar.gz', 0o600)
                    meta['files']['media.tar.gz'] = {'bytes': (directory / 'media.tar.gz').stat().st_size,
                                                    'sha256': file_hash(directory / 'media.tar.gz')}
                (directory / 'backup.json').write_bytes(canonical(meta))
            self.wrapper(self.restored, 'restore', '--directory', directory, expected=1)
            require(self.project_resources(self.restored) == [], 'invalid backup mutated fresh project')
            failures.append(kind)
        name = self.restored['config']['CUTMY_PROJECT'] + '_media'
        self.run(['docker', 'volume', 'create', '--label', 'video.cutmy.acceptance=' + self.token, name])
        self.extra_resources.append(('volume', name))
        self.wrapper(self.restored, 'restore', '--directory', self.current_backup, expected=1)
        owned_resource(self.json(['docker', 'volume', 'inspect', name])[0], token=self.token)
        self.run(['docker', 'volume', 'rm', name]); self.extra_resources.remove(('volume', name))
        require(self.backup_record(self.current_backup) == self.backup_b, 'refusal modified accepted backup')
        self.check('backup_refusals_without_mutation', {'cases': failures + ['existing-volume']})
        self.wrapper(self.restored, 'restore', '--directory', self.current_backup)
        require(all(self.container(self.restored, role)['State']['Running'] is False for role in ('api', 'worker')),
                'restore started apps before policy')
        require(self.snapshot(self.restored) == self.restore_snapshot, 'actual pg_restore changed retained application rows')
        self.policy(self.restored, 'apply'); self.wrapper(self.restored, 'initialize'); self.wrapper(self.restored, 'up')
        self.pg_listener(self.restored)
        self.report['restored_tcp_readiness'] = self.restored['pg_tcp_readiness']
        self.restore_a, self.restore_b = [Client(self.restored['config']['CUTMY_DOMAIN'], self.ca, self.http.append, self.secret) for _ in range(2)]
        # A different test domain receives the same retained capabilities
        # explicitly; this proves server-side preservation, not browser-cookie
        # migration between domains. Same-domain A→B above needs no transfer.
        self.restore_a.cookie, self.restore_b.cookie = self.a.cookie, self.b.cookie
        self.check('actual_dump_media_fresh_restore', {'backup': self.backup_b,
                                                      'old_media_volume': self.current['config']['CUTMY_PROJECT'] + '_media',
                                                      'new_media_volume': self.restored['config']['CUTMY_PROJECT'] + '_media',
                                                      'different_domain_capabilities_explicitly_transferred': True})
        results = self.verify_retained(self.restored, self.restore_a, self.restore_b, prefix='restored')
        accounts, files = self.accounting(self.restored)
        require(accounts == self.restore_accounting and files == self.restore_files, 'restored physical accounting differs')
        fresh = self.restore_a.upload(self.input)
        outputs = [self.export(self.restore_a, fresh, 'restored-new-mp4'),
                   self.export(self.restore_a, fresh, 'restored-new-mp3', fmt='mp3'),
                   self.export(self.restore_b, self.source_b, 'restored-new-copy', mode='copy')]
        self.check('restored_bytes_counters_and_new_exports', {'retained': results, 'accounting_before_new_exports': accounts,
                                                             'fresh_outputs': [r['decoded'] for r in outputs]})

    def catalog(self, project):
        query = ("SELECT jsonb_build_object('tables',(SELECT jsonb_agg(tablename ORDER BY tablename) FROM pg_tables WHERE schemaname='public'),"
                 "'versions',(SELECT jsonb_agg(to_jsonb(x) ORDER BY version) FROM app_schema_versions x),"
                 "'migrations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY position) FROM app_schema_migrations x),"
                 "'roles',(SELECT jsonb_agg(rolname ORDER BY rolname) FROM pg_roles))")
        return json.loads(self.sql(project, query))

    def roles(self):
        self.wrapper(self.current, 'up')
        require(self.sql(self.current, 'SELECT current_user,session_user', runtime=True).decode().strip()
                == 'cutmy_runtime|cutmy_runtime', 'runtime TCP credentials identity differs')
        self.check('runtime_tcp_identity')
        before = self.catalog(self.current)
        cases = {'ledger_insert': "INSERT INTO app_schema_versions(version) VALUES('99999999-acceptance-denied')",
                 'ledger_update': 'UPDATE app_schema_migrations SET checksum=checksum',
                 'ddl': 'CREATE TABLE public.acceptance_forbidden(i integer)',
                 'role': 'CREATE ROLE acceptance_forbidden_role',
                 'temporary': 'CREATE TEMP TABLE acceptance_forbidden_temp(i integer)',
                 'server_file': "SELECT pg_read_file('/etc/passwd',0,64)"}
        results = {}
        for name, query in cases.items():
            code, stdout, stderr = self.sql(self.current, query, runtime=True, expected=None)
            require(code == 3 and re.search(rb'\b42501\b', stderr) and not stdout.strip(), 'runtime privilege denial did not happen')
            results[name] = {'exit': code, 'sqlstate': '42501'}
        self.check('runtime_privilege_denials', results)
        require(self.catalog(self.current) == before, 'denied runtime operation changed catalog/history')
        self.check('unchanged_schema_and_history', {'sha256': digest(canonical(before))})
        self.wrapper(self.current, 'stop')
        initial = int(self.sql(self.current, 'SELECT reserved_bytes FROM storage_counters WHERE id=1'))
        statement = ("BEGIN; INSERT INTO storage_reservations(id,kind,size_bytes,expires_at) VALUES('acceptance_" + self.token
                     + "','acceptance_counter',7,clock_timestamp()+interval '1 minute'); "
                     "SELECT reserved_bytes FROM storage_counters WHERE id=1; ROLLBACK;")
        require(int(self.sql(self.current, statement, runtime=True)) == initial + 7, 'runtime DML did not execute counter trigger')
        require(int(self.sql(self.current, 'SELECT reserved_bytes FROM storage_counters WHERE id=1')) == initial,
                'rolled-back trigger changed durable accounting')
        self.wrapper(self.current, 'maintenance')
        require(self.catalog(self.current) == before, 'routine runtime maintenance modified migration history')
        self.wrapper(self.current, 'up'); accounts, _ = self.accounting(self.current)
        self.check('reduced_grant_counter_and_maintenance', accounts)

    def constraint(self, value, role, project):
        h, cfg = value['HostConfig'], value['Config']
        require(cfg['User'] == ('999:999' if role == 'postgres' else '1000:1000')
                and h['ReadonlyRootfs'] is True and not h['Privileged']
                and h['CapDrop'] == ['ALL'] and not h.get('CapAdd')
                and any(x.startswith('no-new-privileges') for x in h['SecurityOpt'])
                and h['PidsLimit'] == 128 and h['LogConfig']['Type'] == 'json-file'
                and h['LogConfig']['Config'] == {'max-file': '3', 'max-size': '10m'}, 'actual container restrictions differ')
        memory = {'api': 1 << 30, 'worker': 2 << 30, 'postgres': 512 << 20, 'migrate': 512 << 20, 'maintenance': 512 << 20}[role]
        require(h['Memory'] == memory and h['NanoCpus'] == (2_000_000_000 if role == 'worker' else 1_000_000_000),
                'actual CPU/memory bounds differ')
        require(h['RestartPolicy']['Name'] == ('unless-stopped' if role == 'postgres' else 'no'), 'unsafe restart policy')
        env = dict(x.split('=', 1) for x in cfg['Env'])
        secrets_ok = True
        if role != 'postgres':
            require(project['config']['CUTMY_ADMIN_PASSWORD'] not in '\n'.join(cfg['Env']), 'admin secret entered an app')
            if role != 'migrate':
                require(project['config']['CUTMY_MIGRATION_PASSWORD'] not in '\n'.join(cfg['Env']), 'migration secret entered runtime')
            require(urlsplit(env['DATABASE_URL']).username == ('cutmy_migrate' if role == 'migrate' else 'cutmy_runtime'),
                    'actual database-role separation differs')
        expected_mounts = {'/var/lib/postgresql/data', '/docker-entrypoint-initdb.d/10-cutmy-roles.sql'} if role == 'postgres' else {'/data'}
        non_tmp = {m['Destination'] for m in value['Mounts'] if m['Type'] != 'tmpfs'}
        require(non_tmp == expected_mounts and '/tmp' in h['Tmpfs'], 'unexpected actual container mounts')
        for mount in value['Mounts']:
            if mount['Destination'].startswith('/docker-entrypoint-initdb.d/'):
                require(mount['RW'] is False, 'bootstrap SQL not read-only')
        for key in ('net.ipv6.conf.all.disable_ipv6', 'net.ipv6.conf.default.disable_ipv6'):
            require(h['Sysctls'].get(key) == '1', 'IPv6 sysctl missing')
        # No raw Env, passwords, private bind paths or image configuration.
        return {'id': value['Id'], 'image_id': value['Image'], 'user': cfg['User'], 'role': role,
                'readonly': True, 'capabilities_dropped': True, 'no_new_privileges': True, 'memory': memory,
                'nano_cpus': h['NanoCpus'], 'pids_limit': h['PidsLimit'], 'restart': h['RestartPolicy']['Name'],
                'mount_destinations': sorted(non_tmp), 'tmpfs': h['Tmpfs'], 'secrets_separated': secrets_ok}

    def constraints(self):
        values = [self.constraint(self.container(self.current, role), role, self.current) for role in ('api', 'worker', 'postgres')]
        self.check('actual_apps_and_postgres_constraints', {'services': values})
        operators = []
        for role in ('migrate', 'maintenance'):
            name = self.current['config']['CUTMY_PROJECT'] + '-constraint-' + role
            cid = self.run(self.compose(self.current) + ['run', '-d', '--no-deps', '--name', name, '--entrypoint', 'python3', role,
                                                        '-c', 'import time; time.sleep(45)']).decode().strip()
            value = self.json(['docker', 'inspect', cid])[0]
            owned_resource(value, project=self.current['config']['CUTMY_PROJECT'])
            require(value['State']['Running'], 'operator inspection witness exited')
            operators.append(self.constraint(value, role, self.current))
            self.run(['docker', 'stop', '--time', '5', cid]); self.run(['docker', 'rm', cid])
        self.check('operator_constraints_and_secret_separation', {'services': operators})
        code = ('import os,json,errno; assert os.getuid()==1000 and os.getgid()==1000; '
                'assert os.stat("/tmp").st_mode&0o777==0o700; '
                'p="/tmp/acceptance-private-test"; open(p,"w").write("owned"); os.remove(p); '
                '\ntry:\n open("/acceptance-readonly-test","w").write("bad")\n'
                'except OSError as e:\n assert e.errno in (errno.EROFS,errno.EACCES)\n'
                'else:\n raise AssertionError("root writable")\n'
                'print(json.dumps({"uid":os.getuid(),"gid":os.getgid(),"tmp_private":True,"root_write_refused":True}))')
        self.check('private_tmp_and_readonly_roots', {role: self.json(self.compose(self.current) + ['exec', '-T', role, 'python3', '-c', code])
                                                   for role in ('api', 'worker')})
        self.wrapper(self.current, 'stop')
        stopped = {role: self.container(self.current, role)['State'] for role in ('api', 'worker')}
        require(all(v['Running'] is False and v['ExitCode'] == 0 and not v['OOMKilled'] for v in stopped.values()),
                'application graceful stop failed')
        self.check('graceful_stopped_services', {role: {'exit': v['ExitCode'], 'oom': v['OOMKilled']} for role, v in stopped.items()})
        self.wrapper(self.current, 'up')
        self.owned_helper_cleanup_witness()

    def owned_helper_cleanup_witness(self):
        # Use the actual immutable profile implementation. Create, never start,
        # the daemon resource that would remain after a CLI died without an ID
        # receipt; cleanup must rediscover and validate it by its exact name.
        snapshot = self.current['state'] / 'source'
        bootstrap = ('import sys,json,importlib.util; from pathlib import Path; '
                     'root=Path(sys.argv[1]); sys.path.insert(0,str(root/"deploy")); '
                     's=importlib.util.spec_from_file_location("owned_profile",root/"deploy/self-host.py"); '
                     'm=importlib.util.module_from_spec(s); s.loader.exec_module(m); ')
        create_code = bootstrap + ('source=json.loads(m.private_read(root.parent/"SOURCE.json")); '
                     'config=m.parse_config(m.private_read(Path(sys.argv[2]),8192)); '
                     'spec=m.helper_spec(config,source,sys.argv[3],"version"); '
                     'print(json.dumps({"spec":spec,"command":m.helper_command(spec)},sort_keys=True))')
        spec = self.json([sys.executable, '-B', '-c', create_code, snapshot, self.current['config_file'],
                          self.report['images']['current']['image_id']])
        command = spec['command']
        require(command[:3] == ['docker', 'run', '--rm'], 'immutable helper command protocol changed')
        identifier = self.run(['docker', 'create', *command[3:]]).decode().strip()
        require(re.fullmatch(r'[0-9a-f]{64}', identifier), 'actual partial helper identity invalid')
        value = self.json(['docker', 'inspect', identifier])[0]
        proof = self.baseline_helper(value)
        require(proof and proof['explicit_helper_identity'] and value['State']['Running'] is False
                and value['State']['Pid'] == 0, 'partial helper ownership/stopped state not proved')
        host = value['HostConfig']
        cleanup_code = bootstrap + 'm.cleanup_helper(json.loads(sys.stdin.read()))'
        self.run([sys.executable, '-B', '-c', cleanup_code, snapshot], input=canonical(spec['spec']), timeout=120)
        require(not self.run(['docker', 'ps', '-aq', '--no-trunc', '--filter', 'id=' + identifier]).strip(),
                'actual partial helper daemon removal not confirmed')
        self.check('owned_helper_partial_create_cleanup', {'proof': proof, 'exact_id_absent': True,
                   'never_started': True, 'rediscovery_after_missing_client_receipt': True,
                   'actual_limits': {'memory': host['Memory'], 'memory_swap': host['MemorySwap'],
                                     'nano_cpus': host['NanoCpus'], 'pids_limit': host['PidsLimit']}})

    def policy_faults(self):
        self.wrapper(self.current, 'stop')
        desired = self.json([sys.executable, '-B', self.current['state'] / 'source/deploy/egress-policy.py',
                             '--config', self.current['config_file'], 'plan'])
        self.policy(self.current, 'remove')
        self.wrapper(self.current, 'up', expected=1)
        require(all(not self.container(self.current, r)['State']['Running'] for r in ('api', 'worker')), 'missing policy started writers')
        self.policy(self.current, 'apply')
        forward = next(n for n in desired['chains'] if n.startswith('CMF_'))
        row = desired['chains'][forward][1]
        self.run(['iptables', '-w', '5', '-D', forward, *row])
        try:
            self.wrapper(self.current, 'up', expected=1)
            require(all(not self.container(self.current, r)['State']['Running'] for r in ('api', 'worker')), 'modified policy started writers')
        finally:
            self.run(['iptables', '-w', '5', '-I', forward, '2', *row])
        hook = desired['hooks']['DOCKER-USER'][0]
        self.run(['iptables', '-w', '5', '-D', 'DOCKER-USER', *hook])
        self.run(['iptables', '-w', '5', '-I', 'DOCKER-USER', '2', *hook])
        try:
            self.wrapper(self.current, 'up', expected=1)
            require(all(not self.container(self.current, r)['State']['Running'] for r in ('api', 'worker')), 'reordered policy started writers')
        finally:
            self.run(['iptables', '-w', '5', '-D', 'DOCKER-USER', *hook])
            self.run(['iptables', '-w', '5', '-I', 'DOCKER-USER', '1', *hook])
        self.policy(self.current, 'verify'); self.wrapper(self.current, 'up')
        self.check('missing_modified_reordered_start_refusal')

    def two_project_policy(self):
        require(self.probes(self.current) and self.probes(self.restored), 'two-project actual probes incomplete')
        # Apply requires stopped writers even when the exact policy already
        # exists. Prove idempotency under that lifecycle contract.
        self.wrapper(self.current, 'stop'); self.wrapper(self.restored, 'stop')
        before = self.run(['iptables-save', '-t', 'filter'])
        self.policy(self.current, 'apply'); self.policy(self.restored, 'apply')
        require(filter_rules(self.run(['iptables-save', '-t', 'filter'])) == filter_rules(before), 'duplicate apply altered actual filter')
        self.wrapper(self.current, 'up'); self.wrapper(self.restored, 'up')
        self.probes(self.current); self.probes(self.restored)
        for removed, retained in ((self.current, self.restored), (self.restored, self.current)):
            self.wrapper(removed, 'stop'); self.policy(removed, 'remove')
            self.policy(retained, 'verify')
            self.probes(retained); self.unrelated()
            self.policy(removed, 'apply'); self.wrapper(removed, 'up')
            self.policy(retained, 'verify'); self.policy(removed, 'verify')
        (self.output / 'filter-installed.txt').write_bytes(self.run(['iptables-save', '-t', 'filter']))
        self.check('two_project_remove_either', {'two_projects_actually_probed': True, 'duplicate_apply_exact': True})

    def lifecycle(self):
        self.wrapper(self.current, 'stop')
        suffix = digest(self.current['config']['CUTMY_PROJECT'].encode())[:10]
        import fcntl
        path = '/run/cutmy-public-' + suffix + '.lock'
        fd = os.open(path, os.O_RDWR | os.O_NOFOLLOW)
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            self.wrapper(self.old, 'up', expected=1); self.wrapper(self.current, 'up', expected=1)
            require(all(not self.container(self.current, r)['State']['Running'] for r in ('api', 'worker')),
                    'competing immutable state started under shared lock')
        finally:
            os.close(fd)
        self.check('old_new_state_shared_lock', {'old_state_source': UPGRADE_FROM, 'new_state_source': self.args.revision})
        self.policy(self.current, 'remove'); self.wrapper(self.current, 'up', expected=1)
        require(all(not self.container(self.current, r)['State']['Running'] for r in ('api', 'worker')), 'restart bypassed policy')
        self.check('stopped_restart_requires_policy')
        self.policy(self.current, 'apply'); self.wrapper(self.current, 'up')
        require(self.a.request('GET', '/api/v1/sources/' + self.source_a['id'])[0]['id'] == self.source_a['id'],
                'restart lost retained HTTPS capability')
        self.check('checked_restart_retains_https', {'daemon_or_host_reboot': False})
        for project in self.projects:
            self.wrapper(project, 'status')
        self.check('sealed_snapshots_unchanged', {p['state'].name: file_hash(p['state'] / 'SOURCE.json') for p in self.projects})

    def project_resources(self, project):
        name = project['config']['CUTMY_PROJECT']
        values = []
        for kind, command in (
            ('container', ['docker', 'ps', '-aq', '--no-trunc', '--filter', 'label=com.docker.compose.project=' + name]),
            ('network', ['docker', 'network', 'ls', '-q', '--no-trunc', '--filter', 'label=com.docker.compose.project=' + name])):
            values += [(kind, identifier) for identifier in self.run(command).decode().split()]
        names = set(self.run(['docker', 'volume', 'ls', '--format', '{{.Name}}']).decode().splitlines())
        values += [('volume', name + '_' + kind) for kind in ('media', 'postgres') if name + '_' + kind in names]
        return values

    def discovered_resources(self):
        """Discover daemon creations even if the creating CLI failed before receipt."""
        values = set()
        for project in self.projects:
            values.update(self.project_resources(project))
        for kind, command in (
            ('container', ['docker', 'ps', '-aq', '--no-trunc', '--filter', 'label=video.cutmy.acceptance=' + self.token]),
            ('network', ['docker', 'network', 'ls', '-q', '--no-trunc', '--filter', 'label=video.cutmy.acceptance=' + self.token]),
            ('volume', ['docker', 'volume', 'ls', '-q', '--filter', 'label=video.cutmy.acceptance=' + self.token])):
            values.update((kind, identifier) for identifier in self.run(command).decode().split())
        known = {identifier for kind, identifier in values if kind == 'container'}
        unknown = []
        # Baseline A has unlabelled --rm daemon helpers. A killed CLI does not
        # imply that its Docker container stopped. Inspect every newly created
        # container, but never adopt unknown traffic/resources into our scope.
        current_ids = set(self.run(['docker', 'ps', '-aq', '--no-trunc']).decode().split())
        for identifier in sorted(current_ids - self.initial_resources['container'] - known):
            value = self.json(['docker', 'inspect', identifier])[0]
            proof = self.baseline_helper(value)
            if proof:
                values.add(('container', value['Id']))
                self.recovered_helpers[value['Id']] = proof
            else:
                unknown.append(value['Id'])
        self.unknown_new_containers = unknown
        return sorted(values, key=lambda x: (('container', 'network', 'volume').index(x[0]), x[1]))

    def baseline_helper(self, value):
        """Recognize only exact image-bound helpers of our immutable profile."""
        if value.get('Id') in self.initial_resources['container']:
            return None
        image = next((record for record in self.report.get('images', {}).values()
                      if record.get('image_id') == value.get('Image')), None)
        if image is None:
            return None
        actual = self.json(['docker', 'image', 'inspect', image['image_id']])[0]
        labels = actual.get('Config', {}).get('Labels', {})
        expected = {'org.opencontainers.image.revision': image['source_commit'],
                    'org.opencontainers.image.version': image['version'],
                    'org.opencontainers.image.created': image['build_time'],
                    'org.opencontainers.image.source': 'https://github.com/Kay0k1/cutmyvideo-core'}
        if (actual.get('Id') != image['image_id'] or actual.get('Os') != 'linux'
                or actual.get('Architecture') != 'amd64' or any(labels.get(k) != v for k, v in expected.items())):
            return None
        cfg, host = value.get('Config', {}), value.get('HostConfig', {})
        if (host.get('NetworkMode') != 'none' or cfg.get('User') != '1000:1000'
                or not set(value.get('NetworkSettings', {}).get('Networks', {})) <= {'none'}
                or host.get('ReadonlyRootfs') is not True or host.get('Privileged')
                or host.get('CapDrop') != ['ALL'] or host.get('CapAdd')
                or not any(x.startswith('no-new-privileges') for x in host.get('SecurityOpt', []))):
            return None
        entry, command, mounts = cfg.get('Entrypoint'), cfg.get('Cmd'), value.get('Mounts', [])
        kind, project = None, None
        if entry == ['/usr/local/bin/cutmy'] and command == ['version'] and mounts == []:
            kind = 'image-version'
        elif entry == ['python3'] and mounts == []:
            # Exact immutable A tool command, not arbitrary Python code with a
            # familiar prefix. Parsing the retained A checkout executes nothing.
            source = ast.parse((self.old_repo / 'deploy/self-host.py').read_text())
            codes = [ast.literal_eval(node.value) for node in ast.walk(source)
                     if isinstance(node, ast.Assign) and any(isinstance(t, ast.Name) and t.id == 'tools_code' for t in node.targets)]
            if len(codes) == 1 and command == ['-c', codes[0]]:
                kind = 'image-tools'
        elif entry == ['tar'] and len(mounts) == 1:
            backup = ['-C', '/data', '--format=gnu', '-czf', '-', '.']
            restore = ['-C', '/data', '-xzf', '-', '--no-same-owner', '--no-same-permissions']
            mount = mounts[0]
            names = {p['config']['CUTMY_PROJECT'] + '_media': p['config']['CUTMY_PROJECT'] for p in self.projects}
            if (command in (backup, restore) and mount.get('Type') == 'volume' and mount.get('Name') in names
                    and mount.get('Destination') == '/data' and mount.get('RW') is (command == restore)):
                kind, project = ('media-backup' if command == backup else 'media-restore'), names[mount['Name']]
        if kind is None:
            return None
        helper_labels = cfg.get('Labels', {})
        if any(k.startswith('video.cutmy.') for k in helper_labels):
            owner = helper_labels.get('video.cutmy.project')
            purpose = {'image-version': 'version', 'image-tools': 'tools',
                       'media-backup': 'backup-tar', 'media-restore': 'restore-tar'}[kind]
            identity = helper_labels.get('video.cutmy.helper.identity', '')
            allowed = {p['config']['CUTMY_PROJECT'] for p in self.projects}
            if (owner not in allowed or helper_labels.get('video.cutmy.profile') != 'public-v1'
                    or helper_labels.get('video.cutmy.helper') != purpose
                    or not re.fullmatch(r'[0-9a-f]{32}', identity)
                    or value.get('Name', '').lstrip('/') != f'cutmy-helper-{owner}-{purpose}-{identity}'
                    or helper_labels.get('video.cutmy.helper.source') != image['source_commit']
                    or helper_labels.get('video.cutmy.helper.source-sha256') != image['source_sha256']
                    or host.get('Memory') != host.get('MemorySwap') or host.get('Memory') != 256 * 1024**2
                    or host.get('NanoCpus') != 1_000_000_000 or host.get('PidsLimit') != 32
                    or (project is not None and project != owner)):
                return None
            project = owner
        return {'id': value['Id'], 'image_id': image['image_id'], 'source_commit': image['source_commit'],
                'source_sha256': image['source_sha256'], 'operation': kind, 'project': project,
                'network': 'none', 'new_not_initial': True, 'exact_command_and_mounts': True,
                'explicit_helper_identity': helper_labels.get('video.cutmy.helper.identity')}

    def remove_resource(self, kind, identifier):
        inspect = ['docker', 'inspect', identifier] if kind == 'container' else ['docker', kind, 'inspect', identifier]
        value = self.json(inspect)[0]
        exact = value['Name'] if kind == 'volume' else value['Id']
        require(exact not in self.initial_resources[kind], 'preexisting resource cannot be cleaned')
        labels = value.get('Labels') or value.get('Config', {}).get('Labels') or {}
        helper = self.baseline_helper(value) if kind == 'container' and identifier in self.recovered_helpers else None
        if helper:
            self.recovered_helpers[value['Id']] = helper
        elif labels.get('video.cutmy.acceptance') == self.token:
            owned_resource(value, token=self.token)
        else:
            project = labels.get('com.docker.compose.project')
            allowed = {p['config']['CUTMY_PROJECT'] for p in self.projects}
            require(project in allowed, 'unknown project resource cannot be cleaned')
            if kind == 'volume':
                component = labels.get('com.docker.compose.volume')
                require(component in ('media', 'postgres'), 'unknown project volume component')
                owned_resource(value, project=project, component=component)
            else:
                owned_resource(value, project=project)
                if kind == 'container':
                    require(labels.get('com.docker.compose.service') in ('api', 'worker', 'postgres', 'migrate', 'maintenance'),
                            'unknown service cannot be cleaned')
                else:
                    require(labels.get('video.cutmy.profile') == 'public-v1' and labels.get('video.cutmy.project') == project,
                            'unknown network cannot be cleaned')
        if kind == 'container':
            if value['State']['Running']:
                self.run(['docker', 'stop', '--time', '10', exact], timeout=20)
            self.run(['docker', 'rm', exact])
        else:
            self.run(['docker', kind, 'rm', exact])

    def stop_services(self):
        # This discovery fence also catches partially completed Compose starts.
        values = [x for x in self.discovered_resources() if x[0] == 'container']
        errors = []
        for _, identifier in values:
            try:
                value = self.json(['docker', 'inspect', identifier])[0]
                require(value['Id'] not in self.initial_resources['container'], 'preexisting container cannot be stopped')
                labels = value['Config'].get('Labels', {})
                helper = self.baseline_helper(value) if identifier in self.recovered_helpers else None
                if not helper and labels.get('video.cutmy.acceptance') != self.token:
                    owned_resource(value, project=labels.get('com.docker.compose.project'))
                    require(labels.get('com.docker.compose.project') in {p['config']['CUTMY_PROJECT'] for p in self.projects}
                            and labels.get('com.docker.compose.service') in ('api', 'worker', 'migrate', 'maintenance', 'postgres'),
                            'foreign service cannot be stopped')
                if value['State']['Running']:
                    self.run(['docker', 'stop', '--time', '10', value['Id']], timeout=20)
            except Exception as error:
                errors.append(str(error))
        require(not errors, '; '.join(errors))

    def cleanup_policy(self):
        chosen = {}
        for project in self.projects:
            if (project['state'] / 'SOURCE.json').is_file():
                chosen[project['config']['CUTMY_PROJECT']] = project
        errors = []
        data = filter_rules(self.run(['iptables-save', '-t', 'filter']))
        for name, project in chosen.items():
            suffix = digest(name.encode())[:10]
            chains = {'CMF_' + suffix, 'CMD_' + suffix, 'CMH_' + suffix}
            if chains & set(data):
                try:
                    self.policy(project, 'remove')
                except Exception as error:
                    errors.append(str(error))
        require(not errors, '; '.join(errors))

    def cleanup_resources(self):
        errors, empty = [], 0
        # Require repeated successful discovery of absence, after all owned
        # command groups are stopped, instead of trusting a tracking list.
        until = time.monotonic() + 20
        while time.monotonic() < until and empty < 3:
            resources = self.discovered_resources()
            if not resources:
                empty += 1; time.sleep(.3); continue
            empty = 0
            for kind, identifier in resources:
                try:
                    self.remove_resource(kind, identifier)
                except Exception as error:
                    errors.append(kind + ': ' + str(error))
            if errors:
                break
        require(empty == 3 and not errors, 'owned resource cleanup incomplete: ' + '; '.join(errors))

    def cleanup_processes(self):
        errors = []
        for label, process, log in self.processes:
            try:
                if not getattr(process, 'acceptance_stopped', False):
                    self.stop_process(process)
            except Exception as error:
                errors.append(label + ': ' + str(error))
                if process.poll() is None:
                    try:
                        os.killpg(process.pid, signal.SIGKILL); process.wait(timeout=3)
                    except (OSError, subprocess.TimeoutExpired) as rescue:
                        errors.append(label + ' rescue: ' + str(rescue))
            finally:
                try:
                    log.close()
                    data = (self.work / (label + '.log')).read_bytes()
                    require(len(data) <= 4 * 1024 * 1024, 'owned process log bound exceeded')
                    (self.output / (label + '.log')).write_text(redact(data, self.secrets))
                except Exception as error:
                    errors.append(label + ' evidence: ' + str(error))
        require(not errors, '; '.join(errors))

    def cleanup_address(self):
        if getattr(self, 'address_add_attempted', False):
            addresses = self.json(['ip', '-j', 'address', 'show', 'dev', 'lo'])
            if any(a.get('local') == self.network_address for i in addresses for a in i.get('addr_info', [])):
                self.run(['ip', 'address', 'delete', self.network_address + '/32', 'dev', 'lo'])
            self.address_add_attempted = False

    def final_filter(self):
        if self.initial_filter is None:
            # No host mutation precedes this successfully captured snapshot.
            require(not self.projects and not self.extra_resources and not self.network_address,
                    'host mutation without initial filter snapshot')
            return
        data = self.run(['iptables-save', '-t', 'filter'])
        initial, current = filter_rules(self.initial_filter), filter_rules(data)
        for parent in ('INPUT', 'DOCKER-USER'):
            require(current.get(parent) == initial.get(parent), 'cleanup changed unrelated parent-chain rules')
        prefixes = {digest(p['config']['CUTMY_PROJECT'].encode())[:10] for p in self.projects}
        require(not any(n in current for s in prefixes for n in ('CMF_' + s, 'CMD_' + s, 'CMH_' + s)),
                'owned policy chains remain')
        (self.output / 'filter-after.txt').write_bytes(data)

    def close(self):
        self.cleaning = True
        errors = cleanup_all([
            ('services', self.stop_services), ('long processes', self.cleanup_processes),
            *[('listener ' + str(i), w.close) for i, w in enumerate(self.witnesses)],
            ('policy', self.cleanup_policy), ('resources', self.cleanup_resources),
            ('synthetic address', self.cleanup_address), ('unrelated filter', self.final_filter)])
        remaining, live = [], [label for label, p, _ in self.processes if p.poll() is None]
        try:
            remaining = self.discovered_resources()
            remaining.extend(('unknown_container', identifier) for identifier in self.unknown_new_containers)
        except Exception as error:
            errors.append('final discovery: ' + str(error)); remaining = ['discovery_unavailable']
        # Keep private bind sources when cleanup/ownership/discovery is
        # uncertain. They are never copied into the public evidence directory.
        if not errors and not remaining and not live:
            try:
                require(self.work.is_dir() and not self.work.is_symlink() and self.work.name.startswith('cutmy-self-hosting-'), 'owned private path changed')
                shutil.rmtree(self.work)
            except Exception as error:
                errors.append('private workspace cleanup: ' + str(error))
        self.report['cleanup'] = {'ok': not errors and not remaining and not live, 'errors': errors,
                                  'remaining_resources': remaining, 'live_owned_processes': live,
                                  'private_state_backup_config_keys_removed': not self.work.exists(),
                                  'private_workspace_retained': self.work.exists(),
                                  'recovered_daemon_helpers': list(self.recovered_helpers.values()),
                                  'build_cache_and_preexisting_images_not_removed': True}
        self.report['errors'].extend(redact(e, self.secrets) for e in errors)

    def execute(self):
        try:
            self.initialize_host()
            self.group('identity', self.old_identity, complete=False)
            self.group('egress', self.initial_egress, complete=False)
            self.group('https_media', self.old_media)
            self.group('backup_restore', self.backup_and_upgrade)
            self.group('roles', self.roles)
            self.group('container_constraints', self.constraints)
            self.group('egress', lambda: (self.policy_faults(), self.two_project_policy()))
            self.group('lifecycle', self.lifecycle)
        except Exception as error:
            self.report['errors'].append(redact(type(error).__name__ + ': ' + str(error), self.secrets))
        finally:
            self.close()
            self.report['ok'] = not self.report['errors'] and self.report['cleanup']['ok']
            try:
                validate_report(self.report)
            except Exception as error:
                self.report['ok'] = False
                self.report['errors'].append('final receipt: ' + str(error))
            self.save()
            # All output files are sanitized/owned, unlike private work state.
            for path in self.output.iterdir():
                require(path.is_file() and not path.is_symlink(), 'unexpected exported evidence member')
                path.chmod(0o644)
            self.output.chmod(0o755)
        return self.report['ok']


def parse_args(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--verify-report', type=Path)
    parser.add_argument('--allow-disposable-host', action='store_true')
    parser.add_argument('--revision')
    parser.add_argument('--pr-head', help='Actual PR head SHA (checkout may be its provider merge); otherwise checkout SHA')
    parser.add_argument('--upgrade-from', default=UPGRADE_FROM)
    parser.add_argument('--output', type=Path)
    parser.add_argument('--temp-directory', type=Path, default=Path(tempfile.gettempdir()))
    args = parser.parse_args(argv)
    require(args.verify_report is not None or (args.revision and args.output), 'explicit revision/output required')
    return args


def main(argv=None):
    args = parse_args(argv)
    if args.verify_report:
        validate_report(json.loads(args.verify_report.read_bytes()))
        print('Public profile acceptance: all 7 groups and cleanup PASS')
        return 0
    result = Acceptance(args).execute()
    print('Public profile acceptance ' + ('PASS' if result else 'FAIL') + ': ' + str(args.output / 'acceptance-report.json'))
    return 0 if result else 1


if __name__ == '__main__':
    try:
        raise SystemExit(main())
    except (RuntimeError, OSError, ValueError, KeyError, TypeError):
        print('Public profile acceptance refused or incomplete; inspect sanitized owned evidence', file=sys.stderr)
        raise SystemExit(1)
