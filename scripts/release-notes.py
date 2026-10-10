#!/usr/bin/env python3
"""Require a versioned changelog entry and print its GitHub release notes."""
from pathlib import Path
import re
import sys

version = sys.argv[1].removeprefix('v')
changelog = (Path(__file__).resolve().parent.parent / 'CHANGELOG.md').read_text(encoding='utf-8')
match = re.search(r'^## \[?' + re.escape(version) + r'\]?\s+-\s+\d{4}-\d{2}-\d{2}\s*\n(.*?)(?=^## |\Z)', changelog, flags=re.M | re.S)
if not match or not match.group(1).strip():
    sys.exit(f'CHANGELOG.md needs a nonempty "## {version} - YYYY-MM-DD" release entry.')
print(match.group(1).strip())
print('\nCLI archives require external FFmpeg/ffprobe. BUILD.json records the source revision; SHA256SUMS verifies downloads. Linux media tests run in CI; other targets are cross-built.')
