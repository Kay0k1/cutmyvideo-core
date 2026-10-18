import hashlib
import json
from pathlib import Path
import sys
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from cutmyvideo import APIError, CutmyClient, LocalCutmy, LocalError


def request(text="1\n00:00:01,000 --> 00:00:02,000\nПривет $(literal)\n", count=1):
    return {"version": 1, "format": "srt", "text": text, "outputs": [
        {"clip_id": "clip" + str(i), "segments": [{"index": 0, "label": "", "source_start_ms": 1000, "source_end_ms": 2000, "output_start_ms": 0, "output_end_ms": 1000}]} for i in range(count)]}


class SubtitleTests(unittest.TestCase):
    def test_local_retime_uses_private_literal_request_and_allows_bounded_large_results(self):
        local = LocalCutmy([sys.executable, str(Path(__file__).with_name("cli_helper.py")), "subtitles"])
        for body in (request(), {**request(), "version": 2}, request("x" * 450000, 3)):
            result = local.retime_subtitles(body, timeout=5)
            self.assertEqual(result["version"], 1)
            self.assertEqual([item["clip_id"] for item in result["outputs"]], [item["clip_id"] for item in body["outputs"]])
            for item in result["outputs"]:
                self.assertEqual(item["text"], body["text"])
                self.assertEqual(item["sha256"], hashlib.sha256(item["text"].encode()).hexdigest())
        bad = LocalCutmy([sys.executable, str(Path(__file__).with_name("cli_helper.py")), "subtitles"], env={"SDK_BAD_SUBTITLE_HASH": "1"})
        with self.assertRaises(LocalError) as rejected: bad.retime_subtitles(request(), timeout=5)
        self.assertEqual(rejected.exception.code, "invalid_response")

    def test_http_result_identity_hash_and_unknown_fields_are_checked(self):
        config, received = {}, []
        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"
            def log_message(self, *_): pass
            def reply(self, value):
                encoded = json.dumps(value).encode()
                self.send_response(200); self.send_header("Content-Length", str(len(encoded)))
                if self.path == "/api/v1/session": self.send_header("Set-Cookie", "cutmy_session=" + "c" * 43)
                self.end_headers(); self.wfile.write(encoded)
            def do_GET(self): self.reply({"ok": True, "limits": {}})
            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"]))); received.append(body)
                text = body["text"]
                self.reply({"version": 1, "unknown_secret": "private-do-not-return", "outputs": [{"clip_id": item["clip_id"], "format": body["format"], "text": text, "cue_count": 1,
                    "sha256": "0" * 64 if config.get("bad_hash") else hashlib.sha256(text.encode()).hexdigest(), "unknown_secret": "private-do-not-return"} for item in body["outputs"]]})
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True); thread.start()
        self.addCleanup(server.server_close); self.addCleanup(server.shutdown)
        client = CutmyClient("http://127.0.0.1:" + str(server.server_port))
        body = request("x" * 450000, 3)
        result = client.retime_subtitles(body)
        self.assertEqual(received[0], body)
        self.assertGreater(sum(len(item["text"]) for item in result["outputs"]), 1 << 20)
        self.assertNotIn("private-do-not-return", json.dumps(result))
        opted = {**request(), "version": 2}
        self.assertEqual(client.retime_subtitles(opted)["version"], 1)
        self.assertEqual(received[-1], opted)
        config["bad_hash"] = True
        with self.assertRaises(APIError) as failed: client.retime_subtitles(request())
        self.assertEqual(failed.exception.code, "invalid_response")

    def test_oversized_or_unsafe_subtitle_request_is_refused_before_network(self):
        client = CutmyClient("http://127.0.0.1:1")
        for body in (request("x" * ((512 << 10) + 1)), {**request(), "outputs": [{"clip_id": "../outside", "segments": [{}]}]}):
            with self.assertRaises(ValueError): client.retime_subtitles(body)
