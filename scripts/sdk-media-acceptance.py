#!/usr/bin/env python3
"""Exercise actual media features from an installed wheel; Go checks decoded outputs."""
import json
import os
from pathlib import Path
import sys
import cutmyvideo
from cutmyvideo import CutmyClient, LocalCutmy, ready_artifacts

assert Path(cutmyvideo.__file__).resolve().is_relative_to(Path(sys.prefix).resolve())
mode, source, replacement, destination, base_url = sys.argv[1:]
output = Path(destination)
client = LocalCutmy(os.environ['CUTMY_SDK_BINARY'], threads=1) if mode == 'local' else CutmyClient(base_url, request_timeout=30)
source_id = source if mode == 'local' else client.upload_source(source)['id']
replacement_id = replacement if mode == 'local' else client.upload_source(replacement)['id']
silence = client.analyze_silence(source_id, {'threshold_db': -45, 'min_silence_ms': 200})
video = client.analyze_video(source_id, {'scene_threshold': .01, 'min_black_ms': 200})
loudness = client.analyze_loudness(source_id)
cut = client.import_cut_list({'format': 'csv', 'text': '0,1,Привет\n2,,Ending\n'})
assert cut['entries'][1]['end_ms'] is None and cut['entries'][0]['label'] == 'Привет'
serialized = client.export_cut_list({'format': 'csv', 'cut_list': cut})
assert client.import_cut_list({'format': 'csv', 'text': serialized['text']}) == cut
resolved = {'version': 1, 'entries': [{'start_ms': 0, 'end_ms': 1000, 'label': 'Привет'}, {'start_ms': 2000, 'end_ms': 4000, 'label': 'Ending'}]}
chapters = client.export_cut_list({'format': 'ffmetadata', 'cut_list': resolved})
assert client.import_cut_list({'format': 'ffmetadata', 'text': chapters['text']}) == resolved
if mode == 'local':
    plan = client.plan_cut_list({'format': 'csv', 'text': serialized['text']}, source, duration_ms=4000)
    assert plan['ranges'][1]['end_ms'] == 4000
    silence_plan = client.silence_plan(source, {'threshold_db': -45, 'min_silence_ms': 200})
    assert silence_plan['ranges']
settings = [
    ('normalize', {'audio_normalize': {'integrated_lufs': -16, 'true_peak_dbtp': -1, 'loudness_range_lu': 11}}),
    ('canvas', {'canvas': {'width': 96, 'height': 160, 'fit': 'contain', 'background': '#0000ff'}}),
    ('replace', {'audio_replace': {('input' if mode == 'local' else 'source_id'): replacement_id, 'offset_ms': 1000}}),
]
outputs = []
for name, edit in settings:
    options = {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate', **edit}
    ranges = [{'start_ms': 0, 'end_ms': 4000, 'label': 'Whole'}]
    path = output / (name + '.mp4')
    if mode == 'local':
        plan = {'version': 1, 'input': source, 'ranges': ranges, 'output_mode': 'joined', 'options': options}
        assert client.preflight(plan, path, timeout=30)['valid']
        artifact = client.execute_plan(plan, path, timeout=45)['outputs'][0]
        path = Path(artifact['path'])
    else:
        request = {'source_id': source_id, 'ranges': ranges, 'output_mode': 'joined', **options}
        assert client.preflight(request)['valid']
        job = client.create_job(request, idempotency_key='media-python-' + name)
        artifact = ready_artifacts(client.wait_for_job(job['id'], timeout=45, poll_interval=1))[0]
        client.download(artifact, path, max_bytes=16 << 20)
    outputs.append({'name': name, 'path': str(path), 'artifact': artifact})
segments = outputs[0]['artifact']['segments']
segment_text = client.export_cut_list({'format': 'csv', 'segments': segments})
assert client.import_cut_list({'format': 'csv', 'text': segment_text['text']})['entries'][0]['end_ms'] == 4000
print(json.dumps({'mode': mode, 'silence': silence, 'video': video, 'loudness': loudness, 'outputs': outputs}))
