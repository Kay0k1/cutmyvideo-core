#!/usr/bin/env python3
"""Linux-only, task-owned role cgroups for the offline performance protocol.

Never changes ancestor controllers, production services or existing cgroups.
The coordinator creates a fresh root per sample and removes it after collecting
both role receipts. Private CLI configuration is inherited, never serialized.
"""
import argparse
import datetime
import json
import os
from pathlib import Path
import re
import resource
import signal
import subprocess
import time


FILES = ("cpu.stat", "memory.current", "memory.peak", "memory.events",
         "io.stat", "pids.current", "pids.peak", "cgroup.events")


def root_path():
    raw = os.environ.get("CUTMY_CORE_PERF_CGROUP_ROOT", "")
    if not re.fullmatch(r"/sys/fs/cgroup/system.slice/cutmy-go-perf-[a-z0-9-]{8,64}", raw):
        raise RuntimeError("A fresh task-owned cgroup root is required")
    path = Path(raw)
    if path.is_symlink():
        raise RuntimeError("Cgroup root must not be a symlink")
    return path


def snapshot(path):
    return {name: (path / name).read_text() if (path / name).exists() else None
            for name in FILES}


def write_receipt(path, payload):
    temporary = path.with_suffix(path.suffix + ".part")
    with temporary.open("x", encoding="utf-8") as output:
        json.dump(payload, output, indent=2, sort_keys=True)
        output.write("\n")
    os.replace(temporary, path)


def create_root(root):
    parent_controls = (root.parent / "cgroup.subtree_control").read_text().split()
    needed = {"cpu", "memory", "pids", "io"}
    if not needed <= set(parent_controls):
        raise RuntimeError("Ancestor controllers must already be enabled")
    root.mkdir()  # An existing directory is deliberately refused.
    try:
        (root / "cgroup.subtree_control").write_text("+cpu +memory +pids +io")
    except BaseException:
        root.rmdir()
        raise


def cleanup(root):
    for role in ("api", "worker"):
        path = root / role
        if not path.exists():
            continue
        if (path / "cgroup.procs").read_text().strip():
            raise RuntimeError("Role still has processes; keep evidence and drain it first")
        path.rmdir()
    root.rmdir()


def kill_owned(root):
    groups = {role: root / role for role in ("api", "worker") if (root / role).exists()}
    before = {role: snapshot(path) for role, path in groups.items()}
    for path in groups.values():
        (path / "cgroup.kill").write_text("1")
    deadline = time.monotonic() + 3
    while any((path / "cgroup.procs").read_text().strip() for path in groups.values()):
        if time.monotonic() >= deadline:
            raise RuntimeError("Owned failure drain exceeded three seconds")
        time.sleep(.01)
    # Let the outside launchers reap their CLI children and save role receipts
    # before the coordinator removes the now-empty cgroup directories.
    time.sleep(.1)
    print(json.dumps({"failure_only_forced_drain": True, "before": before,
                      "after": {role: snapshot(path) for role, path in groups.items()}}))


def run_role(root, role, command):
    if not root.is_dir() or not {"cpu", "memory", "pids", "io"} <= set((root / "cgroup.subtree_control").read_text().split()):
        raise RuntimeError("Coordinator must create the owned root before role launch")
    evidence = Path(os.environ["CUTMY_CORE_PERF_ROLE_RESULT"])
    pid_file = Path(os.environ["CUTMY_CORE_PERF_ROLE_PID_FILE"])
    if evidence.exists() or pid_file.exists():
        raise RuntimeError("Role evidence must be fresh")
    group = root / role
    group.mkdir()
    limits = {"cpu.max": "100000 100000" if role == "api" else "200000 100000",
              "memory.max": str((512 if role == "api" else 1024) << 20),
              "memory.swap.max": "0", "pids.max": "128"}
    for name, value in limits.items():
        (group / name).write_text(value)
    before = snapshot(group)

    def enter_group():
        (group / "cgroup.procs").write_text(str(os.getpid()))

    process = subprocess.Popen(command, start_new_session=True, preexec_fn=enter_group)
    write_receipt(pid_file, {"role": role, "pid": process.pid, "launcher_pid": os.getpid(), "cgroup": str(group)})
    interrupted = []

    def forward(signum, _frame):
        if not interrupted:
            interrupted.append((signum, time.monotonic()))
        try:
            os.killpg(process.pid, signum)
        except ProcessLookupError:
            pass

    signal.signal(signal.SIGTERM, forward)
    signal.signal(signal.SIGINT, forward)
    peak_sampled, peak_tasks, peak_rss, forced = 0, 0, 0, False
    while process.poll() is None:
        peak_sampled = max(peak_sampled, int((group / "memory.current").read_text()))
        peak_tasks = max(peak_tasks, int((group / "pids.current").read_text()))
        rss = 0
        for pid in (group / "cgroup.procs").read_text().split():
            try:
                for line in Path("/proc", pid, "status").read_text().splitlines():
                    if line.startswith("VmRSS:"):
                        rss += int(line.split()[1]) * 1024
            except (FileNotFoundError, ProcessLookupError):
                pass
        peak_rss = max(peak_rss, rss)
        if interrupted and time.monotonic() - interrupted[0][1] > 8:
            (group / "cgroup.kill").write_text("1")
            forced = True
        time.sleep(.02)
    code = process.wait()
    waited = resource.getrusage(resource.RUSAGE_CHILDREN)
    # A crashing parent must not leave media descendants alive. Keep this fact
    # in the receipt rather than silently presenting cleanup as graceful exit.
    remaining_before_drain = (group / "cgroup.procs").read_text().split()
    if remaining_before_drain:
        (group / "cgroup.kill").write_text("1")
        forced = True
    deadline = time.monotonic() + 3
    while (group / "cgroup.procs").read_text().strip() and time.monotonic() < deadline:
        time.sleep(.01)
    remaining = (group / "cgroup.procs").read_text().split()
    write_receipt(evidence, {"schema_version": 1, "role": role, "pid": process.pid,
                            "cgroup": str(group), "limits": limits, "before": before,
                            "after": snapshot(group), "exit_code": code,
                            "sample_interval_ms": 20, "sampled_peak_bytes": peak_sampled,
                            "sampled_tree_rss_peak_bytes": peak_rss,
                            "waited_max_process_rss_bytes": waited.ru_maxrss * 1024,
                            "memory_scope": "memory.peak is kernel cgroup high-water only when available; sampled aggregate memory/RSS are 20ms lower bounds, RSS can double-count shared pages; waited RSS is largest process, not simultaneous tree peak",
                            "sampled_peak_tasks": peak_tasks, "forced_drain": forced,
                            "remaining_before_drain": remaining_before_drain,
                            "remaining_pids": remaining,
                            "signal": interrupted[0][0] if interrupted else None,
                            "utc": datetime.datetime.now(datetime.timezone.utc).isoformat()})
    if remaining or forced:
        return 1
    return code if code >= 0 else 128 - code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--create-root", action="store_true")
    action.add_argument("--cleanup", action="store_true")
    action.add_argument("--kill-owned", action="store_true")
    action.add_argument("--role", choices=("api", "worker"))
    parser.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    root = root_path()
    if args.create_root:
        create_root(root)
    elif args.cleanup:
        cleanup(root)
    elif args.kill_owned:
        kill_owned(root)
    else:
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        if not command:
            parser.error("CLI role command is required")
        raise SystemExit(run_role(root, args.role, command))


if __name__ == "__main__":
    main()
