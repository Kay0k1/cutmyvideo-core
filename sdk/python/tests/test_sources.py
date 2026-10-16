import hashlib
import json
import os
from pathlib import Path
import socket
import tempfile
import threading
import time
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

from cutmyvideo import APIError, CutmyClient, SourcePreparationError
from cutmyvideo._collection_state import Project

TOKEN = "s" * 43


class PreparationServer:
    def __init__(self):
        self.receipts, self.calls, self.posts = {}, [], []
        self.lose_reply = False
        self.block = False
        self.stalled, self.release = threading.Event(), threading.Event()
        outer = self
        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *_): pass
            def reply(self, value, status=200):
                encoded = json.dumps(value).encode()
                self.send_response(status)
                self.send_header("Content-Length", str(len(encoded)))
                self.send_header("Content-Type", "application/json")
                if self.path == "/api/v1/session": self.send_header("Set-Cookie", "cutmy_session=" + TOKEN)
                self.end_headers(); self.wfile.write(encoded)
            def do_GET(self):
                outer.calls.append((self.command, self.path))
                if self.path == "/api/v1/session": return self.reply({"ok": True, "limits": {}})
                if self.headers.get("Cookie") != "cutmy_session=" + TOKEN: return self.reply({"error": {"code": "unauthorized"}}, 401)
                key = self.path.rsplit("/", 1)[1]
                return self.reply(outer.receipts[key]) if key in outer.receipts else self.reply({"error": {"code": "not_found"}}, 404)
            def do_POST(self):
                outer.calls.append((self.command, self.path))
                key = self.headers.get("Idempotency-Key")
                outer.posts.append((key, self.path))
                if outer.block:
                    outer.stalled.set(); outer.release.wait(5); self.close_connection = True; return
                if self.headers.get("Cookie") != "cutmy_session=" + TOKEN: return self.reply({"error": {"code": "unauthorized"}}, 401)
                body = self.rfile.read(int(self.headers["Content-Length"]))
                if self.path == "/api/v1/uploads":
                    boundary = self.headers["Content-Type"].split("boundary=", 1)[1].encode()
                    payload = body.split(b"\r\n\r\n", 1)[1].rsplit(b"\r\n--" + boundary, 1)[0]
                    fingerprint = {"kind": "content", "sha256": hashlib.sha256(payload).hexdigest(), "size_bytes": len(payload)}
                    if self.headers.get("Upload-SHA256") != fingerprint["sha256"] or self.headers.get("Upload-Size") != str(len(payload)):
                        return self.reply({"error": {"code": "source_fingerprint_mismatch"}}, 409)
                else:
                    url = json.loads(body)["url"]
                    fingerprint = {"kind": "provider", "provider": "fixture", "provider_id": "recording", "canonical_url_sha256": hashlib.sha256(url.encode()).hexdigest()}
                if key not in outer.receipts:
                    source = {"id": "source-" + str(len(outer.receipts) + 1), "duration_ms": 9000, "title": "Fixture", "fingerprint": fingerprint, "unknown_secret": TOKEN}
                    outer.receipts[key] = {"version": 1, "key": key, "state": "ready", "source_id": source["id"], "source": source, "fingerprint": fingerprint}
                if outer.lose_reply:
                    outer.lose_reply = False; self.connection.shutdown(socket.SHUT_RDWR); self.close_connection = True; return
                return self.reply(outer.receipts[key]["source"], 201)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True); self.thread.start()
        self.url = "http://127.0.0.1:" + str(self.server.server_port)
    def close(self):
        self.release.set(); self.server.shutdown(); self.server.server_close(); self.thread.join()


class SourceTests(unittest.TestCase):
    def test_keys_with_spaces_are_refused_before_owner_network_or_durable_initialization(self):
        client = CutmyClient(self.server.url)
        for key in ("agent key", " leading", "trailing ", ".", ".."):
            operations = [
                lambda: client.get_source_preparation(key),
                lambda: client.import_source("https://example.test/interview", idempotency_key=key),
                lambda: client.upload_source(self.input, idempotency_key=key),
                lambda: client.submit_project_collection("project", revision=1, clip_ids=["clip1"], retry_failed=True, idempotency_key=key),
                lambda: client.prepare_source({"kind": "upload", "path": str(self.input)}, self.project, idempotency_key=key),
            ]
            for operation in operations:
                with self.assertRaises(ValueError): operation()
            self.assertFalse(self.project.exists())
            self.assertEqual(self.server.calls, [])
            with self.assertRaises(APIError) as owner: client.export_session_cookie()
            self.assertEqual(owner.exception.code, "session_unavailable")
        # Visible punctuation at both grammar boundaries remains accepted.
        self.assertTrue(client.prepare_source({"kind": "import", "url": "https://example.test/interview"}, self.project,
            idempotency_key="!source~", timeout=10)["complete"])

    def setUp(self):
        self.server = PreparationServer(); self.addCleanup(self.server.close)
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name); self.project = self.root / "source-project"
        self.input = self.root / "interview.mp4"; self.input.write_bytes(b"synthetic-source" * 100)

    def test_lost_upload_reply_fresh_owner_restore_recovers_without_reupload(self):
        self.server.lose_reply = True
        def stop(report):
            if report["phase"] == "uncertain": raise RuntimeError("Stop after lost committed upload reply")
        with self.assertRaises(SourcePreparationError) as interrupted:
            CutmyClient(self.server.url).prepare_source({"kind": "upload", "path": str(self.input)}, self.project, idempotency_key="private-source-key", on_progress=stop)
        self.assertEqual(interrupted.exception.code, "progress_interrupted")
        self.input.unlink()
        result = CutmyClient(self.server.url).resume_source(self.project, timeout=10)
        self.assertTrue(result["complete"]); self.assertEqual(result["source_id"], "source-1")
        self.assertEqual(len(self.server.posts), 1)
        self.assertNotIn(TOKEN, json.dumps(result)); self.assertNotIn("private-source-key", json.dumps(result))
        if os.name == "posix": self.assertEqual((self.project / "owner.json").stat().st_mode & 0o777, 0o600)

    def test_source_input_change_before_first_upload_is_refused(self):
        def stop(report):
            if report["phase"] == "created": raise RuntimeError("Stop before submission")
        with self.assertRaises(SourcePreparationError):
            CutmyClient(self.server.url).prepare_source({"kind": "upload", "path": str(self.input)}, self.project, on_progress=stop)
        self.input.write_bytes(b"different-content")
        with self.assertRaises(SourcePreparationError) as rejected:
            CutmyClient(self.server.url).resume_source(self.project, timeout=10)
        self.assertEqual(rejected.exception.code, "source_changed"); self.assertEqual(self.server.posts, [])

    def test_import_recovers_provider_identity_without_claiming_content_hash(self):
        result = CutmyClient(self.server.url).prepare_source({"kind": "import", "url": "https://example.test/recording"}, self.project, timeout=10)
        self.assertEqual(result["fingerprint"]["kind"], "provider")
        self.assertNotIn("sha256", result["fingerprint"])
        replay = CutmyClient(self.server.url).resume_source(self.project, timeout=10)
        self.assertEqual(result["source_id"], replay["source_id"]); self.assertEqual(len(self.server.posts), 1)

    def test_source_bootstrap_recovers_owner_only_boundary_before_any_post(self):
        original = Project.write
        def interrupted(project, name, data, **options):
            if name == "state.json": raise OSError("Stop at initial state publication")
            return original(project, name, data, **options)
        with patch.object(Project, "write", interrupted), self.assertRaises(SourcePreparationError):
            CutmyClient(self.server.url).prepare_source({"kind": "upload", "path": str(self.input)}, self.project, idempotency_key="original-key")
        self.assertFalse((self.project / "state.json").exists()); self.assertTrue((self.project / ".source-init.json").exists())
        self.assertEqual(self.server.posts, [])
        result = CutmyClient(self.server.url).resume_source(self.project, timeout=10)
        self.assertTrue(result["complete"]); self.assertEqual(self.server.posts, [("original-key", "/api/v1/uploads")])
        self.assertFalse((self.project / ".source-init.json").exists())

    def test_conflicting_server_and_owner_refuse_before_network(self):
        CutmyClient(self.server.url).prepare_source({"kind": "upload", "path": str(self.input)}, self.project, timeout=10)
        before = len(self.server.calls)
        for client in (CutmyClient("http://127.0.0.1:1"), CutmyClient(self.server.url, session_cookie="different-owner")):
            with self.assertRaises(SourcePreparationError): client.resume_source(self.project, timeout=1)
        self.assertEqual(len(self.server.calls), before)

    def test_stalled_upload_cancellation_does_not_depend_on_shutdown_wakeup(self):
        self.input.write_bytes(b"x" * (8 << 20)); self.server.block = True
        cancelled = threading.Event(); stopped = []
        def stop():
            if self.server.stalled.wait(5):
                time.sleep(.15); stopped.append(time.perf_counter()); cancelled.set()
        thread = threading.Thread(target=stop, daemon=True); thread.start()
        with patch.object(socket.socket, "shutdown", lambda *args, **kwargs: None):
            with self.assertRaises(SourcePreparationError) as error:
                CutmyClient(self.server.url).prepare_source({"kind": "upload", "path": str(self.input)}, self.project, timeout=5, cancel_event=cancelled)
        thread.join(1)
        self.assertEqual(error.exception.code, "source_preparation_cancelled")
        self.assertTrue(stopped); self.assertLess(time.perf_counter() - stopped[0], 1)
        state = json.loads((self.project / "state.json").read_text())
        self.assertTrue(state["uncertain"])
