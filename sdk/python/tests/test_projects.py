import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from cutmyvideo import APIError, CollectionError, CutmyClient, export_portable_project, import_portable_project
from test_collections import Server, request


def normalized(body, clip):
    return {"source_id": body["source_id"], "ranges": clip["ranges"], "format": "mp4", "quality": "best", "cut_mode": "accurate", "output_mode": "joined"}


def project(body, jobs):
    chosen = {clip["id"] for clip in body["plan"]["clips"] if not body.get("clip_ids") or clip["id"] in body["clip_ids"]}
    return {"version": 1, "id": "project-1", "revision": 2, **copy.deepcopy(body), "rendition": "final",
            "source": {"id": body["source_id"], "duration_ms": 120000, "title": "Interview"},
            "clips": [{"clip_id": clip["id"], "request": normalized(body, clip)} for clip in body["plan"]["clips"] if clip["id"] in chosen],
            "jobs": [{"clip_id": clip["id"], "request": normalized(body, clip), "job": jobs[clip["id"]], "current": clip["id"] in chosen} for clip in body["plan"]["clips"]]}


class ProjectTests(unittest.TestCase):
    def setUp(self):
        self.server = Server(); self.addCleanup(self.server.close)
        self.temp = tempfile.TemporaryDirectory(); self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name) / "project"
        self.client = CutmyClient(self.server.url)

    def test_project_sync_adopts_two_edits_without_resubmission_and_keeps_selected_scope(self):
        body = request(15)
        first = self.client.run_collection(body, self.directory, idempotency_key="private-workflow-key", timeout=30)
        jobs = {item["clip_id"]: self.server.jobs[item["job_id"]] for item in first["outputs"]}
        revised = request(15)
        for clip in revised["plan"]["clips"][:2]: clip["ranges"][0]["end_ms"] += 20
        submitted = self.client.submit_collection({**revised, "clip_ids": ["clip0", "clip1"]}, idempotency_key="private-workflow-key")
        for item in submitted["clips"]: jobs[item["clip_id"]] = item["job"]
        self.server.projects["project-1"] = project(revised, jobs)
        before = len(self.server.posts)
        result = CutmyClient(self.server.url).resume_collection(self.directory, project_id="project-1", timeout=30)
        self.assertTrue(result["complete"]); self.assertEqual(len(result["outputs"]), 15)
        self.assertEqual(len(self.server.jobs), 17); self.assertEqual(len(self.server.posts), before)
        old = {item["clip_id"]: (item["job_id"], item["sha256"]) for item in first["outputs"] if item["clip_id"] not in ("clip0", "clip1")}
        self.assertEqual({item["clip_id"]: (item["job_id"], item["sha256"]) for item in result["outputs"] if item["clip_id"] in old}, old)
        selected = {**revised, "clip_ids": ["clip0", "clip1"]}
        self.server.projects["project-1"] = project(selected, jobs)
        subset = CutmyClient(self.server.url).resume_collection(self.directory, project_id="project-1", timeout=10)
        self.assertEqual([item["clip_id"] for item in subset["outputs"]], ["clip0", "clip1"])
        self.assertEqual(len(self.server.posts), before)

    def test_project_conflicts_preserve_saved_request_and_never_adopt_wrong_effective_job(self):
        body = request(1)
        result = self.client.run_collection(body, self.directory, timeout=10)
        jobs = {"clip0": self.server.jobs[result["outputs"][0]["job_id"]]}
        dto = project(body, jobs); dto["jobs"][0]["request"]["ranges"] = [{"start_ms": 7000, "end_ms": 8000}]
        self.server.projects["project-1"] = dto
        before = len(self.server.posts)
        with self.assertRaises(CollectionError) as rejected:
            CutmyClient(self.server.url).resume_collection(self.directory, project_id="project-1", timeout=10)
        self.assertEqual(rejected.exception.code, "project_conflict")
        self.assertEqual(json.loads((self.directory / "state.json").read_text())["request"], body)
        dto = project(body, jobs); self.server.projects["project-1"] = dto
        wrong = request(1); wrong["plan"]["clips"][0]["ranges"][0]["end_ms"] += 20
        with self.assertRaises(CollectionError) as conflict:
            CutmyClient(self.server.url).resume_collection(self.directory, project_id="project-1", request=wrong, timeout=10)
        self.assertEqual(conflict.exception.code, "project_conflict"); self.assertEqual(len(self.server.posts), before)

    def test_portable_document_excludes_server_authority_and_requires_explicit_matching_source(self):
        fingerprint = {"kind": "content", "sha256": hashlib.sha256(b"source").hexdigest(), "size_bytes": 6}
        source = {"id": "source", "duration_ms": 120000, "title": "Interview", "fingerprint": fingerprint,
                  "source_url": "private-url", "session_cookie": "private-cookie"}
        document = export_portable_project(request(2), source)
        encoded = json.dumps(document)
        self.assertNotIn("source_id", document); self.assertNotIn("private-", encoded)
        self.assertEqual(import_portable_project(document, source), request(2))
        with self.assertRaises(ValueError): import_portable_project(document, {**source, "fingerprint": {**fingerprint, "sha256": "0" * 64}})
        with self.assertRaises(ValueError): import_portable_project({**document, "session_cookie": "private-cookie"}, source)
        for mutate in (lambda body: body["plan"].update({"session_cookie": "private-cookie"}),
                       lambda body: body["plan"]["clips"][0].update({"label": {"session_cookie": "private-cookie"}})):
            unsafe = request(2); mutate(unsafe)
            with self.assertRaises(ValueError): export_portable_project(unsafe, source)
        with self.assertRaises(APIError): export_portable_project(request(2), {**source, "title": {"session_cookie": "private-cookie"}})
