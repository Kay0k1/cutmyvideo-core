#!/usr/bin/env python3
"""Installed-SDK draft-first benchmark. Run only in a coordinated isolated window.

No FFmpeg is launched by this driver. Progress callbacks only hash completed
files and record monotonic timestamps in memory. Private requests, IDs, cookies
and journals never reach stdout or the public receipt.
"""
from __future__ import annotations

import datetime
import hashlib
import http.client
import importlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import re
import secrets
import stat
import sys
import time
from urllib.parse import urlsplit
import zipfile

BASE = Path('/root/.cache/cutmy-first-draft')
REMOTE_BASE = Path('/root/.cache/cutmy-first-draft-20261016')
SOURCE = Path('/root/.cache/cutmy-sdk-collections/interview/interview.mp4')
FIXTURE = Path('/root/.cache/cutmy-agent-projects/real-interview-6/collections/python/requests.json')
WHEEL = Path('/root/.cache/cutmy-agent-projects/tag-38848898302-sdk/verified-bundle/cutmyvideo-1.4.0-py3-none-any.whl')
SOURCE_SHA = '0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca'
FIXTURE_SHA = '686005efd644b86a0ddf41442458c10b223e58b5c1950c59915bdd9c3fc90ae5'
WHEEL_SHA = '1597f89872977f3e30947609ba6f4f232e9b4724a56ebd88299b68d3c2037a95'
LIMIT = 256 << 20
HASH = re.compile(r'[0-9a-f]{64}\Z')
ID = re.compile(r'[A-Za-z0-9_-]{1,128}\Z')


class BenchmarkError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def require(value, code):
    if not value:
        raise BenchmarkError(code)


def regular(path, maximum, private=False):
    require(path.is_absolute() and not path.is_symlink(), 'regular_absolute_file_required')
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= maximum, 'file_budget_or_type_invalid')
    if private:
        require(info.st_uid == os.getuid() and info.st_mode & 0o077 == 0, 'private_file_required')
    return info


def file_hash(path, maximum=128 << 20):
    before = regular(path, maximum)
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(64 << 10), b''):
            digest.update(chunk)
    after = regular(path, maximum)
    require((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) ==
            (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'file_changed_while_hashing')
    return digest.hexdigest()


def read(path, private=False):
    regular(path, 4 << 20, private)
    value = json.loads(path.read_bytes())
    require(isinstance(value, dict), 'json_object_required')
    return value


def atomic(path, value):
    temporary = path.with_name('.' + path.name + '.' + secrets.token_hex(8))
    try:
        with temporary.open('x', encoding='utf-8') as stream:
            os.chmod(temporary, 0o600)
            json.dump(value, stream, ensure_ascii=True, sort_keys=True, allow_nan=False)
            stream.write('\n'); stream.flush(); os.fsync(stream.fileno())
        os.replace(temporary, path)
        descriptor = os.open(path.parent, os.O_RDONLY | getattr(os, 'O_DIRECTORY', 0))
        try: os.fsync(descriptor)
        finally: os.close(descriptor)
    finally:
        temporary.unlink(missing_ok=True)


def sdk_payload():
    module = importlib.import_module('cutmyvideo')
    package = Path(module.__file__).resolve().parent
    require(package.is_relative_to(Path(sys.prefix).resolve()) and
            importlib.metadata.version('cutmyvideo') == '1.4.0' and
            not importlib.metadata.requires('cutmyvideo'), 'installed_release_sdk_required')
    require(file_hash(WHEEL, 4 << 20) == WHEEL_SHA, 'authenticated_wheel_changed')
    members = {}
    with zipfile.ZipFile(WHEEL) as archive:
        require(len(archive.infolist()) <= 128 and sum(x.file_size for x in archive.infolist()) <= 4 << 20,
                'wheel_budget_exceeded')
        require(len({x.filename for x in archive.infolist()}) == len(archive.infolist()), 'wheel_duplicate_member')
        for row in archive.infolist():
            if row.filename.startswith('cutmyvideo/') and row.filename.endswith('.py'):
                relative = Path(row.filename).relative_to('cutmyvideo')
                require('..' not in relative.parts and not relative.is_absolute(), 'wheel_unsafe_path')
                installed = package / relative
                regular(installed, 1 << 20)
                data = archive.read(row)
                require(installed.read_bytes() == data, 'installed_sdk_differs_from_signed_wheel')
                members[row.filename] = hashlib.sha256(data).hexdigest()
    require(set(members) == {'cutmyvideo/' + p.relative_to(package).as_posix() for p in package.rglob('*.py')},
            'installed_sdk_extra_or_missing_modules')
    payload = hashlib.sha256(json.dumps(members, sort_keys=True, separators=(',', ':')).encode()).hexdigest()
    return module, payload


def input_document(path):
    value = read(path, private=True)
    require(set(value) <= {'base_url', 'run_dir', 'server_commit', 'role', 'timeout_seconds', 'cleanup_source',
                          'source_path', 'wheel_path', 'fixture_path', 'cache_stratum', 'require_client_cgroup'},
            'unknown_controller_field')
    require({'base_url', 'run_dir', 'server_commit', 'role'} <= set(value), 'controller_fields_missing')
    parsed = urlsplit(value['base_url'])
    require(parsed.scheme == 'http' and parsed.hostname in ('localhost', '127.0.0.1', '::1') and
            parsed.port is not None and parsed.path in ('', '/') and not parsed.username and
            not parsed.password and not parsed.query and not parsed.fragment, 'isolated_loopback_required')
    require(re.fullmatch('[0-9a-f]{40}', value['server_commit']) and value['role'] in ('baseline', 'candidate'),
            'controller_identity_invalid')
    root = Path(value['run_dir'])
    direct = root.parent in (BASE, BASE / 'samples', REMOTE_BASE / 'samples')
    nested = (root.parent.parent in (BASE / 'samples', REMOTE_BASE / 'samples') and
              root.name in ('sdk-run', 'sdkrun') and
              re.fullmatch('[a-z0-9][a-z0-9_-]{0,79}', root.parent.name))
    require(root.is_absolute() and (direct or nested) and
            re.fullmatch('[a-z0-9][a-z0-9_-]{0,79}', root.name),
            'new_owned_cache_directory_required')
    remote = root.is_relative_to(REMOTE_BASE)
    defaults = {'source_path': REMOTE_BASE / 'inputs/interview.mp4' if remote else SOURCE,
                'wheel_path': REMOTE_BASE / 'inputs/cutmyvideo-1.4.0-py3-none-any.whl' if remote else WHEEL,
                'fixture_path': REMOTE_BASE / 'inputs/requests.json' if remote else FIXTURE}
    for field, default in defaults.items():
        chosen = Path(value.get(field, str(default)))
        require(chosen == default, 'fixed_input_path_required')
        value[field] = str(chosen)
    timeout = value.get('timeout_seconds', 900)
    require(type(timeout) in (int, float) and math.isfinite(timeout) and 60 <= timeout <= 1800,
            'bounded_timeout_required')
    cleanup = value.get('cleanup_source', True)
    require(type(cleanup) is bool, 'cleanup_option_invalid')
    stratum = value.get('cache_stratum', 'ready-fresh-process')
    require(stratum in ('ready-fresh-process', 'ready-primed-process'), 'cache_stratum_invalid')
    value['cache_stratum'] = stratum
    require_cgroup = value.get('require_client_cgroup', remote)
    require(type(require_cgroup) is bool, 'cgroup_requirement_invalid')
    value['require_client_cgroup'] = require_cgroup
    return value, root, timeout, cleanup


def owned_run_parent(root):
    base = REMOTE_BASE if root.is_relative_to(REMOTE_BASE) else BASE
    require(root.is_relative_to(base), 'cache_root_invalid')
    directory = root.parent
    while True:
        require(not directory.is_symlink() and stat.S_ISDIR(directory.lstat().st_mode) and
                directory.stat().st_uid == os.getuid() and directory.stat().st_mode & 0o077 == 0,
                'cache_root_invalid')
        if directory == base: break
        directory = directory.parent


def raw(client, base_url, method, route, timeout):
    require(route == '/api/v1/jobs' or
            re.fullmatch('/api/v1/sources/[A-Za-z0-9_-]{1,128}', route), 'operator_route_refused')
    parsed = urlsplit(base_url)
    connection = http.client.HTTPConnection(parsed.hostname, parsed.port, timeout=min(30, timeout))
    try:
        connection.request(method, route, headers={'Accept': 'application/json', 'Origin': base_url,
                           'Cookie': 'cutmy_session=' + client.export_session_cookie()})
        response = connection.getresponse()
        data = response.read((1 << 20) + 1)
        require(len(data) <= 1 << 20, 'operator_response_budget')
        if method == 'DELETE':
            require(response.status in (204, 404), 'own_source_cleanup_failed')
            return None
        require(response.status == 200, 'owner_job_history_unavailable')
        return json.loads(data)
    finally:
        connection.close()


def client_memory_peak(required=False):
    control = Path('/proc/self/cgroup')
    if not control.is_file():
        require(not required, 'client_cgroup_peak_unavailable')
        return None
    rows = control.read_text().splitlines()
    unified = [row.split(':', 2)[2] for row in rows if row.startswith('0::')]
    parts = Path(unified[0]).parts if len(unified) == 1 else ()
    owned_parent = any(re.fullmatch(r'cutmyfirstdraft[a-z0-9]{1,80}\.slice', part) for part in parts)
    owned_client = any(re.fullmatch(r'cutmyfirstdraftclient[a-z0-9]{1,80}\.(service|scope)', part) for part in parts)
    if len(unified) != 1 or not (owned_parent and owned_client):
        require(not required, 'client_not_inside_owned_benchmark_cgroup')
        return None
    relative = Path(unified[0].lstrip('/'))
    require('..' not in relative.parts and 'cutmy.slice' not in relative.parts, 'unsafe_cgroup_path')
    peak = Path('/sys/fs/cgroup') / relative / 'memory.peak'
    if not peak.is_file():
        require(not required, 'client_cgroup_peak_unavailable')
        return None
    text = peak.read_text().strip()
    require(text.isdecimal() and len(text) <= 20, 'client_cgroup_peak_invalid')
    return int(text)


class Timing:
    def __init__(self, root):
        self.root = root
        self.started = time.perf_counter_ns()
        self.phases = []
        self.phase_summary = {}
        self.first_ns = None
        self.first_receipt = None
        self.finished_ns = None

    def progress(self, report):
        phase = report.get('phase', '')
        require(isinstance(phase, str) and re.fullmatch('[a-z_]{1,64}', phase), 'progress_phase_invalid')
        elapsed = (time.perf_counter_ns() - self.started) / 1e6
        summary = self.phase_summary.setdefault(phase, {'first_ms': elapsed, 'last_ms': elapsed, 'count': 0})
        summary['last_ms'] = elapsed; summary['count'] += 1
        if len(self.phases) < 1024:
            self.phases.append({'phase': phase, 'elapsed_ms': elapsed,
                                'completed_outputs': len(report.get('outputs', []))})
        if phase == 'output_ready' and self.first_ns is None:
            outputs = report.get('outputs', [])
            require(outputs, 'ready_callback_missing_output')
            output = outputs[0]
            self.verify(output)
            self.first_ns = time.perf_counter_ns()  # Includes independent rehash, never just first network byte.
            self.first_receipt = {k: output[k] for k in ('sha256', 'size_bytes')}

    def verify(self, output):
        path = Path(output['path'])
        require(path.is_relative_to(self.root / 'collection' / 'files') and not path.is_symlink(),
                'output_outside_owned_collection')
        require(type(output['size_bytes']) is int and 0 < output['size_bytes'] <= LIMIT and
                isinstance(output['sha256'], str) and HASH.fullmatch(output['sha256']), 'output_receipt_invalid')
        require(regular(path, LIMIT).st_size == output['size_bytes'] and
                file_hash(path, LIMIT) == output['sha256'], 'downloaded_bytes_differ')

    def finish(self, report, expected_rendition, ids):
        require(report.get('complete') is True and not report.get('pending') and
                report.get('rendition') == expected_rendition and
                [x['clip_id'] for x in report['outputs']] == ids and len(report['outputs']) == 15,
                'full_fifteen_cycle_incomplete')
        for output in report['outputs']: self.verify(output)
        ended = time.perf_counter_ns()
        self.finished_ns = ended
        require(self.first_ns is not None, 'first_persisted_output_missing')
        return {'first_verified_download_ms': (self.first_ns - self.started) / 1e6,
                'all_verified_downloads_ms': (ended - self.started) / 1e6,
                'validation_completed_ms': self.phase_summary.get('validated', {}).get('first_ms'),
                'first_submission_started_ms': self.phase_summary.get('submitting', {}).get('first_ms'),
                'first_submission_acknowledged_ms': self.phase_summary.get('submitted', {}).get('first_ms'),
                'first_output': self.first_receipt,
                'phases': self.phases, 'phase_summary': self.phase_summary,
                'outputs': [{k: o[k] for k in ('sha256', 'size_bytes')} for o in report['outputs']]}


def run(input_path):
    config, root, timeout, cleanup = input_document(input_path)
    owned_run_parent(root)
    source, fixture = Path(config['source_path']), Path(config['fixture_path'])
    global WHEEL
    WHEEL = Path(config['wheel_path'])
    root.mkdir(mode=0o700, exist_ok=False)
    require(file_hash(source) == SOURCE_SHA and source.stat().st_size == 82242740, 'frozen_source_changed')
    require(file_hash(fixture, 4 << 20) == FIXTURE_SHA, 'frozen_fifteen_plan_changed')
    plan = read(fixture)['initial']['plan']
    clips = plan['clips']; ids = [x['id'] for x in clips]
    require(len(clips) == len(set(ids)) == 15 and
            all(len(c['ranges']) == 2 and sum(r['end_ms'] - r['start_ms'] for r in c['ranges']) == 9000
                for c in clips), 'frozen_full_plan_shape_changed')
    sdk, before_payload = sdk_payload()
    client_memory_peak(config['require_client_cgroup'])  # Refuse moved client costs before timing/mutations.
    journal = {'schema_version': 1, 'base_url': config['base_url'], 'controller_input_sha256': file_hash(input_path, 4 << 20),
               'source_key': 'first-draft-source-' + secrets.token_hex(16),
               'collection_key': 'first-draft-collection-' + secrets.token_hex(16),
               'started_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'stage': 'created'}
    atomic(root / 'journal.json', journal)  # Intent identity precedes all network access.
    whole_started = time.perf_counter_ns()
    deadline = time.perf_counter() + timeout

    def remaining():
        left = deadline - time.perf_counter()
        require(left > 0, 'benchmark_deadline')
        return left

    with sdk.CutmyClient(config['base_url'], request_timeout=30, upload_timeout=600, download_timeout=120) as client:
        client.session(timeout=min(30, remaining()))
        journal['owner_cookie'] = client.export_session_cookie(); atomic(root / 'journal.json', journal)
        preparation_started = time.perf_counter_ns()
        prepared = client.prepare_source({'kind': 'upload', 'path': str(source)}, root / 'source',
                                         idempotency_key=journal['source_key'], timeout=remaining())
        preparation_ms = (time.perf_counter_ns() - preparation_started) / 1e6
        require(prepared.get('complete') is True and
                prepared.get('fingerprint') == {'kind': 'content', 'sha256': SOURCE_SHA, 'size_bytes': 82242740},
                'prepared_source_identity_differs')
        source_id = prepared['source_id']; require(ID.fullmatch(source_id), 'prepared_source_id_invalid')
        journal.update(source_id=source_id, stage='source_prepared'); atomic(root / 'journal.json', journal)
        draft_request = {'source_id': source_id, 'plan': plan, 'rendition': 'draft'}
        require('clip_ids' not in draft_request, 'subset_not_a_fifteen_baseline')
        atomic(root / 'requests.json', {'draft': draft_request, 'final': {**draft_request, 'rendition': 'final'}})
        priming_ms = 0.0
        if config['cache_stratum'] == 'ready-primed-process':
            priming_started = time.perf_counter_ns()
            primed = client.validate_collection(draft_request, timeout=min(120, remaining()))
            require(primed.get('valid') is True and primed.get('rendition') == 'draft' and
                    [row['clip_id'] for row in primed.get('clips', [])] == ids, 'full_fifteen_prime_invalid')
            priming_ms = (time.perf_counter_ns() - priming_started) / 1e6
        draft_clock = Timing(root)  # Primary clock begins immediately before the public SDK entrypoint.
        draft = client.run_collection(draft_request, root / 'collection', idempotency_key=journal['collection_key'],
                                      timeout=remaining(), poll_interval=1, admission_batch_size=3,
                                      max_output_bytes=LIMIT, on_progress=draft_clock.progress)
        draft_timing = draft_clock.finish(draft, 'draft', ids)
        atomic(root / 'draft-private-receipt.json', draft)
        journal['stage'] = 'all_drafts_downloaded'; atomic(root / 'journal.json', journal)
        final_request = {**draft_request, 'rendition': 'final'}
        final_clock = Timing(root)
        final = client.resume_collection(root / 'collection', request=final_request, timeout=remaining(),
                                         poll_interval=1, admission_batch_size=3, max_output_bytes=LIMIT,
                                         on_progress=final_clock.progress)
        final_timing = final_clock.finish(final, 'final', ids)
        whole30_seconds = (final_clock.finished_ns - draft_clock.started) / 1e9
        handoff_seconds = (final_clock.started - draft_clock.finished_ns) / 1e9
        # Prior draft revisions remain independently byte-identical after all finals complete.
        for output in draft['outputs']: final_clock.verify(output)
        whole_ms = (time.perf_counter_ns() - whole_started) / 1e6
        client_peak = client_memory_peak(config['require_client_cgroup'])  # Read while the owned client is still alive.
        atomic(root / 'final-private-receipt.json', final)
        jobs = {o['job_id'] for report in (draft, final) for o in report['outputs']}
        artifacts = {o['artifact_id'] for report in (draft, final) for o in report['outputs']}
        require(len(jobs) == len(artifacts) == 30, 'draft_final_identity_collision')
        history = raw(client, config['base_url'], 'GET', '/api/v1/jobs', remaining()).get('jobs', [])
        require(len(history) == 30 and {row['id'] for row in history} == jobs and
                all(row['source_id'] == source_id and row['status'] == 'succeeded' for row in history),
                'history_does_not_match_thirty_outputs')
        require(file_hash(source) == SOURCE_SHA and sdk_payload()[1] == before_payload, 'source_or_sdk_changed')
        journal.update(stage='timed_cycle_complete', timings={'draft': draft_timing, 'final': final_timing,
                       'source_preparation_ms': preparation_ms, 'whole_cycle_ms': whole_ms})
        atomic(root / 'journal.json', journal)
        result = {'schema_version': 1, 'record_kind': 'installed_sdk_first_download_draft_measurement',
                  'complete': True, 'role': config['role'], 'expected_server_commit_from_controller': config['server_commit'],
                  'server_runtime_identity_independently_verified_by_harness': False,
                  'sdk_version': '1.4.0', 'wheel_sha256': WHEEL_SHA, 'installed_module_payload_sha256': before_payload,
                  'harness_sha256': file_hash(Path(__file__).absolute(), 1 << 20),
                  'fixture_sha256': FIXTURE_SHA, 'source_sha256': SOURCE_SHA, 'source_size_bytes': 82242740,
                  'controller_input_sha256': journal['controller_input_sha256'], 'clips': 15, 'ranges_per_clip': 2,
                  'requested_duration_ms_per_clip': 9000, 'draft_then_final_unique_jobs': 30,
                  'owner_history_observed_jobs': len(history), 'history_endpoint_limit': 30,
                  'exact_database_count_independently_verified_by_harness': False,
                  'poll_interval_seconds': 1, 'outstanding_admission_limit': 3, 'retained_output_byte_budget': LIMIT,
                  'cache_stratum': config['cache_stratum'], 'full_fifteen_prime_count': int(priming_ms > 0),
                  'priming_excluded_from_first_download_ms': priming_ms,
                  'client_cgroup_peak_bytes_after_timed_cycle': client_peak,
                  'client_costs_in_owned_shared_cgroup': client_peak is not None,
                  'source_preparation_excluded_from_first_download': True, 'source_preparation_ms': preparation_ms,
                  'first_output_clock_includes_consumer_rehash': True, 'draft': draft_timing, 'final': final_timing,
                  'first_downloaded_draft_seconds': draft_timing['first_verified_download_ms'] / 1000,
                  'all15_draft_seconds': draft_timing['all_verified_downloads_ms'] / 1000,
                  'all15_final_seconds': final_timing['all_verified_downloads_ms'] / 1000,
                  'draft_to_final_handoff_seconds': handoff_seconds,
                  'whole30_cycle_seconds': whole30_seconds,
                  'whole30_clock_excludes_source_preparation_and_priming': True,
                  'whole_cycle_source_prepare_through_final_ms': whole_ms, 'draft_files_retained_unchanged': True,
                  'cleanup_own_source_complete': False, 'independent_content_oracle_pending': True,
                  'fault_injection': False, 'production_operation': False}
        atomic(root / 'public-receipt.json', result)  # Proof survives a later cleanup failure.
        if cleanup:
            raw(client, config['base_url'], 'DELETE', '/api/v1/sources/' + source_id, remaining())
            journal['source_deleted'] = True; journal['stage'] = 'complete'; atomic(root / 'journal.json', journal)
            result['cleanup_own_source_complete'] = True; atomic(root / 'public-receipt.json', result)
        print(json.dumps(result, ensure_ascii=True, sort_keys=True, allow_nan=False), flush=True)


def cleanup(input_path):
    config, root, timeout, _ = input_document(input_path)
    owned_run_parent(root)
    require(root.is_dir() and not root.is_symlink() and root.stat().st_mode & 0o077 == 0, 'private_run_directory_required')
    journal = read(root / 'journal.json', private=True)
    require(journal['base_url'] == config['base_url'] and
            journal['controller_input_sha256'] == file_hash(input_path, 4 << 20), 'cleanup_scope_changed')
    require(journal['stage'] in ('timed_cycle_complete', 'complete') and ID.fullmatch(journal['source_id']),
            'cleanup_requires_successful_owned_cycle')
    global WHEEL
    WHEEL = Path(config['wheel_path'])
    sdk, _ = sdk_payload()
    with sdk.CutmyClient(config['base_url'], request_timeout=30) as client:
        client.restore_session_cookie(journal['owner_cookie'])
        if not journal.get('source_deleted'):
            raw(client, config['base_url'], 'DELETE', '/api/v1/sources/' + journal['source_id'], timeout)
            journal.update(source_deleted=True, stage='complete'); atomic(root / 'journal.json', journal)
        result = read(root / 'public-receipt.json', private=True)
        result['cleanup_own_source_complete'] = True; atomic(root / 'public-receipt.json', result)
        print(json.dumps({'complete': True, 'cleanup_own_source_complete': True}, sort_keys=True))


if __name__ == '__main__':
    os.umask(0o077)
    try:
        if len(sys.argv) == 3 and sys.argv[1] == '--cleanup': cleanup(Path(sys.argv[2]).absolute())
        elif len(sys.argv) == 2: run(Path(sys.argv[1]).absolute())
        else: raise BenchmarkError('private_controller_input_required')
    except Exception as error:
        code = getattr(error, 'code', None)
        if not isinstance(code, str) or not re.fullmatch('[a-z0-9_]{1,100}', code): code = 'performance_harness_failed'
        print(json.dumps({'complete': False, 'code': code, 'exception_type': type(error).__name__}), file=sys.stderr)
        raise SystemExit(1)
