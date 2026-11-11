#!/usr/bin/env python3
"""Choose the checks for one archive-workflow run without dropping coverage on releases.

Tags, nightly runs and manual verification always run the complete matrices and the
archive pipeline. A pull request runs the source checks on Linux, Apple silicon and
Windows; it packages and executes archives only when release inputs change, and it
blocks on new Go vulnerabilities only when its own dependencies or toolchain change.
"""
import json
import os
from pathlib import Path, PurePosixPath
import subprocess

DOCUMENTATION_FILES = {'README.md', 'README.en.md', 'CONTRIBUTING.md', 'SECURITY.md', 'CHANGELOG.md',
                       '.github/PULL_REQUEST_TEMPLATE.md'}
RELEASE_FILES = {'go.mod', 'go.sum', 'Makefile', 'LICENSE', 'CHANGELOG.md', 'THIRD_PARTY_NOTICES.md',
                 'docs/release-artifact.txt', 'scripts/package-release.py', 'scripts/release.sh',
                 'scripts/release_manifest.py', 'scripts/update-notices.py', 'scripts/recovery-acceptance.py',
                 'scripts/check-sdks.py', 'scripts/requirements-sdk-build.txt',
                 'scripts/testdata/release-process-helper.go'}
RELEASE_PREFIXES = ('cmd/cutmy/', 'sdk/', '.github/workflows/', 'scripts/release-', 'scripts/release_')
DEPENDENCY_FILES = {'go.mod', 'go.sum'}
FULL = {'checks': 'full', 'archives': True, 'vulnerabilities': 'blocking'}


def documentation(path):
    p = PurePosixPath(path)
    return (path in DOCUMENTATION_FILES or path.startswith('.github/ISSUE_TEMPLATE/')
            or (p.parts[0] == 'docs' and p.suffix == '.md'))


def release_input(path):
    return path in RELEASE_FILES or path.startswith(RELEASE_PREFIXES)


def scope(event, paths):
    """Return the run scope. Unknown events and missing diffs keep every check."""
    if event != 'pull_request' or paths is None:
        return dict(FULL)
    if paths and all(documentation(path) for path in paths):
        return {'checks': 'docs', 'archives': False, 'vulnerabilities': 'report'}
    return {'checks': 'pr', 'archives': not paths or any(release_input(path) for path in paths),
            'vulnerabilities': 'blocking' if not paths or DEPENDENCY_FILES & set(paths) else 'report'}


def pull_request_paths():
    """Files changed by the provider merge commit against the base it was merged into."""
    parents = subprocess.check_output(['git', 'show', '-s', '--format=%P', 'HEAD'], text=True).split()
    if len(parents) != 2:
        return None
    diff = subprocess.check_output(['git', 'diff', '--name-only', '--no-renames', '-z', parents[0], 'HEAD'])
    return [path.decode('utf-8', 'surrogateescape') for path in diff.split(b'\0') if path]


def main():
    event = os.environ['GITHUB_EVENT_NAME']
    paths = pull_request_paths() if event == 'pull_request' else None
    selected = scope(event, paths)
    with Path(os.environ['GITHUB_OUTPUT']).open('a', encoding='utf-8') as output:
        for key, value in selected.items():
            output.write(f'{key}={str(value).lower() if isinstance(value, bool) else value}\n')
    print(json.dumps({'event': event, 'changed': len(paths) if paths is not None else None, **selected}))


if __name__ == '__main__':
    main()
