"""Manual Ubuntu pilot runner. Runtime use requires a reviewed hosted dispatch."""
from __future__ import annotations

import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import subprocess
import threading
import time
import urllib.request

from protocol import (AFTER, BEFORE, PG_CHILD, PG_CONFIG, PG_INDEX, Refused,
                      admitted_sizes, canonical, loads, promotion_ready, require, sha, validate_go_json,
                      validate_identity, validate_image, validate_report)

HERE = Path(__file__).resolve().parent
WORKSPACE = HERE.parents[1]
MAX_LOG = 64 * 1024 * 1024
MIN_FREE_BYTES = 2 * 1024 * 1024 * 1024
COMMANDS = []
ADMISSIONS = []
ARTIFACT_ROOT = None


def exclusive(path, data):
    with path.open("xb") as target:
        target.write(data)


def capture(command, directory, name, timeout, env=None, cwd=None):
    """Bound each child, stream original bytes, preserve failures without retry."""
    stdout, stderr = directory / (name + ".stdout"), directory / (name + ".stderr")
    files = [stdout.open("xb"), stderr.open("xb")]
    start = time.monotonic()
    child = None
    overflow = threading.Event()
    counts = [0, 0]
    errors = []
    def pump(source, target, index):
        try:
            while block := source.read(65536):
                counts[index] += len(block)
                if counts[index] > MAX_LOG:
                    overflow.set()
                    break
                target.write(block)
        except (OSError, ValueError):
            errors.append("stream_failed")
            overflow.set()
    try:
        child = subprocess.Popen(command, cwd=cwd or directory, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        threads = [threading.Thread(target=pump, args=(source, target, index), daemon=True) for index, (source, target) in enumerate(zip((child.stdout, child.stderr), files))]
        for thread in threads:
            thread.start()
        deadline = start + timeout
        while child.poll() is None and time.monotonic() < deadline and not overflow.is_set():
            time.sleep(.1)
        timed_out = child.poll() is None and time.monotonic() >= deadline
        if child.poll() is None:
            os.killpg(child.pid, signal.SIGTERM)
            try:
                child.wait(timeout=3)
            except subprocess.TimeoutExpired:
                os.killpg(child.pid, signal.SIGKILL)
                child.wait(timeout=3)
        for thread in threads:
            thread.join(timeout=3)
        require(not any(thread.is_alive() for thread in threads), "child_stream_not_closed")
        result = {"command": command, "exit_code": child.returncode, "timeout": timed_out, "overflow": overflow.is_set(), "stream_errors": errors, "elapsed_ms": (time.monotonic() - start) * 1000, "stdout": stdout.relative_to(ARTIFACT_ROOT).as_posix(), "stderr": stderr.relative_to(ARTIFACT_ROOT).as_posix()}
    finally:
        for file in files:
            file.close()
    COMMANDS.append(result)
    require(not result["timeout"] and not result["overflow"] and not result["stream_errors"], "bounded_child_refused")
    return result, stdout.read_bytes()


def checked(command, directory, name, timeout=60, env=None, cwd=None):
    result, raw = capture(command, directory, name, timeout, env, cwd)
    require(result["exit_code"] == 0, "command_failed_" + name)
    return raw


def registry_manifest(directory, digest, token, name):
    request = urllib.request.Request("https://registry-1.docker.io/v2/library/postgres/manifests/" + digest, headers={"Accept": "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json", "Authorization": "Bearer " + token})
    with urllib.request.urlopen(request, timeout=30) as response:
        raw = response.read(4 * 1024 * 1024 + 1)
        require(response.status == 200 and len(raw) <= 4 * 1024 * 1024 and response.headers.get("Docker-Content-Digest") == digest and "sha256:" + sha(raw) == digest, "registry_manifest_bytes")
    exclusive(directory / (name + ".raw.json"), raw)
    return loads(raw)


def admission(directory):
    free = shutil.disk_usage(directory).free
    mem = dict(line.split(":", 1) for line in Path("/proc/meminfo").read_text().splitlines())
    available = int(mem["MemAvailable"].split()[0]) * 1024
    cpu = os.cpu_count() or 0
    ADMISSIONS.append({"disk_free_bytes": free, "mem_available_bytes": available, "host_cpus": cpu, "min_disk_free_bytes": MIN_FREE_BYTES, "min_mem_available_bytes": 3 * 1024**3, "min_host_cpus": 2})
    require(free >= MIN_FREE_BYTES, "disk_admission")
    require(available >= 3 * 1024**3, "memory_admission")
    require(cpu >= 2, "cpu_admission")


def inventory(checkout, identity, label, directory, phase):
    version = identity["versions"][label]
    prefix = label + "-" + phase
    head = checked(["git", "-C", str(checkout), "rev-parse", "HEAD"], directory, prefix + "-head").decode().strip()
    tree = checked(["git", "-C", str(checkout), "rev-parse", "HEAD^{tree}"], directory, prefix + "-tree").decode().strip()
    parents = checked(["git", "-C", str(checkout), "show", "-s", "--format=%P", "HEAD"], directory, prefix + "-parents").decode().strip().split()
    require(head == version["commit"] and tree == version["tree"] and parents == version["parents"], "checkout_git_identity")
    raw = checked(["git", "-C", str(checkout), "ls-tree", "-rz", "--full-tree", "HEAD"], directory, prefix + "-tracked")
    observed = {}
    for record in raw.split(b"\0"):
        if not record:
            continue
        metadata, raw_path = record.split(b"\t", 1)
        mode, kind, blob = metadata.decode().split()
        path = raw_path.decode()
        if kind != "blob" or not (path.startswith(("cmd/", "internal/", "pkg/")) or path in ("go.mod", "go.sum")):
            continue
        item = checkout / path
        require(item.is_file() and not item.is_symlink(), "tracked_file_type")
        data = item.read_bytes()
        observed[path] = {"mode": mode, "git_blob": blob, "bytes": len(data), "sha256": sha(data)}
        require(bool(item.stat().st_mode & 0o111) == (mode == "100755"), "tracked_file_mode")
    injected = checkout / "internal/app/storage005_comparison_pilot_test.go"
    fixture_hash = None
    if phase != "before-injection":
        require(injected.is_file() and not injected.is_symlink(), "injected_fixture_type")
        fixture_hash = sha(injected.read_bytes())
        require(fixture_hash == sha((HERE / "pilot_test.go.txt").read_bytes()), "injected_fixture_drift")
    exclusive(directory / (prefix + "-source-map.json"), canonical({"application_files": observed, "injected_fixture_sha256": fixture_hash}))
    require(observed == version["application_files"], "runtime_source_drift")
    status = checked(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"], directory, prefix + "-status").decode()
    allowed = "" if phase == "before-injection" else "?? internal/app/storage005_comparison_pilot_test.go\n"
    require(status == allowed, "checkout_worktree_drift")
    return {"commit": head, "tree": tree, "parents": parents, "application_inventory_sha256": sha(canonical(observed)), "application_files": len(observed), "status": status}


def container_identity(container, directory, phase):
    raw = checked(["docker", "inspect", "--format", '{{json .Image}}\n{{json .Config.Image}}\n{{json .Platform}}\n{{json .HostConfig.Memory}}\n{{json .HostConfig.NanoCpus}}\n{{json .HostConfig.MemorySwap}}\n{{json .State.OOMKilled}}\n{{json .State.Running}}', container], directory, phase + "-container")
    image = checked(["docker", "image", "inspect", "--format", '{{json .Id}}\n{{json .Os}}\n{{json .Architecture}}\n{{json .RepoDigests}}', "docker.io/library/postgres@" + PG_INDEX], directory, phase + "-image")
    return validate_image(raw, image)


def main():
    global ARTIFACT_ROOT
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and os.environ.get("GITHUB_REPOSITORY") == "Kay0k1/cutmyvideo-core", "hosted_dispatch_only")
    require(platform.system() == "Linux" and platform.machine() == "x86_64" and os.environ.get("RUNNER_OS") == "Linux", "hosted_platform")
    identity = validate_identity(loads((HERE / "identity.json").read_bytes()))
    sizes = admitted_sizes(os.environ.get("CUTMY_STORAGE005_MAXIMUM_SIZE"))
    output = Path(os.environ["RUNNER_TEMP"]) / "storage005-pilot"
    output.mkdir(mode=0o700)
    ARTIFACT_ROOT = output
    summary = {"format": 1, "kind": "storage005-fixed-hosted-pilot", "complete": False, "pass": False, "versions": {}, "workloads": [], "container_cleanup": False, "failure_code": None, "dispatch": {key: os.environ.get(key) for key in ("GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_WORKFLOW_REF", "GITHUB_REPOSITORY")}, "identity_sha256": sha((HERE / "identity.json").read_bytes()), "fixture_sha256": sha((HERE / "pilot_test.go.txt").read_bytes())}
    summary.update(maximum_size=sizes[-1], promotion_ready=False)
    container = "cutmy-storage005-" + secrets.token_hex(8)
    created = False
    private_env = output.parent / (container + ".env")
    try:
        admission(output)
        go_version = checked(["go", "version"], output, "go-version").decode().strip()
        require(go_version == "go version go1.27.2 linux/amd64", "go_toolchain")
        summary["go_version"] = go_version
        for label in ("before", "after"):
            checkout = WORKSPACE / ".comparison" / label
            summary["versions"][label] = {"before": inventory(checkout, identity, label, output, "before-injection")}
            exclusive(checkout / "internal/app/storage005_comparison_pilot_test.go", (HERE / "pilot_test.go.txt").read_bytes())
            admission(output)
            binary = output / (label + ".test")
            env = dict(os.environ, GOTOOLCHAIN="local", GOMAXPROCS="2", GOMEMLIMIT="128MiB")
            checked(["go", "test", "-c", "-o", str(binary), "./internal/app"], output, label + "-compile", 420, env, checkout)
            summary["versions"][label]["binary_sha256"] = sha(binary.read_bytes())
            summary["versions"][label]["compiled"] = inventory(checkout, identity, label, output, "after-compile")
        admission(output)
        # Registry raw manifests bind the immutable index to its amd64 child
        # and config. Image-store .Id can legitimately use the index identity.
        with urllib.request.urlopen("https://auth.docker.io/token?service=registry.docker.io&scope=repository:library/postgres:pull", timeout=30) as response:
            token_raw = response.read(1024 * 1024 + 1)
        require(len(token_raw) <= 1024 * 1024, "registry_token_size")
        token = loads(token_raw).get("token")
        require(isinstance(token, str) and token, "registry_token")
        index = registry_manifest(output, PG_INDEX, token, "registry-index")
        require(any(m.get("digest") == PG_CHILD and m.get("platform", {}).get("os") == "linux" and m.get("platform", {}).get("architecture") == "amd64" for m in index.get("manifests", [])), "registry_index")
        child = registry_manifest(output, PG_CHILD, token, "registry-amd64")
        require(child.get("config", {}).get("digest") == PG_CONFIG, "registry_child_config")
        password = secrets.token_hex(24)
        descriptor = os.open(private_env, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(descriptor, "w") as file:
            file.write("POSTGRES_PASSWORD=" + password + "\nPOSTGRES_DB=cutmy_storage005\n")
        # A refused/timeout create may already have created a resource. Always
        # attempt cleanup for this generated name once creation is attempted.
        created = True
        checked(["docker", "create", "--name", container, "--platform", "linux/amd64", "--cpus", "2", "--memory", "1g", "--memory-swap", "1g", "--env-file", str(private_env), "-p", "127.0.0.1::5432", identity["postgres"]["image"], "-c", "shared_buffers=128MB", "-c", "max_connections=32", "-c", "work_mem=4MB", "-c", "jit=off", "-c", "fsync=on", "-c", "synchronous_commit=on"], output, "container-create", 180)
        private_env.unlink()
        checked(["docker", "start", container], output, "container-start")
        ready = False
        for poll in range(30):
            result, _ = capture(["docker", "exec", container, "pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "postgres", "-d", "cutmy_storage005"], output, "tcp-ready-" + str(poll), 10)
            if result["exit_code"] == 0:
                ready = True
                break
            time.sleep(1)
        require(ready, "postgres_tcp_readiness")
        ports = loads(checked(["docker", "inspect", "--format", '{{json (index .NetworkSettings.Ports "5432/tcp")}}', container], output, "bound-port"))
        require(len(ports) == 1 and ports[0]["HostIp"] == "127.0.0.1" and ports[0]["HostPort"].isdigit(), "private_postgres_port")
        database = "postgres://postgres:" + password + "@127.0.0.1:" + ports[0]["HostPort"] + "/cutmy_storage005?sslmode=disable"
        timed_start = time.monotonic()
        for size in sizes:
            if size == 10000:
                require(promotion_ready(summary["workloads"], 1000, identity), "10000_requires_current_1000_promotion")
            summary["promotion_ready"] = False
            for label in ("before", "after"):
                require(time.monotonic() - timed_start < 30 * 60 - 420, "aggregate_workload_budget")
                admission(output)
                prefix = label + "-" + str(size)
                stage = output / prefix
                stage.mkdir(mode=0o700)
                observed_image = container_identity(container, output, prefix)
                env = dict(os.environ, TEST_DATABASE_URL=database, CUTMY_STORAGE005_PILOT="1", CUTMY_STORAGE005_SIZE=str(size), CUTMY_STORAGE005_SCHEMA=str(identity["versions"][label]["schema"]), CUTMY_STORAGE005_SQL_SHA256=identity["versions"][label]["selector_sql_sha256"], CUTMY_STORAGE_REPORT_DIR=str(stage), GOMAXPROCS="2", GOMEMLIMIT="128MiB", GOTOOLCHAIN="local")
                # Only the precompiled binary is run within the workload window.
                command = ["/usr/bin/time", "-v", "-o", str(stage / "process-usage.txt"), "go", "tool", "test2json", "-t", "-p", "github.com/Kay0k1/cutmyvideo-core/internal/app", str(output / (label + ".test")), "-test.run=^TestStorage005ComparisonPilot$", "-test.count=1", "-test.timeout=6m", "-test.v=test2json"]
                # Source-relative AST reads need the immutable app cwd; output
                # files remain in their fresh outside-checkout directory.
                runner_cwd = WORKSPACE / ".comparison" / label / "internal/app"
                result, raw = capture(command, stage, "go-events", 420, env, runner_cwd)
                report = loads((stage / "pilot.json").read_bytes())
                verification = validate_report(report, label, size, identity)
                validate_go_json(raw, result["exit_code"], verification["workload_pass"])
                cleanup_raw = checked(["docker", "exec", container, "psql", "-U", "postgres", "-d", "cutmy_storage005", "-At", "-v", "ON_ERROR_STOP=1", "-c", "SELECT (SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'capacity_%'),(SELECT count(*) FROM pg_roles WHERE rolname LIKE 'comparison_role_%')"], output, prefix + "-namespace-cleanup")
                require(cleanup_raw.strip() == b"0|0", "fixture_cleanup")
                peak = checked(["docker", "exec", container, "cat", "/sys/fs/cgroup/memory.peak"], output, prefix + "-postgres-memory-peak")
                require(peak.strip().isdigit() and int(peak) > 0, "postgres_memory_peak")
                snapshot = inventory(WORKSPACE / ".comparison" / label, identity, label, output, prefix + "-after")
                post_image = container_identity(container, output, prefix + "-after")
                require(observed_image == post_image, "postgres_identity_changed")
                if summary["workloads"]:
                    require(report["postgresql_settings"] == summary["workloads"][0]["postgresql_settings"] and observed_image == summary["workloads"][0]["image"], "unmatched_environment")
                summary["workloads"].append({"version": label, "size": size, "report": prefix + "/pilot.json", "report_sha256": sha((stage / "pilot.json").read_bytes()), "go_json": prefix + "/go-events.stdout", "go_json_sha256": sha(raw), "go_exit": result["exit_code"], "verification": verification, "image": observed_image, "postgresql_settings": report["postgresql_settings"], "post_source": snapshot, "namespace_role_cleanup": True, "postgres_cgroup_cumulative_peak_bytes": int(peak), "process_usage": prefix + "/process-usage.txt"})
            summary["promotion_ready"] = promotion_ready(summary["workloads"], size, identity)
        summary["complete"] = True
        summary["pass"] = all(w["verification"]["workload_pass"] for w in summary["workloads"])
    except (Refused, OSError, ValueError, KeyError, TypeError) as error:
        summary["failure_code"] = str(error) if isinstance(error, Refused) else "pilot_operation_failed"
    finally:
        if private_env.exists():
            private_env.unlink()
        if created:
            try:
                result, _ = capture(["docker", "rm", "--force", "--volumes", container], output, "container-cleanup", 30)
                remaining = checked(["docker", "ps", "--all", "--filter", "name=^/" + container + "$", "--format", "{{.ID}}"], output, "container-remaining", 15)
                summary["container_cleanup"] = result["exit_code"] == 0 and remaining.strip() == b""
            except (OSError, Refused):
                summary["container_cleanup"] = False
        else:
            summary["container_cleanup"] = True
        summary["commands"] = COMMANDS
        summary["resource_admissions"] = ADMISSIONS
        summary["pass"] = summary["pass"] and summary["container_cleanup"]
        summary["promotion_ready"] = summary["promotion_ready"] and summary["container_cleanup"]
        # Executable files are not needed in the downloadable measurement
        # archive. Their recorded hashes remain, and original raw logs stay.
        for label in ("before", "after"):
            binary = output / (label + ".test")
            if binary.exists():
                binary.unlink()
        summary["catalog"] = {str(p.relative_to(output)): {"bytes": p.stat().st_size, "sha256": sha(p.read_bytes())} for p in sorted(output.rglob("*")) if p.is_file()}
        exclusive(output / "summary.json", canonical(summary))
    print(json.dumps({key: summary[key] for key in ("complete", "pass", "promotion_ready", "maximum_size", "failure_code", "container_cleanup")}, sort_keys=True))
    return 0 if summary["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
