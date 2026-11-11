#!/usr/bin/env python3
"""Run one shard of the complete race suite (`make test-full`) in CI.

Shard 0 runs every package except internal/app. Shards 1..N split the top-level
tests of internal/app, the longest package, by name order so that together the
shards run exactly the tests that `go test -race ./...` runs.
"""
import argparse
import os
import re
import subprocess
import sys

MODULE_APP = 'github.com/Kay0k1/cutmyvideo-core/internal/app'
TOP_LEVEL = re.compile(r'(Test|Fuzz|Example)\w*')


def shard_names(names, shard, count):
    """Names for app shard 1..count; together the shards partition `names`."""
    if not 1 <= shard <= count:
        raise ValueError('app shard out of range')
    return sorted(set(names))[shard - 1::count]


def selector(names):
    if not names:
        raise ValueError('empty race shard')
    return '^(' + '|'.join(re.escape(name) for name in names) + ')$'


def go(*args, env, capture=False):
    command = ['go', *args]
    print('+', ' '.join(command if len(' '.join(command)) < 400 else command[:8] + ['...']), flush=True)
    if capture:
        return subprocess.run(command, env=env, check=True, stdout=subprocess.PIPE, text=True).stdout
    return subprocess.run(command, env=env).returncode


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--shard', type=int, required=True, help='0 for other packages, 1..N for internal/app')
    parser.add_argument('--app-shards', type=int, required=True)
    args = parser.parse_args()
    env = dict(os.environ, CUTMY_REQUIRE_CONTRACT='1', CUTMY_CONTRACT_PYTHON=os.environ.get('PYTHON', 'python3'))
    flags = ['test', '-race', '-count=1', '-timeout=25m', '-v']
    if args.shard == 0:
        packages = [p for p in go('list', './...', env=env, capture=True).split() if p != MODULE_APP]
        if not packages:
            raise SystemExit('no packages outside internal/app')
        return go(*flags, *packages, env=env)
    listed = go('test', '-race', '-count=1', '-list', '.', './internal/app', env=env, capture=True).split()
    names = [name for name in listed if TOP_LEVEL.fullmatch(name)]
    chosen = shard_names(names, args.shard, args.app_shards)
    print(f'internal/app shard {args.shard}/{args.app_shards}: {len(chosen)} of {len(names)} top-level tests', flush=True)
    return go(*flags, './internal/app', '-run', selector(chosen), env=env)


if __name__ == '__main__':
    sys.exit(main())
