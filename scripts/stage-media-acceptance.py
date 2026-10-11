#!/usr/bin/env python3
"""Independent saved-plan CLI acceptance on generated, decodable media.

Requires FFmpeg/ffprobe. Media stays outside the repository; the JSON report
records results of independent decoding, colour/order, audio and packet checks.
"""
import argparse
from array import array
import hashlib
import json
import math
import re
from pathlib import Path
import subprocess


def command(args):
    result = subprocess.run([str(x) for x in args], stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=180)
    if result.returncode:
        raise RuntimeError(f'{args[0]} exited {result.returncode}: {result.stderr.decode(errors="replace")[-2500:]}')
    return result.stdout


def ffmpeg(*args):
    return command(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-nostdin', '-y', '-threads', '1', *args])


def probe(path, packets=False):
    args = ['ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json']
    if packets:
        args += ['-show_packets', '-show_data_hash', 'sha256']
    return json.loads(command([*args, path]))


def frequency(path, start=0.3):
    pcm = array('f')
    pcm.frombytes(ffmpeg('-ss', str(start), '-i', path, '-t', '0.4', '-map', '0:a:0', '-ac', '1', '-ar', '8000', '-f', 'f32le', 'pipe:1'))
    crossings = sum(pcm[i] <= 0 < pcm[i+1] for i in range(len(pcm)-1))
    return crossings * 8000 / len(pcm)


def audio_alignment(source, output, stream_index, source_at_ms, output_at_ms):
    """Compare independently decoded chirps on their presentation timelines."""
    def pcm(path, index, at):
        values = array('f')
        begin, end = at / 1000, (at + 400) / 1000
        audio_filter = (f'aresample=async=1000:min_hard_comp=0.001:first_pts=0,'
                        f'atrim=start={begin}:end={end},asetpts=PTS-STARTPTS')
        values.frombytes(ffmpeg('-copyts', '-i', path, '-map', f'0:{index}', '-af', audio_filter,
                                '-ac', '1', '-ar', '48000', '-f', 'f32le', 'pipe:1'))
        return values
    reference = pcm(source, stream_index, source_at_ms)
    actual = pcm(output, 1, output_at_ms)
    count = min(len(reference), len(actual))
    assert count >= 16000, (source, output, 'short decoded audio', count)
    positions = list(range(2400, count - 2400, 16))
    xx = sum(reference[i] ** 2 for i in positions)
    def score(lag):
        xy = sum(reference[i] * actual[i + lag] for i in positions)
        yy = sum(actual[i + lag] ** 2 for i in positions)
        return xy / math.sqrt(xx * yy)
    lag = max(range(-2400, 2401, 4), key=score)
    lag = max(range(max(-2400, lag-4), min(2400, lag+4)+1), key=score)
    return {'correlation': score(lag), 'lag_samples_48000hz': lag, 'lag_ms': lag / 48}


def colour(path, at):
    rgb = ffmpeg('-ss', str(at), '-i', path, '-frames:v', '1', '-vf', 'scale=1:1', '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1')
    assert len(rgb) == 3, (path, at, rgb)
    return ['red', 'green', 'blue'][max(range(3), key=lambda x: rgb[x])]


def make_fixtures(workspace):
    subtitle = workspace / 'source.srt'
    subtitle.write_text('1\n00:00:01,000 --> 00:00:04,000\nRed caption\n\n2\n00:00:05,000 --> 00:00:07,000\nGreen caption\n\n3\n00:00:09,000 --> 00:00:11,000\nBlue caption\n')
    multi = workspace / 'multi.mp4'
    ffmpeg('-f', 'lavfi', '-i', 'color=red:size=320x240:rate=25:duration=4',
           '-f', 'lavfi', '-i', 'color=green:size=320x240:rate=25:duration=4',
           '-f', 'lavfi', '-i', 'color=blue:size=320x240:rate=25:duration=4',
           '-f', 'lavfi', '-i', 'sine=frequency=440:duration=12',
           '-f', 'lavfi', '-i', 'sine=frequency=880:duration=12', '-i', subtitle,
           '-filter_complex', '[0:v][1:v][2:v]concat=n=3:v=1:a=0[v]',
           '-map', '[v]', '-map', '3:a', '-map', '4:a', '-map', '5:s',
           '-c:v', 'libx264', '-threads', '1', '-preset', 'fast', '-g', '25', '-pix_fmt', 'yuv420p',
           '-c:a', 'aac', '-c:s', 'mov_text', '-metadata:s:a:0', 'language=eng',
           '-metadata:s:a:0', 'title=English 440 Hz', '-metadata:s:a:1', 'language=rus',
           '-metadata:s:a:1', 'title=Russian 880 Hz', '-disposition:a:0', 'default', '-disposition:a:1', '0',
           '-movflags', '+faststart', multi)
    ordinary = workspace / 'ordinary.mp4'
    ffmpeg('-i', multi, '-map', '0:v:0', '-map', '0:a:0', '-c', 'copy', ordinary)
    vfr = workspace / 'vfr.mp4'
    ffmpeg('-i', ordinary, '-map', '0:v:0', '-map', '0:a:0', '-vf', "select='if(lt(t,6),not(mod(n,2)),not(mod(n,3)))'",
           '-fps_mode', 'vfr', '-c:v', 'libx264', '-threads', '1', '-preset', 'fast', '-g', '12', '-c:a', 'copy', vfr)
    rotated = workspace / 'rotated.mp4'
    ffmpeg('-i', ordinary, '-c', 'copy', '-metadata:s:v:0', 'rotate=90', rotated)
    video = next(s for s in probe(rotated)['streams'] if s['codec_type'] == 'video')
    if not any(abs(s.get('rotation', 0)) == 90 for s in video.get('side_data_list', [])):
        ffmpeg('-display_rotation', '90', '-i', ordinary, '-c', 'copy', rotated)
    video = next(s for s in probe(rotated)['streams'] if s['codec_type'] == 'video')
    assert any(abs(s.get('rotation', 0)) == 90 for s in video.get('side_data_list', [])), 'fixture rotation was not written'
    chirp = workspace / 'chirp.mp4'
    ffmpeg('-i', multi, '-f', 'lavfi', '-i', 'aevalsrc=sin(2*PI*(800*t+20*t*t)):s=48000:d=12',
           '-map', '0:v:0', '-map', '0:a:0', '-map', '1:a:0', '-map', '0:s:0',
           '-c', 'copy', '-c:a:1', 'aac', '-metadata:s:a:1', 'language=rus',
           '-metadata:s:a:1', 'title=Russian chirp', '-disposition:a:1', '0', chirp)
    return {'ordinary': ordinary, 'vfr': vfr, 'rotated': rotated, 'multi': multi, 'chirp': chirp}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--workspace', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    a = parser.parse_args()
    a.workspace.mkdir(parents=True, exist_ok=True)
    fixtures = make_fixtures(a.workspace)
    records = []
    for name, source in fixtures.items():
        inspected = json.loads(command([a.binary, 'inspect', '--input', source]))
        tracks = inspected['tracks']
        if name in ('multi', 'chirp'):
            audios = [t for t in tracks if t['type'] == 'audio']
            assert [t.get('language') for t in audios] == ['eng', 'rus'], tracks
        for mode, container, output_mode, silent, subtitles in [
            ('accurate', 'mp4', 'separate', False, False),
            ('accurate', 'mp4', 'joined', False, False),
            ('copy', 'mp4', 'joined', False, name in ('multi', 'chirp')),
            ('copy', 'mkv', 'joined', False, name in ('multi', 'chirp')),
            ('accurate', 'mp4', 'joined', True, False)]:
            case = f'{name}-{mode}-{container}-{output_mode}-' + ('silent' if silent else 'audio')
            target = a.workspace / case
            target.mkdir(exist_ok=True)
            options = {'format': container, 'quality': 'best', 'cut_mode': mode}
            if silent:
                options['no_audio'] = True
            elif name in ('multi', 'chirp'):
                options['audio_track'] = 2
            if subtitles:
                options['subtitle_mode'] = 'preserve'
            plan = {'version': 1, 'input': str(source), 'ranges': [
                {'label': 'Blue first', 'start_ms': 8500, 'end_ms': 10500},
                {'label': 'Red second', 'start_ms': 1500, 'end_ms': 3500}],
                'options': options, 'output_mode': output_mode}
            plan_path = target / 'cuts.json'
            plan_path.write_text(json.dumps(plan))
            output = target if output_mode == 'separate' else target / f'joined.{container}'
            result = json.loads(command([a.binary, 'batch', '--plan', plan_path, '--output', output]))
            outputs = result['outputs']
            assert len(outputs) == (2 if output_mode == 'separate' else 1), result
            files = []
            for index, item in enumerate(outputs):
                path = Path(item['path'])
                ffmpeg('-xerror', '-i', path, '-map', '0:v:0', '-map', '0:a?', '-f', 'null', '-')
                media = probe(path, packets=True)
                videos = [s for s in media['streams'] if s['codec_type'] == 'video']
                audio = [s for s in media['streams'] if s['codec_type'] == 'audio']
                subs = [s for s in media['streams'] if s['codec_type'] == 'subtitle']
                assert len(videos) == 1 and len(audio) == (0 if silent else 1), media['streams']
                assert len(subs) == (1 if subtitles else 0), media['streams']
                duration = float(media['format']['duration'])
                if name == 'rotated' and mode == 'accurate':
                    assert (videos[0]['width'], videos[0]['height']) == (240, 320), videos[0]
                if mode == 'copy':
                    assert (videos[0]['width'], videos[0]['height']) == (320, 240), videos[0]
                    if name == 'rotated':
                        assert any(abs(side.get('rotation', 0)) == 90 for side in videos[0].get('side_data_list', [])), (case, 'copied display rotation lost', videos[0])
                if mode == 'accurate':
                    assert abs(duration - (2 if output_mode == 'separate' else 4)) < .15, duration
                expected = ['blue', 'red'] if output_mode == 'joined' else [['blue', 'red'][index]]
                if output_mode == 'joined':
                    segments = item['segments']
                    assert len(segments) == 2 and segments[0]['output_end_ms'] == segments[1]['output_start_ms'], segments
                    ats = [s['output_start_ms']/1000 + .4 for s in segments]
                else:
                    ats = [.4]
                observed = [colour(path, at) for at in ats]
                assert observed == expected, (case, observed, expected)
                hz = None if silent or name == 'chirp' else frequency(path)
                if hz is not None:
                    assert abs(hz - (880 if name in ('multi', 'chirp') else 440)) < 15, (case, hz)
                for stream in videos + audio:
                    packets = [p for p in media['packets'] if p['stream_index'] == stream['index'] and 'dts_time' in p]
                    dts = [float(p['dts_time']) for p in packets]
                    assert all(x <= y for x, y in zip(dts, dts[1:])), (case, stream['codec_type'])
                av_start_delta = None
                if audio:
                    video_pts = [float(p['pts_time']) for p in media['packets'] if p['stream_index'] == videos[0]['index'] and 'pts_time' in p]
                    audio_pts = [float(p['pts_time']) for p in media['packets'] if p['stream_index'] == audio[0]['index'] and 'pts_time' in p]
                    av_start_delta = min(audio_pts) - min(video_pts)
                    assert abs(av_start_delta) < .1, (case, av_start_delta)
                alignments = []
                if name == 'chirp' and not silent:
                    for segment in item['segments']:
                        alignment = audio_alignment(source, path, 2, segment['source_start_ms'] + 300,
                                                    segment['output_start_ms'] + 300)
                        assert alignment['correlation'] > (.99 if mode == 'copy' else .95), (case, alignment)
                        assert abs(alignment['lag_ms']) <= (2 if mode == 'copy' else 25), (case, alignment)
                        alignments.append(alignment)
                subtitle_text = None
                if subtitles:
                    subtitle_text = ffmpeg('-i', path, '-map', '0:s:0', '-f', 'srt', 'pipe:1').decode()
                    assert subtitle_text.index('Blue caption') < subtitle_text.index('Red caption'), subtitle_text
                    assert 'Green caption' not in subtitle_text, subtitle_text
                    def timestamp(raw):
                        hours, minutes, seconds, ms = (int(x) for x in re.split('[:,]', raw))
                        return hours*3600000 + minutes*60000 + seconds*1000 + ms
                    matches = re.findall(r'(\d+:\d+:\d+,\d+) --> (\d+:\d+:\d+,\d+)\n([^\n]+)', subtitle_text)
                    assert len(matches) == 2, subtitle_text
                    for cue, segment, original_bounds in zip(matches, item['segments'], [(9000, 11000), (1000, 4000)]):
                        begin = segment['output_start_ms'] + max(original_bounds[0], segment['source_start_ms']) - segment['source_start_ms']
                        end = segment['output_start_ms'] + min(original_bounds[1], segment['source_end_ms']) - segment['source_start_ms']
                        assert abs(timestamp(cue[0])-begin) <= 2 and abs(timestamp(cue[1])-end) <= 2, (case, cue, begin, end)
                packet_preserved = None
                if mode == 'copy':
                    original = probe(source, packets=True)
                    source_streams = [s for s in original['streams'] if s['codec_type'] == 'video' or s['codec_type'] == 'audio' and s['index'] == (2 if name in ('multi', 'chirp') else 1)]
                    for out_stream, src_stream in zip(videos+audio, source_streams):
                        hashes = {p['data_hash'] for p in original['packets'] if p['stream_index'] == src_stream['index']}
                        actual = [p['data_hash'] for p in media['packets'] if p['stream_index'] == out_stream['index']]
                        assert actual and all(h in hashes for h in actual), (case, out_stream['codec_type'], 'packet payload changed')
                    packet_preserved = True
                files.append({'duration_s': duration, 'colours': observed, 'audio_hz': hz, 'subtitle_srt': subtitle_text,
                              'audio_video_start_delta_s': av_start_delta, 'chirp_audio_alignment': alignments,
                              'copied_packet_payloads_unchanged': packet_preserved,
                              'width': videos[0]['width'], 'height': videos[0]['height'],
                              'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size})
            records.append({'case': case, 'result': result, 'decoded_files': files})
            print(case + ': passed', flush=True)
    report = {'binary': json.loads(command([a.binary, 'version'])),
              'binary_sha256': hashlib.sha256(a.binary.read_bytes()).hexdigest(),
              'tools': {'ffmpeg': command(['ffmpeg', '-version']).decode().splitlines()[0],
                        'ffprobe': command(['ffprobe', '-version']).decode().splitlines()[0]},
              'fixtures': {name: hashlib.sha256(path.read_bytes()).hexdigest() for name, path in fixtures.items()},
              'cases': records}
    a.report.parent.mkdir(parents=True, exist_ok=True)
    a.report.write_text(json.dumps(report, indent=2) + '\n')


if __name__ == '__main__':
    main()
