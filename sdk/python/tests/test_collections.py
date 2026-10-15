import hashlib
import copy
import json
import os
from pathlib import Path
import socket
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from cutmyvideo import APIError, CollectionError, CutmyClient
from cutmyvideo._collection_state import Project

TOKEN = "private-durable-owner"


def request(count=15):
    return {"source_id": "source", "plan": {"version": 1, "clips": [
        {"id": "clip" + str(i), "ranges": [{"start_ms": i * 1000, "end_ms": i * 1000 + 500}]} for i in range(count)]}}


class Server:
    def __init__(self):
        self.jobs, self.files, self.posts, self.ranges, self.requests = {}, {}, [], [], []
        self.post_times = []
        self.lose_submit, self.break_download, self.malformed_range, self.ignore_range = False, False, False, False
        self.partial, self.terminal = False, False
        self.invalid_validation = False
        self.compressed_download = False
        self.encodings = []
        self.job_clips, self.poll_times = {}, {}
        self.queued_once = set()
        self.poll_failure_status = None
        self.poll_retry_after = 2
        self.submission_status = None
        self.partial_first, self.admission_error_first = False, None
        self.stall_job = False
        self.job_fragment_delay = 0
        self.stall_session = False
        self.poll_failed = threading.Event()
        self.stalled, self.release_stall = threading.Event(), threading.Event()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *_): pass
            def reply(self, value, status=200, headers=None):
                data = value if isinstance(value, bytes) else json.dumps(value).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(data)))
                for key, val in (headers or {}).items(): self.send_header(key, val)
                self.end_headers()
                self.wfile.write(data)
            def do_GET(self): self.handle_request()
            def do_POST(self): self.handle_request()
            def handle_request(self):
                outer.requests.append((self.command, self.path, self.headers.get("Cookie")))
                if self.path == "/api/v1/session":
                    if outer.stall_session:
                        self.send_response(200)
                        self.send_header("Content-Length", "10000")
                        self.end_headers()
                        self.wfile.write(b"{"); self.wfile.flush()
                        outer.stalled.set()
                        outer.release_stall.wait(5)
                        self.close_connection = True
                        return
                    return self.reply({"ok": True, "limits": {}}, headers={"Set-Cookie": "cutmy_session=" + TOKEN})
                if self.headers.get("Cookie") != "cutmy_session=" + TOKEN:
                    return self.reply({"error": {"code": "unauthorized"}}, 401)
                if self.path == "/api/v1/sources/source":
                    return self.reply({"id": "source", "duration_ms": 120000})
                if self.path == "/api/v1/collections/validate":
                    body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    if outer.invalid_validation:
                        return self.reply({"error": {"code": "invalid_range", "message": "End must follow start", "clip_id": "clip0",
                                                     "field": "clips[0].ranges[0].end_ms", "secret": TOKEN}, "unknown": TOKEN}, 400)
                    return self.reply({"version": 1, "valid": True, "rendition": body.get("rendition", "final"), "clips": [{"clip_id": c["id"], "preflight": {}} for c in body["plan"]["clips"]]})
                if self.path == "/api/v1/collections":
                    body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                    key = self.headers.get("Idempotency-Key")
                    outer.posts.append((key, body))
                    outer.post_times.append(time.monotonic())
                    if outer.admission_error_first and len(outer.posts) == 1:
                        return self.reply({"error": {"code": "admission_wait", "message": "Wait before another submission"}}, outer.admission_error_first,
                                          {"Retry-After": "2"})
                    rows = []
                    for clip in body["plan"]["clips"]:
                        if body.get("clip_ids") and clip["id"] not in body["clip_ids"]: continue
                        fingerprint = hashlib.sha256(json.dumps([key, body["source_id"], body.get("rendition", "final"), body["plan"].get("options", {}), clip], sort_keys=True).encode()).hexdigest()[:20]
                        jid, aid = "job_" + fingerprint, "art_" + fingerprint
                        if outer.partial or (outer.partial_first and len(outer.posts) == 1 and clip["id"] == body["clip_ids"][-1]):
                            rows.append({"clip_id": clip["id"], "retryable": True, "error": {"code": "rate_limit"}})
                            continue
                        content = (fingerprint.encode() * 10000)[:180000]
                        outer.files[aid] = content
                        status = "failed" if outer.terminal else "succeeded"
                        artifact = {"id": aid, "filename": "../../ignored", "size_bytes": len(content), "download_url": "https://untrusted.invalid/secret", "segments": [], "track_manifest": []}
                        job = {"id": jid, "status": status, "stage": status, "message": "", "items": [{"status": status, "artifact": artifact if status == "succeeded" else None}]}
                        outer.jobs[jid] = job
                        outer.job_clips[jid] = clip["id"]
                        rows.append({"clip_id": clip["id"], "retryable": False, "job": job, "request": {"format": "mp4"}})
                    if outer.lose_submit:
                        outer.lose_submit = False
                        self.close_connection = True
                        self.connection.shutdown(socket.SHUT_RDWR)
                        return
                    headers = {"Retry-After": "2"} if outer.partial_first and len(outer.posts) == 1 else ({"Retry-After": "1"} if outer.partial else None)
                    return self.reply({"version": 1, "complete": not any("error" in row for row in rows), "rendition": body.get("rendition", "final"), "clips": rows}, outer.submission_status or (429 if outer.partial else 202), headers)
                if self.path.startswith("/api/v1/jobs/"):
                    jid = self.path.rsplit("/", 1)[1]
                    polls = outer.poll_times.setdefault(jid, [])
                    polls.append(time.monotonic())
                    if outer.stall_job:
                        self.send_response(200)
                        self.send_header("Content-Length", "10000")
                        self.end_headers()
                        self.wfile.write(b"{"); self.wfile.flush()
                        outer.stalled.set()
                        outer.release_stall.wait(5)
                        self.close_connection = True
                        return
                    if outer.poll_failure_status and len(polls) == 1:
                        self.reply({"error": {"code": "poll_wait", "message": "Wait before polling"}}, outer.poll_failure_status,
                                   {"Retry-After": str(outer.poll_retry_after)})
                        outer.poll_failed.set()
                        return
                    if outer.job_clips[jid] in outer.queued_once and len(polls) == 1:
                        job = copy.deepcopy(outer.jobs[jid])
                        job["status"] = "running"
                        job["items"][0].update(status="running", artifact=None)
                        return self.reply(job)
                    if outer.job_fragment_delay:
                        data = json.dumps(outer.jobs[jid]).encode()
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(data)))
                        self.send_header("Connection", "close")
                        self.end_headers()
                        for index, chunk in enumerate((data[:1], data[1:len(data)//2], data[len(data)//2:])):
                            if index:
                                time.sleep(outer.job_fragment_delay)
                            self.wfile.write(chunk); self.wfile.flush()
                        self.close_connection = True
                        return
                    return self.reply(outer.jobs[jid])
                if self.path.startswith("/api/v1/artifacts/"):
                    outer.encodings.append(self.headers.get("Accept-Encoding"))
                    aid = self.path.split("/")[-2]
                    content = outer.files[aid]
                    headers = {"Last-Modified": "Thu, 15 Oct 2026 00:00:00 GMT"}
                    if outer.compressed_download:
                        headers["Content-Encoding"] = "gzip"
                        return self.reply(content, headers=headers)
                    if outer.break_download:
                        outer.break_download = False
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(content)))
                        self.send_header("Last-Modified", headers["Last-Modified"])
                        self.send_header("Connection", "close")
                        self.end_headers()
                        self.wfile.write(content[:len(content) // 2])
                        self.wfile.flush()
                        self.close_connection = True
                        return
                    offset = int(self.headers.get("Range", "bytes=0-")[6:-1])
                    outer.ranges.append((offset, self.headers.get("If-Range")))
                    if offset and not outer.ignore_range:
                        headers["Content-Range"] = f"bytes {0 if outer.malformed_range else offset}-{len(content)-1}/{len(content)}"
                        return self.reply(content[offset:], 206, headers)
                    return self.reply(content, headers=headers)
                self.reply({"error": {"code": "not_found"}}, 404)

        class QuietServer(ThreadingHTTPServer):
            def handle_error(self, *_): pass
        self.server = QuietServer(("127.0.0.1", 0), Handler)
        self.server.daemon_threads = True
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.url = f"http://127.0.0.1:{self.server.server_port}"
    def close(self):
        self.release_stall.set()
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()


class CollectionTests(unittest.TestCase):
    def setUp(self):
        self.server = Server()
        self.addCleanup(self.server.close)
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.project = Path(self.directory.name) / "project"
        self.client = CutmyClient(self.server.url)

    def crash_process(self, patch, body=None):
        code = """
import json, os, sys
from cutmyvideo import CutmyClient
from cutmyvideo._collection_state import Project
""" + patch + """
CutmyClient(sys.argv[1]).run_collection(json.loads(sys.argv[3]), sys.argv[2], timeout=30)
"""
        result = subprocess.run([sys.executable, "-c", code, self.server.url, str(self.project), json.dumps(request(1) if body is None else body)],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=35)
        self.assertEqual(result.returncode, 99, result.stderr.decode(errors="replace"))
        self.assertEqual(result.stdout, b"")

    def test_initialization_crash_preserves_original_owner_and_key(self):
        self.crash_process("""
original = Project.write
def write(self, name, data, **options):
    original(self, name, data, **options)
    if name == "owner.json":
        os._exit(99)
Project.write = write
""")
        self.assertTrue((self.project / "owner.json").exists())
        self.assertFalse((self.project / "state.json").exists())
        self.assertEqual(self.server.posts, [])
        journal = json.loads((self.project / ".init.json").read_text())
        before = len(self.server.requests)
        with self.assertRaises(CollectionError):
            CutmyClient("http://127.0.0.1:1").resume_collection(self.project, timeout=1)
        self.assertEqual(len(self.server.requests), before)
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertFalse((self.project / ".init.json").exists())
        self.assertEqual(self.server.posts[0][0], journal["state"]["idempotency_key"])
        self.assertEqual(len(self.server.jobs), 1)
        # State deletion after jobs exist must never mint a new project/key.
        (self.project / "state.json").unlink()
        before = len(self.server.requests)
        with self.assertRaises(CollectionError):
            CutmyClient(self.server.url).resume_collection(self.project)
        with self.assertRaises(CollectionError):
            CutmyClient(self.server.url).run_collection(request(1), self.project)
        self.assertEqual(len(self.server.requests), before)
        self.assertEqual(len(self.server.jobs), 1)

    def test_initialization_conflicts_validate_both_primaries_before_repair(self):
        for conflict in ("owner", "state"):
            with self.subTest(conflict=conflict):
                self.project = Path(self.directory.name) / ("conflict-" + conflict)
                self.crash_process("""
original = Project.write
def write(self, name, data, **options):
    original(self, name, data, **options)
    if name == "owner.json":
        os._exit(99)
Project.write = write
""")
                journal = json.loads((self.project / ".init.json").read_text())
                with Project(self.project) as project:
                    if conflict == "owner":
                        owner = dict(journal["owner"], session_cookie="another-owner")
                        project.write("owner.json", owner)
                    else:
                        state = dict(journal["state"], idempotency_key="different-later-key")
                        project.write("state.json", state)
                        (self.project / "owner.json").unlink()
                before = len(self.server.requests)
                with self.assertRaises(CollectionError):
                    CutmyClient(self.server.url).resume_collection(self.project)
                self.assertFalse((self.project / ("state.json" if conflict == "owner" else "owner.json")).exists())
                self.assertTrue((self.project / ".init.json").exists())
                self.assertEqual(len(self.server.requests), before)
                self.assertEqual(self.server.posts, [])
    def test_publication_crash_adopts_only_prepared_matching_output(self):
        self.crash_process("""
original = Project.publish
def publish(self, *args, **options):
    original(self, *args, **options)
    os._exit(99)
Project.publish = publish
""")
        published = next((self.project / "files").iterdir())
        earlier = published.read_bytes()
        self.assertEqual(published.stat().st_nlink, 2)
        state = json.loads((self.project / "state.json").read_text())
        self.assertTrue(next(iter(state["parts"].values()))["ready"])
        self.assertFalse(state["receipts"])
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertEqual(Path(result["outputs"][0]["path"]).read_bytes(), earlier)
        self.assertEqual(len(self.server.jobs), 1)

    def test_fifteen_resume_after_eight_same_owner_jobs_and_hashes(self):
        def stop(report):
            self.assertNotIn(TOKEN, json.dumps(report))
            if report["phase"] == "output_ready" and len(report["outputs"]) == 8:
                raise RuntimeError("simulate stopped caller")
        with self.assertRaises(CollectionError) as paused:
            self.client.run_collection(request(), self.project, on_progress=stop, timeout=60)
        self.assertEqual(len(paused.exception.report["outputs"]), 8)
        earlier = {r["artifact_id"]: (Path(r["path"]).read_bytes(), r["sha256"]) for r in paused.exception.report["outputs"]}
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=60)
        self.assertTrue(result["complete"])
        self.assertEqual(len(result["outputs"]), 15)
        self.assertEqual(len(self.server.jobs), 15)
        self.assertTrue(all(len(body["clip_ids"]) <= 3 for _, body in self.server.posts))
        for output in result["outputs"]:
            if output["artifact_id"] in earlier:
                self.assertEqual((Path(output["path"]).read_bytes(), output["sha256"]), earlier[output["artifact_id"]])
        if os.name == "posix":
            self.assertEqual((self.project / "owner.json").stat().st_mode & 0o777, 0o600)
        self.assertNotIn(TOKEN, (self.project / "state.json").read_text())
        revised = request()
        revised["plan"]["clips"][0]["ranges"][0]["end_ms"] += 100
        revised["plan"]["clips"][1]["ranges"][0]["end_ms"] += 100
        revised["clip_ids"] = ["clip0", "clip1"]
        changed = CutmyClient(self.server.url).resume_collection(self.project, request=revised, timeout=30)
        self.assertEqual(len(self.server.jobs), 17)
        self.assertEqual(len(changed["outputs"]), 2)
        revised["rendition"] = "draft"
        drafted = CutmyClient(self.server.url).resume_collection(self.project, request=revised, timeout=30)
        self.assertEqual(len(self.server.jobs), 19)
        self.assertTrue(all(".draft." in item["path"] for item in drafted["outputs"]))

    def test_plan_order_survives_state_reordering_and_cosmetic_revision(self):
        for index, ids in enumerate((["2", "1"], ["zeta", "alpha"])):
            with self.subTest(ids=ids):
                path = self.project.with_name("ordered-" + str(index))
                body = request(2)
                for clip, cid in zip(body["plan"]["clips"], ids):
                    clip["id"] = cid
                def stop(report):
                    self.assertEqual([item["clip_id"] for item in report["outputs"] + report["pending"]], ids)
                    if report["phase"] == "output_ready" and len(report["outputs"]) == 1:
                        raise RuntimeError("stop at durable output")
                with self.assertRaises(CollectionError):
                    CutmyClient(self.server.url).run_collection(body, path, timeout=30, on_progress=stop)
                state = json.loads((path / "state.json").read_text())
                job_ids = {cid: row["job_id"] for cid, row in state["clips"].items()}
                state["clips"] = dict(reversed(list(state["clips"].items())))
                with Project(path) as project:
                    project.write("state.json", state)
                result = CutmyClient(self.server.url).resume_collection(path, timeout=30)
                self.assertEqual([item["clip_id"] for item in result["outputs"]], ids)
                self.assertEqual({item["clip_id"]: item["job_id"] for item in result["outputs"]}, job_ids)
                jobs = len(self.server.jobs)
                body["plan"]["clips"].reverse()
                revised = CutmyClient(self.server.url).resume_collection(path, request=body, timeout=30)
                self.assertEqual([item["clip_id"] for item in revised["outputs"]], list(reversed(ids)))
                self.assertEqual({item["clip_id"]: item["job_id"] for item in revised["outputs"]}, job_ids)
                self.assertEqual(len(self.server.jobs), jobs)

    def test_lost_submission_reply_replays_persisted_key_and_body(self):
        self.server.lose_submit = True
        result = self.client.run_collection(request(2), self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertEqual(self.server.posts[0], self.server.posts[1])
        self.assertEqual(len(self.server.jobs), 2)

    def test_partial_202_retry_after_blocks_all_new_ids_after_process_crash(self):
        self.server.partial_first = True
        self.crash_process("""
from cutmyvideo.collections import _Cycle
original = _Cycle.checkpoint
def checkpoint(self, phase, **options):
    original(self, phase, **options)
    if phase == "output_ready" and len(self.report()["outputs"]) == 2:
        os._exit(99)
_Cycle.checkpoint = checkpoint
""", request(7))
        self.assertEqual(len(self.server.posts), 1)
        state = json.loads((self.project / "state.json").read_text())
        self.assertGreater(state["admission_retry_at"], time.time())
        self.assertEqual(len(state["receipts"]), 2)
        self.assertFalse(state.get("uncertain"))
        earlier = {aid: (receipt["job_id"], receipt["sha256"]) for aid, receipt in state["receipts"].items()}
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=15)
        self.assertTrue(result["complete"])
        self.assertEqual(len(result["outputs"]), 7)
        self.assertEqual(len(self.server.jobs), 7)
        self.assertGreaterEqual(self.server.post_times[1] - self.server.post_times[0], 1.95)
        self.assertEqual([body["clip_ids"] for _, body in self.server.posts],
                         [["clip0", "clip1", "clip2"], ["clip2", "clip3", "clip4"], ["clip5", "clip6"]])
        for output in result["outputs"]:
            if output["artifact_id"] in earlier:
                self.assertEqual((output["job_id"], output["sha256"]), earlier[output["artifact_id"]])

    def test_non2xx_admission_retry_after_keeps_uncertain_body_and_global_delay(self):
        self.server.admission_error_first = 429
        self.crash_process("""
from cutmyvideo.collections import _Cycle
original = _Cycle.checkpoint
def checkpoint(self, phase, **options):
    original(self, phase, **options)
    if phase == "submission_uncertain":
        os._exit(99)
_Cycle.checkpoint = checkpoint
""", request(5))
        state = json.loads((self.project / "state.json").read_text())
        self.assertGreater(state["admission_retry_at"], time.time())
        self.assertEqual(state["uncertain"], self.server.posts[0][1])
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=15)
        self.assertTrue(result["complete"])
        self.assertEqual(len(self.server.jobs), 5)
        self.assertEqual(self.server.posts[0], self.server.posts[1])
        self.assertGreaterEqual(self.server.post_times[1] - self.server.post_times[0], 1.95)

    def test_polling_interval_survives_incremental_admission(self):
        self.server.queued_once = {"clip1", "clip2"}
        result = self.client.run_collection(request(4), self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertEqual(len(self.server.posts), 2)
        for jid, times in self.server.poll_times.items():
            if self.server.job_clips[jid] in self.server.queued_once:
                self.assertGreaterEqual(len(times), 2)
                self.assertGreaterEqual(times[1] - times[0], .95)

    def test_poll_retry_after_429_and_503_is_checkpointed(self):
        for status in (429, 503):
            with self.subTest(status=status):
                self.server.poll_failure_status = status
                self.server.poll_failed.clear()
                path = self.project.with_name("retry-" + str(status))
                event = threading.Event()
                def cancel_after_wait():
                    if self.server.poll_failed.wait(5):
                        time.sleep(.1)
                        event.set()
                thread = threading.Thread(target=cancel_after_wait)
                thread.start()
                try:
                    with self.assertRaises(CollectionError) as stopped:
                        CutmyClient(self.server.url).run_collection(request(1), path, timeout=15, cancel_event=event)
                    self.assertEqual(stopped.exception.code, "collection_cancelled")
                    state = json.loads((path / "state.json").read_text())
                    self.assertGreater(state["clips"]["clip0"]["next_poll_at"], time.time())
                finally:
                    thread.join(5)
                result = CutmyClient(self.server.url).resume_collection(path, timeout=15)
                self.assertTrue(result["complete"])
                jid = result["outputs"][0]["job_id"]
                times = self.server.poll_times[jid]
                self.assertEqual(len(times), 2)
                self.assertGreaterEqual(times[1] - times[0], 1.95)
                state = json.loads((path / "state.json").read_text())
                self.assertGreater(state["clips"]["clip0"]["next_poll_at"], 0)

    def test_restore_session_lock_obeys_deadline_and_cancellation(self):
        self.client.run_collection(request(1), self.project, timeout=30)
        self.server.stall_session = True
        for cancelled in (False, True):
            with self.subTest(cancelled=cancelled):
                self.server.stalled.clear(); self.server.release_stall.clear()
                shared = CutmyClient(self.server.url, session_cookie=TOKEN)
                def initialize():
                    try:
                        shared.session(timeout=2)
                    except APIError:
                        pass
                thread = threading.Thread(target=initialize)
                thread.start()
                self.assertTrue(self.server.stalled.wait(2))
                event = threading.Event()
                timer = threading.Timer(.15, event.set)
                if cancelled:
                    timer.start()
                started = time.monotonic()
                try:
                    with self.assertRaises(CollectionError) as stopped:
                        shared.resume_collection(self.project, timeout=5 if cancelled else .15,
                                                 cancel_event=event if cancelled else None)
                    self.assertEqual(stopped.exception.code, "collection_cancelled" if cancelled else "collection_timeout")
                    self.assertLess(time.monotonic() - started, .75)
                finally:
                    timer.cancel()
                    self.server.release_stall.set()
                    thread.join(3)

    def test_stalled_job_json_cancellation_returns_within_one_second(self):
        self.server.stall_job = True
        event = threading.Event()
        cancelled_at = []
        def cancel():
            if self.server.stalled.wait(5):
                cancelled_at.append(time.monotonic())
                event.set()
        thread = threading.Thread(target=cancel)
        thread.start()
        try:
            with self.assertRaises(CollectionError) as stopped:
                CutmyClient(self.server.url, request_timeout=5).run_collection(request(1), self.project, cancel_event=event, timeout=15)
            self.assertEqual(stopped.exception.code, "collection_cancelled")
            self.assertTrue(cancelled_at)
            self.assertLess(time.monotonic() - cancelled_at[0], 1)
            self.assertEqual(len(self.server.jobs), 1)
            self.assertTrue((self.project / "state.json").exists())
        finally:
            self.server.release_stall.set()
            thread.join(5)

    def test_stalled_json_cancellation_does_not_depend_on_shutdown_wakeup(self):
        with patch("socket.socket.shutdown", return_value=None):
            self.test_stalled_job_json_cancellation_returns_within_one_second()

    def test_stalled_tls_handshake_cancellation_does_not_depend_on_shutdown(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0)); listener.listen(1); listener.settimeout(3)
        accepted, release, event = threading.Event(), threading.Event(), threading.Event()
        cancelled_at = []
        def peer():
            try:
                connection, _ = listener.accept()
                with connection:
                    accepted.set()
                    # Accept TCP but never send a TLS ServerHello.
                    release.wait(5)
            except OSError:
                pass
        def cancel():
            if accepted.wait(3):
                time.sleep(.15)
                cancelled_at.append(time.perf_counter())
                event.set()
        peer_thread, cancel_thread = threading.Thread(target=peer), threading.Thread(target=cancel)
        peer_thread.start(); cancel_thread.start()
        try:
            url = "https://127.0.0.1:" + str(listener.getsockname()[1])
            with patch.object(ssl.SSLSocket, "shutdown", return_value=None):
                with self.assertRaises(CollectionError) as stopped:
                    CutmyClient(url, request_timeout=2).run_collection(request(1), self.project, timeout=5, cancel_event=event)
            self.assertEqual(stopped.exception.code, "collection_cancelled")
            self.assertTrue(cancelled_at)
            self.assertLess(time.perf_counter() - cancelled_at[0], 1)
            self.assertEqual(self.server.requests, [])
        finally:
            release.set(); listener.close()
            peer_thread.join(3); cancel_thread.join(3)

    def test_cancellable_reads_wait_for_slow_valid_fragmented_json(self):
        self.server.job_fragment_delay = .15
        event = threading.Event()
        started = time.perf_counter()
        result = CutmyClient(self.server.url, request_timeout=2).run_collection(request(1), self.project, timeout=10, cancel_event=event)
        self.assertTrue(result["complete"])
        self.assertEqual(len(result["outputs"]), 1)
        self.assertGreaterEqual(time.perf_counter() - started, .29)
        self.assertEqual(len(self.server.posts), 1)

    def test_expired_cycle_uses_high_resolution_elapsed_clock(self):
        # Python3.10 Windows monotonic() has coarse tick resolution. It must
        # not keep a microsecond whole-cycle deadline alive until the next tick.
        with patch("cutmyvideo.client.time.monotonic", return_value=100):
            with self.assertRaises(CollectionError) as expired:
                self.client.run_collection(request(1), self.project, timeout=.000001)
        self.assertEqual(expired.exception.code, "collection_timeout")
        self.assertEqual(self.server.requests, [])

    def test_cancelled_name_resolution_does_not_wait_for_request_timeout(self):
        entered, release = threading.Event(), threading.Event()
        event = threading.Event()
        original = socket.getaddrinfo
        def stalled(*args, **kwargs):
            entered.set()
            release.wait(5)
            return original(*args, **kwargs)
        def cancel():
            entered.wait(5)
            event.set()
        thread = threading.Thread(target=cancel)
        thread.start()
        started = time.monotonic()
        try:
            with patch("cutmyvideo.client.socket.getaddrinfo", side_effect=stalled):
                with self.assertRaises(CollectionError) as stopped:
                    CutmyClient(self.server.url, request_timeout=5).run_collection(request(1), self.project, timeout=15, cancel_event=event)
            self.assertEqual(stopped.exception.code, "collection_cancelled")
            self.assertLess(time.monotonic() - started, 1)
            self.assertEqual(self.server.requests, [])
        finally:
            release.set()
            thread.join(5)

    def interrupt_download(self):
        self.server.break_download = True
        def stop(report):
            if report["phase"] == "download_interrupted":
                self.assertGreater(report["pending"][0]["downloaded_bytes"], 0)
                self.assertEqual(report["pending"][0]["stage"], "downloading")
                raise RuntimeError("stop after committed failed transfer")
        with self.assertRaises(CollectionError):
            self.client.run_collection(request(1), self.project, on_progress=stop, timeout=30)

    def test_interrupted_download_genuine_range_and_crash_extras(self):
        self.interrupt_download()
        state = json.loads((self.project / "state.json").read_text())
        part = next(iter(state["parts"].values()))
        with (self.project / part["part"]).open("ab") as stream:
            stream.write(b"uncheckpointed crash bytes")
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertIn((part["offset"], "Thu, 15 Oct 2026 00:00:00 GMT"), self.server.ranges)
        self.assertEqual(Path(result["outputs"][0]["path"]).read_bytes(), next(iter(self.server.files.values())))

    def test_mutable_part_hardlink_never_truncates_or_appends_outside_file(self):
        self.interrupt_download()
        state = json.loads((self.project / "state.json").read_text())
        receipt = next(iter(state["parts"].values()))
        part = self.project / receipt["part"]
        outside = Path(self.directory.name) / "outside.mp4"
        prefix = part.read_bytes()
        outside.write_bytes(prefix + b"unchanged external suffix")
        earlier = outside.read_bytes()
        part.unlink()
        os.link(outside, part)
        with self.assertRaises(CollectionError) as unsafe:
            CutmyClient(self.server.url).resume_collection(self.project, timeout=10)
        self.assertEqual(unsafe.exception.code, "collection_state_invalid")
        self.assertEqual(outside.read_bytes(), earlier)
        self.assertEqual(part.read_bytes(), earlier)
        self.assertEqual(part.stat().st_nlink, 2)
        self.assertFalse(list((self.project / "files").iterdir()))

    def test_ignored_range_restarts_and_malformed_range_preserves_prefix(self):
        self.interrupt_download()
        self.server.malformed_range = True
        with self.assertRaises(CollectionError) as bad:
            CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertEqual(bad.exception.code, "invalid_range")
        self.server.ignore_range = True
        self.server.malformed_range = False
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertEqual(Path(result["outputs"][0]["path"]).read_bytes(), next(iter(self.server.files.values())))

    def test_compressed_download_ignoring_identity_is_never_published(self):
        self.server.compressed_download = True
        with self.assertRaises(CollectionError) as compressed:
            self.client.run_collection(request(1), self.project, timeout=30)
        self.assertEqual(compressed.exception.code, "invalid_response")
        self.assertEqual(self.server.encodings, ["identity"])
        self.assertFalse(list((self.project / "files").iterdir()))
        state = json.loads((self.project / "state.json").read_text())
        part = next(iter(state["parts"].values()))
        self.assertEqual(part["offset"], 0)
        self.assertEqual((self.project / part["part"]).read_bytes(), b"")
        self.server.compressed_download = False
        result = CutmyClient(self.server.url).resume_collection(self.project, timeout=30)
        self.assertTrue(result["complete"])
        self.assertEqual(self.server.encodings, ["identity", "identity"])

    def test_structured_non2xx_and_terminal_jobs(self):
        self.server.partial = True
        response = self.client.submit_collection(request(1), idempotency_key="stable")
        self.assertEqual((response["http_status"], response["retry_after"]), (429, 1))
        self.assertEqual(response["clips"][0]["error"]["code"], "rate_limit")
        self.server.partial = False
        self.server.terminal = True
        with self.assertRaises(CollectionError) as failed:
            self.client.run_collection(request(1), self.project, timeout=30)
        self.assertEqual(failed.exception.code, "job_failed")
        self.assertFalse(failed.exception.report["complete"])

    def test_collection_repair_diagnostic_excludes_unknown_response_fields(self):
        self.server.invalid_validation = True
        with self.assertRaises(APIError) as invalid:
            self.client.validate_collection(request(1))
        self.assertEqual(invalid.exception.code, "invalid_range")
        self.assertEqual(invalid.exception.diagnostic, {"code": "invalid_range", "message": "End must follow start", "clip_id": "clip0",
                                                       "field": "clips[0].ranges[0].end_ms"})
        self.assertNotIn(TOKEN, json.dumps(invalid.exception.diagnostic))
        with self.assertRaises(CollectionError) as failed:
            self.client.run_collection(request(1), self.project, timeout=30)
        pending = failed.exception.report["pending"][0]
        self.assertEqual((pending["clip_id"], pending["field"], pending["code"], pending["retryable"]),
                         ("clip0", "clips[0].ranges[0].end_ms", "invalid_range", False))
        self.assertNotIn(TOKEN, json.dumps(failed.exception.report))
        self.assertNotIn(TOKEN, (self.project / "state.json").read_text())
        self.assertEqual(self.server.posts, [])

    def test_collection_structured_reports_reject_unapproved_statuses(self):
        for status in (401, 403, 418):
            self.server.submission_status = status
            with self.subTest(status=status), self.assertRaises(APIError) as rejected:
                self.client.submit_collection(request(1), idempotency_key="stable")
            self.assertEqual(rejected.exception.status, status)
            self.assertEqual(rejected.exception.idempotency_key, "stable")

    def test_progress_manifest_mutation_cannot_change_private_receipts(self):
        def change(report):
            if report["outputs"]:
                report["outputs"][0]["segments"].append({"fake": True})
                report["outputs"][0]["track_manifest"].append({"fake": True})
        result = self.client.run_collection(request(2), self.project, timeout=30, on_progress=change)
        self.assertTrue(result["complete"])
        state = json.loads((self.project / "state.json").read_text())
        for output in result["outputs"]:
            self.assertEqual(output["segments"], [])
            self.assertEqual(output["track_manifest"], [])
            self.assertEqual(state["receipts"][output["artifact_id"]]["segments"], [])
            self.assertEqual(state["receipts"][output["artifact_id"]]["track_manifest"], [])

    def test_malformed_effective_request_has_stable_error_without_network(self):
        result = self.client.run_collection(request(1), self.project, timeout=30)
        state = json.loads((self.project / "state.json").read_text())
        state["clips"]["clip0"]["effective_request"] = ["invalid"]
        with Project(self.project) as project:
            project.write("state.json", state)
        before = len(self.server.requests)
        with self.assertRaises(CollectionError) as invalid:
            CutmyClient(self.server.url).resume_collection(self.project)
        self.assertEqual(invalid.exception.code, "collection_state_invalid")
        self.assertEqual(len(self.server.requests), before)
        for output in result["outputs"]:
            self.assertEqual(hashlib.sha256(Path(output["path"]).read_bytes()).hexdigest(), output["sha256"])

    def test_scope_tampering_and_live_lock_refuse_without_network(self):
        result = self.client.run_collection(request(1), self.project, timeout=30)
        before = len(self.server.requests)
        with self.assertRaises(CollectionError):
            CutmyClient("http://127.0.0.1:1").resume_collection(self.project, timeout=1)
        with self.assertRaises(CollectionError):
            CutmyClient(self.server.url, session_cookie="another-owner").resume_collection(self.project, timeout=1)
        with Project(self.project):
            with self.assertRaises(CollectionError):
                CutmyClient(self.server.url).resume_collection(self.project, timeout=1)
        self.assertEqual(len(self.server.requests), before)
        output = Path(result["outputs"][0]["path"])
        output.write_bytes(b"tampered")
        with self.assertRaises(CollectionError) as changed:
            CutmyClient(self.server.url).resume_collection(self.project, timeout=1)
        self.assertEqual(changed.exception.code, "output_changed")
        self.assertEqual(output.read_bytes(), b"tampered")

    def test_existing_receipts_remain_with_tighter_aggregate_budget(self):
        result = self.client.run_collection(request(2), self.project, timeout=30)
        before = len(self.server.requests)
        budget = sum(row["size_bytes"] for row in result["outputs"]) - 1
        with self.assertRaises(CollectionError) as limited:
            CutmyClient(self.server.url).resume_collection(self.project, max_output_bytes=budget)
        self.assertEqual(limited.exception.code, "output_limit")
        self.assertEqual(len(self.server.requests), before)
        for output in result["outputs"]:
            self.assertEqual(hashlib.sha256(Path(output["path"]).read_bytes()).hexdigest(), output["sha256"])

    def test_state_owner_and_part_symlinks_are_refused(self):
        if not hasattr(os, "symlink"):
            self.skipTest("Platform lacks symlinks")
        self.interrupt_download()
        state = json.loads((self.project / "state.json").read_text())
        names = ["state.json", "owner.json", next(iter(state["parts"].values()))["part"]]
        for name in names:
            with self.subTest(name=name):
                path = self.project / name
                backup = path.with_name(path.name + ".original")
                path.rename(backup)
                try:
                    try:
                        path.symlink_to(backup)
                    except OSError:
                        self.skipTest("Symlink creation unavailable for this account")
                    before = len(self.server.requests)
                    with self.assertRaises(CollectionError):
                        CutmyClient(self.server.url).resume_collection(self.project, timeout=5)
                    self.assertTrue(path.is_symlink())
                    # A partial-file check follows immutable artifact refresh,
                    # but cannot submit or read any bytes from the linked file.
                    self.assertEqual(len(self.server.posts), 1)
                    if name in ("state.json", "owner.json"):
                        self.assertEqual(len(self.server.requests), before)
                finally:
                    if path.is_symlink():
                        path.unlink()
                    backup.rename(path)

    def test_cancel_deadline_and_corrupt_or_unsafe_state(self):
        event = threading.Event(); event.set()
        with self.assertRaises(CollectionError) as cancelled:
            self.client.run_collection(request(1), self.project, cancel_event=event)
        self.assertEqual(cancelled.exception.code, "collection_cancelled")
        with tempfile.TemporaryDirectory() as other:
            path = Path(other) / "project"
            with self.assertRaises(CollectionError) as timed:
                self.client.run_collection(request(1), path, timeout=0.000001)
            self.assertEqual(timed.exception.code, "collection_timeout")
        with tempfile.TemporaryDirectory() as other:
            path = Path(other) / "project"
            self.client.run_collection(request(1), path, timeout=30)
            state = json.loads((path / "state.json").read_text())
            next(iter(state["receipts"].values()))["path"] = "../outside.mp4"
            (path / "state.json").write_text(json.dumps(state))
            with self.assertRaises(CollectionError):
                CutmyClient(self.server.url).resume_collection(path)
            (path / "state.json").write_text('{bad')
            with self.assertRaises(CollectionError):
                CutmyClient(self.server.url).resume_collection(path)


if __name__ == "__main__":
    unittest.main()
