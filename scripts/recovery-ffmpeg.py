#!/usr/bin/env python3
"""Acceptance-only fault gate. All media is processed by the real FFmpeg."""

import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import time


def main():
    args = sys.argv[1:]
    gate_path = Path(os.environ["CUTMY_RECOVERY_GATE"])
    try:
        gate = json.loads(gate_path.read_text())
    except FileNotFoundError:
        gate = {}
    target = Path(args[-1]).name if args else ""
    if "-progress" not in args or target != gate.get("target"):
        os.execv(os.environ["CUTMY_RECOVERY_REAL_FFMPEG"], ["ffmpeg", *args])

    # Pace just the selected export to make interruption deterministic. Its
    # output remains real FFmpeg output; no database or media result is faked.
    args.insert(args.index("-i"), "-re")
    child = subprocess.Popen([os.environ["CUTMY_RECOVERY_REAL_FFMPEG"], *args])
    time.sleep(0.25)
    if child.poll() is not None:
        return child.returncode or 1
    os.kill(child.pid, signal.SIGSTOP)
    marker = Path(gate["marker"])
    temporary = marker.with_suffix(".tmp")
    temporary.write_text(json.dumps({"pid": os.getpid(), "pgid": os.getpgrp(),
                                     "child_pid": child.pid, "target": target}))
    temporary.replace(marker)
    return child.wait()


if __name__ == "__main__":
    sys.exit(main())
