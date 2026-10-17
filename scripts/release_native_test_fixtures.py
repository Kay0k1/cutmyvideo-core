"""Test-only builders for synthetic native archive reports.

Policy assertions remain in the native and aggregate control suites. None of
these values are evidence of executing an actual binary.
"""
import importlib.util
from pathlib import Path

SPEC = importlib.util.spec_from_file_location('release_native', Path(__file__).with_name('release-native.py'))
n = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(n)


def source():
    return {'commit': 'a' * 40, 'tree': 'b' * 40, 'mode': 'candidate',
            'version': 'v0.0.0-candidate.g' + 'a' * 40, 'build_time': '2026-10-11T00:00:00Z',
            'toolchain': 'go1.27.2', 'sha256': 'c' * 64}


def receipt():
    return {'schema_version': 1, 'repository': 'owner/repository', 'run_id': 123,
            'artifact_id': 456, 'artifact_name': 'candidate-archives', 'artifact_sha256': 'f' * 64,
            'artifact_bytes': 100, 'source_commit': 'a' * 40, 'source_sha256': 'c' * 64}


def report(target_os='linux'):
    value = {'schema_version': 1, 'ok': True, 'required_groups': list(n.REQUIRED_GROUPS), 'errors': [],
             'target': {'os': target_os, 'arch': 'amd64'}, 'source': source(), 'manifest_sha256': 'd' * 64,
             'archive': {'name': 'candidate.tar.gz', 'sha256': 'e' * 64, 'bytes': 100},
             'binary': {'absolute_path': '/tmp/extracted/cutmy', 'bytes': 100,
                        'sha256_before': 'f' * 64, 'sha256_after': 'f' * 64},
             'artifact_receipt': dict(receipt(), sha256='e' * 64),
             'runtime_version': {'version': source()['version'], 'commit': source()['commit'],
                                 'build_time': source()['build_time'], 'go_version': 'go1.27.2',
                                 'os': target_os, 'arch': 'amd64'},
             'tools': {}, 'run': {'id': 'local', 'attempt': 'local', 'job': 'local', 'workflow_sha': 'local'}}
    value['groups'] = {group: {'status': 'pass', 'checks': [{'name': check, 'ok': True} for check in checks],
                               'evidence': {}} for group, checks in n.required_checks(target_os).items()}
    value['groups']['archive']['evidence']['binary_sha256'] = 'f' * 64
    return value
