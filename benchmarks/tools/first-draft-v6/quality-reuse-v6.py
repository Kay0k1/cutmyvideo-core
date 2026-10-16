#!/usr/bin/env python3
"""Prospective-only reuse of genuine complete native media qualifications.

No import-time process/network/media work. Freeze inventory BEFORE a NEW cohort;
run only AFTER that cohort closes. Missing qualification uses the ORIGINAL full
30-output native verifier. No old formal result or native count is rewritten.
"""
import argparse
import copy
import datetime
import fcntl
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import re
import stat
import sys

ROOT = Path('/root/.cache/cutmy-first-draft-20261016')
OLD_REGISTRATION = '8f408fee2296afeba50a7aa48fc4cca2c031acbb467c746fdca4d9547d75a7e7'
MEMORY_DECISION_SHA = '019f13d749a006999ea7270d144a2172f3d31c7a1f79cea12c3ce8ccb6e9881c'
PRESERVED_V5 = {'controller-cohort-v5.py': '1226acb834fd177027e1140f96d1a4edc609e61a7c68ef1c068402ad8861ca67',
                'quality-reuse.py': '92b73d276b0adfdf93c97fd985574b50d9306a45f88f32e5b7d4e0f2622ef2f1'}
BASELINE_IMAGE = 'sha256:4e794f76aeb89fd4838f5509bc1890fc2fce7196ea189e11282bb15eedbec96d'
SOURCE_SHA = '0f9e167eea8a4c503396cf819edd5e356c94858befef12a8e3667cea22d27fca'
FIXTURE_SHA = '686005efd644b86a0ddf41442458c10b223e58b5c1950c59915bdd9c3fc90ae5'
QUALIFIED_PILOTS = {
    '9fadbc21f8907be9d0607d3f874324cc97340f85f8c41586044b482ca85147ac',
    '99b6591285e033a9afbcdab0efdc8a5c63e3848f2276538ab8c7f4b92bd7d8c6',
}
HELPERS = {
    'run-python.py': '0b9578c2d44bdc4e8f6eea41fbb86a66595524c3fb7bade978b8cf5283481f46',
    'verify-content.py': '9d47fc35f1cdba81e54fd080e49aef9ff2bf30fa558df6efa77b240a3a65437f',
    'content-oracle.py': 'cb50448cc01390879778b41d73610e776a854b8e45675b895e23bc6d63a5d455',
    'full-decode.py': '88f5fd75724f5f3e518e2f3f21375e087e564e1694c5a08f8c77489cfaa0590a',
    'compare-final.py': '4672403bb28aac0b52b1fce7dad477e87ecad5bfe8c9332e774bf0018fc7f120',
}
ORCHESTRATOR_SHA = 'b3a0a890824c6607e28e48bc01456f6b32b07ece1b8b81769b6ccb5c5a743a82'
TOOL_SHA = {'ffmpeg': '0dafc1360bb07743f76abeb1e4ae16b0aaa1331e9041a0e8d6a8524111dc0cbb',
            'ffprobe': '0e598fe6d247bfe63757f5c839dc145a166b3f876f2875ca88a306a5b8ec5a58'}
SHA = re.compile('[a-f0-9]{64}\\Z')
COHORT = re.compile('[a-z0-9][a-z0-9-]{0,19}\\Z')
STRATA = ('ready-fresh-process', 'ready-primed-process')
FP_FIELDS = ('decoded_video_sha256', 'decoded_audio_sha256', 'decoded_clock_sha256',
             'decoded_video_frames', 'decoded_audio_frames', 'decoded_audio_samples_per_channel',
             'packet_clocks_sha256', 'packet_payloads_sha256', 'media_structure_sha256', 'packet_count')
FILE_LIMIT = 32 << 20


class Refused(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def need(value, code):
    if not value:
        raise Refused(code)


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True, allow_nan=False).encode()


def key(value):
    return hashlib.sha256(canonical(value)).hexdigest()


def safe_path(raw, exists=True):
    path = Path(raw)
    need(path.is_absolute() and path.is_relative_to(ROOT) and '..' not in path.parts, 'outside_owned_cache')
    for ancestor in (path, *path.parents):
        if ancestor == ROOT.parent:
            break
        need(not ancestor.is_symlink(), 'cache_symlink')
    if exists:
        need(path.exists(), 'missing_owned_file')
    return path


def read(path):
    path = safe_path(path)
    value = path.stat()
    need(stat.S_ISREG(value.st_mode) and value.st_uid == os.geteuid()
         and value.st_mode & 0o077 == 0 and value.st_nlink == 1
         and 0 < value.st_size <= 4 << 20, 'private_bounded_document_required')
    with path.open('rb') as stream:
        raw = stream.read((4 << 20) + 1)
    need(len(raw) <= 4 << 20, 'document_budget')
    return json.loads(raw)


def file_identity(value):
    return (value.st_dev, value.st_ino, value.st_size, value.st_mtime_ns, value.st_ctime_ns, value.st_nlink)


def hash_file(raw, maximum=FILE_LIMIT):
    path = safe_path(raw)
    before = path.lstat()
    need(stat.S_ISREG(before.st_mode) and before.st_nlink == 1 and 0 < before.st_size <= maximum,
         'regular_single_link_file_required')
    value = hashlib.sha256()
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, 'rb') as stream:
        need(file_identity(os.fstat(stream.fileno())) == file_identity(before), 'file_replaced_before_hash')
        consumed = 0
        for chunk in iter(lambda: stream.read(64 << 10), b''):
            consumed += len(chunk)
            need(consumed <= maximum, 'growing_file_hash_budget')
            value.update(chunk)
        need(consumed == before.st_size, 'file_size_changed_during_hash')
        need(file_identity(os.fstat(stream.fileno())) == file_identity(before), 'file_changed_during_hash')
    need(file_identity(path.lstat()) == file_identity(before), 'file_replaced_during_hash')
    return {'sha256': value.hexdigest(), 'size_bytes': before.st_size,
            'stat': list(file_identity(before))}


def digest(path, maximum=4 << 20):
    return hash_file(path, maximum)['sha256']


def save(path, value):
    path = safe_path(path, False)
    need(path.parent.is_dir() and not path.exists(), 'new_owned_output_required')
    raw = canonical(value) + b'\n'
    need(len(raw) <= 4 << 20, 'output_document_budget')
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, 'wb') as stream:
        stream.write(raw); stream.flush(); os.fsync(stream.fileno())
    descriptor = os.open(path.parent, os.O_DIRECTORY)
    try: os.fsync(descriptor)
    finally: os.close(descriptor)


def module(name):
    spec = importlib.util.spec_from_file_location('quality_reuse_' + name.replace('-', '_'), ROOT / 'inputs' / (name + '.py'))
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


def freeze_helpers():
    for name, expected in HELPERS.items():
        need(digest(ROOT / 'inputs' / name) == expected, 'original_quality_helper_changed')
    need(digest(ROOT / 'inputs/run-cohort-oracles.py') == ORCHESTRATOR_SHA, 'native_orchestrator_changed')
    for name, expected in PRESERVED_V5.items():
        need(digest(ROOT / 'inputs' / name) == expected, 'original_v5_source_changed')
    need(hash_file(ROOT / 'inputs/interview.mp4', 128 << 20)['sha256'] == SOURCE_SHA,
         'source_content_changed')
    need(digest(ROOT / 'inputs/requests.json') == FIXTURE_SHA, 'frozen_request_changed')


def environment(tools):
    need(set(tools) == set(TOOL_SHA), 'tool_family_required')
    for name in TOOL_SHA:
        need(tools[name].get('executable_sha256') == TOOL_SHA[name]
             and SHA.fullmatch(tools[name].get('version_stdout_sha256', '')), 'tool_binding_changed')
    return {'baseline_image_id': BASELINE_IMAGE, 'tool_binding': tools, 'original_helpers': HELPERS}


def semantics(plan, clip, rendition):
    need(rendition in ('draft', 'final') and plan.get('version') == 1, 'semantic_version_or_rendition')
    permitted = {'format', 'quality', 'cut_mode', 'subtitle_mode', 'audio_track', 'no_audio', 'crop', 'clear_crop'}
    base, local = plan.get('options', {}), clip.get('options', {})
    need(isinstance(base, dict) and isinstance(local, dict) and set(base) <= permitted and set(local) <= permitted,
         'unknown_effective_option')
    options = {name: value for name, value in base.items() if value not in (None, '')}
    options.update({name: value for name, value in local.items() if value not in (None, '')})
    options.setdefault('format', 'mp4'); options.setdefault('quality', 'best')
    options.setdefault('cut_mode', 'accurate'); options.setdefault('subtitle_mode', 'drop')
    options.setdefault('no_audio', False); options.setdefault('audio_track', -1)
    need(options['format'] == 'mp4' and options['cut_mode'] == 'accurate'
         and options['quality'] in ('best', '720p', '1080p') and options['subtitle_mode'] == 'drop'
         and options['no_audio'] is False and type(options['audio_track']) is int
         and options['audio_track'] >= -1, 'original_native_fixture_options_required')
    if options.pop('clear_crop', False):
        need('crop' not in local, 'crop_and_clear_crop')
        options.pop('crop', None)
    crop = options.get('crop')
    if crop is not None:
        need(isinstance(crop, dict) and set(crop) == {'x', 'y', 'width', 'height'}
             and all(type(x) in (int, float) and math.isfinite(x) for x in crop.values())
             and 0 <= crop['x'] < 1 and 0 <= crop['y'] < 1 and 0 < crop['width'] <= 1
             and 0 < crop['height'] <= 1 and crop['x'] + crop['width'] <= 1
             and crop['y'] + crop['height'] <= 1, 'invalid_crop')
    if rendition == 'draft':
        options['quality'] = '720p'
    ranges = clip.get('ranges')
    need(isinstance(ranges, list) and len(ranges) == 2, 'two_ordered_ranges_required')
    ordered = []
    for interval in ranges:
        start, end = interval.get('start_ms'), interval.get('end_ms')
        need(type(start) is int and type(end) is int and 0 <= start < end, 'invalid_ordered_range')
        ordered.append({'start_ms': start, 'end_ms': end})
    return {'version': 1, 'source_sha256': SOURCE_SHA, 'ranges': ordered,
            'effective_options': options, 'rendition': rendition, 'output_mode': 'joined'}


def tracks(value):
    allowed = {'index', 'type', 'codec', 'language', 'channels', 'sample_rate', 'default', 'included', 'reason'}
    need(isinstance(value, list) and 1 <= len(value) <= 32, 'track_manifest_shape')
    result = []
    for row in value:
        need(isinstance(row, dict) and set(row) <= allowed and type(row.get('index')) is int
             and type(row.get('included')) is bool and row.get('type') in ('video', 'audio', 'subtitle', 'data', 'attachment'),
             'track_manifest_fields')
        need(all(type(v) in (str, int, bool) and (not isinstance(v, str) or len(v) <= 128)
                 for v in row.values()), 'track_manifest_values')
        result.append(copy.deepcopy(row))
    need(len({row['index'] for row in result}) == len(result)
         and sum(row['included'] and row['type'] == 'video' for row in result) == 1
         and sum(row['included'] and row['type'] == 'audio' for row in result) == 1
         and not any(row['included'] and row['type'] not in ('video', 'audio') for row in result),
         'track_manifest_selection')
    return result


def check_manifest(output, clip, plan, rendition, previous_tracks=None):
    intent = semantics(plan, clip, rendition)
    # Installed SDK reports rendition at report level, not on each output.
    # load_sample independently validates that report/request scope first.
    need(output.get('clip_id') == clip['id'] and output.get('rendition', rendition) == rendition,
         'output_clip_scope')
    segments = output.get('segments')
    need(isinstance(segments, list) and len(segments) == len(clip['ranges']), 'segment_count')
    cursor = 0
    for index, (wanted, actual) in enumerate(zip(clip['ranges'], segments)):
        need(isinstance(actual, dict) and set(actual) == {'index', 'label', 'source_start_ms', 'source_end_ms', 'output_start_ms', 'output_end_ms'}
             and actual.get('index') == index and actual.get('label') == wanted.get('label', '')
             and all(type(actual.get(name)) is int for name in
                     ('source_start_ms', 'source_end_ms', 'output_start_ms', 'output_end_ms')),
             'segment_manifest_shape')
        duration = wanted['end_ms'] - wanted['start_ms']
        need(actual['source_start_ms'] == wanted['start_ms']
             and abs(actual['source_end_ms'] - wanted['end_ms']) <= 100
             and actual['output_start_ms'] == cursor
             and abs(actual['output_end_ms'] - (cursor + duration)) <= 100, 'ordered_source_timeline')
        cursor = actual['output_end_ms']
    manifest = tracks(output.get('track_manifest'))
    selected_audio = [row['index'] for row in manifest if row['included'] and row['type'] == 'audio']
    if intent['effective_options']['audio_track'] >= 0:
        need(selected_audio == [intent['effective_options']['audio_track']], 'explicit_audio_manifest')
    if previous_tracks is not None:
        need(manifest == previous_tracks, 'qualified_track_manifest_changed')
    return intent, manifest


def fingerprint(row):
    need(all(name in row for name in (*FP_FIELDS, 'structure')), 'complete_native_fingerprint_required')
    value = {name: copy.deepcopy(row[name]) for name in (*FP_FIELDS, 'structure')}
    for name in FP_FIELDS:
        need(SHA.fullmatch(value[name]) if name.endswith('_sha256') else
             type(value[name]) is int and value[name] > 0, 'native_fingerprint_field')
    need(key(value['structure']) == value['media_structure_sha256'], 'native_structure_hash')
    return value


def decode_key(output, env):
    return key({'encoded_sha256': output['sha256'], 'size_bytes': output['size_bytes'], 'environment': env})


def source_key(output, intent, env):
    return key({'decode_key': decode_key(output, env), 'source_semantics': intent})


def load_sample(input_path, expected_input_sha=None):
    input_path = safe_path(input_path)
    config = read(input_path)
    need(expected_input_sha is None or digest(input_path) == expected_input_sha, 'sample_input_changed')
    directory = safe_path(config.get('run_dir', ''))
    need(directory == input_path.parent / 'sdkrun' and input_path.name == 'client-input.json'
         and input_path.parent.parent == ROOT / 'samples', 'sample_scope')
    public_path = directory / 'public-receipt.json'
    public = read(public_path)
    need(public.get('complete') is True and public.get('controller_input_sha256') == digest(input_path)
         and public.get('harness_sha256') == HELPERS['run-python.py']
         and public.get('source_sha256') == SOURCE_SHA and public.get('fixture_sha256') == FIXTURE_SHA
         and public.get('draft_then_final_unique_jobs') == public.get('owner_history_observed_jobs') == 30,
         'completed_full_sample_required')
    need(public.get('role') == config.get('role') and config.get('role') in ('baseline', 'candidate')
         and public.get('expected_server_commit_from_controller') == config.get('server_commit')
         and public.get('cache_stratum') == config.get('cache_stratum', 'ready-fresh-process'),
         'sample_runtime_or_stratum_changed')
    plan = read(ROOT / 'inputs/requests.json')['initial']['plan']
    native_requests = read(directory / 'requests.json')
    clips = plan.get('clips', [])
    need(len(clips) == 15 and len({row['id'] for row in clips}) == 15, 'fifteen_unique_clips')
    rows, jobs, artifacts = [], set(), set()
    bound = {str(path): digest(path) for path in
             (input_path, public_path, directory / 'requests.json',
              directory / 'draft-private-receipt.json', directory / 'final-private-receipt.json')}
    source_id = None
    for rendition in ('draft', 'final'):
        report = read(directory / (rendition + '-private-receipt.json'))
        request = native_requests.get(rendition, {})
        need(set(request) <= {'source_id', 'plan', 'rendition', 'clip_ids'}
             and request.get('plan') == plan and request.get('rendition') == rendition
             and not request.get('clip_ids') and report.get('complete') is True
             and report.get('rendition') == rendition
             and report.get('source_id') == request.get('source_id'), 'current_request_or_source_scope')
        source_id = source_id or report.get('source_id')
        need(isinstance(source_id, str) and source_id and report.get('source_id') == source_id,
             'one_current_source_required')
        outputs = report.get('outputs')
        need(isinstance(outputs, list) and [row.get('clip_id') for row in outputs] == [row['id'] for row in clips]
             and len(public[rendition]['outputs']) == 15, 'current_fifteen_order')
        for ordinal, (output, clip, bounded_public) in enumerate(zip(outputs, clips, public[rendition]['outputs'])):
            media = safe_path(output.get('path', ''))
            need(media.is_relative_to(directory / 'collection/files') and media.parent == directory / 'collection/files'
                 and all(output.get(name) == bounded_public.get(name) for name in ('sha256', 'size_bytes'))
                 and SHA.fullmatch(output.get('sha256', ''))
                 and type(output.get('size_bytes')) is int and 0 < output['size_bytes'] <= FILE_LIMIT,
                 'current_output_file_scope')
            for name, seen in (('job_id', jobs), ('artifact_id', artifacts)):
                identity = output.get(name)
                need(isinstance(identity, str) and identity and identity not in seen, 'current_unique_resource_scope')
                seen.add(identity)
            observed = hash_file(media)
            need((observed['sha256'], observed['size_bytes']) == (output['sha256'], output['size_bytes']),
                 'fresh_media_digest_or_size_changed')
            intent, manifest = check_manifest(output, clip, plan, rendition)
            rows.append({'output': output, 'intent': intent, 'tracks': manifest, 'observed': observed,
                         'rendition': rendition, 'ordinal': ordinal})
    need(len(jobs) == len(artifacts) == len(rows) == 30, 'thirty_distinct_outputs_required')
    return {'input': str(input_path), 'directory': directory, 'public': public,
            'timed_sha256': digest(public_path), 'rows': rows, 'bound_documents': bound}


def unchanged_sample(sample, rehash=False):
    for raw, expected in sample['bound_documents'].items():
        need(digest(raw) == expected, 'sample_manifest_changed_after_verification')
    for row in sample['rows']:
        need(list(file_identity(safe_path(row['output']['path']).lstat())) == row['observed']['stat'],
             'sample_media_changed_after_verification')
        if rehash:
            need(hash_file(row['output']['path']) == row['observed'],
                 'sample_media_bytes_changed_after_verification')


def original_proof(sample, oracle_sha=None):
    directory = sample['directory']
    oracle_path, private_path = directory / 'content-oracle-receipt.json', directory / 'full-decoded-fingerprints.json'
    oracle, private = read(oracle_path), read(private_path)
    actual_sha = digest(oracle_path)
    need(oracle_sha is None or actual_sha == oracle_sha, 'qualified_oracle_digest_changed')
    need(oracle.get('passed') is True and private.get('complete') is True
         and oracle.get('timed_receipt_sha256') == private.get('timed_receipt_sha256') == sample['timed_sha256']
         and oracle.get('private_fingerprints_sha256') == digest(private_path)
         and oracle.get('source_sha256') == private.get('source_sha256') == SOURCE_SHA
         and oracle.get('fixture_sha256') == private.get('fixture_sha256') == FIXTURE_SHA
         and oracle.get('full_video_audio_decoded_outputs') == 30
         and oracle.get('verifier_sha256') == HELPERS['verify-content.py']
         and oracle.get('full_decode_helper_sha256') == HELPERS['full-decode.py']
         and oracle.get('frozen_comparator_sha256') == HELPERS['content-oracle.py']
         and oracle.get('frame_mae_limit') == 12.0 and oracle.get('audio_correlation_minimum') == .75
         and oracle.get('source_reference_times_come_from_frozen_caller_ranges') is True
         and oracle.get('sdk_timing_completed_before_decode') is True,
         'complete_original_qualification_required')
    need(oracle.get('tool_binding') == private.get('tool_binding'), 'private_tool_binding_changed')
    env = environment(oracle['tool_binding'])
    outputs = private.get('outputs', [])
    need(len(outputs) == 30, 'thirty_native_fingerprints_required')
    need(oracle.get('decoded_content_aggregate_sha256') == key([
         {'rendition': row['rendition'], 'ordinal': index % 15,
          **{name: row[name] for name in FP_FIELDS}} for index, row in enumerate(outputs)]),
         'native_decoded_aggregate_changed')
    summaries = oracle.get('stages', [])
    need(isinstance(summaries, list) and [row.get('rendition') for row in summaries] == ['draft', 'final']
         and all(row.get('clips') == 15 and row.get('segments') == 30 and row.get('frame_samples') == 60
                 and type(row.get('max_frame_mae')) in (int, float) and row['max_frame_mae'] <= 12.0
                 and row.get('audio_samples', -1) + row.get('silent_segments', -1) == 30
                 and (row.get('min_audio_correlation') is None or row['min_audio_correlation'] >= .75)
                 for row in summaries), 'original_content_thresholds_or_counts_changed')
    certificates = []
    for current, decoded in zip(sample['rows'], outputs):
        output = current['output']
        need(decoded.get('path') == output['path'] and decoded.get('clip_id') == output['clip_id']
             and decoded.get('rendition') == current['rendition']
             and decoded.get('encoded_file_sha256') == output['sha256']
             and decoded.get('encoded_size_bytes') == output['size_bytes'], 'native_output_binding_changed')
        certificates.append({'decode_key': decode_key(output, env),
                             'source_key': source_key(output, current['intent'], env),
                             'encoded_sha256': output['sha256'], 'size_bytes': output['size_bytes'],
                             'intent': current['intent'], 'tracks': current['tracks'],
                             'fingerprint': fingerprint(decoded), 'oracle_sha256': actual_sha,
                             'private_fingerprints_sha256': digest(private_path), 'environment': env})
    return certificates, private, oracle


def insert_certificates(cache, values, origin):
    for value in values:
        previous = cache.get(value['source_key'])
        need(previous is None or all(previous[name] == value[name] for name in
             ('decode_key', 'intent', 'tracks', 'fingerprint', 'environment')), 'conflicting_qualified_certificate')
        cache.setdefault(value['source_key'], {**copy.deepcopy(value), 'origin': origin})


def freeze_inventory(config_path, output):
    config = read(config_path)
    need(set(config) == {'cohort_id', 'prior_inputs'} and COHORT.fullmatch(config.get('cohort_id', ''))
         and isinstance(config['prior_inputs'], list) and 1 <= len(config['prior_inputs']) <= 2,
         'bounded_inventory_configuration_required')
    freeze_helpers()
    seeds, cache = [], {}
    for raw in config['prior_inputs']:
        sample = load_sample(raw)
        oracle_sha = digest(sample['directory'] / 'content-oracle-receipt.json')
        need(oracle_sha in QUALIFIED_PILOTS, 'only_genuine_previously_qualified_pilot_allowed')
        certificates, _, _ = original_proof(sample, oracle_sha)
        unchanged_sample(sample)
        insert_certificates(cache, certificates, 'prior')
        seeds.append({'input': sample['input'], 'input_sha256': digest(sample['input']),
                      'timed_receipt_sha256': sample['timed_sha256'], 'oracle_sha256': oracle_sha,
                      'certificates_sha256': key(certificates)})
    value = {'schema_version': 1, 'kind': 'prospective_content_addressed_quality_inventory',
             'cohort_id': config['cohort_id'], 'frozen_at_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
             'algorithm_sha256': digest(Path(__file__)), 'native_orchestrator_sha256': ORCHESTRATOR_SHA,
             'source_sha256': SOURCE_SHA, 'fixture_sha256': FIXTURE_SHA,
             'original_helpers': HELPERS, 'baseline_runtime_image_id': BASELINE_IMAGE,
             'seeds': seeds, 'distinct_source_certificates': len(cache),
             'certificate_aggregate_sha256': key([cache[name] for name in sorted(cache)]),
             'prospective_only_no_existing_cohort_reclassification': True}
    save(output, value)
    return {'inventory_frozen': True, 'prior_samples': len(seeds), 'distinct_source_certificates': len(cache),
            'inventory_sha256': digest(output), 'native_executions': 0}


def restore_inventory(inventory):
    cache, representatives = {}, []
    for seed in inventory['seeds']:
        sample = load_sample(seed['input'], seed['input_sha256'])
        need(sample['timed_sha256'] == seed['timed_receipt_sha256']
             and seed['oracle_sha256'] in QUALIFIED_PILOTS, 'prior_certificate_chain_changed')
        certificates, private, _ = original_proof(sample, seed['oracle_sha256'])
        need(key(certificates) == seed['certificates_sha256'], 'prior_native_certificate_changed')
        insert_certificates(cache, certificates, 'prior')
        representatives.append((sample, private))
    need(len(cache) == inventory['distinct_source_certificates']
         and key([cache[name] for name in sorted(cache)]) == inventory['certificate_aggregate_sha256'],
         'frozen_inventory_no_longer_matches_physical_evidence')
    return cache, representatives


def reusable(sample, cache, env):
    selected = []
    for row in sample['rows']:
        certificate = cache.get(source_key(row['output'], row['intent'], env))
        if certificate is None:
            return None
        need(certificate['decode_key'] == decode_key(row['output'], env)
             and certificate['intent'] == row['intent'] and certificate['environment'] == env
             and certificate['tracks'] == row['tracks'], 'qualified_current_manifest_or_intent_changed')
        selected.append(certificate)
    return selected


def materialize(sample, certificates):
    outputs, mappings = [], []
    for row, certificate in zip(sample['rows'], certificates):
        output = row['output']
        outputs.append({'rendition': row['rendition'], 'clip_id': output['clip_id'],
                        'encoded_file_sha256': output['sha256'], 'encoded_size_bytes': output['size_bytes'],
                        **copy.deepcopy(certificate['fingerprint'])})
        mappings.append({'rendition': row['rendition'], 'ordinal': row['ordinal'],
                         'encoded_sha256': output['sha256'], 'size_bytes': output['size_bytes'],
                         'decode_key': certificate['decode_key'], 'source_key': certificate['source_key'],
                         'qualification_oracle_sha256': certificate['oracle_sha256'],
                         'certificate_origin': certificate['origin']})
    need(len(outputs) == 30, 'thirty_qualified_outputs_required')
    return {'tool_binding': certificates[0]['environment']['tool_binding'], 'outputs': outputs}, mappings


def schedule(manifest, records, timing, registration_sha):
    need(manifest.get('samples') == 24 and manifest.get('pairs_per_stratum') == 6
         and manifest.get('strata') == list(STRATA) and len(records) == len(timing.get('rows', [])) == 24
         and timing.get('completed_runs') == 24
         and timing.get('status') == 'all24_timings_collected_content_pending'
         and timing.get('registration_sha256') == registration_sha, 'new_full_twentyfour_closed_cohort_required')
    expected = []
    for stratum in STRATA:
        for pair in range(1, 7):
            for side in (('baseline', 'candidate') if pair % 2 else ('candidate', 'baseline')):
                token = 'fresh' if stratum == STRATA[0] else 'primed'
                expected.append((side, stratum, pair, f'{manifest["config"]["cohort_id"]}-{token}-p{pair:02}-{side}'))
    for record, public, (side, stratum, pair, sample) in zip(records, timing['rows'], expected):
        need(record.get('sample_id') == sample and record.get('record') == public
             and all(public.get(name) == value for name, value in (('side', side), ('stratum', stratum), ('pair', pair))),
             'cohort_order_or_sample_replacement')
        directory = ROOT / 'samples' / sample
        need(safe_path(record['receipt']) == directory / 'sdkrun/public-receipt.json'
             and safe_path(record['backend']) == directory / 'backend-result.json'
             and digest(record['receipt']) == record['receipt_sha256']
             and digest(record['backend']) == record['backend_sha256'], 'timing_evidence_binding')
        backend = read(record['backend'])
        counts = backend['database_counts']
        need(backend.get('ok') is True and backend.get('status') == 'collected_and_stopped'
             and backend.get('full30_workload_verified') is True
             and set(backend.get('resources_removed', {})) == {'containers', 'network', 'anchor', 'slice'}
             and all(backend['resources_removed'].values())
             and tuple(counts.get(name) for name in ('jobs', 'draft_jobs', 'final_jobs', 'artifacts', 'render_attempts', 'succeeded_jobs'))
                 == (30, 15, 15, 30, 30, 30), 'stopped_exact_thirty_backend_required')
        record['input_path'] = str(directory / 'client-input.json')


def prospective_registration(path):
    registration_sha = digest(path)
    need(registration_sha != OLD_REGISTRATION, 'original_v4_cohort_cannot_use_reuse')
    manifest = read(path)
    need(manifest.get('protocol_version') == 6, 'new_prospective_v6_registration_required')
    decision = manifest.get('memory_protocol_decision', {})
    need(decision.get('path') == str(ROOT / 'MEMORY-PROTOCOL-V6-DECISION.json')
         and decision.get('sha256') == MEMORY_DECISION_SHA
         and digest(decision['path']) == MEMORY_DECISION_SHA
         and manifest.get('frozen_inputs', {}).get(decision['path']) == MEMORY_DECISION_SHA,
         'v6_memory_decision_binding_changed')
    for name, expected in PRESERVED_V5.items():
        need(manifest.get('frozen_inputs', {}).get(str(ROOT / 'inputs' / name)) == expected,
             'preserved_v5_inputs_not_registered')
    mode = manifest.get('prospective_quality', {})
    need(mode.get('mode') == 'content_addressed_v1' and mode.get('runner_sha256') == digest(Path(__file__))
         and mode.get('native_orchestrator_sha256') == ORCHESTRATOR_SHA, 'prospective_algorithm_not_registered')
    inventory_path = safe_path(mode.get('inventory_path', ''))
    need(digest(inventory_path) == mode.get('inventory_sha256'), 'registered_inventory_changed')
    inventory = read(inventory_path)
    need(inventory.get('kind') == 'prospective_content_addressed_quality_inventory'
         and inventory.get('cohort_id') == manifest['config']['cohort_id']
         and inventory.get('algorithm_sha256') == mode['runner_sha256']
         and inventory.get('original_helpers') == HELPERS
         and inventory.get('source_sha256') == SOURCE_SHA and inventory.get('fixture_sha256') == FIXTURE_SHA
         and inventory.get('baseline_runtime_image_id') == BASELINE_IMAGE
         and datetime.datetime.fromisoformat(inventory['frozen_at_utc']) <
             datetime.datetime.fromisoformat(manifest['registered_at_utc']), 'inventory_must_precede_new_cohort')
    frozen = manifest.get('frozen_inputs', {})
    for path, expected in [(Path(__file__), mode['runner_sha256']), (inventory_path, mode['inventory_sha256']),
                           (ROOT / 'inputs/run-cohort-oracles.py', ORCHESTRATOR_SHA),
                           *((ROOT / 'inputs' / name, value) for name, value in HELPERS.items())]:
        need(frozen.get(str(path)) == expected, 'reuse_inputs_missing_from_preregistration')
    need(manifest.get('images', {}).get('baseline', {}).get('id') == BASELINE_IMAGE,
         'baseline_family_required')
    return manifest, inventory, registration_sha


def run(args):
    manifest, inventory, registration_sha = prospective_registration(args.registration)
    need(args.closed_window == manifest['config']['window_id'], 'explicit_closed_window_required')
    freeze_helpers()
    ops, full = module('run-cohort-oracles'), module('full-decode')
    cohort = ROOT / 'proofs' / ('cohort-' + manifest['config']['cohort_id'])
    records_path, timing_path = cohort / 'private-records.json', cohort / 'PUBLIC-TIMING-RESULT.json'
    records, timing = read(records_path), read(timing_path)
    records_sha, timing_sha = digest(records_path), digest(timing_path)
    schedule(manifest, records, timing, registration_sha)
    ops.frozen(manifest)
    directory = cohort / 'content-addressed-quality'
    directory.mkdir(mode=0o700)  # Existing/partial directory is a refusal; no quiet retries.
    save(directory / 'INPUT-BINDING.json', {'registration_sha256': registration_sha,
         'inventory_sha256': manifest['prospective_quality']['inventory_sha256'],
         'timing_result_sha256': timing_sha, 'private_records_sha256': records_sha,
         'runner_sha256': digest(Path(__file__))})
    completed, pairs, cache, now_decodes, invocations, prior_reuse, current_reuse = [], [], {}, 0, 0, 0, 0
    try:
        cache, representatives = restore_inventory(inventory)
        env = next(iter(cache.values()))['environment']
        need(all(row['environment'] == env for row in cache.values()), 'one_qualified_environment_required')
        views, distinct, retained = {}, set(), []
        for index, record in enumerate(records):
            ops.frozen(manifest)
            sample = load_sample(record['input_path'])
            side = record['record']['side']
            need(sample['timed_sha256'] == record['receipt_sha256']
                 and sample['public'].get('role') == side
                 and sample['public'].get('cache_stratum') == record['record']['stratum']
                 and sample['public'].get('expected_server_commit_from_controller') == manifest['images'][side]['commit'],
                 'current_timed_sample_changed')
            selected = reusable(sample, cache, env)
            native_now = selected is None
            if native_now:
                need(not (sample['directory'] / 'content-oracle-receipt.json').exists()
                     and not (sample['directory'] / 'full-decoded-fingerprints.json').exists(),
                     'existing_unregistered_native_proof_no_retry')
                invocations += 1
                ops.native(directory, f'fallback-{index + 1:02}', ROOT / 'inputs/verify-content.py',
                           [record['input_path']], sample['directory'] / 'content-oracle-receipt.json')
                now_decodes += 30
                # Re-read/hash every output after the native operation too.
                after = load_sample(record['input_path'])
                need([row['observed'] for row in sample['rows']] == [row['observed'] for row in after['rows']],
                     'target_files_changed_during_native_qualification')
                certificates, _, _ = original_proof(after)
                need(all(row['environment'] == env for row in certificates), 'native_environment_changed')
                insert_certificates(cache, certificates, 'current')
                selected = reusable(after, cache, env)
                sample = after
            view, mapping = materialize(sample, selected)
            unchanged_sample(sample)
            retained.append(sample)
            for row, certificate in zip(sample['rows'], selected):
                distinct.add(certificate['decode_key'])
                if not native_now:
                    prior_reuse += int(certificate['origin'] == 'prior')
                    current_reuse += int(certificate['origin'] == 'current')
            proof = {'schema_version': 1, 'kind': 'content_addressed_sample_qualification', 'passed': True,
                     'timed_receipt_sha256': sample['timed_sha256'], 'outputs_freshly_hashed': 30,
                     'backend_receipt_sha256': record['backend_sha256'],
                     'current_request_manifest_checks': 30, 'full_output_decodes_executed_now': 30 if native_now else 0,
                     'qualifications': mapping, 'exact_native_environment': env,
                     'current_bound_document_digests': list(sample['bound_documents'].values()),
                     'native_decoding_not_repeated_for_reused_bytes': not native_now}
            output = directory / f'SAMPLE-{index + 1:02}.json'
            save(output, proof)
            save(directory / f'FINGERPRINTS-{index + 1:02}.json', view)
            views[(record['record']['stratum'], record['record']['pair'], record['record']['side'])] = view
            completed.append({'ordinal': index + 1, **{name: record['record'][name] for name in ('side', 'stratum', 'pair')},
                              'qualification_receipt_sha256': digest(output), 'timed_receipt_sha256': sample['timed_sha256'],
                              'backend_receipt_sha256': record['backend_sha256'],
                              'outputs_freshly_hashed': 30, 'current_request_manifest_checks': 30,
                              'full_output_decodes_executed_now': 30 if native_now else 0})
        baseline = views[(STRATA[0], 1, 'baseline')]
        prior_baselines = [private for sample, private in representatives if sample['public'].get('role') == 'baseline']
        need(prior_baselines, 'qualified_baseline_repeat_representative_required')
        repeat = full.compare_final(prior_baselines[0], baseline)
        save(directory / 'BASELINE-REPEAT.json', repeat)
        for stratum in STRATA:
            for pair in range(1, 7):
                result = full.compare_final(views[(stratum, pair, 'baseline')], views[(stratum, pair, 'candidate')])
                proof = {'stratum': stratum, 'pair': pair, **result}
                save(directory / f'PAIR-{len(pairs) + 1:02}.json', proof)
                pairs.append(proof)
        ops.frozen(manifest)
        freeze_helpers()
        for sample in retained:
            unchanged_sample(sample, rehash=True)
        need(digest(records_path) == records_sha and digest(timing_path) == timing_sha,
             'timing_or_record_binding_changed_during_quality')
        need(now_decodes + prior_reuse + current_reuse == 720, 'honest_output_accounting_required')
        result = {'schema_version': 1, 'kind': 'prospective_content_addressed_cohort_quality', 'passed': True,
                  'registration_sha256': registration_sha, 'inventory_sha256': manifest['prospective_quality']['inventory_sha256'],
                  'timing_result_sha256': timing_sha, 'private_records_sha256': records_sha,
                  'runner_sha256': digest(Path(__file__)), 'baseline_runtime_image_id': BASELINE_IMAGE,
                  'original_helper_sha256': HELPERS, 'samples': 24,
                  'outputs_freshly_hashed': 720, 'current_request_manifest_checks': 720,
                  'additional_target_full_file_hash_passes_after_native': now_decodes,
                  'final_target_file_rehash_checks': 720,
                  'distinct_encoded_objects_seen': len(distinct),
                  'whole_sample_native_verifier_invocations_now': invocations,
                  'full_output_decode_executions_now': now_decodes,
                  'prior_certificate_reuse_outputs': prior_reuse, 'current_run_certificate_reuse_outputs': current_reuse,
                  'logical_final_pairs': 12, 'logical_final_output_comparisons': 180,
                  'baseline_repeat_passed': True, 'baseline_repeat_receipt_sha256': digest(directory / 'BASELINE-REPEAT.json'),
                  'all720_qualified_but_not_claimed_as720_native_executions': True,
                  'samples_receipts': completed, 'final_pairs': pairs,
                  'performance_claim_ready': False, 'strict_time_memory_gates_independent': True}
        save(directory / 'PUBLIC-QUALITY-REUSE-RESULT.json', result)
        return {name: result[name] for name in ('passed', 'outputs_freshly_hashed', 'current_request_manifest_checks',
                'full_output_decode_executions_now', 'prior_certificate_reuse_outputs',
                'current_run_certificate_reuse_outputs', 'logical_final_output_comparisons', 'performance_claim_ready')}
    except BaseException:
        save(directory / 'FAILED.json', {'passed': False, 'completed_samples': len(completed),
             'completed_pairs': len(pairs), 'whole_native_invocations_now': invocations,
             'failed_evidence_retained_no_retry_or_replacement': True, 'performance_claim_ready': False})
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    freeze = sub.add_parser('freeze'); freeze.add_argument('--config', required=True, type=Path)
    freeze.add_argument('--output', required=True, type=Path)
    execute = sub.add_parser('run'); execute.add_argument('--registration', required=True, type=Path)
    execute.add_argument('--closed-window', required=True)
    args = parser.parse_args()
    try:
        os.umask(0o077)
        need(os.geteuid() == 0, 'root_operator_required')
        lock_path = safe_path(ROOT / '.first-draft-cohort-window.lock')
        with lock_path.open('rb') as lock:
            try: fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError: raise Refused('timing_or_native_window_active') from None
            result = freeze_inventory(args.config, args.output) if args.action == 'freeze' else run(args)
        print(json.dumps(result, ensure_ascii=True, sort_keys=True))
        return 0
    except BaseException as error:
        code = getattr(error, 'code', 'content_addressed_quality_failed')
        if not isinstance(code, str) or not re.fullmatch('[a-z0-9_]{1,100}', code):
            code = 'content_addressed_quality_failed'
        print(json.dumps({'passed': False, 'code': code, 'performance_claim_ready': False}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
