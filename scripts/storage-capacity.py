#!/usr/bin/env python3
"""Reproduce opt-in retained-row storage workloads; no live media/providers."""
from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import threading
import time


ROOT = Path(__file__).resolve().parents[1]


def command(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True,
                          stdout=subprocess.PIPE, stderr=subprocess.PIPE, **kwargs).stdout.strip()


def digest(paths):
    value = hashlib.sha256()
    for path in sorted(paths):
        value.update(str(path.relative_to(ROOT)).encode() + b"\0" + path.read_bytes() + b"\0")
    return value.hexdigest()


def cgroup(pid):
    """Host-visible cgroup v2; unavailable readings remain explicitly null."""
    try:
        lines = Path(f"/proc/{pid}/cgroup").read_text().splitlines()
        name = next(line.split(":", 2)[2] for line in lines if line.startswith("0::"))
        root = Path("/sys/fs/cgroup") / name.lstrip("/")
        values = {}
        for filename in ("memory.current", "memory.peak", "memory.max", "memory.swap.current"):
            try:
                content = (root / filename).read_text().strip()
                values[filename] = int(content) if content.isdigit() else content
            except FileNotFoundError:
                # Older cgroup-v2 kernels have no memory.peak interface.
                values[filename] = None
        values["memory.stat"] = {key: int(value) for key, value in
                                 (line.split() for line in (root / "memory.stat").read_text().splitlines())}
        values["path"] = name
        return values
    except (OSError, StopIteration, ValueError):
        return None


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--sizes", default="10000,100000")
    parser.add_argument("--samples", type=int, default=20)
    parser.add_argument("--pg-container", default=os.environ.get("STORAGE_PG_CONTAINER"))
    parser.add_argument("--race", action="store_true", help="correctness run; do not compare its latency with normal builds")
    args = parser.parse_args()
    if not os.environ.get("TEST_DATABASE_URL"):
        parser.error("TEST_DATABASE_URL must identify a dedicated, disposable PostgreSQL test database")
    if not 3 <= args.samples <= 100:
        parser.error("samples must be 3..100")
    try:
        sizes = [int(value) for value in args.sizes.split(",")]
        if len(set(sizes)) != len(sizes) or any(value < 1000 or value > 100000 for value in sizes):
            raise ValueError()
    except ValueError:
        parser.error("sizes must be distinct integers in 1000..100000")
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    if any(output.iterdir()):
        parser.error("output must be empty; preserve earlier evidence rather than overwriting it")
    parent = Path(os.environ.get("TMPDIR", "/tmp"))
    parent.mkdir(parents=True, exist_ok=True)
    work = Path(tempfile.mkdtemp(prefix="cutmy-storage-capacity-", dir=parent))
    runtime_files = [p for folder in ("cmd", "internal", "pkg") for p in (ROOT / folder).rglob("*")
                     if p.is_file() and (p.suffix in (".go", ".sql", ".json")) and not p.name.endswith("_test.go")]
    harness_files = [ROOT / "scripts/storage-capacity.py", ROOT / "internal/app/storage_capacity_test.go"]
    env = dict(os.environ)
    env.update(CUTMY_STORAGE_CAPACITY="1", CUTMY_STORAGE_REPORT_DIR=str(output),
               CUTMY_STORAGE_SIZES=args.sizes, CUTMY_STORAGE_SAMPLES=str(args.samples))
    metadata = {
        "format": 1,
        "revision": command(["git", "rev-parse", "HEAD"]),
        "runtime_source_sha256": digest(runtime_files),
        "harness_sha256": digest(harness_files),
        "go_version": command([args.go, "version"], env=env),
        "sizes": sizes, "samples_per_query": args.samples, "race": args.race,
        "environment": {key: env.get(key) for key in ("GOMAXPROCS", "GOFLAGS", "GOTOOLCHAIN", "GOGC", "GOMEMLIMIT")},
        "host": {"kernel": os.uname().release, "machine": os.uname().machine,
                 "cpu_affinity_count": len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else None},
        "scope": "Synthetic retained database rows, tiny filesystem entries, storage admission/publication and maintenance. Excludes FFmpeg, live providers, export capacity and network transfer.",
        "memory_notes": "Go metrics belong to the Go test process. Host cgroup may include unrelated processes; PostgreSQL container cgroup includes its processes and charged cache, not just Go heap. memory.peak may predate this run; sampled memory.current peaks are run-local. RSS and cgroup usage are distinct measurements.",
        "host_cgroup_before": cgroup(os.getpid()),
        "postgres_container": None,
    }
    pg_pid = None
    if args.pg_container:
        info = json.loads(command(["docker", "inspect", args.pg_container, "--format",
                                   '{"pid":{{.State.Pid}},"image":{{json .Config.Image}},"image_id":{{json .Image}},"memory_limit":{{.HostConfig.Memory}},"nano_cpus":{{.HostConfig.NanoCpus}},"shm_size":{{.HostConfig.ShmSize}},"tmpfs":{{json .HostConfig.Tmpfs}}}']))
        if info["pid"] <= 0:
            parser.error("owned PostgreSQL container is not running")
        pg_pid = info["pid"]
        metadata["postgres_container"] = {"name": args.pg_container, **info,
                                           "cgroup_before": cgroup(pg_pid)}
    samples = []
    stopped = threading.Event()

    def monitor():
        while not stopped.wait(.1):
            samples.append({"elapsed_ms": round((time.monotonic() - started) * 1000, 3),
                            "postgres_cgroup": cgroup(pg_pid) if pg_pid else None})

    try:
        binary = work / "storage-capacity.test"
        build = [args.go, "test", "-c", "-o", str(binary)]
        if args.race:
            build.append("-race")
        build.append("./internal/app")
        with (output / "build.log").open("w") as log:
            result = subprocess.run(build, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
        if result.returncode:
            metadata["build_exit_code"] = result.returncode
            return result.returncode
        metadata["test_binary_sha256"] = hashlib.sha256(binary.read_bytes()).hexdigest()
        metadata["test_command"] = ["storage-capacity.test", "-test.run=^TestStorageCapacityEvidence$",
                                    "-test.count=1", "-test.v", "-test.timeout=15m"]
        started = time.monotonic()
        monitor_thread = threading.Thread(target=monitor, daemon=True)
        monitor_thread.start()
        with (output / "workload.log").open("w") as log:
            result = subprocess.run([str(binary), *metadata["test_command"][1:]],
                                    cwd=ROOT / "internal/app", env=env, stdout=log, stderr=subprocess.STDOUT)
        stopped.set()
        monitor_thread.join()
        metadata["elapsed_ms"] = round((time.monotonic() - started) * 1000, 3)
        metadata["test_exit_code"] = result.returncode
        metadata["host_cgroup_after"] = cgroup(os.getpid())
        metadata["cgroup_samples"] = samples
        currents = [s["postgres_cgroup"]["memory.current"] for s in samples
                    if s["postgres_cgroup"] and isinstance(s["postgres_cgroup"]["memory.current"], int)]
        metadata["postgres_sampled_peak_cgroup_bytes"] = max(currents) if currents else None
        print(f"Storage evidence written to {output}; workload exit={result.returncode}")
        return result.returncode
    finally:
        stopped.set()
        (output / "environment.json").write_text(json.dumps(metadata, indent=2) + "\n")
        shutil.rmtree(work)


if __name__ == "__main__":
    raise SystemExit(main())
