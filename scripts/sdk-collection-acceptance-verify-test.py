#!/usr/bin/env python3
"""Required physical-media controls for the independent SDK content oracle."""
from __future__ import annotations

import copy
import importlib.util
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

sys.dont_write_bytecode = True
SPEC = importlib.util.spec_from_file_location(
    "sdk_collection_content_oracle", Path(__file__).with_name("sdk-collection-acceptance-verify.py"))
oracle = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(oracle)


class ContentOracleControls(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        # These controls must fail when decoders are unavailable, never skip.
        if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
            raise RuntimeError("required physical-media decoders are unavailable")
        cls.temporary = tempfile.TemporaryDirectory(prefix="cutmy-sdk-content-controls-")
        cls.directory = Path(cls.temporary.name)
        cls.source = cls.directory / "source.mp4"
        cls.encode([
            "-f", "lavfi", "-i", "color=red:size=160x96:rate=25:duration=2",
            "-f", "lavfi", "-i", "color=blue:size=160x96:rate=25:duration=2",
            "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
            "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000:duration=4",
            "-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]",
            "-map", "[v]", "-map", "2:a", "-map", "3:a",
            "-disposition:a:0", "0", "-disposition:a:1", "default",
        ], cls.source)
        cls.correct = cls.directory / "correct.mp4"
        cls.wrong_video = cls.directory / "wrong-video.mp4"
        cls.wrong_audio = cls.directory / "wrong-audio.mp4"
        cls.correct_crop = cls.directory / "correct-crop.mp4"
        for path, start, audio, crop in (
                (cls.correct, "0.2", "0:2", None),
                (cls.wrong_video, "2.2", "0:2", None),
                (cls.wrong_audio, "0.2", "0:1", None),
                (cls.correct_crop, "0.2", "0:2", "crop=iw*.5:ih:iw*.25:0")):
            options = ["-ss", start, "-i", str(cls.source), "-t", "1", "-map", "0:v:0", "-map", audio]
            if crop:
                options += ["-vf", crop]
            cls.encode(options, path)
        cls.profile = oracle.probe(cls.source)
        cls.clip = {"id": "opening", "ranges": [{"start_ms": 200, "end_ms": 1200}]}
        cls.request = {"rendition": "final", "plan": {"version": 1,
            "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate", "audio_track": 2},
            "clips": [cls.clip]}}

    @classmethod
    def encode(cls, arguments, destination):
        result = subprocess.run(["ffmpeg", "-v", "error", "-nostdin", "-filter_threads", "1",
                                 "-filter_complex_threads", "1", *arguments,
                                 "-c:v", "libx264", "-preset", "ultrafast", "-crf", "20",
                                 "-pix_fmt", "yuv420p", "-c:a", "aac", "-threads", "1",
                                 "-y", str(destination)], capture_output=True, timeout=30, check=False)
        if result.returncode:
            raise RuntimeError("required physical-media fixture encoding failed")

    @classmethod
    def tearDownClass(cls):
        cls.temporary.cleanup()

    def receipt(self, path):
        return {"clip_id": "opening", "job_id": "job_control", "artifact_id": "art_control",
                "path": str(path), "size_bytes": path.stat().st_size, "sha256": oracle.file_hash(path),
                "segments": [{"source_start_ms": 200, "source_end_ms": 1200,
                              "output_start_ms": 0, "output_end_ms": 1000}],
                "track_manifest": [{"index": 0, "type": "video", "included": True},
                                   {"index": 1, "type": "audio", "included": False},
                                   {"index": 2, "type": "audio", "included": True}]}

    def check(self, path, clip=None, request=None):
        return oracle.check_output(self.source, self.profile, self.receipt(path),
                                   clip or self.clip, request or self.request)

    def test_physical_source_video_and_audio_match(self):
        result = self.check(self.correct)
        self.assertEqual(result["frame_samples"], 2)
        self.assertEqual(result["audio_samples"], 1)
        self.assertGreater(result["min_audio_correlation"], .9)

    def test_physical_crop_matches(self):
        clip = copy.deepcopy(self.clip)
        clip["options"] = {"crop": {"x": .25, "y": 0, "width": .5, "height": 1}}
        result = self.check(self.correct_crop, clip)
        self.assertLess(result["max_frame_mae"], 1)

    def test_wrong_passage_with_consistent_forged_maps_and_hash_is_rejected(self):
        # The file's hash, dimensions, duration, decodability and claimed maps
        # all agree. Only source pixels expose the blue passage replacing red.
        with self.assertRaisesRegex(oracle.VerificationError, "^source_video_content$"):
            self.check(self.wrong_video)

    def test_wrong_audio_with_consistent_forged_manifest_and_hash_is_rejected(self):
        # A real 440Hz source track is supplied while claiming selected 880Hz.
        with self.assertRaisesRegex(oracle.VerificationError, "^source_audio_content$"):
            self.check(self.wrong_audio)

    def test_wrong_map_is_rejected(self):
        output = self.receipt(self.correct)
        output["segments"][0]["source_start_ms"] += 40
        with self.assertRaisesRegex(oracle.VerificationError, "^segment_map$"):
            oracle.check_output(self.source, self.profile, output, self.clip, self.request)

    def test_changed_receipt_hash_is_rejected(self):
        output = self.receipt(self.correct)
        output["sha256"] = "0" * 64
        with self.assertRaisesRegex(oracle.VerificationError, "^output_receipt$"):
            oracle.check_output(self.source, self.profile, output, self.clip, self.request)

    def test_wrong_source_fingerprint_is_rejected(self):
        with self.assertRaisesRegex(oracle.VerificationError, "^source_identity$"):
            oracle.verify(self.source, "0" * 64, {}, [])

    def test_duplicate_or_missing_selection_is_rejected(self):
        for ids in (["opening", "opening"], ["missing"]):
            request = copy.deepcopy(self.request)
            request["clip_ids"] = ids
            with self.assertRaisesRegex(oracle.VerificationError, "^caller_selection$"):
                oracle.selected(request)


if __name__ == "__main__":
    unittest.main()
