import concurrent.futures
import json
import os
import pathlib
import tempfile
import threading
import time
import tracemalloc
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from cutmyvideo import APIError, CutmyClient, JobError, WaitCancelled, WaitTimeout, ready_artifacts

TOKEN = "private-fixture-capability"
REQUEST = {"source_id": "source", "ranges": [{"start_ms": 0, "end_ms": 1000}],
           "format": "mp4", "quality": "best", "cut_mode": "accurate"}
ARTIFACT = {"id": "artifact", "filename": "clip.mp4", "size_bytes": 5,
            "download_url": "https://untrusted.invalid/cookie", "actual_start_ms": 0,
            "actual_end_ms": 1000, "expires_at": None}


def job(status="succeeded"):
    return {"id": "job", "status": status, "stage": status, "message": "", "storage_wait_until": None,
            "items": [{"id": "item", "label": "clip", "start_ms": 0, "end_ms": 1000,
                       "status": "succeeded", "artifact": dict(ARTIFACT)},
                      {"id": "later", "label": "later", "start_ms": 1000, "end_ms": 2000,
                       "status": "running", "artifact": dict(ARTIFACT)}], "new_field": {"accepted": True}}


class Fixture:
    def __init__(self):
        self.requests = []
        self.mode = "normal"
        self.job_status = "succeeded"
        self.redirect = "https://untrusted.invalid/cookie"
        self.upload_size = 0
        self.upload_prefix = b""
        self.lock = threading.Lock()
        fixture = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def log_message(self, *_):
                pass

            def reply(self, data, status=200, headers=None):
                payload = data if isinstance(data, bytes) else json.dumps(data).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(payload)))
                for key, value in (headers or {}).items():
                    self.send_header(key, value)
                self.end_headers()
                self.wfile.write(payload)

            def do_GET(self):
                self.handle_request()

            def do_POST(self):
                self.handle_request()

            def handle_request(self):
                with fixture.lock:
                    fixture.requests.append((self.command, self.path, self.headers.get("Cookie"), self.headers.get("Idempotency-Key")))
                if self.path == "/prefix/api/v1/session":
                    self.reply({"ok": True, "limits": {"max_ranges": 128}}, headers={"Set-Cookie": "cutmy_session=" + TOKEN + "; HttpOnly; Path=/"})
                    return
                if self.headers.get("Cookie") != "cutmy_session=" + TOKEN:
                    self.reply({"error": {"code": "unauthorized", "message": "Wrong owner"}}, 401)
                    return
                length = int(self.headers.get("Content-Length", 0))
                if self.path.endswith("/uploads"):
                    remaining = length
                    while remaining:
                        chunk = self.rfile.read(min(64 << 10, remaining))
                        if not chunk:
                            break
                        if not fixture.upload_prefix:
                            fixture.upload_prefix = chunk[:500]
                        fixture.upload_size += len(chunk)
                        remaining -= len(chunk)
                    self.reply({"id": "source", "duration_ms": 2000})
                    return
                body = self.rfile.read(length) if length else None
                mode = fixture.mode
                if mode == "redirect":
                    self.reply(b"", 302, {"Location": fixture.redirect})
                elif mode == "html":
                    self.reply(b"<html>unavailable</html>", 503)
                elif mode == "future-error":
                    self.reply({"error": {"code": "future_additive_code", "message": "Future diagnostic"}}, 503,
                               {"Retry-After": "3"})
                elif mode == "large":
                    self.reply(b'{"large":"' + b"x" * (1 << 20) + b'"}')
                elif mode in ("slow", "short"):
                    self.send_response(200)
                    self.send_header("Content-Length", "100")
                    self.send_header("Connection", "close")
                    self.close_connection = True
                    self.end_headers()
                    if mode == "short":
                        self.wfile.write(b"abc")
                        self.close_connection = True
                    else:
                        try:
                            for _ in range(100):
                                self.wfile.write(b"a")
                                self.wfile.flush()
                                time.sleep(0.03)
                        except OSError:
                            pass
                elif self.path.endswith("/download"):
                    self.reply(b"media")
                elif self.path.endswith("/preflight"):
                    self.reply({"version": 1, "valid": True, "request": json.loads(body), "checks": [{"name": "future", "status": "future_status"}],
                                "warnings": [{"code": "future_warning", "message": "Deferred"}], "new_field": 42})
                elif self.path.endswith("/tracks"):
                    self.reply({"tracks": [{"index": 1, "type": "audio", "codec": "aac", "default": True}]})
                elif self.path.endswith("/sources") and self.command == "GET":
                    self.reply({"sources": []})
                elif "/sources" in self.path:
                    self.reply({"id": "source", "duration_ms": 2000, "new_field": 42})
                else:
                    self.reply(job(fixture.job_status), status=202 if self.path.endswith("/jobs") else 200)

        class QuietServer(ThreadingHTTPServer):
            def handle_error(self, *_):
                pass

        self.server = QuietServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}/prefix"

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


class ClientTests(unittest.TestCase):
    def setUp(self):
        self.fixture = Fixture()
        self.client = CutmyClient(self.fixture.url)
        self.addCleanup(self.fixture.close)

    def test_workflow_unknown_fields_and_owner_session(self):
        self.assertEqual(self.client.session()["limits"]["max_ranges"], 128)
        self.assertEqual(self.client.import_source("https://media.invalid/movie")["id"], "source")
        self.assertEqual(self.client.get_source("source")["new_field"], 42)
        self.assertEqual(self.client.get_tracks("source")["tracks"][0]["index"], 1)
        self.assertEqual(self.client.list_sources(), [])
        report = self.client.preflight(REQUEST)
        self.assertEqual(report["checks"][0]["status"], "future_status")
        created = self.client.create_job(REQUEST, idempotency_key="work-1")
        self.assertEqual(created["new_field"], {"accepted": True})
        self.assertEqual(self.client.wait_for_job(created["id"])["status"], "succeeded")
        self.assertEqual(len(ready_artifacts(created)), 1)
        self.assertEqual(len([r for r in self.fixture.requests if r[1].endswith("/session")]), 1)
        self.assertNotIn(TOKEN, repr(self.client))
        self.assertNotIn(TOKEN, str(self.client.__dict__.keys()))

    def test_concurrent_session_initializes_once_and_resume_explicitly(self):
        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as pool:
            replies = list(pool.map(lambda _: self.client.get_job("job"), range(4)))
        self.assertTrue(all(j["id"] == "job" for j in replies))
        self.assertEqual(len([r for r in self.fixture.requests if r[1].endswith("/session")]), 1)
        self.assertEqual(self.client.export_session_cookie(), TOKEN)
        resumed = CutmyClient(self.fixture.url, session_cookie=self.client.export_session_cookie())
        self.assertEqual(resumed.get_job("job")["id"], "job")
        self.assertNotIn(TOKEN, repr(resumed))
        resumed.close()
        with self.assertRaises(APIError) as closed:
            resumed.export_session_cookie()
        self.assertEqual(closed.exception.code, "session_unavailable")

    def test_job_submission_error_retains_key_without_retry(self):
        self.fixture.mode = "future-error"
        with self.assertRaises(APIError) as failed:
            self.client.create_job(REQUEST, idempotency_key="repeat-me")
        error = failed.exception
        self.assertEqual((error.code, error.status, error.idempotency_key, error.retry_after),
                         ("future_additive_code", 503, "repeat-me", 3))
        self.assertEqual(len([r for r in self.fixture.requests if r[1].endswith("/jobs")]), 1)
        self.fixture.mode = "slow"
        started = time.monotonic()
        with self.assertRaises(APIError) as timed_out:
            self.client.create_job(REQUEST, idempotency_key="recover-timeout", timeout=0.1)
        self.assertEqual(timed_out.exception.idempotency_key, "recover-timeout")
        self.assertLess(time.monotonic() - started, 0.8)

    def test_wait_failed_partial_artifacts_timeout_and_cancellation(self):
        self.fixture.job_status = "failed"
        with self.assertRaises(JobError) as failed:
            self.client.wait_for_job("job")
        self.assertEqual(failed.exception.job_id, "job")
        self.assertEqual(len(failed.exception.artifacts), 1)
        self.fixture.job_status = "future_pending_status"
        with self.assertRaises(WaitTimeout) as timed_out:
            self.client.wait_for_job("job", timeout=0.1)
        self.assertEqual(timed_out.exception.job_id, "job")
        self.assertEqual(timed_out.exception.job["status"], "future_pending_status")
        cancel = threading.Event()
        cancel.set()
        with self.assertRaises(WaitCancelled):
            self.client.wait_for_job("job", cancel_event=cancel)
        self.assertFalse(any(r[1].endswith("/cancel") for r in self.fixture.requests))
        self.client.cancel_job("job")
        self.assertTrue(any(r[1].endswith("/cancel") for r in self.fixture.requests))

    def test_download_uses_artifact_id_exclusive_publication_and_limits(self):
        with tempfile.TemporaryDirectory() as directory:
            target = pathlib.Path(directory) / "output.mp4"
            self.assertEqual(self.client.download(ARTIFACT, target), target)
            self.assertEqual(target.read_bytes(), b"media")
            with self.assertRaises(FileExistsError):
                self.client.download("artifact", target)
            self.assertEqual(target.read_bytes(), b"media")
            target.unlink()
            with self.assertRaises(APIError) as limited:
                self.client.download("artifact", target, max_bytes=4)
            self.assertEqual(limited.exception.code, "download_limit")
            self.assertFalse(target.exists())
            self.assertEqual(list(pathlib.Path(directory).iterdir()), [])
            self.fixture.mode = "short"
            with self.assertRaises(APIError):
                self.client.download("artifact", target)
            self.assertFalse(target.exists())
            self.fixture.mode = "slow"
            with self.assertRaises(APIError):
                self.client.download("artifact", target, timeout=0.1)
            self.assertEqual(list(pathlib.Path(directory).iterdir()), [])
            if os.name == "posix":
                target.symlink_to(pathlib.Path(directory) / "missing")
                with self.assertRaises(FileExistsError):
                    self.client.download("artifact", target)
                self.assertTrue(target.is_symlink())

    def test_redirects_never_forward_cookie_or_fetch_artifact_urls(self):
        other = Fixture()
        self.addCleanup(other.close)
        self.fixture.redirect = other.url + "/api/v1/jobs"
        self.fixture.mode = "redirect"
        with self.assertRaises(APIError) as refused:
            self.client.get_job("job")
        self.assertEqual(refused.exception.code, "redirect_refused")
        self.assertEqual(other.requests, [])
        self.fixture.mode = "normal"
        with tempfile.TemporaryDirectory() as directory:
            malicious = {**ARTIFACT, "download_url": other.url + "/api/v1/jobs"}
            self.client.download(malicious, pathlib.Path(directory) / "output")
        self.assertEqual(other.requests, [])

    def test_large_upload_streaming_and_safe_filename(self):
        with tempfile.TemporaryDirectory() as directory:
            path = pathlib.Path(directory) / "movie ü.mp4"
            with path.open("wb") as file:
                file.truncate(12 << 20)
            tracemalloc.start()
            try:
                self.assertEqual(self.client.upload_source(path)["id"], "source")
                peak = tracemalloc.get_traced_memory()[1]
            finally:
                tracemalloc.stop()
            self.assertGreater(self.fixture.upload_size, 12 << 20)
            self.assertLess(peak, 3 << 20)
            self.assertIn("movie ü.mp4".encode(), self.fixture.upload_prefix)
            self.assertNotIn(str(path.parent).encode(), self.fixture.upload_prefix)

    def test_response_bounds_invalid_json_and_input_validation(self):
        for mode, code in (("html", "invalid_response"), ("large", "response_too_large")):
            self.fixture.mode = mode
            with self.assertRaises(APIError) as failed:
                self.client.get_job("job")
            self.assertEqual(failed.exception.code, code)
        for key in ("", "x" * 129, "bad\r\nCookie: value", " "):
            with self.assertRaises(ValueError):
                self.client.create_job(REQUEST, idempotency_key=key)
        for url in ("file:///tmp", "https://user:password@server", "https://server?secret=value"):
            with self.assertRaises(ValueError):
                CutmyClient(url)
        self.assertIn("uninitialized", repr(CutmyClient("http://selfhosted-server")))
        for timeout in (0, float("nan"), float("inf")):
            with self.assertRaises(ValueError):
                self.client.get_job("job", timeout=timeout)
        with self.assertRaises(ValueError):
            self.client.wait_for_job("job", poll_interval=0.1)
        with self.assertRaises(ValueError):
            self.client.preflight({"large": "x" * (1 << 20)})


if __name__ == "__main__":
    unittest.main()
