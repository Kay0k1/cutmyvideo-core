"""One reviewed hosted populated migration; importing this module starts no work."""
import argparse
import ctypes
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import threading
import time

HERE = Path(__file__).resolve().parent
RUNTIME = "d594cc4681dc1cab61664cec8c92bdf9a3e9444c"
SOURCE_SHA = "75b7b4d2dcf72fce4f96dbaa2722bc26f6021f7b1f4f5705d5ac763dcc69ef14"
APP_SHA = "d1ae273e0d56212ef549f029970cd4ea1f33276faa7e4e11c88127e3aeeacef6"
FREEZE_SHA = "b894f40281a5424ab8bd29e0b471b19f63d5f6baeea7ea58407384f69e216613"
RUNNER_SHA = "cae554523645aeb4a70e3ec224ed76a3dff70a93fb51362971c17f4cb2bf799e"
FIXTURE_SHA = "ffe4a7420629e5590a3325867e068eae9ca3dd4c50a997927ac501fd5ab2e0be"
IDENTITY_SHA = "d7beba6f0231160788265f797601ae1b29c7d35977e8cdec210371a2c7a28ae8"
REVIEW_SHAS = {"1000": "f048d1bbb5c50ff979f3808683ff8957f093d829ce2cf220e7d0074346a9d3e2",
               "10000": "e74a392458309de6f459f57b4542bee310726e9a8c5fb3e6f19501c735dd3aa8"}
REVIEW_BYTES = {'1000': b'{\n  "10000_authorized_pending_peer": true,\n  "GETs": [\n    {\n      "elapsed_seconds": 0.4025365770794451,\n      "name": "original-artifact-api",\n      "pid": 458967\n    },\n    {\n      "elapsed_seconds": 1.4601336000487208,\n      "name": "original-provider-log-zip",\n      "pid": 458989\n    },\n    {\n      "elapsed_seconds": 1.4567162690218538,\n      "name": "original-normal1000-artifact",\n      "pid": 459111\n    }\n  ],\n  "POSTs": 0,\n  "SLA_claim": false,\n  "artifact_binaries_present": false,\n  "artifact_bytes": 496636,\n  "artifact_sha256": "c8e8622e8bceaee9fe3b20b324f5adba8a3de2a224db7ea8b1bd2fa6dee989ba",\n  "attempt": 1,\n  "binary_execution_in_this_review": 0,\n  "head": "e7bf76434c593fda7cef25a07eb3f209e919f1b6",\n  "observations": [\n    {\n      "committed_paths": 2024,\n      "live_ack": true,\n      "physical_healthy_files": 990,\n      "reserved_bytes": 3200,\n      "stage": "normal_maintenance",\n      "stored_bytes": 25056,\n      "version": "before",\n      "wall_ms": 367.703\n    },\n    {\n      "committed_paths": 2024,\n      "live_ack": true,\n      "physical_healthy_files": 990,\n      "reserved_bytes": 3200,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 31616,\n      "version": "before",\n      "wall_ms": 178.395\n    },\n    {\n      "committed_paths": 2024,\n      "live_ack": true,\n      "physical_healthy_files": 990,\n      "reserved_bytes": 3200,\n      "stage": "normal_maintenance",\n      "stored_bytes": 25056,\n      "version": "after",\n      "wall_ms": 765.323\n    },\n    {\n      "committed_paths": 2024,\n      "live_ack": true,\n      "physical_healthy_files": 990,\n      "reserved_bytes": 3200,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 31616,\n      "version": "after",\n      "wall_ms": 173.17\n    }\n  ],\n  "old_5ms_experiment_relabelled": false,\n  "original_files": 164,\n  "original_manifest_sha256": "8da3e9591be800d98aada836e638247e9b21749138fd9ba081fc7c49f5abe412",\n  "overall_speedup_claim": false,\n  "provider_jobs_success": 2,\n  "pure_verifier": {\n    "100000_authorized": false,\n    "context_sha256": "18d4864fea884133f4ce633848ccecf862e6b6d377de575a944306b90b7d8f05",\n    "failed_workloads": [],\n    "format": 1,\n    "measurement_evidence_verified": true,\n    "pairs": [\n      1000\n    ],\n    "performance_claim": false,\n    "promotion_ready": true,\n    "source_inventory_files": {\n      "after": 173,\n      "before": 164\n    },\n    "summary_sha256": "ac3229bf07eef7910681a35481f6336c0c20619d1adc71f231a98813075faf33",\n    "workload_pass": true\n  },\n  "run_id": 38152420022,\n  "status": "PASS"\n}\n', '10000': b'{\n  "100000_runtime_authorized": false,\n  "GET_captures": [\n    {\n      "name": "original-artifact-api",\n      "pid": 487080,\n      "seconds": 0.40391538199037313\n    },\n    {\n      "name": "original-provider-logZIP",\n      "pid": 487090,\n      "seconds": 1.3073148189578205\n    },\n    {\n      "name": "original-normal10000-artifact",\n      "pid": 487102,\n      "seconds": 2.565017560031265\n    }\n  ],\n  "SLA_claim": false,\n  "all_four_workloads_all_seven_stages_pass": true,\n  "artifact_bytes": 1591794,\n  "artifact_members": 185,\n  "artifact_sha256": "c39e14d30415dc00e6bd3fb8949e68892103474417373a81d91abc4a2cd84093",\n  "attempt": 1,\n  "head": "e7bf76434c593fda7cef25a07eb3f209e919f1b6",\n  "new_POST_network_Go_DB_workload_calls": 0,\n  "observations": [\n    {\n      "committed_paths": 2024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 990,\n      "reserved_bytes": 3200,\n      "size": 1000,\n      "stage": "normal_maintenance",\n      "stored_bytes": 25056,\n      "version": "before",\n      "wall_ms": 302.55\n    },\n    {\n      "committed_paths": 2024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 990,\n      "reserved_bytes": 3200,\n      "size": 1000,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 31616,\n      "version": "before",\n      "wall_ms": 131.02\n    },\n    {\n      "committed_paths": 2024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 990,\n      "reserved_bytes": 3200,\n      "size": 1000,\n      "stage": "normal_maintenance",\n      "stored_bytes": 25056,\n      "version": "after",\n      "wall_ms": 662.333\n    },\n    {\n      "committed_paths": 2024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 990,\n      "reserved_bytes": 3200,\n      "size": 1000,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 31616,\n      "version": "after",\n      "wall_ms": 128.813\n    },\n    {\n      "committed_paths": 11024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 9900,\n      "reserved_bytes": 3200,\n      "size": 10000,\n      "stage": "normal_maintenance",\n      "stored_bytes": 172416,\n      "version": "before",\n      "wall_ms": 2249.014\n    },\n    {\n      "committed_paths": 11024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 9900,\n      "reserved_bytes": 3200,\n      "size": 10000,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 180416,\n      "version": "before",\n      "wall_ms": 1468.795\n    },\n    {\n      "committed_paths": 11024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 9900,\n      "reserved_bytes": 3200,\n      "size": 10000,\n      "stage": "normal_maintenance",\n      "stored_bytes": 172416,\n      "version": "after",\n      "wall_ms": 2464.432\n    },\n    {\n      "committed_paths": 11024,\n      "live_ACK_complete": true,\n      "protected_physical_files": 9900,\n      "reserved_bytes": 3200,\n      "size": 10000,\n      "stage": "normal_api_bootstrap",\n      "stored_bytes": 180416,\n      "version": "after",\n      "wall_ms": 1444.776\n    }\n  ],\n  "old_5ms_result_relabelled": false,\n  "original_files": 222,\n  "original_manifest_sha256": "45b0edb05938be22de6f38888796b692fa708a627f491112a2b7844e79a9bff2",\n  "overall_speedup_claim": false,\n  "provider_members": 21,\n  "pure_saved_verifier": {\n    "100000_authorized": false,\n    "context_sha256": "c8a2e06609b18134e7a22520d4ab3d05b3169ea1c44d3a7e26f5ac55a7defe3e",\n    "failed_workloads": [],\n    "format": 1,\n    "measurement_evidence_verified": true,\n    "pairs": [\n      1000,\n      10000\n    ],\n    "performance_claim": false,\n    "promotion_ready": true,\n    "source_inventory_files": {\n      "after": 173,\n      "before": 164\n    },\n    "summary_sha256": "23e6320b5b25d39bb456e73e01ff4cdd703cd7b90ef5925a2d8f1649307a86ee",\n    "workload_pass": true\n  },\n  "run_id": 38153402987,\n  "status": "PASS",\n  "test_binaries_present": false\n}\n'}


def require(condition, label):
    if not condition:
        raise ValueError(label)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return (json.dumps(value, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()


def save(root, name, value):
    with (root / name).open("xb") as target:
        target.write(canonical(value))


def snapshot_processes():
    result = {}
    for path in Path("/proc").glob("[0-9]*/stat"):
        try:
            raw = path.read_text(); fields = raw[raw.rfind(")") + 2:].split()
            result[int(path.parent.name)] = {"state": fields[0], "ppid": int(fields[1]),
                "group": int(fields[2]), "starttime": int(fields[19])}
        except (OSError, ValueError, IndexError):
            continue
    return result


def enable_subreaper():
    libc = ctypes.CDLL(None, use_errno=True)
    previous = ctypes.c_int()
    require(libc.prctl(37, ctypes.byref(previous), 0, 0, 0) == 0
            and libc.prctl(36, 1, 0, 0, 0) == 0, "Linux private child subreaper unavailable")
    return libc, previous.value


def owned_processes(known, rows, siblings, supervisor_pid):
    parents = {pid for pid, old in known.items()
               if pid in rows and rows[pid]["starttime"] == old["starttime"]}
    changed = True
    while changed:
        changed = False
        for pid, row in rows.items():
            if pid == supervisor_pid or pid in known:
                continue
            if row["ppid"] in parents or (row["ppid"] == supervisor_pid
                    and (pid, row["starttime"]) not in siblings):
                known[pid] = dict(row); parents.add(pid); changed = True
    return {pid: rows[pid] for pid, old in known.items()
            if pid in rows and rows[pid]["starttime"] == old["starttime"]}


def supervise(argv, output, name, cwd, env, cancelled, wall=2400, reserve=75, grace=40):
    """Capture one operation; a private subreaper owns only new child lineages."""
    started = time.monotonic(); deadline = started + wall
    before = snapshot_processes()
    siblings = {(pid, row["starttime"]) for pid, row in before.items() if row["ppid"] == os.getpid()}
    child = None; libc = None; previous = None; selector = None; handles = []
    known = {}; counts = [0, 0]; errors = []; adopted_reaped = []
    overflow = timed_out = forced = False; signalled = None; exit_code = None
    def refresh():
        rows = snapshot_processes()
        current = owned_processes(known, rows, siblings, os.getpid())
        if child is not None:
            journal = output / (name + ".processes.json")
            temporary = journal.with_suffix(".tmp")
            try:
                raw = canonical({"owned_pid": child.pid, "processes": known})
                require(len(raw) < 1048576, "owned process journal cap")
                temporary.write_bytes(raw); temporary.replace(journal)
            except (OSError, ValueError) as error:
                if "owned process journal unavailable" not in errors:
                    errors.append("owned process journal unavailable")
        return current
    def signal_owned(number):
        for group in sorted({row["group"] for row in refresh().values()}):
            if group <= 1 or group == os.getpgrp():
                continue
            try: os.killpg(group, number)
            except ProcessLookupError: pass
    def reap_owned():
        current = refresh()
        for pid in current:
            if child is not None and pid == child.pid:
                continue
            try:
                actual, status = os.waitpid(pid, os.WNOHANG)
                if actual:
                    adopted_reaped.append({"pid": actual, "status": status, "starttime": known[pid]["starttime"]})
            except ChildProcessError:
                pass
    def read_streams(delay):
        nonlocal overflow
        for key, _ in selector.select(delay):
            try: block = os.read(key.fileobj.fileno(), 65536)
            except BlockingIOError: continue
            if not block:
                selector.unregister(key.fileobj); key.fileobj.close(); continue
            index = key.data; kept = block[:max(0, 50331648 - counts[index])]
            if handles[index].write(kept) != len(kept): errors.append("short original stream write")
            counts[index] += len(kept); overflow |= len(kept) != len(block)
    try:
        for suffix in (".stdout.bin", ".stderr.bin"):
            handles.append((output / (name + suffix)).open("xb", buffering=0))
        save(output, name + ".invocation.json", {"argv": argv, "cwd": str(cwd), "wall_seconds": wall,
            "cleanup_reserve_seconds": reserve, "signal_grace_seconds": grace,
            "stdout_cap_bytes": 50331648, "stderr_cap_bytes": 50331648, "attempt": 1})
        require(not cancelled.is_set(), "sticky interruption before operation spawn")
        libc, previous = enable_subreaper()
        child = subprocess.Popen(argv, cwd=cwd, env=env, stdin=subprocess.DEVNULL,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
        first = snapshot_processes().get(child.pid)
        if first is not None:
            known[child.pid] = dict(first)
        selector = selectors.DefaultSelector()
        for index, pipe in enumerate((child.stdout, child.stderr)):
            os.set_blocking(pipe.fileno(), False); selector.register(pipe, selectors.EVENT_READ, index)
        refresh()
        while selector.get_map() or child.poll() is None:
            now = time.monotonic(); refresh(); reap_owned()
            if signalled is None and (cancelled.is_set() or overflow or errors or now >= deadline - reserve):
                timed_out = now >= deadline - reserve; signalled = now
                root_row = refresh().get(child.pid)
                if root_row is not None and root_row["group"] == child.pid:
                    try: os.killpg(child.pid, signal.SIGTERM)
                    except ProcessLookupError: pass
            # GitHub cancellation gives much less grace than the normal deadline.
            actual_grace = min(grace, 2) if cancelled.is_set() else grace
            if signalled is not None and now - signalled >= actual_grace:
                signal_owned(signal.SIGKILL); forced = True
            read_streams(.05)
            if now >= deadline - max(.2, reserve / 3):
                signal_owned(signal.SIGKILL); forced = True
                break
        exit_code = child.wait(timeout=min(3, max(.1, deadline - time.monotonic())))
    except Exception as error:
        errors.append(type(error).__name__ + ": " + str(error))
    finally:
        if child is not None:
            try:
                if child.poll() is None or refresh():
                    signal_owned(signal.SIGKILL); forced = True
                if child.poll() is None:
                    child.wait(timeout=min(3, max(.1, deadline - time.monotonic())))
                exit_code = child.returncode
                until = min(deadline, time.monotonic() + 3)
                while refresh() and time.monotonic() < until:
                    reap_owned(); time.sleep(.02)
                reap_owned()
                # Preserve pipe tails from killed descendants before closing originals.
                while selector is not None and selector.get_map() and time.monotonic() < until:
                    read_streams(.02)
            except Exception as error:
                errors.append("final child reconciliation: " + type(error).__name__)
        remaining = refresh() if child is not None else {}
        if selector is not None:
            for key in list(selector.get_map().values()): key.fileobj.close()
            selector.close()
        for handle in handles: handle.close()
        if libc is not None and libc.prctl(36, previous, 0, 0, 0) != 0:
            errors.append("subreaper restore failed")
    elapsed = time.monotonic() - started
    result = {"owned_pid": None if child is None else child.pid, "exit_code": exit_code,
        "reaped": child is not None and child.poll() is not None,
        "owned_groups_absent": not remaining, "observed_processes": known,
        "adopted_reaped": adopted_reaped, "remaining_owned_processes": remaining,
        "timeout": timed_out or elapsed > wall, "cancelled": cancelled.is_set(), "forced": forced,
        "overflow": overflow, "stream_errors": errors, "elapsed_seconds": elapsed,
        "allocated_wall_seconds": wall, "stdout_bytes": counts[0], "stderr_bytes": counts[1]}
    for label in ("stdout", "stderr"):
        path = output / (name + "." + label + ".bin")
        result[label + "_sha256"] = sha(path.read_bytes()) if path.exists() else None
    save(output, name + ".result.json", result)
    return result


def successful(result):
    return (result["exit_code"] == 0 and result["reaped"] and result["owned_groups_absent"]
            and result["elapsed_seconds"] <= result["allocated_wall_seconds"]
            and not any(result[key] for key in ("timeout", "cancelled", "forced", "overflow", "stream_errors")))


def once_pass(original, summary):
    return (successful(original) and summary.get("complete") is True
            and summary.get("pass") is True and summary.get("cleanup") is True
            and not summary.get("interrupted") and not summary.get("refused"))


def verify_sources(checkout):
    frozen_raw = (HERE / "hosted-source-freeze.json").read_bytes()
    require(sha(frozen_raw) == FREEZE_SHA, "committed397 freeze identity")
    frozen = json.loads(frozen_raw)
    require(len(frozen["files"]) == 397, "reviewed397 source scope")
    for relative, record in frozen["files"].items():
        path = HERE.parents[1] / relative; raw = path.read_bytes()
        require(path.is_file() and not path.is_symlink() and len(raw) == record["bytes"]
                and sha(raw) == record["sha256"]
                and oct(stat.S_IMODE(path.stat().st_mode)) == record["posix_mode"], "reviewed source drift")
    require(sha((HERE / "run_once.py").read_bytes()) == RUNNER_SHA
            and sha((HERE / "fixture_test.go.txt").read_bytes()) == FIXTURE_SHA
            and sha((HERE / "identity.json").read_bytes()) == IDENTITY_SHA, "fixed runner/fixture/identity")
    identity = json.loads((HERE / "identity.json").read_bytes())
    source = identity["runtime"]["source_files"]
    require(identity["runtime"]["commit"] == RUNTIME and len(source) == 391
            and sha(canonical(source)) == SOURCE_SHA, "fixed current391")
    application = {path: record for path, record in source.items()
                   if path.startswith(("cmd/", "internal/", "pkg/")) or path in ("go.mod", "go.sum")}
    require(len(application) == 173 and sha(canonical(application)) == APP_SHA, "fixed APP173")
    for relative, record in source.items():
        path = checkout / relative; raw = path.read_bytes()
        require(path.is_file() and not path.is_symlink() and len(raw) == record["bytes"]
                and sha(raw) == record["sha256"]
                and bool(path.stat().st_mode & 0o111) == (record["mode"] == "100755"), "exact runtime file drift")
    return identity


def check_context(context):
    temp = Path(os.environ["RUNNER_TEMP"]).resolve()
    checkout = HERE.parents[1] / ".populated-runtime"
    require(not checkout.is_symlink() and checkout.resolve() == checkout, "separate owned runtime path")
    require(context.get("runner_temp") == str(temp)
            and context.get("checkout") == str(HERE.parents[1] / ".populated-runtime")
            and context.get("private_root") == str(temp / "populated100k-private")
            and context.get("tooling_commit") == os.environ.get("GITHUB_SHA")
            and context.get("run_id") == os.environ.get("GITHUB_RUN_ID")
            and context.get("attempt") == 1 and context.get("freeze_sha256") == FREEZE_SHA
            and context.get("wrapper_sha256") == sha(Path(__file__).read_bytes()), "exact cleanup context")
    require(isinstance(context.get("private_preexisting"), bool)
            and isinstance(context.get("fixture_preexisting"), bool), "complete path admission context")


def owned_journal(root, context):
    path = root / "once/owned-resources.json"
    if not os.path.lexists(path):
        return None
    require(path.is_file() and not path.is_symlink() and path.stat().st_size < 16384, "owned journal file")
    require(context["private_preexisting"] is False and context["fixture_preexisting"] is False,
            "owned paths were fresh before once spawn")
    value = json.loads(path.read_bytes()); name = value.get("container", "")
    require(value.get("format") == 1 and re.fullmatch("cutmy-populated100k-[0-9a-f]{16}", name)
            and value.get("volume") == name + "-data" and value.get("fixture_sha256") == FIXTURE_SHA,
            "exact owned resource names")
    for key, expected in (("private_root", context["private_root"]), ("checkout", context["checkout"]),
                          ("injected_fixture", str(Path(context["checkout"]) / "internal/app/populated004_to005_100k_test.go")),
                          ("private_env", str(Path(context["private_root"]) / "postgres.env")),
                          ("binary", str(Path(context["private_root"]) / "populated100k.test"))):
        require(value.get(key) == expected, "owned path binding")
    return value


def reconcile_processes(root, deadline):
    """Fallback for a terminated wrapper; never adopt a reused PID's identity."""
    path = root / "outer-once.processes.json"
    if not os.path.lexists(path):
        attempted = os.path.lexists(root / "outer-once.invocation.json")
        return {"pass": not attempted, "known_processes": 0, "remaining": {},
                "incomplete_ownership": attempted}
    require(path.is_file() and not path.is_symlink() and path.stat().st_size < 1048576, "owned process journal file")
    record = json.loads(path.read_bytes()); known = record["processes"]
    require(isinstance(known, dict) and str(record["owned_pid"]) in known, "owned process root binding")
    require(all(key.isdigit() and int(key) > 1 and isinstance(row.get("starttime"), int)
                and row["starttime"] > 0 and isinstance(row.get("group"), int) and row["group"] > 1
                for key, row in known.items()), "owned process identities")
    def matching():
        rows = snapshot_processes()
        return {int(pid): rows[int(pid)] for pid, old in known.items()
                if int(pid) in rows and rows[int(pid)]["starttime"] == old["starttime"]}
    for group in {row["group"] for row in matching().values()}:
        require(group != os.getpgrp(), "foreign supervisor group excluded")
        try: os.killpg(group, signal.SIGKILL)
        except ProcessLookupError: pass
    until = min(deadline, time.monotonic() + 3)
    while matching() and time.monotonic() < until: time.sleep(.02)
    remaining = matching()
    return {"pass": not remaining, "known_processes": len(known), "remaining": remaining}


def cleanup(root, context, deadline, prefix="cleanup"):
    check_context(context)
    processes = reconcile_processes(root, deadline)
    journal = owned_journal(root, context); counter = 0
    def command(argv):
        nonlocal counter
        require(deadline - time.monotonic() > 1, "cleanup total30s exhausted")
        counter += 1; name = prefix + "-" + str(counter)
        result = supervise(argv, root, name, HERE.parents[1], os.environ.copy(),
            threading.Event(), wall=min(5, deadline - time.monotonic()), reserve=1, grace=.5)
        raw = (root / (name + ".stdout.bin")).read_bytes()
        require(result["reaped"] and result["owned_groups_absent"]
                and result["elapsed_seconds"] <= result["allocated_wall_seconds"]
                and not any(result[k] for k in ("timeout", "cancelled", "forced", "overflow", "stream_errors")), "cleanup command incomplete")
        return result["exit_code"], raw
    errors = [] if processes["pass"] else ["owned processes still present"]
    if journal:
        for kind in ("container", "volume"):
            name = journal[kind]
            try:
                argv = ["docker", "inspect", "--type", "container", "--format", "{{json .Config.Labels}}", name] if kind == "container" else ["docker", "volume", "inspect", "--format", "{{json .Labels}}", name]
                code, raw = command(argv)
                if code == 0:
                    require(json.loads(raw).get("cutmy.populated100k.owner") == journal["container"], "exact cleanup owner label")
                    delete = ["docker", "rm", "--force", "--volumes", name] if kind == "container" else ["docker", "volume", "rm", name]
                    code, _ = command(delete); require(code == 0, "owned resource delete failed")
                else:
                    require(code == 1, "owned resource inspect failed")
                argv = ["docker", "ps", "--all", "--filter", "name=^/" + name + "$", "--format", "{{.ID}}"] if kind == "container" else ["docker", "volume", "ls", "--filter", "name=^" + name + "$", "--format", "{{.Name}}"]
                code, raw = command(argv); require(code == 0 and not raw.strip(), "owned resource absence unconfirmed")
            except Exception as error:
                errors.append(str(error))
    try:
        private = Path(context["private_root"])
        fixture = Path(context["checkout"]) / "internal/app/populated004_to005_100k_test.go"
        if journal and processes["pass"]:
            if os.path.lexists(private):
                require(private.is_dir() and not private.is_symlink(), "private cleanup type"); shutil.rmtree(private)
            if os.path.lexists(fixture):
                require(fixture.is_file() and not fixture.is_symlink() and sha(fixture.read_bytes()) == FIXTURE_SHA, "owned injected fixture cleanup")
                fixture.unlink()
        # Missing ownership evidence never permits deletion, even at expected paths.
        require(not os.path.lexists(private) and not os.path.lexists(fixture), "private/fixture absence; unowned paths preserved")
    except Exception as error:
        errors.append(str(error))
    return {"pass": not errors, "errors": errors, "resource_journal_present": journal is not None,
            "process_reconciliation": processes, "elapsed_within_deadline": time.monotonic() <= deadline}


def main():
    parser = argparse.ArgumentParser(); parser.add_argument("--cleanup", action="store_true")
    args = parser.parse_args(); temp = Path(os.environ["RUNNER_TEMP"]).resolve()
    state = temp / "populated100k-hosted"
    require(os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
            and os.environ.get("GITHUB_RUN_ATTEMPT") == "1"
            and re.fullmatch("[0-9a-f]{40}", os.environ.get("CUTMY_REVIEWED_TOOLING_COMMIT", ""))
            and os.environ["GITHUB_SHA"] == os.environ["CUTMY_REVIEWED_TOOLING_COMMIT"], "one reviewed dispatch context")
    require(sha(Path(__file__).read_bytes()) == os.environ.get("CUTMY_REVIEWED_WRAPPER_SHA"), "reviewed wrapper identity")
    if args.cleanup:
        if not os.path.lexists(state):
            return 0
        require(state.is_dir() and not state.is_symlink(), "owned evidence directory")
        path = state / "context.json"
        require(path.is_file() and not path.is_symlink() and path.stat().st_size < 16384, "complete cleanup context file")
        context = json.loads(path.read_bytes())
        result = cleanup(state, context, time.monotonic() + 30, prefix="always-cleanup")
        save(state, "always-cleanup.json", result)
        return 0 if result["pass"] and result["elapsed_within_deadline"] else 1
    require(not os.path.lexists(state), "fresh hosted evidence path"); state.mkdir(mode=0o700)
    context = {"runner_temp": str(temp), "checkout": str(HERE.parents[1] / ".populated-runtime"),
        "private_root": str(temp / "populated100k-private"), "tooling_commit": os.environ["GITHUB_SHA"],
        "run_id": os.environ["GITHUB_RUN_ID"], "attempt": 1, "freeze_sha256": FREEZE_SHA,
        "wrapper_sha256": sha(Path(__file__).read_bytes()),
        "private_preexisting": os.path.lexists(temp / "populated100k-private"),
        "fixture_preexisting": os.path.lexists(HERE.parents[1] / ".populated-runtime/internal/app/populated004_to005_100k_test.go")}
    save(state, "context.json", context)
    cancelled = threading.Event()
    def interrupt(signum, frame): cancelled.set()
    previous = {number: signal.signal(number, interrupt) for number in (signal.SIGINT, signal.SIGTERM)}
    start = time.monotonic(); deadline = start + 2400
    result = {"pass": False, "failure": None, "once_result": None, "cleanup": None}
    try:
        checkout = Path(context["checkout"]); verify_sources(checkout)
        require(context["private_preexisting"] is False and context["fixture_preexisting"] is False, "fresh private and fixture paths")
        require(not cancelled.is_set(), "sticky interruption before once authority")
        authority = {"authorized_once": True, "kind": "populated004-to005-100k", "freeze_sha256": FREEZE_SHA,
            "fixture_sha256": FIXTURE_SHA, "runner_sha256": RUNNER_SHA, "runtime_commit": RUNTIME}
        for size, raw in REVIEW_BYTES.items():
            require(sha(raw) == REVIEW_SHAS[size], "fixed accepted normal receipt")
            receipt = json.loads(raw)
            require(receipt["status"] == "PASS" and receipt["head"] == "e7bf76434c593fda7cef25a07eb3f209e919f1b6", "accepted normal prerequisite")
            path = state / ("accepted-normal" + size + "-root-review.json")
            with path.open("xb") as target: target.write(raw)
            authority["accepted_" + size + "_review"] = {"path": str(path), "sha256": REVIEW_SHAS[size]}
        save(state, "authorization.json", authority)
        argv = [sys.executable, "-I", "-B", str(HERE / "run_once.py"), "--authorization", str(state / "authorization.json"),
            "--freeze", str(HERE / "hosted-source-freeze.json"), "--checkout", context["checkout"],
            "--output", str(state / "once"), "--private-root", context["private_root"]]
        env = {key: value for key, value in os.environ.items() if not key.startswith(("DATABASE", "TEST_DATABASE", "POSTGRES", "PGPASSWORD", "CUTMY_POPULATED"))}
        env.update(GOMAXPROCS="2", GOMEMLIMIT="128MiB", GOTOOLCHAIN="local", GOTELEMETRY="off")
        original = supervise(argv, state, "outer-once", HERE.parents[1], env, cancelled,
            wall=max(1, deadline - time.monotonic()))
        result["once_result"] = original
        summary = json.loads((state / "once/summary.json").read_bytes())
        result["pass"] = once_pass(original, summary)
    except Exception as error:
        result["failure"] = str(error)
    finally:
        try:
            result["cleanup"] = cleanup(state, context, min(deadline, time.monotonic() + 30))
            verify_sources(Path(context["checkout"]))
            result["pass"] = result["pass"] and result["cleanup"]["pass"] and result["cleanup"]["elapsed_within_deadline"] and not cancelled.is_set()
        except Exception as error:
            result["cleanup_failure"] = str(error); result["pass"] = False
        for number, handler in previous.items(): signal.signal(number, handler)
        elapsed = time.monotonic() - start
        result["pass"] = result["pass"] and not cancelled.is_set() and elapsed <= 2400
        result.update(cancelled=cancelled.is_set(), elapsed_seconds=elapsed,
            allocated_wall_seconds=2400, runtime_commit=RUNTIME)
        save(state, "hosted-result.json", result)
    print(canonical({key: result[key] for key in ("pass", "failure", "cancelled")}).decode(), end="")
    return 0 if result["pass"] and not cancelled.is_set() else 1


if __name__ == "__main__":
    raise SystemExit(main())
