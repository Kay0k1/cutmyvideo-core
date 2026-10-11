"""Meaningful bounded negative controls; no Docker, Go, PostgreSQL or media encode."""
import copy
import gzip
import hashlib
import http.server
import importlib.util
import io
import json
import math
import os
from pathlib import Path
import struct
import sys
import subprocess
import tarfile
import tempfile
import threading
import time
import unittest
from unittest.mock import patch


def load(name, path):
    spec = importlib.util.spec_from_file_location(name, Path(__file__).with_name(path))
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


m = load('media_capacity', 'media-capacity.py')
f = load('media_capacity_fixtures', 'media-capacity-fixtures.py')


def fixture():
    return {'path': '/owned/main.mp4', 'sha256': 'a' * 64, 'bytes': 1000, 'width': 1920, 'height': 1080,
            'duration_ms': 20000, 'fps': 30, 'start_ms': 2000, 'end_ms': 8000, 'preview_start_ms': 0,
            'kind': 'synthetic', 'provenance': {'generator_argv': ['ffmpeg', 'frozen'], 'tool_sha256': {'ffmpeg': 'a' * 64, 'ffprobe': 'a' * 64}},
            'proof': {key: True for key in ('metadata', 'cfr_pts', 'keyframes', 'physical_bt709', 'stereo_tones', 'full_decode')}}


def config():
    before = {'archive': '/owned/before.tar.gz', 'archive_sha256': m.F2_ARCHIVE, 'binary_sha256': m.F2_BINARY,
              'version': dict(m.F2), 'tree': 'a' * 40, 'runtime_source_sha256': 'a' * 64}
    after = copy.deepcopy(before)
    after.update(archive='/owned/after.tar.gz', archive_sha256='b' * 64, binary_sha256='c' * 64)
    after['version'].update(version='v0.0.0-candidate.g' + 'b' * 40, commit='b' * 40)
    return {'schema_version': 1, 'binaries': {'before': before, 'after': after},
            'runtime_image': 'sha256:' + 'a' * 64, 'postgres_image': 'sha256:' + 'b' * 64,
            'tools': {name: {'path': '/usr/bin/' + name, 'sha256': 'a' * 64, 'version_line': name + ' version frozen'}
                      for name in ('ffmpeg', 'ffprobe')},
            'resources': {'api_cpus': .5, 'worker_cpus': 2, 'postgres_cpus': .5,
                          'api_memory_bytes': 512 << 20, 'worker_memory_bytes': 1536 << 20,
                          'postgres_memory_bytes': 768 << 20, 'ffmpeg_threads': 2, 'gomaxprocs': 2, 'gomemlimit': '128MiB'},
            'fixture_manifest': {'path': '/owned/FIXTURES.json', 'sha256': 'a' * 64}, 'extensions': [], 'seed': 1729}


def transaction(index=0):
    return {'ok': True, 'index': index, 'warmup': False, 'checks': list(m.CHECKS),
            'preview_validation': {'full_decode': True, 'stereo_tones': [1,2], 'audio_alignment': {'edges':[200,700]},
                                   'ssim_mean': .95, 'frame_count': 600,'audio_windows':[{}, {}, {}]},
            'jobs': [{'mode': mode, 'sha256': 'a' * 64, 'bytes': 1000, 'queue': {'lower_ms': 0, 'upper_ms': 20},
                      'validation': {'full_decode': True, 'stereo_tones': [1, 2], 'audio_alignment': {'edges': [200, 700]},
                                     'ssim_mean': 1,'audio_span':{'ok':True}}} for mode in m.MODES]}


def summary():
    return {'schema_version': 1, 'ok': True, 'errors': [], 'shared_host': True, 'dedicated_verifier_cpu': False,
            'mode': 'pilot', 'extensions': [],
            'cells': [{'binary': label, 'cell': name, 'ok': True, 'transactions': [transaction(i) for i in range(3)]}
                      for label in ('before', 'after') for name, _, _, _ in m.CELLS]}


class IdentityControls(unittest.TestCase):
    def test_valid_reviewed_shape(self):
        m.config_shape(config())

    def test_quota_above_reviewed_host_bound_rejected(self):
        value = config(); value['resources']['worker_cpus'] = 3
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_baseline_commit_substitution_rejected(self):
        value = config(); value['binaries']['before']['version']['commit'] = 'b' * 40
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_baseline_binary_hash_substitution_rejected(self):
        value = config(); value['binaries']['before']['binary_sha256'] = 'b' * 64
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_mutable_image_tag_rejected(self):
        value = config(); value['runtime_image'] = 'ubuntu:latest'
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_relative_executable_archive_rejected(self):
        value = config(); value['binaries']['after']['archive'] = 'relative.tar.gz'
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_unknown_config_field_rejected(self):
        value = config(); value['fallback_binary'] = '/usr/bin/cutmy'
        with self.assertRaises(RuntimeError): m.config_shape(value)

    def test_external_json_digest_and_duplicate_keys_fail_closed(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'identity.json'; path.write_bytes(b'{"x":1,"x":2}\n')
            with self.assertRaises(RuntimeError): m.read_json(path, m.sha(path))
            path.write_bytes(b'{"x":1}\n')
            with self.assertRaises(RuntimeError): m.read_json(path, 'a' * 64)

    def test_symlink_is_not_an_external_verified_input(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'binary'; path.write_bytes(b'owned')
            link = Path(tmp) / 'link'; link.symlink_to(path)
            with self.assertRaises(RuntimeError): m.regular(link)


class ArchiveControls(unittest.TestCase):
    def archive(self, directory, attack=None):
        version = dict(m.F2)
        binary = b'controlled-test-only-not-an-application'
        identity = {'version': version, 'binary_sha256': hashlib.sha256(binary).hexdigest()}
        build = {key: version[key] for key in ('version', 'commit', 'build_time', 'os', 'arch')}
        build.update(toolchain=version['go_version'], binary_sha256=identity['binary_sha256'])
        payload = {'cutmy': binary, 'BUILD.json': m.canonical(build), 'README.txt': b'doc', 'LICENSE': b'doc',
                   'THIRD_PARTY_NOTICES.md': b'doc', 'CHANGELOG.md': b'doc'}
        path = directory / 'archive.tar.gz'
        prefix = 'cutmyvideo-core_v0.2.1_linux_amd64/'
        with tarfile.open(path, 'w:gz', format=tarfile.PAX_FORMAT) as tar:
            for name, data in payload.items():
                member = tarfile.TarInfo(prefix + name)
                member.size, member.mode = len(data), 0o755 if name == 'cutmy' else 0o644
                if name == 'cutmy' and attack == 'symlink':
                    member.type, member.linkname, member.size = tarfile.SYMTYPE, '/outside', 0
                if name == 'cutmy' and attack == 'traversal': member.name = '../cutmy'
                if name == 'cutmy' and attack == 'mode': member.mode = 0o777
                if name == 'cutmy' and attack == 'setuid': member.mode = 0o4755
                tar.addfile(member, io.BytesIO(data) if member.isfile() else None)
            if attack == 'extra':
                member = tarfile.TarInfo(prefix + 'unknown'); member.size = 1; member.mode = 0o644
                tar.addfile(member, io.BytesIO(b'x'))
        identity.update(archive=str(path), archive_sha256=m.sha(path))
        return identity

    def test_exact_payload_extracts_outside_without_running(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); identity = self.archive(root)
            binary = m.extract_binary(identity, root / 'extracted')
            self.assertEqual(m.sha(binary), identity['binary_sha256'])
            self.assertEqual(len(list(binary.parent.iterdir())), 6)

    def test_safe_archive_negative_controls(self):
        for attack in ('symlink', 'traversal', 'mode', 'setuid', 'extra'):
            with self.subTest(attack=attack), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp); identity = self.archive(root, attack)
                with self.assertRaises(RuntimeError): m.extract_binary(identity, root / 'extracted')
                self.assertFalse((root / 'extracted').exists())

    def test_archive_and_binary_digests_are_independent(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); identity = self.archive(root); identity['binary_sha256'] = 'a' * 64
            with self.assertRaises(RuntimeError): m.extract_binary(identity, root / 'extracted')
            identity = self.archive(root); identity['archive_sha256'] = 'a' * 64
            with self.assertRaises(RuntimeError): m.extract_binary(identity, root / 'extracted')


class FixtureAndAudioControls(unittest.TestCase):
    def test_old_generator_with_distinct_common_runtime_is_retained_and_reported(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / 'main.mp4'; source.write_bytes(b'x' * 1000)
            docker = root / 'docker-identity-only'; docker.write_bytes(b'not-executed')
            generated = {'schema_version': 1, 'tools': config()['tools'], 'fixtures': {'main': fixture()}}
            generated['fixtures']['main'].update(path=str(source), sha256=m.sha(source))
            runtime = config()
            for tool in runtime['tools'].values(): tool['sha256'] = 'b' * 64
            args = m.argparse.Namespace(config=root / 'CONFIG.json', expected_config_sha256='c' * 64,
                                       evidence_dir=root / 'evidence', scratch_dir=root, docker=docker, mode='pilot')
            with patch.object(m, 'read_json', side_effect=[runtime, generated]), \
                    patch.object(m.platform, 'system', return_value='Linux'), \
                    patch.object(m.platform, 'machine', return_value='x86_64'), \
                    patch.dict(os.environ, {}, clear=True), \
                    patch.object(m.Commands, 'run', side_effect=AssertionError('No command in source control')):
                experiment = m.Experiment(args)
                try:
                    report = experiment.report(False)
                    self.assertEqual(report['fixtures']['tools'], generated['tools'])
                    self.assertEqual(report['runtime_tools'], runtime['tools'])
                    self.assertNotEqual(report['fixtures']['tools'], report['runtime_tools'])
                    self.assertEqual(report['runtime_image'], runtime['runtime_image'])
                finally:
                    m.shutil.rmtree(experiment.work)

    def test_generation_sha_substitution_or_missing_identity_is_rejected(self):
        for attack in ('substitute', 'missing'):
            generated = {'schema_version': 1, 'tools': config()['tools'], 'fixtures': {'main': fixture()}}
            if attack == 'substitute': generated['fixtures']['main']['provenance']['tool_sha256']['ffprobe'] = 'b' * 64
            else: del generated['fixtures']['main']['provenance']['tool_sha256']['ffprobe']
            with self.subTest(attack=attack), self.assertRaises(RuntimeError): m.fixture_shape(generated)

    def test_synthetic_cannot_be_promoted_to_public_camera_result(self):
        value = {'schema_version': 1, 'tools': config()['tools'], 'fixtures': {'main': fixture()}}
        m.fixture_shape(value)
        with self.assertRaises(RuntimeError): m.fixture_shape(value, public=True)

    def test_missing_physical_witness_and_unlicensed_natural_rejected(self):
        for attack in ('proof', 'license'):
            value = {'schema_version': 1, 'tools': config()['tools'], 'fixtures': {'main': fixture()}}
            if attack == 'proof': value['fixtures']['main']['proof']['physical_bt709'] = False
            else: value['fixtures']['main']['kind'] = 'natural-camera-derived'
            with self.subTest(attack=attack), self.assertRaises(RuntimeError): m.fixture_shape(value, public=True)

    def physical(self, wrong=False):
        plane = 256 * 64; yuv = bytearray(plane * 3); rgb = bytearray(plane * 3)
        colors = ((240,20,20),(20,240,20),(20,20,240),(240,240,240))
        for index, color in enumerate(colors):
            pos = 32 * 256 + index * 64 + 32
            red, green, blue = (value / 255 for value in color)
            luminance = (.299 * red + .587 * green + .114 * blue) if wrong else (.2126 * red + .7152 * green + .0722 * blue)
            coefficients = (1.772,1.402) if wrong else (1.8556,1.5748)
            values = (16+219*luminance,128+224*(blue-luminance)/coefficients[0],128+224*(red-luminance)/coefficients[1])
            for component, value in enumerate(values): yuv[component*plane+pos] = round(value)
            rgb[pos*3:pos*3+3] = bytes(color)
        return bytes(yuv), bytes(rgb)

    def test_independent_709_formula_accepts_true_matrix(self):
        self.assertEqual(len(m.physical_709(*self.physical())),4)

    def test_601_physical_values_rejected_despite_correct_labels(self):
        with self.assertRaises(RuntimeError): m.physical_709(*self.physical(True))

    def pcm(self, swapped=False, shift=0, silence=False):
        values = []
        for index in range(43200):
            t = index / 48000
            amplitude = .2 if .2+shift <= t < .7+shift else .01
            for frequency in ((880,440) if swapped else (440,880)):
                values.append(0 if silence else amplitude*math.sin(2*math.pi*frequency*t))
        return struct.pack('<'+'f'*len(values),*values)

    def test_stereo_and_temporal_pulse_oracles_are_independent(self):
        pcm = self.pcm(); m.tone_witness(pcm)
        value = m.audio_alignment_witness(pcm,0,25)
        self.assertEqual(value['observed_edges_ms'],[200,700])

    def test_swapped_silent_and_shifted_audio_rejected(self):
        with self.assertRaises(RuntimeError): m.tone_witness(self.pcm(swapped=True))
        with self.assertRaises(RuntimeError): m.tone_witness(self.pcm(silence=True))
        with self.assertRaises(RuntimeError): m.audio_alignment_witness(self.pcm(shift=.1),0,45)

    def test_full_audio_span_rejects_silent_or_truncated_tail(self):
        good=self.pcm()+bytes(4800*8)
        self.assertTrue(m.audio_span_witness(good+good,2,0,45)['ok'])
        with self.assertRaises(RuntimeError):m.audio_span_witness(good,2,0,45)
        with self.assertRaises(RuntimeError):m.audio_span_witness(good+bytes(48000*8),2,0,45)

    def test_recipe_explicit_color_stereo_cfr_and_no_natural_uhd_upscale(self):
        args = f.generator_argv('/owned/ffmpeg','/owned/main.mp4',1920,1080,20,natural='/owned/original.webm')
        text = ' '.join(args)
        self.assertIn('colorspace=all=bt709:iall=smpte170m,fps=30',text)
        self.assertIn('out_color_matrix=bt709',text)
        self.assertIn('440',text);self.assertIn('880',text)
        uhd = f.generator_argv('/owned/ffmpeg','/owned/uhd.mp4',3840,2160,12)
        self.assertIn('testsrc=size=3840x2160:rate=30:duration=12',uhd)


class CopyTimelineControls(unittest.TestCase):
    def frames(self, prefix=180, tail=(180, 182, 184)):
        return [{'best_effort_timestamp_time': f'{index / 30:.6f}', 'key_frame': int(index == 0)}
                for index in (*range(prefix), *tail)]

    def artifact(self, start=2000, end=8100):
        return {'actual_start_ms': start, 'actual_end_ms': end}

    def test_dense_requested_prefix_and_observed_sparse_packet_tail(self):
        proof = m.copy_timeline_witness(self.frames(), fixture(), self.artifact(), 6.1)
        self.assertEqual(proof['requested_prefix_frames'], 180)
        self.assertEqual(proof['frame_count'], 183)
        self.assertEqual(proof['post_request_tail_pts_seconds'], [6.0, 6.066667, 6.133333])
        self.assertEqual(proof['post_request_tail_frame_count'], 3)
        self.assertEqual(proof['container_duration_seconds'], 6.1)
        self.assertEqual(proof['acknowledged_duration_seconds'], 6.1)
        # A preceding verified source keyframe extends the required dense prefix,
        # even though the user still requests the same end at source 8 seconds.
        proof = m.copy_timeline_witness(self.frames(210, (210, 212, 214)), fixture(), self.artifact(1000), 7.1)
        self.assertEqual(proof['requested_prefix_frames'], 210)
        self.assertEqual(proof['requested_prefix_seconds'], 7)

    def test_missing_shifted_or_internal_gap_prefix_rejected(self):
        for attack in ('missing', 'shifted', 'internal_gap', 'non_keyframe'):
            frames = self.frames()
            if attack == 'missing': del frames[90]
            elif attack == 'shifted':
                for frame in frames:
                    frame['best_effort_timestamp_time'] = str(float(frame['best_effort_timestamp_time']) + 1 / 30)
            elif attack == 'internal_gap':
                del frames[90]
                frames.insert(180, {'best_effort_timestamp_time': '6.033333', 'key_frame': 0})
            else: frames[0]['key_frame'] = 0
            with self.subTest(attack=attack), self.assertRaises(RuntimeError):
                m.copy_timeline_witness(frames, fixture(), self.artifact(), 6.1)
        # Container/ack end 7.9 seconds must not shorten the requested 8s prefix.
        with self.assertRaisesRegex(RuntimeError, 'requested prefix'):
            m.copy_timeline_witness(self.frames(177, ()), fixture(), self.artifact(end=7900), 5.9)

    def test_off_grid_reordered_duplicate_missing_or_nonfinite_pts_rejected(self):
        for attack in ('off_grid_prefix', 'off_grid_tail', 'reordered', 'duplicate', 'missing', 'nan', 'infinity'):
            frames = self.frames()
            if attack == 'off_grid_prefix': frames[90]['best_effort_timestamp_time'] = '3.004'
            elif attack == 'off_grid_tail': frames[-1]['best_effort_timestamp_time'] = '6.131'
            elif attack == 'reordered': frames[90], frames[91] = frames[91], frames[90]
            elif attack == 'duplicate': frames[91] = dict(frames[90])
            elif attack == 'missing': frames[90].pop('best_effort_timestamp_time')
            elif attack == 'nan': frames[90]['best_effort_timestamp_time'] = 'nan'
            else: frames[90]['best_effort_timestamp_time'] = 'inf'
            with self.subTest(attack=attack), self.assertRaises(RuntimeError):
                m.copy_timeline_witness(frames, fixture(), self.artifact(), 6.1)

    def test_unbounded_tail_and_false_acknowledgement_rejected(self):
        cases = [
            (self.frames(tail=(180, 182, 185)), self.artifact(), 6.1),
            (self.frames(), self.artifact(end=8000), 6.1),
            (self.frames(), self.artifact(end=8101), 6.1),
            (self.frames(), self.artifact(), 6.098),
            (self.frames(), self.artifact(start=1500), 6.6),
            (self.frames(), self.artifact(start=0), 8.1),
            (self.frames(), self.artifact(end=8200), 6.2),
        ]
        for frames, artifact, measured in cases:
            with self.subTest(artifact=artifact, measured=measured), self.assertRaises(RuntimeError):
                m.copy_timeline_witness(frames, fixture(), artifact, measured)
        # Published milliseconds are rounded, not an arbitrary 120ms tolerance.
        m.copy_timeline_witness(self.frames(), fixture(), self.artifact(), 6.1004)

    def test_copy_acknowledgement_matches_positive_go_round_at_half_millisecond(self):
        # Keep the tail away from its limit so these controls isolate Go's
        # positive math.Round rule, including the exact .5ms tie.
        frames = self.frames(tail=(180, 181, 182))
        for measured, rounded_ms in ((6.0995, 6100), (6.0985, 6099),
                                     (6.099499, 6099), (6.099501, 6100)):
            with self.subTest(measured=measured):
                m.copy_timeline_witness(frames, fixture(), self.artifact(end=2000 + rounded_ms), measured)
                wrong_ms = rounded_ms - 1 if measured in (6.0995, 6.0985, 6.099501) else rounded_ms + 1
                with self.assertRaisesRegex(RuntimeError, 'rounded measured'):
                    m.copy_timeline_witness(frames, fixture(), self.artifact(end=2000 + wrong_ms), measured)

    def test_accurate_keeps_uniform_cfr_and_copy_still_reaches_full_decode(self):
        video = {'codec_type': 'video', 'width':1920, 'height':1080}
        audio = {'codec_type': 'audio', 'channels':2}
        class StopAtFullDecode(Exception): pass
        for mode in ('accurate', 'copy'):
            experiment = object.__new__(m.Experiment)
            probe = {'streams':[video, audio], 'format':{'duration':'6.1'}}
            experiment.probe = lambda path, work, frames=False: {'frames':self.frames()} if frames else probe
            calls = []
            def full_decode(tool, argv, work):
                calls.append((tool, argv))
                raise StopAtFullDecode
            experiment.container_tool = full_decode
            artifact = self.artifact() if mode == 'copy' else self.artifact(end=8000)
            if mode == 'accurate':
                with self.assertRaisesRegex(RuntimeError, 'lost CFR'):
                    experiment.validate_media(Path('/owned/copy.mp4'), fixture(), mode, artifact, Path('/owned'))
                self.assertEqual(calls, [])
            else:
                with self.assertRaises(StopAtFullDecode):
                    experiment.validate_media(Path('/owned/copy.mp4'), fixture(), mode, artifact, Path('/owned'))
                self.assertEqual(len(calls), 1)
                self.assertIn('-xerror', calls[0][1])
                self.assertEqual(calls[0][1][-3:], ['-f', 'null', '-'])


class ReportAndQueueControls(unittest.TestCase):
    def test_native_uhd_is_bounded_two_seconds_after_regular_jobs(self):
        value=fixture()
        self.assertEqual(m.job_request('owned',value,'native_uhd')['ranges'][0]['end_ms'],4000)
        self.assertEqual(m.job_request('owned',value,'accurate')['ranges'][0]['end_ms'],8000)
    def test_uhd_requires_one_jobs_check_and_all_four_outputs(self):
        row=transaction();row['jobs'].append({**copy.deepcopy(row['jobs'][0]), 'mode':'native_uhd'})
        m.validate_transaction(row,True)
        row['checks'].append('jobs')
        with self.assertRaises(RuntimeError):m.validate_transaction(row,True)

    def test_preview_oracle_rejects_valid_but_wrong_content_or_silence(self):
        for attack in ('silent','black','missing'):
            row=transaction()
            if attack=='silent':row['preview_validation']['stereo_tones']=[]
            elif attack=='black':row['preview_validation']['ssim_mean']=.1
            else:row.pop('preview_validation')
            with self.subTest(attack=attack),self.assertRaises(RuntimeError):m.validate_transaction(row)

    def preview_probe(self, revision):
        width, height, sar = m.PREVIEW_GEOMETRY[revision]
        return {'streams': [{'codec_type':'video', 'codec_name':'h264', 'pix_fmt':'yuv420p',
                             'width':width, 'height':height, 'sample_aspect_ratio':sar, 'color_range':'tv',
                             'color_space':'bt709', 'color_transfer':'bt709', 'color_primaries':'bt709'},
                            {'codec_type':'audio', 'codec_name':'aac', 'channels':2, 'sample_rate':'48000'}],
                'format':{'duration':'20.021'}}

    def test_preview_header_geometry_and_common_24fps_witness(self):
        source={**fixture(),'duration_ms':20021}
        headers={'x-preview-start-ms':'0','x-preview-end-ms':'20021'}
        self.assertEqual(m.PREVIEW_FPS,24)
        frames=[{'best_effort_timestamp_time':str(i/24)} for i in range(480)]
        for revision in m.PREVIEW_GEOMETRY:
            probe=self.preview_probe(revision)
            proof=m.preview_metadata_witness(probe,frames,source,headers,24,revision)
            self.assertEqual(proof['frame_count'],480)
            self.assertEqual(proof['revision'],revision)
            with self.assertRaises(RuntimeError):m.preview_metadata_witness(probe,frames,source,headers,30,revision)
            for attack in ('header','geometry','sar','audio','colour','codec'):
                p,h=copy.deepcopy(probe),dict(headers)
                if attack=='header':h['x-preview-end-ms']='20000'
                elif attack=='geometry':p['streams'][0]['width']=2
                elif attack=='sar':p['streams'][0]['sample_aspect_ratio']='640:639'
                elif attack=='audio':p['streams'][1]['channels']=1
                elif attack=='colour':p['streams'][0]['color_space']='smpte170m'
                else:p['streams'][0]['codec_name']='mpeg4'
                with self.subTest(revision=revision,attack=attack),self.assertRaises(RuntimeError):
                    m.preview_metadata_witness(p,frames,source,h,24,revision)
            wrong=[{'best_effort_timestamp_time':str(i/30)} for i in range(600)]
            with self.assertRaises(RuntimeError):m.preview_metadata_witness(probe,wrong,source,headers,24,revision)

    def test_preview_geometry_is_revision_bound_and_displayed_aspect_independent(self):
        source={**fixture(),'duration_ms':20021}
        headers={'x-preview-start-ms':'0','x-preview-end-ms':'20021'}
        frames=[{'best_effort_timestamp_time':str(i/24)} for i in range(480)]
        before,after=m.PREVIEW_GEOMETRY
        for revision,other in ((before,after),(after,before)):
            with self.subTest(revision=revision),self.assertRaises(RuntimeError):
                m.preview_metadata_witness(self.preview_probe(other),frames,source,headers,24,revision)
        with self.assertRaises(RuntimeError):
            m.preview_metadata_witness(self.preview_probe(before),frames,source,headers,24,'d'*40)
        changed_source={**source,'width':2000}
        with self.assertRaisesRegex(RuntimeError,'displayed aspect'):
            m.preview_metadata_witness(self.preview_probe(before),frames,changed_source,headers,24,before)
        self.assertEqual(m.preview_metadata_witness(self.preview_probe(before),frames,source,headers,24,before)['displayed_aspect_ratio'],'16/9')
        self.assertEqual(m.preview_metadata_witness(self.preview_probe(after),frames,source,headers,24,after)['displayed_aspect_ratio'],'71/40')

    def test_measured_cpu_delta_fails_reset_or_missing_role(self):
        fields=('usage_usec','user_usec','system_usec','nr_periods','nr_throttled','throttled_usec')
        before={role:{'cpu':{key:10 for key in fields}} for role in ('api','worker','postgres')}
        after={role:{'cpu':{key:20 for key in fields}} for role in before}
        self.assertEqual(m.cpu_delta(before,after)['worker']['usage_usec'],10)
        after['worker']['cpu']['usage_usec']=1
        with self.assertRaises(RuntimeError):m.cpu_delta(before,after)

    def test_unavailable_kernel_memory_peak_is_null_not_rss(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for name,value in {'memory.current':'123','memory.events':'oom 0\noom_kill 0\nhigh 2\n',
                               'memory.stat':'anon 12\nfile 111\n','memory.swap.current':'0'}.items():
                (root/name).write_text(value)
            value=m.cgroup_memory(root)
            self.assertIsNone(value['memory_lifetime_peak_bytes'])
            self.assertFalse(value['memory_lifetime_peak_available'])
            self.assertEqual(value['memory_current_bytes'],123)
            (root/'memory.peak').write_text('456')
            self.assertTrue(m.cgroup_memory(root)['memory_lifetime_peak_available'])
            (root/'memory.events').write_text('oom 1\noom_kill 0\n')
            with self.assertRaises(RuntimeError):m.cgroup_memory(root)

    def test_complete_pilot_has_no_tail_descriptor(self):
        m.validate_summary(summary())
        self.assertNotIn('p95_nearest_rank',m.describe([1,2,3]))
        with self.assertRaises(RuntimeError): m.describe([1,2,3],True)

    def test_nearest_rank_all_samples_and_invalid_numbers(self):
        self.assertEqual(m.nearest_rank(list(range(1,21)),.95),19)
        with self.assertRaises(RuntimeError):m.nearest_rank([1,float('nan')],.95)
        with self.assertRaises(RuntimeError):m.nearest_rank([], .95)

    def test_queue_can_claim_before_http_ack(self):
        self.assertEqual(m.queue_bounds(100,200,None,150)['lower_ms'],0)

    def test_queue_bounds_include_clock_uncertainty(self):
        value=m.queue_bounds(100_000_000,120_000_000,150_000_000,180_000_000)
        self.assertEqual((value['lower_ms'],value['upper_ms']),(30,80))
        with self.assertRaises(RuntimeError):m.queue_bounds(200,100,None,300)

    def test_missing_skipped_duplicate_and_bad_decode_rejected(self):
        for attack in ('missingcell','duplicaterow','skip','decode','hash','queue','quality','cleanup','isolation'):
            value=summary()
            if attack=='missingcell':value['cells'].pop()
            elif attack=='duplicaterow':value['cells'][0]['transactions'][1]['index']=0
            elif attack=='skip':value['cells'][0]['transactions'][0]['checks'].pop()
            elif attack=='decode':value['cells'][0]['transactions'][0]['jobs'][0]['validation']['full_decode']=False
            elif attack=='hash':value['cells'][0]['transactions'][0]['jobs'][0]['sha256']='wrong'
            elif attack=='queue':value['cells'][0]['transactions'][0]['jobs'][0]['queue']['lower_ms']=21
            elif attack=='quality':value['cells'][0]['transactions'][0]['jobs'][0]['validation']['ssim_mean']=.1
            elif attack=='cleanup':value['cells'][0]['cleanup_errors']=['failed']
            elif attack=='isolation':value['dedicated_verifier_cpu']=True
            with self.subTest(attack=attack),self.assertRaises(RuntimeError):m.validate_summary(value)

    def test_linux_stat_comm_parenthesis_and_cpu_scope(self):
        fields=['S']+['0']*21
        fields[11]='100';fields[12]='50';fields[13]='70';fields[14]='30';fields[19]='1234';fields[21]='20'
        value=m.process_stat('123 (command with ) character) '+' '.join(fields),4096,100)
        self.assertEqual(value,{'start_ticks':1234,'own_cpu_seconds':1.5,'reaped_children_cpu_seconds':1.0,'rss_bytes':81920})


class RealBoundedIOControls(unittest.TestCase):
    def resource_fixture(self, tmp):
        root = Path(tmp)
        cgroot = root / 'cgroup'; cgroot.mkdir()
        identities = {}
        for index, role in enumerate(('api', 'worker', 'postgres'), 1):
            pid = 100 + index; cid = str(index) * 64
            group = cgroot / cid; group.mkdir()
            (group / 'cpu.max').write_text(('200000' if role == 'worker' else '50000') + ' 100000\n')
            (group / 'memory.max').write_text(str({'api':512,'worker':1536,'postgres':768}[role] << 20))
            (group / 'memory.swap.max').write_text('0')
            proc = root / 'proc' / str(pid); proc.mkdir(parents=True)
            (proc / 'cgroup').write_text('0::/' + cid + '\n')
            fields = ['S'] + ['0'] * 21; fields[19] = str(1000 + index)
            (proc / 'stat').write_text(str(pid) + ' (owned-service) ' + ' '.join(fields))
            identities[role] = (pid, cid)
        class OwnedDocker:
            token = 'cutmy-capacity-' + 'a' * 16
            def info(self, name):
                return {'Running':True, 'Pid':identities[name][0], 'StartedAt':'2026-10-11T00:00:00Z'}
            def json(self, *args):return identities[args[-1]][1]
        def selected_path(value):
            value = str(value)
            if value.startswith('/proc/'):return root / value.lstrip('/')
            if value == '/sys/fs/cgroup':return cgroot
            return Path(value)
        return OwnedDocker(), selected_path, cgroot, identities

    def test_actual_resource_binding_receipt_precedes_sampling(self):
        with tempfile.TemporaryDirectory() as tmp:
            docker, selected_path, cgroot, identities = self.resource_fixture(tmp)
            evidence = m.Evidence(Path(tmp) / 'evidence')
            with patch.object(m, 'Path', side_effect=selected_path):
                resources = m.Resources(docker, {role:role for role in identities}, evidence, {'binary':'before','cell':'F1'})
            self.assertIsNone(resources.thread.ident, 'Resource receipt must exist before any sampler starts')
            record = json.loads((evidence.root / 'resource_binding.jsonl').read_text())
            self.assertEqual(set(record['roles']), {'api','worker','postgres'})
            self.assertEqual(sum(role['effective_cpus'] for role in record['roles'].values()),3)
            for role, (pid,cid) in identities.items():
                value = record['roles'][role]
                self.assertEqual(value['container_id'],cid);self.assertEqual(value['root_pid'],pid)
                self.assertEqual(value['cgroup_path'],str(cgroot / cid))
                self.assertEqual(value['memory_max_bytes'],{'api':512,'worker':1536,'postgres':768}[role] << 20)
                self.assertEqual(value['memory_swap_max_bytes'],0)
                self.assertEqual(value['root_start_ticks'],1000 + ('api','worker','postgres').index(role) + 1)

    def test_resource_binding_refuses_wrong_quota_limits_shared_group_or_identity(self):
        for attack in ('cpu','memory','swap','shared','containerid','duplicate'):
            with self.subTest(attack=attack), tempfile.TemporaryDirectory() as tmp:
                docker, selected_path, cgroot, identities = self.resource_fixture(tmp)
                first = identities['api']; group = cgroot / first[1]
                if attack == 'cpu': (group / 'cpu.max').write_text('75000 100000')
                elif attack == 'memory': (group / 'memory.max').write_text(str(1024 << 20))
                elif attack == 'swap': (group / 'memory.swap.max').write_text('4096')
                elif attack == 'shared': (Path(tmp) / 'proc' / str(first[0]) / 'cgroup').write_text('0::/\n')
                elif attack == 'containerid': docker.json = lambda *args:'unverified'
                elif attack == 'duplicate': identities['postgres'] = first
                evidence = m.Evidence(Path(tmp) / 'evidence')
                with patch.object(m, 'Path', side_effect=selected_path), self.assertRaises(RuntimeError):
                    m.Resources(docker, {role:role for role in identities}, evidence, {'binary':'before','cell':'F1'})
                self.assertFalse((evidence.root / 'resource_binding.jsonl').exists())

    def test_temporary_postgres_socket_readiness_does_not_release_tcp_barrier(self):
        class StartingPostgres:
            argv = ['/owned/docker', '--host', 'unix:///var/run/docker.sock']
            environment = {'PATH': '/usr/bin'}
            def info(self, name):
                if name != 'owned-postgres': raise AssertionError(name)
                return {'Running': True}
        calls = []
        def readiness(args, **kwargs):
            calls.append(args)
            # A socket-only initialization server is ready on every attempt.
            # Only the final server accepts TCP on the third attempt.
            tcp = ['-h', '127.0.0.1', '-p', '5432']
            ready = args[args.index('pg_isready') + 1:][:4] != tcp or len(calls) >= 3
            return subprocess.CompletedProcess(args, 0 if ready else 1)
        with patch.object(m.subprocess, 'run', side_effect=readiness), patch.object(m.time, 'sleep'):
            m.wait_postgres_ready(StartingPostgres(), 'owned-postgres')
        self.assertEqual(len(calls), 3, 'Temporary socket readiness must not release application startup')
        self.assertTrue(all(args[-4:] == ['-U', 'postgres', '-d', 'cutmy'] for args in calls))

    def test_postgres_startup_deadline_and_exit_are_terminal(self):
        class ExitedPostgres:
            argv = ['/owned/docker']; environment = {}
            def info(self, name): return {'Running': False}
        with patch.object(m.subprocess, 'run') as process:
            with self.assertRaisesRegex(RuntimeError, 'TCP-ready'):
                m.wait_postgres_ready(ExitedPostgres(), 'owned-postgres', timeout=0)
            with self.assertRaisesRegex(RuntimeError, 'exited'):
                m.wait_postgres_ready(ExitedPostgres(), 'owned-postgres')
            process.assert_not_called()

    def endpoint_proof(self):
        token = 'cutmy-capacity-' + 'a' * 16
        network = token + '-before-F1'
        return {'task_label': token, 'network_name': network, 'api_name': token + '-api',
                'network': {'id': 'b' * 64, 'name': network, 'driver': 'bridge', 'internal': True,
                            'labels': {m.LABEL: token}, 'ipam': [{'Subnet': '172.30.240.0/24', 'Gateway': '172.30.240.1'}]},
                'api': {'id': 'c' * 64, 'name': '/' + token + '-api', 'running': True,
                        'labels': {m.LABEL: token}, 'ports': {},
                        'networks': {network: {'NetworkID': 'b' * 64, 'IPAddress': '172.30.240.3', 'IPPrefixLen': 24}}}}

    def test_actual_owned_internal_endpoint_inspection_and_no_published_port(self):
        proof = self.endpoint_proof()
        class Inspection:
            def __init__(self, evidence): self.evidence, self.calls = evidence, []
            def run(self, args, **kwargs):
                self.calls.append(args)
                command = args[3:]
                if command[0] == 'run': return b'owned-created\n', b''
                if command[:2] == ['network', 'inspect']:
                    if command[-1] != proof['network_name']: raise AssertionError(command)
                    return m.canonical(proof['network']), b''
                if command[0] == 'inspect':
                    if command[-1] != proof['api_name']: raise AssertionError(command)
                    return m.canonical(proof['api']), b''
                raise AssertionError(command)
        with tempfile.TemporaryDirectory() as tmp, patch.dict(os.environ, {}, clear=True):
            daemon = Inspection(m.Evidence(Path(tmp) / 'evidence'))
            docker = m.Docker(daemon, '/owned/docker', proof['task_label'])
            docker.networks.add(proof['network_name'])
            name = docker.start('api', 'sha256:' + 'd' * 64, proof['network_name'], .5, 512 << 20, ['/binary/cutmy', 'server'])
            endpoint = docker.api_endpoint(name, proof['network_name'])
            self.assertEqual(m.endpoint_address(endpoint), ('172.30.240.3', 8080))
            self.assertFalse(any('--publish' in args for args in daemon.calls))
            self.assertIn('"internal":{{json .Internal}}', daemon.calls[-2][-2])
            stored = json.loads((daemon.evidence.root / 'api_endpoint.jsonl').read_text())
            self.assertEqual(stored, endpoint)
            with self.assertRaises(RuntimeError): docker.api_endpoint(name, 'foreign-network')

    def test_foreign_ambiguous_or_non_internal_network_endpoint_rejected(self):
        for attack in ('foreignlabel', 'foreignname', 'notinternal', 'wrongdriver', 'twosubnets', 'publicsubnet', 'ipv6', 'missingid'):
            proof = self.endpoint_proof(); network = proof['network']
            if attack == 'foreignlabel': network['labels'][m.LABEL] = 'foreign'
            elif attack == 'foreignname': network['name'] = 'foreign'
            elif attack == 'notinternal': network['internal'] = False
            elif attack == 'wrongdriver': network['driver'] = 'overlay'
            elif attack == 'twosubnets': network['ipam'] *= 2
            elif attack == 'publicsubnet': network['ipam'][0]['Subnet'] = '8.8.8.0/24'
            elif attack == 'ipv6': network['ipam'][0]['Subnet'] = 'fd00::/64'
            elif attack == 'missingid': network['id'] = None
            with self.subTest(attack=attack), self.assertRaises(RuntimeError): m.api_endpoint_from_proof(proof)

    def test_stopped_foreign_extra_membership_and_outside_api_address_rejected(self):
        for attack in ('stopped', 'foreignlabel', 'extra', 'missing', 'wrongid', 'wrongprefix', 'outside', 'gateway', 'broadcast', 'loopback', 'publish'):
            proof = self.endpoint_proof(); api = proof['api']; membership = api['networks'][proof['network_name']]
            if attack == 'stopped': api['running'] = False
            elif attack == 'foreignlabel': api['labels'][m.LABEL] = 'foreign'
            elif attack == 'extra': api['networks']['foreign'] = dict(membership)
            elif attack == 'missing': api['networks'] = {}
            elif attack == 'wrongid': membership['NetworkID'] = 'd' * 64
            elif attack == 'wrongprefix': membership['IPPrefixLen'] = 16
            elif attack == 'outside': membership['IPAddress'] = '172.30.241.3'
            elif attack == 'gateway': membership['IPAddress'] = '172.30.240.1'
            elif attack == 'broadcast': membership['IPAddress'] = '172.30.240.255'
            elif attack == 'loopback': membership['IPAddress'] = '127.0.0.1'
            elif attack == 'publish': api['ports'] = {'8080/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '12345'}]}
            with self.subTest(attack=attack), self.assertRaises(RuntimeError): m.api_endpoint_from_proof(proof)

    def test_http_rejects_arbitrary_unproved_or_changed_endpoint_before_connection(self):
        correct = m.api_endpoint_from_proof(self.endpoint_proof())
        changed_host = copy.deepcopy(correct); changed_host['host'] = '8.8.8.8'
        changed_port = copy.deepcopy(correct); changed_port['port'] = 80
        with patch.object(m.http.client, 'HTTPConnection') as connection:
            for endpoint in (None, 8080, 'http://example.test', {'host': '8.8.8.8', 'port': 8080}, changed_host, changed_port):
                with self.subTest(endpoint=endpoint), self.assertRaises(RuntimeError): m.HTTP(endpoint, None, 'controlled')
            connection.assert_not_called()
        client = m.HTTP(correct, None, 'controlled')
        self.assertEqual((client.host, client.port), ('172.30.240.3', 8080))

    def test_remote_docker_overrides_fail_before_any_command(self):
        class NoCalls:
            def run(self,*args,**kwargs):self.fail('Docker command must not run')
        for key in m.DOCKER_OVERRIDES:
            with self.subTest(key=key),patch.dict(os.environ,{key:'forbidden'},clear=True):
                with self.assertRaises(RuntimeError):m.Docker(NoCalls(),'/owned/docker','owned')
        self.assertNotIn('DOCKER_HOST',m.Docker.local_environment({'PATH':'/usr/bin','DOCKER_HOST':''}))

    def test_failed_creation_is_discovered_by_exact_run_label(self):
        class PartialDaemon:
            def __init__(self,evidence):self.calls=[];self.created=set();self.evidence=evidence
            def run(self,args,**kwargs):
                self.calls.append(args)
                self_token='owned-token'
                self.assert_endpoint=args[1:3]==['--host','unix:///var/run/docker.sock']
                command=args[3:]
                if command[0]=='run':
                    self.created.add('owned-token-api');raise RuntimeError('Lost create reply')
                if command[:2]==['network','create']:
                    self.created.add('owned-network');raise RuntimeError('Lost network reply')
                if command[:2]==['ps','-a']:return (b'owned-token-api\n' if 'owned-token-api' in self.created else b''),b''
                if command[:2]==['network','ls']:return (b'owned-network\n' if 'owned-network' in self.created else b''),b''
                if command[0]=='inspect':
                    result={m.LABEL:self_token} if 'Labels' in command[2] else {'Running':True}
                    return m.canonical(result),b''
                if command[:2]==['network','inspect']:return m.canonical({m.LABEL:self_token}),b''
                if command[0]=='rm':self.created.remove(command[-1]);return b'',b''
                if command[0]=='logs':return b'owned synthetic log',b''
                if command[:2]==['network','rm']:self.created.remove(command[-1]);return b'',b''
                raise AssertionError(command)
        with tempfile.TemporaryDirectory() as tmp:
            daemon=PartialDaemon(m.Evidence(Path(tmp)/'evidence'))
            with patch.dict(os.environ,{},clear=True):docker=m.Docker(daemon,'/owned/docker','owned-token')
            with self.assertRaises(RuntimeError):docker.start('api','sha256:'+'a'*64,'owned-network',.5,123,['/binary/cutmy','server'])
            with self.assertRaises(RuntimeError):docker.network('owned-network')
            self.assertFalse(docker.containers);self.assertFalse(docker.networks)
            docker.cleanup()
            self.assertFalse(daemon.created);self.assertTrue(daemon.assert_endpoint)
            discovery=[args for args in daemon.calls if '--filter' in args]
            self.assertTrue(all('label=cutmy.task=owned-token' in args for args in discovery))

    def test_partial_cell_failure_reaps_persistent_client(self):
        with tempfile.TemporaryDirectory() as tmp:
            evidence=m.Evidence(Path(tmp)/'evidence')
            class FakeDocker:
                argv=['/owned/docker'];environment=dict(os.environ)
                def info(self,*args):return {'Running':True}
            real_popen=subprocess.Popen
            def owned_client(*args,**kwargs):
                return real_popen([sys.executable,'-c','import sys; sys.stdin.readline()'],**kwargs)
            experiment=object.__new__(m.Experiment)
            experiment.local_closers=[];experiment.evidence=evidence;experiment.cells=[]
            clients=[]
            def failed_start(*args):
                with patch.object(m.subprocess,'Popen',side_effect=owned_client):
                    client=m.PSQL(FakeDocker(),'owned',evidence)
                clients.append(client);experiment.local_closers.append(client.close)
                raise RuntimeError('Resources validation failure')
            experiment.start_cell=failed_start
            with self.assertRaisesRegex(RuntimeError,'Resources validation'):
                experiment.run_cell('before',('F1','fast',1,1),fixture(),3)
            self.assertEqual(clients[0].process.returncode,0)
            self.assertTrue(clients[0].error_file.closed);self.assertTrue(clients[0].process.stdout.closed)
            self.assertFalse(experiment.local_closers)

    def test_cleanup_attempts_every_staged_resource_after_one_failure(self):
        calls=[]
        def first():calls.append(1);raise RuntimeError('first')
        closers=[first,lambda:calls.append(2)]
        self.assertEqual(len(m.close_all(closers)),1);self.assertEqual(calls,[1,2]);self.assertFalse(closers)

    def test_failed_media_retained_bounded_without_cookies_env_or_pgdata(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)/'cell';root.mkdir();evidence=m.Evidence(Path(tmp)/'evidence')
            (root/'media').mkdir();(root/'media'/'source.media').write_bytes(b'owned-media')
            (root/'pgdata').mkdir();(root/'pgdata'/'PG_VERSION').write_bytes(b'private')
            (root/'app.env').write_bytes(b'COOKIE=DO_NOT_PERSIST')
            result=m.retain_failed_media(root,evidence,'before-F1',bound=20)
            self.assertEqual(result['retained_bytes'],11);self.assertEqual(len(result['files']),1)
            self.assertEqual((evidence.root/result['files'][0]['file']).read_bytes(),b'owned-media')

    def test_failed_daemon_cleanup_preserves_mounted_private_work(self):
        class Mounted:
            def cleanup(self):raise RuntimeError('Owned daemon rm failure')
        class Removed:
            def cleanup(self):pass
        with tempfile.TemporaryDirectory() as tmp:
            work=Path(tmp)/'owned';work.mkdir();(work/'database').write_bytes(b'existing-owned-database')
            with self.assertRaisesRegex(RuntimeError,'rm failure'):m.cleanup_owned_work(Mounted(),work)
            self.assertEqual((work/'database').read_bytes(),b'existing-owned-database')
            m.cleanup_owned_work(Removed(),work);self.assertFalse(work.exists())

    def test_retention_failure_preserves_private_work_after_successful_docker_cleanup(self):
        class Removed:
            def __init__(self):
                self.cleaned=False;self.networks={'owned-network'};self.argv=['/owned/docker'];self.environment={}
            def cleanup(self):self.cleaned=True
            def remove(self,name):pass
            def run(self,*args):return b''
        class Sampler:
            phase='startup'
            failure_details=None
            failure_diagnostic_error=None
            def summary(self):return {}
        class Logs:
            def run(self,*args,**kwargs):return b'owned-log',b''
        with tempfile.TemporaryDirectory() as tmp:
            work=Path(tmp)/'owned';work.mkdir();(work/'partial.mp4').write_bytes(b'owned-partial-bytes')
            evidence=m.Evidence(Path(tmp)/'evidence');docker=Removed()
            experiment=object.__new__(m.Experiment)
            experiment.local_closers=[];experiment.evidence=evidence;experiment.cells=[];experiment.retain_work=False
            experiment.docker=docker;experiment.commands=Logs()
            experiment.start_cell=lambda *args:{'root':work,'sampler':Sampler(),'worker':'owned-worker',
                'api':'owned-api','postgres':'owned-pg','network':'owned-network'}
            def failed_transaction(*args):raise RuntimeError('Failed actual output witness')
            experiment.transaction=failed_transaction
            with patch.object(m,'retain_failed_media',side_effect=OSError('Copy/hash preservation failure')):
                with self.assertRaises(RuntimeError):experiment.run_cell('before',('F1','fast',1,1),fixture(),3)
            self.assertTrue(experiment.retain_work)
            self.assertIn('preservation failure',experiment.cells[0]['cleanup_errors'][0])
            m.cleanup_owned_work(docker,work,keep_work=experiment.retain_work)
            self.assertTrue(docker.cleaned);self.assertEqual((work/'partial.mp4').read_bytes(),b'owned-partial-bytes')

    def test_command_timeout_retains_failure_and_kills_owned_process_group(self):
        with tempfile.TemporaryDirectory() as tmp:
            evidence=m.Evidence(Path(tmp)/'evidence')
            with self.assertRaises(RuntimeError):
                m.Commands(evidence).run([sys.executable,'-c','import time; time.sleep(2)'],timeout=.1)
            records=[json.loads(line) for line in (evidence.root/'commands.jsonl').read_text().splitlines()]
            self.assertFalse(records[0]['ok']);self.assertIn('watchdog',records[0]['error'])

    def test_real_http_cookie_redaction_error_retention_and_response_bound(self):
        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path.endswith('large'):
                    body=b'x'*100;code=200;content='application/octet-stream'
                else:
                    body=b'{"ok":true}';code=503 if self.path.endswith('failure') else 200;content='application/json'
                self.send_response(code);self.send_header('Content-Type',content)
                self.send_header('Set-Cookie','cutmy_session=DO_NOT_PERSIST_COOKIE; HttpOnly')
                self.send_header('Content-Length',str(len(body)));self.end_headers();self.wfile.write(body)
            def log_message(self,*args):pass
        server=http.server.ThreadingHTTPServer(('127.0.0.1',0),Handler)
        thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
        try:
            with tempfile.TemporaryDirectory() as tmp:
                evidence=m.Evidence(Path(tmp)/'evidence')
                # This pure HTTP fixture uses its own loopback server; production
                # callers must pass the separately validated Docker endpoint proof.
                with patch.object(m,'endpoint_address',return_value=('127.0.0.1',server.server_port)):
                    client=m.HTTP({},evidence,'control')
                value,_=client.request('GET','/api/v1/session');self.assertTrue(value['ok'])
                with self.assertRaises(RuntimeError):client.request('GET','/api/v1/failure')
                old=m.JSON_LIMIT;m.JSON_LIMIT=64
                try:
                    with self.assertRaises(RuntimeError):client.request('GET','/api/v1/large')
                finally:m.JSON_LIMIT=old
                raw=(evidence.root/'http.jsonl').read_text()
                self.assertNotIn('DO_NOT_PERSIST_COOKIE',raw)
                rows=[json.loads(line) for line in raw.splitlines()]
                self.assertEqual(rows[1]['status'],503);self.assertFalse(rows[1]['ok']);self.assertFalse(rows[2]['ok'])
        finally:
            server.shutdown();server.server_close();thread.join(timeout=2)


if __name__=='__main__':unittest.main()
