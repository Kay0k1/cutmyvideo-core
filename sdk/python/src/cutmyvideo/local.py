"""Local cutmy process adapter; media remains on the local filesystem."""

from __future__ import annotations

import json
import os
import signal
import subprocess
import tempfile
import threading
import time
from pathlib import Path
from typing import Any, Mapping, Sequence, cast

from .client import _positive
from .errors import LocalError
from .types import BatchResult, LocalPreflightResult, LocalSourceInfo, Plan

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

    def preflight(self, plan: Plan | Mapping[str, Any], output: str | os.PathLike[str],
                  *, timeout: float = 120) -> LocalPreflightResult:
        return cast(LocalPreflightResult, self._plan_operation("preflight", plan, output, timeout))

    def execute_plan(self, plan: Plan | Mapping[str, Any], output: str | os.PathLike[str],
                     *, timeout: float = 1800) -> BatchResult:
        return cast(BatchResult, self._plan_operation("batch", plan, output, timeout))

    def _plan_operation(self, command: str, plan: Mapping[str, Any], output: str | os.PathLike[str],
                        timeout: float) -> dict[str, Any]:
        _positive(timeout, "timeout")
        # JSON-copy prevents mutation of the caller's request and rejects NaN.
        data = json.loads(json.dumps(dict(plan), ensure_ascii=False, allow_nan=False))
        if not isinstance(data.get("input"), str) or not data["input"]:
            raise ValueError("Plan input must be a nonempty local path")
        data["input"] = str(Path(data["input"]).absolute())
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
                                      "--profile", self.profile, "--threads", str(self.threads)], timeout)

    @staticmethod
    def _stop(process: subprocess.Popen[bytes]) -> None:
        if os.name == "posix":
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
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
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
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

    def _run(self, command: str, arguments: list[str], timeout: float) -> dict[str, Any]:
        timeout = _positive(timeout, "timeout")
        argv = self._binary + ["--json-errors", command] + arguments + ["--timeout", f"{timeout:.9f}s"]
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
                    if len(buffer) + len(chunk) > _LIMIT:
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
        if command == "inspect":
            valid = (isinstance(output.get("duration_ms"), int) and not isinstance(output["duration_ms"], bool)
                     and output["duration_ms"] > 0 and isinstance(output.get("tracks"), list))
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
        if not valid:
            raise LocalError("Local cutmy returned an invalid successful JSON result", code="invalid_response")
        return output
