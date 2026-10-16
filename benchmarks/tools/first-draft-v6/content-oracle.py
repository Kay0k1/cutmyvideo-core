#!/usr/bin/env python3
"""Independently compare collection media with caller-requested source content.

The SDK's hashes and maps alone are not a content oracle. Reference times and
crop come from the harness's original request documents; real FFmpeg decodes
both the original source and acknowledged files. Reports contain aggregates.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import re
import stat
import struct
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
HASH = re.compile(r"[0-9a-f]{64}\Z")
MAX_JSON = 4 << 20
MAX_FILE = 128 << 20
STAGES = ("initial", "revised", "draft")
FRAME_WIDTH, FRAME_HEIGHT = 64, 36
FRAME_MAE_LIMIT = 12.0
AUDIO_CORRELATION_MIN = .75
AUDIO_RATE, AUDIO_STRIDE, AUDIO_PAD = 8000, 5, .06


class VerificationError(Exception):
    pass


def require(value, code):
    if not value:
        raise VerificationError(code)


def regular(path, limit=MAX_FILE):
    require(path.is_absolute(), "absolute_file_required")
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= limit, "bounded_regular_file_required")
    return info


def file_hash(path):
    regular(path)
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(64 << 10), b""):
            value.update(chunk)
    return value.hexdigest()


def document(path):
    regular(path, MAX_JSON)
    data = json.loads(path.read_bytes())
    require(isinstance(data, dict), "json_object_required")
    return data


def run(argv, limit=MAX_JSON):
    process = subprocess.run(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30, check=False)
    require(process.returncode == 0 and len(process.stdout) <= limit, "independent_decoder_failed")
    return process.stdout


def probe(path):
    return json.loads(run(["ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", str(path)]))


def frame(path, seconds, crop=None):
    filters = []
    if crop:
        filters.append("crop=iw*{width}:ih*{height}:iw*{x}:ih*{y}".format(**crop))
    filters.append(f"scale={FRAME_WIDTH}:{FRAME_HEIGHT}:flags=area")
    raw = run(["ffmpeg", "-v", "error", "-nostdin", "-filter_threads", "1", "-ss", f"{seconds:.6f}",
               "-i", str(path), "-map", "0:v:0", "-vf", ",".join(filters), "-frames:v", "1",
               "-pix_fmt", "rgb24", "-f", "rawvideo", "-"], FRAME_WIDTH * FRAME_HEIGHT * 3)
    require(len(raw) == FRAME_WIDTH * FRAME_HEIGHT * 3, "source_frame_unavailable")
    return raw


def frame_mae(expected, actual):
    require(len(expected) == len(actual) > 0, "frame_size")
    return sum(abs(a-b) for a, b in zip(expected, actual)) / len(expected)


def pcm(path, seconds, duration, stream_index):
    raw = run(["ffmpeg", "-v", "error", "-nostdin", "-ss", f"{seconds:.6f}", "-i", str(path),
               "-map", f"0:{stream_index}", "-t", f"{duration:.6f}", "-ar", str(AUDIO_RATE),
               "-ac", "1", "-f", "s16le", "-"], 64 << 10)
    require(len(raw) % 2 == 0 and len(raw) >= 64, "source_audio_unavailable")
    return list(struct.unpack("<" + str(len(raw)//2) + "h", raw))[::AUDIO_STRIDE]


def rms(values):
    return math.sqrt(sum(value*value for value in values) / len(values)) if values else 0


def best_correlation(reference, actual):
    # The reference includes +/-60ms for AAC priming and seek rounding. Center
    # and normalize each actual-sized window; a different spoken passage fails.
    require(len(reference) >= len(actual) >= 32, "audio_sample_size")
    mean = sum(actual) / len(actual)
    centered = [value-mean for value in actual]
    actual_energy = sum(value*value for value in centered)
    if actual_energy <= 1:
        return 0.0
    best = -1.0
    for offset in range(len(reference)-len(actual)+1):
        window = reference[offset:offset+len(actual)]
        average = sum(window)/len(window)
        energy = sum((value-average)**2 for value in window)
        if energy > 1:
            dot = sum((value-average)*other for value, other in zip(window, centered))
            best = max(best, dot/math.sqrt(energy*actual_energy))
    return best


def source_streams(profile):
    videos = [s for s in profile["streams"] if s["codec_type"] == "video" and not s.get("disposition", {}).get("attached_pic")]
    audios = [s for s in profile["streams"] if s["codec_type"] == "audio"]
    require(len(videos) == 1 and audios, "fixture_streams")
    video = videos[0]
    require(video.get("sample_aspect_ratio", "1:1") == "1:1"
            and not any(item.get("rotation", 0) for item in video.get("side_data_list", [])),
            "fixture_display_geometry")
    default = next((s for s in audios if s.get("disposition", {}).get("default")), audios[0])
    return video, default, audios


def selected(request):
    plan = request["plan"]
    require(plan.get("version") == 1 and isinstance(plan.get("clips"), list) and 0 < len(plan["clips"]) <= 128, "caller_plan")
    ids = [clip["id"] for clip in plan["clips"]]
    require(len(set(ids)) == len(ids), "caller_duplicate_clip")
    selection = request.get("clip_ids") or ids
    require(len(set(selection)) == len(selection) and set(selection) <= set(ids), "caller_selection")
    return [clip for clip in plan["clips"] if clip["id"] in selection]


def check_output(source, profile, output, clip, request):
    path = Path(output["path"])
    info = regular(path)
    require(info.st_size == output["size_bytes"] and file_hash(path) == output["sha256"], "output_receipt")
    options = {**request["plan"].get("options", {}), **clip.get("options", {})}
    require(options.get("format", "mp4") == "mp4" and options.get("cut_mode", "accurate") == "accurate"
            and not options.get("no_audio"), "fixture_options")
    source_video, default_audio, audios = source_streams(profile)
    audio_index = options.get("audio_track", default_audio["index"])
    require(any(a["index"] == audio_index for a in audios), "caller_audio_track")
    tracks = output["track_manifest"]
    included_video = [t["index"] for t in tracks if t["included"] and t["type"] == "video"]
    included_audio = [t["index"] for t in tracks if t["included"] and t["type"] == "audio"]
    require(included_video == [source_video["index"]] and included_audio == [audio_index], "track_manifest")
    observed = probe(path)
    videos = [s for s in observed["streams"] if s["codec_type"] == "video"]
    audio = [s for s in observed["streams"] if s["codec_type"] == "audio"]
    require(len(observed["streams"]) == 2 and len(videos) == len(audio) == 1, "output_streams")
    crop = options.get("crop")
    expected_width = int(source_video["width"] * (crop["width"] if crop else 1))
    expected_height = int(source_video["height"] * (crop["height"] if crop else 1))
    rendition = request.get("rendition", "final")
    cap = 720 if rendition == "draft" else {"720p": 720, "1080p": 1080}.get(options.get("quality", "best"))
    if cap and expected_height > cap:
        expected_width = int(expected_width * cap / expected_height)//2*2
        expected_height = cap
    require((videos[0]["width"], videos[0]["height"]) == (expected_width, expected_height)
            and videos[0].get("sample_aspect_ratio", "1:1") == "1:1", "output_geometry")
    ranges, segments = clip["ranges"], output["segments"]
    require(len(ranges) == len(segments) and ranges, "segment_count")
    cursor, frame_errors, audio_scores, silent_segments = 0, [], [], 0
    for interval, segment in zip(ranges, segments):
        start, end = interval["start_ms"], interval["end_ms"]
        require(0 <= start < end, "caller_interval")
        require(segment["source_start_ms"] == start and abs(segment["source_end_ms"]-end) <= 100
                and segment["output_start_ms"] == cursor and abs(segment["output_end_ms"]-(cursor+end-start)) <= 100,
                "segment_map")
        duration = (end-start)/1000
        for fraction in (.3, .7):
            offset = duration*fraction
            error = frame_mae(frame(source, start/1000+offset, crop), frame(path, cursor/1000+offset))
            require(error <= FRAME_MAE_LIMIT, "source_video_content")
            frame_errors.append(error)
        length = min(.32, duration*.4)
        candidates = [(duration-length)*fraction for fraction in (.15, .5, .85)]
        references = [(offset, pcm(source, start/1000+offset, length, audio_index)) for offset in candidates]
        offset, strongest = max(references, key=lambda item: rms(item[1]))
        if rms(strongest) >= 20:
            reference = pcm(source, start/1000+offset-AUDIO_PAD, length+2*AUDIO_PAD, audio_index)
            actual = pcm(path, cursor/1000+offset, length, audio[0]["index"])
            score = best_correlation(reference, actual)
            require(score >= AUDIO_CORRELATION_MIN, "source_audio_content")
            audio_scores.append(score)
        else:
            require(rms(pcm(path, cursor/1000+offset, length, audio[0]["index"])) < 60, "source_silence_content")
            silent_segments += 1
        cursor = segment["output_end_ms"]
    require(abs(float(observed["format"]["duration"])*1000-sum(r["end_ms"]-r["start_ms"] for r in ranges)) <= 120, "output_duration")
    return {"segments": len(segments), "frame_samples": len(frame_errors), "max_frame_mae": max(frame_errors),
            "audio_samples": len(audio_scores), "min_audio_correlation": min(audio_scores) if audio_scores else None,
            "silent_segments": silent_segments}


def verify(source, expected_sha, requests, receipts):
    require(HASH.fullmatch(expected_sha), "expected_source_sha256")
    before = file_hash(source)
    require(before == expected_sha, "source_identity")
    profile = probe(source)
    video, _, _ = source_streams(profile)
    require(0 < len(receipts) <= 2, "bounded_harness_receipts_required")
    summaries = []
    for receipt in receipts:
        require(receipt.get("passed") is True, "successful_harness_receipt_required")
        require(receipt.get("source_sha256") == before and receipt.get("source_size_bytes") == source.stat().st_size,
                "harness_source_binding")
        require((receipt.get("source_width"), receipt.get("source_height")) == (video["width"], video["height"]),
                "harness_source_profile")
        require(receipt.get("clips") == 15 and receipt.get("completed_before_hard_kill") == 8
                and receipt.get("lost_submit_reply_recovered") is True
                and receipt.get("interrupted_download_ranged") is True
                and receipt.get("partial_admission") is True
                and receipt.get("admission_retry_after_respected") is True
                and receipt.get("interrupted_artifact_full_gets") == 1
                and receipt.get("resumed_offset", 0) > 0, "required_recovery_observations")
        if receipt.get("real_interview"):
            require(receipt.get("final_restored_without_new_work") is True, "final_restoration_observation")
        observations, seen, job_ids, selected_outputs = [], {}, set(), 0
        source_id = receipt["initial"].get("source_id")
        require(isinstance(source_id, str) and source_id, "stage_scope")
        for stage in STAGES:
            request = requests[stage]
            report = receipt[stage]
            require(report.get("complete") is True and report.get("source_id") == source_id
                    and request.get("source_id", source_id) == source_id, "stage_scope")
            clips = selected(request)
            require(len(clips) == (2 if stage == "draft" else 15), "required_stage_selection")
            require([output["clip_id"] for output in report["outputs"]] == [clip["id"] for clip in clips], "stage_order")
            for output, clip in zip(report["outputs"], clips):
                selected_outputs += 1
                identity = (output["artifact_id"], output["job_id"], output["sha256"], output["size_bytes"])
                require(isinstance(output["job_id"], str) and output["job_id"]
                        and isinstance(output["artifact_id"], str) and output["artifact_id"], "output_identity")
                job_ids.add(output["job_id"])
                key = json.dumps({"clip": clip, "options": request["plan"].get("options", {}), "rendition": request.get("rendition", "final")}, sort_keys=True)
                if output["artifact_id"] in seen:
                    require(seen[output["artifact_id"]] == (identity, key), "immutable_artifact_reused_for_changed_edit")
                    require(file_hash(Path(output["path"])) == output["sha256"], "retained_output_changed")
                    continue
                seen[output["artifact_id"]] = (identity, key)
                observations.append(check_output(source, profile, output, clip, request))
        require(len(seen) == len(job_ids) == receipt["artifacts"] == 19
                and receipt["jobs"] == receipt["render_attempts"] == 19, "nineteen_unique_artifacts")
        correlations = [row["min_audio_correlation"] for row in observations if row["min_audio_correlation"] is not None]
        summaries.append({"unique_artifacts": len(seen), "stage_output_receipts": selected_outputs,
                          "segments": sum(row["segments"] for row in observations),
                          "frame_samples": sum(row["frame_samples"] for row in observations),
                          "max_frame_mae": round(max(row["max_frame_mae"] for row in observations), 4),
                          "audio_samples": sum(row["audio_samples"] for row in observations),
                          "min_audio_correlation": round(min(correlations), 4) if correlations else None,
                          "silent_segments": sum(row["silent_segments"] for row in observations)})
    require(file_hash(source) == before, "source_changed_during_verification")
    return {"schema_version": 1, "passed": True, "source_sha256": before, "source_size_bytes": source.stat().st_size,
            "independent_source_content": True, "frame_mae_limit": FRAME_MAE_LIMIT,
            "audio_correlation_minimum": AUDIO_CORRELATION_MIN, "consumers": summaries}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", required=True, type=Path)
    parser.add_argument("--expected-source-sha256", required=True)
    parser.add_argument("--requests", required=True, type=Path)
    parser.add_argument("--receipt", required=True, type=Path, action="append")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    output = args.output.absolute()
    require(not output.resolve().is_relative_to(ROOT) and not output.exists(), "new_external_report_required")
    result = verify(args.source.absolute(), args.expected_source_sha256, document(args.requests.absolute()),
                    [document(path.absolute()) for path in args.receipt])
    output.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    with output.open("x", encoding="utf-8") as stream:
        os.chmod(output, 0o600)
        json.dump(result, stream, indent=2)
        stream.write("\n")
    print(json.dumps(result))


if __name__ == "__main__":
    os.umask(0o077)
    try:
        main()
    except (VerificationError, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        print(json.dumps({"passed": False, "code": str(error) if isinstance(error, VerificationError) else type(error).__name__}), file=sys.stderr)
        raise SystemExit(1)
