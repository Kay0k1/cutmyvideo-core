"""Offline parser/comparison controls. Never invokes FFmpeg, API or containers."""
import copy
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('full_decode_controls', Path(__file__).with_name('full-decode.py'))
full = importlib.util.module_from_spec(SPEC); SPEC.loader.exec_module(full)


def framehash():
    return ('#format: frame checksums\n#version: 2\n#hash: SHA256\n'
            '#tb 0: 1/25\n#media_type 0: video\n#tb 1: 1/48000\n#media_type 1: audio\n'
            '0, 0, 0, 1, 3456, ' + 'a' * 64 + '\n'
            '1, 0, 0, 1024, 8192, ' + 'b' * 64 + '\n'
            '0, 1, 1, 1, 3456, ' + 'c' * 64 + '\n').encode()


def pair():
    common = full.parse_framehash(framehash())
    common.pop('decoded_audio_pcm_bytes')
    common.update(decoded_audio_samples_per_channel=1024, packet_clocks_sha256='d' * 64,
                  packet_payloads_sha256='e' * 64, media_structure_sha256='f' * 64, packet_count=3)
    left = {'tool_binding': {'ffmpeg': {'executable_sha256': '1' * 64}},
            'outputs': [{**common, 'rendition': 'final', 'clip_id': 'clip-' + str(i),
                         'encoded_file_sha256': '2' * 64} for i in range(15)]}
    return left, copy.deepcopy(left)


class OfflineControls(unittest.TestCase):
    def setUp(self):
        patcher = patch.object(full.subprocess, 'Popen', side_effect=AssertionError('native_execution_forbidden'))
        patcher.start(); self.addCleanup(patcher.stop)

    def test_full_video_audio_parser(self):
        result = full.parse_framehash(framehash())
        self.assertEqual((result['decoded_video_frames'], result['decoded_audio_frames']), (2, 1))
        self.assertEqual(result['decoded_audio_pcm_bytes'], 8192)

    def test_rejects_missing_audio_rows(self):
        value = b'\n'.join(line for line in framehash().splitlines() if not line.startswith(b'1,'))
        with self.assertRaisesRegex(full.DecodeError, '^full_decode_empty_stream$'): full.parse_framehash(value)

    def test_rejects_duplicate_stream_header(self):
        with self.assertRaisesRegex(full.DecodeError, '^framehash_duplicate_stream$'):
            full.parse_framehash(framehash().replace(b'#media_type 0: video', b'#media_type 0: video\n#media_type 0: video'))

    def test_rejects_invalid_checksum(self):
        with self.assertRaisesRegex(full.DecodeError, '^framehash_row$'):
            full.parse_framehash(framehash().replace(b'a' * 64, b'g' * 64))

    def test_rejects_nonmonotonic_decoded_pts(self):
        with self.assertRaisesRegex(full.DecodeError, '^decoded_pts_not_monotonic$'):
            full.parse_framehash(framehash().replace(b'0, 1, 1, 1, 3456', b'0, -1, -1, 1, 3456'))

    def test_exact_final_pair(self):
        a, b = pair(); result = full.compare_final(a, b)
        self.assertTrue(result['exact_decoded_video_audio_equal'])
        self.assertEqual(result['encoded_file_hashes_equal'], 15)

    def test_changed_video_audio_and_clocks_each_refused(self):
        for field in ('decoded_video_sha256', 'decoded_audio_sha256', 'decoded_clock_sha256',
                      'packet_clocks_sha256', 'packet_payloads_sha256', 'media_structure_sha256'):
            with self.subTest(field=field):
                a, b = pair(); b['outputs'][8][field] = '0' * 64
                with self.assertRaisesRegex(full.DecodeError, '^paired_final_content_or_clock_changed$'):
                    full.compare_final(a, b)

    def test_container_only_file_hash_difference_is_explicit(self):
        a, b = pair(); b['outputs'][2]['encoded_file_sha256'] = '3' * 64
        result = full.compare_final(a, b)
        self.assertEqual(result['encoded_file_hashes_equal'], 14)
        self.assertTrue(result['exact_packet_clocks_payloads_equal'])

    def test_different_tool_family_refused(self):
        a, b = pair(); b['tool_binding']['ffmpeg']['executable_sha256'] = '4' * 64
        with self.assertRaisesRegex(full.DecodeError, '^paired_tool_family_differs$'): full.compare_final(a, b)

    def test_incomplete_final_set_refused(self):
        a, b = pair(); b['outputs'].pop()
        with self.assertRaisesRegex(full.DecodeError, '^paired_full_fifteen_final_required$'): full.compare_final(a, b)


if __name__ == '__main__': unittest.main()
