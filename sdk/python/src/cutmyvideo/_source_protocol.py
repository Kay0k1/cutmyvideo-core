"""Bounded source preparation protocol; no credentials enter public receipts."""
from __future__ import annotations

import re
from typing import Any, Mapping

from .errors import APIError


def key_headers(key: str | None, expected: Mapping[str, Any] | None = None, *, upload: bool = False) -> dict[str, str]:
    if key is None:
        if expected is not None:
            raise ValueError("A fingerprint requires an explicit preparation key")
        return {}
    if not isinstance(key, str) or not key or key in (".", "..") or len(key) > 128 or any(not 33 <= ord(c) <= 126 for c in key):
        raise ValueError("Source preparation keys require 1..128 visible ASCII bytes without spaces or reserved dot paths")
    headers = {"Idempotency-Key": key}
    if upload:
        value = fingerprint(expected)
        if value["kind"] != "content":
            raise ValueError("Keyed uploads require a content fingerprint")
        headers.update({"Upload-SHA256": value["sha256"], "Upload-Size": str(value["size_bytes"])})
    return headers


def fingerprint(value: Any) -> dict[str, Any]:
    if isinstance(value, dict) and value.get("kind") == "content":
        if (isinstance(value.get("sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", value["sha256"])
                and isinstance(value.get("size_bytes"), int) and not isinstance(value["size_bytes"], bool)
                and 0 < value["size_bytes"] <= (1 << 53) - 1):
            return {"kind": "content", "sha256": value["sha256"], "size_bytes": value["size_bytes"]}
    if isinstance(value, dict) and value.get("kind") == "provider":
        if (all(isinstance(value.get(key), str) and 0 < len(value[key].encode()) <= 512 for key in ("provider", "provider_id"))
                and isinstance(value.get("canonical_url_sha256"), str) and re.fullmatch(r"[0-9a-f]{64}", value["canonical_url_sha256"])):
            return {key: value[key] for key in ("kind", "provider", "provider_id", "canonical_url_sha256")}
    raise ValueError("Invalid source fingerprint")


def public_source(value: Any) -> dict[str, Any]:
    if (not isinstance(value, dict) or not isinstance(value.get("id"), str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,160}", value["id"])
            or not isinstance(value.get("duration_ms"), int) or isinstance(value["duration_ms"], bool) or value["duration_ms"] < 1):
        raise APIError("Invalid prepared source", code="invalid_response")
    result = {key: value[key] for key in ("id", "duration_ms", "title", "kind", "provider", "provider_video_id", "source_url", "preview_kind", "preview_url", "embed_url", "thumbnail_url", "expires_at", "retention_blocked", "media_removed", "width", "height") if key in value}
    for key in ("title", "kind", "provider", "provider_video_id", "source_url", "preview_kind", "preview_url", "embed_url", "thumbnail_url", "expires_at"):
        if key in result and result[key] is not None and not isinstance(result[key], str):
            raise APIError("Invalid prepared source metadata", code="invalid_response")
    for key in ("retention_blocked", "media_removed"):
        if key in result and not isinstance(result[key], bool):
            raise APIError("Invalid prepared source metadata", code="invalid_response")
    for key in ("width", "height"):
        if key in result and (isinstance(result[key], bool) or not isinstance(result[key], int) or result[key] < 0):
            raise APIError("Invalid prepared source metadata", code="invalid_response")
    if value.get("fingerprint") is not None:
        result["fingerprint"] = fingerprint(value["fingerprint"])
    return result


def preparation(value: Any, key: str) -> dict[str, Any]:
    if (not isinstance(value, dict) or value.get("version") != 1 or isinstance(value.get("version"), bool)
            or value.get("key") != key or value.get("state") not in ("processing", "ready", "retryable", "failed")):
        raise APIError("Invalid source preparation receipt", code="invalid_response")
    result: dict[str, Any] = {"version": 1, "key": key, "state": value["state"]}
    if value.get("source_id") is not None:
        if not isinstance(value["source_id"], str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,160}", value["source_id"]):
            raise APIError("Invalid source preparation identity", code="invalid_response")
        result["source_id"] = value["source_id"]
    if value.get("source") is not None:
        result["source"] = public_source(value["source"])
        if result.get("source_id", result["source"]["id"]) != result["source"]["id"]:
            raise APIError("Prepared source identities differ", code="invalid_response")
    if value.get("fingerprint") is not None:
        result["fingerprint"] = fingerprint(value["fingerprint"])
    delay = value.get("retry_after_ms")
    if delay is not None:
        if isinstance(delay, bool) or not isinstance(delay, int) or not 0 <= delay <= 86400000:
            raise APIError("Invalid preparation retry delay", code="invalid_response")
        result["retry_after_ms"] = delay
    error = value.get("error")
    if isinstance(error, dict) and isinstance(error.get("code"), str) and re.fullmatch(r"[a-z0-9_]{1,128}", error["code"]):
        result["error"] = {"code": error["code"], "retryable": error.get("retryable") is True}
        if isinstance(error.get("message"), str) and len(error["message"].encode()) <= 4096:
            result["error"]["message"] = error["message"]
    if value["state"] == "failed" and "error" not in result:
        raise APIError("Failed preparation lacks a diagnostic", code="invalid_response")
    return result
