#!/usr/bin/env python3
"""Prepare frozen fixtures before any measurement; never downloads or runs cutmy.

Generation is an explicit separate operation. UHD is synthetic. Long input is
an explicitly repeated 20-second scene. Natural camera derivation preserves
attribution and discloses CFR/color conversion, replaced audio and witness strip.
"""
import argparse
import importlib.util
import json
import math
from pathlib import Path
import re
import sys

spec = importlib.util.spec_from_file_location('media_capacity', Path(__file__).with_name('media-capacity.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


def patch_filter():
    colors = ((240, 20, 20), (20, 240, 20), (20, 20, 240), (240, 240, 240))
    components = []
    for component, function in enumerate(('r', 'g', 'b')):
        patch = f'if(lt(X,64),{colors[0][component]},if(lt(X,128),{colors[1][component]},if(lt(X,192),{colors[2][component]},{colors[3][component]})))'
        components.append(f"{function}='if(lt(Y,64)*lt(X,256),{patch},{function}(X,Y))'")
    return 'format=gbrp,geq=' + ':'.join(components) + ',scale=in_range=full:out_range=limited:out_color_matrix=bt709,format=yuv420p'


def pulse_audio(duration):
    envelope = 'if(between(mod(t,1),.2,.7),1,.05)'
    return "aevalsrc=exprs='" + '|'.join(f'.2*sin(2*PI*{frequency}*t)*{envelope}' for frequency in (440, 880)) + f"':s=48000:d={duration}"


def generator_argv(ffmpeg, destination, width, height, duration, natural=None, looped=None):
    args = [ffmpeg, '-hide_banner', '-v', 'error', '-nostdin', '-n', '-threads', '2', '-filter_complex_threads', '2']
    if looped:
        args += ['-stream_loop', '8', '-i', str(looped)]
    elif natural:
        args += ['-i', str(natural)]
    else:
        args += ['-f', 'lavfi', '-i', f'testsrc=size={width}x{height}:rate=30:duration={duration}']
    args += ['-f', 'lavfi', '-i', pulse_audio(duration)]
    if looped:
        args += ['-map', '0:v:0', '-map', '1:a:0', '-c:v', 'copy']
    else:
        before = 'colorspace=all=bt709:iall=smpte170m,fps=30,' if natural else ''
        args += ['-filter_complex', '[0:v]' + before + patch_filter() + '[video]',
                 '-map', '[video]', '-map', '1:a:0', '-c:v', 'libx264', '-preset', 'fast', '-crf', '18',
                 '-threads:v', '2', '-pix_fmt', 'yuv420p', '-r', '30',
                 '-x264-params', 'keyint=30:min-keyint=30:scenecut=0:colorprim=bt709:transfer=bt709:colormatrix=bt709:fullrange=off']
    args += ['-t', str(duration), '-c:a', 'aac', '-b:a', '192k', '-ac', '2', '-ar', '48000',
             '-color_primaries', 'bt709', '-color_trc', 'bt709', '-colorspace', 'bt709', '-color_range', 'tv',
             '-movflags', '+faststart', str(destination)]
    return list(map(str, args))


class Freeze:
    def __init__(self, args):
        self.args = args
        self.tools = m.read_json(args.tools, args.expected_tools_sha256)
        m.require(set(self.tools) == {'ffmpeg', 'ffprobe'}, 'Both frozen media tools are required')
        for name, tool in self.tools.items():
            m.require(set(tool) == {'path', 'sha256', 'version_line'} and m.sha(m.regular(tool['path'])) == tool['sha256'],
                      'Fixture tool differs from frozen identity')
        self.evidence = m.Evidence(args.output_dir)
        self.commands = m.Commands(self.evidence)
        self.original, self.provenance = None, {}
        if args.original:
            self.original = m.regular(args.original)
            self.provenance = m.read_json(args.provenance, args.expected_provenance_sha256)
            m.require(self.original.stat().st_size <= m.SOURCE_LIMIT and m.sha(self.original) == self.provenance['sha256'],
                      'Natural source differs from independently reviewed provenance')
            m.require(self.provenance['license'] == 'CC-BY-4.0' and self.provenance['author'] and self.provenance['source_url']
                      and self.provenance['license_url'] == 'https://creativecommons.org/licenses/by/4.0/',
                      'Reviewed natural-camera attribution/license is required')

    def call(self, tool, arguments, timeout=120):
        if tool == 'ffmpeg':
            arguments = ['-threads', '2', '-filter_threads', '2', *arguments]
        return self.commands.run([self.tools[tool]['path'], *arguments], timeout=timeout)[0]

    def verify(self, path, width, height, duration):
        out = self.call('ffprobe', ['-v', 'error', '-show_entries',
                                   'stream=index,codec_type,codec_name,width,height,pix_fmt,sample_aspect_ratio,color_range,color_space,color_transfer,color_primaries,channels,sample_rate:format=duration',
                                   '-of', 'json', str(path)])
        metadata = json.loads(out)
        video = [s for s in metadata['streams'] if s['codec_type'] == 'video']
        audio = [s for s in metadata['streams'] if s['codec_type'] == 'audio']
        m.require(len(video) == 1 and len(audio) == 1 and video[0]['codec_name'] == 'h264'
                  and video[0]['width'] == width and video[0]['height'] == height and video[0]['pix_fmt'] == 'yuv420p'
                  and video[0]['sample_aspect_ratio'] == '1:1' and video[0]['color_range'] == 'tv'
                  and all(video[0][key] == 'bt709' for key in ('color_space', 'color_transfer', 'color_primaries'))
                  and audio[0]['codec_name'] == 'aac' and audio[0]['channels'] == 2 and audio[0]['sample_rate'] == '48000'
                  and abs(float(metadata['format']['duration']) - duration) <= .1, 'Input metadata does not match frozen common contract')
        frame_data = json.loads(self.call('ffprobe', ['-v', 'error', '-select_streams', 'v:0', '-show_entries',
                                      'frame=best_effort_timestamp_time,key_frame', '-of', 'json', str(path)]))
        pts = [float(frame['best_effort_timestamp_time']) for frame in frame_data['frames']]
        keys = [float(frame['best_effort_timestamp_time']) for frame in frame_data['frames'] if frame['key_frame'] == 1]
        m.require(len(pts) == duration * 30 and abs(pts[0]) <= .002
                  and all(abs(b - a - 1 / 30) <= .001 for a, b in zip(pts, pts[1:])), 'Frozen input is not actual CFR30')
        m.require(len(keys) >= duration and all(abs(value - index) <= .002 for index, value in enumerate(keys)),
                  'Frozen input does not have the actual one-second keyframe grid')
        physical = []
        for format_ in ('yuv444p', 'rgb24'):
            physical.append(self.call('ffmpeg', ['-v', 'error', '-i', str(path), '-frames:v', '1',
                                                '-vf', 'crop=256:64:0:0,format=' + format_, '-pix_fmt', format_, '-f', 'rawvideo', '-']))
        colors = m.physical_709(*physical)
        if duration <= 20:
            pcm = self.call('ffmpeg', ['-v', 'error', '-i', str(path), '-vn', '-ac', '2', '-ar', '48000', '-f', 'f32le', '-'])
            audio_proof = m.audio_span_witness(pcm, duration, 0, 25)
            tones, alignment = audio_proof['windows'][0]['tones'], audio_proof['windows'][0]['alignment']
        else:
            windows = []
            for offset in (0, duration // 2, duration - 1):
                pcm = self.call('ffmpeg', ['-v', 'error', '-ss', str(offset), '-i', str(path), '-t', '0.9',
                                          '-vn', '-ac', '2', '-ar', '48000', '-f', 'f32le', '-'])
                windows.append({'offset_seconds': offset, 'tones': m.tone_witness(pcm),
                                'alignment': m.audio_alignment_witness(pcm, offset * 1000, 25)})
            audio_proof = {'ok': True, 'scope': 'bounded beginning/middle/end witnesses; full decode separately', 'windows': windows}
            tones, alignment = windows[0]['tones'], windows[0]['alignment']
        self.call('ffmpeg', ['-v', 'error', '-xerror', '-i', str(path), '-f', 'null', '-'], timeout=600)
        proof = {'metadata': metadata, 'pts': pts, 'keyframes': keys, 'physical_bt709': colors, 'stereo_tones': tones,
                 'audio_alignment': alignment, 'audio_span': audio_proof, 'full_decode': True}
        (self.evidence.root / (path.stem + '-proof.json')).write_bytes(m.canonical(proof))
        return {name: True for name in ('metadata', 'cfr_pts', 'keyframes', 'physical_bt709', 'stereo_tones', 'full_decode')}

    def generate(self, name, width, height, duration, natural=None, looped=None):
        path = self.evidence.root / (name + '.mp4')
        args = generator_argv(self.tools['ffmpeg']['path'], path, width, height, duration, natural, looped)
        self.commands.run(args, timeout=600)
        m.require(path.stat().st_size <= m.SOURCE_LIMIT, 'Generated input exceeds 256MiB bound')
        proof = self.verify(path, width, height, duration)
        provenance = {'generator_argv': args, 'generator_sha256': m.sha(Path(__file__).absolute()),
                      'tool_sha256': {key: value['sha256'] for key, value in self.tools.items()}}
        kind = 'synthetic'
        if natural or looped and self.original:
            kind = 'repeated-natural-camera-derived' if looped else 'natural-camera-derived'
            provenance.update(author=self.provenance['author'], source_url=self.provenance['source_url'],
                              license_url=self.provenance['license_url'], original_sha256=self.provenance['sha256'],
                              changes='20-second selection; explicit SMPTE170m→BT709 conversion; CFR30/H264 encoding; '
                                      'original audio replaced by stereo440/880Hz pulsed tones; RGB witness strip256×64; '
                                      + ('20-second video scene repeats to180 seconds, new continuous test audio.' if looped else 'No upscale.'))
        if name == 'uhd':
            provenance['changes'] = 'Generated3840×2160 testsrc motion; no natural4K-detail claim; RGB witness strip and stereo test tones.'
        start, end, preview = (120000, 126000, 120000) if name == 'long' else (2000, 8000, 0)
        return {'path': str(path), 'sha256': m.sha(path), 'bytes': path.stat().st_size, 'width': width, 'height': height,
                'duration_ms': duration * 1000, 'fps': 30, 'start_ms': start, 'end_ms': end, 'preview_start_ms': preview,
                'kind': kind, 'provenance': provenance, 'proof': proof}

    def run(self):
        for name, tool in self.tools.items():
            version = self.call(name, ['-version']).decode().splitlines()[0]
            m.require(version == tool['version_line'], 'Actual fixture tool version differs')
        if self.original:
            raw = json.loads(self.call('ffprobe', ['-v', 'error', '-show_entries', 'stream=codec_type,width,height,color_space,color_transfer,color_primaries',
                                                   '-of', 'json', str(self.original)]))
            video = [s for s in raw['streams'] if s['codec_type'] == 'video']
            m.require(len(video) == 1 and video[0]['width'] == 1920 and video[0]['height'] == 1080
                      and all(video[0][key] == 'smpte170m' for key in ('color_space', 'color_transfer', 'color_primaries')),
                      'This licensed-source recipe specifically expects the reviewed actual SMPTE170m1080 original')
        fixtures = {'main': self.generate('main', 1920, 1080, 20, self.original)}
        if self.args.uhd:
            fixtures['uhd'] = self.generate('uhd', 3840, 2160, 12)
        if self.args.long:
            fixtures['long'] = self.generate('long', 1920, 1080, 180, looped=fixtures['main']['path'])
        manifest = {'schema_version': 1, 'tools': self.tools, 'fixtures': fixtures}
        m.fixture_shape(manifest, public=self.original is not None)
        for name, fixture in fixtures.items():
            m.require(m.sha(fixture['path']) == fixture['sha256'], 'Fixture changed before freeze')
        (self.evidence.root / 'FIXTURES.json').write_bytes(m.canonical(manifest))
        print(json.dumps({'manifest': str(self.evidence.root / 'FIXTURES.json'),
                          'sha256': m.sha(self.evidence.root / 'FIXTURES.json'), 'kind': fixtures['main']['kind']}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--tools', type=Path, required=True)
    parser.add_argument('--expected-tools-sha256', required=True)
    parser.add_argument('--output-dir', type=Path, required=True)
    parser.add_argument('--original', type=Path)
    parser.add_argument('--provenance', type=Path)
    parser.add_argument('--expected-provenance-sha256')
    parser.add_argument('--uhd', action='store_true')
    parser.add_argument('--long', action='store_true')
    args = parser.parse_args()
    m.require(bool(args.original) == bool(args.provenance) == bool(args.expected_provenance_sha256), 'Natural source needs explicit external provenance identity')
    m.require(args.output_dir.is_absolute() and not args.output_dir.resolve().is_relative_to(Path(__file__).resolve().parents[1]),
              'Fixture output must be a fresh absolute directory outside checkout')
    Freeze(args).run()


if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Fixture freeze failed: ' + type(error).__name__ + ': ' + str(error), file=sys.stderr)
        sys.exit(1)
