#!/usr/bin/env python3
"""Audit/render the ONE registered cohort, without media or remote execution.

Reads bounded local metadata copies only. Keeps all24 rows and independently
recomputes strict matched-pair timing/memory gates. A valid failed goal still
produces an honest report. Missing/tampered evidence produces no report.
"""
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import statistics

REGISTERED = '8f408fee2296afeba50a7aa48fc4cca2c031acbb467c746fdca4d9547d75a7e7'
AUDIT_SHA = '8289b28b353745c4b01ec8f1f6a9c3477075989dbebfc8287b3493cd29a530e4'
COHORT = 'm2-primary-v1'
STRATA = ('ready-fresh-process', 'ready-primed-process')
SIDES = ('baseline', 'candidate')
TIMES = ('first_downloaded_draft_seconds', 'all15_draft_seconds', 'all15_final_seconds', 'whole30_cycle_seconds')
ROLES = ('parent', 'api', 'worker', 'pg', 'client')
SHA = re.compile(r'[a-f0-9]{64}\Z')
QUALITY_HELPERS = ('run-python.py', 'verify-content.py', 'full-decode.py', 'content-oracle.py', 'compare-final.py')
NATIVE_TOOLS = {'/usr/bin/ffmpeg': '0dafc1360bb07743f76abeb1e4ae16b0aaa1331e9041a0e8d6a8524111dc0cbb',
                '/usr/bin/ffprobe': '0e598fe6d247bfe63757f5c839dc145a166b3f876f2875ca88a306a5b8ec5a58'}


class Refused(Exception):
    pass


def need(value, code):
    if not value:
        raise Refused(code)


def raw(path):
    need(path.is_absolute() and path.is_file() and not path.is_symlink(), 'bounded_local_metadata_required')
    before = path.stat()
    need(before.st_uid == os.geteuid() and not before.st_mode & 0o077 and 0 < before.st_size <= 4 << 20,
         'bounded_private_metadata_required')
    data = path.read_bytes()
    after = path.stat()
    need((before.st_dev, before.st_ino, before.st_size, before.st_mtime_ns) ==
         (after.st_dev, after.st_ino, after.st_size, after.st_mtime_ns), 'metadata_changed_during_read')
    return data, hashlib.sha256(data).hexdigest()


def document(path):
    data, digest = raw(path)
    return json.loads(data), digest


def finite_positive(value):
    return type(value) in (int, float) and math.isfinite(value) and value > 0


def expected_order():
    return [(s, pair, side) for s in STRATA for pair in range(1, 7)
            for side in (SIDES if pair % 2 else tuple(reversed(SIDES)))]


def key(row):
    return row['stratum'], row['pair'], row['side']


def dispersion(values):
    q = statistics.quantiles(values, n=4, method='inclusive')
    return {'median': statistics.median(values), 'minimum': min(values), 'maximum': max(values),
            'inclusive_first_quartile': q[0], 'inclusive_third_quartile': q[2], 'interquartile_range': q[2] - q[0]}


def independent_metrics(rows):
    result = []
    for stratum in STRATA:
        pairs = []
        for number in range(1, 7):
            matched = {row['side']: row for row in rows if row['stratum'] == stratum and row['pair'] == number}
            a, b = matched['baseline'], matched['candidate']
            pairs.append({'pair': number,
                          'timing_ratios': {name: b['timings_seconds'][name] / a['timings_seconds'][name] for name in TIMES},
                          'memory_ratios': {name: b['actual_kernel_peak_bytes'][name] / a['actual_kernel_peak_bytes'][name] for name in ROLES}})
        timings = {name: dispersion([p['timing_ratios'][name] for p in pairs]) for name in TIMES}
        memory = {name: dispersion([p['memory_ratios'][name] for p in pairs]) for name in ROLES}
        faster = sum(pair['timing_ratios'][TIMES[0]] < 1 for pair in pairs)
        maxima = {}
        for role in ROLES:
            maximum = {side: max(r['actual_kernel_peak_bytes'][role] for r in rows
                                if r['stratum'] == stratum and r['side'] == side) for side in SIDES}
            maxima[role] = {**maximum, 'passed': maximum['candidate'] <= maximum['baseline']}
        gates = {'first_draft_at_least20_percent_faster': timings[TIMES[0]]['median'] <= .8,
                 'first_draft_faster_in_at_least5_pairs': faster >= 5,
                 **{name + '_median_no_increase': timings[name]['median'] <= 1 for name in TIMES[1:]},
                 **{name + '_kernel_peak_median_no_increase': memory[name]['median'] <= 1 for name in ROLES},
                 **{name + '_kernel_peak_maximum_no_increase': maxima[name]['passed'] for name in ROLES}}
        result.append({'stratum': stratum, 'complete_pairs': 6, 'pairs': pairs,
                       'timing_ratio_dispersion': timings, 'kernel_peak_ratio_dispersion': memory,
                       'faster_first_draft_pairs': faster, 'kernel_peak_maxima_bytes': maxima,
                       'individual_gates': gates, 'timing_and_memory_gates_passed': all(gates.values())})
    return result


def observations(path, rows):
    data, digest = raw(path)
    pairs = {}; last = 0
    for line in data.splitlines():
        value = json.loads(line)
        need(value.get('status') in ('running', 'all24_timings_closed'), 'failed_or_refused_metadata_observation')
        need(value['registration_sha256'] == REGISTERED and value['completed_pairs'] >= last,
             'mixed_registration_or_reversed_observation')
        last = value['completed_pairs']
        for pair in value['new_completed_pairs']:
            identity = pair['stratum'], pair['pair']
            need(identity not in pairs and len(pair['rows']) == 2, 'duplicate_or_incomplete_observed_pair')
            pairs[identity] = pair
    need(set(pairs) == {(s, p) for s in STRATA for p in range(1, 7)}, 'all12_independently_observed_pairs_required')
    bound = {}
    for row in rows:
        pair = pairs[(row['stratum'], row['pair'])]
        item = next((v for v in pair['rows'] if v['side'] == row['side']), None)
        need(item is not None and item['timings_seconds'] == row['timings_seconds']
             and item['actual_kernel_peak_bytes'] == row['actual_kernel_peak_bytes'], 'observed_clock_or_peak_mismatch')
        need(all(item.get(name) is True for name in ('database_counts_checked', 'actual_parent_and_server_caps_checked',
                                                   'host_oom_unchanged', 'production_unchanged', 'cleanup_verified')),
             'independent_metadata_checks_incomplete')
        need(SHA.fullmatch(item['receipt_sha256']) and SHA.fullmatch(item['backend_sha256']), 'invalid_sample_receipt_digest')
        bound[key(row)] = item
    need(len({v['receipt_sha256'] for v in bound.values()}) == 24 and
         len({v['backend_sha256'] for v in bound.values()}) == 24, 'reused_timed_or_backend_receipt')
    return bound, digest


def quality(value, digest, expected_orchestrator, timing_digest, bound, audit):
    need(SHA.fullmatch(expected_orchestrator or ''), 'externally_reviewed_oracle_orchestrator_digest_required')
    need(value.get('record_kind') == 'completed_postwindow_cohort_quality' and value.get('passed') is True
         and value.get('registration_sha256') == REGISTERED and value.get('timing_result_sha256') == timing_digest
         and value.get('orchestrator_sha256') == expected_orchestrator, 'quality_run_or_orchestrator_binding_changed')
    need((value.get('samples'), value.get('full_video_audio_decoded_outputs'), value.get('paired_comparisons'),
          value.get('paired_final_outputs')) == (24, 720, 12, 180), 'complete720_output180_paired_final_quality_required')
    need(all(value.get(name) is True for name in ('baseline_repeat_qualification_passed',
                 'original_content_thresholds_unchanged', 'all_owned_success_containers_removed',
                 'native_oracles_after_formal_window', 'time_memory_strict_acceptance_still_independent')),
         'quality_or_baseline_repeat_incomplete')
    need(SHA.fullmatch(value['baseline_repeat_receipt_sha256']) and
         value['baseline_runtime_image_sha256'] == audit['images']['baseline']['image_sha256'], 'baseline_repeat_or_runtime_changed')
    need(value['oracle_helper_sha256'] == {n: audit['frozen_small_sha256'][n] for n in QUALITY_HELPERS}, 'original_quality_algorithm_changed')
    samples = value['decoded_samples']
    need(len(samples) == 24 and [(s['stratum'], s['pair'], s['side']) for s in samples] == expected_order(), 'quality_sample_pooling_or_order_changed')
    frame_count = audio_count = silent_count = 0
    for ordinal, sample in enumerate(samples, 1):
        k = key(sample); row = bound[k]
        token = 'fresh' if k[0] == STRATA[0] else 'primed'
        identity = f'{COHORT}-{token}-p{k[1]:02}-{k[2]}'
        need(sample['ordinal'] == ordinal and sample['sample_identity_sha256'] == hashlib.sha256(identity.encode()).hexdigest()
             and sample['timed_receipt_sha256'] == row['receipt_sha256'] and sample['backend_receipt_sha256'] == row['backend_sha256'],
             'quality_not_from_this_timed_sample')
        need(sample['full_video_audio_decoded_outputs'] == 30 and SHA.fullmatch(sample['oracle_sha256'])
             and SHA.fullmatch(sample['decoded_content_aggregate_sha256']), 'sample_decode_or_digest_incomplete')
        need([s['rendition'] for s in sample['stages']] == ['draft', 'final'], 'both_renditions_source_oracle_required')
        for stage in sample['stages']:
            need((stage['clips'], stage['segments'], stage['frame_samples']) == (15, 30, 60)
                 and stage['audio_samples'] + stage['silent_segments'] == 30, 'all_source_ranges_video_and_audio_required')
            need(type(stage['max_frame_mae']) in (int, float) and 0 <= stage['max_frame_mae'] <= 12, 'source_frame_threshold_changed')
            if stage['audio_samples']:
                need(type(stage['min_audio_correlation']) in (int, float) and .75 <= stage['min_audio_correlation'] <= 1.000001,
                     'source_audio_threshold_changed')
            frame_count += stage['frame_samples']; audio_count += stage['audio_samples']; silent_count += stage['silent_segments']
    final_pairs = value['final_pairs']
    need(len(final_pairs) == 12 and [(v['stratum'], v['pair']) for v in final_pairs] == [(s, p) for s in STRATA for p in range(1, 7)],
         'all12_final_pairs_required_no_pooling')
    for item in final_pairs:
        need(item['final_clips'] == 15 and type(item['encoded_file_hashes_equal']) is int
             and 0 <= item['encoded_file_hashes_equal'] <= 15 and SHA.fullmatch(item['receipt_sha256'])
             and SHA.fullmatch(item['decoded_final_aggregate_sha256']), 'invalid_final_pair_content_proof')
    return {'passed': True, 'quality_receipt_sha256': digest, 'orchestrator_sha256': expected_orchestrator,
            'full_video_audio_decoded_outputs': 720, 'exact_decoded_video_audio_packet_clock_payload_final_comparisons': 180,
            'baseline_repeat_final_outputs': 15, 'baseline_repeat_receipt_sha256': value['baseline_repeat_receipt_sha256'],
            'source_frame_samples': frame_count, 'source_speech_correlation_samples': audio_count,
            'source_silence_checks': silent_count, 'frame_mae_maximum': max(v['max_frame_mae'] for s in samples for v in s['stages']),
            'audio_correlation_minimum': min(v['min_audio_correlation'] for s in samples for v in s['stages'] if v['audio_samples']),
            'encoded_final_files_byte_equal': sum(v['encoded_file_hashes_equal'] for v in final_pairs),
            'thresholds': {'maximum_frame_mae': 12, 'minimum_audio_correlation': .75},
            'all24_timed_and_backend_receipts_bound': True, 'original_five_helper_digests_bound': True}


def render(report):
    ready = report['performance_claim_ready']
    text = ['The registered first-draft goal is ' + ('met' if ready else 'not yet met') + '.', '',
            'This report retains every run of one cohort: six alternating matched pairs in each ready-source process stratum. '
            'The workload requests all 15 drafts through the released Python SDK, verifies the first complete download by SHA256, '
            'finishes all 15 drafts, then exports all 15 finals. Source preparation and the one validation prime are outside the user clock; '
            'actual kernel memory peaks include the complete backend/client lifetime.', '',
            'Times are seconds. Memory values below are actual kernel high-water bytes converted to MiB. '
            'This is a 720p interview under fixed local transport and caps; it makes no cold OS cache, public-network, 1080p60, universal-codec, or production SLA claim.', '',
            '| Stratum | Pair | Side | First draft s | All15 draft s | All15 final s | Whole30 s | Parent MiB | API MiB | Worker MiB | PG MiB | Client MiB |',
            '|---|---:|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|']
    for row in report['all24_rows']:
        t = row['timings_seconds']; m = row['actual_kernel_peak_bytes']
        text.append('| ' + ' | '.join([row['stratum'], str(row['pair']), row['side'],
                    *[f'{t[k]:.6f}' for k in TIMES], *[f'{m[k] / (1 << 20):.3f}' for k in ROLES]]) + ' |')
    text.extend(['', 'Ratios are candidate divided by the baseline of the same pair. All gates use the six matched ratios per stratum; '
                 'no supplemental pilot enters these medians. Every memory role also has a maximum-peak guard.', '',
                 '| Stratum | First-draft median ratio | Faster pairs | Draft median ratio | Final median ratio | Whole median ratio | Parent peak median ratio | API | Worker | PG | Client | Strict gates |',
                 '|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|'])
    for row in report['strata']:
        t = row['timing_ratio_dispersion']; m = row['kernel_peak_ratio_dispersion']
        text.append('| ' + ' | '.join([row['stratum'], f'{t[TIMES[0]]["median"]:.6f}', str(row['faster_first_draft_pairs']),
                   *[f'{t[k]["median"]:.6f}' for k in TIMES[1:]], *[f'{m[k]["median"]:.6f}' for k in ROLES],
                   'PASS' if row['timing_and_memory_gates_passed'] else 'FAIL']) + ' |')
    text.extend(['', 'All 24 raw clocks/peaks, all 12 per-pair ratios, quartiles, min/max dispersion, maximum-peak guards and failed individual gates are retained in the accompanying JSON.', ''])
    if report['quality'] is None:
        text.append('Content acceptance is pending. Timings alone cannot establish release readiness.')
    else:
        q = report['quality']
        text.append(f'Independent postwindow verification fully decoded video and audio in all {q["full_video_audio_decoded_outputs"]} outputs, '
                    f'checked {q["source_frame_samples"]} source video samples and {q["source_speech_correlation_samples"]} speech correlations '
                    f'plus {q["source_silence_checks"]} silence checks. All 180 paired finals retained exactly equal decoded video/audio, packet clocks, '
                    f'encoded packet payloads and geometry/codec metadata. Baseline repeat qualification covers 15 finals. '
                    f'Encoded file SHA256 equality holds for {q["encoded_final_files_byte_equal"]}/180 paired finals.')
    text.extend(['', 'Immutable provenance:', '', '```json', json.dumps(report['provenance'], sort_keys=True, indent=2), '```', ''])
    return '\n'.join(text)


def main(args):
    audit, audit_digest = document(args.registration_audit)
    need(audit_digest == AUDIT_SHA and audit.get('passed') is True and audit['registered_sha256'] == REGISTERED,
         'externally_reviewed_physical_registration_audit_required')
    timing, timing_digest = document(args.timings)
    need(timing.get('status') == 'all24_timings_collected_content_pending' and timing.get('completed_runs') == 24
         and timing.get('registration_sha256') == REGISTERED and timing.get('native_oracles_executed_by_controller') is False
         and timing.get('all_failed_adverse_rows_retained') is True, 'one_complete_registered_timing_cohort_required')
    rows = timing['rows']
    need(len(rows) == 24 and [key(v) for v in rows] == expected_order(), 'missing_duplicate_pooled_or_reordered_rows')
    for row in rows:
        need(set(row['timings_seconds']) == set(TIMES) and all(finite_positive(v) for v in row['timings_seconds'].values())
             and set(row['actual_kernel_peak_bytes']) == set(ROLES)
             and all(type(v) is int and v > 0 for v in row['actual_kernel_peak_bytes'].values()), 'invalid_raw_clock_or_peak')
        need(all(row.get(k) is True for k in ['all30_database_counts_passed', 'owned_cleanup_passed', 'production_unchanged']), 'timed_sample_metadata_failed')
    bound, observation_digest = observations(args.observations, rows)
    metrics = independent_metrics(rows)
    b = timing['bindings']
    for side in SIDES:
        need(b['images'][side]['image_sha256'] == audit['images'][side]['image_sha256']
             and b['images'][side]['cli_sha256'] == audit['images'][side]['cli_sha256']
             and b['images'][side]['runtime_version']['commit'] == audit['images'][side]['commit'], 'timing_runtime_changed')
    need(b['limits'] == audit['limits'] and b['source_sha256'] == audit['source_sha256']
         and b['source_size_bytes'] == audit['source_size_bytes']
         and b['request_fixture_sha256'] == audit['request_fixture_sha256']
         and b['installed_sdk_wheel_sha256'] == audit['installed_sdk_wheel_sha256'], 'timing_fixture_or_limits_changed')
    need(b['helper_sha256'] == {n: d for n, d in audit['frozen_small_sha256'].items() if n.endswith('.py')}, 'timing_helper_changed')
    need(b['native_tool_sha256'] == NATIVE_TOOLS, 'timing_native_tool_changed')
    q = None
    if args.quality:
        value, digest = document(args.quality)
        q = quality(value, digest, args.oracle_orchestrator_sha256, timing_digest, bound, audit)
    provenance = {'registration_sha256': REGISTERED, 'timing_result_sha256': timing_digest,
                  'independent_registration_audit_sha256': audit_digest, 'independent_pair_observations_sha256': observation_digest,
                  'source_sha256': audit['source_sha256'], 'source_size_bytes': audit['source_size_bytes'],
                  'request_fixture_sha256': audit['request_fixture_sha256'], 'installed_sdk_wheel_sha256': audit['installed_sdk_wheel_sha256'],
                  'images': audit['images'], 'native_tool_sha256': {k: b['native_tool_sha256'][k] for k in ['/usr/bin/ffmpeg', '/usr/bin/ffprobe']},
                  'frozen_helper_sha256': {k: v for k, v in audit['frozen_small_sha256'].items() if k.endswith('.py')},
                  'kernel_resource_caps': audit['limits'],
                  'application_caps': {'worker_concurrency': 1, 'preparation_concurrency': 0, 'ffmpeg_threads': 2,
                                       'ffmpeg_profile': 'fast', 'go_memory_limit': '128MiB',
                                       'owner_active_jobs_default': 3, 'owner_export_attempts_per_minute_default': 10,
                                       'mutations_per_minute': 20, 'maximum_ranges': 32,
                                       'maximum_source_bytes': 128 << 20, 'maximum_output_bytes': 32 << 20,
                                       'maximum_owner_bytes': 512 << 20, 'maximum_storage_bytes': 2 << 30},
                  'source_license': 'CC-BY-SA-3.0', 'source_creator': 'Wikimedia Deutschland (WMDE)'}
    clean_rows = [{k: row[k] for k in ['side', 'stratum', 'pair', 'timings_seconds', 'actual_kernel_peak_bytes']} for row in rows]
    report = {'schema_version': 1, 'kind': 'independent_complete_first_draft_cohort_audit', 'samples': 24,
              'cohorts': 1, 'supplemental_pilots_used_for_medians': False, 'all24_rows': clean_rows, 'strata': metrics,
              'quality': q, 'provenance': provenance, 'strict_memory_allowance_percent': 0, 'strict_final_time_allowance_percent': 0,
              'timing_and_memory_gates_passed': all(r['timing_and_memory_gates_passed'] for r in metrics),
              'independent_content_oracle_pending': q is None,
              'performance_claim_ready': q is not None and all(r['timing_and_memory_gates_passed'] for r in metrics),
              'scope': 'Ready-source real720p interview; full15draft then15final; one registered cohort; fixed loopback transport/caps; actual lifetime kernel high-water. No coldOS cache or public-network or universal-codec claim.'}
    need(args.output_dir.is_absolute() and not args.output_dir.exists(), 'new_local_report_directory_required')
    args.output_dir.mkdir(mode=0o700)
    for name, content in [('summary.json', json.dumps(report, sort_keys=True, allow_nan=False, indent=2) + '\n'), ('README.md', render(report))]:
        with (args.output_dir / name).open('x', encoding='utf-8') as out:
            os.chmod(args.output_dir / name, 0o600); out.write(content); out.flush(); os.fsync(out.fileno())
    print(json.dumps({'ok': True, 'samples': 24, 'content_pending': q is None,
                      'timing_memory_gates_passed': report['timing_and_memory_gates_passed'],
                      'performance_claim_ready': report['performance_claim_ready']}, sort_keys=True))


if __name__ == '__main__':
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--timings', type=Path, required=True)
    parser.add_argument('--observations', type=Path, required=True)
    parser.add_argument('--registration-audit', type=Path, required=True)
    parser.add_argument('--quality', type=Path)
    parser.add_argument('--oracle-orchestrator-sha256')
    parser.add_argument('--output-dir', type=Path, required=True)
    args = parser.parse_args()
    try:
        main(args)
    except Exception as error:
        print(json.dumps({'ok': False, 'code': str(error) if isinstance(error, Refused) else 'invalid_formal_metadata',
                          'performance_claim_ready': False}, sort_keys=True))
        raise SystemExit(1)
