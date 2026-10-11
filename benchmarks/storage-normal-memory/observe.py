"""Scoped host-side cgroup observer; no SQL/process/network activity at import."""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import threading
import time

INTERVAL = .1
MAX_GAP = .2
MAX_SAMPLES = 5000
MAX_BYTES = 32 * 1024 * 1024
MAX_MARKERS = 160
PHASES = {"workload_setup", "normal_fixture_setup", "comparison_oracle", "normal_oracle",
          "product_call", "report_serialization", "all_fixture_cleanup"}
PHASES.update("stage_" + s for s in ("source_ttl", "foreground_dml", "held_fence",              "persisted_audit", "foreground_maintenance", "normal_maintenance", "normal_api_bootstrap"))
REQUIRED = ("memory.current", "memory.peak", "memory.max", "memory.stat", "memory.events",            "memory.events.local", "memory.swap.current", "memory.swap.max", "memory.swap.events")


def require(condition, code):
    if not condition:
        raise ValueError(code)


def decode(raw):
    def pairs(items):
        result = {}
        for key, value in items:
            require(key not in result, "duplicate_json_key")
            result[key] = value
        return result
    return json.loads(raw, object_pairs_hook=pairs)


def read(path, cap=65536):
    require(not path.is_symlink(), "observer_symlink")
    with path.open("rb") as source:
        raw = source.read(cap + 1)
    require(len(raw) <= cap, "observer_input_cap")
    return raw.decode("ascii")


def proc_identity(pid):
    stat = read(Path("/proc") / str(pid) / "stat")
    fields = stat.rsplit(")", 1)[1].split()
    require(len(fields) >= 20 and fields[19].isdigit(), "owned_pid_stat")
    return {"start_ticks": int(fields[19]), "cgroup_raw": read(Path("/proc") / str(pid) / "cgroup")}


def bind_cgroup(raw):
    fields = raw.decode("ascii").strip().split()
    require(len(fields) == 2 and re.fullmatch("[0-9a-f]{64}", fields[0]) and fields[1].isdigit() and int(fields[1]) > 1, "owned_container_pid")
    identity = proc_identity(int(fields[1]))
    lines = identity["cgroup_raw"].splitlines()
    require(len(lines) == 1 and lines[0].startswith("0::/"), "owned_cgroup_v2")
    relative = lines[0][3:]
    require(fields[0] in relative and all(part not in ("", ".", "..") for part in relative.split("/")[1:]), "owned_cgroup_path")
    directory = Path("/sys/fs/cgroup") / relative[1:]
    require(directory.resolve() == directory and directory.is_dir(), "owned_cgroup_directory")
    return {"container_id": fields[0], "pid": int(fields[1]), "directory": str(directory), **identity}


def keyed(raw):
    result = {}
    for line in raw.splitlines():
        fields = line.split()
        require(len(fields) == 2 and fields[0] not in result and fields[1].isdigit(), "cgroup_keyed_shape")
        result[fields[0]] = int(fields[1])
    require(bool(result), "cgroup_empty_keyed")
    return result


def parse_memory(raw):
    require(all(name in raw for name in REQUIRED), "required_cgroup_field")
    result = {}
    for name, value in raw.items():
        if name in ("memory.stat", "memory.events", "memory.events.local", "memory.swap.events"):
            result[name] = keyed(value)
        elif value is None:
            require(name == "memory.swap.peak", "missing_required_cgroup_file")
            result[name] = None
        else:
            require(isinstance(value, str), "cgroup_raw_type")
            v = value.strip()
            require(v.isdigit() or (name.endswith(".max") and v == "max"), "cgroup_integer_shape")
            result[name] = int(v) if v != "max" else v
    require(result["memory.max"] == 1073741824 and result["memory.swap.max"] == 0, "owned_memory_limits")
    for name in ("memory.events", "memory.events.local"):
        require(all(k in result[name] for k in ("max", "oom", "oom_kill")), "cgroup_event_keys")
    require(all(k in result["memory.stat"] for k in ("anon", "file", "kernel", "shmem")), "cgroup_stat_keys")
    return result


def validate_sample(previous, value):
    require(type(value["sequence"]) is int and value["sequence"] > 0, "sample_sequence")
    require(type(value["monotonic_ns"]) is int and value["monotonic_ns"] > 0, "sample_clock")
    memory = parse_memory(value["raw"])
    if previous:
        require(value["sequence"] == previous["sequence"] + 1, "sample_sequence_gap")
        delta = value["monotonic_ns"] - previous["monotonic_ns"]
        require(0 < delta <= int(MAX_GAP * 1e9), "sample_time_gap")
        old = parse_memory(previous["raw"])
        require(memory["memory.peak"] >= old["memory.peak"], "cumulative_peak_regression")
        for name in ("memory.events", "memory.events.local", "memory.swap.events"):
            require(all(k in memory[name] and memory[name][k] >= v for k, v in old[name].items()), "event_counter_regression")
    else:
        require(value["sequence"] == 1, "initial_sample_sequence")
    return memory


def validate_marker(marker, sequence, stack):
    require(isinstance(marker, dict) and set(marker) == {"sequence", "phase", "utc", "elapsed_ns"}, "marker_shape")
    require(type(marker["sequence"]) is int and marker["sequence"] == sequence and type(marker["elapsed_ns"]) is int and marker["elapsed_ns"] >= 0 and isinstance(marker["utc"], str), "marker_identity")
    phase = marker["phase"]
    require(isinstance(phase, str) and phase.endswith(("_begin", "_end")), "marker_phase")
    base, kind = phase.rsplit("_", 1)
    require(base in PHASES, "unknown_marker_phase")
    next_stack = list(stack)
    if kind == "begin":
        require(not stack or stack[-1] != "product_call", "observer_inside_product_call")
        require(base != "product_call" or not stack or stack[-1] in ("stage_normal_maintenance", "stage_normal_api_bootstrap"), "product_call_scope")
        next_stack.append(base)
    else:
        require(bool(next_stack) and next_stack[-1] == base, "marker_unbalanced")
        next_stack.pop()
    return next_stack


def publish(path, value):
    raw = (json.dumps(value, sort_keys=True) + "\n").encode()
    temporary = path.with_name(path.name + ".pending")
    with temporary.open("xb") as target:
        os.chmod(temporary, 0o600)
        target.write(raw)
    os.link(temporary, path)  # Atomic visibility with exclusive destination.
    temporary.unlink()


class Observer:
    def __init__(self, binding, stage, markers, boundary):
        self.binding, self.stage, self.markers, self.boundary = binding, stage, markers, boundary
        self.finished = threading.Event()
        self.ready = threading.Event()
        self.active = False
        self.failure = None
        self.samples, self.bytes, self.marker_count = 0, 0, 0
        self.previous = None
        self.stack = []
        self.phase = "observer_start"
        self.first_memory, self.last_memory = None, None
        self.threads = []
        self.phases = []
        self.last_marker_elapsed = -1

    def raw_memory(self):
        self.check_owned()
        directory = Path(self.binding["directory"])
        raw = {name: read(directory / name) for name in REQUIRED}
        peak = directory / "memory.swap.peak"
        require(not peak.is_symlink(), "observer_symlink")
        raw[peak.name] = read(peak) if peak.exists() else None
        parse_memory(raw)
        return raw

    def refuse(self, code):
        if self.failure is None:
            self.failure = code
        self.finished.set()
        self.ready.set()

    def check_owned(self):
        require(proc_identity(self.binding["pid"]) == {k:self.binding[k] for k in ("start_ticks", "cgroup_raw")}, "owned_pid_or_cgroup_changed")

    def sample_loop(self):
        try:
            with (self.stage / "memory-samples.jsonl").open("xb") as target:
                next_tick = time.monotonic()
                while not self.finished.is_set():
                    raw = self.raw_memory()
                    sample = {"sequence": self.samples + 1, "utc": datetime.now(timezone.utc).isoformat(), "monotonic_ns":time.monotonic_ns(), "phase":self.phase, "raw":raw}
                    memory = validate_sample(self.previous, sample)
                    encoded = (json.dumps(sample, sort_keys=True) + "\n").encode()
                    require(self.samples < MAX_SAMPLES and self.bytes + len(encoded) <= MAX_BYTES, "sample_output_cap")
                    target.write(encoded)
                    target.flush()
                    self.samples += 1
                    self.bytes += len(encoded)
                    self.previous, self.last_memory = sample, memory
                    if self.first_memory is None:
                        self.first_memory = memory
                    self.ready.set()
                    next_tick += INTERVAL
                    self.finished.wait(max(0, next_tick - time.monotonic()))
        except (OSError, ValueError, KeyError, TypeError) as error:
            self.refuse(str(error) if isinstance(error, ValueError) else "sample_read_failed")

    def marker_loop(self):
        try:
            sequence = 1
            while not self.finished.is_set():
                path = self.markers / ("%03d.request.json" % sequence)
                if not path.exists():
                    self.finished.wait(.005)
                    continue
                require(sequence <= MAX_MARKERS, "marker_count_cap")
                self.check_owned()
                marker = decode(read(path, 4096))
                stack = validate_marker(marker, sequence, self.stack)
                require(marker["elapsed_ns"] > self.last_marker_elapsed, "marker_clock_order")
                self.phase = marker["phase"]
                start = time.monotonic_ns()
                before = self.raw_memory()
                snapshot = self.boundary(marker)
                after = self.raw_memory()
                require(not self.failure, "sampler_failed_during_boundary")
                publish(self.markers / ("%03d.ack.json" % sequence), {"sequence":sequence,"pass":True,"scope":stack,"memory_begin_raw":before,"memory_end_raw":after,"observer_boundary_ns":time.monotonic_ns()-start,"snapshot":snapshot})
                self.stack, self.marker_count = stack, sequence
                self.phases.append(marker["phase"])
                self.last_marker_elapsed = marker["elapsed_ns"]
                sequence += 1
        except (OSError, ValueError, KeyError, TypeError, AssertionError) as error:
            self.refuse(str(error) if isinstance(error, ValueError) else "boundary_failed")

    def start(self):
        self.active = True
        self.threads = [threading.Thread(target=self.sample_loop, daemon=True), threading.Thread(target=self.marker_loop, daemon=True)]
        for thread in self.threads:
            thread.start()
        require(self.ready.wait(5) and not self.failure, "initial_sample_failed")

    def stop(self):
        self.finished.set()
        deadline = time.monotonic() + 50
        remaining = 50
        for thread in self.threads:
            thread.join(max(0, remaining))  # One total shutdown allowance, not the 30s marker ACK.
            remaining = deadline - time.monotonic()
        if any(thread.is_alive() for thread in self.threads):
            self.failure = self.failure or "observer_thread_not_joined"
        self.active = False

    def result(self):
        oom = self.first_memory is not None and self.last_memory is not None and any(self.last_memory["memory.events"][k] > self.first_memory["memory.events"][k] for k in ("oom", "oom_kill"))
        requests = sorted(self.markers.glob("*.request.json"))
        required = {"stage_" + s + "_begin" for s in ("source_ttl", "foreground_dml", "held_fence", "persisted_audit", "foreground_maintenance", "normal_maintenance", "normal_api_bootstrap")}
        complete = self.marker_count > 0 and len(requests) == self.marker_count and not self.stack and self.phase == "all_fixture_cleanup_end" and all(self.phases.count(p) == 1 for p in required | {"workload_setup_begin", "report_serialization_begin", "all_fixture_cleanup_begin"})
        joined = not any(thread.is_alive() for thread in self.threads)
        return {"pass": not self.failure and joined and complete and self.samples > 1 and not oom,
                "failure":self.failure,"markers_complete":complete,"samples":self.samples,"bytes":self.bytes,
                "marker_count":self.marker_count,"binding":self.binding,"first":self.first_memory,"last":self.last_memory,"threads_joined":joined,
                "oom_counter_increased":oom,"interval_ms":100,"maximum_gap_ms":200,"overhead_is_not_performance_neutral":True}
