"""Blocking HTTP client with streamed media and an isolated capability session."""

from __future__ import annotations

import http.client
import json
import math
import os
import queue
import secrets
import socket
import ssl
import stat
import tempfile
import threading
import time
from http.cookies import SimpleCookie
from email.utils import parsedate_to_datetime
from pathlib import Path
from typing import Any, Callable, Mapping, cast
from urllib.parse import quote, urlsplit

from .errors import APIError, WaitCancelled, WaitTimeout, JobError
from .types import Artifact, ExportRequest, Job, PreflightResult, Source, TrackReport

_JSON_LIMIT = 1 << 20
_CHUNK = 64 << 10


def _positive(value: float, name: str) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(value) or value <= 0:
        raise ValueError(name + " must be a finite positive number")
    return float(value)


def _remaining(deadline: float) -> float:
    value = deadline - time.monotonic()
    if value <= 0:
        raise TimeoutError("operation deadline exceeded")
    return value


def _connect(host: str, port: int, deadline: float) -> socket.socket:
    # getaddrinfo has no portable timeout. Bound waiting for its daemon worker
    # separately; an abandoned resolver never acquires an HTTP socket/cookie.
    resolved: queue.Queue[Any] = queue.Queue(maxsize=1)

    def resolve() -> None:
        try:
            resolved.put(socket.getaddrinfo(host, port, type=socket.SOCK_STREAM))
        except OSError as exc:
            resolved.put(exc)

    threading.Thread(target=resolve, daemon=True).start()
    try:
        addresses = resolved.get(timeout=_remaining(deadline))
    except queue.Empty:
        raise TimeoutError("name resolution deadline exceeded") from None
    if isinstance(addresses, OSError):
        raise addresses
    last: OSError | None = None
    for family, socktype, proto, _, address in addresses:
        connection = socket.socket(family, socktype, proto)
        try:
            connection.settimeout(_remaining(deadline))
            connection.connect(address)
            return connection
        except OSError as exc:
            last = exc
            connection.close()
    raise last or OSError("No addresses for configured server")


class _HTTPConnection(http.client.HTTPConnection):
    def __init__(self, host: str, port: int, deadline: float):
        super().__init__(host, port, timeout=_remaining(deadline))
        self.deadline = deadline
        self._wire_socket: socket.socket | None = None

    def connect(self) -> None:
        self.sock = _connect(self.host, self.port, self.deadline)
        self._wire_socket = self.sock


class _HTTPSConnection(_HTTPConnection):
    def connect(self) -> None:
        super().connect()
        assert self.sock is not None
        self.sock.settimeout(_remaining(self.deadline))
        self.sock = ssl.create_default_context().wrap_socket(self.sock, server_hostname=self.host)
        self._wire_socket = self.sock


class CutmyClient:
    """A private session for one configured server; never follows redirects.

    Construction performs no network requests. The first operation initializes
    the session once. Create separate clients for separate owners. No mutation
    is retried automatically, and no cookie is persisted or exposed by repr.
    """

    def __init__(self, base_url: str, *, request_timeout: float = 30,
                 upload_timeout: float = 600, download_timeout: float = 600,
                 session_cookie: str | None = None):
        parsed = urlsplit(base_url)
        if (parsed.scheme not in ("http", "https") or not parsed.hostname
                or parsed.username is not None or parsed.password is not None
                or parsed.query or parsed.fragment or any(ord(c) < 32 for c in base_url)):
            raise ValueError("base_url must be an HTTP(S) server URL without credentials, query or fragment")
        self._host = parsed.hostname
        self._port = parsed.port or (443 if parsed.scheme == "https" else 80)
        self._secure = parsed.scheme == "https"
        self._prefix = quote(parsed.path.rstrip("/"), safe="/%:@!$&'()*+,;=-._~")
        self.request_timeout = _positive(request_timeout, "request_timeout")
        self.upload_timeout = _positive(upload_timeout, "upload_timeout")
        self.download_timeout = _positive(download_timeout, "download_timeout")
        if session_cookie is not None and (not isinstance(session_cookie, str) or not session_cookie
                or len(session_cookie) > 256 or not all(c.isascii() and (c.isalnum() or c in "-_") for c in session_cookie)):
            raise ValueError("session_cookie must be a valid capability token")
        self._cookie = session_cookie
        self._session_report: dict[str, Any] | None = None
        self._session_lock = threading.Lock()

    def __repr__(self) -> str:
        return "CutmyClient(session=" + ("initialized" if self._cookie else "uninitialized") + ")"

    def close(self) -> None:
        """Forget this owner's capability. Connections are closed per request."""
        with self._session_lock:
            self._cookie = None
            self._session_report = None

    def export_session_cookie(self) -> str:
        """Explicitly export this secret for a caller-owned credential store.

        Call session() or another HTTP operation first. Never put the returned
        capability in logs, prompts, source control, or a public plan.
        """
        if self._cookie is None:
            raise APIError("Initialize a session before exporting its capability", code="session_unavailable")
        return self._cookie

    def __enter__(self) -> CutmyClient:
        return self

    def __exit__(self, *_: Any) -> None:
        self.close()

    def _deadline(self, timeout: float | None, default: float) -> float:
        return time.monotonic() + _positive(default if timeout is None else timeout, "timeout")

    def _ensure_session(self, deadline: float, *, require_report: bool = False) -> None:
        if self._cookie and (not require_report or self._session_report is not None):
            return
        if not self._session_lock.acquire(timeout=_remaining(deadline)):
            raise TimeoutError("session initialization deadline exceeded")
        try:
            if self._cookie and (not require_report or self._session_report is not None):
                return
            data = self._exchange("GET", "/api/v1/session", deadline, session=False)
            if data.get("ok") is not True or not self._cookie:
                self._cookie = None
                raise APIError("Server did not establish a capability session", code="invalid_response")
            self._session_report = data
        except BaseException:
            self._cookie = None
            raise
        finally:
            self._session_lock.release()

    def _exchange(self, method: str, path: str, deadline: float, *,
                  body: bytes | None = None, headers: Mapping[str, str] | None = None,
                  stream: tuple[bytes, Any, bytes, int] | None = None,
                  destination: Path | None = None, max_bytes: int | None = None,
                  session: bool = True) -> Any:
        try:
            if session:
                self._ensure_session(deadline)
            conn_type = _HTTPSConnection if self._secure else _HTTPConnection
            connection = conn_type(self._host, self._port, deadline)
        except TimeoutError:
            raise APIError("Request deadline exceeded", code="request_timeout") from None
        expired = threading.Event()

        def abort() -> None:
            expired.set()
            if connection._wire_socket is not None:
                try:
                    connection._wire_socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

        timer = threading.Timer(_remaining(deadline), abort)
        timer.daemon = True
        timer.start()
        response = None
        try:
            outgoing = {"Accept": "application/json", "User-Agent": "cutmyvideo-python/1.3.0.dev0"}
            if self._cookie:
                outgoing["Cookie"] = "cutmy_session=" + self._cookie
            if headers:
                outgoing.update(headers)
            if stream:
                prefix, file, suffix, size = stream
                outgoing["Content-Length"] = str(len(prefix) + size + len(suffix))
                connection.putrequest(method, self._prefix + path)
                for key, value in outgoing.items():
                    connection.putheader(key, value)
                connection.endheaders()
                connection.send(prefix)
                left = size
                while left:
                    chunk = file.read(min(_CHUNK, left))
                    if not chunk:
                        raise OSError("Upload file became shorter during transfer")
                    self._set_budget(connection, deadline)
                    connection.send(chunk)
                    left -= len(chunk)
                if file.read(1):
                    raise OSError("Upload file changed during transfer")
                self._set_budget(connection, deadline)
                connection.send(suffix)
            else:
                connection.request(method, self._prefix + path, body, outgoing)
            self._set_budget(connection, deadline)
            response = connection.getresponse()
            if 300 <= response.status < 400:
                raise APIError("Server redirects are not followed", code="redirect_refused", status=response.status)
            if session is False:
                for key, value in response.getheaders():
                    if key.lower() == "set-cookie":
                        cookies = SimpleCookie()
                        cookies.load(value)
                        if "cutmy_session" in cookies:
                            token = cookies["cutmy_session"].value
                            if token and all(c.isascii() and (c.isalnum() or c in "-_") for c in token):
                                self._cookie = token
            if 200 <= response.status < 300 and destination is not None:
                if response.status != 200:
                    raise APIError("Expected a complete artifact response", code="invalid_response", status=response.status)
                return self._save_download(response, connection, deadline, destination, max_bytes)
            payload = self._read_bounded(response, connection, deadline, _JSON_LIMIT)
            try:
                data = json.loads(payload)
            except (ValueError, UnicodeDecodeError):
                raise APIError("Server returned invalid JSON", code="invalid_response", status=response.status) from None
            if not isinstance(data, dict):
                raise APIError("Server returned a non-object JSON response", code="invalid_response", status=response.status)
            if not 200 <= response.status < 300:
                error = data.get("error")
                code = error.get("code") if isinstance(error, dict) else None
                message = error.get("message") if isinstance(error, dict) else None
                retry_after = None
                retry = response.getheader("Retry-After")
                if retry is not None:
                    try:
                        retry_after = float(retry) if retry.isdecimal() else parsedate_to_datetime(retry).timestamp() - time.time()
                        if not math.isfinite(retry_after) or not 0 <= retry_after <= 86400:
                            retry_after = None
                    except (ValueError, TypeError, OverflowError):
                        pass
                raise APIError(message if isinstance(message, str) else "Server request failed",
                               code=code if isinstance(code, str) else "http_error", status=response.status,
                               retry_after=retry_after)
            if expired.is_set():
                raise TimeoutError("operation deadline exceeded")
            return data
        except APIError:
            raise
        except (OSError, TimeoutError, http.client.HTTPException) as exc:
            timed_out = expired.is_set() or isinstance(exc, TimeoutError) or time.monotonic() >= deadline
            raise APIError("Request deadline exceeded" if timed_out else "Could not complete server request",
                           code="request_timeout" if timed_out else "transport_error") from None
        finally:
            timer.cancel()
            if response is not None:
                response.close()
            connection.close()

    @staticmethod
    def _set_budget(connection: _HTTPConnection, deadline: float) -> None:
        remaining = _remaining(deadline)
        if connection._wire_socket is not None:
            connection._wire_socket.settimeout(remaining)

    def _read_bounded(self, response: http.client.HTTPResponse, connection: _HTTPConnection,
                      deadline: float, limit: int) -> bytes:
        chunks = bytearray()
        while True:
            self._set_budget(connection, deadline)
            chunk = response.read1(min(_CHUNK, limit + 1 - len(chunks)))
            if not chunk:
                _remaining(deadline)
                break
            chunks.extend(chunk)
            if len(chunks) > limit:
                raise APIError("JSON response exceeds SDK limit", code="response_too_large", status=response.status)
        expected = response.getheader("Content-Length")
        if expected is not None and (not expected.isdecimal() or len(chunks) != int(expected)):
            raise APIError("JSON response was incomplete", code="invalid_response", status=response.status)
        return bytes(chunks)

    def _save_download(self, response: http.client.HTTPResponse, connection: _HTTPConnection,
                       deadline: float, destination: Path, max_bytes: int | None) -> Path:
        temporary: Path | None = None
        try:
            with tempfile.NamedTemporaryFile(prefix=".cutmy-download-", dir=destination.parent, delete=False) as output:
                temporary = Path(output.name)
                size = 0
                while True:
                    self._set_budget(connection, deadline)
                    chunk = response.read1(_CHUNK)
                    if not chunk:
                        _remaining(deadline)
                        break
                    size += len(chunk)
                    if max_bytes is not None and size > max_bytes:
                        raise APIError("Download exceeds max_bytes", code="download_limit")
                    output.write(chunk)
                expected = response.getheader("Content-Length")
                if expected is not None and (not expected.isdecimal() or size != int(expected)):
                    raise APIError("Artifact transfer was incomplete", code="incomplete_download")
                _remaining(deadline)
                output.flush()
                os.fsync(output.fileno())
            # Atomic exclusive publication preserves existing files and symlinks.
            # Never fall back to replace on a filesystem without hard links.
            os.link(temporary, destination)
            return destination
        finally:
            if temporary is not None:
                temporary.unlink(missing_ok=True)

    @staticmethod
    def _id(identifier: str) -> str:
        if not isinstance(identifier, str) or not identifier or len(identifier.encode("utf-8")) > 160:
            raise ValueError("identifier must contain 1..160 UTF-8 bytes")
        return quote(identifier, safe="")

    @staticmethod
    def _source(data: Any) -> Source:
        if (not isinstance(data, dict) or not isinstance(data.get("id"), str) or not data["id"]
                or not isinstance(data.get("duration_ms"), int) or isinstance(data["duration_ms"], bool)
                or data["duration_ms"] < 1):
            raise APIError("Server returned an invalid source result", code="invalid_response")
        return cast(Source, data)

    @staticmethod
    def _job(data: Any) -> Job:
        if (not isinstance(data, dict) or not isinstance(data.get("id"), str) or not data["id"]
                or not isinstance(data.get("status"), str) or not isinstance(data.get("items"), list)
                or any(not isinstance(item, dict) or not isinstance(item.get("status"), str)
                       or (item.get("artifact") is not None and not isinstance(item["artifact"], dict))
                       for item in data["items"])):
            raise APIError("Server returned an invalid job result", code="invalid_response")
        return cast(Job, data)

    def _json(self, method: str, path: str, request: Mapping[str, Any] | None = None,
              *, timeout: float | None = None, default: float | None = None,
              headers: Mapping[str, str] | None = None) -> Any:
        deadline = self._deadline(timeout, self.request_timeout if default is None else default)
        outgoing = dict(headers or {})
        body = None
        if request is not None:
            body = json.dumps(dict(request), ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode("utf-8")
            if len(body) > 1 << 20:
                raise ValueError("JSON request exceeds 1 MiB")
            outgoing["Content-Type"] = "application/json"
        return self._exchange(method, path, deadline, body=body, headers=outgoing)

    def session(self, *, timeout: float | None = None) -> Mapping[str, Any]:
        """Get public limits; retains the same private owner session."""
        deadline = self._deadline(timeout, self.request_timeout)
        try:
            self._ensure_session(deadline, require_report=True)
        except TimeoutError:
            raise APIError("Request deadline exceeded", code="request_timeout") from None
        assert self._session_report is not None
        return dict(self._session_report)

    def list_sources(self, *, timeout: float | None = None) -> list[Source]:
        data = self._json("GET", "/api/v1/sources", timeout=timeout)
        if not isinstance(data.get("sources"), list):
            raise APIError("Server returned an invalid source list", code="invalid_response")
        return [self._source(source) for source in data["sources"]]

    def import_source(self, url: str, *, timeout: float | None = None) -> Source:
        return self._source(self._json("POST", "/api/v1/sources", {"url": url}, timeout=timeout,
                                      default=self.upload_timeout))

    def upload_source(self, path: str | os.PathLike[str], *, timeout: float | None = None) -> Source:
        deadline = self._deadline(timeout, self.upload_timeout)
        file_path = Path(path)
        boundary = "cutmy-" + secrets.token_hex(24)
        # Do not place local paths or untrusted filename syntax in multipart headers.
        filename = file_path.name.replace("\\", "_").replace('"', "_").replace("\r", "_").replace("\n", "_")
        prefix = (f"--{boundary}\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\n"
                  "Content-Type: application/octet-stream\r\n\r\n").encode("utf-8")
        suffix = f"\r\n--{boundary}--\r\n".encode("ascii")
        with file_path.open("rb") as file:
            info = os.fstat(file.fileno())
            if not stat.S_ISREG(info.st_mode):
                raise ValueError("Upload requires a regular local file")
            return self._source(self._exchange("POST", "/api/v1/uploads", deadline,
                                               headers={"Content-Type": "multipart/form-data; boundary=" + boundary},
                                               stream=(prefix, file, suffix, info.st_size)))

    def get_source(self, source_id: str, *, timeout: float | None = None) -> Source:
        return self._source(self._json("GET", "/api/v1/sources/" + self._id(source_id), timeout=timeout))

    def get_tracks(self, source_id: str, *, timeout: float | None = None) -> TrackReport:
        data = self._json("GET", "/api/v1/sources/" + self._id(source_id) + "/tracks", timeout=timeout)
        if not isinstance(data.get("tracks"), list) or any(not isinstance(track, dict) for track in data["tracks"]):
            raise APIError("Server returned an invalid track report", code="invalid_response")
        return cast(TrackReport, data)

    def preflight(self, request: ExportRequest | Mapping[str, Any], *, timeout: float | None = None) -> PreflightResult:
        data = self._json("POST", "/api/v1/jobs/preflight", request, timeout=timeout)
        if (data.get("valid") is not True or data.get("version") != 1 or isinstance(data.get("version"), bool)
                or not isinstance(data.get("checks"), list) or not isinstance(data.get("warnings"), list)):
            raise APIError("Server returned an invalid preflight report", code="invalid_response")
        return cast(PreflightResult, data)

    def create_job(self, request: ExportRequest | Mapping[str, Any], *, idempotency_key: str,
                   timeout: float | None = None) -> Job:
        if (not isinstance(idempotency_key, str) or not idempotency_key
                or len(idempotency_key.encode("utf-8")) > 128
                or not idempotency_key.strip()
                or any(ord(c) < 32 or ord(c) == 127 for c in idempotency_key)):
            raise ValueError("idempotency_key must contain 1..128 UTF-8 bytes without control characters")
        try:
            return self._job(self._json("POST", "/api/v1/jobs", request, timeout=timeout,
                                       headers={"Idempotency-Key": idempotency_key.encode("utf-8").decode("latin-1")}))
        except APIError as exc:
            exc.idempotency_key = idempotency_key
            raise

    def get_job(self, job_id: str, *, timeout: float | None = None) -> Job:
        return self._job(self._json("GET", "/api/v1/jobs/" + self._id(job_id), timeout=timeout))

    def cancel_job(self, job_id: str, *, timeout: float | None = None) -> Job:
        return self._job(self._json("POST", "/api/v1/jobs/" + self._id(job_id) + "/cancel", timeout=timeout))

    def wait_for_job(self, job_id: str, *, timeout: float = 1800, poll_interval: float = 1,
                     on_progress: Callable[[Job], None] | None = None,
                     cancel_event: threading.Event | None = None) -> Job:
        deadline = self._deadline(timeout, timeout)
        if _positive(poll_interval, "poll_interval") < 1:
            raise ValueError("poll_interval must be at least one second")
        last: Job | None = None
        while True:
            if cancel_event is not None and cancel_event.is_set():
                raise WaitCancelled(job_id, last)
            if time.monotonic() >= deadline:
                raise WaitTimeout(job_id, last)
            try:
                last = self.get_job(job_id, timeout=min(self.request_timeout, _remaining(deadline)))
            except APIError as exc:
                if time.monotonic() >= deadline:
                    raise WaitTimeout(job_id, last) from None
                raise
            if on_progress is not None:
                on_progress(last)
            status = last.get("status")
            if status == "succeeded":
                return last
            if status in ("failed", "cancelled"):
                raise JobError(last)
            duration = min(poll_interval, max(0, deadline - time.monotonic()))
            if cancel_event is not None:
                cancel_event.wait(duration)
            else:
                time.sleep(duration)

    def download(self, artifact: Artifact | Mapping[str, Any] | str, destination: str | os.PathLike[str],
                 *, timeout: float | None = None, max_bytes: int | None = None) -> Path:
        identifier = artifact if isinstance(artifact, str) else artifact.get("id")
        path = "/api/v1/artifacts/" + self._id(cast(str, identifier)) + "/download"
        if max_bytes is not None and (not isinstance(max_bytes, int) or isinstance(max_bytes, bool) or max_bytes < 1):
            raise ValueError("max_bytes must be a positive integer")
        target = Path(destination).absolute()
        if target.exists() or target.is_symlink():
            raise FileExistsError("Download destination already exists")
        return cast(Path, self._exchange("GET", path, self._deadline(timeout, self.download_timeout),
                                         destination=target, max_bytes=max_bytes))
