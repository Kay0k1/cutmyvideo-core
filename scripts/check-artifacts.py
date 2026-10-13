#!/usr/bin/env python3
"""Keep generated diagnostic payloads out of the tracked source tree."""
import re
import subprocess
from pathlib import Path

root = Path(__file__).resolve().parents[1]
files = subprocess.check_output(['git', 'ls-files', '-z'], cwd=root).decode().split('\0')
bad = []
for name in filter(None, files):
    path = root / name
    if not path.is_file() or not re.match(r'^(benchmarks|verification)/20\d\d-', name):
        continue
    if '/raw/' in name or path.suffix in {'.zip', '.gz', '.jsonl', '.mp4', '.mkv', '.webm'} or path.stat().st_size > 65536:
        bad.append(name)
if bad:
    raise SystemExit('Keep generated diagnostics outside Git:\n' + '\n'.join(bad))
print('Artifact policy passed')
