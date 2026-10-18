"""Blocking HTTP client with streamed media and an isolated capability session."""

from __future__ import annotations

import http.client
from contextvars import ContextVar
import errno
from functools import partial
import io
import json
import math
import os
import queue
import secrets
import selectors
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
from .types import Artifact, CollectionRequest, CollectionRunReport, CollectionSubmission, CollectionValidation, ExportRequest, Job, PreflightResult, Source, TrackReport
from .types import (AgentProject, ProjectCreateRequest, ProjectEditRequest, ProjectHandoff, SourcePreparationInput,
                    SourcePreparationReceipt, SourcePreparationReport, SubtitleRequest, SubtitleResult,
                    SilenceOptions, SilenceResult, VideoAnalysisOptions, VideoAnalysisResult,
                    LoudnessOptions, LoudnessResult, CutList, CutListImportRequest, CutListExportRequest, CutListExportResult)

_JSON_LIMIT = 1 << 20
_CHUNK = 64 << 10
_COLLECTION_CANCEL: ContextVar[tuple[object, threading.Event | None] | None] = ContextVar("cutmy_collection_cancel", default=None)


def _positive(value: float, name: str) -> float:
    if not isinstance(value, (int, float)) or isinstance(value, bool) or not math.isfinite(value) or value <= 0:
        raise ValueError(name + " must be a finite positive number")
    return float(value)


def _remaining(deadline: float) -> float:
    value = deadline - time.perf_counter()
    if value <= 0:
        raise TimeoutError("operation deadline exceeded")
    return value


def _connect(host: str, port: int, deadline: float, cancel_event: threading.Event | None = None) -> socket.socket:
    # getaddrinfo has no portable timeout. Bound waiting for its daemon worker
    # separately; an abandoned resolver never acquires an HTTP socket/cookie.
    resolved: queue.Queue[Any] = queue.Queue(maxsize=1)

    def resolve() -> None:
        try:
            resolved.put(socket.getaddrinfo(host, port, type=socket.SOCK_STREAM))
        except OSError as exc:
            resolved.put(exc)

    threading.Thread(target=resolve, daemon=True).start()
    while True:
        if cancel_event is not None and cancel_event.is_set():
            raise APIError("Collection connection cancelled", code="collection_cancelled")
        try:
            remaining = _remaining(deadline)
            addresses = resolved.get(timeout=min(.05, remaining) if cancel_event is not None else remaining)
            break
        except queue.Empty:
            if cancel_event is None or time.perf_counter() >= deadline:
                raise TimeoutError("name resolution deadline exceeded") from None
    if isinstance(addresses, OSError):
        raise addresses
    last: OSError | None = None
    for family, socktype, proto, _, address in addresses:
        connection = socket.socket(family, socktype, proto)
        try:
            if cancel_event is None:
                connection.settimeout(_remaining(deadline))
                connection.connect(address)
            else:
                connection.setblocking(False)
                status = connection.connect_ex(address)
                pending = {errno.EINPROGRESS, errno.EALREADY, errno.EWOULDBLOCK}
                if os.name == "nt":
                    pending.update((10035, 10036, 10037))
                if status and status not in pending:
                    raise OSError(status, "Connection failed")
                if status:
                    with selectors.DefaultSelector() as selector:
                        selector.register(connection, selectors.EVENT_WRITE)
                        while True:
                            if cancel_event.is_set():
                                raise APIError("Collection connection cancelled", code="collection_cancelled")
                            if selector.select(min(.05, _remaining(deadline))):
                                status = connection.getsockopt(socket.SOL_SOCKET, socket.SO_ERROR)
                                if status:
                                    raise OSError(status, "Connection failed")
                                break
                connection.settimeout(_remaining(deadline))
            return connection
        except OSError as exc:
            last = exc
            connection.close()
        except BaseException:
            connection.close()
            raise
    raise last or OSError("No addresses for configured server")


class _CancellableReader(io.RawIOBase):
    """Poll nonblocking socket reads; shutdown need not wake a Winsock recv."""

    def __init__(self, sock: socket.socket, deadline: float, cancel_event: threading.Event):
        super().__init__()
        self.sock, self.deadline, self.cancel = sock, deadline, cancel_event
        self.raw: Any = None
        sock.setblocking(False)
        # Keep SocketIO's reference ownership, including Connection: close.
        self.raw = sock.makefile("rb", buffering=0)

    def readable(self) -> bool:
        return True

    def readinto(self, buffer: Any) -> int:
        while True:
            if self.cancel.is_set():
                raise APIError("Collection read cancelled", code="collection_cancelled")
            _remaining(self.deadline)
            wait_for = selectors.EVENT_READ
            try:
                count = self.raw.readinto(buffer)
                if count is not None:
                    if self.cancel.is_set():
                        raise APIError("Collection read cancelled", code="collection_cancelled")
                    return count
            except ssl.SSLWantWriteError:
                wait_for = selectors.EVENT_WRITE
            except (BlockingIOError, ssl.SSLWantReadError):
                pass
            with selectors.DefaultSelector() as readiness:
                readiness.register(self.sock, wait_for)
                while True:
                    if self.cancel.is_set():
                        raise APIError("Collection read cancelled", code="collection_cancelled")
                    if readiness.select(min(.05, _remaining(self.deadline))):
                        break

    def close(self) -> None:
        if not self.closed:
            try:
                if self.raw is not None:
                    self.raw.close()
            finally:
                super().close()


class _ReadSocket:
    def __init__(self, sock: socket.socket, deadline: float, cancel_event: threading.Event):
        self.sock, self.deadline, self.cancel = sock, deadline, cancel_event

    def makefile(self, mode: str) -> io.BufferedReader:
        if mode != "rb":
            raise ValueError("Collection response socket is read-only")
        return io.BufferedReader(_CancellableReader(self.sock, self.deadline, self.cancel))


class _CancellableResponse(http.client.HTTPResponse):
    def __init__(self, sock: socket.socket, debuglevel: int = 0, method: str | None = None, url: str | None = None, *,
                 deadline: float, cancel_event: threading.Event):
        super().__init__(_ReadSocket(sock, deadline, cancel_event), debuglevel, method, url)


class _HTTPConnection(http.client.HTTPConnection):
    def __init__(self, host: str, port: int, deadline: float, cancel_event: threading.Event | None = None):
        super().__init__(host, port, timeout=_remaining(deadline))
        self.deadline = deadline
        self._wire_socket: socket.socket | None = None
        self.cancel_event = cancel_event
        self._cancellable_read = False

    def connect(self) -> None:
        self.sock = _connect(self.host, self.port, self.deadline, self.cancel_event)
        self._wire_socket = self.sock

    def getresponse(self) -> http.client.HTTPResponse:
        if self.cancel_event is not None:
            self._cancellable_read = True
            self.response_class = partial(_CancellableResponse, deadline=self.deadline, cancel_event=self.cancel_event)
        return super().getresponse()

    def send(self, data: Any) -> None:
        if self.cancel_event is None:
            return super().send(data)
        if self.sock is None:
            self.connect()
        assert self.sock is not None
        self.sock.setblocking(False)
        remaining = memoryview(data)
        while remaining:
            if self.cancel_event.is_set():
                raise APIError("Source upload cancelled", code="collection_cancelled")
            _remaining(self.deadline)
            try:
                count = self.sock.send(remaining)
                if count <= 0:
                    raise OSError("Connection closed while sending request")
                remaining = remaining[count:]
                continue
            except ssl.SSLWantReadError:
                event = selectors.EVENT_READ
            except (BlockingIOError, ssl.SSLWantWriteError):
                event = selectors.EVENT_WRITE
            with selectors.DefaultSelector() as readiness:
                readiness.register(self.sock, event)
                while True:
                    if self.cancel_event.is_set():
                        raise APIError("Source upload cancelled", code="collection_cancelled")
                    if readiness.select(min(.05, _remaining(self.deadline))):
                        break


class _HTTPSConnection(_HTTPConnection):
    def connect(self) -> None:
        super().connect()
        assert self.sock is not None
        self.sock.settimeout(_remaining(self.deadline))
        self.sock = ssl.create_default_context().wrap_socket(self.sock, server_hostname=self.host, do_handshake_on_connect=False)
        self._wire_socket = self.sock
        if self.cancel_event is None:
            self.sock.do_handshake()
            return
        self.sock.setblocking(False)
        while True:
            if self.cancel_event.is_set():
                raise APIError("Collection TLS connection cancelled", code="collection_cancelled")
            _remaining(self.deadline)
            try:
                self.sock.do_handshake()
                break
            except ssl.SSLWantReadError:
                wait_for = selectors.EVENT_READ
            except ssl.SSLWantWriteError:
                wait_for = selectors.EVENT_WRITE
            with selectors.DefaultSelector() as readiness:
                readiness.register(self.sock, wait_for)
                while True:
                    if self.cancel_event.is_set():
                        raise APIError("Collection TLS connection cancelled", code="collection_cancelled")
                    if readiness.select(min(.05, _remaining(self.deadline))):
                        break
        self.sock.settimeout(_remaining(self.deadline))


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
        authority = "[" + self._host + "]" if ":" in self._host else self._host
        if self._port != (443 if self._secure else 80):
            authority += ":" + str(self._port)
        self._server_url = parsed.scheme + "://" + authority + self._prefix
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
        return time.perf_counter() + _positive(default if timeout is None else timeout, "timeout")

    def _ensure_session(self, deadline: float, *, require_report: bool = False) -> None:
        if self._cookie and (not require_report or self._session_report is not None):
            return
        self._acquire_session(deadline)
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

    def _acquire_session(self, deadline: float, cancel_event: threading.Event | None = None) -> None:
        active = _COLLECTION_CANCEL.get()
        if cancel_event is None and active is not None and active[0] is self:
            cancel_event = active[1]
        while True:
            if cancel_event is not None and cancel_event.is_set():
                raise APIError("Collection session wait cancelled", code="collection_cancelled")
            remaining = _remaining(deadline)
            if self._session_lock.acquire(timeout=min(.05, remaining) if cancel_event is not None else remaining):
                return
            if cancel_event is None:
                raise TimeoutError("session initialization deadline exceeded")

    def _exchange(self, method: str, path: str, deadline: float, *,
                  body: bytes | None = None, headers: Mapping[str, str] | None = None,
                  stream: tuple[bytes, Any, bytes, int] | None = None,
                  destination: Path | None = None, max_bytes: int | None = None,
                  session: bool = True, collection_response: bool = False,
                  receive: Callable[..., Any] | None = None,
                  cancel_event: threading.Event | None = None,
                  json_limit: int = _JSON_LIMIT) -> Any:
        active = _COLLECTION_CANCEL.get()
        if cancel_event is None and active is not None and active[0] is self:
            cancel_event = active[1]
        if cancel_event is not None and cancel_event.is_set():
            raise APIError("Collection request cancelled", code="collection_cancelled")
        try:
            if session:
                self._ensure_session(deadline)
            conn_type = _HTTPSConnection if self._secure else _HTTPConnection
            connection = conn_type(self._host, self._port, deadline, cancel_event)
        except TimeoutError:
            raise APIError("Request deadline exceeded", code="request_timeout") from None
        expired = threading.Event()
        finished = threading.Event()

        def abort() -> None:
            expired.set()
            if connection._wire_socket is not None:
                try:
                    connection._wire_socket.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

        timer: threading.Timer | None = None
        response = None
        try:
            timer = threading.Timer(_remaining(deadline), abort)
            timer.daemon = True
            timer.start()
            if cancel_event is not None:
                def cancel_transfer() -> None:
                    while not finished.wait(0.05):
                        if cancel_event.is_set():
                            abort()
                            return
                threading.Thread(target=cancel_transfer, daemon=True).start()
            outgoing = {"Accept": "application/json", "User-Agent": "cutmyvideo-python/1.5.0"}
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
            if receive is not None and response.status in (200, 206, 416):
                return receive(response, connection, deadline)
            if 200 <= response.status < 300 and destination is not None:
                if response.status != 200:
                    raise APIError("Expected a complete artifact response", code="invalid_response", status=response.status)
                return self._save_download(response, connection, deadline, destination, max_bytes)
            payload = self._read_bounded(response, connection, deadline, json_limit)
            try:
                data = json.loads(payload)
            except (ValueError, UnicodeDecodeError):
                raise APIError("Server returned invalid JSON", code="invalid_response", status=response.status) from None
            if not isinstance(data, dict):
                raise APIError("Server returned a non-object JSON response", code="invalid_response", status=response.status)
            retry_after = None
            retry = response.getheader("Retry-After")
            if retry is not None:
                try:
                    retry_after = float(retry) if retry.isdecimal() else parsedate_to_datetime(retry).timestamp() - time.time()
                    if not math.isfinite(retry_after) or not 0 <= retry_after <= 86400:
                        retry_after = None
                except (ValueError, TypeError, OverflowError):
                    pass
            if collection_response and "clips" in data and response.status in (200, 202, 207, 400, 408, 410, 413, 429, 500, 502, 503, 504):
                from ._collection_protocol import submission
                submission(data, self._job)
                return {**data, "http_status": response.status, "retry_after": retry_after}
            if not 200 <= response.status < 300:
                error = data.get("error")
                code = error.get("code") if isinstance(error, dict) else None
                message = error.get("message") if isinstance(error, dict) else None
                detail = None
                if path in ("/api/v1/collections", "/api/v1/collections/validate"):
                    from ._collection_protocol import diagnostic
                    detail = diagnostic(error)
                raise APIError(message if isinstance(message, str) else "Server request failed",
                               code=code if isinstance(code, str) else "http_error", status=response.status,
                               retry_after=retry_after, diagnostic=detail)
            if expired.is_set():
                raise TimeoutError("operation deadline exceeded")
            return data
        except APIError:
            raise
        except (OSError, TimeoutError, http.client.HTTPException) as exc:
            if cancel_event is not None and cancel_event.is_set():
                raise APIError("Collection transfer interrupted", code="collection_cancelled") from None
            timed_out = expired.is_set() or isinstance(exc, TimeoutError) or time.perf_counter() >= deadline
            raise APIError("Request deadline exceeded" if timed_out else "Could not complete server request",
                           code="request_timeout" if timed_out else "transport_error") from None
        finally:
            finished.set()
            if timer is not None:
                timer.cancel()
            if response is not None:
                response.close()
            connection.close()

    @staticmethod
    def _set_budget(connection: _HTTPConnection, deadline: float) -> None:
        remaining = _remaining(deadline)
        if connection._wire_socket is not None and not connection._cancellable_read:
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
              headers: Mapping[str, str] | None = None, json_limit: int = _JSON_LIMIT) -> Any:
        deadline = self._deadline(timeout, self.request_timeout if default is None else default)
        outgoing = dict(headers or {})
        body = None
        if request is not None:
            body = json.dumps(dict(request), ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode("utf-8")
            if len(body) > 1 << 20:
                raise ValueError("JSON request exceeds 1 MiB")
            outgoing["Content-Type"] = "application/json"
        return self._exchange(method, path, deadline, body=body, headers=outgoing, json_limit=json_limit)

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

    def import_source(self, url: str, *, timeout: float | None = None, idempotency_key: str | None = None) -> Source:
        from ._source_protocol import key_headers
        return self._source(self._json("POST", "/api/v1/sources", {"url": url}, timeout=timeout,
                                      default=self.upload_timeout, headers=key_headers(idempotency_key)))

    def upload_source(self, path: str | os.PathLike[str], *, timeout: float | None = None,
                      idempotency_key: str | None = None, fingerprint: Mapping[str, Any] | None = None) -> Source:
        from ._source_protocol import key_headers
        headers = key_headers(idempotency_key, fingerprint, upload=True)
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
                                               headers={"Content-Type": "multipart/form-data; boundary=" + boundary, **headers},
                                               stream=(prefix, file, suffix, info.st_size)))

    def get_source_preparation(self, key: str, *, timeout: float | None = None) -> SourcePreparationReceipt:
        from ._source_protocol import key_headers, preparation
        key_headers(key)
        return cast(SourcePreparationReceipt, preparation(self._json("GET", "/api/v1/source-preparations/" + self._id(key), timeout=timeout), key))

    def prepare_source(self, source: SourcePreparationInput | Mapping[str, Any], directory: str | os.PathLike[str], *,
                       idempotency_key: str | None = None, timeout: float = 7200,
                       cancel_event: threading.Event | None = None,
                       on_progress: Callable[[Mapping[str, Any]], None] | None = None) -> SourcePreparationReport:
        from .sources import prepare_source
        return cast(SourcePreparationReport, prepare_source(self, source, directory, idempotency_key=idempotency_key, timeout=timeout,
                              cancel_event=cancel_event, on_progress=on_progress))

    def resume_source(self, directory: str | os.PathLike[str], *, timeout: float = 7200,
                      cancel_event: threading.Event | None = None,
                      on_progress: Callable[[Mapping[str, Any]], None] | None = None) -> SourcePreparationReport:
        from .sources import resume_source
        return cast(SourcePreparationReport, resume_source(self, directory, timeout=timeout, cancel_event=cancel_event, on_progress=on_progress))

    def retime_subtitles(self, request: SubtitleRequest | Mapping[str, Any], *, timeout: float | None = None) -> SubtitleResult:
        from .subtitles import subtitle_request, subtitle_result
        body = subtitle_request(request)
        response = self._exchange("POST", "/api/v1/subtitles/retime", self._deadline(timeout, self.request_timeout),
                                  body=json.dumps(body, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode(),
                                  headers={"Content-Type": "application/json"}, json_limit=64 << 20)
        return cast(SubtitleResult, subtitle_result(response, body))

    def get_project(self, project_id: str, *, timeout: float | None = None) -> AgentProject:
        from .projects import project_result
        result = project_result(self._exchange("GET", "/api/v1/projects/" + self._id(project_id), self._deadline(timeout, self.request_timeout), json_limit=16 << 20))
        if result["id"] != project_id:
            raise APIError("Project response identity differs", code="invalid_response")
        for item in result["jobs"]:
            self._job(item["job"])
        return cast(AgentProject, result)

    def create_project(self, request: ProjectCreateRequest | Mapping[str, Any], *, timeout: float | None = None) -> AgentProject:
        from .projects import project_result
        from .media import render_references
        render_references(request)
        return cast(AgentProject, project_result(self._json("POST", "/api/v1/projects", request, timeout=timeout, json_limit=16 << 20)))

    def edit_project(self, project_id: str, request: ProjectEditRequest | Mapping[str, Any], *, timeout: float | None = None) -> AgentProject:
        from .projects import project_result
        from .media import render_references
        render_references(request)
        return cast(AgentProject, project_result(self._json("PUT", "/api/v1/projects/" + self._id(project_id), request, timeout=timeout, json_limit=16 << 20)))

    def submit_project_collection(self, project_id: str, *, revision: int, clip_ids: list[str],
                                  retry_failed: bool = False, idempotency_key: str | None = None,
                                  timeout: float | None = None) -> CollectionSubmission:
        from ._collection_protocol import submission
        suffix = "/collections/retry" if retry_failed else "/collections"
        headers = {"Content-Type": "application/json"}
        if retry_failed:
            from ._source_protocol import key_headers
            if idempotency_key is None:
                raise ValueError("Explicit project retry requires a saved attempt key")
            headers.update(key_headers(idempotency_key))
        body = json.dumps({"revision": revision, "clip_ids": clip_ids}, separators=(",", ":")).encode()
        result = self._exchange("POST", "/api/v1/projects/" + self._id(project_id) + suffix,
                                self._deadline(timeout, self.request_timeout), body=body, headers=headers, collection_response=True)
        return cast(CollectionSubmission, submission(result, self._job))

    def create_project_handoff(self, project_id: str, *, timeout: float | None = None) -> ProjectHandoff:
        data = self._json("POST", "/api/v1/projects/" + self._id(project_id) + "/handoffs", {}, timeout=timeout)
        if (data.get("project_id") != project_id or not isinstance(data.get("token"), str)
                or not 20 <= len(data["token"]) <= 256 or not all(c.isascii() and (c.isalnum() or c in "-_") for c in data["token"])
                or not isinstance(data.get("expires_at"), str)):
            raise APIError("Invalid scoped handoff response", code="invalid_response")
        return cast(ProjectHandoff, {key: data[key] for key in ("project_id", "token", "expires_at")})

    def get_source(self, source_id: str, *, timeout: float | None = None) -> Source:
        return self._source(self._json("GET", "/api/v1/sources/" + self._id(source_id), timeout=timeout))

    def _analyze(self, kind: str, source_id: str, options: Mapping[str, Any] | None, timeout: float | None) -> dict[str, Any]:
        from .media import analysis_options, analysis_result
        body = analysis_options(kind, options)
        return analysis_result(kind, self._json("POST", "/api/v1/sources/" + self._id(source_id) + "/analysis/" + kind,
                                               body, timeout=timeout, default=120))

    def analyze_silence(self, source_id: str, options: SilenceOptions | None = None, *, timeout: float | None = None) -> SilenceResult:
        return cast(SilenceResult, self._analyze("silence", source_id, options, timeout))

    def analyze_video(self, source_id: str, options: VideoAnalysisOptions | None = None, *, timeout: float | None = None) -> VideoAnalysisResult:
        return cast(VideoAnalysisResult, self._analyze("video", source_id, options, timeout))

    def analyze_loudness(self, source_id: str, options: LoudnessOptions | None = None, *, timeout: float | None = None) -> LoudnessResult:
        return cast(LoudnessResult, self._analyze("loudness", source_id, options, timeout))

    def import_cut_list(self, request: CutListImportRequest | Mapping[str, Any], *, timeout: float | None = None) -> CutList:
        from .media import cut_import, cut_list
        return cast(CutList, cut_list(self._json("POST", "/api/v1/cut-lists/import", cut_import(request), timeout=timeout)))

    def export_cut_list(self, request: CutListExportRequest | Mapping[str, Any], *, timeout: float | None = None) -> CutListExportResult:
        from .media import cut_export, cut_export_result
        body = cut_export(request)
        return cast(CutListExportResult, cut_export_result(self._json("POST", "/api/v1/cut-lists/export", body, timeout=timeout), body["format"]))

    def get_tracks(self, source_id: str, *, timeout: float | None = None) -> TrackReport:
        data = self._json("GET", "/api/v1/sources/" + self._id(source_id) + "/tracks", timeout=timeout)
        if not isinstance(data.get("tracks"), list) or any(not isinstance(track, dict) for track in data["tracks"]):
            raise APIError("Server returned an invalid track report", code="invalid_response")
        return cast(TrackReport, data)

    def preflight(self, request: ExportRequest | Mapping[str, Any], *, timeout: float | None = None) -> PreflightResult:
        from .media import render_references
        render_references(request)
        data = self._json("POST", "/api/v1/jobs/preflight", request, timeout=timeout)
        if (data.get("valid") is not True or data.get("version") != 1 or isinstance(data.get("version"), bool)
                or not isinstance(data.get("checks"), list) or not isinstance(data.get("warnings"), list)):
            raise APIError("Server returned an invalid preflight report", code="invalid_response")
        return cast(PreflightResult, data)

    def create_job(self, request: ExportRequest | Mapping[str, Any], *, idempotency_key: str,
                   timeout: float | None = None) -> Job:
        from .media import render_references
        render_references(request)
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

    def validate_collection(self, request: CollectionRequest | Mapping[str, Any], *, timeout: float | None = None) -> CollectionValidation:
        from ._collection_protocol import request_copy
        data = self._json("POST", "/api/v1/collections/validate", request_copy(request), timeout=timeout)
        if (data.get("version") != 1 or isinstance(data.get("version"), bool) or data.get("valid") is not True
                or not isinstance(data.get("clips"), list) or not data["clips"]
                or any(not isinstance(item, dict) or not isinstance(item.get("clip_id"), str)
                       or not isinstance(item.get("preflight"), dict) for item in data["clips"])):
            raise APIError("Invalid collection validation response", code="invalid_response")
        return cast(CollectionValidation, data)

    def submit_collection(self, request: CollectionRequest | Mapping[str, Any], *, idempotency_key: str,
                          timeout: float | None = None) -> CollectionSubmission:
        from ._collection_protocol import request_copy, submission
        if (not isinstance(idempotency_key, str) or not idempotency_key.strip()
                or len(idempotency_key.encode()) > 128 or any(ord(c) < 32 or ord(c) == 127 for c in idempotency_key)):
            raise ValueError("idempotency_key must contain1..128 UTF-8 bytes without controls")
        request = request_copy(request)
        body = json.dumps(request, ensure_ascii=False, allow_nan=False, separators=(",", ":")).encode()
        try:
            data = self._exchange("POST", "/api/v1/collections", self._deadline(timeout, self.request_timeout),
                                  body=body, headers={"Content-Type": "application/json", "Idempotency-Key": idempotency_key.encode().decode("latin-1")},
                                  collection_response=True)
            return cast(CollectionSubmission, submission(data, self._job))
        except APIError as error:
            error.idempotency_key = idempotency_key
            raise

    def run_collection(self, request: CollectionRequest | Mapping[str, Any], directory: str | os.PathLike[str], *,
                       idempotency_key: str | None = None, timeout: float = 1800, poll_interval: float = 1,
                       cancel_event: threading.Event | None = None,
                       on_progress: Callable[[CollectionRunReport], None] | None = None,
                       max_output_bytes: int = 10 << 30, admission_batch_size: int = 3) -> CollectionRunReport:
        """Run a durable owner-scoped cycle; interruption preserves project state."""
        from .collections import run_collection
        return cast(CollectionRunReport, run_collection(self, request, directory, idempotency_key=idempotency_key,
                    timeout=timeout, poll_interval=poll_interval, cancel_event=cancel_event, on_progress=on_progress,
                    max_output_bytes=max_output_bytes, admission_batch_size=admission_batch_size))

    def resume_collection(self, directory: str | os.PathLike[str], *,
                          request: CollectionRequest | Mapping[str, Any] | None = None,
                          retry_failed: list[str] | tuple[str, ...] | None = None,
                          project_id: str | None = None,
                          timeout: float = 1800, poll_interval: float = 1,
                          cancel_event: threading.Event | None = None,
                          on_progress: Callable[[CollectionRunReport], None] | None = None,
                          max_output_bytes: int = 10 << 30, admission_batch_size: int = 3) -> CollectionRunReport:
        """Restore saved owner/key and verify files before continuing or revising."""
        from .collections import resume_collection
        return cast(CollectionRunReport, resume_collection(self, directory, request=request, retry_failed=retry_failed, project_id=project_id, timeout=timeout,
                    poll_interval=poll_interval, cancel_event=cancel_event, on_progress=on_progress,
                    max_output_bytes=max_output_bytes, admission_batch_size=admission_batch_size))

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
            if time.perf_counter() >= deadline:
                raise WaitTimeout(job_id, last)
            try:
                last = self.get_job(job_id, timeout=min(self.request_timeout, _remaining(deadline)))
            except (APIError, TimeoutError) as exc:
                if time.perf_counter() >= deadline:
                    raise WaitTimeout(job_id, last) from None
                raise
            if on_progress is not None:
                on_progress(last)
            status = last.get("status")
            if status == "succeeded":
                return last
            if status in ("failed", "cancelled"):
                raise JobError(last)
            duration = min(poll_interval, max(0, deadline - time.perf_counter()))
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
