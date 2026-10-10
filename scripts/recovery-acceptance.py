#!/usr/bin/env python3
"""Real, disposable v0.2.1 upgrade and coordinated backup/restore acceptance.

Uses only Python's standard library. Processes, databases and synthetic files
belong to this invocation; an explicitly supplied test PostgreSQL is required.
"""

import argparse
import ctypes
import hashlib
import http.client
from http.cookies import SimpleCookie
import json
import os
from pathlib import Path
import re
import shutil
import signal
import socket
import subprocess
import sys
import tarfile
import tempfile
import time
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit
import uuid


HISTORICAL_REVISION = "f2be62c42a6a0d3d7a017b40bd683596bc578b95"
ROOT = Path(__file__).resolve().parents[1]
COMMAND_LOG_DIR = None


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest() if hasattr(hashlib, "file_digest") else hashlib.sha256(stream.read()).hexdigest()


def run(command, *, env=None, cwd=None, stdin=None, stdout=subprocess.PIPE, timeout=120):
    result = subprocess.run(command, env=env, cwd=cwd, stdin=stdin, stdout=stdout,
                            stderr=subprocess.PIPE, timeout=timeout)
    if result.returncode:
        # Arguments and stderr can contain a connection string. Do not expose
        # them in public evidence; private command logs are kept in work-dir.
        if COMMAND_LOG_DIR:
            log = COMMAND_LOG_DIR / ("command-" + uuid.uuid4().hex[:8] + ".log")
            log.write_bytes(result.stderr)
        raise RuntimeError(f"{Path(command[0]).name} failed (exit {result.returncode}); see private command log")
    return result.stdout


def sql_string(value):
    return "'" + value.replace("'", "''") + "'"


class PostgreSQL:
    def __init__(self, dsn, container):
        self.url = urlsplit(dsn)
        require(self.url.scheme in ("postgres", "postgresql"), "RECOVERY_DATABASE_URL must be a PostgreSQL URL")
        require(self.url.hostname in ("127.0.0.1", "localhost", "postgres"), "Recovery acceptance requires an isolated local PostgreSQL")
        self.container = container
        self.created = []
        from urllib.parse import unquote
        self.user = unquote(self.url.username or "postgres")
        self.password = unquote(self.url.password or "")
        self.admin_db = self.url.path.lstrip("/") or "postgres"
        self.environment = {**os.environ, "PGUSER": self.user, "PGPASSWORD": self.password,
                            "PGHOST": self.url.hostname, "PGPORT": str(self.url.port or 5432)}

    def command(self, program, database, *args):
        options = [program, "--username", self.user, "--dbname", database, "--no-password", *args]
        if self.container:
            return ["docker", "exec", "-i", "-e", f"PGPASSWORD={self.password}", self.container,
                    *options, "--host", "127.0.0.1", "--port", "5432"]
        return options

    def sql(self, database, statement):
        command = self.command("psql", database, "-X", "-A", "-t", "--set", "ON_ERROR_STOP=1", "--command", statement)
        return run(command, env=self.environment).decode().strip()

    def create(self, name):
        require(re.fullmatch(r"cutmy_recovery_[a-z0-9_]+", name), "Unsafe acceptance database name")
        self.sql(self.admin_db, f'CREATE DATABASE "{name}"')
        self.created.append(name)

    def dsn(self, name):
        query = [(key, value) for key, value in parse_qsl(self.url.query) if not key.startswith("pool_")]
        return urlunsplit((self.url.scheme, self.url.netloc, "/" + name, urlencode(query), ""))

    def dump(self, database, destination):
        with destination.open("wb") as stream:
            run(self.command("pg_dump", database, "--format=custom"), env=self.environment, stdout=stream)
        require(destination.stat().st_size > 0, "pg_dump produced an empty backup")

    def restore(self, database, source):
        with source.open("rb") as stream:
            run(self.command("pg_restore", database, "--exit-on-error", "--no-owner", "--no-privileges"),
                env=self.environment, stdin=stream)

    def snapshot(self, database):
        tables = ("sources", "jobs", "artifacts", "storage_files", "storage_reservations", "storage_counters", "queue_owners")
        return {table: json.loads(self.sql(database, f"SELECT COALESCE(jsonb_agg(record ORDER BY record::text),'[]'::jsonb) FROM (SELECT to_jsonb(t) record FROM {table} t) rows")) for table in tables}

    def lease(self, database, job_id):
        return json.loads(self.sql(database, "SELECT jsonb_build_object('attempts',attempts,'token',lease_token,'until',lease_until,'active',lease_until>clock_timestamp()) FROM jobs WHERE id=" + sql_string(job_id)))

    def close(self):
        failures = []
        for name in reversed(self.created):
            try:
                self.sql(self.admin_db, f'DROP DATABASE "{name}" WITH (FORCE)')
            except Exception:
                print(f"Could not remove disposable database {name}", file=sys.stderr)
                failures.append("disposable database cleanup failed")
        return failures


class Client:
    def __init__(self, port):
        self.port = port
        self.cookie = ""

    def request(self, method, path, *, data=None, raw=None, headers=None, expected=200):
        fields = dict(headers or {})
        if self.cookie:
            fields["Cookie"] = self.cookie
        if data is not None:
            raw = json.dumps(data).encode()
            fields["Content-Type"] = "application/json"
        connection = http.client.HTTPConnection("127.0.0.1", self.port, timeout=20)
        try:
            connection.request(method, path, body=raw, headers=fields)
            response = connection.getresponse()
            content = response.read()
            response_headers = dict(response.getheaders())
            if response.getheader("Set-Cookie"):
                cookie = SimpleCookie(response.getheader("Set-Cookie"))
                self.cookie = "cutmy_session=" + cookie["cutmy_session"].value
            require(response.status == expected, f"{method} {path} returned {response.status}, expected {expected}")
            if "application/json" in response.getheader("Content-Type", "") and content:
                return json.loads(content)
            return content, response_headers
        finally:
            connection.close()

    def session(self):
        require(self.request("GET", "/api/v1/session")["ok"], "Session creation failed")
        require(bool(self.cookie), "Session cookie is absent")

    def upload(self, fixture, name):
        boundary = "cutmy-recovery-" + uuid.uuid4().hex
        body = (f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="{name}"\r\nContent-Type: video/mp4\r\n\r\n'.encode()
                + fixture.read_bytes() + f"\r\n--{boundary}--\r\n".encode())
        return self.request("POST", "/api/v1/uploads", raw=body,
                            headers={"Content-Type": "multipart/form-data; boundary=" + boundary}, expected=201)

    def job(self, source, key, ranges, *, format="mp4", cut_mode="accurate"):
        body = {"source_id": source["id"], "ranges": ranges, "format": format, "quality": "best", "cut_mode": cut_mode}
        return self.request("POST", "/api/v1/jobs", data=body, headers={"Idempotency-Key": key}, expected=202)

    def get_job(self, job):
        return self.request("GET", "/api/v1/jobs/" + job["id"])


class Acceptance:
    def __init__(self, args):
        self.args = args
        base = Path(args.work_dir or os.environ.get("RECOVERY_WORK_DIR") or tempfile.gettempdir())
        base.mkdir(parents=True, exist_ok=True)
        self.work = Path(tempfile.mkdtemp(prefix="cutmy-recovery-", dir=base))
        global COMMAND_LOG_DIR
        COMMAND_LOG_DIR = self.work
        self.media = self.work / "data"
        self.processes = []
        self.tool_groups = []
        self.pg = PostgreSQL(os.environ["RECOVERY_DATABASE_URL"], os.environ.get("RECOVERY_PG_CONTAINER", ""))
        self.ffmpeg = shutil.which("ffmpeg")
        self.ffprobe = shutil.which("ffprobe")
        require(self.ffmpeg and self.ffprobe, "Install ffmpeg and ffprobe")
        self.gate = self.work / "gate.json"
        self.marker = self.work / "gate-marker.json"
        self.wrapper = self.work / "ffmpeg-wrapper.py"
        shutil.copyfile(ROOT / "scripts/recovery-ffmpeg.py", self.wrapper)
        self.wrapper.chmod(0o700)
        (self.work / "ffmpeg-config.json").write_text(json.dumps({"real_ffmpeg": self.ffmpeg, "gate_path": str(self.gate)}))
        self.report = {"historical_revision": HISTORICAL_REVISION, "checks": [], "artifacts": [], "limitations": [
            "Linux process/container-loss simulation; no physical power-loss or live-provider test",
            "Anonymous capability sessions are preserved; the core has no named accounts",
            "Restore uses a fresh database and fresh physical directory at the original canonical DATA_DIR",
            "SIGKILL includes explicit cleanup of the owned FFmpeg process group, modelling complete container loss",
        ]}
        # Reap tools orphaned by the deliberate parent SIGKILL instead of
        # leaving zombies to the container's init process.
        libc = ctypes.CDLL(None, use_errno=True)
        require(libc.prctl(36, 1, 0, 0, 0) == 0, "Could not enable Linux child subreaping")

    def check(self, name):
        self.report["checks"].append(name)
        print("PASS " + name, flush=True)

    def build(self):
        historical = self.work / "historical"
        historical.mkdir()
        archive = self.work / "historical.tar"
        with archive.open("wb") as stream:
            run(["git", "archive", HISTORICAL_REVISION], cwd=ROOT, stdout=stream)
        with tarfile.open(archive) as files:
            files.extractall(historical)
        archive.unlink()
        old = self.work / "cutmy-v0.2.1"
        current = self.work / "cutmy-current"
        for directory, binary, version, revision in (
                (historical, old, "v0.2.1", HISTORICAL_REVISION),
                (ROOT, current, "recovery-acceptance", run(["git", "rev-parse", "HEAD"], cwd=ROOT).decode().strip())):
            run([self.args.go, "build", "-mod=readonly", "-trimpath", "-ldflags",
                 f"-X main.version={version} -X main.commit={revision}", "-o", str(binary), "./cmd/cutmy"], cwd=directory, timeout=300)
        self.old, self.current = old, current
        self.report["current_revision"] = run(["git", "rev-parse", "HEAD"], cwd=ROOT).decode().strip()
        self.report["current_worktree_diff_sha256"] = hashlib.sha256(run(["git", "diff", "HEAD", "--", "cmd", "internal", "pkg", "go.mod", "go.sum"], cwd=ROOT)).hexdigest()
        source_hash = hashlib.sha256()
        for path in sorted([ROOT / "go.mod", ROOT / "go.sum", *ROOT.glob("cmd/**/*.go"),
                            *ROOT.glob("internal/**/*.go"), *ROOT.glob("internal/**/*.sql"),
                            *ROOT.glob("internal/app/migrations/*.json"), *ROOT.glob("pkg/**/*.go")]):
            source_hash.update(str(path.relative_to(ROOT)).encode() + b"\0" + path.read_bytes() + b"\0")
        self.report["current_source_sha256"] = source_hash.hexdigest()
        self.report["acceptance_script_sha256"] = {name: digest(ROOT / "scripts" / name)
                                                  for name in ("recovery-acceptance.py", "recovery-ffmpeg.py")}
        self.report["binary_sha256"] = {"old": digest(old), "current": digest(current)}
        self.report["go_version"] = run([self.args.go, "version"]).decode().strip()
        self.report["ffmpeg_version"] = run([self.ffmpeg, "-version"]).decode().splitlines()[0]
        self.report["postgres_version"] = self.pg.sql(self.pg.admin_db, "SHOW server_version")
        # Fail early if native tools cannot dump this server version. Container
        # tools come from the same runtime image as the service.
        command = ["docker", "exec", self.pg.container, "pg_dump", "--version"] if self.pg.container else ["pg_dump", "--version"]
        self.report["pg_dump_version"] = run(command).decode().strip()
        major = int(re.search(r"PostgreSQL\) (\d+)", self.report["pg_dump_version"])[1])
        require(major >= int(self.report["postgres_version"].split(".")[0]), "pg_dump is older than the PostgreSQL server")
        fixture = self.work / "synthetic.mp4"
        run([self.ffmpeg, "-hide_banner", "-v", "error", "-nostdin", "-y", "-filter_threads", "1",
             "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=733:sample_rate=48000",
             "-t", "8", "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-pix_fmt", "yuv420p",
             "-c:a", "aac", "-threads", "1", "-movflags", "+faststart", str(fixture)])
        self.fixture = fixture
        self.check("immutable_v0.2.1_and_current_binaries_built")

    def environment(self, database, port):
        return {**os.environ, "DATABASE_URL": self.pg.dsn(database), "DATA_DIR": str(self.media),
                "LISTEN_ADDR": "127.0.0.1:" + str(port), "PUBLIC_ORIGIN": f"http://127.0.0.1:{port}",
                "COOKIE_SECURE": "false", "TRUST_PROXY": "false", "FFMPEG_PATH": str(self.wrapper),
                "FFPROBE_PATH": self.ffprobe, "WORKER_HEALTH_PATH": str(self.work / "worker-health.json"),
                "MAX_SOURCE_BYTES": str(8 << 20), "MAX_FETCH_BYTES": str(8 << 20), "MAX_OUTPUT_BYTES": str(16 << 20),
                "MAX_STORAGE_BYTES": str(1 << 30), "MAX_OWNER_BYTES": str(512 << 20), "STORAGE_SAFETY_BYTES": "0",
                "SOURCE_TTL": "24h", "ARTIFACT_TTL": "24h", "STORAGE_WAIT_TIMEOUT": "2m", "JOB_TIMEOUT": "3m",
                "SOURCE_TIMEOUT": "30s", "UPLOAD_TIMEOUT": "30s", "FFMPEG_THREADS": "1", "WORKER_CONCURRENCY": "1",
                "MUTATIONS_PER_MINUTE": "200", "GOMAXPROCS": "2"}

    def start(self, binary, command, database, port, label):
        log = (self.work / (label + ".log")).open("wb")
        process = subprocess.Popen([str(binary), command], env=self.environment(database, port),
                                   stdout=log, stderr=log, start_new_session=True)
        log.close()
        self.processes.append(process)
        return process

    def server(self, binary, database, port, label):
        process = self.start(binary, "server", database, port, label)
        until = time.monotonic() + 15
        while time.monotonic() < until:
            require(process.poll() is None, label + " exited before readiness; see its private work-dir log")
            try:
                Client(port).request("GET", "/readyz")
                return process
            except (OSError, RuntimeError):
                time.sleep(0.1)
        raise RuntimeError(label + " did not become ready")

    def stop(self, process, kill=False):
        if process.poll() is None:
            process.send_signal(signal.SIGKILL if kill else signal.SIGTERM)
            try:
                process.wait(timeout=15)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise RuntimeError("A service did not stop within its shutdown budget")
            require(process.returncode == (-signal.SIGKILL if kill else 0), "Unexpected service shutdown status")

    def arm(self, job, index=1):
        require(len(job["items"]) > index, "Fault gate item is absent")
        self.marker.unlink(missing_ok=True)
        self.gate.write_text(json.dumps({"target": job["items"][index]["id"] + ".mp4", "marker": str(self.marker)}))

    def disarm(self):
        self.gate.unlink(missing_ok=True)

    def partial(self, client, job, worker, index=1):
        until = time.monotonic() + 20
        while time.monotonic() < until:
            require(worker.poll() is None, "Worker exited before controlled interruption")
            state = client.get_job(job)
            if (self.marker.exists() and state["items"][index]["status"] == "running"
                    and all(item["status"] == "succeeded" for item in state["items"][:index])):
                marker = json.loads(self.marker.read_text())
                require(marker["pid"] == marker["pgid"], "Fault tool does not have an isolated process group")
                self.tool_groups.append(marker)
                return state, marker
            require(state["status"] not in ("failed", "cancelled", "succeeded"), "Job became terminal before fault gate")
            time.sleep(0.1)
        raise RuntimeError("Did not reach a real partially completed job")

    def reap_tool(self, marker):
        try:
            os.killpg(marker["pgid"], signal.SIGKILL)
        except ProcessLookupError:
            pass
        until = time.monotonic() + 3
        remaining = {marker["pid"], marker["child_pid"]}
        while remaining and time.monotonic() < until:
            for pid in list(remaining):
                try:
                    finished, _ = os.waitpid(pid, os.WNOHANG)
                    if finished:
                        remaining.remove(pid)
                except ChildProcessError:
                    try:
                        os.kill(pid, 0)
                    except ProcessLookupError:
                        remaining.remove(pid)
            if remaining:
                time.sleep(0.05)
        require(not remaining, "An owned fault-injection tool survived cleanup")
        self.tool_groups.remove(marker)

    def finished(self, client, job, worker):
        until = time.monotonic() + 90
        while time.monotonic() < until:
            require(worker.poll() is None, "Recovery worker exited unexpectedly")
            state = client.get_job(job)
            if state["status"] == "succeeded":
                require(all(item["status"] == "succeeded" and item["artifact"] for item in state["items"]), "Succeeded job has an incomplete item")
                return state
            require(state["status"] not in ("failed", "cancelled"), "Recovery made a job terminal without succeeding")
            time.sleep(0.3)
        raise RuntimeError("Job did not recover within its real lease expiry budget")

    def verify_artifact(self, client, foreign, item, label):
        artifact = item["artifact"]
        content, _ = client.request("GET", artifact["download_url"])
        require(len(content) == artifact["size_bytes"] and bool(content), "Downloaded artifact size differs from registered size")
        foreign.request("GET", artifact["download_url"], expected=404)
        partial, _ = client.request("GET", artifact["download_url"], headers={"Range": "bytes=0-15"}, expected=206)
        require(partial == content[:16], "Restored download Range differs from full content")
        path = self.work / (label + Path(artifact["filename"]).suffix)
        path.write_bytes(content)
        probe = json.loads(run([self.ffprobe, "-v", "error", "-show_streams", "-show_format", "-of", "json", str(path)]))
        require(float(probe["format"]["duration"]) > 0, "Downloaded output has no playable duration")
        codecs = {stream["codec_type"]: stream["codec_name"] for stream in probe["streams"]}
        require("audio" in codecs, "Synthetic audio was lost")
        if path.suffix == ".mp4":
            require("video" in codecs, "Synthetic video was lost")
        run([self.ffmpeg, "-v", "error", "-nostdin", "-i", str(path), "-f", "null", "-"])
        record = {"label": label, "id": artifact["id"], "sha256": digest(path), "bytes": len(content),
                  "duration_seconds": float(probe["format"]["duration"]), "codecs": codecs}
        self.report["artifacts"].append(record)
        return record

    def ownership(self, owner, foreign, source, job):
        require(owner.request("GET", "/api/v1/sources/" + source["id"])["id"] == source["id"], "Owned source was lost")
        foreign.request("GET", "/api/v1/sources/" + source["id"], expected=404)
        foreign.request("GET", "/api/v1/jobs/" + job["id"], expected=404)
        media, _ = owner.request("GET", "/api/v1/sources/" + source["id"] + "/media")
        require(hashlib.sha256(media).hexdigest() == digest(self.fixture), "Source bytes changed during recovery")
        foreign.request("GET", "/api/v1/sources/" + source["id"] + "/media", expected=404)

    def execute(self):
        self.build()
        token = uuid.uuid4().hex[:12]
        original, restored = "cutmy_recovery_old_" + token, "cutmy_recovery_restored_" + token
        self.pg.create(original)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        self.media.mkdir()
        api = self.server(self.old, original, port, "old-api")
        a, b = Client(port), Client(port)
        a.session()
        b.session()
        require(a.cookie != b.cookie, "Sessions were not independent")
        source_a, source_b = a.upload(self.fixture, "owner-a.mp4"), b.upload(self.fixture, "owner-b.mp4")
        ranges = [{"start_ms": 0, "end_ms": 1000, "label": "retained"}, {"start_ms": 1000, "end_ms": 7000, "label": "interrupted"}]
        batch = a.job(source_a, "same-key-separate-owner", ranges)
        queued = b.job(source_b, "same-key-separate-owner", ranges[:1])
        require(batch["id"] != queued["id"], "Idempotency crossed ownership")
        require(a.job(source_a, "same-key-separate-owner", ranges)["id"] == batch["id"], "Old idempotency replay created a duplicate")
        self.ownership(a, b, source_a, batch)
        self.check("old_real_upload_two_session_ownership_and_queued_idempotency")
        self.arm(batch)
        worker = self.start(self.old, "worker", original, port, "old-worker")
        partial, marker = self.partial(a, batch, worker)
        retained = self.verify_artifact(a, b, partial["items"][0], "before-upgrade")
        self.stop(worker)
        self.reap_tool(marker)
        self.disarm()
        lease = self.pg.lease(original, batch["id"])
        require(lease["active"] and lease["attempts"] == 1, "Interrupted old lease was prematurely released or exhausted")
        require(b.get_job(queued)["status"] == "queued", "Queued job ran before upgrade")
        self.stop(api)
        before_upgrade = self.pg.snapshot(original)
        api = self.server(self.current, original, port, "upgraded-api")
        require(self.pg.snapshot(original) == before_upgrade, "Upgrade changed existing sources/jobs/items/leases/storage records")
        require(a.job(source_a, "same-key-separate-owner", ranges)["id"] == batch["id"], "Upgrade lost idempotency")
        self.ownership(a, b, source_a, batch)
        worker = self.start(self.current, "worker", original, port, "upgraded-worker")
        queued_done = self.finished(b, queued, worker)
        observed_lease = self.pg.lease(original, batch["id"])
        require(observed_lease["token"] == lease["token"] and observed_lease["attempts"] == 1
                and observed_lease["until"] == lease["until"], "Upgrade stole the interrupted lease before the queued job completed")
        done = self.finished(a, batch, worker)
        require(done["items"][0]["artifact"]["id"] == retained["id"], "Upgrade replaced an already completed item")
        recovered = self.verify_artifact(a, b, done["items"][0], "after-upgrade-retained")
        require(recovered["sha256"] == retained["sha256"], "Upgrade changed acknowledged output bytes")
        self.verify_artifact(a, b, done["items"][1], "after-upgrade-recovered")
        self.verify_artifact(b, a, queued_done["items"][0], "after-upgrade-queued")
        require(self.pg.lease(original, batch["id"])["attempts"] == 2, "Old unfinished job did not use exactly one recovery attempt")
        self.check("old_SIGTERM_partial_and_queued_jobs_upgrade_after_real_lease_expiry")

        self.stop(worker)
        interrupted = a.job(source_a, "restore-interrupted", ranges)
        self.arm(interrupted)
        worker = self.start(self.current, "worker", original, port, "before-backup-worker")
        partial, marker = self.partial(a, interrupted, worker)
        retained_restore = self.verify_artifact(a, b, partial["items"][0], "before-backup")
        self.stop(worker, kill=True)
        self.reap_tool(marker)
        self.disarm()
        lease = self.pg.lease(original, interrupted["id"])
        require(lease["active"] and lease["attempts"] == 1, "SIGKILL did not preserve the active unfinished lease")
        before_first = b.job(source_b, "restore-before-first-result", ranges[:1])
        self.arm(before_first, index=0)
        worker = self.start(self.current, "worker", original, port, "before-first-result-worker")
        no_result, marker = self.partial(b, before_first, worker, index=0)
        require(all(item["artifact"] is None for item in no_result["items"]), "Zero-result interruption already published an item")
        require(self.pg.sql(original, "SELECT count(*) FROM artifacts WHERE job_id=" + sql_string(before_first["id"])) == "0",
                "Zero-result interruption already registered an artifact")
        self.stop(worker, kill=True)
        self.reap_tool(marker)
        self.disarm()
        first_lease = self.pg.lease(original, before_first["id"])
        require(first_lease["active"] and first_lease["attempts"] == 1, "First-item interruption did not retain its unfinished lease")
        queued_restore = b.job(source_b, "restore-queued", ranges[:1])
        self.check("real_first_item_SIGKILL_before_any_artifact_publication")
        self.stop(api)
        coordinated = self.pg.snapshot(original)
        dump, media_archive = self.work / "database.dump", self.work / "media.tar.gz"
        self.pg.dump(original, dump)
        media_hashes = {str(path.relative_to(self.media)): digest(path) for path in self.media.rglob("*") if path.is_file()}
        require(media_hashes, "Media backup has no real files")
        with tarfile.open(media_archive, "w:gz") as archive:
            archive.add(self.media, arcname=".")
        self.report["backup"] = {"database_dump_sha256": digest(dump), "database_dump_bytes": dump.stat().st_size,
                                 "media_archive_sha256": digest(media_archive), "media_archive_bytes": media_archive.stat().st_size,
                                 "media_files": len(media_hashes), "writer_processes_stopped": True}
        self.pg.create(restored)
        self.pg.restore(restored, dump)
        require(self.pg.snapshot(restored) == coordinated, "pg_restore changed backed-up jobs/ownership/leases/registered bytes")
        # Exercise the public binary's startup boundary too. Only this throwaway
        # cloned database receives a future marker; old/restored state is intact.
        future = "cutmy_recovery_future_" + token
        self.pg.create(future)
        self.pg.restore(future, dump)
        self.pg.sql(future, "INSERT INTO app_schema_versions(version) VALUES('99991231-future-acceptance')")
        future_snapshot = self.pg.snapshot(future)
        versions_before = self.pg.sql(future, "SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions")
        ledger_before = self.pg.sql(future, "SELECT jsonb_agg(to_jsonb(m) ORDER BY position) FROM app_schema_migrations m")
        with (self.work / "future-startup.log").open("wb") as log:
            refused = subprocess.run([str(self.current), "--json-errors", "server"], env=self.environment(future, port),
                                     stdout=subprocess.PIPE, stderr=log, timeout=15)
        require(refused.returncode == 1 and not refused.stdout, "Current binary did not refuse a future schema at startup")
        startup_error = json.loads((self.work / "future-startup.log").read_text())
        require(startup_error["error"]["code"] == "schema_incompatible", "Future schema did not return its stable startup error")
        require(self.pg.snapshot(future) == future_snapshot, "Future-schema refusal changed application records")
        require(self.pg.sql(future, "SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions") == versions_before,
                "Future-schema refusal changed version markers")
        require(self.pg.sql(future, "SELECT jsonb_agg(to_jsonb(m) ORDER BY position) FROM app_schema_migrations m") == ledger_before,
                "Future-schema refusal changed the migration ledger")
        self.check("actual_binary_future_schema_refusal_without_data_or_ledger_changes")
        old_media = self.work / "media-before-restore"
        original_inode = self.media.stat().st_ino
        self.media.rename(old_media)
        self.media.mkdir()
        require(self.media.stat().st_ino != original_inode, "Restore reused the original physical directory")
        with tarfile.open(media_archive) as archive:
            archive.extractall(self.media)
        restored_hashes = {str(path.relative_to(self.media)): digest(path) for path in self.media.rglob("*") if path.is_file()}
        require(restored_hashes == media_hashes, "Restored media differs from the coordinated backup")
        api = self.server(self.current, restored, port, "restored-api")
        self.ownership(a, b, source_a, batch)
        self.ownership(b, a, source_b, queued_restore)
        require(a.job(source_a, "restore-interrupted", ranges)["id"] == interrupted["id"], "Restore lost idempotency")
        require(a.get_job(interrupted)["items"][0]["artifact"]["id"] == retained_restore["id"], "Restore lost partial results")
        require(self.pg.lease(restored, interrupted["id"])["token"] == lease["token"], "Restore rewrote the old lease")
        require(b.job(source_b, "restore-before-first-result", ranges[:1])["id"] == before_first["id"], "Restore lost zero-result idempotency")
        require(all(item["artifact"] is None for item in b.get_job(before_first)["items"]), "Restore fabricated a result for interrupted first item")
        require(self.pg.lease(restored, before_first["id"])["token"] == first_lease["token"], "Restore rewrote the first-item lease")
        self.check("SIGKILL_coordinated_pg_dump_media_backup_fresh_database_and_directory_restore")
        worker = self.start(self.current, "worker", restored, port, "restored-worker")
        queued_done = self.finished(b, queued_restore, worker)
        recovered = self.finished(a, interrupted, worker)
        require(recovered["items"][0]["artifact"]["id"] == retained_restore["id"], "Recovery replaced the restored completed item")
        restored_result = self.verify_artifact(a, b, recovered["items"][0], "after-restore-retained")
        require(restored_result["sha256"] == retained_restore["sha256"], "Restore changed acknowledged output bytes")
        self.verify_artifact(a, b, recovered["items"][1], "after-restore-recovered")
        self.verify_artifact(b, a, queued_done["items"][0], "after-restore-queued")
        require(self.pg.lease(restored, interrupted["id"])["attempts"] == 2, "Restored unfinished job did not use exactly one recovery attempt")
        first_done = self.finished(b, before_first, worker)
        require(first_done["items"][0]["id"] == before_first["items"][0]["id"], "First-item recovery changed its item identity")
        require(self.pg.lease(restored, before_first["id"])["attempts"] == 2, "Zero-result job did not use exactly one recovery attempt")
        require(self.pg.sql(restored, "SELECT count(*) FROM artifacts WHERE job_id=" + sql_string(before_first["id"])) == "1",
                "First-item recovery registered duplicate or absent artifacts")
        self.verify_artifact(b, a, first_done["items"][0], "after-restore-first-result")
        self.check("restored_zero_result_partial_and_queued_jobs_recover_without_duplicate_results")
        fresh_source = a.upload(self.fixture, "fresh-after-restore.mp4")
        fresh = a.job(fresh_source, "fresh-mp3", ranges[:1], format="mp3")
        fresh_done = self.finished(a, fresh, worker)
        self.verify_artifact(a, b, fresh_done["items"][0], "fresh-after-restore-mp3")
        copy = b.job(source_b, "fresh-copy", ranges[:1], cut_mode="copy")
        copy_done = self.finished(b, copy, worker)
        self.verify_artifact(b, a, copy_done["items"][0], "fresh-after-restore-copy")
        self.check("restored_new_upload_MP3_and_copy_MP4_download_range_probe_decode")
        self.stop(worker)
        self.stop(api)
        final_snapshot = self.pg.snapshot(restored)
        require(all(row["status"] == "succeeded" for row in final_snapshot["jobs"]), "Final recovery has unfinished jobs")
        for row in final_snapshot["storage_files"]:
            path = Path(row["path"])
            require(path.is_relative_to(self.media) and path.is_file(), "Restored ledger references an absent or outside media file")
            require(path.stat().st_size == row["size_bytes"], "Restored ledger charges the wrong file size")
        stored = sum(row["size_bytes"] for row in final_snapshot["storage_files"])
        reserved = sum(row["size_bytes"] for row in final_snapshot["storage_reservations"])
        require(len(final_snapshot["storage_counters"]) == 1, "Restored storage counters are absent")
        counter = final_snapshot["storage_counters"][0]
        require(int(counter["stored_bytes"]) == stored and int(counter["reserved_bytes"]) == reserved,
                "Restored storage counters disagree with registered/reserved rows")
        self.report["storage_accounting"] = {"stored_bytes": stored, "reserved_bytes": reserved,
                                              "retained_reservations": len(final_snapshot["storage_reservations"])}
        self.check("restored_media_ledger_files_and_counters_match")
        self.report["final_counts"] = {table: len(rows) for table, rows in final_snapshot.items()}
        self.report["schema_versions"] = json.loads(self.pg.sql(restored, "SELECT jsonb_agg(version ORDER BY version) FROM app_schema_versions"))
        self.report["ok"] = True

    def close(self):
        failures = []
        for process in reversed(self.processes):
            if process.poll() is None:
                try:
                    self.stop(process)
                except Exception:
                    process.kill()
                    process.wait()
                    failures.append("service cleanup required a forced stop")
        for marker in list(self.tool_groups):
            try:
                self.reap_tool(marker)
            except Exception:
                print("Could not fully reap an owned FFmpeg fault process", file=sys.stderr)
                failures.append("owned FFmpeg process cleanup failed")
        failures.extend(self.pg.close())
        return failures


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--work-dir", help="Parent for disposable files; defaults to RECOVERY_WORK_DIR or TMPDIR")
    parser.add_argument("--keep-work-dir", action="store_true", help="Keep private process logs, synthetic outputs and backup on success")
    args = parser.parse_args()
    require(sys.platform == "linux", "Recovery acceptance currently requires Linux")
    require(bool(os.environ.get("RECOVERY_DATABASE_URL")), "Set RECOVERY_DATABASE_URL to an isolated PostgreSQL with CREATE DATABASE permission")
    os.umask(0o077)
    acceptance = Acceptance(args)
    print("Recovery acceptance work-dir: " + str(acceptance.work), flush=True)
    try:
        acceptance.execute()
    except Exception as error:
        acceptance.report["ok"] = False
        acceptance.report["failure"] = str(error)
        print("FAIL " + str(error), file=sys.stderr)
    finally:
        cleanup_failures = acceptance.close()
        if cleanup_failures:
            acceptance.report["ok"] = False
            acceptance.report["cleanup_failures"] = cleanup_failures
        report_path = acceptance.work / "verification.json"
        report_path.write_text(json.dumps(acceptance.report, indent=2) + "\n")
        destination = os.environ.get("RECOVERY_REPORT_PATH")
        if destination:
            target = Path(destination)
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(report_path, target)
        print("Recovery acceptance report: " + str(report_path), flush=True)
    if acceptance.report["ok"] and not args.keep_work_dir:
        shutil.rmtree(acceptance.work)
    return 0 if acceptance.report["ok"] else 1


if __name__ == "__main__":
    sys.exit(main())
