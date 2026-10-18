#!/usr/bin/env python3
"""Installed-package 1.6 operations; the Go producer independently decodes files."""
import copy
import json
import os
from pathlib import Path
import sys
import cutmyvideo
from cutmyvideo import CutmyClient, LocalCutmy, ready_artifacts, export_portable_project, import_portable_project

assert Path(cutmyvideo.__file__).resolve().is_relative_to(Path(sys.prefix).resolve())
mode, source, _replacement, destination, base_url = sys.argv[1:]
output = Path(destination)
local = mode == "local"
client = LocalCutmy(os.environ["CUTMY_SDK_BINARY"], threads=1) if local else CutmyClient(base_url, request_timeout=30)
source_id = source if local else client.upload_source(source)["id"]
speed = {"numerator": 5, "denominator": 4}
target = 262144
output_ranges = [{"start_ms": 2000, "end_ms": 4000, "label": "Blue"}, {"start_ms": 0, "end_ms": 1000, "label": "Red"}, {"start_ms": 2000, "end_ms": 2500, "label": "Blue again"}]
outputs = []
canonical = []
for name in ("wav", "flac", "m4a", "speed-target"):
    fmt = "mp4" if name == "speed-target" else name
    settings = {"format": fmt, "quality": "best", "cut_mode": "copy" if name == "m4a" else "accurate"}
    ranges = output_ranges if name == "speed-target" else [{"start_ms": 0, "end_ms": 4000, "label": "Audio"}]
    if name == "speed-target":
        settings.update(speed=speed, target_size_bytes=target)
    path = output / (name + "." + fmt)
    original = copy.deepcopy(settings)
    if local:
        plan = {"version": 1, "input": source, "ranges": ranges, "output_mode": "joined", "options": settings}
        validation = client.preflight(plan, path)
        artifact = client.execute_plan(plan, path, timeout=90)["outputs"][0]
    else:
        request = {"source_id": source_id, "ranges": ranges, "output_mode": "joined", **settings}
        validation = client.preflight(request)
        job = client.create_job(request, idempotency_key="delivery-python-" + name)
        replay = client.create_job(request, idempotency_key="delivery-python-" + name)
        assert replay["id"] == job["id"]
        artifact = ready_artifacts(client.wait_for_job(job["id"], timeout=90))[0]
        client.download(artifact, path, max_bytes=4 << 20)
    assert settings == original
    if name == "speed-target":
        assert validation["request"]["speed"] == speed and validation["request"]["target_size_bytes"] == target
    canonical.append(validation["request"])
    outputs.append({"name": name, "path": str(path), "artifact": artifact})

finished = outputs[-1]
sidecar = client.retime_subtitles({"version": 2, "format": "srt", "text": "1\n00:00:00,250 --> 00:00:00,750\nRed cue\n\n", "outputs": [{"clip_id": "speed", "segments": finished["artifact"]["segments"]}]})
assert sidecar["version"] == 1 and sidecar["outputs"][0]["text"] == "1\n00:00:01,800 --> 00:00:02,200\nRed cue\n\n"
poster_options = {"timestamp_ms": 2000, "format": "png"}
poster_path = output / "poster.png"
if local:
    poster = client.generate_poster(finished["path"], poster_path, poster_options)
else:
    parent = finished["artifact"]["id"]
    poster = client.create_poster(parent, poster_options)
    assert client.create_poster(parent, poster_options)["id"] == poster["id"]
    assert client.get_image_artifact(poster["id"])["parent_artifact_id"] == parent
    client.download_image(poster, poster_path, max_bytes=16 << 20)
    client.delete_image_artifact(poster["id"])
    try:
        client.get_image_artifact(poster["id"])
    except cutmyvideo.APIError as error:
        assert error.status == 404
    else:
        raise AssertionError("Deleted own image artifact remained available")

chapters = client.export_cut_list({"format": "ffmetadata", "segments": finished["artifact"]["segments"]})
assert "START=1600" in chapters["text"] and "START=2400" in chapters["text"] and "END=2800" in chapters["text"]

plan = {"version": 1, "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate", "speed": speed, "target_size_bytes": target}, "clips": [{"id": "inherit", "ranges": [{"start_ms": 2000, "end_ms": 4000}]}, {"id": "reset", "ranges": [{"start_ms": 0, "end_ms": 1000}], "options": {"clear_speed": True, "clear_target_size": True}}]}
portable_request = {"source_id": "owned" if local else source_id, "plan": plan}
portable_source = {"id": portable_request["source_id"], "duration_ms": 4000}
portable = export_portable_project(portable_request, portable_source)
assert import_portable_project(portable, portable_source) == portable_request
collection_outputs = []
if local:
    (output / "collection").mkdir(mode=0o700)
    result = client.execute_collection({**plan, "input": source}, output / "collection", timeout=90)
    collection_outputs = [{"name": row["id"], "path": row["path"], "artifact": row} for row in result["outputs"]]
else:
    validation = client.validate_collection(portable_request)
    inherited, reset = [row["preflight"]["request"] for row in validation["clips"]]
    assert inherited["speed"] == speed and inherited["target_size_bytes"] == target
    assert not reset.get("speed") and not reset.get("target_size_bytes")
    report = client.run_collection(portable_request, output / "collection", idempotency_key="delivery-python-collection", timeout=120)
    assert report["complete"]
    collection_outputs = [{"name": row["clip_id"], "path": row["path"], "artifact": {"segments": row["segments"]}} for row in report["outputs"]]
    audio_plan = {"source_id": source_id, "plan": {"version": 1, "options": {"format": "wav", "quality": "best", "cut_mode": "accurate"}, "clips": [{"id": "audio", "ranges": [{"start_ms": 0, "end_ms": 1000}]}]}}
    audio_report = client.run_collection(audio_plan, output / "audio-collection", idempotency_key="delivery-python-wav", timeout=90)
    assert audio_report["complete"] and audio_report["outputs"][0]["path"].endswith(".wav")
    assert client.resume_collection(output / "audio-collection", timeout=30)["complete"]
print(json.dumps({"mode": mode, "outputs": outputs, "poster": {"path": str(poster_path), "artifact": poster}, "chapters": chapters["text"], "canonical": canonical, "collection": collection_outputs, "portable_roundtrip": True, "affine_sidecar": True}))
