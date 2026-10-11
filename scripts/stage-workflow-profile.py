#!/usr/bin/env python3
"""Measure owner HTTP workflows through receipt of all output bytes.

Run against disposable servers/databases with a pre-generated local fixture.
Each sample uploads a fresh source so preview cache hits cannot mask work.
Paired mode alternates the first endpoint within each pair (AB, BA, AB, ...).
Keep --media-dir outside the repository when saving the JSON as evidence.
"""
import argparse
import datetime
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import platform
import statistics
import subprocess
import time
import urllib.request


def command_metadata(args):
    result = subprocess.run([str(value) for value in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=30, check=True)
    return result.stdout.decode().strip()


def environment_metadata():
    cpu = {}
    path = Path('/proc/cpuinfo')
    if path.exists():
        first = path.read_text().split('\n\n', 1)[0]
        for line in first.splitlines():
            key, _, value = line.partition(':')
            if key.strip() in ('model name', 'cpu cores', 'siblings'):
                cpu[key.strip()] = value.strip()
    return {'platform': platform.platform(), 'logical_cpus': os.cpu_count(),
            'cpu': cpu, 'cpu_affinity_count': len(os.sched_getaffinity(0)) if hasattr(os, 'sched_getaffinity') else None,
            'cgroup_cpu_max': Path('/sys/fs/cgroup/cpu.max').read_text().strip() if Path('/sys/fs/cgroup/cpu.max').exists() else None,
            'cgroup_memory_max': Path('/sys/fs/cgroup/memory.max').read_text().strip() if Path('/sys/fs/cgroup/memory.max').exists() else None,
            'load_average_at_start': list(os.getloadavg()),
            'tools': {tool: command_metadata([tool, '-version']).splitlines()[0]
                      for tool in ('ffmpeg', 'ffprobe')},
            'started_utc': datetime.datetime.now(datetime.timezone.utc).isoformat()}


def binary_metadata(path):
    if path is None:
        return None
    return {'sha256': hashlib.sha256(path.read_bytes()).hexdigest(),
            'version': json.loads(command_metadata([path, 'version']))}


def run_sample(url, blob, sample, media_path):
    opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def request(path, body=None, headers=None):
        with opener.open(urllib.request.Request(url + path, data=body, headers=headers or {}), timeout=180) as response:
            return response.read(), dict(response.headers)

    def json_request(path, body=None):
        raw, _ = request(path, None if body is None else json.dumps(body).encode(),
                         {} if body is None else {'Content-Type': 'application/json'})
        return json.loads(raw)

    json_request('/api/v1/session')
    boundary = 'cutmy-stage-measurement'
    upload = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="fixture.mp4"\r\n'
              'Content-Type: video/mp4\r\n\r\n').encode() + blob + f'\r\n--{boundary}--\r\n'.encode()
    begun = time.monotonic()
    raw, _ = request('/api/v1/uploads', upload, {'Content-Type': f'multipart/form-data; boundary={boundary}'})
    source = json.loads(raw)
    uploaded = time.monotonic()
    preview, preview_headers = request('/api/v1/sources/' + source['id'] + '/preview?start_ms=0')
    preview_done = time.monotonic()
    preview_cache = preview_headers.get('X-Preview-Cache')
    if preview_cache != 'miss':
        raise RuntimeError('First preview unexpectedly reused a cache entry: ' + str(preview_cache))
    job = json_request('/api/v1/jobs', {
        'source_id': source['id'], 'ranges': [{'start_ms': 3000, 'end_ms': 11000, 'label': 'First'}],
        'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate'})
    accepted = time.monotonic()
    polls = 0
    while job['status'] not in ('succeeded', 'failed', 'cancelled'):
        time.sleep(.05)
        job = json_request('/api/v1/jobs/' + job['id'])
        polls += 1
    if job['status'] != 'succeeded':
        raise RuntimeError(job)
    ready = time.monotonic()
    artifact = job['items'][0]['artifact']
    output, _ = request(artifact['download_url'])
    received = time.monotonic()
    record = {
        'sample': sample, 'upload_inspect_ms': (uploaded - begun) * 1000,
        'first_preview_request_to_received_ms': (preview_done - uploaded) * 1000,
        'upload_to_preview_received_ms': (preview_done - begun) * 1000,
        'job_accept_ms': (accepted - preview_done) * 1000,
        'job_request_to_ready_ms': (ready - preview_done) * 1000,
        'job_request_to_received_ms': (received - preview_done) * 1000,
        'upload_to_export_received_ms': (received - begun) * 1000,
        'download_ms': (received - ready) * 1000,
        'preview_bytes': len(preview), 'artifact_bytes': len(output), 'polls': polls,
        'preview_cache': preview_cache, 'load_average_after': list(os.getloadavg()),
        'artifact_sha256': hashlib.sha256(output).hexdigest()}
    media_path.parent.mkdir(parents=True, exist_ok=True)
    media_path.write_bytes(output)
    delete = urllib.request.Request(url + '/api/v1/sources/' + source['id'], method='DELETE')
    with opener.open(delete, timeout=30) as response:
        response.read()
    print(json.dumps(record), flush=True)
    return record


def summary(records):
    return {key: statistics.median(row[key] for row in records)
            for key in records[0] if key.endswith('_ms')}


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--url', required=True)
    p.add_argument('--fixture', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--revision', required=True)
    p.add_argument('--samples', type=int, default=5)
    p.add_argument('--binary', type=Path)
    p.add_argument('--media-dir', type=Path)
    p.add_argument('--paired-url')
    p.add_argument('--paired-revision')
    p.add_argument('--paired-binary', type=Path)
    a = p.parse_args()
    if a.samples < 1:
        p.error('--samples must be positive')
    if a.paired_url and not a.paired_revision:
        p.error('--paired-url requires --paired-revision')
    blob = a.fixture.read_bytes()
    metadata = environment_metadata()
    media_dir = a.media_dir or a.output.parent
    groups = [('baseline', a.url, a.revision, a.binary)] if a.paired_url else [('single', a.url, a.revision, a.binary)]
    if a.paired_url:
        groups.append(('candidate', a.paired_url, a.paired_revision, a.paired_binary))
    records = {name: [] for name, _, _, _ in groups}
    order = []
    for n in range(a.samples):
        sequence = groups if n % 2 == 0 else list(reversed(groups))
        for name, url, _, _ in sequence:
            order.append({'sample': n + 1, 'endpoint': name})
            media_path = media_dir / f'{a.output.stem}-{name}-sample-{n+1}.mp4'
            records[name].append(run_sample(url, blob, n+1, media_path))
    base = {'fixture_sha256': hashlib.sha256(blob).hexdigest(), 'fixture_bytes': len(blob),
            'environment': metadata, 'order': order,
            'finished_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
            'scope': 'Fresh owner/session and source for each sample; timed upload+inspection, first preview through all preview bytes, job acceptance/processing and all output bytes. Session creation and source cleanup occur outside the timed interval. HTTP polling interval is 50ms.'}
    if a.paired_url:
        base['endpoints'] = {name: {'revision': revision, 'binary': binary_metadata(binary),
                                   'samples': records[name], 'median': summary(records[name])}
                             for name, _, revision, binary in groups}
        baseline = base['endpoints']['baseline']['median']
        candidate = base['endpoints']['candidate']['median']
        base['candidate_minus_baseline_median_ms'] = {key: candidate[key]-baseline[key] for key in baseline}
        base['paired_candidate_minus_baseline_ms'] = {
            key: [records['candidate'][n][key]-records['baseline'][n][key] for n in range(a.samples)]
            for key in baseline}
    else:
        base.update({'revision': a.revision, 'binary': binary_metadata(a.binary),
                     'samples': records['single'], 'median': summary(records['single'])})
    a.output.parent.mkdir(parents=True, exist_ok=True)
    a.output.write_text(json.dumps(base, indent=2) + '\n')


if __name__ == '__main__':
    main()
