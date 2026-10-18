"""Bounded public analysis and cut-list JSON; no media tools run in this module."""
from __future__ import annotations

import math
from typing import Any, Mapping

from ._collection_protocol import copied
from .errors import APIError

_FIELDS = {
    "silence": {"start_ms", "end_ms", "audio_track", "threshold_db", "min_silence_ms", "keep_before_ms", "keep_after_ms", "min_clip_ms"},
    "video": {"start_ms", "end_ms", "scene_threshold", "black_pixel_threshold", "black_picture_ratio", "min_black_ms"},
    "loudness": {"start_ms", "end_ms", "audio_track"},
}


def integer(value: Any) -> bool:
    return type(value) is int and 0 <= value <= 9007199254740991


def number(value: Any) -> bool:
    try:
        return type(value) in (int, float) and math.isfinite(value)
    except OverflowError:
        return False


def analysis_options(kind: str, value: Mapping[str, Any] | None) -> dict[str, Any]:
    result = copied({} if value is None else value)
    if set(result) - _FIELDS[kind] or any(not number(v) for v in result.values()):
        raise ValueError("Analysis options must contain only finite numeric fields for this analysis")
    for key, v in result.items():
        if (key.endswith("_ms") or key == "audio_track") and not integer(v):
            raise ValueError(key + " must be a nonnegative safe integer")
    return result


def analysis_flags(kind: str, value: Mapping[str, Any] | None) -> list[str]:
    flags: list[str] = []
    for key, v in analysis_options(kind, value).items():
        if key == "end_ms" and v == 0:
            continue
        if key in ("start_ms", "end_ms"):
            flags += ["--" + key[:-3], f"{v // 1000}.{v % 1000:03d}"]
        else:
            flags += ["--" + key.replace("_", "-"), str(v)]
    return flags


def _intervals(value: Any, start: int, end: int, *, labels: bool = False) -> bool:
    return (isinstance(value, list) and len(value) <= 4096 and all(
        isinstance(row, dict) and integer(row.get("start_ms")) and integer(row.get("end_ms"))
        and start <= row["start_ms"] < row["end_ms"] <= end
        and (not labels or isinstance(row.get("label"), str)) for row in value))


def analysis_result(kind: str, value: Any) -> dict[str, Any]:
    valid = (isinstance(value, dict) and type(value.get("version")) is int and value["version"] == 1
             and all(integer(value.get(k)) for k in ("start_ms", "end_ms", "analyzed_end_ms")))
    if valid:
        start, end, analyzed = (value[k] for k in ("start_ms", "end_ms", "analyzed_end_ms"))
        valid = start < end and start <= analyzed <= end
        if kind == "silence":
            valid = valid and integer(value.get("audio_track")) and _intervals(value.get("silence"), start, analyzed) and _intervals(value.get("keep"), start, analyzed, labels=True)
        elif kind == "video":
            scenes = value.get("scenes")
            valid = (valid and integer(value.get("video_track")) and _intervals(value.get("black"), start, analyzed)
                     and isinstance(scenes, list) and len(scenes) + len(value["black"]) <= 4096
                     and all(isinstance(row, dict) and integer(row.get("at_ms")) and start <= row["at_ms"] <= analyzed
                             and number(row.get("score")) and 0 <= row["score"] <= 1 for row in scenes))
        else:
            valid = valid and integer(value.get("audio_track")) and all(k in value and (value[k] is None or number(value[k])) for k in ("integrated_lufs", "true_peak_dbtp", "loudness_range_lu"))
    if not valid:
        raise APIError("Invalid media analysis response", code="invalid_response")
    return value


def cut_list(value: Any) -> dict[str, Any]:
    if (not isinstance(value, dict) or type(value.get("version")) is not int or value["version"] != 1
            or not isinstance(value.get("entries"), list) or not 1 <= len(value["entries"]) <= 128
            or any(not isinstance(row, dict) or not integer(row.get("start_ms")) or "end_ms" not in row
                   or row["end_ms"] is not None and (not integer(row["end_ms"]) or row["end_ms"] <= row["start_ms"])
                   or not isinstance(row.get("label"), str) or len(row["label"].encode()) > 200 for row in value["entries"])):
        raise APIError("Invalid cut-list response", code="invalid_response")
    return value


def cut_format(value: Any) -> str:
    if value not in ("csv", "ffmetadata"):
        raise ValueError("Cut-list format must be csv or ffmetadata")
    return value


def cut_import(value: Mapping[str, Any]) -> dict[str, Any]:
    result = copied(value)
    if set(result) != {"format", "text"} or not isinstance(result.get("text"), str):
        raise ValueError("Cut-list import requires format and text")
    cut_format(result["format"])
    return result


def cut_export(value: Mapping[str, Any]) -> dict[str, Any]:
    result = copied(value)
    cut_format(result.get("format"))
    if set(result) not in ({"format", "cut_list"}, {"format", "segments"}):
        raise ValueError("Cut-list export requires exactly one cut_list or segments")
    if "cut_list" in result:
        try:
            cut_list(result["cut_list"])
        except APIError:
            raise ValueError("Invalid cut_list") from None
    elif not isinstance(result["segments"], list) or not 1 <= len(result["segments"]) <= 128:
        raise ValueError("segments must contain1..128 segment maps")
    return result


def cut_export_result(value: Any, format: str) -> dict[str, Any]:
    if (not isinstance(value, dict) or type(value.get("version")) is not int or value["version"] != 1
            or value.get("format") != format or not isinstance(value.get("text"), str)
            or len(value["text"].encode()) > 1 << 20):
        raise APIError("Invalid cut-list export response", code="invalid_response")
    return value


def plan_result(value: Any) -> dict[str, Any]:
    if (not isinstance(value, dict) or type(value.get("version")) is not int or value["version"] != 1
            or not isinstance(value.get("input"), str) or not value["input"] or not isinstance(value.get("options"), dict)
            or not isinstance(value.get("ranges"), list) or not 1 <= len(value["ranges"]) <= 128
            or not _intervals(value["ranges"], 0, 9007199254740991)):
        raise APIError("Invalid generated edit plan", code="invalid_response")
    return value


def render_option_sets(value: Mapping[str, Any]):
    yield value
    plan = value.get("plan", value)
    if isinstance(plan, dict):
        if isinstance(plan.get("options"), dict):
            yield plan["options"]
        for clip in plan.get("clips", []):
            if isinstance(clip, dict) and isinstance(clip.get("options"), dict):
                yield clip["options"]


def render_references(value: Mapping[str, Any], *, local: bool = False) -> None:
    for options in render_option_sets(value):
        replacement = options.get("audio_replace")
        if replacement is None:
            continue
        field, forbidden = ("input", "source_id") if local else ("source_id", "input")
        if (not isinstance(replacement, dict) or forbidden in replacement or not isinstance(replacement.get(field), str)
                or not replacement[field] or "\0" in replacement[field]):
            raise ValueError("audio_replace requires only a local input" if local else "HTTP audio_replace requires source_id and cannot open local paths")
