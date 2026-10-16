#!/usr/bin/env python3
"""Compare reviewed project outputs with independently decoded source content.

The Go installed-package harness separately proves fault injection, database
counts, unchanged-job reuse, owner isolation and supplied-subtitle timing.
This check uses the caller's reviewed ranges/crop, rather than trusting maps as
the content oracle. It reuses the independently tested frame/audio comparison.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path

SPEC = importlib.util.spec_from_file_location(
    'collection_content', Path(__file__).with_name('sdk-collection-acceptance-verify.py'))
content = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(content)


def verify(source, expected_sha256, directories):
    before = content.file_hash(source)
    content.require(before == expected_sha256, 'source_identity')
    profile = content.probe(source)
    summaries = []
    for directory in directories:
        receipt = content.document(directory / 'acceptance.json')
        request = content.document(directory / 'requests.json')['reviewed']
        content.require(receipt.get('complete') is True and receipt.get('source_count') == 1
                        and receipt.get('clips') == 15 and receipt.get('jobs') == 18
                        and receipt.get('artifacts') == 17 and receipt.get('lost_source_reply') is True
                        and receipt.get('lost_retry_reply') is True
                        and receipt.get('browser_review') is True, 'project_harness_receipt')
        browser = content.document(directory / 'browser-review.json')
        content.require(browser.get('complete') is True and browser.get('fragment_removed') is True
                        and browser.get('edited_ids') == ['clip-01', 'clip-15'], 'real_browser_review')
        outputs, clips = receipt['outputs'], content.selected(request)
        content.require(len(outputs) == len(clips) == 15
                        and [row['clip_id'] for row in outputs] == [row['id'] for row in clips]
                        and len({row['job_id'] for row in outputs}) == 15, 'reviewed_selection')
        observations = [content.check_output(source, profile, output, clip, request)
                        for output, clip in zip(outputs, clips)]
        sidecars = receipt['sidecars']
        content.require(len(sidecars) == 15 and [row['clip_id'] for row in sidecars] == [row['id'] for row in clips], 'sidecar_selection')
        for row in sidecars:
            content.require(content.file_hash(Path(row['path'])) == row['sha256'] and row['cue_count'] == 2, 'sidecar_bytes')
        correlations = [row['min_audio_correlation'] for row in observations if row['min_audio_correlation'] is not None]
        summaries.append({'consumer': directory.name, 'clips': 15, 'sidecars': 15,
                          'frame_samples': sum(row['frame_samples'] for row in observations),
                          'max_frame_mae': round(max(row['max_frame_mae'] for row in observations), 4),
                          'audio_samples': sum(row['audio_samples'] for row in observations),
                          'min_audio_correlation': round(min(correlations), 4) if correlations else None})
    content.require(content.file_hash(source) == before, 'source_changed')
    return {'schema_version': 1, 'passed': True, 'independent_source_content': True,
            'source_sha256': before, 'source_size_bytes': source.stat().st_size,
            'frame_mae_limit': content.FRAME_MAE_LIMIT,
            'audio_correlation_minimum': content.AUDIO_CORRELATION_MIN, 'consumers': summaries,
            'subtitle_fixture': 'Supplied timing text only; no transcription or speech-word accuracy claim.'}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--expected-source-sha256', required=True)
    parser.add_argument('--evidence', type=Path, required=True, action='append')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    output = args.output.absolute()
    content.require(not output.resolve().is_relative_to(Path(__file__).resolve().parents[1])
                    and not output.exists(), 'new_external_report_required')
    result = verify(args.source.absolute(), args.expected_source_sha256,
                    [path.absolute() for path in args.evidence])
    output.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    with output.open('x', encoding='utf-8') as stream:
        os.chmod(output, 0o600)
        json.dump(result, stream, indent=2)
        stream.write('\n')
    print(json.dumps(result))


if __name__ == '__main__':
    os.umask(0o077)
    main()
