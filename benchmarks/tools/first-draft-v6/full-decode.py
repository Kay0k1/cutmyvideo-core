"""Bounded baseline-family media decoding; imported only after timing ends."""
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import subprocess
import time

TOOLS = {'ffmpeg': Path('/usr/bin/ffmpeg'), 'ffprobe': Path('/usr/bin/ffprobe')}
SHA = re.compile(r'[0-9a-f]{64}\Z')
MAX_FRAMES = 16384
STREAM_FIELDS = ('index', 'codec_name', 'codec_type', 'profile', 'width', 'height', 'pix_fmt',
                 'sample_aspect_ratio', 'time_base', 'r_frame_rate', 'avg_frame_rate', 'start_pts',
                 'start_time', 'duration_ts', 'duration', 'sample_fmt', 'sample_rate', 'channels',
                 'channel_layout', 'color_range', 'color_space', 'color_transfer', 'color_primaries')


class DecodeError(Exception):
    def __init__(self, code):
        self.code = code
        super().__init__(code)


def require(value, code):
    if not value: raise DecodeError(code)


def canonical_hash(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(',', ':'), allow_nan=False).encode()).hexdigest()


def run(argv, limit=4 << 20, timeout=30):
    require(argv and argv[0] in TOOLS, 'fixed_baseline_tool_required')
    executable = TOOLS[argv[0]]
    require(executable.is_file() and not executable.is_symlink(), 'baseline_tool_unavailable')
    deadline = time.monotonic() + timeout
    process = subprocess.Popen([str(executable), *argv[1:]], stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                               env={'PATH': '/usr/bin:/bin', 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'})
    output = bytearray(); error_bytes = 0
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, 'stdout')
            selector.register(process.stderr, selectors.EVENT_READ, 'stderr')
            while selector.get_map():
                remaining = deadline - time.monotonic()
                require(remaining > 0, 'decoder_timeout')
                for key, _ in selector.select(min(.25, remaining)):
                    chunk = os.read(key.fileobj.fileno(), 64 << 10)
                    if not chunk:
                        selector.unregister(key.fileobj); continue
                    if key.data == 'stdout':
                        require(len(output) + len(chunk) <= limit, 'decoder_stdout_budget')
                        output.extend(chunk)
                    else:
                        error_bytes += len(chunk)
                        require(error_bytes <= 64 << 10, 'decoder_stderr_budget')
            require(process.wait(timeout=max(.01, deadline - time.monotonic())) == 0, 'full_decoder_failed')
        return bytes(output)
    finally:
        if process.poll() is None:
            process.kill(); process.wait(timeout=5)
        process.stdout.close(); process.stderr.close()


def tool_binding(hash_file):
    result = {}
    for name, path in TOOLS.items():
        result[name] = {'executable_sha256': hash_file(path, 16 << 20),
                        'version_stdout_sha256': hashlib.sha256(run([name, '-version'], 64 << 10)).hexdigest()}
    return result


def parse_framehash(raw):
    require(len(raw) <= 8 << 20, 'framehash_budget')
    try: lines = raw.decode('ascii').splitlines()
    except UnicodeDecodeError: raise DecodeError('framehash_encoding') from None
    require('#format: frame checksums' in lines and '#hash: SHA256' in lines, 'framehash_header')
    kinds, timebases, records = {}, {}, []
    for line in lines:
        line = line.strip()
        match = re.fullmatch(r'#media_type\s+(\d+):\s*(video|audio)', line)
        if match:
            index = int(match[1]); require(index not in kinds, 'framehash_duplicate_stream')
            kinds[index] = match[2]
        match = re.fullmatch(r'#tb\s+(\d+):\s*(\d+)/(\d+)', line)
        if match:
            index = int(match[1]); require(index not in timebases and int(match[2]) > 0 and int(match[3]) > 0,
                                         'framehash_timebase')
            timebases[index] = [int(match[2]), int(match[3])]
        if not line or line.startswith('#'): continue
        fields = [x.strip() for x in line.split(',')]
        require(len(fields) == 6 and all(re.fullmatch(r'-?\d+', x) for x in fields[:5]) and SHA.fullmatch(fields[5]),
                'framehash_row')
        stream, dts, pts, duration, size = map(int, fields[:5])
        require(stream in kinds and duration > 0 and size > 0 and len(records) < MAX_FRAMES,
                'framehash_bounds')
        records.append((stream, dts, pts, duration, size, fields[5]))
    require(kinds == {0: 'video', 1: 'audio'} and set(timebases) == set(kinds), 'full_video_audio_decode_required')
    grouped = {kind: [r for r in records if r[0] == index] for index, kind in kinds.items()}
    require(all(grouped.values()), 'full_decode_empty_stream')
    for rows in grouped.values():
        require(all(a[2] <= b[2] for a, b in zip(rows, rows[1:])), 'decoded_pts_not_monotonic')
    return {'decoded_video_sha256': canonical_hash([[r[4], r[5]] for r in grouped['video']]),
            'decoded_audio_sha256': canonical_hash([[r[4], r[5]] for r in grouped['audio']]),
            'decoded_clock_sha256': canonical_hash({'timebases': timebases, 'rows': [list(r[:5]) for r in records]}),
            'decoded_video_frames': len(grouped['video']), 'decoded_audio_frames': len(grouped['audio']),
            'decoded_audio_pcm_bytes': sum(r[4] for r in grouped['audio'])}


def fingerprint(path, timeout=120):
    deadline = time.monotonic() + timeout
    def budget():
        remaining = deadline - time.monotonic()
        require(remaining > 0, 'full_decode_deadline')
        return min(60, remaining)
    decoded = parse_framehash(run(['ffmpeg', '-v', 'error', '-nostdin', '-xerror', '-threads', '1',
                                  '-i', str(path), '-map', '0:v:0', '-map', '0:a:0',
                                  '-c:v', 'rawvideo', '-pix_fmt', 'yuv420p', '-c:a', 'pcm_f32le',
                                  '-threads', '1', '-f', 'framehash', '-hash', 'sha256', '-'], 8 << 20, budget()))
    raw = run(['ffprobe', '-v', 'error', '-show_streams', '-show_format', '-show_packets',
               '-show_data_hash', 'sha256', '-of', 'json', str(path)], 16 << 20, budget())
    try: profile = json.loads(raw)
    except ValueError: raise DecodeError('full_probe_json') from None
    streams, packets = profile.get('streams'), profile.get('packets')
    require(isinstance(streams, list) and len(streams) == 2 and
            {s.get('codec_type') for s in streams} == {'video', 'audio'}, 'full_probe_streams')
    audio = next(s for s in streams if s['codec_type'] == 'audio')
    require(type(audio.get('channels')) is int and 1 <= audio['channels'] <= 8 and
            decoded['decoded_audio_pcm_bytes'] % (audio['channels'] * 4) == 0, 'decoded_audio_sample_shape')
    decoded['decoded_audio_samples_per_channel'] = decoded.pop('decoded_audio_pcm_bytes') // (audio['channels'] * 4)
    require(isinstance(packets, list) and 0 < len(packets) < MAX_FRAMES, 'packet_budget')
    clocks, payloads = [], []
    indices = {s['index'] for s in streams}
    packet_counts = {i: 0 for i in indices}
    for packet in packets:
        require(packet.get('stream_index') in indices and type(packet.get('pts')) is int and
                type(packet.get('dts')) is int and type(packet.get('duration')) is int and packet['duration'] > 0,
                'packet_clock_missing')
        digest = packet.get('data_hash', '')
        require(isinstance(digest, str) and digest.startswith('SHA256:') and SHA.fullmatch(digest[7:]),
                'packet_payload_hash_missing')
        size = packet.get('size')
        require(isinstance(size, str) and size.isdecimal() and int(size) > 0, 'packet_size')
        packet_counts[packet['stream_index']] += 1
        clocks.append([packet['stream_index'], packet['pts'], packet['dts'], packet['duration'], packet.get('flags')])
        payloads.append([packet['stream_index'], int(size), digest[7:]])
    require(all(packet_counts.values()), 'packet_stream_missing')
    structure = {'streams': [{key: row[key] for key in STREAM_FIELDS if key in row} for row in streams],
                 'format': {key: profile['format'][key] for key in ('format_name', 'start_time', 'duration')
                            if key in profile.get('format', {})}}
    decoded.update(packet_clocks_sha256=canonical_hash(clocks), packet_payloads_sha256=canonical_hash(payloads),
                   media_structure_sha256=canonical_hash(structure), packet_count=len(packets),
                   structure=structure)
    return decoded


COMPARE_FIELDS = ('decoded_video_sha256', 'decoded_audio_sha256', 'decoded_clock_sha256',
                  'decoded_video_frames', 'decoded_audio_frames', 'decoded_audio_samples_per_channel',
                  'packet_clocks_sha256', 'packet_payloads_sha256', 'media_structure_sha256', 'packet_count')


def compare_final(left, right):
    require(left['tool_binding'] == right['tool_binding'], 'paired_tool_family_differs')
    left_rows = [x for x in left['outputs'] if x['rendition'] == 'final']
    right_rows = [x for x in right['outputs'] if x['rendition'] == 'final']
    require(len(left_rows) == len(right_rows) == 15 and
            [x['clip_id'] for x in left_rows] == [x['clip_id'] for x in right_rows], 'paired_full_fifteen_final_required')
    for a, b in zip(left_rows, right_rows):
        require(all(a[field] == b[field] for field in COMPARE_FIELDS), 'paired_final_content_or_clock_changed')
    return {'final_clips': 15, 'exact_decoded_video_audio_equal': True, 'exact_packet_clocks_payloads_equal': True,
            'geometry_and_codec_metadata_equal': True,
            'encoded_file_hashes_equal': sum(a['encoded_file_sha256'] == b['encoded_file_sha256']
                                             for a, b in zip(left_rows, right_rows)),
            'decoded_final_aggregate_sha256': canonical_hash([{k: row[k] for k in COMPARE_FIELDS}
                                                              for row in left_rows])}
