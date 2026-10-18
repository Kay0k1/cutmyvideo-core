"""Bounded collection JSON validation shared by local and durable adapters."""
from __future__ import annotations

import json
import re
from typing import Any, Mapping

from .errors import APIError


def diagnostic(value: Any) -> dict[str, str] | None:
    """Only public collection repair fields; never retain arbitrary response data."""
    if not isinstance(value, dict):
        return None
    result = {key: value[key] for key in ("code", "message", "clip_id", "field")
              if isinstance(value.get(key), str) and len(value[key].encode()) <= 4096}
    return result if isinstance(result.get("code"), str) else None

ID = re.compile(r"^[a-z0-9][a-z0-9_-]{0,63}$")
RESERVED = re.compile(r"^(con|prn|aux|nul|com[1-9]|lpt[1-9])$")
OPAQUE_ID = re.compile(r"^[A-Za-z0-9_-]{1,160}$")


def clip_id(value: Any) -> str:
    if not isinstance(value, str) or not ID.fullmatch(value) or RESERVED.fullmatch(value):
        raise ValueError("clip IDs must be portable lowercase ASCII names")
    return value


def copied(value: Mapping[str, Any], limit: int = 1 << 20) -> dict[str, Any]:
    body = json.dumps(dict(value), ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
    if len(body) > limit:
        raise ValueError("Collection JSON exceeds its byte limit")
    return json.loads(body)


def request_copy(request: Mapping[str, Any]) -> dict[str, Any]:
    data = copied(request)
    from .media import render_references
    render_references(data)
    if not isinstance(data.get("source_id"), str) or not data["source_id"] or len(data["source_id"].encode()) > 160:
        raise ValueError("Collection source_id is required")
    plan = data.get("plan")
    if not isinstance(plan, dict) or plan.get("version") != 1 or isinstance(plan.get("version"), bool) or plan.get("input"):
        raise ValueError("HTTP collections require a version1 plan without local input")
    clips = plan.get("clips")
    if not isinstance(clips, list) or not 1 <= len(clips) <= 128:
        raise ValueError("Collection plan must contain1..128 clips")
    ids = []
    for clip in clips:
        if not isinstance(clip, dict):
            raise ValueError("Collection clip must be an object")
        ids.append(clip_id(clip.get("id")))
    if len(set(ids)) != len(ids):
        raise ValueError("Collection clip IDs must be unique")
    selected = data.get("clip_ids", [])
    if not isinstance(selected, list) or any(not isinstance(cid, str) or cid not in ids for cid in selected) or len(set(selected)) != len(selected):
        raise ValueError("Collection selection must contain distinct existing clip IDs")
    if data.get("rendition", "final") not in ("final", "draft"):
        raise ValueError("Collection rendition must be final or draft")
    return data


def selection(request: Mapping[str, Any]) -> list[str]:
    wanted = request.get("clip_ids", [])
    return [clip["id"] for clip in request["plan"]["clips"] if not wanted or clip["id"] in wanted]


def submission(data: Any, job_validator: Any) -> dict[str, Any]:
    if (not isinstance(data, dict) or data.get("version") != 1 or isinstance(data.get("version"), bool)
            or not isinstance(data.get("complete"), bool) or data.get("rendition") not in ("draft", "final")
            or not isinstance(data.get("clips"), list) or not 1 <= len(data["clips"]) <= 128):
        raise APIError("Invalid collection admission response", code="invalid_response")
    ids = []
    try:
        for item in data["clips"]:
            if not isinstance(item, dict) or not isinstance(item.get("retryable"), bool):
                raise ValueError()
            ids.append(clip_id(item.get("clip_id")))
            if "request" in item:
                from .media import delivery_response
                delivery_response(item["request"])
            if item.get("job") is not None:
                job_validator(item["job"])
            elif not isinstance(item.get("error"), dict) or not isinstance(item["error"].get("code"), str):
                raise ValueError()
        if len(ids) != len(set(ids)) or (data["complete"] and any(item.get("job") is None for item in data["clips"])):
            raise ValueError()
    except (TypeError, ValueError):
        raise APIError("Invalid collection admission response", code="invalid_response") from None
    return data
