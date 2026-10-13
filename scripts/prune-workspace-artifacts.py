#!/usr/bin/env python3
"""Prune reproducible local diagnostics; dry run unless --apply is supplied."""
import argparse
import json
import time
from pathlib import Path


def candidates(root, cutoff):
    for path in root.rglob('*'):
        if path.is_symlink() or any(part in {'.git', 'backups', 'migration-20261005'} for part in path.relative_to(root).parts):
            continue
        if not path.is_file() or any(parent.is_symlink() for parent in path.parents):
            continue
        stat = path.stat()
        if stat.st_size < 1024 * 1024 or stat.st_mtime > cutoff:
            continue
        generated = path.suffix.lower() in {'.mp4', '.mkv', '.mov', '.webm', '.wav', '.pcm', '.rgb', '.zip', '.gz', '.zst', '.tar', '.bin', '.media', '.jsonl', '.json'}
        if not generated:
            with path.open('rb') as file:
                generated = file.read(4) == b'\x7fELF'
        if generated:
            yield path, stat


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--root', type=Path, required=True, help='Explicit local reports directory, never production storage')
    parser.add_argument('--older-than-days', type=float, default=7)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    root = args.root
    if args.older_than_days < 0 or root.is_symlink() or root.name != 'reports' or not root.is_dir():
        parser.error('--root must be an existing, non-symlink reports directory; age must be nonnegative')
    root = root.resolve()
    if root == Path('/reports') or root.is_relative_to('/opt'):
        parser.error('Refusing system or production directories')
    count = size = 0
    for path, before in candidates(root, time.time() - args.older_than_days * 86400):
        if args.apply:
            current = path.lstat()
            if path.is_symlink() or (current.st_ino, current.st_size, current.st_mtime_ns) != (before.st_ino, before.st_size, before.st_mtime_ns):
                continue
            path.unlink()
        count += 1
        size += before.st_size
    print(json.dumps({'applied': args.apply, 'files': count, 'bytes': size}))


if __name__ == '__main__':
    main()
