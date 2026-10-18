import copy
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

from cutmyvideo import APIError, CutmyClient, LocalCutmy, LocalError, export_portable_project, import_portable_project

HELPER = Path(__file__).with_name("media_cli_helper.py")
BASE = {"version": 1, "start_ms": 0, "end_ms": 1000, "analyzed_end_ms": 900, "audio_track": 1}
SILENCE = {**BASE, "silence": [], "keep": [{"start_ms": 0, "end_ms": 900, "label": ""}]}
VIDEO = {k: v for k, v in BASE.items() if k != "audio_track"} | {"video_track": 0, "scenes": [], "black": []}
LOUDNESS = {**BASE, "integrated_lufs": None, "true_peak_dbtp": None, "loudness_range_lu": None}
SOURCE = {"id": "own", "duration_ms": 1000}


class MediaSDKTests(unittest.TestCase):
    def local(self, **kwargs):
        return LocalCutmy([sys.executable, str(HELPER)], threads=3, **kwargs)

    def test_local_analysis_literal_paths_exact_ms_defaults_and_nullable_loudness(self):
        opts = {"start_ms": 1250, "end_ms": 0, "threshold_db": -40, "keep_before_ms": 0}
        result = self.local().analyze_silence("input ; $(unsafe) Привет.mp4", opts, timeout=2)
        args = result["args"]
        self.assertEqual(args[:3], ["--json-errors", "analyze", "silence"])
        self.assertEqual(args[args.index("--start") + 1], "1.250")
        self.assertNotIn("--end", args)
        self.assertEqual(args[args.index("--threads") + 1], "3")
        self.assertEqual(result["analyzed_end_ms"], 2500)
        self.assertEqual(opts["end_ms"], 0)
        self.assertEqual(self.local().analyze_video("input")["scenes"][0]["at_ms"], 2000)
        self.assertIsNone(self.local().analyze_loudness("input")["integrated_lufs"])
        plan = self.local().silence_plan("input", format="mp3", output_mode="separate")
        self.assertEqual(plan["options"]["format"], "mp3")
        self.assertEqual(plan["output_mode"], "separate")

    def test_local_cutlists_private_temporary_text_unicode_markers_and_three_export_modes(self):
        request = {"format": "csv", "text": "0,,Привет\n"}
        cut = self.local().import_cut_list(request)
        self.assertIsNone(cut["entries"][0]["end_ms"])
        self.assertEqual(cut["input_text"], request["text"])
        self.assertFalse(Path(cut["temporary_path"]).exists())
        plan = self.local().plan_cut_list(request, "video.mp4", duration_ms=1000)
        self.assertEqual(plan["ranges"][0]["end_ms"], 1000)
        for key, value in [("cut_list", {"version": 1, "entries": cut["entries"]}), ("plan", {k: plan[k] for k in ("version", "input", "ranges", "options")}), ("segments", [{"index": 0, "label": "Привет", "source_start_ms": 1, "source_end_ms": 2, "output_start_ms": 0, "output_end_ms": 1}])]:
            result = self.local().export_cut_list({"format": "csv", key: value})
            self.assertEqual(result["document"], value)
            self.assertFalse(Path(result["temporary_path"]).exists())

    def test_http_routes_options_copy_and_response_shape(self):
        with CutmyClient("http://127.0.0.1") as client:
            for method, kind, result in [(client.analyze_silence, "silence", SILENCE), (client.analyze_video, "video", VIDEO), (client.analyze_loudness, "loudness", LOUDNESS)]:
                with patch.object(client, "_json", return_value=result) as exchange:
                    opts = {"start_ms": 0, "end_ms": 1000}
                    self.assertEqual(method("src", opts), result)
                    self.assertEqual(exchange.call_args.args[:3], ("POST", "/api/v1/sources/src/analysis/" + kind, opts))
                    self.assertIsNot(exchange.call_args.args[2], opts)
            cut = {"version": 1, "entries": [{"start_ms": 0, "end_ms": None, "label": "Привет"}]}
            with patch.object(client, "_json", return_value=cut):
                self.assertEqual(client.import_cut_list({"format": "csv", "text": "0,,Привет\n"}), cut)
            with patch.object(client, "_json", return_value={"version": 1, "format": "csv", "text": "0,,Привет\n"}) as exchange:
                client.export_cut_list({"format": "csv", "cut_list": cut})
                self.assertEqual(exchange.call_args.args[1], "/api/v1/cut-lists/export")

    def test_malformed_nonfinite_and_uncovered_analysis_is_not_accepted(self):
        invalid = [{**SILENCE, "analyzed_end_ms": 1001}, {**SILENCE, "silence": None}, {**SILENCE, "keep": [{"start_ms": 0, "end_ms": 950, "label": ""}]}, {**SILENCE, "start_ms": True}]
        for result in invalid:
            with self.subTest(result=result), self.assertRaises(LocalError) as error:
                self.local(env={"SDK_BAD_MEDIA_RESPONSE": json.dumps(result)}).analyze_silence("input")
            self.assertEqual(error.exception.code, "invalid_response")
        with CutmyClient("http://127.0.0.1") as client:
            for opts in [{"threshold_db": float("nan")}, {"start_ms": True}, {"audio_track": None}, {"future": 1}]:
                with patch.object(client, "_json") as exchange, self.assertRaises((ValueError, TypeError)):
                    client.analyze_silence("src", opts)
                exchange.assert_not_called()
            with patch.object(client, "_json", return_value={**LOUDNESS, "true_peak_dbtp": float("inf")}), self.assertRaises(APIError):
                client.analyze_loudness("src")
            with self.assertRaises(ValueError):
                client.export_cut_list({"format": "csv", "cut_list": {}, "segments": []})

    def test_local_replacement_paths_resolve_before_private_plan_without_mutation(self):
        helper = Path(__file__).with_name("cli_helper.py")
        client = LocalCutmy([sys.executable, str(helper), "success"])
        plan = {"version": 1, "input": "primary.mp4", "options": {"audio_replace": {"input": "audio Привет.wav", "offset_ms": 250}}, "clips": [{"id": "answer", "ranges": [{"start_ms": 0, "end_ms": 500}], "options": {"audio_replace": {"input": "override.wav"}}}]}
        original = copy.deepcopy(plan)
        with tempfile.TemporaryDirectory() as directory:
            result = client.validate_collection(plan, directory)
        self.assertEqual(result["plan"]["options"]["audio_replace"]["input"], str(Path("audio Привет.wav").absolute()))
        self.assertEqual(result["plan"]["clips"][0]["options"]["audio_replace"]["input"], str(Path("override.wav").absolute()))
        self.assertEqual(plan, original)
        bad = copy.deepcopy(plan); bad["options"]["audio_replace"] = {"source_id": "foreign"}
        with self.assertRaises(ValueError):
            client.validate_collection(bad, ".")

    def test_portable_render_edits_and_clear_flags_keep_no_authority_contract(self):
        edits = {"audio_normalize": {"integrated_lufs": -16, "true_peak_dbtp": -1, "loudness_range_lu": 11}, "canvas": {"width": 1080, "height": 1920, "fit": "contain", "background": "#000000"}}
        request = {"source_id": "own", "plan": {"version": 1, "options": edits, "clips": [{"id": "answer", "ranges": [{"start_ms": 0, "end_ms": 500}], "options": {"clear_audio_normalize": True, "clear_canvas": True, "clear_audio_replace": True}}]}}
        portable = export_portable_project(request, SOURCE)
        self.assertEqual(import_portable_project(portable, SOURCE), request)
        for replacement in [{"source_id": "private-audio"}, {"input": "/private/audio.wav"}]:
            bad = copy.deepcopy(request); bad["plan"]["clips"][0]["options"]["audio_replace"] = replacement
            with self.assertRaises(ValueError):
                export_portable_project(bad, SOURCE)
            bad = copy.deepcopy(portable); bad["plan"]["options"]["audio_replace"] = replacement
            with self.assertRaises(ValueError):
                import_portable_project(bad, SOURCE)


if __name__ == "__main__":
    unittest.main()
