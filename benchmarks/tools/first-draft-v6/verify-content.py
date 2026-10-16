#!/usr/bin/env python3
"""Independent range-based frame/audio checks, AFTER the timed resource window.

The fixed caller plan supplies source times and crop; SDK output maps are checked
against that intent and never constitute the source-content oracle. This reuses
the previously reviewed v1.4 content comparator, copied byte-for-byte to cache.
"""
import importlib.util
import json
import os
from pathlib import Path
import sys
import time

HERE = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def main(input_path):
    driver = module('first_draft_driver', HERE / 'run-python.py')
    content = module('frozen_content_comparator', HERE / 'content-oracle.py')
    full = module('full_decode', HERE / 'full-decode.py')
    config, root, _, _ = driver.input_document(input_path)
    source, fixture = Path(config['source_path']), Path(config['fixture_path'])
    driver.require(driver.file_hash(source) == driver.SOURCE_SHA and
                   driver.file_hash(fixture, 4 << 20) == driver.FIXTURE_SHA, 'independent_input_binding')
    public = driver.read(root / 'public-receipt.json', private=True)
    driver.require(public['complete'] is True and public['controller_input_sha256'] == driver.file_hash(input_path, 4 << 20)
                   and public['harness_sha256'] == driver.file_hash(HERE / 'run-python.py', 1 << 20), 'timed_receipt_binding')
    frozen_plan = driver.read(fixture)['initial']['plan']
    deadline = time.monotonic() + 900
    def bounded_run(argv, limit=4 << 20):
        remaining = deadline - time.monotonic()
        driver.require(remaining > 0, 'oracle_deadline')
        return full.run(argv, limit, min(30, remaining))
    content.run = bounded_run  # Same comparator/thresholds, pinned absolute baseline-family tool executables.
    tools = full.tool_binding(driver.file_hash)
    source_profile = content.probe(source)
    summaries = []
    fingerprints = []
    for stage in ('draft', 'final'):
        report = driver.read(root / (stage + '-private-receipt.json'), private=True)
        request = {'plan': frozen_plan, 'rendition': stage}
        clips = frozen_plan['clips']
        driver.require(report.get('complete') is True and report['rendition'] == stage and
                       [o['clip_id'] for o in report['outputs']] == [c['id'] for c in clips], 'full_fifteen_oracle_required')
        observations = []
        for output, clip, bounded_public in zip(report['outputs'], clips, public[stage]['outputs']):
            driver.require(Path(output['path']).is_relative_to(root / 'collection/files') and
                           all(output[k] == bounded_public[k] for k in ('sha256', 'size_bytes')), 'media_receipt_changed')
            observations.append(content.check_output(source, source_profile, output, clip, request))
            driver.require(time.monotonic() < deadline, 'oracle_deadline')
            fingerprint = full.fingerprint(Path(output['path']), min(120, deadline - time.monotonic()))
            fingerprints.append({'rendition': stage, 'clip_id': clip['id'], 'path': output['path'],
                                 'encoded_file_sha256': output['sha256'], 'encoded_size_bytes': output['size_bytes'],
                                 **fingerprint})
        correlations = [x['min_audio_correlation'] for x in observations if x['min_audio_correlation'] is not None]
        summaries.append({'rendition': stage, 'clips': len(clips),
                          'segments': sum(x['segments'] for x in observations),
                          'frame_samples': sum(x['frame_samples'] for x in observations),
                          'max_frame_mae': round(max(x['max_frame_mae'] for x in observations), 4),
                          'audio_samples': sum(x['audio_samples'] for x in observations),
                          'min_audio_correlation': round(min(correlations), 4) if correlations else None,
                          'silent_segments': sum(x['silent_segments'] for x in observations)})
    driver.require(driver.file_hash(source) == driver.SOURCE_SHA, 'source_changed_during_oracle')
    private = {'schema_version': 1, 'complete': True, 'tool_binding': tools, 'source_sha256': driver.SOURCE_SHA,
               'fixture_sha256': driver.FIXTURE_SHA,
               'timed_receipt_sha256': driver.file_hash(root / 'public-receipt.json', 4 << 20), 'outputs': fingerprints}
    driver.atomic(root / 'full-decoded-fingerprints.json', private)
    aggregate = full.canonical_hash([{'rendition': row['rendition'], 'ordinal': i % 15,
                                    **{k: row[k] for k in full.COMPARE_FIELDS}} for i, row in enumerate(fingerprints)])
    result = {'schema_version': 1, 'record_kind': 'independent_fifteen_draft_then_final_content_oracle',
              'passed': True, 'source_sha256': driver.SOURCE_SHA, 'fixture_sha256': driver.FIXTURE_SHA,
              'timed_receipt_sha256': driver.file_hash(root / 'public-receipt.json', 4 << 20),
              'frozen_comparator_sha256': driver.file_hash(HERE / 'content-oracle.py', 1 << 20),
              'verifier_sha256': driver.file_hash(Path(__file__).resolve(), 1 << 20),
              'full_decode_helper_sha256': driver.file_hash(HERE / 'full-decode.py', 1 << 20),
              'tool_binding': tools, 'full_video_audio_decoded_outputs': len(fingerprints),
              'decoded_content_aggregate_sha256': aggregate,
              'private_fingerprints_sha256': driver.file_hash(root / 'full-decoded-fingerprints.json', 4 << 20),
              'source_reference_times_come_from_frozen_caller_ranges': True,
              'frame_mae_limit': content.FRAME_MAE_LIMIT, 'audio_correlation_minimum': content.AUDIO_CORRELATION_MIN,
              'stages': summaries, 'sdk_timing_completed_before_decode': True}
    driver.atomic(root / 'content-oracle-receipt.json', result)
    print(json.dumps(result, sort_keys=True, ensure_ascii=True), flush=True)


if __name__ == '__main__':
    os.umask(0o077)
    try:
        if len(sys.argv) != 2: raise ValueError('private_input_required')
        main(Path(sys.argv[1]).absolute())
    except Exception as error:
        code = getattr(error, 'code', 'independent_content_failed')
        if not isinstance(code, str) or not __import__('re').fullmatch('[a-z0-9_]{1,100}', code): code = 'independent_content_failed'
        print(json.dumps({'passed': False, 'code': code, 'exception_type': type(error).__name__}),
              file=sys.stderr)
        raise SystemExit(1)
