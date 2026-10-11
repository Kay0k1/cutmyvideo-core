#!/usr/bin/env python3
"""Observe owned FFmpeg/FFprobe lifetimes during one HTTP workflow on Linux.

This is a separate diagnostic run, not the paired performance benchmark. The
observer samples /proc every 20 ms and never captures argv or environment data.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import threading
import time


def main():
    p = argparse.ArgumentParser()
    p.add_argument('--url', required=True)
    p.add_argument('--api-pid', type=int, required=True)
    p.add_argument('--worker-pid', type=int, required=True)
    p.add_argument('--fixture', type=Path, required=True)
    p.add_argument('--binary', type=Path, required=True)
    p.add_argument('--revision', required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--media', type=Path, required=True)
    a = p.parse_args()
    spec = importlib.util.spec_from_file_location('workflow_profile', Path(__file__).with_name('stage-workflow-profile.py'))
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    parents = {a.api_pid: 'api', a.worker_pid: 'worker'}
    for pid in parents:
        assert (Path('/proc') / str(pid) / 'comm').read_text().strip() == 'cutmy', 'observe only explicitly owned services'
    ticks = os.sysconf('SC_CLK_TCK')
    stop = threading.Event()
    known = {}
    events = []
    origin = time.monotonic()

    def mark(name, instant):
        events.append({'event': name, 'elapsed_s': instant-origin})

    def observe():
        active = set()
        for path in Path('/proc').iterdir():
            if not path.name.isdigit():
                continue
            try:
                name = (path/'comm').read_text(errors='replace').strip()
                if name not in ('ffmpeg', 'ffprobe'):
                    continue
                raw = (path/'stat').read_text().rsplit(')', 1)[1].split()
                parent, born = int(raw[1]), int(raw[19])
                if parent not in parents:
                    continue
                cpu = (int(raw[11]) + int(raw[12])) / ticks
                key = (int(path.name), born)
            except (FileNotFoundError, ProcessLookupError, PermissionError, IndexError, ValueError):
                continue
            now = time.monotonic()-origin
            if key not in known:
                known[key] = {'pid': key[0], 'parent_role': parents[parent], 'tool': name,
                              'first_seen_s': now, 'last_seen_s': now, 'cpu_s_last_observed': cpu}
            else:
                known[key]['last_seen_s'] = now
                known[key]['cpu_s_last_observed'] = cpu
            active.add(key)
        now = time.monotonic()-origin
        for key, record in known.items():
            if key not in active and 'exit_observed_s' not in record:
                record['exit_observed_s'] = now

    def observer():
        while not stop.is_set():
            observe()
            stop.wait(.02)
        observe()

    thread = threading.Thread(target=observer, daemon=True)
    thread.start()
    try:
        record = helper.run_sample(a.url, a.fixture.read_bytes(), 1, a.media, mark)
    finally:
        stop.set()
        thread.join()
    tools = sorted(known.values(), key=lambda r: r['first_seen_s'])
    for item in tools:
        item['approx_wall_ms'] = 1000 * (item.get('exit_observed_s', item['last_seen_s']) - item['first_seen_s'])
    boundaries = {event['event']: event['elapsed_s'] for event in events}
    phases = [('upload', 'upload_started', 'upload_received'),
              ('first_preview', 'preview_started', 'preview_received'),
              ('job_through_download', 'job_requested', 'artifact_received')]
    costs = {}
    for phase, begin, end in phases:
        totals = {'ffmpeg_wall_ms': 0., 'ffprobe_wall_ms': 0.}
        for tool in tools:
            overlap = max(0., min(tool.get('exit_observed_s', tool['last_seen_s']), boundaries[end])
                          - max(tool['first_seen_s'], boundaries[begin]))
            totals[tool['tool']+'_wall_ms'] += overlap*1000
        costs[phase] = totals
    result = {'source_revision': a.revision, 'binary': helper.binary_metadata(a.binary),
              'environment': helper.environment_metadata(), 'workflow': record, 'events': events,
              'tools': tools, 'approx_phase_tool_wall_ms': costs,
              'method': 'Separate instrumented workflow; 20 ms requested /proc sampling period; reads tool names, parent PID and process CPU stats only. No argv, environment or URL/cookie data captured. Lifetimes are approximate; tools shorter than one polling period can be missed, and last observed CPU excludes final unsampled work. Observer overhead can affect these workflow timings; use paired uninstrumented report for speed comparison.'}
    a.output.parent.mkdir(parents=True, exist_ok=True)
    a.output.write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(costs), flush=True)


if __name__ == '__main__':
    main()
