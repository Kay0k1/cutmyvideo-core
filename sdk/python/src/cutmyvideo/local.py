"""Local cutmy process adapter; media remains on the local filesystem."""

from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Mapping, Sequence, cast

from .client import _positive
from .errors import APIError, LocalError
from .types import (PosterResult, BatchResult, CollectionPlan, LocalCollectionPreflight, LocalCollectionResult, LocalPreflightResult,
                    LocalSourceInfo, Plan, SubtitleRequest, SubtitleResult, SilenceOptions, SilenceResult,
                    VideoAnalysisOptions, VideoAnalysisResult, LoudnessOptions, LoudnessResult, CutList,
                    CutListImportRequest, LocalCutListExportRequest, CutListExportResult)

_LIMIT = 1 << 20


class LocalCutmy:
    """Use an installed cutmy binary; never downloads or replaces executables.

    ``binary`` can be one executable or an argv prefix for a trusted wrapper.
    An SDK deadline includes a bounded two-second termination grace period.
    POSIX children share a private process group and are terminated together.
    Windows uses CTRL_BREAK then taskkill /T /F when available; a custom wrapper
    must preserve its process tree for descendant cleanup to work.
    """

    def __init__(self, binary: str | os.PathLike[str] | Sequence[str] = "cutmy", *,
                 profile: str = "fast", threads: int = 2,
                 env: Mapping[str, str] | None = None):
        if isinstance(binary, (str, os.PathLike)):
            self._binary = [os.fspath(binary)]
        else:
            self._binary = list(binary)
        if not self._binary or any(not isinstance(arg, str) or not arg or "\0" in arg for arg in self._binary):
            raise ValueError("binary must be a nonempty executable or argv sequence")
        if profile not in ("fast", "compact"):
            raise ValueError("profile must be fast or compact")
        if isinstance(threads, bool) or not isinstance(threads, int) or not 1 <= threads <= 32:
            raise ValueError("threads must be an integer from 1 to 32")
        self.profile, self.threads = profile, threads
        self._env = dict(env) if env is not None else None

    def inspect(self, input: str | os.PathLike[str], *, timeout: float = 120) -> LocalSourceInfo:
        return cast(LocalSourceInfo, self._run("inspect", ["--input", str(Path(input).absolute())], timeout))

    def generate_poster(self, input: str | os.PathLike[str], output: str | os.PathLike[str], options: Mapping[str, Any], *, timeout: float = 30) -> PosterResult:
        from .media import poster_options, poster_result
        body = poster_options(options)
        timestamp = body["timestamp_ms"]
        value = self._run("poster", ["--input", str(Path(input).absolute()), "--output", str(Path(output).absolute()), "--at", f"{timestamp // 1000}.{timestamp % 1000:03d}", "--format", body["format"], "--threads", str(self.threads)], timeout)
        try:
            return cast(PosterResult, poster_result(value, body))
        except APIError:
            raise LocalError("Local poster result is invalid", code="invalid_response") from None

    def _analyze(self, kind: str, input: str | os.PathLike[str], options: Mapping[str, Any] | None,
                 timeout: float, extra: list[str] | None = None) -> dict[str, Any]:
        from .media import analysis_flags
        return self._run(["analyze", kind], ["--input", str(Path(input).absolute()), "--threads", str(self.threads)]
                         + analysis_flags(kind, options) + (extra or []), timeout)

    def analyze_silence(self, input: str | os.PathLike[str], options: SilenceOptions | None = None, *, timeout: float = 120) -> SilenceResult:
        return cast(SilenceResult, self._analyze("silence", input, options, timeout))

    def analyze_video(self, input: str | os.PathLike[str], options: VideoAnalysisOptions | None = None, *, timeout: float = 120) -> VideoAnalysisResult:
        return cast(VideoAnalysisResult, self._analyze("video", input, options, timeout))

    def analyze_loudness(self, input: str | os.PathLike[str], options: LoudnessOptions | None = None, *, timeout: float = 120) -> LoudnessResult:
        return cast(LoudnessResult, self._analyze("loudness", input, options, timeout))

    def silence_plan(self, input: str | os.PathLike[str], options: SilenceOptions | None = None, *,
                     format: str = "mp4", output_mode: str = "joined", timeout: float = 120) -> Plan:
        if format not in ("mp4", "mp3") or output_mode not in ("joined", "separate"):
            raise ValueError("Silence plans require mp4|mp3 and joined|separate")
        return cast(Plan, self._analyze("silence", input, options, timeout, ["--plan", "--format", format, "--output-mode", output_mode]))

    def _cut_operation(self, operation: str, content: str, format: str, timeout: float,
                       flag: str = "--input", extra: list[str] | None = None) -> dict[str, Any]:
        from .media import cut_format
        deadline = time.monotonic() + _positive(timeout, "timeout")
        cut_format(format)
        encoded = content.encode("utf-8")
        if len(encoded) > _LIMIT:
            raise ValueError("Cut-list input exceeds1MiB")
        with tempfile.TemporaryDirectory(prefix="cutmy-sdk-cutlist-") as directory:
            path = Path(directory) / "input.txt"
            with path.open("xb") as file:
                if os.name != "nt":
                    os.chmod(path, 0o600)
                file.write(encoded)
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise LocalError("Local cut-list deadline exceeded", code="timeout")
            return self._run(["cutlist", operation], ["--format", format, flag, str(path)] + (extra or []), remaining)

    def import_cut_list(self, request: CutListImportRequest | Mapping[str, Any], *, timeout: float = 120) -> CutList:
        from .media import cut_import
        body = cut_import(request)
        return cast(CutList, self._cut_operation("import", body["text"], body["format"], timeout))

    def plan_cut_list(self, request: CutListImportRequest | Mapping[str, Any], media: str | os.PathLike[str], *,
                      duration_ms: int, output_mode: str = "joined", timeout: float = 120) -> Plan:
        from .media import cut_import, integer
        body = cut_import(request)
        if not integer(duration_ms) or duration_ms == 0 or output_mode not in ("joined", "separate"):
            raise ValueError("Cut-list plans require a positive duration_ms and joined|separate output_mode")
        return cast(Plan, self._cut_operation("import", body["text"], body["format"], timeout,
                    extra=["--plan", "--media", str(Path(media).absolute()), "--duration-ms", str(duration_ms), "--output-mode", output_mode]))

    def export_cut_list(self, request: LocalCutListExportRequest | Mapping[str, Any], *, timeout: float = 120) -> CutListExportResult:
        from .media import cut_export, cut_format, plan_result
        if set(request) == {"format", "plan"}:
            cut_format(request["format"])
            try:
                plan_result(request["plan"])
            except APIError:
                raise ValueError("Invalid plan") from None
            body = dict(request)
            field = "plan"
        else:
            body = cut_export(request)
            field = "cut_list" if "cut_list" in body else "segments"
        return cast(CutListExportResult, self._cut_operation("export", json.dumps(body[field], ensure_ascii=False, allow_nan=False),
                    body["format"], timeout, flag="--input" if field == "cut_list" else "--" + field))

    def preflight(self, plan: Plan | Mapping[str, Any], output: str | os.PathLike[str],
                  *, timeout: float = 120) -> LocalPreflightResult:
        return cast(LocalPreflightResult, self._plan_operation("preflight", plan, output, timeout))

    def execute_plan(self, plan: Plan | Mapping[str, Any], output: str | os.PathLike[str],
                     *, timeout: float = 1800) -> BatchResult:
        return cast(BatchResult, self._plan_operation("batch", plan, output, timeout))

    def validate_collection(self, plan: CollectionPlan | Mapping[str, Any], output: str | os.PathLike[str], *,
                            clip_ids: Sequence[str] | None = None, rendition: str = "final", timeout: float = 120) -> LocalCollectionPreflight:
        return cast(LocalCollectionPreflight, self._collection_operation("validate", plan, output, clip_ids, rendition, timeout))

    def execute_collection(self, plan: CollectionPlan | Mapping[str, Any], output: str | os.PathLike[str], *,
                           clip_ids: Sequence[str] | None = None, rendition: str = "final", timeout: float = 1800) -> LocalCollectionResult:
        return cast(LocalCollectionResult, self._collection_operation("export", plan, output, clip_ids, rendition, timeout))

    def _collection_operation(self, operation: str, plan: Mapping[str, Any], output: str | os.PathLike[str],
                              clip_ids: Sequence[str] | None, rendition: str, timeout: float) -> dict[str, Any]:
        from ._collection_protocol import clip_id
        if rendition not in ("draft", "final"):
            raise ValueError("rendition must be draft or final")
        flags = ["--rendition", rendition]
        if clip_ids is not None:
            if isinstance(clip_ids, (str, bytes)):
                raise ValueError("clip_ids must be a sequence of clip IDs")
            ids = [clip_id(value) for value in clip_ids]
            if len(ids) != len(set(ids)):
                raise ValueError("clip_ids must be unique")
            if ids:
                flags += ["--clips", ",".join(ids)]
        return self._plan_operation(["collection", operation], plan, output, timeout, flags)

    def retime_subtitles(self, request: SubtitleRequest | Mapping[str, Any], *, timeout: float = 120) -> SubtitleResult:
        from .subtitles import subtitle_request, subtitle_result
        body = subtitle_request(request)
        with tempfile.TemporaryDirectory(prefix="cutmy-sdk-subtitles-") as directory:
            path = Path(directory) / "request.json"
            with path.open("xb") as file:
                if os.name != "nt":
                    os.chmod(path, 0o600)
                file.write(json.dumps(body, ensure_ascii=False, allow_nan=False).encode())
            result = self._run(["subtitles", "retime"], ["--request", str(path)], timeout, max_json_bytes=64 << 20)
        try:
            return cast(SubtitleResult, subtitle_result(result, body))
        except APIError:
            raise LocalError("Local subtitle result identity, size or hash differs", code="invalid_response") from None

    def _plan_operation(self, command: str | list[str], plan: Mapping[str, Any], output: str | os.PathLike[str],
                        timeout: float, extra: list[str] | None = None) -> dict[str, Any]:
        _positive(timeout, "timeout")
        # JSON-copy prevents mutation of the caller's request and rejects NaN.
        data = json.loads(json.dumps(dict(plan), ensure_ascii=False, allow_nan=False))
        if not isinstance(data.get("input"), str) or not data["input"]:
            raise ValueError("Plan input must be a nonempty local path")
        data["input"] = str(Path(data["input"]).absolute())
        from .media import render_option_sets, render_references
        render_references(data, local=True)
        for options in render_option_sets(data):
            if options.get("audio_replace") is not None:
                options["audio_replace"]["input"] = str(Path(options["audio_replace"]["input"]).absolute())
        encoded = json.dumps(data, ensure_ascii=False, allow_nan=False).encode("utf-8")
        if len(encoded) > 1 << 20:
            raise ValueError("Plan exceeds 1 MiB")
        with tempfile.TemporaryDirectory(prefix="cutmy-sdk-plan-") as directory:
            path = Path(directory) / "plan.json"
            with path.open("xb") as file:
                if os.name != "nt":
                    os.chmod(path, 0o600)
                file.write(encoded)
            return self._run(command, ["--plan", str(path), "--output", str(Path(output).absolute()),
                                      "--profile", self.profile, "--threads", str(self.threads)] + (extra or []), timeout)

    @staticmethod
    def _signal_group(process: subprocess.Popen[bytes], signum: int) -> None:
        try:
            os.killpg(process.pid, signum)
        except ProcessLookupError:
            pass
        except PermissionError:
            # Darwin skips zombies when signalling a group and can report
            # EPERM once no live members remain. A reaped leader must not
            # turn successful shutdown into an unrelated permission failure.
            # Preserve permission failures for a live leader or other OSes.
            if sys.platform != "darwin" or process.poll() is None:
                raise

    @staticmethod
    def _stop(process: subprocess.Popen[bytes]) -> None:
        if os.name == "posix":
            LocalCutmy._signal_group(process, signal.SIGTERM)
        else:
            try:
                process.send_signal(getattr(signal, "CTRL_BREAK_EVENT"))
            except (OSError, ValueError):
                process.terminate()
        try:
            process.wait(timeout=2)
        except subprocess.TimeoutExpired:
            pass
        # The leader may already have exited while descendants hold a pipe.
        if os.name == "posix":
            LocalCutmy._signal_group(process, signal.SIGKILL)
        else:
            try:
                subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                               stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                               timeout=2, check=False)
            except (OSError, subprocess.TimeoutExpired):
                pass
            if process.poll() is None:
                process.kill()
        process.wait()

    def _run(self, command: str | list[str], arguments: list[str], timeout: float, *, max_json_bytes: int = _LIMIT) -> dict[str, Any]:
        timeout = _positive(timeout, "timeout")
        cli_timeout = [] if isinstance(command, list) and command[0] == "cutlist" else ["--timeout", f"{timeout:.9f}s"]
        argv = self._binary + ["--json-errors"] + ([command] if isinstance(command, str) else command) + arguments + cli_timeout
        kwargs: dict[str, Any] = {"stdout": subprocess.PIPE, "stderr": subprocess.PIPE, "stdin": subprocess.DEVNULL}
        if os.name == "posix":
            kwargs["start_new_session"] = True
        elif os.name == "nt":
            kwargs["creationflags"] = getattr(subprocess, "CREATE_NEW_PROCESS_GROUP")
        if self._env is not None:
            kwargs["env"] = {**os.environ, **self._env}
        try:
            process: subprocess.Popen[bytes] = subprocess.Popen(argv, **kwargs)
        except OSError:
            raise LocalError("Could not start the installed cutmy executable", code="tool_unavailable") from None
        exceeded = threading.Event()
        capture: list[bytearray] = [bytearray(), bytearray()]

        def read(stream: Any, buffer: bytearray) -> None:
            try:
                while True:
                    chunk = stream.read1(64 << 10)
                    if not chunk:
                        return
                    if len(buffer) + len(chunk) > (max_json_bytes if buffer is capture[0] else _LIMIT):
                        exceeded.set()
                        return
                    buffer.extend(chunk)
            finally:
                stream.close()

        readers = [threading.Thread(target=read, args=(stream, capture[index]), daemon=True)
                   for index, stream in enumerate((process.stdout, process.stderr))]
        for reader in readers:
            reader.start()
        deadline = time.monotonic() + timeout
        reason: str | None = None
        try:
            while process.poll() is None or any(reader.is_alive() for reader in readers):
                if exceeded.is_set():
                    reason = "response_too_large"
                    break
                if time.monotonic() >= deadline:
                    reason = "timeout"
                    break
                time.sleep(min(0.01, max(0, deadline - time.monotonic())))
            if reason:
                self._stop(process)
            else:
                process.wait()
        except BaseException:
            self._stop(process)
            raise
        finally:
            for reader in readers:
                reader.join(timeout=2)
        output: dict[str, Any] | None = None
        if capture[0]:
            try:
                parsed = json.loads(capture[0])
                if isinstance(parsed, dict):
                    output = parsed
            except (ValueError, UnicodeDecodeError):
                pass
        partial = output.get("outputs", []) if output and output.get("incomplete") is True else []
        if not isinstance(partial, list) or any(not isinstance(item, dict) for item in partial):
            partial = []
        if reason:
            raise LocalError("Local operation deadline exceeded" if reason == "timeout" else "Local JSON output exceeds SDK limit",
                             code=reason, exit_code=process.returncode, outputs=partial)
        if process.returncode:
            code, message = "local_error", "Local cutmy operation failed"
            try:
                diagnostic = json.loads(capture[1])
                error = diagnostic.get("error") if isinstance(diagnostic, dict) else None
                if isinstance(error, dict) and isinstance(error.get("code"), str) and isinstance(error.get("message"), str):
                    code, message = error["code"], error["message"]
            except (ValueError, UnicodeDecodeError):
                pass
            raise LocalError(message, code=code, exit_code=process.returncode, outputs=partial)
        if output is None or output.get("incomplete") is True:
            raise LocalError("Local cutmy returned an invalid successful JSON result", code="invalid_response", outputs=partial)
        if isinstance(command, list) and command[0] in ("analyze", "cutlist"):
            from .media import analysis_result, cut_list, cut_export_result, plan_result
            try:
                if command[0] == "analyze":
                    plan_result(output) if "--plan" in arguments else analysis_result(command[1], output)
                elif command[1] == "import":
                    plan_result(output) if "--plan" in arguments else cut_list(output)
                else:
                    cut_export_result(output, arguments[arguments.index("--format") + 1])
            except APIError:
                raise LocalError("Local analysis or cut-list result is invalid", code="invalid_response") from None
            valid = True
        elif command == "poster":
            valid = isinstance(output.get("path"), str) and output.get("format") in ("jpeg", "png")
        elif command == "inspect":
            valid = (isinstance(output.get("duration_ms"), int) and not isinstance(output["duration_ms"], bool)
                     and output["duration_ms"] > 0 and isinstance(output.get("tracks"), list))
        elif command == ["collection", "validate"]:
            valid = (output.get("valid") is True and output.get("version") == 1
                     and not isinstance(output.get("version"), bool) and isinstance(output.get("clips"), list)
                     and isinstance(output.get("selected_clip_ids"), list))
        elif command == ["subtitles", "retime"]:
            from .subtitles import subtitle_result
            try:
                subtitle_result(output)
            except APIError:
                raise LocalError("Local subtitle result identity, size or hash differs", code="invalid_response") from None
            valid = True
        elif command == "preflight":
            valid = (output.get("valid") is True and output.get("version") == 1
                     and not isinstance(output.get("version"), bool)
                     and isinstance(output.get("checks"), list) and isinstance(output.get("warnings"), list)
                     and isinstance(output.get("output_paths"), list)
                     and all(isinstance(path, str) for path in output["output_paths"]))
        else:
            valid = (isinstance(output.get("outputs"), list)
                     and all(isinstance(item, dict) and isinstance(item.get("path"), str)
                             for item in output["outputs"]))
        if valid and command == "preflight" and "request" in output:
            from .media import delivery_response
            try:
                delivery_response(output["request"])
            except APIError:
                valid = False
        if not valid:
            raise LocalError("Local cutmy returned an invalid successful JSON result", code="invalid_response")
        return output
