#!/usr/bin/env python3
"""Frozen, paired CLI/embedding/HLS measurements for disposable local media.

The freeze command creates bounded fixtures and an immutable workload manifest.
Run never builds tools, regenerates fixtures, drops the OS cache, or contacts a
public service. Verification runs after the timed process and outside its tool
counts. Both sides use the same wrappers and sampling overhead.
"""
import argparse
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import signal
import statistics
import subprocess
import threading
import time


SCHEMA = 1
RESERVE_BYTES = 3 * 1024 ** 3
SAMPLE_SECONDS = .02
PROFILES = {
    'accurate-mp4': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'accurate'},
    'copy-mp4': {'format': 'mp4', 'quality': 'best', 'cut_mode': 'copy'},
    'copy-mkv': {'format': 'mkv', 'quality': 'best', 'cut_mode': 'copy'},
    'mp3': {'format': 'mp3', 'quality': 'best', 'cut_mode': 'accurate'},
}


def sha256(path):
    value = hashlib.sha256()
    with Path(path).open('rb') as handle:
        for block in iter(lambda: handle.read(1024 ** 2), b''):
            value.update(block)
    return value.hexdigest()


def write_json(path, data, exclusive=False):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open('x' if exclusive else 'w') as handle:
        json.dump(data, handle, indent=2, sort_keys=True)
        handle.write('\n')


def command(args, timeout=180, cwd=None):
    result = subprocess.run([str(value) for value in args], stdout=subprocess.PIPE,
                            stderr=subprocess.PIPE, timeout=timeout, cwd=cwd)
    if result.returncode:
        raise RuntimeError(f'{args[0]} exited {result.returncode}: '
                           + result.stderr.decode(errors='replace')[-2000:])
    return result.stdout


def ensure_space(path, additional=0):
    free = shutil.disk_usage(path).free
    if free - additional < RESERVE_BYTES:
        raise RuntimeError(f'Disk reserve: {free} free bytes, {additional} planned; '
                           f'keep at least {RESERVE_BYTES} free bytes')


def metadata(path):
    path = Path(path).resolve(strict=True)
    return {'path': str(path), 'sha256': sha256(path), 'bytes': path.stat().st_size}


def build_source_catalog(checkout):
    paths = [*checkout.rglob('*.go'), checkout / 'go.mod', checkout / 'go.sum',
             *(checkout / 'internal/app/migrations').glob('*.sql'),
             *(checkout / 'internal/app/migrations').glob('*.json')]
    return {str(path.relative_to(checkout)): sha256(path) for path in sorted(paths)
            if path.is_file() and '.git' not in path.parts and 'vendor' not in path.parts}


def tools_metadata():
    result = {}
    for name in ('ffmpeg', 'ffprobe'):
        path = shutil.which(name)
        if not path:
            raise RuntimeError(f'{name} is required')
        result[name] = {**metadata(path), 'version': command([path, '-version']).decode().splitlines()[0]}
    return result


def ranges(count, duration, pattern='mixed'):
    """Fixed order includes backward seeks, overlaps, repeats and distant cuts."""
    if duration >= 3600_000:
        starts = [2400, 3200, 2400, 1792400, 3482400, 1212400, 5600, 9600]
        starts += [((i * 137 + 301) % 359) * 10000 + 2400 for i in range(24)]
    else:
        starts = [2400, 3200, 2400, 24200, 31200, 18200, 5600, 9600]
        starts += [((i * 7 + 3) % 31) * 1000 + 200 for i in range(24)]
    if pattern == 'hls-overlap':
        starts = [1450, 1950, 1450, 4450, 4950, 4450, 1950, 4950]
    elif pattern == 'hls-close':
        starts = [1450, 2550, 3650, 4750, 5850, 6950, 8050, 9150]
    return [{'label': f'piece-{i+1:02}', 'start_ms': start, 'end_ms': start + 1100}
            for i, start in enumerate(starts[:count])]


def scenarios():
    rows = []
    for count in (1, 2, 10, 20, 32):
        for profile, options in PROFILES.items():
            for mode in ('separate', 'joined'):
                ident = f'local-{profile}-{mode}-{count:02}'
                rows.append({'id': ident, 'kind': 'cli', 'fixture': 'long', 'count': count,
                             'ranges': ranges(count, 3600_000), 'options': options,
                             'output_mode': mode, 'final_key': count in (1, 32)})
    for profile, options in PROFILES.items():
        for mode in ('separate', 'joined'):
            rows.append({'id': f'complex-{profile}-{mode}-20', 'kind': 'cli',
                         'fixture': 'complex', 'count': 20, 'ranges': ranges(20, 36000),
                         'options': options, 'output_mode': mode,
                         'final_key': profile == 'accurate-mp4'})
    for fixture, profile, extra in [
            ('vfr', 'accurate-mp4', {}), ('rotated', 'accurate-mp4', {}),
            ('multi', 'accurate-mp4', {'audio_track': 2}),
            ('multi', 'copy-mkv', {'audio_track': 2, 'subtitle_mode': 'preserve'}),
            ('multi', 'accurate-mp4', {'audio_track': 2,
                                     'crop': {'x': 0, 'y': 0, 'width': .5, 'height': 1}})]:
        suffix = 'crop' if 'crop' in extra else 'tracks' if extra else 'cadence'
        rows.append({'id': f'acceptance-{fixture}-{profile}-{suffix}', 'kind': 'cli',
                     'fixture': fixture, 'count': 2, 'ranges': [
                         {'label': 'Blue first', 'start_ms': 8500, 'end_ms': 10500},
                         {'label': 'Red second', 'start_ms': 1500, 'end_ms': 3500}],
                     'options': {**PROFILES[profile], **extra}, 'output_mode': 'joined',
                     'final_key': False})
    for profile in ('accurate-mp4', 'copy-mkv'):
        for count in (1, 32):
            rows.append({'id': f'embedded-{profile}-{count:02}', 'kind': 'embedded',
                         'fixture': 'long', 'count': count, 'ranges': ranges(count, 3600_000),
                         'options': PROFILES[profile], 'output_mode': 'joined',
                         'final_key': True})
    for profile, mode in (('accurate-mp4', 'separate'), ('copy-mkv', 'joined')):
        for phase in ('cold', 'warm', 'straggler', 'overlap'):
            rows.append({'id': f'hls-{profile}-{mode}-{phase}', 'kind': 'hls',
                         'fixture': 'hls', 'count': 8,
                         'ranges': ranges(8, 12000, 'hls-overlap' if phase in ('warm', 'overlap') else 'hls-close'),
                         'options': PROFILES[profile], 'output_mode': mode,
                         'network': {'base_delay_ms': 15, 'straggler_path': 'segment-001.ts',
                                     'straggler_delay_ms': 250 if phase == 'straggler' else 0},
                         'warmup': phase == 'warm', 'final_key': phase in ('straggler', 'overlap')})
    return rows


def server_scenarios():
    return [{'id': f'server-{profile}-{owners}', 'profile': profile,
             'owners': owners, 'jobs_per_owner': 4, 'cuts_per_job': 2,
             'arrival': 'simultaneous barrier after distinct-owner upload',
             'worker_concurrency': 2, 'ffmpeg_threads': 2,
             'measure': ['accepted_to_all_downloads', 'queue_wait', 'throughput',
                         'api_and_worker_cgroup_cpu_memory_io', 'actual_download_bytes'],
             'fixture': 'complex', 'output_mode': 'separate',
             'ranges': [{'label': 'first', 'start_ms': 2000, 'end_ms': 3100},
                        {'label': 'second', 'start_ms': 24000, 'end_ms': 25100}]}
            for profile in ('accurate-mp4', 'copy-mp4', 'mp3') for owners in (1, 4)]


def freeze(args):
    corpus = args.corpus.resolve()
    corpus.mkdir(parents=True, exist_ok=True)
    ensure_space(corpus, 128 * 1024 ** 2)
    tool = tools_metadata()
    ffmpeg = tool['ffmpeg']['path']
    ordinary = args.acceptance / 'ordinary.mp4'
    long = corpus / 'hour-ordinary.mkv'
    if long.exists():
        raise RuntimeError('Use a new corpus directory; freeze does not reuse unverified generated files')
    long_cmd = [ffmpeg, '-hide_banner', '-loglevel', 'error', '-nostdin',
                '-stream_loop', '299', '-i', str(ordinary.resolve()), '-map', '0:v:0',
                '-map', '0:a:0', '-t', '3600', '-c', 'copy', str(long)]
    command(long_cmd)
    hls = corpus / 'hls'
    hls.mkdir()
    hls_cmd = [ffmpeg, '-hide_banner', '-loglevel', 'error', '-nostdin', '-i',
               str(ordinary.resolve()), '-map', '0:v:0', '-map', '0:a:0', '-c', 'copy',
               '-f', 'hls', '-hls_time', '1', '-hls_playlist_type', 'vod',
               '-hls_segment_filename', str(hls / 'segment-%03d.ts'), str(hls / 'index.m3u8')]
    command(hls_cmd)
    fixtures = {'long': metadata(long), 'complex': metadata(args.complex)}
    fixtures['long']['generation_command'] = long_cmd
    fixtures['long']['parent'] = metadata(ordinary)
    for name in ('vfr', 'rotated', 'multi'):
        fixtures[name] = metadata(args.acceptance / f'{name}.mp4')
    fixtures['hls'] = {'path': str(hls.resolve()),
                       'files': [metadata(path) for path in sorted(hls.iterdir())],
                       'generation_command': hls_cmd, 'parent': metadata(ordinary)}
    for name, item in fixtures.items():
        if name != 'hls':
            item['probe'] = json.loads(command([tool['ffprobe']['path'], '-v', 'error',
                                                '-show_format', '-show_streams', '-of', 'json', item['path']]))
    baseline = {**metadata(args.baseline),
                'revision': args.baseline_revision,
                'version': json.loads(command([args.baseline, 'version']))}
    manifest = {'schema': SCHEMA, 'frozen_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                'baseline': baseline, 'tools': tool, 'fixtures': fixtures,
                'settings': {'threads': 2, 'profile': 'fast', 'sample_seconds': SAMPLE_SECONDS,
                             'disk_reserve_bytes': RESERVE_BYTES, 'sample_timeout_seconds': 180},
                'pair_protocol': {'exploratory_pairs': 3, 'final_key_pairs': 5,
                                  'ordering': 'AB, BA, AB, BA, AB; fixed manifest scenario order',
                                  'cache': 'warm OS cache, fresh operation workspace/owner; no cache dropping',
                                  'timing': 'single owned workload; no overlapping tests/builds/media tasks'},
                'verification': {'every_run': 'result order/bounds and independently probed/decoded outputs',
                                 'comparison': 'decoded hashes + normalized public manifests; output bytes recorded separately',
                                 'cleanup': 'delete only owned verified media after retaining fingerprints; keep failures'},
                'scenarios': scenarios(), 'server_scenarios': server_scenarios(),
                'limitations': ['No public-platform/network SLA or competitor speed claim.',
                                'HLS uses a fixed HTTPS test transport, not production network bypass.',
                                'Sampled tree RSS can double-count shared pages and miss transient peaks.',
                                'wait4 max RSS is the largest waited process peak, not simultaneous tree RSS.',
                                'Sampled process I/O totals are lower bounds; wait4 block counters are counts, not bytes.',
                                'Warm HLS operation time excludes prefill; outer CPU/RSS/I/O include prefill.']}
    write_json(args.manifest, manifest, exclusive=True)
    args.manifest.with_suffix('.sha256').write_text(sha256(args.manifest) + '\n')
    ensure_space(corpus)
    print(json.dumps({'manifest': str(args.manifest.resolve()), 'sha256': sha256(args.manifest),
                      'scenarios': len(manifest['scenarios']), 'server_scenarios': len(manifest['server_scenarios'])}))


def load_manifest(path):
    expected = path.with_suffix('.sha256').read_text().strip()
    if expected != sha256(path):
        raise RuntimeError('Frozen manifest digest mismatch')
    manifest = json.loads(path.read_text())
    if manifest['schema'] != SCHEMA or manifest['scenarios'] != scenarios():
        raise RuntimeError('Frozen workload does not match this harness; use its recorded source version')
    for item in [manifest['baseline'], *manifest['tools'].values()]:
        if sha256(item['path']) != item['sha256']:
            raise RuntimeError('Binary/tool provenance changed: ' + item['path'])
    for fixture in manifest['fixtures'].values():
        for item in fixture.get('files', [fixture]):
            if sha256(item['path']) != item['sha256']:
                raise RuntimeError('Fixture provenance changed: ' + item['path'])
    return manifest


def process_snapshot(pid):
    values, pending, seen = {}, [pid], set()
    while pending:
        current = pending.pop()
        if current in seen:
            continue
        seen.add(current)
        base = Path('/proc') / str(current)
        try:
            stat = (base / 'stat').read_text().rsplit(')', 1)[1].split()
            io = {line.split(':')[0]: int(line.split(':')[1]) for line in (base / 'io').read_text().splitlines()}
            rss = 0
            for line in (base / 'status').read_text().splitlines():
                if line.startswith('VmRSS:'):
                    rss = int(line.split()[1]) * 1024
            values[(current, int(stat[19]))] = {'rss': rss, **io}
            for task in (base / 'task').iterdir():
                pending.extend(int(child) for child in (task / 'children').read_text().split())
        except (OSError, ValueError, IndexError):
            continue
    return values


def file_bytes(path):
    return sum(file.stat().st_size for file in Path(path).rglob('*') if file.is_file())


def measured_process(args, env, case, timeout_seconds=180, termination_grace_seconds=5):
    started = time.perf_counter()
    observed, peak_rss, peak_work = {}, 0, 0
    stop, timed_out = threading.Event(), threading.Event()
    termination_signals = []
    with (case / 'stdout.txt').open('wb') as out, (case / 'stderr.txt').open('wb') as err:
        process = subprocess.Popen([str(value) for value in args], env=env, stdout=out, stderr=err,
                                   start_new_session=True)
        observed.update(process_snapshot(process.pid))

        def terminate_owned(signum):
            groups = set()
            owned_pids = {pid for pid, _ in observed}
            # FFmpeg uses its own process group. A measured parent's group
            # alone cannot terminate those descendants. Never signal a stale
            # PID that has since been reused by unrelated work.
            for pid, start_ticks in tuple(observed):
                try:
                    fields = (Path('/proc') / str(pid) / 'stat').read_text().rsplit(')', 1)[1].split()
                    if int(fields[19]) == start_ticks:
                        group = os.getpgid(pid)
                        if group == process.pid or group in owned_pids:
                            groups.add(group)
                except (OSError, ValueError, IndexError):
                    continue
            for group in groups:
                try:
                    os.killpg(group, signum)
                except ProcessLookupError:
                    continue
                termination_signals.append({'group': group, 'signal': int(signum),
                                            'elapsed_seconds': time.perf_counter() - started})

        def observe():
            nonlocal peak_rss, peak_work
            while not stop.is_set():
                elapsed = time.perf_counter() - started
                if elapsed > timeout_seconds and not timed_out.is_set():
                    timed_out.set()
                    terminate_owned(signal.SIGTERM)
                if elapsed > timeout_seconds + termination_grace_seconds:
                    terminate_owned(signal.SIGKILL)
                    return
                now = process_snapshot(process.pid)
                peak_rss = max(peak_rss, sum(value['rss'] for value in now.values()))
                for key, value in now.items():
                    previous = observed.setdefault(key, {})
                    for field, count in value.items():
                        previous[field] = max(previous.get(field, 0), count)
                try:
                    peak_work = max(peak_work, file_bytes(case / 'media'))
                except OSError:
                    pass
                stop.wait(SAMPLE_SECONDS)
        observer = threading.Thread(target=observe, daemon=True)
        observer.start()
        try:
            # Blocking wait records completion immediately; the observer's 20ms
            # interval must not quantize short singleton wall measurements.
            _, status, usage = os.wait4(process.pid, 0)
            wall = time.perf_counter() - started
            process.returncode = os.waitstatus_to_exitcode(status)
        finally:
            stop.set()
            observer.join()
            if process.returncode is None:
                # An interrupted measurement must not block on a producer
                # that ignores TERM while its observer has already stopped.
                terminate_owned(signal.SIGKILL)
                _, status, _ = os.wait4(process.pid, 0)
                process.returncode = os.waitstatus_to_exitcode(status)
            if timed_out.is_set():
                # The parent may honor TERM before the grace ends, while an
                # observed child in another group ignores it. Stop that child
                # before returning the timeout failure to the coordinator.
                terminate_owned(signal.SIGKILL)
                write_json(case / 'timeout.json', {'timed_out': True,
                           'timeout_seconds': timeout_seconds,
                           'termination_grace_seconds': termination_grace_seconds,
                           'elapsed_seconds': time.perf_counter() - started,
                           'exit_code': process.returncode, 'signals': termination_signals})
    if timed_out.is_set():
        raise RuntimeError(f'Timed sample exceeded {timeout_seconds} seconds; owned groups terminated with '
                           f'TERM then KILL after at most {termination_grace_seconds} seconds grace')
    if process.returncode:
        raise RuntimeError(f'{case}: exited {process.returncode}: ' + (case / 'stderr.txt').read_text()[-2000:])
    return {'wall_seconds': wall, 'wait4_user_seconds': usage.ru_utime,
            'wait4_system_seconds': usage.ru_stime, 'wait4_cpu_seconds': usage.ru_utime + usage.ru_stime,
            'wait4_maxrss_bytes': usage.ru_maxrss * 1024, 'wait4_input_blocks': usage.ru_inblock,
            'wait4_output_blocks': usage.ru_oublock, 'wait4_minor_faults': usage.ru_minflt,
            'wait4_major_faults': usage.ru_majflt, 'sampled_tree_peak_rss_bytes': peak_rss,
            'sampled_peak_media_workspace_bytes': peak_work,
            **{'sampled_process_' + field: sum(value.get(field, 0) for value in observed.values())
               for field in ('read_bytes', 'write_bytes', 'rchar', 'wchar')},
            'sampled_process_lifetimes': len(observed)}


def create_wrappers(case, tool_metadata):
    wrappers = case / 'tools'
    wrappers.mkdir()
    for name, item in tool_metadata.items():
        path = wrappers / name
        path.write_text('#!/bin/sh\n' + f'printf \'%s\\n\' "$$" >> {shlex.quote(str(case / (name + ".log")))}\n'
                        + f'exec {shlex.quote(item["path"])} "$@"\n')
        path.chmod(0o700)
    return wrappers


def packet_fingerprints(info):
    """Retain exact stream-clock units and unknown timestamps independently of payloads."""
    bases = {stream['index']: stream.get('time_base') for stream in info['streams']}
    packet_hashes, clock_hashes, packet_counts, last_dts = {}, {}, {}, {}
    for packet in info.get('packets', []):
        index = packet['stream_index']
        if index not in bases:
            raise RuntimeError('Packet references an independently unknown stream')
        # Tick values avoid floating-point rounding. Missing/N/A values remain
        # distinct from zero, and PTS reordering is not mistaken for DTS order.
        dts = packet.get('dts')
        if dts is not None and dts != 'N/A':
            dts = int(dts)
            if index in last_dts and dts < last_dts[index]:
                raise RuntimeError('Independently observed non-monotonic output DTS')
            last_dts[index] = dts
        packet_hashes.setdefault(index, hashlib.sha256()).update(packet.get('data_hash', '').encode())
        clocks = [packet.get(field) for field in ('pts', 'dts', 'duration')]
        clock_hashes.setdefault(index, hashlib.sha256()).update(
            json.dumps(clocks, separators=(',', ':')).encode() + b'\n')
        packet_counts[index] = packet_counts.get(index, 0) + 1
    return {'packet_payload_sha256_by_stream': {str(index): value.hexdigest()
                                               for index, value in packet_hashes.items()},
            'packet_clock_sha256_by_stream': {
                str(index): {'time_base': bases[index], 'packets': packet_counts[index],
                             'sha256': value.hexdigest()} for index, value in clock_hashes.items()}}


def output_fingerprint(item, tools):
    path = Path(item['path'])
    info = json.loads(command([tools['ffprobe']['path'], '-v', 'error', '-show_format',
                               '-show_streams', '-show_packets', '-show_data_hash', 'sha256',
                               '-of', 'json', path]))
    streams = [{key: stream.get(key) for key in ('codec_type', 'codec_name', 'width', 'height',
                                                'sample_rate', 'channels', 'sample_aspect_ratio')}
               for stream in info['streams']]
    digests = packet_fingerprints(info)
    for name, args in (('video', ['-map', '0:v:0', '-an', '-pix_fmt', 'yuv420p']),
                       ('audio', ['-map', '0:a:0', '-vn', '-acodec', 'pcm_s16le'])):
        if not any(stream['codec_type'] == name for stream in streams):
            continue
        raw = command([tools['ffmpeg']['path'], '-hide_banner', '-v', 'error', '-nostdin',
                       '-threads', '2', '-xerror', '-i', path, *args, '-f', 'hash', '-hash', 'sha256', '-'])
        digests[name + '_decoded_sha256'] = raw.decode().strip()
    subtitles = [stream for stream in streams if stream['codec_type'] == 'subtitle']
    if subtitles:
        raw = command([tools['ffmpeg']['path'], '-v', 'error', '-nostdin', '-i', path,
                       '-map', '0:s:0', '-c:s', 'srt', '-f', 'srt', '-'])
        digests['subtitle_sha256'] = hashlib.sha256(raw).hexdigest()
        digests['subtitle_text'] = raw.decode()
    return {'file_sha256': sha256(path), 'bytes': path.stat().st_size,
            'duration_seconds': float(info['format']['duration']), 'streams': streams,
            'segments': item.get('segments', []), 'track_manifest': item.get('track_manifest', []),
            'actual_start_ms': item.get('actual_start_ms'), 'actual_end_ms': item.get('actual_end_ms'),
            **digests}


def compare_fingerprints(left, right, copy_mode=False):
    if len(left) != len(right):
        raise RuntimeError('Different output counts')
    for index, (a, b) in enumerate(zip(left, right)):
        fields = set(a) | set(b)
        ignored = {'file_sha256', 'bytes'}
        if not copy_mode:
            # Independently decoded equality is stricter than a perceptual
            # check. Valid encoder packet nondeterminism must remain recorded
            # separately from decoded-content semantics; copy payloads must agree.
            ignored.add('packet_payload_sha256_by_stream')
        for field in fields - ignored:
            if a.get(field) != b.get(field):
                raise RuntimeError(f'Output {index} semantic mismatch: {field}')


def verify_shape(scenario, fingerprints):
    expected = 1 if scenario['output_mode'] == 'joined' else scenario['count']
    if len(fingerprints) != expected:
        raise RuntimeError(f'Expected {expected} independently decoded outputs')
    for item in fingerprints:
        video = [stream for stream in item['streams'] if stream['codec_type'] == 'video']
        audio = [stream for stream in item['streams'] if stream['codec_type'] == 'audio']
        if len(video) != (0 if scenario['options']['format'] == 'mp3' else 1) or len(audio) != 1:
            raise RuntimeError('Unexpected independent stream selection')
        if scenario['options'].get('subtitle_mode') == 'preserve' and 'subtitle_sha256' not in item:
            raise RuntimeError('Preserved subtitle missing from independent output')


def run_sample(manifest, scenario, side, pair, binary, root, witnesses):
    ensure_space(root, 512 * 1024 ** 2)
    case = root / f'{scenario["id"]}-p{pair:02}-{side}'
    case.mkdir()
    media = case / 'media'
    media.mkdir()
    wrappers = create_wrappers(case, manifest['tools'])
    env = os.environ.copy()
    env['PATH'] = str(wrappers) + os.pathsep + env.get('PATH', '')
    env['FFMPEG_PATH'] = str(wrappers / 'ffmpeg')
    env['FFPROBE_PATH'] = str(wrappers / 'ffprobe')
    fixture = manifest['fixtures'][scenario['fixture']]
    plan = {'version': 1, 'input': fixture['path'], 'ranges': scenario['ranges'],
            'options': scenario['options'], 'output_mode': scenario['output_mode']}
    write_json(case / 'plan.json', plan)
    target = media if scenario['output_mode'] == 'separate' else media / ('joined.' + scenario['options']['format'])
    args = [binary, 'batch', '--plan', case / 'plan.json', '--output', target,
            '--threads', '2', '--profile', 'fast']
    if scenario['kind'] == 'cli' and scenario['count'] == 1 and scenario['output_mode'] == 'separate':
        single = scenario['ranges'][0]
        target = media / ('single.' + scenario['options']['format'])
        args = [binary, 'clip', '--input', fixture['path'], '--output', target,
                '--start', str(single['start_ms'] / 1000), '--end', str(single['end_ms'] / 1000),
                '--mode', scenario['options']['cut_mode'], '--quality', 'best', '--threads', '2', '--profile', 'fast']
    if scenario['kind'] == 'embedded':
        if not witnesses.get(side + '_engine'):
            raise RuntimeError('Embedding witness binary required')
        args = [witnesses[side + '_engine'], '--plan', case / 'plan.json', '--output', target]
    elif scenario['kind'] == 'hls':
        if not witnesses.get(side + '_hls'):
            raise RuntimeError('HLS witness binary required')
        write_json(case / 'hls-scenario.json', scenario)
        env.update({'CUTMY_CORE_PERF_HLS_SCENARIO': str(case / 'hls-scenario.json'),
                    'CUTMY_CORE_PERF_HLS_FIXTURE': fixture['path'],
                    'CUTMY_CORE_PERF_HLS_WORK': str(media),
                    'CUTMY_CORE_PERF_HLS_RESULT': str(case / 'hls-result.json')})
        args = [witnesses[side + '_hls'], '-test.run=^TestCorePerformanceHLSObservation$', '-test.count=1']
    metrics = measured_process(args, env, case)
    payload = json.loads((case / 'hls-result.json' if scenario['kind'] == 'hls' else case / 'stdout.txt').read_text())
    if 'outputs' not in payload:
        payload = {'outputs': [payload]}
    fingerprints = [output_fingerprint(item, manifest['tools']) for item in payload['outputs']]
    verify_shape(scenario, fingerprints)
    record = {'scenario': scenario['id'], 'kind': scenario['kind'], 'side': side, 'pair': pair,
              'command': [str(value) for value in args], 'binary_sha256': sha256(args[0]),
              'manifest_sha256': manifest['_sha256'], 'case_path': str(case), **metrics,
              'subprocess_counts': {name: len((case / (name + '.log')).read_text().splitlines())
                                    if (case / (name + '.log')).exists() else 0 for name in manifest['tools']},
              'output_bytes': sum(item['bytes'] for item in fingerprints),
              'outputs': fingerprints,
              'hls': payload.get('hls'), 'verified_media_deleted': False}
    write_json(case / 'metrics.json', record)
    ensure_space(root)
    return record


def summary(records):
    result = []
    for ident in dict.fromkeys(row['scenario'] for row in records):
        row = {'scenario': ident, 'sides': {}}
        for side in ('baseline', 'candidate'):
            selected = [record for record in records if record['scenario'] == ident and record['side'] == side]
            fields = ('wall_seconds', 'wait4_cpu_seconds', 'sampled_tree_peak_rss_bytes',
                      'sampled_peak_media_workspace_bytes', 'output_bytes', 'wait4_input_blocks',
                      'wait4_output_blocks')
            row['sides'][side] = {'samples': len(selected),
                                   **{field: {'median': statistics.median(record[field] for record in selected),
                                              'min': min(record[field] for record in selected),
                                              'max': max(record[field] for record in selected)} for field in fields}}
        a, b = (row['sides'][side]['wall_seconds']['median'] for side in ('baseline', 'candidate'))
        row['median_wall_improvement_percent'] = 100 * (a - b) / a
        result.append(row)
    return result


def protocol_pair_counts(registered):
    return {scenario['id']: 5 if scenario['final_key'] else 3 for scenario in registered}


def register_pairs(args):
    # This preregistration reads only the frozen JSON, without touching media or
    # tools, so it is safe to prepare while a separate diagnostic owns the CPU.
    expected = args.manifest.with_suffix('.sha256').read_text().strip()
    if sha256(args.manifest) != expected:
        raise RuntimeError('Frozen manifest digest mismatch')
    manifest = json.loads(args.manifest.read_text())
    if manifest['scenarios'] != scenarios():
        raise RuntimeError('Registered workload changed')
    counts = protocol_pair_counts(manifest['scenarios'])
    supplement = {'schema': 1, 'registered_utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                  'manifest_sha256': expected, 'scenario_pair_counts': counts,
                  'rule': 'Every frozen final_key receives 5 alternating pairs; every other frozen case receives 3.',
                  'scenarios': len(counts), 'key_scenarios': sum(value == 5 for value in counts.values()),
                  'timed_operations': 2 * sum(counts.values()),
                  'verification': 'Unchanged: independently probe and fully decode every output after timing. No decode memoization.',
                  'scope': 'Orchestration supplement before timings; original frozen manifest remains unchanged.'}
    write_json(args.output, supplement, exclusive=True)
    args.output.with_suffix('.sha256').write_text(sha256(args.output) + '\n')
    print(json.dumps({'supplement': str(args.output.resolve()), 'sha256': sha256(args.output),
                      'timed_operations': supplement['timed_operations']}))


def selected_pair_counts(args, selected, manifest):
    if args.protocol_pairs:
        if args.pairs is not None or args.pair_supplement is None:
            raise RuntimeError('--protocol-pairs requires --pair-supplement and excludes --pairs')
        supplement = args.pair_supplement
        if sha256(supplement) != supplement.with_suffix('.sha256').read_text().strip():
            raise RuntimeError('Pair supplement digest mismatch')
        declared = json.loads(supplement.read_text())
        expected = protocol_pair_counts(manifest['scenarios'])
        if declared['manifest_sha256'] != manifest['_sha256'] or declared['scenario_pair_counts'] != expected:
            raise RuntimeError('Pair supplement changed the frozen case/key policy')
        return {scenario['id']: expected[scenario['id']] for scenario in selected}
    pairs = 3 if args.pairs is None else args.pairs
    if pairs < 3:
        raise RuntimeError('At least three alternating pairs are required')
    if args.final_keys and pairs < 5:
        raise RuntimeError('Final key scenarios require at least five alternating pairs')
    return {scenario['id']: pairs for scenario in selected}


def build_witnesses(args):
    """Build outside timed windows, with immutable helper bytes on both sides."""
    manifest = load_manifest(args.manifest)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    ensure_space(root, 256 * 1024 ** 2)
    helper_root = Path(__file__).resolve().parent / 'testdata'
    sources = {name: helper_root / ('core-performance-' + name + suffix)
               for name, suffix in (('engine', '.go'), ('hls', '_test.go'))}
    commands, products = [], {}
    for side, checkout in (('baseline', args.baseline_root.resolve()),
                            ('candidate', args.candidate_root.resolve())):
        if args.side != 'both' and side != args.side:
            continue
        source_meta = build_source_catalog(checkout)
        helpers_before = {name: metadata(path) for name, path in sources.items()}
        if side == 'baseline':
            revision = command(['git', '-C', checkout, 'rev-parse', 'HEAD']).decode().strip()
            if revision != manifest['baseline']['revision']:
                raise RuntimeError('Embedding/HLS baseline checkout differs from frozen baseline revision')
            command(['git', '-C', checkout, 'diff', '--quiet', 'HEAD', '--', '*.go', 'go.mod', 'go.sum',
                     'internal/app/migrations/*.sql', 'internal/app/migrations/*.json'])
        engine_binary = root / (side + '-engine')
        invocation = ['go', 'build', '-o', str(engine_binary), str(sources['engine'])]
        version = command(['go', 'version'], cwd=checkout).decode().strip()
        command(invocation, timeout=300, cwd=checkout)
        if source_meta != build_source_catalog(checkout) or helpers_before != {name: metadata(path) for name, path in sources.items()}:
            raise RuntimeError('Runtime, embedded migration or observation helper changed during witness compilation')
        commands.append({'side': side, 'kind': 'engine', 'cwd': str(checkout), 'command': invocation,
                         'go': version, 'helper': metadata(sources['engine'])})
        products[side + '_engine'] = metadata(engine_binary)
        adapter = root / (side + '-hls-scope_test.go')
        body = 'return ctx, func() {}, nil' if side == 'baseline' else 'return withHLSDownloadCache(ctx, work, 32<<20)'
        adapter.write_text('package app\nimport "context"\n'
                           'func corePerformanceHLSScope(ctx context.Context, work string) (context.Context, func(), error) { '
                           + body + ' }\n')
        overlay = root / (side + '-overlay.json')
        write_json(overlay, {'Replace': {
            str(checkout / 'internal/app/core_performance_observation_test.go'): str(sources['hls']),
            str(checkout / 'internal/app/core_performance_scope_test.go'): str(adapter)}})
        hls_binary = root / (side + '-hls.test')
        invocation = ['go', 'test', '-overlay', str(overlay), '-c', '-o', str(hls_binary), './internal/app']
        command(invocation, timeout=300, cwd=checkout)
        if source_meta != build_source_catalog(checkout) or helpers_before != {name: metadata(path) for name, path in sources.items()}:
            raise RuntimeError('Runtime, embedded migration or observation helper changed during witness compilation')
        commands.append({'side': side, 'kind': 'hls', 'cwd': str(checkout), 'command': invocation,
                         'go': version, 'helper': metadata(sources['hls']), 'scope_adapter': metadata(adapter),
                         'source_sha256': source_meta})
        products[side + '_hls'] = metadata(hls_binary)
    result = {'manifest_sha256': sha256(args.manifest), 'harness': metadata(__file__),
              'builds': commands, 'products': products}
    write_json(root / 'provenance.json', result, exclusive=True)
    print(json.dumps({'products': {key: value['path'] for key, value in products.items()},
                      'provenance': str(root / 'provenance.json')}))


def build_pure_witnesses(args):
    """Compile an identical pure-Go observation overlay; never execute it here."""
    manifest = load_manifest(args.manifest)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=False)
    ensure_space(root, 256 * 1024 ** 2)
    helper = Path(__file__).resolve().parent / 'testdata/core-performance-pure_test.go'
    products, builds = {}, []
    for side, checkout in (('baseline', args.baseline_root.resolve()),
                           ('candidate', args.candidate_root.resolve())):
        if args.side != 'both' and side != args.side:
            continue
        if side == 'baseline':
            revision = command(['git', '-C', checkout, 'rev-parse', 'HEAD']).decode().strip()
            if revision != manifest['baseline']['revision']:
                raise RuntimeError('Pure-Go baseline differs from frozen baseline revision')
            command(['git', '-C', checkout, 'diff', '--quiet', 'HEAD', '--', '*.go', 'go.mod', 'go.sum',
                     'internal/app/migrations/*.sql', 'internal/app/migrations/*.json'])
        overlay = root / (side + '-pure-overlay.json')
        write_json(overlay, {'Replace': {
            str(checkout / 'internal/app/core_performance_pure_observation_test.go'): str(helper)}})
        binary = root / (side + '-pure.test')
        invocation = ['go', 'test', '-overlay', str(overlay), '-c', '-o', str(binary), './internal/app']
        version = command(['go', 'version'], cwd=checkout).decode().strip()
        source_meta = build_source_catalog(checkout)
        helper_before = metadata(helper)
        command(invocation, timeout=300, cwd=checkout)
        if source_meta != build_source_catalog(checkout) or helper_before != metadata(helper):
            raise RuntimeError('Runtime, embedded migration or pure helper changed during compilation')
        builds.append({'side': side, 'cwd': str(checkout), 'command': invocation,
                       'go': version, 'helper': metadata(helper), 'overlay': metadata(overlay),
                       'source_sha256': source_meta})
        products[side + '_pure'] = metadata(binary)
    write_json(root / 'provenance.json', {'manifest_sha256': sha256(args.manifest),
                                        'harness': metadata(__file__), 'builds': builds,
                                        'products': products}, exclusive=True)
    print(json.dumps({'products': {key: value['path'] for key, value in products.items()},
                      'provenance': str(root / 'provenance.json')}))


def run(args):
    manifest = load_manifest(args.manifest)
    manifest['_sha256'] = sha256(args.manifest)
    root = args.output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    with (root / '.measurement.lock').open('a') as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        selected = [scenario for scenario in manifest['scenarios']
                    if (not args.scenario or scenario['id'] in args.scenario)
                    and (not args.kind or scenario['kind'] == args.kind)
                    and (not args.final_keys or scenario['final_key'])]
        if not selected:
            raise RuntimeError('No registered scenarios selected')
        if args.scenario and set(args.scenario) - {scenario['id'] for scenario in selected}:
            raise RuntimeError('Unknown/unselected scenario requested')
        baseline = Path(manifest['baseline']['path'])
        candidate = args.candidate.resolve(strict=True)
        witnesses = {key: str(value.resolve(strict=True)) for key, value in vars(args).items()
                     if key in ('baseline_engine', 'candidate_engine', 'baseline_hls', 'candidate_hls') and value}
        pair_counts = selected_pair_counts(args, selected, manifest)
        environment = {'manifest_sha256': manifest['_sha256'], 'harness_sha256': sha256(__file__),
                       'candidate': {**metadata(candidate), 'revision': args.candidate_revision,
                                     'version': json.loads(command([candidate, 'version']))},
                       'witnesses': {key: metadata(value) for key, value in witnesses.items()},
                       'platform': platform.platform(), 'affinity': sorted(os.sched_getaffinity(0)),
                       'load_average_at_start': list(os.getloadavg()),
                       'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                       'selected_scenarios': [scenario['id'] for scenario in selected],
                       'pairs': args.pairs, 'scenario_pair_counts': pair_counts,
                       'pair_supplement': metadata(args.pair_supplement) if args.protocol_pairs else None,
                       'coordinator_idle_window': args.idle_window}
        if not args.idle_window:
            raise RuntimeError('Record coordinator-granted --idle-window before timing')
        write_json(root / 'environment.json', environment, exclusive=True)
        records = []
        for scenario in selected:
            for pair in range(pair_counts[scenario['id']]):
                matched = {}
                for side in (('baseline', 'candidate') if pair % 2 == 0 else ('candidate', 'baseline')):
                    record = run_sample(manifest, scenario, side, pair,
                                        baseline if side == 'baseline' else candidate, root, witnesses)
                    matched[side] = record
                    records.append(record)
                    write_json(root / 'raw-results.json', records)
                    print(json.dumps({key: record[key] for key in ('scenario', 'side', 'pair',
                                                                   'wall_seconds', 'wait4_cpu_seconds',
                                                                   'subprocess_counts', 'output_bytes')}), flush=True)
                compare_fingerprints(matched['baseline']['outputs'], matched['candidate']['outputs'],
                                     copy_mode=scenario['options']['cut_mode'] == 'copy')
                # Delete only after both independent decoding and matched-pair
                # comparison pass. A regression retains both sides for diagnosis.
                for record in matched.values():
                    case = Path(record['case_path'])
                    shutil.rmtree(case / 'media')
                    record['verified_media_deleted'] = True
                    write_json(case / 'metrics.json', record)
                write_json(root / 'raw-results.json', records)
                write_json(root / 'summary.json', summary(records))
        print(json.dumps({'verified': True, 'samples': len(records), 'summary': str(root / 'summary.json')}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    frozen = sub.add_parser('freeze')
    frozen.add_argument('--corpus', type=Path, required=True)
    frozen.add_argument('--manifest', type=Path, required=True)
    frozen.add_argument('--baseline', type=Path, required=True)
    frozen.add_argument('--baseline-revision', required=True)
    frozen.add_argument('--acceptance', type=Path, required=True)
    frozen.add_argument('--complex', type=Path, required=True)
    measured = sub.add_parser('run')
    measured.add_argument('--manifest', type=Path, required=True)
    measured.add_argument('--candidate', type=Path, required=True)
    measured.add_argument('--candidate-revision', required=True)
    measured.add_argument('--output', type=Path, required=True)
    measured.add_argument('--pairs', type=int)
    measured.add_argument('--protocol-pairs', action='store_true')
    measured.add_argument('--pair-supplement', type=Path)
    measured.add_argument('--kind', choices=('cli', 'embedded', 'hls'))
    measured.add_argument('--scenario', action='append')
    measured.add_argument('--final-keys', action='store_true')
    measured.add_argument('--idle-window', help='Coordinator-granted idle window identifier')
    for name in ('baseline-engine', 'candidate-engine', 'baseline-hls', 'candidate-hls'):
        measured.add_argument('--' + name, type=Path)
    built = sub.add_parser('build-witnesses')
    built.add_argument('--manifest', type=Path, required=True)
    built.add_argument('--baseline-root', type=Path, required=True)
    built.add_argument('--candidate-root', type=Path, required=True)
    built.add_argument('--output', type=Path, required=True)
    built.add_argument('--side', choices=('baseline', 'candidate', 'both'), default='both')
    pure = sub.add_parser('build-pure-witnesses')
    pure.add_argument('--manifest', type=Path, required=True)
    pure.add_argument('--baseline-root', type=Path, required=True)
    pure.add_argument('--candidate-root', type=Path, required=True)
    pure.add_argument('--output', type=Path, required=True)
    pure.add_argument('--side', choices=('baseline', 'candidate', 'both'), default='both')
    pairs = sub.add_parser('register-pairs')
    pairs.add_argument('--manifest', type=Path, required=True)
    pairs.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.action == 'freeze':
        freeze(args)
    elif args.action == 'run':
        run(args)
    elif args.action == 'build-witnesses':
        build_witnesses(args)
    elif args.action == 'build-pure-witnesses':
        build_pure_witnesses(args)
    else:
        register_pairs(args)


if __name__ == '__main__':
    main()
