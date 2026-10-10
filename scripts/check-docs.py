#!/usr/bin/env python3
"""Check repository-local Markdown links and heading anchors without networking."""
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

ROOT = Path(__file__).resolve().parent.parent
SKIP = {'.git', 'bin', 'dist', 'node_modules', '.venv'}


def prose(path):
    return re.sub(r'^```.*?^```\s*$', '', path.read_text(encoding='utf-8'), flags=re.M | re.S)


def anchors(path):
    result, seen = set(), {}
    for heading in re.findall(r'^#{1,6}\s+(.+?)\s*#*$', prose(path), flags=re.M):
        heading = re.sub(r'\[([^]]+)\]\([^)]*\)', r'\1', heading)
        slug = re.sub(r'[^\w\- ]', '', heading.lower(), flags=re.UNICODE).replace(' ', '-')
        count = seen.get(slug, 0)
        seen[slug] = count + 1
        result.add(slug + (f'-{count}' if count else ''))
    result.update(re.findall(r'<a\s+(?:id|name)=["\']([^"\']+)', prose(path)))
    return result


errors, checked = [], 0
for path in sorted(ROOT.rglob('*.md')):
    if set(path.relative_to(ROOT).parts) & SKIP:
        continue
    for raw in re.findall(r'!?\[[^\]\n]*\]\((<[^>]+>|[^)\s]+)(?:\s+"[^"]*")?\)', prose(path)):
        raw = raw.strip('<>')
        parsed = urlsplit(raw)
        if parsed.scheme or parsed.netloc:
            continue
        target = (path.parent / unquote(parsed.path)).resolve() if parsed.path else path
        checked += 1
        if not target.exists():
            errors.append(f'{path.relative_to(ROOT)}: missing target {raw}')
        elif parsed.fragment and target.suffix == '.md' and unquote(parsed.fragment) not in anchors(target):
            errors.append(f'{path.relative_to(ROOT)}: missing heading {raw}')
if errors:
    print('\n'.join(errors), file=sys.stderr)
    sys.exit(1)
print(f'Checked {checked} local documentation links.')
