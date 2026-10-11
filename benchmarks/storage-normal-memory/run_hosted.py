"""Manual Ubuntu pilot runner. Runtime use requires a reviewed hosted dispatch."""
from __future__ import annotations

import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import signal
import stat
import subprocess
import threading
import time
import urllib.request

import importlib.util
import sys

HERE = Path(__file__).resolve().parent
WORKSPACE = HERE.parents[1]
PINS = json.loads((HERE / "identity.json").read_bytes())
for relative, record in PINS["legacy_imports"].items():
    source = WORKSPACE / relative
    raw = source.read_bytes()
    if source.is_symlink() or len(raw) != record["bytes"] or __import__("hashlib").sha256(raw).hexdigest() != record["sha256"]:
        raise RuntimeError("legacy_source_pin")
spec = importlib.util.spec_from_file_location("protocol", WORKSPACE / "benchmarks/storage-normal-capacity/protocol.py")
protocol = importlib.util.module_from_spec(spec)
sys.modules["protocol"] = protocol
spec.loader.exec_module(protocol)
from protocol import (PG_CHILD, PG_CONFIG, PG_INDEX, Refused, canonical, loads,
                      require, sha, validate_go_json, validate_identity, validate_image, validate_report)
from observe import Observer, bind_cgroup

HERE = Path(__file__).resolve().parent
WORKSPACE = HERE.parents[1]
MAX_LOG = 64 * 1024 * 1024
MIN_FREE_BYTES = 2 * 1024 * 1024 * 1024
COMMANDS = []
ADMISSIONS = []
ARTIFACT_ROOT = None
PRIVATE_ROOT = None
REFUSED = threading.Event()
INTERRUPTED = threading.Event()
EVIDENCE_LIMIT = 256 * 1024 * 1024
CLEANUP_RESERVE = 16 * 1024 * 1024
OWNER_LABEL = "cutmy.storage-normal-memory.owner"


def owned_tree_bytes(roots):
    """Census exact owned roots without suppressed traversal errors or symlink following."""
    total=0;flags=os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
    def visit(descriptor):
        nonlocal total
        with os.scandir(descriptor) as entries:
            for entry in entries:
                try:
                    info=entry.stat(follow_symlinks=False)
                except FileNotFoundError:
                    continue
                require(not stat.S_ISLNK(info.st_mode),"evidence_symlink")
                require(stat.S_ISREG(info.st_mode) or stat.S_ISDIR(info.st_mode),"evidence_type")
                if stat.S_ISREG(info.st_mode):
                    total+=info.st_size
                else:
                    try:
                        child=os.open(entry.name,flags,dir_fd=descriptor)
                    except FileNotFoundError:
                        continue
                    try:
                        visit(child)
                    finally:
                        os.close(child)
    for root in roots:
        try:
            descriptor=os.open(root,flags)
        except FileNotFoundError:
            continue
        try:
            visit(descriptor)
        finally:
            os.close(descriptor)
    return total


def evidence_gate(cleanup=False):
    if ARTIFACT_ROOT is None:
        return
    roots = [ARTIFACT_ROOT.parent]
    if PRIVATE_ROOT is not None:
        try:
            mode=PRIVATE_ROOT.lstat().st_mode
        except FileNotFoundError:
            pass  # This exact owned directory may have completed cleanup.
        else:
            require(stat.S_ISDIR(mode) and not stat.S_ISLNK(mode), "private_type")
            roots.append(PRIVATE_ROOT)
    total=owned_tree_bytes(roots)
    limit = EVIDENCE_LIMIT if cleanup else EVIDENCE_LIMIT - CLEANUP_RESERVE
    if total > limit:
        REFUSED.set()
        raise Refused("aggregate_evidence_cap")



def exclusive(path, data):
    with path.open("xb") as target:
        target.write(data)


def capture(command, directory, name, timeout, env=None, cwd=None, cap=MAX_LOG, cleanup=False):
    """Bound a child and retain its first terminal record, including spawn/reap failures."""
    stdout, stderr = directory / (name + ".stdout"), directory / (name + ".stderr")
    files = [stdout.open("xb"), stderr.open("xb")]
    start=time.monotonic();child=None;threads=[]
    overflow=threading.Event();counts=[0,0];errors=[];timed_out=False
    def signal_child(number):
        if child is not None:
            try:
                os.killpg(child.pid,number)
            except ProcessLookupError:
                pass  # The exact child may exit between poll and signal.
    def pump(source,target,index):
        try:
            while block := source.read(65536):
                counts[index]+=len(block)
                if counts[index] > cap:
                    overflow.set();REFUSED.set();break
                evidence_gate(cleanup)
                target.write(block)
        except (OSError,ValueError):
            REFUSED.set();errors.append("stream_failed");overflow.set()
    try:
        exclusive(directory/(name+".invocation.json"),canonical({"argv":command,"cwd":str(cwd or directory),
            "timeout_seconds":timeout,"stdout_cap_bytes":cap,"stderr_cap_bytes":cap,"cleanup_command":cleanup,"retry":False}))
        require(cleanup or not REFUSED.is_set(),"sticky_refusal_before_command")
        evidence_gate(cleanup)
        child=subprocess.Popen(command,cwd=cwd or directory,env=env,stdin=subprocess.DEVNULL,
                               stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True)
        threads=[threading.Thread(target=pump,args=(source,target,index),daemon=True)
                 for index,(source,target) in enumerate(zip((child.stdout,child.stderr),files))]
        for thread in threads:
            thread.start()
        deadline=start+timeout
        while child.poll() is None and time.monotonic() < deadline and not overflow.is_set():
            if not cleanup and REFUSED.is_set():
                break
            evidence_gate(cleanup)
            time.sleep(.05)
        timed_out=child.poll() is None and time.monotonic() >= deadline
        if timed_out:
            REFUSED.set()
    except (OSError,ValueError,subprocess.SubprocessError) as error:
        REFUSED.set();errors.append("spawn_or_capture_failed:"+type(error).__name__)
    finally:
        try:
            if child is not None:
                if child.poll() is None:
                    signal_child(signal.SIGTERM)
                    try:
                        child.wait(timeout=3)
                    except subprocess.TimeoutExpired:
                        signal_child(signal.SIGKILL)
                        child.wait(timeout=3)
                else:
                    child.wait(timeout=3)
                for thread in threads:
                    thread.join(timeout=3)
                if any(thread.is_alive() for thread in threads):
                    signal_child(signal.SIGKILL)
                    for thread in threads:
                        thread.join(timeout=3)
                if any(thread.is_alive() for thread in threads):
                    errors.append("child_stream_not_closed");REFUSED.set()
        except (OSError,ValueError,subprocess.SubprocessError) as error:
            REFUSED.set();errors.append("child_reconciliation_failed:"+type(error).__name__)
            try:
                signal_child(signal.SIGKILL)
                if child is not None:
                    child.wait(timeout=3)
            except (OSError,ValueError,subprocess.SubprocessError) as error:
                errors.append("final_reap_failed:"+type(error).__name__)
        finally:
            for target in files:
                try:
                    target.close()
                except OSError:
                    REFUSED.set();errors.append("original_stream_close_failed")
            result={"command":command,"owned_pid":None if child is None else child.pid,
                    "spawned":child is not None,"reaped":child is not None and child.returncode is not None and not any(t.is_alive() for t in threads),
                    "cleanup_command":cleanup,"refused":REFUSED.is_set(),"interrupted":INTERRUPTED.is_set(),
                    "exit_code":None if child is None else child.returncode,"timeout":timed_out,
                    "overflow":overflow.is_set(),"stream_errors":errors,"elapsed_ms":(time.monotonic()-start)*1000,
                    "stdout":stdout.relative_to(ARTIFACT_ROOT).as_posix(),"stderr":stderr.relative_to(ARTIFACT_ROOT).as_posix()}
            COMMANDS.append(result)
            exclusive(directory/(name+".result.json"),canonical(result))
    evidence_gate(cleanup)
    require(cleanup or not REFUSED.is_set(),"sticky_refusal_after_command")
    require(result["reaped"] and not result["timeout"] and not result["overflow"] and not result["stream_errors"],"bounded_child_refused")
    return result,stdout.read_bytes()


def checked(command, directory, name, timeout=60, env=None, cwd=None, cap=MAX_LOG, cleanup=False):
    result, raw = capture(command, directory, name, timeout, env, cwd, cap, cleanup)
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
    all_observed = {}
    for record in raw.split(b"\0"):
        if not record:
            continue
        metadata, raw_path = record.split(b"\t", 1)
        mode, kind, blob = metadata.decode().split()
        path = raw_path.decode()
        require(kind == "blob", "tracked_source_type")
        item = checkout / path
        require(item.is_file() and not item.is_symlink(), "tracked_file_type")
        data = item.read_bytes()
        all_observed[path] = {"mode": mode, "git_blob": blob, "bytes": len(data), "sha256": sha(data)}
        if path.startswith(("cmd/", "internal/", "pkg/")) or path in ("go.mod", "go.sum"):
            observed[path] = all_observed[path]
        require(bool(item.stat().st_mode & 0o111) == (mode == "100755"), "tracked_file_mode")
    injected = checkout / "internal/app/storage_normal_capacity_pilot_test.go"
    fixture_hash = None
    if phase != "before-injection":
        require(injected.is_file() and not injected.is_symlink(), "injected_fixture_type")
        fixture_hash = sha(injected.read_bytes())
        require(fixture_hash == sha((HERE / "pilot_test.go.txt").read_bytes()), "injected_fixture_drift")
    exclusive(directory / (prefix + "-source-map.json"), canonical({"application_files": observed, "injected_fixture_sha256": fixture_hash}))
    require(observed == version["application_files"], "runtime_source_drift")
    require(all_observed == version["source_files"], "whole_source_drift")
    exclusive(directory / (prefix + "-whole-source-map.json"), canonical(all_observed))
    status = checked(["git", "-C", str(checkout), "status", "--porcelain=v1", "--untracked-files=all"], directory, prefix + "-status").decode()
    allowed = "" if phase == "before-injection" else "?? internal/app/storage_normal_capacity_pilot_test.go\n"
    require(status == allowed, "checkout_worktree_drift")
    return {"commit": head, "tree": tree, "parents": parents, "application_inventory_sha256": sha(canonical(observed)), "application_files": len(observed), "status": status}


def container_identity(container, directory, phase):
    raw = checked(["docker", "inspect", "--format", '{{json .Image}}\n{{json .Config.Image}}\n{{json .Platform}}\n{{json .HostConfig.Memory}}\n{{json .HostConfig.NanoCpus}}\n{{json .HostConfig.MemorySwap}}\n{{json .State.OOMKilled}}\n{{json .State.Running}}', container], directory, phase + "-container")
    image = checked(["docker", "image", "inspect", "--format", '{{json .Id}}\n{{json .Os}}\n{{json .Architecture}}\n{{json .RepoDigests}}', "docker.io/library/postgres@" + PG_INDEX], directory, phase + "-image")
    return validate_image(raw, image)


BOUNDARY_SQL = """SELECT jsonb_build_object(
'data_directory',current_setting('data_directory'),
'database_bytes',pg_database_size(current_database()),
'database_stats',(SELECT to_jsonb(d) FROM pg_stat_database d WHERE datname=current_database()),
'owned_tables',(SELECT COALESCE(jsonb_agg(jsonb_build_object('schema',s.schemaname,'table',s.relname,'relation_bytes',pg_total_relation_size(s.relid),'stats',to_jsonb(s)) ORDER BY s.schemaname,s.relname),'[]') FROM pg_stat_user_tables s WHERE s.schemaname LIKE 'capacity_%'),
'backend_counts',(SELECT jsonb_agg(to_jsonb(a)) FROM (SELECT state,backend_type,count(*) FROM pg_stat_activity GROUP BY state,backend_type) a))"""
BOUNDARY_DISK = 'test "$PGDATA" = /var/lib/postgresql/data; du -sb "$PGDATA" "$PGDATA/pg_wal"; find "$PGDATA" -type f -path "*/pgsql_tmp/*" -printf "%s\\n" | awk \'{total += $1; count++} END {print "temporary_files", count+0, "temporary_bytes", total+0}\''

def validate_boundary(sql, disk):
    parsed = loads(sql)
    require(isinstance(parsed, dict) and parsed.get("data_directory") == "/var/lib/postgresql/data" and type(parsed.get("database_bytes")) is int and parsed["database_bytes"] > 0 and isinstance(parsed.get("database_stats"), dict) and isinstance(parsed.get("owned_tables"), list), "boundary_sql_shape")
    lines = [line.split() for line in disk.decode("ascii").splitlines()]
    require(len(lines) == 3 and all(len(line) == 2 and line[0].isdigit() and line[1] == path for line,path in zip(lines[:2],("/var/lib/postgresql/data","/var/lib/postgresql/data/pg_wal"))) and len(lines[2]) == 4 and lines[2][0] == "temporary_files" and lines[2][2] == "temporary_bytes" and lines[2][1].isdigit() and lines[2][3].isdigit(), "boundary_disk_shape")
    return parsed


def boundary(container, output, marker):
    evidence_gate()
    stem = "boundary-%03d" % marker["sequence"]
    sql = checked(["docker", "exec", container, "psql", "-U", "postgres", "-d", "cutmy_storage005", "-At", "-v", "ON_ERROR_STOP=1", "-c", BOUNDARY_SQL], output, stem + "-sql", 10, cap=1024*1024)
    disk = checked(["docker", "exec", container, "bash", "-eu", "-o", "pipefail", "-c", BOUNDARY_DISK], output, stem + "-disk", 10, cap=65536)
    validate_boundary(sql,disk)
    evidence_gate()
    return {"sql_sha256":sha(sql),"disk_sha256":sha(disk),"host_disk_free_bytes":shutil.disk_usage(output).free}


def main():
    global ARTIFACT_ROOT, PRIVATE_ROOT
    require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and os.environ.get("GITHUB_REPOSITORY") == "Kay0k1/cutmyvideo-core", "hosted_dispatch_only")
    require(platform.system() == "Linux" and platform.machine() == "x86_64" and os.environ.get("RUNNER_OS") == "Linux", "hosted_platform")
    identity = validate_identity(loads((WORKSPACE / "benchmarks/storage-normal-capacity/identity.json").read_bytes()))
    require(all(PINS["runtime"][key] == identity["versions"]["after"][key] for key in ("commit","tree","parents","schema","application_inventory_sha256","selector_sql_sha256")) and PINS["runtime"]["application_count"] == len(identity["versions"]["after"]["application_files"]) == 173 and PINS["runtime"]["source_count"] == len(identity["versions"]["after"]["source_files"]) == 391, "runtime_inventory_pin")
    require(PINS["sizes"] == [10000] and PINS["runtime"]["commit"] == "d594cc4681dc1cab61664cec8c92bdf9a3e9444c", "current10000_only")
    sizes = [10000]
    output = Path(os.environ["RUNNER_TEMP"]).resolve() / "storage-normal-memory" / "once"
    output.mkdir(mode=0o700)
    ARTIFACT_ROOT = output
    summary = {"format": 1, "kind": "storage-normal-memory-current10000", "complete": False, "pass": False, "versions": {}, "workloads": [], "container_cleanup": False, "failure_code": None, "dispatch": {key: os.environ.get(key) for key in ("GITHUB_SHA", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_WORKFLOW_REF", "GITHUB_REPOSITORY")}, "identity_sha256": sha((HERE / "identity.json").read_bytes()), "fixture_sha256": sha((HERE / "pilot_test.go.txt").read_bytes())}
    summary.update(maximum_size=10000, promotion_ready=False, diagnostic_only=True, memory_observer=None)
    observer = None
    container = "cutmy-storage-memory-" + secrets.token_hex(8)
    volume = container + "-data"
    created = False
    PRIVATE_ROOT = Path(os.environ["RUNNER_TEMP"]).resolve() / "storage-normal-memory-private"
    require(not os.path.lexists(PRIVATE_ROOT), "fresh_private_root")
    private_env = PRIVATE_ROOT / "postgres.env"
    checkout = WORKSPACE / ".comparison" / "after"
    injected = checkout / "internal/app/storage_normal_capacity_pilot_test.go"
    require(not os.path.lexists(private_env) and not os.path.lexists(injected), "fresh_private_and_fixture")
    exclusive(output / "owned-resources.json", canonical({"format":1,"container":container,"volume":volume,
        "owner_label":OWNER_LABEL,"run_id":os.environ["GITHUB_RUN_ID"],"attempt":1,
        "tooling_commit":os.environ["GITHUB_SHA"],"checkout":str(checkout),
        "injected_fixture":str(injected),"private_env":str(private_env),
        "private_root":str(PRIVATE_ROOT),"binary":str(PRIVATE_ROOT/"after.test"),"fixture_sha256":sha((HERE/"pilot_test.go.txt").read_bytes())}))
    def interrupted(signum, frame):
        INTERRUPTED.set()
        REFUSED.set()
        summary["pass"] = False
    previous = {number:signal.signal(number, interrupted) for number in (signal.SIGINT,signal.SIGTERM)}
    try:
        PRIVATE_ROOT.mkdir(mode=0o700)
        admission(output)
        go_version = checked(["go", "version"], output, "go-version").decode().strip()
        require(go_version == "go version go1.27.2 linux/amd64", "go_toolchain")
        summary["go_version"] = go_version
        for label in ("after",):
            checkout = WORKSPACE / ".comparison" / label
            summary["versions"][label] = {"before": inventory(checkout, identity, label, output, "before-injection")}
            exclusive(checkout / "internal/app/storage_normal_capacity_pilot_test.go", (HERE / "pilot_test.go.txt").read_bytes())
            admission(output)
            binary = PRIVATE_ROOT / (label + ".test")
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
        checked(["docker","volume","create","--name",volume,"--label",OWNER_LABEL+"="+container],output,"volume-create",30)
        checked(["docker", "create", "--name", container, "--label", OWNER_LABEL + "=" + container, "--platform", "linux/amd64", "--cpus", "2", "--memory", "1g", "--memory-swap", "1g", "--env-file", str(private_env), "--mount", "type=volume,source="+volume+",target=/var/lib/postgresql/data", "-p", "127.0.0.1::5432", identity["postgres"]["image"], "-c", "shared_buffers=128MB", "-c", "max_connections=32", "-c", "work_mem=4MB", "-c", "jit=off", "-c", "fsync=on", "-c", "synchronous_commit=on"], output, "container-create", 180)
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
            summary["promotion_ready"] = False
            for label in ("after",):
                require(time.monotonic() - timed_start < 30 * 60 - 420, "aggregate_workload_budget")
                admission(output)
                prefix = label + "-" + str(size)
                stage = output / prefix
                stage.mkdir(mode=0o700)
                observed_image = container_identity(container, output, prefix)
                binding_raw = checked(["docker", "inspect", "--format", "{{.Id}} {{.State.Pid}}", container], output, "owned-cgroup-container")
                binding = bind_cgroup(binding_raw)
                markers = stage / "markers"
                markers.mkdir(mode=0o700)
                observer = Observer(binding, stage, markers, lambda marker: boundary(container, output, marker))
                observer.start()
                env = dict(os.environ, TEST_DATABASE_URL=database, CUTMY_STORAGE_NORMAL_PILOT="1", CUTMY_STORAGE_NORMAL_SIZE=str(size), CUTMY_STORAGE_NORMAL_SCHEMA=str(identity["versions"][label]["schema"]), CUTMY_STORAGE_NORMAL_SQL_SHA256=identity["versions"][label]["selector_sql_sha256"], CUTMY_STORAGE_REPORT_DIR=str(stage), GOMAXPROCS="2", GOMEMLIMIT="128MiB", GOTOOLCHAIN="local", CUTMY_STORAGE_MEMORY_MARKERS=str(markers))
                # Only the precompiled binary is run within the workload window.
                command = ["/usr/bin/time", "-v", "-o", str(stage / "process-usage.txt"), "go", "tool", "test2json", "-t", "-p", "github.com/Kay0k1/cutmyvideo-core/internal/app", str(PRIVATE_ROOT / (label + ".test")), "-test.run=^TestStorageNormalCapacityPilot$", "-test.count=1", "-test.timeout=6m", "-test.v=test2json"]
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
                observer.stop()
                summary["memory_observer"] = observer.result()
                exclusive(output / "observer-final.json", canonical(summary["memory_observer"]))
                require(summary["memory_observer"]["pass"], "memory_observer_refused")
                snapshot = inventory(WORKSPACE / ".comparison" / label, identity, label, output, prefix + "-after")
                post_image = container_identity(container, output, prefix + "-after")
                require(observed_image == post_image, "postgres_identity_changed")
                if summary["workloads"]:
                    require(report["postgresql_settings"] == summary["workloads"][0]["postgresql_settings"] and observed_image == summary["workloads"][0]["image"], "unmatched_environment")
                summary["workloads"].append({"version": label, "size": size, "report": prefix + "/pilot.json", "report_sha256": sha((stage / "pilot.json").read_bytes()), "go_json": prefix + "/go-events.stdout", "go_json_sha256": sha(raw), "go_exit": result["exit_code"], "verification": verification, "image": observed_image, "postgresql_settings": report["postgresql_settings"], "post_source": snapshot, "namespace_role_cleanup": True, "postgres_cgroup_cumulative_peak_bytes": int(peak), "process_usage": prefix + "/process-usage.txt"})
            summary["promotion_ready"] = False
        summary["complete"] = True
        summary["pass"] = all(w["verification"]["workload_pass"] for w in summary["workloads"])
    except (Refused, OSError, ValueError, KeyError, TypeError) as error:
        summary["failure_code"] = str(error) if isinstance(error, Refused) else "pilot_operation_failed"
    finally:
        if observer is not None and observer.active:
            observer.stop()
            summary["memory_observer"] = observer.result()
        if private_env.exists():
            private_env.unlink()
        if created:
            summary["container_cleanup_begin"] = {"utc":time.time_ns(), "monotonic_ns":time.monotonic_ns(), "sampler_intentionally_stopped_before_owned_cgroup_removal":True}
            cleanup_deadline=time.monotonic()+30
            cleanup_ok=True
            for kind,name in (("container",container),("volume",volume)):
                try:
                    def remaining_budget():
                        require(cleanup_deadline-time.monotonic() > 1,"cleanup_total30s")
                        return min(5,cleanup_deadline-time.monotonic())
                    inspect=["docker","inspect","--type","container","--format","{{json .Config.Labels}}",name] if kind == "container" else ["docker","volume","inspect","--format","{{json .Labels}}",name]
                    result,owner=capture(inspect,output,kind+"-cleanup-owner",remaining_budget(),cleanup=True)
                    if result["exit_code"] == 0:
                        require(loads(owner).get(OWNER_LABEL) == container,"exact_cleanup_owner")
                        remove=["docker","rm","--force","--volumes",name] if kind == "container" else ["docker","volume","rm",name]
                        checked(remove,output,kind+"-cleanup",remaining_budget(),cleanup=True)
                    else:
                        require(result["exit_code"] == 1,"cleanup_inspect_outcome")
                    absent=["docker","ps","--all","--filter","name=^/"+name+"$","--format","{{.ID}}"] if kind == "container" else ["docker","volume","ls","--filter","name=^"+name+"$","--format","{{.Name}}"]
                    require(not checked(absent,output,kind+"-remaining",remaining_budget(),cleanup=True).strip(),"owned_resource_absence")
                except (OSError,Refused):
                    cleanup_ok=False
            summary["container_cleanup"]=cleanup_ok and time.monotonic() <= cleanup_deadline
            summary["container_cleanup_end"]={"utc":time.time_ns(),"monotonic_ns":time.monotonic_ns(),"owned_container_and_volume_absent":summary["container_cleanup"]}
        else:
            summary["container_cleanup"] = True
        summary["commands"] = COMMANDS
        summary["resource_admissions"] = ADMISSIONS
        summary["pass"] = summary["pass"] and summary["container_cleanup"] and bool(summary["memory_observer"] and summary["memory_observer"]["pass"])
        summary["promotion_ready"] = summary["promotion_ready"] and summary["container_cleanup"]
        # Executable files are not needed in the downloadable measurement
        # archive. Their recorded hashes remain, and original raw logs stay.
        for label in ("after",):
            binary = PRIVATE_ROOT / (label + ".test")
            if binary.exists():
                binary.unlink()
        if injected.exists():
            require(not injected.is_symlink() and injected.read_bytes() == (HERE/"pilot_test.go.txt").read_bytes(), "owned_fixture_cleanup")
            injected.unlink()
        if PRIVATE_ROOT.exists():
            PRIVATE_ROOT.rmdir()
        summary["interrupted"] = INTERRUPTED.is_set()
        summary["refused"] = REFUSED.is_set()
        summary["pass"] = summary["pass"] and not REFUSED.is_set()
        for number, handler in previous.items():
            signal.signal(number, handler)
        total_bytes = sum(p.stat().st_size for p in output.rglob("*") if p.is_file())
        if total_bytes > 256 * 1024 * 1024:
            summary["pass"] = False
            summary["failure_code"] = "aggregate_evidence_cap"
        summary["catalog"] = {str(p.relative_to(output)): {"bytes": p.stat().st_size, "sha256": sha(p.read_bytes())} for p in sorted(output.rglob("*")) if p.is_file()}
        exclusive(output / "summary.json", canonical(summary))
    print(json.dumps({key: summary[key] for key in ("complete", "pass", "maximum_size", "failure_code", "container_cleanup")}, sort_keys=True))
    return 0 if summary["pass"] and not REFUSED.is_set() else 1


if __name__ == "__main__":
    raise SystemExit(main())
