#!/usr/bin/env python3
"""Prospective v5: fixed 24-run design and predeclared negative-only futility.

This cache helper is transferred and explicitly executed by the coordinator.
Authoring/importing it starts no containers, timing, native tool or production
request. A completed collection of timings is still pending content acceptance.
"""
import argparse
import datetime
import fcntl
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import statistics
import sys
import time
from types import SimpleNamespace

ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
SOURCE_SHA = '0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca'
WHEEL_SHA = '1597f89872977f3e30947609ba6f4f232e9b4724a56ebd88299b68d3c2037a95'
REQUEST_SHA = '686005efd644b86a0ddf41442458c10b223e58b5c1950c59915bdd9c3fc90ae5'
BASELINE_COMMIT = '0a5d6cd0fd7257294a39806b23af63b05e91ac88'
ORIGINAL_V4_SHA = 'f6717e36dbc34b4ef89019776f3bc65f3648bb6a3743a9bf1d032eaf82c4332d'
ORIGINAL_ORCHESTRATOR_SHA = 'b3a0a890824c6607e28e48bc01456f6b32b07ece1b8b81769b6ccb5c5a743a82'
STRATA = ('ready-fresh-process', 'ready-primed-process')
SHA = re.compile(r'[a-f0-9]{64}\Z')
SMALL_NAME = re.compile(r'[a-z0-9][a-z0-9-]{0,19}\Z')
WINDOW = re.compile(r'[a-zA-Z0-9][a-zA-Z0-9_.-]{0,79}\Z')
TIMES = ('first_downloaded_draft_seconds', 'all15_draft_seconds',
         'all15_final_seconds', 'whole30_cycle_seconds')
ROLES = ('parent', 'api', 'worker', 'pg', 'client')


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def utc():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()


def safe_path(raw):
    path = Path(raw)
    need(path.is_absolute() and path.is_relative_to(ROOT) and '..' not in path.parts,
         'outside_task_namespace')
    for ancestor in (path, *path.parents):
        if ancestor == ROOT.parent:
            break
        need(not ancestor.is_symlink(), 'symlink_input')
    return path


def raw(path, limit=4 << 20):
    path = safe_path(path)
    info = path.stat()
    need(path.is_file() and info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0
         and 0 < info.st_size <= limit, 'private_bounded_file_required')
    return path.read_bytes()


def sha(path, limit=128 << 20):
    path = safe_path(path)
    info = path.stat()
    need(path.is_file() and 0 < info.st_size <= limit, 'bounded_hash_input_required')
    value = hashlib.sha256()
    with path.open('rb') as stream:
        for chunk in iter(lambda: stream.read(64 << 10), b''):
            value.update(chunk)
    after = path.stat()
    need((info.st_dev, info.st_ino, info.st_size, info.st_mtime_ns) ==
         (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'input_changed_during_hash')
    return value.hexdigest()


def load(path):
    return json.loads(raw(path))


def save(path, payload, fresh=False):
    path = safe_path(path)
    need(path.parent.is_dir() and (not fresh or not path.exists()), 'new_private_output_required')
    part = path.with_name(path.name + '.part')
    need(not part.exists(), 'partial_output_exists')
    with part.open('x', encoding='utf-8') as stream:
        os.chmod(part, 0o600)
        json.dump(payload, stream, sort_keys=True, ensure_ascii=True, allow_nan=False, indent=2)
        stream.write('\n'); stream.flush(); os.fsync(stream.fileno())
    os.replace(part, path)
    descriptor = os.open(path.parent, os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def backend(path, expected=None):
    path = safe_path(path)
    if expected is not None:
        need(sha(path) == expected, 'backend_helper_changed')
    spec = importlib.util.spec_from_file_location('owned_backend', path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    need(module.TASK_ROOT == ROOT and module.NETWORK_INTERNAL is False, 'backend_profile_mismatch')
    return module


def image_metadata(module, reference, expected):
    module.image(reference, expected)
    data = module.docker(['image', 'inspect', '--format',
                         '{"id":{{json .Id}},"os":{{json .Os}},"arch":{{json .Architecture}},"layers":{{json .RootFS.Layers}}}',
                         reference], 'cohort_image_binding').stdout
    value = json.loads(data)
    need(value['id'] == expected and value['os'] == 'linux' and value['arch'] == 'amd64'
         and isinstance(value['layers'], list) and len(value['layers']) > 0, 'image_platform_or_layer_mismatch')
    return value


def validate_binding(value, commit, image_id):
    version = value.get('version', {})
    tools = value.get('tool_sha256', {})
    need(value.get('image_id') == image_id and version.get('commit') == commit
         and version.get('go_version') == 'go1.27.2' and version.get('arch') == 'amd64'
         and version.get('os') == 'linux' and set(tools) ==
         {'/usr/bin/ffmpeg', '/usr/bin/ffprobe', '/usr/local/bin/cutmy'}
         and all(isinstance(d, str) and SHA.fullmatch(d) for d in tools.values()), 'runtime_binding_mismatch')
    return tools


def prospective_quality(config, files):
    runner = safe_path(config['quality_runner_path'])
    inventory_path = safe_path(config['quality_inventory_path'])
    native = safe_path(config['native_orchestrator_path'])
    original = ROOT / 'inputs/controller-cohort.py'
    need(runner == ROOT / 'inputs/quality-reuse.py' and native == ROOT / 'inputs/run-cohort-oracles.py'
         and sha(original) == ORIGINAL_V4_SHA, 'original_v4_or_quality_path_changed')
    for path, expected in ((runner, config['quality_runner_sha256']),
                           (inventory_path, config['quality_inventory_sha256']),
                           (native, config['native_orchestrator_sha256'])):
        need(SHA.fullmatch(expected) and sha(path) == expected, 'prospective_quality_input_changed')
        files[str(path)] = expected
    need(config['native_orchestrator_sha256'] == ORIGINAL_ORCHESTRATOR_SHA,
         'original_native_orchestrator_changed')
    files[str(original)] = ORIGINAL_V4_SHA
    inventory = load(inventory_path)
    need(inventory.get('kind') == 'prospective_content_addressed_quality_inventory'
         and inventory.get('cohort_id') == config['cohort_id']
         and inventory.get('algorithm_sha256') == config['quality_runner_sha256']
         and inventory.get('native_orchestrator_sha256') == ORIGINAL_ORCHESTRATOR_SHA
         and inventory.get('source_sha256') == SOURCE_SHA and inventory.get('fixture_sha256') == REQUEST_SHA
         and inventory.get('baseline_runtime_image_id') == 'sha256:4e794f76aeb89fd4838f5509bc1890fc2fce7196ea189e11282bb15eedbec96d'
         and inventory.get('prospective_only_no_existing_cohort_reclassification') is True,
         'prospective_inventory_scope_changed')
    helpers = inventory.get('original_helpers', {})
    need(set(helpers) == {'run-python.py', 'verify-content.py', 'full-decode.py', 'content-oracle.py', 'compare-final.py'}
         and all(sha(ROOT / 'inputs' / name) == value == files.get(str(ROOT / 'inputs' / name))
                 for name, value in helpers.items()), 'inventory_original_helpers_changed')
    need(datetime.datetime.fromisoformat(inventory['frozen_at_utc']) <
         datetime.datetime.fromisoformat(utc()), 'inventory_must_already_be_frozen')
    return {'mode': 'content_addressed_v1', 'inventory_path': str(inventory_path),
            'inventory_sha256': config['quality_inventory_sha256'], 'runner_sha256': config['quality_runner_sha256'],
            'native_orchestrator_sha256': config['native_orchestrator_sha256']}


def freeze(args):
    config = load(args.config)
    fields = {'cohort_id', 'window_id', 'baseline_image', 'candidate_image', 'pg_image_id',
              'baseline_binding', 'candidate_binding', 'build_provenance', 'protocol_path',
              'backend_path', 'sdk_driver_path', 'client_timeout_seconds', 'disk_floor_bytes',
              'memory_available_floor_bytes', 'load1_ceiling', 'candidate_commit',
              'quality_runner_path', 'quality_runner_sha256', 'quality_inventory_path',
              'quality_inventory_sha256', 'native_orchestrator_path', 'native_orchestrator_sha256'}
    need(set(config) == fields and SMALL_NAME.fullmatch(config['cohort_id'])
         and WINDOW.fullmatch(config['window_id']) and re.fullmatch(r'[a-f0-9]{40}', config.get('candidate_commit', '')),
         'invalid_registration_config')
    need(type(config['client_timeout_seconds']) is int and 300 <= config['client_timeout_seconds'] <= 1800
         and type(config['disk_floor_bytes']) is int and config['disk_floor_bytes'] >= 768 << 20
         and type(config['memory_available_floor_bytes']) is int and config['memory_available_floor_bytes'] >= 2 << 30
         and type(config['load1_ceiling']) in (int, float) and 0 < config['load1_ceiling'] <= 6,
         'invalid_resource_registration')
    module = backend(config['backend_path'])
    first, second = load(config['baseline_binding']), load(config['candidate_binding'])
    baseline_id = module.BASELINE_IMAGE_ID
    provenance = load(config['build_provenance'])
    candidate_id = provenance.get('image_id')
    candidate_commit = config['candidate_commit']
    need(provenance.get('commit') == candidate_commit and provenance.get('image') == config['candidate_image']
         and provenance.get('baseline_runtime_base_image') == baseline_id
         and provenance.get('timed_workload_overlapped') is False
         and SHA.fullmatch(provenance.get('dockerfile_sha256', ''))
         and SHA.fullmatch(provenance.get('source_archive_sha256', '')), 'candidate_build_provenance_mismatch')
    before_tools = validate_binding(first, BASELINE_COMMIT, baseline_id)
    after_tools = validate_binding(second, candidate_commit, candidate_id)
    need(all(before_tools[p] == after_tools[p] for p in ('/usr/bin/ffmpeg', '/usr/bin/ffprobe')),
         'native_tools_changed')
    need(sha(config['sdk_driver_path']) == first.get('sdk_harness_sha256') == second.get('sdk_harness_sha256'),
         'sdk_timing_harness_mismatch')
    base = image_metadata(module, config['baseline_image'], baseline_id)
    candidate = image_metadata(module, config['candidate_image'], candidate_id)
    need(candidate['layers'][:len(base['layers'])] == base['layers'], 'candidate_not_same_runtime_base')
    module.image(module.PG_IMAGE, config['pg_image_id'])
    files = {str(safe_path(p)): sha(p) for p in
             (args.config, config['baseline_binding'], config['candidate_binding'], config['build_provenance'],
              config['protocol_path'], config['backend_path'], config['sdk_driver_path'], Path(__file__))}
    for name, digest in [('interview.mp4', SOURCE_SHA), ('requests.json', REQUEST_SHA),
                         ('cutmyvideo-1.4.0-py3-none-any.whl', WHEEL_SHA)]:
        path = ROOT / 'inputs' / name
        need(sha(path) == digest, 'frozen_fixture_or_sdk_changed')
        files[str(path)] = digest
    for name in ('verify-content.py', 'full-decode.py', 'content-oracle.py', 'compare-final.py'):
        path = ROOT / 'inputs' / name
        files[str(path)] = sha(path)
    quality = prospective_quality(config, files)
    protocol = load(config['protocol_path'])
    need(protocol.get('protocol_version') == 3 and protocol.get('environment_contract_update', {}).get('network_internal') is False,
         'registered_workflow_changed')
    production = module.production_snapshot()
    manifest = {'schema_version': 1, 'kind': 'first_draft_preregistration', 'protocol_version': 5,
                'registered_at_utc': utc(), 'config': config, 'frozen_inputs': files,
                'images': {'baseline': {'reference': config['baseline_image'], 'id': baseline_id, 'commit': BASELINE_COMMIT,
                                        'binding': first, 'layers': base['layers']},
                           'candidate': {'reference': config['candidate_image'], 'id': candidate_id, 'commit': candidate_commit,
                                         'binding': second, 'layers': candidate['layers']}},
                'limits': module.LIMITS, 'application_config': module.APP_ENV,
                'production_before_registration': production, 'network_internal': False,
                'fixtures': ['real-interview-720'], 'secondary_heavy_fixture_registered': False,
                'secondary_heavy_not_required_for_primary_scope': True,
                'pairs_per_stratum': 6, 'strata': list(STRATA), 'samples': 24,
                'prospective_quality': quality,
                'negative_only_futility': {'predeclared': True, 'after_valid_stopped_fresh_pairs': 6,
                     'after_completed_runs': 12, 'only_if_any_original_fresh_gate_fails': True,
                     'no_primed_sample_starts_on_negative': True, 'positive_early_acceptance': False,
                     'resume_replacement_pooling_allowed': False},
                'metric_gates': {'first_draft_candidate_over_baseline_median_max': .8,
                                 'first_draft_faster_pairs_min': 5,
                                 'all_drafts_final_phase_whole_cycle_ratio_median_max': 1.0,
                                 'actual_kernel_parent_api_worker_pg_client_peak_ratio_median_max': 1.0,
                                 'maximum_candidate_kernel_peak_no_greater_than_maximum_baseline': True},
                'source_clock_scope': 'Full15draft SDK invocation through first durable SHA-verified callback; continue15drafts then15finals. Ready-source setup/validation prime excluded from user clocks, included in lifetime kernel peak.',
                'cache_scope': 'Fresh processes with prepared media; no cold OS/page-cache claim. Primed stratum has exactly1 untimed full15draft validation, no encode/output reuse.',
                'quality_acceptance_pending_after_timing': True,
                'content_oracles_forbidden_during_cohort': True,
                'baseline_repeat_qualification': 'Excluded completed pilot02 plus first formal ready-fresh-process baseline; full final decode/packet-clock equality checked after timed window and before any candidate acceptance. No extra30 cycle, no changed tolerances.',
                'supplemental_pilots_excluded_from_all_pairs': True,
                'no_fault_injection_or_quota_change': True}
    save(args.output, manifest, fresh=True)
    return {'ok': True, 'action': 'freeze', 'samples': 24, 'pairs_per_stratum': 6,
            'registration_sha256': sha(args.output), 'performance_claim_ready': False}


def memory_available():
    rows = Path('/proc/meminfo').read_text().splitlines()
    return int(next(line.split()[1] for line in rows if line.startswith('MemAvailable:'))) * 1024


def host_oom_observation():
    values = dict(line.split() for line in Path('/proc/vmstat').read_text().splitlines())
    count = values.get('oom_kill')
    boot = Path('/proc/sys/kernel/random/boot_id').read_text().strip()
    need(isinstance(count, str) and count.isdecimal() and re.fullmatch(r'[a-f0-9-]{36}', boot),
         'host_oom_observation_unavailable')
    return {'boot_id': boot, 'oom_kill': int(count), 'observed_at_utc': utc()}


def check_host_oom_unchanged(before, after):
    need(before['boot_id'] == after['boot_id'], 'host_rebooted_during_sample')
    need(before['oom_kill'] == after['oom_kill'], 'host_oom_during_sample')


def preconditions(manifest, module):
    for path, digest in manifest['frozen_inputs'].items():
        need(sha(path) == digest, 'cohort_input_changed')
    for value in manifest['images'].values():
        module.image(value['reference'], value['id'])
    need(os.statvfs(ROOT).f_bavail * os.statvfs(ROOT).f_frsize >= manifest['config']['disk_floor_bytes'], 'cohort_disk_floor')
    need(memory_available() >= manifest['config']['memory_available_floor_bytes'], 'cohort_host_memory_floor')
    need(os.getloadavg()[0] <= manifest['config']['load1_ceiling'], 'cohort_host_load_ceiling')
    module.assert_production(manifest['production_before_registration'])


def memory_peak(role):
    value = role.get('kernel', {}).get('memory_peak_bytes', role.get('kernel_memory_peak_bytes'))
    need(type(value) is int and value > 0, 'actual_role_peak_missing')
    events = role.get('kernel', {}).get('memory.events')
    if events:
        pairs = dict(line.split() for line in events.splitlines())
        need(all(int(pairs.get(key, '-1')) == 0 for key in ('max', 'oom', 'oom_kill')), 'role_memory_pressure_failure')
    return value


def sample_record(receipt, resources, side, stratum, pair):
    need(receipt.get('complete') is True and receipt.get('fault_injection') is False
         and receipt.get('production_operation') is False and receipt.get('cache_stratum') == stratum
         and receipt.get('role') == side and receipt.get('source_sha256') == SOURCE_SHA
         and receipt.get('wheel_sha256') == WHEEL_SHA and receipt.get('fixture_sha256') == REQUEST_SHA
         and receipt.get('first_output_clock_includes_consumer_rehash') is True
         and receipt.get('draft_files_retained_unchanged') is True and receipt.get('draft_then_final_unique_jobs') == 30
         and receipt.get('full_fifteen_prime_count') == (0 if stratum == STRATA[0] else 1), 'invalid_timed_sample')
    need(resources.get('ok') is True and resources.get('full30_workload_verified') is True
         and all(resources.get('resources_removed', {}).values()), 'actual_database_or_cleanup_failure')
    need(resources['production_before']['roles'] == resources['production_after_cleanup']['roles'], 'production_changed_during_sample')
    times = {}
    for key in TIMES:
        value = receipt.get(key)
        need(type(value) in (int, float) and math.isfinite(value) and value > 0, 'invalid_timing_value')
        times[key] = value
    handoff = receipt.get('draft_to_final_handoff_seconds')
    need(type(handoff) in (int, float) and 0 <= handoff < 1
         and abs(times[TIMES[3]] - times[TIMES[1]] - times[TIMES[2]] - handoff) < 1e-6,
         'whole_cycle_clock_boundary_changed')
    snapshots = resources['resources_before_stop']
    peaks = {role: memory_peak(snapshots['roles'][role]) for role in ROLES[1:]}
    peaks['parent'] = max(snapshots['parent']['memory_peak_bytes'], resources['parent_after_container_stop']['memory_peak_bytes'])
    parent_events = dict(line.split() for line in resources['parent_after_container_stop']['memory.events'].splitlines())
    need(all(int(parent_events.get(k, '-1')) == 0 for k in ('max', 'oom', 'oom_kill')), 'parent_memory_pressure_failure')
    return {'side': side, 'stratum': stratum, 'pair': pair, 'timings_seconds': times, 'actual_kernel_peak_bytes': peaks,
            'all30_database_counts_passed': True, 'owned_cleanup_passed': True, 'production_unchanged': True,
            'independent_content_oracle_pending': True}


def comparisons(rows):
    reports = []
    for stratum in STRATA:
        pairs = []
        for pair in range(1, 7):
            matching = {r['side']: r for r in rows if r['stratum'] == stratum and r['pair'] == pair}
            if set(matching) != {'baseline', 'candidate'}:
                continue
            before, after = matching['baseline'], matching['candidate']
            pairs.append({'pair': pair,
                          'timing_ratios_candidate_over_baseline': {key: after['timings_seconds'][key] / before['timings_seconds'][key] for key in TIMES},
                          'kernel_peak_ratios_candidate_over_baseline': {key: after['actual_kernel_peak_bytes'][key] / before['actual_kernel_peak_bytes'][key] for key in ROLES}})
        if len(pairs) != 6:
            reports.append({'stratum': stratum, 'complete_pairs': len(pairs), 'timing_and_memory_gates_passed': False})
            continue
        timing = {k: statistics.median(p['timing_ratios_candidate_over_baseline'][k] for p in pairs) for k in TIMES}
        memory = {k: statistics.median(p['kernel_peak_ratios_candidate_over_baseline'][k] for p in pairs) for k in ROLES}
        faster = sum(p['timing_ratios_candidate_over_baseline'][TIMES[0]] < 1 for p in pairs)
        maximum_guard = {k: max(r['actual_kernel_peak_bytes'][k] for r in rows if r['stratum'] == stratum and r['side'] == 'candidate') <=
                            max(r['actual_kernel_peak_bytes'][k] for r in rows if r['stratum'] == stratum and r['side'] == 'baseline') for k in ROLES}
        passed = timing[TIMES[0]] <= .8 and faster >= 5 and all(timing[k] <= 1 for k in TIMES[1:]) and all(v <= 1 for v in memory.values()) and all(maximum_guard.values())
        reports.append({'stratum': stratum, 'complete_pairs': 6, 'pairs': pairs,
                        'timing_median_matched_ratios': timing, 'kernel_peak_median_matched_ratios': memory,
                        'timing_ratio_min_max': {k: [min(p['timing_ratios_candidate_over_baseline'][k] for p in pairs), max(p['timing_ratios_candidate_over_baseline'][k] for p in pairs)] for k in TIMES},
                        'kernel_peak_ratio_min_max': {k: [min(p['kernel_peak_ratios_candidate_over_baseline'][k] for p in pairs), max(p['kernel_peak_ratios_candidate_over_baseline'][k] for p in pairs)] for k in ROLES},
                        'faster_first_draft_pairs': faster, 'maximum_peak_no_increase': maximum_guard,
                        'timing_and_memory_gates_passed': passed})
    return reports


def public_bindings(manifest):
    return {'baseline_commit': BASELINE_COMMIT, 'candidate_commit': manifest['config']['candidate_commit'],
            'source_sha256': SOURCE_SHA, 'source_size_bytes': 82242740,
            'source_license': 'CC-BY-SA-3.0', 'source_creator': 'Wikimedia Deutschland (WMDE)',
            'request_fixture_sha256': REQUEST_SHA, 'installed_sdk_wheel_sha256': WHEEL_SHA,
            'images': {side: {'image_sha256': v['id'], 'runtime_version': v['binding']['version'],
                              'cli_sha256': v['binding']['tool_sha256']['/usr/local/bin/cutmy']} for side, v in manifest['images'].items()},
            'native_tool_sha256': {p: manifest['images']['baseline']['binding']['tool_sha256'][p] for p in ('/usr/bin/ffmpeg', '/usr/bin/ffprobe')},
            'helper_sha256': {Path(p).name: d for p, d in manifest['frozen_inputs'].items() if Path(p).suffix == '.py'},
            'limits': manifest['limits'], 'network_internal': False,
            'application_numeric_config': {k: v for k, v in manifest['application_config'].items() if k.startswith(('MAX_', 'STORAGE_', 'WORKER_', 'FFMPEG_', 'GOMEM', 'GOMAX', 'MUTATIONS')) and not k.endswith('_PATH')},
            'scope': 'Ready-source real720interview, full15draft→15final local loopback transport under registered caps; lifetime kernel peak includes boot/upload/prime. No cold OS/page-cache, upload-to-first-output, public-network/SLA/universal-codec claim.'}


def fresh_futility(rows):
    need(len(rows) == 12 and all(row.get('stratum') == STRATA[0] for row in rows),
         'futility_requires_all_twelve_fresh_runs')
    expected = [(pair, side) for pair in range(1, 7) for side in
                (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline'))]
    need([(row.get('pair'), row.get('side')) for row in rows] == expected
         and all(row.get('all30_database_counts_passed') is True and row.get('owned_cleanup_passed') is True
                 and row.get('production_unchanged') is True for row in rows),
         'futility_requires_valid_ordered_stopped_samples')
    fresh = comparisons(rows)[0]
    need(fresh['complete_pairs'] == 6, 'futility_requires_six_complete_pairs')
    return {'schema_version': 1, 'kind': 'predeclared_negative_only_fresh_futility',
            'stop_before_primed': fresh['timing_and_memory_gates_passed'] is False,
            'fresh_original_gate_results': fresh, 'completed_valid_runs': 12,
            'all_fresh_pairs_preserved': True, 'primed_samples_started': 0,
            'original_planned_runs': 24, 'full_cohort_complete': False,
            'early_positive_acceptance': False, 'resume_replacement_pooling_allowed': False,
            'independent_content_oracle_pending': True, 'performance_claim_ready': False}


def failed_owned_cleanup(module, state_path):
    # Abort only these exact positively bound task resources. Failure/media
    # evidence is retained; no production unit, volume, data or setting changes.
    if state_path is None or not state_path.exists():
        return {'owned_resources_created': False}
    data = module.own_state(state_path)
    try:
        snapshot = module.snapshot(data)
        save(state_path.parent / 'failed-sample-before-cleanup.json', snapshot, fresh=True)
    except Exception:
        save(state_path.parent / 'failed-sample-before-cleanup.json', {'partial_resource_snapshot_unavailable': True}, fresh=True)
    if data.get('client_launched'):
        module.owned_unit(data, 'client')
        module.run(['systemctl', 'stop', data['names']['client']], 'cohort_failed_owned_client_stop', timeout=20)
    # A failed client can lose its unit's live cgroup on stop. Its prior snapshot
    # remains above; this is cleanup evidence, never a valid complete workload.
    data['status'] = 'collecting_failed'
    module.save_state(state_path, data, 'cohort_failed_before_bounded_collect_stop')
    output = state_path.parent / 'failed-sample-backend-result.json'
    need(not output.exists(), 'new_failed_backend_result_required')
    module.collect_stop(SimpleNamespace(state=state_path, output=output))
    return {'owned_resources_created': True, 'cleanup_complete': True,
            'failed_sample_backend_result_sha256': sha(output)}


def run_cohort(args):
    manifest = load(args.registration)
    need(manifest.get('protocol_version') == 5 and manifest.get('samples') == 24 and manifest.get('pairs_per_stratum') == 6
         and manifest.get('strata') == list(STRATA) and args.window == manifest['config']['window_id'], 'registered_window_or_scope_mismatch')
    need(manifest.get('negative_only_futility') == {'predeclared': True, 'after_valid_stopped_fresh_pairs': 6,
         'after_completed_runs': 12, 'only_if_any_original_fresh_gate_fails': True,
         'no_primed_sample_starts_on_negative': True, 'positive_early_acceptance': False,
         'resume_replacement_pooling_allowed': False}, 'predeclared_futility_rule_changed')
    need(manifest['frozen_inputs'].get(str(Path(__file__))) == sha(Path(__file__))
         and manifest['frozen_inputs'].get(str(ROOT / 'inputs/controller-cohort.py')) == ORIGINAL_V4_SHA,
         'v5_controller_or_original_v4_binding_changed')
    need(manifest['images']['baseline']['commit'] == BASELINE_COMMIT
         and manifest['images']['candidate']['commit'] == manifest['config']['candidate_commit'],
         'generic_candidate_commit_binding_changed')
    for value in manifest['images'].values():
        validate_binding(value['binding'], value['commit'], value['id'])
    need(prospective_quality(manifest['config'], dict(manifest['frozen_inputs'])) == manifest.get('prospective_quality'),
         'registered_prospective_quality_changed')
    inventory = load(manifest['config']['quality_inventory_path'])
    need(datetime.datetime.fromisoformat(inventory['frozen_at_utc']) <
         datetime.datetime.fromisoformat(manifest['registered_at_utc']), 'inventory_not_frozen_before_registration')
    helper = manifest['config']['backend_path']
    module = backend(helper, manifest['frozen_inputs'][helper])
    directory = ROOT / 'proofs' / ('cohort-' + manifest['config']['cohort_id'])
    need(not directory.exists(), 'new_cohort_directory_required')
    directory.mkdir(mode=0o700)
    lock_path = ROOT / '.first-draft-cohort-window.lock'
    descriptor = os.open(lock_path, os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    rows, raw_records = [], []
    current_state_path = None
    with os.fdopen(descriptor, 'a') as lock:
        need(lock_path.stat().st_uid == os.geteuid() and lock_path.stat().st_mode & 0o077 == 0, 'private_coordinator_lock_required')
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Refused('another_owned_timed_window_active') from None
        save(directory / 'registration-copy.json', manifest, fresh=True)
        try:
            for stratum in STRATA:
                for pair in range(1, 7):
                    sides = ('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')
                    for side in sides:
                        preconditions(manifest, module)
                        token = 'fresh' if stratum == STRATA[0] else 'primed'
                        sample_id = f'{manifest["config"]["cohort_id"]}-{token}-p{pair:02}-{side}'
                        current_state_path = ROOT / 'samples' / sample_id / 'backend-state.json'
                        host_before = host_oom_observation()
                        selected = manifest['images'][side]
                        config = {'sample_id': sample_id, 'side': side, 'core_image': selected['reference'],
                                  'core_image_id': selected['id'], 'pg_image': module.PG_IMAGE,
                                  'pg_image_id': manifest['config']['pg_image_id'], 'protocol_sha256': sha(args.registration)}
                        config_path = directory / (sample_id + '-start.json')
                        save(config_path, config, fresh=True)
                        module.start(SimpleNamespace(config=config_path))
                        sample = ROOT / 'samples' / sample_id
                        state_path = sample / 'backend-state.json'
                        state = load(state_path)
                        input_path = sample / 'client-input.json'
                        save(input_path, {'base_url': state['base_url'], 'run_dir': str(sample / 'sdkrun'),
                                          'server_commit': selected['commit'], 'role': side,
                                          'timeout_seconds': manifest['config']['client_timeout_seconds'],
                                          'cache_stratum': stratum, 'require_client_cgroup': True, 'cleanup_source': False}, fresh=True)
                        client_path = sample / 'client-spec.json'
                        save(client_path, {'script': manifest['config']['sdk_driver_path'], 'input': str(input_path),
                                           'timeout_seconds': manifest['config']['client_timeout_seconds']}, fresh=True)
                        module.client(SimpleNamespace(state=state_path, client=client_path))
                        result_path = sample / 'backend-result.json'
                        module.collect_stop(SimpleNamespace(state=state_path, output=result_path))
                        host_after = host_oom_observation()
                        check_host_oom_unchanged(host_before, host_after)
                        receipt_path = sample / 'sdkrun/public-receipt.json'
                        receipt, resources = load(receipt_path), load(result_path)
                        need(receipt.get('expected_server_commit_from_controller') == selected['commit']
                             and receipt.get('controller_input_sha256') == sha(input_path)
                             and receipt.get('harness_sha256') == manifest['frozen_inputs'][manifest['config']['sdk_driver_path']]
                             and resources.get('core_image_id') == selected['id']
                             and resources.get('limits') == manifest['limits']
                             and resources.get('application_config') == manifest['application_config'], 'sample_source_or_harness_binding_changed')
                        record = sample_record(receipt, resources, side, stratum, pair)
                        rows.append(record)
                        raw_records.append({'sample_id': sample_id, 'receipt': str(receipt_path), 'receipt_sha256': sha(receipt_path),
                                            'backend': str(result_path), 'backend_sha256': sha(result_path), 'record': record,
                                            'host_oom_before': host_before, 'host_oom_after': host_after})
                        preconditions(manifest, module)
                        save(directory / 'private-records.json', raw_records)
                        progress = {'schema_version': 1, 'status': 'timings_running', 'completed_runs': len(rows),
                                    'rows': rows, 'paired_comparisons': comparisons(rows),
                                    'bindings': public_bindings(manifest),
                                    'independent_content_oracle_pending': True, 'performance_claim_ready': False}
                        save(directory / 'public-timing-progress.json', progress)
                        print(json.dumps({'ok': True, 'milestone': 'sample_completed', 'completed_runs': len(rows),
                                          'side': side, 'cache_stratum': stratum, 'pair': pair}, sort_keys=True), flush=True)
                if stratum == STRATA[0]:
                    decision = fresh_futility(rows)
                    if decision['stop_before_primed']:
                        decision['registration_sha256'] = sha(args.registration)
                        decision['private_records_sha256'] = sha(directory / 'private-records.json')
                        save(directory / 'NEGATIVE-FRESH-FUTILITY.json', decision, fresh=True)
                        negative = {'schema_version': 1, 'status': 'negative_futility_incomplete_cohort',
                                    'registration_sha256': sha(args.registration), 'completed_runs': 12,
                                    'planned_runs': 24, 'primed_samples_started': 0,
                                    'futility_receipt_sha256': sha(directory / 'NEGATIVE-FRESH-FUTILITY.json'),
                                    'rows': rows, 'paired_comparisons': comparisons(rows),
                                    'bindings': public_bindings(manifest),
                                    'all_failed_adverse_rows_retained': True,
                                    'native_oracles_executed_by_controller': False,
                                    'independent_content_oracle_pending': True, 'performance_claim_ready': False}
                        save(directory / 'NEGATIVE-INCOMPLETE-TIMING-RESULT.json', negative, fresh=True)
                        return {'ok': False, 'action': 'run', 'status': 'negative_futility_incomplete_cohort',
                                'completed_runs': 12, 'primed_samples_started': 0, 'performance_claim_ready': False}
        except BaseException as error:
            code = str(error) if isinstance(error, (Refused, module.Refused)) else 'cohort_operation_failed'
            save(directory / 'FAILED.json', {'schema_version': 1, 'status': 'failed_cohort_evidence_preserved',
                                            'at_utc': utc(), 'failure_code': code, 'completed_runs': len(rows),
                                            'no_rows_replaced_or_pooled': True, 'performance_claim_ready': False}, fresh=True)
            try:
                # Already collected successful samples must not be stopped twice.
                if current_state_path is not None and current_state_path.exists() and load(current_state_path).get('status') != 'collected_and_stopped':
                    cleanup = failed_owned_cleanup(module, current_state_path)
                    save(directory / 'FAILURE-OWNED-CLEANUP.json', cleanup, fresh=True)
            except Exception:
                save(directory / 'FAILURE-OWNED-CLEANUP.json', {'cleanup_complete': False, 'resource_ownership_or_partial_cleanup_failure': True, 'manual_coordinator_review_required': True}, fresh=True)
            raise
    result = {'schema_version': 1, 'status': 'all24_timings_collected_content_pending', 'completed_runs': 24,
              'registration_sha256': sha(args.registration), 'rows': rows, 'paired_comparisons': comparisons(rows),
              'bindings': public_bindings(manifest),
              'native_oracles_executed_by_controller': False, 'all_failed_adverse_rows_retained': True,
              'independent_content_oracle_pending': True, 'performance_claim_ready': False}
    save(directory / 'PUBLIC-TIMING-RESULT.json', result, fresh=True)
    return {'ok': True, 'action': 'run', 'completed_runs': 24, 'independent_content_oracle_pending': True,
            'performance_claim_ready': False}


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    actions = parser.add_subparsers(dest='action', required=True)
    first = actions.add_parser('freeze'); first.add_argument('--config', type=Path, required=True); first.add_argument('--output', type=Path, required=True)
    second = actions.add_parser('run'); second.add_argument('--registration', type=Path, required=True); second.add_argument('--window', required=True)
    args = parser.parse_args()
    try:
        result = freeze(args) if args.action == 'freeze' else run_cohort(args)
        print(json.dumps(result, sort_keys=True))
        return 2 if result.get('status') == 'negative_futility_incomplete_cohort' else 0
    except BaseException as error:
        code = str(error) if isinstance(error, Refused) else 'cohort_controller_failed'
        if not re.fullmatch(r'[a-z0-9_]{1,100}', code):code = 'cohort_controller_failed'
        print(json.dumps({'ok': False, 'code': code, 'performance_claim_ready': False}, sort_keys=True), file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
