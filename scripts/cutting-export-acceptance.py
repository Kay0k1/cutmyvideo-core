#!/usr/bin/env python3
"""Decode real HTTP cutting results from short and one-hour generated sources.

Use a disposable loopback API/worker and database, never production. Media stays
in --workspace, outside the repository. The JSON evidence contains no sessions
or database settings. Independent FFmpeg/ffprobe checks exercise MP4, copy and
MP3, selected audio, beginning/end, individual/batch and advertised range limits.
"""
import argparse
from array import array
import datetime
import hashlib
import http.cookiejar
import json
import math
from pathlib import Path
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request


def command(args, timeout=240):
    result = subprocess.run([str(v) for v in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f'{args[0]} exited {result.returncode}: {result.stderr.decode(errors="replace")[-1500:]}')
    return result.stdout


def ffmpeg(*args):
    return command(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-nostdin',
                    '-threads', '1', '-filter_threads', '1', *args])


def probe(path, packets=False):
    args = ['ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json']
    if packets:
        args += ['-show_packets', '-show_data_hash', 'sha256']
    return json.loads(command([*args, path]))


def digest(path):
    h = hashlib.sha256()
    with path.open('rb') as handle:
        for block in iter(lambda: handle.read(1 << 20), b''):
            h.update(block)
    return h.hexdigest()


def fixtures(workspace):
    result = {}
    for name, duration in [('short', 12), ('long', 3600)]:
        path = workspace / f'{name}.mp4'
        if not path.exists():
            # Two audio streams identify selection independently. The changing
            # chirp also detects timeline displacement, unlike a stationary tone.
            ffmpeg('-f', 'lavfi', '-i', f'color=red:size=160x96:rate=10:duration={duration}',
                   '-f', 'lavfi', '-i', f'sine=frequency=440:sample_rate=16000:duration={duration}',
                   '-f', 'lavfi', '-i', f'aevalsrc=sin(2*PI*(800*mod(t\,12)+20*mod(t\,12)*mod(t\,12)+2*floor(t/12)*mod(t\,12))):s=16000:d={duration}',
                   '-map', '0:v', '-map', '1:a', '-map', '2:a',
                   '-vf', f"drawbox=color=green:t=fill:enable='between(t,{duration/3},{duration*2/3})',drawbox=color=blue:t=fill:enable='gte(t,{duration*2/3})'",
                   '-c:v', 'libx264', '-threads', '1', '-preset', 'ultrafast', '-pix_fmt', 'yuv420p', '-g', '20', '-bf', '0',
                   '-c:a', 'aac', '-b:a', '24k', '-metadata:s:a:0', 'language=eng',
                   '-metadata:s:a:1', 'language=rus', '-disposition:a:0', 'default', '-disposition:a:1', '0',
                   '-movflags', '+faststart', '-y', path)
        result[name] = path
    return result


class Client:
    def __init__(self, url):
        self.url = url.rstrip('/')
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(self, path, body=None, headers=None, method=None, expected=200):
        req = urllib.request.Request(self.url + path, data=body, headers=headers or {}, method=method)
        try:
            response = self.opener.open(req, timeout=240)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            data = response.read()
            assert response.status == expected, (path, response.status, expected, data.decode(errors='replace'))
            return data

    def json(self, path, body=None, expected=200, key=None):
        headers = {} if body is None else {'Content-Type': 'application/json'}
        if key:
            headers['Idempotency-Key'] = key
        return json.loads(self.request(path, None if body is None else json.dumps(body).encode(), headers, expected=expected))

    def upload(self, path):
        boundary = 'cutmy-cutting-acceptance'
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{path.name}"\r\n'
                'Content-Type: video/mp4\r\n\r\n').encode() + path.read_bytes() + f'\r\n--{boundary}--\r\n'.encode()
        return json.loads(self.request('/api/v1/uploads', body,
                          {'Content-Type': f'multipart/form-data; boundary={boundary}'}, expected=201))

    def finish(self, job):
        deadline = time.monotonic() + 240
        states = [job['status']]
        while job['status'] not in ('succeeded', 'failed', 'cancelled'):
            assert time.monotonic() < deadline, ('job timeout', job['id'])
            time.sleep(.05)
            job = self.json('/api/v1/jobs/' + job['id'])
            if job['status'] != states[-1]:
                states.append(job['status'])
        assert job['status'] == 'succeeded', job
        return job, states


def pcm(path, index, at_ms, audio_only=False, source=False):
    values = array('f')
    begin = at_ms / 1000
    # Normalize presentation time, including codec pre-roll, before taking a
    # sample. This retains gaps rather than comparing demuxed packet order.
    origin = '' if source else ':first_pts=0'
    audio_filter = (f'aresample=async=1000:min_hard_comp=0.001{origin},'
                    f'atrim=start={begin}:end={begin+.4},asetpts=PTS-STARTPTS')
    clock = ['-copyts', '-start_at_zero'] if audio_only else ['-copyts']
    if source:
        clock += ['-ss', str(max(0, begin-.15))]
    values.frombytes(ffmpeg(*clock, '-i', path, '-map', f'0:{index}', '-af', audio_filter,
                            '-t', '0.4', '-ac', '1', '-ar', '16000', '-f', 'f32le', 'pipe:1'))
    return values


def alignment(source, path, index, source_at, output_at, audio_only=False, frame_ms=25):
    reference, actual = pcm(source, 2, source_at, source=True), pcm(path, index, output_at, audio_only)
    count = min(len(reference), len(actual))
    assert count >= 6000, ('short audio', count)
    positions = range(960, count - 960, 8)
    xx = sum(reference[i] ** 2 for i in positions)
    def score(lag):
        xy = sum(reference[i] * actual[i + lag] for i in positions)
        yy = sum(actual[i + lag] ** 2 for i in positions)
        return xy / math.sqrt(xx * yy)
    lag = max(range(-960, 961, 4), key=score)
    lag = max(range(max(-960, lag-4), min(960, lag+4)+1), key=score)
    correlation = score(lag)
    assert correlation > .98 and abs(lag / 16) <= frame_ms, ('audio timeline/selection mismatch', path, lag / 16, correlation)
    return {'correlation': correlation, 'lag_ms': lag / 16, 'tolerance_ms': frame_ms}


def colour(path, at):
    rgb = ffmpeg('-ss', str(at), '-i', path, '-frames:v', '1', '-vf', 'scale=1:1',
                 '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1')
    assert len(rgb) == 3, ('no decoded frame', path, at)
    return ['red', 'green', 'blue'][max(range(3), key=lambda i: rgb[i])]


def validate(source, path, requested, artifact, mode, container, input_packets):
    # Decoder errors are fatal; a successful probe alone is not acceptance.
    ffmpeg('-xerror', '-err_detect', 'explode', '-i', path, '-f', 'null', '-')
    info = probe(path, True)
    audio = next(s for s in info['streams'] if s['codec_type'] == 'audio')
    video = [s for s in info['streams'] if s['codec_type'] == 'video']
    assert len(video) == (0 if container == 'mp3' else 1), info['streams']
    assert len(info['streams']) == (1 if container == 'mp3' else 2), info['streams']
    assert audio.get('tags', {}).get('language') == ('rus' if container == 'mp4' else None), audio
    for stream in info['streams']:
        packets = [p for p in info['packets'] if p['stream_index'] == stream['index']]
        dts = [float(p['dts_time']) for p in packets if 'dts_time' in p]
        assert dts and all(a <= b for a, b in zip(dts, dts[1:])), ('nonmonotonic decode clock', path, stream['codec_type'])
        if stream['codec_type'] == 'video':
            assert abs(float(packets[0]['pts_time'])) <= .101, ('video start does not match reported source boundary', path, packets[0])
    duration = float(info['format']['duration']) * 1000
    start, end = artifact['actual_start_ms'], artifact['actual_end_ms']
    assert abs(end - start - duration) <= 2, (artifact, duration)
    if mode == 'accurate':
        assert start == requested['start_ms'] and abs(end - requested['end_ms']) <= 120, (requested, artifact)
    else:
        assert 0 <= requested['start_ms'] - start <= 2100, (requested, artifact)
        assert abs(end - requested['end_ms']) <= 120, (requested, artifact)
        for stream in info['streams']:
            output_hashes = [p['data_hash'] for p in info['packets'] if p['stream_index'] == stream['index']]
            source_index = 0 if stream['codec_type'] == 'video' else 2
            retained = {p['data_hash'] for p in input_packets if p['stream_index'] == source_index}
            assert output_hashes and all(h in retained for h in output_hashes), ('copy changed selected packet payload', path, stream['codec_type'])
    markers = []
    if video:
        source_duration = float(probe(source)['format']['duration'])
        for at in [.3, duration / 1000 - .3]:
            phase = (start / 1000 + at) / source_duration
            expected = 'red' if phase < 1/3 else 'green' if phase < 2/3 else 'blue'
            observed = colour(path, at)
            assert observed == expected, (path, at, expected, observed)
            markers.append({'output_seconds': at, 'colour': observed})
    # Low-rate MPEG-2 MP3 uses 576 samples per frame; MPEG-1 uses 1152.
    # An audio-only output has no video clock and retains codec frame granularity.
    sample_rate = int(audio['sample_rate'])
    frame_ms = max(25, (576 if sample_rate < 32000 else 1152) * 1000 / sample_rate) if container == 'mp3' else 25
    audio_check = alignment(source, path, audio['index'], start + 500, 500, container == 'mp3', frame_ms)
    return {'bytes': path.stat().st_size, 'sha256': digest(path), 'requested': requested,
            'actual_start_ms': start, 'actual_end_ms': end, 'decoded_duration_ms': duration,
            'streams': [{'type': s['codec_type'], 'codec': s['codec_name']} for s in info['streams']],
            'video_markers': markers, 'audio_alignment': audio_check,
            'copied_packet_payloads_preserved': mode == 'copy'}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--url', required=True)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--workspace', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    a = parser.parse_args()
    if urllib.parse.urlsplit(a.url).hostname not in ('127.0.0.1', 'localhost', '::1'):
        parser.error('--url must be a disposable loopback API')
    a.workspace.mkdir(parents=True, exist_ok=True)
    sources = fixtures(a.workspace)
    client = Client(a.url)
    session = client.json('/api/v1/session')
    limit = session['limits']['max_ranges']
    records, controls = [], []
    owned_sources = []
    try:
        for name, path in sources.items():
            client = Client(a.url)
            client.json('/api/v1/session')
            source = client.upload(path)
            owned_sources.append((client, source['id']))
            duration = source['duration_ms']
            assert duration >= (3599900 if name == 'long' else 11900), source
            packets = probe(path, True)['packets']
            if name == 'short':
                try:
                    alignment(path, path, 1, 500, 500)
                except AssertionError as error:
                    if error.args[0][0] != 'audio timeline/selection mismatch':
                        raise
                    controls.append({'name': 'unselected_audio_negative_control', 'rejected': True,
                                     'correlation': error.args[0][3], 'lag_ms': error.args[0][2]})
                else:
                    raise AssertionError('independent audio checker accepted the unselected 440Hz track')
            boundaries = [{'start_ms': 0, 'end_ms': 2000, 'label': 'Beginning'},
                          {'start_ms': duration - 2500, 'end_ms': duration, 'label': 'Ending'}]
            for mode, container in [('accurate', 'mp4'), ('copy', 'mp4'), ('accurate', 'mp3')]:
                for ranges in [[boundaries[0]], [boundaries[1]], list(reversed(boundaries))]:
                    case = f'{name}-{mode}-{container}-' + ('batch' if len(ranges) > 1 else ranges[0]['label'].lower())
                    body = {'source_id': source['id'], 'ranges': ranges, 'format': container,
                            'quality': 'best', 'cut_mode': mode, 'audio_track': 2}
                    job = client.json('/api/v1/jobs', body, expected=202, key=case)
                    replay = client.json('/api/v1/jobs', body, expected=202, key=case)
                    assert replay['id'] == job['id'], 'unsafe duplicate submission'
                    job, states = client.finish(job)
                    outputs = []
                    for i, item in enumerate(job['items']):
                        artifact = item['artifact']
                        target = a.workspace / f'{case}-{i+1}.{container}'
                        target.write_bytes(client.request(artifact['download_url']))
                        outputs.append(validate(path, target, ranges[i], artifact, mode, container, packets))
                        if name == 'long' and mode == 'accurate' and container == 'mp4' and ranges[i]['label'] == 'Ending' and len(ranges) == 1:
                            try:
                                alignment(path, target, 1, artifact['actual_start_ms'] - 12000 + 500, 500)
                            except AssertionError as error:
                                if error.args[0][0] != 'audio timeline/selection mismatch':
                                    raise
                                controls.append({'name': 'twelve_second_displacement_negative_control', 'rejected': True,
                                                 'correlation': error.args[0][3], 'lag_ms': error.args[0][2]})
                            else:
                                raise AssertionError('independent audio checker accepted a twelve-second displacement')
                    record = {'case': case, 'states': states, 'idempotent_replay': True, 'outputs': outputs}
                    records.append(record)
                    print(json.dumps({'case': case, 'outputs': len(outputs), 'passed': True}), flush=True)
            if name == 'short':
                ranges = [dict(boundaries[i % 2], label=f'Clip {i+1}') for i in range(limit)]
                body = {'source_id': source['id'], 'ranges': ranges, 'format': 'mp4', 'quality': 'best', 'cut_mode': 'copy', 'audio_track': 2}
                refused = client.json('/api/v1/jobs', dict(body, ranges=ranges + [boundaries[0]]), expected=400)
                assert refused['error']['code'] == 'invalid_export', refused
                job, states = client.finish(client.json('/api/v1/jobs', body, expected=202, key='maximum-ranges'))
                assert len(job['items']) == limit, job
                maximum_outputs = []
                for i, item in enumerate(job['items']):
                    target = a.workspace / f'max-ranges-{i+1}.mp4'
                    target.write_bytes(client.request(item['artifact']['download_url']))
                    maximum_outputs.append(validate(path, target, ranges[i], item['artifact'], 'copy', 'mp4', packets))
                controls.append({'name': 'advertised_range_limit', 'limit': limit, 'completed_downloads_independently_decoded': limit,
                                 'limit_plus_one_status': 400, 'limit_plus_one_code': refused['error']['code'], 'states': states,
                                 'outputs': maximum_outputs})
        a.report.parent.mkdir(parents=True, exist_ok=True)
        a.report.write_text(json.dumps({'completed_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'binary_sha256': digest(a.binary), 'binary_version': json.loads(command([a.binary, 'version'])),
            'tools': {tool: command([tool, '-version']).decode().splitlines()[0] for tool in ('ffmpeg', 'ffprobe')},
            'fixtures': {name: {'bytes': path.stat().st_size, 'sha256': digest(path), 'duration_ms': round(float(probe(path)['format']['duration']) * 1000)} for name, path in sources.items()},
            'scope': 'Disposable HTTP upload, immutable/idempotent job acceptance, worker completion, private full-byte download, independent decoding/content/audio sync and selected copy packet verification; synthetic fixtures, no external sites.',
            'cases': records, 'controls': controls}, indent=2) + '\n')
    finally:
        for source_client, source_id in owned_sources:
            source_client.request('/api/v1/sources/' + source_id, method='DELETE', expected=204)


if __name__ == '__main__':
    main()
