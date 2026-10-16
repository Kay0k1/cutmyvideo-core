#!/usr/bin/env python3
"""Compare two completed postwindow final fingerprints without native execution."""
import importlib.util
import json
import os
from pathlib import Path
import re
import sys

HERE = Path(__file__).resolve().parent


def module(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    value = importlib.util.module_from_spec(spec); spec.loader.exec_module(value)
    return value


def evidence(driver, input_path):
    config, root, _, _ = driver.input_document(input_path)
    driver.owned_run_parent(root)
    public = driver.read(root / 'public-receipt.json', private=True)
    oracle = driver.read(root / 'content-oracle-receipt.json', private=True)
    private_path = root / 'full-decoded-fingerprints.json'
    private = driver.read(private_path, private=True)
    driver.require(oracle.get('passed') is True and private.get('complete') is True and
                   oracle['timed_receipt_sha256'] == private['timed_receipt_sha256'] == driver.file_hash(root / 'public-receipt.json', 4 << 20)
                   and oracle['private_fingerprints_sha256'] == driver.file_hash(private_path, 4 << 20), 'paired_evidence_binding')
    driver.require(public['controller_input_sha256'] == driver.file_hash(input_path, 4 << 20) and
                   private['source_sha256'] == driver.SOURCE_SHA and private['fixture_sha256'] == driver.FIXTURE_SHA,
                   'paired_frozen_input_binding')
    return config, public, oracle, private


def main(left_path, right_path, output):
    driver = module('first_draft_driver', HERE / 'run-python.py')
    full = module('full_decode', HERE / 'full-decode.py')
    a_config, a_public, a_oracle, a = evidence(driver, left_path)
    b_config, b_public, b_oracle, b = evidence(driver, right_path)
    comparison = full.compare_final(a, b)
    driver.require(output.is_absolute() and output.parent in (driver.BASE / 'proofs', driver.REMOTE_BASE / 'proofs')
                   and not output.exists() and not output.parent.is_symlink(), 'new_owned_public_comparison_required')
    result = {'schema_version': 1, 'record_kind': 'paired_final_decoded_fingerprints', 'passed': True,
              'roles': [a_config['role'], b_config['role']], 'expected_server_commits_from_controller':
              [a_public['expected_server_commit_from_controller'], b_public['expected_server_commit_from_controller']],
              'source_sha256': driver.SOURCE_SHA, 'fixture_sha256': driver.FIXTURE_SHA,
              'left_oracle_sha256': driver.file_hash(Path(a_config['run_dir']) / 'content-oracle-receipt.json', 4 << 20),
              'right_oracle_sha256': driver.file_hash(Path(b_config['run_dir']) / 'content-oracle-receipt.json', 4 << 20),
              'comparer_sha256': driver.file_hash(Path(__file__).resolve(), 1 << 20), **comparison}
    driver.atomic(output, result)
    print(json.dumps(result, sort_keys=True, ensure_ascii=True))


if __name__ == '__main__':
    os.umask(0o077)
    try:
        if len(sys.argv) != 4: raise ValueError('three_private_comparison_arguments_required')
        main(*(Path(x).absolute() for x in sys.argv[1:]))
    except Exception as error:
        code = getattr(error, 'code', 'final_fingerprint_comparison_failed')
        if not isinstance(code, str) or not re.fullmatch('[a-z0-9_]{1,100}', code): code = 'final_fingerprint_comparison_failed'
        print(json.dumps({'passed': False, 'code': code, 'exception_type': type(error).__name__}), file=sys.stderr)
        raise SystemExit(1)
