"""Portable edit documents and strict owner-protected project responses."""
from __future__ import annotations

import copy
from typing import Any, Mapping

from ._collection_protocol import OPAQUE_ID, copied, request_copy, selection
from ._source_protocol import fingerprint, public_source
from .errors import APIError


def _portable_plan(value: Any) -> dict[str, Any]:
    """Edit documents have a closed vocabulary, unlike private server state."""
    def fields(item: Any, allowed: set[str]) -> None:
        if (not isinstance(item, dict) or set(item) - allowed
                or any(isinstance(value, (dict, list)) for key, value in item.items() if key not in {"options", "crop", "clips", "ranges"})):
            raise ValueError("Portable project contains unknown edit fields")
    def options(item: Any) -> None:
        fields(item, {"format", "quality", "cut_mode", "audio_track", "no_audio", "subtitle_mode", "crop", "clear_crop"})
        if "crop" in item:
            fields(item["crop"], {"x", "y", "width", "height"})
    fields(value, {"version", "input", "options", "clips"})
    if not isinstance(value.get("clips"), list):
        raise ValueError("Portable project clips must be an array")
    if "options" in value:
        options(value["options"])
    for clip in value["clips"]:
        fields(clip, {"id", "label", "ranges", "options"})
        if "options" in clip:
            options(clip["options"])
        if not isinstance(clip.get("ranges"), list):
            raise ValueError("Portable project ranges must be an array")
        for interval in clip["ranges"]:
            fields(interval, {"start_ms", "end_ms", "label"})
    return value


def export_portable_project(request: Mapping[str, Any], source: Mapping[str, Any] | None = None) -> dict[str, Any]:
    body = request_copy(request)
    metadata: dict[str, Any] = {}
    if source is not None:
        source = public_source(dict(source))
        for key in ("duration_ms", "title"):
            if key in source:
                metadata[key] = copy.deepcopy(source[key])
        if source.get("fingerprint") is not None:
            metadata["fingerprint"] = fingerprint(source["fingerprint"])
    result = {"kind": "cutmy.collection.project", "version": 1, "source": metadata, "plan": _portable_plan(body["plan"])}
    for key in ("clip_ids", "rendition"):
        if key in body:
            result[key] = body[key]
    return copied(result)


def import_portable_project(document: Mapping[str, Any], source: Mapping[str, Any]) -> dict[str, Any]:
    value = copied(document)
    if (set(value) - {"kind", "version", "source", "plan", "clip_ids", "rendition"}
            or value.get("kind") != "cutmy.collection.project" or value.get("version") != 1
            or isinstance(value.get("version"), bool) or not isinstance(value.get("source"), dict)
            or set(value["source"]) - {"fingerprint", "duration_ms", "title"}):
        raise ValueError("Invalid portable project document")
    trusted = public_source(dict(source))
    _portable_plan(value.get("plan"))
    expected = value["source"].get("fingerprint")
    if expected is not None and (trusted.get("fingerprint") is None or fingerprint(expected) != fingerprint(trusted["fingerprint"])):
        raise ValueError("Portable project source fingerprint differs from the explicitly supplied source")
    if value["source"].get("duration_ms") is not None and value["source"]["duration_ms"] != trusted["duration_ms"]:
        raise ValueError("Portable project source duration differs")
    return request_copy({"source_id": trusted["id"], "plan": value["plan"], **{key: value[key] for key in ("clip_ids", "rendition") if key in value}})


def project_result(value: Any) -> dict[str, Any]:
    if (not isinstance(value, dict) or value.get("version") != 1 or isinstance(value.get("version"), bool)
            or not isinstance(value.get("id"), str) or not OPAQUE_ID.fullmatch(value["id"])
            or not isinstance(value.get("revision"), int) or isinstance(value["revision"], bool) or value["revision"] < 1
            or not isinstance(value.get("jobs"), list) or len(value["jobs"]) > 128 or not isinstance(value.get("clips"), list)):
        raise APIError("Invalid project response", code="invalid_response")
    try:
        source = public_source(value["source"])
        body = request_copy({"source_id": value["source_id"], "plan": value["plan"], "rendition": value["rendition"],
                             **({"clip_ids": value["clip_ids"]} if "clip_ids" in value else {})})
        if source["id"] != body["source_id"] or [item["clip_id"] for item in value["clips"]] != selection(body):
            raise ValueError()
        for item in value["clips"]:
            if not isinstance(item.get("request"), dict) or item["request"].get("source_id") != source["id"]:
                raise ValueError()
        ids = []
        all_ids = {clip["id"] for clip in body["plan"]["clips"]}
        for item in value["jobs"]:
            ids.append(item["clip_id"])
            if (item["clip_id"] not in all_ids or not isinstance(item.get("current"), bool)
                    or item["current"] and item["clip_id"] not in selection(body)
                    or not isinstance(item.get("request"), dict) or item["request"].get("source_id") != source["id"]
                    or not isinstance(item.get("job"), dict) or not isinstance(item["job"].get("id"), str)
                    or not OPAQUE_ID.fullmatch(item["job"]["id"])):
                raise ValueError()
        if len(set(ids)) != len(ids):
            raise ValueError()
    except (ValueError, KeyError, TypeError):
        raise APIError("Project source, plan and jobs do not share one scope", code="invalid_response") from None
    result = {"version": 1, "id": value["id"], "revision": value["revision"], "source": source, **body,
              "clips": [{"clip_id": item["clip_id"], "request": copy.deepcopy(item["request"])} for item in value["clips"]],
              "jobs": [{key: copy.deepcopy(item[key]) for key in ("clip_id", "request", "current", "job")} for item in value["jobs"]]}
    for key in ("expires_at", "access_expires_at"):
        if isinstance(value.get(key), str):
            result[key] = value[key]
    return result
