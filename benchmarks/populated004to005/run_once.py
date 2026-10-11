"""Owned Linux PostgreSQL runner. Imports perform no runtime operations."""
import argparse
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time

# -I excludes the script directory: admit only this reviewed sibling module.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from protocol import (Refused, canonical, loads, require, sha, validate_fixture,
                      validate_identity, validate_resource_sample)

HERE = Path(__file__).resolve().parent
RUNTIME = "d594cc4681dc1cab61664cec8c92bdf9a3e9444c"
FIXTURE = "internal/app/populated004_to005_100k_test.go"
IMAGE = "docker.io/library/postgres@sha256:3645570cccdfa447589da9f57dd740faa29b30938e861289a5574b6ca6b03826"


class OnceRun:
    def __init__(self, output):
        self.output = output
        self.lock = threading.Lock()
        self.stream_bytes = [0, 0]
        self.metadata_bytes = 0
        self.commands = []
        self.admissions = []
        self.samples = []
        self.refusal = threading.Event()
        self.monitor_error = None
        self.interrupted = threading.Event()

    def metadata(self, name, value):
        raw = canonical(value)
        with self.lock:
            require(self.metadata_bytes + len(raw) <= 1048576, "runner1MiB metadata cap")
            self.metadata_bytes += len(raw)
        with (self.output / name).open("xb") as target:
            target.write(raw)

    def capture(self, command, name, timeout=30, env=None, cwd=None, cleanup=False, monitor=False):
        paths = [self.output / (name + ".stdout"), self.output / (name + ".stderr")]
        handles = [p.open("xb", buffering=0) for p in paths]
        start = time.monotonic()
        counts = [0, 0]
        overflow = threading.Event()
        errors = []
        child = None
        limit = 48 * 1024**2 if cleanup else 46 * 1024**2
        def pump(stream, target, index):
            try:
                while block := stream.read(65536):
                    with self.lock:
                        available = max(0, limit - self.stream_bytes[index])
                        kept = block[:available]
                        self.stream_bytes[index] += len(kept)
                    if target.write(kept) != len(kept):
                        raise OSError("short original stream write")
                    counts[index] += len(kept)
                    if len(kept) != len(block):
                        overflow.set()
                        break
            except (OSError, ValueError):
                errors.append("original_stream_capture_failed")
                overflow.set()
        try:
            require(cleanup or not self.refusal.is_set(), "sticky refusal before noncleanup command")
            child = subprocess.Popen(command, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
            workers = [threading.Thread(target=pump, args=(stream, target, i), daemon=True)
                       for i, (stream, target) in enumerate(zip((child.stdout, child.stderr), handles))]
            for worker in workers:
                worker.start()
            deadline = start + timeout
            while child.poll() is None and time.monotonic() < deadline and not overflow.is_set():
                if self.refusal.is_set() and not cleanup and not monitor:
                    break
                time.sleep(.05)
            timed_out = child.poll() is None and time.monotonic() >= deadline
            interrupted = child.poll() is None
            if interrupted:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass
                try:
                    child.wait(timeout=3)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait(timeout=3)
            for worker in workers:
                worker.join(timeout=3)
            if any(w.is_alive() for w in workers):
                # A descendant can retain a pipe after the direct child exits.
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                for worker in workers:
                    worker.join(timeout=3)
            reaped = child.poll() is not None and not any(w.is_alive() for w in workers)
            result = {"argv": command, "owned_pid": child.pid, "owned_process_group": child.pid,
                      "refused": self.refusal.is_set(), "cleanup_command": cleanup,
                      "exit_code": child.returncode, "timeout": timed_out,
                      "interrupted": interrupted, "overflow": overflow.is_set(),
                      "stream_errors": errors, "elapsed_ns": int((time.monotonic() - start) * 10**9),
                      "stdout": paths[0].name, "stderr": paths[1].name,
                      "captured_stdout_bytes": counts[0], "captured_stderr_bytes": counts[1], "reaped": reaped}
            with self.lock:
                self.commands.append(result)
            return result, paths[0].read_bytes()
        finally:
            if child is not None and child.poll() is None:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait(timeout=3)
            for handle in handles:
                handle.close()

    def checked(self, command, name, timeout=30, **kwargs):
        result, raw = self.capture(command, name, timeout, **kwargs)
        require(result["exit_code"] == 0 and not result["timeout"] and not result["overflow"]
                and not result["stream_errors"] and result["reaped"]
                and (kwargs.get("cleanup") is True or not self.refusal.is_set()), "original command failed: " + name)
        return raw

    def admission(self, name):
        mem = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
        observed = {"phase": name, "host_cpus": os.cpu_count() or 0,
                    "mem_available_bytes": int(mem["MemAvailable"].split()[0]) * 1024,
                    "disk_free_bytes": shutil.disk_usage(self.output).free}
        self.admissions.append(observed)
        require(observed["host_cpus"] >= 2 and observed["mem_available_bytes"] >= 3 * 1024**3
                and observed["disk_free_bytes"] >= 4 * 1024**3, "host stage resource admission")

    def container(self, name, phase):
        format_string = '{{json .Image}}\n{{json .Config.Image}}\n{{json .HostConfig.Memory}}\n{{json .HostConfig.NanoCpus}}\n{{json .HostConfig.MemorySwap}}\n{{json .State.OOMKilled}}\n{{json .Config.Labels}}\n{{json .Mounts}}'
        raw = self.checked(["docker", "inspect", "--format", format_string, name], phase + "-container", monitor=True)
        rows = [loads(line) for line in raw.splitlines()]
        require(len(rows) == 8 and rows[1] == IMAGE and rows[2:6] == [1073741824, 2000000000, 1073741824, False],
                "hard PG resource/image/OOM limits")
        require(rows[6].get("cutmy.populated100k.owner") == name, "exact owned container label")
        mounts = [m for m in rows[7] if m["Destination"] == "/var/lib/postgresql/data"]
        require(len(mounts) == 1 and mounts[0]["Type"] == "volume" and mounts[0]["Name"] == name + "-data", "exact owned PG volume")
        return rows

    def resource_sample(self, container, phase):
        raw = self.checked(["docker", "exec", container, "sh", "-c",
                            "du -sb /var/lib/postgresql/data; cat /sys/fs/cgroup/memory.swap.current; cat /sys/fs/cgroup/memory.current; cat /sys/fs/cgroup/memory.peak; stat -f -c '%a %S' /var/lib/postgresql/data; cat /sys/fs/cgroup/memory.events"],
                           "resources-" + phase, 10, monitor=True)
        lines = raw.decode().splitlines()
        require(len(lines) >= 6 and lines[0].split()[0].isdigit()
                and all(line.isdigit() for line in lines[1:4])
                and len(lines[4].split()) == 2 and all(n.isdigit() for n in lines[4].split()),
                "actual PG volume/cgroup/filesystem sample")
        free_blocks, block_bytes = map(int, lines[4].split())
        events = dict((key, int(value)) for key, value in (line.split() for line in lines[5:]))
        observed = {"phase": phase, "volume_bytes": int(lines[0].split()[0]),
                    "swap_current_bytes": int(lines[1]), "memory_current_bytes": int(lines[2]),
                    "memory_peak_bytes": int(lines[3]), "memory_events": events,
                    "pg_disk_free_bytes": free_blocks * block_bytes}
        self.samples.append(observed)
        validate_resource_sample(observed)
        return observed


def check_freeze(freeze, root):
    require(isinstance(freeze.get("files"), dict) and len(freeze["files"]) == 397, "391 current source plus six frozen files")
    for relative, record in freeze["files"].items():
        path = root / relative
        require(not path.is_symlink() and path.is_file(), "frozen harness path")
        raw = path.read_bytes()
        require(len(raw) == record["bytes"] and sha(raw) == record["sha256"]
                and oct(stat.S_IMODE(path.stat().st_mode)) == record["posix_mode"], "frozen harness bytes/modes")


def runtime_inventory(run, checkout, identity, phase, injected=False, cleanup=False):
    version = identity["runtime"]
    head = run.checked(["git", "--no-optional-locks", "-C", str(checkout), "rev-parse", "HEAD"], phase + "-head", cleanup=cleanup).decode().strip()
    tree = run.checked(["git", "-C", str(checkout), "rev-parse", "HEAD^{tree}"], phase + "-tree", cleanup=cleanup).decode().strip()
    parents = run.checked(["git", "-C", str(checkout), "show", "-s", "--format=%P", "HEAD"], phase + "-parents", cleanup=cleanup).decode().strip().split()
    require(head == version["commit"] == RUNTIME and tree == version["tree"] and parents == version["parents"], "immutable runtime Git identity")
    tracked = run.checked(["git", "-C", str(checkout), "ls-tree", "-rz", "--full-tree", "HEAD"], phase + "-tracked", cleanup=cleanup)
    observed = {}
    for row in tracked.split(b"\0"):
        if not row:
            continue
        meta, relative = row.split(b"\t", 1)
        mode, kind, blob = meta.decode().split()
        relative = relative.decode()
        path = checkout / relative
        require(kind == "blob" and path.is_file() and not path.is_symlink(), "runtime tracked regular files")
        raw = path.read_bytes()
        require(bool(path.stat().st_mode & 0o111) == (mode == "100755"), "runtime Git mode")
        observed[relative] = {"mode": mode, "git_blob": blob, "bytes": len(raw), "sha256": sha(raw)}
    require(observed == version["source_files"], "all391 runtime files")
    selector = version["application_selector"]
    require(selector == {"prefixes": ["cmd/", "internal/", "pkg/"], "exact": ["go.mod", "go.sum"]}, "exact application selector")
    application = {path: record for path, record in observed.items()
                   if path.startswith(tuple(selector["prefixes"])) or path in selector["exact"]}
    require(len(application) == version["application_files"] == 173
            and sha(canonical(application)) == version["application_sha256"], "actual173 application identity")
    status = run.checked(["git", "--no-optional-locks", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"], phase + "-status", cleanup=cleanup).decode()
    require(status == ("?? " + FIXTURE + "\n" if injected else ""), "runtime exact injected status")
    if injected:
        require((checkout / FIXTURE).read_bytes() == (HERE / "fixture_test.go.txt").read_bytes(), "same injected fixture")
    run.metadata(phase + "-runtime-map.json", observed)


def main():
    parser = argparse.ArgumentParser()
    for flag in ("authorization", "freeze", "checkout", "output", "private-root"):
        parser.add_argument("--" + flag, required=True, type=Path)
    args = parser.parse_args()
    require(platform.system() == "Linux" and platform.machine() == "x86_64", "Linux amd64 only")
    identity = validate_identity(loads((HERE / "identity.json").read_bytes()))
    authorization = loads(args.authorization.read_bytes())
    freeze_raw = args.freeze.read_bytes()
    freeze = loads(freeze_raw)
    harness_root = HERE.parents[1]
    require(authorization.get("authorized_once") is True
            and authorization.get("kind") == "populated004-to005-100k"
            and authorization.get("freeze_sha256") == sha(freeze_raw)
            and authorization.get("fixture_sha256") == sha((HERE / "fixture_test.go.txt").read_bytes())
            and authorization.get("runner_sha256") == sha(Path(__file__).read_bytes())
            and authorization.get("runtime_commit") == RUNTIME, "separate reviewed once authorization")
    for stage in ("1000", "10000"):
        receipt = authorization.get("accepted_" + stage + "_review", {})
        require(Path(receipt.get("path", "")).is_file() and sha(Path(receipt["path"]).read_bytes()) == receipt.get("sha256")
                and len(receipt["sha256"]) == 64, "accepted prior actual " + stage + " identity")
    check_freeze(freeze, harness_root)
    require(not args.output.exists() and not args.private_root.exists()
            and args.checkout.resolve() != harness_root.resolve(), "fresh owned destinations and separate runtime checkout")
    args.output.mkdir(mode=0o700)
    args.private_root.mkdir(mode=0o700)
    run = OnceRun(args.output)
    run.metadata("once-invocation.json", {"argv": list(os.sys.argv), "authorization_sha256": sha(args.authorization.read_bytes()),
                                        "freeze_sha256": sha(freeze_raw), "identity_sha256": sha((HERE / "identity.json").read_bytes()),
                                        "fixture_sha256": sha((HERE / "fixture_test.go.txt").read_bytes()),
                                        "actual_started": True, "attempt": 1})
    container = "cutmy-populated100k-" + secrets.token_hex(8)
    volume = container + "-data"
    private_env = args.private_root / "postgres.env"
    binary = args.private_root / "populated100k.test"
    injected = args.checkout / FIXTURE
    attempted_container = attempted_volume = injected_owned = False
    monitor_stop = threading.Event()
    monitor_thread = None
    summary = {"complete": False, "pass": False, "failure": None, "cleanup": False, "proof": None,
               "runtime": RUNTIME, "container": container, "volume": volume, "full_raw_row_archive": False,
               "volume_cap_scope": "sampled operational refusal; five-second sampler may overshoot, not a filesystem quota"}
    run.metadata("owned-resources.json", {"format": 1, "container": container, "volume": volume,
                 "private_root": str(args.private_root), "private_env": str(private_env),
                 "binary": str(binary), "checkout": str(args.checkout), "injected_fixture": str(injected),
                 "fixture_sha256": sha((HERE / "fixture_test.go.txt").read_bytes())})
    def interrupted(signum, frame):
        # Never raise asynchronously while a command is capturing its first streams.
        run.interrupted.set()
        run.refusal.set()
        summary["pass"] = False
    previous_signals = {}
    try:
        for signum in (signal.SIGINT, signal.SIGTERM):
            previous_signals[signum] = signal.signal(signum, interrupted)
        run.admission("before-setup")
        runtime_inventory(run, args.checkout, identity, "before-injection")
        require(run.checked(["go", "version"], "go-version").decode().strip() == "go version go1.27.2 linux/amd64", "pinned Go")
        with injected.open("xb") as target:
            target.write((HERE / "fixture_test.go.txt").read_bytes())
        injected_owned = True
        run.admission("before-compile")
        env = dict(os.environ, GOTOOLCHAIN="local", GOMAXPROCS="2", GOMEMLIMIT="128MiB")
        run.checked(["go", "test", "-c", "-o", str(binary), "./internal/app"], "compile", 420, env=env, cwd=args.checkout)
        runtime_inventory(run, args.checkout, identity, "after-compile", True)
        summary["binary_sha256"] = sha(binary.read_bytes())
        run.checked(["docker", "pull", "--platform", "linux/amd64", IMAGE], "pinned-image-pull", 180)
        image = run.checked(["docker", "image", "inspect", "--format", '{{json .Id}}\n{{json .Os}}\n{{json .Architecture}}\n{{json .RepoDigests}}', IMAGE], "pinned-image")
        image_rows = [loads(row) for row in image.splitlines()]
        require(len(image_rows) == 4 and image_rows[1:3] == ["linux", "amd64"]
                and image_rows[0] in (identity["postgres"]["index"], identity["postgres"]["config"])
                and any(item.endswith("@" + identity["postgres"]["index"]) for item in image_rows[3]), "actual OCI image/platform")
        password = secrets.token_hex(24)
        with os.fdopen(os.open(private_env, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600), "w") as target:
            target.write("POSTGRES_PASSWORD=" + password + "\nPOSTGRES_DB=cutmy_populated100k\n")
        attempted_volume = True
        run.checked(["docker", "volume", "create", "--name", volume, "--label", "cutmy.populated100k.owner=" + container], "volume-create")
        attempted_container = True
        run.checked(["docker", "create", "--name", container, "--label", "cutmy.populated100k.owner=" + container,
                     "--platform", "linux/amd64", "--cpus", "2", "--memory", "1g", "--memory-swap", "1g",
                     "--env-file", str(private_env), "--mount", "type=volume,source=" + volume + ",target=/var/lib/postgresql/data",
                     "-p", "127.0.0.1::5432", IMAGE, "-c", "shared_buffers=128MB", "-c", "work_mem=4MB",
                     "-c", "max_connections=32", "-c", "jit=off", "-c", "fsync=on", "-c", "synchronous_commit=on"], "container-create", 120)
        private_env.unlink()
        run.checked(["docker", "start", container], "container-start")
        for poll in range(30):
            result, _ = run.capture(["docker", "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "postgres", "-d", "cutmy_populated100k"], "readiness-" + str(poll), 5)
            if result["exit_code"] == 0:
                break
            require(not result["timeout"] and not result["overflow"] and result["exit_code"] in (1, 2), "readiness outcome")
            time.sleep(1)
        else:
            raise Refused("owned PostgreSQL TCP readiness")
        run.container(container, "before-fixture")
        run.resource_sample(container, "before-fixture")
        port = loads(run.checked(["docker", "inspect", "--format", '{{json (index .NetworkSettings.Ports "5432/tcp")}}', container], "bound-loopback-port"))
        require(len(port) == 1 and port[0]["HostIp"] == "127.0.0.1" and port[0]["HostPort"].isdigit(), "loopback-only publication")
        database = "postgres://postgres:" + password + "@127.0.0.1:" + port[0]["HostPort"] + "/cutmy_populated100k?sslmode=disable"
        fixture_output = args.output / "fixture"
        fixture_output.mkdir(mode=0o700)
        def monitor():
            sample = 0
            admitted = set()
            next_sample = 0
            try:
                while not monitor_stop.wait(.1):
                    for case in ("success", "deadline"):
                        request = fixture_output / ("admission-request-" + case)
                        if case not in admitted and request.exists():
                            require(not request.is_symlink() and request.read_bytes() == (case + "\n").encode(), "actual admission request")
                            run.admission("before-" + case)
                            run.container(container, "admission-" + case)
                            run.resource_sample(container, "before-" + case)
                            with (fixture_output / ("admission-accepted-" + case)).open("xb") as target:
                                target.write((case + "\n").encode())
                            admitted.add(case)
                    require(sum(p.stat().st_size for p in fixture_output.iterdir() if p.is_file()) <= 31 * 1024**2, "fixture31MiB capture cap")
                    if time.monotonic() >= next_sample:
                        run.resource_sample(container, "sample-" + str(sample))
                        sample += 1
                        next_sample = time.monotonic() + 5
            except Exception as error:
                run.monitor_error = str(error) if isinstance(error, Refused) else "resource monitor failed"
                run.refusal.set()
        monitor_thread = threading.Thread(target=monitor, daemon=True)
        monitor_thread.start()
        env.update(TEST_DATABASE_URL=database, CUTMY_POPULATED100K="authorized-once", CUTMY_POPULATED100K_EVIDENCE=str(fixture_output))
        result, _ = run.capture([str(binary), "-test.run=^TestPopulated004To005100k$", "-test.count=1",
                                "-test.timeout=20m", "-test.v"], "fixture-original", 1260, env=env, cwd=args.checkout / "internal/app")
        monitor_stop.set()
        monitor_thread.join(timeout=20)
        require(not monitor_thread.is_alive(), "resource sampler reaped")
        if result["exit_code"] != 0 or result["timeout"] or result["overflow"]:
            # Preserve committed-state observations even if the Go45s safeguard exited.
            names = run.checked(["docker", "exec", container, "psql", "-U", "postgres", "-d", "cutmy_populated100k", "-At",
                                 "-v", "ON_ERROR_STOP=1", "-c", "SELECT nspname FROM pg_namespace WHERE nspname LIKE 'populated100k_%' ORDER BY nspname"],
                                "failed-call-owned-namespaces", 10, cleanup=True).decode().splitlines()
            require(len(names) <= 2, "failure observation owned namespace count")
            for i, name in enumerate(names):
                require(name.startswith(("populated100k_success_", "populated100k_deadline_"))
                        and all(c.islower() or c.isdigit() or c == "_" for c in name), "failure observation owned name")
                sql = "SELECT jsonb_build_object('namespace','" + name + "','ledger',(SELECT jsonb_agg(to_jsonb(m)||jsonb_build_object('xmin',m.xmin::text) ORDER BY position) FROM \"" + name + "\".app_schema_migrations m),'source_pin_columns',(SELECT count(*) FROM information_schema.columns WHERE table_schema='" + name + "' AND table_name='sources' AND column_name IN ('retention_job_pins','retention_preview_pins','retention_pins_valid','retention_pin_revision')))"
                run.capture(["docker", "exec", container, "psql", "-U", "postgres", "-d", "cutmy_populated100k", "-At",
                             "-v", "ON_ERROR_STOP=1", "-c", sql], "failed-call-committed-state-" + str(i), 10, cleanup=True)
        # Keep an original final sample after the operation as well as sampled peaks.
        # Periodic volume sampling can still miss a transient overshoot between reads.
        run.resource_sample(container, "after-fixture")
        require(run.samples and {"before-success", "before-deadline"}.issubset(
            {admission["phase"] for admission in run.admissions}), "actual resource samples and both case admissions")
        require(run.monitor_error is None and result["exit_code"] == 0 and not result["timeout"]
                and not result["overflow"] and not result["stream_errors"] and result["reaped"], "original fixture outcome")
        run.container(container, "after-fixture")
        runtime_inventory(run, args.checkout, identity, "after-fixture", True)
        check_freeze(freeze, harness_root)
        summary["proof"] = validate_fixture(fixture_output, identity)
        summary["complete"] = summary["pass"] = True
    except (Refused, OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        summary["failure"] = str(error) if isinstance(error, Refused) else "owned operation failed"
    finally:
        monitor_stop.set()
        if monitor_thread is not None:
            monitor_thread.join(timeout=20)
        cleanup_start = time.monotonic()
        cleanup_ok = True
        for resource, attempted in (("container", attempted_container), ("volume", attempted_volume)):
            if not attempted:
                continue
            try:
                name = container if resource == "container" else volume
                command = ["docker", "inspect", "--type", "container", "--format", "{{json .Config.Labels}}", name] if resource == "container" else ["docker", "volume", "inspect", "--format", "{{json .Labels}}", name]
                result, raw = run.capture(command, resource + "-cleanup-owner", max(1, 30 - int(time.monotonic() - cleanup_start)), cleanup=True)
                if result["exit_code"] == 0:
                    require(loads(raw).get("cutmy.populated100k.owner") == container, "cleanup owner label")
                    delete = ["docker", "rm", "--force", "--volumes", name] if resource == "container" else ["docker", "volume", "rm", name]
                    run.checked(delete, resource + "-cleanup", max(1, 30 - int(time.monotonic() - cleanup_start)), cleanup=True)
                elif result["exit_code"] != 1:
                    raise Refused("cleanup resource observation")
                absent = ["docker", "ps", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.ID}}"] if resource == "container" else ["docker", "volume", "ls", "--filter", "name=^" + name + "$", "--format", "{{.Name}}"]
                require(run.checked(absent, resource + "-remaining", max(1, 30 - int(time.monotonic() - cleanup_start)), cleanup=True).strip() == b"", "actual owned resource absence")
            except (OSError, Refused, ValueError, subprocess.SubprocessError):
                cleanup_ok = False
        try:
            if private_env.exists():
                private_env.unlink()
            if binary.exists():
                binary.unlink()
            if injected_owned:
                if injected.is_file() and injected.read_bytes() == (HERE / "fixture_test.go.txt").read_bytes():
                    injected.unlink()
                else:
                    cleanup_ok = False
        except OSError:
            cleanup_ok = False
        try:
            args.private_root.rmdir()
            runtime_inventory(run, args.checkout, identity, "after-cleanup", False, True)
            check_freeze(freeze, harness_root)
        except (OSError, Refused, subprocess.SubprocessError):
            cleanup_ok = False
        summary["cleanup"] = cleanup_ok and time.monotonic() - cleanup_start <= 30
        for signum, previous in previous_signals.items():
            signal.signal(signum, previous)
        summary["interrupted"] = run.interrupted.is_set()
        summary["refused"] = run.refusal.is_set()
        summary["pass"] = summary["pass"] and summary["cleanup"] and not run.refusal.is_set()
        summary.update(commands=run.commands, admissions=run.admissions, resource_samples=run.samples,
                       monitor_failure=run.monitor_error, captured_stdout_bytes=run.stream_bytes[0],
                       captured_stderr_bytes=run.stream_bytes[1])
        summary["catalog"] = {p.relative_to(args.output).as_posix(): {"bytes": p.stat().st_size, "sha256": sha(p.read_bytes())}
                              for p in sorted(args.output.rglob("*")) if p.is_file()}
        run.metadata("summary.json", summary)
    print(canonical({k: summary[k] for k in ("complete", "pass", "failure", "cleanup")}).decode(), end="")
    return 0 if summary["pass"] and not run.refusal.is_set() else 1


if __name__ == "__main__":
    raise SystemExit(main())
