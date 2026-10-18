"""Durable HTTP collection orchestration using one saved owner and header key."""
from __future__ import annotations

import copy
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import re
import secrets
import threading
import time
from typing import Any, Callable, Mapping

from ._collection_protocol import OPAQUE_ID, clip_id, diagnostic, request_copy, selection
from ._collection_state import Project, hash_file, secure_open, single_link_fd, sync_directory
from .client import CutmyClient, _COLLECTION_CANCEL, _positive
from .errors import APIError, CollectionError, ready_artifacts

_EMPTY = hashlib.sha256(b"").hexdigest()
_RANGE = re.compile(r"^bytes ([0-9]+)-([0-9]+)/([0-9]+)$")


def _job_progress(job: Mapping[str, Any]) -> dict[str, Any]:
    """Retain public worker fields only, never an arbitrary response object."""
    result: dict[str, Any] = {}
    stage = job.get("stage")
    if isinstance(stage, str) and re.fullmatch(r"[A-Za-z0-9_-]{1,128}", stage):
        result["job_stage"] = stage
    for item in job.get("items", []):
        progress = item.get("progress_ms")
        if isinstance(progress, int) and not isinstance(progress, bool) and 0 <= progress <= (1 << 53) - 1:
            result["progress_ms"] = progress
        code = item.get("error_code")
        if item.get("status") in ("failed", "cancelled") and isinstance(code, str) and re.fullmatch(r"[a-z0-9_]{1,128}", code):
            result["error_code"] = code
            message = item.get("message")
            if isinstance(message, str) and len(message.encode()) <= 4096:
                result["worker_message"] = message
            break
    return result


class _Cycle:
    def __init__(self, client: CutmyClient, project: Project, *, timeout: float = 1800,
                 poll_interval: float = 1, cancel_event: threading.Event | None = None,
                 on_progress: Callable[[dict[str, Any]], None] | None = None,
                 max_output_bytes: int = 10 << 30, admission_batch_size: int = 3):
        self.client, self.project = client, project
        self.deadline = time.perf_counter() + _positive(timeout, "timeout")
        self.poll = _positive(poll_interval, "poll_interval")
        if self.poll < 1:
            raise ValueError("poll_interval must be at least one second")
        if isinstance(max_output_bytes, bool) or not isinstance(max_output_bytes, int) or max_output_bytes < 1:
            raise ValueError("max_output_bytes must be a positive integer")
        if isinstance(admission_batch_size, bool) or not isinstance(admission_batch_size, int) or not 1 <= admission_batch_size <= 128:
            raise ValueError("admission_batch_size must be 1..128")
        self.max_bytes, self.batch = max_output_bytes, admission_batch_size
        self.cancel, self.progress = cancel_event, on_progress
        self.state: dict[str, Any] = {}
        self._owner_cookie: str | None = None
        self._poll_after: dict[str, float] = {}

    def report(self) -> dict[str, Any]:
        request = self.state.get("request", {})
        outputs, pending = [], []
        for cid in selection(request) if self.state else []:
            clip = self.state["clips"][cid]
            artifact_id = clip.get("artifact_id")
            receipt = self.state.get("receipts", {}).get(artifact_id)
            if receipt and receipt.get("complete"):
                outputs.append({key: receipt[key] for key in ("clip_id", "job_id", "artifact_id", "size_bytes", "sha256", "segments", "track_manifest")})
                outputs[-1]["path"] = str(self.project.path(receipt["path"]))
            else:
                part = self.state.get("parts", {}).get(artifact_id, {})
                pending.append({"clip_id": cid, "code": clip.get("code", "pending"), "retryable": clip.get("retryable", True),
                                "stage": "downloading" if part else clip.get("job_stage", clip.get("job_status", "queued" if clip.get("job_id") else "admission")),
                                "downloaded_bytes": part.get("offset", 0), "total_bytes": part.get("size_bytes", 0)})
                for field in ("job_id", "job_status", "progress_ms", "error_code"):
                    if field in clip:
                        pending[-1][field] = clip[field]
                if clip.get("worker_message"):
                    pending[-1]["message"] = clip["worker_message"]
                detail = diagnostic(clip.get("diagnostic"))
                if detail:
                    pending[-1].update({key: detail[key] for key in ("field", "message") if key in detail})
        result = {"complete": bool(self.state) and not pending, "source_id": request.get("source_id", ""),
                  "rendition": request.get("rendition", "final"), "outputs": outputs, "pending": pending,
                  "phase": self.state.get("phase", "initializing")}
        if self.state.get("download"):
            result["download"] = copy.deepcopy(self.state["download"])
        detail = diagnostic(self.state.get("diagnostic"))
        if detail:
            result["diagnostic"] = detail
            for entry in pending:
                entry.update(code=detail["code"], retryable=False)
                entry.update({key: detail[key] for key in ("field", "message") if key in detail})
                if detail.get("clip_id"):
                    entry["diagnostic_clip_id"] = detail["clip_id"]
        return copy.deepcopy(result)

    def preserve_diagnostic(self, detail: Any) -> None:
        safe = diagnostic(detail)
        if not safe:
            return
        cid = safe.get("clip_id")
        if cid in self.state.get("clips", {}):
            self.state["clips"][cid].update(code=safe["code"], retryable=False, diagnostic=safe)
        else:
            self.state["diagnostic"] = safe
        self.checkpoint("diagnostic", notify=False)

    def fail(self, code: str, message: str) -> None:
        raise CollectionError(message, code=code, report=self.report(), directory=str(self.project.root))

    def guard(self) -> float:
        if self._owner_cookie is not None and self.client._cookie != self._owner_cookie:
            self.fail("owner_changed", "Client owner changed during collection; saved owner retained")
        if self.cancel is not None and self.cancel.is_set():
            self.fail("collection_cancelled", "Collection wait stopped; server jobs were retained")
        remaining = self.deadline - time.perf_counter()
        if remaining <= 0:
            self.fail("collection_timeout", "Collection deadline exceeded; state and server jobs were retained")
        return remaining

    def checkpoint(self, phase: str, *, notify: bool = True) -> None:
        self.state["phase"] = phase
        self.project.write("state.json", self.state)
        if notify and self.progress:
            try:
                self.progress(self.report())
            except BaseException:
                self.fail("progress_interrupted", "Progress callback interrupted collection; checkpoint retained")

    def pause(self, seconds: float) -> None:
        seconds = min(max(0.05, seconds), self.guard())
        if self.cancel:
            self.cancel.wait(seconds)
        else:
            time.sleep(seconds)
        self.guard()

    def timeout(self) -> float:
        return min(self.client.request_timeout, self.guard())

    def initialize(self, request: Mapping[str, Any], key: str | None) -> None:
        if any((self.project.root / name).exists() or (self.project.root / name).is_symlink() for name in ("state.json", "owner.json", ".init.json")):
            raise ValueError("Project already has saved state; use resume_collection")
        body = request_copy(request)
        key = key if key is not None else "sdk-" + secrets.token_hex(24)
        if not isinstance(key, str) or not key.strip() or len(key.encode()) > 128 or any(ord(c) < 32 or ord(c) == 127 for c in key):
            raise ValueError("Invalid collection idempotency key")
        self.client.session(timeout=self.timeout())
        self._owner_cookie = self.client.export_session_cookie()
        owner = {"schema_version": 1, "server_url": self.client._server_url, "session_cookie": self._owner_cookie}
        self.state = {"schema_version": 1, "mode": "http", "sdk": "python", "server_url": self.client._server_url,
                      "source_id": body["source_id"], "idempotency_key": key, "request": body,
                      "clips": {cid: {} for cid in selection(body)}, "receipts": {}, "parts": {}, "history": [], "validated": False,
                      "phase": "created"}
        # This private journal seals owner+key before either primary file. It
        # is removed durably before any mutation/progress callback can occur.
        self.project.write(".init.json", {"schema_version": 1, "sdk": "python", "owner": owner, "state": self.state}, exclusive=True)
        self.project.write("owner.json", owner, exclusive=True)
        self.project.write("state.json", self.state, exclusive=True)
        self.project.path(".init.json").unlink()
        sync_directory(self.project.root)
        self.checkpoint("created")

    def restore(self, revised: Mapping[str, Any] | None) -> None:
        if (self.project.root / ".init.json").exists() or (self.project.root / ".init.json").is_symlink():
            self.recover_initialization()
        state = self.project.read("state.json")
        owner = self.project.read("owner.json")
        if (state.get("schema_version") != 1 or isinstance(state.get("schema_version"), bool)
                or state.get("mode") != "http" or state.get("sdk") != "python"
                or owner.get("schema_version") != 1 or isinstance(owner.get("schema_version"), bool)):
            raise ValueError("Unsupported project SDK/schema")
        # Validate trusted configured origin BEFORE restoring or using a cookie.
        if state.get("server_url") != self.client._server_url or owner.get("server_url") != self.client._server_url:
            raise ValueError("Saved project server differs from configured trusted server")
        token = owner.get("session_cookie")
        if not isinstance(token, str) or not token or len(token) > 256 or not all(c.isascii() and (c.isalnum() or c in "-_") for c in token):
            raise ValueError("Saved owner capability is invalid")
        if self.client._cookie is not None and self.client._cookie != token:
            raise ValueError("Initialized client belongs to a different saved owner")
        body = request_copy(state.get("request", {}))
        if body["source_id"] != state.get("source_id"):
            raise ValueError("Saved project source scope differs")
        if (not isinstance(state.get("idempotency_key"), str) or not state["idempotency_key"].strip()
                or len(state["idempotency_key"].encode()) > 128 or any(ord(c) < 32 or ord(c) == 127 for c in state["idempotency_key"])):
            raise ValueError("Saved project key is invalid")
        if (not isinstance(state.get("clips"), dict) or set(state["clips"]) != set(selection(body))
                or any(not isinstance(value, dict) for value in state["clips"].values())
                or not isinstance(state.get("parts"), dict) or not isinstance(state.get("receipts"), dict)
                or len(state["parts"]) > 1024 or len(state["receipts"]) > 1024
                or not isinstance(state.get("history"), list) or len(state["history"]) > 128):
            raise ValueError("Saved project structure is invalid")
        self.state = state
        attempts = state.setdefault("attempt_keys", {})
        if (not isinstance(attempts, dict) or len(attempts) > 1024
                or any(clip_id(cid) != cid or not self.valid_key(key) for cid, key in attempts.items())):
            raise ValueError("Saved retry attempts are invalid")
        if state.get("uncertain_key") is not None and not self.valid_key(state["uncertain_key"]):
            raise ValueError("Saved uncertain attempt key is invalid")
        if state.get("project_id") is not None:
            if (not isinstance(state["project_id"], str) or not OPAQUE_ID.fullmatch(state["project_id"])
                    or isinstance(state.get("project_revision"), bool) or not isinstance(state.get("project_revision"), int) or state["project_revision"] < 1):
                raise ValueError("Saved server project scope is invalid")
        project_retries = state.get("project_retry_keys", {})
        if not isinstance(project_retries, dict) or any(cid not in state["clips"] or not self.valid_key(key) for cid, key in project_retries.items()):
            raise ValueError("Saved project retry scope is invalid")
        route = state.get("uncertain_project")
        if route is not None and (not isinstance(route, dict) or set(route) != {"id", "revision", "retry"}
                                  or route["id"] != state.get("project_id") or route["revision"] != state.get("project_revision")
                                  or isinstance(route["revision"], bool) or not isinstance(route["revision"], int)
                                  or not isinstance(route["retry"], bool) or state.get("uncertain") is None):
            raise ValueError("Saved project submission scope is invalid")
        if state.get("retry_requested") is not None:
            self.retry_ids(state["retry_requested"])
        if state.get("retry_active") is not None:
            self.retry_ids(state["retry_active"])
        for field in ("admission_retry_at", "submit_retry_at"):
            if field in state and (isinstance(state[field], bool) or not isinstance(state[field], (float, int))
                                  or not math.isfinite(state[field]) or state[field] < 0):
                raise ValueError("Saved collection admission timestamp is invalid")
        for row in state["clips"].values():
            if (row.get("job_id") is not None and (not isinstance(row["job_id"], str) or not OPAQUE_ID.fullmatch(row["job_id"])
                                                   or not isinstance(row.get("effective_request"), dict))
                    or row.get("artifact_id") is not None and (not isinstance(row["artifact_id"], str) or not OPAQUE_ID.fullmatch(row["artifact_id"]))
                    or row.get("retryable") is not None and not isinstance(row["retryable"], bool)
                    or row.get("code") is not None and not isinstance(row["code"], str)):
                raise ValueError("Saved clip job/request is invalid")
            for field in ("job_stage", "job_status", "error_code"):
                value = row.get(field)
                if value is not None and (not isinstance(value, str) or not re.fullmatch(r"[A-Za-z0-9_-]{1,128}", value)):
                    raise ValueError("Saved worker progress/diagnostic is invalid")
            if row.get("progress_ms") is not None and (isinstance(row["progress_ms"], bool) or not isinstance(row["progress_ms"], int)
                                                        or not 0 <= row["progress_ms"] <= (1 << 53) - 1):
                raise ValueError("Saved worker progress is invalid")
            if row.get("worker_message") is not None and (not isinstance(row["worker_message"], str) or len(row["worker_message"].encode()) > 4096):
                raise ValueError("Saved worker diagnostic is invalid")
            for field in ("retry_at", "next_poll_at", "download_retry_at"):
                if field in row and (isinstance(row[field], bool) or not isinstance(row[field], (float, int))
                                     or not math.isfinite(row[field]) or row[field] < 0):
                    raise ValueError("Saved clip retry timestamp is invalid")
            if row.get("job_id"):
                self._poll_after[row["job_id"]] = max(self._poll_after.get(row["job_id"], 0), row.get("next_poll_at", 0))
        if state.get("uncertain") is not None:
            uncertain = request_copy(state["uncertain"])
            original_scope = {key: value for key, value in body.items() if key != "clip_ids"}
            uncertain_scope = {key: value for key, value in uncertain.items() if key != "clip_ids"}
            if original_scope != uncertain_scope or not set(selection(uncertain)) <= set(selection(body)):
                raise ValueError("Saved uncertain submission differs from original request scope")
            if any(self.attempt_key(cid) != state.get("uncertain_key", state["idempotency_key"]) for cid in selection(uncertain)):
                raise ValueError("Saved uncertain submission differs from clip attempt scope")
        retained_bytes = 0
        for identifier, receipt in state["receipts"].items():
            self.guard()
            self.validate_receipt(identifier, receipt)
            retained_bytes += receipt["size_bytes"]
            if retained_bytes > self.max_bytes:
                self.fail("output_limit", "Retained collection outputs exceed current byte budget")
            if hash_file(self.project.path(receipt["path"]), check=self.guard) != (receipt["size_bytes"], receipt["sha256"]):
                self.fail("output_changed", "Previously completed output was changed; file preserved")
            orphan = self.project.path("parts/" + identifier + ".part")
            if orphan.exists():
                if hash_file(orphan, check=self.guard) != (receipt["size_bytes"], receipt["sha256"]):
                    self.fail("part_changed", "Completed transfer residue changed; file preserved")
                orphan.unlink()
                sync_directory(orphan.parent)
        for identifier, part in state["parts"].items():
            self.validate_part(identifier, part)
            retained_bytes += part["size_bytes"]
            if retained_bytes > self.max_bytes:
                self.fail("output_limit", "Retained collection transfers exceed current byte budget")
        if revised is not None:
            updated = request_copy(revised)
            if updated["source_id"] != body["source_id"]:
                raise ValueError("Revisions cannot change project source")
            if updated != body:
                if self.state.get("project_id"):
                    self.fail("project_conflict", "Update the server project explicitly, then adopt its current revision")
                if self.state.get("uncertain"):
                    raise ValueError("Resolve the uncertain original submission before revising")
                self.state["history"].append({"rendition": body.get("rendition", "final"),
                                              "clips": {cid: clip.get("job_id") for cid, clip in state["clips"].items()}})
                # History is advisory; verified receipts remain retained. Never
                # save more entries than a later restore can accept.
                del self.state["history"][:-128]
                self.state["request"] = updated
                self.state["clips"] = {cid: {} for cid in selection(updated)}
                self.state["validated"] = False
                self.state.pop("download", None)
                self.state.pop("diagnostic", None)
                self.checkpoint("revised")
        self.client._acquire_session(time.perf_counter() + self.guard(), self.cancel)
        try:
            if self.client._cookie is not None and self.client._cookie != token:
                raise ValueError("Initialized client owner changed during resume")
            self.client._cookie = token
            self._owner_cookie = token
        finally:
            self.client._session_lock.release()
        self.checkpoint("restored")

    def recover_initialization(self) -> None:
        journal = self.project.read(".init.json")
        owner, initial = journal.get("owner"), journal.get("state")
        if (journal.get("schema_version") != 1 or isinstance(journal.get("schema_version"), bool) or journal.get("sdk") != "python"
                or not isinstance(owner, dict) or not isinstance(initial, dict)
                or owner.get("server_url") != self.client._server_url or initial.get("server_url") != self.client._server_url
                or initial.get("schema_version") != 1 or isinstance(initial.get("schema_version"), bool)
                or initial.get("sdk") != "python" or initial.get("mode") != "http"
                or initial.get("phase") != "created" or initial.get("validated") is not False
                or initial.get("parts") != {} or initial.get("receipts") != {} or initial.get("history") != [] or initial.get("uncertain") is not None):
            raise ValueError("Initialization journal does not seal an unused project")
        body = request_copy(initial.get("request", {}))
        key = initial.get("idempotency_key")
        clips = initial.get("clips")
        if (body["source_id"] != initial.get("source_id") or not isinstance(clips, dict)
                or set(clips) != set(selection(body)) or any(value != {} for value in clips.values())
                or not isinstance(key, str) or not key.strip() or len(key.encode()) > 128
                or any(ord(c) < 32 or ord(c) == 127 for c in key)):
            raise ValueError("Initialization journal source/selection differs")
        token = owner.get("session_cookie")
        if (owner.get("schema_version") != 1 or isinstance(owner.get("schema_version"), bool)
                or not isinstance(token, str) or not token or len(token) > 256
                or not all(c.isascii() and (c.isalnum() or c in "-_") for c in token)
                or self.client._cookie is not None and self.client._cookie != token):
            raise ValueError("Initialization journal owner is invalid/conflicting")
        # Validate both primary files before completing either one. A stale or
        # altered journal cannot replace a later owner or resurrect lost state.
        for name, sealed in (("owner.json", owner), ("state.json", initial)):
            path = self.project.path(name)
            if path.exists() and self.project.read(name) != sealed:
                raise ValueError("Initialization journal differs from existing primary file")
        for name, sealed in (("owner.json", owner), ("state.json", initial)):
            if not self.project.path(name).exists():
                self.project.write(name, sealed, exclusive=True)
        self.project.path(".init.json").unlink()
        sync_directory(self.project.root)

    def validate_receipt(self, identifier: str, item: Any) -> None:
        if (not isinstance(item, dict) or item.get("artifact_id") != identifier or not OPAQUE_ID.fullmatch(identifier)
                or item.get("complete") is not True or not isinstance(item.get("size_bytes"), int)
                or isinstance(item["size_bytes"], bool) or item["size_bytes"] < 1
                or not isinstance(item.get("sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", item["sha256"])):
            raise ValueError("Saved output receipt is invalid")
        self.validate_output_identity(item)
        if not isinstance(item.get("segments"), list) or not isinstance(item.get("track_manifest"), list):
            raise ValueError("Saved output manifest is invalid")
        self.project.path(item["path"])

    def validate_output_identity(self, item: dict[str, Any]) -> None:
        cid = clip_id(item.get("clip_id"))
        if not isinstance(item.get("job_id"), str) or not OPAQUE_ID.fullmatch(item["job_id"]):
            raise ValueError("Saved job identity is invalid")
        expected = r"files/" + re.escape(cid) + r"\.(final|draft)\." + re.escape(item["artifact_id"]) + r"\.(mp4|mp3|mkv|wav|flac|m4a)"
        if not isinstance(item.get("path"), str) or not re.fullmatch(expected, item["path"]):
            raise ValueError("Saved output path does not match its clip/artifact identity")

    def validate_part(self, identifier: str, item: Any) -> None:
        if (not isinstance(item, dict) or not isinstance(identifier, str) or not OPAQUE_ID.fullmatch(identifier)
                or item.get("artifact_id") != identifier or not isinstance(item.get("offset"), int) or isinstance(item["offset"], bool)
                or not isinstance(item.get("size_bytes"), int) or isinstance(item["size_bytes"], bool)
                or not 0 <= item["offset"] <= item["size_bytes"] <= self.max_bytes or item["size_bytes"] < 1
                or not isinstance(item.get("sha256"), str) or not re.fullmatch(r"[0-9a-f]{64}", item["sha256"])):
            raise ValueError("Saved transfer receipt is invalid")
        self.validate_output_identity(item)
        if item.get("part") != "parts/" + identifier + ".part" or item.get("ready") and item["offset"] != item["size_bytes"]:
            raise ValueError("Saved partial path/completion is invalid")
        validator = item.get("last_modified")
        if validator is not None and (not isinstance(validator, str) or len(validator) > 256 or any(ord(c) < 32 for c in validator)):
            raise ValueError("Saved transfer validator is invalid")
        self.project.path(item["part"])
        self.project.path(item["path"])

    def transient(self, error: APIError) -> bool:
        return error.code in ("transport_error", "request_timeout", "incomplete_download") or error.status in (408, 429, 500, 502, 503, 504)

    @staticmethod
    def valid_key(value: Any) -> bool:
        return isinstance(value, str) and bool(value.strip()) and len(value.encode()) <= 128 and not any(ord(c) < 32 or ord(c) == 127 for c in value)

    def adopt_project(self, project_id: str, revised: Mapping[str, Any] | None) -> None:
        if not isinstance(project_id, str) or not OPAQUE_ID.fullmatch(project_id):
            raise ValueError("Project ID is invalid")
        if self.state.get("uncertain"):
            self.fail("submission_uncertain", "Resolve the saved exact submission before adopting project changes")
        project = self.client.get_project(project_id, timeout=self.timeout())
        body = request_copy({key: project[key] for key in ("source_id", "plan", "rendition", "clip_ids") if key in project})
        if body["source_id"] != self.state["source_id"]:
            self.fail("source_conflict", "Remote project belongs to a different source")
        if revised is not None and request_copy(revised) != body:
            self.fail("project_conflict", "Explicit request differs from the current remote project revision")
        expected = {item["clip_id"]: item["request"] for item in project["clips"]}
        rows: dict[str, Any] = {cid: {} for cid in selection(body)}
        for item in project["jobs"]:
            cid = item["clip_id"]
            if not item["current"]:
                continue
            if item["request"] != expected[cid]:
                self.fail("project_conflict", "Attached job differs from the current effective clip request")
            job = self.client._job(item["job"])
            row = {"job_id": job["id"], "job_status": job["status"], "effective_request": copy.deepcopy(item["request"]), "retryable": True, **_job_progress(job)}
            artifacts = ready_artifacts(job)
            if len(artifacts) == 1:
                row["artifact_id"] = artifacts[0]["id"]
            rows[cid] = row
        self.state.update(request=body, clips=rows, project_id=project_id, project_revision=project["revision"], validated=all(row.get("job_id") for row in rows.values()))
        for field in ("diagnostic", "download", "retry_requested", "retry_active"):
            self.state.pop(field, None)
        self.state.pop("project_retry_keys", None)
        self.checkpoint("project_adopted")

    def attempt_key(self, cid: str) -> str:
        return self.state.get("attempt_keys", {}).get(cid, self.state["idempotency_key"])

    def retry_ids(self, values: Any) -> list[str]:
        if isinstance(values, (str, bytes)) or not isinstance(values, (list, tuple)) or not values:
            raise ValueError("retry_failed must contain explicit clip IDs")
        ids = [clip_id(cid) for cid in values]
        if len(set(ids)) != len(ids) or any(cid not in self.state["clips"] for cid in ids):
            raise ValueError("retry_failed must select distinct clips from the saved selection")
        return ids

    def request_retry(self, values: Any) -> None:
        ids = self.retry_ids(values)
        if any(self.completed(self.state["clips"][cid]) for cid in ids):
            self.fail("retry_not_failed", "Verified completed clips cannot be retried as failures")
        if self.state.get("retry_requested") and set(ids) != set(self.state["retry_requested"]):
            self.fail("retry_pending", "Resume the saved retry request before requesting another attempt")
        self.state["retry_requested"] = ids
        self.checkpoint("retry_requested")

    def apply_retry(self) -> None:
        ids = self.state.get("retry_requested")
        if not ids or self.state.get("uncertain"):
            return
        for cid in ids:
            row = self.state["clips"][cid]
            if self.completed(row) or not (row.get("job_status") in ("failed", "cancelled") or row.get("code") == "artifact_expired"):
                self.state.pop("retry_requested", None)
                self.checkpoint("retry_rejected", notify=False)
                self.fail("retry_not_failed", "Retry requires a confirmed failed, cancelled or expired clip")
        attempts = self.state.setdefault("attempt_keys", {})
        if len(set(attempts) | set(ids)) > 1024:
            self.state.pop("retry_requested", None)
            self.checkpoint("retry_rejected", notify=False)
            self.fail("attempt_limit", "Project retry identity limit reached; existing state retained")
        for cid in ids:
            attempts[cid] = "retry-" + secrets.token_hex(24)
            if self.state.get("project_id"):
                self.state.setdefault("project_retry_keys", {})[cid] = attempts[cid]
            self.state["clips"][cid] = {}
        self.state.pop("retry_requested", None)
        self.state["retry_active"] = ids
        self.state.pop("diagnostic", None)
        self.state["validated"] = False
        # All attempt identities commit together before any fresh submission.
        self.checkpoint("retry_prepared")
        self.verify_scope_and_plan()

    def verify_scope_and_plan(self) -> None:
        if self.state.get("validated"):
            return
        while True:
            try:
                self.client.get_source(self.state["source_id"], timeout=self.timeout())
                self.client.validate_collection(self.state["request"], timeout=self.timeout())
                self.state["validated"] = True
                self.checkpoint("validated")
                return
            except APIError as error:
                self.guard()
                if not self.transient(error):
                    self.preserve_diagnostic(error.diagnostic)
                    self.fail(error.code, "Collection validation failed; state retained")
                self.pause(max(1, error.retry_after or 2))

    def submit(self, body: dict[str, Any]) -> None:
        self.state["uncertain"] = copy.deepcopy(body)
        key = self.state.get("uncertain_key", self.attempt_key(selection(body)[0]))
        if any(self.attempt_key(cid) != key for cid in selection(body)):
            self.fail("attempt_conflict", "One submission must contain clips from the same retry attempt")
        self.state["uncertain_key"] = key
        if self.state.get("project_id") and not self.state.get("uncertain_project"):
            retry = all(self.state.get("project_retry_keys", {}).get(cid) == key for cid in selection(body))
            self.state["uncertain_project"] = {"id": self.state["project_id"], "revision": self.state["project_revision"], "retry": retry}
        self.checkpoint("submitting")
        try:
            route = self.state.get("uncertain_project")
            if route:
                response = self.client.submit_project_collection(route["id"], revision=route["revision"], clip_ids=selection(body),
                                                                 retry_failed=route["retry"], idempotency_key=key, timeout=self.timeout())
            else:
                response = self.client.submit_collection(body, idempotency_key=key, timeout=self.timeout())
        except APIError as error:
            self.guard()
            if error.retry_after is not None:
                self.state["admission_retry_at"] = max(self.state.get("admission_retry_at", 0), time.time() + max(self.poll, error.retry_after))
                self.checkpoint("admission_wait", notify=False)
            if not self.transient(error):
                self.preserve_diagnostic(error.diagnostic)
                self.fail(error.code, "Collection submission failed; original owner/key retained")
            self.state["submit_failures"] = self.state.get("submit_failures", 0) + 1
            self.state["submit_retry_at"] = time.time() + max(1, error.retry_after or min(30, 2 ** min(5, self.state["submit_failures"])))
            self.checkpoint("submission_uncertain")
            return
        wanted = selection(body)
        if [item["clip_id"] for item in response["clips"]] != wanted:
            self.fail("invalid_response", "Collection response selection differs from saved submission")
        retry_delay = max(1, response["retry_after"] if response.get("retry_after") is not None else (60 if response.get("http_status") in (207, 429) else 2))
        if response.get("retry_after") is not None or not response["complete"] or response.get("http_status", 200) >= 400:
            self.state["admission_retry_at"] = max(self.state.get("admission_retry_at", 0), time.time() + max(self.poll, retry_delay))
        for item in response["clips"]:
            row = self.state["clips"][item["clip_id"]]
            if item.get("job"):
                row.update(job_id=item["job"]["id"], job_status=item["job"]["status"], effective_request=item.get("request", {}), retryable=True)
                row.pop("code", None)
                row.pop("diagnostic", None)
                self.state.get("project_retry_keys", {}).pop(item["clip_id"], None)
            else:
                row.update(code=item["error"]["code"], diagnostic=diagnostic(item["error"]), retryable=item["retryable"], retry_at=time.time() + retry_delay)
        self.state.pop("uncertain", None)
        self.state.pop("uncertain_key", None)
        self.state.pop("uncertain_project", None)
        self.state.pop("submit_retry_at", None)
        self.state["submit_failures"] = 0
        self.checkpoint("submitted")

    def completed(self, clip: dict[str, Any]) -> bool:
        return bool(self.state["receipts"].get(clip.get("artifact_id"), {}).get("complete"))

    def run(self) -> dict[str, Any]:
        self.guard()
        if self.report()["complete"]:
            self.checkpoint("complete")
            return self.report()
        self.verify_scope_and_plan()
        while True:
            self.guard()
            ordered = selection(self.state["request"])
            for cid in ordered:
                row = self.state["clips"][cid]
                if self.completed(row) or not row.get("job_id"):
                    continue
                if time.time() < max(row.get("next_poll_at", 0), self._poll_after.get(row["job_id"], 0)):
                    continue
                try:
                    job = self.client.get_job(row["job_id"], timeout=self.timeout())
                except APIError as error:
                    self.guard()
                    if not self.transient(error):
                        self.fail(error.code, "Saved collection job is unavailable")
                    row["next_poll_at"] = time.time() + max(self.poll, error.retry_after or self.poll)
                    self._poll_after[row["job_id"]] = row["next_poll_at"]
                    self.checkpoint("poll_wait", notify=False)
                    continue
                row["next_poll_at"] = time.time() + self.poll
                self._poll_after[row["job_id"]] = row["next_poll_at"]
                if job["id"] != row["job_id"]:
                    self.fail("invalid_response", "Saved job lookup returned another job identity")
                row["job_status"] = job["status"]
                for field in ("job_stage", "progress_ms", "error_code", "worker_message"):
                    row.pop(field, None)
                row.update(_job_progress(job))
                self.checkpoint("job", notify=False)
                artifacts = ready_artifacts(job)
                if artifacts:
                    if len(artifacts) != 1:
                        self.fail("invalid_response", "Joined collection job must have one artifact")
                    artifact = artifacts[0]
                    row["artifact_id"] = artifact.get("id")
                    if not self.completed(row):
                        self.transfer(cid, row, artifact)
                if job["status"] in ("failed", "cancelled"):
                    row.update(code="job_" + job["status"], retryable=False)
                elif job["status"] == "succeeded" and not artifacts:
                    row.update(code="artifact_expired", retryable=False)
            if self.state.get("retry_requested") and not self.state.get("uncertain"):
                self.apply_retry()
            terminal = [self.state["clips"][cid] for cid in ordered if self.state["clips"][cid].get("retryable") is False]
            active = self.state.get("retry_active", [])
            active_pending = any(not self.completed(self.state["clips"][cid]) and self.state["clips"][cid].get("retryable") is not False for cid in active)
            if active and not active_pending:
                self.state.pop("retry_active", None)
                self.checkpoint("retry_settled", notify=False)
            if terminal and not self.state.get("retry_requested") and not active_pending:
                self.checkpoint("terminal")
                self.fail(terminal[0].get("code", "collection_failed"), "Collection has a terminal clip diagnostic; completed files retained")
            if self.report()["complete"]:
                self.state.pop("download", None)
                self.checkpoint("complete")
                return self.report()
            inflight = sum(bool(row.get("job_id")) and not self.completed(row) and row.get("retryable") is not False for row in self.state["clips"].values())
            uncertain = self.state.get("uncertain")
            admission_open = time.time() >= self.state.get("admission_retry_at", 0)
            if uncertain and admission_open and time.time() >= self.state.get("submit_retry_at", 0):
                self.submit(uncertain)
            elif not uncertain and admission_open and inflight < self.batch:
                wanted = [cid for cid in ordered if not self.state["clips"][cid].get("job_id")
                          and time.time() >= self.state["clips"][cid].get("retry_at", 0)][:self.batch - inflight]
                if wanted:
                    key = self.attempt_key(wanted[0])
                    wanted = [cid for cid in wanted if self.attempt_key(cid) == key]
                    body = copy.deepcopy(self.state["request"])
                    body["clip_ids"] = wanted
                    self.submit(body)
                    continue
            self.pause(self.poll)

    def transfer(self, cid: str, row: dict[str, Any], artifact: dict[str, Any]) -> None:
        aid, size = artifact.get("id"), artifact.get("size_bytes")
        if not isinstance(aid, str) or not OPAQUE_ID.fullmatch(aid) or not isinstance(size, int) or isinstance(size, bool) or size <= 0:
            self.fail("invalid_response", "Artifact identity/size is invalid")
        reserved = sum(r["size_bytes"] for r in self.state["receipts"].values()) + sum(p["size_bytes"] for key, p in self.state["parts"].items() if key != aid)
        if size + reserved > self.max_bytes:
            self.fail("output_limit", "Collection exceeds saved output byte budget")
        fmt = row.get("effective_request", {}).get("format", "mp4")
        if fmt not in ("mp4", "mp3", "mkv", "wav", "flac", "m4a"):
            self.fail("invalid_response", "Artifact format is unsupported")
        rendition = self.state["request"].get("rendition", "final")
        final = f"files/{cid}.{rendition}.{aid}.{fmt}"
        part = self.state["parts"].get(aid)
        if part is None:
            if len(self.state["parts"]) >= 1024 or len(self.state["receipts"]) >= 1024:
                self.fail("receipt_limit", "Project receipt limit reached; saved state remains readable")
            if self.project.path(final).exists():
                self.fail("output_exists", "Unowned existing final file preserved")
            part = {"artifact_id": aid, "job_id": row["job_id"], "clip_id": cid, "size_bytes": size,
                    "part": "parts/" + aid + ".part", "path": final, "offset": 0, "sha256": _EMPTY, "last_modified": None}
            self.state["parts"][aid] = part
            self.checkpoint("download_intent", notify=False)
        self.validate_part(aid, part)
        if (part["size_bytes"], part["job_id"], part["path"]) != (size, row["job_id"], final):
            self.fail("artifact_changed", "Saved transfer differs from current immutable artifact")
        path = self.project.path(part["part"])
        if part.get("ready"):
            self.finish_transfer(part, artifact)
            return
        if path.exists():
            if hash_file(path, part["offset"], check=self.guard, single_link=True)[1] != part["sha256"]:
                self.fail("part_changed", "Partial transfer prefix changed; file preserved")
            fd = secure_open(path, os.O_RDWR, single_link=True)
            with os.fdopen(fd, "r+b") as stream:
                single_link_fd(stream.fileno())
                stream.truncate(part["offset"])
                stream.flush()
                os.fsync(stream.fileno())
        elif part["offset"]:
            self.fail("part_missing", "Committed transfer prefix is missing")
        else:
            fd = secure_open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, single_link=True)
            os.close(fd)
            sync_directory(path.parent)
        offset = part["offset"]
        if offset == size:
            part["ready"] = True
            self.checkpoint("download_ready", notify=False)
            self.finish_transfer(part, artifact)
            return
        headers = {"Accept-Encoding": "identity"}
        if offset:
            headers["Range"] = f"bytes={offset}-"
        if offset and part.get("last_modified"):
            headers["If-Range"] = part["last_modified"]
        resumed = offset > 0

        def receive(response: Any, connection: Any, deadline: float) -> None:
            nonlocal offset, resumed
            if response.getheader("Content-Encoding", "").strip().lower() not in ("", "identity"):
                raise APIError("Encoded artifact response refused", code="invalid_response")
            validator = response.getheader("Last-Modified")
            if validator is not None and (len(validator) > 256 or any(ord(c) < 32 for c in validator)):
                raise APIError("Invalid artifact validator", code="invalid_response")
            if response.status == 416:
                raise APIError("Artifact range refused", code="invalid_range", status=416)
            if response.status == 206:
                match = _RANGE.fullmatch(response.getheader("Content-Range", ""))
                if not resumed or not match or tuple(map(int, match.groups())) != (offset, size - 1, size):
                    raise APIError("Artifact range identity/bounds differ", code="invalid_range")
                if part.get("last_modified") and validator != part["last_modified"]:
                    raise APIError("Artifact validator changed", code="artifact_changed")
            expected = size if response.status == 200 else size - offset
            length = response.getheader("Content-Length")
            if length is None or not length.isdecimal() or int(length) != expected:
                raise APIError("Artifact transfer length differs", code="invalid_response")
            if response.status == 200:
                # An ignored Range is a fresh body, never an append.
                if offset:
                    fd = secure_open(path, os.O_RDWR, single_link=True)
                    with os.fdopen(fd, "r+b") as reset:
                        single_link_fd(reset.fileno())
                        reset.truncate(0)
                        reset.flush()
                        os.fsync(reset.fileno())
                    offset = 0
                    resumed = False
                    part.update(offset=0, sha256=_EMPTY, last_modified=validator)
                    self.checkpoint("download_restart", notify=False)
            part["last_modified"] = validator
            digest = hashlib.sha256()
            if offset:
                with os.fdopen(secure_open(path, os.O_RDONLY, single_link=True), "rb") as previous:
                    left = offset
                    while left:
                        self.guard()
                        block = previous.read(min(64 << 10, left))
                        if not block:
                            raise APIError("Committed prefix changed", code="part_changed")
                        digest.update(block)
                        left -= len(block)
            with os.fdopen(secure_open(path, os.O_RDWR, single_link=True), "r+b") as stream:
                stream.seek(offset)
                checkpointed = offset
                first = True
                def commit(phase: str) -> None:
                    stream.flush()
                    os.fsync(stream.fileno())
                    part.update(offset=offset, sha256=digest.hexdigest())
                    self.state["download"] = {"clip_id": cid, "artifact_id": aid, "offset": offset, "total_size": size, "resumed": resumed}
                    self.checkpoint(phase)
                try:
                    while offset < size:
                        self.guard()
                        self.client._set_budget(connection, deadline)
                        chunk = response.read1(min(64 << 10, size - offset))
                        if not chunk:
                            raise APIError("Artifact transfer interrupted", code="incomplete_download")
                        single_link_fd(stream.fileno())
                        stream.write(chunk)
                        digest.update(chunk)
                        offset += len(chunk)
                        if first or offset - checkpointed >= 1 << 20:
                            commit("download_checkpoint")
                            checkpointed, first = offset, False
                    if response.read1(1):
                        raise APIError("Artifact exceeded expected size", code="download_limit")
                    commit("download_ready")
                except (APIError, OSError, http.client.HTTPException):
                    commit("download_interrupted")
                    raise
            part["ready"] = True
            self.checkpoint("download_ready", notify=False)

        try:
            self.client._exchange("GET", "/api/v1/artifacts/" + self.client._id(aid) + "/download",
                                  self.client._deadline(min(self.client.download_timeout, self.guard()), self.client.download_timeout),
                                  headers=headers, receive=receive, cancel_event=self.cancel)
        except APIError as error:
            self.guard()
            if not self.transient(error):
                self.fail(error.code, "Artifact transfer failed; committed prefix retained")
            row["download_retry_at"] = time.time() + max(1, error.retry_after or 2)
            self.checkpoint("download_wait", notify=False)
            self.pause(max(1, error.retry_after or 2))
            return
        self.finish_transfer(part, artifact)

    def finish_transfer(self, part: dict[str, Any], artifact: dict[str, Any]) -> None:
        aid = part["artifact_id"]
        if aid not in self.state["receipts"] and len(self.state["receipts"]) >= 1024:
            self.fail("receipt_limit", "Project receipt limit reached before publication; saved state retained")
        self.project.publish(part["part"], part["path"], part["size_bytes"], part["sha256"], check=self.guard)
        self.state["receipts"][aid] = {"complete": True, "clip_id": part["clip_id"], "job_id": part["job_id"], "artifact_id": aid,
                                      "path": part["path"], "size_bytes": part["size_bytes"], "sha256": part["sha256"],
                                      "segments": artifact.get("segments", []), "track_manifest": artifact.get("track_manifest", [])}
        self.state["parts"].pop(aid)
        self.checkpoint("output_ready")
        path = self.project.path(part["part"])
        if path.exists():
            path.unlink()
            sync_directory(path.parent)


def _run(client: CutmyClient, directory: str | os.PathLike[str], request: Mapping[str, Any] | None,
         resume: bool, key: str | None, options: dict[str, Any]) -> dict[str, Any]:
    cycle: _Cycle | None = None
    retry_failed = options.pop("retry_failed", None)
    project_id = options.pop("project_id", None)
    token = _COLLECTION_CANCEL.set((client, options.get("cancel_event")))
    try:
        with Project(directory) as project:
            cycle = _Cycle(client, project, **options)
            if resume:
                if retry_failed is not None and request is not None:
                    raise ValueError("Apply a revision and retry failures in separate resume calls")
                cycle.restore(None if project_id is not None else request)
                if project_id is not None:
                    cycle.adopt_project(project_id, request)
                if retry_failed is not None:
                    cycle.request_retry(retry_failed)
            else:
                assert request is not None
                cycle.initialize(request, key)
            return cycle.run()
    except CollectionError:
        raise
    except APIError as error:
        if cycle is not None:
            cycle.guard()
        report = cycle.report() if cycle is not None else {"complete": False, "source_id": "", "rendition": "final", "outputs": [], "pending": [], "phase": "initializing"}
        raise CollectionError("Collection initialization failed; existing state retained", code=error.code,
                              report=report, directory=str(Path(directory).absolute())) from None
    except (OSError, ValueError, KeyError, TypeError) as error:
        if cycle is not None:
            cycle.guard()
        report = {"complete": False, "source_id": "", "rendition": "final", "outputs": [], "pending": [], "phase": "project_validation"}
        if cycle is not None:
            try:
                report = cycle.report()
            except (OSError, ValueError, KeyError, TypeError):
                pass
        raise CollectionError("Collection project validation/publication failed; existing data preserved", code="collection_state_invalid",
                              report=report, directory=str(Path(directory).absolute())) from None
    finally:
        _COLLECTION_CANCEL.reset(token)


def run_collection(client: CutmyClient, request: Mapping[str, Any], directory: str | os.PathLike[str], *,
                   idempotency_key: str | None = None, **options: Any) -> dict[str, Any]:
    return _run(client, directory, request, False, idempotency_key, options)


def resume_collection(client: CutmyClient, directory: str | os.PathLike[str], *,
                      request: Mapping[str, Any] | None = None, **options: Any) -> dict[str, Any]:
    return _run(client, directory, request, True, None, options)
