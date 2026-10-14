#!/usr/bin/env python3
"""Exercise a real collection workflow and retain bounded, reproducible evidence.

Generated media and raw command results stay in an external evidence directory.
Optional legacy measurements use equivalent final joined plans, identical media,
tools, quality and threads. They do not establish an internet or service SLA.
"""
import argparse
import hashlib
import json
import platform
from pathlib import Path
import statistics
import subprocess
import time


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def run(command, report=None, expected=0):
    started = time.monotonic()
    result = subprocess.run(command, capture_output=True, text=True, timeout=600)
    elapsed = time.monotonic() - started
    if report is not None:
        report.write_text(json.dumps({"command": list(map(str, command)),
                                     "elapsed_s": elapsed, "returncode": result.returncode,
                                     "stdout": result.stdout, "stderr": result.stderr}, indent=2))
    require(result.returncode == expected, f"Command failed ({result.returncode}): {command[0]} {command[1:3]}: {result.stderr[-1200:]}")
    return result, elapsed


def digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def probe(path):
    result, _ = run(["ffprobe", "-v", "error", "-show_streams", "-show_format", "-of", "json", str(path)])
    return json.loads(result.stdout)


def collection_command(binary, action, plan, output, *extra):
    return [str(binary), "--json-errors", "collection", action, "--plan", str(plan),
            "--output", str(output), "--threads", "2", *extra]


def inspect_outputs(outputs, clips):
    require([value["id"] for value in outputs] == [value["id"] for value in clips], "Output clip order/IDs changed")
    records = []
    for value, clip in zip(outputs, clips):
        path = Path(value["path"])
        information = probe(path)
        video = next(stream for stream in information["streams"] if stream["codec_type"] == "video")
        require(any(stream["codec_type"] == "audio" for stream in information["streams"]), "Audio missing")
        requested_ms = sum(interval["end_ms"] - interval["start_ms"] for interval in clip["ranges"])
        duration_ms = float(information["format"]["duration"]) * 1000
        require(abs(duration_ms - requested_ms) <= 350, "Decoded container duration disagrees with edit")
        require(len(value["segments"]) == len(clip["ranges"]), "Segment mapping count disagrees")
        position = 0
        for segment, interval in zip(value["segments"], clip["ranges"]):
            require(segment["source_start_ms"] == interval["start_ms"] and
                    segment["source_end_ms"] == interval["end_ms"], "Accurate source map disagrees")
            require(segment["output_start_ms"] == position, "Output timeline map is discontinuous")
            position = segment["output_end_ms"]
        run(["ffmpeg", "-v", "error", "-i", str(path), "-f", "null", "-"])
        require(path.stat().st_size == value["size_bytes"], "Reported publication size disagrees")
        records.append({"id": value["id"], "sha256": digest(path), "bytes": path.stat().st_size,
                        "duration_ms": duration_ms, "width": video["width"], "height": video["height"]})
    return records


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--baseline-binary", type=Path)
    parser.add_argument("--output", required=True, type=Path)
    parser.add_argument("--samples", type=int, default=3)
    parser.add_argument("--clips", type=int, default=15)
    args = parser.parse_args()
    require(args.binary.is_file(), "Collection binary is missing")
    require(1 <= args.clips <= 32 and 1 <= args.samples <= 10, "Bound clips/samples to the demonstration budget")
    root = args.output.resolve()
    require(not root.exists(), "Evidence directory must be new")
    root.mkdir(parents=True)
    raw = root / "raw"
    raw.mkdir()
    source = root / "source.mp4"
    run(["ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=24:duration=12",
         "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000:duration=12", "-c:v", "libx264",
         "-preset", "ultrafast", "-crf", "18", "-threads", "2", "-c:a", "aac", "-b:a", "128k",
         "-movflags", "+faststart", str(source)], raw / "fixture.json")
    clips = []
    for index in range(args.clips):
        offset = index % 5 * 1000
        second = 6000 + index % 5 * 500
        clip = {"id": f"clip-{index + 1:02d}", "label": f"Edit {index + 1}",
                "ranges": [{"start_ms": offset, "end_ms": offset + 500, "label": "Opening"},
                           {"start_ms": second, "end_ms": second + 1000, "label": "Answer"}]}
        if index % 2:
            clip["options"] = {"crop": {"x": 0.25, "y": 0, "width": 0.5, "height": 1}}
        clips.append(clip)
    plan = {"version": 1, "input": "source.mp4", "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"}, "clips": clips}
    plan_path = root / "collection.json"
    plan_path.write_text(json.dumps(plan, indent=2))
    first = root / "revision-1"
    first.mkdir()
    checked, validation_seconds = run(collection_command(args.binary, "validate", plan_path, first), raw / "validate.json")
    require(json.loads(checked.stdout)["valid"] and not list(first.iterdir()), "Validation wrote files or failed")
    draft, draft_seconds = run(collection_command(args.binary, "export", plan_path, first, "--rendition", "draft"), raw / "draft.json")
    draft_records = inspect_outputs(json.loads(draft.stdout)["outputs"], clips)
    final, final_seconds = run(collection_command(args.binary, "export", plan_path, first), raw / "final.json")
    final_records = inspect_outputs(json.loads(final.stdout)["outputs"], clips)
    preserved = {path.name: digest(path) for path in first.iterdir()}
    edited = json.loads(json.dumps(plan))
    edited["clips"][0]["ranges"][0]["end_ms"] += 250
    edited_path = root / "edited.json"
    edited_path.write_text(json.dumps(edited, indent=2))
    second = root / "revision-2"
    second.mkdir()
    selected, selected_seconds = run(collection_command(args.binary, "export", edited_path, second, "--clips", clips[0]["id"]), raw / "selected.json")
    selected_records = inspect_outputs(json.loads(selected.stdout)["outputs"], [edited["clips"][0]])
    require(len(list(second.iterdir())) == 1, "Unselected clips were rendered")
    require(preserved == {path.name: digest(path) for path in first.iterdir()}, "Previous outputs changed")
    collision, _ = run(collection_command(args.binary, "export", plan_path, first), raw / "collision.json", expected=1)
    require(json.loads(collision.stderr)["error"]["code"] == "output_exists", "Collision did not preserve the public error")
    require(preserved == {path.name: digest(path) for path in first.iterdir()}, "Collision changed existing outputs")
    measurements = []
    if args.baseline_binary:
        require(args.baseline_binary.is_file(), "Legacy binary is missing")
        for sample in range(args.samples):
            for flavor in (["legacy", "collection"] if sample % 2 == 0 else ["collection", "legacy"]):
                directory = root / f"sample-{sample + 1}-{flavor}"
                directory.mkdir()
                started = time.monotonic()
                if flavor == "collection":
                    result, _ = run(collection_command(args.binary, "export", plan_path, directory), raw / f"sample-{sample + 1}-{flavor}.json")
                    files = [Path(value["path"]) for value in json.loads(result.stdout)["outputs"]]
                else:
                    files = []
                    for clip in clips:
                        options = dict(plan["options"], **clip.get("options", {}))
                        legacy_plan = {"version": 1, "input": str(source), "options": options,
                                       "ranges": clip["ranges"], "output_mode": "joined"}
                        legacy_path = raw / f"legacy-{sample + 1}-{clip['id']}.json"
                        legacy_path.write_text(json.dumps(legacy_plan))
                        path = directory / f"{clip['id']}.final.mp4"
                        run([str(args.baseline_binary), "--json-errors", "batch", "--plan", str(legacy_path),
                             "--output", str(path), "--threads", "2"], raw / f"sample-{sample + 1}-legacy-{clip['id']}.json")
                        files.append(path)
                elapsed = time.monotonic() - started
                hashes = [digest(path) for path in files]
                require(hashes == [value["sha256"] for value in final_records], "Equivalent final exports differed in bytes")
                measurements.append({"sample": sample + 1, "flavor": flavor, "elapsed_s": elapsed,
                                     "bytes": sum(path.stat().st_size for path in files), "sha256": hashes})
    binary_info, _ = run([str(args.binary), "version"])
    ffmpeg_info, _ = run(["ffmpeg", "-version"])
    summary = {"schema_version": 1, "binary": json.loads(binary_info.stdout), "binary_sha256": digest(args.binary),
               "platform": platform.platform(), "ffmpeg": ffmpeg_info.stdout.splitlines()[0],
               "source_sha256": digest(source), "clip_count": args.clips, "threads": 2,
               "validation_s": validation_seconds, "draft_s": draft_seconds, "final_s": final_seconds,
               "selected_revision_s": selected_seconds, "draft": draft_records, "final": final_records,
               "selected": selected_records, "measurements": measurements}
    if args.baseline_binary:
        summary["baseline_binary_sha256"] = digest(args.baseline_binary)
        summary["medians_s"] = {flavor: statistics.median(row["elapsed_s"] for row in measurements if row["flavor"] == flavor)
                                for flavor in ("legacy", "collection")}
    (root / "summary.json").write_text(json.dumps(summary, indent=2))
    print(json.dumps({"passed": True, "clip_count": args.clips, "summary": str(root / "summary.json"),
                      "medians_s": summary.get("medians_s")}, indent=2))


if __name__ == "__main__":
    main()
