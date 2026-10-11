#!/usr/bin/env python3
"""One approved Go/PG query experiment; source preparation alone is not authority."""
import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time

PREP = pathlib.Path(__file__).resolve().parent
EXPECTED_PLAN = "c29d65934d88afcc02dcc4582bff1d24f291a9450ca2dd0b2c97d3df78e1c26a"
LABEL = "com.cutmy.test.project"
USER = "cutmy_ttl"
DATABASE = "cutmy_ttl_test"
PASSWORD = "test-only"
INJECTED = "internal/app/ttl_preview_probe_measurement_test.go"
INSPECT = '{"id":{{json .Id}},"image":{{json .Image}},"labels":{{json .Config.Labels}},"memory":{{json .HostConfig.Memory}},"swap":{{json .HostConfig.MemorySwap}},"nanocpus":{{json .HostConfig.NanoCpus}},"pids":{{json .HostConfig.PidsLimit}},"mounts":{{json .Mounts}},"tmpfs":{{json .HostConfig.Tmpfs}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .HostConfig.PortBindings}},"oom":{{json .State.OOMKilled}}}'


def sha(path):
    return hashlib.sha256(pathlib.Path(path).read_bytes()).hexdigest()


def require(value, reason):
    if not value:
        raise RuntimeError(reason)


def write(path, value):
    with pathlib.Path(path).open("x") as target:
        json.dump(value, target, indent=2, sort_keys=True)
        target.write("\n")


class Capture:
    """Retained owned-process pattern, with capped pipe writes and explicit reap."""
    def __init__(self, output, whole_seconds):
        self.output = output
        self.started = time.monotonic()
        self.whole_seconds = whole_seconds
        self.commands = []
        self.total = 0
        self.cleanup_started = None
        self.lock = threading.Lock()
        self.baseenv = {k: v for k, v in os.environ.items() if k in ("PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "SSL_CERT_FILE", "SSL_CERT_DIR")}
        self.baseenv.update(LANG="C.UTF-8", LC_ALL="C.UTF-8")

    def run(self, stage, argv, timeout=30, allow=(), env=None, cwd=None, cleanup=False, per_stream=2*1024**2, extra_tree=None):
        remaining = self.whole_seconds - (time.monotonic() - self.started)
        require(cleanup or remaining > 0, "whole_runner_deadline")
        if not cleanup:
            timeout = min(timeout, remaining)
        else:
            if self.cleanup_started is None:
                self.cleanup_started = time.monotonic()
            cleanup_remaining = 120-(time.monotonic()-self.cleanup_started)
            require(cleanup_remaining > 0, "cleanup_deadline")
            timeout = min(timeout, cleanup_remaining)
        shown = [arg.replace("POSTGRES_PASSWORD="+PASSWORD, "POSTGRES_PASSWORD=<test-fixture-redacted>").replace("PGPASSWORD="+PASSWORD, "PGPASSWORD=<test-fixture-redacted>") for arg in argv]
        record = {"stage": stage, "argv": shown, "timeout_seconds": timeout, "per_stream_cap_bytes": per_stream, "cwd": str(cwd) if cwd else None}
        write(self.output/(stage+".invocation.json"), record)
        paths = [self.output/(stage+".stdout"), self.output/(stage+".stderr")]
        counters = [0, 0]
        overflow = threading.Event()
        stream_errors = []
        begun = time.monotonic()
        process = None
        reason = None
        with paths[0].open("xb") as stdout, paths[1].open("xb") as stderr:
            process = subprocess.Popen(argv, stdout=subprocess.PIPE, stderr=subprocess.PIPE, env=env or self.baseenv, cwd=cwd, start_new_session=True)
            record["pid"] = process.pid
            def pump(source, target, index):
                try:
                    while block := source.read(65536):
                        with self.lock:
                            aggregate_limit = (96 if cleanup else 92)*1024**2
                            usable = min(len(block), per_stream-counters[index], aggregate_limit-self.total)
                            if usable > 0:
                                target.write(block[:usable])
                                counters[index] += usable
                                self.total += usable
                            if usable != len(block):
                                overflow.set()
                                break
                except (OSError, ValueError) as error:
                    stream_errors.append(type(error).__name__)
                    overflow.set()
            threads = [threading.Thread(target=pump, args=(source, target, index), daemon=True) for index, (source, target) in enumerate(zip((process.stdout, process.stderr), (stdout, stderr)))]
            for thread in threads:
                thread.start()
            while process.poll() is None:
                if time.monotonic()-begun >= timeout:
                    reason = "command_deadline"
                if overflow.is_set():
                    reason = "command_output_limit"
                if extra_tree and extra_tree.exists():
                    if sum(path.stat().st_size for path in extra_tree.iterdir() if path.is_file()) > 16*1024**2:
                        reason = "measurement_output_limit"
                if reason:
                    break
                time.sleep(.05)
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=2)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=5)
            code = process.wait(timeout=5)
            # Descendants cannot retain pipes after the direct child exits.
            try:
                os.killpg(process.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            for thread in threads:
                thread.join(timeout=3)
            if any(thread.is_alive() for thread in threads):
                reason = "stream_not_closed"
            if overflow.is_set():
                reason = reason or "command_output_limit"
            if stream_errors:
                reason = reason or "stream_failed"
            # A final write can happen after the last running-child poll, also
            # when the Go test exits with FAIL; the same cap covers every exit.
            if extra_tree and extra_tree.exists():
                if sum(path.stat().st_size for path in extra_tree.iterdir() if path.is_file()) > 16*1024**2:
                    reason = "measurement_output_limit"
        record.update(exit=code, reaped=True, elapsed_seconds=time.monotonic()-begun, refusal=reason, stream_errors=stream_errors, stdout_bytes=paths[0].stat().st_size, stderr_bytes=paths[1].stat().st_size, stdout_sha256=sha(paths[0]), stderr_sha256=sha(paths[1]))
        self.commands.append(record)
        write(self.output/(stage+".result.json"), record)
        require(reason is None, stage+":"+str(reason))
        require(code == 0 or code in allow, stage+":exit_"+str(code))
        return paths[0].read_text(errors="strict"), code


def inventory(capture, plan, root, stage, injected=False, source=False, cleanup=False):
    expected = json.loads((PREP/"source-map.json").read_text())
    names, _ = capture.run(stage+"-git-files", ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=root, cleanup=cleanup)
    allowed = set(expected) | ({INJECTED} if injected else set())
    require(set(names.split("\0"))-{ "" } == allowed, "source_file_set:"+stage)
    observed = {}
    for name, wanted in expected.items():
        path = root/name
        require(path.is_file() and not path.is_symlink(), "source_file_type:"+name)
        require(path.stat().st_size == wanted["bytes"] and sha(path) == wanted["sha256"] and stat.S_IMODE(path.stat().st_mode) == int(wanted["mode"][-3:], 8), "source_file_bytes_or_mode:"+name)
        observed[name] = {"bytes": path.stat().st_size, "sha256": sha(path), "posix_mode": stat.S_IMODE(path.stat().st_mode)}
    if injected:
        require(sha(root/INJECTED) == plan["inputs"]["measurement_test.go.txt"]["sha256"], "injected_fixture_bytes")
    head, _ = capture.run(stage+"-head", ["git", "rev-parse", "HEAD"], cwd=root, cleanup=cleanup)
    tree, _ = capture.run(stage+"-tree", ["git", "rev-parse", "HEAD^{tree}"], cwd=root, cleanup=cleanup)
    parent, _ = capture.run(stage+"-parent", ["git", "show", "-s", "--format=%P", "HEAD"], cwd=root, cleanup=cleanup)
    branch, _ = capture.run(stage+"-branch", ["git", "branch", "--show-current"], cwd=root, cleanup=cleanup)
    status, _ = capture.run(stage+"-status", ["git", "status", "--porcelain=v1", "--untracked-files=all"], cwd=root, cleanup=cleanup)
    require((head.strip(), tree.strip(), parent.strip()) == (plan["base"], plan["tree"], plan["parent"]), "git_runtime_identity")
    require(branch.strip() in (plan["source_branch"], "") if source else branch.strip() == "", "runtime_branch_identity")
    require(status == plan["status"] + ("?? "+INJECTED+"\n" if injected else ""), "runtime_status_identity")
    result = {"head": head.strip(), "tree": tree.strip(), "parent": parent.strip(), "branch": branch.strip(), "status": status, "files": observed, "injected_fixture_sha256": sha(root/INJECTED) if injected else None}
    write(capture.output/(stage+"-source.json"), result)
    return result


def admission(output):
    memory = re.search(r"^MemAvailable:\s+(\d+) kB$", pathlib.Path("/proc/meminfo").read_text(), re.M)
    require(memory is not None, "host_memory_evidence")
    result = {"available_memory_bytes": int(memory.group(1))*1024, "free_disk_bytes": shutil.disk_usage(output).free, "host_cpus": os.cpu_count() or 0}
    require(result["available_memory_bytes"] >= 3*1024**3 and result["free_disk_bytes"] >= 2*1024**3 and result["host_cpus"] >= 2, "resource_admission")
    return result


def validate_measurement(output, plan):
    directory = output/"measurement"
    require(directory.is_dir() and not directory.is_symlink(), "measurement_directory")
    report_path = directory/"paired-report.json"
    require(report_path.is_file() and report_path.stat().st_size <= 1024**2, "measurement_report_file")
    report = json.loads(report_path.read_text())
    require(report["kind"] == "ttl-preview-probe-paired-schema005" and report["complete"] is True and report["pass"] is True and report["test_failed"] is False and report["failure"] is None and report["schema"] == 5, "measurement_report_not_pass")
    require((report["pairs"], report["samples_per_query"], report["retained_sources"], report["expired_sources"]) == (20,20,1000,200), "measurement_dimensions")
    expected_settings = {"server_version_num":"170011", "shared_buffers":"128MB", "work_mem":"4MB", "max_connections":"32", "jit":"off", "fsync":"on", "synchronous_commit":"on"}
    require(all(report["postgresql_settings"].get(k) == v for k,v in expected_settings.items()), "measurement_settings")
    role = report["runtime_role"]
    require(role["pass"] is True and role["superuser"] is False and role["schema_create"] is False and role["migration_ledger_insert"] is False and role["ordinary_table_dml"] is True and role["function_execute_granted"] is False, "measurement_role")
    expected_files = {"baseline.sql", "candidate.sql", "initial-snapshot.json", "paired-report.json"}
    hashes = {"baseline":plan["inputs"]["baseline-f80-source-retention.sql"]["sha256"], "candidate":plan["inputs"]["candidate-source-retention.sql"]["sha256"]}
    require(report["baseline_sql_sha256"] == hashes["baseline"] and report["candidate_sql_sha256"] == hashes["candidate"], "report_sql_identity")
    require(sha(directory/"baseline.sql") == hashes["baseline"] and sha(directory/"candidate.sql") == hashes["candidate"], "saved_sql_identity")
    require(sha(directory/"initial-snapshot.json") == report["initial_snapshot_sha256"], "initial_snapshot_identity")
    require(report["initial_accounting"] == report["final_accounting"], "final_accounting_identity")
    samples = report["samples"]
    require(len(samples) == 40, "actual_40_samples_required")
    for index, sample in enumerate(samples):
        pair = index//2+1
        order = ("baseline", "candidate") if pair%2 else ("candidate", "baseline")
        label = order[index%2]
        require(sample["ordinal"] == index+1 and sample["pair"] == pair and sample["query"] == label and sample["sql_sha256"] == hashes[label], "sample_order_or_sql")
        require(sample["rollback_acknowledged"] is True and sample["retained_pinned_remaining"] == 1000 and sample["expired_remaining"] == 0 and sample["accounting"] == report["initial_accounting"], "sample_rollback_or_rows_or_accounting")
        require(isinstance(sample["fence_elapsed_ns"], int) and sample["fence_elapsed_ns"] >= 0 and isinstance(sample["client_elapsed_ns"], int) and sample["client_elapsed_ns"] >= 0, "actual_sample_times")
        plan_name = "plan-%02d-%s.json"%(index+1,label)
        snapshot_name = "snapshot-%02d-%s.json"%(index+1,label)
        require(sample["plan_file"] == plan_name and sample["persisted_snapshot_file"] == snapshot_name, "sample_file_names")
        expected_files.update((plan_name, snapshot_name))
        for name, digest in ((plan_name,sample["plan_sha256"]), (snapshot_name,sample["persisted_snapshot_sha256"])):
            path = directory/name
            require(path.is_file() and not path.is_symlink() and path.stat().st_size <= 1024**2 and sha(path) == digest, "sample_original_file_identity")
        require(sample["persisted_snapshot_sha256"] == report["initial_snapshot_sha256"], "per_sample_snapshot_drift")
        original = json.loads((directory/plan_name).read_text())
        require(isinstance(original,list) and len(original) == 1 and "Plan" in original[0] and original[0]["Execution Time"] == sample["postgres_execution_ms"], "original_explain_binding")
    require({p.name for p in directory.iterdir()} == expected_files and sum(p.stat().st_size for p in directory.iterdir()) <= 16*1024**2, "measurement_file_set_or_cap")
    return {"pass":True, "actual_samples":40, "actual_pairs":20, "report_sha256":sha(report_path), "timings_are_query_only":True, "speedup_claim":None}


def prepare_owned_module_cache_removal(output, directory):
    require(output.is_dir() and not output.is_symlink() and directory.parent == output
            and directory.name == "module-cache" and directory.is_dir()
            and not directory.is_symlink(), "owned_module_cache_removal_path")
    require(getattr(shutil.rmtree, "avoids_symlink_attacks", False), "safe_cache_removal_unavailable")

    def grant_directory(path):
        identity = path.lstat()
        require(stat.S_ISDIR(identity.st_mode) and identity.st_uid == os.geteuid(),
                "owned_module_cache_directory")
        os.chmod(path, stat.S_IMODE(identity.st_mode) | 0o700, follow_symlinks=False)

    def refuse_walk_error(error):
        raise error

    grant_directory(directory)
    for current, subdirectories, _ in os.walk(directory, topdown=True, followlinks=False,
                                               onerror=refuse_walk_error):
        current = pathlib.Path(current)
        require(current == directory or directory in current.parents, "module_cache_walk_scope")
        for name in subdirectories:
            grant_directory(current/name)


def main():
    parser = argparse.ArgumentParser()
    for name in ("authorization", "worktree", "output", "go"):
        parser.add_argument("--"+name, required=True)
    args = parser.parse_args()
    require(sha(PREP/"execution-plan.json") == EXPECTED_PLAN, "execution_plan_identity")
    plan = json.loads((PREP/"execution-plan.json").read_text())
    authority = json.loads(pathlib.Path(args.authorization).read_text())
    require(authority.get("authorized_once") is True and authority.get("kind") == "ttl-preview-offset-probe-PG16-once", "actual_ROOT_once_authorization_required")
    require(authority.get("plan_sha256") == EXPECTED_PLAN and authority.get("runner_sha256") == sha(__file__) and authority.get("source_map_sha256") == plan["inputs"]["source-map.json"]["sha256"] and authority.get("fixture_sha256") == plan["inputs"]["measurement_test.go.txt"]["sha256"], "authorization_exact_source_binding")
    for name, identity in plan["inputs"].items():
        path = PREP/name
        require(path.is_file() and not path.is_symlink() and path.stat().st_size == identity["bytes"] and sha(path) == identity["sha256"], "prepared_input_identity:"+name)
    source = pathlib.Path(args.worktree).resolve()
    output = pathlib.Path(args.output).resolve()
    go = pathlib.Path(args.go).resolve()
    require(source.is_dir() and not output.exists() and sha(go) == plan["go_sha256"], "fresh_output_and_go_identity")
    mode = authority.get("execution_mode")
    if mode == "local":
        require(str(source) == authority.get("worktree") and str(output) == authority.get("output"), "authorized_local_paths")
    elif mode == "github-actions":
        require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch" and os.environ.get("GITHUB_REPOSITORY") == plan["repository"] and os.environ.get("GITHUB_RUN_ATTEMPT") == "1", "actual_hosted_dispatch_context")
        require(os.environ.get("GITHUB_SHA") == authority.get("harness_commit") and os.environ.get("GITHUB_REF") == "refs/heads/"+authority.get("harness_branch", ""), "authorized_hosted_head_and_branch")
        require(source == pathlib.Path(os.environ["GITHUB_WORKSPACE"])/"ttl-candidate" and output == pathlib.Path(os.environ["RUNNER_TEMP"])/"ttl-preview-offset-probe-once", "authorized_hosted_paths")
    else:
        raise RuntimeError("authorized_execution_mode")
    require(output != source and source not in output.parents, "output_outside_source")
    output.mkdir(mode=0o700)
    write(output/"ROOT-once-authorization.json", authority)
    capture = Capture(output, plan["whole_runner_timeout_seconds"])
    project = "cutmy-ttl-preview-offset-"+os.urandom(8).hex()
    network = project+"-network"
    container = project+"-db"
    network_id = container_id = None
    runtime = output/"runtime"
    runtime_attempted = False
    runtime_before = None
    before = None
    failure = None
    cleanup_errors = []
    tests = measurement = None
    go_exit = None
    psql = None
    try:
        write(output/"resource-admission-before.json", admission(output))
        before = inventory(capture, plan, source, "source-before", source=True)
        actual = []
        for name in ("source_retention_test.go", "retention_test.go"):
            actual.extend(re.findall(r"^func (Test(?:SourcePin|SourceRetention|Retention)\w+)\(t \*testing.T\)", (source/"internal/app"/name).read_text(), re.M))
        require(actual == plan["semantic_test_names"] and len(actual) == 15 and len(set(actual)) == 15, "exact_15_test_source_identity")
        version, _ = capture.run("go-version", [str(go), "version"])
        require(version.strip() == "go version go1.27.2 linux/amd64", "actual_go_version")
        require(not runtime.exists(), "fresh_runtime_path")
        runtime_attempted = True
        capture.run("runtime-worktree-create", ["git", "-C", str(source), "worktree", "add", "--detach", str(runtime), plan["base"]])
        capture.run("candidate-patch-check", ["git", "apply", "--check", str(PREP/"candidate.patch")], cwd=runtime)
        capture.run("candidate-patch-apply", ["git", "apply", str(PREP/"candidate.patch")], cwd=runtime)
        runtime_before = inventory(capture, plan, runtime, "runtime-before-injection")
        cache = output/"go-cache"
        temporary = output/"go-tmp"
        cache.mkdir(mode=0o700)
        temporary.mkdir(mode=0o700)
        env = dict(capture.baseenv, GOCACHE=str(cache), GOTMPDIR=str(temporary), GOMAXPROCS="2", GOMEMLIMIT="128MiB", GOTOOLCHAIN="local", GOWORK="off", GOFLAGS="-mod=readonly", GOPROXY="off", GOSUMDB="off")
        if mode == "local":
            env.update(GOMODCACHE="/root/go/pkg/mod", GOPATH="/root/go")
        else:
            require(authority.get("allow_locked_dependency_download") is True and authority.get("allow_pinned_image_pull") is True, "authorized_hosted_preparation")
            module_cache = output/"module-cache"
            module_cache.mkdir(mode=0o700)
            env.update(GOMODCACHE=str(module_cache), GOPATH=str(output/"gopath"), GOPROXY="https://proxy.golang.org", GOSUMDB="sum.golang.org", GOVCS="*:off")
            capture.run("locked-module-preparation", [str(go), "mod", "download"], timeout=180, env=env, cwd=runtime)
            env.update(GOPROXY="off", GOSUMDB="off")
            capture.run("pinned-image-preparation", ["docker", "pull", "--platform", "linux/amd64", plan["image"]], timeout=180)
        original_image, _ = capture.run("image-original", ["docker", "image", "inspect", plan["image"], "--format", '{"id":{{json .Id}},"digests":{{json .RepoDigests}},"architecture":{{json .Architecture}},"os":{{json .Os}}}'])
        image = json.loads(original_image)
        require(image["id"] == "sha256:"+plan["registry_config"] and image["architecture"] == "amd64" and image["os"] == "linux" and any(x.endswith("@sha256:"+plan["index"]) for x in image["digests"]), "pinned_local_image_identity")
        write(output/"resource-admission-before-pg.json", admission(output))
        raw, _ = capture.run("network-create", ["docker", "network", "create", "--driver", "bridge", "--label", LABEL+"="+project, network])
        network_id = raw.strip()
        require(re.fullmatch("[0-9a-f]{64}", network_id), "owned_network_id")
        tmpfs = "rw,nosuid,nodev,noexec,size=536870912,mode=0700,uid=999,gid=999"
        create = ["docker", "create", "--pull", "never", "--platform", "linux/amd64", "--name", container, "--label", LABEL+"="+project, "--label", "com.cutmy.test.purpose=ttl-preview-probe", "--network", network, "--publish", "127.0.0.1::5432", "--cpus", "2", "--memory", "1g", "--memory-swap", "1g", "--pids-limit", "128", "--tmpfs", "/var/lib/postgresql/data:"+tmpfs, "--env", "POSTGRES_USER="+USER, "--env", "POSTGRES_PASSWORD="+PASSWORD, "--env", "POSTGRES_DB="+DATABASE, plan["image"], "postgres", "-c", "shared_buffers=128MB", "-c", "work_mem=4MB", "-c", "max_connections=32", "-c", "jit=off", "-c", "fsync=on", "-c", "synchronous_commit=on", "-c", "max_wal_size=128MB", "-c", "min_wal_size=32MB"]
        raw, _ = capture.run("container-create", create)
        container_id = raw.strip()
        require(re.fullmatch("[0-9a-f]{64}", container_id), "owned_container_id")
        raw, _ = capture.run("container-original", ["docker", "inspect", "--format", INSPECT, container_id])
        identity = json.loads(raw)
        require(identity["id"] == container_id and identity["image"] == image["id"] and identity["labels"].get(LABEL) == project and identity["memory"] == 1024**3 and identity["swap"] == 1024**3 and identity["nanocpus"] == 2000000000 and identity["pids"] == 128 and identity["network"] == network and identity["oom"] is False, "owned_container_limits")
        require(all(m["Type"] == "tmpfs" for m in identity["mounts"]) and identity["tmpfs"] == {"/var/lib/postgresql/data":tmpfs} and identity["ports"] == {"5432/tcp":[{"HostIp":"127.0.0.1","HostPort":""}]}, "owned_container_mounts_and_loopback")
        capture.run("container-start", ["docker", "start", container_id])
        ready_deadline = time.monotonic()+30
        for index in range(120):
            _, code = capture.run("readiness-%03d"%index, ["docker", "exec", container_id, "pg_isready", "-h", "127.0.0.1", "-U", USER, "-d", DATABASE], timeout=5, allow=(1,2))
            if code == 0:
                break
            require(time.monotonic() < ready_deadline, "postgres_tcp_readiness")
            time.sleep(.2)
        else:
            raise RuntimeError("postgres_tcp_readiness")
        raw, _ = capture.run("container-port", ["docker", "port", container_id, "5432/tcp"])
        port = re.fullmatch(r"127\.0\.0\.1:(\d+)\n?", raw)
        require(port and 1 <= int(port.group(1)) <= 65535, "published_loopback_port")
        psql = ["docker", "exec", "-e", "PGPASSWORD="+PASSWORD, container_id, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", USER, "-d", DATABASE, "-A", "-t", "-c"]
        raw, _ = capture.run("postgres-sql-ready", psql+["SELECT json_build_object('one',1,'version',current_setting('server_version_num'),'shared_buffers',current_setting('shared_buffers'),'work_mem',current_setting('work_mem'),'max_connections',current_setting('max_connections'),'jit',current_setting('jit'),'fsync',current_setting('fsync'),'synchronous_commit',current_setting('synchronous_commit'));"])
        require(json.loads(raw) == {"one":1,"version":"170011","shared_buffers":"128MB","work_mem":"4MB","max_connections":"32","jit":"off","fsync":"on","synchronous_commit":"on"}, "actual_postgres_settings")
        with (runtime/INJECTED).open("xb") as target:
            target.write((PREP/"measurement_test.go.txt").read_bytes())
        inventory(capture, plan, runtime, "runtime-injected", injected=True)
        write(output/"resource-admission-before-go.json", admission(output))
        env.update(TEST_DATABASE_URL=f"postgres://{USER}:{PASSWORD}@127.0.0.1:{port.group(1)}/{DATABASE}?sslmode=disable", CUTMY_TTL_MEASUREMENT_DIR=str(output/"measurement"), CUTMY_TTL_BASELINE_SQL=str(PREP/"baseline-f80-source-retention.sql"))
        names = plan["semantic_test_names"]+["TestTTLPreviewProbeSavedPlans"]
        write(output/"go-once-started.json", {"invocation":1,"names":names,"environment_keys":sorted(env),"tool_sha256":sha(go),"fixture_sha256":sha(runtime/INJECTED)})
        argv = [str(go), "test", "-json", "-race", "-count=1", "-timeout=6m", "-p=1", "-failfast", "./internal/app", "-run", "^("+"|".join(names)+")$"]
        _, go_exit = capture.run("go-test", argv, timeout=420, allow=(1,2), env=env, cwd=runtime, per_stream=32*1024**2, extra_tree=output/"measurement")
        events = [json.loads(line) for line in (output/"go-test.stdout").read_text().splitlines()]
        runs = [event["Test"] for event in events if event.get("Action") == "run" and "Test" in event and "/" not in event["Test"]]
        passes = [event["Test"] for event in events if event.get("Action") == "pass" and "Test" in event and "/" not in event["Test"]]
        bad = [event for event in events if event.get("Action") in ("fail", "skip")]
        tests = {"exit":go_exit,"top_runs":runs,"top_passes":passes,"bad_events":bad,"package_passes":[event for event in events if event.get("Action") == "pass" and "Test" not in event]}
        write(output/"go-test-evidence.json", tests)
        require(go_exit == 0 and sorted(runs) == sorted(names) and sorted(passes) == sorted(names) and not bad and len(tests["package_passes"]) == 1, "targeted_16_tests_not_all_pass")
        measurement = validate_measurement(output, plan)
        write(output/"measurement-validation.json", measurement)
        inventory(capture, plan, runtime, "runtime-after-go", injected=True)
    except BaseException as error:
        failure = str(error) if isinstance(error, RuntimeError) else type(error).__name__
    finally:
        # Ambiguous creation is reconciled by this exact fresh name and label.
        try:
            raw, code = capture.run("container-cleanup-identity", ["docker", "inspect", "--format", INSPECT, container_id or container], allow=(1,), cleanup=True)
            if code == 0:
                owned = json.loads(raw)
                require(owned["labels"].get(LABEL) == project and (container_id is None or owned["id"] == container_id), "cleanup_container_ownership")
                container_id = owned["id"]
                if owned["oom"] is not False:
                    cleanup_errors.append("owned_postgres_oom")
                try:
                    raw, _ = capture.run("postgres-schema-role-cleanup", ["docker", "exec", "-e", "PGPASSWORD="+PASSWORD, container_id, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-U", USER, "-d", DATABASE, "-A", "-t", "-c", "SELECT json_build_object('schemas',(SELECT count(*) FROM pg_namespace WHERE nspname <> 'public' AND nspname <> 'information_schema' AND nspname NOT LIKE 'pg_%'),'roles',(SELECT count(*) FROM pg_roles WHERE rolname <> 'cutmy_ttl' AND rolname NOT LIKE 'pg_%'));"], cleanup=True)
                    db_clean = json.loads(raw)
                    write(output/"postgres-schema-role-evidence.json", db_clean)
                    require(db_clean == {"schemas":0,"roles":0}, "test_database_leftover_namespaces_or_roles")
                except BaseException as error:
                    cleanup_errors.append("database_diagnostic:"+str(error))
                try:
                    capture.run("postgres-original-log", ["docker", "logs", container_id], cleanup=True)
                except BaseException as error:
                    cleanup_errors.append("owned_log:"+str(error))
                try:
                    capture.run("container-stop", ["docker", "stop", "--time", "5", container_id], timeout=15, cleanup=True)
                finally:
                    capture.run("container-remove", ["docker", "rm", "--volumes", container_id], cleanup=True)
        except BaseException as error:
            cleanup_errors.append("container:"+str(error))
        try:
            raw, code = capture.run("network-cleanup-identity", ["docker", "network", "inspect", "--format", '{"id":{{json .Id}},"labels":{{json .Labels}}}', network_id or network], allow=(1,), cleanup=True)
            if code == 0:
                owned = json.loads(raw)
                require(owned["labels"].get(LABEL) == project and (network_id is None or owned["id"] == network_id), "cleanup_network_ownership")
                capture.run("network-remove", ["docker", "network", "rm", owned["id"]], cleanup=True)
        except BaseException as error:
            cleanup_errors.append("network:"+str(error))
        try:
            raw, _ = capture.run("remaining-containers", ["docker", "ps", "-a", "--filter", "label="+LABEL+"="+project, "--format", "{{.ID}}"], cleanup=True)
            raw_network, _ = capture.run("remaining-networks", ["docker", "network", "ls", "--filter", "label="+LABEL+"="+project, "--format", "{{.ID}}"], cleanup=True)
            require(not raw.strip() and not raw_network.strip(), "owned_resources_remain")
        except BaseException as error:
            cleanup_errors.append("resources:"+str(error))
        try:
            if runtime_attempted:
                raw, _ = capture.run("runtime-cleanup-registration", ["git", "-C", str(source), "worktree", "list", "--porcelain", "-z"], cleanup=True)
                registered = str(runtime) in [item.removeprefix("worktree ") for item in raw.split("\0") if item.startswith("worktree ")]
                require(runtime.parent == output and not runtime.is_symlink(), "owned_runtime_cleanup_path")
                write(output/"runtime-cleanup-reconciliation.json", {"attempted":True,"registered":registered,"path_exists":runtime.exists(),"exact_owned_path":str(runtime),"prepared_inventory_saved":runtime_before is not None})
                try:
                    path = runtime/INJECTED
                    if path.exists():
                        require(path.is_file() and not path.is_symlink() and sha(path) == plan["inputs"]["measurement_test.go.txt"]["sha256"], "cleanup_injected_source_identity")
                        path.unlink()
                    if runtime_before is not None:
                        after_runtime = inventory(capture, plan, runtime, "runtime-after-injection-cleanup", cleanup=True)
                        require(after_runtime == runtime_before, "runtime_source_changed")
                finally:
                    if registered:
                        capture.run("runtime-worktree-remove", ["git", "-C", str(source), "worktree", "remove", "--force", str(runtime)], cleanup=True)
                    elif runtime.exists():
                        require(runtime.is_dir() and not runtime.is_symlink(), "owned_partial_runtime_directory")
                        shutil.rmtree(runtime)
                    require(not runtime.exists(), "runtime_worktree_remains")
                    raw, _ = capture.run("runtime-cleanup-registration-after", ["git", "-C", str(source), "worktree", "list", "--porcelain", "-z"], cleanup=True)
                    require(str(runtime) not in [item.removeprefix("worktree ") for item in raw.split("\0") if item.startswith("worktree ")], "runtime_worktree_registration_remains")
        except BaseException as error:
            cleanup_errors.append("runtime_identity:"+str(error))
        try:
            if before is not None:
                after = inventory(capture, plan, source, "source-after", source=True, cleanup=True)
                require(after == before and sha(go) == plan["go_sha256"], "source_or_tool_changed")
        except BaseException as error:
            cleanup_errors.append("source_identity:"+str(error))
        try:
            for name in ("go-cache", "go-tmp", "module-cache", "gopath"):
                directory = output/name
                if directory.exists():
                    require(directory.is_dir() and not directory.is_symlink() and directory.parent == output, "owned_cache_directory")
                    if name == "module-cache":
                        prepare_owned_module_cache_removal(output, directory)
                    shutil.rmtree(directory)
                    require(not directory.exists(), "owned_cache_remains")
            write(output/"ephemeral-cache-cleanup.json", {"removed":True})
        except BaseException as error:
            cleanup_errors.append("ephemeral_cache:"+str(error))
        result = {"status":"PASS" if failure is None and not cleanup_errors else "FAIL", "failure":failure,"go_exit":go_exit,"expected_top_tests":16,"tests":tests,"measurement":measurement,"cleanup_errors":cleanup_errors,"project":project,"network_id":network_id,"container_id":container_id,"commands":capture.commands,"elapsed_seconds":time.monotonic()-capture.started,"speedup_claim":None,"historical_scan_failure_is_unmodified":True}
        write(output/"summary.json", result)
        excluded = {"go-cache","go-tmp","module-cache","gopath","runtime"}
        catalog = {str(path.relative_to(output)):{"bytes":path.stat().st_size,"sha256":sha(path)} for path in sorted(output.rglob("*")) if path.is_file() and not any(part in excluded for part in path.relative_to(output).parts)}
        write(output/"raw-catalog.json", catalog)
        print(json.dumps({"status":result["status"],"failure":failure,"go_exit":go_exit,"cleanup_errors":cleanup_errors,"summary":str(output/"summary.json"),"summary_sha256":sha(output/"summary.json"),"raw_catalog_sha256":sha(output/"raw-catalog.json")}))
        return 0 if result["status"] == "PASS" else 1


if __name__ == "__main__":
    sys.exit(main())
