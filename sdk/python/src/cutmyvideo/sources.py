"""Private durable source preparation, separate from a collection project."""
from __future__ import annotations

import copy
import hashlib
import os
from pathlib import Path
import secrets
import stat
import threading
import time
from typing import Any, Callable, Mapping

from ._collection_state import Project, sync_directory
from ._source_protocol import fingerprint, key_headers, public_source
from .client import CutmyClient, _COLLECTION_CANCEL, _positive
from .errors import APIError, SourcePreparationError


class _Preparation:
    def __init__(self, client: CutmyClient, project: Project, *, timeout: float = 7200,
                 cancel_event: threading.Event | None = None,
                 on_progress: Callable[[Mapping[str, Any]], None] | None = None):
        self.client, self.project = client, project
        self.deadline = time.perf_counter() + _positive(timeout, "timeout")
        self.cancel, self.progress = cancel_event, on_progress
        self.state: dict[str, Any] = {}
        self.owner: str | None = None

    def report(self) -> dict[str, Any]:
        result = {"complete": self.state.get("phase") == "ready", "phase": self.state.get("phase", "initializing")}
        for key in ("source", "fingerprint", "error"):
            if key in self.state:
                result[key] = copy.deepcopy(self.state[key])
        if self.state.get("source"):
            result["source_id"] = self.state["source"]["id"]
        return result

    def fail(self, code: str, message: str) -> None:
        raise SourcePreparationError(message, code=code, report=self.report(), directory=str(self.project.root))

    def guard(self) -> float:
        if self.owner is not None and self.client._cookie != self.owner:
            self.fail("owner_changed", "Saved source preparation owner changed")
        if self.cancel is not None and self.cancel.is_set():
            self.fail("source_preparation_cancelled", "Preparation caller stopped; saved intent was retained")
        remaining = self.deadline - time.perf_counter()
        if remaining <= 0:
            self.fail("source_preparation_timeout", "Preparation deadline exceeded; saved intent was retained")
        return remaining

    def checkpoint(self, phase: str) -> None:
        self.state["phase"] = phase
        self.project.write("state.json", self.state)
        if self.progress:
            try:
                self.progress(self.report())
            except BaseException:
                self.fail("progress_interrupted", "Preparation progress callback interrupted; checkpoint retained")

    def pause(self, delay: float) -> None:
        delay = min(max(.05, delay), self.guard())
        if self.cancel is not None:
            self.cancel.wait(delay)
        else:
            time.sleep(delay)
        self.guard()

    def hash_input(self, path: str) -> dict[str, Any]:
        result, size = hashlib.sha256(), 0
        with open(path, "rb") as source:
            before = os.fstat(source.fileno())
            if not stat.S_ISREG(before.st_mode) or not 0 < before.st_size <= (1 << 53) - 1:
                raise ValueError("Preparation upload requires a finite nonempty regular file")
            while True:
                self.guard()
                chunk = source.read(1 << 20)
                if not chunk:
                    break
                result.update(chunk)
                size += len(chunk)
            after = os.fstat(source.fileno())
        if (before.st_size, before.st_mtime_ns) != (after.st_size, after.st_mtime_ns) or size != before.st_size:
            self.fail("source_changed", "Upload input changed while computing its fingerprint")
        return {"kind": "content", "sha256": result.hexdigest(), "size_bytes": size}

    def initialize(self, source: Mapping[str, Any], key: str | None) -> None:
        if any((self.project.root / name).exists() or (self.project.root / name).is_symlink() for name in ("owner.json", "state.json", ".source-init.json")):
            raise ValueError("Preparation directory already has state; use resume_source")
        if not isinstance(source, Mapping) or source.get("kind") not in ("upload", "import"):
            raise ValueError("Use source kind upload/path or import/url")
        if source["kind"] == "upload":
            if set(source) != {"kind", "path"} or not isinstance(source.get("path"), str) or not source["path"]:
                raise ValueError("Upload preparation requires one explicit local path")
            path = str(Path(source["path"]).absolute())
            intent = {"kind": "upload", "path": path, "fingerprint": self.hash_input(path)}
        else:
            if set(source) != {"kind", "url"} or not isinstance(source.get("url"), str) or not 0 < len(source["url"].encode()) <= 16384:
                raise ValueError("Import preparation requires one bounded URL")
            intent = {"kind": "import", "url": source["url"]}
        key = key if key is not None else "source-" + secrets.token_hex(24)
        key_headers(key)
        self.client.session(timeout=min(self.client.request_timeout, self.guard()))
        self.owner = self.client.export_session_cookie()
        owner = {"schema_version": 1, "server_url": self.client._server_url, "session_cookie": self.owner}
        self.state = {"schema_version": 1, "sdk": "python", "mode": "source", "server_url": self.client._server_url,
                      "idempotency_key": key, "intent": intent, "phase": "created", "uncertain": False}
        # Seal both primaries before publishing either; no source POST occurs
        # until this journal is removed durably.
        self.project.write(".source-init.json", {"schema_version": 1, "owner": owner, "state": self.state}, exclusive=True)
        self.project.write("owner.json", owner, exclusive=True)
        self.project.write("state.json", self.state, exclusive=True)
        self.project.path(".source-init.json").unlink()
        sync_directory(self.project.root)
        self.checkpoint("created")

    def validate(self, state: Any, owner: Any) -> str:
        if (not isinstance(state, dict) or state.get("schema_version") != 1 or isinstance(state.get("schema_version"), bool)
                or state.get("sdk") != "python" or state.get("mode") != "source"
                or not isinstance(owner, dict) or owner.get("schema_version") != 1 or isinstance(owner.get("schema_version"), bool)):
            raise ValueError("Unsupported source preparation state")
        if state.get("server_url") != self.client._server_url or owner.get("server_url") != self.client._server_url:
            raise ValueError("Saved source server differs from configured trusted server")
        token = owner.get("session_cookie")
        if (not isinstance(token, str) or not 0 < len(token) <= 256 or not all(c.isascii() and (c.isalnum() or c in "-_") for c in token)
                or self.client._cookie is not None and self.client._cookie != token):
            raise ValueError("Saved source preparation owner is invalid/conflicting")
        key_headers(state.get("idempotency_key"))
        if state.get("idempotency_key") is None or not isinstance(state.get("uncertain"), bool):
            raise ValueError("Saved source intent is invalid")
        if state.get("phase") not in ("created", "submitting", "uncertain", "waiting", "processing", "ready", "failed"):
            raise ValueError("Saved source preparation phase is invalid")
        intent = state.get("intent")
        if not isinstance(intent, dict):
            raise ValueError("Saved source intent is invalid")
        if intent.get("kind") == "upload":
            if (set(intent) != {"kind", "path", "fingerprint"} or not isinstance(intent["path"], str)
                    or not Path(intent["path"]).is_absolute() or fingerprint(intent["fingerprint"])["kind"] != "content"):
                raise ValueError("Saved upload intent is invalid")
        elif intent.get("kind") == "import":
            if set(intent) != {"kind", "url"} or not isinstance(intent["url"], str) or not 0 < len(intent["url"].encode()) <= 16384:
                raise ValueError("Saved import intent is invalid")
        else:
            raise ValueError("Saved source preparation kind is invalid")
        if state.get("source") is not None:
            state["source"] = public_source(state["source"])
            state["fingerprint"] = fingerprint(state.get("fingerprint"))
            if intent["kind"] == "upload" and state["fingerprint"] != intent["fingerprint"]:
                raise ValueError("Saved prepared source differs from upload intent")
        if state.get("error") is not None:
            error = state["error"]
            if (not isinstance(error, dict) or not isinstance(error.get("code"), str)
                    or not error["code"].isascii() or not 0 < len(error["code"]) <= 128
                    or not all(c.isalnum() or c == "_" for c in error["code"]) or not isinstance(error.get("retryable"), bool)):
                raise ValueError("Saved source diagnostic is invalid")
            state["error"] = {"code": error["code"], "retryable": error["retryable"]}
            if isinstance(error.get("message"), str) and len(error["message"].encode()) <= 4096:
                state["error"]["message"] = error["message"]
        return token

    def restore(self) -> None:
        journal_path = self.project.path(".source-init.json")
        if journal_path.exists():
            journal = self.project.read(".source-init.json")
            state, owner = journal.get("state"), journal.get("owner")
            self.validate(state, owner)
            if (journal.get("schema_version") != 1 or isinstance(journal.get("schema_version"), bool)
                    or state.get("phase") != "created" or state.get("uncertain") is not False or state.get("source") is not None):
                raise ValueError("Source initialization journal was already used")
            for name, sealed in (("owner.json", owner), ("state.json", state)):
                if self.project.path(name).exists() and self.project.read(name) != sealed:
                    raise ValueError("Source initialization conflicts with existing primary")
            for name, sealed in (("owner.json", owner), ("state.json", state)):
                if not self.project.path(name).exists():
                    self.project.write(name, sealed, exclusive=True)
            journal_path.unlink()
            sync_directory(self.project.root)
        state, owner = self.project.read("state.json"), self.project.read("owner.json")
        token = self.validate(state, owner)
        self.state = state
        self.client._acquire_session(time.perf_counter() + self.guard(), self.cancel)
        try:
            if self.client._cookie is not None and self.client._cookie != token:
                raise ValueError("Client owner changed during source restore")
            self.client._cookie = token
            self.owner = token
        finally:
            self.client._session_lock.release()
        self.guard()

    def accept(self, source: Mapping[str, Any], expected: Any = None) -> dict[str, Any]:
        public = public_source(source)
        identity = fingerprint(expected if expected is not None else public.get("fingerprint"))
        if public.get("fingerprint") is not None and fingerprint(public["fingerprint"]) != identity:
            self.fail("source_fingerprint_changed", "Prepared source fingerprint differs from receipt")
        if self.state["intent"]["kind"] == "upload" and identity != self.state["intent"]["fingerprint"]:
            self.fail("source_fingerprint_changed", "Prepared source differs from saved upload content")
        public["fingerprint"] = identity
        self.state.update(source=public, fingerprint=identity, uncertain=False)
        self.state.pop("error", None)
        self.checkpoint("ready")
        return self.report()

    def run(self) -> dict[str, Any]:
        while True:
            self.guard()
            try:
                try:
                    receipt = self.client.get_source_preparation(self.state["idempotency_key"], timeout=min(self.client.request_timeout, self.guard()))
                except APIError as error:
                    if error.status != 404:
                        raise
                    receipt = {"state": "retryable"}
                if receipt.get("error") and receipt["state"] in ("failed", "ready"):
                    self.state["error"] = copy.deepcopy(receipt["error"])
                    self.checkpoint("failed")
                    self.fail(receipt["error"]["code"], "Source preparation needs explicit repair; intent retained")
                if receipt["state"] == "ready":
                    if not receipt.get("source"):
                        self.fail("invalid_response", "Ready source receipt has no source")
                    return self.accept(receipt["source"], receipt.get("fingerprint"))
                if receipt["state"] == "processing":
                    self.checkpoint("processing")
                    self.pause(max(1, receipt.get("retry_after_ms", 1000) / 1000))
                    continue
                intent = self.state["intent"]
                if intent["kind"] == "upload" and self.hash_input(intent["path"]) != intent["fingerprint"]:
                    self.fail("source_changed", "Upload input differs from the saved preparation intent")
                self.state["uncertain"] = True
                self.checkpoint("submitting")
                options = {"idempotency_key": self.state["idempotency_key"], "timeout": self.guard()}
                if intent["kind"] == "upload":
                    source = self.client.upload_source(intent["path"], fingerprint=intent["fingerprint"], **options)
                else:
                    source = self.client.import_source(intent["url"], **options)
                return self.accept(source)
            except APIError as error:
                self.guard()
                if error.code not in ("transport_error", "request_timeout", "incomplete_download") and error.status not in (408, 429, 500, 502, 503, 504):
                    self.fail(error.code, "Source preparation stopped; saved intent and owner retained")
                self.checkpoint("uncertain" if self.state.get("uncertain") else "waiting")
                self.pause(max(1, error.retry_after or 2))


def _run(client: CutmyClient, directory: str | os.PathLike[str], source: Mapping[str, Any] | None,
         key: str | None, **options: Any) -> dict[str, Any]:
    cycle: _Preparation | None = None
    active = _COLLECTION_CANCEL.set((client, options.get("cancel_event")))
    try:
        with Project(directory) as project:
            cycle = _Preparation(client, project, **options)
            if source is None:
                cycle.restore()
            else:
                cycle.initialize(source, key)
            return cycle.run()
    except SourcePreparationError:
        raise
    except (APIError, OSError, ValueError, KeyError, TypeError) as error:
        if cycle is not None:
            cycle.guard()
        raise SourcePreparationError("Source preparation state/request is invalid; existing data retained", code=error.code if isinstance(error, APIError) else "source_state_invalid",
                                     report=cycle.report() if cycle is not None else {"complete": False, "phase": "initializing"},
                                     directory=str(Path(directory).absolute())) from None
    finally:
        _COLLECTION_CANCEL.reset(active)


def prepare_source(client: CutmyClient, source: Mapping[str, Any], directory: str | os.PathLike[str], *,
                   idempotency_key: str | None = None, **options: Any) -> dict[str, Any]:
    if idempotency_key is not None:
        key_headers(idempotency_key)
    return _run(client, directory, source, idempotency_key, **options)


def resume_source(client: CutmyClient, directory: str | os.PathLike[str], **options: Any) -> dict[str, Any]:
    return _run(client, directory, None, None, **options)
