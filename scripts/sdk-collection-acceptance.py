#!/usr/bin/env python3
"""Installed Python consumer for the real collection acceptance harness.

Only loopback acceptance servers are allowed. Preparation performs one explicit
upload; subsequent processes restore the SDK's private project, not a new owner.
"""
from __future__ import annotations

import hashlib
from importlib.metadata import version
import json
import math
import os
from pathlib import Path
import re
import stat
import sys
import tempfile
import threading
from urllib.parse import urlsplit

import cutmyvideo
from cutmyvideo import CutmyClient, LocalCutmy, LocalError


SDK = "python"
EXPECTED_VERSION = "1.6.0"
JSON_LIMIT = 1 << 20
ACTIONS = {"local", "prepare", "validate", "run", "kill-eight", "kill-download", "resume", "replay", "revise-two", "draft-selected"}


class AcceptanceFailure(Exception):
    pass


def require(condition: bool, code: str) -> None:
    if not condition:
        raise AcceptanceFailure(code)


def regular_private(path: Path) -> None:
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode), "acceptance_file_not_regular")
    if os.name != "nt":
        require(info.st_mode & 0o077 == 0, "acceptance_file_not_private")


def read_json(path: Path) -> dict:
    regular_private(path)
    require(path.stat().st_size <= JSON_LIMIT, "acceptance_json_too_large")
    value = json.loads(path.read_bytes())
    require(isinstance(value, dict), "acceptance_json_not_object")
    return value


def write_new(path: Path, value: dict) -> None:
    raw = (json.dumps(value, ensure_ascii=False, separators=(",", ":")) + "\n").encode()
    require(len(raw) <= JSON_LIMIT, "acceptance_json_too_large")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(prefix=".acceptance-", dir=path.parent, delete=False) as output:
            temporary = Path(output.name)
            os.chmod(temporary, 0o600)
            output.write(raw)
            output.flush()
            os.fsync(output.fileno())
        os.link(temporary, path)
        if os.name != "nt":
            fd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def file_sha256(path: Path) -> str:
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(64 << 10), b""):
            value.update(chunk)
    return value.hexdigest()


def local_workflow(input_name: str, directory: Path, timeout: float) -> dict:
    final_dir, draft_dir = directory / "local-final", directory / "local-draft"
    final_dir.mkdir(mode=0o700)
    draft_dir.mkdir(mode=0o700)
    plan = {"version": 1, "input": str(Path(input_name).resolve()),
            "options": {"format": "mp4", "quality": "best", "cut_mode": "accurate"},
            "clips": [{"id": "opening", "label": "Начало", "ranges": [{"start_ms": 500, "end_ms": 1500}]},
                      {"id": "answer", "label": "Позже, потом раньше", "ranges": [{"start_ms": 2200, "end_ms": 3000}, {"start_ms": 200, "end_ms": 1000}]},
                      {"id": "vertical", "label": "Крупный план", "ranges": [{"start_ms": 1000, "end_ms": 2000}],
                       "options": {"crop": {"x": .25, "y": 0, "width": .5, "height": 1}}}]}
    client = LocalCutmy(os.environ["CUTMY_SDK_BINARY"], threads=1)
    checked = client.validate_collection(plan, final_dir, timeout=min(timeout, 120))
    require(checked.get("valid") is True and not list(final_dir.iterdir()), "acceptance_local_validation_wrote_files")
    final = client.execute_collection(plan, final_dir, timeout=min(timeout, 120))
    require(len(final.get("outputs", [])) == 3, "acceptance_local_outputs")
    preserved = {item["path"]: file_sha256(Path(item["path"])) for item in final["outputs"]}
    try:
        client.validate_collection(plan, final_dir, timeout=min(timeout, 120))
    except LocalError as error:
        require(error.code == "output_exists", "acceptance_local_collision_code")
    else:
        raise AcceptanceFailure("acceptance_local_collision_missing")
    require(all(file_sha256(Path(path)) == sha for path, sha in preserved.items()), "acceptance_local_collision_changed_file")
    selected = ["answer", "vertical"]
    draft_checked = client.validate_collection(plan, draft_dir, clip_ids=selected, rendition="draft", timeout=min(timeout, 120))
    require(draft_checked.get("valid") is True and not list(draft_dir.iterdir()), "acceptance_local_draft_validation_wrote_files")
    draft = client.execute_collection(plan, draft_dir, clip_ids=selected, rendition="draft", timeout=min(timeout, 120))
    require([item["id"] for item in draft.get("outputs", [])] == selected, "acceptance_local_draft_selection")
    outputs = [{**item, "clip_id": item["id"], "rendition": rendition, "sha256": file_sha256(Path(item["path"]))}
               for rendition, result in (("final", final), ("draft", draft)) for item in result["outputs"]]
    require(len(list(final_dir.iterdir())) == 3 and len(list(draft_dir.iterdir())) == 2, "acceptance_local_temporary_files")
    return {"complete": True, "phase": "local", "outputs": outputs, "pending": [], "preflight": checked, "draft_preflight": draft_checked}


def main() -> None:
    require(len(sys.argv) == 5, "acceptance_arguments")
    action, input_name, directory_name, base_url = sys.argv[1:]
    require(action in ACTIONS, "acceptance_action")
    parsed = urlsplit(base_url)
    if action != "local":
        require(parsed.scheme == "http" and parsed.hostname in {"localhost", "127.0.0.1", "::1"}
                and parsed.username is None and parsed.password is None and not parsed.query and not parsed.fragment,
                "acceptance_requires_loopback")
    require(Path(cutmyvideo.__file__).resolve().is_relative_to(Path(sys.prefix).resolve()), "acceptance_requires_installed_wheel")
    sdk_version = version("cutmyvideo")
    require(sdk_version == EXPECTED_VERSION, "acceptance_sdk_version")
    directory = Path(directory_name).resolve()
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    require(directory.is_dir(), "acceptance_directory")
    if os.name != "nt":
        require(directory.stat().st_mode & 0o077 == 0, "acceptance_directory_not_private")
    project = directory / "collection"
    owner_path, source_path = directory / "prepared-owner.json", directory / "prepared-source.json"
    timeout = float(os.environ.get("CUTMY_COLLECTION_ACCEPTANCE_TIMEOUT", "600"))
    require(math.isfinite(timeout) and 1 <= timeout <= 600, "acceptance_timeout")
    secrets: list[str] = []

    def emit(value: dict) -> None:
        raw = json.dumps(value, ensure_ascii=True, separators=(",", ":"))
        require(len(raw.encode()) <= JSON_LIMIT, "acceptance_report_too_large")
        require(not any(secret in raw for secret in secrets), "acceptance_secret_in_public_report")
        print(raw, flush=True)

    def finish(report: dict) -> None:
        emit({**report, "consumer_sdk": SDK, "consumer_sdk_version": sdk_version, "consumer_action": action})

    if action == "local":
        finish(local_workflow(input_name, directory, timeout))
        return

    if action == "prepare":
        require(not owner_path.exists() and not source_path.exists(), "acceptance_preparation_already_exists")
        input_path = Path(input_name).resolve()
        require(input_path.is_file(), "acceptance_source_missing")
        with CutmyClient(base_url, request_timeout=30, upload_timeout=300) as client:
            client.session()
            secret = client.export_session_cookie()
            secrets.append(secret)
            # Persist ownership before the one ambiguous upload; never retry it.
            write_new(owner_path, {"schema_version": 1, "server_url": base_url, "session_cookie": secret})
            source = client.upload_source(input_path)
            write_new(source_path, {"schema_version": 1, "server_url": base_url, "source": source})
            finish({"complete": True, "phase": "prepared", "source_id": source["id"], "source": source})
        return

    prepared = read_json(owner_path)
    require(prepared.get("schema_version") == 1 and prepared.get("server_url") == base_url,
            "acceptance_preparation_scope")
    secret = prepared.get("session_cookie")
    require(isinstance(secret, str) and bool(re.fullmatch(r"[A-Za-z0-9_-]{43}", secret)), "acceptance_preparation_owner")
    secrets.append(secret)
    source = read_json(source_path)
    require(source.get("server_url") == base_url and isinstance(source.get("source"), dict), "acceptance_source_scope")

    def progress(report: dict) -> None:
        # This receives only the SDK's public report, AFTER its durable save.
        marker = None
        outputs = report.get("outputs", [])
        if action == "kill-eight" and report.get("phase") == "output_ready" and len(outputs) == 8:
            regular_private(project / "state.json")
            regular_private(project / "owner.json")
            for output in outputs:
                path = Path(output["path"])
                require(path.is_file() and path.stat().st_size == output["size_bytes"]
                        and file_sha256(path) == output["sha256"], "acceptance_ready_receipt")
            marker = "eight_ready"
        elif action == "kill-download" and report.get("phase") == "download_interrupted":
            download = report.get("download", {})
            require(isinstance(download.get("offset"), int) and 0 < download["offset"] < download.get("total_size", 0),
                    "acceptance_interrupted_prefix")
            regular_private(project / "state.json")
            regular_private(project / "owner.json")
            marker = "download_interrupted"
        if marker is not None:
            emit({"event": "checkpoint", "checkpoint": marker, "report": report})
            # The external harness kills the OS process; no exception simulates
            # the crash and no ninth output can be published by this process.
            threading.Event().wait()

    request_name = {"revise-two": "revised-request.json", "draft-selected": "draft-request.json"}.get(action, "request.json")
    request = read_json(directory / request_name)
    require(request.get("source_id") == source["source"].get("id"), "acceptance_request_source")
    initial = action in {"run", "kill-eight", "kill-download"} and not (project / "state.json").exists()
    credential = secret if initial or action == "validate" else None
    with CutmyClient(base_url, request_timeout=30, download_timeout=120, session_cookie=credential) as client:
        if action == "validate":
            finish(client.validate_collection(request, timeout=timeout))
            return
        options = {"timeout": timeout, "poll_interval": 1, "on_progress": progress, "max_output_bytes": 256 << 20}
        if initial:
            report = client.run_collection(request, project, **options)
        else:
            revised = request if action in {"replay", "revise-two", "draft-selected"} else None
            report = client.resume_collection(project, request=revised, **options)
        require(action not in {"kill-eight", "kill-download"}, "acceptance_crash_checkpoint_missing")
        require(report.get("complete") is True and not report.get("pending"), "acceptance_collection_incomplete")
        finish(report)


if __name__ == "__main__":
    os.umask(0o077)
    try:
        main()
    except Exception as error:
        # Never echo exception messages, request bodies, headers or owner state.
        code = str(error) if isinstance(error, AcceptanceFailure) else getattr(error, "code", "consumer_failure")
        if not isinstance(code, str) or not re.fullmatch(r"[a-z0-9_]{1,100}", code):
            code = "consumer_failure"
        print(json.dumps({"ok": False, "code": code, "exception_type": type(error).__name__, "consumer_sdk": SDK}), file=sys.stderr)
        raise SystemExit(1)
