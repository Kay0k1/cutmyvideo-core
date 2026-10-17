"""Adapter fixture; installed acceptance separately uses the actual CLI/media."""
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
def flag(name):
    return args[args.index(name) + 1]

base = {"version": 1, "start_ms": 1250, "end_ms": 3000, "analyzed_end_ms": 2500}
if args[1] == "analyze":
    assert Path(flag("--input")).is_absolute()
    kind = args[2]
    if "--plan" in args:
        result = {"version": 1, "input": flag("--input"), "ranges": [{"start_ms": 1250, "end_ms": 2000}], "options": {"format": flag("--format")}, "output_mode": flag("--output-mode")}
    elif kind == "silence":
        result = {**base, "audio_track": 1, "silence": [{"start_ms": 2000, "end_ms": 2500}], "keep": [{"start_ms": 1250, "end_ms": 2000, "label": "Привет"}]}
    elif kind == "video":
        result = {**base, "video_track": 0, "scenes": [{"at_ms": 2000, "score": .9}], "black": []}
    else:
        result = {**base, "audio_track": 1, "integrated_lufs": None, "true_peak_dbtp": None, "loudness_range_lu": None}
else:
    assert args[1] == "cutlist" and "--timeout" not in args
    input_flag = next(name for name in ("--input", "--segments", "--plan") if name in args and args[args.index(name) + 1] not in ("--media", "--format"))
    path = Path(flag(input_flag))
    if os.name != "nt":
        assert path.stat().st_mode & 0o777 == 0o600 and path.parent.stat().st_mode & 0o777 == 0o700
    content = path.read_text(encoding="utf-8")
    if args[2] == "import" and "--plan" in args:
        result = {"version": 1, "input": flag("--media"), "ranges": [{"start_ms": 0, "end_ms": int(flag("--duration-ms"))}], "options": {}, "output_mode": flag("--output-mode")}
    elif args[2] == "import":
        result = {"version": 1, "entries": [{"start_ms": 0, "end_ms": None, "label": "Привет"}]}
    else:
        result = {"version": 1, "format": flag("--format"), "text": "0,,Привет\n", "document": json.loads(content)}
    result.update(input_text=content, temporary_path=str(path))
result["args"] = args
if os.environ.get("SDK_BAD_MEDIA_RESPONSE"):
    result = json.loads(os.environ["SDK_BAD_MEDIA_RESPONSE"])
print(json.dumps(result, ensure_ascii=True))
