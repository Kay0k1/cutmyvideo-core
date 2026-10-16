#!/usr/bin/env python3
"""Seal tested SDK packages and authenticate their separate release signature.

This does not modify the eight-file CLI archive or its existing trust protocol.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import sys
import tempfile
import zipfile

import release_manifest as m

HERE = Path(__file__).resolve().parent
REPOSITORY = 'Kay0k1/cutmyvideo-core'
WORKFLOW = REPOSITORY + '/.github/workflows/release.yml'
PREDICATE = 'https://slsa.dev/provenance/v1'
MANIFEST = 'SDK-MANIFEST.json'
SIGNATURE = 'sdk-provenance.sigstore.json'
MAX_PACKAGE = 16 * 1024 * 1024
MAX_JSON = 2 * 1024 * 1024


def module(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    value = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(value)
    return value


def package_names(version):
    m.require(type(version) is str and m.VERSION.fullmatch('v' + version), 'Invalid SDK version.')
    return {'wheel': f'cutmyvideo-{version}-py3-none-any.whl',
            'sdist': f'cutmyvideo-{version}.tar.gz',
            'npm': f'cutmyvideo-sdk-{version}.tgz'}


def release_names(version):
    m.release_version(version)
    return set(package_names(version[1:]).values()) | {MANIFEST, SIGNATURE}


def has_sdk_release(version):
    m.release_version(version)
    match = m.VERSION.fullmatch(version)
    return tuple(map(int, match.group(1, 2, 3))) >= (1, 4, 0)


def regular(path, limit):
    path = Path(path)
    info = path.lstat()
    m.require(stat.S_ISREG(info.st_mode) and 0 < info.st_size <= limit, 'Missing, nonregular or oversized SDK file.')
    return path.read_bytes()


def load_manifest(directory, source, expected_sha256=None, *, signed=False):
    directory = Path(directory)
    raw = regular(directory / MANIFEST, MAX_JSON)
    if expected_sha256 is not None:
        m.require(m.HASH.fullmatch(expected_sha256) and m.digest(raw) == expected_sha256, 'SDK manifest differs from sealed identity.')
    value = json.loads(raw)
    expected_keys = {'schema_version', 'repository', 'source_commit', 'source_tree', 'source_sha256',
                     'version', 'mode', 'publishable', 'package_version', 'packages', 'acceptance'}
    m.require(type(value) is dict and set(value) == expected_keys and value['schema_version'] == 1
              and raw == m.canonical(value), 'Unsupported/noncanonical SDK manifest.')
    m.require(value['repository'] == REPOSITORY and value['source_commit'] == source['commit']
              and value['source_tree'] == source['tree'] and value['source_sha256'] == m.digest(m.canonical(source))
              and value['version'] == source['version'] and value['mode'] == source['mode']
              and value['publishable'] is source['publishable'], 'SDK source identity differs.')
    names = package_names(value['package_version'])
    if source['publishable']:
        m.require(source['version'] == 'v' + value['package_version'], 'Release SDK version differs from tag.')
    rows = value['packages']
    m.require(type(rows) is list and len(rows) == 3 and {row.get('kind') for row in rows} == set(names), 'Incomplete SDK package set.')
    subjects = {MANIFEST: m.digest(raw)}
    for row in rows:
        m.require(type(row) is dict and set(row) == {'kind', 'name', 'sha256', 'bytes'}
                  and row['name'] == names[row['kind']] and m.HASH.fullmatch(row['sha256'])
                  and type(row['bytes']) is int and 0 < row['bytes'] <= MAX_PACKAGE, 'Invalid SDK package identity.')
        data = regular(directory / row['name'], MAX_PACKAGE)
        m.require(len(data) == row['bytes'] and m.digest(data) == row['sha256'], 'SDK package bytes differ.')
        subjects[row['name']] = row['sha256']
    acceptance = value['acceptance']
    m.require(type(acceptance) is dict and set(acceptance) == {'installed_consumers', 'sdk_checks_sha256', 'required_tests'}
              and acceptance['installed_consumers'] is True and m.HASH.fullmatch(acceptance['sdk_checks_sha256'])
              and type(acceptance['required_tests']) is list and set(acceptance['required_tests']) == required_tests()
              and len(acceptance['required_tests']) == len(required_tests()), 'Missing installed SDK/HTTP/worker acceptance claim.')
    if signed:
        regular(directory / SIGNATURE, MAX_JSON)
    return value, subjects


def required_tests():
    top = {'TestInstalledSDKLocalWorkflow', 'TestInstalledSDKCollectionLocalWorkflow',
           'TestInstalledSDKHTTPWorkflow', 'TestInstalledSDKCollectionHTTPWorkflow',
           'TestInstalledSDKAgentProjectWorkflow'}
    return top | {name + '/' + sdk for name in top for sdk in ('python', 'typescript')}


def seal(directory, evidence, source, repo):
    """Copy the exact already-installed distributions; never rebuild them here."""
    directory, evidence, repo = Path(directory), Path(evidence), Path(repo)
    m.require(not directory.exists(), 'SDK bundle destination must be new.')
    receipt_raw = regular(evidence / 'SDK-CHECKS.json', MAX_JSON)
    receipt = json.loads(receipt_raw)
    m.require(receipt.get('installed_consumers') is True and set(receipt.get('required_tests', [])) == required_tests(),
              'Full installed SDK HTTP/worker acceptance is required.')
    frozen = receipt.get('source_inputs_sha256')
    m.require(type(frozen) is dict and frozen, 'SDK evidence has no frozen source inputs.')
    for name, digest in frozen.items():
        m.require(name in source['files'] and source['files'][name]['sha256'] == digest,
                  'SDK checks were not run against the resolved source.')
    wheels = list((evidence / 'python-dist').glob('*.whl'))
    sdists = list((evidence / 'python-dist').glob('*.tar.gz'))
    tarballs = list(evidence.glob('*.tgz'))
    m.require(len(wheels) == len(sdists) == len(tarballs) == 1, 'Expected one tested distribution of each kind.')
    metadata = json.loads((repo / 'sdk/typescript/package.json').read_bytes())
    version = metadata['version']
    names = package_names(version)
    paths = {'wheel': wheels[0], 'sdist': sdists[0], 'npm': tarballs[0]}
    rows = []
    for kind, path in paths.items():
        data = regular(path, MAX_PACKAGE)
        m.require(path.name == names[kind] and receipt.get({'wheel': 'python_wheel_sha256', 'sdist': 'python_sdist_sha256', 'npm': 'npm_tarball_sha256'}[kind]) == m.digest(data),
                  'SDK package does not match its installed-consumer receipt.')
        rows.append({'kind': kind, 'name': path.name, 'sha256': m.digest(data), 'bytes': len(data)})
    directory.mkdir(parents=True)
    for kind, path in paths.items():
        shutil.copyfile(path, directory / names[kind])
    value = {'schema_version': 1, 'repository': REPOSITORY, 'source_commit': source['commit'],
             'source_tree': source['tree'], 'source_sha256': m.digest(m.canonical(source)),
             'version': source['version'], 'mode': source['mode'], 'publishable': source['publishable'],
             'package_version': version, 'packages': sorted(rows, key=lambda row: row['kind']),
             'acceptance': {'installed_consumers': True, 'sdk_checks_sha256': m.digest(receipt_raw),
                            'required_tests': sorted(required_tests())}}
    (directory / MANIFEST).write_bytes(m.canonical(value))
    load_manifest(directory, source)
    return value


def extract_sdk(archive, destination, source, *, signed=False):
    fetch = module('release_sdk_fetch', 'release-fetch.py')
    fetch.preflight_zip(archive, 5 if signed else 4)
    destination = Path(destination)
    m.require(not destination.exists(), 'SDK destination must be new.')
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='.cutmy-sdk-fetch-', dir=destination.parent) as temporary:
        stage = Path(temporary)
        with zipfile.ZipFile(archive) as zipped:
            infos = zipped.infolist()
            m.require(len(infos) == (5 if signed else 4) and len({i.filename for i in infos}) == len(infos), 'Unexpected SDK artifact members.')
            for item in infos:
                m.require(Path(item.filename).name == item.filename and not item.is_dir()
                          and stat.S_IFMT(item.external_attr >> 16) in (0, stat.S_IFREG)
                          and not item.flag_bits & 1 and item.file_size <= MAX_PACKAGE
                          and item.compress_type in (zipfile.ZIP_STORED, zipfile.ZIP_DEFLATED), 'Unsafe SDK artifact member.')
                data = zipped.read(item)
                m.require(len(data) == item.file_size, 'SDK artifact member size differs.')
                (stage / item.filename).write_bytes(data)
        value, _ = load_manifest(stage, source, signed=signed)
        expected = set(package_names(value['package_version']).values()) | {MANIFEST}
        if signed: expected.add(SIGNATURE)
        m.require({p.name for p in stage.iterdir()} == expected, 'SDK artifact has missing/extra members.')
        stage.rename(destination)


def verify_signed(directory, source, expected_sha256=None, *, expected_run, command=None):
    value, subjects = load_manifest(directory, source, expected_sha256, signed=True)
    m.require(source['publishable'] is True and source['mode'] == 'release', 'Candidate SDKs cannot carry a publication signature.')
    m.require(type(expected_run) is dict and expected_run.get('repository') == REPOSITORY
              and all(type(expected_run.get(name)) is int and expected_run[name] > 0 for name in ('run_id', 'run_attempt')),
              'SDK signatures require the exact archive run/attempt.')
    trust = module('release_sdk_trust', 'release-trust.py')
    consumer = module('release_sdk_consumer_policy', 'release-consumer.py')
    for name in sorted(subjects):
        argv = ['gh', 'attestation', 'verify', str(Path(directory) / name), '--bundle', str(Path(directory) / SIGNATURE),
                '--hostname', 'github.com', '--repo', REPOSITORY, '--signer-workflow', WORKFLOW,
                '--source-digest', source['commit'], '--signer-digest', source['commit'],
                '--source-ref', 'refs/tags/' + source['version'], '--deny-self-hosted-runners',
                '--predicate-type', PREDICATE, '--format', 'json']
        raw = command(argv) if command else subprocess.check_output(argv, timeout=60)
        m.require(len(raw) <= 16 * 1024 * 1024, 'SDK signature verification output exceeds budget.')
        verified = consumer.plain_json(raw, 16 * 1024 * 1024)
        trust.verified_statement(verified, subjects, PREDICATE)
        verification = verified[0]['verificationResult']
        certificate = verification.get('signature', {}).get('certificate', {})
        invocation = ('https://github.com/' + REPOSITORY + '/actions/runs/'
                      + str(expected_run['run_id']) + '/attempts/' + str(expected_run['run_attempt']))
        m.require(type(certificate) is dict and certificate.get('runInvocationURI') == invocation,
                  'SDK verified certificate differs from the exact archive run/attempt.')
        # Reuse the CLI consumer's exact source/tag/builder/invocation policy.
        consumer.provenance_identity(verification['statement']['predicate'], source, expected_run)
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument('command', choices=['build', 'fetch', 'subjects', 'verify-signed'])
    parser.add_argument('--repo', type=Path, default=HERE.parent)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--expected-source-sha256', required=True)
    parser.add_argument('--directory', type=Path, required=True)
    parser.add_argument('--evidence', type=Path)
    parser.add_argument('--expected-manifest-sha256')
    parser.add_argument('--artifact-id', type=int)
    parser.add_argument('--run-id', type=int)
    parser.add_argument('--run-attempt', type=int)
    parser.add_argument('--artifact-name')
    parser.add_argument('--expected-artifact-sha256')
    parser.add_argument('--signed', action='store_true')
    args = parser.parse_args()
    source = m.load_source(args.source, args.repo, args.expected_source_sha256)
    if args.command == 'build':
        m.require(args.evidence is not None and not args.evidence.exists(), 'New SDK evidence directory required.')
        with tempfile.TemporaryDirectory(prefix='cutmy-sdk-source-') as temporary:
            snapshot = m.materialize_source(args.repo, source, Path(temporary) / 'source')
            subprocess.run([sys.executable, str(snapshot / 'scripts/check-sdks.py'), '--http', '--output', str(args.evidence)], check=True, timeout=900)
            seal(args.directory, args.evidence, source, snapshot)
        m.validate_source(args.repo, source)
    elif args.command == 'fetch':
        m.require(args.evidence is not None, 'Fetch evidence directory required.')
        fetch = module('release_sdk_fetch', 'release-fetch.py')
        fetch.fetch(REPOSITORY, args.artifact_id, args.run_id, args.artifact_name, args.expected_artifact_sha256,
                    source, args.directory, args.evidence,
                    extractor=lambda archive, dest, src: extract_sdk(archive, dest, src, signed=args.signed))
    elif args.command == 'subjects':
        _, subjects = load_manifest(args.directory, source, args.expected_manifest_sha256)
        print(''.join(digest + '  ' + name + '\n' for name, digest in sorted(subjects.items())), end='')
    else:
        verify_signed(args.directory, source, args.expected_manifest_sha256,
                      expected_run={'repository': REPOSITORY, 'run_id': args.run_id, 'run_attempt': args.run_attempt})


if __name__ == '__main__':
    main()
