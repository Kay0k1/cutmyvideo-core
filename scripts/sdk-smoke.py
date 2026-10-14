#!/usr/bin/env python3
"""Installed-package consumer; the Go harness independently decodes its media."""

import json
import os
from pathlib import Path
import sys

import cutmyvideo
from cutmyvideo import CutmyClient, LocalCutmy, LocalError, ready_artifacts


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def main():
    require(Path(cutmyvideo.__file__).resolve().is_relative_to(Path(sys.prefix).resolve()),
            "The smoke consumer must import the installed wheel from its virtual environment")
    mode, source, destination, base_url = sys.argv[1:]
    output = Path(destination)
    ranges = [{"start_ms": 500, "end_ms": 1500, "label": "First scene"},
              {"start_ms": 2000, "end_ms": 3000, "label": "Second scene"}]
    options = {"format": "mp4", "quality": "best", "cut_mode": "accurate"}
    if mode == "local":
        client = LocalCutmy(os.environ["CUTMY_SDK_BINARY"], threads=1)
        inspected = client.inspect(source, timeout=20)
        require(inspected["width"] == 160 and inspected["height"] == 96 and len(inspected["tracks"]) == 3,
                "Local inspection lost source geometry or tracks")
        plan = {"version": 1, "input": source, "ranges": ranges, "options": options}
        checked = client.preflight(plan, output, timeout=20)
        require(not list(output.iterdir()), "Preflight wrote an export or workspace")
        result = client.execute_plan(plan, output, timeout=30)
        require(len(result["outputs"]) == 2, "Local SDK lost completed outputs")
        first = Path(result["outputs"][0]["path"])
        preserved = first.read_bytes()
        try:
            client.preflight(plan, output, timeout=20)
        except LocalError as error:
            require(error.code == "output_exists", "Output conflict lost its machine-readable code")
        else:
            raise AssertionError("Preflight accepted an existing output")
        require(first.read_bytes() == preserved, "Preflight changed an existing output")
        report = {"mode": mode, "preflight": checked, "predicted_paths": checked["output_paths"],
                  "outputs": [{"path": item["path"], "artifact": item} for item in result["outputs"]]}
    elif mode == "http":
        with CutmyClient(base_url, request_timeout=20, upload_timeout=20, download_timeout=20) as client:
            uploaded = client.upload_source(source)
            request = {"source_id": uploaded["id"], "ranges": ranges, **options, "output_mode": "joined"}
            checked = client.preflight(request)
            key = "installed-python-consumer"
            job = client.create_job(request, idempotency_key=key)
            replay = client.create_job(request, idempotency_key=key)
            require(job["id"] == replay["id"], "Submission retry created a different job")
            done = client.wait_for_job(job["id"], timeout=30, poll_interval=1)
            artifacts = ready_artifacts(done)
            require(len(artifacts) == 1, "Succeeded joined job has no single artifact")
            outputs = []
            for index, artifact in enumerate(artifacts):
                downloaded = client.download(artifact, output / f"download-{index}.mp4", max_bytes=16 << 20)
                outputs.append({"path": str(downloaded), "artifact": artifact})
            report = {"mode": mode, "source_id": uploaded["id"], "preflight": checked, "job_id": job["id"],
                      "replay_id": replay["id"], "job": done, "outputs": outputs}
    else:
        raise ValueError("Use local or http")
    print(json.dumps(report, ensure_ascii=False))


if __name__ == "__main__":
    main()
