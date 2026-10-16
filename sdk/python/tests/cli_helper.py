"""Portable subprocess fixture, invoked with the running Python executable."""

import json
import hashlib
import os
import pathlib
import subprocess
import sys
import time

mode, *arguments = sys.argv[1:]
if mode == "subtitles":
    if arguments[:3] != ["--json-errors", "subtitles", "retime"]:
        raise RuntimeError("Expected subtitle command")
    path = pathlib.Path(arguments[arguments.index("--request") + 1])
    if os.name != "nt" and (path.stat().st_mode & 0o777 != 0o600 or path.parent.stat().st_mode & 0o777 != 0o700):
        raise RuntimeError("Unsafe subtitle request")
    request = json.loads(path.read_text(encoding="utf-8"))
    text = request["text"]
    result = {"version": 1, "outputs": [{"clip_id": item["clip_id"], "format": request.get("output_format", request["format"]), "text": text,
               "cue_count": 1, "sha256": "0" * 64 if os.environ.get("SDK_BAD_SUBTITLE_HASH") else hashlib.sha256(text.encode()).hexdigest()} for item in request["outputs"]]}
    print(json.dumps(result, ensure_ascii=True))
    raise SystemExit()
if mode == "child":
    time.sleep(30)
    raise SystemExit()
if mode == "sleep":
    child = subprocess.Popen([sys.executable, __file__, "child"])
    pathlib.Path(os.environ["SDK_TEST_PID"]).write_text(str(child.pid), encoding="ascii")
    time.sleep(30)
if mode == "large":
    sys.stdout.write("x" * ((1 << 20) + 1))
    sys.stdout.flush()
    time.sleep(30)
if mode == "malformed":
    print("{}\n{}")
    raise SystemExit()
if mode == "wrong-shape":
    print(json.dumps({"unexpected": "response"}))
    raise SystemExit()
if mode == "unknown-error":
    print(json.dumps({"error": {"code": "future_media_code", "message": "Future diagnostic"}}), file=sys.stderr)
    raise SystemExit(1)
if mode == "partial":
    print(json.dumps({"incomplete": True, "outputs": [{"path": "first.mp4", "size_bytes": 7}]}))
    print(json.dumps({"error": {"code": "output_exists", "message": "Existing output preserved"}}), file=sys.stderr)
    raise SystemExit(1)
if arguments[0:2] != ["--json-errors", "inspect"]:
    index = arguments.index("--plan")
    plan_path = pathlib.Path(arguments[index + 1])
    plan = json.loads(plan_path.read_text(encoding="utf-8"))
    assert pathlib.Path(plan["input"]).is_absolute()
    if os.name != "nt":
        assert plan_path.stat().st_mode & 0o777 == 0o600
    result = {"outputs": [], "plan": plan, "plan_path": str(plan_path), "args": arguments}
    if arguments[1] == "preflight":
        result.update({"version": 1, "valid": True, "checks": [], "warnings": [], "output_paths": []})
    elif arguments[1:3] == ["collection", "validate"]:
        result.update({"version": 1, "valid": True, "clips": [], "selected_clip_ids": []})
else:
    result = {"duration_ms": 1000, "tracks": [], "args": arguments}
print(json.dumps(result))
