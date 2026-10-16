#!/usr/bin/env python3
"""Independent metadata-only audit of ONE complete prospective v6 cohort.

No subprocess/network/media decoding/imported executable helper. Source/wheel and
frozen inputs are physically hashed; encoded outputs are bound through the
separately reviewed quality proof, not claimed as decoded again by this audit.
"""
import argparse
import datetime
import hashlib
import io
import json
import math
import os
from pathlib import Path
import re
import stat
import statistics
import zipfile

ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
SHA = re.compile(r'[a-f0-9]{64}\Z')
COMMIT = re.compile(r'[a-f0-9]{40}\Z')
BASELINE_COMMIT = '0a5d6cd0fd7257294a39806b23af63b05e91ac88'
M2_COMMIT = '5f3014be8b5f9253677d97b5f5646e6b45e1f9a0'
BASELINE_IMAGE = 'sha256:4e794f76aeb89fd4838f5509bc1890fc2fce7196ea189e11282bb15eedbec96d'
BASELINE_CLI = '73f349bc1d8169c92e6b9396059d0b2ab861b06b76450b9fb5dfdd2189896a5c'
OLD_REGISTRATION = '8f408fee2296afeba50a7aa48fc4cca2c031acbb467c746fdca4d9547d75a7e7'
OLD_NEGATIVE_WITNESS = '99020198dec72cea02aa065aa9062dac8a1a0c7c11c4ddc0d0445b4bba526668'
MEMORY_DECISION_SHA = '019f13d749a006999ea7270d144a2172f3d31c7a1f79cea12c3ce8ccb6e9881c'
SOURCE_SHA = '0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca'
SOURCE_SIZE = 82242740
FIXTURE_SHA = '686005efd644b86a0ddf41442458c10b223e58b5c1950c59915bdd9c3fc90ae5'
WHEEL_SHA = '1597f89872977f3e30947609ba6f4f232e9b4724a56ebd88299b68d3c2037a95'
SDK_PAYLOAD = 'fc00bec8481026f7e15394ab0769b5f944005226bd6ebcacc24cee731b53ea09'
PRIOR_ORACLES = {'9fadbc21f8907be9d0607d3f874324cc97340f85f8c41586044b482ca85147ac',
                 '99b6591285e033a9afbcdab0efdc8a5c63e3848f2276538ab8c7f4b92bd7d8c6'}
HELPERS = {
    'controller-cohort.py': 'f6717e36dbc34b4ef89019776f3bc65f3648bb6a3743a9bf1d032eaf82c4332d',
    'controller-cohort-v5.py': '1226acb834fd177027e1140f96d1a4edc609e61a7c68ef1c068402ad8861ca67',
    'quality-reuse.py': '92b73d276b0adfdf93c97fd985574b50d9306a45f88f32e5b7d4e0f2622ef2f1',
    'controller-cohort-v6.py': '191bad9d4881ab6064350352dfe9e0207b62259f5500f07c6c80d768a1e33006',
    'quality-reuse-v6.py': '70dd0663b9950d0e9b756474e3ab038cc7ab2005ffe054d11cac1ec84c950eeb',
    'launch-cohort.py': '32567f180745f3a378cc3b82e76711d3cde328df767df8343fde6a1bd468834e',
    'run-cohort-oracles.py': 'b3a0a890824c6607e28e48bc01456f6b32b07ece1b8b81769b6ccb5c5a743a82',
    'run-python.py': '0b9578c2d44bdc4e8f6eea41fbb86a66595524c3fb7bade978b8cf5283481f46',
    'verify-content.py': '9d47fc35f1cdba81e54fd080e49aef9ff2bf30fa558df6efa77b240a3a65437f',
    'full-decode.py': '88f5fd75724f5f3e518e2f3f21375e087e564e1694c5a08f8c77489cfaa0590a',
    'content-oracle.py': 'cb50448cc01390879778b41d73610e776a854b8e45675b895e23bc6d63a5d455',
    'compare-final.py': '4672403bb28aac0b52b1fce7dad477e87ecad5bfe8c9332e774bf0018fc7f120',
}
ORIGINAL = ('run-python.py', 'verify-content.py', 'full-decode.py', 'content-oracle.py', 'compare-final.py')
TOOLS = {'ffmpeg': '0dafc1360bb07743f76abeb1e4ae16b0aaa1331e9041a0e8d6a8524111dc0cbb',
         'ffprobe': '0e598fe6d247bfe63757f5c839dc145a166b3f876f2875ca88a306a5b8ec5a58'}
STRATA = ('ready-fresh-process', 'ready-primed-process')
SIDES = ('baseline', 'candidate')
TIMES = ('first_downloaded_draft_seconds', 'all15_draft_seconds', 'all15_final_seconds', 'whole30_cycle_seconds')
ROLES = ('parent', 'api', 'worker', 'pg', 'client')
FP_FIELDS = ('decoded_video_sha256', 'decoded_audio_sha256', 'decoded_clock_sha256', 'decoded_video_frames',
             'decoded_audio_frames', 'decoded_audio_samples_per_channel', 'packet_clocks_sha256',
             'packet_payloads_sha256', 'media_structure_sha256', 'packet_count')
COUNTS = dict(sources=1, jobs=30, draft_jobs=15, final_jobs=15, artifacts=30, render_attempts=30,
              succeeded_jobs=30, unfinished_jobs=0, failed_jobs=0, cancelled_jobs=0, reservations=0, reserved_bytes=0)
LIMITS = {'parent': {'cpu_quota': '150%', 'memory_max': '1536M', 'swap_max': '0', 'cpu_weight': '10', 'tasks_max': '512'},
          'api': {'cpus': '0.5', 'memory': '512m', 'memory_bytes': 512 << 20, 'pids': 128},
          'worker': {'cpus': '1', 'memory': '768m', 'memory_bytes': 768 << 20, 'pids': 128},
          'pg': {'cpus': '0.25', 'memory': '384m', 'memory_bytes': 384 << 20, 'pids': 128},
          'client': {'cpu_quota': '25%', 'memory_max': '256M', 'tasks_max': '64'}}
APP = {'DATA_DIR': '/data', 'LISTEN_ADDR': ':8080', 'GOMEMLIMIT': '128MiB', 'GOMAXPROCS': '2',
       'WORKER_CONCURRENCY': '1', 'WORKER_PREPARATION_CONCURRENCY': '0', 'FFMPEG_THREADS': '2', 'FFMPEG_PROFILE': 'fast',
       'MAX_SOURCE_BYTES': str(128 << 20), 'MAX_FETCH_BYTES': str(128 << 20), 'MAX_OUTPUT_BYTES': str(32 << 20),
       'MAX_STORAGE_BYTES': str(2 << 30), 'MAX_OWNER_BYTES': str(512 << 20), 'STORAGE_SAFETY_BYTES': str(64 << 20),
       'STORAGE_WAIT_TIMEOUT': '10m', 'MAX_RANGES': '32', 'MAX_ACTIVE_JOBS': '32', 'MUTATIONS_PER_MINUTE': '20',
       'MAX_RANGE_MS': '600000', 'MAX_JOB_MS': '3600000', 'JOB_TIMEOUT': '10m', 'SOURCE_TIMEOUT': '2m',
       'UPLOAD_TIMEOUT': '10m', 'SOURCE_TTL': '24h', 'ARTIFACT_TTL': '24h', 'WORKER_HEALTH_PATH': '/tmp/cutmy-worker-health',
       'COOKIE_SECURE': 'false', 'TRUST_PROXY': 'false', 'XDG_CACHE_HOME': '/tmp/cache',
       'FFMPEG_PATH': '/usr/bin/ffmpeg', 'FFPROBE_PATH': '/usr/bin/ffprobe'}
REJECTED_LINK = '../rejected-m2/README.md'


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True, allow_nan=False).encode()


def key(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def finite(value, positive=True):
    return type(value) in (int, float) and math.isfinite(value) and (value > 0 if positive else value >= 0)


def identity(value):
    return value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns, value.st_nlink


class Evidence:
    def __init__(self):
        self.reads = {}
        self.privacy = {}

    def path(self, raw):
        path = Path(raw)
        need(path.is_absolute() and path.is_relative_to(ROOT) and '..' not in path.parts, 'outside_owned_metadata')
        for part in (path, *path.parents):
            if part == ROOT.parent:
                break
            need(not part.is_symlink(), 'evidence_symlink')
        return path

    def data(self, raw, maximum=4 << 20, empty=False, private=True):
        path = self.path(raw); before = path.lstat()
        need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == os.geteuid()
             and (not private or before.st_mode & 0o077 == 0) and (empty or before.st_size > 0) and before.st_size <= maximum,
             'bounded_private_regular_evidence_required')
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as stream:
            need(identity(os.fstat(stream.fileno())) == identity(before), 'evidence_replaced_before_read')
            data = stream.read(maximum + 1)
            need(len(data) == before.st_size and len(data) <= maximum
                 and identity(os.fstat(stream.fileno())) == identity(before), 'evidence_changed_during_read')
        need(identity(path.lstat()) == identity(before), 'evidence_replaced_during_read')
        digest = hashlib.sha256(data).hexdigest()
        previous = self.reads.get(str(path))
        need(previous is None or previous == (digest, identity(before)), 'evidence_changed_between_reads')
        self.reads[str(path)] = digest, identity(before)
        self.privacy[str(path)] = self.privacy.get(str(path), False) or private
        return data, digest

    def doc(self, path):
        data, digest = self.data(path)
        return json.loads(data), digest

    def digest(self, path, maximum=128 << 20, empty=False, private=True):
        # Bounded chunk reads avoid retaining the full interview in the audit.
        path = self.path(path); before = path.lstat()
        need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and before.st_uid == os.geteuid()
             and (not private or before.st_mode & 0o077 == 0) and (empty or before.st_size > 0) and before.st_size <= maximum,
             'bounded_private_hash_input_required')
        h = hashlib.sha256(); consumed = 0
        with os.fdopen(os.open(path, os.O_RDONLY | os.O_NOFOLLOW), 'rb') as stream:
            need(identity(os.fstat(stream.fileno())) == identity(before), 'hash_input_replaced')
            for chunk in iter(lambda: stream.read(65536), b''):
                consumed += len(chunk); need(consumed <= maximum, 'growing_hash_budget'); h.update(chunk)
            need(consumed == before.st_size and identity(os.fstat(stream.fileno())) == identity(before), 'hash_input_changed')
        need(identity(path.lstat()) == identity(before), 'hash_path_changed')
        digest = h.hexdigest(); old = self.reads.get(str(path))
        need(old is None or old == (digest, identity(before)), 'evidence_changed_between_reads')
        self.reads[str(path)] = digest, identity(before)
        self.privacy[str(path)] = self.privacy.get(str(path), False) or private
        return digest

    def finish(self):
        # Re-read every metadata document; large frozen source/wheel are guarded
        # by exact before/after file identity after their full initial hashes.
        previous = list(self.reads.items())
        for raw, (digest, before) in previous:
            need(identity(self.path(raw).lstat()) == before, 'evidence_changed_before_report')
            if before[2] <= 4 << 20:
                need(self.digest(raw, 4 << 20, empty=True, private=self.privacy[raw]) == digest, 'metadata_changed_before_report')
        return key(sorted(d for d, _ in self.reads.values()))


def order():
    return [(s, p, side) for s in STRATA for p in range(1, 7)
            for side in (SIDES if p % 2 else tuple(reversed(SIDES)))]


def stamp(raw):
    value = datetime.datetime.fromisoformat(raw)
    need(value.tzinfo is not None, 'timezone_required')
    return value


def installed_sdk(e):
    wheel, digest = e.data(ROOT / 'inputs/cutmyvideo-1.4.0-py3-none-any.whl')
    need(digest == WHEEL_SHA, 'authenticated_wheel_changed')
    candidates = list(ROOT.glob('sdk/lib/python*/site-packages/cutmyvideo'))
    need(len(candidates) == 1 and candidates[0].is_dir(), 'one_installed_release_sdk_required')
    package = e.path(candidates[0]); members = {}
    with zipfile.ZipFile(io.BytesIO(wheel)) as archive:
        entries = archive.infolist()
        need(len(entries) <= 128 and sum(r.file_size for r in entries) <= 4 << 20
             and len({r.filename for r in entries}) == len(entries), 'bounded_unique_wheel_members_required')
        for row in entries:
            if row.filename.startswith('cutmyvideo/') and row.filename.endswith('.py'):
                relative = Path(row.filename).relative_to('cutmyvideo')
                need(not relative.is_absolute() and '..' not in relative.parts and row.file_size <= 1 << 20, 'wheel_module_path_or_budget')
                current, digest = e.data(package / relative, 1 << 20, private=False)
                need(current == archive.read(row), 'installed_sdk_does_not_match_signed_wheel')
                members[row.filename] = digest
    need(members and set(members) == {'cutmyvideo/' + p.relative_to(package).as_posix() for p in package.rglob('*.py')}
         and key(members) == SDK_PAYLOAD, 'installed_sdk_module_inventory_changed')
    return SDK_PAYLOAD


def registration(e, path, expected):
    m, digest = e.doc(path)
    need(SHA.fullmatch(expected) and digest == expected and digest != OLD_REGISTRATION,
         'externally_bound_new_registration_required')
    need(m.get('schema_version') == 1 and m.get('protocol_version') == 6 and m.get('kind') == 'first_draft_preregistration'
         and m.get('samples') == 24 and m.get('pairs_per_stratum') == 6 and m.get('strata') == list(STRATA),
         'complete_prospective_v6_registration_required')
    c = m['config']; need(re.fullmatch('[a-z0-9][a-z0-9-]{0,19}', c['cohort_id'])
                           and COMMIT.fullmatch(c['candidate_commit'])
                           and c['candidate_commit'] not in (BASELINE_COMMIT, M2_COMMIT), 'new_candidate_or_cohort_identity')
    need(m['limits'] == LIMITS and m['application_config'] == APP and m.get('network_internal') is False,
         'registered_limits_changed')
    need(m.get('metric_gates') == {'first_draft_candidate_over_baseline_median_max': .8,
         'first_draft_faster_pairs_min': 5, 'all_drafts_final_phase_whole_cycle_ratio_median_max': 1.0,
         'actual_kernel_common_parent_peak_ratio_median_max': .98,
         'maximum_candidate_common_parent_peak_no_greater_than_maximum_baseline': True}, 'strict_gates_changed')
    need(m.get('diagnostic_gates') == {'actual_kernel_parent_api_worker_pg_client_peak_ratio_median_max': 1.0,
         'maximum_candidate_kernel_peak_no_greater_than_maximum_baseline': True, 'acceptance_blocking': False},
         'original_role_diagnostics_changed')
    need(m.get('negative_only_futility') == {'predeclared': True, 'after_valid_stopped_fresh_pairs': 6,
         'after_completed_runs': 12, 'only_if_any_registered_v6_fresh_acceptance_gate_fails': True,
         'diagnostic_role_failures_trigger_futility': False, 'no_primed_sample_starts_on_negative': True,
         'positive_early_acceptance': False, 'resume_replacement_pooling_allowed': False}, 'futility_contract_changed')
    frozen = m['frozen_inputs']; need(isinstance(frozen, dict) and 10 <= len(frozen) <= 64, 'frozen_input_inventory_required')
    for raw, value in frozen.items():
        need(SHA.fullmatch(value) and e.digest(raw) == value, 'frozen_input_substitution')
    for name, value in HELPERS.items():
        need(frozen.get(str(ROOT / 'inputs' / name)) == value, 'reviewed_helper_identity_changed')
    for name, value in (('interview.mp4', SOURCE_SHA), ('requests.json', FIXTURE_SHA),
                        ('cutmyvideo-1.4.0-py3-none-any.whl', WHEEL_SHA)):
        need(frozen.get(str(ROOT / 'inputs' / name)) == value, 'source_fixture_sdk_not_frozen')
    need((ROOT / 'inputs/interview.mp4').stat().st_size == SOURCE_SIZE, 'full_source_size_changed')
    installed_sdk(e)
    for side, image in m['images'].items():
        need(side in SIDES and re.fullmatch('sha256:[a-f0-9]{64}', image['id'])
             and image['commit'] == (BASELINE_COMMIT if side == 'baseline' else c['candidate_commit']), 'image_source_binding')
        b = image['binding']; v = b['version']; tools = b['tool_sha256']
        need(b['image_id'] == image['id'] and v['commit'] == image['commit'] and v['os'] == 'linux'
             and v['arch'] == 'amd64' and v['go_version'] == 'go1.27.2'
             and set(tools) == {'/usr/bin/ffmpeg', '/usr/bin/ffprobe', '/usr/local/bin/cutmy'}
             and all(SHA.fullmatch(x) for x in tools.values())
             and all(tools['/usr/bin/' + n] == h for n, h in TOOLS.items())
             and b['sdk_harness_sha256'] == HELPERS['run-python.py'], 'runtime_tool_or_sdk_binding')
    need(set(m['images']) == set(SIDES) and m['images']['baseline']['id'] == BASELINE_IMAGE
         and m['images']['baseline']['binding']['tool_sha256']['/usr/local/bin/cutmy'] == BASELINE_CLI,
         'published_baseline_changed')
    base_layers = m['images']['baseline']['layers']; candidate_layers = m['images']['candidate']['layers']
    need(base_layers and candidate_layers[:len(base_layers)] == base_layers, 'candidate_runtime_base_changed')
    for side, field in (('baseline', 'baseline_binding'), ('candidate', 'candidate_binding')):
        observed, _ = e.doc(c[field]); need(observed == m['images'][side]['binding'], 'frozen_runtime_binding_changed')
    provenance, _ = e.doc(c['build_provenance'])
    need(provenance['commit'] == c['candidate_commit'] and provenance['image_id'] == m['images']['candidate']['id']
         and provenance['image'] == c['candidate_image'] and provenance['baseline_runtime_base_image'] == BASELINE_IMAGE
         and provenance['timed_workload_overlapped'] is False
         and all(SHA.fullmatch(provenance[n]) for n in ('source_archive_sha256', 'dockerfile_sha256')),
         'candidate_provenance_changed')
    mode = m['prospective_quality']
    need(mode['mode'] == 'content_addressed_v1' and mode['runner_sha256'] == HELPERS['quality-reuse-v6.py']
         and mode['native_orchestrator_sha256'] == HELPERS['run-cohort-oracles.py'], 'quality_method_changed')
    inventory, inv_sha = e.doc(mode['inventory_path'])
    need(inv_sha == mode['inventory_sha256'] == frozen.get(mode['inventory_path'])
         and inventory['kind'] == 'prospective_content_addressed_quality_inventory'
         and inventory['cohort_id'] == c['cohort_id'] and inventory['algorithm_sha256'] == HELPERS['quality-reuse-v6.py']
         and inventory['native_orchestrator_sha256'] == HELPERS['run-cohort-oracles.py']
         and inventory['original_helpers'] == {n: HELPERS[n] for n in ORIGINAL}
         and inventory['source_sha256'] == SOURCE_SHA and inventory['fixture_sha256'] == FIXTURE_SHA
         and inventory['baseline_runtime_image_id'] == BASELINE_IMAGE
         and inventory['prospective_only_no_existing_cohort_reclassification'] is True
         and stamp(inventory['frozen_at_utc']) < stamp(m['registered_at_utc']), 'prospective_inventory_changed')
    memory = m['memory_protocol_decision']; decision, decision_sha = e.doc(memory['path'])
    need(memory['path'] == c['memory_protocol_decision_path']
         and e.path(memory['path']) == ROOT / 'MEMORY-PROTOCOL-V6-DECISION.json'
         and decision_sha == memory['sha256'] == c['memory_protocol_decision_sha256'] == MEMORY_DECISION_SHA
         and frozen.get(memory['path']) == decision_sha and decision['kind'] == 'prospective_post_m2_memory_protocol_correction'
         and memory['decided_at_utc'] == decision['decided_at_utc']
         and stamp(memory['decided_at_utc']) < stamp(m['registered_at_utc'])
         and memory['original_registration_sha256'] == decision['original_registration_sha256'] == OLD_REGISTRATION
         and memory['original_negative_witness_sha256'] == decision['original_negative_witness_sha256'] == OLD_NEGATIVE_WITNESS
         and decision['original_m2_rejection_unchanged'] is True and decision['data_informed_post_m2_change_explicit'] is True
         and decision['new_cohort_must_be_separately_frozen_before_first_measurement'] is True
         and decision['no_acceptance_or_reclassification_of_old_or_incomplete_results'] is True
         and decision['common_parent_actual_kernel_memory_peak_ratio_median_max'] == .98
         and decision['common_parent_maximum_candidate_peak_le_maximum_baseline'] is True,
         'prospective_memory_decision_changed')
    need(m.get('coordinator_instrumentation') == {'kind': 'detached_owned_systemd_coordinator',
         'outside_measured_parents': True, 'memory_max_bytes': 128 << 20, 'memory_swap_max_bytes': 0,
         'cpu_quota_percent': 10, 'tasks_max': 64, 'runtime_max_seconds': 14400, 'remain_after_exit': True,
         'stdout_file_max_bytes': 8 << 20, 'launcher_sha256': HELPERS['launch-cohort.py']}
         and e.path(c['coordinator_launcher_path']) == ROOT / 'inputs/launch-cohort.py'
         and c['coordinator_launcher_sha256'] == HELPERS['launch-cohort.py'], 'coordinator_instrumentation_changed')
    need(set(m['production_before_registration']['roles']) == {'postgres', 'api', 'worker', 'web', 'edge'}
         and all(v['healthy'] is True for v in m['production_before_registration']['roles'].values()), 'healthy_five_role_baseline_required')
    plan, _ = e.doc(ROOT / 'inputs/requests.json'); plan = plan['initial']['plan']
    need(plan.get('version') == 1 and len(plan['clips']) == 15 and len({x['id'] for x in plan['clips']}) == 15,
         'fifteen_clip_plan_required')
    return m, digest, inventory, plan


def intent(plan, clip, rendition):
    opts = {k: v for k, v in plan.get('options', {}).items() if v not in (None, '')}
    opts.update({k: v for k, v in clip.get('options', {}).items() if v not in (None, '')})
    for k, v in dict(format='mp4', quality='best', cut_mode='accurate', subtitle_mode='drop', no_audio=False, audio_track=-1).items():
        opts.setdefault(k, v)
    if opts.pop('clear_crop', False): opts.pop('crop', None)
    if rendition == 'draft': opts['quality'] = '720p'
    return {'version': 1, 'source_sha256': SOURCE_SHA,
            'ranges': [{k: r[k] for k in ('start_ms', 'end_ms')} for r in clip['ranges']],
            'effective_options': opts, 'rendition': rendition, 'output_mode': 'joined'}


def maps(output, clip):
    rows = output.get('segments'); need(isinstance(rows, list) and len(rows) == len(clip['ranges']) == 2, 'current_two_segment_maps_required')
    cursor = 0
    for index, (actual, wanted) in enumerate(zip(rows, clip['ranges'])):
        need(set(actual) == {'index', 'label', 'source_start_ms', 'source_end_ms', 'output_start_ms', 'output_end_ms'}
             and actual['index'] == index and actual['label'] == wanted.get('label', '')
             and all(type(actual[n]) is int for n in ('source_start_ms', 'source_end_ms', 'output_start_ms', 'output_end_ms'))
             and actual['source_start_ms'] == wanted['start_ms'] and abs(actual['source_end_ms'] - wanted['end_ms']) <= 100
             and actual['output_start_ms'] == cursor
             and abs(actual['output_end_ms'] - cursor - wanted['end_ms'] + wanted['start_ms']) <= 100,
             'current_source_order_or_timeline_changed')
        cursor = actual['output_end_ms']
    tracks = output.get('track_manifest')
    need(isinstance(tracks, list) and 1 <= len(tracks) <= 32 and len({r['index'] for r in tracks}) == len(tracks)
         and sum(r.get('included') is True and r['type'] == 'video' for r in tracks) == 1
         and sum(r.get('included') is True and r['type'] == 'audio' for r in tracks) == 1
         and not any(r.get('included') is True and r['type'] not in ('video', 'audio') for r in tracks), 'current_track_selection_changed')
    return tracks


def sdk_scope(e, input_path, plan):
    c, input_sha = e.doc(input_path); directory = e.path(c['run_dir'])
    need(Path(input_path).name == 'client-input.json' and directory == Path(input_path).parent / 'sdkrun'
         and directory.parent.parent == ROOT / 'samples', 'sample_directory_scope')
    public, receipt_sha = e.doc(directory / 'public-receipt.json')
    need(public.get('schema_version') == 1 and public.get('record_kind') == 'installed_sdk_first_download_draft_measurement'
         and public.get('complete') is True and public['controller_input_sha256'] == input_sha
         and public['harness_sha256'] == HELPERS['run-python.py'] and public['source_sha256'] == SOURCE_SHA
         and public['fixture_sha256'] == FIXTURE_SHA and public['wheel_sha256'] == WHEEL_SHA
         and public['installed_module_payload_sha256'] == SDK_PAYLOAD
         and public['owner_history_observed_jobs'] == public['draft_then_final_unique_jobs'] == 30
         and public['role'] == c['role'] and public['expected_server_commit_from_controller'] == c['server_commit']
         and public['cache_stratum'] == c.get('cache_stratum', STRATA[0]), 'raw_sdk_binding_changed')
    need(public.get('sdk_version') == '1.4.0' and public.get('source_size_bytes') == SOURCE_SIZE
         and public.get('clips') == 15 and public.get('ranges_per_clip') == 2
         and public.get('requested_duration_ms_per_clip') == 9000
         and c.get('require_client_cgroup', True) is True and c.get('cleanup_source', False) is False,
         'complete_installed_sdk_workload_required')
    for field, name in (('source_path', 'interview.mp4'), ('wheel_path', 'cutmyvideo-1.4.0-py3-none-any.whl'),
                        ('fixture_path', 'requests.json')):
        need(field not in c or e.path(c[field]) == ROOT / 'inputs' / name, 'sdk_fixture_path_changed')
    requests, _ = e.doc(directory / 'requests.json'); rows = []; resources = {'job_id': set(), 'artifact_id': set()}; source = None
    for rendition in ('draft', 'final'):
        report, _ = e.doc(directory / (rendition + '-private-receipt.json')); request = requests[rendition]
        need(set(request) <= {'source_id', 'plan', 'rendition', 'clip_ids'} and request['plan'] == plan
             and request['rendition'] == rendition and not request.get('clip_ids')
             and report['complete'] is True and not report.get('pending') and report['rendition'] == rendition
             and report['source_id'] == request['source_id'], 'current_private_source_or_plan_changed')
        source = source or report['source_id']; need(isinstance(source, str) and source and report['source_id'] == source, 'cross_source_private_report')
        outputs = report['outputs']; need([o['clip_id'] for o in outputs] == [c['id'] for c in plan['clips']]
                                         and len(public[rendition]['outputs']) == 15, 'current_clip_order_changed')
        for ordinal, (out, clip, bounded) in enumerate(zip(outputs, plan['clips'], public[rendition]['outputs'])):
            need(out.get('rendition', rendition) == rendition and e.path(out['path']).parent == directory / 'collection/files'
                 and SHA.fullmatch(out['sha256']) and type(out['size_bytes']) is int and 0 < out['size_bytes'] <= 32 << 20
                 and all(out[n] == bounded[n] for n in ('sha256', 'size_bytes')), 'current_private_output_changed')
            for field in resources:
                need(isinstance(out[field], str) and out[field] and out[field] not in resources[field], 'duplicate_current_job_or_artifact')
                resources[field].add(out[field])
            tracks = maps(out, clip)
            sem = intent(plan, clip, rendition)
            if sem['effective_options']['audio_track'] >= 0:
                need([r['index'] for r in tracks if r.get('included') and r['type'] == 'audio'] == [sem['effective_options']['audio_track']], 'current_explicit_audio_changed')
            rows.append({'output': out, 'intent': sem, 'tracks': tracks, 'rendition': rendition, 'ordinal': ordinal})
    documents = [input_path, directory / 'public-receipt.json', directory / 'requests.json',
                 directory / 'draft-private-receipt.json', directory / 'final-private-receipt.json']
    return {'config': c, 'directory': directory, 'public': public, 'timed_sha256': receipt_sha, 'rows': rows,
            'bound_document_digests': [e.digest(p) for p in documents]}


def events(kernel):
    values = dict(line.split() for line in kernel['memory.events'].splitlines())
    need(all(values.get(k) == '0' for k in ('max', 'oom', 'oom_kill')), 'kernel_memory_pressure')


def kernel_peak(role):
    if 'kernel' in role:
        events(role['kernel']); value = role['kernel']['memory_peak_bytes']
    else:
        value = role.get('kernel_memory_peak_bytes')
        unit = role.get('unit', {})
        need(unit.get('MemoryPeak') == str(value) and unit.get('ExecMainStatus') == '0'
             and unit.get('Result') == 'success' and unit.get('SubState') == 'exited', 'retained_client_kernel_peak_required')
    need(type(value) is int and value > 0, 'actual_kernel_peak_required')
    return value


def backend_scope(m, b, r, side, stratum, registration_sha):
    need(b.get('schema_version') == 1 and b.get('kind') == 'first_draft_backend_resource_receipt'
         and b.get('ok') is True and b.get('status') == 'collected_and_stopped' and b.get('full30_workload_verified') is True
         and b.get('resources_removed') == {k: True for k in ('containers', 'network', 'anchor', 'slice')}, 'stopped_backend_required')
    need(all(type(b['database_counts'].get(k)) is int and b['database_counts'][k] == v for k, v in COUNTS.items()), 'actual_thirty_database_counts')
    need(b['limits'] == LIMITS and b['application_config'] == APP and b['core_image_id'] == m['images'][side]['id']
         and b['pg_image_id'] == m['config']['pg_image_id'] and b['protocol_sha256'] == registration_sha
         and b['network_internal'] is False, 'backend_image_config_caps_binding')
    for field in ('production_before', 'production_before_cleanup', 'production_after_cleanup'):
        need(b[field]['roles'] == m['production_before_registration']['roles'], 'production_changed')
    snap = b['resources_before_stop']; parent = snap['parent']; after = b['parent_after_container_stop']
    for p in (parent, after):
        events(p)
        need(all(p.get(k) == v for k, v in {'cpu.max': '150000 100000', 'memory.max': '1610612736',
             'memory.swap.max': '0', 'cpu.weight': '10', 'pids.max': '512'}.items()), 'actual_parent_caps_changed')
    peaks = {'parent': max(parent['memory_peak_bytes'], after['memory_peak_bytes'])}
    need(all(type(v['memory_peak_bytes']) is int and v['memory_peak_bytes'] > 0 for v in (parent, after)),
         'actual_parent_kernel_peak_required')
    for role, cpu in (('api', '50000 100000'), ('worker', '100000 100000'), ('pg', '25000 100000')):
        x = snap['roles'][role]; k = x['kernel']; need(x.get('healthy') is True, 'role_not_healthy')
        need(k.get('cpu.max') == cpu and k.get('memory.max') == str(LIMITS[role]['memory_bytes'])
             and k.get('memory.swap.max') == '0' and k.get('pids.max') == '128', 'actual_server_caps_changed')
        peaks[role] = kernel_peak(x)
    peaks['client'] = kernel_peak(snap['roles']['client'])
    if 'kernel' in snap['roles']['client']:
        ck = snap['roles']['client']['kernel']
        need(ck['cpu.max'] == '25000 100000' and ck['memory.max'] == str(256 << 20)
             and ck['memory.swap.max'] == '0' and ck['pids.max'] == '64', 'actual_live_client_caps_changed')
    need(r['client_costs_in_owned_shared_cgroup'] is True and type(r['client_cgroup_peak_bytes_after_timed_cycle']) is int
         and 0 < r['client_cgroup_peak_bytes_after_timed_cycle'] <= peaks['client'], 'live_client_kernel_peak_not_bound')
    need(r['fault_injection'] is False and r['production_operation'] is False and r['cache_stratum'] == stratum
         and r['source_preparation_excluded_from_first_download'] is True
         and r['whole30_clock_excludes_source_preparation_and_priming'] is True
         and r['first_output_clock_includes_consumer_rehash'] is True and r['draft_files_retained_unchanged'] is True
         and r['full_fifteen_prime_count'] == int(stratum == STRATA[1])
         and r['poll_interval_seconds'] == 1 and r['outstanding_admission_limit'] == 3
         and r['retained_output_byte_budget'] == 256 << 20, 'raw_timed_workload_boundary_changed')
    times = {n: r[n] for n in TIMES}; need(all(finite(v) for v in times.values()), 'finite_positive_clock_required')
    need(times[TIMES[0]] == r['draft']['first_verified_download_ms'] / 1000
         and times[TIMES[1]] == r['draft']['all_verified_downloads_ms'] / 1000
         and times[TIMES[2]] == r['final']['all_verified_downloads_ms'] / 1000
         and r['draft']['first_output'] in r['draft']['outputs']
         and r['final']['first_output'] in r['final']['outputs'], 'sdk_callback_or_phase_clock_changed')
    need(times[TIMES[0]] <= times[TIMES[1]] and finite(r['draft_to_final_handoff_seconds'], False)
         and r['draft_to_final_handoff_seconds'] < 1
         and abs(times[TIMES[3]] - times[TIMES[1]] - times[TIMES[2]] - r['draft_to_final_handoff_seconds']) < 1e-6,
         'whole30_clock_boundary_changed')
    return times, peaks


def full_fingerprint(row):
    fp = {k: row[k] for k in (*FP_FIELDS, 'structure')}
    need(all(SHA.fullmatch(fp[k]) if k.endswith('_sha256') else type(fp[k]) is int and fp[k] > 0 for k in FP_FIELDS)
         and key(fp['structure']) == fp['media_structure_sha256'], 'complete_native_fingerprint_changed')
    return fp


def environment(tools):
    need(set(tools) == set(TOOLS) and all(v['executable_sha256'] == TOOLS[k] and SHA.fullmatch(v['version_stdout_sha256'])
                                       for k, v in tools.items()), 'native_tool_environment_changed')
    return {'baseline_image_id': BASELINE_IMAGE, 'tool_binding': tools, 'original_helpers': {n: HELPERS[n] for n in ORIGINAL}}


def certificate_keys(out, sem, env):
    decode = key({'encoded_sha256': out['sha256'], 'size_bytes': out['size_bytes'], 'environment': env})
    return decode, key({'decode_key': decode, 'source_semantics': sem})


def native_certificates(e, sample, oracle_sha):
    oracle, sha = e.doc(sample['directory'] / 'content-oracle-receipt.json')
    private, private_sha = e.doc(sample['directory'] / 'full-decoded-fingerprints.json')
    need(sha == oracle_sha and oracle.get('schema_version') == private.get('schema_version') == 1
         and oracle.get('record_kind') == 'independent_fifteen_draft_then_final_content_oracle'
         and oracle['passed'] is True and private['complete'] is True
         and oracle['private_fingerprints_sha256'] == private_sha
         and oracle['timed_receipt_sha256'] == private['timed_receipt_sha256'] == sample['timed_sha256']
         and oracle['source_sha256'] == private['source_sha256'] == SOURCE_SHA
         and oracle['fixture_sha256'] == private['fixture_sha256'] == FIXTURE_SHA
         and oracle['full_video_audio_decoded_outputs'] == 30
         and oracle['verifier_sha256'] == HELPERS['verify-content.py']
         and oracle['full_decode_helper_sha256'] == HELPERS['full-decode.py']
         and oracle['frozen_comparator_sha256'] == HELPERS['content-oracle.py']
         and oracle['frame_mae_limit'] == 12.0 and oracle['audio_correlation_minimum'] == .75
         and oracle['source_reference_times_come_from_frozen_caller_ranges'] is True
         and oracle['sdk_timing_completed_before_decode'] is True, 'complete_native_source_qualification_required')
    need(oracle['tool_binding'] == private['tool_binding'], 'native_tool_binding_mismatch'); env = environment(oracle['tool_binding'])
    need([s['rendition'] for s in oracle['stages']] == ['draft', 'final']
         and all((s['clips'], s['segments'], s['frame_samples']) == (15, 30, 60)
                 and finite(s['max_frame_mae'], False) and s['max_frame_mae'] <= 12
                 and type(s['audio_samples']) is int and type(s['silent_segments']) is int
                 and s['audio_samples'] >= 0 and s['silent_segments'] >= 0 and s['audio_samples'] + s['silent_segments'] == 30
                 and (s['audio_samples'] == 0 or finite(s['min_audio_correlation'], False) and .75 <= s['min_audio_correlation'] <= 1.000001)
                 for s in oracle['stages']), 'native_source_threshold_or_counts_changed')
    decoded = private['outputs']; need(len(decoded) == 30, 'thirty_native_fingerprints_required')
    need(oracle['decoded_content_aggregate_sha256'] == key([{'rendition': v['rendition'], 'ordinal': i % 15,
         **{n: v[n] for n in FP_FIELDS}} for i, v in enumerate(decoded)]), 'native_aggregate_changed')
    certificates = []
    for row, fp in zip(sample['rows'], decoded):
        out = row['output']; need(fp['path'] == out['path'] and fp['clip_id'] == out['clip_id']
             and fp['rendition'] == row['rendition'] and fp['encoded_file_sha256'] == out['sha256']
             and fp['encoded_size_bytes'] == out['size_bytes'], 'native_encoded_output_binding')
        decode, source = certificate_keys(out, row['intent'], env)
        certificates.append({'decode_key': decode, 'source_key': source, 'encoded_sha256': out['sha256'],
             'size_bytes': out['size_bytes'], 'intent': row['intent'], 'tracks': row['tracks'], 'fingerprint': full_fingerprint(fp),
             'oracle_sha256': sha, 'private_fingerprints_sha256': private_sha, 'environment': env})
    return certificates, private


def insert(cache, certificates, origin):
    for c in certificates:
        if c['source_key'] in cache:
            old = cache[c['source_key']]
            need(all(old[n] == c[n] for n in ('decode_key', 'intent', 'tracks', 'fingerprint', 'environment')), 'conflicting_certificate')
        else:
            cache[c['source_key']] = {**c, 'origin': origin}


def compare(left, right):
    need(left['tool_binding'] == right['tool_binding'], 'paired_tool_difference')
    a = [r for r in left['outputs'] if r['rendition'] == 'final']; b = [r for r in right['outputs'] if r['rendition'] == 'final']
    need(len(a) == len(b) == 15 and [r['clip_id'] for r in a] == [r['clip_id'] for r in b], 'paired_fifteen_finals_required')
    need(all(all(x[n] == y[n] for n in FP_FIELDS) for x, y in zip(a, b)), 'paired_final_media_or_clock_regression')
    return {'final_clips': 15, 'exact_decoded_video_audio_equal': True, 'exact_packet_clocks_payloads_equal': True,
            'geometry_and_codec_metadata_equal': True,
            'encoded_file_hashes_equal': sum(x['encoded_file_sha256'] == y['encoded_file_sha256'] for x, y in zip(a, b)),
            'decoded_final_aggregate_sha256': key([{n: r[n] for n in FP_FIELDS} for r in a])}


def distribution(values):
    q = statistics.quantiles(values, n=4, method='inclusive')
    return {'median': statistics.median(values), 'minimum': min(values), 'maximum': max(values),
            'inclusive_first_quartile': q[0], 'inclusive_third_quartile': q[2], 'interquartile_range': q[2] - q[0]}


def metrics(rows):
    result = []
    for s in STRATA:
        pairs = []
        for p in range(1, 7):
            found = {r['side']: r for r in rows if r['stratum'] == s and r['pair'] == p}
            need(set(found) == set(SIDES), 'complete_pairs_required')
            b, c = found['baseline'], found['candidate']
            pairs.append({'pair': p, 'timing_ratios': {n: c['timings_seconds'][n] / b['timings_seconds'][n] for n in TIMES},
                          'kernel_peak_ratios': {n: c['actual_kernel_peak_bytes'][n] / b['actual_kernel_peak_bytes'][n] for n in ROLES}})
        t = {n: distribution([p['timing_ratios'][n] for p in pairs]) for n in TIMES}
        ram = {n: distribution([p['kernel_peak_ratios'][n] for p in pairs]) for n in ROLES}
        maxima = {n: {side: max(r['actual_kernel_peak_bytes'][n] for r in rows if r['side'] == side and r['stratum'] == s)
                      for side in SIDES} for n in ROLES}
        faster = sum(p['timing_ratios'][TIMES[0]] < 1 for p in pairs)
        original_gates = {'first_draft_median_at_least20_percent_faster': t[TIMES[0]]['median'] <= .8,
                 'first_draft_faster_in_at_least5_pairs': faster >= 5,
                 **{n + '_median_no_increase': t[n]['median'] <= 1 for n in TIMES[1:]},
                 **{n + '_kernel_peak_median_no_increase': ram[n]['median'] <= 1 for n in ROLES},
                 **{n + '_kernel_peak_maximum_no_increase': maxima[n]['candidate'] <= maxima[n]['baseline'] for n in ROLES}}
        gates = {k: v for k, v in original_gates.items() if '_kernel_peak_' not in k}
        gates['common_parent_kernel_peak_median_at_least2_percent_lower'] = ram['parent']['median'] <= .98
        gates['common_parent_kernel_peak_maximum_no_increase'] = maxima['parent']['candidate'] <= maxima['parent']['baseline']
        result.append({'stratum': s, 'complete_pairs': 6, 'pairs': pairs, 'timing_ratio_dispersion': t,
             'kernel_peak_ratio_dispersion': ram, 'kernel_peak_maxima_bytes': maxima, 'faster_first_draft_pairs': faster,
             'individual_gates': gates, 'timing_and_memory_gates_passed': all(gates.values()),
             'diagnostic_original_all5_zero_increase_gates': {k: v for k, v in original_gates.items() if '_kernel_peak_' in k},
             'diagnostic_original_all5_zero_increase_passed': all(v for k, v in original_gates.items() if '_kernel_peak_' in k),
             'diagnostic_original_full_timing_and_memory_passed': all(original_gates.values())})
    return result


def crosscheck_controller_summaries(timing, independent):
    expected = []
    for s in independent:
        times, ram, maxima = s['timing_ratio_dispersion'], s['kernel_peak_ratio_dispersion'], s['kernel_peak_maxima_bytes']
        timing_passed = all(v for k, v in s['individual_gates'].items() if not k.startswith('common_parent_'))
        parent_passed = all(v for k, v in s['individual_gates'].items() if k.startswith('common_parent_'))
        expected.append({'stratum': s['stratum'], 'complete_pairs': 6,
            'pairs': [{'pair': r['pair'], 'timing_ratios_candidate_over_baseline': r['timing_ratios'],
                       'kernel_peak_ratios_candidate_over_baseline': r['kernel_peak_ratios']} for r in s['pairs']],
            'timing_median_matched_ratios': {n: v['median'] for n, v in times.items()},
            'kernel_peak_median_matched_ratios': {n: v['median'] for n, v in ram.items()},
            'timing_ratio_min_max': {n: [v['minimum'], v['maximum']] for n, v in times.items()},
            'kernel_peak_ratio_min_max': {n: [v['minimum'], v['maximum']] for n, v in ram.items()},
            'faster_first_draft_pairs': s['faster_first_draft_pairs'],
            'maximum_peak_no_increase': {n: v['candidate'] <= v['baseline'] for n, v in maxima.items()},
            'timing_gates_passed': timing_passed, 'parent_memory_acceptance_passed': parent_passed,
            'original_all_five_scope_zero_increase_gates_passed': s['diagnostic_original_all5_zero_increase_passed'],
            'original_scope_diagnostics': {n: {'matched_median_ratio': ram[n]['median'],
                'maximum_baseline_peak_bytes': maxima[n]['baseline'], 'maximum_candidate_peak_bytes': maxima[n]['candidate'],
                'median_zero_increase_passed': ram[n]['median'] <= 1,
                'maximum_zero_increase_passed': maxima[n]['candidate'] <= maxima[n]['baseline'],
                'original_zero_increase_passed': ram[n]['median'] <= 1 and maxima[n]['candidate'] <= maxima[n]['baseline']}
                for n in ROLES},
            'original_v4_v5_full_timing_and_memory_gates_passed': s['diagnostic_original_full_timing_and_memory_passed'],
            'timing_and_memory_gates_passed': s['timing_and_memory_gates_passed']})
    need(timing.get('paired_comparisons') == expected, 'controller_comparison_disagrees_with_independent_calculation')


def public_bindings(m, runtime):
    return {'baseline_commit': BASELINE_COMMIT, 'candidate_commit': m['config']['candidate_commit'],
        'source_sha256': SOURCE_SHA, 'source_size_bytes': SOURCE_SIZE,
        'source_license': 'CC-BY-SA-3.0', 'source_creator': 'Wikimedia Deutschland (WMDE)',
        'request_fixture_sha256': FIXTURE_SHA, 'installed_sdk_wheel_sha256': WHEEL_SHA,
        'images': {side: {'image_sha256': v['id'], 'runtime_version': v['binding']['version'],
                         'cli_sha256': v['binding']['tool_sha256']['/usr/local/bin/cutmy']} for side, v in m['images'].items()},
        'native_tool_sha256': {'/usr/bin/' + n: v for n, v in TOOLS.items()},
        'helper_sha256': {Path(p).name: d for p, d in m['frozen_inputs'].items() if Path(p).suffix == '.py'},
        'memory_protocol_decision': {n: v for n, v in m['memory_protocol_decision'].items() if n != 'path'},
        'registered_metric_gates': m['metric_gates'], 'diagnostic_gates': m['diagnostic_gates'],
        'coordinator_instrumentation': m['coordinator_instrumentation'], 'runtime_coordinator': runtime,
        'limits': m['limits'], 'network_internal': False,
        'application_numeric_config': {n: v for n, v in m['application_config'].items()
            if n.startswith(('MAX_', 'STORAGE_', 'WORKER_', 'FFMPEG_', 'GOMEM', 'GOMAX', 'MUTATIONS')) and not n.endswith('_PATH')},
        'scope': 'Ready-source real720interview, full15draft→15final local loopback transport under registered caps; lifetime kernel peak includes boot/upload/prime. No cold OS/page-cache, upload-to-first-output, public-network/SLA/universal-codec claim.'}


def execution(e, directory, index, input_path, oracle_sha):
    v, digest = e.doc(directory / f'fallback-{index:02}-execution.json')
    need(v['passed'] is True and v['exit_code'] == v['docker_exit_code'] == 0 and v['oom_killed'] is False
         and v['container_removed'] is True and v['image_id'] == BASELINE_IMAGE, 'native_execution_incomplete')
    original_intent, _ = e.doc(directory / f'fallback-{index:02}-intent.json')
    need(all(v.get(k) == value for k, value in original_intent.items()), 'native_execution_intent_changed')
    argv = v['argv']; need(isinstance(argv, list) and argv[-7:] == ['--entrypoint', '/usr/bin/python3', BASELINE_IMAGE,
         '-I', '-B', str(ROOT / 'inputs/verify-content.py'), str(input_path)] and v['output_sha256'] == oracle_sha,
         'native_execution_input_or_output_changed')
    need(all(flag in argv for flag in ('--network=none', '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges:true',
                                      '--cpus=1', '--memory=512m', '--memory-swap=512m', '--pids-limit=128')),
         'native_execution_caps_changed')
    labels = v['labels']; need(labels.get('cutmy.first-draft') == '20261016'
         and labels.get('cutmy.first-draft.role') == 'quality-oracle'
         and labels.get('com.docker.compose.project') == 'cutmy-firstdraft-bench'
         and labels.get('com.docker.compose.service') == 'quality-oracle'
         and re.fullmatch('cutmyfirstdraftoracle[a-f0-9]{32}', v['container_name'])
         and labels.get('cutmy.first-draft.oracle') == v['container_name']
         and argv[:5] == ['docker', '--host', 'unix:///var/run/docker.sock', 'run', '--pull=never']
         and argv[argv.index('--name') + 1] == v['container_name']
         and '--user=0:0' in argv and '--mount' in argv
         and argv[argv.index('--mount') + 1] == f'type=bind,source={ROOT},target={ROOT}', 'native_execution_scope')
    for kind in ('stdout', 'stderr'):
        path = directory / f'fallback-{index:02}-{kind}.log'
        need(e.digest(path, 4 << 20, empty=True) == (v[kind + '_sha256'] or hashlib.sha256(b'').hexdigest()), 'native_execution_log_changed')
    return digest


def coordinator(e, m, timing, registration_path, registration_sha):
    intent_path = ROOT / 'proofs' / ('COORDINATOR-' + m['config']['cohort_id'] + '-LAUNCH.json')
    launch, launch_sha = e.doc(intent_path); nonce = launch['nonce']
    need(isinstance(nonce, str) and re.fullmatch('[a-f0-9]{32}', nonce), 'coordinator_nonce_scope')
    name = 'cutmyfirstdraftcoordinator' + nonce + '.service'
    directory = ROOT / 'proofs' / ('coordinator-' + nonce)
    need(launch['kind'] == 'immutable_owned_coordinator_launch_intent' and launch['unit_name'] == name
         and launch['cohort_id'] == m['config']['cohort_id'] and launch['window'] == m['config']['window_id']
         and e.path(launch['registration_path']) == e.path(registration_path) and launch['registration_sha256'] == registration_sha
         and e.path(launch['controller_path']) == ROOT / 'inputs/controller-cohort-v6.py'
         and launch['controller_sha256'] == HELPERS['controller-cohort-v6.py']
         and launch['launcher_sha256'] == HELPERS['launch-cohort.py']
         and launch['policy'] == m['coordinator_instrumentation']
         and e.path(launch['log_path']) == directory / 'coordinator.log'
         and launch['description'] == f'cutmy.first-draft=20261016 coordinator={nonce} registration={registration_sha}',
         'coordinator_launch_intent_binding')
    actual, actual_sha = e.doc(directory / 'ACTUAL-COORDINATOR.json')
    need(actual['kind'] == 'actual_detached_coordinator_runtime' and actual['protocol_version'] == 6
         and actual['launch_intent_sha256'] == launch_sha and actual['registration_sha256'] == registration_sha
         and actual['controller_sha256'] == HELPERS['controller-cohort-v6.py']
         and actual['launcher_sha256'] == HELPERS['launch-cohort.py'] and actual['unit_name'] == name
         and re.fullmatch('[a-f0-9]{32}', actual['invocation_id'])
         and actual['cgroup'] == '/sys/fs/cgroup/system.slice/' + name
         and actual['actual_fsize_limits'] == [8 << 20, 8 << 20]
         and actual['outside_all_measured_task_parents'] is True and actual['actual_description_verified'] is True
         and actual['detached_from_ssh_stdout'] is True, 'actual_coordinator_placement_required')
    kernel = actual['actual_kernel']
    need(kernel['memory.max'] == str(128 << 20) and kernel['memory.swap.max'] == '0'
         and kernel['cpu.max'].split() == ['10000', '100000'] and kernel['pids.max'] == '64', 'actual_coordinator_caps_changed')
    accepted, accepted_sha = e.doc(directory / 'LAUNCH-ACCEPTED.json'); unit = accepted['actual_unit']
    need(accepted['launch_intent_sha256'] == launch_sha and accepted['registration_sha256'] == registration_sha
         and accepted['controller_sha256'] == HELPERS['controller-cohort-v6.py']
         and accepted['launcher_sha256'] == HELPERS['launch-cohort.py']
         and accepted['detached_from_ssh_stdout'] is True and accepted['automatic_takeover_or_retry'] is False
         and unit['LoadState'] == 'loaded' and unit['Description'] == launch['description']
         and unit['MemoryMax'] == str(128 << 20) and unit['MemorySwapMax'] == '0' and unit['TasksMax'] == '64'
         and unit['CPUQuotaPerSecUSec'] == '100ms' and unit['RuntimeMaxUSec'] == '4h'
         and unit['RemainAfterExit'] == 'yes' and unit['LimitFSIZE'] == str(8 << 20)
         and unit['ControlGroup'] == '/system.slice/' + name and unit['ActiveState'] == 'active',
         'actual_launcher_unit_policy_changed')
    safe = {'receipt_sha256': actual_sha, 'launch_intent_sha256': launch_sha, 'launcher_sha256': HELPERS['launch-cohort.py']}
    bindings = timing['bindings']
    need(bindings == public_bindings(m, safe),
         'public_coordinator_or_gate_binding_changed')
    final, final_sha = e.doc(directory / 'COORDINATOR-FINAL-COST.json')
    need(final_sha == timing['coordinator_final_cost_sha256']
         and final['kind'] == 'coordinator_outside_instrumentation_final_cost'
         and final['actual_coordinator_receipt_sha256'] == actual_sha
         and final['outside_measured_parent_and_reported_separately'] is True and final['performance_claim_ready'] is False,
         'separate_final_coordinator_cost_required')
    k = final['actual_kernel']; values = dict(line.split() for line in k['memory.events'].splitlines())
    need(all(values.get(n) == '0' for n in ('oom', 'oom_kill'))
         and k['memory.peak'].isdecimal() and int(k['memory.peak']) > 0
         and k['memory.current'].isdecimal(), 'coordinator_oom_or_kernel_cost_missing')
    cpu = dict(line.split() for line in k['cpu.stat'].splitlines())
    need(cpu.get('usage_usec', '').isdecimal(), 'coordinator_cpu_usage_missing')
    return {**safe, 'launch_accepted_receipt_sha256': accepted_sha, 'final_cost_receipt_sha256': final_sha,
            'outside_product_memory_acceptance_scope': True, 'kernel_peak_bytes': int(k['memory.peak']),
            'kernel_current_bytes_at_final_boundary': int(k['memory.current']), 'cpu_usage_microseconds': int(cpu['usage_usec'])}


def audit(args):
    e = Evidence(); m, reg_sha, inventory, plan = registration(e, args.registration, args.expected_registration_sha256)
    cohort = ROOT / 'proofs' / ('cohort-' + m['config']['cohort_id']); qdir = cohort / 'content-addressed-quality'
    need(not any((cohort / n).exists() for n in ('FAILED.json', 'NEGATIVE-FRESH-FUTILITY.json', 'NEGATIVE-INCOMPLETE-TIMING-RESULT.json')),
         'failed_or_incomplete_cohort_cannot_qualify')
    timing, timing_sha = e.doc(cohort / 'PUBLIC-TIMING-RESULT.json'); records, records_sha = e.doc(cohort / 'private-records.json')
    need(timing.get('status') == 'all24_timings_collected_content_pending' and timing.get('completed_runs') == 24
         and len(timing['rows']) == len(records) == 24 and timing['registration_sha256'] == reg_sha
         and timing['native_oracles_executed_by_controller'] is False and timing['performance_claim_ready'] is False
         and timing['all_failed_adverse_rows_retained'] is True, 'all24_closed_timings_required')
    coordinator_cost = coordinator(e, m, timing, args.registration, reg_sha)
    quality, quality_sha = e.doc(qdir / 'PUBLIC-QUALITY-REUSE-RESULT.json'); binding, _ = e.doc(qdir / 'INPUT-BINDING.json')
    need(quality['kind'] == 'prospective_content_addressed_cohort_quality' and quality['passed'] is True
         and quality['registration_sha256'] == binding['registration_sha256'] == reg_sha
         and quality['timing_result_sha256'] == binding['timing_result_sha256'] == timing_sha
         and quality['private_records_sha256'] == binding['private_records_sha256'] == records_sha
         and quality['inventory_sha256'] == binding['inventory_sha256'] == m['prospective_quality']['inventory_sha256']
         and quality['runner_sha256'] == binding['runner_sha256'] == HELPERS['quality-reuse-v6.py']
         and quality['baseline_runtime_image_id'] == BASELINE_IMAGE
         and quality['original_helper_sha256'] == {n: HELPERS[n] for n in ORIGINAL}, 'quality_registration_or_raw_timing_binding')
    cache = {}; prior_baselines = []
    need(1 <= len(inventory['seeds']) <= 2, 'bounded_prior_inventory_required')
    for seed in inventory['seeds']:
        need(seed['oracle_sha256'] in PRIOR_ORACLES, 'unqualified_prior_oracle')
        sample = sdk_scope(e, seed['input'], plan)
        need(e.digest(seed['input']) == seed['input_sha256'] and sample['timed_sha256'] == seed['timed_receipt_sha256'], 'prior_input_or_timing_changed')
        certificates, private = native_certificates(e, sample, seed['oracle_sha256'])
        need(key(certificates) == seed['certificates_sha256'], 'prior_certificate_list_changed')
        insert(cache, certificates, 'prior')
        if sample['public']['role'] == 'baseline': prior_baselines.append(private)
    need(prior_baselines and len(cache) == inventory['distinct_source_certificates']
         and key([cache[n] for n in sorted(cache)]) == inventory['certificate_aggregate_sha256'], 'physical_prior_inventory_changed')
    env = next(iter(cache.values()))['environment']; need(all(v['environment'] == env for v in cache.values()), 'mixed_qualified_environments')
    rows = []; views = {}; samples = []; counters = dict(now=0, prior=0, current=0, native_invocations=0); distinct = set()
    cohort_host = None
    need(len(quality['samples_receipts']) == 24, 'all24_quality_samples_required')
    for index, ((stratum, pair, side), public_row, record, summary) in enumerate(zip(order(), timing['rows'], records, quality['samples_receipts']), 1):
        expected_id = f'{m["config"]["cohort_id"]}-{"fresh" if stratum == STRATA[0] else "primed"}-p{pair:02}-{side}'
        sample_dir = ROOT / 'samples' / expected_id; input_path = sample_dir / 'client-input.json'
        need(record['sample_id'] == expected_id and record['record'] == public_row
             and all(public_row[n] == v for n, v in (('stratum', stratum), ('pair', pair), ('side', side)))
             and e.path(record['receipt']) == sample_dir / 'sdkrun/public-receipt.json'
             and e.path(record['backend']) == sample_dir / 'backend-result.json', 'sample_order_or_pooling_changed')
        sample = sdk_scope(e, input_path, plan); backend, backend_sha = e.doc(record['backend'])
        need(sample['timed_sha256'] == record['receipt_sha256'] and backend_sha == record['backend_sha256']
             and sample['public']['expected_server_commit_from_controller'] == m['images'][side]['commit'], 'sample_runtime_receipts_changed')
        before, after = record['host_oom_before'], record['host_oom_after']
        need(isinstance(before['boot_id'], str) and re.fullmatch('[a-f0-9-]{36}', before['boot_id'])
             and before['boot_id'] == after['boot_id'] and type(before['oom_kill']) is type(after['oom_kill']) is int
             and before['oom_kill'] >= 0 and before['oom_kill'] == after['oom_kill'], 'host_oom_or_reboot')
        observed_host = before['boot_id'], before['oom_kill']
        cohort_host = cohort_host or observed_host
        need(observed_host == cohort_host, 'host_boot_or_oom_changed_between_samples')
        times, peaks = backend_scope(m, backend, sample['public'], side, stratum, reg_sha)
        need(times == public_row['timings_seconds'] and peaks == public_row['actual_kernel_peak_bytes']
             and all(public_row[n] is True for n in ('all30_database_counts_passed', 'owned_cleanup_passed', 'production_unchanged')), 'raw_public_clock_peak_or_validity_mismatch')
        proof, proof_sha = e.doc(qdir / f'SAMPLE-{index:02}.json'); view, view_sha = e.doc(qdir / f'FINGERPRINTS-{index:02}.json')
        need(summary['ordinal'] == index and all(summary[n] == v for n, v in (('stratum', stratum), ('pair', pair), ('side', side)))
             and summary['qualification_receipt_sha256'] == proof_sha
             and summary['timed_receipt_sha256'] == proof['timed_receipt_sha256'] == sample['timed_sha256']
             and summary['backend_receipt_sha256'] == proof['backend_receipt_sha256'] == backend_sha
             and proof['kind'] == 'content_addressed_sample_qualification' and proof['passed'] is True
             and proof['current_bound_document_digests'] == sample['bound_document_digests']
             and proof['exact_native_environment'] == env, 'sample_quality_binding_changed')
        native_now = any(certificate_keys(r['output'], r['intent'], env)[1] not in cache for r in sample['rows'])
        execution_sha = None
        if native_now:
            oracle_path = sample['directory'] / 'content-oracle-receipt.json'; oracle_sha = e.digest(oracle_path)
            execution_sha = execution(e, qdir, index, input_path, oracle_sha)
            certificates, _ = native_certificates(e, sample, oracle_sha)
            need(all(c['environment'] == env for c in certificates), 'fallback_native_environment_changed')
            insert(cache, certificates, 'current'); counters['now'] += 30; counters['native_invocations'] += 1
        expected_outputs = []; mappings = []
        for row in sample['rows']:
            out = row['output']; decode, source = certificate_keys(out, row['intent'], env); cert = cache[source]
            need(cert['decode_key'] == decode and cert['intent'] == row['intent'] and cert['tracks'] == row['tracks'], 'reused_source_or_tracks_changed')
            expected_outputs.append({'rendition': row['rendition'], 'clip_id': out['clip_id'],
                'encoded_file_sha256': out['sha256'], 'encoded_size_bytes': out['size_bytes'], **cert['fingerprint']})
            mappings.append({'rendition': row['rendition'], 'ordinal': row['ordinal'], 'encoded_sha256': out['sha256'],
                'size_bytes': out['size_bytes'], 'decode_key': decode, 'source_key': source,
                'qualification_oracle_sha256': cert['oracle_sha256'], 'certificate_origin': cert['origin']})
            distinct.add(decode)
            if not native_now: counters[cert['origin']] += 1
        need(proof['qualifications'] == mappings and view == {'tool_binding': env['tool_binding'], 'outputs': expected_outputs}, 'qualification_mapping_or_fingerprints_changed')
        need(proof['outputs_freshly_hashed'] == proof['current_request_manifest_checks'] == summary['outputs_freshly_hashed'] == summary['current_request_manifest_checks'] == 30
             and proof['full_output_decodes_executed_now'] == summary['full_output_decodes_executed_now'] == (30 if native_now else 0)
             and proof['native_decoding_not_repeated_for_reused_bytes'] is (not native_now), 'dishonest_per_sample_native_or_reuse_count')
        rows.append({n: public_row[n] for n in ('side', 'stratum', 'pair', 'timings_seconds', 'actual_kernel_peak_bytes')})
        samples.append({'ordinal': index, 'side': side, 'stratum': stratum, 'pair': pair,
            'timed_receipt_sha256': sample['timed_sha256'], 'backend_receipt_sha256': backend_sha,
            'qualification_receipt_sha256': proof_sha, 'qualified_fingerprints_sha256': view_sha,
            'native_execution_receipt_sha256': execution_sha, 'native_output_decodes_now': 30 if native_now else 0})
        views[(stratum, pair, side)] = view
    final_pairs = []; need(len(quality['final_pairs']) == 12, 'all12_final_pairs_required')
    for index, (s, p) in enumerate(((s, p) for s in STRATA for p in range(1, 7)), 1):
        result = {'stratum': s, 'pair': p, **compare(views[(s, p, 'baseline')], views[(s, p, 'candidate')])}
        observed, digest = e.doc(qdir / f'PAIR-{index:02}.json')
        need(observed == quality['final_pairs'][index - 1] == result, 'paired_final_proof_changed')
        final_pairs.append({**result, 'receipt_sha256': digest})
    repeat = compare(prior_baselines[0], views[(STRATA[0], 1, 'baseline')]); observed, repeat_sha = e.doc(qdir / 'BASELINE-REPEAT.json')
    need(observed == repeat and repeat_sha == quality['baseline_repeat_receipt_sha256'] and quality['baseline_repeat_passed'] is True,
         'baseline_repeat_qualification_changed')
    need(all(quality[n] == v for n, v in {'samples': 24, 'outputs_freshly_hashed': 720, 'current_request_manifest_checks': 720,
         'final_target_file_rehash_checks': 720, 'logical_final_pairs': 12, 'logical_final_output_comparisons': 180,
         'full_output_decode_executions_now': counters['now'], 'whole_sample_native_verifier_invocations_now': counters['native_invocations'],
         'additional_target_full_file_hash_passes_after_native': counters['now'], 'prior_certificate_reuse_outputs': counters['prior'],
         'current_run_certificate_reuse_outputs': counters['current'], 'distinct_encoded_objects_seen': len(distinct)}.items())
         and sum(counters[n] for n in ('now', 'prior', 'current')) == 720
         and quality['all720_qualified_but_not_claimed_as720_native_executions'] is True
         and quality['performance_claim_ready'] is False and quality['strict_time_memory_gates_independent'] is True,
         'dishonest_quality_counts_or_readiness')
    independent = metrics(rows); crosscheck_controller_summaries(timing, independent)
    passed = all(r['timing_and_memory_gates_passed'] for r in independent)
    report = {'schema_version': 1, 'kind': 'independent_complete_prospective_v6_cohort_audit', 'samples': 24, 'cohorts': 1,
        'all24_rows': rows, 'strata': independent, 'timing_and_memory_gates_passed': passed, 'performance_claim_ready': passed,
        'quality': {'passed': True, 'quality_receipt_sha256': quality_sha, 'outputs_freshly_hashed_by_quality_runner': 720,
            'final_target_rehash_checks_by_quality_runner': 720, 'current_request_manifest_checks': 720,
            'full_output_decode_executions_now': counters['now'], 'prior_certificate_reuse_outputs': counters['prior'],
            'current_run_certificate_reuse_outputs': counters['current'], 'whole_sample_native_invocations_now': counters['native_invocations'],
            'distinct_encoded_objects': len(distinct), 'logical_final_output_comparisons': 180,
            'encoded_final_files_equal': sum(r['encoded_file_hashes_equal'] for r in final_pairs),
            'baseline_repeat_receipt_sha256': repeat_sha, 'samples': samples, 'final_pairs': final_pairs},
        'strict_common_memory_allowance_percent': 0, 'required_common_peak_median_improvement_percent': 2,
        'strict_final_time_allowance_percent': 0,
        'scope': 'One prospective ready-source 720p interview cohort; all15draft then all15final; local loopback transport/caps; lifetime kernel high-water includes boot/upload/validation prime.',
        'limitations': ['No cold OS/page-cache, public-network, upload-to-first-output, 1080p60, universal-codec or production SLA claim.',
            'Current metadata and exact qualified fingerprint/certificate chains are independently checked; this audit does not decode encoded media again.',
            'Prior and current native executions are counted separately from logical qualified-output reuse.',
            'Client peak is an actual kernel high-water; if its cgroup expired, client caps are bound to the frozen launcher rather than retrospectively observed kernel limit files.',
            'Synthetic control runs validate the audit program only and are not genuine cohort evidence.',
            'The memory decision was prospectively corrected after the unsuccessful M2 series: actual common-parent median must improve by at least2%, its maximum must not increase, all role caps and no-OOM requirements remain fixed. Individual lifetime role peaks are not additive; all original all5-role zero-increase decisions remain displayed as diagnostics.',
            'M2 remains rejected under its original protocol. No historical run, pilot or failed partial series enters this new cohort.',
            'This is controlled benchmark acceptance only; release, deployment and production readiness require separate checks.'],
        'rejected_m2_report': REJECTED_LINK,
        'coordinator_instrumentation': coordinator_cost,
        'provenance': {'registration_sha256': reg_sha, 'timing_result_sha256': timing_sha, 'private_records_sha256': records_sha,
            'inventory_sha256': m['prospective_quality']['inventory_sha256'], 'source_sha256': SOURCE_SHA, 'source_size_bytes': SOURCE_SIZE,
            'memory_protocol_decision_sha256': m['memory_protocol_decision']['sha256'],
            'memory_protocol_decided_at_utc': m['memory_protocol_decision']['decided_at_utc'],
            'separate_original_m2_registration_sha256': OLD_REGISTRATION,
            'separate_original_m2_negative_witness_sha256': OLD_NEGATIVE_WITNESS,
            'fixture_sha256': FIXTURE_SHA, 'signed_sdk_wheel_sha256': WHEEL_SHA, 'installed_sdk_payload_sha256': SDK_PAYLOAD,
            'baseline_commit': BASELINE_COMMIT, 'candidate_commit': m['config']['candidate_commit'],
            'candidate_source_archive_sha256': e.doc(m['config']['build_provenance'])[0]['source_archive_sha256'],
            'source_archive_verification_scope': 'Digest in the frozen builder provenance; this metadata audit does not rebuild or re-extract source.',
            'images': {side: {'image_sha256': v['id'], 'commit': v['commit'],
                'cli_sha256': v['binding']['tool_sha256']['/usr/local/bin/cutmy']} for side, v in m['images'].items()},
            'native_tool_sha256': TOOLS, 'reviewed_helper_sha256': HELPERS, 'kernel_caps': LIMITS,
            'all_frozen_input_digests': sorted(m['frozen_inputs'].values()), 'physical_metadata_bundle_sha256': e.finish(),
            'source_license': 'CC-BY-SA-3.0', 'source_creator': 'Wikimedia Deutschland (WMDE)',
            'image_verification_scope': 'Exact frozen image/source/tool records and actual backend receipts; no Docker image execution by this auditor.'}}
    return report


def render(report):
    lines = [('The registered first-draft goal is met.' if report['performance_claim_ready'] else 'The registered first-draft goal is not met.'), '',
        'One complete prospective series is shown: six alternating matched pairs in each ready-source process stratum. All 15 drafts finish before all 15 finals. '
        'The first-file clock includes the complete download and consumer SHA256 verification. Upload and the one priming validation are outside user clocks, while actual kernel memory peaks include the complete task lifetime.', '',
        '| Stratum | Pair | Side | First draft s | All15 drafts s | All15 finals s | Whole30 s | Parent MiB | API MiB | Worker MiB | PG MiB | Client MiB |',
        '|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|']
    for r in report['all24_rows']:
        values = [r['stratum'], str(r['pair']), r['side'], *[f'{r["timings_seconds"][n]:.6f}' for n in TIMES],
                  *[f'{r["actual_kernel_peak_bytes"][n] / (1 << 20):.6f}' for n in ROLES]]
        lines.append('| ' + ' | '.join(values) + ' |')
    lines += ['', 'Ratios divide candidate by its matched baseline. No pilot enters the medians. V6 requires a common-parent median ratio at most0.98 and no increase in its maximum; every original all5-role zero-increase result remains a diagnostic.', '',
        '| Stratum | First median ratio | Faster pairs | Draft ratio | Final ratio | Whole ratio | Parent ratio | API | Worker | PG | Client | V6 gates | Original all5 diagnostic |',
        '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---|']
    for s in report['strata']:
        values = [s['stratum'], f'{s["timing_ratio_dispersion"][TIMES[0]]["median"]:.9f}', str(s['faster_first_draft_pairs']),
            *[f'{s["timing_ratio_dispersion"][n]["median"]:.9f}' for n in TIMES[1:]],
            *[f'{s["kernel_peak_ratio_dispersion"][n]["median"]:.9f}' for n in ROLES],
            'PASS' if s['timing_and_memory_gates_passed'] else 'FAIL',
            'PASS' if s['diagnostic_original_all5_zero_increase_passed'] else 'FAIL']
        lines.append('| ' + ' | '.join(values) + ' |')
    c = report['coordinator_instrumentation']
    lines += ['', f'The detached coordinator ran outside the product task parents and is reported separately: actual kernel peak {c["kernel_peak_bytes"]}bytes; CPU usage {c["cpu_usage_microseconds"]}microseconds. Its128MiB/10%CPU/64-task/8MiB-log policy and retained actual placement/cost receipts are independently bound.', '']
    q = report['quality']
    lines += ['', f'Quality verification freshly hashed all 720 target outputs and checked every current request/map/track manifest; all 720 files were rehashed at its final boundary. '
        f'It executed {q["full_output_decode_executions_now"]} complete output decodes now, reused {q["prior_certificate_reuse_outputs"]} prior native-qualified outputs '
        f'and {q["current_run_certificate_reuse_outputs"]} outputs qualified earlier in this quality run. '
        f'All 180 logical final comparisons and the 15-final baseline repeat passed; encoded whole-file SHA256 equality holds for {q["encoded_final_files_equal"]}/180 finals.', '',
        'The JSON retains every raw clock and kernel peak, all matched ratios and dispersion, maximum guards, failed gates, individual sample/backend/quality/fingerprint digests and all twelve final comparison digests.', '',
        *['- ' + s for s in report['limitations']], '',
        f'The earlier unsuccessful M2 series remains separate: [rejected M2]({REJECTED_LINK}).', '',
        'Immutable provenance:', '', '```json', json.dumps(report['provenance'], sort_keys=True, indent=2), '```', '']
    return '\n'.join(lines)


def main():
    p = argparse.ArgumentParser(description=__doc__); p.add_argument('--registration', required=True, type=Path)
    p.add_argument('--expected-registration-sha256', required=True); p.add_argument('--output-dir', required=True, type=Path)
    args = p.parse_args()
    try:
        os.umask(0o077); report = audit(args)
        output = Evidence().path(args.output_dir)
        need(output.parent.is_dir() and not output.exists() and not output.is_symlink()
             and output.parent.stat().st_uid == os.geteuid() and output.parent.stat().st_mode & 0o077 == 0,
             'new_report_directory_required')
        args.output_dir.mkdir(mode=0o700)
        for name, data in [('summary.json', json.dumps(report, sort_keys=True, ensure_ascii=True, allow_nan=False, indent=2) + '\n'),
                           ('README.md', render(report))]:
            with (args.output_dir / name).open('x', encoding='utf-8') as f:
                os.chmod(args.output_dir / name, 0o600); f.write(data); f.flush(); os.fsync(f.fileno())
        print(json.dumps({'audit_passed': True, 'complete_runs': 24, 'strict_gates_passed': report['timing_and_memory_gates_passed'],
                          'performance_claim_ready': report['performance_claim_ready']})); return 0
    except BaseException as error:
        code = str(error) if isinstance(error, Refused) else 'v6_report_evidence_invalid'
        if not re.fullmatch('[a-z0-9_]{1,100}', code): code = 'v6_report_evidence_invalid'
        print(json.dumps({'audit_passed': False, 'code': code, 'performance_claim_ready': False})); return 1


if __name__ == '__main__':
    raise SystemExit(main())
