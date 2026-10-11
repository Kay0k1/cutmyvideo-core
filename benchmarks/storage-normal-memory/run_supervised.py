"""Concrete owned wrapper copied from the reviewed100k supervision; one current10k only."""
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
WORKSPACE = HERE.parents[1]
OWNER_LABEL = "cutmy.storage-normal-memory.owner"
RUNTIME = "d594cc4681dc1cab61664cec8c92bdf9a3e9444c"


def require(condition, code):
    if not condition:
        raise ValueError(code)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def canonical(value):
    return (json.dumps(value, indent=2, sort_keys=True, allow_nan=False) + "\n").encode()


def save(root, name, value):
    with (root / name).open("xb") as target:
        target.write(canonical(value))


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


def evidence_bytes(root):
    private=root.parent/"storage-normal-memory-private";roots=[root]
    try:
        mode=private.lstat().st_mode
    except FileNotFoundError:
        pass
    else:
        require(stat.S_ISDIR(mode) and not stat.S_ISLNK(mode),"private resource type")
        roots.append(private)
    return owned_tree_bytes(roots)


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


def supervise(argv, output, name, cwd, env, cancelled, wall=1800, reserve=75, grace=40):
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
            if name == "outer-once" and evidence_bytes(output) > 240 * 1024**2:
                if "aggregate evidence cap" not in errors:
                    errors.append("aggregate evidence cap")
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


def check_context(context):
    temp = Path(os.environ["RUNNER_TEMP"]).resolve()
    checkout = WORKSPACE / ".comparison/after"
    require(not checkout.is_symlink() and checkout.resolve() == checkout, "exact separate runtime path")
    require(context.get("runner_temp") == str(temp)
            and context.get("checkout") == str(checkout)
            and context.get("state") == str(temp / "storage-normal-memory")
            and context.get("private_root") == str(temp/"storage-normal-memory-private")
            and context.get("tooling_commit") == os.environ["GITHUB_SHA"]
            and context.get("run_id") == os.environ["GITHUB_RUN_ID"]
            and context.get("attempt") == 1
            and context.get("wrapper_sha256") == sha(Path(__file__).read_bytes())
            and context.get("fixture_sha256") == sha((HERE/"pilot_test.go.txt").read_bytes()), "exact cleanup context")
    require(context.get("fixture_preexisting") is False and context.get("private_preexisting") is False, "fixture was absent before owned spawn")


def owned_journal(root, context):
    path = root / "once/owned-resources.json"
    if not os.path.lexists(path):
        return None
    require(path.is_file() and not path.is_symlink() and path.stat().st_size < 16384, "owned journal file")
    value = json.loads(path.read_bytes()); name = value.get("container", "")
    require(value.get("format") == 1 and re.fullmatch("cutmy-storage-memory-[0-9a-f]{16}", name)
            and value.get("volume") == name + "-data"
            and value.get("owner_label") == OWNER_LABEL
            and value.get("tooling_commit") == context["tooling_commit"]
            and value.get("run_id") == context["run_id"] and value.get("attempt") == 1
            and value.get("fixture_sha256") == context["fixture_sha256"], "exact owned container identity")
    for key, expected in (("checkout",context["checkout"]),
                          ("injected_fixture",str(Path(context["checkout"])/"internal/app/storage_normal_capacity_pilot_test.go")),
                          ("private_root",context["private_root"]),
                          ("private_env",str(Path(context["private_root"])/"postgres.env")),
                          ("binary",str(Path(context["private_root"])/"after.test"))):
        require(value.get(key) == expected, "exact owned path binding")
    return value


def cleanup(root, context, deadline, prefix="cleanup"):
    check_context(context)
    processes = reconcile_processes(root, deadline)
    journal = owned_journal(root, context); counter = 0
    def command(argv):
        nonlocal counter
        require(deadline - time.monotonic() > 1, "cleanup30s exhausted")
        counter += 1; name = prefix + "-" + str(counter)
        result = supervise(argv, root, name, WORKSPACE, os.environ.copy(), threading.Event(),
                           wall=min(5,deadline-time.monotonic()), reserve=1,grace=.5)
        raw = (root/(name+".stdout.bin")).read_bytes()
        require(result["reaped"] and result["owned_groups_absent"]
                and result["elapsed_seconds"] <= result["allocated_wall_seconds"]
                and not any(result[k] for k in ("timeout","cancelled","forced","overflow","stream_errors")), "cleanup command incomplete")
        return result["exit_code"],raw
    errors = [] if processes["pass"] else ["owned processes still present"]
    if journal:
        for kind in ("container","volume"):
            name=journal[kind]
            try:
                inspect=["docker","inspect","--type","container","--format","{{json .Config.Labels}}",name] if kind == "container" else ["docker","volume","inspect","--format","{{json .Labels}}",name]
                code,raw=command(inspect)
                if code == 0:
                    require(json.loads(raw).get(OWNER_LABEL) == journal["container"], "exact cleanup owner label")
                    remove=["docker","rm","--force","--volumes",name] if kind == "container" else ["docker","volume","rm",name]
                    code,_=command(remove);require(code == 0,"owned resource delete failed")
                else:
                    require(code == 1,"owned resource inspect failed")
                absent=["docker","ps","--all","--filter","name=^/"+name+"$","--format","{{.ID}}"] if kind == "container" else ["docker","volume","ls","--filter","name=^"+name+"$","--format","{{.Name}}"]
                code,raw=command(absent)
                require(code == 0 and not raw.strip(),"owned resource absence unconfirmed")
            except Exception as error:
                errors.append(str(error))
        if processes["pass"]:
            for key in ("private_env","binary","injected_fixture"):
                try:
                    path=Path(journal[key])
                    if os.path.lexists(path):
                        require(path.is_file() and not path.is_symlink(),"owned cleanup file type")
                        if key == "injected_fixture":
                            require(sha(path.read_bytes()) == context["fixture_sha256"],"owned fixture bytes")
                        path.unlink()
                    require(not os.path.lexists(path),"owned private path absent")
                except Exception as error:
                    errors.append(str(error))
            try:
                private=Path(context["private_root"])
                if os.path.lexists(private):
                    require(private.is_dir() and not private.is_symlink(),"owned private directory")
                    private.rmdir()
                require(not os.path.lexists(private),"owned private directory absent")
            except Exception as error:
                errors.append(str(error))
    else:
        require(not os.path.lexists(context["private_root"]),"no journal; unowned private path preserved")
        fixture=Path(context["checkout"])/"internal/app/storage_normal_capacity_pilot_test.go"
        require(not os.path.lexists(fixture),"no journal; unowned fixture preserved")
    return {"pass":not errors,"errors":errors,"resource_journal_present":journal is not None,
            "process_reconciliation":processes,"elapsed_within_deadline":time.monotonic() <= deadline}


def main():
    parser=argparse.ArgumentParser();parser.add_argument("--cleanup",action="store_true");args=parser.parse_args()
    require(os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
            and os.environ.get("GITHUB_REPOSITORY") == "Kay0k1/cutmyvideo-core"
            and os.environ.get("GITHUB_RUN_ATTEMPT") == "1"
            and re.fullmatch("[0-9a-f]{40}",os.environ.get("CUTMY_REVIEWED_TOOLING_COMMIT",""))
            and os.environ["GITHUB_SHA"] == os.environ["CUTMY_REVIEWED_TOOLING_COMMIT"],"one reviewed attempt1 dispatch")
    require(sha(Path(__file__).read_bytes()) == os.environ.get("CUTMY_REVIEWED_WRAPPER_SHA"),"reviewed wrapper identity")
    temp=Path(os.environ["RUNNER_TEMP"]).resolve();state=temp/"storage-normal-memory"
    if args.cleanup:
        if not os.path.lexists(state):
            return 0
        require(state.is_dir() and not state.is_symlink(),"owned evidence directory")
        path=state/"context.json"
        require(path.is_file() and not path.is_symlink() and path.stat().st_size < 16384,"complete cleanup context file")
        context=json.loads(path.read_bytes());result=cleanup(state,context,time.monotonic()+30,"always-cleanup")
        save(state,"always-cleanup.json",result)
        catalog={path.relative_to(state).as_posix():{"bytes":path.stat().st_size,"sha256":sha(path.read_bytes())}
                 for path in sorted(state.rglob("*")) if path.is_file()}
        require(len(catalog) <= 1500 and len(canonical(catalog)) <= 1048576,"final evidence catalog cap")
        save(state,"hosted-catalog.json",catalog)
        require(evidence_bytes(state) <= 256*1024**2,"final evidence cap")
        return 0 if result["pass"] and result["elapsed_within_deadline"] else 1
    require(not os.path.lexists(state),"fresh evidence namespace");state.mkdir(mode=0o700)
    fixture=WORKSPACE/".comparison/after/internal/app/storage_normal_capacity_pilot_test.go"
    context={"runner_temp":str(temp),"checkout":str(WORKSPACE/".comparison/after"),"state":str(state),
             "tooling_commit":os.environ["GITHUB_SHA"],"run_id":os.environ["GITHUB_RUN_ID"],"attempt":1,
             "wrapper_sha256":sha(Path(__file__).read_bytes()),"fixture_sha256":sha((HERE/"pilot_test.go.txt").read_bytes()),
             "fixture_preexisting":os.path.lexists(fixture),"private_root":str(temp/"storage-normal-memory-private"),
             "private_preexisting":os.path.lexists(temp/"storage-normal-memory-private")}
    save(state,"context.json",context)
    cancelled=threading.Event()
    def interrupt(signum,frame):
        cancelled.set()
    previous={number:signal.signal(number,interrupt) for number in (signal.SIGINT,signal.SIGTERM)}
    start=time.monotonic();deadline=start+1800
    result={"pass":False,"failure":None,"once_result":None,"cleanup":None}
    try:
        check_context(context)
        status=supervise(["git","--no-optional-locks","-C",str(WORKSPACE),"rev-parse","HEAD"],state,"tooling-head",WORKSPACE,os.environ.copy(),cancelled,wall=30,reserve=5,grace=1)
        require(successful(status) and (state/"tooling-head.stdout.bin").read_bytes().decode().strip() == context["tooling_commit"],"exact tooling checkout")
        require(not cancelled.is_set(),"sticky interruption before diagnostic spawn")
        env={k:v for k,v in os.environ.items() if not k.startswith(("DATABASE","TEST_DATABASE","POSTGRES","PGPASSWORD"))}
        env.update(GOMAXPROCS="2",GOMEMLIMIT="128MiB",GOTOOLCHAIN="local",GOTELEMETRY="off")
        original=supervise([sys.executable,"-B",str(HERE/"run_hosted.py")],state,"outer-once",WORKSPACE,env,cancelled,wall=max(1,deadline-time.monotonic()))
        result["once_result"]=original
        summary=json.loads((state/"once/summary.json").read_bytes())
        result["pass"]=successful(original) and summary.get("complete") is True and summary.get("pass") is True and summary.get("container_cleanup") is True and not summary.get("interrupted") and not summary.get("refused")
    except Exception as error:
        result["failure"]=str(error)
    finally:
        try:
            result["cleanup"]=cleanup(state,context,min(deadline,time.monotonic()+30))
            result["pass"]=result["pass"] and result["cleanup"]["pass"] and result["cleanup"]["elapsed_within_deadline"] and not cancelled.is_set()
        except Exception as error:
            result["cleanup_failure"]=str(error);result["pass"]=False
        for number,handler in previous.items():
            signal.signal(number,handler)
        elapsed=time.monotonic()-start
        result["pass"]=result["pass"] and not cancelled.is_set() and elapsed <= 1800 and evidence_bytes(state) <= 256*1024**2
        result.update(cancelled=cancelled.is_set(),elapsed_seconds=elapsed,allocated_wall_seconds=1800,runtime_commit=RUNTIME)
        save(state,"hosted-result.json",result)
    print(canonical({k:result[k] for k in ("pass","failure","cancelled")}).decode(),end="")
    return 0 if result["pass"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
