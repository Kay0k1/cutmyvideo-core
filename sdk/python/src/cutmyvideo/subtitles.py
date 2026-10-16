"""Protocol checks for caller-supplied timed text; no transcription or rendering."""
from __future__ import annotations

import hashlib
import json
from typing import Any, Mapping

from ._collection_protocol import clip_id
from .errors import APIError


def subtitle_request(value: Mapping[str, Any]) -> dict[str, Any]:
    encoded = json.dumps(dict(value), ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
    if len(encoded) > 4 << 20:
        raise ValueError("Subtitle request exceeds 4 MiB")
    data = json.loads(encoded)
    if (data.get("version") != 1 or isinstance(data.get("version"), bool) or data.get("format") not in ("srt", "vtt")
            or not isinstance(data.get("text"), str) or len(data["text"].encode()) > 512 << 10
            or data.get("output_format", data["format"]) not in ("srt", "vtt")
            or not isinstance(data.get("outputs"), list) or not 1 <= len(data["outputs"]) <= 128):
        raise ValueError("Invalid subtitle transformation request")
    ids = [clip_id(item.get("clip_id")) for item in data["outputs"] if isinstance(item, dict)]
    if len(ids) != len(data["outputs"]) or len(set(ids)) != len(ids):
        raise ValueError("Subtitle outputs require unique clip IDs")
    if any(not isinstance(item.get("segments"), list) or not 1 <= len(item["segments"]) <= 128 for item in data["outputs"]):
        raise ValueError("Subtitle outputs require 1..128 segments")
    if sum(len(item["segments"]) for item in data["outputs"]) > 4096:
        raise ValueError("Subtitle request exceeds 4096 segments")
    return data


def subtitle_result(value: Any, request: Mapping[str, Any] | None = None) -> dict[str, Any]:
    if (not isinstance(value, dict) or value.get("version") != 1 or isinstance(value.get("version"), bool)
            or not isinstance(value.get("outputs"), list) or not 1 <= len(value["outputs"]) <= 128):
        raise APIError("Invalid subtitle transformation result", code="invalid_response")
    outputs, size, ids = [], 0, []
    try:
        for item in value["outputs"]:
            cid = clip_id(item.get("clip_id"))
            text = item["text"]
            if (item.get("format") not in ("srt", "vtt") or not isinstance(text, str)
                    or not isinstance(item.get("cue_count"), int) or isinstance(item["cue_count"], bool)
                    or not 0 <= item["cue_count"] <= 100000):
                raise ValueError()
            encoded = text.encode()
            size += len(encoded)
            if size > 8 << 20 or item.get("sha256") != hashlib.sha256(encoded).hexdigest():
                raise ValueError()
            outputs.append({key: item[key] for key in ("clip_id", "format", "text", "cue_count", "sha256")})
            ids.append(cid)
        if len(set(ids)) != len(ids) or request and ids != [item["clip_id"] for item in request["outputs"]]:
            raise ValueError()
        if request and any(item["format"] != request.get("output_format", request["format"]) for item in outputs):
            raise ValueError()
    except (AttributeError, KeyError, TypeError, ValueError):
        raise APIError("Subtitle result identity, size or hash differs", code="invalid_response") from None
    return {"version": 1, "outputs": outputs}
