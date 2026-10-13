"""Private configuration and fixed command construction for the public profile."""
from __future__ import annotations

import hashlib
import contextlib
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import time

PROFILE = 'public-v1'
KEYS = {'CUTMY_PROJECT', 'CUTMY_DOMAIN', 'CUTMY_HTTP_PORT', 'CUTMY_DATABASE_PREFIX',
        'CUTMY_PROCESSING_PREFIX', 'CUTMY_ADMIN_PASSWORD', 'CUTMY_MIGRATION_PASSWORD',
        'CUTMY_APP_PASSWORD'}
OPTIONAL_KEYS = {'CUTMY_WORKER_PREPARATION_CONCURRENCY'}
PASSWORDS = ('CUTMY_ADMIN_PASSWORD', 'CUTMY_MIGRATION_PASSWORD', 'CUTMY_APP_PASSWORD')


class Refused(ValueError):
    """Fixed diagnostics only: commands/configuration can contain credentials."""


def require(condition, diagnostic):
    if not condition:
        raise Refused(diagnostic)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2) + '\n').encode()


def private_read(path, limit=1024 * 1024):
    path = Path(path).absolute()
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid()
                and info.st_mode & 0o077 == 0 and info.st_size <= limit,
                'private_file_permissions_invalid')
        data = stream.read(limit + 1)
        require(len(data) <= limit, 'private_file_too_large')
        return data


def private_directory(path, create=False):
    path = Path(path).absolute()
    if create:
        path.mkdir(mode=0o700, parents=False, exist_ok=False)
    info = path.lstat()
    require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid()
            and info.st_mode & 0o077 == 0 and not path.is_symlink(), 'private_directory_invalid')
    return path


def private_write(path, data):
    path = Path(path)
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, 'wb') as stream:
        stream.write(data)
        stream.flush()
        os.fsync(stream.fileno())
    fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def parse_config(data):
    require(len(data) <= 8192, 'configuration_too_large')
    values = {}
    for raw in data.decode('ascii').splitlines():
        if not raw or raw.startswith('#'):
            continue
        key, separator, value = raw.partition('=')
        require(separator and key in KEYS | OPTIONAL_KEYS and key not in values and value == value.strip(),
                'configuration_key_invalid')
        values[key] = value
    require(KEYS <= set(values), 'configuration_keys_incomplete')
    if 'CUTMY_WORKER_PREPARATION_CONCURRENCY' in values:
        require(re.fullmatch(r'[0-8]', values['CUTMY_WORKER_PREPARATION_CONCURRENCY']),
                'worker_preparation_concurrency_invalid')
    require(re.fullmatch(r'cutmy_[a-z0-9][a-z0-9_]{0,24}', values['CUTMY_PROJECT']), 'project_name_invalid')
    require(len(values['CUTMY_DOMAIN']) <= 253 and re.fullmatch(
        r'(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?',
        values['CUTMY_DOMAIN']), 'domain_invalid')
    require(re.fullmatch(r'[1-9][0-9]{3,4}', values['CUTMY_HTTP_PORT'])
            and 1024 <= int(values['CUTMY_HTTP_PORT']) <= 65535, 'loopback_port_invalid')
    networks = []
    private = [ipaddress.ip_network(n) for n in ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16')]
    for key in ('CUTMY_DATABASE_PREFIX', 'CUTMY_PROCESSING_PREFIX'):
        prefix = values[key]
        require(re.fullmatch(r'(?:[0-9]{1,3}\.){2}[0-9]{1,3}', prefix), 'network_prefix_invalid')
        try:
            network = ipaddress.ip_network(prefix + '.0/24', strict=True)
        except ValueError as exc:
            raise Refused('network_prefix_invalid') from exc
        require(str(network.network_address) == prefix + '.0'
                and any(network.subnet_of(n) for n in private), 'network_must_be_private_ipv4')
        networks.append(network)
    require(not networks[0].overlaps(networks[1]), 'networks_overlap')
    require(all(re.fullmatch(r'[A-Za-z0-9_-]{32,128}', values[key]) for key in PASSWORDS),
            'password_format_invalid')
    require(len({values[key] for key in PASSWORDS}) == 3, 'database_passwords_must_be_distinct')
    return values


def bridge_name(config):
    return 'cm_' + digest(config['CUTMY_PROJECT'].encode())[:10]


def runtime_environment(config, image):
    require(re.fullmatch(r'sha256:[0-9a-f]{64}', image), 'image_id_invalid')
    return {**config, 'CUTMY_IMAGE': image, 'CUTMY_BRIDGE': bridge_name(config),
            'CUTMY_DATABASE_BRIDGE': bridge_name(config) + '_d'}


def command_environment():
    # Inherited shell variables must not override --env-file or route Docker
    # to another host. The active local context is checked separately.
    return {key: value for key, value in os.environ.items()
            if key in ('PATH', 'HOME', 'LANG', 'LC_ALL', 'TMPDIR', 'DOCKER_CONFIG')}


@contextlib.contextmanager
def project_lock(config, root=Path('/run')):
    # Old/new immutable installation states share one project lock. A backup
    # cannot race an up from another state; firewall mutation uses the same lock.
    path = root / ('cutmy-public-' + digest(config['CUTMY_PROJECT'].encode())[:10] + '.lock')
    fd = os.open(path, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
    try:
        info = os.fstat(fd)
        require(stat.S_ISREG(info.st_mode) and info.st_uid == os.geteuid()
                and info.st_mode & 0o077 == 0, 'project_lock_invalid')
        try:
            fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError as exc:
            raise Refused('project_operation_already_running') from exc
        yield
    finally:
        os.close(fd)


@contextlib.contextmanager
def operation_signals():
    previous = {}
    def interrupted(_signum, _frame):
        raise Refused('operation_interrupted')
    try:
        for number in (signal.SIGINT, signal.SIGTERM):
            handler = signal.getsignal(number)
            try:
                signal.signal(number, interrupted)
            except ValueError as exc:
                raise Refused('operation_signal_forwarding_unavailable') from exc
            previous[number] = handler
        yield
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)


def run(command, *, input=None, stdin=None, timeout=120, output=None):
    with operation_signals():
        return _run_group(command, input=input, stdin=stdin, timeout=timeout, output=output)


def _run_group(command, *, input=None, stdin=None, timeout=120, output=None):
    """Run an owned process group with bounded diagnostics and streamed files.

    A supplied descriptor is never loaded into memory or closed here. Killing
    a Docker CLI group does not kill daemon containers; their caller must also
    verify and clean its explicitly owned daemon resources.
    """
    require(input is None or stdin is None, 'operation_input_ambiguous')
    require(timeout > 0, 'operation_timeout_invalid')
    process = None
    success = False
    try:
        with tempfile.TemporaryFile() as captured, tempfile.TemporaryFile() as errors, tempfile.TemporaryFile() as supplied:
            if input is not None:
                supplied.write(input)
                supplied.seek(0)
            process = subprocess.Popen([str(a) for a in command],
                stdin=stdin if stdin is not None else supplied if input is not None else subprocess.DEVNULL,
                stdout=output if output is not None else captured, stderr=errors,
                env=command_environment(), start_new_session=True)
            deadline = time.monotonic() + timeout
            while process.poll() is None:
                require(time.monotonic() < deadline, 'operation_unavailable_or_timed_out')
                require(errors.tell() <= 8 * 1024**2 and (output is not None or captured.tell() <= 8 * 1024**2),
                        'operation_diagnostics_too_large')
                time.sleep(.02)
            require(errors.tell() <= 8 * 1024**2 and (output is not None or captured.tell() <= 8 * 1024**2),
                    'operation_diagnostics_too_large')
            require(process.returncode == 0, 'operation_failed_see_private_service_logs')
            success = True
            if output is None:
                captured.seek(0)
                return captured.read(8 * 1024**2 + 1)
            return None
    except OSError as exc:
        raise Refused('operation_unavailable_or_timed_out') from exc
    finally:
        if process is not None and not success:
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired as exc:
                raise Refused('operation_group_cleanup_unconfirmed') from exc


def json_command(command, *, timeout=120):
    data = run(command, timeout=timeout)
    try:
        return json.loads(data)
    except (ValueError, TypeError) as exc:
        raise Refused('operation_returned_invalid_json') from exc


def local_engine():
    require(os.uname().sysname == 'Linux' and os.uname().machine == 'x86_64', 'linux_amd64_host_required')
    context = json_command(['docker', 'context', 'inspect'])[0]
    endpoint = context['Endpoints']['docker']['Host']
    require(endpoint.startswith('unix:///') and not context['Endpoints']['docker'].get('SkipTLSVerify'),
            'local_unix_docker_context_required')
    info = json_command(['docker', 'info', '--format', '{{json .}}'])
    require(info.get('OSType') == 'linux' and info.get('Architecture') in ('x86_64', 'amd64')
            and not any('rootless' in entry or 'userns' in entry for entry in info.get('SecurityOptions', [])),
            'rootful_linux_amd64_docker_required')
    return info


def compose(snapshot, env_file, config):
    return ['docker', 'compose', '--project-name', config['CUTMY_PROJECT'], '--env-file', str(env_file),
            '-f', str(Path(snapshot) / 'deploy/compose.public.yml'),
            '-f', str(Path(snapshot) / 'deploy/compose.https.example.yml')]
