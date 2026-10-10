#!/usr/bin/env python3
"""Collect exact runtime dependency licenses; --check refuses stale notices."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent


def objects(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        raw = raw.lstrip()
        value, end = decoder.raw_decode(raw)
        yield value
        raw = raw[end:]


modules = {}
for target_os in ('linux', 'darwin', 'windows'):
    import os
    env = dict(os.environ, CGO_ENABLED='0', GOOS=target_os, GOARCH='amd64')
    output = subprocess.check_output(['go', 'list', '-mod=readonly', '-deps', '-json', './cmd/cutmy'], cwd=ROOT, env=env, text=True)
    for package in objects(output):
        module = package.get('Module', {})
        if module and not module.get('Main'):
            modules[module['Path']] = module

goroot = Path(subprocess.check_output(['go', 'env', 'GOROOT'], text=True).strip())
sections = [
    '# Third-party notices\n',
    'The CLI includes the Go runtime/standard library and the Go modules below. '
    'License and patent texts are copied verbatim from the selected dependencies. '
    'Regenerate with `python3 scripts/update-notices.py` after dependency changes.\n',
    'FFmpeg, ffprobe, yt-dlp, Node.js and PostgreSQL are external to CLI archives. '
    'Docker images include additional system/media software under its own terms; '
    'retain the image distribution\'s license/source information when redistributing.\n',
    '## Go runtime and standard library\n',
]
for name in ('LICENSE', 'PATENTS'):
    sections.append(f'### {name}\n\n```text\n{(goroot / name).read_text().rstrip()}\n```\n')
sections.append('The Go-vendored `golang.org/x/crypto`, `golang.org/x/net`, '
                '`golang.org/x/sys` and `golang.org/x/text` carry the same Go '
                'Authors BSD license and patent grant reproduced above.\n')
for module_path, module in sorted(modules.items()):
    sections.append(f'## {module_path} {module["Version"]}\n')
    found = False
    for name in ('LICENSE', 'LICENSE.txt', 'COPYING', 'NOTICE', 'PATENTS'):
        license_path = Path(module['Dir']) / name
        if license_path.is_file():
            found = True
            sections.append(f'### {name}\n\n```text\n{license_path.read_text().rstrip()}\n```\n')
    if not found:
        sys.exit(f'No license file found for runtime module {module_path}')
result = '\n'.join(sections)
target = ROOT / 'THIRD_PARTY_NOTICES.md'
if '--check' in sys.argv:
    if not target.exists() or target.read_text() != result:
        sys.exit('THIRD_PARTY_NOTICES.md is stale; run python3 scripts/update-notices.py.')
    print(f'Checked notices for Go and {len(modules)} runtime modules.')
else:
    target.write_text(result, encoding='utf-8')
    print(f'Updated notices for Go and {len(modules)} runtime modules.')
